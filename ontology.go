package fusiondb

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"

	"github.com/dgraph-io/badger/v4"
)

type Degree int

const (
	DegreeUnknown    Degree = 0
	DegreePrimary    Degree = 1
	DegreeSecondary  Degree = 2
	DegreeTertiary   Degree = 3
	DegreeQuaternary Degree = 4
)

var predicateDegrees = map[string]Degree{
	"has_phone":        DegreeSecondary,
	"has_email":        DegreeSecondary,
	"owns_vehicle":     DegreeTertiary,
	"owns_property":    DegreeTertiary,
	"attended_college": DegreeTertiary,
	"referenced_in":    DegreeQuaternary,
	"involved_in":      DegreeQuaternary,
}

func GetDegree(predicate string) Degree {
	if d, ok := predicateDegrees[predicate]; ok {
		return d
	}
	return DegreeQuaternary // Default to least gravity
}

type UFLManifestPII struct {
	Entity UFLEntityPII
}

type UFLEntityPII struct {
	ID        string
	Tier      string            // "verified" | "unverified" | "knowledge"
	KV        map[string]string // document attributes
	Relations []UFLRelationPII
}

type UFLRelationPII struct {
	Predicate string
	Object    string
	Degree    int // 1 = public, 2 = PII
}

func encryptAES_GCM(plaintext []byte, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, 12)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)
	payload := make([]byte, 12+len(ciphertext))
	copy(payload[0:12], nonce)
	copy(payload[12:], ciphertext)
	return payload, nil
}

func decryptAES_GCM(payload []byte, key []byte) ([]byte, error) {
	if len(payload) < 13 {
		return nil, errors.New("payload too short")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := payload[0:12]
	ciphertext := payload[12:]
	return gcm.Open(nil, nonce, ciphertext, nil)
}

func hmacSHA256(data string, key []byte) string {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return hex.EncodeToString(h.Sum(nil))
}

func buildPIIReverseKey(hashedValue string) []byte {
	key := make([]byte, 2+len(hashedValue))
	key[0] = 0x03
	key[1] = 0x01
	copy(key[2:], []byte(hashedValue))
	return key
}

// SecureFuseEntity handles PII compliance with AES-256-GCM encryption at rest
// and HMAC-SHA256 graph key obfuscation for Degree 2 identifiers.
func SecureFuseEntity(txn *badger.Txn, manifest UFLManifestPII, secretKey, saltKey []byte) error {
	// STEP 1 — ENCRYPT KV BLOCK
	jsonBytes, err := json.Marshal(manifest.Entity.KV)
	if err != nil {
		return err
	}
	encryptedKV, err := encryptAES_GCM(jsonBytes, secretKey)
	if err != nil {
		return err
	}
	kvKey := append([]byte{tierByte(manifest.Entity.Tier)}, []byte(manifest.Entity.ID)...)
	if err := txn.Set(kvKey, encryptedKV); err != nil {
		return err
	}

	// STEP 2 — WRITE GRAPH TRIPLES WITH PII MASKING
	for _, relation := range manifest.Entity.Relations {
		var hashedObject string
		var isPII bool

		if relation.Degree == 2 {
			isPII = true
			hashedObject = hmacSHA256(relation.Object, saltKey)
		} else {
			isPII = false
			hashedObject = relation.Object
		}

		fwdKey := BuildGraphKey(0x00, manifest.Entity.ID, relation.Predicate, hashedObject)
		revKey := BuildGraphKey(0x01, hashedObject, relation.Predicate, manifest.Entity.ID)

		tierTag := tierByte(manifest.Entity.Tier)
		if err := txn.Set(fwdKey, []byte{tierTag}); err != nil {
			return err
		}
		if err := txn.Set(revKey, []byte{tierTag}); err != nil {
			return err
		}

		if isPII {
			revLookupKey := buildPIIReverseKey(hashedObject)
			encryptedOriginal, err := encryptAES_GCM([]byte(relation.Object), secretKey)
			if err != nil {
				return err
			}
			if err := txn.Set(revLookupKey, encryptedOriginal); err != nil {
				return err
			}
		}
	}

	return nil
}

// HydrateEntity decrypts the KV block and resolves masked graph relations.
func HydrateEntity(txn *badger.Txn, entityID, tier string, maxDegree int, secretKey, saltKey []byte) (map[string]interface{}, error) {
	// STEP 1 — FETCH AND DECRYPT KV BLOCK
	kvKey := append([]byte{tierByte(tier)}, []byte(entityID)...)
	item, err := txn.Get(kvKey)
	if err != nil {
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	var val []byte
	err = item.Value(func(v []byte) error {
		val = make([]byte, len(v))
		copy(val, v)
		return nil
	})
	if err != nil {
		return nil, err
	}

	plaintext, err := decryptAES_GCM(val, secretKey)
	if err != nil {
		return nil, errors.New("decryption failed: tampered data")
	}

	var kvMap map[string]string
	if err := json.Unmarshal(plaintext, &kvMap); err != nil {
		return nil, err
	}

	output := make(map[string]interface{})
	for k, v := range kvMap {
		output[k] = v
	}

	// STEP 2 — HYDRATE GRAPH RELATIONS
	relationsMap := make(map[int][]map[string]string)

	if maxDegree >= 1 {
		prefix := buildGraphPrefix(0x00, entityID)
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()

		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			k := it.Item().Key()
			_, predicate, object, ok := parseGraphKey(k)
			if !ok {
				continue
			}

			degree := int(GetDegree(predicate))
			if degree <= maxDegree {
				resolvedObject := object
				if degree == 2 {
					var err error
					resolvedObject, err = ReverseKVLookupByHashedIndex(txn, object, secretKey, saltKey)
					if err != nil {
						return nil, err
					}
				}

				rel := map[string]string{
					"predicate": predicate,
					"object":    resolvedObject,
				}
				relationsMap[degree] = append(relationsMap[degree], rel)

				// Also place at root-level of output
				output[predicate] = resolvedObject
			}
		}
	}

	// STEP 3 — ASSEMBLE OUTPUT
	output["relations"] = relationsMap

	return output, nil
}

// ReverseKVLookupByHashedIndex recovers the original PII value using the hashed value.
func ReverseKVLookupByHashedIndex(txn *badger.Txn, hashedValue string, secretKey, saltKey []byte) (string, error) {
	key := buildPIIReverseKey(hashedValue)
	item, err := txn.Get(key)
	if err != nil {
		if errors.Is(err, badger.ErrKeyNotFound) {
			return "", nil
		}
		return "", err
	}
	var decrypted []byte
	err = item.Value(func(val []byte) error {
		var decryptErr error
		decrypted, decryptErr = decryptAES_GCM(val, secretKey)
		return decryptErr
	})
	if err != nil {
		return "", err
	}
	return string(decrypted), nil
}

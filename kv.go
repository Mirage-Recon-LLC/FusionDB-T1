package fusiondb

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/dgraph-io/ristretto/v2"
)

type KVStore struct {
	db              *badger.DB
	cache           *ristretto.Cache[string, []byte]
	BadgerReadCount int64 // Instrumented counter for cache checks
	cfg             Config
}

type KnowledgeRecord struct {
	Agent      string         `json:"agent"`
	Type       string         `json:"type"`
	Query      string         `json:"query"`
	Result     string         `json:"result"`
	Correction string         `json:"correction"`
	Data       string         `json:"data,omitempty"`
	Meta       map[string]any `json:"meta,omitempty"`
}

func tierByte(tier string) byte {
	switch strings.ToLower(tier) {
	case "verified":
		return 0x10
	case "unverified":
		return 0x11
	case "knowledge":
		return 0x12
	default:
		return 0x11 // default to unverified; never silently promote.
	}
}

// StoreInTxn writes a KV record directly into the provided transaction and invalidates the cache.
func (kv *KVStore) StoreInTxn(txn *badger.Txn, tier string, key string, record KnowledgeRecord) error {
	fullKey := append([]byte{tierByte(tier)}, []byte(key)...)
	val, err := json.Marshal(record)
	if err != nil {
		return err
	}
	
	// Encrypt for compatibility with new spec if cfg is present
	if len(kv.cfg.MasterKey) == 32 {
		nonce, cipherText, err := encryptData(kv.cfg.MasterKey, val)
		if err == nil {
			metaMap := map[string]interface{}{
				"type": record.Type,
				"nonce": nonce,
				"payload": cipherText,
				"created_at": time.Now().Format(time.RFC3339),
			}
			val, _ = json.Marshal(metaMap)
		}
	}

	if kv.cache != nil {
		kv.cache.Del(string(fullKey))
	}
	return txn.Set(fullKey, val)
}

func encryptData(masterKey, plaintext []byte) ([]byte, []byte, error) {
	block, err := aes.NewCipher(masterKey)
	if err != nil { return nil, nil, err }
	gcm, err := cipher.NewGCM(block)
	if err != nil { return nil, nil, err }
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil { return nil, nil, err }
	return nonce, gcm.Seal(nil, nonce, plaintext, nil), nil
}

func decryptData(masterKey, nonce, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(masterKey)
	if err != nil { return nil, err }
	gcm, err := cipher.NewGCM(block)
	if err != nil { return nil, err }
	return gcm.Open(nil, nonce, ciphertext, nil)
}

func (kv *KVStore) Store(ctx context.Context, tier string, key string, record KnowledgeRecord) error {
	return safeUpdate(ctx, kv.db, func(txn *badger.Txn) error {
		return kv.StoreInTxn(txn, tier, key, record)
	})
}

func (kv *KVStore) Get(ctx context.Context, tier string, key string) (KnowledgeRecord, error) {
	fullKey := append([]byte{tierByte(tier)}, []byte(key)...)

	if kv.cache != nil {
		if val, found := kv.cache.Get(string(fullKey)); found {
			var record KnowledgeRecord
			if err := json.Unmarshal(val, &record); err == nil {
				return record, nil
			}
		}
	}

	atomic.AddInt64(&kv.BadgerReadCount, 1)

	var record KnowledgeRecord
	err := kv.db.View(func(txn *badger.Txn) error {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
		}

		item, err := txn.Get(fullKey)
		if err != nil {
			if errors.Is(err, badger.ErrKeyNotFound) {
				return ErrNotFound
			}
			return err
		}
		return item.Value(func(val []byte) error {
			if kv.cache != nil {
				valCopy := make([]byte, len(val))
				copy(valCopy, val)
				kv.cache.Set(string(fullKey), valCopy, int64(len(valCopy)))
			}
			
			// Try to decrypt if it looks like the new format
			var metaRaw map[string]json.RawMessage
			if err := json.Unmarshal(val, &metaRaw); err == nil {
				if nonceRaw, ok := metaRaw["nonce"]; ok && len(kv.cfg.MasterKey) == 32 {
					var nonce, payload []byte
					json.Unmarshal(nonceRaw, &nonce)
					json.Unmarshal(metaRaw["payload"], &payload)
					
					plain, err := decryptData(kv.cfg.MasterKey, nonce, payload)
					if err == nil {
						val = plain
					}
				}
			}
			
			return json.Unmarshal(val, &record)
		})
	})
	return record, err
}

// ScanFilterStream calls the 'processor' function for every record matching the criteria.
// Returning 'false' from the processor stops the iteration early.
func (kv *KVStore) ScanFilterStream(ctx context.Context, tier string, match func(KnowledgeRecord) bool, processor func(KnowledgeRecord) bool) error {
	prefix := []byte{tierByte(tier)}
	return kv.db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()

		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			if ctx != nil {
				if err := ctx.Err(); err != nil {
					return err
				}
			}

			var rec KnowledgeRecord
			err := it.Item().Value(func(val []byte) error {
				// Try to decrypt if it looks like the new format
				var metaRaw map[string]json.RawMessage
				if err := json.Unmarshal(val, &metaRaw); err == nil {
					if nonceRaw, ok := metaRaw["nonce"]; ok && len(kv.cfg.MasterKey) == 32 {
						var nonce, payload []byte
						json.Unmarshal(nonceRaw, &nonce)
						json.Unmarshal(metaRaw["payload"], &payload)
						
						plain, err := decryptData(kv.cfg.MasterKey, nonce, payload)
						if err == nil {
							val = plain
						}
					}
				}
				return json.Unmarshal(val, &rec)
			})
			if err == nil && match(rec) {
				// Stream record to the caller; stop if they tell us to
				if !processor(rec) {
					break
				}
			}
		}
		return nil
	})
}

func (kv *KVStore) ScanFilter(ctx context.Context, tier string, match func(KnowledgeRecord) bool) ([]KnowledgeRecord, error) {
	var results []KnowledgeRecord
	err := kv.ScanFilterStream(ctx, tier, match, func(rec KnowledgeRecord) bool {
		results = append(results, rec)
		return true
	})
	return results, err
}

func (kv *KVStore) ScanKeyword(ctx context.Context, tier, keyword string) ([]KnowledgeRecord, error) {
	kw := strings.ToLower(keyword)
	return kv.ScanFilter(ctx, tier, func(r KnowledgeRecord) bool {
		return strings.Contains(strings.ToLower(r.Query), kw) ||
			strings.Contains(strings.ToLower(r.Result), kw) ||
			strings.Contains(strings.ToLower(r.Data), kw) ||
			strings.Contains(strings.ToLower(r.Correction), kw)
	})
}

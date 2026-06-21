// Copyright (c) 2026 Johnny Harvey
// All rights reserved.
package fusiondb

import (
	"bytes"
	"io/ioutil"
	"os"
	"strings"
	"testing"

	"github.com/dgraph-io/badger/v4"
)

// Acceptance Criteria 1: TestPIICompliance_PlaintextLeakCheck
// Write an entity with has_email = "user@example.com" (Degree 2).
// Scan BadgerDB raw keys and values. Verify no key or value contains the string "user@example.com" in plaintext.
func TestPIICompliance_PlaintextLeakCheck(t *testing.T) {
	dir, err := ioutil.TempDir("", "pii-leak-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	secretKey := db.SystemSecretKey
	saltKey := db.SystemSaltKey

	manifest := UFLManifestPII{
		Entity: UFLEntityPII{
			ID:   "person:alice",
			Tier: "verified",
			KV: map[string]string{
				"name": "Alice Smith",
			},
			Relations: []UFLRelationPII{
				{Predicate: "has_email", Object: "user@example.com", Degree: 2},
				{Predicate: "owns_vehicle", Object: "vin:12345", Degree: 1}, // public
			},
		},
	}

	err = db.core.Update(func(txn *badger.Txn) error {
		return SecureFuseEntity(txn, manifest, secretKey, saltKey)
	})
	if err != nil {
		t.Fatalf("SecureFuseEntity failed: %v", err)
	}

	// Scan all raw keys and values in the database
	err = db.core.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			key := item.Key()
			if strings.Contains(string(key), "user@example.com") {
				t.Errorf("Plaintext email leaked in key: %x (%s)", key, string(key))
			}

			err = item.Value(func(val []byte) error {
				if strings.Contains(string(val), "user@example.com") {
					t.Errorf("Plaintext email leaked in value for key %x: %s", key, string(val))
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Acceptance Criteria 2: TestPIICompliance_Hydrate
// Call HydrateEntity on the same entity. Verify the returned map contains has_email = "user@example.com" (correctly decrypted and unmasked).
func TestPIICompliance_Hydrate(t *testing.T) {
	dir, err := ioutil.TempDir("", "pii-hydrate-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	secretKey := db.SystemSecretKey
	saltKey := db.SystemSaltKey

	manifest := UFLManifestPII{
		Entity: UFLEntityPII{
			ID:   "person:alice",
			Tier: "verified",
			KV: map[string]string{
				"name": "Alice Smith",
			},
			Relations: []UFLRelationPII{
				{Predicate: "has_email", Object: "user@example.com", Degree: 2},
				{Predicate: "owns_vehicle", Object: "vin:12345", Degree: 1},
			},
		},
	}

	err = db.core.Update(func(txn *badger.Txn) error {
		return SecureFuseEntity(txn, manifest, secretKey, saltKey)
	})
	if err != nil {
		t.Fatal(err)
	}

	err = db.core.View(func(txn *badger.Txn) error {
		res, err := HydrateEntity(txn, "person:alice", "verified", 3, secretKey, saltKey)
		if err != nil {
			return err
		}

		if res["name"] != "Alice Smith" {
			t.Errorf("Expected name 'Alice Smith', got %v", res["name"])
		}

		if res["has_email"] != "user@example.com" {
			t.Errorf("Expected has_email 'user@example.com', got %v", res["has_email"])
		}

		if res["owns_vehicle"] != "vin:12345" {
			t.Errorf("Expected owns_vehicle 'vin:12345', got %v", res["owns_vehicle"])
		}

		// Check grouped relations
		relations, ok := res["relations"].(map[int][]map[string]string)
		if !ok {
			t.Fatalf("Relations not found or wrong type")
		}

		if len(relations[2]) != 1 || relations[2][0]["object"] != "user@example.com" {
			t.Errorf("Grouped Degree 2 relations incorrect: %v", relations[2])
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Acceptance Criteria 3: TestPIICompliance_Tampering
// Tamper with the ciphertext bytes in BadgerDB. Call HydrateEntity. Verify it returns a decryption error, not corrupted data.
func TestPIICompliance_Tampering(t *testing.T) {
	dir, err := ioutil.TempDir("", "pii-tampering-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	secretKey := db.SystemSecretKey
	saltKey := db.SystemSaltKey

	manifest := UFLManifestPII{
		Entity: UFLEntityPII{
			ID:   "person:alice",
			Tier: "verified",
			KV: map[string]string{
				"name": "Alice Smith",
			},
			Relations: []UFLRelationPII{
				{Predicate: "has_email", Object: "user@example.com", Degree: 2},
			},
		},
	}

	err = db.core.Update(func(txn *badger.Txn) error {
		return SecureFuseEntity(txn, manifest, secretKey, saltKey)
	})
	if err != nil {
		t.Fatal(err)
	}

	// Get the ciphertext, modify it (tamper), and store it back
	kvKey := append([]byte{tierByte("verified")}, []byte("person:alice")...)
	err = db.core.Update(func(txn *badger.Txn) error {
		item, err := txn.Get(kvKey)
		if err != nil {
			return err
		}
		var val []byte
		err = item.Value(func(v []byte) error {
			val = make([]byte, len(v))
			copy(val, v)
			return nil
		})
		if err != nil {
			return err
		}

		// Tamper with the ciphertext (flip a bit in the ciphertext portion, which starts at index 12)
		val[len(val)-1] ^= 0xFF

		return txn.Set(kvKey, val)
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify HydrateEntity returns a decryption failure error
	err = db.core.View(func(txn *badger.Txn) error {
		_, err := HydrateEntity(txn, "person:alice", "verified", 2, secretKey, saltKey)
		if err == nil {
			t.Errorf("Expected decryption failed error, got nil")
		} else if !strings.Contains(err.Error(), "decryption failed") {
			t.Errorf("Expected error to contain 'decryption failed', got: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Acceptance Criteria 4: TestPIICompliance_DeterministicHMAC_RandomIV
// Write two entities with the same email.
// Verify their graph keys for has_email are identical (deterministic HMAC), but their KV payloads have different nonces (random IV per write).
func TestPIICompliance_DeterministicHMAC_RandomIV(t *testing.T) {
	dir, err := ioutil.TempDir("", "pii-hmac-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	secretKey := db.SystemSecretKey
	saltKey := db.SystemSaltKey

	manifest1 := UFLManifestPII{
		Entity: UFLEntityPII{
			ID:   "person:alice",
			Tier: "verified",
			KV: map[string]string{
				"name": "Alice Smith",
			},
			Relations: []UFLRelationPII{
				{Predicate: "has_email", Object: "user@example.com", Degree: 2},
			},
		},
	}

	manifest2 := UFLManifestPII{
		Entity: UFLEntityPII{
			ID:   "person:bob",
			Tier: "verified",
			KV: map[string]string{
				"name": "Bob Jones",
			},
			Relations: []UFLRelationPII{
				{Predicate: "has_email", Object: "user@example.com", Degree: 2},
			},
		},
	}

	err = db.core.Update(func(txn *badger.Txn) error {
		return SecureFuseEntity(txn, manifest1, secretKey, saltKey)
	})
	if err != nil {
		t.Fatal(err)
	}

	err = db.core.Update(func(txn *badger.Txn) error {
		return SecureFuseEntity(txn, manifest2, secretKey, saltKey)
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify that their graph keys for has_email are identical (same hash value)
	hashedEmail := hmacSHA256("user@example.com", saltKey)
	fwdKey1 := BuildGraphKey(0x00, "person:alice", "has_email", hashedEmail)
	fwdKey2 := BuildGraphKey(0x00, "person:bob", "has_email", hashedEmail)

	err = db.core.View(func(txn *badger.Txn) error {
		_, err1 := txn.Get(fwdKey1)
		_, err2 := txn.Get(fwdKey2)
		if err1 != nil || err2 != nil {
			t.Errorf("Expected both graph keys to exist under HMAC-SHA256 hashed value, got err1: %v, err2: %v", err1, err2)
		}

		// Retrieve and compare KV payloads (first 12 bytes should be different due to random IV/nonce)
		kvKey1 := append([]byte{tierByte("verified")}, []byte("person:alice")...)
		kvKey2 := append([]byte{tierByte("verified")}, []byte("person:bob")...)

		item1, err := txn.Get(kvKey1)
		if err != nil {
			return err
		}
		item2, err := txn.Get(kvKey2)
		if err != nil {
			return err
		}

		var val1, val2 []byte
		_ = item1.Value(func(v []byte) error { val1 = v; return nil })
		_ = item2.Value(func(v []byte) error { val2 = v; return nil })

		if len(val1) < 12 || len(val2) < 12 {
			t.Fatalf("KV payloads too short")
		}

		nonce1 := val1[0:12]
		nonce2 := val2[0:12]

		if bytes.Equal(nonce1, nonce2) {
			t.Errorf("Expected different nonces for subsequent writes, got identical nonces: %x", nonce1)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

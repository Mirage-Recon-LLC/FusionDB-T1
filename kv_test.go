// Copyright (c) 2026 Johnny Harvey
// All rights reserved.
package fusiondb

import (
	"context"
	"errors"
	"io/ioutil"
	"os"
	"testing"

	"github.com/dgraph-io/badger/v4"
)

func TestKVStore_ScanFilterStream(t *testing.T) {
	dir, err := ioutil.TempDir("", "badger-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	opts := badger.DefaultOptions(dir).WithSyncWrites(false)
	db, err := badger.Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	kv := &KVStore{db: db}
	ctx := context.Background()

	records := []KnowledgeRecord{
		{Agent: "AgentA", Type: "type1", Query: "hello world", Result: "apple"},
		{Agent: "AgentB", Type: "type2", Query: "hello test", Result: "banana"},
		{Agent: "AgentC", Type: "type1", Query: "other query", Result: "cherry"},
	}

	for i, rec := range records {
		err := kv.Store(ctx, "verified", string(rune('0'+i)), rec)
		if err != nil {
			t.Fatal(err)
		}
	}

	// Test ScanFilterStream with all items matching
	var matches []KnowledgeRecord
	err = kv.ScanFilterStream(ctx, "verified", func(r KnowledgeRecord) bool {
		return r.Agent != ""
	}, func(r KnowledgeRecord) bool {
		matches = append(matches, r)
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 3 {
		t.Errorf("expected 3 matches, got %d", len(matches))
	}

	// Test early stopping by returning false from processor
	var partialMatches []KnowledgeRecord
	err = kv.ScanFilterStream(ctx, "verified", func(r KnowledgeRecord) bool {
		return r.Agent != ""
	}, func(r KnowledgeRecord) bool {
		partialMatches = append(partialMatches, r)
		return false // stop immediately
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(partialMatches) != 1 {
		t.Errorf("expected 1 match (early stop), got %d", len(partialMatches))
	}

	// Test ScanKeyword
	kwMatches, err := kv.ScanKeyword(ctx, "verified", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if len(kwMatches) != 2 {
		t.Errorf("expected 2 matches for keyword 'hello', got %d", len(kwMatches))
	}
}

func TestKVStore_GetNotFound(t *testing.T) {
	dir, err := ioutil.TempDir("", "badger-test-notfound")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	db, err := badger.Open(badger.DefaultOptions(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	kv := &KVStore{db: db}
	ctx := context.Background()

	_, err = kv.Get(ctx, "verified", "non-existent")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

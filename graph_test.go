// Copyright (c) 2026 Johnny Harvey
// All rights reserved.
package fusiondb

import (
	"context"
	"io/ioutil"
	"os"
	"testing"

	"github.com/cayleygraph/quad"
	"github.com/dgraph-io/badger/v4"
)

func TestParseGraphKey_Valid(t *testing.T) {
	key := BuildGraphKey(0x00, "alice", "knows", "bob")
	sub, pred, obj, ok := parseGraphKey(key)
	if !ok {
		t.Fatalf("expected parse to succeed")
	}
	if sub != "alice" || pred != "knows" || obj != "bob" {
		t.Errorf("unexpected parsing output: sub=%q, pred=%q, obj=%q", sub, pred, obj)
	}
}

func TestParseGraphKey_Corrupted(t *testing.T) {
	key := BuildGraphKey(0x00, "alice", "knows", "bob")
	// Expected length: 1 (prefix) + 2 (subLen) + 5 (alice) + 2 (predLen) + 5 (knows) + 2 (objLen) + 3 (bob) = 20
	fullValidLength := len(key)

	// Test prefixes of the valid key. With length-prefixing for all fields, it must only succeed for the full key.
	for i := 0; i < len(key); i++ {
		truncated := key[:i]
		sub, pred, obj, ok := parseGraphKey(truncated)
		if i < fullValidLength {
			if ok {
				t.Errorf("expected ok = false for truncated key of length %d, got sub=%q, pred=%q, obj=%q", i, sub, pred, obj)
			}
		} else {
			if !ok {
				t.Errorf("expected ok = true for full key, got false")
			}
		}
	}
}

func TestGraphStore_QuerySubject(t *testing.T) {
	dir, err := ioutil.TempDir("", "badger-graph-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	db, err := badger.Open(badger.DefaultOptions(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	gs := &GraphStore{db: db}
	ctx := context.Background()

	q := quad.Make("alice", "knows", "bob", "")
	err = gs.AddQuadInTier(ctx, q, "verified")
	if err != nil {
		t.Fatal(err)
	}

	results, err := gs.QuerySubject(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if quadStr(results[0].Object) != "bob" {
		t.Errorf("expected object 'bob', got %q", quadStr(results[0].Object))
	}
}

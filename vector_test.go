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

func TestDeserializeCometNode_Roundtrip(t *testing.T) {
	vec := []float32{1.0, -2.5, 3.14}
	neigh := []uint64{10, 20, 30, 40}

	data := serializeCometNode(vec, neigh)

	resVec, resNeigh := deserializeCometNode(data)

	if len(resVec) != len(vec) {
		t.Fatalf("expected vector length %d, got %d", len(vec), len(resVec))
	}
	for i := range vec {
		if resVec[i] != vec[i] {
			t.Errorf("at index %d: expected %f, got %f", i, vec[i], resVec[i])
		}
	}

	if len(resNeigh) != len(neigh) {
		t.Fatalf("expected neighbors length %d, got %d", len(neigh), len(resNeigh))
	}
	for i := range neigh {
		if resNeigh[i] != neigh[i] {
			t.Errorf("at index %d: expected %d, got %d", i, neigh[i], resNeigh[i])
		}
	}
}

func TestDeserializeCometNode_Truncated(t *testing.T) {
	vec := []float32{1.0, -2.5, 3.14}
	neigh := []uint64{10, 20, 30, 40}

	data := serializeCometNode(vec, neigh)

	// Test with every possible prefix of the valid serialized data to ensure it does not panic.
	for i := 0; i < len(data); i++ {
		truncated := data[:i]

		// Ensure we don't panic
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panicked on truncated input of length %d: %v", i, r)
				}
			}()
			deserializeCometNode(truncated)
		}()
	}
}

func TestVectorStore_GetHNSWNode(t *testing.T) {
	dir, err := ioutil.TempDir("", "badger-vector-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	db, err := badger.Open(badger.DefaultOptions(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	vs := &VectorStore{db: db}
	ctx := context.Background()

	vec := []float32{1.0, 2.0}
	neigh := []uint64{3, 4}
	err = vs.StoreHNSWNode(ctx, 1, vec, neigh)
	if err != nil {
		t.Fatal(err)
	}

	resVec, resNeigh, err := vs.GetHNSWNode(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(resVec) != 2 || resVec[0] != 1.0 {
		t.Errorf("vector data corrupted")
	}
	if len(resNeigh) != 2 || resNeigh[0] != 3 {
		t.Errorf("neighbor data corrupted")
	}
}

func TestVectorStore_GetNotFound(t *testing.T) {
	dir, err := ioutil.TempDir("", "badger-vector-test-notfound")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	db, err := badger.Open(badger.DefaultOptions(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	vs := &VectorStore{db: db}
	ctx := context.Background()

	_, _, err = vs.GetHNSWNode(ctx, 999)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

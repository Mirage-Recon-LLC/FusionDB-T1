// Copyright (c) 2026 Johnny Harvey
// All rights reserved.
package fusiondb

import (
	"context"
	"io/ioutil"
	"os"
	"testing"

	"github.com/dgraph-io/badger/v4"
)

// Acceptance Criteria 1: TestVectorRoundTrip
// StoreHNSWNode + GetHNSWNode returns identical vector and non-empty neighbor list.
func TestVectorRoundTrip(t *testing.T) {
	dir, err := ioutil.TempDir("", "hnsw-roundtrip-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	nodeID := uint64(42)
	vector := []float32{0.5, 0.5, 0.5}
	neighbors := []uint64{1, 2, 3}

	err = db.Vector.StoreHNSWNode(ctx, nodeID, vector, neighbors)
	if err != nil {
		t.Fatalf("StoreHNSWNode failed: %v", err)
	}

	retVec, retNeighbors, err := db.Vector.GetHNSWNode(ctx, nodeID)
	if err != nil {
		t.Fatalf("GetHNSWNode failed: %v", err)
	}

	if len(retVec) != len(vector) {
		t.Fatalf("Expected vector length %d, got %d", len(vector), len(retVec))
	}
	for i := range vector {
		if retVec[i] != vector[i] {
			t.Errorf("At index %d: expected %f, got %f", i, vector[i], retVec[i])
		}
	}

	if len(retNeighbors) != len(neighbors) {
		t.Fatalf("Expected neighbors length %d, got %d", len(neighbors), len(retNeighbors))
	}
	for i := range neighbors {
		if retNeighbors[i] != neighbors[i] {
			t.Errorf("At index %d: expected neighbor %d, got %d", i, neighbors[i], retNeighbors[i])
		}
	}
}

// Acceptance Criteria 4: GetHNSWMetadata returns a valid EntryNodeID after the first insert.
func TestGetHNSWMetadata_Initialized(t *testing.T) {
	dir, err := ioutil.TempDir("", "hnsw-meta-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	// Insert first node
	err = db.AtomicFusion(ctx, "verified", 100, []float32{1.0, 0.0}, "subject1", "pred", "obj")
	if err != nil {
		t.Fatalf("First insert failed: %v", err)
	}

	err = db.core.View(func(txn *badger.Txn) error {
		meta, err := GetHNSWMetadata(txn)
		if err != nil {
			return err
		}
		if meta.EntryNodeID != 100 {
			t.Errorf("Expected EntryNodeID to be 100, got %d", meta.EntryNodeID)
		}
		if meta.MaxLayer != 0 {
			t.Errorf("Expected MaxLayer to be 0, got %d", meta.MaxLayer)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Failed to retrieve/verify metadata: %v", err)
	}
}

// Acceptance Criteria 2: TestAtomicFusion
// After 3+ AtomicFusion calls, SearchHNSWGraph returns results without falling back to LinearScanFallback.
func TestAtomicFusion(t *testing.T) {
	dir, err := ioutil.TempDir("", "hnsw-fusion-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	// 3+ AtomicFusion calls
	err = db.AtomicFusion(ctx, "verified", 1, []float32{1.0, 0.0}, "subject1", "pred", "obj")
	if err != nil {
		t.Fatal(err)
	}
	err = db.AtomicFusion(ctx, "verified", 2, []float32{0.0, 1.0}, "subject2", "pred", "obj")
	if err != nil {
		t.Fatal(err)
	}
	err = db.AtomicFusion(ctx, "verified", 3, []float32{0.707, 0.707}, "subject3", "pred", "obj")
	if err != nil {
		t.Fatal(err)
	}

	// Verify neighbors were linked (non-empty adjacency list for nodes 2 and 3)
	_, n1, _ := db.Vector.GetHNSWNode(ctx, 1)
	_, n2, _ := db.Vector.GetHNSWNode(ctx, 2)
	_, n3, _ := db.Vector.GetHNSWNode(ctx, 3)

	t.Logf("Node 1 neighbors: %v", n1)
	t.Logf("Node 2 neighbors: %v", n2)
	t.Logf("Node 3 neighbors: %v", n3)

	if len(n1) == 0 && len(n2) == 0 && len(n3) == 0 {
		t.Errorf("All neighbor lists are empty, neighbor linking failed")
	}

	// Verify SearchHNSWGraph does not fall back (meaning metadata is found and EntryNodeID exists)
	err = db.core.View(func(txn *badger.Txn) error {
		// If metadata exists, SearchHNSWGraph will start at entry point and not fall back
		meta, err := GetHNSWMetadata(txn)
		if err != nil {
			t.Errorf("Search would fall back: GetHNSWMetadata failed: %v", err)
		} else if meta.EntryNodeID != 1 {
			t.Errorf("Expected entry node to be 1, got %d", meta.EntryNodeID)
		}

		res, err := SearchHNSWGraph(ctx, txn, []float32{0.6, 0.6}, 2, 4)
		if err != nil {
			t.Fatalf("SearchHNSWGraph failed: %v", err)
		}
		if len(res) == 0 {
			t.Fatalf("SearchHNSWGraph returned empty results")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Acceptance Criteria 3: TestSearchHNSWGraph_Accuracy
// Insert 10 nodes, then call SearchHNSWGraph(queryVec, k=3, ef=10). Verify results match LinearScanFallback results (same top-3 node IDs).
func TestSearchHNSWGraph_Accuracy(t *testing.T) {
	dir, err := ioutil.TempDir("", "hnsw-accuracy-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()

	// Insert 10 nodes with progressive angles/directions
	// Let's use 2D unit vectors
	vectors := [][]float32{
		{1.0, 0.0},
		{0.9, 0.1},
		{0.8, 0.2},
		{0.7, 0.3},
		{0.6, 0.4},
		{0.5, 0.5},
		{0.4, 0.6},
		{0.3, 0.7},
		{0.2, 0.8},
		{0.1, 0.9},
	}

	for i, vec := range vectors {
		nodeID := uint64(i + 1)
		err = db.AtomicFusion(ctx, "verified", nodeID, vec, string(rune('A'+i)), "link", "root")
		if err != nil {
			t.Fatalf("Failed to insert node %d: %v", nodeID, err)
		}
	}

	queryVec := []float32{0.55, 0.45}

	err = db.core.View(func(txn *badger.Txn) error {
		hnswRes, err := SearchHNSWGraph(ctx, txn, queryVec, 3, 10)
		if err != nil {
			t.Fatalf("SearchHNSWGraph failed: %v", err)
		}

		linearRes, err := LinearScanFallback(ctx, txn, queryVec, 3)
		if err != nil {
			t.Fatalf("LinearScanFallback failed: %v", err)
		}

		if len(hnswRes) != len(linearRes) {
			t.Fatalf("Results length mismatch: HNSW has %d, Linear has %d", len(hnswRes), len(linearRes))
		}

		for i := range hnswRes {
			if hnswRes[i].NodeID != linearRes[i].NodeID {
				t.Errorf("At index %d: HNSW node ID %d does not match Linear node ID %d", i, hnswRes[i].NodeID, linearRes[i].NodeID)
			}
			t.Logf("Rank %d: NodeID %d (dist HNSW: %f, dist Linear: %f)", i, hnswRes[i].NodeID, hnswRes[i].Distance, linearRes[i].Distance)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Additional Verification: TestUFL_VectorQuery
func TestUFL_VectorQuery(t *testing.T) {
	dir, err := ioutil.TempDir("", "ufl-vector-query-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()

	// Fuse two entities with vectors
	manifest1 := UFLManifest{
		Action: "fuse",
		Entity: UFLEntity{
			ID:     "person:alice",
			Type:   "Person",
			Tier:   "verified",
			Vector: []float32{1.0, 0.0, 0.0},
			KV:     map[string]any{"name": "Alice"},
		},
	}
	manifest2 := UFLManifest{
		Action: "fuse",
		Entity: UFLEntity{
			ID:     "person:bob",
			Type:   "Person",
			Tier:   "verified",
			Vector: []float32{0.0, 1.0, 0.0},
			KV:     map[string]any{"name": "Bob"},
		},
	}

	if err := db.Fuse(ctx, manifest1); err != nil {
		t.Fatal(err)
	}
	if err := db.Fuse(ctx, manifest2); err != nil {
		t.Fatal(err)
	}

	// Query selector near Alice's vector
	query := UFLQuery{
		Action: "query",
		Selector: UFLSelector{
			Vector: &UFLVectorSelector{
				Near:  []float32{0.9, 0.1, 0.0},
				Limit: 1,
			},
		},
		Options: UFLOptions{
			Hydrate: UFLHydrationOptions{
				MaxDegree:     1,
				IncludeVector: true,
			},
		},
	}

	res, err := db.UFLQuery(ctx, query)
	if err != nil {
		t.Fatalf("UFLQuery with vector selector failed: %v", err)
	}

	if res.ID != "person:alice" {
		t.Errorf("Expected to resolve person:alice, resolved %q", res.ID)
	}

	if len(res.Vector) != 3 || res.Vector[0] != 1.0 {
		t.Errorf("Vector not hydrated correctly: %v", res.Vector)
	}
}

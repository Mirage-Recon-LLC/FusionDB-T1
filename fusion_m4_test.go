package fusiondb

import (
	"context"
	"os"
	"strconv"
	"testing"
)

func TestAtomicFusion_Success(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "fusiondb-test-m4-success")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	db, err := Open(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	tier := "verified"
	nodeID := uint64(420)
	vector := []float32{0.1, 0.2, 0.3}
	subject := "target-ip"
	predicate := "resolves_to"
	object := "malicious-host"

	err = db.AtomicFusion(ctx, tier, nodeID, vector, subject, predicate, object)
	if err != nil {
		t.Fatalf("AtomicFusion failed: %v", err)
	}

	// 1. Verify graph triple exists
	relations, err := db.Graph.QuerySubject(ctx, subject)
	if err != nil {
		t.Fatalf("QuerySubject failed: %v", err)
	}
	if len(relations) != 1 {
		t.Errorf("expected 1 relation, got %d", len(relations))
	} else {
		subStr := quadStr(relations[0].Subject)
		predStr := quadStr(relations[0].Predicate)
		objStr := quadStr(relations[0].Object)
		if subStr != subject || predStr != predicate || objStr != object {
			t.Errorf("unexpected relation contents: %s -- %s -> %s", subStr, predStr, objStr)
		}
	}

	// 2. Verify vector node exists
	vec, _, err := db.Vector.GetHNSWNode(ctx, nodeID)
	if err != nil {
		t.Fatalf("GetHNSWNode failed: %v", err)
	}
	if len(vec) != len(vector) || vec[0] != vector[0] {
		t.Errorf("vector mismatch: got %v, expected %v", vec, vector)
	}

	// 3. Verify KV record exists
	kvKey := tier + ":" + subject + ":" + strconv.FormatUint(uint64(nodeID), 10)
	record, err := db.KV.Get(ctx, tier, kvKey)
	if err != nil {
		t.Fatalf("KV Get failed: %v", err)
	}
	if record.Agent != subject || record.Type != predicate || record.Result != object {
		t.Errorf("KV record contents mismatch: %+v", record)
	}
}

func TestAtomicFusion_CrashRollback(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "fusiondb-test-m4-crash")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	db, err := Open(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate crash/panic after graph write but before commit
	func() {
		defer func() {
			if r := recover(); r != nil {
				// Recovered from simulated panic
			}
		}()

		txn := db.core.NewTransaction(true)
		defer txn.Discard()

		// Write graph quad (simulating start of AtomicFusion write)
		err = db.Graph.AddQuadStringsInTxn(txn, "crash-sub", "resolves_to", "crash-obj", "verified")
		if err != nil {
			t.Fatal(err)
		}

		// Simulate panic
		panic("simulated panic after graph write but before commit")
	}()

	// Close database and reopen to simulate restart
	db.Close()

	db2, err := Open(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()

	// Verify that neither the graph triple nor the KV record exists
	relations, err := db2.Graph.QuerySubject(context.Background(), "crash-sub")
	if err != nil {
		t.Fatal(err)
	}
	if len(relations) > 0 {
		t.Errorf("expected graph triple to not exist after crash/rollback, found %v", relations)
	}
}

func TestAtomicFusion_TierIsolation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "fusiondb-test-m4-tier")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	db, err := Open(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()

	// 1. Call AtomicFusion with "verified" tier
	err = db.AtomicFusion(ctx, "verified", 1, []float32{1.0}, "entity-v", "link", "target-v")
	if err != nil {
		t.Fatal(err)
	}

	// 2. Call AtomicFusion with "knowledge" tier
	err = db.AtomicFusion(ctx, "knowledge", 2, []float32{2.0}, "entity-k", "link", "target-k")
	if err != nil {
		t.Fatal(err)
	}

	// Verify KV records are in their correct tiers
	keyV := "verified:entity-v:1"
	_, err = db.KV.Get(ctx, "verified", keyV)
	if err != nil {
		t.Errorf("expected keyV to exist in verified tier, got err: %v", err)
	}
	_, err = db.KV.Get(ctx, "knowledge", keyV)
	if err == nil {
		t.Errorf("expected keyV to not exist in knowledge tier")
	}

	keyK := "knowledge:entity-k:2"
	_, err = db.KV.Get(ctx, "knowledge", keyK)
	if err != nil {
		t.Errorf("expected keyK to exist in knowledge tier, got err: %v", err)
	}
	_, err = db.KV.Get(ctx, "verified", keyK)
	if err == nil {
		t.Errorf("expected keyK to not exist in verified tier")
	}
}

func TestAtomicFusion_InvalidInputs(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "fusiondb-test-m4-invalid")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	db, err := Open(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()

	// Nil/empty vector
	err = db.AtomicFusion(ctx, "verified", 1, nil, "sub", "pred", "obj")
	if err == nil {
		t.Error("expected error for nil vector")
	}

	// Empty subject
	err = db.AtomicFusion(ctx, "verified", 1, []float32{1.0}, "", "pred", "obj")
	if err == nil {
		t.Error("expected error for empty subject")
	}

	// Invalid tier defaults to verified
	err = db.AtomicFusion(ctx, "invalid-tier", 100, []float32{1.0}, "sub-invalid", "pred", "obj")
	if err != nil {
		t.Fatalf("expected invalid tier to default to verified, got error: %v", err)
	}

	// Verify it exists in verified tier
	key := "verified:sub-invalid:100"
	_, err = db.KV.Get(ctx, "verified", key)
	if err != nil {
		t.Errorf("expected record to be in defaulted verified tier, got error: %v", err)
	}
}

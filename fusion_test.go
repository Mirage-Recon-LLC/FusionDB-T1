// Copyright (c) 2026 Johnny Harvey
// All rights reserved.
package fusiondb

import (
	"context"
	"os"
	"testing"
)

func TestUnifiedEngineLifecycle(t *testing.T) {
	tmpDir := "./test_vault"
	defer os.RemoveAll(tmpDir)

	// 1. Unified Open
	db, err := Open(tmpDir)
	if err != nil {
		t.Fatalf("Failed to open unified engine: %v", err)
	}
	defer db.Close()

	// 2. Unified Atomic Storage
	ctx := context.Background()
	err = db.AtomicFusion(ctx, "verified", 99, []float32{1.0, -1.0, 0.5}, "malware-sample", "belongs_to", "APT28")
	if err != nil {
		t.Fatalf("Atomic Fusion write failed: %v", err)
	}

	// 3. Dual-Modality Cross Verification
	relations, err := db.Graph.QuerySubject(ctx, "malware-sample")
	if err != nil {
		t.Fatalf("Graph verification query failed: %v", err)
	}
	if len(relations) != 1 {
		t.Errorf("Expected 1 graph relationship, got %d", len(relations))
	}

	vec, _, err := db.Vector.GetHNSWNode(ctx, 99)
	if err != nil {
		t.Fatalf("Vector verification query failed: %v", err)
	}
	if len(vec) != 3 || vec[0] != 1.0 {
		t.Errorf("Vector data corrupted or missing during unified write")
	}
}

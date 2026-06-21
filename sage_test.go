package fusiondb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"
)

func TestSAGEKernel_Integration(t *testing.T) {
	dir := t.TempDir()
	
	// Setup environment
	key := make([]byte, 32)
	rand.Read(key)
	os.Setenv("FUSIONDB_SECRET", hex.EncodeToString(key))
	
	kernel, err := Open(dir)
	if err != nil {
		t.Fatalf("failed to open kernel: %v", err)
	}
	defer kernel.Close()

	ctx := context.Background()
	
	entity := &FusionEntity{
		ID:          "test-node-1",
		Type:        "observation",
		Layer:       LayerVerified,
		Content:     "This is a encrypted cognitive fact.",
		Salience:    1.0,
		Reliability: 1.0,
		DecayFactor: 0.1,
		Subjects:    []string{"security", "encryption"},
		CreatedAt:   time.Now(),
	}
	
	embedding := make([]float32, 384)
	embedding[0] = 1.0
	
	// Test Write
	err = kernel.SecureWriteTransaction(ctx, entity, embedding)
	if err != nil {
		t.Fatalf("secure write failed: %v", err)
	}
	
	// Test Hybrid Query
	q := HybridSearchQuery{
		QueryEmbedding: embedding,
		PoolLimit:      10,
		TargetLimit:    1,
	}
	
	results, err := kernel.HybridQueryEngine(ctx, q)
	if err != nil {
		t.Fatalf("hybrid query failed: %v", err)
	}
	
	if len(results) == 0 {
		t.Fatalf("expected results, got 0")
	}
	
	if results[0].ID != entity.ID {
		t.Errorf("expected ID %s, got %s", entity.ID, results[0].ID)
	}
	
	if results[0].Content != entity.Content {
		t.Errorf("expected content %q, got %q (decryption failed?)", entity.Content, results[0].Content)
	}
}

func TestSAGEKernel_CircuitBreaker(t *testing.T) {
	dir := t.TempDir()
	kernel, _ := Open(dir)
	defer kernel.Close()
	
	ctx := context.Background()
	
	// Trip circuit breaker
	err := os.ErrPermission
	kernel.CircuitBreaker(ctx, err)
	kernel.CircuitBreaker(ctx, err)
	kernel.CircuitBreaker(ctx, err)
	
	if !kernel.IsCircuitBroken {
		t.Errorf("expected circuit breaker to be broken")
	}
	
	// Verify write is rejected
	entity := &FusionEntity{ID: "fail-test", Layer: LayerVerified}
	err = kernel.SecureWriteTransaction(ctx, entity, make([]float32, 384))
	if err == nil || err.Error() != "database transaction rejected: central circuit breaker is open" {
		t.Errorf("expected rejection, got %v", err)
	}
}

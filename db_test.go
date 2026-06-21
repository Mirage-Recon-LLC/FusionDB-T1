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

func TestDB_Open(t *testing.T) {
	dir, err := ioutil.TempDir("", "badger-db-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	if db.KV == nil || db.Vector == nil || db.Graph == nil {
		t.Error("expected DB stores to be initialized")
	}
}

func TestDB_OpenAdvanced(t *testing.T) {
	dir, err := ioutil.TempDir("", "badger-db-test-advanced")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	opts := DefaultOptions(dir)
	opts.SyncWrites = false
	opts.ValueThreshold = 512

	db, err := OpenAdvanced(opts)
	if err != nil {
		t.Fatalf("OpenAdvanced failed: %v", err)
	}
	defer db.Close()

	if db.KV == nil || db.Vector == nil || db.Graph == nil {
		t.Error("expected DB stores to be initialized")
	}
}

func TestDB_AtomicFusionContextCancelled(t *testing.T) {
	dir, err := ioutil.TempDir("", "badger-db-test-context")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel context immediately

	err = db.AtomicFusion(ctx, "verified", 100, []float32{1.0}, "subj", "pred", "obj")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled error, got: %v", err)
	}
}

func TestDB_SafeUpdatePanicRecovery(t *testing.T) {
	dir, err := ioutil.TempDir("", "badger-db-test-panic")
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
	err = db.SafeUpdate(ctx, func(txn *badger.Txn) error {
		panic("something went horribly wrong")
	})

	if err == nil {
		t.Fatal("expected panic to be recovered as an error, got nil")
	}

	expectedMsg := "transaction aborted due to internal panic: something went horribly wrong"
	if err.Error() != expectedMsg {
		t.Errorf("expected error message %q, got %q", expectedMsg, err.Error())
	}
}

func TestValidateFusionDBSecret_Empty(t *testing.T) {
	_, err := validateFusionDBSecret("")
	if err == nil {
		t.Error("expected error for empty secret, got nil")
	}
}

func TestValidateFusionDBSecret_TooShort(t *testing.T) {
	_, err := validateFusionDBSecret("tooshort")
	if err == nil {
		t.Error("expected error for 8-byte secret, got nil")
	}
}

func TestValidateFusionDBSecret_ShortHex(t *testing.T) {
	_, err := validateFusionDBSecret("0102030405060708090a0b0c0d0e0f10")
	if err == nil {
		t.Error("expected error for 16-byte hex-decoded secret, got nil")
	}
}

func TestValidateFusionDBSecret_ValidHex(t *testing.T) {
	secret := "0102030405060708090a0b0c0d0e0f100102030405060708090a0b0c0d0e0f10"
	decoded, err := validateFusionDBSecret(secret)
	if err != nil {
		t.Fatalf("expected no error for valid 32-byte hex, got: %v", err)
	}
	if len(decoded) < 32 {
		t.Errorf("expected decoded length >= 32, got %d", len(decoded))
	}
}

func TestValidateFusionDBSecret_ValidRaw32Bytes(t *testing.T) {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	decoded, err := validateFusionDBSecret(string(raw))
	if err != nil {
		t.Fatalf("expected no error for 32 raw bytes, got: %v", err)
	}
	if len(decoded) < 32 {
		t.Errorf("expected decoded length >= 32, got %d", len(decoded))
	}
}

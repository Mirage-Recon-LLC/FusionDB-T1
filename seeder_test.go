package fusiondb

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/dgraph-io/badger/v4"
	"github.com/xuri/excelize/v2"
)

func TestOpenAdvanced_MemoryLimits(t *testing.T) {
	// Create a temporary directory for the DB
	tmpDir, err := os.MkdirTemp("", "fusiondb-test-opts")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Call OpenAdvanced with zero-value options (except Path)
	opts := Options{
		Path: tmpDir,
	}

	db, err := OpenAdvanced(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Verify defaults were set in our local helper / opts copy
	defaultedOpts := opts.withDefaults()
	if defaultedOpts.BlockCacheSize != 64<<20 {
		t.Errorf("expected BlockCacheSize to default to 64MB, got %d", defaultedOpts.BlockCacheSize)
	}
	if defaultedOpts.IndexCacheSize != 32<<20 {
		t.Errorf("expected IndexCacheSize to default to 32MB, got %d", defaultedOpts.IndexCacheSize)
	}
	if defaultedOpts.RistrettoMaxCost != 128<<20 {
		t.Errorf("expected RistrettoMaxCost to default to 128MB, got %d", defaultedOpts.RistrettoMaxCost)
	}
	if defaultedOpts.ValueThreshold != 1024 {
		t.Errorf("expected ValueThreshold to default to 1024, got %d", defaultedOpts.ValueThreshold)
	}
}

func TestRistrettoCache_Hit(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "fusiondb-test-cache")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	db, err := OpenAdvanced(Options{Path: tmpDir})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	key := "test-key"
	record := KnowledgeRecord{
		Agent:      "test-agent",
		Type:       "test-type",
		Query:      "test-query",
		Result:     "test-result",
		Correction: "test-correction",
		Data:       "test-data",
	}

	// Store key
	err = db.KV.Store(nil, "verified", key, record)
	if err != nil {
		t.Fatal(err)
	}

	// Read 1 (Miss): should hit BadgerDB and populate cache
	_, err = db.KV.Get(nil, "verified", key)
	if err != nil {
		t.Fatal(err)
	}

	// Ristretto updates the cache asynchronously. Call Wait() to block until Set is processed.
	db.cache.Wait()

	// Read 2 (Hit): should return directly from cache
	startReadCount := db.KV.BadgerReadCount
	_, err = db.KV.Get(nil, "verified", key)
	if err != nil {
		t.Fatal(err)
	}
	endReadCount := db.KV.BadgerReadCount

	// Since the first Get() incremented BadgerReadCount to 1, the second Get() should not increment it further.
	// BadgerReadCount is only incremented on cache miss.
	if endReadCount != startReadCount {
		t.Errorf("expected no additional BadgerDB read, read count went from %d to %d", startReadCount, endReadCount)
	}
}

func TestStreamDirectorySeeder_Markdown(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "fusiondb-test-md")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create 500 markdown files with frontmatter and body
	seederDir := filepath.Join(tmpDir, "seeder")
	if err := os.MkdirAll(seederDir, 0755); err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= 500; i++ {
		filePath := filepath.Join(seederDir, fmt.Sprintf("file-%d.md", i))
		content := fmt.Sprintf(`---
entity:
  id: "md-%d"
  tier: "verified"
  type: "document"
  kv:
    name: "Doc %d"
---
This is the description body of document %d.
It contains some text to simulate file size.
`, i, i, i)
		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	dbDir := filepath.Join(tmpDir, "db")
	db, err := OpenAdvanced(Options{Path: dbDir})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Measure memory before
	runtime.GC()
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)

	// Run seeder
	secretKey := []byte("12345678901234567890123456789012")
	saltKey := []byte("abcdefghijklmnopqrstuvwxyz123456")
	err = StreamDirectorySeeder(db, seederDir, secretKey, saltKey)
	if err != nil {
		t.Fatal(err)
	}

	// Measure memory after
	runtime.GC()
	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)

	// Max limit is 200MB
	limit := uint64(200 * 1024 * 1024)
	if memAfter.Alloc > limit {
		t.Errorf("heap memory exceeded 200MB limit: got %d bytes", memAfter.Alloc)
	}

	// Verify that entities were seeded
	err = db.core.View(func(txn *badger.Txn) error {
		res, err := HydrateEntity(txn, "md-250", "verified", 2, secretKey, saltKey)
		if err != nil {
			return err
		}
		if res["name"] != "Doc 250" {
			t.Errorf("expected name Doc 250, got %v", res["name"])
		}
		if res["description"] != "This is the description body of document 250.\nIt contains some text to simulate file size." {
			t.Errorf("expected description mismatch, got %v", res["description"])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStreamDirectorySeeder_Excel(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "fusiondb-test-xlsx")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	seederDir := filepath.Join(tmpDir, "seeder")
	if err := os.MkdirAll(seederDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create 10,000 row excel file
	excelPath := filepath.Join(seederDir, "entities.xlsx")
	f := excelize.NewFile()
	index, err := f.NewSheet("Entities")
	if err != nil {
		t.Fatal(err)
	}
	f.SetActiveSheet(index)

	sw, err := f.NewStreamWriter("Entities")
	if err != nil {
		t.Fatal(err)
	}

	// Write header
	if err := sw.SetRow("A1", []interface{}{"_id", "_tier", "name", "email", "phone", "category"}); err != nil {
		t.Fatal(err)
	}

	// Write 10,000 rows
	for i := 1; i <= 10000; i++ {
		rowStr := fmt.Sprintf("A%d", i+1)
		rowVals := []interface{}{
			fmt.Sprintf("ent-%d", i),
			"verified",
			fmt.Sprintf("User %d", i),
			fmt.Sprintf("user%d@example.com", i),
			fmt.Sprintf("555-%04d", i),
			"person",
		}
		if err := sw.SetRow(rowStr, rowVals); err != nil {
			t.Fatal(err)
		}
	}
	if err := sw.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.SaveAs(excelPath); err != nil {
		t.Fatal(err)
	}

	dbDir := filepath.Join(tmpDir, "db")
	db, err := OpenAdvanced(Options{Path: dbDir})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	secretKey := []byte("12345678901234567890123456789012")
	saltKey := []byte("abcdefghijklmnopqrstuvwxyz123456")

	err = StreamDirectorySeeder(db, seederDir, secretKey, saltKey)
	if err != nil {
		t.Fatal(err)
	}

	// Verify that entities were seeded
	err = db.core.View(func(txn *badger.Txn) error {
		// Test one from the start, middle, and end
		for _, i := range []int{1, 5000, 10000} {
			id := fmt.Sprintf("ent-%d", i)
			res, err := HydrateEntity(txn, id, "verified", 2, secretKey, saltKey)
			if err != nil {
				return fmt.Errorf("failed to hydrate %s: %w", id, err)
			}
			expectedName := fmt.Sprintf("User %d", i)
			if res["name"] != expectedName {
				t.Errorf("expected name %s, got %v", expectedName, res["name"])
			}
			expectedEmail := fmt.Sprintf("user%d@example.com", i)
			if res["has_email"] != expectedEmail {
				t.Errorf("expected email %s, got %v", expectedEmail, res["has_email"])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

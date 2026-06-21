package fusiondb

import (
	"context"
	"io/ioutil"
	"os"
	"testing"
)

func TestUFL_Fuse(t *testing.T) {
	dir, err := ioutil.TempDir("", "ufl-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	manifest := UFLManifest{
		Action: "fuse",
		Entity: UFLEntity{
			ID: "person:test",
			Type: "Person",
			Tier: "verified",
			KV: map[string]any{"name": "Test User"},
			Relations: map[string][]UFLRelation{
				"secondary": {{Predicate: "has_phone", Object: "555-1234"}},
			},
		},
	}

	err = db.Fuse(context.Background(), manifest)
	if err != nil {
		t.Fatalf("Fuse failed: %v", err)
	}

	// Verify Graph
	rels, err := db.Graph.QuerySubject(context.Background(), "person:test")
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 1 {
		t.Errorf("Expected 1 relation, got %d", len(rels))
	}
}

func TestUFL_Query(t *testing.T) {
	dir, err := ioutil.TempDir("", "ufl-query-test")
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
	manifest := UFLManifest{
		Action: "fuse",
		Entity: UFLEntity{
			ID:   "person:test-query",
			Type: "Person",
			Tier: "verified",
			KV:   map[string]any{"name": "Query User"},
			Relations: map[string][]UFLRelation{
				"secondary": {{Predicate: "has_phone", Object: "555-9999"}},
			},
		},
	}

	if err := db.Fuse(ctx, manifest); err != nil {
		t.Fatalf("Fuse failed: %v", err)
	}

	query := UFLQuery{
		Action:   "query",
		Selector: UFLSelector{ID: "person:test-query"},
		Options:  UFLOptions{Hydrate: UFLHydrationOptions{MaxDegree: 4}},
	}

	res, err := db.UFLQuery(ctx, query)
	if err != nil {
		t.Fatalf("UFLQuery failed: %v", err)
	}

	if res.ID != "person:test-query" {
		t.Errorf("Expected ID 'person:test-query', got %q", res.ID)
	}

	if len(res.Relations["secondary"]) != 1 {
		t.Errorf("Expected 1 secondary relation, got %d", len(res.Relations["secondary"]))
	}

	if res.Relations["secondary"][0].Object != "555-9999" {
		t.Errorf("Expected object '555-9999', got %q", res.Relations["secondary"][0].Object)
	}
}

func TestUFL_TypePreservation(t *testing.T) {
	dir, err := ioutil.TempDir("", "ufl-type-test")
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
	manifest := UFLManifest{
		Action: "fuse",
		Entity: UFLEntity{
			ID:   "type:test",
			Type: "Test",
			Tier: "verified",
			KV: map[string]any{
				"string": "value",
				"int":    float64(42), // JSON unmarshals numbers as float64
				"bool":   true,
			},
		},
	}

	if err := db.Fuse(ctx, manifest); err != nil {
		t.Fatalf("Fuse failed: %v", err)
	}

	query := UFLQuery{
		Action:   "query",
		Selector: UFLSelector{ID: "type:test"},
		Options:  UFLOptions{Hydrate: UFLHydrationOptions{MaxDegree: 1}},
	}

	res, err := db.UFLQuery(ctx, query)
	if err != nil {
		t.Fatalf("UFLQuery failed: %v", err)
	}

	if res.KV["string"] != "value" {
		t.Errorf("Expected 'value', got %v", res.KV["string"])
	}
	if res.KV["int"] != float64(42) {
		t.Errorf("Expected 42 (float64), got %v (%T)", res.KV["int"], res.KV["int"])
	}
	if res.KV["bool"] != true {
		t.Errorf("Expected true, got %v", res.KV["bool"])
	}
}

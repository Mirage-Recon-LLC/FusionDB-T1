# FusionDB Go API Reference

Package path: `github.com/Mirage-Recon-LLC/FusionDB-T1`

---

## Table of Contents

1. [Opening a Database](#opening-a-database)
2. [Configuration](#configuration)
3. [Core Types](#core-types)
4. [Writing Data](#writing-data)
5. [Reading Data](#reading-data)
6. [UFL Interface](#ufl-interface)
7. [KV Store](#kv-store)
8. [Graph Store](#graph-store)
9. [Vector Store](#vector-store)
10. [Encryption Utilities](#encryption-utilities)
11. [Governance and Safety](#governance-and-safety)
12. [Constants and Errors](#constants-and-errors)

---

## Opening a Database

### `Open(path string) (*DB, error)`

Opens a FusionDB database at the given directory path using default options. Creates the directory if it does not exist.

Reads `FUSIONDB_SECRET` from the environment. Exits the process (`os.Exit(1)`) if the secret is missing or has insufficient entropy.

```go
db, err := fusiondb.Open("./mydata")
if err != nil {
    log.Fatal(err)
}
defer db.Close()
```

### `OpenAdvanced(opts Options) (*DB, error)`

Opens a database with custom configuration.

```go
db, err := fusiondb.OpenAdvanced(fusiondb.Options{
    Path:             "./mydata",
    SyncWrites:       true,
    ValueThreshold:   2048,
    BlockCacheSize:   128 << 20,  // 128 MB
    IndexCacheSize:   64 << 20,   // 64 MB
    RistrettoMaxCost: 256 << 20,  // 256 MB
    EmbeddingDims:    384,         // Enforce 384-dimensional vectors
})
```

### `DefaultOptions(path string) Options`

Returns a safe production configuration:

| Field | Default |
|---|---|
| `SyncWrites` | `true` |
| `ValueThreshold` | `1024` bytes |
| `BlockCacheSize` | `64 MB` |
| `IndexCacheSize` | `32 MB` |
| `RistrettoMaxCost` | `128 MB` |
| `EmbeddingDims` | `0` (disabled) |

### `(*DB) Close() error`

Flushes all pending writes, closes the Ristretto cache, and releases the BadgerDB LOCK file.

Always call `Close()` before the process exits. Defer it immediately after `Open()`.

---

## Configuration

### `Options`

```go
type Options struct {
    Path             string  // Database directory path
    SyncWrites       bool    // Flush to disk on every commit (recommended: true)
    ValueThreshold   int64   // Values larger than this go to the value log
    BlockCacheSize   int64   // LSM block cache size in bytes
    IndexCacheSize   int64   // Bloom filter index cache size in bytes
    RistrettoMaxCost int64   // Maximum in-process LRU cache cost in bytes
    EmbeddingDims    int     // If > 0, validates all vectors match this dimension count
}
```

### `Config`

Internal configuration populated from `FUSIONDB_SECRET` at startup. Not set directly by callers.

```go
type Config struct {
    MasterKey     []byte  // 32-byte AES-256-GCM encryption key (derived)
    HMACSalt      []byte  // 32-byte HMAC salt for PII masking (derived)
    EmbeddingDims int     // Passed from Options
}
```

---

## Core Types

### `DB` / `SAGEKernel`

`DB` is an alias for `SAGEKernel`. Use `DB` in application code.

```go
type SAGEKernel struct {
    Graph           *GraphStore
    Vector          *VectorStore
    KV              *KVStore
    ActiveSessionID string
    IsCircuitBroken bool
    FailedToolCount int
    BusChannel      chan interface{}
}
```

### `FusionEntity`

The internal representation of a stored entity. Returned by `HybridQueryEngine`.

```go
type FusionEntity struct {
    ID          string
    Type        string        // "observation", "reflection", "plan", "correction"
    Layer       DataLayer
    Content     string        // Decrypted primary content
    Salience    float64       // Relevance weight (used for scoring)
    Reliability float64       // Trust score (0.0–1.0)
    DecayFactor float64       // Rate at which salience decays over time
    Subjects    []string      // Graph subject identifiers
    Relations   []UFLRelation // Graph relations
    ContextChat string        // Session or chat context tag
    Metadata    map[string]any
    CreatedAt   time.Time
}
```

### `DataLayer`

```go
type DataLayer string

const (
    LayerVerified   DataLayer = "VERIFIED"    // Prefix 0x10
    LayerUnverified DataLayer = "UNVERIFIED"  // Prefix 0x11
    LayerKnowledge  DataLayer = "KNOWLEDGE"   // Prefix 0x12
)
```

---

## Writing Data

### `(*DB) Fuse(ctx context.Context, manifest UFLManifest) error`

The primary write method. Converts a UFL manifest into a `FusionEntity` and calls `SecureWriteTransaction`. Writes KV, Graph, and Vector atomically.

```go
err := db.Fuse(ctx, fusiondb.UFLManifest{
    Version: "1.0",
    Action:  "fuse",
    Entity: fusiondb.UFLEntity{
        ID:     "person:alice",
        Type:   "Person",
        Tier:   fusiondb.TierVerified,
        Vector: []float32{0.1, 0.9, 0.4},
        KV: map[string]any{
            "full_name": "Alice Johnson",
        },
        Relations: map[string][]fusiondb.UFLRelation{
            "secondary": {
                {Predicate: "has_email", Object: "alice@example.com"},
            },
        },
    },
})
```

### `(*DB) SecureWriteTransaction(ctx context.Context, entity *FusionEntity, embedding []float32) error`

Low-level atomic write. Performs all four steps inside one `badger.DB.Update()` transaction:

1. Encrypts and stores the KV document
2. Writes forward and reverse graph edges for all relations
3. Stores the raw vector bytes
4. Saves the HNSW node and ID mapping

Returns an error (without writing anything) if:
- The circuit breaker is open (`IsCircuitBroken == true`)
- `EmbeddingDims > 0` and `len(embedding) != EmbeddingDims`
- Any step within the transaction fails

```go
err := db.SecureWriteTransaction(ctx, &fusiondb.FusionEntity{
    ID:          "event:login_001",
    Type:        "observation",
    Layer:       fusiondb.LayerVerified,
    Content:     "User login detected",
    Salience:    1.0,
    Reliability: 0.95,
    DecayFactor: 0.1,
    Subjects:    []string{"user:alice"},
    CreatedAt:   time.Now(),
}, []float32{0.1, 0.8, 0.3})
```

### `(*DB) AtomicFusion(ctx context.Context, tier string, nodeID uint64, vector []float32, subject, predicate, object string) error`

Lower-level variant that takes individual fields rather than a `FusionEntity` struct. Suitable for simple triple-based writes where you control the node ID directly.

```go
err := db.AtomicFusion(
    ctx,
    "verified",
    uint64(42),
    []float32{0.14, -0.22, 0.98},
    "ip:10.0.0.1",
    "resolves_to",
    "hostname:evil.example.com",
)
```

### `(*DB) SafeUpdate(ctx context.Context, fn func(txn *badger.Txn) error) error`

Executes a custom transaction function with panic recovery and context cancellation checks. Use when you need direct BadgerDB transaction access.

---

## Reading Data

### `(*DB) UFLQuery(ctx context.Context, query UFLQuery) (*UFLEntity, error)`

The primary query method. Supports:
- **ID lookup**: finds a record by exact entity ID, decrypts it, and optionally hydrates graph relations and vectors
- **Vector search**: finds the nearest neighbor to a query embedding using the HNSW index

```go
// Query by ID
result, err := db.UFLQuery(ctx, fusiondb.UFLQuery{
    Action: "query",
    Selector: fusiondb.UFLSelector{
        ID: "person:alice",
    },
    Options: fusiondb.UFLOptions{
        Hydrate: fusiondb.UFLHydrationOptions{
            MaxDegree:     4,    // Hydrate all four degrees of relations
            IncludeVector: true, // Include the stored embedding in the response
        },
    },
})

// Vector similarity search
result, err := db.UFLQuery(ctx, fusiondb.UFLQuery{
    Action: "query",
    Selector: fusiondb.UFLSelector{
        Vector: &fusiondb.UFLVectorSelector{
            Near:   []float32{0.1, 0.9, 0.4},
            Limit:  10,
        },
    },
})
```

### `(*DB) HybridQueryEngine(ctx context.Context, q HybridSearchQuery) ([]FusionEntity, error)`

Advanced search that combines vector retrieval with graph constraint filtering and Bayesian recency scoring.

```go
type HybridSearchQuery struct {
    QueryText      string      // (reserved for future use)
    QueryEmbedding []float32   // Vector to search against
    Subjects       []string    // Filter: entity must have an "about" edge to all listed subjects
    ContextChat    string      // Filter: entity must have a "context" edge matching this value
    PoolLimit      int         // Number of vector candidates to retrieve (pre-filter pool size)
    TargetLimit    int         // Maximum number of results to return after scoring
}
```

```go
results, err := db.HybridQueryEngine(ctx, fusiondb.HybridSearchQuery{
    QueryEmbedding: []float32{0.1, 0.9, 0.4},
    Subjects:       []string{"user:alice"},
    ContextChat:    "session:chat_001",
    PoolLimit:      50,
    TargetLimit:    5,
})
```

Results are sorted by computed weight descending:
```
weight = (salience × recency_boost × reliability) − (decay_factor × age_days)
recency_boost = exp(−age_days / 7)
```

---

## UFL Interface

### Types

```go
type UFLManifest struct {
    Version string    `json:"ufl_version"`
    Action  string    `json:"action"`
    Entity  UFLEntity `json:"entity"`
}

type UFLEntity struct {
    ID        string                   `json:"id"`
    Type      string                   `json:"type"`
    Tier      string                   `json:"tier"`
    Vector    []float32                `json:"vector,omitempty"`
    KV        map[string]any           `json:"kv"`
    Relations map[string][]UFLRelation `json:"relations,omitempty"`
}

type UFLRelation struct {
    Predicate string `json:"predicate"`
    Object    string `json:"object"`
}

type UFLQuery struct {
    Action   string      `json:"action"`
    Selector UFLSelector `json:"selector"`
    Options  UFLOptions  `json:"options"`
}

type UFLSelector struct {
    ID     string             `json:"id,omitempty"`
    Vector *UFLVectorSelector `json:"vector,omitempty"`
    KV     map[string]any     `json:"kv,omitempty"`
}

type UFLVectorSelector struct {
    Near  []float32 `json:"$near"`
    Limit int       `json:"$limit"`
}

type UFLOptions struct {
    Hydrate UFLHydrationOptions `json:"hydrate"`
}

type UFLHydrationOptions struct {
    MaxDegree     int  `json:"max_degree"`
    IncludeVector bool `json:"include_vector"`
}
```

### Tier Constants

```go
const (
    TierVerified   = "verified"
    TierUnverified = "unverified"
    TierKnowledge  = "knowledge"
)

var UFLTiers = []string{TierVerified, TierUnverified, TierKnowledge}
```

---

## KV Store

`DB.KV` provides direct access to the encrypted key-value store.

### `(*KVStore) Store(ctx, tier, key string, record KnowledgeRecord) error`

Writes a `KnowledgeRecord` to the given tier. Encrypts the payload with AES-256-GCM before writing.

### `(*KVStore) Get(ctx, tier, key string) (KnowledgeRecord, error)`

Reads and decrypts a record. Checks the Ristretto cache first. Returns `ErrNotFound` if the key does not exist.

### `(*KVStore) ScanFilterStream(ctx, tier string, match func(KnowledgeRecord) bool, processor func(KnowledgeRecord) bool) error`

Streams all records in a tier, calling `match` to filter and `processor` to handle each match. Return `false` from `processor` to stop iteration early.

### `(*KVStore) ScanFilter(ctx, tier string, match func(KnowledgeRecord) bool) ([]KnowledgeRecord, error)`

Collects all records matching `match` into a slice. Wrapper around `ScanFilterStream`.

### `(*KVStore) ScanKeyword(ctx, tier, keyword string) ([]KnowledgeRecord, error)`

Case-insensitive keyword search across `Query`, `Result`, `Data`, and `Correction` fields.

### `KnowledgeRecord`

```go
type KnowledgeRecord struct {
    Agent      string
    Type       string
    Query      string
    Result     string
    Correction string
    Data       string
    Meta       map[string]any
}
```

---

## Graph Store

`DB.Graph` provides access to the semantic triple store.

### `(*GraphStore) AddQuadInTier(ctx context.Context, q quad.Quad, tier string) error`

Writes a Cayley `quad.Quad` as forward and reverse edges. Both edges are tagged with the tier byte.

### `(*GraphStore) AddQuadStringsInTxn(txn *badger.Txn, subject, predicate, object, tier string) error`

Transaction-scoped variant. Used inside `SecureWriteTransaction` to ensure atomicity with other layers.

### `(*GraphStore) QuerySubject(ctx context.Context, subject string) ([]quad.Quad, error)`

Returns all forward edges where the given string is the subject.

### `BuildGraphKey(prefix byte, subject, predicate, object string) []byte`

Constructs a deterministic, length-prefixed BadgerDB key for a graph triple. Used for manual key construction when accessing BadgerDB directly.

---

## Vector Store

`DB.Vector` provides access to the HNSW vector index.

### `(*VectorStore) StoreHNSWNode(ctx context.Context, nodeID uint64, vector []float32, neighbors []uint64) error`

Persists an HNSW node with its neighbor list.

### `(*VectorStore) GetHNSWNode(ctx context.Context, nodeID uint64) (vector []float32, neighbors []uint64, err error)`

Retrieves a stored HNSW node. Returns `ErrNotFound` if the node does not exist.

### `(*VectorStore) Search(ctx context.Context, queryVector []float32, k int, ef int) ([]SearchCandidate, error)`

Performs an HNSW approximate nearest-neighbor search.

| Parameter | Description |
|---|---|
| `queryVector` | The query embedding |
| `k` | Number of nearest neighbors to return |
| `ef` | Size of the dynamic candidate list during search (higher = better recall, slower search) |

### `CosineDistance(A, B []float32) (float32, error)`

Exported distance function. Returns cosine distance (0.0 = identical, 1.0 = orthogonal, 2.0 = opposite). Returns an error if the vectors have mismatched or zero dimensions.

### `SearchCandidate`

```go
type SearchCandidate struct {
    NodeID   uint64
    Vector   []float32
    Distance float32
}
```

---

## Encryption Utilities

### `(*SAGEKernel) MaskPII(pii string) string`

Returns the HMAC-SHA256 hex digest of the given PII string, keyed with the derived salt key. Used for privacy-preserving graph index lookups.

```go
masked := db.MaskPII("alice@example.com")
// → "a3f1c2..." (deterministic hex string)
```

### `SecureFuseEntity(txn *badger.Txn, manifest UFLManifestPII, secretKey, saltKey []byte) error`

Low-level PII-aware write function. Encrypts the KV block with AES-256-GCM and masks Degree 2 relation objects with HMAC-SHA256 before writing to BadgerDB.

### `HydrateEntity(txn *badger.Txn, entityID, tier string, maxDegree int, secretKey, saltKey []byte) (map[string]interface{}, error)`

Decrypts the KV block and resolves HMAC-masked graph relations back to their original PII values. Returns a flat map combining KV fields and hydrated relation objects.

---

## Governance and Safety

### `(*SAGEKernel) CircuitBreaker(ctx context.Context, toolError error) error`

Reports a tool error. After three consecutive errors, sets `IsCircuitBroken = true`. A non-nil error increments the counter; a nil error resets it to zero.

```go
if err := db.CircuitBreaker(ctx, someErr); err != nil {
    // Circuit tripped — halt
    return err
}
```

### `(*SAGEKernel) VerifySessionSync(currentSessionID string) error`

Returns an error if the provided session ID does not match `ActiveSessionID`. Set `ActiveSessionID` at session start to enable the guard.

### `(*SAGEKernel) PublishMeshTelemetry(event interface{})`

Non-blocking publish to the telemetry bus channel. Drops the event silently if the channel buffer (capacity 1024) is full.

---

## Constants and Errors

```go
var ErrNotFound = errors.New("fusiondb: record not found")
```

Returned by `KVStore.Get`, `VectorStore.GetHNSWNode`, and `UFLQuery` when the requested key or entity does not exist.

```go
const (
    HNSWMaxNeighbors = 16   // Maximum neighbors per HNSW node
    SeederBatchSize  = 100  // Records committed per transaction during seeding
)
```

### Ontology Degrees

```go
const (
    DegreeUnknown    Degree = 0
    DegreePrimary    Degree = 1
    DegreeSecondary  Degree = 2  // PII — HMAC-masked in graph
    DegreeTertiary   Degree = 3
    DegreeQuaternary Degree = 4  // Default for unknown predicates
)
```

### `GetDegree(predicate string) Degree`

Returns the registered degree for a predicate. Defaults to `DegreeQuaternary` for unknown predicates.

Built-in predicate registrations:

| Predicate | Degree |
|---|---|
| `has_phone` | 2 (Secondary) |
| `has_email` | 2 (Secondary) |
| `owns_vehicle` | 3 (Tertiary) |
| `owns_property` | 3 (Tertiary) |
| `attended_college` | 3 (Tertiary) |
| `referenced_in` | 4 (Quaternary) |
| `involved_in` | 4 (Quaternary) |

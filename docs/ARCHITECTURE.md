# FusionDB Architecture

This document describes the internal design of FusionDB: how storage is organized, how the three layers interact, and the design decisions behind key subsystems.

---

## Table of Contents

1. [Design Philosophy](#design-philosophy)
2. [The SAGEKernel](#the-sagekernel)
3. [BadgerDB as the Unified Backend](#badgerdb-as-the-unified-backend)
4. [Key Namespace Layout](#key-namespace-layout)
5. [Storage Layers](#storage-layers)
   - [KV Store (0x10–0x12)](#kv-store)
   - [Graph Store (0x00–0x01)](#graph-store)
   - [Vector Store (0x02–0x04)](#vector-store)
6. [The Atomic Fusion Transaction](#the-atomic-fusion-transaction)
7. [UFL: Unified Fusion Language](#ufl-unified-fusion-language)
8. [Data Tiers](#data-tiers)
9. [4-Degree Ontology](#4-degree-ontology)
10. [HNSW Vector Index](#hnsw-vector-index)
11. [Hybrid Query Engine](#hybrid-query-engine)
12. [Ristretto Cache Layer](#ristretto-cache-layer)
13. [Circuit Breaker](#circuit-breaker)
14. [Session Sync Guard](#session-sync-guard)
15. [Encryption Architecture](#encryption-architecture)
16. [Observability](#observability)

---

## Design Philosophy

FusionDB is built around one principle: **an entity is not complete until all of its representations are stored and consistent.** A record exists simultaneously as a document (KV), a relationship node (Graph), and a semantic point in vector space (Vector). If any one of those writes fails, none of them should persist.

This eliminates the class of bugs caused by partial writes in multi-store architectures, where a crash between two separate API calls leaves data split across systems.

---

## The SAGEKernel

The `SAGEKernel` (aliased as `DB`) is the central engine. It owns the BadgerDB handle and exposes all storage operations:

```go
type SAGEKernel struct {
    core            *badger.DB       // Single shared BadgerDB instance
    cfg             Config           // Derived encryption and salt keys
    Graph           *GraphStore      // Semantic triple store
    Vector          *VectorStore     // HNSW vector index
    KV              *KVStore         // Encrypted key-value store
    cache           *ristretto.Cache // In-process LRU cache
    BusChannel      chan interface{}  // Non-blocking telemetry channel (1024 cap)
    ActiveSessionID string           // Session sync guard
    IsCircuitBroken bool             // Circuit breaker state
    FailedToolCount int              // Sequential failure counter
}
```

All three sub-stores (`GraphStore`, `VectorStore`, `KVStore`) hold a reference to the same `*badger.DB` handle. This is what makes atomic cross-layer writes possible — all three can participate in the same `db.Update()` transaction.

---

## BadgerDB as the Unified Backend

FusionDB uses a single BadgerDB instance as its physical storage layer. BadgerDB is an LSM-tree key-value store that supports serializable read-write transactions.

Key BadgerDB configuration choices:

| Setting | Default | Reason |
|---|---|---|
| `SyncWrites: true` | enabled | Guarantees durability on every commit |
| `ValueThreshold: 1024` | 1 KB | Large values (e.g., vectors) are stored in value log, not LSM tree |
| `BlockCacheSize: 64 MB` | 64 MB | Caches frequently accessed LSM blocks |
| `IndexCacheSize: 32 MB` | 32 MB | Accelerates Bloom filter lookups |
| `NumMemtables: 3` | 3 | Reduces write stalls under burst ingestion |
| `LoggingLevel: ERROR` | ERROR | Suppresses BadgerDB's verbose INFO output |

---

## Key Namespace Layout

All three storage layers share the same BadgerDB keyspace. Isolation is achieved through a **1-byte prefix** on every key.

| Prefix | Store | Direction / Type |
|---|---|---|
| `0x00` | Graph | Forward edge: subject → predicate → object |
| `0x01` | Graph | Reverse edge: object → predicate → subject |
| `0x02` | Vector | HNSW node data (vector + neighbor list) |
| `0x03` | Vector | Raw vector bytes (fast scan path) |
| `0x04` | Vector | HNSW metadata (entry node ID, max layer) |
| `0x10` | KV | `verified` tier records |
| `0x11` | KV | `unverified` tier records |
| `0x12` | KV | `knowledge` tier records |

ID mapping (vector nodeID ↔ string entity ID) uses prefix `0x04`.

PII reverse lookup records use compound prefix `0x03 0x01`.

---

## Storage Layers

### KV Store

The KV Store holds encrypted JSON documents. Each record is a `KnowledgeRecord`:

```go
type KnowledgeRecord struct {
    Agent      string         // Originating agent or process
    Type       string         // Entity type
    Query      string         // Source query, if applicable
    Result     string         // Result payload
    Data       string         // Primary data field
    Correction string         // Correction or amendment
    Meta       map[string]any // Arbitrary metadata
}
```

**Write path:** The record is JSON-marshaled, encrypted with AES-256-GCM (random IV per write), then stored as a structured envelope:

```json
{
  "type": "...",
  "nonce": "<base64>",
  "payload": "<base64 ciphertext>",
  "salience": 1.0,
  "reliability": 1.0,
  "decay_factor": 0.1,
  "created_at": "2026-01-01T00:00:00Z"
}
```

**Read path:** The `Get()` method checks the Ristretto cache first, then falls back to a BadgerDB `View` transaction. Cached values are stored as raw bytes (pre-decryption); decryption happens on cache miss.

**Scan path:** `ScanFilterStream()` iterates all records under a tier prefix and calls the caller-supplied `match` and `processor` functions, enabling early termination.

---

### Graph Store

The Graph Store implements a bidirectional quad store: every write creates both a **forward edge** and a **reverse edge**.

**Key format** (length-prefixed to avoid collisions on prefix scans):

```
[prefix: 1B][subject_len: 2B][subject][predicate_len: 2B][predicate][object_len: 2B][object]
```

**Forward key** (prefix `0x00`): enables "find all predicates and objects for this subject"
**Reverse key** (prefix `0x01`): enables "find all subjects pointing to this object"

The value stored at each graph key is a single tier byte (`0x10`, `0x11`, or `0x12`), making traversal queries lightweight.

PII graph edges (Degree 2) store the **HMAC-SHA256 hash** of the object, not the raw value. The original value is stored separately in an encrypted reverse-lookup record under prefix `0x03 0x01`.

---

### Vector Store

The Vector Store persists HNSW nodes. Each node contains:
- A `float32` vector (arbitrary dimensions)
- A list of neighbor node IDs (up to `HNSWMaxNeighbors = 16`)

Nodes are serialized in the "CometNode" format:

```
[vector_length: 4B][vector_data: N*4B][neighbor_count: 4B][neighbor_ids: M*8B]
```

**HNSW structure** is stored flat in BadgerDB rather than in memory. This means the index survives process restarts without a rebuild phase. The tradeoff is slightly higher read latency versus an in-memory ANN library, with the benefit of zero warm-up time and zero memory overhead beyond the cache.

The ID mapping table (prefix `0x04`) translates between the `uint64` node IDs used internally by HNSW and the human-readable string entity IDs used by UFL.

---

## The Atomic Fusion Transaction

The `SecureWriteTransaction()` method (and the lower-level `AtomicFusion()`) execute all three layer writes inside a single `db.core.Update()` call:

```
SecureWriteTransaction(ctx, entity, embedding)
│
├── Step A: KVStore.StoreInTxn()         → encrypted KV record
├── Step B: GraphStore.AddQuadStringsInTxn() → forward + reverse graph edges
├── Step C: txn.Set(vecKey, vecBytes)    → raw float32 vector bytes
└── Step D: SaveIDMapping() + InsertHNSWNode() → HNSW node + ID mapping
```

If any step returns an error, the entire `db.core.Update()` call returns without committing. BadgerDB rolls back all changes automatically.

Context cancellation is checked at the entry of the transaction and again inside it, ensuring long-running batches respect deadlines.

---

## UFL: Unified Fusion Language

UFL is the declarative interface for FusionDB. It uses a JSON schema with two action types:

**`fuse`** — write an entity across all three layers
**`query`** — retrieve an entity with optional relationship hydration

The `UFLManifest` and `UFLQuery` structs are the Go representations of these schemas. The `Fuse()` and `UFLQuery()` methods on `DB` are the entry points.

UFL abstracts away the differences between the three storage layers. Callers never need to know which tier byte to use or how graph keys are encoded.

See [UFL Reference](UFL_REFERENCE.md) for the complete schema and examples.

---

## Data Tiers

The three data tiers provide logical isolation within the same BadgerDB instance:

| Tier | Prefix | Semantic meaning |
|---|---|---|
| `verified` | `0x10` | Confirmed, accountable facts. Highest trust. |
| `unverified` | `0x11` | Raw, unconfirmed discoveries. Default when tier is omitted or invalid. |
| `knowledge` | `0x12` | Methods, heuristics, and self-referential operational data. |

Tier isolation is enforced at the key level. A scan of the `verified` tier (`0x10` prefix) will never return records from `unverified` (`0x11`) or `knowledge` (`0x12`) tiers. Tiers cannot be promoted silently — the `tierByte()` function defaults to `unverified` for any unrecognized string.

---

## 4-Degree Ontology

Relationship predicates are assigned a degree based on a registered map:

| Degree | Name | Registered predicates |
|---|---|---|
| 1 | Primary | (implicit — the entity's own KV and vector) |
| 2 | Secondary | `has_phone`, `has_email` |
| 3 | Tertiary | `owns_vehicle`, `owns_property`, `attended_college` |
| 4 | Quaternary | `referenced_in`, `involved_in` (and all unknowns) |

Degree 2 predicates are treated as PII and receive HMAC masking in the graph index. Unknown predicates default to Degree 4 (least gravity, no PII treatment).

When hydrating a query with `max_degree: 2`, only Degree 1 and Degree 2 relations are returned, and Degree 2 PII values are decrypted from the reverse-lookup table for the response.

---

## HNSW Vector Index

FusionDB implements HNSW (Hierarchical Navigable Small World) as a persistent graph embedded in BadgerDB.

**Distance metric:** Cosine distance — computed as `1 − cosine_similarity`. This makes the index suitable for embedding vectors from language models (which are typically compared by cosine similarity).

**Node insertion (`InsertHNSWNode`):** When a new node is inserted, its nearest neighbors are found via a greedy graph search starting from the current entry node. The node is then written with its neighbor list. The entry node metadata is updated if the new node's layer exceeds the current maximum.

**Search (`SearchHNSWGraph`):** A greedy best-first traversal starting from the entry node, exploring neighbors layer by layer. Results are collected into a max-heap and trimmed to the requested `k` nearest neighbors.

**Linear scan fallback (`LinearScanFallback`):** Used inside `HybridQueryEngine` when the HNSW index is small or a full-precision scan is required. Scans all nodes under the vector prefix and computes cosine distance against the query vector.

**Maximum neighbors:** `HNSWMaxNeighbors = 16`. Increasing this value improves recall at the cost of more edges per node and higher insertion cost.

---

## Hybrid Query Engine

`HybridQueryEngine()` combines vector search with graph-constraint filtering:

```
1. Vector candidate retrieval (LinearScanFallback or HNSW)
2. Graph constraint filtering:
   a. Subject filter: entity must have an "about" edge to each required subject
   b. Context filter: entity must have a "context" edge matching ContextChat
3. KV decryption and content extraction
4. Bayesian recency scoring:
   weight = (salience × recency_boost × reliability) − decay_penalty
   where recency_boost = exp(−age_days / 7)  [7-day half-life]
5. Sort by weight descending
6. Truncate to TargetLimit
```

The scoring formula is a Bayesian-inspired relevance model: it boosts recently created records and penalizes records with high decay factors or low reliability scores.

---

## Ristretto Cache Layer

The KV Store uses a Ristretto LRU cache as a read-through layer:

| Parameter | Value |
|---|---|
| `NumCounters` | 1,000,000 |
| `MaxCost` | 128 MB (configurable via `Options.RistrettoMaxCost`) |
| `BufferItems` | 64 |
| `Cost function` | `len(value)` bytes |

On a cache hit, deserialization happens from the cached bytes without touching BadgerDB. On a cache miss, the value is read from BadgerDB and inserted into the cache.

Cache entries are invalidated immediately on `StoreInTxn()` to prevent stale reads after a write.

---

## Circuit Breaker

The circuit breaker (`CircuitBreaker()`) tracks sequential tool execution failures. After three consecutive failures, `IsCircuitBroken` is set to `true` and all further `SecureWriteTransaction()` calls are rejected with:

```
database transaction rejected: central circuit breaker is open
```

A successful operation resets the failure counter to zero. This prevents cascading failures in automated pipelines where a broken state could cause an agent to loop indefinitely.

---

## Session Sync Guard

`VerifySessionSync()` checks that the caller's session ID matches the `ActiveSessionID` recorded in the kernel. If they differ, the call returns:

```
DESYNC_PROTOCOL_TRIGGERED: Disconnected from active session context. Halting thread
```

This guard protects against prompt injection and mid-session context drift in multi-agent environments where multiple goroutines or processes share a database handle.

---

## Encryption Architecture

See [Security Guide](SECURITY.md) for the full encryption model. In brief:

1. `FUSIONDB_SECRET` is read from the environment at startup.
2. Two independent keys are derived via HMAC-SHA256:
   - `secretKey` (for AES-256-GCM encryption)
   - `saltKey` (for HMAC-SHA256 PII masking)
3. The raw secret never touches the database.
4. Each KV write generates a fresh random IV (nonce). No two ciphertexts share an IV, even for identical plaintext.
5. Degree 2 graph edges store `HMAC-SHA256(saltKey, plaintext_pii)` rather than the raw value.

---

## Observability

The `serve` command starts an HTTP server with two endpoints:

**`GET /healthz`**
Returns `200 ok` when the process is alive. No database check.

**`GET /readyz`**
Returns `200 ok` when:
- The BadgerDB `LOCK` file exists at the database path (indicating the engine has started)
- Disk usage on the database volume is below 90%

Returns `503` with a descriptive message if either check fails.

The telemetry bus channel (`BusChannel`, capacity 1024) is available for internal event publishing. The non-blocking `PublishMeshTelemetry()` method drops frames rather than blocking the main execution thread if the channel is saturated.

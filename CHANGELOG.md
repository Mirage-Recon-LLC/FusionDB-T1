# Changelog

All notable changes to FusionDB are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versions follow [Semantic Versioning](https://semver.org/).

---

## [1.0.0] — 2026-06-20

### Added

**Core engine**
- `SAGEKernel` — unified database handle holding a single BadgerDB instance shared by all three storage layers
- `Open(path string) (*DB, error)` — simple constructor with environment-based key loading
- `OpenAdvanced(opts Options) (*SAGEKernel, error)` — full options constructor with explicit key injection
- Atomic tri-modal write: all three layers (KV, Graph, Vector) committed in a single `badger.DB.Update()` transaction
- Ristretto v2 in-process LRU cache over BadgerDB reads (default 128 MB, configurable)

**KV storage**
- AES-256-GCM encryption of all KV payloads; fresh random nonce per write via `crypto/rand`
- Envelope format: `type`, `nonce`, `payload`, `salience`, `reliability`, `decay_factor`, `created_at`
- `Get()` with Ristretto-first read path; BadgerDB fallback on miss
- `ScanFilterStream()` for streaming filtered reads across a tier prefix
- Three byte-prefix-isolated data tiers: `verified` (`0x10`), `unverified` (`0x11`), `knowledge` (`0x12`)

**Graph storage**
- Bidirectional quad store: forward edges at prefix `0x00`, reverse edges at `0x01`
- Length-prefixed key format for subjects, predicates, and objects
- `QuerySubject()` for forward-edge scan
- `SecureFuseEntity()` for degree-aware graph writes with PII masking

**Vector storage**
- HNSW approximate nearest-neighbor index backed by BadgerDB
- Cosine distance metric; `HNSWMaxNeighbors = 16`
- CometNode binary serialization: `[vecLen:4B][vecData:N*4B][neighborCount:4B][neighborIDs:M*8B]`
- HNSW data at prefix `0x02`; node ID mapping at `0x03`; reverse at `0x04`

**4-Degree Ontology**
- Degree 1 (Primary): entity identity anchor
- Degree 2 (Secondary / PII): `has_email`, `has_phone` — HMAC-SHA256 masked in graph index; encrypted in reverse-lookup at prefix `0x03 0x01`
- Degree 3 (Tertiary): `owns_vehicle`, `owns_property`, `attended_college` — stored as plaintext graph edges
- Degree 4 (Quaternary): `referenced_in`, `involved_in`, all unknown predicates — loose references
- `HydrateEntity()` resolves all degrees, decrypts PII from reverse lookup
- `MaskPII(pii string) string` — deterministic HMAC-SHA256 mask using the derived salt key

**Key derivation**
- `deriveKeys(master []byte) (secretKey, saltKey []byte)` — HMAC-SHA256 with domain-separated labels (`fusiondb-secret-v1`, `fusiondb-salt-v1`)
- `FUSIONDB_SECRET` environment variable required; must decode to ≥ 32 bytes
- Startup validation with structured log error and immediate exit on insufficient entropy

**Hybrid Query Engine**
- `HybridQueryEngine(ctx, HybridSearchQuery) ([]FusionEntity, error)`
- Bayesian scoring: `weight = (salience × recency_boost × reliability) - decay_penalty`
- `recency_boost = exp(-age_days / 7)`

**Unified Fusion Language (UFL)**
- JSON-based declarative interface for all reads and writes
- `Fuse(ctx, UFLManifest) error` — write path
- `UFLQuery(ctx, UFLQuery) (*FusionEntity, error)` — read path; supports `selector.id` and `selector.vector.$near`
- `UFLManifest`, `UFLEntity`, `UFLQuery`, `UFLSelector`, `UFLOptions`, `UFLHydrationOptions` types

**Multi-format seeding**
- `ParseMarkdownManifest(data []byte) (*UFLManifest, error)` — YAML frontmatter + body as `description` KV field
- `ParseExcelManifests(data []byte) ([]*UFLManifest, error)` — sheet named `Entities`; reserved columns `_id`, `_type`, `_tier`, `_vector`
- Batch commit at `SeederBatchSize = 100` to prevent transaction overflow

**Governance**
- Circuit breaker: `CircuitBreaker(ctx, error) error` — halts after 3 sequential failures; resets on success
- Session sync guard: `VerifySessionSync(sessionID string) error` — rejects cross-session operations when `ActiveSessionID` is set
- `BusChannel chan interface{}` — async event bus for telemetry

**CLI (`fusiondb`)**
- `store` — single entity write via command-line flags
- `query` — forward graph index lookup by subject
- `ufl` — parse and fuse a single UFL manifest JSON file
- `seed` — recursive directory ingestion of JSON, Markdown, and Excel files (`--max-depth`, `--no-recursion` flags); skips `.git-app-internal` directories
- `serve` — HTTP observability server (`--port` flag)
  - `GET /healthz` — liveness probe (always 200)
  - `GET /readyz` — readiness probe; checks LOCK file and disk < 90% full

**Observability**
- Structured JSON logging via `log/slog`
- Health and readiness HTTP endpoints

**Installer**
- `fusiondb-installer` binary for Windows self-update / bootstrap
- `fusiondb-bootstrap-linux` for Linux

**Documentation**
- `README.md` — project overview, quick start, feature table, architecture diagram
- `docs/ARCHITECTURE.md` — internal design, key namespace, storage layers, scoring, encryption
- `docs/QUICKSTART.md` — step-by-step getting started guide
- `docs/CLI_REFERENCE.md` — complete CLI command and flag reference
- `docs/API_REFERENCE.md` — full Go API reference
- `docs/SECURITY.md` — encryption model, key management, threat model
- `docs/UFL_REFERENCE.md` — UFL language reference with full schema and examples

---

[1.0.0]: https://github.com/Mirage-Recon-LLC/FusionDB-T1/releases/tag/v1.0.0

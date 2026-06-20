# FusionDB

**A unified, multi-modal embedded database engine for Go.**

FusionDB stores every entity atomically across three storage layers — key-value, semantic graph, and vector — inside a single BadgerDB transaction. Every record is encrypted at rest with AES-256-GCM. PII fields are masked with HMAC-SHA256 before they ever touch the index.

---

## What Makes FusionDB Different

Most embedded databases pick one model. FusionDB fuses all three in one atomic write:

| Layer | What it stores | Use case |
|---|---|---|
| **KV Store** | Encrypted JSON documents | Fast point lookups, metadata, audit fields |
| **Graph Store** | Semantic triples (subject → predicate → object) | Relationship traversal, entity linking |
| **Vector Store** | Float32 embeddings via HNSW | Similarity search, RAG retrieval, semantic ranking |

A single `Fuse()` call writes all three layers or rolls them all back. There is no way to get a partial write.

---

## Features

- **Atomic tri-modal writes** — KV + Graph + Vector in a single BadgerDB transaction
- **AES-256-GCM encryption at rest** — all KV payloads encrypted before touching disk
- **HMAC-SHA256 PII masking** — Degree 2 identifiers (emails, phones) stored as one-way hashes in the graph index
- **HNSW approximate nearest-neighbor search** — cosine-distance vector index backed by BadgerDB, no external service required
- **Three data tiers** — `verified`, `unverified`, and `knowledge` with byte-prefix isolation
- **4-Degree ontology** — structured gravity model for entity relationships (Primary → Secondary → Tertiary → Quaternary)
- **Bayesian hybrid scoring** — results ranked by salience × recency × reliability − decay
- **Ristretto LRU cache** — configurable in-process cache over BadgerDB reads
- **UFL (Unified Fusion Language)** — JSON-based interface for all reads and writes
- **Multi-format seeding** — bulk-ingest JSON manifests, Markdown (YAML frontmatter), or Excel files
- **Circuit breaker** — halts on three sequential failures to prevent cascading loops
- **HTTP observability** — `/healthz` and `/readyz` endpoints with disk threshold checks
- **Cross-platform** — ships as a single self-contained binary for Windows and Linux

---

## Installation

### Download a pre-built binary

Binaries are provided for Windows (`.exe`) and Linux in each release.

**Windows:**
```
fusiondb-installer.exe
```
The installer bootstraps the runtime and places `fusiondb.exe` on your PATH.

**Linux:**
```bash
chmod +x fusiondb-bootstrap-linux
./fusiondb-bootstrap-linux
```

### Build from source

Requires Go 1.21+.

```bash
git clone https://github.com/Mirage-Recon-LLC/FusionDB-T1
cd FusionDB-T1
go build -o fusiondb ./cmd/fusiondb
```

---

## Quick Start

### 1. Set your encryption key

FusionDB requires a 32-byte master key. Generate one and export it:

```bash
# Generate a secure 32-byte hex key (64 hex chars)
openssl rand -hex 32
# Example output: a3f1c2d4e5b6a7f8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2

export FUSIONDB_SECRET=a3f1c2d4e5b6a7f8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2
```

> **Keep this key safe.** Data cannot be decrypted without it. Store it in a secrets manager, not in source control.

### 2. Fuse an entity via UFL manifest

Create a file called `manifest.json`:

```json
{
  "ufl_version": "1.0",
  "action": "fuse",
  "entity": {
    "id": "person:jane_smith",
    "type": "Person",
    "tier": "verified",
    "vector": [0.12, -0.05, 0.88, 0.34],
    "kv": {
      "full_name": "Jane Smith",
      "department": "Engineering",
      "active": true
    },
    "relations": {
      "secondary": [
        { "predicate": "has_email", "object": "jane@example.com" }
      ],
      "tertiary": [
        { "predicate": "owns_vehicle", "object": "vin:XYZ789" }
      ]
    }
  }
}
```

Fuse it:

```bash
fusiondb -db=./mydata ufl manifest.json
```

### 3. Query the graph

```bash
fusiondb -db=./mydata query person:jane_smith
```

Output:
```
Found 2 semantic graph relations:
  (person:jane_smith) --[has_email]--> (jane@example.com)
  (person:jane_smith) --[owns_vehicle]--> (vin:XYZ789)
```

### 4. Seed from a directory

Place JSON, Markdown, or Excel files in a folder and bulk-ingest them:

```bash
fusiondb -db=./mydata seed ./my_data_folder
```

### 5. Start the observability server

```bash
fusiondb -db=./mydata serve --port 8080
```

- `GET /healthz` — returns `200 ok` when the process is running
- `GET /readyz` — returns `200 ok` when the database LOCK file exists and disk usage is below 90%

---

## Using FusionDB as a Go Library

```go
package main

import (
    "context"
    "log"
    "os"

    "github.com/Mirage-Recon-LLC/FusionDB-T1"
)

func main() {
    os.Setenv("FUSIONDB_SECRET", "your-64-char-hex-key-here...")

    db, err := fusiondb.Open("./mydata")
    if err != nil {
        log.Fatal(err)
    }
    defer db.Close()

    ctx := context.Background()

    // Write an entity across all three layers atomically
    err = db.Fuse(ctx, fusiondb.UFLManifest{
        Version: "1.0",
        Action:  "fuse",
        Entity: fusiondb.UFLEntity{
            ID:     "device:sensor_42",
            Type:   "IoTDevice",
            Tier:   "verified",
            Vector: []float32{0.1, 0.9, 0.4, 0.6},
            KV: map[string]any{
                "location": "Building A",
                "status":   "online",
            },
        },
    })
    if err != nil {
        log.Fatal(err)
    }

    // Query by ID
    result, err := db.UFLQuery(ctx, fusiondb.UFLQuery{
        Action: "query",
        Selector: fusiondb.UFLSelector{
            ID: "device:sensor_42",
        },
        Options: fusiondb.UFLOptions{
            Hydrate: fusiondb.UFLHydrationOptions{MaxDegree: 4},
        },
    })
    if err != nil {
        log.Fatal(err)
    }
    log.Printf("Found: %s (tier: %s)", result.ID, result.Tier)
}
```

---

## Documentation

| Document | Description |
|---|---|
| [Quick Start](docs/QUICKSTART.md) | Install, configure, and run your first queries |
| [Architecture](docs/ARCHITECTURE.md) | Storage design, key namespacing, HNSW, scoring engine |
| [UFL Reference](docs/UFL_REFERENCE.md) | Unified Fusion Language schema, actions, and examples |
| [CLI Reference](docs/CLI_REFERENCE.md) | All CLI commands, flags, and usage examples |
| [Go API Reference](docs/API_REFERENCE.md) | Full Go package documentation |
| [Security Guide](docs/SECURITY.md) | Encryption model, key management, PII masking, threat model |

---

## Architecture Overview

```
                    ┌─────────────────────────────┐
                    │         SAGEKernel           │
                    │   (Unified Database Engine)  │
                    └──────────────┬──────────────┘
                                   │ single BadgerDB instance
          ┌────────────────────────┼────────────────────────┐
          │                        │                        │
   ┌──────▼──────┐         ┌───────▼──────┐         ┌──────▼──────┐
   │  KV Store   │         │ Graph Store  │         │Vector Store │
   │  (0x10-12)  │         │  (0x00-01)  │         │  (0x02-04)  │
   │  AES-256-   │         │  Semantic    │         │  HNSW +     │
   │  GCM enc.   │         │  Triples     │         │  Cosine     │
   └─────────────┘         └─────────────┘         └─────────────┘
```

All three layers write inside one serializable BadgerDB transaction. A failure in any step rolls back all three.

See [Architecture](docs/ARCHITECTURE.md) for the complete design document.

---

## Data Tiers

FusionDB isolates data into three tiers, each with a unique byte prefix in the key namespace:

| Tier | Prefix | Purpose |
|---|---|---|
| `verified` | `0x10` | Accountable, audited facts |
| `unverified` | `0x11` | Raw, unchecked discoveries |
| `knowledge` | `0x12` | Methods, heuristics, and self-referential data |

---

## 4-Degree Ontology

Entity relationships are organized by "gravity":

| Degree | Name | Examples |
|---|---|---|
| 1 | Primary | Core node — anchors the KV document and vector embedding |
| 2 | Secondary | PII identifiers — emails, phones, government IDs (HMAC-masked in the graph) |
| 3 | Tertiary | Relational assets — vehicles, properties, affiliations |
| 4 | Quaternary | Loose references — external documents, mentions (default for unknown predicates) |

---

## Security

FusionDB encrypts all KV payloads with **AES-256-GCM** before writing to disk. Degree 2 (PII) graph edges are stored as **HMAC-SHA256 hashes** of the original value. The original value is stored in a separate reverse-lookup record, also encrypted.

The master key is derived from `FUSIONDB_SECRET` via two independent HMAC-SHA256 derivations — one for encryption, one for PII salting. The raw secret never touches the database.

See [Security Guide](docs/SECURITY.md) for the complete threat model.

---

## License

Copyright (c) 2026 Johnny Harvey. All rights reserved.

This software is proprietary. No part of this software may be reproduced, distributed, or transmitted in any form or by any means without the prior written permission of the author.
# FusionDB-T1

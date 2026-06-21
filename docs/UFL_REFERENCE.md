# Unified Fusion Language (UFL) Reference

UFL is the declarative interface for FusionDB. All reads and writes go through UFL, whether via the CLI, the Go API, or direct JSON files.

---

## Table of Contents

1. [Core Concepts](#core-concepts)
2. [Fuse Action (Write)](#fuse-action-write)
3. [Query Action (Read)](#query-action-read)
4. [Data Tiers](#data-tiers)
5. [4-Degree Ontology](#4-degree-ontology)
6. [Multi-Format Seeding](#multi-format-seeding)
7. [Full Examples](#full-examples)

---

## Core Concepts

### Entity

An entity is the fundamental unit in UFL. Every entity has:
- A unique **ID** (namespaced string, e.g., `person:alice`, `device:sensor_42`)
- A **type** (e.g., `Person`, `Document`, `IoTDevice`)
- A **tier** (`verified`, `unverified`, or `knowledge`)
- An optional **vector** (float32 embedding)
- A **KV block** (arbitrary JSON metadata)
- Optional **relations** (graph edges to other entities or values)

When an entity is fused, all of its components are written atomically — if any layer fails, none persist.

### The Storage Triad

Every UFL entity exists simultaneously across three layers:

| Layer | What is stored | Accessed via |
|---|---|---|
| KV | Encrypted JSON metadata | `selector.id` query |
| Graph | Semantic triples (subject → predicate → object) | `GraphStore.QuerySubject` / hydration |
| Vector | Float32 embedding in HNSW index | `selector.vector.$near` query |

---

## Fuse Action (Write)

A Fusion Manifest is a JSON document with `"action": "fuse"`.

### Schema

```json
{
  "ufl_version": "1.0",
  "action": "fuse",
  "entity": {
    "id":     "string",
    "type":   "string",
    "tier":   "verified | unverified | knowledge",
    "vector": [float32, float32, ...],
    "kv": {
      "key": "any JSON value"
    },
    "relations": {
      "secondary":   [{ "predicate": "string", "object": "string" }],
      "tertiary":    [{ "predicate": "string", "object": "string" }],
      "quaternary":  [{ "predicate": "string", "object": "string" }]
    }
  }
}
```

### Field Reference

| Field | Required | Description |
|---|---|---|
| `ufl_version` | yes | Must be `"1.0"` |
| `action` | yes | Must be `"fuse"` for writes |
| `entity.id` | yes | Unique entity identifier. Use namespaced format: `type:identifier` |
| `entity.type` | yes | Entity type label. Free-form string. |
| `entity.tier` | yes | Data tier. Invalid values default to `unverified`. |
| `entity.vector` | no | Embedding vector. If omitted, the vector layer is still updated (empty vector). |
| `entity.kv` | yes | JSON object with arbitrary metadata fields. |
| `entity.relations` | no | Map of relation groups. Keys can be any string, but `secondary`, `tertiary`, and `quaternary` activate degree-based behavior. |
| `entity.relations[*][].predicate` | yes (if relations present) | The relationship type. Determines degree if registered. |
| `entity.relations[*][].object` | yes (if relations present) | The target entity ID or value. |

### Behavior

- **KV write:** The `kv` block is encrypted with AES-256-GCM and stored under the tier prefix
- **Graph write:** Each relation produces a forward edge (`subject → predicate → object`) and a reverse edge (`object → predicate → subject`)
- **PII masking:** Relations under the `secondary` group where the predicate is registered as Degree 2 (`has_email`, `has_phone`) will have their `object` HMAC-masked in the graph index
- **Vector write:** The `vector` array is stored as raw float32 bytes and also inserted into the HNSW index

---

## Query Action (Read)

A UFL Query returns an entity by ID or by vector similarity.

### Schema

```json
{
  "action": "query",
  "selector": {
    "id":     "string",
    "vector": { "$near": [float32, ...], "$limit": int },
    "kv":     { "field": "value" }
  },
  "options": {
    "hydrate": {
      "max_degree":     int,
      "include_vector": bool
    }
  }
}
```

### Selector Field Reference

Provide exactly one of `id` or `vector`:

| Field | Description |
|---|---|
| `selector.id` | Exact entity ID lookup. Searches all three tiers in order: `verified`, `unverified`, `knowledge`. Returns the first match. |
| `selector.vector.$near` | Float32 embedding. Returns the entity whose stored vector is nearest (by cosine distance) to this query vector. |
| `selector.vector.$limit` | Number of HNSW candidates to retrieve before applying filters. Defaults to `10`. |

### Options Field Reference

| Field | Default | Description |
|---|---|---|
| `options.hydrate.max_degree` | `0` | Maximum relationship degree to hydrate. `0` = no relations, `4` = all degrees. |
| `options.hydrate.include_vector` | `false` | If `true`, include the stored float32 embedding in the response. |

### Response

```json
{
  "id":     "string",
  "type":   "string",
  "tier":   "string",
  "vector": [float32, ...],
  "kv":     { ... },
  "relations": {
    "secondary":  [{ "predicate": "...", "object": "..." }],
    "tertiary":   [{ "predicate": "...", "object": "..." }],
    "quaternary": [{ "predicate": "...", "object": "..." }]
  }
}
```

Degree 2 (`secondary`) relation objects are returned as their **original PII values**, decrypted from the reverse-lookup table. The caller never sees the HMAC hash in query results.

---

## Data Tiers

| Tier | Purpose | Prefix |
|---|---|---|
| `verified` | Confirmed, accountable facts | `0x10` |
| `unverified` | Raw, unchecked discoveries | `0x11` |
| `knowledge` | Methods, heuristics, self-referential data | `0x12` |

Tiers are isolated at the byte-prefix level. A scan of `verified` will never return `unverified` records.

Invalid tier strings silently default to `unverified`. This is intentional — FusionDB never silently promotes data to a higher-trust tier.

---

## 4-Degree Ontology

The degree system classifies how closely a relation is tied to the entity's primary identity:

| Degree | Name | Behavior | Examples |
|---|---|---|---|
| 1 | Primary | The entity itself. KV document and vector are anchored here. | The `entity.id` node |
| 2 | Secondary | PII identifiers. HMAC-masked in the graph index. Encrypted in reverse lookup. | `has_email`, `has_phone` |
| 3 | Tertiary | Relational assets. Stored plaintext in graph edges. | `owns_vehicle`, `owns_property`, `attended_college` |
| 4 | Quaternary | Loose references. Default for all unknown predicates. | `referenced_in`, `involved_in` |

**Registered predicates and their degrees:**

| Predicate | Degree |
|---|---|
| `has_email` | 2 (Secondary / PII) |
| `has_phone` | 2 (Secondary / PII) |
| `owns_vehicle` | 3 (Tertiary) |
| `owns_property` | 3 (Tertiary) |
| `attended_college` | 3 (Tertiary) |
| `referenced_in` | 4 (Quaternary) |
| `involved_in` | 4 (Quaternary) |
| *(any unknown)* | 4 (Quaternary) |

To use Degree 2 PII masking, place the relation in the `secondary` group **and** use one of the registered Degree 2 predicates (`has_email`, `has_phone`). The masking is applied based on the predicate degree, not the group name.

---

## Multi-Format Seeding

The `seed` command (CLI) and `ParseMarkdownManifest` / `ParseExcelManifests` (Go API) accept three file formats.

### JSON Manifests

A standard UFL manifest file (see [Fuse Action](#fuse-action-write)). Any number of manifests can be placed in a directory.

### Markdown Files

Markdown files use YAML frontmatter to define the entity. The document body becomes the `description` field in the KV block.

**Structure:**

```markdown
---
entity:
  id: "doc:whitepaper_2026"
  type: "Document"
  tier: "knowledge"
action: fuse
---

This whitepaper discusses the architecture of multi-modal databases...
The full body text is stored in the `description` KV field.
```

**Rules:**
- The frontmatter must be delimited by `---` markers
- `entity.id`, `entity.type`, and `entity.tier` must be present
- The body is trimmed of leading/trailing whitespace before storage
- `ufl_version` and `entity.vector` may be included in the frontmatter

### Excel Files (.xlsx)

Excel files must contain a sheet named `Entities`. The first row is treated as a header.

**Reserved columns** (mapped to entity fields):

| Column name | Maps to |
|---|---|
| `_id` | `entity.id` |
| `_type` | `entity.type` |
| `_tier` | `entity.tier` |
| `_vector` | `entity.vector` (comma-separated floats) |

**All other columns** are mapped as KV metadata fields using the column header as the key.

Example sheet layout:

| `_id` | `_type` | `_tier` | `_vector` | `full_name` | `department` |
|---|---|---|---|---|---|
| person:alice | Person | verified | 0.1,0.9,0.4 | Alice Johnson | Engineering |
| person:bob | Person | verified | 0.3,0.7,0.6 | Bob Chen | Research |

---

## Full Examples

### Person entity with PII

```json
{
  "ufl_version": "1.0",
  "action": "fuse",
  "entity": {
    "id": "person:john_doe",
    "type": "Person",
    "tier": "verified",
    "vector": [0.12, -0.05, 0.88, 0.34, 0.61, 0.02],
    "kv": {
      "full_name": "John Michael Doe",
      "date_of_birth": "1985-03-15",
      "active": true,
      "risk_score": 0.3
    },
    "relations": {
      "secondary": [
        { "predicate": "has_email", "object": "jdoe@example.com" },
        { "predicate": "has_phone", "object": "+1-555-0100" }
      ],
      "tertiary": [
        { "predicate": "owns_vehicle", "object": "vin:1HGCM82633A123456" },
        { "predicate": "owns_property", "object": "addr:123_main_st" }
      ],
      "quaternary": [
        { "predicate": "referenced_in", "object": "doc:incident_report_001" }
      ]
    }
  }
}
```

### IoT device entity

```json
{
  "ufl_version": "1.0",
  "action": "fuse",
  "entity": {
    "id": "device:sensor_42",
    "type": "IoTDevice",
    "tier": "unverified",
    "vector": [0.55, 0.12, 0.88, 0.03],
    "kv": {
      "manufacturer": "AcmeSensors",
      "firmware_version": "3.1.4",
      "location": "Building-B/Floor-2/Room-201",
      "last_seen": "2026-06-20T12:00:00Z"
    }
  }
}
```

### Query by ID with full hydration

```json
{
  "action": "query",
  "selector": {
    "id": "person:john_doe"
  },
  "options": {
    "hydrate": {
      "max_degree": 4,
      "include_vector": false
    }
  }
}
```

### Query by vector similarity

```json
{
  "action": "query",
  "selector": {
    "vector": {
      "$near": [0.11, -0.04, 0.89, 0.33, 0.60, 0.03],
      "$limit": 5
    }
  },
  "options": {
    "hydrate": {
      "max_degree": 1,
      "include_vector": true
    }
  }
}
```

### Markdown entity (document)

```markdown
---
entity:
  id: "doc:threat_report_q2_2026"
  type: "ThreatReport"
  tier: "knowledge"
action: fuse
---

# Q2 2026 Threat Intelligence Report

This report summarizes active threat actors observed during Q2 2026.
Indicators of compromise are cross-referenced with the verified entity graph.
```

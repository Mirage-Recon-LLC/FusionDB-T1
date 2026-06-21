# FusionDB CLI Reference

All commands share a common `-db` flag that specifies the database directory path.

```
fusiondb -db=[path] [command] [flags] [args]
```

If `-db` is omitted, it defaults to `./fusion_data`.

---

## Global Flag

| Flag | Default | Description |
|---|---|---|
| `-db` | `./fusion_data` | Path to the FusionDB data directory. Created automatically if it does not exist. |

---

## Commands

- [`store`](#store) — Write a single entity via raw flags
- [`query`](#query) — Query all graph relations for a subject
- [`ufl`](#ufl) — Fuse a single UFL manifest JSON file
- [`seed`](#seed) — Bulk-ingest a directory of JSON, Markdown, and Excel files
- [`serve`](#serve) — Start the HTTP observability server

---

## `store`

Write a single entity across all three storage layers (KV, Graph, Vector) using command-line flags.

**Usage:**

```bash
fusiondb -db=[path] store \
  -tier [tier] \
  -node [uint64] \
  -vector [float32,...] \
  -subject [string] \
  -predicate [string] \
  -object [string]
```

**Flags:**

| Flag | Required | Description |
|---|---|---|
| `-tier` | yes | Data tier: `verified`, `unverified`, or `knowledge` |
| `-node` | yes | Unique numeric node ID (uint64). Used as the HNSW graph node identifier. |
| `-vector` | yes | Comma-separated float32 values for the embedding vector. Example: `0.14,-0.22,0.98,0.05` |
| `-subject` | yes | The entity ID (graph subject). Example: `person:john_doe` |
| `-predicate` | yes | The relationship type (graph predicate). Example: `resolves_to` |
| `-object` | yes | The related entity or value (graph object). Example: `malicious-host` |

**Examples:**

```bash
# Store a threat intelligence record
fusiondb -db=./threat_data store \
  -tier verified \
  -node 420 \
  -vector "0.14,-0.22,0.98,0.05" \
  -subject "ip:192.168.1.100" \
  -predicate "resolves_to" \
  -object "malicious-host.example.com"

# Store a person entity
fusiondb -db=./mydata store \
  -tier verified \
  -node 1001 \
  -vector "0.12,-0.05,0.88,0.34,0.61" \
  -subject "person:jane_doe" \
  -predicate "works_at" \
  -object "org:acme_corp"
```

**Notes:**
- All four string flags (`-tier`, `-subject`, `-predicate`, `-object`) are required. The command exits with an error if any are missing.
- The node ID must be unique per entity. Reusing a node ID for a different entity will overwrite the existing HNSW mapping.
- For entities with multiple relations or KV metadata, use the `ufl` command with a manifest file instead.

---

## `query`

Query all semantic graph relations where the given string is the subject.

**Usage:**

```bash
fusiondb -db=[path] query [subject]
```

**Arguments:**

| Argument | Required | Description |
|---|---|---|
| `subject` | yes | The entity ID to query. Example: `person:alice` |

**Output:**

```
Found N semantic graph relations:
  (subject) --[predicate]--> (object)
  ...
```

**Examples:**

```bash
# Query all relations for a person
fusiondb -db=./mydata query person:alice

# Query an IP address's graph relations
fusiondb -db=./threat_data query ip:10.0.0.1
```

**Notes:**
- This command queries the **forward graph index** only. It does not perform KV lookups or vector searches.
- Returned relations are not filtered by tier.
- PII values stored at Degree 2 (using HMAC masking) will appear as their HMAC hash, not the original value. Use the Go API (`HydrateEntity`) for PII-resolved hydration.

---

## `ufl`

Parse and fuse a single UFL manifest JSON file into the database.

**Usage:**

```bash
fusiondb -db=[path] ufl [json_file]
```

**Arguments:**

| Argument | Required | Description |
|---|---|---|
| `json_file` | yes | Path to a UFL manifest JSON file |

**Examples:**

```bash
fusiondb -db=./mydata ufl ./manifests/person_alice.json
fusiondb -db=./mydata ufl manifest.json
```

**Manifest format:**

```json
{
  "ufl_version": "1.0",
  "action": "fuse",
  "entity": {
    "id": "person:alice",
    "type": "Person",
    "tier": "verified",
    "vector": [0.12, -0.05, 0.88],
    "kv": {
      "full_name": "Alice Johnson"
    },
    "relations": {
      "secondary": [
        { "predicate": "has_email", "object": "alice@example.com" }
      ]
    }
  }
}
```

See [UFL Reference](UFL_REFERENCE.md) for the complete schema.

---

## `seed`

Bulk-ingest a directory of files. Supports `.json` (UFL manifests), `.md` (Markdown with YAML frontmatter), and `.xlsx` (Excel) files. Recurses into subdirectories by default.

**Usage:**

```bash
fusiondb -db=[path] seed [flags] [directory]
```

**Flags:**

| Flag | Default | Description |
|---|---|---|
| `--max-depth` | `-1` (unlimited) | Maximum directory recursion depth. `0` means only the immediate directory. `-1` means recurse without limit. |
| `--no-recursion` | `false` | Process only files in the immediate directory. Equivalent to `--max-depth 0`. |

**Arguments:**

| Argument | Required | Description |
|---|---|---|
| `directory` | yes | Path to the directory to scan |

**Examples:**

```bash
# Seed all supported files recursively
fusiondb -db=./mydata seed ./data_exports

# Only process files in the root folder, no subdirectories
fusiondb -db=./mydata seed --no-recursion ./data_exports

# Recurse at most 2 levels deep
fusiondb -db=./mydata seed --max-depth 2 ./data_exports
```

**Supported file formats:**

**JSON** — UFL manifest files (same format as the `ufl` command).

**Markdown** — entities defined with YAML frontmatter:

```markdown
---
entity:
  id: "doc:whitepaper_01"
  type: "Document"
  tier: "knowledge"
action: fuse
---
The body text is stored in the `description` KV field.
```

**Excel (.xlsx)** — must contain a sheet named `Entities`:
- `_id` — entity ID (required)
- `_type` — entity type
- `_tier` — tier string (`verified`, `unverified`, or `knowledge`)
- `_vector` — comma-separated float32 values
- All other columns — mapped as KV metadata fields

**Notes:**
- Files inside `.git-app-internal` directories are automatically skipped.
- Errors during seeding are collected and printed at the end rather than aborting immediately. The command reports a count of successfully fused records.
- Records are committed in batches of 100 to prevent transaction size overflow.

---

## `serve`

Start an HTTP observability server with health and readiness endpoints.

**Usage:**

```bash
fusiondb -db=[path] serve [flags]
```

**Flags:**

| Flag | Default | Description |
|---|---|---|
| `--port` | `8080` | TCP port to listen on |

**Examples:**

```bash
# Start on the default port
fusiondb -db=./mydata serve

# Start on a custom port
fusiondb -db=./mydata serve --port 9090
```

**Endpoints:**

**`GET /healthz`**

Returns `200 ok` if the process is running. No database check is performed. Use this as a liveness probe.

```bash
curl http://localhost:8080/healthz
# → 200 ok
```

**`GET /readyz`**

Returns `200 ok` when both of the following are true:
1. The database `LOCK` file exists at the configured `-db` path
2. Disk usage on the database volume is below 90%

Returns `503 Service Unavailable` with a descriptive body on failure.

```bash
curl http://localhost:8080/readyz
# → 200 ok

# On failure:
# 503 not ready: database LOCK file not found
# 503 not ready: disk 91.2% full (threshold 90%)
```

Use `/readyz` as a startup/readiness probe. Use `/healthz` as a liveness probe.

---

## Exit Codes

| Code | Meaning |
|---|---|
| `0` | Success |
| `1` | Error — details logged to stderr as structured JSON |

All error output is written to stderr as structured JSON via `slog`. Example:

```json
{"time":"2026-06-20T12:00:00Z","level":"ERROR","msg":"atomic fusion failed","error":"..."}
```

---

## Environment Variables

| Variable | Required | Description |
|---|---|---|
| `FUSIONDB_SECRET` | Yes | Master encryption key. Must be either a 64-character hex string (32 decoded bytes) or at least 32 raw bytes. FusionDB will refuse to start if this is not set or insufficient. |

# FusionDB Quick Start

This guide gets you from zero to a running FusionDB database in under five minutes.

---

## Prerequisites

- Windows 10/11 (64-bit) or Linux (x86_64)
- A terminal / command prompt
- `openssl` or any tool that can generate random bytes (for key generation)

To build from source: Go 1.21 or later.

---

## Step 1: Install FusionDB

### Windows (pre-built binary)

Run the installer:

```cmd
fusiondb-installer.exe
```

The installer downloads the latest `fusiondb.exe` runtime and places it in the current directory. Verify:

```cmd
fusiondb.exe --help
```

### Linux (pre-built binary)

```bash
chmod +x fusiondb-bootstrap-linux
./fusiondb-bootstrap-linux
```

### Build from source

```bash
git clone https://github.com/Mirage-Recon-LLC/FusionDB-T1
cd FusionDB-T1
go build -o fusiondb ./cmd/fusiondb
```

---

## Step 2: Generate and Set Your Encryption Key

FusionDB requires a 32-byte master key before it will start. This key encrypts all data at rest.

**Generate a key:**

```bash
# Linux / macOS
openssl rand -hex 32
```

```powershell
# Windows PowerShell
[System.BitConverter]::ToString([System.Security.Cryptography.RandomNumberGenerator]::GetBytes(32)).Replace("-","").ToLower()
```

Example output: `a3f1c2d4e5b6a7f8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2`

**Set it as an environment variable:**

```bash
# Linux / macOS
export FUSIONDB_SECRET=a3f1c2d4e5b6a7f8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2
```

```cmd
:: Windows Command Prompt
set FUSIONDB_SECRET=a3f1c2d4e5b6a7f8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2
```

```powershell
# Windows PowerShell
$env:FUSIONDB_SECRET = "a3f1c2d4e5b6a7f8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2"
```

> **Important:** Save this key somewhere safe. Data cannot be decrypted without it. Never commit it to source control. In production, load it from a secrets manager.

---

## Step 3: Create Your First Entity

Create a file called `first.json`:

```json
{
  "ufl_version": "1.0",
  "action": "fuse",
  "entity": {
    "id": "person:alice",
    "type": "Person",
    "tier": "verified",
    "vector": [0.12, -0.05, 0.88, 0.34, 0.61],
    "kv": {
      "full_name": "Alice Johnson",
      "department": "Research",
      "active": true
    },
    "relations": {
      "secondary": [
        { "predicate": "has_email", "object": "alice@example.com" }
      ],
      "tertiary": [
        { "predicate": "owns_vehicle", "object": "vin:ABC123" }
      ]
    }
  }
}
```

Fuse it into the database:

```bash
fusiondb -db=./mydata ufl first.json
```

You should see a JSON log line confirming the write:
```json
{"time":"...","level":"INFO","msg":"UFL manifest fused"}
```

---

## Step 4: Query the Graph

Retrieve all semantic relationships for Alice:

```bash
fusiondb -db=./mydata query person:alice
```

Output:
```
Found 2 semantic graph relations:
  (person:alice) --[has_email]--> (alice@example.com)
  (person:alice) --[owns_vehicle]--> (vin:ABC123)
```

---

## Step 5: Use the Raw Store Command

For quick writes without a JSON file, use the `store` command directly:

```bash
fusiondb -db=./mydata store \
  -tier verified \
  -node 42 \
  -vector "0.14,-0.22,0.98,0.05,0.33" \
  -subject "device:router_01" \
  -predicate "located_at" \
  -object "datacenter:nyc-1"
```

---

## Step 6: Seed a Directory

Place multiple JSON, Markdown, or Excel files in a folder and ingest them all at once:

```bash
fusiondb -db=./mydata seed ./my_data_folder
```

**JSON file** — a UFL manifest (see Step 3).

**Markdown file** — YAML frontmatter defines the entity; the body becomes the `description` KV field:

```markdown
---
entity:
  id: "doc:report_2026_q1"
  type: "Document"
  tier: "knowledge"
action: fuse
---
This report covers Q1 findings across all research projects...
```

**Excel file** — must contain a sheet named `Entities` with reserved columns `_id`, `_type`, `_tier`, `_vector`. All other columns map to KV fields automatically.

Control recursion depth:

```bash
# Only process files in the immediate directory (no subdirectories)
fusiondb -db=./mydata seed --no-recursion ./my_data_folder

# Recurse at most 2 levels deep
fusiondb -db=./mydata seed --max-depth 2 ./my_data_folder
```

---

## Step 7: Start the Observability Server

```bash
fusiondb -db=./mydata serve --port 8080
```

Check status:

```bash
curl http://localhost:8080/healthz
# → ok

curl http://localhost:8080/readyz
# → ok  (or a 503 with details if the DB isn't ready)
```

---

## Step 8: Use FusionDB in Go Code

Add the module to your project:

```bash
go get github.com/Mirage-Recon-LLC/FusionDB-T1
```

Basic usage:

```go
package main

import (
    "context"
    "fmt"
    "log"
    "os"

    "github.com/Mirage-Recon-LLC/FusionDB-T1"
)

func main() {
    // Key must be set before Open()
    os.Setenv("FUSIONDB_SECRET", "your-64-char-hex-key-here...")

    db, err := fusiondb.Open("./mydata")
    if err != nil {
        log.Fatal(err)
    }
    defer db.Close()

    ctx := context.Background()

    // Write an entity
    err = db.Fuse(ctx, fusiondb.UFLManifest{
        Version: "1.0",
        Action:  "fuse",
        Entity: fusiondb.UFLEntity{
            ID:     "server:web-01",
            Type:   "Server",
            Tier:   fusiondb.TierVerified,
            Vector: []float32{0.1, 0.9, 0.4},
            KV: map[string]any{
                "hostname": "web-01.prod",
                "region":   "us-east-1",
            },
        },
    })
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println("Entity fused successfully")

    // Read it back
    result, err := db.UFLQuery(ctx, fusiondb.UFLQuery{
        Action:   "query",
        Selector: fusiondb.UFLSelector{ID: "server:web-01"},
        Options:  fusiondb.UFLOptions{Hydrate: fusiondb.UFLHydrationOptions{MaxDegree: 4}},
    })
    if err != nil {
        log.Fatal(err)
    }
    fmt.Printf("Found entity: %s (type: %s, tier: %s)\n", result.ID, result.Type, result.Tier)
}
```

---

## Next Steps

- [UFL Reference](UFL_REFERENCE.md) — full schema for all UFL actions
- [CLI Reference](CLI_REFERENCE.md) — complete flag documentation for all commands
- [Go API Reference](API_REFERENCE.md) — all exported types and methods
- [Security Guide](SECURITY.md) — key management, encryption model, PII masking
- [Architecture](ARCHITECTURE.md) — internal design and storage layout

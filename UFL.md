# Unified Fusion Language (UFL) Reference

UFL is the primary programmatic interface for interacting with FusionDB. It allows you to manage multi-modal entities (KV, Graph, and Vector) through a single JSON-based schema.

## 1. Concepts

### 1.1 Entities
An entity is the fundamental unit in UFL. It represents a unified record that exists simultaneously across all storage layers.

### 1.2 The 4-Degree Ontology
UFL uses a "gravity" model to organize relationships:
- **Degree 1 (Primary)**: The core node. Anchors the KV document and Vector embedding.
- **Degree 2 (Secondary)**: Essential identifiers (Emails, Phones, IDs).
- **Degree 3 (Tertiary)**: Relational assets (Vehicles, Properties).
- **Degree 4 (Quaternary)**: Loose references (External documents, mentions).

## 2. Storage: The Fusion Manifest

To create or update an entity, submit a **Fusion Manifest**.

### Schema
```json
{
  "ufl_version": "1.0",
  "action": "fuse",
  "entity": {
    "id": "string",
    "type": "string",
    "tier": "verified | unverified | knowledge",
    "vector": [float32, ...],
    "kv": {
      "key": "any_value"
    },
    "relations": {
      "secondary": [{ "predicate": "string", "object": "string" }],
      "tertiary": [...],
      "quaternary": [...]
    }
  }
}
```

### Example: Person Entity
```json
{
  "ufl_version": "1.0",
  "action": "fuse",
  "entity": {
    "id": "person:john_doe",
    "type": "Person",
    "tier": "verified",
    "vector": [0.12, -0.05, 0.88],
    "kv": {
      "full_name": "John Michael Doe",
      "active": true
    },
    "relations": {
      "secondary": [
        { "predicate": "has_email", "object": "john@example.com" }
      ],
      "tertiary": [
        { "predicate": "owns_vehicle", "object": "vin:ABC123" }
      ]
    }
  }
}
```

## 3. Retrieval: UFL Query

Queries search for entities and return "hydrated" (collapsed) JSON objects.

### Schema
```json
{
  "action": "query",
  "selector": {
    "id": "string",
    "vector": { "$near": [...], "$limit": int },
    "kv": { "field": "value" }
  },
  "options": {
    "hydrate": {
      "max_degree": int,
      "include_vector": bool
    }
  }
}
```

### Example: Hydrated Retrieval
```json
{
  "action": "query",
  "selector": { "id": "person:john_doe" },
  "options": {
    "hydrate": { "max_degree": 4 }
  }
}
```
*Result:* Returns the entity JSON including all relationships from Degrees 1 through 4.

## 4. Multi-Format Seeding

### 4.1 Markdown (`.md`)
Markdown files can be used as entities.
- **Header**: YAML frontmatter defines metadata (`id`, `type`, `tier`).
- **Body**: The document text is stored in the `description` KV field.

```markdown
---
entity:
  id: "doc:whitepaper_01"
  type: "Document"
  tier: "knowledge"
---
This whitepaper discusses the future of multi-modal databases...
```

### 4.2 Excel (`.xlsx`)
Excel files must contain a sheet named `Entities`.
- **Reserved Columns**: `_id`, `_type`, `_tier`, `_vector`.
- **Other Columns**: Mapped automatically to the `kv` metadata block.

## 5. CLI Commands

### Fusing a single file
```bash
.\fusiondb.exe ufl manifest.json
```

### Bulk seeding a directory
```bash
.\fusiondb.exe seed ./my_data_folder
```

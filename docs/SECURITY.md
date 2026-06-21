# FusionDB Security Guide

This document describes the FusionDB security model: how data is encrypted, how PII is protected, how the master key is managed, and what threats the system does and does not protect against.

---

## Table of Contents

1. [The Master Key](#the-master-key)
2. [Key Derivation](#key-derivation)
3. [Encryption at Rest](#encryption-at-rest)
4. [PII Masking in the Graph Index](#pii-masking-in-the-graph-index)
5. [Tamper Detection](#tamper-detection)
6. [Tier Isolation](#tier-isolation)
7. [Threat Model](#threat-model)
8. [Key Management Best Practices](#key-management-best-practices)
9. [What FusionDB Does Not Protect Against](#what-fusiondb-does-not-protect-against)

---

## The Master Key

FusionDB requires a master key to start. This key is provided via the environment variable `FUSIONDB_SECRET`.

**Valid formats:**

| Format | Requirement |
|---|---|
| Hex string | Must decode to ≥ 32 bytes (i.e., ≥ 64 hex characters) |
| Raw string | Must be ≥ 32 bytes in length |

If `FUSIONDB_SECRET` is not set, empty, or has insufficient entropy, FusionDB logs an error and exits immediately:

```json
{"level":"ERROR","msg":"kernel secret validation failed; refusing to start",
 "error":"FUSIONDB_SECRET entropy insufficient: hex-decoded to 16 bytes, need at least 32",
 "hint":"set FUSIONDB_SECRET to a hex-encoded 32-byte (64-char) cryptographic key"}
```

**Generate a secure key:**

```bash
# Linux / macOS
openssl rand -hex 32
```

```powershell
# Windows PowerShell
[System.BitConverter]::ToString([System.Security.Cryptography.RandomNumberGenerator]::GetBytes(32)).Replace("-","").ToLower()
```

---

## Key Derivation

The raw master key from `FUSIONDB_SECRET` is never used directly for encryption or hashing. Two purpose-specific keys are derived from it via HMAC-SHA256:

```
secretKey = HMAC-SHA256(masterKey, "fusiondb-secret-v1")  ← used for AES-256-GCM
saltKey   = HMAC-SHA256(masterKey, "fusiondb-salt-v1")    ← used for HMAC PII masking
```

This separation ensures that:
- Compromise of the saltKey (PII masking) does not compromise the encryption key
- Compromise of the secretKey (encryption) does not compromise the PII salt
- The raw master key is held in memory only during the derivation step and is not accessible through any public API

Both derived keys are 32 bytes (256 bits). They are stored in the `SAGEKernel.cfg` struct for the lifetime of the database handle.

---

## Encryption at Rest

All KV store payloads are encrypted with **AES-256-GCM** before being written to BadgerDB.

**Encryption process (per write):**

1. The `KnowledgeRecord` is JSON-marshaled to plaintext bytes
2. A fresh 12-byte random nonce (IV) is generated using `crypto/rand`
3. AES-256-GCM seals the plaintext: `ciphertext = GCM.Seal(nonce, plaintext)`
4. The envelope is stored on disk:

```json
{
  "type": "<entity type>",
  "nonce": "<base64-encoded 12-byte IV>",
  "payload": "<base64-encoded ciphertext>",
  "salience": 1.0,
  "reliability": 1.0,
  "decay_factor": 0.1,
  "created_at": "2026-01-01T00:00:00Z"
}
```

**Decryption process (per read):**

1. The envelope is read from BadgerDB
2. The nonce and ciphertext are extracted
3. AES-256-GCM opens the ciphertext: `plaintext = GCM.Open(nonce, ciphertext)`
4. If decryption fails (wrong key, corrupted data, or truncated payload), the record is skipped with an error — it is never returned with corrupted content

**Key properties:**
- **Fresh IV per write:** No two ciphertexts share a nonce, even for identical plaintexts. This prevents ciphertext comparison attacks.
- **GCM authentication tag:** AES-GCM provides authenticated encryption. Any modification to the ciphertext or nonce is detected on decryption.
- **Zero plaintext on disk:** The KV plaintext is never written to BadgerDB in unencrypted form when the 32-byte key is present.

---

## PII Masking in the Graph Index

Degree 2 relationships — those involving PII identifiers such as email addresses and phone numbers — are handled differently from other graph edges.

**Graph write (Degree 2):**

1. The original PII value (e.g., `alice@example.com`) is passed through `HMAC-SHA256(saltKey, pii_value)` to produce a deterministic hash
2. The graph edge stores `subject → predicate → HMAC_hash` instead of the raw PII
3. The original PII value is separately encrypted with AES-256-GCM and stored in a reverse-lookup record under prefix `0x03 0x01`

```
Forward edge (0x00): person:alice → has_email → a3f1c2...  (HMAC hash)
Reverse edge (0x01): a3f1c2...   → has_email → person:alice
Reverse lookup (0x03 0x01): a3f1c2... → AES-GCM(alice@example.com)
```

**Graph read (Degree 2):**

When `HydrateEntity()` or `UFLQuery()` resolves a Degree 2 relation:
1. The HMAC hash is extracted from the graph edge
2. The reverse-lookup record is retrieved and decrypted
3. The original PII value is returned in the response

**Implication:** An attacker who obtains the raw BadgerDB data files cannot recover PII email addresses or phone numbers without the `FUSIONDB_SECRET` master key. The HMAC hash in the graph index is one-way and cannot be reversed without the salt key.

**Caveat:** HMAC-SHA256 is deterministic. Two records with the same PII value and the same salt key will produce the same hash, which means it is possible to confirm whether two records share the same PII value without decrypting. To confirm a specific value (e.g., "does person:alice have email alice@example.com?"), an attacker would need to know the value to hash and compare — they cannot enumerate from the hash alone.

---

## Tamper Detection

AES-256-GCM includes a 128-bit authentication tag on every ciphertext. If any byte of the ciphertext or the nonce is modified after encryption:

- Decryption will return an error
- The record will be skipped, not returned with corrupted data
- In `HybridQueryEngine`, records that fail decryption are silently excluded from results

There is no separate integrity log. If you need to detect whether records have been added, removed, or modified at the database level, that should be implemented at the application layer.

---

## Tier Isolation

Each data tier (`verified`, `unverified`, `knowledge`) occupies a separate byte prefix in the BadgerDB keyspace. A scan of one tier cannot return records from another tier, because the prefix filter is applied before any record is read.

This is a logical isolation, not a cryptographic boundary. All three tiers use the same derived encryption key. If an attacker can decrypt one tier, they can decrypt all tiers.

---

## Threat Model

### Protected against

| Threat | Mechanism |
|---|---|
| Disk theft / cold storage capture | AES-256-GCM encrypts all KV payloads; PII is HMAC-masked in graph edges |
| PII enumeration from graph index | HMAC-SHA256 masking prevents reverse-lookup without the salt key |
| Plaintext storage of sensitive values | No KV payload is written to disk in plaintext when the master key is set |
| Ciphertext replay / substitution | GCM authentication tag detects modified ciphertexts |
| Nonce reuse attacks | Fresh `crypto/rand` nonce per write |
| Key exposure via log output | Derived keys are never logged; `FUSIONDB_SECRET` is only read from the environment |
| Agent looping / cascading failures | Circuit breaker halts after 3 sequential failures |
| Session drift in multi-agent contexts | Session sync guard rejects cross-session operations |

### Not protected against

See the section below.

---

## Key Management Best Practices

1. **Do not hardcode `FUSIONDB_SECRET` in source code, config files, or Docker images.** Load it at runtime from a secrets manager (AWS Secrets Manager, HashiCorp Vault, Azure Key Vault, etc.).

2. **Rotate the key by re-encrypting.** FusionDB does not have a built-in key rotation path. To rotate: export all data, generate a new key, start a fresh database, re-import.

3. **Back up the key separately from the data.** Storing both on the same system defeats the purpose of encryption.

4. **Use unique keys per environment.** Development, staging, and production databases should each have a distinct `FUSIONDB_SECRET`.

5. **Restrict access to the database directory.** BadgerDB's data files should be readable only by the FusionDB process user. On Linux: `chmod 700 ./mydata`.

6. **The key is all-or-nothing.** If the key is lost, the data cannot be recovered. There is no key escrow built into FusionDB.

---

## What FusionDB Does Not Protect Against

FusionDB is an **embedded** database library. It does not have a network layer, authentication layer, or multi-user access control. The following threats are outside its scope:

| Threat | Notes |
|---|---|
| **Process memory access** | A process with access to the FusionDB process memory can extract decrypted data and derived keys. |
| **Unauthorized process access** | Any process running as the same OS user can open the database directory directly. OS-level access controls must be enforced externally. |
| **Encryption in transit** | FusionDB does not have a network server mode. If you expose data over a network, TLS must be implemented at the application layer. |
| **Multi-user access control** | There is no user authentication or role-based access control. All callers with access to the database handle have full read/write access. |
| **Application-layer data exfiltration** | FusionDB cannot prevent an application with valid credentials from exporting or transmitting data. |
| **Side-channel attacks** | No countermeasures for timing, cache, or power analysis attacks. |
| **Key derivation collisions** | HMAC-SHA256 is treated as collision-resistant; theoretical weaknesses in SHA-256 are outside scope. |

package fusiondb

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/dgraph-io/badger/v4"
	"github.com/dgraph-io/ristretto/v2"
)

var (
	// ErrNotFound is returned when a requested key or record does not exist.
	ErrNotFound = errors.New("fusiondb: record not found")
)

// DB is the unified multi-model database engine (aliased to SAGEKernel for compatibility).
type DB = SAGEKernel

// SAGEKernel serves as the central brain stem managing the in-process state mesh.
type SAGEKernel struct {
	mu              sync.RWMutex
	core            *badger.DB
	cfg             Config
	ActiveSessionID string
	IsCircuitBroken bool
	FailedToolCount int
	BusChannel      chan interface{} // Non-blocking telemetry channel
	Graph           *GraphStore
	Vector          *VectorStore
	KV              *KVStore
	SystemSecretKey []byte
	SystemSaltKey   []byte
	cache           *ristretto.Cache[string, []byte]
}

type DataLayer string

const (
	LayerVerified   DataLayer = "VERIFIED"   // Prefix 0x10: Accountable, audited facts
	LayerUnverified DataLayer = "UNVERIFIED" // Prefix 0x11: Raw, unchecked discoveries
	LayerKnowledge  DataLayer = "KNOWLEDGE"  // Prefix 0x12: Methods, mistakes, and selfhood
)

type Config struct {
	MasterKey     []byte // Strict 32-byte key for AES-256-GCM disk encryption
	HMACSalt      []byte // Cryptographic salt for subject PII masking
	EmbeddingDims int    // Dimensions matching local GGUF provider (e.g., 384)
}

// FusionEntity unifies the PostgreSQL schema layout into a native Go object.
type FusionEntity struct {
	ID          string            `json:"id"`
	Type        string            `json:"type"` // "observation", "reflection", "plan", "correction"
	Layer       DataLayer         `json:"layer"`
	Content     string            `json:"content"`
	Salience    float64           `json:"salience"`
	Reliability float64           `json:"reliability"`
	DecayFactor float64           `json:"decay_factor"`
	Subjects    []string          `json:"subjects"`
	Relations   []UFLRelation     `json:"relations"`
	ContextChat string            `json:"context_chat"`
	Metadata    map[string]any    `json:"metadata"`
	CreatedAt   time.Time         `json:"created_at"`
}

type Options struct {
	Path             string
	SyncWrites       bool
	ValueThreshold   int64
	BlockCacheSize   int64
	IndexCacheSize   int64
	RistrettoMaxCost int64
	EmbeddingDims    int
}

// DefaultOptions returns a safe, transactional production configuration.
func DefaultOptions(path string) Options {
	return Options{
		Path:             path,
		SyncWrites:       true, // Default to strict safety
		ValueThreshold:   1024, // Keep large vectors out of the LSM tree
		BlockCacheSize:   64 << 20,
		IndexCacheSize:   32 << 20,
		RistrettoMaxCost: 128 << 20,
		EmbeddingDims:    0, // Default to 0 (disabled) to maintain test compatibility
	}
}

// Open initializes the unified database at the given path using default options.
func Open(path string) (*DB, error) {
	return OpenAdvanced(DefaultOptions(path))
}

// withDefaults returns a copy of Options with zero-value configuration fields populated with their defaults.
func (opts Options) withDefaults() Options {
	if opts.ValueThreshold == 0 {
		opts.ValueThreshold = 1024
	}
	if opts.BlockCacheSize == 0 {
		opts.BlockCacheSize = 64 << 20
	}
	if opts.IndexCacheSize == 0 {
		opts.IndexCacheSize = 32 << 20
	}
	if opts.RistrettoMaxCost == 0 {
		opts.RistrettoMaxCost = 128 << 20
	}
	return opts
}

// validateFusionDBSecret decodes FUSIONDB_SECRET and validates ≥32 bytes of raw entropy.
// If the value is valid hex, the decoded byte count must be ≥32.
// Otherwise the raw byte count must be ≥32.
func validateFusionDBSecret(s string) ([]byte, error) {
	if s == "" {
		return nil, errors.New("FUSIONDB_SECRET is not set")
	}
	if decoded, err := hex.DecodeString(s); err == nil {
		if len(decoded) < 32 {
			return nil, fmt.Errorf("FUSIONDB_SECRET entropy insufficient: hex-decoded to %d bytes, need at least 32", len(decoded))
		}
		return decoded, nil
	}
	raw := []byte(s)
	if len(raw) < 32 {
		return nil, fmt.Errorf("FUSIONDB_SECRET entropy insufficient: got %d raw bytes, need at least 32", len(raw))
	}
	return raw, nil
}

// deriveKeys derives a 32-byte secretKey and 32-byte saltKey from the master key via HMAC-SHA256.
func deriveKeys(master []byte) (secretKey, saltKey []byte) {
	h1 := hmac.New(sha256.New, master)
	h1.Write([]byte("fusiondb-secret-v1"))
	secretKey = h1.Sum(nil)

	h2 := hmac.New(sha256.New, master)
	h2.Write([]byte("fusiondb-salt-v1"))
	saltKey = h2.Sum(nil)
	return
}

// OpenAdvanced initializes the database using custom configurations.
func OpenAdvanced(opts Options) (*SAGEKernel, error) {
	opts = opts.withDefaults()

	badgerOpts := badger.DefaultOptions(opts.Path).
		WithSyncWrites(opts.SyncWrites).
		WithValueThreshold(opts.ValueThreshold).
		WithBlockCacheSize(opts.BlockCacheSize).
		WithIndexCacheSize(opts.IndexCacheSize).
		WithNumMemtables(3).
		WithLoggingLevel(badger.ERROR)

	coreDB, err := badger.Open(badgerOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	master, err := validateFusionDBSecret(os.Getenv("FUSIONDB_SECRET"))
	if err != nil {
		coreDB.Close()
		slog.Error("kernel secret validation failed; refusing to start",
			"error", err.Error(),
			"hint", "set FUSIONDB_SECRET to a hex-encoded 32-byte (64-char) cryptographic key")
		os.Exit(1)
	}
	secretKey, saltKey := deriveKeys(master)

	ristrettoCache, err := ristretto.NewCache(&ristretto.Config[string, []byte]{
		NumCounters: 1e6,
		MaxCost:     opts.RistrettoMaxCost,
		BufferItems: 64,
		Cost: func(value []byte) int64 {
			return int64(len(value))
		},
	})
	if err != nil {
		coreDB.Close()
		return nil, fmt.Errorf("failed to initialize Ristretto cache: %w", err)
	}

	return &SAGEKernel{
		core:            coreDB,
		cfg:             Config{MasterKey: secretKey, HMACSalt: saltKey, EmbeddingDims: opts.EmbeddingDims},
		Graph:           &GraphStore{db: coreDB},
		Vector:          &VectorStore{db: coreDB},
		KV:              &KVStore{db: coreDB, cache: ristrettoCache, cfg: Config{MasterKey: secretKey, HMACSalt: saltKey}},
		SystemSecretKey: secretKey,
		SystemSaltKey:   saltKey,
		cache:           ristrettoCache,
		BusChannel:      make(chan interface{}, 1024),
	}, nil
}

// PublishMeshTelemetry implements the non-blocking select/default safety pattern.
func (k *SAGEKernel) PublishMeshTelemetry(event interface{}) {
	select {
	case k.BusChannel <- event:
		// Queue cleared cleanly
	default:
		// Safeguard: Drop frame on saturation to protect main reasoning thread clock cycles
	}
}

// CircuitBreaker evaluates sequential tool execution behaviors to kill Workslop loops.
func (k *SAGEKernel) CircuitBreaker(ctx context.Context, toolError error) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	if toolError != nil {
		k.FailedToolCount++
		if k.FailedToolCount >= 3 {
			k.IsCircuitBroken = true
			return fmt.Errorf("CIRCUIT_BREAKER_TRIPPED: Looping or chaotic failures detected. Halting for review")
		}
	} else {
		k.FailedToolCount = 0
	}
	return nil
}

// VerifySessionSync blocks prompt-bleed and mid-session directory drift.
func (k *SAGEKernel) VerifySessionSync(currentSessionID string) error {
	k.mu.RLock()
	defer k.mu.RUnlock()
	if k.ActiveSessionID != "" && k.ActiveSessionID != currentSessionID {
		return fmt.Errorf("DESYNC_PROTOCOL_TRIGGERED: Disconnected from active session context. Halting thread")
	}
	return nil
}

// Close shuts down the BadgerDB instance cleanly, ensuring all data is flushed to disk.
func (db *DB) Close() error {
	if db.cache != nil {
		db.cache.Close()
	}
	return db.core.Close()
}

// Local AES-256-GCM Encrypter
func (k *SAGEKernel) encryptContent(plaintext []byte) ([]byte, []byte, error) {
	block, err := aes.NewCipher(k.cfg.MasterKey)
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, err
	}
	return nonce, gcm.Seal(nil, nonce, plaintext, nil), nil
}

// Local AES-256-GCM Decrypter
func (k *SAGEKernel) decryptContent(nonce, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(k.cfg.MasterKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nonce, ciphertext, nil)
}

// SHA256 HMAC for privacy-preserving index lookups
func (k *SAGEKernel) MaskPII(pii string) string {
	mac := hmac.New(sha256.New, k.cfg.HMACSalt)
	mac.Write([]byte(pii))
	return fmt.Sprintf("%x", mac.Sum(nil))
}

func (k *SAGEKernel) buildGraphKey(prefix byte, s, p, o string) []byte {
	return append([]byte{prefix}, []byte(s+":"+p+":"+o)...)
}

// safeUpdate runs a badger transaction with panic recovery and context check.
func safeUpdate(ctx context.Context, db *badger.DB, fn func(txn *badger.Txn) error) (err error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}

	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("transaction aborted due to internal panic: %v", r)
		}
	}()

	return db.Update(func(txn *badger.Txn) error {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		return fn(txn)
	})
}

// SafeUpdate safely executes unified transactions with panic recovery and context checks.
func (k *SAGEKernel) SafeUpdate(ctx context.Context, fn func(txn *badger.Txn) error) (err error) {
	return safeUpdate(ctx, k.core, fn)
}

// SecureWriteTransaction executes the unified, atomic multi-tier write path.
func (k *SAGEKernel) SecureWriteTransaction(ctx context.Context, entity *FusionEntity, embedding []float32) error {
	if k.IsCircuitBroken {
		return fmt.Errorf("database transaction rejected: central circuit breaker is open")
	}
	// 1. Validate dimensions if requested
	if k.cfg.EmbeddingDims > 0 && len(embedding) != k.cfg.EmbeddingDims {
		return fmt.Errorf("mismatched embedding dimensions: expected %d, got %d", k.cfg.EmbeddingDims, len(embedding))
	}

	// 2. Open serializable transaction block
	return k.core.Update(func(txn *badger.Txn) error {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		// Step A: Ingest Key-Value Core Payload (Using updated KV store with encryption)
		kvKey := entity.ID // Use ID as the KV key directly
		metadata := make(map[string]any)
		for k, v := range entity.Metadata {
			metadata[k] = v
		}
		record := KnowledgeRecord{
			Agent: "sage-kernel",
			Type:  entity.Type,
			Data:  entity.Content,
			Meta:  metadata,
		}
		if err := k.KV.StoreInTxn(txn, string(entity.Layer), kvKey, record); err != nil {
			return err
		}

		// Step B: Build Relational Graph (Using Graph store's format)
		if len(entity.Relations) > 0 {
			for _, rel := range entity.Relations {
				if err := k.Graph.AddQuadStringsInTxn(txn, entity.ID, rel.Predicate, rel.Object, string(entity.Layer)); err != nil {
					return err
				}
			}
		} else {
			for _, subject := range entity.Subjects {
				if err := k.Graph.AddQuadStringsInTxn(txn, entity.ID, "about", subject, string(entity.Layer)); err != nil {
					return err
				}
			}
		}

		if entity.ContextChat != "" {
			if err := k.Graph.AddQuadStringsInTxn(txn, entity.ID, "context", entity.ContextChat, string(entity.Layer)); err != nil {
				return err
			}
		}

		// Step C: Write Vector float32 array
		vecKey := append([]byte{0x03}, []byte("vector:"+entity.ID)...)
		vecBytes := make([]byte, len(embedding)*4)
		for i, f := range embedding {
			binary.BigEndian.PutUint32(vecBytes[i*4:(i+1)*4], math.Float32bits(f))
		}
		if err := txn.Set(vecKey, vecBytes); err != nil {
			return err
		}

		// Step D: Integrate with HNSW Graph
		nodeID := xxhash.Sum64String(entity.ID)
		if err := SaveIDMapping(txn, nodeID, entity.ID); err != nil {
			return err
		}
		return InsertHNSWNode(ctx, txn, nodeID, embedding)
	})
}

// AtomicFusion writes a vector embedding, its corresponding graph quad, and its KV metadata
// atomically inside a single BadgerDB transaction, while respecting context cancellation or timeouts.
func (db *DB) AtomicFusion(
	ctx context.Context,
	tier string,
	nodeID uint64,
	vector []float32,
	subject, predicate, object string,
) error {
	// 1. Validate Inputs
	if len(vector) == 0 {
		return errors.New("vector cannot be nil or empty")
	}
	if subject == "" || predicate == "" || object == "" {
		return errors.New("subject, predicate, and object cannot be empty")
	}

	if tier != "verified" && tier != "unverified" && tier != "knowledge" {
		tier = "verified"
	}

	var layer DataLayer
	switch tier {
	case "verified":
		layer = LayerVerified
	case "knowledge":
		layer = LayerKnowledge
	default:
		layer = LayerUnverified
	}

	entity := &FusionEntity{
		ID:          subject,
		Type:        predicate,
		Layer:       layer,
		Content:     object,
		Salience:    1.0,
		Reliability: 1.0,
		DecayFactor: 0.1,
		Subjects:    []string{subject},
		Metadata:    make(map[string]any),
		CreatedAt:   time.Now(),
	}

	// 1. Validate dimensions if requested
	if db.cfg.EmbeddingDims > 0 && len(vector) != db.cfg.EmbeddingDims {
		return fmt.Errorf("mismatched embedding dimensions: expected %d, got %d", db.cfg.EmbeddingDims, len(vector))
	}

	// 2. Open serializable transaction block
	return db.core.Update(func(txn *badger.Txn) error {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
		}

		// Step A: Ingest Key-Value Core Payload
		kvKey := tier + ":" + subject + ":" + strconv.FormatUint(nodeID, 10)
		record := KnowledgeRecord{
			Agent:  subject,
			Type:   predicate,
			Result: object,
		}
		if err := db.KV.StoreInTxn(txn, string(entity.Layer), kvKey, record); err != nil {
			return err
		}

		// Step B: Build Relational Graph (Original Triple)
		if err := db.Graph.AddQuadStringsInTxn(txn, subject, predicate, object, string(entity.Layer)); err != nil {
			return err
		}

		// Step C: Write Vector float32 array
		vecKey := append([]byte{0x03}, []byte("vector:"+entity.ID)...)
		vecBytes := make([]byte, len(vector)*4)
		for i, f := range vector {
			binary.BigEndian.PutUint32(vecBytes[i*4:(i+1)*4], math.Float32bits(f))
		}
		if err := txn.Set(vecKey, vecBytes); err != nil {
			return err
		}

		// Step D: Integrate with HNSW Graph (Using provided nodeID for compatibility)
		if err := SaveIDMapping(txn, nodeID, entity.ID); err != nil {
			return err
		}
		return InsertHNSWNode(ctx, txn, nodeID, vector)
	})
}
type HybridSearchQuery struct {
	QueryText      string
	QueryEmbedding []float32
	Subjects       []string
	ContextChat    string
	PoolLimit      int
	TargetLimit    int
}

func (k *SAGEKernel) HybridQueryEngine(ctx context.Context, q HybridSearchQuery) ([]FusionEntity, error) {
	var operationalPool []FusionEntity

	err := k.core.View(func(txn *badger.Txn) error {
		// In a live execution, this block pulls candidate node strings from the 0x03 index
		// For now, we use LinearScanFallback as a placeholder for the HNSW candidate selection
		candidates, err := LinearScanFallback(ctx, txn, q.QueryEmbedding, q.PoolLimit)
		if err != nil {
			return err
		}

		for _, cand := range candidates {
			id, err := GetIDMapping(txn, cand.NodeID)
			if err != nil || id == "" {
				continue
			}

			// 1. Enforce strict relational Graph constraint filters inside the transaction
			if len(q.Subjects) > 0 {
				matchedSubject := false
				for _, sub := range q.Subjects {
					// Use original graph key format (length-prefixed)
					graphKey := BuildGraphKey(0x00, id, "about", sub)
					if _, err := txn.Get(graphKey); err == nil {
						matchedSubject = true
						break
					}
				}
				if !matchedSubject {
					continue // Relational constraint mismatch
				}
			}

			if q.ContextChat != "" {
				graphKey := BuildGraphKey(0x00, id, "context", q.ContextChat)
				if _, err := txn.Get(graphKey); err != nil {
					continue // Context mismatch
				}
			}

			// 2. Locate and extract content payload across active tiers (0x10, 0x11, 0x12)
			var targetKey []byte
			var item *badger.Item

			tiers := []byte{0x10, 0x11, 0x12}
			for _, t := range tiers {
				testKey := append([]byte{t}, []byte(id)...)
				if item, err = txn.Get(testKey); err == nil {
					targetKey = testKey
					break
				}
			}
			if targetKey == nil {
				continue
			}

			var valBytes []byte
			_ = item.Value(func(v []byte) error {
				valBytes = append([]byte{}, v...)
				return nil
			})

			var metaRaw map[string]json.RawMessage
			if err := json.Unmarshal(valBytes, &metaRaw); err != nil {
				continue
			}

			var nonce, payload []byte
			var salience, reliability, decayFactor float64
			var createdAtStr string
			var metadata map[string]any

			_ = json.Unmarshal(metaRaw["nonce"], &nonce)
			_ = json.Unmarshal(metaRaw["payload"], &payload)
			_ = json.Unmarshal(metaRaw["salience"], &salience)
			_ = json.Unmarshal(metaRaw["reliability"], &reliability)
			_ = json.Unmarshal(metaRaw["decay_factor"], &decayFactor)
			_ = json.Unmarshal(metaRaw["created_at"], &createdAtStr)
			_ = json.Unmarshal(metaRaw["metadata"], &metadata)

			createdAt, _ := time.Parse(time.RFC3339, createdAtStr)
			plainText, err := k.decryptContent(nonce, payload)
			if err != nil {
				continue // Decryption fault: skip node to preserve integrity
			}

			// Extract original content from KnowledgeRecord JSON
			var record KnowledgeRecord
			contentStr := string(plainText)
			if err := json.Unmarshal(plainText, &record); err == nil {
				contentStr = record.Data
			}

			// 3. PostgreSQL Bayesian Hybrid Scoring Translation
			ageInDays := time.Since(createdAt).Hours() / 24.0
			recencyBoost := math.Exp(-ageInDays / 7.0) // 7-day half-life decay parameter (tau)
			decayPenalty := decayFactor * ageInDays

			// Weight Formula: w(t) = (salience * recency_boost * reliability) - decay_penalty
			computedWeight := (salience * recencyBoost * reliability) - decayPenalty

			operationalPool = append(operationalPool, FusionEntity{
				ID:          id,
				Content:     contentStr,
				Salience:    computedWeight,
				Reliability: reliability,
				DecayFactor: decayFactor,
				Metadata:    metadata,
				CreatedAt:   createdAt,
			})
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	// 4. Rank and sort matching entities based on real-time calculated weight profile
	sort.Slice(operationalPool, func(i, j int) bool {
		return operationalPool[i].Salience > operationalPool[j].Salience
	})

	if len(operationalPool) > q.TargetLimit {
		return operationalPool[:q.TargetLimit], nil
	}
	return operationalPool, nil
}

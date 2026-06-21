package fusiondb

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/dgraph-io/badger/v4"
)

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

const (
	TierVerified   = "verified"
	TierUnverified = "unverified"
	TierKnowledge  = "knowledge"
)

var UFLTiers = []string{TierVerified, TierUnverified, TierKnowledge}

func (db *DB) Fuse(ctx context.Context, manifest UFLManifest) error {
	ent := manifest.Entity

	var layer DataLayer
	switch ent.Tier {
	case TierVerified:
		layer = LayerVerified
	case TierKnowledge:
		layer = LayerKnowledge
	default:
		layer = LayerUnverified
	}

	metadata := ent.KV

	var subjects []string
	var relations []UFLRelation
	for _, rels := range ent.Relations {
		for _, r := range rels {
			subjects = append(subjects, r.Object)
			relations = append(relations, r)
		}
	}

	fusionEnt := &FusionEntity{
		ID:          ent.ID,
		Type:        ent.Type,
		Layer:       layer,
		Content:     ent.ID, // For UFL, content is often the anchor ID or descriptive text
		Salience:    1.0,    // Default salience
		Reliability: 1.0,    // Default reliability
		DecayFactor: 0.1,    // Default decay factor
		Subjects:    subjects,
		Relations:   relations,
		Metadata:    metadata,
		CreatedAt:   time.Now(),
	}

	return db.SecureWriteTransaction(ctx, fusionEnt, ent.Vector)
}

func (db *DB) UFLQuery(ctx context.Context, query UFLQuery) (*UFLEntity, error) {
	if query.Selector.Vector != nil && len(query.Selector.Vector.Near) > 0 {
		hQuery := HybridSearchQuery{
			QueryEmbedding: query.Selector.Vector.Near,
			PoolLimit:      query.Selector.Vector.Limit,
			TargetLimit:    1,
		}
		if hQuery.PoolLimit <= 0 {
			hQuery.PoolLimit = 10
		}
		if hQuery.TargetLimit <= 0 {
			hQuery.TargetLimit = 1
		}

		results, err := db.HybridQueryEngine(ctx, hQuery)
		if err != nil || len(results) == 0 {
			return nil, ErrNotFound
		}

		res := results[0]
		entity := &UFLEntity{
			ID:        res.ID,
			Type:      res.Type,
			Tier:      string(res.Layer),
			KV:        make(map[string]any),
			Relations: make(map[string][]UFLRelation),
		}
		for k, v := range res.Metadata {
			entity.KV[k] = v
		}
		if query.Options.Hydrate.IncludeVector {
			nodeID := xxhash.Sum64String(res.ID)
			vec, _, err := db.Vector.GetHNSWNode(ctx, nodeID)
			if err == nil {
				entity.Vector = vec
			}
		}
		return entity, nil
	}

	id := query.Selector.ID
	if id == "" {
		return nil, fmt.Errorf("selector ID or Vector required")
	}

	// Fallback to existing hydration logic if ID is provided
	var foundRec KnowledgeRecord
	var foundTier string
	err := db.core.View(func(txn *badger.Txn) error {
		tiers := []byte{0x10, 0x11, 0x12}
		tierNames := []string{TierVerified, TierUnverified, TierKnowledge}
		for i, t := range tiers {
			testKey := append([]byte{t}, []byte(id)...)
			item, err := txn.Get(testKey)
			if err == nil {
				var valBytes []byte
				item.Value(func(v []byte) error {
					valBytes = append([]byte{}, v...)
					return nil
				})
				
				// Try to decrypt
				var metaRaw map[string]json.RawMessage
				if err := json.Unmarshal(valBytes, &metaRaw); err == nil {
					if nonceRaw, ok := metaRaw["nonce"]; ok {
						var nonce, payload []byte
						json.Unmarshal(nonceRaw, &nonce)
						json.Unmarshal(metaRaw["payload"], &payload)
						plainText, err := db.decryptContent(nonce, payload)
						if err == nil {
							json.Unmarshal(plainText, &foundRec)
							foundTier = tierNames[i]
							return nil
						}
					}
				}
				
				// Fallback to unencrypted
				if err := json.Unmarshal(valBytes, &foundRec); err == nil {
					foundTier = tierNames[i]
					return nil
				}
			}
		}
		return ErrNotFound
	})
	if err != nil {
		return nil, err
	}

	entity := &UFLEntity{
		ID:        id,
		Type:      foundRec.Type,
		Tier:      foundTier,
		KV:        foundRec.Meta,
		Relations: make(map[string][]UFLRelation),
	}
	if entity.KV == nil {
		entity.KV = make(map[string]any)
	}

	// 2. Hydrate Relations from Graph Store
	if query.Options.Hydrate.MaxDegree > 1 {
		rels, err := db.Graph.QuerySubject(ctx, id)
		if err == nil {
			for _, r := range rels {
				pred := quadStr(r.Predicate)
				obj := quadStr(r.Object)
				degree := GetDegree(pred)

				if int(degree) <= query.Options.Hydrate.MaxDegree {
					group := "quaternary"
					switch degree {
					case DegreeSecondary: group = "secondary"
					case DegreeTertiary: group = "tertiary"
					}
					entity.Relations[group] = append(entity.Relations[group], UFLRelation{
						Predicate: pred,
						Object:    obj,
					})
				}
			}
		}
	}

	// 3. Hydrate Vector if requested
	if query.Options.Hydrate.IncludeVector {
		nodeID := xxhash.Sum64String(id)
		vector, _, err := db.Vector.GetHNSWNode(ctx, nodeID)
		if err == nil {
			entity.Vector = vector
		}
	}

	return entity, nil
}

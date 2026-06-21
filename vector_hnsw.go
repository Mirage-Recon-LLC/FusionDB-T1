// Copyright (c) 2026 Johnny Harvey
// All rights reserved.
package fusiondb

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"sort"

	"github.com/dgraph-io/badger/v4"
)

const (
	HNSWMetaPrefix   byte = 0x03
	HNSWMaxNeighbors int  = 16
	IDMapPrefix      byte = 0x04
)

type HNSWMetadata struct {
	EntryNodeID uint64
	MaxLayer    int
}

type SearchCandidate struct {
	NodeID   uint64
	Vector   []float32
	Distance float32
}

// CosineDistance computes the cosine distance between two float32 vectors.
func CosineDistance(A []float32, B []float32) (float32, error) {
	if len(A) != len(B) {
		return 1.0, errors.New("dimension mismatch")
	}
	if len(A) == 0 {
		return 1.0, errors.New("empty vectors")
	}

	var dotProduct, normA, normB float64
	for i := range A {
		a := float64(A[i])
		b := float64(B[i])
		dotProduct += a * b
		normA += a * a
		normB += b * b
	}

	if normA == 0 || normB == 0 {
		return 1.0, nil
	}

	similarity := dotProduct / (math.Sqrt(normA) * math.Sqrt(normB))
	if similarity < -1.0 {
		similarity = -1.0
	} else if similarity > 1.0 {
		similarity = 1.0
	}

	return float32(1.0 - similarity), nil
}

// GetHNSWMetadata retrieves the HNSW entry point and max layer metadata.
// Metadata payload is 12 bytes: 8-byte EntryNodeID + 4-byte MaxLayer.
func GetHNSWMetadata(txn *badger.Txn) (*HNSWMetadata, error) {
	key := []byte{HNSWMetaPrefix, 'm', 'e', 't', 'a'}
	item, err := txn.Get(key)
	if err != nil {
		return nil, err
	}

	var meta HNSWMetadata
	err = item.Value(func(val []byte) error {
		if len(val) < 12 {
			return errors.New("invalid metadata payload length")
		}
		meta.EntryNodeID = binary.BigEndian.Uint64(val[0:8])
		meta.MaxLayer = int(binary.BigEndian.Uint32(val[8:12]))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &meta, nil
}

// SaveHNSWMetadata stores the HNSW metadata back to Badger.
func SaveHNSWMetadata(txn *badger.Txn, meta *HNSWMetadata) error {
	key := []byte{HNSWMetaPrefix, 'm', 'e', 't', 'a'}
	val := make([]byte, 12)
	binary.BigEndian.PutUint64(val[0:8], meta.EntryNodeID)
	binary.BigEndian.PutUint32(val[8:12], uint32(meta.MaxLayer))
	return txn.Set(key, val)
}

// StoreHNSWNodeInTxn saves the vector and neighbor list for a given node within a transaction.
func StoreHNSWNodeInTxn(txn *badger.Txn, nodeID uint64, vector []float32, neighbors []uint64) error {
	key := buildVectorKey(0x02, nodeID)
	val := serializeCometNode(vector, neighbors)
	return txn.Set(key, val)
}

// GetNodeByTxn fetches a vector node's vector and neighbors from the database.
func GetNodeByTxn(txn *badger.Txn, nodeID uint64) ([]float32, []uint64, error) {
	key := buildVectorKey(0x02, nodeID)
	item, err := txn.Get(key)
	if err != nil {
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}

	var vector []float32
	var neighbors []uint64
	err = item.Value(func(val []byte) error {
		vector, neighbors = deserializeCometNode(val)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return vector, neighbors, nil
}

func insertSorted(candidates []SearchCandidate, candidate SearchCandidate, k int) []SearchCandidate {
	idx := len(candidates)
	for i, c := range candidates {
		if candidate.Distance < c.Distance {
			idx = i
			break
		}
	}
	if idx >= k {
		return candidates
	}
	candidates = append(candidates, SearchCandidate{})
	copy(candidates[idx+1:], candidates[idx:])
	candidates[idx] = candidate
	if len(candidates) > k {
		candidates = candidates[:k]
	}
	return candidates
}

func insertCandidate(list []SearchCandidate, item SearchCandidate) []SearchCandidate {
	idx := len(list)
	for i, c := range list {
		if item.Distance < c.Distance {
			idx = i
			break
		}
	}
	list = append(list, SearchCandidate{})
	copy(list[idx+1:], list[idx:])
	list[idx] = item
	return list
}

func sortHistory(candidates []SearchCandidate) {
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Distance < candidates[j].Distance
	})
}

// LinearScanFallback scans the entire vector space (prefix 0x02) to find the k nearest neighbors.
// Keys are 9 bytes: 1-byte prefix + 8-byte uint64 nodeID.
func LinearScanFallback(ctx context.Context, txn *badger.Txn, queryVector []float32, k int) ([]SearchCandidate, error) {
	opts := badger.DefaultIteratorOptions
	it := txn.NewIterator(opts)
	defer it.Close()

	prefix := []byte{0x02}
	var candidates []SearchCandidate

	for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}

		item := it.Item()
		key := item.Key()
		if len(key) != 9 {
			continue
		}
		nodeID := binary.BigEndian.Uint64(key[1:9])

		var vec []float32
		err := item.Value(func(val []byte) error {
			vec, _ = deserializeCometNode(val)
			return nil
		})
		if err != nil {
			return nil, err
		}

		dist, err := CosineDistance(queryVector, vec)
		if err != nil {
			continue
		}

		candidates = insertSorted(candidates, SearchCandidate{
			NodeID:   nodeID,
			Vector:   vec,
			Distance: dist,
		}, k)
	}

	return candidates, nil
}

// GreedyHillClimbingNSW traverses the graph moving greedily towards the closest unvisited neighbor.
func GreedyHillClimbingNSW(ctx context.Context, txn *badger.Txn, queryVector []float32, k int) ([]SearchCandidate, error) {
	meta, err := GetHNSWMetadata(txn)
	if err != nil {
		return LinearScanFallback(ctx, txn, queryVector, k)
	}

	currNodeID := meta.EntryNodeID
	currVector, currNeighbors, err := GetNodeByTxn(txn, currNodeID)
	if err != nil {
		return LinearScanFallback(ctx, txn, queryVector, k)
	}

	currDist, err := CosineDistance(queryVector, currVector)
	if err != nil {
		return nil, err
	}

	visited := map[uint64]bool{currNodeID: true}
	history := []SearchCandidate{
		{NodeID: currNodeID, Vector: currVector, Distance: currDist},
	}

	for {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}

		var bestNeighborNodeID uint64
		var bestNeighborVector []float32
		var bestNeighborNeighbors []uint64
		bestNeighborDist := currDist
		foundCloser := false

		for _, neighborID := range currNeighbors {
			if visited[neighborID] {
				continue
			}
			visited[neighborID] = true

			nVec, nNeigh, err := GetNodeByTxn(txn, neighborID)
			if err != nil {
				continue
			}

			dist, err := CosineDistance(queryVector, nVec)
			if err != nil {
				continue
			}

			history = append(history, SearchCandidate{
				NodeID:   neighborID,
				Vector:   nVec,
				Distance: dist,
			})

			if dist < bestNeighborDist {
				bestNeighborNodeID = neighborID
				bestNeighborVector = nVec
				bestNeighborNeighbors = nNeigh
				bestNeighborDist = dist
				foundCloser = true
			}
		}

		if foundCloser {
			currNodeID = bestNeighborNodeID
			currVector = bestNeighborVector
			currNeighbors = bestNeighborNeighbors
			currDist = bestNeighborDist
		} else {
			break
		}
	}

	sortHistory(history)
	if len(history) > k {
		history = history[:k]
	}
	return history, nil
}

// SearchHNSWGraph runs HNSW Beam Search starting from the entry point.
func SearchHNSWGraph(ctx context.Context, txn *badger.Txn, queryVector []float32, k int, ef int) ([]SearchCandidate, error) {
	if ef < k {
		ef = k
	}

	meta, err := GetHNSWMetadata(txn)
	if err != nil {
		return LinearScanFallback(ctx, txn, queryVector, k)
	}

	currNodeID := meta.EntryNodeID
	vec, _, err := GetNodeByTxn(txn, currNodeID)
	if err != nil {
		return LinearScanFallback(ctx, txn, queryVector, k)
	}

	dist, err := CosineDistance(queryVector, vec)
	if err != nil {
		return nil, err
	}

	startCand := SearchCandidate{
		NodeID:   currNodeID,
		Vector:   vec,
		Distance: dist,
	}

	candidates := []SearchCandidate{startCand}
	results := []SearchCandidate{startCand}
	visited := map[uint64]bool{currNodeID: true}

	for len(candidates) > 0 {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}

		c := candidates[0]
		candidates = candidates[1:]

		if len(results) >= ef && c.Distance > results[len(results)-1].Distance {
			break
		}

		_, cNeighbors, err := GetNodeByTxn(txn, c.NodeID)
		if err != nil {
			continue
		}

		for _, neighbor := range cNeighbors {
			if visited[neighbor] {
				continue
			}
			visited[neighbor] = true

			nVec, _, err := GetNodeByTxn(txn, neighbor)
			if err != nil {
				continue
			}

			nDist, err := CosineDistance(queryVector, nVec)
			if err != nil {
				continue
			}

			if len(results) < ef || nDist < results[len(results)-1].Distance {
				candidates = insertCandidate(candidates, SearchCandidate{
					NodeID:   neighbor,
					Vector:   nVec,
					Distance: nDist,
				})
				results = insertCandidate(results, SearchCandidate{
					NodeID:   neighbor,
					Vector:   nVec,
					Distance: nDist,
				})
				if len(results) > ef {
					results = results[:ef]
				}
			}
		}
	}

	if len(results) > k {
		results = results[:k]
	}
	return results, nil
}

// InsertHNSWNode inserts a node into the HNSW graph and bidirectionally links neighbors.
func InsertHNSWNode(ctx context.Context, txn *badger.Txn, nodeID uint64, vector []float32) error {
	_, err := GetHNSWMetadata(txn)
	if err != nil {
		if errors.Is(err, badger.ErrKeyNotFound) {
			if err := StoreHNSWNodeInTxn(txn, nodeID, vector, []uint64{}); err != nil {
				return err
			}
			return SaveHNSWMetadata(txn, &HNSWMetadata{EntryNodeID: nodeID, MaxLayer: 0})
		}
		return err
	}

	candidates, err := SearchHNSWGraph(ctx, txn, vector, HNSWMaxNeighbors, HNSWMaxNeighbors*2)
	if err != nil {
		candidates, err = LinearScanFallback(ctx, txn, vector, HNSWMaxNeighbors)
		if err != nil {
			return err
		}
	}

	neighborIDs := make([]uint64, 0, len(candidates))
	for _, c := range candidates {
		if c.NodeID != nodeID {
			neighborIDs = append(neighborIDs, c.NodeID)
		}
	}
	if len(neighborIDs) > HNSWMaxNeighbors {
		neighborIDs = neighborIDs[:HNSWMaxNeighbors]
	}

	if err := StoreHNSWNodeInTxn(txn, nodeID, vector, neighborIDs); err != nil {
		return err
	}

	for _, neighborID := range neighborIDs {
		nVec, nNeighbors, err := GetNodeByTxn(txn, neighborID)
		if err != nil {
			continue
		}

		exists := false
		for _, nid := range nNeighbors {
			if nid == nodeID {
				exists = true
				break
			}
		}
		if !exists {
			nNeighbors = append(nNeighbors, nodeID)
		}

		var updatedNeighbors []uint64
		if len(nNeighbors) > HNSWMaxNeighbors*2 {
			var nCandidates []SearchCandidate
			for _, nid := range nNeighbors {
				var nidVec []float32
				if nid == nodeID {
					nidVec = vector
				} else {
					var err error
					nidVec, _, err = GetNodeByTxn(txn, nid)
					if err != nil {
						continue
					}
				}
				dist, err := CosineDistance(nVec, nidVec)
				if err != nil {
					continue
				}
				nCandidates = append(nCandidates, SearchCandidate{
					NodeID:   nid,
					Vector:   nidVec,
					Distance: dist,
				})
			}
			sortHistory(nCandidates)
			if len(nCandidates) > HNSWMaxNeighbors {
				nCandidates = nCandidates[:HNSWMaxNeighbors]
			}
			updatedNeighbors = make([]uint64, len(nCandidates))
			for i, c := range nCandidates {
				updatedNeighbors[i] = c.NodeID
			}
		} else {
			updatedNeighbors = nNeighbors
		}

		if err := StoreHNSWNodeInTxn(txn, neighborID, nVec, updatedNeighbors); err != nil {
			return err
		}
	}

	return nil
}

// SaveIDMapping maps a nodeID (uint64) back to its string ID. Key is 9 bytes.
func SaveIDMapping(txn *badger.Txn, nodeID uint64, id string) error {
	key := make([]byte, 9)
	key[0] = IDMapPrefix
	binary.BigEndian.PutUint64(key[1:], nodeID)
	return txn.Set(key, []byte(id))
}

// GetIDMapping retrieves the string ID for a given nodeID.
func GetIDMapping(txn *badger.Txn, nodeID uint64) (string, error) {
	key := make([]byte, 9)
	key[0] = IDMapPrefix
	binary.BigEndian.PutUint64(key[1:], nodeID)
	item, err := txn.Get(key)
	if err != nil {
		return "", err
	}
	var id string
	err = item.Value(func(val []byte) error {
		id = string(val)
		return nil
	})
	return id, err
}

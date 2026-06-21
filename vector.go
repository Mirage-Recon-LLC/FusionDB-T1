// Copyright (c) 2026 Johnny Harvey
// All rights reserved.
package fusiondb

import (
	"context"
	"encoding/binary"
	"errors"
	"math"

	"github.com/dgraph-io/badger/v4"
)

type VectorEntry struct {
	NodeID    uint64
	Vector    []float32
	Neighbors []uint64
}

type VectorStore struct {
	db *badger.DB
}

// buildVectorKey returns a 9-byte key: 1-byte prefix + 8-byte uint64 nodeID.
func buildVectorKey(prefix byte, nodeID uint64) []byte {
	key := make([]byte, 9)
	key[0] = prefix
	binary.BigEndian.PutUint64(key[1:], nodeID)
	return key
}

func serializeVector(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.BigEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

func serializeCometNode(vector []float32, neighbors []uint64) []byte {
	vecBytes := serializeVector(vector)
	buf := make([]byte, 4+len(vecBytes)+4+len(neighbors)*8)
	offset := 0
	binary.BigEndian.PutUint32(buf[offset:], uint32(len(vector)))
	offset += 4
	copy(buf[offset:], vecBytes)
	offset += len(vecBytes)
	binary.BigEndian.PutUint32(buf[offset:], uint32(len(neighbors)))
	offset += 4
	for _, n := range neighbors {
		binary.BigEndian.PutUint64(buf[offset:], n)
		offset += 8
	}
	return buf
}

func deserializeCometNode(data []byte) (vector []float32, neighbors []uint64) {
	if len(data) < 8 {
		return nil, nil
	}
	offset := 0

	vecLen := binary.BigEndian.Uint32(data[offset:])
	offset += 4

	if len(data) < offset+int(vecLen*4)+4 {
		return nil, nil
	}

	vector = make([]float32, vecLen)
	for i := range vector {
		bits := binary.BigEndian.Uint32(data[offset:])
		vector[i] = math.Float32frombits(bits)
		offset += 4
	}

	nCount := binary.BigEndian.Uint32(data[offset:])
	offset += 4

	if len(data) < offset+int(nCount*8) {
		return vector, nil
	}

	neighbors = make([]uint64, nCount)
	for i := range neighbors {
		neighbors[i] = binary.BigEndian.Uint64(data[offset:])
		offset += 8
	}
	return
}

func (vs *VectorStore) StoreHNSWNode(ctx context.Context, nodeID uint64, vector []float32, neighbors []uint64) error {
	return safeUpdate(ctx, vs.db, func(txn *badger.Txn) error {
		key := buildVectorKey(0x02, nodeID)
		val := serializeCometNode(vector, neighbors)
		return txn.Set(key, val)
	})
}

func (vs *VectorStore) GetHNSWNode(ctx context.Context, nodeID uint64) (vector []float32, neighbors []uint64, err error) {
	key := buildVectorKey(0x02, nodeID)
	err = vs.db.View(func(txn *badger.Txn) error {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
		}

		item, err := txn.Get(key)
		if err != nil {
			if errors.Is(err, badger.ErrKeyNotFound) {
				return ErrNotFound
			}
			return err
		}
		return item.Value(func(val []byte) error {
			vector, neighbors = deserializeCometNode(val)
			return nil
		})
	})
	return
}

func (vs *VectorStore) Search(ctx context.Context, queryVector []float32, k int, ef int) ([]SearchCandidate, error) {
	var results []SearchCandidate
	err := vs.db.View(func(txn *badger.Txn) error {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		var err error
		results, err = SearchHNSWGraph(ctx, txn, queryVector, k, ef)
		return err
	})
	return results, err
}

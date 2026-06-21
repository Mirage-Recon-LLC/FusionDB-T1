// Copyright (c) 2026 Johnny Harvey
// All rights reserved.
package fusiondb

import (
	"bytes"
	"context"
	"encoding/binary"

	"github.com/cayleygraph/quad"
	"github.com/dgraph-io/badger/v4"
)

type GraphStore struct {
	db *badger.DB
}

// BuildGraphKey constructs a BadgerDB key for a graph quad using deterministic length-prefixing.
// Format: [prefix byte][subject len: 2B][subject str][predicate len: 2B][predicate str][object len: 2B][object str]
func BuildGraphKey(prefix byte, subject, predicate, object string) []byte {
	var buf bytes.Buffer
	buf.WriteByte(prefix)

	binary.Write(&buf, binary.BigEndian, uint16(len(subject)))
	buf.WriteString(subject)

	binary.Write(&buf, binary.BigEndian, uint16(len(predicate)))
	buf.WriteString(predicate)

	binary.Write(&buf, binary.BigEndian, uint16(len(object)))
	buf.WriteString(object)
	return buf.Bytes()
}

// buildGraphPrefix constructs the safe scan prefix for all quads with a given subject.
func buildGraphPrefix(prefix byte, subject string) []byte {
	var buf bytes.Buffer
	buf.WriteByte(prefix)
	binary.Write(&buf, binary.BigEndian, uint16(len(subject)))
	buf.WriteString(subject)
	return buf.Bytes()
}

// parseGraphKey safely unpacks subject, predicate, and object boundaries by parsing integer offsets.
func parseGraphKey(key []byte) (subject, predicate, object string, ok bool) {
	if len(key) < 7 { // 1 byte prefix + 3 * 2 bytes length
		return "", "", "", false
	}
	idx := 1

	// Unpack Subject
	if idx+2 > len(key) {
		return "", "", "", false
	}
	subLen := binary.BigEndian.Uint16(key[idx : idx+2])
	idx += 2
	if idx+int(subLen) > len(key) {
		return "", "", "", false
	}
	subject = string(key[idx : idx+int(subLen)])
	idx += int(subLen)

	// Unpack Predicate
	if idx+2 > len(key) {
		return "", "", "", false
	}
	predLen := binary.BigEndian.Uint16(key[idx : idx+2])
	idx += 2
	if idx+int(predLen) > len(key) {
		return "", "", "", false
	}
	predicate = string(key[idx : idx+int(predLen)])
	idx += int(predLen)

	// Unpack Object
	if idx+2 > len(key) {
		return "", "", "", false
	}
	objLen := binary.BigEndian.Uint16(key[idx : idx+2])
	idx += 2
	if idx+int(objLen) > len(key) {
		return "", "", "", false
	}
	object = string(key[idx : idx+int(objLen)])

	return subject, predicate, object, true
}

func (gs *GraphStore) AddQuadInTier(ctx context.Context, q quad.Quad, tier string) error {
	return safeUpdate(ctx, gs.db, func(txn *badger.Txn) error {
		fwdKey := BuildGraphKey(0x00, quadStr(q.Subject), quadStr(q.Predicate), quadStr(q.Object))
		revKey := BuildGraphKey(0x01, quadStr(q.Object), quadStr(q.Predicate), quadStr(q.Subject))
		tag := []byte{tierByte(tier)}
		if err := txn.Set(fwdKey, tag); err != nil {
			return err
		}
		return txn.Set(revKey, tag)
	})
}

// AddQuadStringsInTxn writes a graph quad directly into the provided transaction.
func (gs *GraphStore) AddQuadStringsInTxn(txn *badger.Txn, subject, predicate, object, tier string) error {
	fwdKey := BuildGraphKey(0x00, subject, predicate, object)
	revKey := BuildGraphKey(0x01, object, predicate, subject)
	tag := []byte{tierByte(tier)}
	if err := txn.Set(fwdKey, tag); err != nil {
		return err
	}
	return txn.Set(revKey, tag)
}

func (gs *GraphStore) QuerySubject(ctx context.Context, subject string) ([]quad.Quad, error) {
	var results []quad.Quad
	prefix := buildGraphPrefix(0x00, subject)
	err := gs.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			if ctx != nil {
				if err := ctx.Err(); err != nil {
					return err
				}
			}

			item := it.Item()
			k := item.KeyCopy(nil)
			sub, pred, obj, ok := parseGraphKey(k)
			if !ok {
				continue
			}
			results = append(results, quad.Make(sub, pred, obj, ""))
		}
		return nil
	})
	return results, err
}

func quadStr(v quad.Value) string {
	if v == nil {
		return ""
	}
	if iri, ok := v.(quad.IRI); ok {
		return string(iri)
	}
	if s, ok := v.(quad.String); ok {
		return string(s)
	}
	return v.String()
}

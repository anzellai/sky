package bluedb

import (
	"bytes"
	"encoding/binary"
)

// ColType tags a column's value domain so the ONE canonical encoder (encodeIndexKey)
// can pick its order-preserving encoding (§2.2). int/text/bool (+ composite / descending)
// are range-optimized; real/money/blob have NO proven order-preserving encoding and route
// through the conservative fallback witness (§2.2). The high bit is the descending flag.
type ColType uint8

const (
	ColInt   ColType = 1 // sign-biased big-endian 8-byte
	ColText  ColType = 2 // raw UTF-8 bytes (lexicographic == byte order)
	ColBool  ColType = 3 // a single byte (0x00 / 0x01)
	ColReal  ColType = 4 // fallback — no order-preserving encoding
	ColMoney ColType = 5 // fallback
	ColBlob  ColType = 6 // fallback

	colDescendingFlag ColType = 0x80 // OR-flag: the column's encoded bytes are inverted
)

// Descending marks a column as descending: encodeIndexKey inverts its bytes AND the
// scan-bound builder (encodeScanRange) swaps lo/hi. The two facts live inside the ONE
// encoder path so they cannot be applied on one side only (§2.2, R-2.1).
func Descending(c ColType) ColType { return c | colDescendingFlag }

// rangeOptimized reports whether colType has a proven order-preserving encoding and is
// therefore validated by the tight byte-range test (int/text/bool). Everything else uses
// the conservative fallback witness (§2.2).
func rangeOptimized(c ColType) bool {
	switch c &^ colDescendingFlag {
	case ColInt, ColText, ColBool:
		return true
	}
	return false
}

// encodeIndexKey is the SOLE producer of index-coordinate value bytes (§2.2, R-2.1).
// BOTH the scan-bound construction (encodeScanRange, which Txn.Scan's lo/hi go through)
// AND the coord emission (tx.indexer → IndexCoord.Key) call it. There is NO second encoder
// anywhere, so drift between a scan bound and a change coord is structurally impossible.
//
// Order-preserving encodings:
//   - int  — sign-biased big-endian 8-byte (flip the sign bit so negatives sort below
//     positives, then compare as unsigned bytes). Pass the value as BE8 (see IntKey).
//   - text — raw UTF-8 bytes.
//   - bool — a single byte (0x00 / 0x01).
//   - descending — the encoded bytes are bitwise-inverted (a larger value sorts smaller).
//
// Fallback colTypes (real/money/blob) have no proven order-preserving encoding; a coord is
// still emitted (raw bytes) so the change carries an Index the fallback witness matches on,
// but the byte-range test is never applied to them (§2.2).
func encodeIndexKey(indexID IndexID, colType ColType, value []byte) []byte {
	_ = indexID // index identity is matched separately (IndexCoord.Index / indexRange.index)
	raw := encodeColValue(colType&^colDescendingFlag, value)
	if colType&colDescendingFlag != 0 {
		raw = invertBytes(raw)
	}
	return raw
}

// encodeColValue produces the order-preserving (or fallback) bytes for one column value.
func encodeColValue(c ColType, value []byte) []byte {
	switch c {
	case ColInt:
		// sign-biased BE8: right-align to 8 bytes (callers use IntKey), flip the sign bit.
		out := make([]byte, 8)
		if len(value) >= 8 {
			copy(out, value[len(value)-8:])
		} else {
			copy(out[8-len(value):], value)
		}
		out[0] ^= 0x80
		return out
	case ColBool:
		if len(value) > 0 && value[0] != 0 {
			return []byte{0x01}
		}
		return []byte{0x00}
	case ColText:
		return append([]byte(nil), value...)
	default:
		// Fallback types (real/money/blob): raw bytes. NOT order-preserving — validated via
		// the collection/index-level witness, never the byte-range test.
		return append([]byte(nil), value...)
	}
}

// IntKey returns the big-endian 8-byte form of an int64, ready to hand to encodeIndexKey
// with ColInt (which applies the sign bias). A helper so callers/tests never hand-roll the
// width.
func IntKey(n int64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(n))
	return b
}

// encodeScanRange is the single scan-bound builder (§2.2). It encodes the value-space
// bounds through encodeIndexKey and returns the CLOSED encoded interval [lo, hi] with
// lo ≤ hi in byte order. For a descending column the encoding is anti-monotone, so the two
// encoded bounds come out reversed; sorting them is exactly the "scan SWAPS lo/hi" fact the
// design requires — and because it lives here (the one bound-builder that calls the one
// encoder), invert + swap can never be applied on one side only. Closed intervals may
// over-reject a coord exactly at the boundary (safe: over-reject, never under-reject).
func encodeScanRange(indexID IndexID, colType ColType, loVal, hiVal []byte) (lo, hi []byte) {
	a := encodeIndexKey(indexID, colType, loVal)
	b := encodeIndexKey(indexID, colType, hiVal)
	if bytes.Compare(a, b) <= 0 {
		return a, b
	}
	return b, a // descending inverted the order → swap so lo ≤ hi in encoded bytes
}

// IndexCol is one column of a composite index key.
type IndexCol struct {
	Type  ColType
	Value []byte
}

// encodeCompositeKey concatenates the per-column encodings in declared column order, so
// lexicographic byte order == tuple order (§2.2). Order-preserving as long as every
// variable-width column (text/blob) is a suffix of the tuple — fixed-width columns
// (int BE8, bool 1B) must precede them. Both the scan bound and the coord go through this
// same builder, so they byte-match.
func encodeCompositeKey(indexID IndexID, cols []IndexCol) []byte {
	var out []byte
	for _, c := range cols {
		out = append(out, encodeIndexKey(indexID, c.Type, c.Value)...)
	}
	return out
}

// encodeCompositeScanRange builds the CLOSED encoded interval for a composite scan, through
// the same encodeCompositeKey both sides use.
func encodeCompositeScanRange(indexID IndexID, lo, hi []IndexCol) (loKey, hiKey []byte) {
	a := encodeCompositeKey(indexID, lo)
	b := encodeCompositeKey(indexID, hi)
	if bytes.Compare(a, b) <= 0 {
		return a, b
	}
	return b, a
}

// invertBytes returns the bitwise-NOT of b in a fresh slice (descending encoding).
func invertBytes(b []byte) []byte {
	out := make([]byte, len(b))
	for i := range b {
		out[i] = ^b[i]
	}
	return out
}

// inRangeClosed reports lo ≤ key ≤ hi in byte order (the closed-interval membership the
// validator uses for range-optimized index coords).
func inRangeClosed(lo, hi, key []byte) bool {
	return bytes.Compare(lo, key) <= 0 && bytes.Compare(key, hi) <= 0
}

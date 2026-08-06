package bluedb

// indexer.go — THE codec-driven indexer (§2.2/§2.3) and the record↔column mapping. buildIndexer
// turns a CollSchema into the closure Txn.SetIndexer installs: it decodes a stored row's indexed
// columns and emits ONE IndexCoord per declared index via the SINGLE canonical encodeIndexKey —
// the SAME encoder Txn.ScanRange's bounds go through, so a Put coord and a Scan bound byte-match
// by construction (R-2.1 at the L3 boundary).
//
// The row's canonical stored form on the embedded engine is the codec JSON blob (§2.3). In Phase
// 3a a stored row is a JSON object; decodeColumns extracts the schema's columns from it with the
// SAME normalization the value constructors (IntVal/TextVal/…) produce, so the write coord, the
// pre-image coord, and the query scan-bound all feed encodeIndexKey identical bytes (the fidelity
// contract, §2.3 / §9 R3-1).

import (
	"encoding/json"
	"sort"
	"strconv"
)

// buildIndexer produces the closure Txn.SetIndexer installs for a collection (§2.2). For a
// put/pre-image it decodes the record's declared-index columns and emits one IndexCoord per
// index. A NULL value emits NO coordinate (§2.3 — Nothing/JSON-null has nothing to encode; an
// IS-NULL predicate is validated via the collection witness, never a missed range). A
// not-order-preserving index type still emits a coord (raw bytes) so a fallback witness matches
// on the Index — but the byte-range test is never applied to it (rangeOptimized == false).
func buildIndexer(cs *CollSchema) func(userKey, record []byte) []IndexCoord {
	return func(_, record []byte) []IndexCoord {
		if len(cs.Indexes) == 0 {
			return nil
		}
		cols, err := decodeColumns(cs, record)
		if err != nil {
			return nil // a non-JSON record (e.g. a stored unique-key value) has no coords
		}
		out := make([]IndexCoord, 0, len(cs.Indexes))
		for i := range cs.Indexes {
			idx := &cs.Indexes[i]
			cv, present := cols[idx.Col]
			if !present || cv.Null {
				continue // NULL / absent → no coord (§2.3)
			}
			out = append(out, IndexCoord{Index: idx.ID, Key: encodeIndexKey(idx.ID, idx.Type, cv.Bytes)})
		}
		return out
	}
}

// decodeColumns extracts the schema's columns from a stored codec JSON blob as normalized
// ColValues. A field absent or JSON-null → Null. The normalization MUST match the value
// constructors below (the fidelity contract, §2.3).
func decodeColumns(cs *CollSchema, record []byte) (map[string]ColValue, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(record, &raw); err != nil {
		return nil, err
	}
	out := make(map[string]ColValue, len(cs.Cols))
	for i := range cs.Cols {
		spec := &cs.Cols[i]
		rv, present := raw[spec.Name]
		if !present || isJSONNull(rv) {
			out[spec.Name] = ColValue{Type: spec.Type, Null: true}
			continue
		}
		out[spec.Name] = rawToColValue(spec.Type, rv)
	}
	return out, nil
}

func isJSONNull(rv json.RawMessage) bool {
	s := trimJSONSpace(rv)
	return len(s) == 4 && s[0] == 'n' && s[1] == 'u' && s[2] == 'l' && s[3] == 'l'
}

func trimJSONSpace(rv json.RawMessage) []byte {
	i, j := 0, len(rv)
	for i < j && (rv[i] == ' ' || rv[i] == '\t' || rv[i] == '\n' || rv[i] == '\r') {
		i++
	}
	for j > i && (rv[j-1] == ' ' || rv[j-1] == '\t' || rv[j-1] == '\n' || rv[j-1] == '\r') {
		j--
	}
	return rv[i:j]
}

// rawToColValue converts one JSON field to a normalized ColValue per the column's engine ColType.
// The Bytes MUST be byte-identical to what the matching value constructor produces.
func rawToColValue(colType ColType, rv json.RawMessage) ColValue {
	base := colType &^ colDescendingFlag
	switch base {
	case ColInt:
		var n int64
		if err := json.Unmarshal(rv, &n); err != nil {
			// tolerate a JSON string carrying an int
			var s string
			if json.Unmarshal(rv, &s) == nil {
				if p, e := strconv.ParseInt(s, 10, 64); e == nil {
					n = p
				}
			}
		}
		return IntVal(n).withType(colType)
	case ColBool:
		var b bool
		_ = json.Unmarshal(rv, &b)
		return BoolVal(b).withType(colType)
	case ColText:
		var s string
		if json.Unmarshal(rv, &s) != nil {
			s = string(trimJSONSpace(rv)) // non-string JSON → raw text
		}
		return TextVal(s).withType(colType)
	case ColReal:
		var f float64
		_ = json.Unmarshal(rv, &f)
		return ColValue{Type: colType, Bytes: []byte(strconv.FormatFloat(f, 'g', -1, 64))}
	default: // ColMoney / ColBlob / any not-orderable fallback
		var s string
		if json.Unmarshal(rv, &s) != nil {
			s = string(trimJSONSpace(rv))
		}
		return ColValue{Type: colType, Bytes: []byte(s)}
	}
}

// withType overrides the ColType (preserving normalized Bytes) so a value built by the
// constructor with a base type carries the schema's exact (possibly descending) ColType.
func (cv ColValue) withType(t ColType) ColValue { cv.Type = t; return cv }

// ── value constructors (the normalized currency, §2.3) ───────────────────────────────────────
// These produce the SAME normalized Bytes decodeColumns produces from JSON, so a query bound and
// a write coord byte-match. A caller/test uses these to build a plan's Val / a Put's cols.

// IntVal builds a range-optimized integer value (Bytes = big-endian 8-byte, sign bias applied by
// encodeIndexKey).
func IntVal(n int64) ColValue { return ColValue{Type: ColInt, Bytes: IntKey(n)} }

// TextVal builds a range-optimized text value (Bytes = raw UTF-8).
func TextVal(s string) ColValue { return ColValue{Type: ColText, Bytes: []byte(s)} }

// BoolVal builds a range-optimized bool value (Bytes = {0x00}/{0x01}).
func BoolVal(b bool) ColValue {
	if b {
		return ColValue{Type: ColBool, Bytes: []byte{0x01}}
	}
	return ColValue{Type: ColBool, Bytes: []byte{0x00}}
}

// RealVal builds a NOT-order-preserving real value (fallback; validated via the collection/index
// witness, never a byte-range, §2.3). Bytes = the canonical float text.
func RealVal(f float64) ColValue {
	return ColValue{Type: ColReal, Bytes: []byte(strconv.FormatFloat(f, 'g', -1, 64))}
}

// MoneyVal builds a NOT-order-preserving money value (fallback, §2.3). Bytes = the "ISO_CODE
// AMOUNT" text (the Store SqlMoney round-trip form). NEVER range-optimized — lexical byte order ≠
// numeric order.
func MoneyVal(s string) ColValue { return ColValue{Type: ColMoney, Bytes: []byte(s)} }

// BlobVal builds a NOT-order-preserving blob value (fallback, §2.3).
func BlobVal(b []byte) ColValue {
	return ColValue{Type: ColBlob, Bytes: append([]byte(nil), b...)}
}

// NullVal builds a typed NULL value (emits no coord; an IS-NULL predicate routes to the witness).
func NullVal(t ColType) ColValue { return ColValue{Type: t, Null: true} }

// ── ordering + pagination (§4.4) ─────────────────────────────────────────────────────────────

// orderAndPage sorts rows by the plan's OrderSpecs (priority order) then applies offset/limit.
// Ordering uses the same order-preserving encode-compare as validation for range-optimized
// columns, and the numeric-parse fallback for not-orderable columns (autocommit ordering; the
// dialect-forced NULLS FIRST + LIKE collation of §0.6 is a Phase-3b parity concern).
func orderAndPage(cs *CollSchema, rows [][]byte, plan QueryPlan) [][]byte {
	if len(plan.Orders) > 0 {
		decoded := make([]map[string]ColValue, len(rows))
		for i, r := range rows {
			cols, err := decodeColumns(cs, r)
			if err != nil {
				cols = map[string]ColValue{}
			}
			decoded[i] = cols
		}
		idxs := make([]int, len(rows))
		for i := range idxs {
			idxs[i] = i
		}
		sort.SliceStable(idxs, func(a, b int) bool {
			ca, cb := decoded[idxs[a]], decoded[idxs[b]]
			for _, o := range plan.Orders {
				va, oka := ca[o.Col]
				vb, okb := cb[o.Col]
				// NULLs first (the forced §0.6 semantics the embedded arm mirrors).
				na, nb := !oka || va.Null, !okb || vb.Null
				if na != nb {
					less := na // a is NULL, b is not → a first
					if o.Desc {
						less = nb
					}
					return less
				}
				if na && nb {
					continue
				}
				c := orderCompare(va, vb)
				if c == 0 {
					continue
				}
				if o.Desc {
					return c > 0
				}
				return c < 0
			}
			return false
		})
		sorted := make([][]byte, len(rows))
		for i, id := range idxs {
			sorted[i] = rows[id]
		}
		rows = sorted
	}

	if plan.Offset > 0 {
		if plan.Offset >= len(rows) {
			return nil
		}
		rows = rows[plan.Offset:]
	}
	if plan.Limit >= 0 && plan.Limit < len(rows) {
		rows = rows[:plan.Limit]
	}
	return rows
}

// orderCompare returns -1/0/1 for two non-NULL column values in value order.
func orderCompare(a, b ColValue) int {
	return compareValues(a, b)
}

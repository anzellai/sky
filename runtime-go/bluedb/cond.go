package bluedb

// cond.go — the RESOLVED predicate tree (CondNode), the in-RAM evaluator (bluedbEvalCond), and
// the txn-Query read-set classifier (classifyIndexable, §2.6). In Phase 3a a test/caller builds a
// CondNode directly; in Phase 3b Store.planJson decodes into it. The evaluator is the PORT of the
// prior-art bluedbEvalCond — the exact-filter applied to rows a scan returns; the classifier is
// what routes a txn-Query into Txn.ScanRange (precise range read-set) vs Txn.ScanCollection
// (coarse collection witness), the phantom-hole fix.

import (
	"bytes"
	"math"
	"strconv"
	"strings"
)

// CondOp discriminates a resolved Cond leaf/combinator.
type CondOp uint8

const (
	CondTrue    CondOp = iota // always true (empty WHERE)
	CondEq                    // col == val
	CondNe                    // col != val
	CondGt                    // col > val
	CondGte                   // col >= val
	CondLt                    // col < val
	CondLte                   // col <= val
	CondLike                  // col LIKE val (SQL % / _ wildcards; ASCII, forced by §0.6)
	CondIn                    // col IN vals (empty vals ⇒ always false, §0.6)
	CondIsNull                // col IS NULL (§2.3)
	CondNotNull               // col IS NOT NULL (§2.3)
	CondAnd                   // conjunction of Kids
	CondOr                    // disjunction of Kids
	CondNot                   // negation of Kids[0]
)

// CondNode is one node of a resolved predicate tree. A leaf carries Col + Type (the column's
// mapped engine ColType) + Val (or Vals for CondIn); a combinator carries Kids.
type CondNode struct {
	Op   CondOp
	Col  string
	Type ColType
	Val  ColValue
	Vals []ColValue
	Kids []CondNode
}

// bluedbEvalCond evaluates cond against a row's decoded columns (§4.4 / §2.6 residual filter).
// cols is the record's column values keyed by column name (decodeColumns). NULL semantics follow
// SQL 3-valued logic collapsed to a boolean: a comparison against a NULL column is a NON-match;
// IS NULL / IS NOT NULL test the Null flag explicitly.
func bluedbEvalCond(cols map[string]ColValue, cond *CondNode) bool {
	switch cond.Op {
	case CondTrue:
		return true
	case CondAnd:
		for i := range cond.Kids {
			if !bluedbEvalCond(cols, &cond.Kids[i]) {
				return false
			}
		}
		return true
	case CondOr:
		for i := range cond.Kids {
			if bluedbEvalCond(cols, &cond.Kids[i]) {
				return true
			}
		}
		return false
	case CondNot:
		if len(cond.Kids) == 0 {
			return true
		}
		return !bluedbEvalCond(cols, &cond.Kids[0])
	case CondIsNull:
		cv, ok := cols[cond.Col]
		return !ok || cv.Null
	case CondNotNull:
		cv, ok := cols[cond.Col]
		return ok && !cv.Null
	case CondIn:
		cv, ok := cols[cond.Col]
		if !ok || cv.Null || len(cond.Vals) == 0 { // empty IN ⇒ always false (§0.6)
			return false
		}
		for i := range cond.Vals {
			if valuesEqual(cv, cond.Vals[i]) {
				return true
			}
		}
		return false
	case CondLike:
		cv, ok := cols[cond.Col]
		if !ok || cv.Null {
			return false
		}
		return likeMatch(string(cv.Bytes), string(cond.Val.Bytes))
	default: // Eq / Ne / Gt / Gte / Lt / Lte
		cv, ok := cols[cond.Col]
		if !ok || cv.Null {
			return false
		}
		return compareLeaf(cond.Op, cv, cond.Val)
	}
}

// valuesEqual reports byte-equal normalized values (Eq/In/Ne share this).
func valuesEqual(a, b ColValue) bool { return bytes.Equal(a.Bytes, b.Bytes) }

// compareLeaf evaluates an ordered comparison. For a range-optimized column both sides go through
// the ONE encodeIndexKey so the byte comparison is order-preserving (the same order the SSI
// validation uses). For a not-order-preserving column (real/money/blob) an ORDERED comparison
// falls back to a numeric parse (money "USD 100.00" → 100.00) so autocommit ordering is sane;
// equality is always byte-equal. (Correct dialect-forced ordering across SQL is a Phase-3b
// concern; the Phase-3a SSI guarantee for these columns is the collection witness, not a range.)
func compareLeaf(op CondOp, cv, want ColValue) bool {
	switch op {
	case CondEq:
		return valuesEqual(cv, want)
	case CondNe:
		return !valuesEqual(cv, want)
	}
	var c int
	if rangeOptimized(cv.Type) {
		c = bytes.Compare(encodeIndexKey(0, cv.Type&^colDescendingFlag, cv.Bytes),
			encodeIndexKey(0, want.Type&^colDescendingFlag, want.Bytes))
	} else if fa, oka := numericOf(cv); oka {
		if fb, okb := numericOf(want); okb {
			switch {
			case fa < fb:
				c = -1
			case fa > fb:
				c = 1
			}
		} else {
			c = bytes.Compare(cv.Bytes, want.Bytes)
		}
	} else {
		c = bytes.Compare(cv.Bytes, want.Bytes)
	}
	switch op {
	case CondGt:
		return c > 0
	case CondGte:
		return c >= 0
	case CondLt:
		return c < 0
	case CondLte:
		return c <= 0
	}
	return false
}

// compareValues returns -1/0/1 for two non-NULL column values in value order — the ordering
// primitive orderAndPage uses. Range-optimized types compare via the ONE encoder (order-
// preserving); not-orderable types fall back to a numeric parse then raw bytes.
func compareValues(a, b ColValue) int {
	if rangeOptimized(a.Type) {
		return bytes.Compare(encodeIndexKey(0, a.Type&^colDescendingFlag, a.Bytes),
			encodeIndexKey(0, b.Type&^colDescendingFlag, b.Bytes))
	}
	if fa, oka := numericOf(a); oka {
		if fb, okb := numericOf(b); okb {
			switch {
			case fa < fb:
				return -1
			case fa > fb:
				return 1
			default:
				return 0
			}
		}
	}
	return bytes.Compare(a.Bytes, b.Bytes)
}

// numericOf extracts a float from a fallback-typed value's bytes: the LAST whitespace-separated
// token parsed as a float ("USD 100.00" → 100.00; "3.14" → 3.14). Returns ok=false when no token
// parses.
func numericOf(cv ColValue) (float64, bool) {
	s := strings.TrimSpace(string(cv.Bytes))
	if s == "" {
		return 0, false
	}
	fields := strings.Fields(s)
	last := fields[len(fields)-1]
	f, err := strconv.ParseFloat(last, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// likeMatch implements SQL LIKE with % (any run) and _ (any single char), ASCII, case-sensitive
// (the forced §0.6 collation the embedded arm mirrors). Backtracking matcher — inputs are small.
func likeMatch(s, pat string) bool {
	// Iterative greedy backtracking (Thompson-style for % / _).
	sIdx, pIdx := 0, 0
	star, mark := -1, 0
	for sIdx < len(s) {
		if pIdx < len(pat) && (pat[pIdx] == '_' || pat[pIdx] == s[sIdx]) {
			sIdx++
			pIdx++
		} else if pIdx < len(pat) && pat[pIdx] == '%' {
			star = pIdx
			mark = sIdx
			pIdx++
		} else if star != -1 {
			pIdx = star + 1
			mark++
			sIdx = mark
		} else {
			return false
		}
	}
	for pIdx < len(pat) && pat[pIdx] == '%' {
		pIdx++
	}
	return pIdx == len(pat)
}

// ── the txn-Query read-set classifier (§2.6) ─────────────────────────────────────────────────

// indexHit is the result of classifyIndexable: a single range/equality leaf resolved onto a
// declared, range-optimized index → a precise Txn.ScanRange read-set.
type indexHit struct {
	idx    IndexSpec
	loVal  []byte
	hiVal  []byte
}

// classifyIndexable decides whether cond is a SINGLE indexable range/equality leaf on a DECLARED,
// range-optimized index (§2.6). If so it returns the index + value-space [loVal, hiVal] bounds to
// feed Txn.ScanRange (which records the precise index interval). ANYTHING ELSE — an OR/nested/NOT
// predicate, a predicate on a non-declared or NOT-order-preserving column (Money/Decimal/blob/
// Codec.map — §2.3), or an IS-NULL/IS-NOT-NULL leaf — returns ok=false so the caller routes to
// Txn.ScanCollection (the coarse-but-sound collection witness). A NOT-orderable column NEVER
// yields a range here.
//
// Supported precise shapes (v1, single-column ascending):
//   - eq on a range-optimized declared index                     → point range [v, v]
//   - a single gt/gte/lt/lte on int/bool                         → open range to the domain bound
//   - AND of exactly two bounds (one lower, one upper) on ONE    → [lo, hi]
//     range-optimized declared column
//
// An open-ended TEXT inequality has no clean domain max, so it routes to the collection witness
// (safe: over-reject). The residual predicate is always re-applied as an exact in-RAM filter by
// the caller, so an over-approximate range is sound.
func classifyIndexable(cs *CollSchema, cond *CondNode) (indexHit, bool) {
	switch cond.Op {
	case CondEq:
		idx, ok := declaredRangeIndex(cs, cond.Col)
		if !ok {
			return indexHit{}, false
		}
		v := cond.Val.Bytes
		return indexHit{idx: idx, loVal: v, hiVal: v}, true

	case CondGt, CondGte, CondLt, CondLte:
		idx, ok := declaredRangeIndex(cs, cond.Col)
		if !ok {
			return indexHit{}, false
		}
		lo, hi, ok := openBound(idx.Type, cond.Op, cond.Val.Bytes)
		if !ok {
			return indexHit{}, false
		}
		return indexHit{idx: idx, loVal: lo, hiVal: hi}, true

	case CondAnd:
		return classifyAndRange(cs, cond)
	}
	return indexHit{}, false
}

// classifyAndRange handles `lowerBound AND upperBound` on ONE declared range-optimized column.
func classifyAndRange(cs *CollSchema, cond *CondNode) (indexHit, bool) {
	if len(cond.Kids) != 2 {
		return indexHit{}, false
	}
	a, b := &cond.Kids[0], &cond.Kids[1]
	if a.Op == CondAnd || a.Op == CondOr || a.Op == CondNot ||
		b.Op == CondAnd || b.Op == CondOr || b.Op == CondNot {
		return indexHit{}, false
	}
	if a.Col == "" || a.Col != b.Col {
		return indexHit{}, false
	}
	idx, ok := declaredRangeIndex(cs, a.Col)
	if !ok {
		return indexHit{}, false
	}
	lo, hasLo := lowerOf(a.Op, a.Val.Bytes)
	if !hasLo {
		lo, hasLo = lowerOf(b.Op, b.Val.Bytes)
	}
	var hi []byte
	var hasHi bool
	hi, hasHi = upperOf(a.Op, a.Val.Bytes)
	if !hasHi {
		hi, hasHi = upperOf(b.Op, b.Val.Bytes)
	}
	if !hasLo || !hasHi {
		return indexHit{}, false
	}
	return indexHit{idx: idx, loVal: lo, hiVal: hi}, true
}

func lowerOf(op CondOp, v []byte) ([]byte, bool) {
	switch op {
	case CondGt, CondGte: // over-approx: include the boundary; residual eval excludes gt's boundary
		return v, true
	}
	return nil, false
}

func upperOf(op CondOp, v []byte) ([]byte, bool) {
	switch op {
	case CondLt, CondLte:
		return v, true
	}
	return nil, false
}

// openBound turns a single inequality into a [lo, hi] value-space range using the domain bound on
// the open side. Only int/bool have a clean finite domain bound here; text returns ok=false.
func openBound(colType ColType, op CondOp, v []byte) (lo, hi []byte, ok bool) {
	base := colType &^ colDescendingFlag
	dmin, dmax, hasDomain := domainBounds(base)
	if !hasDomain {
		return nil, nil, false
	}
	switch op {
	case CondGt, CondGte: // [v, dmax] (over-approx includes v; residual eval excludes gt boundary)
		return v, dmax, true
	case CondLt, CondLte: // [dmin, v]
		return dmin, v, true
	}
	return nil, nil, false
}

// domainBounds returns the minimum/maximum value-space bytes for a range-optimized type that has
// a finite domain (int, bool). Text has no finite max → hasDomain=false.
func domainBounds(base ColType) (dmin, dmax []byte, hasDomain bool) {
	switch base {
	case ColInt:
		return IntKey(math.MinInt64), IntKey(math.MaxInt64), true
	case ColBool:
		return []byte{0x00}, []byte{0x01}, true
	}
	return nil, nil, false
}

// declaredRangeIndex finds a declared, range-optimized, NON-unique-or-any secondary index on col.
// A NOT-order-preserving index type (real/money/blob) is rejected here (§2.3 — never a range).
func declaredRangeIndex(cs *CollSchema, col string) (IndexSpec, bool) {
	for i := range cs.Indexes {
		idx := cs.Indexes[i]
		if idx.Col == col && rangeOptimized(idx.Type) {
			return idx, true
		}
	}
	return IndexSpec{}, false
}

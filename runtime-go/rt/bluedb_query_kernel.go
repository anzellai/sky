// bluedb_query_kernel.go — general Cond querying on the BlueDB KV backend (P5).
//
// Std.Persist reuses the Std.Db.Store `Cond` query builder; on the KV backend the
// resolved Cond is serialized to a plan JSON (Store.condToPlanJson) and evaluated
// here in Go over each decoded record. Phase 1 is a full collection scan +
// predicate (an O(n) analytics/cold-path op — must never sit on the reactive hot
// path), with a MANDATORY result cap and early-stop. Scanning the exact record
// prefix (\x00x\x00d\x00<coll>\x00) means the walk never sees index/manifest or
// another collection's keys (F1), and the cap bounds result size (F2).
package rt

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"sky-app/bluedb"
)

// bluedbQueryMaxRows bounds a single query's returned rows (F2 — no unbounded
// scan materialising the whole store).
const bluedbQueryMaxRows = 10000

type bluedbOrderTerm struct {
	Col string `json:"col"`
	Dir string `json:"dir"`
}

type bluedbQueryPlan struct {
	Cond   map[string]any    `json:"cond"`
	Orders []bluedbOrderTerm `json:"orders"`
	Limit  int               `json:"limit"`
	Offset int               `json:"offset"`
}

func bluedbRecGet(rec map[string]any, col string) (any, bool) {
	if v, ok := rec[col]; ok {
		return v, true
	}
	if v, ok := rec[camelToSnake(col)]; ok {
		return v, true
	}
	return nil, false
}

// bluedbCmpVal compares a decoded record value against a plan value {k,s}.
// Returns -1/0/1, or 2 for "incomparable" (treated as not-equal / not-ordered).
func bluedbCmpVal(rv any, pv map[string]any) int {
	kind, _ := pv["k"].(string)
	s, _ := pv["s"].(string)
	switch kind {
	case "int", "float":
		var rf float64
		switch x := rv.(type) {
		case float64:
			rf = x
		case string:
			f, err := strconv.ParseFloat(x, 64)
			if err != nil {
				return 2
			}
			rf = f
		default:
			return 2
		}
		pf, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 2
		}
		switch {
		case rf < pf:
			return -1
		case rf > pf:
			return 1
		default:
			return 0
		}
	case "bool":
		rb, ok := rv.(bool)
		if !ok {
			return 2
		}
		pb := s == "true"
		if rb == pb {
			return 0
		}
		return 1
	default: // str
		rs, ok := rv.(string)
		if !ok {
			return 2
		}
		return strings.Compare(rs, s)
	}
}

// bluedbLike evaluates a SQL LIKE pattern (% = any run, _ = any one char) against s.
func bluedbLike(s, pat string) bool {
	// Recursive backtracking match (patterns are small).
	var match func(si, pi int) bool
	match = func(si, pi int) bool {
		for pi < len(pat) {
			switch pat[pi] {
			case '%':
				// collapse consecutive % and try each suffix
				for pi < len(pat) && pat[pi] == '%' {
					pi++
				}
				if pi == len(pat) {
					return true
				}
				for k := si; k <= len(s); k++ {
					if match(k, pi) {
						return true
					}
				}
				return false
			case '_':
				if si >= len(s) {
					return false
				}
				si++
				pi++
			default:
				if si >= len(s) || s[si] != pat[pi] {
					return false
				}
				si++
				pi++
			}
		}
		return si == len(s)
	}
	return match(0, 0)
}

func bluedbNodeList(v any) []map[string]any {
	arr, _ := v.([]any)
	out := make([]map[string]any, 0, len(arr))
	for _, e := range arr {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func bluedbValList(v any) []map[string]any { return bluedbNodeList(v) }

// bluedbEvalCond evaluates a plan node against a decoded record.
func bluedbEvalCond(node map[string]any, rec map[string]any) bool {
	t, _ := node["t"].(string)
	switch t {
	case "", "true":
		return true
	case "and":
		for _, c := range bluedbNodeList(node["cs"]) {
			if !bluedbEvalCond(c, rec) {
				return false
			}
		}
		return true
	case "or":
		for _, c := range bluedbNodeList(node["cs"]) {
			if bluedbEvalCond(c, rec) {
				return true
			}
		}
		return false
	case "not":
		if c, ok := node["c"].(map[string]any); ok {
			return !bluedbEvalCond(c, rec)
		}
		return true
	case "null":
		col, _ := node["col"].(string)
		neg, _ := node["neg"].(bool)
		v, has := bluedbRecGet(rec, col)
		isNull := !has || v == nil
		if neg {
			return isNull
		}
		return !isNull
	case "in":
		col, _ := node["col"].(string)
		rv, has := bluedbRecGet(rec, col)
		if !has {
			return false
		}
		for _, pv := range bluedbValList(node["vs"]) {
			if bluedbCmpVal(rv, pv) == 0 {
				return true
			}
		}
		return false
	case "op":
		col, _ := node["col"].(string)
		op, _ := node["op"].(string)
		pv, _ := node["v"].(map[string]any)
		rv, has := bluedbRecGet(rec, col)
		if !has {
			return op == "!="
		}
		if op == "like" {
			rs, ok := rv.(string)
			ps, _ := pv["s"].(string)
			return ok && bluedbLike(rs, ps)
		}
		c := bluedbCmpVal(rv, pv)
		if c == 2 {
			return op == "!="
		}
		switch op {
		case "=":
			return c == 0
		case "!=":
			return c != 0
		case ">":
			return c > 0
		case ">=":
			return c >= 0
		case "<":
			return c < 0
		case "<=":
			return c <= 0
		}
	}
	return false
}

// bluedbCmpOrderVal orders two raw record field values (from JSON: float64 /
// string / bool / nil). nil sorts first; numbers numerically; strings lexically.
func bluedbCmpOrderVal(a, b any) int {
	if a == nil && b == nil {
		return 0
	}
	if a == nil {
		return -1
	}
	if b == nil {
		return 1
	}
	switch av := a.(type) {
	case float64:
		if bv, ok := b.(float64); ok {
			switch {
			case av < bv:
				return -1
			case av > bv:
				return 1
			default:
				return 0
			}
		}
	case string:
		if bv, ok := b.(string); ok {
			return strings.Compare(av, bv)
		}
	case bool:
		if bv, ok := b.(bool); ok {
			if av == bv {
				return 0
			}
			if !av {
				return -1
			}
			return 1
		}
	}
	return 0
}

func bluedbLessByOrders(a, b map[string]any, orders []bluedbOrderTerm) bool {
	for _, o := range orders {
		av, _ := bluedbRecGet(a, o.Col)
		bv, _ := bluedbRecGet(b, o.Col)
		c := bluedbCmpOrderVal(av, bv)
		if c == 0 {
			continue
		}
		if strings.EqualFold(o.Dir, "desc") {
			return c > 0
		}
		return c < 0
	}
	return false
}

func bluedbRunQuery(db *bluedb.DB, coll, planJSON string, wantRows bool) (out []any, count int) {
	var plan bluedbQueryPlan
	_ = json.Unmarshal([]byte(planJSON), &plan)
	limit := plan.Limit
	if limit <= 0 || limit > bluedbQueryMaxRows {
		limit = bluedbQueryMaxRows
	}
	offset := plan.Offset
	if offset < 0 {
		offset = 0
	}
	prefix := bluedbCollRecordPrefix(coll)
	out = []any{}

	// No ORDER BY: stream in primary-key order with an early stop at limit.
	if len(plan.Orders) == 0 {
		skipped := 0
		db.Scan([]byte(prefix), nil, 0, func(k, v []byte) bool {
			var m map[string]any
			if json.Unmarshal(v, &m) != nil {
				return true
			}
			if !bluedbEvalCond(plan.Cond, m) {
				return true
			}
			if skipped < offset {
				skipped++
				return true
			}
			if wantRows {
				out = append(out, SkyTuple2{V0: string(k)[len(prefix):], V1: string(v)})
			}
			count++
			return count < limit
		})
		return out, count
	}

	// ORDER BY present: collect matches (capped), sort, then offset+limit.
	type qrow struct {
		pk  string
		raw string
		rec map[string]any
	}
	rows := []qrow{}
	db.Scan([]byte(prefix), nil, 0, func(k, v []byte) bool {
		var m map[string]any
		if json.Unmarshal(v, &m) != nil {
			return true
		}
		if !bluedbEvalCond(plan.Cond, m) {
			return true
		}
		rows = append(rows, qrow{pk: string(k)[len(prefix):], raw: string(v), rec: m})
		return len(rows) < bluedbQueryMaxRows
	})
	sort.SliceStable(rows, func(i, j int) bool {
		return bluedbLessByOrders(rows[i].rec, rows[j].rec, plan.Orders)
	})
	for i := offset; i < len(rows) && count < limit; i++ {
		if wantRows {
			out = append(out, SkyTuple2{V0: rows[i].pk, V1: rows[i].raw})
		}
		count++
	}
	return out, count
}

// BlueDB_collQuery : Int -> String(coll) -> String(planJson) -> Task Error (List (String,String))
func BlueDB_collQuery(idArg, collArg, planArg any) any {
	return func() any {
		db := bluedbLookup(idArg)
		if db == nil {
			return Err[any, any](ErrInvalidInput("BlueDB.collQuery: store not found (closed?)"))
		}
		out, _ := bluedbRunQuery(db, AsString(collArg), AsString(planArg), true)
		return Ok[any, any](out)
	}
}

// BlueDB_collQueryCount : Int -> String(coll) -> String(planJson) -> Task Error Int
func BlueDB_collQueryCount(idArg, collArg, planArg any) any {
	return func() any {
		db := bluedbLookup(idArg)
		if db == nil {
			return Err[any, any](ErrInvalidInput("BlueDB.collQueryCount: store not found (closed?)"))
		}
		_, count := bluedbRunQuery(db, AsString(collArg), AsString(planArg), false)
		return Ok[any, any](count)
	}
}

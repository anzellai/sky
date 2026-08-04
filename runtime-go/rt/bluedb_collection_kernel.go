// bluedb_collection_kernel.go — per-collection namespaced KV (P1).
//
// Std.Persist's KV backend keys records at a bare <pk>, and secondary indexes at
// \x00x\x00i\x00<field>… with no collection dimension — so two collections in one
// store share a keyspace (all/scan/count see the WHOLE store, and same-named
// indexes cross-contaminate). This file introduces a per-COLLECTION layout so one
// BlueDB store cleanly holds many isolated collections (like tables), which is
// also the foundation for KV-side unique/serial enforcement (P2–P4) and querying.
//
// Layout (all under the reserved \x00x\x00 space, hidden from raw keys/scan):
//   record:   \x00x\x00d\x00 <coll> \x00 <pk>              -> codec JSON
//   index:    \x00x\x00i\x00 <coll> \x00 <field> \x00 <E(v)>[pk]  (fixed int/bool)
//             \x00x\x00i\x00 <coll> \x00 <field> \x00 <value> \x00 <pk> (variable text)
//   manifest: \x00x\x00m\x00 <coll>                        -> {layout, fields}
// Records move under \x00x\x00d\x00<coll> so they're namespaced AND invisible to the
// raw string-KV surface (BlueDB.put/get/keys/scan) — the two layers never collide.
// Reuses the R1 order-preserving encoder + extract-pk + pk-stripe-lock + WriteBatch
// atomicity from bluedb_index_kernel.go.
package rt

import (
	"bytes"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"sky-app/bluedb"
)

// bluedbNow is the timestamp source for defaultNow/touchOnUpdate. It's a var so
// tests can override it for determinism. The text shape matches SQLite's
// datetime('now') so a record graduating KV→SQL reads back a comparable string.
var bluedbNow = func() string {
	return time.Now().UTC().Format("2006-01-02 15:04:05")
}

// bluedbIsZeroVal reports whether a JSON-decoded field value is absent or the zero
// value for its base column kind (the D2 "apply default when zero" rule).
func bluedbIsZeroVal(cur any, has bool, base string) bool {
	if !has || cur == nil {
		return true
	}
	switch base {
	case "int", "bigint", "real":
		f, ok := cur.(float64)
		return ok && f == 0
	case "bool":
		b, ok := cur.(bool)
		return ok && !b
	default: // text / blob
		s, ok := cur.(string)
		return ok && s == ""
	}
}

// bluedbNowValue renders "now" in the field's base type (text string, or epoch int
// for an int/bigint column).
func bluedbNowValue(base string) any {
	switch base {
	case "int", "bigint":
		return float64(time.Now().UTC().Unix())
	default:
		return bluedbNow()
	}
}

// bluedbDefaultValue renders a declared default (|dtext=/|dint=/|dbool=) as a typed
// JSON value.
func bluedbDefaultValue(defKind, defVal string) any {
	switch defKind {
	case "int":
		n, _ := strconv.ParseInt(defVal, 10, 64)
		return float64(n)
	case "bool":
		return defVal == "true"
	default:
		return defVal
	}
}

// bluedbInjectDefaults applies defaultNow / default* on insert (when the field is
// zero) and touchOnUpdate on update, mutating the decoded record map in place.
// `cols` are (columnName, kind-with-flags) — the same flag grammar the SQL side
// parses (codecColExtras / codecColIsTouch). Column names are snake_case (the
// codec's JSON keys), so a direct map lookup matches.
func bluedbInjectDefaults(m map[string]any, cols []bluedbFieldType, isInsert bool) {
	for _, c := range cols {
		name := c.field
		kind := c.colType
		base, _ := codecSplitKind(kind)
		if codecColIsTouch(kind) {
			if !isInsert {
				m[name] = bluedbNowValue(base)
				continue
			}
			// touch fields are also dnow → fall through to stamp on insert too.
		}
		if !isInsert {
			continue
		}
		_, defKind, defVal := codecColExtras(kind)
		cur, has := m[name]
		if !bluedbIsZeroVal(cur, has, base) {
			continue
		}
		switch defKind {
		case "now":
			m[name] = bluedbNowValue(base)
		case "text", "int", "bool":
			m[name] = bluedbDefaultValue(defKind, defVal)
		}
	}
}

// bluedbCollLayoutVersion — bump when the per-collection on-disk layout changes.
const bluedbCollLayoutVersion = 1

func bluedbCollRecordPrefix(coll string) string {
	return bluedbReserved + "d\x00" + coll + "\x00"
}

func bluedbCollRecordKey(coll, pk string) []byte {
	return []byte(bluedbCollRecordPrefix(coll) + pk)
}

// bluedbCollFieldPrefix = \x00x\x00i\x00<coll>\x00<field>\x00 (all index entries for a
// (collection, field)). The R1 extract-pk logic is unchanged — only the prefix grows.
func bluedbCollFieldPrefix(coll, field string) []byte {
	return []byte(bluedbReserved + "i\x00" + coll + "\x00" + field + "\x00")
}

func bluedbCollIndexKey(coll, field, value, colType, pk string) []byte {
	enc, fixed := bluedbEncodeIndexVal(value, colType)
	buf := bluedbCollFieldPrefix(coll, field)
	buf = append(buf, enc...)
	if !fixed {
		buf = append(buf, 0)
	}
	buf = append(buf, pk...)
	return buf
}

func bluedbCollEqPrefix(coll, field, value, colType string) []byte {
	enc, fixed := bluedbEncodeIndexVal(value, colType)
	buf := bluedbCollFieldPrefix(coll, field)
	buf = append(buf, enc...)
	if !fixed {
		buf = append(buf, 0)
	}
	return buf
}

func bluedbCollManifestKey(coll string) []byte {
	return []byte(bluedbReserved + "m\x00" + coll)
}

// bluedbCollSeqKey holds the per-collection serial (auto-increment) counter.
func bluedbCollSeqKey(coll string) []byte {
	return []byte(bluedbReserved + "s\x00" + coll)
}

func bluedbReadSeq(db *bluedb.DB, key []byte) int64 {
	if v, ok := db.Get(key); ok {
		n, _ := strconv.ParseInt(string(v), 10, 64)
		return n
	}
	return 0
}

// bluedbCollUniqueKey = \x00x\x00u\x00<coll>\x00<field>\x00<E(v)> → <owner-pk>. ONE
// entry per value (not per-pk), so a second pk claiming the same value collides.
func bluedbCollUniqueKey(coll, field, value, colType string) []byte {
	enc, _ := bluedbEncodeIndexVal(value, colType)
	buf := []byte(bluedbReserved + "u\x00" + coll + "\x00" + field + "\x00")
	return append(buf, enc...)
}

// bluedbUniqueLock serializes writers racing to claim ONE (coll,field,value) — the
// cross-pk uniqueness race (distinct from the per-pk lock). Held across the
// Get(uniqueKey)→check→WriteBatch so no two pks can both pass the check.
func bluedbUniqueLock(id int64, coll, field string, enc []byte) *sync.Mutex {
	return bluedbPkLock(id, "\x00u\x00"+coll+"\x00"+field+"\x00"+string(enc))
}

type bluedbUniqueSpec struct {
	field, colType, value string
	key                   []byte
	enc                   []byte
}

// bluedbCollManifest: per-collection layout + declared index fields.
type bluedbCollManifest struct {
	Layout int      `json:"layout"`
	Fields []string `json:"fields"`
}

func bluedbReadCollManifest(db *bluedb.DB, coll string) bluedbCollManifest {
	m := bluedbCollManifest{}
	if mb, ok := db.Get(bluedbCollManifestKey(coll)); ok {
		_ = json.Unmarshal(mb, &m)
	}
	return m
}

// The per-pk stripe lock is keyed by (collection, pk) via bluedbPkLock(id,
// coll+"\x00"+pk) so the same pk in different collections doesn't share a lock.

func bluedbCollNULCheck(coll, pk string) any {
	if strings.ContainsRune(coll, 0) || strings.ContainsRune(pk, 0) {
		return ErrInvalidInput("BlueDB: collection name / key must not contain NUL")
	}
	return nil
}

// BlueDB_collPut : Int -> String(coll) -> String(pk) -> String(json)
//   -> List (String,String,String)(field,value,colType)
//   -> List (String,String)(col,kindWithFlags)
//   -> Task Error String   (the STORED json — id/timestamps filled)
// Namespaced upsert with defaultNow/touchOnUpdate/default* enforcement (P2):
// detects insert-vs-update, injects defaults into the record, re-derives index
// values from the injected record (so an indexed defaulted field stays
// consistent), and writes record + indexes in ONE atomic WriteBatch under the
// (coll,pk) stripe lock. Returns the stored JSON so Persist.insert can decode
// back the generated fields.
func BlueDB_collPut(idArg, collArg, pkArg, jsonArg, fvtArg, colsArg any) any {
	return func() any {
		db := bluedbLookup(idArg)
		if db == nil {
			return Err[any, any](ErrInvalidInput("BlueDB.collPut: store not found (closed?)"))
		}
		id := int64(AsInt(idArg))
		coll := AsString(collArg)
		pk := AsString(pkArg)
		fvts := bluedbParseFVT(fvtArg)
		cols := bluedbParseFT(colsArg) // (colName, kind-with-flags)

		var m map[string]any
		if err := json.Unmarshal([]byte(AsString(jsonArg)), &m); err != nil {
			return Err[any, any](ErrInvalidInput("BlueDB.collPut: record is not JSON"))
		}

		// Serial PK: a "!" auto-increment column with an unset pk gets the next
		// per-collection sequence value. The seq lock (outermost) serializes serial
		// inserts; the counter bump rides the record's WriteBatch (crash-safe — a
		// crash leaves neither the record nor the bump, so no gap / double-assign).
		serialCol := ""
		for _, c := range cols {
			if codecColIsAutoInc(c.colType) {
				serialCol = c.field
				break
			}
		}
		seqKey := bluedbCollSeqKey(coll)
		assignSerial := serialCol != "" && (pk == "" || pk == "0")
		var newSeq int64
		if assignSerial {
			seqLk := bluedbPkLock(id, "\x00seq\x00"+coll)
			seqLk.Lock()
			defer seqLk.Unlock()
			newSeq = bluedbReadSeq(db, seqKey) + 1
			pk = strconv.FormatInt(newSeq, 10)
			m[serialCol] = float64(newSeq)
		}
		if e := bluedbCollNULCheck(coll, pk); e != nil {
			return Err[any, any](e)
		}

		// Unique constraints: derive the (field, value) each declared |u column
		// claims from the record. NULL/absent skips (SQL allows multiple NULLs).
		// Lock order = seq (already held) → per-(field,value) locks in sorted key
		// order (deadlock-free even with multiple unique cols) → pk lock.
		var uniques []bluedbUniqueSpec
		for _, c := range cols {
			uniq, _, _ := codecColExtras(c.colType)
			if !uniq {
				continue
			}
			rv, has := bluedbOldIndexVal(m, c.field)
			if !has {
				continue
			}
			base, _ := codecSplitKind(c.colType)
			enc, _ := bluedbEncodeIndexVal(rv, base)
			uniques = append(uniques, bluedbUniqueSpec{
				field: c.field, colType: base, value: rv,
				key: bluedbCollUniqueKey(coll, c.field, rv, base), enc: enc,
			})
		}
		sort.Slice(uniques, func(i, j int) bool {
			return bytes.Compare(uniques[i].key, uniques[j].key) < 0
		})
		for _, u := range uniques {
			ul := bluedbUniqueLock(id, coll, u.field, u.enc)
			ul.Lock()
			defer ul.Unlock()
		}

		lk := bluedbPkLock(id, coll+"\x00"+pk)
		lk.Lock()
		defer lk.Unlock()

		recKey := bluedbCollRecordKey(coll, pk)
		old, isUpdate := db.Get(recKey)

		// Enforce uniqueness under the held value locks: a value owned by a
		// DIFFERENT pk is a conflict (self-upsert with owner==pk is fine).
		for _, u := range uniques {
			if owner, ok := db.Get(u.key); ok && string(owner) != pk {
				return Err[any, any](ErrInvalidInput(
					"BlueDB.collPut: unique constraint \"" + u.field + "\"=\"" + u.value +
						"\" already held by \"" + string(owner) + "\""))
			}
		}

		bluedbInjectDefaults(m, cols, !isUpdate)

		// Re-derive each index value from the (possibly defaulted) record so an
		// indexed default/touch field indexes its actual stored value.
		type fvt struct{ field, value, colType string }
		derived := make([]fvt, 0, len(fvts))
		for _, f := range fvts {
			v := f.value
			if rv, has := bluedbOldIndexVal(m, f.field); has {
				v = rv
			}
			if bluedbFixedWidth(f.colType) == 0 && strings.ContainsRune(v, 0) {
				return Err[any, any](ErrInvalidInput("BlueDB: indexed text value must not contain NUL"))
			}
			derived = append(derived, fvt{f.field, v, f.colType})
		}

		stored, err := json.Marshal(m)
		if err != nil {
			return Err[any, any](ErrFfi("BlueDB.collPut: re-encode: " + err.Error()))
		}

		b := bluedb.NewBatch()
		if isUpdate {
			var oldM map[string]any
			if json.Unmarshal(old, &oldM) == nil {
				for _, f := range derived {
					if oldVal, has := bluedbOldIndexVal(oldM, f.field); has && oldVal != f.value {
						b.Delete(bluedbCollIndexKey(coll, f.field, oldVal, f.colType, pk))
					}
				}
				for _, u := range uniques {
					if oldVal, has := bluedbOldIndexVal(oldM, u.field); has && oldVal != u.value {
						b.Delete(bluedbCollUniqueKey(coll, u.field, oldVal, u.colType))
					}
				}
			}
		}
		for _, f := range derived {
			b.Put(bluedbCollIndexKey(coll, f.field, f.value, f.colType, pk), nil)
		}
		for _, u := range uniques {
			b.Put(u.key, []byte(pk))
		}
		b.Put(recKey, stored)
		if assignSerial {
			b.Put(seqKey, []byte(strconv.FormatInt(newSeq, 10)))
		}
		if err := db.WriteBatch(b); err != nil {
			return Err[any, any](ErrFfi("BlueDB.collPut: " + err.Error()))
		}
		return Ok[any, any](string(stored))
	}
}

// BlueDB_collGet : Int -> String(coll) -> String(pk) -> Task Error (Maybe String)
func BlueDB_collGet(idArg, collArg, pkArg any) any {
	return func() any {
		db := bluedbLookup(idArg)
		if db == nil {
			return Err[any, any](ErrInvalidInput("BlueDB.collGet: store not found (closed?)"))
		}
		v, ok := db.Get(bluedbCollRecordKey(AsString(collArg), AsString(pkArg)))
		if !ok {
			return Ok[any, any](makeMaybeNothing())
		}
		return Ok[any, any](makeMaybeJust(string(v)))
	}
}

// BlueDB_collDelete : Int -> String(coll) -> String(pk) -> List (String,String)(idx fieldTypes)
//   -> List (String,String)(cols) -> Task Error ()
// Removes the record + its secondary index AND unique-index entries.
func BlueDB_collDelete(idArg, collArg, pkArg, ftArg, colsArg any) any {
	return func() any {
		db := bluedbLookup(idArg)
		if db == nil {
			return Err[any, any](ErrInvalidInput("BlueDB.collDelete: store not found (closed?)"))
		}
		id := int64(AsInt(idArg))
		coll := AsString(collArg)
		pk := AsString(pkArg)
		fts := bluedbParseFT(ftArg)
		cols := bluedbParseFT(colsArg)
		lk := bluedbPkLock(id, coll+"\x00"+pk)
		lk.Lock()
		defer lk.Unlock()

		recKey := bluedbCollRecordKey(coll, pk)
		old, ok := db.Get(recKey)
		if !ok {
			return Ok[any, any](nil)
		}
		b := bluedb.NewBatch()
		var m map[string]any
		if err := json.Unmarshal(old, &m); err == nil {
			for _, ft := range fts {
				if v, has := bluedbOldIndexVal(m, ft.field); has {
					b.Delete(bluedbCollIndexKey(coll, ft.field, v, ft.colType, pk))
				}
			}
			for _, c := range cols {
				if uniq, _, _ := codecColExtras(c.colType); uniq {
					if v, has := bluedbOldIndexVal(m, c.field); has {
						base, _ := codecSplitKind(c.colType)
						b.Delete(bluedbCollUniqueKey(coll, c.field, v, base))
					}
				}
			}
		}
		b.Delete(recKey)
		if err := db.WriteBatch(b); err != nil {
			return Err[any, any](ErrFfi("BlueDB.collDelete: " + err.Error()))
		}
		return Ok[any, any](nil)
	}
}

// BlueDB_collAll : Int -> String(coll) -> Int(limit) -> Task Error (List (String,String))
// Returns (pk, json) tuples for every record in the collection (bounded by limit).
func BlueDB_collAll(idArg, collArg, limitArg any) any {
	return func() any {
		db := bluedbLookup(idArg)
		if db == nil {
			return Err[any, any](ErrInvalidInput("BlueDB.collAll: store not found (closed?)"))
		}
		coll := AsString(collArg)
		prefix := bluedbCollRecordPrefix(coll)
		limit := AsInt(limitArg)
		out := []any{}
		db.Scan([]byte(prefix), nil, limit, func(k, v []byte) bool {
			out = append(out, SkyTuple2{V0: string(k)[len(prefix):], V1: string(v)})
			return true
		})
		return Ok[any, any](out)
	}
}

// BlueDB_collCount : Int -> String(coll) -> Task Error Int
func BlueDB_collCount(idArg, collArg any) any {
	return func() any {
		db := bluedbLookup(idArg)
		if db == nil {
			return Err[any, any](ErrInvalidInput("BlueDB.collCount: store not found (closed?)"))
		}
		n := 0
		db.Scan([]byte(bluedbCollRecordPrefix(AsString(collArg))), nil, 0, func(_, _ []byte) bool {
			n++
			return true
		})
		return Ok[any, any](n)
	}
}

// BlueDB_collFindByIndex : Int -> String(coll) -> String(field) -> String(value) -> String(colType)
//   -> Task Error (List String)
func BlueDB_collFindByIndex(idArg, collArg, fieldArg, valueArg, colTypeArg any) any {
	return func() any {
		db := bluedbLookup(idArg)
		if db == nil {
			return Err[any, any](ErrInvalidInput("BlueDB.collFindByIndex: store not found (closed?)"))
		}
		coll := AsString(collArg)
		field := AsString(fieldArg)
		colType := AsString(colTypeArg)
		prefix := bluedbCollEqPrefix(coll, field, AsString(valueArg), colType)
		fpLen := len(bluedbCollFieldPrefix(coll, field))
		width := bluedbFixedWidth(colType)
		pks := []any{}
		db.Scan(prefix, nil, 0, func(k, _ []byte) bool {
			pks = append(pks, bluedbExtractPk(k, fpLen, width))
			return true
		})
		return Ok[any, any](pks)
	}
}

// BlueDB_collCountByIndex : Int -> String(coll) -> String(field) -> String(value) -> String(colType) -> Task Error Int
func BlueDB_collCountByIndex(idArg, collArg, fieldArg, valueArg, colTypeArg any) any {
	return func() any {
		db := bluedbLookup(idArg)
		if db == nil {
			return Err[any, any](ErrInvalidInput("BlueDB.collCountByIndex: store not found (closed?)"))
		}
		prefix := bluedbCollEqPrefix(AsString(collArg), AsString(fieldArg), AsString(valueArg), AsString(colTypeArg))
		n := 0
		db.Scan(prefix, nil, 0, func(_, _ []byte) bool {
			n++
			return true
		})
		return Ok[any, any](n)
	}
}

// BlueDB_collFindByIndexRange : Int -> String(coll) -> String(field) -> String(colType)
//   -> Bool(hasLo) -> String(lo) -> Bool(hasHi) -> String(hi) -> Task Error (List String)
func BlueDB_collFindByIndexRange(idArg, collArg, fieldArg, colTypeArg, hasLoArg, loArg, hasHiArg, hiArg any) any {
	return func() any {
		db := bluedbLookup(idArg)
		if db == nil {
			return Err[any, any](ErrInvalidInput("BlueDB.collFindByIndexRange: store not found (closed?)"))
		}
		coll := AsString(collArg)
		field := AsString(fieldArg)
		colType := AsString(colTypeArg)
		if !bluedbRangeable(colType) {
			return Err[any, any](ErrInvalidInput(
				"BlueDB.collFindByIndexRange: field \"" + field + "\" of type " + colType +
					" has no order-preserving KV range (use the SQL backend)"))
		}
		pks := []any{}
		bluedbCollRangeScan(db, coll, field, colType,
			AsBool(hasLoArg), AsString(loArg), AsBool(hasHiArg), AsString(hiArg),
			func(pk string) { pks = append(pks, pk) })
		return Ok[any, any](pks)
	}
}

// BlueDB_collCountByIndexRange : same args as findRange -> Task Error Int
func BlueDB_collCountByIndexRange(idArg, collArg, fieldArg, colTypeArg, hasLoArg, loArg, hasHiArg, hiArg any) any {
	return func() any {
		db := bluedbLookup(idArg)
		if db == nil {
			return Err[any, any](ErrInvalidInput("BlueDB.collCountByIndexRange: store not found (closed?)"))
		}
		coll := AsString(collArg)
		field := AsString(fieldArg)
		colType := AsString(colTypeArg)
		if !bluedbRangeable(colType) {
			return Err[any, any](ErrInvalidInput(
				"BlueDB.collCountByIndexRange: field \"" + field + "\" of type " + colType + " not rangeable"))
		}
		n := 0
		bluedbCollRangeScan(db, coll, field, colType,
			AsBool(hasLoArg), AsString(loArg), AsBool(hasHiArg), AsString(hiArg),
			func(_ string) { n++ })
		return Ok[any, any](n)
	}
}

func bluedbCollRangeScan(db *bluedb.DB, coll, field, colType string, hasLo bool, lo string, hasHi bool, hi string, emit func(string)) {
	fp := bluedbCollFieldPrefix(coll, field)
	fpLen := len(fp)
	width := bluedbFixedWidth(colType)
	var startAfter []byte
	if hasLo {
		enc, _ := bluedbEncodeIndexVal(lo, colType)
		startAfter = append(append([]byte(nil), fp...), enc...)
	}
	var ceiling []byte
	if hasHi {
		enc, _ := bluedbEncodeIndexVal(hi, colType)
		ceiling = append(append([]byte(nil), fp...), enc...)
	}
	db.Scan(fp, startAfter, 0, func(k, _ []byte) bool {
		if ceiling != nil && bytes.Compare(k, ceiling) >= 0 {
			return false
		}
		emit(bluedbExtractPk(k, fpLen, width))
		return true
	})
}

// BlueDB_collReindex : Int -> String(coll) -> List (String,String)(field,colType) -> Task Error Int
// Startup migration + index (re)build for a collection. If the per-collection
// manifest is already at CURRENT layout with the same field set → O(1) skip. A
// LEGACY store (layout 0: bare-pk records + un-namespaced indexes) is relaid out:
// bare-pk records move under \x00x\x00d\x00<coll>\x00; this collection's namespaced
// index space is swept + rebuilt; the manifest (layout=1) is written LAST so a
// crash mid-migration re-runs cleanly. Refuses a newer layout.
func BlueDB_collReindex(idArg, collArg, ftArg any) any {
	return func() any {
		db := bluedbLookup(idArg)
		if db == nil {
			return Err[any, any](ErrInvalidInput("BlueDB.collReindex: store not found (closed?)"))
		}
		id := int64(AsInt(idArg))
		coll := AsString(collArg)
		fts := bluedbParseFT(ftArg)
		declared := make([]string, 0, len(fts))
		colTypeOf := map[string]string{}
		declaredSet := map[string]bool{}
		for _, ft := range fts {
			declared = append(declared, ft.field)
			colTypeOf[ft.field] = ft.colType
			declaredSet[ft.field] = true
		}

		manifest := bluedbReadCollManifest(db, coll)
		if manifest.Layout > bluedbCollLayoutVersion {
			return Err[any, any](ErrInvalidInput(
				"BlueDB.collReindex: store layout is newer than this build — upgrade Sky"))
		}
		manifestSet := map[string]bool{}
		for _, f := range manifest.Fields {
			manifestSet[f] = true
		}
		if manifest.Layout == bluedbCollLayoutVersion && sameStringSet(declaredSet, manifestSet) {
			return Ok[any, any](0) // steady state
		}

		// Legacy relayout: move bare-pk records into the collection namespace.
		if manifest.Layout < 1 {
			var bareKeys [][]byte
			db.ForEach(func(k, _ []byte) bool {
				if !bluedbIsReserved(string(k)) {
					bareKeys = append(bareKeys, append([]byte(nil), k...))
				}
				return true
			})
			for _, bk := range bareKeys {
				if v, ok := db.Get(bk); ok {
					b := bluedb.NewBatch()
					b.Put(bluedbCollRecordKey(coll, string(bk)), append([]byte(nil), v...))
					b.Delete(bk)
					_ = db.WriteBatch(b)
				}
			}
		}

		// Rebuild THIS collection's index space (sweep \x00x\x00i\x00<coll>\x00, then
		// re-derive from the namespaced records). Scoped to the collection, so it
		// never touches another collection's indexes.
		if e := bluedbSweepPrefix(db, bluedbReserved+"i\x00"+coll+"\x00"); e != nil {
			return Err[any, any](e)
		}
		count := 0
		recPrefix := bluedbCollRecordPrefix(coll)
		var pks []string
		db.Scan([]byte(recPrefix), nil, 0, func(k, _ []byte) bool {
			pks = append(pks, string(k)[len(recPrefix):])
			return true
		})
		for _, pk := range pks {
			lk := bluedbPkLock(id, coll+"\x00"+pk)
			lk.Lock()
			if cur, ok := db.Get(bluedbCollRecordKey(coll, pk)); ok {
				var m map[string]any
				if json.Unmarshal(cur, &m) == nil {
					b := bluedb.NewBatch()
					wrote := false
					for _, f := range declared {
						if v, has := bluedbOldIndexVal(m, f); has {
							b.Put(bluedbCollIndexKey(coll, f, v, colTypeOf[f], pk), nil)
							wrote = true
						}
					}
					if wrote {
						_ = db.WriteBatch(b)
						count++
					}
				}
			}
			lk.Unlock()
		}

		mb, _ := json.Marshal(bluedbCollManifest{Layout: bluedbCollLayoutVersion, Fields: declared})
		if err := db.Put(bluedbCollManifestKey(coll), mb); err != nil {
			return Err[any, any](ErrFfi("BlueDB.collReindex: manifest write: " + err.Error()))
		}
		return Ok[any, any](count)
	}
}

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
	"strings"

	"sky-app/bluedb"
)

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
//   -> List (String,String,String)(field,value,colType) -> Task Error ()
// Namespaced upsert: writes the record + maintains its secondary index entries in
// ONE atomic WriteBatch under the (coll,pk) stripe lock. (P1: no default/serial/
// unique enforcement yet — layered in P2–P4.)
func BlueDB_collPut(idArg, collArg, pkArg, jsonArg, fvtArg any) any {
	return func() any {
		db := bluedbLookup(idArg)
		if db == nil {
			return Err[any, any](ErrInvalidInput("BlueDB.collPut: store not found (closed?)"))
		}
		id := int64(AsInt(idArg))
		coll := AsString(collArg)
		pk := AsString(pkArg)
		if e := bluedbCollNULCheck(coll, pk); e != nil {
			return Err[any, any](e)
		}
		recJSON := AsString(jsonArg)
		fvts := bluedbParseFVT(fvtArg)
		for _, fv := range fvts {
			if bluedbFixedWidth(fv.colType) == 0 && strings.ContainsRune(fv.value, 0) {
				return Err[any, any](ErrInvalidInput("BlueDB: indexed text value must not contain NUL"))
			}
		}
		lk := bluedbPkLock(id, coll+"\x00"+pk)
		lk.Lock()
		defer lk.Unlock()

		b := bluedb.NewBatch()
		recKey := bluedbCollRecordKey(coll, pk)
		if old, ok := db.Get(recKey); ok {
			var m map[string]any
			if err := json.Unmarshal(old, &m); err == nil {
				for _, fv := range fvts {
					if oldVal, has := bluedbOldIndexVal(m, fv.field); has && oldVal != fv.value {
						b.Delete(bluedbCollIndexKey(coll, fv.field, oldVal, fv.colType, pk))
					}
				}
			}
		}
		for _, fv := range fvts {
			b.Put(bluedbCollIndexKey(coll, fv.field, fv.value, fv.colType, pk), nil)
		}
		b.Put(recKey, []byte(recJSON))
		if err := db.WriteBatch(b); err != nil {
			return Err[any, any](ErrFfi("BlueDB.collPut: " + err.Error()))
		}
		return Ok[any, any](nil)
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

// BlueDB_collDelete : Int -> String(coll) -> String(pk) -> List (String,String) -> Task Error ()
func BlueDB_collDelete(idArg, collArg, pkArg, ftArg any) any {
	return func() any {
		db := bluedbLookup(idArg)
		if db == nil {
			return Err[any, any](ErrInvalidInput("BlueDB.collDelete: store not found (closed?)"))
		}
		id := int64(AsInt(idArg))
		coll := AsString(collArg)
		pk := AsString(pkArg)
		fts := bluedbParseFT(ftArg)
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

// bluedb_index_kernel.go — BlueDB secondary indexes (E2), built on WriteBatch.
//
// Design (grill-approved):
//   - OPT-IN: a collection with no declared index uses the plain putValue path,
//     byte-identical to before (no lock, no reserved keys). This file only runs
//     when a Persist collection declares indexes.
//   - Index entry: \x00x\x00i\x00<field>\x00<value>\x00<pk> -> ""  (NON-unique,
//     pk-in-key). Different pks with the same value → distinct keys → no cross-key
//     race, so a single per-pk lock is sufficient (no value lock).
//   - Maintained atomically: put = ONE WriteBatch [Delete stale idx, Put new idx,
//     Put pk]. E1 gives crash-consistency (all-or-none). Index + primary never
//     diverge across a crash.
//   - TOCTOU: the read-old Get is NOT ordered against the committer, so concurrent
//     same-pk updates would orphan a stale index entry. A per-pk striped mutex is
//     held across Get(old)→WriteBatch so same-pk RMW serializes (different pks run
//     in parallel, batched by group commit).
//   - Reserved \x00 keyspace is hidden from public keys/scan/all/count; public put
//     rejects NUL keys, so the reserved space is the runtime's alone.
package rt

import (
	"encoding/json"
	"hash/fnv"
	"strconv"
	"strings"
	"sync"

	"sky-app/bluedb"
)

const bluedbReserved = "\x00x\x00" // reserved-key prefix (index entries + manifest)

var bluedbIndexManifestKey = []byte(bluedbReserved + "meta\x00indexes")

func bluedbIsReserved(key string) bool { return strings.HasPrefix(key, bluedbReserved) }

func bluedbIndexKey(field, value, pk string) []byte {
	return []byte(bluedbReserved + "i\x00" + field + "\x00" + value + "\x00" + pk)
}

func bluedbIndexPrefix(field, value string) string {
	return bluedbReserved + "i\x00" + field + "\x00" + value + "\x00"
}

const bluedbIndexStripes = 256

var bluedbIndexLocks [bluedbIndexStripes]sync.Mutex

func bluedbPkLock(id int64, pk string) *sync.Mutex {
	h := fnv.New32a()
	var b [8]byte
	for i := 0; i < 8; i++ {
		b[i] = byte(uint64(id) >> (uint(i) * 8))
	}
	h.Write(b[:])
	h.Write([]byte(pk))
	return &bluedbIndexLocks[h.Sum32()%bluedbIndexStripes]
}

type bluedbFieldVal struct{ field, value string }

func bluedbParseFieldVals(arg any) []bluedbFieldVal {
	out := []bluedbFieldVal{}
	for _, it := range asList(arg) {
		// A Sky (String,String) may lower to SkyTuple2 OR T2[string,string]; read
		// V0/V1 reflectively so both work.
		v0 := Field(it, "V0")
		v1 := Field(it, "V1")
		if v0 != nil && v1 != nil {
			out = append(out, bluedbFieldVal{field: AsString(v0), value: AsString(v1)})
		}
	}
	return out
}

// bluedbRenderIndexVal renders a JSON-decoded scalar to the SAME string
// Persist_keyString produces, so an OLD value read from the record's JSON matches
// the index key that was written from the NEW value at put time.
func bluedbRenderIndexVal(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case float64:
		if x == float64(int64(x)) { // JSON numbers decode to float64; integer PK/index → int string
			return strconv.FormatInt(int64(x), 10), true
		}
		return "", false // non-integer float is not an indexable key (keyString rejects it too)
	case bool:
		if x {
			return "true", true
		}
		return "false", true
	default:
		return "", false
	}
}

func bluedbOldIndexVal(m map[string]any, field string) (string, bool) {
	if v, ok := m[field]; ok {
		return bluedbRenderIndexVal(v)
	}
	if v, ok := m[camelToSnake(field)]; ok { // codec stores fields snake_cased
		return bluedbRenderIndexVal(v)
	}
	return "", false
}

func bluedbFieldNames(arg any) []string {
	out := []string{}
	for _, it := range asList(arg) {
		out = append(out, AsString(it))
	}
	return out
}

// BlueDB_putIndexed : Int -> String -> String -> List (String,String) -> Task Error ()
// pk, recordJson, [(indexField, newValue)]. Maintains all index entries + the
// primary in ONE atomic WriteBatch under the pk lock.
func BlueDB_putIndexed(idArg, pkArg, jsonArg, fvArg any) any {
	return func() any {
		db := bluedbLookup(idArg)
		if db == nil {
			return Err[any, any](ErrInvalidInput("BlueDB.putIndexed: store not found (closed?)"))
		}
		id := int64(AsInt(idArg))
		pk := AsString(pkArg)
		if strings.ContainsRune(pk, 0) {
			return Err[any, any](ErrInvalidInput("BlueDB: key must not contain NUL"))
		}
		recordJSON := AsString(jsonArg)
		fvs := bluedbParseFieldVals(fvArg)
		for _, fv := range fvs {
			if strings.ContainsRune(fv.value, 0) {
				return Err[any, any](ErrInvalidInput("BlueDB: indexed value must not contain NUL"))
			}
		}
		lk := bluedbPkLock(id, pk)
		lk.Lock()
		defer lk.Unlock()

		b := bluedb.NewBatch()
		if old, ok := db.Get([]byte(pk)); ok {
			var m map[string]any
			if err := json.Unmarshal(old, &m); err != nil {
				return Err[any, any](ErrInvalidInput(
					"BlueDB.putIndexed: existing record at \"" + pk + "\" is not JSON (corrupt); index not maintained"))
			}
			for _, fv := range fvs {
				if oldVal, has := bluedbOldIndexVal(m, fv.field); has && oldVal != fv.value {
					b.Delete(bluedbIndexKey(fv.field, oldVal, pk))
				}
			}
		}
		for _, fv := range fvs {
			b.Put(bluedbIndexKey(fv.field, fv.value, pk), nil)
		}
		b.Put([]byte(pk), []byte(recordJSON))
		if err := db.WriteBatch(b); err != nil {
			return Err[any, any](ErrFfi("BlueDB.putIndexed: " + err.Error()))
		}
		return Ok[any, any](nil)
	}
}

// BlueDB_deleteIndexed : Int -> String -> List String -> Task Error ()
// pk, [indexField]. Removes the record and all its index entries in one batch.
func BlueDB_deleteIndexed(idArg, pkArg, fieldsArg any) any {
	return func() any {
		db := bluedbLookup(idArg)
		if db == nil {
			return Err[any, any](ErrInvalidInput("BlueDB.deleteIndexed: store not found (closed?)"))
		}
		id := int64(AsInt(idArg))
		pk := AsString(pkArg)
		fields := bluedbFieldNames(fieldsArg)
		lk := bluedbPkLock(id, pk)
		lk.Lock()
		defer lk.Unlock()

		old, ok := db.Get([]byte(pk))
		if !ok {
			return Ok[any, any](nil) // nothing to delete
		}
		b := bluedb.NewBatch()
		var m map[string]any
		if err := json.Unmarshal(old, &m); err == nil {
			for _, f := range fields {
				if v, has := bluedbOldIndexVal(m, f); has {
					b.Delete(bluedbIndexKey(f, v, pk))
				}
			}
		} // if corrupt, still delete the pk (best effort; index sweep via reindex)
		b.Delete([]byte(pk))
		if err := db.WriteBatch(b); err != nil {
			return Err[any, any](ErrFfi("BlueDB.deleteIndexed: " + err.Error()))
		}
		return Ok[any, any](nil)
	}
}

// BlueDB_findByIndex : Int -> String -> String -> Task Error (List String)
// Returns the pks whose <field> == <value> (prefix-scan of the index keyspace).
func BlueDB_findByIndex(idArg, fieldArg, valueArg any) any {
	return func() any {
		db := bluedbLookup(idArg)
		if db == nil {
			return Err[any, any](ErrInvalidInput("BlueDB.findByIndex: store not found (closed?)"))
		}
		prefix := bluedbIndexPrefix(AsString(fieldArg), AsString(valueArg))
		pks := []any{}
		db.Scan([]byte(prefix), nil, 0, func(k, _ []byte) bool {
			// key = prefix + pk; the pk is the tail after the last NUL.
			pks = append(pks, string(k)[len(prefix):])
			return true
		})
		return Ok[any, any](pks)
	}
}

// BlueDB_reindex : Int -> List String -> Task Error Int
// Idempotent backfill: if the declared field set equals the manifest, skip (O(1));
// else backfill added fields over all primaries (through the pk lock, so it's safe
// against concurrent writes), sweep dropped fields, and update the manifest.
func BlueDB_reindex(idArg, fieldsArg any) any {
	return func() any {
		db := bluedbLookup(idArg)
		if db == nil {
			return Err[any, any](ErrInvalidInput("BlueDB.reindex: store not found (closed?)"))
		}
		id := int64(AsInt(idArg))
		declared := bluedbFieldNames(fieldsArg)
		declaredSet := map[string]bool{}
		for _, f := range declared {
			declaredSet[f] = true
		}

		var manifest []string
		if mb, ok := db.Get(bluedbIndexManifestKey); ok {
			_ = json.Unmarshal(mb, &manifest)
		}
		manifestSet := map[string]bool{}
		for _, f := range manifest {
			manifestSet[f] = true
		}
		if sameStringSet(declaredSet, manifestSet) {
			return Ok[any, any](0) // steady state — no O(n) work
		}

		// Fields to backfill (declared but not yet in manifest).
		var added []string
		for _, f := range declared {
			if !manifestSet[f] {
				added = append(added, f)
			}
		}
		count := 0
		if len(added) > 0 {
			// Snapshot the primary pks (non-reserved keys) first.
			var pks []string
			db.ForEach(func(k, _ []byte) bool {
				if !bluedbIsReserved(string(k)) {
					pks = append(pks, string(k))
				}
				return true
			})
			for _, pk := range pks {
				lk := bluedbPkLock(id, pk)
				lk.Lock()
				cur, ok := db.Get([]byte(pk)) // current value under the lock
				if ok {
					var m map[string]any
					if json.Unmarshal(cur, &m) == nil {
						b := bluedb.NewBatch()
						wrote := false
						for _, f := range added {
							if v, has := bluedbOldIndexVal(m, f); has {
								b.Put(bluedbIndexKey(f, v, pk), nil)
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
		}
		// Sweep entries of fields that were dropped from the declaration.
		for _, f := range manifest {
			if !declaredSet[f] {
				pfx := bluedbReserved + "i\x00" + f + "\x00"
				var stale [][]byte
				db.Scan([]byte(pfx), nil, 0, func(k, _ []byte) bool {
					stale = append(stale, append([]byte(nil), k...))
					return true
				})
				for _, k := range stale {
					_ = db.Delete(k)
				}
			}
		}
		// Persist the new manifest.
		mb, _ := json.Marshal(declared)
		if err := db.Put(bluedbIndexManifestKey, mb); err != nil {
			return Err[any, any](ErrFfi("BlueDB.reindex: manifest write: " + err.Error()))
		}
		return Ok[any, any](count)
	}
}

func sameStringSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

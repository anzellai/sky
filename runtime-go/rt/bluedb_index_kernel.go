// bluedb_index_kernel.go — BlueDB secondary indexes (E2/E3) + ordered range (R1).
//
// Design (grill-approved):
//   - OPT-IN: a collection with no declared index uses the plain putValue path,
//     byte-identical to before (no lock, no reserved keys).
//   - Index entry, TYPE-DIRECTED key format (R1 order-preserving):
//       fixed-width value (int/bool):  \x00x\x00i\x00<field>\x00<E(v)><pk>   (no delim)
//       variable value   (text):       \x00x\x00i\x00<field>\x00<value>\x00<pk>
//     E is ORDER-PRESERVING so memcmp(E(a),E(b)) == semantic compare(a,b):
//       int  -> big-endian of uint64(v) ^ 0x8000…  (8 bytes, sign-biased)
//       bool -> 0x00 / 0x01                          (1 byte)
//       text -> UTF-8 as-is (byte order == code-point order); NUL-terminated
//     This makes findByIndexRange CORRECT for all indexed types (a LEXICAL range
//     over decimal-string ints, e.g. "100"<"18"<"5", was silently wrong — E3
//     deferred it for exactly this reason).
//   - The field TYPE comes from the codec `shape` (uniform per field), NOT per-value
//     JSON inference (which can't tell int from real and corrupts a field's keyspace
//     with one stray record). Persist resolves it and passes a colType tag per field.
//   - Atomic maintenance: put = ONE WriteBatch [Delete stale idx, Put new idx, Put
//     pk]. E1 (WriteBatch) gives crash-consistency (all-or-none).
//   - TOCTOU: a per-pk striped mutex is held across Get(old)→WriteBatch so same-pk
//     RMW serializes (different pks run in parallel).
//   - Format migration: the manifest carries {version, fields}. reindex does a FULL
//     re-encode when version < CURRENT (existing entries were raw-string v1); it
//     flips the version LAST (crash → stays v1 → next reindex redoes it). Run reindex
//     once at startup before serving (the documented contract).
//   - Reserved \x00 keyspace is hidden from public keys/scan/all/count; public put
//     rejects NUL keys. NUL is rejected in TEXT values + pk (so the \x00 delimiter /
//     pk-tail stay unambiguous); int/bool encode to binary that may contain NUL.
package rt

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"hash/fnv"
	"strconv"
	"strings"
	"sync"

	"sky-app/bluedb"
)

const bluedbReserved = "\x00x\x00" // reserved-key prefix (index entries + manifest)

// bluedbIndexFormatVersion — bump when the on-disk index key encoding changes.
// v1 = raw-string values (E2/E3); v2 = order-preserving type-directed (R1).
const bluedbIndexFormatVersion = 2

var bluedbIndexManifestKey = []byte(bluedbReserved + "meta\x00indexes")

func bluedbIsReserved(key string) bool { return strings.HasPrefix(key, bluedbReserved) }

// ── Order-preserving value encoding ──────────────────────────────────────────

// bluedbColClean strips a nullable "?" suffix (CNull inner → "int?" etc).
func bluedbColClean(colType string) string { return strings.TrimSuffix(colType, "?") }

// bluedbFixedWidth returns the fixed byte width for a fixed-width colType, else 0.
func bluedbFixedWidth(colType string) int {
	switch bluedbColClean(colType) {
	case "int":
		return 8
	case "bool":
		return 1
	default:
		return 0 // variable-width (text) or non-fixed
	}
}

// bluedbRangeable reports whether a colType has a correct order-preserving range.
// int/bool/text yes; real/blob/money no (SQL covers typed decimal/float range).
func bluedbRangeable(colType string) bool {
	switch bluedbColClean(colType) {
	case "int", "bool", "text":
		return true
	default:
		return false
	}
}

// bluedbEncodeIndexVal renders a value string to ORDER-PRESERVING bytes per colType.
// Returns (encoded, fixedWidth). Used identically by write, equality-read, and range,
// so all three paths agree by construction (no drift possible).
func bluedbEncodeIndexVal(value, colType string) ([]byte, bool) {
	switch bluedbColClean(colType) {
	case "int":
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			// persistKeyString renders int fields as decimal, so this is a data
			// bug, not a normal path. Degrade to text bytes (best-effort) rather
			// than corrupt the whole field — well-typed data never hits this.
			return []byte(value), false
		}
		var b [8]byte
		// Flip the sign bit: maps [min..-1] below [0..max] in unsigned byte order.
		binary.BigEndian.PutUint64(b[:], uint64(n)^0x8000000000000000)
		return b[:], true
	case "bool":
		if value == "true" {
			return []byte{1}, true
		}
		return []byte{0}, true
	default:
		// text (and any non-fixed) — UTF-8 byte order is code-point order.
		return []byte(value), false
	}
}

// bluedbFieldPrefix = \x00x\x00i\x00<field>\x00  (all entries for a field).
func bluedbFieldPrefix(field string) []byte {
	return []byte(bluedbReserved + "i\x00" + field + "\x00")
}

// bluedbIndexKeyOP builds a full index key: fieldPrefix ++ E(v) [++ NUL] ++ pk.
// Fixed-width values need no delimiter (a given field's width is constant, so the
// value/pk split is at a known offset); variable values get the NUL terminator.
func bluedbIndexKeyOP(field, value, colType, pk string) []byte {
	enc, fixed := bluedbEncodeIndexVal(value, colType)
	buf := bluedbFieldPrefix(field)
	buf = append(buf, enc...)
	if !fixed {
		buf = append(buf, 0) // NUL delimiter — NUL is the minimum byte, so
		// shorter-value-sorts-first holds (prefix property).
	}
	buf = append(buf, pk...)
	return buf
}

// bluedbEqPrefix — the scan prefix that matches exactly the pks with <field>==<value>.
// Fixed: fieldPrefix ++ E(v) (E(v) is a distinct N-byte string, never a prefix of a
// different value's encoding → no trailing delimiter needed). Variable: fieldPrefix
// ++ value ++ NUL (the NUL stops "5" from prefix-matching "55").
func bluedbEqPrefix(field, value, colType string) []byte {
	enc, fixed := bluedbEncodeIndexVal(value, colType)
	buf := bluedbFieldPrefix(field)
	buf = append(buf, enc...)
	if !fixed {
		buf = append(buf, 0)
	}
	return buf
}

// bluedbExtractPk splits the pk tail out of an index key given the field prefix
// length and the colType's fixed width (0 = variable → pk after the value's NUL).
func bluedbExtractPk(key []byte, fieldPrefixLen, width int) string {
	if width > 0 { // fixed: value is exactly `width` bytes after the field prefix
		start := fieldPrefixLen + width
		if start > len(key) {
			return ""
		}
		return string(key[start:])
	}
	// variable: pk follows the first NUL at/after the field prefix.
	rest := key[fieldPrefixLen:]
	if i := bytes.IndexByte(rest, 0); i >= 0 {
		return string(rest[i+1:])
	}
	return ""
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

// ── Argument parsing ─────────────────────────────────────────────────────────

// bluedbFieldValType is a (field, value, colType) triple from Sky (put path).
type bluedbFieldValType struct{ field, value, colType string }

func bluedbParseFVT(arg any) []bluedbFieldValType {
	out := []bluedbFieldValType{}
	for _, it := range asList(arg) {
		// A Sky (String,String,String) lowers to SkyTuple3 OR T3[...]; read
		// V0/V1/V2 reflectively so both work.
		v0, v1, v2 := Field(it, "V0"), Field(it, "V1"), Field(it, "V2")
		if v0 != nil && v1 != nil && v2 != nil {
			out = append(out, bluedbFieldValType{AsString(v0), AsString(v1), AsString(v2)})
		}
	}
	return out
}

// bluedbFieldType is a (field, colType) pair from Sky (delete / reindex paths).
type bluedbFieldType struct{ field, colType string }

func bluedbParseFT(arg any) []bluedbFieldType {
	out := []bluedbFieldType{}
	for _, it := range asList(arg) {
		v0, v1 := Field(it, "V0"), Field(it, "V1")
		if v0 != nil && v1 != nil {
			out = append(out, bluedbFieldType{AsString(v0), AsString(v1)})
		}
	}
	return out
}

// bluedbRenderIndexVal renders a JSON-decoded scalar to the SAME string
// persistKeyString produces, so an OLD value read from the record's JSON matches
// the index key written from the NEW value at put time.
func bluedbRenderIndexVal(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case float64:
		if x == float64(int64(x)) { // JSON numbers decode to float64; integer → int string
			return strconv.FormatInt(int64(x), 10), true
		}
		return "", false // non-integer float is not an indexable key
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

// ── Manifest {version, fields} ───────────────────────────────────────────────

type bluedbManifest struct {
	Version int      `json:"version"`
	Fields  []string `json:"fields"`
}

// bluedbReadManifest reads the manifest, tolerating the legacy bare-array (v1) form.
func bluedbReadManifest(db *bluedb.DB) bluedbManifest {
	mb, ok := db.Get(bluedbIndexManifestKey)
	if !ok {
		return bluedbManifest{Version: 0}
	}
	var m bluedbManifest
	if err := json.Unmarshal(mb, &m); err == nil && (m.Version != 0 || m.Fields != nil) {
		return m
	}
	var fields []string
	if err := json.Unmarshal(mb, &fields); err == nil {
		return bluedbManifest{Version: 1, Fields: fields} // legacy raw-string format
	}
	return bluedbManifest{Version: 0}
}

// BlueDB_putIndexed : Int -> String -> String -> List (String,String,String) -> Task Error ()
// pk, recordJson, [(indexField, newValue, colType)]. Maintains all index entries +
// the primary in ONE atomic WriteBatch under the pk lock.
func BlueDB_putIndexed(idArg, pkArg, jsonArg, fvtArg any) any {
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
		fvts := bluedbParseFVT(fvtArg)
		for _, fv := range fvts {
			// Only TEXT values need the NUL ban (the \x00 delimiter stays
			// unambiguous); int/bool encode to fixed-width binary.
			if bluedbFixedWidth(fv.colType) == 0 && strings.ContainsRune(fv.value, 0) {
				return Err[any, any](ErrInvalidInput("BlueDB: indexed text value must not contain NUL"))
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
			for _, fv := range fvts {
				if oldVal, has := bluedbOldIndexVal(m, fv.field); has && oldVal != fv.value {
					b.Delete(bluedbIndexKeyOP(fv.field, oldVal, fv.colType, pk))
				}
			}
		}
		for _, fv := range fvts {
			b.Put(bluedbIndexKeyOP(fv.field, fv.value, fv.colType, pk), nil)
		}
		b.Put([]byte(pk), []byte(recordJSON))
		if err := db.WriteBatch(b); err != nil {
			return Err[any, any](ErrFfi("BlueDB.putIndexed: " + err.Error()))
		}
		return Ok[any, any](nil)
	}
}

// BlueDB_deleteIndexed : Int -> String -> List (String,String) -> Task Error ()
// pk, [(indexField, colType)]. Removes the record and all its index entries in one batch.
func BlueDB_deleteIndexed(idArg, pkArg, ftArg any) any {
	return func() any {
		db := bluedbLookup(idArg)
		if db == nil {
			return Err[any, any](ErrInvalidInput("BlueDB.deleteIndexed: store not found (closed?)"))
		}
		id := int64(AsInt(idArg))
		pk := AsString(pkArg)
		fts := bluedbParseFT(ftArg)
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
			for _, ft := range fts {
				if v, has := bluedbOldIndexVal(m, ft.field); has {
					b.Delete(bluedbIndexKeyOP(ft.field, v, ft.colType, pk))
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

// BlueDB_findByIndex : Int -> String -> String -> String -> Task Error (List String)
// field, value, colType → the pks whose <field> == <value> (equality prefix scan).
func BlueDB_findByIndex(idArg, fieldArg, valueArg, colTypeArg any) any {
	return func() any {
		db := bluedbLookup(idArg)
		if db == nil {
			return Err[any, any](ErrInvalidInput("BlueDB.findByIndex: store not found (closed?)"))
		}
		field := AsString(fieldArg)
		colType := AsString(colTypeArg)
		prefix := bluedbEqPrefix(field, AsString(valueArg), colType)
		fpLen := len(bluedbFieldPrefix(field))
		width := bluedbFixedWidth(colType)
		pks := []any{}
		db.Scan(prefix, nil, 0, func(k, _ []byte) bool {
			pks = append(pks, bluedbExtractPk(k, fpLen, width))
			return true
		})
		return Ok[any, any](pks)
	}
}

// BlueDB_countByIndex : Int -> String -> String -> String -> Task Error Int
// Count records whose <field> == <value> (count index entries, no record fetch).
func BlueDB_countByIndex(idArg, fieldArg, valueArg, colTypeArg any) any {
	return func() any {
		db := bluedbLookup(idArg)
		if db == nil {
			return Err[any, any](ErrInvalidInput("BlueDB.countByIndex: store not found (closed?)"))
		}
		prefix := bluedbEqPrefix(AsString(fieldArg), AsString(valueArg), AsString(colTypeArg))
		n := 0
		db.Scan(prefix, nil, 0, func(_, _ []byte) bool {
			n++
			return true
		})
		return Ok[any, any](n)
	}
}

// BlueDB_findByIndexRange : Int -> String(field) -> String(colType)
//   -> Bool(hasLo) -> String(lo) -> Bool(hasHi) -> String(hi)
//   -> Task Error (List String)
// The pks whose <field> is in the half-open range [lo, hi). Unbounded ends via the
// hasLo/hasHi flags (an absent upper bound is NOT ""). Correct because E is
// order-preserving; rejects non-rangeable colTypes (real/blob/money → use SQL).
func BlueDB_findByIndexRange(idArg, fieldArg, colTypeArg, hasLoArg, loArg, hasHiArg, hiArg any) any {
	return func() any {
		db := bluedbLookup(idArg)
		if db == nil {
			return Err[any, any](ErrInvalidInput("BlueDB.findByIndexRange: store not found (closed?)"))
		}
		field := AsString(fieldArg)
		colType := AsString(colTypeArg)
		if !bluedbRangeable(colType) {
			return Err[any, any](ErrInvalidInput(
				"BlueDB.findByIndexRange: field \"" + field + "\" of type " + colType +
					" has no order-preserving KV range (use the SQL backend's typed query builder)"))
		}
		pks := []any{}
		bluedbRangeScan(db, field, colType,
			AsBool(hasLoArg), AsString(loArg), AsBool(hasHiArg), AsString(hiArg),
			func(pk string) { pks = append(pks, pk) })
		return Ok[any, any](pks)
	}
}

// BlueDB_countByIndexRange : Int -> String -> String -> Bool -> String -> Bool -> String -> Task Error Int
func BlueDB_countByIndexRange(idArg, fieldArg, colTypeArg, hasLoArg, loArg, hasHiArg, hiArg any) any {
	return func() any {
		db := bluedbLookup(idArg)
		if db == nil {
			return Err[any, any](ErrInvalidInput("BlueDB.countByIndexRange: store not found (closed?)"))
		}
		field := AsString(fieldArg)
		colType := AsString(colTypeArg)
		if !bluedbRangeable(colType) {
			return Err[any, any](ErrInvalidInput(
				"BlueDB.countByIndexRange: field \"" + field + "\" of type " + colType +
					" has no order-preserving KV range (use the SQL backend's typed query builder)"))
		}
		n := 0
		bluedbRangeScan(db, field, colType,
			AsBool(hasLoArg), AsString(loArg), AsBool(hasHiArg), AsString(hiArg),
			func(_ string) { n++ })
		return Ok[any, any](n)
	}
}

// bluedbRangeScan walks the field's index keyspace ascending in [lo, hi), calling
// emit(pk) per match. Seek: startAfter = fieldPrefix ++ E(lo) is a proper prefix of
// the first lo entry, so the engine's strict k>after INCLUDES lo and excludes < lo.
// Stop: at the first key >= (fieldPrefix ++ E(hi)) — since keys sort by E(v) then pk
// and E is order-preserving, that key has value >= hi and so does every later key,
// so stopping skips no match (half-open: hi excluded).
func bluedbRangeScan(db *bluedb.DB, field, colType string, hasLo bool, lo string, hasHi bool, hi string, emit func(string)) {
	fp := bluedbFieldPrefix(field)
	fpLen := len(fp)
	width := bluedbFixedWidth(colType)

	var startAfter []byte
	if hasLo {
		enc, _ := bluedbEncodeIndexVal(lo, colType)
		startAfter = append(append([]byte(nil), fp...), enc...)
	} else {
		startAfter = nil // engine seeks from the field prefix's first key
	}

	var ceiling []byte
	if hasHi {
		enc, _ := bluedbEncodeIndexVal(hi, colType)
		ceiling = append(append([]byte(nil), fp...), enc...)
	}

	db.Scan(fp, startAfter, 0, func(k, _ []byte) bool {
		if ceiling != nil && bytes.Compare(k, ceiling) >= 0 {
			return false // reached hi — every later key is also >= hi
		}
		emit(bluedbExtractPk(k, fpLen, width))
		return true
	})
}

// BlueDB_reindex : Int -> List (String,String) -> Task Error Int
// (field, colType) pairs. Idempotent backfill + FORMAT MIGRATION:
//   - version < CURRENT → FULL re-encode: sweep ALL index entries, rewrite every
//     declared field for every record in the new (v2) encoding, flip version LAST.
//   - version == CURRENT, field set unchanged → O(1) skip.
//   - version == CURRENT, field set changed → incremental (backfill added, sweep
//     dropped), as E2.
// Call once at startup BEFORE serving so equality/range never split-brain v1/v2.
func BlueDB_reindex(idArg, ftArg any) any {
	return func() any {
		db := bluedbLookup(idArg)
		if db == nil {
			return Err[any, any](ErrInvalidInput("BlueDB.reindex: store not found (closed?)"))
		}
		id := int64(AsInt(idArg))
		fts := bluedbParseFT(ftArg)
		declared := make([]string, 0, len(fts))
		colTypeOf := map[string]string{}
		declaredSet := map[string]bool{}
		for _, ft := range fts {
			declared = append(declared, ft.field)
			colTypeOf[ft.field] = ft.colType
			declaredSet[ft.field] = true
		}

		manifest := bluedbReadManifest(db)
		manifestSet := map[string]bool{}
		for _, f := range manifest.Fields {
			manifestSet[f] = true
		}
		fullRebuild := manifest.Version < bluedbIndexFormatVersion
		if !fullRebuild && sameStringSet(declaredSet, manifestSet) {
			return Ok[any, any](0) // steady state — no O(n) work
		}

		// Sweep: full rebuild wipes EVERY index entry (format changed); incremental
		// wipes only dropped fields' entries.
		if fullRebuild {
			if e := bluedbSweepPrefix(db, bluedbReserved+"i\x00"); e != nil {
				return Err[any, any](e)
			}
		} else {
			for _, f := range manifest.Fields {
				if !declaredSet[f] {
					if e := bluedbSweepPrefix(db, bluedbReserved+"i\x00"+f+"\x00"); e != nil {
						return Err[any, any](e)
					}
				}
			}
		}

		// Fields to (re)build: full → all declared; incremental → added only.
		var toBuild []string
		for _, f := range declared {
			if fullRebuild || !manifestSet[f] {
				toBuild = append(toBuild, f)
			}
		}

		count := 0
		if len(toBuild) > 0 {
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
						for _, f := range toBuild {
							if v, has := bluedbOldIndexVal(m, f); has {
								b.Put(bluedbIndexKeyOP(f, v, colTypeOf[f], pk), nil)
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

		// Persist the new manifest LAST (crash before this → version stays < CURRENT
		// → next reindex redoes the full rebuild; idempotent).
		mb, _ := json.Marshal(bluedbManifest{Version: bluedbIndexFormatVersion, Fields: declared})
		if err := db.Put(bluedbIndexManifestKey, mb); err != nil {
			return Err[any, any](ErrFfi("BlueDB.reindex: manifest write: " + err.Error()))
		}
		return Ok[any, any](count)
	}
}

// bluedbSweepPrefix deletes every key under a reserved prefix (index entries).
// Returns nil, or a Sky Error value (type any) on a delete failure.
func bluedbSweepPrefix(db *bluedb.DB, prefix string) any {
	var stale [][]byte
	db.Scan([]byte(prefix), nil, 0, func(k, _ []byte) bool {
		stale = append(stale, append([]byte(nil), k...))
		return true
	})
	for _, k := range stale {
		if err := db.Delete(k); err != nil {
			return ErrFfi("BlueDB.reindex: sweep: " + err.Error())
		}
	}
	return nil
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

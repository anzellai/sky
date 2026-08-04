package rt

import (
	"bytes"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"sky-app/bluedb"
)

func registerIdxStore(t *testing.T) int {
	t.Helper()
	path := filepath.Join(t.TempDir(), "idx.blue")
	db, err := bluedb.Open(path, bluedb.Options{Sync: false})
	if err != nil {
		t.Fatal(err)
	}
	id := bluedbNextID.Add(1)
	bluedbRegMu.Lock()
	bluedbRegistry[id] = &bluedbEntry{db: db, path: path}
	bluedbByPath[path] = id
	bluedbRegMu.Unlock()
	t.Cleanup(func() {
		bluedbRegMu.Lock()
		delete(bluedbRegistry, id)
		delete(bluedbByPath, path)
		bluedbRegMu.Unlock()
		_ = db.Close()
	})
	return int(id)
}

func idxDB(id int) *bluedb.DB {
	bluedbRegMu.Lock()
	defer bluedbRegMu.Unlock()
	return bluedbRegistry[int64(id)].db
}

func runOK(t *testing.T, task any) any {
	t.Helper()
	res := task.(func() any)()
	r, ok := res.(SkyResult[any, any])
	if !ok || r.Tag != 0 {
		t.Fatalf("expected Ok, got %#v", res)
	}
	return r.OkValue
}

// putIdx: each fvt is {field, value, colType}.
func putIdx(t *testing.T, id int, pk, json string, fvt ...[3]string) {
	t.Helper()
	triples := []any{}
	for _, p := range fvt {
		triples = append(triples, SkyTuple3{V0: p[0], V1: p[1], V2: p[2]})
	}
	runOK(t, BlueDB_putIndexed(id, pk, json, triples))
}

func findPks(t *testing.T, id int, field, value, colType string) []string {
	t.Helper()
	r := BlueDB_findByIndex(id, field, value, colType).(func() any)().(SkyResult[any, any])
	out := []string{}
	for _, p := range r.OkValue.([]any) {
		out = append(out, p.(string))
	}
	sort.Strings(out)
	return out
}

// ftPairs builds a Sky List (String,String) of (field, colType).
func ftPairs(pairs ...[2]string) []any {
	out := []any{}
	for _, p := range pairs {
		out = append(out, SkyTuple2{V0: p[0], V1: p[1]})
	}
	return out
}

func countTextEntries(id int, field, value, colType string) int {
	return BlueDB_countByIndex(id, field, value, colType).(func() any)().(SkyResult[any, any]).OkValue.(int)
}

func totalFieldEntries(id int, field string) int {
	db := idxDB(id)
	n := 0
	db.Scan(bluedbFieldPrefix(field), nil, 0, func(_, _ []byte) bool { n++; return true })
	return n
}

func TestIndexPutFindAndStaleRemoval(t *testing.T) {
	id := registerIdxStore(t)
	putIdx(t, id, "u1", `{"email":"ada@x"}`, [3]string{"email", "ada@x", "text"})
	if pks := findPks(t, id, "email", "ada@x", "text"); len(pks) != 1 || pks[0] != "u1" {
		t.Fatalf("find ada: %v", pks)
	}
	putIdx(t, id, "u1", `{"email":"ada2@x"}`, [3]string{"email", "ada2@x", "text"})
	if pks := findPks(t, id, "email", "ada@x", "text"); len(pks) != 0 {
		t.Fatalf("stale index entry must be gone: %v", pks)
	}
	if pks := findPks(t, id, "email", "ada2@x", "text"); len(pks) != 1 || pks[0] != "u1" {
		t.Fatalf("new index entry: %v", pks)
	}
	if n := totalFieldEntries(id, "email"); n != 1 {
		t.Fatalf("exactly one email entry expected, got %d", n)
	}
}

func TestIndexNonUniqueMultiplePks(t *testing.T) {
	id := registerIdxStore(t)
	putIdx(t, id, "u1", `{"status":"active"}`, [3]string{"status", "active", "text"})
	putIdx(t, id, "u2", `{"status":"active"}`, [3]string{"status", "active", "text"})
	putIdx(t, id, "u3", `{"status":"idle"}`, [3]string{"status", "idle", "text"})
	if pks := findPks(t, id, "status", "active", "text"); len(pks) != 2 {
		t.Fatalf("non-unique: want 2 active pks, got %v", pks)
	}
}

func TestIndexDeleteRemovesEntries(t *testing.T) {
	id := registerIdxStore(t)
	putIdx(t, id, "u1", `{"email":"ada@x"}`, [3]string{"email", "ada@x", "text"})
	runOK(t, BlueDB_deleteIndexed(id, "u1", ftPairs([2]string{"email", "text"})))
	if pks := findPks(t, id, "email", "ada@x", "text"); len(pks) != 0 {
		t.Fatalf("index entry must be gone after delete: %v", pks)
	}
	if _, ok := idxDB(id).Get([]byte("u1")); ok {
		t.Fatal("pk must be gone after delete")
	}
}

// KEYSTONE: concurrent updates to the SAME pk leave exactly ONE index entry.
func TestIndexConcurrentSamePkNoOrphan(t *testing.T) {
	id := registerIdxStore(t)
	const n = 64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			val := fmt.Sprintf("v%02d", i)
			j := fmt.Sprintf(`{"email":%q}`, val)
			triples := []any{SkyTuple3{V0: "email", V1: val, V2: "text"}}
			BlueDB_putIndexed(id, "u1", j, triples).(func() any)()
		}(i)
	}
	wg.Wait()
	if got := totalFieldEntries(id, "email"); got != 1 {
		t.Fatalf("concurrent same-pk left %d email index entries (want 1) — TOCTOU orphans", got)
	}
}

func TestIndexReservedHiddenFromKeys(t *testing.T) {
	id := registerIdxStore(t)
	putIdx(t, id, "u1", `{"email":"ada@x"}`, [3]string{"email", "ada@x", "text"})
	r := BlueDB_keys(id).(func() any)().(SkyResult[any, any])
	for _, k := range r.OkValue.([]any) {
		if bluedbIsReserved(k.(string)) {
			t.Fatalf("reserved key leaked into keys(): %q", k)
		}
	}
	if len(r.OkValue.([]any)) != 1 {
		t.Fatalf("keys() should show only the 1 app key, got %v", r.OkValue)
	}
}

func TestIndexReindexBackfillAndManifestGuard(t *testing.T) {
	id := registerIdxStore(t)
	db := idxDB(id)
	_ = db.Put([]byte("u1"), []byte(`{"email":"ada@x"}`))
	_ = db.Put([]byte("u2"), []byte(`{"email":"lin@x"}`))
	c := runOK(t, BlueDB_reindex(id, ftPairs([2]string{"email", "text"})))
	if c.(int) != 2 {
		t.Fatalf("reindex should backfill 2 records, got %v", c)
	}
	if pks := findPks(t, id, "email", "ada@x", "text"); len(pks) != 1 || pks[0] != "u1" {
		t.Fatalf("backfilled index: %v", pks)
	}
	c2 := runOK(t, BlueDB_reindex(id, ftPairs([2]string{"email", "text"})))
	if c2.(int) != 0 {
		t.Fatalf("re-reindex should skip (manifest), got %v", c2)
	}
}

func TestIndexCrashConsistencyReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.blue")
	db, _ := bluedb.Open(path, bluedb.Options{Sync: false})
	id := bluedbNextID.Add(1)
	bluedbRegMu.Lock()
	bluedbRegistry[id] = &bluedbEntry{db: db, path: path}
	bluedbByPath[path] = id
	bluedbRegMu.Unlock()
	putIdx(t, int(id), "u1", `{"email":"ada@x"}`, [3]string{"email", "ada@x", "text"})
	db.Close()
	bluedbRegMu.Lock()
	delete(bluedbRegistry, id)
	delete(bluedbByPath, path)
	bluedbRegMu.Unlock()

	db2, _ := bluedb.Open(path, bluedb.Options{Sync: false})
	defer db2.Close()
	if _, ok := db2.Get([]byte("u1")); !ok {
		t.Fatal("primary missing after reopen")
	}
	n := 0
	db2.Scan(bluedbFieldPrefix("email"), nil, 0, func(_, _ []byte) bool { n++; return true })
	if n != 1 {
		t.Fatalf("index entry missing after reopen (index+primary not atomic): %d", n)
	}
}

func TestIndexCountByIndex(t *testing.T) {
	id := registerIdxStore(t)
	putIdx(t, id, "u1", `{"status":"active"}`, [3]string{"status", "active", "text"})
	putIdx(t, id, "u2", `{"status":"active"}`, [3]string{"status", "active", "text"})
	putIdx(t, id, "u3", `{"status":"idle"}`, [3]string{"status", "idle", "text"})
	if countTextEntries(id, "status", "active", "text") != 2 {
		t.Fatalf("count active want 2")
	}
	if countTextEntries(id, "status", "idle", "text") != 1 {
		t.Fatalf("count idle want 1")
	}
	if countTextEntries(id, "status", "none", "text") != 0 {
		t.Fatalf("count none want 0")
	}
}

// ── R1: order-preserving encoding + ordered range ────────────────────────────

// Grill must-get-right #1: E(int) memcmp order == numeric order across the sign
// boundary. This is the entire correctness basis of the range.
func TestIndexIntEncodingOrderPreserving(t *testing.T) {
	nums := []int64{-9223372036854775808, -1000000, -5, -1, 0, 1, 5, 18, 100, 9223372036854775807}
	var prev []byte
	for i, n := range nums {
		enc, fixed := bluedbEncodeIndexVal(fmt.Sprintf("%d", n), "int")
		if !fixed || len(enc) != 8 {
			t.Fatalf("int encoding must be 8 fixed bytes, got %d", len(enc))
		}
		if i > 0 && bytes.Compare(prev, enc) >= 0 {
			t.Fatalf("encoding not order-preserving at %d: prev >= cur", n)
		}
		prev = enc
	}
	// The exact case lexical range gets WRONG: "100" < "18" < "5" lexically, but
	// the encoding must order 5 < 18 < 100.
	e5, _ := bluedbEncodeIndexVal("5", "int")
	e18, _ := bluedbEncodeIndexVal("18", "int")
	e100, _ := bluedbEncodeIndexVal("100", "int")
	if !(bytes.Compare(e5, e18) < 0 && bytes.Compare(e18, e100) < 0) {
		t.Fatal("5 < 18 < 100 must hold in encoding (lexical would fail)")
	}
}

func putInt(t *testing.T, id int, pk string, age int) {
	t.Helper()
	j := fmt.Sprintf(`{"id":%q,"age":%d}`, pk, age)
	putIdx(t, id, pk, j, [3]string{"age", fmt.Sprintf("%d", age), "int"})
}

func rangePks(t *testing.T, id int, field, colType string, hasLo bool, lo string, hasHi bool, hi string) []string {
	t.Helper()
	r := BlueDB_findByIndexRange(id, field, colType, hasLo, lo, hasHi, hi).(func() any)().(SkyResult[any, any])
	if r.Tag != 0 {
		t.Fatalf("range returned Err: %#v", r)
	}
	out := []string{}
	for _, p := range r.OkValue.([]any) {
		out = append(out, p.(string))
	}
	sort.Strings(out)
	return out
}

func TestIndexRangeInt(t *testing.T) {
	id := registerIdxStore(t)
	// ages that expose the lexical bug: 5, 18, 100.
	putInt(t, id, "a", 5)
	putInt(t, id, "b", 18)
	putInt(t, id, "c", 100)
	putInt(t, id, "d", -3)
	putInt(t, id, "e", 42)

	// [18, 100) → {18=b, 42=e}. A lexical range would (wrongly) include/exclude
	// based on "18".."100" string order.
	if got := rangePks(t, id, "age", "int", true, "18", true, "100"); !eqStrs(got, []string{"b", "e"}) {
		t.Fatalf("[18,100) = %v want [b e]", got)
	}
	// equality still works with int encoding
	if got := findPks(t, id, "age", "42", "int"); !eqStrs(got, []string{"e"}) {
		t.Fatalf("eq 42 = %v want [e]", got)
	}
	// unbounded lower: (-inf, 5) → {-3=d}
	if got := rangePks(t, id, "age", "int", false, "", true, "5"); !eqStrs(got, []string{"d"}) {
		t.Fatalf("(-inf,5) = %v want [d]", got)
	}
	// unbounded upper: [42, +inf) → {42=e, 100=c}
	if got := rangePks(t, id, "age", "int", true, "42", false, ""); !eqStrs(got, []string{"c", "e"}) {
		t.Fatalf("[42,+inf) = %v want [c e]", got)
	}
	// fully unbounded → all 5
	if got := rangePks(t, id, "age", "int", false, "", false, ""); len(got) != 5 {
		t.Fatalf("(-inf,+inf) = %v want 5", got)
	}
	// negative boundary: [-3, 6) → {-3=d, 5=a}
	if got := rangePks(t, id, "age", "int", true, "-3", true, "6"); !eqStrs(got, []string{"a", "d"}) {
		t.Fatalf("[-3,6) = %v want [a d]", got)
	}
	// count agrees
	rc := BlueDB_countByIndexRange(id, "age", "int", true, "18", true, "100").(func() any)().(SkyResult[any, any])
	if rc.OkValue.(int) != 2 {
		t.Fatalf("countRange [18,100) = %v want 2", rc.OkValue)
	}
}

func TestIndexRangeText(t *testing.T) {
	id := registerIdxStore(t)
	for _, s := range []string{"ada", "ad", "ada2", "bob", "zoe"} {
		putIdx(t, id, s, fmt.Sprintf(`{"name":%q}`, s), [3]string{"name", s, "text"})
	}
	// [ada, bob) → ada, ada2 (NOT "ad" which is < "ada"; NOT bob which is excluded)
	if got := rangePks(t, id, "name", "text", true, "ada", true, "bob"); !eqStrs(got, []string{"ada", "ada2"}) {
		t.Fatalf("[ada,bob) = %v want [ada ada2]", got)
	}
	// equality on "ada" must not match "ada2" (prefix guard)
	if got := findPks(t, id, "name", "ada", "text"); !eqStrs(got, []string{"ada"}) {
		t.Fatalf("eq ada = %v want [ada] (must not prefix-match ada2)", got)
	}
}

func TestIndexRangeRejectsNonRangeable(t *testing.T) {
	id := registerIdxStore(t)
	r := BlueDB_findByIndexRange(id, "price", "real", true, "1.0", true, "9.0").(func() any)().(SkyResult[any, any])
	if r.Tag == 0 {
		t.Fatal("range over a real field must return Err (no order-preserving KV range)")
	}
}

// Migration: a v1 store (legacy bare-array manifest + raw-string int entries) must
// full-rebuild on reindex to v2 so range works.
func TestIndexV1MigrationFullRebuild(t *testing.T) {
	id := registerIdxStore(t)
	db := idxDB(id)
	// Simulate v1: legacy manifest (bare array) + raw-string int index entries.
	db.Put(bluedbIndexManifestKey, []byte(`["age"]`))
	db.Put([]byte("a"), []byte(`{"id":"a","age":5}`))
	db.Put([]byte("b"), []byte(`{"id":"b","age":18}`))
	db.Put([]byte(bluedbReserved+"i\x00age\x005\x00a"), nil)  // v1 raw-string entry
	db.Put([]byte(bluedbReserved+"i\x00age\x0018\x00b"), nil) // v1 raw-string entry

	// reindex must detect version<2 and FULL-rebuild.
	runOK(t, BlueDB_reindex(id, ftPairs([2]string{"age", "int"})))

	// v1 raw-string entries gone; v2 order-preserving entries present + range works.
	if got := rangePks(t, id, "age", "int", true, "5", true, "18"); !eqStrs(got, []string{"a"}) {
		t.Fatalf("post-migration [5,18) = %v want [a]", got)
	}
	if got := findPks(t, id, "age", "18", "int"); !eqStrs(got, []string{"b"}) {
		t.Fatalf("post-migration eq 18 = %v want [b]", got)
	}
	// exactly 2 entries total (no v1 leftovers doubling up)
	if n := totalFieldEntries(id, "age"); n != 2 {
		t.Fatalf("post-migration age entries = %d want 2 (v1 not swept?)", n)
	}
}

func eqStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

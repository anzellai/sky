package rt

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"testing"

	"sky-app/bluedb"
)

func collPut(t *testing.T, id int, coll, pk, jsonStr string, fvt ...[3]string) {
	t.Helper()
	collPutCols(t, id, coll, pk, jsonStr, nil, fvt...)
}

// collPutCols is collPut with a schema descriptor (colName, kind-with-flags) for
// default/touch enforcement. Returns the stored JSON.
func collPutCols(t *testing.T, id int, coll, pk, jsonStr string, cols [][2]string, fvt ...[3]string) string {
	t.Helper()
	triples := []any{}
	for _, p := range fvt {
		triples = append(triples, SkyTuple3{V0: p[0], V1: p[1], V2: p[2]})
	}
	colList := []any{}
	for _, c := range cols {
		colList = append(colList, SkyTuple2{V0: c[0], V1: c[1]})
	}
	return runOK(t, BlueDB_collPut(id, coll, pk, jsonStr, triples, colList)).(string)
}

func collGet(t *testing.T, id int, coll, pk string) (string, bool) {
	t.Helper()
	r := BlueDB_collGet(id, coll, pk).(func() any)().(SkyResult[any, any])
	if r.Tag != 0 {
		t.Fatalf("collGet Err: %#v", r)
	}
	m := r.OkValue.(SkyMaybe[any])
	if m.Tag == 1 {
		return "", false
	}
	return m.JustValue.(string), true
}

func collAllPks(t *testing.T, id int, coll string) []string {
	t.Helper()
	r := BlueDB_collAll(id, coll, 0).(func() any)().(SkyResult[any, any])
	out := []string{}
	for _, it := range r.OkValue.([]any) {
		out = append(out, it.(SkyTuple2).V0.(string))
	}
	sort.Strings(out)
	return out
}

func collCountN(id int, coll string) int {
	return BlueDB_collCount(id, coll).(func() any)().(SkyResult[any, any]).OkValue.(int)
}

func collFind(t *testing.T, id int, coll, field, value, colType string) []string {
	t.Helper()
	r := BlueDB_collFindByIndex(id, coll, field, value, colType).(func() any)().(SkyResult[any, any])
	out := []string{}
	for _, p := range r.OkValue.([]any) {
		out = append(out, p.(string))
	}
	sort.Strings(out)
	return out
}

// The headline P1 win: two collections in ONE store are fully isolated (the
// old bare-pk layout let all/count/get see the whole store).
func TestCollIsolation(t *testing.T) {
	id := registerIdxStore(t)
	collPut(t, id, "users", "1", `{"kind":"user"}`)
	collPut(t, id, "orders", "1", `{"kind":"order"}`)
	collPut(t, id, "users", "2", `{"kind":"user2"}`)

	if v, ok := collGet(t, id, "users", "1"); !ok || v != `{"kind":"user"}` {
		t.Fatalf("users/1 = %q,%v", v, ok)
	}
	if v, ok := collGet(t, id, "orders", "1"); !ok || v != `{"kind":"order"}` {
		t.Fatalf("orders/1 = %q,%v (same pk, different collection must not collide)", v, ok)
	}
	if got := collAllPks(t, id, "users"); !eqStrs(got, []string{"1", "2"}) {
		t.Fatalf("all users = %v want [1 2] (must NOT include orders)", got)
	}
	if got := collAllPks(t, id, "orders"); !eqStrs(got, []string{"1"}) {
		t.Fatalf("all orders = %v want [1]", got)
	}
	if collCountN(id, "users") != 2 || collCountN(id, "orders") != 1 {
		t.Fatalf("counts: users=%d orders=%d", collCountN(id, "users"), collCountN(id, "orders"))
	}
}

func TestCollIndexIsolation(t *testing.T) {
	id := registerIdxStore(t)
	collPut(t, id, "users", "u1", `{"email":"a@x"}`, [3]string{"email", "a@x", "text"})
	collPut(t, id, "signups", "s1", `{"email":"a@x"}`, [3]string{"email", "a@x", "text"})

	if got := collFind(t, id, "users", "email", "a@x", "text"); !eqStrs(got, []string{"u1"}) {
		t.Fatalf("users email a@x = %v want [u1] (must not see signups)", got)
	}
	if got := collFind(t, id, "signups", "email", "a@x", "text"); !eqStrs(got, []string{"s1"}) {
		t.Fatalf("signups email a@x = %v want [s1]", got)
	}
}

func TestCollCRUDAndStaleIndex(t *testing.T) {
	id := registerIdxStore(t)
	collPut(t, id, "c", "u1", `{"email":"ada@x"}`, [3]string{"email", "ada@x", "text"})
	if got := collFind(t, id, "c", "email", "ada@x", "text"); !eqStrs(got, []string{"u1"}) {
		t.Fatalf("find ada: %v", got)
	}
	// update the indexed field → old entry gone
	collPut(t, id, "c", "u1", `{"email":"ada2@x"}`, [3]string{"email", "ada2@x", "text"})
	if got := collFind(t, id, "c", "email", "ada@x", "text"); len(got) != 0 {
		t.Fatalf("stale entry must be gone: %v", got)
	}
	if got := collFind(t, id, "c", "email", "ada2@x", "text"); !eqStrs(got, []string{"u1"}) {
		t.Fatalf("new entry: %v", got)
	}
	// delete → record + index gone
	runOK(t, BlueDB_collDelete(id, "c", "u1", ftPairs([2]string{"email", "text"})))
	if _, ok := collGet(t, id, "c", "u1"); ok {
		t.Fatal("record must be gone after delete")
	}
	if got := collFind(t, id, "c", "email", "ada2@x", "text"); len(got) != 0 {
		t.Fatalf("index must be gone after delete: %v", got)
	}
}

func TestCollRangeInt(t *testing.T) {
	id := registerIdxStore(t)
	for pk, age := range map[string]int{"a": 5, "b": 18, "c": 100, "d": 42} {
		collPut(t, id, "p", pk, fmt.Sprintf(`{"age":%d}`, age),
			[3]string{"age", fmt.Sprintf("%d", age), "int"})
	}
	r := BlueDB_collFindByIndexRange(id, "p", "age", "int", true, "18", true, "100").(func() any)().(SkyResult[any, any])
	out := []string{}
	for _, p := range r.OkValue.([]any) {
		out = append(out, p.(string))
	}
	sort.Strings(out)
	if !eqStrs(out, []string{"b", "d"}) { // 18, 42 — a lexical range would be wrong
		t.Fatalf("[18,100) = %v want [b d]", out)
	}
}

// Legacy bare-pk records migrate into the collection namespace on reindex.
func TestCollMigrationFromBarePk(t *testing.T) {
	id := registerIdxStore(t)
	db := idxDB(id)
	db.Put([]byte("legacy1"), []byte(`{"email":"ada@x"}`))
	db.Put([]byte("legacy2"), []byte(`{"email":"lin@x"}`))

	c := runOK(t, BlueDB_collReindex(id, "users", ftPairs([2]string{"email", "text"})))
	if c.(int) != 2 {
		t.Fatalf("reindex should index 2 migrated records, got %v", c)
	}
	// records now under the namespace
	if got := collAllPks(t, id, "users"); !eqStrs(got, []string{"legacy1", "legacy2"}) {
		t.Fatalf("migrated all = %v want [legacy1 legacy2]", got)
	}
	// bare keys gone
	if _, ok := db.Get([]byte("legacy1")); ok {
		t.Fatal("bare key must be removed after migration")
	}
	// namespaced index built
	if got := collFind(t, id, "users", "email", "ada@x", "text"); !eqStrs(got, []string{"legacy1"}) {
		t.Fatalf("migrated index = %v want [legacy1]", got)
	}
	// re-run is O(1) skip
	c2 := runOK(t, BlueDB_collReindex(id, "users", ftPairs([2]string{"email", "text"})))
	if c2.(int) != 0 {
		t.Fatalf("re-reindex should skip, got %v", c2)
	}
}

// Concurrent puts to distinct pks in one collection all land; -race clean.
func TestCollConcurrentPuts(t *testing.T) {
	id := registerIdxStore(t)
	const n = 64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pk := fmt.Sprintf("u%02d", i)
			BlueDB_collPut(id, "c", pk, fmt.Sprintf(`{"i":%d}`, i), []any{}, []any{}).(func() any)()
		}(i)
	}
	wg.Wait()
	if collCountN(id, "c") != n {
		t.Fatalf("concurrent puts: count=%d want %d", collCountN(id, "c"), n)
	}
}

// Guard: reserved/namespaced keys stay hidden from the raw public keys surface.
func TestCollHiddenFromRawKeys(t *testing.T) {
	id := registerIdxStore(t)
	collPut(t, id, "c", "u1", `{"x":1}`, [3]string{"x", "1", "int"})
	r := BlueDB_keys(id).(func() any)().(SkyResult[any, any])
	if len(r.OkValue.([]any)) != 0 {
		t.Fatalf("namespaced records must be hidden from raw keys(), got %v", r.OkValue)
	}
	_ = bluedb.Options{}
}

// P2: defaultNow stamps created_at on insert; touchOnUpdate bumps updated_at on
// every update; default* fills a zero field; a non-zero field is untouched (D2).
func TestCollDefaultsAndTouch(t *testing.T) {
	id := registerIdxStore(t)
	// deterministic clock
	old := bluedbNow
	bluedbNow = func() string { return "2026-01-01 00:00:00" }
	defer func() { bluedbNow = old }()

	cols := [][2]string{
		{"created_at", "text|dnow"},
		{"updated_at", "text|dnow|touch"},
		{"status", "text|dtext=active"},
		{"name", "text"},
	}
	// insert with zero created_at/updated_at/status → all stamped/defaulted
	stored := collPutCols(t, id, "u", "1", `{"name":"Ann","created_at":"","updated_at":"","status":""}`, cols)
	var m map[string]any
	json.Unmarshal([]byte(stored), &m)
	if m["created_at"] != "2026-01-01 00:00:00" {
		t.Fatalf("created_at not stamped on insert: %v", m["created_at"])
	}
	if m["updated_at"] != "2026-01-01 00:00:00" {
		t.Fatalf("updated_at not stamped on insert: %v", m["updated_at"])
	}
	if m["status"] != "active" {
		t.Fatalf("status default not applied: %v", m["status"])
	}
	if m["name"] != "Ann" {
		t.Fatalf("name (no default) must be preserved: %v", m["name"])
	}

	// update: created_at STAYS (non-zero now); updated_at bumps to the new now
	bluedbNow = func() string { return "2026-02-02 00:00:00" }
	stored2 := collPutCols(t, id, "u", "1",
		`{"name":"Ann2","created_at":"2026-01-01 00:00:00","updated_at":"2026-01-01 00:00:00","status":"active"}`, cols)
	var m2 map[string]any
	json.Unmarshal([]byte(stored2), &m2)
	if m2["created_at"] != "2026-01-01 00:00:00" {
		t.Fatalf("created_at must NOT change on update: %v", m2["created_at"])
	}
	if m2["updated_at"] != "2026-02-02 00:00:00" {
		t.Fatalf("updated_at must bump on update: %v", m2["updated_at"])
	}

	// D2 boundary: a deliberately-set status is preserved (non-zero)
	stored3 := collPutCols(t, id, "u", "2", `{"name":"Bo","status":"vip"}`, cols)
	var m3 map[string]any
	json.Unmarshal([]byte(stored3), &m3)
	if m3["status"] != "vip" {
		t.Fatalf("deliberate non-zero status must be preserved: %v", m3["status"])
	}
}

func colListOf(cols [][2]string) []any {
	out := []any{}
	for _, c := range cols {
		out = append(out, SkyTuple2{V0: c[0], V1: c[1]})
	}
	return out
}

func serialIDOf(t *testing.T, stored string) int {
	t.Helper()
	var m map[string]any
	json.Unmarshal([]byte(stored), &m)
	return int(m["id"].(float64))
}

// P3: serial PK assignment — an unset "!" pk gets the next per-collection id.
func TestCollSerial(t *testing.T) {
	id := registerIdxStore(t)
	cols := [][2]string{{"id", "int!"}}
	if s := collPutCols(t, id, "u", "", `{"id":0,"name":"Ann"}`, cols); serialIDOf(t, s) != 1 {
		t.Fatalf("first serial id != 1: %s", s)
	}
	if s := collPutCols(t, id, "u", "", `{"id":0,"name":"Bo"}`, cols); serialIDOf(t, s) != 2 {
		t.Fatalf("second serial id != 2: %s", s)
	}
	// records land at pk "1" and "2"
	if _, ok := collGet(t, id, "u", "1"); !ok {
		t.Fatal("serial record 1 missing")
	}
	if _, ok := collGet(t, id, "u", "2"); !ok {
		t.Fatal("serial record 2 missing")
	}
	// a record WITH an explicit pk is an upsert, not a new assignment
	if s := collPutCols(t, id, "u", "1", `{"id":1,"name":"Ann2"}`, cols); serialIDOf(t, s) != 1 {
		t.Fatalf("explicit pk must upsert, not reassign: %s", s)
	}
	if collCountN(id, "u") != 2 {
		t.Fatalf("count after upsert = %d want 2", collCountN(id, "u"))
	}
}

// Concurrent serial inserts get DISTINCT contiguous ids (seq lock serializes).
func TestCollSerialConcurrent(t *testing.T) {
	id := registerIdxStore(t)
	cols := colListOf([][2]string{{"id", "int!"}})
	const n = 50
	var wg sync.WaitGroup
	ids := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := BlueDB_collPut(id, "u", "", `{"id":0}`, []any{}, cols).(func() any)().(SkyResult[any, any])
			ids[i] = serialIDOf(t, r.OkValue.(string))
		}(i)
	}
	wg.Wait()
	seen := map[int]bool{}
	for _, x := range ids {
		if seen[x] {
			t.Fatalf("duplicate serial id %d in %v", x, ids)
		}
		seen[x] = true
	}
	for i := 1; i <= n; i++ {
		if !seen[i] {
			t.Fatalf("missing serial id %d", i)
		}
	}
}

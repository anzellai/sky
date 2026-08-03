package rt

import (
	"fmt"
	"path/filepath"
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

func putIdx(t *testing.T, id int, pk, json string, fv ...[2]string) {
	t.Helper()
	pairs := []any{}
	for _, p := range fv {
		pairs = append(pairs, SkyTuple2{V0: p[0], V1: p[1]})
	}
	runOK(t, BlueDB_putIndexed(id, pk, json, pairs))
}

func findPks(t *testing.T, id int, field, value string) []string {
	t.Helper()
	r := BlueDB_findByIndex(id, field, value).(func() any)().(SkyResult[any, any])
	out := []string{}
	for _, p := range r.OkValue.([]any) {
		out = append(out, p.(string))
	}
	return out
}

func countIndexEntries(id int, field string) int {
	db := idxDB(id)
	n := 0
	db.Scan([]byte(bluedbReserved+"i\x00"+field+"\x00"), nil, 0, func(_, _ []byte) bool { n++; return true })
	return n
}

func TestIndexPutFindAndStaleRemoval(t *testing.T) {
	id := registerIdxStore(t)
	putIdx(t, id, "u1", `{"email":"ada@x"}`, [2]string{"email", "ada@x"})
	if pks := findPks(t, id, "email", "ada@x"); len(pks) != 1 || pks[0] != "u1" {
		t.Fatalf("find ada: %v", pks)
	}
	// update the indexed field → stale entry must be removed
	putIdx(t, id, "u1", `{"email":"ada2@x"}`, [2]string{"email", "ada2@x"})
	if pks := findPks(t, id, "email", "ada@x"); len(pks) != 0 {
		t.Fatalf("stale index entry must be gone: %v", pks)
	}
	if pks := findPks(t, id, "email", "ada2@x"); len(pks) != 1 || pks[0] != "u1" {
		t.Fatalf("new index entry: %v", pks)
	}
	if n := countIndexEntries(id, "email"); n != 1 {
		t.Fatalf("exactly one email entry expected, got %d", n)
	}
}

func TestIndexNonUniqueMultiplePks(t *testing.T) {
	id := registerIdxStore(t)
	putIdx(t, id, "u1", `{"status":"active"}`, [2]string{"status", "active"})
	putIdx(t, id, "u2", `{"status":"active"}`, [2]string{"status", "active"})
	putIdx(t, id, "u3", `{"status":"idle"}`, [2]string{"status", "idle"})
	pks := findPks(t, id, "status", "active")
	if len(pks) != 2 {
		t.Fatalf("non-unique: want 2 active pks, got %v", pks)
	}
}

func TestIndexDeleteRemovesEntries(t *testing.T) {
	id := registerIdxStore(t)
	putIdx(t, id, "u1", `{"email":"ada@x"}`, [2]string{"email", "ada@x"})
	runOK(t, BlueDB_deleteIndexed(id, "u1", []any{"email"}))
	if pks := findPks(t, id, "email", "ada@x"); len(pks) != 0 {
		t.Fatalf("index entry must be gone after delete: %v", pks)
	}
	if _, ok := idxDB(id).Get([]byte("u1")); ok {
		t.Fatal("pk must be gone after delete")
	}
}

// KEYSTONE (must-fix #1): concurrent updates to the SAME pk must leave exactly ONE
// index entry (the survivor) — no orphaned stale entries. Without the per-pk lock
// held across Get(old)→WriteBatch, this leaves many orphans.
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
			pairs := []any{SkyTuple2{V0: "email", V1: val}}
			BlueDB_putIndexed(id, "u1", j, pairs).(func() any)()
		}(i)
	}
	wg.Wait()
	if got := countIndexEntries(id, "email"); got != 1 {
		t.Fatalf("concurrent same-pk left %d email index entries (want 1) — TOCTOU orphans", got)
	}
}

func TestIndexReservedHiddenFromKeys(t *testing.T) {
	id := registerIdxStore(t)
	putIdx(t, id, "u1", `{"email":"ada@x"}`, [2]string{"email", "ada@x"})
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
	// write records WITHOUT indexing (plain put)
	_ = db.Put([]byte("u1"), []byte(`{"email":"ada@x"}`))
	_ = db.Put([]byte("u2"), []byte(`{"email":"lin@x"}`))
	// backfill the email index
	c := runOK(t, BlueDB_reindex(id, []any{"email"}))
	if c.(int) != 2 {
		t.Fatalf("reindex should backfill 2 records, got %v", c)
	}
	if pks := findPks(t, id, "email", "ada@x"); len(pks) != 1 || pks[0] != "u1" {
		t.Fatalf("backfilled index: %v", pks)
	}
	// manifest guard: re-running with the same declaration is O(1) (count 0)
	c2 := runOK(t, BlueDB_reindex(id, []any{"email"}))
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
	putIdx(t, int(id), "u1", `{"email":"ada@x"}`, [2]string{"email", "ada@x"})
	db.Close()
	bluedbRegMu.Lock()
	delete(bluedbRegistry, id)
	delete(bluedbByPath, path)
	bluedbRegMu.Unlock()

	// reopen: both the primary and the index entry must be present (one batch).
	db2, _ := bluedb.Open(path, bluedb.Options{Sync: false})
	defer db2.Close()
	if _, ok := db2.Get([]byte("u1")); !ok {
		t.Fatal("primary missing after reopen")
	}
	n := 0
	db2.Scan([]byte(bluedbReserved+"i\x00email\x00ada@x\x00"), nil, 0, func(_, _ []byte) bool { n++; return true })
	if n != 1 {
		t.Fatalf("index entry missing after reopen (index+primary not atomic): %d", n)
	}
}

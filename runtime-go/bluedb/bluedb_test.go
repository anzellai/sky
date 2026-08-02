package bluedb

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func open(t *testing.T, dir string, opts ...Options) *DB {
	t.Helper()
	db, err := Open(filepath.Join(dir, "app.blue"), opts...)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return db
}

func mustGet(t *testing.T, db *DB, key string) string {
	t.Helper()
	v, ok := db.Get([]byte(key))
	if !ok {
		t.Fatalf("Get(%q): missing", key)
	}
	return string(v)
}

func TestPutGetDelete(t *testing.T) {
	db := open(t, t.TempDir())
	defer db.Close()

	if err := db.Put([]byte("a"), []byte("1")); err != nil {
		t.Fatal(err)
	}
	if got := mustGet(t, db, "a"); got != "1" {
		t.Fatalf("a = %q, want 1", got)
	}
	if err := db.Put([]byte("a"), []byte("2")); err != nil {
		t.Fatal(err)
	}
	if got := mustGet(t, db, "a"); got != "2" {
		t.Fatalf("a = %q, want 2 (overwrite)", got)
	}
	if err := db.Delete([]byte("a")); err != nil {
		t.Fatal(err)
	}
	if _, ok := db.Get([]byte("a")); ok {
		t.Fatal("a present after delete")
	}
}

// The core durability property: acked writes survive a reopen.
func TestReopenPreservesData(t *testing.T) {
	dir := t.TempDir()
	db := open(t, dir)
	for i := 0; i < 100; i++ {
		if err := db.Put([]byte(fmt.Sprintf("k%03d", i)), []byte(fmt.Sprintf("v%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db2 := open(t, dir)
	defer db2.Close()
	if db2.Len() != 100 {
		t.Fatalf("after reopen Len = %d, want 100", db2.Len())
	}
	for i := 0; i < 100; i++ {
		if got := mustGet(t, db2, fmt.Sprintf("k%03d", i)); got != fmt.Sprintf("v%d", i) {
			t.Fatalf("k%03d = %q", i, got)
		}
	}
	// A write after reopen must continue the seq and persist too.
	if err := db2.Put([]byte("k100"), []byte("v100")); err != nil {
		t.Fatal(err)
	}
	if mustGet(t, db2, "k100") != "v100" {
		t.Fatal("post-reopen write lost")
	}
}

func TestDeleteRecovery(t *testing.T) {
	dir := t.TempDir()
	db := open(t, dir)
	_ = db.Put([]byte("keep"), []byte("y"))
	_ = db.Put([]byte("gone"), []byte("y"))
	_ = db.Delete([]byte("gone"))
	_ = db.Close()

	db2 := open(t, dir)
	defer db2.Close()
	if _, ok := db2.Get([]byte("gone")); ok {
		t.Fatal("deleted key resurrected after reopen")
	}
	if mustGet(t, db2, "keep") != "y" {
		t.Fatal("kept key lost")
	}
}

// A crash that leaves garbage after the last good record (torn tail) must not
// lose the committed prefix, must ignore the garbage, and must leave the file
// clean enough to append to.
func TestTornTailRecovery(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.blue")

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"a", "b", "c"} {
		if err := db.Put([]byte(k), []byte("v")); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Simulate a crash mid-write of a 4th record: append a corrupt/partial tail.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	// A plausible-looking header claiming a big payload, then too few bytes.
	if _, err := f.Write([]byte{0xDE, 0xAD, 0xBE, 0xEF, 0x40, 0x00, 0x00, 0x00, 0x01, 0x02}); err != nil {
		t.Fatal(err)
	}
	f.Close()

	db2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen after torn tail: %v", err)
	}
	defer db2.Close()
	if db2.Len() != 3 {
		t.Fatalf("Len = %d, want 3 (torn tail must be dropped)", db2.Len())
	}
	for _, k := range []string{"a", "b", "c"} {
		if _, ok := db2.Get([]byte(k)); !ok {
			t.Fatalf("committed key %q lost after torn-tail recovery", k)
		}
	}
	// The torn tail must have been truncated: a fresh write must persist.
	if err := db2.Put([]byte("d"), []byte("v")); err != nil {
		t.Fatal(err)
	}
	if err := db2.Close(); err != nil {
		t.Fatal(err)
	}
	db3, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db3.Close()
	if db3.Len() != 4 {
		t.Fatalf("Len = %d, want 4 (append after torn recovery lost)", db3.Len())
	}
}

// A write that was cut off mid-record (partial payload) must be ignored, and
// everything before it preserved.
func TestPartialRecordAtEnd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.blue")

	db, _ := Open(path)
	_ = db.Put([]byte("x"), []byte("1"))
	_ = db.Put([]byte("y"), []byte("2"))
	_ = db.Close()

	// Craft a valid record for "z" then write only the first half of it.
	rec := encodeRecord(entry{seq: 999, op: opPut, key: []byte("z"), value: []byte("3")})
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	_, _ = f.Write(rec[:len(rec)/2]) // truncated mid-record
	f.Close()

	db2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	if db2.Len() != 2 {
		t.Fatalf("Len = %d, want 2 (partial record must be ignored)", db2.Len())
	}
	if _, ok := db2.Get([]byte("z")); ok {
		t.Fatal("partial record was applied")
	}
}

// Concurrent writers must all durably land, and group commit must batch them.
func TestConcurrentGroupCommit(t *testing.T) {
	dir := t.TempDir()
	db := open(t, dir)

	const G, N = 8, 500
	var wg sync.WaitGroup
	wg.Add(G)
	for g := 0; g < G; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < N; i++ {
				k := fmt.Sprintf("g%d-k%04d", g, i)
				if err := db.Put([]byte(k), []byte("v")); err != nil {
					t.Errorf("Put: %v", err)
					return
				}
			}
		}(g)
	}
	wg.Wait()

	if db.Len() != G*N {
		t.Fatalf("Len = %d, want %d", db.Len(), G*N)
	}
	batches, writes := db.Stats()
	if writes != G*N {
		t.Fatalf("writes stat = %d, want %d", writes, G*N)
	}
	// Not a hard assert (a fast disk may not batch), but log the amortization.
	t.Logf("group commit: %d writes across %d fsync batches (avg %.1f writes/fsync)",
		writes, batches, float64(writes)/float64(batches))
	_ = db.Close()

	// Everything survives a reopen.
	db2 := open(t, dir)
	defer db2.Close()
	if db2.Len() != G*N {
		t.Fatalf("after reopen Len = %d, want %d", db2.Len(), G*N)
	}
}

func TestNoSyncModeStillReopens(t *testing.T) {
	dir := t.TempDir()
	db := open(t, dir, Options{Sync: false})
	for i := 0; i < 50; i++ {
		_ = db.Put([]byte(fmt.Sprintf("k%d", i)), []byte("v"))
	}
	_ = db.Close() // graceful close flushes buffered writes to the OS

	db2 := open(t, dir)
	defer db2.Close()
	if db2.Len() != 50 {
		t.Fatalf("Len = %d, want 50", db2.Len())
	}
}

func TestWriteAfterCloseFails(t *testing.T) {
	db := open(t, t.TempDir())
	_ = db.Close()
	if err := db.Put([]byte("a"), []byte("1")); err != ErrClosed {
		t.Fatalf("Put after close: err = %v, want ErrClosed", err)
	}
}

package bluedb

import (
	"path/filepath"
	"testing"
)

func openTmpForScan(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "s.blue"), Options{Sync: false})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func collectScan(db *DB, prefix, after string, limit int) []string {
	var got []string
	db.Scan([]byte(prefix), []byte(after), limit, func(k, _ []byte) bool {
		got = append(got, string(k))
		return true
	})
	return got
}

func TestScanIsSortedAndPrefixIsolated(t *testing.T) {
	db := openTmpForScan(t)
	// Insert in NON-sorted order across two prefixes.
	for _, k := range []string{"user:3", "order:1", "user:1", "user:10", "order:2", "user:2"} {
		if err := db.Put([]byte(k), []byte("v-"+k)); err != nil {
			t.Fatal(err)
		}
	}
	got := collectScan(db, "user:", "", 0)
	want := []string{"user:1", "user:10", "user:2", "user:3"} // lexicographic, deterministic
	if len(got) != len(want) {
		t.Fatalf("prefix scan len: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("prefix scan order at %d: got %v want %v", i, got, want)
		}
	}
}

func TestScanLimitAndCursorPaginate(t *testing.T) {
	db := openTmpForScan(t)
	for _, k := range []string{"k:a", "k:b", "k:c", "k:d", "k:e"} {
		_ = db.Put([]byte(k), []byte("x"))
	}
	// Page 1: limit 2.
	p1 := collectScan(db, "k:", "", 2)
	if len(p1) != 2 || p1[0] != "k:a" || p1[1] != "k:b" {
		t.Fatalf("page1: %v", p1)
	}
	// Page 2: startAfter last of page 1.
	p2 := collectScan(db, "k:", p1[len(p1)-1], 2)
	if len(p2) != 2 || p2[0] != "k:c" || p2[1] != "k:d" {
		t.Fatalf("page2: %v", p2)
	}
	// Page 3: exhausts.
	p3 := collectScan(db, "k:", p2[len(p2)-1], 2)
	if len(p3) != 1 || p3[0] != "k:e" {
		t.Fatalf("page3: %v", p3)
	}
	// No pages beyond.
	p4 := collectScan(db, "k:", p3[len(p3)-1], 2)
	if len(p4) != 0 {
		t.Fatalf("page4 should be empty: %v", p4)
	}
}

func TestScanEarlyStopAndDeleteSkipped(t *testing.T) {
	db := openTmpForScan(t)
	for _, k := range []string{"p:1", "p:2", "p:3"} {
		_ = db.Put([]byte(k), []byte("v"))
	}
	if err := db.Delete([]byte("p:2")); err != nil {
		t.Fatal(err)
	}
	got := collectScan(db, "p:", "", 0)
	if len(got) != 2 || got[0] != "p:1" || got[1] != "p:3" {
		t.Fatalf("deleted key must not appear: %v", got)
	}
	// Early stop via fn returning false.
	var seen int
	db.Scan([]byte("p:"), nil, 0, func(_, _ []byte) bool { seen++; return false })
	if seen != 1 {
		t.Fatalf("early-stop should visit exactly 1, saw %d", seen)
	}
}

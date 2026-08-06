package bluedb

import (
	"testing"

	"github.com/cockroachdb/pebble/v2"
)

// TestSecondOpenFailsSingleProcessLock verifies Pebble provides the single-writer
// directory lock: it acquires an exclusive OS file lock (a LOCK file, flock on unix)
// on Open, so a second Open of the SAME directory fails — including from the same
// process (a distinct fd → EAGAIN). BlueDB relies on this rather than reinventing a
// flock (design §6 / phase1-status).
func TestSecondOpenFailsSingleProcessLock(t *testing.T) {
	dir := t.TempDir()
	e1, err := Open(dir)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	defer e1.Close()

	e2, err := Open(dir)
	if err == nil {
		_ = e2.Close()
		t.Fatalf("second Open of the same directory must FAIL — Pebble holds an exclusive lock")
	}
}

// TestWrongComparerNameRefusesOpen is the comparer-immutability gate (§7 G1, §2.4). A
// store created under Name "skydb.mvcc.v1" refuses to open under any other comparer
// name — the cheapest insurance against the irreversible format bug.
func TestWrongComparerNameRefusesOpen(t *testing.T) {
	dir := t.TempDir()
	e, err := openWith(config{dir: dir})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if r := e.Commit(CommitReq{Writes: []VersionedWrite{{UserKey: []byte("K"), Op: OpPut, Value: []byte("v")}}}); r.Err != nil {
		t.Fatalf("commit: %v", r.Err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	wrong := *skydbComparer
	wrong.Name = "skydb.mvcc.v2-INCOMPATIBLE"
	db, err := pebble.Open(dir, &pebble.Options{Comparer: &wrong, Logger: quietLogger{}})
	if err == nil {
		_ = db.Close()
		t.Fatalf("opening under a different Comparer.Name must be REFUSED")
	}
}

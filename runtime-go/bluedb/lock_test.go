package bluedb

import (
	"strings"
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
	// WHY it was refused, not merely THAT it was. A bare `err != nil` is green for
	// the wrong reason by construction: leak the Pebble handle in Close and this
	// second Open fails on the DIRECTORY LOCK instead, the comparer check is never
	// reached, and the fixture reports PASS having exercised nothing. The manifest
	// check names both comparers, so the error text distinguishes the two refusals.
	if got := err.Error(); !strings.Contains(got, "comparer name") ||
		!strings.Contains(got, skydbComparer.Name) || !strings.Contains(got, wrong.Name) {
		t.Fatalf("the second Open was refused, but NOT by the comparer check: %v.\n"+
			"Want an error naming `comparer name` and both %q (from the manifest) and %q (from "+
			"Options). An open refused for any other reason — the directory lock a leaked handle "+
			"leaves behind, a missing manifest — leaves the comparer-immutability contract (§7 G1, "+
			"§2.4) UNEXERCISED while this test stays green",
			err, skydbComparer.Name, wrong.Name)
	}
}

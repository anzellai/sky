package rt

import (
	"path/filepath"
	"testing"

	"sky-app/bluedb"
)

// storePath returns the on-disk WAL path of a registered store handle.
func storePath(id int) string {
	bluedbRegMu.Lock()
	defer bluedbRegMu.Unlock()
	return bluedbRegistry[int64(id)].path
}

// TestBlueDBBackupKernelRoundTripAndVerify: BlueDB_backup writes an openable,
// clean-verifying copy that round-trips every key/value.
func TestBlueDBBackupKernelRoundTripAndVerify(t *testing.T) {
	id := registerIdxStore(t)
	for _, kv := range [][2]string{{"a", "1"}, {"b", "2"}, {"c", "3"}} {
		runOK(t, BlueDB_put(id, kv[0], kv[1]))
	}

	dest := filepath.Join(filepath.Dir(storePath(id)), "bak.blue")
	runOK(t, BlueDB_backup(id, dest))

	rep, err := bluedb.Verify(dest)
	if err != nil {
		t.Fatalf("Verify(dest): %v", err)
	}
	if !rep.OK {
		t.Fatalf("backup Verify not OK: %+v", rep)
	}

	bak, err := bluedb.Open(dest)
	if err != nil {
		t.Fatalf("Open(dest): %v", err)
	}
	defer bak.Close()
	for _, kv := range [][2]string{{"a", "1"}, {"b", "2"}, {"c", "3"}} {
		v, ok := bak.Get([]byte(kv[0]))
		if !ok || string(v) != kv[1] {
			t.Fatalf("backup %s = %q (present=%v), want %q", kv[0], string(v), ok, kv[1])
		}
	}
}

// TestBlueDBBackupKernelErrors: empty dest and a missing store handle both return
// Err from the kernel.
func TestBlueDBBackupKernelErrors(t *testing.T) {
	id := registerIdxStore(t)
	runBatchErr(t, BlueDB_backup(id, "")) // empty dest → Err
	runBatchErr(t, BlueDB_backup(999999, filepath.Join(t.TempDir(), "x.blue")))
	// A self-clobber dest (the live WAL path) is rejected by the engine → Err.
	runBatchErr(t, BlueDB_backup(id, storePath(id)))
}

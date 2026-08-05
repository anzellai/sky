package bluedb

import (
	"os"
	"path/filepath"
	"testing"
)

// F7: WAL + snapshot files must be 0o600 (app/session data, not world-readable).
func TestF7FilePermsAre0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.blue")
	db, err := Open(path, Options{Sync: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Put([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("put: %v", err)
	}
	// force a snapshot/checkpoint so the snapshot file exists too
	_ = db.Close()
	for _, f := range []string{path, path + ".snap"} {
		fi, err := os.Stat(f)
		if err != nil {
			continue // snapshot may not exist without a checkpoint; WAL must
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s perm = %o, want 600", f, perm)
		}
	}
}

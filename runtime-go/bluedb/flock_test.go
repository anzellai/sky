//go:build unix

package bluedb

import (
	"path/filepath"
	"testing"
)

// A second open of the same path must be refused — two engines on one WAL would
// corrupt it. After the first closes (releasing the advisory lock), a fresh open
// succeeds.
func TestOpenLockRejectsSecondOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.blue")
	db1, err := Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if _, err := Open(path); err != ErrLocked {
		db1.Close()
		t.Fatalf("second open: err=%v want ErrLocked", err)
	}
	if err := db1.Close(); err != nil {
		t.Fatal(err)
	}
	db2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen after close: %v", err)
	}
	db2.Close()
}

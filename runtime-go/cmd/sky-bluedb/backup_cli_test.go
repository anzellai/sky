package main

import (
	"path/filepath"
	"strings"
	"testing"

	"sky-app/bluedb"
)

// TestCLIBackup: `bluedb <path> backup <dest>` on a populated (closed) store
// exits 0, prints the "backed up" line, and produces a clean, openable copy that
// holds the data.
func TestCLIBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.blue")
	runCLI(t, "", path, "put", "user:1", `{"name":"Ada"}`)
	runCLI(t, "", path, "put", "user:2", `{"name":"Lin"}`)

	dest := filepath.Join(dir, "backups", "app.blue")
	code, out, errs := runCLI(t, "", path, "backup", dest)
	if code != 0 {
		t.Fatalf("backup: code=%d err=%s", code, errs)
	}
	if !strings.Contains(out, "backed up") || !strings.Contains(out, dest) {
		t.Fatalf("backup output = %q, want a 'backed up ... -> <dest>' line", out)
	}

	// The backup verifies clean and Open-able.
	rep, err := bluedb.Verify(dest)
	if err != nil || !rep.OK {
		t.Fatalf("Verify(dest): err=%v ok=%v (%+v)", err, rep.OK, rep)
	}
	bak, err := bluedb.Open(dest)
	if err != nil {
		t.Fatalf("Open(dest): %v", err)
	}
	defer bak.Close()
	if v, ok := bak.Get([]byte("user:1")); !ok || string(v) != `{"name":"Ada"}` {
		t.Fatalf("backup user:1 = %q (present=%v)", string(v), ok)
	}
	if bak.Len() != 2 {
		t.Fatalf("backup has %d keys, want 2", bak.Len())
	}
}

// TestCLIBackupNeedsDest: `backup` with no <dest> exits non-zero.
func TestCLIBackupNeedsDest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.blue")
	runCLI(t, "", path, "put", "k", "v")
	if code, _, _ := runCLI(t, "", path, "backup"); code == 0 {
		t.Fatal("backup with no <dest> should exit non-zero")
	}
}

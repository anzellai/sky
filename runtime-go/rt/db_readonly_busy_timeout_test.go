//go:build !js

package rt

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// With SKY_READ_ONLY_DB=1 (`sky db check`, `sky db status`) the SQLite
// PRAGMAs that write (journal_mode=WAL) are skipped. busy_timeout used to go
// with them, so a check against a database another process was writing
// failed at once with "database is locked" instead of waiting (v0.29.2 third
// Judge). busy_timeout reads nothing and writes nothing, so a read-only
// connection keeps it.
func TestDbConnectReadOnlyKeepsBusyTimeout(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "busy.db")
	w, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	// The default rollback journal: an EXCLUSIVE lock blocks readers too.
	if _, err := w.Exec(`CREATE TABLE t (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec(`INSERT INTO t VALUES ('a')`); err != nil {
		t.Fatal(err)
	}

	t.Setenv("SKY_READ_ONLY_DB", "1")
	db := unwrapDbConnect(t, dbPath)
	defer func() {
		_ = db.conn.Close()
		dbRegistryMu.Lock()
		delete(dbRegistry, dbPath)
		dbRegistryMu.Unlock()
	}()

	var timeout string
	if err := db.conn.QueryRow("PRAGMA busy_timeout").Scan(&timeout); err != nil {
		t.Fatalf("PRAGMA busy_timeout: %v", err)
	}
	if timeout != "5000" {
		t.Fatalf("busy_timeout on a read-only connection = %q, want 5000", timeout)
	}

	ctx := context.Background()
	lock, err := w.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := lock.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	if _, err := lock.ExecContext(ctx, `INSERT INTO t VALUES ('b')`); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		_, _ = lock.ExecContext(ctx, "COMMIT")
	}()
	var n int
	if err := db.conn.QueryRow("SELECT count(*) FROM t").Scan(&n); err != nil {
		t.Fatalf("a read while another connection writes failed instead of waiting: %v", err)
	}
	if n != 2 {
		t.Fatalf("count = %d, want 2 (the read waited for the commit)", n)
	}
}

// `sky db status` on a project with no db/migrations runs the app's main with
// SKY_READ_ONLY_DB=1. An app that writes before Db.migrate used to fail there
// with only "attempt to write a readonly database" (v0.29.2 third Judge). The
// Err now names the read-only mode and what to do.
func TestReadOnlyWriteErrorNamesStatusModeAndTheFix(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ro.db")
	w, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec(`CREATE TABLE boot_log (at TEXT)`); err != nil {
		t.Fatal(err)
	}
	w.Close()

	t.Setenv("SKY_READ_ONLY_DB", "status")
	db := unwrapDbConnect(t, dbPath)
	defer func() {
		_ = db.conn.Close()
		dbRegistryMu.Lock()
		delete(dbRegistry, dbPath)
		dbRegistryMu.Unlock()
	}()
	res := Db_exec(db, "INSERT INTO boot_log VALUES ('now')", []any{}).(func() any)()
	r, ok := res.(SkyResult[any, any])
	if !ok || r.Tag == 0 {
		t.Fatalf("a write on a read-only database succeeded: %v", res)
	}
	msg := errorMessage(r.ErrValue)
	for _, want := range []string{
		"attempt to write a readonly database",
		"SKY_READ_ONLY_DB=status",
		"`sky db status`",
		"Move that write after Db.migrate",
		"file migrations",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("the Err does not say %q: %s", want, msg)
		}
	}

	// `sky db check` runs no app code: its hint says what the check does,
	// never the status advice about a write before Db.migrate (v0.29.2
	// fifth Judge).
	t.Setenv("SKY_READ_ONLY_DB", "check")
	chk := withReadOnlyDbHint("db.query: attempt to write a readonly database (1544)")
	if !strings.Contains(chk, "`sky db check`") || !strings.Contains(chk, "-wal") || strings.Contains(chk, "Db.migrate") {
		t.Fatalf("the check hint is wrong: %s", chk)
	}

	// Outside read-only mode the text of an I/O error is unchanged.
	t.Setenv("SKY_READ_ONLY_DB", "")
	if got := withReadOnlyDbHint("db.exec: attempt to write a readonly database (8)"); got != "db.exec: attempt to write a readonly database (8)" {
		t.Fatalf("the hint was added outside read-only mode: %s", got)
	}
}

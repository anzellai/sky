package rt

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// TestDbWithTransactionCommitRollback locks the two historical
// withTransaction defects (anzellai/sky, DarraghStudio report):
//
//  1. the body was applied via a raw `.(func(any) any)` assertion that never
//     matched a compiled Sky closure → "body is not a function";
//  2. even when callable, the body ran against the pool, not the tx, so a
//     rollback rolled back nothing.
//
// The body here is a plain `func(any) any` (sky_call is reflect-based, so it
// applies both that and real Sky closures identically) that writes THROUGH the
// handle it is given. If the handle is tx-scoped, a rollback must erase the
// write.
func TestDbWithTransactionCommitRollback(t *testing.T) {
	dir := t.TempDir()
	conn, err := sql.Open("sqlite", filepath.Join(dir, "tx.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer conn.Close()
	db := &SkyDb{conn: conn, name: "test", driver: "sqlite"}
	if _, err := conn.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}

	count := func() int {
		var n int
		if err := conn.QueryRow(`SELECT count(*) FROM items`).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}

	// A body that inserts one row THROUGH the handle it's handed, then yields
	// the given final task (Ok to commit, an Err task to roll back).
	insertThen := func(name string, final any) func(any) any {
		return func(txdb any) any {
			return func() any {
				// Run the insert on whatever handle withTransaction passed in.
				res := AnyTaskRun(Db_exec(txdb, "INSERT INTO items (name) VALUES (?)", []any{name}))
				if sr, ok := res.(SkyResult[any, any]); !ok || sr.Tag != 0 {
					return Err[any, any](ErrUnexpected("insert failed inside tx"))
				}
				return final
			}
		}
	}

	// Commit path — body returns Ok → COMMIT → row persists.
	okRes := AnyTaskRun(Db_withTransaction(db, insertThen("a", Ok[any, any](int64(1)))))
	if sr, ok := okRes.(SkyResult[any, any]); !ok || sr.Tag != 0 {
		t.Fatalf("commit path: want Ok, got %v", okRes)
	}
	if n := count(); n != 1 {
		t.Fatalf("after commit: %d rows, want 1", n)
	}

	// Rollback path — body returns Err → ROLLBACK → the 'b' insert is undone.
	errRes := AnyTaskRun(Db_withTransaction(db, insertThen("b", Err[any, any](ErrUnexpected("boom")))))
	if sr, ok := errRes.(SkyResult[any, any]); !ok || sr.Tag != 1 {
		t.Fatalf("rollback path: want Err, got %v", errRes)
	}
	if n := count(); n != 1 {
		t.Fatalf("after rollback: %d rows, want 1 ('b' must be rolled back — body wrote inside the tx)", n)
	}
}

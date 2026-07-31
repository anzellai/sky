package rt

// db_reset.go end-to-end tests — `sky db reset` / `sky db drop` kernels.
//
// Builds a SQLite *SkyDb with two tables joined by a FK, one carrying an
// AUTOINCREMENT pk, inserts rows, then asserts:
//   - Db_resetTables empties both + resets the autoinc counter (schema stays)
//   - Db_dropTables removes them (introspection confirms absence)

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func openResetTestDb(t *testing.T) *SkyDb {
	t.Helper()
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := conn.Exec(`CREATE TABLE authors (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create authors: %v", err)
	}
	if _, err := conn.Exec(`CREATE TABLE books (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		title TEXT NOT NULL,
		author_id INTEGER REFERENCES authors(id)
	)`); err != nil {
		t.Fatalf("create books: %v", err)
	}
	return &SkyDb{conn: conn, driver: "sqlite"}
}

func skyList(names ...string) any {
	out := make([]any, len(names))
	for i, n := range names {
		out[i] = n
	}
	return out
}

func countRows(t *testing.T, db *SkyDb, table string) int {
	t.Helper()
	var n int
	if err := db.conn.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func tableExistsSQLite(t *testing.T, db *SkyDb, table string) bool {
	t.Helper()
	var name string
	err := db.conn.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
	).Scan(&name)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatalf("sqlite_master lookup %s: %v", table, err)
	}
	return name == table
}

func seedResetDb(t *testing.T, db *SkyDb) {
	t.Helper()
	if _, err := db.conn.Exec(`INSERT INTO authors (name) VALUES ('Ada'), ('Grace')`); err != nil {
		t.Fatalf("seed authors: %v", err)
	}
	if _, err := db.conn.Exec(`INSERT INTO books (title, author_id) VALUES ('Notes', 1), ('Compiler', 2)`); err != nil {
		t.Fatalf("seed books: %v", err)
	}
}

func TestDb_ResetTables_EmptiesAndResetsAutoinc(t *testing.T) {
	db := openResetTestDb(t)
	seedResetDb(t, db)

	if got := countRows(t, db, "authors"); got != 2 {
		t.Fatalf("pre-reset authors count = %d, want 2", got)
	}

	r := runTask(t, Db_resetTables(db, skyList("books", "authors")))
	if r.Tag != 0 {
		t.Fatalf("Db_resetTables returned Err: %+v", r.ErrValue)
	}

	// Both tables empty.
	if got := countRows(t, db, "authors"); got != 0 {
		t.Fatalf("post-reset authors count = %d, want 0", got)
	}
	if got := countRows(t, db, "books"); got != 0 {
		t.Fatalf("post-reset books count = %d, want 0", got)
	}
	// Schema still present.
	if !tableExistsSQLite(t, db, "authors") || !tableExistsSQLite(t, db, "books") {
		t.Fatal("reset dropped a table; schema must be preserved")
	}
	// Autoinc reset: next insert starts at id 1 again.
	if _, err := db.conn.Exec(`INSERT INTO authors (name) VALUES ('Linus')`); err != nil {
		t.Fatalf("post-reset insert: %v", err)
	}
	var id int
	if err := db.conn.QueryRow(`SELECT id FROM authors WHERE name='Linus'`).Scan(&id); err != nil {
		t.Fatalf("read new id: %v", err)
	}
	if id != 1 {
		t.Fatalf("autoinc not reset: new id = %d, want 1", id)
	}
}

func TestDb_ResetTables_SingleTable(t *testing.T) {
	db := openResetTestDb(t)
	seedResetDb(t, db)

	// Reset only books (FK child) — authors untouched.
	r := runTask(t, Db_resetTables(db, skyList("books")))
	if r.Tag != 0 {
		t.Fatalf("Db_resetTables single returned Err: %+v", r.ErrValue)
	}
	if got := countRows(t, db, "books"); got != 0 {
		t.Fatalf("books count = %d, want 0", got)
	}
	if got := countRows(t, db, "authors"); got != 2 {
		t.Fatalf("authors count = %d, want 2 (untouched)", got)
	}
}

func TestDb_ResetTables_SkipsNonExistent(t *testing.T) {
	db := openResetTestDb(t)
	seedResetDb(t, db)

	// "ghosts" doesn't exist → silently skipped; authors still reset.
	r := runTask(t, Db_resetTables(db, skyList("ghosts", "authors")))
	if r.Tag != 0 {
		t.Fatalf("Db_resetTables returned Err on non-existent table: %+v", r.ErrValue)
	}
	if got := countRows(t, db, "authors"); got != 0 {
		t.Fatalf("authors count = %d, want 0", got)
	}
}

func TestDb_ResetTables_RejectsBadIdent(t *testing.T) {
	db := openResetTestDb(t)
	r := runTask(t, Db_resetTables(db, skyList("authors; DROP TABLE books")))
	if r.Tag == 0 {
		t.Fatal("expected Err for invalid identifier, got Ok")
	}
}

func TestDb_DropTables_RemovesTables(t *testing.T) {
	db := openResetTestDb(t)
	seedResetDb(t, db)

	r := runTask(t, Db_dropTables(db, skyList("books", "authors")))
	if r.Tag != 0 {
		t.Fatalf("Db_dropTables returned Err: %+v", r.ErrValue)
	}
	if tableExistsSQLite(t, db, "authors") {
		t.Fatal("authors still exists after drop")
	}
	if tableExistsSQLite(t, db, "books") {
		t.Fatal("books still exists after drop")
	}
}

func TestDb_DropTables_IfExistsNoError(t *testing.T) {
	db := openResetTestDb(t)
	// Dropping a non-existent table is a no-op (IF EXISTS).
	r := runTask(t, Db_dropTables(db, skyList("nope")))
	if r.Tag != 0 {
		t.Fatalf("Db_dropTables on missing table returned Err: %+v", r.ErrValue)
	}
}

func TestDb_DropTables_RejectsBadIdent(t *testing.T) {
	db := openResetTestDb(t)
	r := runTask(t, Db_dropTables(db, skyList("books\"; DROP")))
	if r.Tag == 0 {
		t.Fatal("expected Err for invalid identifier, got Ok")
	}
}

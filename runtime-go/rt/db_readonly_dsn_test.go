//go:build !js

package rt

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// v0.29.2 fourth Judge, gap 1: a SQLite `file:` DSN kept its own `mode`, so
// SKY_DB_PATH="file:st.db?mode=rwc" made `sky db status` create the file and
// run writes, and `sky db check` create an empty database. Read-only mode now
// replaces any mode with ro and adds query_only, and a missing file is refused
// for every spelling of the DSN.

func readOnlyConnectErr(t *testing.T, dsn string) string {
	t.Helper()
	res := Db_connect(dsn).(func() any)()
	r, ok := res.(SkyResult[any, any])
	if !ok {
		t.Fatalf("Db_connect(%q): unexpected %T", dsn, res)
	}
	if r.Tag == 0 {
		db := r.OkValue.(*SkyDb)
		_ = db.conn.Close()
		dbRegistryMu.Lock()
		delete(dbRegistry, dsn)
		dbRegistryMu.Unlock()
		return ""
	}
	return errorMessage(r.ErrValue)
}

func TestReadOnlySqliteMissingFileRefusedForEveryDsnSpelling(t *testing.T) {
	t.Setenv("SKY_READ_ONLY_DB", "1")
	dir := t.TempDir()
	t.Chdir(dir)
	for _, dsn := range []string{
		"missing.db",
		filepath.Join(dir, "abs-missing.db"),
		"file:missing.db",
		"file:missing.db?mode=rwc",
		"file:missing.db?mode=rw",
		"file:missing.db?cache=shared&mode=rwc",
		"file:missing.db?MODE=RWC",
		"file:" + filepath.Join(dir, "abs-missing.db") + "?mode=rwc",
		"file://" + filepath.Join(dir, "abs-missing.db") + "?mode=rwc",
		"file:miss%69ng.db?mode=rwc",
	} {
		msg := readOnlyConnectErr(t, dsn)
		if !strings.Contains(msg, "does not exist") || !strings.Contains(msg, "SKY_READ_ONLY_DB is set") {
			t.Errorf("%s: a missing database was not refused: %q", dsn, msg)
		}
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		t.Errorf("read-only connect created %s", e.Name())
	}
}

func TestReadOnlySqliteFileDsnModeIsReplacedAndWritesFail(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	w, err := sql.Open("sqlite", "st.db")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec(`CREATE TABLE t (id TEXT)`); err != nil {
		t.Fatal(err)
	}
	w.Close()

	t.Setenv("SKY_READ_ONLY_DB", "1")
	for _, dsn := range []string{
		"st.db",
		"file:st.db",
		"file:st.db?mode=rwc",
		"file:st.db?mode=rw&cache=shared",
		"file:st.db?mode=rw#frag",
	} {
		ro, msg := dbSqliteReadOnlyDSN(dsn)
		if msg != "" {
			t.Fatalf("%s: %s", dsn, msg)
		}
		if strings.Contains(ro, "mode=rw") || !strings.Contains(ro, "mode=ro") || !strings.Contains(ro, "query_only(1)") {
			t.Errorf("%s: read-only DSN %q keeps a write mode or lacks a guard", dsn, ro)
		}
		db := unwrapDbConnect(t, dsn)
		for _, stmt := range []string{"INSERT INTO t VALUES ('a')", "CREATE TABLE u (x INT)"} {
			if _, err := db.conn.Exec(stmt); err == nil || !strings.Contains(err.Error(), "readonly") {
				t.Errorf("%s: %s was not refused: %v", dsn, stmt, err)
			}
		}
		var q string
		if err := db.conn.QueryRow("PRAGMA query_only").Scan(&q); err != nil || q != "1" {
			t.Errorf("%s: query_only = %q (%v), want 1", dsn, q, err)
		}
		_ = db.conn.Close()
		dbRegistryMu.Lock()
		delete(dbRegistry, dsn)
		dbRegistryMu.Unlock()
	}
	var n int
	r, _ := sql.Open("sqlite", "st.db")
	defer r.Close()
	if err := r.QueryRow("SELECT count(*) FROM t").Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows after read-only writes = %d (%v), want 0", n, err)
	}
}

// An in-memory database has no file to protect: it opens, and query_only
// still refuses writes.
func TestReadOnlySqliteMemoryDsnKeepsMemoryMode(t *testing.T) {
	for _, dsn := range []string{":memory:", "file::memory:", "file:x?mode=memory"} {
		ro, msg := dbSqliteReadOnlyDSN(dsn)
		if msg != "" {
			t.Fatalf("%s: refused: %s", dsn, msg)
		}
		if !strings.Contains(ro, "query_only(1)") || strings.Contains(ro, "mode=ro") {
			t.Errorf("%s: %q", dsn, ro)
		}
	}
}

// v0.29.2 fifth Judge, gap 5: a plain-path DSN with parameters
// (`w/x.db?_pragma=foreign_keys(1)`) opens fine in the app, because the
// driver reads the path before the first `?`. Read-only mode stat'ed the
// whole string and refused it as a missing file.
func TestReadOnlySqlitePlainPathWithParamsOpens(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.Mkdir("w", 0o755); err != nil {
		t.Fatal(err)
	}
	w, err := sql.Open("sqlite", "w/x.db")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec(`CREATE TABLE t (id TEXT); INSERT INTO t VALUES ('a')`); err != nil {
		t.Fatal(err)
	}
	w.Close()
	t.Setenv("SKY_READ_ONLY_DB", "check")
	dsn := "w/x.db?_pragma=foreign_keys(1)&mode=rwc"
	ro, msg := dbSqliteReadOnlyDSN(dsn)
	if msg != "" {
		t.Fatalf("%s refused: %s", dsn, msg)
	}
	if strings.Contains(ro, "mode=rwc") || !strings.Contains(ro, "foreign_keys(1)") || !strings.Contains(ro, "mode=ro") {
		t.Fatalf("read-only DSN %q", ro)
	}
	db := unwrapDbConnect(t, dsn)
	defer func() {
		_ = db.conn.Close()
		dbRegistryMu.Lock()
		delete(dbRegistry, dsn)
		dbRegistryMu.Unlock()
	}()
	var n int
	if err := db.conn.QueryRow("SELECT count(*) FROM t").Scan(&n); err != nil || n != 1 {
		t.Fatalf("count = %d (%v)", n, err)
	}
	var fk string
	if err := db.conn.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil || fk != "1" {
		t.Fatalf("foreign_keys = %q (%v): the DSN's own parameter was dropped", fk, err)
	}
	// A missing file under the same spelling is still refused.
	if _, msg := dbSqliteReadOnlyDSN("w/missing.db?_pragma=foreign_keys(1)"); !strings.Contains(msg, "does not exist") {
		t.Fatalf("a missing file with parameters was not refused: %q", msg)
	}
}

// v0.29.2 fifth Judge, gap 3: Db.connect puts a SQLite database in WAL mode,
// and a read-only open of a WAL database created `x.db-wal` and `x.db-shm`
// beside it; in a read-only directory it failed with "attempt to write a
// readonly database (1544)". A read-only open now creates no file, reads in a
// read-only directory, and reads a live writer's committed rows through the
// `-wal` and `-shm` that writer keeps.
func TestReadOnlySqliteWalDatabaseCreatesNoFilesAndReadsInAReadOnlyDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "x.db")
	w, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"PRAGMA journal_mode=WAL", "CREATE TABLE t (id TEXT)", "INSERT INTO t VALUES ('a')"} {
		if _, err := w.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	w.Close() // the last connection checkpoints and deletes -wal and -shm
	if !dbSqliteIsWAL(dbPath) {
		t.Fatal("the database is not in WAL mode")
	}
	names := func() []string {
		es, _ := os.ReadDir(dir)
		out := []string{}
		for _, e := range es {
			out = append(out, e.Name())
		}
		return out
	}
	count := func(label string) int {
		t.Helper()
		db := unwrapDbConnect(t, dbPath)
		defer func() {
			_ = db.conn.Close()
			dbRegistryMu.Lock()
			delete(dbRegistry, dbPath)
			dbRegistryMu.Unlock()
		}()
		var n int
		if err := db.conn.QueryRow("SELECT count(*) FROM t").Scan(&n); err != nil {
			t.Fatalf("%s: read failed: %v", label, err)
		}
		return n
	}
	t.Setenv("SKY_READ_ONLY_DB", "check")

	// 1. No writer, writable directory: no file is created.
	if n := count("no writer"); n != 1 {
		t.Fatalf("no writer: count = %d", n)
	}
	if got := names(); len(got) != 1 {
		t.Fatalf("a read-only open created files: %v", got)
	}

	// 2. No writer, read-only directory: the read works.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o755)
	if n := count("read-only dir"); n != 1 {
		t.Fatalf("read-only dir: count = %d", n)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	// 3. A live writer holds rows in its -wal: the read sees them (immutable
	// would not), and with the directory read-only it still works.
	t.Setenv("SKY_READ_ONLY_DB", "")
	lw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer lw.Close()
	lw.SetMaxOpenConns(1)
	for _, s := range []string{"PRAGMA journal_mode=WAL", "PRAGMA wal_autocheckpoint=0", "INSERT INTO t VALUES ('b')"} {
		if _, err := lw.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(dbPath + "-wal"); err != nil {
		t.Fatalf("the live writer has no -wal: %v", err)
	}
	t.Setenv("SKY_READ_ONLY_DB", "check")
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	if n := count("live writer, read-only dir"); n != 2 {
		t.Fatalf("live writer: count = %d, want 2 (the row in the -wal)", n)
	}
}

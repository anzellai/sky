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
		if !strings.Contains(msg, "does not exist") || !strings.Contains(msg, "SKY_READ_ONLY_DB=1") {
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

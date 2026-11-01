//go:build !js

package rt

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSqliteMarksSkipQuotesAndComments(t *testing.T) {
	q := `SELECT '?' AS a, "?" AS b FROM t /* ? */ WHERE x = ? AND f = ?/*sky:t|f*/ -- ?
AND r IN (?/*sky:t|r*/, ?)`
	got := sqliteMarks(q)
	want := map[int]string{1: "t|f", 2: "t|r"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("marks = %v, want %v", got, want)
	}
}

// A marked placeholder of a column that is not TEXT binds the value as it is,
// and a schema change in this process is seen by the next statement.
func TestSqliteMarkedParamFollowsTheDeclaredType(t *testing.T) {
	conn, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "marks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	d := &SkyDb{conn: conn, driver: "sqlite"}
	exec := func(q string, args ...any) {
		t.Helper()
		if r, _ := AnyTaskRun(Db_exec(d, q, args)).(SkyResult[any, any]); r.Tag != 0 {
			t.Fatalf("%s: %v", q, r.ErrValue)
		}
	}
	stored := func(id string) string {
		t.Helper()
		var s string
		if err := conn.QueryRow("SELECT typeof(b) || ':' || b FROM m WHERE id = ?", id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	exec("CREATE TABLE m (id TEXT PRIMARY KEY, b BOOLEAN)")
	exec("INSERT INTO m (id, b) VALUES ('x', ?/*sky:m|b*/)", true)
	if got := stored("x"); got != "integer:1" {
		t.Fatalf("a BOOLEAN column stores %s, want integer:1", got)
	}
	// Rebuild the table with a TEXT column, as v0.28 made it.
	exec("DROP TABLE m")
	exec("CREATE TABLE m (id TEXT PRIMARY KEY, b TEXT)")
	exec("INSERT INTO m (id, b) VALUES ('y', ?/*sky:m|b*/)", true)
	if got := stored("y"); got != "text:true" {
		t.Fatalf("a TEXT column stores %s, want text:true", got)
	}
}

// openLegacyFileDb opens a SQLite pool on `path` (one file, several pools).
func openLegacyFileDb(t *testing.T, path string) *SkyDb {
	t.Helper()
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return &SkyDb{conn: conn, driver: "sqlite"}
}

func legacyExec(t *testing.T, d *SkyDb, q string, args ...any) {
	t.Helper()
	if r, _ := AnyTaskRun(Db_exec(d, q, args)).(SkyResult[any, any]); r.Tag != 0 {
		t.Fatalf("%s: %v", q, r.ErrValue)
	}
}

func legacyStored(t *testing.T, d *SkyDb, table, id string) string {
	t.Helper()
	var s string
	if err := d.conn.QueryRow("SELECT typeof(b) || ':' || b FROM "+table+" WHERE id = ?", id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// Finding 3 (v0.29.1): another process (here another pool on the same file)
// converts the v0.28 TEXT column. The first pool's cached declared type is
// keyed on PRAGMA schema_version, so its next marked statement reads the new
// type and binds the value as it is.
func TestSqliteDeclCacheFollowsSchemaVersionAcrossPools(t *testing.T) {
	path := filepath.Join(t.TempDir(), "two-pools.db")
	a := openLegacyFileDb(t, path)
	b := openLegacyFileDb(t, path)
	legacyExec(t, a, "CREATE TABLE m (id TEXT PRIMARY KEY, b TEXT)")
	legacyExec(t, a, "INSERT INTO m (id, b) VALUES ('x', ?/*sky:m|b*/)", true)
	if got := legacyStored(t, a, "m", "x"); got != "text:true" {
		t.Fatalf("pool A into the TEXT column stores %s, want text:true", got)
	}
	// Pool B converts the column, as docs/migration/v0.29.md shows. Its own
	// cache is cleared, pool A's is not told.
	for _, q := range []string{
		"CREATE TABLE m_new (id TEXT PRIMARY KEY, b BOOLEAN)",
		"INSERT INTO m_new SELECT id, CASE b WHEN 'true' THEN 1 WHEN 'false' THEN 0 ELSE b END FROM m",
		"DROP TABLE m",
		"ALTER TABLE m_new RENAME TO m",
	} {
		if _, err := b.conn.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	legacyExec(t, a, "INSERT INTO m (id, b) VALUES ('y', ?/*sky:m|b*/)", true)
	if got := legacyStored(t, a, "m", "y"); got != "integer:1" {
		t.Fatalf("pool A after pool B's conversion stores %s, want integer:1", got)
	}
}

// recordingExecutor records every statement run through it.
type recordingExecutor struct {
	inner dbExecutor
	seen  *[]string
}

func (r recordingExecutor) Exec(q string, args ...any) (sql.Result, error) {
	*r.seen = append(*r.seen, q)
	return r.inner.Exec(q, args...)
}

func (r recordingExecutor) Query(q string, args ...any) (*sql.Rows, error) {
	*r.seen = append(*r.seen, q)
	return r.inner.Query(q, args...)
}

func (r recordingExecutor) QueryRow(q string, args ...any) *sql.Row {
	*r.seen = append(*r.seen, q)
	return r.inner.QueryRow(q, args...)
}

// Finding 5 (v0.29.1): a mark whose table or column is not an identifier is
// skipped before any PRAGMA. The args are passed through and nothing runs.
func TestSqliteHostileMarkRunsNoPragma(t *testing.T) {
	d := openLegacyFileDb(t, filepath.Join(t.TempDir(), "hostile.db"))
	legacyExec(t, d, "CREATE TABLE t (id TEXT PRIMARY KEY, c TEXT)")
	legacyExec(t, d, "INSERT INTO t (id, c) VALUES ('a', 'true')")
	for _, q := range []string{
		`SELECT id FROM t WHERE c = ?/*sky:x");DROP TABLE t;--|c*/`,
		`SELECT id FROM t WHERE c = ?/*sky:t|c") OR 1=1;--*/`,
	} {
		var seen []string
		args := []any{true}
		got := sqliteArgs(recordingExecutor{inner: d.conn, seen: &seen}, d.conn, q, args)
		if len(seen) != 0 {
			t.Fatalf("%s ran %v, want no statement", q, seen)
		}
		if !reflect.DeepEqual(got, args) || got[0] != true {
			t.Fatalf("%s changed the args to %v", q, got)
		}
		if r, _ := AnyTaskRun(Db_query(d, q, []any{true})).(SkyResult[any, any]); r.Tag != 0 {
			t.Fatalf("%s: %v", q, r.ErrValue)
		}
	}
	var n int
	if err := d.conn.QueryRow("SELECT count(*) FROM t").Scan(&n); err != nil || n != 1 {
		t.Fatalf("table t after the hostile marks: n=%d err=%v", n, err)
	}
}

// Finding 1 (v0.29.1): the scanner of a partial-index predicate finds a
// comparison of a TEXT column with a number or TRUE / FALSE.
func TestSqlitePredicateLegacyComparison(t *testing.T) {
	text := map[string]bool{"flag": true, "score": true, "label": true}
	cases := []struct {
		where   string
		col     string
		literal string
	}{
		{"flag = TRUE", "flag", "TRUE"},
		{"(id <> '') AND (Flag = FALSE)", "Flag", "FALSE"},
		{"score >= 7.5", "score", "7.5"},
		{"score < -2", "score", "-2"},
		{"score IN ('a', 3)", "score", "3"},
		{"NOT (score NOT IN (1e-07))", "score", "1e-07"},
		{"1 = score", "score", "1"},
		{"label = 'true'", "", ""},
		{"label IS NOT NULL", "", ""},
		{"flag = label", "", ""},
		{"n = 1", "", ""},
		{"1 = 1", "", ""},
		{"label = 'flag = TRUE'", "", ""},
	}
	for _, c := range cases {
		col, lit := sqlitePredicateTextVsLiteral(c.where, text)
		if col != c.col || lit != c.literal {
			t.Errorf("%q: got (%q, %q), want (%q, %q)", c.where, col, lit, c.col, c.literal)
		}
	}
}

// Finding 1 (v0.29.1): Store.migrate, Store.create and a migration file's
// addIndex refuse a partial index that compares a v0.28 TEXT Bool or Float
// column with a number or TRUE / FALSE, naming the column. A BOOLEAN column
// and a text literal pass.
func TestSqlitePartialIndexOnLegacyTextColumnIsRefused(t *testing.T) {
	d := openLegacyFileDb(t, filepath.Join(t.TempDir(), "partial.db"))
	legacyExec(t, d, "CREATE TABLE lf (id TEXT PRIMARY KEY NOT NULL, flag TEXT, score TEXT, ok BOOLEAN)")
	cs := cspec("id", "text", "flag", "bool?", "score", "real?", "ok", "bool")
	wantErr := func(what string, r SkyResult[any, any], col string) {
		t.Helper()
		if r.Tag == 0 {
			t.Fatalf("%s: got Ok %v, want an Err naming %s", what, r.OkValue, col)
		}
		msg := fmt.Sprint(r.ErrValue)
		if !strings.Contains(msg, `column "`+col+`" of lf is TEXT`) || !strings.Contains(msg, "docs/migration/v0.29.md") {
			t.Fatalf("%s: the Err does not name the column and the conversion: %s", what, msg)
		}
	}
	wantErr("migrate", autoMigrate(t, d, "lf", cs, []any{storeIdx([]string{"id"}, true, "flag = TRUE")}), "flag")
	r, _ := AnyTaskRun(Db_createCols(d, "lf", cs, "id", []any{storeIdx([]string{"id"}, true, "score = 7.5")})).(SkyResult[any, any])
	wantErr("create", r, "score")
	wantErr("migration file", migrateOpsJson(t, d,
		`[{"id":"001_ix","ops":[{"kind":"addIndex","table":"lf","name":"ux_lf_flag","indexColumns":["id"],"unique":true,"where":"flag = FALSE"}]}]`), "flag")
	var n int
	_ = d.conn.QueryRow("SELECT count(*) FROM sqlite_master WHERE type = 'index' AND tbl_name = 'lf' AND sql IS NOT NULL").Scan(&n)
	if n != 0 {
		t.Fatalf("a refused partial index was created (%d)", n)
	}
	// A BOOLEAN column and a text literal on the TEXT column are fine.
	if r := autoMigrate(t, d, "lf", cs, []any{
		storeIdx([]string{"id"}, true, "ok = TRUE"),
		storeIdx([]string{"id"}, true, "flag = 'true'"),
	}); r.Tag != 0 {
		t.Fatalf("migrate with a BOOLEAN column and a text literal: %v", r.ErrValue)
	}
}

//go:build !js

package rt

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// v0.29.2 fourth Judge, gap 4: Store.insertMany built ONE INSERT for every
// row, so 10,000 rows of 4 columns (40,000 bound parameters) failed on
// SQLite with "too many SQL variables" (its limit is 32,766; PostgreSQL's is
// 65,535). The insert is now chunked by the engine's limit inside one
// transaction, so it stays all-or-nothing.

// insertManyChunkCase inserts rows x cols through Db_execObjectMany and
// checks the count, the rows, and that a failing last row leaves none.
func insertManyChunkCase(t *testing.T, d *SkyDb, table string, rows, cols int) {
	t.Helper()
	_, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table)
	t.Cleanup(func() { _, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table) })
	names := make([]string, cols)
	pairs := make([][2]string, cols)
	ddl := make([]string, cols)
	for c := 0; c < cols; c++ {
		names[c] = fmt.Sprintf("c%d", c)
		pairs[c] = [2]string{names[c], "text"}
		ddl[c] = names[c] + " TEXT NOT NULL"
	}
	ddl[0] += " PRIMARY KEY"
	if _, err := d.conn.Exec("CREATE TABLE " + table + " (" + strings.Join(ddl, ", ") + ")"); err != nil {
		t.Fatal(err)
	}
	mk := func(n int) ([]any, []any) {
		objs := make([]any, n)
		extras := make([]any, n)
		for i := 0; i < n; i++ {
			vals := make([]any, cols)
			for c := 0; c < cols; c++ {
				vals[c] = fmt.Sprintf("r%d-%d", i, c)
			}
			objs[i] = storeObj(names, vals)
			extras[i] = []any{}
		}
		return objs, extras
	}
	objs, extras := mk(rows)
	res := runTask(t, Db_execObjectMany(d, table, colspec(pairs...), objs, extras))
	if res.Tag != 0 {
		t.Fatalf("%d x %d: insertMany: %v", rows, cols, errorMessage(res.ErrValue))
	}
	if got := AsIntOrZero(res.OkValue); got != rows {
		t.Fatalf("%d x %d: insertMany returned %d", rows, cols, got)
	}
	var n int
	if err := d.conn.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != rows {
		t.Fatalf("%d x %d: %d rows stored (%v)", rows, cols, n, err)
	}
	// All-or-nothing: a second batch whose LAST row repeats a key fails
	// after the earlier chunks ran, and none of its rows stay.
	more, moreExtras := mk(rows)
	for i := range more {
		vals := make([]any, cols)
		for c := 0; c < cols; c++ {
			vals[c] = fmt.Sprintf("s%d-%d", i, c)
		}
		if i == rows-1 {
			vals[0] = "r0-0"
		}
		more[i] = storeObj(names, vals)
	}
	res = runTask(t, Db_execObjectMany(d, table, colspec(pairs...), more, moreExtras))
	if res.Tag == 0 {
		t.Fatalf("%d x %d: a batch with a repeated key was accepted", rows, cols)
	}
	if err := d.conn.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != rows {
		t.Fatalf("%d x %d: a failed batch left %d rows, want %d (%v)", rows, cols, n, rows, err)
	}
}

func TestStoreInsertManyChunksAboveTheSqliteParameterLimit(t *testing.T) {
	conn, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "many.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetMaxOpenConns(1)
	d := &SkyDb{conn: conn, driver: "sqlite"}
	insertManyChunkCase(t, d, "many_4", 10000, 4)
	insertManyChunkCase(t, d, "many_5", 20000, 5)
}

func TestRowsPerStatementStaysWithinTheEngineLimit(t *testing.T) {
	for _, c := range []struct {
		driver string
		cols   int
		want   int
	}{{"sqlite", 4, 8191}, {"sqlite", 5, 6553}, {"pgx", 4, 16383}, {"pgx", 5, 13107}, {"sqlite", 40000, 1}} {
		if got := dbRowsPerStatement(c.driver, c.cols); got != c.want {
			t.Errorf("%s %d cols: %d rows per statement, want %d", c.driver, c.cols, got, c.want)
		}
	}
}

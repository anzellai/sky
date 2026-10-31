//go:build !js

package rt

import (
	"database/sql"
	"path/filepath"
	"reflect"
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

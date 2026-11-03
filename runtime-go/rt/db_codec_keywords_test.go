//go:build !js

package rt

import (
	"database/sql"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The keyword lists are measured (v0.29.2 second Judge, gap 5: `commit`
// failed on SQLite in every statement, `if` as a table name, and
// `system_user` on PostgreSQL 16+, and none was listed). Each name is used
// unquoted as a column and as a table name in the statements the Store
// writes; a name that fails must be listed for that position, and a listed
// name must fail there.

// sqliteKeywords is SQLite's keyword list (sqlite.org/lang_keywords.html).
const sqliteKeywords = `ABORT ACTION ADD AFTER ALL ALTER ALWAYS ANALYZE AND AS ASC ATTACH
AUTOINCREMENT BEFORE BEGIN BETWEEN BY CASCADE CASE CAST CHECK COLLATE COLUMN COMMIT
CONFLICT CONSTRAINT CREATE CROSS CURRENT CURRENT_DATE CURRENT_TIME CURRENT_TIMESTAMP
DATABASE DEFAULT DEFERRABLE DEFERRED DELETE DESC DETACH DISTINCT DO DROP EACH ELSE END
ESCAPE EXCEPT EXCLUDE EXCLUSIVE EXISTS EXPLAIN FAIL FILTER FIRST FOLLOWING FOR FOREIGN
FROM FULL GENERATED GLOB GROUP GROUPS HAVING IF IGNORE IMMEDIATE IN INDEX INDEXED
INITIALLY INNER INSERT INSTEAD INTERSECT INTO IS ISNULL JOIN KEY LAST LEFT LIKE LIMIT
MATCH MATERIALIZED NATURAL NO NOT NOTHING NOTNULL NULL NULLS OF OFFSET ON OR ORDER
OTHERS OUTER OVER PARTITION PLAN PRAGMA PRECEDING PRIMARY QUERY RAISE RANGE RECURSIVE
REFERENCES REGEXP REINDEX RELEASE RENAME REPLACE RESTRICT RETURNING RIGHT ROLLBACK ROW
ROWS SAVEPOINT SELECT SET TABLE TEMP TEMPORARY THEN TIES TO TRANSACTION TRIGGER
UNBOUNDED UNION UNIQUE UPDATE USING VACUUM VALUES VIEW VIRTUAL WHEN WHERE WINDOW WITH
WITHOUT`

// keywordFails runs the Store's statement shapes with `n` as a column name and
// as a table name; it reports whether any statement fails in each position.
func keywordFails(exec func(string) error, n string) (column, table bool) {
	col := []string{
		"CREATE TABLE kwp_t (id BIGINT PRIMARY KEY, " + n + " TEXT, u TEXT UNIQUE)",
		"INSERT INTO kwp_t (id, " + n + ", u) VALUES (1, 'a', 'x')",
		"SELECT id, " + n + " FROM kwp_t WHERE " + n + " = 'a' AND " + n + " IS NOT NULL ORDER BY " + n + " ASC",
		"UPDATE kwp_t SET " + n + " = 'b' WHERE " + n + " = 'a'",
		"CREATE INDEX kwp_t_ix ON kwp_t (" + n + ")",
		"INSERT INTO kwp_t (id, " + n + ", u) VALUES (2, 'c', 'x') ON CONFLICT (u) DO UPDATE SET " + n + " = excluded." + n,
		"DELETE FROM kwp_t WHERE " + n + " = 'zz'",
	}
	tab := []string{
		"CREATE TABLE " + n + " (id BIGINT PRIMARY KEY, v TEXT)",
		"INSERT INTO " + n + " (id, v) VALUES (1, 'a')",
		"SELECT id, v FROM " + n + " WHERE v = 'a' ORDER BY id",
		"UPDATE " + n + " SET v = 'b' WHERE id = 1",
		"DELETE FROM " + n + " WHERE id = 2",
		"CREATE INDEX kwp_ix ON " + n + " (v)",
		"ALTER TABLE " + n + " ADD COLUMN w TEXT",
		"SELECT a.id FROM " + n + " a JOIN kwp_t b ON a.id = b.id",
	}
	for _, q := range col {
		if exec(q) != nil {
			column = true
		}
	}
	_ = exec("CREATE TABLE IF NOT EXISTS kwp_t (id BIGINT PRIMARY KEY)")
	for _, q := range tab {
		if exec(q) != nil {
			table = true
		}
	}
	return column, table
}

// storeSqlKeywords is Std.Db.Store's list of names that fail on both engines.
func storeSqlKeywords(t *testing.T) map[string]bool {
	t.Helper()
	b, err := os.ReadFile("../../sky-stdlib/Std/Db/Store.sky")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "\nsqlKeywords =")
	if i < 0 {
		t.Fatal("Store.sky has no sqlKeywords")
	}
	body := src[i:]
	body = body[:strings.Index(body, "]")]
	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`"([a-z_]+)"`).FindAllStringSubmatch(body, -1) {
		out[m[1]] = true
	}
	return out
}

func TestSqlKeywordListsAreMeasuredOnSqlite(t *testing.T) {
	both := storeSqlKeywords(t)
	var problems []string
	for _, kw := range strings.Fields(strings.ToLower(sqliteKeywords)) {
		conn, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		conn.SetMaxOpenConns(1)
		col, tab := keywordFails(func(q string) error { _, e := conn.Exec(q); return e }, kw)
		conn.Close()
		if both[kw] {
			if !col && !tab {
				problems = append(problems, kw+": in Store.sky sqlKeywords, and SQLite takes it")
			}
			continue
		}
		listedCol := codecReservedName("sqlite", kw, false)
		listedTab := codecReservedName("sqlite", kw, true)
		if col != listedCol {
			problems = append(problems, kw+": as a column, fails="+boolWord(col)+", listed="+boolWord(listedCol))
		}
		if tab != listedTab {
			problems = append(problems, kw+": as a table, fails="+boolWord(tab)+", listed="+boolWord(listedTab))
		}
	}
	// Every listed name is a SQLite keyword.
	for _, m := range []map[string]bool{codecReservedSqlite, codecReservedSqliteColumn, codecReservedSqliteTable} {
		for w := range m {
			if !strings.Contains(" "+strings.Join(strings.Fields(strings.ToLower(sqliteKeywords)), " ")+" ", " "+w+" ") {
				problems = append(problems, w+": listed, and not a SQLite keyword")
			}
		}
	}
	sort.Strings(problems)
	if len(problems) > 0 {
		t.Fatalf("the SQLite keyword lists do not match SQLite:\n%s", strings.Join(problems, "\n"))
	}
}

func boolWord(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

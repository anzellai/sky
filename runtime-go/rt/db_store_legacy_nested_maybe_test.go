package rt

// A v0.28 `Codec.maybe (Codec.maybe x)` field of a scalar `x` had the kind
// `int??` (`real??`, `bool??`). v0.28 split off one `?`, so
// codecColKindToSchema saw the base `int?`, fell through to its default arm
// and created the column as TEXT on both dialects, and rawToSqlArg wrote
// `Just (Just 5)` as the text "5" (`%v`). v0.29 gives the same field the kind
// `int?` and binds the value as an int64. Migrate never alters a column, so an
// app's existing table keeps its TEXT column and the v0.29 write lands there.
// pgx refused that bind ("unable to encode 7 into text format for text (OID
// 25)"). SQLite stored `1` / `7.0` for a Bool / Float, not v0.28's `true` /
// `7`, and a condition binding 1 compared as the text `1`, so it missed every
// v0.28 row with no error (db_sqlite_legacy.go). This file proves the write,
// the conditions (a placeholder marked as Std.Db.Store marks it) and the read
// on SQLite here, and on PostgreSQL in
// db_store_legacy_nested_maybe_integration_test.go.

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// assertLegacyNestedMaybeTextColumns creates `table` as v0.28 did (each nested
// Maybe scalar a nullable TEXT column) with one row v0.28 wrote, migrates it
// with the v0.29 colspec, inserts as v0.29 does and reads every row back
// through the store read path.
func assertLegacyNestedMaybeTextColumns(t *testing.T, d *SkyDb, table string) {
	t.Helper()
	_, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table)
	t.Cleanup(func() { _, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table) })
	if _, err := d.conn.Exec("CREATE TABLE " + table + " (id TEXT PRIMARY KEY NOT NULL, n TEXT, r TEXT, b TEXT)"); err != nil {
		t.Fatalf("create the v0.28 table: %v", err)
	}
	if _, err := d.conn.Exec("INSERT INTO " + table + " (id, n, r, b) VALUES ('a', '5', '2.5', 'true'), ('d', NULL, '7', 'false')"); err != nil {
		t.Fatalf("insert the v0.28 rows: %v", err)
	}
	cs := colspec([2]string{"id", "text"}, [2]string{"n", "int?"}, [2]string{"r", "real?"}, [2]string{"b", "bool?"})

	res, ok := AnyTaskRun(Db_autoMigrate(d, table, cs, "id", []any{})).(SkyResult[any, any])
	if !ok || res.Tag != 0 {
		t.Fatalf("migrate the v0.28 table: %v", res.ErrValue)
	}
	if applied := AsList(res.OkValue); len(applied) != 0 {
		t.Fatalf("migrate must leave the v0.28 columns alone, applied %v", applied)
	}

	keys := []string{"id", "n", "r", "b"}
	for _, obj := range []JsonValue{
		storeObj(keys, []any{"b", 7, 1.5, false}),
		storeObj(keys, []any{"c", nil, nil, nil}),
		storeObj(keys, []any{"e", 8, 7.0, true}),
	} {
		if r, _ := AnyTaskRun(Db_execObject(d, table, cs, obj, "id", "text")).(SkyResult[any, any]); r.Tag != 0 {
			t.Fatalf("insert into the TEXT columns as v0.29 does: %v", r.ErrValue)
		}
	}

	read := func(sqlText string, params []any, want []string) {
		t.Helper()
		got, _ := AnyTaskRun(Db_queryObjects(d, sqlText, params, cs)).(SkyResult[any, any])
		if got.Tag != 0 {
			t.Fatalf("%s: %v", sqlText, got.ErrValue)
		}
		rows := AsList(got.OkValue)
		if len(rows) != len(want) {
			t.Fatalf("%s: rows = %v, want %v", sqlText, rows, want)
		}
		for i := range want {
			if AsString(rows[i]) != want[i] {
				t.Fatalf("%s: row %d = %s, want %s", sqlText, i, AsString(rows[i]), want[i])
			}
		}
	}
	// A Store UPDATE of a marked column writes the same text.
	upd := "UPDATE " + table + " SET b = ?/*sky:" + table + "|b*/, r = ?/*sky:" + table + "|r*/ WHERE id = 'c'"
	if r, _ := AnyTaskRun(Db_exec(d, upd, []any{true, 2.0})).(SkyResult[any, any]); r.Tag != 0 {
		t.Fatalf("update the TEXT columns as v0.29 does: %v", r.ErrValue)
	}

	read("SELECT id, n, r, b FROM "+table+" ORDER BY id", []any{}, []string{
		`{"id":"a","n":5,"r":2.5,"b":true}`,
		`{"id":"b","n":7,"r":1.5,"b":false}`,
		`{"id":"c","n":null,"r":2,"b":true}`,
		`{"id":"d","n":null,"r":7,"b":false}`,
		`{"id":"e","n":8,"r":7,"b":true}`,
	})
	read("SELECT id, n, r, b FROM "+table+" WHERE n = ?", []any{int64(5)}, []string{
		`{"id":"a","n":5,"r":2.5,"b":true}`,
	})
	// A condition matches the v0.28 rows and the v0.29 rows alike.
	mark := func(col string) string { return "?/*sky:" + table + "|" + col + "*/" }
	read("SELECT id, n, r, b FROM "+table+" WHERE b = "+mark("b")+" ORDER BY id", []any{true}, []string{
		`{"id":"a","n":5,"r":2.5,"b":true}`,
		`{"id":"c","n":null,"r":2,"b":true}`,
		`{"id":"e","n":8,"r":7,"b":true}`,
	})
	read("SELECT id, n, r, b FROM "+table+" WHERE b = "+mark("b")+" ORDER BY id", []any{false}, []string{
		`{"id":"b","n":7,"r":1.5,"b":false}`,
		`{"id":"d","n":null,"r":7,"b":false}`,
	})
	read("SELECT id, n, r, b FROM "+table+" WHERE r = "+mark("r")+" ORDER BY id", []any{7.0}, []string{
		`{"id":"d","n":null,"r":7,"b":false}`,
		`{"id":"e","n":8,"r":7,"b":true}`,
	})
	read("SELECT id, n, r, b FROM "+table+" WHERE r IN ("+mark("r")+", "+mark("r")+") ORDER BY id", []any{1.5, 2.5}, []string{
		`{"id":"a","n":5,"r":2.5,"b":true}`,
		`{"id":"b","n":7,"r":1.5,"b":false}`,
	})

	// The stored text is the text v0.28 wrote, so every row has one spelling
	// and a hand-written query sees it.
	for _, want := range [][4]string{
		{"a", "5", "2.5", "true"},
		{"b", "7", "1.5", "false"},
		{"c", "", "2", "true"},
		{"d", "", "7", "false"},
		{"e", "8", "7", "true"},
	} {
		var n, r, b sql.NullString
		if err := d.conn.QueryRow("SELECT n, r, b FROM "+table+" WHERE id = '"+want[0]+"'").Scan(&n, &r, &b); err != nil {
			t.Fatalf("read the stored text of %s: %v", want[0], err)
		}
		if n.String != want[1] || r.String != want[2] || b.String != want[3] {
			t.Fatalf("row %s stores (%q, %q, %q), want (%q, %q, %q)",
				want[0], n.String, r.String, b.String, want[1], want[2], want[3])
		}
	}
}

func TestLegacyNestedMaybeTextColumnsSqlite(t *testing.T) {
	conn, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	assertLegacyNestedMaybeTextColumns(t, &SkyDb{conn: conn, driver: "sqlite"}, "legacy_nested_maybe")
}

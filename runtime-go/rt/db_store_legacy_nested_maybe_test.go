package rt

// A v0.28 `Codec.maybe (Codec.maybe x)` field of a scalar `x` had the kind
// `int??` (`real??`, `bool??`). v0.28 split off one `?`, so
// codecColKindToSchema saw the base `int?`, fell through to its default arm
// and created the column as TEXT on both dialects, and rawToSqlArg wrote
// `Just (Just 5)` as the text "5" (`%v`). v0.29 gives the same field the kind
// `int?` and binds the value as an int64. Migrate never alters a column, so an
// app's existing table keeps its TEXT column and the v0.29 write lands there.
// pgx refused that bind ("unable to encode 7 into text format for text (OID
// 25)"); SQLite stored the text. This file proves the write, a condition and
// the read on SQLite here, and on PostgreSQL in
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
	if _, err := d.conn.Exec("INSERT INTO " + table + " (id, n, r, b) VALUES ('a', '5', '2.5', 'true')"); err != nil {
		t.Fatalf("insert the v0.28 row: %v", err)
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
	read("SELECT id, n, r, b FROM "+table+" ORDER BY id", []any{}, []string{
		`{"id":"a","n":5,"r":2.5,"b":true}`,
		`{"id":"b","n":7,"r":1.5,"b":false}`,
		`{"id":"c","n":null,"r":null,"b":null}`,
	})
	read("SELECT id, n, r, b FROM "+table+" WHERE n = ?", []any{int64(5)}, []string{
		`{"id":"a","n":5,"r":2.5,"b":true}`,
	})

	// The stored text is the plain decimal, so a hand-written query sees it.
	var text string
	if err := d.conn.QueryRow("SELECT n FROM " + table + " WHERE id = 'b'").Scan(&text); err != nil {
		t.Fatalf("read the stored text: %v", err)
	}
	if text != "7" {
		t.Fatalf("stored text = %q, want \"7\"", text)
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

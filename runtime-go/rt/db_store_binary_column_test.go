//go:build !js

package rt

import (
	"strings"
	"testing"
)

// Schema.blob makes a binary column (BYTEA on PostgreSQL, BLOB on SQLite)
// through Schema.createSchema, and a Store reads and writes a JSON field (a
// list, a record, a custom type, a Json.Value, Schema.json) as TEXT. The two
// types stay (user decision, v0.29.2), so the mismatch is made loud: migrate
// and pushProject (Db_autoMigrate) refuse a JSON column that the table has as
// binary, naming the column and the fix, instead of misreading every row.
// A Schema.blob column (the `bin` flag Schema.toProject sets) on a binary
// column is a match.
func TestStoreMigrateRefusesAJsonFieldOnABinaryColumn(t *testing.T) {
	d := openStoreQueryDb(t)
	const table = "bin_cols"
	_, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table)
	t.Cleanup(func() { _, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table) })
	if _, err := d.conn.Exec("CREATE TABLE " + table + " (id TEXT PRIMARY KEY NOT NULL, items BLOB NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	cs := []any{T2[any, any]{V0: "id", V1: "text"}, T2[any, any]{V0: "items", V1: "blob"}}
	res, ok := AnyTaskRun(Db_autoMigrate(d, table, cs, "id", []any{})).(SkyResult[any, any])
	if !ok || res.Tag == 0 {
		t.Fatalf("a JSON field on a BLOB column was accepted: %v", res)
	}
	msg := errorMessage(res.ErrValue)
	for _, want := range []string{"bin_cols.items", "BLOB", "JSON text", "Schema.json"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the Err does not name %q: %s", want, msg)
		}
	}
	// The Schema.blob column itself (toProject's `bin` flag) is a match.
	csBin := []any{T2[any, any]{V0: "id", V1: "text"}, T2[any, any]{V0: "items", V1: "blob|bin"}}
	res, ok = AnyTaskRun(Db_autoMigrate(d, table, csBin, "id", []any{})).(SkyResult[any, any])
	if !ok || res.Tag != 0 {
		t.Fatalf("a Schema.blob column on a BLOB column was refused: %v", res)
	}
}

// The reverse is detectable only as a notice: a Schema.blob column that the
// table has as TEXT (sky db push made it) is not refused, because push itself
// made it so, but it is named once.
func TestSchemaBlobOnATextColumnIsNamed(t *testing.T) {
	n := codecBinaryColumnNotices("sqlite", "files", []any{T2[any, any]{V0: "data", V1: "blob|bin"}}, map[string]string{"data": "TEXT"})
	if len(n) != 1 || !strings.Contains(n[0], "files.data") || !strings.Contains(n[0], "Schema.createSchema") {
		t.Fatalf("notices = %v", n)
	}
	if n := codecBinaryColumnNotices("pgx", "files", []any{T2[any, any]{V0: "data", V1: "blob|bin"}}, map[string]string{"data": "bytea"}); len(n) != 0 {
		t.Fatalf("a Schema.blob column on BYTEA is a match, notices = %v", n)
	}
}

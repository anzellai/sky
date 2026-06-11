package rt

// #585 — Std.Db.insertFields end-to-end tests.
//
// Mirrors the structure of db_sqlvalue_test.go but exercises the
// dynamic INSERT builder: SetField vs OmitField, NULL via SqlNull
// witness, identifier validation, all-omit DEFAULT VALUES path,
// and mixed columns in declared order.

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func openInsertFieldsTestDb(t *testing.T) *SkyDb {
	t.Helper()
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// `status` carries a DEFAULT so OmitField can be observed; `note`
	// allows NULL so SqlNull binds explicitly; `id` autoincrements so
	// all-OmitField DEFAULT VALUES has somewhere to go.
	if _, err := conn.Exec(`CREATE TABLE items (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL DEFAULT 'unnamed',
		status TEXT NOT NULL DEFAULT 'pending',
		note TEXT,
		qty INTEGER
	)`); err != nil {
		t.Fatalf("create items: %v", err)
	}
	return &SkyDb{conn: conn, driver: "sqlite"}
}

// setField / omitField mirror the SqlField ADT shape codegen would
// produce.  Tag is irrelevant — dbBindArg routes by SkyName.
func setField(v any) SkyADT {
	return SkyADT{Tag: 0, SkyName: "SetField", Fields: []any{v}}
}

func omitField() SkyADT {
	return SkyADT{Tag: 1, SkyName: "OmitField", Fields: nil}
}

func pair(col string, v any) SkyTuple2 {
	return SkyTuple2{V0: col, V1: v}
}

// mustRows runs a SELECT and returns the rows as []map[string]any so
// individual assertions stay legible.
func mustRows(t *testing.T, db *SkyDb, query string, args ...any) []map[string]any {
	t.Helper()
	rs, err := db.conn.Query(query, args...)
	if err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	defer rs.Close()
	cols, err := rs.Columns()
	if err != nil {
		t.Fatalf("columns: %v", err)
	}
	var out []map[string]any
	for rs.Next() {
		buf := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range buf {
			ptrs[i] = &buf[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			t.Fatalf("scan: %v", err)
		}
		row := map[string]any{}
		for i, c := range cols {
			row[c] = buf[i]
		}
		out = append(out, row)
	}
	return out
}

// runTask drains a Sky Task back into a SkyResult so tests can read
// the affected-rows int (or the Err for negative cases).
func runTask(t *testing.T, taskish any) SkyResult[any, any] {
	t.Helper()
	out := AnyTaskRun(taskish)
	r, ok := out.(SkyResult[any, any])
	if !ok {
		t.Fatalf("expected SkyResult, got %T", out)
	}
	return r
}

func TestDb_insertFields_SetAndOmit(t *testing.T) {
	db := openInsertFieldsTestDb(t)
	// name = "Widget", status omitted (DEFAULT 'pending'), note set.
	task := Db_insertFields(db, "items", []any{
		pair("name", setField(sqlValue("SqlString", "Widget"))),
		pair("status", omitField()),
		pair("note", setField(sqlValue("SqlString", "first batch"))),
	})
	res := runTask(t, task)
	if res.Tag != 0 {
		t.Fatalf("insert failed: %+v", res.ErrValue)
	}
	if n, _ := res.OkValue.(int); n != 1 {
		t.Fatalf("affected rows: got %v want 1", res.OkValue)
	}
	rows := mustRows(t, db, "SELECT name, status, note, qty FROM items")
	if len(rows) != 1 {
		t.Fatalf("row count: got %d want 1", len(rows))
	}
	if got := rows[0]["name"]; got != "Widget" {
		t.Errorf("name: got %v want Widget", got)
	}
	if got := rows[0]["status"]; got != "pending" {
		t.Errorf("status (should be DEFAULT): got %v want pending", got)
	}
	if got := rows[0]["note"]; got != "first batch" {
		t.Errorf("note: got %v want first batch", got)
	}
	if got := rows[0]["qty"]; got != nil {
		t.Errorf("qty (no DEFAULT, no SetField): got %v want nil", got)
	}
}

func TestDb_insertFields_ExplicitNullViaSqlNull(t *testing.T) {
	db := openInsertFieldsTestDb(t)
	// `note` has no DEFAULT but allows NULL. SqlNull witness binds
	// nil at the driver — distinguishable from OmitField (which would
	// leave the column out of the SQL entirely and apply nothing).
	task := Db_insertFields(db, "items", []any{
		pair("name", setField(sqlValue("SqlString", "Gadget"))),
		pair("note", setField(sqlValue("SqlNull", sqlValue("SqlString", "")))),
	})
	res := runTask(t, task)
	if res.Tag != 0 {
		t.Fatalf("insert failed: %+v", res.ErrValue)
	}
	rows := mustRows(t, db, "SELECT name, note FROM items")
	if len(rows) != 1 || rows[0]["name"] != "Gadget" {
		t.Fatalf("name mismatch: %+v", rows)
	}
	if rows[0]["note"] != nil {
		t.Errorf("note (explicit SqlNull): got %v want nil", rows[0]["note"])
	}
}

func TestDb_insertFields_AllOmitDefaultValues(t *testing.T) {
	db := openInsertFieldsTestDb(t)
	// All columns OmitField → INSERT INTO items DEFAULT VALUES. Each
	// non-NULL column carries its own DEFAULT; NULL columns end up
	// nil.  This is the documented behaviour and the "useful" choice
	// over updateFields' "no clauses → 0 rows" semantics.
	task := Db_insertFields(db, "items", []any{
		pair("name", omitField()),
		pair("status", omitField()),
		pair("note", omitField()),
		pair("qty", omitField()),
	})
	res := runTask(t, task)
	if res.Tag != 0 {
		t.Fatalf("insert failed: %+v", res.ErrValue)
	}
	if n, _ := res.OkValue.(int); n != 1 {
		t.Fatalf("affected rows: got %v want 1", res.OkValue)
	}
	rows := mustRows(t, db, "SELECT name, status, note, qty FROM items")
	if rows[0]["name"] != "unnamed" || rows[0]["status"] != "pending" {
		t.Errorf("defaults not applied: %+v", rows[0])
	}
	if rows[0]["note"] != nil || rows[0]["qty"] != nil {
		t.Errorf("nullable columns should be nil: %+v", rows[0])
	}
}

func TestDb_insertFields_PreservesDeclaredColumnOrder(t *testing.T) {
	db := openInsertFieldsTestDb(t)
	// Caller specifies columns in a non-table order; emitted SQL must
	// preserve the caller's order so the bind-positions line up.  We
	// can't read the SQL directly but the round-trip values prove the
	// per-column wiring: if note went into name (or vice-versa), the
	// assertion below would catch it.
	task := Db_insertFields(db, "items", []any{
		pair("note", setField(sqlValue("SqlString", "NOTE-FIRST"))),
		pair("qty", setField(sqlValue("SqlInt", 42))),
		pair("name", setField(sqlValue("SqlString", "ORDER"))),
	})
	res := runTask(t, task)
	if res.Tag != 0 {
		t.Fatalf("insert failed: %+v", res.ErrValue)
	}
	rows := mustRows(t, db, "SELECT name, note, qty FROM items")
	if rows[0]["name"] != "ORDER" {
		t.Errorf("name: %v", rows[0]["name"])
	}
	if rows[0]["note"] != "NOTE-FIRST" {
		t.Errorf("note: %v", rows[0]["note"])
	}
	// SQLite scans INTEGER columns into int64.
	if got, _ := rows[0]["qty"].(int64); got != 42 {
		t.Errorf("qty: %v (%T)", rows[0]["qty"], rows[0]["qty"])
	}
}

func TestDb_insertFields_RejectsBadIdent(t *testing.T) {
	db := openInsertFieldsTestDb(t)
	// Column name contains an injection vector — reused validSqlIdent
	// must reject before any SQL hits the driver.
	task := Db_insertFields(db, "items", []any{
		pair("name); DROP TABLE items;--", setField(sqlValue("SqlString", "x"))),
	})
	res := runTask(t, task)
	if res.Tag == 0 {
		t.Fatalf("expected error for bad column name; got Ok %v", res.OkValue)
	}
	// Table should still be intact.
	rows := mustRows(t, db, "SELECT count(*) AS n FROM items")
	if got, _ := rows[0]["n"].(int64); got != 0 {
		t.Errorf("table count after rejected insert: %v", rows[0]["n"])
	}
}

func TestDb_insertFields_RejectsBadTableName(t *testing.T) {
	db := openInsertFieldsTestDb(t)
	task := Db_insertFields(db, "items; DROP TABLE items;--", []any{
		pair("name", setField(sqlValue("SqlString", "x"))),
	})
	res := runTask(t, task)
	if res.Tag == 0 {
		t.Fatalf("expected error for bad table name; got Ok %v", res.OkValue)
	}
}

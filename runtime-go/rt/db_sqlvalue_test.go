package rt

// v0.16.26 (#582) — Std.Db.SqlValue ADT end-to-end binding tests.
// Each variant flows through `Db_exec` against in-memory SQLite to
// verify (a) `dbBindArg` recognises the SkyADT shape, (b)
// `sqlValueToGo` decodes to the right database/sql type, (c) the
// driver writes the cell correctly. Mixed-type INSERT closes the
// #575/#579 blocker that brought sky-sqlgen to a halt.

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// sqlValue is a test-side helper to construct a SqlValue variant
// without depending on Sky codegen. Mirrors what the lowered
// `SqlString "x"` etc. would build at runtime.
func sqlValue(name string, fields ...any) SkyADT {
	return SkyADT{Tag: 0, SkyName: name, Fields: fields}
}

func openSqlValueTestDb(t *testing.T) *SkyDb {
	t.Helper()
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := conn.Exec(`CREATE TABLE items (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		qty INTEGER,
		ratio REAL,
		active INTEGER,
		payload BLOB,
		updated_at INTEGER
	)`); err != nil {
		t.Fatalf("create items: %v", err)
	}
	return &SkyDb{conn: conn, driver: "sqlite"}
}

// TestSqlValue_E2E_MixedTypes — the headline win. Bind a row with
// SqlString + SqlInt + SqlFloat + SqlBool + SqlBytes + SqlNull, all
// from a single homogeneous []any list (which is how the Sky-side
// `List SqlValue` lowers).
func TestSqlValue_E2E_MixedTypes(t *testing.T) {
	db := openSqlValueTestDb(t)
	insertTask := Db_exec(
		db,
		"INSERT INTO items (id, name, qty, ratio, active, payload, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		[]any{
			sqlValue("SqlInt", 1),                          // id = 1
			sqlValue("SqlString", "widget"),                // name
			sqlValue("SqlInt", 42),                         // qty
			sqlValue("SqlFloat", 3.14),                     // ratio
			sqlValue("SqlBool", true),                      // active
			sqlValue("SqlBytes", "binary\x00data"),         // payload
			sqlValue("SqlNull", sqlValue("SqlInt", 0)),     // updated_at = NULL
		},
	)
	res := AnyTaskRun(insertTask)
	sr, ok := res.(SkyResult[any, any])
	if !ok || sr.Tag != 0 {
		t.Fatalf("Db_exec failed: %v", res)
	}

	var name string
	var qty, active sql.NullInt64
	var ratio sql.NullFloat64
	var payload []byte
	var updatedAt sql.NullInt64
	err := db.conn.QueryRow(
		"SELECT name, qty, ratio, active, payload, updated_at FROM items WHERE id = 1").
		Scan(&name, &qty, &ratio, &active, &payload, &updatedAt)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if name != "widget" {
		t.Errorf("name: expected widget, got %q", name)
	}
	if !qty.Valid || qty.Int64 != 42 {
		t.Errorf("qty: expected 42, got %v", qty)
	}
	if !ratio.Valid || ratio.Float64 < 3.13 || ratio.Float64 > 3.15 {
		t.Errorf("ratio: expected ~3.14, got %v", ratio)
	}
	if !active.Valid || active.Int64 != 1 {
		t.Errorf("active: expected 1, got %v", active)
	}
	if string(payload) != "binary\x00data" {
		t.Errorf("payload: expected %q, got %q", "binary\x00data", payload)
	}
	if updatedAt.Valid {
		t.Errorf("updated_at: expected NULL, got %v", updatedAt.Int64)
	}
}

// TestSqlValue_NullVariantBindsNil — the SqlNull witness only
// matters at the type-design level; the driver gets nil.
func TestSqlValue_NullVariantBindsNil(t *testing.T) {
	cases := []struct {
		name string
		v    any
	}{
		{"SqlNull(SqlString)", sqlValue("SqlNull", sqlValue("SqlString", ""))},
		{"SqlNull(SqlInt)", sqlValue("SqlNull", sqlValue("SqlInt", 0))},
		{"SqlNull(SqlFloat)", sqlValue("SqlNull", sqlValue("SqlFloat", 0.0))},
		{"SqlNull(SqlBool)", sqlValue("SqlNull", sqlValue("SqlBool", false))},
	}
	for _, tc := range cases {
		got := dbBindArg(tc.v)
		if got != nil {
			t.Errorf("%s: expected nil, got %#v", tc.name, got)
		}
	}
}

// TestSqlValue_MoneyBindsAsText — Money serialises as
// "ISO_CODE AMOUNT" for lossless single-TEXT-column round-trip.
// sqlDecimalToString accepts string pass-through, so the test feeds
// the amount as a pre-stringified value rather than depending on
// the decimal-kernel box path.
func TestSqlValue_MoneyBindsAsText(t *testing.T) {
	usdCurrency := SkyADT{Tag: 0, SkyName: "USD", Fields: []any{}}
	money := SkyADT{Tag: 0, SkyName: "Money",
		Fields: []any{"1234.56", usdCurrency}}
	sv := sqlValue("SqlMoney", money)
	got := dbBindArg(sv)
	s, ok := got.(string)
	if !ok {
		t.Fatalf("expected string, got %T (%v)", got, got)
	}
	if s != "USD 1234.56" {
		t.Errorf("expected 'USD 1234.56', got %q", s)
	}

	// CurrencyRaw fallback for non-standard codes.
	rawCurrency := SkyADT{Tag: 0, SkyName: "CurrencyRaw", Fields: []any{"XYZ"}}
	money2 := SkyADT{Tag: 0, SkyName: "Money",
		Fields: []any{"99.00", rawCurrency}}
	got2 := dbBindArg(sqlValue("SqlMoney", money2))
	if got2 != "XYZ 99.00" {
		t.Errorf("CurrencyRaw: expected 'XYZ 99.00', got %v", got2)
	}
}

// TestSqlValue_UpdateFields — partial-update builder includes only
// SetField columns; OmitField columns are dropped from the SQL so
// the database keeps their existing value.
func TestSqlValue_UpdateFields(t *testing.T) {
	db := openSqlValueTestDb(t)
	if _, err := db.conn.Exec(
		"INSERT INTO items (id, name, qty) VALUES (1, 'old', 100)"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	whereCols := []any{
		SkyTuple2{V0: "id", V1: sqlValue("SqlInt", 1)},
	}
	setFields := []any{
		// name: change to "new"
		SkyTuple2{V0: "name",
			V1: sqlValue("SetField", sqlValue("SqlString", "new"))},
		// qty: leave alone
		SkyTuple2{V0: "qty",
			V1: SkyADT{Tag: 1, SkyName: "OmitField", Fields: nil}},
	}
	updateTask := Db_updateFields(db, "items", whereCols, setFields)
	res := AnyTaskRun(updateTask)
	sr, ok := res.(SkyResult[any, any])
	if !ok || sr.Tag != 0 {
		t.Fatalf("Db_updateFields failed: %v", res)
	}
	if n, _ := sr.OkValue.(int); n != 1 {
		t.Errorf("expected 1 row affected, got %v", sr.OkValue)
	}

	// Verify: name changed, qty unchanged.
	var name string
	var qty int
	if err := db.conn.QueryRow(
		"SELECT name, qty FROM items WHERE id = 1").Scan(&name, &qty); err != nil {
		t.Fatalf("verify select: %v", err)
	}
	if name != "new" {
		t.Errorf("name: expected 'new', got %q", name)
	}
	if qty != 100 {
		t.Errorf("qty: expected 100 (unchanged), got %d", qty)
	}
}

// TestSqlValue_UpdateFields_AllOmitted — when every field is
// OmitField the kernel short-circuits to 0 rows affected without
// emitting an empty SET clause (which would be a SQL syntax error).
func TestSqlValue_UpdateFields_AllOmitted(t *testing.T) {
	db := openSqlValueTestDb(t)
	if _, err := db.conn.Exec(
		"INSERT INTO items (id, name) VALUES (1, 'x')"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	whereCols := []any{
		SkyTuple2{V0: "id", V1: sqlValue("SqlInt", 1)},
	}
	setFields := []any{
		SkyTuple2{V0: "name",
			V1: SkyADT{Tag: 1, SkyName: "OmitField", Fields: nil}},
	}
	res := AnyTaskRun(Db_updateFields(db, "items", whereCols, setFields))
	sr, _ := res.(SkyResult[any, any])
	if sr.Tag != 0 {
		t.Fatalf("expected Ok, got %v", res)
	}
	if n, _ := sr.OkValue.(int); n != 0 {
		t.Errorf("expected 0 rows affected, got %v", sr.OkValue)
	}
}

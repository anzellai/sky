package rt

// Regression for Sky issue #574 — `Db.exec` and `Db.query` must
// accept `Maybe a` parameters (bind Nothing as SQL NULL, Just x as
// the unwrapped value). Pre-fix, the database/sql driver rejected
// the boxed `rt.SkyMaybe[T]` struct outright:
//
//	db.exec: sql: converting argument $2 type: unsupported type
//	rt.SkyMaybe[int], a struct
//
// blocking every nullable INSERT / UPDATE.
//
// The fix adds `dbBindArg` in db_auth.go which reflect-walks any
// arg with the SkyMaybe shape (struct with int `Tag` + `JustValue`
// fields) and substitutes nil for Nothing / the unwrapped value
// for Just. These tests exercise both halves end-to-end against
// in-memory SQLite using the actual `Db_exec` / `Db_query`
// kernels — same code path users hit at runtime.

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func openNullableTestDb(t *testing.T) *SkyDb {
	t.Helper()
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := conn.Exec(`CREATE TABLE people (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		age INTEGER
	)`); err != nil {
		t.Fatalf("create people: %v", err)
	}
	return &SkyDb{conn: conn, driver: "sqlite"}
}

// TestDbExec_BindsJustAsValue asserts that binding `Just 30` to a
// nullable INTEGER column writes 30, not a struct, and reads back
// the same value. Without the fix this errors at the database/sql
// argument-converter layer with "unsupported type rt.SkyMaybe[int]".
func TestDbExec_BindsJustAsValue(t *testing.T) {
	db := openNullableTestDb(t)
	insertTask := Db_exec(
		db,
		"INSERT INTO people (id, name, age) VALUES (?, ?, ?)",
		[]any{1, "alice", Just[int](30)},
	)
	result := AnyTaskRun(insertTask)
	sr, ok := result.(SkyResult[any, any])
	if !ok {
		t.Fatalf("Db_exec returned %T, expected SkyResult", result)
	}
	if sr.Tag != 0 {
		t.Fatalf("Db_exec returned Err: %v — Just 30 should bind as 30, not struct",
			sr.ErrValue)
	}
	// Confirm SELECT sees the value as an actual integer column.
	var age sql.NullInt64
	if err := db.conn.QueryRow("SELECT age FROM people WHERE id = 1").Scan(&age); err != nil {
		t.Fatalf("select after insert: %v", err)
	}
	if !age.Valid || age.Int64 != 30 {
		t.Errorf("expected age=30, got valid=%v value=%d", age.Valid, age.Int64)
	}
}

// TestDbExec_BindsNothingAsNull asserts that binding `Nothing` to
// a nullable INTEGER column writes SQL NULL, readable via
// `IS NULL` and `sql.NullInt64.Valid == false`.
func TestDbExec_BindsNothingAsNull(t *testing.T) {
	db := openNullableTestDb(t)
	insertTask := Db_exec(
		db,
		"INSERT INTO people (id, name, age) VALUES (?, ?, ?)",
		[]any{2, "bob", Nothing[int]()},
	)
	result := AnyTaskRun(insertTask)
	sr, ok := result.(SkyResult[any, any])
	if !ok {
		t.Fatalf("Db_exec returned %T, expected SkyResult", result)
	}
	if sr.Tag != 0 {
		t.Fatalf("Db_exec returned Err: %v — Nothing should bind as NULL",
			sr.ErrValue)
	}
	var age sql.NullInt64
	if err := db.conn.QueryRow("SELECT age FROM people WHERE id = 2").Scan(&age); err != nil {
		t.Fatalf("select after insert: %v", err)
	}
	if age.Valid {
		t.Errorf("expected NULL age, got valid=true value=%d", age.Int64)
	}
	// Also reachable via IS NULL — the SQL-NULL semantic, not a
	// sentinel value.
	var n int
	if err := db.conn.QueryRow(
		"SELECT count(*) FROM people WHERE id = 2 AND age IS NULL").Scan(&n); err != nil {
		t.Fatalf("count IS NULL: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 row with age IS NULL, got %d", n)
	}
}

// TestDbBindArg_PassThroughNonMaybe asserts the helper doesn't
// disturb non-Maybe arguments. The reflect walk should bail out on
// the first non-Maybe-shaped value and return it unchanged.
func TestDbBindArg_PassThroughNonMaybe(t *testing.T) {
	cases := []struct {
		in  any
		out any
	}{
		{42, 42},
		{"hello", "hello"},
		{nil, nil},
		{true, true},
		{3.14, 3.14},
	}
	for _, tc := range cases {
		got := dbBindArg(tc.in)
		if got != tc.out {
			t.Errorf("dbBindArg(%v) = %v; want %v", tc.in, got, tc.out)
		}
	}
}

// TestDbBindArg_UnwrapsJust asserts the helper drills into a Just
// to surface the underlying value.
func TestDbBindArg_UnwrapsJust(t *testing.T) {
	if got := dbBindArg(Just[int](42)); got != 42 {
		t.Errorf("dbBindArg(Just 42) = %v; want 42", got)
	}
	if got := dbBindArg(Just[string]("ok")); got != "ok" {
		t.Errorf("dbBindArg(Just \"ok\") = %v; want \"ok\"", got)
	}
}

// TestDbBindArg_NothingIsNil asserts Nothing → nil so the
// database/sql driver writes SQL NULL.
func TestDbBindArg_NothingIsNil(t *testing.T) {
	if got := dbBindArg(Nothing[int]()); got != nil {
		t.Errorf("dbBindArg(Nothing) = %v; want nil", got)
	}
	if got := dbBindArg(Nothing[string]()); got != nil {
		t.Errorf("dbBindArg(Nothing String) = %v; want nil", got)
	}
}

// TestDbQuery_BindsMaybeInWhereClause exercises the round-trip
// for `Db_query` (vs `Db_exec` above) so both kernel sites are
// covered. Issuing `WHERE id = ?` with `Just 1` should return the
// matching row.
func TestDbQuery_BindsMaybeInWhereClause(t *testing.T) {
	db := openNullableTestDb(t)
	if _, err := db.conn.Exec(
		"INSERT INTO people (id, name, age) VALUES (1, 'alice', 30)"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	queryTask := Db_query(
		db,
		"SELECT id, name FROM people WHERE id = ?",
		[]any{Just[int](1)},
	)
	result := AnyTaskRun(queryTask)
	sr, ok := result.(SkyResult[any, any])
	if !ok || sr.Tag != 0 {
		t.Fatalf("Db_query failed: %v", result)
	}
	rows, ok := sr.OkValue.([]any)
	if !ok {
		t.Fatalf("expected []any rows, got %T", sr.OkValue)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row matching id=Just 1, got %d", len(rows))
	}
}

// ── #577 regression: nullable decoder must handle real SQL NULL ──
//
// Pre-fix this errored with "expected Int, got rt.SkyMaybe[interface
// {}]" because Db_query's normaliseSqlValue wraps SQL NULL as
// SkyMaybe[any]{Tag:1} BEFORE the row reaches the decoder, but the
// nullable kernel checked only `v == nil`. The unit tests above (line
// 76, 94) synthesise `row["middle_name"] = nil` directly and never
// hit the wrapped form — these tests go end-to-end through Db_query
// against in-memory SQLite + a literal `INSERT … VALUES (?, NULL)` so
// the SkyMaybe-wrap path is exercised.

// TestDbDecNullable_E2E_NullColumnReturnsNothing — the actual
// regression: queryDecode-style execution against a SQL row where
// the column is NULL must yield Nothing, not error.
func TestDbDecNullable_E2E_NullColumnReturnsNothing(t *testing.T) {
	db := openNullableTestDb(t)
	if _, err := db.conn.Exec(
		"INSERT INTO people (id, name, age) VALUES (1, 'alice', NULL)"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	queryRes := AnyTaskRun(Db_query(
		db, "SELECT id, name, age FROM people WHERE id = 1", []any{}))
	sr, ok := queryRes.(SkyResult[any, any])
	if !ok || sr.Tag != 0 {
		t.Fatalf("Db_query failed: %v", queryRes)
	}
	rows, _ := sr.OkValue.([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	row, ok := rows[0].(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any row, got %T", rows[0])
	}

	// Run a single nullable (int "age") decoder against the actual
	// row Db_query produced. This is what the user's `sky run`
	// stack hits.
	dec := DbDec_nullable(DbDec_int("age"))
	got := DbDec_run(dec, row)
	r, ok := got.(SkyResult[any, any])
	if !ok {
		t.Fatalf("expected SkyResult, got %T", got)
	}
	if r.Tag != 0 {
		t.Fatalf("nullable decoded a NULL row as Err: %v\n"+
			"hint: this is the #577 SkyMaybe-wrap regression — "+
			"normaliseSqlValue at db_auth.go:976 converts SQL NULL "+
			"to SkyMaybe[any]{Tag:1}, the nullable gate must "+
			"recognise that form not just raw nil.", r.ErrValue)
	}
	m, ok := r.OkValue.(SkyMaybe[any])
	if !ok {
		t.Fatalf("expected SkyMaybe[any], got %T", r.OkValue)
	}
	if m.Tag != 1 {
		t.Errorf("expected Nothing (tag 1), got tag %d value %v", m.Tag, m.JustValue)
	}
}

// TestDbDecNullable_E2E_NonNullColumnReturnsJust — the matching
// happy path: a non-NULL int cell flows through nullable as
// Just (int value).
func TestDbDecNullable_E2E_NonNullColumnReturnsJust(t *testing.T) {
	db := openNullableTestDb(t)
	if _, err := db.conn.Exec(
		"INSERT INTO people (id, name, age) VALUES (1, 'alice', 42)"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	queryRes := AnyTaskRun(Db_query(
		db, "SELECT age FROM people WHERE id = 1", []any{}))
	sr, _ := queryRes.(SkyResult[any, any])
	rows, _ := sr.OkValue.([]any)
	row, _ := rows[0].(map[string]any)

	dec := DbDec_nullable(DbDec_int("age"))
	got := DbDec_run(dec, row)
	r, _ := got.(SkyResult[any, any])
	if r.Tag != 0 {
		t.Fatalf("nullable decoded a non-NULL row as Err: %v", r.ErrValue)
	}
	m, _ := r.OkValue.(SkyMaybe[any])
	if m.Tag != 0 {
		t.Errorf("expected Just, got Nothing")
	}
	if v := AsInt(m.JustValue); v != 42 {
		t.Errorf("expected 42, got %v", m.JustValue)
	}
}

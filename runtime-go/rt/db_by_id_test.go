package rt

// Regression coverage for the Std.Db by-id surface — getById / updateById /
// deleteById — discovered at GENUINE ZERO coverage by the v1 B8 audit.
//
// The defect these lock: all three kernels were written for an OLD signature
// (`Db -> String -> Int -> …`) and bound the id with `AsInt(capId)`. But the
// shipped Std.Db signatures take the id as a `String`
// (`getById : Db -> String -> String -> Task Error (Maybe (Dict String String))`),
// so any well-typed Sky call passing the id as a String hit
// `rt.AsInt: expected numeric value, got string` and panicked — an
// "if it compiles it works" break. getById ALSO returned `Ok(bareDict)` /
// `Err(NotFound)` instead of the `Maybe` its type advertises, so even a numeric
// id would coerce-fail on the caller's `case … of Just … / Nothing`.
//
// dbBindId is the fix: a base-10 integer String binds as int64 (PostgreSQL's
// `integer = text` has no implicit cast), a non-numeric String binds unchanged
// (OAuth-subject / text-PK ids), and an already-numeric id passes through (the
// Go-side callers like Auth.setRole). getById now returns Just/Nothing.

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func openByIdTestDb(t *testing.T) *SkyDb {
	t.Helper()
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := conn.Exec(`CREATE TABLE items (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		qty INTEGER NOT NULL
	)`); err != nil {
		t.Fatalf("create items: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO items (id, name, qty) VALUES (1,'widget',10),(2,'gadget',20)`); err != nil {
		t.Fatalf("seed items: %v", err)
	}
	return &SkyDb{conn: conn, driver: "sqlite"}
}

// A String id that is a base-10 integer must be accepted (not panic on AsInt)
// and return Just(row).
func TestDb_getById_StringId_ReturnsJust(t *testing.T) {
	db := openByIdTestDb(t)
	res := runTask(t, Db_getById(db, "items", "1"))
	if res.Tag != 0 {
		t.Fatalf("getById returned Err: %+v", res.ErrValue)
	}
	m, ok := res.OkValue.(SkyMaybe[any])
	if !ok {
		t.Fatalf("getById OkValue is not a Maybe: %T (%v)", res.OkValue, res.OkValue)
	}
	if m.Tag != 0 {
		t.Fatalf("getById of an existing row returned Nothing, want Just")
	}
	row := m.JustValue
	if got := getStringField(row, "name"); got != "widget" {
		t.Errorf("getById name: got %q want widget", got)
	}
}

// An absent id returns Ok(Nothing) — NOT an Err, and NOT a panic.
func TestDb_getById_AbsentId_ReturnsNothing(t *testing.T) {
	db := openByIdTestDb(t)
	res := runTask(t, Db_getById(db, "items", "999"))
	if res.Tag != 0 {
		t.Fatalf("getById of an absent row returned Err (want Ok Nothing): %+v", res.ErrValue)
	}
	m, ok := res.OkValue.(SkyMaybe[any])
	if !ok {
		t.Fatalf("getById OkValue is not a Maybe: %T", res.OkValue)
	}
	if m.Tag != 1 {
		t.Fatalf("getById of an absent row returned Just, want Nothing")
	}
}

// updateById with a String id updates exactly the addressed row.
func TestDb_updateById_StringId(t *testing.T) {
	db := openByIdTestDb(t)
	res := runTask(t, Db_updateById(db, "items", "1", map[string]any{"qty": "99"}))
	if res.Tag != 0 {
		t.Fatalf("updateById returned Err: %+v", res.ErrValue)
	}
	if n, _ := res.OkValue.(int); n != 1 {
		t.Fatalf("updateById affected rows: got %v want 1", res.OkValue)
	}
	rows := mustRows(t, db, "SELECT id, qty FROM items ORDER BY id")
	if got, _ := rows[0]["qty"].(int64); got != 99 {
		t.Errorf("row 1 qty after update: got %v want 99", rows[0]["qty"])
	}
	if got, _ := rows[1]["qty"].(int64); got != 20 {
		t.Errorf("row 2 qty must be untouched: got %v want 20", rows[1]["qty"])
	}
}

// deleteById with a String id removes exactly the addressed row.
func TestDb_deleteById_StringId(t *testing.T) {
	db := openByIdTestDb(t)
	res := runTask(t, Db_deleteById(db, "items", "1"))
	if res.Tag != 0 {
		t.Fatalf("deleteById returned Err: %+v", res.ErrValue)
	}
	if n, _ := res.OkValue.(int); n != 1 {
		t.Fatalf("deleteById affected rows: got %v want 1", res.OkValue)
	}
	gone := runTask(t, Db_getById(db, "items", "1"))
	m, _ := gone.OkValue.(SkyMaybe[any])
	if gone.Tag != 0 || m.Tag != 1 {
		t.Fatalf("row 1 should be gone (Ok Nothing), got tag=%d maybe=%+v", gone.Tag, m)
	}
	still := runTask(t, Db_getById(db, "items", "2"))
	m2, _ := still.OkValue.(SkyMaybe[any])
	if still.Tag != 0 || m2.Tag != 0 {
		t.Fatalf("row 2 must survive deleteById(1)")
	}
}

// dbBindId is the unit under all three: a base-10 integer String → int64;
// a non-numeric String → unchanged (OAuth-subject / text-PK ids); an already-
// numeric id → unchanged.
func TestDbBindId_Normalisation(t *testing.T) {
	if got := dbBindId("42"); got != int64(42) {
		t.Errorf("dbBindId(\"42\"): got %v (%T) want int64(42)", got, got)
	}
	if got := dbBindId("  7 "); got != int64(7) {
		t.Errorf("dbBindId with surrounding space: got %v want int64(7)", got)
	}
	if got := dbBindId("user|abc123"); got != "user|abc123" {
		t.Errorf("dbBindId(non-numeric): got %v want the string unchanged", got)
	}
	if got := dbBindId(5); got != 5 {
		t.Errorf("dbBindId(int): got %v want 5 unchanged", got)
	}
}

// A non-numeric text primary key must round-trip through getById — proving
// dbBindId does not force every id to an integer (an OAuth subject would then
// never match).
func TestDb_getById_TextPrimaryKey(t *testing.T) {
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := conn.Exec(`CREATE TABLE accounts (id TEXT PRIMARY KEY, label TEXT NOT NULL)`); err != nil {
		t.Fatalf("create accounts: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO accounts (id, label) VALUES ('user|abc123','Ada')`); err != nil {
		t.Fatalf("seed accounts: %v", err)
	}
	db := &SkyDb{conn: conn, driver: "sqlite"}
	res := runTask(t, Db_getById(db, "accounts", "user|abc123"))
	if res.Tag != 0 {
		t.Fatalf("getById(text pk) returned Err: %+v", res.ErrValue)
	}
	m, ok := res.OkValue.(SkyMaybe[any])
	if !ok || m.Tag != 0 {
		t.Fatalf("getById(text pk) did not return Just: %T %+v", res.OkValue, res.OkValue)
	}
	if got := getStringField(m.JustValue, "label"); got != "Ada" {
		t.Errorf("label: got %q want Ada", got)
	}
}

// Auth.setRole declares `Task Error ()`, so its Ok value must be unit, not the
// affected-row Int that Db_updateById returns — the raw Int made a well-typed
// caller CoerceFailure ("source int cannot be cast to target struct {}").
func TestAuth_setRole_ReturnsUnit(t *testing.T) {
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := conn.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, role TEXT NOT NULL DEFAULT 'user')`); err != nil {
		t.Fatalf("create users: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO users (id, role) VALUES (1,'user')`); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	db := &SkyDb{conn: conn, driver: "sqlite"}
	res := runTask(t, Auth_setRole(db, 1, "admin"))
	if res.Tag != 0 {
		t.Fatalf("setRole returned Err: %+v", res.ErrValue)
	}
	if _, isInt := res.OkValue.(int); isInt {
		t.Fatalf("setRole Ok value is an int (%v) — must be unit for `Task Error ()`", res.OkValue)
	}
	if _, isUnit := res.OkValue.(struct{}); !isUnit {
		t.Fatalf("setRole Ok value is %T, want unit struct{}{}", res.OkValue)
	}
	rows := mustRows(t, db, "SELECT role FROM users WHERE id = 1")
	if rows[0]["role"] != "admin" {
		t.Errorf("role after setRole: got %v want admin", rows[0]["role"])
	}
}

// getStringField reads a column from a Sky row value (the shape Db_query
// produces) by name, via the same kernel Sky's Db.getString routes through.
func getStringField(row any, field string) string {
	return Db_getString(field, row)
}

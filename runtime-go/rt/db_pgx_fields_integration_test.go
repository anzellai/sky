//go:build integration
// +build integration

package rt

// Real-Postgres arm for the field-builder kernels — `Db.updateFields`,
// `Db.insertFields`, `Db.insertFieldsReturning`.
//
// db_pgx_placeholder_test.go proves the STATEMENT is dialect-correct without a
// server, so the per-push suite has a gate. This file proves the statement
// actually EXECUTES on the engine, which is the claim that matters and the one
// no amount of string assertion can make. Before the `d.rebind(...)` fix all
// three failed here against PostgreSQL 14 with
// `ERROR: syntax error at or near "," (SQLSTATE 42601)` — Postgres rejects the
// `?` while parsing. (The `unused argument: 0` reported by the apps/ledger
// Layer-2 gate is the same defect surfacing through pgx's own argument check on
// a single-placeholder statement; the engine error depends on the statement
// shape, the cause is identical.)
//
// Gated on SKY_TEST_POSTGRES_DSN, matching live_store_postgres_test.go:
//
//	SKY_TEST_POSTGRES_DSN='postgres://sky@/skytest?host=/tmp/skpg&port=55432&sslmode=disable' \
//	  go test -tags integration ./rt/ -run PgFields

import (
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// openPgFieldsDb opens a real Postgres connection and gives the test a private
// table, dropped and recreated so a previous run cannot mask a failure.
func openPgFieldsDb(t *testing.T) *SkyDb {
	t.Helper()
	dsn := requirePostgresDSN(t)
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	if err := conn.Ping(); err != nil {
		t.Skipf("postgres unreachable at SKY_TEST_POSTGRES_DSN: %v", err)
	}
	for _, stmt := range []string{
		`DROP TABLE IF EXISTS pgfields_items`,
		`CREATE TABLE pgfields_items (
			id     SERIAL PRIMARY KEY,
			name   TEXT NOT NULL DEFAULT 'unnamed',
			status TEXT NOT NULL DEFAULT 'pending',
			note   TEXT,
			qty    INTEGER
		)`,
	} {
		if _, err := conn.Exec(stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
	t.Cleanup(func() {
		conn.Exec(`DROP TABLE IF EXISTS pgfields_items`)
		conn.Close()
	})
	return &SkyDb{conn: conn, driver: "pgx"}
}

func TestPgFields_insertFields_ExecutesOnPostgres(t *testing.T) {
	db := openPgFieldsDb(t)
	res := runTask(t, Db_insertFields(db, "pgfields_items", []any{
		pair("name", setField(sqlValue("SqlString", "Widget"))),
		pair("status", omitField()), // DEFAULT 'pending'
		pair("qty", setField(sqlValue("SqlInt", 3))),
	}))
	if res.Tag != 0 {
		t.Fatalf("insertFields on Postgres: %+v", res.ErrValue)
	}
	if n, _ := res.OkValue.(int); n != 1 {
		t.Fatalf("affected rows: got %v want 1", res.OkValue)
	}
	rows := mustRows(t, db, `SELECT name, status, qty FROM pgfields_items`)
	if len(rows) != 1 {
		t.Fatalf("row count: got %d want 1", len(rows))
	}
	if got := rows[0]["name"]; got != "Widget" {
		t.Errorf("name: got %v want Widget", got)
	}
	if got := rows[0]["status"]; got != "pending" {
		t.Errorf("status (DEFAULT via OmitField): got %v want pending", got)
	}
}

func TestPgFields_updateFields_ExecutesOnPostgres(t *testing.T) {
	db := openPgFieldsDb(t)
	if res := runTask(t, Db_insertFields(db, "pgfields_items", []any{
		pair("name", setField(sqlValue("SqlString", "Before"))),
		pair("qty", setField(sqlValue("SqlInt", 1))),
	})); res.Tag != 0 {
		t.Fatalf("seed insert: %+v", res.ErrValue)
	}
	// Two SET columns and a WHERE column: three placeholders, so a
	// mis-numbered rewrite (all `$1`) is caught as well as a missing one.
	res := runTask(t, Db_updateFields(db, "pgfields_items",
		[]any{pair("name", sqlValue("SqlString", "Before"))},
		[]any{
			pair("name", setField(sqlValue("SqlString", "After"))),
			pair("qty", setField(sqlValue("SqlInt", 9))),
		},
	))
	if res.Tag != 0 {
		t.Fatalf("updateFields on Postgres: %+v", res.ErrValue)
	}
	if n, _ := res.OkValue.(int); n != 1 {
		t.Fatalf("affected rows: got %v want 1", res.OkValue)
	}
	rows := mustRows(t, db, `SELECT name, qty FROM pgfields_items`)
	if len(rows) != 1 {
		t.Fatalf("row count: got %d want 1", len(rows))
	}
	if got := rows[0]["name"]; got != "After" {
		t.Errorf("name: got %v want After", got)
	}
	if got := AsIntOrZero(rows[0]["qty"]); got != 9 {
		t.Errorf("qty: got %v want 9", got)
	}
}

// Store.insert's assigned-id contract on a real Postgres. The unit gate covers
// SQLite; this covers the dialect where the id can only come from RETURNING,
// there being no LastInsertId at all.
func TestPgFields_StoreInsert_ReturnsAssignedIdOnPostgres(t *testing.T) {
	db := openPgFieldsDb(t)
	ins := func(name string) int {
		t.Helper()
		res := runTask(t, Db_execObjectWith(db, "pgfields_items",
			colspec([2]string{"name", "text"}),
			storeObj([]string{"name"}, []any{name}),
			[]any{}, "id", "int"))
		if res.Tag != 0 {
			t.Fatalf("Store.insert %q: %+v", name, res.ErrValue)
		}
		return AsIntOrZero(res.OkValue)
	}
	first, second := ins("first"), ins("second")
	if first == second {
		t.Fatalf("Store.insert reported the same id %d for two rows on Postgres", first)
	}
	rows := mustRows(t, db, `SELECT id, name FROM pgfields_items ORDER BY id`)
	if len(rows) != 2 {
		t.Fatalf("row count: got %d want 2", len(rows))
	}
	if got := AsIntOrZero(rows[0]["id"]); got != first {
		t.Errorf("first insert returned %d but the row has id %d", first, got)
	}
	if got := AsIntOrZero(rows[1]["id"]); got != second {
		t.Errorf("second insert returned %d but the row has id %d", second, got)
	}
}

func TestPgFields_insertFieldsReturning_ExecutesOnPostgres(t *testing.T) {
	db := openPgFieldsDb(t)
	res := runTask(t, Db_insertFieldsReturning(db, "pgfields_items", []any{
		pair("name", setField(sqlValue("SqlString", "Returned"))),
		pair("qty", setField(sqlValue("SqlInt", 5))),
	}, "id", makeIntDecoder("id")))
	if res.Tag != 0 {
		t.Fatalf("insertFieldsReturning on Postgres: %+v", res.ErrValue)
	}
	ids := AsList(res.OkValue)
	if len(ids) != 1 {
		t.Fatalf("RETURNING rows: got %d want 1", len(ids))
	}
	// SERIAL starts at 1; the point is a real id came back, not rows-affected.
	if got := AsIntOrZero(ids[0]); got < 1 {
		t.Errorf("returned id: got %v want >= 1", got)
	}
}

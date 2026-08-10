package rt

// Regression gate: every Std.Db kernel that BUILDS its own SQL must hand the
// driver dialect-correct placeholders.
//
// `Db_updateFields`, `Db_insertFields` and `Db_insertFieldsReturning` compose
// their statements with literal `?` (db_auth.go: `col+" = ?"` and the
// `placeholders` slice in `dbBuildInsertFields`).  `Db_exec` / `Db_query`
// launder that through `d.rebind(q)`; these three called `Exec`/`Query`
// DIRECTLY, so on Postgres the `?` reached pgx verbatim and it failed with
// `unused argument: 0`.
//
// This was invisible because the repo had zero Postgres coverage. A real-engine
// test alone would not fix that: it can only run where a server exists. So the
// gate is two-legged —
//
//   - THIS file: no server needed, runs in the default `go test ./...`. A
//     recording driver captures the exact string handed to database/sql, and
//     the assertion is that a pgx-driver Db emits `$n` and NOT `?`.
//   - db_pgx_fields_integration_test.go: the same three verbs against a real
//     Postgres, proving the rewritten SQL actually executes.
//
// The sqlite arm is asserted here too, because the fix must not disturb the
// dialect that already worked.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"strings"
	"sync"
	"testing"
)

// ─── recording driver ──────────────────────────────────────────────
// Captures the query text reaching database/sql without needing an engine.
// Registered once; each connection is handed a *recorder via the DSN key.

type recorder struct {
	mu      sync.Mutex
	queries []string
}

func (r *recorder) add(q string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queries = append(r.queries, q)
}

func (r *recorder) last() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.queries) == 0 {
		return ""
	}
	return r.queries[len(r.queries)-1]
}

var (
	recMu     sync.Mutex
	recByName = map[string]*recorder{}
	recOnce   sync.Once
)

type recDriver struct{}

func (recDriver) Open(name string) (driver.Conn, error) {
	recMu.Lock()
	defer recMu.Unlock()
	r, ok := recByName[name]
	if !ok {
		r = &recorder{}
		recByName[name] = r
	}
	return &recConn{rec: r}, nil
}

type recConn struct{ rec *recorder }

func (c *recConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (c *recConn) Close() error                        { return nil }
func (c *recConn) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }

// ExecerContext / QueryerContext keep database/sky off the Prepare path so the
// verbatim statement text is what we record.
func (c *recConn) ExecContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	c.rec.add(q)
	return driver.RowsAffected(1), nil
}

func (c *recConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	c.rec.add(q)
	return &recRows{}, nil
}

// recRows reports one column and zero rows: enough for the RETURNING kernel to
// reach its decode step without an engine.
type recRows struct{}

func (r *recRows) Columns() []string              { return []string{"id"} }
func (r *recRows) Close() error                   { return nil }
func (r *recRows) Next(dest []driver.Value) error { return io.EOF }

// newRecordingDb returns a *SkyDb whose driver field is `driver` (the dialect
// under test) but whose connection is the recorder.
func newRecordingDb(t *testing.T, dialect string) (*SkyDb, *recorder) {
	t.Helper()
	recOnce.Do(func() { sql.Register("skyrec", recDriver{}) })
	name := t.Name() + "/" + dialect
	recMu.Lock()
	r := &recorder{}
	recByName[name] = r
	recMu.Unlock()
	conn, err := sql.Open("skyrec", name)
	if err != nil {
		t.Fatalf("open recording driver: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return &SkyDb{conn: conn, driver: dialect}, r
}

// assertPlaceholders is the shared verdict: on pgx the statement must carry
// `$1…` and no bare `?`; on sqlite it must still carry `?` and no `$n`.
func assertPlaceholders(t *testing.T, dialect, verb, gotSQL string) {
	t.Helper()
	if gotSQL == "" {
		t.Fatalf("%s/%s: no statement reached the driver", verb, dialect)
	}
	switch dialect {
	case "pgx":
		if strings.Contains(gotSQL, "?") {
			t.Errorf("%s on Postgres handed pgx a `?` placeholder — pgx rejects "+
				"this with `unused argument: 0`.\n  statement: %s", verb, gotSQL)
		}
		if !strings.Contains(gotSQL, "$1") {
			t.Errorf("%s on Postgres: expected a `$1` placeholder.\n  statement: %s",
				verb, gotSQL)
		}
	case "sqlite":
		if strings.Contains(gotSQL, "$1") {
			t.Errorf("%s on sqlite was rewritten to `$n` — the dialect that worked "+
				"must be untouched.\n  statement: %s", verb, gotSQL)
		}
		if !strings.Contains(gotSQL, "?") {
			t.Errorf("%s on sqlite: expected a `?` placeholder.\n  statement: %s",
				verb, gotSQL)
		}
	}
}

func TestDb_updateFields_DialectPlaceholders(t *testing.T) {
	for _, dialect := range []string{"pgx", "sqlite"} {
		t.Run(dialect, func(t *testing.T) {
			db, rec := newRecordingDb(t, dialect)
			res := runTask(t, Db_updateFields(db, "items",
				[]any{pair("id", sqlValue("SqlInt", 7))},
				[]any{pair("name", setField(sqlValue("SqlString", "Widget")))},
			))
			if res.Tag != 0 {
				t.Fatalf("updateFields errored: %+v", res.ErrValue)
			}
			assertPlaceholders(t, dialect, "Db.updateFields", rec.last())
		})
	}
}

func TestDb_insertFields_DialectPlaceholders(t *testing.T) {
	for _, dialect := range []string{"pgx", "sqlite"} {
		t.Run(dialect, func(t *testing.T) {
			db, rec := newRecordingDb(t, dialect)
			res := runTask(t, Db_insertFields(db, "items", []any{
				pair("name", setField(sqlValue("SqlString", "Widget"))),
				pair("qty", setField(sqlValue("SqlInt", 3))),
			}))
			if res.Tag != 0 {
				t.Fatalf("insertFields errored: %+v", res.ErrValue)
			}
			assertPlaceholders(t, dialect, "Db.insertFields", rec.last())
		})
	}
}

func TestDb_insertFieldsReturning_DialectPlaceholders(t *testing.T) {
	for _, dialect := range []string{"pgx", "sqlite"} {
		t.Run(dialect, func(t *testing.T) {
			db, rec := newRecordingDb(t, dialect)
			// Zero rows come back, so the decoder is never applied; the
			// statement text is the subject under test.
			_ = runTask(t, Db_insertFieldsReturning(db, "items", []any{
				pair("name", setField(sqlValue("SqlString", "Widget"))),
			}, "id", makeIntDecoder("id")))
			assertPlaceholders(t, dialect, "Db.insertFieldsReturning", rec.last())
		})
	}
}

// Control: Db.exec already routed through rebind. Pinned so a refactor that
// centralises placeholder handling cannot regress the verb that worked.
func TestDb_exec_DialectPlaceholders_Control(t *testing.T) {
	for _, dialect := range []string{"pgx", "sqlite"} {
		t.Run(dialect, func(t *testing.T) {
			db, rec := newRecordingDb(t, dialect)
			res := runTask(t, Db_exec(db, "UPDATE items SET name = ? WHERE id = ?",
				[]any{"Widget", 7}))
			if res.Tag != 0 {
				t.Fatalf("exec errored: %+v", res.ErrValue)
			}
			assertPlaceholders(t, dialect, "Db.exec", rec.last())
		})
	}
}

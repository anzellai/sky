//go:build integration
// +build integration

package rt

// Real-PostgreSQL arm of the list parameter (db_list_param.go): the server
// types `col = ANY($n)` as an array of the column's type, and the slice
// pgListArgs binds encodes into it for every column type the Store compares
// (BIGINT, DOUBLE PRECISION, BOOLEAN, TEXT, NUMERIC, JSONB, TIMESTAMPTZ and a
// v0.28 TEXT column holding an int, float or bool), with the same rows as
// `col IN ($1, $2, …)`; and 70,000 values run where 70,000 placeholders fail.
//
//	SKY_TEST_POSTGRES_DSN=postgres://… go test -tags integration ./rt/ -run PgListParam

import (
	"database/sql"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// openPgListParamDb opens the database on pgx's extended protocol, or with
// `simple` on the simple protocol the app's pool runs (db_auth.go).
func openPgListParamDb(t *testing.T, simple bool) (*sql.DB, *SkyDb) {
	t.Helper()
	dsn := requirePostgresDSN(t)
	if simple {
		cfg, err := pgx.ParseConfig(dsn)
		if err != nil {
			t.Fatalf("parse dsn: %v", err)
		}
		cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
		dsn = stdlib.RegisterConnConfig(cfg)
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	if err := conn.Ping(); err != nil {
		t.Fatalf("postgres unreachable at SKY_TEST_POSTGRES_DSN: %v", err)
	}
	for _, stmt := range []string{
		`DROP TABLE IF EXISTS pglist_items`,
		`CREATE TABLE pglist_items (
			id   TEXT PRIMARY KEY,
			n    BIGINT,
			i4   INTEGER,
			r    DOUBLE PRECISION,
			b    BOOLEAN,
			t    TEXT,
			d    NUMERIC,
			j    JSONB,
			ts   TIMESTAMPTZ,
			lt   TEXT
		)`,
		`INSERT INTO pglist_items VALUES
			('a', 1, 1, 0.1, true, 'Ada', 12.30, '{"k":1}', '2026-01-02T03:04:05Z', 'true'),
			('b', 2, 2, 2.5, false, 'ada', 7, '[1,2]', '2026-01-03T00:00:00Z', '7'),
			('c', 9223372036854775807, 3, 1e21, true, 'Bo', -0.5, '"s"', '2026-01-04T00:00:00Z', '2.5'),
			('e', NULL, NULL, NULL, NULL, 'a"b\c', NULL, NULL, NULL, NULL),
			('z', NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL)`,
	} {
		if _, err := conn.Exec(stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
	t.Cleanup(func() {
		conn.Exec(`DROP TABLE IF EXISTS pglist_items`)
		conn.Close()
	})
	return conn, &SkyDb{conn: conn, driver: "pgx"}
}

func pgListIds(t *testing.T, d *SkyDb, where string, args ...any) string {
	t.Helper()
	rows, err := d.executor().Query(d.rebind(`SELECT id FROM pglist_items WHERE `+where+` ORDER BY id`), args...)
	if err != nil {
		t.Fatalf("%s: %v", where, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%s: %v", where, err)
	}
	return strings.Join(out, ",")
}

func TestPgListParam_MatchesInListForEveryColumnType(t *testing.T) {
	for _, simple := range []bool{false, true} {
		_, d := openPgListParamDb(t, simple)
		pgListMatchesInList(t, d, simple)
	}
}

func pgListMatchesInList(t *testing.T, d *SkyDb, simple bool) {
	cases := []struct {
		col  string
		args []any
	}{
		{"n", []any{int64(1), int64(9223372036854775807), int64(5), int64(1)}},
		{"i4", []any{int64(3), int64(4)}},
		{"r", []any{0.1, 1e21, int64(2)}},
		{"b", []any{true}},
		{"b", []any{false, false}},
		{"t", []any{"ada", "Bo", "x", `a"b\c`, "NULL", "{x}"}},
		{"d", []any{"12.3", "7", "-0.50", int64(7)}},
		{"j", []any{`{"k": 1}`, `"s"`}},
		{"ts", []any{"2026-01-02T03:04:05Z", "2026-01-04 00:00:00+00"}},
		{"lt", []any{true, int64(7), 2.5}},
	}
	for _, c := range cases {
		marks := "?" + strings.Repeat(", ?", len(c.args)-1)
		list := "?/*sky-list:" + strconv.Itoa(len(c.args)) + "*/"
		want := pgListIds(t, d, c.col+" IN ("+marks+")", c.args...)
		got := pgListIds(t, d, c.col+" = ANY("+list+")", c.args...)
		if got != want || want == "" || (c.col == "t" && !strings.Contains(want, "e")) {
			t.Fatalf("%s (simple protocol %v): ANY = %q, IN = %q", c.col, simple, got, want)
		}
		wantNot := pgListIds(t, d, "NOT ("+c.col+" IN ("+marks+"))", c.args...)
		gotNot := pgListIds(t, d, "NOT ("+c.col+" = ANY("+list+"))", c.args...)
		if gotNot != wantNot {
			t.Fatalf("NOT %s (simple protocol %v): ANY = %q, IN = %q", c.col, simple, gotNot, wantNot)
		}
	}
}

func TestPgListParam_SeventyThousandValues(t *testing.T) {
	args := make([]any, 70000)
	for i := range args {
		args[i] = int64(i)
	}
	for _, simple := range []bool{false, true} {
		_, d := openPgListParamDb(t, simple)
		if got := pgListIds(t, d, "n = ANY(?/*sky-list:70000*/)", args...); got != "a,b" {
			t.Fatalf("70,000 values (simple protocol %v): %q, want a,b", simple, got)
		}
	}
	// The extended protocol refuses 70,000 placeholders.
	_, d := openPgListParamDb(t, false)
	_, err := d.executor().Query(d.rebind(`SELECT id FROM pglist_items WHERE n IN (?`+strings.Repeat(", ?", 69999)+`)`), args...)
	if err == nil || !strings.Contains(err.Error(), "65535") {
		t.Fatalf("70,000 placeholders: err = %v, want the 65535 parameter limit", err)
	}
}

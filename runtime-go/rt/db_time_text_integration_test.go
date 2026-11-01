//go:build integration
// +build integration

package rt

// Real-PostgreSQL arm of db_time_text_test.go: timestamptz, timestamp,
// date and time columns read as PostgreSQL's own text form (as `::text`
// gives on a UTC server), whatever the time zone of the app process. Runs
// in release.yml gate-race ("Postgres session-store integration tests",
// -run Postgres).

import (
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func openTimeTextPostgres(t *testing.T) *SkyDb {
	t.Helper()
	dsn := requirePostgresDSN(t)
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := conn.Ping(); err != nil {
		t.Fatalf("postgres unreachable at SKY_TEST_POSTGRES_DSN: %v", err)
	}
	return &SkyDb{conn: conn, driver: "pgx"}
}

const pgTimeTextSelect = "SELECT TIMESTAMPTZ '2026-11-01 02:13:48.987333+00' AS tz, " +
	"TIMESTAMPTZ '2026-11-01 12:13:48+10' AS tz0, " +
	"TIMESTAMP '2026-11-01 02:13:48.5' AS ts, " +
	"DATE '2026-11-01' AS d, DATE '0044-03-15 BC' AS bc, " +
	"TIME '02:13:48' AS tm, TIME '02:13:48.25' AS tmf, " +
	"TIMESTAMPTZ 'infinity' AS inf"

var pgTimeTextWant = map[string]string{
	"tz":  "2026-11-01 02:13:48.987333+00",
	"tz0": "2026-11-01 02:13:48+00",
	"ts":  "2026-11-01 02:13:48.5",
	"d":   "2026-11-01",
	"bc":  "0044-03-15 BC",
	"tm":  "02:13:48",
	"tmf": "02:13:48.25",
	"inf": "infinity",
}

func TestPostgresTimeColumnsReadAsStableText(t *testing.T) {
	d := openTimeTextPostgres(t)
	assertQueryTimeText(t, d, pgTimeTextSelect, pgTimeTextWant)

	// The same text as PostgreSQL's own `::text` on a UTC session.
	tx, err := d.conn.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("SET LOCAL TIME ZONE 'UTC'"); err != nil {
		t.Fatalf("set time zone: %v", err)
	}
	var tz, ts, dd, tm string
	if err := tx.QueryRow("SELECT TIMESTAMPTZ '2026-11-01 02:13:48.987333+00'::text, "+
		"TIMESTAMP '2026-11-01 02:13:48.5'::text, DATE '2026-11-01'::text, TIME '02:13:48'::text").
		Scan(&tz, &ts, &dd, &tm); err != nil {
		t.Fatalf("::text: %v", err)
	}
	for col, pg := range map[string]string{"tz": tz, "ts": ts, "d": dd, "tm": tm} {
		if pgTimeTextWant[col] != pg {
			t.Errorf("column %q: Sky reads %q, PostgreSQL ::text on UTC is %q", col, pgTimeTextWant[col], pg)
		}
	}
}

func TestPostgresStoreTimeStringFieldsRoundTrip(t *testing.T) {
	d := openTimeTextPostgres(t)
	const table = "ev_time_text_pg"
	for _, stmt := range []string{
		"DROP TABLE IF EXISTS " + table,
		"CREATE TABLE " + table + " (id TEXT PRIMARY KEY, tz TIMESTAMPTZ, ts TIMESTAMP, d DATE, tm TIME)",
		"INSERT INTO " + table + " VALUES ('e1', '2026-11-01 02:13:48.987333+00', " +
			"'2026-11-01 02:13:48.5', '2026-11-01', '02:13:48')",
	} {
		if _, err := d.conn.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	t.Cleanup(func() { _, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table) })

	const sel = "SELECT id, tz, ts, d, tm FROM " + table + " WHERE id = 'e1'"
	assertStoreTimeText(t, d, sel, []string{"id", "tz", "ts", "d", "tm"},
		`{"id":"e1","tz":"2026-11-01 02:13:48.987333+00","ts":"2026-11-01 02:13:48.5",`+
			`"d":"2026-11-01","tm":"02:13:48"}`)

	// The text read back is a valid parameter: written as a new row, it
	// stores the same values, under a non-UTC process zone too.
	for _, loc := range timeTextZones {
		withProcessZone(t, loc, func() {
			res := runTask(t, Db_query(d, "SELECT tz, ts, d, tm FROM "+table+" WHERE id = 'e1'", []any{}))
			if res.Tag != 0 {
				t.Fatalf("[TZ %s] query: %v", loc, res.ErrValue)
			}
			row := AsList(res.OkValue)[0]
			args := []any{}
			for _, c := range []string{"tz", "ts", "d", "tm"} {
				args = append(args, Db_getField(c, row))
			}
			if r := runTask(t, Db_exec(d, "INSERT INTO "+table+" VALUES ('e2', $1, $2, $3, $4)", args)); r.Tag != 0 {
				t.Fatalf("[TZ %s] write back: %v", loc, r.ErrValue)
			}
			var same bool
			if err := d.conn.QueryRow("SELECT a.tz = b.tz AND a.ts = b.ts AND a.d = b.d AND a.tm = b.tm FROM " +
				table + " a, " + table + " b WHERE a.id = 'e1' AND b.id = 'e2'").Scan(&same); err != nil {
				t.Fatalf("[TZ %s] compare: %v", loc, err)
			}
			if !same {
				t.Errorf("[TZ %s] the written-back row differs from the original", loc)
			}
			if _, err := d.conn.Exec("DELETE FROM " + table + " WHERE id = 'e2'"); err != nil {
				t.Fatalf("delete: %v", err)
			}
		})
	}

	// An Int field over a time column stays a decode error.
	cs := colspec([2]string{"id", "text"}, [2]string{"tz", "int"})
	res, _ := AnyTaskRun(Db_queryObjects(d, sel, []any{}, cs)).(SkyResult[any, any])
	if res.Tag == 0 {
		t.Errorf("an Int field over a timestamptz column decoded: %v", res.OkValue)
	}
}

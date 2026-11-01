//go:build !js

package rt

// A database time value read as text must not depend on the time zone of
// the app process. Before the fix a time column reached the String readers
// (`Db.query` + `Db.getField`, a Store `String` field) as Go's
// time.Time.String() text in the process's local zone, so the same stored
// value read "2026-11-01 02:13:48 +0000 UTC" on one host and
// "2026-11-01 12:13:48 +1000 AEST" on another. This file is the SQLite arm.
// db_time_text_integration_test.go is the PostgreSQL arm.

import (
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// timeTextZones are the process zones every case runs under: UTC, a zone
// ahead of UTC with a half-hour part, and a zone behind UTC.
var timeTextZones = []*time.Location{
	time.UTC,
	time.FixedZone("ACST", 9*3600+1800),
	time.FixedZone("EST", -5*3600),
}

// withProcessZone runs f with time.Local set to loc.
func withProcessZone(t *testing.T, loc *time.Location, f func()) {
	t.Helper()
	saved := time.Local
	time.Local = loc
	defer func() { time.Local = saved }()
	f()
}

// assertQueryTimeText checks every column of the single row `q` returns
// reads as want[col] through Db.query + Db.getField, under every zone in
// timeTextZones.
func assertQueryTimeText(t *testing.T, db *SkyDb, q string, want map[string]string) {
	t.Helper()
	for _, loc := range timeTextZones {
		withProcessZone(t, loc, func() {
			res := runTask(t, Db_query(db, q, []any{}))
			if res.Tag != 0 {
				t.Fatalf("[TZ %s] query failed: %v", loc, res.ErrValue)
			}
			rows := AsList(res.OkValue)
			if len(rows) != 1 {
				t.Fatalf("[TZ %s] want 1 row, got %d", loc, len(rows))
			}
			for col, w := range want {
				if got := Db_getField(col, rows[0]); got != w {
					t.Errorf("[TZ %s] Db.getField %q = %q, want %q", loc, col, got, w)
				}
			}
		})
	}
}

// assertStoreTimeText checks a Store read with every column as a `String`
// field gives wantJSON, under every zone in timeTextZones.
func assertStoreTimeText(t *testing.T, db *SkyDb, q string, cols []string, wantJSON string) {
	t.Helper()
	specs := make([][2]string, len(cols))
	for i, c := range cols {
		specs[i] = [2]string{c, "text"}
	}
	cs := colspec(specs...)
	for _, loc := range timeTextZones {
		withProcessZone(t, loc, func() {
			res, _ := AnyTaskRun(Db_queryObjects(db, q, []any{}, cs)).(SkyResult[any, any])
			if res.Tag != 0 {
				t.Fatalf("[TZ %s] Store query failed: %v", loc, res.ErrValue)
			}
			rows := AsList(res.OkValue)
			if len(rows) != 1 || AsString(rows[0]) != wantJSON {
				t.Errorf("[TZ %s] Store rows = %v, want [%s]", loc, rows, wantJSON)
			}
		})
	}
}

func openTimeTextSqlite(t *testing.T) *SkyDb {
	t.Helper()
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { conn.Close() })
	return &SkyDb{conn: conn, driver: "sqlite"}
}

// SQLite keeps a time as the text the app wrote. The driver parses a TEXT
// value in a DATE, DATETIME or TIMESTAMP column into a time.Time, and Sky
// renders it back in SQLite's own text form.
func TestSqliteTimeColumnsReadAsStableText(t *testing.T) {
	d := openTimeTextSqlite(t)
	for _, stmt := range []string{
		"CREATE TABLE ev (id TEXT, d DATE, dt DATETIME, ts TIMESTAMP, tz TIMESTAMP, tm TIME, dc DATE)",
		"INSERT INTO ev VALUES ('e1', '2026-11-01', '2026-11-01 02:13:48.5', " +
			"'2026-11-01 02:13:48', '2026-11-01T10:00:00+02:00', '02:13:48', '2026-11-01 07:30')",
	} {
		if _, err := d.conn.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	want := map[string]string{
		"d":  "2026-11-01",
		"dt": "2026-11-01 02:13:48.5",
		"ts": "2026-11-01 02:13:48",
		"tz": "2026-11-01 10:00:00+02:00",
		"tm": "02:13:48",
		"dc": "2026-11-01 07:30:00",
	}
	assertQueryTimeText(t, d, "SELECT d, dt, ts, tz, tm, dc FROM ev", want)
	assertStoreTimeText(t, d, "SELECT id, d, dt, ts, tz, tm, dc FROM ev",
		[]string{"id", "d", "dt", "ts", "tz", "tm", "dc"},
		`{"id":"e1","d":"2026-11-01","dt":"2026-11-01 02:13:48.5","ts":"2026-11-01 02:13:48",`+
			`"tz":"2026-11-01 10:00:00+02:00","tm":"02:13:48","dc":"2026-11-01 07:30:00"}`)

	// The text read back is a valid parameter: written to the same columns
	// it reads back unchanged.
	res := runTask(t, Db_query(d, "SELECT d, dt, ts, tz, tm, dc FROM ev", []any{}))
	if res.Tag != 0 {
		t.Fatalf("query: %v", res.ErrValue)
	}
	row := AsList(res.OkValue)[0]
	args := []any{}
	for _, c := range []string{"d", "dt", "ts", "tz", "tm", "dc"} {
		args = append(args, Db_getField(c, row))
	}
	if r := runTask(t, Db_exec(d, "INSERT INTO ev VALUES ('e2', ?, ?, ?, ?, ?, ?)", args)); r.Tag != 0 {
		t.Fatalf("write back: %v", r.ErrValue)
	}
	assertQueryTimeText(t, d, "SELECT d, dt, ts, tz, tm, dc FROM ev WHERE id = 'e2'", want)
}

// The rendering itself, for the shapes no driver test above reaches: a
// timestamptz in a non-UTC process zone, a BC year, a year past 9999 and
// an offset with a minutes part.
func TestDbTimeTextFormats(t *testing.T) {
	inst := time.Date(2026, 11, 1, 2, 13, 48, 987333000, time.UTC)
	for _, c := range []struct {
		name, dbType string
		v            time.Time
		want         string
	}{
		{"timestamptz in a non-UTC zone", "TIMESTAMPTZ", inst.In(time.FixedZone("X", 9*3600+1800)), "2026-11-01 02:13:48.987333+00"},
		{"timestamptz whole second", "TIMESTAMPTZ", time.Date(2026, 11, 1, 2, 13, 48, 0, time.UTC), "2026-11-01 02:13:48+00"},
		{"timestamp", "TIMESTAMP", inst, "2026-11-01 02:13:48.987333"},
		{"date", "DATE", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), "2026-11-01"},
		{"date BC", "DATE", time.Date(-43, 3, 15, 0, 0, 0, 0, time.UTC), "0044-03-15 BC"},
		{"timestamptz BC", "TIMESTAMPTZ", time.Date(-43, 3, 15, 12, 0, 0, 0, time.UTC), "0044-03-15 12:00:00+00 BC"},
		{"date past 9999", "DATE", time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), "10000-01-01"},
		{"sqlite offset with minutes", "DATETIME", time.Date(2026, 11, 1, 10, 0, 0, 0, time.FixedZone("", 5*3600+1800)), "2026-11-01 10:00:00+05:30"},
		{"sqlite negative offset", "TIMESTAMP", time.Date(2026, 11, 1, 10, 0, 0, 0, time.FixedZone("", -4*3600)), "2026-11-01 10:00:00-04:00"},
		{"unknown type reads as an instant", "", inst.In(time.FixedZone("Y", -5*3600)), "2026-11-01 02:13:48.987333+00"},
	} {
		if got := dbTimeText(c.v, c.dbType); got != c.want {
			t.Errorf("%s: dbTimeText = %q, want %q", c.name, got, c.want)
		}
	}
	for in, want := range map[string]string{
		"02:13:48.000000": "02:13:48",
		"02:13:48.250000": "02:13:48.25",
		"02:13:48.987333": "02:13:48.987333",
		"02:13:48":        "02:13:48",
		"24:00:00.000000": "24:00:00",
		"not a time":      "not a time",
	} {
		if got := pgTimeOfDayText(in); got != want {
			t.Errorf("pgTimeOfDayText(%q) = %q, want %q", in, got, want)
		}
	}
}

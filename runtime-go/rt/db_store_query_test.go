package rt

// Std.Db.Store query path, SQLite arm (v0.29.0, batch A).
//
// B1: dbQueryRows dropped an error that surfaced after the first row. modernc
// runs sqlite3_step inside rows.Next, so a malformed JSON value in row 2 ended
// the loop with rows.Err() set, and the caller got Ok with one row. A short
// list that looks complete is a silent wrong answer.
//
// B2: the colspec flag suffix was parsed loosely. `touch` was a substring
// match, so `Store.defaultText "status" "untouched"` made Store.update stamp
// `status` with the clock. A `defaultText` value was put in raw, so a `|` in
// it injected flags. Sky now percent-encodes `%` and `|` in a default value,
// and Go splits on `|`, compares each flag exactly and decodes the value.

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func openStoreQueryDb(t *testing.T) *SkyDb {
	t.Helper()
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { conn.Close() })
	return &SkyDb{conn: conn, driver: "sqlite"}
}

// assertMidIterationErrorSurfaces is shared by the SQLite and PostgreSQL arms.
// Row 1 holds valid JSON and row 2 does not. The query has no ORDER BY, so the
// engine streams row 1 before it evaluates row 2.
func assertMidIterationErrorSurfaces(t *testing.T, d *SkyDb, table, query string) {
	t.Helper()
	for _, stmt := range []string{
		"DROP TABLE IF EXISTS " + table,
		"CREATE TABLE " + table + " (id INTEGER, props TEXT)",
		"INSERT INTO " + table + " (id, props) VALUES (1, '{\"a\":1}')",
		"INSERT INTO " + table + " (id, props) VALUES (2, 'not json')",
	} {
		if _, err := d.conn.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	t.Cleanup(func() { _, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table) })
	res, ok := AnyTaskRun(Db_query(d, query, []any{})).(SkyResult[any, any])
	if !ok {
		t.Fatalf("Db.query returned a non-Result")
	}
	if res.Tag == 0 {
		t.Fatalf("Db.query returned Ok with %d row(s); the malformed row 2 must make it Err",
			len(AsList(res.OkValue)))
	}
}

func TestStoreQueryB1_SqliteMidIterationErrorIsErr(t *testing.T) {
	d := openStoreQueryDb(t)
	assertMidIterationErrorSurfaces(t, d, "events_b1",
		"SELECT json_extract(props, '$.a') AS a FROM events_b1")
}

func TestStoreQueryB2_TouchFlagIsExact(t *testing.T) {
	cases := []struct {
		kind  string
		touch bool
	}{
		{"text|touch|dnow", true},
		{"text|dnow|touch", true},
		{"text|dtext=untouched", false},
		{"text|dtext=touch", false},
		{"text?|dtext=retouch", false},
		{"text", false},
	}
	for _, c := range cases {
		if got := codecColIsTouch(c.kind); got != c.touch {
			t.Errorf("codecColIsTouch(%q) = %v, want %v", c.kind, got, c.touch)
		}
	}
}

func TestStoreQueryB2_DefaultTextValueIsDecodedNotParsed(t *testing.T) {
	cases := []struct {
		kind   string
		unique bool
		defK   string
		defV   string
	}{
		// What Sky sends for defaultText "note" "a|u": `|` encoded as %7C.
		{"text|dtext=a%7Cu", false, "text", "a|u"},
		// defaultText "note" "a|dnow" must stay a text default.
		{"text|dtext=a%7Cdnow", false, "text", "a|dnow"},
		// defaultText "note" "a|b%c": both characters survive.
		{"text|dtext=a%7Cb%25c", false, "text", "a|b%c"},
		// A literal "%7C" in the value is sent as "%257C".
		{"text|dtext=%257C", false, "text", "%7C"},
		{"text|u|dtext=x", true, "text", "x"},
		{"int|dint=7", false, "int", "7"},
	}
	for _, c := range cases {
		u, k, v := codecColExtras(c.kind)
		if u != c.unique || k != c.defK || v != c.defV {
			t.Errorf("codecColExtras(%q) = (%v, %q, %q), want (%v, %q, %q)",
				c.kind, u, k, v, c.unique, c.defK, c.defV)
		}
	}
}

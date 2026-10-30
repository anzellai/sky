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

// Batch B: Db_queryObjectGroups, the read kernel of every Store query. A join
// row comes back as `a__<col>` and `b__<col>` result columns; the kernel gives
// one JSON object per group with the prefix stripped, and the JSON text `null`
// for the right group of a LEFT JOIN when its marker column is NULL.

func storeGroups(groups ...[3]any) []any {
	out := make([]any, 0, len(groups))
	for _, g := range groups {
		out = append(out, SkyTuple3{V0: g[0], V1: g[1], V2: g[2]})
	}
	return out
}

// assertJoinGroups is shared by the SQLite and PostgreSQL arms. The aliases
// are quoted and camelCase, so on PostgreSQL the test also proves the kernel
// reads a quoted alias with its case.
func assertJoinGroups(t *testing.T, d *SkyDb, suffix string) {
	t.Helper()
	left, right := "bills_grp"+suffix, "clients_grp"+suffix
	for _, stmt := range []string{
		"DROP TABLE IF EXISTS " + left,
		"DROP TABLE IF EXISTS " + right,
		"CREATE TABLE " + left + " (id TEXT, client_id TEXT, amount INTEGER)",
		"CREATE TABLE " + right + " (id TEXT, name TEXT)",
		"INSERT INTO " + left + " (id, client_id, amount) VALUES ('b1', 'c1', 10)",
		"INSERT INTO " + left + " (id, client_id, amount) VALUES ('b2', 'c9', 20)",
		"INSERT INTO " + right + " (id, name) VALUES ('c1', 'Ada')",
	} {
		if _, err := d.conn.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	t.Cleanup(func() {
		_, _ = d.conn.Exec("DROP TABLE IF EXISTS " + left)
		_, _ = d.conn.Exec("DROP TABLE IF EXISTS " + right)
	})
	q := `SELECT a.id AS "a__id", a.client_id AS "a__clientId", a.amount AS "a__amount", ` +
		`b.id AS "b__id", b.name AS "b__name" FROM ` + left + ` a LEFT JOIN ` + right +
		` b ON a.client_id = b.id ORDER BY a.id ASC`
	groups := storeGroups(
		[3]any{"a__", "", colspec([2]string{"id", "text"}, [2]string{"clientId", "text"}, [2]string{"amount", "int"})},
		[3]any{"b__", "id", colspec([2]string{"id", "text"}, [2]string{"name", "text"})},
	)
	res, ok := AnyTaskRun(Db_queryObjectGroups(d, q, []any{}, groups)).(SkyResult[any, any])
	if !ok || res.Tag != 0 {
		t.Fatalf("Db_queryObjectGroups: %#v", res)
	}
	var got [][]string
	for _, row := range AsList(res.OkValue) {
		var texts []string
		for _, g := range AsList(row) {
			texts = append(texts, AsString(g))
		}
		got = append(got, texts)
	}
	want := [][]string{
		{`{"id":"b1","clientId":"c1","amount":10}`, `{"id":"c1","name":"Ada"}`},
		{`{"id":"b2","clientId":"c9","amount":20}`, `null`},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rows %v, want %v", len(got), got, want)
	}
	for i := range want {
		if len(got[i]) != 2 || got[i][0] != want[i][0] || got[i][1] != want[i][1] {
			t.Errorf("row %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestStoreQueryJoinGroups_SqliteStripsPrefixesAndNullsTheMissingSide(t *testing.T) {
	assertJoinGroups(t, openStoreQueryDb(t), "")
}

// An error from the query is the kernel's Err, not an empty list.
func TestStoreQueryJoinGroups_SqliteQueryErrorIsErr(t *testing.T) {
	d := openStoreQueryDb(t)
	groups := storeGroups([3]any{"", "", colspec([2]string{"n", "int"})})
	res, ok := AnyTaskRun(Db_queryObjectGroups(d, "SELECT n FROM no_such_table", []any{}, groups)).(SkyResult[any, any])
	if !ok || res.Tag == 0 {
		t.Fatalf("a query on a missing table returned %#v, want Err", res)
	}
}

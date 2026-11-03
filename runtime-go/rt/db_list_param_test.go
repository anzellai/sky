//go:build !js

package rt

import (
	"database/sql"
	"math"
	"math/rand"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestParseListMark(t *testing.T) {
	for body, want := range map[string]listMark{
		"3":                {n: 3},
		"1001:reals":       {n: 1001, reals: true},
		"5:t|flag":         {n: 5, table: "t", col: "flag"},
		"7:reals:t|score":  {n: 7, reals: true, table: "t", col: "score"},
		"70000:reals:a1|b": {n: 70000, reals: true, table: "a1", col: "b"},
	} {
		got, ok := parseListMark(body)
		if !ok || got != want {
			t.Fatalf("%q = %+v %v, want %+v", body, got, ok, want)
		}
	}
	for _, bad := range []string{"", "0", "-1", "x", "3:", "3:other", "3:t", "3:t|c:x", "3:t;x|c", "3:reals:reals"} {
		if _, ok := parseListMark(bad); ok {
			t.Fatalf("%q was read as a list mark", bad)
		}
	}
}

func TestListMarksFindTheirPlaceholders(t *testing.T) {
	q := `SELECT '?/*sky-list:2*/' FROM t /* ? */ WHERE a = ? AND b IN (SELECT +value FROM json_each(?/*sky-list:3*/)) -- ?
AND c = ?/*sky:t|c*/ AND d IN (SELECT +value FROM json_each(?/*sky-list:2:t|d*/))`
	want := map[int]listMark{1: {n: 3}, 3: {n: 2, table: "t", col: "d"}}
	if got := sqliteListMarks(q); !reflect.DeepEqual(got, want) {
		t.Fatalf("sqlite marks = %+v, want %+v", got, want)
	}
	pq := `SELECT * FROM t WHERE a = $1 AND b = ANY($2/*sky-list:3*/) AND c = ANY($12/*sky-list:2*/) AND d = $3`
	if got := pgListMarks(pq); !reflect.DeepEqual(got, map[int]listMark{1: {n: 3}, 11: {n: 2}}) {
		t.Fatalf("pg marks = %+v", got)
	}
}

func TestPgListArgsCollapseEachListIntoOneArrayLiteral(t *testing.T) {
	q := `x = $1 AND y = ANY($2/*sky-list:4*/) AND z = $3 AND w = ANY($4/*sky-list:2*/)`
	got := pgListArgs(q, []any{"a", int64(1), 2.5, "s\"\\", true, "b", nil, int64(-3)})
	want := []any{"a", `{"1","2.5","s\"\\","true"}`, "b", `{NULL,"-3"}`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
	// Too few arguments, or a value that has no array text: unchanged, so
	// the statement fails on its argument count.
	short := []any{"a", int64(1)}
	if got := pgListArgs(q, short); !reflect.DeepEqual(got, short) {
		t.Fatalf("short args = %#v", got)
	}
	bytesArg := []any{"a", []byte("x"), int64(1), int64(2), int64(3), "b", int64(1), int64(2)}
	if got := pgListArgs(q, bytesArg); !reflect.DeepEqual(got, bytesArg) {
		t.Fatalf("bytes args = %#v", got)
	}
}

func TestPgArrayLiteralQuotesEveryElement(t *testing.T) {
	got, ok := pgArrayLiteral([]any{int64(math.MinInt64), 0.1, 1e21, false, `a"b\c`, "", "NULL", "{x,y}", nil})
	want := `{"-9223372036854775808","0.1","1e+21","false","a\"b\\c","","NULL","{x,y}",NULL}`
	if !ok || got != want {
		t.Fatalf("literal = %s, want %s", got, want)
	}
	if _, ok := pgArrayLiteral([]any{math.NaN()}); ok {
		t.Fatal("NaN has no array text")
	}
}

func TestFloatSignificandIsExact(t *testing.T) {
	for _, f := range []float64{0, 1, -1, 7, 0.1, math.MaxFloat64, -math.SmallestNonzeroFloat64, 2.2250738585072014e-308, 5.167133041957456e-308, 1e21} {
		m, e := floatSignificand(f)
		if got := math.Ldexp(float64(m), int(e)); got != f || e < -1074 || m > 1<<53 || m < -(1<<53) {
			t.Fatalf("%v = %d * 2^%d gives %v", f, m, e, got)
		}
	}
	got, ok := sqliteListJSON([]any{int64(1), 0.5, "x", true, nil}, true, false)
	if !ok || got != `[1,[4503599627370496,-53],"x",true,null]` {
		t.Fatalf("reals JSON = %s", got)
	}
	got, _ = sqliteListJSON([]any{true, 7.0, int64(7), 1e21, "x"}, false, true)
	if got != `["true","7","7","1e+21","x"]` {
		t.Fatalf("legacy JSON = %s", got)
	}
}

func openListParamSqlite(t *testing.T) (*sql.DB, *SkyDb) {
	t.Helper()
	conn, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "list.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn, &SkyDb{conn: conn, driver: "sqlite"}
}

// The subquery Std.Db.Store writes for a Float column on SQLite (listParamSql
// in Std/Db/Store.sky), around a list placeholder with mark body `mark`.
func sqliteRealsSubquery(mark string) string {
	return `SELECT CASE WHEN type = 'array' THEN json_extract(value, '$[0]') * pow(2, json_extract(value, '$[1]')) ELSE value END FROM json_each(?/*sky-list:` + mark + `*/)`
}

func queryCount(t *testing.T, ex dbExecutor, q string, args ...any) int {
	t.Helper()
	var n int
	if err := ex.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%.120s: %v", q, err)
	}
	return n
}

func queryIds(t *testing.T, ex dbExecutor, q string, args ...any) string {
	t.Helper()
	rows, err := ex.Query(q, args...)
	if err != nil {
		t.Fatalf("%.120s: %v", q, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		out = append(out, s)
	}
	return strings.Join(out, ",")
}

// A REAL column compares each float exactly, as one bound parameter per
// value does, including near the bottom of the double range where SQLite's
// own reading of a JSON number is not exact.
func TestSqliteListParamFloatsCompareExactly(t *testing.T) {
	conn, d := openListParamSqlite(t)
	ex := d.executor()
	var raw float64
	if err := conn.QueryRow(`SELECT +value FROM json_each(?)`, `[5.167133041957456e-308]`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw == 5.167133041957456e-308 {
		t.Log("this SQLite reads 5.167133041957456e-308 exactly; the [m, e] form is still checked below")
	}
	if _, err := conn.Exec(`CREATE TABLE f (id INTEGER PRIMARY KEY, r REAL)`); err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(7))
	vals := []float64{5.167133041957456e-308, -4.418896780900564e-308, 0, math.Copysign(0, -1), 0.1, 0.2, 0.3, 1.0 / 3,
		math.MaxFloat64, math.SmallestNonzeroFloat64, -math.MaxFloat64, 1e21, 1 << 60, 123456789.123456789, 2.2250738585072014e-308}
	for len(vals) < 20000 {
		f := math.Float64frombits(rng.Uint64())
		if !math.IsNaN(f) && !math.IsInf(f, 0) {
			vals = append(vals, f)
		}
	}
	tx, _ := conn.Begin()
	for i, v := range vals {
		if _, err := tx.Exec(`INSERT INTO f (id, r) VALUES (?, ?)`, i, v); err != nil {
			t.Fatal(err)
		}
	}
	tx.Commit()
	q := `SELECT count(*) FROM f WHERE r IN (` + sqliteRealsSubquery("2:reals") + `) AND id = ?`
	for i, v := range vals {
		near := math.Nextafter(v, math.Inf(1))
		if math.IsInf(near, 0) {
			near = math.Nextafter(v, math.Inf(-1))
		}
		if n := queryCount(t, ex, q, near, v, i); n != 1 {
			t.Fatalf("%v did not match its own row", v)
		}
		if n := queryCount(t, ex, q, near, near, i); n != 0 {
			t.Fatalf("the next double after %v matched it", v)
		}
	}
}

// 70,000 values are one parameter: the statement runs where 70,000
// placeholders fail with "too many SQL variables".
func TestSqliteListParamTakesSeventyThousandValues(t *testing.T) {
	conn, d := openListParamSqlite(t)
	if _, err := conn.Exec(`CREATE TABLE n (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec("INSERT INTO n (id) VALUES (5), (69999), (70000)"); err != nil {
		t.Fatal(err)
	}
	args := make([]any, 70000)
	for i := range args {
		args[i] = int64(i)
	}
	if n := queryCount(t, d.executor(), `SELECT count(*) FROM n WHERE id IN (SELECT +value FROM json_each(?/*sky-list:70000*/))`, args...); n != 2 {
		t.Fatalf("matched %d rows, want 2", n)
	}
	var n int
	err := conn.QueryRow(`SELECT count(*) FROM n WHERE id IN (?`+strings.Repeat(", ?", 69999)+`)`, args...).Scan(&n)
	if err == nil || !strings.Contains(err.Error(), "too many SQL variables") {
		t.Fatalf("70,000 placeholders: err = %v, want too many SQL variables", err)
	}
}

// Each column kind matches the same rows through the list parameter as
// through one placeholder per value, and under NOT; text that is not valid
// UTF-8 keeps its bytes.
func TestSqliteListParamMatchesInList(t *testing.T) {
	conn, d := openListParamSqlite(t)
	ex := d.executor()
	bad := string([]byte{0xff, 'a'})
	if _, err := conn.Exec(`CREATE TABLE k (id TEXT PRIMARY KEY, n INTEGER, r REAL, b BOOLEAN, t TEXT, d TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO k VALUES ('a', 1, 0.1, 1, 'Ada', '0'), ('b', 2, 2.5, 0, 'ada', '7'), ('c', -3, 7, 1, 'a"b\c', '7.0'), ('d', 4, 8, 0, ?, 'x'), ('z', NULL, NULL, NULL, NULL, NULL)`, bad); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		col, flags string
		vals       []any
	}{
		{"n", "", []any{int64(1), int64(-3), int64(1)}},
		{"r", "reals", []any{0.1, int64(7), 3.5}},
		{"b", "", []any{true, true}},
		{"t", "", []any{"ada", `a"b\c`, "x", bad, "\n"}},
		// A TEXT column (a decimal column, a v0.28 nested Maybe Int) reads an
		// int as its text, as it does a bound int.
		{"d", "", []any{int64(0), int64(7)}},
		{"t", "", []any{int64(1)}},
	} {
		n := strconv.Itoa(len(c.vals))
		sub := `SELECT +value FROM json_each(?/*sky-list:` + n + `*/)`
		if c.flags != "" {
			sub = sqliteRealsSubquery(n + ":" + c.flags)
		}
		ph := "?" + strings.Repeat(", ?", len(c.vals)-1)
		for _, not := range []bool{false, true} {
			in, list := c.col+" IN ("+ph+")", c.col+" IN ("+sub+")"
			if not {
				in, list = "NOT ("+in+")", "NOT ("+list+")"
			}
			want := queryIds(t, conn, `SELECT id FROM k WHERE `+in+` ORDER BY id`, c.vals...)
			got := queryIds(t, ex, `SELECT id FROM k WHERE `+list+` ORDER BY id`, c.vals...)
			if got != want || (!not && want == "" && c.col != "t") || (c.col == "t" && len(c.vals) > 1 && !not && !strings.Contains(want, "d")) {
				t.Fatalf("%s (not %v): list = %q, IN = %q", c.col, not, got, want)
			}
		}
	}
}

// A list mark naming a v0.28 TEXT column of a nullable Bool or Float binds
// the text v0.28 wrote for each value; on a BOOLEAN / REAL column it binds
// the values as they are.
func TestSqliteListMarkFollowsTheDeclaredType(t *testing.T) {
	conn, d := openListParamSqlite(t)
	ex := d.executor()
	for _, q := range []string{
		`CREATE TABLE old (id TEXT PRIMARY KEY, flag TEXT, score TEXT)`,
		`INSERT INTO old VALUES ('a', 'true', '7'), ('b', 'false', '2.5'), ('c', 'true', '1e+21'), ('d', NULL, NULL)`,
		`CREATE TABLE cur (id TEXT PRIMARY KEY, flag BOOLEAN, score REAL)`,
		`INSERT INTO cur VALUES ('a', 1, 7), ('b', 0, 2.5), ('c', 1, 1e21), ('d', NULL, NULL)`,
	} {
		if _, err := conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(table, col string, vals ...any) string {
		t.Helper()
		n := strconv.Itoa(len(vals))
		sub := `SELECT +value FROM json_each(?/*sky-list:` + n + `:` + table + `|` + col + `*/)`
		if col == "score" {
			sub = sqliteRealsSubquery(n + ":reals:" + table + "|" + col)
		}
		return queryIds(t, ex, `SELECT id FROM `+table+` WHERE `+col+` IN (`+sub+`) ORDER BY id`, vals...)
	}
	for _, table := range []string{"old", "cur"} {
		if got := ids(table, "flag", true); got != "a,c" {
			t.Fatalf("%s flag IN [true] = %q, want a,c", table, got)
		}
		if got := ids(table, "flag", false, false); got != "b" {
			t.Fatalf("%s flag IN [false] = %q, want b", table, got)
		}
		if got := ids(table, "score", 7.0, 2.5, 1e21); got != "a,b,c" {
			t.Fatalf("%s score IN [7, 2.5, 1e21] = %q, want a,b,c", table, got)
		}
		if got := ids(table, "score", int64(7), 3.5); got != "a" {
			t.Fatalf("%s score IN [7 (an Int), 3.5] = %q, want a", table, got)
		}
	}
}

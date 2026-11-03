//go:build !js

package rt

// One parameter for a whole value list (Std.Db.Store `inList`).
//
// `col IN (?, ?, …)` binds one parameter per value, and each engine caps the
// parameters of one statement: SQLite at 32,766 ("too many SQL variables"),
// PostgreSQL at 65,535 on pgx's extended protocol ("extended protocol limited
// to 65535 parameters"). A condition is one statement, so it cannot be split.
// Above `listParamFrom` values (Std/Db/Store.sky) the Store writes ONE
// placeholder for the list, marked with the number of values it takes:
//
//	PostgreSQL  col = ANY(?/*sky-list:N*/)
//	SQLite      col IN (SELECT +value FROM json_each(?/*sky-list:N*/))
//
// and passes the N values as N arguments, exactly as it passes them to
// `IN (?, …)`. Before the statement runs, the executor replaces those N
// arguments with one (pgListArgs, sqliteListArgs), built from the values as
// they would have been bound one by one (sqlValueToGo):
//
//   - PostgreSQL: the text of an array literal, every element quoted
//     (`{"1","2.5","true","a\"b"}`, NULL for a nil). The app's pool runs
//     pgx's simple protocol (db_auth.go), which inlines a string as an untyped
//     literal; the extended protocol sends it as text for the array type the
//     server infers. Either way the server reads it as an array of the
//     column's type, each element through that type's input (BIGINT, DOUBLE
//     PRECISION, BOOLEAN, NUMERIC, JSONB, TIMESTAMPTZ, and TEXT, where an int,
//     float or bool element is the text pgArg binds into a TEXT column).
//     `x = ANY(array)` is how PostgreSQL runs `x IN (…)` itself.
//   - SQLite: a JSON array. json_each gives each value with the type it was
//     bound with (an int as INTEGER, a bool as 1 / 0, a string as TEXT). Its
//     `value` column has NUMERIC affinity, which `+value` takes away, so the
//     comparison takes the column's affinity and collation as `IN (…)` does
//     (a TEXT column reads an int as its text, exactly as for a bound int). SQLite's own text-to-double reading is not exact below about
//     1e-305 (5.167133041957456e-308 reads as 5.167133041957457e-308), where
//     a bound double is, so a Float column's mark says `reals` and each float
//     is written `[m, e]`, its integer significand and power of two; the
//     subquery reads it back as `m * pow(2, e)`, a product of two exact
//     doubles that is the float itself. A nullable Bool or Float column's
//     mark also names the column (`N:table|col`, `N:reals:table|col`), and
//     when that column is a v0.28 TEXT column each value is written as the
//     text v0.28 wrote (sqliteLegacyText), as one marked value is.
//
// The mark stays in `Store.toSql`, so its SQL and parameters run unchanged
// through Db.query. A mark whose count does not fit the arguments, or a value
// of another type (bytes, a time), leaves the arguments as they are, and the
// statement then fails on its argument count rather than compare something
// else.

import (
	"bytes"
	"database/sql"
	"math"
	"strconv"
	"strings"
)

// listMarkOpen opens the mark of a list placeholder: `/*sky-list:N…*/`.
const listMarkOpen = "/*sky-list:"

// listMark is a parsed list mark.
type listMark struct {
	n          int
	reals      bool
	int        bool // PostgreSQL: an Int list naming its column (db_pgx_int_cast.go)
	table, col string
}

// parseListMark reads the body of a list mark: `N`, then optionally `reals`
// or `int`, then optionally `table|col`, separated by `:`.
func parseListMark(body string) (listMark, bool) {
	parts := strings.Split(body, ":")
	n, err := strconv.Atoi(parts[0])
	if err != nil || n < 1 {
		return listMark{}, false
	}
	m := listMark{n: n}
	rest := parts[1:]
	if len(rest) > 0 && rest[0] == "reals" {
		m.reals = true
		rest = rest[1:]
	} else if len(rest) > 0 && rest[0] == "int" {
		m.int = true
		rest = rest[1:]
	}
	if len(rest) > 0 {
		table, col, ok := strings.Cut(rest[0], "|")
		if !ok || !codecValidIdent(table) || !codecValidIdent(col) {
			return listMark{}, false
		}
		m.table, m.col = table, col
		rest = rest[1:]
	}
	if len(rest) > 0 {
		return listMark{}, false
	}
	return m, true
}

// listMarkAt reads a list mark at the start of `rest`, returning it and the
// length of its text.
func listMarkAt(rest string) (listMark, int, bool) {
	if !strings.HasPrefix(rest, listMarkOpen) {
		return listMark{}, 0, false
	}
	end := strings.Index(rest, "*/")
	if end < 0 {
		return listMark{}, 0, false
	}
	m, ok := parseListMark(rest[len(listMarkOpen):end])
	return m, end + 2, ok
}

// sqliteListMarks maps each list-marked `?` of `query` (by placeholder
// index) to its mark. `?` inside a string literal, a quoted identifier or a
// comment is not a placeholder, as in sqliteMarks.
func sqliteListMarks(query string) map[int]listMark {
	marks := map[int]listMark{}
	n := 0
	for i := 0; i < len(query); i++ {
		switch c := query[i]; c {
		case '\'', '"', '`':
			for i++; i < len(query) && query[i] != c; i++ {
			}
		case '-':
			if i+1 < len(query) && query[i+1] == '-' {
				for i < len(query) && query[i] != '\n' {
					i++
				}
			}
		case '/':
			if i+1 < len(query) && query[i+1] == '*' {
				end := strings.Index(query[i+2:], "*/")
				if end < 0 {
					return marks
				}
				i += end + 3
			}
		case '?':
			if m, size, ok := listMarkAt(query[i+1:]); ok {
				marks[n] = m
				i += size
			}
			n++
		}
	}
	return marks
}

// pgListMarks maps each `$k/*sky-list:N*/` of a rebound PostgreSQL statement
// to its mark, by placeholder index k-1.
func pgListMarks(query string) map[int]listMark {
	marks := map[int]listMark{}
	off := 0
	for {
		at := strings.Index(query[off:], listMarkOpen)
		if at < 0 {
			return marks
		}
		end := off + at
		start := end
		for start > 0 && query[start-1] >= '0' && query[start-1] <= '9' {
			start--
		}
		if start < end && start > 0 && query[start-1] == '$' {
			if k, err := strconv.Atoi(query[start:end]); err == nil && k >= 1 {
				if m, _, ok := listMarkAt(query[end:]); ok {
					marks[k-1] = m
				}
			}
		}
		off = end + len(listMarkOpen)
	}
}

// collapseListArgs replaces the N arguments of each list placeholder with the
// one `build` makes of them. Placeholders are numbered in order, each taking
// one argument except a list placeholder, which takes its mark's N. When the
// arguments do not fit, or `build` refuses a value, it returns `args`
// unchanged.
func collapseListArgs(marks map[int]listMark, args []any, build func(listMark, []any) (any, bool)) []any {
	if len(marks) == 0 {
		return args
	}
	out := make([]any, 0, len(args))
	a := 0
	for p := 0; a < len(args); p++ {
		m, ok := marks[p]
		if !ok {
			out = append(out, args[a])
			a++
			continue
		}
		if a+m.n > len(args) {
			return args
		}
		v, ok := build(m, args[a:a+m.n])
		if !ok {
			return args
		}
		out = append(out, v)
		a += m.n
	}
	for p := range marks {
		if p >= len(out) {
			return args // a marked placeholder found no arguments
		}
	}
	return out
}

// pgListArgs collapses each list placeholder's arguments into the text of a
// PostgreSQL array literal.
func pgListArgs(query string, args []any) []any {
	if !strings.Contains(query, listMarkOpen) {
		return args
	}
	return collapseListArgs(pgListMarks(query), args, func(_ listMark, vals []any) (any, bool) {
		return pgArrayLiteral(vals)
	})
}

// pgArrayLiteral writes values as a PostgreSQL array literal: nil as NULL,
// anything else double-quoted with `"` and `\` escaped. An int is its
// decimal, a float its shortest text (strconv 'g', -1, which PostgreSQL reads
// back to the same double), a bool `true` / `false`: the text pgArg gives
// each (db_pgx_args.go). Any other type is refused.
func pgArrayLiteral(vals []any) (string, bool) {
	var b strings.Builder
	b.WriteByte('{')
	for i, v := range vals {
		if i > 0 {
			b.WriteByte(',')
		}
		var t string
		switch x := v.(type) {
		case nil:
			b.WriteString("NULL")
			continue
		case int64:
			t = strconv.FormatInt(x, 10)
		case int:
			t = strconv.Itoa(x)
		case float64:
			if math.IsNaN(x) || math.IsInf(x, 0) {
				return "", false
			}
			t = strconv.FormatFloat(x, 'g', -1, 64)
		case bool:
			t = strconv.FormatBool(x)
		case string:
			t = x
		default:
			return "", false
		}
		b.WriteByte('"')
		for j := 0; j < len(t); j++ {
			if t[j] == '"' || t[j] == '\\' {
				b.WriteByte('\\')
			}
			b.WriteByte(t[j])
		}
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String(), true
}

// sqliteListArgs collapses each list placeholder's arguments into one JSON
// array (sqliteListJSON), reading whether a named column is a v0.28 TEXT
// column through the declared-type cache of db_sqlite_legacy.go.
func sqliteListArgs(ex dbExecutor, pool *sql.DB, query string, args []any) []any {
	if !strings.Contains(query, listMarkOpen) {
		return args
	}
	return collapseListArgs(sqliteListMarks(query), args, func(m listMark, vals []any) (any, bool) {
		text := false
		if m.table != "" && pool != nil {
			version, versionOk := sqliteSchemaVersion(ex)
			text = sqliteDeclCacheFor(pool).textColumns(ex, m.table, version, versionOk)[m.col]
		}
		return sqliteListJSON(vals, m.reals && !text, text)
	})
}

// sqliteListJSON writes values as a JSON array for json_each: nil as null,
// an int as a number, a bool as true / false, a string as a JSON string, a
// float as a number, or with `reals` as `[m, e]` (floatSignificand). With
// `legacyText` every value is the string v0.28 wrote (sqliteLegacyText).
// Any other type is refused.
func sqliteListJSON(vals []any, reals, legacyText bool) (string, bool) {
	var b bytes.Buffer
	b.WriteByte('[')
	for i, v := range vals {
		if i > 0 {
			b.WriteByte(',')
		}
		if legacyText && v != nil {
			v = sqliteLegacyText(v)
		}
		switch x := v.(type) {
		case nil:
			b.WriteString("null")
		case int64:
			b.WriteString(strconv.FormatInt(x, 10))
		case int:
			b.WriteString(strconv.Itoa(x))
		case float64:
			if math.IsNaN(x) || math.IsInf(x, 0) {
				return "", false
			}
			if reals {
				m, e := floatSignificand(x)
				b.WriteString("[" + strconv.FormatInt(m, 10) + "," + strconv.FormatInt(e, 10) + "]")
			} else {
				b.WriteString(strconv.FormatFloat(x, 'g', -1, 64))
			}
		case bool:
			if x {
				b.WriteString("true")
			} else {
				b.WriteString("false")
			}
		case string:
			writeJSONStringRaw(&b, x)
		default:
			return "", false
		}
	}
	b.WriteByte(']')
	return b.String(), true
}

// writeJSONStringRaw writes s as a JSON string, escaping `"`, `\` and the
// control characters, and copying every other byte as it is: a string that
// is not valid UTF-8 keeps its bytes, as it does bound as one parameter
// (encoding/json would replace them with U+FFFD).
func writeJSONStringRaw(b *bytes.Buffer, s string) {
	const hex = "0123456789abcdef"
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c < 0x20:
			b.WriteString(`\u00`)
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0xf])
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
}

// floatSignificand splits a finite float into m * 2^e, m an integer of at
// most 53 bits with the float's sign and e >= -1074, so 2^e is itself an
// exact double and m * 2^e is the float with no rounding.
func floatSignificand(f float64) (int64, int64) {
	bits := math.Float64bits(f)
	biased := int64(bits>>52) & 0x7ff
	frac := int64(bits & (1<<52 - 1))
	m, e := frac, int64(-1074)
	if biased != 0 {
		m, e = frac|1<<52, biased-1075
	}
	if bits>>63 != 0 {
		m = -m
	}
	return m, e
}

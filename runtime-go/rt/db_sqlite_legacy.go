//go:build !js

package rt

// SQLite argument binding for a v0.28 TEXT column of a nullable Float or Bool.
//
// v0.28 created the column of a `Codec.object` field
// `Codec.maybe (Codec.maybe Codec.bool)` (`Codec.float`) as TEXT and wrote
// the value as Go's %v text: `true` / `false`, `7`, `7.5`. v0.29 gives the
// field the kind `bool?` (`real?`) and binds a Go bool (float64), and migrate
// never alters a column. SQLite would store that as `1` / `7.0` in the TEXT
// column, and a condition binding 1 would compare as the text `1`, so it
// misses every v0.28 row (`true`) with no error, and the column ends up with
// two spellings.
//
// Std.Db.Store marks each placeholder of a nullable Float or Bool column on
// SQLite with a comment naming the column: `?/*sky:table|col*/` (paramOf in
// Std/Db/Store.sky, codecParam here for the statements built in Go). The
// executor below reads the column's declared type, once per connection pool
// and table, and binds the v0.28 text in place of the value when the column
// is TEXT, so writes keep one spelling and an `eq` matches old and new rows.
// A statement with no mark (every statement on a table with no such column,
// and every statement on PostgreSQL) is passed through untouched. An Int
// column needs no mark: SQLite stores an integer bound into TEXT as its
// decimal, the text v0.28 wrote. PostgreSQL binds the text form itself
// (db_pgx_args.go).
//
// The declared types are cached per pool and table. A schema statement run
// through a Db handle (CREATE / ALTER / DROP, Store.migrate, `sky db migrate`
// in this process) clears the pool's cache, and so does a rolled-back
// transaction (its body may have read a schema it changed). A column
// converted by another
// process is seen after the app restarts: docs/migration/v0.29.md says to
// convert with the app stopped.

import (
	"database/sql"
	"strconv"
	"strings"
	"sync"
)

const sqliteLegacyMark = "/*sky:"

// codecParam is the placeholder of column `col` of `table` (kind `kind`) in a
// statement built in Go: marked on SQLite for a nullable Float or Bool
// column, `?` otherwise. The same rule as paramOf in Std/Db/Store.sky.
func codecParam(driver, table, col, kind string) string {
	if driver == "pgx" {
		return "?"
	}
	base, nullable := codecSplitKind(kind)
	if !nullable || (base != "real" && base != "bool") || !codecValidIdent(table) || !codecValidIdent(col) {
		return "?"
	}
	return "?" + sqliteLegacyMark + table + "|" + col + "*/"
}

// codecParams is codecParam for each of `cols`, its kind read from the
// colspec (a column the colspec does not list, such as a `defaultWith`
// column, takes `?`).
func codecParams(driver, table string, colspecArg any, cols []string) []string {
	kinds := map[string]string{}
	if driver != "pgx" {
		for _, cs := range AsList(colspecArg) {
			t := AsTuple2(cs)
			kinds[AsString(t.V0)] = AsString(t.V1)
		}
	}
	out := make([]string, len(cols))
	for i, c := range cols {
		if k, ok := kinds[c]; ok {
			out[i] = codecParam(driver, table, c, k)
		} else {
			out[i] = "?"
		}
	}
	return out
}

// sqliteDeclCache holds, per table, whether each column has TEXT affinity.
type sqliteDeclCache struct {
	mu     sync.Mutex
	tables map[string]map[string]bool
}

var sqliteDeclCaches sync.Map // *sql.DB → *sqliteDeclCache

func sqliteDeclCacheFor(pool *sql.DB) *sqliteDeclCache {
	if c, ok := sqliteDeclCaches.Load(pool); ok {
		return c.(*sqliteDeclCache)
	}
	c, _ := sqliteDeclCaches.LoadOrStore(pool, &sqliteDeclCache{tables: map[string]map[string]bool{}})
	return c.(*sqliteDeclCache)
}

// sqliteDeclCacheReset forgets the declared types of every table of `pool`,
// after a schema change.
func sqliteDeclCacheReset(pool *sql.DB) {
	if pool != nil {
		sqliteDeclCaches.Delete(pool)
	}
}

// sqliteTextAffinity applies SQLite's affinity rules to a declared type: a
// type with INT is INTEGER, else one with CHAR, CLOB or TEXT is TEXT.
func sqliteTextAffinity(declared string) bool {
	t := strings.ToUpper(declared)
	if strings.Contains(t, "INT") {
		return false
	}
	return strings.Contains(t, "CHAR") || strings.Contains(t, "CLOB") || strings.Contains(t, "TEXT")
}

// textColumns reads (once) which columns of `table` have TEXT affinity. The
// PRAGMA runs on `ex`, so inside a transaction it uses the transaction's
// connection.
func (c *sqliteDeclCache) textColumns(ex dbExecutor, table string) map[string]bool {
	c.mu.Lock()
	cols, ok := c.tables[table]
	c.mu.Unlock()
	if ok {
		return cols
	}
	cols = map[string]bool{}
	rows, err := ex.Query(`PRAGMA table_info("` + table + `")`)
	if err != nil {
		return cols // not cached: the next statement asks again
	}
	defer rows.Close()
	names, err := rows.Columns()
	if err != nil {
		return cols
	}
	nameAt, typeAt := -1, -1
	for i, n := range names {
		switch n {
		case "name":
			nameAt = i
		case "type":
			typeAt = i
		}
	}
	if nameAt < 0 || typeAt < 0 {
		return cols
	}
	for rows.Next() {
		vals := make([]any, len(names))
		ptrs := make([]any, len(names))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return map[string]bool{}
		}
		cols[sqliteCellText(vals[nameAt])] = sqliteTextAffinity(sqliteCellText(vals[typeAt]))
	}
	if rows.Err() != nil {
		return map[string]bool{}
	}
	c.mu.Lock()
	c.tables[table] = cols
	c.mu.Unlock()
	return cols
}

func sqliteCellText(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case []byte:
		return string(s)
	}
	return ""
}

// sqliteMarks maps each marked `?` of `query` (by argument index) to its
// `table|col`. `?` inside a string literal, a quoted identifier or a comment
// is not a placeholder.
func sqliteMarks(query string) map[int]string {
	marks := map[int]string{}
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
			rest := query[i+1:]
			if strings.HasPrefix(rest, sqliteLegacyMark) {
				if end := strings.Index(rest, "*/"); end > 0 {
					marks[n] = rest[len(sqliteLegacyMark):end]
					i += end + 2
				}
			}
			n++
		}
	}
	return marks
}

// sqliteLegacyText is the text v0.28 wrote for a value (Go's %v): `true` /
// `false`, the shortest float, the decimal. Any other value is unchanged.
func sqliteLegacyText(v any) any {
	switch x := v.(type) {
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	}
	return v
}

// sqliteArgs binds the v0.28 text for each marked argument whose column is
// TEXT. Without a mark it returns `args` as they are.
func sqliteArgs(ex dbExecutor, pool *sql.DB, query string, args []any) []any {
	if pool == nil || !strings.Contains(query, sqliteLegacyMark) {
		return args
	}
	marks := sqliteMarks(query)
	if len(marks) == 0 {
		return args
	}
	cache := sqliteDeclCacheFor(pool)
	var out []any
	for i, target := range marks {
		if i >= len(args) {
			continue
		}
		table, col, ok := strings.Cut(target, "|")
		if !ok || !cache.textColumns(ex, table)[col] {
			continue
		}
		if out == nil {
			out = append([]any(nil), args...)
		}
		out[i] = sqliteLegacyText(args[i])
	}
	if out == nil {
		return args
	}
	return out
}

// sqliteIsSchemaStatement reports whether a statement changes the schema.
func sqliteIsSchemaStatement(query string) bool {
	q := strings.TrimLeft(query, " \t\r\n(")
	if len(q) > 6 {
		q = q[:6]
	}
	q = strings.ToUpper(q)
	return strings.HasPrefix(q, "CREATE") || strings.HasPrefix(q, "ALTER") || strings.HasPrefix(q, "DROP")
}

// sqliteArgExecutor is a dbExecutor on SQLite that binds its arguments with
// sqliteArgs and clears the declared-type cache after a schema statement.
type sqliteArgExecutor struct {
	inner dbExecutor
	pool  *sql.DB
}

func (e sqliteArgExecutor) Exec(query string, args ...any) (sql.Result, error) {
	res, err := e.inner.Exec(query, sqliteArgs(e.inner, e.pool, query, args)...)
	if sqliteIsSchemaStatement(query) {
		sqliteDeclCacheReset(e.pool)
	}
	return res, err
}

func (e sqliteArgExecutor) Query(query string, args ...any) (*sql.Rows, error) {
	return e.inner.Query(query, sqliteArgs(e.inner, e.pool, query, args)...)
}

func (e sqliteArgExecutor) QueryRow(query string, args ...any) *sql.Row {
	return e.inner.QueryRow(query, sqliteArgs(e.inner, e.pool, query, args)...)
}

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
// The declared types are cached per pool and table, keyed on the database's
// `PRAGMA schema_version`, which SQLite raises on every schema change. Each
// marked statement reads the version on the connection it runs on, and a
// changed version reloads the cache. So a column converted by another process
// (or another pool on the same file) is seen by the next statement, and so is
// DDL inside a transaction. A schema statement run through a Db handle
// (CREATE / ALTER / DROP, Store.migrate, `sky db migrate` in this process)
// also clears the pool's cache, and so does a rolled-back transaction: its
// DDL raised the version, the rollback puts it back, and a later change by
// another process can reach that same number with a different schema.
//
// A mark whose table or column is not an identifier is skipped before any
// PRAGMA (Std.Db.Store never writes one, and a hand-written query may).

import (
	"database/sql"
	"fmt"
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

// sqliteDeclCache holds, per table, whether each column has TEXT affinity, as
// read at schema version `version`.
type sqliteDeclCache struct {
	mu      sync.Mutex
	version int64
	tables  map[string]map[string]bool
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

// sqliteSchemaVersion reads `PRAGMA schema_version` on `ex`.
func sqliteSchemaVersion(ex dbExecutor) (int64, bool) {
	var v int64
	if err := ex.QueryRow("PRAGMA schema_version").Scan(&v); err != nil {
		return 0, false
	}
	return v, true
}

// textColumns reports which columns of `table` have TEXT affinity, read once
// per schema version (`version`, read by the caller on `ex` for the whole
// statement; `versionOk` false reads without caching). The PRAGMA runs on
// `ex`, so inside a transaction it uses the transaction's connection and sees
// its uncommitted DDL.
func (c *sqliteDeclCache) textColumns(ex dbExecutor, table string, version int64, versionOk bool) map[string]bool {
	if !versionOk {
		cols, _ := sqliteReadTextColumns(ex, table)
		return cols // not cached: the next statement asks again
	}
	c.mu.Lock()
	if c.version != version {
		c.version = version
		c.tables = map[string]map[string]bool{}
	}
	cols, ok := c.tables[table]
	c.mu.Unlock()
	if ok {
		return cols
	}
	cols, err := sqliteReadTextColumns(ex, table)
	if err != nil {
		return cols // not cached: the next statement asks again
	}
	c.mu.Lock()
	if c.version == version {
		c.tables[table] = cols
	}
	c.mu.Unlock()
	return cols
}

// sqliteReadTextColumns runs `PRAGMA table_info` for `table` on `ex` and
// reports which columns have TEXT affinity. The caller has checked that
// `table` is an identifier. A table that does not exist has no columns.
func sqliteReadTextColumns(ex dbExecutor, table string) (map[string]bool, error) {
	cols := map[string]bool{}
	rows, err := ex.Query(`PRAGMA table_info("` + table + `")`)
	if err != nil {
		return cols, err
	}
	defer rows.Close()
	names, err := rows.Columns()
	if err != nil {
		return cols, err
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
		return cols, fmt.Errorf("PRAGMA table_info has no name or type column")
	}
	for rows.Next() {
		vals := make([]any, len(names))
		ptrs := make([]any, len(names))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return map[string]bool{}, err
		}
		cols[sqliteCellText(vals[nameAt])] = sqliteTextAffinity(sqliteCellText(vals[typeAt]))
	}
	if err := rows.Err(); err != nil {
		return map[string]bool{}, err
	}
	return cols, nil
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
	// A list placeholder takes its values as one JSON parameter first
	// (db_list_param.go), so every placeholder below has one argument.
	args = sqliteListArgs(ex, pool, query, args)
	if pool == nil || !strings.Contains(query, sqliteLegacyMark) {
		return args
	}
	marks := sqliteMarks(query)
	if len(marks) == 0 {
		return args
	}
	// A mark whose table or column is not an identifier is skipped before
	// any PRAGMA: the table name is spliced into one.
	type target struct{ table, col string }
	valid := map[int]target{}
	for i, mark := range marks {
		table, col, ok := strings.Cut(mark, "|")
		if ok && i < len(args) && codecValidIdent(table) && codecValidIdent(col) {
			valid[i] = target{table, col}
		}
	}
	if len(valid) == 0 {
		return args
	}
	cache := sqliteDeclCacheFor(pool)
	version, versionOk := sqliteSchemaVersion(ex)
	var out []any
	for i, tc := range valid {
		if !cache.textColumns(ex, tc.table, version, versionOk)[tc.col] {
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

// A partial index on a v0.28 TEXT column.
//
// The predicate of a partial index (Store.uniqueWhere, Store.index with a
// condition, the `where` of a migration file's addIndex) is DDL with
// constants, so the marks above cannot reach it. On SQLite a v0.28 TEXT
// column compares by text: `flag = TRUE` is `flag = '1'` and misses every
// `'true'` row, and `score = 7.0` misses `'7'`. The index would then hold the
// wrong rows with no error, and a unique one would let duplicates in. So the
// index is refused before it runs, naming the column and the conversion.

// sqliteIndexPredicateLegacyErr reports an error when `where` compares a
// column of `table` that has TEXT affinity with a number or TRUE / FALSE.
// It reads `PRAGMA table_info` on `ex` (the transaction's connection inside
// one). An empty predicate, or a table that does not exist yet, passes.
func sqliteIndexPredicateLegacyErr(ex dbExecutor, table, index, where string) error {
	if strings.TrimSpace(where) == "" || !codecValidIdent(table) {
		return nil
	}
	cols, err := sqliteReadTextColumns(ex, table)
	if err != nil {
		return fmt.Errorf("index %s on %s: read the column types: %v", index, table, err)
	}
	text := map[string]bool{}
	for name, isText := range cols {
		if isText {
			text[strings.ToLower(name)] = true
		}
	}
	if len(text) == 0 {
		return nil
	}
	col, lit := sqlitePredicateTextVsLiteral(where, text)
	if col == "" {
		return nil
	}
	return sqliteLegacyIndexError{fmt.Sprintf("index %s: column %q of %s is TEXT (as v0.28 made the column of a nested Maybe Bool or Float), "+
		"and the index condition compares it with %s. SQLite compares that as text, so the index would hold the wrong rows. "+
		"Convert the column first (docs/migration/v0.29.md, Std.Db.Store, a nested Maybe scalar column made by v0.28), then run this again",
		index, col, table, lit)}
}

// sqliteLegacyIndexError is the refusal of sqliteIndexPredicateLegacyErr, an
// invalid input (any other error of the check is an I/O error).
type sqliteLegacyIndexError struct{ msg string }

func (e sqliteLegacyIndexError) Error() string { return e.msg }

// sqliteIndexCheckErr wraps an error of sqliteCheckIndexes for `verb`.
func sqliteIndexCheckErr(verb string, err error) any {
	if _, ok := err.(sqliteLegacyIndexError); ok {
		return Err[any, any](ErrInvalidInput(verb + ": " + err.Error()))
	}
	return Err[any, any](ErrIo(verb + ": " + err.Error()))
}

// sqliteCheckIndexes is sqliteIndexPredicateLegacyErr for each partial index
// of a Store that does not exist yet, on SQLite only. It runs before any
// statement of Store.create or Store.migrate, so a refusal changes nothing.
// An index that exists is left alone (its IF NOT EXISTS DDL does not run).
func sqliteCheckIndexes(d *SkyDb, table string, indexes []storeIndex) error {
	if d.driver == "pgx" {
		return nil
	}
	for _, ix := range indexes {
		if strings.TrimSpace(ix.Where) == "" {
			continue
		}
		if exists, err := codecIndexExists(d, table, ix.Name); err != nil {
			return err
		} else if exists {
			continue
		}
		if err := sqliteIndexPredicateLegacyErr(d.executor(), table, ix.Name, ix.Where); err != nil {
			return err
		}
	}
	return nil
}

type sqlitePredTok struct {
	kind string // "ident", "str", "num", "bool", "op", "kw", "punct"
	text string
}

// sqlitePredTokens splits an index predicate into tokens. A string literal
// is one token, so text inside it is never read as a column.
func sqlitePredTokens(s string) []sqlitePredTok {
	var out []sqlitePredTok
	isDigit := func(b byte) bool { return b >= '0' && b <= '9' }
	isIdent := func(b byte) bool {
		return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || isDigit(b)
	}
	startsNumber := func(i int) bool {
		if i >= len(s) {
			return false
		}
		if isDigit(s[i]) {
			return true
		}
		return s[i] == '.' && i+1 < len(s) && isDigit(s[i+1])
	}
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			i++
		case c == '\'':
			j := i + 1
			for j < len(s) {
				if s[j] == '\'' {
					if j+1 < len(s) && s[j+1] == '\'' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			out = append(out, sqlitePredTok{"str", s[i:min(j+1, len(s))]})
			i = j + 1
		case c == '"' || c == '`':
			j := strings.IndexByte(s[i+1:], c)
			if j < 0 {
				out = append(out, sqlitePredTok{"ident", s[i+1:]})
				i = len(s)
			} else {
				out = append(out, sqlitePredTok{"ident", s[i+1 : i+1+j]})
				i += j + 2
			}
		case startsNumber(i) || ((c == '-' || c == '+') && startsNumber(i+1)):
			j := i + 1
			for j < len(s) {
				if isDigit(s[j]) || s[j] == '.' {
					j++
				} else if (s[j] == 'e' || s[j] == 'E') && j+1 < len(s) &&
					(isDigit(s[j+1]) || ((s[j+1] == '-' || s[j+1] == '+') && j+2 < len(s) && isDigit(s[j+2]))) {
					j += 2
				} else {
					break
				}
			}
			out = append(out, sqlitePredTok{"num", s[i:j]})
			i = j
		case isIdent(c):
			j := i
			for j < len(s) && isIdent(s[j]) {
				j++
			}
			w := s[i:j]
			switch strings.ToUpper(w) {
			case "TRUE", "FALSE":
				out = append(out, sqlitePredTok{"bool", strings.ToUpper(w)})
			case "AND", "OR", "NOT", "IN", "IS", "NULL", "LIKE", "BETWEEN":
				out = append(out, sqlitePredTok{"kw", strings.ToUpper(w)})
			default:
				out = append(out, sqlitePredTok{"ident", w})
			}
			i = j
		case strings.ContainsRune("=<>!", rune(c)):
			j := i + 1
			for j < len(s) && strings.ContainsRune("=<>", rune(s[j])) {
				j++
			}
			out = append(out, sqlitePredTok{"op", s[i:j]})
			i = j
		default:
			out = append(out, sqlitePredTok{"punct", string(c)})
			i++
		}
	}
	return out
}

// sqlitePredicateTextVsLiteral finds the first comparison in `where` of a
// column in `text` (lower-case names) with a number or TRUE / FALSE:
// `col op lit`, `lit op col`, or `col [NOT] IN (…)` with such a literal in the
// list. It returns the column as written and the literal, or "", "".
func sqlitePredicateTextVsLiteral(where string, text map[string]bool) (string, string) {
	toks := sqlitePredTokens(where)
	isLit := func(k int) bool { return k < len(toks) && (toks[k].kind == "num" || toks[k].kind == "bool") }
	isTextCol := func(k int) bool {
		return k >= 0 && k < len(toks) && toks[k].kind == "ident" && text[strings.ToLower(toks[k].text)] &&
			!(k+1 < len(toks) && toks[k+1].text == ".") // `t.col`: only the last part names a column
	}
	at := func(k int) sqlitePredTok {
		if k < len(toks) {
			return toks[k]
		}
		return sqlitePredTok{}
	}
	for k := range toks {
		if !isTextCol(k) {
			if isLit(k) && at(k+1).kind == "op" && isTextCol(k+2) {
				return toks[k+2].text, toks[k].text
			}
			continue
		}
		next := k + 1
		if at(next).kind == "op" && isLit(next+1) {
			return toks[k].text, toks[next+1].text
		}
		if at(next).text == "NOT" {
			next++
		}
		if at(next).text == "IN" && at(next+1).text == "(" {
			for m := next + 2; m < len(toks) && toks[m].text != ")"; m++ {
				if isLit(m) {
					return toks[k].text, toks[m].text
				}
			}
		}
	}
	return "", ""
}

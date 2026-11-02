//go:build !js

package rt

// PostgreSQL: an Int compared with a foreign INTEGER or SMALLINT column.
//
// The app pool runs pgx's simple protocol (db_auth.go), which inlines every
// argument as an untyped literal, and the server reads that literal as the
// type of the column it is compared with. So `n = '3000000000'` on an INTEGER
// column fails with "value out of range for type integer" (SQLSTATE 22003),
// where SQL compares an integer with a wider one and the answer is simply "no
// match". A cast to BIGINT fixes that, but the cast cannot go into every Int
// condition: a TEXT column read by an Int field (a v0.28 nested-maybe column,
// a foreign table) has no `text = bigint` operator, and its conditions worked
// in v0.29.1 with the uncast literal (v0.29.2 sixth Judge).
//
// Std.Db.Store marks each Int placeholder compared with a plain column with
// that column: `?/*sky-int:table|col*/` for one value, and
// `?/*sky-list:N:int:table|col*/` for a long `inList` (condParam and
// listParamSql in Std/Db/Store.sky). After rebinding, the executor below
// reads the column's type from the catalogue, once per connection pool and
// table, and wraps the placeholder in `CAST(… AS BIGINT)` (`BIGINT[]` for a
// list) when the column is an INTEGER or SMALLINT, or a domain over one. Every
// other column keeps the uncast literal, so TEXT, NUMERIC and the Store's own
// BIGINT columns compare exactly as in v0.29.1. `int2 = int8` and
// `int4 = int8` are PostgreSQL's own cross-type operators of the integer
// B-tree family, so the cast keeps the column's index.
//
// The cache is cleared when a schema statement runs through a Db handle, when
// a transaction rolls back (its DDL may be what was read), after a migration,
// and when a marked statement fails with "operator does not exist" (42883) or
// "out of range" (22003), the two errors a stale entry can cause after
// another process changed the column's type. A table the catalogue does not
// know, or a lookup that fails, leaves the statement as written.

import (
	"database/sql"
	"errors"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/pgconn"
)

// pgIntMarkOpen opens the mark of one Int placeholder: `/*sky-int:table|col*/`.
const pgIntMarkOpen = "/*sky-int:"

// pgIntCache holds, per table, which columns take a BIGINT cast.
type pgIntCache struct {
	mu     sync.Mutex
	tables map[string]map[string]bool
}

var pgIntCaches sync.Map // *sql.DB → *pgIntCache

func pgIntCacheFor(pool *sql.DB) *pgIntCache {
	if c, ok := pgIntCaches.Load(pool); ok {
		return c.(*pgIntCache)
	}
	c, _ := pgIntCaches.LoadOrStore(pool, &pgIntCache{tables: map[string]map[string]bool{}})
	return c.(*pgIntCache)
}

// pgIntCacheReset forgets the column types read for `pool`.
func pgIntCacheReset(pool *sql.DB) {
	if pool != nil {
		pgIntCaches.Delete(pool)
	}
}

// dbColumnCacheReset forgets every column type read for `pool`, on either
// engine, after a schema change or a rolled-back transaction.
func dbColumnCacheReset(pool *sql.DB) {
	sqliteDeclCacheReset(pool)
	pgIntCacheReset(pool)
}

// narrowIntColumns reports which columns of `table` are INTEGER or SMALLINT
// (or a domain over one), read through `ex`. The caller has checked that
// `table` is an identifier. It is spelled as Std.Db.Store writes it in SQL
// (unquoted), so `to_regclass` resolves it on the search path as the
// statement does. A table that does not exist has no columns.
func (c *pgIntCache) narrowIntColumns(ex dbExecutor, table string) map[string]bool {
	c.mu.Lock()
	cols, ok := c.tables[table]
	c.mu.Unlock()
	if ok {
		return cols
	}
	cols, err := pgReadNarrowIntColumns(ex, table)
	if err != nil {
		return cols // not cached: the next statement asks again
	}
	c.mu.Lock()
	c.tables[table] = cols
	c.mu.Unlock()
	return cols
}

func pgReadNarrowIntColumns(ex dbExecutor, table string) (map[string]bool, error) {
	cols := map[string]bool{}
	rows, err := ex.Query(
		`SELECT a.attname::text, `+
			`format_type(COALESCE(NULLIF(t.typbasetype, 0), a.atttypid), NULL) `+
			`FROM pg_attribute a JOIN pg_type t ON t.oid = a.atttypid `+
			`WHERE a.attrelid = to_regclass($1) AND a.attnum > 0 AND NOT a.attisdropped`,
		table)
	if err != nil {
		return cols, err
	}
	defer rows.Close()
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			return map[string]bool{}, err
		}
		cols[name] = typ == "integer" || typ == "smallint"
	}
	if err := rows.Err(); err != nil {
		return map[string]bool{}, err
	}
	return cols, nil
}

// pgIntSite is one marked Int placeholder of a rebound statement: the byte
// range of `$k/*…*/`, the column it names, and whether it is a list.
type pgIntSite struct {
	start, end int
	table, col string
	list       bool
}

// pgIntSites finds every `$k/*sky-int:table|col*/` and
// `$k/*sky-list:N:int:table|col*/` of a rebound statement.
func pgIntSites(query string) []pgIntSite {
	var sites []pgIntSite
	off := 0
	for {
		at := strings.Index(query[off:], "/*sky-")
		if at < 0 {
			return sites
		}
		markAt := off + at
		off = markAt + len("/*sky-")
		closeAt := strings.Index(query[markAt:], "*/")
		if closeAt < 0 {
			return sites
		}
		end := markAt + closeAt + 2
		start := markAt
		for start > 0 && query[start-1] >= '0' && query[start-1] <= '9' {
			start--
		}
		if start == markAt || start == 0 || query[start-1] != '$' {
			continue
		}
		start--
		body := query[markAt:end]
		var target string
		list := false
		switch {
		case strings.HasPrefix(body, pgIntMarkOpen):
			target = body[len(pgIntMarkOpen) : len(body)-2]
		case strings.HasPrefix(body, listMarkOpen):
			m, ok := parseListMark(body[len(listMarkOpen) : len(body)-2])
			if !ok || !m.int || m.table == "" {
				continue
			}
			target, list = m.table+"|"+m.col, true
		default:
			continue
		}
		table, col, ok := strings.Cut(target, "|")
		if !ok || !codecValidIdent(table) || !codecValidIdent(col) {
			continue
		}
		sites = append(sites, pgIntSite{start: start, end: end, table: table, col: col, list: list})
	}
}

// pgIntCastQuery wraps each marked Int placeholder whose column is an INTEGER
// or SMALLINT in a BIGINT cast. It reports whether the statement had a mark,
// so a failure can clear the cache. Without a mark it returns `query` as is.
func pgIntCastQuery(ex dbExecutor, pool *sql.DB, query string) (string, bool) {
	if !strings.Contains(query, pgIntMarkOpen) && !strings.Contains(query, ":int:") {
		return query, false
	}
	sites := pgIntSites(query)
	if len(sites) == 0 {
		return query, false
	}
	var cache *pgIntCache
	if pool != nil {
		cache = pgIntCacheFor(pool)
	}
	narrow := func(table, col string) bool {
		if cache == nil {
			cols, _ := pgReadNarrowIntColumns(ex, table)
			return cols[col]
		}
		return cache.narrowIntColumns(ex, table)[col]
	}
	var b strings.Builder
	last := 0
	for _, s := range sites {
		if !narrow(s.table, s.col) {
			continue
		}
		b.WriteString(query[last:s.start])
		b.WriteString("CAST(")
		b.WriteString(query[s.start:s.end])
		if s.list {
			b.WriteString(" AS BIGINT[])")
		} else {
			b.WriteString(" AS BIGINT)")
		}
		last = s.end
	}
	if last == 0 {
		return query, true
	}
	b.WriteString(query[last:])
	return b.String(), true
}

// pgIntStaleErr reports whether `err` is one a stale column type can cause on
// a marked statement: "operator does not exist" (42883), "out of range"
// (22003).
func pgIntStaleErr(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "42883" || pgErr.Code == "22003"
	}
	return false
}

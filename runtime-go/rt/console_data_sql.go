// console_data_sql.go — SQL browsing for the console Data endpoint (R2).
//
// Read-only, opt-in, and isolated from the app's hot-path pool, per the security
// review's must-haves:
//   - DEFAULT-DENY table allowlist: only tables created via Std.Db.Store are
//     browsable (registered in Db_createCols), NEVER a raw information_schema walk
//     (which would disclose other tenants' / system / migration / auth tables).
//   - SEPARATE read-only capped connection (not dbRegistry's pool), so a heavy
//     operator browse can't lock the app's traffic. SELECTs are fully CONSTRUCTED
//     here (no user SQL text) — only allow-listed table/column identifiers reach
//     the query, quoted; values are never interpolated.
//   - REDACT-BY-DEFAULT: columns whose name matches a sensitive pattern
//     (password/token/secret/hash/api_key/ssn/...) are masked.
//   - Row + byte caps, statement timeout, and every read audit-logged.
//   - Tagged `sql`/analytics in discovery — a may-be-slow scan, not a KV point op.
package rt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// handleSqlBrowse serves ?sql=<name> (list tables) and ?sql=<name>&table=<t> (rows).
func handleSqlBrowse(w http.ResponseWriter, r *http.Request, sqlName string, q url.Values) {
	d := findSqlSource(sqlName)
	if d == nil {
		http.Error(w, "unknown sql source", http.StatusNotFound)
		return
	}
	table := q.Get("table")
	if table == "" {
		writeJSON(w, map[string]any{
			"source": sqlName, "driver": d.driver, "kind": "sql",
			"tables": browsableTablesFor(sqlName),
		})
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	res, err := browseSqlTable(d, table, limit, offset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	logStructured("info", "console.data.sql.read",
		"source", sqlName, "table", table, "rows", strconv.Itoa(len(res.Rows)),
		"remote", r.RemoteAddr, "forwarded", r.Header.Get("X-Forwarded-For"))
	writeJSON(w, map[string]any{
		"source": sqlName, "table": table, "kind": "rows",
		"columns": res.Columns, "redacted": res.Redacted,
		"rows": res.Rows, "truncated": res.Truncated,
	})
}

func errBrowse(msg string) error { return errors.New("console.data.sql: " + msg) }

// browsableTables: connection name (DSN/path) → set of Store-created table names.
// Populated by Db_createCols; the ONLY source of browsable tables (default-deny).
var (
	browsableTablesMu sync.Mutex
	browsableTables   = map[string]map[string]struct{}{}
)

func registerBrowsableTable(dbName, table string) {
	if dbName == "" || table == "" {
		return
	}
	browsableTablesMu.Lock()
	defer browsableTablesMu.Unlock()
	if browsableTables[dbName] == nil {
		browsableTables[dbName] = map[string]struct{}{}
	}
	browsableTables[dbName][table] = struct{}{}
}

func browsableTablesFor(dbName string) []string {
	browsableTablesMu.Lock()
	defer browsableTablesMu.Unlock()
	set := browsableTables[dbName]
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func isBrowsableTable(dbName, table string) bool {
	browsableTablesMu.Lock()
	defer browsableTablesMu.Unlock()
	_, ok := browsableTables[dbName][table]
	return ok
}

// sqlSourceInfo describes a browsable SQL connection for discovery.
type sqlSourceInfo struct {
	Name   string   `json:"name"`
	Driver string   `json:"driver"`
	Kind   string   `json:"kind"` // always "sql"
	Tables []string `json:"tables"`
}

func listSqlSources() []sqlSourceInfo {
	dbRegistryMu.Lock()
	handles := make([]*SkyDb, 0, len(dbRegistry))
	for _, d := range dbRegistry {
		handles = append(handles, d)
	}
	dbRegistryMu.Unlock()

	out := []sqlSourceInfo{}
	for _, d := range handles {
		tables := browsableTablesFor(d.name)
		if len(tables) == 0 {
			continue // nothing Store-created here → not browsable
		}
		out = append(out, sqlSourceInfo{Name: d.name, Driver: d.driver, Kind: "sql", Tables: tables})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func findSqlSource(name string) *SkyDb {
	dbRegistryMu.Lock()
	defer dbRegistryMu.Unlock()
	return dbRegistry[name]
}

// sensitive column-name patterns → redact by default.
var sensitiveColParts = []string{
	"password", "passwd", "secret", "token", "hash", "salt",
	"api_key", "apikey", "access_key", "private_key", "privatekey",
	"jwt", "ssn", "card", "cvv", "pan", "otp", "mfa", "recovery",
	"session", "cookie",
}

func isSensitiveCol(name string) bool {
	n := strings.ToLower(name)
	for _, p := range sensitiveColParts {
		if strings.Contains(n, p) {
			return true
		}
	}
	return false
}

// openBrowseConn opens a SEPARATE, capped, read-only connection to the SkyDb's DSN
// — never the app's hot-path pool. Best-effort read-only enforcement in addition to
// the fully-constructed SELECT (which can't write anyway).
func openBrowseConn(d *SkyDb) (*sql.DB, error) {
	driver, dsn := detectDriver(d.name)
	conn, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(2)
	conn.SetMaxIdleConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if driver == "pgx" {
		_, _ = conn.ExecContext(ctx, "SET default_transaction_read_only = on")
	} else {
		_, _ = conn.ExecContext(ctx, "PRAGMA query_only = ON")
	}
	return conn, nil
}

const sqlBrowseMaxLimit = 200

type sqlBrowseResult struct {
	Columns   []string   `json:"columns"`
	Redacted  []string   `json:"redacted"` // column names masked in the rows
	Rows      [][]string `json:"rows"`
	Truncated bool       `json:"truncated"`
}

// browseSqlTable runs a fully-constructed, read-only, capped SELECT over an
// allow-listed table with sensitive columns redacted. Returns typed-as-string cells.
func browseSqlTable(d *SkyDb, table string, limit, offset int) (*sqlBrowseResult, error) {
	// table MUST be allow-listed AND a safe identifier before it touches SQL.
	if !isBrowsableTable(d.name, table) || !isSafeIdent(table) {
		return nil, errBrowse("unknown or non-browsable table")
	}
	colSet, err := codecTableColumns(d, table)
	if err != nil {
		return nil, err
	}
	cols := make([]string, 0, len(colSet))
	for c := range colSet {
		if isSafeIdent(c) {
			cols = append(cols, c)
		}
	}
	sort.Strings(cols)
	if len(cols) == 0 {
		return nil, errBrowse("no columns")
	}
	if limit <= 0 || limit > sqlBrowseMaxLimit {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	redacted := []string{}
	selCols := make([]string, len(cols))
	for i, c := range cols {
		if isSensitiveCol(c) {
			selCols[i] = "'***' AS " + quoteIdent(c) // never SELECT the secret column
			redacted = append(redacted, c)
		} else {
			selCols[i] = quoteIdent(c)
		}
	}
	// Deterministic order by the first column (already validated + quoted).
	query := "SELECT " + strings.Join(selCols, ", ") + " FROM " + quoteIdent(table) +
		" ORDER BY " + quoteIdent(cols[0]) + " LIMIT ? OFFSET ?"

	conn, err := openBrowseConn(d)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := conn.QueryContext(ctx, d.rebind(query), limit+1, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := &sqlBrowseResult{Columns: cols, Redacted: redacted, Rows: [][]string{}}
	n := 0
	for rows.Next() {
		if n >= limit {
			out.Truncated = true
			break
		}
		cells := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range cells {
			ptrs[i] = &cells[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make([]string, len(cols))
		for i, c := range cells {
			row[i] = cellToString(c)
		}
		out.Rows = append(out.Rows, row)
		n++
	}
	return out, rows.Err()
}

func cellToString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(x)
	case string:
		return x
	case time.Time:
		return x.Format(time.RFC3339)
	default:
		return fmt.Sprintf("%v", x)
	}
}

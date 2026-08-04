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
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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

// The public source identifier is an OPAQUE HANDLE (sha256 of the DSN), NEVER the
// raw connection string — a Postgres DSN carries user:PASSWORD@host and must never
// reach the discovery JSON, the audit log, or a client-echoed error.
func sqlSourceHandle(dsn string) string {
	sum := sha256.Sum256([]byte(dsn))
	return "src-" + hex.EncodeToString(sum[:])[:12]
}

// sqlSourceLabel is a human-readable, credential-free display name for a source.
func sqlSourceLabel(dsn, driver string) string {
	if driver == "pgx" {
		if u, err := url.Parse(dsn); err == nil && u.Host != "" {
			return "postgres://" + u.Host + u.Path // userinfo (user:pass) stripped
		}
		return "postgres"
	}
	// sqlite: a file path — show the base name only (dirs may reveal layout).
	if i := strings.LastIndexAny(dsn, "/\\"); i >= 0 {
		return dsn[i+1:]
	}
	return dsn
}

// sqlBrowseSem bounds concurrent SQL browses (each opens a small read-only pool);
// caps backend connection pressure so a burst can't exhaust Postgres.
var sqlBrowseSem = make(chan struct{}, 4)

// handleSqlBrowse serves ?sql=<handle> (list tables) and ?sql=<handle>&table=<t> (rows).
func handleSqlBrowse(w http.ResponseWriter, r *http.Request, handle string, q url.Values) {
	d := findSqlSource(handle)
	if d == nil {
		logStructured("warn", "console.data.sql.denied", "reason", "unknown-source",
			"handle", handle, "remote", r.RemoteAddr)
		http.Error(w, "unknown sql source", http.StatusNotFound)
		return
	}
	table := q.Get("table")
	if table == "" {
		logStructured("info", "console.data.sql.list", "handle", handle,
			"remote", r.RemoteAddr, "forwarded", r.Header.Get("X-Forwarded-For"))
		writeJSON(w, map[string]any{
			"source": handle, "label": sqlSourceLabel(d.name, d.driver),
			"driver": d.driver, "kind": "sql", "tables": browsableTablesFor(d.name),
		})
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))

	sqlBrowseSem <- struct{}{}
	res, err := browseSqlTable(d, table, limit, offset)
	<-sqlBrowseSem

	if err != nil {
		// Audit the rejected probe (allowlist miss / bad ident) — never echo internals.
		logStructured("warn", "console.data.sql.denied", "handle", handle,
			"table", table, "reason", err.Error(), "remote", r.RemoteAddr)
		http.Error(w, "table not browsable", http.StatusBadRequest)
		return
	}
	logStructured("info", "console.data.sql.read",
		"handle", handle, "table", table, "rows", strconv.Itoa(len(res.Rows)),
		"remote", r.RemoteAddr, "forwarded", r.Header.Get("X-Forwarded-For"))
	writeJSON(w, map[string]any{
		"source": handle, "table": table, "kind": "rows",
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

// sqlSourceInfo describes a browsable SQL connection for discovery. `Name` is the
// OPAQUE HANDLE (never the DSN); `Label` is a credential-free display string.
type sqlSourceInfo struct {
	Name   string   `json:"name"` // opaque handle
	Label  string   `json:"label"`
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
		out = append(out, sqlSourceInfo{
			Name:   sqlSourceHandle(d.name),
			Label:  sqlSourceLabel(d.name, d.driver),
			Driver: d.driver, Kind: "sql", Tables: tables,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// findSqlSource resolves an opaque handle back to its SkyDb (no DSN ever crosses
// the wire). Iterating the registry is fine — a process has a handful of DBs.
func findSqlSource(handle string) *SkyDb {
	dbRegistryMu.Lock()
	defer dbRegistryMu.Unlock()
	for _, d := range dbRegistry {
		if sqlSourceHandle(d.name) == handle && len(browsableTablesFor(d.name)) > 0 {
			return d
		}
	}
	return nil
}

// Redaction is token-aware, not a bare substring denylist: split the column on
// non-alphanumerics and redact if ANY token is a known secret token, OR the whole
// name contains a strong secret substring. Catches user_pw / signing_key / pwd /
// passphrase / pin without over-redacting monkey_id / keyboard. Over-redaction is
// the safe default here; unlisted secret columns are the only residual risk.
var sensitiveTokens = map[string]bool{
	"password": true, "passwd": true, "passphrase": true, "pass": true,
	"pw": true, "pwd": true, "pin": true, "secret": true, "secrets": true,
	"token": true, "tokens": true, "hash": true, "salt": true,
	"key": true, "keys": true, "apikey": true, "jwt": true, "bearer": true,
	"credential": true, "credentials": true, "cred": true, "creds": true,
	"ssn": true, "cvv": true, "cvc": true, "pan": true, "otp": true,
	"mfa": true, "totp": true, "recovery": true, "session": true,
	"cookie": true, "private": true, "privatekey": true,
}

var sensitiveSubstrings = []string{
	"password", "passwd", "passphrase", "secret", "apikey", "api_key",
	"access_key", "private_key", "signing_key", "encryption_key", "auth_key",
	"session_token", "refresh_token", "card_number", "creditcard",
}

func isSensitiveCol(name string) bool {
	n := strings.ToLower(name)
	for _, s := range sensitiveSubstrings {
		if strings.Contains(n, s) {
			return true
		}
	}
	for _, tok := range strings.FieldsFunc(n, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	}) {
		if sensitiveTokens[tok] {
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
	// MaxOpenConns(1): the session-scoped read-only PRAGMA/SET below must apply to
	// the SAME connection the query later runs on. With a >1 pool the query could
	// get a connection that never received it.
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)
	conn.SetConnMaxLifetime(30 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if driver == "pgx" {
		if _, err := conn.ExecContext(ctx, "SET default_transaction_read_only = on"); err != nil {
			conn.Close()
			return nil, err
		}
	} else {
		if _, err := conn.ExecContext(ctx, "PRAGMA query_only = ON"); err != nil {
			conn.Close()
			return nil, err
		}
	}
	return conn, nil
}

const sqlBrowseMaxLimit = 200
const sqlBrowseMaxOffset = 100000 // cap skip-scan cost (defense-in-depth w/ timeout)

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
	if offset > sqlBrowseMaxOffset {
		offset = sqlBrowseMaxOffset
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
	// Read-only transaction: a second, driver-level guarantee that this browse
	// cannot mutate, on top of the constructed column-only SELECT + query_only conn.
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, d.rebind(query), limit+1, offset)
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

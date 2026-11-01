//go:build !js

// Package rt — the read-only schema and ledger inspection behind
// `sky db check` (Std.Db.Migrate.inspect).
//
// Db_inspect reads the live database's catalogue (tables, columns, types,
// NULL, defaults, primary keys, indexes with their predicate) and judges each
// committed migration file against the `_sky_migrations` ledger by the ONE
// ledger rule (migLedgerCheck), and it writes nothing: it never creates the
// ledger, never re-stamps a v0.28 row, never runs a statement that changes
// the database. The report is raw catalogue facts as JSON; the CLI turns them
// into the schema model `sky db migrate --gen` diffs, so `sky db check` and
// `--gen` compare through the same code (rust/crates/sky/src/db_check.rs).
package rt

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// inspectColumn is one live column, as the catalogue states it.
type inspectColumn struct {
	Name    string  `json:"name"`
	Type    string  `json:"type"`
	NotNull bool    `json:"notnull"`
	Default *string `json:"default"`
	Pk      bool    `json:"pk"`
	Autoinc bool    `json:"autoinc"`
	// Unique is a single-column UNIQUE constraint (a column-level UNIQUE,
	// which the database backs with an index of its own).
	Unique bool `json:"unique"`
}

// inspectIndex is one index a CREATE INDEX made. An index that backs a
// primary key or a UNIQUE constraint is not listed: it is the constraint.
type inspectIndex struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Unique  bool     `json:"unique"`
	Where   string   `json:"where"`
}

type inspectTable struct {
	Name    string          `json:"name"`
	Columns []inspectColumn `json:"columns"`
	Indexes []inspectIndex  `json:"indexes"`
}

// inspectMigration is one committed migration file against the ledger:
// `applied`, `pending`, `edited` (its checksum changed after it was
// applied) or `restamp` (applied by v0.28; the next migrate re-stamps it).
type inspectMigration struct {
	Name      string `json:"name"`
	State     string `json:"state"`
	AppliedAt string `json:"appliedAt,omitempty"`
}

type inspectLedger struct {
	Present    bool               `json:"present"`
	Migrations []inspectMigration `json:"migrations"`
	// Other lists ledger rows with no committed file: a Db.migrate list in
	// the app, or a stdlib module that keeps its tables in the same ledger.
	Other []string `json:"other"`
}

type inspectReport struct {
	Dialect string         `json:"dialect"`
	Tables  []inspectTable `json:"tables"`
	Ledger  inspectLedger  `json:"ledger"`
}

// Db_inspect : Db -> String(json) -> Task Error String.
// The migrations JSON is the `[{id, ops}]` array `sky db migrate` applies
// ("[]" when the project has no db/migrations).
func Db_inspect(connArg, jsonArg any) any {
	return func() any {
		d, ok := connArg.(*SkyDb)
		if !ok {
			return Err[any, any](ErrInvalidInput("inspect: first argument is not a Db"))
		}
		raw := strings.TrimSpace(AsString(jsonArg))
		if raw == "" {
			raw = "[]"
		}
		entries, err := migOpEntries(d.driver, raw)
		if err != nil {
			return Err[any, any](ErrInvalidInput("inspect: " + err.Error()))
		}
		rep, err := dbInspect(d, entries)
		if err != nil {
			return Err[any, any](ErrIo("inspect: " + err.Error()))
		}
		b, err := json.Marshal(rep)
		if err != nil {
			return Err[any, any](ErrIo("inspect: " + err.Error()))
		}
		return Ok[any, any](string(b))
	}
}

func dbInspect(d *SkyDb, entries []migEntry) (inspectReport, error) {
	rep := inspectReport{Dialect: "sqlite", Tables: []inspectTable{}}
	var err error
	if d.driver == "pgx" {
		rep.Dialect = "postgres"
		rep.Tables, err = pgInspectTables(d.conn)
	} else {
		rep.Tables, err = sqliteInspectTables(d.conn)
	}
	if err != nil {
		return rep, err
	}
	rep.Ledger, err = inspectLedgerOf(d, entries)
	return rep, err
}

// inspectLedgerOf judges each entry by migLedgerCheck, reading the ledger
// only. A database with no ledger table has every file pending.
func inspectLedgerOf(d *SkyDb, entries []migEntry) (inspectLedger, error) {
	led := inspectLedger{Migrations: []inspectMigration{}, Other: []string{}}
	var probe string
	if d.driver == "pgx" {
		probe = `SELECT to_regclass('_sky_migrations') IS NOT NULL`
	} else {
		probe = `SELECT COUNT(*) > 0 FROM sqlite_master WHERE type = 'table' AND name = '_sky_migrations'`
	}
	if err := d.conn.QueryRow(probe).Scan(&led.Present); err != nil {
		return led, fmt.Errorf("probe _sky_migrations: %v", err)
	}
	applied := map[string]appliedMigration{}
	if led.Present {
		rows, err := d.conn.Query(`SELECT name, checksum, applied_at, kind FROM _sky_migrations`)
		withKind := err == nil
		if !withKind {
			// A ledger that v0.28 or earlier wrote has no `kind` column.
			rows, err = d.conn.Query(`SELECT name, checksum, applied_at FROM _sky_migrations`)
		}
		if err != nil {
			return led, fmt.Errorf("read _sky_migrations: %v", err)
		}
		for rows.Next() {
			var n, c, at string
			var kind sql.NullString
			var scanErr error
			if withKind {
				scanErr = rows.Scan(&n, &c, &at, &kind)
			} else {
				scanErr = rows.Scan(&n, &c, &at)
			}
			if scanErr != nil {
				rows.Close()
				return led, fmt.Errorf("scan _sky_migrations: %v", scanErr)
			}
			applied[n] = appliedMigration{checksum: c, appliedAt: at, kind: kind.String}
		}
		rows.Close()
	}
	files := map[string]bool{}
	for _, e := range entries {
		files[e.name] = true
		prev, seen := applied[e.name]
		m := inspectMigration{Name: e.name}
		switch migLedgerCheck(e, prev, seen) {
		case migVerified:
			m.State, m.AppliedAt = "applied", prev.appliedAt
		case migRestamp:
			m.State, m.AppliedAt = "restamp", prev.appliedAt
		case migDrifted:
			m.State, m.AppliedAt = "edited", prev.appliedAt
		default:
			m.State = "pending"
		}
		led.Migrations = append(led.Migrations, m)
	}
	for n := range applied {
		if !files[n] {
			led.Other = append(led.Other, n)
		}
	}
	sort.Strings(led.Other)
	return led, nil
}

// sqliteQuoteIdent quotes a name for a PRAGMA argument.
func sqliteQuoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func sqliteInspectTables(conn *sql.DB) ([]inspectTable, error) {
	type tbl struct{ name, sql string }
	rows, err := conn.Query(
		`SELECT name, COALESCE(sql, '') FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list tables: %v", err)
	}
	var tbls []tbl
	for rows.Next() {
		var t tbl
		if err := rows.Scan(&t.name, &t.sql); err != nil {
			rows.Close()
			return nil, err
		}
		tbls = append(tbls, t)
	}
	rows.Close()
	out := []inspectTable{}
	for _, t := range tbls {
		it := inspectTable{Name: t.name, Columns: []inspectColumn{}, Indexes: []inspectIndex{}}
		autoinc := strings.Contains(strings.ToUpper(t.sql), "AUTOINCREMENT")
		crows, err := conn.Query(`PRAGMA table_info(` + sqliteQuoteIdent(t.name) + `)`)
		if err != nil {
			return nil, fmt.Errorf("columns of %s: %v", t.name, err)
		}
		for crows.Next() {
			var cid, notnull, pk int
			var name, typ string
			var dflt sql.NullString
			if err := crows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
				crows.Close()
				return nil, err
			}
			c := inspectColumn{Name: name, Type: typ, NotNull: notnull != 0 || pk != 0, Pk: pk != 0}
			if dflt.Valid {
				v := dflt.String
				c.Default = &v
			}
			c.Autoinc = c.Pk && autoinc && strings.EqualFold(strings.TrimSpace(typ), "INTEGER")
			it.Columns = append(it.Columns, c)
		}
		crows.Close()
		type ixRow struct {
			name, origin string
			unique       bool
		}
		irows, err := conn.Query(`PRAGMA index_list(` + sqliteQuoteIdent(t.name) + `)`)
		if err != nil {
			return nil, fmt.Errorf("indexes of %s: %v", t.name, err)
		}
		var ixs []ixRow
		for irows.Next() {
			cols, err := irows.Columns()
			if err != nil {
				irows.Close()
				return nil, err
			}
			// seq, name, unique, origin, partial (older SQLite: the first three).
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := irows.Scan(ptrs...); err != nil {
				irows.Close()
				return nil, err
			}
			r := ixRow{name: inspectText(vals[1]), unique: inspectText(vals[2]) == "1", origin: "c"}
			if len(vals) > 3 {
				r.origin = inspectText(vals[3])
			}
			ixs = append(ixs, r)
		}
		irows.Close()
		sort.Slice(ixs, func(i, j int) bool { return ixs[i].name < ixs[j].name })
		for _, ix := range ixs {
			cols, err := sqliteIndexColumns(conn, ix.name)
			if err != nil {
				return nil, err
			}
			switch ix.origin {
			case "pk":
				continue
			case "u":
				if len(cols) == 1 {
					for i := range it.Columns {
						if it.Columns[i].Name == cols[0] {
							it.Columns[i].Unique = true
						}
					}
				}
				continue
			}
			var ddl sql.NullString
			if err := conn.QueryRow(
				`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?`, ix.name).Scan(&ddl); err != nil && err != sql.ErrNoRows {
				return nil, err
			}
			it.Indexes = append(it.Indexes, inspectIndex{
				Name: ix.name, Columns: cols, Unique: ix.unique, Where: indexPredicateOf(ddl.String),
			})
		}
		out = append(out, it)
	}
	return out, nil
}

func inspectText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(x)
	default:
		return fmt.Sprintf("%v", x)
	}
}

func sqliteIndexColumns(conn *sql.DB, index string) ([]string, error) {
	rows, err := conn.Query(`PRAGMA index_info(` + sqliteQuoteIdent(index) + `)`)
	if err != nil {
		return nil, fmt.Errorf("columns of index %s: %v", index, err)
	}
	defer rows.Close()
	type part struct {
		seq  int
		name string
	}
	var parts []part
	for rows.Next() {
		var seq, cid int
		var name sql.NullString
		if err := rows.Scan(&seq, &cid, &name); err != nil {
			return nil, err
		}
		parts = append(parts, part{seq, name.String})
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].seq < parts[j].seq })
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, p.name)
	}
	return out, nil
}

// indexPredicateOf returns the WHERE predicate of a CREATE INDEX statement:
// the text after the column list's closing parenthesis, or "".
func indexPredicateOf(ddl string) string {
	up := strings.ToUpper(ddl)
	on := strings.Index(up, " ON ")
	if on < 0 {
		return ""
	}
	open := strings.Index(ddl[on:], "(")
	if open < 0 {
		return ""
	}
	depth := 0
	inStr := false
	for i := on + open; i < len(ddl); i++ {
		ch := ddl[i]
		switch {
		case inStr:
			if ch == '\'' {
				inStr = false
			}
		case ch == '\'':
			inStr = true
		case ch == '(':
			depth++
		case ch == ')':
			depth--
			if depth == 0 {
				rest := strings.TrimSpace(ddl[i+1:])
				if len(rest) >= 5 && strings.EqualFold(rest[:5], "WHERE") {
					return strings.TrimSpace(rest[5:])
				}
				return ""
			}
		}
	}
	return ""
}

func pgInspectTables(conn *sql.DB) ([]inspectTable, error) {
	rows, err := conn.Query(
		`SELECT table_name FROM information_schema.tables
		 WHERE table_schema = current_schema() AND table_type = 'BASE TABLE' ORDER BY table_name`)
	if err != nil {
		return nil, fmt.Errorf("list tables: %v", err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		names = append(names, n)
	}
	rows.Close()
	out := []inspectTable{}
	const rel = `to_regclass(quote_ident(current_schema()) || '.' || quote_ident($1))`
	for _, name := range names {
		it := inspectTable{Name: name, Columns: []inspectColumn{}, Indexes: []inspectIndex{}}
		pks := map[string]bool{}
		prows, err := conn.Query(
			`SELECT a.attname FROM pg_index i
			 JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
			 WHERE i.indrelid = `+rel+` AND i.indisprimary`, name)
		if err != nil {
			return nil, fmt.Errorf("primary key of %s: %v", name, err)
		}
		for prows.Next() {
			var a string
			if err := prows.Scan(&a); err != nil {
				prows.Close()
				return nil, err
			}
			pks[a] = true
		}
		prows.Close()
		crows, err := conn.Query(
			`SELECT column_name, data_type, is_nullable, column_default, COALESCE(is_identity, 'NO')
			 FROM information_schema.columns
			 WHERE table_schema = current_schema() AND table_name = $1 ORDER BY ordinal_position`, name)
		if err != nil {
			return nil, fmt.Errorf("columns of %s: %v", name, err)
		}
		for crows.Next() {
			var cname, typ, nullable, identity string
			var dflt sql.NullString
			if err := crows.Scan(&cname, &typ, &nullable, &dflt, &identity); err != nil {
				crows.Close()
				return nil, err
			}
			c := inspectColumn{Name: cname, Type: typ, NotNull: nullable == "NO", Pk: pks[cname]}
			if dflt.Valid {
				v := dflt.String
				c.Default = &v
			}
			c.Autoinc = identity == "YES" || (dflt.Valid && strings.HasPrefix(dflt.String, "nextval("))
			it.Columns = append(it.Columns, c)
		}
		crows.Close()
		irows, err := conn.Query(
			`SELECT c.relname, i.indisunique, i.indisprimary,
			        COALESCE(pg_get_expr(i.indpred, i.indrelid), ''),
			        COALESCE(array_to_string(ARRAY(
			            SELECT a.attname FROM unnest(i.indkey::int2[]) WITH ORDINALITY AS k(attnum, ord)
			            JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
			            ORDER BY k.ord), ','), ''),
			        EXISTS (SELECT 1 FROM pg_constraint con WHERE con.conindid = i.indexrelid)
			 FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
			 WHERE i.indrelid = `+rel+` ORDER BY c.relname`, name)
		if err != nil {
			return nil, fmt.Errorf("indexes of %s: %v", name, err)
		}
		for irows.Next() {
			var iname, pred, cols string
			var uniq, primary, constraint bool
			if err := irows.Scan(&iname, &uniq, &primary, &pred, &cols, &constraint); err != nil {
				irows.Close()
				return nil, err
			}
			colList := []string{}
			if cols != "" {
				colList = strings.Split(cols, ",")
			}
			if primary {
				continue
			}
			if constraint {
				if uniq && len(colList) == 1 {
					for i := range it.Columns {
						if it.Columns[i].Name == colList[0] {
							it.Columns[i].Unique = true
						}
					}
				}
				continue
			}
			it.Indexes = append(it.Indexes, inspectIndex{Name: iname, Columns: colList, Unique: uniq, Where: pred})
		}
		irows.Close()
		out = append(out, it)
	}
	return out, nil
}

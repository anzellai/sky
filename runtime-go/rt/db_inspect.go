//go:build !js

// Package rt — the read-only schema and ledger inspection behind
// `sky db check` (Std.Db.Migrate.inspectSchema).
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
	// PkPos is the column's 1-based place in the primary key (0: not in
	// it), so a composite key keeps its column order.
	PkPos   int  `json:"pkPos"`
	Autoinc bool `json:"autoinc"`
	// Unique is a single-column UNIQUE constraint (a column-level UNIQUE,
	// which the database backs with an index of its own).
	Unique bool `json:"unique"`
}

// inspectIndex is one index: one a CREATE INDEX made, or one that backs a
// UNIQUE (or exclusion) constraint (`Constraint`; its name is the
// database's own). The index that backs the primary key is not listed (the
// columns carry `pkPos`), unless it has unsupported features. Nor is an exact single-column UNIQUE constraint
// (the column carries `unique`).
//
// `Inexact` says why the column list does not describe the index (an
// expression column, a non-default collation or operator class, a DESC
// column, INCLUDE columns, an exclusion constraint), "" when it does. A
// Store declares only plain column lists, so `sky db check` never treats an
// inexact index as one the code declares (v0.29.2: an expression column was
// dropped from the list, an INCLUDE column added to it, `COLLATE NOCASE`
// lost, and the check then said "matches").
type inspectIndex struct {
	Name       string   `json:"name"`
	Columns    []string `json:"columns"`
	Unique     bool     `json:"unique"`
	Where      string   `json:"where"`
	Constraint bool     `json:"constraint,omitempty"`
	Inexact    string   `json:"inexact,omitempty"`
	Definition string   `json:"definition,omitempty"`
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
			c := inspectColumn{Name: name, Type: typ, NotNull: notnull != 0 || pk != 0, Pk: pk != 0, PkPos: pk}
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
		colls := sqliteColumnCollations(t.sql)
		for _, ix := range ixs {
			if ix.origin == "pk" {
				continue
			}
			cols, inexact, err := sqliteIndexParts(conn, ix.name, colls)
			if err != nil {
				return nil, err
			}
			// An exact one-column UNIQUE constraint is the column's UNIQUE.
			// Every other UNIQUE constraint is listed as the index it is: a
			// multi-column one is what Store.uniqueOn declares (v0.29.2: it
			// was skipped, so the check said "matches" without it).
			if ix.origin == "u" && len(cols) == 1 && inexact == "" {
				for i := range it.Columns {
					if it.Columns[i].Name == cols[0] {
						it.Columns[i].Unique = true
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
				Constraint: ix.origin == "u", Inexact: inexact, Definition: ddl.String,
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

// sqliteIndexParts reads an index's key columns from PRAGMA index_xinfo, and
// says why the list does not describe the index ("" when it does): an
// expression column (cid -2), a DESC column, or a collation other than the
// default (`UNIQUE (email COLLATE NOCASE)` is case-insensitive, a Store
// `unique "email"` is not). An inherited column collation has the same effect.
// The decimal collation is the one collation a Store declares itself.
// An expression column is listed as `<expression>`.
func sqliteIndexParts(conn *sql.DB, index string, colls map[string]string) ([]string, string, error) {
	rows, err := conn.Query(`PRAGMA index_xinfo(` + sqliteQuoteIdent(index) + `)`)
	if err != nil {
		return nil, "", fmt.Errorf("columns of index %s: %v", index, err)
	}
	defer rows.Close()
	type part struct {
		seq  int
		name string
	}
	var parts []part
	var why []string
	for rows.Next() {
		// seqno, cid, name, desc, coll, key
		var seq, cid, desc, key int
		var name, coll sql.NullString
		if err := rows.Scan(&seq, &cid, &name, &desc, &coll, &key); err != nil {
			return nil, "", err
		}
		if key == 0 {
			continue // the rowid an index carries for its own use
		}
		n := name.String
		switch {
		case cid == -2 || !name.Valid:
			n = "<expression>"
			why = append(why, "an expression column")
		case desc != 0:
			why = append(why, "column "+n+" is DESC")
		}
		if name.Valid && cid >= 0 && desc == 0 {
			own := colls[strings.ToLower(n)]
			if own != "SKY_DECIMAL" {
				own = "BINARY"
			}
			if c := strings.ToUpper(coll.String); c != "" && c != own {
				why = append(why, "column "+n+" has COLLATE "+c)
			}
		}
		parts = append(parts, part{seq, n})
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].seq < parts[j].seq })
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, p.name)
	}
	return out, strings.Join(why, ", "), nil
}

// sqliteColumnCollations reads each column's declared collation (`name TEXT
// COLLATE NOCASE`) from a CREATE TABLE statement, upper-cased, by lower-cased
// column name. A column with no COLLATE clause is absent (BINARY). An index
// on such a column takes its collation by default, so only a collation that
// differs from the column's makes an index inexact.
func sqliteColumnCollations(ddl string) map[string]string {
	out := map[string]string{}
	open := strings.Index(ddl, "(")
	if open < 0 {
		return out
	}
	for _, def := range sqliteSplitTopLevel(ddl[open+1:]) {
		toks := sqliteTokens(def)
		if len(toks) == 0 {
			continue
		}
		switch strings.ToUpper(toks[0]) {
		case "CONSTRAINT", "PRIMARY", "UNIQUE", "CHECK", "FOREIGN":
			continue // a table constraint, not a column
		}
		col := strings.ToLower(sqliteUnquote(toks[0]))
		for i := 1; i+1 < len(toks); i++ {
			if strings.EqualFold(toks[i], "COLLATE") {
				out[col] = strings.ToUpper(sqliteUnquote(toks[i+1]))
			}
		}
	}
	return out
}

// sqliteSplitTopLevel splits a column-definition list at its top-level commas
// and stops at the list's closing parenthesis.
func sqliteSplitTopLevel(s string) []string {
	var out []string
	depth, start := 0, 0
	var quote byte
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case quote != 0:
			if ch == quote {
				quote = 0
			}
		case ch == '\'' || ch == '"' || ch == '`':
			quote = ch
		case ch == '[':
			quote = ']'
		case ch == '(':
			depth++
		case ch == ')':
			if depth == 0 {
				return append(out, s[start:i])
			}
			depth--
		case ch == ',' && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// sqliteTokens splits one definition into words, a quoted name staying one
// word and a parenthesised group being skipped.
func sqliteTokens(s string) []string {
	var out []string
	i := 0
	for i < len(s) {
		ch := s[i]
		switch {
		case ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r':
			i++
		case ch == '(':
			depth := 0
			for i < len(s) {
				if s[i] == '(' {
					depth++
				} else if s[i] == ')' {
					depth--
					if depth == 0 {
						i++
						break
					}
				}
				i++
			}
		case ch == '\'' || ch == '"' || ch == '`' || ch == '[':
			end := ch
			if ch == '[' {
				end = ']'
			}
			j := i + 1
			for j < len(s) && s[j] != end {
				j++
			}
			if j < len(s) {
				j++
			}
			out = append(out, s[i:j])
			i = j
		default:
			j := i
			for j < len(s) && !strings.ContainsRune(" \t\n\r('\"`[", rune(s[j])) {
				j++
			}
			out = append(out, s[i:j])
			i = j
		}
	}
	return out
}

func sqliteUnquote(s string) string {
	if len(s) >= 2 {
		switch s[0] {
		case '"', '\'', '`':
			return strings.ReplaceAll(s[1:len(s)-1], string(s[0])+string(s[0]), string(s[0]))
		case '[':
			return s[1 : len(s)-1]
		}
	}
	return s
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
		// Each primary-key column with its 1-based place in the key, so a
		// composite key keeps its order (v0.29.2).
		pks := map[string]int{}
		prows, err := conn.Query(
			`SELECT a.attname, k.ord FROM pg_index i
			 CROSS JOIN LATERAL unnest(i.indkey::int2[]) WITH ORDINALITY AS k(attnum, ord)
			 JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
			 WHERE i.indrelid = `+rel+` AND i.indisprimary AND k.ord <= i.indnkeyatts`, name)
		if err != nil {
			return nil, fmt.Errorf("primary key of %s: %v", name, err)
		}
		for prows.Next() {
			var a string
			var ord int
			if err := prows.Scan(&a, &ord); err != nil {
				prows.Close()
				return nil, err
			}
			pks[a] = ord
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
			c := inspectColumn{Name: cname, Type: typ, NotNull: nullable == "NO", Pk: pks[cname] > 0, PkPos: pks[cname]}
			if dflt.Valid {
				v := dflt.String
				c.Default = &v
			}
			c.Autoinc = identity == "YES" || (dflt.Valid && strings.HasPrefix(dflt.String, "nextval("))
			it.Columns = append(it.Columns, c)
		}
		crows.Close()
		// The key columns only (`indnkeyatts`: INCLUDE columns are not part
		// of the key), an expression column by its text, and why the list
		// does not describe the index. The constraint is the table's own
		// UNIQUE / PRIMARY KEY / EXCLUDE (`conrelid`): a foreign key on
		// another table names this index in `conindid` too.
		// NULLS NOT DISTINCT changes which rows a unique index accepts and
		// cannot be declared by a Store. Read the catalogue flag through JSON
		// so PostgreSQL before 15, which has no such field, still works.
		irows, err := conn.Query(
			`SELECT c.relname, i.indisunique, i.indisprimary,
			        COALESCE(pg_get_expr(i.indpred, i.indrelid), ''),
			        array_to_json(ARRAY(
			            SELECT COALESCE(a.attname::text, pg_get_indexdef(i.indexrelid, k.ord::int, true))
			            FROM unnest(i.indkey::int2[]) WITH ORDINALITY AS k(attnum, ord)
			            LEFT JOIN pg_attribute a
			              ON a.attrelid = i.indrelid AND a.attnum = k.attnum AND k.attnum > 0
			            WHERE k.ord <= i.indnkeyatts
			            ORDER BY k.ord))::text,
			        COALESCE((SELECT con.contype::text FROM pg_constraint con
			                  WHERE con.conindid = i.indexrelid AND con.conrelid = i.indrelid
			                    AND con.contype IN ('u', 'p', 'x') LIMIT 1), ''),
			        i.indexprs IS NOT NULL,
			        i.indnatts > i.indnkeyatts,
			        EXISTS (SELECT 1 FROM unnest(i.indoption::int2[]) AS o(opt) WHERE o.opt <> 0),
			        EXISTS (SELECT 1 FROM unnest(i.indkey::int2[], i.indcollation::oid[]) AS kc(attnum, coll)
			                JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = kc.attnum
			                WHERE kc.attnum > 0 AND kc.coll IS NOT NULL AND kc.coll <> 0
			                  AND (kc.coll <> a.attcollation
			                       OR a.attcollation <> 'pg_catalog."default"'::regcollation)),
			        EXISTS (SELECT 1 FROM unnest(i.indclass::oid[]) AS cl(opc)
			                JOIN pg_opclass oc ON oc.oid = cl.opc WHERE NOT oc.opcdefault),
			        COALESCE((to_jsonb(i)->>'indnullsnotdistinct')::boolean, false),
			        NOT i.indimmediate,
			        NOT (i.indisvalid AND i.indisready AND i.indislive),
			        am.amname,
			        pg_get_indexdef(i.indexrelid)
			 FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid JOIN pg_am am ON am.oid = c.relam
			 WHERE i.indrelid = `+rel+` ORDER BY c.relname`, name)
		if err != nil {
			return nil, fmt.Errorf("indexes of %s: %v", name, err)
		}
		for irows.Next() {
			var iname, pred, colsJSON, contype, am, def string
			var uniq, primary bool
			var features pgIndexFeatures
			if err := irows.Scan(&iname, &uniq, &primary, &pred, &colsJSON, &contype,
				&features.exprs, &features.include, &features.desc, &features.coll,
				&features.opclass, &features.nullsNotDistinct, &features.deferrable,
				&features.unusable, &am, &def); err != nil {
				irows.Close()
				return nil, err
			}
			colList := []string{}
			if err := json.Unmarshal([]byte(colsJSON), &colList); err != nil {
				irows.Close()
				return nil, fmt.Errorf("columns of index %s: %v", iname, err)
			}
			inexact := pgIndexInexact(features, am, contype)
			if primary && inexact == "" {
				continue
			}
			// An exact one-column UNIQUE constraint is the column's UNIQUE.
			// Every other constraint index is listed: a multi-column UNIQUE
			// constraint is what Store.uniqueOn declares (v0.29.2: skipped).
			if contype == "u" && uniq && len(colList) == 1 && inexact == "" && pred == "" {
				for i := range it.Columns {
					if it.Columns[i].Name == colList[0] {
						it.Columns[i].Unique = true
					}
				}
				continue
			}
			it.Indexes = append(it.Indexes, inspectIndex{
				Name: iname, Columns: colList, Unique: uniq, Where: pred,
				Constraint: contype != "", Inexact: inexact, Definition: def,
			})
		}
		if err := irows.Err(); err != nil {
			irows.Close()
			return nil, err
		}
		irows.Close()
		out = append(out, it)
	}
	return out, nil
}

type pgIndexFeatures struct {
	exprs, include, desc, coll, opclass bool
	nullsNotDistinct, deferrable, unusable bool
}

// pgIndexInexact says why an index's key-column list does not describe it,
// or "" when it does. A Store declares a plain B-tree index over columns
// with the database's default collation and operator class, ascending.
func pgIndexInexact(f pgIndexFeatures, am, contype string) string {
	var why []string
	if f.exprs {
		why = append(why, "an expression column")
	}
	if f.include {
		why = append(why, "INCLUDE columns")
	}
	if f.desc {
		why = append(why, "a DESC or NULLS FIRST column")
	}
	if f.coll {
		why = append(why, "a column with a COLLATE of its own")
	}
	if f.opclass {
		why = append(why, "an operator class that is not the default")
	}
	if f.nullsNotDistinct {
		why = append(why, "NULLS NOT DISTINCT")
	}
	if f.deferrable {
		why = append(why, "a deferrable constraint")
	}
	if f.unusable {
		why = append(why, "an index that is not valid, ready and live")
	}
	if am != "" && am != "btree" {
		why = append(why, "the "+am+" index method")
	}
	if contype == "x" {
		why = append(why, "an exclusion constraint")
	}
	return strings.Join(why, ", ")
}

//go:build !js

// Package rt — migration op renderer. Turns dialect-NEUTRAL typed migration ops
// (the JSON committed in db/migrations/) into dialect-correct SQL, so one
// migration file applies to SQLite AND Postgres. Apply reuses the existing
// checksummed _sky_migrations ledger (Db_migrateApply).
package rt

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// migDefault is a column DEFAULT in the dialect-neutral op JSON. It is also
// the shape Db_dumpProject writes, so a captured default round-trips dump →
// schema.json → op → renderMigOp unchanged.
//
// NowAtApply is the v2 addColumn form of a `Store.defaultNow` column.
// PostgreSQL renders it `now()`. SQLite refuses a non-constant default in
// ADD COLUMN, so there it renders the constant token migNowToken, which
// dbMigrateCore swaps for the apply time (only in a v2 text).
type migDefault struct {
	Int        *int64   `json:"int,omitempty"`
	Text       *string  `json:"text,omitempty"`
	Bool       *bool    `json:"bool,omitempty"`
	Float      *float64 `json:"float,omitempty"`
	Now        bool     `json:"now,omitempty"`
	NowAtApply bool     `json:"nowAtApply,omitempty"`
}

// The v2 migration text. `sky db migrate --gen` writes `"v": 2` on every new
// op. A file with any v2 op renders with migV2Header as its first line, and
// a SQLite `defaultNow` column added by a v2 op renders the constant
// migNowToken. The text is a pure function of the op JSON, so its checksum is
// stable. dbMigrateCore swaps the token for the apply time AFTER it takes the
// checksum, and only in a text that starts with the header. A v1 op (no `v`)
// renders exactly as v0.28.1 did, so an applied v1 migration keeps its
// checksum.
const (
	migV2Header       = "-- sky:migration v2"
	migNowToken       = "'__sky_now_at_apply__'"
	migNowMsToken     = "'__sky_now_ms_at_apply__'"
	sqliteNowLayout   = "2006-01-02 15:04:05"
	migIndexHashChars = 8
)

// sqliteNowLiteral is the clock in SQLite's `datetime('now')` text format
// (UTC), quoted as a SQL string literal.
func sqliteNowLiteral() string {
	return "'" + time.Now().UTC().Format(sqliteNowLayout) + "'"
}

// migApplyText is the SQL that dbMigrateCore runs for a migration text. Only
// a v2 text (first line migV2Header) has its migNowToken replaced with the
// apply time. Every other text (v1 files, hand-written Db.migrate SQL,
// Ai_migrate) runs as given.
func migApplyText(stmt string) string {
	if !strings.HasPrefix(stmt, migV2Header+"\n") {
		return stmt
	}
	now := time.Now().UTC()
	stmt = strings.ReplaceAll(stmt, migNowToken, "'"+now.Format(sqliteNowLayout)+"'")
	return strings.ReplaceAll(stmt, migNowMsToken, strconv.FormatInt(now.UnixMilli(), 10))
}

// migNowMs is the SQLite epoch-milliseconds constant that goes with nowAt:
// the v2 token for the v2 token, else the time nowAt names.
func migNowMs(nowAt string) string {
	if nowAt == migNowToken {
		return migNowMsToken
	}
	at, err := time.Parse(sqliteNowLayout, strings.Trim(nowAt, "'"))
	if err != nil {
		at = time.Now().UTC()
	}
	return strconv.FormatInt(at.UnixMilli(), 10)
}

type migColumn struct {
	Name     string      `json:"name"`
	Type     string      `json:"type"` // codec kind: text/int/real/bool/blob (→ codecColKindToSchema)
	Nullable bool        `json:"nullable"`
	Pk       bool        `json:"pk"`
	Autoinc  bool        `json:"autoinc"`
	Unique   bool        `json:"unique"`
	Default  *migDefault `json:"default,omitempty"`
}

type migOp struct {
	Kind         string      `json:"kind"`
	V            int         `json:"v,omitempty"`
	Table        string      `json:"table,omitempty"`
	Column       string      `json:"column,omitempty"`
	Type         string      `json:"type,omitempty"`
	Nullable     bool        `json:"nullable,omitempty"`
	Default      *migDefault `json:"default,omitempty"`
	From         string      `json:"from,omitempty"`
	To           string      `json:"to,omitempty"`
	Name         string      `json:"name,omitempty"`
	Columns      []migColumn `json:"columns,omitempty"`
	IndexColumns []string    `json:"indexColumns,omitempty"`
	Unique       bool        `json:"unique,omitempty"`
	Where        string      `json:"where,omitempty"`
	Sql          string      `json:"sql,omitempty"`
}

type migFile struct {
	Id  string  `json:"id"`
	Ops []migOp `json:"ops"`
}

func migDefKind(d *migDefault) string {
	switch {
	case d == nil:
		return "none"
	case d.Int != nil:
		return "int"
	case d.Text != nil:
		return "text"
	case d.Bool != nil:
		return "bool"
	case d.Float != nil:
		return "float"
	case d.Now, d.NowAtApply:
		return "now"
	}
	return "none"
}

func migDefVal(d *migDefault) string {
	switch {
	case d == nil:
		return ""
	case d.Int != nil:
		return strconv.FormatInt(*d.Int, 10)
	case d.Text != nil:
		return *d.Text
	case d.Bool != nil:
		if *d.Bool {
			return "true"
		}
		return "false"
	case d.Float != nil:
		return strconv.FormatFloat(*d.Float, 'g', -1, 64)
	}
	return ""
}

// migDefaultOf turns a colspec default (codecColExtras) into the op shape.
// Db_dumpProject and Db_autoMigrate both read a colspec through here.
func migDefaultOf(defKind, defVal string) *migDefault {
	switch defKind {
	case "now":
		return &migDefault{Now: true}
	case "int":
		if n, err := strconv.ParseInt(defVal, 10, 64); err == nil {
			return &migDefault{Int: &n}
		}
	case "text":
		v := defVal
		return &migDefault{Text: &v}
	case "bool":
		b := defVal == "true"
		return &migDefault{Bool: &b}
	case "float":
		if f, err := strconv.ParseFloat(defVal, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			return &migDefault{Float: &f}
		}
	}
	return nil
}

// codecColspecMigColumn reads one colspec entry (name, kind with flags) as a
// migColumn. It is the ONE reading of a Store column that Db_autoMigrate and
// Db_dumpProject share, so `Store.migrate` and `sky db migrate --gen` start
// from the same column.
func codecColspecMigColumn(name, rawKind, pk string) migColumn {
	base, nullable := codecSplitKind(rawKind)
	unique, defKind, defVal := codecColExtras(rawKind)
	return migColumn{
		Name: name, Type: base, Nullable: nullable, Pk: name == pk,
		Autoinc: codecColIsAutoInc(rawKind), Unique: unique,
		Default: migDefaultOf(defKind, defVal),
	}
}

// migZeroDefault is the DEFAULT of a required column added with no declared
// default: the zero value of its kind. A real column takes `{"int":0}`, so
// it renders `0`, the same bytes as `sky db migrate --gen` writes. A blob has
// no simple zero, so it is nil (the column is added nullable, with a warning).
func migZeroDefault(kind string) *migDefault {
	switch kind {
	case "int", "bigint", "real":
		z := int64(0)
		return &migDefault{Int: &z}
	case "text":
		z := ""
		return &migDefault{Text: &z}
	case "bool":
		f := false
		return &migDefault{Bool: &f}
	}
	return nil
}

// migAddDefault renders a DEFAULT for ADD COLUMN of a column of schema
// `kind`. A `now` default is `now()` on PostgreSQL. On SQLite it is the
// constant nowAt, because SQLite refuses a non-constant default in ADD
// COLUMN. An integer column takes epoch milliseconds (see schemaNowDefault).
func migAddDefault(driver, kind string, d *migDefault, nowAt string) string {
	if d.Now || d.NowAtApply {
		if driver == "pgx" {
			return schemaNowDefault(driver, kind)
		}
		if schemaIsIntKind(kind) {
			return migNowMs(nowAt)
		}
		return nowAt
	}
	return schemaDefault(driver, migDefKind(d), migDefVal(d))
}

// renderAddColumn is the ONE renderer of an added column. Db_autoMigrate
// (`Store.migrate`, `sky db push`) and renderMigOp for a v2 addColumn op
// (`sky db migrate`) both call it, so the two paths give the same DDL per
// dialect by construction. The rule (user decision D1):
//
//   - a nullable (Maybe) column is added nullable, with its declared default
//     if it has one;
//   - any other column is added NOT NULL DEFAULT <declared, else the zero of
//     its kind>, so the rows that exist read a value;
//   - a required blob has no simple zero: it is added nullable, and a warning
//     is returned;
//   - a `now` default is `now()` on PostgreSQL and the constant nowAt on
//     SQLite (the clock at the ALTER, or migNowToken in a v2 op);
//   - a unique column is added without UNIQUE (SQLite refuses it in ADD
//     COLUMN) and then gets a unique index named by codecIndexName. A required
//     unique column cannot be added to a table with rows: every row would get
//     the same value.
//
// An existing column is never altered: callers only pass a column the table
// does not have.
func renderAddColumn(driver, table string, c migColumn, nowAt string, tableHasRows bool) ([]string, []string, error) {
	if !codecValidIdent(table) {
		return nil, nil, fmt.Errorf("invalid table identifier %q", table)
	}
	if !codecValidIdent(c.Name) {
		return nil, nil, fmt.Errorf("invalid column identifier %q", c.Name)
	}
	var warnings []string
	kind := codecColKindToSchema(c.Type)
	s := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, c.Name, schemaTypeName(driver, kind))
	nullable := c.Nullable
	d := c.Default
	if !nullable && d == nil {
		d = migZeroDefault(c.Type)
		if d == nil {
			nullable = true
			warnings = append(warnings, fmt.Sprintf(
				"column %s.%s is a required non-scalar — added NULLABLE (no default possible)", table, c.Name))
		}
	}
	if nullable {
		if d != nil {
			s += " DEFAULT " + migAddDefault(driver, kind, d, nowAt)
		}
	} else {
		s += " NOT NULL DEFAULT " + migAddDefault(driver, kind, d, nowAt)
	}
	stmts := []string{s}
	if c.Unique {
		if !nullable && tableHasRows {
			return nil, nil, fmt.Errorf(
				"column %s.%s: a required unique column cannot be added to a table with rows, every row would get the same value",
				table, c.Name)
		}
		cols := []string{c.Name}
		stmts = append(stmts, renderIndexDDL(table, codecIndexName(table, cols, true, ""), cols, true, ""))
	}
	return stmts, warnings, nil
}

// renderIndexDDL renders one CREATE INDEX. `where` is a partial-index
// predicate (Store.uniqueWhere), rendered from constants only.
func renderIndexDDL(table, name string, cols []string, unique bool, where string) string {
	kw := "INDEX"
	if unique {
		kw = "UNIQUE INDEX"
	}
	s := fmt.Sprintf("CREATE %s IF NOT EXISTS %s ON %s (%s)", kw, name, table, strings.Join(cols, ", "))
	if where != "" {
		s += " WHERE " + where
	}
	return s
}

// codecIndexName names a Store index: `ix_<table>_<h>` or `ux_<table>_<h>`,
// where <h> is 8 hex digits of FNV-1a 32 over the table, the columns, the
// unique flag and the predicate. Every definition gets its own name, so two
// tables can never share one (`a_b` + `c` and `a` + `b, c` differ), and a
// changed predicate gives a new index. The table part is cut to 51 bytes so
// the name stays inside PostgreSQL's 63-byte limit. rust/crates/sky/src/
// db_migrate.rs `index_name` computes the same name; the parity fixture pins
// both.
func codecIndexName(table string, cols []string, unique bool, where string) string {
	flag, prefix := "0", "ix_"
	if unique {
		flag, prefix = "1", "ux_"
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(table + "|" + strings.Join(cols, ",") + "|" + flag + "|" + where))
	t := table
	if len(t) > 51 {
		t = t[:51]
	}
	return fmt.Sprintf("%s%s_%0*x", prefix, t, migIndexHashChars, h.Sum32())
}

func migColDefaultClause(driver string, d *migDefault) string {
	if d == nil {
		return ""
	}
	return " DEFAULT " + schemaDefault(driver, migDefKind(d), migDefVal(d))
}

// renderMigOp → the dialect-correct SQL for one op.
func renderMigOp(driver string, op migOp) (string, error) {
	if !codecValidIdent(op.Table) && op.Kind != "dropIndex" && op.Kind != "raw" {
		return "", fmt.Errorf("migration: invalid table identifier %q", op.Table)
	}
	switch op.Kind {
	case "createTable":
		// Route through the SAME per-column map builder the push/create path uses
		// (schemaColMap in db_codec.go) so the committed-migration CREATE TABLE is
		// byte-identical to `sky db push` — carrying serial AUTOINCREMENT/BIGSERIAL,
		// UNIQUE, and DEFAULT. Both paths converge on schemaRenderTable; the two DDL
		// renderings cannot diverge.
		//
		// A v1 op keeps the v0.28.1 bytes (a non-serial PK without NOT NULL,
		// a `now` default as timestamp text on every kind), so an applied v1
		// migration keeps its checksum. A v2 op gets B8 (a PK is always NOT
		// NULL) and an integer `now` column in epoch milliseconds.
		cols := make([]any, 0, len(op.Columns))
		for _, c := range op.Columns {
			m := schemaColMap(
				c.Name, c.Type, c.Pk, c.Nullable, c.Unique, c.Autoinc,
				migDefKind(c.Default), migDefVal(c.Default))
			if op.V < 2 {
				m["Legacy"] = true
			}
			cols = append(cols, m)
		}
		sm := map[string]any{"Name": op.Table, "Columns": cols, "Indexes": []any{}}
		return strings.Join(schemaRenderTable(driver, sm), ";\n"), nil

	case "addColumn":
		if !codecValidIdent(op.Column) {
			return "", fmt.Errorf("migration: invalid column identifier %q", op.Column)
		}
		if op.V >= 2 {
			stmts, _, err := renderAddColumn(driver, op.Table, migColumn{
				Name: op.Column, Type: op.Type, Nullable: op.Nullable,
				Unique: op.Unique, Default: op.Default,
			}, migNowToken, false)
			if err != nil {
				return "", fmt.Errorf("migration: %v", err)
			}
			return strings.Join(stmts, ";\n"), nil
		}
		s := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", op.Table, op.Column, schemaTypeName(driver, codecColKindToSchema(op.Type)))
		if !op.Nullable {
			s += " NOT NULL" + migColDefaultClause(driver, op.Default)
		} else if op.Default != nil {
			s += migColDefaultClause(driver, op.Default)
		}
		return s, nil

	case "dropColumn":
		if !codecValidIdent(op.Column) {
			return "", fmt.Errorf("migration: invalid column identifier %q", op.Column)
		}
		return fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s", op.Table, op.Column), nil

	case "renameColumn":
		if !codecValidIdent(op.From) || !codecValidIdent(op.To) {
			return "", fmt.Errorf("migration: invalid rename identifiers")
		}
		return fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s", op.Table, op.From, op.To), nil

	case "addIndex":
		if !codecValidIdent(op.Name) {
			return "", fmt.Errorf("migration: invalid index name %q", op.Name)
		}
		// B10: the column names are spliced into the DDL, so each one must be
		// an identifier.
		if len(op.IndexColumns) == 0 {
			return "", fmt.Errorf("migration: index %q has no columns", op.Name)
		}
		for _, c := range op.IndexColumns {
			if !codecValidIdent(c) {
				return "", fmt.Errorf("migration: index %q: invalid column identifier %q", op.Name, c)
			}
		}
		return renderIndexDDL(op.Table, op.Name, op.IndexColumns, op.Unique, op.Where), nil

	case "dropIndex":
		if !codecValidIdent(op.Name) {
			return "", fmt.Errorf("migration: invalid index name %q", op.Name)
		}
		return "DROP INDEX IF EXISTS " + op.Name, nil

	case "raw":
		return op.Sql, nil
	}
	return "", fmt.Errorf("migration: unknown op kind %q", op.Kind)
}

// Db_renderMigrations : Db -> String(json) -> Task Error (List (String, String)).
// Renders a JSON array of {id, ops} into (id, sql) pairs for Db.migrateApply
// (which owns the checksummed _sky_migrations ledger).
func Db_renderMigrations(connArg, jsonArg any) any {
	return func() any {
		d, ok := connArg.(*SkyDb)
		if !ok {
			return Err[any, any](ErrInvalidInput("renderMigrations: first argument is not a Db"))
		}
		var files []migFile
		if err := json.Unmarshal([]byte(AsString(jsonArg)), &files); err != nil {
			return Err[any, any](ErrInvalidInput("renderMigrations: bad JSON: " + err.Error()))
		}
		out := make([]any, 0, len(files))
		for _, f := range files {
			text, err := renderMigFile(d.driver, f)
			if err != nil {
				return Err[any, any](ErrInvalidInput(err.Error()))
			}
			out = append(out, T2[any, any]{V0: f.Id, V1: text})
		}
		return Ok[any, any](out)
	}
}

// renderMigFile renders one migration file to the text the ledger checksums.
// A file with any v2 op starts with the migV2Header line.
func renderMigFile(driver string, f migFile) (string, error) {
	v2 := false
	body := make([]string, 0, len(f.Ops))
	for _, op := range f.Ops {
		if op.V >= 2 {
			v2 = true
		}
		s, err := renderMigOp(driver, op)
		if err != nil {
			return "", err
		}
		body = append(body, s)
	}
	text := strings.Join(body, ";\n")
	if v2 {
		return migV2Header + "\n" + text, nil
	}
	return text, nil
}

var (
	migAddRequiredRe = regexp.MustCompile(
		`^ALTER TABLE ([A-Za-z_][A-Za-z0-9_]*) ADD COLUMN ([A-Za-z_][A-Za-z0-9_]*) .* NOT NULL DEFAULT `)
	migUniqueIndexRe = regexp.MustCompile(
		`^CREATE UNIQUE INDEX IF NOT EXISTS [A-Za-z_][A-Za-z0-9_]* ON ([A-Za-z_][A-Za-z0-9_]*) \(([^)]*)\)`)
)

// migRequiredUniqueHint explains a v2 migration that failed because it adds
// a required unique column to a table that already has 2 or more rows.
// `sky db migrate --gen` writes such a column as an addColumn op (NOT NULL
// DEFAULT <declared, else zero>) and then an addIndex op (UNIQUE). Every row
// that exists takes the same default, so the unique index fails, and the
// database error names an index, not the cause. The text is a pure function
// of the op JSON, so the hint reads the rendered statements: a column that
// this text adds NOT NULL and then puts in a UNIQUE index of the same table.
// It is used only when the error is a unique violation and, after the
// rollback, the table holds 2 or more rows. It returns "" otherwise, and the
// caller reports the database error as it is.
func migRequiredUniqueHint(d *SkyDb, text string, execErr error) string {
	if execErr == nil || !strings.HasPrefix(text, migV2Header+"\n") ||
		!strings.Contains(strings.ToLower(execErr.Error()), "unique") {
		return ""
	}
	added := map[string]bool{}
	for _, raw := range strings.Split(strings.TrimPrefix(text, migV2Header+"\n"), ";\n") {
		stmt := strings.TrimSpace(raw)
		if m := migAddRequiredRe.FindStringSubmatch(stmt); m != nil {
			added[m[1]+"."+m[2]] = true
			continue
		}
		m := migUniqueIndexRe.FindStringSubmatch(stmt)
		if m == nil {
			continue
		}
		table := m[1]
		for _, c := range strings.Split(m[2], ",") {
			col := strings.TrimSpace(c)
			if !added[table+"."+col] || !codecValidIdent(table) {
				continue
			}
			n, err := migRowCountUpTo2(d, table)
			if err != nil || n < 2 {
				continue
			}
			return fmt.Sprintf(
				"column %s.%s is required and unique, and table %s already has rows: "+
					"the added column gives every row the same default, so the unique index "+
					"cannot be built (the migration was rolled back). Fix: give each row its "+
					"own value in a data step, a {\"kind\": \"raw\", \"sql\": \"UPDATE %s SET %s = ...\"} "+
					"op between the addColumn op and the addIndex op, or make the field a Maybe "+
					"so the column is nullable. Database error: %v",
				table, col, table, table, col, execErr)
		}
	}
	return ""
}

// migRowCountUpTo2 counts the rows of a table, stopping at 2.
func migRowCountUpTo2(d *SkyDb, table string) (int, error) {
	var n int
	err := d.conn.QueryRow("SELECT COUNT(*) FROM (SELECT 1 AS x FROM " + table + " LIMIT 2) sky_n").Scan(&n)
	return n, err
}

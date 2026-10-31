//go:build !js

// Package rt — migration op renderer. Turns dialect-NEUTRAL typed migration ops
// (the JSON committed in db/migrations/) into dialect-correct SQL, so one
// migration file applies to SQLite AND Postgres. Apply reuses the existing
// checksummed _sky_migrations ledger (Db_migrateApply).
package rt

import (
	"bytes"
	"crypto/sha256"
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
// NowAtApply is the addColumn form of a `Store.defaultNow` column.
// PostgreSQL renders it `now()`. SQLite refuses a non-constant default in
// ADD COLUMN, so there it renders the constant token migNowToken, which
// dbMigrateCore swaps for the apply time (only in an op-file text).
type migDefault struct {
	Int        *int64   `json:"int,omitempty"`
	Text       *string  `json:"text,omitempty"`
	Bool       *bool    `json:"bool,omitempty"`
	Float      *float64 `json:"float,omitempty"`
	Now        bool     `json:"now,omitempty"`
	NowAtApply bool     `json:"nowAtApply,omitempty"`
}

// The migration text of an op file. A SQLite `defaultNow` column added by an
// op renders the constant migNowToken, so the text is a pure function of the
// op JSON. dbMigrateCore swaps the token for the apply time, and only in the
// text of an op file (never in hand-written Db.migrate SQL).
//
// The ledger checksum of an op file is NOT taken over this text: it is
// migOpsChecksum, a checksum of the ops themselves, so a later change to the
// renderer never makes an applied file look edited.
const (
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

// migApplyText is the SQL that dbMigrateCore runs for the text of an op file:
// migNowToken is replaced with the apply time. The caller takes the checksum
// first, and never calls this for hand-written Db.migrate SQL.
func migApplyText(stmt string) string {
	now := time.Now().UTC()
	stmt = strings.ReplaceAll(stmt, migNowToken, "'"+now.Format(sqliteNowLayout)+"'")
	return strings.ReplaceAll(stmt, migNowMsToken, strconv.FormatInt(now.UnixMilli(), 10))
}

// migNowMs is the SQLite epoch-milliseconds constant that goes with nowAt:
// the milliseconds token for migNowToken, else the time nowAt names.
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
	// Zero is a required column's own field zero when it is not the zero of
	// its kind: the JSON of a blob field's zero, or an enum's zero name (the
	// `zero=` flag Std.Db.Store adds from Codec.fieldZeros), as a text
	// default. It is used only when a column is added (renderAddColumn),
	// never in CREATE TABLE.
	Zero *migDefault `json:"-"`
}

// migOp is one op of a migration file. A `"v"` key, which an earlier v0.29
// development build of `--gen` wrote, is accepted and ignored.
type migOp struct {
	Kind         string      `json:"kind"`
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
		Default: migDefaultOf(defKind, defVal), Zero: codecColZero(rawKind),
	}
}

// migZeroDefault is the DEFAULT of a required column added with no declared
// default: the zero value of its kind. A real column takes `{"int":0}`, so
// it renders `0`, the same bytes as `sky db migrate --gen` writes. A blob has
// no zero of its kind, and an enum's text zero `”` is not one of its names:
// their zero is the field's own (migColumn.Zero, read first), else nil.
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
// (`Store.migrate`, `sky db push`) and renderMigOp for an addColumn op
// (`sky db migrate`) both call it, so the two paths give the same DDL per
// dialect by construction. The rule (user decision D1):
//
//   - a nullable (Maybe) column is added nullable, with its declared default
//     if it has one;
//   - any other column is added NOT NULL DEFAULT <declared, else the zero
//     value>, so the rows that exist read a value. The zero of a required
//     JSON (blob) column is the JSON of its own field's zero, and the zero of
//     a required enum (TEXT) column is the enum's zero name
//     (Codec.fieldZeros, sent as the `zero=` flag), a text literal; the zero
//     of any other column is the zero of its kind;
//   - a required column with no zero value (a blob whose field knows none) is
//     added NOT NULL with no default. Only an empty table takes it, so with
//     rows >= 1 it is an error, and the table is not changed;
//   - a `now` default is `now()` on PostgreSQL and the constant nowAt on
//     SQLite (the clock at the ALTER, or migNowToken in an op file);
//   - a unique column is added without UNIQUE (SQLite refuses it in ADD
//     COLUMN) and then gets a unique index named by codecIndexName. A required
//     unique column cannot be added to a table with rows >= 2: every row would
//     get the same value.
//
// rows is the table's row count, stopped at 2. renderMigOp renders an op file
// without a database and passes 0: there the database refuses the ALTER, and
// dbMigrateCore explains it (migRequiredNoZeroHint, migRequiredUniqueHint).
//
// An existing column is never altered: callers only pass a column the table
// does not have.
func renderAddColumn(driver, table string, c migColumn, nowAt string, rows int) ([]string, error) {
	if !codecValidIdent(table) {
		return nil, fmt.Errorf("invalid table identifier %q", table)
	}
	if !codecValidIdent(c.Name) {
		return nil, fmt.Errorf("invalid column identifier %q", c.Name)
	}
	if err := migDefaultFits(table, c); err != nil {
		return nil, err
	}
	kind := codecColKindToSchema(c.Type)
	s := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, c.Name, schemaTypeName(driver, kind))
	d := migAddedDefault(c)
	switch {
	case c.Nullable:
		if d != nil {
			s += " DEFAULT " + migAddDefault(driver, kind, d, nowAt)
		}
	case d != nil:
		s += " NOT NULL DEFAULT " + migAddDefault(driver, kind, d, nowAt)
	default:
		if rows >= 1 {
			return nil, fmt.Errorf("%s", migNoZeroAdvice(table, c.Name))
		}
		s += " NOT NULL"
	}
	stmts := []string{s}
	if c.Unique {
		if !c.Nullable && rows >= 2 {
			return nil, fmt.Errorf(
				"column %s.%s: a required unique column cannot be added to a table with rows (2 or more), every row would get the same value",
				table, c.Name)
		}
		cols := []string{c.Name}
		stmts = append(stmts, renderIndexDDL(table, codecIndexName(table, cols, true, ""), cols, true, ""))
	}
	return stmts, nil
}

// migDefaultFits refuses a declared default that does not read back as the
// column's kind. An added column's DEFAULT is written into every row the table
// holds, so `DEFAULT 'abc'` on an integer column would give each old row a
// value SQLite stores as text and Sky cannot read as an Int (PostgreSQL
// refuses it). Std.Db.Store and Std.Db.Schema refuse such a default when the
// builder runs; this is the same rule for a hand-edited migration op. A text
// default fits a text or JSON column (JSON must parse), an int default an
// integer or real column, a float default a real column, a bool default a
// bool column, and a `now` default a text or integer column (epoch
// milliseconds). A column type this function does not know is not checked.
func migDefaultFits(table string, c migColumn) error {
	d := c.Default
	if d == nil {
		return nil
	}
	base, _ := codecSplitKind(c.Type)
	intLike := base == "int" || base == "bigint" || base == "timestamp"
	textLike := base == "text" || base == "blob" || base == "json"
	known := intLike || textLike || base == "real" || base == "bool"
	if !known {
		return nil
	}
	ok := true
	switch migDefKind(d) {
	case "int":
		ok = intLike || base == "real"
	case "float":
		ok = base == "real"
	case "bool":
		ok = base == "bool"
	case "now":
		ok = intLike || base == "text"
	case "text":
		ok = textLike
		if ok && base != "text" && !json.Valid([]byte(*d.Text)) {
			return fmt.Errorf("column %s.%s is JSON, and its default %q is not JSON", table, c.Name, *d.Text)
		}
	}
	if !ok {
		return fmt.Errorf("column %s.%s is %s, and its %s default %q does not fit it",
			table, c.Name, base, migDefKind(d), migDefVal(d))
	}
	return nil
}

// migAddedDefault is the DEFAULT of an added column under rule D1: the
// declared default, else (for a required column) the codec's zero value of
// the column, else the zero of its kind. nil for a nullable column with no
// declared default, and for a required column with no zero value.
func migAddedDefault(c migColumn) *migDefault {
	if c.Default != nil || c.Nullable {
		return c.Default
	}
	if c.Zero != nil {
		return c.Zero
	}
	return migZeroDefault(c.Type)
}

// migNeedsRowCount reports whether renderAddColumn reads the row count for
// this column: a required unique column, or a required column with no zero.
func migNeedsRowCount(c migColumn) bool {
	return !c.Nullable && (c.Unique || migAddedDefault(c) == nil)
}

// migNoZeroAdvice explains a required column with no zero value added to a
// table with rows. Store.migrate returns it, and dbMigrateCore puts it in the
// error of a migration file that the database refused. The zero is the
// column's own field zero (Codec.fieldZeros): a taggedUnion, a result,
// Codec.error, or an object holding one has none.
func migNoZeroAdvice(table, col string) string {
	return fmt.Sprintf(
		"column %s.%s is required and its field's codec has no zero value (a taggedUnion, a "+
			"result, Codec.error, or an object holding one), so it is added NOT NULL with no "+
			"default, and table %s already has rows that would hold no value. Fix: make the field "+
			"a Maybe (a nullable column), derive the record codec with Codec.auto or Codec.autoWith "+
			"(the blank record's value for the field gives the default), or give the rows a value "+
			"in a data step",
		table, col, table)
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
		// renderings cannot diverge. B8: a PK is always NOT NULL, and an
		// integer `now` column takes epoch milliseconds.
		cols := make([]any, 0, len(op.Columns))
		for _, c := range op.Columns {
			cols = append(cols, schemaColMap(
				c.Name, c.Type, c.Pk, c.Nullable, c.Unique, c.Autoinc,
				migDefKind(c.Default), migDefVal(c.Default)))
		}
		sm := map[string]any{"Name": op.Table, "Columns": cols, "Indexes": []any{}}
		return strings.Join(schemaRenderTable(driver, sm), ";\n"), nil

	case "addColumn":
		if !codecValidIdent(op.Column) {
			return "", fmt.Errorf("migration: invalid column identifier %q", op.Column)
		}
		stmts, err := renderAddColumn(driver, op.Table, migColumn{
			Name: op.Column, Type: op.Type, Nullable: op.Nullable,
			Unique: op.Unique, Default: op.Default,
		}, migNowToken, 0)
		if err != nil {
			return "", fmt.Errorf("migration: %v", err)
		}
		return strings.Join(stmts, ";\n"), nil

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
// Renders a JSON array of {id, ops} into (id, sql) pairs, without applying
// (`sky db migrate --sql`). Db_migrateOps applies them.
func Db_renderMigrations(connArg, jsonArg any) any {
	return func() any {
		d, ok := connArg.(*SkyDb)
		if !ok {
			return Err[any, any](ErrInvalidInput("renderMigrations: first argument is not a Db"))
		}
		entries, err := migOpEntries(d.driver, AsString(jsonArg))
		if err != nil {
			return Err[any, any](ErrInvalidInput("renderMigrations: " + err.Error()))
		}
		out := make([]any, 0, len(entries))
		for _, e := range entries {
			out = append(out, T2[any, any]{V0: e.name, V1: e.sql})
		}
		return Ok[any, any](out)
	}
}

// Db_migrateOps : Db -> String(json) -> Task Error (List String).
// The kernel behind Std.Db.Migrate.migrateOps, `sky db migrate` and
// `SKY_DB_OP=migrate ./app`: renders each {id, ops} file and applies it
// through the `_sky_migrations` ledger with the ops checksum (migOpsChecksum).
// Under SKY_DB_OP it reports and exits, as Db_migrateApply does.
func Db_migrateOps(connArg, jsonArg any) any {
	raw := AsString(jsonArg)
	return dbMigrateTask(connArg, func(d *SkyDb) ([]migEntry, error) {
		return migOpEntries(d.driver, raw)
	})
}

// migEntry is one migration for dbMigrateCore. opsSum is "" for hand-written
// SQL (Db.migrate): its ledger checksum is the sha256 of sql. For an op file
// it is migOpsChecksum of the file's ops, and the ledger keeps that instead.
type migEntry struct {
	name   string
	sql    string
	opsSum string
}

func (e migEntry) isOps() bool { return e.opsSum != "" }

// migSQLChecksum is the ledger checksum of hand-written SQL, and the checksum
// v0.28 recorded for every migration.
func migSQLChecksum(sql string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(sql)))
}

// migOpsChecksum is the ledger checksum of an op file: the sha256 of its ops
// in a canonical form. The ops are parsed as JSON (numbers kept as written),
// a `"v"` key on an op is dropped (an earlier v0.29 build of `--gen` wrote
// it), and the list is written back with sorted keys and no spaces. The form
// does not depend on the dialect or on the renderer, so a change to how an op
// renders never changes the checksum of a file that was already applied.
func migOpsChecksum(ops json.RawMessage) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(ops))
	dec.UseNumber()
	var list []any
	if err := dec.Decode(&list); err != nil {
		return "", err
	}
	for _, op := range list {
		if m, ok := op.(map[string]any); ok {
			delete(m, "v")
		}
	}
	canon, err := json.Marshal(list)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(append([]byte("sky-migration-ops:"), canon...))), nil
}

// migOpEntries parses a JSON array of {id, ops} migration files and renders
// each for the driver. Every entry carries its ops checksum.
func migOpEntries(driver, raw string) ([]migEntry, error) {
	var files []struct {
		Id  string          `json:"id"`
		Ops json.RawMessage `json:"ops"`
	}
	if err := json.Unmarshal([]byte(raw), &files); err != nil {
		return nil, fmt.Errorf("bad JSON: %v", err)
	}
	out := make([]migEntry, 0, len(files))
	for _, f := range files {
		ops := f.Ops
		if len(bytes.TrimSpace(ops)) == 0 || string(bytes.TrimSpace(ops)) == "null" {
			ops = json.RawMessage("[]")
		}
		var typed []migOp
		if err := json.Unmarshal(ops, &typed); err != nil {
			return nil, fmt.Errorf("migration %q: bad ops: %v", f.Id, err)
		}
		text, err := renderMigFile(driver, migFile{Id: f.Id, Ops: typed})
		if err != nil {
			return nil, err
		}
		sum, err := migOpsChecksum(ops)
		if err != nil {
			return nil, fmt.Errorf("migration %q: bad ops: %v", f.Id, err)
		}
		out = append(out, migEntry{name: f.Id, sql: text, opsSum: sum})
	}
	return out, nil
}

// renderMigFile renders one migration file to the SQL that applies it.
func renderMigFile(driver string, f migFile) (string, error) {
	body := make([]string, 0, len(f.Ops))
	for _, op := range f.Ops {
		s, err := renderMigOp(driver, op)
		if err != nil {
			return "", err
		}
		body = append(body, s)
	}
	return strings.Join(body, ";\n"), nil
}

var (
	migAddNoDefaultRe = regexp.MustCompile(
		`^ALTER TABLE ([A-Za-z_][A-Za-z0-9_]*) ADD COLUMN ([A-Za-z_][A-Za-z0-9_]*) [A-Za-z ]+ NOT NULL$`)
	migAddRequiredRe = regexp.MustCompile(
		`^ALTER TABLE ([A-Za-z_][A-Za-z0-9_]*) ADD COLUMN ([A-Za-z_][A-Za-z0-9_]*) .* NOT NULL DEFAULT `)
	migUniqueIndexRe = regexp.MustCompile(
		`^CREATE UNIQUE INDEX IF NOT EXISTS [A-Za-z_][A-Za-z0-9_]* ON ([A-Za-z_][A-Za-z0-9_]*) \(([^)]*)\)`)
)

// migRequiredUniqueHint explains an op-file migration that failed because it
// adds a required unique column to a table that already has 2 or more rows.
// `sky db migrate --gen` writes such a column as an addColumn op (NOT NULL
// DEFAULT <declared, else zero>) and then an addIndex op (UNIQUE). Every row
// that exists takes the same default, so the unique index fails, and the
// database error names an index, not the cause. The text is a pure function
// of the op JSON, so the hint reads the rendered statements: a column that
// this text adds NOT NULL and then puts in a UNIQUE index of the same table.
// dbMigrateCore calls it only for an op file, only when the error is a unique
// violation, and the hint needs the table to hold 2 or more rows after the
// rollback. It returns "" otherwise, and the caller reports the database
// error as it is.
func migRequiredUniqueHint(d *SkyDb, text string, execErr error) string {
	if execErr == nil || !strings.Contains(strings.ToLower(execErr.Error()), "unique") {
		return ""
	}
	added := map[string]bool{}
	for _, raw := range strings.Split(text, ";\n") {
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

// migRequiredNoZeroHint explains an op-file migration that failed because it
// adds a required column with no zero value (`NOT NULL` with no DEFAULT) to a
// table that has rows. Both dialects refuse that ALTER (SQLite: "Cannot add a
// NOT NULL column with default value NULL", PostgreSQL: "column … contains
// null values"), and neither error names the fix. dbMigrateCore calls it
// only for an op file; it reads the rendered statements and needs the table
// to hold a row after the rollback. It returns "" otherwise.
func migRequiredNoZeroHint(d *SkyDb, text string, execErr error) string {
	if execErr == nil || !strings.Contains(strings.ToLower(execErr.Error()), "null") {
		return ""
	}
	for _, raw := range strings.Split(text, ";\n") {
		m := migAddNoDefaultRe.FindStringSubmatch(strings.TrimSpace(raw))
		if m == nil || !codecValidIdent(m[1]) {
			continue
		}
		if n, err := migRowCountUpTo2(d, m[1]); err != nil || n < 1 {
			continue
		}
		return migNoZeroAdvice(m[1], m[2]) + " (the migration was rolled back). Database error: " + execErr.Error()
	}
	return ""
}

// migRowCountUpTo2 counts the rows of a table, stopping at 2.
func migRowCountUpTo2(d *SkyDb, table string) (int, error) {
	var n int
	err := d.conn.QueryRow("SELECT COUNT(*) FROM (SELECT 1 AS x FROM " + table + " LIMIT 2) sky_n").Scan(&n)
	return n, err
}

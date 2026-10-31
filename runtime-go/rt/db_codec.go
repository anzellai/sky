//go:build !js

// Package rt — codec-driven DB bridge for Std.Db.Store.
//
// A `Codec a` (Std.Codec) maps a record to/from a JSON object; this bridge turns
// that object into flat columns and back. Top-level scalar fields become typed
// columns; anything the codec marks as a blob (ADT / tuple / list / nested
// record) is stored as a JSON TEXT column. Decode is done Sky-side via
// `Codec.fromJson` — this file only reassembles each row into a JSON string.
//
// colspec = a Sky `List (String, String)` of (columnName, kind) where kind ∈
// {text,int,real,bool,blob} (the ColType, stringified by the Sky layer).
package rt

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// codecKindBase returns the base-kind portion of a colspec kind, before any
// `|`-delimited flag suffix (`unique`, `default`) that Store.unique / Store.default*
// append. Read/write paths only care about the base type + nullability.
func codecKindBase(kind string) string {
	if i := strings.IndexByte(kind, '|'); i >= 0 {
		return kind[:i]
	}
	return kind
}

// codecSplitKind splits a colspec kind into (baseKind, nullable). A trailing `?`
// marks a nullable column (a Maybe field); its absence means NOT NULL. A trailing
// `!` marks an auto-increment PK (see codecColIsAutoInc); any `|flag` suffix marks
// a UNIQUE / DEFAULT (see codecColExtras). All markers are stripped here so every
// read/write path sees the plain base kind.
func codecSplitKind(kind string) (string, bool) {
	kind = codecKindBase(kind)
	kind = strings.TrimSuffix(kind, "!")
	if strings.HasSuffix(kind, "?") {
		return kind[:len(kind)-1], true
	}
	return kind, false
}

// codecColIsAutoInc reports whether a colspec kind carries the `!` auto-increment
// marker that `Store.serial` stamps on the PK column.
func codecColIsAutoInc(kind string) bool {
	return strings.HasSuffix(codecKindBase(kind), "!")
}

// codecColIsTouch reports whether a colspec kind carries the `touch` flag that
// `Store.touchOnUpdate` stamps — such a column is set to the current timestamp on
// every UPDATE (an `updated_at`), not bound from the record value.
//
// Each flag is compared EXACTLY. A substring match made
// `Store.defaultText "status" "untouched"` a touch column, so every
// Store.update overwrote `status` with the clock.
func codecColIsTouch(kind string) bool {
	for _, f := range codecColFlags(kind) {
		if f == "touch" {
			return true
		}
	}
	return false
}

// codecColIsNow reports whether a colspec kind carries the `dnow` flag that
// `Store.defaultNow` and `Store.touchOnUpdate` stamp: the column's value is the
// clock at INSERT. Each flag is compared exactly.
func codecColIsNow(kind string) bool {
	for _, f := range codecColFlags(kind) {
		if f == "dnow" {
			return true
		}
	}
	return false
}

// codecNowValue is the value an INSERT binds for a `dnow` column. On SQLite
// it is the app clock in `datetime('now')` format: a column that `migrate`
// added holds a constant default (the clock at the ALTER), because SQLite
// refuses a non-constant default in ADD COLUMN, so new rows must not take
// it. On PostgreSQL the column is left out (ok is false) and the database
// default `now()` stamps it.
func codecNowValue(driver string) (any, bool) {
	if driver == "pgx" {
		return nil, false
	}
	return time.Now().UTC().Format(sqliteNowLayout), true
}

// codecColFlags returns the `|`-delimited flags after the base kind.
func codecColFlags(kind string) []string {
	i := strings.IndexByte(kind, '|')
	if i < 0 {
		return nil
	}
	return strings.Split(kind[i+1:], "|")
}

// codecFlagValueDecoder reverses the Sky-side encoding of a default value
// (Store.sky `flagValue`): `%` is sent as `%25` and `|` as `%7C`, so a value
// can hold either character without splitting into, or injecting, a flag.
var codecFlagValueDecoder = strings.NewReplacer("%7C", "|", "%25", "%")

// codecColExtras parses the `|`-delimited flag suffix Store.unique / Store.default*
// append onto a colspec kind, returning (unique, defaultKind, defaultVal) where
// defaultKind ∈ {none,now,text,int,bool,float} matches schemaDefault. A default
// value arrives percent-encoded (see codecFlagValueDecoder) and is decoded here.
// codecColZero reads the `zero=` flag Std.Db.Store adds to a required blob
// or enum column: the column's own field zero (Codec.fieldZeros), the JSON of
// a blob field's zero or an enum's zero name, as a text default for
// renderAddColumn. nil when there is no flag.
func codecColZero(kind string) *migDefault {
	for _, f := range codecColFlags(kind) {
		if strings.HasPrefix(f, "zero=") {
			v := codecFlagValueDecoder.Replace(f[len("zero="):])
			return &migDefault{Text: &v}
		}
	}
	return nil
}

func codecColExtras(kind string) (bool, string, string) {
	unique := false
	defKind, defVal := "none", ""
	i := strings.IndexByte(kind, '|')
	if i < 0 {
		return unique, defKind, defVal
	}
	for _, f := range strings.Split(kind[i+1:], "|") {
		switch {
		case f == "u":
			unique = true
		case f == "dnow":
			defKind = "now"
		case strings.HasPrefix(f, "dtext="):
			defKind, defVal = "text", codecFlagValueDecoder.Replace(f[len("dtext="):])
		case strings.HasPrefix(f, "dint="):
			defKind, defVal = "int", f[len("dint="):]
		case strings.HasPrefix(f, "dbool="):
			defKind, defVal = "bool", f[len("dbool="):]
		case strings.HasPrefix(f, "dfloat="):
			defKind, defVal = "float", f[len("dfloat="):]
		}
	}
	return unique, defKind, defVal
}

func codecColKindToSchema(kind string) string {
	kind, _ = codecSplitKind(kind)
	switch kind {
	case "int":
		return "bigint"
	case "real":
		return "real"
	case "bool":
		return "bool"
	default: // text, blob
		return "text"
	}
}

// schemaColMap builds the single per-column Schema-Table map that schemaRenderTable
// consumes. This is the ONE place a column's DDL flags are assembled, shared by BOTH
// the direct-create/push path (codecColspecSchemaMap) AND the committed-migration
// path (renderMigOp's createTable). Routing both through here is what keeps
// `sky db push` and `sky db migrate` byte-identical — they physically cannot diverge.
//
//   - A required (non-Maybe) column is NOT NULL on a FRESH table; the PK always is.
//     (A column added to an existing table goes through renderAddColumn, which
//     adds it NOT NULL DEFAULT <declared or zero> so existing rows read a value.)
//   - Auto-increment only applies to the PK (schemaRenderColumn renders the serial
//     token only when IsPk && IsAutoInc), so guard it with isPk here too.
func schemaColMap(name, codecKind string, isPk, nullable, unique, autoInc bool, defKind, defVal string) map[string]any {
	return map[string]any{
		"Name": name, "Kind": codecColKindToSchema(codecKind),
		"IsPk": isPk, "IsNotNull": !nullable || isPk, "IsUnique": unique,
		"IsAutoInc": autoInc && isPk, "DefaultKind": defKind, "DefaultVal": defVal,
		"ForeignKey": "",
	}
}

// codecColspecSchemaMap builds a Schema-Table map from a (name,kind) colspec.
// The PK column is NOT NULL; all others are nullable (the codec enforces types).
func codecColspecSchemaMap(table string, colspecArg any, pk string) map[string]any {
	cols := []any{}
	for _, cs := range AsList(colspecArg) {
		t := AsTuple2(cs)
		name := AsString(t.V0)
		rawKind := AsString(t.V1)
		_, nullable := codecSplitKind(rawKind)
		unique, defKind, defVal := codecColExtras(rawKind)
		cols = append(cols, schemaColMap(
			name, rawKind, name == pk, nullable, unique, codecColIsAutoInc(rawKind), defKind, defVal))
	}
	return map[string]any{"Name": table, "Columns": cols, "Indexes": []any{}}
}

// Db_createCols : Db -> String -> colspec -> String -> List IndexSpec -> Task Error ()
// renders + executes the CREATE TABLE derived from a colspec, then one CREATE
// INDEX per declared index (Store.index / uniqueOn / uniqueWhere).
func Db_createCols(connArg, tableArg, colspecArg, pkArg, indexesArg any) any {
	return func() any {
		d, ok := connArg.(*SkyDb)
		if !ok {
			return Err[any, any](ErrInvalidInput("Store.create: first argument is not a Db"))
		}
		table := AsString(tableArg)
		indexes, err := codecStoreIndexes(table, indexesArg)
		if err != nil {
			return Err[any, any](ErrInvalidInput("Store.create: " + err.Error()))
		}
		sm := codecColspecSchemaMap(table, colspecArg, AsString(pkArg))
		stmts := schemaRenderTable(d.driver, sm)
		for _, ix := range indexes {
			stmts = append(stmts, ix.ddl(table))
		}
		for _, stmt := range stmts {
			if _, err := d.executor().Exec(stmt); err != nil {
				return Err[any, any](ErrIo("Store.create: " + err.Error()))
			}
		}
		return Ok[any, any](struct{}{})
	}
}

// storeIndex is one declared Store index. A Store builder leaves the name
// empty and codecStoreIndexes derives it (codecIndexName). A Std.Db.Schema
// `withIndex` keeps its own name.
type storeIndex struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Unique  bool     `json:"unique,omitempty"`
	Where   string   `json:"where,omitempty"`
}

func (ix storeIndex) ddl(table string) string {
	return renderIndexDDL(table, ix.Name, ix.Columns, ix.Unique, ix.Where)
}

// codecStoreIndexes reads a Sky `List IndexSpec` ({ name, cols, unique,
// predicate }). Every column name is spliced into DDL, so each must be an
// identifier (B10), and so must the name.
func codecStoreIndexes(table string, indexesArg any) ([]storeIndex, error) {
	var out []storeIndex
	for _, raw := range AsList(indexesArg) {
		ix := storeIndex{
			Name:   codecStrField(raw, "Name"),
			Unique: schemaBool(fieldOrNil(raw, "Unique")),
			Where:  codecStrField(raw, "Predicate"),
		}
		for _, c := range AsList(fieldOrNil(raw, "Cols")) {
			ix.Columns = append(ix.Columns, AsString(c))
		}
		if len(ix.Columns) == 0 {
			return nil, fmt.Errorf("an index on %s has no columns", table)
		}
		for _, c := range ix.Columns {
			if !codecValidIdent(c) {
				return nil, fmt.Errorf("index on %s: invalid column identifier %q", table, c)
			}
		}
		if ix.Name == "" {
			ix.Name = codecIndexName(table, ix.Columns, ix.Unique, ix.Where)
		}
		if !codecValidIdent(ix.Name) {
			return nil, fmt.Errorf("invalid index name %q", ix.Name)
		}
		out = append(out, ix)
	}
	return out, nil
}

// codecStrField reads a String record field, "" when it is absent.
func codecStrField(record any, field string) string {
	v := fieldOrNil(record, field)
	if v == nil {
		return ""
	}
	return AsString(v)
}

// codecIndexExists looks an index up in the catalogue by table AND name, so
// an index of the same name on another table is not taken for it.
// PostgreSQL folds the unquoted DDL names to lower case.
func codecIndexExists(d *SkyDb, table, name string) (bool, error) {
	var q string
	var params []any
	if d.driver == "pgx" {
		q = "SELECT 1 AS x FROM pg_indexes WHERE schemaname = current_schema() AND tablename = ? AND indexname = ?"
		params = []any{strings.ToLower(table), strings.ToLower(name)}
	} else {
		q = "SELECT 1 AS x FROM sqlite_master WHERE type = 'index' AND tbl_name = ? AND name = ?"
		params = []any{table, name}
	}
	r, ok := AnyTaskRun(dbQueryRows(d, q, params)).(SkyResult[any, any])
	if !ok || r.Tag != 0 {
		return false, fmt.Errorf("index lookup failed")
	}
	return len(AsList(r.OkValue)) > 0, nil
}

// codecTableRowsUpTo2 counts a table's rows, stopping at 2: at 1 a required
// column with no zero value has a row with no value, and at 2 one default in a
// unique column must collide. The same thresholds explain a migration file
// (migRequiredNoZeroHint, migRequiredUniqueHint), so Store.migrate and
// `sky db migrate` agree.
func codecTableRowsUpTo2(d *SkyDb, table string) (int, error) {
	r, ok := AnyTaskRun(dbQueryRows(d, "SELECT 1 AS x FROM "+table+" LIMIT 2", []any{})).(SkyResult[any, any])
	if !ok || r.Tag != 0 {
		return 0, fmt.Errorf("row check failed")
	}
	return len(AsList(r.OkValue)), nil
}

// codecValidIdent guards interpolated identifiers (table/column names, which come
// from the codec's field names — but validate defensively).
func codecValidIdent(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

// codecTableColumns returns the set of existing column names for a table, or an
// empty set if the table doesn't exist.
func codecTableColumns(d *SkyDb, table string) (map[string]bool, error) {
	var sql string
	var params []any
	if d.driver == "pgx" {
		// The schema filter matters: without it a same-named table in another
		// schema on the search_path made migrate skip the CREATE and ALTER
		// that other table (B7). Names come back folded to lower case,
		// because the Store DDL is unquoted (B3), so the set is keyed folded.
		sql = "SELECT column_name FROM information_schema.columns WHERE table_name = ? AND table_schema = current_schema()"
		params = []any{strings.ToLower(table)}
	} else {
		sql = "PRAGMA table_info(" + table + ")" // PRAGMA takes no bind params
		params = []any{}
	}
	resp := AnyTaskRun(dbQueryRows(d, sql, params))
	r, ok := resp.(SkyResult[any, any])
	if !ok || r.Tag != 0 {
		return nil, fmt.Errorf("introspection failed")
	}
	out := map[string]bool{}
	for _, row := range AsList(r.OkValue) {
		m, ok := dbRowAsMap(row)
		if !ok {
			continue
		}
		// SQLite PRAGMA → "name"; Postgres information_schema → "column_name".
		if v, present := m["name"]; present {
			out[dbRawToString(v)] = true
		} else if v, present := m["column_name"]; present {
			out[strings.ToLower(dbRawToString(v))] = true
		}
	}
	return out, nil
}

// codecHasColumn looks a codec column name up in the set codecTableColumns
// returned. On PostgreSQL an unquoted name is folded to lower case, so the
// lookup folds too (B3): an autoCamel `priceMinor` exists as `priceminor`.
func codecHasColumn(driver string, current map[string]bool, name string) bool {
	if driver == "pgx" {
		return current[strings.ToLower(name)]
	}
	return current[name]
}

// codecRowValue reads a column from a row map. PostgreSQL returns an unquoted
// column folded to lower case, so on pgx a missing key is retried folded (B3).
// An output alias is quoted by Store, so it arrives with its case and the
// first lookup finds it.
func codecRowValue(driver string, m map[string]any, name string) (any, bool) {
	v, ok := m[name]
	if !ok && driver == "pgx" {
		v, ok = m[strings.ToLower(name)]
	}
	return v, ok
}

// Db_dialect : Db -> String. "postgres" for a pgx handle, else "sqlite".
// Std.Db.Store reads it once per verb to render dialect-correct SQL.
func Db_dialect(connArg any) any {
	if d, ok := connArg.(*SkyDb); ok && d.driver == "pgx" {
		return "postgres"
	}
	return "sqlite"
}

// Db_autoMigrate : Db -> String -> colspec -> String -> List IndexSpec
//
//	-> Task Error (List String)
//
// SAFE additive migration: creates the table if absent, ADDs any column the
// type gained, and creates any declared index that is missing. A column is
// added by renderAddColumn, the same renderer `sky db migrate` uses for a v2
// addColumn op: a Maybe field is nullable, any other field is NOT NULL
// DEFAULT <declared default, else the zero of its kind> (user decision D1).
// An existing column is never altered, and nothing is dropped, renamed or
// retyped. Returns the statements it ran (empty when up to date).
func Db_autoMigrate(connArg, tableArg, colspecArg, pkArg, indexesArg any) any {
	return func() any {
		d, ok := connArg.(*SkyDb)
		if !ok {
			return Err[any, any](ErrInvalidInput("Store.migrate: first argument is not a Db"))
		}
		table := AsString(tableArg)
		if !codecValidIdent(table) {
			return Err[any, any](ErrInvalidInput("Store.migrate: invalid table name"))
		}
		indexes, err := codecStoreIndexes(table, indexesArg)
		if err != nil {
			return Err[any, any](ErrInvalidInput("Store.migrate: " + err.Error()))
		}
		current, err := codecTableColumns(d, table)
		if err != nil {
			return Err[any, any](ErrIo("Store.migrate: " + err.Error()))
		}
		applied := []any{}
		run := func(stmt string) error {
			if _, e := d.executor().Exec(stmt); e != nil {
				return e
			}
			applied = append(applied, stmt)
			return nil
		}
		if len(current) == 0 {
			// table absent → create it, with its indexes
			stmts := schemaRenderTable(d.driver, codecColspecSchemaMap(table, colspecArg, AsString(pkArg)))
			for _, ix := range indexes {
				stmts = append(stmts, ix.ddl(table))
			}
			for _, stmt := range stmts {
				if e := run(stmt); e != nil {
					return Err[any, any](ErrIo("Store.migrate: " + e.Error()))
				}
			}
			return Ok[any, any](applied)
		}
		// existing → add any missing columns
		pk := AsString(pkArg)
		for _, cs := range AsList(colspecArg) {
			t := AsTuple2(cs)
			name := AsString(t.V0)
			if !codecValidIdent(name) || codecHasColumn(d.driver, current, name) {
				continue
			}
			mc := codecColspecMigColumn(name, AsString(t.V1), pk)
			rows := 0
			if migNeedsRowCount(mc) {
				if rows, err = codecTableRowsUpTo2(d, table); err != nil {
					return Err[any, any](ErrIo("Store.migrate: " + err.Error()))
				}
			}
			stmts, err := renderAddColumn(d.driver, table, mc, sqliteNowLiteral(), rows)
			if err != nil {
				return Err[any, any](ErrInvalidInput("Store.migrate: " + err.Error()))
			}
			for _, stmt := range stmts {
				if e := run(stmt); e != nil {
					return Err[any, any](ErrIo("Store.migrate: " + e.Error()))
				}
			}
		}
		// declared indexes that are missing
		for _, ix := range indexes {
			exists, err := codecIndexExists(d, table, ix.Name)
			if err != nil {
				return Err[any, any](ErrIo("Store.migrate: " + err.Error()))
			}
			if exists {
				continue
			}
			if e := run(ix.ddl(table)); e != nil {
				return Err[any, any](ErrIo("Store.migrate: " + e.Error()))
			}
		}
		return Ok[any, any](applied)
	}
}

// jsonObjFields unwraps a JSON object Value into a field map.
func jsonObjFields(v any) (map[string]any, bool) {
	jv, ok := v.(JsonValue)
	if !ok {
		return nil, false
	}
	obj, ok := jv.raw.(jsonOrderedObject)
	if !ok {
		return nil, false
	}
	m := map[string]any{}
	for i, k := range obj.keys {
		if i < len(obj.vals) {
			m[k] = obj.vals[i]
		}
	}
	return m, true
}

// rawToSqlArg converts a JSON raw value (a codec's encoding of one field) to
// a driver arg per column kind. The value must be of the kind: an int column
// takes an integral number, a real column a number, a bool column a bool, a
// text column a string, and a JSON (blob) column any JSON value. Anything
// else is an error naming the column, never a converted value: this used to
// write 0 for a non-number into an int column, false for a non-bool, and the
// Go `%v` text of a non-string into a text column, all with no error. A
// codec's encoder writes the kind its shape declares, so the error means a
// codec whose encoder and shape disagree.
func rawToSqlArg(raw any, kind, col string) (any, error) {
	if raw == nil {
		return nil, nil
	}
	refuse := func() (any, error) {
		return nil, fmt.Errorf("column %q is %s, and the codec encoded %s, which is not a value of that kind",
			col, kind, codecShortJSON(raw))
	}
	switch kind {
	case "blob":
		b, err := json.Marshal(raw)
		if err != nil {
			return nil, fmt.Errorf("column %q is JSON, and the value does not encode as JSON: %v", col, err)
		}
		return string(b), nil
	case "int":
		switch n := raw.(type) {
		case int:
			return int64(n), nil
		case int64:
			return n, nil
		}
		if n, err := jsonDecodeInt(raw); err == nil {
			return n, nil
		}
		return refuse()
	case "real":
		switch n := raw.(type) {
		case int:
			return float64(n), nil
		case int64:
			return float64(n), nil
		}
		if f, ok := jsonDecodeFloat(raw); ok {
			return f, nil
		}
		return refuse()
	case "bool":
		if b, ok := raw.(bool); ok {
			return b, nil
		}
		return refuse()
	default: // text
		if s, ok := raw.(string); ok {
			return s, nil
		}
		return refuse()
	}
}

// codecShortJSON is a value for an error message: its JSON, cut at 40 bytes.
func codecShortJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%T", v)
	}
	return codecShortText(string(b))
}

func codecShortText(s string) string {
	if len(s) > 40 {
		return s[:40] + "…"
	}
	return s
}

// codecRecordArgs is rawToSqlArg over the named columns of one record, with
// the verb that reports an error.
func codecRecordArg(verb string, fields map[string]any, name, base string) (any, error) {
	v, err := rawToSqlArg(fields[name], base, name)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", verb, err)
	}
	return v, nil
}

// storeWriteResult runs an insert-shaped statement and produces the Int the
// Store verb returns.
//
// The verbs are typed `Task Error Int`, and for a store whose primary key the
// DATABASE assigns (`Store.serial`, or any integer PK) the only useful integer
// is that assigned key — the caller cannot obtain it any other way, because the
// PK is `generated` and therefore omitted from the INSERT. These kernels used
// to end in `AnyTaskRun(Db_exec(...))`, which returns RowsAffected: always 1
// for a single-row insert. Callers wiring child rows to it attributed every one
// of them to id 1.
//
// Precedence is decided by the PK's declared kind, not by guessing:
//
//   - integer PK  → `RETURNING <pk>`, and the assigned key is returned.
//   - other PK    → the caller already supplied the key, so there is no
//     DB-assigned integer to report and the verb keeps returning rows
//     affected. Fabricating a number here would be the same class of silent
//     wrong value this fix exists to remove.
//
// `RETURNING` is used on BOTH dialects rather than `LastInsertId` on SQLite:
// SQLite only updates `last_insert_rowid()` on a real INSERT, so an upsert that
// took the `DO UPDATE` branch would report a stale rowid. RETURNING has no such
// hole and is already the repo-wide floor (SQLite >= 3.35 / PostgreSQL), per
// `Db_insertFieldsReturning`.
//
// The statement is passed through `d.rebind` — it is composed with literal `?`
// (see the placeholder loops below), and Postgres rejects those.
func storeWriteResult(d *SkyDb, verb, sqlText string, params []any, pk, pkKind string) any {
	// SSR settle guard: Store.insert/upsert-returning reach the driver HERE, not
	// through Db_exec, so the destructive-kernel suppressor must be repeated on
	// this path — otherwise a `Store.insert` batched into an onNavigate/init
	// command mutates the database during a GET server-side render (Store is the
	// pinned default write path). See spa_ssr_safe.go.
	if r := ssrSuppressedWrite("store.write:" + verb); r != nil {
		return r
	}
	// These kernels used to reach the driver via Db_exec, which binds every
	// argument through dbBindArg — that is what turns a `Store.defaultWith`
	// SqlValue ADT into a driver-friendly Go value. Bypassing Db_exec means
	// doing it here; dropping it would silently break UUID/Money defaults.
	bound := make([]any, len(params))
	for i, p := range params {
		bound[i] = dbBindArg(p)
	}
	params = bound

	base, _ := codecSplitKind(pkKind)
	if base != "int" || pk == "" {
		res, err := d.executor().Exec(d.rebind(sqlText), params...)
		if err != nil {
			return Err[any, any](ErrIo(verb + ": " + err.Error()))
		}
		n, _ := res.RowsAffected()
		return Ok[any, any](int(n))
	}
	rows, err := d.executor().Query(d.rebind(sqlText+" RETURNING "+pk), params...)
	if err != nil {
		return Err[any, any](ErrIo(verb + ": " + err.Error()))
	}
	defer rows.Close()
	if !rows.Next() {
		// No row came back — an `ON CONFLICT ... DO NOTHING` that did nothing.
		if err := rows.Err(); err != nil {
			return Err[any, any](ErrIo(verb + ": " + err.Error()))
		}
		return Ok[any, any](0)
	}
	var id any
	if err := rows.Scan(&id); err != nil {
		return Err[any, any](ErrIo(verb + " id: " + err.Error()))
	}
	if err := rows.Err(); err != nil {
		return Err[any, any](ErrIo(verb + ": " + err.Error()))
	}
	return Ok[any, any](AsIntOrZero(normaliseSqlValue(id)))
}

// Db_execObject splits a record's JSON object into columns and INSERTs it.
// `pkArg` / `pkKindArg` let it return the DB-assigned key — see
// storeWriteResult.
func Db_execObject(connArg, tableArg, colspecArg, objArg, pkArg, pkKindArg any) any {
	return func() any {
		d, isDb := connArg.(*SkyDb)
		if !isDb {
			return Err[any, any](ErrInvalidInput("Store.insert: first argument is not a Db"))
		}
		fields, ok := jsonObjFields(objArg)
		if !ok {
			return Err[any, any](ErrInvalidInput("Store.insert: value is not a record"))
		}
		cols := []string{}
		params := []any{}
		for _, cs := range AsList(colspecArg) {
			t := AsTuple2(cs)
			name := AsString(t.V0)
			base, _ := codecSplitKind(AsString(t.V1))
			arg, err := codecRecordArg("Store.insert", fields, name, base)
			if err != nil {
				return Err[any, any](ErrInvalidInput(err.Error()))
			}
			cols = append(cols, name)
			params = append(params, arg)
		}
		ph := codecParams(d.driver, AsString(tableArg), colspecArg, cols)
		sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
			AsString(tableArg), strings.Join(cols, ", "), strings.Join(ph, ", "))
		return storeWriteResult(d, "Store.insert", sql, params,
			AsString(pkArg), AsString(pkKindArg))
	}
}

// Db_execObjectWith is Db_execObject plus a list of app-computed (column,
// SqlValue) pairs (Store.defaultWith) appended to the INSERT — the SqlValue
// params bind through the same SqlValue path, so a UUID PK generated in Sky
// lands in the row without the record carrying it.
func Db_execObjectWith(connArg, tableArg, colspecArg, objArg, extraArg, pkArg, pkKindArg any) any {
	return func() any {
		d, isDb := connArg.(*SkyDb)
		if !isDb {
			return Err[any, any](ErrInvalidInput("Store.insert: first argument is not a Db"))
		}
		fields, ok := jsonObjFields(objArg)
		if !ok {
			return Err[any, any](ErrInvalidInput("Store.insert: value is not a record"))
		}
		cols, params, err := codecInsertColumns("Store.insert", d.driver, colspecArg, fields)
		if err != nil {
			return Err[any, any](ErrInvalidInput(err.Error()))
		}
		for _, ex := range AsList(extraArg) {
			t := AsTuple2(ex)
			cols = append(cols, AsString(t.V0))
			params = append(params, t.V1) // a Sky SqlValue ADT — Db_exec binds it
		}
		if len(cols) == 0 {
			return Ok[any, any](0)
		}
		ph := codecParams(d.driver, AsString(tableArg), colspecArg, cols)
		sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
			AsString(tableArg), strings.Join(cols, ", "), strings.Join(ph, ", "))
		return storeWriteResult(d, "Store.insert", sql, params,
			AsString(pkArg), AsString(pkKindArg))
	}
}

// Db_upsertObject inserts a record, or updates it in place on a primary-key
// conflict — `Store.upsert`. Uses `INSERT ... ON CONFLICT(pk) DO UPDATE SET
// col = excluded.col` (SQLite ≥ 3.24 / Postgres), updating every non-PK column
// to the incoming value. If the PK is the only column, DO NOTHING.
//
// Shares Store.insert's return contract via storeWriteResult: an integer PK
// yields the row's key, anything else yields rows affected. A DO NOTHING that
// did nothing returns 0.
//
// extraArg is the `defaultWith` columns, ( column, SqlValue ) pairs computed
// in Sky: they are INSERT columns, as for Db_execObjectWith, and an update
// keeps the value the row holds (a computed value is a default, not a field).
func Db_upsertObject(connArg, tableArg, colspecArg, pkArg, objArg, pkKindArg, extraArg any) any {
	return func() any {
		d, isDb := connArg.(*SkyDb)
		if !isDb {
			return Err[any, any](ErrInvalidInput("Store.upsert: first argument is not a Db"))
		}
		fields, ok := jsonObjFields(objArg)
		if !ok {
			return Err[any, any](ErrInvalidInput("Store.upsert: value is not a record"))
		}
		pk := AsString(pkArg)
		cols, params, err := codecInsertColumns("Store.upsert", d.driver, colspecArg, fields)
		if err != nil {
			return Err[any, any](ErrInvalidInput(err.Error()))
		}
		for _, ex := range AsList(extraArg) {
			t := AsTuple2(ex)
			cols = append(cols, AsString(t.V0))
			params = append(params, t.V1) // a Sky SqlValue ADT — Db_exec binds it
		}
		// A stamped `dnow` column is an INSERT column only: an update keeps
		// the stamp the row was created with.
		setParts := []string{}
		for _, cs := range AsList(colspecArg) {
			t := AsTuple2(cs)
			name := AsString(t.V0)
			if name != pk && !codecColIsNow(AsString(t.V1)) {
				setParts = append(setParts, name+" = excluded."+name)
			}
		}
		ph := codecParams(d.driver, AsString(tableArg), colspecArg, cols)
		var conflict string
		if len(setParts) == 0 {
			conflict = fmt.Sprintf("ON CONFLICT(%s) DO NOTHING", pk)
		} else {
			conflict = fmt.Sprintf("ON CONFLICT(%s) DO UPDATE SET %s", pk, strings.Join(setParts, ", "))
		}
		sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) %s",
			AsString(tableArg), strings.Join(cols, ", "), strings.Join(ph, ", "), conflict)
		return storeWriteResult(d, "Store.upsert", sql, params,
			AsString(pkArg), AsString(pkKindArg))
	}
}

// Db_execObjectMany bulk-inserts many records in ONE multi-row INSERT (for
// time-series / batch writes) — `Store.insertMany`. All rows share the colspec;
// each row's values bind positionally. Empty list is a no-op. extrasArg holds
// one list of `defaultWith` ( column, SqlValue ) pairs per record, computed in
// Sky for that row, appended as Db_execObjectWith appends them.
func Db_execObjectMany(connArg, tableArg, colspecArg, objsArg, extrasArg any) any {
	return func() any {
		objs := AsList(objsArg)
		if len(objs) == 0 {
			return Ok[any, any](0)
		}
		extras := AsList(extrasArg)
		if len(extras) != len(objs) {
			return Err[any, any](ErrInvalidInput("Store.insertMany: one defaultWith list per record is needed"))
		}
		driver := dbDriverOf(connArg)
		var cols []string
		rowPh := make([]string, 0, len(objs))
		params := []any{}
		for i, obj := range objs {
			fields, ok := jsonObjFields(obj)
			if !ok {
				return Err[any, any](ErrInvalidInput("Store.insertMany: a value is not a record"))
			}
			rowCols, rowParams, err := codecInsertColumns("Store.insertMany", driver, colspecArg, fields)
			if err != nil {
				return Err[any, any](ErrInvalidInput(err.Error()))
			}
			for _, ex := range AsList(extras[i]) {
				t := AsTuple2(ex)
				rowCols = append(rowCols, AsString(t.V0))
				rowParams = append(rowParams, t.V1)
			}
			cols = rowCols
			rowPh = append(rowPh, "("+strings.Join(codecParams(driver, AsString(tableArg), colspecArg, rowCols), ", ")+")")
			params = append(params, rowParams...)
		}
		if len(cols) == 0 {
			return Ok[any, any](0)
		}
		sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES %s",
			AsString(tableArg), strings.Join(cols, ", "), strings.Join(rowPh, ", "))
		return AnyTaskRun(Db_exec(connArg, sql, params))
	}
}

// codecInsertColumns lists the INSERT columns and values of one record. A
// `dnow` column (Store.defaultNow / touchOnUpdate) takes the app clock on
// SQLite and is left out on PostgreSQL (see codecNowValue). Every other
// column binds the record's value.
func codecInsertColumns(verb, driver string, colspecArg any, fields map[string]any) ([]string, []any, error) {
	cols := []string{}
	params := []any{}
	for _, cs := range AsList(colspecArg) {
		t := AsTuple2(cs)
		name := AsString(t.V0)
		rawKind := AsString(t.V1)
		if codecColIsNow(rawKind) {
			if v, ok := codecNowValue(driver); ok {
				cols = append(cols, name)
				params = append(params, v)
			}
			continue
		}
		base, _ := codecSplitKind(rawKind)
		arg, err := codecRecordArg(verb, fields, name, base)
		if err != nil {
			return nil, nil, err
		}
		cols = append(cols, name)
		params = append(params, arg)
	}
	return cols, params, nil
}

// codecRowIsNull reports whether a row value is SQL NULL (or absent).
func codecRowIsNull(raw any, present bool) bool {
	if !present || raw == nil {
		return true
	}
	tag, _ := anyMaybeView(raw) // NULL comes back as a Sky Nothing
	return tag == 1
}

// rowValToJsonRaw converts a DB row value to a JSON raw value per column kind.
// SQL NULL is JSON null. A value that does not read as the kind is an error
// naming the column (`col`, the result name: Std.Db.Store puts the table in
// front of it), never JSON null: a `Codec.maybe` decoder reads null as
// Nothing, so a stored value it could not parse used to read back as Nothing
// with no error. A column missing from the result row is an error too (a
// `selectRaw` query must return every codec column).
//
// What each kind reads, including the shapes earlier Sky versions wrote:
//
//   - int: an integer, or an integral number in any float form ("5.0",
//     "1e+06"), which is how an int written as text by a v0.28 nested
//     `Maybe (Maybe Int)` column, or an integral float, reads back;
//   - real: any number (an integer included);
//   - bool: true/false (PostgreSQL), 1/0 (SQLite), and t/f, TRUE/FALSE;
//   - blob: valid JSON;
//   - text: any value, as its text.
func rowValToJsonRaw(raw any, present bool, kind, col string) (any, error) {
	if !present {
		return nil, fmt.Errorf(
			"Store: column %q is not in the query result: every codec column is read by name "+
				"(in selectRaw, select it, or write NULL AS %s for a Maybe field)", col, col)
	}
	if codecRowIsNull(raw, present) {
		return nil, nil
	}
	s := dbRawToString(raw)
	switch kind {
	case "int":
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n, nil
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			if n, err := floatToStrictInt(f); err == nil {
				return n, nil
			}
		}
	case "real":
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f, nil
		}
	case "bool":
		switch s {
		case "1", "true", "TRUE", "True", "t", "T":
			return true, nil
		case "0", "false", "FALSE", "False", "f", "F":
			return false, nil
		}
	case "blob":
		var v any
		if json.Unmarshal([]byte(s), &v) == nil {
			return v, nil
		}
	default: // text
		return s, nil
	}
	return nil, fmt.Errorf("Store: column %q is %s, and the database holds %q, which does not read as %s",
		col, codecKindNoun(kind), codecShortText(s), codecKindNoun(kind))
}

// codecKindNoun names a column kind in an error message.
func codecKindNoun(kind string) string {
	switch kind {
	case "int":
		return "an Int"
	case "real":
		return "a Float"
	case "bool":
		return "a Bool"
	case "blob":
		return "JSON"
	}
	return "text"
}

// Db_queryObjects runs a query and reassembles each row into a JSON string
// (per the colspec) for Sky-side decode via Codec.fromJson.
func Db_queryObjects(connArg, sqlArg, paramsArg, colspecArg any) any {
	return func() any {
		resp := AnyTaskRun(dbQueryRows(connArg, sqlArg, paramsArg))
		r, ok := resp.(SkyResult[any, any])
		if !ok || r.Tag != 0 {
			return resp
		}
		driver := dbDriverOf(connArg)
		out := []any{}
		for _, row := range AsList(r.OkValue) {
			m, ok := dbRowAsMap(row)
			if !ok {
				return Err[any, any](ErrDecode("Store: row is not a Dict"))
			}
			obj := jsonOrderedObject{}
			for _, cs := range AsList(colspecArg) {
				t := AsTuple2(cs)
				name := AsString(t.V0)
				base, _ := codecSplitKind(AsString(t.V1))
				raw, present := codecRowValue(driver, m, name)
				val, err := rowValToJsonRaw(raw, present, base, name)
				if err != nil {
					return Err[any, any](ErrDecode(err.Error()))
				}
				obj.keys = append(obj.keys, name)
				obj.vals = append(obj.vals, val)
			}
			b, err := json.Marshal(obj)
			if err != nil {
				return Err[any, any](ErrDecode("Store: marshal: " + err.Error()))
			}
			out = append(out, string(b))
		}
		return Ok[any, any](out)
	}
}

// Db_queryObjectGroups : Db -> String -> List SqlValue
//
//	-> List ( String, String, List ( String, String ) ) -> Task Error (List (List String))
//
// The Std.Db.Store read kernel (v0.29.0). It runs a query and turns each row
// into one JSON object per GROUP. A group is ( prefix, marker, colspec ): the
// object holds every colspec column, read from the result column
// `prefix ++ name` and keyed by `name` (the prefix is stripped). A plain query
// has one group with an empty prefix. A join has two, `a__` and `b__`, so two
// tables with the same column names never clash. The second element is
// `marker|table`: the table names a column the kernel cannot read in its
// error (`orders.qty`). When `marker` is not empty and
// the result column `prefix ++ marker` is NULL, the group is the JSON text
// `null`: that is the missing right side of a LEFT JOIN (the marker is a right
// ON column, and an equality never matches NULL).
func Db_queryObjectGroups(connArg, sqlArg, paramsArg, groupsArg any) any {
	return func() any {
		resp := AnyTaskRun(dbQueryRows(connArg, sqlArg, paramsArg))
		r, ok := resp.(SkyResult[any, any])
		if !ok || r.Tag != 0 {
			return resp
		}
		driver := dbDriverOf(connArg)
		type group struct {
			prefix, marker, table string
			names, kinds          []string
		}
		var groups []group
		for _, g := range AsList(groupsArg) {
			t := AsTuple3(g)
			// The marker slot is `marker|table` (Std.Db.Store's markedTable):
			// an identifier holds no `|`. A bare marker names no table.
			marker, table, _ := strings.Cut(AsString(t.V1), "|")
			gr := group{prefix: AsString(t.V0), marker: marker, table: table}
			for _, cs := range AsList(t.V2) {
				c := AsTuple2(cs)
				base, _ := codecSplitKind(AsString(c.V1))
				gr.names = append(gr.names, AsString(c.V0))
				gr.kinds = append(gr.kinds, base)
			}
			groups = append(groups, gr)
		}
		out := []any{}
		for _, row := range AsList(r.OkValue) {
			m, ok := dbRowAsMap(row)
			if !ok {
				return Err[any, any](ErrDecode("Store: row is not a Dict"))
			}
			texts := make([]any, 0, len(groups))
			for _, g := range groups {
				if g.marker != "" {
					raw, present := codecRowValue(driver, m, g.prefix+g.marker)
					if codecRowIsNull(raw, present) {
						texts = append(texts, "null")
						continue
					}
				}
				obj := jsonOrderedObject{}
				for i, name := range g.names {
					raw, present := codecRowValue(driver, m, g.prefix+name)
					col := g.prefix + name
					if g.table != "" {
						col = g.table + "." + name
					}
					val, err := rowValToJsonRaw(raw, present, g.kinds[i], col)
					if err != nil {
						return Err[any, any](ErrDecode(err.Error()))
					}
					obj.keys = append(obj.keys, name)
					obj.vals = append(obj.vals, val)
				}
				b, err := json.Marshal(obj)
				if err != nil {
					return Err[any, any](ErrDecode("Store: marshal: " + err.Error()))
				}
				texts = append(texts, string(b))
			}
			out = append(out, texts)
		}
		return Ok[any, any](out)
	}
}

// jcol is one column of the schema dump (`db/schema.json`).
type jcol struct {
	Name     string      `json:"name"`
	Kind     string      `json:"kind"`
	Nullable bool        `json:"nullable"`
	Autoinc  bool        `json:"autoinc,omitempty"`
	Unique   bool        `json:"unique,omitempty"`
	Default  *migDefault `json:"default,omitempty"`
	// Zero is a required blob or enum column's own field zero
	// (migColumn.Zero). `--gen` makes it the DEFAULT of the addColumn op.
	Zero *migDefault `json:"zero,omitempty"`
}

// codecDumpColumn is the schema-dump form of one colspec entry.
func codecDumpColumn(name, rawKind string) jcol {
	mc := codecColspecMigColumn(name, rawKind, "")
	return jcol{
		Name: mc.Name, Kind: mc.Type, Nullable: mc.Nullable,
		Autoinc: mc.Autoinc, Unique: mc.Unique, Default: mc.Default, Zero: mc.Zero,
	}
}

// Db_dumpProject prints a project's schema as JSON between markers (SKY_SCHEMA_*)
// so `sky db migrate --gen` can capture the target schema WITHOUT a live DB —
// this is the pure schema-dump the file-based migration flow diffs against.
func Db_dumpProject(tablesArg any) any {
	return func() any {
		// A column's default is a migDefault, the shape renderMigOp reads, so a
		// captured DEFAULT round-trips dump → schema.json → op unchanged. The
		// column is read by codecColspecMigColumn, the same reading
		// Db_autoMigrate uses (one migrate rule, D1).
		type jtable struct {
			Name    string       `json:"name"`
			Pk      string       `json:"pk"`
			Columns []jcol       `json:"columns"`
			Indexes []storeIndex `json:"indexes,omitempty"`
		}
		out := struct {
			Tables []jtable `json:"tables"`
		}{}
		for _, t := range AsList(tablesArg) {
			jt := jtable{
				Name: fmt.Sprintf("%v", fieldOrNil(t, "Name")),
				Pk:   fmt.Sprintf("%v", fieldOrNil(t, "Pk")),
			}
			for _, c := range AsList(fieldOrNil(t, "Cols")) {
				tup := AsTuple2(c)
				jt.Columns = append(jt.Columns, codecDumpColumn(AsString(tup.V0), AsString(tup.V1)))
			}
			ixs, err := codecStoreIndexes(jt.Name, fieldOrNil(t, "Indexes"))
			if err != nil {
				return Err[any, any](ErrInvalidInput("dumpProject: " + err.Error()))
			}
			jt.Indexes = ixs
			out.Tables = append(out.Tables, jt)
		}
		b, err := json.Marshal(out)
		if err != nil {
			return Err[any, any](ErrIo("dumpProject: " + err.Error()))
		}
		fmt.Println("SKY_SCHEMA_BEGIN")
		fmt.Println(string(b))
		fmt.Println("SKY_SCHEMA_END")
		return Ok[any, any](struct{}{})
	}
}

// Db_updateByPk updates one row by primary key: SET every column in setColspec to
// the record's value, WHERE pkCol = the record's pk value. Backs Store.update.
func Db_updateByPk(connArg, tableArg, setColspecArg, pkColArg, pkKindArg, objArg any) any {
	return func() any {
		fields, ok := jsonObjFields(objArg)
		if !ok {
			return Err[any, any](ErrInvalidInput("Store.update: value is not a record"))
		}
		setCols, params, err := codecSetClause("Store.update", dbDriverOf(connArg), AsString(tableArg), setColspecArg, fields)
		if err != nil {
			return Err[any, any](ErrInvalidInput(err.Error()))
		}
		if len(setCols) == 0 {
			return Ok[any, any](0)
		}
		pkCol := AsString(pkColArg)
		pkBase, _ := codecSplitKind(AsString(pkKindArg))
		pkArg, err := codecRecordArg("Store.update", fields, pkCol, pkBase)
		if err != nil {
			return Err[any, any](ErrInvalidInput(err.Error()))
		}
		params = append(params, pkArg)
		sql := fmt.Sprintf("UPDATE %s SET %s WHERE %s = ?",
			AsString(tableArg), strings.Join(setCols, ", "), pkCol)
		return AnyTaskRun(Db_exec(connArg, sql, params))
	}
}

// Db_updateWhere updates rows matching a pre-rendered WHERE clause: SET every
// column in setColspec to the record's value, WHERE <whereSql> (whereParams are
// SqlValues, bound by Db_exec's SqlValue path). Backs Store.updateWhere.
func Db_updateWhere(connArg, tableArg, setColspecArg, objArg, whereSqlArg, whereParamsArg any) any {
	return func() any {
		fields, ok := jsonObjFields(objArg)
		if !ok {
			return Err[any, any](ErrInvalidInput("Store.updateWhere: value is not a record"))
		}
		setCols, params, err := codecSetClause("Store.updateWhere", dbDriverOf(connArg), AsString(tableArg), setColspecArg, fields)
		if err != nil {
			return Err[any, any](ErrInvalidInput(err.Error()))
		}
		if len(setCols) == 0 {
			return Ok[any, any](0)
		}
		for _, p := range AsList(whereParamsArg) {
			params = append(params, p)
		}
		sql := fmt.Sprintf("UPDATE %s SET %s WHERE %s",
			AsString(tableArg), strings.Join(setCols, ", "), AsString(whereSqlArg))
		return AnyTaskRun(Db_exec(connArg, sql, params))
	}
}

// dbDriverOf returns the driver ("pgx" / "sqlite") for a Db conn arg, or "" if the
// arg isn't a *SkyDb.
func dbDriverOf(connArg any) string {
	if d, ok := connArg.(*SkyDb); ok {
		return d.driver
	}
	return ""
}

// codecSetClause builds the SET fragments + bound values for a set-column spec,
// pulling each value from the codec-encoded record fields. A `touch`-flagged
// column (Store.touchOnUpdate) is set to the current timestamp via a SQL
// expression (no bound param), so `updated_at` bumps on every UPDATE.
func codecSetClause(verb, driver, table string, setColspecArg any, fields map[string]any) ([]string, []any, error) {
	setCols := []string{}
	params := []any{}
	for _, cs := range AsList(setColspecArg) {
		t := AsTuple2(cs)
		name := AsString(t.V0)
		rawKind := AsString(t.V1)
		if codecColIsTouch(rawKind) {
			setCols = append(setCols, name+" = "+schemaDefault(driver, "now", ""))
			continue
		}
		base, _ := codecSplitKind(rawKind)
		arg, err := codecRecordArg(verb, fields, name, base)
		if err != nil {
			return nil, nil, err
		}
		setCols = append(setCols, name+" = "+codecParam(driver, table, name, rawKind))
		params = append(params, arg)
	}
	return setCols, params, nil
}

// Db_sqlOfValue converts a JSON Encode Value (produced by a codec's encoder) into
// the matching scalar SqlValue — backs Std.Db.Store.sqlOf, so a query builder can
// filter by a TYPED value (enum / Money / Time / a Codec.map wrapper) using the
// SAME encoding the column stores, with no hand-wrapping in SqlString/SqlInt/….
// A JSON object/array (a blob column) becomes its JSON TEXT (SqlString).
func Db_sqlOfValue(valueArg any) any {
	jv, ok := valueArg.(JsonValue)
	if !ok {
		return SkyADT{Tag: 0, SkyName: "SqlString", Fields: []any{fmt.Sprintf("%v", valueArg)}}
	}
	switch r := jv.raw.(type) {
	case string:
		return SkyADT{Tag: 0, SkyName: "SqlString", Fields: []any{r}}
	case int:
		return SkyADT{Tag: 1, SkyName: "SqlInt", Fields: []any{r}}
	case int64:
		return SkyADT{Tag: 1, SkyName: "SqlInt", Fields: []any{int(r)}}
	case float64:
		return SkyADT{Tag: 2, SkyName: "SqlFloat", Fields: []any{r}}
	case bool:
		return SkyADT{Tag: 3, SkyName: "SqlBool", Fields: []any{r}}
	case nil:
		return SkyADT{Tag: 8, SkyName: "SqlNull", Fields: []any{
			SkyADT{Tag: 0, SkyName: "SqlString", Fields: []any{""}},
		}}
	default:
		return SkyADT{Tag: 0, SkyName: "SqlString", Fields: []any{AsString(JsonEnc_encode(0, jv))}}
	}
}

// Db_objectSqlValues : List ( String, String ) -> Value -> Result Error (List SqlValue)
//
// The values of one record, one SqlValue per colspec column, in colspec
// order (v0.29.0). Std.Db.Store renders the INSERT text of insertOrIgnore,
// upsertWith and insertReturning itself and binds these values, so the
// column list and the values cannot drift apart. A column binds the
// record's value read by its kind, as Db_execObject does: a blob is its
// JSON text, a missing or null value is SqlNull. A value that is not a
// record gives one SqlNull per column (Store refuses a non-record codec
// before any statement, so that never reaches the database).
func Db_objectSqlValues(colspecArg, objArg any) any {
	fields, _ := jsonObjFields(objArg)
	out := []any{}
	for _, cs := range AsList(colspecArg) {
		t := AsTuple2(cs)
		base, _ := codecSplitKind(AsString(t.V1))
		arg, err := codecRecordArg("Store", fields, AsString(t.V0), base)
		if err != nil {
			return Err[any, any](ErrInvalidInput(err.Error()))
		}
		out = append(out, codecSqlValueOf(arg))
	}
	return Ok[any, any](out)
}

// codecSqlValueOf wraps a driver value from rawToSqlArg as a Std.Db.SqlValue.
func codecSqlValueOf(v any) any {
	switch x := v.(type) {
	case nil:
		return SkyADT{Tag: 8, SkyName: "SqlNull", Fields: []any{
			SkyADT{Tag: 0, SkyName: "SqlString", Fields: []any{""}},
		}}
	case string:
		return SkyADT{Tag: 0, SkyName: "SqlString", Fields: []any{x}}
	case int64:
		return SkyADT{Tag: 1, SkyName: "SqlInt", Fields: []any{int(x)}}
	case int:
		return SkyADT{Tag: 1, SkyName: "SqlInt", Fields: []any{x}}
	case float64:
		return SkyADT{Tag: 2, SkyName: "SqlFloat", Fields: []any{x}}
	case bool:
		return SkyADT{Tag: 3, SkyName: "SqlBool", Fields: []any{x}}
	default:
		return SkyADT{Tag: 0, SkyName: "SqlString", Fields: []any{fmt.Sprintf("%v", x)}}
	}
}

// Db_writeReturningObjects : Db -> String -> List SqlValue
//
//	-> List ( String, String, List ( String, String ) ) -> Task Error (List (List String))
//
// The Std.Db.Store RETURNING kernel (v0.29.0): an INSERT / UPDATE / DELETE
// … RETURNING, read back as Db_queryObjectGroups reads a SELECT, so the rows
// decode through the store's codec. It is a write, so it carries the SSR
// settle guard that Db_exec has: a GET server-side render never mutates.
func Db_writeReturningObjects(connArg, sqlArg, paramsArg, groupsArg any) any {
	return func() any {
		if r := ssrSuppressedWrite("store.write:returning"); r != nil {
			return r
		}
		return AnyTaskRun(Db_queryObjectGroups(connArg, sqlArg, paramsArg, groupsArg))
	}
}

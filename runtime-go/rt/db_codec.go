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
func codecColIsTouch(kind string) bool { return strings.Contains(kind, "touch") }

// codecColExtras parses the `|`-delimited flag suffix Store.unique / Store.default*
// append onto a colspec kind, returning (unique, defaultKind, defaultVal) where
// defaultKind ∈ {none,now,text,int,bool} matches schemaDefault.
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
			defKind, defVal = "text", f[len("dtext="):]
		case strings.HasPrefix(f, "dint="):
			defKind, defVal = "int", f[len("dint="):]
		case strings.HasPrefix(f, "dbool="):
			defKind, defVal = "bool", f[len("dbool="):]
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
//     (An ALTER ADD on an existing table stays nullable — see Db_autoMigrate — since
//     existing rows can't satisfy NOT NULL.)
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

// Db_createCols renders + executes the CREATE TABLE derived from a colspec.
func Db_createCols(connArg, tableArg, colspecArg, pkArg any) any {
	return func() any {
		d, ok := connArg.(*SkyDb)
		if !ok {
			return Err[any, any](ErrInvalidInput("Store.create: first argument is not a Db"))
		}
		sm := codecColspecSchemaMap(AsString(tableArg), colspecArg, AsString(pkArg))
		for _, stmt := range schemaRenderTable(d.driver, sm) {
			if _, err := d.executor().Exec(stmt); err != nil {
				return Err[any, any](ErrIo("Store.create: " + err.Error()))
			}
		}
		return Ok[any, any](struct{}{})
	}
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
		sql = "SELECT column_name FROM information_schema.columns WHERE table_name = ?"
		params = []any{table}
	} else {
		sql = "PRAGMA table_info(" + table + ")" // PRAGMA takes no bind params
		params = []any{}
	}
	resp := AnyTaskRun(Db_query(d, sql, params))
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
			out[dbRawToString(v)] = true
		}
	}
	return out, nil
}

// Db_autoMigrate : Db -> String -> colspec -> String -> Task Error (List String).
// SAFE additive migration: creates the table if absent; ADDs any columns the
// type gained (nullable — the codec supplies defaults on read). Never drops,
// renames, or retypes a column — those are gated/manual per the migration
// architecture. Returns the applied statements (empty on an up-to-date table).
func Db_autoMigrate(connArg, tableArg, colspecArg, pkArg any) any {
	return func() any {
		d, ok := connArg.(*SkyDb)
		if !ok {
			return Err[any, any](ErrInvalidInput("Store.migrate: first argument is not a Db"))
		}
		table := AsString(tableArg)
		if !codecValidIdent(table) {
			return Err[any, any](ErrInvalidInput("Store.migrate: invalid table name"))
		}
		current, err := codecTableColumns(d, table)
		if err != nil {
			return Err[any, any](ErrIo("Store.migrate: " + err.Error()))
		}
		applied := []any{}
		if len(current) == 0 {
			// table absent → create it
			for _, stmt := range schemaRenderTable(d.driver, codecColspecSchemaMap(table, colspecArg, AsString(pkArg))) {
				if _, e := d.executor().Exec(stmt); e != nil {
					return Err[any, any](ErrIo("Store.migrate: " + e.Error()))
				}
				applied = append(applied, stmt)
			}
			return Ok[any, any](applied)
		}
		// existing → add any missing columns (nullable)
		for _, cs := range AsList(colspecArg) {
			t := AsTuple2(cs)
			name := AsString(t.V0)
			if !codecValidIdent(name) || current[name] {
				continue
			}
			sqlType := schemaTypeName(d.driver, codecColKindToSchema(AsString(t.V1)))
			stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, name, sqlType)
			if _, e := d.executor().Exec(stmt); e != nil {
				return Err[any, any](ErrIo("Store.migrate: " + e.Error()))
			}
			applied = append(applied, stmt)
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

// rawToSqlArg converts a JSON raw value to a driver arg per column kind.
func rawToSqlArg(raw any, kind string) any {
	if raw == nil {
		return nil
	}
	switch kind {
	case "blob":
		b, err := json.Marshal(raw)
		if err != nil {
			return ""
		}
		return string(b)
	case "int":
		return int64(AsIntOrZero(raw))
	case "real":
		return AsFloatOrZero(raw)
	case "bool":
		if b, ok := raw.(bool); ok {
			return b
		}
		return AsIntOrZero(raw) != 0
	default: // text
		if s, ok := raw.(string); ok {
			return s
		}
		return fmt.Sprintf("%v", raw)
	}
}

// Db_execObject splits a record's JSON object into columns and INSERTs it.
func Db_execObject(connArg, tableArg, colspecArg, objArg any) any {
	return func() any {
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
			cols = append(cols, name)
			params = append(params, rawToSqlArg(fields[name], base))
		}
		ph := make([]string, len(cols))
		for i := range ph {
			ph[i] = "?"
		}
		sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
			AsString(tableArg), strings.Join(cols, ", "), strings.Join(ph, ", "))
		return AnyTaskRun(Db_exec(connArg, sql, params))
	}
}

// Db_execObjectWith is Db_execObject plus a list of app-computed (column,
// SqlValue) pairs (Store.defaultWith) appended to the INSERT — the SqlValue
// params bind through Db_exec's SqlValue path, so a UUID PK generated in Sky
// lands in the row without the record carrying it.
func Db_execObjectWith(connArg, tableArg, colspecArg, objArg, extraArg any) any {
	return func() any {
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
			cols = append(cols, name)
			params = append(params, rawToSqlArg(fields[name], base))
		}
		for _, ex := range AsList(extraArg) {
			t := AsTuple2(ex)
			cols = append(cols, AsString(t.V0))
			params = append(params, t.V1) // a Sky SqlValue ADT — Db_exec binds it
		}
		if len(cols) == 0 {
			return Ok[any, any](0)
		}
		ph := make([]string, len(cols))
		for i := range ph {
			ph[i] = "?"
		}
		sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
			AsString(tableArg), strings.Join(cols, ", "), strings.Join(ph, ", "))
		return AnyTaskRun(Db_exec(connArg, sql, params))
	}
}

// Db_upsertObject inserts a record, or updates it in place on a primary-key
// conflict — `Store.upsert`. Uses `INSERT ... ON CONFLICT(pk) DO UPDATE SET
// col = excluded.col` (SQLite ≥ 3.24 / Postgres), updating every non-PK column
// to the incoming value. If the PK is the only column, DO NOTHING.
func Db_upsertObject(connArg, tableArg, colspecArg, pkArg, objArg any) any {
	return func() any {
		fields, ok := jsonObjFields(objArg)
		if !ok {
			return Err[any, any](ErrInvalidInput("Store.upsert: value is not a record"))
		}
		pk := AsString(pkArg)
		cols := []string{}
		params := []any{}
		setParts := []string{}
		for _, cs := range AsList(colspecArg) {
			t := AsTuple2(cs)
			name := AsString(t.V0)
			base, _ := codecSplitKind(AsString(t.V1))
			cols = append(cols, name)
			params = append(params, rawToSqlArg(fields[name], base))
			if name != pk {
				setParts = append(setParts, name+" = excluded."+name)
			}
		}
		ph := make([]string, len(cols))
		for i := range ph {
			ph[i] = "?"
		}
		var conflict string
		if len(setParts) == 0 {
			conflict = fmt.Sprintf("ON CONFLICT(%s) DO NOTHING", pk)
		} else {
			conflict = fmt.Sprintf("ON CONFLICT(%s) DO UPDATE SET %s", pk, strings.Join(setParts, ", "))
		}
		sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) %s",
			AsString(tableArg), strings.Join(cols, ", "), strings.Join(ph, ", "), conflict)
		return AnyTaskRun(Db_exec(connArg, sql, params))
	}
}

// Db_execObjectMany bulk-inserts many records in ONE multi-row INSERT (for
// time-series / batch writes) — `Store.insertMany`. All rows share the colspec;
// each row's values bind positionally. Empty list is a no-op.
func Db_execObjectMany(connArg, tableArg, colspecArg, objsArg any) any {
	return func() any {
		objs := AsList(objsArg)
		if len(objs) == 0 {
			return Ok[any, any](0)
		}
		colspec := AsList(colspecArg)
		cols := make([]string, len(colspec))
		for i, cs := range colspec {
			cols[i] = AsString(AsTuple2(cs).V0)
		}
		onePh := "(" + strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ") + ")"
		rowPh := make([]string, 0, len(objs))
		params := []any{}
		for _, obj := range objs {
			fields, ok := jsonObjFields(obj)
			if !ok {
				return Err[any, any](ErrInvalidInput("Store.insertMany: a value is not a record"))
			}
			rowPh = append(rowPh, onePh)
			for _, cs := range colspec {
				t := AsTuple2(cs)
				base, _ := codecSplitKind(AsString(t.V1))
				params = append(params, rawToSqlArg(fields[AsString(t.V0)], base))
			}
		}
		sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES %s",
			AsString(tableArg), strings.Join(cols, ", "), strings.Join(rowPh, ", "))
		return AnyTaskRun(Db_exec(connArg, sql, params))
	}
}

// rowValToJsonRaw converts a DB row value to a JSON raw value per column kind.
func rowValToJsonRaw(raw any, present bool, kind string) any {
	if !present || raw == nil {
		return nil
	}
	if tag, _ := anyMaybeView(raw); tag == 1 { // NULL comes back as a Sky Nothing
		return nil
	}
	s := dbRawToString(raw)
	switch kind {
	case "int":
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n
		}
		return nil
	case "real":
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f
		}
		return nil
	case "bool":
		return dbTruthy(s)
	case "blob":
		var v any
		if json.Unmarshal([]byte(s), &v) == nil {
			return v
		}
		return nil
	default: // text
		return s
	}
}

// Db_queryObjects runs a query and reassembles each row into a JSON string
// (per the colspec) for Sky-side decode via Codec.fromJson.
func Db_queryObjects(connArg, sqlArg, paramsArg, colspecArg any) any {
	return func() any {
		resp := AnyTaskRun(Db_query(connArg, sqlArg, paramsArg))
		r, ok := resp.(SkyResult[any, any])
		if !ok || r.Tag != 0 {
			return resp
		}
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
				raw, present := m[name]
				obj.keys = append(obj.keys, name)
				obj.vals = append(obj.vals, rowValToJsonRaw(raw, present, base))
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

// Db_dumpProject prints a project's schema as JSON between markers (SKY_SCHEMA_*)
// so `sky db migrate --gen` can capture the target schema WITHOUT a live DB —
// this is the pure schema-dump the file-based migration flow diffs against.
func Db_dumpProject(tablesArg any) any {
	return func() any {
		// jdefault mirrors the runtime's migDefault (db_migrate_ops.go) AND the Rust
		// side's default shape, so a captured DEFAULT round-trips dump → schema.json →
		// createTable op → renderMigOp unchanged.
		type jdefault struct {
			Int  *int64  `json:"int,omitempty"`
			Text *string `json:"text,omitempty"`
			Bool *bool   `json:"bool,omitempty"`
			Now  bool    `json:"now,omitempty"`
		}
		type jcol struct {
			Name     string    `json:"name"`
			Kind     string    `json:"kind"`
			Nullable bool      `json:"nullable"`
			Autoinc  bool      `json:"autoinc,omitempty"`
			Unique   bool      `json:"unique,omitempty"`
			Default  *jdefault `json:"default,omitempty"`
		}
		type jtable struct {
			Name    string `json:"name"`
			Pk      string `json:"pk"`
			Columns []jcol `json:"columns"`
		}
		out := struct {
			Tables []jtable `json:"tables"`
		}{}
		for _, t := range AsList(tablesArg) {
			jt := jtable{
				Name: fmt.Sprintf("%v", Field(t, "Name")),
				Pk:   fmt.Sprintf("%v", Field(t, "Pk")),
			}
			for _, c := range AsList(Field(t, "Cols")) {
				tup := AsTuple2(c)
				raw := AsString(tup.V1)
				base, nullable := codecSplitKind(raw)
				unique, defKind, defVal := codecColExtras(raw)
				jc := jcol{
					Name: AsString(tup.V0), Kind: base, Nullable: nullable,
					Autoinc: codecColIsAutoInc(raw), Unique: unique,
				}
				switch defKind {
				case "now":
					jc.Default = &jdefault{Now: true}
				case "int":
					if n, err := strconv.ParseInt(defVal, 10, 64); err == nil {
						jc.Default = &jdefault{Int: &n}
					}
				case "text":
					v := defVal
					jc.Default = &jdefault{Text: &v}
				case "bool":
					b := defVal == "true"
					jc.Default = &jdefault{Bool: &b}
				}
				jt.Columns = append(jt.Columns, jc)
			}
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
		setCols, params := codecSetClause(dbDriverOf(connArg), setColspecArg, fields)
		if len(setCols) == 0 {
			return Ok[any, any](0)
		}
		pkCol := AsString(pkColArg)
		pkBase, _ := codecSplitKind(AsString(pkKindArg))
		params = append(params, rawToSqlArg(fields[pkCol], pkBase))
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
		setCols, params := codecSetClause(dbDriverOf(connArg), setColspecArg, fields)
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
func codecSetClause(driver string, setColspecArg any, fields map[string]any) ([]string, []any) {
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
		setCols = append(setCols, name+" = ?")
		params = append(params, rawToSqlArg(fields[name], base))
	}
	return setCols, params
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

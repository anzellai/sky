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

// codecSplitKind splits a colspec kind into (baseKind, nullable). A trailing `?`
// marks a nullable column (a Maybe field); its absence means NOT NULL. A trailing
// `!` marks an auto-increment PK (see codecColIsAutoInc) and is stripped here so
// every read/write path sees the plain base kind.
func codecSplitKind(kind string) (string, bool) {
	kind = strings.TrimSuffix(kind, "!")
	if strings.HasSuffix(kind, "?") {
		return kind[:len(kind)-1], true
	}
	return kind, false
}

// codecColIsAutoInc reports whether a colspec kind carries the `!` auto-increment
// marker that `Store.serial` stamps on the PK column.
func codecColIsAutoInc(kind string) bool { return strings.HasSuffix(kind, "!") }

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

// codecColspecSchemaMap builds a Schema-Table map from a (name,kind) colspec.
// The PK column is NOT NULL; all others are nullable (the codec enforces types).
func codecColspecSchemaMap(table string, colspecArg any, pk string) map[string]any {
	cols := []any{}
	for _, cs := range AsList(colspecArg) {
		t := AsTuple2(cs)
		name := AsString(t.V0)
		rawKind := AsString(t.V1)
		_, nullable := codecSplitKind(rawKind)
		autoInc := codecColIsAutoInc(rawKind) && name == pk
		cols = append(cols, map[string]any{
			"Name": name, "Kind": codecColKindToSchema(rawKind),
			// A required (non-Maybe) column is NOT NULL on a FRESH table; the PK
			// always is. (ALTER ADD on an existing table stays nullable — see
			// Db_autoMigrate — since existing rows can't satisfy NOT NULL.)
			"IsPk": name == pk, "IsNotNull": !nullable || name == pk, "IsUnique": false,
			"IsAutoInc": autoInc, "DefaultKind": "none", "DefaultVal": "", "ForeignKey": "",
		})
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
		type jcol struct {
			Name     string `json:"name"`
			Kind     string `json:"kind"`
			Nullable bool   `json:"nullable"`
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
				base, nullable := codecSplitKind(AsString(tup.V1))
				jt.Columns = append(jt.Columns, jcol{Name: AsString(tup.V0), Kind: base, Nullable: nullable})
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
		setCols, params := codecSetClause(setColspecArg, fields)
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
		setCols, params := codecSetClause(setColspecArg, fields)
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

// codecSetClause builds the `col = ?` fragments + bound values for a set-column
// spec, pulling each value from the codec-encoded record fields.
func codecSetClause(setColspecArg any, fields map[string]any) ([]string, []any) {
	setCols := []string{}
	params := []any{}
	for _, cs := range AsList(setColspecArg) {
		t := AsTuple2(cs)
		name := AsString(t.V0)
		base, _ := codecSplitKind(AsString(t.V1))
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

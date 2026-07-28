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

func codecColKindToSchema(kind string) string {
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

// Db_createCols renders + executes the CREATE TABLE derived from a colspec.
// The PK column is NOT NULL; all others are nullable (the codec enforces types).
func Db_createCols(connArg, tableArg, colspecArg, pkArg any) any {
	return func() any {
		d, ok := connArg.(*SkyDb)
		if !ok {
			return Err[any, any](ErrInvalidInput("Store.create: first argument is not a Db"))
		}
		pk := AsString(pkArg)
		cols := []any{}
		for _, cs := range AsList(colspecArg) {
			t := AsTuple2(cs)
			name := AsString(t.V0)
			cols = append(cols, map[string]any{
				"Name": name, "Kind": codecColKindToSchema(AsString(t.V1)),
				"IsPk": name == pk, "IsNotNull": name == pk, "IsUnique": false,
				"IsAutoInc": false, "DefaultKind": "none", "DefaultVal": "", "ForeignKey": "",
			})
		}
		sm := map[string]any{"Name": AsString(tableArg), "Columns": cols, "Indexes": []any{}}
		for _, stmt := range schemaRenderTable(d.driver, sm) {
			if _, err := d.executor().Exec(stmt); err != nil {
				return Err[any, any](ErrIo("Store.create: " + err.Error()))
			}
		}
		return Ok[any, any](struct{}{})
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
			cols = append(cols, name)
			params = append(params, rawToSqlArg(fields[name], AsString(t.V1)))
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
				raw, present := m[name]
				obj.keys = append(obj.keys, name)
				obj.vals = append(obj.vals, rowValToJsonRaw(raw, present, AsString(t.V1)))
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

// Package rt — Std.Db.Table: a reflection-based record↔row mapper.
//
// One `Db.Table a` value (carrying a zero-value witness of the record type +
// constraint/codec metadata) drives DDL, typed reads, and typed writes. The
// record type is the single source of truth for column names + types; the
// runtime reflects the Go struct a Sky record lowers to (exported PascalCase
// fields, no tags — e.g. `struct { Id string; PriceMinor int; Active bool }`).
//
// Boundary (the sqlx split): Table owns the record↔row MAPPING; SQL stays SQL.
// `select` takes a raw WHERE/ORDER/JOIN/LIMIT tail; joins decode into any record
// whose fields match the projection. No relations / eager-loading magic.
//
// Field ↔ column: camelCase field → snake_case column.
// Type ↔ column: string→TEXT, int→BIGINT, float→REAL, bool→bool, Maybe a→
// nullable(inner). Enums via `enum` (constructor name ↔ TEXT); anything else via
// `codec` (Sky encode/decode closures, invoked through SkyCall).
package rt

import (
	"fmt"
	"reflect"
	"strings"
)

// ── Table spec access (the map[string]any the .sky builders produce) ─────────

func dbTableName(t any) string { return fmt.Sprintf("%v", Field(t, "Name")) }
func dbTableSample(t any) any  { return Field(t, "Sample") }
func dbTablePk(t any) string   { return fmt.Sprintf("%v", Field(t, "Pk")) }
func dbTableStrList(t any, k string) []string {
	out := []string{}
	for _, v := range AsList(Field(t, k)) {
		out = append(out, fmt.Sprintf("%v", v))
	}
	return out
}

// codecFor returns the (encode, decode) Sky closures registered for a column,
// or (nil, nil).
func dbCodecFor(t any, col string) (any, any) {
	for _, c := range AsList(Field(t, "Codecs")) {
		if fmt.Sprintf("%v", Field(c, "Col")) == col {
			return Field(c, "Enc"), Field(c, "Dec")
		}
	}
	return nil, nil
}

// enumFor returns the (value, name) pairs registered for an enum column, or nil.
// Nullary enums lower to a runtime int (no constructor name), so the mapping to
// stored TEXT is carried explicitly as value↔name pairs and matched by value.
func dbEnumFor(t any, col string) []any {
	for _, e := range AsList(Field(t, "Enums")) {
		if fmt.Sprintf("%v", Field(e, "Col")) == col {
			return AsList(Field(e, "Pairs"))
		}
	}
	return nil
}

// ── Field/column name mapping ────────────────────────────────────────────────

// dbFieldToColumn: PascalCase Go field → snake_case column.
// "Id"→"id", "PriceMinor"→"price_minor", "ImageUrl"→"image_url".
func dbFieldToColumn(name string) string {
	var b strings.Builder
	runes := []rune(name)
	for i, r := range runes {
		if r >= 'A' && r <= 'Z' {
			if i > 0 && (runes[i-1] < 'A' || runes[i-1] > 'Z') {
				b.WriteByte('_')
			}
			b.WriteRune(r - 'A' + 'a')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// dbStructType resolves the struct reflect.Type of the sample witness.
func dbStructType(sample any) (reflect.Type, bool) {
	rv := reflect.ValueOf(sample)
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil, false
	}
	return rv.Type(), true
}

// ── DDL derivation (reuses schemaRenderTable) ────────────────────────────────

// dbTableToSchemaMap builds a Schema-Table-shaped map from the sample's fields +
// constraints, so schemaRenderTable renders the dialect-correct DDL.
func dbTableToSchemaMap(t any) (map[string]any, bool) {
	st, ok := dbStructType(dbTableSample(t))
	if !ok {
		return nil, false
	}
	pk := dbTablePk(t)
	uniques := map[string]bool{}
	for _, u := range dbTableStrList(t, "Uniques") {
		uniques[u] = true
	}
	cols := []any{}
	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		if f.PkgPath != "" { // unexported — skip
			continue
		}
		col := dbFieldToColumn(f.Name)
		kind, nullable := dbSchemaKindForField(t, col, f.Type)
		cols = append(cols, map[string]any{
			"Name": col, "Kind": kind,
			"IsPk":        col == pk,
			"IsNotNull":   !nullable && col != pk,
			"IsUnique":    uniques[col],
			"IsAutoInc":   false,
			"DefaultKind": "none", "DefaultVal": "", "ForeignKey": "",
		})
	}
	indexes := []any{}
	for i, ix := range dbTableStrList(t, "Indexes") {
		indexes = append(indexes, map[string]any{
			"Name":          fmt.Sprintf("idx_%s_%d", dbTableName(t), i),
			"Columns":       []any{ix},
			"IsUniqueIndex": false,
		})
	}
	return map[string]any{"Name": dbTableName(t), "Columns": cols, "Indexes": indexes}, true
}

// dbSchemaKindForField maps a Go struct field type → schema kind + nullability.
// Enum/codec columns are TEXT. Maybe a → nullable inner.
func dbSchemaKindForField(t any, col string, ft reflect.Type) (string, bool) {
	if enc, _ := dbCodecFor(t, col); enc != nil {
		return "text", false
	}
	if len(dbEnumFor(t, col)) > 0 {
		return "text", false
	}
	// Maybe a — struct with Tag + JustValue → nullable, inner kind from JustValue.
	if ft.Kind() == reflect.Struct {
		if jv, ok := ft.FieldByName("JustValue"); ok {
			if _, hasTag := ft.FieldByName("Tag"); hasTag {
				k, _ := dbSchemaKindForField(t, col, jv.Type)
				return k, true // nullable
			}
		}
	}
	switch ft.Kind() {
	case reflect.String:
		return "text", false
	case reflect.Bool:
		return "bool", false
	case reflect.Float32, reflect.Float64:
		return "real", false
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "bigint", false
	default:
		return "text", false // interface{}/unknown → TEXT (use a codec for fidelity)
	}
}

// ── Write: record → (columns, param values) ──────────────────────────────────

func dbEncodeRecord(t any, record any) ([]string, []any, error) {
	st, ok := dbStructType(dbTableSample(t))
	if !ok {
		return nil, nil, fmt.Errorf("table sample is not a record")
	}
	rv := reflect.ValueOf(record)
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil, nil, fmt.Errorf("insert/update value is not a record")
	}
	cols := []string{}
	vals := []any{}
	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		if f.PkgPath != "" {
			continue
		}
		col := dbFieldToColumn(f.Name)
		fv := rv.Field(i).Interface()
		cols = append(cols, col)
		vals = append(vals, dbEncodeField(t, col, fv))
	}
	return cols, vals, nil
}

// dbEncodeField turns a struct field value into a driver-bindable value.
// Codec → SkyCall(enc); enum → constructor name; else raw (Db_exec's dbBindArg
// handles Sql*, Maybe, scalars).
func dbEncodeField(t any, col string, fv any) any {
	if enc, _ := dbCodecFor(t, col); enc != nil {
		return SkyCall(enc, fv)
	}
	if pairs := dbEnumFor(t, col); len(pairs) > 0 {
		for _, p := range pairs {
			if fmt.Sprintf("%v", Field(p, "V0")) == fmt.Sprintf("%v", fv) {
				return AsString(Field(p, "V1"))
			}
		}
		return fmt.Sprintf("%v", fv) // unmapped — store raw
	}
	return fv
}

// ── Read: rows → []struct ────────────────────────────────────────────────────

func dbRowsToStructs(t any, rows []any) (any, error) {
	st, ok := dbStructType(dbTableSample(t))
	if !ok {
		return nil, fmt.Errorf("table sample is not a record")
	}
	out := make([]any, 0, len(rows))
	for _, row := range rows {
		m, ok := dbRowAsMap(row)
		if !ok {
			return nil, fmt.Errorf("row is not a Dict")
		}
		sv := reflect.New(st).Elem()
		for i := 0; i < st.NumField(); i++ {
			f := st.Field(i)
			if f.PkgPath != "" {
				continue
			}
			col := dbFieldToColumn(f.Name)
			raw, present := m[col]
			if err := dbDecodeInto(t, col, sv.Field(i), raw, present); err != nil {
				return nil, err
			}
		}
		out = append(out, sv.Interface())
	}
	return out, nil
}

// dbDecodeInto sets one struct field from a raw column value.
func dbDecodeInto(t any, col string, dst reflect.Value, raw any, present bool) error {
	// Codec column → SkyCall(dec, rawString).
	if _, dec := dbCodecFor(t, col); dec != nil {
		v := SkyCall(dec, dbRawToString(raw))
		dst.Set(reflect.ValueOf(v).Convert(dst.Type()))
		return nil
	}
	// Enum column → match stored name back to its value.
	if pairs := dbEnumFor(t, col); len(pairs) > 0 {
		name := dbRawToString(raw)
		for _, p := range pairs {
			if AsString(Field(p, "V1")) == name {
				dst.Set(reflect.ValueOf(Field(p, "V0")).Convert(dst.Type()))
				return nil
			}
		}
		return fmt.Errorf("column %s: no enum entry for %q", col, name)
	}
	// Maybe a → Nothing / Just(inner). A NULL column comes back from Db_query
	// already wrapped as a Sky Nothing (SkyMaybe); a present value comes raw.
	if dst.Kind() == reflect.Struct {
		if _, hasJV := dst.Type().FieldByName("JustValue"); hasJV {
			if _, hasTag := dst.Type().FieldByName("Tag"); hasTag {
				if !present || raw == nil {
					dst.FieldByName("Tag").SetInt(1) // Nothing
					return nil
				}
				if tag, just := anyMaybeView(raw); tag >= 0 {
					if tag != 0 { // already a Nothing marker
						dst.FieldByName("Tag").SetInt(1)
						return nil
					}
					raw = just // unwrap Just(inner)
				}
				dst.FieldByName("Tag").SetInt(0) // Just
				return dbSetScalar(dst.FieldByName("JustValue"), raw, col)
			}
		}
	}
	return dbSetScalar(dst, raw, col)
}

// dbSetScalar converts a raw driver value into a scalar struct field.
func dbSetScalar(dst reflect.Value, raw any, col string) error {
	switch dst.Kind() {
	case reflect.String:
		dst.SetString(dbRawToString(raw))
	case reflect.Bool:
		dst.SetBool(dbTruthy(dbRawToString(raw)))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		dst.SetInt(int64(AsIntOrZero(raw)))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		dst.SetUint(uint64(AsIntOrZero(raw)))
	case reflect.Float32, reflect.Float64:
		dst.SetFloat(AsFloatOrZero(raw))
	default:
		// interface{} / unknown — best effort store the string.
		if dst.Kind() == reflect.Interface {
			dst.Set(reflect.ValueOf(dbRawToString(raw)))
		}
	}
	return nil
}

func dbRawToString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return fmt.Sprintf("%v", v)
}

// ── Kernels ──────────────────────────────────────────────────────────────────

// Table_create renders + executes the DDL derived from the record + constraints.
func Table_create(connArg, tableArg any) any {
	return func() any {
		d, ok := connArg.(*SkyDb)
		if !ok {
			return Err[any, any](ErrInvalidInput("Table.createTable: not a Db"))
		}
		sm, ok := dbTableToSchemaMap(tableArg)
		if !ok {
			return Err[any, any](ErrInvalidInput("Table.createTable: sample is not a record"))
		}
		for _, stmt := range schemaRenderTable(d.driver, sm) {
			if _, err := d.executor().Exec(stmt); err != nil {
				return Err[any, any](ErrIo("Table.createTable: " + err.Error()))
			}
		}
		return Ok[any, any](struct{}{})
	}
}

// Table_all : SELECT * FROM t, decoded into List a.
func Table_all(connArg, tableArg any) any {
	return dbTableSelectTail(connArg, tableArg, "", []any{})
}

// Table_select : SELECT * FROM t <tail>, params, decoded into List a.
func Table_select(connArg, tableArg, tailArg, paramsArg any) any {
	return dbTableSelectTail(connArg, tableArg, AsString(tailArg), AsList(paramsArg))
}

func dbTableSelectTail(connArg, tableArg any, tail string, params []any) any {
	return func() any {
		name := dbTableName(tableArg)
		sql := "SELECT * FROM " + name
		if strings.TrimSpace(tail) != "" {
			sql = sql + " " + tail
		}
		resp := AnyTaskRun(Db_query(connArg, sql, params))
		r, ok := resp.(SkyResult[any, any])
		if !ok || r.Tag != 0 {
			return resp
		}
		structs, err := dbRowsToStructs(tableArg, AsList(r.OkValue))
		if err != nil {
			return Err[any, any](ErrDecode("Table.select: " + err.Error()))
		}
		return Ok[any, any](structs)
	}
}

// Table_findBy : SELECT * FROM t WHERE col = ? LIMIT 1 → Maybe a.
func Table_findBy(connArg, tableArg, colArg, valArg any) any {
	return func() any {
		col := AsString(colArg)
		sql := "SELECT * FROM " + dbTableName(tableArg) + " WHERE " + col + " = ? LIMIT 1"
		resp := AnyTaskRun(Db_query(connArg, sql, []any{valArg}))
		r, ok := resp.(SkyResult[any, any])
		if !ok || r.Tag != 0 {
			return resp
		}
		structs, err := dbRowsToStructs(tableArg, AsList(r.OkValue))
		if err != nil {
			return Err[any, any](ErrDecode("Table.findBy: " + err.Error()))
		}
		list := structs.([]any)
		if len(list) == 0 {
			return Ok[any, any](SkyMaybe[any]{Tag: 1}) // Nothing
		}
		return Ok[any, any](SkyMaybe[any]{Tag: 0, JustValue: list[0]}) // Just
	}
}

// Table_insert : reflect record → INSERT.
func Table_insert(connArg, tableArg, recordArg any) any {
	return func() any {
		cols, vals, err := dbEncodeRecord(tableArg, recordArg)
		if err != nil {
			return Err[any, any](ErrInvalidInput("Table.insert: " + err.Error()))
		}
		ph := make([]string, len(cols))
		for i := range ph {
			ph[i] = "?"
		}
		sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
			dbTableName(tableArg), strings.Join(cols, ", "), strings.Join(ph, ", "))
		return AnyTaskRun(Db_exec(connArg, sql, vals))
	}
}

// Table_update : reflect record → UPDATE t SET … WHERE pkCol = ?.
func Table_update(connArg, tableArg, pkColArg, pkValArg, recordArg any) any {
	return func() any {
		pkCol := AsString(pkColArg)
		cols, vals, err := dbEncodeRecord(tableArg, recordArg)
		if err != nil {
			return Err[any, any](ErrInvalidInput("Table.update: " + err.Error()))
		}
		sets := []string{}
		args := []any{}
		for i, c := range cols {
			if c == pkCol {
				continue // never update the key
			}
			sets = append(sets, c+" = ?")
			args = append(args, vals[i])
		}
		args = append(args, pkValArg)
		sql := fmt.Sprintf("UPDATE %s SET %s WHERE %s = ?",
			dbTableName(tableArg), strings.Join(sets, ", "), pkCol)
		return AnyTaskRun(Db_exec(connArg, sql, args))
	}
}

// Table_delete : DELETE FROM t WHERE pkCol = ?.
func Table_delete(connArg, tableArg, pkColArg, pkValArg any) any {
	return func() any {
		sql := "DELETE FROM " + dbTableName(tableArg) + " WHERE " + AsString(pkColArg) + " = ?"
		return AnyTaskRun(Db_exec(connArg, sql, []any{pkValArg}))
	}
}

// ── Builder kernels (map[string]any, shallow-clone + set) ────────────────────

func dbTableClone(t any) map[string]any {
	out := map[string]any{}
	if m, ok := t.(map[string]any); ok {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// Table_table : String -> a -> Table a
func Table_table(nameArg, sampleArg any) any {
	return map[string]any{
		"Name": AsString(nameArg), "Sample": sampleArg,
		"Pk": "", "Uniques": []any{}, "Indexes": []any{},
		"Codecs": []any{}, "Enums": []any{},
	}
}

func Table_primaryKey(colArg, tableArg any) any {
	m := dbTableClone(tableArg)
	m["Pk"] = AsString(colArg)
	return m
}

func dbTableAppend(tableArg any, key string, v any) map[string]any {
	m := dbTableClone(tableArg)
	m[key] = append(append([]any{}, AsList(m[key])...), v)
	return m
}

func Table_unique(colArg, tableArg any) any {
	return dbTableAppend(tableArg, "Uniques", AsString(colArg))
}

func Table_index(colArg, tableArg any) any {
	return dbTableAppend(tableArg, "Indexes", AsString(colArg))
}

// Table_enum : String -> List (v, String) -> Table a -> Table a
func Table_enum(colArg, pairsArg, tableArg any) any {
	return dbTableAppend(tableArg, "Enums", map[string]any{
		"Col": AsString(colArg), "Pairs": AsList(pairsArg),
	})
}

// Table_codec : String -> (v -> String) -> (String -> v) -> Table a -> Table a
func Table_codec(colArg, encArg, decArg, tableArg any) any {
	return dbTableAppend(tableArg, "Codecs", map[string]any{
		"Col": AsString(colArg), "Enc": encArg, "Dec": decArg,
	})
}

//go:build !js

// Package rt — Std.Db.Schema runtime: render a typed Table definition into the
// DIALECT-CORRECT DDL for the connection's backend (SQLite or Postgres) and
// execute it. The portable → dialect type mapping (below) is what lets ONE
// schema definition run unchanged on both, killing the INTEGER/BIGINT,
// AUTOINCREMENT/BIGSERIAL, and datetime()/now() drift.
//
// Type mapping favours dev==prod consistency at the DECODED-VALUE level (via
// Std.Db.Decode), while using each backend's natural column type:
//   - `bool` → BOOLEAN on Postgres, INTEGER 0/1 on SQLite. A `SqlBool` param
//     (a native Go bool) binds cleanly to both, and `Decode.bool` reads both
//     representations back to a Sky Bool.
//   - `json` → TEXT on both (native JSONB is a future opt-in).
//   - `bigint`/`timestamp` → BIGINT on Postgres, INTEGER on SQLite (SQLite
//     INTEGER is 8-byte; Postgres INTEGER is 4-byte and overflows on millis) —
//     both read back as int64.
package rt

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Schema_createTable renders + executes the DDL for one Table. Idempotent.
func Schema_createTable(connArg, tableArg any) any {
	return func() any {
		d, ok := connArg.(*SkyDb)
		if !ok {
			return Err[any, any](ErrInvalidInput("schema.createTable: first argument is not a Db"))
		}
		for _, stmt := range schemaRenderTable(d.driver, tableArg) {
			if _, err := d.executor().Exec(stmt); err != nil {
				return Err[any, any](ErrIo("schema.createTable: " + err.Error()))
			}
		}
		return Ok[any, any](struct{}{})
	}
}

// Schema_createSchema creates a list of tables in order.
func Schema_createSchema(connArg, tablesArg any) any {
	return func() any {
		d, ok := connArg.(*SkyDb)
		if !ok {
			return Err[any, any](ErrInvalidInput("schema.createSchema: first argument is not a Db"))
		}
		for _, t := range AsList(tablesArg) {
			for _, stmt := range schemaRenderTable(d.driver, t) {
				if _, err := d.executor().Exec(stmt); err != nil {
					return Err[any, any](ErrIo("schema.createSchema: " + err.Error()))
				}
			}
		}
		return Ok[any, any](struct{}{})
	}
}

// schemaRenderTable → the CREATE TABLE statement + one CREATE INDEX per index.
func schemaRenderTable(driver string, tableArg any) []string {
	name := fmt.Sprintf("%v", fieldOrNil(tableArg, "Name"))
	cols := AsList(fieldOrNil(tableArg, "Columns"))
	defs := make([]string, 0, len(cols))
	for _, c := range cols {
		defs = append(defs, schemaRenderColumn(driver, c))
	}
	out := []string{
		fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (\n  %s\n)", name, strings.Join(defs, ",\n  ")),
	}
	for _, idx := range AsList(fieldOrNil(tableArg, "Indexes")) {
		out = append(out, schemaRenderIndex(name, idx))
	}
	return out
}

func schemaRenderColumn(driver string, colArg any) string {
	name := fmt.Sprintf("%v", fieldOrNil(colArg, "Name"))
	kind := fmt.Sprintf("%v", fieldOrNil(colArg, "Kind"))
	pk := schemaBool(fieldOrNil(colArg, "IsPk"))
	notNull := schemaBool(fieldOrNil(colArg, "IsNotNull"))
	uniq := schemaBool(fieldOrNil(colArg, "IsUnique"))
	autoInc := schemaBool(fieldOrNil(colArg, "IsAutoInc"))
	defKind := fmt.Sprintf("%v", fieldOrNil(colArg, "DefaultKind"))
	defVal := fmt.Sprintf("%v", fieldOrNil(colArg, "DefaultVal"))
	fk := fmt.Sprintf("%v", fieldOrNil(colArg, "ForeignKey"))

	// Auto-increment PK is a single dialect-specific token that already implies
	// PRIMARY KEY.
	if pk && autoInc {
		if driver == "pgx" {
			return name + " BIGSERIAL PRIMARY KEY"
		}
		return name + " INTEGER PRIMARY KEY AUTOINCREMENT"
	}

	parts := []string{name, schemaTypeName(driver, kind)}
	if pk {
		parts = append(parts, "PRIMARY KEY")
	}
	// B8: a primary key is always NOT NULL. SQLite lets a non-INTEGER PK
	// hold NULL unless the column says NOT NULL.
	if notNull || pk {
		parts = append(parts, "NOT NULL")
	}
	if uniq && !pk {
		parts = append(parts, "UNIQUE")
	}
	if defKind == "now" {
		parts = append(parts, "DEFAULT "+schemaNowDefault(driver, kind))
	} else if defKind != "none" && defKind != "" {
		parts = append(parts, "DEFAULT "+schemaDefault(driver, defKind, defVal))
	}
	if fk != "" {
		parts = append(parts, "REFERENCES "+fk)
	}
	return strings.Join(parts, " ")
}

func schemaRenderIndex(tableName string, idxArg any) string {
	idxName := fmt.Sprintf("%v", fieldOrNil(idxArg, "Name"))
	uniq := schemaBool(fieldOrNil(idxArg, "IsUniqueIndex"))
	cols := AsList(fieldOrNil(idxArg, "Columns"))
	colNames := make([]string, len(cols))
	for i, c := range cols {
		colNames[i] = fmt.Sprintf("%v", c)
	}
	kw := "INDEX"
	if uniq {
		kw = "UNIQUE INDEX"
	}
	return fmt.Sprintf("CREATE %s IF NOT EXISTS %s ON %s (%s)",
		kw, idxName, tableName, strings.Join(colNames, ", "))
}

// schemaTypeName — the portable → dialect type mapping. This is the single
// place the dialect difference lives.
func schemaTypeName(driver, kind string) string {
	pg := driver == "pgx"
	switch kind {
	case "text":
		return "TEXT"
	case "int":
		return "INTEGER"
	case "bigint", "timestamp":
		// The one that MUST diverge — Postgres INTEGER is 4-byte + overflows on
		// millis; SQLite INTEGER is 8-byte. Both read back as int64.
		if pg {
			return "BIGINT"
		}
		return "INTEGER"
	case "real":
		if pg {
			return "DOUBLE PRECISION"
		}
		return "REAL"
	case "bool":
		// Native BOOLEAN on Postgres; INTEGER 0/1 on SQLite. A SqlBool (Go bool)
		// binds to both, and Decode.bool reads both back.
		if pg {
			return "BOOLEAN"
		}
		return "INTEGER"
	case "blob":
		if pg {
			return "BYTEA"
		}
		return "BLOB"
	case "json":
		// TEXT on BOTH for read consistency (JSONB is a future opt-in).
		return "TEXT"
	default:
		return "TEXT"
	}
}

func schemaDefault(driver, kind, val string) string {
	switch kind {
	case "int":
		if val == "" {
			return "0"
		}
		return val
	case "text":
		return "'" + strings.ReplaceAll(val, "'", "''") + "'"
	case "float":
		// Store.defaultFloat refuses NaN and Infinity. A value that does not
		// parse as a finite float renders 0, never raw text.
		f, err := strconv.ParseFloat(val, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return "0"
		}
		return strconv.FormatFloat(f, 'g', -1, 64)
	case "bool":
		// BOOLEAN on Postgres (TRUE/FALSE); INTEGER 0/1 on SQLite.
		if driver == "pgx" {
			if val == "true" {
				return "TRUE"
			}
			return "FALSE"
		}
		if val == "true" {
			return "1"
		}
		return "0"
	case "now":
		if driver == "pgx" {
			return "now()"
		}
		return "(datetime('now'))"
	default:
		return "NULL"
	}
}

// schemaIsIntKind reports whether a schema kind is stored as an integer.
func schemaIsIntKind(kind string) bool {
	return kind == "int" || kind == "bigint" || kind == "timestamp"
}

// schemaNowDefault is the `now` DEFAULT of a column of `kind` in CREATE
// TABLE. A text column takes the timestamp text (`datetime('now')` /
// `now()`). An integer column (Std.Db.Schema `timestamp`, `bigInt`, `int`)
// takes epoch milliseconds: `now()` on a BIGINT column made PostgreSQL refuse
// the CREATE TABLE ("default expression is of type timestamp with time
// zone"), and SQLite stored text that an Int field could not read.
func schemaNowDefault(driver, kind string) string {
	if schemaIsIntKind(kind) {
		if driver == "pgx" {
			return "(CAST(EXTRACT(EPOCH FROM now()) * 1000 AS BIGINT))"
		}
		return "(CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER))"
	}
	return schemaDefault(driver, "now", "")
}

func schemaBool(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

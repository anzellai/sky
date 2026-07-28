// Package rt — Std.Db.Schema runtime: render a typed Table definition into the
// DIALECT-CORRECT DDL for the connection's backend (SQLite or Postgres) and
// execute it. The portable → dialect type mapping (below) is what lets ONE
// schema definition run unchanged on both, killing the INTEGER/BIGINT,
// AUTOINCREMENT/BIGSERIAL, and datetime()/now() drift.
//
// Type mapping is chosen for dev==prod READ consistency, not native-per-dialect
// types: `bool` and `json` map to INTEGER/TEXT on BOTH backends so a value reads
// back the same shape whether you developed on SQLite or deployed on Postgres.
// `bigint` is the one that MUST diverge (SQLite INTEGER is 8-byte; Postgres
// INTEGER is 4-byte and overflows on millis) — but both read back as int64.
package rt

import (
	"fmt"
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
	name := fmt.Sprintf("%v", Field(tableArg, "Name"))
	cols := AsList(Field(tableArg, "Columns"))
	defs := make([]string, 0, len(cols))
	for _, c := range cols {
		defs = append(defs, schemaRenderColumn(driver, c))
	}
	out := []string{
		fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (\n  %s\n)", name, strings.Join(defs, ",\n  ")),
	}
	for _, idx := range AsList(Field(tableArg, "Indexes")) {
		out = append(out, schemaRenderIndex(name, idx))
	}
	return out
}

func schemaRenderColumn(driver string, colArg any) string {
	name := fmt.Sprintf("%v", Field(colArg, "Name"))
	kind := fmt.Sprintf("%v", Field(colArg, "Kind"))
	pk := schemaBool(Field(colArg, "IsPk"))
	notNull := schemaBool(Field(colArg, "IsNotNull"))
	uniq := schemaBool(Field(colArg, "IsUnique"))
	autoInc := schemaBool(Field(colArg, "IsAutoInc"))
	defKind := fmt.Sprintf("%v", Field(colArg, "DefaultKind"))
	defVal := fmt.Sprintf("%v", Field(colArg, "DefaultVal"))
	fk := fmt.Sprintf("%v", Field(colArg, "ForeignKey"))

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
	if notNull && !pk {
		parts = append(parts, "NOT NULL")
	}
	if uniq && !pk {
		parts = append(parts, "UNIQUE")
	}
	if defKind != "none" && defKind != "" {
		parts = append(parts, "DEFAULT "+schemaDefault(driver, defKind, defVal))
	}
	if fk != "" {
		parts = append(parts, "REFERENCES "+fk)
	}
	return strings.Join(parts, " ")
}

func schemaRenderIndex(tableName string, idxArg any) string {
	idxName := fmt.Sprintf("%v", Field(idxArg, "Name"))
	uniq := schemaBool(Field(idxArg, "IsUniqueIndex"))
	cols := AsList(Field(idxArg, "Columns"))
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
		// INTEGER (0/1) on BOTH for dev==prod read consistency.
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
	case "bool":
		// bool columns are INTEGER 0/1 on both dialects.
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

func schemaBool(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

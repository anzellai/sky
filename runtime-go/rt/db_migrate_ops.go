// Package rt — migration op renderer. Turns dialect-NEUTRAL typed migration ops
// (the JSON committed in db/migrations/) into dialect-correct SQL, so one
// migration file applies to SQLite AND Postgres. Apply reuses the existing
// checksummed _sky_migrations ledger (Db_migrateApply).
package rt

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type migDefault struct {
	Int  *int64  `json:"int,omitempty"`
	Text *string `json:"text,omitempty"`
	Bool *bool   `json:"bool,omitempty"`
	Now  bool    `json:"now,omitempty"`
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
	case d.Now:
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
	}
	return ""
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
		kw := "INDEX"
		if op.Unique {
			kw = "UNIQUE INDEX"
		}
		return fmt.Sprintf("CREATE %s IF NOT EXISTS %s ON %s (%s)", kw, op.Name, op.Table, strings.Join(op.IndexColumns, ", ")), nil

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
			stmts := make([]string, 0, len(f.Ops))
			for _, op := range f.Ops {
				s, err := renderMigOp(d.driver, op)
				if err != nil {
					return Err[any, any](ErrInvalidInput(err.Error()))
				}
				stmts = append(stmts, s)
			}
			out = append(out, T2[any, any]{V0: f.Id, V1: strings.Join(stmts, ";\n")})
		}
		return Ok[any, any](out)
	}
}

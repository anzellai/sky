// Package rt — data-reset / table-drop kernels for `sky db reset` / `sky db drop`.
//
// Both take a `Db` connection and a `List String` of table names and return a
// `Task Error (List String)` of the SQL statements actually executed (so the CLI
// can report a count + the operator can see what ran). Names are validated with
// `codecValidIdent` before any interpolation; non-existent tables are skipped
// cleanly (introspection via `codecTableColumns`), so passing the whole project's
// declared table set is safe even if some tables were never created.
//
// - Db_resetTables EMPTIES the given tables and resets their autoincrement
//   counters, keeping the schema + the `_sky_migrations` ledger.
// - Db_dropTables DROPs the given tables (the CLI includes `_sky_migrations` in
//   the drop-all case to return to a fresh "never migrated" state).
package rt

import (
	"strings"
)

// dbResetDropNames validates + de-dupes the incoming `List String` of table
// names, returning the ordered unique list. An invalid identifier aborts with a
// non-nil error message so the caller can surface `Err`.
func dbResetDropNames(namesArg any) ([]string, string) {
	seen := map[string]bool{}
	out := []string{}
	for _, n := range AsList(namesArg) {
		name := AsString(n)
		if !codecValidIdent(name) {
			return nil, "invalid table name " + name
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out, ""
}

// dbTableExists reports whether `table` exists (has ≥1 introspected column).
func dbTableExists(d *SkyDb, table string) bool {
	cols, err := codecTableColumns(d, table)
	if err != nil {
		return false
	}
	return len(cols) > 0
}

// Db_resetTables : Db -> List String -> Task Error (List String).
// Empties every named table that actually exists and resets its autoincrement
// counter, leaving the schema (and the migration ledger) intact. Returns the SQL
// statements executed (empty when nothing existed).
func Db_resetTables(connArg, namesArg any) any {
	return func() any {
		d, ok := connArg.(*SkyDb)
		if !ok {
			return Err[any, any](ErrInvalidInput("Store.resetTables: first argument is not a Db"))
		}
		names, verr := dbResetDropNames(namesArg)
		if verr != "" {
			return Err[any, any](ErrInvalidInput("Store.resetTables: " + verr))
		}
		// Keep only tables that exist so we never error on a declared-but-
		// never-created table.
		existing := []string{}
		for _, name := range names {
			if dbTableExists(d, name) {
				existing = append(existing, name)
			}
		}
		applied := []any{}
		if len(existing) == 0 {
			return Ok[any, any](applied)
		}
		if d.driver == "pgx" {
			// One statement empties all tables + resets identities + follows FKs.
			quoted := make([]string, len(existing))
			for i, name := range existing {
				quoted[i] = `"` + name + `"`
			}
			stmt := "TRUNCATE TABLE " + strings.Join(quoted, ", ") + " RESTART IDENTITY CASCADE"
			if _, e := d.executor().Exec(stmt); e != nil {
				return Err[any, any](ErrIo("Store.resetTables: " + e.Error()))
			}
			applied = append(applied, stmt)
			return Ok[any, any](applied)
		}
		// SQLite: disable FK enforcement, DELETE each table, reset the
		// sqlite_sequence rows (only present when an AUTOINCREMENT table
		// exists — its absence is ignored), re-enable FKs.
		if _, e := d.executor().Exec("PRAGMA foreign_keys=OFF"); e != nil {
			return Err[any, any](ErrIo("Store.resetTables: " + e.Error()))
		}
		applied = append(applied, "PRAGMA foreign_keys=OFF")
		for _, name := range existing {
			stmt := `DELETE FROM "` + name + `"`
			if _, e := d.executor().Exec(stmt); e != nil {
				return Err[any, any](ErrIo("Store.resetTables: " + e.Error()))
			}
			applied = append(applied, stmt)
		}
		// Reset autoincrement counters. sqlite_sequence only exists when at
		// least one AUTOINCREMENT table was created; a failure here (no such
		// table) is expected and ignored.
		quoted := make([]string, len(existing))
		for i, name := range existing {
			quoted[i] = "'" + strings.ReplaceAll(name, "'", "''") + "'"
		}
		seqStmt := "DELETE FROM sqlite_sequence WHERE name IN (" + strings.Join(quoted, ", ") + ")"
		if _, e := d.executor().Exec(seqStmt); e == nil {
			applied = append(applied, seqStmt)
		}
		if _, e := d.executor().Exec("PRAGMA foreign_keys=ON"); e != nil {
			return Err[any, any](ErrIo("Store.resetTables: " + e.Error()))
		}
		applied = append(applied, "PRAGMA foreign_keys=ON")
		return Ok[any, any](applied)
	}
}

// Db_dropTables : Db -> List String -> Task Error (List String).
// Drops every named table (IF EXISTS, so a missing table is a no-op). Returns the
// SQL statements executed.
func Db_dropTables(connArg, namesArg any) any {
	return func() any {
		d, ok := connArg.(*SkyDb)
		if !ok {
			return Err[any, any](ErrInvalidInput("Store.dropTables: first argument is not a Db"))
		}
		names, verr := dbResetDropNames(namesArg)
		if verr != "" {
			return Err[any, any](ErrInvalidInput("Store.dropTables: " + verr))
		}
		applied := []any{}
		if len(names) == 0 {
			return Ok[any, any](applied)
		}
		if d.driver == "pgx" {
			for _, name := range names {
				stmt := `DROP TABLE IF EXISTS "` + name + `" CASCADE`
				if _, e := d.executor().Exec(stmt); e != nil {
					return Err[any, any](ErrIo("Store.dropTables: " + e.Error()))
				}
				applied = append(applied, stmt)
			}
			return Ok[any, any](applied)
		}
		// SQLite: disable FK enforcement so drop order doesn't matter, drop
		// each table, re-enable.
		if _, e := d.executor().Exec("PRAGMA foreign_keys=OFF"); e != nil {
			return Err[any, any](ErrIo("Store.dropTables: " + e.Error()))
		}
		applied = append(applied, "PRAGMA foreign_keys=OFF")
		for _, name := range names {
			stmt := `DROP TABLE IF EXISTS "` + name + `"`
			if _, e := d.executor().Exec(stmt); e != nil {
				return Err[any, any](ErrIo("Store.dropTables: " + e.Error()))
			}
			applied = append(applied, stmt)
		}
		if _, e := d.executor().Exec("PRAGMA foreign_keys=ON"); e != nil {
			return Err[any, any](ErrIo("Store.dropTables: " + e.Error()))
		}
		applied = append(applied, "PRAGMA foreign_keys=ON")
		return Ok[any, any](applied)
	}
}

package rt

import (
	"strings"

	"sky-app/rt/procenv"
)

// Read-only database mode, in a file with no build tag: ErrIo (rt.go) adds
// the read-only hint, and rt.go is built for the wasm client too, where no
// database is opened and the mode is never on.

// dbReadOnly reports whether this process opens every app database
// read-only: SKY_READ_ONLY_DB=1, which `sky db check` and `sky db status` set
// for their helper (v0.29.2). A SQLite file is opened with mode=ro (a missing
// file is an Err, not a new empty database), a PostgreSQL session has
// default_transaction_read_only=on, and no telemetry is persisted
// (EnableConsolePersistence).
func dbReadOnly() bool {
	return strings.TrimSpace(procenv.Getenv("SKY_READ_ONLY_DB")) == "1"
}

// withReadOnlyDbHint adds what to do to an I/O error that is a write refused
// because this process opened its databases read-only (SKY_READ_ONLY_DB=1).
// `sky db status` (without db/migrations) runs the app's own main in that
// mode, and since v0.29.2 an app that writes before Db.migrate fails there
// with only SQLite's "attempt to write a readonly database" or PostgreSQL's
// "cannot execute ... in a read-only transaction". Every I/O error goes
// through ErrIo, so the hint reaches each write path; outside read-only
// mode, or for any other error, the text is unchanged.
func withReadOnlyDbHint(msg string) string {
	if !dbIsReadOnlyRefusal(msg) || strings.Contains(msg, "SKY_READ_ONLY_DB") || !dbReadOnly() {
		return msg
	}
	return msg + ". This process opens its databases read-only (SKY_READ_ONLY_DB=1): " +
		"`sky db status` and `sky db check` run it this way so that they write nothing, " +
		"and `sky db status` runs the app's main up to Db.migrate. A write before Db.migrate fails here. " +
		"Move that write after Db.migrate, or use file migrations " +
		"(`sky db init`; `sky db status` then reads db/migrations and does not run main)"
}

// dbIsReadOnlyRefusal reports whether a driver error is a write the database
// refused because the connection is read-only: SQLite SQLITE_READONLY,
// PostgreSQL SQLSTATE 25006 (read_only_sql_transaction).
func dbIsReadOnlyRefusal(msg string) bool {
	return strings.Contains(msg, "attempt to write a readonly database") ||
		strings.Contains(msg, "in a read-only transaction") ||
		strings.Contains(msg, "SQLSTATE 25006")
}

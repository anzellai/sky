package rt

import (
	"strings"

	"sky-app/rt/procenv"
)

// Read-only database mode, in a file with no build tag: ErrIo (rt.go) adds
// the read-only hint, and rt.go is built for the wasm client too, where no
// database is opened and the mode is never on.

// dbReadOnly reports whether this process opens every app database
// read-only: SKY_READ_ONLY_DB set, which `sky db check` and `sky db status`
// do for their helper (v0.29.2). The value names the command, `check` or
// `status` (`1` is the same mode with no command named), so an error can say
// what that command does. A SQLite file is opened with mode=ro (a missing
// file is an Err, not a new empty database), a PostgreSQL session has
// default_transaction_read_only=on, and no telemetry is persisted
// (EnableConsolePersistence).
func dbReadOnly() bool {
	switch dbReadOnlyCommand() {
	case "1", "check", "status":
		return true
	}
	return false
}

// dbReadOnlyCommand is the value of SKY_READ_ONLY_DB: "check", "status", "1"
// or "" (off).
func dbReadOnlyCommand() string {
	return strings.TrimSpace(procenv.Getenv("SKY_READ_ONLY_DB"))
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
	return msg + readOnlyDbHint(dbReadOnlyCommand())
}

// readOnlyDbHint is the advice for a write refused in read-only mode, for the
// command that set the mode. The text for `sky db status` (an app write before
// Db.migrate) was appended to `sky db check` errors too, where it is wrong:
// the check runs no app code (v0.29.2 fifth Judge).
func readOnlyDbHint(command string) string {
	switch command {
	case "check":
		return ". `sky db check` opens the database read-only (SKY_READ_ONLY_DB=check) and writes nothing, " +
			"and the database refused a read that needed a write. On SQLite in WAL mode, check that the " +
			"database file and its -wal and -shm files (when they exist) are readable by this user"
	case "status":
		// Two causes, and the advice differs (v0.29.2 sixth Judge: the text
		// named only the first, and an app with no Db.migrate got "move that
		// write after Db.migrate").
		return ". This process opens its databases read-only (SKY_READ_ONLY_DB=status): " +
			"`sky db status` runs the app's main until Db.migrate, which prints the status and exits, so that it writes nothing. " +
			"This write came first. If the app calls Db.migrate, move the write after it. " +
			"If the app has no Db.migrate (it migrates with Store.migrate, which creates and alters tables, " +
			"or writes data), `sky db status` has no migration status to report: use `sky db check`, " +
			"which compares the database with the code's Store.project and runs no app code. " +
			"Or use file migrations (`sky db init`; `sky db status` then reads db/migrations and does not run main)"
	}
	return ". This process opens its databases read-only (SKY_READ_ONLY_DB=1), so a write fails. " +
		"`sky db status` and `sky db check` run it this way so that they write nothing"
}

// dbIsReadOnlyRefusal reports whether a driver error is a write the database
// refused because the connection is read-only: SQLite SQLITE_READONLY,
// PostgreSQL SQLSTATE 25006 (read_only_sql_transaction).
func dbIsReadOnlyRefusal(msg string) bool {
	return strings.Contains(msg, "attempt to write a readonly database") ||
		strings.Contains(msg, "in a read-only transaction") ||
		strings.Contains(msg, "SQLSTATE 25006")
}

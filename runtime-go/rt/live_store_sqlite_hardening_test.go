package rt

import (
	"path/filepath"
	"testing"
	"time"

	"sky-app/rt/dbshare"
)

// The Sky.Live sqlite session store must open with the same concurrency
// hardening Db.connect applies (MaxOpenConns=1 + busy_timeout + WAL). Without
// it the store was fragile: a lock held by a still-exiting process (fast
// restart, a second instance sharing the file) surfaced as a bare
// "unable to open database file (14)" and silently dropped the WHOLE store to
// memory, losing session persistence; and its unbounded pool contended on the
// WAL writer lock under Sky.Live's read+write-per-request load.
//
// This pins the fix: two stores opened on the SAME path both succeed (the
// previously-failing lock case), and each serialises on a single connection.
func TestSqliteStoreOpensUnderConcurrentHandle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")

	s1, err := newSQLiteStore(path, time.Hour, 0)
	if err != nil {
		t.Fatalf("first store open failed: %v", err)
	}
	defer s1.Close()

	// A second store on the SAME file — this is the fast-restart / dual-instance
	// case that used to fail with "unable to open database file (14)". With
	// busy_timeout + WAL it must open cleanly.
	s2, err := newSQLiteStore(path, time.Hour, 0)
	if err != nil {
		t.Fatalf("second store open on the same path failed (the lock case the "+
			"hardening fixes): %v", err)
	}
	defer s2.Close()

	// The pool is serialised to one connection (no WAL multi-conn contention).
	if got := s1.db.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("store MaxOpenConns = %d, want 1 (serialised writer)", got)
	}

	// Round-trips through the sqlite store still work.
	sess := &liveSession{}
	s1.Set("sid-1", sess)
	if _, ok := s1.Get("sid-1"); !ok {
		t.Error("session written via the hardened sqlite store was not read back")
	}
}

// The Postgres session store needs the same assertion, and did not have it.
//
// `TestDbAuxPoolIsASmallShareOfTheAppPool` checks the arithmetic of
// `dbAuxPoolConfig` and stops there: it never asks whether the number reaches a
// pool. Deleting the `applyTo` call from the store's open path therefore left
// the whole suite green while `MaxOpenConns` went back to Go's zero value,
// which means UNLIMITED — on what live_store.go's own comment calls the
// HOTTEST pool in a Sky.Live app. A spike then opens one backend per concurrent
// request until `FATAL: sorry, too many clients already`, and because every
// request begins with a session lookup, that fails EVERY request rather than
// the excess ones.
//
// No server is needed: `sql.Open` does not dial, and the pool config is
// readable from Stats() straight away.
func TestPostgresSessionPoolHasACeiling(t *testing.T) {
	want := dbSharedAuxPoolConfig().MaxOpenConns
	if want <= 0 {
		t.Fatalf("dbSharedAuxPoolConfig().MaxOpenConns is %d — this gate would assert "+
			"'unlimited == unlimited' and prove nothing", want)
	}
	dbshare.ResetForTesting()
	t.Cleanup(dbshare.ResetForTesting)

	// Port 1 is deliberately dead; nothing here connects.
	h, err := openPostgresSessionPool("postgres://sky:sky@127.0.0.1:1/sky?sslmode=disable")
	if err != nil {
		t.Fatalf("openPostgresSessionPool: %v", err)
	}
	defer h.Close()
	db := h.DB()

	if got := db.Stats().MaxOpenConnections; got != want {
		t.Errorf("the Sky.Live postgres session pool has MaxOpenConnections = %d, want %d.\n"+
			"0 means UNLIMITED: a traffic spike opens one backend per concurrent request "+
			"until the server answers `FATAL: sorry, too many clients already`, which fails "+
			"the session lookup on EVERY request.", got, want)
	}
}

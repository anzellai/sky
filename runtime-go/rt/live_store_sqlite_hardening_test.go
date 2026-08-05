package rt

import (
	"path/filepath"
	"testing"
	"time"
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

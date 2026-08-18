// live_store_cleanup_gate_test.go — the session-cleanup loop survives a panic
// and reports a failed reap.
//
//	live-session-cleanup-survives-a-panic   TestLiveSessionCleanupSurvivesAPanic
//	live-session-reap-errors-are-reported   TestLiveSessionReapErrorsAreReported
//
// # The defect
//
// Both SQL session stores ran `cleanupLoop` with no recover anywhere and
// `_, _ = s.db.Exec(DELETE FROM sky_sessions …)`. That is the full defect
// class in one loop:
//
//   - a panic out of the driver ended the goroutine for the process lifetime,
//     and
//   - a permissions failure, a locked database or a dropped table was
//     indistinguishable from a successful zero-row delete.
//
// The consequence is worse than "rows accumulate". This loop also evicts the
// memCache pointers that OWN the Time.every goroutines (Cycle 3 P36 / Gap C4),
// so a dead cleanup loop means sessions never expire on disk AND their
// subscription goroutines run forever — it compounds the Time.every defect
// rather than merely sitting beside it.
//
// Both tests drive `runCleanupLoop`, the real production loop, with an
// injected execer. The injection is the only way to reach the two behaviours
// that matter: a real driver does not panic or fail on demand, and the second
// cycle of the shipped loop is sixty seconds after the first.
//
// Fixture isolation: nothing touches the filesystem; the execer is in-memory.
package rt

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// bothSQLStores runs a subtest against each SQL store, since the two carried
// byte-identical defects and a fix to one is worthless if the other drifts.
func bothSQLStores(t *testing.T, run func(t *testing.T, loop func(liveStoreExecer), stop chan struct{})) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		stop := make(chan struct{})
		s := &sqliteStore{ttl: time.Hour, stop: stop, memCache: map[string]*liveSession{}}
		run(t, s.runCleanupLoop, stop)
	})
	t.Run("postgres", func(t *testing.T) {
		stop := make(chan struct{})
		s := &postgresStore{ttl: time.Hour, stop: stop, memCache: map[string]*liveSession{}}
		run(t, s.runCleanupLoop, stop)
	})
}

// TestLiveSessionCleanupSurvivesAPanic — a panic in a cleanup cycle costs THAT
// CYCLE, not the goroutine.
//
// The discriminating assertion is about cycles 2 and 3. "Cycle 1 panicked" is
// true under the broken and the fixed shape alike.
func TestLiveSessionCleanupSurvivesAPanic(t *testing.T) {
	restore := liveStoreCleanupInterval
	liveStoreCleanupInterval = 5 * time.Millisecond
	t.Cleanup(func() { liveStoreCleanupInterval = restore })

	n := 0
	bothSQLStores(t, func(t *testing.T, loop func(liveStoreExecer), stop chan struct{}) {
		ex := &scriptedExecer{panicOn: map[int]bool{1: true}}
		done := make(chan struct{})
		go func() {
			// Stands in for the absent recover the shipped loop had — it is
			// here so the defect lands as a failed assertion rather than as a
			// crashed test binary. Under the fixed code nothing reaches it.
			defer func() {
				_ = recover()
				close(done)
			}()
			loop(ex)
		}()

		deadline := time.Now().Add(5 * time.Second)
		for ex.count() < 3 && time.Now().Before(deadline) {
			time.Sleep(2 * time.Millisecond)
		}
		got := ex.count()
		close(stop)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the cleanup loop did not return after stop closed")
		}

		n++
		if got < 3 {
			t.Errorf("the session-cleanup loop ran %d cycle(s) after a panic in its first one, want >= 3.\n"+
				"The goroutine is dead for the process lifetime: sessions never expire on disk, and the "+
				"memCache pointers that own Time.every goroutines are never evicted, so those goroutines "+
				"run forever too. The recover must be scoped to ONE CYCLE.", got)
		}

		n++
		if _, ok := warnLogged("live.session-cleanup.sqlite.cycle_panicked"); !ok {
			if _, ok := warnLogged("live.session-cleanup.postgres.cycle_panicked"); !ok {
				t.Error("no warn logged for the panicked cleanup cycle — a recover that " +
					"discards what it caught is how a dead loop produces no evidence")
			}
		}
	})
	reportAssertions(t, n)
}

// TestLiveSessionReapErrorsAreReported — a failing reap DELETE produces a
// warn, not silence.
//
// The defect was `_, _ = s.db.Exec(...)`: a store that had never once reaped a
// session looked exactly like a healthy one, while sessions accumulated on
// disk forever.
func TestLiveSessionReapErrorsAreReported(t *testing.T) {
	restore := liveStoreCleanupInterval
	liveStoreCleanupInterval = 5 * time.Millisecond
	t.Cleanup(func() { liveStoreCleanupInterval = restore })

	n := 0
	bothSQLStores(t, func(t *testing.T, loop func(liveStoreExecer), stop chan struct{}) {
		wantErr := errors.New("attempt to write a readonly database")
		ex := &scriptedExecer{failOn: map[int]bool{1: true, 2: true, 3: true}, err: wantErr}
		done := make(chan struct{})
		go func() { loop(ex); close(done) }()

		deadline := time.Now().Add(5 * time.Second)
		for ex.count() < 2 && time.Now().Before(deadline) {
			time.Sleep(2 * time.Millisecond)
		}
		close(stop)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the cleanup loop did not return after stop closed")
		}

		n++
		sqliteEntry, okSqlite := warnLogged("live.session-cleanup.sqlite.cycle_failed")
		pgEntry, okPg := warnLogged("live.session-cleanup.postgres.cycle_failed")
		entry, ok := sqliteEntry, okSqlite
		if !ok {
			entry, ok = pgEntry, okPg
		}
		if !ok {
			t.Error("no warn logged for the failed reap — with the error discarded, a " +
				"permissions failure, a lock timeout and a successful zero-row delete " +
				"are the same observable, and sessions pile up on disk unnoticed")
		} else if !strings.Contains(entry.Fields["error"], wantErr.Error()) {
			t.Errorf("the reap warn carries %q, want the driver's error %q",
				entry.Fields["error"], wantErr.Error())
		}

		n++
		if ex.count() < 2 {
			t.Errorf("the loop ran %d cycle(s) against a permanently failing database, want >= 2 — "+
				"a failed cycle must not end the loop", ex.count())
		}
	})
	reportAssertions(t, n)
}

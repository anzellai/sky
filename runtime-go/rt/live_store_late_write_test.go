package rt

// The residual the release phase introduced, and its bound.
//
// Closing the session store after the drain is the right order — a store closed
// while the hook chain is still flushing is a store taken away from a writer —
// but it opens a window that did not exist when nothing ever called Close. The
// commit that landed the release phase disclosed it and left it ungated:
//
//	"they also do not cover the residual window after srv.Close(): a handler
//	 goroutine that has not yet issued its Set can find the store closed and
//	 log one save failure. That write was going to die with the process either
//	 way, so it is a log line and not a lost session, but it is new and it is
//	 not gated."
//
// Two claims are being made there and only the first was true. "Not a lost
// session" is right, and this file proves it rather than asserting it: a fresh
// handle opened on the file still reads every session written before the close.
//
// "ONE save failure" was not right. `Set`'s error branch was a bare `log.Printf`
// per call, so the bound was one line per late write per handler goroutine, not
// one line. A Sky.Live app draining a hundred sessions could put a hundred
// `failed to save session` lines into the log in the last second of its life —
// each one reading, to an operator watching a deploy, exactly like the data loss
// this is not. So the residual is gated at the number it actually is, which
// required making the number one.
//
// The bound is narrow on purpose. It applies ONLY when the store has already
// been closed, which is a state only the release phase produces. A save failure
// during ordinary operation — a full disk, a revoked permission, a Postgres
// gone away — still logs every time, because there the repetition IS the signal.

import (
	"bytes"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestALateSessionWriteAfterTheReleasePhaseIsALogLineNotALostSession(t *testing.T) {
	withCleanShutdownRegistries(t)
	forgetLogOnceKeys(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.db")
	store := chooseStore("sqlite", path, time.Minute, 0)
	if _, ok := store.(*sqliteStore); !ok {
		t.Fatalf("chooseStore returned %T, want *sqliteStore — against a memory fallback there "+
			"is no close for a late write to lose to", store)
	}

	// The session an ordinary request persisted before the signal arrived.
	store.Set("sid-before-shutdown", &liveSession{})

	// The termination sequence: drain, stop accepting, release. The store is
	// closed when this returns.
	drainAndRelease(2*time.Second, nil)

	// Now the handler goroutines that were in flight across srv.Close(). Several
	// of them, because "one log line" has to mean one line for the process and
	// not one line per goroutine — the distinction the disclosure got wrong.
	logs := captureLog(t)
	late := []string{"sid-late-a", "sid-late-b", "sid-late-c", "sid-late-d", "sid-late-e"}
	for _, sid := range late {
		store.Set(sid, &liveSession{}) // must not panic
	}

	// CLAIM 1 — not a lost session. What was written before the close is
	// readable by a handle that never saw the writer, which is the only
	// observation that distinguishes "persisted" from "still in the writer's
	// memory".
	fresh, err := newSQLiteStore(path, time.Minute, 0)
	if err != nil {
		t.Fatalf("cannot reopen the closed store's file: %v", err)
	}
	defer func() { _ = fresh.Close() }()
	if _, ok := fresh.Get("sid-before-shutdown"); !ok {
		t.Errorf("a fresh handle cannot read sid-before-shutdown — the late writes did not just "+
			"fail, they cost a session that WAS persisted before the release phase closed the "+
			"store at %s", path)
	}

	// …and the honest other half: the late write really did not land. This gate
	// does not claim the window is harmless, only that it costs a write the
	// process was about to abandon anyway.
	if _, ok := fresh.Get("sid-late-a"); ok {
		t.Errorf("sid-late-a is readable from a fresh handle — the store accepted a write after " +
			"Close, so either Close did not close it or the write went somewhere unexpected")
	}

	// CLAIM 2 — one log line, for the process, not for each goroutine.
	got := strings.Count(logs.String(), "sid-late")
	if got != 1 {
		t.Errorf("%d late-write log lines for %d late writes, want exactly 1 — a store closed by "+
			"the release phase is a known, single fact about the process, and one line per "+
			"in-flight handler is a burst of `failed to save session` in the last second of a "+
			"deploy that reads exactly like the data loss it is not.\nlog:\n%s",
			got, len(late), logs.String())
	}

	// …and the line has to say WHY, or a bounded log line is just a quieter
	// false alarm.
	line := logs.String()
	for _, want := range []string{"shutdown", "terminating"} {
		if !strings.Contains(strings.ToLower(line), want) {
			t.Errorf("the late-write log line does not contain %q — an operator reading "+
				"`failed to save session` during a deploy has no way to tell it from a real "+
				"persistence failure.\nline: %s", want, line)
		}
	}
}

// A save failure that is NOT the shutdown window still logs every time. The
// bound above is a statement about one specific state, and a bound that leaked
// into ordinary operation would silence the third disk-full write onwards —
// trading a burst of noise at shutdown for a blind spot in production.
func TestAnOrdinarySaveFailureIsStillLoggedEveryTime(t *testing.T) {
	forgetLogOnceKeys(t)
	logs := captureLog(t)

	const n = 4
	for i := 0; i < n; i++ {
		reportSessionSaveFailure("sqlite", "sid-disk-full", false, errDiskFullFixture)
	}

	if got := strings.Count(logs.String(), "sid-disk-full"); got != n {
		t.Errorf("%d log lines for %d ordinary save failures, want %d — outside the shutdown "+
			"window the repetition IS the signal: a store failing every write is not the same "+
			"event as a store that failed one.\nlog:\n%s", got, n, n, logs.String())
	}
}

// The link one call further out. The gate above drives the SQLite store, which
// is the one a test can close without a server; `postgresStore` and `redisStore`
// have the identical error branch and no unit test can reach theirs. So the
// decision lives in exactly one function and the three Set methods are asserted
// to reach it — the same reference-graph matcher the termination-path audit
// uses, applied to the same class of gap it exists for.
func TestEverySessionStoreReportsSaveFailuresThroughTheBoundedPath(t *testing.T) {
	g := buildShutdownGraph(shutdownAuditFiles(t, "."))

	const reporter = "reportSessionSaveFailure"
	if !g.decls[reporter] {
		t.Fatalf("package rt no longer declares %s — this gate is asserting reachability of a "+
			"name that does not exist", reporter)
	}

	for _, setter := range []string{"sqliteStore.Set", "postgresStore.Set", "redisStore.Set"} {
		if !g.decls[setter] {
			t.Errorf("package rt declares no %s — a renamed or deleted store Set is one this "+
				"gate stops watching", setter)
			continue
		}
		if !g.refs[setter][reporter] {
			t.Errorf("%s does not name %s — it reports save failures on its own, so a store "+
				"closed by the release phase logs one line per in-flight handler on that "+
				"backend. Only the SQLite path is covered by an end-to-end gate; this is what "+
				"holds the other two.", setter, reporter)
		}
	}
}

// ---------------------------------------------------------------------------

// captureLog redirects the standard logger into a buffer for the rest of the
// test. Concurrency-safe because `Set` may be called from several goroutines and
// bytes.Buffer is not.
func captureLog(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})
	return buf
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// forgetLogOnceKeys clears the process-wide once-per-key log ledger, so a test
// asserting "exactly one line" is not silently answered by a sibling test that
// already burned the key.
func forgetLogOnceKeys(t *testing.T) {
	t.Helper()
	clear := func() {
		logOnceMu.Lock()
		logOnceKeys = map[string]bool{}
		logOnceMu.Unlock()
	}
	clear()
	t.Cleanup(clear)
}

var errDiskFullFixture = errFixture("write sky_sessions: no space left on device")

type errFixture string

func (e errFixture) Error() string { return string(e) }

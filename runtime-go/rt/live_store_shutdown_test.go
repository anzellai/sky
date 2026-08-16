package rt

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// SessionStore.Close is part of the interface and every backend implements it
// idempotently — and until this gate, NOTHING on a production path called it.
// The Sky.Live signal handler flipped readyz, shut tracing down, stopped the
// jobs worker, ran the shutdown hooks and closed the HTTP server; the session
// store's cleanup goroutine and its backing handle were left to process exit.
//
// The gate observes the store through the FILESYSTEM rather than through a flag
// the store sets, because the difference a Close makes is externally visible: a
// SQLite session store runs in WAL mode (live_store.go, `PRAGMA journal_mode=
// WAL`), so every session written since the last checkpoint lives in a `-wal`
// sidecar. `db.Close()` checkpoints that WAL into the main file and removes the
// `-wal`/`-shm` pair. A process that exits without closing leaves both behind.
// Asking the filesystem is what makes this an OBSERVATION of the running store
// and not a re-evaluation of the expression the production site would run.
func TestTheShutdownPathClosesTheSessionStore(t *testing.T) {
	withCleanShutdownRegistries(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.db")
	store := chooseStore("sqlite", path, time.Minute, 0)
	if _, ok := store.(*sqliteStore); !ok {
		t.Fatalf("chooseStore returned %T, want *sqliteStore — the gate would prove nothing "+
			"against a memory fallback", store)
	}
	store.Set("sid-shutdown", &liveSession{})

	// Precondition: the write really is sitting in an un-checkpointed WAL, so
	// "the sidecar is gone" can only mean the store was closed.
	if !fileExists(path + "-wal") {
		t.Fatalf("precondition: expected an un-checkpointed %s-wal after a session write; "+
			"without one this gate cannot distinguish closed from never-closed", path)
	}

	// The termination sequence a Sky.Live app runs on SIGTERM.
	drainAndRelease(2*time.Second, nil)

	if fileExists(path + "-wal") {
		t.Errorf("the shutdown sequence left %s-wal behind — nothing on the path closed the "+
			"session store, so its WAL was never checkpointed and its handle never released",
			path)
	}
	if fileExists(path + "-shm") {
		t.Errorf("the shutdown sequence left %s-shm behind — same cause", path)
	}
}

// The ordering half, and the reason a session-store close is NOT simply another
// entry on the shutdown-hook chain.
//
// The hook chain is LIFO and drains telemetry: the hub exporter, the analytics
// writer, telemetry-persistence, observability-push. A store closed DURING that
// drain is a store taken away from callers still using it — the same defect
// shape the drain-gate fix closed one layer over. Resources are therefore
// released in a phase strictly AFTER the drain, which is what this asserts.
func TestResourcesAreReleasedAfterTheDrainHasFinished(t *testing.T) {
	withCleanShutdownRegistries(t)

	var mu sync.Mutex
	var order []string
	note := func(s string) { mu.Lock(); order = append(order, s); mu.Unlock() }

	RegisterShutdownHook("slow-drain", func(ctx context.Context) {
		time.Sleep(120 * time.Millisecond)
		note("drain-finished")
	})
	RegisterResourceCloser("a-store", func() { note("resource-closed") })

	drainAndRelease(2*time.Second, nil)

	mu.Lock()
	defer mu.Unlock()
	want := []string{"drain-finished", "resource-closed"}
	if len(order) != len(want) || order[0] != want[0] || order[1] != want[1] {
		t.Errorf("shutdown order = %v, want %v — a resource released before the drain finished "+
			"is taken away from the hooks still writing to it", order, want)
	}
}

// The SECOND-caller case, which is the one that actually bites.
//
// Every app shape installs its own signal handler and they all call
// RunShutdownHooks. The first caller claims the chain; every later caller finds
// `shutdownRan` already set and RETURNS IMMEDIATELY — with the hooks still in
// flight. A sequence that treated that return as "the drain is done" and went
// on to close the store would close it underneath the running drain. Waiting on
// the completion barrier is what makes "drained" true rather than merely called
// (the same distinction pg_embed.go's phase 2 documents).
func TestResourceReleaseWaitsForADrainAnotherGoroutineClaimed(t *testing.T) {
	withCleanShutdownRegistries(t)

	var mu sync.Mutex
	var order []string
	note := func(s string) { mu.Lock(); order = append(order, s); mu.Unlock() }

	started := make(chan struct{})
	RegisterShutdownHook("slow-drain", func(ctx context.Context) {
		close(started)
		time.Sleep(150 * time.Millisecond)
		note("drain-finished")
	})
	RegisterResourceCloser("a-store", func() { note("resource-closed") })

	// Goroutine A claims the chain.
	go RunShutdownHooks(2 * time.Second)
	<-started

	// Goroutine B (this one) arrives second: its RunShutdownHooks is a no-op.
	drainAndRelease(2*time.Second, nil)

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "drain-finished" || order[1] != "resource-closed" {
		t.Errorf("shutdown order = %v, want [drain-finished resource-closed] — the second "+
			"caller released resources while the first caller's drain was still running",
			order)
	}
}

func withCleanShutdownRegistries(t *testing.T) {
	t.Helper()
	resetShutdownHooksForTesting()
	resetResourceClosersForTesting()
	t.Cleanup(func() {
		resetShutdownHooksForTesting()
		resetResourceClosersForTesting()
	})
}

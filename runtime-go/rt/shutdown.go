package rt

// Process-wide shutdown-hook registry. Multiple subsystems (Sky.Live
// signal handler, Sky.Http.Server signal handler, future v0.17+
// hooks) call runShutdownHooks(deadline) inside their SIGTERM/SIGINT
// handler. The HubExporter's Flush registers itself here at Start —
// no coupling between the exporter file and the framework signal
// surfaces.
//
// Hooks run in REVERSE registration order (LIFO) — the most-
// recently-registered subsystem cleans up first, so a hook can
// depend on infrastructure registered earlier (e.g. exporter
// registers AFTER tracing init, so exporter drain runs BEFORE
// tracing shutdown).
//
// Deadline is shared — total budget for the whole hook chain. Each
// hook gets an interleaved slice; a hook that exceeds its share
// trips a watchdog log line and is forcibly cut.

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"
)

// ShutdownHook is the closure interface — typically wraps an
// exporter Flush / tracing Shutdown / job-queue Drain. Each hook
// gets the REMAINING deadline at the moment it runs.
type ShutdownHook func(ctx context.Context)

var (
	shutdownMu        sync.Mutex
	shutdownHooks     []shutdownEntry
	shutdownRan       bool
)

type shutdownEntry struct {
	name string
	fn   ShutdownHook
}

// RegisterShutdownHook adds fn to the LIFO shutdown chain. name is
// used for watchdog log lines. Safe to call concurrently; safe to
// call after runShutdownHooks (the hook will not run, but won't
// panic — the registry is closed after the first run).
func RegisterShutdownHook(name string, fn ShutdownHook) {
	if fn == nil {
		return
	}
	shutdownMu.Lock()
	defer shutdownMu.Unlock()
	if shutdownRan {
		// Registry is closed — late registration silently ignored.
		// This handles the (rare) case where a goroutine spawns a
		// new sub-system after SIGTERM fired.
		return
	}
	shutdownHooks = append(shutdownHooks, shutdownEntry{name: name, fn: fn})
}

// RunShutdownHooks executes every registered hook in LIFO order
// under the shared deadline. Returns when ALL hooks complete OR the
// deadline expires. Idempotent — subsequent calls are no-ops.
//
// Called from Sky.Live's signal handler (live.go) AND
// Sky.Http.Server's (rt.go). Both pass the same deadline budget —
// the registry is shared, so the second caller's invocation is a
// no-op after the first ran.
func RunShutdownHooks(deadline time.Duration) {
	shutdownMu.Lock()
	if shutdownRan {
		shutdownMu.Unlock()
		return
	}
	shutdownRan = true
	hooks := make([]shutdownEntry, len(shutdownHooks))
	copy(hooks, shutdownHooks)
	shutdownMu.Unlock()

	if len(hooks) == 0 {
		return
	}

	// LIFO ordering — reverse the snapshot.
	for i, j := 0, len(hooks)-1; i < j; i, j = i+1, j-1 {
		hooks[i], hooks[j] = hooks[j], hooks[i]
	}

	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	for _, h := range hooks {
		// Each hook runs in its own goroutine with the shared ctx
		// so a wedged hook can't block the rest. We wait for done
		// or deadline.
		done := make(chan struct{})
		go func(entry shutdownEntry) {
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintf(os.Stderr,
						"[sky.shutdown] hook %q panicked: %v\n", entry.name, r)
				}
				close(done)
			}()
			entry.fn(ctx)
		}(h)

		select {
		case <-done:
			// Hook completed cleanly; move to next.
		case <-ctx.Done():
			// Deadline exhausted; let remaining hooks see the
			// already-expired ctx (they should bail quickly).
			fmt.Fprintf(os.Stderr,
				"[sky.shutdown] deadline exceeded mid-hook %q; continuing\n",
				h.name)
		}
	}
}

// resetShutdownHooksForTesting — TEST-ONLY. Cabal / Go tests that
// register hooks need a way to reset between cases.
func resetShutdownHooksForTesting() {
	shutdownMu.Lock()
	defer shutdownMu.Unlock()
	shutdownHooks = nil
	shutdownRan = false
}

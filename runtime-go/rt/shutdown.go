//go:build !js

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
	shutdownMu    sync.Mutex
	shutdownHooks []shutdownEntry
	shutdownRan   bool
	// shutdownDone is closed when the hook chain has actually FINISHED.
	//
	// `shutdownRan` alone cannot answer "has the app drained": it is set by the
	// first caller before the hooks run, so a second caller — and there is
	// always a second caller, since each app shape installs its own signal
	// handler — returns from RunShutdownHooks while the drain is still in
	// flight. Anything that must happen strictly AFTER the drain (the embedded
	// PostgreSQL supervisor stopping the database, in pg_embed.go) waits on
	// this instead.
	shutdownDone = make(chan struct{})
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
	done := shutdownDone
	shutdownMu.Unlock()
	// Only the goroutine that actually ran the chain closes the barrier.
	defer close(done)

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

// awaitShutdownHooks blocks until the hook chain has finished, or until the
// budget expires, or returns at once if nothing ever started one.
//
// The waiting — rather than a second RunShutdownHooks call, which is a no-op
// once the first caller has claimed the chain — is what lets a caller sequence
// work strictly after the drain.
func awaitShutdownHooks(budget time.Duration) {
	shutdownMu.Lock()
	started, done := shutdownRan, shutdownDone
	shutdownMu.Unlock()
	if !started {
		return
	}
	select {
	case <-done:
	case <-time.After(budget):
		fmt.Fprintf(os.Stderr, "[sky.shutdown] drain did not finish within %s; continuing\n", budget)
	}
}

// resetShutdownHooksForTesting — TEST-ONLY. Cabal / Go tests that
// register hooks need a way to reset between cases.
func resetShutdownHooksForTesting() {
	shutdownMu.Lock()
	defer shutdownMu.Unlock()
	shutdownHooks = nil
	shutdownRan = false
	shutdownDone = make(chan struct{})
}

// ---------------------------------------------------------------------------
// The release phase — resources the drain was still using
// ---------------------------------------------------------------------------

// A resource closer is NOT a shutdown hook, and the distinction is the whole
// point of having a second registry.
//
// A hook DRAINS: it flushes telemetry, pushes the last batch, persists what is
// buffered. Hooks are the things that still WRITE while the process is on its
// way out. A resource closer RELEASES the thing they were writing to — a pooled
// database handle, a session store's cleanup goroutine, a Redis client. Put a
// release on the hook chain and its LIFO position decides whether it lands
// before or after the writers, which is a coin flip nobody wants to be
// depending on. Give it its own phase and the ordering is a property of the
// sequence instead of a property of registration order.
//
// This is the same phase separation pg_embed.go's supervisor already applies to
// the embedded database: stop accepting → drain → stop PostgreSQL. The database
// is a resource, and the reason it is stopped last is the reason these run after
// the drain.
var (
	releaseMu       sync.Mutex
	resourceClosers []namedStopper
)

// RegisterResourceCloser records something to release AFTER the drain. Called
// by whatever OWNS the resource, at the point it creates it (chooseStore for a
// Sky.Live session store), so there is no second wiring step for a caller to
// forget.
func RegisterResourceCloser(name string, fn func()) {
	if fn == nil {
		return
	}
	releaseMu.Lock()
	resourceClosers = append(resourceClosers, namedStopper{name, fn})
	releaseMu.Unlock()
}

// runResourceClosers releases every registered resource, LIFO, and drains the
// registry so a second termination path cannot double-release. A panicking
// closer is contained: the process is exiting, and one broken teardown must not
// skip the rest.
func runResourceClosers() {
	releaseMu.Lock()
	list := append([]namedStopper(nil), resourceClosers...)
	resourceClosers = nil
	releaseMu.Unlock()
	for i := len(list) - 1; i >= 0; i-- {
		func(s namedStopper) {
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintf(os.Stderr, "[sky.shutdown] resource closer %q panicked: %v\n", s.name, r)
				}
			}()
			s.fn()
		}(list[i])
	}
}

func resetResourceClosersForTesting() {
	releaseMu.Lock()
	resourceClosers = nil
	releaseMu.Unlock()
}

// drainAndRelease is the tail every app shape's termination sequence shares:
// drain, stop accepting, WAIT for the drain to have actually finished, then
// release. closeListener may be nil when the caller has already stopped
// accepting (the embedded-PostgreSQL supervisor runs its accept-stoppers first).
//
// The await is not belt-and-braces. Each app shape installs its own signal
// handler and they all call RunShutdownHooks; the first caller claims the chain
// and every later caller returns IMMEDIATELY, with the hooks still in flight.
// Releasing on that return would take the store away from the drain that is
// still writing — the defect this whole phase exists to avoid, arrived at from
// the other direction.
func drainAndRelease(budget time.Duration, closeListener func()) {
	RunShutdownHooks(budget)
	if closeListener != nil {
		closeListener()
	}
	awaitShutdownHooks(budget)
	runResourceClosers()
}

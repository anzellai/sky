// exporter_drainer_panic_gate_test.go — the telemetry drainer survives a
// panicking push, and a panicking Flush cycle still releases its caller.
//
//	exporter-drainer-survives-a-panic   TestExporterDrainerSurvivesAPanic
//	exporter-flush-releases-its-caller  TestExporterFlushReleasesCallerOnPanic
//
// # The defect
//
// The drainer's ONLY recover sat at its goroutine's top level:
//
//	go func() {
//	    defer func() {
//	        if r := recover(); r != nil { fmt.Fprintf(os.Stderr, ...) }
//	        e.markDrained()
//	        close(e.doneCh)
//	    }()
//	    e.drain(ctx)
//	}()
//
// A panic anywhere in batching or push unwound past the loop, was swallowed
// there, and ALL telemetry export stopped permanently — while `doneCh` still
// closed, so `Stop()` returned promptly and reported a clean drain. That is
// worse than a silent failure: it is a silent failure that actively lies about
// itself, and the lie is the thing an operator would have checked.
//
// The recover is now per loop iteration, inside drain(). The exit path keeps a
// recover of its own so a panic in the BOOKKEEPING still closes doneCh rather
// than deadlocking every Stop — it is unreachable from the work.
//
// # The Flush hazard, which the fix had to handle rather than inherit
//
// `case done := <-e.flushReq: drainAll(); close(done)`. A per-cycle recover
// that merely keeps the drainer alive would leave a waiting Flush caller
// blocked until its own timeout with no explanation, so `close(done)` is
// DEFERRED inside the guard. A guard has to release whoever is waiting on the
// cycle it just lost, not only survive it.
//
// Fixture isolation: no filesystem, no network — the transport is a closure.
package rt

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// panickingPushes returns a transport override that panics on its Nth call,
// plus a counter. A push panic is otherwise unreachable on demand: the real
// one would be a codec or driver defect, which is precisely the class the
// drainer has to survive rather than a thing a test can wait for.
func panickingPushes(panicOn int64) (func(context.Context, []byte) (int, error), *atomic.Int64) {
	var calls atomic.Int64
	return func(context.Context, []byte) (int, error) {
		if calls.Add(1) == panicOn {
			panic("injected transport panic inside the drainer's push")
		}
		return 200, nil
	}, &calls
}

// TestExporterDrainerSurvivesAPanic — a panic in a push costs THAT CYCLE, not
// the drainer.
//
// The discriminating assertion is that pushes kept arriving after the first
// one panicked. Under the shipped code the drainer was gone and no further
// push ever happened — while Stop() still reported a clean drain.
func TestExporterDrainerSurvivesAPanic(t *testing.T) {
	transport, calls := panickingPushes(1)
	e := NewHubExporterForTesting(transport)
	e.Start(context.Background())
	t.Cleanup(e.Stop)

	deadline := time.Now().Add(10 * time.Second)
	for calls.Load() < 3 && time.Now().Before(deadline) {
		e.Submit(KindLog, []byte(`{"body":"x"}`), SevError)
		time.Sleep(5 * time.Millisecond)
	}
	got := calls.Load()

	n := 0

	n++
	if got < 3 {
		t.Errorf("the drainer attempted %d push(es) after a panic in its first one, want >= 3.\n"+
			"The panic killed the drainer: ALL telemetry export has stopped for the lifetime "+
			"of the process, and Stop() still closes doneCh and reports a clean drain — a "+
			"silent failure that lies about itself. The recover must be scoped to ONE loop "+
			"iteration (periodic.Guard), not to the goroutine.", got)
	}

	reportAssertions(t, n)
}

// TestExporterFlushReleasesCallerOnPanic — a panicking Flush cycle still
// releases the caller waiting on it.
//
// A per-cycle recover that only keeps the loop alive would convert "the
// drainer died" into "every Flush blocks for its full budget", which on the
// SIGTERM path is an 8-second stall reported as a lost telemetry tail that was
// never actually lost.
func TestExporterFlushReleasesCallerOnPanic(t *testing.T) {
	// EVERY push panics, and the batch ticker is pushed out of reach, so the
	// cycle that panics is deterministically the FLUSH cycle rather than
	// whichever one the scheduler happened to reach first. Without both, this
	// gate passes for the wrong reason about half the time.
	panicAlways := func(context.Context, []byte) (int, error) {
		panic("injected transport panic inside the drainer's flush")
	}
	e := NewHubExporterForTesting(panicAlways)
	e.batchInt = time.Hour
	e.Start(context.Background())
	t.Cleanup(e.Stop)

	e.Submit(KindLog, []byte(`{"body":"x"}`), SevError)

	// The assertion is on Flush's VERDICT, not on how long it took. Flush
	// returns nil only when the drainer closed `done`; it returns an error
	// when the caller's own deadline expired. A duration budget cannot tell
	// those apart — a generous one passes under the defect, and that is the
	// trap this gate exists to avoid.
	result := make(chan error, 1)
	go func() { result <- e.Flush(2 * time.Second) }()

	n := 0

	n++
	select {
	case err := <-result:
		if err != nil {
			t.Errorf("Flush returned %v after its drain cycle panicked, want nil.\n"+
				"The caller was released by its OWN deadline, not by the drainer: "+
				"`close(done)` must be DEFERRED inside the guard, because a guard has to "+
				"release whoever is waiting on the cycle it just lost, not merely survive "+
				"it. Otherwise the SIGTERM path stalls for its whole budget and reports a "+
				"lost telemetry tail it did not lose.", err)
		}
	case <-time.After(10 * time.Second):
		t.Error("Flush never returned at all after its drain cycle panicked")
	}

	reportAssertions(t, n)
}

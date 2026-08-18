// pruner_panic_gate_test.go — the hub's retention pruner survives a panic.
//
//	hub-pruner-survives-a-panic   TestHubPrunerSurvivesAPanic
//
// # The defect
//
// `pruner` checked and logged runPrune's error from the day it was written and
// had NO recover at all — the exact mirror of the analytics retention pruner,
// which recovered at its goroutine's top level and discarded its error. Each
// was missing precisely what the other had, and both end the same way: hub
// retention stops for the process lifetime and telemetry_log / telemetry_metric
// / telemetry_span grow without bound.
//
// The panic is not hypothetical. runPrune drives a database/sql driver, and
// modernc's SQLite panics on a closed or nil underlying handle. A driver that
// panics once panics every interval, which is why the recover has to be scoped
// to the CYCLE — wrapping the loop turns the first panic into permanent
// silence.
//
// The gate drives the store's REAL pruner loop (runPruner) with an injected
// execer, because a real driver panics on nobody's demand and the shipped
// second cycle is an hour after the first.
//
// Fixture isolation: nothing touches the filesystem; the execer is in-memory.
package hub

import (
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"
)

// scriptedExecer is a hubPruneExecer whose Nth call panics.
type scriptedExecer struct {
	mu      sync.Mutex
	calls   int
	panicOn map[int]bool
}

func (e *scriptedExecer) Exec(_ string, _ ...any) (sql.Result, error) {
	e.mu.Lock()
	e.calls++
	n := e.calls
	e.mu.Unlock()
	if e.panicOn[n] {
		panic(fmt.Sprintf("injected driver panic on prune exec %d", n))
	}
	return driverResult{}, nil
}

func (e *scriptedExecer) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

type driverResult struct{}

func (driverResult) LastInsertId() (int64, error) { return 0, nil }
func (driverResult) RowsAffected() (int64, error) { return 0, nil }

// TestHubPrunerSurvivesAPanic — a panic in a prune cycle costs THAT CYCLE, not
// the goroutine.
//
// runPruneOn issues three DELETEs per cycle, so panicking on exec 1 panics the
// FIRST cycle. The discriminating assertion is that execs kept arriving
// afterwards: "cycle 1 panicked" is true under the broken and the fixed shape
// alike, and only a later cycle tells them apart.
func TestHubPrunerSurvivesAPanic(t *testing.T) {
	ex := &scriptedExecer{panicOn: map[int]bool{1: true}}

	s := &Store{stop: make(chan struct{})}
	s.opts.pruneInterval = 5 * time.Millisecond
	s.opts.retentionHours = 1
	s.wg.Add(1)

	done := make(chan struct{})
	go func() {
		// Stands in for the absent recover the shipped pruner had, so the
		// defect lands as a failed assertion rather than a crashed binary.
		// Under the fixed code nothing reaches it.
		defer func() {
			_ = recover()
			close(done)
		}()
		s.runPruner(ex)
	}()

	// Three DELETEs per successful cycle, one panicking exec on the first, so
	// seven execs means the first cycle panicked and at least two more ran.
	deadline := time.Now().Add(5 * time.Second)
	for ex.count() < 7 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	got := ex.count()
	close(s.stop)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the pruner did not return after stop closed")
	}

	n := 0

	n++
	if got < 7 {
		t.Errorf("the hub pruner issued %d exec(s) after a panic in its first cycle, want >= 7 "+
			"(three DELETEs per cycle, so >= 2 further cycles).\n"+
			"The goroutine is dead for the process lifetime: telemetry_log, telemetry_metric "+
			"and telemetry_span grow without bound and nothing anywhere says so. The recover "+
			"must be scoped to ONE CYCLE (periodic.Guard, via periodic.Every), never wrapped "+
			"around the ticker loop.", got)
	}

	reportAssertions(t, n)
}

func reportAssertions(t *testing.T, n int) {
	t.Helper()
	fmt.Printf("ASSERTIONS: %d\n", n)
}

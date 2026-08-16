// analytics_retention_gate_test.go — the two gates behind the analytics
// retention-pruner defect found by adversarial review on 2026-08-17.
//
//	analytics-retention-survives-a-panic   TestAnalyticsRetentionSurvivesAPanic
//	analytics-prune-errors-are-reported    TestAnalyticsPruneErrorsAreReported
//
// (The console's read path is the other half of the same review; its gates are
// in analytics_console_bounds_gate_test.go.)
//
// Each test is the body of one harness gate (see
// rust/crates/xtask/src/harness/registry.rs). Each prints an
// `ASSERTIONS: <n>` line the gate body parses, so the harness enforces an
// EXACT assertion count and a body that checked nothing reports 0 — which is a
// FAIL, never a pass.
//
// Fixture isolation: every store goes in `t.TempDir()`, which is per-process
// and per-test, so several agents' worktrees can run these at once.
package rt

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"sky-app/rt/telemetry"
)

// reportAssertions prints the count the harness gate body parses. Shared by
// every analytics observability gate.
func reportAssertions(t *testing.T, n int) {
	t.Helper()
	fmt.Printf("ASSERTIONS: %d\n", n)
}

// ── fake execer ────────────────────────────────────────────────────────────

// scriptedExecer is an analyticsPruneExecer whose Nth call panics or fails on
// demand. It exists because the two defects are only visible ACROSS cycles:
// the real pruner's second cycle is six hours after the first.
type scriptedExecer struct {
	mu      sync.Mutex
	calls   int
	panicOn map[int]bool
	failOn  map[int]bool
	err     error
}

func (e *scriptedExecer) Exec(_ string, _ ...any) (sql.Result, error) {
	e.mu.Lock()
	e.calls++
	n := e.calls
	e.mu.Unlock()
	if e.panicOn[n] {
		panic(fmt.Sprintf("injected driver panic on prune cycle %d", n))
	}
	if e.failOn[n] {
		return nil, e.err
	}
	return driver.RowsAffected(0), nil
}

func (e *scriptedExecer) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

// warnLogged reports whether a warn-level entry with `msg` reached the
// telemetry ring — the same ring the console's Logs tab reads, so "was it
// logged" means "could an operator have seen it".
func warnLogged(msg string) (telemetry.LogEntry, bool) {
	for _, e := range telemetry.Default().RecentLogs(400) {
		if e.Message == msg && e.Level == "warn" {
			return e, true
		}
	}
	return telemetry.LogEntry{}, false
}

// ── gate 1: analytics-retention-survives-a-panic ───────────────────────────

// TestAnalyticsRetentionSurvivesAPanic — a panic inside a prune cycle costs
// THAT CYCLE, not the goroutine.
//
// The defect: `recover` sat at the retention goroutine's top level, so the
// first panic unwound past the ticker loop, was swallowed, and the goroutine
// returned. Retention was then dead for the process lifetime with no log line
// — the table grew without bound and nothing anywhere said so. Six hours
// between cycles is why nobody noticed: the failure and its consequence are
// separated by a day of table growth.
//
// The assertion that matters is the one about the SECOND and THIRD cycles.
// "The first prune panicked" is true under both the broken and the fixed
// shape; only "the loop was still running afterwards" tells them apart.
func TestAnalyticsRetentionSurvivesAPanic(t *testing.T) {
	restore := analyticsRetentionInterval
	analyticsRetentionInterval = 5 * time.Millisecond
	t.Cleanup(func() { analyticsRetentionInterval = restore })

	ex := &scriptedExecer{panicOn: map[int]bool{1: true}}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		// This `recover` stands in for the one the SHIPPED code had at the
		// retention goroutine's top level. It is here so the defect's real
		// consequence — the goroutine unwinds and never ticks again — shows up
		// as a failed assertion rather than as a crashed test binary, which is
		// indistinguishable from a harness fault. Under the fixed code nothing
		// ever reaches it.
		defer func() {
			_ = recover()
			close(done)
		}()
		analyticsRetentionLoop(ex, 24*time.Hour, stop)
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
		t.Fatal("retention loop did not return after stop")
	}

	n := 0

	n++
	if got < 3 {
		t.Errorf("the retention pruner ran %d cycle(s) after a panic in its first one, want >= 3.\n"+
			"A panic has killed the goroutine: retention is now dead for the whole process "+
			"lifetime and the analytics table grows without bound. `recover` must be scoped to "+
			"ONE cycle (analyticsPruneOnce), not wrapped around the ticker loop.", got)
	}

	n++
	if entry, ok := warnLogged("analytics.retention_prune_panicked"); !ok {
		t.Error("no warn logged for the panicked prune cycle — a recover that discards what " +
			"it caught is how a dead pruner produces no evidence at all")
	} else if !strings.Contains(entry.Fields["panic"], "injected driver panic") {
		t.Errorf("panic warn carries %q, want the recovered value", entry.Fields["panic"])
	}

	reportAssertions(t, n)
}

// ── gate 2: analytics-prune-errors-are-reported ────────────────────────────

// TestAnalyticsPruneErrorsAreReported — a failing retention DELETE is warned
// about.
//
// The defect: `_, _ = db.Exec(...)`. A permissions failure, a lock timeout, a
// dropped table and a successful delete of zero rows were byte-identical from
// outside the process. A pruner that had never once deleted a row looked
// exactly like a healthy one.
func TestAnalyticsPruneErrorsAreReported(t *testing.T) {
	injected := errors.New("attempt to write a readonly database")
	ex := &scriptedExecer{failOn: map[int]bool{1: true}, err: injected}

	analyticsPruneOnce(ex, 24*time.Hour)

	n := 0

	n++
	if ex.count() != 1 {
		t.Fatalf("prune made %d Exec call(s), want exactly 1", ex.count())
	}

	n++
	entry, ok := warnLogged("analytics.retention_prune_failed")
	if !ok {
		t.Error("a failing retention DELETE produced no warn. `_, _ = db.Exec(...)` discards " +
			"the error, so a store that has never successfully pruned is indistinguishable " +
			"from a healthy one.")
	}

	n++
	if ok && !strings.Contains(entry.Fields["error"], injected.Error()) {
		t.Errorf("warn carries error=%q, want the driver's message %q — a warn that omits "+
			"WHY cannot be acted on", entry.Fields["error"], injected.Error())
	}

	reportAssertions(t, n)
}

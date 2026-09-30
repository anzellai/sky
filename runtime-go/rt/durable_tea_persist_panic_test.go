//go:build !js

package rt

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sky-app/rt/telemetry"
)

// panickyPersistWiring is a durable wiring whose persist task panics the way
// an unencodable model does (a NaN Float reaching Json.Encode). `calls`
// counts every persist that ran.
func panickyPersistWiring(marker string, calls *atomic.Int32) map[string]any {
	return map[string]any{
		"Enabled": true,
		"RunId":   "default",
		"Persist": func(runId any, model any) any {
			return func() any {
				calls.Add(1)
				panic("rt.JsonEnc_encode: cannot encode NaN (" + marker + ")")
			}
		},
	}
}

func durablePanicLogs(marker string) []telemetry.LogEntry {
	var out []telemetry.LogEntry
	for _, e := range telemetry.Default().RecentLogs(0) {
		if strings.Contains(e.Fields["panicMsg"], marker) {
			out = append(out, e)
		}
	}
	return out
}

// TestDurablePersist_PanicSuspendsTheRunAndKeepsTheProcess is the C-1c
// regression. `Durable.persist` ran on safeGo, the TERMINAL runtime's
// goroutine wrapper, which answers a panic with ExitProcess(2). So a Sky.Live
// server whose snapshot write panicked (an unencodable model: a NaN Float is
// enough) exited on the next update. Before the fix this test does not fail:
// the test binary exits with status 2.
//
// The panic must be logged classified, the run suspended (no later persist
// may overwrite the stored snapshot with a state it cannot encode), and the
// process must keep serving.
func TestDurablePersist_PanicSuspendsTheRunAndKeepsTheProcess(t *testing.T) {
	marker := fmt.Sprintf("persist-nan-%d", time.Now().UnixNano())
	var calls atomic.Int32
	d := durableCtxOf(panickyPersistWiring(marker, &calls))
	d.persist("sid-c1c", "model-with-nan")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !d.isSuspended("sid-c1c") {
		time.Sleep(5 * time.Millisecond)
	}
	if !d.isSuspended("sid-c1c") {
		t.Fatal("a persist that panicked did not suspend its run")
	}
	logs := durablePanicLogs(marker)
	if len(logs) != 1 || logs[0].Fields["panicKind"] != "JsonEncodeFailure" {
		t.Fatalf("want one JsonEncodeFailure panic log, got %d: %+v", len(logs), logs)
	}
	d.persist("sid-c1c", "next-model")
	d.persistSync("sid-c1c", "next-model")
	time.Sleep(50 * time.Millisecond)
	if n := calls.Load(); n != 1 {
		t.Fatalf("a suspended run was persisted again (%d persist calls)", n)
	}
	// Another run on the same wiring is unaffected until it fails itself.
	if d.isSuspended("sid-other") {
		t.Fatal("one run's failure suspended another run")
	}
}

// TestDurablePersistSync_PanicIsRecovered: the synchronous persists (the
// session-id rotation, and the Cli / Tui loops) recover the same way instead
// of unwinding into their caller.
func TestDurablePersistSync_PanicIsRecovered(t *testing.T) {
	marker := fmt.Sprintf("persist-sync-%d", time.Now().UnixNano())
	var calls atomic.Int32
	d := durableCtxOf(panickyPersistWiring(marker, &calls))
	if rec := recoverFrom(func() { d.persistSync("sid-sync", "m") }); rec != nil {
		t.Fatalf("persistSync let a panic escape: %v", rec)
	}
	if !d.isSuspended("sid-sync") {
		t.Fatal("persistSync did not suspend the run")
	}
	if rec := recoverFrom(func() { d.persistFixed("m") }); rec != nil {
		t.Fatalf("persistFixed let a panic escape: %v", rec)
	}
	if !d.isSuspended("default") {
		t.Fatal("persistFixed did not suspend the run")
	}
}

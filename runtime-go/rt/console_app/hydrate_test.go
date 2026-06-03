// hydrate_test.go — regression spec for hydrateInitialModel + the
// telemetry → State_*_R bridges.
//
// Pre-v0.16.1 PR10-G these tests covered an end-to-end MountInlineConsole
// HTTP rendering path too, but that bespoke surface is gone (the
// canonical Sky.Live mount handles the HTTP layer now — see
// rt.MountEmbeddedConsole). The bridges hydrateInitialModel uses to
// surface telemetry on the first render stay, so the per-bridge tests
// stay.

package console_app

import (
	"testing"
	"time"

	rt "sky-app/rt"
	"sky-app/rt/telemetry"
)

// TestHydrateInitialModel_PopulatesOverview ensures hydrateInitialModel
// fills Overview from telemetry.Default(). Strategy: reset the default
// store, inject a known counter via Inc(), call hydrateInitialModel on
// a fresh empty Model, and check the Overview reports the counter +
// non-zero buffer counts.
func TestHydrateInitialModel_PopulatesOverview(t *testing.T) {
	telemetry.ResetDefault()
	store := telemetry.Default()

	// Inject a recorded HTTP request via the counter family
	// HandleConsoleOverview reads from.
	store.Inc("sky_live_requests_total", map[string]string{"status": "200", "route": "/"})
	store.Inc("sky_live_requests_total", map[string]string{"status": "200", "route": "/"})
	store.Inc("sky_live_requests_total", map[string]string{"status": "500", "route": "/api"})

	// And inject a log entry so BufferLogUsed > 0.
	store.AppendLog(telemetry.LogEntry{
		TS:      time.Now(),
		Level:   "info",
		Message: "test entry",
	})

	// Build an empty starter Model — this is what init_ would produce
	// for a fresh request.
	starter := State_Model_R{
		LogFilter: State_emptyLogFilter(),
		Overview:  State_emptyOverview(),
	}

	got := hydrateInitialModel(starter)

	if got.Overview.RequestsTotal != 3 {
		t.Errorf("RequestsTotal: got %d, want 3", got.Overview.RequestsTotal)
	}
	// 1 of the 3 was 5xx → error rate = 1/3.
	if got.Overview.ErrorRate5xx <= 0 {
		t.Errorf("ErrorRate5xx: got %f, want > 0", got.Overview.ErrorRate5xx)
	}
	if got.Overview.BufferLogUsed < 1 {
		t.Errorf("BufferLogUsed: got %d, want >= 1", got.Overview.BufferLogUsed)
	}
	// BuiltAt / Commit / SkyVersion come from the embedded ld-flags;
	// they default to "unknown" / "dev" / "dev" — non-empty in any case.
	if got.Overview.SkyVersion == "" {
		t.Errorf("SkyVersion: got empty, want a non-empty version string")
	}
}

// TestHydrateInitialModel_PopulatesLogs ensures hydrateInitialModel
// fills Logs from telemetry.Default() filtered through the empty
// LogFilter (showDebug=false; info/warn/error true).
func TestHydrateInitialModel_PopulatesLogs(t *testing.T) {
	telemetry.ResetDefault()
	store := telemetry.Default()

	now := time.Now()
	store.AppendLog(telemetry.LogEntry{TS: now, Level: "debug", Message: "noisy debug"})
	store.AppendLog(telemetry.LogEntry{TS: now, Level: "info", Message: "real info"})
	store.AppendLog(telemetry.LogEntry{TS: now, Level: "error", Message: "an error"})

	starter := State_Model_R{
		LogFilter: State_emptyLogFilter(),
		Overview:  State_emptyOverview(),
	}
	got := hydrateInitialModel(starter)

	if len(got.Logs) != 2 {
		t.Fatalf("Logs (after default filter): got %d entries, want 2 (info+error, debug excluded)", len(got.Logs))
	}
	// Should NOT contain the debug entry's message.
	for _, l := range got.Logs {
		if l.Level == "debug" {
			t.Errorf("debug-level entry leaked through default filter: %+v", l)
		}
	}
}

// TestConsoleCurrentBuildInfo cross-checks the rt-side accessor
// returns a populated BuildInfo even with the default unset ld-flags
// (so the inline console renders dev-mode safe strings, not "" + ""
// + "").
func TestConsoleCurrentBuildInfo(t *testing.T) {
	bi := rt.ConsoleCurrentBuildInfo()
	if bi.SkyVersion == "" {
		t.Errorf("ConsoleCurrentBuildInfo.SkyVersion: got empty")
	}
	if bi.Commit == "" {
		t.Errorf("ConsoleCurrentBuildInfo.Commit: got empty")
	}
	if bi.BuiltAt == "" {
		t.Errorf("ConsoleCurrentBuildInfo.BuiltAt: got empty")
	}
}

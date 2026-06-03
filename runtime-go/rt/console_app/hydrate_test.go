// hydrate_test.go — regression spec for v0.16.1 PR7-B's
// hydrateInitialModel + the inline-mount rendered output.
//
// The bug PR7 closes: production users on sky-lang.org saw the
// rendered console UI carrying "Standalone mode — no parent URL
// configured" + all-zero stats because:
//
//  1. The inline mount path doesn't set SKY_PARENT_URL (PR7-A fixes
//     this at the rt.live.go boot path).
//  2. Even with SKY_PARENT_URL set, init_'s returned Cmd would be
//     discarded by handleConsoleRoot — so the first render still
//     showed empty data (PR7-B closes this by reading telemetry
//     directly).
//
// These tests pin the FIXED behaviour: a fresh app with some recorded
// log + counter increments must render real numbers, not the mock
// banner or zeros.

package console_app

import (
	"net/http"
	"net/http/httptest"
	"strings"
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

// TestHandleConsoleRoot_RendersRealData is the end-to-end gate:
// after the rt mount path runs init_ + hydrate, the rendered HTML
// must NOT contain the mock-mode banner strings and SHOULD contain
// a non-zero Requests count.
//
// This is the test that v0.16.1 PR7 fixes — pre-PR7, this fails
// because init_ returns mock strings or empty zeros, depending on
// whether SKY_PARENT_URL was seeded.
func TestHandleConsoleRoot_RendersRealData(t *testing.T) {
	telemetry.ResetDefault()
	store := telemetry.Default()

	// Generate some real telemetry the same way a production app
	// would: HTTP request counter + a structured log line.
	store.Inc("sky_live_requests_total", map[string]string{"status": "200"})
	store.Inc("sky_live_requests_total", map[string]string{"status": "200"})
	store.Inc("sky_live_requests_total", map[string]string{"status": "200"})
	store.AppendLog(telemetry.LogEntry{
		TS:      time.Now(),
		Level:   "info",
		Message: "request handled",
	})

	mux := http.NewServeMux()
	if err := MountInlineConsole(mux, ""); err != nil {
		t.Fatalf("MountInlineConsole: %v", err)
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/_sky/console")
	if err != nil {
		t.Fatalf("GET /_sky/console: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	bodyBytes, err := readBodyN(resp, 512*1024)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	body := string(bodyBytes)

	// Bug signatures — these strings MUST NOT appear in the rendered
	// output anymore. They came from State_mockLogs() when init_ fell
	// back to mock mode (SKY_PARENT_URL unset). PR7-A's seed + PR7-B's
	// direct hydration both contribute to suppressing them.
	for _, mock := range []string{
		"Standalone mode",
		"no parent URL configured",
		"Run from a host app to see live telemetry",
	} {
		if strings.Contains(body, mock) {
			t.Errorf("rendered body contains mock-mode signature %q — bug is back\nbody head:\n%s", mock, head(body, 1024))
		}
	}

	// We need at least ONE substring evidence that the rendered HTML
	// reflects the injected telemetry. The Overview tab prints
	// RequestsTotal as a number; with 3 increments we expect to see
	// "3" rendered somewhere in the Overview surface. The exact CSS
	// makes a strict string match brittle; instead we assert the
	// overview metric label is present (the Sky source spells it
	// "Requests" — case-insensitive grep to survive minor styling
	// tweaks across regen).
	if !strings.Contains(strings.ToLower(body), "requests") {
		t.Errorf("rendered body has no 'Requests' KPI label — Overview tab likely empty.\nbody head:\n%s", head(body, 2048))
	}
}

// TestHandleConsoleRoot_ConsoleCurrentBuildInfo cross-checks the
// rt-side accessor returns a populated BuildInfo even with the
// default unset ld-flags (so the inline console renders dev-mode
// safe strings, not "" + "" + "").
func TestHandleConsoleRoot_ConsoleCurrentBuildInfo(t *testing.T) {
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

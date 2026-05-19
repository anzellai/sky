package rt

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sky-app/rt/telemetry"
)

// Phase 1.1b — /_sky/console dashboard tests. Covers:
//   - HTML shell served at /_sky/console
//   - Each JSON API endpoint returns sane payload
//   - Auth gate (open in dev, requires admin token in prod)
//   - Serverless mode → 503
//   - Filter params on /api/logs
//   - Error grouping on /api/errors

// ─── HTML shell ────────────────────────────────────────────

func TestConsole_HTMLShell_ServedAtRoot(t *testing.T) {
	resetReadiness(t)
	withServerlessEnv(t, nil)
	resp := serveOnce(HandleConsole, http.MethodGet, "/_sky/console")
	if resp.Code != http.StatusOK {
		t.Fatalf("dev mode console should be 200, got %d", resp.Code)
	}
	if ct := resp.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("expected HTML content-type, got %q", ct)
	}
	body := resp.Body.String()
	if !strings.Contains(body, "Sky Console") {
		t.Errorf("expected 'Sky Console' in HTML body")
	}
	// Tabs present.
	for _, tab := range []string{`data-tab="overview"`, `data-tab="logs"`, `data-tab="traces"`, `data-tab="errors"`, `data-tab="metrics"`} {
		if !strings.Contains(body, tab) {
			t.Errorf("missing tab marker %s", tab)
		}
	}
	// Polling loop present.
	if !strings.Contains(body, "setInterval(refresh") {
		t.Errorf("missing polling loop in console JS")
	}
}

func TestConsole_NoCacheHeader(t *testing.T) {
	resetReadiness(t)
	withServerlessEnv(t, nil)
	resp := serveOnce(HandleConsole, http.MethodGet, "/_sky/console")
	if cc := resp.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("expected no-store cache-control, got %q", cc)
	}
}

// ─── Auth gate ─────────────────────────────────────────────

func TestConsole_DevModeOpen(t *testing.T) {
	resetReadiness(t)
	withServerlessEnv(t, nil)
	SetProductionMode(false)
	resp := serveOnce(HandleConsole, http.MethodGet, "/_sky/console")
	if resp.Code != http.StatusOK {
		t.Errorf("dev mode console should be open, got %d", resp.Code)
	}
}

func TestConsole_ProductionRequiresToken(t *testing.T) {
	resetReadiness(t)
	withServerlessEnv(t, nil)
	SetProductionMode(true)
	t.Setenv("SKY_METRICS_TOKEN", "secret-token")
	defer SetProductionMode(false)

	// No auth → 401
	resp := serveOnce(HandleConsole, http.MethodGet, "/_sky/console")
	if resp.Code != http.StatusUnauthorized {
		t.Errorf("prod console without auth should be 401, got %d", resp.Code)
	}
	if resp.Header().Get("WWW-Authenticate") == "" {
		t.Errorf("401 should carry WWW-Authenticate challenge")
	}

	// With valid token → 200
	req := httptest.NewRequest(http.MethodGet, "/_sky/console", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	resp = httptest.NewRecorder()
	HandleConsole(resp, req)
	if resp.Code != http.StatusOK {
		t.Errorf("prod console with token should be 200, got %d", resp.Code)
	}
}

func TestConsole_ServerlessReturns503(t *testing.T) {
	withServerlessEnv(t, map[string]string{"K_SERVICE": "x"})
	resetReadiness(t)
	resp := serveOnce(HandleConsole, http.MethodGet, "/_sky/console")
	if resp.Code != http.StatusServiceUnavailable {
		t.Errorf("serverless console should be 503, got %d", resp.Code)
	}
	if !strings.Contains(resp.Body.String(), "OTLP") {
		t.Errorf("503 body should hint at OTLP push, got: %s", resp.Body.String())
	}
}

// ─── /api/overview ─────────────────────────────────────────

func TestConsoleOverview_ReturnsKPIs(t *testing.T) {
	withServerlessEnv(t, nil)
	resetReadiness(t)
	resetTelemetry(t)
	telemetry.Default().Inc("sky_live_requests_total", map[string]string{"status": "200"})
	telemetry.Default().Inc("sky_live_requests_total", map[string]string{"status": "200"})
	telemetry.Default().Inc("sky_live_requests_total", map[string]string{"status": "500"})

	resp := serveOnce(HandleConsoleOverview, http.MethodGet, "/_sky/console/api/overview")
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.Code)
	}
	var ov OverviewResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &ov); err != nil {
		t.Fatalf("body not valid JSON: %v\n%s", err, resp.Body.String())
	}
	if ov.RequestsTotal != 3 {
		t.Errorf("RequestsTotal: expected 3, got %v", ov.RequestsTotal)
	}
	// 1 / 3 ≈ 0.333…
	if ov.ErrorRate5xx < 0.33 || ov.ErrorRate5xx > 0.34 {
		t.Errorf("ErrorRate5xx: expected ~1/3, got %v", ov.ErrorRate5xx)
	}
	if ov.UptimeSeconds < 0 {
		t.Errorf("UptimeSeconds should be non-negative")
	}
	if ov.SkyVersion == "" {
		t.Errorf("SkyVersion should populate from buildinfo (defaults to 'dev')")
	}
}

func TestConsoleOverview_ServerlessFlag(t *testing.T) {
	resetTelemetry(t)
	resetReadiness(t)
	// Serverless mode returns 503 from the handler before producing
	// the overview payload. Verify the gate.
	withServerlessEnv(t, map[string]string{"K_SERVICE": "x"})
	resp := serveOnce(HandleConsoleOverview, http.MethodGet, "/_sky/console/api/overview")
	if resp.Code != http.StatusServiceUnavailable {
		t.Errorf("overview API serverless gate: expected 503, got %d", resp.Code)
	}
}

// ─── /api/metrics-summary ──────────────────────────────────

func TestConsoleMetricsSummary_StructuredOutput(t *testing.T) {
	withServerlessEnv(t, nil)
	resetReadiness(t)
	resetTelemetry(t)
	telemetry.Default().Inc("sky_test", map[string]string{"k": "v"})

	resp := serveOnce(HandleConsoleMetricsSummary, http.MethodGet, "/_sky/console/api/metrics-summary")
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.Code)
	}
	var rows []map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &rows); err != nil {
		t.Fatalf("body not valid JSON: %v\n%s", err, resp.Body.String())
	}
	found := false
	for _, r := range rows {
		if r["name"] == "sky_test" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected sky_test metric in summary; got %d rows", len(rows))
	}
}

// ─── /api/logs ─────────────────────────────────────────────

func TestConsoleLogs_FiltersAndLimits(t *testing.T) {
	withServerlessEnv(t, nil)
	resetReadiness(t)
	resetTelemetry(t)
	store := telemetry.Default()
	store.AppendLog(telemetry.LogEntry{Level: "info", Message: "info A", ReqID: "r1", TS: time.Now()})
	store.AppendLog(telemetry.LogEntry{Level: "warn", Message: "warn B", ReqID: "r1", TS: time.Now()})
	store.AppendLog(telemetry.LogEntry{Level: "error", Message: "error C", ReqID: "r2", TS: time.Now()})

	// All
	resp := serveOnce(HandleConsoleLogs, http.MethodGet, "/_sky/console/api/logs")
	if resp.Code != 200 {
		t.Fatalf("logs: got %d", resp.Code)
	}
	var logs []map[string]any
	json.Unmarshal(resp.Body.Bytes(), &logs)
	if len(logs) != 3 {
		t.Errorf("expected 3 logs, got %d", len(logs))
	}

	// Filter by level
	resp = serveOnce(HandleConsoleLogs, http.MethodGet, "/_sky/console/api/logs?level=warn,error")
	json.Unmarshal(resp.Body.Bytes(), &logs)
	if len(logs) != 2 {
		t.Errorf("level filter: expected 2 logs, got %d", len(logs))
	}

	// Filter by req
	resp = serveOnce(HandleConsoleLogs, http.MethodGet, "/_sky/console/api/logs?req=r2")
	json.Unmarshal(resp.Body.Bytes(), &logs)
	if len(logs) != 1 {
		t.Errorf("req filter: expected 1 log, got %d", len(logs))
	}

	// Limit
	for i := 0; i < 50; i++ {
		store.AppendLog(telemetry.LogEntry{Level: "info", Message: "x", TS: time.Now()})
	}
	resp = serveOnce(HandleConsoleLogs, http.MethodGet, "/_sky/console/api/logs?limit=10")
	json.Unmarshal(resp.Body.Bytes(), &logs)
	if len(logs) != 10 {
		t.Errorf("limit=10: got %d", len(logs))
	}
}

// ─── /api/traces ───────────────────────────────────────────

func TestConsoleTraces_ReturnsRecent(t *testing.T) {
	withServerlessEnv(t, nil)
	resetReadiness(t)
	resetTelemetry(t)
	start := time.Now()
	telemetry.Default().AppendTrace(telemetry.TraceEntry{
		TraceID:   "abc123",
		SpanID:    "span1",
		Name:      "GET /foo",
		Kind:      "server",
		StartTime: start,
		EndTime:   start.Add(50 * time.Millisecond),
		StatusCode: "Ok",
	})
	resp := serveOnce(HandleConsoleTraces, http.MethodGet, "/_sky/console/api/traces")
	if resp.Code != 200 {
		t.Fatalf("traces: got %d", resp.Code)
	}
	var traces []map[string]any
	json.Unmarshal(resp.Body.Bytes(), &traces)
	if len(traces) != 1 {
		t.Fatalf("expected 1 trace, got %d", len(traces))
	}
	if traces[0]["name"] != "GET /foo" {
		t.Errorf("expected name 'GET /foo', got %v", traces[0]["name"])
	}
	if d := traces[0]["durationMs"].(float64); d < 49 || d > 51 {
		t.Errorf("expected duration ~50ms, got %v", d)
	}
}

// ─── /api/errors ───────────────────────────────────────────

func TestConsoleErrors_GroupsByMessage(t *testing.T) {
	withServerlessEnv(t, nil)
	resetReadiness(t)
	resetTelemetry(t)
	store := telemetry.Default()
	// Same message + error → grouped
	for i := 0; i < 5; i++ {
		store.AppendLog(telemetry.LogEntry{
			Level:    "error",
			Message:  "db_unreachable",
			ErrorStr: "dial tcp: connection refused",
			TS:       time.Now(),
		})
	}
	// Different message → separate bucket
	store.AppendLog(telemetry.LogEntry{
		Level:    "error",
		Message:  "auth_failed",
		ErrorStr: "wrong password",
		TS:       time.Now(),
	})
	// Info-level log should not appear in errors view.
	store.AppendLog(telemetry.LogEntry{
		Level: "info", Message: "boring", TS: time.Now(),
	})

	resp := serveOnce(HandleConsoleErrors, http.MethodGet, "/_sky/console/api/errors")
	if resp.Code != 200 {
		t.Fatalf("errors: got %d", resp.Code)
	}
	var rows []map[string]any
	json.Unmarshal(resp.Body.Bytes(), &rows)
	if len(rows) != 2 {
		t.Fatalf("expected 2 grouped errors, got %d: %+v", len(rows), rows)
	}
	// First row is the most-frequent (5 occurrences of db_unreachable).
	if rows[0]["message"] != "db_unreachable" {
		t.Errorf("expected db_unreachable to top the list, got %v", rows[0]["message"])
	}
	if int(rows[0]["count"].(float64)) != 5 {
		t.Errorf("expected count=5, got %v", rows[0]["count"])
	}
}

// ─── Mount integration ─────────────────────────────────────

func TestMountObservabilityEndpoints_IncludesConsole(t *testing.T) {
	resetReadiness(t)
	withServerlessEnv(t, nil)
	mux := http.NewServeMux()
	MountObservabilityEndpoints(mux)

	for _, path := range []string{
		"/_sky/console",
		"/_sky/console/api/overview",
		"/_sky/console/api/metrics-summary",
		"/_sky/console/api/logs",
		"/_sky/console/api/traces",
		"/_sky/console/api/errors",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		resp := httptest.NewRecorder()
		mux.ServeHTTP(resp, req)
		if resp.Code != http.StatusOK {
			t.Errorf("%s: expected 200, got %d body=%s",
				path, resp.Code, resp.Body.String())
		}
	}
}

func TestConsole_DisabledViaEnv(t *testing.T) {
	t.Setenv("SKY_CONSOLE_DISABLED", "1")
	mux := http.NewServeMux()
	MountConsoleEndpoints(mux)
	req := httptest.NewRequest(http.MethodGet, "/_sky/console", nil)
	resp := httptest.NewRecorder()
	mux.ServeHTTP(resp, req)
	if resp.Code != http.StatusNotFound {
		t.Errorf("opt-out should leave /_sky/console unmounted (404), got %d", resp.Code)
	}
}

// ─── isObservabilityPath covers console ──────────────────

func TestIsObservabilityPath_CoversConsoleSubroutes(t *testing.T) {
	for _, p := range []string{
		"/_sky/console",
		"/_sky/console/api/overview",
		"/_sky/console/api/logs",
		"/_sky/console/api/errors",
	} {
		if !isObservabilityPath(p) {
			t.Errorf("%s should be classed as observability (CSRF skip)", p)
		}
	}
	// Negative — unrelated path doesn't match.
	if isObservabilityPath("/api/users") {
		t.Errorf("/api/users must NOT be classed as observability")
	}
}

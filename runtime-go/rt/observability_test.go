package rt

// Tests for Phase 1.1a Step 4 — /_sky/{healthz,readyz,metrics,buildinfo}.
// Validates: endpoints mount cleanly, return expected shapes,
// production-mode auth gate fires, readyz drain on SetReady(false),
// and the Prometheus exposition format from telemetry plumbs through
// http.ResponseWriter without escaping the io.Writer interface.

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"sky-app/rt/telemetry"
)

// ─── Healthz ──────────────────────────────────────────────────

func TestHealthz_Always200(t *testing.T) {
	resp := serveOnce(HandleHealthz, http.MethodGet, "/_sky/healthz")
	if resp.Code != http.StatusOK {
		t.Errorf("healthz should always be 200, got %d", resp.Code)
	}
	if got := resp.Body.String(); got != `{"status":"ok"}` {
		t.Errorf("unexpected body: %q", got)
	}
}

// ─── Readyz ───────────────────────────────────────────────────

func TestReadyz_DefaultReady(t *testing.T) {
	resetReadiness(t)
	resp := serveOnce(HandleReadyz, http.MethodGet, "/_sky/readyz")
	if resp.Code != http.StatusOK {
		t.Errorf("readyz should default to 200, got %d body=%s",
			resp.Code, resp.Body.String())
	}
}

func TestReadyz_DrainsWhenNotReady(t *testing.T) {
	resetReadiness(t)
	SetReady(false)
	resp := serveOnce(HandleReadyz, http.MethodGet, "/_sky/readyz")
	if resp.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 in drain state, got %d", resp.Code)
	}
	if !strings.Contains(resp.Body.String(), "draining") {
		t.Errorf("expected 'draining' in body, got %q", resp.Body.String())
	}
}

func TestReadyz_RegisteredProbe_FailureBlocks(t *testing.T) {
	resetReadiness(t)
	// Register a probe that fails.
	RegisterReadinessProbe("db", func() error { return errors.New("connection refused") })
	resp := serveOnce(HandleReadyz, http.MethodGet, "/_sky/readyz")
	if resp.Code != http.StatusServiceUnavailable {
		t.Errorf("failing probe should block readyz; got %d", resp.Code)
	}
	body := resp.Body.String()
	if !strings.Contains(body, "db: connection refused") {
		t.Errorf("expected probe error in body, got %q", body)
	}
}

func TestReadyz_AllProbesPass(t *testing.T) {
	resetReadiness(t)
	RegisterReadinessProbe("db", func() error { return nil })
	RegisterReadinessProbe("sessions", func() error { return nil })
	resp := serveOnce(HandleReadyz, http.MethodGet, "/_sky/readyz")
	if resp.Code != http.StatusOK {
		t.Errorf("all-probes-pass should be 200, got %d body=%s",
			resp.Code, resp.Body.String())
	}
}

// ─── Buildinfo ────────────────────────────────────────────────

func TestBuildInfo_ReturnsJSON(t *testing.T) {
	resp := serveOnce(HandleBuildInfo, http.MethodGet, "/_sky/buildinfo")
	if resp.Code != http.StatusOK {
		t.Errorf("buildinfo should be 200, got %d", resp.Code)
	}
	var bi BuildInfo
	if err := json.Unmarshal(resp.Body.Bytes(), &bi); err != nil {
		t.Fatalf("buildinfo body not valid JSON: %v\n%s", err, resp.Body.String())
	}
	if bi.GoVersion == "" {
		t.Errorf("expected GoVersion populated, got empty")
	}
	if bi.Commit == "" || bi.SkyVersion == "" {
		t.Errorf("expected Commit + SkyVersion populated (defaults to 'dev'), got %+v", bi)
	}
}

// ─── Metrics ──────────────────────────────────────────────────

func TestMetrics_DevModeOpen(t *testing.T) {
	resetReadiness(t)
	SetProductionMode(false)
	// Seed a counter so the snapshot has user data.
	telemetry.ResetDefault()
	telemetry.Default().Inc("sky_live_requests_total",
		map[string]string{"method": "GET", "route": "/", "status": "200"})

	resp := serveOnce(HandleMetrics, http.MethodGet, "/_sky/metrics")
	if resp.Code != http.StatusOK {
		t.Fatalf("dev-mode metrics should be 200, got %d", resp.Code)
	}
	body := resp.Body.String()
	if !strings.Contains(body, "sky_live_requests_total") {
		t.Errorf("expected seeded counter in output, got:\n%s", body)
	}
	if !strings.Contains(body, "process_start_time_seconds") {
		t.Errorf("expected built-in process metric, got:\n%s", body)
	}
	if !strings.HasPrefix(resp.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("Content-Type should be Prometheus text, got %q",
			resp.Header().Get("Content-Type"))
	}
}

func TestMetrics_ProductionWithoutAuth_401(t *testing.T) {
	resetReadiness(t)
	SetProductionMode(true)
	t.Setenv("SKY_METRICS_TOKEN", "supersecret")
	defer SetProductionMode(false)

	resp := serveOnce(HandleMetrics, http.MethodGet, "/_sky/metrics")
	if resp.Code != http.StatusUnauthorized {
		t.Errorf("prod-mode without auth should be 401, got %d", resp.Code)
	}
	if resp.Header().Get("WWW-Authenticate") == "" {
		t.Errorf("401 should include WWW-Authenticate challenge")
	}
}

func TestMetrics_ProductionWithToken_200(t *testing.T) {
	resetReadiness(t)
	SetProductionMode(true)
	t.Setenv("SKY_METRICS_TOKEN", "supersecret")
	defer SetProductionMode(false)

	req := httptest.NewRequest(http.MethodGet, "/_sky/metrics", nil)
	req.Header.Set("Authorization", "Bearer supersecret")
	resp := httptest.NewRecorder()
	HandleMetrics(resp, req)
	if resp.Code != http.StatusOK {
		t.Errorf("prod-mode with valid token should be 200, got %d body=%s",
			resp.Code, resp.Body.String())
	}
}

func TestMetrics_ProductionWithWrongToken_401(t *testing.T) {
	resetReadiness(t)
	SetProductionMode(true)
	t.Setenv("SKY_METRICS_TOKEN", "supersecret")
	defer SetProductionMode(false)

	req := httptest.NewRequest(http.MethodGet, "/_sky/metrics", nil)
	req.Header.Set("Authorization", "Bearer wrongtoken")
	resp := httptest.NewRecorder()
	HandleMetrics(resp, req)
	if resp.Code != http.StatusUnauthorized {
		t.Errorf("prod-mode with wrong token should be 401, got %d", resp.Code)
	}
}

// ─── Production-mode detection heuristic ─────────────────────

// TestProductionFromEnv — the rule is "ENV unset OR matching a
// dev marker → dev; anything else → prod". The previous addr-based
// detector was removed because Docker / reverse-proxy / sidecar
// patterns all broke it in both directions.
func TestProductionFromEnv(t *testing.T) {
	saveEnv := os.Getenv("ENV")
	saveSky := os.Getenv("SKY_ENV")
	t.Cleanup(func() {
		os.Setenv("ENV", saveEnv)
		os.Setenv("SKY_ENV", saveSky)
	})

	cases := []struct {
		env, skyEnv string
		want        bool
	}{
		// Unset → dev (the bare default for local users).
		{"", "", false},
		// Explicit dev markers (case-insensitive) → dev.
		{"dev", "", false},
		{"Dev", "", false},
		{"DEVELOPMENT", "", false},
		{"local", "", false},
		// SKY_ENV fallback when ENV is unset.
		{"", "dev", false},
		{"", "local", false},
		// Anything else with ENV set → prod (bias-to-gate).
		{"production", "", true},
		{"prod", "", true},
		{"staging", "", true},
		{"qa", "", true},
		{"preview", "", true},
		{"eu-west-2", "", true},
		// ENV wins over SKY_ENV when both set.
		{"dev", "production", false},
		{"production", "dev", true},
	}
	for _, c := range cases {
		os.Setenv("ENV", c.env)
		os.Setenv("SKY_ENV", c.skyEnv)
		got := productionFromEnv()
		if got != c.want {
			t.Errorf("productionFromEnv (ENV=%q SKY_ENV=%q): got %v, want %v",
				c.env, c.skyEnv, got, c.want)
		}
	}
}

// ─── Mounting ─────────────────────────────────────────────────

func TestMountObservabilityEndpoints_AllFourRespond(t *testing.T) {
	resetReadiness(t)
	mux := http.NewServeMux()
	MountObservabilityEndpoints(mux)
	for _, ep := range []string{
		"/_sky/healthz",
		"/_sky/readyz",
		"/_sky/buildinfo",
	} {
		req := httptest.NewRequest(http.MethodGet, ep, nil)
		resp := httptest.NewRecorder()
		mux.ServeHTTP(resp, req)
		if resp.Code != http.StatusOK {
			t.Errorf("%s: expected 200, got %d", ep, resp.Code)
		}
	}
	// Metrics: dev mode is open so 200 expected.
	SetProductionMode(false)
	req := httptest.NewRequest(http.MethodGet, "/_sky/metrics", nil)
	resp := httptest.NewRecorder()
	mux.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Errorf("/_sky/metrics dev: expected 200, got %d", resp.Code)
	}
}

func TestMountObservabilityEndpoints_OptOutEnv(t *testing.T) {
	t.Setenv("SKY_OBSERVABILITY_DISABLED", "1")
	mux := http.NewServeMux()
	MountObservabilityEndpoints(mux)
	// With opt-out, the endpoint is NOT registered → 404 from mux.
	req := httptest.NewRequest(http.MethodGet, "/_sky/healthz", nil)
	resp := httptest.NewRecorder()
	mux.ServeHTTP(resp, req)
	if resp.Code != http.StatusNotFound {
		t.Errorf("opt-out should leave path unmounted (404), got %d", resp.Code)
	}
}

func TestMountObservabilityEndpoints_UserHandlerWins(t *testing.T) {
	// User mounts their own /_sky/healthz BEFORE we try. Our
	// safeMount should swallow the duplicate-registration panic
	// and leave the user's handler intact.
	mux := http.NewServeMux()
	mux.HandleFunc("/_sky/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(418)
		w.Write([]byte("teapot"))
	})
	MountObservabilityEndpoints(mux)
	req := httptest.NewRequest(http.MethodGet, "/_sky/healthz", nil)
	resp := httptest.NewRecorder()
	mux.ServeHTTP(resp, req)
	if resp.Code != 418 {
		t.Errorf("user handler should win, got %d (body=%q)", resp.Code, resp.Body.String())
	}
}

// ─── Constant-time compare ────────────────────────────────────

func TestSafeStringEqual(t *testing.T) {
	if !safeStringEqual("abc", "abc") {
		t.Errorf("equal strings should match")
	}
	if safeStringEqual("abc", "abd") {
		t.Errorf("different strings should not match")
	}
	if safeStringEqual("abc", "abcd") {
		t.Errorf("different-length strings should not match")
	}
	if !safeStringEqual("", "") {
		t.Errorf("empty strings should match")
	}
}

// ─── Helpers ──────────────────────────────────────────────────

// serveOnce runs a single HTTP request against the given handler
// and returns the response recorder.
func serveOnce(h http.HandlerFunc, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	resp := httptest.NewRecorder()
	h(resp, req)
	return resp
}

// resetReadiness re-initialises the readinessReady flag + clears
// any probes registered by previous tests. Tests that mutate
// global readiness state call this in t.Cleanup.
func resetReadiness(t *testing.T) {
	t.Helper()
	readinessReady.Store(true)
	probes := []func() error{}
	readinessProbes.Store(&probes)
	// v0.16.0 PR 3: the console auth state is snapshotted once per
	// process; reset it so each test gets a fresh resolveConsoleAuthMode
	// pass against whatever env vars it sets via t.Setenv.
	ResetConsoleAuthStateForTesting()
	t.Cleanup(func() {
		readinessReady.Store(true)
		probes := []func() error{}
		readinessProbes.Store(&probes)
		SetProductionMode(false)
		os.Unsetenv("SKY_METRICS_TOKEN")
		ResetConsoleAuthStateForTesting()
	})
}

// Compile-time check: atomic pointer load is what we think it is.
var _ = atomic.Pointer[[]func() error]{}

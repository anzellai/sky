package rt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"sky-app/rt/telemetry"
)

// Step 3 — HTTP middleware (req-id, access log, metric bumps).
// The middleware sits between the panic-recovery wrapper and the
// mux; tests verify it stamps X-Request-Id, bumps the right
// counters, observes latency, captures status / bytes-written, and
// honours the serverless / observability-disabled gates.

// ─── Request-id ───────────────────────────────────────────────

func TestMiddleware_GeneratesRequestID(t *testing.T) {
	resetTelemetry(t)
	withServerlessEnv(t, nil)
	h := ObservabilityMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Echo the context-bound req-id into the body.
		id := RequestIDFromContext(r.Context())
		w.Write([]byte(id))
	}))
	req := httptest.NewRequest(http.MethodGet, "/foo", nil)
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, req)

	hdr := resp.Header().Get("X-Request-Id")
	if hdr == "" {
		t.Errorf("X-Request-Id header missing on response")
	}
	if len(hdr) != 32 {
		t.Errorf("expected 32-char hex id, got %d chars (%q)", len(hdr), hdr)
	}
	if body := resp.Body.String(); body != hdr {
		t.Errorf("context req-id (%q) != response header (%q)", body, hdr)
	}
}

func TestMiddleware_HonoursClientRequestID(t *testing.T) {
	resetTelemetry(t)
	withServerlessEnv(t, nil)
	h := ObservabilityMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/foo", nil)
	req.Header.Set("X-Request-Id", "abc-from-client")
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, req)

	if got := resp.Header().Get("X-Request-Id"); got != "abc-from-client" {
		t.Errorf("client req-id should be honoured, got %q", got)
	}
}

func TestRequestIDFromContext_ZeroValue(t *testing.T) {
	if id := RequestIDFromContext(context.Background()); id != "" {
		t.Errorf("background context should have empty req-id, got %q", id)
	}
	if id := RequestIDFromContext(nil); id != "" {
		t.Errorf("nil context should be safe, got %q", id)
	}
}

func TestWithRequestID_RoundTrips(t *testing.T) {
	ctx := WithRequestID(context.Background(), "my-id")
	if id := RequestIDFromContext(ctx); id != "my-id" {
		t.Errorf("expected round-trip 'my-id', got %q", id)
	}
}

// ─── Metric bumps ─────────────────────────────────────────────

func TestMiddleware_BumpsRequestCounter(t *testing.T) {
	resetTelemetry(t)
	withServerlessEnv(t, nil)
	h := ObservabilityMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/users", nil)
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, req)

	// Counter should have one sample matching method/route/status.
	snap := telemetry.Default().Snapshot()
	found := false
	for _, s := range snap {
		if s.Name == "sky_live_requests_total" &&
			s.Labels["method"] == "POST" &&
			s.Labels["status"] == "201" &&
			s.Value == 1 {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected POST/201 counter sample, snapshot:\n%+v", snap)
	}
}

func TestMiddleware_ObservesLatencyHistogram(t *testing.T) {
	resetTelemetry(t)
	withServerlessEnv(t, nil)
	h := ObservabilityMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/foo", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	snap := telemetry.Default().Snapshot()
	found := false
	for _, s := range snap {
		if s.Name == "sky_live_request_seconds" && s.Count == 1 {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected histogram sample, snapshot:\n%+v", snap)
	}
}

// ─── Status / bytes capture ───────────────────────────────────

func TestStatusCapture_DefaultsTo200(t *testing.T) {
	resetTelemetry(t)
	withServerlessEnv(t, nil)
	// Handler doesn't call WriteHeader explicitly — Go defaults to 200.
	h := ObservabilityMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hi"))
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.Code)
	}
	// Counter should reflect 200.
	snap := telemetry.Default().Snapshot()
	for _, s := range snap {
		if s.Name == "sky_live_requests_total" && s.Labels["status"] != "200" {
			t.Errorf("expected status=200 in counter, got %q", s.Labels["status"])
		}
	}
}

func TestStatusCapture_BytesWritten(t *testing.T) {
	resetTelemetry(t)
	withServerlessEnv(t, nil)
	h := ObservabilityMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello world"))
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	snap := telemetry.Default().Snapshot()
	found := false
	for _, s := range snap {
		if s.Name == "sky_http_response_bytes" && s.Sum == 11 {
			found = true
		}
	}
	if !found {
		t.Errorf("expected response_bytes histogram sum=11, snapshot:\n%+v", snap)
	}
}

// ─── Skip observability endpoints ─────────────────────────────

func TestMiddleware_SkipsObservabilityEndpoints(t *testing.T) {
	resetTelemetry(t)
	withServerlessEnv(t, nil)
	h := ObservabilityMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	// /_sky/metrics scrape — must NOT bump request counter.
	req := httptest.NewRequest(http.MethodGet, "/_sky/metrics", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	snap := telemetry.Default().Snapshot()
	for _, s := range snap {
		if s.Name == "sky_live_requests_total" {
			t.Errorf("scrape against /_sky/* should not bump request counter; got %+v", s)
		}
	}
}

// ─── Route label normalisation ────────────────────────────────

// UNBOUNDED-MEMORY regression: the fallback label (used whenever
// r.Pattern is empty — every Sky.Live app, whose mux registers "/")
// used to be the first TWO raw path segments, so /users/12345 put
// the raw ID into a label value. 10k unique second segments filled
// the per-name series cap in 10k requests, permanently freezing the
// request metrics (first-10k-win, no eviction). The fallback is now
// the FIRST segment only, and only when it matches a known-safe
// route shape; anything else collapses to the "/:dynamic" sentinel.
func TestRouteLabelFor_LowCardinalityFallback(t *testing.T) {
	cases := map[string]string{
		"/":                           "/",
		"/users":                      "/users",
		"/users/123":                  "/users",
		"/users/123/orders":           "/users",
		"/users/123/orders/456/items": "/users",
		"/api/v1":                     "/api",
		"/static":                     "/static",
		"/index.html":                 "/index.html",
		// Unsafe first segments — IDs, encodings, junk — collapse.
		"/12345":            "/:dynamic",
		"/123abc":           "/:dynamic",
		"/%41%42":           "/:dynamic",
		"/a b":              "/:dynamic",
		"/" + strings.Repeat("s", 65): "/:dynamic",
	}
	for path, want := range cases {
		// Built directly rather than via httptest.NewRequest, which
		// refuses the very shapes an attacker sends ("/a b").
		req := &http.Request{Method: http.MethodGet, URL: &url.URL{Path: path}}
		got := routeLabelFor(req)
		if got != want {
			t.Errorf("routeLabelFor(%q): got %q, want %q", path, got, want)
		}
	}
}

// The bomb itself: unique numeric second segments must all land on
// ONE route label, not one series each.
func TestRouteLabel_PathIDCardinalityBombNeutralised(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		req := httptest.NewRequest(http.MethodGet, "/users/"+strconv.Itoa(i), nil)
		seen[routeLabelFor(req)] = true
	}
	if len(seen) != 1 {
		t.Errorf("1000 /users/<id> requests produced %d distinct route labels; want 1", len(seen))
	}
}

// ─── Serverless mode → stderr access log ──────────────────────

func TestMiddleware_ServerlessSkipsRingBuffer(t *testing.T) {
	withServerlessEnv(t, map[string]string{"K_SERVICE": "x"})
	resetTelemetry(t)
	h := ObservabilityMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/foo", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	// In serverless mode the access log goes to stderr, not the
	// ring buffer. So RecentLogs() should be empty.
	logs := telemetry.Default().RecentLogs(0)
	for _, l := range logs {
		if l.Message == "http_request" {
			t.Errorf("serverless mode should NOT write to ring buffer; got %+v", l)
		}
	}
	// Counters DO still bump in serverless mode (they ship via
	// OTLP push in Step 7).
	snap := telemetry.Default().Snapshot()
	gotCounter := false
	for _, s := range snap {
		if s.Name == "sky_live_requests_total" {
			gotCounter = true
		}
	}
	if !gotCounter {
		t.Errorf("serverless mode must still bump metrics counters")
	}
}

// ─── Opt-out env ──────────────────────────────────────────────

func TestMiddleware_DisabledViaEnv(t *testing.T) {
	resetTelemetry(t)
	withServerlessEnv(t, nil)
	t.Setenv("SKY_OBSERVABILITY_DISABLED", "1")
	h := ObservabilityMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/foo", nil)
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, req)

	// Disabled → no req-id header generated.
	if hdr := resp.Header().Get("X-Request-Id"); hdr != "" {
		t.Errorf("opt-out should skip req-id stamping, got %q", hdr)
	}
	// No counter bumps.
	snap := telemetry.Default().Snapshot()
	for _, s := range snap {
		if s.Name == "sky_live_requests_total" {
			t.Errorf("opt-out should skip metrics; got %+v", s)
		}
	}
}

// ─── Flush / Hijack passthrough ───────────────────────────────

func TestStatusCapture_FlushPassthrough(t *testing.T) {
	// SSE handlers need Flush to work through the middleware.
	resetTelemetry(t)
	withServerlessEnv(t, nil)
	flushed := false
	rw := &flushableRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		onFlush:          func() { flushed = true },
	}
	h := ObservabilityMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			t.Fatalf("wrapped writer should expose http.Flusher")
		}
		f.Flush()
	}))
	req := httptest.NewRequest(http.MethodGet, "/sse", nil)
	h.ServeHTTP(rw, req)
	if !flushed {
		t.Errorf("Flush did not propagate to underlying writer")
	}
}

// ─── Client IP extraction ─────────────────────────────────────

func TestClientIP_XForwardedFor(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8")
	req.RemoteAddr = "10.0.0.1:1234"
	if got := clientIP(req); got != "1.2.3.4" {
		t.Errorf("XFF first IP wins, got %q", got)
	}
}

func TestClientIP_XRealIP(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Real-IP", "9.8.7.6")
	req.RemoteAddr = "10.0.0.1:1234"
	if got := clientIP(req); got != "9.8.7.6" {
		t.Errorf("X-Real-IP should win when XFF absent, got %q", got)
	}
}

func TestClientIP_FallsBackToRemoteAddr(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	if got := clientIP(req); got != "10.0.0.1:1234" {
		t.Errorf("fallback should be RemoteAddr, got %q", got)
	}
}

// ─── helpers ──────────────────────────────────────────────────

// resetTelemetry — give each test a fresh telemetry singleton so
// counters from prior tests don't bleed.
func resetTelemetry(t *testing.T) {
	t.Helper()
	telemetry.ResetDefault()
	t.Cleanup(func() {
		telemetry.ResetDefault()
	})
}

// flushableRecorder — httptest.ResponseRecorder doesn't implement
// http.Flusher by default; we wrap it for the Flush passthrough test.
type flushableRecorder struct {
	*httptest.ResponseRecorder
	onFlush func()
}

func (f *flushableRecorder) Flush() {
	if f.onFlush != nil {
		f.onFlush()
	}
}

// Compile-time check that our middleware-wrapper supports the
// optional interfaces. If we accidentally drop one, this fails to
// compile.
var (
	_ http.Flusher  = (*statusCapture)(nil)
	_ http.Hijacker = (*statusCapture)(nil)
)

// ─── Step 2 — req-id propagation through goroutine context ────

// The middleware stamps both context AND the goroutine-local
// registry. Verify CurrentRequestID() reads the stamped id from
// inside the handler — this is the path Cmd.perform's runCmd uses
// when it captures parentReqID before spawning a Task goroutine.
func TestMiddleware_StampsGoroutineRequestID(t *testing.T) {
	resetTelemetry(t)
	withServerlessEnv(t, nil)
	var seenFromGoroutine string
	h := ObservabilityMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// runCmd reads via CurrentRequestID(), not the context —
		// because Sky kernels don't take context.Context.
		seenFromGoroutine = CurrentRequestID()
	}))
	req := httptest.NewRequest(http.MethodGet, "/foo", nil)
	req.Header.Set("X-Request-Id", "trace-xyz")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if seenFromGoroutine != "trace-xyz" {
		t.Errorf("CurrentRequestID inside handler should equal stamped id; got %q", seenFromGoroutine)
	}
}

// Verifies cleanup: after the handler returns, the goroutine's
// stamp must be cleared so the underlying net/http worker doesn't
// leak the previous request's id into the next.
func TestMiddleware_ClearsGoroutineRequestIDOnExit(t *testing.T) {
	resetTelemetry(t)
	withServerlessEnv(t, nil)
	h := ObservabilityMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/foo", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	// After ServeHTTP returns, the calling goroutine's stamp from
	// the middleware should be cleared.
	if got := CurrentRequestID(); got != "" {
		t.Errorf("expected goroutine stamp cleared after handler; got %q", got)
	}
}

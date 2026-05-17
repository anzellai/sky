package rt

// Step 7 — OTel trace export tests.
//
// Covers:
//   - InitTracer with no endpoint → noop tracer, zero-cost spans
//   - InitTracer with endpoint → real exporter (in-memory spy
//     captures emitted spans)
//   - Inbound W3C traceparent propagation (server span links to
//     client's trace)
//   - Outbound traceparent injection (Http.get/post equivalent)
//   - Sampler: VM default 1%, serverless default 100%
//   - SIGTERM shutdown flushes spans

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"sky-app/rt/telemetry"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// installSpyTracer replaces the global TracerProvider with one
// backed by an in-memory exporter; returns the exporter so tests
// can read captured spans. Re-installs the original at cleanup.
func installSpyTracer(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSyncer(exporter),
	)
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
	})
	return exporter
}

// ─── No-op tracer (default state) ─────────────────────────────

func TestTracer_NoopByDefault(t *testing.T) {
	// Fresh init with empty endpoint — should install noop.
	withServerlessEnv(t, nil)
	telemetry.InitTracer(telemetry.TracerConfig{Endpoint: ""})
	tracer := telemetry.Tracer()
	if tracer == nil {
		t.Fatal("tracer should not be nil even in no-op mode")
	}
	// Start a span — should be non-recording.
	_, span := tracer.Start(context.Background(), "test")
	if span.IsRecording() {
		t.Errorf("no-op tracer span should not be recording")
	}
	span.End() // must not panic
}

// ─── Middleware wraps requests in spans ───────────────────────

func TestMiddleware_CreatesServerSpan(t *testing.T) {
	resetTelemetry(t)
	withServerlessEnv(t, nil)
	exporter := installSpyTracer(t)

	h := ObservabilityMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	req := httptest.NewRequest(http.MethodGet, "/foo", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 emitted span, got %d", len(spans))
	}
	s := spans[0]
	if s.Name != "GET /foo" {
		t.Errorf("span name: got %q, want %q", s.Name, "GET /foo")
	}
	if s.SpanKind != trace.SpanKindServer {
		t.Errorf("expected SpanKindServer, got %v", s.SpanKind)
	}
	// Status attrs verify the end-helper plumbing.
	foundStatus := false
	for _, a := range s.Attributes {
		if string(a.Key) == "http.status_code" {
			foundStatus = true
			if a.Value.AsInt64() != 200 {
				t.Errorf("expected 200 status attr, got %d", a.Value.AsInt64())
			}
		}
	}
	if !foundStatus {
		t.Errorf("http.status_code attribute missing")
	}
}

func TestMiddleware_5xx_SetsErrorStatus(t *testing.T) {
	resetTelemetry(t)
	withServerlessEnv(t, nil)
	exporter := installSpyTracer(t)

	h := ObservabilityMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
	}))
	req := httptest.NewRequest(http.MethodGet, "/foo", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span")
	}
	if spans[0].Status.Code.String() != "Error" {
		t.Errorf("5xx response should set Error status, got %v", spans[0].Status.Code)
	}
}

func TestMiddleware_4xx_KeepsOkStatus(t *testing.T) {
	// 4xx is client error, not server error — span status stays Ok
	// per OTel HTTP semantic conventions.
	resetTelemetry(t)
	withServerlessEnv(t, nil)
	exporter := installSpyTracer(t)

	h := ObservabilityMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	req := httptest.NewRequest(http.MethodGet, "/notfound", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span")
	}
	if spans[0].Status.Code.String() == "Error" {
		t.Errorf("4xx should NOT set Error status (client's fault), got %v", spans[0].Status.Code)
	}
}

// ─── W3C traceparent propagation ─────────────────────────────

func TestMiddleware_HonoursInboundTraceparent(t *testing.T) {
	resetTelemetry(t)
	withServerlessEnv(t, nil)
	telemetry.InitTracer(telemetry.TracerConfig{Endpoint: ""}) // sets propagator
	exporter := installSpyTracer(t)

	// Client supplies a traceparent — server should adopt this
	// trace-id so distributed traces chain.
	const clientTraceparent = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"

	h := ObservabilityMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	req.Header.Set("traceparent", clientTraceparent)
	h.ServeHTTP(httptest.NewRecorder(), req)

	spans := exporter.GetSpans()
	if len(spans) == 0 {
		t.Fatal("no span emitted")
	}
	gotTraceID := spans[0].SpanContext.TraceID().String()
	if gotTraceID != "0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("server span should adopt client's trace-id; got %s", gotTraceID)
	}
}

func TestInjectTraceHeaders_OutboundCarriesTraceparent(t *testing.T) {
	resetTelemetry(t)
	withServerlessEnv(t, nil)
	telemetry.InitTracer(telemetry.TracerConfig{Endpoint: ""})
	_ = installSpyTracer(t)

	// Create a context with an active span (simulates being inside
	// a request handler).
	tracer := telemetry.Tracer()
	ctx, span := tracer.Start(context.Background(), "parent")
	defer span.End()

	// Build an outbound request and inject.
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://downstream.example/api", nil)
	InjectTraceHeaders(req)

	got := req.Header.Get("traceparent")
	if got == "" {
		t.Errorf("InjectTraceHeaders should set traceparent on outbound request")
	}
	// Format check: "00-<32hex>-<16hex>-<2hex>"
	if len(got) != 55 {
		t.Errorf("malformed traceparent %q (expected 55 chars)", got)
	}
}

// ─── Sampler defaults ────────────────────────────────────────

func TestLoadTracerConfigFromEnv_VMDefaultsTo1Percent(t *testing.T) {
	for _, env := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_TRACES_SAMPLER_ARG"} {
		t.Setenv(env, "")
	}
	cfg := telemetry.LoadTracerConfigFromEnv(false /* not serverless */)
	if cfg.SampleRate != 0.01 {
		t.Errorf("VM mode default sample rate should be 1%%; got %v", cfg.SampleRate)
	}
}

func TestLoadTracerConfigFromEnv_ServerlessDefaultsTo100Percent(t *testing.T) {
	for _, env := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_TRACES_SAMPLER_ARG"} {
		t.Setenv(env, "")
	}
	cfg := telemetry.LoadTracerConfigFromEnv(true /* serverless */)
	if cfg.SampleRate != 1.0 {
		t.Errorf("serverless default sample rate should be 100%%; got %v", cfg.SampleRate)
	}
}

func TestLoadTracerConfigFromEnv_EnvOverridesSampleRate(t *testing.T) {
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "0.25")
	cfg := telemetry.LoadTracerConfigFromEnv(false)
	if cfg.SampleRate != 0.25 {
		t.Errorf("env override should set rate to 0.25; got %v", cfg.SampleRate)
	}
}

func TestLoadTracerConfigFromEnv_HeadersParsed(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS",
		"x-honeycomb-team=abc123,x-dataset=production")
	cfg := telemetry.LoadTracerConfigFromEnv(false)
	if cfg.Headers["x-honeycomb-team"] != "abc123" {
		t.Errorf("missing honeycomb header: %+v", cfg.Headers)
	}
	if cfg.Headers["x-dataset"] != "production" {
		t.Errorf("missing dataset header: %+v", cfg.Headers)
	}
}

// ─── Cleanup safety ──────────────────────────────────────────

func TestInitTracer_NoEndpoint_StillSetsPropagator(t *testing.T) {
	// Critical for distributed tracing: a Sky service in the middle
	// of a call chain (e.g. browser → API gateway → Sky → DB) MUST
	// propagate inbound traceparent onward even when it doesn't
	// export its own spans. Otherwise the trace breaks at our hop.
	withServerlessEnv(t, nil)
	telemetry.InitTracer(telemetry.TracerConfig{Endpoint: ""})

	// Verify the propagator IS installed (extract picks up an
	// inbound traceparent even when our local tracer is no-op).
	// Use http.Header so Go's textproto canonicalisation matches
	// the OTel TraceContext propagator's expectations.
	headers := http.Header{}
	headers.Set("Traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
	carrier := propagationHeaderCarrier(headers)
	extracted := telemetry.Propagator().Extract(context.Background(), carrier)
	sc := trace.SpanContextFromContext(extracted)
	if !sc.IsValid() {
		t.Errorf("propagator should extract valid trace context from inbound headers even with no-op tracer")
	}
}

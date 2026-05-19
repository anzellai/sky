package rt

// High-level tracing helpers for Sky runtime — Phase 1.1a Step 7.
//
// Thin wrapper around go.opentelemetry.io/otel so the rest of the
// runtime doesn't need to import the SDK directly. Three families:
//
//   1. Auto-instrumentation: HTTP middleware + Msg dispatcher
//      wrap request handling in spans automatically. Users do
//      nothing.
//
//   2. Outbound propagation: rt.InjectTraceHeaders(req) is called
//      by Http.get / Http.post wrappers so downstream services
//      see the same trace.
//
//   3. Manual: rt.StartSpan / rt.EndSpan exposed via FFI for
//      user code that wants custom spans (post-v1 — wire into
//      Sky's stdlib in 1.x).

import (
	"context"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"sky-app/rt/telemetry"
)

// StartHTTPServerSpan begins a server-side span for an incoming
// HTTP request. Honours W3C traceparent if the client supplied
// one — that way distributed traces (e.g. client → API gateway →
// Sky → DB) chain correctly.
//
// Returns the updated context (with the span attached) + the span
// itself. Caller is responsible for calling span.End() — typically
// via a defer at the top of the middleware.
func StartHTTPServerSpan(r *http.Request, route string) (context.Context, trace.Span) {
	ctx := telemetry.Propagator().Extract(r.Context(),
		propagationHeaderCarrier(r.Header))
	tracer := telemetry.Tracer()
	ctx, span := tracer.Start(ctx, r.Method+" "+route,
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("http.method", r.Method),
			attribute.String("http.route", route),
			attribute.String("http.target", r.URL.Path),
			attribute.String("http.scheme", schemeOf(r)),
			attribute.String("net.peer.ip", clientIP(r)),
			attribute.String("user_agent.original", r.UserAgent()),
			attribute.String("sky.req_id", RequestIDFromContext(r.Context())),
		),
	)
	return ctx, span
}

// EndHTTPServerSpan finalises the span with response metadata.
// Sets status to Error for 5xx responses; everything else gets
// status Ok. 4xx is technically a client error but doesn't trigger
// our error status (it's the client's fault, not the server's —
// matches the OTel HTTP semantic conventions).
func EndHTTPServerSpan(span trace.Span, status int, bytesWritten int64) {
	if span == nil || !span.IsRecording() {
		span.End()
		return
	}
	span.SetAttributes(
		attribute.Int("http.status_code", status),
		attribute.Int64("http.response_content_length", bytesWritten),
	)
	if status >= 500 {
		span.SetStatus(codes.Error, "server error")
	} else {
		span.SetStatus(codes.Ok, "")
	}
	span.End()
}

// StartMsgSpan begins a child span around a Sky.Live Msg dispatch.
// Parent is the current request's span (extracted from goroutine
// context or context.Background when called outside a request).
//
// Span name uses the constructor name — e.g. "msg:Increment" —
// which gives a useful "what's slow" view in a span search UI.
func StartMsgSpan(ctx context.Context, msgName string) (context.Context, trace.Span) {
	tracer := telemetry.Tracer()
	ctx, span := tracer.Start(ctx, "msg:"+msgName,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("sky.msg.name", msgName),
			attribute.String("sky.req_id", CurrentRequestID()),
		),
	)
	return ctx, span
}

// StartCmdSpan begins a child span around a Cmd.perform Task.
// Linked to the dispatching Msg's span via the context.
func StartCmdSpan(ctx context.Context, taskName string) (context.Context, trace.Span) {
	tracer := telemetry.Tracer()
	ctx, span := tracer.Start(ctx, "cmd:"+taskName,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("sky.cmd.task", taskName),
		),
	)
	return ctx, span
}

// EndSpanWithError finalises a span with error status when err is
// non-nil. Records the error as an event (OTel convention so the
// UI surfaces it as a clickable entry inside the span).
func EndSpanWithError(span trace.Span, err error) {
	if span == nil {
		return
	}
	if err != nil && span.IsRecording() {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

// InjectTraceHeaders writes the W3C traceparent + tracestate
// headers onto an outbound HTTP request. Called by Http.get /
// Http.post wrappers so downstream services see the same trace.
//
// When tracing is disabled (no exporter configured), the
// underlying propagator is still set (W3C is configured at
// InitTracer time even for no-op exporter) so we still honour
// inbound headers + propagate them onward. This means a Sky
// service in the middle of a distributed call chain doesn't break
// trace continuity even when it doesn't export its own spans.
func InjectTraceHeaders(req *http.Request) {
	ctx := req.Context()
	telemetry.Propagator().Inject(ctx, propagationHeaderCarrier(req.Header))
}

// InjectTraceHeadersInto stamps the headers from the given context
// onto an outgoing header map. Used by the Cmd.perform path so a
// goroutine-spawned HTTP call can pass headers without holding a
// http.Request.
func InjectTraceHeadersInto(ctx context.Context, headers http.Header) {
	telemetry.Propagator().Inject(ctx, propagationHeaderCarrier(headers))
}

// ─── propagation adapter ──────────────────────────────────────

// propagationHeaderCarrier adapts http.Header to OTel's
// propagation.TextMapCarrier. The SDK ships propagation.HeaderCarrier
// for exactly this but importing it pulls a separate go module —
// reimplementing here is two lines and keeps the dep surface tight.
type propagationHeaderCarrier http.Header

func (c propagationHeaderCarrier) Get(key string) string {
	return http.Header(c).Get(key)
}

func (c propagationHeaderCarrier) Set(key, value string) {
	http.Header(c).Set(key, value)
}

func (c propagationHeaderCarrier) Keys() []string {
	out := make([]string, 0, len(c))
	for k := range c {
		out = append(out, k)
	}
	return out
}

// ─── startup ──────────────────────────────────────────────────

// InitTracingFromEnv builds a TracerConfig from env vars and
// installs it. Called once during Sky.Live / Sky.Http.Server
// startup. Idempotent for tests.
//
// When OTEL_EXPORTER_OTLP_ENDPOINT is unset, this still wires
// the W3C propagator + no-op tracer — so an unconfigured app
// still propagates inbound traceparent headers (preserving trace
// continuity even when it doesn't export its own spans).
//
// Returns error only on exporter init failure — runtime startup
// treats as non-fatal (logs + continues with noop tracer).
func InitTracingFromEnv() error {
	cfg := telemetry.LoadTracerConfigFromEnv(IsServerless())
	return telemetry.InitTracer(cfg)
}

// ShutdownTracing flushes pending spans + tears down the
// TracerProvider. Called from SIGTERM handlers. Bounded timeout
// because orchestrator grace windows are tight.
func ShutdownTracing() {
	timeout := 2 * time.Second
	if IsServerless() {
		timeout = 500 * time.Millisecond
	}
	_ = telemetry.ShutdownTracer(timeout)
}

// ─── HTTP helpers ─────────────────────────────────────────────

func schemeOf(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		return proto
	}
	return "http"
}

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
	"fmt"
	"net/http"
	"reflect"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"sky-app/rt/telemetry"
)

// skyResultError inspects a kernel return value. When it is an
// Err-shaped SkyResult (Tag == 1), it returns an `error` describing
// the ErrValue so WithSpan can mark the span failed. Returns nil for
// Ok results and for non-Result values (a kernel may return a bare
// value or a Task thunk).
//
// Generic over E/A so it reads via reflection — SkyResult[E,A] has
// no non-generic base type to assert against.
func skyResultError(v any) error {
	if v == nil {
		return nil
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Struct {
		return nil
	}
	tagF := rv.FieldByName("Tag")
	errF := rv.FieldByName("ErrValue")
	if !tagF.IsValid() || !errF.IsValid() || tagF.Kind() != reflect.Int {
		return nil
	}
	if tagF.Int() != 1 {
		return nil // Ok
	}
	ev := errF.Interface()
	if e, ok := ev.(error); ok {
		return e
	}
	return fmt.Errorf("%v", ev)
}

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

// WithSpan is the auto-instrumentation seam (observability-design.md
// Layer 2). An observable kernel — Db.* / Auth.* / Http.* / File.* /
// session store — wraps its implementation in exactly one WithSpan
// call; that is the entire per-kernel maintenance burden.
//
// It reads the calling goroutine's current trace context
// (CurrentTraceContext — set by the HTTP middleware / Msg dispatch /
// Cmd.perform spawn site), opens a CHILD span, stamps the child
// context for the duration of fn so nested kernels parent off it,
// runs fn, and finalises. The stamp is restored on exit
// (stack-disciplined defer) so sibling work on the same goroutine
// sees the right parent.
//
// `WithSpan` is Go-runtime-internal: it is never a kernel, never
// in the kernel registry, never visible in Sky source. The kernel's
// Sky-level type signature is unchanged.
//
// fn's return value flows through untouched; if it is an Err-shaped
// SkyResult the span is marked with error status.
func WithSpan(name string, kind trace.SpanKind, attrs []attribute.KeyValue, fn func() any) any {
	parent := CurrentTraceContext()
	tracer := telemetry.Tracer()
	childCtx, span := tracer.Start(parent, name,
		trace.WithSpanKind(kind),
		trace.WithAttributes(attrs...),
	)
	defer span.End()

	prev := CurrentTraceContext()
	SetGoroutineTraceContext(childCtx)
	defer SetGoroutineTraceContext(prev)

	out := fn()
	if err := skyResultError(out); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return out
}

// WithCmdSpan wraps a Cmd.perform task execution in an internal
// span (observability-design.md Tier 1). Convenience over WithSpan
// so the caller (live.go) need not import go.opentelemetry.io/otel.
func WithCmdSpan(taskName string, fn func() any) any {
	return WithSpan("cmd.perform", trace.SpanKindInternal,
		[]attribute.KeyValue{attribute.String("sky.cmd.task", taskName)},
		fn)
}

// WithMsgSpan wraps a Sky.Live Msg dispatch in an internal span
// (observability-design.md Tier 1 — the causal middle layer that
// groups DB / Http child spans under "which Msg caused them").
func WithMsgSpan(msgName string, fn func() any) any {
	return WithSpan("msg "+msgName, trace.SpanKindInternal,
		[]attribute.KeyValue{attribute.String("sky.msg", msgName)},
		fn)
}

// ─── Tier-1 auto-instrumentation convenience wrappers ─────────────
//
// One per observable-kernel family. They pick the right SpanKind +
// OTEL semantic-convention attributes so the kernel files
// (db_auth.go, http kernels, file kernels, session stores) need not
// import go.opentelemetry.io/otel directly.
//
// SECURITY: only structural metadata is captured — never bind
// values, secrets, bodies, or session contents (observability-
// design.md "safe-by-default").

// WithDbSpan wraps a DB operation. `statement` is the PARAMETERISED
// SQL (placeholders intact); bind values live in a separate args
// slice and are deliberately NOT captured.
func WithDbSpan(system, op, statement string, fn func() any) any {
	return WithSpan("db."+op, trace.SpanKindClient,
		[]attribute.KeyValue{
			attribute.String("db.system", system),
			attribute.String("db.operation", op),
			attribute.String("db.statement", statement),
		}, fn)
}

// WithAuthSpan wraps an auth operation. No email / password / token
// is ever captured — only the operation name.
func WithAuthSpan(op string, fn func() any) any {
	return WithSpan("auth."+op, trace.SpanKindInternal,
		[]attribute.KeyValue{attribute.String("sky.auth.op", op)}, fn)
}

// WithHTTPClientSpan wraps an outbound HTTP call.
func WithHTTPClientSpan(method, url string, fn func() any) any {
	return WithSpan("http "+method, trace.SpanKindClient,
		[]attribute.KeyValue{
			attribute.String("http.method", method),
			attribute.String("http.url", url),
		}, fn)
}

// WithFileSpan wraps a filesystem operation.
func WithFileSpan(op, path string, fn func() any) any {
	return WithSpan("file."+op, trace.SpanKindInternal,
		[]attribute.KeyValue{
			attribute.String("sky.file.op", op),
			attribute.String("sky.file.path", path),
		}, fn)
}

// WithSessionSpan wraps a session-store load / save.
func WithSessionSpan(op, store string, fn func() any) any {
	return WithSpan("session."+op, trace.SpanKindClient,
		[]attribute.KeyValue{
			attribute.String("sky.session.op", op),
			attribute.String("sky.session.store", store),
		}, fn)
}

// ─── Std.Trace — Sky-level opt-in API (observability-design.md L3) ─
//
// Kernel implementations behind `sky-stdlib/Std/Trace.sky`. The Sky
// signatures are fully parametric — `Trace.span : String -> Task e a
// -> Task e a` preserves `a`; the value flows through untouched.

// Trace_span wraps a Task in a named child span. Sky:
//   Trace.span : String -> Task e a -> Task e a
// Returns a Task thunk; when forced it opens the span, runs the
// inner task under it, and returns the inner task's value verbatim.
func Trace_span(name any, task any) any {
	n := AsString(name)
	capTask := task
	return func() any {
		return WithSpan(n, trace.SpanKindInternal, nil, func() any {
			return AnyTaskRun(capTask)
		})
	}
}

// Trace_event records an instantaneous event on the current span.
// Sky: Trace.event : String -> Task e ()
func Trace_event(name any) any {
	n := AsString(name)
	return func() any {
		trace.SpanFromContext(CurrentTraceContext()).AddEvent(n)
		return Ok[any, any](struct{}{})
	}
}

// Trace_attr annotates the current span with a string attribute.
// Sky: Trace.attr : String -> String -> Task e ()
func Trace_attr(key any, value any) any {
	k := AsString(key)
	v := AsString(value)
	return func() any {
		trace.SpanFromContext(CurrentTraceContext()).
			SetAttributes(attribute.String("sky.trace."+k, v))
		return Ok[any, any](struct{}{})
	}
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
	// Register the in-process span ring BEFORE InitTracer so spans
	// reach the Sky Console even with no OTLP endpoint configured
	// (observability-design.md "useful by default").
	registerTraceRing()
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

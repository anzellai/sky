package telemetry

// The trace sampler must not be attacker-overridable. An inbound
// W3C `traceparent` header is unauthenticated wire input; with a
// plain sdktrace.ParentBased sampler, sampled=01 on that header
// forced 100% sampling (export volume + span-ring churn dictated by
// the client), and sampled=00 suppressed sampling of the attacker's
// own requests. Sky's sampler applies the configured ratio to
// REMOTE parents in both directions; in-process (local) parenting
// keeps inheriting, so trace trees stay whole. The escape hatch for
// deployments behind a trusted head-sampling gateway is
// SKY_TRACE_HONOR_REMOTE_PARENT=1.

import (
	"context"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func remoteParentCtx(sampled bool) context.Context {
	flags := trace.TraceFlags(0)
	if sampled {
		flags = trace.FlagsSampled
	}
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10},
		SpanID:     trace.SpanID{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08},
		TraceFlags: flags,
		Remote:     true,
	})
	return trace.ContextWithRemoteSpanContext(context.Background(), sc)
}

func startWith(t *testing.T, sampler sdktrace.Sampler, ctx context.Context) trace.Span {
	t.Helper()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sampler))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	_, span := tp.Tracer("probe").Start(ctx, "probe")
	span.End()
	return span
}

func TestSampler_RemoteSampledParentCannotForceSampling(t *testing.T) {
	t.Setenv("SKY_TRACE_HONOR_REMOTE_PARENT", "")
	// Ratio 0: nothing should sample — even when the attacker sends
	// traceparent ...-01.
	span := startWith(t, skySampler(0), remoteParentCtx(true))
	if span.SpanContext().IsSampled() {
		t.Fatal("remote sampled=01 traceparent forced sampling at ratio 0 — the sampler is attacker-overridable")
	}
}

func TestSampler_RemoteUnsampledParentCannotSuppressSampling(t *testing.T) {
	t.Setenv("SKY_TRACE_HONOR_REMOTE_PARENT", "")
	// Ratio 1: everything should sample — even when the attacker
	// sends traceparent ...-00 to hide their own requests.
	span := startWith(t, skySampler(1), remoteParentCtx(false))
	if !span.SpanContext().IsSampled() {
		t.Fatal("remote sampled=00 traceparent suppressed sampling at ratio 1 — attackers can hide from tracing")
	}
}

func TestSampler_LocalParentStillInherits(t *testing.T) {
	t.Setenv("SKY_TRACE_HONOR_REMOTE_PARENT", "")
	// In-process parenting is trusted: a sampled local parent keeps
	// its children even at ratio 0, so trace trees stay whole.
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(skySampler(0)))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	tr := tp.Tracer("probe")
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0xaa, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
		SpanID:     trace.SpanID{0xaa, 1, 2, 3, 4, 5, 6, 7},
		TraceFlags: trace.FlagsSampled,
		// Remote NOT set — a local in-process parent.
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)
	_, child := tr.Start(ctx, "child")
	child.End()
	if !child.SpanContext().IsSampled() {
		t.Fatal("local sampled parent no longer inherited — in-process trace trees would fragment")
	}
}

func TestSampler_EnvOptInHonoursRemoteParent(t *testing.T) {
	t.Setenv("SKY_TRACE_HONOR_REMOTE_PARENT", "1")
	span := startWith(t, skySampler(0), remoteParentCtx(true))
	if !span.SpanContext().IsSampled() {
		t.Fatal("SKY_TRACE_HONOR_REMOTE_PARENT=1 must restore parent-based inheritance for trusted gateways")
	}
}

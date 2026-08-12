package rt

// trace_ring_processor.go — bridges OTEL spans into the Sky Console
// in-process trace ring.
//
// observability-design.md "useful by default": a dev who sets no
// OTLP endpoint must still see a trace tree in /_sky/console. The
// OTEL SDK on its own only exports to a configured collector — with
// no endpoint it falls back to a noop tracer and every span
// vanishes.
//
// traceRingProcessor is an `sdktrace.SpanProcessor` that, on every
// span end, converts the finished span to a telemetry.TraceEntry
// and appends it to the local ring (RecordTrace). It is registered
// via telemetry.RegisterSpanProcessor before InitTracer runs, so
// the SDK wires it into the TracerProvider in BOTH modes — endpoint
// configured (alongside the OTLP exporter) and not (as the sole
// processor). Either way, WithSpan-created spans land in the
// Console.

import (
	"context"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"sky-app/rt/telemetry"
)

type traceRingProcessor struct{}

// OnStart — no-op; the ring records only completed spans.
func (traceRingProcessor) OnStart(context.Context, sdktrace.ReadWriteSpan) {}

// OnEnd converts the finished span to a TraceEntry and appends it to
// the Console trace ring (+ pushes to the parent when running as a
// sub-app — RecordTrace handles that).
func (traceRingProcessor) OnEnd(s sdktrace.ReadOnlySpan) {
	sc := s.SpanContext()
	parentID := ""
	if p := s.Parent(); p.HasSpanID() {
		parentID = p.SpanID().String()
	}
	attrs := make(map[string]string)
	for _, kv := range s.Attributes() {
		attrs[string(kv.Key)] = kv.Value.Emit()
	}
	// Normalise the status to the Console's convention: lowercase
	// "ok" / "error". OTEL's Code.String() yields "Unset" / "Ok" /
	// "Error" — the Console treats anything ≠ "ok" as a failure, so
	// emitting the raw OTEL string would paint EVERY span red. An
	// Unset span (the common case — a span that simply didn't error)
	// is a success, not an error.
	statusCode := "ok"
	if s.Status().Code == codes.Error {
		statusCode = "error"
	}
	RecordTrace(telemetry.TraceEntry{
		TraceID:       sc.TraceID().String(),
		SpanID:        sc.SpanID().String(),
		ParentID:      parentID,
		Name:          s.Name(),
		Kind:          s.SpanKind().String(),
		StartTime:     s.StartTime(),
		EndTime:       s.EndTime(),
		Attributes:    attrs,
		StatusCode:    statusCode,
		StatusMessage: s.Status().Description,
	})
}

// Shutdown / ForceFlush — nothing to drain; RecordTrace is synchronous.
func (traceRingProcessor) Shutdown(context.Context) error   { return nil }
func (traceRingProcessor) ForceFlush(context.Context) error { return nil }

// registerTraceRing wires the in-process processor into the OTEL
// SDK. Called once from InitTracingFromEnv before telemetry.InitTracer.
func registerTraceRing() {
	telemetry.RegisterSpanProcessor(traceRingProcessor{})
}

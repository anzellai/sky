package telemetry_test

// Gates for Tracer()'s provider-identity cache.
//
// The defect: Tracer() called otel.GetTracerProvider().Tracer("sky-app") on
// every span, and sdktrace's TracerProvider.Tracer takes a provider-global
// mutex to guard its named-tracer map. A single interaction reaches it several
// times (server span, msg span, and each db/http/auth/file kernel span), so a
// mutex profile at GOMAXPROCS=8 put 6.0% of all contention there. It is paid
// even with no OTLP endpoint, because the Sky Console trace ring registers a
// span processor unconditionally, which installs a real SDK provider.
//
// The cache is keyed on provider IDENTITY because an EARLIER flat cache was
// removed for going stale when tests swap the provider. That property is the
// point of TestTracerCache_SwapIsPickedUp — it is the regression the fix must
// not reintroduce.
//
// Falsifying mutation: make Tracer() ignore the provider and memoise flatly
// (e.g. `if c := tracerCache.Load(); c != nil { return c.tracer }`) — the swap
// test goes red.

import (
	"testing"

	"sky-app/rt/telemetry"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"
	"go.opentelemetry.io/otel/trace/noop"
)

// TestTracerCache_SwapIsPickedUp — the property that caused the previous cache
// to be deleted. Swapping the global provider must be visible on the very next
// Tracer() call, with no explicit invalidation from the swapper.
func TestTracerCache_SwapIsPickedUp(t *testing.T) {
	orig := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(orig) })

	// Prime the cache against a noop provider.
	otel.SetTracerProvider(noop.NewTracerProvider())
	if _, span := telemetry.Tracer().Start(t.Context(), "before"); span.IsRecording() {
		span.End()
		t.Fatal("noop provider produced a recording span")
	}

	// Swap in a recording SDK provider. Nothing tells telemetry about it.
	rec := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))

	_, span := telemetry.Tracer().Start(t.Context(), "after")
	if !span.IsRecording() {
		t.Fatal("provider swap was not picked up — Tracer() served a stale " +
			"cached tracer, which is exactly why the previous cache was removed")
	}
	span.End()

	if got := len(rec.Ended()); got != 1 {
		t.Fatalf("swapped-in recorder captured %d spans, want 1", got)
	}
}

// TestTracerCache_StableProviderReturnsSameTracer — the fix itself. With the
// provider unchanged, repeated calls must return the identical tracer, which is
// what keeps them off the provider's mutex.
func TestTracerCache_StableProviderReturnsSameTracer(t *testing.T) {
	orig := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(orig) })

	otel.SetTracerProvider(sdktrace.NewTracerProvider())
	first := telemetry.Tracer()
	for i := 0; i < 100; i++ {
		if got := telemetry.Tracer(); got != first {
			t.Fatalf("call %d returned a different tracer — the cache is not "+
				"holding, so every span still takes the provider's mutex", i)
		}
	}
}

// TestTracerCache_NonComparableProviderDoesNotPanic — Tracer() compares two
// interface values, which panics when their dynamic types are identical and
// non-comparable. The cache is therefore only allowed to store comparable
// providers. Sky's contract is that well-typed code cannot panic at runtime, so
// a third-party provider carrying a map field must not be able to take the
// process down through the tracing path.
func TestTracerCache_NonComparableProviderDoesNotPanic(t *testing.T) {
	orig := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(orig) })

	otel.SetTracerProvider(nonComparableProvider{fields: map[string]string{"a": "b"}})
	for i := 0; i < 3; i++ {
		if telemetry.Tracer() == nil {
			t.Fatal("Tracer() returned nil for a non-comparable provider")
		}
	}
}

// nonComparableProvider has a map field, so `==` on two of them panics.
// The embedded.TracerProvider is otel's forward-compatibility guard: the
// TracerProvider interface carries an unexported method, so an implementation
// outside the otel module must embed it.
type nonComparableProvider struct {
	embedded.TracerProvider
	fields map[string]string
}

func (p nonComparableProvider) Tracer(name string, opts ...trace.TracerOption) trace.Tracer {
	return noop.NewTracerProvider().Tracer(name, opts...)
}

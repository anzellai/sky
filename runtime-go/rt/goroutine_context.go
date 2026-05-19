package rt

// Goroutine-local trace-context propagation.
//
// Sky's `Task` kernel is an opaque thunk `func() any` — it carries
// no context parameter, and threading `context.Context` through
// every kernel signature would be a sweeping breaking change across
// the FFI surface. For observability we instead keep an out-of-band
// per-goroutine store: the spawn site of any goroutine that should
// inherit a parent trace stamps the parent's `context.Context` on
// entry and clears it on exit.
//
// The stored value is a full `context.Context` (not just a
// request-id string): it carries the OTEL span (traceID / spanID /
// sampled bit) AND the Sky request-id. `CurrentTraceContext()` hands
// it to `WithSpan` so an auto-instrumented kernel (Db.* / Auth.* /
// Http.* / File.* / session store) opens its span as a CHILD of
// whatever span is active on the calling goroutine — without any
// kernel signature carrying a ctx parameter.
//
// Goroutine ID is parsed from `runtime.Stack` output. This is the
// standard non-blessed approach used by every Go observability
// library (otel-go, datadog, honeycomb, sentry, …). It is stable
// across Go versions because the runtime emits
// "goroutine <gid> [<state>]:" as the first stack-trace line, a
// format that is part of Go's effective ABI.
//
// RELIABILITY CONTRACT: every goroutine-spawn site in the runtime
// must wrap its child in `RunWithTraceContext` (or the no-op when
// there is no parent). A CI grep-gate forbids a bare `go func` in
// `runtime-go/rt/` outside the blessed spawn helpers so a future
// un-wrapped spawn fails the build rather than silently dropping
// the trace.

import (
	"context"
	"runtime"
	"strconv"
	"sync"
)

// goroutineCtx stores per-goroutine trace context. Sized to hold
// every active Cmd.perform goroutine + every SSE subscription tick.
// At typical workloads (~1000 concurrent goroutines) the map fits
// in well under 1 MB.
var goroutineCtx sync.Map // map[int64]context.Context

// CurrentTraceContext returns the trace context stamped on the
// calling goroutine, or context.Background() when none is set.
// Safe from any goroutine; cheap (one sync.Map.Load + a
// goroutine-ID parse).
//
// `WithSpan` calls this to find the parent span for an
// auto-instrumented kernel.
func CurrentTraceContext() context.Context {
	gid := currentGoroutineID()
	if v, ok := goroutineCtx.Load(gid); ok {
		if ctx, ok := v.(context.Context); ok && ctx != nil {
			return ctx
		}
	}
	return context.Background()
}

// SetGoroutineTraceContext stamps the calling goroutine with a
// trace context. Pairs with `defer ClearGoroutineTraceContext()`.
// Passing a nil ctx deletes the stamp.
func SetGoroutineTraceContext(ctx context.Context) {
	gid := currentGoroutineID()
	if ctx == nil {
		goroutineCtx.Delete(gid)
		return
	}
	goroutineCtx.Store(gid, ctx)
}

// ClearGoroutineTraceContext removes the calling goroutine's stamp.
// Must run (via defer) at the top of any goroutine that called
// SetGoroutineTraceContext, so the sync.Map doesn't accumulate
// entries for spawned-and-exited goroutines.
func ClearGoroutineTraceContext() {
	gid := currentGoroutineID()
	goroutineCtx.Delete(gid)
}

// RunWithTraceContext is the canonical goroutine-spawn pattern.
// Equivalent to:
//
//	go func() {
//	    SetGoroutineTraceContext(ctx)
//	    defer ClearGoroutineTraceContext()
//	    fn()
//	}()
//
// Encapsulating the pair makes it impossible to forget the defer.
// A nil ctx degrades to running fn() with no stamp (no-op
// propagation) rather than crashing.
func RunWithTraceContext(ctx context.Context, fn func()) {
	if ctx != nil {
		SetGoroutineTraceContext(ctx)
		defer ClearGoroutineTraceContext()
	}
	fn()
}

// ─── request-id compatibility shims ───────────────────────────────
//
// The trace context subsumes the request-id (it lives inside the
// ctx via WithRequestID / RequestIDFromContext). These shims keep
// the existing CurrentRequestID-style call sites working unchanged.

// CurrentRequestID returns the request-id of the calling
// goroutine's trace context, or "" when none is set.
func CurrentRequestID() string {
	return RequestIDFromContext(CurrentTraceContext())
}

// SetGoroutineRequestID stamps the calling goroutine with a context
// carrying just the given request-id. Prefer SetGoroutineTraceContext
// when a full ctx (with the OTEL span) is available — this shim
// exists for call sites that only have a bare id.
//
// Passing id == "" deletes the stamp.
func SetGoroutineRequestID(id string) {
	if id == "" {
		SetGoroutineTraceContext(nil)
		return
	}
	SetGoroutineTraceContext(WithRequestID(context.Background(), id))
}

// ClearGoroutineRequestID removes the calling goroutine's stamp.
// Alias of ClearGoroutineTraceContext for back-compat.
func ClearGoroutineRequestID() {
	ClearGoroutineTraceContext()
}

// RunWithRequestID is the request-id-only spawn pattern. Prefer
// RunWithTraceContext when a full ctx is available.
func RunWithRequestID(id string, fn func()) {
	if id != "" {
		SetGoroutineRequestID(id)
		defer ClearGoroutineTraceContext()
	}
	fn()
}

// ─── goroutine-ID parse ────────────────────────────────────────────

// currentGoroutineID parses the calling goroutine's ID from the
// stack header. runtime.Stack(buf, false) writes
//
//	"goroutine <gid> [<state>]:\n\t<frames>...\n"
//
// as the first line. A 64-byte buffer is always enough for the
// header (gid ≤ ~20 decimal digits, state a short word).
//
// Cost: ~150 ns per call on M1 — paid per goroutine spawn (one
// stamp + one clear), not per span. Well below the per-Task budget.
func currentGoroutineID() int64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	if n < 11 {
		return 0
	}
	line := buf[10:n] // skip "goroutine " prefix
	end := 0
	for end < len(line) && line[end] >= '0' && line[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	id, err := strconv.ParseInt(string(line[:end]), 10, 64)
	if err != nil {
		return 0
	}
	return id
}

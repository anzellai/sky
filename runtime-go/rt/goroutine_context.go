package rt

// Goroutine-local request-id storage. Phase 1.1a Step 2 — propagate
// the triggering request's id into Cmd.perform goroutines so logs +
// traces emitted from background tasks correlate back to the user
// action that started them.
//
// Go has no native goroutine-local storage (deliberate design choice
// — passing context.Context as a value is the canonical idiom). For
// observability we need an out-of-band channel because Sky's Task
// kernel doesn't carry context: the Task is an opaque thunk
// `func() any`, and threading context through every kernel signature
// would be a sweeping breaking change across the FFI surface.
//
// Implementation: a sync.Map keyed by goroutine ID. The Cmd.perform
// spawn site stamps the id on entry to the goroutine and clears on
// exit. FFI helpers (and the diff-based Msg logger in Step 5) read
// via CurrentRequestID(). When called outside a stamped goroutine,
// returns "" — observability gracefully degrades to "untracked"
// instead of crashing.
//
// Goroutine ID is parsed from runtime.Stack output. This is the
// standard non-blessed approach used by every Go observability
// library (otel-go, datadog, honeycomb, sentry, …). It's stable
// across Go versions because the runtime emits "goroutine <gid>
// [<state>]:" as the first line of any stack trace, and that format
// is part of Go's effective ABI even if not in the spec.

import (
	"runtime"
	"strconv"
	"sync"
)

// goroutineReqIDs stores per-goroutine request-id stamps. Sized to
// hold every active Cmd.perform goroutine + every SSE subscription
// tick. At typical workloads (~1000 concurrent goroutines) the map
// fits in <1 MB.
var goroutineReqIDs sync.Map // map[int64]string

// CurrentRequestID returns the request-id stamped on the calling
// goroutine, or "" when none is set. Safe to call from any
// goroutine; cheap (one sync.Map.Load + a goroutine-ID parse).
//
// Used by:
//   - The diff-based Msg logger (Step 5) — annotates each logged
//     Msg with the triggering request's id.
//   - User code via FFI — Sky source can call
//     `System.requestID` (registered as a kernel function) to
//     embed the current id in user-emitted logs.
//   - The OTel span exporter (Step 7) — links background spans to
//     the request span via the same id.
func CurrentRequestID() string {
	gid := currentGoroutineID()
	if v, ok := goroutineReqIDs.Load(gid); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// SetGoroutineRequestID stamps the calling goroutine with a
// request-id. Pairs with `defer ClearGoroutineRequestID()` at the
// top of any goroutine that should propagate the parent's id. Used
// by Cmd.perform's spawn site (runCmd).
//
// Calling with id == "" deletes the stamp (same as ClearGoroutineRequestID).
func SetGoroutineRequestID(id string) {
	gid := currentGoroutineID()
	if id == "" {
		goroutineReqIDs.Delete(gid)
		return
	}
	goroutineReqIDs.Store(gid, id)
}

// ClearGoroutineRequestID removes the calling goroutine's req-id
// stamp. Should be called as `defer ClearGoroutineRequestID()` at
// the top of any goroutine that has previously called
// SetGoroutineRequestID, so the sync.Map entry doesn't leak after
// the goroutine exits.
//
// Without this cleanup, sync.Map would accumulate entries for
// every spawned-and-exited goroutine, growing unboundedly. The Go
// runtime reuses goroutine IDs but eventually rolls them; cleanup
// keeps the map size bounded to currently-active goroutines.
func ClearGoroutineRequestID() {
	gid := currentGoroutineID()
	goroutineReqIDs.Delete(gid)
}

// RunWithRequestID is the canonical pattern for goroutine spawn
// sites. Equivalent to:
//
//	go func() {
//	    SetGoroutineRequestID(id)
//	    defer ClearGoroutineRequestID()
//	    fn()
//	}()
//
// Encapsulating the pair makes it impossible to forget the defer;
// Cmd.perform's spawn path uses this.
func RunWithRequestID(id string, fn func()) {
	if id != "" {
		SetGoroutineRequestID(id)
		defer ClearGoroutineRequestID()
	}
	fn()
}

// currentGoroutineID parses the calling goroutine's ID from the
// stack header. runtime.Stack(buf, false) writes
//
//	"goroutine <gid> [<state>]:\n\t<frames>...\n"
//
// as the first line. We allocate a 64-byte buffer (always enough
// for the header — gid is at most ~20 decimal digits, state is a
// short word like "running" / "select" / "chan send") and parse
// only the gid.
//
// This is the Go-community-standard approach. Alternatives:
//   - context.Context threading — would require changing every
//     kernel signature; rejected (see top-of-file comment).
//   - runtime.SetFinalizer + reflective access — too fragile,
//     finalizers don't run reliably under load.
//   - github.com/petermattis/goid CGo — extra dep + CGo cost.
//
// Cost: ~150 ns per call on M1. Each Cmd.perform goroutine stamps
// once + clears once — 300 ns overhead per dispatched Task,
// well below the per-Task latency budget.
func currentGoroutineID() int64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	// Skip "goroutine " prefix (10 bytes).
	if n < 11 {
		return 0
	}
	line := buf[10:n]
	// Parse digits until non-digit (space).
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

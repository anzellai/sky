//go:build !js

package rt

// Diff-based Msg logging — Phase 1.1a Step 5. The Tick noise killer.
//
// The problem: a `Sub.every 100 Tick` subscription generates 36,000
// Msg dispatches per hour. Naïve "log every Msg" approach floods
// logs uselessly.
//
// The rule: log when state changes, meter always.
//
// A Msg dispatch produces a log line ONLY when:
//   1. hash(new_model) != hash(old_model), OR
//   2. The update returned a non-Cmd.none command, OR
//   3. The dispatch failed (panic, type error, guard rejection).
//
// Otherwise: counters + histograms get bumped but no log line.
// Result: a Tick that resyncs from DB and finds nothing new emits
// `sky_live_msg_total{name="Tick",noop="true"}` + 0 log bytes.
// The 1% of Ticks that DO produce side effects (state change or
// fired Cmd) are logged at full fidelity.
//
// Lifecycle marker (Step 6): for Msgs the developer KNOWS are noisy
// heartbeats, `Std.Live.lifecycle msg` wraps them with metadata
// that suppresses the no-op log line. Counters and histograms still
// record every dispatch, including these lifecycle messages.

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"reflect"
	"sort"
	"strconv"
	"sync"
	"time"

	"sky-app/rt/telemetry"
)

// MsgLogContext captures the dispatch context for one Msg cycle.
// Created at the top of liveApp.dispatch; consumed by Observe()
// after update + view return.
type MsgLogContext struct {
	MsgName                string // constructor name (e.g. "EditDraft", "Tick")
	IsLifecycle            bool   // marked via Std.Live.lifecycle
	StartTime              time.Time
	OldModelHash           uint64 // hash(model) BEFORE update
	OldModelHashIncomplete bool   // partial hashes never certify an unchanged model
	// SessionID — the Sky.Live session this dispatch belongs to.
	// Populated by BeginMsgLogForSession (the dispatcher knows the
	// session; pass it through). Empty for non-Live dispatch sites.
	// Surfaced in the emitted LogEntry's Fields["session_id"] so the
	// console Logs tab can correlate every msg_dispatch for one user
	// into a single timeline.
	SessionID string
	// TraceID — the OTEL trace id of the Msg span for this dispatch.
	// Set by the dispatcher (via WithMsgSpanTraced) AFTER the span
	// runs but BEFORE the deferred ObserveMsgLog fires. When set, it
	// becomes the log entry's correlation id so a log line and its
	// trace share ONE id — the console can pivot Logs ↔ Traces.
	TraceID string
}

// BeginMsgLog snapshots the dispatch start. Call BEFORE invoking
// update. The returned context is fed to ObserveMsgLog after
// update returns.
//
// Hashing happens here so we capture the OLD model's state — the
// update mutates model in place, so reading the hash after update
// would always equal new hash (= no-op detection broken).
func BeginMsgLog(msgValue, model any) MsgLogContext {
	// Preserve name/lifecycle/start-time evaluation before hashing so the
	// dispatch latency continues to include the old-model hash cost.
	ctx := MsgLogContext{
		MsgName:     ExtractMsgName(msgValue),
		IsLifecycle: isLifecycleMsg(msgValue),
		StartTime:   time.Now(),
	}
	oldHash, complete := hashAnyResult(model, time.Time{})
	ctx.OldModelHash = oldHash
	ctx.OldModelHashIncomplete = !complete
	return ctx
}

// BeginMsgLogForSession is the session-aware variant the Sky.Live
// dispatcher uses. Pre-2026-05-18 dispatch always called BeginMsgLog
// — which left SessionID empty and the console Logs tab couldn't
// correlate entries to a single user. This variant carries the sid
// through to ObserveMsgLog so the structured log entry's
// Fields["session_id"] is set.
func BeginMsgLogForSession(msgValue, model any, sid string) MsgLogContext {
	ctx := BeginMsgLog(msgValue, model)
	ctx.SessionID = sid
	return ctx
}

// ObserveMsgLog folds the dispatch outcome into metrics + (when
// state changed) the structured log buffer. Call AFTER update.
//
// `newModel` is post-update. `cmd` is the Cmd returned alongside
// the new model. `err` is non-nil when the dispatch failed (panic
// recovered, type error in update body, guard rejection).
//
// Decision tree:
//
//  1. Compute newModelHash.
//  2. noop = (oldHash == newHash) && cmdIsNone(cmd) && err == nil.
//  3. Always bump sky_live_msg_total{name, noop, outcome}.
//  4. Always observe sky_live_msg_seconds{name}.
//  5. Lifecycle + noop → done (skip log even at info level).
//  6. Non-lifecycle + noop → log at debug level (filterable
//     via SKY_LOG_LEVEL=info to drop).
//  7. State change / cmd / error → log at info (or error) level
//     with the diff summary.
//
// Serverless mode: writes to stderr via the access-log path
// instead of the ring buffer (container evicts before ring readers
// see the entry).
func ObserveMsgLog(ctx MsgLogContext, newModel any, cmd any, err error) {
	observeMsgLogWithDeadline(ctx, newModel, cmd, err, time.Time{})
}

// Zero deadline preserves the normal per-hash cooperative budget.
func observeMsgLogWithDeadline(ctx MsgLogContext, newModel any, cmd any, err error, deadline time.Time) {
	elapsed := time.Since(ctx.StartTime)
	newHash, complete := hashAnyResult(newModel, deadline)
	noop := !ctx.OldModelHashIncomplete && complete && ctx.OldModelHash == newHash && cmdIsNone(cmd) && err == nil

	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	labels := map[string]string{
		"name":    ctx.MsgName,
		"outcome": outcome,
		"noop":    strconv.FormatBool(noop),
	}
	store := telemetry.Default()
	store.Inc("sky_live_msg_total", labels)
	store.Observe("sky_live_msg_seconds", map[string]string{
		"name": ctx.MsgName,
	}, elapsed.Seconds())

	// Filter: skip the log line for boring dispatches.
	if noop {
		if ctx.IsLifecycle {
			// Marked lifecycle Msg that did nothing — total silence.
			return
		}
		// Unmarked Msg that did nothing — debug-level log line so
		// users with SKY_LOG_LEVEL=info don't see it but
		// SKY_LOG_LEVEL=debug does (for slow-Msg / wasted-work
		// investigations).
		emitMsgLog("debug", ctx, elapsed, noop, err)
		return
	}
	// State changed / cmd fired / error → real log.
	level := "info"
	if err != nil {
		level = "error"
	}
	emitMsgLog(level, ctx, elapsed, noop, err)
}

// emitMsgLog writes the structured Msg log entry to the appropriate
// sink — stderr in serverless mode (container-evict-safe), ring
// buffer in VM mode (dashboard reads it).
func emitMsgLog(level string, ctx MsgLogContext, elapsed time.Duration, noop bool, err error) {
	// Prefer the Msg span's trace id so this log line shares ONE
	// correlation id with its trace (console Logs ↔ Traces pivot).
	// Fall back to the goroutine's request id only when no span id
	// was captured (e.g. a dispatch site that didn't open a span).
	reqID := ctx.TraceID
	if reqID == "" {
		reqID = CurrentRequestID()
	}
	if IsServerless() {
		// Single JSON line to stderr — Cloud Run / Lambda capture.
		errStr := ""
		if err != nil {
			errStr = err.Error()
		}
		encoder := json.NewEncoder(serverlessStderr())
		encoder.SetEscapeHTML(false)
		// Keep the existing field order and three-decimal numeric latency.
		// Encode only primitive values, never the caller's error object.
		_ = encoder.Encode(struct {
			Timestamp string      `json:"ts"`
			Level     string      `json:"level"`
			Message   string      `json:"msg"`
			RequestID string      `json:"req_id"`
			Name      string      `json:"name"`
			Noop      bool        `json:"noop"`
			Lifecycle bool        `json:"lifecycle"`
			LatencyMS json.Number `json:"latency_ms"`
			Error     string      `json:"error"`
		}{
			Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
			Level:     level, Message: "msg_dispatch", RequestID: reqID,
			Name: ctx.MsgName, Noop: noop, Lifecycle: ctx.IsLifecycle,
			LatencyMS: json.Number(strconv.FormatFloat(float64(elapsed.Microseconds())/1000.0, 'f', 3, 64)),
			Error:     errStr,
		})
		return
	}
	fields := map[string]string{
		"name":      ctx.MsgName,
		"noop":      strconv.FormatBool(noop),
		"lifecycle": strconv.FormatBool(ctx.IsLifecycle),
	}
	// SessionID, if known, lets the console Logs tab pivot on a
	// single user's timeline. Empty on non-Live dispatch sites.
	if ctx.SessionID != "" {
		fields["session_id"] = ctx.SessionID
	}
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	// Bake the Msg name into the visible Message text. Pre-fix every
	// entry literally said "msg_dispatch" — the console Logs tab
	// (which doesn't render Fields) showed dozens of identical-
	// looking rows with no signal which Msg actually fired. Now
	// each entry self-documents: `msg_dispatch Tick` /
	// `msg_dispatch Increment` / etc.
	msg := "msg_dispatch " + ctx.MsgName
	if noop {
		msg += " (noop)"
	}
	if err != nil {
		msg += " ERROR: " + err.Error()
	}
	RecordLog(telemetry.LogEntry{
		TS:        time.Now(),
		Level:     level,
		Message:   msg,
		ReqID:     reqID,
		LatencyMS: float64(elapsed.Microseconds()) / 1000.0,
		ErrorStr:  errStr,
		Fields:    fields,
	})
}

// serverlessStderr — function indirection so tests can override to
// capture emitted lines. Returns os.Stderr in production.
var serverlessStderr = func() io.Writer { return os.Stderr }

// ─── Msg name extraction ──────────────────────────────────────

// ExtractMsgName returns the human-readable Msg constructor name.
// Three sources, in priority order:
//
//  1. lifecycleMsg wrapper — unwrap and recurse.
//  2. SkyADT.SkyName field set by codegen for typed ADT
//     constructors (the canonical case for almost every Msg
//     written by hand or AI).
//  3. Function values: runtime.FuncForPC name parsing (the
//     msgDisplayName path already used by the wire dispatcher).
//     Falls back to "<func>" when reflection can't recover a
//     readable name.
//  4. Anything else: "<unknown>".
func ExtractMsgName(msg any) string {
	if msg == nil {
		return "<nil>"
	}
	if lm, ok := msg.(lifecycleMsg); ok {
		return ExtractMsgName(lm.inner)
	}
	// v0.17 sealed-iface ADT: variant structs expose name via the
	// SkyVariantName() method. Probe the SkyVariant interface FIRST
	// so codegen-emitted variants resolve cleanly; fall through to
	// the legacy SkyADT.SkyName field for rt-side builders and
	// pre-v0.17 codegen.
	if sv, ok := msg.(SkyVariant); ok {
		if name := sv.SkyVariantName(); name != "" {
			return name
		}
	}
	if adt, ok := msg.(SkyADT); ok && adt.SkyName != "" {
		return adt.SkyName
	}
	// Function-value Msg — use the wire dispatcher's helper for
	// consistency with the sky-input attribute rendering.
	if name := msgDisplayName(msg); name != "" {
		return name
	}
	rv := reflect.ValueOf(msg)
	if rv.IsValid() {
		return "<" + rv.Type().String() + ">"
	}
	return "<unknown>"
}

// ─── Step 6 — lifecycle marker ────────────────────────────────

// lifecycleMsg wraps a Msg value to tag it as a heartbeat / Tick
// for the diff-based logger. Constructed via Std.Live.lifecycle
// in Sky source. The dispatcher unwraps via UnwrapLifecycle before
// passing to user's update function — user code never sees the
// wrapper.
type lifecycleMsg struct {
	inner any
}

// Live_lifecycle is the FFI binding for Sky.Live.lifecycle.
// Wraps `msg` so the runtime dispatcher recognises it as a noisy
// heartbeat and suppresses the log line on no-op dispatches. Metrics
// still record every dispatch.
//
// Sky-side signature: `lifecycle : msg -> msg`.
//
// Common usage:
//
//	subscriptions model =
//	    Sub.batch
//	        [ Sub.every 100 (lifecycle Tick)
//	        , Sub.every 5000 (lifecycle Heartbeat)
//	        ]
//
// Without the wrapper, every Tick produces a debug-level log line
// (filterable via SKY_LOG_LEVEL=info). With it, no log even at
// debug — useful when the Tick rate is so high it would dominate
// even debug-level output.
func Live_lifecycle(msg any) any {
	if _, already := msg.(lifecycleMsg); already {
		return msg // idempotent — wrapping a wrapped value is fine
	}
	return lifecycleMsg{inner: msg}
}

// UnwrapLifecycle returns the inner Msg if `m` is a lifecycleMsg,
// else returns m unchanged. Called by the dispatcher BEFORE
// invoking update so user code receives the raw Msg.
func UnwrapLifecycle(m any) any {
	if lm, ok := m.(lifecycleMsg); ok {
		return lm.inner
	}
	return m
}

func isLifecycleMsg(m any) bool {
	_, ok := m.(lifecycleMsg)
	return ok
}

// ─── Cmd inspection ───────────────────────────────────────────

// cmdIsNone returns true when the Cmd value is `Cmd.none` (no
// side effect). Used by the no-op detector — a dispatch that
// returns Cmd.none AND doesn't mutate model is fully boring.
//
// Cmd.batch with empty list counts as none too (degenerate batch
// produces no effect).
func cmdIsNone(cmd any) bool {
	c, ok := cmd.(cmdT)
	if !ok {
		// Not a cmdT — could be nil or a different shape; treat as
		// non-none to be conservative (don't suppress logs we
		// can't analyse).
		return cmd == nil
	}
	switch c.kind {
	case "none":
		return true
	case "batch":
		// Empty batch → effectively none.
		return len(c.batch) == 0
	default:
		return false
	}
}

// ─── Model hashing ────────────────────────────────────────────

// hashAny returns a 64-bit structural hash of an arbitrary Sky
// value. Used to detect "model didn't change" after a Msg dispatch.
//
// Implementation: walk the value via reflect, feed every primitive
// + map-entry-sorted-key + slice-element into a SHA-256 digest,
// fold to the leading 8 bytes. Slow path (~200 ns for a small
// record), but this is an observational fingerprint, not a collision-free
// semantic equality proof.
//
// Caching: each Msg dispatch hashes the model twice (once before,
// once after update). Caching one across calls isn't safe because
// the dispatcher's update CAN mutate in-place (RecordUpdate
// preserves identity for some optimisations); recompute fresh each
// time.
//
// Pathological case: an enormous model (50 MB user record) hashed
// twice per dispatch dominates the budget. Fallback when hashing
// reaches its cooperative time/depth limit: mark the result incomplete.
// Observe never treats an incomplete digest as proof of no change. The
// budget does not preempt a single reflection/sort/conversion/write. No
// digest is reused across dispatches.
func hashAny(v any) uint64 {
	value, _ := hashAnyResult(v, time.Time{})
	return value
}

// hashAnyResult keeps truncation separate from the digest. A partial prefix
// may equal another partial or complete digest; it never proves equality.
// The time budget is cooperative: individual reflection, sorting, Dict
// conversion and digest writes are not preempted by this walker.
func hashAnyResult(v any, deadline time.Time) (value uint64, complete bool) {
	h := hashPool.Get().(*hashState)
	h.reset()
	defer hashPool.Put(h)
	if deadline.IsZero() {
		deadline = time.Now().Add(time.Millisecond)
	}
	walkHash(h, reflect.ValueOf(v), 0, deadline)
	return h.sum(), !h.incomplete
}

const hashMaxDepth = 32 // bounds recursion on cyclic data structures

func walkHash(h *hashState, rv reflect.Value, depth int, deadline time.Time) {
	if h.incomplete {
		return
	}
	if depth > hashMaxDepth {
		h.incomplete = true
		h.writeString("<depth-limit>")
		return
	}
	if !time.Now().Before(deadline) {
		h.incomplete = true
		h.writeString("<timeout>")
		return
	}
	if !rv.IsValid() {
		h.writeString("<invalid>")
		return
	}
	// Bound pure pointer/interface cycles too: they do not recurse into
	// walkHash, so recursive depth checks alone cannot stop them.
	unwrapDepth := 0
	for rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface {
		if unwrapDepth >= hashMaxDepth {
			h.incomplete = true
			h.writeString("<depth-limit>")
			return
		}
		if !time.Now().Before(deadline) {
			h.incomplete = true
			h.writeString("<timeout>")
			return
		}
		unwrapDepth++
		if rv.IsNil() {
			h.writeString("<nil>")
			return
		}
		rv = rv.Elem()
	}
	if rv.CanInterface() {
		if handle, ok := rv.Interface().(ffiGoFuncHandle); ok {
			rv = handle.function
		}
	}

	switch rv.Kind() {
	case reflect.Bool:
		if rv.Bool() {
			h.writeString("t")
		} else {
			h.writeString("f")
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		h.writeInt(rv.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		h.writeUint(rv.Uint())
	case reflect.Float32, reflect.Float64:
		// Hash the IEEE 754 bit pattern so NaN payloads and
		// +0 / -0 still hash distinctly. The previous
		// `reflect.ValueOf(rv.Float()).Pointer()` call was
		// nonsense — `.Pointer()` only works on Func / Chan /
		// Map / Slice / Ptr / UnsafePointer, so any float-
		// valued field in Msg / Model crashed dispatch with
		// "reflect: call of reflect.Value.Pointer on float64
		// Value". Surfaced by the Std.Ui console's Overview
		// model carrying `errorRate5xx : Float` — every tab
		// click sent the float through walkHash.
		h.writeUint(math.Float64bits(rv.Float()))
	case reflect.String:
		h.writeString(rv.String())
	case reflect.Slice, reflect.Array:
		h.writeString("[")
		for i := 0; i < rv.Len(); i++ {
			walkHash(h, rv.Index(i), depth+1, deadline)
			if h.incomplete {
				return
			}
			h.writeString(",")
		}
		h.writeString("]")
	case reflect.Map:
		// Sort keys for deterministic order. Slow but correct;
		// pathological case is mitigated by the deadline.
		h.writeString("{")
		keys := rv.MapKeys()
		sort.Slice(keys, func(i, j int) bool {
			return fmt.Sprintf("%v", keys[i].Interface()) <
				fmt.Sprintf("%v", keys[j].Interface())
		})
		for _, k := range keys {
			walkHash(h, k, depth+1, deadline)
			if h.incomplete {
				return
			}
			h.writeString(":")
			walkHash(h, rv.MapIndex(k), depth+1, deadline)
			if h.incomplete {
				return
			}
			h.writeString(",")
		}
		h.writeString("}")
	case reflect.Struct:
		// A Dict hashes as the map of its entries (its trie layout is not
		// part of the value).
		if rv.CanInterface() {
			if d, ok := rv.Interface().(skyDictAny); ok {
				walkHash(h, reflect.ValueOf(dictToMapAny(d)), depth+1, deadline)
				return
			}
		}
		h.writeString("S<")
		for i := 0; i < rv.NumField(); i++ {
			// Provenance is not logical model state. Preserve the pre-owner
			// ADT hash, including for legacy restored boxes with empty owner.
			if isSkyAdtType(rv.Type()) && rv.Type().Field(i).Name == "SkyOwner" {
				continue
			}
			h.writeString(rv.Type().Field(i).Name)
			h.writeString("=")
			walkHash(h, rv.Field(i), depth+1, deadline)
			if h.incomplete {
				return
			}
			h.writeString(",")
		}
		h.writeString(">")
	case reflect.Func, reflect.Chan, reflect.UnsafePointer:
		// Preserve the existing opaque pointer feed. For functions this is
		// a code pointer: closures with different captures can share it.
		// This observational hash does not establish opaque-value equality.
		h.writeUint(uint64(rv.Pointer()))
	default:
		h.writeString("?")
	}
}

// hashState — a SHA-256 builder. We use SHA-256 + truncate to 8
// bytes because Go's hash/fnv has known weak distribution on
// length-prefixed string sequences (and we feed those frequently
// in walkHash). SHA-256 is overkill for the security claim but
// used only for observational model comparison. The cooperative cutoff
// cannot preempt an individual reflection/sort/conversion/digest write.
type hashState struct {
	incomplete bool     // budget/depth exhaustion, reset on every pooled use
	buf        [16]byte // scratch for int/uint encoding
	digest     hashAccumulator
}

type hashAccumulator interface {
	Reset()
	Write([]byte) (int, error)
	Sum([]byte) []byte
}

var hashPool = sync.Pool{
	New: func() any {
		return &hashState{digest: sha256.New()}
	},
}

func (h *hashState) reset()               { h.digest.Reset(); h.incomplete = false }
func (h *hashState) writeString(s string) { h.digest.Write([]byte(s)) }
func (h *hashState) writeInt(n int64) {
	binary.LittleEndian.PutUint64(h.buf[:8], uint64(n))
	h.digest.Write(h.buf[:8])
}
func (h *hashState) writeUint(n uint64) {
	binary.LittleEndian.PutUint64(h.buf[:8], n)
	h.digest.Write(h.buf[:8])
}

func (h *hashState) sum() uint64 {
	out := h.digest.Sum(nil)
	return binary.LittleEndian.Uint64(out[:8])
}

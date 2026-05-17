package rt

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"sky-app/rt/telemetry"
)

// Step 5+6 — diff-based Msg logging + lifecycle marker.
// Behaviour matrix the tests pin down:
//
//   model unchanged + Cmd.none + no error → NOOP, no log line (debug only)
//   model unchanged + Cmd.none + lifecycle  → NOOP, no log at all
//   model changed   + Cmd.none + no error → log at info level
//   any error                             → log at error level
//   any Cmd.perform / batch               → log at info level

// ─── Msg name extraction ──────────────────────────────────────

func TestExtractMsgName_SkyADT(t *testing.T) {
	msg := SkyADT{Tag: 1, SkyName: "Increment"}
	if got := ExtractMsgName(msg); got != "Increment" {
		t.Errorf("expected 'Increment', got %q", got)
	}
}

func TestExtractMsgName_LifecycleUnwrap(t *testing.T) {
	msg := lifecycleMsg{inner: SkyADT{SkyName: "Tick"}}
	if got := ExtractMsgName(msg); got != "Tick" {
		t.Errorf("expected unwrapped 'Tick', got %q", got)
	}
}

func TestExtractMsgName_Nil(t *testing.T) {
	if got := ExtractMsgName(nil); got != "<nil>" {
		t.Errorf("expected '<nil>', got %q", got)
	}
}

// ─── Lifecycle marker ─────────────────────────────────────────

func TestLifecycle_WrapsOnce(t *testing.T) {
	msg := SkyADT{SkyName: "Tick"}
	wrapped := Live_lifecycle(msg)
	if _, ok := wrapped.(lifecycleMsg); !ok {
		t.Fatalf("lifecycle should produce lifecycleMsg wrapper")
	}
	// Wrapping a wrapped value is idempotent.
	twice := Live_lifecycle(wrapped)
	if a, b := twice.(lifecycleMsg), wrapped.(lifecycleMsg); !sameLifecycleInner(a, b) {
		t.Errorf("double-wrap should be idempotent (no second wrapper layer)")
	}
}

// sameLifecycleInner — true when both wrappers carry the same Msg
// inside. Used because SkyADT is uncomparable directly.
func sameLifecycleInner(a, b lifecycleMsg) bool {
	if as, ok := a.inner.(SkyADT); ok {
		if bs, ok := b.inner.(SkyADT); ok {
			return as.Tag == bs.Tag && as.SkyName == bs.SkyName
		}
	}
	return false
}

func TestUnwrapLifecycle_PassthroughForUnwrapped(t *testing.T) {
	msg := SkyADT{SkyName: "Tick"}
	got := UnwrapLifecycle(msg)
	if got.(SkyADT).SkyName != "Tick" {
		t.Errorf("unwrap on bare Msg should be identity, got %+v", got)
	}
}

func TestUnwrapLifecycle_StripsWrapper(t *testing.T) {
	inner := SkyADT{SkyName: "Tick"}
	wrapped := Live_lifecycle(inner)
	got := UnwrapLifecycle(wrapped)
	if got.(SkyADT).SkyName != "Tick" {
		t.Errorf("unwrap should strip lifecycleMsg, got %+v", got)
	}
}

// ─── cmdIsNone ────────────────────────────────────────────────

func TestCmdIsNone_KindNone(t *testing.T) {
	cmd := cmdT{kind: "none"}
	if !cmdIsNone(cmd) {
		t.Errorf("Cmd.none should be detected")
	}
}

func TestCmdIsNone_EmptyBatch(t *testing.T) {
	cmd := cmdT{kind: "batch", batch: nil}
	if !cmdIsNone(cmd) {
		t.Errorf("empty Cmd.batch should be treated as none")
	}
}

func TestCmdIsNone_NonEmptyBatch(t *testing.T) {
	cmd := cmdT{kind: "batch", batch: []any{cmdT{kind: "perform"}}}
	if cmdIsNone(cmd) {
		t.Errorf("non-empty Cmd.batch must NOT be none")
	}
}

func TestCmdIsNone_Perform(t *testing.T) {
	cmd := cmdT{kind: "perform", task: func() any { return nil }}
	if cmdIsNone(cmd) {
		t.Errorf("Cmd.perform must NOT be none")
	}
}

// ─── hashAny ──────────────────────────────────────────────────

func TestHashAny_SameValueSameHash(t *testing.T) {
	a := map[string]any{"count": 1, "name": "alice"}
	b := map[string]any{"count": 1, "name": "alice"}
	if hashAny(a) != hashAny(b) {
		t.Errorf("structurally-equal values must hash equal")
	}
}

func TestHashAny_DiffValueDiffHash(t *testing.T) {
	a := map[string]any{"count": 1}
	b := map[string]any{"count": 2}
	if hashAny(a) == hashAny(b) {
		t.Errorf("count=1 and count=2 must hash differently")
	}
}

func TestHashAny_MapOrderInvariant(t *testing.T) {
	a := map[string]any{"a": 1, "b": 2, "c": 3}
	b := map[string]any{"c": 3, "a": 1, "b": 2}
	if hashAny(a) != hashAny(b) {
		t.Errorf("map hashing must be order-invariant (got %d vs %d)",
			hashAny(a), hashAny(b))
	}
}

func TestHashAny_NilStable(t *testing.T) {
	// Multiple calls on nil should be deterministic.
	if hashAny(nil) != hashAny(nil) {
		t.Errorf("hash(nil) must be deterministic")
	}
}

// ─── ObserveMsgLog — the diff filter ──────────────────────────

// Helper: build a Msg name + capture which level any emitted log
// line was written at.
func observeAndCaptureLog(t *testing.T, msg any, oldModel, newModel any, cmd any, err error) (level string, hadLog bool) {
	t.Helper()
	telemetry.ResetDefault()
	withServerlessEnv(t, nil) // VM mode → ring buffer captures
	ctx := BeginMsgLog(msg, oldModel)
	// Synthetic elapsed: set start to past so ObserveMsgLog computes
	// a sane latency.
	ctx.StartTime = time.Now().Add(-10 * time.Millisecond)
	ObserveMsgLog(ctx, newModel, cmd, err)
	logs := telemetry.Default().RecentLogs(0)
	for _, l := range logs {
		if l.Message == "msg_dispatch" {
			return l.Level, true
		}
	}
	return "", false
}

func TestObserveMsgLog_NoopUnmarked_DebugLevel(t *testing.T) {
	// Same model + Cmd.none + no error → noop. Unmarked Msg
	// emits at debug level (filterable via log_level).
	msg := SkyADT{SkyName: "Tick"}
	model := map[string]any{"count": 0}
	level, hadLog := observeAndCaptureLog(t, msg, model, model, cmdT{kind: "none"}, nil)
	if !hadLog {
		t.Fatalf("noop unmarked Msg should still emit debug log line")
	}
	if level != "debug" {
		t.Errorf("noop unmarked Msg should log at debug, got %q", level)
	}
}

func TestObserveMsgLog_NoopLifecycle_NoLog(t *testing.T) {
	// Same model + Cmd.none + lifecycle wrapper → no log AT ALL.
	// Counter must still bump though.
	inner := SkyADT{SkyName: "Tick"}
	msg := Live_lifecycle(inner)
	model := map[string]any{"count": 0}
	_, hadLog := observeAndCaptureLog(t, msg, model, model, cmdT{kind: "none"}, nil)
	if hadLog {
		t.Errorf("noop lifecycle Msg must NOT emit any log line")
	}
	// Counter SHOULD have bumped (lifecycle suppresses logs, not metrics).
	snap := telemetry.Default().Snapshot()
	found := false
	for _, s := range snap {
		if s.Name == "sky_live_msg_total" && s.Labels["name"] == "Tick" {
			found = true
			if s.Labels["noop"] != "true" {
				t.Errorf("expected noop=true label, got %q", s.Labels["noop"])
			}
		}
	}
	if !found {
		t.Errorf("counter must bump even for silenced lifecycle Msgs")
	}
}

func TestObserveMsgLog_StateChange_InfoLevel(t *testing.T) {
	msg := SkyADT{SkyName: "Increment"}
	oldModel := map[string]any{"count": 0}
	newModel := map[string]any{"count": 1}
	level, hadLog := observeAndCaptureLog(t, msg, oldModel, newModel, cmdT{kind: "none"}, nil)
	if !hadLog {
		t.Fatalf("state change must produce log line")
	}
	if level != "info" {
		t.Errorf("state change should log at info, got %q", level)
	}
}

func TestObserveMsgLog_FiredCmd_InfoLevel(t *testing.T) {
	// Same model BUT Cmd.perform fired → log at info.
	msg := SkyADT{SkyName: "AskTime"}
	model := map[string]any{"time": "x"}
	cmd := cmdT{kind: "perform"}
	level, hadLog := observeAndCaptureLog(t, msg, model, model, cmd, nil)
	if !hadLog {
		t.Fatalf("non-none Cmd must produce log line even if model unchanged")
	}
	if level != "info" {
		t.Errorf("expected info, got %q", level)
	}
}

func TestObserveMsgLog_Error_ErrorLevel(t *testing.T) {
	msg := SkyADT{SkyName: "BadMsg"}
	model := map[string]any{"x": 1}
	level, _ := observeAndCaptureLog(t, msg, model, model, cmdT{kind: "none"}, errors.New("panic in update"))
	if level != "error" {
		t.Errorf("error dispatch should log at error level, got %q", level)
	}
}

func TestObserveMsgLog_BumpsCounterEveryDispatch(t *testing.T) {
	telemetry.ResetDefault()
	withServerlessEnv(t, nil)
	msg := SkyADT{SkyName: "Tick"}
	model := map[string]any{"x": 0}
	// 100 noop dispatches
	for i := 0; i < 100; i++ {
		ctx := BeginMsgLog(msg, model)
		ObserveMsgLog(ctx, model, cmdT{kind: "none"}, nil)
	}
	snap := telemetry.Default().Snapshot()
	for _, s := range snap {
		if s.Name == "sky_live_msg_total" && s.Labels["name"] == "Tick" {
			if s.Value != 100 {
				t.Errorf("expected counter=100 across noop dispatches, got %v", s.Value)
			}
			return
		}
	}
	t.Errorf("counter for Tick never registered")
}

func TestObserveMsgLog_HistogramObservesEveryDispatch(t *testing.T) {
	telemetry.ResetDefault()
	withServerlessEnv(t, nil)
	msg := SkyADT{SkyName: "Tick"}
	model := map[string]any{"x": 0}
	for i := 0; i < 5; i++ {
		ctx := BeginMsgLog(msg, model)
		ObserveMsgLog(ctx, model, cmdT{kind: "none"}, nil)
	}
	snap := telemetry.Default().Snapshot()
	for _, s := range snap {
		if s.Name == "sky_live_msg_seconds" && s.Labels["name"] == "Tick" {
			if s.Count != 5 {
				t.Errorf("histogram count should equal dispatch count, got %d", s.Count)
			}
			return
		}
	}
	t.Errorf("histogram never registered")
}

// ─── Serverless mode → stderr instead of ring buffer ──────────

func TestObserveMsgLog_ServerlessEmitsToStderr(t *testing.T) {
	withServerlessEnv(t, map[string]string{"K_SERVICE": "x"})
	telemetry.ResetDefault()

	var capture bytes.Buffer
	orig := serverlessStderr
	serverlessStderr = func() io.Writer { return &capture }
	defer func() { serverlessStderr = orig }()

	msg := SkyADT{SkyName: "Increment"}
	oldModel := map[string]any{"count": 0}
	newModel := map[string]any{"count": 1}
	ctx := BeginMsgLog(msg, oldModel)
	ObserveMsgLog(ctx, newModel, cmdT{kind: "none"}, nil)

	body := capture.String()
	if !strings.Contains(body, `"msg":"msg_dispatch"`) {
		t.Errorf("expected msg_dispatch in stderr output, got: %s", body)
	}
	if !strings.Contains(body, `"name":"Increment"`) {
		t.Errorf("expected Msg name in stderr output, got: %s", body)
	}
	if !strings.Contains(body, `"noop":false`) {
		t.Errorf("state-change dispatch should be noop=false, got: %s", body)
	}
	// Ring buffer should NOT have the entry.
	logs := telemetry.Default().RecentLogs(0)
	for _, l := range logs {
		if l.Message == "msg_dispatch" {
			t.Errorf("serverless mode must NOT write Msg log to ring buffer; got %+v", l)
		}
	}
}

// ─── ReqID propagation into Msg log ───────────────────────────

func TestObserveMsgLog_PicksUpCurrentRequestID(t *testing.T) {
	telemetry.ResetDefault()
	withServerlessEnv(t, nil)
	SetGoroutineRequestID("req-12345")
	defer ClearGoroutineRequestID()

	msg := SkyADT{SkyName: "Increment"}
	oldModel := map[string]any{"count": 0}
	newModel := map[string]any{"count": 1}
	ctx := BeginMsgLog(msg, oldModel)
	ObserveMsgLog(ctx, newModel, cmdT{kind: "none"}, nil)

	logs := telemetry.Default().RecentLogs(0)
	for _, l := range logs {
		if l.Message == "msg_dispatch" {
			if l.ReqID != "req-12345" {
				t.Errorf("expected req-id propagated into Msg log, got %q", l.ReqID)
			}
			return
		}
	}
	t.Errorf("Msg log not emitted")
}

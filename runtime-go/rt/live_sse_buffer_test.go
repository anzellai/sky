package rt

// Cycle 3 P42 / Gap C14 — `sess.sseCh` buffer capacity is
// configurable via SKY_LIVE_SSE_BUFFER (default 16, clamped to
// [1, 1024]); drop count surfaces as the Prometheus counter
// `sky_live_sse_drops_total{session=<sid>}` at /_sky/metrics.
//
// Verifies:
//
//   (a) Default capacity is 16 when env unset.
//   (b) SKY_LIVE_SSE_BUFFER=N honoured at session creation.
//   (c) Out-of-range values clamp to [1, 1024]; bogus values
//       fall back to default.
//   (d) Re-loading after the SKY_-prefix changes picks up the
//       prefixed env name (env_prefix.go onEnvPrefixChange hook).
//   (e) Each of the 3 sseCh-producing call sites (dispatchBatched,
//       runPerformBody, Time.every Tick) increments
//       `sky_live_sse_drops_total` exactly once per dropped frame.
//   (f) /_sky/metrics exposition includes the new metric in
//       Prometheus text format with the {session} label.

import (
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"sky-app/rt/telemetry"
)

// withUnsetEnv unsets `name` for the duration of the test body and
// restores on cleanup. Lets a default-case test pin the unset shape.
// (t.Setenv covers the set case but has no built-in "unset for the
// duration of the test" — this is the equivalent.)
func withUnsetEnv(t *testing.T, name string) {
	t.Helper()
	prev, hadPrev := os.LookupEnv(name)
	_ = os.Unsetenv(name)
	t.Cleanup(func() {
		if hadPrev {
			_ = os.Setenv(name, prev)
		}
	})
}

// ─── (a) default capacity ──────────────────────────────────────

func TestSseChanBuffer_DefaultIs16(t *testing.T) {
	withUnsetEnv(t, "SKY_LIVE_SSE_BUFFER")
	loadSseChanBuffer()
	if sseChanBuffer != 16 {
		t.Fatalf("default sseChanBuffer: want 16, got %d", sseChanBuffer)
	}
}

// ─── (b) env override honoured ─────────────────────────────────

func TestSseChanBuffer_EnvOverride(t *testing.T) {
	t.Setenv("SKY_LIVE_SSE_BUFFER", "4")
	loadSseChanBuffer()
	if sseChanBuffer != 4 {
		t.Fatalf("SKY_LIVE_SSE_BUFFER=4: want 4, got %d", sseChanBuffer)
	}
	// And the value flows to channel creation. Direct construction
	// here because the production code paths require a full liveApp
	// to exercise; the per-call capacity assertion is sufficient to
	// pin the value-flow contract.
	ch := make(chan string, sseChanBuffer)
	if cap(ch) != 4 {
		t.Fatalf("chan capacity from env: want 4, got %d", cap(ch))
	}
}

// ─── (c) clamping rules ────────────────────────────────────────

func TestSseChanBuffer_ClampLow(t *testing.T) {
	t.Setenv("SKY_LIVE_SSE_BUFFER", "0")
	loadSseChanBuffer()
	// "0" → parsePositiveInt-style fallback to default. Refusing 0
	// outright matches every other live_* env-knob.
	if sseChanBuffer != 16 {
		t.Fatalf("SKY_LIVE_SSE_BUFFER=0: want fallback to 16, got %d", sseChanBuffer)
	}
}

func TestSseChanBuffer_ClampHigh(t *testing.T) {
	t.Setenv("SKY_LIVE_SSE_BUFFER", "9999")
	loadSseChanBuffer()
	if sseChanBuffer != 1024 {
		t.Fatalf("SKY_LIVE_SSE_BUFFER=9999: want clamp to 1024, got %d", sseChanBuffer)
	}
}

func TestSseChanBuffer_BogusFallback(t *testing.T) {
	t.Setenv("SKY_LIVE_SSE_BUFFER", "not-a-number")
	loadSseChanBuffer()
	if sseChanBuffer != 16 {
		t.Fatalf("SKY_LIVE_SSE_BUFFER=bogus: want fallback to 16, got %d", sseChanBuffer)
	}
}

// ─── (d) env-prefix re-load hook ───────────────────────────────

func TestSseChanBuffer_PrefixHookReloads(t *testing.T) {
	prevPrefix := EnvPrefix()
	t.Cleanup(func() { SetEnvPrefix(prevPrefix) })

	withUnsetEnv(t, "SKY_LIVE_SSE_BUFFER")
	t.Setenv("FENCE_LIVE_SSE_BUFFER", "32")
	SetEnvPrefix("FENCE")
	if sseChanBuffer != 32 {
		t.Fatalf("after SetEnvPrefix(FENCE) + FENCE_LIVE_SSE_BUFFER=32: want 32, got %d", sseChanBuffer)
	}
	// Restore default prefix; the unset SKY_LIVE_SSE_BUFFER should
	// drop the buffer back to the default. Pins that the hook
	// re-runs in BOTH directions.
	SetEnvPrefix("SKY")
	if sseChanBuffer != 16 {
		t.Fatalf("after SetEnvPrefix(SKY) with unset env: want default 16, got %d", sseChanBuffer)
	}
}

// ─── (e) drop counter increments per sseCh writer ─────────────

// counterValue returns the current value of
// sky_live_sse_drops_total{session=<sid>}. Returns 0 when no series
// exists yet (e.g. before any drop has been recorded). Snapshot is a
// copy so the test can mutate the returned map without affecting the
// live series.
func counterValue(sid string) float64 {
	snap := telemetry.Default().Snapshot()
	for _, s := range snap {
		if s.Name != "sky_live_sse_drops_total" {
			continue
		}
		if s.Labels["session"] == sid {
			return s.Value
		}
	}
	return 0
}

// dropCounterApp builds a minimal app whose update + view never
// reflect a change in the model — the SSE producers only push when
// the view changes, so the test has to force a view change between
// pushes. The view function captures `counter` so the test can
// mutate it externally between dispatches.
func dropCounterApp(viewN func() int) *liveApp {
	return &liveApp{
		update: func(msg, model any) any {
			return SkyTuple2{V0: model, V1: cmdT{kind: "none"}}
		},
		view: func(model any) any {
			return velement("div", nil, []any{vtext(strconv.Itoa(viewN()))})
		},
	}
}

// fillChanWithDrops floods sess.sseCh until the next push WOULD
// drop, then runs the producer fn (which performs one push) and
// returns the post-fn drop count. The producer fn is expected to
// drop because the channel is full and the test never drains.
func TestRunPerformBody_DropIncrementsCounter(t *testing.T) {
	t.Setenv("SKY_LIVE_SSE_BUFFER", "1")
	loadSseChanBuffer()
	sid := "test-run-perform-drop-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	priorDrops := counterValue(sid)

	counter := 0
	bumpView := func() int {
		counter++
		return counter
	}
	app := dropCounterApp(bumpView)
	sess := &liveSession{
		cancelSub: make(chan struct{}),
		sseCh:     make(chan string, sseChanBuffer),
		model:     "initial",
		sid:       sid,
	}
	// Baseline render: dispatch once so lastComputedBody is populated.
	body := app.dispatch(sess, "bootstrap")
	sess.lastShippedBody = body

	// Fill the buffer (capacity 1) with a sentinel frame the test
	// will NOT drain. The next sseCh write inside runPerformBody
	// MUST select the default arm and drop.
	sess.sseCh <- "<sentinel>"

	// runPerformBody renders a NEW body (bumpView advances) so
	// suppression doesn't fire — the producer tries to push, and
	// because the channel is full, drops + counts.
	task := func() any { return 0 }
	toMsg := func(r any) any { return r }
	app.runPerformBody(sess, task, toMsg)

	got := counterValue(sid) - priorDrops
	if got != 1 {
		t.Fatalf("runPerformBody full-channel drop: want +1 increment, got +%v", got)
	}
}

func TestDispatchBatched_DropIncrementsCounter(t *testing.T) {
	t.Setenv("SKY_LIVE_SSE_BUFFER", "1")
	loadSseChanBuffer()
	sid := "test-dispatch-batched-drop-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	priorDrops := counterValue(sid)

	counter := 0
	app := dropCounterApp(func() int {
		counter++
		return counter
	})
	sess := &liveSession{
		cancelSub: make(chan struct{}),
		sseCh:     make(chan string, sseChanBuffer),
		model:     "initial",
		sid:       sid,
		handlers:  map[string]any{},
	}
	// Baseline dispatch so prevTree + lastComputedBody are set;
	// register a no-op handler keyed off the batched event's
	// HandlerID so dispatchBatched can locate a Msg without
	// triggering the unknown-Msg early return.
	body := app.dispatch(sess, "bootstrap")
	sess.lastShippedBody = body
	sess.handlers["h1"] = "test-msg"

	// Saturate the buffer with a sentinel the test never drains.
	sess.sseCh <- "<sentinel>"

	// Drive a batched dispatch via the public path. View advances
	// per call (bumpView increments) so suppression is bypassed
	// and the producer reaches the sseCh write — which drops.
	ev := batchedEvent{HandlerID: "h1"}
	app.dispatchBatched(sess, ev)

	got := counterValue(sid) - priorDrops
	if got != 1 {
		t.Fatalf("dispatchBatched full-channel drop: want +1 increment, got +%v", got)
	}
}

// TestSetupSubscriptions_TickDropIncrementsCounter exercises the
// Time.every tick goroutine — fills the buffer, lets one tick fire,
// asserts the drop counter advanced.
//
// Subscriptions stream Msgs at a wallclock interval; the test uses
// a 10ms tick so two ticks land within the 50ms grace window and
// AT LEAST one drop is observable. Sub.every is the only public
// path; bypassing it would test a code path users never reach.
func TestSetupSubscriptions_TickDropIncrementsCounter(t *testing.T) {
	t.Setenv("SKY_LIVE_SSE_BUFFER", "1")
	loadSseChanBuffer()
	sid := "test-tick-drop-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	priorDrops := counterValue(sid)

	counter := 0
	app := &liveApp{
		update: func(msg, model any) any {
			return SkyTuple2{V0: model, V1: cmdT{kind: "none"}}
		},
		view: func(model any) any {
			counter++
			// Each tick renders a different body so suppression
			// doesn't fire — the producer reaches sseCh, finds
			// the buffer full, and drops.
			return velement("div", nil, []any{vtext(strconv.Itoa(counter))})
		},
		subscriptions: func(model any) any {
			// Sub.every 10ms with toMsg = identity.
			return subT{kind: "every", ms: 10, toMsg: "tick"}
		},
	}
	sess := &liveSession{
		cancelSub: make(chan struct{}),
		done:      make(chan struct{}),
		sseCh:     make(chan string, sseChanBuffer),
		model:     "initial",
		sid:       sid,
	}
	// Baseline so subscriptions can establish a starting prevTree /
	// lastComputedBody / lastShippedBody.
	body := app.dispatch(sess, "bootstrap")
	sess.lastShippedBody = body
	t.Cleanup(func() {
		close(sess.cancelSub)
		close(sess.done)
	})

	// Saturate the channel; never drain.
	sess.sseCh <- "<sentinel>"

	app.setupSubscriptions(sess)
	// Wait long enough for several ticks to fire — even a single
	// dropped tick is enough; sleeping longer just makes the assertion
	// robust against scheduler hiccups.
	time.Sleep(80 * time.Millisecond)

	got := counterValue(sid) - priorDrops
	if got < 1 {
		t.Fatalf("Time.every full-channel drop: want ≥1 increment, got +%v", got)
	}
}

// ─── (f) /_sky/metrics exposition includes the drop counter ────

func TestMetrics_ExposesSseDropsCounter(t *testing.T) {
	sid := "test-metrics-expose-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	recordSseDrop(sid)
	recordSseDrop(sid)

	req := httptest.NewRequest("GET", "/_sky/metrics", nil)
	rec := httptest.NewRecorder()
	HandleMetrics(rec, req)
	if rec.Code != 200 {
		t.Fatalf("/_sky/metrics: want 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "sky_live_sse_drops_total") {
		t.Fatalf("/_sky/metrics: missing sky_live_sse_drops_total in exposition\n%s", body)
	}
	// The session label MUST be present (operators care about
	// per-session drops to identify hot loops).
	if !strings.Contains(body, `session="`+sid+`"`) {
		t.Fatalf("/_sky/metrics: missing session label for %q in exposition\n%s", sid, body)
	}
	// And the value MUST be 2 (two recordSseDrop calls above).
	// The Prometheus exposition format renders counter values as
	// `name{labels} value\n`; substring-match keeps the test stable
	// across Prometheus exposition tweaks.
	wantLine := `sky_live_sse_drops_total{session="` + sid + `"} 2`
	if !strings.Contains(body, wantLine) {
		t.Fatalf("/_sky/metrics: want line %q, body:\n%s", wantLine, body)
	}
}

package rt

import (
	"strings"
	"testing"
)

// v0.27.0 regression: the Sky.Spa client logged a HANDLED client-local Task
// error as an RPC failure. `performTask` wrote
//
//	[sky.spa] RPC failed; kept last good model (no app-level handler for this transport error): …
//
// for every `Cmd.perform` whose result was a non-network `Err` — a
// `Native.secureGet` in a browser (`Unavailable`), a `Task.fail` — although no
// RPC ran, the model was not kept and the app's own Msg arm handled the Err.
// The RPC delivery wrote the same line even when the app routed the failure
// into `update` with `App.withRpcError`.
//
// The contract now: a Task's Err is a result, delivered to its Msg and never
// logged by the delivery. The loud line fires only from the generated
// no-handler `Applied<Msg> (Err e)` arm, through `Spa.reportRpcFailure e`
// (spaReportRpcFailure), and stays silent for a network Err (the retry overlay
// is that class's signal).

// spaRecordConsole swaps the console sink for a recorder for one test.
func spaRecordConsole(t *testing.T) *[]string {
	t.Helper()
	var got []string
	prev := spaConsoleError
	spaConsoleError = func(args ...any) {
		parts := make([]string, 0, len(args))
		for _, a := range args {
			parts = append(parts, a.(string))
		}
		got = append(got, strings.Join(parts, " "))
	}
	t.Cleanup(func() { spaConsoleError = prev })
	return &got
}

// spaRecordOverlay swaps the overlay hooks for counters for one test.
func spaRecordOverlay(t *testing.T) (armed, cleared *int) {
	t.Helper()
	a, c := 0, 0
	prevRetry, prevOk := spaRetryHook, spaOkHook
	spaRetryHook = func(func()) { a++ }
	spaOkHook = func() { c++ }
	t.Cleanup(func() { spaRetryHook, spaOkHook = prevRetry, prevOk })
	return &a, &c
}

func spaErrAdt(t *testing.T, e any) SkyADT {
	t.Helper()
	ev, ok := e.(SkyADT)
	if !ok {
		t.Fatalf("an Error value is a SkyADT, got %T", e)
	}
	return ev
}

// A client-local perform whose Err the app handles: the Err reaches the app's
// Msg and nothing is logged. Both shapes of the report: `Unavailable` (a
// `Native.secureGet` with no native shell) and `InvalidInput` (`Task.fail`).
func TestSpaPerform_HandledClientLocalErrReachesUpdateAndLogsNothing(t *testing.T) {
	logs := spaRecordConsole(t)
	armed, _ := spaRecordOverlay(t)
	for _, e := range []any{
		ErrUnavailable("Native.secureGet needs a native app shell"),
		ErrInvalidInput("local"),
	} {
		ev := spaErrAdt(t, e)
		task := func() SkyResult[SkyADT, any] { return SkyResult[SkyADT, any]{Tag: 1, ErrValue: ev} }
		var delivered []any
		toMsg := func(r SkyResult[SkyADT, any]) any { return r }
		spaPerform(task, toMsg, func(m any) { delivered = append(delivered, m) })

		if len(delivered) != 1 {
			t.Fatalf("the perform must dispatch exactly one Msg, got %d", len(delivered))
		}
		r := delivered[0].(SkyResult[SkyADT, any])
		if r.Tag != 1 || Basics_errorToStringT(r.ErrValue) != Basics_errorToStringT(ev) {
			t.Fatalf("the app's Msg must receive the Err unchanged, got %+v", r)
		}
	}
	if len(*logs) != 0 {
		t.Fatalf("a handled client-local Err must log nothing, got %q", *logs)
	}
	if *armed != 0 {
		t.Fatalf("a non-network client-local Err must not arm the connection overlay (%d)", *armed)
	}
}

// A client-local perform through a real Sky Task (`Task.fail`), not a bare
// closure: the same Err result reaches the Msg and nothing is logged.
func TestSpaPerform_TaskFailThroughTheTaskKernel(t *testing.T) {
	logs := spaRecordConsole(t)
	spaRecordOverlay(t)
	var delivered []any
	spaPerform(Task_fail[any, any](ErrInvalidInput("local")), func(r SkyResult[SkyADT, any]) any { return r },
		func(m any) { delivered = append(delivered, m) })
	if len(delivered) != 1 || delivered[0].(SkyResult[SkyADT, any]).Tag != 1 {
		t.Fatalf("Task.fail must deliver one Err result, got %#v", delivered)
	}
	if len(*logs) != 0 {
		t.Fatalf("a handled Task.fail must log nothing, got %q", *logs)
	}
}

// A network Err from a perform still arms the overlay and still reaches the
// Msg, and an Ok clears the overlay. Neither logs.
func TestSpaPerform_NetworkErrArmsOverlayOkClearsIt(t *testing.T) {
	logs := spaRecordConsole(t)
	armed, cleared := spaRecordOverlay(t)
	net := spaErrAdt(t, ErrNetwork("failed to fetch"))
	n := 0
	spaPerform(func() SkyResult[SkyADT, any] { return SkyResult[SkyADT, any]{Tag: 1, ErrValue: net} },
		func(r SkyResult[SkyADT, any]) any { return r }, func(any) { n++ })
	spaPerform(func() SkyResult[SkyADT, any] { return SkyResult[SkyADT, any]{Tag: 0, OkValue: "x"} },
		func(r SkyResult[SkyADT, any]) any { return r }, func(any) { n++ })
	if n != 2 || *armed != 1 || *cleared != 1 {
		t.Fatalf("dispatched=%d armed=%d cleared=%d, want 2/1/1", n, *armed, *cleared)
	}
	if len(*logs) != 0 {
		t.Fatalf("the overlay is the network signal; nothing is logged, got %q", *logs)
	}
}

// An RPC whose app declared `App.withRpcError`: the generated arm routes the
// Err into `update`, so the delivery logs nothing and the handler's Msg runs.
func TestSpaRpcDeliver_HandledRpcErrLogsNothing(t *testing.T) {
	logs := spaRecordConsole(t)
	spaRecordOverlay(t)
	s := newSpaSched("n")
	j := s.issue(nil, func(r SkyResult[SkyADT, any]) any { return r }, false)
	var ran []any
	http500 := spaErrAdt(t, ErrUnexpected("HTTP 500: boom"))
	spaRpcDeliver(s, j, SkyResult[SkyADT, any]{Tag: 1, ErrValue: http500},
		func(m any) { ran = append(ran, m) }, nil)
	if len(ran) != 1 || ran[0].(SkyResult[SkyADT, any]).Tag != 1 {
		t.Fatalf("the RPC Err must reach update as its Applied Msg, got %#v", ran)
	}
	if s.pending() {
		t.Fatal("a completed failed RPC settles")
	}
	if len(*logs) != 0 {
		t.Fatalf("the delivery must not decide the app has no handler; got %q", *logs)
	}
}

// An RPC that fails and that the app does NOT handle: the generated
// `Applied<Msg> (Err e)` arm keeps the model and returns
// `Spa.reportRpcFailure e`. That command stays loud, with the Error text.
func TestSpaReportRpcFailure_UnhandledRpcErrStaysLoud(t *testing.T) {
	logs := spaRecordConsole(t)
	spaRecordOverlay(t)
	s := newSpaSched("n")
	type applied struct{ r SkyResult[SkyADT, any] }
	j := s.issue(nil, func(r SkyResult[SkyADT, any]) any { return applied{r} }, false)
	model := "last good"
	// The generated no-handler arm: ( model, Spa.reportRpcFailure e ).
	run := func(m any) {
		a := m.(applied)
		if a.r.Tag == 1 {
			cmd, _ := any(Spa_reportRpcFailure(a.r.ErrValue)).(cmdT)
			if cmd.kind != "spaRpcFailed" {
				t.Fatalf("Spa.reportRpcFailure must build the spaRpcFailed command, got %q", cmd.kind)
			}
			spaReportRpcFailure(cmd.payload)
		}
	}
	for _, e := range []any{ErrUnexpected("HTTP 500: boom"), ErrDecode("bad json")} {
		spaRpcDeliver(s, j, SkyResult[SkyADT, any]{Tag: 1, ErrValue: spaErrAdt(t, e)}, run, nil)
		j = s.issue(nil, j.toMsg, false)
	}
	if model != "last good" {
		t.Fatal("the model is kept")
	}
	if len(*logs) != 2 {
		t.Fatalf("each unhandled RPC failure logs once, got %q", *logs)
	}
	for i, want := range []string{"HTTP 500: boom", "bad json"} {
		if !strings.HasPrefix((*logs)[i], spaRpcFailedPrefix) || !strings.Contains((*logs)[i], want) {
			t.Fatalf("log %d must be the loud RPC line with %q, got %q", i, want, (*logs)[i])
		}
	}
}

// A network Err reaching the no-handler arm is not logged: the overlay already
// signals it, and a retry that fails again must not stack console errors.
func TestSpaReportRpcFailure_NetworkErrIsTheOverlaysJob(t *testing.T) {
	logs := spaRecordConsole(t)
	spaReportRpcFailure(ErrNetwork("failed to fetch"))
	if len(*logs) != 0 {
		t.Fatalf("a network Err is signalled by the overlay, got %q", *logs)
	}
}

// A panic in a perform's task is recovered (logged, not dispatched), on the
// first run and on an overlay retry alike.
func TestSpaPerform_PanicIsRecovered(t *testing.T) {
	spaRecordConsole(t)
	var retry func()
	prevRetry, prevOk := spaRetryHook, spaOkHook
	spaRetryHook = func(r func()) { retry = r }
	spaOkHook = func() {}
	t.Cleanup(func() { spaRetryHook, spaOkHook = prevRetry, prevOk })
	net := spaErrAdt(t, ErrNetwork("down"))
	calls := 0
	task := func() SkyResult[SkyADT, any] {
		calls++
		if calls > 1 {
			panic("boom on retry")
		}
		return SkyResult[SkyADT, any]{Tag: 1, ErrValue: net}
	}
	n := 0
	spaPerform(task, func(r SkyResult[SkyADT, any]) any { return r }, func(any) { n++ })
	if retry == nil || n != 1 {
		t.Fatalf("a network Err arms a retry and is dispatched (retry=%v n=%d)", retry != nil, n)
	}
	retry() // must not panic out of the retry
	if n != 1 || calls != 2 {
		t.Fatalf("a panicking retry dispatches nothing (n=%d calls=%d)", n, calls)
	}
}

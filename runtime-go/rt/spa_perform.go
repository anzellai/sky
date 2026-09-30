package rt

import "fmt"

// spa_perform.go — the Sky.Spa client's result delivery for a `Cmd.perform`
// and for a server-branch RPC (portable, no build tag, so it is unit-tested on
// the host; the js driver in live_wasm.go wires the browser hooks below).
//
// The contract (v0.27.0): a Task's `Err` is a RESULT, delivered to the Msg its
// `toMsg` names, exactly like an `Ok`. The client never logs it as a failure of
// its own. A client-local perform (`Native.secureGet` in a browser, a
// `Task.fail`, a client `Http` call) has an app-level handler by construction:
// its `toMsg` is the app's own constructor, and the app's `update` decides.
//
// The one place a failure has no app-level handler is the generated
// `Applied<Msg> (Err e)` arm of a server branch when the app declared no
// `App.withRpcError`: that arm keeps the model and returns
// `Spa.reportRpcFailure e`, which logs the loud "[sky.spa] RPC failed" line
// (spaReportRpcFailure). The log therefore fires exactly where its text is
// true: an RPC ran, it failed, the model was kept, and no handler exists.

// spaConsoleError is the client's console.error sink for the reports in this
// file. The js driver points it at the browser console; a host test records.
var spaConsoleError = func(args ...any) {}

// spaRetryHook arms the built-in connection overlay with `retry` (a network
// failure: the server could not be reached). spaOkHook clears it after a
// perform that succeeded. Both are no-ops until the js driver sets them.
var (
	spaRetryHook = func(retry func()) {}
	spaOkHook    = func() {}
)

// spaRpcFailedPrefix is the loud line for a failed RPC the app does not handle.
const spaRpcFailedPrefix = "[sky.spa] RPC failed; kept last good model (no app-level handler for this transport error):"

// spaPerform runs a Cmd.perform Task and dispatches toMsg(result). A network
// Err arms the retry overlay (re-running this perform); an Ok clears it. The
// result is dispatched either way, so the app's own handling decides. A panic
// escaping the task or toMsg is recovered and logged (it cannot be dispatched
// as a typed Msg); this holds for a retry the overlay runs as well.
func spaPerform(task, toMsg any, dispatch func(any)) {
	defer func() {
		if r := recover(); r != nil {
			logEmit(logLevelError, "error",
				"Sky.Spa Cmd.perform: task panicked; effect dropped", map[string]any{
					"panic": fmt.Sprintf("%v", r),
				})
		}
	}()
	result := spaRunTask(task)
	if spaIsNetworkErr(result) {
		t, tm := task, toMsg
		spaRetryHook(func() { spaPerform(t, tm, dispatch) })
	} else if result.Tag == 0 {
		spaOkHook()
	}
	// An Err is a result like an Ok: the app's Msg receives it. Nothing is
	// logged here: the perform's toMsg is the app's own handler.
	dispatch(spaApplyToMsg(toMsg, result))
}

// spaRpcDeliver delivers a finished RPC's result through the scheduler. A
// network failure keeps the RPC in flight (the overlay's retry re-sends it) and
// reports the Err to the app at once; any other result settles the RPC.
func spaRpcDeliver(s *spaSched, j *spaRpcJob, result SkyResult[SkyADT, any], run func(any), retry func()) {
	if spaIsNetworkErr(result) {
		spaRetryHook(retry)
		s.report(j, spaApplyToMsg(j.toMsg, result), run)
		return
	}
	if result.Tag == 0 {
		spaOkHook()
	}
	// A failed RPC is delivered to its Applied<Msg>. Whether the app handles it
	// is the generated arm's knowledge, not the delivery's: with
	// App.withRpcError the arm routes it into update, without it the arm
	// returns Spa.reportRpcFailure (spaReportRpcFailure), which logs.
	s.settle(j, spaApplyToMsg(j.toMsg, result), run)
}

// spaRunRpcTask builds the request task from the request id and runs it to a
// Result. A panic while building or running is a classified Err, never a dead
// client.
func spaRunRpcTask(j *spaRpcJob) (result SkyResult[SkyADT, any]) {
	defer func() {
		if r := recover(); r != nil {
			ev, _ := ErrUnexpected(fmt.Sprintf("Sky.Spa RPC panicked: %v", r)).(SkyADT)
			result = SkyResult[SkyADT, any]{Tag: 1, ErrValue: ev}
		}
	}()
	return spaRunTask(sky_call(j.mk, j.rid))
}

// spaApplyToMsg maps a perform/RPC Result to its Msg (typed assertion first,
// reflect fallback for a non-standard shape).
func spaApplyToMsg(toMsg any, result SkyResult[SkyADT, any]) any {
	if tm, ok := toMsg.(func(SkyResult[SkyADT, any]) any); ok {
		return tm(result)
	}
	return sky_call(toMsg, result)
}

// spaRunTask runs a Cmd.perform / RPC task to its Result, reflection-free for
// the standard shapes. `Sky.Core.Error` aliases to rt.SkyADT and the
// Cmd.perform boundary is uniformly `SkyTask[SkyADT, any]`, so the task is
// invoked by typed assertion; the fallback erases E to any.
func spaRunTask(task any) SkyResult[SkyADT, any] {
	switch t := task.(type) {
	case func() SkyResult[SkyADT, any]:
		return t()
	default:
		r := anyTaskInvoke(task) // reflection-free for SkyTask nodes; erases E to any
		ev, _ := r.ErrValue.(SkyADT)
		return SkyResult[SkyADT, any]{Tag: r.Tag, OkValue: r.OkValue, ErrValue: ev}
	}
}

// spaReportRpcFailure is the `Spa.reportRpcFailure` command's effect: the loud
// console line for a server-branch RPC that failed while the app declared no
// `App.withRpcError`. Only the generated no-handler `Applied<Msg> (Err e)` arm
// returns that command, so the line's claims hold (an RPC ran, it failed, the
// model was kept, no handler exists). A network Err is not logged: the retry
// overlay is its signal, and each failed retry reaches this arm again.
func spaReportRpcFailure(err any) {
	if EnumTagIs(AdtField(err, 0), 1) { // ErrorKind 1 = Network
		return
	}
	spaConsoleError(spaRpcFailedPrefix, Basics_errorToStringT(err))
}

//go:build js

package rt

import (
	"fmt"
	"syscall/js"
)

// live_wasm.go — the Sky.Spa client TEA driver (GOOS=js GOARCH=wasm).
//
// Single-threaded: the browser event loop is the only scheduler, so there are
// no goroutines, no locks, and no channels here. The driver holds the current
// Model, and every dispatched Msg runs the pure `update`, re-renders the view
// to the DOM, interprets the returned Cmd, and reconciles the active
// subscriptions. This is the wasm counterpart of live.go's server-side
// liveAppRun / dispatch / runCmd / setupSubscriptions (all //go:build !js),
// with the goroutine + SSE + lock machinery replaced by direct calls and
// browser timers / Promises.

// The live application state (single-threaded ⇒ plain package vars).
var (
	spaModel  any
	spaUpdate any
	spaView   any
	// spaSubs is the config's `subscriptions : model -> Sub msg` (nil when the
	// config omits it). Evaluated after every dispatch to reconcile timers.
	spaSubs any
	spaRoot js.Value
	// spaPrev is the previously-rendered VNode tree (sky-id-stamped). Kept
	// across dispatches so each render can diff against it and apply a minimal
	// patch set instead of rebuilding the whole DOM. nil before the first mount.
	spaPrev *VNode
	// spaTimers holds the active Sub.every intervals, keyed by interval in ms
	// (a Sub.every leaf's identity for reconciliation). Each carries its
	// browser setInterval handle, the js.Func callback (released on stop), and
	// the current msg/toMsg to dispatch each tick.
	spaTimers = map[int]*spaTimer{}
)

type spaTimer struct {
	id  js.Value // setInterval handle
	fn  js.Func  // the interval callback — MUST be Released when the timer stops
	msg any      // Sub.every's second arg: a Msg value, or an (Int -> Msg) fn
}

// spaRun is the js/wasm implementation of the Spa_app task thunk (the host stub
// is in spa_notjs.go). It reads init/update/view/subscriptions from the config
// record, runs init, mounts the first render, interprets the initial Cmd, and
// starts any initial subscriptions, then parks the Go runtime so the browser
// can deliver events. It never returns.
func spaRun(cfg any) any {
	initFn := Field(cfg, "Init")
	spaUpdate = Field(cfg, "Update")
	spaView = Field(cfg, "View")
	// subscriptions is a required config field as of P3, but Field returns nil
	// for an absent field so an older/partial config degrades to "no subs"
	// rather than trapping.
	spaSubs = Field(cfg, "Subscriptions")

	doc := js.Global().Get("document")
	spaRoot = doc.Call("getElementById", "app")
	if !spaRoot.Truthy() {
		spaRoot = doc.Get("body")
	}

	// Wire the DOM renderer's event callbacks back into this loop before the
	// first render, so handlers built during render can dispatch.
	spaDispatch = step

	// init : a -> ( model, Cmd msg ) — the flags arg is unused by the client
	// (no server request); pass nil.
	pair := sky_call(initFn, nil)
	spaModel = tupleFirst(pair)
	cmd0 := tupleSecond(pair)

	renderCurrent()
	interpretCmd(asCmdT(cmd0), spaDispatch)
	reconcileSubs()

	select {} // keep the Go runtime alive to service events
}

// step is the TEA transition: msg -> pure update -> re-render -> interpret Cmd
// -> reconcile subscriptions. It is the single entry point for every event:
// DOM handlers, async Cmd.perform completions, and Sub.every timer ticks all
// funnel through here, so the model mutation + render + effect + subscription
// reconciliation always happen together and in order.
func step(msg any) {
	pair := sky_call2(spaUpdate, msg, spaModel)
	spaModel = tupleFirst(pair)
	cmd := tupleSecond(pair)
	renderCurrent()
	interpretCmd(asCmdT(cmd), spaDispatch)
	reconcileSubs()
}

// renderCurrent runs view(model) -> Html -> VNode and paints it to the DOM.
// The FIRST render is a full mount; every subsequent render diffs against the
// previous tree (diffTrees) and applies only the resulting patches, so DOM node
// identity — and therefore a focused input's focus/caret/uncommitted value — is
// preserved across updates.
func renderCurrent() {
	vn := HtmlToVNode(sky_call(spaView, spaModel))
	assignSkyIDs(&vn, "r")
	if spaPrev == nil {
		spaMount(spaRoot, vn)
	} else {
		// clientState tells diffTrees what the focused input actually shows
		// right now, so it skips emitting a value patch that would only
		// re-assert (and caret-reset) the user's own in-flight typing. This is
		// what keeps the typing case a MINIMAL patch set.
		patches := diffTrees(spaPrev, &vn, snapshotFocusedInput())
		spaApplyPatches(patches, spaPrev, &vn)
	}
	spaPrev = &vn
}

// snapshotFocusedInput reports the currently-focused form field's live DOM
// value keyed by its sky-id (or nil when nothing input-like is focused). Fed to
// diffTrees as clientState for input-authority alignment.
func snapshotFocusedInput() map[string]string {
	active := js.Global().Get("document").Get("activeElement")
	if !active.Truthy() {
		return nil
	}
	if t := active.Get("tagName"); t.Type() != js.TypeString {
		return nil
	} else {
		switch t.String() {
		case "INPUT", "TEXTAREA", "SELECT":
		default:
			return nil
		}
	}
	sid := active.Call("getAttribute", "sky-id")
	if sid.Type() != js.TypeString || sid.String() == "" {
		return nil
	}
	val := ""
	if v := active.Get("value"); v.Type() == js.TypeString {
		val = v.String()
	}
	return map[string]string{sid.String(): val}
}

// asCmdT narrows a Cmd value (tupleSecond of update's result) to cmdT. A
// well-typed Sky app always returns a Cmd here; anything else degrades to none.
func asCmdT(v any) cmdT {
	if c, ok := v.(cmdT); ok {
		return c
	}
	return cmdT{kind: "none"}
}

// asSubT narrows a Sub value to subT. A well-typed subscriptions function
// always returns a Sub; anything else degrades to none.
func asSubT(v any) subT {
	if s, ok := v.(subT); ok {
		return s
	}
	return subT{kind: "none"}
}

// interpretCmd is the single-threaded wasm effect interpreter over the same
// cmdT value the server runs through runCmd. It replaces the server's
// goroutine + SSE dispatch with direct calls (sync effects) and Promise
// .then/.catch (async effects) — no goroutine, no lock.
func interpretCmd(cmd cmdT, dispatch func(any)) {
	switch cmd.kind {
	case "", "none":
		return
	case "batch":
		for _, c := range cmd.batch {
			interpretCmd(asCmdT(c), dispatch)
		}
	case "perform":
		performTask(cmd.task, cmd.toMsg, dispatch)
	case "publish", "publishNoEcho":
		// v1 DECISION: Cmd.publish / publishNoEcho are a documented no-op on
		// the Sky.Spa client. In-process pub/sub in Sky.Live fans a message
		// out across *sessions* (other users / tabs) via the server broker;
		// a Sky.Spa client is a single browser tab with no peer to deliver to
		// and no server session bus. Cross-tab / cross-user pub-sub is a
		// server concern (Std.Http.Server + a shared broker), not a client
		// one, so wiring an in-tab bus here would be surface with no consumer
		// in the single-tab TEA model. When the explicit server boundary
		// lands (P4), cross-client fan-out routes through it. See
		// docs/skyspa/v1-progress.md (P3 decisions).
	}
}

// performTask runs a Cmd.perform Task and dispatches toMsg(result).
//
//   - A SYNCHRONOUS client task (pure code, Time.now, Random, Uuid — the
//     kernels compute a value immediately) returns a Sky Result directly; we
//     map it through toMsg and dispatch inline, same turn.
//   - An ASYNCHRONOUS client task (Http via fetch) returns a jsAsync carrying a
//     Promise; we attach .then/.catch that build the Sky Result and dispatch
//     when it settles — single-threaded, no goroutine.
//
// A task that FAILS reports through the Result Err branch (the kernels return
// Err on failure; fetch rejection maps to Err via jsAsync.onReject), never a
// silent drop. A panic escaping the task/toMsg is recovered and logged rather
// than killing the browser event loop (mirrors the server's per-perform
// recover); it cannot be re-dispatched as a typed Msg, so it is reported, not
// swallowed silently.
func performTask(task, toMsg any, dispatch func(any)) {
	defer func() {
		if r := recover(); r != nil {
			logEmit(logLevelError, "error",
				"Sky.Spa Cmd.perform: task panicked; effect dropped", map[string]any{
					"panic": fmt.Sprintf("%v", r),
				})
		}
	}()

	result := sky_call(task, nil)

	if a, ok := result.(jsAsync); ok {
		a.attach(func(settled any) {
			// This runs from the Promise callback (a fresh browser turn), so
			// wrap toMsg + dispatch in their own recover — a panic here has no
			// caller to unwind to.
			defer func() {
				if r := recover(); r != nil {
					logEmit(logLevelError, "error",
						"Sky.Spa Cmd.perform: async completion panicked", map[string]any{
							"panic": fmt.Sprintf("%v", r),
						})
				}
			}()
			dispatch(sky_call(toMsg, settled))
		})
		return
	}

	dispatch(sky_call(toMsg, result))
}

// jsAsync is an asynchronous client effect: a browser Promise plus the Go
// functions that turn its settled value into a Sky Result. The Promise only
// ever carries JS values (a Go value can't survive a trip through JS), so the
// Sky Result is BUILT in Go by toResult (resolve) / onReject (reject) and
// handed straight to the interpreter — never marshalled into the Promise.
type jsAsync struct {
	promise  js.Value
	toResult func(js.Value) any // resolved JS value -> Sky Result (Ok …)
	onReject func(js.Value) any // rejection reason -> Sky Result (Err …)
}

// attach wires the Promise to `deliver`, which receives the Sky Result exactly
// once (on resolve OR reject) and is expected to run toMsg + dispatch. The two
// js.Funcs are released after the single settlement so they don't leak.
func (a jsAsync) attach(deliver func(any)) {
	var onOk, onErr js.Func
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		onOk.Release()
		onErr.Release()
	}
	onOk = js.FuncOf(func(this js.Value, args []js.Value) any {
		var v js.Value
		if len(args) > 0 {
			v = args[0]
		}
		r := a.toResult(v)
		release()
		deliver(r)
		return nil
	})
	onErr = js.FuncOf(func(this js.Value, args []js.Value) any {
		var v js.Value
		if len(args) > 0 {
			v = args[0]
		}
		r := a.onReject(v)
		release()
		deliver(r)
		return nil
	})
	a.promise.Call("then", onOk).Call("catch", onErr)
}

// reconcileSubs evaluates subscriptions(model) and reconciles the active
// Sub.every timers against the desired set: it starts intervals that are newly
// desired, stops intervals that are no longer desired (clearInterval + release
// the callback), and leaves unchanged intervals running (updating only the msg
// they dispatch). Called after every dispatch and once at startup — mirrors the
// server's setupSubscriptions, minus the goroutine/SSE machinery.
//
// Reconciliation identity is the interval in ms: two Sub.every with the same
// interval are the same timer. Unlike the server (which honours ONE Sub.every
// per dispatch), the client honours any number of distinct intervals.
// Sub kinds other than "every" (subscribeTopic / stream / websocket) are not
// wired on the client in v1 — see interpretCmd's publish note.
func reconcileSubs() {
	desired := map[int]any{} // interval ms -> msg (last-write-wins per interval)
	if spaSubs != nil {
		collectEvery(asSubT(sky_call(spaSubs, spaModel)), desired)
	}

	// Stop intervals no longer desired.
	for ms, t := range spaTimers {
		if _, keep := desired[ms]; !keep {
			stopTimer(ms, t)
		}
	}
	// Start new intervals; refresh the msg on ones already running.
	for ms, msg := range desired {
		if t, ok := spaTimers[ms]; ok {
			t.msg = msg
			continue
		}
		startTimer(ms, msg)
	}
}

// collectEvery flattens a Sub tree into the interval->msg map, recursing through
// Sub.batch. A non-positive interval is ignored (a 0ms timer is a busy loop).
func collectEvery(s subT, out map[int]any) {
	switch s.kind {
	case "every":
		if s.ms > 0 {
			out[s.ms] = s.toMsg // last-write-wins for a repeated interval
		}
	case "batch":
		for _, c := range s.batch {
			collectEvery(asSubT(c), out)
		}
	}
}

// startTimer registers a browser setInterval for a Sub.every leaf. Each tick
// dispatches the sub's msg through step (so update + render + effects +
// re-reconciliation all run). If the msg is an (Int -> Msg) function, it is
// called with the current epoch-millis first — matching the server's
// timeEveryDispatch (live.go), which supports both a bare Msg and a
// time-taking function.
func startTimer(ms int, msg any) {
	if ms <= 0 {
		return
	}
	t := &spaTimer{msg: msg}
	t.fn = js.FuncOf(func(this js.Value, args []js.Value) any {
		defer func() {
			if r := recover(); r != nil {
				logEmit(logLevelError, "error",
					"Sky.Spa Sub.every: tick panicked", map[string]any{
						"panic":      fmt.Sprintf("%v", r),
						"intervalMs": ms,
					})
			}
		}()
		m := t.msg
		if isFunc(m) {
			m = sky_call(m, nowMillis())
		}
		step(m)
		return nil
	})
	t.id = js.Global().Call("setInterval", t.fn, ms)
	spaTimers[ms] = t
}

// stopTimer clears a browser interval and releases its callback.
func stopTimer(ms int, t *spaTimer) {
	js.Global().Call("clearInterval", t.id)
	t.fn.Release()
	delete(spaTimers, ms)
}

// nowMillis returns the current epoch time in milliseconds via Date.now(),
// used to feed a Sub.every (Int -> Msg) tick. Kept a syscall/js call (not
// time.Now) so it reads the browser clock directly.
func nowMillis() int {
	return js.Global().Get("Date").Call("now").Int()
}

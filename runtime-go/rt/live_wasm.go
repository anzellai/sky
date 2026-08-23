//go:build js

package rt

import (
	"fmt"
	"strings"
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
	spaModel any
	// Typed adapter closures from the rt.SpaFns codegen emits for the
	// Spa.config record — the reflect-free client dispatch table. Each is
	// invoked directly (a plain Go call), NOT via reflect.Value.Call, so the
	// steady-state TEA loop carries no reflection. spaInit/spaUpdate repack
	// their ( model, Cmd ) tuple result into SkyTuple2 (V0=model, V1=cmd).
	spaInit   func(any) SkyTuple2
	spaUpdate func(any, any) SkyTuple2
	spaView   func(any) any
	// spaSubs is the config's `subscriptions : model -> Sub msg` (nil when the
	// config omits it). Evaluated after every dispatch to reconcile timers.
	spaSubs func(any) any
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
	// spaTopics holds the active Sub.subscribeTopic EventSource connections,
	// keyed by topic string (a subscribeTopic leaf's identity for
	// reconciliation). Each carries its browser EventSource handle, the
	// onmessage js.Func (released on close), and the current toMsg decoder. This
	// is the server→client PUSH channel of the Sky.Spa auto-split: the generated
	// backend mounts `GET /_sky/sub?topic=<topic>` and emits each broker publish
	// as an SSE `data: <json>` frame (docs/skyspa/auto-split.md §16).
	spaTopics = map[string]*spaTopicSub{}
	// Routing (P4). spaRoutes is the registered client-side routes (empty ⇒ no
	// routing; a route-less app keeps native <a href> behaviour). spaNotFound is
	// the 404 page value (nil ⇒ leave the model's Page unchanged on a miss).
	// spaOnNavigate is the optional (page -> msg) hook fired after each nav.
	spaRoutes     []spaRoute
	spaNotFound   any
	spaOnNavigate any
)

type spaTimer struct {
	id  js.Value // setInterval handle
	fn  js.Func  // the interval callback — MUST be Released when the timer stops
	msg any      // Sub.every's second arg: a Msg value, or an (Int -> Msg) fn
}

type spaTopicSub struct {
	es    js.Value // the browser EventSource handle
	onMsg js.Func  // the onmessage callback — MUST be Released when the sub stops
	toMsg any      // subscribeTopic's decoder: `any -> Msg`, called per frame
}

// spaRun is the js/wasm implementation of the Spa_app task thunk (the host stub
// is in spa_notjs.go). It reads init/update/view/subscriptions from the config
// record, runs init, mounts the first render, interprets the initial Cmd, and
// starts any initial subscriptions, then parks the Go runtime so the browser
// can deliver events. It never returns.
func spaRun(cfg any) any {
	// The Sky.Spa target always emits an rt.SpaFns (typed adapter closures);
	// asSpaFns unwraps it reflect-free. A missing/foreign config yields nil
	// closures — but codegen guarantees a SpaFns for every real client.
	fns := asSpaFns(Field(cfg, "Fns"))
	spaInit = fns.Init
	spaUpdate = fns.Update
	spaView = fns.View
	// subscriptions is a required config field as of P3, but SpaFns.Subs is nil
	// when the config omits it so an older/partial config degrades to "no subs"
	// rather than trapping.
	spaSubs = fns.Subs
	// Routing config (P4). All optional — a route-less app leaves these empty
	// and behaves exactly as before P4.
	spaRoutes = asSpaRoutes(Field(cfg, "Routes"))
	spaNotFound = Field(cfg, "NotFound")
	spaOnNavigate = Field(cfg, "OnNavigate")

	doc := js.Global().Get("document")
	spaRoot = doc.Call("getElementById", "app")
	if !spaRoot.Truthy() {
		spaRoot = doc.Get("body")
	}

	// Inject the SAME base reset the server splices into <head> (liveBaseCSS,
	// live_core.go) — box-sizing:border-box, the flex-fill `#app` root, form /
	// heading resets. The Sky.Spa index.html carries none of this, so without it
	// padding+width overflow (content-box) and a `Ui.height Ui.fill` root
	// collapses. Idempotent: skipped if a page already provides it.
	spaInjectBaseCSS(doc)

	// Wire the DOM renderer's event callbacks back into this loop before the
	// first render, so handlers built during render can dispatch.
	spaDispatch = step

	// init : () -> ( model, Cmd msg ) — the flags arg is Sky Unit, which lowers
	// to Go `struct{}`; the reflect-free Init adapter asserts `a0.(struct{})`,
	// so pass the Unit VALUE `struct{}{}`, NOT nil (nil.(struct{}) panics
	// "interface conversion: interface {} is nil, not struct {}" at boot — the
	// client never renders). The adapter returns the ( model, Cmd ) tuple
	// repacked as SkyTuple2, read reflect-free.
	pair := spaInit(struct{}{})
	spaModel = pair.V0
	cmd0 := pair.V1

	// Deep-link: resolve the initial URL and set the model's Page BEFORE the
	// first paint, so a load straight onto /about renders About. Only when the
	// app registered routes.
	if len(spaRoutes) > 0 {
		spaApplyURL(spaCurrentPath())
	}

	renderCurrent()
	interpretCmd(asCmdT(cmd0), spaDispatch)
	reconcileSubs()

	// Install link interception + Back/Forward only when the app routes — a
	// route-less app (a counter) must keep native <a href> behaviour, so we
	// never preventDefault its links.
	if len(spaRoutes) > 0 {
		spaInstallRouter()
		spaFireOnNavigate() // initial-mount navigation hook (mirrors Sky.Live)
	}

	select {} // keep the Go runtime alive to service events
}

// spaCurrentPath reads location.pathname, defaulting to "/".
func spaCurrentPath() string {
	loc := js.Global().Get("location")
	if !loc.Truthy() {
		return "/"
	}
	if p := loc.Get("pathname"); p.Type() == js.TypeString && p.String() != "" {
		return p.String()
	}
	return "/"
}

// spaApplyURL resolves path against the registered routes and writes the matched
// page into the model's Page field (RecordUpdate — the same field-set Sky.Live's
// applyRoute uses, live.go). An unmatched path falls back to the notFound page
// when one is set; otherwise the model's Page is left unchanged.
func spaApplyURL(path string) {
	if len(spaRoutes) == 0 {
		return
	}
	page, ok := spaResolveRoutes(spaRoutes, path)
	if !ok {
		if spaNotFound == nil {
			return
		}
		page = spaNotFound
	}
	spaModel = RecordUpdate(spaModel, map[string]any{"Page": page})
}

// spaFireOnNavigate dispatches onNavigate(model.Page) through the TEA step —
// mirrors Sky.Live's dispatchOnNavigate (live.go). step runs update + render +
// effects + subs, so a navigation that triggers an effect (e.g. fetch the
// destination's data) is handled uniformly. No-op when the hook is unset.
func spaFireOnNavigate() {
	if spaOnNavigate == nil {
		return
	}
	page := Field(spaModel, "Page")
	if page == nil {
		return
	}
	if msg := sky_call(spaOnNavigate, page); msg != nil {
		step(msg)
	}
}

// spaNavigate applies a new URL to the model and repaints, then fires the
// navigation hook. Called by the click interceptor (after pushState updates the
// URL) and by popstate. When an onNavigate hook is set, its step does the
// render; otherwise we render + reconcile here.
func spaNavigate(path string) {
	spaApplyURL(path)
	if spaOnNavigate != nil {
		spaFireOnNavigate()
		return
	}
	renderCurrent()
	reconcileSubs()
}

// spaInstallRouter wires the History-API client router: a document-level click
// listener that intercepts internal-link clicks into pushState + in-app
// navigation, and a popstate listener for Back/Forward. Both js.Funcs live for
// the app's lifetime (global singletons), so they are intentionally not
// released. This is the wasm counterpart of Sky.Live's sky-nav JS blob
// (live.go), minus the fetch (the loop is already client-side).
func spaInstallRouter() {
	doc := js.Global().Get("document")

	clickFn := js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) == 0 {
			return nil
		}
		ev := args[0]
		if ev.Get("defaultPrevented").Truthy() {
			return nil
		}
		// Only a plain left-click navigates in-app; a modified or middle click
		// is the user's explicit "open in a new tab/window".
		if b := ev.Get("button"); b.Type() == js.TypeNumber && b.Int() != 0 {
			return nil
		}
		if ev.Get("metaKey").Truthy() || ev.Get("ctrlKey").Truthy() ||
			ev.Get("shiftKey").Truthy() || ev.Get("altKey").Truthy() {
			return nil
		}
		a := spaClosestAnchor(ev.Get("target"))
		if !a.Truthy() {
			return nil
		}
		// Explicit escapes to a real browser navigation.
		if spaHasAttr(a, "sky-external") || spaHasAttr(a, "download") {
			return nil
		}
		if t := a.Call("getAttribute", "target"); t.Type() == js.TypeString && t.String() == "_blank" {
			return nil
		}
		href := a.Call("getAttribute", "href")
		if href.Type() != js.TypeString {
			return nil
		}
		h := href.String()
		// Empty, in-page fragment, or a non-navigation scheme (mailto:/tel:) —
		// leave it to the browser.
		if h == "" || strings.HasPrefix(h, "#") {
			return nil
		}
		// Same-origin only: compare the anchor's resolved origin to location's.
		// (A relative href resolves against the document base, so its .origin is
		// the app's origin.)
		loc := js.Global().Get("location")
		if ao := a.Get("origin"); ao.Type() == js.TypeString && loc.Truthy() {
			if lo := loc.Get("origin"); lo.Type() == js.TypeString && ao.String() != lo.String() {
				return nil
			}
		}
		path := "/"
		if p := a.Get("pathname"); p.Type() == js.TypeString && p.String() != "" {
			path = p.String()
		}
		ev.Call("preventDefault")
		if hist := js.Global().Get("history"); hist.Truthy() {
			hist.Call("pushState", js.Null(), "", h)
		}
		spaNavigate(path)
		return nil
	})
	doc.Call("addEventListener", "click", clickFn)

	popFn := js.FuncOf(func(this js.Value, args []js.Value) any {
		spaNavigate(spaCurrentPath())
		return nil
	})
	// popstate is a window/global event; js.Global() is the browser window.
	js.Global().Call("addEventListener", "popstate", popFn)
}

// spaHasAttr reports whether el has attribute name (guarding hasAttribute's
// presence for non-element nodes).
func spaHasAttr(el js.Value, name string) bool {
	if el.Get("hasAttribute").Type() != js.TypeFunction {
		return false
	}
	return el.Call("hasAttribute", name).Truthy()
}

// spaClosestAnchor walks up from node to the nearest <a> ancestor (inclusive),
// or Undefined when there is none — so a click on a <span> inside a link still
// resolves to the link.
func spaClosestAnchor(node js.Value) js.Value {
	for node.Truthy() {
		if tn := node.Get("tagName"); tn.Type() == js.TypeString && tn.String() == "A" {
			return node
		}
		node = node.Get("parentNode")
	}
	return js.Undefined()
}

// step is the TEA transition: msg -> pure update -> re-render -> interpret Cmd
// -> reconcile subscriptions. It is the single entry point for every event:
// DOM handlers, async Cmd.perform completions, and Sub.every timer ticks all
// funnel through here, so the model mutation + render + effect + subscription
// reconciliation always happen together and in order.
func step(msg any) {
	pair := spaUpdate(msg, spaModel)
	spaModel = pair.V0
	cmd := pair.V1
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
	vn := HtmlToVNode(spaView(spaModel))
	assignSkyIDs(&vn, "r")
	// Parity with the server (HtmlRenderWithHandlers): hoist media-query /
	// pseudo-class (:hover/:focus) / transition / animation markers into scoped
	// <style> children. Without this pass `Ui.breakpoint` / `Ui.onPseudo` etc.
	// are INERT in the wasm client (their data-sky-mq/pc markers are never
	// consumed) — responsive + hover styling silently do nothing.
	applyStyleInjections(&vn)
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

// interpretCmd is the wasm effect interpreter over the same cmdT value the
// server runs through runCmd. It replaces the server's goroutine + SSE + lock
// machinery with a per-perform goroutine that dispatches directly (no SSE, no
// lock) — cooperatively scheduled on wasm's single thread.
func interpretCmd(cmd cmdT, dispatch func(any)) {
	switch cmd.kind {
	case "", "none":
		return
	case "batch":
		for _, c := range cmd.batch {
			interpretCmd(asCmdT(c), dispatch)
		}
	case "perform":
		// Run each perform on its own cooperatively-scheduled goroutine (NOT
		// an OS thread — wasm is single-threaded). This is required, not
		// optional: typed codegen wraps the Task in rt.TaskCoerceT, which runs
		// the task and coerces its SYNCHRONOUS return to the declared result
		// type, so an async client effect (Http via fetch) must BLOCK inside
		// the task until the Promise settles and return a real Result (see
		// http_wasm.go). Blocking inline in the event handler would freeze the
		// browser event loop the fetch Promise needs; a goroutine's block
		// yields to that loop instead. A synchronous task (Time.now / Random)
		// simply returns immediately on its goroutine and dispatches. This
		// mirrors the server's `go runPerform` (live.go), minus the SSE/lock.
		go performTask(cmd.task, cmd.toMsg, dispatch)
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

// performTask runs a Cmd.perform Task and dispatches toMsg(result). It runs on
// its own goroutine (see interpretCmd's "perform" arm) so an async task can
// BLOCK until it settles without freezing the browser event loop.
//
//   - A SYNCHRONOUS client task (pure code, Time.now, Random, Uuid — the
//     kernels compute a value immediately) returns a Sky Result at once.
//   - An ASYNCHRONOUS client task (Http via fetch, http_wasm.go) blocks the
//     goroutine on a channel the fetch Promise's .then/.catch fill, then
//     returns the settled Sky Result. Either way the task returns a real
//     `SkyResult` — which is what typed codegen's rt.TaskCoerceT requires.
//
// toMsg maps the Result to a Msg (its Ok/Err branch), and step dispatches it.
// A task that FAILS reports through the Result Err branch (the kernels return
// Err on failure; a fetch rejection maps to Err in fetchBlocking), never a
// silent drop. A panic escaping the task/toMsg is recovered and logged rather
// than killing the goroutine silently (mirrors the server's per-perform
// recover); it cannot be re-dispatched as a typed Msg, so it is reported.
func performTask(task, toMsg any, dispatch func(any)) {
	defer func() {
		if r := recover(); r != nil {
			logEmit(logLevelError, "error",
				"Sky.Spa Cmd.perform: task panicked; effect dropped", map[string]any{
					"panic": fmt.Sprintf("%v", r),
				})
		}
	}()

	// Reflection-free perform (Sky.Spa client). `Sky.Core.Error` aliases to
	// rt.SkyADT (codegen: `type Sky_Core_Error_Error = rt.SkyADT`) and the
	// Cmd.perform boundary is uniformly `SkyTask[SkyADT, any]` with
	// `toMsg : func(SkyResult[SkyADT, any]) any`, so both are invoked by TYPED
	// ASSERTION — no `reflect.Value.Call`, which TinyGo cannot compile. The
	// reflect `sky_call` fallbacks below are unreached for a real Spa client and
	// exist only for a non-standard task/toMsg shape (and are DCE-stripped once
	// no client path references reflect).
	var result SkyResult[SkyADT, any]
	switch t := task.(type) {
	case SkyTask[SkyADT, any]:
		result = t()
	case func() SkyResult[SkyADT, any]:
		result = t()
	default:
		r := anyTaskInvoke(task) // reflection-free (RunAny); erases E to any
		ev, _ := r.ErrValue.(SkyADT)
		result = SkyResult[SkyADT, any]{Tag: r.Tag, OkValue: r.OkValue, ErrValue: ev}
	}
	// Built-in connection resilience: if the perform failed because the server
	// was unreachable (a fetch rejection → Err(ErrNetwork)), show the retry
	// overlay armed to re-run THIS exact perform, instead of leaving the client
	// stranded when the generated `Applied<Msg> (Err _)` arm folds nothing back.
	// Any successful perform clears the overlay (connectivity is back). The
	// result is still dispatched below, so app-level handling is unaffected.
	if spaIsNetworkErr(result) {
		t, tm := task, toMsg
		spaShowRetryOverlay(func() { performTask(t, tm, dispatch) })
	} else if result.Tag == 0 {
		spaHideRetryOverlay()
	}

	if tm, ok := toMsg.(func(SkyResult[SkyADT, any]) any); ok {
		dispatch(tm(result))
		return
	}
	dispatch(sky_call(toMsg, result))
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
//
// "subscribeTopic" leaves are ALSO reconciled here (identity = the topic
// string): each opens an EventSource to the auto-split backend's
// `/_sky/sub?topic=<topic>` push endpoint (openTopic). Sub kinds "stream" /
// "websocket" are still not wired on the client in v1.
func reconcileSubs() {
	desired := map[int]any{}       // interval ms -> msg (last-write-wins per interval)
	desiredTopics := map[string]any{} // topic -> toMsg (last-write-wins per topic)
	if spaSubs != nil {
		root := asSubT(spaSubs(spaModel))
		collectEvery(root, desired)
		collectTopics(root, desiredTopics)
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

	// Close topic subscriptions no longer desired.
	for topic, sub := range spaTopics {
		if _, keep := desiredTopics[topic]; !keep {
			closeTopic(topic, sub)
		}
	}
	// Open new topic subscriptions; refresh the decoder on ones already open.
	for topic, toMsg := range desiredTopics {
		if sub, ok := spaTopics[topic]; ok {
			sub.toMsg = toMsg
			continue
		}
		openTopic(topic, toMsg)
	}
}

// collectTopics flattens a Sub tree into the topic->toMsg map, recursing through
// Sub.batch. Mirrors collectEvery for the "subscribeTopic" leaf. An empty topic
// is ignored (nothing to connect to).
func collectTopics(s subT, out map[string]any) {
	switch s.kind {
	case "subscribeTopic":
		if s.topic != "" {
			out[s.topic] = s.toMsg // last-write-wins for a repeated topic
		}
	case "batch":
		for _, c := range s.batch {
			collectTopics(asSubT(c), out)
		}
	}
}

// openTopic opens an EventSource to the auto-split backend's SSE push endpoint
// for `topic` and wires each `data:` frame back into the TEA loop. The frame
// body is JSON (the server marshals the published payload); it is decoded to the
// Sky `any` value the user's `toMsg` decoder expects — `sky_call(toMsg, payload)`
// yields the Msg, and `step` runs update + render + effects + re-reconcile.
//
// Same-origin: the client only ever talks to its own backend, so the endpoint
// is a bare absolute path (no CORS). Reconciliation identity is the topic
// string (see reconcileSubs).
func openTopic(topic string, toMsg any) {
	esCtor := js.Global().Get("EventSource")
	if esCtor.Type() != js.TypeFunction {
		logEmit(logLevelError, "error",
			"Sky.Spa Sub.subscribeTopic: EventSource is unavailable in this runtime", map[string]any{
				"topic": topic,
			})
		return
	}
	sub := &spaTopicSub{toMsg: toMsg}
	sub.es = esCtor.New("/_sky/sub?topic=" + jsEncodeURIComponent(topic))
	sub.onMsg = js.FuncOf(func(this js.Value, args []js.Value) any {
		defer func() {
			if r := recover(); r != nil {
				logEmit(logLevelError, "error",
					"Sky.Spa Sub.subscribeTopic: frame handling panicked", map[string]any{
						"panic": fmt.Sprintf("%v", r),
						"topic": topic,
					})
			}
		}()
		if len(args) == 0 {
			return nil
		}
		data := args[0].Get("data")
		if data.Type() != js.TypeString {
			return nil
		}
		payload := spaDecodeSSEData(data.String())
		step(sky_call(sub.toMsg, payload))
		return nil
	})
	sub.es.Call("addEventListener", "message", sub.onMsg)
	spaTopics[topic] = sub
}

// closeTopic tears down a topic EventSource and releases its callback.
func closeTopic(topic string, sub *spaTopicSub) {
	if sub.es.Truthy() {
		sub.es.Call("close")
	}
	sub.onMsg.Release()
	delete(spaTopics, topic)
}

// spaDecodeSSEData turns an SSE `data:` frame body (JSON text) into the Sky `any`
// value a subscribeTopic `toMsg` decoder consumes — the client counterpart of
// the raw in-process payload Sky.Live hands `toMsg`. Decoding via the browser's
// JSON.parse (not encoding/json) keeps this reflect-free and matches the wire
// exactly; a JSON number that is integral becomes a Go `int` (so an `Int` Msg
// arg is not handed a float). This is structural JSON→Sky decoding, NOT a `.(T)`
// assertion: the value's Sky shape is reconstructed, not coerced.
func spaDecodeSSEData(raw string) any {
	jsonObj := js.Global().Get("JSON")
	if jsonObj.Type() != js.TypeObject {
		return raw
	}
	// JSON.parse can throw on malformed input; guard so a bad frame is dropped
	// rather than killing the message callback.
	defer func() { _ = recover() }()
	return jsValueToSky(jsonObj.Call("parse", raw))
}

// jsValueToSky converts a parsed JS value into the Sky `any` representation the
// runtime uses (int/float64/string/bool, []any for arrays, map[string]any for
// objects, nil for null/undefined). Integral numbers map to int.
func jsValueToSky(v js.Value) any {
	switch v.Type() {
	case js.TypeNumber:
		f := v.Float()
		if f == float64(int(f)) {
			return int(f)
		}
		return f
	case js.TypeString:
		return v.String()
	case js.TypeBoolean:
		return v.Bool()
	case js.TypeObject:
		if v.InstanceOf(js.Global().Get("Array")) {
			n := v.Length()
			out := make([]any, 0, n)
			for i := 0; i < n; i++ {
				out = append(out, jsValueToSky(v.Index(i)))
			}
			return out
		}
		keys := js.Global().Get("Object").Call("keys", v)
		n := keys.Length()
		out := make(map[string]any, n)
		for i := 0; i < n; i++ {
			k := keys.Index(i).String()
			out[k] = jsValueToSky(v.Get(k))
		}
		return out
	default:
		return nil
	}
}

// jsEncodeURIComponent percent-encodes a topic for the `?topic=` query via the
// browser's encodeURIComponent, so a topic with spaces / reserved chars forms a
// valid URL. Falls back to the raw string if the global is unavailable.
func jsEncodeURIComponent(s string) string {
	enc := js.Global().Get("encodeURIComponent")
	if enc.Type() != js.TypeFunction {
		return s
	}
	return enc.Invoke(s).String()
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

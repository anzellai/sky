//go:build js

package rt

import "syscall/js"

// live_wasm.go — the Sky.Spa client TEA driver (GOOS=js GOARCH=wasm).
//
// Single-threaded: the browser event loop is the only scheduler, so there are
// no goroutines, no locks, and no channels here. The driver holds the current
// Model, and every dispatched Msg runs the pure `update`, re-renders the view
// to the DOM, and interprets the returned Cmd. This is the wasm counterpart of
// live.go's server-side liveAppRun/dispatch/runCmd (all //go:build !js).

// The live application state (single-threaded ⇒ plain package vars).
var (
	spaModel  any
	spaUpdate any
	spaView   any
	spaRoot   js.Value
)

// spaRun is the js/wasm implementation of the Spa_app task thunk (the host stub
// is in spa_notjs.go). It reads init/update/view from the config record, runs
// init, mounts the first render, and parks the Go runtime so the browser can
// deliver events. It never returns.
func spaRun(cfg any) any {
	initFn := Field(cfg, "Init")
	spaUpdate = Field(cfg, "Update")
	spaView = Field(cfg, "View")

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

	select {} // keep the Go runtime alive to service events
}

// step is the TEA transition: msg -> pure update -> re-render -> interpret Cmd.
func step(msg any) {
	pair := sky_call2(spaUpdate, msg, spaModel)
	spaModel = tupleFirst(pair)
	cmd := tupleSecond(pair)
	renderCurrent()
	interpretCmd(asCmdT(cmd), spaDispatch)
}

// renderCurrent runs view(model) -> Html -> VNode and paints it to the DOM.
func renderCurrent() {
	vn := HtmlToVNode(sky_call(spaView, spaModel))
	assignSkyIDs(&vn, "r")
	renderVNodeToDOM(spaRoot, vn)
}

// asCmdT narrows a Cmd value (tupleSecond of update's result) to cmdT. A
// well-typed Sky app always returns a Cmd here; anything else degrades to none.
func asCmdT(v any) cmdT {
	if c, ok := v.(cmdT); ok {
		return c
	}
	return cmdT{kind: "none"}
}

// interpretCmd is the single-threaded wasm effect interpreter over the same
// cmdT value the server runs through runCmd. "perform" runs the task inline
// (no goroutine) and dispatches the mapped Msg; pub/sub is a client TODO.
func interpretCmd(cmd cmdT, dispatch func(any)) {
	switch cmd.kind {
	case "", "none":
		return
	case "batch":
		for _, c := range cmd.batch {
			interpretCmd(asCmdT(c), dispatch)
		}
	case "perform":
		// task : Task e a — a thunk producing a Result; toMsg : a -> Msg (or
		// Result-aware). Run it inline and feed the result through toMsg.
		result := sky_call(cmd.task, nil)
		dispatch(sky_call(cmd.toMsg, result))
	case "publish", "publishNoEcho":
		// TODO: client-side in-process pub/sub is not wired in the prototype.
	}
}

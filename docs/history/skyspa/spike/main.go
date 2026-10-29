//go:build js && wasm

// spa-spike: a faithful, hand-written mirror of what Sky.Spa would EMIT for a
// client-side TEA loop. It proves three unknowns cheaply, BEFORE the real
// runtime-subset (rt core) carve:
//
//   1. bundle size of a Go->wasm TEA app (the WASM-vs-JS / TinyGo decision)
//   2. that an Element->DOM renderer works over syscall/js at acceptable interop
//   3. the shape of the client TEA driver (Model + pure update + view + event
//      dispatch), so the real emit path has a target to generate toward.
//
// Everything here maps 1:1 onto Sky concepts:
//   - Element      <-> Std.Ui.Element (a pure data tree, renderer-agnostic)
//   - Model/Msg    <-> the app's Model/Msg
//   - update       <-> update : Msg -> Model -> (Model, Cmd Msg)  [pure branch only]
//   - the driver   <-> what Sky.Spa's client runtime (rt core) would provide
//
// The pillar this de-risks: PERFORMANCE (pure UI transitions are client-local,
// zero round-trip) and DX (the app author writes Model/update/view, nothing else).

package main

import "syscall/js"

// ---------- Element: the renderer-agnostic view tree (mirrors Std.Ui.Element) ----------

type kind int

const (
	kNode kind = iota
	kText
)

// Element is a pure data value. No DOM, no rendering concerns embedded — exactly
// like Std.Ui.Element. A per-platform renderer (below, for DOM) interprets it.
type Element struct {
	kind     kind
	tag      string            // kNode: "div", "button", ...
	attrs    map[string]string // static attributes (class, style, ...)
	handlers map[string]Msg    // event name -> Msg to dispatch (e.g. "click" -> Increment)
	text     string            // kText
	children []Element
}

func node(tag string, attrs map[string]string, handlers map[string]Msg, children ...Element) Element {
	return Element{kind: kNode, tag: tag, attrs: attrs, handlers: handlers, children: children}
}
func text(s string) Element { return Element{kind: kText, text: s} }

// ---------- App: Model + Msg + pure update + view (this is ALL an author writes) ----------

// Msg — a tagged union. In Sky this is `type Msg = Increment | Decrement | Reset`.
type Msg int

const (
	Increment Msg = iota
	Decrement
	Reset
)

// Model — the client-owned UI state. In the real design this is `{ ui, data }`;
// here it's pure `ui` (a count) since the spike proves the client-local loop with
// NO server round-trip. Every one of these transitions is Cmd.none => client-only.
type Model struct {
	count int
}

func initModel() Model { return Model{count: 0} }

// update — PURE. Msg -> Model -> Model (Cmd.none branch). Runs entirely client-side.
func update(msg Msg, m Model) Model {
	switch msg {
	case Increment:
		return Model{count: m.count + 1}
	case Decrement:
		return Model{count: m.count - 1}
	case Reset:
		return Model{count: 0}
	}
	return m
}

// view — Model -> Element. The SAME kind of function you'd write for Sky.Live;
// here it targets the client renderer instead of the server HTML renderer.
func view(m Model) Element {
	btn := func(label string, msg Msg) Element {
		return node("button",
			map[string]string{"style": "font-size:1.2rem;margin:0 .4rem;padding:.4rem 1rem;cursor:pointer"},
			map[string]Msg{"click": msg},
			text(label))
	}
	return node("div", map[string]string{"style": "font-family:system-ui;text-align:center;margin-top:4rem"}, nil,
		node("h1", nil, nil, text("Sky.Spa spike — client-only TEA")),
		node("p", map[string]string{"style": "font-size:3rem;margin:1rem;font-variant-numeric:tabular-nums"}, nil,
			text(itoa(m.count))),
		node("div", nil, nil,
			btn("−1", Decrement),
			btn("Reset", Reset),
			btn("+1", Increment),
		),
		node("p", map[string]string{"style": "color:#888;margin-top:2rem"}, nil,
			text("every click: pure update, client-local, zero round-trip")),
	)
}

// ---------- The client TEA driver + Element->DOM renderer (this is the "rt core") ----------

var (
	document js.Value
	root     js.Value
	model    Model
	// keep event closures alive for the lifetime of the app
	liveFns []js.Func
)

// dispatch is the single entry point every event handler calls. This is the TEA
// loop: msg -> pure update -> re-render. (Spike uses full re-render; the real
// renderer will diff — that is a renderer optimisation, not an architecture change.)
func dispatch(msg Msg) {
	model = update(msg, model)
	renderInto(root, view(model))
}

// renderInto: interpret an Element into real DOM under `mount`, replacing content.
// This is the per-platform renderer shim — the ONLY platform-specific piece.
func renderInto(mount js.Value, el Element) {
	// release previous handler closures before rebuilding
	for _, f := range liveFns {
		f.Release()
	}
	liveFns = liveFns[:0]
	mount.Set("innerHTML", "")
	mount.Call("appendChild", toDOM(el))
}

func toDOM(el Element) js.Value {
	if el.kind == kText {
		return document.Call("createTextNode", el.text)
	}
	n := document.Call("createElement", el.tag)
	for k, v := range el.attrs {
		n.Call("setAttribute", k, v)
	}
	for evt, msg := range el.handlers {
		m := msg // capture
		f := js.FuncOf(func(this js.Value, args []js.Value) any {
			dispatch(m)
			return nil
		})
		liveFns = append(liveFns, f)
		n.Call("addEventListener", evt, f)
	}
	for _, c := range el.children {
		n.Call("appendChild", toDOM(c))
	}
	return n
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func main() {
	document = js.Global().Get("document")
	root = document.Call("getElementById", "app")
	model = initModel()
	renderInto(root, view(model))
	select {} // keep the Go runtime alive to service events
}

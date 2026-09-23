//go:build !js

package rt

import (
	"io"
	"strings"
	"testing"
)

// ─── Element builders (Std.Ui ADT shapes, legacy SkyADT form) ────────

func tEl(tag string, attrs []any, children ...any) any {
	return SkyADT{Tag: 3, SkyName: "TaggedNode", Fields: []any{tag, nil, attrs, children}}
}
func tNode(attrs []any, children ...any) any {
	return SkyADT{Tag: 2, SkyName: "Node", Fields: []any{nil, attrs, children}}
}
func tText(s string) any { return SkyADT{Tag: 1, SkyName: "Text", Fields: []any{s}} }
func tOn(name string, msg any) any {
	return SkyADT{Tag: 11, SkyName: "AttrEvent", Fields: []any{eventPair{name: name, msg: msg}}}
}
func tAttr(k, v string) any { return SkyADT{Tag: 12, SkyName: "AttrAttribute", Fields: []any{k, v}} }
func tFill() any            { return SkyADT{Tag: 2, SkyName: "Fill", Fields: []any{1}} }
func tPx(n int) any         { return SkyADT{Tag: 0, SkyName: "Px", Fields: []any{n}} }
func tWidth(l any) any      { return SkyADT{Tag: 1, SkyName: "AttrWidth", Fields: []any{l}} }
func tHeight(l any) any     { return SkyADT{Tag: 2, SkyName: "AttrHeight", Fields: []any{l}} }
func tButton(label string, msg any) any {
	return tEl("button", []any{tAttr("type", "button"), tOn("click", msg)}, tText(label))
}
func tTextInput(attrs ...any) any {
	return tEl("input", append([]any{tAttr("type", "text")}, attrs...))
}

func key(kind, value string) tuiKeyMsg { return tuiKeyMsg{ev: keyEvent{kind: kind, value: value}} }

// newTestTuiApp builds a tuiAppState + teaLoop over a Go update/view.
func newTestTuiApp(t *testing.T, model any, update func(msg, model any) any, view func(model any) any) (*tuiAppState, *teaLoop, chan any) {
	t.Helper()
	old := tuiOut
	tuiOut = io.Discard
	t.Cleanup(func() { tuiOut = old })
	msgCh := make(chan any, 64)
	loop := newTeaLoop(msgCh, func(msg, m any) any { return SkyTuple2{V0: update(msg, m), V1: Cmd_none()} }, nil, nil)
	s := &tuiAppState{viewFn: func(m any) any { return view(m) }, fd: -1, canvas: tuiCanvas{width: 1280, height: 720}, model: model, inputs: newInputRegistry()}
	s.cols, s.rows = tuiTermSize(-1)
	s.render(true)
	return s, loop, msgCh
}

// runQueued runs the loop over the queued messages until they are gone.
func runQueued(s *tuiAppState, loop *teaLoop, msgCh chan any) {
	eof := make(chan struct{})
	close(eof)
	s.run(loop, msgCh, eof, nil)
}

func gridText(g [][]tuiCell) string {
	var sb strings.Builder
	for _, row := range g {
		for _, c := range row {
			sb.WriteString(c.ch)
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

type delMsg struct{ item string }

func listView(m any) any {
	items := m.([]string)
	kids := []any{tText("items=" + strings.Join(items, ","))}
	for _, it := range items {
		kids = append(kids, tButton("Del "+it, delMsg{it}))
	}
	return tNode(nil, kids...)
}

func listUpdate(msg, m any) any {
	items := m.([]string)
	d, ok := msg.(delMsg)
	if !ok {
		return m
	}
	out := []string{}
	for _, it := range items {
		if it != d.item {
			out = append(out, it)
		}
	}
	return out
}

// Two queued Enter keys: the second one must act on the frame produced by
// the first (T2). Pre-fix the drained key was interpreted against the
// PREVIOUS frame's focusables, so "Del A" fired twice and B survived.
func TestTuiLoop_QueuedKeysUseCurrentFrame(t *testing.T) {
	s, loop, msgCh := newTestTuiApp(t, []string{"A", "B", "C"}, listUpdate, listView)
	msgCh <- key("enter", "")
	msgCh <- key("enter", "")
	runQueued(s, loop, msgCh)
	got := strings.Join(s.model.([]string), ",")
	if got != "C" {
		t.Fatalf("after two Enters on the first Del button, items=%q, want \"C\"", got)
	}
}

// A Msg applied just before an unhandled key is followed by a repaint
// (T3): the screen must show the model the loop holds.
func TestTuiLoop_RepaintsAfterDrainedMsgs(t *testing.T) {
	s, loop, msgCh := newTestTuiApp(t, []string{"A", "B"}, listUpdate, listView)
	msgCh <- delMsg{"A"}
	msgCh <- key("char", "z") // unhandled key right behind it
	runQueued(s, loop, msgCh)
	if txt := gridText(s.prev); !strings.Contains(txt, "items=B") {
		t.Fatalf("screen not repainted after the update; screen:\n%s", txt)
	}
}

// Focus follows the element: a background removal ABOVE the focused
// button must not move focus onto its neighbour (T6).
func TestTuiLoop_FocusFollowsElementIdentity(t *testing.T) {
	s, loop, msgCh := newTestTuiApp(t, []string{"A", "B", "C", "D"}, listUpdate, listView)
	msgCh <- key("tab", "")
	msgCh <- key("tab", "") // focus: Del C
	msgCh <- delMsg{"A"}    // a background update removes A
	msgCh <- key("enter", "")
	runQueued(s, loop, msgCh)
	got := strings.Join(s.model.([]string), ",")
	if got != "B,D" {
		t.Fatalf("items=%q, want \"B,D\" (Enter must delete the focused C, not the item that slid into its index)", got)
	}
}

// Ctrl-C quits even when the app has an onKey handler (T15).
func TestTuiLoop_CtrlCQuitsWithOnKey(t *testing.T) {
	s, _, _ := newTestTuiApp(t, []string{"A"}, listUpdate, listView)
	s.onKeyFn = func(k any) any { return delMsg{"A"} }
	if out := s.handleKey(keyEvent{kind: "ctrl", value: "c"}); !out.quit {
		t.Fatalf("Ctrl-C with onKey did not quit: %+v", out)
	}
	// Without onKey, `q` quits.
	s.onKeyFn = nil
	if out := s.handleKey(keyEvent{kind: "char", value: "q"}); !out.quit {
		t.Fatalf("q without onKey did not quit")
	}
}

// Ui.width on an input hoists `width fill, height fill` onto the control.
// Inside the content-sized wrapper that fill must not claim the 50,000-row
// layout budget (T4).
func TestTuiLayout_FillInsideContentSizedParent(t *testing.T) {
	view := func(_ any) any {
		return tNode(nil,
			tText("above"),
			tNode([]any{tWidth(tPx(300))}, tTextInput(tWidth(tFill()), tHeight(tFill()))),
			tText("below"),
		)
	}
	s, _, _ := newTestTuiApp(t, nil, func(msg, m any) any { return m }, view)
	if s.contentH != s.rows {
		t.Fatalf("contentH=%d, want the viewport (%d): the input's fill height leaked", s.contentH, s.rows)
	}
	txt := gridText(s.prev)
	if !strings.Contains(txt, "below") {
		t.Fatalf("text after the input is not on screen:\n%s", txt)
	}
	// A root `height fill` still fills the viewport.
	root := func(_ any) any {
		return tNode([]any{tHeight(tFill())}, tText("top"), tNode([]any{tHeight(tFill())}, tText("body")), tText("footer"))
	}
	s2, _, _ := newTestTuiApp(t, nil, func(msg, m any) any { return m }, root)
	lines := strings.Split(gridText(s2.prev), "\n")
	if !strings.HasPrefix(lines[s2.rows-1], "footer") {
		t.Fatalf("root fill layout: footer not on the last row:\n%s", gridText(s2.prev))
	}
}

// A focused button whose label reaches its edges keeps its label; the
// focus shows as reverse video instead of ▸ ◂ over the characters (T5).
func TestTuiFocus_MarkersNeverOverwriteLabel(t *testing.T) {
	s, _, _ := newTestTuiApp(t, nil, func(msg, m any) any { return m }, func(_ any) any {
		return tNode(nil, tButton("OK", "ok"))
	})
	txt := gridText(s.prev)
	if !strings.HasPrefix(txt, "OK") {
		t.Fatalf("focused button label overwritten: %q", strings.SplitN(txt, "\n", 2)[0])
	}
	if !s.prev[0][0].reverse {
		t.Fatalf("focused button shows no focus state")
	}
}

type setText struct{ s string }

// Input.multiline renders a <textarea>: it is focusable, editable, and
// Enter inserts a newline (T7).
func TestTuiTextarea_EditableMultiline(t *testing.T) {
	view := func(m any) any {
		return tNode(nil, tEl("textarea", []any{tAttr("type", "textarea"), tAttr("value", m.(string)),
			tOn("input", func(v any) any { return setText{v.(string)} })}))
	}
	update := func(msg, m any) any {
		if st, ok := msg.(setText); ok {
			return st.s
		}
		return m
	}
	s, loop, msgCh := newTestTuiApp(t, "", update, view)
	if len(s.focusables) != 1 || !s.focusables[0].isInput {
		t.Fatalf("textarea is not an editable focusable: %+v", s.focusables)
	}
	msgCh <- key("char", "a")
	msgCh <- key("enter", "")
	msgCh <- key("char", "b")
	runQueued(s, loop, msgCh)
	if s.model.(string) != "a\nb" {
		t.Fatalf("textarea model = %q, want \"a\\nb\"", s.model)
	}
}

type loginForm struct {
	Email string
	Age   int
	Keep  bool
}
type signIn struct{ f loginForm }

// Enter in a single-line input inside a Ui.form submits it: the named
// controls are collected into the onSubmit record; the form itself is
// not a tab stop (T8).
func TestTuiForm_EnterSubmitsNamedFields(t *testing.T) {
	var got any
	view := func(_ any) any {
		return tEl("form", []any{tOn("submit", func(f loginForm) any { return signIn{f} })},
			tTextInput(tAttr("name", "email")),
			tTextInput(tAttr("name", "age")),
		)
	}
	update := func(msg, m any) any { got = msg; return m }
	s, loop, msgCh := newTestTuiApp(t, nil, update, view)
	if len(s.focusables) != 2 {
		t.Fatalf("focusables = %d, want 2 (the form must not be a tab stop)", len(s.focusables))
	}
	for _, k := range []tuiKeyMsg{key("char", "a"), key("char", "@"), key("char", "b"), key("tab", ""), key("char", "4"), key("char", "2"), key("enter", "")} {
		msgCh <- k
	}
	runQueued(s, loop, msgCh)
	si, ok := got.(signIn)
	if !ok || si.f.Email != "a@b" || si.f.Age != 42 || si.f.Keep {
		t.Fatalf("submit msg = %#v, want signIn{a@b 42 false}", got)
	}
}

// A field that does not decode is a classified error, never a zero value.
func TestTuiForm_DecodeErrorIsClassified(t *testing.T) {
	if _, err := tuiDecodeFormSubmit(func(f loginForm) any { return signIn{f} }, map[string]string{"email": "x", "age": "old"}); err == nil {
		t.Fatalf("Int field fed \"old\" decoded without an error")
	}
	if _, err := tuiDecodeFormSubmit(func(f loginForm) any { return signIn{f} }, map[string]string{"age": "3"}); err == nil {
		t.Fatalf("missing String field decoded without an error")
	}
	msg, err := tuiDecodeFormSubmit("plainMsg", nil)
	if err != nil || msg != "plainMsg" {
		t.Fatalf("plain Msg handler = %v, %v", msg, err)
	}
}

type skyRec struct {
	Email string `sky:"email,string"`
	Age   int    `sky:"age,int"`
}
type submitV struct{ V0 skyRec }

// The typed codegen wraps the constructor as func(any) any narrowing its
// argument; the record type is probed from the Msg, so "42" decodes as the
// Int 42 and a non-Int is an error — never the zero the narrowing of a
// string map would produce.
func TestTuiForm_AnyTypedHandlerDecodesIntoProbedRecord(t *testing.T) {
	handler := func(p any) any {
		r, _ := p.(skyRec) // what rt.Coerce of a string map would zero-fill
		return submitV{V0: r}
	}
	msg, err := tuiDecodeFormSubmit(handler, map[string]string{"email": "a@b", "age": "42"})
	if err != nil {
		t.Fatal(err)
	}
	if got := msg.(submitV).V0; got.Email != "a@b" || got.Age != 42 {
		t.Fatalf("record = %+v, want {a@b 42}", got)
	}
	if _, err := tuiDecodeFormSubmit(handler, map[string]string{"email": "a@b", "age": "x"}); err == nil {
		t.Fatalf("age \"x\" decoded without an error")
	}
}

// onEnter fires on Enter in a single-line input (the chat composer).
func TestTuiInput_OnEnterFires(t *testing.T) {
	var got any
	view := func(_ any) any { return tNode(nil, tTextInput(tOn("enter", "send"))) }
	s, loop, msgCh := newTestTuiApp(t, nil, func(msg, m any) any { got = msg; return m }, view)
	msgCh <- key("enter", "")
	runQueued(s, loop, msgCh)
	if got != "send" {
		t.Fatalf("onEnter msg = %v, want send", got)
	}
}

// A slider paints its value and the arrow keys step it (T9).
func TestTuiSlider_ValueAndArrowKeys(t *testing.T) {
	view := func(m any) any {
		return tNode(nil, tEl("input", []any{tAttr("type", "range"), tAttr("value", m.(string)),
			tAttr("min", "0"), tAttr("max", "100"), tAttr("step", "5"), tWidth(tPx(12 * 16)),
			tOn("input", func(v any) any { return setText{v.(string)} })}))
	}
	update := func(msg, m any) any {
		if st, ok := msg.(setText); ok {
			return st.s
		}
		return m
	}
	s, loop, msgCh := newTestTuiApp(t, "0", update, view)
	row := s.prev[0]
	if row[0].ch != "●" {
		t.Fatalf("value 0: thumb not at the left end: %q", gridText(s.prev[:1]))
	}
	msgCh <- key("right", "")
	msgCh <- key("right", "")
	runQueued(s, loop, msgCh)
	if s.model.(string) != "10" {
		t.Fatalf("after two Right presses value = %q, want 10", s.model)
	}
	msgCh <- key("end", "")
	runQueued(s, loop, msgCh)
	w := s.focusables[0].w
	if s.model.(string) != "100" || s.prev[0][w-1].ch != "●" {
		t.Fatalf("End: value %q, thumb row %q", s.model, gridText(s.prev[:1]))
	}
}

// App.withInput on a terminal:tui Element view: a runtime line prompt
// under the view dispatches onLine on Enter and then clears (SA-10).
func TestTuiLinePrompt_DispatchesOnLine(t *testing.T) {
	var lines []string
	s, loop, msgCh := newTestTuiApp(t, nil, func(msg, m any) any {
		if st, ok := msg.(setText); ok {
			lines = append(lines, st.s)
		}
		return m
	}, func(_ any) any { return tText("view") })
	userView := s.viewFn
	s.viewFn = func(m any) any { return tuiWithLinePrompt(SkyCall(userView, m)) }
	s.onLineFn = func(v any) any { return setText{v.(string)} }
	s.focusKey = tuiLinePromptKey
	s.render(true)
	for _, k := range []tuiKeyMsg{key("char", "h"), key("char", "i"), key("enter", ""), key("char", "x"), key("enter", "")} {
		msgCh <- k
	}
	runQueued(s, loop, msgCh)
	if strings.Join(lines, "|") != "hi|x" {
		t.Fatalf("onLine got %q, want hi|x", lines)
	}
}

// A published payload reaches this app's own subscribeTopic subscriber
// through the terminal loop (T11).
func TestTuiLoop_PublishDeliversToOwnSubscriber(t *testing.T) {
	type got struct{ v any }
	msgCh := make(chan any, 16)
	var seen any
	loop := newTeaLoop(msgCh, func(msg, m any) any {
		switch x := msg.(type) {
		case string:
			return SkyTuple2{V0: m, V1: Cmd_publish("room", 7)}
		case got:
			seen = x.v
		}
		return SkyTuple2{V0: m, V1: Cmd_none()}
	}, nil, nil)
	loop.subs.update(func(_ any) any {
		return Sub_subscribeTopic("room", func(p any) any { return got{p} })
	}, nil)
	defer loop.subs.stopAll()
	model := loop.apply("go", nil)
	raw := <-msgCh
	msg, ok := loop.resolve(raw)
	if !ok {
		t.Fatalf("published payload dropped")
	}
	loop.apply(msg, model)
	if seen != 7 {
		t.Fatalf("subscriber saw %v, want 7", seen)
	}
}

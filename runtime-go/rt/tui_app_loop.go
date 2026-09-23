//go:build !js

// Sky.Tui — the Element-view (Tui.app / App.app on terminal:tui) main loop.
//
// Invariants this loop keeps (each one was a confirmed bug):
//
//   - A key is always interpreted against the CURRENT frame. Keys that
//     arrive while Msgs are being applied are deferred until the frame
//     has been re-laid-out and painted, so Tab order, focus and click
//     targets never come from a stale frame (T2), and every applied Msg
//     is followed by a repaint (T3).
//   - Focus follows the ELEMENT, not its tab index. Each focusable has a
//     stable identity (focusable.key: its id attribute, else its tag,
//     events, name and text), and editor buffers are keyed the same way
//     (T6). When the focused element disappears, focus stays at the same
//     position, clamped.
//   - Runtime-generated Msgs (onFocus / onBlur, form submit, the line
//     prompt) are queued locally, never dropped when the channel is busy.
//   - Ctrl-C always quits (the documented rule, see Tui_program); with no
//     onKey handler `q` quits too, unless it is typed into an input.

package rt

import (
	"fmt"
	"os"
	"strings"
)

// tuiLineSubmitMsg is the runtime line prompt's Enter (App.withInput on a
// terminal:tui Element view): the loop clears the prompt and dispatches
// onLine(text).
type tuiLineSubmitMsg struct{ text string }

// tuiFormErrorMsg reports a form submit whose fields do not decode into
// the onSubmit handler's record. It is applied as a classified error on
// the model (notification fields) and in the exit summary — never a
// zero-filled record, never a silent drop.
type tuiFormErrorMsg struct {
	err    any
	detail string
}

const tuiLinePromptID = "__sky_input_line"

// tuiLinePromptKey is the focus identity tuiFocusIdentity gives the prompt.
var tuiLinePromptKey = "id:" + tuiLinePromptID + "#0"

type tuiAppState struct {
	viewFn     any
	fd         int
	canvas     tuiCanvas
	cols, rows int
	model      any
	inputs     *inputRegistry
	focusables []focusable
	focusIdx   int
	focusKey   string
	scrollY    int
	contentH   int
	prev       [][]tuiCell
	onKeyFn    any
	onLineFn   any
}

type tuiKeyOutcome struct {
	quit   bool
	dirty  bool // repaint needed
	follow bool // scroll so the focused element is visible
	msgs   []any
}

func tuiAppLoop(cfg any, fd int, canvas tuiCanvas, initFn, updateFn, viewFn, subsFn, onKeyFn, guardFn any) any {
	onLineFn := Field(cfg, "OnLine")
	msgCh := make(chan any, 64)
	quitCh := make(chan struct{})
	eofCh := make(chan struct{})
	defer close(quitCh)

	loop := newTeaLoop(msgCh, updateFn, guardFn, durableCtxOf(Field(cfg, "Durable")))

	initRes := SkyCall(initFn, struct{}{})
	// Durable: restore the persisted model (if any) before the first render.
	model := loop.dur.bootFixed(tupleFirst(initRes))
	if cmd := tupleSecond(initRes); cmd != nil {
		loop.runCmd(cmd)
	}
	subMgr := loop.subs
	subMgr.update(subsFn, model)
	defer subMgr.stopAll()

	s := &tuiAppState{
		viewFn:   viewFn,
		fd:       fd,
		canvas:   canvas,
		model:    model,
		inputs:   newInputRegistry(),
		onKeyFn:  onKeyFn,
		onLineFn: onLineFn,
	}
	if onLineFn != nil {
		s.viewFn = func(m any) any { return tuiWithLinePrompt(SkyCall(viewFn, m)) }
		s.focusKey = tuiLinePromptKey
	}
	s.cols, s.rows = tuiTermSize(fd)
	s.render(true)

	safeGo("Tui key reader", func() {
		defer close(eofCh)
		tuiRunKeyReader(os.Stdin, func(ev keyEvent) bool {
			select {
			case msgCh <- tuiKeyMsg{ev: ev}:
				return true
			case <-quitCh:
				return false
			}
		})
	})
	tuiWatchResize(msgCh, quitCh)

	s.run(loop, msgCh, eofCh, subsFn)
	return Ok[any, any](struct{}{})
}

// run is the event loop proper. It returns on quit, or on eofCh once no
// queued Msg is left.
func (s *tuiAppState) run(loop *teaLoop, msgCh chan any, eofCh <-chan struct{}, subsFn any) {
	subMgr := loop.subs
	var pending []any
	for {
		var raw any
		if len(pending) > 0 {
			raw, pending = pending[0], pending[1:]
		} else {
			select {
			case raw = <-msgCh:
			default:
				select {
				case raw = <-msgCh:
				case <-eofCh:
					return
				}
			}
		}
		switch m := raw.(type) {
		case tuiResizeMsg:
			s.prev = nil
			s.render(true)
			continue
		case tuiKeyMsg:
			out := s.handleKey(m.ev)
			if out.quit {
				return
			}
			if len(out.msgs) > 0 {
				pending = append(append([]any{}, out.msgs...), pending...)
			}
			// With Msgs to apply, the repaint follows them (painting the
			// pre-Msg model in between would flash a stale frame).
			if out.dirty && len(out.msgs) == 0 {
				s.render(out.follow)
			}
			continue
		}
		s.applyOne(loop, raw)
		subMgr.update(subsFn, s.model)

		// Apply further queued Msgs (ticks, Cmd results) against the same
		// model before one repaint. A key or resize stops the drain: it
		// must be interpreted against the repainted frame.
	drain:
		for drained := 0; drained < 64; drained++ {
			var nx any
			if len(pending) > 0 {
				nx = pending[0]
			} else {
				select {
				case nx = <-msgCh:
					pending = append(pending, nx)
				default:
					break drain
				}
			}
			switch nx.(type) {
			case tuiKeyMsg, tuiResizeMsg:
				break drain
			}
			pending = pending[1:]
			s.applyOne(loop, nx)
			subMgr.update(subsFn, s.model)
		}
		s.render(true)
	}
}

// applyOne applies one non-key Msg to the model.
func (s *tuiAppState) applyOne(loop *teaLoop, raw any) {
	switch m := raw.(type) {
	case tuiLineSubmitMsg:
		st := s.inputs.get(tuiLinePromptKey)
		st.buffer, st.cursor, st.lastValueAttr = "", 0, ""
		if s.onLineFn == nil {
			return
		}
		if msg := SkyCall(s.onLineFn, m.text); msg != nil {
			s.model = loop.apply(msg, s.model)
		}
	case tuiFormErrorMsg:
		tuiWarn("form", m.detail)
		s.model = RecordUpdate(s.model, map[string]any{
			"Notification":     m.err,
			"NotificationType": "error",
		})
	default:
		if msg, ok := loop.resolve(raw); ok {
			s.model = loop.apply(msg, s.model)
		}
	}
}

// render lays the view out, resolves focus by identity, keeps the focused
// element in view (follow) and paints the diff.
func (s *tuiAppState) render(follow bool) {
	if c, r := tuiTermSize(s.fd); c != s.cols || r != s.rows {
		s.cols, s.rows = c, r
		s.prev = nil
	}
	var grid [][]tuiCell
	for pass := 0; pass < 3; pass++ {
		grid, s.focusables, s.contentH = renderElementFrameScroll(s.viewFn, s.model, s.cols, s.rows, s.canvas, s.focusIdx, s.inputs, s.scrollY)
		want := s.resolveFocus()
		scroll := s.clampScroll(s.scrollY)
		if follow {
			scroll = ensureFocusVisible(s.focusables, want, scroll, s.rows, s.contentH)
		}
		if want == s.focusIdx && scroll == s.scrollY {
			break
		}
		s.focusIdx, s.scrollY = want, scroll
	}
	if s.focusIdx >= 0 && s.focusIdx < len(s.focusables) {
		s.focusKey = s.focusables[s.focusIdx].key
	}
	tuiPaint(paintDiff(s.prev, grid))
	s.prev = grid
}

func (s *tuiAppState) resolveFocus() int {
	if s.focusKey != "" {
		for i, f := range s.focusables {
			if f.key == s.focusKey {
				return i
			}
		}
	}
	return clampFocus(s.focusIdx, len(s.focusables))
}

func (s *tuiAppState) clampScroll(y int) int {
	maxScroll := s.contentH - s.rows
	if maxScroll < 0 {
		maxScroll = 0
	}
	if y > maxScroll {
		y = maxScroll
	}
	if y < 0 {
		y = 0
	}
	return y
}

// setFocus moves focus and returns the onBlur / onFocus Msgs.
func (s *tuiAppState) setFocus(idx int) []any {
	if idx < 0 || idx >= len(s.focusables) || idx == s.focusIdx {
		return nil
	}
	msgs := tuiFocusChangeMsgs(s.focusables, s.focusIdx, idx)
	s.focusIdx = idx
	s.focusKey = s.focusables[idx].key
	return msgs
}

// tuiIsTextInput: an editable text control (the editor owns its keys).
func tuiIsTextInput(f focusable) bool {
	if !f.isInput {
		return false
	}
	switch f.inputType {
	case "checkbox", "radio", "range", "submit", "button", "reset":
		return false
	}
	return true
}

func (s *tuiAppState) handleKey(ev keyEvent) tuiKeyOutcome {
	// Ctrl-C always quits (see Tui_program for the rule).
	if ev.kind == "ctrl" && ev.value == "c" {
		return tuiKeyOutcome{quit: true}
	}
	n := len(s.focusables)
	var cur *focusable
	if s.focusIdx >= 0 && s.focusIdx < n {
		cur = &s.focusables[s.focusIdx]
	}
	textual := cur != nil && tuiIsTextInput(*cur)

	if ev.kind == "mouse" {
		return s.handleMouse(ev)
	}

	// Focus navigation: Tab / Shift-Tab always; Down / Up when the focus
	// is not in a text editor (which uses them for its cursor).
	next := -1
	switch ev.kind {
	case "tab":
		if n > 0 {
			next = (s.focusIdx + 1) % n
		}
	case "down":
		if !textual && n > 0 {
			next = (s.focusIdx + 1) % n
		}
	case "up":
		if !textual && n > 0 {
			next = (s.focusIdx - 1 + n) % n
		}
	case "other":
		if ev.value == "\x1b[Z" && n > 0 { // Shift-Tab
			next = (s.focusIdx - 1 + n) % n
		}
	}
	if next >= 0 {
		return tuiKeyOutcome{dirty: true, follow: true, msgs: s.setFocus(next)}
	}

	if cur != nil && cur.inputType == "range" {
		switch ev.kind {
		case "left", "right", "home", "end":
			return tuiKeyOutcome{msgs: tuiRangeStep(*cur, ev.kind)}
		}
	}

	if cur != nil && isCheckboxOrRadio(*cur) && (ev.kind == "space" || ev.kind == "enter") {
		return tuiKeyOutcome{msgs: s.activate(*cur)}
	}

	// Text editor. Ctrl-<letter> and Alt-<key> bypass it so an app's
	// global hotkeys (Ctrl-S, Alt-X, …) reach onKey while an input has
	// focus.
	if textual && ev.kind != "ctrl" && !ev.alt {
		if ev.kind == "enter" && !ev.alt {
			if e := focusableEvent(*cur, "enter"); e != nil {
				if msg := tuiExtractClickMsg(e); msg != nil {
					return tuiKeyOutcome{msgs: []any{msg}}
				}
			}
			if !isMultilineInput(*cur) {
				st := s.inputs.get(cur.key)
				var msgs []any
				if ch := focusableEvent(*cur, "change"); ch != nil {
					if msg := tuiExtractInputMsg(ch, st.buffer); msg != nil {
						msgs = append(msgs, msg)
					}
				}
				if cur.form != nil {
					if msg := s.submitForm(cur.form); msg != nil {
						msgs = append(msgs, msg)
					}
				}
				return tuiKeyOutcome{msgs: msgs}
			}
		}
		st := s.inputs.get(cur.key)
		changed, msg := tuiEditInput(st, ev, *cur)
		out := tuiKeyOutcome{}
		if changed {
			out.dirty = true
		}
		if msg != nil {
			out.msgs = []any{msg}
		}
		return out
	}

	// Enter / Space on a focused control activates it (a <button>
	// activates on both keys). An onEnter handler takes Enter first.
	if cur != nil && (ev.kind == "enter" || ev.kind == "space") {
		if ev.kind == "enter" {
			if e := focusableEvent(*cur, "enter"); e != nil {
				if msg := tuiExtractClickMsg(e); msg != nil {
					return tuiKeyOutcome{msgs: []any{msg}}
				}
			}
		}
		if msgs := s.activate(*cur); len(msgs) > 0 {
			return tuiKeyOutcome{msgs: msgs}
		}
	}

	// Viewport scrolling when the focus is not in a text editor.
	if !textual {
		y := s.scrollY
		switch ev.kind {
		case "up":
			y--
		case "down":
			y++
		case "pageup":
			y -= s.rows
		case "pagedown":
			y += s.rows
		case "home":
			y = 0
		case "end":
			y = s.contentH
		}
		if y = s.clampScroll(y); y != s.scrollY {
			s.scrollY = y
			return tuiKeyOutcome{dirty: true}
		}
	}

	if s.onKeyFn != nil {
		if key := tuiKeyToSky(s.onKeyFn, ev); key != nil {
			if msg := SkyCall(s.onKeyFn, key); msg != nil {
				return tuiKeyOutcome{msgs: []any{msg}}
			}
		}
		return tuiKeyOutcome{}
	}
	// No onKey handler: `q` quits (it never reaches here while typing
	// into an input — the editor above owns those keys).
	if ev.kind == "char" && ev.value == "q" && !ev.alt {
		return tuiKeyOutcome{quit: true}
	}
	return tuiKeyOutcome{}
}

func (s *tuiAppState) handleMouse(ev keyEvent) tuiKeyOutcome {
	button, col1, row1, isPress, ok := parseMouseEvent(ev.value)
	if !ok || !isPress {
		return tuiKeyOutcome{}
	}
	switch button {
	case 64, 65: // wheel
		y := s.scrollY - 3
		if button == 65 {
			y = s.scrollY + 3
		}
		if y = s.clampScroll(y); y != s.scrollY {
			s.scrollY = y
			return tuiKeyOutcome{dirty: true}
		}
		return tuiKeyOutcome{}
	case 0:
		// Focusable rows are content coordinates; the click is in the
		// viewport, so add the scroll offset.
		hit := hitTestFocusables(s.focusables, col1-1, row1-1+s.scrollY)
		if hit < 0 {
			return tuiKeyOutcome{}
		}
		out := tuiKeyOutcome{dirty: true, follow: true, msgs: s.setFocus(hit)}
		f := s.focusables[hit]
		if !tuiIsTextInput(f) && f.inputType != "range" {
			out.msgs = append(out.msgs, s.activate(f)...)
		}
		return out
	}
	return tuiKeyOutcome{}
}

// activate returns the Msgs a press of f dispatches: its onClick, then —
// for a type="submit" button inside a form — the form's submit.
func (s *tuiAppState) activate(f focusable) []any {
	var msgs []any
	if e := focusableEvent(f, "click"); e != nil {
		if msg := tuiExtractClickMsg(e); msg != nil {
			msgs = append(msgs, msg)
		}
	}
	if f.inputType == "submit" && f.form != nil {
		if msg := s.submitForm(f.form); msg != nil {
			msgs = append(msgs, msg)
		}
	}
	return msgs
}

// submitForm collects the form's named controls (text from the editor
// buffers; a checked checkbox / selected radio as "true") and builds the
// onSubmit Msg — or a tuiFormErrorMsg when they do not decode.
func (s *tuiAppState) submitForm(form *tuiForm) any {
	fields := map[string]string{}
	for _, fld := range form.fields {
		switch fld.inputType {
		case "checkbox", "radio":
			if fld.valueAttr != "" && fld.valueAttr != "false" {
				fields[fld.name] = "true"
			}
		default:
			fields[fld.name] = s.inputs.get(fld.key).buffer
		}
	}
	msg, err := tuiDecodeFormSubmit(tuiExtractClickMsg(form.submit), fields)
	if err != nil {
		detail := "form submit: " + err.Error()
		return tuiFormErrorMsg{err: ErrInvalidInput(detail), detail: detail}
	}
	return msg
}

// tuiRangeStep moves a range input by one step (left / right) or to an
// end (home / end) and returns its onInput / onChange Msg.
func tuiRangeStep(f focusable, key string) []any {
	lo, hi, step := tuiRangeBounds(f.min, f.max, f.step)
	v := tuiRangeValue(f.initialValue, lo, hi)
	nv := v
	switch key {
	case "left":
		nv = v - step
	case "right":
		nv = v + step
	case "home":
		nv = lo
	case "end":
		nv = hi
	}
	if nv < lo {
		nv = lo
	}
	if nv > hi {
		nv = hi
	}
	if nv == v {
		return nil
	}
	evt := focusableEvent(f, "input")
	if evt == nil {
		evt = focusableEvent(f, "change")
	}
	if evt == nil {
		return nil
	}
	if msg := tuiExtractInputMsg(evt, tuiFormatRange(nv)); msg != nil {
		return []any{msg}
	}
	return nil
}

// tuiLinePromptElement is the runtime-owned one-line input shown under the
// view when App.withInput is set on terminal:tui. Enter dispatches
// onLine(text) — the same contract as terminal:cli.
func tuiLinePromptElement() any {
	attrs := []any{
		SkyADT{Tag: 12, SkyName: "AttrAttribute", Fields: []any{"id", tuiLinePromptID}},
		SkyADT{Tag: 12, SkyName: "AttrAttribute", Fields: []any{"type", "text"}},
		SkyADT{Tag: 12, SkyName: "AttrAttribute", Fields: []any{"placeholder", "> type a line, Enter to send"}},
		SkyADT{Tag: 11, SkyName: "AttrEvent", Fields: []any{eventPair{name: "change", msg: func(v any) any {
			s, _ := v.(string)
			return tuiLineSubmitMsg{text: s}
		}}}},
		SkyADT{Tag: 1, SkyName: "AttrWidth", Fields: []any{SkyADT{Tag: 2, SkyName: "Fill", Fields: []any{1}}}},
	}
	return SkyADT{Tag: 3, SkyName: "TaggedNode", Fields: []any{"input", nil, attrs, []any{}}}
}

func tuiWithLinePrompt(view any) any {
	return SkyADT{Tag: 2, SkyName: "Node", Fields: []any{nil, []any{}, []any{view, tuiLinePromptElement()}}}
}

// ─── Focus identity ─────────────────────────────────────────────────

// isInputTag: the tags the runtime edits (Input.multiline renders a
// <textarea>; every other control is an <input>).
func isInputTag(tag string) bool { return tag == "input" || tag == "textarea" }

// tuiEventNamed returns the eventPair with the given name, or nil.
func tuiEventNamed(events []any, name string) any {
	for _, ev := range events {
		if ep, ok := ev.(eventPair); ok && ep.name == name {
			return ev
		}
	}
	return nil
}

// tuiHasFocusEvents: the box handles an event a key can trigger. A form's
// own onSubmit does not make it a tab stop.
func tuiHasFocusEvents(b layoutBox) bool {
	for _, ev := range b.events {
		if ep, ok := ev.(eventPair); ok && b.tag == "form" && ep.name == "submit" {
			continue
		}
		return true
	}
	return false
}

// tuiFocusIdentity is the stable identity of a focusable box: its id
// attribute when set, else its tag, type, name, events (with their Msg
// values) and text. The input's current value is deliberately NOT part
// of it (it changes on every keystroke).
func tuiFocusIdentity(b layoutBox) string {
	if b.idAttr != "" {
		return "id:" + b.idAttr
	}
	var sb strings.Builder
	sb.WriteString(b.tag)
	sb.WriteByte('|')
	sb.WriteString(b.inputType)
	if b.nameAttr != "" {
		sb.WriteString("|name=")
		sb.WriteString(b.nameAttr)
	}
	for _, ev := range b.events {
		ep, ok := ev.(eventPair)
		if !ok {
			continue
		}
		sb.WriteString("|")
		sb.WriteString(ep.name)
		if ep.msg != nil && !isFunc(ep.msg) {
			v := fmt.Sprintf("=%v", ep.msg)
			if len(v) > 200 {
				v = v[:200]
			}
			sb.WriteString(v)
		}
	}
	sb.WriteString("|")
	sb.WriteString(tuiBoxText(b, 64))
	return sb.String()
}

// tuiBoxText concatenates the text leaves of a box, up to max bytes.
func tuiBoxText(b layoutBox, max int) string {
	var sb strings.Builder
	var walk func(layoutBox)
	walk = func(x layoutBox) {
		if sb.Len() >= max {
			return
		}
		if x.kind == "text" {
			sb.WriteString(x.text)
		}
		for _, c := range x.children {
			walk(c)
		}
	}
	walk(b)
	s := sb.String()
	if len(s) > max {
		s = s[:max]
	}
	return s
}

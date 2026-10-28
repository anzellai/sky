//go:build js

package rt

import (
	"syscall/js"
	"unicode/utf16"
)

// Std.Native.insertAtCursor in the browser: insert text at a text control's
// selection, the "Insert" menu of an editor (a heading marker, a link, a
// `{{first_name}}` placeholder).
//
// The person presses a menu button to insert, so focus has usually LEFT the
// text control by the time the Task runs, and a blurred control's own
// selectionStart / selectionEnd are not a reliable record of where the caret
// was (a value write moves them to the end). The caret tracker below records
// each text control's selection while the person works in it, keyed by the
// element id, and the kernel uses that record when the control is not the
// focused element.

// spaCaretSel is the last selection the person made in a text control.
type spaCaretSel struct{ start, end int }

var (
	// spaCarets maps an element id to its last recorded selection. Keyed by
	// id, not by node, so a re-render that rebuilds the node keeps it. wasm
	// runs one goroutine at a time, so the event callbacks and the perform
	// goroutine never touch the map together.
	spaCarets        = map[string]spaCaretSel{}
	spaCaretTrackOn  bool
	spaCaretTrackErr bool
)

// isTextControl reports whether el takes setRangeText: a textarea, or an
// input whose type has a text selection (the selection API throws on email,
// number and the rest).
func isTextControl(el js.Value) bool {
	switch tagName(el) {
	case "TEXTAREA":
		return true
	case "INPUT":
		t := ""
		if v := el.Get("type"); v.Type() == js.TypeString {
			t = v.String()
		}
		switch t {
		case "", "text", "search", "url", "tel", "password":
			return true
		}
	}
	return false
}

// spaRecordCaret stores el's current selection under its id.
func spaRecordCaret(el js.Value) {
	if !el.Truthy() || !isTextControl(el) {
		return
	}
	id := ""
	if v := el.Get("id"); v.Type() == js.TypeString {
		id = v.String()
	}
	if id == "" {
		return
	}
	s, e := selectionRange(el)
	if s < 0 {
		return
	}
	spaCarets[id] = spaCaretSel{start: s, end: e}
}

// NativeInstallCaretTracker binds the document-level listeners that record
// each text control's selection. Idempotent: the Sky.Spa client calls it on
// every render (next to spaInstallSelectTracker) and the insertAtCursor
// kernel calls it too. The listeners run in the CAPTURE phase, so a control's
// own handler cannot stop them.
//
// Only a selection the PERSON made is recorded: an event whose control is the
// focused element (typing, a click, a selection, a key), or the focusout that
// ends the person's work in it. A value write by the patcher on a blurred
// control moves its caret to the end, and that position is never recorded.
func NativeInstallCaretTracker() {
	if spaCaretTrackOn || spaCaretTrackErr {
		return
	}
	doc := js.Global().Get("document")
	if !doc.Truthy() || doc.Get("addEventListener").Type() != js.TypeFunction {
		return
	}
	spaCaretTrackOn = true
	record := js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) == 0 || !args[0].Truthy() {
			return nil
		}
		ev := args[0]
		d := js.Global().Get("document")
		active := d.Get("activeElement")
		t := ev.Get("target")
		if ev.Get("type").String() == "focusout" {
			// The control is losing focus: its selection is still the one the
			// person left.
			spaRecordCaret(t)
			return nil
		}
		// selectionchange is dispatched at the document (and, in newer
		// browsers, at the control): the focused control is the one whose
		// selection moved.
		if !t.Truthy() || t.Equal(d) {
			t = active
		}
		if active.Truthy() && t.Equal(active) {
			spaRecordCaret(t)
		}
		return nil
	})
	opts := map[string]any{"capture": true}
	for _, e := range []string{"focusout", "select", "selectionchange", "keyup", "pointerup", "mouseup", "input"} {
		if _, err := safeJsCall(doc, "addEventListener", e, record, opts); err != nil {
			spaCaretTrackErr = true
			return
		}
	}
}

// Native_insertAtCursor is the Std.Native.insertAtCursor kernel
// (`String -> String -> Task Error String`). It inserts text at the control's
// selection with setRangeText(text, start, end, "end") (a selection is
// replaced), puts the caret after the inserted text, focuses the control, and
// yields the control's new whole value.
func Native_insertAtCursor(elementID any, text any) any {
	id, s := AsString(elementID), AsString(text)
	return func() any { return nativeInsertAtCursor(id, s) }
}

func nativeInsertAtCursor(id, text string) SkyResult[any, any] {
	doc := js.Global().Get("document")
	if !doc.Truthy() || doc.Get("getElementById").Type() != js.TypeFunction {
		return Err[any, any](ErrUnavailable("Native.insertAtCursor: no document in this runtime"))
	}
	NativeInstallCaretTracker()
	el, err := safeJsCall(doc, "getElementById", id)
	if err != nil || !el.Truthy() {
		return Err[any, any](makeError(5, "NotFound", "Native.insertAtCursor: no element with id \""+id+"\""))
	}
	if !isTextControl(el) || !hasFn(el, "setRangeText") {
		return Err[any, any](ErrInvalidInput("Native.insertAtCursor: the element with id \"" + id +
			"\" is not a textarea or a text input"))
	}
	// The value's length in UTF-16 code units, the unit of selectionStart and
	// setRangeText.
	length := 0
	if v := el.Get("value"); v.Type() == js.TypeString {
		length = utf16Len(v.String())
	}
	start, end := length, length
	active := doc.Get("activeElement")
	if active.Truthy() && el.Equal(active) {
		start, end = selectionRange(el)
	} else if sel, ok := spaCarets[id]; ok {
		start, end = sel.start, sel.end
	}
	start = max(0, min(start, length))
	end = max(start, min(end, length))
	if _, err := safeJsCall(el, "setRangeText", text, start, end, "end"); err != nil {
		return Err[any, any](ErrInvalidInput("Native.insertAtCursor: " + err.Error()))
	}
	caret := start + utf16Len(text)
	if hasFn(el, "focus") {
		_, _ = safeJsCall(el, "focus")
	}
	// Focus does not move a control's selection in a browser, but set it
	// again so the caret is after the inserted text on every engine.
	if hasFn(el, "setSelectionRange") {
		_, _ = safeJsCall(el, "setSelectionRange", caret, caret)
	}
	spaCarets[id] = spaCaretSel{start: caret, end: caret}
	value := ""
	if v := el.Get("value"); v.Type() == js.TypeString {
		value = v.String()
	}
	return Ok[any, any](value)
}

// utf16Len is s's length in UTF-16 code units, the unit of a JS string's
// length and of the selection API.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

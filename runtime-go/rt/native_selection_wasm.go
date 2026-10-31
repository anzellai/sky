//go:build js

package rt

import (
	"syscall/js"
	"unicode/utf16"
)

// Std.Native.selection / setSelection in the browser: read and set the
// selection of a textarea or a text input. They share insertAtCursor's caret
// tracker (native_cursor_wasm.go): a control that is not the focused element
// reports the last selection the person made in it, so a toolbar button that
// took the focus still sees where the person was.
//
// Positions cross to Sky in CHARACTERS (Unicode code points, the unit of
// String.slice and String.length). The browser's selection API counts UTF-16
// code units, so each position is converted on the way in and out: "😀" is one
// character to Sky and two units to the browser.

// u16ToChars converts a UTF-16 offset into s to a code-point offset.
func u16ToChars(s string, off int) int {
	n, u := 0, 0
	for _, r := range s {
		if u >= off {
			break
		}
		u += utf16.RuneLen(r)
		n++
	}
	return n
}

// charsToU16 converts a code-point offset into s to a UTF-16 offset, clamped
// to the end of s.
func charsToU16(s string, chars int) int {
	n, u := 0, 0
	for _, r := range s {
		if n >= chars {
			break
		}
		u += utf16.RuneLen(r)
		n++
	}
	return u
}

// sliceChars is s[start:end] in code points.
func sliceChars(s string, start, end int) string {
	rs := []rune(s)
	start = max(0, min(start, len(rs)))
	end = max(start, min(end, len(rs)))
	return string(rs[start:end])
}

// nativeTextControl finds the text control with this id for the named
// function: Err Unavailable without a document, NotFound for an unknown id,
// InvalidInput for an element that is not a textarea or a text input.
func nativeTextControl(fn, id string) (js.Value, js.Value, any) {
	doc := js.Global().Get("document")
	if !doc.Truthy() || doc.Get("getElementById").Type() != js.TypeFunction {
		return js.Value{}, js.Value{}, ErrUnavailable(fn + ": no document in this runtime")
	}
	NativeInstallCaretTracker()
	el, err := safeJsCall(doc, "getElementById", id)
	if err != nil || !el.Truthy() {
		return js.Value{}, js.Value{}, makeError(5, "NotFound", fn+": no element with id \""+id+"\"")
	}
	if !isTextControl(el) {
		return js.Value{}, js.Value{}, ErrInvalidInput(fn + ": the element with id \"" + id +
			"\" is not a textarea or a text input")
	}
	return doc, el, nil
}

func controlValue(el js.Value) string {
	if v := el.Get("value"); v.Type() == js.TypeString {
		return v.String()
	}
	return ""
}

// Native_selection is the Std.Native.selection kernel
// (`String -> Task Error Selection`).
func Native_selection(elementID any) any {
	id := AsString(elementID)
	return func() any { return nativeSelection(id) }
}

func nativeSelection(id string) SkyResult[any, any] {
	doc, el, e := nativeTextControl("Native.selection", id)
	if e != nil {
		return Err[any, any](e)
	}
	value := controlValue(el)
	length := utf16Len(value)
	start, end := length, length
	if active := doc.Get("activeElement"); active.Truthy() && el.Equal(active) {
		start, end = selectionRange(el)
	} else if sel, ok := spaCarets[id]; ok {
		start, end = sel.start, sel.end
	}
	start = max(0, min(start, length))
	end = max(start, min(end, length))
	cs, ce := u16ToChars(value, start), u16ToChars(value, end)
	return Ok[any, any](map[string]any{
		"start": cs,
		"end":   ce,
		"text":  sliceChars(value, cs, ce),
	})
}

// Native_setSelection is the Std.Native.setSelection kernel
// (`String -> Int -> Int -> Task Error ()`). It clamps the range to the
// value (a start past the end is the end; an end before the start is the
// start), focuses the control and selects the range.
func Native_setSelection(elementID any, start any, end any) any {
	id, s, en := AsString(elementID), AsInt(start), AsInt(end)
	return func() any { return nativeSetSelection(id, s, en) }
}

func nativeSetSelection(id string, start, end int) SkyResult[any, any] {
	_, el, e := nativeTextControl("Native.setSelection", id)
	if e != nil {
		return Err[any, any](e)
	}
	value := controlValue(el)
	chars := len([]rune(value))
	start = max(0, min(start, chars))
	end = max(start, min(end, chars))
	s16, e16 := charsToU16(value, start), charsToU16(value, end)
	if hasFn(el, "focus") {
		_, _ = safeJsCall(el, "focus")
	}
	if !hasFn(el, "setSelectionRange") {
		return Err[any, any](ErrInvalidInput("Native.setSelection: the element with id \"" + id +
			"\" has no selection"))
	}
	if _, err := safeJsCall(el, "setSelectionRange", s16, e16); err != nil {
		return Err[any, any](ErrInvalidInput("Native.setSelection: " + err.Error()))
	}
	spaCarets[id] = spaCaretSel{start: s16, end: e16}
	return Ok[any, any](struct{}{})
}

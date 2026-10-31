//go:build js && wasm

package nativecursorwasm

import (
	"syscall/js"
	"testing"

	"sky-app/rt"
)

func selection(t *testing.T, id string) rt.SkyResult[any, any] {
	t.Helper()
	task, ok := rt.Native_selection(id).(func() any)
	if !ok {
		t.Fatalf("Native_selection must return a Task thunk")
	}
	return task().(rt.SkyResult[any, any])
}

func setSelection(t *testing.T, id string, s, e int) rt.SkyResult[any, any] {
	t.Helper()
	task, ok := rt.Native_setSelection(id, s, e).(func() any)
	if !ok {
		t.Fatalf("Native_setSelection must return a Task thunk")
	}
	return task().(rt.SkyResult[any, any])
}

func sel(t *testing.T, r rt.SkyResult[any, any]) (int, int, string) {
	t.Helper()
	if r.Tag != 0 {
		t.Fatalf("want Ok, got Err %+v", r.ErrValue)
	}
	m := r.OkValue.(map[string]any)
	return m["start"].(int), m["end"].(int), m["text"].(string)
}

func range16(el js.Value) (int, int) {
	return el.Get("selectionStart").Int(), el.Get("selectionEnd").Int()
}

func TestWasmSelection(t *testing.T) {
	sky := setup(t)

	t.Run("reads the focused control's selection", func(t *testing.T) {
		sky.Call("make", "TEXTAREA", "doc", "textarea", "Hello world")
		sky.Call("userSelect", "doc", 6, 11)
		s, e, text := sel(t, selection(t, "doc"))
		if s != 6 || e != 11 || text != "world" {
			t.Fatalf("got [%d, %d] %q", s, e, text)
		}
	})

	t.Run("reads the remembered selection when focus has moved to a menu", func(t *testing.T) {
		sky.Call("make", "TEXTAREA", "memo", "textarea", "Hello world")
		sky.Call("userSelect", "memo", 0, 5)
		sky.Call("pressMenu")
		sky.Call("driftCaret", "memo")
		s, e, text := sel(t, selection(t, "memo"))
		if s != 0 || e != 5 || text != "Hello" {
			t.Fatalf("got [%d, %d] %q", s, e, text)
		}
	})

	t.Run("a control never selected reports the end", func(t *testing.T) {
		sky.Call("make", "INPUT", "q", "search", "abc")
		sky.Call("pressMenu")
		s, e, text := sel(t, selection(t, "q"))
		if s != 3 || e != 3 || text != "" {
			t.Fatalf("got [%d, %d] %q", s, e, text)
		}
	})

	t.Run("positions are characters, not UTF-16 units", func(t *testing.T) {
		// "😀" is 2 UTF-16 units; "£" and "─" are 1 each.
		el := sky.Call("make", "TEXTAREA", "emoji", "textarea", "a😀£─b")
		sky.Call("userSelect", "emoji", 3, 5) // "£─" in UTF-16 units
		s, e, text := sel(t, selection(t, "emoji"))
		if s != 2 || e != 4 || text != "£─" {
			t.Fatalf("got [%d, %d] %q", s, e, text)
		}
		// setSelection 1..2 (the emoji) selects UTF-16 units 1..3.
		if r := setSelection(t, "emoji", 1, 2); r.Tag != 0 {
			t.Fatalf("setSelection: %+v", r.ErrValue)
		}
		if a, b := range16(el); a != 1 || b != 3 {
			t.Fatalf("browser range: got [%d, %d]", a, b)
		}
		if _, _, text := sel(t, selection(t, "emoji")); text != "😀" {
			t.Fatalf("read back: got %q", text)
		}
	})

	t.Run("setSelection focuses, clamps, and is remembered after a blur", func(t *testing.T) {
		el := sky.Call("make", "TEXTAREA", "draft", "textarea", "abcdef")
		sky.Call("pressMenu")
		if r := setSelection(t, "draft", 4, 99); r.Tag != 0 {
			t.Fatalf("setSelection: %+v", r.ErrValue)
		}
		if !sky.Call("active").Equal(el) {
			t.Fatal("setSelection must focus the control")
		}
		if a, b := range16(el); a != 4 || b != 6 {
			t.Fatalf("clamped range: got [%d, %d]", a, b)
		}
		// An end before the start collapses to the start; a negative start is 0.
		_ = setSelection(t, "draft", 3, 1)
		if a, b := range16(el); a != 3 || b != 3 {
			t.Fatalf("collapsed range: got [%d, %d]", a, b)
		}
		_ = setSelection(t, "draft", -5, 2)
		sky.Call("pressMenu")
		sky.Call("driftCaret", "draft")
		s, e, text := sel(t, selection(t, "draft"))
		if s != 0 || e != 2 || text != "ab" {
			t.Fatalf("remembered: got [%d, %d] %q", s, e, text)
		}
		// insertAtCursor then replaces that remembered selection.
		if got := okValue(t, insert(t, "draft", "X")); got != "Xcdef" {
			t.Fatalf("insert after setSelection: got %q", got)
		}
	})

	t.Run("an unknown id is Err NotFound", func(t *testing.T) {
		errKind(t, selection(t, "missing"), "NotFound")
		errKind(t, setSelection(t, "missing", 0, 0), "NotFound")
	})

	t.Run("an element that is not a text control is Err InvalidInput", func(t *testing.T) {
		sky.Call("div", "panel")
		errKind(t, selection(t, "panel"), "InvalidInput")
		errKind(t, setSelection(t, "panel", 0, 0), "InvalidInput")
		sky.Call("make", "INPUT", "age", "number", "42")
		errKind(t, selection(t, "age"), "InvalidInput")
	})

	t.Run("no document is Err Unavailable", func(t *testing.T) {
		js.Global().Call("eval", "delete globalThis.document")
		errKind(t, selection(t, "doc"), "Unavailable")
		errKind(t, setSelection(t, "doc", 0, 1), "Unavailable")
	})
}

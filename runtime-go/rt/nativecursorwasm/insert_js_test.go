//go:build js && wasm

// Package nativecursorwasm runs Std.Native.insertAtCursor INSIDE Go's js/wasm
// build, the way a Sky.Spa client runs it in the browser, against a small
// stand-in for the DOM (a document, text controls with the selection API and
// setRangeText, and capture listeners). It is driven by
// rt/native_cursor_node_test.go, which builds this package for js/wasm and
// runs it under Node.js.
package nativecursorwasm

import (
	"fmt"
	"reflect"
	"syscall/js"
	"testing"

	"sky-app/rt"
)

// stubDOM is the stand-in. setRangeText follows the HTML spec: it replaces
// [start, end) and, with "end", puts the caret after the new text. It throws
// for an input type with no text selection, like a browser.
const stubDOM = `
(function () {
  const listeners = {};
  const fire = (type, target) => { for (const f of (listeners[type] || [])) f({ type: type, target: target }); };
  const textTypes = ["", "text", "search", "url", "tel", "password"];
  class Ctl {
    constructor(tag, id, type, value) {
      this.tagName = tag; this.id = id; this.type = type; this.value = value;
      this.selectionStart = value.length; this.selectionEnd = value.length;
    }
    setRangeText(rep, start, end, mode) {
      if (this.tagName === "INPUT" && !textTypes.includes(this.type)) throw new Error("InvalidStateError: no selection on this input");
      if (start > end) throw new Error("IndexSizeError");
      const n = this.value.length; start = Math.min(start, n); end = Math.min(end, n);
      this.value = this.value.slice(0, start) + rep + this.value.slice(end);
      if (mode === "end") { this.selectionStart = this.selectionEnd = start + rep.length; }
    }
    setSelectionRange(s, e) { this.selectionStart = s; this.selectionEnd = e; }
    focus() {
      const prev = document.activeElement;
      if (prev === this) return;
      if (prev && prev !== document.body) fire("focusout", prev);
      document.activeElement = this;
    }
  }
  const els = {};
  const button = { tagName: "BUTTON", id: "menu" };
  const document = {
    body: { tagName: "BODY" },
    activeElement: null,
    getElementById: (id) => els[id] || null,
    addEventListener(t, f) { (listeners[t] = listeners[t] || []).push(f); },
  };
  document.activeElement = document.body;
  globalThis.document = document;
  globalThis.__sky = {
    make(tag, id, type, value) { const c = new Ctl(tag, id, type, value); els[id] = c; return c; },
    div(id) { els[id] = { tagName: "DIV", id: id }; },
    // The person clicks into a control and selects [s, e).
    userSelect(id, s, e) {
      const c = els[id]; c.focus(); c.selectionStart = s; c.selectionEnd = e;
      fire("select", c); fire("pointerup", c); fire("selectionchange", document);
    },
    // The person presses the Insert menu button: focus leaves the control.
    pressMenu() {
      const prev = document.activeElement;
      if (prev && prev !== document.body) fire("focusout", prev);
      document.activeElement = button;
    },
    // A blurred control's caret moves without the person: a value write by
    // the patcher, which a browser reports as a selectionchange.
    driftCaret(id) {
      const c = els[id]; c.selectionStart = c.selectionEnd = c.value.length;
      fire("selectionchange", c); fire("selectionchange", document); fire("input", c);
    },
    get(id) { return els[id]; },
    active() { return document.activeElement; },
  };
})();
`

func setup(t *testing.T) js.Value {
	t.Helper()
	js.Global().Call("eval", stubDOM)
	rt.NativeInstallCaretTracker()
	return js.Global().Get("__sky")
}

func insert(t *testing.T, id, text string) rt.SkyResult[any, any] {
	t.Helper()
	task, ok := rt.Native_insertAtCursor(id, text).(func() any)
	if !ok {
		t.Fatalf("Native_insertAtCursor must return a Task thunk")
	}
	res, ok := task().(rt.SkyResult[any, any])
	if !ok {
		t.Fatalf("the task must yield a Result")
	}
	return res
}

func okValue(t *testing.T, r rt.SkyResult[any, any]) string {
	t.Helper()
	if r.Tag != 0 {
		t.Fatalf("want Ok, got Err %+v", r.ErrValue)
	}
	return r.OkValue.(string)
}

// errorKinds are the Sky Error kind tags (rt.makeError) the kernel returns.
var errorKinds = map[string]int{"NotFound": 5, "InvalidInput": 7, "Unavailable": 9}

func errKind(t *testing.T, r rt.SkyResult[any, any], kind string) {
	t.Helper()
	if r.Tag != 1 {
		t.Fatalf("want Err %s, got Ok %v", kind, r.OkValue)
	}
	// An Error carries its kind as the enum tag in Fields[0].
	fields := reflect.ValueOf(r.ErrValue).FieldByName("Fields")
	if !fields.IsValid() || fields.Len() == 0 {
		t.Fatalf("want a Sky Error, got %+v", r.ErrValue)
	}
	if tag, ok := fields.Index(0).Interface().(int); !ok || tag != errorKinds[kind] {
		t.Fatalf("want an Err of kind %s, got %s", kind, fmt.Sprintf("%+v", r.ErrValue))
	}
}

func caret(t *testing.T, el js.Value, want int) {
	t.Helper()
	s, e := el.Get("selectionStart").Int(), el.Get("selectionEnd").Int()
	if s != want || e != want {
		t.Fatalf("caret: got [%d, %d], want %d", s, e, want)
	}
}

func TestWasmInsertAtCursor(t *testing.T) {
	sky := setup(t)

	t.Run("inserts at a middle caret in the focused control", func(t *testing.T) {
		el := sky.Call("make", "TEXTAREA", "body", "textarea", "Hello world")
		sky.Call("userSelect", "body", 6, 6)
		got := okValue(t, insert(t, "body", "{{first_name}}"))
		if got != "Hello {{first_name}}world" || el.Get("value").String() != got {
			t.Fatalf("value: got %q", got)
		}
		caret(t, el, 20)
		if !sky.Call("active").Equal(el) {
			t.Fatal("the control must have focus after the insert")
		}
	})

	t.Run("replaces the selected text", func(t *testing.T) {
		el := sky.Call("make", "INPUT", "subject", "text", "Hello world")
		sky.Call("userSelect", "subject", 6, 11)
		got := okValue(t, insert(t, "subject", "there"))
		if got != "Hello there" {
			t.Fatalf("value: got %q", got)
		}
		caret(t, el, 11)
	})

	t.Run("uses the remembered selection when focus has moved to a menu", func(t *testing.T) {
		el := sky.Call("make", "TEXTAREA", "note", "textarea", "Hello world")
		sky.Call("userSelect", "note", 6, 6)
		sky.Call("pressMenu")
		// The blurred control's own caret moves to the end; that move must
		// not replace the person's selection.
		sky.Call("driftCaret", "note")
		got := okValue(t, insert(t, "note", "{{first_name}}"))
		if got != "Hello {{first_name}}world" {
			t.Fatalf("value: got %q", got)
		}
		caret(t, el, 20)
		if !sky.Call("active").Equal(el) {
			t.Fatal("the control must have focus after the insert")
		}
		// A second insert from the menu goes after the first.
		sky.Call("pressMenu")
		got = okValue(t, insert(t, "note", "!"))
		if got != "Hello {{first_name}}!world" {
			t.Fatalf("second insert: got %q", got)
		}
	})

	t.Run("a control never selected inserts at the end", func(t *testing.T) {
		sky.Call("make", "TEXTAREA", "fresh", "textarea", "abc")
		sky.Call("pressMenu")
		if got := okValue(t, insert(t, "fresh", "d")); got != "abcd" {
			t.Fatalf("value: got %q", got)
		}
	})

	t.Run("an unknown id is Err NotFound", func(t *testing.T) {
		errKind(t, insert(t, "nope", "x"), "NotFound")
	})

	t.Run("an element that is not a text control is Err InvalidInput", func(t *testing.T) {
		sky.Call("div", "box")
		errKind(t, insert(t, "box", "x"), "InvalidInput")
		sky.Call("make", "INPUT", "mail", "email", "a@b.c")
		errKind(t, insert(t, "mail", "x"), "InvalidInput")
	})

	t.Run("no document is Err Unavailable", func(t *testing.T) {
		js.Global().Call("eval", "delete globalThis.document")
		errKind(t, insert(t, "body", "x"), "Unavailable")
	})
}

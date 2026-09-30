package rt

import (
	"fmt"
	"testing"
)

type asMapRec struct{ N int }

func asMapPanic(fn func()) (msg string) {
	defer func() {
		if r := recover(); r != nil {
			msg = fmt.Sprintf("%v", r)
		}
	}()
	fn()
	return ""
}

// TestAsMapT_FailsLoudly is the runtime half of C-5. A Go `map[int]string`
// returned through FFI reached AsMapT, which found no string key and returned
// nil: the Dict came back EMPTY, a silent wrong answer (C/f2: `size=0`). A
// value that is not a string-keyed map, or an entry that cannot become the
// Dict's value type, is now a classified CoerceFailure. (S3c converts
// non-string keys in the FFI wrapper; this is the backstop.) A nil map is
// still the empty Dict.
func TestAsMapT_FailsLoudly(t *testing.T) {
	for name, fn := range map[string]func(){
		"map[int]string": func() { AsMapT[string](map[int]string{1: "a", 2: "b"}) },
		"an Int":         func() { AsMapT[int](42) },
		"a List":         func() { AsMapT[int]([]any{1}) },
		"a bad value":    func() { AsMapT[asMapRec](map[string]any{"k": "not a record"}) },
	} {
		msg := asMapPanic(fn)
		if msg == "" {
			t.Errorf("%s: AsMapT returned instead of failing", name)
			continue
		}
		if kind, _ := classifyPanic(msg); kind != "CoerceFailure" {
			t.Errorf("%s: panicked %q, classified %s, want CoerceFailure", name, msg, kind)
		}
	}
	if m := AsMapT[int](nil); len(m) != 0 {
		t.Errorf("AsMapT(nil) = %v, want the empty Dict", m)
	}
	if m := AsMapT[string](map[string]any{"a": "x", "b": 3}); m["a"] != "x" || m["b"] != "3" {
		t.Errorf("the string-widening path changed: %v", m)
	}
	if m := AsMapT[int](map[string]int{"a": 1}); m["a"] != 1 {
		t.Errorf("a typed map lost its entries: %v", m)
	}
}

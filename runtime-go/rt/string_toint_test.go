package rt

// Regression tests asserting String.toInt / String.toFloat return
// `Maybe` from BOTH the any-typed legacy dispatch path
// (String_toInt / String_toFloat) and the typed-codegen path
// (String_toIntT / String_toFloatT). The kernel type registered in
// lookupKernelType is `String -> Maybe Int` (and similarly for
// Float), so user patterns like
//     case String.toInt s of
//         Nothing -> ...
//         Just n  -> ...
// must work whichever path the compiler dispatches through.
//
// Pre-v0.9.10 the typed companion returned SkyResult[string, _] —
// mismatching the declared type — so the same Sky source compiled
// fine via the any-path but panicked at the case match when typed-
// codegen lowered the call site. Surfaced by a downstream app.

import "testing"

func TestStringToInt_AnyPathReturnsMaybe(t *testing.T) {
	out := String_toInt("42")
	m, ok := out.(SkyMaybe[any])
	if !ok {
		t.Fatalf("any-path: expected SkyMaybe[any], got %T %v", out, out)
	}
	if m.Tag != 0 {
		t.Errorf("any-path: '42' should be Just, got Tag=%d", m.Tag)
	}
	if v, ok := m.JustValue.(int); !ok || v != 42 {
		t.Errorf("any-path: Just payload want int 42, got %T %v", m.JustValue, m.JustValue)
	}

	bad := String_toInt("abc")
	mb, ok := bad.(SkyMaybe[any])
	if !ok {
		t.Fatalf("any-path bad: expected SkyMaybe[any], got %T %v", bad, bad)
	}
	if mb.Tag != 1 {
		t.Errorf("any-path bad: 'abc' should be Nothing, got Tag=%d", mb.Tag)
	}
}

func TestStringToInt_TypedPathReturnsMaybe(t *testing.T) {
	// Pre-v0.9.10 this was SkyResult[string, int]; now SkyMaybe[int]
	// to match the kernel-declared `String -> Maybe Int` shape.
	out := String_toIntT("42")
	if out.Tag != 0 {
		t.Errorf("typed: '42' should be Just (Tag=0), got Tag=%d", out.Tag)
	}
	if out.JustValue != 42 {
		t.Errorf("typed: Just payload want 42, got %d", out.JustValue)
	}

	bad := String_toIntT("abc")
	if bad.Tag != 1 {
		t.Errorf("typed bad: 'abc' should be Nothing (Tag=1), got Tag=%d", bad.Tag)
	}
}

func TestStringToFloat_TypedPathReturnsMaybe(t *testing.T) {
	out := String_toFloatT("3.14")
	if out.Tag != 0 {
		t.Errorf("typed: '3.14' should be Just (Tag=0), got Tag=%d", out.Tag)
	}
	if out.JustValue != 3.14 {
		t.Errorf("typed: Just payload want 3.14, got %v", out.JustValue)
	}

	bad := String_toFloatT("xyz")
	if bad.Tag != 1 {
		t.Errorf("typed bad: 'xyz' should be Nothing (Tag=1), got Tag=%d", bad.Tag)
	}
}

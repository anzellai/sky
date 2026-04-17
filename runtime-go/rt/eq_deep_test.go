package rt

// Audit P0-7: rt.Eq must do deep structural equality across both
// concrete and generic-instantiated Sky values. Pre-fix, Maybe values
// constructed at different generic instantiations (SkyMaybe[any]
// vs SkyMaybe[string]) compared as unequal even when both held
// Nothing — because the fields-by-name fallback compared the
// payload field which held different zero-value types (`nil` any
// vs `""` string) per instantiation.

import "testing"

func TestEq_MaybeNothingAcrossInstantiations(t *testing.T) {
	// Both Nothing, but different generic instantiations.
	a := Nothing[any]()
	b := Nothing[string]()
	if !asBool(Eq(a, b)) {
		t.Fatal("Eq(Nothing[any], Nothing[string]) should be true (both Nothing)")
	}
}

func TestEq_MaybeJustAcrossInstantiations(t *testing.T) {
	// Both Just-with-same-payload but different generic envelope.
	a := Just[any]("hello")
	b := Just[string]("hello")
	if !asBool(Eq(a, b)) {
		t.Fatal("Eq(Just[any] \"hello\", Just[string] \"hello\") should be true")
	}
}

func TestEq_MaybeMismatchedTagsStillFalse(t *testing.T) {
	a := Just[any]("hello")
	b := Nothing[any]()
	if asBool(Eq(a, b)) {
		t.Fatal("Eq(Just \"hello\", Nothing) should be false")
	}
}

func TestEq_ResultOkAcrossInstantiations(t *testing.T) {
	a := Ok[any, any]("payload")
	b := Ok[string, string]("payload")
	if !asBool(Eq(a, b)) {
		t.Fatal("Eq(Ok[any,any] \"payload\", Ok[string,string] \"payload\") should be true")
	}
}

func TestEq_ResultErrAcrossInstantiations(t *testing.T) {
	a := Err[any, any]("nope")
	b := Err[string, string]("nope")
	if !asBool(Eq(a, b)) {
		t.Fatal("Eq(Err \"nope\" across instantiations) should be true")
	}
}

func TestEq_ResultDoesNotConfuseOkVsErr(t *testing.T) {
	// Critical: pre-fix bug aside, the optimisation must not
	// accidentally treat Ok and Err as equal just because the
	// non-active payload happens to hold matching zero values.
	a := Ok[any, any](nil)
	b := Err[any, any](nil)
	if asBool(Eq(a, b)) {
		t.Fatal("Ok and Err should never compare equal regardless of payload")
	}
}

func TestEq_DeepListEquality(t *testing.T) {
	// Audit M10: Test.equal on lists previously didn't deep-compare
	// (workaround in user tests was to extract a scalar). Confirm
	// rt.Eq handles []any deep-comparison correctly.
	a := []any{1, 2, []any{"x", "y"}}
	b := []any{1, 2, []any{"x", "y"}}
	if !asBool(Eq(a, b)) {
		t.Fatal("Eq on equivalent nested []any should be true")
	}
	c := []any{1, 2, []any{"x", "z"}}
	if asBool(Eq(a, c)) {
		t.Fatal("Eq on differing nested []any should be false")
	}
}

func TestEq_DeepMapEquality(t *testing.T) {
	a := map[string]any{"k": []any{1, 2, 3}}
	b := map[string]any{"k": []any{1, 2, 3}}
	if !asBool(Eq(a, b)) {
		t.Fatal("Eq on equivalent map[string]any should be true")
	}
}

// helper: Eq returns `any` holding bool.
func asBool(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

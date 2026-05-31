package rt

import "testing"

// v0.15.45 — typed-key Dict.toList variants closing Limitation #10.
//
// Pre-fix, Dict.toList on a `Dict Int v` returned `[]SkyTuple2` with
// V0 typed as the underlying Go map's string keys (the runtime is
// always map[string]V regardless of the Sky-level key type), so
// arithmetic on the keys silently produced 0.  The new Int/Float
// typed-key entry points re-parse the string back to the original
// key type before building the tuple list.

func TestDictToListIntKeyParsesIntKeys(t *testing.T) {
	// Construct a Dict Int String via Dict_fromList (the existing
	// path that stringifies the Int keys for the underlying
	// map[string]any).
	pairs := []any{
		SkyTuple2{V0: 1, V1: "a"},
		SkyTuple2{V0: 2, V1: "b"},
		SkyTuple2{V0: 3, V1: "c"},
	}
	d := Dict_fromList(pairs)

	out := Dict_toListIntKey(d).([]any)
	if len(out) != 3 {
		t.Fatalf("expected 3 tuples, got %d", len(out))
	}

	// Every key should now be a real Int (not a String).  Sum them
	// to prove arithmetic works.
	sum := 0
	for _, item := range out {
		tup, ok := item.(SkyTuple2)
		if !ok {
			t.Fatalf("expected SkyTuple2, got %T", item)
		}
		// V0 must be `int` — pre-fix this would have been a `string`
		// like "1" and the type assertion would have failed.
		n, ok := tup.V0.(int)
		if !ok {
			t.Fatalf("V0 must be int, got %T (value %v)", tup.V0, tup.V0)
		}
		sum += n
	}
	if sum != 6 {
		t.Errorf("expected sum 1+2+3=6, got %d (keys silently round-tripped to 0?)", sum)
	}
}

func TestDictToListFloatKeyParsesFloatKeys(t *testing.T) {
	pairs := []any{
		SkyTuple2{V0: 1.5, V1: "a"},
		SkyTuple2{V0: 2.25, V1: "b"},
	}
	d := Dict_fromList(pairs)

	out := Dict_toListFloatKey(d).([]any)
	if len(out) != 2 {
		t.Fatalf("expected 2 tuples, got %d", len(out))
	}

	sum := 0.0
	for _, item := range out {
		tup, ok := item.(SkyTuple2)
		if !ok {
			t.Fatalf("expected SkyTuple2, got %T", item)
		}
		f, ok := tup.V0.(float64)
		if !ok {
			t.Fatalf("V0 must be float64, got %T (value %v)", tup.V0, tup.V0)
		}
		sum += f
	}
	if sum != 3.75 {
		t.Errorf("expected sum 1.5+2.25=3.75, got %v", sum)
	}
}

func TestDictToListIntKeyFallsBackOnUnparsableKey(t *testing.T) {
	// A malformed Dict Int v (e.g. constructed by FFI returning a
	// map[string]any where some keys aren't valid integers) should
	// not panic — fall back to 0 for the bad key.
	d := map[string]any{
		"42":  "ok",
		"bad": "noise",
	}
	out := Dict_toListIntKey(d).([]any)
	if len(out) != 2 {
		t.Fatalf("expected 2 tuples, got %d", len(out))
	}
	// Both entries should have int V0 (one parsed, one zero).
	for _, item := range out {
		tup := item.(SkyTuple2)
		if _, ok := tup.V0.(int); !ok {
			t.Errorf("V0 must be int, got %T", tup.V0)
		}
	}
}

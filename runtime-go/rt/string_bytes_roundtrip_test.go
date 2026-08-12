package rt

import "testing"

// `String.toBytes` / `String.fromBytes` must round-trip whichever Go slice
// shape the codegen hands them.
//
// `String_fromBytes` used to assert `bytes.([]any)` and return `""` on any
// other shape. That was invisible while the two members had no Sky
// signature — an untyped `toBytes` result stays `[]any` — and became a
// SILENT WRONG ANSWER the moment `toBytes : String -> List Int` was
// declared, because typed codegen then passes a Go `[]int`:
// `String.fromBytes (String.toBytes "hey")` evaluated to `""`.
//
// Both shapes are pinned here so neither the typed nor the untyped path can
// regress, and so a future revert of the signature cannot quietly re-open
// the other half.
func TestStringFromBytesRoundTripsBothSliceShapes(t *testing.T) {
	for _, s := range []string{"hey", "", "héllo ☃", "👨‍👩‍👧"} {
		boxed := String_toBytes(s) // []any of int — the untyped-codegen shape
		if got := AsString(String_fromBytes(boxed)); got != s {
			t.Fatalf("[]any round-trip: fromBytes(toBytes(%q)) = %q, want %q", s, got, s)
		}

		// The typed-codegen shape: `List Int` lowers to a Go []int.
		xs := AsList(boxed)
		typed := make([]int, len(xs))
		for i, v := range xs {
			typed[i] = AsInt(v)
		}
		if got := AsString(String_fromBytes(typed)); got != s {
			t.Fatalf("[]int round-trip: fromBytes(toBytes(%q)) = %q, want %q — a "+
				"typed slice must not fall through to the empty string", s, got, s)
		}
	}
}

func TestStringFromBytesOnNonSliceIsEmptyNotAPanic(t *testing.T) {
	if got := AsString(String_fromBytes(nil)); got != "" {
		t.Fatalf("fromBytes(nil) = %q, want \"\"", got)
	}
}

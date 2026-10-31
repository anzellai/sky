package rt

import (
	"testing"
)

// Regression fence for the v0.13.1 → v0.13.2 Std.Ui event-emission
// bug: `AsListT[any]` on a typed Go slice (e.g. `[]SkyADT` for
// `Std_Ui_Attribute`) returned nil, silently dropping every Sky.Live
// event attribute. The fix widens via reflect when T=any.
//
// User-facing symptom pre-fix: a downstream app and 19-skyforum rendered
// `<button>` elements with NO `sky-click=` / `sky-input=` attrs at
// all — the entire interactive surface was dead.
//
// The bug class: `var zero T; reflect.TypeOf(zero) == nil` when T=any
// (Go's reflect can't describe the dynamic type of the zero value of
// an interface).  Pre-fix code took `targetTy == nil` as a signal to
// abort with nil; post-fix it widens each element to `any` via
// `rv.Index(i).Interface()`.

func TestAsListTAny_WidensTypedSliceToAny(t *testing.T) {
	type myStruct struct {
		Name string
		N    int
	}
	src := []myStruct{
		{"alpha", 1},
		{"beta", 2},
	}
	out := AsListT[any](src)
	if len(out) != 2 {
		t.Fatalf("expected 2 elements, got %d (T=any path returned nil)", len(out))
	}
	if s, ok := out[0].(myStruct); !ok || s.Name != "alpha" {
		t.Fatalf("element 0 lost typing: %+v", out[0])
	}
	if s, ok := out[1].(myStruct); !ok || s.N != 2 {
		t.Fatalf("element 1 lost typing: %+v", out[1])
	}
}

func TestAsListTAny_TypedSkyADTSlice(t *testing.T) {
	// Mirrors the exact downstream-app / skyforum trigger: a typed
	// `[]SkyADT` slice (the alias the typed-codegen emits for
	// `Std_Ui_Attribute = SkyADT`) coerced to `[]any`.
	src := []SkyADT{
		{Tag: 0, SkyName: "Attr", Fields: []any{"style", "color: red"}},
		{Tag: 2, SkyName: "EventAttr", Fields: []any{"click", "Increment"}},
		{Tag: 0, SkyName: "Attr", Fields: []any{"class", "btn"}},
	}
	out := AsListT[any](src)
	if len(out) != 3 {
		t.Fatalf("typed []SkyADT lost elements: got %d/3", len(out))
	}
	// Spot-check the event attr survived — pre-fix it was silently
	// dropped at this very boundary, defeating Sky.Live event emission.
	adt, ok := out[1].(SkyADT)
	if !ok {
		t.Fatalf("element 1 type lost: %T", out[1])
	}
	if adt.SkyName != "EventAttr" {
		t.Fatalf("event attr corrupted: %+v", adt)
	}
}

func TestAsListTAny_PassesThroughAnySlice(t *testing.T) {
	// Sanity: the `[]any` fast-path was never affected; verify it
	// still returns the input verbatim.
	src := []any{"a", 1, true}
	out := AsListT[any](src)
	if len(out) != 3 {
		t.Fatalf("[]any input: expected 3, got %d", len(out))
	}
	if out[0] != "a" || out[1] != 1 || out[2] != true {
		t.Fatalf("element corruption: %+v", out)
	}
}

func TestAsListTAny_NilInput(t *testing.T) {
	out := AsListT[any](nil)
	if out != nil {
		t.Fatalf("nil input should return nil, got %+v", out)
	}
}

func TestAsListTAny_NonSlice(t *testing.T) {
	out := AsListT[any]("not a slice")
	if out != nil {
		t.Fatalf("non-slice should return nil, got %+v", out)
	}
}

package rt

import "testing"

// Regression: rt-side skyErrorAdt and Sky-side Sky_Core_Error_Error
// have the same memory layout (Tag int; SkyName string; Fields []any)
// but are distinct Go types. A user doing `case e of Error kind info ->`
// lowers to `any(e).(Sky_Core_Error_Error)` which panicked when `e`
// came from rt.ErrIo/ErrNetwork/etc because those produce skyErrorAdt,
// not Sky_Core_Error_Error.
//
// Mirror the Sky-emitted shape inside this test (same fields, different
// type) to detect whether our Err* builders return an `any` that's
// structurally compatible.
type testSkyErrorShape struct {
	Tag     int
	SkyName string
	Fields  []any
}

func TestErrIoAdtLayout(t *testing.T) {
	v := ErrIo("boom")
	// skyErrorAdt is an alias to SkyADT, so type-assert to the canonical name.
	adt, ok := v.(SkyADT)
	if !ok {
		t.Fatalf("ErrIo did not return SkyADT, got %T", v)
	}
	if adt.SkyName != "Error" {
		t.Fatalf("unexpected SkyName %q", adt.SkyName)
	}
	if len(adt.Fields) != 2 {
		t.Fatalf("expected 2 fields (kind, info), got %d", len(adt.Fields))
	}
}

// Regression for the skyvote panic: Sky-emitted ADT types are now
// aliases to rt.SkyADT, so rt-side ErrIo / ErrNetwork / etc. values
// are type-assertion compatible with user-declared Error struct types.
func TestErrorAdtAliasCompatibility(t *testing.T) {
	// Simulate what Sky codegen emits: `type UserError = rt.SkyADT`.
	type UserError = SkyADT

	// An rt-built error can be cast back through the user alias.
	v := ErrPermissionDenied("denied")
	_, ok := v.(UserError)
	if !ok {
		t.Fatalf("ErrPermissionDenied is not assignable to a SkyADT alias: %T", v)
	}
}

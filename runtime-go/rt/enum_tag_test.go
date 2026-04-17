package rt

// Regression: rt-built errors produce a `kind` field shaped as
// SkyADT, but Sky codegen optimises zero-arg ADTs (including
// ErrorKind) to a typed-int enum. A `case kind of Io -> ...`
// lowered to `__subject == Sky_Core_Error_ErrorKind_Io` never
// matched a rt-built kind — Go's `any == any` returns false when
// the boxed concrete types differ (SkyADT vs typed int). Every
// call to `Error.toString` / `Error.kindLabel` / `Error.isRetryable`
// on an rt-built error fell through to rt.Unreachable and panicked.
// Fix: codegen emits `rt.EnumTagIs(subject, N)` instead of `==`.

import "testing"


// A simulated user-generated zero-arg ADT enum — matches the
// `type Sky_Core_Error_ErrorKind int` + iota pattern codegen emits.
type fixtureKind int

const (
	fixtureIo fixtureKind = iota
	fixtureNetwork
	fixtureFfi
)


func TestEnumTagIs_TypedIntConstant(t *testing.T) {
	if !EnumTagIs(any(fixtureIo), 0) {
		t.Fatalf("EnumTagIs should match typed int zero-arg constant")
	}
	if !EnumTagIs(any(fixtureNetwork), 1) {
		t.Fatalf("EnumTagIs should match tag 1 for Network")
	}
	if EnumTagIs(any(fixtureIo), 1) {
		t.Fatalf("EnumTagIs should not match wrong tag")
	}
}


func TestEnumTagIs_SkyADTFromRuntime(t *testing.T) {
	// rt-built kind (SkyADT) compared against the tag codegen would
	// emit for the typed-int enum — both sides must be comparable.
	kind := errorKindAdt(7, "InvalidInput")
	if !EnumTagIs(kind, 7) {
		t.Fatalf("SkyADT kind with Tag=7 must match EnumTagIs(7)")
	}
	if EnumTagIs(kind, 0) {
		t.Fatalf("SkyADT kind with Tag=7 must not match EnumTagIs(0)")
	}
}


// End-to-end: pull the kind out of an rt.ErrIo just as Sky codegen
// does via rt.AdtField, and dispatch it against the integer tag.
func TestEnumTagIs_ErrIoRoundTrip(t *testing.T) {
	err := ErrIo("boom")
	kind := AdtField(err, 0)
	if !EnumTagIs(kind, 0) {
		t.Fatalf("ErrIo's kind field must satisfy EnumTagIs(0) (was the skyvote panic root cause)")
	}
	// Cross-check: does not match an unrelated tag.
	if EnumTagIs(kind, 7) {
		t.Fatalf("ErrIo's kind must not match InvalidInput's tag")
	}
}

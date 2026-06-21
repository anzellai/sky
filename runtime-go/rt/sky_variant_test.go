package rt

import "testing"

// fakeVariantFoo / fakeVariantBar exist purely to verify the
// SkyVariant dispatch contract: every emitted variant struct
// implements SkyVariantTag() + SkyVariantName(), and rt's
// dispatch helpers (AdtTag / AdtField / EnumTagIs) prefer the
// SkyVariant path over the legacy SkyADT path.
//
// Codegen will emit structs of this exact shape in P3+.

type fakeVariantFoo struct {
	V0 int
	V1 string
}

func (fakeVariantFoo) SkyVariantTag() int     { return 0 }
func (fakeVariantFoo) SkyVariantName() string { return "Foo" }

type fakeVariantBar struct{}

func (fakeVariantBar) SkyVariantTag() int     { return 1 }
func (fakeVariantBar) SkyVariantName() string { return "Bar" }

func TestSkyVariantAdtTag(t *testing.T) {
	if AdtTag(fakeVariantFoo{V0: 42, V1: "x"}) != 0 {
		t.Fatalf("expected SkyVariant Foo tag=0")
	}
	if AdtTag(fakeVariantBar{}) != 1 {
		t.Fatalf("expected SkyVariant Bar tag=1")
	}
	// Legacy SkyADT still works
	if AdtTag(SkyADT{Tag: 7, SkyName: "Legacy"}) != 7 {
		t.Fatalf("expected legacy SkyADT tag=7")
	}
}

func TestSkyVariantAdtField(t *testing.T) {
	f := fakeVariantFoo{V0: 42, V1: "hello"}
	if got := AdtField(f, 0); got != 42 {
		t.Fatalf("expected V0=42, got %v", got)
	}
	if got := AdtField(f, 1); got != "hello" {
		t.Fatalf("expected V1=hello, got %v", got)
	}
	if got := AdtField(f, 2); got != nil {
		t.Fatalf("expected out-of-range nil, got %v", got)
	}
	// Bar has no fields
	if got := AdtField(fakeVariantBar{}, 0); got != nil {
		t.Fatalf("expected nullary nil, got %v", got)
	}
}

func TestSkyVariantEnumTagIs(t *testing.T) {
	if !EnumTagIs(fakeVariantFoo{}, 0) {
		t.Fatalf("expected SkyVariant Foo == 0")
	}
	if EnumTagIs(fakeVariantFoo{}, 1) {
		t.Fatalf("expected SkyVariant Foo != 1")
	}
	if !EnumTagIs(fakeVariantBar{}, 1) {
		t.Fatalf("expected SkyVariant Bar == 1")
	}
	// Legacy SkyADT still works
	if !EnumTagIs(SkyADT{Tag: 5}, 5) {
		t.Fatalf("expected legacy SkyADT tag-5 match")
	}
}

// Verify SkyVariant takes priority — if a struct embedded SkyADT
// somehow (unlikely but possible), the SkyVariant branch wins.
// Not literally testable here without ambiguous types; documented
// for the architectural contract.

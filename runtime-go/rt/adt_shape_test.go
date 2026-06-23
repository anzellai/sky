package rt

import "testing"

// fakeShapeVariantNullary / fakeShapeVariantOne / fakeShapeVariantTwo
// model the codegen-emitted sealed-iface variant struct shape:
// V0, V1, ... payload fields, plus SkyVariantTag + SkyVariantName
// methods. These mirror what `emitSealedIfaceUnion` produces in
// Compile.hs for a parametric ADT flip.

type fakeShapeVariantNullary struct{}

func (fakeShapeVariantNullary) SkyVariantTag() int     { return 0 }
func (fakeShapeVariantNullary) SkyVariantName() string { return "Nullary" }

type fakeShapeVariantOne struct {
	V0 string
}

func (fakeShapeVariantOne) SkyVariantTag() int     { return 1 }
func (fakeShapeVariantOne) SkyVariantName() string { return "One" }

type fakeShapeVariantTwo struct {
	V0 int
	V1 string
}

func (fakeShapeVariantTwo) SkyVariantTag() int     { return 2 }
func (fakeShapeVariantTwo) SkyVariantName() string { return "Two" }

// TestUnwrapADTShape_LegacySkyADT — the legacy `SkyADT{Tag, SkyName, Fields}`
// shape must round-trip through the shim with byte-identical contents
// (no allocation of a new fields slice for the legacy fast path).
func TestUnwrapADTShape_LegacySkyADT(t *testing.T) {
	legacy := SkyADT{Tag: 3, SkyName: "Legacy", Fields: []any{1, "x", true}}
	name, tag, fields, ok := unwrapADTShape(legacy)
	if !ok {
		t.Fatalf("expected ok=true for legacy SkyADT")
	}
	if name != "Legacy" {
		t.Fatalf("expected name=Legacy, got %q", name)
	}
	if tag != 3 {
		t.Fatalf("expected tag=3, got %d", tag)
	}
	if len(fields) != 3 || fields[0] != 1 || fields[1] != "x" || fields[2] != true {
		t.Fatalf("expected fields=[1, x, true], got %v", fields)
	}
}

// TestUnwrapADTShape_NullaryVariant — a variant struct with no V*
// fields returns an empty (but non-nil-safe) fields slice; tag + name
// come from the SkyVariant interface methods.
func TestUnwrapADTShape_NullaryVariant(t *testing.T) {
	v := fakeShapeVariantNullary{}
	name, tag, fields, ok := unwrapADTShape(v)
	if !ok {
		t.Fatalf("expected ok=true for nullary variant")
	}
	if name != "Nullary" {
		t.Fatalf("expected name=Nullary, got %q", name)
	}
	if tag != 0 {
		t.Fatalf("expected tag=0, got %d", tag)
	}
	if len(fields) != 0 {
		t.Fatalf("expected len(fields)=0, got %d", len(fields))
	}
}

// TestUnwrapADTShape_VariantOneField + _VariantTwoFields — the shim
// collects V0, V1, ... fields in declaration order. This is the
// contract: any switch case in walkAttrs / layoutElement / colorOf
// that previously read `adt.Fields[i]` must read the same value via
// `fields[i]` after migration.
func TestUnwrapADTShape_VariantOneField(t *testing.T) {
	v := fakeShapeVariantOne{V0: "hello"}
	_, tag, fields, ok := unwrapADTShape(v)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if tag != 1 {
		t.Fatalf("expected tag=1, got %d", tag)
	}
	if len(fields) != 1 || fields[0] != "hello" {
		t.Fatalf("expected fields=[hello], got %v", fields)
	}
}

func TestUnwrapADTShape_VariantTwoFields(t *testing.T) {
	v := fakeShapeVariantTwo{V0: 42, V1: "world"}
	name, tag, fields, ok := unwrapADTShape(v)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if name != "Two" {
		t.Fatalf("expected name=Two, got %q", name)
	}
	if tag != 2 {
		t.Fatalf("expected tag=2, got %d", tag)
	}
	if len(fields) != 2 {
		t.Fatalf("expected len(fields)=2, got %d", len(fields))
	}
	if fields[0] != 42 || fields[1] != "world" {
		t.Fatalf("expected fields=[42, world], got %v", fields)
	}
}

// TestUnwrapADTShape_NonADT — a value that's neither legacy SkyADT
// nor a SkyVariant returns ok=false. Required: every migrated site
// has an `if !ok { return / continue / break }` guard, so behaviour
// must match the prior `_, ok := v.(SkyADT)` path.
func TestUnwrapADTShape_NonADT(t *testing.T) {
	for _, v := range []any{nil, 42, "string", []int{1, 2}, struct{}{}} {
		_, _, _, ok := unwrapADTShape(v)
		if ok {
			t.Fatalf("expected ok=false for non-ADT %v", v)
		}
	}
}

// TestUnwrapADTShape_PreservesBytesForRendererSwitch — verify the
// canonical case (legacy SkyADT shape, real Html/Attribute names)
// drives the renderer switch correctly. This is the load-bearing
// contract for HtmlToVNode + applyHtmlAttr migration.
func TestUnwrapADTShape_PreservesBytesForRendererSwitch(t *testing.T) {
	// HText case
	html := SkyADT{Tag: 0, SkyName: "HText", Fields: []any{"hello world"}}
	name, _, fields, ok := unwrapADTShape(html)
	if !ok || name != "HText" || len(fields) != 1 || fields[0] != "hello world" {
		t.Fatalf("HText round-trip failed: name=%q fields=%v", name, fields)
	}
	// Attr case (Attribute_Attr "class" "foo")
	attr := SkyADT{Tag: 0, SkyName: "Attr", Fields: []any{"class", "foo"}}
	name, _, fields, ok = unwrapADTShape(attr)
	if !ok || name != "Attr" || len(fields) != 2 ||
		fields[0] != "class" || fields[1] != "foo" {
		t.Fatalf("Attr round-trip failed: name=%q fields=%v", name, fields)
	}
}

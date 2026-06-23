package rt

import (
	"fmt"
	"reflect"
)

// adt_shape.go — v0.17 iter 89 — runtime shim for ADT introspection.
//
// During the v0.17 architectural close we are progressively migrating
// Sky ADTs from the legacy `SkyADT{Tag, SkyName, Fields:[]any}` shape
// to sealed-interface emission with per-variant concrete structs that
// implement `SkyVariant` and expose typed `V0`, `V1`, ... fields.
//
// Many runtime sites (HtmlToVNode, applyHtmlAttr, walkAttrs, etc.)
// were written when only the legacy shape existed and use a direct
// `v.(SkyADT)` type assertion + switch on `.SkyName` / `.Tag` /
// `.Fields[i]`.  Such sites would silently drop ADT values that
// arrive via the new sealed-interface path, since those values do
// NOT satisfy the SkyADT struct type.
//
// `unwrapADTShape` normalises both shapes to a uniform tuple so that
// the same downstream switch logic works against either producer.
// Wire it into a site by replacing:
//
//	adt, ok := v.(SkyADT)
//	if !ok { ... }
//	switch adt.SkyName { ... }
//	switch adt.Tag    { ... }
//	x := adt.Fields[0]
//
// with:
//
//	name, tag, fields, ok := unwrapADTShape(v)
//	if !ok { ... }
//	switch name { ... }
//	switch tag  { ... }
//	x := fields[0]
//
// This is ZERO BEHAVIOR CHANGE for legacy SkyADT values — the
// returned tuple carries the exact same `.Tag` / `.SkyName` /
// `.Fields` data the old code read directly.  For sealed-iface
// variant structs (produced when an ADT type is in the
// `sealedIfaceFlipAllowList` or `sealedIfaceFlipParametricAllowList`
// in Compile.hs) the shim introspects via the `SkyVariant`
// interface + reflected `V0..VN` fields.
//
// Convention (must match Compile.hs's `emitSealedIfaceUnion`):
//   * Variant struct names end with `_V` (e.g. `Main_Msg_Increment_V`)
//   * Methods `SkyVariantTag() int` and `SkyVariantName() string`
//   * Payload fields named `V0`, `V1`, `V2`, ... in declaration order
//
// Sites that need only one of (tag, name, fields) can pass the
// others through `_`.  Sites that hot-loop over many values may
// prefer the legacy fast path when they know they're consuming
// SkyADT exclusively; the shim's cost is one extra interface
// type-test + a reflect.Value alloc on the variant path.
//
// Companion to: AdtTag (rt.go:1977), AdtField (rt.go:2000),
// EnumTagIs (rt.go:3829), IsFinalisedAdt (adt_variant_factory.go).
// Those three helpers handle single-value introspection; this
// shim is the multi-field-extraction surface the renderer needs.
func unwrapADTShape(v any) (name string, tag int, fields []any, ok bool) {
	if v == nil {
		return "", -1, nil, false
	}
	// Legacy fast path: SkyADT struct value.
	if adt, isLegacy := v.(SkyADT); isLegacy {
		return adt.SkyName, adt.Tag, adt.Fields, true
	}
	// Sealed-iface variant path: the value implements SkyVariant
	// AND its underlying type is a struct whose payload fields are
	// named V0, V1, ... in declaration order.
	sv, isVariant := v.(SkyVariant)
	if !isVariant {
		return "", -1, nil, false
	}
	tag = sv.SkyVariantTag()
	name = sv.SkyVariantName()
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Struct {
		// Nullary variant whose underlying type isn't a struct — unusual
		// but possible (e.g. a typedef'd zero-size value).  Return empty
		// fields rather than panic.
		return name, tag, nil, true
	}
	// Collect V0, V1, ... in order, stopping at the first missing index.
	// We avoid rv.NumField()/Field(i) because the variant struct could
	// theoretically have unexported metadata fields in future revisions;
	// keying off "V<i>" gives a stable contract.
	rt := rv.Type()
	maxIdx := rv.NumField()
	out := make([]any, 0, maxIdx)
	for i := 0; i < maxIdx; i++ {
		fieldName := fmt.Sprintf("V%d", i)
		f, found := rt.FieldByName(fieldName)
		if !found {
			break
		}
		out = append(out, rv.FieldByIndex(f.Index).Interface())
	}
	return name, tag, out, true
}

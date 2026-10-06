// union_value.go — THE recogniser for a Sky union (custom type) value.
//
// # The defect this closes (v0.27.7)
//
// `type T = A | B | C Int` lowers to a sealed interface whose constructors are
// distinct Go struct types: `A` is `Main_T_A_V{}`, `B` is `Main_T_B_V{}`, `C 1`
// is `Main_T_C_V{V0: 1}`. Three runtime paths looked at such a value only as
// "a struct" and never at WHICH constructor it was:
//
//   - `deepEq` (behind `==`, `/=`, `List.member`, `Test.equal`,
//     `Sync.compareAndSwap`) fell into its "fields-by-name fallback for aliased
//     Sky ADTs": two structs of different types with the same field names
//     compare field by field. `A{}` and `B{}` have no fields, so `A == B` was
//     True; `C 1` and `D 1` both carry `V0 = 1`, so `C 1 == D 1` was True.
//   - `identityKey` (behind `Set`, `Cache` and the `Std.Ui.Lazy` fingerprint)
//     wrote every struct as `R<n>;<fields>`, so `A` and `B` both keyed `R0;`:
//     `Set.fromList [ A, B ]` held ONE element.
//   - `cmp` already ordered by constructor (C-11), but recognised a legacy
//     union by "an int `Tag` field next to a `Fields` field", which a user
//     record `{ tag : Int, fields : List a, name : String }` also matches.
//
// All three now ask this file. A union value is one of exactly four shapes,
// recognised by Go TYPE, never by field names a user record could also have:
//
//   - a sealed variant: a struct implementing `SkyVariant` (typed codegen);
//   - `rt.SkyADT` (an unsealed custom type is `type X = rt.SkyADT`);
//   - `rt.SkyMaybe[A]` and `rt.SkyResult[E, A]` at any instantiation.
//
// Two union values are equal exactly when they have the same constructor (tag,
// and name where both sides carry one) AND equal payloads.
package rt

import (
	"reflect"
	"strings"
)

type unionShape uint8

const (
	unionNone unionShape = iota
	unionSealed
	unionLegacy
	unionMaybe
	unionResult
)

var (
	skyADTType          = reflect.TypeOf(SkyADT{})
	skyVariantIfaceType = reflect.TypeOf((*SkyVariant)(nil)).Elem()
	rtPackagePath       = skyADTType.PkgPath()
)

// unionShapeOf classifies a Go type. Only struct types can be union values.
func unionShapeOf(t reflect.Type) unionShape {
	if t.Kind() != reflect.Struct {
		return unionNone
	}
	if t.Implements(skyVariantIfaceType) {
		return unionSealed
	}
	if t.PkgPath() != rtPackagePath {
		return unionNone
	}
	if t == skyADTType {
		return unionLegacy
	}
	name := t.Name()
	switch {
	case strings.HasPrefix(name, "SkyMaybe["):
		return unionMaybe
	case strings.HasPrefix(name, "SkyResult["):
		return unionResult
	}
	return unionNone
}

// unionCtor returns the constructor identity of a union value: its tag, and
// its constructor name when the shape carries one (sealed, legacy). It never
// calls `Value.Interface()` on rv, so it is safe on a value read out of an
// unexported field: a sealed variant's methods are value-receiver constants,
// so the ZERO value of its type answers them.
func unionCtor(rv reflect.Value, shape unionShape) (tag int, name string, hasName bool) {
	switch shape {
	case unionSealed:
		sv := reflect.Zero(rv.Type()).Interface().(SkyVariant)
		return sv.SkyVariantTag(), sv.SkyVariantName(), true
	case unionLegacy:
		return int(rv.FieldByName("Tag").Int()), rv.FieldByName("SkyName").String(), true
	case unionMaybe, unionResult:
		return int(rv.FieldByName("Tag").Int()), "", false
	}
	return 0, "", false
}

// unionPayloadValues lists the constructor arguments a union value carries, in
// order: a sealed variant's fields (V0, V1, …); a SkyADT's Fields; the
// JustValue of a Just; the OkValue of an Ok or the ErrValue of an Err. Never a
// zero-valued payload field of another constructor.
func unionPayloadValues(rv reflect.Value, shape unionShape, tag int) []reflect.Value {
	switch shape {
	case unionSealed:
		out := make([]reflect.Value, rv.NumField())
		for i := range out {
			out[i] = rv.Field(i)
		}
		return out
	case unionLegacy:
		f := rv.FieldByName("Fields")
		out := make([]reflect.Value, f.Len())
		for i := range out {
			out[i] = f.Index(i)
		}
		return out
	case unionMaybe:
		if tag == 0 {
			return []reflect.Value{rv.FieldByName("JustValue")}
		}
		return nil
	case unionResult:
		if tag == 0 {
			return []reflect.Value{rv.FieldByName("OkValue")}
		}
		return []reflect.Value{rv.FieldByName("ErrValue")}
	}
	return nil
}

// unionEq is deepEq's union arm. isUnion is false when neither side is a union
// value (the caller goes on to records and tuples). When exactly one side is a
// union the values are not equal.
func unionEq(ra, rb reflect.Value) (eq bool, isUnion bool) {
	sa, sb := unionShapeOf(ra.Type()), unionShapeOf(rb.Type())
	if sa == unionNone && sb == unionNone {
		return false, false
	}
	if sa == unionNone || sb == unionNone {
		return false, true
	}
	ta, na, hna := unionCtor(ra, sa)
	tb, nb, hnb := unionCtor(rb, sb)
	if ta != tb || (hna && hnb && na != nb) {
		return false, true
	}
	pa := unionPayloadValues(ra, sa, ta)
	pb := unionPayloadValues(rb, sb, tb)
	if len(pa) != len(pb) {
		return false, true
	}
	for i := range pa {
		if !pa[i].CanInterface() || !pb[i].CanInterface() {
			return reflect.DeepEqual(ra.Interface(), rb.Interface()), true
		}
		if !deepEq(pa[i].Interface(), pb[i].Interface()) {
			return false, true
		}
	}
	return true, true
}

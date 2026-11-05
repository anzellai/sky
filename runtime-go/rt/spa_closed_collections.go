package rt

import (
	"reflect"
	"strconv"
	"strings"
)

// Private capabilities plus exact nominal/direct layout checks. Promoted
// methods on embedding structs do not confer collection admission.
type spaClosedTupleMarker interface{ spaClosedTuple() }

func (T2[A, B]) spaClosedTuple()    {}
func (T3[A, B, C]) spaClosedTuple() {}

type spaClosedDictMarker interface {
	spaClosedDictElement() reflect.Type
	spaClosedDictSame(any) bool
}

func (SkyDict[V]) spaClosedDictElement() reflect.Type { return reflect.TypeOf((*V)(nil)).Elem() }
func (d SkyDict[V]) spaClosedDictSame(other any) bool {
	old, ok := other.(SkyDict[V])
	return ok && d.kids == old.kids && d.n == old.n && d.bm == old.bm && d.trie == old.trie && d.foreign == old.foreign
}

var spaClosedRuntimePkg = reflect.TypeOf(T2[int, int]{}).PkgPath()
var spaClosedTupleIface = reflect.TypeOf((*spaClosedTupleMarker)(nil)).Elem()
var spaClosedDictIface = reflect.TypeOf((*spaClosedDictMarker)(nil)).Elem()

func spaClosedNominalTuple(t reflect.Type) bool {
	if t.Kind() != reflect.Struct || t.PkgPath() != spaClosedRuntimePkg || !t.Implements(spaClosedTupleIface) {
		return false
	}
	n := t.NumField()
	if (n != 2 && n != 3) || !strings.HasPrefix(t.Name(), "T"+strconv.Itoa(n)+"[") {
		return false
	}
	for i := 0; i < n; i++ {
		f := t.Field(i)
		if f.Name != "V"+strconv.Itoa(i) || f.Anonymous || f.PkgPath != "" || f.Tag != "" {
			return false
		}
	}
	return true
}
func spaClosedNominalDict(t reflect.Type) bool {
	if t.Kind() != reflect.Struct || t.PkgPath() != spaClosedRuntimePkg || !strings.HasPrefix(t.Name(), "SkyDict[") || !t.Implements(spaClosedDictIface) || t.NumField() != 5 {
		return false
	}
	ref := reflect.TypeOf(SkyDict[int]{})
	for i := 0; i < 5; i++ {
		f, want := t.Field(i), ref.Field(i)
		if f.Name != want.Name || f.PkgPath != want.PkgPath || f.Anonymous || f.Tag != "" {
			return false
		}
		if i > 0 && f.Type != want.Type {
			return false
		}
	}
	kids := t.Field(0).Type
	return kids.Kind() == reflect.Pointer && kids.Elem().PkgPath() == spaClosedRuntimePkg && strings.HasPrefix(kids.Elem().Name(), "dslot[")
}

// MIXED-only witness. The cache retains its prior model owner and the caller
// holds the current owner; typed pointer comparison never uses uintptr or a
// global value registry. A changed immutable root takes full canonical encode.
func spaClosedMixedValueSame(a, b reflect.Value, budget *int) bool {
	if *budget <= 0 || !a.IsValid() || !b.IsValid() || a.Type() != b.Type() {
		return false
	}
	if spaClosedNominalDict(a.Type()) {
		*budget--
		return a.CanInterface() && b.CanInterface() && a.Interface().(spaClosedDictMarker).spaClosedDictSame(b.Interface())
	}
	if a.Kind() == reflect.Array || a.Kind() == reflect.Struct {
		*budget--
		if isSkyMaybeType(a.Type()) {
			ta, tb := a.FieldByName("Tag").Int(), b.FieldByName("Tag").Int()
			return ta == tb && (ta != 0 || spaClosedMixedValueSame(a.FieldByName("JustValue"), b.FieldByName("JustValue"), budget))
		}
		if a.Kind() == reflect.Array {
			for i := 0; i < a.Len(); i++ {
				if !spaClosedMixedValueSame(a.Index(i), b.Index(i), budget) {
					return false
				}
			}
		} else {
			for i := 0; i < a.NumField(); i++ {
				if !spaClosedMixedValueSame(a.Field(i), b.Field(i), budget) {
					return false
				}
			}
		}
		return true
	}
	return spaModelValueSame(a, b, budget)
}

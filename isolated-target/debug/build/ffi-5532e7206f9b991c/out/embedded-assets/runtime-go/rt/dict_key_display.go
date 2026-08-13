package rt

import (
	"fmt"
	"reflect"
	"strings"
)

// Rendering a Dict for a HUMAN.
//
// `encodeDictKey` writes a kind tag into the runtime map key so that a
// key-polymorphic helper can decode it back (see the typed-key section of
// rt.go). That tag is an INTERNAL representation: `toString` on a
// `Dict Int String` must still read `map[10:j 9:i]`, not `map[\x01i10:j …]`.
//
// So the display path undoes it. The cheap check comes first — a rendered
// string with no tag byte in it (every `Dict String v`, and every value that
// holds no Dict at all) is returned as-is, so the common path costs one extra
// `strings.IndexByte` over a string that was being built anyway. Only when a tag
// IS present does the reflective walk below run.
//
// The walk rebuilds values of the SAME Go type — a `map[string]int` stays a
// `map[string]int`, only its keys change — so a Dict nested inside a record,
// a list, a tuple or another Dict is detagged in place and the re-render is
// otherwise identical to what `%v` produced before.

// toStringForDisplay renders v the way `%v` does, with any encoded Dict key
// shown in its logical form.
func toStringForDisplay(v any) string {
	s := fmt.Sprintf("%v", v)
	if !strings.ContainsRune(s, dictKeyTagByte) {
		return s
	}
	if dv, ok := detagForDisplay(reflect.ValueOf(v), 0); ok {
		return fmt.Sprintf("%v", dv.Interface())
	}
	return s
}

// detagDisplayKey turns one encoded map key back into what the user wrote.
// An untagged key is its own display form.
func detagDisplayKey(raw string) string {
	if k, _, ok := decodeTaggedDictKey(raw); ok {
		if s, isStr := k.(string); isStr {
			return s
		}
		return fmt.Sprintf("%v", k)
	}
	return raw
}

// detagForDisplayDepth bounds the walk. Sky values are trees, but a Go value
// reached through `any` can be cyclic (a closure's captured state, an FFI
// handle), and a display helper must not hang. Eight levels is deeper than any
// record/Dict nesting that reads sensibly in a one-line `%v` anyway.
const detagForDisplayDepth = 8

// detagForDisplay returns a copy of rv with every string-keyed map's keys
// decoded, and ok=true when anything actually changed. ok=false means "nothing
// to do here" and the caller keeps the original value — so a shape this cannot
// rebuild (a struct with unexported fields, a channel, a func) degrades to the
// raw render rather than to a wrong one.
func detagForDisplay(rv reflect.Value, depth int) (reflect.Value, bool) {
	if !rv.IsValid() || depth > detagForDisplayDepth {
		return rv, false
	}
	switch rv.Kind() {
	case reflect.Interface, reflect.Pointer:
		if rv.IsNil() {
			return rv, false
		}
		inner, changed := detagForDisplay(rv.Elem(), depth+1)
		if !changed {
			return rv, false
		}
		if rv.Kind() == reflect.Interface {
			// Re-box: the interface's dynamic value is what changed.
			out := reflect.New(rv.Type()).Elem()
			if !inner.Type().Implements(rv.Type()) && rv.Type().NumMethod() != 0 {
				return rv, false
			}
			out.Set(inner)
			return out, true
		}
		out := reflect.New(inner.Type())
		out.Elem().Set(inner)
		return out, true

	case reflect.Map:
		if rv.IsNil() || rv.Type().Key().Kind() != reflect.String {
			return rv, false
		}
		out := reflect.MakeMapWithSize(rv.Type(), rv.Len())
		changed := false
		iter := rv.MapRange()
		for iter.Next() {
			k := iter.Key()
			display := detagDisplayKey(k.String())
			if display != k.String() {
				changed = true
			}
			nk := reflect.New(rv.Type().Key()).Elem()
			nk.SetString(display)
			nv, vChanged := detagForDisplay(iter.Value(), depth+1)
			if vChanged {
				changed = true
			} else {
				nv = iter.Value()
			}
			out.SetMapIndex(nk, nv)
		}
		if !changed {
			return rv, false
		}
		return out, true

	case reflect.Slice, reflect.Array:
		if rv.Kind() == reflect.Slice && rv.IsNil() {
			return rv, false
		}
		changed := false
		out := reflect.MakeSlice(sliceTypeFor(rv.Type()), rv.Len(), rv.Len())
		for i := 0; i < rv.Len(); i++ {
			ev, eChanged := detagForDisplay(rv.Index(i), depth+1)
			if eChanged {
				changed = true
			} else {
				ev = rv.Index(i)
			}
			out.Index(i).Set(ev)
		}
		if !changed {
			return rv, false
		}
		return out, true

	case reflect.Struct:
		t := rv.Type()
		out := reflect.New(t).Elem()
		changed := false
		for i := 0; i < t.NumField(); i++ {
			if t.Field(i).PkgPath != "" {
				// Unexported: cannot be copied, so leave the whole struct
				// alone rather than emit a half-populated one.
				return rv, false
			}
			fv, fChanged := detagForDisplay(rv.Field(i), depth+1)
			if fChanged {
				changed = true
			} else {
				fv = rv.Field(i)
			}
			out.Field(i).Set(fv)
		}
		if !changed {
			return rv, false
		}
		return out, true
	}
	return rv, false
}

// sliceTypeFor gives the slice type to rebuild into: arrays are rendered by
// `%v` the same way slices are, and a slice is settable element-wise.
func sliceTypeFor(t reflect.Type) reflect.Type {
	if t.Kind() == reflect.Array {
		return reflect.SliceOf(t.Elem())
	}
	return t
}

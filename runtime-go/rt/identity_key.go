// identity_key.go — THE injective identity of a Sky value.
//
// # The defect this replaces
//
// Three stores keyed themselves on `fmt.Sprintf("%v", value)`:
//
//   - `SkySet` (`stdlib_extra.go`) — `Sky.Core.Set`'s backing map,
//   - `Cache.getRaw/putRaw/removeRaw` (`cache_kernel.go`),
//   - `Std.Ui.Lazy`'s memoisation fingerprint (`lazy.go`, `lazyKey`).
//
// `%v` is not injective on composites. It joins a tuple's fields with a single
// space inside braces, so the tuples `( "a b", "c" )` and `( "a", "b c" )` both
// render `{a b c}`; it joins a list's elements the same way, so `[ "a b" ]` and
// `[ "a", "b" ]` both render `[a b]`. Two DISTINCT Sky values therefore shared
// one map key:
//
//	Set.fromList [ ( "a b", "c" ), ( "a", "b c" ) ]   -- size 1, not 2
//
// No error, no panic — one element of the user's data was simply gone. In the
// Cache the same collision returned a value that was never stored under the key
// asked for; in `Std.Ui.Lazy` it rendered a memoised subtree built from other
// arguments. `lazyKey` additionally joined its arguments with `|`, so a single
// argument containing a literal pipe forged an argument boundary.
//
// # Why a runtime fix and not an `[E2008]`-style check-time rejection
//
// `Dict` closed the same non-injectivity at CHECK time (`[E2008] UNSUPPORTED
// DICT KEY`, `rust/crates/ty/src/dictkey.rs`), restricting a key to `String`,
// `Int`, `Float`, `Char`, `Bool`. That is right for `Dict` and WRONG for these
// three, because they need different things from the key:
//
//   - A `Dict k v` is a Go `map[string]v` that must DECODE the key back out —
//     `toList` / `keys` / `foldl` / `map` all hand the user a `k` again. Decode
//     is definable for exactly those five types, so the check-time restriction
//     costs nothing that could have worked.
//   - A `Set a`, a `Cache k v` and a lazy fingerprint never reproduce the key.
//     `SkySet` stores the ORIGINAL element beside its key and `Set.toList`
//     returns that; `Cache.get` returns the stored VALUE; `lazyKey`'s string is
//     never read back. They need INJECTIVITY ONLY.
//
// Measured on `main` before this change, `Set` handled `Set ( Int, Int )`,
// `Set Color`, `Set (Maybe Int)`, `Set { x : Int, y : Int }` and
// `Set (List Int)` correctly — `%v` only fails to separate composites whose
// rendering is not self-delimiting, which in practice means composites
// containing STRINGS. Transplanting `[E2008]`'s five-type rule onto `Set` would
// have rejected every one of those working programs. Making the key injective
// costs nothing and fixes the composites too, so there is no `Set` analogue of
// `[E2008]`, deliberately. See `dictkey.rs` for the same statement from the
// other side.
//
// # The encoding
//
// A prefix-coded, self-delimiting grammar. Every variable-length item carries
// an explicit length or count, so the string can be parsed back unambiguously —
// which is exactly what injectivity means. (Nothing parses it; the property is
// what matters, not the round trip.)
//
//	N                nil / a nil pointer / a nil interface
//	b0 b1            false / true
//	i<decimal>;      every integer width, and a float that is an exact integer
//	f<shortest>;     any other float (Go's shortest round-tripping form)
//	c<complex>;      complex (defensive — Sky has no complex type)
//	s<len>:<bytes>   string — THE fix; the length is what `%v` was missing
//	Z                a nil slice
//	L<n>;<elem>…     slice / array
//	M<n>;<k><v>…     map, pairs sorted by encoded key (Go randomises map order)
//	R<n>;<field>…    struct, fields in declaration order
//	S<n>;<key>…      SkySet, its element keys sorted
//	P<elem>          non-nil pointer
//	p<hex>;          func / chan / unsafe pointer — reference identity
//
// # Scalars keep their current identity, on purpose
//
// An `int` 1, an `int64` 1 and a `float64` 1.0 are ONE Sky value that arrives
// at these kernels in whichever Go width the codegen picked, and they keyed the
// same under `%v`. They still do (`i1;`) — splitting them would turn
// `Set.fromList [ 1, 1 ]` into a two-element set at a typed/erased boundary,
// trading this bug for a mirror image of it. `TestIdentityKeyKeepsScalarEquivalences`
// pins that.
//
// Identity is STRUCTURAL, not Go-type-based, for the same reason: a
// `T2[string, string]` and a `T2[any, any]` holding the same pair are one Sky
// tuple that crossed the typed-codegen boundary (#461), and they must key alike.
package rt

import (
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// identityKeyMaxDepth bounds the walk. Sky values are finite immutable trees, so
// this is unreachable in practice; it exists so a pathological or hand-built Go
// value cannot take the process down with a stack overflow. At the limit the
// walk stops descending, which costs injectivity BELOW the limit only.
const identityKeyMaxDepth = 512

// identityKey returns a string that is equal for two Sky values exactly when
// those values are equal. Safe to call on any Go value, including one holding
// unexported fields: the walk never calls `Value.Interface()`.
func identityKey(v any) string {
	var b strings.Builder
	writeIdentityKey(&b, reflect.ValueOf(v), 0)
	return b.String()
}

func writeIdentityKey(b *strings.Builder, rv reflect.Value, depth int) {
	if !rv.IsValid() {
		b.WriteByte('N')
		return
	}
	if depth > identityKeyMaxDepth {
		// Deliberately terminal: no further structure is read.
		b.WriteByte('!')
		return
	}
	switch rv.Kind() {
	case reflect.Bool:
		if rv.Bool() {
			b.WriteString("b1")
		} else {
			b.WriteString("b0")
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		writeIntKey(b, rv.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		u := rv.Uint()
		if u <= math.MaxInt64 {
			writeIntKey(b, int64(u))
			return
		}
		b.WriteByte('i')
		b.WriteString(strconv.FormatUint(u, 10))
		b.WriteByte(';')
	case reflect.Float32, reflect.Float64:
		writeFloatKey(b, rv.Float(), rv.Kind() == reflect.Float32)
	case reflect.Complex64, reflect.Complex128:
		b.WriteByte('c')
		b.WriteString(strconv.FormatComplex(rv.Complex(), 'g', -1, 128))
		b.WriteByte(';')
	case reflect.String:
		// The whole defect, in one line: the LENGTH is what `%v` omitted.
		s := rv.String()
		b.WriteByte('s')
		b.WriteString(strconv.Itoa(len(s)))
		b.WriteByte(':')
		b.WriteString(s)
	case reflect.Slice:
		if rv.IsNil() {
			b.WriteByte('Z')
			return
		}
		fallthrough
	case reflect.Array:
		n := rv.Len()
		b.WriteByte('L')
		b.WriteString(strconv.Itoa(n))
		b.WriteByte(';')
		for i := 0; i < n; i++ {
			writeIdentityKey(b, rv.Index(i), depth+1)
		}
	case reflect.Map:
		if rv.IsNil() {
			b.WriteByte('N')
			return
		}
		writeMapKey(b, rv, depth)
	case reflect.Struct:
		// A SkySet's own identity is its ELEMENTS, not the internal map: two
		// equal sets built in a different order must key alike, and the
		// generic map arm below would already sort them — but going through
		// the item keys directly skips re-encoding every element.
		if s, ok := skySetValue(rv); ok {
			writeSkySetKey(b, s)
			return
		}
		n := rv.NumField()
		b.WriteByte('R')
		b.WriteString(strconv.Itoa(n))
		b.WriteByte(';')
		for i := 0; i < n; i++ {
			writeIdentityKey(b, rv.Field(i), depth+1)
		}
	case reflect.Pointer:
		if rv.IsNil() {
			b.WriteByte('N')
			return
		}
		b.WriteByte('P')
		writeIdentityKey(b, rv.Elem(), depth+1)
	case reflect.Interface:
		if rv.IsNil() {
			b.WriteByte('N')
			return
		}
		writeIdentityKey(b, rv.Elem(), depth+1)
	case reflect.Func, reflect.Chan, reflect.UnsafePointer:
		// No value identity exists for these. Reference identity is what `%v`
		// gave and what Go itself gives; `Set (Int -> Int)` is typeable in Sky
		// (`Set a` carries no `comparable` constraint) and must not panic.
		b.WriteByte('p')
		b.WriteString(strconv.FormatUint(uint64(rv.Pointer()), 16))
		b.WriteByte(';')
	default:
		b.WriteByte('?')
	}
}

func writeIntKey(b *strings.Builder, i int64) {
	b.WriteByte('i')
	b.WriteString(strconv.FormatInt(i, 10))
	b.WriteByte(';')
}

// writeFloatKey keeps a float that is an exact integer in the INTEGER space, so
// a Sky `Float` 1.0 and a Sky `Int` 1 key alike exactly as they did under `%v`.
// Everything else uses Go's shortest round-tripping form, which is injective on
// float64 (and on float32 at its own width).
func writeFloatKey(b *strings.Builder, f float64, is32 bool) {
	if !math.IsInf(f, 0) && !math.IsNaN(f) && f == math.Trunc(f) &&
		f >= math.MinInt64 && f <= math.MaxInt64 {
		writeIntKey(b, int64(f))
		return
	}
	bits := 64
	if is32 {
		bits = 32
	}
	b.WriteByte('f')
	b.WriteString(strconv.FormatFloat(f, 'g', -1, bits))
	b.WriteByte(';')
}

// writeMapKey sorts by ENCODED key. Go randomises map iteration order, so an
// unsorted walk would give one value a different identity on every call — every
// `Std.Ui.Lazy` render would miss its cache and a `Set` of dictionaries would
// hold duplicates.
func writeMapKey(b *strings.Builder, rv reflect.Value, depth int) {
	n := rv.Len()
	pairs := make([]string, 0, n)
	for iter := rv.MapRange(); iter.Next(); {
		var kb strings.Builder
		writeIdentityKey(&kb, iter.Key(), depth+1)
		writeIdentityKey(&kb, iter.Value(), depth+1)
		pairs = append(pairs, kb.String())
	}
	sort.Strings(pairs)
	b.WriteByte('M')
	b.WriteString(strconv.Itoa(n))
	b.WriteByte(';')
	for _, p := range pairs {
		b.WriteString(p)
	}
}

func writeSkySetKey(b *strings.Builder, s SkySet) {
	keys := make([]string, 0, len(s.items))
	for k := range s.items {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b.WriteByte('S')
	b.WriteString(strconv.Itoa(len(keys)))
	b.WriteByte(';')
	for _, k := range keys {
		b.WriteByte('s')
		b.WriteString(strconv.Itoa(len(k)))
		b.WriteByte(':')
		b.WriteString(k)
	}
}

// skySetValue recognises a SkySet without calling `Interface()` (which panics on
// a value read out of an unexported field).
func skySetValue(rv reflect.Value) (SkySet, bool) {
	if rv.Type() != skySetType {
		return SkySet{}, false
	}
	if !rv.CanInterface() {
		// Reached only from inside another struct's unexported field. Rebuild
		// the map by reflection rather than giving up.
		f := rv.Field(0)
		out := SkySet{items: make(map[string]any, f.Len())}
		for iter := f.MapRange(); iter.Next(); {
			out.items[iter.Key().String()] = nil
		}
		return out, true
	}
	return rv.Interface().(SkySet), true
}

var skySetType = reflect.TypeOf(SkySet{})

// identityLess is a TOTAL, DETERMINISTIC order for values `cmpSafe` cannot
// order. `skyLessThan`'s previous fallback compared `fmt.Sprintf("%v", …)`,
// which is the same non-injective rendering this file exists to replace: two
// distinct records rendered equal, compared equal, and then landed in whatever
// order the caller happened to present them — for `Set.toList` that is Go's
// randomised map iteration, so the same set printed differently run to run.
func identityLess(a, b any) bool {
	return identityKey(a) < identityKey(b)
}

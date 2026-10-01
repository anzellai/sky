package rt

// Go FFI value conversion (surface format 3).
//
// A `sky add` binding's typed wrapper hands Go values to Sky and Sky values
// to Go. Before format 3 the wrapper passed them through raw, and the Sky
// side assumed a shape the value did not have:
//
//   - a `*string` result reached Sky typed `String` and panicked at the
//     narrowing (C-4);
//   - a `map[int]string` result was typed `Dict String String`, and the
//     narrowing found no `map[string]` and produced an EMPTY Dict (C-5);
//   - a `uint64` result above the Int range wrapped to a negative number, and
//     an Int argument for a `uint8` or `int32` parameter was truncated (C-7);
//   - an opaque Go value was narrowed into a typed parameter slot OUTSIDE the
//     wrapper's recover, so a mismatch crashed the process (C-6).
//
// This file is the one place those shapes are converted. The rules mirror the
// inspector's `sky3` classification (`tools/sky-ffi-inspect`, `sky3Of`), which
// is what the checker types the binding by, so the Sky type and the converted
// value agree by construction:
//
//   - a Go integer of any width or signedness is Sky `Int`, range-checked in
//     both directions; a Go float is `Float`, range-checked into `float32`;
//   - a pointer to a non-opaque type (`*string`, `*int`, `*[]T`) is `Maybe`: nil
//     is `Nothing`;
//   - `[]byte` and `[N]byte` are `Bytes` (a Sky String); other arrays are
//     `List`;
//   - a map with a String, integer, float or Bool key is a `Dict` keyed the way
//     Sky keys it (`encodeDictKey`); any other map is opaque;
//   - a defined (named) type with a non-basic underlying type, an interface,
//     a func, a chan, a complex number or an unsafe pointer is OPAQUE: it
//     passes through unchanged, and a named `T` and `*T` adapt to each other
//     when a parameter wants the other one.
//
// Every conversion failure panics with an *FfiConvError. The wrapper's guard
// (`SkyFfiGuardT` / `SkyFfiGuard`) turns it into an `Err` with the message and
// no "panic:" prefix; any other panic keeps today's `Err "panic: …"`. So a
// conversion that cannot be made is a Result the program handles, never a
// crash and never a wrong value.

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"sync"
)

// FfiConvError is a Go FFI value that cannot be converted to the shape the
// other side requires: an integer out of range, a Go value of another type
// than the parameter, a fixed-size array of the wrong length.
type FfiConvError struct{ Msg string }

func (e *FfiConvError) Error() string { return e.Msg }

func ffiFail(format string, args ...any) {
	panic(&FfiConvError{Msg: "Go FFI: " + fmt.Sprintf(format, args...)})
}

// ffiPanicMessage renders a recovered panic for the Err a guard returns.
func ffiPanicMessage(r any) string {
	var ce *FfiConvError
	if e, ok := r.(error); ok && errors.As(e, &ce) {
		return ce.Msg
	}
	return fmt.Sprintf("panic: %v", r)
}

// SkyFfiGuardT is the recover a format-3 typed wrapper defers. A conversion
// failure becomes `Err` with its own message; any other panic becomes the
// same `Err "panic: …"` SkyFfiRecoverT gives.
func SkyFfiGuardT[A any](out *SkyResult[any, A]) func() {
	return func() {
		if r := recover(); r != nil {
			*out = Err[any, A](ErrFfi(ffiPanicMessage(r)))
		}
	}
}

// SkyFfiGuard is SkyFfiGuardT for an untyped (`any`) wrapper.
func SkyFfiGuard(out *any) func() {
	return func() {
		if r := recover(); r != nil {
			*out = Err[any, any](ErrFfi(ffiPanicMessage(r)))
		}
	}
}

var ffiAnyType = reflect.TypeOf((*any)(nil)).Elem()

// ffiDefined: a defined (named, package-level) type. Predeclared types
// (`int`, `string`, `error`) have an empty PkgPath.
func ffiDefined(t reflect.Type) bool {
	return t.Name() != "" && t.PkgPath() != ""
}

func ffiBasicKind(k reflect.Kind) bool {
	switch k {
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.String:
		return true
	}
	return false
}

// The per-type classification is pure; it is cached, because the wrappers
// ask it on every call.
var ffiOpaqueCache, ffiSameRepCache sync.Map

// ffiOpaque: a type Sky holds as an opaque Go value (no conversion).
func ffiOpaque(t reflect.Type) bool {
	if v, ok := ffiOpaqueCache.Load(t); ok {
		return v.(bool)
	}
	r := ffiOpaqueUncached(t)
	ffiOpaqueCache.Store(t, r)
	return r
}

func ffiOpaqueUncached(t reflect.Type) bool {
	if ffiDefined(t) && !ffiBasicKind(t.Kind()) {
		return true
	}
	switch t.Kind() {
	case reflect.Interface, reflect.Func, reflect.Chan, reflect.UnsafePointer,
		reflect.Complex64, reflect.Complex128:
		return true
	case reflect.Struct:
		return t.NumField() > 0
	case reflect.Ptr:
		return ffiOpaque(t.Elem())
	case reflect.Map:
		return !ffiDictKey(t.Key())
	}
	return false
}

// ffiSameRep: a type whose Go value IS its Sky value (string, int, float64,
// bool, and slices / String-keyed maps of those), so no copy is needed.
func ffiSameRep(t reflect.Type) bool {
	if v, ok := ffiSameRepCache.Load(t); ok {
		return v.(bool)
	}
	r := ffiSameRepUncached(t)
	ffiSameRepCache.Store(t, r)
	return r
}

func ffiSameRepUncached(t reflect.Type) bool {
	if ffiDefined(t) {
		return false
	}
	switch t.Kind() {
	case reflect.String, reflect.Int, reflect.Float64, reflect.Bool:
		return true
	case reflect.Slice:
		return !ffiIsByte(t.Elem()) && ffiSameRep(t.Elem())
	case reflect.Map:
		return t.Key().Kind() == reflect.String && !ffiDefined(t.Key()) && ffiSameRep(t.Elem())
	}
	return false
}

func ffiIsByte(t reflect.Type) bool {
	return t.Kind() == reflect.Uint8 && !ffiDefined(t)
}

// FfiRet converts a Go value of static type T to its Sky representation.
func FfiRet[T any](v T) any {
	return ffiToSky(reflect.ValueOf(&v).Elem())
}

// FfiCommaOk is the Sky `Maybe` of a Go `(T, bool)` result whose value is
// already converted.
func FfiCommaOk(v any, ok bool) SkyMaybe[any] {
	if ok {
		return Just[any](v)
	}
	return Nothing[any]()
}

// FfiArg converts a Sky value to the Go parameter type T.
func FfiArg[T any](v any) T {
	var zero T
	t := reflect.TypeOf(&zero).Elem()
	ffiRefuseSkyOwned(v, t)
	if tv, ok := v.(T); ok && !ffiNeedsDeepCheck(t) {
		return tv
	}
	out := ffiToGo(v, t)
	return out.Interface().(T)
}

// ffiRtPkgPath is the Go package path of this runtime.
var ffiRtPkgPath = reflect.TypeOf(FfiConvError{}).PkgPath()

// ffiSkyOwned: a value whose Go type the Sky runtime defines (a `Secret`, a
// `Std.Sync` handle, a key type, a Noise or CPace state, a Maybe or Result,
// ...). Such a value is Sky's own; Go code never receives it.
func ffiSkyOwned(t reflect.Type) bool {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t.PkgPath() == ffiRtPkgPath
}

// ffiRefuseSkyOwned: a Sky runtime value given to a Go interface slot is an
// Err. An annotated generic helper can reach such a slot past the checker
// (doc 14 §9.7); without this the value would pass whenever it implements
// the interface (`Secret` is a `fmt.Stringer` and a `json.Marshaler`). Plain
// primitives (String, Int, Float, Bool) have no package and still pass.
func ffiRefuseSkyOwned(v any, t reflect.Type) {
	if v == nil || t.Kind() != reflect.Interface {
		return
	}
	if dt := reflect.TypeOf(v); ffiSkyOwned(dt) {
		ffiFail("a Sky runtime value (%s) cannot be passed to the Go type %s (see docs/migration/v0.27.md#ffi-go-interface-params)", dt, t)
	}
}

// ffiNeedsDeepCheck: a value that already has the target Go type may still
// need no work (a string, an opaque handle), but a slice or map of
// convertible elements always arrives from Sky in Sky's own shape, so the
// fast path is only taken for a type whose Go and Sky shapes coincide.
func ffiNeedsDeepCheck(t reflect.Type) bool {
	if ffiSameRep(t) || ffiOpaque(t) {
		return false
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map, reflect.Ptr, reflect.Func:
		return true
	}
	return false
}

// ffiToSky: the Sky value for a Go value, by the value's STATIC type.
func ffiToSky(v reflect.Value) any {
	t := v.Type()
	if ffiSameRep(t) {
		return v.Interface()
	}
	if ffiOpaque(t) {
		if (t.Kind() == reflect.Interface || t.Kind() == reflect.Ptr || t.Kind() == reflect.Func ||
			t.Kind() == reflect.Map || t.Kind() == reflect.Slice || t.Kind() == reflect.Chan) && v.IsNil() {
			return nil
		}
		return v.Interface()
	}
	switch t.Kind() {
	case reflect.Bool:
		return v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return int(v.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		u := v.Uint()
		if u > math.MaxInt64 {
			ffiFail("%d (%s) is out of range for Int (see docs/migration/v0.27.md#ffi-integer-range)", u, t)
		}
		return int(u)
	case reflect.Float32, reflect.Float64:
		return v.Float()
	case reflect.String:
		return v.String()
	case reflect.Ptr:
		if v.IsNil() {
			return Nothing[any]()
		}
		return Just[any](ffiToSky(v.Elem()))
	case reflect.Slice:
		if ffiIsByte(t.Elem()) {
			return string(v.Bytes())
		}
		out := make([]any, v.Len())
		for i := range out {
			out[i] = ffiToSky(v.Index(i))
		}
		return out
	case reflect.Array:
		if ffiIsByte(t.Elem()) {
			b := make([]byte, v.Len())
			reflect.Copy(reflect.ValueOf(b), v)
			return string(b)
		}
		out := make([]any, v.Len())
		for i := range out {
			out[i] = ffiToSky(v.Index(i))
		}
		return out
	case reflect.Map:
		if !ffiDictKey(t.Key()) {
			if v.IsNil() {
				return nil
			}
			return v.Interface()
		}
		out := make(map[string]any, v.Len())
		it := v.MapRange()
		for it.Next() {
			out[encodeDictKey(ffiToSky(it.Key()))] = ffiToSky(it.Value())
		}
		return out
	case reflect.Struct:
		return struct{}{}
	}
	return v.Interface()
}

// ffiDictKey: a Go map key kind a Sky Dict can carry.
func ffiDictKey(k reflect.Type) bool {
	switch k.Kind() {
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
}

func ffiTypeName(v any) string {
	if v == nil {
		return "nothing"
	}
	return reflect.TypeOf(v).String()
}

// ffiToGo: the Go value of type t for a Sky value v.
func ffiToGo(v any, t reflect.Type) reflect.Value {
	if v == nil {
		// Sky holds a nil Go value (a nil pointer, interface, map, …) as an
		// opaque value it cannot test. It passes on only where Go has a nil.
		switch t.Kind() {
		case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.UnsafePointer:
			return reflect.Zero(t)
		}
		ffiFail("nil for %s", t)
	}
	rv := reflect.ValueOf(v)
	ffiRefuseSkyOwned(v, t)
	if ffiOpaque(t) {
		return ffiOpaqueToGo(rv, t)
	}
	switch t.Kind() {
	case reflect.Bool:
		if rv.Kind() != reflect.Bool {
			ffiFail("expected Bool, got %s", rv.Type())
		}
		return rv.Convert(t)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n := ffiSkyInt(rv, t)
		out := reflect.New(t).Elem()
		if out.OverflowInt(n) {
			ffiFail("%d is out of range for %s (see docs/migration/v0.27.md#ffi-integer-range)", n, t)
		}
		out.SetInt(n)
		return out
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		n := ffiSkyInt(rv, t)
		out := reflect.New(t).Elem()
		if n < 0 || out.OverflowUint(uint64(n)) {
			ffiFail("%d is out of range for %s (see docs/migration/v0.27.md#ffi-integer-range)", n, t)
		}
		out.SetUint(uint64(n))
		return out
	case reflect.Float32, reflect.Float64:
		var f float64
		switch rv.Kind() {
		case reflect.Float32, reflect.Float64:
			f = rv.Float()
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			f = float64(rv.Int())
		default:
			ffiFail("expected Float, got %s", rv.Type())
		}
		out := reflect.New(t).Elem()
		if !math.IsInf(f, 0) && !math.IsNaN(f) && out.OverflowFloat(f) {
			ffiFail("%g is out of range for %s (see docs/migration/v0.27.md#ffi-integer-range)", f, t)
		}
		out.SetFloat(f)
		return out
	case reflect.String:
		switch rv.Kind() {
		case reflect.String:
			return rv.Convert(t)
		case reflect.Slice:
			if ffiIsByte(rv.Type().Elem()) {
				return reflect.ValueOf(string(rv.Bytes())).Convert(t)
			}
		}
		ffiFail("expected String, got %s", rv.Type())
	case reflect.Ptr:
		// A pointer to a non-opaque type is a Sky Maybe.
		tag, just, ok := ffiMaybeParts(rv)
		if !ok {
			ffiFail("expected a Maybe for %s, got %s", t, rv.Type())
		}
		if tag != 0 {
			return reflect.Zero(t)
		}
		p := reflect.New(t.Elem())
		p.Elem().Set(ffiToGo(just, t.Elem()))
		return p
	case reflect.Slice:
		if ffiIsByte(t.Elem()) {
			return ffiBytes(rv, t, -1)
		}
		if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
			ffiFail("expected a List for %s, got %s", t, rv.Type())
		}
		out := reflect.MakeSlice(t, rv.Len(), rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out.Index(i).Set(ffiToGo(ffiElem(rv.Index(i)), t.Elem()))
		}
		return out
	case reflect.Array:
		if ffiIsByte(t.Elem()) {
			return ffiBytes(rv, t, t.Len())
		}
		if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
			ffiFail("expected a List for %s, got %s", t, rv.Type())
		}
		if rv.Len() != t.Len() {
			ffiFail("%s needs exactly %d elements, got %d", t, t.Len(), rv.Len())
		}
		out := reflect.New(t).Elem()
		for i := 0; i < rv.Len(); i++ {
			out.Index(i).Set(ffiToGo(ffiElem(rv.Index(i)), t.Elem()))
		}
		return out
	case reflect.Map:
		if rv.Kind() != reflect.Map || rv.Type().Key().Kind() != reflect.String {
			ffiFail("expected a Dict for %s, got %s", t, rv.Type())
		}
		out := reflect.MakeMapWithSize(t, rv.Len())
		it := rv.MapRange()
		for it.Next() {
			k := ffiToGo(ffiDecodeKey(it.Key().String(), t.Key()), t.Key())
			out.SetMapIndex(k, ffiToGo(ffiElem(it.Value()), t.Elem()))
		}
		return out
	case reflect.Struct:
		// struct{} — Sky's unit.
		return reflect.Zero(t)
	}
	ffiFail("cannot pass %s as %s", rv.Type(), t)
	return reflect.Value{}
}

// ffiElem unwraps an element read out of an `any`-typed container.
func ffiElem(v reflect.Value) any {
	if !v.IsValid() {
		return nil
	}
	if v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil
		}
		return v.Elem().Interface()
	}
	return v.Interface()
}

func ffiSkyInt(rv reflect.Value, t reflect.Type) int64 {
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		u := rv.Uint()
		if u > math.MaxInt64 {
			ffiFail("%d is out of range for %s (see docs/migration/v0.27.md#ffi-integer-range)", u, t)
		}
		return int64(u)
	}
	ffiFail("expected Int for %s, got %s", t, rv.Type())
	return 0
}

// ffiBytes: a Sky Bytes (a String, or a raw []byte) as a Go []byte or [N]byte
// (n >= 0 is the array length, checked exactly).
func ffiBytes(rv reflect.Value, t reflect.Type, n int) reflect.Value {
	var b []byte
	switch {
	case rv.Kind() == reflect.String:
		b = []byte(rv.String())
	case (rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array) && ffiIsByte(rv.Type().Elem()):
		b = make([]byte, rv.Len())
		reflect.Copy(reflect.ValueOf(b), rv)
	default:
		ffiFail("expected Bytes for %s, got %s", t, rv.Type())
	}
	if n < 0 {
		return reflect.ValueOf(b).Convert(t)
	}
	if len(b) != n {
		ffiFail("%s needs exactly %d bytes, got %d", t, n, len(b))
	}
	out := reflect.New(t).Elem()
	reflect.Copy(out, reflect.ValueOf(b))
	return out
}

// ffiDecodeKey reads a Sky Dict key back as the Go key kind k.
func ffiDecodeKey(s string, k reflect.Type) any {
	if dk, _, ok := decodeTaggedDictKey(s); ok {
		return dk
	}
	switch k.Kind() {
	case reflect.String:
		return s
	case reflect.Bool:
		b, err := strconv.ParseBool(s)
		if err != nil {
			ffiFail("Dict key %q is not a Bool", s)
		}
		return b
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			ffiFail("Dict key %q is not a Float", s)
		}
		return f
	default:
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			ffiFail("Dict key %q is not an Int", s)
		}
		return int(n)
	}
}

// ffiMaybeParts reads any instantiation of SkyMaybe: (tag, payload, ok).
func ffiMaybeParts(rv reflect.Value) (int, any, bool) {
	if rv.Kind() != reflect.Struct {
		return 0, nil, false
	}
	tag := rv.FieldByName("Tag")
	jv := rv.FieldByName("JustValue")
	if !tag.IsValid() || !jv.IsValid() || tag.Kind() != reflect.Int {
		return 0, nil, false
	}
	return int(tag.Int()), ffiElem(jv), true
}

// ffiResultParts reads any instantiation of SkyResult: (tag, ok, err, ok).
func ffiResultParts(rv reflect.Value) (int, any, any, bool) {
	if rv.Kind() != reflect.Struct {
		return 0, nil, nil, false
	}
	tag := rv.FieldByName("Tag")
	okv := rv.FieldByName("OkValue")
	errv := rv.FieldByName("ErrValue")
	if !tag.IsValid() || !okv.IsValid() || !errv.IsValid() || tag.Kind() != reflect.Int {
		return 0, nil, nil, false
	}
	return int(tag.Int()), ffiElem(okv), ffiElem(errv), true
}

// ffiOpaqueToGo passes an opaque Go value to a parameter of opaque type t:
// the same type, an interface it implements, or the `T` / `*T` of it.
func ffiOpaqueToGo(rv reflect.Value, t reflect.Type) reflect.Value {
	if rv.Type().AssignableTo(t) {
		out := reflect.New(t).Elem()
		out.Set(rv)
		return out
	}
	// `*T` given where `T` is wanted: dereference.
	if rv.Kind() == reflect.Ptr && !rv.IsNil() && rv.Elem().Type().AssignableTo(t) {
		out := reflect.New(t).Elem()
		out.Set(rv.Elem())
		return out
	}
	// `T` given where `*T` is wanted: a pointer to a copy, unless `*T` has
	// pointer-receiver methods, which would then act on the copy (a copied
	// mutex, a buffer write that is lost). That fails loudly instead.
	if t.Kind() == reflect.Ptr && rv.Type().AssignableTo(t.Elem()) {
		if t.NumMethod() > t.Elem().NumMethod() {
			ffiFail("%s is a value, and %s has pointer-receiver methods that would act on a copy of it", rv.Type(), t)
		}
		p := reflect.New(t.Elem())
		p.Elem().Set(rv)
		return p
	}
	// A Sky function given for a named Go func type (`http.HandlerFunc`).
	if t.Kind() == reflect.Func && rv.Kind() == reflect.Func {
		return ffiCallback(rv, t)
	}
	if t.Kind() == reflect.Interface {
		ffiFail("%s does not implement %s", rv.Type(), t)
	}
	ffiFail("expected %s, got %s", t, rv.Type())
	return reflect.Value{}
}

// ffiCallback adapts a Sky function to the Go func type t. Go's arguments are
// converted to Sky values, the Sky function runs, and its result is
// converted back to t's results:
//
//	no result        → the Sky result is ignored (it is `()`)
//	error            → Sky `Result Error ()`: Err is a Go error
//	(T, error)       → Sky `Result Error T`
//	(T, bool)        → Sky `Maybe T`
//	T                → Sky T
//	(A, B) / (A,B,C) → a Sky tuple
func ffiCallback(sky reflect.Value, t reflect.Type) reflect.Value {
	if sky.Type() == t {
		return sky
	}
	// A zero-parameter Go callback calls a Sky `() -> r` with the unit.
	unitArg := t.NumIn() == 0 && sky.Type().NumIn() == 1
	nIn := t.NumIn()
	if unitArg {
		nIn = 1
	}
	ins := make([]reflect.Type, nIn)
	for i := range ins {
		ins[i] = ffiAnyType
	}
	callable := adaptFuncValue(sky, reflect.FuncOf(ins, []reflect.Type{ffiAnyType}, false))
	return reflect.MakeFunc(t, func(in []reflect.Value) (outs []reflect.Value) {
		// Go may run the callback later, on its own goroutine (a server
		// handler, time.AfterFunc), outside every wrapper guard. A conversion
		// failure then comes back through the callback's `error` result when
		// it has one; otherwise it stays a panic whose message
		// `classifyPanic` names (FfiConversion), never a raw one.
		defer func() {
			r := recover()
			if r == nil {
				return
			}
			var ce *FfiConvError
			if e, ok := r.(error); ok && errors.As(e, &ce) && t.NumOut() > 0 && t.Out(t.NumOut()-1) == ffiErrorType {
				outs = make([]reflect.Value, t.NumOut())
				for i := range outs {
					outs[i] = reflect.Zero(t.Out(i))
				}
				outs[len(outs)-1] = reflect.ValueOf(errors.New(ce.Msg)).Convert(ffiErrorType)
				return
			}
			panic(r)
		}()
		if unitArg {
			u := reflect.New(ffiAnyType).Elem()
			u.Set(reflect.ValueOf(struct{}{}))
			in = nil
			out := callable.Call([]reflect.Value{u})
			var res any
			if len(out) > 0 {
				res = ffiElem(out[0])
			}
			return ffiCallbackResults(res, t)
		}
		args := make([]reflect.Value, len(in))
		for i, a := range in {
			av := reflect.New(ffiAnyType).Elem()
			if x := ffiToSky(a); x != nil {
				av.Set(reflect.ValueOf(x))
			}
			args[i] = av
		}
		out := callable.Call(args)
		var res any
		if len(out) > 0 {
			res = ffiElem(out[0])
		}
		return ffiCallbackResults(res, t)
	})
}

var ffiErrorType = reflect.TypeOf((*error)(nil)).Elem()

func ffiCallbackResults(res any, t reflect.Type) []reflect.Value {
	n := t.NumOut()
	outs := make([]reflect.Value, n)
	for i := range outs {
		outs[i] = reflect.Zero(t.Out(i))
	}
	switch {
	case n == 0:
		return outs
	case n == 1 && t.Out(0) == ffiErrorType:
		tag, _, e, ok := ffiResultParts(reflect.ValueOf(res))
		if !ok {
			ffiFail("a callback returning error must return a Result, got %s", ffiTypeName(res))
		}
		if tag != 0 {
			outs[0] = ffiGoError(e)
		}
		return outs
	case n == 2 && t.Out(1) == ffiErrorType:
		tag, okv, e, ok := ffiResultParts(reflect.ValueOf(res))
		if !ok {
			ffiFail("a callback returning (%s, error) must return a Result, got %s", t.Out(0), ffiTypeName(res))
		}
		if tag != 0 {
			outs[1] = ffiGoError(e)
			return outs
		}
		outs[0] = ffiToGo(okv, t.Out(0))
		return outs
	case n == 2 && t.Out(1).Kind() == reflect.Bool && !ffiDefined(t.Out(1)):
		tag, just, ok := ffiMaybeParts(reflect.ValueOf(res))
		if !ok {
			ffiFail("a callback returning (%s, bool) must return a Maybe, got %s", t.Out(0), ffiTypeName(res))
		}
		if tag == 0 {
			outs[0] = ffiToGo(just, t.Out(0))
			outs[1] = reflect.ValueOf(true).Convert(t.Out(1))
		}
		return outs
	case n == 1:
		outs[0] = ffiToGo(res, t.Out(0))
		return outs
	case n == 2 || n == 3:
		rv := reflect.ValueOf(res)
		for i := 0; i < n; i++ {
			f := rv.FieldByName("V" + strconv.Itoa(i))
			if rv.Kind() != reflect.Struct || !f.IsValid() {
				ffiFail("a callback returning %d values must return a tuple, got %s", n, ffiTypeName(res))
			}
			outs[i] = ffiToGo(ffiElem(f), t.Out(i))
		}
		return outs
	}
	ffiFail("cannot adapt a Sky function to %s", t)
	return outs
}

func ffiGoError(e any) reflect.Value {
	return reflect.ValueOf(errors.New(Basics_errorToStringT(e)))
}

// ffiToGoParam converts a Sky argument for a Go parameter of type t, reached
// by reflection (a wrapper that cannot spell t).
func ffiToGoParam(v any, t reflect.Type) reflect.Value {
	return ffiToGo(v, t)
}

// SkyFfiFieldGet3 reads a struct field of an opaque Go value and converts it
// to its Sky value. Format-3 counterpart of SkyFfiFieldGet.
func SkyFfiFieldGet3(recv any, field string) (out any) {
	defer SkyFfiGuard(&out)()
	v := reflect.ValueOf(recv)
	for v.IsValid() && (v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface) {
		if v.IsNil() {
			return Err[any, any](ErrFfi(field + ": nil receiver"))
		}
		v = v.Elem()
	}
	if !v.IsValid() {
		return Err[any, any](ErrFfi(field + ": nil receiver"))
	}
	if v.Kind() != reflect.Struct {
		return Err[any, any](ErrFfi(field + ": receiver is not a struct"))
	}
	f := v.FieldByName(field)
	if !f.IsValid() {
		return Err[any, any](ErrFfi(field + ": no such field"))
	}
	if f.Kind() == reflect.Struct && ffiOpaque(f.Type()) && f.CanAddr() {
		return Ok[any, any](f.Addr().Interface())
	}
	return Ok[any, any](ffiToSky(f))
}

// SkyFfiFieldSet3 writes a struct field of an opaque Go value, converting
// the Sky value to the field's Go type, and returns the receiver.
func SkyFfiFieldSet3(value any, recv any, field string) (out any) {
	defer SkyFfiGuard(&out)()
	if recv == nil {
		return Err[any, any](ErrFfi(field + ": nil receiver"))
	}
	rv := reflect.ValueOf(recv)
	var target reflect.Value
	switch rv.Kind() {
	case reflect.Ptr:
		if rv.IsNil() {
			return Err[any, any](ErrFfi(field + ": nil receiver"))
		}
		target = rv.Elem()
	case reflect.Struct:
		tmp := reflect.New(rv.Type())
		tmp.Elem().Set(rv)
		target = tmp.Elem()
		rv = tmp.Elem()
	default:
		return Err[any, any](ErrFfi(field + ": receiver is not a struct"))
	}
	if target.Kind() != reflect.Struct {
		return Err[any, any](ErrFfi(field + ": receiver is not a struct"))
	}
	f := target.FieldByName(field)
	if !f.IsValid() || !f.CanSet() {
		return Err[any, any](ErrFfi(field + ": no settable field"))
	}
	f.Set(ffiToGo(value, f.Type()))
	return Ok[any, any](rv.Interface())
}

// SkyFfiSetVar sets a package-level Go variable (`ptr` is its address) from a
// Sky value.
func SkyFfiSetVar(ptr any, value any) {
	p := reflect.ValueOf(ptr)
	if p.Kind() != reflect.Ptr || p.IsNil() {
		ffiFail("cannot set a package variable through %s", ffiTypeName(ptr))
	}
	p.Elem().Set(ffiToGo(value, p.Elem().Type()))
}

// SkyFfiPtrOf is the Sky value of a generic identity-pointer binding
// (`func Ptr[T any](v T) *T`): the pointer to a Sky value is `Just` it.
func SkyFfiPtrOf(v any) any {
	return Ok[any, any](Just[any](v))
}

// SkyFfiReflectCall3 calls a Go function reached by reflection (a wrapper
// that cannot spell its parameter types), converting each argument to its
// parameter type and each result to its Sky value, with the same result
// shapes a typed wrapper returns.
func SkyFfiReflectCall3(fn reflect.Value, hasError bool, args []any) (out any) {
	defer SkyFfiGuard(&out)()
	if !fn.IsValid() || fn.Kind() != reflect.Func {
		return Err[any, any](ErrFfi("not a function value"))
	}
	ft := fn.Type()
	n := ft.NumIn()
	variadic := ft.IsVariadic()
	if len(args) != n {
		// A unit argument stands for a zero-parameter call.
		if !(n == 0 && len(args) == 1) {
			return Err[any, any](ErrFfi(fmt.Sprintf("%d arguments for %s", len(args), ft)))
		}
		args = nil
	}
	vals := make([]reflect.Value, len(args))
	for i, a := range args {
		vals[i] = ffiToGoParam(a, ft.In(i))
	}
	var results []reflect.Value
	if variadic {
		results = fn.CallSlice(vals)
	} else {
		results = fn.Call(vals)
	}
	return ffiPackResults(results, hasError)
}

// ffiPackResults: the Sky Result of a Go call's results, in the shapes the
// surface renders (`gen::wrapper_sky_type`).
func ffiPackResults(results []reflect.Value, hasError bool) any {
	if hasError && len(results) > 0 {
		last := results[len(results)-1]
		if !last.IsNil() {
			err, _ := last.Interface().(error)
			if err != nil {
				return Err[any, any](ErrFfi(err.Error()))
			}
		}
		results = results[:len(results)-1]
	}
	switch len(results) {
	case 0:
		return Ok[any, any](struct{}{})
	case 1:
		return Ok[any, any](ffiToSky(results[0]))
	case 2:
		if !hasError && results[1].Kind() == reflect.Bool && !ffiDefined(results[1].Type()) &&
			results[0].Kind() != reflect.Bool {
			return Ok[any, any](FfiCommaOk(ffiToSky(results[0]), results[1].Bool()))
		}
		return Ok[any, any](SkyTuple2{V0: ffiToSky(results[0]), V1: ffiToSky(results[1])})
	case 3:
		return Ok[any, any](SkyTuple3{V0: ffiToSky(results[0]), V1: ffiToSky(results[1]), V2: ffiToSky(results[2])})
	}
	vs := make([]any, len(results))
	for i, r := range results {
		vs[i] = ffiToSky(r)
	}
	return Ok[any, any](vs)
}

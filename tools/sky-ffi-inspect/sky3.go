package main

// Surface format 3: the Sky type of every FFI slot, computed from the
// go/types structure rather than re-parsed from a type string.
//
// The rules MUST agree with the runtime converter (`runtime-go/rt/
// ffi_convert.go`), which converts values by the same classification, and
// with the Rust generator (`rust/crates/ffi/src/gen.rs`), which maps the
// opaque markers below to the checker's nominal keys:
//
//	Go integer of any width           Int (range-checked by the wrapper)
//	Go float                          Float
//	string / bool                     String / Bool
//	struct{}                          ()
//	[]byte, [N]byte                   Bytes
//	[]T, [N]T                         List T
//	*T, T not opaque                  Maybe T   (nil is Nothing)
//	*T, T opaque                      T         (the pointer is erased)
//	map[K]V, K String/Int/Float/Bool  Dict K V
//	a defined type over a basic       that basic
//	a defined non-basic type          Name@importPath       (opaque)
//	an interface a parameter RECEIVES iface:Name@importPath (checked at the call)
//	an empty interface it receives    any                   (accepts anything)
//	func in a parameter               (A -> B -> R) a callback
//	anything else                     @kind                 (opaque)
//
// Direction matters for interfaces and funcs: `dirIn` is a value Sky hands
// to Go (a parameter, a callback's result), `dirOut` one Go hands to Sky (a
// result, a callback's argument).

import (
	"go/types"
	"strings"
)

type sky3Dir int

const (
	dirIn sky3Dir = iota
	dirOut
)

// resultFor builds a Param for a value Go hands to Sky.
func resultFor(t types.Type) Param {
	p := paramFor(t)
	p.Sky3 = sky3Of(t, dirOut, false)
	return p
}

func resultForNamed(name string, t types.Type) Param {
	p := resultFor(t)
	p.Name = name
	return p
}

// sky3Opaque reports whether Sky holds values of t as an opaque Go value.
func sky3Opaque(t types.Type) bool {
	t = types.Unalias(t)
	if n, ok := t.(*types.Named); ok {
		_, basic := n.Underlying().(*types.Basic)
		return !basic
	}
	switch u := t.(type) {
	case *types.Interface, *types.Signature, *types.Chan:
		return true
	case *types.Struct:
		return u.NumFields() > 0
	case *types.Pointer:
		return sky3Opaque(u.Elem())
	case *types.Basic:
		k := u.Kind()
		return k == types.Complex64 || k == types.Complex128 || k == types.UnsafePointer ||
			k == types.UntypedComplex || k == types.UntypedNil
	case *types.TypeParam:
		return false
	}
	return false
}

func sky3Paren(s string) string {
	if strings.Contains(s, " ") && !strings.HasPrefix(s, "(") {
		return "(" + s + ")"
	}
	return s
}

func sky3NamedKey(n *types.Named) string {
	obj := n.Obj()
	name := obj.Name()
	if args := n.TypeArgs(); args != nil && args.Len() > 0 {
		var b strings.Builder
		b.WriteString(name)
		for i := 0; i < args.Len(); i++ {
			b.WriteString("__")
			for _, c := range types.TypeString(args.At(i), func(p *types.Package) string { return p.Name() }) {
				if c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
					b.WriteRune(c)
				} else {
					b.WriteRune('_')
				}
			}
		}
		name = b.String()
	}
	if obj.Pkg() == nil {
		if name == "error" {
			return "@error"
		}
		return "@" + name
	}
	return name + "@" + obj.Pkg().Path()
}

// sky3Of renders the format-3 Sky type of t in direction dir. `top` marks a
// parameter position, the only place a func is a callback.
func sky3Of(t types.Type, dir sky3Dir, top bool) string {
	t = types.Unalias(t)
	switch u := t.(type) {
	case *types.Named:
		if b, ok := u.Underlying().(*types.Basic); ok {
			return sky3Basic(b)
		}
		if iface, ok := u.Underlying().(*types.Interface); ok {
			key := sky3NamedKey(u)
			if dir == dirIn {
				if iface.NumMethods() == 0 {
					return "any"
				}
				return "iface:" + key
			}
			return key
		}
		if sig, ok := u.Underlying().(*types.Signature); ok && dir == dirIn && top {
			if cb := sky3Callback(sig); cb != "" {
				return cb
			}
		}
		return sky3NamedKey(u)
	case *types.Basic:
		return sky3Basic(u)
	case *types.Pointer:
		if sky3Opaque(u.Elem()) {
			return sky3Of(u.Elem(), dir, false)
		}
		return "Maybe " + sky3Paren(sky3Of(u.Elem(), dir, false))
	case *types.Slice:
		if sky3IsByte(u.Elem()) {
			return "Bytes"
		}
		return "List " + sky3Paren(sky3Of(u.Elem(), dir, false))
	case *types.Array:
		if sky3IsByte(u.Elem()) {
			return "Bytes"
		}
		return "List " + sky3Paren(sky3Of(u.Elem(), dir, false))
	case *types.Map:
		k := sky3DictKey(u.Key())
		if k == "" {
			return "@map"
		}
		return "Dict " + k + " " + sky3Paren(sky3Of(u.Elem(), dir, false))
	case *types.Interface:
		if dir == dirIn {
			if u.NumMethods() == 0 {
				return "any"
			}
			return "iface:@any"
		}
		return "@any"
	case *types.Signature:
		if dir == dirIn && top {
			if cb := sky3Callback(u); cb != "" {
				return cb
			}
		}
		return "@func"
	case *types.Struct:
		if u.NumFields() == 0 {
			return "()"
		}
		return "@struct"
	case *types.Chan:
		return "@chan"
	case *types.TypeParam:
		return "$" + u.Obj().Name()
	}
	return "@unknown"
}

func sky3IsByte(t types.Type) bool {
	b, ok := types.Unalias(t).(*types.Basic)
	return ok && b.Kind() == types.Uint8
}

func sky3Basic(b *types.Basic) string {
	switch b.Kind() {
	case types.Bool, types.UntypedBool:
		return "Bool"
	case types.Int, types.Int8, types.Int16, types.Int32, types.Int64,
		types.Uint, types.Uint8, types.Uint16, types.Uint32, types.Uint64, types.Uintptr,
		types.UntypedInt, types.UntypedRune:
		return "Int"
	case types.Float32, types.Float64, types.UntypedFloat:
		return "Float"
	case types.String, types.UntypedString:
		return "String"
	case types.Complex64, types.Complex128, types.UntypedComplex:
		return "@complex"
	case types.UnsafePointer:
		return "@unsafe"
	}
	return "@unknown"
}

func sky3DictKey(t types.Type) string {
	b, ok := types.Unalias(t).Underlying().(*types.Basic)
	if !ok {
		return ""
	}
	switch s := sky3Basic(b); s {
	case "Int", "Float", "String", "Bool":
		return s
	}
	return ""
}

// sky3Callback renders a Go func a parameter receives as the Sky function
// the runtime adapter (`rt.ffiCallback`) accepts. "" when there is none.
func sky3Callback(sig *types.Signature) string {
	if sig.Variadic() {
		return ""
	}
	var parts []string
	for i := 0; i < sig.Params().Len(); i++ {
		parts = append(parts, sky3Paren(sky3Of(sig.Params().At(i).Type(), dirOut, false)))
	}
	if len(parts) == 0 {
		parts = []string{"()"}
	}
	res := sky3CallbackResult(sig.Results())
	if res == "" {
		return ""
	}
	parts = append(parts, res)
	return "(" + strings.Join(parts, " -> ") + ")"
}

func sky3IsError(t types.Type) bool {
	n, ok := types.Unalias(t).(*types.Named)
	return ok && n.Obj().Pkg() == nil && n.Obj().Name() == "error"
}

func sky3IsPlainBool(t types.Type) bool {
	b, ok := types.Unalias(t).(*types.Basic)
	return ok && b.Kind() == types.Bool
}

// sky3CallbackResult: the Sky result of a callback, in the shapes
// `rt.ffiCallbackResults` converts.
func sky3CallbackResult(rs *types.Tuple) string {
	in := func(i int) string { return sky3Paren(sky3Of(rs.At(i).Type(), dirIn, false)) }
	switch n := rs.Len(); {
	case n == 0:
		return "()"
	case n == 1 && sky3IsError(rs.At(0).Type()):
		return "Result Error ()"
	case n == 1:
		return sky3Of(rs.At(0).Type(), dirIn, false)
	case n == 2 && sky3IsError(rs.At(1).Type()) && !sky3IsError(rs.At(0).Type()):
		return "Result Error " + in(0)
	case n == 2 && sky3IsPlainBool(rs.At(1).Type()) && !sky3IsError(rs.At(0).Type()):
		return "Maybe " + in(0)
	case (n == 2 || n == 3):
		var xs []string
		for i := 0; i < n; i++ {
			if sky3IsError(rs.At(i).Type()) {
				return ""
			}
			xs = append(xs, sky3Of(rs.At(i).Type(), dirIn, false))
		}
		return "(" + strings.Join(xs, ", ") + ")"
	}
	return ""
}

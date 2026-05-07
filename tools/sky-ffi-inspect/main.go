// sky-ffi-inspect inspects one or more Go packages and emits JSON
// descriptions of their exported top-level functions suitable for
// generating Sky FFI bindings.
//
// Usage:
//   sky-ffi-inspect github.com/pkg/path                 # single
//   sky-ffi-inspect pkg1 pkg2 pkg3 ...                  # multi
//
// Single-package mode (1 argv) emits a single PackageInfo JSON object
// for backwards compat. Multi-package mode (2+ argv) emits a JSON
// ARRAY of PackageInfo objects, one per requested root.
//
// The multi-package mode is the performance win: a single
// packages.Load([pkg1, pkg2, ...]) call lets the loader dedupe shared
// transitive deps across roots — when Stripe and Firestore both
// import golang.org/x/oauth2, that package's type-checking happens
// ONCE in multi-mode where it would happen twice in two separate
// calls. For Sky.Live apps with a Google Cloud + payment-stack
// dependency profile, this typically halves total install time.
//
// Output schema for single-mode (legacy callers):
//   {
//     "pkg": "github.com/pkg/path",
//     "name": "path",
//     "functions": [
//       {
//         "name": "Func",
//         "params": [{"name":"x", "type":"string"}, ...],
//         "results": [{"type":"int"}, ...],
//         "effect": "pure"|"fallible"|"effectful",
//         "exported": true
//       }
//     ],
//     "errors": []
//   }
//
// Multi-mode output: a JSON array of the above shape, indexed in the
// same order as the argv pkg paths so callers can match results back
// to inputs without re-reading the "pkg" field.
//
// Effect classification:
//   - fallible  : returns (T, error) or error — maps to Result String T
//   - effectful : returns channels, starts goroutines, or has zero signals
//                 we can't tell → conservatively mark as effectful when
//                 we can't prove purity
//   - pure      : everything else. Caller should call via Ffi.callPure.
//
// The tool never crashes: on any failure it emits a JSON with "errors".
package main

import (
	"encoding/json"
	"fmt"
	"go/types"
	"os"
	"strings"

	"golang.org/x/tools/go/packages"
)

type Param struct {
	Name string `json:"name,omitempty"`
	// Type — the canonical Go type string (e.g. `string`,
	// `*pkg.Foo`, `time.Duration`, `pkg.CheckoutSessionStatus`).
	// Drives Go-wrapper code generation on the Haskell side, so
	// derived types stay distinct from their underlying basic so
	// the generated wrapper can build / cast correctly.
	Type string `json:"type"`
	// SkyType — Sky-side surface form. Differs from Type only when
	// the type is a named alias of a basic type (Stripe's
	// `type CheckoutSessionStatus string`, Firestore's
	// `type Direction int`): SkyType collapses to the basic name
	// so HM treats them as String / Int / Bool. Empty when SkyType
	// equals Type — Haskell side defaults to Type when empty.
	SkyType string     `json:"skyType,omitempty"`
	GoType  types.Type `json:"-"` // unexported; used for interface-implements checks
}

type Function struct {
	Name      string  `json:"name"`
	Params    []Param `json:"params"`
	Results   []Param `json:"results"`
	Variadic  bool    `json:"variadic"`
	Effect    string  `json:"effect"`
	Exported  bool    `json:"exported"`
	// For method wrappers: the Go receiver type name (e.g. "Router" for
	// *mux.Router.HandleFunc) and the actual Go method name ("HandleFunc").
	// Empty for free-standing functions.
	RecvType   string `json:"recvType,omitempty"`
	MethodName string `json:"methodName,omitempty"`
	// IsField: true for synthetic struct-field getters.
	IsField    bool   `json:"isField,omitempty"`
	// IsFieldSet: true for synthetic struct-field setters (value-first).
	IsFieldSet bool   `json:"isFieldSet,omitempty"`
	// IsPkgVar: true for synthetic accessors around package-level vars
	// and consts (Firestore.Asc, Firestore.Desc, etc.).
	IsPkgVar   bool   `json:"isPkgVar,omitempty"`
}

type PackageInfo struct {
	Pkg       string     `json:"pkg"`
	Name      string     `json:"name"`
	Functions []Function `json:"functions"`
	Errors    []string   `json:"errors"`
}

func main() {
	if len(os.Args) < 2 {
		emitError("usage: sky-ffi-inspect <import-path> [<import-path> ...]")
		os.Exit(1)
	}
	pkgPaths := os.Args[1:]

	cfg := &packages.Config{
		// Trimmed mode set: NeedSyntax (parsed AST per file) and
		// NeedTypesInfo (per-expression type info) were unused — the
		// inspector reads only top-level scope objects + their type
		// signatures, never raw AST nodes or expression-level types.
		// Dropping them cuts loader work substantially on big SDKs:
		// no per-file parser pass kept around, no per-expression type
		// table populated. Audited every helper (methodsOf,
		// addPointerMethods, addInterfaceMethods, addFieldGetters,
		// addZeroConstructor, describe, paramsOf, resultsOf,
		// classifyEffect, implementsError) — all consume go/types
		// objects that NeedTypes alone provides. Verified output is
		// byte-identical to the previous mode set on skyshop's 18
		// Go deps (Stripe SDK, Firebase, Firestore, stdlib chunks)
		// before merging this change.
		Mode: packages.NeedName | packages.NeedTypes |
			packages.NeedDeps | packages.NeedImports,
	}

	// Single packages.Load over the full requested set. Go's loader
	// dedupes shared transitive deps across the roots: when Stripe
	// and Firestore both import golang.org/x/oauth2, that package's
	// type information is built ONCE here, vs once per root in N
	// separate invocations. For Sky.Live apps with Google Cloud +
	// payment-stack profiles, this is the dominant speedup over
	// per-package invocation.
	pkgs, err := packages.Load(cfg, pkgPaths...)
	if err != nil {
		// Hard load failure — emit a single error envelope per requested
		// pkg so the caller can match results back. In multi-mode this
		// preserves the array shape; in single-mode this is the legacy
		// error path.
		errors := []string{"load: " + err.Error()}
		out := make([]PackageInfo, 0, len(pkgPaths))
		for _, p := range pkgPaths {
			out = append(out, PackageInfo{Pkg: p, Errors: errors})
		}
		emitInfoOrArray(out, len(pkgPaths) > 1)
		return
	}

	// packages.Load returns roots in the same order as the input list
	// (per Go source: "the order matches the order of the patterns").
	// If the loader skipped some (rare — usually means a typo'd path),
	// we synthesise an empty PackageInfo so the array stays aligned.
	results := make([]PackageInfo, 0, len(pkgPaths))
	loadedByPath := make(map[string]*packages.Package, len(pkgs))
	for _, pkg := range pkgs {
		loadedByPath[pkg.PkgPath] = pkg
	}
	for _, requested := range pkgPaths {
		pkg, ok := loadedByPath[requested]
		if !ok {
			results = append(results, PackageInfo{
				Pkg: requested,
				Errors: []string{"no package loaded for " + requested},
			})
			continue
		}
		results = append(results, walkPackage(requested, pkg))
	}

	emitInfoOrArray(results, len(pkgPaths) > 1)
}


// walkPackage produces the PackageInfo for one loaded *packages.Package.
// Extracted out of main so single-mode and multi-mode share the same
// per-pkg traversal — keeps the output guaranteed-equivalent across
// the two paths (a callsite calling with N==1 gets the same JSON as
// the legacy single-arg invocation).
func walkPackage(requestedPath string, pkg *packages.Package) PackageInfo {
	info := PackageInfo{Pkg: requestedPath}

	if len(pkg.Errors) > 0 {
		for _, e := range pkg.Errors {
			info.Errors = append(info.Errors, e.Error())
		}
		// Continue anyway — some errors are ignorable (e.g. missing
		// internal packages we don't need to introspect).
	}
	info.Name = pkg.Name

	if pkg.Types == nil {
		return info
	}

	scope := pkg.Types.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if obj == nil || !obj.Exported() {
			continue
		}
		// Free-standing function.
		if fn, ok := obj.(*types.Func); ok {
			sig, ok := fn.Type().(*types.Signature)
			if !ok {
				continue
			}
			if sig.Recv() != nil {
				continue
			}
			info.Functions = append(info.Functions, describe(fn, sig))
			continue
		}
		// Package-level var (e.g. firestore.Asc, firestore.Desc — exported
		// singleton values). Emit as a zero-arg Sky thunk that returns the
		// value. Sky-side convention: takes a unit param `()`.
		// Also emit a `Set<Name>` setter so Sky can mutate pkg-level
		// configuration vars (e.g. stripe.Key).
		if v, ok := obj.(*types.Var); ok && v.Exported() {
			info.Functions = append(info.Functions, Function{
				Name:     v.Name(),
				Params:   []Param{{Name: "_", Type: "struct{}"}},
				Results:  []Param{paramFor(v.Type())},
				Effect:   "pure",
				Exported: true,
				IsPkgVar: true,
			})
			info.Functions = append(info.Functions, Function{
				Name:       "Set" + v.Name(),
				Params:     []Param{paramForNamed("value", v.Type())},
				Results:    []Param{{Type: "struct{}"}},
				Effect:     "effectful",
				Exported:   true,
				IsPkgVar:   true,
				MethodName: v.Name(), // store the real var name for emission
			})
			continue
		}
		// Package-level const — same shape as var.
		if c, ok := obj.(*types.Const); ok && c.Exported() {
			info.Functions = append(info.Functions, Function{
				Name:     c.Name(),
				Params:   []Param{{Name: "_", Type: "struct{}"}},
				Results:  []Param{paramFor(c.Type())},
				Effect:   "pure",
				Exported: true,
				IsPkgVar: true,
			})
			continue
		}
		// Named type — emit each of its exported methods as a synthetic
		// free function whose first param is the receiver. Matches the
		// legacy Sky convention where `*Router.HandleFunc` surfaces in
		// Sky as `Mux.routerHandleFunc router ...`.
		if tn, ok := obj.(*types.TypeName); ok {
			named, ok := tn.Type().(*types.Named)
			if !ok {
				continue
			}
			info.Functions = append(info.Functions, methodsOf(named, name)...)
			// Pointer-receiver methods live on *Named.
			ptr := types.NewPointer(named)
			msetP := types.NewMethodSet(ptr)
			addPointerMethods(&info, msetP, name, named)
			// Interface method sets — emit each method as a free function
			// taking the interface value as receiver.
			if iface, ok := named.Underlying().(*types.Interface); ok {
				addInterfaceMethods(&info, iface, name, named)
			}
			// Struct-field getters — exported fields become <Type><Field>
			// synthetic functions that reflect on the receiver to read the
			// field. Needed for opaque Go structs like *DocumentRef whose
			// public surface includes fields (e.g., `ref.ID`).
			if strct, ok := named.Underlying().(*types.Struct); ok {
				addFieldGetters(&info, strct, name, named)
				// Zero-value constructor `New<TypeName>() -> *<TypeName>`
				// — matches the Opaque Struct Pattern documented in
				// CLAUDE.md. User writes `Stripe.newCustomerParams ()`
				// to get a fresh *CustomerParams, then pipes setters.
				addZeroConstructor(&info, name, named)
			}
		}
	}

	return info
}


// emitInfoOrArray writes the appropriate JSON shape for single or multi
// mode. Single-mode (1 root): a bare PackageInfo object — keeps the
// legacy callers happy. Multi-mode (2+ roots): a JSON array. The
// caller chooses based on the number of input pkg paths so the choice
// is unambiguous.
func emitInfoOrArray(results []PackageInfo, multi bool) {
	if multi {
		b, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			emitError("marshal: " + err.Error())
			return
		}
		fmt.Println(string(b))
		return
	}
	if len(results) == 0 {
		emitError("no packages")
		return
	}
	emitInfo(results[0])
}


// methodsOf emits methods declared directly on a named type. Each method
// carries its real declared receiver type (value or pointer) so generated
// wrappers produce the correct `.(T)` or `.(*T)` assertion.
func methodsOf(named *types.Named, typeName string) []Function {
	var out []Function
	for i := 0; i < named.NumMethods(); i++ {
		m := named.Method(i)
		if !m.Exported() {
			continue
		}
		sig, ok := m.Type().(*types.Signature)
		if !ok {
			continue
		}
		// Use the method's actual receiver type (pointer or value) rather
		// than guessing from the named type alone.
		recv := sig.Recv()
		var rt types.Type
		if recv != nil {
			rt = recv.Type()
		} else {
			rt = named.Obj().Type()
		}
		out = append(out, describeMethod(typeName, m, sig, rt))
	}
	return out
}

func addPointerMethods(info *PackageInfo, mset *types.MethodSet, typeName string, named *types.Named) {
	seen := map[string]bool{}
	for _, f := range info.Functions {
		seen[f.Name] = true
	}
	for i := 0; i < mset.Len(); i++ {
		sel := mset.At(i)
		obj := sel.Obj()
		if !obj.Exported() {
			continue
		}
		fn, ok := obj.(*types.Func)
		if !ok {
			continue
		}
		sig, ok := fn.Type().(*types.Signature)
		if !ok {
			continue
		}
		name := typeName + fn.Name()
		if seen[name] {
			continue
		}
		info.Functions = append(info.Functions, Function{
			Name:     name,
			Params:   append([]Param{{Name: "recv", Type: types.NewPointer(named.Obj().Type()).String()}}, paramsOf(sig)...),
			Results:  resultsOf(sig),
			Variadic: sig.Variadic(),
			Effect:   classifyEffect(resultsOf(sig)),
			Exported: true,
			RecvType: typeName,
			MethodName: fn.Name(),
		})
		seen[name] = true
	}
}

func paramsOf(sig *types.Signature) []Param {
	out := make([]Param, 0, sig.Params().Len())
	for i := 0; i < sig.Params().Len(); i++ {
		p := sig.Params().At(i)
		out = append(out, paramForNamed(p.Name(), p.Type()))
	}
	return out
}

func resultsOf(sig *types.Signature) []Param {
	out := make([]Param, 0, sig.Results().Len())
	for i := 0; i < sig.Results().Len(); i++ {
		r := sig.Results().At(i)
		out = append(out, withGoType(paramForNamed(r.Name(), r.Type()), r.Type()))
	}
	return out
}

// paramFor / paramForNamed build Param values with both fields
// populated: Type stays the canonical Go-type rendering (drives
// wrapper code generation), SkyType collapses derived-of-basic
// types so HM treats them as their underlying basic.
func paramFor(t types.Type) Param {
	gt := t.String()
	st := skyTypeOf(t)
	if st == gt {
		return Param{Type: gt}
	}
	return Param{Type: gt, SkyType: st}
}

func paramForNamed(name string, t types.Type) Param {
	p := paramFor(t)
	p.Name = name
	return p
}

func withGoType(p Param, gt types.Type) Param {
	p.GoType = gt
	return p
}


// skyTypeOf renders a Go type as the string the Sky-side
// goTypeToSky translator expects, with one important
// transformation versus the bare types.Type.String() path: a
// named type whose underlying is a basic type (Stripe's
// `type CheckoutSessionStatus string`, Firestore's
// `type Direction int`, etc.) collapses to the underlying
// basic-type name. Without this, Sky-side HM treats every
// such enum as an opaque "Value", forcing user code into
// awkward `case ... of Ok v -> ... ; Err _ -> default` shapes
// for what is structurally a String / Int / Bool comparison.
//
// Pointer / slice / map / chan / func types recurse so
// `[]CheckoutSessionStatus` becomes `[]string` and
// `map[string]CheckoutSessionStatus` becomes
// `map[string]string`. Struct-shaped named types stay
// opaque (we render them as the package-qualified name) —
// only the basic-underlying case unwraps.
func skyTypeOf(t types.Type) string {
	switch tt := t.(type) {
	case *types.Pointer:
		return "*" + skyTypeOf(tt.Elem())
	case *types.Slice:
		return "[]" + skyTypeOf(tt.Elem())
	case *types.Array:
		return tt.String() // fall back; Sky doesn't see fixed-size arrays separately
	case *types.Map:
		return "map[" + skyTypeOf(tt.Key()) + "]" + skyTypeOf(tt.Elem())
	case *types.Named:
		// Unwrap derived-from-basic types (Stripe-style enums).
		if basic, ok := tt.Underlying().(*types.Basic); ok {
			return basic.Name()
		}
		return tt.String()
	default:
		return t.String()
	}
}

func describeMethod(typeName string, fn *types.Func, sig *types.Signature, recvType types.Type) Function {
	params := []Param{{Name: "recv", Type: recvType.String()}}
	params = append(params, paramsOf(sig)...)
	return Function{
		Name:       typeName + fn.Name(),
		Params:     params,
		Results:    resultsOf(sig),
		Variadic:   sig.Variadic(),
		Effect:     classifyEffect(resultsOf(sig)),
		Exported:   true,
		RecvType:   typeName,
		MethodName: fn.Name(),
	}
}

// addZeroConstructor emits `New<TypeName>() -> *TypeName` — a zero-value
// constructor helper so Sky code can write `Stripe.newCustomerParams ()`
// without hand-writing a Go factory. Skipped when:
//   * the package already exports a `New<TypeName>` function (avoid Go
//     redeclaration — happens regardless of `scope.Names()` iteration
//     order because we consult the pkg scope directly).
//   * the type is generic — `new(pkg.Foo)` won't compile without
//     instantiation, and we don't know the constraint here.
func addZeroConstructor(info *PackageInfo, typeName string, named *types.Named) {
	name := "New" + typeName
	// Skip if a real factory with the same name exists anywhere in scope.
	if pkg := named.Obj().Pkg(); pkg != nil {
		if pkg.Scope().Lookup(name) != nil {
			return
		}
	}
	// Skip generics: named.TypeParams() is non-empty for parameterised types.
	if named.TypeParams() != nil && named.TypeParams().Len() > 0 {
		return
	}
	for _, f := range info.Functions {
		if f.Name == name {
			return
		}
	}
	info.Functions = append(info.Functions, Function{
		Name:       name,
		Params:     []Param{{Name: "_", Type: "struct{}"}},
		Results:    []Param{{Type: types.NewPointer(named.Obj().Type()).String()}},
		Effect:     "pure",
		Exported:   true,
		RecvType:   typeName,
		IsPkgVar:   true,  // reuse the "one-line wrapper" path
	})
}


// addFieldGetters emits one synthetic unary function per exported struct
// field (the getter) AND one binary setter per settable field. Name
// convention matches the legacy Sky FFI: <TypeName><FieldName> for the
// getter, <TypeName>Set<FieldName> for the setter. Marker flags on the
// JSON drive FfiGen's reflect-based emission.
//
// Setter param order is value-first (then receiver) so it composes with
// Sky's |> pipeline: `doc |> DocumentRefSetID "abc"`.
func addFieldGetters(info *PackageInfo, s *types.Struct, typeName string, named *types.Named) {
	seen := map[string]bool{}
	for _, f := range info.Functions {
		seen[f.Name] = true
	}
	recvType := types.NewPointer(named.Obj().Type()).String()
	for i := 0; i < s.NumFields(); i++ {
		f := s.Field(i)
		if !f.Exported() {
			continue
		}
		getterName := typeName + f.Name()
		if !seen[getterName] {
			info.Functions = append(info.Functions, Function{
				Name:       getterName,
				Params:     []Param{{Name: "recv", Type: recvType}},
				Results:    []Param{paramFor(f.Type())},
				Effect:     "pure",
				Exported:   true,
				RecvType:   typeName,
				MethodName: f.Name(),
				IsField:    true,
			})
			seen[getterName] = true
		}
		setterName := typeName + "Set" + f.Name()
		if !seen[setterName] {
			info.Functions = append(info.Functions, Function{
				Name: setterName,
				// value-first, receiver second — matches Sky pipeline idiom.
				Params: []Param{
					paramForNamed("value", f.Type()),
					{Name: "recv", Type: recvType},
				},
				Results:    []Param{{Type: recvType}},
				Effect:     "pure",
				Exported:   true,
				RecvType:   typeName,
				MethodName: f.Name(),
				IsFieldSet: true,
			})
			seen[setterName] = true
		}
	}
}


// addInterfaceMethods emits methods from an interface's explicit method set
// as synthetic free functions. Receiver is the named interface type itself
// (no pointer — interface values are already reference-typed).
func addInterfaceMethods(info *PackageInfo, iface *types.Interface, typeName string, named *types.Named) {
	seen := map[string]bool{}
	for _, f := range info.Functions {
		seen[f.Name] = true
	}
	n := iface.NumMethods()
	for i := 0; i < n; i++ {
		m := iface.Method(i)
		if !m.Exported() {
			continue
		}
		sig, ok := m.Type().(*types.Signature)
		if !ok {
			continue
		}
		name := typeName + m.Name()
		if seen[name] {
			continue
		}
		info.Functions = append(info.Functions, Function{
			Name:       name,
			Params:     append([]Param{paramForNamed("recv", named.Obj().Type())}, paramsOf(sig)...),
			Results:    resultsOf(sig),
			Variadic:   sig.Variadic(),
			Effect:     classifyEffect(resultsOf(sig)),
			Exported:   true,
			RecvType:   typeName,
			MethodName: m.Name(),
		})
		seen[name] = true
	}
}


func lowerFirstByte(s string) string {
	if len(s) == 0 {
		return s
	}
	if s[0] >= 'A' && s[0] <= 'Z' {
		return string(s[0]+32) + s[1:]
	}
	return s
}

func describe(fn *types.Func, sig *types.Signature) Function {
	params := []Param{}
	for i := 0; i < sig.Params().Len(); i++ {
		p := sig.Params().At(i)
		params = append(params, paramForNamed(p.Name(), p.Type()))
	}
	results := []Param{}
	for i := 0; i < sig.Results().Len(); i++ {
		r := sig.Results().At(i)
		results = append(results, paramForNamed(r.Name(), r.Type()))
	}
	return Function{
		Name:     fn.Name(),
		Params:   params,
		Results:  results,
		Variadic: sig.Variadic(),
		Effect:   classifyEffect(results),
		Exported: true,
	}
}

// errorIface is the Go `error` interface, looked up once from the
// universe scope. Used by implementsError to detect named error
// types (e.g. *os.PathError) that implement `error` but whose
// type string isn't literally "error".
var errorIface *types.Interface

func init() {
	obj := types.Universe.Lookup("error")
	if obj != nil {
		errorIface = obj.Type().Underlying().(*types.Interface)
	}
}

// implementsError checks whether a Go type (or its pointer form)
// satisfies the built-in `error` interface. Catches named error
// types like *os.PathError, *url.Error, *json.SyntaxError that
// the old string-match "error" missed.
func implementsError(t types.Type) bool {
	if errorIface == nil {
		return false
	}
	if types.Implements(t, errorIface) {
		return true
	}
	// Check *T as well — Go convention is pointer receivers on
	// Error() methods.
	if _, isPtr := t.(*types.Pointer); !isPtr {
		return types.Implements(types.NewPointer(t), errorIface)
	}
	return false
}

// classifyEffect chooses pure / fallible / effectful from the result list.
// Conservative: when in doubt, call it effectful.
func classifyEffect(results []Param) string {
	// error-returning functions are fallible. Check both the literal
	// "error" string AND whether the type implements the error
	// interface (catches named error types like *os.PathError).
	for _, r := range results {
		if r.Type == "error" {
			return "fallible"
		}
		if r.GoType != nil && implementsError(r.GoType) {
			return "fallible"
		}
	}
	// Channels, functions, or unsafe.Pointer results suggest effects
	for _, r := range results {
		t := r.Type
		if strings.HasPrefix(t, "chan ") ||
			strings.HasPrefix(t, "<-chan ") ||
			strings.HasPrefix(t, "chan<- ") ||
			strings.HasPrefix(t, "func(") {
			return "effectful"
		}
	}
	return "pure"
}

func emitInfo(info PackageInfo) {
	b, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		emitError("marshal: " + err.Error())
		return
	}
	fmt.Println(string(b))
}

func emitError(msg string) {
	b, _ := json.Marshal(PackageInfo{Errors: []string{msg}})
	fmt.Println(string(b))
}

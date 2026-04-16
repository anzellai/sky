// sky-ffi-inspect inspects a Go package and emits a JSON description of its
// exported top-level functions suitable for generating Sky FFI bindings.
//
// Usage:
//   sky-ffi-inspect github.com/pkg/path
//
// Output (to stdout) is JSON of the form:
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
	Name   string     `json:"name,omitempty"`
	Type   string     `json:"type"`
	GoType types.Type `json:"-"` // unexported; used for interface-implements checks
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
		emitError("usage: sky-ffi-inspect <import-path>")
		os.Exit(1)
	}
	pkgPath := os.Args[1]

	info := PackageInfo{Pkg: pkgPath}

	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes |
			packages.NeedTypesInfo | packages.NeedSyntax |
			packages.NeedDeps | packages.NeedImports,
	}
	pkgs, err := packages.Load(cfg, pkgPath)
	if err != nil {
		info.Errors = append(info.Errors, "load: "+err.Error())
		emitInfo(info)
		return
	}
	if len(pkgs) == 0 {
		info.Errors = append(info.Errors, "no packages loaded")
		emitInfo(info)
		return
	}
	pkg := pkgs[0]
	if len(pkg.Errors) > 0 {
		for _, e := range pkg.Errors {
			info.Errors = append(info.Errors, e.Error())
		}
		// continue anyway — sometimes there are ignorable errors
	}
	info.Name = pkg.Name

	if pkg.Types == nil {
		emitInfo(info)
		return
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
				Results:  []Param{{Type: v.Type().String()}},
				Effect:   "pure",
				Exported: true,
				IsPkgVar: true,
			})
			info.Functions = append(info.Functions, Function{
				Name:       "Set" + v.Name(),
				Params:     []Param{{Name: "value", Type: v.Type().String()}},
				Results:    []Param{{Type: "struct{}"}},
				Effect:     "effectful",
				Exported:   true,
				IsPkgVar:   true,
				MethodName: v.Name(),  // store the real var name for emission
			})
			continue
		}
		// Package-level const — same shape as var.
		if c, ok := obj.(*types.Const); ok && c.Exported() {
			info.Functions = append(info.Functions, Function{
				Name:     c.Name(),
				Params:   []Param{{Name: "_", Type: "struct{}"}},
				Results:  []Param{{Type: c.Type().String()}},
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

	emitInfo(info)
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
		out = append(out, Param{Name: p.Name(), Type: p.Type().String()})
	}
	return out
}

func resultsOf(sig *types.Signature) []Param {
	out := make([]Param, 0, sig.Results().Len())
	for i := 0; i < sig.Results().Len(); i++ {
		r := sig.Results().At(i)
		out = append(out, Param{Name: r.Name(), Type: r.Type().String(), GoType: r.Type()})
	}
	return out
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
				Results:    []Param{{Type: f.Type().String()}},
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
					{Name: "value", Type: f.Type().String()},
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
			Params:     append([]Param{{Name: "recv", Type: named.Obj().Type().String()}}, paramsOf(sig)...),
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
		params = append(params, Param{Name: p.Name(), Type: p.Type().String()})
	}
	results := []Param{}
	for i := 0; i < sig.Results().Len(); i++ {
		r := sig.Results().At(i)
		results = append(results, Param{Name: r.Name(), Type: r.Type().String()})
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

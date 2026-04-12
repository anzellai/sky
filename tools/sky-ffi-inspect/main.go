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
	Name string `json:"name,omitempty"`
	Type string `json:"type"`
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
		out = append(out, Param{Name: r.Name(), Type: r.Type().String()})
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

// classifyEffect chooses pure / fallible / effectful from the result list.
// Conservative: when in doubt, call it effectful.
func classifyEffect(results []Param) string {
	// error-returning functions are fallible (and typically effectful too,
	// but the Result wrapping is the important thing)
	for _, r := range results {
		if r.Type == "error" {
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

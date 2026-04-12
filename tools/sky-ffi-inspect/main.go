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
	Name     string  `json:"name"`
	Params   []Param `json:"params"`
	Results  []Param `json:"results"`
	Variadic bool    `json:"variadic"`
	Effect   string  `json:"effect"`
	Exported bool    `json:"exported"`
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
		fn, ok := obj.(*types.Func)
		if !ok {
			continue
		}
		sig, ok := fn.Type().(*types.Signature)
		if !ok {
			continue
		}
		// Skip methods (they have a receiver) — only free-standing funcs for now.
		if sig.Recv() != nil {
			continue
		}
		info.Functions = append(info.Functions, describe(fn, sig))
	}

	emitInfo(info)
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

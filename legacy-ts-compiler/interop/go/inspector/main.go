package main

import (
	"encoding/json"
	"fmt"
	"go/types"
	"os"

	"golang.org/x/tools/go/packages"
)

type Output struct {
	Name   string      `json:"name"`
	Path   string      `json:"path"`
	Types  []TypeDecl  `json:"types"`
	Funcs  []FuncDecl  `json:"funcs"`
	Vars   []VarDecl   `json:"vars"`
	Consts []ConstDecl `json:"consts"`
}

type TypeDecl struct {
	Name    string       `json:"name"`
	Kind    string       `json:"kind"` // "struct", "interface", "alias", "basic"
	Methods []MethodDecl `json:"methods"`
	Fields  []FieldDecl  `json:"fields"`
}

type FieldDecl struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type MethodDecl struct {
	Name    string  `json:"name"`
	Params  []Param `json:"params"`
	Results []Param `json:"results"`
}

type FuncDecl struct {
	Name    string  `json:"name"`
	Params  []Param `json:"params"`
	Results []Param `json:"results"`
}

type VarDecl struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type ConstDecl struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

type Param struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: inspector <pkg>")
		os.Exit(1)
	}
	pkgPath := os.Args[1]

	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedImports | packages.NeedDeps | packages.NeedTypesInfo,
	}
	pkgs, err := packages.Load(cfg, pkgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if packages.PrintErrors(pkgs) > 0 {
		os.Exit(1)
	}

	pkg := pkgs[0]
	if pkg.Types == nil {
	    fmt.Fprintln(os.Stderr, "Package types not found")
	    os.Exit(1)
	}

	scope := pkg.Types.Scope()

	out := Output{
		Name: pkg.Name,
		Path: pkg.PkgPath,
	}

	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if !obj.Exported() {
			continue
		}

		switch obj := obj.(type) {
		case *types.TypeName:
			typ := obj.Type()
			decl := TypeDecl{Name: name}
			
			if named, ok := typ.(*types.Named); ok {
				underlying := named.Underlying()
				switch u := underlying.(type) {
				case *types.Struct:
					decl.Kind = "struct"
					for i := 0; i < u.NumFields(); i++ {
						f := u.Field(i)
						if f.Exported() {
							decl.Fields = append(decl.Fields, FieldDecl{
								Name: f.Name(),
								Type: typeToString(f.Type()),
							})
						}
					}
				case *types.Interface:
					decl.Kind = "interface"
					for i := 0; i < u.NumExplicitMethods(); i++ {
						m := u.ExplicitMethod(i)
						if m.Exported() {
							sig := m.Type().(*types.Signature)
							meth := MethodDecl{Name: m.Name()}
							meth.Params = extractParams(sig.Params())
							meth.Results = extractParams(sig.Results())
							decl.Methods = append(decl.Methods, meth)
						}
					}
				default:
					decl.Kind = "other"
				}

				for i := 0; i < named.NumMethods(); i++ {
					m := named.Method(i)
					if m.Exported() {
						sig := m.Type().(*types.Signature)
						meth := MethodDecl{Name: m.Name()}
						meth.Params = extractParams(sig.Params())
						meth.Results = extractParams(sig.Results())
						decl.Methods = append(decl.Methods, meth)
					}
				}
			}
			out.Types = append(out.Types, decl)

		case *types.Func:
			sig := obj.Type().(*types.Signature)
			if sig.Recv() == nil {
				f := FuncDecl{Name: name}
				f.Params = extractParams(sig.Params())
				f.Results = extractParams(sig.Results())
				out.Funcs = append(out.Funcs, f)
			}

		case *types.Var:
			out.Vars = append(out.Vars, VarDecl{Name: name, Type: typeToString(obj.Type())})

		case *types.Const:
			out.Consts = append(out.Consts, ConstDecl{Name: name, Type: typeToString(obj.Type()), Value: obj.Val().String()})
		}
	}

	json.NewEncoder(os.Stdout).Encode(out)
}

func typeToString(t types.Type) string {
    fmt.Fprintf(os.Stderr, "DEBUG typeToString: %T %s\n", t, t.String())
	switch u := t.(type) {
	case *types.Pointer:
		inner := typeToString(u.Elem())
		if inner == "interface{}" {
			return "interface{}"
		}
		return "*" + inner
	case *types.Slice:
		inner := typeToString(u.Elem())
		if inner == "interface{}" {
			return "interface{}"
		}
		return "[]" + inner
	case *types.Array:
		inner := typeToString(u.Elem())
		if inner == "interface{}" {
			return "interface{}"
		}
		return fmt.Sprintf("[%d]%s", u.Len(), inner)
	case *types.Named:
		if u.Obj().Pkg() == nil {
			return u.Obj().Name()
		}
		if !u.Obj().Exported() {
			return "interface{}"
		}
	case *types.Basic:
		return u.String()
	}

	return t.String()
}

func extractParams(tuple *types.Tuple) []Param {
	if tuple == nil {
		return nil
	}
	var res []Param
	for i := 0; i < tuple.Len(); i++ {
		v := tuple.At(i)
		res = append(res, Param{Name: v.Name(), Type: typeToString(v.Type())})
	}
	return res
}

package main

import (
	"encoding/json"
	"fmt"
	"go/importer"
	"go/token"
	"go/types"
	"os"
	_ "strings"
	"golang.org/x/tools/go/packages"
)

type Output struct {
	Name   string     `json:"name"`
	Path   string     `json:"path"`
	Types  []TypeDecl `json:"types"`
	Funcs  []FuncDecl `json:"funcs"`
	Vars   []VarDecl  `json:"vars"`
	Consts []ConstDecl `json:"consts"`
}

type TypeDecl struct {
	Name    string      `json:"name"`
	Kind    string      `json:"kind"`
	Fields  []FieldDecl `json:"fields,omitempty"`
	Methods []MethodDecl `json:"methods,omitempty"`
}

type FieldDecl struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type MethodDecl struct {
	Name          string      `json:"name"`
	Params        []ParamDecl `json:"params"`
	Results       []ParamDecl `json:"results"`
	Variadic      bool        `json:"variadic,omitempty"`
	HasTypeParams bool        `json:"hasTypeParams,omitempty"`
}

type FuncDecl struct {
	Name          string      `json:"name"`
	Params        []ParamDecl `json:"params"`
	Results       []ParamDecl `json:"results"`
	Variadic      bool        `json:"variadic,omitempty"`
	HasTypeParams bool        `json:"hasTypeParams,omitempty"`
}

type ParamDecl struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type VarDecl struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type ConstDecl struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value,omitempty"`
}

func typeStr(t types.Type) string {
	switch u := t.(type) {
	case *types.Named:
		obj := u.Obj()
		pkg := obj.Pkg()
		if pkg != nil {
			return pkg.Path() + "." + obj.Name()
		}
		return obj.Name()
	case *types.Pointer:
		return "*" + typeStr(u.Elem())
	case *types.Slice:
		return "[]" + typeStr(u.Elem())
	case *types.Map:
		return "map[" + typeStr(u.Key()) + "]" + typeStr(u.Elem())
	case *types.Interface:
		if u.Empty() { return "interface{}" }
		return "interface{}"
	default:
		return t.String()
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: inspector <package>")
		os.Exit(1)
	}
	minimal := len(os.Args) > 2 && os.Args[1] == "--minimal"
	pkgPath := os.Args[len(os.Args)-1]

	var pkg *types.Package
	if minimal {
		// Use go/importer for large packages (avoids OOM from go/packages NeedSyntax)
		fset := token.NewFileSet()
		imp := importer.ForCompiler(fset, "source", nil)
		var err error
		pkg, err = imp.Import(pkgPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "import error: %v\n", err)
			os.Exit(1)
		}
	} else {
		cfg := &packages.Config{Mode: packages.NeedTypes | packages.NeedName | packages.NeedImports | packages.NeedDeps | packages.NeedSyntax}
		pkgs, err := packages.Load(cfg, pkgPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load error: %v\n", err)
			os.Exit(1)
		}
		if len(pkgs) == 0 || pkgs[0].Types == nil {
			fmt.Fprintln(os.Stderr, "no types found")
			os.Exit(1)
		}
		pkg = pkgs[0].Types
	}

	scope := pkg.Scope()
	out := Output{Name: pkg.Name(), Path: pkg.Path()}

	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if !obj.Exported() { continue }
		switch o := obj.(type) {
		case *types.TypeName:
			named, ok := o.Type().(*types.Named)
			if !ok { continue }
			// Skip generic types — can't be used from any-typed Sky code
			if named.TypeParams() != nil && named.TypeParams().Len() > 0 { continue }
			td := TypeDecl{Name: name}
			switch u := named.Underlying().(type) {
			case *types.Struct:
				td.Kind = "struct"
				for i := 0; i < u.NumFields(); i++ {
					f := u.Field(i)
					if f.Exported() {
						td.Fields = append(td.Fields, FieldDecl{Name: f.Name(), Type: typeStr(f.Type())})
					}
				}
			case *types.Interface:
				td.Kind = "interface"
				for i := 0; i < u.NumMethods(); i++ {
					m := u.Method(i)
					if !m.Exported() { continue }
					sig := m.Type().(*types.Signature)
					md := MethodDecl{Name: m.Name(), Variadic: sig.Variadic(), HasTypeParams: sig.TypeParams() != nil && sig.TypeParams().Len() > 0}
					for j := 0; j < sig.Params().Len(); j++ {
						p := sig.Params().At(j)
						md.Params = append(md.Params, ParamDecl{Name: p.Name(), Type: typeStr(p.Type())})
					}
					for j := 0; j < sig.Results().Len(); j++ {
						r := sig.Results().At(j)
						md.Results = append(md.Results, ParamDecl{Name: r.Name(), Type: typeStr(r.Type())})
					}
					td.Methods = append(td.Methods, md)
				}
			default:
				td.Kind = "other"
			}
			mset := types.NewMethodSet(types.NewPointer(named))
			for i := 0; i < mset.Len(); i++ {
				m := mset.At(i)
				fn, ok := m.Obj().(*types.Func)
				if !ok || !fn.Exported() { continue }
				sig := fn.Type().(*types.Signature)
				hasTP := sig.TypeParams() != nil && sig.TypeParams().Len() > 0
				if sig.RecvTypeParams() != nil && sig.RecvTypeParams().Len() > 0 { hasTP = true }
				md := MethodDecl{Name: fn.Name(), Variadic: sig.Variadic(), HasTypeParams: hasTP}
				for j := 0; j < sig.Params().Len(); j++ {
					p := sig.Params().At(j)
					md.Params = append(md.Params, ParamDecl{Name: p.Name(), Type: typeStr(p.Type())})
				}
				for j := 0; j < sig.Results().Len(); j++ {
					r := sig.Results().At(j)
					md.Results = append(md.Results, ParamDecl{Name: r.Name(), Type: typeStr(r.Type())})
				}
				td.Methods = append(td.Methods, md)
			}
			out.Types = append(out.Types, td)
		case *types.Func:
			sig := o.Type().(*types.Signature)
			fd := FuncDecl{Name: name, Variadic: sig.Variadic(), HasTypeParams: sig.TypeParams() != nil && sig.TypeParams().Len() > 0}
			for i := 0; i < sig.Params().Len(); i++ {
				p := sig.Params().At(i)
				fd.Params = append(fd.Params, ParamDecl{Name: p.Name(), Type: typeStr(p.Type())})
			}
			for i := 0; i < sig.Results().Len(); i++ {
				r := sig.Results().At(i)
				fd.Results = append(fd.Results, ParamDecl{Name: r.Name(), Type: typeStr(r.Type())})
			}
			out.Funcs = append(out.Funcs, fd)
		case *types.Var:
			out.Vars = append(out.Vars, VarDecl{Name: name, Type: typeStr(o.Type())})
		case *types.Const:
			out.Consts = append(out.Consts, ConstDecl{Name: name, Type: typeStr(o.Type()), Value: o.Val().String()})
		}
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintf(os.Stderr, "encode error: %v\n", err)
		os.Exit(1)
	}
}
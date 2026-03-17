// src/interop/go/inspect-package.ts

import { execSync } from "child_process";
import fs from "fs";
import path from "path";
import { getDirname } from "../../utils/path.js";

const __dirname = getDirname(import.meta.url);

export interface InspectResult {
    name: string;
    path: string;
    types: {
        name: string;
        kind: string;
        methods: MethodDecl[];
        fields: FieldDecl[];
    }[];
    funcs: FuncDecl[];
    vars: VarDecl[];
    consts: ConstDecl[];
}

export interface MethodDecl {
    name: string;
    params: Param[];
    results: Param[];
    variadic?: boolean;
}

export interface FuncDecl {
    name: string;
    params: Param[];
    results: Param[];
    variadic?: boolean;
}

export interface FieldDecl {
    name: string;
    type: string;
}

export interface VarDecl {
    name: string;
    type: string;
}

export interface ConstDecl {
    name: string;
    type: string;
    value: string;
}

export interface Param {
    name: string;
    type: string;
    variadic?: boolean;
}

export function inspectPackage(pkgName: string): InspectResult {
    
    // Since pkg packages files in a virtual filesystem but go build needs real files,
    // it's easier to just build the go tool once globally or generate the go script in the user project
    // and run it.
    const projectDir = process.cwd();
    const inspectorDir = path.join(projectDir, ".skycache", "inspector");
    fs.mkdirSync(inspectorDir, { recursive: true });

    const inspectorGoCode = `package main

import (
	"encoding/json"
	"fmt"
	"go/types"
	"os"

	"golang.org/x/tools/go/packages"
)

type Output struct {
	Name   string      \`json:"name"\`
	Path   string      \`json:"path"\`
	Types  []TypeDecl  \`json:"types"\`
	Funcs  []FuncDecl  \`json:"funcs"\`
	Vars   []VarDecl   \`json:"vars"\`
	Consts []ConstDecl \`json:"consts"\`
}

type TypeDecl struct {
	Name    string       \`json:"name"\`
	Kind    string       \`json:"kind"\`
	Methods []MethodDecl \`json:"methods"\`
	Fields  []FieldDecl  \`json:"fields"\`
}

type FieldDecl struct {
	Name string \`json:"name"\`
	Type string \`json:"type"\`
}

type MethodDecl struct {
	Name     string  \`json:"name"\`
	Params   []Param \`json:"params"\`
	Results  []Param \`json:"results"\`
	Variadic bool    \`json:"variadic"\`
}

type FuncDecl struct {
	Name     string  \`json:"name"\`
	Params   []Param \`json:"params"\`
	Results  []Param \`json:"results"\`
	Variadic bool    \`json:"variadic"\`
}

type VarDecl struct {
	Name string \`json:"name"\`
	Type string \`json:"type"\`
}

type ConstDecl struct {
	Name  string \`json:"name"\`
	Type  string \`json:"type"\`
	Value string \`json:"value"\`
}

type Param struct {
	Name     string \`json:"name"\`
	Type     string \`json:"type"\`
	Variadic bool   \`json:"variadic"\`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: inspector <pkg>")
		os.Exit(1)
	}
	pkgPath := os.Args[1]
	os.Setenv("GO111MODULE", "on")

	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedImports | packages.NeedDeps | packages.NeedTypesInfo,
		Dir: os.Getenv("SKY_PROJECT_DIR"),
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
							meth := MethodDecl{Name: m.Name(), Variadic: sig.Variadic()}
							meth.Params = extractParams(sig.Params(), sig.Variadic())
							meth.Results = extractParams(sig.Results(), false)
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
						meth := MethodDecl{Name: m.Name(), Variadic: sig.Variadic()}
						meth.Params = extractParams(sig.Params(), sig.Variadic())
						meth.Results = extractParams(sig.Results(), false)
						decl.Methods = append(decl.Methods, meth)
					}
				}
			}
			out.Types = append(out.Types, decl)

		case *types.Func:
			sig := obj.Type().(*types.Signature)
			if sig.Recv() == nil {
				f := FuncDecl{Name: name, Variadic: sig.Variadic()}
				f.Params = extractParams(sig.Params(), sig.Variadic())
				f.Results = extractParams(sig.Results(), false)
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

func extractParams(tuple *types.Tuple, variadic bool) []Param {
	if tuple == nil {
		return nil
	}
	var res []Param
	for i := 0; i < tuple.Len(); i++ {
		v := tuple.At(i)
		isVariadic := variadic && i == tuple.Len()-1
		res = append(res, Param{Name: v.Name(), Type: typeToString(v.Type()), Variadic: isVariadic})
	}
	return res
}
`;

    // Always overwrite main.go to ensure latest version
    fs.writeFileSync(path.join(inspectorDir, "main.go"), inspectorGoCode);

    if (!fs.existsSync(path.join(inspectorDir, "go.mod"))) {
        execSync("go mod init sky-inspector", { cwd: inspectorDir, stdio: "ignore" });
        execSync("go get golang.org/x/tools/go/packages", { cwd: inspectorDir, stdio: "ignore" });
    }

    
    // Check if we need to build the inspector binary first
    const inspectorBin = process.platform === "win32" ? "sky-inspector.exe" : "sky-inspector";
    const binPath = path.join(inspectorDir, inspectorBin);

    // Build the inspector
    execSync(`go build -o ${inspectorBin} main.go`, { cwd: inspectorDir, stdio: "inherit" });

    const out = execSync(`"${binPath}" ${pkgName}`, { 
        cwd: inspectorDir, 
        maxBuffer: 1024 * 1024 * 10,
        env: { ...process.env, SKY_PROJECT_DIR: path.join(projectDir, ".skycache", "gomod") }
    }).toString();
    return JSON.parse(out);
}

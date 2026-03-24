// src/interop/go/inspect-package.ts

import { execSync } from "child_process";
import fs from "fs";
import path from "path";
import { getDirname } from "../../utils/path.js";

const __dirname = getDirname(import.meta.url);

// In-memory cache: avoids re-inspecting the same package within a single process
const inspectCache = new Map<string, InspectResult>();

export function clearInspectCache() {
    inspectCache.clear();
}

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
    hasTypeParams?: boolean;
}

export interface FuncDecl {
    name: string;
    params: Param[];
    results: Param[];
    variadic?: boolean;
    hasTypeParams?: boolean;
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
    // Return cached result if already inspected this process
    const cached = inspectCache.get(pkgName);
    if (cached) return cached;

    const projectDir = process.cwd();

    // Check disk cache: .skycache/go/{pkgPath}/inspect.json
    // Invalidated by go.sum changes (dependency version updates)
    const safePkgDir = path.join(projectDir, ".skycache", "go", pkgName);
    const diskCachePath = path.join(safePkgDir, "inspect.json");
    const goSumPath = path.join(projectDir, ".skycache", "gomod", "go.sum");
    if (fs.existsSync(diskCachePath)) {
        const cacheValid = !fs.existsSync(goSumPath) ||
            fs.statSync(diskCachePath).mtimeMs > fs.statSync(goSumPath).mtimeMs;
        if (cacheValid) {
            try {
                const diskResult: InspectResult = JSON.parse(fs.readFileSync(diskCachePath, "utf8"));
                inspectCache.set(pkgName, diskResult);
                return diskResult;
            } catch (_) { /* corrupted cache, re-inspect */ }
        }
    }
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
	Name          string  \`json:"name"\`
	Params        []Param \`json:"params"\`
	Results       []Param \`json:"results"\`
	Variadic      bool    \`json:"variadic"\`
	HasTypeParams bool    \`json:"hasTypeParams,omitempty"\`
}

type FuncDecl struct {
	Name          string  \`json:"name"\`
	Params        []Param \`json:"params"\`
	Results       []Param \`json:"results"\`
	Variadic      bool    \`json:"variadic"\`
	HasTypeParams bool    \`json:"hasTypeParams,omitempty"\`
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

				// Use NewMethodSet on pointer-to-type to get all methods,
				// including promoted methods from embedded structs.
				mset := types.NewMethodSet(types.NewPointer(named))
				seen := map[string]bool{}
				for i := 0; i < mset.Len(); i++ {
					sel := mset.At(i)
					fn, ok := sel.Obj().(*types.Func)
					if !ok || !fn.Exported() {
						continue
					}
					if seen[fn.Name()] {
						continue
					}
					seen[fn.Name()] = true
					sig := fn.Type().(*types.Signature)
					meth := MethodDecl{Name: fn.Name(), Variadic: sig.Variadic()}
					meth.Params = extractParams(sig.Params(), sig.Variadic())
					meth.Results = extractParams(sig.Results(), false)
					if sig.TypeParams() != nil && sig.TypeParams().Len() > 0 {
						meth.HasTypeParams = true
					}
					// Also check if the receiver type itself has type params
					if sig.RecvTypeParams() != nil && sig.RecvTypeParams().Len() > 0 {
						meth.HasTypeParams = true
					}
					decl.Methods = append(decl.Methods, meth)
				}
			}
			out.Types = append(out.Types, decl)

		case *types.Func:
			sig := obj.Type().(*types.Signature)
			if sig.Recv() == nil {
				f := FuncDecl{Name: name, Variadic: sig.Variadic()}
				f.Params = extractParams(sig.Params(), sig.Variadic())
				f.Results = extractParams(sig.Results(), false)
				if sig.TypeParams() != nil && sig.TypeParams().Len() > 0 {
					f.HasTypeParams = true
				}
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
	case *types.TypeParam:
		// Go generic type parameter (e.g., T ~string) — resolve to constraint's underlying type.
		// This allows Sky to generate correct type mappings for generic functions.
		constraint := u.Constraint()
		if iface, ok := constraint.Underlying().(*types.Interface); ok {
			if iface.NumEmbeddeds() > 0 {
				embedded := iface.EmbeddedType(0)
				// Handle ~T (approximation) constraints
				if union, ok := embedded.(*types.Union); ok && union.Len() > 0 {
					term := union.Term(0)
					return typeToString(term.Type())
				}
				return typeToString(embedded)
			}
		}
		return "interface{}"
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

    // Only overwrite main.go if content changed (avoids triggering go build)
    const mainGoPath = path.join(inspectorDir, "main.go");
    const existing = fs.existsSync(mainGoPath) ? fs.readFileSync(mainGoPath, "utf8") : "";
    if (existing !== inspectorGoCode) {
        fs.writeFileSync(mainGoPath, inspectorGoCode);
    }

    if (!fs.existsSync(path.join(inspectorDir, "go.mod"))) {
        execSync("go mod init sky-inspector", { cwd: inspectorDir, stdio: "ignore" });
        execSync("go get golang.org/x/tools/go/packages", { cwd: inspectorDir, stdio: "ignore" });
    }

    const inspectorBin = process.platform === "win32" ? "sky-inspector.exe" : "sky-inspector";
    const binPath = path.join(inspectorDir, inspectorBin);

    // Only rebuild inspector binary if main.go is newer or binary doesn't exist
    const needsRebuild = !fs.existsSync(binPath) ||
        fs.statSync(mainGoPath).mtimeMs > fs.statSync(binPath).mtimeMs;

    if (needsRebuild) {
        execSync(`go build -o ${inspectorBin} main.go`, { cwd: inspectorDir, stdio: "inherit" });
    }

    const out = execSync(`"${binPath}" ${pkgName}`, {
        cwd: inspectorDir,
        maxBuffer: 1024 * 1024 * 10,
        env: { ...process.env, SKY_PROJECT_DIR: fs.existsSync(path.join(projectDir, ".skycache", "gomod", "go.mod")) ? path.join(projectDir, ".skycache", "gomod") : projectDir }
    }).toString();
    const result: InspectResult = JSON.parse(out);
    inspectCache.set(pkgName, result);

    // Write to disk cache for subsequent builds
    try {
        fs.mkdirSync(safePkgDir, { recursive: true });
        fs.writeFileSync(diskCachePath, out);
    } catch (_) { /* non-fatal: disk cache is optional */ }

    return result;
}

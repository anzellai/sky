import fs from "fs";
import path from "path";
import { InspectResult, Param } from "./inspect-package.js";
import { lowerCamelCase } from "./type-mapper.js";

function makeSafeGoName(pkgName: string) {
    return pkgName.replace(/[\/\.-]/g, "_");
}

export function generateWrappers(pkgName: string, pkg: InspectResult, usedSymbols?: Set<string>) {
    const safePkg = makeSafeGoName(pkgName);
    
    const wrapperDir = path.join(".skycache", "go", "wrappers");
    fs.mkdirSync(wrapperDir, { recursive: true });
    const helperPath = path.join(wrapperDir, "00_sky_helpers.go");
    
      let helperCode = `package sky_wrappers

type SkyResult struct {
	Tag int
	OkValue any
	ErrValue any
}

func SkyOk(v any) SkyResult {
	return SkyResult{Tag: 0, OkValue: v}
}

func SkyErr(e any) SkyResult {
	return SkyResult{Tag: 1, ErrValue: e}
}

type Tuple2 struct {
    V0 any
    V1 any
}

type Tuple3 struct {
    V0 any
    V1 any
    V2 any
}

var CmdNone any = struct{ Tag int }{Tag: 0}
var SubNone any = struct{ Tag int }{Tag: 0}

func UpdateRecord(base any, update map[string]any) any {
    // Very naive record update for map-based records
    m, ok := base.(map[string]any)
    if !ok {
        return base
    }
    newMap := make(map[string]any)
    for k, v := range m {
        newMap[k] = v
    }
    for k, v := range update {
        newMap[k] = v
    }
    return newMap
}
`;
      fs.writeFileSync(helperPath, helperCode);


    const imports = new Set<string>();

    const extractImports = (t: string) => {
        // ... previous implementation ...
        const matches = [...t.matchAll(/([a-zA-Z0-9_\/\.-]+)\.[a-zA-Z0-9_]+/g)];
        for (const m of matches) {
            const p = m[1];
            if (p.includes("/")) {
                imports.add(p);
            } else if (p !== pkg.name) {
                if (["io", "fmt", "time", "os", "context", "net", "http", "bufio", "log", "hash", "crypto", "syscall"].includes(p)) {
                    imports.add(p);
                }
            }
        }
    };

    // Always import the package we are wrapping
    imports.add(pkgName);

    const cleanType = (t: string) => {
        extractImports(t);
        const res = t.replace(/([a-zA-Z0-9_\/\.-]+)\.([a-zA-Z0-9_]+)/g, (match, p1, p2) => {
            const parts = p1.split("/");
            const pkgBase = parts[parts.length - 1];
            // If it's interface{}, it might be a sanitized unexported type
            if (p2 === "interface{}") return "any";
            return pkgBase + "." + p2;
        });
        if (res.includes("interface{}")) return res.replace(/interface\{\}/g, "any");
        return res;
    };

    const pkgBase = pkg.name;
    let goCode = "";

    const generateFuncWrapper = (skyName: string, goName: string, params: Param[], results: Param[], isMethod = false, isField = false, recvType = "", variadic = false) => {
        const skyNamePascal = skyName.charAt(0).toUpperCase() + skyName.slice(1);
        let wrapperName = `Sky_${safePkg}_${skyNamePascal}`;
        
        /* Disable tree-shaking for now
        if (usedSymbols && !usedSymbols.has(wrapperName)) {
            return; // Skip unused wrapper
        }
        */
        
        imports.add(pkgName);

        let goParams = params.map((p, i) => {
            return `arg${i} any`;
        }).join(", ");
        
        let casts = params.map((p, i) => {
            let t = cleanType(p.type);
            // Replace net/http with just http if imported that way
            if (t.includes("net/http.ResponseWriter")) {
                t = t.replace(/net\/http\./g, "http.");
            }
            if (variadic && i === params.length - 1) {
                return `\tvar _arg${i} []${t.substring(2)}\n\tfor _, v := range arg${i}.([]any) {\n\t\t_arg${i} = append(_arg${i}, v.(${t.substring(2)}))\n\t}`;
            }
            if (t === "func(http.ResponseWriter, *http.Request)") {
                return `\t_arg${i} := func(w http.ResponseWriter, r *http.Request) {\n\t\t_f0 := arg${i}.(func(any) any)\n\t\t_f1 := _f0(w).(func(any) any)\n\t\t_f1(r)\n\t}`;
            }
            if (t === "func(net/http.ResponseWriter, *net/http.Request)") {
                return `\t_arg${i} := func(w http.ResponseWriter, r *http.Request) {\n\t\t_f0 := arg${i}.(func(any) any)\n\t\t_f1 := _f0(w).(func(any) any)\n\t\t_f1(r)\n\t}`;
            }
            return `\t_arg${i} := arg${i}.(${t})`;
        }).join("\n");
        
        if (recvType && (isMethod || isField)) {
            const recvArg = `this any`;
            goParams = goParams ? `${recvArg}, ${goParams}` : recvArg;
            casts = `\t_this := this.(${cleanType(recvType)})\n` + casts;
        }

        let goReturns = " ";
        let retTypes = results.map(r => cleanType(r.type));
        
        // ONLY wrap in SkyResult if it's a FUNCTION call that returns an error
        // Variables and fields should be returned as-is
        const shouldWrap = !isField && !isMethod && (
            (retTypes.length === 1 && retTypes[0] === "error") ||
            (retTypes.length === 2 && retTypes[1] === "error")
        );

        if (shouldWrap) {
            goReturns = ` SkyResult `;
        } else if (retTypes.length > 0) {
            if (retTypes.length === 1) {
                goReturns = ` ${retTypes[0]} `;
            } else {
                goReturns = ` (${retTypes.join(", ")}) `;
            }
        }

        goCode += `func ${wrapperName}(${goParams})${goReturns}{\n`;
        if (casts.trim()) {
            goCode += `${casts}\n`;
        }
        
        const callArgs = params.map((p, i) => {
            if (p.variadic || (variadic && i === params.length - 1)) return `_arg${i}...`;
            return `_arg${i}`;
        }).join(", ");
        
        if (isField) {
            if (recvType) {
                goCode += `\treturn _this.${goName}\n`;
            } else {
                goCode += `\treturn ${pkgBase}.${goName}\n`;
            }
        } else {
            let callExpr = `${pkgBase}.${goName}(${callArgs})`;
            if (recvType) {
                callExpr = `_this.${goName}(${callArgs})`;
            }

            if (retTypes.length === 0) {
                goCode += `\t${callExpr}\n`;
            } else if (shouldWrap) {
                if (retTypes.length === 1) {
                    goCode += `\terr := ${callExpr}\n\tif err != nil {\n\t\treturn SkyErr(err)\n\t}\n\treturn SkyOk(struct{}{})\n`;
                } else {
                    goCode += `\tres, err := ${callExpr}\n\tif err != nil {\n\t\treturn SkyErr(err)\n\t}\n\treturn SkyOk(res)\n`;
                }
            } else {
                goCode += `\treturn ${callExpr}\n`;
            }
        }
        
        goCode += `}\n\n`;
    }

    for (const f of pkg.funcs || []) {
        generateFuncWrapper(lowerCamelCase(f.name), f.name, f.params || [], f.results || [], false, false, "", f.variadic);
    }

    for (const v of pkg.vars || []) {
        // Variables might be functions or simple values
        generateFuncWrapper(lowerCamelCase(v.name), v.name, [], [{name: "", type: v.type}], false, true);
    }

    for (const t of pkg.types || []) {
        if (t.methods) {
            for (const m of t.methods) {
                // If it's an interface, the receiver shouldn't be a pointer!
                const isInterface = t.kind === "interface";
                const recv = isInterface ? `${pkg.name}.${t.name}` : `*${pkg.name}.${t.name}`;
                generateFuncWrapper(lowerCamelCase(t.name + m.name), m.name, m.params || [], m.results || [], true, false, recv, m.variadic);
            }
        }
        if (t.fields) {
            for (const f of t.fields) {
                const isInterface = t.kind === "interface";
                const recv = isInterface ? `${pkg.name}.${t.name}` : `*${pkg.name}.${t.name}`;
                generateFuncWrapper(lowerCamelCase(t.name + f.name), f.name, [], [{name: "", type: f.type}], false, true, recv);
            }
        }
    }

    const wrapperPath = path.join(wrapperDir, `${safePkg}.go`);
    if (fs.existsSync(wrapperPath)) {
        fs.unlinkSync(wrapperPath);
    }

    if (goCode.trim() === "") {
        return; // No wrappers needed
    }

    const importsStr = Array.from(imports).map(i => `\t"${i}"`).join("\n");
    const finalCode = `package sky_wrappers\n\nimport (\n${importsStr}\n)\n\n` + goCode;
    fs.writeFileSync(wrapperPath, finalCode);
}

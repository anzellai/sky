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
    if (!fs.existsSync(helperPath)) {
      let helperCode = `package sky_wrappers\ntype SkyResult[E any, A any] struct {\n\tTag int\n\tOkValue A\n\tErrValue E\n}\nfunc SkyOk[E any, A any](v A) SkyResult[E, A] {\n\treturn SkyResult[E, A]{Tag: 0, OkValue: v}\n}\nfunc SkyErr[E any, A any](e E) SkyResult[E, A] {\n\treturn SkyResult[E, A]{Tag: 1, ErrValue: e}\n}\n`;
      fs.writeFileSync(helperPath, helperCode);
    }

    const imports = new Set<string>();

    const extractImports = (t: string) => {
        const matches = [...t.matchAll(/([a-zA-Z0-9_\/\.-]+)\.[a-zA-Z0-9_]+/g)];
        for (const m of matches) {
            imports.add(m[1]);
        }
    };

    const cleanType = (t: string) => {
        extractImports(t);
        return t.replace(/([a-zA-Z0-9_\/\.-]+)\.([a-zA-Z0-9_]+)/g, (match, p1, p2) => {
            const pkgBase = p1.split("/").pop();
            return pkgBase + "." + p2;
        });
    };

    let goCode = "";

    const generateFuncWrapper = (skyName: string, goName: string, params: Param[], results: Param[], isMethod = false, isField = false, recvType = "") => {
        const wrapperName = `Sky_${safePkg}_${skyName}`;
        
        if (usedSymbols && !usedSymbols.has(wrapperName)) {
            return; // Skip unused wrapper
        }
        
        imports.add(pkgName);

        let goParams = params.map((p, i) => `arg${i} ${cleanType(p.type)}`).join(", ");
        if (isMethod || isField) {
            goParams = `this ${cleanType(recvType)}` + (params.length > 0 ? ", " + params.map((p, i) => `arg${i} ${cleanType(p.type)}`).join(", ") : "");
        }

        let goReturns = " ";
        let retTypes = results.map(r => cleanType(r.type));
        if (retTypes.length === 1) {
            if (retTypes[0] === "error") {
                goReturns = ` SkyResult[error, struct{}] `;
            } else {
                goReturns = ` ${retTypes[0]} `;
            }
        } else if (retTypes.length === 2 && retTypes[1] === "error") {
            goReturns = ` SkyResult[error, ${retTypes[0]}] `;
        } else if (retTypes.length > 0) {
            goReturns = ` (${retTypes.join(", ")}) `;
        }

        goCode += `func ${wrapperName}(${goParams})${goReturns}{\n`;
        
        const callArgs = params.map((_, i) => `arg${i}`).join(", ");
        
        if (isField) {
            goCode += `\treturn this.${goName}\n`;
        } else {
            let callExpr = `${pkg.name}.${goName}(${callArgs})`;
            if (isMethod) {
                callExpr = `this.${goName}(${callArgs})`;
            }

            if (retTypes.length === 0) {
                goCode += `\t${callExpr}\n`;
            } else if (retTypes.length === 1) {
                if (retTypes[0] === "error") {
                    goCode += `\terr := ${callExpr}\n\tif err != nil {\n\t\treturn SkyErr[error, struct{}](err)\n\t}\n\treturn SkyOk[error, struct{}](struct{}{})\n`;
                } else {
                    goCode += `\treturn ${callExpr}\n`;
                }
            } else if (retTypes.length === 2 && retTypes[1] === "error") {
                goCode += `\tres, err := ${callExpr}\n\tif err != nil {\n\t\treturn SkyErr[error, ${retTypes[0]}](err)\n\t}\n\treturn SkyOk[error, ${retTypes[0]}](res)\n`;
            } else {
                goCode += `\treturn ${callExpr}\n`;
            }
        }
        
        goCode += `}\n\n`;
    };

    for (const f of pkg.funcs || []) {
        generateFuncWrapper(lowerCamelCase(f.name), f.name, f.params || [], f.results || []);
    }

    for (const t of pkg.types || []) {
        if (t.methods) {
            for (const m of t.methods) {
                // If it's an interface, the receiver shouldn't be a pointer!
                const isInterface = t.kind === "interface";
                const recv = isInterface ? `${pkg.name}.${t.name}` : `*${pkg.name}.${t.name}`;
                generateFuncWrapper(lowerCamelCase(t.name + m.name), m.name, m.params || [], m.results || [], true, false, recv);
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

    if (imports.has("http")) { imports.delete("http"); imports.add("net/http"); }
    let importsStr = Array.from(imports).map(i => `\t"${i}"`).join("\n");
    let finalCode = `package sky_wrappers\n\nimport (\n${importsStr}\n)\n\n` + goCode;
    fs.writeFileSync(path.join(wrapperDir, `${safePkg}.go`), finalCode);
}

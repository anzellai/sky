// src/interop/go/generate-bindings.ts

import { inspectPackage, Param } from "./inspect-package.js";
import { mapGoTypeToSky, lowerCamelCase, isGoPointerToPrimitive } from "./type-mapper.js";
import { generateWrappers } from "./generate-wrappers.js";

export interface GeneratedForeignBindings {
  packageName: string;
  skyModuleName: string;
  runtimeEntryPath: string;
  values: { skyName: string; jsName: string; sourceModule: string; skyType: string; }[];
  types: { skyName: string; jsName: string; sourceModule: string; typeParams: string[]; }[];
}

export async function generateForeignBindings(packageName: string, requestedNames: string[], options?: { skipWrappers?: boolean }): Promise<{ generated?: GeneratedForeignBindings, diagnostics: string[], skyiContent?: string }> {
  try {
    const pkg = inspectPackage(packageName);

    // Convert Go package path to Sky module name.
    // Handles dashes: "kanda-co" -> "KandaCo", "ks-schema" -> "KsSchema"
    const moduleName = packageName.split(/[\/\.]/).map(p =>
      p.split("-").map(s => s.charAt(0).toUpperCase() + s.slice(1)).join("")
    ).join(".");

    // We will emit the skyi Content here as well.
    let skyiContent = `module ${moduleName} exposing (..)\n\n`;
    // Skip wrapper generation when the compiler will handle it separately with tree-shaking
    if (!options?.skipWrappers) {
        generateWrappers(packageName, pkg);
    }

    // Always emit base utility types
    skyiContent += `type Error = Error\n\ntype Any = Any\n\ntype List a = List\n\ntype Map k v = Map\n\ntype Bytes = Bytes\n\n`;

    const values: GeneratedForeignBindings['values'] = [];

    // 2. Constants
    for (const c of pkg.consts || []) {
        const skyName = lowerCamelCase(c.name);
        const t = mapGoTypeToSky(c.type, packageName);
        skyiContent += `foreign import "${packageName}" exposing (${c.name})\n\n`;
        skyiContent += `${skyName} : ${t}\n`;
        skyiContent += `${skyName} = ${c.name}\n\n`;
        values.push({ skyName, jsName: c.name, sourceModule: packageName, skyType: "Foreign" });
    }

    const safePkg = packageName.replace(/[\/\.-]/g, "_");

    // 3. Variables — expose as zero-arg getter and setter functions via wrappers
    for (const v of pkg.vars || []) {
        const skyName = lowerCamelCase(v.name);
        const t = mapGoTypeToSky(v.type, packageName);
        const skyNamePascal = skyName.charAt(0).toUpperCase() + skyName.slice(1);

        // Getter: varName : () -> T
        const wrapperName = `Sky_${safePkg}_${skyNamePascal}`;
        skyiContent += `foreign import "sky_wrappers" exposing (${wrapperName})\n\n`;
        skyiContent += `${skyName} : () -> ${t}\n`;
        skyiContent += `${skyName} arg0 = ${wrapperName} arg0\n\n`;
        values.push({ skyName, jsName: v.name, sourceModule: packageName, skyType: "Foreign" });

        // Setter: setVarName : T -> ()
        // Skip setter for:
        // - variables with unexported Go types (can't assign from external code)
        // - variables whose inspector type is interface{} (unexported concrete type)
        const rawGoType = v.type.replace(/^\*+/, "").replace(/^\[\]/, "");
        const hasUnexportedGoType = v.type.includes("interface{}") ||
            /\.\s*[a-z]/.test(v.type) ||
            (/^[a-z]/.test(rawGoType) && !["string", "int", "int8", "int16", "int32", "int64",
            "uint", "uint8", "uint16", "uint32", "uint64", "float32", "float64",
            "bool", "byte", "rune", "error", "any", "interface{}"].includes(rawGoType));
        if (!hasUnexportedGoType) {
            const setterSkyName = `set${skyNamePascal}`;
            const setterWrapperName = `Sky_${safePkg}_Set${skyNamePascal}`;
            skyiContent += `foreign import "sky_wrappers" exposing (${setterWrapperName})\n\n`;
            skyiContent += `${setterSkyName} : ${t} -> ()\n`;
            skyiContent += `${setterSkyName} arg0 = ${setterWrapperName} arg0\n\n`;
            values.push({ skyName: setterSkyName, jsName: `Set${v.name}`, sourceModule: packageName, skyType: "Foreign" });
        }
    }

    const cleanType = (t: string) => {
        const res = t.replace(/([a-zA-Z0-9_\/\.-]+)\.([a-zA-Z0-9_]+)/g, (match, p1, p2) => {
            const parts = p1.split("/");
            const pkgBase = parts[parts.length - 1];
            if (p2 === "interface{}") return "any";
            return pkgBase + "." + p2;
        });
        if (res.includes("interface{}")) return res.replace(/interface\{\}/g, "any");
        return res;
    };

    const processFunc = (skyName: string, goName: string, params: Param[], results: Param[], isMethod = false, recvType = "") => {
        let skyArgs = params.map(p => {
            // Pointer-to-struct params accept nil in Go — map to Any for flexibility
            if (p.type.startsWith("*") && !isGoPointerToPrimitive(p.type)) {
                return "Any";
            }
            let t = mapGoTypeToSky(p.type, packageName);
            if (t.includes(" ")) t = `(${t})`;
            return t;
        });

        let retType = "Unit";
        if (results && results.length > 0) {
            const lastType = results[results.length - 1].type;
            const hasError = lastType.endsWith("error");
            if (results.length === 1) {
                if (hasError) {
                    retType = "Result Error Unit";
                } else {
                    retType = mapGoTypeToSky(results[0].type, packageName);
                }
            } else if (results.length === 2 && hasError) {
                const t = mapGoTypeToSky(results[0].type, packageName);
                retType = `Result Error ${t.includes(" ") ? `(${t})` : t}`;
            } else if (results.length === 2 && results[1].type === "bool") {
                // (T, bool) comma-ok pattern → Maybe T
                const t = mapGoTypeToSky(results[0].type, packageName);
                retType = `Maybe ${t.includes(" ") ? `(${t})` : t}`;
            } else if (hasError) {
                // (T1, T2, ..., error) → Result Error (TupleN T1 T2 ...)
                const valueResults = results.slice(0, -1);
                const mapped = valueResults.map(r => mapGoTypeToSky(r.type, packageName));
                const tupleInner = mapped.map(m => m.includes(" ") ? `(${m})` : m).join(" ");
                retType = `Result Error (Tuple${valueResults.length} ${tupleInner})`;
            } else {
                // Multi-return without error → TupleN
                const mapped = results.map(r => mapGoTypeToSky(r.type, packageName));
                const tupleInner = mapped.map(m => m.includes(" ") ? `(${m})` : m).join(" ");
                retType = `Tuple${mapped.length} ${tupleInner}`;
            }
        }

        let sig = skyArgs.length === 0 ? `() -> ${retType}` : skyArgs.join(" -> ") + " -> " + retType;

        const skyNamePascal = skyName.charAt(0).toUpperCase() + skyName.slice(1);
        let wrapperName = `Sky_${safePkg}_${skyNamePascal}`;

        skyiContent += `foreign import "sky_wrappers" exposing (${wrapperName})\n\n`;
        skyiContent += `${skyName} : ${sig}\n`;

        const argNames = params.map((_, i) => `arg${i}`).join(" ");
        if (argNames) {
            skyiContent += `${skyName} ${argNames} = ${wrapperName} ${argNames}\n\n`;
        } else {
            skyiContent += `${skyName} arg0 = ${wrapperName} arg0\n\n`;
        }

        values.push({
            skyName,
            jsName: goName,
            sourceModule: packageName,
            skyType: "Foreign"
        });
    };

    // 4. Functions (skip generic functions — Go can't infer type params from any)
    for (const f of pkg.funcs || []) {
        if (f.hasTypeParams) continue;
        const skyName = lowerCamelCase(f.name);
        processFunc(skyName, f.name, f.params || [], f.results || []);
    }

    // 5. Methods and Field Accessors
    for (const t of pkg.types || []) {
        if (!t.name) continue;
        
        // Methods (skip generic methods)
        if (t.methods) {
            for (const m of t.methods) {
                if (m.hasTypeParams) continue;
                const skyName = lowerCamelCase(t.name + m.name);
                const params = [{ name: "this", type: t.name }, ...(m.params || [])];
                processFunc(skyName, m.name, params, m.results || [], true, t.name);
            }
        }

        // Field Accessors
        if (t.fields) {
            for (const f of t.fields) {
                const skyName = lowerCamelCase(t.name + f.name);
                const retType = mapGoTypeToSky(f.type, packageName);
                
                const skyNamePascal = skyName.charAt(0).toUpperCase() + skyName.slice(1);
                const wrapperName = `Sky_${safePkg}_${skyNamePascal}`;
                
                skyiContent += `foreign import "sky_wrappers" exposing (${wrapperName})\n\n`;
                skyiContent += `${skyName} : ${t.name} -> ${retType}\n`;
                skyiContent += `${skyName} arg0 = ${wrapperName} arg0\n\n`;

                values.push({ skyName, jsName: skyName, sourceModule: packageName, skyType: "Foreign" });
            }
        }
    }

    // 6. Pattern-based convenience wrappers
    for (const t of pkg.types || []) {
        if (!t.name || !t.methods) continue;

        const methodNames = new Set(t.methods.map(m => m.name));

        // Pattern: Iterator with Scan (e.g., sql.Rows)
        if (methodNames.has("Next") && methodNames.has("Scan") && methodNames.has("Columns") && methodNames.has("Close")) {
            const skyName = lowerCamelCase(t.name + "ToMaps");
            const skyNamePascal = skyName.charAt(0).toUpperCase() + skyName.slice(1);
            const wrapperName = `Sky_${safePkg}_${skyNamePascal}`;

            skyiContent += `foreign import "sky_wrappers" exposing (${wrapperName})\n\n`;
            skyiContent += `${skyName} : ${t.name} -> Result Error (List (Dict String String))\n`;
            skyiContent += `${skyName} arg0 = ${wrapperName} arg0\n\n`;

            values.push({ skyName, jsName: skyName, sourceModule: packageName, skyType: "Foreign" });
        }

        // Pattern: DB-like type with Exec(string, ...any) + Query(string, ...any) methods
        const execMethod = t.methods!.find(m => m.name === "Exec");
        const queryMethod = t.methods!.find(m => m.name === "Query");
        const execTakesQuery = execMethod && execMethod.params && execMethod.params.length >= 1 && execMethod.params[0].type === "string";
        const queryTakesQuery = queryMethod && queryMethod.params && queryMethod.params.length >= 1 && queryMethod.params[0].type === "string";
        if (execTakesQuery && queryTakesQuery) {
            // ExecResult: db -> query -> args -> Result Error Int
            const skyNameE = lowerCamelCase(t.name + "ExecResult");
            const skyNameEPascal = skyNameE.charAt(0).toUpperCase() + skyNameE.slice(1);
            const wrapperNameE = `Sky_${safePkg}_${skyNameEPascal}`;

            skyiContent += `foreign import "sky_wrappers" exposing (${wrapperNameE})\n\n`;
            skyiContent += `${skyNameE} : ${t.name} -> String -> (List Any) -> Result Error Int\n`;
            skyiContent += `${skyNameE} arg0 arg1 arg2 = ${wrapperNameE} arg0 arg1 arg2\n\n`;

            values.push({ skyName: skyNameE, jsName: skyNameE, sourceModule: packageName, skyType: "Foreign" });

            // QueryToMaps: db -> query -> args -> Result Error (List (Dict String String))
            const skyNameQ = lowerCamelCase(t.name + "QueryToMaps");
            const skyNameQPascal = skyNameQ.charAt(0).toUpperCase() + skyNameQ.slice(1);
            const wrapperNameQ = `Sky_${safePkg}_${skyNameQPascal}`;

            skyiContent += `foreign import "sky_wrappers" exposing (${wrapperNameQ})\n\n`;
            skyiContent += `${skyNameQ} : ${t.name} -> String -> (List Any) -> Result Error (List (Dict String String))\n`;
            skyiContent += `${skyNameQ} arg0 arg1 arg2 = ${wrapperNameQ} arg0 arg1 arg2\n\n`;

            values.push({ skyName: skyNameQ, jsName: skyNameQ, sourceModule: packageName, skyType: "Foreign" });
        }
    }

    return {
        generated: {
            packageName,
            skyModuleName: `Sky.FFI.${packageName.replace(/\//g, ".")}`,
            runtimeEntryPath: packageName,
            values,
            types: (pkg.types || []).map(t => ({
                skyName: t.name,
                jsName: t.name,
                sourceModule: packageName,
                typeParams: []
            }))
        },
        skyiContent,
        diagnostics: []
    };
  } catch (e: any) {
    console.warn(`Warning: Could not introspect go package ${packageName}. Error: ${e.message}`);
    return { diagnostics: [e.message] };
  }
}

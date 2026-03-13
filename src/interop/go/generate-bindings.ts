// src/interop/go/generate-bindings.ts

import { inspectPackage, Param } from "./inspect-package.js";
import { mapGoTypeToSky, lowerCamelCase } from "./type-mapper.js";

export interface GeneratedForeignBindings {
  packageName: string;
  skyModuleName: string;
  runtimeEntryPath: string;
  values: { skyName: string; jsName: string; sourceModule: string; skyType: string; }[];
  types: { skyName: string; jsName: string; sourceModule: string; typeParams: string[]; }[];
}

export async function generateForeignBindings(packageName: string, requestedNames: string[]): Promise<{ generated?: GeneratedForeignBindings, diagnostics: string[], skyiContent?: string }> {
  try {
    const pkg = inspectPackage(packageName);

    const moduleName = packageName.split(/[\/\.]/).map(p => p.charAt(0).toUpperCase() + p.slice(1)).join(".");
    
    // We will emit the skyi Content here as well.
    let skyiContent = `module ${moduleName} exposing (..)\n\n`;

    // Always emit base utility types
    skyiContent += `type Error = Error\n\ntype Any = Any\n\ntype List a = List\n\ntype Map k v = Map\n\ntype Bytes = Bytes\n\n`;

    const values: GeneratedForeignBindings['values'] = [];

    // Helper to process functions
    const processFunc = (skyName: string, goName: string, params: Param[], results: Param[]) => {
        let skyArgs = params.map(p => {
            let t = mapGoTypeToSky(p.type);
            if (t.includes(" ")) t = `(${t})`;
            return t;
        });

        let retType = "Unit";
        if (results && results.length > 0) {
            if (results.length === 1) {
                if (results[0].type === "error") {
                    retType = "Result Error Unit";
                } else {
                    retType = mapGoTypeToSky(results[0].type);
                }
            } else if (results.length === 2 && results[1].type === "error") {
                const t = mapGoTypeToSky(results[0].type);
                retType = `Result Error ${t.includes(" ") ? `(${t})` : t}`;
            } else {
                const mapped = results.map(r => mapGoTypeToSky(r.type));
                retType = `Tuple${mapped.length} ${mapped.join(" ")}`;
            }
        }

        let sig = skyArgs.length === 0 ? `() -> ${retType}` : skyArgs.join(" -> ") + " -> " + retType;
        
        skyiContent += `${skyName} : ${sig}\n`;
        skyiContent += `foreign import "${packageName}" exposing (${goName})\n\n`;
        
        values.push({
            skyName,
            jsName: goName,
            sourceModule: packageName,
            skyType: "Foreign"
        });
    };

    // 1. Types
    for (const t of pkg.types || []) {
        if (!t.name) continue;
        skyiContent += `type ${t.name} = ${t.name}\n\n`;
    }

    // 2. Constants
    for (const c of pkg.consts || []) {
        const skyName = lowerCamelCase(c.name);
        const t = mapGoTypeToSky(c.type);
        skyiContent += `${skyName} : ${t}\n`;
        skyiContent += `foreign import "${packageName}" exposing (${c.name})\n\n`;
        values.push({ skyName, jsName: c.name, sourceModule: packageName, skyType: "Foreign" });
    }

    // 3. Variables
    for (const v of pkg.vars || []) {
        const skyName = lowerCamelCase(v.name);
        const t = mapGoTypeToSky(v.type);
        skyiContent += `${skyName} : ${t}\n`;
        skyiContent += `foreign import "${packageName}" exposing (${v.name})\n\n`;
        values.push({ skyName, jsName: v.name, sourceModule: packageName, skyType: "Foreign" });
    }

    // 4. Functions
    for (const f of pkg.funcs || []) {
        const skyName = lowerCamelCase(f.name);
        processFunc(skyName, f.name, f.params || [], f.results || []);
    }

    // 5. Methods and Field Accessors
    for (const t of pkg.types || []) {
        if (!t.name) continue;
        
        // Methods
        if (t.methods) {
            for (const m of t.methods) {
                const skyName = lowerCamelCase(t.name + m.name);
                const params = [{ name: "this", type: t.name }, ...(m.params || [])];
                processFunc(skyName, m.name, params, m.results || []);
            }
        }

        // Field Accessors
        if (t.fields) {
            for (const f of t.fields) {
                const skyName = lowerCamelCase(t.name + f.name);
                const retType = mapGoTypeToSky(f.type);
                
                skyiContent += `${skyName} : ${t.name} -> ${retType}\n`;
                // Generate a special foreign import telling the emitter this is a field access
                skyiContent += `foreign import "${packageName}" exposing (${skyName})\n\n`;
                values.push({ skyName, jsName: skyName, sourceModule: packageName, skyType: "Foreign" });
            }
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

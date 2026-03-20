// src/lower/lower-to-go.ts
import * as CoreIR from "../core-ir/core-ir.js";
import * as GoIR from "../go-ir/go-ir.js";
import type { Scheme, Type } from "../types/types.js";
import { pascalToKebab } from "../utils/path.js";

const GO_RESERVED_WORDS = new Set([
  "break", "case", "chan", "const", "continue", "default", "defer", "else",
  "fallthrough", "for", "func", "go", "goto", "if", "import", "interface",
  "map", "package", "range", "return", "select", "struct", "switch", "type",
  "var", "true", "false", "nil", "int", "string", "bool", "float64", "any",
  "error", "len", "cap", "make", "new", "append", "copy", "delete", "panic",
  "recover", "close", "print", "println", "complex", "real", "imag",
]);

function sanitizeGoIdent(name: string): string {
  if (GO_RESERVED_WORDS.has(name)) {
    return name + "_";
  }
  return name;
}

// Minimal Go expression serializer used by the lowerer for GoRawExpr construction.
// Handles the subset of GoIR nodes that appear inside well-known constructor args.
function emitGoExprForLower(expr: any): string {
    if (!expr) return "nil";
    switch (expr.kind) {
        case "GoIdent": return expr.name;
        case "GoBasicLit": return expr.value;
        case "GoCallExpr": {
            const fn = emitGoExprForLower(expr.fn);
            const args = (expr.args || []).map((a: any) => emitGoExprForLower(a)).join(", ");
            return `${fn}(${args})`;
        }
        case "GoSelectorExpr": return `${emitGoExprForLower(expr.expr)}.${expr.sel}`;
        case "GoRawExpr": return expr.code;
        case "GoSliceLit": {
            const elems = (expr.elements || []).map((e: any) => emitGoExprForLower(e)).join(", ");
            return `[]any{${elems}}`;
        }
        case "GoCompositeLit": {
            const typeName = expr.type ? (expr.type.name || "struct {  }") : "struct {  }";
            if (expr.elements && expr.elements.length > 0) {
                const elems = expr.elements.map((e: any) => emitGoExprForLower(e)).join(", ");
                return `${typeName}{${elems}}`;
            }
            return `${typeName}{}`;
        }
        case "GoMapLit": {
            const entries = (expr.entries || []).map((e: any) => `${emitGoExprForLower(e.key)}: ${emitGoExprForLower(e.value)}`).join(", ");
            return `map[string]any{${entries}}`;
        }
        case "GoIndexExpr":
            return `${emitGoExprForLower(expr.expr)}[${emitGoExprForLower(expr.index)}]`;
        case "GoBinaryExpr":
            return `${emitGoExprForLower(expr.left)} ${expr.op} ${emitGoExprForLower(expr.right)}`;
        case "GoUnaryExpr":
            return `${expr.op}${emitGoExprForLower(expr.expr)}`;
        case "GoTypeAssertExpr": {
            let typeStr = "any";
            if (expr.type) {
                if (expr.type.kind === "GoMapType") typeStr = "map[string]any";
                else if (expr.type.kind === "GoSliceType") typeStr = "[]any";
                else if (expr.type.kind === "GoIdentType" || expr.type.name) typeStr = expr.type.name || "any";
            }
            return `${emitGoExprForLower(expr.expr)}.(${typeStr})`;
        }
        default: return `(any)(nil) /* unsupported ${expr.kind} */`;
    }
}

function makeSafeGoPkgName(name: string, fullModulePath?: string): string {
    if (name === "Main") return "main";
    // Use full module path to avoid collisions (e.g., Std.Css vs SkyTailwind.Internal.Css)
    if (fullModulePath) {
        const parts = fullModulePath.split(".");
        if (parts.length > 1) {
            return "sky_" + parts.map(p => p.toLowerCase()).join("_");
        }
    }
    return "sky_" + name.toLowerCase();
}

const stdlibPaths: Record<string, string> = {
    "fmt": "fmt",
    "http": "net/http",
    "io": "io",
    "time": "time",
    "hash": "hash",
    "sha256": "crypto/sha256",
    "hex": "encoding/hex"
};

export function lowerModule(module: CoreIR.Module, moduleExports?: Map<string, Map<string, Scheme>>, foreignModules?: Set<string>, importedModules?: Set<string>): GoIR.GoPackage {
  const pkg: GoIR.GoPackage = {
    name: makeSafeGoPkgName(module.name[module.name.length - 1], module.name.join(".")),
    imports: [],
    declarations: []
  };

  const localEnvOuter = new Map<string, Type>();

  // Build param count map for currying detection
  const declParamCounts = new Map<string, number>();
  for (const decl of module.declarations) {
    let count = 0;
    let body = decl.body;
    while (body.kind === "Lambda") {
      count += body.params.length;
      body = body.body;
    }
    declParamCounts.set(decl.name, count);
  }
  _declParamCounts = declParamCounts;
  _importedModules = importedModules || new Set();

  // Build constructor → ADT mapping (e.g. "GenerateUuid" → { adtName: "Msg", tagIndex: 0, arity: 0 })
  const constructorMap = new Map<string, { adtName: string; tagIndex: number; arity: number }>();
  // Track which type declarations are record aliases (should not emit as Go structs)
  const recordAliasTypes = new Set<string>();
  _recordAliasTypes = recordAliasTypes; // Set module-level ref for lowerType

  for (const tDecl of module.typeDeclarations) {
    // Detect record aliases: single constructor with same name as type,
    // AND the constructor wraps a record type (not a simple wrapper like Sub Foreign)
    if (tDecl.constructors.length === 1 && tDecl.constructors[0].name === tDecl.name) {
      const ctorTypes = tDecl.constructors[0].types;
      const isRecordAlias = ctorTypes.length >= 1 && ctorTypes[0]?.kind === "TypeRecord";
      if (isRecordAlias) {
        recordAliasTypes.add(tDecl.name);
        continue; // Don't emit Go struct for record type aliases (records are maps)
      }
    }

    for (let i = 0; i < tDecl.constructors.length; i++) {
      const c = tDecl.constructors[i];
      constructorMap.set(c.name, { adtName: tDecl.name, tagIndex: i, arity: c.types.length });
    }
  }

  // Convert types (skip record aliases)
  for (const tDecl of module.typeDeclarations) {
    if (recordAliasTypes.has(tDecl.name)) continue;

    const fields: { name: string; type: GoIR.GoType }[] = [
      { name: "Tag", type: { kind: "GoIdentType", name: "int" } }
    ];

    // Naive variant field mapping
    for (const c of tDecl.constructors) {
        if (c.types.length > 0) {
            for (let j = 0; j < c.types.length; j++) {
                fields.push({
                    name: `${c.name}Value${j > 0 ? j : ""}`,
                    type: lowerType(c.types[j])
                });
            }
        }
    }

    pkg.declarations.push({
      kind: "GoTypeDecl",
      name: tDecl.name,
      typeParams: [],
      underlyingType: {
        kind: "GoStructType",
        fields
      }
    });
  }

  // Generate constructor functions for ADT variants (for cross-module use)
  // Skip record aliases and single-constructor types where ctor name = type name
  for (const tDecl of module.typeDeclarations) {
    if (recordAliasTypes.has(tDecl.name)) continue;
    if (tDecl.constructors.length === 1 && tDecl.constructors[0].name === tDecl.name) continue;
    for (let i = 0; i < tDecl.constructors.length; i++) {
      const c = tDecl.constructors[i];
      const kvPairs: string[] = [`Tag: ${i}`];
      const goParams: string[] = [];
      for (let j = 0; j < c.types.length; j++) {
        const fieldName = c.name + "Value" + (j > 0 ? j : "");
        const paramName = `arg${j}`;
        goParams.push(`${paramName} any`);
        kvPairs.push(`${fieldName}: ${paramName}`);
      }
      const goFnName = c.name.charAt(0).toUpperCase() + c.name.slice(1);
      const body = `${tDecl.name}{${kvPairs.join(", ")}}`;
      pkg.declarations.push({
        kind: "GoRawDecl",
        code: `func ${goFnName}(${goParams.join(", ")}) any {\n\treturn ${body}\n}`
      } as any);
    }
  }

  // Convert functions
  for (const decl of module.declarations) {
    const stmts: GoIR.GoStmt[] = [];
    let params: {name: string, type: GoIR.GoType}[] = [];
    const localEnv = new Map<string, Type>(localEnvOuter);

    // Flatten nested lambdas to Go parameters
    let currentType = decl.scheme.type;
    const flattenLambda = (e: CoreIR.Expr) => {
        if (e.kind === "Lambda") {
            for (const p of e.params) {
                let skyType: Type = { kind: "TypeConstant", name: "Any" };
                if (currentType.kind === "TypeFunction") {
                    skyType = currentType.from;
                    currentType = currentType.to;
                }
                params.push({ name: sanitizeGoIdent(p), type: { kind: "GoIdentType", name: "any" } });
                localEnv.set(p, skyType);
            }
            flattenLambda(e.body);
        } else {
            // This is the actual body
            const flattenLet = (inner: CoreIR.Expr) => {
                if (inner.kind === "LetBinding") {
                    if (inner.name === "_") {
                        stmts.push({
                            kind: "GoExprStmt",
                            expr: lowerExpr(inner.value, moduleExports, localEnv, foreignModules, constructorMap)
                        });
                    } else {
                        stmts.push({
                            kind: "GoAssignStmt",
                            define: true,
                            left: [{ kind: "GoIdent", name: sanitizeGoIdent(inner.name) }],
                            right: lowerExpr(inner.value, moduleExports, localEnv, foreignModules, constructorMap)
                        });
                        localEnv.set(inner.name, inner.value.type);
                    }
                    flattenLet(inner.body);
                } else if (inner.kind === "Match") {
                    // Match in let body — return result for non-main functions
                    const loweredMatch = lowerExpr(inner, moduleExports, localEnv, foreignModules, constructorMap);
                    if (decl.name === "main") {
                        stmts.push({ kind: "GoExprStmt", expr: loweredMatch });
                    } else {
                        stmts.push({ kind: "GoReturnStmt", expr: loweredMatch });
                    }
                } else {
                    const lowered = lowerExpr(inner, moduleExports, localEnv, foreignModules, constructorMap);
                    if (decl.name === "main") {
                        stmts.push({ kind: "GoExprStmt", expr: lowered });
                    } else {
                        stmts.push({ kind: "GoReturnStmt", expr: lowered });
                    }
                }
            };
            flattenLet(e);
        }
    };

    flattenLambda(decl.body);

    let retType: GoIR.GoType | undefined = undefined;
    if (decl.name !== "main") {
      retType = { kind: "GoIdentType", name: "any" };
    }

    const goName = decl.name === "main" ? decl.name : decl.name.charAt(0).toUpperCase() + decl.name.slice(1);

    pkg.declarations.push({
      kind: "GoFuncDecl",
      name: goName,
      typeParams: [],
      params: params,
      returnType: retType,
      body: stmts
    });

  }

  const foreignModulesDetected = new Set<string>();
  const localModulesDetected = new Set<string>();
  const scanGoNode = (node: any) => {
      if (!node) return;
      if (typeof node !== "object") return;
      if (node.kind === "GoSelectorExpr" && node.expr && node.expr.kind === "GoIdent") {
         const name = node.expr.name;
         if (name === "sky_wrappers" || stdlibPaths[name]) {
             foreignModulesDetected.add(name);
         } else {
             localModulesDetected.add(name);
         }
      }
      if (node.kind === "GoIdentType" && node.name.includes(".")) {
          const name = node.name.split(".")[0];
          if (name === "sky_wrappers" || stdlibPaths[name]) {
              foreignModulesDetected.add(name);
          } else {
              localModulesDetected.add(name);
          }
      }
      if (node.kind === "GoSelectorType") {
          const name = node.pkg;
          if (name === "sky_wrappers" || stdlibPaths[name]) {
              foreignModulesDetected.add(name);
          } else {
              localModulesDetected.add(name);
          }
      }
      // Detect module references inside GoRawExpr code strings
      if (node.kind === "GoRawExpr" && typeof node.code === "string") {
          const rawMatches = node.code.match(/\bsky_\w+\./g);
          if (rawMatches) {
              for (const m of rawMatches) {
                  const name = m.slice(0, -1); // remove trailing dot
                  if (name === "sky_wrappers" || stdlibPaths[name]) {
                      foreignModulesDetected.add(name);
                  } else {
                      localModulesDetected.add(name);
                  }
              }
          }
          if (node.code.includes("sky_wrappers.")) {
              foreignModulesDetected.add("sky_wrappers");
          }
      }
      for (const k of Object.keys(node)) {
          scanGoNode(node[k]);
      }
  };

  for (const decl of pkg.declarations) {
      scanGoNode(decl);
  }

  if (foreignModulesDetected.has("sky_wrappers") || localModulesDetected.has("sky_wrappers")) {
      pkg.imports.push({ path: "sky-out/sky_wrappers", alias: "sky_wrappers" });
  }
  
  for (const mod of foreignModulesDetected) {
      if (mod === "sky_wrappers") continue;
      const path = stdlibPaths[mod];
      if (path) {
          pkg.imports.push({ path });
      }
  }

  for (const local of localModulesDetected) {
      if (stdlibPaths[local] || local === "sky_wrappers") continue;
      // Find the full module name for this local pkg name
      if (moduleExports) {
          for (const full of moduleExports.keys()) {
              const moduleParts = full.split(".");
              const safeName = makeSafeGoPkgName(moduleParts[moduleParts.length - 1], full);
              if (safeName === local) {
                  pkg.imports.push({ path: "sky-out/" + full.replace(/\./g, "/"), alias: local });
                  break;
              }
          }
      }
  }

  return pkg;
}

// Module-level set of record alias type names, populated by lowerModule
let _recordAliasTypes: Set<string> = new Set();
let _declParamCounts: Map<string, number> = new Map();
let _importedModules: Set<string> = new Set();

function lowerType(t: Type): GoIR.GoType {
  if (!t || !t.kind) {
      return { kind: "GoIdentType", name: "any" };
  }
  // Simplified type lowering
  if (t.kind === "TypeConstant") {
    if (t.name === "Int") return { kind: "GoIdentType", name: "int" };
    if (t.name === "Float") return { kind: "GoIdentType", name: "float64" };
    if (t.name === "Bool") return { kind: "GoIdentType", name: "bool" };
    if (t.name === "String") return { kind: "GoIdentType", name: "string" };
    if (t.name === "Unit") return { kind: "GoStructType", fields: [] };
    if (t.name === "Any") return { kind: "GoIdentType", name: "any" };
    if (t.name === "Bytes") return { kind: "GoSliceType", elem: { kind: "GoIdentType", name: "byte" } };
    // Record aliases (like Model) are maps, not Go structs
    if (_recordAliasTypes.has(t.name)) return { kind: "GoIdentType", name: "any" };
    
    if (t.name.startsWith("Untyped ")) {
        const inner = t.name.substring(8);
        if (inner === "int") return { kind: "GoIdentType", name: "int" };
        if (inner === "float") return { kind: "GoIdentType", name: "float64" };
        return { kind: "GoIdentType", name: "any" };
    }
    
    if (t.name.includes(".")) {
        const parts = t.name.split(".");
        const modulePath = parts.slice(0, -1).join(".");
        const pkg = makeSafeGoPkgName(parts[parts.length - 2], modulePath);
        const name = parts[parts.length - 1];
        return { kind: "GoSelectorType", pkg, name };
    }

    // Known Go FFI types that should map to any (interfaces, structs from Go packages)
    // These are types from .skyi binding files like Writer, Reader, Request, Response, etc.
    return {
      kind: "GoIdentType",
      name: "any"
    };
  }
  if (t.kind === "TypeVariable") {
    return { kind: "GoIdentType", name: "any" };
  }
  if (t.kind === "TypeFunction") {
    return {
      kind: "GoFuncType",
      params: [lowerType(t.from)],
      results: [lowerType(t.to)]
    };
  }
  if (t.kind === "TypeTuple") {
    return { kind: "GoIdentType", name: "sky_wrappers.Tuple" + t.items.length };
  }
  if (t.kind === "TypeRecord") {
    return { kind: "GoIdentType", name: "any" };
  }
  if (t.kind === "TypeApplication") {
    const base = lowerType(t.constructor);
    if (base.kind === "GoIdentType") {
       if (base.name === "Untyped") {
           if (t.arguments && t.arguments.length > 0) {
               const arg = lowerType(t.arguments[0]);
               if (arg.kind === "GoIdentType") {
                   if (arg.name === "int") return { kind: "GoIdentType", name: "int" };
                   if (arg.name === "float64") return { kind: "GoIdentType", name: "float64" };
               }
           }
           return { kind: "GoIdentType", name: "any" };
       }
       if (base.name === "Result") return { kind: "GoIdentType", name: "sky_wrappers.SkyResult" };
       return {
         ...base,
         typeArgs: t.arguments?.map(lowerType)
       };
    }
    return base;
  }
  return { kind: "GoIdentType", name: "any" };
}

function lowerExpr(expr: CoreIR.Expr, moduleExports?: Map<string, Map<string, Scheme>>, localEnv?: Map<string, Type>, foreignModules?: Set<string>, constructorMap?: Map<string, { adtName: string; tagIndex: number; arity: number }>, _isCallTarget?: boolean): GoIR.GoExpr {
  switch (expr.kind) {
    case "Literal": {
      if (expr.literalType === "String") {
        // Escape backslashes and double quotes for Go string literal
        const escaped = String(expr.value).replace(/\\/g, '\\\\').replace(/"/g, '\\"').replace(/\n/g, '\\n').replace(/\r/g, '\\r').replace(/\t/g, '\\t');
        return { kind: "GoBasicLit", value: '"' + escaped + '"' };
      }
      if (expr.literalType === "Unit") {
          return { kind: "GoCompositeLit", type: { kind: "GoStructType", fields: [] }, elements: [] };
      }
      if (expr.literalType === "Bool") {
          return { kind: "GoBasicLit", value: String(expr.value).toLowerCase() };
      }
      return { kind: "GoBasicLit", value: String(expr.value) };
    }
    case "Variable": {
      // Field accessor functions like ".uuid" — keep as-is for Application handler
      if (expr.name.startsWith(".")) {
          return { kind: "GoIdent", name: expr.name };
      }
      if (expr.name.includes(".")) {
          const parts = expr.name.split(".");
          const moduleParts = parts.slice(0, -1);
          const name = parts[parts.length - 1];
          const pkgName = moduleParts.join(".");
          
          // Built-ins and Foreign
          if (pkgName === "String" && name === "toBytes") {
              return { kind: "GoIdent", name: "[]byte" };
          }
          if (pkgName === "fmt" && name === "Sprintf") {
              return { kind: "GoSelectorExpr", expr: { kind: "GoIdent", name: "fmt" }, sel: "Sprintf" };
          }
          if (pkgName === "sky_builtin" && name === "stringToBytes") {
              return { kind: "GoIdent", name: "[]byte" };
          }
          if (pkgName === "updateRecord") {
              return { kind: "GoSelectorExpr", expr: { kind: "GoIdent", name: "sky_wrappers" }, sel: "UpdateRecord" };
          }

          // sky_wrappers functions already have their final names — use as-is
          if (pkgName === "sky_wrappers") {
              return { kind: "GoSelectorExpr", expr: { kind: "GoIdent", name: "sky_wrappers" }, sel: name };
          }

          // Heuristic for Go FFI wrappers
          let safePkg = pkgName.toLowerCase().replace(/\./g, "_");
          
          if (pkgName === "Http" || pkgName === "Net.Http") safePkg = "net_http";
          else if (pkgName === "Sha256" || pkgName === "Crypto.Sha256") safePkg = "crypto_sha256";
          else if (pkgName === "Hex" || pkgName === "Encoding.Hex") safePkg = "encoding_hex";
          else if (pkgName === "Time" || pkgName === "Std.Time") safePkg = "time";
          else if (pkgName === "Uuid" || pkgName === "Std.Uuid") safePkg = "github_com_google_uuid";
          else if (pkgName === "Dotenv" || pkgName === "Std.Dotenv") safePkg = "github_com_joho_godotenv";
          else if (pkgName === "Cmd" || pkgName === "Std.Cmd") safePkg = "std_cmd";
          else if (pkgName === "Sub" || pkgName === "Std.Sub") safePkg = "std_sub";
          else if (pkgName === "Log" || pkgName === "Std.Log") safePkg = "fmt"; // special case
          else {
              // Resolve short module names to full names for wrapper lookup.
              // E.g., "Schema" → "Github.Com.KandaCo.KsSchema.Pkg.Schema"
              let fullPkgName = pkgName;
              if (moduleExports) {
                  for (const full of moduleExports.keys()) {
                      const lastPart = full.split(".").pop();
                      if (lastPart === pkgName || full === pkgName) {
                          fullPkgName = full;
                          break;
                      }
                  }
              }
              // Convert PascalCase parts to kebab-case first (FyneIo -> fyne-io),
              // then replace all separators with underscores to match makeSafeGoName
              safePkg = fullPkgName.split(".").map(p => pascalToKebab(p)).join("_").replace(/[\/\.-]/g, "_");
          }

          const goName = name.charAt(0).toUpperCase() + name.slice(1);

          // Check if this is a Sky module (not a Go FFI module) — must run before
          // the Std.* FFI shortcut so that real Sky modules like Std.Html take priority.
          // Exclude thin FFI wrapper modules that only re-export foreign imports.
          const ffiWrappers = new Set(["Std.Log", "Std.Cmd", "Std.Task", "Std.Program", "Cmd", "Log"]);
          if (moduleExports && moduleExports.has(pkgName) && !ffiWrappers.has(pkgName) && (!foreignModules || !foreignModules.has(pkgName))) {
              // It's a non-foreign Sky module! Lower to direct Go package call.
              const goPkg = makeSafeGoPkgName(moduleParts[moduleParts.length - 1], pkgName);
              const selectorExpr: GoIR.GoExpr = { kind: "GoSelectorExpr", expr: { kind: "GoIdent", name: goPkg }, sel: goName };

              // Auto-call zero-arg cross-module bindings (they compile to Go functions).
              // Check if the export's type scheme is a non-function type → arity 0.
              if (!_isCallTarget) {
                  const exports = moduleExports.get(pkgName);
                  const scheme = exports?.get(name);
                  if (scheme && scheme.type && scheme.type.kind !== "TypeFunction") {
                      return { kind: "GoCallExpr", fn: selectorExpr, args: [] } as any;
                  }
              }

              return selectorExpr;
          }

          // FFI wrapper modules (Std.Log, Std.Cmd etc.) — route through sky_wrappers
          // Std.Sub and Std.Time are normal ADT modules — skip FFI routing
          if (pkgName === "Std.Sub" || pkgName === "Sub" || pkgName === "Std.Time") {
              // fall through to normal module resolution below
          } else if (pkgName.startsWith("Std.") || pkgName === "Net.Http" || pkgName === "Crypto.Sha256" || pkgName === "Encoding.Hex" || pkgName === "Cmd" || pkgName === "Uuid" || pkgName === "Dotenv") {
              if (name === "none") {
                  return { kind: "GoSelectorExpr", expr: { kind: "GoIdent", name: "sky_wrappers" }, sel: "CmdNone" };
              }
              const wrapperName = "Sky_" + safePkg + "_" + goName;
              return { kind: "GoSelectorExpr", expr: { kind: "GoIdent", name: "sky_wrappers" }, sel: wrapperName };
          }

          // For foreign Go FFI modules, reference the wrapper directly
          const wrapperName2 = "Sky_" + safePkg + "_" + goName;
          return { kind: "GoSelectorExpr", expr: { kind: "GoIdent", name: "sky_wrappers" }, sel: wrapperName2 };
      }
      
      if (expr.name === "Sprintf") {
          return { kind: "GoSelectorExpr", expr: { kind: "GoIdent", name: "fmt" }, sel: "Sprintf" };
      }
      if (expr.name === "stringToBytes") {
          return { kind: "GoIdent", name: "[]byte" };
      }
      if (expr.name === "updateRecord") {
          return { kind: "GoSelectorExpr", expr: { kind: "GoIdent", name: "sky_wrappers" }, sel: "UpdateRecord" };
      }
      // Prelude: errorToString → sky_wrappers.Sky_errorToString
      if (expr.name === "errorToString" || expr.name === "Sky_errorToString") {
          return { kind: "GoSelectorExpr", expr: { kind: "GoIdent", name: "sky_wrappers" }, sel: "Sky_errorToString" };
      }
      // Prelude: not → inline Go negation function
      if (expr.name === "not") {
          return { kind: "GoRawExpr", code: `func(arg0 any) any { if arg0.(bool) { return false }; return true }` } as any;
      }
      
      const goName = (expr.name[0] >= 'a' && expr.name[0] <= 'z') ? expr.name.charAt(0).toUpperCase() + expr.name.slice(1) : expr.name;

      // If it's a local variable, don't capitalize
      if (localEnv && localEnv.has(expr.name)) {
          return { kind: "GoIdent", name: sanitizeGoIdent(expr.name) };
      }

      // If the variable is a multi-param top-level function used as a value,
      // generate a currying wrapper so it can be passed to FFI functions expecting func(any) any
      // Check if this is a known top-level function by looking at the module's declarations
      const declArity = _declParamCounts?.get(expr.name);
      // Zero-param top-level bindings are emitted as Go functions; call them when referenced as values
      if (declArity === 0 && !_isCallTarget && _declParamCounts?.has(expr.name)) {
          return { kind: "GoCallExpr", fn: { kind: "GoIdent", name: goName }, args: [] } as any;
      }
      if (declArity && declArity >= 2 && !_isCallTarget) {
              // Generate curried wrapper using raw Go string
              // e.g., func(__c0 any) any { return func(__c1 any) any { return GoName(__c0, __c1) } }
              const cid = Math.floor(Math.random() * 10000);
              const paramNames = Array.from({length: declArity}, (_, i) => `__c${cid}_${i}`);
              const callArgs = paramNames.join(", ");
              let code = "";
              for (let i = 0; i < declArity; i++) {
                  code += `func(${paramNames[i]} any) any { return `;
              }
              code += `${goName}(${callArgs})`;
              for (let i = 0; i < declArity; i++) {
                  code += ` }`;
              }
              return { kind: "GoRawExpr", code } as any;
      }

      // If the unqualified name isn't local, check if it comes from an imported module.
      // This handles `exposing (..)` imports where names are unqualified in Sky
      // but must be qualified with the Go package name in the output.
      // Excludes Prelude types (Ok, Err, identity) and thin FFI wrapper modules
      // (Std.Log, Std.Cmd, etc.) which have special lowering paths.
      if (moduleExports && !_declParamCounts?.has(expr.name)) {
          const ffiWrapperModules = new Set(["Std.Log", "Std.Cmd", "Std.Task", "Std.Program", "Sky.Core.Prelude"]);
          // When multiple modules export the same name, prefer the one actually imported
          let candidates: [string, Map<string, Scheme>][] = [];
          for (const [modName, exports] of moduleExports) {
              if (exports.has(expr.name) && !ffiWrapperModules.has(modName) && (!foreignModules || !foreignModules.has(modName))) {
                  candidates.push([modName, exports]);
              }
          }
          // Prefer modules explicitly imported by the current file
          if (candidates.length > 1 && _importedModules.size > 0) {
              const imported = candidates.filter(([m]) => _importedModules.has(m));
              if (imported.length > 0) candidates = imported;
          }
          if (candidates.length > 0) {
              const [modName, exports] = candidates[0];
              const modParts = modName.split(".");
              const goPkg = makeSafeGoPkgName(modParts[modParts.length - 1], modName);
              const selectorExpr2: GoIR.GoExpr = { kind: "GoSelectorExpr", expr: { kind: "GoIdent", name: goPkg }, sel: goName };
              // Auto-call zero-arg cross-module bindings
              if (!_isCallTarget) {
                  const scheme = exports.get(expr.name);
                  if (scheme && scheme.type && scheme.type.kind !== "TypeFunction") {
                      return { kind: "GoCallExpr", fn: selectorExpr2, args: [] } as any;
                  }
              }
              return selectorExpr2;
          }
      }

      return { kind: "GoIdent", name: goName };
    }
    case "Lambda": {
      const newLocalEnv = new Map(localEnv || []);
      for (const p of expr.params) {
        newLocalEnv.set(p, { kind: "Unknown" } as any); // just truthy for localEnv
      }
      return {
        kind: "GoFuncLit",
        type: {
          kind: "GoFuncType",
          params: expr.params.map(() => ({ kind: "GoIdentType", name: "any" } as GoIR.GoType)),
          results: [{ kind: "GoIdentType", name: "any" }]
        },
        body: [
          ...expr.params.map((p, i): GoIR.GoStmt => ({
            kind: "GoAssignStmt",
            left: [{ kind: "GoIdent", name: sanitizeGoIdent(p) }],
            right: { kind: "GoIdent", name: `arg${i}` },
            define: true
          })),
          { kind: "GoReturnStmt", expr: lowerExpr(expr.body, moduleExports, newLocalEnv, foreignModules, constructorMap) }
        ]
      };
    }
    case "IfExpr": {
      // Ensure condition is bool — add .(bool) assertion if needed
      let condExpr = lowerExpr(expr.condition, moduleExports, localEnv, foreignModules, constructorMap);
      // If the condition is already a binary comparison (GoBinaryExpr), it's bool.
      // Otherwise, add a type assertion.
      if ((condExpr as any).kind !== "GoBinaryExpr") {
          condExpr = { kind: "GoTypeAssertExpr", expr: condExpr, type: { kind: "GoIdentType", name: "bool" } } as any;
      }
      return {
        kind: "GoCallExpr",
        fn: {
          kind: "GoFuncLit",
          type: { kind: "GoFuncType", params: [], results: [{ kind: "GoIdentType", name: "any" }] },
          body: [
            {
              kind: "GoIfStmt",
              condition: condExpr,
              thenBranch: [{ kind: "GoReturnStmt", expr: lowerExpr(expr.thenBranch, moduleExports, localEnv, foreignModules, constructorMap) }],
              elseBranch: [{ kind: "GoReturnStmt", expr: lowerExpr(expr.elseBranch, moduleExports, localEnv, foreignModules, constructorMap) }]
            }
          ]
        },
        args: []
      };
    }
    case "Application": {
      // Uncurry the application if it's a chain of calls
      const flattenApp = (app: CoreIR.Application): { fn: CoreIR.Expr, args: CoreIR.Expr[] } => {
        if (app.fn.kind === "Application") {
          const inner = flattenApp(app.fn);
          return { fn: inner.fn, args: [...inner.args, ...app.args] };
        }
        return { fn: app.fn, args: app.args };
      };

      const flat = flattenApp(expr);

      // Well-known Prelude constructors applied as functions: Ok value, Err value, Just value
      if (flat.fn.kind === "Constructor") {
          const wellKnownAppCtors: Record<string, { wrapper: string; tag: number; field: string }> = {
              "Ok":   { wrapper: "sky_wrappers.SkyOk",  tag: 0, field: "OkValue" },
              "Err":  { wrapper: "sky_wrappers.SkyErr",  tag: 1, field: "ErrValue" },
              "Just": { wrapper: "",                     tag: 0, field: "JustValue" },
          };
          const wk = wellKnownAppCtors[flat.fn.name];
          if (wk) {
              const argExprs = flat.args.map(a => lowerExpr(a, moduleExports, localEnv, foreignModules, constructorMap));
              if (wk.wrapper) {
                  // Result Ok/Err
                  return {
                      kind: "GoCallExpr",
                      fn: { kind: "GoIdent", name: wk.wrapper },
                      args: argExprs
                  } as any;
              }
              // Maybe Just
              return {
                  kind: "GoRawExpr",
                  code: `struct{ Tag int; ${wk.field} any }{Tag: ${wk.tag}, ${wk.field}: ${emitGoExprForLower(argExprs[0])}}`
              } as any;
          }
          // Check local constructorMap for ADT constructor applications
          const localCtorInfo = constructorMap?.get(flat.fn.name);
          if (localCtorInfo) {
              const argExprs = flat.args.map(a => lowerExpr(a, moduleExports, localEnv, foreignModules, constructorMap));
              // Use named field init so constructors with shared structs work correctly
              const kvPairs: string[] = [`Tag: ${localCtorInfo.tagIndex}`];
              for (let j = 0; j < flat.args.length; j++) {
                  const fieldName = flat.fn.name + "Value" + (j > 0 ? j : "");
                  kvPairs.push(`${fieldName}: ${emitGoExprForLower(argExprs[j])}`);
              }
              return {
                  kind: "GoRawExpr",
                  code: `${localCtorInfo.adtName}{${kvPairs.join(", ")}}`
              } as any;
          }
          // Check imported modules for constructor applications
          // Emit as function call to the generated constructor function in the target package
          if (moduleExports) {
              for (const [modName, exports] of moduleExports) {
                  if (exports.has(flat.fn.name)) {
                      const ffiWrapperModulesCheck = new Set(["Std.Log", "Std.Cmd", "Std.Task", "Std.Program", "Sky.Core.Prelude"]);
                      if (ffiWrapperModulesCheck.has(modName)) break; // Skip FFI wrapper modules
                      const parts = modName.split(".");
                      const pkgName = makeSafeGoPkgName(parts[parts.length - 1], modName);
                      const goCtorName = flat.fn.name.charAt(0).toUpperCase() + flat.fn.name.slice(1);
                      const argExprs = flat.args.map(a => lowerExpr(a, moduleExports, localEnv, foreignModules, constructorMap));
                      // Call the generated constructor function: pkg.CtorName(args...)
                      return {
                          kind: "GoCallExpr",
                          fn: { kind: "GoSelectorExpr", expr: { kind: "GoIdent", name: pkgName }, sel: goCtorName },
                          args: argExprs,
                      } as any;
                  }
              }
          }
      }

      // Desugar pipe operators: `a |> f` → `f(a)`, `f <| a` → `f(a)`
      if (flat.fn.kind === "Variable" && flat.fn.name === "|>" && flat.args.length === 2) {
          const [value, fn] = flat.args;
          return lowerExpr({ kind: "Application", fn, args: [value], type: expr.type }, moduleExports, localEnv, foreignModules, constructorMap);
      }
      if (flat.fn.kind === "Variable" && flat.fn.name === "<|" && flat.args.length === 2) {
          const [fn, value] = flat.args;
          return lowerExpr({ kind: "Application", fn, args: [value], type: expr.type }, moduleExports, localEnv, foreignModules, constructorMap);
      }

      // Partial application: if applying fewer args than the function's arity,
      // generate a curried closure that captures the applied args.
      // e.g., `handleLanding db` (arity 3, 1 arg) →
      //   func(__p0 any) any { return func(__p1 any) any { return HandleLanding(db, __p0, __p1) } }
      if (flat.fn.kind === "Variable" && !flat.fn.name.startsWith(".")) {
          const fnArity = _declParamCounts?.get(flat.fn.name);
          if (fnArity && flat.args.length < fnArity) {
              const remaining = fnArity - flat.args.length;
              const appliedArgs = flat.args.map(a => lowerExpr(a, moduleExports, localEnv, foreignModules, constructorMap));
              const goFnName = flat.fn.name.charAt(0).toUpperCase() + flat.fn.name.slice(1);
              const pid = Math.floor(Math.random() * 10000);
              const remainingNames = Array.from({length: remaining}, (_, i) => `__p${pid}_${i}`);
              const allCallArgs = [...appliedArgs.map(a => emitGoExprForLower(a)), ...remainingNames].join(", ");
              let code = "";
              for (let i = 0; i < remaining; i++) {
                  code += `func(${remainingNames[i]} any) any { return `;
              }
              code += `${goFnName}(${allCallArgs})`;
              for (let i = 0; i < remaining; i++) {
                  code += ` }`;
              }
              return { kind: "GoRawExpr", code } as any;
          }
      }

      // Map listenAndServe to http.ListenAndServe and println to fmt.Println
      let fnExpr = lowerExpr(flat.fn, moduleExports, localEnv, foreignModules, constructorMap, true);
      let args = flat.args.map((a, i) => {
          let lowered = lowerExpr(a, moduleExports, localEnv, foreignModules, constructorMap);

          // Heuristic: if it's a Go FFI call and the arg is known to be Bytes (which might be an array in Go)
          const coreArg = flat.args[i];
          const isForeign = (fnExpr.kind === "GoSelectorExpr" && 
                           fnExpr.expr.kind === "GoIdent" && 
                           fnExpr.expr.name === "sky_wrappers") ||
                           (fnExpr.kind === "GoSelectorExpr" && 
                           fnExpr.expr.kind === "GoIdent" && 
                           stdlibPaths[fnExpr.expr.name]);
          
          if (isForeign) {
              let isBytes = false;
              if (coreArg.type && coreArg.type.kind === "TypeConstant" && coreArg.type.name === "Bytes") {
                  isBytes = true;
              } else if (coreArg.kind === "Variable" && localEnv) {
                  const t = localEnv.get(coreArg.name);
                  if (t && t.kind === "TypeConstant" && (t.name === "Bytes" || t.name.startsWith("["))) {
                      isBytes = true;
                  }
              }
              
              if (isBytes) {
                  return {
                      kind: "GoSliceExpr",
                      expr: lowered,
                  } as any;
              }
          }
          return lowered;
      });

      // Field access like .uuid model -> model.(map[string]any)["uuid"]
      if (fnExpr.kind === "GoIdent" && fnExpr.name.startsWith(".")) {
          const fieldName = fnExpr.name.substring(1);
          const container = args[0];
          // Type-assert container to map[string]any for record field access
          const assertedContainer: GoIR.GoExpr = {
              kind: "GoTypeAssertExpr",
              expr: container,
              type: { kind: "GoMapType", key: { kind: "GoIdentType", name: "string" }, value: { kind: "GoIdentType", name: "any" } }
          } as any;
          return {
              kind: "GoIndexExpr",
              expr: assertedContainer,
              index: { kind: "GoBasicLit", value: '"' + fieldName + '"' }
          } as any;
      }

      // Handle Go FFI zero-arg calls (which take unit () in Sky)
      if (args.length === 1 && (flat.args[0] as any).kind === "Literal" && (flat.args[0] as any).literalType === "Unit") {
          const isWrappers = (fnExpr.kind === "GoSelectorExpr" && fnExpr.expr.kind === "GoIdent" && fnExpr.expr.name === "sky_wrappers");
          if (isWrappers) {
              args = [];
          }
      }

      if (fnExpr.kind === "GoIdent" && fnExpr.name === "listenAndServe") {
        fnExpr = { kind: "GoSelectorExpr", expr: { kind: "GoIdent", name: "http" }, sel: "ListenAndServe" };
        if (args.length > 1 && (args[1] as any).kind === "GoBasicLit" && (args[1] as any).value === '"nil"') {
          args[1] = { kind: "GoIdent", name: "nil" };
        }
      } else if (fnExpr.kind === "GoIdent" && fnExpr.name.startsWith("Tuple")) {
          // Special case for Tuples: map Tuple2 a b to sky_wrappers.Tuple2{V0: a, V1: b}
          return {
              kind: "GoCompositeLit",
              type: { 
                  kind: "GoSelectorType", 
                  pkg: "sky_wrappers", 
                  name: fnExpr.name 
              },
              elements: args
          } as any;
      } else if (fnExpr.kind === "GoIdent" && fnExpr.name === "[]byte") {
          // Special case: []byte(s.(string))
          return {
              kind: "GoCallExpr",
              fn: fnExpr,
              args: args.map((a, i) => {
                  const coreArg = flat.args[i];
                  let isString = false;
                  if (coreArg.type && coreArg.type.kind === "TypeConstant" && coreArg.type.name === "String") {
                      isString = true;
                  } else if (coreArg.kind === "Variable" && localEnv) {
                      const t = localEnv.get(coreArg.name);
                      if (t && t.kind === "TypeConstant" && t.name === "String") {
                          isString = true;
                      }
                  }
                  
                  if (isString) {
                      return a;
                  }
                  return {
                      kind: "GoTypeAssertExpr",
                      expr: a,
                      type: { kind: "GoIdentType", name: "string" }
                  } as any;
              })
          };
      } else if (fnExpr.kind === "GoIdent" && (fnExpr.name === "UpdateRecord" || fnExpr.name === "Updaterecord")) {
          // Special case for record update: map { model | uuid = x } to UpdateRecord(model, {uuid: x})
          const base = args[0];
          const update = args[1];
          return {
              kind: "GoCallExpr",
              fn: { kind: "GoSelectorExpr", expr: { kind: "GoIdent", name: "sky_wrappers" }, sel: "UpdateRecord" },
              args: [base, update]
          };
      } else if (fnExpr.kind === "GoIdent" && (fnExpr.name === "Println" || fnExpr.name === "Printf" || fnExpr.name === "Sprintf")) {
        const fmtFn = fnExpr.name;
        fnExpr = { kind: "GoSelectorExpr", expr: { kind: "GoIdent", name: "fmt" }, sel: fmtFn };

        // Printf/Sprintf need first arg as string, rest as any
        const callArgs: GoIR.GoExpr[] = args.map((_, i) => {
            if (i === 0 && (fmtFn === "Printf" || fmtFn === "Sprintf")) {
                return { kind: "GoTypeAssertExpr", expr: { kind: "GoIdent", name: "arg" + i }, type: { kind: "GoIdentType", name: "string" } } as any;
            }
            return { kind: "GoIdent", name: "arg" + i } as any;
        });

        // Sprintf returns a value directly, no wrapping needed
        if (fmtFn === "Sprintf") {
            return {
                kind: "GoCallExpr",
                fn: fnExpr,
                args: callArgs
            } as any;
        }

        // Println/Printf: wrap to discard (int, error) and return Unit
        return {
            kind: "GoCallExpr",
            fn: {
                kind: "GoFuncLit",
                type: { kind: "GoFuncType", params: args.map((_, i) => ({ kind: "GoIdentType", name: "any" } as any)), results: [{ kind: "GoIdentType", name: "any" }] },
                body: [
                    {
                        kind: "GoExprStmt",
                        expr: { kind: "GoCallExpr", fn: fnExpr, args: callArgs }
                    },
                    {
                        kind: "GoReturnStmt",
                        expr: { kind: "GoCompositeLit", type: { kind: "GoStructType", fields: [] }, elements: [] }
                    }
                ]
            },
            args: args as GoIR.GoExpr[]
        } as any;
      } else if (fnExpr.kind === "GoIdent" && (["+", "-", "*", "/", "++", "==", "!=", "<", ">", "<=", ">=", "&&", "||"].includes(fnExpr.name))) {
          // Check if ++ is operating on lists (type is List/TypeApplication with List constructor)
          if (fnExpr.name === "++" && expr.type && expr.type.kind === "TypeApplication" &&
              expr.type.constructor.kind === "TypeConstant" && expr.type.constructor.name === "List") {
              // List concatenation: append(left, right.([]any)...)
              // Wrap in (any)(...) to handle both []any literals and any-typed variables
              const l = emitGoExprForLower(args[0]);
              const r = emitGoExprForLower(args[1]);
              return {
                  kind: "GoRawExpr",
                  code: `append((any)(${l}).([]any), (any)(${r}).([]any)...)`
              } as any;
          }

          // Binary operator uncurried
          const op = fnExpr.name === "++" ? "+" : fnExpr.name;

          // Add type assertions for binary operators on any-typed values
          const fnName0 = (fnExpr as any).name;
          const finalArgs = args.map((a, i) => {
              const coreArg = flat.args[i];
              // Check if the Go expression is already a concrete type (literal, type-asserted, etc.)
              const isAlreadyConcrete = (a as any).kind === "GoBasicLit" ||
                  (a as any).kind === "GoTypeAssertExpr" ||
                  (a as any).kind === "GoBinaryExpr";
              if (isAlreadyConcrete) return a;

              // Determine the target assertion type from the operator
              let targetType: string | null = null;
              if (fnName0 === "++") {
                  targetType = "string";
              } else if (["+", "-", "*", "/", "%"].includes(fnName0)) {
                  targetType = "int";
              } else if (["<", ">", "<=", ">="].includes(fnName0)) {
                  // Comparison — check if any arg is a string literal
                  const hasStringLit = flat.args.some(a2 => a2.kind === "Literal" && typeof (a2 as any).value === "string");
                  targetType = hasStringLit ? "string" : "int";
              } else if (fnName0 === "==" || fnName0 === "!=") {
                  targetType = null; // equality works on any
              } else if (fnName0 === "&&" || fnName0 === "||") {
                  targetType = "bool";
              }

              if (!targetType) return a;

              // Wrap in (any)(...) to ensure the value is interface-typed
              // before applying the type assertion.  This is necessary because
              // Go FFI wrappers may return concrete types (e.g. string) and
              // Go does not allow type assertions on non-interface values.
              return {
                  kind: "GoRawExpr",
                  code: `(any)(${emitGoExprForLower(a)}).(${targetType})`
              } as any;
          });

          return {
              kind: "GoBinaryExpr",
              left: finalArgs[0],
              op: op,
              right: finalArgs[1]
          };
      }
      
      // If calling a local variable (not a known Go function), add type assertion
      // so Go knows it's callable: fn.(func(any) any)(args...)
      // Wrap in (any)(...) first to handle both any-typed and concrete-typed variables
      let callFn = fnExpr;
      if (fnExpr.kind === "GoIdent" && !["fmt", "len", "panic", "append", "make", "[]byte"].includes(fnExpr.name) && !fnExpr.name.startsWith(".")) {
          // Check if it's a local variable (lambda parameter or let binding) — needs type assertion
          if (localEnv && localEnv.has(fnExpr.name)) {
              // Emit curried calls for multi-arg local variable calls:
              // f(a, b, c) → (any)(f).(func(any) any)(a).(func(any) any)(b).(func(any) any)(c)
              if (args.length > 1) {
                  const safeName = sanitizeGoIdent(fnExpr.name);
                  let code = `(any)(${safeName}).(func(any) any)(${emitGoExprForLower(args[0])})`;
                  for (let ci = 1; ci < args.length; ci++) {
                      code = `(any)(${code}).(func(any) any)(${emitGoExprForLower(args[ci])})`;
                  }
                  return { kind: "GoRawExpr", code } as any;
              }
              callFn = {
                  kind: "GoRawExpr",
                  code: `(any)(${sanitizeGoIdent(fnExpr.name)}).(func(any) any)`
              } as any;
          }
      }

      const result: GoIR.GoExpr = {
        kind: "GoCallExpr",
        fn: callFn,
        args: args as GoIR.GoExpr[]
      };

      return result;
    }
    case "LetBinding": {
      // Create an IIFE for local let bindings inside expressions
      const stmts: GoIR.GoStmt[] = [];
      const newLocalEnv = new Map(localEnv || []);
      
      const flattenLet = (e: CoreIR.Expr) => {
        if (e.kind === "LetBinding") {
          newLocalEnv.set(e.name, e.value.type);
          stmts.push({
            kind: "GoAssignStmt",
            define: true,
            left: [{ kind: "GoIdent", name: sanitizeGoIdent(e.name) }],
            right: lowerExpr(e.value, moduleExports, newLocalEnv, foreignModules, constructorMap)
          });
          flattenLet(e.body);
        } else if (e.kind === "Match") {
            stmts.push({
                kind: "GoExprStmt",
                expr: lowerExpr(e, moduleExports, newLocalEnv, foreignModules, constructorMap)
            });
        } else {
          stmts.push({ kind: "GoReturnStmt", expr: lowerExpr(e, moduleExports, newLocalEnv, foreignModules, constructorMap) });
        }
      };
      flattenLet(expr);

      return {
        kind: "GoCallExpr",
        fn: {
          kind: "GoFuncLit",
          type: { kind: "GoFuncType", params: [], results: [lowerType(expr.type)] },
          body: stmts
        },
        args: []
      };
    }
    case "Match": {
      // Check if this is a tuple destructuring pattern (e.g. let (a, b) = expr)
      const isTupleMatch = expr.cases.length === 1 &&
          expr.cases[0].pattern.kind === "ConstructorPattern" &&
          expr.cases[0].pattern.name.startsWith("Tuple");

      if (isTupleMatch) {
          // Tuple destructuring: extract .V0, .V1, etc. directly
          const c = expr.cases[0];
          const pat = c.pattern as CoreIR.ConstructorPattern;
          const stmts: GoIR.GoStmt[] = [];
          const newLocalEnv = new Map(localEnv || []);
          const subjExpr = lowerExpr(expr.expr, moduleExports, localEnv, foreignModules, constructorMap);

          // Type-assert the subject to the tuple type
          const tupleArity = pat.args.length;
          const assertedSubj: GoIR.GoExpr = {
              kind: "GoTypeAssertExpr",
              expr: subjExpr,
              type: { kind: "GoSelectorType", pkg: "sky_wrappers", name: "Tuple" + tupleArity }
          } as any;

          // Assign to a temp variable to avoid repeated evaluation
          const tmpName = "__tuple" + Math.floor(Math.random() * 10000);
          stmts.push({
              kind: "GoAssignStmt",
              define: true,
              left: [{ kind: "GoIdent", name: tmpName }],
              right: assertedSubj
          });

          for (let j = 0; j < pat.args.length; j++) {
              const argPat = pat.args[j];
              if (argPat.kind === "VariablePattern" && argPat.name !== "_") {
                  newLocalEnv.set(argPat.name, { kind: "TypeConstant", name: "Any" });
                  stmts.push({
                      kind: "GoAssignStmt",
                      define: true,
                      left: [{ kind: "GoIdent", name: sanitizeGoIdent(argPat.name) }],
                      right: { kind: "GoSelectorExpr", expr: { kind: "GoIdent", name: tmpName }, sel: "V" + j }
                  });
              }
          }
          stmts.push({ kind: "GoReturnStmt", expr: lowerExpr(c.body, moduleExports, newLocalEnv, foreignModules, constructorMap) });

          return {
              kind: "GoCallExpr",
              fn: {
                  kind: "GoFuncLit",
                  type: { kind: "GoFuncType", params: [], results: [lowerType(expr.type)] },
                  body: stmts
              },
              args: []
          };
      }

      // ConsPattern match: case list of x :: xs -> ...
      const hasConsPattern = expr.cases.some(c => c.pattern.kind === "ConsPattern");
      if (hasConsPattern) {
          const stmts: GoIR.GoStmt[] = [];
          const newLocalEnv = new Map(localEnv || []);
          const subjExpr = lowerExpr(expr.expr, moduleExports, localEnv, foreignModules, constructorMap);

          // Type-assert subject to []any
          const tmpName = "__list" + Math.floor(Math.random() * 10000);
          stmts.push({
              kind: "GoAssignStmt",
              define: true,
              left: [{ kind: "GoIdent", name: tmpName }],
              right: {
                  kind: "GoTypeAssertExpr",
                  expr: subjExpr,
                  type: { kind: "GoSliceType", elem: { kind: "GoIdentType", name: "any" } }
              } as any
          });

          // Build if-else chain for cons vs other patterns
          const buildConsChain = (caseIdx: number): GoIR.GoStmt[] => {
              if (caseIdx >= expr.cases.length) {
                  return [{ kind: "GoExprStmt", expr: { kind: "GoCallExpr", fn: { kind: "GoIdent", name: "panic" }, args: [{ kind: "GoBasicLit", value: '"unmatched case"' }] } }];
              }
              const c = expr.cases[caseIdx];
              if (c.pattern.kind === "ConsPattern") {
                  const branchEnv = new Map(newLocalEnv);
                  const branchStmts: GoIR.GoStmt[] = [];

                  if (c.pattern.head.kind === "VariablePattern" && c.pattern.head.name !== "_") {
                      branchEnv.set(c.pattern.head.name, { kind: "TypeConstant", name: "Any" });
                      branchStmts.push({
                          kind: "GoAssignStmt",
                          define: true,
                          left: [{ kind: "GoIdent", name: sanitizeGoIdent(c.pattern.head.name) }],
                          right: { kind: "GoIndexExpr", expr: { kind: "GoIdent", name: tmpName }, index: { kind: "GoBasicLit", value: "0" } } as any
                      });
                  }
                  if (c.pattern.tail.kind === "VariablePattern" && c.pattern.tail.name !== "_") {
                      branchEnv.set(c.pattern.tail.name, { kind: "TypeConstant", name: "Any" });
                      branchStmts.push({
                          kind: "GoAssignStmt",
                          define: true,
                          left: [{ kind: "GoIdent", name: sanitizeGoIdent(c.pattern.tail.name) }],
                          right: { kind: "GoSliceExpr", expr: { kind: "GoIdent", name: tmpName }, low: { kind: "GoBasicLit", value: "1" } } as any
                      });
                  }
                  branchStmts.push({ kind: "GoReturnStmt", expr: lowerExpr(c.body, moduleExports, branchEnv, foreignModules, constructorMap) });

                  return [{
                      kind: "GoIfStmt",
                      condition: {
                          kind: "GoBinaryExpr",
                          left: { kind: "GoCallExpr", fn: { kind: "GoIdent", name: "len" }, args: [{ kind: "GoIdent", name: tmpName }] },
                          op: ">",
                          right: { kind: "GoBasicLit", value: "0" }
                      } as any,
                      thenBranch: branchStmts,
                      elseBranch: buildConsChain(caseIdx + 1)
                  }];
              } else if (c.pattern.kind === "LiteralPattern" && (c.pattern as any).value === "__empty_list__") {
                  // Empty list pattern: check len == 0
                  const branchStmts: GoIR.GoStmt[] = [];
                  branchStmts.push({ kind: "GoReturnStmt", expr: lowerExpr(c.body, moduleExports, new Map(newLocalEnv), foreignModules, constructorMap) });
                  return [{
                      kind: "GoIfStmt",
                      condition: {
                          kind: "GoBinaryExpr",
                          left: { kind: "GoCallExpr", fn: { kind: "GoIdent", name: "len" }, args: [{ kind: "GoIdent", name: tmpName }] },
                          op: "==",
                          right: { kind: "GoBasicLit", value: "0" }
                      } as any,
                      thenBranch: branchStmts,
                      elseBranch: buildConsChain(caseIdx + 1)
                  }];
              } else {
                  // Wildcard or variable fallback
                  const branchEnv = new Map(newLocalEnv);
                  const branchStmts: GoIR.GoStmt[] = [];
                  if (c.pattern.kind === "VariablePattern" && c.pattern.name !== "_") {
                      branchEnv.set(c.pattern.name, expr.expr.type);
                      branchStmts.push({
                          kind: "GoAssignStmt",
                          define: true,
                          left: [{ kind: "GoIdent", name: sanitizeGoIdent(c.pattern.name) }],
                          right: { kind: "GoIdent", name: tmpName }
                      });
                  }
                  branchStmts.push({ kind: "GoReturnStmt", expr: lowerExpr(c.body, moduleExports, branchEnv, foreignModules, constructorMap) });
                  return branchStmts;
              }
          };

          stmts.push(...buildConsChain(0));

          return {
              kind: "GoCallExpr",
              fn: {
                  kind: "GoFuncLit",
                  type: { kind: "GoFuncType", params: [], results: [lowerType(expr.type)] },
                  body: stmts
              },
              args: []
          };
      }

      // AsPattern match: handle as-patterns inside case branches
      const hasAsPattern = expr.cases.some(c => c.pattern.kind === "AsPattern");
      if (hasAsPattern) {
          // Rewrite AsPattern cases: bind name = subject, then delegate to inner pattern
          const rewrittenCases = expr.cases.map(c => {
              if (c.pattern.kind === "AsPattern") {
                  // Wrap body in a let binding for the as-name
                  const wrappedBody: CoreIR.Expr = {
                      kind: "LetBinding",
                      name: c.pattern.name,
                      value: expr.expr,
                      body: c.body,
                      type: c.body.type
                  };
                  return { pattern: c.pattern.pattern, body: wrappedBody };
              }
              return c;
          });
          const rewrittenMatch: CoreIR.Match = {
              kind: "Match",
              expr: expr.expr,
              cases: rewrittenCases,
              type: expr.type
          };
          return lowerExpr(rewrittenMatch, moduleExports, localEnv, foreignModules, constructorMap);
      }

      // Check if this is a literal-value case (string/int/float patterns) vs ADT constructor case
      const hasLiteralPatterns = expr.cases.some(c => c.pattern.kind === "LiteralPattern");

      if (hasLiteralPatterns) {
          // Literal value switch: case x of "add" -> ... "list" -> ... _ -> ...
          const switchSubj = lowerExpr(expr.expr, moduleExports, localEnv, foreignModules, constructorMap);
          const litCases: GoIR.GoCaseClause[] = expr.cases.map(c => {
              const stmts: GoIR.GoStmt[] = [];
              const newLocalEnv = new Map(localEnv || []);

              if (c.pattern.kind === "LiteralPattern") {
                  let caseValue: string;
                  if (typeof c.pattern.value === "string") {
                      caseValue = JSON.stringify(c.pattern.value);
                  } else {
                      caseValue = String(c.pattern.value);
                  }
                  stmts.push({ kind: "GoReturnStmt", expr: lowerExpr(c.body, moduleExports, newLocalEnv, foreignModules, constructorMap) });
                  return { kind: "GoCaseClause" as const, exprs: [{ kind: "GoBasicLit" as const, value: caseValue }], body: stmts };
              }

              // Wildcard or variable fallback → default case
              if (c.pattern.kind === "VariablePattern" && c.pattern.name !== "_") {
                  newLocalEnv.set(c.pattern.name, expr.expr.type);
                  stmts.push({
                      kind: "GoAssignStmt",
                      define: true,
                      left: [{ kind: "GoIdent", name: sanitizeGoIdent(c.pattern.name) }],
                      right: lowerExpr(expr.expr, moduleExports, newLocalEnv, foreignModules, constructorMap)
                  });
              }
              stmts.push({ kind: "GoReturnStmt", expr: lowerExpr(c.body, moduleExports, newLocalEnv, foreignModules, constructorMap) });
              return { kind: "GoCaseClause" as const, exprs: [] as GoIR.GoExpr[], body: stmts };
          });

          return {
              kind: "GoCallExpr",
              fn: {
                  kind: "GoFuncLit",
                  type: { kind: "GoFuncType", params: [], results: [lowerType(expr.type)] },
                  body: [
                      { kind: "GoSwitchStmt", expr: switchSubj, cases: litCases },
                      { kind: "GoExprStmt", expr: { kind: "GoCallExpr", fn: { kind: "GoIdent", name: "panic" }, args: [{ kind: "GoBasicLit", value: '"unmatched case"' }] } }
                  ]
              },
              args: []
          };
      }

      // ADT pattern matching
      // Determine the ADT type name from the first constructor pattern
      let adtTypeName: string | undefined;
      for (const c of expr.cases) {
          if (c.pattern.kind === "ConstructorPattern") {
              const info = constructorMap?.get(c.pattern.name);
              if (info) {
                  adtTypeName = info.adtName;
                  break;
              }
          }
      }

      // Fallback: detect well-known constructors not in the local constructorMap
      // (e.g., Ok/Err from Prelude's Result, Just/Nothing from Maybe)
      if (!adtTypeName) {
          const ctorNames = expr.cases
              .filter(c => c.pattern.kind === "ConstructorPattern")
              .map(c => (c.pattern as any).name);
          if (ctorNames.includes("Ok") || ctorNames.includes("Err")) {
              adtTypeName = "sky_wrappers.SkyResult";
          } else if (ctorNames.includes("Just") || ctorNames.includes("Nothing")) {
              // Maybe type: both Just and Nothing use struct{ Tag int; JustValue any }
              // so the type assertion is consistent for the switch statement.
              adtTypeName = "struct{ Tag int; JustValue any }";
          }
      }

      // Use well-known tag indices for Prelude types
      const wellKnownTags: Record<string, Record<string, number>> = {
          "Ok": { tag: 0 }, "Err": { tag: 1 },
          "Just": { tag: 0 }, "Nothing": { tag: 1 },
      };
      // Well-known field names for Prelude types
      const wellKnownFields: Record<string, string> = {
          "Ok": "OkValue", "Err": "ErrValue",
          "Just": "JustValue",
      };

      // Bind subject to a temp variable to avoid re-evaluation
      const subjTempName = `__match_${Math.floor(Math.random() * 100000)}`;
      const subjExpr = lowerExpr(expr.expr, moduleExports, localEnv, foreignModules, constructorMap);
      const subjRef: GoIR.GoExpr = { kind: "GoIdent", name: subjTempName };

      const cases: GoIR.GoCaseClause[] = expr.cases.map((c, i) => {
        const stmts: GoIR.GoStmt[] = [];
        const newLocalEnv = new Map(localEnv || []);

        if (c.pattern.kind === "ConstructorPattern") {
           const ctorInfo = constructorMap?.get(c.pattern.name);
           const tagIndex = ctorInfo ? ctorInfo.tagIndex : (wellKnownTags[c.pattern.name]?.tag ?? i);

           // Extract variables from the ADT struct fields
           for (let j = 0; j < c.pattern.args.length; j++) {
              const argPat = c.pattern.args[j];
              if (argPat.kind === "VariablePattern" && argPat.name !== "_") {
                  newLocalEnv.set(argPat.name, { kind: "TypeConstant", name: "Any" });
                  let subj: GoIR.GoExpr = subjRef;
                  const fieldName = wellKnownFields[c.pattern.name] || (c.pattern.name + "Value" + (j > 0 ? j : ""));
                  // For Maybe's Just branch, use the full struct type that includes JustValue
                  const fieldAssertType = (adtTypeName === "struct{ Tag int }" && fieldName === "JustValue")
                      ? "struct{ Tag int; JustValue any }"
                      : adtTypeName;
                  if (fieldAssertType) {
                      subj = { kind: "GoTypeAssertExpr", expr: subj, type: { kind: "GoIdentType", name: fieldAssertType } } as any;
                  }
                  stmts.push({
                      kind: "GoAssignStmt",
                      define: true,
                      left: [{ kind: "GoIdent", name: sanitizeGoIdent(argPat.name) }],
                      right: { kind: "GoSelectorExpr", expr: subj, sel: fieldName }
                  });
              }
           }
           stmts.push({ kind: "GoReturnStmt", expr: lowerExpr(c.body, moduleExports, newLocalEnv, foreignModules, constructorMap) });

           return {
               kind: "GoCaseClause",
               exprs: [{ kind: "GoBasicLit", value: String(tagIndex) }],
               body: stmts
           };
        }

        // Fallback catch-all
        if (c.pattern.kind === "WildcardPattern" || c.pattern.kind === "VariablePattern") {
           if (c.pattern.kind === "VariablePattern" && c.pattern.name !== "_") {
               newLocalEnv.set(c.pattern.name, expr.expr.type);
               stmts.push({
                   kind: "GoAssignStmt",
                   define: true,
                   left: [{ kind: "GoIdent", name: sanitizeGoIdent(c.pattern.name) }],
                   right: subjRef
               });
           }
           stmts.push({ kind: "GoReturnStmt", expr: lowerExpr(c.body, moduleExports, newLocalEnv, foreignModules, constructorMap) });
           return { kind: "GoCaseClause", exprs: [], body: stmts };
        }

        return { kind: "GoCaseClause", exprs: [], body: [{ kind: "GoReturnStmt", expr: lowerExpr(c.body, moduleExports, newLocalEnv, foreignModules, constructorMap) }] };
      });

      // Build the switch — bind subject to a temp var, then switch on .Tag
      const bodyStmts: GoIR.GoStmt[] = [];

      if (adtTypeName) {
          // For well-known types (SkyResult), use: var __match any = <expr>
          // Then assert to the concrete type for .Tag/.OkValue access.
          // "var x any = expr" ensures x is interface-typed so assertion always works.
          bodyStmts.push({
              kind: "GoExprStmt",
              expr: { kind: "GoRawExpr", code: `var ${subjTempName} any` } as any
          });
          bodyStmts.push({
              kind: "GoAssignStmt",
              define: false,
              left: [{ kind: "GoIdent", name: subjTempName }],
              right: subjExpr
          } as any);

          const assertedSubj: GoIR.GoExpr = {
              kind: "GoTypeAssertExpr",
              expr: subjRef,
              type: { kind: "GoIdentType", name: adtTypeName }
          } as any;

          bodyStmts.push({
              kind: "GoSwitchStmt",
              expr: { kind: "GoSelectorExpr", expr: assertedSubj, sel: "Tag" },
              cases: cases
          });
      } else {
          bodyStmts.push({
              kind: "GoAssignStmt",
              define: true,
              left: [{ kind: "GoIdent", name: subjTempName }],
              right: subjExpr
          } as any);
          bodyStmts.push({
              kind: "GoSwitchStmt",
              expr: { kind: "GoSelectorExpr", expr: subjRef, sel: "Tag" },
              cases: cases
          });
      }

      bodyStmts.push({
          kind: "GoExprStmt",
          expr: { kind: "GoCallExpr", fn: { kind: "GoIdent", name: "panic" }, args: [{ kind: "GoBasicLit", value: '"unmatched case"' }] }
      });

      return {
        kind: "GoCallExpr",
        fn: {
          kind: "GoFuncLit",
          type: { kind: "GoFuncType", params: [], results: [lowerType(expr.type)] },
          body: bodyStmts
        },
        args: []
      };
    }
    case "Constructor": {
        const ctorInfo = constructorMap?.get(expr.name);
        if (ctorInfo) {
            // Known ADT constructor: emit ParentType{Tag: tagIndex, CtorValueN: arg}
            const argExprs = expr.args.map(a => lowerExpr(a, moduleExports, localEnv, foreignModules, constructorMap));
            const kvPairs: string[] = [`Tag: ${ctorInfo.tagIndex}`];
            for (let j = 0; j < expr.args.length; j++) {
                const fieldName = expr.name + "Value" + (j > 0 ? j : "");
                kvPairs.push(`${fieldName}: ${emitGoExprForLower(argExprs[j])}`);
            }
            return {
                kind: "GoRawExpr",
                code: `${ctorInfo.adtName}{${kvPairs.join(", ")}}`
            } as any;
        }
        // Well-known Prelude constructors: Ok, Err, Just, Nothing
        // These are defined in Sky.Core.Prelude/Maybe and may not be in the
        // current module's constructorMap.  Emit proper Go runtime values.
        const wellKnownCtors: Record<string, { tag: number; wrapper: string; field: string }> = {
            "Ok":      { tag: 0, wrapper: "sky_wrappers.SkyOk",  field: "OkValue" },
            "Err":     { tag: 1, wrapper: "sky_wrappers.SkyErr",  field: "ErrValue" },
            "Just":    { tag: 0, wrapper: "",                     field: "JustValue" },
            "Nothing": { tag: 1, wrapper: "",                     field: "" },
        };
        const wk = wellKnownCtors[expr.name];
        if (wk) {
            const argExprs = expr.args.map(a => lowerExpr(a, moduleExports, localEnv, foreignModules, constructorMap));
            if (wk.wrapper) {
                // Result Ok/Err — use helper functions SkyOk / SkyErr
                return {
                    kind: "GoCallExpr",
                    fn: { kind: "GoIdent", name: wk.wrapper },
                    args: argExprs.length > 0 ? argExprs : [{ kind: "GoIdent", name: "nil" }]
                } as any;
            }
            // Maybe Just/Nothing — use anonymous struct
            if (argExprs.length > 0) {
                // Just value
                return {
                    kind: "GoRawExpr",
                    code: `struct{ Tag int; ${wk.field} any }{Tag: ${wk.tag}, ${wk.field}: ${emitGoExprForLower(argExprs[0])}}`
                } as any;
            }
            // Nothing (no args) — include JustValue: nil so the struct type
            // matches Just's struct{ Tag int; JustValue any } for consistent matching.
            if (expr.name === "Nothing") {
                return {
                    kind: "GoRawExpr",
                    code: `struct{ Tag int; JustValue any }{Tag: ${wk.tag}, JustValue: nil}`
                } as any;
            }
            return {
                kind: "GoRawExpr",
                code: `struct{ Tag int }{Tag: ${wk.tag}}`
            } as any;
        }
        // Special interop types: Foreign, JsValue map to nil in Go
        if (expr.name === "Foreign" || expr.name === "JsValue") {
            return { kind: "GoIdent", name: "nil" } as any;
        }
        // Fallback: unknown constructor — may be from an imported module
        const goName = expr.name.charAt(0).toUpperCase() + expr.name.slice(1);
        // Check if this constructor comes from an imported module
        let qualifiedGoName = goName;
        if (moduleExports) {
            for (const [modName, exports] of moduleExports) {
                if (exports.has(expr.name)) {
                    const parts = modName.split(".");
                    const pkgName = makeSafeGoPkgName(parts[parts.length - 1], modName);
                    qualifiedGoName = `${pkgName}.${goName}`;
                    break;
                }
            }
        }
        const argExprs2 = expr.args.map(a => lowerExpr(a, moduleExports, localEnv, foreignModules, constructorMap));
        const kvPairs2: string[] = ["Tag: 0"];
        for (let j = 0; j < argExprs2.length; j++) {
            const fieldName = expr.name + "Value" + (j > 0 ? j : "");
            kvPairs2.push(`${fieldName}: ${emitGoExprForLower(argExprs2[j])}`);
        }
        return {
            kind: "GoRawExpr",
            code: `${qualifiedGoName}{${kvPairs2.join(", ")}}`
        } as any;
    }
    case "RecordExpr": {
      // Map records to map[string]any for flexibility in UpdateRecord
      const keys = Object.keys(expr.fields);
      return {
          kind: "GoMapLit",
          type: { kind: "GoMapType", key: { kind: "GoIdentType", name: "string" }, value: { kind: "GoIdentType", name: "any" } },
          entries: keys.map(k => ({
              key: { kind: "GoBasicLit", value: '"' + k + '"' },
              value: lowerExpr(expr.fields[k], moduleExports, localEnv, foreignModules, constructorMap)
          }))
      } as any;
    }
    case "ListExpr": {
        // Always use []any for Sky lists — all runtime functions expect []any
        return {
            kind: "GoSliceLit",
            type: { kind: "GoSliceType", elem: { kind: "GoIdentType", name: "any" } },
            elements: expr.items.map(i => lowerExpr(i, moduleExports, localEnv, foreignModules, constructorMap))
        };
    }
    default:
      return { kind: "GoBasicLit", value: "/* unimplemented */" };
  }
}

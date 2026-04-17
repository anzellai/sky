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
            let typeName = "struct {  }";
            if (expr.type) {
                if (expr.type.kind === "GoSelectorType" && expr.type.pkg) {
                    typeName = `${expr.type.pkg}.${expr.type.name}`;
                } else {
                    typeName = expr.type.name || "struct {  }";
                }
            }
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
            const inner = emitGoExprForLower(expr.expr);
            // Use safe helpers for basic types and well-known ADTs to prevent panics
            const safeMap: Record<string, string> = {
                "map[string]any": "sky_wrappers.Sky_AsMap",
                "[]any": "sky_wrappers.Sky_AsList",
                "int": "sky_wrappers.Sky_AsInt",
                "string": "sky_wrappers.Sky_AsString",
                "bool": "sky_wrappers.Sky_AsBool",
                "float64": "sky_wrappers.Sky_AsFloat",
                "sky_wrappers.SkyResult": "sky_wrappers.Sky_AsSkyResult",
                "sky_wrappers.SkyMaybe": "sky_wrappers.Sky_AsSkyMaybe",
                "sky_wrappers.Tuple2": "sky_wrappers.Sky_AsTuple2",
                "sky_wrappers.Tuple3": "sky_wrappers.Sky_AsTuple3",
            };
            // Also handle unqualified names (used within sky_wrappers package)
            const safeMapUnqualified: Record<string, string> = {
                "SkyResult": "sky_wrappers.Sky_AsSkyResult",
                "SkyMaybe": "sky_wrappers.Sky_AsSkyMaybe",
                "Tuple2": "sky_wrappers.Sky_AsTuple2",
                "Tuple3": "sky_wrappers.Sky_AsTuple3",
            };
            const safeHelper = safeMap[typeStr] || safeMapUnqualified[typeStr];
            if (safeHelper) {
                return `${safeHelper}(${inner})`;
            }
            return `${inner}.(${typeStr})`;
        }
        case "GoFuncLit": {
            const params = (expr.type?.params || []).map((p: any, i: number) => `arg${i} ${p.name || "any"}`).join(", ");
            const retType = expr.type?.results?.[0] ? (expr.type.results[0].name || "any") : "any";
            let body = "";
            for (const stmt of (expr.body || [])) {
                if (stmt.kind === "GoAssignStmt") {
                    const allBlank = stmt.left.every((l: any) => l.kind === "GoIdent" && l.name === "_");
                    const op = (stmt.define && !allBlank) ? ":=" : "=";
                    const leftNames = stmt.left.map((l: any) => emitGoExprForLower(l));
                    body += `${leftNames.join(", ")} ${op} ${emitGoExprForLower(stmt.right)}; `;
                    // Suppress "declared and not used" for case-bound variables
                    if (stmt.define && !allBlank) {
                        for (const n of leftNames) {
                            if (n !== "_") body += `_ = ${n}; `;
                        }
                    }
                } else if (stmt.kind === "GoReturnStmt") {
                    body += `return ${emitGoExprForLower(stmt.expr)}`;
                } else if (stmt.kind === "GoExprStmt") {
                    body += `${emitGoExprForLower(stmt.expr)}; `;
                }
            }
            return `func(${params}) ${retType} { ${body} }`;
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

// Symbols collected during lowering — accumulated across all modules by the compiler.
// Contains all Sky_* wrapper symbols actually referenced by lowered Go code.
let _collectedWrapperSymbols: Set<string> | null = null;

export function setWrapperSymbolCollector(collector: Set<string>) {
    _collectedWrapperSymbols = collector;
}

export function lowerModule(module: CoreIR.Module, moduleExports?: Map<string, Map<string, Scheme>>, foreignModules?: Set<string>, importedModules?: Set<string>, importedConstructors?: Map<string, { adtName: string; tagIndex: number; arity: number }>): GoIR.GoPackage {
  const pkg: GoIR.GoPackage = {
    name: makeSafeGoPkgName(module.name[module.name.length - 1], module.name.join(".")),
    imports: [],
    declarations: []
  };

  // Set imported constructor tags for cross-module case matching
  _importedCtorTags = importedConstructors || new Map();

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

  // Well-known ADT types that live in sky_wrappers — skip type/ctor generation
  const wellKnownAdtCtors = new Set(["Ok", "Err", "Just", "Nothing"]);
  const isWellKnownAdt = (tDecl: CoreIR.TypeDeclaration) =>
      tDecl.constructors.some(c => wellKnownAdtCtors.has(c.name));

  // Skip ALL ADT type declarations — custom ADTs use map[string]any at runtime.
  // Well-known types (Maybe/Result) live in sky_wrappers as named structs.
  // Record aliases are also maps (handled separately above).

  // Generate constructor functions for ADT variants (for cross-module use)
  // Skip record aliases, single-constructor types, and well-known ADTs
  for (const tDecl of module.typeDeclarations) {
    if (recordAliasTypes.has(tDecl.name)) continue;
    if (tDecl.constructors.length === 1 && tDecl.constructors[0].name === tDecl.name) continue;
    if (isWellKnownAdt(tDecl)) continue;
    for (let i = 0; i < tDecl.constructors.length; i++) {
      const c = tDecl.constructors[i];
      const kvPairs: string[] = [`"Tag": ${i}`, `"SkyName": "${c.name}"`];
      const goParams: string[] = [];
      for (let j = 0; j < c.types.length; j++) {
        const fieldName = `V${j}`;
        const paramName = `arg${j}`;
        goParams.push(`${paramName} any`);
        kvPairs.push(`"${fieldName}": ${paramName}`);
      }
      const goFnName = c.name.charAt(0).toUpperCase() + c.name.slice(1);
      const body = `map[string]any{${kvPairs.join(", ")}}`;
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
      // Detect sky_wrappers references in GoIdent nodes (e.g., sky_wrappers.SkyNothing)
      if (node.kind === "GoIdent" && typeof node.name === "string" && node.name.startsWith("sky_wrappers.")) {
          foreignModulesDetected.add("sky_wrappers");
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
// Constructor tag info from imported modules (for cross-module case matching)
let _importedCtorTags: Map<string, { adtName: string; tagIndex: number; arity: number }> = new Map();

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
          let fullPkgName = pkgName;
          
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
              fullPkgName = pkgName;
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
          const isForeignModule = foreignModules && (foreignModules.has(pkgName) || foreignModules.has(fullPkgName));
          if (moduleExports && moduleExports.has(pkgName) && !ffiWrappers.has(pkgName) && !isForeignModule) {
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
              if (_collectedWrapperSymbols) _collectedWrapperSymbols.add(wrapperName);
              return { kind: "GoSelectorExpr", expr: { kind: "GoIdent", name: "sky_wrappers" }, sel: wrapperName };
          }

          // For foreign Go FFI modules, reference the wrapper directly.
          // Constants/variables are zero-arg Go wrapper functions. When the
          // identifier is NOT a call target (i.e., used as a value, not being called),
          // check if its exported type is non-function and auto-call it.
          const wrapperName2 = "Sky_" + safePkg + "_" + goName;
          if (_collectedWrapperSymbols) _collectedWrapperSymbols.add(wrapperName2);
          const wrapperExpr2: GoIR.GoExpr = { kind: "GoSelectorExpr", expr: { kind: "GoIdent", name: "sky_wrappers" }, sel: wrapperName2 };
          // Auto-call constants/variables (non-function types) in FFI modules.
          // These compile to zero-arg Go wrappers: Sky_pkg_Const()
          if (!_isCallTarget && moduleExports) {
              for (const [, exports] of moduleExports) {
                  const scheme = exports.get(name);
                  if (scheme) {
                      if (scheme.type && scheme.type.kind !== "TypeFunction") {
                          return { kind: "GoCallExpr", fn: wrapperExpr2, args: [] } as any;
                      }
                      break;
                  }
              }
          }
          return wrapperExpr2;
      }
      
      if (expr.name === "Sprintf") {
          return { kind: "GoSelectorExpr", expr: { kind: "GoIdent", name: "fmt" }, sel: "Sprintf" };
      }
      if (expr.name === "stringToBytes") {
          return { kind: "GoIdent", name: "[]byte" };
      }
      if (expr.name === "bytesToString") {
          return { kind: "GoIdent", name: "string" };
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
          return { kind: "GoRawExpr", code: `func(arg0 any) any { if sky_wrappers.Sky_AsBool(arg0) { return false }; return true }` } as any;
      }
      // Prelude: fst/snd → inline tuple accessors (tuples are sky_wrappers.Tuple2 structs)
      if (expr.name === "fst") {
          return { kind: "GoRawExpr", code: `func(arg0 any) any { return sky_wrappers.Sky_AsTuple2(arg0).V0 }` } as any;
      }
      if (expr.name === "snd") {
          return { kind: "GoRawExpr", code: `func(arg0 any) any { return sky_wrappers.Sky_AsTuple2(arg0).V1 }` } as any;
      }
      // Prelude: identity → inline pass-through
      if (expr.name === "identity") {
          return { kind: "GoRawExpr", code: `func(arg0 any) any { return arg0 }` } as any;
      }
      // Prelude: always → inline constant function
      if (expr.name === "always") {
          return { kind: "GoRawExpr", code: `func(arg0 any) any { return func(_ any) any { return arg0 } }` } as any;
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
      // Prelude names that have special-case lowering above (inline Go code or wrappers):
      const preludeSpecialNames = new Set(["identity", "not", "fst", "snd", "always", "errorToString", "Ok", "Err", "Just", "Nothing"]);
      if (moduleExports && !_declParamCounts?.has(expr.name)) {
          const ffiWrapperModules = new Set(["Std.Log", "Std.Cmd", "Std.Task", "Std.Program"]);
          // When multiple modules export the same name, prefer the one actually imported
          let candidates: [string, Map<string, Scheme>][] = [];
          for (const [modName, exports] of moduleExports) {
              const skipModule = ffiWrapperModules.has(modName) || (modName === "Sky.Core.Prelude" && preludeSpecialNames.has(expr.name));
              if (exports.has(expr.name) && !skipModule && (!foreignModules || !foreignModules.has(modName))) {
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
          // Use safe bool assertion to handle both interface and concrete-typed FFI returns
          const inner = emitGoExprForLower(condExpr);
          condExpr = { kind: "GoRawExpr", code: `sky_wrappers.Sky_AsBool(${inner})` } as any;
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
          // Tuple constructors → sky_wrappers.Tuple2{V0: a, V1: b}
          if (flat.fn.name.startsWith("Tuple") && /^Tuple\d+$/.test(flat.fn.name)) {
              const argExprs = flat.args.map(a => lowerExpr(a, moduleExports, localEnv, foreignModules, constructorMap));
              return {
                  kind: "GoCompositeLit",
                  type: { kind: "GoSelectorType", pkg: "sky_wrappers", name: flat.fn.name },
                  elements: argExprs
              } as any;
          }
          const wellKnownAppCtors: Record<string, { wrapper: string; tag: number; field: string }> = {
              "Ok":   { wrapper: "sky_wrappers.SkyOk",  tag: 0, field: "OkValue" },
              "Err":  { wrapper: "sky_wrappers.SkyErr",  tag: 1, field: "ErrValue" },
              "Just": { wrapper: "sky_wrappers.SkyJust",  tag: 0, field: "JustValue" },
          };
          const wk = wellKnownAppCtors[flat.fn.name];
          if (wk) {
              const argExprs = flat.args.map(a => lowerExpr(a, moduleExports, localEnv, foreignModules, constructorMap));
              // Result Ok/Err, Maybe Just — use wrapper functions
              return {
                  kind: "GoCallExpr",
                  fn: { kind: "GoIdent", name: wk.wrapper },
                  args: argExprs
              } as any;
          }
          // Check local constructorMap for ADT constructor applications
          const localCtorInfo = constructorMap?.get(flat.fn.name);
          if (localCtorInfo) {
              const argExprs = flat.args.map(a => lowerExpr(a, moduleExports, localEnv, foreignModules, constructorMap));
              // Use map[string]any for custom ADT constructors
              const kvPairs: string[] = [`"Tag": ${localCtorInfo.tagIndex}`, `"SkyName": "${flat.fn.name}"`];
              for (let j = 0; j < flat.args.length; j++) {
                  const fieldName = `V${j}`;
                  kvPairs.push(`"${fieldName}": ${emitGoExprForLower(argExprs[j])}`);
              }
              return {
                  kind: "GoRawExpr",
                  code: `map[string]any{${kvPairs.join(", ")}}`
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
          // Only add byte-slice conversions when calling Go stdlib functions directly,
          // NOT when calling sky_wrappers — those wrappers handle type assertions internally.
          const isForeign = (fnExpr.kind === "GoSelectorExpr" &&
                           fnExpr.expr.kind === "GoIdent" &&
                           fnExpr.expr.name !== "sky_wrappers" &&
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
          // Safely assert container to map[string]any for record field access
          const assertedContainer: GoIR.GoExpr = {
              kind: "GoRawExpr",
              code: `sky_wrappers.Sky_AsMap(${emitGoExprForLower(container)})`
          } as any;
          return {
              kind: "GoIndexExpr",
              expr: assertedContainer,
              index: { kind: "GoBasicLit", value: '"' + fieldName + '"' }
          } as any;
      }

      // Handle Go FFI zero-arg calls (which take unit () in Sky)
      // Strip the unit argument when calling sky_wrappers functions — the Go wrapper takes 0 args.
      // This covers both direct calls like `wrapper ()` (Literal Unit) and forwarded calls
      // like `skyName arg0 = wrapper arg0` where arg0 is a unit-typed variable.
      if (args.length === 1) {
          const coreArg0 = flat.args[0] as any;
          const isUnitLiteral = coreArg0.kind === "Literal" && coreArg0.literalType === "Unit";
          const isUnitTypedVar = coreArg0.kind === "Variable" && coreArg0.type?.kind === "TypeConstant" && coreArg0.type?.name === "Unit";
          if (isUnitLiteral || isUnitTypedVar) {
              const isWrappers = (fnExpr.kind === "GoSelectorExpr" && fnExpr.expr.kind === "GoIdent" && fnExpr.expr.name === "sky_wrappers");
              if (isWrappers) {
                  args = [];
              }
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
                      kind: "GoRawExpr",
                      code: `sky_wrappers.Sky_AsString(${emitGoExprForLower(a)})`
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
                return { kind: "GoRawExpr", code: `sky_wrappers.Sky_AsString(arg${i})` } as any;
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
      } else if (fnExpr.kind === "GoIdent" && fnExpr.name === "::" && args.length === 2) {
          // Cons expression: head :: tail → append([]any{head}, tail...)
          const h = emitGoExprForLower(args[0]);
          const t = emitGoExprForLower(args[1]);
          return {
              kind: "GoRawExpr",
              code: `append([]any{${h}}, sky_wrappers.Sky_AsList(${t})...)`
          } as any;
      } else if (fnExpr.kind === "GoIdent" && (["+", "-", "*", "/", "//", "%", "++", "==", "!=", "/=", "<", ">", "<=", ">=", "&&", "||"].includes(fnExpr.name))) {
          // ++ operator: handles both string concatenation and list append.
          // Use compile-time type info when available, otherwise emit runtime dispatch.
          if (fnExpr.name === "++") {
              const l = emitGoExprForLower(args[0]);
              const r = emitGoExprForLower(args[1]);
              const isListType = expr.type && expr.type.kind === "TypeApplication" &&
                  expr.type.constructor.kind === "TypeConstant" && expr.type.constructor.name === "List";
              if (isListType) {
                  return {
                      kind: "GoRawExpr",
                      code: `append(sky_wrappers.Sky_AsList(${l}), sky_wrappers.Sky_AsList(${r})...)`
                  } as any;
              }
              // Runtime dispatch: check if operands are []any (list) or string
              return {
                  kind: "GoRawExpr",
                  code: `sky_wrappers.Sky_Append(${l}, ${r})`
              } as any;
          }

          // Binary operator uncurried
          const op = fnExpr.name === "/=" ? "!=" : fnExpr.name === "//" ? "/" : fnExpr.name;

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
              if (["+", "-", "*", "/", "//", "%"].includes(fnName0)) {
                  targetType = "int";
              } else if (["<", ">", "<=", ">="].includes(fnName0)) {
                  // Comparison — check if any arg is a string literal
                  const hasStringLit = flat.args.some(a2 => a2.kind === "Literal" && typeof (a2 as any).value === "string");
                  targetType = hasStringLit ? "string" : "int";
              } else if (fnName0 === "==" || fnName0 === "!=" || fnName0 === "/=") {
                  targetType = null; // equality works on any
              } else if (fnName0 === "&&" || fnName0 === "||") {
                  targetType = "bool";
              }

              if (!targetType) return a;

              // Use safe assertion helpers from sky_wrappers to prevent panics
              const safeHelperMap: Record<string, string> = {
                  "int": "Sky_AsInt",
                  "string": "Sky_AsString",
                  "bool": "Sky_AsBool",
                  "float64": "Sky_AsFloat",
              };
              const helper = safeHelperMap[targetType];
              if (helper) {
                  return {
                      kind: "GoRawExpr",
                      code: `sky_wrappers.${helper}(${emitGoExprForLower(a)})`
                  } as any;
              }
              // Fallback for unknown types (shouldn't happen for binary ops)
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
              // f(a, b, c) → sky_wrappers.Sky_AsFunc(sky_wrappers.Sky_AsFunc(sky_wrappers.Sky_AsFunc(f)(a))(b))(c)
              if (args.length > 1) {
                  const safeName = sanitizeGoIdent(fnExpr.name);
                  let code = `sky_wrappers.Sky_AsFunc(${safeName})(${emitGoExprForLower(args[0])})`;
                  for (let ci = 1; ci < args.length; ci++) {
                      code = `sky_wrappers.Sky_AsFunc(${code})(${emitGoExprForLower(args[ci])})`;
                  }
                  return { kind: "GoRawExpr", code } as any;
              }
              callFn = {
                  kind: "GoRawExpr",
                  code: `sky_wrappers.Sky_AsFunc(${sanitizeGoIdent(fnExpr.name)})`
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
                kind: "GoReturnStmt",
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

          // Safely assert subject to []any
          const tmpName = "__list" + Math.floor(Math.random() * 10000);
          stmts.push({
              kind: "GoAssignStmt",
              define: true,
              left: [{ kind: "GoIdent", name: tmpName }],
              right: {
                  kind: "GoRawExpr",
                  code: `sky_wrappers.Sky_AsList(${emitGoExprForLower(subjExpr)})`
              } as any
          });

          // Build if-else chain for cons vs other patterns
          const buildConsChain = (caseIdx: number): GoIR.GoStmt[] => {
              if (caseIdx >= expr.cases.length) {
                  return [{ kind: "GoExprStmt", expr: { kind: "GoRawExpr", code: `panic("non-exhaustive pattern match in list expression")` } as any }];
              }
              const c = expr.cases[caseIdx];
              if (c.pattern.kind === "ConsPattern") {
                  const branchEnv = new Map(newLocalEnv);
                  const branchStmts: GoIR.GoStmt[] = [];

                  // Base condition: len(list) > 0
                  let condition: any = {
                      kind: "GoBinaryExpr",
                      left: { kind: "GoCallExpr", fn: { kind: "GoIdent", name: "len" }, args: [{ kind: "GoIdent", name: tmpName }] },
                      op: ">",
                      right: { kind: "GoBasicLit", value: "0" }
                  };

                  if (c.pattern.head.kind === "LiteralPattern") {
                      // Literal head (e.g., "--flag" :: rest): add && list[0] == "literal"
                      const litValue = typeof c.pattern.head.value === "string"
                          ? JSON.stringify(c.pattern.head.value)
                          : String(c.pattern.head.value);
                      condition = {
                          kind: "GoBinaryExpr",
                          left: condition,
                          op: "&&",
                          right: {
                              kind: "GoBinaryExpr",
                              left: { kind: "GoIndexExpr", expr: { kind: "GoIdent", name: tmpName }, index: { kind: "GoBasicLit", value: "0" } },
                              op: "==",
                              right: { kind: "GoBasicLit", value: litValue }
                          }
                      };
                  } else if (c.pattern.head.kind === "VariablePattern" && c.pattern.head.name !== "_") {
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
                      condition: condition,
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
                      { kind: "GoExprStmt", expr: { kind: "GoRawExpr", code: `panic("non-exhaustive pattern match on literal value")` } as any }
                  ]
              },
              args: []
          };
      }

      // ADT pattern matching
      // Determine the ADT type name from the constructor patterns.
      // Strategy: check local constructorMap first, then _importedCtorTags
      // (which has accurate ADT info from the module graph), and only fall
      // back to moduleExports search as a last resort.
      let adtTypeName: string | undefined;

      // Collect all constructor names from this case expression
      const ctorPatternNames = expr.cases
          .filter((c: any) => c.pattern.kind === "ConstructorPattern")
          .map((c: any) => c.pattern.name as string);

      // 1. Well-known Prelude types — always use sky_wrappers runtime types.
      //    Must be checked FIRST, even before local constructorMap, because
      //    Ok/Err/Just/Nothing always use sky_wrappers types regardless of
      //    which module defines them.
      if (ctorPatternNames.includes("Ok") || ctorPatternNames.includes("Err")) {
          adtTypeName = "sky_wrappers.SkyResult";
      } else if (ctorPatternNames.includes("Just") || ctorPatternNames.includes("Nothing")) {
          adtTypeName = "sky_wrappers.SkyMaybe";
      }

      // 2. Check local constructorMap (for constructors defined in this module)
      if (!adtTypeName) {
          for (const name of ctorPatternNames) {
              const info = constructorMap?.get(name);
              if (info) {
                  adtTypeName = info.adtName;
                  break;
              }
          }
      }

      // 3. Check _importedCtorTags (has correct adtName from module graph)
      //    When multiple constructors match different ADTs (collision), use
      //    majority vote — the ADT that matches the most constructors wins.
      if (!adtTypeName) {
          const adtCounts = new Map<string, number>();
          for (const name of ctorPatternNames) {
              const info = _importedCtorTags.get(name);
              if (info) {
                  adtCounts.set(info.adtName, (adtCounts.get(info.adtName) || 0) + 1);
              }
          }
          let maxCount = 0;
          for (const [adt, count] of adtCounts) {
              if (count > maxCount) {
                  maxCount = count;
                  adtTypeName = adt;
              }
          }
      }

      // 4. Fallback: search moduleExports
      if (!adtTypeName && moduleExports) {
          // Search imported modules — cross-reference ALL constructors to find
          // the module that exports the most of them (avoids name collisions
          // where e.g. "Error" exists in both a Sky ADT and an FFI module).
          const ffiWrappers = new Set(["Std.Log", "Std.Cmd", "Std.Task", "Std.Program", "Sky.Core.Prelude"]);
          const candidates = new Map<string, { goPkg: string; typeName: string; count: number }>();

          for (const ctorName of ctorPatternNames) {
              for (const [modName, exports] of moduleExports) {
                  if (ffiWrappers.has(modName)) continue;
                  if (!exports.has(ctorName)) continue;
                  const scheme = exports.get(ctorName);
                  if (scheme?.type) {
                      let retType = scheme.type;
                      while (retType.kind === "TypeFunction") retType = retType.to;
                      if (retType.kind === "TypeConstant") {
                          const parts = modName.split(".");
                          const goPkg = makeSafeGoPkgName(parts[parts.length - 1], modName);
                          const typeName = retType.name.split(".").pop() || retType.name;
                          const key = `${goPkg}.${typeName}`;
                          const existing = candidates.get(key);
                          candidates.set(key, {
                              goPkg, typeName,
                              count: (existing?.count || 0) + 1
                          });
                      }
                  }
              }
          }

          // Pick the candidate that matches the most constructors
          let maxCount = 0;
          for (const [key, info] of candidates) {
              if (info.count > maxCount) {
                  maxCount = info.count;
                  adtTypeName = key;
              }
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

      // Helper: resolve tag index for a constructor pattern
      const resolveTagIndex = (patName: string, fallback: number): number => {
          let ctorInfo = constructorMap?.get(patName);
          if (!ctorInfo && _importedCtorTags.has(patName)) {
              ctorInfo = _importedCtorTags.get(patName);
          }
          return ctorInfo ? ctorInfo.tagIndex : (wellKnownTags[patName]?.tag ?? fallback);
      };

      // Helper: determine the inner ADT type for a nested constructor pattern
      const resolveInnerAdtType = (innerCtorName: string): string => {
          // Well-known types
          if (innerCtorName === "Ok" || innerCtorName === "Err") return "sky_wrappers.SkyResult";
          if (innerCtorName === "Just" || innerCtorName === "Nothing") return "sky_wrappers.SkyMaybe";
          // Check constructorMap and _importedCtorTags
          const info = constructorMap?.get(innerCtorName) || _importedCtorTags.get(innerCtorName);
          if (info) return info.adtName;
          return "any";
      };

      // Helper: determine if an ADT type is well-known (uses named structs, not maps)
      const isWellKnownAdtType = (typeName: string | undefined): boolean =>
          typeName === "sky_wrappers.SkyResult" || typeName === "sky_wrappers.SkyMaybe";

      // Helper: generate stmts for a simple (non-nested) constructor case
      const genSimpleCtorStmts = (
          pat: CoreIR.ConstructorPattern,
          body: CoreIR.Expr,
          env: Map<string, any>,
          subj: GoIR.GoExpr,
          outerAdtType: string | undefined
      ): GoIR.GoStmt[] => {
          const stmts: GoIR.GoStmt[] = [];
          for (let j = 0; j < pat.args.length; j++) {
              const argPat = pat.args[j];
              if (argPat.kind === "VariablePattern" && argPat.name !== "_") {
                  env.set(argPat.name, { kind: "TypeConstant", name: "Any" });
                  const fieldName = wellKnownFields[pat.name] || `V${j}`;
                  if (/^Tuple\d+$/.test(pat.name)) {
                      // Tuple types: use Sky_AsTuple struct access
                      const tupleArity = pat.args.length;
                      const assertFn = tupleArity <= 2 ? "sky_wrappers.Sky_AsTuple2" : "sky_wrappers.Sky_AsTuple3";
                      stmts.push({
                          kind: "GoAssignStmt",
                          define: true,
                          left: [{ kind: "GoIdent", name: sanitizeGoIdent(argPat.name) }],
                          right: { kind: "GoRawExpr", code: `${assertFn}(${emitGoExprForLower(subj)}).V${j}` } as any
                      });
                  } else if (isWellKnownAdtType(outerAdtType)) {
                      // Well-known types: struct-based field access
                      let s: GoIR.GoExpr = subj;
                      s = { kind: "GoTypeAssertExpr", expr: s, type: { kind: "GoIdentType", name: outerAdtType! } } as any;
                      stmts.push({
                          kind: "GoAssignStmt",
                          define: true,
                          left: [{ kind: "GoIdent", name: sanitizeGoIdent(argPat.name) }],
                          right: { kind: "GoSelectorExpr", expr: s, sel: fieldName }
                      });
                  } else {
                      // Custom ADTs: map-based field access
                      stmts.push({
                          kind: "GoAssignStmt",
                          define: true,
                          left: [{ kind: "GoIdent", name: sanitizeGoIdent(argPat.name) }],
                          right: { kind: "GoRawExpr", code: `sky_wrappers.Sky_AsMap(${emitGoExprForLower(subj)})["${fieldName}"]` } as any
                      });
                  }
              }
          }
          stmts.push({ kind: "GoReturnStmt", expr: lowerExpr(body, moduleExports, env, foreignModules, constructorMap) });
          return stmts;
      };

      // Detect nested constructor patterns: cases where an outer constructor has
      // a ConstructorPattern as one of its args (e.g., Ok (Just x), Ok Nothing).
      // These share the same outer tag and need a nested switch.
      // Group cases by their outer tag index to detect duplicates.
      const outerTagGroups = new Map<number, { caseIdx: number; pat: CoreIR.ConstructorPattern; body: CoreIR.Expr }[]>();
      for (let i = 0; i < expr.cases.length; i++) {
          const c = expr.cases[i];
          if (c.pattern.kind === "ConstructorPattern") {
              const tagIndex = resolveTagIndex(c.pattern.name, i);
              if (!outerTagGroups.has(tagIndex)) outerTagGroups.set(tagIndex, []);
              outerTagGroups.get(tagIndex)!.push({ caseIdx: i, pat: c.pattern, body: c.body });
          }
      }

      // Check if any outer tag has multiple cases (indicating nested patterns that need grouping)
      const hasNestedCtorPatterns = (group: { pat: CoreIR.ConstructorPattern }[]) =>
          group.length > 1 || group.some(g => g.pat.args.some(a => a.kind === "ConstructorPattern"));

      const cases: GoIR.GoCaseClause[] = [];
      const processedOuterTags = new Set<number>();

      for (let i = 0; i < expr.cases.length; i++) {
        const c = expr.cases[i];

        if (c.pattern.kind === "ConstructorPattern") {
           const tagIndex = resolveTagIndex(c.pattern.name, i);

           // Skip if we already processed this outer tag as part of a group
           if (processedOuterTags.has(tagIndex)) continue;

           const group = outerTagGroups.get(tagIndex)!;

           // Check if this group needs nested switching
           if (hasNestedCtorPatterns(group)) {
               processedOuterTags.add(tagIndex);

               // Generate a single case clause with a nested switch for the inner constructors
               const outerStmts: GoIR.GoStmt[] = [];

               // Extract the outer field value into a temp variable
               const outerFieldName = wellKnownFields[c.pattern.name] || "V0";
               const innerTmpName = `__inner_${Math.floor(Math.random() * 100000)}`;

               outerStmts.push({
                   kind: "GoExprStmt",
                   expr: { kind: "GoRawExpr", code: `var ${innerTmpName} any` } as any
               });

               if (isWellKnownAdtType(adtTypeName)) {
                   // Well-known types: struct-based field access
                   const outerSubj: GoIR.GoExpr = { kind: "GoTypeAssertExpr", expr: subjRef, type: { kind: "GoIdentType", name: adtTypeName! } } as any;
                   outerStmts.push({
                       kind: "GoAssignStmt",
                       define: false,
                       left: [{ kind: "GoIdent", name: innerTmpName }],
                       right: { kind: "GoSelectorExpr", expr: outerSubj, sel: outerFieldName }
                   } as any);
               } else {
                   // Custom ADTs: map-based field access
                   outerStmts.push({
                       kind: "GoAssignStmt",
                       define: false,
                       left: [{ kind: "GoIdent", name: innerTmpName }],
                       right: { kind: "GoRawExpr", code: `sky_wrappers.Sky_AsMap(${subjTempName})["${outerFieldName}"]` } as any
                   } as any);
               }

               const innerRef: GoIR.GoExpr = { kind: "GoIdent", name: innerTmpName };

               // Determine inner ADT type from the nested constructor patterns
               let innerAdtType: string | undefined;
               for (const g of group) {
                   for (const arg of g.pat.args) {
                       if (arg.kind === "ConstructorPattern") {
                           innerAdtType = resolveInnerAdtType(arg.name);
                           break;
                       }
                   }
                   if (innerAdtType) break;
               }

               // Build inner case clauses
               const innerCases: GoIR.GoCaseClause[] = [];
               let hasInnerDefault = false;

               for (const g of group) {
                   const innerEnv = new Map(localEnv || []);

                   // Check if the arg is a nested ConstructorPattern
                   const innerCtorArg = g.pat.args.find(a => a.kind === "ConstructorPattern") as CoreIR.ConstructorPattern | undefined;

                   if (innerCtorArg) {
                       // Nested constructor: switch on inner tag
                       const innerTagIndex = resolveTagIndex(innerCtorArg.name, 0);
                       const innerStmts = genSimpleCtorStmts(innerCtorArg, g.body, innerEnv, innerRef, innerAdtType);
                       innerCases.push({
                           kind: "GoCaseClause",
                           exprs: [{ kind: "GoBasicLit", value: String(innerTagIndex) }],
                           body: innerStmts
                       });
                   } else if (g.pat.args.length === 1 && g.pat.args[0].kind === "VariablePattern") {
                       // Single variable arg capturing the whole inner value (e.g., Ok value)
                       // This is a catch-all for the inner switch
                       const varPat = g.pat.args[0];
                       const innerStmts: GoIR.GoStmt[] = [];
                       if (varPat.name !== "_") {
                           innerEnv.set(varPat.name, { kind: "TypeConstant", name: "Any" });
                           innerStmts.push({
                               kind: "GoAssignStmt",
                               define: true,
                               left: [{ kind: "GoIdent", name: sanitizeGoIdent(varPat.name) }],
                               right: innerRef
                           });
                       }
                       innerStmts.push({ kind: "GoReturnStmt", expr: lowerExpr(g.body, moduleExports, innerEnv, foreignModules, constructorMap) });
                       hasInnerDefault = true;
                       innerCases.push({ kind: "GoCaseClause", exprs: [], body: innerStmts });
                   } else if (g.pat.args.length === 0) {
                       // No args (e.g., Ok Nothing where Nothing has no args but is not nested)
                       // This shouldn't normally happen since Nothing would be a ConstructorPattern arg
                       const innerStmts: GoIR.GoStmt[] = [];
                       innerStmts.push({ kind: "GoReturnStmt", expr: lowerExpr(g.body, moduleExports, innerEnv, foreignModules, constructorMap) });
                       innerCases.push({ kind: "GoCaseClause", exprs: [], body: innerStmts });
                   } else {
                       // Wildcard or other pattern as arg — treat as default
                       const innerStmts: GoIR.GoStmt[] = [];
                       innerStmts.push({ kind: "GoReturnStmt", expr: lowerExpr(g.body, moduleExports, innerEnv, foreignModules, constructorMap) });
                       hasInnerDefault = true;
                       innerCases.push({ kind: "GoCaseClause", exprs: [], body: innerStmts });
                   }
               }

               // Build inner switch on the inner value's .Tag
               let innerSwitchExpr: GoIR.GoExpr;
               if (isWellKnownAdtType(innerAdtType)) {
                   const innerAsserted: GoIR.GoExpr = { kind: "GoTypeAssertExpr", expr: innerRef, type: { kind: "GoIdentType", name: innerAdtType! } } as any;
                   innerSwitchExpr = { kind: "GoSelectorExpr", expr: innerAsserted, sel: "Tag" };
               } else {
                   innerSwitchExpr = { kind: "GoRawExpr", code: `sky_wrappers.Sky_AsInt(sky_wrappers.Sky_AsMap(${innerTmpName})["Tag"])` } as any;
               }

               // Check if there's a wildcard/variable fallback case in the match expression.
               // If so, use its body as the default for the inner switch (instead of returning nil).
               const wildcardCase = expr.cases.find(
                   wc => wc.pattern.kind === "WildcardPattern" || wc.pattern.kind === "VariablePattern"
               );
               if (wildcardCase) {
                   const wcStmts: GoIR.GoStmt[] = [];
                   const wcEnv = new Map(localEnv || []);
                   if (wildcardCase.pattern.kind === "VariablePattern" && wildcardCase.pattern.name !== "_") {
                       wcEnv.set(wildcardCase.pattern.name, expr.expr.type);
                       wcStmts.push({
                           kind: "GoAssignStmt",
                           define: true,
                           left: [{ kind: "GoIdent", name: sanitizeGoIdent(wildcardCase.pattern.name) }],
                           right: subjRef
                       });
                   }
                   wcStmts.push({ kind: "GoReturnStmt", expr: lowerExpr(wildcardCase.body, moduleExports, wcEnv, foreignModules, constructorMap) });
                   innerCases.push({ kind: "GoCaseClause", exprs: [], body: wcStmts });
               }

               outerStmts.push({
                   kind: "GoSwitchStmt",
                   expr: innerSwitchExpr,
                   cases: innerCases
               });

               outerStmts.push({ kind: "GoReturnStmt", expr: { kind: "GoBasicLit", value: "nil" } });

               cases.push({
                   kind: "GoCaseClause",
                   exprs: [{ kind: "GoBasicLit", value: String(tagIndex) }],
                   body: outerStmts
               });
           } else {
               // Simple case: no nested constructor patterns
               processedOuterTags.add(tagIndex);
               const stmts: GoIR.GoStmt[] = [];
               const newLocalEnv = new Map(localEnv || []);
               const simpleStmts = genSimpleCtorStmts(c.pattern, c.body, newLocalEnv, subjRef, adtTypeName);
               cases.push({
                   kind: "GoCaseClause",
                   exprs: [{ kind: "GoBasicLit", value: String(tagIndex) }],
                   body: simpleStmts
               });
           }
           continue;
        }

        // Fallback catch-all
        if (c.pattern.kind === "WildcardPattern" || c.pattern.kind === "VariablePattern") {
           const stmts: GoIR.GoStmt[] = [];
           const newLocalEnv = new Map(localEnv || []);
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
           cases.push({ kind: "GoCaseClause", exprs: [], body: stmts });
           continue;
        }

        cases.push({ kind: "GoCaseClause", exprs: [], body: [{ kind: "GoReturnStmt", expr: lowerExpr(c.body, moduleExports, localEnv, foreignModules, constructorMap) }] });
      }

      // Build the switch — bind subject to a temp var, then switch on .Tag
      const bodyStmts: GoIR.GoStmt[] = [];

      // Always bind subject to a var (interface-typed so assertions work)
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

      if (isWellKnownAdtType(adtTypeName)) {
          // Well-known types (SkyResult/SkyMaybe): struct-based type assertion for .Tag
          const assertedSubj: GoIR.GoExpr = {
              kind: "GoTypeAssertExpr",
              expr: subjRef,
              type: { kind: "GoIdentType", name: adtTypeName! }
          } as any;

          bodyStmts.push({
              kind: "GoSwitchStmt",
              expr: { kind: "GoSelectorExpr", expr: assertedSubj, sel: "Tag" },
              cases: cases
          });
      } else {
          // Custom ADTs: map-based Tag access
          bodyStmts.push({
              kind: "GoSwitchStmt",
              expr: { kind: "GoRawExpr", code: `sky_wrappers.Sky_AsInt(sky_wrappers.Sky_AsMap(${subjTempName})["Tag"])` } as any,
              cases: cases
          });
      }

      // Only add nil fallback if no wildcard/default case was generated
      // (wildcard cases generate default: clauses which cover all remaining values)
      const hasWildcardCase = cases.some(c => c.exprs.length === 0);
      if (!hasWildcardCase) {
          bodyStmts.push({
              kind: "GoExprStmt", expr: { kind: "GoRawExpr", code: `panic("non-exhaustive pattern match")` } as any
          });
      }
      bodyStmts.push({
          kind: "GoReturnStmt", expr: { kind: "GoBasicLit", value: "nil" }
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
        // Well-known Prelude constructors: Ok, Err, Just, Nothing
        // Must be checked FIRST — these always use sky_wrappers runtime types,
        // even when defined in the current module (e.g., Sky.Core.Maybe).
        const wellKnownCtors: Record<string, { tag: number; wrapper: string; field: string }> = {
            "Ok":      { tag: 0, wrapper: "sky_wrappers.SkyOk",  field: "OkValue" },
            "Err":     { tag: 1, wrapper: "sky_wrappers.SkyErr",  field: "ErrValue" },
            "Just":    { tag: 0, wrapper: "sky_wrappers.SkyJust",    field: "JustValue" },
            "Nothing": { tag: 1, wrapper: "sky_wrappers.SkyNothing", field: "" },
        };
        const wk = wellKnownCtors[expr.name];
        if (wk) {
            const argExprs = expr.args.map(a => lowerExpr(a, moduleExports, localEnv, foreignModules, constructorMap));
            // Use wrapper functions: SkyOk/SkyErr/SkyJust/SkyNothing
            return {
                kind: "GoCallExpr",
                fn: { kind: "GoIdent", name: wk.wrapper },
                args: argExprs.length > 0 ? argExprs : (wk.field === "" ? [] : [{ kind: "GoIdent", name: "nil" }])
            } as any;
        }
        // Local or imported ADT constructor (not well-known)
        const ctorInfo = constructorMap?.get(expr.name) || _importedCtorTags?.get(expr.name);
        if (ctorInfo) {
            const argExprs = expr.args.map(a => lowerExpr(a, moduleExports, localEnv, foreignModules, constructorMap));
            const kvPairs: string[] = [`"Tag": ${ctorInfo.tagIndex}`, `"SkyName": "${expr.name}"`];
            for (let j = 0; j < expr.args.length; j++) {
                const fieldName = `V${j}`;
                kvPairs.push(`"${fieldName}": ${emitGoExprForLower(argExprs[j])}`);
            }
            return {
                kind: "GoRawExpr",
                code: `map[string]any{${kvPairs.join(", ")}}`
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
        let isImported = false;
        if (moduleExports) {
            for (const [modName, exports] of moduleExports) {
                if (exports.has(expr.name)) {
                    const ffiWrapperCheck = new Set(["Std.Log", "Std.Cmd", "Std.Task", "Std.Program", "Sky.Core.Prelude"]);
                    if (ffiWrapperCheck.has(modName)) break;
                    const parts = modName.split(".");
                    const pkgName = makeSafeGoPkgName(parts[parts.length - 1], modName);
                    qualifiedGoName = `${pkgName}.${goName}`;
                    isImported = true;
                    break;
                }
            }
        }
        // For imported constructors, use the generated constructor function
        if (isImported) {
            const argExprs2 = expr.args.map(a => lowerExpr(a, moduleExports, localEnv, foreignModules, constructorMap));
            if (argExprs2.length > 0) {
                // Constructor with args: call it
                return {
                    kind: "GoCallExpr",
                    fn: { kind: "GoRawExpr", code: qualifiedGoName } as any,
                    args: argExprs2
                } as any;
            }
            // Zero args: check if it's a zero-arg constructor (call it) or a
            // multi-arg constructor used as a function reference (pass it)
            const modExport = moduleExports ? Array.from(moduleExports.entries()).find(([_, exports]) => exports.has(expr.name)) : null;
            const exportScheme = modExport ? modExport[1].get(expr.name) : null;
            const isFunctionType = exportScheme?.type?.kind === "TypeFunction";
            if (isFunctionType) {
                // Constructor takes args but none provided — pass as function reference
                return { kind: "GoRawExpr", code: qualifiedGoName } as any;
            }
            // Zero-arg constructor: call it to get the value
            return {
                kind: "GoCallExpr",
                fn: { kind: "GoRawExpr", code: qualifiedGoName } as any,
                args: []
            } as any;
        }
        const argExprs2 = expr.args.map(a => lowerExpr(a, moduleExports, localEnv, foreignModules, constructorMap));
        const kvPairs2: string[] = [`"Tag": 0`, `"SkyName": "${expr.name}"`];
        for (let j = 0; j < argExprs2.length; j++) {
            const fieldName = `V${j}`;
            kvPairs2.push(`"${fieldName}": ${emitGoExprForLower(argExprs2[j])}`);
        }
        return {
            kind: "GoRawExpr",
            code: `map[string]any{${kvPairs2.join(", ")}}`
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

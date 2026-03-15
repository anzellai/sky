// src/lower/lower-to-go.ts
import * as CoreIR from "../core-ir/core-ir.js";
import * as GoIR from "../go-ir/go-ir.js";
import type { Scheme, Type } from "../types/types.js";

function makeSafeGoPkgName(name: string): string {
    if (name === "Main") return "main";
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

export function lowerModule(module: CoreIR.Module, moduleExports?: Map<string, Map<string, Scheme>>, foreignModules?: Set<string>): GoIR.GoPackage {
  const pkg: GoIR.GoPackage = {
    name: makeSafeGoPkgName(module.name[module.name.length - 1]),
    imports: [],
    declarations: []
  };

  const localEnvOuter = new Map<string, Type>();

  // Convert types
  for (const tDecl of module.typeDeclarations) {
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
                let pType: GoIR.GoType = { kind: "GoIdentType", name: "any" };
                let skyType: Type = { kind: "TypeConstant", name: "Any" };
                if (currentType.kind === "TypeFunction") {
                    skyType = currentType.from;
                    pType = lowerType(skyType);
                    currentType = currentType.to;
                }
                params.push({ name: p, type: pType });
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
                            expr: lowerExpr(inner.value, moduleExports, localEnv, foreignModules)
                        });
                    } else {
                        stmts.push({
                            kind: "GoAssignStmt",
                            define: true,
                            left: [{ kind: "GoIdent", name: inner.name }],
                            right: lowerExpr(inner.value, moduleExports, localEnv, foreignModules)
                        });
                        localEnv.set(inner.name, inner.value.type);
                    }
                    flattenLet(inner.body);
                } else if (inner.kind === "Match") {
                    // Match in let body
                    stmts.push({
                        kind: "GoExprStmt",
                        expr: lowerExpr(inner, moduleExports, localEnv, foreignModules)
                    });
                } else {
                    const lowered = lowerExpr(inner, moduleExports, localEnv, foreignModules);
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
      retType = lowerType(currentType);
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
              const safeName = makeSafeGoPkgName(moduleParts[moduleParts.length - 1]);
              if (safeName === local) {
                  pkg.imports.push({ path: "sky-out/" + full.replace(/\./g, "/"), alias: local });
                  break;
              }
          }
      }
  }

  return pkg;
}

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
    
    if (t.name.startsWith("Untyped ")) {
        const inner = t.name.substring(8);
        if (inner === "int") return { kind: "GoIdentType", name: "int" };
        if (inner === "float") return { kind: "GoIdentType", name: "float64" };
        return { kind: "GoIdentType", name: "any" };
    }
    
    if (t.name.includes(".")) {
        const parts = t.name.split(".");
        const pkg = makeSafeGoPkgName(parts[parts.length - 2]);
        const name = parts[parts.length - 1];
        return { kind: "GoSelectorType", pkg, name };
    }
    
    return {
      kind: "GoIdentType",
      name: t.name
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

function lowerExpr(expr: CoreIR.Expr, moduleExports?: Map<string, Map<string, Scheme>>, localEnv?: Map<string, Type>, foreignModules?: Set<string>): GoIR.GoExpr {
  switch (expr.kind) {
    case "Literal": {
      if (expr.literalType === "String") {
        return { kind: "GoBasicLit", value: '"' + expr.value + '"' };
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
              safePkg = pkgName.split(".").map(p => p.toLowerCase()).join("_");
          }

          const goName = name.charAt(0).toUpperCase() + name.slice(1);

          // Force sky_wrappers for stdlib even if it's technically a Sky module
          if (pkgName.startsWith("Std.") || pkgName === "Net.Http" || pkgName === "Crypto.Sha256" || pkgName === "Encoding.Hex" || pkgName === "Cmd" || pkgName === "Sub" || pkgName === "Uuid" || pkgName === "Dotenv") {
              if (name === "none") {
                  const sel = pkgName.endsWith("Cmd") ? "CmdNone" : "SubNone";
                  return { kind: "GoSelectorExpr", expr: { kind: "GoIdent", name: "sky_wrappers" }, sel };
              }
              const wrapperName = "Sky_" + safePkg + "_" + goName;
              return { kind: "GoSelectorExpr", expr: { kind: "GoIdent", name: "sky_wrappers" }, sel: wrapperName };
          }

          // Check if this is a Sky module
          if (moduleExports && moduleExports.has(pkgName) && (!foreignModules || !foreignModules.has(pkgName))) {
              // It's a non-foreign Sky module! Lower to direct Go package call.
              const goPkg = makeSafeGoPkgName(moduleParts[moduleParts.length - 1]);
              return { kind: "GoSelectorExpr", expr: { kind: "GoIdent", name: goPkg }, sel: goName };
          }

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
      
      const goName = (expr.name[0] >= 'a' && expr.name[0] <= 'z') ? expr.name.charAt(0).toUpperCase() + expr.name.slice(1) : expr.name;
      
      // If it's a local variable, don't capitalize
      if (localEnv && localEnv.has(expr.name)) {
          return { kind: "GoIdent", name: expr.name };
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
            left: [{ kind: "GoIdent", name: p }],
            right: { kind: "GoIdent", name: `arg${i}` },
            define: true
          })),
          { kind: "GoReturnStmt", expr: lowerExpr(expr.body, moduleExports, newLocalEnv, foreignModules) }
        ]
      };
    }
    case "IfExpr": {
      return {
        kind: "GoCallExpr",
        fn: {
          kind: "GoFuncLit",
          type: { kind: "GoFuncType", params: [], results: [{ kind: "GoIdentType", name: "any" }] },
          body: [
            {
              kind: "GoIfStmt",
              condition: lowerExpr(expr.condition, moduleExports, localEnv, foreignModules),
              thenBranch: [{ kind: "GoReturnStmt", expr: lowerExpr(expr.thenBranch, moduleExports, localEnv, foreignModules) }],
              elseBranch: [{ kind: "GoReturnStmt", expr: lowerExpr(expr.elseBranch, moduleExports, localEnv, foreignModules) }]
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
      
      // Map listenAndServe to http.ListenAndServe and println to fmt.Println
      let fnExpr = lowerExpr(flat.fn, moduleExports, localEnv, foreignModules);
      let args = flat.args.map((a, i) => {
          let lowered = lowerExpr(a, moduleExports, localEnv, foreignModules);

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

      // Field access like .uuid model -> model["uuid"]
      if (fnExpr.kind === "GoIdent" && fnExpr.name.startsWith(".")) {
          const fieldName = fnExpr.name.substring(1);
          const container = args[0];
          return {
              kind: "GoIndexExpr",
              expr: container,
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
      } else if (fnExpr.kind === "GoIdent" && fnExpr.name === "Println") {
        fnExpr = { kind: "GoSelectorExpr", expr: { kind: "GoIdent", name: "fmt" }, sel: "Println" };
        
        // Special case: if this is used as an expression, we need to wrap it to discard (int, error)
        return {
            kind: "GoCallExpr",
            fn: {
                kind: "GoFuncLit",
                type: { kind: "GoFuncType", params: args.map((_, i) => ({ kind: "GoIdentType", name: "any" } as any)), results: [{ kind: "GoIdentType", name: "any" }] },
                body: [
                    {
                        kind: "GoExprStmt",
                        expr: { kind: "GoCallExpr", fn: fnExpr, args: args.map((_, i) => ({ kind: "GoIdent", name: "arg" + i } as any)) }
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
          // Binary operator uncurried
          const op = fnExpr.name === "++" ? "+" : fnExpr.name;
          
          // Add type assertions if needed for any types
          const finalArgs = args.map((a, i) => {
              const coreArg = flat.args[i];
              let needsAssert = true;
              if (coreArg.type && coreArg.type.kind === "TypeConstant" && (coreArg.type.name === "Int" || coreArg.type.name === "String" || coreArg.type.name === "Float")) {
                  needsAssert = false;
              } else if (coreArg.kind === "Variable" && localEnv) {
                  const t = localEnv.get(coreArg.name);
                  if (t && t.kind === "TypeConstant" && (t.name === "Int" || t.name === "String" || t.name === "Float")) {
                      needsAssert = false;
                  }
              }
              
              if (!needsAssert) return a;
              
              let targetType = "int";
              const fnName = (fnExpr as any).name;
              if (fnName === "++" || (fnName === "+" && (flat.args[0].kind === "Literal" && typeof (flat.args[0] as any).value === "string" || flat.args[1].kind === "Literal" && typeof (flat.args[1] as any).value === "string"))) {
                  targetType = "string";
              } else if (["==", "!=", "<", ">", "<=", ">="].includes(fnName)) {
                  if (flat.args[0].kind === "Literal" && typeof (flat.args[0] as any).value === "string" || flat.args[1].kind === "Literal" && typeof (flat.args[1] as any).value === "string") {
                      targetType = "string";
                  } else if (fnName === "==" || fnName === "!=") {
                      targetType = "any";
                  }
              }

              if (targetType === "any") return a;

              return {
                  kind: "GoTypeAssertExpr",
                  expr: a,
                  type: { kind: "GoIdentType", name: targetType }
              } as any;
          });

          return {
              kind: "GoBinaryExpr",
              left: finalArgs[0],
              op: op,
              right: finalArgs[1]
          };
      }
      
      const result: GoIR.GoExpr = {
        kind: "GoCallExpr",
        fn: fnExpr,
        args: args as GoIR.GoExpr[]
      };

      if (expr.type && (expr.type.kind === "TypeTuple" || expr.type.kind === "TypeApplication" || expr.type.kind === "TypeConstant")) {
          // Type assertion removed because FFI wrappers return concrete types
      }

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
            left: [{ kind: "GoIdent", name: e.name }],
            right: lowerExpr(e.value, moduleExports, newLocalEnv, foreignModules)
          });
          flattenLet(e.body);
        } else if (e.kind === "Match") {
            stmts.push({
                kind: "GoExprStmt",
                expr: lowerExpr(e, moduleExports, newLocalEnv, foreignModules)
            });
        } else {
          stmts.push({ kind: "GoReturnStmt", expr: lowerExpr(e, moduleExports, newLocalEnv, foreignModules) });
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
      const cases: GoIR.GoCaseClause[] = expr.cases.map((c, i) => {
        const stmts: GoIR.GoStmt[] = [];
        const newLocalEnv = new Map(localEnv || []);
        
        // Very basic matching mapped to constructor tags
        // Assuming ADTs are structs with Tag int, and ConstructorValue fields
        if (c.pattern.kind === "ConstructorPattern") {
           // We extract the variables from the struct
           for (let j = 0; j < c.pattern.args.length; j++) {
              const argPat = c.pattern.args[j];
              if (argPat.kind === "VariablePattern" && argPat.name !== "_") {
                  newLocalEnv.set(argPat.name, { kind: "TypeConstant", name: "Any" });
                  // Cast the subject to access its fields
                  let subj = lowerExpr(expr.expr, moduleExports, localEnv, foreignModules);
                  stmts.push({
                      kind: "GoAssignStmt",
                      define: true,
                      left: [{ kind: "GoIdent", name: argPat.name }],
                      right: { kind: "GoSelectorExpr", expr: subj, sel: c.pattern.name + "Value" + (j > 0 ? j : "") }
                  });
              }
           }
           stmts.push({ kind: "GoReturnStmt", expr: lowerExpr(c.body, moduleExports, newLocalEnv, foreignModules) });
           
           return {
               kind: "GoCaseClause",
               exprs: [{ kind: "GoBasicLit", value: String(i) }], // Naive: assuming index is tag
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
                   left: [{ kind: "GoIdent", name: c.pattern.name }],
                   right: lowerExpr(expr.expr, moduleExports, newLocalEnv, foreignModules)
               });
           }
           stmts.push({ kind: "GoReturnStmt", expr: lowerExpr(c.body, moduleExports, newLocalEnv, foreignModules) });
           return { kind: "GoCaseClause", exprs: [], body: stmts };
        }

        return { kind: "GoCaseClause", exprs: [], body: [{ kind: "GoReturnStmt", expr: lowerExpr(c.body, moduleExports, newLocalEnv, foreignModules) }] };
      });

      return {
        kind: "GoCallExpr",
        fn: {
          kind: "GoFuncLit",
          type: { kind: "GoFuncType", params: [], results: [lowerType(expr.type)] },
          body: [
            {
              kind: "GoSwitchStmt",
              expr: { kind: "GoSelectorExpr", expr: lowerExpr(expr.expr, moduleExports, localEnv, foreignModules), sel: "Tag" },
              cases: cases
            },
            {
               // Unreachable panic for exhaustiveness fallback
               kind: "GoExprStmt",
               expr: { kind: "GoCallExpr", fn: { kind: "GoIdent", name: "panic" }, args: [{ kind: "GoBasicLit", value: '"unmatched case"' }] }
            }
          ]
        },
        args: []
      };
    }
    case "Constructor": {
        // Local constructor
        const goName = expr.name.charAt(0).toUpperCase() + expr.name.slice(1);
        return {
            kind: "GoCompositeLit",
            type: { kind: "GoIdentType", name: goName },
            elements: [{ kind: "GoBasicLit", value: "0" }, ...expr.args.map(a => lowerExpr(a, moduleExports, localEnv, foreignModules))]
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
              value: lowerExpr(expr.fields[k], moduleExports, localEnv, foreignModules)
          }))
      } as any;
    }
    case "ListExpr": {
        let elemType: GoIR.GoType = { kind: "GoIdentType", name: "any" };
        if (expr.items.length > 0 && expr.items[0].type && expr.items[0].type.kind === "TypeConstant") {
            if (expr.items[0].type.name === "String") elemType = { kind: "GoIdentType", name: "string" };
        }
        return {
            kind: "GoSliceLit",
            type: { kind: "GoSliceType", elem: elemType },
            elements: expr.items.map(i => lowerExpr(i, moduleExports, localEnv, foreignModules))
        };
    }
    default:
      return { kind: "GoBasicLit", value: "/* unimplemented */" };
  }
}

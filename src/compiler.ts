// src/compiler.ts
// Sky compiler pipeline with module graph support.

import fs from "fs";
import path from "path";
import { getDirname } from "./utils/path.js";

const __dirname = getDirname(import.meta.url);

import { lowerModule } from "./lower/lower-to-go.js";
import { emitGoPackage } from "./emit/go-emitter.js";
import * as CoreIR from "./core-ir/core-ir.js";
import { checkModule } from "./types/checker.js";
import { collectForeignImports } from "./interop/go/collect-foreign.js";
import { buildModuleGraph } from "./modules/resolver.js";
import type { Scheme } from "./types.js";
import * as AST from "./ast/ast.js";
import { listVirtualAssets, readVirtualAsset, hasVirtualAsset } from "./assets.js";

import type { TypeEnvironment } from "./types/env.js";
import type { TypeCheckResult } from "./types/checker.js";

export interface CompileResult {
  readonly diagnostics: readonly string[];
}

export interface TypeCheckProjectResult {
  readonly diagnostics: readonly string[];
  readonly moduleResults: ReadonlyMap<string, TypeCheckResult>;
  readonly latestModuleAst?: AST.Module;
}

export async function typeCheckProject(
  entryFile: string,
  virtualFile?: { path: string; content: string }
): Promise<TypeCheckProjectResult> {
  const diagnostics: string[] = [];
  const moduleResults = new Map<string, TypeCheckResult>();
  
  const graph = await buildModuleGraph(entryFile, virtualFile);

  if (graph.diagnostics.length > 0) {
    return { diagnostics: graph.diagnostics, moduleResults };
  }

  // Map of moduleName -> exported names -> type scheme
  const moduleExports = new Map<string, Map<string, Scheme>>();

  let latestModuleAst: AST.Module | undefined;

  for (const loaded of graph.modules) {
    latestModuleAst = loaded.moduleAst;
    
    const foreignResult = await collectForeignImports(loaded.moduleAst, loaded.filePath);
    diagnostics.push(...foreignResult.diagnostics);

    if (diagnostics.length > 0) {
      return { diagnostics, moduleResults, latestModuleAst };
    }

    const importsMap = new Map<string, Scheme>();

    for (const imp of loaded.moduleAst.imports) {
      const depName = imp.moduleName.join(".");
      let depExports = moduleExports.get(depName);

      // Fallback for implicitly resolved FFI modules
      if (!depExports) {
        depExports = moduleExports.get(`Sky.FFI.${depName}`);
      }

      if (!depExports) {
        // If it's a completely foreign auto-generated module or skipped somehow, we just proceed.
        continue;
      }

      if (imp.exposing) {
        if (imp.exposing.open) {
          // Open import: import Foo exposing (..)
          for (const [name, scheme] of depExports.entries()) {
            importsMap.set(name, scheme);
          }
        } else {
          // Explicit import: import Foo exposing (bar, baz)
          for (const item of imp.exposing.items) {
            if (item.kind === "value") {
              const scheme = depExports.get(item.name);
              if (scheme) {
                importsMap.set(item.name, scheme);
              } else {
                diagnostics.push(`${loaded.filePath}:${item.span.start.line}:${item.span.start.column}: Module ${depName} does not expose ${item.name}`);
              }
            }
          }
        }
      }

      // Always add qualified names: Foo.bar
      for (const [name, scheme] of depExports.entries()) {
        importsMap.set(`${depName}.${name}`, scheme);
      }
    }

    if (diagnostics.length > 0) {
      return { diagnostics, moduleResults, latestModuleAst };
    }

    const typeCheck = checkModule(loaded.moduleAst, {
      foreignBindings: foreignResult.bindings,
      imports: importsMap,
    });
    
    moduleResults.set(loaded.moduleAst.name.join("."), typeCheck);

    if (typeCheck.diagnostics.length > 0) {
      for (const d of typeCheck.diagnostics) {
        diagnostics.push(`${loaded.filePath}:${d.span.start.line}:${d.span.start.column}: ${d.message}`);
      }
      return { diagnostics, moduleResults, latestModuleAst };
    }

    const myExports = new Map<string, Scheme>();
    
    // Auto-expose all top level declarations for now,
    // or filter by `loaded.moduleAst.exposing` if it exists.
    for (const decl of typeCheck.declarations) {
      const isExposed = !loaded.moduleAst.exposing || 
        loaded.moduleAst.exposing.open || 
        loaded.moduleAst.exposing.items.some(i => i.kind === "value" && i.name === decl.name);

      if (isExposed) {
        myExports.set(decl.name, decl.scheme);
      }
    }

    // Also export foreign functions if they are exposed
    for (const binding of foreignResult.bindings) {
      for (const val of binding.values) {
        const isExposed = !loaded.moduleAst.exposing || 
          loaded.moduleAst.exposing.open || 
          loaded.moduleAst.exposing.items.some(i => i.kind === "value" && i.name === val.skyName);
          
        if (isExposed) {
          const scheme = typeCheck.environment.get(val.skyName);
          if (scheme) {
            myExports.set(val.skyName, scheme);
          }
        }
      }
    }

    moduleExports.set(loaded.moduleAst.name.join("."), myExports);
  }

  return { diagnostics, moduleResults, latestModuleAst };
}

// Incremental compilation cache
interface ModuleCacheEntry {
  readonly mtime: number;
  readonly typeCheck: TypeCheckResult;
  readonly exports: Map<string, Scheme>;
  readonly code: string;
}

const moduleCache = new Map<string, ModuleCacheEntry>();

export async function compileProject(
  entryFile: string,
  outDir = "dist",
  target: "web" | "node" | "native" = "node"
): Promise<CompileResult> {
  const diagnostics: string[] = [];

  const graph = await buildModuleGraph(entryFile);

  if (graph.diagnostics.length > 0) {
    return { diagnostics: graph.diagnostics };
  }

  // Ensure output directory exists and is marked as an ES module
  fs.mkdirSync(outDir, { recursive: true });
  
  // Extract runtime from virtual assets (if bundled) or copy from src
  const virtualRuntimeAssets = listVirtualAssets("runtime/");
  if (virtualRuntimeAssets.length > 0) {
    for (const assetPath of virtualRuntimeAssets) {
      const destPath = path.join(outDir, assetPath);
      fs.mkdirSync(path.dirname(destPath), { recursive: true });
      fs.writeFileSync(destPath, readVirtualAsset(assetPath));
    }
  } else {
    // Development mode fallback
    const runtimeSrc = path.resolve(__dirname, "../src/runtime");
    const runtimeDest = path.join(outDir, "runtime");
    if (fs.existsSync(runtimeSrc)) {
      fs.cpSync(runtimeSrc, runtimeDest, { recursive: true });
    }
  }

  const pkgJsonPath = path.join(outDir, "package.json");
  const pkgJson = JSON.stringify({ 
    type: "module",
    imports: {
      "@sky/runtime/*": "./runtime/*.js"
    }
  }, null, 2);

  if (!fs.existsSync(pkgJsonPath) || fs.readFileSync(pkgJsonPath, "utf8") !== pkgJson) {
    fs.writeFileSync(pkgJsonPath, pkgJson);
  }

  // Map of moduleName -> exported names -> type scheme
  const moduleExports = new Map<string, Map<string, Scheme>>();

  for (const loaded of graph.modules) {
    const moduleNameStr = loaded.moduleAst.name.join(".");
    
    // Determine mtime, handling virtual assets
    let mtime: number;
    const stdlibIndex = loaded.filePath.indexOf("stdlib/");
    const runtimeIndex = loaded.filePath.indexOf("runtime/");
    let relPath: string | undefined;
    if (stdlibIndex !== -1) relPath = loaded.filePath.substring(stdlibIndex);
    else if (runtimeIndex !== -1) relPath = loaded.filePath.substring(runtimeIndex);

    if (relPath && hasVirtualAsset(relPath)) {
      mtime = 0; // Virtual assets are static for a given compiler build
    } else {
      mtime = fs.statSync(loaded.filePath).mtimeMs;
    }

    const cached = moduleCache.get(loaded.filePath);

    if (cached && cached.mtime === mtime) {
      moduleExports.set(moduleNameStr, cached.exports);
      
      const outputFile = computeOutputFile(loaded.moduleAst.name, outDir);
      if (!fs.existsSync(outputFile)) {
        fs.mkdirSync(path.dirname(outputFile), { recursive: true });
        fs.writeFileSync(outputFile, cached.code, "utf8");
      }
      continue;
    }

    const foreignResult = await collectForeignImports(loaded.moduleAst, loaded.filePath);
    diagnostics.push(...foreignResult.diagnostics);

    if (diagnostics.length > 0) {
      return { diagnostics };
    }

    const importsMap = new Map<string, Scheme>();
    const importPaths = new Map<string, string>();
    const importExposes = new Map<string, string[]>();

    for (const imp of loaded.moduleAst.imports) {
      const depName = imp.moduleName.join(".");
      let depExports = moduleExports.get(depName);

      // Fallback for implicitly resolved FFI modules
      if (!depExports) {
        depExports = moduleExports.get(`Sky.FFI.${depName}`);
        if (depExports) {
          const thunkPath = path.resolve(".skycache", "ffi", "Sky", "FFI", `${depName}.js`);
          if (fs.existsSync(thunkPath)) {
            const outFilePath = computeOutputFile(loaded.moduleAst.name, outDir);
            let rel = path.relative(path.dirname(outFilePath), thunkPath);
            if (!rel.startsWith(".")) rel = "./" + rel;
            importPaths.set(depName, rel.replace(/\\/g, "/"));
          } else {
            importPaths.set(depName, depName.toLowerCase());
          }
        }
      }

      if (!depExports) {
        // If it's a completely foreign auto-generated module or skipped somehow, we just proceed.
        continue;
      }

      if (imp.exposing) {
        if (imp.exposing.open) {
          // Open import: import Foo exposing (..)
          const exposedKeys: string[] = [];
          for (const [name, scheme] of depExports.entries()) {
            importsMap.set(name, scheme);
            exposedKeys.push(name);
          }
          importExposes.set(depName, exposedKeys);
        } else {
          // Explicit import: import Foo exposing (bar, baz)
          for (const item of imp.exposing.items) {
            if (item.kind === "value") {
              const scheme = depExports.get(item.name);
              if (scheme) {
                importsMap.set(item.name, scheme);
              } else {
                diagnostics.push(`${loaded.filePath}:${item.span.start.line}:${item.span.start.column}: Module ${depName} does not expose ${item.name}`);
              }
            }
          }
        }
      }

      // Always add qualified names: Foo.bar
      for (const [name, scheme] of depExports.entries()) {
        importsMap.set(`${depName}.${name}`, scheme);
      }
    }

    if (diagnostics.length > 0) {
      return { diagnostics };
    }

    const typeCheck = checkModule(loaded.moduleAst, {
      foreignBindings: foreignResult.bindings,
      imports: importsMap,
    });

    if (typeCheck.diagnostics.length > 0) {
      for (const d of typeCheck.diagnostics) {
        diagnostics.push(`${loaded.filePath}:${d.span.start.line}:${d.span.start.column}: ${d.message}`);
      }
      return { diagnostics };
    }

    const myExports = new Map<string, Scheme>();
    
    // Auto-expose all top level declarations for now,
    // or filter by `loaded.moduleAst.exposing` if it exists.
    for (const decl of typeCheck.declarations) {
      const isExposed = !loaded.moduleAst.exposing || 
        loaded.moduleAst.exposing.open || 
        loaded.moduleAst.exposing.items.some(i => i.kind === "value" && i.name === decl.name);

      if (isExposed) {
        myExports.set(decl.name, decl.scheme);
      }
    }

    // Also export foreign functions if they are exposed
    for (const binding of foreignResult.bindings) {
      for (const val of binding.values) {
        const isExposed = !loaded.moduleAst.exposing || 
          loaded.moduleAst.exposing.open || 
          loaded.moduleAst.exposing.items.some(i => i.kind === "value" && i.name === val.skyName);
          
        if (isExposed) {
          const scheme = typeCheck.environment.get(val.skyName);
          if (scheme) {
            myExports.set(val.skyName, scheme);
          }
        }
      }
    }

    moduleExports.set(moduleNameStr, myExports);

    // Basic AST to CoreIR conversion
    const coreModule: CoreIR.Module = astToCore(loaded.moduleAst, typeCheck, foreignResult);
    
    // Lower to GoIR
    const goPkg = lowerModule(coreModule);
    
    // Emit Go code
    const goCode = emitGoPackage(goPkg);

    // Update cache
    moduleCache.set(loaded.filePath, {
      mtime,
      typeCheck,
      exports: myExports,
      code: goCode
    });

    const outputFile = computeOutputFile(loaded.moduleAst.name, outDir);

    fs.mkdirSync(path.dirname(outputFile), { recursive: true });
    fs.writeFileSync(outputFile, goCode, "utf8");
  }

  return { diagnostics };
}

function computeOutputFile(moduleName: readonly string[], outDir: string): string {
  if (moduleName.length === 1 && moduleName[0] === "Main") {
    return path.join(outDir, "main.go"); // main package special case
  }
  return path.join(outDir, ...moduleName) + ".go";
}


function convertExpr(expr: AST.Expression): CoreIR.Expr {
  switch (expr.kind) {
    case "IntegerLiteralExpression":
      return { kind: "Literal", value: expr.value, literalType: "Int", type: { kind: "TypeConstant", name: "Int" } };
    case "FloatLiteralExpression":
      return { kind: "Literal", value: expr.value, literalType: "Float", type: { kind: "TypeConstant", name: "Float" } };
    case "StringLiteralExpression":
      return { kind: "Literal", value: expr.value, literalType: "String", type: { kind: "TypeConstant", name: "String" } };
    case "IdentifierExpression":
      return { kind: "Variable", name: expr.name, type: { kind: "TypeConstant", name: "Any" } }; // Simplified type
    case "CallExpression":
      // A call like f(a, b) in Sky is parsed as f(a, b) or nested applications
      // The AST has it as `callee` and `arguments`
      let res = convertExpr(expr.callee);
      for (const arg of expr.arguments) {
        res = { kind: "Application", fn: res, args: [convertExpr(arg)], type: { kind: "TypeConstant", name: "Any" } };
      }
      return res;
    case "LetExpression": {
      let res = convertExpr(expr.body);
      // Let bindings in AST are usually represented as an array of declarations
      // We'll wrap the body in LetBinding nodes
      for (let i = expr.bindings.length - 1; i >= 0; i--) {
        const binding = expr.bindings[i];
        if (binding.pattern.kind === "VariablePattern") {
          res = {
            kind: "LetBinding",
            name: binding.pattern.name,
            value: convertExpr(binding.value),
            body: res,
            type: { kind: "TypeConstant", name: "Any" }
          };
        }
      }
      return res;
    }
    default:
      return { kind: "Literal", value: `/* unimplemented AST node: ${expr.kind} */`, literalType: "String", type: { kind: "TypeConstant", name: "String" } };
  }
}

function astToCore(ast: AST.Module, typeCheck: TypeCheckResult, foreignResult: any): CoreIR.Module {
  const declarations: CoreIR.Declaration[] = [];
  const typeDeclarations: CoreIR.TypeDeclaration[] = [];

  for (const decl of ast.declarations) {
    if (decl.kind === "FunctionDeclaration") {
      const declInfo = typeCheck.declarations.find(d => d.name === decl.name);
      
      let bodyExpr = convertExpr(decl.body);
      
      // If it has parameters, wrap in lambdas
      for (let i = decl.parameters.length - 1; i >= 0; i--) {
        const paramPattern = decl.parameters[i].pattern;
        const paramName = paramPattern.kind === "VariablePattern" ? paramPattern.name : "_";
        bodyExpr = {
          kind: "Lambda",
          params: [paramName],
          body: bodyExpr,
          type: { kind: "TypeConstant", name: "Any" }
        };
      }

      declarations.push({
        name: decl.name,
        scheme: declInfo?.scheme || { type: { kind: "TypeConstant", name: "Any" }, bound: [] },
        body: bodyExpr
      });
    } else if (decl.kind === "TypeDeclaration") {
      typeDeclarations.push({
        name: decl.name,
        typeParams: Array.from(decl.typeParameters),
        constructors: (decl as any).constructors.map((c: any) => ({
          name: c.name,
          types: c.arguments.map(() => ({ kind: "TypeConstant", name: "Any" }))
        }))
      });
    }
  }

  // Inject foreign imports as declarations
  for (const ffi of foreignResult.bindings) {
    for (const val of ffi.values) {
      // Create a foreign mock function/var
      // Since it's mapped to a Go package, we'll create a ModuleRef or similar
      // Or in the lowerer, we'll map `listenAndServe` directly.
      // We will let the Go emitter handle foreign identifiers by preserving their Go names.
    }
  }

  return {
    name: Array.from(ast.name),
    declarations,
    typeDeclarations
  };
}


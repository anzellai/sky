// src/compiler.ts
// Sky compiler pipeline with module graph support.

import fs from "fs";
import path from "path";
import { getDirname } from "./utils/path.js";

const __dirname = getDirname(import.meta.url);

import { lowerModule } from "./lower/lower-to-go.js";
import { analyzeUsage } from "./lower/passes/usage-analysis.js";
import { eliminateDeadBindings } from "./lower/passes/dead-bindings.js";
import { emitGoPackage } from "./emit/go-emitter.js";
import * as CoreIR from "./core-ir/core-ir.js";
import { checkModule } from "./types/checker.js";
import { collectForeignImports } from "./interop/go/collect-foreign.js";
import { buildModuleGraph } from "./modules/resolver.js";
import type { Scheme } from "./types/types.js";
import * as AST from "./ast/ast.js";

import type { TypeEnvironment } from "./types/env.js";
import type { TypeCheckResult } from "./types/checker.js";

export interface CompileResult {
  readonly diagnostics: readonly string[];
}

export interface TypeCheckProjectResult {
  readonly diagnostics: readonly string[];
  readonly moduleResults: ReadonlyMap<string, TypeCheckResult>;
  readonly latestModuleAst?: AST.Module;
  readonly modules?: readonly { filePath: string; moduleAst: AST.Module }[];
}

export async function typeCheckProject(
  entryFile: string,
  virtualFile?: { path: string; content: string }
): Promise<TypeCheckProjectResult> {
  const diagnostics: string[] = [];
  const moduleResults = new Map<string, TypeCheckResult>();
  
  const graph = await buildModuleGraph(entryFile, virtualFile);

  if (graph.diagnostics.length > 0) {
    return { diagnostics: graph.diagnostics, moduleResults, modules: graph.modules };
  }

  // Map of moduleName -> exported names -> type scheme
  const moduleExports = new Map<string, Map<string, Scheme>>();

  let latestModuleAst: AST.Module | undefined;

  for (const loaded of graph.modules) {
    latestModuleAst = loaded.moduleAst;
    
    const foreignResult = await collectForeignImports(loaded.moduleAst, loaded.filePath);
    diagnostics.push(...foreignResult.diagnostics);

    if (diagnostics.length > 0) {
      return { diagnostics, moduleResults, latestModuleAst, modules: graph.modules };
    }

    const importsMap = new Map<string, Scheme>();

    for (const imp of loaded.moduleAst.imports) {
      const depName = imp.moduleName.join(".");
      let depExports = moduleExports.get(depName); console.log(`[DEBUG] depName: ${depName}, found: ${!!depExports}`);

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

      for (const [name, scheme] of depExports.entries()) {
        importsMap.set(`${depName}.${name}`, scheme);
        if (imp.alias) {
          importsMap.set(`${imp.alias.name}.${name}`, scheme); console.log(`[DEBUG] Aliased ${imp.alias.name}.${name}`);
        }
      }
    }

    if (diagnostics.length > 0) {
      return { diagnostics, moduleResults, latestModuleAst, modules: graph.modules };
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
      return { diagnostics, moduleResults, latestModuleAst, modules: graph.modules };
    }

    const myExports = new Map<string, Scheme>();
    
    // Auto-expose all top level declarations for now,
    // or filter by `loaded.moduleAst.exposing` if it exists.
    for (const decl of typeCheck.declarations) {
      const isExposed = !loaded.moduleAst.exposing || 
        loaded.moduleAst.exposing.open || 
        loaded.moduleAst.exposing.items.some((i: any) => i.kind === "value" && i.name === decl.name);

      if (isExposed) {
        myExports.set(decl.name, decl.scheme);
      }
    }

    for (const astDecl of loaded.moduleAst.declarations) {
      if (astDecl.kind === "TypeDeclaration") {
        const isExposed = !loaded.moduleAst.exposing || 
          loaded.moduleAst.exposing.open || 
          loaded.moduleAst.exposing.items.some((i: any) => i.kind === "type" && i.name === astDecl.name);

        if (isExposed) {
          for (const variant of astDecl.variants) {
             const scheme = typeCheck.environment.get(variant.name);
             if (scheme) {
               myExports.set(variant.name, scheme);
             }
          }
        }
      }
    }

    // Also export foreign functions if they are exposed
    for (const binding of foreignResult.bindings) {
      for (const val of binding.values) {
        const isExposed = !loaded.moduleAst.exposing || 
          loaded.moduleAst.exposing.open || 
          loaded.moduleAst.exposing.items.some((i: any) => i.kind === "value" && i.name === val.skyName);
          
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

  return { diagnostics, moduleResults, latestModuleAst, modules: graph.modules };
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

  // Map of moduleName -> exported names -> type scheme
  const moduleExports = new Map<string, Map<string, Scheme>>();
  const allForeignPackages = new Set<string>();

  for (const loaded of graph.modules) {
    const moduleNameStr = loaded.moduleAst.name.join(".");
    
    // Determine mtime, handling virtual assets
    let mtime: number;
    const stdlibIndex = loaded.filePath.indexOf("stdlib/");
    const runtimeIndex = loaded.filePath.indexOf("runtime/");
    let relPath: string | undefined;
    if (stdlibIndex !== -1) relPath = loaded.filePath.substring(stdlibIndex);
    else if (runtimeIndex !== -1) relPath = loaded.filePath.substring(runtimeIndex);

    
      if (loaded.filePath.startsWith("virtual:")) {
      mtime = 0; // Virtual assets are static
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

    for (const b of foreignResult.bindings) {
        allForeignPackages.add(b.packageName);
    }

    if (diagnostics.length > 0) {
      return { diagnostics };
    }

    const importsMap = new Map<string, Scheme>();
    const importPaths = new Map<string, string>();
    const importExposes = new Map<string, string[]>();

    for (const imp of loaded.moduleAst.imports) {
      const depName = imp.moduleName.join(".");
      let depExports = moduleExports.get(depName); console.log(`[DEBUG] depName: ${depName}, found: ${!!depExports}`);

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
        continue;
      }

      if (imp.exposing) {
        if (imp.exposing.open) {
          const exposedKeys: string[] = [];
          for (const [name, scheme] of depExports.entries()) {
            importsMap.set(name, scheme);
            exposedKeys.push(name);
          }
          importExposes.set(depName, exposedKeys);
        } else {
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

      for (const [name, scheme] of depExports.entries()) {
        importsMap.set(`${depName}.${name}`, scheme);
        if (imp.alias) {
          importsMap.set(`${imp.alias.name}.${name}`, scheme); console.log(`[DEBUG] Aliased ${imp.alias.name}.${name}`);
        }
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
    
    for (const decl of typeCheck.declarations) {
      const isExposed = !loaded.moduleAst.exposing || 
        loaded.moduleAst.exposing.open || 
        loaded.moduleAst.exposing.items.some((i: any) => i.kind === "value" && i.name === decl.name);

      if (isExposed) {
        myExports.set(decl.name, decl.scheme);
      }
    }

    for (const astDecl of loaded.moduleAst.declarations) {
      if (astDecl.kind === "TypeDeclaration") {
        const isExposed = !loaded.moduleAst.exposing || 
          loaded.moduleAst.exposing.open || 
          loaded.moduleAst.exposing.items.some((i: any) => i.kind === "type" && i.name === astDecl.name);

        if (isExposed) {
          for (const variant of astDecl.variants) {
             const scheme = typeCheck.environment.get(variant.name);
             if (scheme) {
               myExports.set(variant.name, scheme);
             }
          }
        }
      }
    }

    for (const binding of foreignResult.bindings) {
      for (const val of binding.values) {
        const isExposed = !loaded.moduleAst.exposing || 
          loaded.moduleAst.exposing.open || 
          loaded.moduleAst.exposing.items.some((i: any) => i.kind === "value" && i.name === val.skyName);
          
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
    let coreModule: CoreIR.Module = astToCore(loaded.moduleAst, typeCheck, foreignResult);
    const usage = analyzeUsage(coreModule);
    coreModule = eliminateDeadBindings(coreModule, usage);
    
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

  // Tree-shake wrappers
  const usedSymbols = new Set<string>();
  const scanDir = (dir: string) => {
      if (!fs.existsSync(dir)) return;
      const files = fs.readdirSync(dir);
      for (const f of files) {
          const p = path.join(dir, f);
          if (fs.statSync(p).isDirectory()) {
              if (f !== "sky_wrappers" && f !== ".skycache" && f !== ".git") {
                  scanDir(p);
              }
          } else if (p.endsWith(".go")) {
              const code = fs.readFileSync(p, "utf8");
              const matches = code.match(/Sky_[a-zA-Z0-9_]+/g);
              if (matches) {
                  for (const m of matches) usedSymbols.add(m);
              }
          }
      }
  };
  scanDir(outDir);

  if (allForeignPackages.size > 0) {
      const { inspectPackage } = await import("./interop/go/inspect-package.js");
      const { generateWrappers } = await import("./interop/go/generate-wrappers.js");
      for (const pkgName of allForeignPackages) {
          if (pkgName === "JSON" || pkgName === "global" || pkgName.startsWith("@sky/runtime/") || pkgName === "sky_wrappers" || pkgName === "sky_std_channel") continue;
          try {
              const pkg = inspectPackage(pkgName);
              generateWrappers(pkgName, pkg, usedSymbols);
          } catch (e) {
              console.warn(`Failed to inspect package ${pkgName} for tree-shaking.`);
          }
      }
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
    case "QualifiedIdentifierExpression":
      return { kind: "Variable", name: expr.name.parts.join("."), type: { kind: "TypeConstant", name: "Any" } };
    case "CallExpression":
      let res = convertExpr(expr.callee);
      for (const arg of expr.arguments) {
        res = { kind: "Application", fn: res, args: [convertExpr(arg)], type: { kind: "TypeConstant", name: "Any" } };
      }
      return res;
    case "LetExpression": {
      let res = convertExpr(expr.body);
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
    case "CaseExpression": {
      const cases: CoreIR.MatchCase[] = expr.branches.map(b => {
          let pat: CoreIR.Pattern = { kind: "WildcardPattern" };
          if (b.pattern.kind === "VariablePattern") {
              pat = { kind: "VariablePattern", name: b.pattern.name };
          } else if (b.pattern.kind === "ConstructorPattern") {
              pat = { 
                  kind: "ConstructorPattern", 
                  name: b.pattern.constructorName.parts.join("."), 
                  args: b.pattern.arguments.map(a => {
                      if (a.kind === "VariablePattern") return { kind: "VariablePattern", name: a.name };
                      return { kind: "WildcardPattern" };
                  })
              };
          }
          return {
              pattern: pat,
              body: convertExpr(b.body)
          };
      });
      return {
          kind: "Match",
          expr: convertExpr(expr.subject),
          cases,
          type: { kind: "TypeConstant", name: "Any" }
      };
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
        scheme: declInfo?.scheme || { type: { kind: "TypeConstant", name: "Any" }, quantified: [] },
        body: bodyExpr
      });
    } else if (decl.kind === "TypeDeclaration") {
      typeDeclarations.push({
        name: decl.name,
        typeParams: Array.from((decl as any).typeParameters || []),
        constructors: (decl as any).variants ? (decl as any).variants.map((c: any) => ({
          name: c.name,
          types: c.fields ? c.fields.map(() => ({ kind: "TypeConstant", name: "Any" })) : []
        })) : []
      });
    }
  }

  return {
    name: Array.from(ast.name),
    declarations,
    typeDeclarations
  };
}

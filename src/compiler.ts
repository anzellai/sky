// src/compiler.ts
// Sky compiler pipeline with module graph support.

import fs from "fs";
import path from "path";
import { getDirname } from "./utils/path.js";

const __dirname = getDirname(import.meta.url);

import { lex } from "./lexer/lexer.js";
import { parse } from "./parser/parser.js";
import { filterLayout } from "./parser/filter-layout.js";
import { checkModule, TypeCheckResult } from "./types/checker.js";
import { lowerModule } from "./lower/lower-to-go.js";
import { emitGoPackage } from "./emit/go-emitter.js";
import { buildModuleGraph, LoadedModule } from "./modules/resolver.js";
import { collectForeignImports } from "./interop/go/collect-foreign.js";
import * as CoreIR from "./core-ir/core-ir.js";
import * as GoIR from "./go-ir/go-ir.js";
import * as AST from "./ast/ast.js";
import { Scheme, Type } from "./types/types.js";
import { analyzeUsage } from "./lower/passes/usage-analysis.js";
import { eliminateDeadBindings } from "./lower/passes/dead-bindings.js";
import { execSync } from "child_process";

export async function typeCheckProject(entryFile: string, virtualFile?: { path: string; content: string }) {
  const graph = await buildModuleGraph(entryFile, virtualFile);
  const moduleExports = new Map<string, Map<string, Scheme>>();
  const allDiagnostics: any[] = [];
  const moduleResults = new Map<string, TypeCheckResult>();

  if (graph.diagnostics.length > 0) {
      return { diagnostics: graph.diagnostics, exports: moduleExports, modules: graph.modules, moduleResults };
  }

  let latestModuleAst: AST.Module | undefined;

  for (const loaded of graph.modules) {
    const moduleNameStr = loaded.moduleAst.name.join(".");
    latestModuleAst = loaded.moduleAst;
    
    // Build environment from dependencies
    const importsMap = new Map<string, Scheme>();
    for (const imp of loaded.moduleAst.imports) {
      const depName = imp.moduleName.join(".");
      const depExports = moduleExports.get(depName);
      if (depExports) {
        for (const [name, scheme] of depExports) {
          if (imp.exposing?.kind === "ExposingClause" && imp.exposing.open) {
            importsMap.set(name, scheme);
          }
          if (imp.alias) {
            importsMap.set(`${imp.alias.name}.${name}`, scheme);
          }
          importsMap.set(`${depName}.${name}`, scheme);
        }
      }
    }

    const foreignResult = await collectForeignImports(loaded.moduleAst, loaded.filePath);
    const typeCheck = checkModule(loaded.moduleAst, { 
        imports: importsMap, 
        foreignBindings: foreignResult.bindings 
    });
    
    moduleResults.set(moduleNameStr, typeCheck);
    allDiagnostics.push(...typeCheck.diagnostics);

    const myExports = new Map<string, Scheme>();
    const isFullyExposed = loaded.moduleAst.exposing?.kind === "ExposingClause" && loaded.moduleAst.exposing.open;

    for (const decl of loaded.moduleAst.declarations) {
      const isExposed = loaded.moduleAst.exposing?.kind === "ExposingClause" &&
        (loaded.moduleAst.exposing.open || loaded.moduleAst.exposing.items.some((it: any) => it.name === decl.name));

      if (decl.kind === "FunctionDeclaration" && isExposed) {
        const info = typeCheck.declarations.find(d => d.name === decl.name);
        if (info) {
          myExports.set(decl.name, info.scheme);
        } else {
          // Fallback: use the environment entry (handles cases where type checker
          // returned early but the function's type was registered via annotation/foreign binding)
          const envScheme = typeCheck.environment.get(decl.name);
          if (envScheme) {
            myExports.set(decl.name, envScheme);
          }
        }
      }
    }

    // For binding modules (exposing (..)), also export any environment entries
    // that came from foreign bindings but aren't in AST declarations
    // (e.g., foreign imported constants/values)
    if (isFullyExposed) {
      for (const [name, scheme] of typeCheck.environment.entries()) {
        if (!myExports.has(name) && !name.includes(".") && name !== "+" && name !== "-" && name !== "*" && name !== "/" && name !== "True" && name !== "False" && name !== "()" && !name.includes("Sky_") && !name.includes("sky_")) {
          myExports.set(name, scheme);
        }
      }
    }

    moduleExports.set(moduleNameStr, myExports);
  }

  return {
      diagnostics: allDiagnostics,
      exports: moduleExports, 
      modules: graph.modules, 
      moduleResults,
      latestModuleAst
  };
}

export async function compileProject(entryFile: string, outDir: string) {
  const graph = await buildModuleGraph(entryFile);
  if (graph.diagnostics.length > 0) {
    return { diagnostics: graph.diagnostics };
  }

  // Ensure output directory exists
  if (fs.existsSync(outDir)) {
    fs.rmSync(outDir, { recursive: true, force: true });
  }
  fs.mkdirSync(outDir, { recursive: true });

  // Map of moduleName -> exported names -> type scheme
  const moduleExports = new Map<string, Map<string, Scheme>>();
  const allForeignPackages = new Set<string>();
  const allForeignModules = new Set<string>();

  for (const loaded of graph.modules) {
    const moduleNameStr = loaded.moduleAst.name.join(".");
    
    if (loaded.filePath.includes(".skycache/go/")) {
        allForeignModules.add(moduleNameStr);
    }

    // Build environment from dependencies
    const importsMap = new Map<string, Scheme>();
    for (const imp of loaded.moduleAst.imports) {
      const depName = imp.moduleName.join(".");
      const depExports = moduleExports.get(depName);
      if (depExports) {
        for (const [name, scheme] of depExports) {
          if (imp.exposing?.kind === "ExposingClause" && imp.exposing.open) {
            importsMap.set(name, scheme);
          }
          if (imp.alias) {
            importsMap.set(`${imp.alias.name}.${name}`, scheme);
          }
          importsMap.set(`${depName}.${name}`, scheme);
        }
      }
    }

    const foreignResult = await collectForeignImports(loaded.moduleAst, loaded.filePath);
    const typeCheck = checkModule(loaded.moduleAst, {
        imports: importsMap,
        foreignBindings: foreignResult.bindings
    });
    
    const myExports = new Map<string, Scheme>();
    const isFullyExposed2 = loaded.moduleAst.exposing?.kind === "ExposingClause" && loaded.moduleAst.exposing.open;

    for (const decl of loaded.moduleAst.declarations) {
      const isExposed = loaded.moduleAst.exposing?.kind === "ExposingClause" &&
        (loaded.moduleAst.exposing.open || loaded.moduleAst.exposing.items.some((it: any) => it.name === decl.name));

      if (decl.kind === "FunctionDeclaration" && isExposed) {
        const info = typeCheck.declarations.find(d => d.name === decl.name);
        if (info) {
          myExports.set(decl.name, info.scheme);
        } else {
          const envScheme = typeCheck.environment.get(decl.name);
          if (envScheme) {
            myExports.set(decl.name, envScheme);
          }
        }
      }
    }

    if (isFullyExposed2) {
      for (const [name, scheme] of typeCheck.environment.entries()) {
        if (!myExports.has(name) && !name.includes(".") && name !== "+" && name !== "-" && name !== "*" && name !== "/" && name !== "True" && name !== "False" && name !== "()" && !name.includes("Sky_") && !name.includes("sky_")) {
          myExports.set(name, scheme);
        }
      }
    }

    moduleExports.set(moduleNameStr, myExports);

    for (const b of foreignResult.bindings) {
        allForeignPackages.add(b.packageName);
    }

    // Basic AST to CoreIR conversion
    let coreModule: CoreIR.Module = astToCore(loaded.moduleAst, typeCheck, foreignResult, importsMap);
    const usage = analyzeUsage(coreModule);
    coreModule = eliminateDeadBindings(coreModule, usage);
    
    // Lower to GoIR
    const goPkg = lowerModule(coreModule, moduleExports, allForeignModules);
    
    // Emit Go code
    const goCode = emitGoPackage(goPkg);

    const outPath = computeOutputFile(loaded.moduleAst.name, outDir);
    fs.mkdirSync(path.dirname(outPath), { recursive: true });
    fs.writeFileSync(outPath, goCode);
  }

  // Go FFI: Generate wrappers for all unique Go packages used
  if (allForeignPackages.size > 0) {
      const { inspectPackage } = await import("./interop/go/inspect-package.js");
      const { generateWrappers } = await import("./interop/go/generate-wrappers.js");
      
      const usedSymbols = new Set<string>();
      const scanDir = (dir: string) => {
          for (const item of fs.readdirSync(dir)) {
              const p = path.join(dir, item);
              if (fs.statSync(p).isDirectory()) {
                  scanDir(p);
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

      for (const pkgName of allForeignPackages) {
          if (pkgName === "JSON" || pkgName === "global" || pkgName.startsWith("@sky/runtime/") || pkgName === "sky_wrappers" || pkgName === "sky_std_channel" || pkgName === "sky_builtin") continue;
          try {
              const pkg = inspectPackage(pkgName);
              generateWrappers(pkgName, pkg, usedSymbols);
          } catch (e) {
              console.warn(`Failed to inspect package ${pkgName} for tree-shaking.`);
          }
      }
  }

  return { diagnostics: [] };
}

function computeOutputFile(moduleName: readonly string[], outDir: string): string {
  if (moduleName.length === 1 && moduleName[0] === "Main") {
    return path.join(outDir, "main.go"); // main package special case
  }
  const folder = path.join(outDir, ...moduleName);
  return path.join(folder, moduleName[moduleName.length - 1] + ".go");
}

function astToCore(ast: AST.Module, typeCheck: TypeCheckResult, foreignResult: any, imports?: Map<string, Scheme>): CoreIR.Module {
  const declarations: CoreIR.Declaration[] = [];
  const typeDeclarations: CoreIR.TypeDeclaration[] = [];
  const localTypes = new Map<string, Type>();
  
  const foreignImports = new Map<string, string>();
  for (const decl of ast.declarations) {
      if (decl.kind === "ForeignImportDeclaration") {
          foreignImports.set(decl.name, decl.sourceModule);
      }
  }

  function convertPattern(pattern: AST.Pattern): CoreIR.Pattern {
    switch (pattern.kind) {
      case "VariablePattern":
        return { kind: "VariablePattern", name: pattern.name };
      case "WildcardPattern":
        return { kind: "WildcardPattern" };
      case "ConstructorPattern":
        return {
          kind: "ConstructorPattern",
          name: pattern.constructorName.parts.join("."),
          args: pattern.arguments.map(a => convertPattern(a)),
        };
      case "LiteralPattern":
        return { kind: "LiteralPattern", value: pattern.value };
      case "ConsPattern":
        return {
          kind: "ConsPattern",
          head: convertPattern(pattern.head),
          tail: convertPattern(pattern.tail),
        };
      case "AsPattern":
        return {
          kind: "AsPattern",
          pattern: convertPattern(pattern.pattern),
          name: pattern.name,
        };
      case "TuplePattern":
        return {
          kind: "ConstructorPattern",
          name: "Tuple" + pattern.items.length,
          args: pattern.items.map(p => convertPattern(p)),
        };
      case "ListPattern":
        if (pattern.items.length === 0) {
          // Empty list pattern: match when list is empty
          return { kind: "LiteralPattern", value: "__empty_list__" };
        }
        return { kind: "WildcardPattern" };
      default:
        return { kind: "WildcardPattern" };
    }
  }

  function convertExpr(expr: AST.Expression): CoreIR.Expr {
    switch (expr.kind) {
      case "IntegerLiteralExpression":
        return { kind: "Literal", value: expr.value, literalType: "Int", type: { kind: "TypeConstant", name: "Int" } };
      case "FloatLiteralExpression":
        return { kind: "Literal", value: expr.value, literalType: "Float", type: { kind: "TypeConstant", name: "Float" } };
      case "StringLiteralExpression":
        return { kind: "Literal", value: expr.value, literalType: "String", type: { kind: "TypeConstant", name: "String" } };
      case "BooleanLiteralExpression":
        return { kind: "Literal", value: expr.value, literalType: "Bool", type: { kind: "TypeConstant", name: "Bool" } };
      case "UnitExpression":
        return { kind: "Literal", value: "()", literalType: "Unit", type: { kind: "TypeConstant", name: "Unit" } };
      case "CharLiteralExpression":
        return { kind: "Literal", value: expr.value, literalType: "String", type: { kind: "TypeConstant", name: "String" } };
      case "ParenthesizedExpression":
        return convertExpr(expr.expression);
      case "IdentifierExpression": {
        const type = localTypes.get(expr.name) || { kind: "TypeConstant", name: "Any" };
        if (expr.name === "True" || expr.name === "False") {
            return { kind: "Literal", value: expr.name === "True", literalType: "Bool", type: { kind: "TypeConstant", name: "Bool" } };
        }
        if (expr.name[0] >= "A" && expr.name[0] <= "Z" && !localTypes.has(expr.name) && !foreignImports.has(expr.name)) {
            return { kind: "Constructor", name: expr.name, args: [], type };
        }
        if (foreignImports.has(expr.name)) {
            return { kind: "Variable", name: foreignImports.get(expr.name) + "." + expr.name, type };
        }
        return { kind: "Variable", name: expr.name, type };
      }
      case "QualifiedIdentifierExpression": {
        const pkg = expr.name.parts.slice(0, -1).join(".");
        const name = expr.name.parts[expr.name.parts.length - 1];
        let type: Type = { kind: "TypeConstant", name: "Any" };
        
        let fullName = expr.name.parts.join(".");
        for (const imp of ast.imports) {
            if (imp.alias && imp.alias.name === pkg) {
                fullName = imp.moduleName.join(".") + "." + name;
                break;
            }
        }
        
        if (imports && imports.has(fullName)) {
            type = imports.get(fullName)!.type;
        }

        if (name === "True" || name === "False") {
            return { kind: "Literal", value: name === "True", literalType: "Bool", type: { kind: "TypeConstant", name: "Bool" } };
        }

        // Heuristic for constructors: uppercase first letter of the name part
        if (name[0] >= "A" && name[0] <= "Z") {
            return { kind: "Constructor", name: fullName, args: [], type };
        }

        return { kind: "Variable", name: fullName, type };
      }
      case "CallExpression":
        let callRes = convertExpr(expr.callee);
        let currentCallType = callRes.type;
        for (const arg of expr.arguments) {
          let retType: Type = { kind: "TypeConstant", name: "Any" };
          if (currentCallType.kind === "TypeFunction") {
              retType = currentCallType.to;
              currentCallType = currentCallType.to;
          }
          callRes = { kind: "Application", fn: callRes, args: [convertExpr(arg)], type: retType };
        }
        return callRes;
      case "LambdaExpression":
        let lambdaBody = convertExpr(expr.body);
        for (let i = expr.parameters.length - 1; i >= 0; i--) {
          const param = expr.parameters[i];
          const name = param.pattern.kind === "VariablePattern" ? param.pattern.name : "_";
          lambdaBody = {
            kind: "Lambda",
            params: [name],
            body: lambdaBody,
            type: { kind: "TypeConstant", name: "Any" }
          };
        }
        return lambdaBody;
      case "LetExpression": {
        let letBody = convertExpr(expr.body);
        for (let i = expr.bindings.length - 1; i >= 0; i--) {
          const binding = expr.bindings[i];
          if (binding.pattern.kind === "VariablePattern") {
            letBody = {
              kind: "LetBinding",
              name: binding.pattern.name,
              value: convertExpr(binding.value),
              body: letBody,
              type: { kind: "TypeConstant", name: "Any" }
            };
          } else if (binding.pattern.kind === "WildcardPattern") {
            letBody = {
              kind: "LetBinding",
              name: "_",
              value: convertExpr(binding.value),
              body: letBody,
              type: { kind: "TypeConstant", name: "Any" }
            };
          } else {
              let pat: CoreIR.Pattern = { kind: "WildcardPattern" };
              if (binding.pattern.kind === "TuplePattern") {
                  pat = {
                      kind: "ConstructorPattern",
                      name: "Tuple" + (binding.pattern as any).items.length,
                      args: (binding.pattern as any).items.map((p: any) => {
                          if (p.kind === "VariablePattern") return { kind: "VariablePattern", name: p.name };
                          return { kind: "WildcardPattern" };
                      })
                  };
              }
              letBody = {
                  kind: "Match",
                  expr: convertExpr(binding.value),
                  cases: [{ pattern: pat, body: letBody }],
                  type: { kind: "TypeConstant", name: "Any" }
              };
          }
        }
        return letBody;
      }
      case "IfExpression":
        return {
          kind: "IfExpr",
          condition: convertExpr(expr.condition),
          thenBranch: convertExpr(expr.thenBranch),
          elseBranch: convertExpr(expr.elseBranch),
          type: { kind: "TypeConstant", name: "Any" }
        };
      case "RecordExpression": {
        const fields: Record<string, CoreIR.Expr> = {};
        for (const f of expr.fields) {
          fields[f.name] = convertExpr(f.value);
        }
        return {
          kind: "RecordExpr",
          fields,
          type: { kind: "TypeConstant", name: "Any" }
        };
      }
      case "RecordUpdateExpression": {
        return {
          kind: "Application",
          fn: { kind: "Variable", name: "updateRecord", type: { kind: "TypeConstant", name: "Any" } },
          args: [convertExpr(expr.base), {
              kind: "RecordExpr",
              fields: Object.fromEntries(expr.fields.map(f => [f.name, convertExpr(f.value)])),
              type: { kind: "TypeConstant", name: "Any" }
          } as any],
          type: { kind: "TypeConstant", name: "Any" }
        };
      }
      case "FieldAccessExpression": {
        return {
          kind: "Application",
          fn: { kind: "Variable", name: "." + expr.fieldName, type: { kind: "TypeConstant", name: "Any" } },
          args: [convertExpr(expr.target)],
          type: { kind: "TypeConstant", name: "Any" }
        };
      }
      case "BinaryExpression": {
        let retType: Type = { kind: "TypeConstant", name: "Any" };
        if (expr.operator === "++") retType = { kind: "TypeConstant", name: "String" };
        if (["+", "-", "*", "/"].includes(expr.operator)) retType = { kind: "TypeConstant", name: "Int" };
        
        return {
          kind: "Application",
          fn: { kind: "Variable", name: expr.operator, type: { kind: "TypeConstant", name: "Any" } },
          args: [convertExpr(expr.left), convertExpr(expr.right)],
          type: retType
        };
      }
      case "TupleExpression": {
        return {
          kind: "Application",
          fn: { kind: "Variable", name: "Tuple" + expr.items.length, type: { kind: "TypeConstant", name: "Any" } },
          args: expr.items.map(convertExpr),
          type: { kind: "TypeTuple", items: expr.items.map(() => ({ kind: "TypeConstant", name: "Any" })) }
        };
      }
      case "ListExpression": {
        return {
          kind: "ListExpr",
          items: expr.items.map(convertExpr),
          type: { kind: "TypeConstant", name: "Any" }
        };
      }
      case "CaseExpression": {
        return {
          kind: "Match",
          expr: convertExpr(expr.subject),
          cases: expr.branches.map(b => {
            return {
              pattern: convertPattern(b.pattern),
              body: convertExpr(b.body)
            };
          }),
          type: { kind: "TypeConstant", name: "Any" }
        };
      }
    }
  }

  for (const decl of ast.declarations) {
    if (decl.kind === "FunctionDeclaration") {
      const declInfo = typeCheck.declarations.find(d => d.name === decl.name);
      const scheme = declInfo?.scheme || { type: { kind: "TypeConstant", name: "Any" }, quantified: [] };
      
      localTypes.clear();
      let currentType = scheme.type;
      for (const param of decl.parameters) {
          if (param.pattern.kind === "VariablePattern") {
              if (currentType.kind === "TypeFunction") {
                  localTypes.set(param.pattern.name, currentType.from);
                  currentType = currentType.to;
              }
          }
      }

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
        scheme,
        body: bodyExpr
      });
    } else if (decl.kind === "TypeDeclaration") {
      typeDeclarations.push({
        name: decl.name,
        typeParams: Array.from(decl.typeParameters || []),
        constructors: decl.variants ? decl.variants.map((c: any) => ({
          name: c.name,
          types: c.fields ? c.fields.map(() => ({ kind: "TypeConstant", name: "Any" })) : []
        })) : []
      });
    } else if (decl.kind === "TypeAliasDeclaration") {
        if (decl.aliasedType.kind === "RecordType") {
            typeDeclarations.push({
                name: decl.name,
                typeParams: Array.from(decl.typeParameters || []),
                constructors: [{
                    name: decl.name,
                    types: decl.aliasedType.fields.map(() => ({ kind: "TypeConstant", name: "Any" }))
                }]
            });
        }
    }
  }

  // Recursive alias resolution for all declarations
  const resolveAliasesInExpr = (expr: CoreIR.Expr): CoreIR.Expr => {
      if (expr.kind === "Variable" && expr.name.includes(".")) {
          const parts = expr.name.split(".");
          const pkg = parts.slice(0, -1).join(".");
          const name = parts[parts.length - 1];
          for (const imp of ast.imports) {
              if (imp.alias && imp.alias.name === pkg) {
                  return { ...expr, name: imp.moduleName.join(".") + "." + name };
              }
          }
      }
      if (expr.kind === "Application") {
          return { ...expr, fn: resolveAliasesInExpr(expr.fn), args: expr.args.map(resolveAliasesInExpr) };
      }
      if (expr.kind === "Lambda") {
          return { ...expr, body: resolveAliasesInExpr(expr.body) };
      }
      if (expr.kind === "LetBinding") {
          return { ...expr, value: resolveAliasesInExpr(expr.value), body: resolveAliasesInExpr(expr.body) };
      }
      if (expr.kind === "IfExpr") {
          return { ...expr, condition: resolveAliasesInExpr(expr.condition), thenBranch: resolveAliasesInExpr(expr.thenBranch), elseBranch: resolveAliasesInExpr(expr.elseBranch) };
      }
      return expr;
  };

  for (const decl of declarations) {
      decl.body = resolveAliasesInExpr(decl.body);
  }

  return {
    name: Array.from(ast.name),
    declarations,
    typeDeclarations
  };
}

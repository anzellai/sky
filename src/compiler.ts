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
import { detectLiveApp } from "./live/detect.js";
import { generateLiveMain, extractRoutes, findPageType, extractNotFound } from "./live/emit-live-runtime.js";
import { writeRuntimeFiles } from "./live/runtime-files.js";
import { detectComponents } from "./live/detect-components.js";
import { buildComponentInfos, ComponentModuleInfo } from "./live/emit-component-wiring.js";

// Cache type-check results for unchanged modules (e.g., stdlib, bindings)
const _typeCheckCache = new Map<string, { filePath: string; exports: Map<string, Scheme>; result: TypeCheckResult }>();

export async function typeCheckProject(entryFile: string, virtualFile?: { path: string; content: string }) {
  const graph = await buildModuleGraph(entryFile, virtualFile);
  const moduleExports = new Map<string, Map<string, Scheme>>();
  const allDiagnostics: any[] = [];
  const moduleResults = new Map<string, TypeCheckResult>();

  if (graph.diagnostics.length > 0) {
      return { diagnostics: graph.diagnostics, exports: moduleExports, modules: graph.modules, moduleResults };
  }

  // Find the entry module's AST (not the last module — implicit modules come after entry)
  let latestModuleAst: AST.Module | undefined;
  if (virtualFile) {
    const entryAbs = path.resolve(virtualFile.path);
    for (const m of graph.modules) {
      if (path.resolve(m.filePath) === entryAbs) {
        latestModuleAst = m.moduleAst;
        break;
      }
    }
  }
  if (!latestModuleAst && graph.modules.length > 0) {
    latestModuleAst = graph.modules[graph.modules.length - 1].moduleAst;
  }

  for (const loaded of graph.modules) {
    const moduleNameStr = loaded.moduleAst.name.join(".");

    // Skip type-checking for cached unchanged modules (not the edited file)
    const isEdited = virtualFile && path.resolve(virtualFile.path) === path.resolve(loaded.filePath);
    if (!isEdited) {
      const cached = _typeCheckCache.get(moduleNameStr);
      if (cached && cached.filePath === loaded.filePath) {
        moduleExports.set(moduleNameStr, cached.exports);
        moduleResults.set(moduleNameStr, cached.result);
        continue;
      }
    }

    // Build environment from dependencies
    const importsMap = new Map<string, Scheme>();
    for (const imp of loaded.moduleAst.imports) {
      // Skip blank imports (import X as _) — side-effect only
      if (imp.alias && imp.alias.name === "_") continue;
      const depName = imp.moduleName.join(".");
      const depExports = moduleExports.get(depName);
      if (depExports) {
        // Collect specifically exposed names for selective import
        const exposedNames = new Set<string>();
        if (imp.exposing?.kind === "ExposingClause" && !imp.exposing.open) {
          for (const item of imp.exposing.items) {
            exposedNames.add((item as any).name);
          }
        }

        for (const [name, scheme] of depExports) {
          // Import unqualified if exposing (..) or exposing (name)
          if (imp.exposing?.kind === "ExposingClause" && (imp.exposing.open || exposedNames.has(name))) {
            importsMap.set(name, scheme);
          }
          if (imp.alias) {
            importsMap.set(`${imp.alias.name}.${name}`, scheme);
          }
          importsMap.set(`${depName}.${name}`, scheme);
        }
      }
    }

    // Auto-import standard library modules as qualified names (like Elm's implicit imports)
    // This makes Result.withDefault, Maybe.withDefault, List.map, etc. available everywhere
    const implicitModules = ["Sky.Core.Result", "Sky.Core.Maybe", "Sky.Core.List", "Sky.Core.String", "Sky.Core.Dict"];
    for (const modName of implicitModules) {
      const modExports = moduleExports.get(modName);
      if (modExports) {
        const shortName = modName.split(".").pop()!;
        for (const [name, scheme] of modExports) {
          const qualKey = `${shortName}.${name}`;
          if (!importsMap.has(qualKey)) {
            importsMap.set(qualKey, scheme);
          }
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
          const envScheme = typeCheck.environment.get(decl.name);
          if (envScheme) {
            myExports.set(decl.name, envScheme);
          }
        }
      }
      // Also export foreign-imported names when they're in the exposing list
      // (but skip raw Go wrapper names like Sky_github_com_...)
      if (decl.kind === "ForeignImportDeclaration" && isExposed && !decl.name.includes("Sky_") && !decl.name.includes("sky_")) {
        const envScheme = typeCheck.environment.get(decl.name);
        if (envScheme) {
          myExports.set(decl.name, envScheme);
        }
      }
    }

    // For fully exposed modules (exposing (..)), export all environment entries
    if (isFullyExposed) {
      for (const [name, scheme] of typeCheck.environment.entries()) {
        if (!myExports.has(name) && !name.includes(".") && name !== "+" && name !== "-" && name !== "*" && name !== "/" && name !== "True" && name !== "False" && name !== "()" && !name.includes("Sky_") && !name.includes("sky_")) {
          myExports.set(name, scheme);
        }
      }
    }

    // Also handle explicit exposing lists for names not covered above
    // (e.g., functions defined as wrappers over foreign imports in non-open modules)
    if (loaded.moduleAst.exposing?.kind === "ExposingClause" && !loaded.moduleAst.exposing.open) {
      for (const item of loaded.moduleAst.exposing.items) {
        const itemName = (item as any).name;
        if (itemName && !myExports.has(itemName)) {
          const envScheme = typeCheck.environment.get(itemName);
          if (envScheme) {
            myExports.set(itemName, envScheme);
          }
        }
      }
    }

    moduleExports.set(moduleNameStr, myExports);

    // Cache type-check results for non-edited modules
    if (!isEdited) {
      _typeCheckCache.set(moduleNameStr, { filePath: loaded.filePath, exports: myExports, result: typeCheck });
    }
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
        // Extract the Go package path from the .skycache file path
        // e.g., ".skycache/go/modernc.org/sqlite/bindings.skyi" → "modernc.org/sqlite"
        const cacheMatch = loaded.filePath.match(/\.skycache\/go\/(.+?)\/bindings\.skyi$/);
        if (cacheMatch) {
            allForeignPackages.add(cacheMatch[1]);
        }
    }

    // Build environment from dependencies
    const importsMap = new Map<string, Scheme>();
    for (const imp of loaded.moduleAst.imports) {
      // Skip blank imports (import X as _) — side-effect only
      if (imp.alias && imp.alias.name === "_") continue;
      const depName = imp.moduleName.join(".");
      const depExports = moduleExports.get(depName);
      if (depExports) {
        const exposedNames = new Set<string>();
        if (imp.exposing?.kind === "ExposingClause" && !imp.exposing.open) {
          for (const item of imp.exposing.items) {
            exposedNames.add((item as any).name);
          }
        }

        for (const [name, scheme] of depExports) {
          if (imp.exposing?.kind === "ExposingClause" && (imp.exposing.open || exposedNames.has(name))) {
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
      if (decl.kind === "ForeignImportDeclaration" && isExposed) {
        const envScheme = typeCheck.environment.get(decl.name);
        if (envScheme) {
          myExports.set(decl.name, envScheme);
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

    if (loaded.moduleAst.exposing?.kind === "ExposingClause" && !loaded.moduleAst.exposing.open) {
      for (const item of loaded.moduleAst.exposing.items) {
        const itemName = (item as any).name;
        if (itemName && !myExports.has(itemName)) {
          const envScheme = typeCheck.environment.get(itemName);
          if (envScheme) {
            myExports.set(itemName, envScheme);
          }
        }
      }
    }

    moduleExports.set(moduleNameStr, myExports);

    for (const b of foreignResult.bindings) {
        allForeignPackages.add(b.packageName);
    }

    // Collect blank imports (import X as _) → Go blank import _ "pkg"
    const blankImports: string[] = [];
    for (const imp of loaded.moduleAst.imports) {
        if (imp.alias && imp.alias.name === "_") {
            // Resolve the Go package path from the module name.
            // Use the same logic as the module resolver to find the correct path.
            const modParts = imp.moduleName as readonly string[];
            const possiblePaths = [
                modParts.join("/").toLowerCase(),
                modParts.length >= 2 ? (modParts[0] + "." + modParts[1] + "/" + modParts.slice(2).join("/")).toLowerCase() : null,
            ].filter(Boolean) as string[];
            const projectRoot = path.dirname(loaded.filePath.replace(/[/\\]src[/\\].*$/, ""));
            for (const p of possiblePaths) {
                const cachePath = path.join(projectRoot, ".skycache", "go", p, "bindings.skyi");
                if (fs.existsSync(cachePath)) {
                    blankImports.push(p);
                    break;
                }
            }
            // Fallback: if no cache found, use the domain.tld/path form
            if (blankImports.length === 0 && possiblePaths.length > 1) {
                blankImports.push(possiblePaths[possiblePaths.length - 1]);
            }
        }
    }

    // Basic AST to CoreIR conversion
    let coreModule: CoreIR.Module = astToCore(loaded.moduleAst, typeCheck, foreignResult, importsMap);
    const usage = analyzeUsage(coreModule);
    coreModule = eliminateDeadBindings(coreModule, usage);

    // Collect the set of modules this file actually imports (for resolving ambiguous names)
    const importedModules = new Set<string>();
    for (const imp of loaded.moduleAst.imports) {
        importedModules.add(imp.moduleName.join("."));
    }

    // Lower to GoIR
    const goPkg = lowerModule(coreModule, moduleExports, allForeignModules, importedModules);

    // Inject blank imports into the Go package
    for (const blankPkg of blankImports) {
        goPkg.imports.push({ path: blankPkg, alias: "_" });
    }

    // Emit Go code
    const goCode = emitGoPackage(goPkg);

    const outPath = computeOutputFile(loaded.moduleAst.name, outDir);
    fs.mkdirSync(path.dirname(outPath), { recursive: true });
    fs.writeFileSync(outPath, goCode);
  }

  // Sky.Live: Detect if this is a Live app and generate server code
  let isLiveApp = false;
  const mainModuleLoaded = graph.modules.find(m => m.moduleAst.name.length === 1 && m.moduleAst.name[0] === "Main");
  if (mainModuleLoaded) {
    const liveDetection = detectLiveApp(mainModuleLoaded.moduleAst);
    if (liveDetection.isLive) {
      isLiveApp = true;
      console.log("Detected Sky.Live application");

      // Extract route definitions from the main module
      const routes = extractRoutes(mainModuleLoaded.moduleAst);
      const pageTypeDecl = findPageType(mainModuleLoaded.moduleAst);

      // Read port from sky.toml if available
      const { readManifest } = await import("./pkg/manifest.js");
      const manifest = readManifest();
      const port = (manifest as any)?.live?.port || 4000;

      // Read session store config from sky.toml
      const storeType = (manifest as any)?.live?.session?.store || "memory";
      const storePath = (manifest as any)?.live?.session?.path || (manifest as any)?.live?.session?.url || "";
      const inputMode = (manifest as any)?.live?.input || "debounce";
      const pollInterval = (manifest as any)?.live?.poll_interval || 0;

      // Extract notFound page constructor
      const notFoundPage = extractNotFound(mainModuleLoaded.moduleAst) || "";

      // Detect component bindings
      const componentBindings = detectComponents(mainModuleLoaded.moduleAst, moduleExports);
      let componentInfos: ComponentModuleInfo[] = [];
      if (componentBindings.length > 0) {
        componentInfos = buildComponentInfos(componentBindings, graph.modules, outDir);
        for (const info of componentInfos) {
          console.log(`  Component: ${info.binding.fieldName} : ${info.binding.typeName} → ${info.binding.msgWrapperName} (auto-wired)`);
        }
      }

      // Generate the Live main.go (replaces normal main.go)
      const liveMainCode = generateLiveMain(
        mainModuleLoaded.moduleAst,
        liveDetection.msgType,
        pageTypeDecl,
        routes,
        port,
        storeType,
        storePath,
        notFoundPage,
        componentInfos,
        inputMode,
        pollInterval
      );

      // Read the existing main.go to preserve the compiled functions
      const existingMainPath = path.join(outDir, "main.go");
      let existingMain = "";
      if (fs.existsSync(existingMainPath)) {
        existingMain = fs.readFileSync(existingMainPath, "utf8");
      }

      // Merge: keep existing functions, replace main() and add Live imports/functions
      const mergedMain = mergeLiveMain(existingMain, liveMainCode);
      fs.writeFileSync(existingMainPath, mergedMain);

      // Write the skylive_rt Go runtime package into dist/
      writeRuntimeFiles(outDir);
    }
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

  return { diagnostics: [], isLiveApp };
}

/**
 * Merge the existing compiled main.go with the Live-generated main.go.
 * Keeps existing function declarations (Init, Update, View, etc.)
 * but replaces the main() function and adds Live imports.
 */
function mergeLiveMain(existingMain: string, liveMain: string): string {
  if (!existingMain) return liveMain;

  // Extract the existing functions (everything except package, imports, and main func)
  const lines = existingMain.split("\n");
  const existingFuncs: string[] = [];
  let inMain = false;
  let braceDepth = 0;
  let skipImports = false;

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    const trimmed = line.trim();

    // Skip package declaration
    if (trimmed.startsWith("package ")) continue;

    // Skip import block
    if (trimmed === "import (") {
      skipImports = true;
      continue;
    }
    if (skipImports) {
      if (trimmed === ")") {
        skipImports = false;
      }
      continue;
    }
    if (trimmed.startsWith("import ")) continue;

    // Skip the existing main() function
    if (trimmed.startsWith("func main()") || trimmed === "func main() {") {
      inMain = true;
      braceDepth = 0;
    }
    if (inMain) {
      for (const ch of line) {
        if (ch === "{") braceDepth++;
        if (ch === "}") braceDepth--;
      }
      if (braceDepth <= 0 && line.includes("}")) {
        inMain = false;
      }
      continue;
    }

    existingFuncs.push(line);
  }

  // Extract Live imports and main function from the generated code
  const liveLines = liveMain.split("\n");
  const liveImports: string[] = [];
  const liveFuncs: string[] = [];
  let inLiveImport = false;

  for (const line of liveLines) {
    const trimmed = line.trim();
    if (trimmed.startsWith("package ")) continue;
    if (trimmed === "import (") {
      inLiveImport = true;
      continue;
    }
    if (inLiveImport) {
      if (trimmed === ")") {
        inLiveImport = false;
        continue;
      }
      liveImports.push(trimmed);
      continue;
    }
    liveFuncs.push(line);
  }

  // Collect all unique imports from both files
  const existingImportSet = new Set<string>();
  const importBlock = existingMain.match(/import \(([\s\S]*?)\)/);
  if (importBlock) {
    for (const line of importBlock[1].split("\n")) {
      const trimmed = line.trim();
      if (trimmed) existingImportSet.add(trimmed);
    }
  }
  for (const imp of liveImports) {
    existingImportSet.add(imp);
  }

  // Build the function body to check which imports are actually used
  const funcBody = existingFuncs.join("\n") + "\n" + liveFuncs.join("\n");

  // Filter out unused imports
  const usedImports = new Set<string>();
  for (const imp of existingImportSet) {
    // Extract the local alias or package name
    const aliasMatch = imp.match(/^(\w+)\s+".*"/);
    const plainMatch = imp.match(/".*\/(\w+)"/);
    const alias = aliasMatch ? aliasMatch[1] : (plainMatch ? plainMatch[1] : null);

    if (alias && funcBody.includes(alias + ".")) {
      usedImports.add(imp);
    } else if (alias && funcBody.includes(alias + "{")) {
      usedImports.add(imp);
    } else if (!alias) {
      // Keep imports we can't analyze
      usedImports.add(imp);
    }
    // Also keep standard library imports that might be used without prefix
    if (imp.includes('"encoding/json"') || imp.includes('"fmt"') ||
        imp.includes('"time"') || imp.includes('"log"') ||
        imp.includes('"net/http"') || imp.includes('"strings"')) {
      if (funcBody.includes("json.") || funcBody.includes("fmt.") ||
          funcBody.includes("time.") || funcBody.includes("log.") ||
          funcBody.includes("http.") || funcBody.includes("strings.")) {
        usedImports.add(imp);
      }
    }
  }

  // Build merged output
  let merged = "package main\n\nimport (\n";
  for (const imp of usedImports) {
    merged += `\t${imp}\n`;
  }
  merged += ")\n\n";
  merged += existingFuncs.join("\n");
  merged += "\n\n";
  merged += liveFuncs.join("\n");

  return merged;
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
          if (param.pattern.kind === "VariablePattern") {
            lambdaBody = {
              kind: "Lambda",
              params: [param.pattern.name],
              body: lambdaBody,
              type: { kind: "TypeConstant", name: "Any" }
            };
          } else if (param.pattern.kind === "TuplePattern") {
            // Desugar tuple destructuring: \(a, b) -> body  =>  \__tup -> case __tup of (a, b) -> body
            const syntheticName = `__tup${i}`;
            const pat: CoreIR.Pattern = {
              kind: "ConstructorPattern",
              name: "Tuple" + param.pattern.items.length,
              args: param.pattern.items.map((p: AST.Pattern) => convertPattern(p))
            };
            lambdaBody = {
              kind: "Lambda",
              params: [syntheticName],
              body: {
                kind: "Match",
                expr: { kind: "Variable", name: syntheticName, type: { kind: "TypeConstant", name: "Any" } },
                cases: [{ pattern: pat, body: lambdaBody }],
                type: { kind: "TypeConstant", name: "Any" }
              },
              type: { kind: "TypeConstant", name: "Any" }
            };
          } else {
            lambdaBody = {
              kind: "Lambda",
              params: ["_"],
              body: lambdaBody,
              type: { kind: "TypeConstant", name: "Any" }
            };
          }
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
        if (expr.operator === "++") {
          // Check if operands are lists to determine String vs List concatenation
          const leftConverted = convertExpr(expr.left);
          const rightConverted = convertExpr(expr.right);
          // Detect list concatenation from: list literals, or operand types from type checker
          const isListExpr = (e: CoreIR.Expr) => e.kind === "ListExpr";
          const isListType = (t: Type | undefined) =>
            t?.kind === "TypeApplication" &&
            t.constructor.kind === "TypeConstant" &&
            t.constructor.name === "List";
          const getNodeType = (astExpr: AST.Expression) => {
            if (astExpr.span) {
              return typeCheck.nodeTypes?.get(`${astExpr.span.start.line}:${astExpr.span.start.column}`);
            }
            return undefined;
          };
          const leftNodeType = getNodeType(expr.left);
          const rightNodeType = getNodeType(expr.right);
          const isListConcat = isListExpr(leftConverted) || isListExpr(rightConverted) ||
            isListType(leftConverted.type) || isListType(rightConverted.type) ||
            isListType(leftNodeType) || isListType(rightNodeType);
          if (isListConcat) {
            const elemType: Type = { kind: "TypeConstant", name: "Any" };
            retType = { kind: "TypeApplication", constructor: { kind: "TypeConstant", name: "List" }, arguments: [elemType] };
          } else {
            retType = { kind: "TypeConstant", name: "String" };
          }
          return {
            kind: "Application",
            fn: { kind: "Variable", name: expr.operator, type: { kind: "TypeConstant", name: "Any" } },
            args: [leftConverted, rightConverted],
            type: retType
          };
        }
        if (["+", "-", "*", "/"].includes(expr.operator)) {
          retType = { kind: "TypeConstant", name: "Int" };
        }

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
        if (paramPattern.kind === "VariablePattern") {
          bodyExpr = {
            kind: "Lambda",
            params: [paramPattern.name],
            body: bodyExpr,
            type: { kind: "TypeConstant", name: "Any" }
          };
        } else if (paramPattern.kind === "TuplePattern") {
          // Desugar tuple destructuring: foo (a, b) = body  =>  foo __tup = case __tup of (a, b) -> body
          const syntheticName = `__tup${i}`;
          const pat: CoreIR.Pattern = {
            kind: "ConstructorPattern",
            name: "Tuple" + paramPattern.items.length,
            args: paramPattern.items.map((p: AST.Pattern) => convertPattern(p))
          };
          bodyExpr = {
            kind: "Lambda",
            params: [syntheticName],
            body: {
              kind: "Match",
              expr: { kind: "Variable", name: syntheticName, type: { kind: "TypeConstant", name: "Any" } },
              cases: [{ pattern: pat, body: bodyExpr }],
              type: { kind: "TypeConstant", name: "Any" }
            },
            type: { kind: "TypeConstant", name: "Any" }
          };
        } else {
          bodyExpr = {
            kind: "Lambda",
            params: ["_"],
            body: bodyExpr,
            type: { kind: "TypeConstant", name: "Any" }
          };
        }
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

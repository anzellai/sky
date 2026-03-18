// src/modules/resolver.ts
import fs from "fs";
import path from "path";
import { lex } from "../lexer/lexer.js";
import { parse } from "../parser/parser.js";
import { filterLayout } from "../parser/filter-layout.js";
import * as AST from "../ast/ast.js";
import { getDirname, getFilename } from "../utils/path.js";
import { isVirtualAsset, readVirtualAsset } from "../utils/assets.js";
import { readManifest, SkyManifest } from "../pkg/manifest.js";

const __filename = getFilename(import.meta.url);
const __dirname = getDirname(import.meta.url);

export interface LoadedModule {
  filePath: string;
  moduleAst: AST.Module;
}

export interface ModuleGraph {
  modules: LoadedModule[];
  diagnostics: string[];
}

export async function buildModuleGraph(
  entryFile: string,
  virtualFile?: { path: string; content: string },
): Promise<ModuleGraph> {
  const loaded = new Map<string, LoadedModule>();
  const visiting = new Set<string>();
  const ordered: LoadedModule[] = [];
  const diagnostics: string[] = [];

  const entryAbs = path.resolve(entryFile);
  const srcRoot = findSourceRoot(entryAbs);

  await loadModule(
    entryAbs,
    loaded,
    visiting,
    ordered,
    diagnostics,
    srcRoot,
    virtualFile,
  );

  // Ensure implicit stdlib modules are always in the graph.
  // This is needed for the LSP which may open a single file (not Main.sky)
  // that uses qualified calls like Result.withDefault, Maybe.map, etc.
  const implicitModules = ["Sky.Core.Result", "Sky.Core.Maybe", "Sky.Core.List", "Sky.Core.String", "Sky.Core.Dict"];
  for (const modName of implicitModules) {
    const modFile = resolveModuleToFile(srcRoot, modName.split("."));
    if (modFile) {
      const modAbs = modFile.startsWith("virtual:") ? modFile : path.resolve(modFile);
      if (!loaded.has(modAbs)) {
        await loadModule(modAbs, loaded, visiting, ordered, diagnostics, srcRoot, virtualFile);
      }
    }
  }

  return {
    modules: ordered,
    diagnostics,
  };

  async function loadModule(
    abs: string,
    loaded: Map<string, LoadedModule>,
    visiting: Set<string>,
    ordered: LoadedModule[],
    diagnostics: string[],
    srcRoot: string,
    virtualFile?: { path: string; content: string },
  ): Promise<void> {
    if (loaded.has(abs)) return;
    if (visiting.has(abs)) {
      diagnostics.push(`Circular dependency detected: ${abs}`);
      return;
    }

    visiting.add(abs);

    let source: string;
    try {
      if (virtualFile && path.resolve(virtualFile.path) === abs) {
        source = virtualFile.content;
      } else if (abs.startsWith("virtual:")) {
        const virtualPath = abs.substring("virtual:".length);
        source = readVirtualAsset(virtualPath) || "";
      } else {
        source = fs.readFileSync(abs, "utf8");
      }
    } catch {
      diagnostics.push(`Cannot read file: ${abs}`);
      visiting.delete(abs);
      return;
    }

    const lexResult = lex(source, abs);

    if (lexResult.diagnostics.length > 0) {
      for (const d of lexResult.diagnostics) {
        diagnostics.push(formatDiagnostic(d.message, d.span?.start.line, d.span?.start.column, abs));
      }
      visiting.delete(abs);
      return;
    }

    let moduleAst: AST.Module;
    try {
      moduleAst = parse(filterLayout(lexResult.tokens));
    } catch (error: any) {
      diagnostics.push(`Parse error in ${abs}: ${error.message}`);
      visiting.delete(abs);
      return;
    }

    const imports = moduleAst.imports.map((imp: any) => imp.moduleName);

    for (const importParts of imports) {
      let importFile = resolveModuleToFile(srcRoot, importParts);

      if (!importFile) {
        // Try Go package resolution via .skycache
        // PascalCase parts like ["Github", "Com", "Google", "Uuid"]
        // need to be mapped back to potentially "github.com/google/uuid"
        
        const projectRoot = path.dirname(srcRoot);
        
        const possiblePackages = [
            importParts.join("/").toLowerCase(),
            // Common pattern: First two parts are often domain.tld (github.com)
            importParts.length >= 2 ? (importParts[0] + "." + importParts[1] + "/" + importParts.slice(2).join("/")).toLowerCase() : null
        ].filter(Boolean) as string[];

        for (const goPackage of possiblePackages) {
            const goCachePath = path.join(projectRoot, ".skycache", "go", goPackage, "bindings.skyi");
            if (fs.existsSync(goCachePath)) {
                importFile = goCachePath;
                break;
            }
        }
      }

      if (!importFile) {
        diagnostics.push(
          `Cannot resolve import ${importParts.join(".")} from ${moduleAst.name.join(".")} (${abs})`,
        );
        continue;
      }

      await loadModule(
        importFile.startsWith("virtual:") ? importFile : path.resolve(importFile),
        loaded,
        visiting,
        ordered,
        diagnostics,
        srcRoot,
      );
    }

    visiting.delete(abs);

    const loadedModule: LoadedModule = {
      filePath: abs,
      moduleAst,
    };

    loaded.set(abs, loadedModule);
    ordered.push(loadedModule);
  }
}

function resolveModuleToFile(
  srcRoot: string,
  moduleName: readonly string[],
): string | undefined {
  const projectRoot = path.dirname(srcRoot);

  // 1. Project Source
  const filePath = path.join(srcRoot, ...moduleName) + ".sky";
  if (fs.existsSync(filePath)) return filePath;

  // 2. .skydeps — scan installed Sky packages, respect source.root and [lib].exposing
  const skydepsPath = path.join(projectRoot, ".skydeps");
  if (fs.existsSync(skydepsPath)) {
    const orgs = fs.readdirSync(skydepsPath);
    for (const org of orgs) {
      if (org.startsWith(".")) continue;
      const orgPath = path.join(skydepsPath, org);
      if (!fs.statSync(orgPath).isDirectory()) continue;
      const repos = fs.readdirSync(orgPath);
      for (const repo of repos) {
        const pkgDir = path.join(orgPath, repo);
        const depManifest = readDepManifest(pkgDir);
        const depSrcRoot = depManifest?.source?.root || "src";
        const pkgSrc = path.join(pkgDir, depSrcRoot);
        const depFilePath = path.join(pkgSrc, ...moduleName) + ".sky";
        if (fs.existsSync(depFilePath)) {
          // Enforce [lib].exposing — if the package declares exposed modules,
          // only those are importable. No [lib] = all modules are internal.
          if (depManifest?.lib?.exposing) {
            const moduleNameStr = moduleName.join(".");
            if (!depManifest.lib.exposing.includes(moduleNameStr)) {
              return undefined; // Module exists but is not publicly exposed
            }
          } else {
            // No [lib] section — package doesn't expose any modules
            return undefined;
          }
          return depFilePath;
        }
      }
    }
  }

  // 3. Stdlib (Virtual or bundled)
  const virtualPath = `stdlib/${moduleName.join("/")}.sky`;
  if (isVirtualAsset(virtualPath)) {
    return `virtual:${virtualPath}`;
  }

  if (moduleName[0] === "Sky" && moduleName[1] === "Core") {
    return path.join(__dirname, "../src/stdlib", ...moduleName) + ".sky";
  }

  if (moduleName[0] === "Sky" && moduleName[1] === "Interop") {
    return path.join(__dirname, "../src/stdlib/Sky/Interop.sky");
  }

  if (moduleName[0] === "Std") {
    return path.join(__dirname, "../src/stdlib", ...moduleName) + ".sky";
  }

  if (moduleName.length === 1 && moduleName[0] === "Ui") {
    return path.join(__dirname, "../src/stdlib/Ui.sky");
  }

  return undefined;
}

function findSourceRoot(entryAbs: string): string {
  const parts = entryAbs.split(path.sep);
  const srcIndex = parts.lastIndexOf("src");

  if (srcIndex >= 0) {
    return parts.slice(0, srcIndex + 1).join(path.sep);
  }

  return path.dirname(entryAbs);
}

function formatDiagnostic(message: string, line?: number, column?: number, file?: string): string {
  let res = "";
  if (file) res += `${file}:`;
  if (line) res += `${line}:`;
  if (column) res += `${column}: `;
  res += message;
  return res;
}

// Cache dep manifests to avoid re-reading sky.toml for every import resolution
const depManifestCache = new Map<string, SkyManifest | null>();

function readDepManifest(pkgDir: string): SkyManifest | null {
  if (depManifestCache.has(pkgDir)) return depManifestCache.get(pkgDir)!;
  const manifestPath = path.join(pkgDir, "sky.toml");
  const result = fs.existsSync(manifestPath) ? readManifest(manifestPath) : null;
  depManifestCache.set(pkgDir, result);
  return result;
}

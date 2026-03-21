// src/modules/resolver.ts
import fs from "fs";
import path from "path";
import { lex } from "../lexer/lexer.js";
import { parse } from "../parser/parser.js";
import { filterLayout } from "../parser/filter-layout.js";
import * as AST from "../ast/ast.js";
import { getDirname, getFilename, skyImportToGoPaths } from "../utils/path.js";
import { isVirtualAsset, readVirtualAsset } from "../utils/assets.js";
import { readManifest, SkyManifest } from "../pkg/manifest.js";
import { generateForeignBindings } from "../interop/go/generate-bindings.js";
import { execSync } from "child_process";

const __filename = getFilename(import.meta.url);
const __dirname = getDirname(import.meta.url);

// Cache parsed ASTs for non-virtual files (especially large .skyi bindings)
// Key: absolute path, Value: { mtime, ast }
const _parseCache = new Map<string, { mtime: number; ast: AST.Module }>();

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

  // Load implicit stdlib modules BEFORE the entry module so that
  // Just/Nothing/Result constructors and qualified names (List.map, etc.)
  // are available when type-checking the entry and its dependencies.
  // This is critical for the LSP which opens individual files as entry points.
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

  await loadModule(
    entryAbs,
    loaded,
    visiting,
    ordered,
    diagnostics,
    srcRoot,
    virtualFile,
  );

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

    const isVirtual = abs.startsWith("virtual:");
    const isEdited = virtualFile && path.resolve(virtualFile.path) === abs;

    // Check parse cache for non-virtual, non-edited files (e.g. .skyi bindings)
    if (!isVirtual && !isEdited) {
      try {
        const stat = fs.statSync(abs);
        const cached = _parseCache.get(abs);
        if (cached && cached.mtime === stat.mtimeMs) {
          const mod: LoadedModule = { filePath: abs, moduleAst: cached.ast };
          loaded.set(abs, mod);
          // Still need to recurse into imports
          const imports = cached.ast.imports.map((imp: any) => imp.moduleName);
          for (const imp of imports) {
            const resolved = resolveModuleToFile(srcRoot, imp);
            if (resolved) {
              const depAbs = resolved.startsWith("virtual:") ? resolved : path.resolve(resolved);
              await loadModule(depAbs, loaded, visiting, ordered, diagnostics, srcRoot, virtualFile);
            }
          }
          ordered.push(mod);
          visiting.delete(abs);
          return;
        }
      } catch {}
    }

    let source: string;
    try {
      if (isEdited) {
        source = virtualFile!.content;
      } else if (isVirtual) {
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

    // Cache the parsed AST for non-virtual files
    if (!isVirtual && !isEdited) {
      try {
        const stat = fs.statSync(abs);
        _parseCache.set(abs, { mtime: stat.mtimeMs, ast: moduleAst });
      } catch {}
    }

    const imports = moduleAst.imports.map((imp: any) => imp.moduleName);

    for (const importParts of imports) {
      let importFile = resolveModuleToFile(srcRoot, importParts);

      if (!importFile) {
        // Try Go package resolution via .skycache
        // PascalCase parts like ["Github", "Com", "Google", "Uuid"]
        // need to be mapped back to potentially "github.com/google/uuid"

        const projectRoot = path.dirname(srcRoot);

        const possiblePackages = skyImportToGoPaths(importParts);

        for (const goPackage of possiblePackages) {
            const goCachePath = path.join(projectRoot, ".skycache", "go", goPackage, "bindings.skyi");
            if (fs.existsSync(goCachePath)) {
                importFile = goCachePath;
                break;
            }

            // Lazy subpackage resolution: if bindings don't exist but a parent
            // module is installed (has a go.mod entry), auto-generate bindings.
            // e.g. "fyne.io/fyne/v2/widget" when "fyne.io/fyne/v2" was added.
            const goModPath = path.join(projectRoot, ".skycache", "gomod", "go.mod");
            if (fs.existsSync(goModPath)) {
                const goMod = fs.readFileSync(goModPath, "utf8");
                // Check if any parent path is a known module
                const parts = goPackage.split("/");
                let parentFound = false;
                for (let len = parts.length - 1; len >= 2; len--) {
                    const parentPkg = parts.slice(0, len).join("/");
                    if (goMod.includes(parentPkg)) {
                        parentFound = true;
                        break;
                    }
                }
                if (parentFound) {
                    try {
                        // Resolve transitive deps for this subpackage before inspection
                        const goModDir = path.join(projectRoot, ".skycache", "gomod");
                        execSync(`go get ${goPackage}`, { cwd: goModDir, stdio: "ignore" });

                        const result = await generateForeignBindings(goPackage, []);
                        if (result.skyiContent) {
                            const cacheDir = path.join(projectRoot, ".skycache", "go", goPackage);
                            fs.mkdirSync(cacheDir, { recursive: true });
                            fs.writeFileSync(path.join(cacheDir, "bindings.skyi"), result.skyiContent);
                            importFile = path.join(cacheDir, "bindings.skyi");
                        }
                    } catch {}
                }
                if (importFile) break;
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
  // Packages may be nested at any depth (e.g., github.com/org/repo → 3 levels).
  // Recursively find directories containing sky.toml.
  const skydepsPath = path.join(projectRoot, ".skydeps");
  if (fs.existsSync(skydepsPath)) {
    const pkgDirs = findSkydepPackages(skydepsPath);
    for (const pkgDir of pkgDirs) {
      const depManifest = readDepManifest(pkgDir);
      const depSrcRoot = depManifest?.source?.root || "src";
      const pkgSrc = path.join(pkgDir, depSrcRoot);

      // Derive the import prefix from the package's path relative to .skydeps.
      // e.g., .skydeps/github.com/anzellai/sky-tailwind → ["Github", "Com", "Anzellai", "SkyTailwind"]
      // Dots in path segments (like github.com) become separate parts (Github, Com).
      // Hyphens/underscores within a segment are joined (sky-tailwind → SkyTailwind).
      const relPkgPath = path.relative(skydepsPath, pkgDir);
      const importPrefix: string[] = [];
      for (const seg of relPkgPath.split(path.sep)) {
        for (const dotPart of seg.split(".")) {
          importPrefix.push(dotPart.split(/[-_]/).map((w: string) => w.charAt(0).toUpperCase() + w.slice(1)).join(""));
        }
      }

      // Derive PascalCase package name for prefix matching
      const pkgName = depManifest?.name || "";
      const pkgPascal = pkgName.split(/[-_.]/).map((w: string) => w.charAt(0).toUpperCase() + w.slice(1)).join("");

      // Try multiple resolution strategies:
      // 1. Direct path: import Tailwind → src/Tailwind.sky
      const depFilePath = path.join(pkgSrc, ...moduleName) + ".sky";

      // 2. Strip full URL prefix: import Github.Com.Anzellai.SkyTailwind.Tailwind → src/Tailwind.sky
      let strippedPath: string | undefined;
      if (moduleName.length > importPrefix.length) {
        const prefixMatches = importPrefix.every((seg: string, i: number) => seg === moduleName[i]);
        if (prefixMatches) {
          const stripped = moduleName.slice(importPrefix.length);
          strippedPath = path.join(pkgSrc, ...stripped) + ".sky";
        }
      }

      // 3. Strip PascalCase package name prefix: import SkyTailwind.Tailwind → src/Tailwind.sky
      let pkgPrefixPath: string | undefined;
      if (pkgPascal && moduleName.length > 1 && moduleName[0] === pkgPascal) {
        const stripped = moduleName.slice(1);
        pkgPrefixPath = path.join(pkgSrc, ...stripped) + ".sky";
      }

      const resolvedPath = fs.existsSync(depFilePath) ? depFilePath :
                           (strippedPath && fs.existsSync(strippedPath)) ? strippedPath :
                           (pkgPrefixPath && fs.existsSync(pkgPrefixPath)) ? pkgPrefixPath : undefined;
      if (resolvedPath) {
        // Enforce [lib].exposing — if the package declares exposed modules,
        // only those are importable. No [lib] = all modules are internal.
        if (depManifest?.lib?.exposing) {
          const moduleNameStr = moduleName.join(".");
          const strippedNameStr = strippedPath ? moduleName.slice(importPrefix.length).join(".") : "";
          const pkgStrippedNameStr = pkgPrefixPath ? moduleName.slice(1).join(".") : "";
          // Build the prefixed module name: e.g., "SkyTailwind.Tailwind" for root
          const prefixedName = strippedNameStr ? `${pkgPascal}.${strippedNameStr}` : pkgPascal;

          const isExposed = depManifest.lib.exposing.includes(moduleNameStr) ||
                            depManifest.lib.exposing.includes(strippedNameStr) ||
                            depManifest.lib.exposing.includes(pkgStrippedNameStr) ||
                            depManifest.lib.exposing.includes(prefixedName);
          if (!isExposed) {
            return undefined; // Module exists but is not publicly exposed
          }
        } else {
          // No [lib] section — package doesn't expose any modules
          return undefined;
        }
        return resolvedPath;
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

// Recursively find package directories (containing sky.toml) in .skydeps.
// Handles arbitrary nesting depth (e.g., github.com/org/repo = 3 levels).
function findSkydepPackages(dir: string): string[] {
  const results: string[] = [];
  if (fs.existsSync(path.join(dir, "sky.toml"))) {
    results.push(dir);
    return results; // Don't recurse into packages
  }
  try {
    for (const entry of fs.readdirSync(dir)) {
      if (entry.startsWith(".")) continue;
      const full = path.join(dir, entry);
      if (fs.statSync(full).isDirectory()) {
        results.push(...findSkydepPackages(full));
      }
    }
  } catch {}
  return results;
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

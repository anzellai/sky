// src/modules/resolver.ts
import fs from "fs";
import path from "path";
import { lex } from "../lexer/lexer.js";
import { parse } from "../parser/parser.js";
import { filterLayout } from "../parser/filter-layout.js";
import * as AST from "../ast/ast.js";
import { getDirname, getFilename } from "../utils/path.js";
import { isVirtualAsset, readVirtualAsset } from "../utils/assets.js";

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
        const goPackage = importParts.join("/").toLowerCase();
        const goCachePath = path.join(".skycache", "go", goPackage, "bindings.skyi");
        if (fs.existsSync(goCachePath)) {
          importFile = goCachePath;
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
  // 1. Try Virtual Assets (Embedded Stdlib)
  const virtualPath = `stdlib/${moduleName.join("/")}.sky`;
  if (isVirtualAsset(virtualPath)) {
    return `virtual:${virtualPath}`;
  }

  if (moduleName[0] === "Sky" && moduleName[1] === "Core") {
    // Read from the bundled stdlib inside the compiler
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

  const filePath = path.join(srcRoot, ...moduleName) + ".sky";
  return fs.existsSync(filePath) ? filePath : undefined;
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

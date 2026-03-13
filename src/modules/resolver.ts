// src/module-graph.ts
// Build a module dependency graph for Sky source files.

import fs from "fs";
import path from "path";
import { fileURLToPath } from "url";

import { lex } from "../lexer/lexer.js";
import { parse } from "../parser/parser.js";
import { filterLayout } from "../parser/filter-layout.js";
import * as AST from "../ast/ast.js";
import { getDirname, getFilename } from "../utils/path.js";

const __filename = getFilename(import.meta.url);
const __dirname = getDirname(import.meta.url);

export interface LoadedModule {
  readonly filePath: string;
  readonly moduleAst: AST.Module;
}

export interface ModuleGraphResult {
  readonly modules: readonly LoadedModule[];
  readonly diagnostics: readonly string[];
}

export interface VirtualFile {
  readonly path: string;
  readonly content: string;
}

export async function buildModuleGraph(entryFile: string, virtualFile?: VirtualFile): Promise<ModuleGraphResult> {
  const diagnostics: string[] = [];
  const loaded = new Map<string, LoadedModule>();
  const visiting = new Set<string>();
  const ordered: LoadedModule[] = [];

  const entryAbs = path.resolve(entryFile);
  const srcRoot = findSourceRoot(entryAbs);

  await visit(entryAbs);

  return {
    modules: ordered,
    diagnostics,
  };

  async function visit(filePath: string): Promise<void> {
    const abs = path.resolve(filePath);

    if (loaded.has(abs)) {
      return;
    }

    if (visiting.has(abs)) {
      diagnostics.push(`Import cycle detected involving ${abs}`);
      return;
    }

    visiting.add(abs);

    let source: string;
    try {
      if (virtualFile && path.resolve(virtualFile.path) === abs) {
        source = virtualFile.content;
      } else {
        // Try virtual assets first for internal modules
        const stdlibIndex = abs.indexOf("stdlib/");
        const runtimeIndex = abs.indexOf("runtime/");
        
        let relPath: string | undefined;
        if (stdlibIndex !== -1) {
          relPath = abs.substring(stdlibIndex);
        } else if (runtimeIndex !== -1) {
          relPath = abs.substring(runtimeIndex);
        }

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
    } catch (error) {
      diagnostics.push(
        error instanceof Error
          ? `${abs}: ${error.message}`
          : `${abs}: ${String(error)}`,
      );
      visiting.delete(abs);
      return;
    }

    const imports = moduleAst.imports.map((imp) => imp.moduleName);

    for (const importParts of imports) {
      let importFile = resolveModuleToFile(srcRoot, importParts);

      if (!importFile) {
        // Mock Go resolution
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

      await visit(importFile);
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

function formatDiagnostic(
  message: string,
  line?: number,
  column?: number,
  filePath?: string,
): string {
  if (line === undefined || column === undefined) {
    return filePath ? `${filePath}: ${message}` : message;
  }

  return `${filePath ?? "<unknown>"}:${line}:${column}: ${message}`;
}

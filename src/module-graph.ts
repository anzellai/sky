// src/module-graph.ts
// Sky module graph builder

import fs from "fs";
import path from "path";

import { lex } from "./lexer.js";
import { parse } from "./parser.js";
import { filterLayout } from "./parser/filter-layout.js";
import * as AST from "./ast.js";

export interface GraphNode {
  readonly filePath: string;
  readonly moduleAst: AST.Module;
  readonly imports: readonly string[];
}

export interface ModuleGraphResult {
  readonly nodes: readonly GraphNode[];
  readonly diagnostics: readonly string[];
}

export function buildModuleGraph(entryFile: string): ModuleGraphResult {
  const diagnostics: string[] = [];
  const nodes = new Map<string, GraphNode>();
  const visiting = new Set<string>();
  const visited = new Set<string>();
  const ordered: GraphNode[] = [];

  const entryAbs = path.resolve(entryFile);
  const srcRoot = findSourceRoot(entryAbs);

  visit(entryAbs);

  return {
    nodes: ordered,
    diagnostics,
  };

  function visit(filePath: string): void {
    const abs = path.resolve(filePath);

    if (visited.has(abs)) return;

    if (visiting.has(abs)) {
      diagnostics.push(`Import cycle detected involving: ${abs}`);
      return;
    }

    visiting.add(abs);

    const source = readFileSafe(abs);
    if (source === undefined) {
      visiting.delete(abs);
      return;
    }

    const lexResult = lex(source, abs);
    if (lexResult.diagnostics.length > 0) {
      for (const d of lexResult.diagnostics) {
        diagnostics.push(`${d.message} at ${d.span.start.line}:${d.span.start.column}`);
      }
      visiting.delete(abs);
      return;
    }

    let moduleAst: AST.Module;
    try {
      moduleAst = parse(filterLayout(lexResult.tokens));
    } catch (err) {
      diagnostics.push(err instanceof Error ? err.message : String(err));
      visiting.delete(abs);
      return;
    }

    const imports = moduleAst.imports.map((imp) => imp.moduleName.join("."));

    const node: GraphNode = {
      filePath: abs,
      moduleAst,
      imports,
    };

    nodes.set(abs, node);

    for (const importName of imports) {
      const importFile = resolveImportToFile(srcRoot, importName);
      if (!importFile) {
        diagnostics.push(`Cannot resolve import ${importName} from ${abs}`);
        continue;
      }
      visit(importFile);
    }

    visiting.delete(abs);
    visited.add(abs);
    ordered.push(node);
  }

  function readFileSafe(filePath: string): string | undefined {
    try {
      return fs.readFileSync(filePath, "utf8");
    } catch {
      diagnostics.push(`Cannot read file: ${filePath}`);
      return undefined;
    }
  }
}

function resolveImportToFile(srcRoot: string, moduleName: string): string | undefined {
  const candidate = path.join(srcRoot, ...moduleName.split(".")) + ".sky";
  return fs.existsSync(candidate) ? candidate : undefined;
}

function findSourceRoot(entryAbs: string): string {
  const parts = entryAbs.split(path.sep);
  const srcIndex = parts.lastIndexOf("src");

  if (srcIndex >= 0) {
    return parts.slice(0, srcIndex + 1).join(path.sep);
  }

  return path.dirname(entryAbs);
}
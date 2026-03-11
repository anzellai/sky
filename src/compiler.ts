// src/compiler.ts
// Sky compiler pipeline with module graph support.

import fs from "fs";
import path from "path";

import { emitModule } from "./codegen/js-emitter.js";
import { checkModule } from "./type-system/checker.js";
import { collectForeignImports } from "./ffi/collect-foreign.js";
import { buildModuleGraph } from "./module-graph.js";

export interface CompileResult {
  readonly diagnostics: readonly string[];
}

export async function compileProject(
  entryFile: string,
  outDir = "dist",
): Promise<CompileResult> {
  const diagnostics: string[] = [];

  const graph = await buildModuleGraph(entryFile);

  if (graph.diagnostics.length > 0) {
    return { diagnostics: graph.diagnostics };
  }

  for (const loaded of graph.modules) {
    const foreignResult = await collectForeignImports(loaded.moduleAst);
    diagnostics.push(...foreignResult.diagnostics);

    if (diagnostics.length > 0) {
      return { diagnostics };
    }

    const typeCheck = checkModule(loaded.moduleAst, {
      foreignBindings: foreignResult.bindings,
    });

    if (typeCheck.diagnostics.length > 0) {
      for (const d of typeCheck.diagnostics) {
        diagnostics.push(`${loaded.filePath}:${d.span.start.line}:${d.span.start.column}: ${d.message}`);
      }
      return { diagnostics };
    }

    const emitted = emitModule(loaded.moduleAst, {
      moduleName: loaded.moduleAst.name.join("."),
    });

    const outputFile = computeOutputFile(loaded.moduleAst.name, outDir);

    fs.mkdirSync(path.dirname(outputFile), { recursive: true });
    fs.writeFileSync(outputFile, emitted.code, "utf8");
  }

  return { diagnostics };
}

function computeOutputFile(moduleName: readonly string[], outDir: string): string {
  return path.join(outDir, ...moduleName) + ".js";
}

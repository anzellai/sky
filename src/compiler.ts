// src/compiler.ts
// Sky compiler pipeline
//
// Responsibilities
// - load module
// - lex → parse → typecheck → emit
// - write JS output

import fs from "fs";
import path from "path";

import { emitModule } from "./codegen/js-emitter.js";
import { checkModule } from "./type-system/checker.js";
import { buildModuleGraph } from "./module-graph.js";

export interface CompileResult {
  readonly diagnostics: readonly string[];
}

export function compileProject(entryFile: string, outDir = "dist"): CompileResult {
  const diagnostics: string[] = [];

  const graph = buildModuleGraph(entryFile);

  if (graph.diagnostics.length > 0) {
    return { diagnostics: [...graph.diagnostics] };
  }

  for (const node of graph.nodes) {
    const typeCheck = checkModule(node.moduleAst);

    if (typeCheck.diagnostics.length > 0) {
      for (const d of typeCheck.diagnostics) {
        diagnostics.push(d.message);
      }
      return { diagnostics };
    }

    const emit = emitModule(node.moduleAst, {
      moduleName: node.moduleAst.name.join("."),
    });

    const outputFile = computeOutputFile(node.moduleAst.name, outDir);

    fs.mkdirSync(path.dirname(outputFile), { recursive: true });
    fs.writeFileSync(outputFile, emit.code, "utf8");
  }

  return { diagnostics };
}

function computeOutputFile(moduleName: readonly string[], outDir: string): string {

  return path.join(outDir, ...moduleName) + ".js";

}

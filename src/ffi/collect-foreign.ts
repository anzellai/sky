// src/ffi/collect-foreign.ts
// Collect and resolve all foreign imports inside a Sky module.

import * as AST from "../ast.js"
import fs from "fs"
import {
  generateForeignBindings,
  type GeneratedForeignBindings
} from "./generate-bindings.js"

export interface CollectForeignResult {
  readonly bindings: readonly GeneratedForeignBindings[]
  readonly diagnostics: readonly string[]
}

export async function collectForeignImports(
  module: AST.Module,
  filePath: string
): Promise<CollectForeignResult> {

  const diagnostics: string[] = []
  const bindings: GeneratedForeignBindings[] = []

  // Handle synthetic FFI stubs that have an accompanying .json metadata file
  const jsonPath = filePath.replace(/\.sky$/, ".json")
  if (fs.existsSync(jsonPath)) {
    try {
      const meta = JSON.parse(fs.readFileSync(jsonPath, "utf8"))
      if (meta && typeof meta.packageName === "string") {
        const result = await generateForeignBindings(meta.packageName, [])
        diagnostics.push(...result.diagnostics)
        if (result.generated) {
          bindings.push(result.generated)
        }
      }
    } catch (e) {
      // Ignore read/parse errors for optional metadata
    }
  }

  for (const decl of module.declarations) {

    if (decl.kind !== "ForeignImportDeclaration") {
      continue
    }

    const packageName = decl.sourceModule

    // Skip TS type extraction for JS built-in globals and just inject them as Foreign types
    if (packageName === "JSON" || packageName === "global") {
      bindings.push({
        packageName: packageName,
        skyModuleName: `Sky.FFI.${packageName}`,
        runtimeEntryPath: "",
        values: [{
          skyName: decl.name,
          jsName: decl.name,
          sourceModule: packageName,
          skyType: "Foreign"
        }],
        types: []
      });
      continue;
    }

    const requested = [decl.name]

    if (requested.length === 0) {
      diagnostics.push(
        `Foreign import "${packageName}" must specify exposing (...)`
      )
      continue
    }

    const result = await generateForeignBindings(
      packageName,
      requested
    )

    diagnostics.push(...result.diagnostics)

    if (result.generated) {
      bindings.push(result.generated)
    }

  }

  return {
    bindings,
    diagnostics
  }

}

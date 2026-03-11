// src/ffi/collect-foreign.ts
// Collect and resolve all foreign imports inside a Sky module.

import * as AST from "../ast.js"
import {
  generateForeignBindings,
  type GeneratedForeignBindings
} from "./generate-bindings.js"

export interface CollectForeignResult {
  readonly bindings: readonly GeneratedForeignBindings[]
  readonly diagnostics: readonly string[]
}

export async function collectForeignImports(
  module: AST.Module
): Promise<CollectForeignResult> {

  const diagnostics: string[] = []
  const bindings: GeneratedForeignBindings[] = []

  for (const decl of module.declarations) {

    if (decl.kind !== "ForeignImportDeclaration") {
      continue
    }

    const packageName = decl.sourceModule
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

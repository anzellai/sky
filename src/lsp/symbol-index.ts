// src/lsp/symbol-index.ts

import * as AST from "../ast.js";

export interface SymbolInfo {
  readonly name: string;
  readonly kind: "function" | "value";
  readonly span: AST.SourceSpan;
}

export class SymbolIndex {

  private symbols = new Map<string, SymbolInfo>();

  build(module: AST.Module): void {

    this.symbols.clear();

    for (const decl of module.declarations) {

      if (decl.kind === "FunctionDeclaration") {

        this.symbols.set(decl.name, {
          name: decl.name,
          kind: "function",
          span: decl.span
        });

      }

    }

  }

  lookup(name: string): SymbolInfo | undefined {
    return this.symbols.get(name);
  }

  all(): readonly SymbolInfo[] {
    return [...this.symbols.values()];
  }

}

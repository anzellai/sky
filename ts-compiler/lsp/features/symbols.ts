import { DocumentSymbol, SymbolKind, Range } from "vscode-languageserver/node.js";
import type * as AST from "../../ast/ast.js";
import type { SourceSpan } from "../../lexer/lexer.js";

function spanToRange(span: SourceSpan): Range {
  return {
    start: { line: span.start.line - 1, character: span.start.column - 1 },
    end: { line: span.end.line - 1, character: span.end.column - 1 }
  };
}

export function getDocumentSymbols(ast: AST.Module): DocumentSymbol[] {
  const symbols: DocumentSymbol[] = [];

  for (const decl of ast.declarations) {
    if (decl.kind === "FunctionDeclaration") {
      symbols.push({
        name: decl.name,
        kind: SymbolKind.Function,
        range: spanToRange(decl.span),
        selectionRange: spanToRange(decl.span),
      });
    } else if (decl.kind === "TypeDeclaration") {
      const children: DocumentSymbol[] = [];
      for (const v of decl.variants) {
        children.push({
          name: v.name,
          kind: SymbolKind.EnumMember,
          range: spanToRange(v.span),
          selectionRange: spanToRange(v.span),
        });
      }
      symbols.push({
        name: decl.name,
        kind: SymbolKind.Enum,
        range: spanToRange(decl.span),
        selectionRange: spanToRange(decl.span),
        children,
      });
    } else if (decl.kind === "TypeAliasDeclaration") {
      symbols.push({
        name: decl.name,
        kind: SymbolKind.Struct,
        range: spanToRange(decl.span),
        selectionRange: spanToRange(decl.span),
      });
    } else if (decl.kind === "TypeAnnotation") {
      // Skip — type annotations are shown with their functions
    }
  }

  return symbols;
}

import { FoldingRange, FoldingRangeKind } from "vscode-languageserver/node.js";
import type * as AST from "../../ast/ast.js";

export function getFoldingRanges(ast: AST.Module): FoldingRange[] {
  const ranges: FoldingRange[] = [];

  for (const decl of ast.declarations) {
    if (decl.span && decl.span.end.line > decl.span.start.line) {
      ranges.push({
        startLine: decl.span.start.line - 1,
        endLine: decl.span.end.line - 1,
        kind: FoldingRangeKind.Region,
      });
    }
  }

  // Fold let-in blocks and case expressions recursively
  for (const decl of ast.declarations) {
    if (decl.kind === "FunctionDeclaration") {
      collectFoldingFromExpr(decl.body, ranges);
    }
  }

  // Fold import block
  if (ast.imports.length > 1) {
    const firstImport = ast.imports[0];
    const lastImport = ast.imports[ast.imports.length - 1];
    if (firstImport.span && lastImport.span) {
      ranges.push({
        startLine: firstImport.span.start.line - 1,
        endLine: lastImport.span.end.line - 1,
        kind: FoldingRangeKind.Imports,
      });
    }
  }

  return ranges;
}

function collectFoldingFromExpr(expr: AST.Expression, ranges: FoldingRange[]) {
  if (!expr || !expr.span) return;

  switch (expr.kind) {
    case "LetExpression":
      if (expr.span.end.line > expr.span.start.line) {
        ranges.push({
          startLine: expr.span.start.line - 1,
          endLine: expr.span.end.line - 1,
          kind: FoldingRangeKind.Region,
        });
      }
      for (const b of expr.bindings) collectFoldingFromExpr(b.value, ranges);
      collectFoldingFromExpr(expr.body, ranges);
      break;
    case "CaseExpression":
      if (expr.span.end.line > expr.span.start.line) {
        ranges.push({
          startLine: expr.span.start.line - 1,
          endLine: expr.span.end.line - 1,
          kind: FoldingRangeKind.Region,
        });
      }
      for (const b of expr.branches) collectFoldingFromExpr(b.body, ranges);
      break;
    case "IfExpression":
      collectFoldingFromExpr(expr.thenBranch, ranges);
      collectFoldingFromExpr(expr.elseBranch, ranges);
      break;
    case "LambdaExpression":
      collectFoldingFromExpr(expr.body, ranges);
      break;
    case "CallExpression":
      collectFoldingFromExpr(expr.callee, ranges);
      for (const a of expr.arguments) collectFoldingFromExpr(a, ranges);
      break;
  }
}

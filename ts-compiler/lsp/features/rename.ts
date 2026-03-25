import { WorkspaceEdit, TextEdit, Range } from "vscode-languageserver/node.js";
import type * as AST from "../../ast/ast.js";

function spanToRange(span: AST.SourceSpan): Range {
  return {
    start: { line: span.start.line - 1, character: span.start.column - 1 },
    end: { line: span.end.line - 1, character: span.end.column - 1 }
  };
}

export function renameSymbol(
  oldName: string,
  newName: string,
  modules: Map<string, { filePath: string; moduleAst: AST.Module }>,
): WorkspaceEdit {
  const changes: { [uri: string]: TextEdit[] } = {};

  for (const [_, mod] of modules) {
    const uri = `file://${mod.filePath}`;
    const edits: TextEdit[] = [];

    // Rename declarations
    for (const decl of mod.moduleAst.declarations) {
      if (decl.kind === "FunctionDeclaration" && decl.name === oldName && decl.span) {
        // The function name is at the start of the span
        edits.push({
          range: {
            start: { line: decl.span.start.line - 1, character: decl.span.start.column - 1 },
            end: { line: decl.span.start.line - 1, character: decl.span.start.column - 1 + oldName.length }
          },
          newText: newName
        });
      }
    }

    // Rename references in expressions
    walkExpressions(mod.moduleAst, (node) => {
      if (node.kind === "IdentifierExpression" && node.name === oldName && node.span) {
        edits.push({
          range: spanToRange(node.span),
          newText: newName
        });
      }
    });

    if (edits.length > 0) {
      changes[uri] = edits;
    }
  }

  return { changes };
}

function walkExpressions(ast: AST.Module, callback: (node: AST.Expression) => void) {
  for (const decl of ast.declarations) {
    if (decl.kind === "FunctionDeclaration") {
      walkExpr(decl.body, callback);
    }
  }
}

function walkExpr(expr: AST.Expression, callback: (node: AST.Expression) => void) {
  if (!expr) return;
  callback(expr);
  switch (expr.kind) {
    case "CallExpression":
      walkExpr(expr.callee, callback);
      for (const arg of expr.arguments) walkExpr(arg, callback);
      break;
    case "BinaryExpression":
      walkExpr(expr.left, callback);
      walkExpr(expr.right, callback);
      break;
    case "IfExpression":
      walkExpr(expr.condition, callback);
      walkExpr(expr.thenBranch, callback);
      walkExpr(expr.elseBranch, callback);
      break;
    case "LetExpression":
      for (const b of expr.bindings) walkExpr(b.value, callback);
      walkExpr(expr.body, callback);
      break;
    case "CaseExpression":
      walkExpr(expr.subject, callback);
      for (const b of expr.branches) walkExpr(b.body, callback);
      break;
    case "LambdaExpression":
      walkExpr(expr.body, callback);
      break;
    case "ListExpression":
      for (const item of expr.items) walkExpr(item, callback);
      break;
    case "TupleExpression":
      for (const item of expr.items) walkExpr(item, callback);
      break;
    case "RecordExpression":
      for (const f of expr.fields) walkExpr(f.value, callback);
      break;
    case "RecordUpdateExpression":
      walkExpr(expr.base, callback);
      for (const f of expr.fields) walkExpr(f.value, callback);
      break;
    case "FieldAccessExpression":
      walkExpr(expr.target, callback);
      break;
    case "ParenthesizedExpression":
      walkExpr(expr.expression, callback);
      break;
  }
}

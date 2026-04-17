import { Location, Range } from "vscode-languageserver/node.js";
import type * as AST from "../../ast/ast.js";
import type { SourceSpan } from "../../lexer/lexer.js";

function spanToRange(span: SourceSpan): Range {
  return {
    start: { line: span.start.line - 1, character: span.start.column - 1 },
    end: { line: span.end.line - 1, character: span.end.column - 1 }
  };
}

export function findReferences(
  name: string,
  modules: ReadonlyArray<{ filePath: string; moduleAst: AST.Module }>,
): Location[] {
  const locations: Location[] = [];

  for (const mod of modules) {
    const uri = `file://${mod.filePath}`;
    walkExpressions(mod.moduleAst, (node) => {
      if (node.kind === "IdentifierExpression" && node.name === name && node.span) {
        locations.push({ uri, range: spanToRange(node.span) });
      } else if (node.kind === "QualifiedIdentifierExpression") {
        const lastPart = node.name.parts[node.name.parts.length - 1];
        if (lastPart === name && node.span) {
          locations.push({ uri, range: spanToRange(node.span) });
        }
      }
    });

    // Also check declarations (definition sites)
    for (const decl of mod.moduleAst.declarations) {
      if (decl.kind === "FunctionDeclaration" && decl.name === name && decl.span) {
        locations.push({ uri, range: spanToRange(decl.span) });
      }
    }
  }

  return locations;
}

function walkExpressions(ast: AST.Module, callback: (node: AST.Expression) => void) {
  for (const decl of ast.declarations) {
    if (decl.kind === "FunctionDeclaration") {
      walkExpr(decl.body, callback);
    }
  }
}

function walkExpr(expr: AST.Expression, callback: (node: AST.Expression) => void) {
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

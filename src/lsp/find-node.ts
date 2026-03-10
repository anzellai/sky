import * as AST from "../ast.js"

export function findIdentifierAtPosition(
  module: AST.Module,
  line: number,
  column: number
): AST.IdentifierExpression | undefined {

  for (const decl of module.declarations) {

    if (decl.kind === "FunctionDeclaration") {

      const found = searchExpression(decl.body, line, column)

      if (found) return found

    }

  }

}

function searchExpression(
  expr: AST.Expression,
  line: number,
  column: number
): AST.IdentifierExpression | undefined {

  if (isInside(expr.span, line, column)) {

    if (expr.kind === "IdentifierExpression") {
      return expr
    }

    switch (expr.kind) {

      case "CallExpression":

        return (
          searchExpression(expr.callee, line, column) ??
          expr.arguments.map(a => searchExpression(a, line, column)).find(Boolean)
        )

      case "BinaryExpression":

        return (
          searchExpression(expr.left, line, column) ??
          searchExpression(expr.right, line, column)
        )

      case "ParenthesizedExpression":

        return searchExpression(expr.expression, line, column)

    }

  }

}

function isInside(span: AST.SourceSpan, line: number, column: number): boolean {

  const start = span.start
  const end = span.end

  if (line < start.line || line > end.line) return false

  if (line === start.line && column < start.column) return false

  if (line === end.line && column > end.column) return false

  return true

}

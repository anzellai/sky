import * as AST from "../ast.js"
import { concat, Doc, group, indent, line, text } from "./doc.js"
import { render } from "./render.js"

export function formatModule(module: AST.Module): string {

  const docs: Doc[] = []

  docs.push(formatModuleHeader(module))
  docs.push(line)
  docs.push(line)

  if (module.imports.length > 0) {

    for (const imp of module.imports) {
      docs.push(formatImport(imp))
      docs.push(line)
    }

    docs.push(line)

  }

  module.declarations.forEach((decl, i) => {

    docs.push(formatDeclaration(decl))

    if (i !== module.declarations.length - 1) {
      docs.push(line)
      docs.push(line)
    }

  })

  return render(concat(...docs)).trimEnd() + "\n"

}

function block(header: Doc, body: Doc): Doc {

  return group(
    concat(
      header,
      line,
      indent(body)
    )
  )

}

function formatModuleHeader(module: AST.Module): Doc {

  let exposingDoc: Doc = text("");
  if (module.exposing) {
    if (module.exposing.open) {
      exposingDoc = text(" exposing (..)");
    } else if (module.exposing.items && module.exposing.items.length > 0) {
      const items = module.exposing.items.map(i => text(i.name));
      exposingDoc = concat(
        text(" exposing ("),
        joinDocs(items, text(", ")),
        text(")")
      );
    } else {
      exposingDoc = text(" exposing ()");
    }
  }

  return concat(
    text("module "),
    text(module.name.join(".")),
    exposingDoc
  )

}

function formatImport(imp: AST.ImportDeclaration): Doc {

  const parts: Doc[] = [
    text("import "),
    text(imp.moduleName.join("."))
  ];

  if (imp.alias) {
    parts.push(text(" as "));
    parts.push(text(imp.alias.name));
  }

  if (imp.exposing) {
    if (imp.exposing.open) {
      parts.push(text(" exposing (..)"));
    } else if (imp.exposing.items && imp.exposing.items.length > 0) {
      const items = imp.exposing.items.map(i => text(i.name));
      parts.push(
        text(" exposing ("),
        joinDocs(items, text(", ")),
        text(")")
      );
    } else {
      parts.push(text(" exposing ()"));
    }
  }

  return concat(...parts);

}

function formatDeclaration(decl: AST.Declaration): Doc {

  switch (decl.kind) {

    case "FunctionDeclaration":
      return formatFunction(decl)

    default:
      return text("-- unsupported declaration")

  }

}

function joinDocs(items: Doc[], sep: Doc): Doc {

  if (items.length === 0) {
    return text("")
  }

  const parts: Doc[] = [items[0]]

  for (let i = 1; i < items.length; i++) {
    parts.push(sep)
    parts.push(items[i])
  }

  return concat(...parts)

}

function formatPattern(pattern: AST.Pattern): Doc {

  switch (pattern.kind) {

    case "VariablePattern":
      return text(pattern.name)

    case "WildcardPattern":
      return text("_")

    case "TuplePattern":
      return concat(
        text("("),
        joinDocs(pattern.items.map(formatPattern), text(", ")),
        text(")")
      )

    case "ConstructorPattern":
      return concat(
        text(pattern.constructorName.parts.join(".")),
        pattern.arguments.length
          ? concat(
            text(" "),
            joinDocs(pattern.arguments.map(formatPattern), text(" "))
          )
          : text("")
      )

    case "ListPattern":
      return concat(
        text("["),
        joinDocs(pattern.items.map(formatPattern), text(", ")),
        text("]")
      )

    default:
      return text("-- unsupported pattern")
  }

}

function formatCase(expr: AST.CaseExpression): Doc {

  const header =
    concat(
      text("case "),
      formatExpression(expr.subject),
      text(" of")
    )

  const branches = concat(

    ...expr.branches.flatMap((b, i) => {

      const branch =
        concat(
          formatPattern(b.pattern),
          text(" ->"),
          line,
          indent(formatExpression(b.body))
        )

      if (i === 0) return [branch]

      return [
        line,
        line,
        branch
      ]

    })

  )

  return block(header, branches)

}

function formatLet(expr: AST.LetExpression): Doc {

  const bindings = concat(

    ...expr.bindings.flatMap((b, i) => {

      const bind =
        concat(
          formatPattern(b.pattern),
          text(" ="),
          line,
          indent(formatExpression(b.value))
        )

      if (i === 0) return [bind]

      return [
        line,
        line,
        bind
      ]

    })

  )

  const letPart =
    block(text("let"), bindings)

  return concat(
    letPart,
    line,
    text("in"),
    line,
    indent(formatExpression(expr.body))
  )

}

function formatFunction(fn: AST.FunctionDeclaration): Doc {

  const params = fn.parameters.map(p => formatPattern(p.pattern));

  const paramsDoc = params.length > 0
    ? concat(text(" "), joinDocs(params, text(" ")))
    : text("");

  const header =
    concat(
      text(fn.name),
      paramsDoc,
      text(" =")
    )

  return block(header, formatExpression(fn.body))

}

function formatExpression(expr: AST.Expression): Doc {

  switch (expr.kind) {

    case "IdentifierExpression":
      return text(expr.name)

    case "IntegerLiteralExpression":
      return text(expr.raw)

    case "FloatLiteralExpression":
      return text(expr.raw)

    case "StringLiteralExpression":
      return text(JSON.stringify(expr.value))

    case "BooleanLiteralExpression":
      return text(expr.value ? "True" : "False")

    case "BinaryExpression":
      return concat(
        formatExpression(expr.left),
        text(" "),
        text(expr.operator),
        text(" "),
        formatExpression(expr.right)
      )

    case "CallExpression":

      return concat(
        formatExpression(expr.callee),
        text(" "),
        joinDocs(expr.arguments.map(formatExpression), text(" "))
      )

    case "UnitExpression":
      return text("()")

    case "ParenthesizedExpression":
      return concat(
        text("("),
        formatExpression(expr.expression),
        text(")")
      )

    default:
      return text("-- unsupported expression")

  }

}

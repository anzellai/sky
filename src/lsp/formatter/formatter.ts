import * as AST from "../../ast/ast.js"
import { concat, Doc, group, indent, line, text, hardline } from "./layout.js"
import { render } from "./printer.js"

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
      indent(
        concat(line, body)
      )
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

function formatTypeExpression(t: AST.TypeExpression): Doc {
  switch (t.kind) {
    case "TypeVariable":
      return text(t.name);
    case "TypeReference":
      if (t.arguments.length === 0) return text(t.name.parts.join("."));
      return concat(
        text(t.name.parts.join(".")),
        text(" "),
        joinDocs(t.arguments.map(formatTypeExpression), text(" "))
      );
    case "FunctionType":
      return concat(
        formatTypeExpression(t.from),
        text(" -> "),
        formatTypeExpression(t.to)
      );
    case "RecordType":
      if (t.fields.length === 0) return text("{}");
      if (t.fields.length === 1) {
        return concat(text("{ "), text(t.fields[0].name), text(" : "), formatTypeExpression(t.fields[0].type), text(" }"));
      }
      return group(concat(
        text("{ "),
        joinDocs(
          t.fields.map((f, i) => {
            const prefix = i === 0 ? text("") : text(", ");
            return concat(prefix, text(f.name), text(" : "), formatTypeExpression(f.type));
          }),
          hardline
        ),
        hardline,
        text("}")
      ))
  }
}

function formatTypeDeclaration(decl: AST.TypeDeclaration): Doc {
  const header = concat(
    text("type "),
    text(decl.name),
    decl.typeParameters.length > 0 ? text(" " + decl.typeParameters.join(" ")) : text("")
  );

  if (decl.variants.length === 0) return header;

  const variants = decl.variants.map((v, i) => {
    const prefix = i === 0 ? text("= ") : text("| ");
    if (v.fields.length === 0) return concat(prefix, text(v.name));
    return concat(
      prefix,
      text(v.name),
      text(" "),
      joinDocs(v.fields.map(formatTypeExpression), text(" "))
    );
  });

  return block(header, joinDocs(variants, line));
}

function formatTypeAliasDeclaration(decl: AST.TypeAliasDeclaration): Doc {
  const header = concat(
    text("type alias "),
    text(decl.name),
    decl.typeParameters.length > 0 ? text(" " + decl.typeParameters.join(" ")) : text(""),
    text(" =")
  );

  return block(header, formatTypeExpression(decl.aliasedType));
}

function formatForeignImportDeclaration(decl: AST.ForeignImportDeclaration): Doc {
  return concat(
    text("foreign import "),
    text(JSON.stringify(decl.sourceModule)),
    text(" exposing ("),
    text(decl.name),
    text(")")
  );
}

function formatTypeAnnotation(decl: AST.TypeAnnotation): Doc {
  return concat(
    text(decl.name),
    text(" : "),
    formatTypeExpression(decl.type)
  );
}

function formatDeclaration(decl: AST.Declaration): Doc {

  switch (decl.kind) {

    case "FunctionDeclaration":
      return formatFunction(decl)

    case "TypeDeclaration":
      return formatTypeDeclaration(decl)

    case "TypeAliasDeclaration":
      return formatTypeAliasDeclaration(decl)

    case "ForeignImportDeclaration":
      return formatForeignImportDeclaration(decl)

    case "TypeAnnotation":
      return formatTypeAnnotation(decl)

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

    case "LiteralPattern":
      if (typeof pattern.value === "string") return text(JSON.stringify(pattern.value));
      if (typeof pattern.value === "boolean") return text(pattern.value ? "True" : "False");
      return text(String(pattern.value));

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
          indent(concat(hardline, formatExpression(b.body)))
        )

      if (i === 0) return [branch]

      return [
        hardline,
        hardline,
        branch
      ]

    })

  )

  return block(header, branches)

}

function formatLet(expr: AST.LetExpression): Doc {

  const bindings = expr.bindings.map((b) => {

    const patternDoc = formatPattern(b.pattern);
    const valueDoc = formatExpression(b.value);
    
    let bind: Doc;
    
    if (b.typeAnnotation) {
      // Repeat pattern name for the assignment if it's a simple variable
      const assignmentPattern = b.pattern.kind === "VariablePattern" ? text(b.pattern.name) : patternDoc;

      bind = concat(
        patternDoc,
        text(" : "),
        formatTypeExpression(b.typeAnnotation),
        hardline,
        group(concat(
          assignmentPattern,
          text(" ="),
          indent(concat(line, valueDoc))
        ))
      );
    } else {
      bind = group(concat(
        patternDoc,
        text(" ="),
        indent(concat(line, valueDoc))
      ));
    }

    return bind;

  });

  return group(concat(
    text("let"),
    indent(concat(hardline, joinDocs(bindings, hardline))),
    hardline,
    text("in"),
    indent(concat(hardline, formatExpression(expr.body)))
  ))

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

  return group(concat(
    header,
    indent(concat(hardline, formatExpression(fn.body)))
  ))

}

function formatExpression(expr: AST.Expression): Doc {

  switch (expr.kind) {

    case "QualifiedIdentifierExpression":
      return text(expr.name.parts.join("."))

    case "RecordExpression":
      if (expr.fields.length === 0) return text("{}");
      if (expr.fields.length === 1) {
        return group(concat(
          text("{ "),
          text(expr.fields[0].name),
          text(" = "),
          formatExpression(expr.fields[0].value),
          text(" }")
        ));
      }
      return group(concat(
        text("{ "),
        joinDocs(
          expr.fields.map((f, i) => {
            const prefix = i === 0 ? text("") : text(", ");
            return concat(prefix, text(f.name), text(" = "), formatExpression(f.value));
          }),
          hardline
        ),
        hardline,
        text("}")
      ))

    case "FieldAccessExpression":
      return concat(
        formatExpression(expr.target),
        text("."),
        text(expr.fieldName)
      )

    case "CaseExpression":
      return formatCase(expr)

    case "LetExpression":
      return formatLet(expr)

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

    case "BinaryExpression": {
      const isPipe = expr.operator === "|>" || expr.operator === "<|";
      if (isPipe) {
        return concat(
          formatExpression(expr.left),
          indent(concat(hardline, text(expr.operator), text(" "), formatExpression(expr.right)))
        )
      }
      return concat(
        formatExpression(expr.left),
        text(" "),
        text(expr.operator),
        text(" "),
        formatExpression(expr.right)
      )
    }

    case "CallExpression":
      // Elm style: if the last argument is a large list or record, put it on a new line
      if (expr.arguments.length > 0) {
        const lastArg = expr.arguments[expr.arguments.length - 1];
        if (lastArg.kind === "ListExpression" || lastArg.kind === "RecordExpression") {
          const otherArgs = expr.arguments.slice(0, -1);
          const calleeAndOthers = otherArgs.length > 0
            ? concat(formatExpression(expr.callee), text(" "), joinDocs(otherArgs.map(formatExpression), text(" ")))
            : formatExpression(expr.callee);
          
          return concat(
            calleeAndOthers,
            indent(concat(hardline, formatExpression(lastArg)))
          );
        }
      }

      return concat(
        formatExpression(expr.callee),
        text(" "),
        joinDocs(expr.arguments.map(formatExpression), text(" "))
      )

    case "UnitExpression":
      return text("()")

    case "IfExpression":
      return group(concat(
        text("if "),
        formatExpression(expr.condition),
        text(" then"),
        indent(concat(hardline, formatExpression(expr.thenBranch))),
        hardline,
        text("else"),
        indent(concat(hardline, formatExpression(expr.elseBranch)))
      ))

    case "TupleExpression":
      return group(concat(
        text("("),
        joinDocs(expr.items.map(formatExpression), text(", ")),
        text(")")
      ))

    case "ListExpression":
      if (expr.items.length === 0) return text("[]");
      if (expr.items.length === 1) {
        return group(concat(text("[ "), formatExpression(expr.items[0]), text(" ]")));
      }
      return group(concat(
        text("[ "),
        joinDocs(
          expr.items.map((item, i) => {
            const prefix = i === 0 ? text("") : text(", ");
            return concat(prefix, formatExpression(item));
          }),
          hardline
        ),
        hardline,
        text("]")
      ))

    case "ParenthesizedExpression":
      return concat(
        text("("),
        formatExpression(expr.expression),
        text(")")
      )

    case "LambdaExpression":
      return concat(
        text("\\"),
        joinDocs(expr.parameters.map(p => formatPattern(p.pattern)), text(" ")),
        text(" -> "),
        formatExpression(expr.body)
      )

    case "CharLiteralExpression":
      return text(JSON.stringify(expr.value))

    default:
      return text("-- unsupported expression")

  }

}

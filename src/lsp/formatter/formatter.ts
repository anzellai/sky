import * as AST from "../../ast/ast.js"
import { align, concat, Doc, group, indent, line, text, hardline, softline } from "./layout.js"
import { render } from "./printer.js"

export function formatModule(module: AST.Module, originalSource?: string): string {

  const docs: Doc[] = []

  docs.push(formatModuleHeader(module))
  docs.push(hardline)
  docs.push(hardline)

  if (module.imports.length > 0) {
    for (const imp of module.imports) {
      docs.push(formatImport(imp))
      docs.push(hardline)
    }
    docs.push(hardline)
  }

  for (let i = 0; i < module.declarations.length; i++) {
    const decl = module.declarations[i];
    docs.push(formatDeclaration(decl));

    if (i < module.declarations.length - 1) {
      const nextDecl = module.declarations[i + 1];
      if (decl.kind === "TypeAnnotation" && nextDecl.kind === "FunctionDeclaration" && decl.name === nextDecl.name) {
        docs.push(hardline);
      } else {
        docs.push(hardline);
        docs.push(hardline);
      }
    }
  }

  let result = render(concat(...docs)).trimEnd() + "\n"

  if (originalSource) {
    result = preserveComments(originalSource, result);
  }

  return result;
}

// ============================================================
// Comment preservation
// ============================================================

function preserveComments(original: string, formatted: string): string {
  const origLines = original.split('\n');
  const fmtLines = formatted.split('\n');

  const commentBlocks: { comments: string[], nextDeclPrefix: string }[] = [];
  let currentComments: string[] = [];

  for (let i = 0; i < origLines.length; i++) {
    const trimmed = origLines[i].trimStart();
    if (trimmed.startsWith('--') || trimmed.startsWith('{-')) {
      currentComments.push(origLines[i]);
    } else if (currentComments.length > 0) {
      const prefix = trimmed.split(/\s/)[0] || '';
      if (prefix && prefix !== '') {
        commentBlocks.push({ comments: [...currentComments], nextDeclPrefix: prefix });
      } else if (trimmed === '') {
        continue;
      }
      currentComments = [];
    }
  }
  if (currentComments.length > 0) {
    commentBlocks.push({ comments: [...currentComments], nextDeclPrefix: '' });
  }

  const result: string[] = [];
  let blockIdx = 0;

  for (let i = 0; i < fmtLines.length; i++) {
    if (blockIdx < commentBlocks.length) {
      const block = commentBlocks[blockIdx];
      const fmtTrimmed = fmtLines[i].trimStart();
      if (block.nextDeclPrefix && fmtTrimmed.startsWith(block.nextDeclPrefix) && !fmtTrimmed.startsWith('--') && !fmtTrimmed.startsWith('{-')) {
        if (result.length > 0 && result[result.length - 1].trim() !== '') {
          result.push('');
        }
        for (const comment of block.comments) {
          result.push(comment);
        }
        blockIdx++;
      }
    }
    result.push(fmtLines[i]);
  }

  while (blockIdx < commentBlocks.length) {
    result.push('');
    for (const comment of commentBlocks[blockIdx].comments) {
      result.push(comment);
    }
    blockIdx++;
  }

  return result.join('\n');
}

// ============================================================
// Helpers
// ============================================================

function joinDocs(items: Doc[], sep: Doc): Doc {
  if (items.length === 0) return text("")
  const parts: Doc[] = [items[0]]
  for (let i = 1; i < items.length; i++) {
    parts.push(sep)
    parts.push(items[i])
  }
  return concat(...parts)
}

// ============================================================
// Module header & imports
// ============================================================

function formatModuleHeader(module: AST.Module): Doc {
  let exposingDoc: Doc = text("");
  if (module.exposing) {
    if (module.exposing.open) {
      exposingDoc = text(" exposing (..)");
    } else if (module.exposing.items && module.exposing.items.length > 0) {
      const items = module.exposing.items.map(i => text((i as any).exposeConstructors ? i.name + "(..)" : i.name));
      exposingDoc = concat(text(" exposing ("), joinDocs(items, text(", ")), text(")"));
    }
  }
  return concat(text("module "), text(module.name.join(".")), exposingDoc)
}

function formatImport(imp: AST.ImportDeclaration): Doc {
  const parts: Doc[] = [text("import "), text(imp.moduleName.join("."))];
  if (imp.alias) {
    parts.push(text(" as "), text(imp.alias.name));
  }
  if (imp.exposing) {
    if (imp.exposing.open) {
      parts.push(text(" exposing (..)"));
    } else if (imp.exposing.items && imp.exposing.items.length > 0) {
      const items = imp.exposing.items.map(i => text((i as any).exposeConstructors ? i.name + "(..)" : i.name));
      parts.push(text(" exposing ("), joinDocs(items, text(", ")), text(")"));
    }
  }
  return concat(...parts);
}

// ============================================================
// Declarations
// ============================================================

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

function formatFunction(fn: AST.FunctionDeclaration): Doc {
  const params = fn.parameters.map(p => formatPattern(p.pattern));
  const paramsDoc = params.length > 0
    ? concat(text(" "), joinDocs(params, text(" ")))
    : text("");

  const header = concat(text(fn.name), paramsDoc, text(" ="))

  return concat(
    header,
    indent(concat(hardline, formatExpression(fn.body)))
  )
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
    return concat(prefix, text(v.name), text(" "), joinDocs(v.fields.map(formatTypeExpression), text(" ")));
  });

  return concat(header, indent(concat(hardline, joinDocs(variants, hardline))))
}

function formatTypeAliasDeclaration(decl: AST.TypeAliasDeclaration): Doc {
  const header = concat(
    text("type alias "),
    text(decl.name),
    decl.typeParameters.length > 0 ? text(" " + decl.typeParameters.join(" ")) : text(""),
    text(" =")
  );
  return concat(header, indent(concat(hardline, formatTypeExpression(decl.aliasedType))))
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
  return concat(text(decl.name), text(" : "), formatTypeExpression(decl.type));
}

// ============================================================
// Type expressions
// ============================================================

function formatTypeExpression(t: AST.TypeExpression): Doc {
  switch (t.kind) {
    case "TypeVariable":
      return text(t.name);
    case "TypeReference":
      // Preserve tuple syntax: Tuple a b → ( a, b )
      if (t.name.parts.join(".") === "Tuple" && t.arguments.length >= 2) {
        return concat(
          text("( "),
          joinDocs(t.arguments.map(formatTypeExpression), text(", ")),
          text(" )")
        );
      }
      if (t.arguments.length === 0) return text(t.name.parts.join("."));
      return concat(text(t.name.parts.join(".")), text(" "), joinDocs(t.arguments.map(arg => {
        // Wrap compound type arguments in parens to preserve semantics
        // e.g., Maybe (Dict String String) not Maybe Dict String String
        if (arg.kind === "TypeReference" && arg.arguments.length > 0) {
          return concat(text("("), formatTypeExpression(arg), text(")"));
        }
        if (arg.kind === "FunctionType") {
          return concat(text("("), formatTypeExpression(arg), text(")"));
        }
        return formatTypeExpression(arg);
      }), text(" ")));
    case "FunctionType":
      return concat(formatTypeExpression(t.from), text(" -> "), formatTypeExpression(t.to));
    case "RecordType":
      if (t.fields.length === 0) return text("{}");
      if (t.fields.length === 1) {
        return concat(text("{ "), text(t.fields[0].name), text(" : "), formatTypeExpression(t.fields[0].type), text(" }"));
      }
      return group(concat(
        text("{ "),
        formatRecordTypeField(t.fields[0]),
        ...t.fields.slice(1).map(f => concat(line, text(", "), formatRecordTypeField(f))),
        line,
        text("}")
      ))
  }
}

function formatRecordTypeField(f: { name: string; type: AST.TypeExpression }): Doc {
  return concat(text(f.name), text(" : "), formatTypeExpression(f.type));
}

// ============================================================
// Patterns
// ============================================================

function formatPattern(pattern: AST.Pattern): Doc {
  switch (pattern.kind) {
    case "VariablePattern":
      return text(pattern.name)
    case "WildcardPattern":
      return text("_")
    case "TuplePattern":
      return group(concat(
        text("( "),
        joinDocs(pattern.items.map(formatPattern), text(", ")),
        text(" )")
      ))
    case "ConstructorPattern":
      if (pattern.arguments.length === 0) return text(pattern.constructorName.parts.join("."));
      return concat(
        text(pattern.constructorName.parts.join(".")),
        text(" "),
        joinDocs(pattern.arguments.map(formatPattern), text(" "))
      )
    case "ListPattern":
      if (pattern.items.length === 0) return text("[]");
      return concat(text("["), joinDocs(pattern.items.map(formatPattern), text(", ")), text("]"))
    case "LiteralPattern":
      if (typeof pattern.value === "string") return text(JSON.stringify(pattern.value));
      if (typeof pattern.value === "boolean") return text(pattern.value ? "True" : "False");
      return text(String(pattern.value));
    case "ConsPattern":
      return concat(formatPattern(pattern.head), text(" :: "), formatPattern(pattern.tail))
    case "AsPattern":
      return concat(formatPattern(pattern.pattern), text(" as "), text(pattern.name))
    default:
      return text("-- unsupported pattern")
  }
}

// ============================================================
// Expressions — the core of the formatter
//
// Key principle: use group(concat(..., line, ...)) everywhere.
// The printer decides: if it fits on one line → line becomes space.
// If not → line becomes newline with current indentation.
// ============================================================

function formatExpression(expr: AST.Expression): Doc {
  switch (expr.kind) {

    case "IdentifierExpression":
      return text(expr.name)

    case "QualifiedIdentifierExpression":
      return text(expr.name.parts.join("."))

    case "IntegerLiteralExpression":
      return text(expr.raw)

    case "FloatLiteralExpression":
      return text(expr.raw)

    case "StringLiteralExpression":
      return text(JSON.stringify(expr.value))

    case "BooleanLiteralExpression":
      return text(expr.value ? "True" : "False")

    case "CharLiteralExpression":
      return text(JSON.stringify(expr.value))

    case "UnitExpression":
      return text("()")

    // ---- Call expressions ----
    // Elm-format: if call fits on one line, keep it.
    // Otherwise: callee on first line, each arg indented on new line.
    case "CallExpression":
      return group(concat(
        formatExpression(expr.callee),
        indent(concat(
          ...expr.arguments.map(arg => concat(line, formatExpression(arg)))
        ))
      ))

    // ---- Lists ----
    // Short: [ a, b, c ]
    // Long: leading comma style
    //   [ first
    //   , second
    //   , third
    //   ]
    case "ListExpression":
      if (expr.items.length === 0) return text("[]");
      return group(concat(
        text("[ "),
        formatExpression(expr.items[0]),
        ...expr.items.slice(1).map(item =>
          concat(line, text(", "), formatExpression(item))
        ),
        line,
        text("]")
      ))

    // ---- Tuples ----
    // Short: ( a, b )
    // Long: leading comma style
    //   ( first
    //   , second
    //   )
    case "TupleExpression":
      if (expr.items.length === 0) return text("()");
      return group(concat(
        text("( "),
        formatExpression(expr.items[0]),
        ...expr.items.slice(1).map(item =>
          concat(line, text(", "), formatExpression(item))
        ),
        line,
        text(")")
      ))

    // ---- Records ----
    // Short: { name = "Alice", age = 30 }
    // Long: leading comma style
    //   { name = "Alice"
    //   , age = 30
    //   }
    case "RecordExpression":
      if (expr.fields.length === 0) return text("{}");
      return group(align(concat(
        text("{ "),
        formatRecordField(expr.fields[0]),
        ...expr.fields.slice(1).map(f =>
          concat(line, text(", "), formatRecordField(f))
        ),
        line,
        text("}")
      )))

    case "RecordUpdateExpression":
      return group(align(concat(
        text("{ "),
        formatExpression(expr.base),
        line,
        text("| "),
        formatRecordField(expr.fields[0]),
        ...expr.fields.slice(1).map(f =>
          concat(line, text(", "), formatRecordField(f))
        ),
        line,
        text("}")
      )))

    case "FieldAccessExpression":
      return concat(formatExpression(expr.target), text("."), text(expr.fieldName))

    // ---- Parenthesized ----
    case "ParenthesizedExpression":
      return group(concat(
        text("("),
        softline,
        formatExpression(expr.expression),
        softline,
        text(")")
      ))

    // ---- Lambda ----
    case "LambdaExpression":
      return group(concat(
        text("\\"),
        joinDocs(expr.parameters.map(p => formatPattern(p.pattern)), text(" ")),
        text(" ->"),
        indent(concat(line, formatExpression(expr.body)))
      ))

    // ---- Binary operators ----
    case "BinaryExpression": {
      const isPipe = expr.operator === "|>" || expr.operator === "<|";
      if (isPipe) {
        return concat(
          formatExpression(expr.left),
          indent(concat(hardline, text(expr.operator), text(" "), formatExpression(expr.right)))
        )
      }
      return group(concat(
        formatExpression(expr.left),
        text(" "),
        text(expr.operator),
        line,
        formatExpression(expr.right)
      ))
    }

    // ---- If-then-else ----
    case "IfExpression":
      return concat(
        text("if "),
        formatExpression(expr.condition),
        text(" then"),
        indent(concat(hardline, formatExpression(expr.thenBranch))),
        hardline,
        text("else"),
        indent(concat(hardline, formatExpression(expr.elseBranch)))
      )

    // ---- Case-of ----
    case "CaseExpression":
      return formatCase(expr)

    // ---- Let-in ----
    case "LetExpression":
      return formatLet(expr)

    default:
      return text("-- unsupported expression")
  }
}

function formatRecordField(f: { name: string; value: AST.Expression }): Doc {
  return group(concat(text(f.name), text(" ="), indent(concat(line, formatExpression(f.value)))));
}

// ============================================================
// Case expression
// ============================================================

function formatCase(expr: AST.CaseExpression): Doc {
  const header = concat(text("case "), formatExpression(expr.subject), text(" of"))

  const branches = concat(
    ...expr.branches.flatMap((b, i) => {
      const branch = concat(
        formatPattern(b.pattern),
        text(" ->"),
        indent(concat(hardline, formatExpression(b.body)))
      )
      if (i === 0) return [branch]
      return [hardline, hardline, branch]
    })
  )

  return concat(header, indent(concat(hardline, branches)))
}

// ============================================================
// Let expression
// ============================================================

function formatLet(expr: AST.LetExpression): Doc {
  const bindings = expr.bindings.map((b) => {
    const patternDoc = formatPattern(b.pattern);
    const valueDoc = formatExpression(b.value);

    if (b.typeAnnotation) {
      const assignmentPattern = b.pattern.kind === "VariablePattern" ? text(b.pattern.name) : patternDoc;
      return concat(
        patternDoc,
        text(" : "),
        formatTypeExpression(b.typeAnnotation),
        hardline,
        assignmentPattern,
        text(" ="),
        indent(concat(hardline, valueDoc))
      );
    }

    return concat(
      patternDoc,
      text(" ="),
      indent(concat(hardline, valueDoc))
    );
  });

  return concat(
    text("let"),
    indent(concat(hardline, joinDocs(bindings, hardline))),
    hardline,
    text("in"),
    indent(concat(hardline, formatExpression(expr.body)))
  )
}

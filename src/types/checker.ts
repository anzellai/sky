/* src/type-system/checker.ts
 *
 * Sky type checking pipeline
 *
 * Responsibilities:
 * - create base type environment
 * - register ADTs
 * - inject foreign bindings
 * - infer top-level declarations
 * - run exhaustiveness checks
 */

import * as AST from "../ast/ast.js"
import { TypeEnvironment, createPreludeEnvironment } from "./env.js"
import { inferTopLevel, setTypeAliases } from "./infer.js"
import { registerAdts } from "./adt.js"
import { checkCaseExhaustiveness } from "./exhaustiveness.js"

import {
  type Type,
  type Scheme,
  typeConstant,
  freshTypeVariable,
  mono,
  registerRecordAlias
} from "../types/types.js"

export interface TypeDiagnostic {
  readonly severity: "error" | "warning"
  readonly message: string
  readonly span: AST.NodeBase["span"]
  readonly hint?: string
}

export interface TypedDeclarationInfo {
  readonly name: string
  readonly scheme: Scheme
  readonly pretty: string
}

export interface TypeCheckResult {
  readonly environment: TypeEnvironment
  readonly declarations: readonly TypedDeclarationInfo[]
  readonly diagnostics: readonly TypeDiagnostic[]
  readonly nodeTypes: Map<string, import("../types/types.js").Type>
}

/* -----------------------------------------------------------
   Foreign bindings
----------------------------------------------------------- */

export interface ForeignValueBinding {
  readonly skyName: string
  readonly skyType?: string
}

export interface ForeignBindingSet {
  readonly values: readonly ForeignValueBinding[]
}

export interface CheckModuleOptions {
  readonly foreignBindings?: readonly ForeignBindingSet[]
  readonly imports?: ReadonlyMap<string, Scheme>
  readonly importedTypeAliases?: ReadonlyMap<string, import("../ast/ast.js").TypeExpression>
}

/* -----------------------------------------------------------
   Module checking
----------------------------------------------------------- */

export function checkModule(
  module: AST.Module,
  options: CheckModuleOptions = {}
): TypeCheckResult {

  let env = createPreludeEnvironment()

  if (options.imports) {
    for (const [name, scheme] of options.imports) {
      env = env.extend(name, scheme)
    }
  }

  const declarations: TypedDeclarationInfo[] = []
  const diagnostics: TypeDiagnostic[] = []
  const nodeTypes = new Map<string, import("../types/types.js").Type>()

  /* --------------------------------------------
     1. Register ADTs
  --------------------------------------------- */

  const adtRegistration = registerAdts(env, module.declarations)

  env = adtRegistration.environment

  for (const d of adtRegistration.diagnostics) {
    diagnostics.push({
      severity: "error",
      message: d,
      span: module.span
    })
  }

  if (diagnostics.length > 0) {
    return {
      environment: env,
      declarations,
      diagnostics,
      nodeTypes
    }
  }

  /* --------------------------------------------
     2. Inject foreign bindings
  --------------------------------------------- */

  if (options.foreignBindings) {
    env = injectForeignBindings(env, options.foreignBindings)
  }

  /* --------------------------------------------
     3. Infer top level declarations
  --------------------------------------------- */

  const typeAnnotations = new Map<string, AST.TypeAnnotation>();
  const typeAliases = new Map<string, AST.TypeExpression>();

  for (const declaration of module.declarations) {
      if (declaration.kind === "TypeAnnotation") {
          typeAnnotations.set(declaration.name, declaration);
      }
      if (declaration.kind === "TypeAliasDeclaration") {
          typeAliases.set(declaration.name, declaration.aliasedType);
      }
  }

  // Merge imported type aliases (from other modules) into local aliases
  if (options.importedTypeAliases) {
    for (const [name, aliasType] of options.importedTypeAliases) {
      if (!typeAliases.has(name)) {
        typeAliases.set(name, aliasType);
      }
    }
  }

  // Set type aliases for expansion in type annotations
  setTypeAliases(typeAliases);

  // Register record type aliases for pretty-printing (LSP hover)
  for (const [name, aliasType] of typeAliases) {
    if (aliasType.kind === "RecordType" && aliasType.fields) {
      const fieldNames = aliasType.fields.map((f: any) => f.name);
      registerRecordAlias(fieldNames, name);
    }
  }

  // Pre-register all function declarations with fresh type variables
  // to support forward references (e.g., update calling handleSetSort defined later)
  for (const declaration of module.declarations) {
      if (declaration.kind === "FunctionDeclaration" && !env.get(declaration.name)) {
          env = env.extend(declaration.name, { quantified: [], type: freshTypeVariable() });
      }
  }

  for (const declaration of module.declarations) {

    switch (declaration.kind) {

      case "FunctionDeclaration": {

        try {

          const inferred =
            inferTopLevel(
              adtRegistration.registry,
              env,
              declaration,
              typeAnnotations.get(declaration.name),
              nodeTypes
            )

          declarations.push(inferred)

          env = env.extend(
            inferred.name,
            inferred.scheme
          )

          collectCaseDiagnostics(
            adtRegistration.registry,
            declaration.body,
            diagnostics
          )

          // Warn about discarded function values (likely partial application bugs)
          collectDiscardedFunctionDiagnostics(declaration.body, nodeTypes, diagnostics)

        } catch (error) {

          diagnostics.push({
            severity: "error",
            message:
              error instanceof Error
                ? error.message
                : String(error),
            span: declaration.span,
            hint: `Could not infer the type of ${declaration.name}.`
          })

          // Add a fallback type so later declarations can still reference this function
          // (prevents cascading "Unbound variable" errors)
          const arity = declaration.parameters.length;
          let fallbackType: import("../types/types.js").Type = typeConstant("Any");
          for (let i = 0; i < arity; i++) {
            fallbackType = { kind: "TypeFunction", from: typeConstant("Any"), to: fallbackType };
          }
          env = env.extend(declaration.name, mono(fallbackType));

        }

        break
      }

      case "TypeDeclaration":
      case "TypeAliasDeclaration":
      case "ForeignImportDeclaration":
      case "TypeAnnotation":
        break
    }

  }

  // Warn about Go reserved words used as identifiers
  collectGoReservedWordDiagnostics(module, diagnostics)

  return {
    environment: env,
    declarations,
    diagnostics,
    nodeTypes
  }

}

/* -----------------------------------------------------------
   Result formatting
----------------------------------------------------------- */

export function formatTypeCheckResult(
  result: TypeCheckResult
): string {

  const lines: string[] = []

  for (const decl of result.declarations) {
    lines.push(`${decl.name} : ${decl.pretty}`)
  }

  if (result.diagnostics.length > 0) {

    if (lines.length > 0) {
      lines.push("")
    }

    for (const diagnostic of result.diagnostics) {
      lines.push(`${diagnostic.severity}: ${diagnostic.message}`)
    }

  }

  return lines.join("\n")

}

/* -----------------------------------------------------------
   Foreign binding injection
----------------------------------------------------------- */

function injectForeignBindings(
  env: TypeEnvironment,
  bindingSets: readonly ForeignBindingSet[]
): TypeEnvironment {

  let next = env
  let foreignVarId = -2000; // unique IDs for untyped foreign bindings

  for (const set of bindingSets) {

    for (const value of set.values) {

      if (value.skyType) {
        const type = parseForeignType(value.skyType);
        next = next.extend(value.skyName, mono(type));
      } else {
        // Untyped foreign bindings get a universally quantified a -> b type
        // so they can be applied to any args (like fmt.Println)
        const a: Type = { kind: "TypeVariable", id: foreignVarId--, name: undefined };
        const b: Type = { kind: "TypeVariable", id: foreignVarId--, name: undefined };
        const fnType = { kind: "TypeFunction" as const, from: a, to: b };
        next = next.extend(value.skyName, { quantified: [a.id, b.id], type: fnType });
      }

    }

  }

  return next
}

/* -----------------------------------------------------------
   Foreign type parser (simple)
----------------------------------------------------------- */

function parseForeignType(typeText: string): Type {
  let pos = 0;
  function parsePrimary(): Type {
    while (pos < typeText.length && typeText[pos] === " ") pos++;
    if (pos >= typeText.length) return { kind: "TypeConstant", name: "Foreign" };

    if (typeText[pos] === "(") {
      pos++;
      const t = parseFunctionType();
      while (pos < typeText.length && typeText[pos] === " ") pos++;
      if (typeText[pos] === ")") pos++;
      return t;
    }

    let end = pos;
    while (end < typeText.length && /[a-zA-Z0-9_]/.test(typeText[end])) end++;
    const name = typeText.slice(pos, end);
    pos = end;

    if (name === "List") {
      return { kind: "TypeApplication", constructor: { kind: "TypeConstant", name: "List" }, arguments: [parsePrimary()] };
    }
    
    if (/^[a-z]/.test(name)) return { kind: "TypeVariable", id: stableTypeVar(name), name };
    return { kind: "TypeConstant", name: name || "Foreign" };
  }

  function parseFunctionType(): Type {
    const t = parsePrimary();
    while (pos < typeText.length && typeText[pos] === " ") pos++;
    if (pos + 1 < typeText.length && typeText[pos] === "-" && typeText[pos+1] === ">") {
      pos += 2;
      return { kind: "TypeFunction", from: t, to: parseFunctionType() };
    }
    return t;
  }

  return parseFunctionType();
}

function parseAtomic(text: string): Type {

  if (text.startsWith("List ")) {

    return {
      kind: "TypeApplication",
      constructor: typeConstant("List"),
      arguments: [
        parseAtomic(text.slice(5))
      ]
    }

  }

  if (/^[a-z]/.test(text)) {

    return {
      kind: "TypeVariable",
      id: stableTypeVar(text),
      name: text
    }

  }

  return typeConstant(text)

}

function stableTypeVar(name: string): number {

  let hash = 17

  for (let i = 0; i < name.length; i++) {
    hash = (hash * 31 + name.charCodeAt(i)) | 0
  }

  return Math.abs(hash) + 100000

}

/* -----------------------------------------------------------
   Exhaustiveness diagnostics
----------------------------------------------------------- */

function collectCaseDiagnostics(
  registry: unknown,
  expression: AST.Expression,
  diagnostics: TypeDiagnostic[]
) {

  visitExpression(
    expression,
    expr => {

      if (expr.kind !== "CaseExpression") {
        return
      }

      const subjectType: Type | undefined = undefined

      const result =
        checkCaseExhaustiveness(
          registry as any,
          subjectType as any,
          expr.branches
        )

      if (result) {

        diagnostics.push({
          severity: "error",
          message: result.message,
          span: expr.span
        })

      }

    }
  )

}

/* -----------------------------------------------------------
   Expression visitor
----------------------------------------------------------- */

function visitExpression(
  expression: AST.Expression,
  visitor: (expr: AST.Expression) => void
) {

  visitor(expression)

  switch (expression.kind) {

    case "CallExpression":

      visitExpression(expression.callee, visitor)

      for (const arg of expression.arguments) {
        visitExpression(arg, visitor)
      }

      return

    case "BinaryExpression":

      visitExpression(expression.left, visitor)
      visitExpression(expression.right, visitor)

      return

    case "LambdaExpression":

      visitExpression(expression.body, visitor)

      return

    case "LetExpression":

      for (const binding of expression.bindings) {
        visitExpression(binding.value, visitor)
      }

      visitExpression(expression.body, visitor)

      return

    case "CaseExpression":

      visitExpression(expression.subject, visitor)

      for (const branch of expression.branches) {
        visitExpression(branch.body, visitor)
      }

      return

    case "TupleExpression":
    case "ListExpression":

      for (const item of expression.items) {
        visitExpression(item, visitor)
      }

      return

    case "RecordExpression":

      for (const field of expression.fields) {
        visitExpression(field.value, visitor)
      }

      return

    case "RecordUpdateExpression":

      visitExpression(expression.base, visitor)
      for (const field of expression.fields) {
        visitExpression(field.value, visitor)
      }

      return

    case "FieldAccessExpression":

      visitExpression(expression.target, visitor)

      return

    case "ParenthesizedExpression":

      visitExpression(expression.expression, visitor)

      return

    case "IdentifierExpression":
    case "QualifiedIdentifierExpression":
    case "IntegerLiteralExpression":
    case "FloatLiteralExpression":
    case "StringLiteralExpression":
    case "CharLiteralExpression":
    case "BooleanLiteralExpression":
    case "UnitExpression":

      return

  }

}

/* -----------------------------------------------------------
   Discarded function value diagnostics
----------------------------------------------------------- */

function collectDiscardedFunctionDiagnostics(
  expression: AST.Expression,
  nodeTypes: Map<string, import("../types/types.js").Type>,
  diagnostics: TypeDiagnostic[]
) {
  visitExpression(expression, expr => {
    if (expr.kind !== "LetExpression") return;

    for (const binding of expr.bindings) {
      // Check for _ = <expr> where the binding's resolved type is a function
      // This catches `_ = Slog.info "msg"` (missing second arg — type is List Any -> Unit)
      // but not `_ = Db.exec db query args` (fully applied — type is Result Error Int)
      if (binding.pattern.kind === "WildcardPattern" ||
          (binding.pattern.kind === "VariablePattern" && binding.pattern.name === "_")) {

        // Use the pattern span to find the binding's resolved type
        const patSpan = binding.pattern.span;
        if (patSpan) {
          const key = `${patSpan.start.line}:${patSpan.start.column}`;
          const bindingType = nodeTypes.get(key);
          if (bindingType && bindingType.kind === "TypeFunction") {
            diagnostics.push({
              severity: "warning",
              message: `Discarded function value — this expression returns a function, which suggests a missing argument.`,
              span: binding.value.span || patSpan
            });
          }
        }
      }
    }
  });
}

/* -----------------------------------------------------------
   Go reserved word diagnostics
----------------------------------------------------------- */

const GO_RESERVED_WORDS = new Set([
  "break", "case", "chan", "const", "continue", "default", "defer", "else",
  "fallthrough", "for", "func", "go", "goto", "if", "import", "interface",
  "map", "package", "range", "return", "select", "struct", "switch", "type",
  "var", "true", "false", "nil", "int", "string", "bool", "float64", "any",
  "error", "len", "cap", "make", "new", "append", "copy", "delete", "panic",
  "recover", "close", "print", "println", "complex", "real", "imag",
])

function isGoReserved(name: string): boolean {
  return GO_RESERVED_WORDS.has(name)
}

function warnGoReserved(
  name: string,
  what: string,
  span: AST.NodeBase["span"],
  diagnostics: TypeDiagnostic[]
) {
  if (isGoReserved(name)) {
    diagnostics.push({
      severity: "warning",
      message: `${what} '${name}' clashes with a Go reserved word and will be renamed to '${name}_' in generated code.`,
      span
    })
  }
}

function collectGoReservedWordDiagnostics(
  module: AST.Module,
  diagnostics: TypeDiagnostic[]
) {
  for (const decl of module.declarations) {
    switch (decl.kind) {
      case "FunctionDeclaration":
        warnGoReserved(decl.name, "Function", decl.span, diagnostics)
        // Check parameters
        for (const param of decl.parameters) {
          checkPatternForGoReserved(param.pattern, "Parameter", diagnostics)
        }
        // Check let bindings and lambda params in the body
        checkExprForGoReserved(decl.body, diagnostics)
        break

      case "TypeDeclaration":
        warnGoReserved(decl.name, "Type", decl.span, diagnostics)
        for (const variant of decl.variants) {
          warnGoReserved(variant.name, "Constructor", variant.span, diagnostics)
        }
        break

      case "TypeAliasDeclaration":
        warnGoReserved(decl.name, "Type alias", decl.span, diagnostics)
        break
    }
  }
}

function checkPatternForGoReserved(
  pattern: AST.Pattern,
  what: string,
  diagnostics: TypeDiagnostic[]
) {
  switch (pattern.kind) {
    case "VariablePattern":
      if (pattern.name !== "_") {
        warnGoReserved(pattern.name, what, pattern.span, diagnostics)
      }
      break
    case "ConstructorPattern":
      for (const arg of pattern.arguments) {
        checkPatternForGoReserved(arg, what, diagnostics)
      }
      break
    case "TuplePattern":
    case "ListPattern":
      for (const item of pattern.items) {
        checkPatternForGoReserved(item, what, diagnostics)
      }
      break
    case "ConsPattern":
      checkPatternForGoReserved(pattern.head, what, diagnostics)
      checkPatternForGoReserved(pattern.tail, what, diagnostics)
      break
    case "AsPattern":
      warnGoReserved(pattern.name, what, pattern.span, diagnostics)
      checkPatternForGoReserved(pattern.pattern, what, diagnostics)
      break
    case "RecordPattern":
      for (const field of pattern.fields) {
        warnGoReserved(field, what, pattern.span, diagnostics)
      }
      break
  }
}

function checkExprForGoReserved(
  expression: AST.Expression,
  diagnostics: TypeDiagnostic[]
) {
  visitExpression(expression, expr => {
    if (expr.kind === "LetExpression") {
      for (const binding of expr.bindings) {
        checkPatternForGoReserved(binding.pattern, "Variable", diagnostics)
      }
    }
    if (expr.kind === "LambdaExpression") {
      for (const param of expr.parameters) {
        checkPatternForGoReserved(param.pattern, "Parameter", diagnostics)
      }
    }
    if (expr.kind === "CaseExpression") {
      for (const branch of expr.branches) {
        checkPatternForGoReserved(branch.pattern, "Variable", diagnostics)
      }
    }
  })
}

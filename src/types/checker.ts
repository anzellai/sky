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
import { inferTopLevel } from "./infer.js"
import { registerAdts } from "./adt.js"
import { checkCaseExhaustiveness } from "./exhaustiveness.js"

import {
  type Type,
  type Scheme,
  typeConstant,
  mono
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

  for (const declaration of module.declarations) {
      if (declaration.kind === "TypeAnnotation") {
          typeAnnotations.set(declaration.name, declaration);
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

  for (const set of bindingSets) {

    for (const value of set.values) {

      const type =
        value.skyType
          ? parseForeignType(value.skyType)
          : typeConstant("Foreign")

      next =
        next.extend(
          value.skyName,
          mono(type)
        )

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

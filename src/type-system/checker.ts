// src/type-system/checker.ts
// Sky type checking pipeline with ADT registration + exhaustiveness checking

import * as AST from "../ast.js";
import { TypeEnvironment, createPreludeEnvironment } from "./env.js";
import { inferTopLevel } from "./infer.js";
import { registerAdts } from "./adt.js";
import { checkCaseExhaustiveness } from "./exhaustiveness.js";
import type { Scheme, Type } from "./../types.js";

export interface TypeDiagnostic {
  readonly severity: "error" | "warning";
  readonly message: string;
  readonly span: AST.NodeBase["span"];
  readonly hint?: string;
}

export interface TypedDeclarationInfo {
  readonly name: string;
  readonly scheme: Scheme;
  readonly pretty: string;
}

export interface TypeCheckResult {
  readonly environment: TypeEnvironment;
  readonly declarations: readonly TypedDeclarationInfo[];
  readonly diagnostics: readonly TypeDiagnostic[];
}

export function checkModule(module: AST.Module): TypeCheckResult {

  let env = createPreludeEnvironment();

  const declarations: TypedDeclarationInfo[] = [];
  const diagnostics: TypeDiagnostic[] = [];

  // ------------------------------------------------------------
  // 1. Register ADTs first so constructors are available
  // ------------------------------------------------------------

  const adtRegistration = registerAdts(env, module.declarations);

  env = adtRegistration.environment;

  for (const d of adtRegistration.diagnostics) {
    diagnostics.push({
      severity: "error",
      message: d,
      span: module.span
    });
  }

  if (diagnostics.length > 0) {
    return {
      environment: env,
      declarations,
      diagnostics
    };
  }

  // ------------------------------------------------------------
  // 2. Infer top-level functions
  // ------------------------------------------------------------

  for (const declaration of module.declarations) {

    switch (declaration.kind) {

      case "FunctionDeclaration": {

        try {

          const inferred = inferTopLevel(adtRegistration.registry, env, declaration);

          declarations.push(inferred);

          env = env.extend(inferred.name, inferred.scheme);

          // -----------------------------------------------
          // Exhaustiveness check inside the function body
          // -----------------------------------------------

          collectCaseDiagnostics(
            adtRegistration.registry,
            declaration.body,
            diagnostics
          );

        } catch (error) {

          diagnostics.push({
            severity: "error",
            message: error instanceof Error ? error.message : String(error),
            span: declaration.span,
            hint: `Could not infer the type of ${declaration.name}.`
          });

        }

        break;
      }

      case "TypeDeclaration":
      case "TypeAliasDeclaration":
      case "ForeignImportDeclaration":
        // already handled or ignored for now
        break;

    }

  }

  return {
    environment: env,
    declarations,
    diagnostics
  };

}

export function formatTypeCheckResult(result: TypeCheckResult): string {

  const lines: string[] = [];

  for (const decl of result.declarations) {
    lines.push(`${decl.name} : ${decl.pretty}`);
  }

  if (result.diagnostics.length > 0) {

    if (lines.length > 0) {
      lines.push("");
    }

    for (const diagnostic of result.diagnostics) {
      lines.push(`${diagnostic.severity}: ${diagnostic.message}`);
    }

  }

  return lines.join("\n");

}

// ------------------------------------------------------------
// Internal helpers
// ------------------------------------------------------------

function collectCaseDiagnostics(
  registry: any,
  expression: AST.Expression,
  diagnostics: TypeDiagnostic[]
) {
  visitExpression(expression, (expr) => {

    if (expr.kind !== "CaseExpression") return;

    const subjectType: Type | undefined = undefined;

    const result = checkCaseExhaustiveness(
      registry,
      subjectType as any,
      expr.branches
    );

    if (result) {
      diagnostics.push({
        severity: "error",
        message: result.message,
        span: expr.span
      });
    }

  });
}

function visitExpression(
  expression: AST.Expression,
  visitor: (expr: AST.Expression) => void
) {

  visitor(expression);

  switch (expression.kind) {

    case "CallExpression":
      visitExpression(expression.callee, visitor);
      for (const arg of expression.arguments) visitExpression(arg, visitor);
      return;

    case "BinaryExpression":
      visitExpression(expression.left, visitor);
      visitExpression(expression.right, visitor);
      return;

    case "LambdaExpression":
      visitExpression(expression.body, visitor);
      return;

    case "LetExpression":
      for (const binding of expression.bindings) {
        visitExpression(binding.value, visitor);
      }
      visitExpression(expression.body, visitor);
      return;

    case "CaseExpression":
      visitExpression(expression.subject, visitor);
      for (const branch of expression.branches) {
        visitExpression(branch.body, visitor);
      }
      return;

    case "TupleExpression":
    case "ListExpression":
      for (const item of expression.items) visitExpression(item, visitor);
      return;

    case "RecordExpression":
      for (const field of expression.fields) {
        visitExpression(field.value, visitor);
      }
      return;

    case "FieldAccessExpression":
      visitExpression(expression.target, visitor);
      return;

    case "ParenthesizedExpression":
      visitExpression(expression.expression, visitor);
      return;

    case "IdentifierExpression":
    case "QualifiedIdentifierExpression":
    case "IntegerLiteralExpression":
    case "FloatLiteralExpression":
    case "StringLiteralExpression":
    case "CharLiteralExpression":
    case "BooleanLiteralExpression":
    case "UnitExpression":
      return;

  }

}

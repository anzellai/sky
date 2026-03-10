// src/type-system/exhaustiveness.ts
// Exhaustiveness checking for Sky pattern matches
//
// Current scope:
// - wildcard patterns
// - variable patterns
// - constructor patterns for registered ADTs
// - literal patterns (best-effort for Bool)
//
// This is intentionally conservative:
// - it reports definite gaps it can prove
// - it does not try to prove totality for all tuple/list/literal spaces yet
//
// Main use case:
//   case maybe of
//       Just x -> x
//
// should report missing: Nothing

import * as AST from "../ast.js";
import type { AdtRegistry, RegisteredAdt } from "./adt.js";
import type { Type } from "./../types.js";

export interface ExhaustivenessDiagnostic {
  readonly message: string;
  readonly missingPatterns: readonly string[];
}

export function checkCaseExhaustiveness(
  registry: AdtRegistry,
  subjectType: Type,
  branches: readonly AST.CaseBranch[],
): ExhaustivenessDiagnostic | undefined {
  const patterns = branches.map((branch) => branch.pattern);

  if (patterns.some(isCatchAllPattern)) {
    return undefined;
  }

  const boolResult = checkBooleanExhaustiveness(patterns, subjectType);
  if (boolResult) {
    return boolResult;
  }

  const adtResult = checkAdtExhaustiveness(registry, patterns, subjectType);
  if (adtResult) {
    return adtResult;
  }

  return undefined;
}

export function collectCaseExhaustivenessDiagnostics(
  registry: AdtRegistry,
  expression: AST.Expression,
  getExpressionType?: (expr: AST.Expression) => Type | undefined,
): ExhaustivenessDiagnostic[] {
  const diagnostics: ExhaustivenessDiagnostic[] = [];
  visitExpression(expression, (expr) => {
    if (expr.kind !== "CaseExpression") {
      return;
    }

    const subjectType = getExpressionType?.(expr.subject);
    if (!subjectType) {
      return;
    }

    const diagnostic = checkCaseExhaustiveness(registry, subjectType, expr.branches);
    if (diagnostic) {
      diagnostics.push(diagnostic);
    }
  });
  return diagnostics;
}

function checkBooleanExhaustiveness(
  patterns: readonly AST.Pattern[],
  subjectType: Type,
): ExhaustivenessDiagnostic | undefined {
  if (!isBoolType(subjectType)) {
    return undefined;
  }

  const seen = new Set<boolean>();

  for (const pattern of patterns) {
    if (pattern.kind === "LiteralPattern" && typeof pattern.value === "boolean") {
      seen.add(pattern.value);
    }
  }

  const missing: string[] = [];
  if (!seen.has(true)) missing.push("true");
  if (!seen.has(false)) missing.push("false");

  if (missing.length === 0) {
    return undefined;
  }

  return {
    message: `Non-exhaustive pattern match. Missing cases: ${missing.join(", ")}`,
    missingPatterns: missing,
  };
}

function checkAdtExhaustiveness(
  registry: AdtRegistry,
  patterns: readonly AST.Pattern[],
  subjectType: Type,
): ExhaustivenessDiagnostic | undefined {
  const adt = findRegisteredAdtForType(registry, subjectType);
  if (!adt) {
    return undefined;
  }

  const seenConstructors = new Set<string>();

  for (const pattern of patterns) {
    if (pattern.kind === "ConstructorPattern") {
      const name = pattern.constructorName.parts[pattern.constructorName.parts.length - 1];
      seenConstructors.add(name);
    }
  }

  const missing = Object.keys(adt.constructors).filter((ctor) => !seenConstructors.has(ctor));

  if (missing.length === 0) {
    return undefined;
  }

  return {
    message: `Non-exhaustive pattern match for ${adt.name}. Missing cases: ${missing.join(", ")}`,
    missingPatterns: missing,
  };
}

function findRegisteredAdtForType(
  registry: AdtRegistry,
  subjectType: Type,
): RegisteredAdt | undefined {
  const typeName = getHeadTypeName(subjectType);
  if (!typeName) {
    return undefined;
  }

  return registry.types.get(typeName);
}

function getHeadTypeName(type: Type): string | undefined {
  switch (type.kind) {
    case "TypeConstant":
      return type.name;
    case "TypeApplication":
      return getHeadTypeName(type.constructor);
    default:
      return undefined;
  }
}

function isBoolType(type: Type): boolean {
  return type.kind === "TypeConstant" && type.name === "Bool";
}

function isCatchAllPattern(pattern: AST.Pattern): boolean {
  return pattern.kind === "WildcardPattern" || pattern.kind === "VariablePattern";
}

function visitExpression(
  expression: AST.Expression,
  visitor: (expr: AST.Expression) => void,
): void {
  visitor(expression);

  switch (expression.kind) {
    case "CallExpression":
      visitExpression(expression.callee, visitor);
      for (const arg of expression.arguments) {
        visitExpression(arg, visitor);
      }
      return;

    case "BinaryExpression":
      visitExpression(expression.left, visitor);
      visitExpression(expression.right, visitor);
      return;

    case "IfExpression":
      visitExpression(expression.condition, visitor);
      visitExpression(expression.thenBranch, visitor);
      visitExpression(expression.elseBranch, visitor);
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
      for (const item of expression.items) {
        visitExpression(item, visitor);
      }
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

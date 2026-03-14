// src/type-system/patterns.ts
// Typed pattern inference / checking for Sky
//
// Responsibilities:
// - infer bindings introduced by patterns
// - validate constructor patterns against ADT constructor schemes
// - return substitutions + bound variable types
// - provide a reusable primitive for case expressions, let patterns,
//   lambda parameters, and future exhaustiveness checks

import * as AST from "../ast/ast.js";
import { TypeEnvironment } from "./env.js";
import { lookupConstructorScheme, type AdtRegistry } from "./adt.js";
import {
  Type,
  Scheme,
  Substitution,
  emptySubstitution,
  composeSubstitutions,
  applySubstitution,
  instantiate,
  freshTypeVariable,
  functionType,
} from "../types/types.js";
import { unify } from "./unify.js";

export interface PatternCheckResult {
  readonly substitution: Substitution;
  readonly bindings: Readonly<Record<string, Type>>;
}

export function inferPattern(
  registry: AdtRegistry,
  env: TypeEnvironment,
  pattern: AST.Pattern,
  expectedType: Type,
): PatternCheckResult {
  switch (pattern.kind) {
    case "WildcardPattern":
      return {
        substitution: emptySubstitution(),
        bindings: {},
      };

    case "VariablePattern":
      return {
        substitution: emptySubstitution(),
        bindings: {
          [pattern.name]: expectedType,
        },
      };

    case "LiteralPattern":
      return inferLiteralPattern(pattern, expectedType);

    case "TuplePattern":
      return inferTuplePattern(registry, env, pattern, expectedType);

    case "ListPattern":
      return inferListPattern(registry, env, pattern, expectedType);

    case "ConstructorPattern":
      return inferConstructorPattern(registry, env, pattern, expectedType);
  }
}

export function extendEnvironmentWithPatternBindings(
  environment: TypeEnvironment,
  bindings: Readonly<Record<string, Type>>,
): TypeEnvironment {
  const schemes: Record<string, Scheme> = {};

  for (const [name, type] of Object.entries(bindings)) {
    schemes[name] = {
      quantified: [],
      type,
    };
  }

  return environment.extendMany(schemes);
}

function inferLiteralPattern(
  pattern: AST.LiteralPattern,
  expectedType: Type,
): PatternCheckResult {
  const literalType = inferLiteralType(pattern.value);
  const substitution = unify(expectedType, literalType);

  return {
    substitution,
    bindings: {},
  };
}

function inferTuplePattern(
  registry: AdtRegistry,
  env: TypeEnvironment,
  pattern: AST.TuplePattern,
  expectedType: Type,
): PatternCheckResult {
  const itemTypes = pattern.items.map(() => freshTypeVariable());

  const tupleLikeType: Type = {
    kind: "TypeTuple",
    items: itemTypes,
  };

  let currentSub = unify(expectedType, tupleLikeType);
  let bindings: Record<string, Type> = {};

  for (let i = 0; i < pattern.items.length; i += 1) {
    const itemPattern = pattern.items[i];
    const itemExpected = applySubstitution(itemTypes[i], currentSub);

    const result = inferPattern(registry, env, itemPattern, itemExpected);

    currentSub = composeSubstitutions(result.substitution, currentSub);
    bindings = mergeBindings(bindings, applyBindingsSubstitution(result.bindings, currentSub));
  }

  return {
    substitution: currentSub,
    bindings,
  };
}

function inferListPattern(
  registry: AdtRegistry,
  env: TypeEnvironment,
  pattern: AST.ListPattern,
  expectedType: Type,
): PatternCheckResult {
  const elementType = freshTypeVariable();

  const listType: Type = {
    kind: "TypeApplication",
    constructor: { kind: "TypeConstant", name: "List" },
    arguments: [elementType],
  };

  let currentSub = unify(expectedType, listType);
  let bindings: Record<string, Type> = {};

  for (const itemPattern of pattern.items) {
    const result = inferPattern(
      registry,
      env,
      itemPattern,
      applySubstitution(elementType, currentSub),
    );

    currentSub = composeSubstitutions(result.substitution, currentSub);
    bindings = mergeBindings(bindings, applyBindingsSubstitution(result.bindings, currentSub));
  }

  return {
    substitution: currentSub,
    bindings,
  };
}

function inferConstructorPattern(
  registry: AdtRegistry,
  env: TypeEnvironment,
  pattern: AST.ConstructorPattern,
  expectedType: Type,
): PatternCheckResult {
  const constructorName = pattern.constructorName.parts[pattern.constructorName.parts.length - 1];
  let constructorScheme = lookupConstructorScheme(registry, constructorName);

  if (!constructorScheme) {
    // Fallback to environment
    constructorScheme = env.get(constructorName) || env.get(pattern.constructorName.parts.join("."));
  }

  if (!constructorScheme) {
    throw new Error(`Unknown constructor ${constructorName}`);
  }

  let constructorType = instantiate(constructorScheme);
  let currentSub = emptySubstitution();

  const argTypes: Type[] = [];

  for (let i = 0; i < pattern.arguments.length; i += 1) {
    if (constructorType.kind !== "TypeFunction") {
      throw new Error(`Constructor ${constructorName} used with too many arguments`);
    }

    argTypes.push(constructorType.from);
    constructorType = constructorType.to;
  }

  currentSub = unify(applySubstitution(constructorType, currentSub), expectedType);

  let bindings: Record<string, Type> = {};

  for (let i = 0; i < pattern.arguments.length; i += 1) {
    const argPattern = pattern.arguments[i];
    const argExpected = applySubstitution(argTypes[i], currentSub);

    const result = inferPattern(registry, env, argPattern, argExpected);
    currentSub = composeSubstitutions(result.substitution, currentSub);
    bindings = mergeBindings(bindings, applyBindingsSubstitution(result.bindings, currentSub));
  }

  return {
    substitution: currentSub,
    bindings,
  };
}

function inferLiteralType(value: AST.LiteralValue): Type {
  switch (typeof value) {
    case "number":
      return { kind: "TypeConstant", name: "Int" };
    case "string":
      return { kind: "TypeConstant", name: "String" };
    case "boolean":
      return { kind: "TypeConstant", name: "Bool" };
    default:
      throw new Error(`Unsupported literal pattern type: ${typeof value}`);
  }
}

function mergeBindings(
  left: Readonly<Record<string, Type>>,
  right: Readonly<Record<string, Type>>,
): Record<string, Type> {
  const merged: Record<string, Type> = { ...left };

  for (const [name, type] of Object.entries(right)) {
    if (name in merged) {
      throw new Error(`Duplicate variable ${name} in pattern`);
    }
    merged[name] = type;
  }

  return merged;
}

function applyBindingsSubstitution(
  bindings: Readonly<Record<string, Type>>,
  substitution: Substitution,
): Record<string, Type> {
  const next: Record<string, Type> = {};

  for (const [name, type] of Object.entries(bindings)) {
    next[name] = applySubstitution(type, substitution);
  }

  return next;
}

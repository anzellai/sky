// src/type-system/adt.ts
// Algebraic data type registration for Sky Hindley–Milner inference
//
// Responsibilities:
// - register `type` declarations in the type environment
// - expose constructor schemes
// - support later case-expression and pattern typing
//
// Example:
//   type Maybe a = Just a | Nothing
//
// Produces constructor schemes:
//   Just    : a -> Maybe a
//   Nothing : Maybe a

import * as AST from "../ast/ast.js";
import {
  Scheme,
  Type,
  TypeApplication,
  curriedFunctionType,
  freshTypeVariable,
  mono,
  scheme,
  typeApplication,
  typeConstant,
  functionType,
  tupleType,
  recordType,
} from "../types/types.js";
import { TypeEnvironment } from "./env.js";

export interface RegisteredAdt {
  readonly name: string;
  readonly arity: number;
  readonly constructors: Readonly<Record<string, Scheme>>;
}

export interface AdtRegistry {
  readonly types: ReadonlyMap<string, RegisteredAdt>;
}

export interface RegisterAdtsResult {
  readonly environment: TypeEnvironment;
  readonly registry: AdtRegistry;
  readonly diagnostics: readonly string[];
}

export function registerAdts(
  environment: TypeEnvironment,
  declarations: readonly AST.Declaration[],
): RegisterAdtsResult {
  let env = environment.clone();
  const diagnostics: string[] = [];
  const types = new Map<string, RegisteredAdt>();

  for (const declaration of declarations) {
    if (declaration.kind !== "TypeDeclaration") {
      continue;
    }

    const result = registerTypeDeclaration(declaration);

    if (result.diagnostics.length > 0) {
      diagnostics.push(...result.diagnostics);
      continue;
    }

    types.set(result.adt.name, result.adt);

    for (const [ctorName, ctorScheme] of Object.entries(result.adt.constructors)) {
      env = env.extend(ctorName, ctorScheme);
    }
  }

  return {
    environment: env,
    registry: { types },
    diagnostics,
  };
}

interface RegisterTypeDeclarationResult {
  readonly adt: RegisteredAdt;
  readonly diagnostics: readonly string[];
}

function registerTypeDeclaration(
  declaration: AST.TypeDeclaration,
): RegisterTypeDeclarationResult {
  const diagnostics: string[] = [];

  const parameterMap = new Map<string, Type>();
  const quantifiedIds: number[] = [];

  for (const parameterName of declaration.typeParameters) {
    const variable = freshTypeVariable(parameterName);
    parameterMap.set(parameterName, variable);
    quantifiedIds.push(variable.id);
  }

  const resultType = buildSelfType(declaration.name, declaration.typeParameters, parameterMap);

  const constructors: Record<string, Scheme> = {};

  for (const variant of declaration.variants) {
    if (constructors[variant.name]) {
      diagnostics.push(`Duplicate constructor ${variant.name} in type ${declaration.name}`);
      continue;
    }

    const fieldTypes: Type[] = [];

    for (const field of variant.fields) {
      try {
        fieldTypes.push(convertAstType(field, parameterMap));
      } catch (error) {
        diagnostics.push(
          error instanceof Error
            ? `${variant.name}: ${error.message}`
            : `${variant.name}: ${String(error)}`,
        );
      }
    }

    if (fieldTypes.length !== variant.fields.length) {
      continue;
    }

    const ctorType = fieldTypes.length === 0
      ? resultType
      : curriedFunctionType([...fieldTypes, resultType]);

    constructors[variant.name] = scheme(quantifiedIds, ctorType);
  }

  return {
    adt: {
      name: declaration.name,
      arity: declaration.typeParameters.length,
      constructors,
    },
    diagnostics,
  };
}

function buildSelfType(
  typeName: string,
  typeParameters: readonly string[],
  parameterMap: ReadonlyMap<string, Type>,
): Type {
  const constructor = typeConstant(typeName);

  if (typeParameters.length === 0) {
    return constructor;
  }

  const args = typeParameters.map((name) => {
    const value = parameterMap.get(name);
    if (!value) {
      throw new Error(`Unknown type parameter ${name}`);
    }
    return value;
  });

  return typeApplication(constructor, args);
}

export function convertAstType(
  typeNode: AST.TypeExpression,
  parameterMap: ReadonlyMap<string, Type>,
): Type {
  switch (typeNode.kind) {
    case "TypeVariable": {
      const existing = parameterMap.get(typeNode.name);
      if (existing) {
        return existing;
      }
      return typeConstant(typeNode.name);
    }

    case "TypeReference": {
      const ctorName = typeNode.name.parts.join(".");
      const ctor = typeConstant(ctorName);

      if (typeNode.arguments.length === 0) {
        return ctor;
      }

      return typeApplication(
        ctor,
        typeNode.arguments.map((arg) => convertAstType(arg, parameterMap)),
      );
    }

    case "FunctionType":
      return functionType(
        convertAstType(typeNode.from, parameterMap),
        convertAstType(typeNode.to, parameterMap),
      );

    case "RecordType": {
      const fields: Record<string, Type> = {};
      for (const field of typeNode.fields) {
        fields[field.name] = convertAstType(field.type, parameterMap);
      }
      return recordType(fields);
    }

    default:
      return assertNever(typeNode);
  }
}

export function lookupConstructorScheme(
  registry: AdtRegistry,
  constructorName: string,
): Scheme | undefined {
  for (const adt of registry.types.values()) {
    const schemeValue = adt.constructors[constructorName];
    if (schemeValue) {
      return schemeValue;
    }
  }
  return undefined;
}

function assertNever(value: never): never {
  throw new Error(`Unhandled ADT type node ${(value as { kind?: string }).kind ?? "<unknown>"}`);
}

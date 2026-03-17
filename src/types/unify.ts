// src/type-system/unify.ts
// Robinson unification for Sky Hindley–Milner type inference

import {
  Type,
  TypeVariable,
  TypeFunction,
  TypeApplication,
  TypeTuple,
  TypeRecord,
  Substitution,
  emptySubstitution,
  substitution,
  applySubstitution,
  composeSubstitutions,
  isTypeVariable,
  formatType
} from "../types/types.js";

export class UnificationError extends Error {
  constructor(message: string) {
    super(message);
  }
}

function isJsValue(t: Type): boolean {
  if (t.kind === "TypeConstant") {
    return t.name === "JsValue" || t.name === "Foreign" || t.name === "Any" || t.name === "Sky.Interop.JsValue";
  }
  if (t.kind === "TypeApplication") {
    return isJsValue(t.constructor);
  }
  return false;
}

// Sky-native types that must match exactly during unification.
// Any PascalCase TypeConstant NOT in this set is assumed to originate
// from Go FFI and is treated permissively (Go interface satisfaction
// cannot be verified statically by the Sky type checker).
const SKY_NATIVE_TYPES = new Set([
  "Int", "Float", "String", "Bool", "Unit",
  "Result", "Maybe", "List", "Dict", "Map",
  "Cmd", "Sub", "Task", "Program",
  "Bytes", "Channel", "Tuple", "Error",
]);

function isForeignGoType(t: Type): boolean {
  if (t.kind !== "TypeConstant") return false;
  if (isJsValue(t)) return true;
  // If it's not a known Sky type, it's likely from Go FFI
  return !SKY_NATIVE_TYPES.has(t.name);
}

export function unify(a: Type, b: Type): Substitution {

  if (isJsValue(a) || isJsValue(b)) return emptySubstitution();

  if (a.kind === "TypeVariable") {
    return unifyVar(a, b);
  }

  if (b.kind === "TypeVariable") {
    return unifyVar(b, a);
  }

  if (a.kind === "TypeConstant" && b.kind === "TypeConstant") {

    if (a.name !== b.name) {
      // Allow Int and Float to unify
      if ((a.name === "Int" && b.name === "Float") || (a.name === "Float" && b.name === "Int")) {
        return emptySubstitution();
      }
      // Allow Go FFI types to unify with each other (Go interface satisfaction).
      // E.g., ResponseWriter unifies with Writer, Router with Handler.
      if (isForeignGoType(a) && isForeignGoType(b)) {
        return emptySubstitution();
      }
      throw new UnificationError(`Type mismatch: expected ${formatType(a)}, but found ${formatType(b)}`);
    }

    return emptySubstitution();
  }

  if (a.kind === "TypeFunction" && b.kind === "TypeFunction") {

    const s1 = unify(a.from, b.from);

    const s2 = unify(
      applySubstitution(a.to, s1),
      applySubstitution(b.to, s1)
    );

    return composeSubstitutions(s2, s1);
  }

  if (a.kind === "TypeApplication" && b.kind === "TypeApplication") {

    const s1 = unify(a.constructor, b.constructor);

    let current = s1;

    for (let i = 0; i < a.arguments.length; i++) {

      const left = applySubstitution(a.arguments[i], current);
      const right = applySubstitution(b.arguments[i], current);

      const s = unify(left, right);

      current = composeSubstitutions(s, current);

    }

    return current;
  }

  if (a.kind === "TypeTuple" && b.kind === "TypeTuple") {

    if (a.items.length !== b.items.length) {
      throw new UnificationError(`Tuple arity mismatch: expected ${formatType(a)}, but found ${formatType(b)}`);
    }

    let current = emptySubstitution();

    for (let i = 0; i < a.items.length; i++) {

      const s = unify(
        applySubstitution(a.items[i], current),
        applySubstitution(b.items[i], current)
      );

      current = composeSubstitutions(s, current);

    }

    return current;
  }

  if (a.kind === "TypeRecord" && b.kind === "TypeRecord") {

    const aKeys = Object.keys(a.fields);
    const bKeys = Object.keys(b.fields);

    if (aKeys.length !== bKeys.length) {
      throw new UnificationError(`Record field mismatch: expected ${formatType(a)}, but found ${formatType(b)}`);
    }

    let current = emptySubstitution();

    for (const key of aKeys) {

      if (!(key in b.fields)) {
        throw new UnificationError(`Missing field ${key} in ${formatType(b)}`);
      }

      const s = unify(
        applySubstitution(a.fields[key], current),
        applySubstitution(b.fields[key], current)
      );

      current = composeSubstitutions(s, current);

    }

    return current;
  }

  throw new UnificationError(`Cannot unify types: expected ${formatType(a)}, but found ${formatType(b)}`);
}

function unifyVar(variable: TypeVariable, type: Type): Substitution {

  if (isTypeVariable(type) && type.id === variable.id) {
    return emptySubstitution();
  }

  if (occurs(variable, type)) {
    throw new UnificationError(`Occurs check failed: cannot unify variable ${formatType(variable)} with ${formatType(type)}`);
  }

  return substitution([[variable.id, type]]);
}

function occurs(variable: TypeVariable, type: Type): boolean {

  switch (type.kind) {

    case "TypeVariable":
      return variable.id === type.id;

    case "TypeConstant":
      return false;

    case "TypeFunction":
      return occurs(variable, type.from) || occurs(variable, type.to);

    case "TypeApplication":
      return (
        occurs(variable, type.constructor) ||
        type.arguments.some(arg => occurs(variable, arg))
      );

    case "TypeTuple":
      return type.items.some(item => occurs(variable, item));

    case "TypeRecord":
      return Object.values(type.fields).some(field => occurs(variable, field));

  }
}

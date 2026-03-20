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
  formatType,
  freshTypeVariable,
  recordType
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

// Normalize TypeApplication("Tuple", [a, b]) to TypeTuple([a, b])
function normalizeTuple(t: Type): Type {
  if (t.kind === "TypeApplication" && t.constructor.kind === "TypeConstant" && t.constructor.name === "Tuple") {
    return { kind: "TypeTuple", items: t.arguments };
  }
  return t;
}

export function unify(a: Type, b: Type): Substitution {

  if (isJsValue(a) || isJsValue(b)) return emptySubstitution();

  // Normalize Tuple type applications to TypeTuple
  a = normalizeTuple(a);
  b = normalizeTuple(b);

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
      // Allow Char and String to unify (Char is a single-character String in Sky/Go)
      if ((a.name === "Char" && b.name === "String") || (a.name === "String" && b.name === "Char")) {
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
    // Row-polymorphic record unification:
    // Both records may have a "rest" type variable representing extra fields.
    // Common fields are unified. Extra fields on each side are collected
    // and assigned to the other side's rest variable.
    const aKeys = new Set(Object.keys(a.fields));
    const bKeys = new Set(Object.keys(b.fields));
    const commonKeys = [...aKeys].filter(k => bKeys.has(k));
    const aOnly = [...aKeys].filter(k => !bKeys.has(k));
    const bOnly = [...bKeys].filter(k => !aKeys.has(k));

    let current = emptySubstitution();

    // Unify common fields
    for (const key of commonKeys) {
      const s = unify(
        applySubstitution(a.fields[key], current),
        applySubstitution(b.fields[key], current)
      );
      current = composeSubstitutions(s, current);
    }

    // Handle extra fields via rest variables
    if (aOnly.length > 0 && !b.rest) {
      throw new UnificationError(`Missing field ${aOnly[0]} in ${formatType(b)}`);
    }
    if (bOnly.length > 0 && !a.rest) {
      throw new UnificationError(`Missing field ${bOnly[0]} in ${formatType(a)}`);
    }

    if (aOnly.length > 0 || bOnly.length > 0) {
      // Both sides have extra fields — use a shared fresh rest variable
      // to avoid circular bindings (rest1 -> {f, ...rest2} -> {g, ...rest1})
      const sharedRest = (a.rest && b.rest) ? freshTypeVariable() : undefined;

      if (aOnly.length > 0 && b.rest) {
        const extraFields: Record<string, Type> = {};
        for (const k of aOnly) extraFields[k] = applySubstitution(a.fields[k], current);
        const extraRecord = recordType(extraFields, sharedRest || a.rest);
        const s = unifyVar(b.rest, extraRecord);
        current = composeSubstitutions(s, current);
      }

      if (bOnly.length > 0 && a.rest) {
        const extraFields: Record<string, Type> = {};
        for (const k of bOnly) extraFields[k] = applySubstitution(b.fields[k], current);
        const extraRecord = recordType(extraFields, sharedRest || b.rest);
        const aRestResolved = applySubstitution(a.rest, current);
        if (aRestResolved.kind === "TypeVariable") {
          const s = unifyVar(aRestResolved, extraRecord);
          current = composeSubstitutions(s, current);
        }
      }
    } else {
      // No extra fields on either side
      if (a.rest && b.rest) {
        // Both open — unify rest variables
        const aRestResolved = applySubstitution(a.rest, current);
        const bRestResolved = applySubstitution(b.rest, current);
        if (aRestResolved.kind === "TypeVariable" && bRestResolved.kind === "TypeVariable" && aRestResolved.id !== bRestResolved.id) {
          const s = unifyVar(aRestResolved, bRestResolved);
          current = composeSubstitutions(s, current);
        }
      } else if (a.rest && !b.rest) {
        // a is open, b is closed — close a's rest
        const aRestResolved = applySubstitution(a.rest, current);
        if (aRestResolved.kind === "TypeVariable") {
          const s = unifyVar(aRestResolved, recordType({}));
          current = composeSubstitutions(s, current);
        }
      } else if (b.rest && !a.rest) {
        // b is open, a is closed — close b's rest
        const bRestResolved = applySubstitution(b.rest, current);
        if (bRestResolved.kind === "TypeVariable") {
          const s = unifyVar(bRestResolved, recordType({}));
          current = composeSubstitutions(s, current);
        }
      }
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
      if (Object.values(type.fields).some(field => occurs(variable, field))) return true;
      if (type.rest && type.rest.id === variable.id) return true;
      return false;

  }
}

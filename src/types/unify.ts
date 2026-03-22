// src/type-system/unify.ts
// Robinson unification for Sky Hindley–Milner type inference

import {
  Type,
  TypeVariable,
  Substitution,
  emptySubstitution,
  substitution,
  applySubstitution,
  composeSubstitutions,
  isTypeVariable,
  formatTypeNormalized,
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
      // Go FFI types are opaque — they must match exactly by name.
      // JsValue/Foreign/Any are universal unifiers (handled above at line 62),
      // but named Go types like Db, Rows, Response etc. are strict.
      throw new UnificationError(`Type mismatch: expected ${formatTypeNormalized(a)}, but found ${formatTypeNormalized(b)}`);
    }

    return emptySubstitution();
  }

  // VNode is a recursive record type used for HTML views.
  // Allow it to unify with record types and String (backward compat).
  if ((a.kind === "TypeConstant" && a.name === "VNode" && (b.kind === "TypeRecord" || (b.kind === "TypeConstant" && b.name === "String"))) ||
      (b.kind === "TypeConstant" && b.name === "VNode" && (a.kind === "TypeRecord" || (a.kind === "TypeConstant" && a.name === "String")))) {
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
      throw new UnificationError(`Tuple arity mismatch: expected ${formatTypeNormalized(a)}, but found ${formatTypeNormalized(b)}`);
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
      throw new UnificationError(`Missing field ${aOnly[0]} in ${formatTypeNormalized(b)}`);
    }
    if (bOnly.length > 0 && !a.rest) {
      throw new UnificationError(`Missing field ${bOnly[0]} in ${formatTypeNormalized(a)}`);
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

  throw new UnificationError(`Cannot unify types: expected ${formatTypeNormalized(a)}, but found ${formatTypeNormalized(b)}`);
}

function mergeConstraints(a?: readonly string[], b?: readonly string[]): string[] {
  const set = new Set<string>([...(a || []), ...(b || [])]);
  return Array.from(set);
}

function validateConstraint(constraint: string, type: Type, _variable: TypeVariable): void {
  switch (constraint) {
    case "comparable":
      if (!isComparable(type)) {
        throw new UnificationError(
          `Type ${formatTypeNormalized(type)} is not comparable. Only Int, Float, String, Bool, Char, and tuples/lists of comparable types can be compared.`
        );
      }
      break;
    case "number":
      if (!isNumber(type)) {
        throw new UnificationError(
          `Type ${formatTypeNormalized(type)} is not a number. Only Int and Float support arithmetic operations.`
        );
      }
      break;
    case "appendable":
      if (!isAppendable(type)) {
        throw new UnificationError(
          `Type ${formatTypeNormalized(type)} is not appendable. Only String and List types support the ++ operator.`
        );
      }
      break;
  }
}

function isComparable(type: Type): boolean {
  if (type.kind === "TypeConstant") {
    return ["Int", "Float", "String", "Bool", "Char"].includes(type.name);
  }
  if (type.kind === "TypeTuple") {
    return type.items.every(isComparable);
  }
  if (type.kind === "TypeApplication" && type.constructor.kind === "TypeConstant" && type.constructor.name === "List") {
    return type.arguments.length === 1 && isComparable(type.arguments[0]);
  }
  // Type variables are potentially comparable (will be checked when resolved)
  if (type.kind === "TypeVariable") return true;
  return false;
}

function isNumber(type: Type): boolean {
  if (type.kind === "TypeConstant") {
    return type.name === "Int" || type.name === "Float";
  }
  if (type.kind === "TypeVariable") return true;
  return false;
}

function isAppendable(type: Type): boolean {
  if (type.kind === "TypeConstant") {
    return type.name === "String";
  }
  if (type.kind === "TypeApplication" && type.constructor.kind === "TypeConstant" && type.constructor.name === "List") {
    return true;
  }
  if (type.kind === "TypeVariable") return true;
  return false;
}

function unifyVar(variable: TypeVariable, type: Type): Substitution {

  if (isTypeVariable(type) && type.id === variable.id) {
    return emptySubstitution();
  }

  if (occurs(variable, type)) {
    throw new UnificationError(`Occurs check failed: cannot unify variable ${formatTypeNormalized(variable)} with ${formatTypeNormalized(type)}`);
  }

  // If the variable has constraints, validate them against the substituted type
  if (variable.constraints && variable.constraints.length > 0 && !isTypeVariable(type)) {
    for (const constraint of variable.constraints) {
      validateConstraint(constraint, type, variable);
    }
  }

  // If substituting into another type variable, transfer constraints
  if (isTypeVariable(type) && variable.constraints && variable.constraints.length > 0) {
    const merged = mergeConstraints(variable.constraints, (type as TypeVariable).constraints);
    if (merged.length > 0) {
      const constrained: TypeVariable = { kind: "TypeVariable", id: type.id, name: type.name, constraints: merged };
      return substitution([[variable.id, constrained]]);
    }
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

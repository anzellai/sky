// src/type-system/types.ts
// Sky Hindley–Milner type representation
//
// Design goals:
// - immutable type structures
// - explicit schemes for generalized bindings
// - friendly to unification / inference / diagnostics

export type Type =
  | TypeVariable
  | TypeConstant
  | TypeFunction
  | TypeApplication
  | TypeTuple
  | TypeRecord;

export interface TypeVariable {
  readonly kind: "TypeVariable";
  readonly id: number;
  readonly name?: string;
}

export interface TypeConstant {
  readonly kind: "TypeConstant";
  readonly name: string;
}

export interface TypeFunction {
  readonly kind: "TypeFunction";
  readonly from: Type;
  readonly to: Type;
}

export interface TypeApplication {
  readonly kind: "TypeApplication";
  readonly constructor: Type;
  readonly arguments: readonly Type[];
}

export interface TypeTuple {
  readonly kind: "TypeTuple";
  readonly items: readonly Type[];
}

export interface TypeRecord {
  readonly kind: "TypeRecord";
  readonly fields: Readonly<Record<string, Type>>;
  readonly rest?: TypeVariable;  // Row polymorphism: open record with extra fields
}

export interface Scheme {
  readonly quantified: readonly number[];
  readonly type: Type;
}

export interface Substitution {
  readonly mappings: ReadonlyMap<number, Type>;
}

export const TYPE_INT: TypeConstant = { kind: "TypeConstant", name: "Int" };
export const TYPE_FLOAT: TypeConstant = { kind: "TypeConstant", name: "Float" };
export const TYPE_STRING: TypeConstant = { kind: "TypeConstant", name: "String" };
export const TYPE_BOOL: TypeConstant = { kind: "TypeConstant", name: "Bool" };
export const TYPE_CHAR: TypeConstant = { kind: "TypeConstant", name: "Char" };
export const TYPE_UNIT: TypeConstant = { kind: "TypeConstant", name: "Unit" };

let nextTypeVariableId = 0;

export function freshTypeVariable(name?: string): TypeVariable {
  nextTypeVariableId += 1;
  return {
    kind: "TypeVariable",
    id: nextTypeVariableId,
    name,
  };
}

export function typeConstant(name: string): TypeConstant {
  return {
    kind: "TypeConstant",
    name,
  };
}

export function functionType(from: Type, to: Type): TypeFunction {
  return {
    kind: "TypeFunction",
    from,
    to,
  };
}

export function curriedFunctionType(parts: readonly Type[]): Type {
  if (parts.length === 0) {
    throw new Error("curriedFunctionType requires at least one type part");
  }

  if (parts.length === 1) {
    return parts[0];
  }

  let current = functionType(parts[parts.length - 2], parts[parts.length - 1]);

  for (let i = parts.length - 3; i >= 0; i -= 1) {
    current = functionType(parts[i], current);
  }

  return current;
}

export function typeApplication(constructor: Type, arguments_: readonly Type[]): TypeApplication {
  return {
    kind: "TypeApplication",
    constructor,
    arguments: [...arguments_],
  };
}

export function tupleType(items: readonly Type[]): TypeTuple {
  return {
    kind: "TypeTuple",
    items: [...items],
  };
}

export function recordType(fields: Readonly<Record<string, Type>>, rest?: TypeVariable): TypeRecord {
  return {
    kind: "TypeRecord",
    fields: { ...fields },
    ...(rest ? { rest } : {}),
  };
}

export function mono(type: Type): Scheme {
  return {
    quantified: [],
    type,
  };
}

export function scheme(quantified: readonly number[], type: Type): Scheme {
  return {
    quantified: [...quantified],
    type,
  };
}

export function emptySubstitution(): Substitution {
  return {
    mappings: new Map(),
  };
}

export function substitution(entries: readonly (readonly [number, Type])[]): Substitution {
  return {
    mappings: new Map(entries),
  };
}

export function isTypeVariable(type: Type): type is TypeVariable {
  return type.kind === "TypeVariable";
}

export function isFunctionType(type: Type): type is TypeFunction {
  return type.kind === "TypeFunction";
}

export function freeTypeVariables(type: Type): ReadonlySet<number> {
  const result = new Set<number>();
  collectFreeTypeVariables(type, result);
  return result;
}

export function freeTypeVariablesInScheme(value: Scheme): ReadonlySet<number> {
  const vars = new Set(freeTypeVariables(value.type));
  for (const quantified of value.quantified) {
    vars.delete(quantified);
  }
  return vars;
}

function collectFreeTypeVariables(type: Type, out: Set<number>): void {
  switch (type.kind) {
    case "TypeVariable":
      out.add(type.id);
      return;

    case "TypeConstant":
      return;

    case "TypeFunction":
      collectFreeTypeVariables(type.from, out);
      collectFreeTypeVariables(type.to, out);
      return;

    case "TypeApplication":
      collectFreeTypeVariables(type.constructor, out);
      for (const arg of type.arguments) {
        collectFreeTypeVariables(arg, out);
      }
      return;

    case "TypeTuple":
      for (const item of type.items) {
        collectFreeTypeVariables(item, out);
      }
      return;

    case "TypeRecord":
      for (const value of Object.values(type.fields)) {
        collectFreeTypeVariables(value, out);
      }
      if (type.rest) {
        out.add(type.rest.id);
      }
      return;
  }
}

export function applySubstitution(type: Type, sub: Substitution): Type {
  switch (type.kind) {
    case "TypeVariable": {
      const replacement = sub.mappings.get(type.id);
      // Guard against self-referencing substitutions (id maps to itself)
      if (replacement && replacement.kind === "TypeVariable" && replacement.id === type.id) {
        return type;
      }
      return replacement ? applySubstitution(replacement, sub) : type;
    }

    case "TypeConstant":
      return type;

    case "TypeFunction":
      return functionType(
        applySubstitution(type.from, sub),
        applySubstitution(type.to, sub),
      );

    case "TypeApplication":
      return typeApplication(
        applySubstitution(type.constructor, sub),
        type.arguments.map((arg) => applySubstitution(arg, sub)),
      );

    case "TypeTuple":
      return tupleType(type.items.map((item) => applySubstitution(item, sub)));

    case "TypeRecord": {
      const next: Record<string, Type> = {};
      for (const [key, value] of Object.entries(type.fields)) {
        next[key] = applySubstitution(value, sub);
      }
      let newRest = type.rest;
      if (type.rest) {
        const replacement = sub.mappings.get(type.rest.id);
        if (replacement) {
          if (replacement.kind === "TypeRecord") {
            // Merge fields from the resolved record directly (no recursive apply
            // on the whole record to avoid cycles — fields are applied individually)
            for (const [k, v] of Object.entries(replacement.fields)) {
              if (!(k in next)) next[k] = applySubstitution(v, sub);
            }
            newRest = replacement.rest;
          } else if (replacement.kind === "TypeVariable") {
            newRest = replacement;
          } else {
            newRest = undefined;
          }
        }
      }
      return recordType(next, newRest);
    }
  }
}

export function applySubstitutionToScheme(value: Scheme, sub: Substitution): Scheme {
  const filtered = new Map<number, Type>();
  for (const [key, mapped] of sub.mappings.entries()) {
    if (!value.quantified.includes(key)) {
      filtered.set(key, mapped);
    }
  }

  return scheme(value.quantified, applySubstitution(value.type, { mappings: filtered }));
}

export function composeSubstitutions(left: Substitution, right: Substitution): Substitution {
  const composed = new Map<number, Type>();

  for (const [key, value] of right.mappings.entries()) {
    composed.set(key, applySubstitution(value, left));
  }

  for (const [key, value] of left.mappings.entries()) {
    composed.set(key, value);
  }

  return {
    mappings: composed,
  };
}

export function instantiate(value: Scheme): Type {
  const mapping = new Map<number, Type>();

  for (const quantified of value.quantified) {
    mapping.set(quantified, freshTypeVariable());
  }

  return applySubstitution(value.type, { mappings: mapping });
}

export function generalize(type: Type, environmentFreeVars: ReadonlySet<number>): Scheme {
  const typeFreeVars = freeTypeVariables(type);
  const quantified: number[] = [];

  for (const typeVar of typeFreeVars) {
    if (!environmentFreeVars.has(typeVar)) {
      quantified.push(typeVar);
    }
  }

  quantified.sort((a, b) => a - b);
  return scheme(quantified, type);
}

// Registry of type alias names for record types, populated by the checker.
// Maps sorted field names → alias name (e.g., "count,page" → "Model")
const _recordAliasNames = new Map<string, string>();

export function registerRecordAlias(fieldNames: string[], aliasName: string): void {
  _recordAliasNames.set(fieldNames.sort().join(","), aliasName);
}

export function formatType(type: Type): string {
  switch (type.kind) {
    case "TypeVariable":
      return type.name ?? `'t${type.id}`;

    case "TypeConstant":
      return type.name;

    case "TypeFunction": {
      const left = needsParensInFunctionLeft(type.from)
        ? `(${formatType(type.from)})`
        : formatType(type.from);
      return `${left} -> ${formatType(type.to)}`;
    }

    case "TypeApplication": {
      const ctor = formatType(type.constructor);
      const args = type.arguments.map((arg) => {
        return needsParensInTypeApplication(arg) ? `(${formatType(arg)})` : formatType(arg);
      }).join(" ");
      return `${ctor} ${args}`;
    }

    case "TypeTuple":
      return `(${type.items.map(formatType).join(", ")})`;

    case "TypeRecord": {
      // Check if this record matches a known type alias
      const fieldNames = Object.keys(type.fields).sort().join(",");
      const aliasName = _recordAliasNames.get(fieldNames);
      if (aliasName && !type.rest) return aliasName;

      const fieldStrs = Object.entries(type.fields).map(([k, v]) => `${k} : ${formatType(v)}`).join(", ");
      if (type.rest) return `{ ${fieldStrs}, ...${formatType(type.rest)} }`;
      return `{ ${fieldStrs} }`;
    }
  }
}

function needsParensInFunctionLeft(type: Type): boolean {
  return type.kind === "TypeFunction";
}

function needsParensInTypeApplication(type: Type): boolean {
  return type.kind === "TypeFunction";
}

/**
 * Format a type with normalized variable names (a, b, c, ...) instead of 't123.
 */
export function formatTypeNormalized(type: Type): string {
  const varIds = new Set<number>();
  collectTypeVarIds(type, varIds);

  if (varIds.size === 0) return formatType(type);

  const sorted = [...varIds].sort((a, b) => a - b);
  const nameMap = new Map<number, string>();
  let idx = 0;
  for (const id of sorted) {
    nameMap.set(id, varName(idx++));
  }

  return formatTypeWithNames(type, nameMap);
}

function varName(index: number): string {
  if (index < 26) return String.fromCharCode(97 + index); // a-z
  return `t${index - 25}`;
}

function collectTypeVarIds(type: Type, out: Set<number>): void {
  switch (type.kind) {
    case "TypeVariable":
      if (!type.name) out.add(type.id);
      return;
    case "TypeConstant":
      return;
    case "TypeFunction":
      collectTypeVarIds(type.from, out);
      collectTypeVarIds(type.to, out);
      return;
    case "TypeApplication":
      collectTypeVarIds(type.constructor, out);
      for (const arg of type.arguments) collectTypeVarIds(arg, out);
      return;
    case "TypeTuple":
      for (const item of type.items) collectTypeVarIds(item, out);
      return;
    case "TypeRecord":
      for (const value of Object.values(type.fields)) collectTypeVarIds(value, out);
      if (type.rest && !type.rest.name) out.add(type.rest.id);
      return;
  }
}

function formatTypeWithNames(type: Type, names: Map<number, string>): string {
  switch (type.kind) {
    case "TypeVariable":
      if (type.name) return type.name;
      return names.get(type.id) ?? `'t${type.id}`;
    case "TypeConstant":
      return type.name;
    case "TypeFunction": {
      const left = type.from.kind === "TypeFunction"
        ? `(${formatTypeWithNames(type.from, names)})`
        : formatTypeWithNames(type.from, names);
      return `${left} -> ${formatTypeWithNames(type.to, names)}`;
    }
    case "TypeApplication": {
      const ctor = formatTypeWithNames(type.constructor, names);
      const args = type.arguments.map(arg =>
        arg.kind === "TypeFunction" ? `(${formatTypeWithNames(arg, names)})` : formatTypeWithNames(arg, names)
      ).join(" ");
      return `${ctor} ${args}`;
    }
    case "TypeTuple":
      return `(${type.items.map(i => formatTypeWithNames(i, names)).join(", ")})`;
    case "TypeRecord": {
      const fieldStrs = Object.entries(type.fields).map(([k, v]) => `${k} : ${formatTypeWithNames(v, names)}`).join(", ");
      if (type.rest) {
        const restName = type.rest.name || names.get(type.rest.id) || `'t${type.rest.id}`;
        return `{ ${fieldStrs}, ...${restName} }`;
      }
      return `{ ${fieldStrs} }`;
    }
  }
}

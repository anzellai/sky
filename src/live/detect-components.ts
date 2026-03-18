// src/live/detect-components.ts
// Detects the component protocol pattern in a Sky.Live application
// using naming conventions between Model fields, Msg variants, and imports.

import * as AST from "../ast/ast.js";
import { Scheme } from "../types/types.js";

export interface ComponentBinding {
  fieldName: string;        // e.g., "myCounter"
  moduleName: string;       // e.g., "Counter"
  typeName: string;         // e.g., "Counter.Counter"
  msgWrapperName: string;   // e.g., "CounterMsg"
  msgWrapperTag: number;    // Tag index in the app's Msg type
  hasExplicitCase: boolean; // true if developer wrote the case themselves
}

/**
 * Detect component bindings by matching:
 * 1. Msg variant "FooMsg Foo.Msg" (wraps a module's Msg type)
 * 2. Model field initialized with Foo.init or Foo.initWith (in init function)
 * 3. Module Foo exports init, update, view, Msg
 */
export function detectComponents(
  moduleAst: AST.Module,
  moduleExports: Map<string, Map<string, Scheme>>
): ComponentBinding[] {
  const bindings: ComponentBinding[] = [];

  // Find Msg type declaration
  const msgDecl = moduleAst.declarations.find(
    d => d.kind === "TypeDeclaration" && d.name === "Msg"
  ) as any;
  if (!msgDecl?.variants) return bindings;

  // Find update function for explicit case detection
  const updateDecl = moduleAst.declarations.find(
    d => d.kind === "FunctionDeclaration" && d.name === "update"
  ) as any;
  const explicitCases = updateDecl ? extractCaseNames(updateDecl.body) : new Set<string>();

  // Build set of imported module names
  const importedModules = new Map<string, string>(); // short name → full name
  for (const imp of moduleAst.imports) {
    const parts = imp.moduleName;
    const shortName = imp.alias?.name || parts[parts.length - 1];
    importedModules.set(shortName, parts.join("."));
  }

  // Scan Msg variants for component wrapper pattern: FooMsg Foo.Msg
  for (let i = 0; i < msgDecl.variants.length; i++) {
    const variant = msgDecl.variants[i];
    const variantName = variant.name; // e.g., "CounterMsg"

    // Check if it ends with "Msg" and wraps a single Foo.Msg type
    if (!variantName.endsWith("Msg")) continue;
    if (!variant.fields || variant.fields.length !== 1) continue;

    // The wrapped type should be a qualified name: Foo.Msg
    const wrappedField = variant.fields[0];
    let wrappedModuleName: string | null = null;

    // Handle TypeReference with QualifiedIdentifier name
    if (wrappedField.name?.parts) {
      const parts = wrappedField.name.parts;
      if (parts.length === 2 && parts[1] === "Msg") {
        wrappedModuleName = parts[0];
      }
    }
    // Handle string-based name (e.g., "Counter.Msg")
    if (!wrappedModuleName && typeof wrappedField.name === "string" && wrappedField.name.includes(".")) {
      const parts = wrappedField.name.split(".");
      if (parts[parts.length - 1] === "Msg") {
        wrappedModuleName = parts[parts.length - 2];
      }
    }

    if (!wrappedModuleName) continue;

    // Verify the module is imported
    if (!importedModules.has(wrappedModuleName)) continue;

    // Convention: variant name = ModuleName + "Msg"
    const expectedVariantName = wrappedModuleName + "Msg";
    if (variantName !== expectedVariantName) continue;

    // Check if the module exports the component protocol
    const fullModuleName = importedModules.get(wrappedModuleName)!;
    if (moduleExports.has(fullModuleName)) {
      const exports = moduleExports.get(fullModuleName)!;
      // Must export at least update and init
      if (!exports.has("update") || !exports.has("init")) continue;
    }

    // Find the corresponding Model field
    // Convention: field name is camelCase of module name, or any field
    // initialized with Module.init / Module.initWith in the init function
    const fieldName = findComponentField(moduleAst, wrappedModuleName);
    if (!fieldName) continue;

    bindings.push({
      fieldName,
      moduleName: wrappedModuleName,
      typeName: `${wrappedModuleName}.${wrappedModuleName}`,
      msgWrapperName: variantName,
      msgWrapperTag: i,
      hasExplicitCase: explicitCases.has(variantName),
    });
  }

  return bindings;
}

/**
 * Find the Model field name that corresponds to a component module.
 * Looks in the init function for patterns like `Module.init` or `Module.initWith`.
 */
function findComponentField(moduleAst: AST.Module, moduleName: string): string | null {
  // Check Model record fields
  const modelDecl = moduleAst.declarations.find(
    d => d.kind === "TypeAliasDeclaration" && d.name === "Model"
  ) as any;
  if (!modelDecl?.aliasedType?.fields) return null;

  // Look in the init function for field assignments using Module.init
  const initDecl = moduleAst.declarations.find(
    d => d.kind === "FunctionDeclaration" && d.name === "init"
  ) as any;
  if (!initDecl) return null;

  // Search for `fieldName = Module.init` or `fieldName = Module.initWith ...` in init body
  const fieldInitMap = extractRecordFieldInits(initDecl.body);
  for (const [fieldName, initExpr] of fieldInitMap) {
    if (isModuleCall(initExpr, moduleName)) {
      return fieldName;
    }
  }

  // Fallback: try camelCase convention
  const camelName = moduleName.charAt(0).toLowerCase() + moduleName.slice(1);
  const hasField = modelDecl.aliasedType.fields.some((f: any) => f.name === camelName);
  if (hasField) return camelName;

  // Try "my" + ModuleName convention
  const myName = "my" + moduleName;
  const hasMyField = modelDecl.aliasedType.fields.some((f: any) => f.name === myName);
  if (hasMyField) return myName;

  return null;
}

/**
 * Extract field name → init expression mappings from record expressions in init body.
 */
function extractRecordFieldInits(expr: AST.Expression): Map<string, AST.Expression> {
  const map = new Map<string, AST.Expression>();
  collectRecordFields(expr, map);
  return map;
}

function collectRecordFields(expr: AST.Expression, map: Map<string, AST.Expression>): void {
  if (!expr) return;
  switch (expr.kind) {
    case "RecordExpression":
      for (const field of expr.fields) {
        map.set(field.name, field.value);
      }
      break;
    case "TupleExpression":
      for (const item of expr.items) {
        collectRecordFields(item, map);
      }
      break;
    case "LetExpression":
      for (const binding of expr.bindings) {
        collectRecordFields(binding.value, map);
      }
      collectRecordFields(expr.body, map);
      break;
    case "ParenthesizedExpression":
      collectRecordFields(expr.expression, map);
      break;
    case "CallExpression":
      collectRecordFields(expr.callee, map);
      for (const arg of expr.arguments) {
        collectRecordFields(arg, map);
      }
      break;
  }
}

/**
 * Check if an expression is a call to Module.something
 */
function isModuleCall(expr: AST.Expression, moduleName: string): boolean {
  if (expr.kind === "QualifiedIdentifierExpression") {
    return expr.name.parts[0] === moduleName;
  }
  if (expr.kind === "CallExpression") {
    return isModuleCall(expr.callee, moduleName);
  }
  return false;
}

/**
 * Extract case branch constructor names from an update function.
 */
function extractCaseNames(expr: AST.Expression): Set<string> {
  const names = new Set<string>();
  extractCaseNamesFromExpr(expr, names);
  return names;
}

function extractCaseNamesFromExpr(expr: AST.Expression, names: Set<string>): void {
  if (!expr) return;
  switch (expr.kind) {
    case "CaseExpression":
      for (const branch of expr.branches) {
        if (branch.pattern.kind === "ConstructorPattern") {
          const parts = branch.pattern.constructorName.parts;
          names.add(parts[parts.length - 1]);
        }
      }
      break;
    case "LetExpression":
      for (const binding of expr.bindings) {
        extractCaseNamesFromExpr(binding.value, names);
      }
      extractCaseNamesFromExpr(expr.body, names);
      break;
    case "LambdaExpression":
      extractCaseNamesFromExpr(expr.body, names);
      break;
    case "ParenthesizedExpression":
      extractCaseNamesFromExpr(expr.expression, names);
      break;
  }
}

// src/formatter/formatter.ts

import * as AST from "../ast.js";

export function formatModule(module: AST.Module): string {
  const lines: string[] = [];

  lines.push(formatModuleHeader(module));
  lines.push("");

  if (module.imports.length > 0) {
    lines.push("");
  }

  if (module.imports.length > 0) {

    for (const imp of module.imports) {
      lines.push(`import ${imp.moduleName.join(".")}`);
    }

    lines.push("");

  }

  for (const decl of module.declarations) {
    switch (decl.kind) {
      case "FunctionDeclaration":
        lines.push(formatFunction(decl));
        lines.push("");
        break;

      default:
        break;
    }
  }

  return lines.join("\n").trimEnd() + "\n";
}

function formatModuleHeader(module: AST.Module): string {
  const moduleName = module.name.join(".");

  if (!module.exposing) {
    return `module ${moduleName}`;
  }

  if (module.exposing.open) {
    return `module ${moduleName} exposing (.. )`.replace(".. ", "..");
  }

  const items = module.exposing.items.map(formatExposedItem).join(", ");
  return `module ${moduleName} exposing (${items})`;
}

function formatExposedItem(item: AST.ExposedItem): string {
  if (item.kind === "value") {
    return item.name;
  }

  return item.exposeConstructors
    ? `${item.name}(..)`
    : item.name;
}

function formatFunction(fn: AST.FunctionDeclaration): string {
  const params = fn.parameters.map((p) => formatPattern(p.pattern)).join(" ");
  const header = `${fn.name}${params ? " " + params : ""} =`;
  const body = formatExpression(fn.body, 1);

  return `${header}\n${body}`;
}

function formatExpression(expr: AST.Expression, indent: number): string {
  const pad = "    ".repeat(indent);

  switch (expr.kind) {
    case "IdentifierExpression":
      return pad + expr.name;

    case "QualifiedIdentifierExpression":
      return pad + expr.name.parts.join(".");

    case "IntegerLiteralExpression":
      return pad + expr.raw;

    case "FloatLiteralExpression":
      return pad + expr.raw;

    case "StringLiteralExpression":
      return pad + JSON.stringify(expr.value);

    case "BooleanLiteralExpression":
      return pad + (expr.value ? "true" : "false");

    case "CallExpression": {
      const callee = formatExpression(expr.callee, 0).trim();
      const args = expr.arguments.map((arg) => formatExpression(arg, 0).trim()).join(" ");
      return pad + `${callee} ${args}`;
    }

    case "BinaryExpression": {
      const left = formatExpression(expr.left, 0).trim();
      const right = formatExpression(expr.right, 0).trim();
      return pad + `${left} ${expr.operator} ${right}`;
    }

    case "ParenthesizedExpression":
      return pad + `(${formatExpression(expr.expression, 0).trim()})`;

    default:
      throw new Error(`Formatter missing case ${(expr as { kind?: string }).kind}`);
  }
}

function formatPattern(pattern: AST.Pattern): string {
  switch (pattern.kind) {
    case "VariablePattern":
      return pattern.name;

    case "WildcardPattern":
      return "_";

    default:
      return "_";
  }
}

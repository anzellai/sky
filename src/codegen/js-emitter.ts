// src/codegen/js-emitter.ts
// Sky → JavaScript emitter with Elm-style currying + pipeline operators

import path from "path";
import * as AST from "../ast.js";

export interface EmitOptions {
  readonly moduleName: string;
}

export interface EmitResult {
  readonly code: string;
}

export function emitModule(module: AST.Module, options: EmitOptions): EmitResult {
  const lines: string[] = [];

  lines.push(`// Generated from Sky module: ${options.moduleName}`);

  const currentParts = module.name;

  for (const imp of module.imports) {
    const alias = imp.moduleName.join("_");
    const importPath = computeRelativeImport(currentParts, imp.moduleName);
    lines.push(`import * as ${alias} from "${importPath}"`);
  }

  const foreignImports = module.declarations.filter(
    (decl): decl is AST.ForeignImportDeclaration => decl.kind === "ForeignImportDeclaration",
  );

  for (const decl of foreignImports) {
    lines.push(`import { ${decl.name} } from ${JSON.stringify(decl.sourceModule)}`);
  }

  if (module.imports.length > 0 || foreignImports.length > 0) {
    lines.push("");
  }

  for (const decl of module.declarations) {
    switch (decl.kind) {
      case "FunctionDeclaration":
        lines.push(emitFunction(decl));
        lines.push("");
        break;

      case "TypeDeclaration":
      case "TypeAliasDeclaration":
      case "ForeignImportDeclaration":
        break;
    }
  }

  const hasMain = module.declarations.some(
    (d) => d.kind === "FunctionDeclaration" && d.name === "main",
  );

  if (hasMain) {
    lines.push("");
    lines.push("// Auto-run main when executed directly");
    lines.push(
      `if (import.meta.url === \`file://\${process.argv[1]}\`) {
  if (typeof main === "function") {
    const result = main();
    if (result !== undefined) {
      console.log(result);
    }
  }
}`,
    );
  }

  return {
    code: lines.join("\n"),
  };
}

function emitFunction(fn: AST.FunctionDeclaration): string {
  const params: string[] = [];
  const bindingsPerParam: string[][] = [];

  fn.parameters.forEach((param, index) => {
    const pattern = param.pattern;

    if (pattern.kind === "VariablePattern") {
      params.push(pattern.name);
      bindingsPerParam.push([]);
    } else {
      const argName = `__arg${index}`;
      params.push(argName);
      bindingsPerParam.push(
        emitPatternBindingStatements(pattern, argName),
      );
    }
  });

  const body = emitExpression(fn.body);
  const lines: string[] = [];

  if (params.length === 0) {
    lines.push(`export function ${fn.name}() {`);
    lines.push(`  return ${body};`);
    lines.push(`}`);
    return lines.join("\n");
  }

  lines.push(`export function ${fn.name}(${params[0]}) {`);

  for (const binding of bindingsPerParam[0]) {
    lines.push(`  ${binding}`);
  }

  for (let i = 1; i < params.length; i += 1) {
    lines.push(`  return function (${params[i]}) {`);
    for (const binding of bindingsPerParam[i]) {
      lines.push(`    ${binding}`);
    }
  }

  lines.push(`    return ${body};`);

  for (let i = 1; i < params.length; i += 1) {
    lines.push(`  };`);
  }

  lines.push(`}`);

  return lines.join("\n");
}

function emitExpression(expr: AST.Expression): string {
  switch (expr.kind) {
    case "IdentifierExpression":
      return expr.name;

    case "QualifiedIdentifierExpression":
      return expr.name.parts.join("_");

    case "IntegerLiteralExpression":
      return expr.raw;

    case "FloatLiteralExpression":
      return expr.raw;

    case "StringLiteralExpression":
      return JSON.stringify(expr.value);

    case "BooleanLiteralExpression":
      return expr.value ? "true" : "false";

    case "BinaryExpression":
      return emitBinaryExpression(expr);

    case "CallExpression": {
      const callee = emitExpression(expr.callee);

      // Since Sky functions are emitted as curried JS functions,
      // emit nested calls: f(a)(b)(c)
      return expr.arguments.reduce(
        (acc, arg) => `${acc}(${emitExpression(arg)})`,
        callee,
      );
    }

    case "ParenthesizedExpression":
      return `(${emitExpression(expr.expression)})`;

    case "LambdaExpression": {
      const params = expr.parameters.map((param, index) => {
        const pattern = param.pattern;
        return pattern.kind === "VariablePattern"
          ? pattern.name
          : `__lambdaArg${index}`;
      });

      const bindings: string[] = [];
      expr.parameters.forEach((param, index) => {
        const pattern = param.pattern;
        const paramName = params[index];
        if (pattern.kind !== "VariablePattern") {
          bindings.push(...emitPatternBindingStatements(pattern, paramName));
        }
      });

      let result = emitExpression(expr.body);

      for (let i = params.length - 1; i >= 0; i -= 1) {
        const innerBindings =
          i === params.length - 1 ? bindings.map((b) => `  ${b}\n`).join("") : "";
        if (innerBindings.length > 0) {
          result = `(${params[i]} => {\n${innerBindings}  return ${result};\n})`;
        } else {
          result = `(${params[i]} => ${result})`;
        }
      }

      return result;
    }

    default:
      throw new Error(`Unsupported expression kind ${(expr as { kind?: string }).kind}`);
  }
}

function emitBinaryExpression(expr: AST.BinaryExpression): string {
  const left = emitExpression(expr.left);
  const right = emitExpression(expr.right);

  switch (expr.operator) {
    case "|>":
      return `${right}(${left})`;

    case "<|":
      return `${left}(${right})`;

    case ">>":
      return `(x => ${right}(${left}(x)))`;

    case "<<":
      return `(x => ${left}(${right}(x)))`;

    default:
      return `(${left} ${expr.operator} ${right})`;
  }
}

function emitPatternBindingStatements(pattern: AST.Pattern, valueRef: string): string[] {
  switch (pattern.kind) {
    case "VariablePattern":
      return [`const ${pattern.name} = ${valueRef};`];

    case "WildcardPattern":
      return [];

    default:
      return [];
  }
}

function computeRelativeImport(from: readonly string[], to: readonly string[]): string {
  const fromPath = path.join(...from) + ".js";
  const toPath = path.join(...to) + ".js";

  let rel = path.relative(path.dirname(fromPath), toPath);

  if (!rel.startsWith(".")) {
    rel = "./" + rel;
  }

  return rel.replace(/\\/g, "/");
}

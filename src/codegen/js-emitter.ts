// src/codegen/js-emitter.ts
// Sky → JavaScript emitter with:
// - curried function emission
// - Elm-style pipeline operators
// - foreign import emission

import path from "path";
import * as AST from "../ast.js";

export interface EmitOptions {
  readonly moduleName: string;
  readonly importPaths?: ReadonlyMap<string, string>;
  readonly importExposes?: ReadonlyMap<string, readonly string[]>;
  readonly target?: "web" | "node" | "native";
  readonly activeImports?: ReadonlySet<string>;
}

export interface EmitResult {
  readonly code: string;
}

export function emitModule(module: AST.Module, options: EmitOptions): EmitResult {
  const lines: string[] = [];

  lines.push(`// Generated from Sky module: ${options.moduleName}`);

  const currentParts = module.name;

  const activeImports = new Set<string>();
  for (const imp of module.imports) {
    activeImports.add(imp.moduleName.join("."));
  }

  const enrichedOptions: EmitOptions = {
    ...options,
    activeImports
  };

  // Sky module imports
  for (const imp of module.imports) {
    const alias = imp.moduleName.join("_");
    const moduleNameStr = imp.moduleName.join(".");
    const importPath = options.importPaths?.get(moduleNameStr) ?? computeRelativeImport(currentParts, imp.moduleName);
    lines.push(`import * as ${alias} from "${importPath}";`);

    if (imp.exposing && !imp.exposing.open && imp.exposing.items.length > 0) {
      const names = imp.exposing.items
        .filter((item) => item.kind === "value")
        .map((item) => item.name);
      
      if (names.length > 0) {
        lines.push(`const { ${names.join(", ")} } = ${alias};`);
      }
    } else if (imp.exposing && imp.exposing.open) {
      const exposed = options.importExposes?.get(moduleNameStr);
      if (exposed && exposed.length > 0) {
        lines.push(`const { ${exposed.join(", ")} } = ${alias};`);
      } else {
        lines.push(`// open import ${alias} exposing (..) not fully supported by simple js-emitter yet`);
      }
    }
  }

  // Foreign imports
  for (const decl of module.declarations) {
    if (decl.kind !== "ForeignImportDeclaration") {
      continue;
    }

    const names = getForeignImportNames(decl);

    if (names.length === 0) {
      continue;
    }

    const exposedNames = names.filter(name => 
      !module.exposing || 
      module.exposing.open || 
      module.exposing.items.some(i => i.kind === "value" && i.name === name)
    );

    if (decl.sourceModule === "JSON" || decl.sourceModule === "global") {
      for (const name of names) {
        const isExposed = exposedNames.includes(name);
        lines.push(`${isExposed ? "export " : ""}const ${name} = ${decl.sourceModule}.${name};`);
      }
    } else {
      let source = decl.sourceModule;

      // TARGET-AWARE MAPPING
      if (source === "@sky/runtime/program") {
        if (options.target === "node") {
          source = "@sky/runtime/program-node.js";
        } else {
          source = "@sky/runtime/program-react.js";
        }
      } else if (source === "@sky/runtime/interop") {
        source = "@sky/runtime/interop.js";
      } else if (source.startsWith("@sky/runtime/") && !source.endsWith(".js")) {
        source += ".js";
      }

      // Convert @sky/runtime to relative path
      if (source.startsWith("@sky/runtime/")) {
        const runtimeModule = source.replace("@sky/runtime/", "");
        const relPath = computeRelativeImport(currentParts, ["runtime", runtimeModule.replace(".js", "")]);
        source = relPath;
      }

      lines.push(
        `import { ${names.join(", ")} } from ${JSON.stringify(source)};`,
      );
      if (exposedNames.length > 0) {
        lines.push(`export { ${exposedNames.join(", ")} };`);
      }
    }
  }

  if (
    module.imports.length > 0 ||
    module.declarations.some((d) => d.kind === "ForeignImportDeclaration")
  ) {
    lines.push("");
  }

  // Top-level declarations
  for (const decl of module.declarations) {
    switch (decl.kind) {
      case "FunctionDeclaration":
        lines.push(emitFunction(decl, enrichedOptions));
        lines.push("");
        break;

      case "TypeDeclaration":
      case "TypeAliasDeclaration":
      case "ForeignImportDeclaration":
      case "TypeAnnotation":
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
      `
const isMain = typeof require !== 'undefined' 
  ? require.main === module 
  : (typeof import.meta !== 'undefined' && import.meta.url === \`file://\${process.argv[1]}\`);

if (isMain) {
  if (typeof main === "function") {
    const result = main();
    if (result instanceof Promise) {
      result.then(res => { if (res !== undefined) console.log(res); });
    } else if (result !== undefined) {
      if (typeof result === "function" && typeof window !== "undefined") {
         console.log("Sky UI component returned from main. Mount it in your React root.");
      } else if (result && typeof result.dispatch === "function") {
         // It's a Node program, it keeps itself alive via process.stdin.resume()
      } else {
         console.log(result);
      }
    }
  }
}`,
    );
  }

  return {
    code: lines.join("\n"),
  };
}

function emitFunction(fn: AST.FunctionDeclaration, options: EmitOptions): string {
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
      bindingsPerParam.push(emitPatternBindingStatements(pattern, argName));
    }
  });

  const body = emitExpression(fn.body, options);
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

function emitExpression(expr: AST.Expression, options: EmitOptions): string {
  switch (expr.kind) {
    case "IdentifierExpression":
      return expr.name;

    case "QualifiedIdentifierExpression":
      return emitQualifiedName(expr.name.parts, options);

    case "IntegerLiteralExpression":
      return expr.raw;

    case "FloatLiteralExpression":
      return expr.raw;

    case "StringLiteralExpression":
      return JSON.stringify(expr.value);

    case "CharLiteralExpression":
      return JSON.stringify(expr.value);

    case "BooleanLiteralExpression":
      return expr.value ? "true" : "false";

    case "UnitExpression":
      return "undefined";

    case "ParenthesizedExpression":
      return `(${emitExpression(expr.expression, options)})`;

    case "TupleExpression":
      return `[${expr.items.map(i => emitExpression(i, options)).join(", ")}]`;

    case "ListExpression":
      return `[${expr.items.map(i => emitExpression(i, options)).join(", ")}]`;

    case "RecordExpression":
      return `{ ${expr.fields.map((f) => `${f.name}: ${emitExpression(f.value, options)}`).join(", ")} }`;

    case "FieldAccessExpression":
      return `${emitExpression(expr.target, options)}.${expr.fieldName}`;

    case "BinaryExpression":
      return emitBinaryExpression(expr, options);

    case "CallExpression":
      return expr.arguments.reduce(
        (acc, arg) => `${acc}(${emitExpression(arg, options)})`,
        emitExpression(expr.callee, options),
      );

    case "LambdaExpression": {
      const params = expr.parameters.map((param, index) => {
        const pattern = param.pattern;
        return pattern.kind === "VariablePattern"
          ? pattern.name
          : `__lambdaArg${index}`;
      });

      const body = emitExpression(expr.body, options);

      let result = body;

      for (let i = params.length - 1; i >= 0; i -= 1) {
        const pattern = expr.parameters[i].pattern;
        const bindings =
          pattern.kind === "VariablePattern"
            ? []
            : emitPatternBindingStatements(pattern, params[i]);

        if (bindings.length > 0) {
          result = `(${params[i]} => { ${bindings.join(" ")} return ${result}; })`;
        } else {
          result = `(${params[i]} => ${result})`;
        }
      }

      return result;
    }

    case "IfExpression":
      return `(${emitExpression(expr.condition, options)} ? ${emitExpression(expr.thenBranch, options)} : ${emitExpression(expr.elseBranch, options)})`;

    case "LetExpression": {
      const bindingLines = expr.bindings.flatMap((binding, index) => {
        const temp = `__let${index}`;
        return [
          `const ${temp} = ${emitExpression(binding.value, options)};`,
          ...emitPatternBindingStatements(binding.pattern, temp),
        ];
      });

      return `(() => { ${bindingLines.join(" ")} return ${emitExpression(expr.body, options)}; })()`;
    }

    case "CaseExpression": {
      const subject = emitExpression(expr.subject, options);
      const temp = `__case${Math.floor(Math.random() * 1000)}`;
      
      const branches = expr.branches.map((branch) => {
        const condition = emitPatternCondition(branch.pattern, temp);
        const bindings = emitPatternBindingStatements(branch.pattern, temp);
        return `if (${condition}) { ${bindings.join(" ")} return ${emitExpression(branch.body, options)}; }`;
      });

      return `(( ${temp} ) => { ${branches.join(" else ")} throw new Error("Pattern match failed"); })(${subject})`;
    }

    default:
      throw new Error(`Unsupported expression kind ${(expr as { kind?: string }).kind}`);
  }
}

function emitQualifiedName(parts: readonly string[], options: EmitOptions): string {
  if (!options.activeImports) {
    return parts.join("_");
  }

  // Find the longest prefix that matches an imported module
  for (let i = parts.length - 1; i >= 1; i--) {
    const prefix = parts.slice(0, i).join(".");
    if (options.activeImports.has(prefix)) {
      const alias = parts.slice(0, i).join("_");
      const member = parts.slice(i).join(".");
      return `${alias}.${member}`;
    }
  }

  // Fallback to underscore join if no import matches (e.g. local qualified name or ADT constructor)
  return parts.join("_");
}

function emitBinaryExpression(expr: AST.BinaryExpression, options: EmitOptions): string {
  const left = emitExpression(expr.left, options);
  const right = emitExpression(expr.right, options);

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

function emitPatternCondition(pattern: AST.Pattern, valueRef: string): string {
  switch (pattern.kind) {
    case "WildcardPattern":
      return "true";

    case "VariablePattern":
      return pattern.name === "_" ? "true" : "true";

    case "LiteralPattern":
      return `${valueRef} === ${JSON.stringify(pattern.value)}`;

    case "ConstructorPattern": {
      const tag = pattern.constructorName.parts[pattern.constructorName.parts.length - 1];
      const check = `${valueRef} && ${valueRef}.$ === ${JSON.stringify(tag)}`;
      const argsCheck = pattern.arguments
        .map((arg, index) => emitPatternCondition(arg, `${valueRef}.values[${index}]`))
        .filter((c) => c !== "true")
        .join(" && ");
      return argsCheck ? `(${check} && ${argsCheck})` : check;
    }

    case "TuplePattern": {
      const check = `Array.isArray(${valueRef}) && ${valueRef}.length === ${pattern.items.length}`;
      const argsCheck = pattern.items
        .map((arg, index) => emitPatternCondition(arg, `${valueRef}[${index}]`))
        .filter((c) => c !== "true")
        .join(" && ");
      return argsCheck ? `(${check} && ${argsCheck})` : check;
    }

    case "ListPattern": {
      const check = `Array.isArray(${valueRef}) && ${valueRef}.length === ${pattern.items.length}`;
      const argsCheck = pattern.items
        .map((arg, index) => emitPatternCondition(arg, `${valueRef}[${index}]`))
        .filter((c) => c !== "true")
        .join(" && ");
      return argsCheck ? `(${check} && ${argsCheck})` : check;
    }

    default:
      return "true";
  }
}

function emitPatternBindingStatements(pattern: AST.Pattern, valueRef: string): string[] {
  switch (pattern.kind) {
    case "VariablePattern":
      return pattern.name === "_" ? [] : [`const ${pattern.name} = ${valueRef};`];

    case "WildcardPattern":
      return [];

    case "TuplePattern": {
      const lines: string[] = [];
      pattern.items.forEach((item, index) => {
        lines.push(...emitPatternBindingStatements(item, `${valueRef}[${index}]`));
      });
      return lines;
    }

    case "ListPattern": {
      const lines: string[] = [];
      pattern.items.forEach((item, index) => {
        lines.push(...emitPatternBindingStatements(item, `${valueRef}[${index}]`));
      });
      return lines;
    }

    case "ConstructorPattern": {
      const lines: string[] = [];
      pattern.arguments.forEach((arg, index) => {
        lines.push(...emitPatternBindingStatements(arg, `${valueRef}.values[${index}]`));
      });
      return lines;
    }

    case "LiteralPattern":
      return [];

    default:
      return [];
  }
}

function getForeignImportNames(decl: AST.ForeignImportDeclaration): string[] {
  // Supports either:
  // - decl.exposing = string[]
  // - decl.name for single-import AST shapes
  // - decl.importName/name legacy shapes
  const maybeExposing = (decl as unknown as { exposing?: readonly string[] }).exposing;
  if (Array.isArray(maybeExposing)) {
    return [...maybeExposing];
  }

  const maybeName = (decl as unknown as { name?: string }).name;
  if (typeof maybeName === "string" && maybeName.length > 0) {
    return [maybeName];
  }

  const maybeImportName = (decl as unknown as { importName?: string }).importName;
  if (typeof maybeImportName === "string" && maybeImportName.length > 0) {
    return [maybeImportName];
  }

  return [];
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

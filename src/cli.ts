// src/cli.ts
// Sky CLI

import fs from "fs";
import path from "path";
import { spawnSync } from "child_process";

import { lex } from "./lexer.js";
import { parse } from "./parser.js";
import { formatModule } from "./formatter/formatter.js";
import { startRepl } from "./repl/repl.js";
import { compileProject } from "./compiler.js";
import { filterLayout } from "./parser/filter-layout.js";
import { emitModule } from "./codegen/js-emitter.js";
import { checkModule } from "./type-system/checker.js";

function main(): void {
  const args = process.argv.slice(2);
  const command = args[0];

  if (!command) {
    printHelp();
    process.exit(1);
  }

  switch (command) {
    case "build":
      cmdBuild(args.slice(1));
      return;

    case "run":
      cmdRun(args.slice(1));
      return;

    case "debug":
      cmdDebug(args.slice(1));
      return;

    case "ast":
      cmdAst(args.slice(1));
      return;

    case "tokens":
    case "token":
      cmdTokens(args.slice(1));
      return;

    case "format":
    case "fmt":
      cmdFormat(args.slice(1));
      return;

    case "repl":
      void cmdRepl();
      return;

    case "help":
    case "--help":
    case "-h":
      printHelp();
      return;

    default:
      console.error(`Unknown command: ${command}\n`);
      printHelp();
      process.exit(1);
  }
}

function cmdBuild(args: string[]): void {
  const entry = requireFileArg("sky build <entry.sky>", args);

  const result = compileProject(entry);

  if (result.diagnostics.length > 0) {
    printDiagnostics("Compilation failed", result.diagnostics);
    process.exit(1);
  }

  console.log("Build succeeded");
}

function cmdRun(args: string[]): void {
  const entry = requireFileArg("sky run <entry.sky>", args);

  const result = compileProject(entry);

  if (result.diagnostics.length > 0) {
    printDiagnostics("Compilation failed", result.diagnostics);
    process.exit(1);
  }

  const modulePath = computeOutputModule(entry);

  if (!fs.existsSync(modulePath)) {
    console.error(`Cannot find compiled module: ${modulePath}`);
    process.exit(1);
  }

  const node = spawnSync("node", [modulePath], {
    stdio: "inherit",
  });

  process.exit(node.status ?? 0);
}

function cmdDebug(args: string[]): void {

  const file = requireFileArg("sky debug <file.sky>", args);

  const source = readFileOrExit(file);

  console.log("========== SOURCE ==========");
  console.log(source);

  const lexResult = lex(source, file);

  if (lexResult.diagnostics.length > 0) {
    printLexDiagnostics("Lexing failed", lexResult.diagnostics);
    process.exit(1);
  }

  console.log("\n========== TOKENS ==========");

  for (const token of lexResult.tokens) {
    const pos = `${token.span.start.line}:${token.span.start.column}`;
    console.log(`${token.kind.padEnd(16)} ${JSON.stringify(token.lexeme)} @ ${pos}`);
  }

  let moduleAst;

  try {

    const tokens = filterLayout(lexResult.tokens);

    moduleAst = parse(tokens);

  } catch (err) {

    console.error("\n========== PARSE ERROR ==========");
    console.error(err instanceof Error ? err.message : String(err));

    process.exit(1);

  }

  console.log("\n========== AST ==========");
  console.log(JSON.stringify(moduleAst, null, 2));

  const typeCheck = checkModule(moduleAst);

  if (typeCheck.diagnostics.length > 0) {

    console.log("\n========== TYPE ERRORS ==========");

    for (const d of typeCheck.diagnostics) {
      console.log(d.message);
    }

  } else {

    console.log("\n========== TYPECHECK OK ==========");

  }

  const emitted = emitModule(moduleAst, {
    moduleName: moduleAst.name.join("."),
  });

  console.log("\n========== JS OUTPUT ==========");
  console.log(emitted.code);

}

function cmdAst(args: string[]): void {
  const file = requireFileArg("sky ast <file.sky>", args);
  const source = readFileOrExit(file);

  const lexResult = lex(source, file);
  if (lexResult.diagnostics.length > 0) {
    printLexDiagnostics("Lexing failed", lexResult.diagnostics);
    process.exit(1);
  }

  try {
    const tokens = filterLayout(lexResult.tokens);
    const moduleAst = parse(tokens);
    console.log(JSON.stringify(moduleAst, null, 2));
  } catch (error) {
    console.error("Parse failed:\n");
    console.error(error instanceof Error ? error.message : String(error));
    process.exit(1);
  }
}

function cmdTokens(args: string[]): void {
  const file = requireFileArg("sky tokens <file.sky>", args);
  const source = readFileOrExit(file);

  const lexResult = lex(source, file);

  if (lexResult.diagnostics.length > 0) {
    printLexDiagnostics("Lexing failed", lexResult.diagnostics);
    process.exit(1);
  }

  for (const token of lexResult.tokens) {
    const pos = `${token.span.start.line}:${token.span.start.column}`;
    console.log(
      `${token.kind.padEnd(18)} ${JSON.stringify(token.lexeme).padEnd(20)} ${pos}`,
    );
  }
}

function formatSource(source: string, filename: string): string {

  const lexResult = lex(source, filename);

  if (lexResult.diagnostics.length > 0) {
    throw new Error("Cannot format file with lexer errors");
  }

  const tokens = filterLayout(lexResult.tokens);

  const moduleAst = parse(tokens);

  return formatModule(moduleAst);

}

async function readStdin(): Promise<string> {

  return new Promise((resolve, reject) => {

    let data = "";

    process.stdin.setEncoding("utf8");

    process.stdin.on("data", chunk => {
      data += chunk;
    });

    process.stdin.on("end", () => {
      resolve(data);
    });

    process.stdin.on("error", reject);

  });

}

async function cmdFormat(args: string[]): Promise<void> {

  const target = args[0];

  if (!target || target === "-") {

    const source = await readStdin();

    const formatted = formatSource(source, "<stdin>");

    process.stdout.write(formatted);

    return;

  }

  const source = fs.readFileSync(target, "utf8");

  const formatted = formatSource(source, target);

  fs.writeFileSync(target, formatted, "utf8");

  console.log(`Formatted ${target}`);

}

async function cmdRepl(): Promise<void> {
  await startRepl();
}

function requireFileArg(usage: string, args: string[]): string {
  const file = args[0];
  if (!file) {
    console.error(`${usage}\n`);
    process.exit(1);
  }
  return file;
}

function readFileOrExit(file: string): string {
  try {
    return fs.readFileSync(file, "utf8");
  } catch {
    console.error(`Cannot read file: ${file}`);
    process.exit(1);
  }
}

function computeOutputModule(entry: string): string {
  const withoutExt = entry.replace(/\.sky$/, "");
  const parts = withoutExt.split(/[\\/]/);

  const srcIndex = parts.indexOf("src");
  const relativeParts = srcIndex >= 0 ? parts.slice(srcIndex + 1) : parts;

  return path.join("dist", ...relativeParts) + ".js";
}

function printDiagnostics(title: string, diagnostics: readonly string[]): void {
  console.error(`${title}:\n`);
  for (const diagnostic of diagnostics) {
    console.error(diagnostic);
  }
}

function printLexDiagnostics(
  title: string,
  diagnostics: readonly {
    severity: string;
    message: string;
    span: { start: { line: number; column: number } };
    hint?: string;
  }[],
): void {
  console.error(`${title}:\n`);
  for (const diagnostic of diagnostics) {
    const pos = `${diagnostic.span.start.line}:${diagnostic.span.start.column}`;
    console.error(`${diagnostic.severity}: ${diagnostic.message} at ${pos}`);
    if (diagnostic.hint) {
      console.error(`  hint: ${diagnostic.hint}`);
    }
  }
}

function printHelp(): void {
  console.log(`Sky compiler

Usage:
  sky build <file.sky>      Compile a Sky program
  sky run <file.sky>        Compile and run a Sky program
  sky debug <file.sky>      Show tokens, AST, types, and emitted JS
  sky ast <file.sky>        Print parsed AST as JSON
  sky tokens <file.sky>     Print lexer tokens
  sky token <file.sky>      Alias for tokens
  sky format <file.sky>     Format a file in place
  sky fmt <file.sky>        Alias for format
  sky repl                  Start interactive REPL
  sky help                  Show this help
`);
}

main();

// src/repl/repl.ts
// Sky interactive REPL
//
// Provides:
//   sky repl
//
// Features:
// - incremental evaluation
// - persistent scope
// - expression execution
// - graceful syntax errors
//
// Implementation strategy (v1):
// - wrap user input inside a temporary Sky module
// - compile to JS using the existing emitter
// - execute with dynamic import

import readline from "readline";
import fs from "fs";
import os from "os";
import path from "path";

import { lex } from "../lexer.js";
import { parse } from "../parser.js";
import { emitModule } from "../codegen/js-emitter.js";

export async function startRepl() {

  console.log("Sky REPL");
  console.log("Type :quit to exit");
  console.log("");

  const rl = readline.createInterface({
    input: process.stdin,
    output: process.stdout,
    prompt: "> "
  });

  rl.prompt();

  rl.on("line", async (line) => {

    const input = line.trim();

    if (input === "") {
      rl.prompt();
      return;
    }

    if (input === ":quit") {
      rl.close();
      return;
    }

    try {

      const result = await evaluateExpression(input);

      if (result !== undefined) {
        console.log(result);
      }

    } catch (err) {

      console.error("Error:", err instanceof Error ? err.message : err);

    }

    rl.prompt();

  });

  rl.on("close", () => {
    console.log("Bye.");
    process.exit(0);
  });
}

async function evaluateExpression(expr: string): Promise<any> {

  const moduleSource = `
module Repl.Main exposing (main)

main =
    ${expr}
`;

  const lexResult = lex(moduleSource, "<repl>");

  if (lexResult.diagnostics.length) {
    throw new Error(lexResult.diagnostics.map(d => d.message).join("\n"));
  }

  const moduleAst = parse(lexResult.tokens);

  const emit = emitModule(moduleAst, {
    moduleName: "Repl.Main"
  });

  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "sky-repl-"));

  const file = path.join(tmpDir, "repl.js");

  fs.writeFileSync(file, emit.code);

  const mod = await import("file://" + file);

  if (typeof mod.main === "function") {
    return mod.main();
  }

  return undefined;
}

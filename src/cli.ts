// src/cli.ts
// Sky CLI with build/run/ast/format/repl/deps

import fs from "fs"
import path from "path"
import process from "process"

import { compileProject } from "./compiler.js"
import { buildModuleGraph } from "./module-graph.js"

import { lex } from "./lexer.js"
import { parse } from "./parser.js"
import { filterLayout } from "./parser/filter-layout.js"

import { formatModule } from "./formatter/formatter.js"

import { loadProject } from "./project/load-project.js"
import { resolveEntryFile } from "./project/resolve-entry.js"

import { initProject } from "./project/init-project.js"
import { addPackage } from "./project/add-package.js"

async function main() {

  const args = process.argv.slice(2)

  const command = args[0]

  switch (command) {
    case "init":
      initProject()
      return

    case "add":
      addPackage(args[1])
      return

    case "build":
      await cmdBuild(args[1])
      return

    case "run":
      await cmdRun(args[1])
      return

    case "ast":
      await cmdAst(args[1])
      return

    case "fmt":
    case "format":
      await cmdFormat(args[1])
      return

    case "deps":
      await cmdDeps(args[1])
      return

    case "repl":
      await cmdRepl()
      return

    default:
      printHelp()
      process.exit(1)

  }

}

/* ------------------------------------------------ */

async function cmdBuild(file?: string) {

  const start = performance.now()

  const project = loadProject()

  const entry =
    file ||
    resolveEntryFile(
      project.sourceDir,
      project.entryModule
    )

  const graph = await buildModuleGraph(entry)

  if (graph.diagnostics.length > 0) {

    console.error("Dependency errors:\n")

    for (const d of graph.diagnostics) {
      console.error(d)
    }

    process.exit(1)

  }

  const result =
    await compileProject(
      entry,
      project.outputDir
    )

  if (result.diagnostics.length > 0) {

    console.error("Compilation failed:\n")

    for (const d of result.diagnostics) {
      console.error(d)
    }

    process.exit(1)

  }

  const end = performance.now()

  console.log(
    `Built ${graph.modules.length} module(s) in ${(end - start).toFixed(0)} ms`
  )

}

/* ------------------------------------------------ */

async function cmdRun(file: string) {

  const project = loadProject()

  const entry =
    file ||
    resolveEntryFile(
      project.sourceDir,
      project.entryModule
    )

  const result =
    await compileProject(
      entry,
      project.outputDir
    )

  if (result.diagnostics.length > 0) {

    console.error("Compilation failed:\n")

    for (const d of result.diagnostics) {
      console.error(d)
    }

    process.exit(1)

  }

  const modulePath = computeOutputPath(file)

  const mod = await import(path.resolve(modulePath))

  if (typeof mod.main === "function") {

    const value = mod.main()

    if (value !== undefined) {
      console.log(value)
    }

  }

}

/* ------------------------------------------------ */

async function cmdDeps(file: string) {

  if (!file) {
    console.error("Missing input file")
    process.exit(1)
  }

  const graph = await buildModuleGraph(file)

  if (graph.diagnostics.length > 0) {

    console.error("Dependency errors:\n")

    for (const d of graph.diagnostics) {
      console.error(d)
    }

    process.exit(1)

  }

  console.log("Module dependency order:\n")

  for (const m of graph.modules) {

    const name =
      m.moduleAst.name.join(".")

    console.log(name)

  }

}

/* ------------------------------------------------ */

async function cmdAst(file: string) {

  const source = fs.readFileSync(file, "utf8")

  const lexResult = lex(source, file)

  const tokens = filterLayout(lexResult.tokens)

  const ast = parse(tokens)

  console.log(JSON.stringify(ast, null, 2))

}

/* ------------------------------------------------ */

async function cmdFormat(file: string) {

  const source = fs.readFileSync(file, "utf8")

  const lexResult = lex(source, file)

  const tokens = filterLayout(lexResult.tokens)

  const module = parse(tokens)

  const formatted = formatModule(module)

  fs.writeFileSync(file, formatted)

  console.log("Formatted", file)

}

/* ------------------------------------------------ */

async function cmdRepl() {

  console.log("Sky REPL (minimal)")
  console.log("Type :quit to exit\n")

  process.stdin.setEncoding("utf8")

  process.stdin.on("data", async (line: Buffer) => {

    const code = line.toString().trim()

    if (code === ":quit") {
      process.exit(0)
    }

    try {

      const lexResult = lex(code, "<repl>")

      const tokens = filterLayout(lexResult.tokens)

      const ast = parse(tokens)

      console.log(JSON.stringify(ast, null, 2))

    } catch (err) {

      console.error(err)

    }

  })

}

/* ------------------------------------------------ */

function computeOutputPath(sourceFile: string) {

  const parsed = path.parse(sourceFile)

  const parts = parsed.dir.split(path.sep)

  return path.join(
    "dist",
    ...parts.slice(-2),
    parsed.name + ".js"
  )

}

/* ------------------------------------------------ */

function printHelp() {

  console.log(`
Sky compiler

Commands:
  sky init
  sky add <package>
  sky build <file.sky>
  sky run <file.sky>
  sky deps <file.sky>
  sky ast <file.sky>
  sky fmt <file.sky>
  sky repl
`)

}

/* ------------------------------------------------ */

main()

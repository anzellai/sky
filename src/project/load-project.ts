// src/project/load-project.ts

import fs from "fs"
import path from "path"

export interface SkyProject {

  readonly root: string
  readonly name: string
  readonly version: string

  readonly sourceDir: string
  readonly outputDir: string

  readonly entryModule: string

}

export function loadProject(startDir: string = process.cwd()): SkyProject {

  const root = findProjectRoot(startDir)

  const configPath = path.join(root, "sky.toml")

  if (!fs.existsSync(configPath)) {
    throw new Error("sky.toml not found in project root")
  }

  const raw = fs.readFileSync(configPath, "utf8")

  const config = parseToml(raw)

  const sourceDir =
    path.resolve(root, config.source ?? "src")

  const outputDir =
    path.resolve(root, config.output ?? "dist")

  const entryModule =
    config.entry ?? "Main"

  return {

    root,
    name: config.name ?? "sky-project",
    version: config.version ?? "0.1.0",

    sourceDir,
    outputDir,
    entryModule

  }

}

function findProjectRoot(dir: string): string {

  let current = dir

  while (true) {

    const candidate =
      path.join(current, "sky.toml")

    if (fs.existsSync(candidate)) {
      return current
    }

    const parent = path.dirname(current)

    if (parent === current) {
      throw new Error("Could not locate sky.toml")
    }

    current = parent

  }

}

function parseToml(content: string): Record<string, any> {

  const result: Record<string, any> = {}

  const lines =
    content
      .split("\n")
      .map(l => l.trim())
      .filter(Boolean)

  for (const line of lines) {

    if (line.startsWith("#")) continue

    const eq = line.indexOf("=")

    if (eq === -1) continue

    const key =
      line.slice(0, eq).trim()

    let value =
      line.slice(eq + 1).trim()

    if (value.startsWith('"') && value.endsWith('"')) {
      value = value.slice(1, -1)
    }

    result[key] = value

  }

  return result

}
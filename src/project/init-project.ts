// src/project/init-project.ts

import fs from "fs"
import path from "path"

export function initProject(targetDir: string = process.cwd()) {

  const skyToml = path.join(targetDir, "sky.toml")
  const srcDir = path.join(targetDir, "src")
  const mainDir = path.join(srcDir, "App")
  const mainFile = path.join(mainDir, "Main.sky")

  if (fs.existsSync(skyToml)) {
    throw new Error("sky.toml already exists")
  }

  fs.mkdirSync(mainDir, { recursive: true })

  fs.writeFileSync(
    skyToml,
    `name = "sky-app"
version = "0.1.0"

source = "src"
output = "dist"

entry = "App.Main"
`
  )

  fs.writeFileSync(
    mainFile,
    `module App.Main exposing (main)

main =
    "Hello Sky"
`
  )

  console.log("Sky project initialized")
}

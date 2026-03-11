// src/project/add-package.ts

import { spawnSync } from "child_process"
import fs from "fs"
import path from "path"

export function addPackage(pkg: string) {

  if (!pkg) {
    throw new Error("Missing package name")
  }

  console.log(`Installing ${pkg}...`)

  const result =
    spawnSync("npm", ["install", pkg], {
      stdio: "inherit"
    })

  if (result.status !== 0) {
    throw new Error("npm install failed")
  }

  generateBindings(pkg)

}

function generateBindings(pkg: string) {

  const pkgName =
    pkg.replace("@", "").replace("/", "_")

  const dir =
    path.join(".skycache", "packages")

  const file =
    path.join(dir, `${pkgName}.sky`)

  fs.mkdirSync(dir, { recursive: true })

  fs.writeFileSync(
    file,
    `module Sky.FFI.${pkgName} exposing ()

foreign import "${pkg}" exposing ()
`
  )

  console.log(`Generated Sky binding stub: ${file}`)

}

// src/project/resolve-entry.ts

import path from "path"
import fs from "fs"

export function resolveEntryFile(
  sourceDir: string,
  entryModule: string
): string {

  const parts =
    entryModule.split(".")

  const file =
    path.join(sourceDir, ...parts) + ".sky"

  if (!fs.existsSync(file)) {

    throw new Error(
      `Entry module not found: ${entryModule}`
    )

  }

  return file

}
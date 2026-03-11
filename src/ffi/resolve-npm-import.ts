import fs from "fs"
import path from "path"
import { generateForeignBindings } from "./generate-bindings.js"

export async function resolveNpmImport(
  moduleName: string
): Promise<string | undefined> {

  const npmPackage =
    moduleName.toLowerCase()

  const generated =
    await generateForeignBindings(
      npmPackage,
      []
    )

  if (!generated.generated) {
    return undefined
  }

  const skyModule =
    generated.generated.skyModuleName

  const file =
    path.join(
      ".skycache",
      "ffi",
      ...skyModule.split(".")
    ) + ".sky"

  if (!fs.existsSync(file)) {

    fs.mkdirSync(
      path.dirname(file),
      { recursive: true }
    )

    fs.writeFileSync(
      file,
      `module ${skyModule} exposing (..)`
    )

    fs.writeFileSync(
      file.replace(/\.sky$/, ".json"),
      JSON.stringify({ packageName: npmPackage }, null, 2)
    )

  }

  return path.resolve(file)

}

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

    let jsCode = `import * as $pkg from ${JSON.stringify(npmPackage)};\n\n`;

    for (const val of generated.generated.values) {
      if (val.parameters) {
        // Generate nested arrows for currying
        const args = val.parameters.map((_, i) => `a${i}`);
        
        let curried = "";
        if (val.methodOf) {
           curried = `instance => `;
        }
        curried += args.length > 0 ? args.join(" => ") + " => " : "(_) => ";

        // Generate arguments to pass to the underlying JS function, handling callbacks
        const callArgs = val.parameters.map((p, i) => {
          if (p.isCallback) {
            const cbArgs = Array.from({ length: p.callbackArity }, (_, j) => `c${j}`);
            if (cbArgs.length === 0) return `() => a${i}(undefined)`;
            // E.g., req => res => ...
            const skyCall = cbArgs.reduce((acc, c) => `${acc}(${c})`, `a${i}`);
            // Await Promises seamlessly on the JS side
            return `async (${cbArgs.join(", ")}) => { const r = ${skyCall}; return r instanceof Promise ? await r : r; }`;
          }
          return `a${i}`;
        });

        let call = "";
        if (val.methodOf) {
          call = `instance.${val.jsName}(${callArgs.join(", ")})`;
        } else if (val.jsName === npmPackage.replace(/[^a-zA-Z0-9]/g, "")) {
          // It's the default export factory function!
          call = `$pkg.default ? $pkg.default(${callArgs.join(", ")}) : $pkg(${callArgs.join(", ")})`;
        } else {
          call = `$pkg.${val.jsName}(${callArgs.join(", ")})`;
        }
        
        jsCode += `export const ${val.skyName} = ${curried}${call};\n`;
      } else {
        // Fallback for values / constants
        if (val.jsName === npmPackage.replace(/[^a-zA-Z0-9]/g, "")) {
           jsCode += `export const ${val.skyName} = $pkg.default || $pkg;\n`;
        } else {
           jsCode += `export const ${val.skyName} = $pkg.${val.jsName};\n`;
        }
      }
    }

    fs.writeFileSync(file.replace(/\.sky$/, ".js"), jsCode);

  }

  return path.resolve(file)

}

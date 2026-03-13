import fs from "fs";
import path from "path";
import { execSync } from "child_process";
import { readManifest } from "./manifest.js";
import { generateForeignBindings } from "../interop/go/generate-bindings.js";

import { resolveRegistryPackage } from "./registry.js";

export function installSkyPackage(pkgName: string, version: string): string {
  const depPath = path.join(".skydeps", pkgName);
  
  if (fs.existsSync(depPath)) {
    console.log(`Package ${pkgName} is already installed.`);
    return "1.0.0"; // Return mock resolved version
  }

  console.log(`Installing Sky package: ${pkgName}@${version}`);
  
  // Use registry resolution to find the repository URL
  const repoUrl = resolveRegistryPackage(pkgName, version);

  try {
    fs.mkdirSync(path.dirname(depPath), { recursive: true });
    // Shallow clone for speed
    execSync(`git clone --depth 1 ${repoUrl} ${depPath}`, { stdio: "ignore" });
    
    // Check if it has a sky.toml
    if (!fs.existsSync(path.join(depPath, "sky.toml"))) {
      console.warn(`Warning: Installed package ${pkgName} does not contain a sky.toml`);
    }
  } catch (e) {
    console.error(`Failed to install Sky package ${pkgName} from ${repoUrl}`);
    // If it fails, clean up the empty dir
    if (fs.existsSync(depPath)) {
        fs.rmSync(depPath, { recursive: true, force: true });
    }
    throw e;
  }

  return version === "latest" ? "1.0.0" : version;
}

export function installGoPackage(pkgName: string, version: string): string {
  console.log(`Installing Go package: ${pkgName}@${version}`);
  
  // Ensure go.mod exists
  if (!fs.existsSync("go.mod")) {
    execSync(`go mod init sky-project`, { stdio: "inherit" });
  }

  try {
    // If it's a standard library package, go get will fail with a specific message
    const isStdlib = !pkgName.includes(".");
    if (!isStdlib) {
      execSync(`go get ${pkgName}@${version}`, { stdio: "inherit" });
    }
  } catch (e: any) {
    console.error(`Failed to go get ${pkgName}`);
    throw e;
  }

  // Generate .skyi file
  const cacheDir = path.join(".skycache", "go", pkgName.toLowerCase());
  fs.mkdirSync(cacheDir, { recursive: true });

  const moduleName = pkgName.split(/[\/\.]/).map(p => p.charAt(0).toUpperCase() + p.slice(1)).join(".");
  
  
  
  let skyiContent = `module ${moduleName} exposing (..)\n\n`;
  
  try {
    const out = execSync(`go doc -short ${pkgName}`).toString();
    const lines = out.split("\n");

    const types: string[] = [];
    const funcs: { name: string, args: string, ret: string }[] = [];
    const vars: string[] = [];

    for (const line of lines) {
      let safeLine = line.replace(/func\(.*?\)/g, "Any");
      let m;
      if ((m = safeLine.match(/^func ([A-Z]\w*)\((.*?)\)(.*)/))) {
        funcs.push({ name: m[1], args: m[2], ret: m[3] });
      } else if ((m = line.match(/^type ([A-Z]\w*)/))) {
        types.push(m[1]);
      } else if ((m = line.match(/^(?:var|const) ([A-Z]\w*)/))) {
        vars.push(m[1]);
      }
    }

    const mapType = (t: string) => {
      if (t.includes("(") || t.includes(")") || t.includes(",")) return "Any";
      t = t.trim().replace(/^\*/, ""); // remove pointer
      if (t === "string") return "String";
      if (t === "int" || t === "byte" || t === "rune" || t.startsWith("int") || t.startsWith("uint")) return "Int";
      if (t === "float32" || t === "float64") return "Float";
      if (t === "bool") return "Bool";
      if (t === "error") return "Error";
      if (t === "any" || t === "interface{}") return "Any";
      if (t.startsWith("[]")) {
          const inner = mapType(t.substring(2));
          return inner.includes(" ") ? `(List (${inner}))` : `List ${inner}`;
      }
      if (t.startsWith("map[")) {
        const match = t.match(/map\[(.*?)\](.*)/);
        if (match) {
           const k = mapType(match[1]);
           const v = mapType(match[2]);
           return `Map ${k.includes(" ") ? `(${k})` : k} ${v.includes(" ") ? `(${v})` : v}`;
        }
      }
      const parts = t.split(".");
      return parts[parts.length - 1]; // e.g. Response
    };

    const parseRet = (ret: string) => {
        ret = ret.trim();
        if (!ret) return "Unit";
        if (ret.startsWith("(") && ret.endsWith(")")) {
            const inner = ret.substring(1, ret.length - 1);
            const parts = inner.split(",");
            const types = parts.map(p => {
                const tokens = p.trim().split(/\s+/);
                return mapType(tokens[tokens.length - 1]);
            });
            if (types.length === 1) return types[0];
            return `Tuple${types.length} ${types.join(" ")}`; // Map to Tuple2, Tuple3, etc. for cleaner interop than raw parentheses
        }
        return mapType(ret);
    };

    // Add Error and Any just in case
    skyiContent += `type Error = Error\n\ntype Any = Any\n\ntype List a = List\n\ntype Map k v = Map\n\ntype Tuple2 a b = Tuple2\n\ntype Tuple3 a b c = Tuple3\n\n`;

    for (const t of types) {
      skyiContent += `type ${t} = ${t}\n\n`;
    }

    for (const v of vars) {
      skyiContent += `${v} : Any\n`;
      skyiContent += `foreign import "${pkgName}" exposing (${v})\n\n`;
    }

    for (const f of funcs) {
      let sig = "() -> Unit";
      const argParts = f.args.split(",").filter(s => s.trim().length > 0);
      let skyArgs = [];
      
      // Safety catch for weird function nesting inside arguments
      if (f.args.includes("func(") || f.args.includes("func (")) {
         skyArgs.push("Any");
      } else {
        for (let part of argParts) {
           part = part.trim();
           const tokens = part.split(/\s+/);
           const typeStr = tokens[tokens.length - 1];
           let mapped = mapType(typeStr.replace("...", "[]"));
           if (mapped.includes(" ")) mapped = `(${mapped})`;
           skyArgs.push(mapped);
        }
      }

      let retType = parseRet(f.ret);

      if (skyArgs.length === 0) {
        sig = `() -> ${retType}`;
      } else {
        sig = skyArgs.join(" -> ") + " -> " + retType;
      }
      
      skyiContent += `${f.name} : ${sig}\n`;
      skyiContent += `foreign import "${pkgName}" exposing (${f.name})\n\n`;
    }

  } catch (e) {
    console.warn(`Warning: Could not introspect go package ${pkgName}`);
  }



  fs.writeFileSync(path.join(cacheDir, "bindings.skyi"), skyiContent);
  console.log(`Generated bindings for ${pkgName} at ${cacheDir}/bindings.skyi`);

  return version;
}

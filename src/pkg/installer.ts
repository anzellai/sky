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
      let m;
      if ((m = line.match(/^func ([A-Z]\w*)\((.*?)\)(.*)/))) {
        funcs.push({ name: m[1], args: m[2], ret: m[3] });
      } else if ((m = line.match(/^type ([A-Z]\w*)/))) {
        types.push(m[1]);
      } else if ((m = line.match(/^(?:var|const) ([A-Z]\w*)/))) {
        vars.push(m[1]);
      }
    }

    const mapType = (t: string) => {
      t = t.trim();
      if (t.includes("string")) return "String";
      if (t.includes("int") || t.includes("byte") || t.includes("rune")) return "Int";
      if (t.includes("float")) return "Float";
      if (t.includes("bool")) return "Bool";
      return "Any";
    };

    for (const t of types) {
      skyiContent += `type ${t} = ${t}\n\n`;
    }

    for (const v of vars) {
      skyiContent += `${v} : Any\n`;
      skyiContent += `foreign import "${pkgName}" exposing (${v})\n\n`;
    }

    for (const f of funcs) {
      const argParts = f.args.split(",").filter(s => s.trim().length > 0);
      const skyArgs = argParts.map(a => {
        const parts = a.trim().split(/\s+/);
        const typeStr = parts.length > 1 ? parts[parts.length - 1] : parts[0];
        return mapType(typeStr);
      });

      let retType = "Unit";
      if (f.ret.trim().length > 0) {
        retType = mapType(f.ret);
      }

      let sig = skyArgs.length === 0 ? `() -> ${retType}` : skyArgs.join(" -> ") + " -> " + retType;
      
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

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
  
  // Generate .skyi file using the rigorous tool
  const cacheDir = path.join(".skycache", "go", pkgName.toLowerCase());
  fs.mkdirSync(cacheDir, { recursive: true });

  // Use the new robust generator
  generateForeignBindings(pkgName, []).then(result => {
      if (result.skyiContent) {
          fs.writeFileSync(path.join(cacheDir, "bindings.skyi"), result.skyiContent);
          console.log(`Generated bindings for ${pkgName} at ${cacheDir}/bindings.skyi`);
      }
  }).catch(e => console.error("Binding generation failed", e));

  return version;
}

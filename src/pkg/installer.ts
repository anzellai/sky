import fs from "fs";
import path from "path";
import { execSync } from "child_process";
import { readManifest } from "./manifest.js";
import { generateForeignBindings } from "../interop/go/generate-bindings.js";

import { resolveRegistryPackage } from "./registry.js";

/**
 * Get the Go module directory. We keep go.mod/go.sum inside .skycache/gomod/
 * so they don't pollute the project root.
 */
function ensureGoModDir(): string {
  const goModDir = path.join(".skycache", "gomod");
  fs.mkdirSync(goModDir, { recursive: true });

  // Migrate: if go.mod exists in project root (from older Sky versions), move it
  if (fs.existsSync("go.mod") && !fs.existsSync(path.join(goModDir, "go.mod"))) {
    fs.renameSync("go.mod", path.join(goModDir, "go.mod"));
    if (fs.existsSync("go.sum")) {
      fs.renameSync("go.sum", path.join(goModDir, "go.sum"));
    }
  }

  if (!fs.existsSync(path.join(goModDir, "go.mod"))) {
    execSync(`go mod init sky-project`, { cwd: goModDir, stdio: "ignore" });
  }
  return goModDir;
}

/**
 * Detect whether a remote package is a Sky package (has sky.toml) or a Go package.
 * Tries GitHub raw content first, falls back to assuming Go.
 */
export async function detectPackageType(pkgName: string): Promise<"sky" | "go"> {
  // Try GitHub raw URL for sky.toml
  // Strip github.com/ prefix since raw.githubusercontent.com expects owner/repo
  const repoPath = pkgName.startsWith("github.com/") ? pkgName.slice("github.com/".length) : pkgName;
  const rawUrl = `https://raw.githubusercontent.com/${repoPath}/HEAD/sky.toml`;
  try {
    const resp = await fetch(rawUrl);
    if (resp.ok) return "sky";
  } catch {}

  return "go";
}

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

/**
 * After installing a Sky package, recursively install its transitive dependencies:
 * - Its [go.dependencies] get installed via go get
 * - Its [dependencies] (other Sky packages) get cloned, then their deps installed too
 */
export async function installTransitiveDependencies(pkgName: string, visited = new Set<string>()): Promise<void> {
  if (visited.has(pkgName)) return;
  visited.add(pkgName);

  const depManifestPath = path.join(".skydeps", pkgName, "sky.toml");
  const depManifest = readManifest(depManifestPath);
  if (!depManifest) return;

  // Install Go dependencies from the Sky dependency
  if (depManifest.go?.dependencies) {
    for (const [goPkg, version] of Object.entries(depManifest.go.dependencies)) {
      const isStdlib = !goPkg.includes(".");
      if (!isStdlib) {
        try {
          installGoPackage(goPkg, version);
        } catch (e) {
          console.warn(`Warning: Failed to install transitive Go dep ${goPkg} from ${pkgName}`);
        }
      }
    }
  }

  // Recursively install Sky dependencies of this dependency
  if (depManifest.dependencies) {
    for (const [skyPkg, version] of Object.entries(depManifest.dependencies)) {
      try {
        installSkyPackage(skyPkg, version);
        await installTransitiveDependencies(skyPkg, visited);
      } catch (e) {
        console.warn(`Warning: Failed to install transitive Sky dep ${skyPkg} from ${pkgName}`);
      }
    }
  }
}

export function installGoPackage(pkgName: string, version: string): string {
  console.log(`Installing Go package: ${pkgName}@${version}`);

  const goModDir = ensureGoModDir();

  try {
    // If it's a standard library package, go get will fail with a specific message
    const isStdlib = !pkgName.includes(".");
    if (!isStdlib) {
      execSync(`go get ${pkgName}@${version}`, { cwd: goModDir, stdio: "inherit" });
    }
  } catch (e: any) {
    console.error(`Failed to go get ${pkgName}`);
    throw e;
  }

  // Generate .skyi bindings
  const cacheDir = path.join(".skycache", "go", pkgName.toLowerCase());
  fs.mkdirSync(cacheDir, { recursive: true });

  generateForeignBindings(pkgName, []).then(result => {
      if (result.skyiContent) {
          fs.writeFileSync(path.join(cacheDir, "bindings.skyi"), result.skyiContent);
          console.log(`Generated bindings for ${pkgName} at ${cacheDir}/bindings.skyi`);
      }
  }).catch(e => console.error("Binding generation failed", e));

  return version;
}

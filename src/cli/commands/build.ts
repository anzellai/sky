import fs from "fs";
import path from "path";
import { execSync } from "child_process";
import { compileProject } from "../../compiler.js";
import { readManifest } from "../../pkg/manifest.js";
import { checkForUpdates } from "../update-check.js";

/**
 * Resolve the entry file from the argument or sky.toml.
 * Supports:
 *   - Explicit path: "src/Main.sky"
 *   - Module name:   "Main" → "src/Main.sky" (using source.root)
 *   - From manifest: entry field in sky.toml
 */
function resolveEntry(arg: string | undefined): string | null {
  if (arg) {
    // If the argument ends with .sky, use as-is
    if (arg.endsWith(".sky")) return arg;
    // Otherwise treat as a module name: resolve via source root
    const manifest = readManifest();
    const root = manifest?.source?.root || "src";
    return `${root}/${arg}.sky`;
  }

  // No argument — check sky.toml for entry
  const manifest = readManifest();
  if (manifest?.entry) {
    // entry can be "src/Main.sky" or "Main"
    if (manifest.entry.endsWith(".sky")) return manifest.entry;
    const root = manifest.source?.root || "src";
    return `${root}/${manifest.entry}.sky`;
  }

  return null;
}

export async function handleBuild(entryFile?: string) {
  const resolved = resolveEntry(entryFile);
  if (!resolved) {
    console.error("Usage: sky build <file.sky>");
    console.error("  Or set entry in sky.toml:  entry = \"src/Main.sky\"");
    process.exit(1);
  }

  if (!fs.existsSync(resolved)) {
    console.error(`Entry file not found: ${resolved}`);
    process.exit(1);
  }

  console.log(`Compiling ${resolved}...`);

  const outDir = "dist";

  const result = await compileProject(resolved, outDir);

  if (result.diagnostics && result.diagnostics.length > 0) {
    for (const diag of result.diagnostics) {
      console.error(diag);
    }
    process.exit(1);
  }

  const isLiveApp = (result as any).isLiveApp || false;
  if (isLiveApp) {
    console.log(`Successfully compiled Sky.Live app to Go in ${outDir}/`);
  } else {
    console.log(`Successfully compiled Sky to Go in ${outDir}/`);
  }

  console.log("Running go build...");
  try {
    const wrappersDir = ".skycache/go/wrappers";
    if (fs.existsSync(wrappersDir)) {
      fs.cpSync(wrappersDir, `${outDir}/sky_wrappers`, { recursive: true });
    }
    // Copy project-level Go helpers (e.g., go_helpers/*.go)
    const goHelpersDir = "go_helpers";
    if (fs.existsSync(goHelpersDir)) {
      fs.cpSync(goHelpersDir, `${outDir}/sky_wrappers`, { recursive: true });
    }
  } catch (e) {}
  try {
    if (!fs.existsSync(`${outDir}/go.mod`)) {
      execSync(`cd ${outDir} && go mod init sky-out`, { stdio: "inherit" });
    }
    execSync(`cd ${outDir} && go mod tidy`, { stdio: "inherit" });

    // Resolve output binary path from sky.toml bin field or default
    const manifest = readManifest();
    const binPath = manifest?.bin || "dist/app";
    // bin is relative to project root; go build runs in dist/
    const binAbs = path.resolve(binPath);
    const binRel = path.relative(path.resolve(outDir), binAbs);
    // Ensure output directory exists
    fs.mkdirSync(path.dirname(binAbs), { recursive: true });
    execSync(`cd ${outDir} && go build -o "${binRel}"`, { stdio: "inherit" });
    console.log(`Build complete: ${binPath}`);
  } catch (e) {
    console.error("go build failed", e);
    process.exit(1);
  }

  await checkForUpdates();
}

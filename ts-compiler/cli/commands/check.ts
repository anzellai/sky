import fs from "fs";
import process from "process";
import { typeCheckProject } from "../../compiler.js";
import { readManifest } from "../../pkg/manifest.js";

/**
 * Resolve the entry file from the argument or sky.toml.
 */
function resolveEntry(arg: string | undefined): string | null {
  if (arg) {
    if (arg.endsWith(".sky")) return arg;
    const manifest = readManifest();
    const root = manifest?.source?.root || "src";
    return `${root}/${arg}.sky`;
  }

  const manifest = readManifest();
  if (manifest?.entry) {
    if (manifest.entry.endsWith(".sky")) return manifest.entry;
    const root = manifest.source?.root || "src";
    return `${root}/${manifest.entry}.sky`;
  }

  return null;
}

export async function handleCheck(entryFile?: string) {
  const resolved = resolveEntry(entryFile);
  if (!resolved) {
    console.error("Usage: sky check <file.sky>");
    console.error("  Or set entry in sky.toml:  entry = \"src/Main.sky\"");
    process.exit(1);
  }

  if (!fs.existsSync(resolved)) {
    console.error(`Entry file not found: ${resolved}`);
    process.exit(1);
  }

  const result = await typeCheckProject(resolved);

  let errorCount = 0;
  let warningCount = 0;

  if (result.diagnostics && result.diagnostics.length > 0) {
    for (const diag of result.diagnostics) {
      if (typeof diag === "string") {
        console.error(diag);
        errorCount++;
        continue;
      }
      const severity = (diag as any).severity || "error";
      const message = (diag as any).message || String(diag);
      const span = (diag as any).span;
      const hint = (diag as any).hint;

      const loc = span
        ? `${span.start.line}:${span.start.column}`
        : "";

      const prefix = severity === "warning" ? "warning" : "error";
      console.error(`${prefix}${loc ? ` [${loc}]` : ""}: ${message}`);
      if (hint) {
        console.error(`  hint: ${hint}`);
      }

      if (severity === "warning") {
        warningCount++;
      } else {
        errorCount++;
      }
    }
  }

  if (errorCount === 0 && warningCount === 0) {
    console.log(`Type check passed: ${resolved}`);
  } else if (errorCount === 0) {
    console.log(`Type check passed with ${warningCount} warning${warningCount > 1 ? "s" : ""}: ${resolved}`);
  } else {
    console.error(`Type check failed with ${errorCount} error${errorCount > 1 ? "s" : ""}: ${resolved}`);
    process.exit(1);
  }
}

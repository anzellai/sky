import process from "process";
import { execSync } from "child_process";
import { handleBuild } from "./build.js";

export async function handleRun(file?: string) {
  await handleBuild(file);

  console.log("Running application...");
  try {
    const outDir = "dist";
    const appBinary = process.platform === "win32" ? "app.exe" : "./app";
    execSync(`cd ${outDir} && ${appBinary}`, { stdio: "inherit" });
  } catch (e: any) {
    // Exit code 130 = Ctrl+C, not an error
    if (e.status !== 130) {
      process.exit(e.status || 1);
    }
  }
}

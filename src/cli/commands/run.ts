import process from "process";
import { execSync } from "child_process";
import { handleBuild } from "./build.js";

export async function handleRun(file: string) {
  if (!file) {
    console.error("Usage: sky run <file.sky>");
    process.exit(1);
  }
  
  await handleBuild(file);
  
  console.log("Running application...");
  try {
    const outDir = "dist";
    const appBinary = process.platform === "win32" ? "app.exe" : "./app";
    execSync(`cd ${outDir} && ${appBinary}`, { stdio: "inherit" });
  } catch (e) {
    console.error("Run failed", e);
    process.exit(1);
  }
}

import process from "process";
import { execSync } from "child_process";
import { handleBuild } from "./build.js";
import { readManifest } from "../../pkg/manifest.js";

export async function handleRun(file?: string) {
  await handleBuild(file);

  const manifest = readManifest();
  const binPath = manifest?.bin || "dist/app";
  const ext = process.platform === "win32" ? ".exe" : "";
  const binary = binPath + ext;

  console.log("Running application...");
  try {
    execSync(`./${binary}`, { stdio: "inherit" });
  } catch (e: any) {
    // Exit code 130 = Ctrl+C, not an error
    if (e.status !== 130) {
      process.exit(e.status || 1);
    }
  }
}

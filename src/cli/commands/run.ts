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

  // Detect Live app by checking for [live] section in manifest
  const isLive = !!(manifest as any)?.live;
  if (isLive) {
    const port = (manifest as any).live?.port || 4000;
    console.log(`Starting Sky.Live server on http://localhost:${port}`);
    console.log("Press Ctrl+C to stop\n");
  } else {
    console.log("Running application...");
  }

  try {
    execSync(`./${binary}`, { stdio: "inherit" });
  } catch (e: any) {
    // Exit code 130 = Ctrl+C, not an error
    if (e.status !== 130) {
      process.exit(e.status || 1);
    }
  }
}

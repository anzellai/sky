import fs from "fs";
import os from "os";
import path from "path";
import { execSync } from "child_process";
import { SKY_VERSION } from "../../utils/assets.js";
import { updateCheckCache } from "../update-check.js";

const REPO = "anzellai/sky";

function compareVersions(a: string, b: string): number {
  const pa = a.replace(/^v/, "").split(".").map(Number);
  const pb = b.replace(/^v/, "").split(".").map(Number);
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const diff = (pa[i] || 0) - (pb[i] || 0);
    if (diff !== 0) return diff;
  }
  return 0;
}

function detectPlatform(): { platform: string; arch: string; ext: string } {
  const platformMap: Record<string, string> = { darwin: "darwin", linux: "linux", win32: "windows" };
  const archMap: Record<string, string> = { arm64: "arm64", x64: "x64" };

  const platform = platformMap[process.platform];
  const arch = archMap[process.arch];
  if (!platform || !arch) {
    throw new Error(`Unsupported platform: ${process.platform}/${process.arch}`);
  }
  const ext = process.platform === "win32" ? ".exe" : "";
  return { platform, arch, ext };
}

async function fetchLatestVersion(): Promise<string> {
  const resp = await fetch(`https://api.github.com/repos/${REPO}/releases/latest`, {
    headers: { "Accept": "application/vnd.github.v3+json" },
  });
  if (!resp.ok) {
    throw new Error(`GitHub API error: ${resp.status}`);
  }
  const data = await resp.json() as { tag_name?: string };
  const version = (data.tag_name || "").replace(/^v/, "");
  if (!version) throw new Error("Could not determine latest version");
  return version;
}

async function downloadBinary(url: string, destPath: string): Promise<void> {
  const resp = await fetch(url, { redirect: "follow" });
  if (!resp.ok) {
    throw new Error(`Failed to download ${url}: ${resp.status}`);
  }
  const buffer = Buffer.from(await resp.arrayBuffer());
  // Write to temp file then rename for atomicity
  const tmpPath = destPath + ".tmp";
  fs.writeFileSync(tmpPath, buffer, { mode: 0o755 });
  fs.renameSync(tmpPath, destPath);
}

export async function handleUpgrade() {
  console.log("Checking for updates...");

  let latest: string;
  try {
    // Always fetch fresh — bypass the 24h cache
    latest = await fetchLatestVersion();
    // Update the cache so subsequent commands don't re-check
    updateCheckCache(latest);
  } catch (e: any) {
    console.error(`Failed to check for updates: ${e.message}`);
    process.exit(1);
  }

  const current: string = SKY_VERSION || "dev";
  const isDev = current === "dev" || current.includes("-dev");
  if (!isDev && compareVersions(latest, current) <= 0) {
    console.log(`Already up to date (v${current}).`);
    return;
  }

  console.log(`Upgrading: v${current} -> v${latest}`);

  const { platform, arch, ext } = detectPlatform();

  // Determine install directory from current binary location
  const skyBin = process.execPath;
  const installDir = path.dirname(skyBin);
  const skyPath = path.join(installDir, `sky${ext}`);
  const lspPath = path.join(installDir, `sky-lsp${ext}`);

  const skyAsset = `sky-${platform}-${arch}${ext}`;
  const lspAsset = `sky-lsp-${platform}-${arch}${ext}`;
  const baseUrl = `https://github.com/${REPO}/releases/download/v${latest}`;

  try {
    console.log(`Downloading sky v${latest}...`);
    await downloadBinary(`${baseUrl}/${skyAsset}`, skyPath);

    console.log(`Downloading sky-lsp v${latest}...`);
    await downloadBinary(`${baseUrl}/${lspAsset}`, lspPath);
  } catch (e: any) {
    // If direct write fails (permission denied), retry with sudo on Unix
    if (e.code === "EACCES" && process.platform !== "win32") {
      console.log("Requires elevated permissions. Retrying with sudo...");
      const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "sky-upgrade-"));
      const tmpSky = path.join(tmpDir, `sky${ext}`);
      const tmpLsp = path.join(tmpDir, `sky-lsp${ext}`);

      await downloadBinary(`${baseUrl}/${skyAsset}`, tmpSky);
      await downloadBinary(`${baseUrl}/${lspAsset}`, tmpLsp);

      execSync(`sudo mv "${tmpSky}" "${skyPath}" && sudo mv "${tmpLsp}" "${lspPath}"`, { stdio: "inherit" });
      fs.rmSync(tmpDir, { recursive: true, force: true });
    } else {
      console.error(`Upgrade failed: ${e.message}`);
      process.exit(1);
    }
  }

  console.log(`\nSky upgraded to v${latest} successfully!`);
}

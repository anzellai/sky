import fs from "fs";
import path from "path";
import os from "os";
import { SKY_VERSION } from "../utils/assets.js";

const REPO = "anzellai/sky";
const CHECK_INTERVAL_MS = 24 * 60 * 60 * 1000; // 24 hours
const CACHE_DIR = path.join(os.homedir(), ".sky");
const CACHE_FILE = path.join(CACHE_DIR, "last-update-check.json");

interface CacheData {
  lastCheck: number;
  latestVersion: string;
}

function readCache(): CacheData | null {
  try {
    if (fs.existsSync(CACHE_FILE)) {
      return JSON.parse(fs.readFileSync(CACHE_FILE, "utf8"));
    }
  } catch {}
  return null;
}

function writeCache(data: CacheData) {
  try {
    fs.mkdirSync(CACHE_DIR, { recursive: true });
    fs.writeFileSync(CACHE_FILE, JSON.stringify(data));
  } catch {}
}

function compareVersions(a: string, b: string): number {
  const pa = a.replace(/^v/, "").split(".").map(Number);
  const pb = b.replace(/^v/, "").split(".").map(Number);
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const diff = (pa[i] || 0) - (pb[i] || 0);
    if (diff !== 0) return diff;
  }
  return 0;
}

/**
 * Non-blocking update check. Call at end of commands.
 * Fetches latest release from GitHub (at most once per 24h) and prints
 * a notice if a newer version is available.
 */
export async function checkForUpdates(): Promise<void> {
  // Skip for dev/local builds or when version is unknown
  const version: string = SKY_VERSION;
  if (!version || version === "dev" || version.includes("-dev")) return;

  const cache = readCache();
  const now = Date.now();

  // Use cached result if checked recently
  if (cache && now - cache.lastCheck < CHECK_INTERVAL_MS) {
    if (compareVersions(cache.latestVersion, SKY_VERSION) > 0) {
      printUpdateNotice(cache.latestVersion);
    }
    return;
  }

  try {
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), 3000);

    const resp = await fetch(
      `https://api.github.com/repos/${REPO}/releases/latest`,
      {
        headers: { "Accept": "application/vnd.github.v3+json" },
        signal: controller.signal,
      }
    );
    clearTimeout(timeout);

    if (!resp.ok) return;

    const data = await resp.json() as { tag_name?: string };
    const latest = (data.tag_name || "").replace(/^v/, "");
    if (!latest) return;

    writeCache({ lastCheck: now, latestVersion: latest });

    if (compareVersions(latest, SKY_VERSION) > 0) {
      printUpdateNotice(latest);
    }
  } catch {
    // Network errors are silently ignored — never block the user
  }
}

/**
 * Update the cache with a known latest version.
 * Called by `sky upgrade` after a fresh fetch, so subsequent commands
 * don't re-check within the 24h window.
 */
export function updateCheckCache(latestVersion: string) {
  writeCache({ lastCheck: Date.now(), latestVersion });
}

function printUpdateNotice(latest: string) {
  console.log(
    `\nA newer version of Sky is available: v${latest} (current: v${SKY_VERSION}). Run 'sky upgrade' to update.`
  );
}

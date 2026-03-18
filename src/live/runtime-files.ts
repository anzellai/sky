// src/live/runtime-files.ts
// Writes the Go runtime files for Sky.Live into the dist directory.
// Reads from src/runtime/go/skylive_rt/ if available on disk,
// otherwise falls back to VIRTUAL_ASSETS (bundled in assets.ts).

import fs from "fs";
import path from "path";
import { getDirname } from "../utils/path.js";
import { VIRTUAL_ASSETS } from "../utils/assets.js";

const __dirname = getDirname(import.meta.url);

const RUNTIME_FILES = [
  "vnode.go",
  "diff.go",
  "session.go",
  "server.go",
  "store_sqlite.go",
  "eventsource.go",
  "sse.go",
  "livejs.go",
  "parse.go",
];

/**
 * Write all Sky.Live Go runtime files to outDir/skylive_rt/.
 */
export function writeRuntimeFiles(outDir: string): void {
  const rtDir = path.join(outDir, "skylive_rt");
  fs.mkdirSync(rtDir, { recursive: true });

  // Try to find source Go files on disk first
  const possibleDirs = [
    path.join(__dirname, "../../src/runtime/go/skylive_rt"),
    path.join(__dirname, "../src/runtime/go/skylive_rt"),
    path.join(process.cwd(), "src/runtime/go/skylive_rt"),
  ];

  for (const srcDir of possibleDirs) {
    if (fs.existsSync(srcDir) && fs.existsSync(path.join(srcDir, "server.go"))) {
      // Copy from disk
      for (const file of RUNTIME_FILES) {
        const src = path.join(srcDir, file);
        if (fs.existsSync(src)) {
          fs.copyFileSync(src, path.join(rtDir, file));
        }
      }
      return;
    }
  }

  // Fallback: read from VIRTUAL_ASSETS (bundled binary)
  let wrote = false;
  for (const file of RUNTIME_FILES) {
    const key = `runtime/go/skylive_rt/${file}`;
    if (VIRTUAL_ASSETS[key]) {
      fs.writeFileSync(path.join(rtDir, file), VIRTUAL_ASSETS[key]);
      wrote = true;
    }
  }

  if (!wrote) {
    console.error("Warning: Sky.Live runtime files not found. The build may fail.");
  }
}

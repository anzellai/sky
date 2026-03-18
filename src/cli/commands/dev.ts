import fs from "fs";
import path from "path";
import process from "process";
import { spawn, execSync, ChildProcess } from "child_process";
import { compileProject } from "../../compiler.js";
import { readManifest } from "../../pkg/manifest.js";

const PREFIX = "[sky dev]";

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

/**
 * Run the full build pipeline: compile Sky -> Go, then go build.
 * Returns true on success, false on failure.
 */
async function buildProject(entryFile: string): Promise<boolean> {
  const outDir = "dist";

  try {
    const result = await compileProject(entryFile, outDir);

    if (result.diagnostics && result.diagnostics.length > 0) {
      for (const diag of result.diagnostics) {
        console.error(diag);
      }
      return false;
    }

    const isLiveApp = (result as any).isLiveApp || false;
    if (isLiveApp) {
      console.log(`${PREFIX} Compiled Sky.Live app to Go`);
    } else {
      console.log(`${PREFIX} Compiled Sky to Go`);
    }

    // Copy wrappers
    try {
      const wrappersDir = ".skycache/go/wrappers";
      if (fs.existsSync(wrappersDir)) {
        fs.cpSync(wrappersDir, `${outDir}/sky_wrappers`, { recursive: true });
      }
      const goHelpersDir = "go_helpers";
      if (fs.existsSync(goHelpersDir)) {
        fs.cpSync(goHelpersDir, `${outDir}/sky_wrappers`, { recursive: true });
      }
    } catch (_e) {}

    // go mod init if needed, then tidy + build
    if (!fs.existsSync(`${outDir}/go.mod`)) {
      execSync(`cd ${outDir} && go mod init sky-out`, { stdio: "inherit" });
    }
    execSync(`cd ${outDir} && go mod tidy`, { stdio: "inherit" });

    const manifest = readManifest();
    const binPath = manifest?.bin || "dist/app";
    const binAbs = path.resolve(binPath);
    const binRel = path.relative(path.resolve(outDir), binAbs);
    fs.mkdirSync(path.dirname(binAbs), { recursive: true });
    execSync(`cd ${outDir} && go build -o "${binRel}"`, { stdio: "inherit" });

    return true;
  } catch (e) {
    console.error(`${PREFIX} Build failed:`, e);
    return false;
  }
}

export async function handleDev(file?: string) {
  const resolved = resolveEntry(file);
  if (!resolved) {
    console.error("Usage: sky dev [file.sky]");
    console.error("  Or set entry in sky.toml:  entry = \"src/Main.sky\"");
    process.exit(1);
  }

  if (!fs.existsSync(resolved)) {
    console.error(`Entry file not found: ${resolved}`);
    process.exit(1);
  }

  const manifest = readManifest();
  const binPath = manifest?.bin || "dist/app";
  const ext = process.platform === "win32" ? ".exe" : "";
  const binary = binPath + ext;
  const isLive = !!manifest?.live;
  const port = manifest?.live?.port || 4000;
  const srcRoot = manifest?.source?.root || "src";

  let child: ChildProcess | null = null;
  let debounceTimer: ReturnType<typeof setTimeout> | null = null;
  let building = false;

  function killChild(): Promise<void> {
    return new Promise((resolve) => {
      if (!child || child.exitCode !== null) {
        child = null;
        resolve();
        return;
      }
      child.once("exit", () => {
        child = null;
        resolve();
      });
      child.kill("SIGTERM");
      // Force kill after 3 seconds if it hasn't exited
      setTimeout(() => {
        if (child && child.exitCode === null) {
          child.kill("SIGKILL");
        }
      }, 3000);
    });
  }

  function spawnApp() {
    child = spawn(`./${binary}`, [], { stdio: "inherit" });
    child.on("error", (err) => {
      console.error(`${PREFIX} Failed to start process:`, err.message);
    });
    child.on("exit", (code, signal) => {
      // Only log unexpected exits (not from our own kill)
      if (child && signal !== "SIGTERM" && signal !== "SIGKILL" && code !== null && code !== 0) {
        console.error(`${PREFIX} Process exited with code ${code}`);
      }
    });
  }

  async function rebuild() {
    if (building) return;
    building = true;

    console.log(`\n${PREFIX} Rebuilding...`);

    await killChild();

    const success = await buildProject(resolved!);

    if (success) {
      console.log(`${PREFIX} Server restarted`);
      if (isLive) {
        console.log(`${PREFIX} http://localhost:${port}`);
      }
      spawnApp();
    } else {
      console.log(`${PREFIX} Build failed, waiting for changes...`);
    }

    building = false;
  }

  // Graceful shutdown on Ctrl+C
  function cleanup() {
    console.log(`\n${PREFIX} Shutting down...`);
    if (debounceTimer) clearTimeout(debounceTimer);
    if (child && child.exitCode === null) {
      child.kill("SIGTERM");
    }
    process.exit(0);
  }
  process.on("SIGINT", cleanup);
  process.on("SIGTERM", cleanup);

  // Initial build + run
  console.log(`${PREFIX} Starting dev server...`);
  const success = await buildProject(resolved);
  if (!success) {
    console.error(`${PREFIX} Initial build failed`);
    process.exit(1);
  }

  if (isLive) {
    console.log(`${PREFIX} Sky.Live server at http://localhost:${port}`);
  }
  spawnApp();

  // Set up file watcher
  const watchPaths: string[] = [];

  if (fs.existsSync(srcRoot)) {
    watchPaths.push(srcRoot);
  }
  if (fs.existsSync("sky.toml")) {
    watchPaths.push("sky.toml");
  }

  console.log(`${PREFIX} Watching for changes...`);

  for (const watchPath of watchPaths) {
    const isDir = fs.statSync(watchPath).isDirectory();
    const watcher = fs.watch(watchPath, { recursive: isDir }, (_event, filename) => {
      // Only react to .sky and .toml files
      if (filename) {
        const ext = path.extname(filename);
        if (ext !== ".sky" && ext !== ".toml") return;
      }

      if (debounceTimer) clearTimeout(debounceTimer);
      debounceTimer = setTimeout(() => {
        rebuild();
      }, 300);
    });

    watcher.on("error", (err) => {
      console.error(`${PREFIX} Watch error:`, err.message);
    });
  }

  // Keep the process alive
  await new Promise<void>(() => {});
}

import process from "process";
import { initProject } from "./commands/init.js";
import { handleAdd } from "./commands/add.js";
import { handleRemove } from "./commands/remove.js";
import { handleInstall } from "./commands/install.js";
import { handleUpdate } from "./commands/update.js";
import { handleBuild } from "./commands/build.js";
import { handleRun } from "./commands/run.js";
import { handleDev } from "./commands/dev.js";
import { handleFmt } from "./commands/fmt.js";
import { handleCheck } from "./commands/check.js";
import { handleClean } from "./commands/clean.js";
import { handleUpgrade } from "./commands/upgrade.js";
import { startServer } from "../lsp/server.js";
import { SKY_VERSION } from "../utils/assets.js";

async function main() {
  const args = process.argv.slice(2);
  const command = args[0];

  switch (command) {
    case "init":
      await initProject(args[1]);
      return;
    case "add":
      await handleAdd(args[1]);
      return;
    case "remove":
      handleRemove(args[1]);
      return;
    case "install":
      await handleInstall();
      return;
    case "update":
      handleUpdate();
      return;
    case "build":
      await handleBuild(args[1]);
      return;
    case "run":
      await handleRun(args[1]);
      return;
    case "dev":
      await handleDev(args[1]);
      return;
    case "check":
      await handleCheck(args[1]);
      return;
    case "fmt":
      await handleFmt(args[1]);
      return;
    case "clean":
      handleClean();
      return;
    case "upgrade":
      await handleUpgrade();
      return;
    case "lsp":
      // Helix sometimes passes "-" to mean stdin, but LSP is already stdio based.
      // Ensure --stdio flag is present for vscode-languageserver transport detection.
      if (!process.argv.includes("--stdio")) {
        process.argv.push("--stdio");
      }
      startServer();
      return;
    case "--version":
    case "-v":
      console.log(`sky v${SKY_VERSION}`);
      return;
    case "--help":
    case "-h":
    case undefined:
      printHelp();
      return;
    default:
      printHelp();
      process.exit(1);
  }
}

function printHelp() {
  console.log(`
Sky compiler v${SKY_VERSION} (Go backend)

Commands:
  sky init [name]
  sky add <package>
  sky remove <package>
  sky install
  sky update
  sky build [file.sky]      (uses entry from sky.toml if omitted)
  sky run [file.sky]        (uses entry from sky.toml if omitted)
  sky dev [file.sky]        (watch mode: auto-rebuild + restart on changes)
  sky check <file.sky>
  sky fmt <file-or-dir>
  sky clean                 (remove dist/, .skycache/, .skydeps/)
  sky upgrade               (update sky to the latest release)
  sky lsp

Flags:
  --version, -v             Show version
  --help, -h                Show this help
`);
}

main();

import process from "process";
import { initProject } from "./commands/init.js";
import { handleAdd } from "./commands/add.js";
import { handleRemove } from "./commands/remove.js";
import { handleInstall } from "./commands/install.js";
import { handleUpdate } from "./commands/update.js";
import { handleBuild } from "./commands/build.js";
import { handleRun } from "./commands/run.js";
import { handleFmt } from "./commands/fmt.js";
import { startServer } from "../lsp/server.js";

async function main() {
  const args = process.argv.slice(2);
  const command = args[0];

  switch (command) {
    case "init":
      initProject();
      return;
    case "add":
      handleAdd(args[1]);
      return;
    case "remove":
      handleRemove(args[1]);
      return;
    case "install":
      handleInstall();
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
    case "check":
      console.log("Check not fully implemented yet.");
      return;
    case "fmt":
      await handleFmt(args[1]);
      return;
    case "lsp":
      // Helix sometimes passes "-" to mean stdin, but LSP is already stdio based.
      startServer();
      return;
    default:
      printHelp();
      process.exit(1);
  }
}

function printHelp() {
  console.log(`
Sky compiler (Go backend)

Commands:
  sky init
  sky add <package>
  sky remove <package>
  sky install
  sky update
  sky build [file.sky]      (uses entry from sky.toml if omitted)
  sky run [file.sky]        (uses entry from sky.toml if omitted)
  sky check <file.sky>
  sky fmt <file-or-dir>
  sky lsp
`);
}

main();

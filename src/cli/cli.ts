import process from "process";
import fs from "fs";
import { execSync } from "child_process";
import { compileProject } from "../compiler.js";

export function handleAdd(pkgName: string) {
  if (!pkgName) {
    console.error("Usage: sky add <go-package>");
    process.exit(1);
  }
  console.log(`Adding Go package: ${pkgName}`);
  
  try {
    execSync(`go get ${pkgName}`, { stdio: "inherit" });
  } catch (e) {
    console.error(`Failed to get go package: ${pkgName}`);
    if (!fs.existsSync("go.mod")) {
      execSync(`go mod init sky-project`, { stdio: "inherit" });
      execSync(`go get ${pkgName}`, { stdio: "inherit" });
    }
  }

  const tomlPath = "sky.toml";
  let tomlContent = "";
  if (fs.existsSync(tomlPath)) {
    tomlContent = fs.readFileSync(tomlPath, "utf-8");
  } else {
    tomlContent = `[project]\nname = "sky-project"\ntype = "application"\n\n[dependencies]\n`;
  }

  if (!tomlContent.includes(`"${pkgName}"`)) {
    tomlContent += `\n"${pkgName}" = "latest"`;
    fs.writeFileSync(tomlPath, tomlContent);
  }
  console.log("Done.");
}

export async function handleBuild(entryFile: string) {
  if (!entryFile) {
    console.error("Usage: sky build <file.sky>");
    process.exit(1);
  }

  console.log(`Compiling ${entryFile}...`);
  
  const outDir = "dist";
  
  const result = await compileProject(entryFile, outDir, "node");
  
  if (result.diagnostics && result.diagnostics.length > 0) {
    for (const diag of result.diagnostics) {
      console.error(diag);
    }
    process.exit(1);
  }

  console.log(`Successfully compiled Sky to Go in ${outDir}/`);

  console.log("Running go build...");
  try {
    if (!fs.existsSync(`${outDir}/go.mod`)) {
      execSync(`cd ${outDir} && go mod init sky-out`, { stdio: "inherit" });
    }
    execSync(`cd ${outDir} && go mod tidy`, { stdio: "inherit" });
    execSync(`cd ${outDir} && go build -o app`, { stdio: "inherit" });
    console.log("Build complete: dist/app");
  } catch (e) {
    console.error("go build failed", e);
    process.exit(1);
  }
}

export function initProject() {
  console.log("Initializing Sky project...");

  const tomlContent = `[project]\nname = "sky-project"\ntype = "application"\n\n[dependencies]\n`;

  if (!fs.existsSync("sky.toml")) {
    fs.writeFileSync("sky.toml", tomlContent);
    console.log("Created sky.toml");
  }

  if (!fs.existsSync("src")) {
    fs.mkdirSync("src");
    console.log("Created src directory");
  }

  const mainContent = `module Main exposing (main)\n\nimport Std.Log exposing (println)\n\nmain =\n    println "Hello from Sky!"\n`;

  if (!fs.existsSync("src/Main.sky")) {
    fs.writeFileSync("src/Main.sky", mainContent);
    console.log("Created src/Main.sky");
  }

  console.log("Project initialized successfully.");
}

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
    case "build":
      await handleBuild(args[1]);
      return;
    case "run":
      await cmdRun(args[1]);
      return;
    case "check":
      console.log("Check not fully implemented yet.");
      return;
    default:
      printHelp();
      process.exit(1);
  }
}

async function cmdRun(file: string) {
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

function printHelp() {
  console.log(`
Sky compiler (Go backend)

Commands:
  sky init
  sky add <go-package>
  sky build <file.sky>
  sky run <file.sky>
  sky check <file.sky>
`);
}

main();

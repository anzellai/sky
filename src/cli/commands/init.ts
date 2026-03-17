import fs from "fs";
import path from "path";
import { writeManifest } from "../../pkg/manifest.js";

export function initProject() {
  console.log("Initializing Sky project...");

  // Use current directory name as project name
  const projectName = path.basename(process.cwd());

  if (!fs.existsSync("sky.toml")) {
    writeManifest({
      name: projectName,
      version: "0.1.0",
      entry: "src/Main.sky",
      source: { root: "src" }
    });
    console.log("Created sky.toml");
  }

  if (!fs.existsSync("src")) {
    fs.mkdirSync("src");
    console.log("Created src/");
  }

  const mainContent = `module Main exposing (main)

import Std.Log exposing (println)

main =
    println "Hello from Sky!"
`;

  if (!fs.existsSync("src/Main.sky")) {
    fs.writeFileSync("src/Main.sky", mainContent);
    console.log("Created src/Main.sky");
  }

  // Create .gitignore if it doesn't exist
  if (!fs.existsSync(".gitignore")) {
    const gitignore = `# Sky build artifacts
dist/
.skycache/
.skydeps/
sky.lock
go.mod
go.sum
`;
    fs.writeFileSync(".gitignore", gitignore);
    console.log("Created .gitignore");
  }

  console.log(`\nProject "${projectName}" initialized successfully.`);
  console.log("Run: sky run");
}

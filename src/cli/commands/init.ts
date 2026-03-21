import fs from "fs";
import path from "path";
import { fileURLToPath } from "url";
import { writeManifest } from "../../pkg/manifest.js";

export async function initProject(name?: string) {
  // If a name is provided, create the directory and work inside it
  if (name) {
    const targetDir = path.resolve(name);
    if (!fs.existsSync(targetDir)) {
      fs.mkdirSync(targetDir, { recursive: true });
    }
    process.chdir(targetDir);
  }

  const projectName = name || path.basename(process.cwd());
  console.log(`Initializing Sky project "${projectName}"...`);

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

# Databases
*.db
*.db-shm
*.db-wal

# Environment
.env
`;
    fs.writeFileSync(".gitignore", gitignore);
    console.log("Created .gitignore");
  }

  // Create CLAUDE.md for AI-assisted development
  if (!fs.existsSync("CLAUDE.md")) {
    const __dirname = path.dirname(fileURLToPath(import.meta.url));
    const claudeMdPath = path.join(__dirname, "..", "..", "..", "templates", "CLAUDE.md");
    if (fs.existsSync(claudeMdPath)) {
      fs.copyFileSync(claudeMdPath, "CLAUDE.md");
    } else {
      // Fallback: try virtual assets
      try {
        const { VIRTUAL_ASSETS } = await import("../../utils/assets.js");
        const content = VIRTUAL_ASSETS["templates/CLAUDE.md"];
        if (content) fs.writeFileSync("CLAUDE.md", content);
      } catch {}
    }
    if (fs.existsSync("CLAUDE.md")) {
      console.log("Created CLAUDE.md");
    }
  }

  console.log(`\nProject "${projectName}" initialized successfully.`);
  console.log("Run: sky run");
}

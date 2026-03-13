import fs from "fs";
import { writeManifest } from "../../pkg/manifest.js";

export function initProject() {
  console.log("Initializing Sky project...");

  if (!fs.existsSync("sky.toml")) {
    writeManifest({
      name: "sky-project",
      version: "0.1.0",
      source: { root: "src" }
    });
    console.log("Created sky.toml");
  }

  if (!fs.existsSync("src")) {
    fs.mkdirSync("src");
    console.log("Created src directory");
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

  console.log("Project initialized successfully.");
}

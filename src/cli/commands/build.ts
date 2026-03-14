import fs from "fs";
import { execSync } from "child_process";
import { compileProject } from "../../compiler.js";

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
    const wrappersDir = ".skycache/go/wrappers";
    if (fs.existsSync(wrappersDir)) {
      fs.cpSync(wrappersDir, `${outDir}/sky_wrappers`, { recursive: true });
    }
  } catch (e) {}
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

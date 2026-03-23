import fs from "fs";

const CLEAN_DIRS = ["dist", ".skycache", ".skydeps"];

export function handleClean() {
  let cleaned = false;

  for (const dir of CLEAN_DIRS) {
    if (fs.existsSync(dir)) {
      fs.rmSync(dir, { recursive: true, force: true });
      console.log(`Removed ${dir}/`);
      cleaned = true;
    }
  }

  if (!cleaned) {
    console.log("Nothing to clean.");
  }
}

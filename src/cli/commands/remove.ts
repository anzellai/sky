import process from "process";
import fs from "fs";
import path from "path";
import { readManifest, writeManifest } from "../../pkg/manifest.js";
import { readLockfile, writeLockfile } from "../../pkg/lockfile.js";

export function handleRemove(pkgName: string) {
  if (!pkgName) {
    console.error("Usage: sky remove <package>");
    process.exit(1);
  }

  const manifest = readManifest();
  const lockfile = readLockfile();

  let removed = false;

  if (manifest) {
    if (manifest.dependencies && manifest.dependencies[pkgName]) {
      delete manifest.dependencies[pkgName];
      removed = true;
    }
    if (manifest.go?.dependencies && manifest.go.dependencies[pkgName]) {
      delete manifest.go.dependencies[pkgName];
      removed = true;
    }
    if (removed) {
      writeManifest(manifest);
    }
  }

  if (lockfile) {
    if (lockfile.dependencies && lockfile.dependencies[pkgName]) {
      delete lockfile.dependencies[pkgName];
    }
    if (lockfile.go && lockfile.go[pkgName]) {
      delete lockfile.go[pkgName];
    }
    writeLockfile(lockfile);
  }

  // Optionally remove from .skydeps or .skycache, but we can leave it for now
  const skyDepPath = path.join(".skydeps", pkgName);
  if (fs.existsSync(skyDepPath)) {
    fs.rmSync(skyDepPath, { recursive: true, force: true });
  }

  if (removed) {
    console.log(`Removed ${pkgName}`);
  } else {
    console.log(`Package ${pkgName} not found in dependencies.`);
  }
}

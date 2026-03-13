import fs from "fs";
import * as yaml from "js-yaml";

export interface SkyLockfile {
  dependencies?: Record<string, string>;
  go?: Record<string, string>;
}

export function readLockfile(path = "sky.lock"): SkyLockfile | null {
  if (!fs.existsSync(path)) return null;
  try {
    const content = fs.readFileSync(path, "utf8");
    const parsed = yaml.load(content) as any;
    
    return {
      dependencies: parsed?.dependencies || {},
      go: parsed?.go || {},
    };
  } catch (e) {
    console.error(`Failed to parse ${path}`, e);
    return null;
  }
}

export function writeLockfile(lockfile: SkyLockfile, path = "sky.lock") {
  const out: any = {};
  if (lockfile.dependencies && Object.keys(lockfile.dependencies).length > 0) {
    out.dependencies = lockfile.dependencies;
  }
  if (lockfile.go && Object.keys(lockfile.go).length > 0) {
    out.go = lockfile.go;
  }
  
  fs.writeFileSync(path, yaml.dump(out, { sortKeys: true }));
}

import { readManifest } from "../../pkg/manifest.js";
import { readLockfile, writeLockfile, SkyLockfile } from "../../pkg/lockfile.js";
import { installGoPackage, installSkyPackage } from "../../pkg/installer.js";
import { resolveDependencies } from "../../pkg/resolver.js";
import { checkForUpdates } from "../update-check.js";

export async function handleInstall() {
  const manifest = readManifest();
  if (!manifest) {
    console.error("No sky.toml found.");
    process.exit(1);
  }

  console.log("Resolving dependencies...");
  const resolvedDeps = resolveDependencies(manifest);
  
  const lockfile: SkyLockfile = { dependencies: {}, go: {} };

  for (const dep of resolvedDeps) {
    if (dep.isGo) {
      const resolvedVersion = installGoPackage(dep.name, dep.version);
      if (lockfile.go) {
        lockfile.go[dep.name] = resolvedVersion;
      }
    } else {
      const resolvedVersion = installSkyPackage(dep.name, dep.version);
      if (lockfile.dependencies) {
        lockfile.dependencies[dep.name] = resolvedVersion;
      }
    }
  }

  writeLockfile(lockfile);
  console.log("Install complete.");
  await checkForUpdates();
}

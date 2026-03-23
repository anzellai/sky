import process from "process";
import { readManifest, writeManifest, SkyManifest } from "../../pkg/manifest.js";
import { detectPackageType, installGoPackage, installSkyPackage, installTransitiveDependencies } from "../../pkg/installer.js";
import { readLockfile, writeLockfile, SkyLockfile } from "../../pkg/lockfile.js";
import { checkForUpdates } from "../update-check.js";

const goStdlibRoots = ["archive", "bufio", "bytes", "compress", "container", "context", "crypto", "database", "debug", "embed", "encoding", "errors", "expvar", "flag", "fmt", "go", "hash", "html", "image", "index", "io", "log", "maps", "math", "mime", "net", "os", "path", "plugin", "reflect", "regexp", "runtime", "slices", "sort", "strconv", "strings", "sync", "syscall", "testing", "text", "time", "unicode", "unsafe"];

export async function handleAdd(pkgName: string) {
  if (!pkgName) {
    console.error("Usage: sky add <package>");
    process.exit(1);
  }

  const manifest = readManifest() || { name: "sky-project", version: "0.1.0" };
  const lockfile = readLockfile() || {};

  const firstPart = pkgName.split("/")[0];

  // Go stdlib — no dots in domain, known root package
  if (goStdlibRoots.includes(firstPart)) {
    addGoPackage(pkgName, manifest, lockfile);
    writeManifest(manifest);
    writeLockfile(lockfile);
    console.log("Done.");
    await checkForUpdates();
    return;
  }

  // Domain-prefixed path (e.g. github.com/...) — auto-detect Sky vs Go
  if (pkgName.includes("/") && firstPart.includes(".")) {
    console.log(`Detecting package type for ${pkgName}...`);
    const pkgType = await detectPackageType(pkgName);

    if (pkgType === "sky") {
      console.log(`Detected Sky package: ${pkgName}`);
      await addSkyPackage(pkgName, manifest, lockfile);
    } else {
      console.log(`Detected Go package: ${pkgName}`);
      addGoPackage(pkgName, manifest, lockfile);
    }
  } else {
    // Bare name — treat as Sky package
    await addSkyPackage(pkgName, manifest, lockfile);
  }

  writeManifest(manifest);
  writeLockfile(lockfile);
  console.log("Done.");
  await checkForUpdates();
}

async function addSkyPackage(pkgName: string, manifest: SkyManifest, lockfile: SkyLockfile) {
  try {
    const version = installSkyPackage(pkgName, "latest");
    manifest.dependencies = manifest.dependencies || {};
    manifest.dependencies[pkgName] = version;

    lockfile.dependencies = lockfile.dependencies || {};
    lockfile.dependencies[pkgName] = version;

    // Install transitive deps (Go deps of the Sky package, nested Sky deps)
    await installTransitiveDependencies(pkgName);
  } catch (e: any) {
    process.exit(1);
  }
}

function addGoPackage(pkgName: string, manifest: SkyManifest, lockfile: SkyLockfile) {
  try {
    const version = installGoPackage(pkgName, "latest");
    manifest.go = manifest.go || { dependencies: {} };
    manifest.go.dependencies = manifest.go.dependencies || {};
    manifest.go.dependencies[pkgName] = version;

    lockfile.go = lockfile.go || {};
    lockfile.go[pkgName] = version;
  } catch (e: any) {
    process.exit(1);
  }
}

import process from "process";
import { readManifest, writeManifest, SkyManifest } from "../../pkg/manifest.js";
import { installGoPackage, installSkyPackage } from "../../pkg/installer.js";
import { readLockfile, writeLockfile, SkyLockfile } from "../../pkg/lockfile.js";

export function handleAdd(pkgName: string) {
  if (!pkgName) {
    console.error("Usage: sky add <package>");
    process.exit(1);
  }

  const manifest = readManifest() || { name: "sky-project", version: "0.1.0" };
  const lockfile = readLockfile() || {};

  // Detect Go packages: domain-prefixed paths, stdlib paths with slashes, or known stdlib names.
  const goStdlibRoots = ["archive", "bufio", "bytes", "compress", "container", "context", "crypto", "database", "debug", "embed", "encoding", "errors", "expvar", "flag", "fmt", "go", "hash", "html", "image", "index", "io", "log", "maps", "math", "mime", "net", "os", "path", "plugin", "reflect", "regexp", "runtime", "slices", "sort", "strconv", "strings", "sync", "syscall", "testing", "text", "time", "unicode", "unsafe"];
  const firstPart = pkgName.split("/")[0];
  const isGoPackage = pkgName.includes("/") || pkgName.includes(".") || goStdlibRoots.includes(firstPart);

  if (isGoPackage) {
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
  } else {
    // Treat as Sky package
    try {
      const version = installSkyPackage(pkgName, "latest");
      manifest.dependencies = manifest.dependencies || {};
      manifest.dependencies[pkgName] = version;
      
      lockfile.dependencies = lockfile.dependencies || {};
      lockfile.dependencies[pkgName] = version;
    } catch (e: any) {
      process.exit(1);
    }
  }

  writeManifest(manifest);
  writeLockfile(lockfile);
  console.log("Done.");
}

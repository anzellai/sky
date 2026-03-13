// src/ffi/resolve-module.ts
// Resolve npm package entry points and declaration roots for Sky FFI.

import fs from "fs";
import path from "path";
import { getRequire } from "../utils/path.js";

const require = getRequire(import.meta.url);

export interface ResolvedPackage {
  readonly packageName: string;
  readonly packageJsonPath: string;
  readonly packageRoot: string;
  readonly runtimeEntryPath: string;
  readonly declaredTypesPath?: string;
}

export interface ResolvePackageResult {
  readonly resolved?: ResolvedPackage;
  readonly diagnostics: readonly string[];
}

export function resolveForeignPackage(packageName: string): ResolvePackageResult {
  const diagnostics: string[] = [];
  const projectRequire = getRequire(path.join(process.cwd(), "index.js"));

  let runtimeEntryPath: string;
  try {
    runtimeEntryPath = projectRequire.resolve(packageName);
  } catch {
    diagnostics.push(`Could not resolve npm package "${packageName}".`);
    return { diagnostics };
  }

  const packageRoot = findPackageRoot(runtimeEntryPath);
  if (!packageRoot) {
    diagnostics.push(`Could not locate package root for "${packageName}".`);
    return { diagnostics };
  }

  const packageJsonPath = path.join(packageRoot, "package.json");
  if (!fs.existsSync(packageJsonPath)) {
    diagnostics.push(`Missing package.json for "${packageName}" at ${packageRoot}.`);
    return { diagnostics };
  }

  let declaredTypesPath: string | undefined;
  try {
    declaredTypesPath = resolveDeclaredTypes(packageRoot, packageJsonPath, projectRequire);
  } catch (error) {
    diagnostics.push(
      error instanceof Error ? error.message : `Failed to inspect types for "${packageName}".`,
    );
  }

  return {
    resolved: {
      packageName,
      packageJsonPath,
      packageRoot,
      runtimeEntryPath,
      declaredTypesPath,
    },
    diagnostics,
  };
}

function findPackageRoot(startPath: string): string | undefined {
  let current = fs.statSync(startPath).isDirectory() ? startPath : path.dirname(startPath);

  while (true) {
    const packageJsonPath = path.join(current, "package.json");
    if (fs.existsSync(packageJsonPath)) {
      return current;
    }

    const parent = path.dirname(current);
    if (parent === current) {
      return undefined;
    }

    current = parent;
  }
}

function resolveDeclaredTypes(packageRoot: string, packageJsonPath: string, projectRequire: NodeRequire): string | undefined {
  const raw = fs.readFileSync(packageJsonPath, "utf8");
  const pkg = JSON.parse(raw) as {
    types?: string;
    typings?: string;
    exports?: unknown;
    name?: string;
  };

  const directTypes =
    typeof pkg.types === "string"
      ? pkg.types
      : typeof pkg.typings === "string"
        ? pkg.typings
        : undefined;

  if (directTypes) {
    const candidate = path.resolve(packageRoot, directTypes);
    if (fs.existsSync(candidate)) {
      return candidate;
    }
  }

  const indexDts = path.join(packageRoot, "index.d.ts");
  if (fs.existsSync(indexDts)) {
    return indexDts;
  }

  const definitelyTyped = resolveDefinitelyTyped(pkg.name ?? path.basename(packageRoot), projectRequire);
  if (definitelyTyped) {
    return definitelyTyped;
  }

  return undefined;
}

function resolveDefinitelyTyped(packageName: string, projectRequire: NodeRequire): string | undefined {
  const normalized =
    packageName.startsWith("@")
      ? packageName.slice(1).replace("/", "__")
      : packageName;

  const typesPackage = `@types/${normalized}`;

  try {
    const typesEntry = projectRequire.resolve(typesPackage + "/package.json");
    const typesRoot = path.dirname(typesEntry);

    const indexDts = path.join(typesRoot, "index.d.ts");
    if (fs.existsSync(indexDts)) {
      return indexDts;
    }

    return undefined;
  } catch {
    return undefined;
  }
}
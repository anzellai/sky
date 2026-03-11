// src/ffi/resolve-module.ts
// Resolve npm package entry points and declaration roots for Sky FFI.

import fs from "fs";
import path from "path";
import { createRequire } from "module";

const require = createRequire(import.meta.url);

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

  let runtimeEntryPath: string;
  try {
    runtimeEntryPath = require.resolve(packageName);
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
    declaredTypesPath = resolveDeclaredTypes(packageRoot, packageJsonPath);
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

function resolveDeclaredTypes(packageRoot: string, packageJsonPath: string): string | undefined {
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

  const definitelyTyped = resolveDefinitelyTyped(pkg.name ?? path.basename(packageRoot));
  if (definitelyTyped) {
    return definitelyTyped;
  }

  return undefined;
}

function resolveDefinitelyTyped(packageName: string): string | undefined {
  const normalized =
    packageName.startsWith("@")
      ? packageName.slice(1).replace("/", "__")
      : packageName;

  const typesPackage = `@types/${normalized}`;

  try {
    const typesEntry = require.resolve(typesPackage);
    const packageRoot = findPackageRoot(typesEntry);
    if (!packageRoot) return undefined;

    const indexDts = path.join(packageRoot, "index.d.ts");
    if (fs.existsSync(indexDts)) {
      return indexDts;
    }

    return typesEntry.endsWith(".d.ts") ? typesEntry : undefined;
  } catch {
    return undefined;
  }
}
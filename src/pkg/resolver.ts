import { SkyManifest } from "./manifest.js";

// Basic version resolution and SAT solving for dependencies
// In a real package manager, this would perform a topological sort and version satisfiability checks

export interface ResolvedDependency {
  name: string;
  version: string;
  isGo: boolean;
}

export function resolveDependencies(manifest: SkyManifest): ResolvedDependency[] {
  const resolved: ResolvedDependency[] = [];

  // Currently simply taking exactly what is in the manifest.
  // Real implementation: traverse tree, fetch manifests, solve ranges.
  if (manifest.dependencies) {
    for (const [pkg, version] of Object.entries(manifest.dependencies)) {
      resolved.push({ name: pkg, version, isGo: false });
    }
  }

  if (manifest.go?.dependencies) {
    for (const [pkg, version] of Object.entries(manifest.go.dependencies)) {
      resolved.push({ name: pkg, version, isGo: true });
    }
  }

  return resolved;
}

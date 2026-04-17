import path from "path";
import { readManifest, SkyManifest } from "./manifest.js";

// Basic version resolution and SAT solving for dependencies
// In a real package manager, this would perform a topological sort and version satisfiability checks

export interface ResolvedDependency {
  name: string;
  version: string;
  isGo: boolean;
}

export function resolveDependencies(manifest: SkyManifest): ResolvedDependency[] {
  const resolved: ResolvedDependency[] = [];
  const visited = new Set<string>();

  function collectDeps(m: SkyManifest) {
    // Collect Sky dependencies and recurse into their manifests
    if (m.dependencies) {
      for (const [pkg, version] of Object.entries(m.dependencies)) {
        if (visited.has(pkg)) continue;
        visited.add(pkg);
        resolved.push({ name: pkg, version, isGo: false });

        // Read transitive manifest from .skydeps
        const depManifest = readManifest(path.join(".skydeps", pkg, "sky.toml"));
        if (depManifest) collectDeps(depManifest);
      }
    }

    // Collect Go dependencies
    if (m.go?.dependencies) {
      for (const [pkg, version] of Object.entries(m.go.dependencies)) {
        if (visited.has(pkg)) continue;
        visited.add(pkg);
        resolved.push({ name: pkg, version, isGo: true });
      }
    }
  }

  collectDeps(manifest);
  return resolved;
}

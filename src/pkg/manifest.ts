import fs from "fs";
import * as toml from "smol-toml";

export interface SkyManifest {
  name: string;
  version: string;
  source?: {
    root?: string;
  };
  dependencies?: Record<string, string>;
  go?: {
    dependencies?: Record<string, string>;
  };
}

export function readManifest(path = "sky.toml"): SkyManifest | null {
  if (!fs.existsSync(path)) return null;
  try {
    const content = fs.readFileSync(path, "utf8");
    const parsed = toml.parse(content) as any;
    
    return {
      name: parsed.name || "unknown",
      version: parsed.version || "0.0.0",
      source: parsed.source,
      dependencies: parsed.dependencies || {},
      go: parsed.go || { dependencies: parsed["go.dependencies"] || {} },
    };
  } catch (e) {
    console.error(`Failed to parse ${path}`, e);
    return null;
  }
}

export function writeManifest(manifest: SkyManifest, path = "sky.toml") {
  const out: any = {
    name: manifest.name,
    version: manifest.version,
  };
  if (manifest.source) out.source = manifest.source;
  if (manifest.dependencies && Object.keys(manifest.dependencies).length > 0) {
    out.dependencies = manifest.dependencies;
  }
  if (manifest.go?.dependencies && Object.keys(manifest.go.dependencies).length > 0) {
    out["go.dependencies"] = manifest.go.dependencies;
  }
  
  fs.writeFileSync(path, toml.stringify(out));
}

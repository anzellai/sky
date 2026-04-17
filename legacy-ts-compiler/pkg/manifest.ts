import fs from "fs";
import * as toml from "smol-toml";

export interface SkyManifest {
  name: string;
  version: string;
  entry?: string;
  bin?: string;
  source?: {
    root?: string;
  };
  lib?: {
    exposing?: string[];
  };
  dependencies?: Record<string, string>;
  go?: {
    dependencies?: Record<string, string>;
  };
  live?: {
    port?: number;
    ttl?: string;
    session?: {
      store?: string;
      path?: string;
      url?: string;
      snapshot_interval?: number;
    };
    static?: {
      dir?: string;
    };
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
      entry: parsed.entry || undefined,
      bin: parsed.bin || undefined,
      source: parsed.source,
      lib: parsed.lib || undefined,
      dependencies: parsed.dependencies || {},
      go: parsed.go || { dependencies: parsed["go.dependencies"] || {} },
      live: parsed.live || undefined,
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
  if (manifest.entry) out.entry = manifest.entry;
  if (manifest.bin) out.bin = manifest.bin;
  if (manifest.source) out.source = manifest.source;
  if (manifest.lib) out.lib = manifest.lib;
  if (manifest.dependencies && Object.keys(manifest.dependencies).length > 0) {
    out.dependencies = manifest.dependencies;
  }
  if (manifest.go?.dependencies && Object.keys(manifest.go.dependencies).length > 0) {
    out["go.dependencies"] = manifest.go.dependencies;
  }

  fs.writeFileSync(path, toml.stringify(out));
}

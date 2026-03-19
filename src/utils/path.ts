import path from 'path';
import { fileURLToPath } from 'url';
import { createRequire } from 'module';

/**
 * Gets the equivalent of __dirname in a cross-platform, cross-module-system way.
 */
export function getDirname(importMetaUrl: string): string {
  try {
    return path.dirname(fileURLToPath(importMetaUrl));
  } catch {
    // Fallback for CommonJS/Bundled environments
    return typeof __dirname !== 'undefined' ? __dirname : process.cwd();
  }
}

/**
 * Gets the equivalent of __filename in a cross-platform, cross-module-system way.
 */
export function getFilename(importMetaUrl: string): string {
  try {
    return fileURLToPath(importMetaUrl);
  } catch {
    // Fallback for CommonJS/Bundled environments
    return typeof __filename !== 'undefined' ? __filename : '';
  }
}

/**
 * Gets a require function for the given URL.
 */
/**
 * Convert a PascalCase Sky identifier to a kebab-case Go path segment.
 * e.g. "FyneIo" -> "fyne-io", "GoText" -> "go-text", "Google" -> "google"
 */
export function pascalToKebab(s: string): string {
  return s.replace(/([a-z0-9])([A-Z])/g, "$1-$2").toLowerCase();
}

/**
 * Convert Sky import parts to possible Go package paths.
 * Handles domain.tld patterns and kebab-case path segments.
 * e.g. ["Github", "Com", "FyneIo", "Fyne"] ->
 *   ["github.com/fyne-io/fyne", "github.com/fyneio/fyne", ...]
 */
export function skyImportToGoPaths(importParts: readonly string[]): string[] {
  const kebabParts = importParts.map(pascalToKebab);
  const lowerParts = importParts.map(p => p.toLowerCase());

  const paths: string[] = [];

  // With domain.tld pattern (first two parts joined by dot)
  if (importParts.length >= 2) {
    const kebabPath = kebabParts[0] + "." + kebabParts[1] + "/" + kebabParts.slice(2).join("/");
    const lowerPath = lowerParts[0] + "." + lowerParts[1] + "/" + lowerParts.slice(2).join("/");
    paths.push(kebabPath);
    if (lowerPath !== kebabPath) paths.push(lowerPath);
  }

  // Plain join
  const kebabPlain = kebabParts.join("/");
  const lowerPlain = lowerParts.join("/");
  paths.push(kebabPlain);
  if (lowerPlain !== kebabPlain) paths.push(lowerPlain);

  return paths;
}

export function getRequire(importMetaUrl: string): NodeRequire {
  try {
    return createRequire(importMetaUrl);
  } catch {
    // Fallback for CommonJS/Bundled environments where require is global
    return typeof require !== 'undefined' ? require : createRequire(path.join(process.cwd(), 'index.js'));
  }
}

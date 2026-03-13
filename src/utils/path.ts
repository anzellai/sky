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
export function getRequire(importMetaUrl: string): NodeRequire {
  try {
    return createRequire(importMetaUrl);
  } catch {
    // Fallback for CommonJS/Bundled environments where require is global
    return typeof require !== 'undefined' ? require : createRequire(path.join(process.cwd(), 'index.js'));
  }
}

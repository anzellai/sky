// Future registry implementation for `sky publish` and advanced resolution

export function resolveRegistryPackage(pkgName: string, version: string) {
  // Prototype falls back to GitHub directly
  return `https://github.com/${pkgName}.git`;
}

export function publishPackage(pkgName: string) {
  throw new Error("Publishing is not yet supported in the prototype.");
}

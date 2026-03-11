// src/ffi/generate-bindings.ts
// Generate Sky FFI binding metadata from resolved npm packages.

import { resolveForeignPackage } from "./resolve-module.js";
import { extractForeignExports } from "./extract-exports.js";
import { convertFunctionSignature } from "./convert-types.js";
import { npmNameToSkyModule } from "./npm-name.js";

export function foreignPackageToModule(pkg: string): string {
  return `Sky.FFI.${npmNameToSkyModule(pkg)}`;
}

export interface GeneratedForeignValueBinding {
  readonly skyName: string;
  readonly jsName: string;
  readonly sourceModule: string;
  readonly skyType?: string;
}

export interface GeneratedForeignTypeBinding {
  readonly skyName: string;
  readonly sourceModule: string;
  readonly kind: "interface" | "typeAlias" | "class" | "enum";
}

export interface GeneratedForeignBindings {
  readonly packageName: string;
  readonly skyModuleName: string;

  readonly runtimeEntryPath: string;
  readonly declaredTypesPath?: string;

  readonly values: readonly GeneratedForeignValueBinding[];
  readonly types: readonly GeneratedForeignTypeBinding[];
}

export interface GenerateBindingsResult {
  readonly generated?: GeneratedForeignBindings;
  readonly diagnostics: readonly string[];
}

export async function generateForeignBindings(
  packageName: string,
  requestedExports: readonly string[],
): Promise<GenerateBindingsResult> {

  const diagnostics: string[] = [];

  const skyModuleName =
    foreignPackageToModule(packageName);

  const resolvedResult =
    resolveForeignPackage(packageName);

  diagnostics.push(...resolvedResult.diagnostics);

  if (!resolvedResult.resolved) {
    return { diagnostics };
  }

  const extractedResult =
    await extractForeignExports(resolvedResult.resolved);

  diagnostics.push(...extractedResult.diagnostics);

  if (!extractedResult.extracted) {
    return { diagnostics };
  }

  const runtimeExportSet =
    new Set(extractedResult.extracted.runtimeExports);

  const functionMap =
    new Map(
      extractedResult.extracted.functions
        .map(fn => [fn.name, fn] as const)
    );

  const typeMap =
    new Map(
      extractedResult.extracted.types
        .map(ty => [ty.name, ty] as const)
    );

  const values: GeneratedForeignValueBinding[] = [];
  const types: GeneratedForeignTypeBinding[] = [];

  const exportsToProcess = requestedExports.length > 0 
    ? requestedExports 
    : [...runtimeExportSet, ...typeMap.keys()];

  for (const requestedName of exportsToProcess) {

    const existsAtRuntime =
      runtimeExportSet.has(requestedName);

    const functionInfo =
      functionMap.get(requestedName);

    const typeInfo =
      typeMap.get(requestedName);

    if (!existsAtRuntime && !functionInfo && !typeInfo) {

      diagnostics.push(
        `Foreign export "${requestedName}" was requested from "${packageName}" but was not found.`,
      );

      continue;
    }

    // Pure TypeScript type
    if (typeInfo && !existsAtRuntime) {

      types.push({
        skyName: requestedName,
        sourceModule: skyModuleName,
        kind: typeInfo.kind,
      });

      continue;
    }

    let skyType: string | undefined;

    if (functionInfo) {

      const converted =
        convertFunctionSignature(
          requestedName,
          functionInfo.signatureText,
        );

      diagnostics.push(...converted.diagnostics);

      skyType = converted.converted?.skyType;

    }

    values.push({
      skyName: requestedName,
      jsName: requestedName,
      sourceModule: skyModuleName,
      skyType,
    });

  }

  values.sort((a, b) =>
    a.skyName.localeCompare(b.skyName)
  );

  types.sort((a, b) =>
    a.skyName.localeCompare(b.skyName)
  );

  return {

    generated: {

      packageName,
      skyModuleName,

      runtimeEntryPath:
        resolvedResult.resolved.runtimeEntryPath,

      declaredTypesPath:
        resolvedResult.resolved.declaredTypesPath,

      values,
      types,

    },

    diagnostics,

  };

}

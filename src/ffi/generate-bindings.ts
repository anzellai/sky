// src/ffi/generate-bindings.ts
// Generate Sky FFI binding metadata from resolved npm packages.

import { resolveForeignPackage } from "./resolve-module.js";
import { extractForeignExports } from "./extract-exports.js";
import { convertFunctionSignature } from "./convert-types.js";
import { npmNameToSkyModule } from "./npm-name.js";

export function foreignPackageToModule(pkg: string): string {
  return `Sky.FFI.${npmNameToSkyModule(pkg)}`;
}

export interface GeneratedForeignParameter {
  readonly name: string;
  readonly isCallback: boolean;
  readonly callbackArity: number;
}

export interface GeneratedForeignValueBinding {
  readonly skyName: string;
  readonly jsName: string;
  readonly sourceModule: string;
  readonly skyType?: string;
  readonly parameters?: readonly GeneratedForeignParameter[];
  readonly isAsync?: boolean;
  readonly methodOf?: string;
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
        .filter(fn => /^[a-zA-Z_][a-zA-Z0-9_]*$/.test(fn.name))
        .map(fn => {
          const key = fn.methodOf ? `${fn.methodOf.toLowerCase()}_${fn.name}` : fn.name;
          return [key, fn] as const;
        })
    );

  const typeMap =
    new Map(
      extractedResult.extracted.types
        .map(ty => [ty.name, ty] as const)
    );

  const values: GeneratedForeignValueBinding[] = [];
  const types: GeneratedForeignTypeBinding[] = [];

  const reservedWords = new Set(["break", "case", "catch", "class", "const", "continue", "debugger", "default", "delete", "do", "else", "export", "extends", "finally", "for", "function", "if", "import", "in", "instanceof", "new", "return", "super", "switch", "this", "throw", "try", "typeof", "var", "void", "while", "with", "yield", "enum", "implements", "interface", "let", "package", "private", "protected", "public", "static", "await"]);

  const rawExportsToProcess = requestedExports.length > 0 
    ? requestedExports 
    : Array.from(new Set([...runtimeExportSet, ...typeMap.keys(), ...functionMap.keys()]));

  const exportsToProcess = rawExportsToProcess.filter(name => 
    /^[a-zA-Z_][a-zA-Z0-9_]*$/.test(name) && !reservedWords.has(name)
  );

  for (const requestedName of exportsToProcess) {

    // Methods don't strictly exist "at runtime" in the same way as top-level module exports,
    // so we treat them as existing if they are in the functionMap
    const functionInfo = functionMap.get(requestedName);
    const existsAtRuntime = runtimeExportSet.has(requestedName) || !!functionInfo?.methodOf;

    const typeInfo = typeMap.get(requestedName);

    if (!existsAtRuntime && !functionInfo && !typeInfo) {
      // Skip if not found, instead of failing
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
      
      if (skyType && functionInfo.methodOf) {
        skyType = `Foreign -> ${skyType}`;
      }

    }

    values.push({
      skyName: requestedName,
      jsName: functionInfo ? functionInfo.name : requestedName,
      sourceModule: skyModuleName,
      skyType,
      parameters: functionInfo?.parameters,
      methodOf: functionInfo?.methodOf,
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

// src/ffi/extract-exports.ts
// Extract named runtime exports and TypeScript declaration symbols for Sky FFI.

import fs from "fs";
import path from "path";
import ts from "typescript";
import type { ResolvedPackage } from "./resolve-module.js";

export interface ExtractedForeignParameter {
  readonly name: string;
  readonly isCallback: boolean;
  readonly callbackArity: number;
}

export interface ExtractedForeignFunction {
  readonly name: string;
  readonly signatureText: string;
  readonly declarationFile?: string;
  readonly parameters: readonly ExtractedForeignParameter[];
  readonly methodOf?: string;
}

export interface ExtractedForeignType {
  readonly name: string;
  readonly kind: "interface" | "typeAlias" | "class" | "enum";
  readonly declarationFile?: string;
}

export interface ExtractedForeignExports {
  readonly runtimeExports: readonly string[];
  readonly functions: readonly ExtractedForeignFunction[];
  readonly types: readonly ExtractedForeignType[];
}

export interface ExtractExportsResult {
  readonly extracted?: ExtractedForeignExports;
  readonly diagnostics: readonly string[];
}

export async function extractForeignExports(
  resolved: ResolvedPackage,
): Promise<ExtractExportsResult> {
  const diagnostics: string[] = [];

  const runtimeExports = await extractRuntimeExports(resolved, diagnostics);
  const dtsInfo = extractDeclarationExports(resolved, diagnostics);

  return {
    extracted: {
      runtimeExports,
      functions: dtsInfo.functions,
      types: dtsInfo.types,
    },
    diagnostics,
  };
}

async function extractRuntimeExports(
  resolved: ResolvedPackage,
  diagnostics: string[],
): Promise<readonly string[]> {
  try {
    const moduleUrl = pathToFileUrl(resolved.runtimeEntryPath);
    const mod = await import(moduleUrl);

    return Object.keys(mod)
      .filter((key) => key !== "default")
      .sort();
  } catch (error) {
    diagnostics.push(
      error instanceof Error
        ? `Failed to inspect runtime exports for "${resolved.packageName}": ${error.message}`
        : `Failed to inspect runtime exports for "${resolved.packageName}".`,
    );

    return [];
  }
}

function extractDeclarationExports(
  resolved: ResolvedPackage,
  diagnostics: string[],
): {
  readonly functions: readonly ExtractedForeignFunction[];
  readonly types: readonly ExtractedForeignType[];
} {
  if (!resolved.declaredTypesPath) {
    return {
      functions: [],
      types: [],
    };
  }

  if (!fs.existsSync(resolved.declaredTypesPath)) {
    diagnostics.push(
      `Declaration file not found for "${resolved.packageName}": ${resolved.declaredTypesPath}`,
    );
    return {
      functions: [],
      types: [],
    };
  }

  const program = ts.createProgram([resolved.declaredTypesPath], {
    allowJs: false,
    declaration: false,
    emitDeclarationOnly: false,
    noEmit: true,
    skipLibCheck: true,
    target: ts.ScriptTarget.ES2022,
    module: ts.ModuleKind.ES2022,
    moduleResolution: ts.ModuleResolutionKind.Bundler,
  });

  const checker = program.getTypeChecker();
  const sourceFile = program.getSourceFile(resolved.declaredTypesPath);

  if (!sourceFile) {
    diagnostics.push(
      `Could not load declaration source for "${resolved.packageName}": ${resolved.declaredTypesPath}`,
    );
    return {
      functions: [],
      types: [],
    };
  }

  const moduleSymbol = checker.getSymbolAtLocation(sourceFile);
  if (!moduleSymbol) {
    diagnostics.push(
      `Could not read declaration symbols for "${resolved.packageName}" from ${resolved.declaredTypesPath}`,
    );
    return {
      functions: [],
      types: [],
    };
  }

  const exportedSymbols = Array.from(checker.getExportsOfModule(moduleSymbol));

  if (moduleSymbol.exports) {
    const exportEq = moduleSymbol.exports.get(ts.escapeLeadingUnderscores("export="));
    if (exportEq && !exportedSymbols.includes(exportEq)) {
      exportedSymbols.push(exportEq);
    }
    const defaultExport = moduleSymbol.exports.get(ts.escapeLeadingUnderscores("default"));
    if (defaultExport && !exportedSymbols.includes(defaultExport)) {
      exportedSymbols.push(defaultExport);
    }
  }

  const functions: ExtractedForeignFunction[] = [];
  const types: ExtractedForeignType[] = [];

  for (const symbol of exportedSymbols) {
    let symbolName = symbol.getName();

    if (symbolName === "default" || symbolName === "export=") {
      // Clean up package name to be a valid identifier
      symbolName = resolved.packageName.replace(/[^a-zA-Z0-9]/g, "");
    }

    const declarations = symbol.getDeclarations() ?? [];
    const declaration = declarations[0];
    const declarationFile = declaration?.getSourceFile().fileName;

    if (isTypeLikeDeclaration(declaration)) {
      types.push({
        name: symbolName,
        kind: getTypeKind(declaration),
        declarationFile,
      });

      // Attempt to extract methods from this interface/class
      const type = checker.getDeclaredTypeOfSymbol(symbol);
      if (type && type.isClassOrInterface()) {
        const props = type.getApparentProperties();
        for (const prop of props) {
          const propName = prop.getName();
          if (propName === "apply" || propName === "bind" || propName === "call" || propName === "toString") {
            continue;
          }

          const propDecl = prop.valueDeclaration || prop.getDeclarations()?.[0];
          const propType = checker.getTypeOfSymbolAtLocation(prop, propDecl || declaration);
          const callSigs = propType.getCallSignatures();
          
          if (callSigs.length > 0) {
            const signature = callSigs.reduce((max, current) => current.parameters.length > max.parameters.length ? current : max, callSigs[0]);
            const parameters = signature.parameters.map((p) => {
              let isCallback = false;
              let callbackArity = 0;
              const pDecl = p.valueDeclaration;
              if (pDecl) {
                let paramType = checker.getTypeOfSymbolAtLocation(p, pDecl);
                
                // If it's a variadic array (e.g. ...handlers: RequestHandler[]), check its element type
                if (checker.isArrayType(paramType)) {
                  const typeArguments = checker.getTypeArguments(paramType as ts.TypeReference);
                  if (typeArguments && typeArguments.length > 0) {
                    paramType = typeArguments[0];
                  }
                }

                const pCallSigs = paramType.getCallSignatures();
                if (pCallSigs.length > 0) {
                  isCallback = true;
                  callbackArity = pCallSigs[0].parameters.length;
                }
              }
              return { name: p.name, isCallback, callbackArity };
            });

            functions.push({
              name: prop.getName(),
              methodOf: symbolName,
              signatureText: checker.signatureToString(
                signature,
                propDecl,
                ts.TypeFormatFlags.NoTruncation | ts.TypeFormatFlags.WriteArrowStyleSignature
              ),
              declarationFile,
              parameters,
            });
          }
        }
      }
      continue;
    }

    const type = checker.getTypeOfSymbolAtLocation(symbol, declaration ?? sourceFile);
    const signatures = checker.getSignaturesOfType(type, ts.SignatureKind.Call);

    if (signatures.length > 0) {
      const signature = signatures.reduce((max, current) => current.parameters.length > max.parameters.length ? current : max, signatures[0]);

      const parameters = signature.parameters.map((p) => {
        let isCallback = false;
        let callbackArity = 0;

        const pDecl = p.valueDeclaration;
        if (pDecl) {
          let paramType = checker.getTypeOfSymbolAtLocation(p, pDecl);

          if (checker.isArrayType(paramType)) {
            const typeArguments = checker.getTypeArguments(paramType as ts.TypeReference);
            if (typeArguments && typeArguments.length > 0) {
              paramType = typeArguments[0];
            }
          }

          const callSigs = paramType.getCallSignatures();
          if (callSigs.length > 0) {
            isCallback = true;
            callbackArity = callSigs[0].parameters.length;
          }
        }

        return {
          name: p.name,
          isCallback,
          callbackArity,
        };
      });

      functions.push({
        name: symbolName,
        signatureText: checker.signatureToString(
          signature,
          declaration,
          ts.TypeFormatFlags.NoTruncation |
            ts.TypeFormatFlags.WriteArrowStyleSignature |
            ts.TypeFormatFlags.UseFullyQualifiedType,
        ),
        declarationFile,
        parameters,
      });
    }
  }

  functions.sort((a, b) => a.name.localeCompare(b.name));
  types.sort((a, b) => a.name.localeCompare(b.name));

  return { functions, types };
}

function isTypeLikeDeclaration(
  declaration: ts.Declaration | undefined,
): declaration is
  | ts.InterfaceDeclaration
  | ts.TypeAliasDeclaration
  | ts.ClassDeclaration
  | ts.EnumDeclaration {
  if (!declaration) {
    return false;
  }

  return (
    ts.isInterfaceDeclaration(declaration) ||
    ts.isTypeAliasDeclaration(declaration) ||
    ts.isClassDeclaration(declaration) ||
    ts.isEnumDeclaration(declaration)
  );
}

function getTypeKind(
  declaration:
    | ts.InterfaceDeclaration
    | ts.TypeAliasDeclaration
    | ts.ClassDeclaration
    | ts.EnumDeclaration,
): "interface" | "typeAlias" | "class" | "enum" {
  if (ts.isInterfaceDeclaration(declaration)) {
    return "interface";
  }

  if (ts.isTypeAliasDeclaration(declaration)) {
    return "typeAlias";
  }

  if (ts.isClassDeclaration(declaration)) {
    return "class";
  }

  return "enum";
}

function pathToFileUrl(filePath: string): string {
  const resolved = path.resolve(filePath);
  const normalized = resolved.replace(/\\/g, "/");
  return normalized.startsWith("/")
    ? `file://${normalized}`
    : `file:///${normalized}`;
}
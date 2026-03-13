export interface GeneratedForeignBindings {
  packageName: string;
  skyModuleName: string;
  runtimeEntryPath: string;
  values: { skyName: string; jsName: string; sourceModule: string; skyType: string; }[];
  types: { skyName: string; jsName: string; sourceModule: string; typeParams: string[]; }[];
}

export async function generateForeignBindings(packageName: string, requestedNames: string[]): Promise<{ generated?: GeneratedForeignBindings, diagnostics: string[] }> {
  return {
    generated: {
      packageName,
      skyModuleName: `Sky.FFI.${packageName.replace(/\//g, ".")}`,
      runtimeEntryPath: packageName,
      values: requestedNames.map(name => ({
        skyName: name,
        jsName: name,
        sourceModule: packageName,
        skyType: "Foreign"
      })),
      types: []
    },
    diagnostics: []
  };
}

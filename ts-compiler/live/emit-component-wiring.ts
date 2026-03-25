// src/live/emit-component-wiring.ts
// Generates Go code for component auto-wiring:
// - Update forwarding cases for unhandled component messages
// - Msg decoder entries for component wrapper variants
// - Sub-message resolver functions per component
// - Required Go import paths

import { ComponentBinding } from "./detect-components.js";

interface ComponentMsgVariant {
  name: string;
  arity: number;
}

export interface ComponentModuleInfo {
  binding: ComponentBinding;
  goImportPath: string;       // e.g., "sky-out/Counter"
  goImportAlias: string;      // e.g., "sky_counter"
  msgVariants: ComponentMsgVariant[]; // The component's Msg type variants
}

/**
 * Build ComponentModuleInfo from bindings and module graph data.
 */
export function buildComponentInfos(
  bindings: ComponentBinding[],
  moduleGraph: { moduleAst: any; filePath: string }[],
  outDir: string
): ComponentModuleInfo[] {
  const infos: ComponentModuleInfo[] = [];

  for (const binding of bindings) {
    if (binding.hasExplicitCase) continue; // Skip explicitly handled components

    // Find the component module in the graph
    const componentModule = moduleGraph.find(m => {
      const name = m.moduleAst.name;
      const lastPart = name[name.length - 1];
      return lastPart === binding.moduleName;
    });

    if (!componentModule) continue;

    // Extract Msg variants from the component module
    const msgVariants: ComponentMsgVariant[] = [];
    for (const decl of componentModule.moduleAst.declarations) {
      if (decl.kind === "TypeDeclaration" && decl.name === "Msg") {
        if (decl.variants) {
          for (const v of decl.variants) {
            msgVariants.push({
              name: v.name,
              arity: (v.fields || []).length,
            });
          }
        }
      }
    }

    // Compute Go import path and alias
    const modulePath = componentModule.moduleAst.name;
    const goImportPath = `sky-out/${modulePath.join("/")}`;
    const goImportAlias = `sky_${modulePath.join("_").toLowerCase()}`;

    infos.push({
      binding,
      goImportPath,
      goImportAlias,
      msgVariants,
    });
  }

  return infos;
}

/**
 * Generate the Go switch cases for component forwarding in the Update wrapper.
 * Returns Go code to insert inside the Update lambda, before the fallthrough.
 */
export function generateComponentUpdateCases(infos: ComponentModuleInfo[]): string {
  if (infos.length === 0) return "";

  let code = `\t\t\t// Component auto-wiring: forward unhandled component messages\n`;
  code += `\t\t\tif msgStruct, ok := msg.(Msg); ok {\n`;
  code += `\t\t\t\tswitch msgStruct.Tag {\n`;

  for (const info of infos) {
    const b = info.binding;
    code += `\t\t\t\tcase ${b.msgWrapperTag}: // ${b.msgWrapperName}\n`;
    code += `\t\t\t\t\tsubMsg := msgStruct.${b.msgWrapperName}Value\n`;
    code += `\t\t\t\t\tm := model.(map[string]any)\n`;
    code += `\t\t\t\t\tsubResult := ${info.goImportAlias}.Update(subMsg, m["${b.fieldName}"])\n`;
    code += `\t\t\t\t\tif t, ok := subResult.(sky_wrappers.Tuple2); ok {\n`;
    code += `\t\t\t\t\t\treturn sky_wrappers.UpdateRecord(model, map[string]any{"${b.fieldName}": t.V0}), nil\n`;
    code += `\t\t\t\t\t}\n`;
    code += `\t\t\t\t\treturn model, nil\n`;
  }

  code += `\t\t\t\t}\n`;
  code += `\t\t\t}\n`;
  return code;
}

/**
 * Generate sub-message resolver functions for each component.
 * These map event name strings to the component's Msg struct.
 */
export function generateComponentMsgResolvers(infos: ComponentModuleInfo[]): string {
  let code = "";
  for (const info of infos) {
    const funcName = `resolve${info.binding.moduleName}Msg`;
    code += `func ${funcName}(name string) any {\n`;
    code += `\tswitch name {\n`;
    for (let i = 0; i < info.msgVariants.length; i++) {
      const v = info.msgVariants[i];
      if (v.arity === 0) {
        code += `\tcase "${v.name}":\n`;
        code += `\t\treturn ${info.goImportAlias}.Msg{Tag: ${i}}\n`;
      }
      // V3: only zero-arg component sub-messages on the wire
    }
    code += `\t}\n\treturn nil\n}\n\n`;
  }
  return code;
}

/**
 * Generate decodeMsg case entries for component wrapper variants.
 * Uses the compound message format: "CounterMsg Increment" → splits and resolves.
 */
export function generateComponentMsgDecoderCases(infos: ComponentModuleInfo[]): string {
  let code = "";
  for (const info of infos) {
    const b = info.binding;
    const funcName = `resolve${info.binding.moduleName}Msg`;
    code += `\tcase "${b.msgWrapperName}":\n`;
    code += `\t\tif inlineResolvedArg != nil {\n`;
    code += `\t\t\t// Inline arg is a sub-message name string\n`;
    code += `\t\t\tif s, ok := inlineResolvedArg.(string); ok {\n`;
    code += `\t\t\t\tresolved := ${funcName}(s)\n`;
    code += `\t\t\t\tif resolved != nil {\n`;
    code += `\t\t\t\t\treturn Msg{Tag: ${b.msgWrapperTag}, ${b.msgWrapperName}Value: resolved}, nil\n`;
    code += `\t\t\t\t}\n`;
    code += `\t\t\t}\n`;
    code += `\t\t}\n`;
    code += `\t\t// Try from JSON args\n`;
    code += `\t\tif len(args) >= 1 {\n`;
    code += `\t\t\tvar subName string\n`;
    code += `\t\t\tjson.Unmarshal(args[0], &subName)\n`;
    code += `\t\t\tresolved := ${funcName}(subName)\n`;
    code += `\t\t\tif resolved != nil {\n`;
    code += `\t\t\t\treturn Msg{Tag: ${b.msgWrapperTag}, ${b.msgWrapperName}Value: resolved}, nil\n`;
    code += `\t\t\t}\n`;
    code += `\t\t}\n`;
    code += `\t\treturn nil, fmt.Errorf("unknown ${b.moduleName} sub-message")\n`;
  }
  return code;
}

/**
 * Get the Go import entries needed for component packages.
 */
export function getComponentImports(infos: ComponentModuleInfo[]): string[] {
  return infos.map(info => `${info.goImportAlias} "${info.goImportPath}"`);
}

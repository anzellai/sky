import { CompletionItem, CompletionItemKind, Position } from 'vscode-languageserver/node.js';
import * as AST from '../../ast/ast.js';
import { Workspace } from '../analysis/workspace.js';
import { formatType, Scheme } from '../../types/types.js';
import fs from 'fs';
import path from 'path';

export function getCompletions(workspace: Workspace, uri: string, position: Position): CompletionItem[] {
  const items: CompletionItem[] = [];
  const doc = workspace.getDocument(uri);

  if (!doc) {
    return addKeywords(items);
  }

  const lines = doc.source.split("\n");
  const currentLine = lines[position.line] || "";
  const textBeforeCursor = currentLine.substring(0, position.character);

  // Check if we're in an import context (line starts with "import" or we're typing after "import ")
  const importLineMatch = textBeforeCursor.match(/^\s*import\s+(.*)$/);
  if (importLineMatch) {
    return getImportCompletions(doc, importLineMatch[1], uri, items);
  }

  // Check if we're typing a qualified access like "Http." or "String."
  const qualifiedMatch = textBeforeCursor.match(/([A-Z][a-zA-Z0-9]*(?:\.[A-Z][a-zA-Z0-9]*)*)\.\s*([a-zA-Z0-9_]*)$/);
  if (qualifiedMatch) {
    const qualifier = qualifiedMatch[1];
    const prefix = qualifiedMatch[2].toLowerCase();
    return getQualifiedCompletions(doc, qualifier, prefix, items);
  }

  // Default: keywords + all environment names
  addKeywords(items);

  if (doc.env) {
      for (const [name, scheme] of doc.env.entries()) {
          // Hide underlying FFI wrappers
          if (name.includes("Sky_") || name.includes("sky_")) continue;
          // Hide fully qualified names in general completion (show unqualified only)
          if (name.includes(".")) continue;

          items.push(makeCompletionItem(name, scheme));
      }

      // Also add available module qualifiers (e.g., "Http", "String") for dot-access
      if (doc.ast) {
          const addedModules = new Set<string>();
          for (const imp of doc.ast.imports) {
              const moduleName = imp.moduleName.join(".");
              const displayName = imp.alias?.name || imp.moduleName[imp.moduleName.length - 1];

              if (!addedModules.has(displayName)) {
                  addedModules.add(displayName);
                  items.push({
                      label: displayName,
                      kind: CompletionItemKind.Module,
                      detail: moduleName,
                  });
              }
          }
      }
  } else if (doc.ast) {
      // Fallback if type checker hasn't run or failed
      for (const decl of doc.ast.declarations) {
        if (decl.kind === "FunctionDeclaration") {
          items.push({ label: decl.name, kind: CompletionItemKind.Function });
        } else if (decl.kind === "TypeDeclaration" || decl.kind === "TypeAliasDeclaration") {
          items.push({ label: decl.name, kind: CompletionItemKind.Class });
        } else if (decl.kind === "ForeignImportDeclaration") {
          items.push({ label: decl.name, kind: CompletionItemKind.Function });
        }
      }
  }

  return items;
}

function getQualifiedCompletions(
    doc: NonNullable<ReturnType<Workspace['getDocument']>>,
    qualifier: string,
    prefix: string,
    items: CompletionItem[]
): CompletionItem[] {
    const seen = new Set<string>();

    // Strategy 1: Use moduleExports to find exports for the module
    if (doc.moduleExports && doc.ast) {
        for (const imp of doc.ast.imports) {
            const moduleName = imp.moduleName.join(".");
            const alias = imp.alias?.name;
            const lastPart = imp.moduleName[imp.moduleName.length - 1];

            if (qualifier === moduleName || qualifier === alias || qualifier === lastPart) {
                const exports = doc.moduleExports.get(moduleName);
                if (exports && exports.size > 0) {
                    for (const [name, scheme] of exports) {
                        if (!prefix || name.toLowerCase().startsWith(prefix)) {
                            seen.add(name);
                            items.push(makeCompletionItem(name, scheme, qualifier));
                        }
                    }
                }
            }
        }
    }

    // Strategy 2: Also check env entries by qualifier prefix (catches items not in exports)
    if (doc.env) {
        // Try direct qualifier match
        const qualifierDot = qualifier + ".";
        for (const [name, scheme] of doc.env.entries()) {
            if (name.startsWith(qualifierDot)) {
                const memberName = name.substring(qualifierDot.length);
                if (memberName.includes(".")) continue;
                if (name.includes("Sky_") || name.includes("sky_")) continue;
                if (seen.has(memberName)) continue;
                if (!prefix || memberName.toLowerCase().startsWith(prefix)) {
                    seen.add(memberName);
                    items.push(makeCompletionItem(memberName, scheme, qualifier));
                }
            }
        }

        // Also try resolving aliases to full module names
        if (doc.ast) {
            for (const imp of doc.ast.imports) {
                const alias = imp.alias?.name;
                const lastPart = imp.moduleName[imp.moduleName.length - 1];
                // Match if qualifier is the alias OR the last part of the module name
                if (alias === qualifier || (lastPart === qualifier && !alias)) {
                    const fullQualifier = imp.moduleName.join(".") + ".";
                    for (const [name, scheme] of doc.env.entries()) {
                        if (name.startsWith(fullQualifier)) {
                            const memberName = name.substring(fullQualifier.length);
                            if (memberName.includes(".")) continue;
                            if (name.includes("Sky_") || name.includes("sky_")) continue;
                            if (seen.has(memberName)) continue;
                            if (!prefix || memberName.toLowerCase().startsWith(prefix)) {
                                seen.add(memberName);
                                items.push(makeCompletionItem(memberName, scheme, qualifier));
                            }
                        }
                    }
                }
            }
        }
    }

    return items;
}

function getImportCompletions(
    doc: NonNullable<ReturnType<Workspace['getDocument']>>,
    typed: string,
    uri: string,
    items: CompletionItem[]
): CompletionItem[] {
    const knownModules = new Set<string>();

    if (doc.moduleExports) {
        for (const moduleName of doc.moduleExports.keys()) {
            knownModules.add(moduleName);
        }
    }

    if (doc.modules) {
        for (const mod of doc.modules) {
            const name = mod.moduleAst.name.join(".");
            knownModules.add(name);
        }
    }

    // Add common stdlib modules
    const stdlibModules = [
        "Sky.Core.Prelude", "Sky.Core.Maybe", "Sky.Core.String",
        "Sky.Core.List", "Sky.Core.Result", "Sky.Core.Dict",
        "Sky.Core.Json", "Sky.Core.Json.Encode", "Sky.Core.Json.Decode",
        "Sky.Core.Json.Decode.Pipeline", "Sky.Core.Json.Pipeline",
        "Sky.Core.Debug",
        "Sky.Interop", "Std.Channel", "Std.Log",
        "Std.Cmd", "Std.Sub", "Std.Task", "Std.Program",
    ];
    for (const m of stdlibModules) {
        knownModules.add(m);
    }

    // Scan .skycache/go/ for available Go binding modules
    const projectRoot = findProjectRoot(uri);
    if (projectRoot) {
        const skycacheGoDir = path.join(projectRoot, ".skycache", "go");
        if (fs.existsSync(skycacheGoDir)) {
            scanSkycacheModules(skycacheGoDir, skycacheGoDir, knownModules);
        }
    }

    const prefix = typed.trim().toLowerCase();
    // Don't suggest already-imported modules
    const alreadyImported = new Set<string>();
    if (doc.ast) {
        for (const imp of doc.ast.imports) {
            alreadyImported.add(imp.moduleName.join("."));
        }
    }

    for (const moduleName of knownModules) {
        if (alreadyImported.has(moduleName)) continue;
        if (!prefix || moduleName.toLowerCase().startsWith(prefix) || moduleName.toLowerCase().includes(prefix)) {
            items.push({
                label: moduleName,
                kind: CompletionItemKind.Module,
                detail: "module",
                insertText: moduleName,
            });
        }
    }

    return items;
}

/**
 * Scan .skycache/go/ directories for bindings.skyi files and
 * convert Go package paths to Sky module names (PascalCase).
 */
function scanSkycacheModules(baseDir: string, currentDir: string, modules: Set<string>): void {
    try {
        const entries = fs.readdirSync(currentDir, { withFileTypes: true });
        for (const entry of entries) {
            if (entry.name === "wrappers" || entry.name === "inspector") continue;
            const fullPath = path.join(currentDir, entry.name);
            if (entry.isDirectory()) {
                // Check if this directory has a bindings.skyi
                const bindingsPath = path.join(fullPath, "bindings.skyi");
                if (fs.existsSync(bindingsPath)) {
                    const relativePath = path.relative(baseDir, fullPath);
                    const skyName = goPathToSkyModule(relativePath);
                    if (skyName) {
                        modules.add(skyName);
                    }
                }
                scanSkycacheModules(baseDir, fullPath, modules);
            }
        }
    } catch {
        // Ignore filesystem errors
    }
}

/**
 * Convert a Go package path like "net/http" or "github.com/gorilla/mux"
 * to a Sky module name like "Net.Http" or "Github.Com.Gorilla.Mux".
 */
function goPathToSkyModule(goPath: string): string | null {
    const parts = goPath.split(path.sep);
    // PascalCase each segment, converting dashes to camelCase boundaries.
    // e.g. "kanda-co" → "KandaCo", "ks-schema" → "KsSchema"
    const pascalCase = (s: string) =>
        s.split("-").map(w => w.charAt(0).toUpperCase() + w.slice(1)).join("");
    const skyParts = parts.map(part => {
        if (part.includes(".")) {
            return part.split(".").map(pascalCase).join(".");
        }
        return pascalCase(part);
    });
    return skyParts.join(".");
}

/**
 * Find the project root by walking up from the file URI to find a directory
 * containing src/ or .skycache/.
 */
function findProjectRoot(uri: string): string | null {
    let filePath = uri;
    if (filePath.startsWith('file://')) {
        filePath = decodeURIComponent(filePath.substring(7));
    }

    let dir = path.dirname(filePath);
    for (let i = 0; i < 10; i++) {
        if (fs.existsSync(path.join(dir, ".skycache")) || fs.existsSync(path.join(dir, "sky.json"))) {
            return dir;
        }
        // Check if "src" is a child — project root is the parent of src
        const parts = filePath.split(path.sep);
        const srcIndex = parts.lastIndexOf("src");
        if (srcIndex >= 0) {
            return parts.slice(0, srcIndex).join(path.sep);
        }
        const parent = path.dirname(dir);
        if (parent === dir) break;
        dir = parent;
    }
    return null;
}

function makeCompletionItem(name: string, scheme: Scheme, qualifier?: string): CompletionItem {
    let kind: CompletionItemKind = CompletionItemKind.Variable;
    if (scheme.type.kind === "TypeFunction") {
        kind = CompletionItemKind.Function;
    } else if (name[0] >= "A" && name[0] <= "Z") {
        kind = CompletionItemKind.Class;
    }

    let schemeType = formatType(scheme.type);
    if (scheme.quantified.length > 0) {
       const vars = scheme.quantified.map(id => `'t${id}`).join(" ");
       schemeType = `forall ${vars}. ${schemeType}`;
    }

    const item: CompletionItem = {
        label: name,
        kind,
        detail: schemeType,
    };

    // Set filterText so the editor matches "Os.getenv" against the full qualified text
    if (qualifier) {
        item.filterText = `${qualifier}.${name}`;
    }

    return item;
}

function addKeywords(items: CompletionItem[]): CompletionItem[] {
    const keywords = ['module', 'import', 'exposing', 'let', 'in', 'case', 'of', 'type', 'alias', 'foreign'];
    for (const kw of keywords) {
        items.push({ label: kw, kind: CompletionItemKind.Keyword });
    }
    return items;
}

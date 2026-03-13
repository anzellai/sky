import { CompletionItem, CompletionItemKind, Position } from 'vscode-languageserver/node.js';
import * as AST from '../../ast/ast.js';
import { Workspace } from '../analysis/workspace.js';
import fs from 'fs';
import path from 'path';

export function getCompletions(workspace: Workspace, uri: string, position: Position): CompletionItem[] {
  const items: CompletionItem[] = [];
  const doc = workspace.getDocument(uri);
  
  // Add keywords
  const keywords = ['module', 'import', 'exposing', 'let', 'in', 'case', 'of', 'type', 'alias', 'foreign'];
  for (const kw of keywords) {
    items.push({ label: kw, kind: CompletionItemKind.Keyword });
  }

  if (!doc || !doc.ast) return items;

  // Add local declarations
  for (const decl of doc.ast.declarations) {
    if (decl.kind === "FunctionDeclaration") {
      items.push({ label: decl.name, kind: CompletionItemKind.Function });
    } else if (decl.kind === "TypeDeclaration" || decl.kind === "TypeAliasDeclaration") {
      items.push({ label: decl.name, kind: CompletionItemKind.Class });
    } else if (decl.kind === "ForeignImportDeclaration") {
      items.push({ label: decl.name, kind: CompletionItemKind.Function });
    }
  }

  // Scan imported go packages from .skycache for autocompletion
  // Real implementation would properly resolve the module graph, this is a fast heuristic for the LSP
  try {
    for (const imp of doc.ast.imports) {
       const pkgName = imp.moduleName.join("/").toLowerCase();
       let projectRoot = process.cwd();
       if (uri.startsWith('file://')) {
          const fsPath = uri.replace('file://', '');
          projectRoot = path.dirname(fsPath);
          while (projectRoot !== '/' && !fs.existsSync(path.join(projectRoot, 'sky.toml'))) {
            projectRoot = path.dirname(projectRoot);
          }
          if (projectRoot === '/') projectRoot = process.cwd();
       }

       const skyiPath = path.join(projectRoot, ".skycache", "go", pkgName, "bindings.skyi");
       if (fs.existsSync(skyiPath)) {
          const content = fs.readFileSync(skyiPath, "utf8");
          // Match `Name : Signature` followed by foreign import
          const typeRegex = /([A-Za-z0-9_]+) : (.*?)\nforeign import/g;
          let match;
          while ((match = typeRegex.exec(content)) !== null) {
            const name = match[1];
            const sig = match[2].trim();
            const modulePrefix = imp.alias ? imp.alias.name : imp.moduleName.join(".");
            items.push({
              label: `${modulePrefix}.${name}`,
              kind: CompletionItemKind.Function,
              detail: sig,
              documentation: `Exported from ${pkgName}`
            });
            // Also add unqualified if exposed directly
            if (imp.exposing && imp.exposing.open) {
               items.push({
                 label: name,
                 kind: CompletionItemKind.Function,
                 detail: sig,
                 documentation: `Exported from ${pkgName}`
               });
            }
          }
       }
    }
  } catch(e) {}

  return items;
}

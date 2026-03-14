import { CompletionItem, CompletionItemKind, Position } from 'vscode-languageserver/node.js';
import * as AST from '../../ast/ast.js';
import { Workspace } from '../analysis/workspace.js';
import { formatType } from '../../types/types.js';

export function getCompletions(workspace: Workspace, uri: string, position: Position): CompletionItem[] {
  const items: CompletionItem[] = [];
  const doc = workspace.getDocument(uri);
  
  // Add keywords
  const keywords = ['module', 'import', 'exposing', 'let', 'in', 'case', 'of', 'type', 'alias', 'foreign'];
  for (const kw of keywords) {
    items.push({ label: kw, kind: CompletionItemKind.Keyword });
  }

  if (!doc) return items;

  if (doc.env) {
      for (const [name, scheme] of doc.env.entries()) {
          // Hide underlying FFI wrappers
          if (name.includes("Sky_") || name.includes("sky_")) continue;

          // Determine kind based on type
          let kind: CompletionItemKind = CompletionItemKind.Variable;
          if (scheme.type.kind === "TypeFunction") {
              kind = CompletionItemKind.Function;
          }
          
          let schemeType = formatType(scheme.type);
          if (scheme.quantified.length > 0) {
             const vars = scheme.quantified.map(id => `'t${id}`).join(" ");
             schemeType = `forall ${vars}. ${schemeType}`;
          }

          items.push({
              label: name,
              kind,
              detail: schemeType,
          });
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

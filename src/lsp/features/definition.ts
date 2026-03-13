import { Location, Position } from 'vscode-languageserver/node.js';
import * as AST from '../../ast/ast.js';
import { Workspace } from '../analysis/workspace.js';

export function getDefinition(workspace: Workspace, uri: string, position: Position): Location | null {
  const doc = workspace.getDocument(uri);
  if (!doc || !doc.ast) return null;

  const node = workspace.findNodeAtPosition(doc.ast, position);
  if (!node) return null;

  if (node.kind === "IdentifierExpression") {
    const name = (node as AST.IdentifierExpression).name;
    // Look for it in declarations
    const decl = doc.ast.declarations.find(d => 
      (d.kind === "FunctionDeclaration" && d.name === name) ||
      (d.kind === "TypeDeclaration" && d.name === name) ||
      (d.kind === "TypeAliasDeclaration" && d.name === name)
    );

    if (decl && decl.span) {
      return {
        uri: uri,
        range: {
          start: { line: decl.span.start.line - 1, character: decl.span.start.column - 1 },
          end: { line: decl.span.end.line - 1, character: decl.span.end.column - 1 }
        }
      };
    }
  }

  return null;
}
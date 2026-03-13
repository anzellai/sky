import { Hover, Position } from 'vscode-languageserver/node.js';
import * as AST from '../../ast/ast.js';
import { Workspace } from '../analysis/workspace.js';

export function getHover(workspace: Workspace, uri: string, position: Position): Hover | null {
  const doc = workspace.getDocument(uri);
  if (!doc || !doc.ast) return null;

  const node = workspace.findNodeAtPosition(doc.ast, position);
  if (!node) return null;

  if (node.kind === "IdentifierExpression" || node.kind === "QualifiedIdentifierExpression") {
    let name = "";
    if (node.kind === "IdentifierExpression") {
      name = (node as AST.IdentifierExpression).name;
    } else {
      name = (node as AST.QualifiedIdentifierExpression).name.parts.join(".");
    }

    if (name === "listenAndServe") {
      return {
        contents: {
          kind: 'markdown',
          value: [
            '```elm',
            'listenAndServe : String -> Handler -> Result Error Unit',
            '```',
            '---',
            'Start an HTTP server.'
          ].join('\n')
        }
      };
    }
    
    return {
      contents: {
        kind: 'markdown',
        value: [
          '```elm',
          `${name} : Any`,
          '```'
        ].join('\n')
      }
    };
  }

  return null;
}
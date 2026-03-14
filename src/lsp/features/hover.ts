import { Hover, Position } from 'vscode-languageserver/node.js';
import * as AST from '../../ast/ast.js';
import { Workspace } from '../analysis/workspace.js';
import { formatType } from '../../types/types.js';

export function getHover(workspace: Workspace, uri: string, position: Position): Hover | null {
  const doc = workspace.getDocument(uri);
  if (!doc || !doc.ast) return null;

  const node = workspace.findNodeAtPosition(doc.ast, position);
  if (!node) return null;

  if (node.kind === "IdentifierExpression" || node.kind === "QualifiedIdentifierExpression" || node.kind === "VariablePattern") {
    let name = "";
    if (node.kind === "IdentifierExpression") {
      name = (node as AST.IdentifierExpression).name;
    } else if (node.kind === "VariablePattern") {
      name = (node as AST.VariablePattern).name;
    } else {
      name = (node as AST.QualifiedIdentifierExpression).name.parts.join(".");
    }

    if (doc.env) {
        const scheme = doc.env.get(name);
        if (scheme) {
            let schemeType = formatType(scheme.type);
            if (scheme.quantified.length > 0) {
               // E.g. 'a 'b. a -> b -> a
               const vars = scheme.quantified.map(id => `'t${id}`).join(" ");
               schemeType = `forall ${vars}. ${schemeType}`;
            }

            return {
              contents: {
                kind: 'markdown',
                value: [
                  '```elm',
                  `${name} : ${schemeType}`,
                  '```'
                ].join('\n')
              }
            };
        }
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
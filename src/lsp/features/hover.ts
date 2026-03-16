import { Hover, Position } from 'vscode-languageserver/node.js';
import * as AST from '../../ast/ast.js';
import { Workspace } from '../analysis/workspace.js';
import { formatType, formatTypeNormalized } from '../../types/types.js';

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

    // 1. Try the type environment (top-level declarations + imports)
    if (doc.env) {
        const scheme = doc.env.get(name);
        if (scheme) {
            return {
              contents: {
                kind: 'markdown',
                value: [
                  '```elm',
                  `${name} : ${formatScheme(scheme)}`,
                  '```'
                ].join('\n')
              }
            };
        }
    }

    // 2. Try the node-level type map (local let-bindings, parameters, sub-expressions)
    if (doc.nodeTypes && node.span) {
        const key = `${node.span.start.line}:${node.span.start.column}`;
        const nodeType = doc.nodeTypes.get(key);
        if (nodeType) {
            return {
              contents: {
                kind: 'markdown',
                value: [
                  '```elm',
                  `${name} : ${formatTypeNormalized(nodeType)}`,
                  '```'
                ].join('\n')
              }
            };
        }
    }

    // 3. For qualified names, try moduleExports
    if (doc.moduleExports && name.includes(".")) {
        const parts = name.split(".");
        const memberName = parts[parts.length - 1];
        // Try to find the module by matching import aliases or full module names
        if (doc.ast) {
            for (const imp of doc.ast.imports) {
                const moduleName = imp.moduleName.join(".");
                const alias = imp.alias?.name;
                const qualifier = parts.slice(0, -1).join(".");

                if (qualifier === moduleName || qualifier === alias) {
                    const exports = doc.moduleExports.get(moduleName);
                    if (exports) {
                        const scheme = exports.get(memberName);
                        if (scheme) {
                            return {
                              contents: {
                                kind: 'markdown',
                                value: [
                                  '```elm',
                                  `${name} : ${formatScheme(scheme)}`,
                                  '```'
                                ].join('\n')
                              }
                            };
                        }
                    }
                }
            }
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

function formatScheme(scheme: { quantified: readonly number[]; type: import('../../types/types.js').Type }): string {
    return formatTypeNormalized(scheme.type);
}

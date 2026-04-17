import { Location, Position } from 'vscode-languageserver/node.js';
import * as AST from '../../ast/ast.js';
import { Workspace } from '../analysis/workspace.js';

export function getDefinition(workspace: Workspace, uri: string, position: Position): Location | null {
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

    // Is it qualified? If so, we need to find the specific module
    const parts = name.split(".");
    let targetName = name;
    
    // Look across all modules from type checking
    if (doc.modules) {
        for (const mod of doc.modules) {
            // Find declaration
            // If qualified (e.g. Http.get), the targetName is the last part
            // Or it could be in another module
            let searchName = parts[parts.length - 1];
            
            const decl = mod.moduleAst.declarations.find(d => 
              (d.kind === "FunctionDeclaration" && d.name === searchName) ||
              (d.kind === "TypeDeclaration" && d.name === searchName) ||
              (d.kind === "TypeAliasDeclaration" && d.name === searchName)
            );

            if (decl && decl.span) {
              return {
                uri: `file://${mod.filePath}`,
                range: {
                  start: { line: decl.span.start.line - 1, character: decl.span.start.column - 1 },
                  end: { line: decl.span.end.line - 1, character: decl.span.end.column - 1 }
                }
              };
            }
        }
    }

    // Fallback to local
    const decl = doc.ast.declarations.find(d => 
      (d.kind === "FunctionDeclaration" && d.name === targetName) ||
      (d.kind === "TypeDeclaration" && d.name === targetName) ||
      (d.kind === "TypeAliasDeclaration" && d.name === targetName)
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
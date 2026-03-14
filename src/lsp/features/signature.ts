import { SignatureHelp, Position, SignatureInformation } from 'vscode-languageserver/node.js';
import * as AST from '../../ast/ast.js';
import { Workspace } from '../analysis/workspace.js';
import { formatType } from '../../types/types.js';

export function getSignatureHelp(workspace: Workspace, uri: string, position: Position): SignatureHelp | null {
  const doc = workspace.getDocument(uri);
  if (!doc || !doc.ast) return null;

  const lines = doc.source.split("\n");
  const currentLine = lines[position.line];
  if (!currentLine) return null;

  const textBeforeCursor = currentLine.substring(0, position.character).trim();
  const parts = textBeforeCursor.split(/[\s\(]+/);
  if (parts.length === 0) return null;
  
  const lastWord = parts[parts.length - 1];
  
  if (!lastWord) return null;

  if (doc.env) {
      const scheme = doc.env.get(lastWord);
      if (scheme) {
          let schemeType = formatType(scheme.type);
          let prefix = "";
          if (scheme.quantified.length > 0) {
             const vars = scheme.quantified.map(id => `'t${id}`).join(" ");
             prefix = `forall ${vars}. `;
          }

          const signature: SignatureInformation = {
            label: `${lastWord} : ${prefix}${schemeType}`,
          };
          
          // Extract parameters based on '->' if it's a TypeFunction
          if (scheme.type.kind === "TypeFunction") {
             const typeParts = schemeType.split('->').map(p => p.trim());
             if (typeParts.length > 1) {
                signature.parameters = typeParts.slice(0, -1).map(p => ({ label: p }));
             }
          }
          
          return {
            signatures: [signature],
            activeSignature: 0,
            activeParameter: 0
          };
      }
  }

  return null;
}

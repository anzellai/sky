import { SignatureHelp, Position, SignatureInformation } from 'vscode-languageserver/node.js';
import * as AST from '../../ast/ast.js';
import { Workspace } from '../analysis/workspace.js';
import fs from 'fs';
import path from 'path';

export function getSignatureHelp(workspace: Workspace, uri: string, position: Position): SignatureHelp | null {
  const doc = workspace.getDocument(uri);
  if (!doc || !doc.ast) return null;

  // For signature help triggered by ' ', we want to look at the token right before the cursor
  // Find the node before the cursor. For a simpler heuristic, let's see if we can find a function call
  // Since our parser is basic, we might not have a CallExpression if it's incomplete.
  // We'll scan the source code around the position.
  
  const lines = doc.source.split("\n");
  const currentLine = lines[position.line];
  if (!currentLine) return null;

  const textBeforeCursor = currentLine.substring(0, position.character).trim();
  const parts = textBeforeCursor.split(/[\s\(]+/);
  if (parts.length === 0) return null;
  
  const lastWord = parts[parts.length - 1];
  
  if (!lastWord) return null;

  // Is it a qualified name?
  const nameParts = lastWord.split('.');
  if (nameParts.length >= 2) {
    const pkgPrefix = nameParts.slice(0, -1).join(".");
    const funcName = nameParts[nameParts.length - 1];

    // Find import
    const imp = doc.ast.imports.find(i => 
      (i.alias && i.alias.name === pkgPrefix) || 
      (!i.alias && i.moduleName.join(".") === pkgPrefix)
    );

    if (imp) {
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
          const regex = new RegExp(`^${funcName} : (.*?)$`, 'm');
          const match = content.match(regex);
          if (match) {
            const sig = match[1].trim();
            const signature: SignatureInformation = {
              label: `${funcName} : ${sig}`,
              documentation: `From ${pkgName}`
            };
            
            // Extract parameters based on '->' (excluding the return type)
            const typeParts = sig.split('->').map(p => p.trim());
            if (typeParts.length > 1) {
               signature.parameters = typeParts.slice(0, -1).map(p => ({ label: p }));
            }
            
            return {
              signatures: [signature],
              activeSignature: 0,
              activeParameter: 0
            };
          }
       }
    }
  }

  // Not qualified or not found
  return null;
}

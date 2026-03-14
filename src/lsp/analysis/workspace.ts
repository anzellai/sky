import { Diagnostic, Hover, Location, Position, Range, DiagnosticSeverity } from 'vscode-languageserver/node.js';
import * as AST from '../../ast/ast.js';
import { lex } from '../../lexer/lexer.js';
import { filterLayout } from '../../parser/filter-layout.js';
import { parse } from '../../parser/parser.js';
import { typeCheckProject } from '../../compiler.js';
import { TypeEnvironment } from '../../types/env.js';

import { getHover } from '../features/hover.js';
import { getDefinition } from '../features/definition.js';
import { getCompletions } from '../features/completion.js';
import { getSignatureHelp } from '../features/signature.js';

export interface DocumentInfo {
  uri: string;
  source: string;
  ast: AST.Module | null;
  diagnostics: Diagnostic[];
  env: TypeEnvironment | null;
  modules?: readonly { filePath: string; moduleAst: AST.Module }[];
}

function uriToPath(uri: string): string {
  if (uri.startsWith('file://')) {
    let p = uri.substring(7);
    if (process.platform === 'win32') {
      if (p.startsWith('/')) {
        p = p.substring(1);
      }
      p = p.replace(/\//g, '\\');
    }
    return decodeURIComponent(p);
  }
  return uri;
}

export class Workspace {
  private documents = new Map<string, DocumentInfo>();

  public getDocument(uri: string): DocumentInfo | undefined {
    return this.documents.get(uri);
  }

  public async updateDocument(uri: string, source: string): Promise<Diagnostic[]> {
    const diagnostics: Diagnostic[] = [];
    let ast: AST.Module | null = null;
    let env: TypeEnvironment | null = null;

    try {
      const { tokens, lexErrors } = lex(source, uri) as any;
      if (lexErrors) {
        for (const err of lexErrors) {
          diagnostics.push({
            severity: DiagnosticSeverity.Error,
            range: {
              start: { line: err.span.start.line - 1, character: err.span.start.column - 1 },
              end: { line: err.span.end.line - 1, character: err.span.end.column - 1 }
            },
            message: err.message
          });
        }
      }

      const filtered = filterLayout(tokens);
      ast = parse(filtered);
    } catch (e: any) {
      // Basic syntax error handling
      diagnostics.push({
        severity: DiagnosticSeverity.Error,
        range: {
          start: { line: 0, character: 0 },
          end: { line: 0, character: 10 }
        },
        message: e.message || "Parse error"
      });
    }

    let modules;
    try {
        const filePath = uriToPath(uri);
        const result = await typeCheckProject(filePath, { path: filePath, content: source });
        
        if (result.latestModuleAst) {
            ast = result.latestModuleAst;
        }
        
        modules = result.modules;

        const moduleName = ast?.name.join(".") || "";
        const typeCheckResult = result.moduleResults.get(moduleName);
        if (typeCheckResult) {
            env = typeCheckResult.environment;
        }
        
        // Map diagnostics
        for (const diagStr of result.diagnostics) {
            // "path:line:col: message"
            const match = diagStr.match(/^(.*?):(\d+):(\d+):\s*(.*)$/);
            if (match) {
                const line = parseInt(match[2]) - 1;
                const col = parseInt(match[3]) - 1;
                diagnostics.push({
                    severity: DiagnosticSeverity.Error,
                    range: {
                        start: { line: Math.max(0, line), character: Math.max(0, col) },
                        end: { line: Math.max(0, line), character: Math.max(0, col + 5) }
                    },
                    message: match[4]
                });
            } else {
                 diagnostics.push({
                    severity: DiagnosticSeverity.Error,
                    range: { start: { line: 0, character: 0 }, end: { line: 0, character: 0 } },
                    message: diagStr
                });
            }
        }
    } catch (e) {
        // Fallback if compiler fails entirely
    }

    this.documents.set(uri, { uri, source, ast, diagnostics, env, modules });
    return diagnostics;
  }

  public getHover(uri: string, position: Position): Hover | null {
    return getHover(this, uri, position);
  }

  public getDefinition(uri: string, position: Position): Location | null {
    return getDefinition(this, uri, position);
  }

  public getCompletions(uri: string, position: Position) {
    return getCompletions(this, uri, position);
  }

  public getSignatureHelp(uri: string, position: Position) {
    return getSignatureHelp(this, uri, position);
  }

  public findNodeAtPosition(ast: AST.Module, position: Position): AST.NodeBase | null {
    const targetLine = position.line + 1;
    const targetCol = position.character + 1;

    let found: AST.NodeBase | null = null;

    function visit(node: any) {
      if (!node || typeof node !== "object") return;
      
      if (node.span && node.span.start && node.span.end) {
        const start = node.span.start;
        const end = node.span.end;
        if (
          targetLine >= start.line && targetLine <= end.line &&
          (targetLine > start.line || targetCol >= start.column) &&
          (targetLine < end.line || targetCol <= end.column)
        ) {
          found = node;
          for (const key in node) {
            if (key !== "span") {
              if (Array.isArray(node[key])) {
                node[key].forEach(visit);
              } else {
                visit(node[key]);
              }
            }
          }
        }
      } else if (Array.isArray(node)) {
        node.forEach(visit);
      } else {
        for (const key in node) {
          if (Array.isArray(node[key])) {
            node[key].forEach(visit);
          } else if (typeof node[key] === "object") {
            visit(node[key]);
          }
        }
      }
    }

    visit(ast);
    return found;
  }
}
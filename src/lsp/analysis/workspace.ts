import { Diagnostic, Hover, Location, Position, Range, DiagnosticSeverity } from 'vscode-languageserver/node.js';
import * as AST from '../../ast/ast.js';
import { lex } from '../../lexer/lexer.js';
import { filterLayout } from '../../parser/filter-layout.js';
import { parse } from '../../parser/parser.js';
import { typeCheckProject } from '../../compiler.js';
import { TypeEnvironment } from '../../types/env.js';
import type { Type, Scheme } from '../../types/types.js';

import { getHover } from '../features/hover.js';
import { getDefinition } from '../features/definition.js';
import { getCompletions } from '../features/completion.js';
import { getSignatureHelp } from '../features/signature.js';
import { getDocumentSymbols } from '../features/symbols.js';
import { findReferences } from '../features/references.js';
import { getFoldingRanges } from '../features/folding.js';
import { renameSymbol } from '../features/rename.js';

export interface DocumentInfo {
  uri: string;
  source: string;
  ast: AST.Module | null;
  diagnostics: Diagnostic[];
  env: TypeEnvironment | null;
  modules?: readonly { filePath: string; moduleAst: AST.Module }[];
  nodeTypes?: Map<string, Type>;
  moduleExports?: Map<string, Map<string, Scheme>>;
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

    // Preserve previous successful analysis for completions/hover during typing
    const prev = this.documents.get(uri);
    let env: TypeEnvironment | null = prev?.env || null;
    let nodeTypes: Map<string, Type> | undefined = prev?.nodeTypes;
    let moduleExports: Map<string, Map<string, Scheme>> | undefined = prev?.moduleExports;
    let modules = prev?.modules;

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

    try {
        const filePath = uriToPath(uri);
        const result = await typeCheckProject(filePath, { path: filePath, content: source });

        if (result.latestModuleAst) {
            ast = result.latestModuleAst;
        }

        modules = result.modules;

        moduleExports = result.exports;

        const moduleName = ast?.name.join(".") || "";
        const typeCheckResult = result.moduleResults.get(moduleName);
        if (typeCheckResult) {
            env = typeCheckResult.environment;
            nodeTypes = typeCheckResult.nodeTypes;
        }

        // Collect diagnostics: module-specific type errors + graph-level resolution errors
        const allDiags: any[] = [];
        // Add type check diagnostics from the current module only
        if (typeCheckResult && typeCheckResult.diagnostics) {
            allDiags.push(...typeCheckResult.diagnostics);
        }
        // Add graph-level diagnostics (resolution errors like "Cannot resolve import")
        for (const d of result.diagnostics) {
            if (typeof d === 'string') allDiags.push(d);
        }

        for (const diag of allDiags) {
            if (typeof diag === 'string') {
                // String diagnostic (e.g., from module resolution)
                const match = diag.match(/^(.*?):(\d+):(\d+):\s*(.*)$/);
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
                        message: diag
                    });
                }
            } else if (diag && typeof diag === 'object' && diag.message) {
                // TypeDiagnostic object with { severity, message, span, hint? }
                const span = diag.span;
                const startLine = span?.start?.line ? span.start.line - 1 : 0;
                const startCol = span?.start?.column ? span.start.column - 1 : 0;
                const endLine = span?.end?.line ? span.end.line - 1 : startLine;
                const endCol = span?.end?.column ? span.end.column - 1 : startCol + 10;
                diagnostics.push({
                    severity: diag.severity === 'warning' ? DiagnosticSeverity.Warning : DiagnosticSeverity.Error,
                    range: {
                        start: { line: Math.max(0, startLine), character: Math.max(0, startCol) },
                        end: { line: Math.max(0, endLine), character: Math.max(0, endCol) }
                    },
                    message: diag.hint ? `${diag.message}\n${diag.hint}` : diag.message
                });
            }
        }
    } catch (e: any) {
        // If the compiler crashes, report it as a diagnostic so the user sees something
        diagnostics.push({
            severity: DiagnosticSeverity.Error,
            range: { start: { line: 0, character: 0 }, end: { line: 0, character: 0 } },
            message: `Sky analysis error: ${e?.message || String(e)}`
        });
    }

    this.documents.set(uri, { uri, source, ast, diagnostics, env, modules, nodeTypes, moduleExports });
    return diagnostics;
  }

  public getHover(uri: string, position: Position): Hover | null {
    return getHover(this, uri, position);
  }

  public getDefinition(uri: string, position: Position): Location | null {
    return getDefinition(this, uri, position);
  }

  public getCompletions(uri: string, position: Position, liveText?: string) {
    return getCompletions(this, uri, position, liveText);
  }

  public getSignatureHelp(uri: string, position: Position) {
    return getSignatureHelp(this, uri, position);
  }

  public getDocumentSymbols(uri: string): import('vscode-languageserver/node.js').DocumentSymbol[] {
    const doc = this.documents.get(uri);
    if (!doc || !doc.ast) return [];
    return getDocumentSymbols(doc.ast);
  }

  public getReferences(uri: string, position: Position): Location[] {
    const doc = this.documents.get(uri);
    if (!doc || !doc.ast) return [];

    const node = this.findNodeAtPosition(doc.ast, position);
    if (!node) return [];

    let name = "";
    if (node.kind === "IdentifierExpression") {
      name = (node as AST.IdentifierExpression).name;
    } else if (node.kind === "QualifiedIdentifierExpression") {
      const parts = (node as AST.QualifiedIdentifierExpression).name.parts;
      name = parts[parts.length - 1];
    } else if (node.kind === "FunctionDeclaration") {
      name = (node as AST.FunctionDeclaration).name;
    } else {
      return [];
    }

    if (!name) return [];

    const modules = doc.modules || [];
    return findReferences(name, modules as { filePath: string; moduleAst: AST.Module }[]);
  }

  public getFoldingRanges(uri: string): import('vscode-languageserver/node.js').FoldingRange[] {
    const doc = this.documents.get(uri);
    if (!doc || !doc.ast) return [];
    return getFoldingRanges(doc.ast);
  }

  public getRename(uri: string, position: Position, newName: string): import('vscode-languageserver/node.js').WorkspaceEdit | null {
    const doc = this.documents.get(uri);
    if (!doc || !doc.ast) return null;

    const node = this.findNodeAtPosition(doc.ast, position);
    if (!node) return null;

    let oldName = "";
    if (node.kind === "IdentifierExpression") {
      oldName = (node as AST.IdentifierExpression).name;
    } else if (node.kind === "FunctionDeclaration") {
      oldName = (node as AST.FunctionDeclaration).name;
    } else {
      return null;
    }

    if (!oldName) return null;

    const modules = doc.modules || [];
    const moduleMap = new Map<string, { filePath: string; moduleAst: AST.Module }>();
    for (const m of modules) {
      moduleMap.set(m.filePath, m as { filePath: string; moduleAst: AST.Module });
    }
    return renameSymbol(oldName, newName, moduleMap);
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
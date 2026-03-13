import { Diagnostic, Hover, Location, Position, Range, DiagnosticSeverity } from 'vscode-languageserver/node.js';
import * as AST from '../../ast/ast.js';
import { lex } from '../../lexer/lexer.js';
import { filterLayout } from '../../parser/filter-layout.js';
import { parse } from '../../parser/parser.js';

import { getHover } from '../features/hover.js';
import { getDefinition } from '../features/definition.js';

export interface DocumentInfo {
  uri: string;
  source: string;
  ast: AST.Module | null;
  diagnostics: Diagnostic[];
}

export class Workspace {
  private documents = new Map<string, DocumentInfo>();

  public getDocument(uri: string): DocumentInfo | undefined {
    return this.documents.get(uri);
  }

  public updateDocument(uri: string, source: string): Diagnostic[] {
    const diagnostics: Diagnostic[] = [];
    let ast: AST.Module | null = null;

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

    this.documents.set(uri, { uri, source, ast, diagnostics });
    return diagnostics;
  }

  public getHover(uri: string, position: Position): Hover | null {
    return getHover(this, uri, position);
  }

  public getDefinition(uri: string, position: Position): Location | null {
    return getDefinition(this, uri, position);
  }

  public findNodeAtPosition(ast: AST.Module, position: Position): AST.NodeBase | null {
    // Real implementation would traverse the AST looking for the narrowest node containing the position
    // Since AST uses 1-based lines, we adjust:
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
          // Continue searching children for a narrower match
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
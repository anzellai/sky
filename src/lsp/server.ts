import {
  createConnection,
  CompletionItem,
  CompletionItemKind,
  ProposedFeatures,
  TextDocuments,
  InitializeParams,
  Diagnostic,
  DiagnosticSeverity,
  TextDocumentSyncKind,
  TextDocumentChangeEvent,
  Definition,
  Hover
} from "vscode-languageserver/node.js";

import { TextDocument } from "vscode-languageserver-textdocument";

import { lex } from "../lexer.js";
import { parse } from "../parser.js";
import { filterLayout } from "../parser/filter-layout.js";
import { typeCheckProject } from "../compiler.js";
import { URL } from "url";

import { findIdentifierAtPosition } from "./find-node.js";
import { SymbolIndex } from "./symbol-index.js";

const connection = createConnection(ProposedFeatures.all);
const documents = new TextDocuments(TextDocument);

const symbols = new SymbolIndex();

let lastModule: any | undefined;
let lastTypeCheck: any | undefined;

connection.onInitialize((_params: InitializeParams) => {
  return {
    capabilities: {
      textDocumentSync: TextDocumentSyncKind.Incremental,
      hoverProvider: true,
      definitionProvider: true,
      completionProvider: {
        resolveProvider: false
      }
    }
  };
});

documents.onDidOpen((change: TextDocumentChangeEvent<TextDocument>) => {
  validate(change.document);
});

documents.onDidChangeContent((change: TextDocumentChangeEvent<TextDocument>) => {
  validate(change.document);
});

function identifierName(node: any): string {

  const name = node?.name;

  if (typeof name === "string") {
    return name;
  }

  if (name && Array.isArray(name.parts)) {
    return name.parts[name.parts.length - 1];
  }

  return "";
}

async function validate(document: TextDocument) {

  const diagnostics: Diagnostic[] = [];
  const source = document.getText();

  const lexResult = lex(source, document.uri);

  for (const d of lexResult.diagnostics) {
    diagnostics.push({
      severity: DiagnosticSeverity.Error,
      message: d.message,
      range: {
        start: {
          line: d.span.start.line - 1,
          character: d.span.start.column - 1
        },
        end: {
          line: d.span.start.line - 1,
          character: d.span.start.column
        }
      }
    });
  }

  if (diagnostics.length === 0) {
    try {
      const filePath = new URL(document.uri).pathname;
      const result = await typeCheckProject(filePath, {
        path: filePath,
        content: source
      });

      if (result.latestModuleAst) {
        lastModule = result.latestModuleAst;
        symbols.build(result.latestModuleAst);
      }

      if (result.latestModuleAst && result.moduleResults.has(result.latestModuleAst.name.join("."))) {
        lastTypeCheck = result.moduleResults.get(result.latestModuleAst.name.join("."));
      }

      // Collect diagnostics matching this file
      for (const msg of result.diagnostics) {
        if (msg.startsWith(filePath) || msg.startsWith(document.uri)) {
          // Attempt to parse line/col from diagnostic string like "/path:1:5: message"
          const match = msg.match(/:(\d+):(\d+):\s*(.*)/);
          if (match) {
            const line = parseInt(match[1]) - 1;
            const col = parseInt(match[2]) - 1;
            diagnostics.push({
              severity: DiagnosticSeverity.Error,
              message: match[3],
              range: {
                start: { line, character: col },
                end: { line, character: col + 1 }
              }
            });
          } else {
             diagnostics.push({
              severity: DiagnosticSeverity.Error,
              message: msg,
              range: {
                start: { line: 0, character: 0 },
                end: { line: 0, character: 1 }
              }
            });
          }
        }
      }

    } catch (err) {
      diagnostics.push({
        severity: DiagnosticSeverity.Error,
        message: err instanceof Error ? err.message : String(err),
        range: {
          start: { line: 0, character: 0 },
          end: { line: 0, character: 1 }
        }
      });
    }
  }

  connection.sendDiagnostics({
    uri: document.uri,
    diagnostics
  });

}

connection.onDefinition((params): Definition | null => {

  if (!lastModule) return null;

  const node = findIdentifierAtPosition(
    lastModule,
    params.position.line + 1,
    params.position.character + 1
  );

  if (!node) return null;

  const name = identifierName(node);

  const symbol = symbols.lookup(name);

  if (!symbol) return null;

  return {
    uri: params.textDocument.uri,
    range: {
      start: {
        line: symbol.span.start.line - 1,
        character: symbol.span.start.column - 1
      },
      end: {
        line: symbol.span.end.line - 1,
        character: symbol.span.end.column - 1
      }
    }
  };

});

connection.onHover((params): Hover | null => {

  if (!lastModule || !lastTypeCheck) return null;

  const node = findIdentifierAtPosition(
    lastModule,
    params.position.line + 1,
    params.position.character + 1
  );

  if (!node) return null;

  const name = identifierName(node);

  const info = lastTypeCheck.declarations?.find((d: any) => d.name === name);

  if (!info) return null;

  return {
    contents: {
      kind: "markdown",
      value: `\`\`\`sky\n${info.name} : ${info.pretty}\n\`\`\``
    }
  };

});

documents.listen(connection);
connection.listen();

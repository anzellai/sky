import {
  createConnection,
  TextDocuments,
  Diagnostic,
  DiagnosticSeverity,
  ProposedFeatures,
  InitializeParams,
  DidChangeConfigurationNotification,
  TextDocumentSyncKind,
  InitializeResult,
  HoverParams,
  Hover,
  DefinitionParams,
  Location,
  DocumentFormattingParams,
  TextEdit,
  Position,
  CompletionItem,
  CompletionParams,
  SignatureHelp,
  SignatureHelpParams
} from 'vscode-languageserver/node.js';

import { TextDocument } from 'vscode-languageserver-textdocument';
import { Workspace } from './analysis/workspace.js';
import { formatModule } from './formatter/formatter.js';
import { lex } from '../lexer/lexer.js';
import { filterLayout } from '../parser/filter-layout.js';
import { parse } from '../parser/parser.js';

export function startServer() {
  const connection = createConnection(ProposedFeatures.all);
  const documents: TextDocuments<TextDocument> = new TextDocuments(TextDocument);

  const workspace = new Workspace();

  connection.onInitialize((params: InitializeParams) => {
    const result: InitializeResult = {
      capabilities: {
        textDocumentSync: TextDocumentSyncKind.Incremental,
        hoverProvider: true,
        definitionProvider: true,
        documentFormattingProvider: true,
        completionProvider: { resolveProvider: false, triggerCharacters: ['.'] },
        signatureHelpProvider: { triggerCharacters: [' ', '('] }
      }
    };
    return result;
  });

  connection.onInitialized(() => {
    connection.console.log("Sky LSP Server initialized");
  });

  documents.onDidChangeContent(change => {
    validateTextDocument(change.document);
  });

  async function validateTextDocument(textDocument: TextDocument): Promise<void> {
    const diagnostics: Diagnostic[] = workspace.updateDocument(textDocument.uri, textDocument.getText());
    connection.sendDiagnostics({ uri: textDocument.uri, diagnostics });
  }

  connection.onHover((params: HoverParams): Hover | null => {
    return workspace.getHover(params.textDocument.uri, params.position);
  });

  connection.onDefinition((params: DefinitionParams): Location | null => {
    return workspace.getDefinition(params.textDocument.uri, params.position);
  });

  connection.onCompletion((params: CompletionParams): CompletionItem[] => {
    return workspace.getCompletions(params.textDocument.uri, params.position);
  });

  connection.onSignatureHelp((params: SignatureHelpParams): SignatureHelp | null => {
    return workspace.getSignatureHelp(params.textDocument.uri, params.position);
  });

  connection.onDocumentFormatting((params: DocumentFormattingParams): TextEdit[] | null => {
    const doc = documents.get(params.textDocument.uri);
    if (!doc) return null;

    const text = doc.getText();
    try {
      const { tokens } = lex(text, params.textDocument.uri);
      const filtered = filterLayout(tokens);
      const ast = parse(filtered);
      const formatted = formatModule(ast);
      
      if (formatted === text) return [];

      return [
        TextEdit.replace(
          {
            start: doc.positionAt(0),
            end: doc.positionAt(text.length)
          },
          formatted
        )
      ];
    } catch (e) {
      connection.console.error(`Formatting failed: ${e}`);
      return null;
    }
  });

  documents.listen(connection);
  connection.listen();
}

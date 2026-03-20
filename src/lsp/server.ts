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
  // Prevent the LSP from crashing on unhandled errors
  process.on('uncaughtException', (err) => {
    // Silently swallow — the LSP must stay alive
  });
  process.on('unhandledRejection', (err) => {
    // Silently swallow — the LSP must stay alive
  });

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
        signatureHelpProvider: { triggerCharacters: [' ', '('] },
        documentSymbolProvider: true,
        referencesProvider: true,
        foldingRangeProvider: true,
        renameProvider: true
      }
    };
    return result;
  });

  connection.onInitialized(() => {
    connection.console.log("Sky LSP Server initialized");
  });

  // Debounce validation to avoid recompiling on every keystroke
  const pendingValidations = new Map<string, ReturnType<typeof setTimeout>>();

  documents.onDidChangeContent(change => {
    const uri = change.document.uri;
    const existing = pendingValidations.get(uri);
    if (existing) clearTimeout(existing);
    pendingValidations.set(uri, setTimeout(async () => {
      pendingValidations.delete(uri);
      try { await validateTextDocument(change.document); }
      catch {}
    }, 300));
  });

  async function validateTextDocument(textDocument: TextDocument): Promise<void> {
    const diagnostics: Diagnostic[] = await workspace.updateDocument(textDocument.uri, textDocument.getText());
    connection.sendDiagnostics({ uri: textDocument.uri, diagnostics });
  }

  connection.onHover((params: HoverParams): Hover | null => {
    try { return workspace.getHover(params.textDocument.uri, params.position); }
    catch { return null; }
  });

  connection.onDefinition((params: DefinitionParams): Location | null => {
    try { return workspace.getDefinition(params.textDocument.uri, params.position); }
    catch { return null; }
  });

  connection.onCompletion((params: CompletionParams) => {
    try {
      const items = workspace.getCompletions(params.textDocument.uri, params.position);
      return { isIncomplete: true, items };
    } catch { return { isIncomplete: true, items: [] }; }
  });

  connection.onSignatureHelp((params: SignatureHelpParams): SignatureHelp | null => {
    try { return workspace.getSignatureHelp(params.textDocument.uri, params.position); }
    catch { return null; }
  });

  connection.onDocumentSymbol((params) => {
    try { return workspace.getDocumentSymbols(params.textDocument.uri); }
    catch { return []; }
  });

  connection.onReferences((params) => {
    try { return workspace.getReferences(params.textDocument.uri, params.position); }
    catch { return []; }
  });

  connection.onFoldingRanges((params) => {
    try { return workspace.getFoldingRanges(params.textDocument.uri); }
    catch { return []; }
  });

  connection.onRenameRequest((params) => {
    try { return workspace.getRename(params.textDocument.uri, params.position, params.newName); }
    catch { return null; }
  });

  connection.onDocumentFormatting((params: DocumentFormattingParams): TextEdit[] | null => {
    const doc = documents.get(params.textDocument.uri);
    if (!doc) return null;

    const originalText = doc.getText();
    try {
      const { tokens } = lex(originalText, params.textDocument.uri);
      const filtered = filterLayout(tokens);
      const ast = parse(filtered);
      const formatted = formatModule(ast, originalText);

      if (formatted === originalText) return [];

      // Roundtrip safety
      try {
        parse(filterLayout(lex(formatted, params.textDocument.uri).tokens));
      } catch {
        return null;
      }

      return [
        TextEdit.replace(
          { start: doc.positionAt(0), end: doc.positionAt(originalText.length) },
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

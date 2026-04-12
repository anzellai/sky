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
import { typeCheckProject } from '../compiler.js';
import { formatModule } from './formatter/formatter.js';
import { lex } from '../lexer/lexer.js';
import { filterLayout } from '../parser/filter-layout.js';
import { parse } from '../parser/parser.js';

function uriToPath(uri: string): string {
  if (uri.startsWith('file://')) {
    let p = uri.substring(7);
    if (process.platform === 'win32') {
      if (p.startsWith('/')) p = p.substring(1);
      p = p.replace(/\//g, '\\');
    }
    return decodeURIComponent(p);
  }
  return uri;
}

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

    // Pre-warm caches in the background: find the first open document's
    // project root and run typeCheckProject once so subsequent requests
    // hit warm caches (~2s instead of ~38s cold start for large dep trees).
    setImmediate(async () => {
      try {
        for (const doc of documents.all()) {
          const filePath = uriToPath(doc.uri);
          await typeCheckProject(filePath, { path: filePath, content: doc.getText() });
          break; // one pre-warm is enough — caches are shared
        }
      } catch {}
    });
  });

  // Debounce validation to avoid recompiling on every keystroke.
  // Uses a longer delay for the first validation (cold cache) to avoid
  // blocking the LSP server during initial type checking of large projects.
  const pendingValidations = new Map<string, ReturnType<typeof setTimeout>>();
  const validatedOnce = new Set<string>();

  documents.onDidChangeContent(change => {
    const uri = change.document.uri;
    const existing = pendingValidations.get(uri);
    if (existing) clearTimeout(existing);
    // First validation gets a longer delay to let the LSP finish init
    // handshake before starting heavy type checking.
    const delay = validatedOnce.has(uri) ? 300 : 500;
    pendingValidations.set(uri, setTimeout(async () => {
      pendingValidations.delete(uri);
      try { await validateTextDocument(change.document); }
      catch {}
      validatedOnce.add(uri);
    }, delay));
  });

  // Track whether a background validation is in progress so requests
  // can return stale-but-fast results instead of waiting.
  let validating = false;

  async function validateTextDocument(textDocument: TextDocument): Promise<void> {
    validating = true;
    try {
      // Yield to the event loop before starting heavy type checking so the
      // LSP can respond to pending requests (hover, completion) with stale data.
      await new Promise(resolve => setImmediate(resolve));
      const diagnostics: Diagnostic[] = await workspace.updateDocument(textDocument.uri, textDocument.getText());
      connection.sendDiagnostics({ uri: textDocument.uri, diagnostics });
    } finally {
      validating = false;
    }
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
      // Pass the live document text so completions work even before
      // the background type check has updated the stored doc.source.
      const liveDoc = documents.get(params.textDocument.uri);
      const liveText = liveDoc?.getText();
      const items = workspace.getCompletions(params.textDocument.uri, params.position, liveText);
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

//! `sky-lsp` — the `tower-lsp` transport over the `crate::Analysis` engine
//! (doc 10). This binary is *thin*: it owns a shared source db behind one async
//! mutex and answers each request by running a query — no fixpoint loop, no
//! background `sky check` thread, no externals timeout (L1/L2). All the analysis
//! lives in the library (`lib.rs`) so it is testable without a client.

use std::collections::HashMap;
use std::path::PathBuf;
use std::sync::{Arc, Mutex as StdMutex};
use std::time::Duration;

use crate::Analysis;
use tokio::sync::Mutex;
use tower_lsp::jsonrpc::Result;
use tower_lsp::lsp_types::*;
use tower_lsp::{Client, LanguageServer, LspService, Server};

/// Quiet window before a `didChange` publishes diagnostics. Rapid edits inside
/// this window coalesce into ONE publish (the newest generation wins); a
/// superseded generation's delayed publish is dropped. Only publish *timing*
/// changes — diagnostic content is exactly what a synchronous publish produced.
const DEBOUNCE_MS: u64 = 200;

struct Backend {
    client: Client,
    /// The one piece of state: the analysis engine (source db + open docs).
    analysis: Arc<Mutex<Analysis>>,
    /// Per-document monotonic edit generation. Each `didChange` bumps its
    /// document's counter and spawns a delayed publish that fires only if its
    /// captured generation is STILL the latest — so a stale (superseded) publish
    /// for an older edit is dropped, and a keystroke burst yields one publish.
    gens: Arc<StdMutex<HashMap<Url, u64>>>,
}

impl Backend {
    async fn publish(&self, uri: Url) {
        let diags = {
            let a = self.analysis.lock().await;
            a.diagnostics(&uri)
        };
        self.client.publish_diagnostics(uri, diags, None).await;
    }
}

#[tower_lsp::async_trait]
impl LanguageServer for Backend {
    async fn initialize(&self, params: InitializeParams) -> Result<InitializeResult> {
        // Prefer the deprecated `root_uri`, but fall back to `workspaceFolders`
        // (what modern clients — Helix included — send). Without this a client
        // that only sends workspaceFolders would leave the LSP with no root.
        let root: Option<PathBuf> = params
            .root_uri
            .as_ref()
            .and_then(|u| u.to_file_path().ok())
            .or_else(|| {
                params
                    .workspace_folders
                    .as_ref()
                    .and_then(|fs| fs.first())
                    .and_then(|f| f.uri.to_file_path().ok())
            });
        {
            let mut a = self.analysis.lock().await;
            a.load_stdlib(root.as_deref());
            if let Some(r) = &root {
                a.load_project(r);
            }
        }
        Ok(InitializeResult {
            server_info: Some(ServerInfo {
                name: "sky-lsp".into(),
                version: Some("1.0.0".into()),
            }),
            capabilities: ServerCapabilities {
                text_document_sync: Some(TextDocumentSyncCapability::Kind(
                    TextDocumentSyncKind::FULL,
                )),
                hover_provider: Some(HoverProviderCapability::Simple(true)),
                definition_provider: Some(OneOf::Left(true)),
                declaration_provider: Some(DeclarationCapability::Simple(true)),
                completion_provider: Some(CompletionOptions {
                    trigger_characters: Some(vec![".".into()]),
                    ..Default::default()
                }),
                references_provider: Some(OneOf::Left(true)),
                workspace_symbol_provider: Some(OneOf::Left(true)),
                document_highlight_provider: Some(OneOf::Left(true)),
                rename_provider: Some(OneOf::Right(RenameOptions {
                    prepare_provider: Some(true),
                    work_done_progress_options: Default::default(),
                })),
                document_symbol_provider: Some(OneOf::Left(true)),
                document_formatting_provider: Some(OneOf::Left(true)),
                inlay_hint_provider: Some(OneOf::Left(true)),
                code_action_provider: Some(CodeActionProviderCapability::Options(
                    CodeActionOptions {
                        code_action_kinds: Some(vec![
                            CodeActionKind::QUICKFIX,
                            CodeActionKind::SOURCE_ORGANIZE_IMPORTS,
                        ]),
                        work_done_progress_options: Default::default(),
                        resolve_provider: Some(false),
                    },
                )),
                signature_help_provider: Some(SignatureHelpOptions {
                    trigger_characters: Some(vec!["(".into(), ",".into()]),
                    retrigger_characters: Some(vec![",".into()]),
                    work_done_progress_options: Default::default(),
                }),
                semantic_tokens_provider: Some(
                    SemanticTokensServerCapabilities::SemanticTokensOptions(
                        SemanticTokensOptions {
                            legend: crate::semantic_legend(),
                            full: Some(SemanticTokensFullOptions::Bool(true)),
                            range: Some(false),
                            work_done_progress_options: Default::default(),
                        },
                    ),
                ),
                ..Default::default()
            },
        })
    }

    async fn initialized(&self, _: InitializedParams) {
        self.client
            .log_message(MessageType::INFO, "sky-lsp ready")
            .await;
    }

    async fn shutdown(&self) -> Result<()> {
        Ok(())
    }

    async fn did_open(&self, params: DidOpenTextDocumentParams) {
        let uri = params.text_document.uri.clone();
        {
            let mut a = self.analysis.lock().await;
            // Load the stdlib + this file's project, resolved from the file path
            // itself — so hover/completion/goto work regardless of how the client
            // chose the workspace root, and even for a project outside the
            // compiler repo (embedded stdlib).
            if let Ok(path) = uri.to_file_path() {
                a.ensure_project_for(&path);
            }
            a.set_document(uri.clone(), params.text_document.text);
        }
        self.publish(uri).await;
    }

    async fn did_change(&self, params: DidChangeTextDocumentParams) {
        let uri = params.text_document.uri.clone();
        // FULL sync: the last content change is the whole buffer. Apply it
        // IMMEDIATELY (before any await on the publish) so hover/completion/goto
        // on the next request already see the fresh text — only the diagnostics
        // publish is debounced.
        if let Some(change) = params.content_changes.into_iter().next_back() {
            let mut a = self.analysis.lock().await;
            a.set_document(uri.clone(), change.text);
        }
        // Bump this document's generation and capture it for the delayed publish.
        let my_gen = {
            let mut g = self.gens.lock().unwrap();
            let e = g.entry(uri.clone()).or_insert(0);
            *e += 1;
            *e
        };
        // Debounced, version-guarded publish. After the quiet window, publish
        // only if no newer edit superseded this generation; otherwise the stale
        // publish is dropped. Diagnostics are computed from the (always-latest)
        // analysis state, so a surviving publish reflects the newest edit.
        let client = self.client.clone();
        let analysis = self.analysis.clone();
        let gens = self.gens.clone();
        tokio::spawn(async move {
            tokio::time::sleep(Duration::from_millis(DEBOUNCE_MS)).await;
            // Superseded by a later edit while we slept → drop this publish.
            if gens.lock().unwrap().get(&uri).copied() != Some(my_gen) {
                return;
            }
            let diags = {
                let a = analysis.lock().await;
                a.diagnostics(&uri)
            };
            client.publish_diagnostics(uri, diags, None).await;
        });
    }

    async fn hover(&self, params: HoverParams) -> Result<Option<Hover>> {
        let p = params.text_document_position_params;
        let a = self.analysis.lock().await;
        Ok(a.hover(&p.text_document.uri, p.position))
    }

    async fn goto_definition(
        &self,
        params: GotoDefinitionParams,
    ) -> Result<Option<GotoDefinitionResponse>> {
        let p = params.text_document_position_params;
        let a = self.analysis.lock().await;
        Ok(a.goto(&p.text_document.uri, p.position)
            .map(GotoDefinitionResponse::Scalar))
    }

    /// `textDocument/declaration` — for Sky, a symbol's declaration IS its
    /// definition (no forward-declare / header split), so this delegates to the
    /// exact engine resolution `goto_definition` uses. Advertised via
    /// `declaration_provider`; without this method a client's request would get
    /// `method_not_found` (the tower-lsp default), a protocol bug.
    async fn goto_declaration(
        &self,
        params: request::GotoDeclarationParams,
    ) -> Result<Option<request::GotoDeclarationResponse>> {
        let p = params.text_document_position_params;
        let a = self.analysis.lock().await;
        Ok(a.goto(&p.text_document.uri, p.position)
            .map(request::GotoDeclarationResponse::Scalar))
    }

    async fn completion(&self, params: CompletionParams) -> Result<Option<CompletionResponse>> {
        let p = params.text_document_position;
        let a = self.analysis.lock().await;
        let items = a.completion(&p.text_document.uri, p.position);
        Ok(Some(CompletionResponse::Array(items)))
    }

    async fn references(&self, params: ReferenceParams) -> Result<Option<Vec<Location>>> {
        let p = params.text_document_position;
        let include_decl = params.context.include_declaration;
        let a = self.analysis.lock().await;
        Ok(Some(a.references(
            &p.text_document.uri,
            p.position,
            include_decl,
        )))
    }

    async fn symbol(
        &self,
        params: WorkspaceSymbolParams,
    ) -> Result<Option<Vec<SymbolInformation>>> {
        let a = self.analysis.lock().await;
        Ok(Some(a.workspace_symbol(&params.query)))
    }

    async fn document_highlight(
        &self,
        params: DocumentHighlightParams,
    ) -> Result<Option<Vec<DocumentHighlight>>> {
        let p = params.text_document_position_params;
        let a = self.analysis.lock().await;
        Ok(Some(a.document_highlight(&p.text_document.uri, p.position)))
    }

    async fn prepare_rename(
        &self,
        params: TextDocumentPositionParams,
    ) -> Result<Option<PrepareRenameResponse>> {
        let a = self.analysis.lock().await;
        Ok(a.prepare_rename(&params.text_document.uri, params.position))
    }

    async fn rename(&self, params: RenameParams) -> Result<Option<WorkspaceEdit>> {
        let p = params.text_document_position;
        let a = self.analysis.lock().await;
        Ok(a.rename(&p.text_document.uri, p.position, &params.new_name))
    }

    async fn document_symbol(
        &self,
        params: DocumentSymbolParams,
    ) -> Result<Option<DocumentSymbolResponse>> {
        let a = self.analysis.lock().await;
        let syms = a.document_symbols(&params.text_document.uri);
        Ok(Some(DocumentSymbolResponse::Nested(syms)))
    }

    async fn semantic_tokens_full(
        &self,
        params: SemanticTokensParams,
    ) -> Result<Option<SemanticTokensResult>> {
        let a = self.analysis.lock().await;
        Ok(a.semantic_tokens(&params.text_document.uri))
    }

    async fn formatting(&self, params: DocumentFormattingParams) -> Result<Option<Vec<TextEdit>>> {
        let a = self.analysis.lock().await;
        Ok(a.formatting(&params.text_document.uri))
    }

    async fn inlay_hint(&self, params: InlayHintParams) -> Result<Option<Vec<InlayHint>>> {
        let a = self.analysis.lock().await;
        Ok(Some(a.inlay_hints(&params.text_document.uri, params.range)))
    }

    async fn code_action(&self, params: CodeActionParams) -> Result<Option<CodeActionResponse>> {
        let a = self.analysis.lock().await;
        Ok(Some(a.code_actions(
            &params.text_document.uri,
            params.range,
            &params.context,
        )))
    }

    async fn signature_help(&self, params: SignatureHelpParams) -> Result<Option<SignatureHelp>> {
        let p = params.text_document_position_params;
        let a = self.analysis.lock().await;
        Ok(a.signature_help(&p.text_document.uri, p.position))
    }
}

pub fn run() {
    let rt = tokio::runtime::Builder::new_multi_thread()
        .enable_all()
        .build()
        .expect("sky-lsp: failed to start tokio runtime");
    rt.block_on(async {
        let stdin = tokio::io::stdin();
        let stdout = tokio::io::stdout();
        let (service, socket) = LspService::new(|client| Backend {
            client,
            analysis: Arc::new(Mutex::new(Analysis::new())),
            gens: Arc::new(StdMutex::new(HashMap::new())),
        });
        Server::new(stdin, stdout, socket).serve(service).await;
    });
}

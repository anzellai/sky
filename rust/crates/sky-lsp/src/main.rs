//! `sky-lsp` — the `tower-lsp` transport over the `sky_lsp::Analysis` engine
//! (doc 10). This binary is *thin*: it owns a shared source db behind one async
//! mutex and answers each request by running a query — no fixpoint loop, no
//! background `sky check` thread, no externals timeout (L1/L2). All the analysis
//! lives in the library (`lib.rs`) so it is testable without a client.

use std::path::PathBuf;
use std::sync::Arc;

use sky_lsp::Analysis;
use tokio::sync::Mutex;
use tower_lsp::jsonrpc::Result;
use tower_lsp::lsp_types::*;
use tower_lsp::{Client, LanguageServer, LspService, Server};

struct Backend {
    client: Client,
    /// The one piece of state: the analysis engine (source db + open docs).
    analysis: Arc<Mutex<Analysis>>,
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
        let root: Option<PathBuf> = params.root_uri.as_ref().and_then(|u| u.to_file_path().ok());
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
                rename_provider: Some(OneOf::Right(RenameOptions {
                    prepare_provider: Some(true),
                    work_done_progress_options: Default::default(),
                })),
                document_symbol_provider: Some(OneOf::Left(true)),
                semantic_tokens_provider: Some(
                    SemanticTokensServerCapabilities::SemanticTokensOptions(
                        SemanticTokensOptions {
                            legend: sky_lsp::semantic_legend(),
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
            a.set_document(uri.clone(), params.text_document.text);
        }
        self.publish(uri).await;
    }

    async fn did_change(&self, params: DidChangeTextDocumentParams) {
        let uri = params.text_document.uri.clone();
        // FULL sync: the last content change is the whole buffer.
        if let Some(change) = params.content_changes.into_iter().next_back() {
            let mut a = self.analysis.lock().await;
            a.set_document(uri.clone(), change.text);
        }
        self.publish(uri).await;
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
        Ok(Some(a.references(&p.text_document.uri, p.position, include_decl)))
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
}

#[tokio::main]
async fn main() {
    let stdin = tokio::io::stdin();
    let stdout = tokio::io::stdout();
    let (service, socket) = LspService::new(|client| Backend {
        client,
        analysis: Arc::new(Mutex::new(Analysis::new())),
    });
    Server::new(stdin, stdout, socket).serve(service).await;
}

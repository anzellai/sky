//! `sky_lsp` — the analysis engine behind the LSP server (doc 10). Per law L2
//! the LSP is *not* a special case: every request is a query over the same
//! `hir` resolver + `ty` inference the CLI uses — no LSP-private fixpoint, no
//! background `sky check` thread, no externals side-channel. This module holds
//! the driver-set state (open documents) and answers hover / goto-definition /
//! completion / diagnostics by resolving the cursor to a `Res` (via the
//! resolver's occurrence index) and reading the type table.
//!
//! `SourceDb` uses `Rc`/`RefCell` internally (so it is `!Send`); tower-lsp needs
//! a `Send` backend. We therefore keep the *inputs* here — parsed documents,
//! which are cheap `Arc`-backed `syntax::Parse` values (`Send + Sync`) — and
//! rebuild the lightweight `SourceDb` synchronously inside each request, never
//! holding it across an `await`. (The salsa `db` in doc 10 makes this a
//! memoised handle; this is the value-threaded variant the task permits.)
//!
//! Deliberately independent of `tower-lsp`'s server so it can be unit- and
//! integration-tested directly; `main.rs` is the thin async transport wrapper.
//!
//! **Persistent salsa db (doc 10 §"Incremental for free", the salsa payoff).**
//! The engine holds ONE long-lived `skydb::SkyDatabase`. On `didOpen`/`didChange`
//! only the edited file's `SourceFile` input is `set_source_text`-ed; every
//! feature answers by running the same tracked queries (`resolve`/`type_world`/
//! `infer`) the CLI uses, reached through the forbid-clean `SkyDb`/`TyDb` traits.
//! Salsa recomputes only the edited module + its dependents — an untouched
//! module's memoised `resolve`/`infer` stands, so a keystroke no longer re-walks
//! the whole stdlib+project (the per-request `SourceDb` rebuild is gone). The
//! `SkyDatabase` is `Send`, so it is held across `await` behind the server's one
//! async mutex; no salsa is imported here (it stays quarantined in `skydb`, L1).

use base::{DefId, FileId, ModuleId, Span};
use hir::{DefKind, FieldOcc, ImportSource, LocalId, RefOcc, Res, ResolveResult, SkyDb, TypeOcc};
use skydb::{SkyDatabase, SourceFile};
use std::collections::{BTreeMap, HashMap, HashSet};
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};
use syntax::ast::{self, AstNode};
use syntax::SyntaxKind;
use ty::{Ty, TyDb, Typer};

use tower_lsp::lsp_types::{
    CompletionItem, CompletionItemKind, Diagnostic, DiagnosticRelatedInformation,
    DiagnosticSeverity, DocumentSymbol, Hover,
    HoverContents, InlayHint, InlayHintKind, InlayHintLabel, Location, MarkupContent, MarkupKind,
    ParameterInformation, ParameterLabel, Position, PrepareRenameResponse, Range, SemanticToken,
    SemanticTokenType, SemanticTokens, SemanticTokensLegend, SemanticTokensResult, SignatureHelp,
    SignatureInformation, SymbolKind, TextEdit, Url, WorkspaceEdit,
};

/// One loaded document: its parsed tree, source text, and url. (The module name
/// keys `by_name`; it need not be duplicated on the doc.)
struct Doc {
    parse: syntax::Parse,
    text: String,
    url: Url,
}

/// The workspace: a persistent salsa `SkyDatabase` plus the driver-set inputs.
/// The db is long-lived — an edit `set_source_text`s the one changed file's input
/// and every query recomputes only the dirty sub-DAG (L2). `docs`/`files` mirror
/// the db's module registry position-for-position so a `ModuleId`/span
/// (`FileId(module.index())`) indexes straight back into `docs` for text + url.
pub struct Analysis {
    /// The one incremental engine — held across requests (and `await`s), so
    /// memoised queries survive keystrokes.
    db: SkyDatabase,
    /// The `SourceFile` input backing each module, parallel to `docs` (index ==
    /// `ModuleId` == span `FileId`); editing calls `set_text` on `files[i]`.
    files: Vec<SourceFile>,
    /// Documents in insertion order; the index doubles as the db's
    /// `ModuleId`/`FileId` (register-in-order is deterministic — L4). Holds the
    /// text + url + eager parse for span↔position conversion and token walks.
    docs: Vec<Doc>,
    /// Module name → index in `docs` (dedup so indices stay 1:1 with modules).
    by_name: HashMap<String, usize>,
    /// Canonical fs-path key → index (request routing; urls agree after
    /// canonicalisation regardless of encoding differences).
    by_path: HashMap<String, usize>,
    stdlib_loaded: bool,
}

impl Default for Analysis {
    fn default() -> Self {
        Analysis::new()
    }
}

impl Analysis {
    pub fn new() -> Self {
        Analysis::from_db(SkyDatabase::with_kernel())
    }

    /// Test seam: an engine whose salsa db mirrors every event `kind` into `sink`,
    /// so an integration test can assert WHICH queries re-execute after an edit
    /// (the incrementality proof) — the LSP peer of `skydb`'s
    /// `with_kernel_events`. Behaviour is otherwise identical to [`Analysis::new`].
    pub fn with_event_log(sink: Arc<Mutex<Vec<String>>>) -> Self {
        Analysis::from_db(SkyDatabase::with_kernel_events(sink))
    }

    fn from_db(db: SkyDatabase) -> Self {
        Analysis {
            db,
            files: Vec::new(),
            docs: Vec::new(),
            by_name: HashMap::new(),
            by_path: HashMap::new(),
            stdlib_loaded: false,
        }
    }

    /// The persistent salsa db as the query interface every feature runs on.
    fn db(&self) -> &SkyDatabase {
        &self.db
    }

    // ---- loading -------------------------------------------------------

    /// Register (or update) one source under a url. Idempotent per module name:
    /// a re-add (didChange) updates the module in place, keeping its index so
    /// spans (which carry a `FileId` == index) stay valid.
    ///
    /// The salsa payoff lives here: on an UPDATE we `set_source_text` the existing
    /// module's `SourceFile` input — the sole mutation — so salsa invalidates only
    /// that module's `parse` and its transitive dependents; an unrelated module's
    /// memoised `resolve`/`infer` is untouched. On a NEW module we mint a fresh
    /// input and register it (position == `ModuleId` == span `FileId`).
    pub fn set_document(&mut self, url: Url, text: String) {
        let parse = syntax::parse(&text, FileId(0));
        let name = module_name(&parse, url_key(&url).as_deref());
        let idx = match self.by_name.get(&name) {
            Some(&i) => {
                // In-place edit: mutate the salsa input (incremental), then the
                // eager Doc bookkeeping (text/url/parse for position + tokens).
                self.db.set_source_text(self.files[i], text.clone());
                self.docs[i] = Doc {
                    parse,
                    text,
                    url: url.clone(),
                };
                i
            }
            None => {
                let i = self.docs.len();
                // New module: a fresh SourceFile input, registered under `name`
                // so `ModuleId(i)` binds to it; `file_id == i` keeps span
                // `FileId(module.index())` indexing straight back into `docs`.
                let file = self.db.new_source(i as u32, text.clone());
                self.db.add_module(&name, file);
                self.files.push(file);
                self.docs.push(Doc {
                    parse,
                    text,
                    url: url.clone(),
                });
                self.by_name.insert(name, i);
                i
            }
        };
        if let Some(p) = url_key(&url) {
            self.by_path.insert(p, idx);
        }
    }

    /// Load the stdlib (kernel-module Sky source) so cross-module + kernel
    /// signatures resolve. Located via `SKY_STDLIB_DIR`, else an upward search
    /// for a `sky-stdlib` directory from `root` / the cwd.
    pub fn load_stdlib(&mut self, root: Option<&Path>) {
        if self.stdlib_loaded {
            return;
        }
        if let Some(dir) = stdlib_dir(root) {
            self.load_dir(&dir);
            self.stdlib_loaded = true;
        }
    }

    /// Load every `.sky` under a project's `src/` + `tests/` so sibling-module
    /// imports resolve even before the editor opens them.
    pub fn load_project(&mut self, root: &Path) {
        for sub in ["src", "tests"] {
            self.load_dir(&root.join(sub));
        }
    }

    fn load_dir(&mut self, dir: &Path) {
        let mut files = Vec::new();
        collect_sky(dir, &mut files);
        for path in files {
            let Ok(text) = std::fs::read_to_string(&path) else {
                continue;
            };
            if let Ok(url) = Url::from_file_path(&path) {
                self.set_document(url, text);
            }
        }
    }

    // ---- request routing ----------------------------------------------

    fn index_of(&self, url: &Url) -> Option<usize> {
        let key = url_key(url)?;
        self.by_path.get(&key).copied()
    }

    fn text_at(&self, file: u32) -> Option<&str> {
        self.docs.get(file as usize).map(|d| d.text.as_str())
    }

    fn slice(&self, span: Span) -> &str {
        match self.text_at(span.file.index()) {
            Some(t) => t
                .get(span.range.0 as usize..span.range.1 as usize)
                .unwrap_or(""),
            None => "",
        }
    }

    // ---- hover ---------------------------------------------------------

    pub fn hover(&self, url: &Url, pos: Position) -> Option<Hover> {
        let idx = self.index_of(url)?;
        let text = &self.docs[idx].text;
        let off = offset_of(text, pos)?;
        let db = self.db();
        let module = ModuleId(idx as u32);
        let resolved = db.resolve(module);
        let cand = self.cand_at(db, &resolved, module, off)?;
        let typer = Typer::new(db);
        let md = match cand {
            Cand::Ref(o) => self.hover_ref(&typer, &resolved, o),
            Cand::Field(o) => self.hover_field(&typer, &resolved, o),
            Cand::Type(o) => self.hover_type(o),
            Cand::Def { def, .. } => self.hover_def(&typer, &resolved, db, def),
        }?;
        Some(Hover {
            contents: HoverContents::Markup(MarkupContent {
                kind: MarkupKind::Markdown,
                value: md,
            }),
            range: None,
        })
    }

    fn hover_ref(&self, typer: &Typer, resolved: &ResolveResult, o: &RefOcc) -> Option<String> {
        let name = self.slice(o.span).trim().to_string();
        let ty = self
            .ref_type_string(typer, resolved, o)
            .unwrap_or_else(|| "?".to_string());
        Some(format!("```sky\n{name} : {ty}\n```"))
    }

    /// The rendered type of a reference occurrence — the shared core of hover and
    /// signature help. `Res::Def` prefers the declared signature, then the
    /// pass-3 inferred scheme, then the body-inferred result; `Res::Kernel` /
    /// `Res::Ctor` read their scheme; `Res::Local` reads the owning body's
    /// per-local table (doc 10 §"resolve at call head → infer signature").
    fn ref_type_string(&self, typer: &Typer, resolved: &ResolveResult, o: &RefOcc) -> Option<String> {
        match &o.res {
            Res::Def(d) => self.def_sig_string(typer, resolved, *d),
            Res::Kernel { module, func } => typer
                .kernel_sig(module.as_str(), func.as_str())
                .map(|s| s.ty.render()),
            Res::Ctor(cr) => typer.ctor_sig_by_def(cr.def).map(|s| s.ty.render()),
            Res::Local(l) => {
                let body = resolved.bodies.get(&o.owner)?;
                typer
                    .body_types_annotated(o.owner, body)
                    .locals
                    .get(l)
                    .map(|t| t.render())
            }
            Res::Foreign { .. } | Res::Error => None,
        }
    }

    /// The rendered type of a top-level value `def`: its declared signature, then
    /// the pass-3 inferred scheme, then the body-inferred FULL signature (the arrow
    /// spine — `bt.signature`), falling back to the body-root `result` only if the
    /// signature is somehow absent. Using `signature` (not `result`) is what makes
    /// an unannotated function hover as `Int -> Int -> Int` rather than the
    /// body-result-only `Int` (bug (b)). Shared by `ref_type_string` (use sites) +
    /// `hover_def` (declaration sites).
    fn def_sig_string(&self, typer: &Typer, resolved: &ResolveResult, d: DefId) -> Option<String> {
        typer
            .value_sig(d)
            .or_else(|| typer.inferred_sig(d))
            .map(|s| s.ty.render())
            .or_else(|| {
                let body = resolved.bodies.get(&d)?;
                let bt = typer.body_types_annotated(d, body);
                bt.signature.or(bt.result).map(|t| t.render())
            })
    }

    fn hover_field(&self, typer: &Typer, resolved: &ResolveResult, o: &FieldOcc) -> Option<String> {
        let name = self.slice(o.span).trim().to_string();
        let body = resolved.bodies.get(&o.owner)?;
        let bt = typer.body_types_annotated(o.owner, body);
        let recv = bt.exprs.get(&o.receiver)?;
        let s = record_field(recv, o.field.as_str())
            .map(|t| t.render())
            .unwrap_or_else(|| "?".to_string());
        Some(format!("```sky\n{name} : {s}\n```"))
    }

    fn hover_type(&self, o: &TypeOcc) -> Option<String> {
        Some(format!("```sky\ntype {}\n```", o.name.as_str()))
    }

    /// Hover for a cursor ON a declaration name (bug (a)). A value renders the
    /// same `name : ty` a use site would (via `def_sig_string`); a constructor
    /// renders its scheme; a type-con / alias renders `type Name`. Mirrors the
    /// hover a use of the symbol produces.
    fn hover_def(
        &self,
        typer: &Typer,
        resolved: &ResolveResult,
        db: &dyn SkyDb,
        def: DefId,
    ) -> Option<String> {
        let loc = db.def_loc(def)?;
        let name = loc.name.as_str().to_string();
        let md = match loc.kind {
            DefKind::Value => {
                let ty = self
                    .def_sig_string(typer, resolved, def)
                    .unwrap_or_else(|| "?".to_string());
                format!("```sky\n{name} : {ty}\n```")
            }
            DefKind::Ctor => {
                let ty = typer
                    .ctor_sig_by_def(def)
                    .map(|s| s.ty.render())
                    .unwrap_or_else(|| "?".to_string());
                format!("```sky\n{name} : {ty}\n```")
            }
            DefKind::TypeCon | DefKind::TypeAlias => format!("```sky\ntype {name}\n```"),
        };
        Some(md)
    }

    // ---- goto-definition ----------------------------------------------

    pub fn goto(&self, url: &Url, pos: Position) -> Option<Location> {
        let idx = self.index_of(url)?;
        let text = &self.docs[idx].text;
        let off = offset_of(text, pos)?;
        let db = self.db();
        let module = ModuleId(idx as u32);
        let resolved = db.resolve(module);
        let cand = self.cand_at(db, &resolved, module, off)?;
        let span = match cand {
            // On a declaration name → jump to the definition site (from an
            // annotation `foo : T` this jumps to the `foo =` value site; on the
            // value/type/ctor name itself it is the decl's own span). (bug (a))
            Cand::Def { def, .. } => def_span(db, &resolved, module, def),
            Cand::Ref(o) => match &o.res {
                Res::Def(d) => def_span(db, &resolved, module, *d),
                Res::Ctor(cr) => def_span(db, &resolved, module, cr.def),
                Res::Local(l) => resolved
                    .binders
                    .iter()
                    .find(|b| b.owner == o.owner && b.local == *l)
                    .map(|b| b.span),
                Res::Kernel { .. } | Res::Foreign { .. } | Res::Error => None,
            },
            Cand::Type(o) => def_span(db, &resolved, module, o.con),
            Cand::Field(o) => {
                let typer = Typer::new(db);
                let recv_fields = receiver_fields(&typer, &resolved, o.owner, o.receiver);
                field_span(&resolved, o.field.as_str(), recv_fields.as_deref())
            }
        }?;
        self.location(span)
    }

    fn location(&self, span: Span) -> Option<Location> {
        let doc = self.docs.get(span.file.index() as usize)?;
        Some(Location {
            uri: doc.url.clone(),
            range: span_to_range(&doc.text, span),
        })
    }

    // ---- completion ----------------------------------------------------

    pub fn completion(&self, url: &Url, pos: Position) -> Vec<CompletionItem> {
        let Some(idx) = self.index_of(url) else {
            return Vec::new();
        };
        let text = &self.docs[idx].text;
        let Some(off) = offset_of(text, pos) else {
            return Vec::new();
        };
        let before = &text[..off as usize];
        let db = self.db();
        let module = ModuleId(idx as u32);
        let resolved = db.resolve(module);

        if let Some((recv, _partial)) = split_qualified(before) {
            // Qualified module member: `M.` → enumerate M's exports.
            if let Some(ImportSource::Dep(dep)) = resolved.qualifiers.get(&recv) {
                return module_completion(db, *dep, &recv);
            }
            // Record field: `record.` → the receiver type's fields.
            if let Some(items) = self.field_completion(db, &resolved, &recv, off as usize) {
                return items;
            }
            return Vec::new();
        }

        self.scope_completion(&resolved)
    }

    fn field_completion(
        &self,
        db: &dyn TyDb,
        resolved: &ResolveResult,
        recv: &str,
        off: usize,
    ) -> Option<Vec<CompletionItem>> {
        // The nearest same-named reference occurrence ending before the cursor
        // is the receiver we are completing on.
        let cand = resolved
            .ref_occs
            .iter()
            .filter(|o| self.slice(o.span) == recv && (o.span.range.1 as usize) <= off)
            .max_by_key(|o| o.span.range.1)?;
        let typer = Typer::new(db);
        let ty = match &cand.res {
            Res::Local(l) => {
                let body = resolved.bodies.get(&cand.owner)?;
                typer.body_types_annotated(cand.owner, body).locals.get(l).cloned()
            }
            Res::Def(d) => typer.value_sig(*d).map(|s| s.ty.clone()),
            _ => None,
        }?;
        if let Ty::Record(fields, _) = ty {
            return Some(
                fields
                    .iter()
                    .map(|(n, _)| plain_item(n.as_str(), CompletionItemKind::FIELD))
                    .collect(),
            );
        }
        None
    }

    fn scope_completion(&self, resolved: &ResolveResult) -> Vec<CompletionItem> {
        let mut seen: HashSet<String> = HashSet::new();
        let mut items = Vec::new();
        for b in &resolved.binders {
            let n = self.slice(b.span).to_string();
            if !n.is_empty() && seen.insert(n.clone()) {
                items.push(plain_item(&n, CompletionItemKind::VARIABLE));
            }
        }
        for td in &resolved.top_defs {
            let n = td.name.as_str().to_string();
            if !n.is_empty() && seen.insert(n.clone()) {
                items.push(plain_item(&n, CompletionItemKind::FUNCTION));
            }
        }
        for q in resolved.qualifiers.keys() {
            if seen.insert(q.clone()) {
                items.push(plain_item(q, CompletionItemKind::MODULE));
            }
        }
        items
    }

    // ---- diagnostics ---------------------------------------------------

    pub fn diagnostics(&self, url: &Url) -> Vec<Diagnostic> {
        let Some(idx) = self.index_of(url) else {
            return Vec::new();
        };
        let text = &self.docs[idx].text;
        let db = self.db();
        let module = ModuleId(idx as u32);
        let mut out: Vec<diagnostics::Diagnostic> = Vec::new();
        out.extend(db.module_parse(module).errors().iter().cloned());
        let resolved = db.resolve(module);
        out.extend(resolved.diagnostics.iter().cloned());
        let checked = ty::check_modules(db, &[module]);
        out.extend(checked.diagnostics);
        out.into_iter().map(|d| to_lsp_diag(&self.docs, text, &d)).collect()
    }

    // ---- references / rename target resolution -------------------------

    /// Resolve the cursor to a rename/reference target plus the *name* span at
    /// the cursor (already narrowed to the identifier token, so a qualified
    /// `M.foo` reports only `foo`). Reuses the same occurrence index + candidate
    /// selection hover/goto use — no new index (doc 10 §references/rename).
    /// The candidate the cursor resolves to. `best_candidate` covers the four
    /// resolved channels (uses + `def_spans` declaration names); this adds the one
    /// site the resolver does not index — a top-level `foo : T` annotation name —
    /// as a `Cand::Def` fallback, so hover/goto/references/rename answer there too
    /// (bug (a)). The annotation site is only consulted when nothing else matched,
    /// so an overlapping use/decl (there is none in practice) still wins.
    fn cand_at<'r>(
        &self,
        db: &dyn SkyDb,
        resolved: &'r ResolveResult,
        module: ModuleId,
        off: u32,
    ) -> Option<Cand<'r>> {
        if let Some(c) = best_candidate(resolved, off) {
            return Some(c);
        }
        self.annotation_decl_at(db, module, off)
            .map(|(def, span)| Cand::Def { def, span })
    }

    /// If `off` sits on a top-level `foo : T` annotation NAME token, return the
    /// value def it names plus the token span. The annotation shares the value
    /// def's identity but is not in `def_spans` (which records the `foo =` site),
    /// so we walk the parse tree and map the name back to its `DefId` via the
    /// module's `def_spans` (a value def of the same name). Bug (a) resolution
    /// counterpart of the collection-side `annotation_name_spans`.
    fn annotation_decl_at(
        &self,
        db: &dyn SkyDb,
        module: ModuleId,
        off: u32,
    ) -> Option<(DefId, Span)> {
        let tree = db.module_parse(module);
        for node in tree.syntax().children() {
            if node.kind() != SyntaxKind::TypeAnnoDecl {
                continue;
            }
            let Some(tok) = node
                .children_with_tokens()
                .filter_map(|e| e.into_token())
                .find(|t| t.kind() == SyntaxKind::LowerIdent)
            else {
                continue;
            };
            let r = tok.text_range();
            let (s, e) = (u32::from(r.start()), u32::from(r.end()));
            if off < s || off > e {
                continue;
            }
            let name = tok.text();
            let resolved = db.resolve(module);
            for (id, _) in &resolved.def_spans {
                if let Some(loc) = db.def_loc(*id) {
                    if loc.module == module
                        && loc.kind == DefKind::Value
                        && loc.name.as_str() == name
                    {
                        return Some((*id, Span::new(FileId(module.index()), s, e)));
                    }
                }
            }
            return None;
        }
        None
    }

    fn resolve_target(
        &self,
        db: &dyn SkyDb,
        resolved: &ResolveResult,
        module: ModuleId,
        off: u32,
    ) -> Option<(Target, Span)> {
        let cand = self.cand_at(db, resolved, module, off)?;
        match cand {
            // Cursor ON a declaration name → the same `Target::Global(def)` a use
            // site resolves to, so references/rename from the decl == from a use.
            Cand::Def { def, span } => Some((Target::Global(def), span)),
            Cand::Ref(o) => {
                let name_span = self.narrow_to_name(o.span);
                let t = match &o.res {
                    Res::Def(d) => Target::Global(*d),
                    Res::Ctor(cr) => Target::Global(cr.def),
                    Res::Local(l) => Target::Local {
                        owner: o.owner,
                        local: *l,
                    },
                    Res::Kernel { module, func } => Target::Kernel {
                        module: module.as_str().to_string(),
                        func: func.as_str().to_string(),
                    },
                    Res::Foreign { .. } | Res::Error => return None,
                };
                Some((t, name_span))
            }
            Cand::Type(o) => Some((Target::Global(o.con), o.span)),
            Cand::Field(o) => Some((Target::Field(o.field.as_str().to_string()), o.span)),
        }
    }

    /// The kind of a `DefId` (value / type / ctor), for rename validation +
    /// symbol classification. `None` when the def is a builtin (Prelude) — those
    /// are not renameable.
    fn def_kind(&self, db: &dyn SkyDb, d: DefId) -> Option<DefKind> {
        let loc = db.def_loc(d)?;
        if loc.module.index() == u32::MAX {
            return None; // builtin/Prelude — no rename
        }
        Some(loc.kind)
    }

    /// Narrow an occurrence span down to its trailing identifier token, so a
    /// qualified reference (`M.foo`, recorded as the whole `QualRefExpr` node)
    /// yields just the `foo` token's span. Simple references already are the
    /// bare token, so we only pay the tree walk when the span text contains `.`.
    fn narrow_to_name(&self, span: Span) -> Span {
        if !self.slice(span).contains('.') {
            return span;
        }
        let mi = span.file.index() as usize;
        let Some(doc) = self.docs.get(mi) else {
            return span;
        };
        let mut best = span;
        for el in doc.parse.syntax().descendants_with_tokens() {
            if let Some(t) = el.as_token() {
                if matches!(t.kind(), SyntaxKind::LowerIdent | SyntaxKind::UpperIdent) {
                    let r = t.text_range();
                    let (s, e) = (u32::from(r.start()), u32::from(r.end()));
                    if s >= span.range.0 && e <= span.range.1 {
                        best = Span::new(span.file, s, e); // last one within wins
                    }
                }
            }
        }
        best
    }

    /// Every occurrence span of `target` across the workspace. Declarations are
    /// included iff `include_decl`. Deterministic order: (file, start) sorted,
    /// deduplicated (L4).
    fn collect_occurrences(&self, db: &dyn SkyDb, target: &Target, include_decl: bool) -> Vec<Span> {
        let mut out: Vec<Span> = Vec::new();
        match target {
            Target::Local { owner, local } => {
                // Locals never escape their body → resolve only the owner's
                // module. The binder site is itself a `ref_occs` entry
                // (`record_binder`), so all uses + the declaration are covered.
                // NB: bind the module out of the borrow BEFORE calling
                // `hir::resolve` (which takes `defs().borrow_mut()`), else the
                // if-let temporary keeps the shared borrow alive across it.
                let owner_mod = db.def_loc(*owner).map(|l| l.module);
                if let Some(m) = owner_mod {
                    let r = db.resolve(m);
                    for o in &r.ref_occs {
                        if o.owner == *owner {
                            if let Res::Local(l) = &o.res {
                                if l == local {
                                    out.push(o.span);
                                }
                            }
                        }
                    }
                }
            }
            Target::Global(d) => {
                for mi in 0..self.docs.len() {
                    let m = ModuleId(mi as u32);
                    let r = db.resolve(m);
                    for o in &r.ref_occs {
                        if res_is_def(&o.res, *d) {
                            out.push(self.narrow_to_name(o.span));
                        }
                    }
                    for t in &r.type_occs {
                        if t.con == *d {
                            out.push(t.span);
                        }
                    }
                    if include_decl {
                        for (id, s) in &r.def_spans {
                            if id == d {
                                out.push(*s);
                            }
                        }
                        // A `foo : T` annotation name shares the def's identity
                        // but is a separate top-level decl not in `def_spans`;
                        // add it so rename touches the signature too.
                        for s in self.annotation_name_spans(db, m, *d) {
                            out.push(s);
                        }
                    }
                }
            }
            Target::Kernel { module, func } => {
                for mi in 0..self.docs.len() {
                    let m = ModuleId(mi as u32);
                    let r = db.resolve(m);
                    for o in &r.ref_occs {
                        if let Res::Kernel { module: km, func: kf } = &o.res {
                            if km.as_str() == module && kf.as_str() == func {
                                out.push(self.narrow_to_name(o.span));
                            }
                        }
                    }
                }
            }
            Target::Field(name) => {
                for mi in 0..self.docs.len() {
                    let m = ModuleId(mi as u32);
                    let r = db.resolve(m);
                    for o in &r.field_occs {
                        if o.field.as_str() == name {
                            out.push(o.span);
                        }
                    }
                    if include_decl {
                        for f in &r.field_decls {
                            if f.field.as_str() == name {
                                out.push(f.span);
                            }
                        }
                    }
                }
            }
        }
        out.sort_by_key(|s| (s.file.index(), s.range.0, s.range.1));
        out.dedup_by_key(|s| (s.file.index(), s.range.0, s.range.1));
        out
    }

    /// Top-level `name : T` annotation-name spans in module `m` whose identifier
    /// matches def `d`'s name — recovers the signature occurrence the resolver's
    /// `def_spans` (which records the `name =` value site) does not carry.
    fn annotation_name_spans(&self, db: &dyn SkyDb, m: ModuleId, d: DefId) -> Vec<Span> {
        let Some(loc) = db.def_loc(d) else {
            return Vec::new();
        };
        if loc.module != m || loc.kind != DefKind::Value {
            return Vec::new();
        }
        let name = loc.name.as_str().to_string();
        let mut out = Vec::new();
        for node in db.module_parse(m).syntax().children() {
            if node.kind() == SyntaxKind::TypeAnnoDecl {
                if let Some(tok) = node
                    .children_with_tokens()
                    .filter_map(|e| e.into_token())
                    .find(|t| t.kind() == SyntaxKind::LowerIdent)
                {
                    if tok.text() == name {
                        let r = tok.text_range();
                        out.push(Span::new(
                            FileId(m.index()),
                            u32::from(r.start()),
                            u32::from(r.end()),
                        ));
                    }
                }
            }
        }
        out
    }

    // ---- textDocument/references ---------------------------------------

    pub fn references(&self, url: &Url, pos: Position, include_decl: bool) -> Vec<Location> {
        let Some(idx) = self.index_of(url) else {
            return Vec::new();
        };
        let text = &self.docs[idx].text;
        let Some(off) = offset_of(text, pos) else {
            return Vec::new();
        };
        let db = self.db();
        let module = ModuleId(idx as u32);
        let resolved = db.resolve(module);
        let Some((target, _)) = self.resolve_target(db, &resolved, module, off) else {
            return Vec::new();
        };
        self.collect_occurrences(db, &target, include_decl)
            .into_iter()
            .filter_map(|s| self.location(s))
            .collect()
    }

    // ---- textDocument/prepareRename ------------------------------------

    pub fn prepare_rename(&self, url: &Url, pos: Position) -> Option<PrepareRenameResponse> {
        let idx = self.index_of(url)?;
        let text = &self.docs[idx].text;
        let off = offset_of(text, pos)?;
        let db = self.db();
        let module = ModuleId(idx as u32);
        let resolved = db.resolve(module);
        let (target, span) = self.resolve_target(db, &resolved, module, off)?;
        if !self.is_renameable(db, &target) {
            return None;
        }
        Some(PrepareRenameResponse::Range(span_to_range(text, span)))
    }

    fn is_renameable(&self, db: &dyn SkyDb, target: &Target) -> bool {
        match target {
            Target::Local { .. } => true,
            Target::Global(d) => self.def_kind(db, *d).is_some(),
            // A kernel/builtin function has no Sky definition site; a field
            // rename would need whole-program record-shape analysis to be safe.
            Target::Kernel { .. } | Target::Field(_) => false,
        }
    }

    // ---- textDocument/rename -------------------------------------------

    pub fn rename(&self, url: &Url, pos: Position, new_name: &str) -> Option<WorkspaceEdit> {
        let idx = self.index_of(url)?;
        let text = &self.docs[idx].text;
        let off = offset_of(text, pos)?;
        let db = self.db();
        let module = ModuleId(idx as u32);
        let resolved = db.resolve(module);
        let (target, _) = self.resolve_target(db, &resolved, module, off)?;
        if !self.is_renameable(db, &target) {
            return None;
        }
        // The new name must match the lexical class of what it replaces: values
        // + locals are lower-ident; types + constructors are upper-ident.
        let upper = match &target {
            Target::Global(d) => matches!(
                self.def_kind(db, *d),
                Some(DefKind::TypeCon | DefKind::TypeAlias | DefKind::Ctor)
            ),
            _ => false,
        };
        if !is_valid_ident(new_name, upper) {
            return None;
        }
        let spans = self.collect_occurrences(db, &target, true);
        if spans.is_empty() {
            return None;
        }
        // Group by document, sorted edits per file (deterministic — L4).
        let mut by_url: BTreeMap<String, (Url, Vec<TextEdit>)> = BTreeMap::new();
        for s in spans {
            let Some(doc) = self.docs.get(s.file.index() as usize) else {
                continue;
            };
            let range = span_to_range(&doc.text, s);
            let entry = by_url
                .entry(doc.url.to_string())
                .or_insert_with(|| (doc.url.clone(), Vec::new()));
            entry.1.push(TextEdit {
                range,
                new_text: new_name.to_string(),
            });
        }
        let mut changes: HashMap<Url, Vec<TextEdit>> = HashMap::new();
        for (_, (u, mut edits)) in by_url {
            edits.sort_by_key(|e| (e.range.start.line, e.range.start.character));
            changes.insert(u, edits);
        }
        Some(WorkspaceEdit {
            changes: Some(changes),
            ..Default::default()
        })
    }

    // ---- textDocument/documentSymbol -----------------------------------

    pub fn document_symbols(&self, url: &Url) -> Vec<DocumentSymbol> {
        let Some(idx) = self.index_of(url) else {
            return Vec::new();
        };
        let text = &self.docs[idx].text.clone();
        let db = self.db();
        let resolved = db.resolve(ModuleId(idx as u32));
        let mut out = Vec::new();
        for (d, span) in &resolved.def_spans {
            let Some(loc) = db.def_loc(*d) else {
                continue;
            };
            let (kind, keep) = match loc.kind {
                DefKind::Value => (SymbolKind::FUNCTION, true),
                DefKind::TypeCon => (SymbolKind::ENUM, true),
                DefKind::TypeAlias => (SymbolKind::STRUCT, true),
                DefKind::Ctor => (SymbolKind::ENUM_MEMBER, false), // nested under types
            };
            if !keep {
                continue;
            }
            let range = span_to_range(text, *span);
            #[allow(deprecated)]
            out.push(DocumentSymbol {
                name: loc.name.as_str().to_string(),
                detail: None,
                kind,
                tags: None,
                deprecated: None,
                range,
                selection_range: range,
                children: None,
            });
        }
        out
    }

    // ---- textDocument/semanticTokens/full ------------------------------

    pub fn semantic_tokens(&self, url: &Url) -> Option<SemanticTokensResult> {
        let idx = self.index_of(url)?;
        let text = self.docs[idx].text.clone();
        let db = self.db();
        let resolved = db.resolve(ModuleId(idx as u32));

        // Span-keyed classification from the occurrence index. First writer wins
        // (type/field beat the coarser ref span at the same key).
        let mut exact: HashMap<(u32, u32), u32> = HashMap::new();
        let mut ref_ranges: Vec<(u32, u32, u32)> = Vec::new();
        for o in &resolved.type_occs {
            exact.entry(o.span.range).or_insert(TOK_TYPE);
        }
        for o in &resolved.field_occs {
            exact.entry(o.span.range).or_insert(TOK_PROPERTY);
        }
        for o in &resolved.ref_occs {
            let tt = classify_res(&o.res);
            exact.entry(o.span.range).or_insert(tt);
            ref_ranges.push((o.span.range.0, o.span.range.1, tt));
        }
        for b in &resolved.binders {
            exact.entry(b.span.range).or_insert(TOK_PARAMETER);
        }
        for (d, s) in &resolved.def_spans {
            let tt = match db.def_loc(*d).map(|l| l.kind) {
                Some(DefKind::Value) => TOK_FUNCTION,
                Some(DefKind::TypeCon) | Some(DefKind::TypeAlias) => TOK_TYPE,
                Some(DefKind::Ctor) => TOK_ENUM_MEMBER,
                None => TOK_TYPE,
            };
            exact.entry(s.range).or_insert(tt);
        }

        let tokens: Vec<syntax::SyntaxToken> = resolved_tokens(db, ModuleId(idx as u32));
        let mut data: Vec<SemanticToken> = Vec::new();
        let (mut prev_line, mut prev_char) = (0u32, 0u32);
        for (i, t) in tokens.iter().enumerate() {
            let Some(tt) = classify_token(t, &tokens, i, &exact, &ref_ranges) else {
                continue;
            };
            let ttext = t.text();
            if ttext.contains('\n') {
                continue; // LSP tokens must not span lines
            }
            let r = t.text_range();
            let start = u32::from(r.start());
            let pos = position_at(&text, start);
            let length: u32 = ttext.chars().map(|c| c.len_utf16() as u32).sum();
            if length == 0 {
                continue;
            }
            let delta_line = pos.line - prev_line;
            let delta_start = if delta_line == 0 {
                pos.character - prev_char
            } else {
                pos.character
            };
            data.push(SemanticToken {
                delta_line,
                delta_start,
                length,
                token_type: tt,
                token_modifiers_bitset: 0,
            });
            prev_line = pos.line;
            prev_char = pos.character;
        }
        Some(SemanticTokensResult::Tokens(SemanticTokens {
            result_id: None,
            data,
        }))
    }

    // ---- textDocument/formatting ---------------------------------------

    /// Format the whole document via the shared `fmt::format_source` (the same
    /// entry point `sky fmt` uses — doc 10 §"`sky fmt` — exact formatter over
    /// the CST"). Returns a single whole-file replacement `TextEdit`, or an
    /// empty edit list when the buffer is already canonical (no client churn).
    /// `fmt` is lossless on broken input (L8), so this never throws.
    pub fn formatting(&self, url: &Url) -> Option<Vec<TextEdit>> {
        let idx = self.index_of(url)?;
        let text = &self.docs[idx].text;
        let formatted = fmt::format_source(text);
        if formatted == *text {
            return Some(Vec::new());
        }
        let end = position_at(text, text.len() as u32);
        Some(vec![TextEdit {
            range: Range {
                start: Position { line: 0, character: 0 },
                end,
            },
            new_text: formatted,
        }])
    }

    // ---- textDocument/inlayHint ----------------------------------------

    /// Inferred-type inlay hints on unannotated bindings (doc 10 §request→query
    /// map: `infer(def)` per-region types). Emits ` : T` after the name of every
    /// top-level value def that carries no `name : T` annotation (its
    /// `value_sig` is absent — typed by the body's inferred result), and after
    /// every `let name … = …` binding with no inline annotation (typed by the
    /// owning body's per-local table). Mirrors the Haskell server's
    /// `handleInlayHint` (Server.hs:2239) — kind `Type`, no left padding, label
    /// leads with " : ". `range` scopes the walk to the client's viewport.
    pub fn inlay_hints(&self, url: &Url, range: Range) -> Vec<InlayHint> {
        let Some(idx) = self.index_of(url) else {
            return Vec::new();
        };
        let text = &self.docs[idx].text.clone();
        let db = self.db();
        let module = ModuleId(idx as u32);
        let resolved = db.resolve(module);
        let typer = Typer::new(db);
        let lo = offset_of(text, range.start).unwrap_or(0);
        let hi = offset_of(text, range.end).unwrap_or(u32::MAX);
        // One body-types computation per owning def, shared across its binders.
        let mut body_cache: HashMap<DefId, ty::BodyTypes> = HashMap::new();
        let mut hints: Vec<InlayHint> = Vec::new();

        // Top-level unannotated value defs.
        for td in &resolved.top_defs {
            let def = td.def;
            if typer.value_sig(def).is_some() {
                continue; // has a declared signature — no hint
            }
            let Some((_, span)) = resolved.def_spans.iter().find(|(id, _)| *id == def) else {
                continue;
            };
            if span.range.1 < lo || span.range.0 > hi {
                continue;
            }
            let Some(body) = resolved.bodies.get(&def) else {
                continue;
            };
            let bt = body_cache
                .entry(def)
                .or_insert_with(|| typer.body_types_annotated(def, body));
            // Render the FULL signature (arrow spine) so an unannotated function
            // hints as `foo : Int -> Int -> Int`, not the body-root result alone
            // (bug (b)). For a nullary value `signature == result`, so value-def
            // hints are unchanged. Fall back to `result` only if absent.
            if let Some(ty) = bt.signature.clone().or_else(|| bt.result.clone()) {
                hints.push(type_hint(text, span.range.1, &ty.render()));
            }
        }

        // Let bindings without an inline annotation.
        let tree = db.module_parse(module);
        for node in tree.syntax().descendants() {
            let Some(let_expr) = ast::LetExpr::cast(node.clone()) else {
                continue;
            };
            // Names carried by an annotation binding (`x : T`) in this `let`;
            // an annotation binding has a type child but no value body.
            let annotated: HashSet<String> = let_expr
                .bindings()
                .filter(|b| b.body().is_none())
                .filter_map(|b| b.name().map(|t| t.text().to_string()))
                .collect();
            for b in let_expr.bindings() {
                if b.body().is_none() {
                    continue; // the annotation binding itself
                }
                let Some(tok) = b.name() else { continue };
                if annotated.contains(tok.text()) {
                    continue; // an explicit `x : T` already documents it
                }
                let r = tok.text_range();
                let (s, e) = (u32::from(r.start()), u32::from(r.end()));
                if e < lo || s > hi {
                    continue;
                }
                let Some(binder) = resolved.binders.iter().find(|bd| bd.span.range == (s, e)) else {
                    continue;
                };
                let Some(obody) = resolved.bodies.get(&binder.owner) else {
                    continue;
                };
                let bt = body_cache
                    .entry(binder.owner)
                    .or_insert_with(|| typer.body_types_annotated(binder.owner, obody));
                if let Some(ty) = bt.locals.get(&binder.local).cloned() {
                    hints.push(type_hint(text, e, &ty.render()));
                }
            }
        }

        hints.sort_by_key(|h| (h.position.line, h.position.character));
        hints
    }

    // ---- textDocument/signatureHelp ------------------------------------

    /// Parameter info for the call enclosing the cursor (doc 10 §request→query
    /// map: `resolve` at call head → `infer` signature). Finds the innermost
    /// `CallExpr` whose range contains the cursor but whose callee head does
    /// not (i.e. the cursor is in argument territory), resolves the head to its
    /// signature, and reports the active-parameter index. Mirrors the Haskell
    /// server's `handleSignatureHelp` (Server.hs:2963).
    pub fn signature_help(&self, url: &Url, pos: Position) -> Option<SignatureHelp> {
        let idx = self.index_of(url)?;
        let text = &self.docs[idx].text;
        let off = offset_of(text, pos)?;
        let db = self.db();
        let module = ModuleId(idx as u32);
        let resolved = db.resolve(module);
        let tree = db.module_parse(module);

        // The innermost enclosing call whose head ends before the cursor.
        let (head_off, active) = enclosing_call(&tree.syntax(), off)?;

        // Resolve the head identifier to its type via the same channels hover uses.
        let head_occ = resolved
            .ref_occs
            .iter()
            .filter(|o| contains(o.span, head_off))
            .min_by_key(|o| span_len(o.span))?;
        let name = self.slice(self.narrow_to_name(head_occ.span)).trim().to_string();
        let typer = Typer::new(db);
        let ty = self.ref_type_string(&typer, &resolved, head_occ)?;

        Some(build_signature(&name, &ty, active))
    }
}

// ---- references / rename / semantic-token support ---------------------

/// What a cursor resolves to for references + rename (doc 10). `Global` folds
/// value / constructor / type into one `DefId` — equality alone identifies the
/// symbol (distinct `DefKind`s never share an id).
enum Target {
    Global(DefId),
    Local { owner: DefId, local: LocalId },
    Kernel { module: String, func: String },
    Field(String),
}

/// Does resolution `res` name top-level def `d` (value or constructor)?
fn res_is_def(res: &Res, d: DefId) -> bool {
    match res {
        Res::Def(x) => *x == d,
        Res::Ctor(cr) => cr.def == d,
        _ => false,
    }
}

/// A legal Sky identifier for rename: lower-ident for values/locals, upper-ident
/// for types/constructors. First char sets the class; the rest is `[A-Za-z0-9_]`;
/// keywords are rejected.
fn is_valid_ident(name: &str, upper: bool) -> bool {
    let mut chars = name.chars();
    let Some(first) = chars.next() else {
        return false;
    };
    let head_ok = if upper {
        first.is_ascii_uppercase()
    } else {
        first.is_ascii_lowercase() || first == '_'
    };
    if !head_ok {
        return false;
    }
    if !chars.all(|c| c.is_ascii_alphanumeric() || c == '_') {
        return false;
    }
    !matches!(
        name,
        "module" | "import" | "exposing" | "as" | "type" | "alias" | "foreign" | "if" | "then"
            | "else" | "case" | "of" | "let" | "in" | "True" | "False"
    )
}

// Semantic-token legend indices (doc 10 §semantic tokens — 12 types, frozen).
const TOK_NAMESPACE: u32 = 0;
const TOK_TYPE: u32 = 1;
const TOK_FUNCTION: u32 = 2;
const TOK_PARAMETER: u32 = 3;
const TOK_VARIABLE: u32 = 4;
const TOK_ENUM_MEMBER: u32 = 5;
const TOK_KEYWORD: u32 = 6;
const TOK_STRING: u32 = 7;
const TOK_NUMBER: u32 = 8;
const TOK_OPERATOR: u32 = 9;
const TOK_COMMENT: u32 = 10;
const TOK_PROPERTY: u32 = 11;

/// The 12-type semantic-token legend, in index order (matches `TOK_*`).
pub fn semantic_legend() -> SemanticTokensLegend {
    SemanticTokensLegend {
        token_types: vec![
            SemanticTokenType::NAMESPACE,
            SemanticTokenType::TYPE,
            SemanticTokenType::FUNCTION,
            SemanticTokenType::PARAMETER,
            SemanticTokenType::VARIABLE,
            SemanticTokenType::ENUM_MEMBER,
            SemanticTokenType::KEYWORD,
            SemanticTokenType::STRING,
            SemanticTokenType::NUMBER,
            SemanticTokenType::OPERATOR,
            SemanticTokenType::COMMENT,
            SemanticTokenType::PROPERTY,
        ],
        token_modifiers: vec![],
    }
}

fn classify_res(res: &Res) -> u32 {
    match res {
        Res::Local(_) => TOK_PARAMETER,
        Res::Def(_) | Res::Kernel { .. } | Res::Foreign { .. } => TOK_FUNCTION,
        Res::Ctor(_) => TOK_ENUM_MEMBER,
        Res::Error => TOK_VARIABLE,
    }
}

/// Every token of module `m` in document order (trivia included — comments carry
/// a semantic-token class).
fn resolved_tokens(db: &dyn SkyDb, m: ModuleId) -> Vec<syntax::SyntaxToken> {
    db.module_parse(m)
        .syntax()
        .descendants_with_tokens()
        .filter_map(|e| e.into_token())
        .collect()
}

/// The next non-trivia token after index `i`.
fn next_significant(tokens: &[syntax::SyntaxToken], i: usize) -> Option<&syntax::SyntaxToken> {
    tokens[i + 1..].iter().find(|t| !t.kind().is_trivia())
}

fn classify_token(
    t: &syntax::SyntaxToken,
    tokens: &[syntax::SyntaxToken],
    i: usize,
    exact: &HashMap<(u32, u32), u32>,
    ref_ranges: &[(u32, u32, u32)],
) -> Option<u32> {
    use SyntaxKind::*;
    match t.kind() {
        Whitespace | Newline => None,
        LineComment | BlockComment => Some(TOK_COMMENT),
        String | MultilineString | Char | StringChunk => Some(TOK_STRING),
        Int | HexInt | Float => Some(TOK_NUMBER),
        ModuleKw | ExposingKw | ImportKw | AsKw | TypeKw | AliasKw | ForeignKw | IfKw | ThenKw
        | ElseKw | CaseKw | OfKw | LetKw | InKw | TrueKw | FalseKw => Some(TOK_KEYWORD),
        Op | Arrow | Backslash | Pipe | DotDot | Eq | Colon | Colon2 => Some(TOK_OPERATOR),
        LowerIdent | UpperIdent => {
            // A module qualifier (`M.`) is a namespace, not the thing after it.
            if t.kind() == UpperIdent
                && next_significant(tokens, i).map(|n| n.kind() == Dot).unwrap_or(false)
            {
                return Some(TOK_NAMESPACE);
            }
            let r = t.text_range();
            let key = (u32::from(r.start()), u32::from(r.end()));
            if let Some(tt) = exact.get(&key) {
                return Some(*tt);
            }
            // Fall back to the smallest containing ref occurrence (a qualified
            // ref's tail identifier lives inside the whole-`QualRef` span).
            if let Some((_, _, tt)) = ref_ranges
                .iter()
                .filter(|(s, e, _)| *s <= key.0 && key.1 <= *e)
                .min_by_key(|(s, e, _)| e - s)
            {
                return Some(*tt);
            }
            Some(if t.kind() == UpperIdent {
                TOK_TYPE
            } else {
                TOK_VARIABLE
            })
        }
        _ => None,
    }
}

// ---- free helpers over a rebuilt db -----------------------------------

/// Declaration span of a def — this module first, else the def's home module
/// (cross-file goto for free: `DefId` carries its module via the interner).
fn def_span(db: &dyn SkyDb, resolved: &ResolveResult, this: ModuleId, d: DefId) -> Option<Span> {
    if let Some((_, s)) = resolved.def_spans.iter().find(|(id, _)| *id == d) {
        return Some(*s);
    }
    let loc = db.def_loc(d)?;
    if loc.module == this || loc.module.index() == u32::MAX {
        return None;
    }
    let other = db.resolve(loc.module);
    other
        .def_spans
        .iter()
        .find(|(id, _)| *id == d)
        .map(|(_, s)| *s)
}

fn field_span(resolved: &ResolveResult, field: &str, recv_fields: Option<&[String]>) -> Option<Span> {
    let cands: Vec<&hir::FieldDecl> = resolved
        .field_decls
        .iter()
        .filter(|f| f.field.as_str() == field)
        .collect();
    if cands.is_empty() {
        return None;
    }
    if let Some(rf) = recv_fields {
        if let Some(f) = cands
            .iter()
            .find(|f| rf.iter().all(|n| f.siblings.iter().any(|s| s.as_str() == n)))
        {
            return Some(f.span);
        }
    }
    Some(cands[0].span)
}

fn receiver_fields(
    typer: &Typer,
    resolved: &ResolveResult,
    owner: DefId,
    receiver: hir::ExprId,
) -> Option<Vec<String>> {
    let body = resolved.bodies.get(&owner)?;
    let bt = typer.body_types_annotated(owner, body);
    match bt.exprs.get(&receiver)? {
        Ty::Record(fields, _) => Some(fields.iter().map(|(n, _)| n.as_str().to_string()).collect()),
        _ => None,
    }
}

fn module_completion(db: &dyn SkyDb, dep: ModuleId, recv: &str) -> Vec<CompletionItem> {
    let exports = db.module_exports(dep);
    let mut items = Vec::new();
    for (name, _) in &exports.values {
        items.push(qualified_item(recv, name.as_str()));
    }
    for u in &exports.unions {
        items.push(qualified_item(recv, u.name.as_str()));
        for c in &u.ctors {
            items.push(qualified_item(recv, c.name.as_str()));
        }
    }
    for a in &exports.aliases {
        items.push(qualified_item(recv, a.name.as_str()));
    }
    items
}

// ---- candidate selection ----------------------------------------------

enum Cand<'a> {
    Ref(&'a RefOcc),
    Field(&'a FieldOcc),
    Type(&'a TypeOcc),
    /// The cursor sits ON a declaration name (a value `foo =` site, a type/alias
    /// con, or a constructor) — resolved from `def_spans`, or a top-level
    /// annotation `foo : T` name (recovered separately). Maps to the SAME
    /// `Target::Global(def)` a use site of the symbol resolves to, so
    /// hover/goto/references/rename all answer from the declaration too (bug (a)).
    Def { def: DefId, span: Span },
}

/// The smallest span containing `off` across the occurrence channels — smallest
/// wins so a `.field` beats its enclosing receiver, etc. Scans the three use
/// channels (`field_occs`/`ref_occs`/`type_occs`) AND the declaration-name
/// channel (`def_spans`) so a cursor ON a decl name resolves (bug (a)); the
/// annotation-name site needs `db` + the parse tree and is handled by `cand_at`.
fn best_candidate(resolved: &ResolveResult, off: u32) -> Option<Cand<'_>> {
    let mut best: Option<(u32, Cand<'_>)> = None;
    for o in &resolved.field_occs {
        if contains(o.span, off) {
            let len = span_len(o.span);
            if best.as_ref().map(|(l, _)| len < *l).unwrap_or(true) {
                best = Some((len, Cand::Field(o)));
            }
        }
    }
    for o in &resolved.ref_occs {
        if contains(o.span, off) {
            let len = span_len(o.span);
            if best.as_ref().map(|(l, _)| len < *l).unwrap_or(true) {
                best = Some((len, Cand::Ref(o)));
            }
        }
    }
    for o in &resolved.type_occs {
        if contains(o.span, off) {
            let len = span_len(o.span);
            if best.as_ref().map(|(l, _)| len < *l).unwrap_or(true) {
                best = Some((len, Cand::Type(o)));
            }
        }
    }
    for (d, span) in &resolved.def_spans {
        if contains(*span, off) {
            let len = span_len(*span);
            if best.as_ref().map(|(l, _)| len < *l).unwrap_or(true) {
                best = Some((len, Cand::Def { def: *d, span: *span }));
            }
        }
    }
    best.map(|(_, c)| c)
}

fn contains(span: Span, off: u32) -> bool {
    off >= span.range.0 && off <= span.range.1
}
fn span_len(span: Span) -> u32 {
    span.range.1.saturating_sub(span.range.0)
}

fn record_field(ty: &Ty, field: &str) -> Option<Ty> {
    match ty {
        Ty::Record(fields, _) => fields
            .iter()
            .find(|(n, _)| n.as_str() == field)
            .map(|(_, t)| t.clone()),
        _ => None,
    }
}

// ---- completion-item constructors -------------------------------------

fn qualified_item(qualifier: &str, name: &str) -> CompletionItem {
    // label carries the `M.name` shown to the user; insertText is the bare name
    // so accepting after `M.` does NOT double the prefix (nvim:
    // completion-qualified-insert-text).
    CompletionItem {
        label: format!("{qualifier}.{name}"),
        insert_text: Some(name.to_string()),
        kind: Some(CompletionItemKind::FUNCTION),
        ..Default::default()
    }
}

fn plain_item(name: &str, kind: CompletionItemKind) -> CompletionItem {
    CompletionItem {
        label: name.to_string(),
        insert_text: Some(name.to_string()),
        kind: Some(kind),
        ..Default::default()
    }
}

// ---- inlay hint / signature help support ------------------------------

/// A `Type`-kind inlay hint ` : T` rendered at byte-offset `off` (no left
/// padding — the leading space is in the label, matching the Haskell server).
fn type_hint(text: &str, off: u32, ty: &str) -> InlayHint {
    InlayHint {
        position: position_at(text, off),
        label: InlayHintLabel::String(format!(" : {ty}")),
        kind: Some(InlayHintKind::TYPE),
        text_edits: None,
        tooltip: None,
        padding_left: Some(false),
        padding_right: Some(false),
        data: None,
    }
}

/// The innermost `CallExpr` enclosing byte-offset `off` whose callee head ends
/// before `off` (the cursor is in argument territory). Returns the head's start
/// offset (for resolution) and the active-parameter index (count of arguments
/// fully typed before the cursor). Calls are flat (`app_expr` — `parts[0]` is
/// the callee, the rest are arguments), so no nesting walk is needed.
fn enclosing_call(root: &syntax::SyntaxNode, off: u32) -> Option<(u32, u32)> {
    let mut best: Option<(u32, u32, u32)> = None; // (width, head_start, active)
    for node in root.descendants() {
        let Some(call) = ast::CallExpr::cast(node) else {
            continue;
        };
        let r = call.syntax().text_range();
        let (cs, ce) = (u32::from(r.start()), u32::from(r.end()));
        if off < cs || off > ce {
            continue;
        }
        let parts = call.parts();
        let Some(head) = parts.first() else {
            continue;
        };
        let hr = head.syntax().text_range();
        let (hs, he) = (u32::from(hr.start()), u32::from(hr.end()));
        if off < he {
            continue; // still inside the callee head — not yet in args
        }
        // Active parameter: arguments whose extent ends at/before the cursor.
        let active = parts[1..]
            .iter()
            .filter(|a| u32::from(a.syntax().text_range().end()) <= off)
            .count() as u32;
        let width = ce - cs;
        if best.as_ref().map(|(w, _, _)| width < *w).unwrap_or(true) {
            best = Some((width, hs, active));
        }
    }
    best.map(|(_, h, a)| (h, a))
}

/// Build the `SignatureHelp` for `name : ty`, splitting `ty` at top-level
/// arrows into per-parameter label offsets (the return-type tail is not a
/// parameter). `active` is clamped into range. Mirrors `mkSignature`
/// (Server.hs:3098).
fn build_signature(name: &str, ty: &str, active: u32) -> SignatureHelp {
    let label = format!("{name} : {ty}");
    let base = name.chars().count() + 3; // "name" + " : "
    let slots = param_slots(ty);
    let params: Vec<ParameterInformation> = slots
        .iter()
        .map(|(s, e)| ParameterInformation {
            label: ParameterLabel::LabelOffsets([(base + s) as u32, (base + e) as u32]),
            documentation: None,
        })
        .collect();
    let nparams = params.len() as u32;
    let active = if nparams == 0 {
        0
    } else {
        active.min(nparams - 1)
    };
    SignatureHelp {
        signatures: vec![SignatureInformation {
            label,
            documentation: None,
            parameters: Some(params),
            active_parameter: None,
        }],
        active_signature: Some(0),
        active_parameter: Some(active),
    }
}

/// Char-offset ranges of each parameter slot in a function-type string, split
/// at TOP-LEVEL `->` (parens / brackets / braces raise the depth so
/// `List a -> a` is one slot and `(a -> b) -> …` keeps the callback whole).
/// The final slot (the return type) is excluded.
fn param_slots(ty: &str) -> Vec<(usize, usize)> {
    let chars: Vec<char> = ty.chars().collect();
    let mut depth = 0i32;
    let mut slots: Vec<(usize, usize)> = Vec::new();
    let mut start = 0usize;
    let mut i = 0usize;
    while i < chars.len() {
        match chars[i] {
            '(' | '[' | '{' => depth += 1,
            ')' | ']' | '}' => depth -= 1,
            '-' if depth == 0 && i + 1 < chars.len() && chars[i + 1] == '>' => {
                slots.push(trim_slot(&chars, start, i));
                i += 2;
                start = i;
                continue;
            }
            _ => {}
        }
        i += 1;
    }
    // [start, len) is the return type — deliberately not a parameter.
    slots
}

/// Trim leading/trailing whitespace from a `[s, e)` char range, in char units.
fn trim_slot(chars: &[char], s: usize, e: usize) -> (usize, usize) {
    let mut a = s;
    let mut b = e;
    while a < b && chars[a].is_whitespace() {
        a += 1;
    }
    while b > a && chars[b - 1].is_whitespace() {
        b -= 1;
    }
    (a, b)
}

// ---- position / span conversion ---------------------------------------
//
// The byte↔line:col primitives (`line_starts`, `position_at`) live in the
// `diagnostics` crate so the CLI renderer and this position mapper share ONE
// implementation. The thin wrappers below adapt `diagnostics::position_at`'s
// `(line, col)` tuple to the LSP `Position` type (which `diagnostics` must not
// depend on — it sits below `tower-lsp`).

/// LSP `Position` (0-based line, UTF-16 character) → byte offset.
fn offset_of(text: &str, pos: Position) -> Option<u32> {
    let starts = diagnostics::line_starts(text);
    let start = *starts.get(pos.line as usize)?;
    let mut utf16 = 0u32;
    let mut byte = start;
    for ch in text[start..].chars() {
        if ch == '\n' || utf16 >= pos.character {
            break;
        }
        utf16 += ch.len_utf16() as u32;
        byte += ch.len_utf8();
    }
    Some(byte as u32)
}

/// byte offset → LSP `Position` (adapts the shared `diagnostics::position_at`).
fn position_at(text: &str, offset: u32) -> Position {
    let (line, character) = diagnostics::position_at(text, offset);
    Position { line, character }
}

fn span_to_range(text: &str, span: Span) -> Range {
    Range {
        start: position_at(text, span.range.0),
        end: position_at(text, span.range.1),
    }
}

/// Map a structured `diagnostics::Diagnostic` into the LSP JSON shape. The
/// primary label (`labels[0]`) drives the underline `range`; the secondary
/// labels (`labels[1..]`) become `related_information` entries, resolving each
/// span's file (`span.file.index()`) back to its document url + range via
/// `docs` — so a secondary label pointing into a *different* file still links
/// correctly. `docs` is the loaded document set (indexed by module/file id).
///
/// TODO(code-action): `d.suggestion`, when present, should also be surfaced as a
/// `textDocument/codeAction` quick-fix (a `CodeAction` titled from the
/// suggestion). No frontend emit site populates `suggestion` yet, so a handler
/// would be dead/untestable code today; when the first suggestion-carrying
/// diagnostic lands, register `code_action_provider` + a `code_action` handler
/// and thread the fix here.
fn to_lsp_diag(docs: &[Doc], text: &str, d: &diagnostics::Diagnostic) -> Diagnostic {
    let range = d
        .labels
        .first()
        .map(|l| span_to_range(text, l.span))
        .unwrap_or(Range {
            start: Position { line: 0, character: 0 },
            end: Position { line: 0, character: 0 },
        });
    let severity = match d.severity {
        diagnostics::Severity::Error => DiagnosticSeverity::ERROR,
        diagnostics::Severity::Warning => DiagnosticSeverity::WARNING,
        diagnostics::Severity::Info => DiagnosticSeverity::INFORMATION,
    };
    // Secondary labels → related information (each resolved to its own file).
    let related: Vec<DiagnosticRelatedInformation> = d
        .labels
        .iter()
        .skip(1)
        .filter_map(|l| {
            let doc = docs.get(l.span.file.index() as usize)?;
            Some(DiagnosticRelatedInformation {
                location: Location {
                    uri: doc.url.clone(),
                    range: span_to_range(&doc.text, l.span),
                },
                message: l.message.clone(),
            })
        })
        .collect();
    let related_information = if related.is_empty() { None } else { Some(related) };
    Diagnostic {
        range,
        severity: Some(severity),
        code: Some(tower_lsp::lsp_types::NumberOrString::String(d.code.0.clone())),
        message: d.message.clone(),
        source: Some("sky".to_string()),
        related_information,
        ..Default::default()
    }
}

// ---- completion context -----------------------------------------------

fn is_ident_byte(b: u8) -> bool {
    b.is_ascii_alphanumeric() || b == b'_'
}

/// Split a `<receiver>.<partial>` completion context out of the text before the
/// cursor. `Some((receiver, partial))` when the cursor sits after `receiver.`.
fn split_qualified(before: &str) -> Option<(String, String)> {
    let bytes = before.as_bytes();
    let mut i = bytes.len();
    while i > 0 && is_ident_byte(bytes[i - 1]) {
        i -= 1;
    }
    let partial = before[i..].to_string();
    if i == 0 || bytes[i - 1] != b'.' {
        return None;
    }
    let dot = i - 1;
    let mut j = dot;
    while j > 0 && is_ident_byte(bytes[j - 1]) {
        j -= 1;
    }
    let recv = &before[j..dot];
    if recv.is_empty() {
        return None;
    }
    Some((recv.to_string(), partial))
}

// ---- module loading helpers -------------------------------------------

fn module_name(parse: &syntax::Parse, path: Option<&str>) -> String {
    let tree = parse.tree();
    if let Some(n) = tree.module_header().and_then(|h| h.name()).map(|n| n.text()) {
        if !n.is_empty() {
            return n;
        }
    }
    path.and_then(|p| Path::new(p).file_stem().and_then(|s| s.to_str()))
        .unwrap_or("Main")
        .to_string()
}

fn collect_sky(dir: &Path, out: &mut Vec<PathBuf>) {
    let Ok(rd) = std::fs::read_dir(dir) else {
        return;
    };
    let mut entries: Vec<PathBuf> = rd.filter_map(|e| e.ok().map(|e| e.path())).collect();
    entries.sort();
    for path in entries {
        let name = path.file_name().and_then(|s| s.to_str()).unwrap_or("");
        if matches!(name, "sky-out" | ".skycache" | ".skydeps" | "node_modules" | ".git") {
            continue;
        }
        if path.is_dir() {
            collect_sky(&path, out);
        } else if path.extension().and_then(|e| e.to_str()) == Some("sky") {
            out.push(path);
        }
    }
}

/// A stable fs-path key for a `file://` url (canonical where possible).
fn url_key(url: &Url) -> Option<String> {
    let path = url.to_file_path().ok()?;
    let canon = std::fs::canonicalize(&path).unwrap_or(path);
    Some(canon.to_string_lossy().into_owned())
}

/// Locate `sky-stdlib`: `SKY_STDLIB_DIR` env wins; else search upward from
/// `root` (and the cwd) for a `sky-stdlib` directory.
fn stdlib_dir(root: Option<&Path>) -> Option<PathBuf> {
    if let Ok(dir) = std::env::var("SKY_STDLIB_DIR") {
        let p = PathBuf::from(dir);
        if p.is_dir() {
            return Some(p);
        }
    }
    let mut starts: Vec<PathBuf> = Vec::new();
    if let Some(r) = root {
        starts.push(r.to_path_buf());
    }
    if let Ok(cwd) = std::env::current_dir() {
        starts.push(cwd);
    }
    for start in starts {
        let mut cur: Option<&Path> = Some(start.as_path());
        while let Some(dir) = cur {
            let cand = dir.join("sky-stdlib");
            if cand.is_dir() {
                return Some(cand);
            }
            cur = dir.parent();
        }
    }
    None
}

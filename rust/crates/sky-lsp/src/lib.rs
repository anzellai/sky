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

use base::{DefId, FileId, ModuleId, Span};
use hir::{FieldOcc, ImportSource, RefOcc, Res, ResolveResult, SourceDb, TypeOcc};
use std::collections::{HashMap, HashSet};
use std::path::{Path, PathBuf};
use ty::{Ty, Typer};

use tower_lsp::lsp_types::{
    CompletionItem, CompletionItemKind, Diagnostic, DiagnosticSeverity, Hover, HoverContents,
    Location, MarkupContent, MarkupKind, Position, Range, Url,
};

/// One loaded document: its module name, parsed tree, source text, and url.
struct Doc {
    name: String,
    parse: syntax::Parse,
    text: String,
    url: Url,
}

/// The workspace: the driver-set inputs (loaded documents). The `SourceDb` is
/// rebuilt per request from these — the only mutable state the LSP owns (L1).
pub struct Analysis {
    /// Documents in insertion order; the index doubles as the rebuilt db's
    /// `ModuleId`/`FileId` (register-in-order is deterministic — L4).
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
        Analysis {
            docs: Vec::new(),
            by_name: HashMap::new(),
            by_path: HashMap::new(),
            stdlib_loaded: false,
        }
    }

    // ---- loading -------------------------------------------------------

    /// Register (or update) one source under a url. Idempotent per module name:
    /// a re-add (didChange) updates the module in place, keeping its index so
    /// spans (which carry a `FileId` == index) stay valid.
    pub fn set_document(&mut self, url: Url, text: String) {
        let parse = syntax::parse(&text, FileId(0));
        let name = module_name(&parse, url_key(&url).as_deref());
        let doc = Doc {
            name: name.clone(),
            parse,
            text,
            url: url.clone(),
        };
        let idx = match self.by_name.get(&name) {
            Some(&i) => {
                self.docs[i] = doc;
                i
            }
            None => {
                let i = self.docs.len();
                self.docs.push(doc);
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

    /// Rebuild the (lightweight) source db from the loaded documents. Cheap:
    /// `syntax::Parse` is `Arc`-backed, so `add_module` clones a pointer.
    fn build_db(&self) -> SourceDb {
        let mut db = SourceDb::new();
        for d in &self.docs {
            db.add_module(&d.name, d.parse.clone());
        }
        db
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
        let db = self.build_db();
        let module = ModuleId(idx as u32);
        let resolved = hir::resolve(&db, module);
        let cand = best_candidate(&resolved, off)?;
        let typer = Typer::new(&db);
        let md = match cand {
            Cand::Ref(o) => self.hover_ref(&typer, &resolved, o),
            Cand::Field(o) => self.hover_field(&typer, &resolved, o),
            Cand::Type(o) => self.hover_type(o),
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
        let ty = match &o.res {
            Res::Def(d) => typer
                .value_sig(*d)
                .or_else(|| typer.inferred_sig(*d))
                .map(|s| s.ty.render())
                .or_else(|| {
                    let body = resolved.bodies.get(d)?;
                    typer.body_types_annotated(*d, body).result.map(|t| t.render())
                }),
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
        };
        let ty = ty.unwrap_or_else(|| "?".to_string());
        Some(format!("```sky\n{name} : {ty}\n```"))
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

    // ---- goto-definition ----------------------------------------------

    pub fn goto(&self, url: &Url, pos: Position) -> Option<Location> {
        let idx = self.index_of(url)?;
        let text = &self.docs[idx].text;
        let off = offset_of(text, pos)?;
        let db = self.build_db();
        let module = ModuleId(idx as u32);
        let resolved = hir::resolve(&db, module);
        let cand = best_candidate(&resolved, off)?;
        let span = match cand {
            Cand::Ref(o) => match &o.res {
                Res::Def(d) => def_span(&db, &resolved, module, *d),
                Res::Ctor(cr) => def_span(&db, &resolved, module, cr.def),
                Res::Local(l) => resolved
                    .binders
                    .iter()
                    .find(|b| b.owner == o.owner && b.local == *l)
                    .map(|b| b.span),
                Res::Kernel { .. } | Res::Foreign { .. } | Res::Error => None,
            },
            Cand::Type(o) => def_span(&db, &resolved, module, o.con),
            Cand::Field(o) => {
                let typer = Typer::new(&db);
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
        let db = self.build_db();
        let module = ModuleId(idx as u32);
        let resolved = hir::resolve(&db, module);

        if let Some((recv, _partial)) = split_qualified(before) {
            // Qualified module member: `M.` → enumerate M's exports.
            if let Some(ImportSource::Dep(dep)) = resolved.qualifiers.get(&recv) {
                return module_completion(&db, *dep, &recv);
            }
            // Record field: `record.` → the receiver type's fields.
            if let Some(items) = self.field_completion(&db, &resolved, &recv, off as usize) {
                return items;
            }
            return Vec::new();
        }

        self.scope_completion(&resolved)
    }

    fn field_completion(
        &self,
        db: &SourceDb,
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
        let db = self.build_db();
        let module = ModuleId(idx as u32);
        let mut out: Vec<diagnostics::Diagnostic> = Vec::new();
        out.extend(db.module_parse(module).errors().iter().cloned());
        let resolved = hir::resolve(&db, module);
        out.extend(resolved.diagnostics.iter().cloned());
        let checked = ty::check_modules(&db, &[module]);
        out.extend(checked.diagnostics);
        out.into_iter().map(|d| to_lsp_diag(text, &d)).collect()
    }
}

// ---- free helpers over a rebuilt db -----------------------------------

/// Declaration span of a def — this module first, else the def's home module
/// (cross-file goto for free: `DefId` carries its module via the interner).
fn def_span(db: &SourceDb, resolved: &ResolveResult, this: ModuleId, d: DefId) -> Option<Span> {
    if let Some((_, s)) = resolved.def_spans.iter().find(|(id, _)| *id == d) {
        return Some(*s);
    }
    let loc = db.defs().borrow().loc(d)?;
    if loc.module == this || loc.module.index() == u32::MAX {
        return None;
    }
    let other = hir::resolve(db, loc.module);
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

fn module_completion(db: &SourceDb, dep: ModuleId, recv: &str) -> Vec<CompletionItem> {
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
}

/// The smallest span containing `off` across the three occurrence channels —
/// smallest wins so a `.field` beats its enclosing receiver, etc.
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

// ---- position / span conversion ---------------------------------------

fn line_starts(text: &str) -> Vec<usize> {
    let mut v = vec![0usize];
    for (i, b) in text.bytes().enumerate() {
        if b == b'\n' {
            v.push(i + 1);
        }
    }
    v
}

/// LSP `Position` (0-based line, UTF-16 character) → byte offset.
fn offset_of(text: &str, pos: Position) -> Option<u32> {
    let starts = line_starts(text);
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

/// byte offset → LSP `Position`.
fn position_at(text: &str, offset: u32) -> Position {
    let starts = line_starts(text);
    let off = (offset as usize).min(text.len());
    let line = match starts.binary_search(&off) {
        Ok(i) => i,
        Err(i) => i.saturating_sub(1),
    };
    let col: usize = text[starts[line]..off].chars().map(|c| c.len_utf16()).sum();
    Position {
        line: line as u32,
        character: col as u32,
    }
}

fn span_to_range(text: &str, span: Span) -> Range {
    Range {
        start: position_at(text, span.range.0),
        end: position_at(text, span.range.1),
    }
}

fn to_lsp_diag(text: &str, d: &diagnostics::Diagnostic) -> Diagnostic {
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
    Diagnostic {
        range,
        severity: Some(severity),
        code: Some(tower_lsp::lsp_types::NumberOrString::String(d.code.0.clone())),
        message: d.message.clone(),
        source: Some("sky".to_string()),
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

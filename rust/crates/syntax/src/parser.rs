//! The parser core (doc 04 §7, §10, §11): a cursor over the significant
//! `PToken` stream, the marker API driving the event list, layout-aware
//! predicates (`col`/`newline_before` vs the current block anchor), token-set
//! error recovery, and the nesting-depth guard. The grammar productions live in
//! [`crate::grammar`].

use crate::event::Event;
use crate::kind::SyntaxKind::{self, *};
use crate::layout::PToken;
use base::{FileId, Span};
use diagnostics::Diagnostic;

/// Recovery bail-out: a construct nested past this becomes an ERROR node rather
/// than overflowing the stack (doc 04 §10 — a fuzzer input can never crash the
/// LSP).
pub(crate) const MAX_DEPTH: u32 = 256;

/// A 128-bit set of token `SyntaxKind`s (all token kinds have discriminant
/// < 128; nodes are never matched against the cursor).
#[derive(Clone, Copy)]
pub(crate) struct TokenSet(u128);

impl TokenSet {
    pub(crate) const fn new(kinds: &[SyntaxKind]) -> TokenSet {
        let mut mask = 0u128;
        let mut i = 0;
        while i < kinds.len() {
            mask |= 1u128 << (kinds[i] as u16);
            i += 1;
        }
        TokenSet(mask)
    }

    pub(crate) fn contains(self, k: SyntaxKind) -> bool {
        self.0 & (1u128 << (k as u16)) != 0
    }
}

pub(crate) struct Parser<'a> {
    src: &'a str,
    toks: Vec<PToken>,
    pos: usize,
    pub(crate) events: Vec<Event>,
    pub(crate) diags: Vec<Diagnostic>,
    file: FileId,
    /// Continuation anchor: a fresh-line token continues the current block iff
    /// its `col > block_indent` (doc 04 §4, `checkIndent`).
    pub(crate) block_indent: u32,
    depth: u32,
    /// The lexical units (see [`lexical_units`]): the token index where each
    /// starts, in order. A file parse has one per top-level unit. A
    /// sub-parser (an interpolation body) has one for all its tokens.
    units: Vec<usize>,
    /// The unit the cursor is in (an index into `units`).
    unit: usize,
    /// The token index where that unit ends. The cursor never passes it: a
    /// token there reads as the end of input until the top level begins the
    /// next unit ([`Parser::begin_unit`]).
    limit: usize,
    /// Whether the current unit has already reported a syntax error.
    unit_errored: bool,
    /// Where each reported diagnostic was raised.
    sites: Vec<ErrorSite>,
}

/// Tokens that can never begin a declaration or an expression: at column 1
/// they continue the unit before them.
const CONTINUES: TokenSet = TokenSet::new(&[
    Comma, RParen, RBrack, RBrace, Pipe, Arrow, Eq, Colon, Colon2, Dot, DotDot, Op,
]);

/// Split a token stream into its lexical top-level units: a unit starts at
/// the first token and at every token that begins a line at column 1, except
/// punctuation that can only continue the line before ([`CONTINUES`]: the
/// formatter writes a multi-line record type with `,` and `}` at column 1).
/// A definition that follows its own annotation (`name : T` and then
/// `name … =`) continues the annotation's unit: the two are one declaration.
///
/// The boundaries depend on the tokens alone, never on how far the parser
/// reads, so the error recovery in one unit cannot reach into the next one.
pub(crate) fn lexical_units(src: &str, toks: &[PToken]) -> Vec<usize> {
    let text = |i: usize| toks.get(i).map(|t| &src[t.start as usize..t.end as usize]);
    let kind = |i: usize| toks.get(i).map(|t| t.kind);
    let mut units: Vec<usize> = Vec::new();
    for (i, t) in toks.iter().enumerate() {
        let line_start = i == 0 || (t.newline_before && t.col == 1 && !CONTINUES.contains(t.kind));
        if !line_start {
            continue;
        }
        if let Some(&prev) = units.last() {
            let annotated = kind(prev + 1) == Some(Colon);
            if annotated && text(prev) == text(i) && kind(i + 1) != Some(Colon) {
                continue;
            }
        }
        units.push(i);
    }
    units
}

/// Where a reported syntax diagnostic was raised: its index in the
/// diagnostics, the token it was raised at, and its unit (an index into
/// [`Finished::units`]).
#[derive(Clone, Copy, Debug)]
pub(crate) struct ErrorSite {
    pub(crate) diag: usize,
    pub(crate) at: usize,
    pub(crate) unit: usize,
}

/// What a parse yields besides the tree: the diagnostics, where each was
/// raised, and the lexical unit starts.
pub(crate) struct Finished {
    pub(crate) events: Vec<Event>,
    pub(crate) diags: Vec<Diagnostic>,
    pub(crate) sites: Vec<ErrorSite>,
    pub(crate) units: Vec<usize>,
}

/// An open node marker (an index into the event list at its `Start`).
pub(crate) struct Marker {
    pos: usize,
    completed: bool,
}

/// A finished node marker — supports Pratt re-parenting via `precede`.
#[derive(Clone, Copy)]
pub(crate) struct CompletedMarker {
    pos: usize,
}

impl Marker {
    pub(crate) fn complete(mut self, p: &mut Parser, kind: SyntaxKind) -> CompletedMarker {
        self.completed = true;
        match &mut p.events[self.pos] {
            Event::Start { kind: k, .. } => *k = kind,
            _ => unreachable!("marker must point at a Start"),
        }
        p.events.push(Event::Finish);
        CompletedMarker { pos: self.pos }
    }
}

impl Drop for Marker {
    fn drop(&mut self) {
        if !self.completed && !std::thread::panicking() {
            panic!("Marker dropped without complete/abandon");
        }
    }
}

impl CompletedMarker {
    pub(crate) fn precede(self, p: &mut Parser) -> Marker {
        let m = p.start();
        if let Event::Start { forward_parent, .. } = &mut p.events[self.pos] {
            *forward_parent = Some((m.pos - self.pos) as u32);
        }
        m
    }
}

impl<'a> Parser<'a> {
    /// A parser over `toks` as one unit (an interpolation body).
    pub(crate) fn new(src: &'a str, toks: Vec<PToken>, file: FileId) -> Parser<'a> {
        Parser::with_units(src, toks, file, vec![0])
    }

    /// A parser over a whole file: one unit per [`lexical_units`].
    pub(crate) fn new_file(src: &'a str, toks: Vec<PToken>, file: FileId) -> Parser<'a> {
        let units = lexical_units(src, &toks);
        Parser::with_units(src, toks, file, units)
    }

    fn with_units(src: &'a str, toks: Vec<PToken>, file: FileId, units: Vec<usize>) -> Parser<'a> {
        let limit = units.get(1).copied().unwrap_or(toks.len());
        Parser {
            src,
            toks,
            pos: 0,
            events: Vec::new(),
            diags: Vec::new(),
            file,
            block_indent: 0,
            depth: 0,
            units,
            unit: 0,
            limit,
            unit_errored: false,
            sites: Vec::new(),
        }
    }

    /// The token at `i`, or `None` past the end of the current unit.
    fn tok(&self, i: usize) -> Option<&PToken> {
        if i < self.limit {
            self.toks.get(i)
        } else {
            None
        }
    }

    // ---- cursor ----

    pub(crate) fn current(&self) -> SyntaxKind {
        self.tok(self.pos).map(|t| t.kind).unwrap_or(Eof)
    }

    pub(crate) fn nth(&self, n: usize) -> SyntaxKind {
        self.tok(self.pos + n).map(|t| t.kind).unwrap_or(Eof)
    }

    pub(crate) fn at(&self, kind: SyntaxKind) -> bool {
        self.current() == kind
    }

    pub(crate) fn at_any(&self, set: TokenSet) -> bool {
        set.contains(self.current())
    }

    pub(crate) fn at_end(&self) -> bool {
        self.pos >= self.limit.min(self.toks.len())
    }

    /// Text of the current token (empty at EOF).
    pub(crate) fn cur_text(&self) -> &'a str {
        match self.tok(self.pos) {
            Some(t) => &self.src[t.start as usize..t.end as usize],
            None => "",
        }
    }

    pub(crate) fn newline_before(&self) -> bool {
        self.tok(self.pos).map(|t| t.newline_before).unwrap_or(true)
    }

    pub(crate) fn col(&self) -> u32 {
        self.tok(self.pos).map(|t| t.col).unwrap_or(0)
    }

    pub(crate) fn ws_before(&self) -> bool {
        self.tok(self.pos).map(|t| t.ws_before).unwrap_or(true)
    }

    pub(crate) fn nth_ws_before(&self, n: usize) -> bool {
        self.tok(self.pos + n).map(|t| t.ws_before).unwrap_or(true)
    }

    /// The current token *continues* the enclosing block: same line, or a fresh
    /// line indented strictly past the block anchor (doc 04 §4, `checkIndent`).
    pub(crate) fn at_continuation(&self) -> bool {
        if self.at_end() {
            return false;
        }
        !self.newline_before() || self.col() > self.block_indent
    }

    /// The span a diagnostic at the cursor points at. At the end of a unit
    /// that is not the end of the file, it is the point just after the
    /// unit's last token, so the diagnostic stays inside its own unit.
    fn cur_span(&self) -> Span {
        match self.tok(self.pos) {
            Some(t) => Span::new(self.file, t.start, t.end),
            None if self.pos < self.toks.len() && self.pos > 0 => {
                let end = self.toks[self.pos - 1].end;
                Span::new(self.file, end, end)
            }
            None => {
                let end = self.src.len() as u32;
                Span::new(self.file, end, end)
            }
        }
    }

    // ---- marker API ----

    pub(crate) fn start(&mut self) -> Marker {
        let pos = self.events.len();
        self.events.push(Event::Start {
            kind: SyntaxKind::Tombstone,
            forward_parent: None,
        });
        Marker {
            pos,
            completed: false,
        }
    }

    /// Emit the current token with its own kind and advance.
    pub(crate) fn bump(&mut self) {
        let kind = self.current();
        self.do_bump(kind);
    }

    fn do_bump(&mut self, kind: SyntaxKind) {
        if self.at_end() {
            return;
        }
        self.events.push(Event::Token { kind });
        self.pos += 1;
    }

    pub(crate) fn eat(&mut self, kind: SyntaxKind) -> bool {
        if self.at(kind) {
            self.bump();
            true
        } else {
            false
        }
    }

    /// Consume `kind` or record a diagnostic without consuming.
    pub(crate) fn expect(&mut self, kind: SyntaxKind) -> bool {
        if self.eat(kind) {
            true
        } else {
            self.error(format!("expected {kind:?}"));
            false
        }
    }

    // ---- diagnostics + recovery ----

    /// Record a syntax diagnostic at the current token. Only the FIRST one in
    /// a lexical unit is kept (the diagnostic budget): a later one is a
    /// follow-on of the first, so it is dropped. The cursor never leaves its
    /// unit, so the unit that keeps a diagnostic is the unit it points into. The ERROR nodes and
    /// the recovery are unchanged, so the tree the LSP and `fmt` read is the
    /// same.
    pub(crate) fn error(&mut self, msg: impl Into<std::string::String>) {
        if self.unit_errored {
            return;
        }
        self.unit_errored = true;
        self.sites.push(ErrorSite {
            diag: self.diags.len(),
            at: self.pos,
            unit: self.unit,
        });
        let msg = msg.into();
        let span = self.cur_span();
        self.diags
            .push(Diagnostic::error("E0001", msg.clone()).with_label(span, msg));
    }

    /// Wrap the current token in an ERROR node with a diagnostic, then advance.
    pub(crate) fn err_and_bump(&mut self, msg: impl Into<std::string::String>) {
        let m = self.start();
        self.error(msg);
        if !self.at_end() {
            self.bump();
        }
        m.complete(self, Error);
    }

    /// Diagnose, then wrap tokens up to a recovery anchor in an ERROR node so
    /// the enclosing production can resume (doc 04 §11). Stops at the recovery
    /// set, EOF, or a dedent below the current block anchor.
    pub(crate) fn err_recover(&mut self, msg: impl Into<std::string::String>, recovery: TokenSet) {
        if self.at_end() || self.at_any(recovery) || self.at_block_boundary() {
            self.error(msg);
            return;
        }
        let m = self.start();
        self.error(msg);
        while !self.at_end() && !self.at_any(recovery) && !self.at_block_boundary() {
            self.bump();
        }
        m.complete(self, Error);
    }

    /// A fresh-line token dedented to/below the enclosing block anchor — a
    /// natural recovery stop.
    pub(crate) fn at_block_boundary(&self) -> bool {
        !self.at_end() && self.newline_before() && self.col() <= self.block_indent
    }

    // ---- diagnostic budget ----

    /// Begin the next lexical unit when the cursor has reached the end of
    /// the current one (the module header, an import, a declaration, or
    /// recovery garbage at the top level). Only the top level calls this, so
    /// a construct nested in one unit ends at the unit's end, and the
    /// diagnostic budget resets here: an error in one declaration never
    /// hides an error in the next one.
    pub(crate) fn begin_unit(&mut self) {
        if self.pos < self.limit || self.pos >= self.toks.len() {
            return;
        }
        self.unit += 1;
        self.limit = self
            .units
            .get(self.unit + 1)
            .copied()
            .unwrap_or(self.toks.len());
        self.unit_errored = false;
    }

    // ---- block-indent scoping (Haskell `withIndent`) ----

    pub(crate) fn with_indent<R>(&mut self, anchor: u32, f: impl FnOnce(&mut Self) -> R) -> R {
        let saved = self.block_indent;
        self.block_indent = anchor;
        let r = f(self);
        self.block_indent = saved;
        r
    }

    // ---- depth guard (doc 04 §10) ----

    /// Enter a nested `expr`/`type`/`pattern`. Returns `true` if the depth limit
    /// is exceeded (the caller must emit an [`Parser::error_node`] and bail).
    pub(crate) fn depth_enter(&mut self) -> bool {
        self.depth += 1;
        self.depth > MAX_DEPTH
    }

    pub(crate) fn depth_leave(&mut self) {
        self.depth -= 1;
    }

    /// Emit a one-token ERROR node — the depth-guard bail-out (never recurse
    /// further; still yields a well-formed tree).
    pub(crate) fn error_node(&mut self) -> CompletedMarker {
        let m = self.start();
        self.error("nested too deeply");
        if !self.at_end() {
            self.bump();
        }
        m.complete(self, Error)
    }

    // ---- multiline interpolation interior (doc 04 §9) ----

    /// Byte range of the current significant token.
    pub(crate) fn cur_range(&self) -> Option<(u32, u32)> {
        self.tok(self.pos).map(|t| (t.start, t.end))
    }

    pub(crate) fn src_slice(&self, start: u32, end: u32) -> &'a str {
        &self.src[start as usize..end as usize]
    }

    /// The file this parser produces spans for — used to seed the sub-parser
    /// that runs the real `expr` grammar over an interpolation `{{…}}` body
    /// (doc 04 §9).
    pub(crate) fn file(&self) -> FileId {
        self.file
    }

    /// Consume the current significant token *without* emitting it — the caller
    /// re-covers its bytes with `emit_slice`s (multiline interpolation split).
    pub(crate) fn skip_raw(&mut self) {
        if self.at_end() {
            return;
        }
        self.events.push(Event::Skip);
        self.pos += 1;
    }

    /// Emit a synthetic leaf covering `src[start..end]`.
    pub(crate) fn emit_slice(&mut self, kind: SyntaxKind, start: u32, end: u32) {
        self.events.push(Event::RawSlice { kind, start, end });
    }

    /// Parse the interior of an interpolation `{{…}}` body (absolute byte range
    /// `start..end`) as a real sub-expression, emitting offset-correct leaves so
    /// hover/goto work inside interpolations (doc 04 §9). Fully lossless: every
    /// byte of the body is re-emitted.
    pub(crate) fn parse_interp_expr(&mut self, start: u32, end: u32) {
        crate::interp::body(self, start, end);
    }

    // ---- finish ----

    pub(crate) fn finish(self) -> Finished {
        Finished {
            events: self.events,
            diags: self.diags,
            sites: self.sites,
            units: self.units,
        }
    }
}

/// A lower-case reserved word token (`lexer::RESERVED_WORDS`). `True` /
/// `False` are literals, not names, and are not included.
pub(crate) fn is_reserved_word(kind: SyntaxKind) -> bool {
    matches!(
        kind,
        ModuleKw
            | ExposingKw
            | ImportKw
            | AsKw
            | TypeKw
            | AliasKw
            | IfKw
            | ThenKw
            | ElseKw
            | CaseKw
            | OfKw
            | LetKw
            | InKw
    )
}

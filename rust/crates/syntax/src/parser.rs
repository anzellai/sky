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
    pub(crate) fn new(src: &'a str, toks: Vec<PToken>, file: FileId) -> Parser<'a> {
        Parser {
            src,
            toks,
            pos: 0,
            events: Vec::new(),
            diags: Vec::new(),
            file,
            block_indent: 0,
            depth: 0,
        }
    }

    // ---- cursor ----

    pub(crate) fn current(&self) -> SyntaxKind {
        self.toks.get(self.pos).map(|t| t.kind).unwrap_or(Eof)
    }

    pub(crate) fn nth(&self, n: usize) -> SyntaxKind {
        self.toks.get(self.pos + n).map(|t| t.kind).unwrap_or(Eof)
    }

    pub(crate) fn at(&self, kind: SyntaxKind) -> bool {
        self.current() == kind
    }

    pub(crate) fn at_any(&self, set: TokenSet) -> bool {
        set.contains(self.current())
    }

    pub(crate) fn at_end(&self) -> bool {
        self.pos >= self.toks.len()
    }

    /// Text of the current token (empty at EOF).
    pub(crate) fn cur_text(&self) -> &'a str {
        match self.toks.get(self.pos) {
            Some(t) => &self.src[t.start as usize..t.end as usize],
            None => "",
        }
    }

    pub(crate) fn newline_before(&self) -> bool {
        self.toks
            .get(self.pos)
            .map(|t| t.newline_before)
            .unwrap_or(true)
    }

    pub(crate) fn col(&self) -> u32 {
        self.toks.get(self.pos).map(|t| t.col).unwrap_or(0)
    }

    pub(crate) fn ws_before(&self) -> bool {
        self.toks.get(self.pos).map(|t| t.ws_before).unwrap_or(true)
    }

    pub(crate) fn nth_ws_before(&self, n: usize) -> bool {
        self.toks
            .get(self.pos + n)
            .map(|t| t.ws_before)
            .unwrap_or(true)
    }

    /// The current token *continues* the enclosing block: same line, or a fresh
    /// line indented strictly past the block anchor (doc 04 §4, `checkIndent`).
    pub(crate) fn at_continuation(&self) -> bool {
        if self.at_end() {
            return false;
        }
        !self.newline_before() || self.col() > self.block_indent
    }

    fn cur_span(&self) -> Span {
        match self.toks.get(self.pos) {
            Some(t) => Span::new(self.file, t.start, t.end),
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

    pub(crate) fn error(&mut self, msg: impl Into<std::string::String>) {
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
        self.toks.get(self.pos).map(|t| (t.start, t.end))
    }

    pub(crate) fn src_slice(&self, start: u32, end: u32) -> &'a str {
        &self.src[start as usize..end as usize]
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

    pub(crate) fn finish(self) -> (Vec<Event>, Vec<Diagnostic>) {
        (self.events, self.diags)
    }
}

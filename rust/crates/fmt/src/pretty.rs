//! Opinionated, deterministic pretty-printer over the typed AST view of the
//! lossless CST — an Elm-format-compatible re-layout that mirrors the Haskell
//! oracle (`src/Sky/Format/Format.hs`) rule-for-rule, with the two observed
//! deltas the compiled oracle actually emits: **call arguments break
//! all-or-nothing** (callee then one arg per line at `col+4`) and **operator
//! chains greedy-fill the first line then drop one group per line at the
//! operator column**.
//!
//! Absolute-column tracking: every method takes `col` (the current indent) and
//! returns a `String` with newlines already placed. The golden rule is "one
//! line, or each on its own line".
//!
//! Comments are trivia in the CST. Own-line comments are threaded through a
//! consumable stream (`comments`) and drained at semantic boundaries, exactly
//! like the oracle's `CS`. Trailing (inline) comments are intentionally NOT
//! placed here; the caller's safety net (comment-multiset preservation) falls
//! the whole file back to a lossless reprint when any comment would be lost, so
//! no data is ever dropped (closing the oracle's `Format.hs:18` hole).

use syntax::ast::{AstNode, Decl, Expr, Import, Pattern, SourceFile, Type};
use syntax::{SyntaxKind, SyntaxNode, SyntaxToken};

const MAX: usize = 80;
/// The exposing-clause wrap threshold is wider than the code width (matches the
/// oracle's `maxLineWidth = 100`).
const EXPOSING_MAX: usize = 100;
const STEP: usize = 4;

/// One own-line comment pulled out of the CST trivia.
#[derive(Clone)]
struct Comment {
    line: usize,
    /// 0-based count of leading spaces on the comment's own line.
    col: usize,
    /// Raw token text, verbatim (`-- foo` / `{- .. -}`), delimiters included.
    text: String,
}

pub struct Printer {
    /// Own-line comments still to be placed, kept sorted by source line.
    comments: Vec<Comment>,
    line_starts: Vec<usize>,
    src: String,
}

fn width(s: &str) -> usize {
    s.chars().count()
}

fn last_line_width(s: &str) -> usize {
    match s.rfind('\n') {
        Some(i) => width(&s[i + 1..]),
        None => width(s),
    }
}

fn indent(n: usize) -> String {
    " ".repeat(n)
}

impl Printer {
    /// Build a printer for `src` over its parsed `root`, collecting own-line
    /// comments as a drainable stream.
    pub fn new(src: &str, root: &SyntaxNode) -> Printer {
        let mut line_starts = vec![0usize];
        for (i, b) in src.bytes().enumerate() {
            if b == b'\n' {
                line_starts.push(i + 1);
            }
        }
        let mut comments = Vec::new();
        for elem in root.descendants_with_tokens() {
            let Some(tok) = elem.into_token() else {
                continue;
            };
            let k = tok.kind();
            if k != SyntaxKind::LineComment && k != SyntaxKind::BlockComment {
                continue;
            }
            let start = usize::from(tok.text_range().start());
            let line = line_of(&line_starts, start);
            let line_start = line_starts[line];
            // Own-line iff only whitespace precedes the comment on its line.
            let prefix = &src[line_start..start];
            if !prefix.chars().all(|c| c == ' ' || c == '\t') {
                continue; // trailing comment: left for the safety net to catch
            }
            comments.push(Comment {
                line,
                col: prefix.chars().count(),
                text: tok.text().to_string(),
            });
        }
        comments.sort_by_key(|c| c.line);
        Printer {
            comments,
            line_starts,
            src: src.to_string(),
        }
    }

    /// The source slice spanning a node's significant tokens only (leading and
    /// trailing trivia excluded). Used to emit multiline string literals
    /// verbatim without the newline/indent trivia rowan attaches to the node.
    fn sig_span(&self, node: &SyntaxNode) -> String {
        let toks: Vec<_> = node
            .descendants_with_tokens()
            .filter_map(|e| e.into_token())
            .filter(|t| !t.kind().is_trivia())
            .collect();
        match (toks.first(), toks.last()) {
            (Some(f), Some(l)) => {
                let start = usize::from(f.text_range().start());
                let end = usize::from(l.text_range().end());
                self.src[start..end].to_string()
            }
            _ => node.text().to_string(),
        }
    }

    /// Line of a node's first **significant** token. A node's `text_range`
    /// often begins inside its leading comment/whitespace trivia (rowan attaches
    /// leading trivia to the following node), so using the raw start would place
    /// the node's line inside a comment block above it and mis-route drains.
    fn line_of_node(&self, node: &SyntaxNode) -> usize {
        let start = node
            .descendants_with_tokens()
            .filter_map(|e| e.into_token())
            .find(|t| !t.kind().is_trivia())
            .map(|t| usize::from(t.text_range().start()))
            .unwrap_or_else(|| usize::from(node.text_range().start()));
        line_of(&self.line_starts, start)
    }

    // ---- comment draining (mirrors the oracle's CS helpers) --------------

    /// Drain own-line comments strictly above `node_line` whose source column
    /// matches this `ind` (0-based). Rendered at `ind`.
    fn drain_before(&mut self, ind: usize, node_line: usize) -> String {
        let mut out = String::new();
        let mut kept = Vec::with_capacity(self.comments.len());
        for c in std::mem::take(&mut self.comments) {
            if c.line < node_line && c.col == ind {
                out.push_str(&render_comment(ind, &c));
            } else {
                kept.push(c);
            }
        }
        self.comments = kept;
        out
    }

    /// Drain own-line comments in the half-open line window `[low, node_line)`
    /// regardless of column, rendered at `ind` (used at re-indenting boundaries
    /// like a lambda body). The `low` bound prevents slurping comments that sit
    /// above the enclosing construct — those belong to an outer scope.
    fn drain_any_between(&mut self, ind: usize, low: usize, node_line: usize) -> String {
        let mut out = String::new();
        let mut kept = Vec::with_capacity(self.comments.len());
        for c in std::mem::take(&mut self.comments) {
            if c.line >= low && c.line < node_line {
                out.push_str(&render_comment(ind, &c));
            } else {
                kept.push(c);
            }
        }
        self.comments = kept;
        out
    }

    /// End-of-decl mop-up: drain remaining own-line comments below the body but
    /// before `next_line`, each at its own source column. Filters column 0 so
    /// top-level headers still route to the next decl's start-of-decl drain.
    fn drain_inner_until(&mut self, next_line: usize) -> String {
        let mut out = String::new();
        let mut kept = Vec::with_capacity(self.comments.len());
        for c in std::mem::take(&mut self.comments) {
            if c.line < next_line && c.col > 0 {
                out.push_str(&render_comment(c.col, &c));
            } else {
                kept.push(c);
            }
        }
        self.comments = kept;
        out
    }

    fn drain_remaining(&mut self) -> String {
        let mut out = String::new();
        for c in std::mem::take(&mut self.comments) {
            out.push_str(&render_comment(c.col, &c));
        }
        out
    }

    // ---- module ----------------------------------------------------------

    pub fn format(&mut self, file: &SourceFile) -> String {
        let mut sections: Vec<String> = Vec::new();

        if let Some(h) = file.module_header() {
            let name = h.name().map(|n| n.text()).unwrap_or_default();
            let lhs = format!("module {name} exposing");
            let exposing = h
                .exposing()
                .map(|e| self.exposing_clause(width(&lhs), e.syntax()))
                .unwrap_or_else(|| " (..)".to_string());
            sections.push(format!("{lhs}{exposing}"));
        }

        let imports: Vec<String> = file.imports().map(|i| self.fmt_import(&i)).collect();
        if !imports.is_empty() {
            sections.push(format!("\n{}", imports.join("\n")));
        }

        // Group decls: a `name : Type` annotation immediately followed by the
        // matching `name …=…` value renders as one unit (value kind).
        let decls: Vec<Decl> = file.decls().collect();
        let next_lines = decl_next_lines(self, &decls);
        let mut i = 0;
        while i < decls.len() {
            // Drain this decl's own-line header comments (col 0) BEFORE walking
            // its body, so a lambda body's permissive drain cannot slurp a
            // section header that belongs above this decl.
            let node_line = self.line_of_node(decls[i].syntax());
            let drained = self.drain_before(0, node_line);
            let (unit, is_value, consumed) = self.decl_unit(&decls, i);
            // Body-trailer comments below this unit, before the next decl.
            let nl = next_lines[i + consumed - 1];
            let post = trim_trailing_newlines(&self.drain_inner_until_opt(nl));
            let sep = if is_value { "\n\n" } else { "\n" };
            let body = if post.is_empty() {
                unit
            } else {
                format!("{unit}\n{post}")
            };
            sections.push(format!("{sep}{drained}{body}"));
            i += consumed;
        }

        let trailing = trim_trailing_newlines(&self.drain_remaining());
        if !trailing.is_empty() {
            sections.push(format!("\n{trailing}"));
        }

        let mut out = sections.join("\n");
        out.push('\n');
        out
    }

    fn drain_inner_until_opt(&mut self, next_line: Option<usize>) -> String {
        match next_line {
            Some(n) => self.drain_inner_until(n),
            None => String::new(),
        }
    }

    /// Render one decl unit starting at index `i`. Returns (text, is_value_kind,
    /// number_of_decls_consumed).
    fn decl_unit(&mut self, decls: &[Decl], i: usize) -> (String, bool, usize) {
        match &decls[i] {
            Decl::TypeAnno(anno) => {
                let anno_name = anno.name().map(|t| t.text().to_string());
                // Pair with an immediately-following value of the same name.
                if let Some(Decl::Value(v)) = decls.get(i + 1) {
                    let vn = v.name().map(|t| t.text().to_string());
                    if anno_name.is_some() && anno_name == vn {
                        let sig = self.fmt_anno(anno);
                        let val = self.fmt_value(v);
                        return (format!("{sig}\n{val}"), true, 2);
                    }
                }
                (self.fmt_anno(anno), true, 1)
            }
            Decl::Value(v) => (self.fmt_value(v), true, 1),
            Decl::Alias(a) => (self.fmt_alias(a), false, 1),
            Decl::Union(u) => (self.fmt_union(u), false, 1),
            Decl::Foreign(f) => {
                // No opinionated shape yet: reprint verbatim (safety net will
                // fall the file back if this ever loses a comment).
                (
                    f.syntax().text().to_string().trim_end().to_string(),
                    true,
                    1,
                )
            }
        }
    }

    // ---- exposing / imports ----------------------------------------------

    fn exposing_clause(&self, lhs_len: usize, node: &SyntaxNode) -> String {
        if exposing_is_all(node) {
            return " (..)".to_string();
        }
        let items = exposed_items(node);
        let single = format!("({})", items.join(", "));
        let total = lhs_len + 1 + width(&single);
        if total <= EXPOSING_MAX || items.len() <= 1 {
            format!(" {single}")
        } else {
            format!("\n    ( {}\n    )", items.join("\n    , "))
        }
    }

    fn fmt_import(&self, imp: &Import) -> String {
        let name = imp.name().map(|n| n.text()).unwrap_or_default();
        let alias = imp
            .alias()
            .map(|a| format!(" as {}", a.text()))
            .unwrap_or_default();
        let prefix = format!("import {name}{alias}");
        let exposing = match imp.exposing() {
            Some(e) if exposing_is_all(e.syntax()) => {
                format!(
                    " exposing{}",
                    self.exposing_clause(width(&prefix) + 9, e.syntax())
                )
            }
            Some(e) => {
                let items = exposed_items(e.syntax());
                if items.is_empty() {
                    String::new()
                } else {
                    let lhs = format!("{prefix} exposing");
                    format!(" exposing{}", self.exposing_clause(width(&lhs), e.syntax()))
                }
            }
            None => String::new(),
        };
        format!("{prefix}{exposing}")
    }

    // ---- declarations ----------------------------------------------------

    fn fmt_anno(&self, anno: &syntax::ast::TypeAnnoDecl) -> String {
        let name = anno
            .name()
            .map(|t| t.text().to_string())
            .unwrap_or_default();
        let ty = anno.ty().map(|t| self.fmt_type(0, &t)).unwrap_or_default();
        format!("{name} : {ty}")
    }

    fn fmt_value(&mut self, v: &syntax::ast::ValueDecl) -> String {
        let name = v.name().map(|t| t.text().to_string()).unwrap_or_default();
        let params = v
            .params()
            .map(|pl| {
                pl.params()
                    .map(|p| self.fmt_pattern(&p))
                    .collect::<Vec<_>>()
                    .join(" ")
            })
            .unwrap_or_default();
        let params_str = if params.is_empty() {
            String::new()
        } else {
            format!(" {params}")
        };
        let header = format!("{name}{params_str} =\n");
        match v.body() {
            Some(body) => {
                let body_line = self.line_of_node(body.syntax());
                let drained = self.drain_before(STEP, body_line);
                let b = self.fmt_expr(STEP, &body);
                format!("{header}{drained}{}{b}", indent(STEP))
            }
            None => format!("{header}{}", indent(STEP)),
        }
    }

    fn fmt_alias(&self, a: &syntax::ast::AliasDecl) -> String {
        let name = a.name().map(|t| t.text().to_string()).unwrap_or_default();
        let vars = type_var_list(a.syntax());
        let vars_str = if vars.is_empty() {
            String::new()
        } else {
            format!(" {}", vars.join(" "))
        };
        let body = a.ty().map(|t| self.fmt_type(STEP, &t)).unwrap_or_default();
        format!("type alias {name}{vars_str} =\n{}{body}", indent(STEP))
    }

    fn fmt_union(&self, u: &syntax::ast::UnionDecl) -> String {
        let name = u.name().map(|t| t.text().to_string()).unwrap_or_default();
        let vars = type_var_list(u.syntax());
        let vars_str = if vars.is_empty() {
            String::new()
        } else {
            format!(" {}", vars.join(" "))
        };
        let ctors: Vec<String> = u.variants().iter().map(|v| self.fmt_ctor(v)).collect();
        let body = match ctors.as_slice() {
            [] => String::new(),
            [c] => format!("\n{}= {c}", indent(STEP)),
            [c, rest @ ..] => {
                let mut s = format!("\n{}= {c}", indent(STEP));
                for r in rest {
                    s.push_str(&format!("\n{}| {r}", indent(STEP)));
                }
                s
            }
        };
        format!("type {name}{vars_str}{body}")
    }

    fn fmt_ctor(&self, v: &syntax::ast::UnionVariant) -> String {
        let name = v.name().map(|t| t.text().to_string()).unwrap_or_default();
        let args: Vec<String> = type_children(v.syntax())
            .iter()
            .map(|t| self.fmt_type_parens(0, t))
            .collect();
        if args.is_empty() {
            name
        } else {
            format!("{name} {}", args.join(" "))
        }
    }

    // ---- types -----------------------------------------------------------

    fn fmt_type(&self, col: usize, t: &Type) -> String {
        let u = unwrap_type_paren(t);
        match &u {
            Type::Fun(f) => {
                let parts = type_children(f.syntax());
                if let [a, b] = parts.as_slice() {
                    format!(
                        "{} -> {}",
                        self.fmt_type_atom(col, a),
                        self.fmt_type(col, b)
                    )
                } else {
                    self.fmt_type_atom(col, &u)
                }
            }
            _ => self.fmt_type_atom(col, &u),
        }
    }

    fn fmt_type_atom(&self, col: usize, t: &Type) -> String {
        // The oracle's type AST has no paren node: source parens are dropped and
        // re-added by structure. Unwrap here, then re-add only where required.
        let u = unwrap_type_paren(t);
        match &u {
            Type::Var(v) => sig_text(v.syntax()),
            Type::Con(c) => sig_text(c.syntax()),
            Type::Qual(q) => sig_text(q.syntax()),
            Type::Unit(_) => "()".to_string(),
            Type::App(a) => {
                let parts = type_children(a.syntax());
                if let [head, args @ ..] = parts.as_slice() {
                    if args.is_empty() {
                        self.fmt_type_atom(col, head)
                    } else {
                        let head_s = self.fmt_type_atom(col, head);
                        let args_s: Vec<String> =
                            args.iter().map(|x| self.fmt_type_parens(col, x)).collect();
                        format!("{head_s} {}", args_s.join(" "))
                    }
                } else {
                    String::new()
                }
            }
            Type::Tuple(tp) => {
                let parts = type_children(tp.syntax());
                let s: Vec<String> = parts.iter().map(|x| self.fmt_type(col, x)).collect();
                format!("( {} )", s.join(", "))
            }
            Type::Record(r) => self.fmt_type_record(col, r),
            // A function type in atom position needs grouping parens.
            Type::Fun(_) => format!("({})", self.fmt_type(col, &u)),
            Type::Paren(_) => String::new(), // unreachable: unwrapped above
        }
    }

    fn fmt_type_parens(&self, col: usize, t: &Type) -> String {
        // Argument position: an applied type or a function type needs parens.
        let u = unwrap_type_paren(t);
        match &u {
            Type::App(a) if type_children(a.syntax()).len() > 1 => {
                format!("({})", self.fmt_type_atom(col, &u))
            }
            Type::Fun(_) => format!("({})", self.fmt_type(col, &u)),
            _ => self.fmt_type_atom(col, &u),
        }
    }

    fn fmt_type_record(&self, col: usize, r: &syntax::ast::TypeRecord) -> String {
        let row = row_var(r.syntax());
        let fields: Vec<String> = type_record_fields(r.syntax())
            .iter()
            .map(|(n, ty)| format!("{n} : {}", self.fmt_type(col + 6, ty)))
            .collect();
        let prefix = match &row {
            Some(rv) => format!("{rv} | "),
            None => String::new(),
        };
        // Note: an empty record renders as the one-line "{  }" (two spaces) —
        // the oracle's `oneLine` branch, matched here byte-for-byte.
        let one_line = format!("{{ {prefix}{} }}", fields.join(", "));
        if col + width(&one_line) <= MAX && fields.len() <= 1 {
            return one_line;
        }
        // multi-line, leading commas at `col`
        let mut s = format!("{{ {prefix}{}", fields[0]);
        for f in &fields[1..] {
            s.push_str(&format!("\n{}, {f}", indent(col)));
        }
        s.push_str(&format!("\n{}}}", indent(col)));
        s
    }

    // ---- patterns --------------------------------------------------------

    fn fmt_pattern(&self, p: &Pattern) -> String {
        match p {
            Pattern::Wildcard(_) => "_".to_string(),
            Pattern::Var(v) => sig_text(v.syntax()),
            Pattern::Unit(_) => "()".to_string(),
            Pattern::Int(n) => sig_text(n.syntax()),
            Pattern::Float(n) => {
                let raw = sig_text(n.syntax());
                raw.parse::<f64>().map(haskell_show_double).unwrap_or(raw)
            }
            Pattern::Str(s) => sig_text(s.syntax()),
            Pattern::Char(c) => sig_text(c.syntax()),
            Pattern::Bool(b) => sig_text(b.syntax()),
            Pattern::Negate(n) => sig_text(n.syntax()),
            Pattern::Ctor(c) => {
                let head = ctor_head(c.syntax());
                let args = pattern_children(c.syntax());
                if args.is_empty() {
                    head
                } else {
                    let a: Vec<String> = args.iter().map(|x| self.fmt_pattern_atom(x)).collect();
                    format!("{head} {}", a.join(" "))
                }
            }
            Pattern::CtorQual(c) => {
                let head = ctor_head(c.syntax());
                let args = pattern_children(c.syntax());
                if args.is_empty() {
                    head
                } else {
                    let a: Vec<String> = args.iter().map(|x| self.fmt_pattern_atom(x)).collect();
                    format!("{head} {}", a.join(" "))
                }
            }
            Pattern::List(l) => {
                let ps: Vec<String> = pattern_children(l.syntax())
                    .iter()
                    .map(|x| self.fmt_pattern(x))
                    .collect();
                format!("[{}]", ps.join(", "))
            }
            Pattern::Cons(c) => {
                let ps = pattern_children(c.syntax());
                if let [hd, tl] = ps.as_slice() {
                    format!("{} :: {}", self.fmt_pattern_atom(hd), self.fmt_pattern(tl))
                } else {
                    String::new()
                }
            }
            Pattern::Tuple(t) => {
                let ps: Vec<String> = pattern_children(t.syntax())
                    .iter()
                    .map(|x| self.fmt_pattern(x))
                    .collect();
                format!("( {} )", ps.join(", "))
            }
            Pattern::Record(r) => {
                let names = record_pat_names(r.syntax());
                format!("{{ {} }}", names.join(", "))
            }
            Pattern::Alias(a) => {
                let ps = pattern_children(a.syntax());
                let inner = ps.first().map(|x| self.fmt_pattern(x)).unwrap_or_default();
                let name = alias_name(a.syntax());
                format!("{inner} as {name}")
            }
            Pattern::Paren(p) => {
                let ps = pattern_children(p.syntax());
                match ps.first() {
                    Some(x) => format!("({})", self.fmt_pattern(x)),
                    None => "()".to_string(),
                }
            }
        }
    }

    fn fmt_pattern_atom(&self, p: &Pattern) -> String {
        match p {
            Pattern::Ctor(c) if !pattern_children(c.syntax()).is_empty() => {
                format!("({})", self.fmt_pattern(p))
            }
            Pattern::CtorQual(c) if !pattern_children(c.syntax()).is_empty() => {
                format!("({})", self.fmt_pattern(p))
            }
            Pattern::Cons(_) | Pattern::Alias(_) => format!("({})", self.fmt_pattern(p)),
            _ => self.fmt_pattern(p),
        }
    }

    // ---- expressions -----------------------------------------------------

    fn fmt_expr(&mut self, col: usize, e: &Expr) -> String {
        match e {
            Expr::Literal(l) => match l.as_float() {
                Some(f) => haskell_show_double(f),
                None => sig_text(l.syntax()),
            },
            Expr::Multiline(m) => self.sig_span(m.syntax()),
            Expr::Ref(r) => sig_text(r.syntax()),
            Expr::QualRef(r) => sig_text(r.syntax()),
            Expr::Unit(_) => "()".to_string(),
            Expr::Accessor(a) => sig_text(a.syntax()),
            Expr::FieldAccess(fa) => {
                let inner = expr_children(fa.syntax());
                let field = last_lower(fa.syntax());
                match inner.first() {
                    Some(x) => format!("{}.{field}", self.fmt_expr(col, x)),
                    None => format!(".{field}"),
                }
            }
            Expr::Negate(n) => {
                let inner = expr_children(n.syntax());
                match inner.first() {
                    Some(x) => format!("-{}", self.fmt_expr(col, x)),
                    None => "-".to_string(),
                }
            }
            Expr::Paren(p) => {
                // Explicit source parens: the oracle renders the inner at the
                // SAME col (only fmtArg-*added* parens shift to col+1).
                let inner = expr_children(p.syntax());
                match inner.first() {
                    Some(x) => format!("({})", self.fmt_expr(col, x)),
                    None => "()".to_string(),
                }
            }
            Expr::List(l) => {
                self.fmt_collection(col, "[ ", ", ", "]", "[]", expr_children(l.syntax()))
            }
            Expr::Tuple(t) => {
                self.fmt_collection(col, "( ", ", ", ")", "()", expr_children(t.syntax()))
            }
            Expr::Record(r) => self.fmt_record(col, r),
            Expr::RecordUpdate(r) => self.fmt_record_update(col, r),
            Expr::Call(c) => self.fmt_call(col, c),
            Expr::Bin(b) => self.fmt_bin(col, b),
            Expr::Lambda(l) => self.fmt_lambda(col, l),
            Expr::If(i) => self.fmt_if(col, i),
            Expr::Let(l) => self.fmt_let(col, l),
            Expr::Case(c) => self.fmt_case(col, c),
        }
    }

    /// List / tuple with per-element own-line comment drains.
    fn fmt_collection(
        &mut self,
        col: usize,
        open: &str,
        sep: &str,
        close: &str,
        empty: &str,
        elems: Vec<Expr>,
    ) -> String {
        if elems.is_empty() {
            return empty.to_string();
        }
        let elem_col = col + 2;
        let mut pairs: Vec<(String, String)> = Vec::new();
        for e in &elems {
            let line = self.line_of_node(e.syntax());
            let drained = self.drain_before(col, line);
            let s = self.fmt_expr(elem_col, e);
            pairs.push((drained, s));
        }
        let any_drained = pairs.iter().any(|(d, _)| !d.is_empty());
        let items: Vec<&str> = pairs.iter().map(|(_, s)| s.as_str()).collect();
        let one_line = format!("{open}{} {}", items.join(sep), close.trim());
        let fits = !any_drained
            && col + width(&one_line) <= MAX
            && !items.iter().any(|s| s.contains('\n'));
        if fits {
            one_line
        } else {
            render_collection_with_drains(col, open, sep, close, &pairs)
        }
    }

    fn fmt_record(&mut self, col: usize, r: &syntax::ast::RecordExpr) -> String {
        let fields = record_fields(r.syntax());
        if fields.is_empty() {
            return "{}".to_string();
        }
        let elem_col = col + 2;
        let mut pairs: Vec<(String, String)> = Vec::new();
        for (name_node, value) in &fields {
            let line = line_of(
                &self.line_starts,
                usize::from(name_node.text_range().start()),
            );
            let drained = self.drain_before(col, line);
            let name = name_node.text().to_string();
            let s = match value {
                Some(v) => format!("{name} = {}", self.fmt_expr(elem_col, v)),
                None => name,
            };
            pairs.push((drained, s));
        }
        let any_drained = pairs.iter().any(|(d, _)| !d.is_empty());
        let items: Vec<&String> = pairs.iter().map(|(_, s)| s).collect();
        let one_line = format!(
            "{{ {} }}",
            items
                .iter()
                .map(|s| s.as_str())
                .collect::<Vec<_>>()
                .join(", ")
        );
        let fits = !any_drained
            && col + width(&one_line) <= MAX
            && !items.iter().any(|s| s.contains('\n'));
        if fits {
            one_line
        } else {
            render_collection_with_drains(col, "{ ", ", ", "}", &pairs)
        }
    }

    fn fmt_record_update(&mut self, col: usize, r: &syntax::ast::RecordUpdate) -> String {
        let base = record_update_base(r.syntax());
        let fields = record_fields(r.syntax());
        let elem_col = col + 2;
        let mut pairs: Vec<(String, String)> = Vec::new();
        for (name_node, value) in &fields {
            let line = line_of(
                &self.line_starts,
                usize::from(name_node.text_range().start()),
            );
            let drained = self.drain_before(col, line);
            let name = name_node.text().to_string();
            let s = match value {
                Some(v) => format!("{name} = {}", self.fmt_expr(elem_col, v)),
                None => name,
            };
            pairs.push((drained, s));
        }
        let any_drained = pairs.iter().any(|(d, _)| !d.is_empty());
        let items: Vec<&String> = pairs.iter().map(|(_, s)| s).collect();
        let one_line = format!(
            "{{ {base} | {} }}",
            items
                .iter()
                .map(|s| s.as_str())
                .collect::<Vec<_>>()
                .join(", ")
        );
        let fits = !any_drained && col + width(&one_line) <= MAX;
        if fits {
            return one_line;
        }
        let open = format!("{{ {base} | ");
        let (d0, i0) = &pairs[0];
        let mut out = if d0.is_empty() {
            format!("{open}{i0}")
        } else {
            format!("{}{}{open}{i0}", strip_leading_indent(col, d0), indent(col))
        };
        for (d, i) in &pairs[1..] {
            out.push_str(&format!("\n{d}{}, {i}", indent(col)));
        }
        out.push_str(&format!("\n{}}}", indent(col)));
        out
    }

    fn fmt_call(&mut self, col: usize, c: &syntax::ast::CallExpr) -> String {
        let parts = c.parts();
        let Some((func, args)) = parts.split_first() else {
            return String::new();
        };
        let func_str = self.fmt_expr(col, func);
        let arg_col = col + STEP;
        // Speculative render for the width decision; roll back the comment
        // stream so the real render below drains each comment exactly once.
        let snapshot = self.comments.clone();
        let flat: Vec<String> = args.iter().map(|a| self.fmt_arg(col, a)).collect();
        let one_line = format!("{func_str} {}", flat.join(" "));
        let fits = col + width(&one_line) <= MAX && !flat.iter().any(|s| s.contains('\n'));
        self.comments = snapshot;
        if fits {
            let real: Vec<String> = args.iter().map(|a| self.fmt_arg(col, a)).collect();
            format!("{func_str} {}", real.join(" "))
        } else {
            let arg_strs: Vec<String> = args.iter().map(|a| self.fmt_arg(arg_col, a)).collect();
            let mut out = func_str;
            for a in &arg_strs {
                out.push_str(&format!("\n{}{a}", indent(arg_col)));
            }
            out
        }
    }

    /// Function argument: parens around complex expressions (matches the
    /// oracle's `fmtArg`).
    fn fmt_arg(&mut self, col: usize, e: &Expr) -> String {
        match e {
            Expr::Call(_)
            | Expr::Bin(_)
            | Expr::If(_)
            | Expr::Let(_)
            | Expr::Case(_)
            | Expr::Lambda(_)
            | Expr::Negate(_) => {
                let body = self.fmt_expr(col + 1, e);
                format!("({body})")
            }
            _ => self.fmt_expr(col, e),
        }
    }

    fn fmt_bin(&mut self, col: usize, b: &syntax::ast::BinExpr) -> String {
        let (operands, ops) = flatten_bin(b);
        // Speculative single-line render of each operand, for width decisions
        // only; roll back the comment stream so the real pass drains once.
        let snapshot = self.comments.clone();
        let flat_ops: Vec<String> = operands.iter().map(|o| self.fmt_expr(col, o)).collect();
        self.comments = snapshot;
        let mut one_line = flat_ops[0].clone();
        for (k, op) in ops.iter().enumerate() {
            one_line.push_str(&format!(" {op} {}", flat_ops[k + 1]));
        }
        if col + width(&one_line) <= MAX && !one_line.contains('\n') {
            let real: Vec<String> = operands.iter().map(|o| self.fmt_expr(col, o)).collect();
            let mut line = real[0].clone();
            for (k, op) in ops.iter().enumerate() {
                line.push_str(&format!(" {op} {}", real[k + 1]));
            }
            return line;
        }
        // Greedy fill: pack the first line, then one group per line at op-col.
        let op_col = col + STEP;
        let mut out = self.fmt_expr(col, &operands[0]);
        let mut cur_width = col + last_line_width(&out);
        let mut wrapped = out.contains('\n');
        for (k, op) in ops.iter().enumerate() {
            let flat = &flat_ops[k + 1];
            let group_w = 1 + width(op) + 1 + width(flat);
            if !wrapped && !flat.contains('\n') && cur_width + group_w <= MAX {
                let real = self.fmt_expr(col, &operands[k + 1]);
                out.push_str(&format!(" {op} {real}"));
                cur_width += group_w;
            } else {
                let rhs_col = op_col + width(op) + 1;
                let rendered = self.fmt_expr(rhs_col, &operands[k + 1]);
                out.push_str(&format!("\n{}{op} {rendered}", indent(op_col)));
                wrapped = true;
            }
        }
        out
    }

    fn fmt_lambda(&mut self, col: usize, l: &syntax::ast::LambdaExpr) -> String {
        let params = l
            .params()
            .map(|pl| {
                pl.params()
                    .map(|p| self.fmt_pattern(&p))
                    .collect::<Vec<_>>()
                    .join(" ")
            })
            .unwrap_or_default();
        let body_col = col + STEP;
        let Some(body) = l.body() else {
            return format!("\\{params} ->");
        };
        let body_line = self.line_of_node(body.syntax());
        // Unbounded (matches the oracle's `drainAnyBefore`): a lambda body's
        // drain claims any not-yet-placed own-line comment above the body. A
        // decl's section-header comments are already drained (header-before-body
        // in `format`), so they can't be slurped here.
        let drained = self.drain_any_between(body_col, 0, body_line);
        let body_str = self.fmt_expr(body_col, &body);
        let one_line = format!("\\{params} -> {body_str}");
        if drained.is_empty() && !body_str.contains('\n') && col + width(&one_line) <= MAX {
            one_line
        } else {
            format!("\\{params} ->\n{drained}{}{body_str}", indent(body_col))
        }
    }

    fn fmt_if(&mut self, col: usize, i: &syntax::ast::IfExpr) -> String {
        // parts: [cond, then, else]; else may be a nested If (else-if chain).
        let parts = i.parts();
        if parts.len() < 3 {
            return String::new();
        }
        let cond = self.fmt_expr(col, &parts[0]);
        let then_b = self.fmt_expr(col + STEP, &parts[1]);
        let branch = format!("if {cond} then\n{}{then_b}", indent(col + STEP));
        let else_line = self.line_of_node(parts[2].syntax());
        let else_drained = self.drain_before(col + STEP, else_line);
        // An `else if` chain: render the nested If inline after `else `.
        if let Expr::If(_) = &parts[2] {
            let nested = self.fmt_expr(col, &parts[2]);
            format!("{branch}\n\n{}else {nested}", indent(col))
        } else {
            let else_b = self.fmt_expr(col + STEP, &parts[2]);
            format!(
                "{branch}\n\n{}else\n{else_drained}{}{else_b}",
                indent(col),
                indent(col + STEP)
            )
        }
    }

    fn fmt_let(&mut self, col: usize, l: &syntax::ast::LetExpr) -> String {
        let def_col = col + STEP;
        let bindings: Vec<SyntaxNode> = let_bindings(l.syntax());
        let mut defs = String::new();
        for b in &bindings {
            let line = self.line_of_node(b);
            let drained = self.drain_before(def_col, line);
            let s = self.fmt_binding(def_col, b);
            defs.push_str(&format!("{drained}{}{s}\n", indent(def_col)));
        }
        let body = l.body();
        let (body_drained, body_str) = match &body {
            Some(e) => {
                let line = self.line_of_node(e.syntax());
                let d = self.drain_before(def_col, line);
                (d, self.fmt_expr(def_col, e))
            }
            None => (String::new(), String::new()),
        };
        format!(
            "let\n{defs}{body_drained}{}in\n{}{body_str}",
            indent(col),
            indent(def_col)
        )
    }

    fn fmt_binding(&mut self, col: usize, b: &SyntaxNode) -> String {
        // Annotation binding: `name : Type`.
        if b.kind() == SyntaxKind::LetBinding && binding_is_anno(b) {
            let name = first_lower(b);
            let ty = type_children(b)
                .first()
                .map(|t| self.fmt_type(col, t))
                .unwrap_or_default();
            return format!("{name} : {ty}");
        }
        // Destructure binding: `pat = expr`.
        if b.kind() == SyntaxKind::DestructureBinding {
            let pat = pattern_children(b)
                .first()
                .map(|p| self.fmt_pattern(p))
                .unwrap_or_default();
            let body = expr_children(b)
                .first()
                .map(|e| self.fmt_expr(col + STEP, e));
            return self.binding_body(col, &pat, body);
        }
        // Value binding: `name pat* = expr`.
        let name = first_lower(b);
        let params = binding_params(b)
            .iter()
            .map(|p| self.fmt_pattern(p))
            .collect::<Vec<_>>()
            .join(" ");
        let lhs = if params.is_empty() {
            name
        } else {
            format!("{name} {params}")
        };
        let body = expr_children(b)
            .first()
            .map(|e| self.fmt_expr(col + STEP, e));
        self.binding_body(col, &lhs, body)
    }

    fn binding_body(&self, col: usize, lhs: &str, body: Option<String>) -> String {
        let body = body.unwrap_or_default();
        let one_line = format!("{lhs} = {body}");
        if !body.contains('\n') && col + width(&one_line) <= 76 {
            one_line
        } else {
            format!("{lhs} =\n{}{body}", indent(col + STEP))
        }
    }

    fn fmt_case(&mut self, col: usize, c: &syntax::ast::CaseExpr) -> String {
        let subj_col = col + 5;
        let subj = c
            .subject()
            .map(|s| self.fmt_expr(subj_col, &s))
            .unwrap_or_default();
        let branch_col = col + STEP;
        let mut out = format!("case {subj} of");
        for arm in c.arms() {
            let pat = arm.pattern();
            let pat_line = pat
                .as_ref()
                .map(|p| self.line_of_node(p.syntax()))
                .unwrap_or(0);
            let drained = self.drain_before(branch_col, pat_line);
            let pat_str = pat.map(|p| self.fmt_pattern(&p)).unwrap_or_default();
            let body = arm
                .body()
                .map(|b| self.fmt_expr(branch_col + STEP, &b))
                .unwrap_or_default();
            out.push_str(&format!(
                "\n\n{drained}{}{pat_str} ->\n{}{body}",
                indent(branch_col),
                indent(branch_col + STEP)
            ));
        }
        out
    }
}

// ---- free helpers --------------------------------------------------------

/// Render a `Double` the way Haskell's `show` does (the oracle uses it):
/// scientific `d.ddde±X` when the decimal exponent is `< -1` or `> 6`, else
/// fixed notation; always at least one fractional digit.
fn haskell_show_double(x: f64) -> String {
    if x == 0.0 {
        return "0.0".to_string();
    }
    if x < 0.0 {
        return format!("-{}", haskell_show_double(-x));
    }
    // Rust `{:e}` gives the shortest mantissa in `m e exp` form (no `+`).
    let sci = format!("{x:e}");
    let (mant, exp) = sci.split_once('e').unwrap_or((sci.as_str(), "0"));
    let exp_i: i32 = exp.parse().unwrap_or(0);
    if !(-1..=6).contains(&exp_i) {
        let mant = if mant.contains('.') {
            mant.to_string()
        } else {
            format!("{mant}.0")
        };
        format!("{mant}e{exp_i}")
    } else {
        let s = format!("{x}");
        if s.contains('.') {
            s
        } else {
            format!("{s}.0")
        }
    }
}

fn line_of(line_starts: &[usize], offset: usize) -> usize {
    match line_starts.binary_search(&offset) {
        Ok(i) => i,
        Err(i) => i - 1,
    }
}

fn render_comment(ind: usize, c: &Comment) -> String {
    format!("{}{}\n", indent(ind), c.text)
}

fn trim_trailing_newlines(s: &str) -> String {
    s.trim_end_matches('\n').to_string()
}

fn strip_leading_indent(col: usize, s: &str) -> String {
    let leader: String = s.chars().take(col).collect();
    if leader.len() == col && leader.chars().all(|c| c == ' ') {
        s.chars().skip(col).collect()
    } else {
        s.to_string()
    }
}

fn render_collection_with_drains(
    col: usize,
    open: &str,
    sep: &str,
    close: &str,
    pairs: &[(String, String)],
) -> String {
    let (d0, i0) = &pairs[0];
    let mut out = if d0.is_empty() {
        format!("{open}{i0}")
    } else {
        format!("{}{}{open}{i0}", strip_leading_indent(col, d0), indent(col))
    };
    for (d, i) in &pairs[1..] {
        out.push_str(&format!("\n{d}{}{sep}{i}", indent(col)));
    }
    out.push_str(&format!("\n{}{close}", indent(col)));
    out
}

/// Concatenate a node's significant (non-trivia) token texts.
fn sig_text(node: &SyntaxNode) -> String {
    node.children_with_tokens()
        .filter_map(|e| e.into_token())
        .filter(|t| !t.kind().is_trivia())
        .map(|t| t.text().to_string())
        .collect()
}

fn type_children(node: &SyntaxNode) -> Vec<Type> {
    node.children().filter_map(Type::cast).collect()
}

/// Peel redundant `TypeParen` wrappers — the oracle's type AST has no paren
/// node, so `(Html Msg)` in tail position renders as `Html Msg`.
fn unwrap_type_paren(t: &Type) -> Type {
    match t {
        Type::Paren(p) => match type_children(p.syntax()).into_iter().next() {
            Some(inner) => unwrap_type_paren(&inner),
            None => t.clone(),
        },
        _ => t.clone(),
    }
}

fn expr_children(node: &SyntaxNode) -> Vec<Expr> {
    node.children().filter_map(Expr::cast).collect()
}

fn pattern_children(node: &SyntaxNode) -> Vec<Pattern> {
    node.children().filter_map(Pattern::cast).collect()
}

fn type_var_list(node: &SyntaxNode) -> Vec<String> {
    node.children()
        .find(|n| n.kind() == SyntaxKind::TypeVarList)
        .map(|l| {
            l.children_with_tokens()
                .filter_map(|e| e.into_token())
                .filter(|t| t.kind() == SyntaxKind::LowerIdent)
                .map(|t| t.text().to_string())
                .collect()
        })
        .unwrap_or_default()
}

fn type_record_fields(node: &SyntaxNode) -> Vec<(String, Type)> {
    node.children()
        .filter(|n| n.kind() == SyntaxKind::TypeRecordField)
        .filter_map(|f| {
            let name = f
                .children_with_tokens()
                .filter_map(|e| e.into_token())
                .find(|t| t.kind() == SyntaxKind::LowerIdent)?
                .text()
                .to_string();
            let ty = f.children().find_map(Type::cast)?;
            Some((name, ty))
        })
        .collect()
}

fn row_var(node: &SyntaxNode) -> Option<String> {
    node.children()
        .find(|n| n.kind() == SyntaxKind::RowVar)
        .map(|r| sig_text(&r))
}

fn record_fields(node: &SyntaxNode) -> Vec<(SyntaxToken, Option<Expr>)> {
    node.children()
        .filter(|n| n.kind() == SyntaxKind::RecordField)
        .filter_map(|f| {
            let name = f
                .children_with_tokens()
                .filter_map(|e| e.into_token())
                .find(|t| t.kind() == SyntaxKind::LowerIdent)?;
            let value = f.children().find_map(Expr::cast);
            Some((name, value))
        })
        .collect()
}

fn record_update_base(node: &SyntaxNode) -> String {
    node.children_with_tokens()
        .filter_map(|e| e.into_token())
        .find(|t| t.kind() == SyntaxKind::LowerIdent)
        .map(|t| t.text().to_string())
        .unwrap_or_default()
}

fn record_pat_names(node: &SyntaxNode) -> Vec<String> {
    node.children_with_tokens()
        .filter_map(|e| e.into_token())
        .filter(|t| t.kind() == SyntaxKind::LowerIdent)
        .map(|t| t.text().to_string())
        .collect()
}

fn ctor_head(node: &SyntaxNode) -> String {
    // Head is the leading Upper(.Upper|.lower)* dotted name; stop at the first
    // child pattern node.
    let mut s = String::new();
    for elem in node.children_with_tokens() {
        match elem.as_token() {
            Some(t) => {
                if t.kind().is_trivia() {
                    continue;
                }
                match t.kind() {
                    SyntaxKind::UpperIdent | SyntaxKind::LowerIdent | SyntaxKind::Dot => {
                        s.push_str(t.text())
                    }
                    _ => break,
                }
            }
            None => break, // hit a child node — head is done
        }
    }
    s
}

fn alias_name(node: &SyntaxNode) -> String {
    node.children_with_tokens()
        .filter_map(|e| e.into_token())
        .filter(|t| t.kind() == SyntaxKind::LowerIdent)
        .last()
        .map(|t| t.text().to_string())
        .unwrap_or_default()
}

fn last_lower(node: &SyntaxNode) -> String {
    node.children_with_tokens()
        .filter_map(|e| e.into_token())
        .filter(|t| t.kind() == SyntaxKind::LowerIdent)
        .last()
        .map(|t| t.text().to_string())
        .unwrap_or_default()
}

fn first_lower(node: &SyntaxNode) -> String {
    node.children_with_tokens()
        .filter_map(|e| e.into_token())
        .find(|t| t.kind() == SyntaxKind::LowerIdent)
        .map(|t| t.text().to_string())
        .unwrap_or_default()
}

fn exposing_is_all(node: &SyntaxNode) -> bool {
    // `(..)` — a DotDot token directly under the exposing node.
    node.children_with_tokens()
        .filter_map(|e| e.into_token())
        .any(|t| t.kind() == SyntaxKind::DotDot)
}

fn exposed_items(node: &SyntaxNode) -> Vec<String> {
    node.children()
        .filter_map(|n| match n.kind() {
            SyntaxKind::ExposedValue => Some(sig_text(&n)),
            SyntaxKind::ExposedOperator => Some(sig_text(&n)),
            SyntaxKind::ExposedType => {
                let name = n
                    .children_with_tokens()
                    .filter_map(|e| e.into_token())
                    .find(|t| t.kind() == SyntaxKind::UpperIdent)
                    .map(|t| t.text().to_string())
                    .unwrap_or_default();
                let ctor_list = n
                    .children()
                    .find(|c| c.kind() == SyntaxKind::ExposedCtorList);
                match ctor_list {
                    None => Some(name),
                    Some(cl) => {
                        if cl
                            .children_with_tokens()
                            .filter_map(|e| e.into_token())
                            .any(|t| t.kind() == SyntaxKind::DotDot)
                        {
                            Some(format!("{name}(..)"))
                        } else {
                            let ctors: Vec<String> = cl
                                .children_with_tokens()
                                .filter_map(|e| e.into_token())
                                .filter(|t| t.kind() == SyntaxKind::UpperIdent)
                                .map(|t| t.text().to_string())
                                .collect();
                            Some(format!("{name}({})", ctors.join(", ")))
                        }
                    }
                }
            }
            _ => None,
        })
        .collect()
}

fn let_bindings(node: &SyntaxNode) -> Vec<SyntaxNode> {
    node.children()
        .filter(|n| {
            matches!(
                n.kind(),
                SyntaxKind::LetBinding | SyntaxKind::DestructureBinding
            )
        })
        .collect()
}

fn binding_is_anno(node: &SyntaxNode) -> bool {
    // `name : Type` — a Colon token and no `=`.
    let mut has_colon = false;
    let mut has_eq = false;
    for t in node.children_with_tokens().filter_map(|e| e.into_token()) {
        match t.kind() {
            SyntaxKind::Colon => has_colon = true,
            SyntaxKind::Eq => has_eq = true,
            _ => {}
        }
    }
    has_colon && !has_eq
}

fn binding_params(node: &SyntaxNode) -> Vec<Pattern> {
    node.children()
        .find(|n| n.kind() == SyntaxKind::ParamList)
        .map(|pl| pl.children().filter_map(Pattern::cast).collect())
        .unwrap_or_default()
}

/// Flatten a (possibly nested) `BinExpr` tree into source-order operands and
/// the operators between them (mirrors the oracle's flat `Binops`).
fn flatten_bin(b: &syntax::ast::BinExpr) -> (Vec<Expr>, Vec<String>) {
    let mut operands = Vec::new();
    let mut ops = Vec::new();
    flatten_bin_into(b, &mut operands, &mut ops);
    (operands, ops)
}

fn flatten_bin_into(b: &syntax::ast::BinExpr, operands: &mut Vec<Expr>, ops: &mut Vec<String>) {
    let lhs = b.lhs();
    let op = b.op().map(|t| t.text().to_string()).unwrap_or_default();
    let rhs = b.rhs();
    match lhs {
        Some(Expr::Bin(inner)) => flatten_bin_into(&inner, operands, ops),
        Some(e) => operands.push(e),
        None => {}
    }
    ops.push(op);
    match rhs {
        Some(Expr::Bin(inner)) => flatten_bin_into(&inner, operands, ops),
        Some(e) => operands.push(e),
        None => {}
    }
}

fn decl_next_lines(p: &Printer, decls: &[Decl]) -> Vec<Option<usize>> {
    let lines: Vec<usize> = decls.iter().map(|d| p.line_of_node(d.syntax())).collect();
    let mut next = Vec::with_capacity(decls.len());
    for i in 0..decls.len() {
        next.push(lines.get(i + 1).copied());
    }
    next
}

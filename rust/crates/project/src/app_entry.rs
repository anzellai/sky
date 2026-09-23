//! Structural read of a `Std.App` entry.
//!
//! A `Std.App` entry describes its app as a value: `App.app { … }` followed by
//! builder steps (`|> App.withGuard guard`, `|> secured`, `App.withRoutes r
//! app`, …), and runs it with the dispatcher `App.run`. The build needs to know
//! three things about that value without type-checking the whole program:
//!
//! * where the dispatcher is called (under whatever qualifier `Std.App` is
//!   imported as, or bare through `exposing (run)`), so it can be rewritten to
//!   the target's concrete runner;
//! * the `init` / `update` / `view` / `subscriptions` fields of the app value
//!   that is ACTUALLY passed to `run` (not a second `App.app` elsewhere in the
//!   file);
//! * every builder step applied to that value, including steps applied inside a
//!   local helper function (`secured a = a |> App.withGuard guard`).
//!
//! This module reads the CST produced by the `syntax` crate and evaluates the
//! app expression symbolically: it follows local bindings and inlines local
//! helper functions and lambdas, flattening `|>`, `<|` and direct application.
//! Anything it cannot account for is an `Err` naming the construct. It never
//! guesses and never drops a step: the caller decides, per builder name, whether
//! a step is carried, ignorable for its target, or an error.

use std::collections::{HashMap, HashSet};
use std::rc::Rc;

use syntax::ast::{self, AstNode, Expr};
use syntax::{SyntaxKind, SyntaxNode};

/// How `Std.App` is imported by the entry.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct AppImport {
    /// True when the entry has an `import Std.App …` line.
    pub imported: bool,
    /// The qualifier `Std.App` is reachable under: the `as` alias, else the
    /// last path segment `App` (unless another module's alias claims `App`).
    /// Defaults to `App` when there is no import (source fragments).
    pub qualifier: Option<String>,
    /// Names exposed bare by the import (`exposing (run, withGuard)`).
    pub bare: HashSet<String>,
    /// `exposing (..)`.
    pub expose_all: bool,
}

impl AppImport {
    /// True when `name` is reachable bare (unqualified) from `Std.App`.
    pub fn is_bare(&self, name: &str) -> bool {
        self.imported && (self.expose_all || self.bare.contains(name))
    }

    /// True when the dotted module path `module` names `Std.App` in this file.
    pub fn is_qualifier(&self, module: &str) -> bool {
        module == "Std.App" || self.qualifier.as_deref() == Some(module)
    }

    /// The qualifier to write a generated reference with.
    pub fn qualifier_or_default(&self) -> String {
        self.qualifier.clone().unwrap_or_else(|| "App".to_string())
    }
}

fn parse(src: &str) -> ast::SourceFile {
    syntax::parse(src, base::FileId(0)).tree()
}

/// Read how `Std.App` is imported (see [`AppImport`]).
pub fn app_import(src: &str) -> AppImport {
    let file = parse(src);
    let mut claims: HashMap<String, String> = HashMap::new();
    for imp in file.imports() {
        if let (Some(alias), Some(path)) = (imp.alias(), imp.name()) {
            claims.insert(alias.text().to_string(), path.text());
        }
    }
    for imp in file.imports() {
        let Some(path) = imp.name().map(|n| n.text()) else {
            continue;
        };
        if path != "Std.App" {
            continue;
        }
        let alias = imp.alias().map(|a| a.text().to_string());
        let qualifier = match alias {
            Some(a) => Some(a),
            None => match claims.get("App") {
                Some(other) if other != "Std.App" => None,
                _ => Some("App".to_string()),
            },
        };
        let mut bare = HashSet::new();
        let mut expose_all = false;
        if let Some(exp) = imp.exposing() {
            let text = exp.syntax().text().to_string();
            let inner = text
                .trim()
                .trim_start_matches("exposing")
                .trim()
                .trim_start_matches('(')
                .trim_end_matches(')')
                .trim()
                .to_string();
            if inner == ".." {
                expose_all = true;
            } else {
                for item in inner.split(',') {
                    let name: String = item
                        .trim()
                        .chars()
                        .take_while(|c| c.is_ascii_alphanumeric() || *c == '_')
                        .collect();
                    if !name.is_empty() {
                        bare.insert(name);
                    }
                }
            }
        }
        return AppImport {
            imported: true,
            qualifier,
            bare,
            expose_all,
        };
    }
    AppImport {
        imported: false,
        qualifier: Some("App".to_string()),
        bare: HashSet::new(),
        expose_all: false,
    }
}

/// Byte ranges of the module header and every import statement — references in
/// them (`exposing (run)`) are declarations, not calls.
fn header_ranges(src: &str) -> Vec<(usize, usize)> {
    let file = parse(src);
    let mut out = Vec::new();
    if let Some(h) = file.module_header() {
        let r = h.syntax().text_range();
        out.push((usize::from(r.start()), usize::from(r.end())));
    }
    for imp in file.imports() {
        let r = imp.syntax().text_range();
        out.push((usize::from(r.start()), usize::from(r.end())));
    }
    out
}

/// A reference to the bare `run` dispatcher in `src`: its byte span and, for a
/// qualified reference, the module qualifier as written.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RunRef {
    pub start: usize,
    pub end: usize,
    pub qualifier: Option<String>,
}

/// Every reference to the `Std.App` dispatcher `run` in `src` — `App.run`,
/// `A.run` under `import Std.App as A`, or bare `run` under `exposing (run)`.
/// The concrete runners (`App.runTui`, …) are different identifiers and never
/// match. Token-based, so comments and string literals never match, and it
/// works on source fragments with parse errors.
pub fn run_refs(src: &str) -> Vec<RunRef> {
    let imp = app_import(src);
    let toks: Vec<syntax::LexToken> = syntax::lex(src);
    let skip = header_ranges(src);
    let in_header = |pos: usize| skip.iter().any(|(s, e)| pos >= *s && pos < *e);
    let text = |t: &syntax::LexToken| &src[t.start as usize..t.end as usize];
    let mut out = Vec::new();
    let mut i = 0;
    while i < toks.len() {
        let t = &toks[i];
        let is_ident = matches!(t.kind, SyntaxKind::UpperIdent | SyntaxKind::LowerIdent);
        if !is_ident {
            i += 1;
            continue;
        }
        // Gather the maximal dotted chain of ADJACENT idents starting here.
        let mut parts: Vec<usize> = vec![i];
        let mut j = i;
        while j + 2 < toks.len()
            && toks[j + 1].kind == SyntaxKind::Dot
            && toks[j + 1].start == toks[j].end
            && matches!(
                toks[j + 2].kind,
                SyntaxKind::UpperIdent | SyntaxKind::LowerIdent
            )
            && toks[j + 2].start == toks[j + 1].end
            && toks[j].kind == SyntaxKind::UpperIdent
        {
            parts.push(j + 2);
            j += 2;
        }
        let preceded_by_dot =
            i > 0 && toks[i - 1].kind == SyntaxKind::Dot && toks[i - 1].end == t.start;
        let start = t.start as usize;
        let last = &toks[*parts.last().unwrap()];
        if !preceded_by_dot && !in_header(start) {
            if parts.len() >= 2 {
                let module = parts[..parts.len() - 1]
                    .iter()
                    .map(|k| text(&toks[*k]))
                    .collect::<Vec<_>>()
                    .join(".");
                if text(last) == "run"
                    && last.kind == SyntaxKind::LowerIdent
                    && imp.is_qualifier(&module)
                {
                    out.push(RunRef {
                        start,
                        end: last.end as usize,
                        qualifier: Some(module),
                    });
                }
            } else if t.kind == SyntaxKind::LowerIdent
                && text(t) == "run"
                && imp.is_bare("run")
                && !(i + 1 < toks.len()
                    && toks[i + 1].kind == SyntaxKind::Dot
                    && toks[i + 1].start == t.end)
            {
                out.push(RunRef {
                    start,
                    end: t.end as usize,
                    qualifier: None,
                });
            }
        }
        i = parts.last().unwrap() + 1;
    }
    out
}

/// True when `src` calls the `Std.App` dispatcher `run` (see [`run_refs`]).
pub fn uses_run(src: &str) -> bool {
    !run_refs(src).is_empty()
}

/// Rewrite every dispatcher reference to the concrete runner `runner`
/// (`runLive`, `runTui`, …), keeping the qualifier it was written with. A bare
/// `run` becomes `<qualifier>.<runner>`.
pub fn rewrite_run(src: &str, runner: &str) -> String {
    let imp = app_import(src);
    let refs = run_refs(src);
    let mut out = String::with_capacity(src.len() + 16);
    let mut at = 0;
    for r in refs {
        out.push_str(&src[at..r.start]);
        let q = r.qualifier.unwrap_or_else(|| imp.qualifier_or_default());
        out.push_str(&format!("{q}.{runner}"));
        at = r.end;
    }
    out.push_str(&src[at..]);
    out
}

// ---- the app value ---------------------------------------------------------

/// The verbatim source of an expression the build carries into generated code,
/// with the 0-based column its first line starts at (so a multi-line argument
/// can be re-indented without changing its layout).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ArgText {
    pub text: String,
    pub col: usize,
}

impl ArgText {
    pub fn is_multiline(&self) -> bool {
        self.text.contains('\n')
    }

    /// True when the text is a plain (possibly qualified) name.
    pub fn is_name(&self) -> bool {
        let t = self.text.trim();
        !t.is_empty()
            && t.chars()
                .all(|c| c.is_ascii_alphanumeric() || c == '_' || c == '.')
            && t.chars()
                .next()
                .map(|c| c.is_ascii_alphabetic())
                .unwrap_or(false)
    }

    /// True when the whole text is one parenthesised group (`()`, `(a b)`), so
    /// it needs no further parentheses to stand as a single argument.
    pub fn is_parenthesised(&self) -> bool {
        let t = self.text.trim();
        if !(t.starts_with('(') && t.ends_with(')')) {
            return false;
        }
        let mut depth = 0i32;
        let (mut in_str, mut escaped) = (false, false);
        for (i, c) in t.char_indices() {
            if in_str {
                if escaped {
                    escaped = false;
                } else if c == '\\' {
                    escaped = true;
                } else if c == '"' {
                    in_str = false;
                }
                continue;
            }
            match c {
                '"' => in_str = true,
                '(' | '[' | '{' => depth += 1,
                ')' | ']' | '}' => {
                    depth -= 1;
                    if depth == 0 && i + 1 != t.len() {
                        return false;
                    }
                }
                _ => {}
            }
        }
        true
    }

    /// The text on ONE line: each line's `--` comment dropped, lines joined by a
    /// space. Only safe for layout-free expressions (a route list).
    pub fn flat(&self) -> String {
        self.text
            .lines()
            .map(|l| strip_line_comment(l.trim()).trim().to_string())
            .filter(|l| !l.is_empty())
            .collect::<Vec<_>>()
            .join(" ")
    }

    /// A top-level binding `name =` whose body is this text, re-indented so the
    /// relative columns of every line are preserved (a `case` / `let` inside a
    /// multi-line lambda keeps its layout).
    pub fn render_binding(&self, name: &str) -> String {
        let lines: Vec<&str> = self.text.lines().collect();
        let indent = |l: &str| l.len() - l.trim_start().len();
        let min_cont = lines
            .iter()
            .skip(1)
            .filter(|l| !l.trim().is_empty())
            .map(|l| indent(l))
            .min()
            .unwrap_or(self.col);
        let base = self.col.min(min_cont);
        let mut out = format!(
            "{name} =\n{}{}\n",
            " ".repeat(4 + self.col - base),
            lines.first().copied().unwrap_or("").trim_start()
        );
        for l in lines.iter().skip(1) {
            if l.trim().is_empty() {
                out.push('\n');
            } else {
                out.push_str(&" ".repeat(4 + indent(l) - base));
                out.push_str(l.trim_start());
                out.push('\n');
            }
        }
        out
    }
}

/// Truncate `s` at its first `--` that is not inside a `"…"` string literal.
fn strip_line_comment(s: &str) -> &str {
    let bytes = s.as_bytes();
    let (mut in_str, mut escaped) = (false, false);
    let mut i = 0;
    while i < bytes.len() {
        let c = bytes[i];
        if in_str {
            if escaped {
                escaped = false;
            } else if c == b'\\' {
                escaped = true;
            } else if c == b'"' {
                in_str = false;
            }
        } else if c == b'-' && i + 1 < bytes.len() && bytes[i + 1] == b'-' {
            return &s[..i];
        } else if c == b'"' {
            in_str = true;
        }
        i += 1;
    }
    s
}

/// One builder step applied to the app value (`withGuard`, …) with its
/// arguments. `args` is empty when the caller did not ask for them (a step it
/// ignores for its target); `arity` is always the number written.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct AppStep {
    pub name: String,
    pub arity: usize,
    pub args: Vec<ArgText>,
}

/// The app value passed to `run`, read structurally.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct AppValue {
    /// The `Std.App` constructor: `app`, `web`, `cli` or `tui`.
    pub builder: String,
    /// The constructor's record fields, in source order.
    pub fields: Vec<(String, ArgText)>,
    /// Builder steps in application order.
    pub steps: Vec<AppStep>,
    /// Zero-parameter top-level bindings the value flowed through
    /// (`appDef = App.app … |> …`). The caller may drop them from a derived
    /// entry that replaces `main`.
    pub value_bindings: Vec<String>,
    /// How `Std.App` is imported.
    pub import: AppImport,
}

/// A value bound in the symbolic environment.
#[derive(Clone)]
enum Bound {
    /// An expression, evaluated lazily in its own environment.
    Expr(Expr, Env),
    /// A function (a let-bound or lambda helper): params + body.
    Fn(Vec<Option<String>>, Expr, Env),
    /// The app value under construction.
    App(Box<Partial>),
    /// A local whose value the build cannot know (an enclosing lambda or
    /// `case` parameter): any use of it in carried code is an error.
    Opaque,
}

#[derive(Clone, Default)]
struct Env(Rc<HashMap<String, Bound>>);

impl Env {
    fn get(&self, name: &str) -> Option<&Bound> {
        self.0.get(name)
    }
    fn with(&self, binds: Vec<(String, Bound)>) -> Env {
        let mut m = (*self.0).clone();
        for (k, v) in binds {
            m.insert(k, v);
        }
        Env(Rc::new(m))
    }
}

#[derive(Clone)]
struct Partial {
    builder: String,
    fields: Vec<(String, ArgText)>,
    steps: Vec<AppStep>,
}

struct Reader<'a> {
    src: &'a str,
    imp: AppImport,
    decls: HashMap<String, ast::ValueDecl>,
    needs_args: &'a dyn Fn(&str) -> bool,
    value_bindings: Vec<String>,
    depth: usize,
}

const MAX_DEPTH: usize = 64;

/// Byte offset of the first non-trivia token of `n` (a node's range can start
/// at leading whitespace / comments the parser attached to it).
fn content_start(n: &SyntaxNode) -> usize {
    n.descendants_with_tokens()
        .filter_map(|e| e.into_token())
        .find(|t| !t.kind().is_trivia())
        .map(|t| usize::from(t.text_range().start()))
        .unwrap_or_else(|| usize::from(n.text_range().start()))
}

fn node_text(n: &SyntaxNode) -> String {
    n.text().to_string().trim().to_string()
}

fn strip_parens(e: Expr) -> Expr {
    let mut cur = e;
    while let Expr::Paren(p) = &cur {
        match p.syntax().children().find_map(Expr::cast) {
            Some(inner) => cur = inner,
            None => break,
        }
    }
    cur
}

/// The dotted text of a qualified reference without whitespace (`App.withGuard`).
fn qual_text(e: &Expr) -> String {
    e.syntax()
        .text()
        .to_string()
        .chars()
        .filter(|c| !c.is_whitespace())
        .collect()
}

fn split_qual(q: &str) -> (&str, &str) {
    match q.rsplit_once('.') {
        Some((m, n)) => (m, n),
        None => ("", q),
    }
}

fn pattern_names(params: Option<ast::ParamList>) -> Result<Vec<Option<String>>, String> {
    let mut out = Vec::new();
    if let Some(pl) = params {
        for p in pl.params() {
            match p {
                ast::Pattern::Var(v) => out.push(Some(node_text(v.syntax()))),
                ast::Pattern::Wildcard(_) => out.push(None),
                other => {
                    return Err(format!(
                        "the parameter `{}` is a pattern; the build reads only plain \
                         parameter names in a function applied to the App value",
                        node_text(other.syntax())
                    ))
                }
            }
        }
    }
    Ok(out)
}

impl<'a> Reader<'a> {
    fn col_of(&self, n: &SyntaxNode) -> usize {
        let off = content_start(n);
        let line_start = self.src[..off].rfind('\n').map(|i| i + 1).unwrap_or(0);
        off - line_start
    }

    fn enter(&mut self, what: &str) -> Result<(), String> {
        self.depth += 1;
        if self.depth > MAX_DEPTH {
            return Err(format!(
                "the App value is built through too many nested helpers (at `{what}`)"
            ));
        }
        Ok(())
    }

    /// The carried source text of `e`: follows a parameter / local that is
    /// bound to an expression, and rejects any other reference to a local the
    /// generated top-level code cannot see.
    fn arg_text(&mut self, e: &Expr, env: &Env, what: &str) -> Result<ArgText, String> {
        self.enter(what)?;
        let r = self.arg_text_inner(e, env, what);
        self.depth -= 1;
        r
    }

    fn arg_text_inner(&mut self, e: &Expr, env: &Env, what: &str) -> Result<ArgText, String> {
        let inner = strip_parens(e.clone());
        if let Expr::Ref(r) = &inner {
            let name = node_text(r.syntax());
            match env.get(&name) {
                Some(Bound::Expr(e2, env2)) => {
                    let (e2, env2) = (e2.clone(), env2.clone());
                    return self.arg_text(&e2, &env2, what);
                }
                Some(Bound::App(_)) => {
                    return Err(format!(
                        "the App value itself is passed as the `{what}` argument; the build \
                         cannot read that"
                    ))
                }
                Some(Bound::Fn(..)) => {
                    return Err(format!(
                        "the `{what}` argument `{name}` is a local function; move it to a \
                         top-level definition so the client build can reference it"
                    ))
                }
                Some(Bound::Opaque) | None => {}
            }
        }
        // Names bound INSIDE the argument (lambda params, let names, case
        // patterns) are its own; any other name must be top-level.
        let node = e.syntax();
        let mut inside: HashSet<String> = HashSet::new();
        for d in node.descendants() {
            match d.kind() {
                SyntaxKind::PatVar => {
                    inside.insert(node_text(&d));
                }
                SyntaxKind::LetBinding => {
                    if let Some(b) = ast::LetBinding::cast(d.clone()) {
                        if let Some(n) = b.name() {
                            inside.insert(n.text().to_string());
                        }
                    }
                }
                _ => {}
            }
        }
        for d in node.descendants() {
            if d.kind() != SyntaxKind::RefExpr {
                continue;
            }
            let name = node_text(&d);
            if inside.contains(&name) {
                continue;
            }
            if env.get(&name).is_some() {
                return Err(format!(
                    "the `{what}` argument `{}` uses `{name}`, a parameter or local binding of \
                     the code that builds the App value; the client build places this argument \
                     at top level, where `{name}` does not exist. Pass a top-level function \
                     (or name it with a top-level definition) instead",
                    one_line(&node_text(node))
                ));
            }
        }
        Ok(ArgText {
            text: node.text().to_string().trim().to_string(),
            col: self.col_of(node),
        })
    }

    fn decl(&self, name: &str) -> Option<ast::ValueDecl> {
        self.decls.get(name).cloned()
    }

    /// Evaluate `e` to the app value.
    fn eval(&mut self, e: &Expr, env: &Env) -> Result<Partial, String> {
        self.enter(&one_line(&node_text(e.syntax())))?;
        let r = self.eval_inner(e, env);
        self.depth -= 1;
        r
    }

    fn eval_inner(&mut self, e: &Expr, env: &Env) -> Result<Partial, String> {
        let e = strip_parens(e.clone());
        match &e {
            Expr::Ref(r) => {
                let name = node_text(r.syntax());
                match env.get(&name).cloned() {
                    Some(Bound::App(p)) => return Ok(*p),
                    Some(Bound::Expr(e2, env2)) => return self.eval(&e2, &env2),
                    Some(Bound::Fn(..)) => {
                        return Err(format!("`{name}` is a function, not an App value"))
                    }
                    Some(Bound::Opaque) => {
                        return Err(format!(
                            "`{name}` is a lambda or `case` parameter; the build cannot know \
                             which App value it holds"
                        ))
                    }
                    None => {}
                }
                let Some(d) = self.decl(&name) else {
                    return Err(format!(
                        "`{name}` is not an App value defined in this module; the build reads \
                         the App value from this file only"
                    ));
                };
                let params = pattern_names(d.params())?;
                if !params.is_empty() {
                    return Err(format!(
                        "`{name}` takes parameters, so it is not an App value by itself"
                    ));
                }
                let body = d.body().ok_or_else(|| format!("`{name}` has no body"))?;
                if !self.value_bindings.contains(&name) {
                    self.value_bindings.push(name.clone());
                }
                self.eval(&body, &Env::default())
            }
            Expr::Let(l) => {
                let env2 = self.bind_let(l, env)?;
                let body = l
                    .body()
                    .ok_or_else(|| "a `let` without an `in` body".to_string())?;
                self.eval(&body, &env2)
            }
            Expr::Bin(b) => {
                let op = b.op().map(|t| t.text().to_string()).unwrap_or_default();
                let (lhs, rhs) = match (b.lhs(), b.rhs()) {
                    (Some(l), Some(r)) => (l, r),
                    _ => return Err("an incomplete operator expression".to_string()),
                };
                match op.as_str() {
                    "|>" => {
                        let app = self.eval(&lhs, env)?;
                        self.apply(&rhs, env, Vec::new(), app)
                    }
                    "<|" => {
                        let app = self.eval(&rhs, env)?;
                        self.apply(&lhs, env, Vec::new(), app)
                    }
                    _ => Err(format!(
                        "the operator `{op}` in `{}` does not build an App value",
                        one_line(&node_text(e.syntax()))
                    )),
                }
            }
            Expr::Call(c) => {
                let parts = c.parts();
                if parts.len() < 2 {
                    return Err("an incomplete application".to_string());
                }
                let head = strip_parens(parts[0].clone());
                if let Some(builder) = self.constructor_name(&head) {
                    if parts.len() != 2 {
                        return Err(format!(
                            "`{}` takes exactly one record argument",
                            qual_text(&head)
                        ));
                    }
                    let fields = self.record_fields(&parts[1], env)?;
                    return Ok(Partial {
                        builder,
                        fields,
                        steps: Vec::new(),
                    });
                }
                // A local function applied to ALL its parameters: bind them and
                // read its body (`mkApp db`, `secured appDef`).
                if let Some((params, body, fenv, _)) = self.resolve_fn(&head, env)? {
                    if params.len() == parts.len() - 1 {
                        let binds = params
                            .iter()
                            .zip(parts[1..].iter())
                            .filter_map(|(p, a)| {
                                p.as_ref()
                                    .map(|p| (p.clone(), Bound::Expr(a.clone(), env.clone())))
                            })
                            .collect();
                        return self.eval(&body, &fenv.with(binds));
                    }
                }
                // `f a1 … an app`: the LAST argument is the App value.
                let last = parts.last().unwrap().clone();
                let app = self.eval(&last, env)?;
                let pre: Vec<(Expr, Env)> = parts[1..parts.len() - 1]
                    .iter()
                    .map(|p| (p.clone(), env.clone()))
                    .collect();
                self.apply(&parts[0], env, pre, app)
            }
            other => Err(format!(
                "the build cannot read `{}` as an App value; build it from `App.app {{ … }}` \
                 and builder steps (`|> App.with…`)",
                one_line(&node_text(other.syntax()))
            )),
        }
    }

    /// The parameters + body of `head` when it names a function defined in this
    /// module (a top-level function, a let-bound function, or a lambda).
    #[allow(clippy::type_complexity)]
    fn resolve_fn(
        &self,
        head: &Expr,
        env: &Env,
    ) -> Result<Option<(Vec<Option<String>>, Expr, Env, String)>, String> {
        match head {
            Expr::Ref(r) => {
                let name = node_text(r.syntax());
                match env.get(&name) {
                    Some(Bound::Fn(params, body, fenv)) => {
                        return Ok(Some((params.clone(), body.clone(), fenv.clone(), name)))
                    }
                    Some(_) => return Ok(None),
                    None => {}
                }
                match self.decl(&name) {
                    Some(d) => {
                        let params = pattern_names(d.params())?;
                        if params.is_empty() {
                            return Ok(None);
                        }
                        let body = d.body().ok_or_else(|| format!("`{name}` has no body"))?;
                        Ok(Some((params, body, Env::default(), name)))
                    }
                    None => Ok(None),
                }
            }
            Expr::Lambda(l) => {
                let params = pattern_names(l.params())?;
                let body = l
                    .body()
                    .ok_or_else(|| "a lambda without a body".to_string())?;
                Ok(Some((params, body, env.clone(), "a lambda".to_string())))
            }
            _ => Ok(None),
        }
    }

    /// `app` / `web` / `cli` / `tui` when `head` names a `Std.App` constructor.
    fn constructor_name(&self, head: &Expr) -> Option<String> {
        let name = match head {
            Expr::QualRef(_) => {
                let q = qual_text(head);
                let (m, n) = split_qual(&q);
                if !self.imp.is_qualifier(m) {
                    return None;
                }
                n.to_string()
            }
            Expr::Ref(r) => {
                let n = node_text(r.syntax());
                if !self.imp.is_bare(&n) {
                    return None;
                }
                n
            }
            _ => return None,
        };
        matches!(name.as_str(), "app" | "web" | "cli" | "tui").then_some(name)
    }

    fn record_fields(&mut self, e: &Expr, env: &Env) -> Result<Vec<(String, ArgText)>, String> {
        let inner = strip_parens(e.clone());
        match &inner {
            Expr::Record(r) => {
                let mut out = Vec::new();
                for f in r.fields() {
                    let name = f
                        .name()
                        .map(|t| t.text().to_string())
                        .ok_or_else(|| "a record field without a name".to_string())?;
                    let v = f
                        .value()
                        .ok_or_else(|| format!("the record field `{name}` has no value"))?;
                    let t = self.arg_text(&v, env, &name)?;
                    out.push((name, t));
                }
                Ok(out)
            }
            Expr::Ref(r) => {
                let name = node_text(r.syntax());
                if let Some(Bound::Expr(e2, env2)) = env.get(&name).cloned() {
                    return self.record_fields(&e2, &env2);
                }
                match self.decl(&name) {
                    Some(d) if d.params().map(|p| p.params().count()).unwrap_or(0) == 0 => {
                        let body = d.body().ok_or_else(|| format!("`{name}` has no body"))?;
                        self.record_fields(&body, &Env::default())
                    }
                    _ => Err(format!(
                        "the App record `{name}` is not a record defined in this module"
                    )),
                }
            }
            other => Err(format!(
                "the App constructor argument `{}` is not a record literal; write \
                 `App.app {{ init = …, update = …, view = …, subscriptions = … }}`",
                one_line(&node_text(other.syntax()))
            )),
        }
    }

    fn bind_let(&mut self, l: &ast::LetExpr, env: &Env) -> Result<Env, String> {
        let mut binds = Vec::new();
        for b in l.bindings() {
            let Some(name) = b.name().map(|t| t.text().to_string()) else {
                continue; // a destructuring binding: names it binds shadow nothing we read
            };
            let Some(body) = b.body() else {
                continue; // an annotation line
            };
            let params = pattern_names(b.syntax().children().find_map(ast::ParamList::cast))?;
            if params.is_empty() {
                binds.push((name, Bound::Expr(body, env.clone())));
            } else {
                binds.push((name, Bound::Fn(params, body, env.clone())));
            }
        }
        Ok(env.with(binds))
    }

    /// Apply the function expression `f` (with leading arguments `pre`) to the
    /// app value.
    fn apply(
        &mut self,
        f: &Expr,
        env: &Env,
        pre: Vec<(Expr, Env)>,
        app: Partial,
    ) -> Result<Partial, String> {
        self.enter(&one_line(&node_text(f.syntax())))?;
        let r = self.apply_inner(f, env, pre, app);
        self.depth -= 1;
        r
    }

    fn apply_inner(
        &mut self,
        f: &Expr,
        env: &Env,
        pre: Vec<(Expr, Env)>,
        app: Partial,
    ) -> Result<Partial, String> {
        let f = strip_parens(f.clone());
        match &f {
            Expr::Call(c) => {
                let parts = c.parts();
                let mut args: Vec<(Expr, Env)> = parts[1..]
                    .iter()
                    .map(|p| (p.clone(), env.clone()))
                    .collect();
                args.extend(pre);
                self.apply(&parts[0], env, args, app)
            }
            Expr::QualRef(_) => {
                let q = qual_text(&f);
                let (m, n) = split_qual(&q);
                if self.imp.is_qualifier(m) {
                    self.step(n, pre, app)
                } else {
                    Err(format!(
                        "`{q}` is applied to the App value, but it is defined in another module; \
                         the build cannot see what it does to the app. Apply the `App.with…` \
                         builders in this file (a local helper function is fine)"
                    ))
                }
            }
            Expr::Ref(r) => {
                let name = node_text(r.syntax());
                match env.get(&name).cloned() {
                    Some(Bound::Fn(params, body, fenv)) => {
                        return self.call_fn(&name, &params, &body, &fenv, pre, app)
                    }
                    Some(Bound::Expr(e2, env2)) => return self.apply(&e2, &env2, pre, app),
                    Some(Bound::App(_)) => {
                        return Err(format!(
                            "`{name}` holds the App value but is applied as a function"
                        ))
                    }
                    Some(Bound::Opaque) => {
                        return Err(format!(
                            "`{name}` is a lambda or `case` parameter applied to the App value; \
                             the build cannot see what it does to the app"
                        ))
                    }
                    None => {}
                }
                if self.imp.is_bare(&name) && self.decl(&name).is_none() {
                    return self.step(&name, pre, app);
                }
                let Some(d) = self.decl(&name) else {
                    return Err(format!(
                        "`{name}` is applied to the App value, but it is not a function defined \
                         in this module; the build cannot see what it does to the app"
                    ));
                };
                let params = pattern_names(d.params())?;
                let body = d.body().ok_or_else(|| format!("`{name}` has no body"))?;
                self.call_fn(&name, &params, &body, &Env::default(), pre, app)
            }
            Expr::Lambda(l) => {
                let params = pattern_names(l.params())?;
                let body = l
                    .body()
                    .ok_or_else(|| "a lambda without a body".to_string())?;
                self.call_fn("a lambda", &params, &body, env, pre, app)
            }
            other => Err(format!(
                "the build cannot read `{}`, which is applied to the App value; use \
                 `App.with…` builders or a local helper function",
                one_line(&node_text(other.syntax()))
            )),
        }
    }

    fn call_fn(
        &mut self,
        name: &str,
        params: &[Option<String>],
        body: &Expr,
        fenv: &Env,
        pre: Vec<(Expr, Env)>,
        app: Partial,
    ) -> Result<Partial, String> {
        let total = pre.len() + 1;
        if params.len() > total {
            return Err(format!(
                "`{name}` takes {} parameters but is applied to the App value with {} \
                 argument(s); the build reads only fully applied helpers",
                params.len(),
                total
            ));
        }
        let mut binds = Vec::new();
        let mut rest: Vec<(Expr, Env)> = Vec::new();
        let mut app_slot = Some(app);
        for (i, (e, env)) in pre.into_iter().enumerate() {
            match params.get(i) {
                Some(Some(p)) => binds.push((p.clone(), Bound::Expr(e, env))),
                Some(None) => {}
                None => rest.push((e, env)),
            }
        }
        if params.len() == total {
            let app = app_slot.take().unwrap();
            match &params[total - 1] {
                Some(p) => binds.push((p.clone(), Bound::App(Box::new(app)))),
                None => {
                    return Err(format!(
                        "`{name}` ignores the App value it is given (`_` parameter)"
                    ))
                }
            }
            let env2 = fenv.with(binds);
            self.eval(body, &env2)
        } else {
            let env2 = fenv.with(binds);
            self.apply(body, &env2, rest, app_slot.take().unwrap())
        }
    }

    fn step(
        &mut self,
        name: &str,
        pre: Vec<(Expr, Env)>,
        mut app: Partial,
    ) -> Result<Partial, String> {
        if !name.starts_with("with") {
            return Err(format!(
                "`App.{name}` is applied to the App value, but it is not an `App.with…` builder"
            ));
        }
        let arity = pre.len();
        let mut args = Vec::new();
        if (self.needs_args)(name) {
            for (e, env) in &pre {
                args.push(self.arg_text(e, env, name)?);
            }
        }
        app.steps.push(AppStep {
            name: name.to_string(),
            arity,
            args,
        });
        Ok(app)
    }
}

fn one_line(s: &str) -> String {
    let flat = s.split_whitespace().collect::<Vec<_>>().join(" ");
    if flat.chars().count() > 80 {
        let cut: String = flat.chars().take(77).collect();
        format!("{cut}...")
    } else {
        flat
    }
}

/// Read the app value passed to the `Std.App` dispatcher `run` in `src`.
///
/// `needs_args(builder)` says whether the caller will use a builder step's
/// arguments; the arguments of other steps are not read (so an ignored step
/// never fails on an argument the caller would not carry).
///
/// When `src` has no `run` call, the legacy form is read: a top-level `app`
/// binding.
pub fn read_app_value(src: &str, needs_args: &dyn Fn(&str) -> bool) -> Result<AppValue, String> {
    let file = parse(src);
    let imp = app_import(src);
    let mut decls: HashMap<String, ast::ValueDecl> = HashMap::new();
    for d in file.decls() {
        if let ast::Decl::Value(v) = d {
            if let Some(n) = v.name() {
                decls.entry(n.text().to_string()).or_insert(v);
            }
        }
    }
    let mut reader = Reader {
        src,
        imp: imp.clone(),
        decls,
        needs_args,
        value_bindings: Vec::new(),
        depth: 0,
    };
    let refs = run_refs(src);
    let q = imp.qualifier_or_default();
    let partial = if refs.len() > 1 {
        return Err(format!(
            "the entry calls `{q}.run` {} times; a `Std.App` entry runs exactly one app",
            refs.len()
        ));
    } else if let Some(r) = refs.first() {
        // The CST node of the reference, then its argument.
        let root = file.syntax().clone();
        let node = root
            .token_at_offset(syntax::TextSize::from(r.start as u32))
            .right_biased()
            .and_then(|t| {
                t.parent_ancestors()
                    .find(|n| matches!(n.kind(), SyntaxKind::QualRefExpr | SyntaxKind::RefExpr))
            })
            .ok_or_else(|| format!("`{q}.run` is not in an expression the build can read"))?;
        let mut cur = node.clone();
        while let Some(p) = cur.parent() {
            if p.kind() == SyntaxKind::ParenExpr {
                cur = p;
            } else {
                break;
            }
        }
        let parent = cur
            .parent()
            .ok_or_else(|| format!("`{q}.run` is not applied to an App value"))?;
        let arg: Expr = match parent.kind() {
            SyntaxKind::CallExpr => {
                let c = ast::CallExpr::cast(parent.clone()).unwrap();
                let parts = c.parts();
                if parts.first().map(|p| p.syntax() == &cur).unwrap_or(false) && parts.len() == 2 {
                    parts[1].clone()
                } else {
                    return Err(format!(
                        "`{q}.run` must be applied to exactly one App value"
                    ));
                }
            }
            SyntaxKind::BinExpr => {
                let b = ast::BinExpr::cast(parent.clone()).unwrap();
                let op = b.op().map(|t| t.text().to_string()).unwrap_or_default();
                match (op.as_str(), b.lhs(), b.rhs()) {
                    ("<|", Some(l), Some(rhs)) if l.syntax() == &cur => rhs,
                    ("|>", Some(lhs), Some(rr)) if rr.syntax() == &cur => lhs,
                    _ => return Err(format!("`{q}.run` must be applied to the App value")),
                }
            }
            _ => return Err(format!("`{q}.run` must be applied to the App value")),
        };
        // Locals in scope at the call: every enclosing `let` (read), and every
        // enclosing lambda / `case` parameter (opaque — carried code must not
        // use them), outermost first.
        let mut scopes: Vec<SyntaxNode> = node
            .ancestors()
            .filter(|n| {
                matches!(
                    n.kind(),
                    SyntaxKind::LetExpr | SyntaxKind::LambdaExpr | SyntaxKind::MatchArm
                )
            })
            .collect();
        scopes.reverse();
        let mut env = Env::default();
        for sc in &scopes {
            if let Some(l) = ast::LetExpr::cast(sc.clone()) {
                env = reader.bind_let(&l, &env)?;
            } else {
                let mut names = Vec::new();
                let pats: Vec<SyntaxNode> = if sc.kind() == SyntaxKind::LambdaExpr {
                    sc.children()
                        .filter(|c| c.kind() == SyntaxKind::ParamList)
                        .collect()
                } else {
                    ast::MatchArm::cast(sc.clone())
                        .and_then(|a| a.pattern())
                        .map(|p| vec![p.syntax().clone()])
                        .unwrap_or_default()
                };
                for p in pats {
                    for d in p.descendants() {
                        if d.kind() == SyntaxKind::PatVar {
                            names.push((node_text(&d), Bound::Opaque));
                        }
                    }
                }
                env = env.with(names);
            }
        }
        reader.eval(&arg, &env)?
    } else if reader.decls.contains_key("app") {
        let root_ref = syntax::parse("x = app\n", base::FileId(0))
            .tree()
            .decls()
            .find_map(|d| match d {
                ast::Decl::Value(v) => v.body(),
                _ => None,
            })
            .ok_or_else(|| "internal: could not build a reference to `app`".to_string())?;
        reader.eval(&root_ref, &Env::default())?
    } else {
        return Err(format!(
            "no `{q}.run <app>` call found; write `main = {q}.run app`"
        ));
    };
    Ok(AppValue {
        builder: partial.builder,
        fields: partial.fields,
        steps: partial.steps,
        value_bindings: reader.value_bindings,
        import: imp,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    const HEAD: &str = "module Main exposing (main)\n\nimport Std.App as App\n\n\n";

    fn all(_: &str) -> bool {
        true
    }

    #[test]
    fn follows_a_helper_and_keeps_step_order() {
        let src = format!(
            "{HEAD}appDef =\n    App.app {{ init = init, update = update, view = view, subscriptions = subs }}\n        |> App.withNotFound ()\n        |> secured\n        |> App.withConfig cfg\n\n\nsecured a =\n    a |> App.withGuard guard\n\n\nmain =\n    App.run appDef\n"
        );
        let v = read_app_value(&src, &all).unwrap();
        let names: Vec<&str> = v.steps.iter().map(|s| s.name.as_str()).collect();
        assert_eq!(names, ["withNotFound", "withGuard", "withConfig"]);
        assert_eq!(v.steps[1].args[0].text, "guard");
        assert_eq!(v.value_bindings, ["appDef"]);
        assert_eq!(v.builder, "app");
    }

    #[test]
    fn a_param_used_inside_a_larger_argument_fails_closed() {
        let src = format!(
            "{HEAD}appDef =\n    wrap guard (App.app {{ init = init, update = update, view = view, subscriptions = subs }})\n\n\nwrap g a =\n    a |> App.withGuard (\\m mo -> g m mo)\n\n\nmain =\n    App.run appDef\n"
        );
        let e = read_app_value(&src, &all).unwrap_err();
        assert!(e.contains("`g`"), "{e}");
    }

    #[test]
    fn run_refs_skip_comments_strings_and_import_lines() {
        let src = "module Main exposing (main)\n\nimport Std.App as App exposing (run)\n\n\n-- App.run in a comment\nmain =\n    let\n        s = \"App.run\"\n    in\n    run appDef\n";
        let refs = run_refs(src);
        assert_eq!(refs.len(), 1, "{refs:?}");
        assert_eq!(&src[refs[0].start..refs[0].end], "run");
    }

    #[test]
    fn render_binding_keeps_relative_layout() {
        let t = ArgText {
            text: "(\\m ->\n            case m of\n                A ->\n                    1\n            )".to_string(),
            col: 30,
        };
        let b = t.render_binding("x_");
        assert!(b.starts_with("x_ =\n"), "{b}");
        assert!(b.contains("\n    case m of\n        A ->\n"), "{b}");
    }
}

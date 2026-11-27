//! Bind lines `p <- e` in a `let` block: choosing their `andThen`
//! (docs/design/let-bind-lines.md §4, v0.32.0).
//!
//! The resolver writes a bind line as
//! `Call(Var(Res::BindAndThen), [\p -> rest, e])`. The placeholder is not a
//! name lookup. The type checker chooses the `andThen` from the type of `e`:
//! the head type constructor (after alias expansion) names a home module, and
//! that module's EXPOSED `andThen` is used, the same `Res` a hand-written
//! `M.andThen` gives. Kernel-implicit heads use a fixed table.
//!
//! The choice is made ONCE, on the check path ([`compute_bind_elaboration`],
//! the same inference the accept/reject checker runs), and recorded in a
//! [`BindElaboration`]. Every later pass reads that table: the lowering path
//! infers the body with each placeholder replaced by the chosen `Res`
//! ([`elaborate_body`]), so it instantiates the chosen def with its own
//! lowering-path scheme and never chooses again.

use crate::infer::Infer;
use crate::sig::World;
use crate::unify::{Content, FlatTy, UnionFind};
use crate::{nominal, Ty, TyVarId};
use base::{DefId, ModuleId, Name, Span};
use hir::{Body, Expr, ExprId, Pattern, Res, SkyDb};
use std::collections::HashMap;
use std::sync::Arc;

/// The `andThen` each bind line of one def uses, keyed by the `ExprId` of the
/// synthesised `Call` (the same id in the raw and the elaborated body).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct BindElaboration {
    pub choices: HashMap<ExprId, Res>,
}

impl BindElaboration {
    /// The `Res` chosen for the bind line whose `Call` is `call`.
    pub fn choice(&self, call: ExprId) -> Option<&Res> {
        self.choices.get(&call)
    }
}

/// A bind line whose `andThen` is not chosen yet: the type of `e` had no
/// known head when it was inferred.
#[derive(Clone, Debug)]
pub(crate) struct PendingBind {
    /// The synthesised `Call`.
    pub call: ExprId,
    /// `(\p -> rest) -> e -> result`, the type the chosen `andThen` must have.
    pub callee_ty: TyVarId,
    /// The type of `e`.
    pub e_ty: TyVarId,
    /// Inferring `e` reported a type error: an unknown head is its result,
    /// so it gives no `[E2015]` (the head is treated as `Ty::Error`).
    pub e_errored: bool,
    /// The span of the `<-` token.
    pub span: Option<Span>,
    /// The module of the def the bind line is in.
    pub module: Option<ModuleId>,
    /// `p <- …` as written, for the message (a simple pattern only).
    pub pat_text: String,
    /// The span of the expression after `<-`, to quote it in a message.
    pub e_span: Option<Span>,
    /// The annotated result, when the block is the whole body of an
    /// annotated def: a note tries its fix against it.
    pub expected: Option<TyVarId>,
}

/// The first bind line of the block that is the whole of `root` (through
/// the `let` segments the resolver wraps around it), when there is one.
pub(crate) fn root_bind_call(body: &Body, root: ExprId) -> Option<ExprId> {
    let mut cur = root;
    loop {
        match &body.exprs[cur] {
            Expr::Let { body: b, .. } => cur = *b,
            Expr::Call(f, args)
                if args.len() == 2 && matches!(body.exprs[*f], Expr::Var(Res::BindAndThen)) =>
            {
                return Some(cur)
            }
            _ => return None,
        }
    }
}

/// A name bound with `=` to a value whose type `M … x` has an `andThen`,
/// at one use ([`Infer::eq_bound_value`]).
pub(crate) struct EqBound {
    name: Name,
    /// The expression after `=`.
    rhs: ExprId,
    /// `M`.
    head: String,
    /// `x`, the result a bind line would give.
    pub(crate) inner: TyVarId,
    /// `M … x`, the type at this use.
    ty: TyVarId,
}

impl EqBound {
    /// The head `M` of the value's type.
    pub(crate) fn head(&self) -> &str {
        &self.head
    }
}

/// Fixed homes of the kernel-implicit heads (§4): the bare builtin names have
/// no declaring module of their own.
const KERNEL_BIND_HOMES: &[(&str, &str)] = &[
    ("Task", "Sky.Core.Task"),
    ("Maybe", "Sky.Core.Maybe"),
    ("Result", "Sky.Core.Result"),
];

/// Does `body` contain a bind line?
pub fn body_has_bind(body: &Body) -> bool {
    body.exprs
        .iter()
        .any(|(_, e)| matches!(e, Expr::Var(Res::BindAndThen)))
}

/// The bind line calls of `body`: `(call, placeholder, lambda, e)`.
pub fn bind_calls(body: &Body) -> Vec<(ExprId, ExprId, ExprId, ExprId)> {
    body.exprs
        .iter()
        .filter_map(|(id, e)| match e {
            Expr::Call(f, args)
                if args.len() == 2 && matches!(body.exprs[*f], Expr::Var(Res::BindAndThen)) =>
            {
                Some((id, *f, args[0], args[1]))
            }
            _ => None,
        })
        .collect()
}

/// `body` with every bind-line placeholder replaced by the `Res` `elab` chose
/// for it. `None` when the body has no bind line (callers keep the shared
/// body). A placeholder with no choice becomes `Res::Error`: the checker
/// refused that bind line and reported it (`[E2015]` / `[E2016]`), which is
/// what `Res::Error` means to every later pass (a diagnostic was emitted, L7).
/// So `Res::BindAndThen` never reaches a pass after inference.
pub fn elaborate_body(body: &Body, elab: &BindElaboration) -> Option<Body> {
    if !body_has_bind(body) {
        return None;
    }
    let mut out = body.clone();
    for (call, head, _, _) in bind_calls(body) {
        out.exprs[head] = Expr::Var(elab.choice(call).cloned().unwrap_or(Res::Error));
    }
    Some(out)
}

/// The bind-line choices of one def, made on the CHECK path: the same
/// inference the accept/reject checker runs for the def (against its
/// annotation when it has one), so the choice is the one the checker
/// verified. `world` is the full check world.
pub fn compute_bind_elaboration(
    world: &World,
    db: &dyn SkyDb,
    module: ModuleId,
    def: DefId,
) -> BindElaboration {
    let resolved = db.resolve(module);
    let Some(body) = resolved.bodies.get(&def) else {
        return BindElaboration::default();
    };
    if !body_has_bind(body) {
        return BindElaboration::default();
    }
    let mut infer = Infer::new(world, db).with_self_def(Some(def));
    match world.value_sigs.get(&def) {
        Some(scheme) => {
            let scheme = scheme.clone();
            infer.infer_def_against(body, &scheme);
        }
        None => {
            infer.infer_def(body);
        }
    }
    BindElaboration {
        choices: infer.take_bind_choices().into_iter().collect(),
    }
}

/// The source text of a bind pattern for a message (`Just x`), read from
/// the module that owns `self_def`. `…` when it is long or not found.
pub(crate) fn pat_text(
    db: &dyn SkyDb,
    self_def: Option<DefId>,
    body: &Body,
    p: hir::PatId,
) -> String {
    if let Pattern::Anything = &body.pats[p] {
        return "_".to_string();
    }
    let text = (|| {
        let span = body.pat_span(p)?;
        let module = db.def_loc(self_def?)?.module;
        let src = db.module_parse(module).syntax().text().to_string();
        let t = src
            .get(span.range.0 as usize..span.range.1 as usize)?
            .trim();
        (!t.is_empty() && t.len() <= 40 && !t.contains('\n')).then(|| t.to_string())
    })();
    text.unwrap_or_else(|| "…".to_string())
}

/// Why no `andThen` fits a head ([E2016]).
enum NoAndThen {
    /// The head is not a named type (a function, record, tuple or unit).
    NotNamed(String),
    /// A function whose result has a named head: an argument is missing.
    NeedsArgs { ty: String, args: usize },
    /// A named type whose declaring module is unknown.
    NoHome(String),
    /// The home module declares no `andThen`, or does not expose it.
    Missing {
        ty: String,
        module: String,
        arity: usize,
        unexposed: bool,
    },
    /// The exposed `andThen` has another shape.
    Shape {
        ty: String,
        module: String,
        arity: usize,
        found: Ty,
    },
    /// The bind line is inside the very `andThen` it would choose.
    SelfReference { ty: String, module: String },
}

/// The template `(a -> T b) -> T a -> T b`, with `T`'s other arguments named.
fn template_text(ty: &str, arity: usize) -> String {
    let prefix: Vec<String> = match arity.saturating_sub(1) {
        0 => Vec::new(),
        1 => vec!["e".to_string()],
        n => (1..=n).map(|i| format!("e{i}")).collect(),
    };
    let t = |x: &str| {
        let mut parts = vec![ty.to_string()];
        parts.extend(prefix.iter().cloned());
        parts.push(x.to_string());
        parts.join(" ")
    };
    format!("andThen : (a -> {}) -> {} -> {}", t("b"), t("a"), t("b"))
}

/// "`T` (module M) has no `andThen : …`; use `case`, or add one to M". A
/// stdlib module is not the user's to change, so it says `case` only.
fn no_fitting_and_then(
    ty: &str,
    module: &str,
    arity: usize,
    pat: &str,
    found: Option<&Ty>,
) -> String {
    let fix = if module.starts_with("Sky.") || module.starts_with("Std.") {
        "use `case`".to_string()
    } else {
        format!("use `case`, or add one to {module}")
    };
    let other = match found {
        Some(f) => format!(" (its `andThen : {}` has another shape)", f.render_pretty()),
        None => String::new(),
    };
    format!(
        "`{ty}` (module {module}) has no `{}`{other}; {fix}. The bind line `{pat} <- …` \
         needs one to chain with. See docs/errors/E2016-no-andthen.md",
        template_text(ty, arity)
    )
}

impl NoAndThen {
    /// `e` is how the message names the expression after `<-`: its source
    /// text in backticks, else "The expression after `<-`".
    fn message(&self, pat: &str, e: &str) -> String {
        match self {
            NoAndThen::NeedsArgs { ty, args } => format!(
                "{e} is a function (`{ty}`): it needs {args} more argument(s) before `<-` can \
                 run it. The bind line `{pat} <- …` needs a value whose type has an \
                 `andThen`. See docs/errors/E2016-no-andthen.md"
            ),
            NoAndThen::NotNamed(t) => format!(
                "The bind line `{pat} <- …` needs a type with an `andThen`, but the expression \
                 after `<-` is `{t}`, which has none. Use `case` or a plain `let` line. \
                 See docs/errors/E2016-no-andthen.md"
            ),
            NoAndThen::NoHome(t) => format!(
                "The bind line `{pat} <- …` chooses `andThen` from the module that declares \
                 `{t}`, and that module is not known. Use `case`. \
                 See docs/errors/E2016-no-andthen.md"
            ),
            NoAndThen::Missing {
                ty,
                module,
                arity,
                unexposed: true,
            } => format!(
                "`{ty}` (module {module}) declares `andThen` but does not expose it, so the \
                 bind line `{pat} <- …` has no `{}` to chain with. Use `case`, or expose it \
                 from {module}. See docs/errors/E2016-no-andthen.md",
                template_text(ty, *arity)
            ),
            NoAndThen::Missing {
                ty, module, arity, ..
            } => no_fitting_and_then(ty, module, *arity, pat, None),
            // The exposed `andThen` does not fit: calling it by name would
            // not fit either, so the fix is `case` or a fitting one.
            NoAndThen::Shape {
                ty,
                module,
                arity,
                found,
            } => no_fitting_and_then(ty, module, *arity, pat, Some(found)),
            NoAndThen::SelfReference { ty, module } => format!(
                "The bind line `{pat} <- …` would chain with `{module}.andThen` for `{ty}`, \
                 and it is a bind line in `andThen` itself (or in a definition that \
                 `andThen` depends on). Write this definition with `case`. \
                 See docs/errors/E2016-no-andthen.md"
            ),
        }
    }
}

/// The outcome of one attempt to choose a bind line's `andThen`.
enum Attempt {
    /// The head is still unknown: try again later.
    Unknown,
    /// Done: a choice was made, or an error was reported, or the head is
    /// `Ty::Error` (no second report).
    Done,
}

impl<'a> Infer<'a> {
    /// Try to choose the `andThen` of `pb` from the type of `e` now. On the
    /// check path the choice is recorded in `bind_choices`.
    fn attempt_bind(&mut self, pb: &PendingBind) -> Attempt {
        // Inference types come from alias-expanded signatures and annotations,
        // so the head is already expanded: no bare-name alias lookup here,
        // which could take a same-named alias of another module (D1).
        let raw = self.read_back(pb.e_ty);
        let (name, arity) = match &raw {
            Ty::Var(_) => return Attempt::Unknown,
            Ty::Error => return Attempt::Done,
            Ty::App(n, args) => (n.as_str().to_string(), args.len()),
            Ty::Fun(..) if matches!(final_result(&raw), Ty::App(..)) => {
                let mut args = 0;
                let mut t = &raw;
                while let Ty::Fun(_, r) = t {
                    args += 1;
                    t = r;
                }
                self.bind_error(
                    pb,
                    NoAndThen::NeedsArgs {
                        ty: raw.render_pretty(),
                        args,
                    },
                );
                return Attempt::Done;
            }
            other => {
                self.bind_error(pb, NoAndThen::NotNamed(other.render_pretty()));
                return Attempt::Done;
            }
        };
        let shown = nominal::strip(&name).to_string();
        if arity == 0 {
            self.bind_error(pb, NoAndThen::NotNamed(shown));
            return Attempt::Done;
        }
        let home = self.head_home(&name);
        let Some(home) = home else {
            self.bind_error(pb, NoAndThen::NoHome(shown));
            return Attempt::Done;
        };
        let Some(mid) = self.db.module_by_name(&home) else {
            self.bind_error(pb, NoAndThen::NoHome(shown));
            return Attempt::Done;
        };
        // HIR's own qualified-name lookup: the same `Res` a hand-written
        // `M.andThen` resolves to. In the bind line's own module, a bare
        // `andThen` names the module's own top-level `andThen`, exposed or not.
        let own = (pb.module == Some(mid))
            .then(|| {
                self.db
                    .resolve(mid)
                    .top_defs
                    .iter()
                    .find(|td| td.name.as_str() == "andThen")
                    .map(|td| td.def)
            })
            .flatten();
        let Some(def) = own.or_else(|| self.db.module_exports(mid).value("andThen")) else {
            let unexposed = self
                .db
                .resolve(mid)
                .top_defs
                .iter()
                .any(|td| td.name.as_str() == "andThen");
            self.bind_error(
                pb,
                NoAndThen::Missing {
                    ty: shown,
                    module: home,
                    arity,
                    unexposed,
                },
            );
            return Attempt::Done;
        };
        if self.self_def == Some(def) || self.provisional_defs.contains_key(&def) {
            self.bind_error(
                pb,
                NoAndThen::SelfReference {
                    ty: shown,
                    module: home,
                },
            );
            return Attempt::Done;
        }
        let res = Res::Def(def);
        let saved = self.cur_span;
        self.cur_span = pb.span.or(saved);
        let inst = self.infer_res(res.clone());
        let found = self.read_back_scheme(inst, false);
        if !fits_template(&found, &name, arity) {
            self.cur_span = saved;
            self.bind_error(
                pb,
                NoAndThen::Shape {
                    ty: shown,
                    module: home,
                    arity,
                    found,
                },
            );
            return Attempt::Done;
        }
        // The conversion note is for a block that mixes type constructors:
        // the continuation or the block's expected result has another head.
        // A clash inside one head (`Maybe Int` against `Maybe Float`) gets
        // the plain mismatch, as the hand-written `andThen` chain does.
        let callee = self.read_back(pb.callee_ty);
        // The other head the block needs: its result's, else the one the
        // continuation ends in.
        let block = match &callee {
            Ty::Fun(lam, rest) => {
                let result = match rest.as_ref() {
                    Ty::Fun(_, r) => r.as_ref(),
                    other => other,
                };
                [result, final_result(lam)]
                    .into_iter()
                    .find(|t| other_head(t, &name))
                    .and_then(|t| match t {
                        Ty::App(n, _) => Some(n.as_str().to_string()),
                        _ => None,
                    })
            }
            _ => None,
        };
        let snap = block.as_ref().map(|_| self.uf.clone());
        let before = self.errors.len();
        self.unify(inst, pb.callee_ty);
        if let (Some(block), Some(snap)) = (block, snap) {
            if self.errors.len() > before {
                let note = self.line_conversion_note(pb, &snap, &name, &block);
                self.append_note(before, Some(note));
            }
        }
        self.cur_span = saved;
        self.bind_choices.push((pb.call, res));
        Attempt::Done
    }

    /// The home module of a type head (§4): the fixed table for the kernel
    /// heads, the kernel-implicit homes, the module part of a qualified name,
    /// else the `Sky.Core` module of a builtin.
    pub(crate) fn head_home(&self, name: &str) -> Option<String> {
        KERNEL_BIND_HOMES
            .iter()
            .find(|(n, _)| *n == name)
            .map(|(_, m)| m.to_string())
            .or_else(|| hir::kernel_implicit_home(name).map(str::to_string))
            .or_else(|| {
                (nominal::is_qualified(name) && !nominal::is_go_type(name))
                    .then(|| name.rsplit_once('.').map(|(m, _)| m.to_string()))
                    .flatten()
            })
            // Another builtin (`List`, `Dict`, …): its `Sky.Core` module.
            .or_else(|| {
                (!nominal::is_qualified(name))
                    .then(|| format!("Sky.Core.{name}"))
                    .filter(|m| self.db.module_by_name(m).is_some())
            })
    }

    /// Does the home module of the head `name` expose `value`?
    fn home_exposes(&self, name: &str, value: &str) -> Option<ModuleId> {
        let mid = self.db.module_by_name(&self.head_home(name)?)?;
        self.db.module_exports(mid).value(value).map(|_| mid)
    }

    /// How the module of the def being checked writes a qualified name of
    /// module `mid`: its import alias (`Decode`), else `fallback`.
    fn qualifier_of(&self, mid: ModuleId, fallback: &str) -> String {
        let own = self
            .self_def
            .and_then(|d| self.db.def_loc(d))
            .map(|l| l.module);
        let Some(own) = own else {
            return fallback.to_string();
        };
        let mut names: Vec<String> = self
            .db
            .resolve(own)
            .qualifiers
            .iter()
            .filter(|(_, src)| matches!(src, hir::ImportSource::Dep(m) if *m == mid))
            .map(|(q, _)| q.to_string())
            .collect();
        names.sort_by_key(|q| (q.len(), q.clone()));
        names
            .into_iter()
            .next()
            .unwrap_or_else(|| fallback.to_string())
    }

    /// Run `f` on a copy of `snap` (a union-find taken before a failed
    /// unification), then put every piece of inference state `f` may touch
    /// back as it was. A note that names a fix is decided this way: the fix
    /// is unified on the copy, and nothing of the trial is kept.
    pub(crate) fn on_snapshot<R>(&mut self, snap: &UnionFind, f: impl FnOnce(&mut Self) -> R) -> R {
        let real = std::mem::replace(&mut self.uf, snap.clone());
        let errors = self.errors.len();
        let obligations = self.obligations.len();
        let page_ties = self.page_ties.len();
        let bound_seen = self.bound_seen.clone();
        let inferred_bounds = self.inferred_bounds.len();
        let unresolved = self.unresolved_seen;
        let span = self.cur_span;
        let out = f(self);
        self.uf = real;
        self.errors.truncate(errors);
        self.obligations.truncate(obligations);
        self.page_ties.truncate(page_ties);
        self.bound_seen = bound_seen;
        self.inferred_bounds.truncate(inferred_bounds);
        self.unresolved_seen = unresolved;
        self.cur_span = span;
        out
    }

    /// Does `f` succeed on a copy of `snap` without a type error?
    pub(crate) fn fits_on(&mut self, snap: &UnionFind, f: impl FnOnce(&mut Self) -> bool) -> bool {
        self.on_snapshot(snap, |s| {
            let before = s.errors.len();
            f(s) && s.errors.len() == before
        })
    }

    fn fun_var(&mut self, from: TyVarId, to: TyVarId) -> TyVarId {
        self.uf.fresh(Content::Structure(FlatTy::Fun(from, to)))
    }

    /// The head and the last argument of the application `v`, when it is one.
    fn app_parts(&mut self, v: TyVarId) -> Option<(Name, TyVarId)> {
        match self.uf.content(v) {
            Content::Structure(FlatTy::App(n, args)) => args.last().map(|l| (n, *l)),
            _ => None,
        }
    }

    /// The type of `chain` applied to a value of type `v`, on the current
    /// union-find: each `(module, function, holes)` is applied after `holes`
    /// leading arguments of any type (`err` in `Result.fromMaybe err`).
    /// `None` when a step does not type-check.
    fn apply_chain(&mut self, v: TyVarId, chain: &[(&str, &str, usize)]) -> Option<TyVarId> {
        let mut cur = v;
        for (module, name, holes) in chain {
            let mid = self.db.module_by_name(module)?;
            let def = self.db.module_exports(mid).value(name)?;
            let mut f = self.infer_res(Res::Def(def));
            for _ in 0..*holes {
                let (h, r) = (self.uf.fresh_flex(), self.uf.fresh_flex());
                let want = self.fun_var(h, r);
                self.uf.unify(f, want).ok()?;
                f = r;
            }
            let r = self.uf.fresh_flex();
            let want = self.fun_var(cur, r);
            self.uf.unify(f, want).ok()?;
            cur = r;
        }
        Some(cur)
    }

    /// The conversion of a value of head `from` into head `to`, as the
    /// functions to apply and the text that shows it around `arg`.
    fn conversion(
        &self,
        from: &str,
        to: &str,
        arg: &str,
    ) -> Option<(Vec<(&'static str, &'static str, usize)>, String)> {
        let q = |m: &str, f: &str| -> String {
            self.db
                .module_by_name(m)
                .map(|mid| self.qualifier_of(mid, f))
                .unwrap_or_else(|| f.to_string())
        };
        let task = || q("Sky.Core.Task", "Task");
        let result = || q("Sky.Core.Result", "Result");
        Some(match (from, to) {
            ("Maybe", "Task") => (
                vec![
                    ("Sky.Core.Result", "fromMaybe", 1),
                    ("Sky.Core.Task", "fromResult", 0),
                ],
                format!("{}.fromResult ({}.fromMaybe err {arg})", task(), result()),
            ),
            ("Result", "Task") => (
                vec![("Sky.Core.Task", "fromResult", 0)],
                format!("{}.fromResult {arg}", task()),
            ),
            ("Maybe", "Result") => (
                vec![("Sky.Core.Result", "fromMaybe", 1)],
                format!("{}.fromMaybe err {arg}", result()),
            ),
            ("Result", "Maybe") => (
                vec![("Sky.Core.Result", "toMaybe", 0)],
                format!("{}.toMaybe {arg}", result()),
            ),
            _ => return None,
        })
    }

    /// The note for a block of bind lines whose `in` expression does not have
    /// the block's type. `lam` is the inferred type of the last bind line's
    /// continuation (`p -> body`), `want` the type its `andThen` gives it
    /// (`a -> M … R`), `expected` the annotated result of a block that is a
    /// whole def's body; all are read on `snap`, the union-find before the
    /// clash, with the parameters of `lam` and `want` unified and `M … R`
    /// unified with `expected`. A fix (a conversion, or a wrap such as
    /// `Task.succeed`) is named only when the fixed expression unifies with
    /// `M … R` on a copy of it; otherwise the note gives the two types, and,
    /// for a function, how many arguments make it fit. `None` when the body's
    /// type is not known.
    pub(crate) fn body_note(
        &mut self,
        snap: &UnionFind,
        block_head: &str,
        lam: TyVarId,
        want: TyVarId,
        expected: Option<TyVarId>,
        body_text: Option<String>,
    ) -> Option<String> {
        let (prepared, body_ty, block_ty, bv, rv, body_head) = self.on_snapshot(snap, |s| {
            let (pl, bv) = s.fun_parts(lam)?;
            let (pw, rv) = s.fun_parts(want)?;
            let _ = s.uf.unify(pl, pw);
            if let Some(x) = expected {
                let _ = s.uf.unify(rv, x);
            }
            let body_ty = s.read_back(bv);
            if matches!(body_ty, Ty::Var(_) | Ty::Error) {
                return None;
            }
            let head = s.app_parts(bv).map(|(n, _)| n.as_str().to_string());
            let block_ty = s.read_back(rv);
            Some((s.uf.clone(), body_ty, block_ty, bv, rv, head))
        })?;
        let snap = &prepared;
        let b = nominal::strip(block_head).to_string();
        let v = body_text.unwrap_or_else(|| "…".to_string());
        let arg = if v.contains(' ') && !(v.starts_with('(') && v.ends_with(')')) {
            format!("({v})")
        } else {
            v.clone()
        };
        let intro = format!(
            "\n\nNote: the `in` expression has type `{}`; this block needs `{}`.",
            body_ty.render_pretty(),
            block_ty.render_pretty()
        );
        // A conversion from another head, when the converted value fits.
        if let Some(h) = body_head
            .as_deref()
            .filter(|h| !nominal::same(h, block_head))
        {
            if let Some((chain, text)) = self.conversion(nominal::strip(h), &b, &arg) {
                let fits = self.fits_on(snap, |s| {
                    s.apply_chain(bv, &chain)
                        .is_some_and(|c| s.uf.unify(c, rv).is_ok())
                });
                if fits {
                    return Some(format!("{intro} To fix it, convert it: `{text}`."));
                }
            }
        }
        // A wrap of a plain value, when the wrapped value fits.
        let wrap = match b.as_str() {
            "Task" => Some(("Task.succeed".to_string(), None)),
            "Result" => Some(("Ok".to_string(), None)),
            "Maybe" => Some(("Just".to_string(), None)),
            _ => self.home_exposes(block_head, "succeed").and_then(|mid| {
                let def = self.db.module_exports(mid).value("succeed")?;
                let fallback = if b == "Decoder" {
                    "Decode".to_string()
                } else {
                    self.head_home(block_head).unwrap_or_default()
                };
                Some((
                    format!("{}.succeed", self.qualifier_of(mid, &fallback)),
                    Some(def),
                ))
            }),
        };
        if let Some((wrap, def)) = wrap {
            let fits = self.fits_on(snap, |s| match def {
                // The home `succeed` itself, instantiated and applied.
                Some(def) => {
                    let f = s.infer_res(Res::Def(def));
                    let want = s.fun_var(bv, rv);
                    s.uf.unify(f, want).is_ok()
                }
                // `Task.succeed`, `Ok`, `Just`: `a -> M … a`, so the body
                // fits when it unifies with the block's last argument.
                None => s
                    .app_parts(rv)
                    .is_some_and(|(_, r)| s.uf.unify(bv, r).is_ok()),
            });
            if fits {
                return Some(format!(
                    "{intro} To fix it, wrap the value in `{wrap}`: `{wrap} {arg}`."
                ));
            }
        }
        // A function: the number of arguments after which it fits.
        let arrows = {
            let mut n = 0;
            let mut t = &body_ty;
            while let Ty::Fun(_, r) = t {
                n += 1;
                t = r;
            }
            n
        };
        let needs = (1..=arrows).find(|&k| {
            self.fits_on(snap, |s| {
                let mut cur = bv;
                for _ in 0..k {
                    let Some((_, r)) = s.fun_parts(cur) else {
                        return false;
                    };
                    cur = r;
                }
                s.uf.unify(cur, rv).is_ok()
            })
        });
        Some(match needs {
            Some(k) => format!("{intro} It is a function: it needs {k} more argument(s)."),
            None => intro,
        })
    }

    /// A name bound with `=` in a `let` to a value whose type `M … x` has an
    /// `andThen`, when `e` is that name and `te` its type here.
    pub(crate) fn eq_bound_value(
        &mut self,
        body: &Body,
        e: ExprId,
        te: TyVarId,
    ) -> Option<EqBound> {
        let Expr::Var(Res::Local(l)) = &body.exprs[e] else {
            return None;
        };
        let (name, rhs) = self.eq_bound.get(l).cloned()?;
        let (head, inner) = self.app_parts(te)?;
        self.home_exposes(head.as_str(), "andThen")?;
        Some(EqBound {
            name,
            rhs,
            head: head.as_str().to_string(),
            inner,
            ty: te,
        })
    }

    /// Can the parameter of the callee type `tf` refuse the value of `c`? A
    /// free variable or a parameter of the same head takes it (a cheap test,
    /// so the union-find is copied only for a call that may clash).
    pub(crate) fn param_may_refuse(&mut self, tf: TyVarId, c: &EqBound) -> bool {
        let Some((p, _)) = self.fun_parts(tf) else {
            return false;
        };
        match self.uf.content(p) {
            Content::Flex | Content::Error => false,
            Content::Structure(FlatTy::App(n, _)) => !nominal::same(n.as_str(), &c.head),
            _ => true,
        }
    }

    /// "`a` is a `Task`; to use its result, bind it with `a <- fetch 1`",
    /// when the slot `c` was used in failed with `M … x` and `fits(x)` (the
    /// same use with the result `x` in its place) type-checks on a copy of
    /// `snap`, the union-find before the clash. The note is never given when
    /// the slot takes `M … x` itself (`fits(M … x)` holds).
    pub(crate) fn bind_result_note(
        &mut self,
        body: &Body,
        c: &EqBound,
        snap: &UnionFind,
        fits: impl Fn(&mut Self, TyVarId) -> bool,
    ) -> Option<String> {
        if self.fits_on(snap, |s| fits(s, c.ty)) || !self.fits_on(snap, |s| fits(s, c.inner)) {
            return None;
        }
        let module = self
            .self_def
            .and_then(|d| self.db.def_loc(d))
            .map(|l| l.module);
        let rhs_text = self
            .span_text(module, body.expr_span(c.rhs))
            .unwrap_or_else(|| "…".to_string());
        Some(format!(
            "\n\nNote: `{}` is a `{}`; to use its result, bind it with `{} <- {rhs_text}`.",
            c.name.as_str(),
            nominal::strip(&c.head),
            c.name.as_str()
        ))
    }

    /// The note on a bind line `p <- e` whose `e` has the head `name` while
    /// the block needs another head `block`. It names the conversion of the
    /// line when the converted `e` chains with `block`'s `andThen` on a copy
    /// of `snap` (the union-find before the clash), else only the two heads.
    fn line_conversion_note(
        &mut self,
        pb: &PendingBind,
        snap: &UnionFind,
        name: &str,
        block: &str,
    ) -> String {
        let shown = nominal::strip(name).to_string();
        let b = nominal::strip(block).to_string();
        let plain = format!(
            "\n\nNote: this bind line chains with the `andThen` of `{shown}`, and the block \
             is a `{b}` block. One block of bind lines chains one type."
        );
        let e_text = self
            .span_text(pb.module, pb.e_span)
            .unwrap_or_else(|| "…".to_string());
        let arg = if e_text.contains(' ') && !(e_text.starts_with('(') && e_text.ends_with(')')) {
            format!("({e_text})")
        } else {
            e_text
        };
        let Some((chain, text)) = self.conversion(&shown, &b, &arg) else {
            return plain;
        };
        let Some(and_then) = self
            .home_exposes(block, "andThen")
            .and_then(|mid| self.db.module_exports(mid).value("andThen"))
        else {
            return plain;
        };
        let (e_ty, callee, expected) = (pb.e_ty, pb.callee_ty, pb.expected);
        let fits = self.fits_on(snap, |s| {
            let Some((lam, rest)) = s.fun_parts(callee) else {
                return false;
            };
            let Some((_, result)) = s.fun_parts(rest) else {
                return false;
            };
            if let Some(x) = expected {
                if s.uf.unify(result, x).is_err() {
                    return false;
                }
            }
            let Some(conv) = s.apply_chain(e_ty, &chain) else {
                return false;
            };
            let f = s.infer_res(Res::Def(and_then));
            let rest = s.fun_var(conv, result);
            let want = s.fun_var(lam, rest);
            s.uf.unify(f, want).is_ok()
        });
        if fits {
            format!(
                "{plain} To fix it, convert the line: `{} <- {text}`.",
                pb.pat_text
            )
        } else {
            plain
        }
    }

    /// The source text at `span` in `module`, when it is short and on one
    /// line (for a message).
    pub(crate) fn span_text(&self, module: Option<ModuleId>, span: Option<Span>) -> Option<String> {
        let (module, span) = (module?, span?);
        let src = self.db.module_parse(module).syntax().text().to_string();
        let t = src
            .get(span.range.0 as usize..span.range.1 as usize)?
            .trim();
        (!t.is_empty() && t.len() <= 40 && !t.contains('\n')).then(|| t.to_string())
    }

    fn bind_error(&mut self, pb: &PendingBind, why: NoAndThen) {
        // A name or type error inside `e` is already reported, and it is why
        // no `andThen` fits: no second report for the same line.
        if pb.e_errored {
            return;
        }
        self.errors.push(crate::infer::TypeError {
            message: why.message(
                &pb.pat_text,
                &self
                    .span_text(pb.module, pb.e_span)
                    .map(|t| format!("`{t}`"))
                    .unwrap_or_else(|| "The expression after `<-`".to_string()),
            ),
            span: pb.span,
            code: "E2016",
        });
    }

    /// Try to choose a new bind line's `andThen` at once; when its head is not
    /// known yet, keep it pending.
    pub(crate) fn start_bind(&mut self, pb: PendingBind) {
        if let Attempt::Unknown = self.attempt_bind(&pb) {
            self.pending_binds.push(pb);
        }
    }

    /// Solve pending binds until none resolves (one choice can fix another).
    /// At the end of a def (`fin`), each one still unresolved is `[E2015]`.
    pub(crate) fn solve_pending_binds(&mut self, fin: bool) {
        loop {
            let pending = std::mem::take(&mut self.pending_binds);
            if pending.is_empty() {
                break;
            }
            let n = pending.len();
            let mut kept = Vec::new();
            for pb in pending {
                if let Attempt::Unknown = self.attempt_bind(&pb) {
                    kept.push(pb);
                }
            }
            let progressed = kept.len() < n;
            self.pending_binds = kept;
            if !progressed {
                break;
            }
        }
        if fin {
            for pb in std::mem::take(&mut self.pending_binds) {
                if pb.e_errored {
                    continue;
                }
                let t = self.read_back(pb.e_ty).render_pretty();
                self.errors.push(crate::infer::TypeError {
                    message: format!(
                        "The bind line `{} <- …` chooses `andThen` from the type of the \
                         expression after `<-`, and that type is still `{t}`. Annotate it, \
                         for example `fetch : String -> Task Error User`. \
                         See docs/errors/E2015-bind-needs-a-type.md",
                        pb.pat_text
                    ),
                    span: pb.span,
                    code: "E2015",
                });
            }
        }
    }
}

/// The expression a bind line's continuation `lam` ends in, when no later
/// bind line follows in its block: the `in` expression (through the `let`
/// segments the resolver wraps it in, and a `let` written in the body).
pub(crate) fn final_body(body: &Body, lam: ExprId) -> Option<ExprId> {
    let Expr::Lambda { body: mut b, .. } = &body.exprs[lam] else {
        return None;
    };
    loop {
        match &body.exprs[b] {
            Expr::Let { body: inner, .. } => b = *inner,
            Expr::Call(f, args)
                if args.len() == 2 && matches!(body.exprs[*f], Expr::Var(Res::BindAndThen)) =>
            {
                return None
            }
            _ => return Some(b),
        }
    }
}

/// The result of a function type after all its arguments (`t` itself when it
/// is not a function).
pub(crate) fn final_result(t: &Ty) -> &Ty {
    match t {
        Ty::Fun(_, r) => final_result(r),
        _ => t,
    }
}

/// Is `t` an application of a type constructor other than `head`? A type
/// variable or the same head is not.
pub(crate) fn other_head(t: &Ty, head: &str) -> bool {
    matches!(t, Ty::App(n, _) if !nominal::same(n.as_str(), head))
}

/// The note on a type clash in a block of bind lines: every line and the
/// `in` expression must have the head type of the first. It names no fix
/// (no fix was checked here).
pub(crate) fn mixed_note(head: &str) -> String {
    format!(
        "\n\nNote: this bind line chains with the `andThen` of `{head}`, so the rest of the \
         block must be a `{head}` too. One block of bind lines chains one type."
    )
}

/// Does `t` have the shape `(a -> T p b) -> T p a -> T p b`, with `T` the
/// head `name` of `arity` arguments, `p` the same other arguments in all three
/// places, and `a`, `b` two distinct variables that `p` does not mention?
fn fits_template(t: &Ty, name: &str, arity: usize) -> bool {
    let Ty::Fun(f, rest) = t else { return false };
    let Ty::Fun(a1, tb1) = f.as_ref() else {
        return false;
    };
    let Ty::Fun(ta, tb2) = rest.as_ref() else {
        return false;
    };
    let split = |x: &Ty| -> Option<(Vec<Ty>, Ty)> {
        match x {
            Ty::App(n, args) if args.len() == arity && nominal::same(n.as_str(), name) => {
                let (last, prefix) = args.split_last()?;
                Some((prefix.to_vec(), last.clone()))
            }
            _ => None,
        }
    };
    let (Some((p1, b1)), Some((p2, a2)), Some((p3, b2))) = (split(tb1), split(ta), split(tb2))
    else {
        return false;
    };
    let (Ty::Var(av), Ty::Var(bv)) = (a1.as_ref(), &b1) else {
        return false;
    };
    let mentions = |p: &[Ty], v: &base::Name| p.iter().any(|x| x.free_vars().contains(v));
    av != bv
        && a2 == Ty::Var(av.clone())
        && b2 == Ty::Var(bv.clone())
        && p1 == p2
        && p2 == p3
        && !mentions(&p1, av)
        && !mentions(&p1, bv)
}

/// The candidate `andThen`s a def with a bind line may choose (§4,
/// dependency order): the kernel three and the exposed `andThen` of every
/// module `module` imports, plus `module`'s own. Conservative: a superset of
/// what any bind line in the module can choose.
pub fn candidate_and_thens(db: &dyn SkyDb, module: ModuleId) -> Vec<DefId> {
    let mut out = Vec::new();
    let mut push = |m: ModuleId| {
        if let Some(d) = db.module_exports(m).value("andThen") {
            if !out.contains(&d) {
                out.push(d);
            }
        }
    };
    for (_, home) in KERNEL_BIND_HOMES.iter().chain(hir::KERNEL_IMPLICIT_HOMES) {
        if let Some(m) = db.module_by_name(home) {
            push(m);
        }
    }
    for src in db.resolve(module).qualifiers.values() {
        if let hir::ImportSource::Dep(m) = src {
            push(*m);
        }
    }
    push(module);
    // An unexposed own `andThen` is still ordered first: the checker names it
    // in `[E2016]`.
    if let Some(td) = db
        .resolve(module)
        .top_defs
        .iter()
        .find(|td| td.name.as_str() == "andThen")
    {
        if !out.contains(&td.def) {
            out.push(td.def);
        }
    }
    out
}

/// One def's body as every pass after type inference reads it
/// (docs/design/let-bind-lines.md §5): the shared body of the resolved module
/// when the def has no bind line, else a copy with each bind line's
/// placeholder replaced by the `andThen` the check path chose. ExprIds are
/// those of the shared body, so `BodyTypes` keys stay valid.
///
/// Obtain it ONLY through [`crate::TyDb::elaborated_body_of`] (or
/// [`elaborated_bodies`]); a raw `resolve(m).bodies` read after inference is
/// refused by the source-scan test `ty/tests/elaborated_body_scan.rs`. A bind
/// line the checker refused has `Res::Error` in place of its `andThen` (the
/// checker reported it), so no `Res::BindAndThen` is left.
#[derive(Clone)]
pub struct ElaboratedBody {
    owner: Arc<hir::ResolveResult>,
    def: DefId,
    elaborated: Option<Arc<Body>>,
}

impl std::ops::Deref for ElaboratedBody {
    type Target = Body;
    fn deref(&self) -> &Body {
        match &self.elaborated {
            Some(b) => b,
            None => &self.owner.bodies[&self.def],
        }
    }
}

impl std::fmt::Debug for ElaboratedBody {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("ElaboratedBody")
            .field("def", &self.def)
            .field("elaborated", &self.elaborated.is_some())
            .finish()
    }
}

impl ElaboratedBody {
    /// `owner`'s body of `def`, with `elaborated` in its place when the def
    /// has a bind line. `None` when `owner` has no body for `def`.
    pub fn new(
        owner: Arc<hir::ResolveResult>,
        def: DefId,
        elaborated: Option<Arc<Body>>,
    ) -> Option<Self> {
        owner.bodies.get(&def)?;
        Some(Self {
            owner,
            def,
            elaborated,
        })
    }

    /// The def whose body this is.
    pub fn def(&self) -> DefId {
        self.def
    }

    /// The resolved module that owns the shared body.
    pub fn owner(&self) -> &Arc<hir::ResolveResult> {
        &self.owner
    }

    /// The elaborated copy, when the def has a bind line.
    pub fn elaborated(&self) -> Option<&Arc<Body>> {
        self.elaborated.as_ref()
    }

    /// `(owner, def, elaborated copy)`.
    pub fn into_parts(self) -> (Arc<hir::ResolveResult>, DefId, Option<Arc<Body>>) {
        (self.owner, self.def, self.elaborated)
    }
}

/// `raw` with the choices `elab()` gives in place of its placeholders, shared
/// when `raw` has no bind line. `elab` runs only for a def with a bind line.
pub fn elaborate_with(
    raw: &Body,
    elab: impl FnOnce() -> Arc<BindElaboration>,
) -> Option<Arc<Body>> {
    if !body_has_bind(raw) {
        return None;
    }
    elaborate_body(raw, &elab()).map(Arc::new)
}

/// Every body of `module` as the passes after inference read it, in the
/// resolved module's body order.
pub fn elaborated_bodies(db: &dyn crate::TyDb, module: ModuleId) -> Vec<ElaboratedBody> {
    let resolved = db.resolve(module);
    resolved
        .bodies
        .keys()
        .filter_map(|d| db.elaborated_body_of(module, *d))
        .collect()
}

/// The internal error for a bind-line placeholder that reaches a pass after
/// type inference. Such a pass reads [`ElaboratedBody`], which has none (a
/// refused bind line has `Res::Error`). Reaching one is a compiler bug: fail
/// closed, never skip it.
#[track_caller]
pub fn unchosen_bind(pass: &str) -> ! {
    panic!(
        "internal compiler error: a bind line placeholder with no chosen `andThen` reached \
         {pass} (docs/design/let-bind-lines.md §5). Please report it."
    )
}

/// The next bind line of the block whose bind line has continuation `lam`
/// (`\p -> rest`): `rest` itself, or the body of the `let` segment `rest`
/// is, when that is a bind line's `Call`.
pub(crate) fn next_bind_line(body: &Body, lam: ExprId) -> Option<ExprId> {
    let Expr::Lambda { body: mut cur, .. } = &body.exprs[lam] else {
        return None;
    };
    while let Expr::Let { body: b, .. } = &body.exprs[cur] {
        cur = *b;
    }
    match &body.exprs[cur] {
        Expr::Call(f, args)
            if args.len() == 2 && matches!(body.exprs[*f], Expr::Var(Res::BindAndThen)) =>
        {
            Some(cur)
        }
        _ => None,
    }
}

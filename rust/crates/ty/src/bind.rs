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

/// A bind line whose continuation is being inferred (`Infer::bind_stack`).
pub(crate) struct BindFrame {
    pub(crate) lam: ExprId,
    pub(crate) tlam: TyVarId,
    pub(crate) param: Option<TyVarId>,
    /// The line continues the block of the frame below it.
    pub(crate) continues: bool,
    /// The line's `andThen` is chosen already (the head of `e` was known)
    /// and its type fits: the continuation's parameter takes the type that
    /// `andThen` gives it BEFORE the continuation is inferred, so a misuse
    /// of the bound name is blamed at the use, as in a plain `let`. The
    /// span is the `<-`, where a pattern that refuses the type is blamed.
    pub(crate) seed: Option<Option<Span>>,
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
    /// `e` calls an unannotated def whose own inference reported an error
    /// (an `[E2015]` or any other): a head left unknown by it is no second
    /// `[E2015]` here.
    pub e_calls_failed: bool,
    /// The span of the `<-` token.
    pub span: Option<Span>,
    /// The module of the def the bind line is in.
    pub module: Option<ModuleId>,
    /// `p <- …` as written, for the message (a simple pattern only).
    pub pat_text: String,
    /// The expression after `<-`.
    pub e_expr: ExprId,
    /// The span of the expression after `<-`, to quote it in a message.
    pub e_span: Option<Span>,
}

/// The first bind line of each block in tail position of `root`: the whole
/// of `root`, or a tail of it through the `let` segments (the resolver's and
/// the written ones), the branches of an `if` and the arms of a `case`, and
/// the continuation of each bind line (its later lines and its `in`
/// expression are the rest of the block, so a block nested in them is in
/// tail position too). Each such block has the type of `root`.
pub(crate) fn root_bind_calls(body: &Body, root: ExprId) -> Vec<ExprId> {
    let mut out = Vec::new();
    let mut todo = vec![root];
    while let Some(cur) = todo.pop() {
        match &body.exprs[cur] {
            Expr::Let { body: b, .. } => todo.push(*b),
            Expr::If { arms, els } => {
                todo.push(*els);
                todo.extend(arms.iter().map(|(_, t)| *t));
            }
            Expr::Case { branches, .. } => todo.extend(branches.iter().map(|b| b.body)),
            Expr::Call(f, args)
                if args.len() == 2 && matches!(body.exprs[*f], Expr::Var(Res::BindAndThen)) =>
            {
                out.push(cur);
                if let Expr::Lambda { body: lb, .. } = &body.exprs[args[0]] {
                    todo.push(*lb);
                }
            }
            _ => {}
        }
    }
    out
}

/// A name bound with `=` to a value whose type `M … x` has an `andThen`,
/// at one use ([`Infer::eq_bound_value`]).
pub(crate) struct EqBound {
    name: Name,
    /// The local `name` binds.
    local: hir::LocalId,
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
        /// A note names the conversion of a list of effects.
        converts: bool,
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
    converts: bool,
) -> String {
    // A list of effects the note converts: its fix is the conversion, never `case`.
    let fix = if converts {
        String::new()
    } else if module.starts_with("Sky.") || module.starts_with("Std.") {
        "; use `case`".to_string()
    } else {
        format!("; use `case`, or add one to {module}")
    };
    let other = match found {
        Some(f) => format!(" (its `andThen : {}` has another shape)", f.render_pretty()),
        None => String::new(),
    };
    format!(
        "`{ty}` (module {module}) has no `{}`{other}{fix}. The bind line `{pat} <- …` \
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
            NoAndThen::NoHome(t) => {
                // A type with no module of its own to give an `andThen`
                // (`Cmd`, `Sub` and other runtime types). `case` cannot open
                // these, so the advice names what does combine them.
                let tip = match t.rsplit('.').next().unwrap_or(t) {
                    "Cmd" => {
                        " To run effects in order, chain their Tasks in one bind-line \
                         block and send the result with `Cmd.perform task ToMsg`. To send \
                         several commands, use `Cmd.batch`."
                    }
                    "Sub" => " To listen to several subscriptions, use `Sub.batch`.",
                    _ => "",
                };
                format!(
                    "`{t}` has no `andThen`, so the bind line `{pat} <- …` cannot chain \
                     it.{tip} See docs/errors/E2016-no-andthen.md"
                )
            }
            NoAndThen::Missing {
                ty,
                module,
                arity,
                unexposed: true,
                ..
            } => format!(
                "`{ty}` (module {module}) declares `andThen` but does not expose it, so the \
                 bind line `{pat} <- …` has no `{}` to chain with. Use `case`, or expose it \
                 from {module}. See docs/errors/E2016-no-andthen.md",
                template_text(ty, *arity)
            ),
            NoAndThen::Missing {
                ty,
                module,
                arity,
                converts,
                ..
            } => no_fitting_and_then(ty, module, *arity, pat, None, *converts),
            // The exposed `andThen` does not fit: calling it by name would
            // not fit either, so the fix is `case` or a fitting one.
            NoAndThen::Shape {
                ty,
                module,
                arity,
                found,
            } => no_fitting_and_then(ty, module, *arity, pat, Some(found), false),
            NoAndThen::SelfReference { ty, module } => format!(
                "The bind line `{pat} <- …` would chain with `{module}.andThen` for `{ty}`, \
                 and it is a bind line in `andThen` itself (or in a definition that \
                 `andThen` depends on). Write this definition with `case`. \
                 See docs/errors/E2016-no-andthen.md"
            ),
        }
    }
}

/// "a" or "an" before a type name, by the sound of its first letter
/// (`an Int`, `a Maybe`, `a Unit`).
pub(crate) fn article(name: &str) -> &'static str {
    let mut cs = name.chars().map(|c| c.to_ascii_lowercase());
    match (cs.next(), cs.next()) {
        // `Url`, `Unit`, `User`, `Uuid` start with the sound "you".
        (Some('u'), Some('n' | 'r' | 's' | 'u' | 't')) => "a",
        (Some('a' | 'e' | 'i' | 'o' | 'u'), _) => "an",
        _ => "a",
    }
}

/// A copy of one def's resolved body with a note's fix applied, to re-check
/// the whole def with it ([`Infer::fixed_def_checks`]). Fresh locals no
/// pattern binds stand for a value the fix leaves to the user (`err` in
/// `Result.fromMaybe err`): a typed hole, which takes the type its use
/// needs. A seeded local is a value of a known scheme (`Just`, `Ok`).
pub(crate) struct FixedBody {
    body: Body,
    seeds: Vec<(hir::LocalId, crate::Scheme)>,
    next_local: u32,
}

impl FixedBody {
    fn new(body: &Body) -> Self {
        FixedBody {
            body: body.clone(),
            seeds: Vec::new(),
            next_local: u32::MAX,
        }
    }

    fn fresh_local(&mut self) -> hir::LocalId {
        let l = hir::LocalId(self.next_local);
        self.next_local -= 1;
        l
    }

    /// A typed hole: a value of the type its use needs.
    fn hole(&mut self) -> ExprId {
        let l = self.fresh_local();
        self.body.expr(Expr::Var(Res::Local(l)))
    }

    /// A value of type `scheme` (a constructor such as `Just`).
    fn seeded(&mut self, scheme: crate::Scheme) -> ExprId {
        let l = self.fresh_local();
        self.seeds.push((l, scheme));
        self.body.expr(Expr::Var(Res::Local(l)))
    }

    /// Replace the expression at `at` with `f args… (old)`.
    fn wrap(&mut self, at: ExprId, f: ExprId, mut args: Vec<ExprId>) {
        let old = self.body.exprs[at].clone();
        let moved = self.body.expr(old);
        if let Some(sp) = self.body.expr_span(at) {
            self.body.expr_spans.insert(moved, sp);
        }
        args.push(moved);
        self.body.exprs[at] = Expr::Call(f, args);
    }

    /// Turn the `let` line `local = rhs` into the bind line `local <- rhs`,
    /// the first line of its segment's continuation. `None` when the line
    /// is not found.
    fn bind_line(&mut self, local: hir::LocalId, rhs: ExprId) -> Option<()> {
        let (at, defs, rest) = self.body.exprs.iter().find_map(|(id, e)| match e {
            Expr::Let { defs, body } => defs
                .iter()
                .any(|d| {
                    d.body == rhs
                        && d.params.is_empty()
                        && d.pat.is_none()
                        && matches!(d.binders.as_slice(), [(_, l)] if *l == local)
                })
                .then(|| (id, defs.clone(), *body)),
            _ => None,
        })?;
        let others: Vec<hir::LocalDef> = defs.into_iter().filter(|d| d.body != rhs).collect();
        let pat = self.body.pat(Pattern::Var(local));
        let lam = self.body.expr(Expr::Lambda {
            params: vec![pat],
            body: rest,
        });
        let head = self.body.expr(Expr::Var(Res::BindAndThen));
        let call = Expr::Call(head, vec![lam, rhs]);
        self.body.exprs[at] = if others.is_empty() {
            call
        } else {
            let call = self.body.expr(call);
            Expr::Let {
                defs: others,
                body: call,
            }
        };
        Some(())
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
                        ty: self.show(&raw),
                        args,
                    },
                );
                return Attempt::Done;
            }
            other => {
                let shown = self.show(other);
                self.bind_error(pb, NoAndThen::NotNamed(shown));
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
            let note = self.list_sequence_note(pb, &raw);
            let before = self.errors.len();
            self.bind_error(
                pb,
                NoAndThen::Missing {
                    ty: shown,
                    module: home,
                    arity,
                    unexposed,
                    converts: note.is_some(),
                },
            );
            self.append_note(before, note);
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
        // The other type the block needs: its result's, else the one the
        // continuation ends in. A type of another head, a record, a tuple or
        // `()` (none of which chains this line's `andThen`).
        let block = match &callee {
            Ty::Fun(lam, rest) => {
                let result = match rest.as_ref() {
                    Ty::Fun(_, r) => r.as_ref(),
                    other => other,
                };
                [result, final_result(lam)]
                    .into_iter()
                    .find(|t| {
                        other_head(t, &name)
                            || matches!(t, Ty::Record(..) | Ty::Tuple(_) | Ty::Unit)
                    })
                    .cloned()
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

    /// `e : List x` after `<-`: a `List` has no `andThen`, but the home
    /// module of the block's type `M` may run the list as one `M`
    /// (`Task.sequence`, `Result.combine`, `Maybe.combine`). `M` is the head
    /// of the block's type, else the head of the list's element. The note
    /// names the conversion only when the def with the line so converted
    /// type-checks (`fixed_def_checks`): that one test decides it, whatever
    /// variables the element's type still holds (`Task a Int` from
    /// `Task.succeed`, whose error type the block fixes).
    fn list_sequence_note(&mut self, pb: &PendingBind, raw: &Ty) -> Option<String> {
        if pb.e_errored {
            return None;
        }
        let Ty::App(list, args) = raw else {
            return None;
        };
        if nominal::strip(list.as_str()) != "List" || args.len() != 1 {
            return None;
        }
        let head = |t: &Ty| match t {
            Ty::App(n, a) if !a.is_empty() && nominal::strip(n.as_str()) != "List" => {
                Some(n.as_str().to_string())
            }
            _ => None,
        };
        let callee = self.read_back(pb.callee_ty);
        let block = match &callee {
            Ty::Fun(_, rest) => match rest.as_ref() {
                Ty::Fun(_, r) => head(r),
                _ => None,
            },
            _ => None,
        };
        let m = block.or_else(|| head(&args[0]))?;
        let home = self.head_home(&m)?;
        let mid = self.db.module_by_name(&home)?;
        let fun = ["sequence", "combine"].into_iter().find(|f| {
            self.db.module_exports(mid).value(f).is_some()
                && self.fixed_def_checks(|fx| self.apply_chain_at(fx, pb.e_expr, &[(&home, f, 0)]))
        })?;
        let shown = nominal::strip(&m).to_string();
        let q = self.qualifier_of(mid, &shown);
        let write = match self.span_text_within(pb.module, pb.e_span, 100) {
            Some(t) => {
                let one_group = |open: char, close: char| {
                    // `t` is ONE bracket group: its first bracket closes at the
                    // very end (so `[ a ] ++ [ b ]` still gets parentheses).
                    t.starts_with(open) && t.ends_with(close) && {
                        let mut depth = 0i32;
                        let last = t.len() - close.len_utf8();
                        t.char_indices().all(|(i, c)| {
                            if c == open {
                                depth += 1;
                            } else if c == close {
                                depth -= 1;
                            }
                            depth > 0 || i == last
                        })
                    }
                };
                let arg = if t.contains(' ') && !one_group('(', ')') && !one_group('[', ']') {
                    format!("({t})")
                } else {
                    t
                };
                format!("write `{} <- {q}.{fun} {arg}`", pb.pat_text)
            }
            None => format!(
                "put the expression after `<-` in `{q}.{fun}`: `{} <- {q}.{fun} (…)`",
                pb.pat_text
            ),
        };
        Some(format!(
            "\n\nNote: the expression after `<-` is a list of `{shown}` values. To run them \
             as one `{shown}`, {write}."
        ))
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
        self.import_qualifier(mid)
            .unwrap_or_else(|| fallback.to_string())
    }

    /// The shortest import alias under which the module of the def being
    /// checked imports `mid`, if it imports it at all.
    fn import_qualifier(&self, mid: ModuleId) -> Option<String> {
        let own = self
            .self_def
            .and_then(|d| self.db.def_loc(d))
            .map(|l| l.module);
        let own = own?;
        let mut names: Vec<String> = self
            .db
            .resolve(own)
            .qualifiers
            .iter()
            .filter(|(_, src)| matches!(src, hir::ImportSource::Dep(m) if *m == mid))
            .map(|(q, _)| q.to_string())
            .collect();
        names.sort_by_key(|q| (q.len(), q.clone()));
        names.into_iter().next()
    }

    /// The resolved body of the def under inference, the one the checker
    /// reports on.
    fn own_body(&self) -> Option<(Arc<hir::ResolveResult>, DefId)> {
        let def = self.self_def?;
        let module = self.db.def_loc(def)?.module;
        let resolved = self.db.resolve(module);
        resolved
            .bodies
            .contains_key(&def)
            .then_some((resolved, def))
    }

    /// `f` on the resolved body of the def under inference.
    fn on_own_body<R>(&self, f: impl FnOnce(&Body) -> R) -> Option<R> {
        let (resolved, def) = self.own_body()?;
        resolved.bodies.get(&def).map(f)
    }

    /// THE test for a note that names a code fix: apply the fix with `edit`
    /// to a copy of the def's body, and infer the WHOLE def again in a run of
    /// its own (against its annotation when it has one), as the checker
    /// does. The fix is named only when that run has no error at all, so a
    /// fix that clears this clash but breaks another line of the same def is
    /// never shown. Nothing of the run is kept. One run per candidate note,
    /// only after an error; a run started here tries no fix of its own.
    pub(crate) fn fixed_def_checks(&self, edit: impl FnOnce(&mut FixedBody) -> Option<()>) -> bool {
        if self.fix_trial {
            return false;
        }
        let Some((resolved, def)) = self.own_body() else {
            return false;
        };
        let Some(orig) = resolved.bodies.get(&def) else {
            return false;
        };
        let mut fixed = FixedBody::new(orig);
        if edit(&mut fixed).is_none() {
            return false;
        }
        let mut run = Infer::new(self.world, self.db).with_self_def(Some(def));
        run.fix_trial = true;
        for (l, scheme) in &fixed.seeds {
            let v = run.instantiate(scheme);
            run.locals.insert(*l, v);
        }
        match self.world.value_sigs.get(&def) {
            Some(scheme) => {
                let scheme = scheme.clone();
                run.infer_def_against(&fixed.body, &scheme);
            }
            None => {
                run.infer_def(&fixed.body);
            }
        }
        run.errors.is_empty()
    }

    /// The `Var` of the exposed value `name` of `module`, in `fixed`.
    fn exposed_var(&self, fixed: &mut FixedBody, module: &str, name: &str) -> Option<ExprId> {
        let mid = self.db.module_by_name(module)?;
        let def = self.db.module_exports(mid).value(name)?;
        Some(fixed.body.expr(Expr::Var(Res::Def(def))))
    }

    /// Apply the conversion `chain` to the expression at `at` in `fixed`,
    /// a typed hole for each leading argument it leaves to the user.
    fn apply_chain_at(
        &self,
        fixed: &mut FixedBody,
        at: ExprId,
        chain: &[(&str, &str, usize)],
    ) -> Option<()> {
        for (module, name, holes) in chain {
            let f = self.exposed_var(fixed, module, name)?;
            let args = (0..*holes).map(|_| fixed.hole()).collect();
            fixed.wrap(at, f, args);
        }
        Some(())
    }

    /// The words after a conversion that leaves `err` to the user: what
    /// `err` is, with an example when the block's error type is `Error`.
    fn err_placeholder(&self, block_ty: &Ty) -> String {
        let err = match block_ty {
            Ty::App(_, args) if args.len() == 2 => Some(&args[0]),
            _ => None,
        };
        match err {
            Some(Ty::App(n, a)) if a.is_empty() && nominal::strip(n.as_str()) != "Error" => {
                let t = nominal::strip(n.as_str());
                format!(", where `err` is the `{t}` to report")
            }
            _ => {
                // The example resolves only under an import of
                // `Sky.Core.Error`: name it when the module has none.
                let (q, import) = match self
                    .db
                    .module_by_name("Sky.Core.Error")
                    .and_then(|mid| self.import_qualifier(mid))
                {
                    Some(q) => (q, String::new()),
                    None => (
                        "Error".to_string(),
                        ", with `import Sky.Core.Error as Error`".to_string(),
                    ),
                };
                format!(
                    ", where `err` is the `Error` to report (e.g. `{q}.invalidInput \"…\"`{import})"
                )
            }
        }
    }

    /// The plain note for a value of type `ty` (head `head`) used where its
    /// result is needed and no fix checks: the types, and the idiom that
    /// takes the result out, in words. `who` names the value (`` `a` `` or
    /// the function that gives it).
    fn result_idiom(&self, who: &str, head: &str, ty: &Ty) -> String {
        let q = |m: &str, f: &str| -> String {
            self.db
                .module_by_name(m)
                .map(|mid| self.qualifier_of(mid, f))
                .unwrap_or_else(|| f.to_string())
        };
        let shown = self.show(ty);
        let a = article(&shown);
        match nominal::strip(head) {
            "Maybe" => format!(
                "{who} gives {a} `{shown}`: handle `Nothing` with `case` or `{}.withDefault`.",
                q("Sky.Core.Maybe", "Maybe")
            ),
            "Result" => format!(
                "{who} gives {a} `{shown}`: handle `Err` with `case` or `{}.withDefault`.",
                q("Sky.Core.Result", "Result")
            ),
            "Task" => format!(
                "{who} gives {a} `{shown}`: its result is available only inside a `Task` \
                 chain (`{}.andThen`, or a block of `Task` bind lines).",
                q("Sky.Core.Task", "Task")
            ),
            _ => format!("{who} gives {a} `{shown}`, not its result."),
        }
    }

    /// How a note names the value `rhs` gives: the called function
    /// (`` `String.toInt` ``), else the value's own text.
    fn giver_text(&self, body: &Body, rhs: ExprId) -> String {
        let module = self
            .self_def
            .and_then(|d| self.db.def_loc(d))
            .map(|l| l.module);
        let at = match &body.exprs[rhs] {
            Expr::Call(f, _) => *f,
            _ => rhs,
        };
        self.span_text(module, body.expr_span(at))
            .map(|t| format!("`{t}`"))
            .unwrap_or_else(|| "The value".to_string())
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
        let failed = self.failed_def_seen;
        let span = self.cur_span;
        let out = f(self);
        self.uf = real;
        self.errors.truncate(errors);
        self.obligations.truncate(obligations);
        self.page_ties.truncate(page_ties);
        self.bound_seen = bound_seen;
        self.inferred_bounds.truncate(inferred_bounds);
        self.unresolved_seen = unresolved;
        self.failed_def_seen = failed;
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

    /// The head and the last argument of the application `v`, when it is one.
    fn app_parts(&mut self, v: TyVarId) -> Option<(Name, TyVarId)> {
        match self.uf.content(v) {
            Content::Structure(FlatTy::App(n, args)) => args.last().map(|l| (n, *l)),
            _ => None,
        }
    }

    /// The conversion of a value of head `from` into head `to`, as the
    /// functions to apply and the text that shows it around `arg`.
    fn conversion(
        &self,
        from: &str,
        to: &str,
        arg: &str,
    ) -> Option<(Vec<(&'static str, &'static str, usize)>, String, bool)> {
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
                true,
            ),
            ("Result", "Task") => (
                vec![("Sky.Core.Task", "fromResult", 0)],
                format!("{}.fromResult {arg}", task()),
                false,
            ),
            ("Maybe", "Result") => (
                vec![("Sky.Core.Result", "fromMaybe", 1)],
                format!("{}.fromMaybe err {arg}", result()),
                true,
            ),
            ("Result", "Maybe") => (
                vec![("Sky.Core.Result", "toMaybe", 0)],
                format!("{}.toMaybe {arg}", result()),
                false,
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
    /// type is not known, `Some(None)` when the body's own type fits the
    /// block's type: the clash is then inside the body, and the error is
    /// given with no note, as for a hand-written `andThen`.
    /// At the last bind line of a block, before its continuation is
    /// unified with its `andThen`: each earlier line's name takes the type
    /// its `andThen` gives it (the unification the earlier line makes later
    /// in any case). A name whose uses refuse that type is left alone, so
    /// its earlier line still reports the clash. With the names typed, an
    /// `in` body of the wrong type is the clash of the last line, whatever
    /// the number of lines.
    pub(crate) fn type_block_names(&mut self) {
        let mut pairs = Vec::new();
        let mut i = self.bind_stack.len();
        while i > 1 && self.bind_stack[i - 1].continues {
            i -= 1;
            let f = &self.bind_stack[i - 1];
            if let Some(p) = f.param {
                pairs.push((p, f.tlam));
            }
        }
        for (p, tlam) in pairs {
            let Some((pw, _)) = self.fun_parts(tlam) else {
                continue;
            };
            let cur = self.uf.clone();
            if self.fits_on(&cur, |s| {
                s.unify(p, pw);
                true
            }) {
                self.unify(p, pw);
            }
        }
    }

    pub(crate) fn body_note(
        &mut self,
        snap: &UnionFind,
        line: &PendingBind,
        block_head: &str,
        lam: TyVarId,
        want: TyVarId,
        expected: Option<TyVarId>,
        fb: ExprId,
        body_text: Option<String>,
    ) -> Option<Option<String>> {
        let Some((prepared, body_ty, block_ty, bv, rv, body_head)) =
            self.on_snapshot(snap, |s| {
                let (pl, bv) = s.fun_parts(lam)?;
                let (pw, rv) = s.fun_parts(want)?;
                if let Some(x) = expected {
                    let _ = s.uf.unify(rv, x);
                }
                // The body's type fits the block's (the unifier expands
                // aliases) on a copy of the union-find.
                let fits = |s: &mut Self| {
                    let cur = s.uf.clone();
                    s.fits_on(&cur, |t| {
                        t.unify(bv, rv);
                        true
                    })
                };
                // The last line's name refuses the type of its line (`a + 1`
                // with `a <- Task.succeed "x"`, `a * b` with `a : Float`
                // and `b <- Just 2`): the clash is that line's, so the
                // error needs no note. The earlier lines' names have their
                // types already (`Infer::type_block_names`).
                let before = s.errors.len();
                s.unify(pl, pw);
                if s.errors.len() > before {
                    return Some(None);
                }
                let body_ty = s.read_back(bv);
                if matches!(body_ty, Ty::Var(_) | Ty::Error) {
                    return None;
                }
                // The body's type fits the block's: the clash is inside the
                // body (`user.agee`, `a + 1` with `a : String`), so the
                // error needs no note, as for a hand-written `andThen`.
                if fits(s) {
                    return Some(None);
                }
                let head = s.app_parts(bv).map(|(n, _)| n.as_str().to_string());
                let block_ty = s.read_back(rv);
                Some(Some((s.uf.clone(), body_ty, block_ty, bv, rv, head)))
            })?
        else {
            return Some(None);
        };
        // The `in` expression has a type its use fixes (the TEA update
        // shape `( model, Cmd msg )`, a view, a `Sub`), in a def with no
        // annotation: the block's line cannot be chained there. Give the
        // note the annotated def gets (`Cmd.perform` for a Task, the
        // `case` / `withDefault` idiom for a `Maybe` or `Result`), never
        // a wrap of the body in the line's type.
        if fixed_by_use(&body_ty) {
            let shown = self.show(&body_ty);
            let who = Some("the `in` expression has type");
            if nominal::strip(block_head) == "Task" {
                if let Some(note) = self.tea_update_note(&body_ty, &shown, who) {
                    return Some(Some(note));
                }
            }
            return Some(Some(self.fixed_type_note(
                line,
                snap,
                nominal::strip(block_head),
                &body_ty,
                &shown,
                who,
            )));
        }
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
            self.show(&body_ty),
            self.show(&block_ty)
        );
        // A conversion from another head, when the converted value fits.
        if let Some(h) = body_head
            .as_deref()
            .filter(|h| !nominal::same(h, block_head))
        {
            if let Some((chain, text, err)) = self.conversion(nominal::strip(h), &b, &arg) {
                if self.fixed_def_checks(|f| self.apply_chain_at(f, fb, &chain)) {
                    let err = if err {
                        self.err_placeholder(&block_ty)
                    } else {
                        String::new()
                    };
                    return Some(Some(format!(
                        "{intro} To fix it, convert it: `{text}`{err}."
                    )));
                }
            }
        }
        // A wrap of a plain value, when the wrapped value fits.
        // Each is a `Res::Def` (`Task.succeed`, the home `succeed`) or a
        // builtin constructor's scheme (`Ok`, `Just`).
        enum Wrap {
            Def(DefId),
            Ctor(crate::Scheme),
        }
        let wrap = match b.as_str() {
            "Task" => self
                .db
                .module_by_name("Sky.Core.Task")
                .and_then(|m| self.db.module_exports(m).value("succeed"))
                .map(|d| ("Task.succeed".to_string(), Wrap::Def(d))),
            "Result" => self
                .world
                .ctors
                .get("Ok")
                .map(|s| ("Ok".to_string(), Wrap::Ctor(s.clone()))),
            "Maybe" => self
                .world
                .ctors
                .get("Just")
                .map(|s| ("Just".to_string(), Wrap::Ctor(s.clone()))),
            _ => self.home_exposes(block_head, "succeed").and_then(|mid| {
                let def = self.db.module_exports(mid).value("succeed")?;
                let fallback = if b == "Decoder" {
                    "Decode".to_string()
                } else {
                    self.head_home(block_head).unwrap_or_default()
                };
                Some((
                    format!("{}.succeed", self.qualifier_of(mid, &fallback)),
                    Wrap::Def(def),
                ))
            }),
        };
        if let Some((wrap, how)) = wrap {
            let fits = self.fixed_def_checks(|f| {
                let callee = match how {
                    Wrap::Def(d) => f.body.expr(Expr::Var(Res::Def(d))),
                    Wrap::Ctor(s) => f.seeded(s),
                };
                f.wrap(fb, callee, Vec::new());
                Some(())
            });
            if fits {
                return Some(Some(format!(
                    "{intro} To fix it, wrap the value in `{wrap}`: `{wrap} {arg}`."
                )));
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
        Some(Some(match needs {
            Some(k) => format!("{intro} It is a function: it needs {k} more argument(s)."),
            None => intro,
        }))
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
            local: *l,
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
        // The bind line is named only when the whole def checks with it: in
        // a block of another type, or a body that is not a block of that
        // type, it moves the clash elsewhere.
        if self.fixed_def_checks(|f| f.bind_line(c.local, c.rhs)) {
            let module = self
                .self_def
                .and_then(|d| self.db.def_loc(d))
                .map(|l| l.module);
            let rhs_text = self
                .span_text(module, body.expr_span(c.rhs))
                .unwrap_or_else(|| "…".to_string());
            let head = nominal::strip(&c.head);
            return Some(format!(
                "\n\nNote: `{}` is {} `{head}`; to use its result, bind it with `{} <- {rhs_text}`.",
                c.name.as_str(),
                article(head),
                c.name.as_str()
            ));
        }
        let ty = self.on_snapshot(snap, |s| s.read_back(c.ty));
        let who = self.giver_text(body, c.rhs);
        Some(format!(
            "\n\nNote: {}",
            self.result_idiom(&who, &c.head, &ty)
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
        block_ty: &Ty,
    ) -> String {
        let block = match block_ty {
            Ty::App(n, _) if self.home_exposes(n.as_str(), "andThen").is_some() => {
                n.as_str().to_string()
            }
            // The block's type has no `andThen`: the line cannot run in it.
            _ => return self.cannot_run_note(pb, snap, name, block_ty),
        };
        let shown = nominal::strip(name).to_string();
        let b = nominal::strip(&block).to_string();
        let (line_name, block_name) = self.two_type_names(name, &block);
        let plain = format!(
            "\n\nNote: this bind line chains with the `andThen` of `{line_name}`, and the block \
             is {} `{block_name}` block. One block of bind lines chains one type.",
            article(&block_name)
        );
        let e_text = self
            .span_text(pb.module, pb.e_span)
            .unwrap_or_else(|| "…".to_string());
        let arg = if e_text.contains(' ') && !(e_text.starts_with('(') && e_text.ends_with(')')) {
            format!("({e_text})")
        } else {
            e_text
        };
        // With no fix that checks: the idiom that takes a `Maybe` or a
        // `Result` apart, in words.
        let idiom: String = {
            let e_ty = self.on_snapshot(snap, |s| s.read_back(pb.e_ty));
            match shown.as_str() {
                "Maybe" | "Result" => {
                    let who = self.on_own_body(|b| self.giver_text(b, pb.e_expr));
                    format!(
                        "{plain} {}",
                        self.result_idiom(&who.unwrap_or_else(|| "It".to_string()), &shown, &e_ty)
                    )
                }
                _ => plain.clone(),
            }
        };
        let Some((chain, text, err)) = self.conversion(&shown, &b, &arg) else {
            return idiom;
        };
        if self.fixed_def_checks(|f| self.apply_chain_at(f, pb.e_expr, &chain)) {
            let err = if err {
                let block_ty = self.on_snapshot(snap, |s| {
                    let (_, rest) = s.fun_parts(pb.callee_ty)?;
                    let (_, result) = s.fun_parts(rest)?;
                    Some(s.read_back(result))
                });
                self.err_placeholder(&block_ty.unwrap_or(Ty::Error))
            } else {
                String::new()
            };
            format!(
                "{plain} To fix it, convert the line: `{} <- {text}`{err}.",
                pb.pat_text
            )
        } else {
            idiom
        }
    }

    /// The names of two type heads in one note. Two heads that read the
    /// same once their modules are dropped (`Decoder` of `Sky.Core.Json.Decode`
    /// and of `Std.Db.Decode`) are shown as the module writes them: its
    /// import alias (`Decode.Decoder`, `DbDecode.Decoder`), else the module's
    /// full name.
    pub(crate) fn two_type_names(&self, a: &str, b: &str) -> (String, String) {
        let (sa, sb) = (nominal::strip(a), nominal::strip(b));
        if sa != sb || nominal::same(a, b) {
            return (sa.to_string(), sb.to_string());
        }
        (self.qualified_head(a), self.qualified_head(b))
    }

    /// The head `name` as the module of the def being checked writes it,
    /// qualified: `Alias.Type`, else `Full.Module.Type`.
    fn qualified_head(&self, name: &str) -> String {
        let bare = nominal::strip(name);
        match self.head_home(name) {
            Some(home) => {
                let q = self
                    .db
                    .module_by_name(&home)
                    .map(|mid| self.qualifier_of(mid, &home))
                    .unwrap_or(home);
                format!("{q}.{bare}")
            }
            None => name.to_string(),
        }
    }

    /// The note on a bind line whose block has a type with no `andThen`
    /// (`String`, `Int`, a `List`, a record): the line's `M` cannot run there.
    /// It names the def and the type it would need to return, as advice: the
    /// change of a signature is not a code fix this note claims to check.
    fn cannot_run_note(
        &mut self,
        pb: &PendingBind,
        snap: &UnionFind,
        name: &str,
        block_ty: &Ty,
    ) -> String {
        let head = nominal::strip(name).to_string();
        let shown_head = if nominal::is_qualified(name) {
            self.qualified_head(name)
        } else {
            head.clone()
        };
        let block_text = self.show(block_ty);
        // A Task line in a TEA `update` (a block of `( model, Cmd msg )`):
        // the Task runs through `Cmd.perform`, never by making `update`
        // return a Task (the app builder takes no such `update`).
        if head == "Task" {
            if let Some(note) = self.tea_update_note(block_ty, &block_text, None) {
                return note;
            }
        }
        // A block of a type its use fixes (`( model, Cmd msg )`, a view, a
        // `Sub`): never advise another return type.
        if fixed_by_use(block_ty) {
            return self.fixed_type_note(pb, snap, &head, block_ty, &block_text, None);
        }
        // The type the block would need: the line's type with the block's
        // type as its result (`Task Error Int` with `String`: `Task Error
        // String`).
        let e_ty = self.on_snapshot(snap, |s| s.read_back(pb.e_ty));
        let needed = match &e_ty {
            Ty::App(_, args) if !args.is_empty() => {
                let mut args = args.clone();
                if let Some(last) = args.last_mut() {
                    *last = block_ty.clone();
                }
                Ty::App(Name::from(shown_head.as_str()), args)
            }
            _ => Ty::App(Name::from(shown_head.as_str()), vec![block_ty.clone()]),
        };
        let needed = self.show(&needed);
        let callers = if head == "Task" {
            "run it"
        } else {
            "handle it"
        };
        // A `Maybe` or a `Result` is often taken apart where it is: the
        // idiom, in words.
        let idiom = match head.as_str() {
            "Maybe" | "Result" => {
                let who = self
                    .on_own_body(|b| self.giver_text(b, pb.e_expr))
                    .unwrap_or_else(|| "It".to_string());
                format!(" {}", self.result_idiom(&who, &head, &e_ty))
            }
            _ => String::new(),
        };
        let a = article(&head);
        let why = format!(
            "so the `{head}` after `<-` cannot run here: a bind line on {a} `{head}` needs a \
             block that is {a} `{head}` too."
        );
        match self.def_returning(block_ty) {
            Some(def) => format!(
                "\n\nNote: `{def}` returns `{block_text}`, {why} Make `{def}` return \
                 `{needed}` (and its callers {callers}), or do this work in a function that \
                 returns {a} {head}.{idiom}"
            ),
            None => format!(
                "\n\nNote: this block has type `{block_text}`, {why} Make the block {} \
                 `{needed}`, or do this work in a function that returns {a} {head}.{idiom}",
                article(&needed)
            ),
        }
    }

    /// The note on a bind line in a block whose type its use fixes (see
    /// [`fixed_by_use`]): the line's type cannot be chained there, and a
    /// `Maybe` or `Result` is taken apart with the line's own names.
    fn fixed_type_note(
        &mut self,
        pb: &PendingBind,
        snap: &UnionFind,
        head: &str,
        block_ty: &Ty,
        block_text: &str,
        who: Option<&str>,
    ) -> String {
        let who = match who.map(str::to_string).or_else(|| {
            self.def_returning(block_ty)
                .map(|d| format!("`{d}` returns"))
        }) {
            Some(w) => w,
            None => "this block has type".to_string(),
        };
        let e_ty = self.on_snapshot(snap, |s| s.read_back(pb.e_ty));
        let e = self
            .span_text_within(pb.module, pb.e_span, 100)
            .filter(|t| !t.contains('\n'));
        let pat = if pb.pat_text.is_empty() {
            "…".to_string()
        } else {
            pb.pat_text.clone()
        };
        let q = |m: &str, f: &str| -> String {
            self.db
                .module_by_name(m)
                .map(|mid| self.qualifier_of(mid, f))
                .unwrap_or_else(|| f.to_string())
        };
        let a = article(head);
        let (arms, home, inner) = match (head, &e_ty) {
            ("Maybe", Ty::App(_, args)) if args.len() == 1 => (
                format!("Just {pat} -> … ; Nothing -> …"),
                q("Sky.Core.Maybe", "Maybe"),
                &args[0],
            ),
            ("Result", Ty::App(_, args)) if args.len() == 2 => (
                format!("Ok {pat} -> … ; Err e -> …"),
                q("Sky.Core.Result", "Result"),
                &args[1],
            ),
            ("Task", _) => {
                let cmd = q("Std.Cmd", "Cmd");
                return format!(
                    "\n\nNote: {who} `{block_text}`, so a Task cannot run in it. Run the Task \
                     from `update` with `{cmd}.perform`, and keep its result in the model."
                );
            }
            _ => {
                return format!(
                    "\n\nNote: {who} `{block_text}`, so {a} `{head}` cannot be chained here."
                );
            }
        };
        let default = match inner {
            Ty::App(n, args) => match (nominal::strip(n.as_str()), args.len()) {
                ("Int", 0) => "0",
                ("Float", 0) => "0.0",
                ("String", 0) => "\"\"",
                ("Bool", 0) => "False",
                ("List", 1) => "[]",
                _ => "…",
            },
            _ => "…",
        };
        let how = match e {
            Some(e) => {
                let arg = if e.contains(' ') {
                    format!("({e})")
                } else {
                    e.clone()
                };
                format!(
                    " Handle it with `case {e} of {arms}`, or `{home}.withDefault {default} {arg}`."
                )
            }
            None => format!(" Handle it with `case` or `{home}.withDefault`."),
        };
        format!("\n\nNote: {who} `{block_text}`, so {a} `{head}` cannot be chained here.{how}")
    }

    /// The name of the def being checked, when its annotated result (after
    /// its parameters) is `ty`: a block of that type is what it returns.
    fn def_returning(&self, ty: &Ty) -> Option<String> {
        let def = self.self_def?;
        let loc = self.db.def_loc(def)?;
        let scheme = self.world.value_sigs.get(&def)?;
        let params = self.on_own_body(|b| b.params.len())?;
        let mut t = &scheme.ty;
        for _ in 0..params {
            match t {
                Ty::Fun(_, r) => t = r,
                _ => return None,
            }
        }
        (t.render_pretty() == ty.render_pretty()).then(|| loc.name.as_str().to_string())
    }

    /// The note on a `Task` bind line in a block of the TEA update shape
    /// `( model, Cmd msg )`: the Task runs through `Cmd.perform`, and its
    /// result comes back as a `Msg`. `None` for any other block type.
    fn tea_update_note(
        &self,
        block_ty: &Ty,
        block_text: &str,
        who: Option<&str>,
    ) -> Option<String> {
        let Ty::Tuple(parts) = block_ty else {
            return None;
        };
        let [_, Ty::App(cmd, cmd_args)] = parts.as_slice() else {
            return None;
        };
        if nominal::strip(cmd.as_str()) != "Cmd" || cmd_args.len() != 1 {
            return None;
        }
        let q = |m: &str, f: &str| -> String {
            self.db
                .module_by_name(m)
                .map(|mid| self.qualifier_of(mid, f))
                .unwrap_or_else(|| f.to_string())
        };
        let cmd_q = q("Std.Cmd", "Cmd");
        // The model: the def's last parameter, when it is a plain name.
        let model = self
            .on_own_body(|b| {
                let p = *b.params.last()?;
                matches!(b.pats[p], Pattern::Var(_))
                    .then(|| pat_text(self.db, self.self_def, b, p))
                    .filter(|t| t != "…")
            })
            .flatten()
            .unwrap_or_else(|| "model".to_string());
        // The `Msg` constructor that takes a `Result`, when the msg type has
        // one; else a name to declare.
        let msg_name = match &cmd_args[0] {
            Ty::App(m, a) if a.is_empty() => Some(m.as_str().to_string()),
            _ => None,
        };
        let ctor = msg_name.as_deref().and_then(|m| {
            let ctors = self
                .world
                .union_ctors
                .get(m)
                .or_else(|| self.world.union_ctors.get(nominal::strip(m)))?;
            ctors.iter().find_map(|c| {
                let s = self.world.ctors.get(c)?;
                match &s.ty {
                    Ty::Fun(arg, res)
                        if matches!(arg.as_ref(), Ty::App(r, _) if nominal::strip(r.as_str()) == "Result")
                            && matches!(res.as_ref(), Ty::App(..)) =>
                    {
                        Some(nominal::strip(c).to_string())
                    }
                    _ => None,
                }
            })
        });
        let msg_shown = msg_name
            .as_deref()
            .map(|m| nominal::strip(m).to_string())
            .unwrap_or_else(|| "Msg".to_string());
        let (ctor, declare) = match ctor {
            Some(c) => (c, String::new()),
            None => (
                "Saved".to_string(),
                format!(
                    " (with a `{msg_shown}` constructor that takes its `Result`, such as \
                     `Saved (Result Error a)`)"
                ),
            ),
        };
        let who = match who.map(str::to_string).or_else(|| {
            self.def_returning(block_ty)
                .map(|d| format!("`{d}` returns"))
        }) {
            Some(w) => w,
            None => "this block has type".to_string(),
        };
        Some(format!(
            "\n\nNote: {who} `{block_text}`, so a Task cannot run in it directly. Put the Task \
             block in its own function and run it with `{cmd_q}.perform`: `( {model}, \
             {cmd_q}.perform (save {model}) {ctor} )`{declare}; its result comes back as a \
             `{msg_shown}`."
        ))
    }

    /// The record aliases the def's annotation writes by name (`Model`),
    /// each with the record type it stands for, as `render_pretty` shows it.
    fn annotation_aliases(&self) -> Vec<(String, Name)> {
        fn walk(raw: &Ty, ty: &Ty, out: &mut Vec<(String, Name)>) {
            match (raw, ty) {
                (Ty::App(name, args), Ty::Record(..)) if args.is_empty() => {
                    out.push((ty.render_pretty(), name.clone()))
                }
                (Ty::App(_, args), Ty::App(_, targs)) if args.len() == targs.len() => {
                    for (a, b) in args.iter().zip(targs) {
                        walk(a, b, out);
                    }
                }
                (Ty::Fun(a, b), Ty::Fun(x, y)) => {
                    walk(a, x, out);
                    walk(b, y, out);
                }
                (Ty::Tuple(xs), Ty::Tuple(ys)) if xs.len() == ys.len() => {
                    for (a, b) in xs.iter().zip(ys) {
                        walk(a, b, out);
                    }
                }
                (Ty::Record(fs, _), Ty::Record(gs, _)) => {
                    for (n, a) in fs {
                        if let Some((_, b)) = gs.iter().find(|(m, _)| m == n) {
                            walk(a, b, out);
                        }
                    }
                }
                _ => {}
            }
        }
        use syntax::ast;
        let mut out = Vec::new();
        let Some(def) = self.self_def else {
            return out;
        };
        let (Some(loc), Some(scheme)) = (self.db.def_loc(def), self.world.value_sigs.get(&def))
        else {
            return out;
        };
        // The annotation as written: alias names unexpanded.
        let tree = self.db.module_parse(loc.module).tree();
        for decl in tree.decls() {
            if let ast::Decl::TypeAnno(a) = &decl {
                if a.name().map(|t| t.text().to_string()).as_deref() == Some(loc.name.as_str()) {
                    if let Some(t) = a.ty() {
                        walk(&crate::sig::ast_type_to_ty(&t), &scheme.ty, &mut out);
                    }
                    break;
                }
            }
        }
        out
    }

    /// `ty` as a note shows it: a record the def's annotation wrote through
    /// an alias is shown by the alias name (`Model`, not its fields).
    pub(crate) fn show(&self, ty: &Ty) -> String {
        let aliases = self.annotation_aliases();
        if aliases.is_empty() {
            return ty.render_pretty();
        }
        fn sub(t: &Ty, al: &[(String, Name)]) -> Ty {
            match t {
                Ty::Record(fs, ext) => {
                    let shown = t.render_pretty();
                    if let Some((_, n)) = al.iter().find(|(r, _)| *r == shown) {
                        return Ty::App(n.clone(), Vec::new());
                    }
                    Ty::Record(
                        fs.iter().map(|(n, x)| (n.clone(), sub(x, al))).collect(),
                        ext.clone(),
                    )
                }
                Ty::Fun(a, b) => Ty::Fun(Box::new(sub(a, al)), Box::new(sub(b, al))),
                Ty::App(n, xs) => Ty::App(n.clone(), xs.iter().map(|x| sub(x, al)).collect()),
                Ty::Tuple(xs) => Ty::Tuple(xs.iter().map(|x| sub(x, al)).collect()),
                other => other.clone(),
            }
        }
        sub(ty, &aliases).render_pretty()
    }

    /// The names a message about the code at `span` must not give a type
    /// variable: every lower-case name written in the top-level definition
    /// that holds `span` (its name, its parameters and the names its body
    /// binds or uses), so `a` never reads as a value. Read from the source,
    /// so the answer is the same whichever def's inference reports it.
    pub(crate) fn names_at(
        &self,
        module: Option<ModuleId>,
        span: Option<Span>,
    ) -> std::collections::HashSet<String> {
        use syntax::ast::AstNode;
        let mut out = std::collections::HashSet::new();
        let (Some(module), Some(span)) = (module, span) else {
            return out;
        };
        let tree = self.db.module_parse(module).tree();
        let at = span.range.0;
        for decl in tree.decls() {
            let r = decl.syntax().text_range();
            if u32::from(r.start()) <= at && at < u32::from(r.end()) {
                let text = decl.syntax().text().to_string();
                for w in text.split(|c: char| !(c.is_alphanumeric() || c == '_' || c == '\'')) {
                    if w.starts_with(|c: char| c.is_lowercase() || c == '_') {
                        out.insert(w.to_string());
                    }
                }
                break;
            }
        }
        out
    }

    /// The source text at `span` in `module`, when it is short and on one
    /// line (for a message).
    pub(crate) fn span_text(&self, module: Option<ModuleId>, span: Option<Span>) -> Option<String> {
        self.span_text_within(module, span, 40)
    }

    /// As [`Infer::span_text`], up to `max` bytes.
    pub(crate) fn span_text_within(
        &self,
        module: Option<ModuleId>,
        span: Option<Span>,
        max: usize,
    ) -> Option<String> {
        let (module, span) = (module?, span?);
        let src = self.db.module_parse(module).syntax().text().to_string();
        let t = src
            .get(span.range.0 as usize..span.range.1 as usize)?
            .trim();
        (!t.is_empty() && t.len() <= max && !t.contains('\n')).then(|| t.to_string())
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
                if pb.e_errored || pb.e_calls_failed {
                    continue;
                }
                let root = self.uf.find(pb.e_ty);
                let message = if let Content::Rigid(n) = self.uf.content(root) {
                    // A type variable of the annotation: any type, so no
                    // annotation of `e` helps. Name the variable and a type
                    // that would do, of the block's head.
                    let e = self
                        .span_text(pb.module, pb.e_span)
                        .map(|t| format!("`{t}`"))
                        .unwrap_or_else(|| "The expression after `<-`".to_string());
                    let var = Ty::Var(n.clone());
                    let callee = self.read_back(pb.callee_ty);
                    let example = match &callee {
                        Ty::Fun(_, rest) => match rest.as_ref() {
                            Ty::Fun(_, r) => match r.as_ref() {
                                Ty::App(h, a) if !a.is_empty() => {
                                    let mut a = a.clone();
                                    a.pop();
                                    a.push(var);
                                    Some(Ty::App(h.clone(), a))
                                }
                                _ => None,
                            },
                            _ => None,
                        },
                        _ => None,
                    }
                    .unwrap_or_else(|| {
                        Ty::App(
                            Name::new("Task"),
                            vec![Ty::App(Name::new("Error"), Vec::new()), Ty::Var(n.clone())],
                        )
                    });
                    format!(
                        "The bind line `{} <- …` chooses `andThen` from the type of the \
                         expression after `<-`. {e} has the type variable `{}` from the \
                         annotation, which can be any type; a bind line needs a type with an \
                         `andThen` such as `{}`. See docs/errors/E2015-bind-needs-a-type.md",
                        pb.pat_text,
                        n.as_str(),
                        example.render_pretty()
                    )
                } else {
                    let t = self
                        .read_back(pb.e_ty)
                        .render_pretty_avoiding(&self.names_at(pb.module, pb.span));
                    format!(
                        "The bind line `{} <- …` chooses `andThen` from the type of the \
                         expression after `<-`, and that type is still `{t}`. Annotate it, \
                         for example `fetch : String -> Task Error User`. \
                         See docs/errors/E2015-bind-needs-a-type.md",
                        pb.pat_text
                    )
                };
                self.errors.push(crate::infer::TypeError {
                    message,
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
         block must be {} `{head}` too. One block of bind lines chains one type.",
        article(head)
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

/// A block type that its use fixes, so a note never advises another return
/// type for it: the TEA update shape `( model, Cmd msg )`, a view
/// (`Element msg`, `Html msg`) and `Sub msg`, which the app builders
/// (`App.app`, `App.web`, `Live.app`, `Spa.app`) take as they are.
pub(crate) fn fixed_by_use(t: &Ty) -> bool {
    match t {
        Ty::Tuple(parts) => matches!(
            parts.as_slice(),
            [_, Ty::App(cmd, args)] if nominal::strip(cmd.as_str()) == "Cmd" && args.len() == 1
        ),
        Ty::App(n, args) => {
            args.len() == 1 && matches!(nominal::strip(n.as_str()), "Element" | "Html" | "Sub")
        }
        _ => false,
    }
}

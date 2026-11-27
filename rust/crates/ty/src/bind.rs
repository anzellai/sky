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
use crate::{nominal, Ty, TyVarId};
use base::{DefId, ModuleId, Span};
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
    /// The span of the `<-` token.
    pub span: Option<Span>,
    /// The module of the def the bind line is in.
    pub module: Option<ModuleId>,
    /// `p <- …` as written, for the message (a simple pattern only).
    pub pat_text: String,
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

impl NoAndThen {
    fn message(&self, pat: &str) -> String {
        match self {
            NoAndThen::NotNamed(t) => format!(
                "The bind line `{pat} <- …` needs a type with an `andThen`, but the expression \
                 after `<-` is `{t}`, which has none. Use `case` or a plain `let` line. \
                 See docs/errors/E2016-no-andthen.md"
            ),
            NoAndThen::NoHome(t) => format!(
                "The bind line `{pat} <- …` chooses `andThen` from the module that declares \
                 `{t}`, and that module is not known. Use `case`, or call its `andThen` by name. \
                 See docs/errors/E2016-no-andthen.md"
            ),
            NoAndThen::Missing {
                ty,
                module,
                arity,
                unexposed,
            } => {
                let why = if *unexposed {
                    "declares `andThen` but does not expose it"
                } else {
                    "exposes no `andThen`"
                };
                format!(
                    "`{ty}` ({module}) {why}, so the bind line `{pat} <- …` has no \
                     `{}` to chain with. Use `case`. See docs/errors/E2016-no-andthen.md",
                    template_text(ty, *arity)
                )
            }
            NoAndThen::Shape {
                ty,
                module,
                arity,
                found,
            } => format!(
                "`{ty}` ({module}) exposes `andThen : {}`, but a bind line `{pat} <- …` \
                 needs `{}`. Use `case`, or call that `andThen` by name. \
                 See docs/errors/E2016-no-andthen.md",
                found.render_pretty(),
                template_text(ty, *arity)
            ),
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
        let home = KERNEL_BIND_HOMES
            .iter()
            .find(|(n, _)| *n == name)
            .map(|(_, m)| m.to_string())
            .or_else(|| hir::kernel_implicit_home(&name).map(str::to_string))
            .or_else(|| {
                (nominal::is_qualified(&name) && !nominal::is_go_type(&name))
                    .then(|| name.rsplit_once('.').map(|(m, _)| m.to_string()))
                    .flatten()
            })
            // Another builtin (`List`, `Dict`, …): its `Sky.Core` module.
            .or_else(|| {
                (!nominal::is_qualified(&name))
                    .then(|| format!("Sky.Core.{name}"))
                    .filter(|m| self.db.module_by_name(m).is_some())
            });
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
        let before = self.errors.len();
        self.unify(inst, pb.callee_ty);
        let note = mixed_note(&shown);
        self.append_note(before, Some(note));
        self.cur_span = saved;
        self.bind_choices.push((pb.call, res));
        Attempt::Done
    }

    fn bind_error(&mut self, pb: &PendingBind, why: NoAndThen) {
        self.errors.push(crate::infer::TypeError {
            message: why.message(&pb.pat_text),
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

/// The note on a type clash in a block of bind lines: every line and the
/// `in` expression must have the head type of the first.
pub(crate) fn mixed_note(head: &str) -> String {
    format!(
        "\n\nNote: this bind line chains with the `andThen` of `{head}`, so the rest of the \
         block must be a `{head}` too. One block cannot mix types: convert first, e.g. \
         `Task.fromResult` for a `Result` in a `Task` block, or `Maybe.toResult err` for a \
         `Maybe` in a `Result` block."
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

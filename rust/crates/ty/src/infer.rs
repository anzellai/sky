//! Constraint generation + solving, interleaved (doc 06 §"`infer`"). Algorithm-W
//! shaped: every sub-expression yields a [`TyVarId`], unifying against the slot
//! it flows into as it is built — the current Haskell solver is already
//! value-threaded (`solveHelp`), so interleaving is the honest translation.
//!
//! Leniency contract (the accept-parity discipline): an unknown name — a kernel
//! function with no stdlib sig, a Go-FFI reference, an unannotated cross-module
//! def — resolves to a **fresh flexible var**, never an error. That is what keeps
//! the checker from emitting false positives on programs the oracle accepts,
//! while genuine clashes *within* known-typed code still unify-fail (L7).

use crate::sig::World;
use crate::unify::{Content, FlatTy, SuperType, UnionFind};
use crate::{Scheme, Ty, TyVarId};
use base::{DefId, Name, Span};
use hir::{Body, Expr, ExprId, LocalId, PatId, Pattern, Res, SkyDb};
use std::collections::HashMap;

/// A recorded type error (an unify clash). A value, not an exception (L7).
pub struct TypeError {
    pub message: String,
    /// Source span of the sub-expression whose unification clashed, if known.
    /// Populated from `Infer::cur_span` (the expr currently under inference) or,
    /// for the def-level gates, from `body.expr_span(..)`. `None` when no CST
    /// range was recorded (synthesised / recovery nodes) — the renderer then
    /// falls back to the whole-def span.
    pub span: Option<Span>,
}

/// Replace leading `Unit` arguments on a scheme's top arrow-spine with fresh
/// distinct type-var names, so a kernel `() -> X` (and `() -> a -> X`) accepts
/// a call that supplies a real value in the unit slot. Return-position `Unit`
/// (e.g. `Task Error ()`) is untouched — only argument positions relax.
fn relax_unit_arg_spine(s: &Scheme) -> Scheme {
    fn go(ty: &Ty, n: &mut u32) -> Ty {
        match ty {
            Ty::Fun(a, b) => {
                let arg = if matches!(a.as_ref(), Ty::Unit) {
                    *n += 1;
                    Ty::var(&format!("__unitrelax{n}"))
                } else {
                    (**a).clone()
                };
                Ty::Fun(Box::new(arg), Box::new(go(b, n)))
            }
            other => other.clone(),
        }
    }
    let mut n = 0;
    Scheme {
        vars: s.vars.clone(),
        ty: go(&s.ty, &mut n),
    }
}

/// Does `ty` contain a record anywhere in its structure? Gates the annotation
/// gate's param seeding (see [`Infer::infer_def_against`]).
fn ty_contains_record(ty: &Ty) -> bool {
    match ty {
        Ty::Record(..) => true,
        Ty::Fun(a, b) => ty_contains_record(a) || ty_contains_record(b),
        Ty::App(_, args) => args.iter().any(ty_contains_record),
        Ty::Tuple(xs) => xs.iter().any(ty_contains_record),
        Ty::Var(_) | Ty::Unit | Ty::Error => false,
    }
}

pub struct Infer<'a> {
    world: &'a World,
    db: &'a dyn SkyDb,
    pub uf: UnionFind,
    locals: HashMap<LocalId, TyVarId>,
    pub errors: Vec<TypeError>,
    /// Per-expression type-var recording — enabled for the lowerer's typed
    /// table (M4). Off by default so the M3 accept-parity path is unchanged.
    record_exprs: bool,
    expr_vars: Vec<(ExprId, TyVarId)>,
    /// Per-local recording: which locals bound to which type-var (params of the
    /// def + let/lambda binders). The lowerer reads these back for param types.
    local_vars: Vec<(LocalId, TyVarId)>,
    /// The def currently being inferred. Its own body must NOT pick up its
    /// pass-3-inferred polymorphic scheme at a recursive self-reference —
    /// recursion is monomorphic, so the self-call shares the def's type rather
    /// than instantiating a fresh polymorphic copy (which would split e.g.
    /// `SkyResult[any,any]` vs `SkyResult[any,[]any]` in an accumulator helper).
    self_def: Option<DefId>,
    /// Whether to consult the pass-3 INFERRED schemes (`World::inferred_sigs`)
    /// at a `Res::Def` call site. `true` only for the lowerer's typed table
    /// (result pinning) — the accept-parity check (M3) leaves it `false` so a
    /// combinator's precise inferred sig never flags a latent mismatch the
    /// oracle accepts leniently (`Result.withDefault "" (loadEnv-shaped `()`)`).
    use_inferred: bool,
    /// The def's DECLARED scheme, if any — set by the tooling layer's typed
    /// query so `infer_def_typed` unifies the full function type against the
    /// annotation, seeding param types from the signature (so a hover on a
    /// param reflects `f : Int -> Int`, not the body-inferred `number`). Not
    /// set on the M3 accept-parity path, whose behaviour is unchanged.
    expected: Option<Scheme>,
    /// Span of the expression currently under inference — maintained as a stack
    /// discipline by the `infer_expr` wrapper (save on entry, restore on exit).
    /// A unify clash reads this to anchor its `TypeError` at the offending
    /// sub-expression. Read-only bookkeeping; never affects unification.
    cur_span: Option<Span>,
}

impl<'a> Infer<'a> {
    pub fn new(world: &'a World, db: &'a dyn SkyDb) -> Self {
        Infer {
            world,
            db,
            uf: UnionFind::new(),
            locals: HashMap::new(),
            errors: Vec::new(),
            record_exprs: false,
            expr_vars: Vec::new(),
            local_vars: Vec::new(),
            self_def: None,
            use_inferred: false,
            expected: None,
            cur_span: None,
        }
    }

    /// Provide the def's declared scheme so `infer_def_typed` seeds param types
    /// from the annotation (tooling/hover path — see [`Infer::expected`]).
    pub fn with_expected(mut self, scheme: Option<Scheme>) -> Self {
        self.expected = scheme;
        self
    }

    /// Mark the def being inferred so its own body treats a recursive
    /// self-reference monomorphically (see [`Infer::self_def`]).
    pub fn with_self_def(mut self, def: Option<DefId>) -> Self {
        self.self_def = def;
        self
    }

    /// Consult pass-3 inferred schemes at call sites (lowerer only — see
    /// [`Infer::use_inferred`]).
    pub fn with_inferred(mut self, on: bool) -> Self {
        self.use_inferred = on;
        self
    }

    /// Infer a top-level def body, returning its read-back type (the result
    /// type — params are stripped in the resolved HIR). `None` for bodyless
    /// defs (annotation-only / type decls).
    pub fn infer_def(&mut self, body: &Body) -> Option<Ty> {
        let root = body.root?;
        let v = self.infer_expr(body, root);
        Some(self.read_back(v))
    }

    /// Infer a body while recording a per-expression + per-local type table —
    /// the input type-directed lowering (doc 07 §2) consumes. Keyed by `ExprId`
    /// (the arena index): the stable per-expression identity in this HIR, a
    /// cleaner key than a source span for an arena-based IR (see report).
    pub fn infer_def_typed(
        &mut self,
        body: &Body,
    ) -> (
        Option<Ty>,
        std::collections::HashMap<ExprId, Ty>,
        std::collections::HashMap<LocalId, Ty>,
    ) {
        self.record_exprs = true;
        let root = match body.root {
            Some(r) => r,
            None => return (None, Default::default(), Default::default()),
        };
        // Type + bind the top-level params first, so references in the body pick
        // up the same type-var and the locals table carries their inferred type.
        let param_pats: Vec<PatId> = body.params.clone();
        let param_vars: Vec<TyVarId> = param_pats
            .iter()
            .map(|&p| self.infer_pat_fresh(body, p))
            .collect();
        // Seed param types from the declared signature BEFORE inferring the body
        // (tooling path). The annotation is the source of truth for a param's
        // type, so it must win over a loose body-inferred one — e.g. `m : Model`
        // used as `String.fromInt m` (an incomplete edit) must still hover/field
        // -complete as `Model`, not the `Int` the misuse would infer. Peel one
        // arg per top-level param; the tail is the expected result, unified with
        // the body's type afterwards. Clashes are non-fatal (tooling only).
        let mut expected_result: Option<TyVarId> = None;
        if let Some(scheme) = self.expected.take() {
            let mut sub: HashMap<String, TyVarId> = HashMap::new();
            for name in &scheme.vars {
                if name.as_str() != "any" {
                    let fresh = self.uf.fresh_flex();
                    sub.insert(name.as_str().to_string(), fresh);
                }
            }
            let mut cur = scheme.ty.clone();
            for &pv in &param_vars {
                match cur {
                    Ty::Fun(a, b) => {
                        let av = self.ty_to_var(&a, &mut sub);
                        self.unify(pv, av);
                        cur = *b;
                    }
                    _ => break,
                }
            }
            expected_result = Some(self.ty_to_var(&cur, &mut sub));
        }
        let v = self.infer_expr(body, root);
        if let Some(ev) = expected_result {
            self.unify(v, ev);
        }
        let result = self.read_back(v);
        let mut exprs = std::collections::HashMap::new();
        let recorded: Vec<(ExprId, TyVarId)> = std::mem::take(&mut self.expr_vars);
        for (e, tv) in recorded {
            let t = self.read_back(tv);
            exprs.insert(e, t);
        }
        let mut locals = std::collections::HashMap::new();
        let recorded_locals: Vec<(LocalId, TyVarId)> = std::mem::take(&mut self.local_vars);
        for (lid, tv) in recorded_locals {
            let t = self.read_back(tv);
            locals.insert(lid, t);
        }
        (Some(result), exprs, locals)
    }

    /// Enforce a top-level def's body against its DECLARED annotation (the
    /// accept/reject checker's annotation gate — the M3 residual). Seeds each
    /// param var from the annotation's arrow spine, infers the body, and unifies
    /// the body's result type against the annotation's result. A def whose body
    /// contradicts its own signature (`count : Int` / `count = "x"`, or
    /// `grab : Int -> String` / `grab n = n.name`) is a genuine type error the
    /// Haskell oracle rejects; the clash lands in `self.errors` (L7).
    ///
    /// Scoped by the CALLER to the modules under check (never the trusted
    /// stdlib), so this only tightens app-code annotations. It never LOOSENS:
    /// wildcard `any` is instantiated fresh-per-occurrence (so a signature that
    /// widens to `any` still accepts a concrete body), and any unknown/kernel
    /// reference in the body stays a fresh flex var — the enforcement adds
    /// constraints only where both the annotation AND the body are concrete,
    /// which is exactly the "unambiguous contradiction" the corpus targets.
    pub fn infer_def_against(&mut self, body: &Body, scheme: &Scheme) {
        let Some(root) = body.root else { return };
        let param_pats: Vec<PatId> = body.params.clone();
        let param_vars: Vec<TyVarId> = param_pats
            .iter()
            .map(|&p| self.infer_pat_fresh(body, p))
            .collect();
        // SKOLEMIZE the scheme's RESULT-position quantifiers to RIGID vars; leave
        // argument-only quantifiers (and `any`) as fresh-per-occurrence flex.
        //
        // A rigid var BINDS a plain flex (so the body may keep the quantifier
        // genuinely polymorphic — `identity : a -> a`) but CLASHES with a
        // concrete Structure or a different rigid (so a body that PINS the
        // quantifier to a concrete type is rejected). That closes the exploitable
        // soundness hole — an over-general RESULT type (audit #5/#6): `f : a ->
        // a; f n = n + 1` returns `Int` while promising `a`, so a caller relying
        // on the polymorphic return (`f "hi" : String`) gets an `Int` and panics.
        //
        // We deliberately DO NOT skolemize a quantifier that appears ONLY in
        // ARGUMENT position. An over-general argument (`init : a -> (Model, Cmd
        // Msg)` whose body uses `req` as a `Dict`) is a DIFFERENT, milder class:
        // the full-HM oracle also accepts it (shared leniency) and this checker
        // historically instantiated every quantifier flexibly — so keeping
        // argument-only quantifiers flex is exact accept-parity (13-skyshop `init`
        // is framework-called; the runtime always supplies a real request). The
        // distinction is principled, not name-keyed: RESULT over-generality is
        // observable to any caller who trusts the declared return type; ARGUMENT
        // over-generality only misfires if a caller passes an incompatible
        // value, which the oracle itself does not reject. We add rejections only
        // in the result-position class — never a new accept.
        let result_ty = {
            let mut cur = &scheme.ty;
            for _ in 0..param_vars.len() {
                match cur {
                    Ty::Fun(_, b) => cur = b,
                    _ => break,
                }
            }
            cur
        };
        let result_vars: std::collections::HashSet<String> = result_ty
            .free_vars()
            .into_iter()
            .map(|n| n.as_str().to_string())
            .collect();
        let mut sub: HashMap<String, TyVarId> = HashMap::new();
        for name in &scheme.vars {
            if name.as_str() != "any" {
                let fresh = if result_vars.contains(name.as_str()) {
                    self.uf.fresh(Content::Rigid(Name::new(name.as_str())))
                } else {
                    self.uf.fresh_flex()
                };
                sub.insert(name.as_str().to_string(), fresh);
            }
        }
        // Peel one arrow per top-level param, seeding the param's declared type.
        // A param whose declared type CONTAINS a record is NOT seeded: pinning a
        // record-alias param (`model : Model`) makes real TEA code's record
        // updates / `List.map`-over-field threading clash on field-presence
        // under this checker's record unification (which the full-HM oracle
        // resolves) — a false positive on accepted code (19-skyforum). Seeding
        // primitive / nominal / function params still catches the genuine
        // non-record-vs-record misuse (`grab : Int -> String; grab n = n.name`).
        let mut cur = scheme.ty.clone();
        let mut consumed = 0usize;
        for &pv in &param_vars {
            match cur {
                Ty::Fun(a, b) => {
                    if !ty_contains_record(&a) {
                        let av = self.ty_to_var_open(&a, &mut sub);
                        self.unify(pv, av);
                    }
                    cur = *b;
                    consumed += 1;
                }
                _ => break,
            }
        }
        // Arity gate (RC1 / audit #4): the body binds MORE top-level params than
        // the declared signature has arrows, AND the declared result is a
        // concrete non-function type that cannot absorb the extra params. This
        // is a genuine signature-vs-body arity mismatch — `f : Int -> Int;
        // f x y = x + y` — which the flexible-instantiation path (arrow-peel
        // stops at `_ => break`, silently dropping leftover params) would
        // otherwise ACCEPT, build, and then panic at runtime (`rt.AsInt: got
        // func(...)`, oracle rejects at compile time). We fire only when the
        // remaining result is DEFINITELY concrete-non-function: a `Ty::Var`
        // result (polymorphic return may itself be a function), `Ty::Error`
        // (cascade suppression), and the `any` wildcard stay lenient. Aliases
        // are already unfolded in `sig` (`getUser : Handler` → `Fun`), so a
        // function-typed alias result correctly presents as `Ty::Fun` and is
        // consumed by the loop, never reaching this gate.
        let cur_is_concrete_non_fun = match &cur {
            Ty::Var(_) | Ty::Error | Ty::Fun(..) => false,
            Ty::App(name, _) if name.as_str() == "any" => false,
            _ => true,
        };
        if consumed < param_vars.len() && cur_is_concrete_non_fun {
            self.errors.push(TypeError {
                message: format!(
                    "the body binds {} parameter(s) but the type signature declares only {}",
                    param_vars.len(),
                    consumed
                ),
                span: body.expr_span(root),
            });
        }
        // Record-literal-vs-closed-annotation strictness (RC2 / audit #1,#2): a
        // bare record LITERAL checked against a CLOSED declared record type must
        // have an EXACTLY matching field set. The general body unify below stays
        // presence-lenient (protecting real TEA model-threading through record
        // updates / subset field access — 19-skyforum), but a literal written
        // directly against a closed annotation has no such excuse: a missing
        // field silently zero-fills and an extra field is silently dropped (both
        // accepted here, both rejected by the oracle). Fires only for a bare
        // `Expr::Record` literal (never `Expr::Update`) against `Ty::Record(_,
        // None)` (closed; `Some(ext)` = extensible → stays lenient).
        if let (Expr::Record(lit_fields), Ty::Record(decl_fields, None)) =
            (&body.exprs[root], &cur)
        {
            for (dn, _) in decl_fields {
                if !lit_fields.iter().any(|(ln, _)| ln == dn) {
                    self.errors.push(TypeError {
                        message: format!(
                            "record literal is missing required field `{}`",
                            dn.as_str()
                        ),
                        // No literal expr for a missing field — anchor at the
                        // whole record literal (the def root here).
                        span: body.expr_span(root),
                    });
                }
            }
            for (ln, fid) in lit_fields {
                if !decl_fields.iter().any(|(dn, _)| dn == ln) {
                    self.errors.push(TypeError {
                        message: format!(
                            "record literal has unknown field `{}` not in the declared type",
                            ln.as_str()
                        ),
                        // Precise: anchor at the offending field's value expr.
                        span: body.expr_span(*fid),
                    });
                }
            }
        }
        let expected_result = self.ty_to_var_open(&cur, &mut sub);
        // Body inference stays STRICT (record-presence clashes inside the body —
        // e.g. a call passing a record that lacks a required field — must still
        // reject; that is exactly `record_missing_field` at its call site).
        let v = self.infer_expr(body, root);
        // Only the final body-vs-annotation unification is presence-lenient:
        // a body result that structurally matches the declared type up to open
        // rows is accepted, while a field-TYPE clash (age String vs Int) or a
        // non-record-vs-record clash still lands in `errors`.
        self.uf.lenient_record_presence = true;
        self.unify(v, expected_result);
        self.uf.lenient_record_presence = false;
    }

    /// Like [`Infer::ty_to_var`] but seeds every CLOSED record (`ext = None`)
    /// with a fresh row variable, making it OPEN. Used only by the annotation
    /// gate ([`Infer::infer_def_against`]): a declared record type constrains
    /// the body's field TYPES and overall SHAPE, but must not force exact
    /// field-presence parity — real TEA code threads models through record
    /// updates / subset accesses that the lenient body checker models with open
    /// rows, so a closed seed would false-positive "missing/extra field" on
    /// accepted programs (accept-parity gate: 19-skyforum). Opening the seed
    /// keeps the sound clashes (wrong field type, non-record vs record,
    /// primitive vs primitive) while dropping the field-presence class that the
    /// full-HM oracle resolves and this checker cannot match exactly.
    fn ty_to_var_open(&mut self, ty: &Ty, sub: &mut HashMap<String, TyVarId>) -> TyVarId {
        match ty {
            Ty::Fun(a, b) => {
                let va = self.ty_to_var_open(a, sub);
                let vb = self.ty_to_var_open(b, sub);
                self.fun(va, vb)
            }
            Ty::App(name, args) => {
                let vs: Vec<TyVarId> = args.iter().map(|a| self.ty_to_var_open(a, sub)).collect();
                self.uf
                    .fresh(Content::Structure(FlatTy::App(name.clone(), vs)))
            }
            Ty::Tuple(xs) => {
                let vs: Vec<TyVarId> = xs.iter().map(|x| self.ty_to_var_open(x, sub)).collect();
                self.uf.fresh(Content::Structure(FlatTy::Tuple(vs)))
            }
            Ty::Record(fields, ext) => {
                let mut map = std::collections::BTreeMap::new();
                for (n, t) in fields {
                    let v = self.ty_to_var_open(t, sub);
                    map.insert(n.clone(), v);
                }
                // Force OPEN: reuse a named row var if present, else a fresh one.
                let ext_var = match ext {
                    Some(e) if e.as_str() != "any" => Some(
                        *sub.entry(e.as_str().to_string())
                            .or_insert_with(|| self.uf.fresh_flex()),
                    ),
                    _ => Some(self.uf.fresh_flex()),
                };
                self.uf
                    .fresh(Content::Structure(FlatTy::Record(map, ext_var)))
            }
            // vars / unit / error: identical to `ty_to_var`.
            _ => self.ty_to_var(ty, sub),
        }
    }

    /// Infer the FULL scheme of a (typically unannotated) top-level def:
    /// `param0 -> … -> paramN -> result`, generalised over its residual vars.
    /// Used to give unannotated stdlib combinators (`Result.map3`, `List.foldl`,
    /// `List.map`, …) a real polymorphic signature so that applying them at a
    /// call site pins the result type from the argument types (proper HM). The
    /// scheme read-back maps every unbound flex/super var to a *distinct*
    /// quantifier so two independent vars never collapse into one.
    pub fn infer_def_scheme(&mut self, body: &Body) -> Option<Scheme> {
        let root = body.root?;
        let param_vars: Vec<TyVarId> = body
            .params
            .iter()
            .map(|&p| self.infer_pat_fresh(body, p))
            .collect();
        let rv = self.infer_expr(body, root);
        let full = param_vars
            .into_iter()
            .rev()
            .fold(rv, |acc, pv| self.fun(pv, acc));
        let ty = self.read_back_scheme(full);
        Some(Scheme::generalize(ty))
    }

    fn unify(&mut self, a: TyVarId, b: TyVarId) {
        if let Err(m) = self.uf.unify(a, b) {
            self.errors.push(TypeError {
                message: m.message,
                span: self.cur_span,
            });
        }
    }

    // ---- expressions ----------------------------------------------------

    fn infer_expr(&mut self, body: &Body, e: ExprId) -> TyVarId {
        // Track the span of the sub-expression under inference so a unify clash
        // deeper in the tree anchors its diagnostic here. Save/restore so the
        // parent's span is reinstated as the recursion unwinds. `.or(prev)`
        // keeps a parent's span when this node has no recorded range.
        let prev = self.cur_span;
        self.cur_span = body.expr_span(e).or(prev);
        let tv = self.infer_expr_inner(body, e);
        self.cur_span = prev;
        if self.record_exprs {
            self.expr_vars.push((e, tv));
        }
        tv
    }

    fn infer_expr_inner(&mut self, body: &Body, e: ExprId) -> TyVarId {
        match &body.exprs[e] {
            Expr::Int(_) => self.uf.fresh(Content::FlexSuper(SuperType::Number)),
            Expr::Float(_) => self.con("Float", vec![]),
            Expr::Str(_) => self.con("String", vec![]),
            Expr::Chr(_) => self.con("Char", vec![]),
            Expr::Bool(_) => self.con("Bool", vec![]),
            Expr::Unit => self.uf.fresh(Content::Structure(FlatTy::Unit)),
            Expr::List(elems) => {
                let elem = self.uf.fresh_flex();
                for &el in elems {
                    let te = self.infer_expr(body, el);
                    self.unify(te, elem);
                }
                self.con("List", vec![elem])
            }
            Expr::Tuple(elems) => {
                let vs: Vec<TyVarId> = elems.iter().map(|&el| self.infer_expr(body, el)).collect();
                self.uf.fresh(Content::Structure(FlatTy::Tuple(vs)))
            }
            Expr::Record(fields) => {
                let mut map = std::collections::BTreeMap::new();
                for (n, val) in fields {
                    let tv = self.infer_expr(body, *val);
                    map.insert(n.clone(), tv);
                }
                self.uf.fresh(Content::Structure(FlatTy::Record(map, None)))
            }
            Expr::Update { base, fields } => {
                let tb = self.infer_expr(body, *base);
                let mut map = std::collections::BTreeMap::new();
                for (n, val) in fields {
                    let tv = self.infer_expr(body, *val);
                    map.insert(n.clone(), tv);
                }
                // base must be an open record carrying at least the updated fields
                let row = self.uf.fresh_flex();
                let constraint = self.uf.fresh(Content::Structure(FlatTy::Record(map, Some(row))));
                self.unify(tb, constraint);
                tb
            }
            Expr::Var(res) => self.infer_res(res.clone()),
            Expr::Negate(inner) => {
                let ti = self.infer_expr(body, *inner);
                let num = self.uf.fresh(Content::FlexSuper(SuperType::Number));
                self.unify(ti, num);
                ti
            }
            Expr::Lambda { params, body: lb } => {
                let param_vars: Vec<TyVarId> =
                    params.iter().map(|&p| self.infer_pat_fresh(body, p)).collect();
                let rb = self.infer_expr(body, *lb);
                param_vars
                    .into_iter()
                    .rev()
                    .fold(rb, |acc, pv| self.fun(pv, acc))
            }
            Expr::Call(callee, args) => {
                let mut tf = self.infer_expr(body, *callee);
                for &arg in args {
                    let ta = self.infer_expr(body, arg);
                    let res = self.uf.fresh_flex();
                    let want = self.fun(ta, res);
                    self.unify(tf, want);
                    tf = res;
                }
                tf
            }
            Expr::Binop { op, lhs, rhs, .. } => {
                let tl = self.infer_expr(body, *lhs);
                let tr = self.infer_expr(body, *rhs);
                self.infer_binop(op.as_str(), tl, tr)
            }
            Expr::If { arms, els } => {
                let result = self.uf.fresh_flex();
                for (cond, then) in arms {
                    let tc = self.infer_expr(body, *cond);
                    let boolt = self.con("Bool", vec![]);
                    self.unify(tc, boolt);
                    let tt = self.infer_expr(body, *then);
                    self.unify(tt, result);
                }
                let te = self.infer_expr(body, *els);
                self.unify(te, result);
                result
            }
            Expr::Let { defs, body: lb } => {
                // pre-bind binder names for forward reference / recursion.
                for d in defs {
                    for (_, lid) in &d.binders {
                        self.locals.entry(*lid).or_insert_with(|| self.uf.fresh_flex());
                    }
                }
                for d in defs {
                    // parameters (function let-binding) get fresh vars, then body.
                    let param_vars: Vec<TyVarId> =
                        d.params.iter().map(|&p| self.infer_pat_fresh(body, p)).collect();
                    let tv = self.infer_expr(body, d.body);
                    let full = param_vars
                        .into_iter()
                        .rev()
                        .fold(tv, |acc, pv| self.fun(pv, acc));
                    if let Some(pat) = d.pat {
                        // destructure binding: pattern typed against the value.
                        self.infer_pat_against(body, pat, full);
                    }
                    for (_, lid) in &d.binders {
                        if let Some(&placeholder) = self.locals.get(lid) {
                            self.unify(placeholder, full);
                            // Tooling table only (inlay hints / hover on a let
                            // binding): record the binder's type var so the
                            // per-local table carries it. Guarded by
                            // `record_exprs`, so the check/build path (which never
                            // sets it) is byte-for-byte unchanged.
                            if self.record_exprs {
                                self.local_vars.push((*lid, placeholder));
                            }
                        }
                    }
                }
                self.infer_expr(body, *lb)
            }
            Expr::Case { subject, branches } => {
                let ts = self.infer_expr(body, *subject);
                let result = self.uf.fresh_flex();
                for br in branches {
                    self.infer_pat_against(body, br.pat, ts);
                    let tb = self.infer_expr(body, br.body);
                    self.unify(tb, result);
                }
                result
            }
            Expr::Accessor(field) => {
                let fv = self.uf.fresh_flex();
                let row = self.uf.fresh_flex();
                let mut map = std::collections::BTreeMap::new();
                map.insert(field.clone(), fv);
                let rec = self.uf.fresh(Content::Structure(FlatTy::Record(map, Some(row))));
                self.fun(rec, fv)
            }
            Expr::Access(base, field) => {
                let tb = self.infer_expr(body, *base);
                let fv = self.uf.fresh_flex();
                let row = self.uf.fresh_flex();
                let mut map = std::collections::BTreeMap::new();
                map.insert(field.clone(), fv);
                let rec = self.uf.fresh(Content::Structure(FlatTy::Record(map, Some(row))));
                self.unify(tb, rec);
                fv
            }
            Expr::Error => self.uf.fresh_flex(),
        }
    }

    fn infer_res(&mut self, res: Res) -> TyVarId {
        match res {
            Res::Local(id) => *self
                .locals
                .entry(id)
                .or_insert_with(|| self.uf.fresh_flex()),
            Res::Def(def) => {
                // Monomorphic recursion: the def's own body never instantiates
                // its own scheme (see `self_def`).
                // An ANNOTATED def uses its annotation even at a recursive
                // self-reference — annotated recursion is sound HM (the sig is
                // the fixed point). Only the def's own INFERRED (pass-3) scheme
                // is skipped for self, so unannotated recursive helpers stay
                // monomorphic (a polymorphic self-instantiation would split
                // e.g. `SkyResult[any,any]` vs `SkyResult[any,[]any]`).
                if let Some(s) = self.world.value_sigs.get(&def) {
                    let s = s.clone();
                    return self.instantiate(&s);
                }
                if self.self_def == Some(def) {
                    return self.uf.fresh_flex();
                }
                if let Some(s) = self.world.inferred_sigs.get(&def) {
                    if self.use_inferred {
                        let s = s.clone();
                        return self.instantiate(&s);
                    }
                }
                // CHECK-ONLY precise combinator sig (audit #3). Consulted only on
                // the accept/reject-check path (`!use_inferred`) so the lowerer's
                // lenient wildcard behaviour — and hence Go emission — is
                // unchanged. Pins e.g. `List.map`'s result element from its arg.
                if !self.use_inferred {
                    if let Some(s) = self.world.check_sigs.get(&def) {
                        let s = s.clone();
                        return self.instantiate(&s);
                    }
                }
                self.uf.fresh_flex()
            }
            Res::Kernel { module, func } => {
                let key = (module.as_str().to_string(), func.as_str().to_string());
                if let Some(s) = self.world.kernel_sigs.get(&key) {
                    // Zero-arg kernel-shim class (Limitation #7 family:
                    // `loadEnv`/`uuidV4`/`timeNow`/`Pure.*`): a kernel
                    // `() -> X` accepts a call with or without the unit, so
                    // relax leading `Unit` params to flex. Narrow — only
                    // affects Unit-first-param kernel sigs.
                    let s = relax_unit_arg_spine(s);
                    return self.instantiate(&s);
                }
                // CHECK-ONLY precise combinator sig for a bare prelude-qualified
                // `List.map` that stayed `Res::Kernel` (audit #3). Same
                // `!use_inferred` gate + `relax` symmetry as the kernel path.
                if !self.use_inferred {
                    if let Some(s) = self.world.check_kernel_sigs.get(&key) {
                        let s = relax_unit_arg_spine(s);
                        return self.instantiate(&s);
                    }
                }
                self.uf.fresh_flex()
            }
            Res::Ctor(cr) => {
                // Disambiguate same-named ctors by DefId first, then by name
                // (builtins Just/Ok/… live in the by-name table only).
                if let Some(s) = self.world.ctors_by_def.get(&cr.def).cloned() {
                    return self.instantiate(&s);
                }
                let name = self
                    .db
                    .def_loc(cr.def)
                    .map(|l| l.name.as_str().to_string());
                match name.and_then(|n| self.world.ctors.get(&n).cloned()) {
                    Some(s) => self.instantiate(&s),
                    None => self.uf.fresh_flex(),
                }
            }
            Res::Foreign { .. } | Res::Error => self.uf.fresh_flex(),
        }
    }

    fn infer_binop(&mut self, op: &str, tl: TyVarId, tr: TyVarId) -> TyVarId {
        match op {
            // arithmetic: number a => a -> a -> a
            "+" | "-" | "*" | "/" | "^" => {
                let n = self.uf.fresh(Content::FlexSuper(SuperType::Number));
                self.unify(tl, n);
                self.unify(tr, n);
                n
            }
            "//" | "%" => {
                let int = self.con("Int", vec![]);
                self.unify(tl, int);
                self.unify(tr, int);
                self.con("Int", vec![])
            }
            // appendable a => a -> a -> a
            "++" => {
                let a = self.uf.fresh(Content::FlexSuper(SuperType::Appendable));
                self.unify(tl, a);
                self.unify(tr, a);
                a
            }
            // cons: a -> List a -> List a
            "::" => {
                let list = self.con("List", vec![tl]);
                self.unify(tr, list);
                tr
            }
            // equality / comparison: a -> a -> Bool (lenient — no super-gate)
            "==" | "/=" | "<" | ">" | "<=" | ">=" => {
                self.unify(tl, tr);
                self.con("Bool", vec![])
            }
            "&&" | "||" => {
                let boolt = self.con("Bool", vec![]);
                self.unify(tl, boolt);
                self.unify(tr, boolt);
                self.con("Bool", vec![])
            }
            // pipes: a |> (a -> b) => b   ;   (a -> b) <| a => b
            "|>" => {
                let b = self.uf.fresh_flex();
                let f = self.fun(tl, b);
                self.unify(tr, f);
                b
            }
            "<|" => {
                let b = self.uf.fresh_flex();
                let f = self.fun(tr, b);
                self.unify(tl, f);
                b
            }
            // composition: (a->b) >> (b->c) => a->c  ; (b->c) << (a->b) => a->c
            ">>" => {
                let (a, b, c) = (self.uf.fresh_flex(), self.uf.fresh_flex(), self.uf.fresh_flex());
                let ab = self.fun(a, b);
                let bc = self.fun(b, c);
                self.unify(tl, ab);
                self.unify(tr, bc);
                self.fun(a, c)
            }
            "<<" => {
                let (a, b, c) = (self.uf.fresh_flex(), self.uf.fresh_flex(), self.uf.fresh_flex());
                let bc = self.fun(b, c);
                let ab = self.fun(a, b);
                self.unify(tl, bc);
                self.unify(tr, ab);
                self.fun(a, c)
            }
            _ => self.uf.fresh_flex(),
        }
    }

    // ---- patterns -------------------------------------------------------

    /// Type a pattern, returning a fresh var for its type (used for lambda /
    /// function-let params: no external expected type).
    fn infer_pat_fresh(&mut self, body: &Body, p: PatId) -> TyVarId {
        let v = self.uf.fresh_flex();
        self.infer_pat_against(body, p, v);
        v
    }

    /// Type a pattern against an expected type var, binding its locals.
    fn infer_pat_against(&mut self, body: &Body, p: PatId, expected: TyVarId) {
        match &body.pats[p] {
            Pattern::Anything => {}
            Pattern::Var(id) => {
                let id = *id;
                self.locals.insert(id, expected); if self.record_exprs { self.local_vars.push((id, expected)); }
            }
            Pattern::Unit => {
                let u = self.uf.fresh(Content::Structure(FlatTy::Unit));
                self.unify(expected, u);
            }
            Pattern::Bool(_) => {
                let b = self.con("Bool", vec![]);
                self.unify(expected, b);
            }
            Pattern::Int(_) => {
                let n = self.uf.fresh(Content::FlexSuper(SuperType::Number));
                self.unify(expected, n);
            }
            Pattern::Float(_) => {
                let f = self.con("Float", vec![]);
                self.unify(expected, f);
            }
            Pattern::Str(_) => {
                let s = self.con("String", vec![]);
                self.unify(expected, s);
            }
            Pattern::Chr(_) => {
                let c = self.con("Char", vec![]);
                self.unify(expected, c);
            }
            Pattern::Record(binders) => {
                let mut map = std::collections::BTreeMap::new();
                for (n, id) in binders {
                    let fv = self.uf.fresh_flex();
                    self.locals.insert(*id, fv); if self.record_exprs { self.local_vars.push((*id, fv)); }
                    map.insert(n.clone(), fv);
                }
                let row = self.uf.fresh_flex();
                let rec = self.uf.fresh(Content::Structure(FlatTy::Record(map, Some(row))));
                self.unify(expected, rec);
            }
            Pattern::Alias(inner, id) => {
                let inner = *inner;
                let id = *id;
                self.locals.insert(id, expected); if self.record_exprs { self.local_vars.push((id, expected)); }
                self.infer_pat_against(body, inner, expected);
            }
            Pattern::Tuple(pats) => {
                let vs: Vec<TyVarId> = pats.iter().map(|_| self.uf.fresh_flex()).collect();
                let pats: Vec<PatId> = pats.clone();
                let tup = self.uf.fresh(Content::Structure(FlatTy::Tuple(vs.clone())));
                self.unify(expected, tup);
                for (pat, v) in pats.iter().zip(vs) {
                    self.infer_pat_against(body, *pat, v);
                }
            }
            Pattern::List(pats) => {
                let elem = self.uf.fresh_flex();
                let list = self.con("List", vec![elem]);
                self.unify(expected, list);
                let pats: Vec<PatId> = pats.clone();
                for pat in pats {
                    self.infer_pat_against(body, pat, elem);
                }
            }
            Pattern::Cons(head, tail) => {
                let (head, tail) = (*head, *tail);
                let elem = self.uf.fresh_flex();
                let list = self.con("List", vec![elem]);
                self.unify(expected, list);
                self.infer_pat_against(body, head, elem);
                let list2 = self.con("List", vec![elem]);
                self.infer_pat_against(body, tail, list2);
            }
            Pattern::Ctor { ctor, name, args } => {
                let args: Vec<PatId> = args.clone();
                let cname = name.as_str().to_string();
                let by_def = ctor
                    .as_ref()
                    .and_then(|cr| self.world.ctors_by_def.get(&cr.def).cloned());
                // instantiate the ctor scheme: peel args, unify result w/ expected.
                if let Some(scheme) = by_def.or_else(|| self.world.ctors.get(&cname).cloned()) {
                    let mut cur = self.instantiate(&scheme);
                    let mut arg_vars = Vec::new();
                    for _ in &args {
                        let a = self.uf.fresh_flex();
                        let r = self.uf.fresh_flex();
                        let want = self.fun(a, r);
                        self.unify(cur, want);
                        arg_vars.push(a);
                        cur = r;
                    }
                    self.unify(cur, expected);
                    for (pat, av) in args.iter().zip(arg_vars) {
                        self.infer_pat_against(body, *pat, av);
                    }
                } else {
                    // unknown ctor — type args leniently, no result constraint.
                    for pat in &args {
                        let _ = self.infer_pat_fresh(body, *pat);
                    }
                }
            }
            Pattern::Error => {}
        }
    }

    // ---- helpers --------------------------------------------------------

    fn con(&mut self, name: &str, args: Vec<TyVarId>) -> TyVarId {
        self.uf
            .fresh(Content::Structure(FlatTy::App(Name::new(name), args)))
    }

    fn fun(&mut self, from: TyVarId, to: TyVarId) -> TyVarId {
        self.uf.fresh(Content::Structure(FlatTy::Fun(from, to)))
    }

    // ---- instantiation (doc 06 §"Generalisation & instantiation") -------

    fn instantiate(&mut self, s: &Scheme) -> TyVarId {
        let mut sub: HashMap<String, TyVarId> = HashMap::new();
        for v in &s.vars {
            if v.as_str() == "any" {
                continue; // per-occurrence: never shared (Instantiate.hs:43)
            }
            let fresh = self.uf.fresh_flex();
            sub.insert(v.as_str().to_string(), fresh);
        }
        self.ty_to_var(&s.ty, &mut sub)
    }

    fn ty_to_var(&mut self, ty: &Ty, sub: &mut HashMap<String, TyVarId>) -> TyVarId {
        match ty {
            Ty::Var(n) => {
                if n.as_str() == "any" {
                    // wildcard: a fresh var at EVERY occurrence (buildEnv).
                    return self.uf.fresh_flex();
                }
                if let Some(&v) = sub.get(n.as_str()) {
                    v
                } else {
                    let v = self.uf.fresh_flex();
                    sub.insert(n.as_str().to_string(), v);
                    v
                }
            }
            Ty::Fun(a, b) => {
                let va = self.ty_to_var(a, sub);
                let vb = self.ty_to_var(b, sub);
                self.fun(va, vb)
            }
            Ty::App(name, args) => {
                let vs: Vec<TyVarId> = args.iter().map(|a| self.ty_to_var(a, sub)).collect();
                self.uf
                    .fresh(Content::Structure(FlatTy::App(name.clone(), vs)))
            }
            Ty::Tuple(xs) => {
                let vs: Vec<TyVarId> = xs.iter().map(|x| self.ty_to_var(x, sub)).collect();
                self.uf.fresh(Content::Structure(FlatTy::Tuple(vs)))
            }
            Ty::Record(fields, ext) => {
                let mut map = std::collections::BTreeMap::new();
                for (n, t) in fields {
                    let v = self.ty_to_var(t, sub);
                    map.insert(n.clone(), v);
                }
                let ext_var = ext.as_ref().map(|e| {
                    if e.as_str() == "any" {
                        self.uf.fresh_flex()
                    } else if let Some(&v) = sub.get(e.as_str()) {
                        v
                    } else {
                        let v = self.uf.fresh_flex();
                        sub.insert(e.as_str().to_string(), v);
                        v
                    }
                });
                self.uf
                    .fresh(Content::Structure(FlatTy::Record(map, ext_var)))
            }
            Ty::Unit => self.uf.fresh(Content::Structure(FlatTy::Unit)),
            Ty::Error => self.uf.fresh(Content::Error),
        }
    }

    // ---- read-back (variableToType, Solve.hs:1428) ----------------------

    pub fn read_back(&mut self, v: TyVarId) -> Ty {
        let mut seen = std::collections::HashSet::new();
        self.read_back_seen(v, &mut seen, false)
    }

    /// Read-back for scheme generation: every unbound flex/super var becomes a
    /// *distinct* quantifier `t<repr>` (super-constraints are dropped — lenient,
    /// accept-more). This avoids (a) `Number → App("number")` unifying wrongly
    /// against `Int`/`Float`, and (b) two independent `comparable`/`appendable`
    /// vars collapsing onto one shared name and over-constraining the scheme.
    fn read_back_scheme(&mut self, v: TyVarId) -> Ty {
        let mut seen = std::collections::HashSet::new();
        self.read_back_seen(v, &mut seen, true)
    }

    fn read_back_seen(
        &mut self,
        v: TyVarId,
        seen: &mut std::collections::HashSet<TyVarId>,
        scheme: bool,
    ) -> Ty {
        let r = self.uf.find(v);
        if !seen.insert(r) {
            return Ty::Error; // cycle guard (anyEquivSeen, Solve.hs:1449)
        }
        let super_var = |r: TyVarId| Ty::Var(Name::new(&format!("t{}", r.0)));
        let out = match self.uf.content(r) {
            Content::Flex => Ty::Var(Name::new(&format!("t{}", r.0))),
            Content::Rigid(n) => Ty::Var(n),
            Content::FlexSuper(_) if scheme => super_var(r),
            Content::FlexSuper(SuperType::Number) => Ty::app("number", vec![]),
            Content::FlexSuper(SuperType::Comparable) => Ty::var("comparable"),
            Content::FlexSuper(SuperType::Appendable) => Ty::var("appendable"),
            Content::FlexSuper(SuperType::CompAppend) => Ty::var("compappend"),
            Content::Error => Ty::Error,
            Content::Structure(ft) => match ft {
                FlatTy::App(name, args) => Ty::App(
                    name,
                    args.into_iter().map(|a| self.read_back_seen(a, seen, scheme)).collect(),
                ),
                FlatTy::Fun(a, b) => Ty::Fun(
                    Box::new(self.read_back_seen(a, seen, scheme)),
                    Box::new(self.read_back_seen(b, seen, scheme)),
                ),
                FlatTy::Tuple(xs) => {
                    Ty::Tuple(xs.into_iter().map(|x| self.read_back_seen(x, seen, scheme)).collect())
                }
                FlatTy::Unit => Ty::Unit,
                FlatTy::Record(fs, ext) => {
                    let mut fields: Vec<(Name, Ty)> = fs
                        .into_iter()
                        .map(|(n, t)| (n, self.read_back_seen(t, seen, scheme)))
                        .collect();
                    fields.sort_by(|a, b| a.0.as_str().cmp(b.0.as_str()));
                    let ext_name = ext.and_then(|e| match self.uf.content(e) {
                        Content::Flex => Some(Name::new(&format!("r{}", self.uf.find(e).0))),
                        _ => None,
                    });
                    Ty::Record(fields, ext_name)
                }
            },
        };
        seen.remove(&r);
        out
    }
}

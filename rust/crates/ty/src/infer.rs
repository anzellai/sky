//! Constraint generation + solving, interleaved (doc 06 §"`infer`"). Algorithm-W
//! shaped: every sub-expression yields a [`TyVarId`], unifying against the slot
//! it flows into as it is built — the current Haskell solver is already
//! value-threaded (`solveHelp`), so interleaving is the honest translation.
//!
//! Derivative work adapted from elm/compiler's `Type.Unify` and `Type.Solve`
//! (Copyright © 2012–present Evan Czaplicki, BSD-3-Clause). See NOTICE.md at the
//! repo root for the full attribution and licence text.
//!
//! Leniency contract (the accept-parity discipline): an unknown name — a kernel
//! function with no stdlib sig, an unannotated cross-module def — resolves to a
//! **fresh flexible var**, never an error. That is what keeps the checker from
//! emitting false positives on programs the oracle accepts, while genuine clashes
//! *within* known-typed code still unify-fail (L7).
//!
//! A **Go-FFI reference is NOT lenient** (v0.27.0). Its pinned `skyType`
//! reaches every inference run through the type database (`SkyDb::ffi_fn`), is
//! parsed on first use (`crate::ffi_sig`), and is instantiated like a kernel
//! signature: the `Result Error` wrapper, the arity and the primitives are
//! enforced; Go-opaque positions are the per-occurrence wildcard `any`. Only a
//! reference whose package surface is not loaded at all stays a fresh flexible
//! var — lowering then refuses to emit it (`sky install`), so no unsound binary
//! can come of it.

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
    /// The diagnostic code the checker renders. Defaults to `"E2001"` (the
    /// unify-clash class); the arity gate sets `"E2007"` so an over-application
    /// gets a precise arity message instead of a generic "T vs function" clash.
    pub code: &'static str,
}

/// The arrow count of `t`'s full spine — its arity as a curried function.
/// `Int -> Int -> Int` is 2; a non-function is 0. Aliases are already expanded
/// at this layer, so a function-returning callee's result contributes to the
/// count (which is exactly what the arity gate needs).
fn count_fun_arrows(t: &Ty) -> usize {
    match t {
        Ty::Fun(_, r) => 1 + count_fun_arrows(r),
        _ => 0,
    }
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
    /// Go-FFI schemes parsed so far in THIS run, keyed by `(package, name)`.
    /// A skyType is parsed only when a reference to it is first instantiated,
    /// and at most once per run — never the whole surface.
    ffi_schemes: HashMap<(Name, Name), Scheme>,
    /// Let-generalisation (doc 06 §"Let-generalisation"): a let binder that
    /// passes the value restriction (a function, or a syntactic value),
    /// generalised over the flex vars its type does not share with the
    /// enclosing scope. Maps the binder to its generic type var and the
    /// quantified roots; every reference instantiates a fresh copy.
    poly_locals: HashMap<LocalId, (TyVarId, Vec<TyVarId>)>,
    /// Every instantiation of a `poly_locals` binder, in order. Read by
    /// [`Infer::collapse_let_poly`] at the end of the def.
    poly_insts: Vec<(LocalId, TyVarId)>,
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
            ffi_schemes: HashMap::new(),
            poly_locals: HashMap::new(),
            poly_insts: Vec::new(),
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

    /// Record the per-expression type-var table WITHOUT otherwise changing
    /// inference — what the `[E2008]` unsupported-`Dict`-key scan needs from the
    /// accept-parity path (`check.rs`).
    ///
    /// **This flag is inference-neutral by construction.** Every one of its read
    /// sites (`infer_expr`, and the three local-binder sites) does nothing but
    /// `push` onto `expr_vars` / `local_vars`; none of them unifies, mints a
    /// var, or branches the algorithm. So switching it on for the checker cannot
    /// change which programs are accepted — it only makes the types the checker
    /// already computed READABLE afterwards, via [`Infer::recorded_expr_types`].
    pub fn with_record_exprs(mut self, on: bool) -> Self {
        self.record_exprs = on;
        self
    }

    /// Drain the recorded per-expression table, reading each type back.
    ///
    /// Read-back is memoised per union-find ROOT: a def's expressions share very
    /// few distinct solved types (every reference to one param resolves to the
    /// same root), so this costs strictly less than `infer_def_typed`'s
    /// unconditional per-expression `read_back` — which the build path already
    /// pays for every def. Empty unless [`Infer::with_record_exprs`] was set.
    pub fn recorded_expr_types(&mut self) -> Vec<(ExprId, Ty)> {
        let recorded: Vec<(ExprId, TyVarId)> = std::mem::take(&mut self.expr_vars);
        let mut memo: HashMap<TyVarId, Ty> = HashMap::new();
        let mut out = Vec::with_capacity(recorded.len());
        for (e, tv) in recorded {
            let root = self.uf.find(tv);
            let t = match memo.get(&root) {
                Some(t) => t.clone(),
                None => {
                    let t = self.read_back(tv);
                    memo.insert(root, t.clone());
                    t
                }
            };
            out.push((e, t));
        }
        out
    }

    /// Infer a top-level def body, returning its read-back type (the result
    /// type — params are stripped in the resolved HIR). `None` for bodyless
    /// defs (annotation-only / type decls).
    pub fn infer_def(&mut self, body: &Body) -> Option<Ty> {
        let root = body.root?;
        let v = self.infer_expr(body, root);
        self.collapse_let_poly();
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
        Option<Ty>,
        std::collections::HashMap<ExprId, Ty>,
        std::collections::HashMap<LocalId, Ty>,
    ) {
        let Some((v, param_vars)) = self.infer_def_vars(body) else {
            return (None, None, Default::default(), Default::default());
        };
        let result = self.read_back(v);
        // Full inferred signature: fold the (read-back) top-level param types over
        // the result to recover the arrow spine `p0 -> … -> result`. `read_back`
        // only path-compresses the union-find + walks a local `seen` set — it does
        // NOT touch `expr_vars`/`local_vars`, so the per-expr/per-local tables
        // recorded below are byte-identical whether or not this runs. Tooling-only.
        let signature = {
            let mut sig = result.clone();
            for &pv in param_vars.iter().rev() {
                let pt = self.read_back(pv);
                sig = Ty::Fun(Box::new(pt), Box::new(sig));
            }
            sig
        };
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
        (Some(result), Some(signature), exprs, locals)
    }

    /// Infer a body exactly as [`Infer::infer_def_typed`] does, but read back
    /// ONLY the recorded types of the expressions in `exprs` and the locals in
    /// `locals`. Read-back is pure over the solved union-find (it only
    /// path-compresses), so each returned entry is identical to the one
    /// `infer_def_typed` records; the other entries are never built.
    ///
    /// For the call-site harvests, which infer a caller only to read the types
    /// of a few call arguments. Reading back every expression there cost
    /// O(sum of expression type sizes) per caller, which a large generated
    /// module makes quadratic (a `Codec.object` pipeline's step types each
    /// carry the whole N-field constructor).
    pub fn infer_def_selected(
        &mut self,
        body: &Body,
        exprs: &std::collections::HashSet<ExprId>,
        locals: &std::collections::HashSet<LocalId>,
    ) -> (
        std::collections::HashMap<ExprId, Ty>,
        std::collections::HashMap<LocalId, Ty>,
    ) {
        let mut out_e = std::collections::HashMap::new();
        let mut out_l = std::collections::HashMap::new();
        if self.infer_def_vars(body).is_none() {
            return (out_e, out_l);
        }
        let recorded: Vec<(ExprId, TyVarId)> = std::mem::take(&mut self.expr_vars);
        for (e, tv) in recorded {
            if exprs.contains(&e) {
                let t = self.read_back(tv);
                out_e.insert(e, t);
            }
        }
        let recorded_locals: Vec<(LocalId, TyVarId)> = std::mem::take(&mut self.local_vars);
        for (lid, tv) in recorded_locals {
            if locals.contains(&lid) {
                let t = self.read_back(tv);
                out_l.insert(lid, t);
            }
        }
        (out_e, out_l)
    }

    /// The inference half of [`Infer::infer_def_typed`]: type the params, seed
    /// them from the expected scheme (tooling path), infer the body, and unify
    /// the expected result. Returns the body's result var and the param vars;
    /// every expression / local var is recorded for read-back. `None` for a
    /// bodyless def.
    fn infer_def_vars(&mut self, body: &Body) -> Option<(TyVarId, Vec<TyVarId>)> {
        self.record_exprs = true;
        let root = body.root?;
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
        self.collapse_let_poly();
        Some((v, param_vars))
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
        // Peel one arrow per top-level param, seeding EVERY param's declared type
        // (including record-typed params) CLOSED via `ty_to_var` — a param `model
        // : Model` is exactly the closed record the user wrote. Real TEA record
        // threading (updates / subset field access) still resolves: an
        // `Expr::Update` / field `Expr::Access` on a closed record introduces an
        // OPEN row-poly constraint whose extra-field row absorbs into the closed
        // record's own fields, so `{ m | count = .. }` on a closed `Model`
        // unifies without a presence clash. Seeding closed catches the genuine
        // misuses the open seed silently accepted: an update / literal of a
        // NONEXISTENT field, a record passed where a wider closed record is
        // required, and the non-record-vs-record misuse.
        let mut cur = scheme.ty.clone();
        let mut consumed = 0usize;
        for &pv in &param_vars {
            match cur {
                Ty::Fun(a, b) => {
                    let av = self.ty_to_var(&a, &mut sub);
                    self.unify(pv, av);
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
                code: "E2001",
            });
        }
        // (The bespoke record-literal-vs-closed strictness check that used to
        // sit here — RC2 / audit #1,#2 — is now redundant: with the record-param
        // and expected-result seeds both CLOSED (`ty_to_var`) and the extras
        // rules in `unify_records` unconditional, a bare record literal bound to
        // a closed annotation that omits or adds a field is rejected by the
        // general body-vs-annotation unify below — verified by the reject corpus
        // `record_literal_missing_field_direct` / `record_literal_extra_field`.)
        let expected_result = self.ty_to_var(&cur, &mut sub);
        // Body inference stays STRICT (record-presence clashes inside the body —
        // e.g. a call passing a record that lacks a required field — must still
        // reject; that is exactly `record_missing_field` at its call site).
        let errs_before = self.errors.len();
        let v = self.infer_expr(body, root);
        // Body-vs-annotation unification — ONLY when the body itself type-checked
        // cleanly (T2.4). A body that already clashed internally has a poisoned
        // result var; unifying it against the annotation emits a DUPLICATE error
        // that is ALSO span-less (`infer_expr` restored `cur_span` to `None` on
        // exit). The guard removes the cascade; anchoring `cur_span` at the body
        // root gives the primary annotation-mismatch its Elm-style caret + source
        // location instead of a bare header. Record-presence clashes are surfaced
        // by the (now unconditional) extras rules in `unify_records`; real TEA
        // threading still resolves because an `Expr::Update` / field `Expr::Access`
        // introduces an OPEN row-poly constraint that absorbs into the closed
        // record's own fields, while a genuine field-type clash, a
        // non-record-vs-record clash, or a closed-vs-closed field-presence
        // mismatch rejects.
        if self.errors.len() == errs_before {
            let prev = self.cur_span;
            self.cur_span = body.expr_span(root);
            self.unify(v, expected_result);
            self.cur_span = prev;
        }
    }

    /// Infer the FULL scheme of a (typically unannotated) top-level def:
    /// `param0 -> … -> paramN -> result`, generalised over its residual vars.
    /// Used to give unannotated stdlib combinators (`Result.map3`, `List.foldl`,
    /// `List.map`, …) a real polymorphic signature so that applying them at a
    /// call site pins the result type from the argument types (proper HM). The
    /// scheme read-back maps every unbound flex/super var to a *distinct*
    /// quantifier so two independent vars never collapse into one.
    /// `concretize_super`: when true, an unresolved `Number` super reads back as
    /// concrete `Int` (oracle-faithful, Solve.hs:1457) so numeric helpers infer
    /// monomorphic sigs. Passed `true` on the checker's `app_check_sigs` channel
    /// and `false` on `inferred_sigs` (lowerer — must stay byte-identical).
    pub fn infer_def_scheme(&mut self, body: &Body, concretize_super: bool) -> Option<Scheme> {
        let root = body.root?;
        let param_vars: Vec<TyVarId> = body
            .params
            .iter()
            .map(|&p| self.infer_pat_fresh(body, p))
            .collect();
        let rv = self.infer_expr(body, root);
        self.collapse_let_poly();
        let full = param_vars
            .into_iter()
            .rev()
            .fold(rv, |acc, pv| self.fun(pv, acc));
        let ty = self.read_back_scheme(full, concretize_super);
        Some(Scheme::generalize(ty))
    }

    /// D1 (wildcard-`any` result pin): for an ANNOTATED def whose declared result
    /// contains `any` (`f : Int -> any`), infer what the BODY actually returns and
    /// build a check-only sig `<annotation params> -> <body result>`. Params are
    /// SEEDED from the annotation (so the body result resolves concretely — a bare
    /// `f x = x` with `x : Int` returns `Int`, not a fresh var). The annotation's
    /// `any` result is deliberately NOT unified with the body — that lenient valve
    /// is exactly what lets a caller absorb the result at any type. Returns the
    /// pinned scheme ONLY if it is fully MONOMORPHIC: a polymorphic body (e.g.
    /// `kernelAttr k v = Attr.href v : Attribute msg`) is left unpinned, matching
    /// the oracle, which also does not pin a wildcard result whose body is
    /// polymorphic. Populated into `World::any_result_check_sigs` (check-only).
    pub fn infer_any_result_pin(
        &mut self,
        body: &Body,
        anno: &Ty,
        concretize: bool,
    ) -> Option<Scheme> {
        let root = body.root?;
        let param_vars: Vec<TyVarId> = body
            .params
            .iter()
            .map(|&p| self.infer_pat_fresh(body, p))
            .collect();
        // Seed each param from the annotation's arrow spine (closed, via ty_to_var).
        let mut sub: HashMap<String, TyVarId> = HashMap::new();
        let mut cur = anno.clone();
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
        let rv = self.infer_expr(body, root);
        self.collapse_let_poly();
        let full = param_vars
            .into_iter()
            .rev()
            .fold(rv, |acc, pv| self.fun(pv, acc));
        let ty = self.read_back_scheme(full, concretize);
        let scheme = Scheme::generalize(ty);
        if scheme.vars.is_empty() {
            Some(scheme)
        } else {
            None
        }
    }

    fn unify(&mut self, a: TyVarId, b: TyVarId) {
        if let Err(m) = self.uf.unify(a, b) {
            self.errors.push(TypeError {
                message: m.message,
                span: self.cur_span,
                code: "E2001",
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
            // Integer literals are CONCRETE `Int` — Sky fully separates Int and
            // Float (no implicit numeric widening). A bare `1` in a Float slot is
            // a type error; write `1.0`. This is INTENTIONALLY stricter than the
            // Haskell oracle, whose int-literal model is inconsistent (concrete
            // Int for same-module/kernel/binding contexts — it rejects `1 + 2.0`,
            // `v : Float = 1`, `Math.sqrt 4` — but leniently widens an int literal
            // into a CROSS-MODULE external Float param, e.g. `Css.pct 100` where
            // `pct : Float`). That convenience does not scale; Sky separates the
            // two types uniformly. Float literals are already concrete (below).
            Expr::Int(_) => self.con("Int", vec![]),
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
                let constraint = self
                    .uf
                    .fresh(Content::Structure(FlatTy::Record(map, Some(row))));
                self.unify(tb, constraint);
                // D2 (lowering-path only): if the base is an unannotated sibling
                // `Res::Def`, it resolved to a fresh flex above, so `tb`'s row
                // stays open and reads back as the SUBSET of updated fields — but
                // the emitted value is the FULL record → `go build` mismatch. Close
                // the row by unifying `tb` with the base def's full closed-record
                // result. Gated on `use_inferred` so the check path (accept/reject
                // + LSP) is untouched and acceptance stays byte-identical; it only
                // ADDS already-committed field info, so it can't clash.
                if self.use_inferred {
                    match &body.exprs[*base] {
                        Expr::Var(Res::Def(d)) => {
                            if let Some(s) = self.world.record_result_sigs.get(d) {
                                let s = s.clone();
                                let full = self.instantiate(&s);
                                self.unify(tb, full);
                            }
                        }
                        // #166: the base is a PARAM of this def. Close the update's
                        // row with that param's DECLARED record from the def's
                        // signature — the local-param analogue of the `Res::Def`
                        // close above. The lowering path (unlike the check path)
                        // does NOT seed params from the sig, so without this an
                        // annotated `{ model | f = v }` leaves the row open and
                        // reads back as the NARROW subset of updated fields, and
                        // codegen drops every un-updated field (silent for value
                        // fields; a nil-interface `case` panic for an ADT field).
                        // SCOPED to the single updated param + additive (only
                        // unifies already-committed field info), so it does NOT
                        // perturb unrelated inference the way whole-def param
                        // seeding did (that broke a `List (Dict String String)`
                        // field in 12-skyvote/16-skychess). Only a DECLARED record
                        // param closes; a bare type var adds nothing.
                        Expr::Var(Res::Local(id)) => {
                            let id = *id;
                            if let Some(def) = self.self_def {
                                let pidx = body.params.iter().position(
                                    |p| matches!(&body.pats[*p], Pattern::Var(pid) if *pid == id),
                                );
                                if let Some(idx) = pidx {
                                    // The param's full record comes from EITHER the
                                    // declared sig (annotated) OR the concrete
                                    // record its callers pass (unannotated —
                                    // harvested into `callsite_param_records`,
                                    // #166). Both give the full record so the
                                    // update's open row closes to it.
                                    let param_record: Option<Ty> = self
                                        .world
                                        .value_sigs
                                        .get(&def)
                                        .and_then(|scheme| {
                                            let mut cur = &scheme.ty;
                                            for _ in 0..idx {
                                                match cur {
                                                    Ty::Fun(_, b) => cur = b,
                                                    _ => return None,
                                                }
                                            }
                                            match cur {
                                                // ONLY a CLOSED record from the sig closes
                                                // the row. An annotated `model : M` gives
                                                // `Record(_, None)`. An UNANNOTATED param's
                                                // inferred sig is an OPEN row (the narrow
                                                // subset of updated fields) — that must NOT
                                                // win, or it re-narrows the update; fall
                                                // through to the callsite harvest below.
                                                Ty::Fun(a, _)
                                                    if matches!(
                                                        a.as_ref(),
                                                        Ty::Record(_, None)
                                                    ) =>
                                                {
                                                    Some((**a).clone())
                                                }
                                                _ => None,
                                            }
                                        })
                                        .or_else(|| {
                                            self.world
                                                .callsite_param_records
                                                .get(&def)
                                                .and_then(|v| v.get(idx))
                                                .and_then(|o| o.clone())
                                        });
                                    if let Some(rec) = param_record {
                                        let mut sub: HashMap<String, TyVarId> = HashMap::new();
                                        let av = self.ty_to_var(&rec, &mut sub);
                                        self.unify(tb, av);
                                    }
                                }
                            }
                        }
                        _ => {}
                    }
                }
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
                let param_vars: Vec<TyVarId> = params
                    .iter()
                    .map(|&p| self.infer_pat_fresh(body, p))
                    .collect();
                let rb = self.infer_expr(body, *lb);
                param_vars
                    .into_iter()
                    .rev()
                    .fold(rb, |acc, pv| self.fun(pv, acc))
            }
            Expr::Call(callee, args) => {
                let mut tf = self.infer_expr(body, *callee);
                // E2007 arity gate: a NAMED callee applied to MORE args than its
                // resolved (alias-unfolded) arrow count is over-application. Emit
                // a precise arity error + recover, instead of the generic "T vs
                // function" unify clash and its downstream cascade. Over-application
                // is always already a type error, so this only swaps the message on
                // a rejected program — accept-parity is untouched. The unfolded
                // arrow count means a function-RETURNING callee (`withCors o h
                // extra`, whose result is itself applied) is never mis-flagged.
                let callee_name = match &body.exprs[*callee] {
                    Expr::Var(res) => self.callee_name(res),
                    _ => None,
                };
                if let Some(name) = callee_name {
                    let ty = self.read_back(tf);
                    if !matches!(ty, Ty::Var(_) | Ty::Error) {
                        let arity = count_fun_arrows(&ty);
                        if args.len() > arity {
                            for &arg in args {
                                self.infer_expr(body, arg);
                            }
                            self.errors.push(TypeError {
                                message: format!(
                                    "`{name}` is declared as {arity}-arg, called with {} arg(s)",
                                    args.len()
                                ),
                                span: body.expr_span(e),
                                code: "E2007",
                            });
                            return self.uf.fresh_flex();
                        }
                    }
                }
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
                        self.locals
                            .entry(*lid)
                            .or_insert_with(|| self.uf.fresh_flex());
                    }
                }
                // Infer the defs one dependency group (SCC) at a time, callees
                // first, so a group is generalised before the defs that use it
                // are inferred (`y = twice 1` and `z = twice "a"` beside `twice`).
                for group in let_def_groups(body, defs) {
                    let group_defs: Vec<&hir::LocalDef> = group.iter().map(|&i| &defs[i]).collect();
                    self.infer_let_defs(body, &group_defs);
                    self.generalise_let_group(body, defs, &group);
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
                let rec = self
                    .uf
                    .fresh(Content::Structure(FlatTy::Record(map, Some(row))));
                self.fun(rec, fv)
            }
            Expr::Access(base, field) => {
                let tb = self.infer_expr(body, *base);
                let fv = self.uf.fresh_flex();
                let row = self.uf.fresh_flex();
                let mut map = std::collections::BTreeMap::new();
                map.insert(field.clone(), fv);
                let rec = self
                    .uf
                    .fresh(Content::Structure(FlatTy::Record(map, Some(row))));
                self.unify(tb, rec);
                fv
            }
            Expr::Error => self.uf.fresh_flex(),
        }
    }

    /// Infer the defs of ONE let dependency group (see [`let_def_groups`]).
    fn infer_let_defs(&mut self, body: &Body, defs: &[&hir::LocalDef]) {
        for &d in defs {
            // parameters (function let-binding) get fresh vars, then body.
            let param_vars: Vec<TyVarId> = d
                .params
                .iter()
                .map(|&p| self.infer_pat_fresh(body, p))
                .collect();
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
    }

    // ---- let-generalisation (doc 06 §"Let-generalisation") ---------------

    /// Generalise one let dependency group once it is inferred, under the
    /// VALUE RESTRICTION: a binding generalises only when it is a function
    /// (`f x = …`) or its right-hand side is a syntactic value (a lambda, a
    /// literal, a reference, a constructor applied to values, a list / tuple /
    /// record of values). An application (`r = Task.run t`, `d = decode s`)
    /// stays monomorphic, and so does a destructuring binding. See doc 06
    /// §"Let-generalisation" for why Sky keeps the restriction.
    ///
    /// The quantified vars are the unbound flex vars of each binder's type that
    /// are NOT reachable from the enclosing scope or from the let's other
    /// binders — HM's `ftv(τ) \ ftv(Γ)`.
    fn generalise_let_group(&mut self, body: &Body, defs: &[hir::LocalDef], group: &[usize]) {
        let generalisable = |d: &hir::LocalDef| {
            d.pat.is_none()
                && d.binders.len() == 1
                && (!d.params.is_empty() || is_syntactic_value(body, d.body))
        };
        if !group.iter().all(|&i| generalisable(&defs[i])) {
            return;
        }
        let group_binders: Vec<LocalId> = group.iter().map(|&i| defs[i].binders[0].1).collect();
        // Candidate quantifiers first: a binder with no unbound flex var (the
        // common, fully-concrete helper) needs no scope walk at all.
        let mut candidates: Vec<(LocalId, TyVarId, Vec<TyVarId>)> = Vec::new();
        for &b in &group_binders {
            let Some(&v) = self.locals.get(&b) else {
                continue;
            };
            let mut fv = Vec::new();
            let mut seen = std::collections::HashSet::new();
            self.collect_flex(v, &mut fv, &mut seen);
            if !fv.is_empty() {
                candidates.push((b, v, fv));
            }
        }
        if candidates.is_empty() {
            return;
        }
        // Γ: every local typed so far EXCEPT the ones bound inside this group
        // (its binders, their parameters, and every lambda / case / let binder
        // in their bodies). That is the enclosing scope — including a scope
        // local first typed while inferring this group, as an outer parameter
        // is on the check path, which types parameters lazily at their first
        // reference — plus the let's other binders. Locals of sibling scopes
        // typed earlier are included too, which is conservative, never unsound.
        let mut bound_inside: std::collections::HashSet<LocalId> =
            group_binders.iter().copied().collect();
        for &i in group {
            let d = &defs[i];
            for &p in &d.params {
                collect_pat_binders(body, p, &mut bound_inside);
            }
            collect_expr_binders(body, d.body, &mut bound_inside);
        }
        let mut env_seen = std::collections::HashSet::new();
        let mut env_fv = Vec::new();
        let env_locals: Vec<LocalId> = self
            .locals
            .keys()
            .copied()
            .filter(|l| !bound_inside.contains(l))
            .collect();
        for l in env_locals {
            if let Some(&v) = self.locals.get(&l) {
                self.collect_flex(v, &mut env_fv, &mut env_seen);
            }
        }
        let env: std::collections::HashSet<TyVarId> = env_fv.into_iter().collect();
        for (b, v, fv) in candidates {
            let quantified: Vec<TyVarId> = fv.into_iter().filter(|r| !env.contains(r)).collect();
            if !quantified.is_empty() {
                self.poly_locals.insert(b, (v, quantified));
            }
        }
    }

    /// The unbound (`Flex` / `FlexSuper`) roots reachable from `v`, in first-
    /// visit order. Rigid annotation vars never generalise here: they belong to
    /// the enclosing def's signature.
    fn collect_flex(
        &mut self,
        v: TyVarId,
        out: &mut Vec<TyVarId>,
        seen: &mut std::collections::HashSet<TyVarId>,
    ) {
        let r = self.uf.find(v);
        if !seen.insert(r) {
            return;
        }
        match self.uf.content(r) {
            Content::Flex | Content::FlexSuper(_) => out.push(r),
            Content::Structure(ft) => {
                for k in crate::unify::flat_children(&ft) {
                    self.collect_flex(k, out, seen);
                }
            }
            Content::Rigid(_) | Content::Error => {}
        }
    }

    /// A fresh instance of a generalised let binder: copy its type graph,
    /// replacing each quantified root by a fresh var (keeping a super-var's
    /// constraint) and sharing everything else.
    fn instantiate_local(&mut self, generic: TyVarId, quantified: &[TyVarId]) -> TyVarId {
        let mut memo: HashMap<TyVarId, TyVarId> = HashMap::new();
        for &q in quantified {
            let r = self.uf.find(q);
            let fresh = match self.uf.content(r) {
                Content::FlexSuper(s) => self.uf.fresh(Content::FlexSuper(s)),
                _ => self.uf.fresh_flex(),
            };
            memo.insert(r, fresh);
        }
        self.copy_generic(generic, &mut memo)
    }

    fn copy_generic(&mut self, v: TyVarId, memo: &mut HashMap<TyVarId, TyVarId>) -> TyVarId {
        let r = self.uf.find(v);
        if let Some(&c) = memo.get(&r) {
            return c;
        }
        let out = match self.uf.content(r) {
            Content::Structure(ft) => {
                let copied = match &ft {
                    FlatTy::App(n, args) => {
                        let a: Vec<TyVarId> =
                            args.iter().map(|&x| self.copy_generic(x, memo)).collect();
                        FlatTy::App(n.clone(), a)
                    }
                    FlatTy::Fun(a, b) => {
                        let (a, b) = (*a, *b);
                        FlatTy::Fun(self.copy_generic(a, memo), self.copy_generic(b, memo))
                    }
                    FlatTy::Record(fs, ext) => {
                        let mut m = std::collections::BTreeMap::new();
                        for (n, &t) in fs {
                            m.insert(n.clone(), self.copy_generic(t, memo));
                        }
                        let e = ext.map(|e| self.copy_generic(e, memo));
                        FlatTy::Record(m, e)
                    }
                    FlatTy::Tuple(xs) => {
                        FlatTy::Tuple(xs.iter().map(|&x| self.copy_generic(x, memo)).collect())
                    }
                    FlatTy::Unit => FlatTy::Unit,
                };
                // Share a sub-graph that holds no quantified var.
                let old = crate::unify::flat_children(&ft);
                let new = crate::unify::flat_children(&copied);
                let unchanged = old.len() == new.len()
                    && old
                        .iter()
                        .zip(&new)
                        .all(|(&a, &b)| self.uf.find(a) == self.uf.find(b));
                if unchanged {
                    r
                } else {
                    self.uf.fresh(Content::Structure(copied))
                }
            }
            _ => r,
        };
        memo.insert(r, out);
        out
    }

    /// End-of-def step for the lowering table: a generalised let binder whose
    /// every instance solved to the SAME type is used monomorphically, so bind
    /// its quantifiers to that type. The binder (and every expression inside
    /// it) then reads back concrete, and the lowerer emits the typed closure it
    /// always emitted for a helper used at one type. A binder used at two
    /// different types stays generic and lowers erased, like a polymorphic
    /// top-level def (doc 07 §5.1: one emit per definition, no specialisation).
    ///
    /// Sound by construction: identical instances mean the unification binds
    /// only the quantified vars, which nothing outside the binder mentions, so
    /// no type in the enclosing scope changes. Repeated to a fixpoint, because
    /// collapsing an outer binder can make an inner binder's instances equal.
    fn collapse_let_poly(&mut self) {
        loop {
            let mut progressed = false;
            let mut by_local: Vec<(LocalId, Vec<TyVarId>)> = Vec::new();
            for &(lid, inst) in &self.poly_insts {
                match by_local.iter_mut().find(|(l, _)| *l == lid) {
                    Some((_, v)) => v.push(inst),
                    None => by_local.push((lid, vec![inst])),
                }
            }
            for (lid, insts) in by_local {
                let Some((generic, _)) = self.poly_locals.get(&lid).cloned() else {
                    continue;
                };
                if self.uf.find(generic) == self.uf.find(insts[0]) {
                    continue;
                }
                let first = self.read_back_scheme(insts[0], false);
                let same = insts[1..]
                    .iter()
                    .all(|&i| self.read_back_scheme(i, false) == first);
                if !same {
                    continue;
                }
                let snapshot = self.uf.clone();
                if self.uf.unify(generic, insts[0]).is_ok() {
                    progressed = true;
                } else {
                    self.uf = snapshot;
                }
            }
            if !progressed {
                break;
            }
        }
    }

    /// A displayable name for a callee reference — `Module.func` for a kernel,
    /// the def's own name for a user def. `None` for anything unnameable (locals,
    /// foreign, errors), which suppresses the arity gate (no name to blame).
    fn callee_name(&self, res: &Res) -> Option<String> {
        match res {
            Res::Kernel { module, func } => Some(format!("{}.{}", module.as_str(), func.as_str())),
            Res::Def(d) => self.db.def_loc(*d).map(|l| l.name.as_str().to_string()),
            _ => None,
        }
    }

    fn infer_res(&mut self, res: Res) -> TyVarId {
        match res {
            Res::Local(id) => {
                // A generalised let-bound function: a fresh instance per use.
                if let Some((generic, quantified)) = self.poly_locals.get(&id).cloned() {
                    let inst = self.instantiate_local(generic, &quantified);
                    self.poly_insts.push((id, inst));
                    return inst;
                }
                *self
                    .locals
                    .entry(id)
                    .or_insert_with(|| self.uf.fresh_flex())
            }
            Res::Def(def) => {
                // D1 (wildcard-`any` result pin) — CHECK-ONLY. When a def's
                // declared result contains `any` and its body returns a concrete
                // monomorphic type, use that pinned type at call sites so misuse
                // of the result is caught (`f : Int -> any; f x = x` →
                // `List.length (f 5)` rejects). Consulted only on the check path
                // (`!use_inferred`) so the lowerer still sees the annotation's
                // `Int -> any` and Go emission is byte-identical. Skipped for the
                // def's own body (`self_def`) to keep monomorphic-recursion parity.
                if !self.use_inferred && self.self_def != Some(def) {
                    if let Some(s) = self.world.any_result_check_sigs.get(&def) {
                        let s = s.clone();
                        return self.instantiate(&s);
                    }
                }
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
                    // CHECK-ONLY precise scheme for an unannotated APP-module def
                    // used cross-module (F1c narrow subset). Same `!use_inferred`
                    // gate + isolation as `check_sigs`; the map is strictly
                    // filtered at populate time (`World::infer_app_check_sigs`)
                    // to fully-monomorphic, record-free, Unit-spine-free types,
                    // so pinning e.g. `allCategories : List String` here lets the
                    // checker reject `allCategories + 1` without perturbing any
                    // accept-parity case. Empty on the lowerer path → no-op there.
                    if let Some(s) = self.world.app_check_sigs.get(&def) {
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
                let name = self.db.def_loc(cr.def).map(|l| l.name.as_str().to_string());
                match name.and_then(|n| self.world.ctors.get(&n).cloned()) {
                    Some(s) => self.instantiate(&s),
                    None => self.uf.fresh_flex(),
                }
            }
            Res::Foreign { package, name } => self.foreign_ref(&package, &name),
            Res::Error => self.uf.fresh_flex(),
        }
    }

    /// Type a Go-FFI reference from its pinned signature (see the module doc's
    /// leniency contract). A leading `()` parameter relaxes exactly as a
    /// kernel's does (`relax_unit_arg_spine`), so `Uuid.newString ()` and a
    /// real-value unit slot both check.
    fn foreign_ref(&mut self, package: &Name, name: &Name) -> TyVarId {
        let key = (package.clone(), name.clone());
        if let Some(s) = self.ffi_schemes.get(&key) {
            let s = s.clone();
            return self.instantiate(&s);
        }
        let Some(sig) = self.db.ffi_fn(package.as_str(), name.as_str()) else {
            return self.uf.fresh_flex();
        };
        let scheme =
            relax_unit_arg_spine(&crate::ffi_sig::scheme_for(&sig.sky_type, sig.arity).scheme);
        self.ffi_schemes.insert(key, scheme.clone());
        self.instantiate(&scheme)
    }

    fn infer_binop(&mut self, op: &str, tl: TyVarId, tr: TyVarId) -> TyVarId {
        match op {
            // arithmetic `+ - *`: fully polymorphic `a -> a -> a`, matching the
            // oracle (which accepts `add "s" "t"`, `add True False` — its `+/-/*`
            // are unconstrained). Result = the (unified) operand type. A concrete
            // misuse still clashes at unify (`add "s" 1` → String vs Int). Poly
            // helpers whose operands stay `any` lower via rt.Add/Sub/Mul
            // (lower.rs `both_prim` gate), so an unannotated `add a b = a + b`
            // builds and runs at Int AND Float — closing the false-reject class
            // (`add 1.0 2.0`) the old `FlexSuper(Number)`+concretize model caused.
            "+" | "-" | "*" => {
                self.unify(tl, tr);
                tl
            }
            // division is Float-only (`Float -> Float -> Float`): the oracle
            // rejects integer division via `/` (use `//`). Concrete-Int literals
            // then make `5 / 2` clash (Int vs Float) with oracle parity, while
            // `5.0 / 2.0` and `divh x y = x / y ; divh 1.0 2.0` type as Float.
            "/" => {
                let f = self.con("Float", vec![]);
                self.unify(tl, f);
                self.unify(tr, f);
                self.con("Float", vec![])
            }
            // power `^` kept on the Number super (UNCHANGED) — out of scope for the
            // literal-model fix. Its codegen (`rt.Pow`, lower.rs) is a separate
            // pending gap: `rt.Pow` is undefined in the runtime, so `^` fails
            // `go build` regardless of inference (pre-existing; tracked separately).
            "^" => {
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
                let (a, b, c) = (
                    self.uf.fresh_flex(),
                    self.uf.fresh_flex(),
                    self.uf.fresh_flex(),
                );
                let ab = self.fun(a, b);
                let bc = self.fun(b, c);
                self.unify(tl, ab);
                self.unify(tr, bc);
                self.fun(a, c)
            }
            "<<" => {
                let (a, b, c) = (
                    self.uf.fresh_flex(),
                    self.uf.fresh_flex(),
                    self.uf.fresh_flex(),
                );
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
                self.locals.insert(id, expected);
                if self.record_exprs {
                    self.local_vars.push((id, expected));
                }
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
                // Concrete `Int` — see `Expr::Int` (Sky separates Int/Float).
                let n = self.con("Int", vec![]);
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
                    self.locals.insert(*id, fv);
                    if self.record_exprs {
                        self.local_vars.push((*id, fv));
                    }
                    map.insert(n.clone(), fv);
                }
                let row = self.uf.fresh_flex();
                let rec = self
                    .uf
                    .fresh(Content::Structure(FlatTy::Record(map, Some(row))));
                self.unify(expected, rec);
            }
            Pattern::Alias(inner, id) => {
                let inner = *inner;
                let id = *id;
                self.locals.insert(id, expected);
                if self.record_exprs {
                    self.local_vars.push((id, expected));
                }
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
        self.read_back_seen(v, &mut seen, false, false)
    }

    /// Read-back for scheme generation: every unbound flex/super var becomes a
    /// *distinct* quantifier `t<repr>` (super-constraints are dropped — lenient,
    /// accept-more). This avoids (a) `Number → App("number")` unifying wrongly
    /// against `Int`/`Float`, and (b) two independent `comparable`/`appendable`
    /// vars collapsing onto one shared name and over-constraining the scheme.
    ///
    /// `concretize_super` opts into the ORACLE's read-back semantics for an
    /// unresolved `Number` super: it DEFAULTS TO CONCRETE `Int`
    /// (`Sky/Type/Solve.hs:1457`) rather than dropping to a fresh quantifier.
    /// This is used ONLY on the checker's `app_check_sigs` channel so an
    /// unannotated numeric helper (`mkBadge n = { lvl = n + 1 }`) infers a
    /// MONOMORPHIC `Int -> {…}` and no longer escapes the F1c monomorphism
    /// filter — matching the oracle, which rejects `mkBadge "s"`. The
    /// `inferred_sigs` channel (consumed by the lowerer) passes `false` so
    /// emitted Go stays byte-identical. Only `Number` has an unambiguous
    /// single concrete default; `Comparable`/`Appendable`/`CompAppend` keep the
    /// drop-to-quantifier behaviour even under `concretize_super`.
    fn read_back_scheme(&mut self, v: TyVarId, concretize_super: bool) -> Ty {
        let mut seen = std::collections::HashSet::new();
        self.read_back_seen(v, &mut seen, true, concretize_super)
    }

    fn read_back_seen(
        &mut self,
        v: TyVarId,
        seen: &mut std::collections::HashSet<TyVarId>,
        scheme: bool,
        concretize_super: bool,
    ) -> Ty {
        let r = self.uf.find(v);
        if !seen.insert(r) {
            return Ty::Error; // cycle guard (anyEquivSeen, Solve.hs:1449)
        }
        let super_var = |r: TyVarId| Ty::Var(Name::new(&format!("t{}", r.0)));
        let out = match self.uf.content(r) {
            Content::Flex => Ty::Var(Name::new(&format!("t{}", r.0))),
            Content::Rigid(n) => Ty::Var(n),
            // Oracle-faithful concrete default: unresolved `Number` super reads
            // back as concrete `Int` (Solve.hs:1457) on the concretize channel.
            // `Int` (not `App("number")`) so `super_matches(Number, Int)` still
            // admits valid `Int` uses at call sites.
            Content::FlexSuper(SuperType::Number) if scheme && concretize_super => {
                Ty::app("Int", vec![])
            }
            Content::FlexSuper(_) if scheme => super_var(r),
            Content::FlexSuper(SuperType::Number) => Ty::app("number", vec![]),
            Content::FlexSuper(SuperType::Comparable) => Ty::var("comparable"),
            Content::FlexSuper(SuperType::Appendable) => Ty::var("appendable"),
            Content::FlexSuper(SuperType::CompAppend) => Ty::var("compappend"),
            Content::Error => Ty::Error,
            Content::Structure(ft) => match ft {
                FlatTy::App(name, args) => Ty::App(
                    name,
                    args.into_iter()
                        .map(|a| self.read_back_seen(a, seen, scheme, concretize_super))
                        .collect(),
                ),
                FlatTy::Fun(a, b) => Ty::Fun(
                    Box::new(self.read_back_seen(a, seen, scheme, concretize_super)),
                    Box::new(self.read_back_seen(b, seen, scheme, concretize_super)),
                ),
                FlatTy::Tuple(xs) => Ty::Tuple(
                    xs.into_iter()
                        .map(|x| self.read_back_seen(x, seen, scheme, concretize_super))
                        .collect(),
                ),
                FlatTy::Unit => Ty::Unit,
                FlatTy::Record(fs, ext) => {
                    // Flatten any resolved-Record extension before reading back.
                    // A generalised row-poly result (`bump {name, age}` yields
                    // `{age | {name}}` where the ext var has bound to
                    // `Record({name})`) must read back as the closed `{age,
                    // name}` — dropping the ext (the pre-fix behaviour) silently
                    // lost every row-carried field, closing the scheme to its
                    // literal fields only. See `UnionFind::normalize_record`.
                    let (fs, ext) = self.uf.normalize_record(fs, ext);
                    let mut fields: Vec<(Name, Ty)> = fs
                        .into_iter()
                        .map(|(n, t)| (n, self.read_back_seen(t, seen, scheme, concretize_super)))
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

/// Every local a pattern binds.
fn collect_pat_binders(body: &Body, p: PatId, out: &mut std::collections::HashSet<LocalId>) {
    match &body.pats[p] {
        Pattern::Var(l) => {
            out.insert(*l);
        }
        Pattern::Alias(inner, l) => {
            out.insert(*l);
            collect_pat_binders(body, *inner, out);
        }
        Pattern::Record(fs) => {
            for (_, l) in fs {
                out.insert(*l);
            }
        }
        Pattern::Tuple(ps) | Pattern::List(ps) => {
            for &q in ps {
                collect_pat_binders(body, q, out);
            }
        }
        Pattern::Cons(h, t) => {
            collect_pat_binders(body, *h, out);
            collect_pat_binders(body, *t, out);
        }
        Pattern::Ctor { args, .. } => {
            for &q in args {
                collect_pat_binders(body, q, out);
            }
        }
        Pattern::Anything
        | Pattern::Unit
        | Pattern::Bool(_)
        | Pattern::Chr(_)
        | Pattern::Str(_)
        | Pattern::Int(_)
        | Pattern::Float(_)
        | Pattern::Error => {}
    }
}

/// Every local bound INSIDE expression `e`: lambda parameters, case-branch
/// patterns, and nested let binders, parameters and destructuring patterns.
fn collect_expr_binders(body: &Body, e: ExprId, out: &mut std::collections::HashSet<LocalId>) {
    match &body.exprs[e] {
        Expr::Var(_)
        | Expr::Int(_)
        | Expr::Float(_)
        | Expr::Str(_)
        | Expr::Chr(_)
        | Expr::Bool(_)
        | Expr::Unit
        | Expr::Accessor(_)
        | Expr::Error => {}
        Expr::List(xs) | Expr::Tuple(xs) => {
            for &x in xs {
                collect_expr_binders(body, x, out);
            }
        }
        Expr::Record(fs) => {
            for (_, x) in fs {
                collect_expr_binders(body, *x, out);
            }
        }
        Expr::Update { base, fields } => {
            collect_expr_binders(body, *base, out);
            for (_, x) in fields {
                collect_expr_binders(body, *x, out);
            }
        }
        Expr::Negate(x) | Expr::Access(x, _) => collect_expr_binders(body, *x, out),
        Expr::Lambda { params, body: b } => {
            for &p in params {
                collect_pat_binders(body, p, out);
            }
            collect_expr_binders(body, *b, out);
        }
        Expr::Call(f, args) => {
            collect_expr_binders(body, *f, out);
            for &a in args {
                collect_expr_binders(body, a, out);
            }
        }
        Expr::Binop { lhs, rhs, .. } => {
            collect_expr_binders(body, *lhs, out);
            collect_expr_binders(body, *rhs, out);
        }
        Expr::If { arms, els } => {
            for (c, t) in arms {
                collect_expr_binders(body, *c, out);
                collect_expr_binders(body, *t, out);
            }
            collect_expr_binders(body, *els, out);
        }
        Expr::Let { defs, body: b } => {
            for d in defs {
                for (_, l) in &d.binders {
                    out.insert(*l);
                }
                if let Some(p) = d.pat {
                    collect_pat_binders(body, p, out);
                }
                for &p in &d.params {
                    collect_pat_binders(body, p, out);
                }
                collect_expr_binders(body, d.body, out);
            }
            collect_expr_binders(body, *b, out);
        }
        Expr::Case { subject, branches } => {
            collect_expr_binders(body, *subject, out);
            for br in branches {
                collect_pat_binders(body, br.pat, out);
                collect_expr_binders(body, br.body, out);
            }
        }
    }
}

/// ML's syntactic-value test (the value restriction): an expression whose
/// evaluation cannot run a computation. Only such a let binding generalises.
fn is_syntactic_value(body: &Body, e: ExprId) -> bool {
    match &body.exprs[e] {
        Expr::Lambda { .. }
        | Expr::Int(_)
        | Expr::Float(_)
        | Expr::Str(_)
        | Expr::Chr(_)
        | Expr::Bool(_)
        | Expr::Unit
        | Expr::Accessor(_)
        | Expr::Var(_) => true,
        Expr::Negate(x) => is_syntactic_value(body, *x),
        Expr::List(xs) | Expr::Tuple(xs) => xs.iter().all(|&x| is_syntactic_value(body, x)),
        Expr::Record(fs) => fs.iter().all(|(_, x)| is_syntactic_value(body, *x)),
        // A constructor applied to values builds data; it runs nothing.
        Expr::Call(f, args) => {
            matches!(body.exprs[*f], Expr::Var(Res::Ctor(_)))
                && args.iter().all(|&a| is_syntactic_value(body, a))
        }
        _ => false,
    }
}

/// The defs of one `let`, grouped into strongly connected components of their
/// references to each other and ordered callees-first (Tarjan). Each inner vec
/// keeps source order. A def that references no sibling is its own group.
fn let_def_groups(body: &Body, defs: &[hir::LocalDef]) -> Vec<Vec<usize>> {
    let owner: HashMap<LocalId, usize> = defs
        .iter()
        .enumerate()
        .flat_map(|(i, d)| d.binders.iter().map(move |(_, l)| (*l, i)))
        .collect();
    let edges: Vec<Vec<usize>> = defs
        .iter()
        .map(|d| {
            let mut refs = Vec::new();
            collect_local_refs(body, d.body, &mut refs);
            let mut out: Vec<usize> = refs.iter().filter_map(|l| owner.get(l).copied()).collect();
            out.sort_unstable();
            out.dedup();
            out
        })
        .collect();
    struct Tarjan<'e> {
        edges: &'e [Vec<usize>],
        index: Vec<Option<usize>>,
        low: Vec<usize>,
        on_stack: Vec<bool>,
        stack: Vec<usize>,
        next: usize,
        out: Vec<Vec<usize>>,
    }
    impl Tarjan<'_> {
        fn visit(&mut self, v: usize) {
            self.index[v] = Some(self.next);
            self.low[v] = self.next;
            self.next += 1;
            self.stack.push(v);
            self.on_stack[v] = true;
            for k in 0..self.edges[v].len() {
                let w = self.edges[v][k];
                match self.index[w] {
                    None => {
                        self.visit(w);
                        self.low[v] = self.low[v].min(self.low[w]);
                    }
                    Some(iw) if self.on_stack[w] => self.low[v] = self.low[v].min(iw),
                    Some(_) => {}
                }
            }
            if Some(self.low[v]) == self.index[v] {
                let mut group = Vec::new();
                while let Some(w) = self.stack.pop() {
                    self.on_stack[w] = false;
                    group.push(w);
                    if w == v {
                        break;
                    }
                }
                group.sort_unstable();
                self.out.push(group);
            }
        }
    }
    let n = defs.len();
    let mut t = Tarjan {
        edges: &edges,
        index: vec![None; n],
        low: vec![0; n],
        on_stack: vec![false; n],
        stack: Vec::new(),
        next: 0,
        out: Vec::new(),
    };
    for v in 0..n {
        if t.index[v].is_none() {
            t.visit(v);
        }
    }
    t.out
}

/// Every `Res::Local` referenced in expression `e` (including nested lets,
/// lambdas and case branches).
fn collect_local_refs(body: &Body, e: ExprId, out: &mut Vec<LocalId>) {
    match &body.exprs[e] {
        Expr::Var(Res::Local(l)) => out.push(*l),
        Expr::Var(_)
        | Expr::Int(_)
        | Expr::Float(_)
        | Expr::Str(_)
        | Expr::Chr(_)
        | Expr::Bool(_)
        | Expr::Unit
        | Expr::Accessor(_)
        | Expr::Error => {}
        Expr::List(xs) | Expr::Tuple(xs) => {
            for &x in xs {
                collect_local_refs(body, x, out);
            }
        }
        Expr::Record(fs) => {
            for (_, x) in fs {
                collect_local_refs(body, *x, out);
            }
        }
        Expr::Update { base, fields } => {
            collect_local_refs(body, *base, out);
            for (_, x) in fields {
                collect_local_refs(body, *x, out);
            }
        }
        Expr::Negate(x) | Expr::Access(x, _) => collect_local_refs(body, *x, out),
        Expr::Lambda { body: b, .. } => collect_local_refs(body, *b, out),
        Expr::Call(f, args) => {
            collect_local_refs(body, *f, out);
            for &a in args {
                collect_local_refs(body, a, out);
            }
        }
        Expr::Binop { lhs, rhs, .. } => {
            collect_local_refs(body, *lhs, out);
            collect_local_refs(body, *rhs, out);
        }
        Expr::If { arms, els } => {
            for (c, t) in arms {
                collect_local_refs(body, *c, out);
                collect_local_refs(body, *t, out);
            }
            collect_local_refs(body, *els, out);
        }
        Expr::Let { defs, body: b } => {
            for d in defs {
                collect_local_refs(body, d.body, out);
            }
            collect_local_refs(body, *b, out);
        }
        Expr::Case { subject, branches } => {
            collect_local_refs(body, *subject, out);
            for br in branches {
                collect_local_refs(body, br.body, out);
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use hir::SourceDb;
    use std::collections::HashSet;

    const SRC: &str = "module Main exposing (main)\n\
        type alias P = { name : String, tags : List String, inner : { a : String, b : ( String, String ) } }\n\
        type Msg = Go P | Stop\n\
        mk n = { name = n, tags = [ n, n ], inner = { a = n, b = ( n, n ) } }\n\
        rename p n = { p | name = n }\n\
        pick m = case m of\n\
        \x20   Go p -> p.inner.b\n\
        \x20   Stop -> ( \"x\", \"y\" )\n\
        main =\n\
        \x20   let\n\
        \x20       p = mk \"a\"\n\
        \x20       q = rename p \"b\"\n\
        \x20       f = \\x -> ( x, q.tags )\n\
        \x20   in\n\
        \x20   ( pick (Go q), f p.name, [ mk \"c\", q ] )\n";

    /// `infer_def_selected` returns exactly the entries `infer_def_typed`
    /// records, for the full key set and for a sparse subset.
    #[test]
    fn selected_readback_matches_full_readback() {
        let mut db = SourceDb::new();
        let m = db.add_module("Main", syntax::parse(SRC, base::FileId(0)));
        let world = World::build(&db);
        let resolved = db.resolve(m);
        assert!(resolved.bodies.len() >= 4, "fixture defs resolved");
        for (def, body) in resolved.bodies.iter() {
            let (_, _, all_e, all_l) = Infer::new(&world, &db)
                .with_self_def(Some(*def))
                .with_inferred(true)
                .infer_def_typed(body);
            let keys_e: HashSet<ExprId> = all_e.keys().copied().collect();
            let keys_l: HashSet<LocalId> = all_l.keys().copied().collect();
            let (sel_e, sel_l) = Infer::new(&world, &db)
                .with_self_def(Some(*def))
                .with_inferred(true)
                .infer_def_selected(body, &keys_e, &keys_l);
            assert_eq!(sel_e, all_e);
            assert_eq!(sel_l, all_l);

            // Every other expression / local only.
            let half_e: HashSet<ExprId> = keys_e.iter().copied().step_by(2).collect();
            let half_l: HashSet<LocalId> = keys_l.iter().copied().step_by(2).collect();
            let (sub_e, sub_l) = Infer::new(&world, &db)
                .with_self_def(Some(*def))
                .with_inferred(true)
                .infer_def_selected(body, &half_e, &half_l);
            let want_e: HashMap<ExprId, Ty> = all_e
                .iter()
                .filter(|(k, _)| half_e.contains(k))
                .map(|(k, v)| (*k, v.clone()))
                .collect();
            let want_l: HashMap<LocalId, Ty> = all_l
                .iter()
                .filter(|(k, _)| half_l.contains(k))
                .map(|(k, v)| (*k, v.clone()))
                .collect();
            assert_eq!(sub_e, want_e);
            assert_eq!(sub_l, want_l);
        }
    }
}

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
use base::Name;
use hir::{Body, Expr, ExprId, LocalId, PatId, Pattern, Res, SourceDb};
use std::collections::HashMap;

/// A recorded type error (an unify clash). A value, not an exception (L7).
pub struct TypeError {
    pub message: String,
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
    db: &'a SourceDb,
    pub uf: UnionFind,
    locals: HashMap<LocalId, TyVarId>,
    pub errors: Vec<TypeError>,
}

impl<'a> Infer<'a> {
    pub fn new(world: &'a World, db: &'a SourceDb) -> Self {
        Infer {
            world,
            db,
            uf: UnionFind::new(),
            locals: HashMap::new(),
            errors: Vec::new(),
        }
    }

    /// Infer a top-level def body, returning its read-back type (the result
    /// type — params are stripped in the resolved HIR). `None` for bodyless
    /// defs (annotation-only / type decls).
    pub fn infer_def(&mut self, body: &Body) -> Option<Ty> {
        let root = body.root?;
        let v = self.infer_expr(body, root);
        Some(self.read_back(v))
    }

    fn unify(&mut self, a: TyVarId, b: TyVarId) {
        if let Err(m) = self.uf.unify(a, b) {
            self.errors.push(TypeError { message: m.message });
        }
    }

    // ---- expressions ----------------------------------------------------

    fn infer_expr(&mut self, body: &Body, e: ExprId) -> TyVarId {
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
            Res::Def(def) => match self.world.value_sigs.get(&def) {
                Some(s) => {
                    let s = s.clone();
                    self.instantiate(&s)
                }
                None => self.uf.fresh_flex(),
            },
            Res::Kernel { module, func } => {
                let key = (module.as_str().to_string(), func.as_str().to_string());
                match self.world.kernel_sigs.get(&key) {
                    Some(s) => {
                        // Zero-arg kernel-shim class (Limitation #7 family:
                        // `loadEnv`/`uuidV4`/`timeNow`/`Pure.*`): a kernel
                        // `() -> X` accepts a call with or without the unit, so
                        // relax leading `Unit` params to flex. Narrow — only
                        // affects Unit-first-param kernel sigs.
                        let s = relax_unit_arg_spine(s);
                        self.instantiate(&s)
                    }
                    None => self.uf.fresh_flex(),
                }
            }
            Res::Ctor(cr) => {
                // Disambiguate same-named ctors by DefId first, then by name
                // (builtins Just/Ok/… live in the by-name table only).
                if let Some(s) = self.world.ctors_by_def.get(&cr.def).cloned() {
                    return self.instantiate(&s);
                }
                let name = self
                    .db
                    .defs()
                    .borrow()
                    .loc(cr.def)
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
                self.locals.insert(id, expected);
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
                    self.locals.insert(*id, fv);
                    map.insert(n.clone(), fv);
                }
                let row = self.uf.fresh_flex();
                let rec = self.uf.fresh(Content::Structure(FlatTy::Record(map, Some(row))));
                self.unify(expected, rec);
            }
            Pattern::Alias(inner, id) => {
                let inner = *inner;
                let id = *id;
                self.locals.insert(id, expected);
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
        self.read_back_seen(v, &mut seen)
    }

    fn read_back_seen(&mut self, v: TyVarId, seen: &mut std::collections::HashSet<TyVarId>) -> Ty {
        let r = self.uf.find(v);
        if !seen.insert(r) {
            return Ty::Error; // cycle guard (anyEquivSeen, Solve.hs:1449)
        }
        let out = match self.uf.content(r) {
            Content::Flex => Ty::Var(Name::new(&format!("t{}", r.0))),
            Content::FlexSuper(SuperType::Number) => Ty::app("number", vec![]),
            Content::FlexSuper(SuperType::Comparable) => Ty::var("comparable"),
            Content::FlexSuper(SuperType::Appendable) => Ty::var("appendable"),
            Content::FlexSuper(SuperType::CompAppend) => Ty::var("compappend"),
            Content::Error => Ty::Error,
            Content::Structure(ft) => match ft {
                FlatTy::App(name, args) => Ty::App(
                    name,
                    args.into_iter().map(|a| self.read_back_seen(a, seen)).collect(),
                ),
                FlatTy::Fun(a, b) => Ty::Fun(
                    Box::new(self.read_back_seen(a, seen)),
                    Box::new(self.read_back_seen(b, seen)),
                ),
                FlatTy::Tuple(xs) => {
                    Ty::Tuple(xs.into_iter().map(|x| self.read_back_seen(x, seen)).collect())
                }
                FlatTy::Unit => Ty::Unit,
                FlatTy::Record(fs, ext) => {
                    let mut fields: Vec<(Name, Ty)> = fs
                        .into_iter()
                        .map(|(n, t)| (n, self.read_back_seen(t, seen)))
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

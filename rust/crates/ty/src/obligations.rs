//! Qualified type bounds (v0.27.0, C-11 and B-1): deciding the bound
//! obligations the unifier defers, and the nominal rules behind them.
//!
//! # What a bound is
//!
//! A type variable can carry a bound ([`SuperType`]): `Number`, `Comparable`,
//! `Appendable` (Elm's super-vars) and `Encodable` (Sky, v0.27.0). The unifier
//! decides a bound against every STRUCTURAL shape itself (a function has no
//! order, a list is comparable when its elements are; `UnionFind::satisfy`).
//! Two cases need more than the union-find:
//!
//! * **A nominal type** other than the builtins. Whether `Color` is comparable
//!   depends on its constructors, which live in the nominal environment
//!   ([`World`]); whether `Secret` encodes is a property of the stdlib. The
//!   unifier records `(bound, var)` in `UnionFind::pending` and this module
//!   decides it ([`classify`]).
//! * **A record.** Its fields are constrained at once, but whether its row is
//!   CLOSED is known only after solving. An ordering of an open record is
//!   refused (the runtime would compare two values of different field sets);
//!   an open record that must encode passes the bound to its row variable.
//!
//! # The rules
//!
//! * **Comparable**: `Int`, `Float`, `String`, `Char`, `Bool`, `()`, and
//!   `List`/`Maybe`/`Result`/tuples of comparables (decided in `unify`); a
//!   closed record of comparables; a custom type whose every constructor holds
//!   only comparables. NOT: a function, `Dict`, `Set`, an opaque stdlib type
//!   (hidden constructor: `Secret`, `Decimal`, `Table`, …), a runtime handle, a
//!   kernel-implicit type with no constructors (`Task`, `Cmd`, `Html`, a Go
//!   type). The runtime orders a custom type by constructor declaration
//!   order, then payload (S1's `cmpSafe`).
//! * **Encodable** (what `Codec.auto` round-trips): everything comparable, plus
//!   `Dict`/`Set` of encodables, any record of encodables, and a custom type
//!   holding only encodables. NOT: a function, `Secret`, a crypto key or
//!   protocol state, a runtime handle, a kernel type that holds closures
//!   (`Task`, `Cmd`, `Sub`, `Html`, …), an opaque stdlib type other than
//!   `Decimal` (which the runtime encodes as its canonical string).
//!
//! # How a bound travels
//!
//! A bounded variable that is generalised reads back as `<label><id>`
//! (`comparable12`), and instantiation maps a label-prefixed name back to a
//! bounded variable, so the bound reaches every caller of an unannotated
//! helper. An ANNOTATED helper whose plain variable the body bounds is Elm's
//! "too general" error for `Comparable`/`Number`/`Appendable`; for `Encodable`
//! the bound is inferred and added to the scheme callers see
//! (`World::infer_annotated_bounds`).

use crate::infer::{Infer, TypeError};
use crate::sig::World;
use crate::unify::{self, Content, FlatTy, SuperType};
use crate::{Scheme, TyVarId};
use base::{Name, Span};
use hir::SkyDb;

/// A deferred bound obligation, with the span of the code that raised it.
#[derive(Clone, Debug)]
pub(crate) struct Obligation {
    pub bound: SuperType,
    pub var: TyVarId,
    pub span: Option<Span>,
}

/// Runtime handles (A-1/A-1b): an id into a table of one running process.
/// Never ordered, never encoded (a handle decoded from JSON would be forged,
/// and a restored one is dead).
const HANDLES: &[&str] = &[
    "Sky.Core.Process.Process",
    "Std.Watch.Watcher",
    "Std.Sync.Ref",
    "Std.Sync.Mutex",
    "Std.Sync.Queue",
    "Sky.Core.WebSocket.WebSocket",
    "Sky.Http.Server.WebSocket.WebSocketServer",
    "Sky.Core.Http.Stream.StreamId",
    "Sky.Http.Server.Stream.StreamWriter",
    "Std.Cache.Cache",
];

/// Secrets, keys and protocol state (B-1): each redacts itself in every JSON
/// path, so it would encode as `{}` and fail to decode.
const SECRETS: &[&str] = &[
    "Sky.Core.Secret.Secret",
    "Std.Crypto.Sign.SecretKey",
    "Std.Crypto.Sign.PublicKey",
    "Std.Crypto.Kx.SecretKey",
    "Std.Crypto.Kx.PublicKey",
    "Std.Crypto.Noise.Handshake",
    "Std.Crypto.Noise.Transport",
    "Std.Crypto.Cpace.Pending",
];

/// Kernel-implicit types (no Sky constructors) whose values hold closures, so
/// the reflective encoder panics on them.
const CLOSURE_KERNEL_TYPES: &[&str] = &[
    "Task",
    "Cmd",
    "Sub",
    "Html",
    "Attribute",
    "Element",
    "Decoder",
];

/// Opaque stdlib types the runtime encoder has a dedicated arm for.
const ENCODABLE_OPAQUE: &[&str] = &["Std.Decimal.Decimal"];

/// What the nominal environment says about a type name, for a bound.
pub(crate) enum Nominal {
    /// A handle, a secret, a key or protocol state: never ordered or encoded.
    Denied(&'static str),
    /// A kernel type holding closures: never ordered or encoded.
    Closures,
    /// A stdlib type with a hidden constructor: no order; encodes only when
    /// the runtime has a dedicated arm for it.
    Opaque { encodable: bool },
    /// A custom type with known constructors: decided by their payloads.
    Union(Vec<(String, Scheme)>),
    /// Nothing is known (a Go FFI type, an unresolved name): no order;
    /// encodes (the reflective encoder handles exported Go data).
    Unknown,
}

/// Is `name` (qualified `M.T` or bare `T`) the type `qualified`?
fn names(name: &str, qualified: &str) -> bool {
    if name.contains('.') {
        name == qualified
    } else {
        crate::nominal::base(qualified) == name
    }
}

/// Classify the nominal type `name` for a bound (see the module doc).
pub(crate) fn classify(world: &World, db: &dyn SkyDb, name: &str) -> Nominal {
    if HANDLES.iter().any(|q| names(name, q)) {
        return Nominal::Denied("a runtime handle");
    }
    if SECRETS.iter().any(|q| names(name, q)) {
        return Nominal::Denied("a secret, key or protocol state");
    }
    let ctors = union_ctors(world, db, name);
    if ctors.is_empty() {
        if !name.contains('.') && CLOSURE_KERNEL_TYPES.contains(&name) {
            return Nominal::Closures;
        }
        return Nominal::Unknown;
    }
    let opaque = ctors
        .iter()
        .any(|(c, _)| c.ends_with("_OPAQUE") || c.ends_with("__Internal"));
    if opaque {
        return Nominal::Opaque {
            encodable: ENCODABLE_OPAQUE.iter().any(|q| names(name, q)),
        };
    }
    Nominal::Union(ctors)
}

/// The constructors of the union `name`, with their schemes, in declaration
/// order. Empty when `name` is not a known union.
fn union_ctors(world: &World, db: &dyn SkyDb, name: &str) -> Vec<(String, Scheme)> {
    if let Some((module, tname)) = name.rsplit_once('.') {
        let Some(m) = db.module_by_name(module) else {
            return Vec::new();
        };
        let tdef = db.intern_def(m, &Name::new(tname), hir::DefKind::TypeCon);
        let Some(members) = world.union_members_by_def.get(&tdef) else {
            return Vec::new();
        };
        members
            .iter()
            .filter_map(|c| {
                let cdef = db.intern_def(m, &Name::new(c), hir::DefKind::Ctor);
                world
                    .ctors_by_def
                    .get(&cdef)
                    .map(|s| (c.clone(), s.clone()))
            })
            .collect()
    } else {
        let Some(members) = world.union_ctors.get(name) else {
            return Vec::new();
        };
        members
            .iter()
            .filter_map(|c| world.ctors.get(c).map(|s| (c.clone(), s.clone())))
            .collect()
    }
}

impl Infer<'_> {
    /// Move the obligations the unifier recorded into the checker's list,
    /// anchored at the expression under inference.
    pub(crate) fn drain_pending(&mut self) {
        if self.uf.pending.is_empty() {
            return;
        }
        let span = self.cur_span;
        for (bound, var) in std::mem::take(&mut self.uf.pending) {
            self.obligations.push(Obligation { bound, var, span });
        }
    }

    /// Decide every deferred obligation (see the module doc). `fin` is the
    /// end of a def: an open record row is then decided (refused for an
    /// ordering, bounded for an encoding). Before that (let-generalisation) an
    /// open record is kept for later. The lowering path never raises bounds
    /// (`Infer::fresh_for_name`), so it has nothing to decide.
    pub(crate) fn discharge_bounds(&mut self, fin: bool) {
        if self.use_inferred_path() {
            self.uf.pending.clear();
            self.obligations.clear();
            return;
        }
        let saved = self.cur_span;
        let mut kept = Vec::new();
        loop {
            self.drain_pending();
            let batch = std::mem::take(&mut self.obligations);
            if batch.is_empty() {
                break;
            }
            for ob in batch {
                self.cur_span = ob.span;
                if !self.discharge_one(&ob, fin) {
                    kept.push(ob);
                }
            }
        }
        self.obligations = kept;
        self.cur_span = saved;
    }

    /// Decide one obligation. `false` = keep it for the end of the def.
    fn discharge_one(&mut self, ob: &Obligation, fin: bool) -> bool {
        let r = self.uf.find(ob.var);
        match self.uf.content(r) {
            Content::Structure(FlatTy::Record(fs, ext)) => {
                let (fs, ext) = self.uf.normalize_record(fs, ext);
                // Fields that arrived through the row since the record was
                // first bounded carry the bound too.
                for (_, f) in fs {
                    let b = self.uf.fresh(Content::FlexSuper(ob.bound));
                    self.unify_bound(f, b);
                }
                let Some(row) = ext else {
                    return true;
                };
                if !matches!(self.uf.content(row), Content::Flex | Content::FlexSuper(_)) {
                    return true;
                }
                if !fin {
                    return false;
                }
                if ob.bound.has(SuperType::Comparable) {
                    let what = self.uf.describe(r);
                    self.push_bound_error(unify::comparable_failure(&format!(
                        "a record whose other fields are not known here (`{what}` with an open \
                         row, from a field access such as `r.x`)"
                    )));
                } else {
                    let b = self.uf.fresh(Content::FlexSuper(ob.bound));
                    self.unify_bound(row, b);
                }
                true
            }
            Content::Structure(FlatTy::App(n, args)) => {
                self.discharge_nominal(ob.bound, r, n.as_str(), &args);
                true
            }
            _ => true,
        }
    }

    /// A nominal obligation: `bound` on the type `name args` at root `r`.
    fn discharge_nominal(&mut self, bound: SuperType, r: TyVarId, name: &str, args: &[TyVarId]) {
        let key = (
            bound,
            name.to_string(),
            args.iter().map(|&a| self.uf.find(a)).collect::<Vec<_>>(),
        );
        if !self.bound_seen.insert(key) {
            return;
        }
        let comparable = bound.has(SuperType::Comparable);
        let shown = format!("`{}`", self.uf.describe(r));
        let refuse = |this: &mut Self, why: &str| {
            let subject = format!("{shown} ({why})");
            let msg = if comparable {
                unify::comparable_failure(&subject)
            } else {
                unify::encodable_failure(&subject)
            };
            this.push_bound_error(msg);
        };
        match classify(self.world, self.db, name) {
            Nominal::Denied(kind) => refuse(self, kind),
            Nominal::Closures => refuse(self, "its values hold functions"),
            Nominal::Opaque { encodable } => {
                if comparable {
                    refuse(
                        self,
                        "an opaque type: its constructor is hidden, so it has no derived order; \
                         when its module has its own `compare` (`Decimal.compare`, \
                         `Money.compare`), sort with `List.sortWith` and that function",
                    );
                } else if !encodable {
                    refuse(self, "an opaque type the encoder cannot see inside");
                }
            }
            Nominal::Unknown => {
                if comparable {
                    refuse(
                        self,
                        "a type with no constructors Sky can see, so it has no order",
                    );
                }
            }
            Nominal::Union(ctors) => {
                let tname = crate::nominal::strip(name).to_string();
                for (cname, scheme) in ctors {
                    let mut cur = self.instantiate(&scheme);
                    let mut payload = Vec::new();
                    loop {
                        let c = self.uf.find(cur);
                        match self.uf.content(c) {
                            Content::Structure(FlatTy::Fun(a, b)) => {
                                payload.push(a);
                                cur = b;
                            }
                            _ => break,
                        }
                    }
                    // Bind the constructor's type parameters to `args`.
                    let _ = self.uf.unify(cur, r);
                    self.drain_pending();
                    for p in payload {
                        let b = self.uf.fresh(Content::FlexSuper(bound));
                        let res = self.uf.unify(p, b);
                        self.drain_pending();
                        if let Err(m) = res {
                            self.push_bound_error(format!(
                                "in the custom type `{tname}`, the constructor `{cname}` holds a \
                                 value that {}: {}",
                                if comparable {
                                    "cannot be ordered"
                                } else {
                                    "cannot be encoded"
                                },
                                m.message
                            ));
                            return;
                        }
                    }
                }
            }
        }
    }

    /// `uf.unify` for a bound, reporting a failure at the obligation's span.
    fn unify_bound(&mut self, a: TyVarId, b: TyVarId) {
        let res = self.uf.unify(a, b);
        self.drain_pending();
        if let Err(m) = res {
            self.push_bound_error(m.message);
        }
    }

    fn push_bound_error(&mut self, message: String) {
        self.errors.push(TypeError {
            message,
            span: self.cur_span,
            code: "E2001",
        });
    }

    /// After the annotation gate: every ARGUMENT-only annotation variable
    /// (kept flexible) that the body bounded must spell the bound. A missing
    /// `Comparable`/`Number`/`Appendable` is Elm's "too general" error; a
    /// missing `Encodable` is recorded in `inferred_bounds` (B-1), together
    /// with the ones rigid variables picked up (`UnionFind::rigid_needs`).
    pub(crate) fn check_annotation_bounds(
        &mut self,
        body: &hir::Body,
        root: hir::ExprId,
        arg_only: &[(String, TyVarId)],
    ) {
        for (name, v) in arg_only {
            let r = self.uf.find(*v);
            let Content::FlexSuper(b) = self.uf.content(r) else {
                continue;
            };
            let have = SuperType::from_var_name(name).unwrap_or_default().closure();
            let missing = b.closure().minus(have);
            if missing.minus(SuperType::Encodable).is_empty() {
                if missing.has(SuperType::Encodable) {
                    self.inferred_bounds
                        .push((Name::new(name), SuperType::Encodable));
                }
                continue;
            }
            self.errors.push(TypeError {
                message: unify::rigid_bound_message(name, missing),
                span: body.expr_span(root),
                code: "E2001",
            });
        }
        for (n, b) in std::mem::take(&mut self.uf.rigid_needs) {
            if !self.inferred_bounds.iter().any(|(m, _)| *m == n) {
                self.inferred_bounds.push((n, b));
            }
        }
    }
}

/// The stdlib APIs that encode a free type by reflection (B-1), and the type
/// variable of each that must be `Encodable`. Their `.sky` signatures keep the
/// plain name (the lowerer reads those); the bound is added in the check-only
/// channel ([`World::bound_check_sigs`]). Both directions: `Jobs.define`
/// decodes the payload it is handed, `Codec.auto` builds a decoder too.
const ENCODABLE_APIS: &[(&str, &str, &str)] = &[
    ("Std.Codec", "auto", "a"),
    ("Std.Codec", "autoCamel", "a"),
    ("Std.Codec", "autoWith", "a"),
    ("Std.App", "withDurable", "model"),
    ("Std.App", "withDurableId", "model"),
    ("Std.Db.Table", "table", "a"),
    ("Std.Db.Table", "insert", "a"),
    ("Std.Jobs", "define", "a"),
    ("Std.Jobs", "enqueue", "a"),
    ("Std.Jobs", "enqueueIn", "a"),
    ("Std.Auth", "signToken", "a"),
    ("Std.Auth", "signSlidingToken", "a"),
];

/// The name a plain variable gets once it carries an inferred bound:
/// `a` → `encodable_a`. The label prefix is what makes instantiation restore
/// the bound ([`SuperType::from_var_name`]); the suffix keeps it readable.
fn bounded_name(var: &str, b: SuperType) -> String {
    format!("{}_{var}", b.label())
}

/// `scheme` with each named variable renamed to carry its bound.
fn with_bounds(scheme: &Scheme, bounds: &[(Name, SuperType)]) -> Scheme {
    let map: std::collections::HashMap<Name, Name> = bounds
        .iter()
        .map(|(v, b)| (v.clone(), Name::new(&bounded_name(v.as_str(), *b))))
        .collect();
    Scheme::generalize(crate::variance::rename_vars(&scheme.ty, &map))
}

/// Pass 4 (with the other static seeds): the bounded overrides of
/// [`ENCODABLE_APIS`].
pub(crate) fn seed_bound_overrides(world: &mut World, db: &dyn SkyDb) {
    for (module, name, var) in ENCODABLE_APIS {
        let Some(m) = db.module_by_name(module) else {
            continue;
        };
        let def = db.intern_def(m, &Name::new(name), hir::DefKind::Value);
        let Some(scheme) = world.value_sigs.get(&def) else {
            continue;
        };
        let bounded = with_bounds(scheme, &[(Name::new(var), SuperType::Encodable)]);
        if let Some((_, pseudo)) = hir::KERNEL_MODULES.iter().find(|(p, _)| p == module) {
            world
                .bound_kernel_sigs
                .insert((pseudo.to_string(), name.to_string()), bounded.clone());
        }
        world.bound_check_sigs.insert(def, bounded);
    }
}

/// Does a scheme type carry a variable whose bound a CALLER can pick up?
/// `encodable_only` narrows to the bounds an ANNOTATED caller infers
/// (`Encodable` without `Comparable`; a missing `Comparable` is an error).
fn has_bound_var(t: &crate::Ty, encodable_only: bool) -> bool {
    t.free_vars()
        .iter()
        .any(|v| match SuperType::from_var_name(v.as_str()) {
            Some(b) if encodable_only => {
                !b.has(SuperType::Comparable) && b.has(SuperType::Encodable)
            }
            Some(_) => true,
            None => false,
        })
}

/// Every `Res::Def` / `Res::Kernel` a body references.
fn callees(body: &hir::Body) -> (Vec<base::DefId>, Vec<(String, String)>) {
    let mut defs = Vec::new();
    let mut kernels = Vec::new();
    for (_, e) in body.exprs.iter() {
        match e {
            hir::Expr::Var(hir::Res::Def(d)) => defs.push(*d),
            hir::Expr::Var(hir::Res::Kernel { module, func }) => {
                kernels.push((module.as_str().to_string(), func.as_str().to_string()))
            }
            _ => {}
        }
    }
    (defs, kernels)
}

impl World {
    /// The scheme a check-path caller of `d` instantiates (mirrors the order
    /// of `Infer::infer_res`).
    fn caller_scheme(&self, d: &base::DefId) -> Option<&Scheme> {
        self.bound_check_sigs
            .get(d)
            .or_else(|| self.any_result_check_sigs.get(d))
            .or_else(|| self.value_sigs.get(d))
            .or_else(|| self.check_sigs.get(d))
            .or_else(|| self.app_check_sigs.get(d))
    }

    /// Pass 6c (v0.27.0, C-11/B-1): carry bounds through helpers, to a
    /// fixpoint.
    ///
    /// * An ANNOTATED def whose body forces `Encodable` on a plain variable
    ///   (`persist : a -> Codec a; persist b = Codec.auto b`) is seen by its
    ///   callers as `encodable_a -> Codec encodable_a` ([`World::bound_check_sigs`]).
    /// * An UNANNOTATED def is re-inferred when a callee's scheme gained a
    ///   bound after the def was inferred: a same-module helper defined LATER
    ///   (pass 5 infers in source order and saw it as a fresh variable), or a
    ///   def this pass changed.
    ///
    /// Each round re-checks only the defs that reference a changed def, so
    /// the pass costs a handful of inferences on a real program. Bounds only
    /// grow, so it converges; the round cap is a backstop.
    pub(crate) fn infer_bounds_fixpoint(&mut self, db: &dyn SkyDb) {
        use crate::infer::Infer;
        struct Target {
            def: base::DefId,
            body: hir::Body,
            annotated: bool,
            defs: Vec<base::DefId>,
            kernels: Vec<(String, String)>,
            later_siblings: Vec<base::DefId>,
        }
        let mut targets: Vec<Target> = Vec::new();
        for m in db.module_ids() {
            let resolved = db.resolve(m);
            let order: std::collections::HashMap<base::DefId, usize> = resolved
                .top_defs
                .iter()
                .enumerate()
                .map(|(i, td)| (td.def, i))
                .collect();
            for (def, body) in &resolved.bodies {
                let Some(&idx) = order.get(def) else {
                    continue;
                };
                let annotated = self.value_sigs.contains_key(def);
                if !annotated {
                    // Only defs pass 5 admitted; never a CAF (its scheme may
                    // carry weak variables, pass 6b).
                    let caf = body.params.is_empty()
                        && body
                            .root
                            .is_some_and(|r| !crate::infer::is_syntactic_value(body, r));
                    if !self.app_check_sigs.contains_key(def) || caf {
                        continue;
                    }
                }
                let (defs, kernels) = callees(body);
                let later_siblings = defs
                    .iter()
                    .filter(|d| order.get(d).is_some_and(|&j| j > idx))
                    .copied()
                    .collect();
                targets.push(Target {
                    def: *def,
                    body: body.clone(),
                    annotated,
                    defs,
                    kernels,
                    later_siblings,
                });
            }
        }
        let mut changed: std::collections::HashSet<base::DefId> = std::collections::HashSet::new();
        for round in 0..6 {
            let mut now: std::collections::HashSet<base::DefId> = std::collections::HashSet::new();
            for t in &targets {
                let due = if round == 0 {
                    if t.annotated {
                        t.defs.iter().any(|d| {
                            self.caller_scheme(d)
                                .is_some_and(|s| has_bound_var(&s.ty, true))
                        }) || t
                            .kernels
                            .iter()
                            .any(|k| self.bound_kernel_sigs.contains_key(k))
                    } else {
                        t.later_siblings.iter().any(|d| {
                            self.caller_scheme(d)
                                .is_some_and(|s| has_bound_var(&s.ty, false))
                        })
                    }
                } else {
                    t.defs.iter().any(|d| changed.contains(d))
                };
                if !due {
                    continue;
                }
                if t.annotated {
                    let Some(anno) = self.value_sigs.get(&t.def).cloned() else {
                        continue;
                    };
                    let mut infer = Infer::new(self, db).with_self_def(Some(t.def));
                    infer.infer_def_against(&t.body, &anno);
                    if !infer.errors.is_empty() || infer.inferred_bounds.is_empty() {
                        continue;
                    }
                    let bounds = infer.inferred_bounds.clone();
                    let base = self
                        .any_result_check_sigs
                        .get(&t.def)
                        .cloned()
                        .unwrap_or(anno);
                    let bounded = with_bounds(&base, &bounds);
                    if self.bound_check_sigs.get(&t.def) != Some(&bounded) {
                        self.bound_check_sigs.insert(t.def, bounded);
                        now.insert(t.def);
                    }
                } else {
                    let mut infer = Infer::new(self, db).with_self_def(Some(t.def));
                    let Some(scheme) = infer.infer_def_scheme(&t.body, true) else {
                        continue;
                    };
                    if self.app_check_sigs.get(&t.def) != Some(&scheme) {
                        self.app_check_sigs.insert(t.def, scheme);
                        now.insert(t.def);
                    }
                }
            }
            if now.is_empty() {
                break;
            }
            changed = now;
        }
    }
}

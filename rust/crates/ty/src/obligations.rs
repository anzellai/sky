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

/// A deferred tie between a routed app's page type and its model's `page`
/// field (v0.29.14). `Std.App.withRoutes` has the type
/// `List (Route page) -> App f s page model msg key -> …`, and the runtime
/// writes the routed page (and the NotFound page) into the model's `page`
/// field on every request. A model with no `page` field is valid (the runtime
/// writes nothing), so the tie is not in the signature: it is decided once the
/// model's type is known ([`Infer::discharge_page_ties`]).
#[derive(Clone, Debug)]
pub(crate) struct PageTie {
    pub page: TyVarId,
    pub model: TyVarId,
    pub span: Option<Span>,
    /// A raw route API in a user module: a model still unknown at the end of
    /// the def is refused (no caller can check it).
    pub strict: bool,
}

/// A raw route API whose use ties a page to the model's `page` field.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum RoutedApi {
    /// `Live.config { …, routes : List (Route page), notFound : page }`.
    LiveConfig,
    /// `Spa.withRoutes : List (Route page) -> AppConfig model msg -> …`.
    SpaRoutes,
    /// `Spa.withNotFound : page -> AppConfig model msg -> …`.
    SpaNotFound,
}

/// Does a scheme type hold an `App f s page model msg key` whose page or
/// model has a type variable? Only then can a use of it meet a model its own
/// body did not check.
pub(crate) fn app_page_or_model_is_generic(t: &crate::Ty) -> bool {
    use crate::Ty;
    match t {
        Ty::App(n, args) => {
            (n.as_str() == "Std.App.App"
                && args.len() == 6
                && (!args[2].free_vars().is_empty() || !args[3].free_vars().is_empty()))
                || args.iter().any(app_page_or_model_is_generic)
        }
        Ty::Fun(a, b) => app_page_or_model_is_generic(a) || app_page_or_model_is_generic(b),
        Ty::Tuple(vs) => vs.iter().any(app_page_or_model_is_generic),
        Ty::Record(fs, _) => fs.iter().any(|(_, f)| app_page_or_model_is_generic(f)),
        _ => false,
    }
}

/// A stdlib module: its routing plumbing is generic in the model, and the
/// user's use of it is what the checker ties.
fn is_stdlib_module(name: &str) -> bool {
    name.starts_with("Std.") || name.starts_with("Sky.")
}

/// Pass 4 (v0.29.14): the routing defs of [`PageTie`]. The carriers are
/// `Std.App.withRoutes` and every user def whose body reaches a carrier (a
/// fixpoint over the call graph, [`extend_routed_page_carriers`]), so a
/// generic helper is checked at its callers. The raw APIs tie at their own
/// use.
fn seed_routed_page(world: &mut World, db: &dyn SkyDb) {
    let mut apis = std::collections::HashMap::new();
    for (module, name, api) in [
        ("Std.Live", "config", RoutedApi::LiveConfig),
        ("Std.Spa", "withRoutes", RoutedApi::SpaRoutes),
        ("Std.Spa", "withNotFound", RoutedApi::SpaNotFound),
    ] {
        if let Some(d) = routing_def(db, module, name).filter(|d| world.value_sigs.contains_key(d))
        {
            apis.insert(d, api);
        }
    }
    world.routed_page_apis = apis;
    world.routed_page_carriers = std::collections::HashSet::new();
    extend_routed_page_carriers(world, db);
}

fn routing_def(db: &dyn SkyDb, module: &str, name: &str) -> Option<base::DefId> {
    let m = db.module_by_name(module)?;
    Some(db.intern_def(m, &Name::new(name), hir::DefKind::Value))
}

/// Add to the routed page carriers every user def of `db`'s module set whose
/// body reaches a carrier. Additive, so the shared-world path runs it again
/// over a case's own modules (a body-derived pass, see
/// `World::extend_query_bodies`) on top of the base world's carriers; the
/// whole-program path reaches the same set.
pub(crate) fn extend_routed_page_carriers(world: &mut World, db: &dyn SkyDb) {
    let Some(with_routes) =
        routing_def(db, "Std.App", "withRoutes").filter(|d| world.value_sigs.contains_key(d))
    else {
        return;
    };
    let mut carriers = std::mem::take(&mut world.routed_page_carriers);
    carriers.insert(with_routes);
    let mut user = Vec::new();
    for m in db.module_ids() {
        if is_stdlib_module(db.module_name(m)) {
            continue;
        }
        let resolved = db.resolve(m);
        for (def, body) in &resolved.bodies {
            user.push((*def, callees(body).0));
        }
    }
    loop {
        let mut changed = false;
        for (d, calls) in &user {
            if !carriers.contains(d) && calls.iter().any(|c| carriers.contains(c)) {
                carriers.insert(*d);
                changed = true;
            }
        }
        if !changed {
            break;
        }
    }
    world.routed_page_carriers = carriers;
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
    // v0.29.0: a compiled `Regex` (a `*regexp.Regexp`). Store its pattern.
    "Sky.Core.Regex.Regex",
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
        // The end of a def: an integer literal that no rigid `number`
        // variable absorbed is the `Int` it always was (`SuperType::IntLit`).
        if fin {
            self.uf.default_all_int_literals();
        }
        self.discharge_page_ties(fin);
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

    /// Tie every `Std.App.App f s page model msg key` in `inst`, the instance
    /// of a use of a routing def (`App.withRoutes`, or a user def that reaches
    /// it). A generic helper keeps the tie out of its own scheme; its callers
    /// are checked here, where the model is known.
    pub(crate) fn tie_app_nodes(&mut self, inst: TyVarId) {
        let mut seen = std::collections::HashSet::new();
        let mut tied = std::collections::HashSet::new();
        let mut stack = vec![inst];
        while let Some(v) = stack.pop() {
            let r = self.uf.find(v);
            if !seen.insert(r) {
                continue;
            }
            match self.uf.content(r) {
                Content::Structure(FlatTy::App(n, args)) => {
                    if n.as_str() == "Std.App.App" {
                        if let [_, _, page, model, _, _] = args[..] {
                            let key = (self.uf.find(page), self.uf.find(model));
                            if tied.insert(key) {
                                self.page_ties.push(PageTie {
                                    page,
                                    model,
                                    span: self.cur_span,
                                    strict: false,
                                });
                            }
                        }
                    }
                    stack.extend(args);
                }
                Content::Structure(FlatTy::Fun(a, b)) => stack.extend([a, b]),
                Content::Structure(FlatTy::Tuple(vs)) => stack.extend(vs),
                Content::Structure(FlatTy::Record(fs, ext)) => {
                    stack.extend(fs.values().copied());
                    stack.extend(ext);
                }
                _ => {}
            }
        }
    }

    /// The `(argument, result)` of a function type, if `t` is one.
    fn fun_parts(&mut self, t: TyVarId) -> Option<(TyVarId, TyVarId)> {
        match self.uf.content(t) {
            Content::Structure(FlatTy::Fun(a, b)) => Some((a, b)),
            _ => None,
        }
    }

    /// The first argument of a nominal type application, if `t` is one.
    fn first_arg(&mut self, t: TyVarId) -> Option<TyVarId> {
        match self.uf.content(t) {
            Content::Structure(FlatTy::App(_, args)) => args.first().copied(),
            _ => None,
        }
    }

    /// Tie the page and the model of a use of a raw route API (`inst` is its
    /// instance). In a user module the tie is strict: a model that is still
    /// unknown at the end of the def is refused, because `AppConfig model msg`
    /// carries no page type a caller could check.
    pub(crate) fn tie_routed_api(&mut self, api: RoutedApi, inst: TyVarId) {
        let found = (|| {
            let (arg, rest) = self.fun_parts(inst)?;
            match api {
                RoutedApi::LiveConfig => {
                    let page = match self.uf.content(arg) {
                        Content::Structure(FlatTy::Record(fs, ext)) => {
                            let (fs, _) = self.uf.normalize_record(fs, ext);
                            *fs.get(&Name::new("notFound"))?
                        }
                        _ => return None,
                    };
                    Some((page, self.first_arg(rest)?))
                }
                RoutedApi::SpaRoutes => {
                    let route = self.first_arg(arg)?;
                    let page = self.first_arg(route)?;
                    let (cfg, _) = self.fun_parts(rest)?;
                    Some((page, self.first_arg(cfg)?))
                }
                RoutedApi::SpaNotFound => {
                    let (cfg, _) = self.fun_parts(rest)?;
                    Some((arg, self.first_arg(cfg)?))
                }
            }
        })();
        let Some((page, model)) = found else {
            return;
        };
        let strict = self
            .self_def
            .and_then(|d| self.db.def_loc(d))
            .is_some_and(|l| !is_stdlib_module(self.db.module_name(l.module)));
        self.page_ties.push(PageTie {
            page,
            model,
            span: self.cur_span,
            strict,
        });
    }

    /// Decide each [`PageTie`] whose model type is known: a record with a
    /// `page` field must hold the routes' page type there. A model that is
    /// still a variable (or a record whose row may still gain a `page`
    /// field) waits for the end of the def; then a model with no `page`
    /// field passes (the runtime writes nothing into it).
    pub(crate) fn discharge_page_ties(&mut self, fin: bool) {
        let saved = self.cur_span;
        let mut kept = Vec::new();
        for tie in std::mem::take(&mut self.page_ties) {
            let r = self.uf.find(tie.model);
            let field = match self.uf.content(r) {
                Content::Structure(FlatTy::Record(fs, ext)) => {
                    let (fs, ext) = self.uf.normalize_record(fs, ext);
                    match fs.get(&Name::new("page")) {
                        Some(&f) => f,
                        None => {
                            let open = ext.is_some_and(|row| {
                                matches!(
                                    self.uf.content(row),
                                    Content::Flex | Content::FlexSuper(_)
                                )
                            });
                            if open && !fin {
                                kept.push(tie);
                            }
                            continue;
                        }
                    }
                }
                Content::Flex | Content::FlexSuper(_) if !fin => {
                    kept.push(tie);
                    continue;
                }
                Content::Flex | Content::FlexSuper(_) | Content::Rigid(_) if tie.strict => {
                    self.errors.push(TypeError {
                        message: "The routed page type cannot be checked here: the model type is \
                                  not known in this function.\n\nThe app writes the routed page \
                                  into the model's `page` field on every request, so the checker \
                                  must see the model type where the routes are set. Give this \
                                  function a type annotation with the app's model type, or use \
                                  `App.withRoutes`, whose routes the checker follows through \
                                  helpers."
                            .to_string(),
                        span: tie.span,
                        code: "E2001",
                    });
                    continue;
                }
                _ => continue,
            };
            let routed = self.uf.describe(tie.page);
            let held = self.uf.describe(field);
            if self.uf.unify(field, tie.page).is_err() {
                self.errors.push(TypeError {
                    message: format!(
                        "The routes give pages of type `{routed}`, but the model's `page` field \
                         has type `{held}`.\n\nThe app writes the routed page (and the \
                         `App.withNotFound` page) into the model's `page` field on every \
                         request, so the two types must be the same. Make every \
                         `App.route` / `App.routeParam` page and the `App.withNotFound` page a \
                         `{held}`, or give the model field another name if it is not the \
                         app's page."
                    ),
                    span: tie.span,
                    code: "E2001",
                });
            }
            self.cur_span = tie.span;
            self.drain_pending();
        }
        self.cur_span = saved;
        self.page_ties.extend(kept);
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

    /// After the annotation gate, when the body raised no other error: refuse
    /// each bound a rigid annotation variable needs and does not spell
    /// (`UnionFind::rigid_missing`: a missing `Comparable`/`Number`/
    /// `Appendable`, Elm's "too general" error), and record the bounds the
    /// checker infers instead (`UnionFind::rigid_needs`, only `Encodable`,
    /// B-1) in `inferred_bounds`.
    pub(crate) fn check_annotation_bounds(&mut self, body: &hir::Body, root: hir::ExprId) {
        // The deferred refusals (`UnionFind::rigid_missing`): one per
        // variable, its missing bounds joined.
        let mut missing: Vec<(Name, SuperType)> = Vec::new();
        for (n, b) in std::mem::take(&mut self.uf.rigid_missing) {
            match missing.iter_mut().find(|(m, _)| *m == n) {
                Some((_, have)) => *have = have.union(b),
                None => missing.push((n, b)),
            }
        }
        for (n, b) in missing {
            self.errors.push(TypeError {
                message: unify::rigid_bound_message(n.as_str(), b),
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
    seed_routed_page(world, db);
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
    pub(crate) fn caller_scheme(&self, d: &base::DefId) -> Option<&Scheme> {
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
    /// * An UNANNOTATED def is re-inferred when a callee's scheme gains a
    ///   bound after the dependency/SCC inference pass, for example through an
    ///   annotated helper whose inferred Encodable bound is published here.
    ///
    /// A worklist revisits callers only when a callee's bounds change. The
    /// finite bound lattice grows monotonically, without a helper-depth limit.
    /// Recursive type equations have already been solved by the app SCC pass.
    pub(crate) fn infer_bounds_fixpoint(&mut self, db: &dyn SkyDb) {
        use crate::infer::Infer;
        struct Target {
            def: base::DefId,
            body: crate::retained_body::RetainedBody,
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
                    if !self.app_check_sigs.contains_key(def)
                        || self.app_group_errors.contains_key(def)
                        || caf
                    {
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
                    body: crate::retained_body::RetainedBody::new(&resolved, *def)
                        .expect("iterated bound target belongs to its resolved owner"),
                    annotated,
                    defs,
                    kernels,
                    later_siblings,
                });
            }
        }
        let mut dependents: std::collections::HashMap<base::DefId, Vec<usize>> =
            std::collections::HashMap::new();
        let mut queue = std::collections::VecDeque::new();
        let mut queued = vec![false; targets.len()];
        for (i, t) in targets.iter().enumerate() {
            for d in &t.defs {
                dependents.entry(*d).or_default().push(i);
            }
            let due = if t.annotated {
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
            };
            if due {
                queue.push_back(i);
                queued[i] = true;
            }
        }
        while let Some(i) = queue.pop_front() {
            queued[i] = false;
            let t = &targets[i];
            if self.app_group_errors.contains_key(&t.def) {
                continue;
            }
            let mut changed = false;
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
                    changed = true;
                }
            } else {
                let Some(previous) = self.app_check_sigs.get(&t.def).cloned() else {
                    continue;
                };
                let mut infer = Infer::new(self, db).with_self_def(Some(t.def));
                let Some(scheme) = infer.refine_def_scheme(&t.body, &previous) else {
                    continue;
                };
                if !infer.errors.is_empty() {
                    let errors = std::mem::take(&mut infer.errors);
                    drop(infer);
                    self.app_group_errors
                        .entry(t.def)
                        .or_default()
                        .extend(errors);
                    continue;
                }
                if self.app_check_sigs.get(&t.def) != Some(&scheme) {
                    self.app_check_sigs.insert(t.def, scheme);
                    changed = true;
                }
            }
            if changed {
                for &caller in dependents.get(&t.def).into_iter().flatten() {
                    if !queued[caller] {
                        queue.push_back(caller);
                        queued[caller] = true;
                    }
                }
            }
        }
    }
}

//! Signature extraction (doc 06 §"SIGNATURES SOURCE"). Stdlib + kernel function
//! types come from their `name : Type` annotations (and `Ffi.kernel "K"` aliased
//! sigs) declared in `sky-stdlib/**/*.sky`; constructor types come from union
//! declarations; the four built-in super-typed operators come from a static
//! table. All read straight off the **AST** (`syntax::ast::Type`) so the
//! DefId-keyed body collision in `hir` is irrelevant here, and type aliases are
//! expanded transparently (record aliases → records so field access unifies;
//! function aliases like `Handler` → `Fun`).

use crate::{Scheme, Ty};
use base::{DefId, ModuleId, Name};
use hir::{DefKind, SkyDb, KERNEL_MODULES};
use std::collections::HashMap;
use syntax::ast::{self, AstNode};
use syntax::{SyntaxKind, SyntaxNode};

/// One alias definition: its type parameters and its (unexpanded) body.
#[derive(Clone, PartialEq, Eq)]
struct AliasDef {
    params: Vec<String>,
    body: Ty,
}

/// The typed world: everything inference needs to look a name's scheme up.
///
/// `PartialEq`/`Eq` back salsa **backdating** of the `type_world` tracked query
/// (Stage D-2): the world is a pure function of every module's declarations
/// (annotations, unions, aliases) plus pass-3 inference of the stdlib
/// combinators — none of which a body-only edit to app code changes. So an app
/// body edit re-executes `World::build` but yields a value-equal `World`; salsa
/// backdates it and dependent `infer(DefId)` queries validate from memo rather
/// than re-inferring. See `skydb::type_world_query`.
#[derive(Clone, PartialEq, Eq)]
pub struct World {
    /// Top-level value schemes from EXPLICIT annotations, keyed by `DefId`
    /// (matches `Res::Def`). Only these are surfaced to the lowerer as a
    /// "declared signature" (`Typer::value_sig`).
    pub value_sigs: HashMap<DefId, Scheme>,
    /// Schemes INFERRED for unannotated stdlib combinators (pass 3). Consulted
    /// by inference at a `Res::Def` call site (to pin results from arg types)
    /// but NOT exposed as declared signatures — the lowerer keeps its lenient
    /// body-inference path for these defs, so their own emission is unchanged.
    pub inferred_sigs: HashMap<DefId, Scheme>,
    /// Kernel function schemes, keyed by `(pseudo-module, func)`
    /// (matches `Res::Kernel`).
    pub kernel_sigs: HashMap<(String, String), Scheme>,
    /// Precise combinator schemes consulted by the accept/reject CHECKER ONLY
    /// (`use_inferred == false`). Deliberately invisible to the lowerer
    /// (`use_inferred == true`) and to pass-3 inference, so call-site element
    /// pinning cannot perturb Go emission (see the pass-3 `Result`-only
    /// exclusion rationale at sig.rs ~line 180-189). Closes audit #3.
    pub check_sigs: HashMap<DefId, Scheme>,
    pub check_kernel_sigs: HashMap<(String, String), Scheme>,
    /// CHECK-ONLY monomorphic schemes for UNANNOTATED **app-module** top-level
    /// defs used cross-module (F1c narrow subset). Same isolation contract as
    /// `check_sigs`: consulted only by the accept/reject checker
    /// (`use_inferred == false`), never by the lowerer (`use_inferred == true`)
    /// nor pass-3 inference, so Go emission is byte-identical. STRICTLY filtered
    /// at populate time (`infer_app_check_sigs`): admits only fully-monomorphic,
    /// Unit-spine-free types (e.g. `allCategories : List String`, `Int -> Int`,
    /// `String`, `Int -> {px:Int, py:Int}`) — every polymorphic/Unit-spine app
    /// helper is excluded to preserve accept-parity. Record-typed monomorphic
    /// sigs ARE admitted (the record-free clause was lifted once record
    /// row-polymorphism became sound) so cross-module record
    /// misuse is caught. Empty until pass 5 populates it.
    pub app_check_sigs: HashMap<DefId, Scheme>,
    /// CHECK-ONLY pins of a wildcard-`any` RESULT to the body-inferred concrete
    /// type (D1). For an ANNOTATED, non-polymorphic def whose declared result
    /// contains `any` and whose body returns a fully-monomorphic type
    /// (`f : Int -> any; f x = x` → `Int -> Int`), this pins the result so callers
    /// can't absorb it at an arbitrary type (`List.length (f 5)` rejects, matching
    /// the oracle). Same isolation as `check_sigs`/`app_check_sigs`: consulted only
    /// at `!use_inferred`, never by the lowerer → Go emission byte-identical.
    /// Populated by pass 6; empty on the lowerer path.
    pub any_result_check_sigs: HashMap<DefId, Scheme>,
    /// LOWERING-PATH ONLY: full closed-record result types for zero-param,
    /// unannotated top-level defs (D2). Consulted ONLY in the `Expr::Update` arm
    /// on the `use_inferred` (codegen region-type) path, to close the row var when
    /// a record update's base is an unannotated sibling `Res::Def` (which
    /// otherwise resolves to a fresh flex → the update reads back as the SUBSET of
    /// updated fields, while the emitted value is the FULL record → `go build`
    /// mismatch). Never consulted on the check path, so accept/reject + LSP are
    /// byte-identical. Populated by the pass after `any_result_check_sigs`.
    pub record_result_sigs: HashMap<DefId, Scheme>,
    /// Constructor schemes, keyed by constructor name (matches `Res::Ctor`).
    pub ctors: HashMap<String, Scheme>,
    /// Constructor schemes keyed by the ctor's own `DefId` — disambiguates
    /// same-named constructors in different unions (e.g. `Center` in both
    /// `HAlign` and `TextAlign`). `Res::Ctor(cr)` carries `cr.def`.
    pub ctors_by_def: HashMap<DefId, Scheme>,
    /// Constructor name → the union type name it belongs to (exhaustiveness).
    pub ctor_union: HashMap<String, String>,
    /// Union type name → all its constructor names, in declaration order.
    pub union_ctors: HashMap<String, Vec<String>>,
    /// Union type `DefId` → its constructor names (disambiguates same-named
    /// unions across modules, e.g. a `Msg` in each of several modules).
    pub union_members_by_def: HashMap<DefId, Vec<String>>,
    /// Whether a bare type name is a known nominal type (for opaque handling).
    aliases: HashMap<String, AliasDef>,
}

impl World {
    /// Build the FULL world from every loaded module (stdlib + deps + entry) —
    /// declarations (passes 1-4) PLUS the body-derived CHECK/LOWER channels
    /// (passes 5-7). This is what the accept/reject checker (`check_modules`) and
    /// the eager backend consume. The salsa backend splits it: `type_world` uses
    /// only [`World::build_decls`] (body-independent → backdates on app body
    /// edits), while the body-derived channels are demanded per-def
    /// (`record_result_scheme_for`) or rebuilt for the check path
    /// (`check_world`). See `skydb::type_world_query` / `check_world_query`.
    pub fn build(db: &dyn SkyDb) -> World {
        let mut world = World::build_decls(db);

        // ---- pass 5: precise CHECK-ONLY schemes for UNANNOTATED app defs ----
        // (F1c narrow subset — see `infer_app_check_sigs`.) Runs on top of the
        // decls world so it can consult every declaration channel + its own prior
        // topo-order admissions.
        world.infer_app_check_sigs(db);

        // ---- pass 6: wildcard-`any` RESULT pins (D1, check-only) ----
        world.infer_any_result_check_sigs(db);

        // ---- pass 7: full record result types (D2, lowering-path only) ----
        world.infer_record_result_sigs(db);

        world
    }

    /// Build the DECLARATION-ONLY world (passes 1-4): aliases, unions,
    /// annotations, kernel/stdlib pass-3 inference of unannotated combinators, and
    /// the static CHECK-ONLY combinator seeds (pass 4). **Body-independent w.r.t.
    /// APP code** — none of these passes read an app def's body (pass 3 reads only
    /// `Result` pseudo-module bodies in the trusted stdlib), so an app body-only
    /// edit re-executes this to a *value-equal* `World`. That is the backdating
    /// mechanism `skydb::type_world_query` relies on: the body-derived channels
    /// (`app_check_sigs`/`any_result_check_sigs`/`record_result_sigs`, passes 5-7)
    /// are left EMPTY here and supplied lazily/per-def instead of baked into the
    /// aggregate world (which would couple every `infer` to every body).
    pub fn build_decls(db: &dyn SkyDb) -> World {
        let path_to_pseudo: HashMap<&str, &str> = KERNEL_MODULES.iter().copied().collect();

        let mut world = World {
            value_sigs: HashMap::new(),
            inferred_sigs: HashMap::new(),
            kernel_sigs: HashMap::new(),
            check_sigs: HashMap::new(),
            check_kernel_sigs: HashMap::new(),
            app_check_sigs: HashMap::new(),
            any_result_check_sigs: HashMap::new(),
            record_result_sigs: HashMap::new(),
            ctors: HashMap::new(),
            ctors_by_def: HashMap::new(),
            ctor_union: HashMap::new(),
            union_ctors: HashMap::new(),
            union_members_by_def: HashMap::new(),
            aliases: HashMap::new(),
        };
        world.seed_builtin_ctors();

        // ---- pass 1: collect aliases + unions (so signatures can expand) ----
        for m in db.module_ids() {
            let tree = db.module_parse(m).tree();
            for decl in tree.decls() {
                if let ast::Decl::Alias(a) = &decl {
                    if let Some(name) = a.name().map(|t| t.text().to_string()) {
                        let params = decl_type_vars(a.syntax());
                        let body = a.ty().map(|t| ast_type_to_ty(&t)).unwrap_or(Ty::Error);
                        world.aliases.insert(name, AliasDef { params, body });
                    }
                }
            }
        }

        // ---- pass 2: signatures, kernel sigs, union ctors ----
        for m in db.module_ids() {
            let mname = db.module_name(m).to_string();
            let pseudo = path_to_pseudo.get(mname.as_str()).map(|s| s.to_string());
            let tree = db.module_parse(m).tree();
            for decl in tree.decls() {
                match &decl {
                    ast::Decl::TypeAnno(a) => {
                        let Some(name) = a.name().map(|t| t.text().to_string()) else {
                            continue;
                        };
                        let Some(t) = a.ty() else { continue };
                        let raw = ast_type_to_ty(&t);
                        let expanded = world.expand(&raw, 0);
                        let scheme = Scheme::generalize(expanded);
                        let def = intern_value(db, m, &name);
                        world.value_sigs.insert(def, scheme.clone());
                        if let Some(p) = &pseudo {
                            world
                                .kernel_sigs
                                .entry((p.clone(), name.clone()))
                                .or_insert(scheme);
                        }
                    }
                    ast::Decl::Union(u) => {
                        world.record_union(db, m, u);
                    }
                    ast::Decl::Alias(a) => {
                        // a record alias' name doubles as a positional ctor value.
                        if let (Some(name), Some(ast::Type::Record(_))) =
                            (a.name().map(|t| t.text().to_string()), a.ty())
                        {
                            let raw = a.ty().map(|t| ast_type_to_ty(&t)).unwrap_or(Ty::Error);
                            let expanded = world.expand(&raw, 0);
                            let scheme = record_ctor_scheme(&expanded);
                            let def = intern_value(db, m, &name);
                            world.value_sigs.entry(def).or_insert(scheme.clone());
                            // record-alias ctor is also reachable as Res::Ctor
                            world.ctors.entry(name).or_insert(scheme);
                        }
                    }
                    _ => {}
                }
            }
        }

        // ---- pass 3: infer schemes for UNANNOTATED stdlib combinators ----
        // Unannotated kernel/stdlib defs (`Result.map3`, `List.foldl`,
        // `List.map`, `Result.andMap`, …) carry no `name : Type` line, so pass 2
        // never records a scheme and every call site resolves them to a fresh
        // flex var — the result then stays `any` (breaking `.field`/arithmetic on
        // it in emitted Go). Inferring their bodies against the annotated world
        // recovers the real polymorphic signature so application PINS the result
        // from the arg types. Scoped to pseudo (kernel/stdlib) modules only: app
        // code keeps its lenient fresh-flex behaviour, minimising blast radius.
        world.infer_unannotated_kernel(db);

        // ---- pass 4: seed precise CHECK-ONLY List combinator schemes ----
        // Seeded AFTER pass 3 (grill hardening): `check_sigs` must never leak
        // into the pass-3-inferred Result schemes the lowerer consumes. Both run
        // with `use_inferred=false`; today no Result combinator body calls a List
        // HOF, but seed-after makes the isolation structural, not incidental.
        world.seed_check_sigs(db);

        world
    }

    /// Pass 5 (see `build`, F1c narrow subset). Infer + register CHECK-ONLY
    /// monomorphic schemes for unannotated top-level defs in APP (non-kernel)
    /// modules, so a cross-module misuse of such a def (`allCategories + 1`) is
    /// rejected instead of absorbed by a wildcard flex. STRICTLY filtered:
    /// admits only fully-monomorphic, Unit-spine-free types (record-typed sigs
    /// now included; excludes under-generalised polymorphs and `() -> X`
    /// kernel-shim spines — the two remaining accept-parity landmines).
    /// Modules in an import cycle (SCC size > 1) are skipped (fixpoint deferred).
    fn infer_app_check_sigs(&mut self, db: &dyn SkyDb) {
        use crate::infer::Infer;
        let path_to_pseudo: HashMap<&str, &str> = KERNEL_MODULES.iter().copied().collect();

        // App (non-kernel) modules only.
        let app_mods: Vec<ModuleId> = db
            .module_ids()
            .into_iter()
            .filter(|m| {
                let mname = db.module_name(*m).to_string();
                !path_to_pseudo.contains_key(mname.as_str())
            })
            .collect();
        let app_set: std::collections::HashSet<ModuleId> = app_mods.iter().copied().collect();

        // Import DAG among app modules: edge dep -> importer (dependency first).
        // Only Dep imports that target another APP module contribute an edge.
        let mut deps_of: HashMap<ModuleId, Vec<ModuleId>> = HashMap::new();
        for &m in &app_mods {
            let mut ds: Vec<ModuleId> = Vec::new();
            let tree = db.module_parse(m).tree();
            for imp in tree.imports() {
                let Some(path) = imp.name().map(|n| n.text()) else {
                    continue;
                };
                if let hir::ImportSource::Dep(dep) = db.classify_import(&path) {
                    if app_set.contains(&dep) && dep != m && !ds.contains(&dep) {
                        ds.push(dep);
                    }
                }
            }
            deps_of.insert(m, ds);
        }

        // Detect modules in an import cycle (SCC of size > 1) — defer those.
        let in_cycle = modules_in_cycle(&app_mods, &deps_of);

        // Topo order (dependencies before dependents) over the acyclic remainder.
        let order = topo_order(&app_mods, &deps_of, &in_cycle);

        // Infer each unannotated app def in topo order; admit if cleanly usable.
        for m in order {
            let resolved = db.resolve(m);
            let names: HashMap<DefId, String> = resolved
                .top_defs
                .iter()
                .map(|td| (td.def, td.name.as_str().to_string()))
                .collect();
            for (def, body) in &resolved.bodies {
                // Respect annotations: never shadow a declared signature.
                if self.value_sigs.contains_key(def) {
                    continue;
                }
                if !names.contains_key(def) {
                    continue;
                }
                let mut infer = Infer::new(self, db).with_self_def(Some(*def));
                // Checker channel: concretize an unresolved `Number` super to
                // `Int` (oracle-faithful) so a numeric helper infers a
                // MONOMORPHIC sig and stays subject to the F1c monomorphism
                // filter rather than escaping via a spurious `∀a. a -> …`.
                let Some(scheme) = infer.infer_def_scheme(body, true) else {
                    continue;
                };
                // "CLEANLY USABLE" filter (the accept-parity guard). The one
                // surviving clause: no Unit in the param spine — excludes the
                // `() -> X` shims that interact with the zero-arg-call arity gate.
                //
                // HISTORY — two earlier clauses, both LIFTED after empirical P0
                // experiments proved them vestigial (accept-parity held):
                //   * record-free — lifted once record row-polymorphism became
                //     sound + oracle-parity (leniency valve retired);
                //     unlocked cross-module record misuse detection (F1c).
                //   * monomorphic-only (`!scheme.vars.is_empty()`) — lifted here
                //     (gap2). It was meant to guard "under-generalisation", but an
                //     under-generalised scheme only makes the check LOOSER (accept-
                //     more), never a false-reject — so the exclusion cost real
                //     accept-invalid parity: an unannotated POLYMORPHIC helper
                //     (`apply f x = f x`, `compose g f x = g (f x)`, `pair a b =
                //     (a, b)`, `first xs = List.head xs`) escaped caller checking,
                //     so a caller passing a wrong-typed arg (`apply String.fromInt
                //     "s"`) was accepted where the oracle rejects. Admitting the
                //     poly scheme lets the checker instantiate it fresh per call
                //     site and unify the args — standard HM, exactly the oracle's
                //     behaviour. Codegen-neutral (app_check_sigs is read only at
                //     `!use_inferred`; the lowerer never consults it): infer 42/42,
                //     build-run 31/31 match, repro 40/40 byte-stable, reject +5
                //     gap2 locks, and 15+ valid-poly stress cases with zero
                //     false-reject. Numeric helpers stay concretized to `Int` via
                //     infer_def_scheme's concretize_super, so they were already
                //     monomorphic-admitted in baseline — this lift adds only
                //     genuine ∀-quantified helpers.
                if param_spine_contains_unit(&scheme.ty) {
                    continue;
                }
                self.app_check_sigs.entry(*def).or_insert(scheme);
            }
        }
    }

    /// Pass 6 (see `build`). D1 — wildcard-`any` RESULT pins (check-only). For an
    /// ANNOTATED, non-polymorphic APP def whose declared result contains `any`
    /// and whose body returns a fully-monomorphic type, pin that concrete result
    /// into `any_result_check_sigs` so a caller can't absorb the result at an
    /// arbitrary type (`f : Int -> any; f x = x` → `List.length (f 5)` rejects,
    /// matching the oracle). Stdlib modules are skipped (kernel + `Std.`/`Sky.`
    /// namespaces) — the predicate already excludes every stdlib `any`-result def
    /// (they are polymorphic or have polymorphic bodies), and the namespace skip
    /// makes that immunity structural.
    fn infer_any_result_check_sigs(&mut self, db: &dyn SkyDb) {
        use crate::infer::Infer;
        for m in db.module_ids() {
            let mname = db.module_name(m).to_string();
            if KERNEL_MODULES.iter().any(|(p, _)| *p == mname)
                || mname.starts_with("Std.")
                || mname.starts_with("Sky.")
            {
                continue;
            }
            let resolved = db.resolve(m);
            for (def, body) in &resolved.bodies {
                // Clause 1 (annotated) + clause 2 (non-polymorphic: `Scheme::
                // generalize` already drops `any` from `vars`, so `vars` empty ⟺
                // the only free type var is `any`).
                let Some(anno) = self.value_sigs.get(def) else {
                    continue;
                };
                if !anno.vars.is_empty() {
                    continue;
                }
                // Clause 3: the declared RESULT position contains `any`.
                if !result_contains_any(&anno.ty, body.params.len()) {
                    continue;
                }
                let anno_ty = anno.ty.clone();
                // Clause 4: the body-inferred pin must be fully monomorphic (a
                // polymorphic body is left unpinned — the oracle does not pin it
                // either). `infer_any_result_pin` returns `None` otherwise.
                let mut infer = Infer::new(self, db).with_self_def(Some(*def));
                if let Some(scheme) = infer.infer_any_result_pin(body, &anno_ty, true) {
                    self.any_result_check_sigs.insert(*def, scheme);
                }
            }
        }
    }

    /// Pass 7 (see `build`). D2 — record the FULL CLOSED-record result type of
    /// every zero-param, unannotated top-level def. The `Expr::Update` arm
    /// consults this ON THE LOWERING PATH ONLY to close the row when an update's
    /// base is such a def (`bumped = { r | age = 2 }` where `r = {name, age}`):
    /// without it the base resolves to a fresh flex and the update reads back as
    /// the SUBSET `{age}`, while the emitted value is the FULL record → `go build`
    /// mismatch. Uses the byte-faithful lowerer channel (`infer_def_scheme(_,
    /// false)`); admits only a CLOSED record result so nothing else is affected.
    fn infer_record_result_sigs(&mut self, db: &dyn SkyDb) {
        for m in db.module_ids() {
            let resolved = db.resolve(m);
            for (def, body) in &resolved.bodies {
                if let Some(scheme) = self.admit_record_result(db, *def, body) {
                    self.record_result_sigs.insert(*def, scheme);
                }
            }
        }
    }

    /// Pass-7 admission logic for ONE def, using `self` as the inference context
    /// (which MUST be a full check-world — passes 1-6 — so the inferred field
    /// types are byte-identical to what the bulk [`World::infer_record_result_sigs`]
    /// pass computed). A zero-param, unannotated def whose body infers a CLOSED
    /// record is admitted; everything else yields `None`. Shared by the bulk pass
    /// (eager full build) and the per-def salsa query (`record_result_scheme_for`).
    fn admit_record_result(&self, db: &dyn SkyDb, def: DefId, body: &hir::Body) -> Option<Scheme> {
        use crate::infer::Infer;
        // Zero-param + unannotated (an annotated def already resolves to its full
        // record via `value_sigs`).
        if !body.params.is_empty() || self.value_sigs.contains_key(&def) {
            return None;
        }
        let mut infer = Infer::new(self, db).with_self_def(Some(def));
        let scheme = infer.infer_def_scheme(body, false)?;
        matches!(&scheme.ty, Ty::Record(_, None)).then_some(scheme)
    }

    /// Per-def entry point for the D2 record-result scheme (pass 7), demanded
    /// LAZILY on the lowering path (`skydb::record_result_sig_query`) instead of
    /// baked into the aggregate world. `self` MUST be the full check-world
    /// (passes 1-6, e.g. `World::build`) so the value matches the bulk pass
    /// exactly. Fetches the def's own body via `def_loc` + `resolve` (revision-safe
    /// inside a salsa query — every live `DefId` names an interned `DefKey`).
    pub fn record_result_scheme_for(&self, db: &dyn SkyDb, def: DefId) -> Option<Scheme> {
        let loc = db.def_loc(def)?;
        let resolved = db.resolve(loc.module);
        let body = resolved.bodies.get(&def)?;
        self.admit_record_result(db, def, body)
    }

    /// Pass 4 (see `build`). Register precise stdlib combinator schemes into the
    /// CHECK-ONLY channels (`check_sigs` / `check_kernel_sigs`). These pin a
    /// combinator's result element/type from its argument types at accept/reject-
    /// check time (`use_inferred == false`) so an unannotated `List.map` /
    /// `List.reverse` / `fst` / `Maybe.withDefault` no longer falls to a wildcard
    /// flex that silently absorbs a downstream param clash (audit #3:
    /// `String.join "," (List.map String.length xs)`; F8: `String.join ","
    /// (List.reverse [1,2,3])`, `Maybe.withDefault 0 (Dict.get "k" strDict)`,
    /// `String.fromInt (fst ("a", 1))`). The lowerer (`use_inferred == true`)
    /// never reads these channels — emission is byte-identical to the pre-fix
    /// wildcard path.
    ///
    /// SCOPE. Only UNANNOTATED, lenient stdlib combinators are seeded here. The
    /// already-annotated modules (`Dict`, `Set`, `String` — each `.sky` def
    /// carries a `name : Type` line) land in `value_sigs`/`kernel_sigs` at pass 2
    /// and are precise already; the `!value_sigs.contains_key` guard below keeps
    /// this pass from ever shadowing such an annotation. `Result` combinators are
    /// seeded by pass 3 into `inferred_sigs`/`kernel_sigs`, so they are omitted
    /// here too. F8 (this extension) adds the `Sky.Core.List` core ops
    /// (`reverse`/`take`/`drop`/`append`/`concat`/`member`/`range`/`zip`/`length`/
    /// `isEmpty`/`head`/`tail`/`cons`), `Sky.Core.Basics` tuple projections
    /// (`fst`/`snd`), and `Sky.Core.Maybe` core combinators
    /// (`withDefault`/`map`/`andThen`) to the audit-#3 List-HOF set.
    fn seed_check_sigs(&mut self, db: &dyn SkyDb) {
        let a = || Ty::var("a");
        let b = || Ty::var("b");
        let c = || Ty::var("c");
        let d = || Ty::var("d");
        let e = || Ty::var("e");
        let g = || Ty::var("g");
        let x = || Ty::var("x"); // Result error type param
        let bool_ = || Ty::app("Bool", vec![]);
        let int_ = || Ty::app("Int", vec![]);
        let list = |t: Ty| Ty::app("List", vec![t]);
        let maybe = |t: Ty| Ty::app("Maybe", vec![t]);
        let result = |xe: Ty, t: Ty| Ty::app("Result", vec![xe, t]);
        let tup2 = |x: Ty, y: Ty| Ty::Tuple(vec![x, y]);
        let fun = |from: Ty, to: Ty| Ty::Fun(Box::new(from), Box::new(to));

        // Schemes are order-correct against the actual `sky-stdlib/Sky/Core/
        // *.sky` bodies (element-first `foldl`, `Int`-first `take`/`drop`/`range`,
        // predicate-first `filter`/`find`, `def`-first `Maybe.withDefault`,
        // matching the Sky source + the oracle).
        let list_specs: Vec<(&str, Ty)> = vec![
            // audit #3 — List HOFs (unchanged).
            ("map", fun(fun(a(), b()), fun(list(a()), list(b())))),
            ("filter", fun(fun(a(), bool_()), fun(list(a()), list(a())))),
            (
                "foldl",
                fun(fun(a(), fun(b(), b())), fun(b(), fun(list(a()), b()))),
            ),
            (
                "foldr",
                fun(fun(a(), fun(b(), b())), fun(b(), fun(list(a()), b()))),
            ),
            (
                "concatMap",
                fun(fun(a(), list(b())), fun(list(a()), list(b()))),
            ),
            (
                "filterMap",
                fun(fun(a(), maybe(b())), fun(list(a()), list(b()))),
            ),
            ("find", fun(fun(a(), bool_()), fun(list(a()), maybe(a())))),
            ("any", fun(fun(a(), bool_()), fun(list(a()), bool_()))),
            ("all", fun(fun(a(), bool_()), fun(list(a()), bool_()))),
            (
                "indexedMap",
                fun(fun(int_(), fun(a(), b())), fun(list(a()), list(b()))),
            ),
            // F8 — List core ops (element/type-precise, verified against List.sky).
            ("reverse", fun(list(a()), list(a()))),
            ("take", fun(int_(), fun(list(a()), list(a())))),
            ("drop", fun(int_(), fun(list(a()), list(a())))),
            ("append", fun(list(a()), fun(list(a()), list(a())))),
            ("concat", fun(list(list(a())), list(a()))),
            ("member", fun(a(), fun(list(a()), bool_()))),
            ("range", fun(int_(), fun(int_(), list(int_())))),
            ("zip", fun(list(a()), fun(list(b()), list(tup2(a(), b()))))),
            ("length", fun(list(a()), int_())),
            ("isEmpty", fun(list(a()), bool_())),
            ("head", fun(list(a()), maybe(a()))),
            ("tail", fun(list(a()), maybe(list(a())))),
            ("cons", fun(a(), fun(list(a()), list(a())))),
        ];

        // F8 — Basics tuple projections (`fst : (a, b) -> a`, `snd : (a, b) -> b`).
        // F9 — Basics kernel-anchored Int arithmetic. `modBy` / `clamp` are the two
        // Basics kernels the oracle pins as strictly `Int`-monomorphic
        // (Constrain/Expression.hs: `modBy : Int -> Int -> Int`,
        // `clamp : Int -> Int -> Int -> Int`), so unannotated Rust call sites that
        // otherwise fall to a wildcard flex now reject a non-Int arg at CHECK-time
        // with oracle parity (`modBy "x" 5`, `clamp "a" 1 2`, and `clamp`-on-Float,
        // which the oracle also rejects). Deliberately NOT extended to the sibling
        // arithmetic kernels (`min`/`max`/`abs`/`negate`/`sqrt`/`compare`): the
        // oracle is genuinely LENIENT on those (`abs "x"` / `min "a" 2` accept), so
        // pinning them would make Rust stricter than the oracle — a divergence, not
        // a fix. Verified against the absolute-path differential, 2026-07-20.
        let basics_specs: Vec<(&str, Ty)> = vec![
            ("fst", fun(tup2(a(), b()), a())),
            ("snd", fun(tup2(a(), b()), b())),
            ("modBy", fun(int_(), fun(int_(), int_()))),
            ("clamp", fun(int_(), fun(int_(), fun(int_(), int_())))),
        ];

        // F8 — Maybe core combinators (`withDefault : a -> Maybe a -> a`, etc.).
        // map2..5 / andMap / combine are seeded so a cross-module ill-typed
        // payload (`Maybe.map2 f (Just 1) (Just "s")`) is rejected instead of
        // absorbed by a wildcard flex — the oracle rejects it, and these are
        // unannotated in the stdlib so no annotation supplies the sig. Arg order
        // matches the `sky-stdlib/Sky/Core/Maybe.sky` bodies (`andMap ma mfn`).
        let maybe_specs: Vec<(&str, Ty)> = vec![
            ("withDefault", fun(a(), fun(maybe(a()), a()))),
            ("map", fun(fun(a(), b()), fun(maybe(a()), maybe(b())))),
            (
                "andThen",
                fun(fun(a(), maybe(b())), fun(maybe(a()), maybe(b()))),
            ),
            (
                "map2",
                fun(
                    fun(a(), fun(b(), c())),
                    fun(maybe(a()), fun(maybe(b()), maybe(c()))),
                ),
            ),
            (
                "map3",
                fun(
                    fun(a(), fun(b(), fun(c(), d()))),
                    fun(maybe(a()), fun(maybe(b()), fun(maybe(c()), maybe(d())))),
                ),
            ),
            (
                "map4",
                fun(
                    fun(a(), fun(b(), fun(c(), fun(d(), e())))),
                    fun(
                        maybe(a()),
                        fun(maybe(b()), fun(maybe(c()), fun(maybe(d()), maybe(e())))),
                    ),
                ),
            ),
            (
                "map5",
                fun(
                    fun(a(), fun(b(), fun(c(), fun(d(), fun(e(), g()))))),
                    fun(
                        maybe(a()),
                        fun(
                            maybe(b()),
                            fun(maybe(c()), fun(maybe(d()), fun(maybe(e()), maybe(g())))),
                        ),
                    ),
                ),
            ),
            // andMap ma mfn : Maybe a -> Maybe (a -> b) -> Maybe b
            (
                "andMap",
                fun(maybe(a()), fun(maybe(fun(a(), b())), maybe(b()))),
            ),
            // combine : List (Maybe a) -> Maybe (List a)
            ("combine", fun(list(maybe(a())), maybe(list(a())))),
        ];

        // Result combinators — same shape as Maybe but carry the error type `x`
        // through every arg + the result. Unannotated in the stdlib, so seed the
        // check-sigs here for cross-module payload checking (`Result.map2 f (Ok 1)
        // (Ok "s")` must reject). Arg order matches `Sky.Core.Result.sky`
        // (`andMap ra rfn`, `map2 fn ra rb`).
        // NOTE: only the map2..5 / andMap / combine holes are seeded. withDefault
        // / map / andThen / mapError are deliberately LEFT to the existing lenient
        // path — the oracle accepts `Result.withDefault "" (Task.run (loadEnv …))`
        // where the payload is `()` (17-skymon), so pinning `withDefault`'s payload
        // to the default's type would make Rust stricter than the oracle (a
        // divergence, not a fix).
        let result_specs: Vec<(&str, Ty)> = vec![
            (
                "map2",
                fun(
                    fun(a(), fun(b(), c())),
                    fun(result(x(), a()), fun(result(x(), b()), result(x(), c()))),
                ),
            ),
            (
                "map3",
                fun(
                    fun(a(), fun(b(), fun(c(), d()))),
                    fun(
                        result(x(), a()),
                        fun(result(x(), b()), fun(result(x(), c()), result(x(), d()))),
                    ),
                ),
            ),
            (
                "map4",
                fun(
                    fun(a(), fun(b(), fun(c(), fun(d(), e())))),
                    fun(
                        result(x(), a()),
                        fun(
                            result(x(), b()),
                            fun(result(x(), c()), fun(result(x(), d()), result(x(), e()))),
                        ),
                    ),
                ),
            ),
            (
                "map5",
                fun(
                    fun(a(), fun(b(), fun(c(), fun(d(), fun(e(), g()))))),
                    fun(
                        result(x(), a()),
                        fun(
                            result(x(), b()),
                            fun(
                                result(x(), c()),
                                fun(result(x(), d()), fun(result(x(), e()), result(x(), g()))),
                            ),
                        ),
                    ),
                ),
            ),
            // andMap ra rfn : Result x a -> Result x (a -> b) -> Result x b
            (
                "andMap",
                fun(
                    result(x(), a()),
                    fun(result(x(), fun(a(), b())), result(x(), b())),
                ),
            ),
            // combine : List (Result x a) -> Result x (List a)
            (
                "combine",
                fun(list(result(x(), a())), result(x(), list(a()))),
            ),
        ];

        for (pseudo, path, specs) in [
            ("List", "Sky.Core.List", list_specs),
            ("Basics", "Sky.Core.Basics", basics_specs),
            ("Maybe", "Sky.Core.Maybe", maybe_specs),
            ("Result", "Sky.Core.Result", result_specs),
        ] {
            let module = db.module_by_name(path);
            for (name, ty) in specs {
                let scheme = Scheme::generalize(ty);
                // The `Res::Kernel` key (pseudo-module, func): a bare
                // prelude-qualified `List.reverse` / `fst` / `Maybe.withDefault`
                // resolves here.
                self.check_kernel_sigs
                    .entry((pseudo.to_string(), name.to_string()))
                    .or_insert_with(|| scheme.clone());
                // The `Res::Def` key: mirror pass-2's `intern_value` interning so
                // an `import Sky.Core.List as List` call site (resolving to
                // `Res::Def`) finds the same DefId. GUARD: respect an explicit
                // annotation (never shadow `value_sigs`). A kernel-only name with
                // no Sky-source public def leaves an inert DefId key; harmless.
                if let Some(m) = module {
                    let def = intern_value(db, m, name);
                    if !self.value_sigs.contains_key(&def) {
                        self.check_sigs.entry(def).or_insert(scheme);
                    }
                }
            }
        }
    }

    /// Pass 3 (see `build`). Infer + register schemes for unannotated top-level
    /// defs living in kernel/stdlib pseudo-modules. One pass against the
    /// annotated world: a combinator's body only references builtins (`Ok`/`Err`),
    /// its own params, or (leniently) itself — sufficient for `map*`/`fold*`/etc.
    fn infer_unannotated_kernel(&mut self, db: &dyn SkyDb) {
        use crate::infer::Infer;
        let path_to_pseudo: HashMap<&str, &str> = KERNEL_MODULES.iter().copied().collect();

        // Collect (def, pseudo, name, body) for unannotated pseudo-module defs.
        struct Target {
            def: DefId,
            pseudo: String,
            name: String,
            body: hir::Body,
        }
        let mut targets: Vec<Target> = Vec::new();
        for m in db.module_ids() {
            let mname = db.module_name(m).to_string();
            let Some(pseudo) = path_to_pseudo.get(mname.as_str()).map(|s| s.to_string()) else {
                continue; // app / non-kernel module — leave lenient
            };
            // Scope to `Result` combinators (`map2`/`map3`/`map4`/`map5`/
            // `andMap`/…) whose result type is a struct pinned from the ARGUMENT
            // types (a constructor / Result-returning call), correctly typed at
            // the call site. `List a` combinators are deliberately excluded: their
            // list arg is frequently a record FIELD access that the lowerer labels
            // with the erased `[]any` slot, so pinning a concrete element makes a
            // `[]Job` field flow into a `[]any` param without a coercion
            // (`go build` rejects it) — closing that needs broader boundary
            // coercion than is safe to land here (it regressed the stdlib smoke
            // test with a runtime CoerceFailure). Result-only is the safe subset.
            if pseudo != "Result" {
                continue;
            }
            let resolved = db.resolve(m);
            let names: HashMap<DefId, String> = resolved
                .top_defs
                .iter()
                .map(|td| (td.def, td.name.as_str().to_string()))
                .collect();
            for (def, body) in &resolved.bodies {
                if self.value_sigs.contains_key(def) {
                    continue; // already annotated
                }
                let Some(name) = names.get(def).cloned() else {
                    continue;
                };
                targets.push(Target {
                    def: *def,
                    pseudo: pseudo.clone(),
                    name,
                    body: body.clone(),
                });
            }
        }

        // Infer each against the (immutable) annotated world; collect schemes.
        let mut inferred: Vec<(DefId, String, String, Scheme)> = Vec::new();
        for t in &targets {
            let mut infer = Infer::new(self, db).with_self_def(Some(t.def));
            // Lowerer channel (`inferred_sigs`): DO NOT concretize — the emitted
            // Go reads these via `use_inferred` and MUST stay byte-identical.
            if let Some(scheme) = infer.infer_def_scheme(&t.body, false) {
                inferred.push((t.def, t.pseudo.clone(), t.name.clone(), scheme));
            }
        }

        // Register into the INFERENCE-only channels. Both keyings: `Res::Def`
        // (inferred_sigs) and `Res::Kernel` (kernel_sigs) may target these
        // depending on how the name resolved. Deliberately NOT value_sigs — the
        // lowerer must not treat an inferred scheme as a user annotation.
        for (def, pseudo, name, scheme) in inferred {
            self.inferred_sigs
                .entry(def)
                .or_insert_with(|| scheme.clone());
            self.kernel_sigs.entry((pseudo, name)).or_insert(scheme);
        }
    }

    fn record_union(&mut self, db: &dyn SkyDb, m: ModuleId, u: &ast::UnionDecl) {
        let Some(tname) = u.name().map(|t| t.text().to_string()) else {
            return;
        };
        let params = decl_type_vars(u.syntax());
        let result = Ty::App(
            Name::new(&tname),
            params.iter().map(|p| Ty::var(p)).collect(),
        );
        let mut names = Vec::new();
        for var in u.variants() {
            let Some(cn) = var.name().map(|t| t.text().to_string()) else {
                continue;
            };
            let arg_tys: Vec<Ty> = child_types(var.syntax())
                .iter()
                .map(|t| self.expand(&ast_type_to_ty(t), 0))
                .collect();
            let ty = arg_tys
                .into_iter()
                .rev()
                .fold(result.clone(), |acc, a| Ty::Fun(Box::new(a), Box::new(acc)));
            let scheme = Scheme::generalize(ty);
            let cdef = db.intern_def(m, &Name::new(&cn), hir::DefKind::Ctor);
            self.ctors_by_def.insert(cdef, scheme.clone());
            self.ctors.insert(cn.clone(), scheme);
            self.ctor_union.insert(cn.clone(), tname.clone());
            names.push(cn.clone());
        }
        let type_def = db.intern_def(m, &Name::new(&tname), hir::DefKind::TypeCon);
        self.union_members_by_def.insert(type_def, names.clone());
        self.union_ctors.insert(tname, names);
    }

    fn seed_builtin_ctors(&mut self) {
        let a = Ty::var("a");
        let e = Ty::var("e");
        let maybe_a = Ty::app("Maybe", vec![a.clone()]);
        let result_ea = Ty::app("Result", vec![e.clone(), a.clone()]);
        self.ctors.insert(
            "Just".into(),
            Scheme::generalize(Ty::Fun(Box::new(a.clone()), Box::new(maybe_a.clone()))),
        );
        self.ctors
            .insert("Nothing".into(), Scheme::generalize(maybe_a));
        self.ctors.insert(
            "Ok".into(),
            Scheme::generalize(Ty::Fun(Box::new(a.clone()), Box::new(result_ea.clone()))),
        );
        self.ctors.insert(
            "Err".into(),
            Scheme::generalize(Ty::Fun(Box::new(e), Box::new(result_ea))),
        );
        self.ctors
            .insert("True".into(), Scheme::mono(Ty::app("Bool", vec![])));
        self.ctors
            .insert("False".into(), Scheme::mono(Ty::app("Bool", vec![])));
        for (u, cs) in [
            ("Bool", vec!["True", "False"]),
            ("Maybe", vec!["Just", "Nothing"]),
            ("Result", vec!["Ok", "Err"]),
        ] {
            self.union_ctors
                .insert(u.into(), cs.iter().map(|s| s.to_string()).collect());
            for c in cs {
                self.ctor_union.insert(c.into(), u.into());
            }
        }
    }

    /// Public entry to transparent alias expansion (doc 07 §3). Used by the
    /// lowerer to expand a record field's declared type — a field annotated
    /// `List Point` where `type alias Point = (Float, Float)` must render the
    /// tuple, not erase the un-expanded `Point` nominal to `any` (26-ui-showcase /
    /// 37-composite-live-shop: the struct-field decl must agree with the function
    /// signatures, which come pre-expanded from the sig world).
    pub fn expand_ty(&self, ty: &Ty) -> Ty {
        self.expand(ty, 0)
    }

    /// Expand a type transparently through the alias table (record/function
    /// aliases). Guarded against runaway recursion (aliases are non-recursive in
    /// Sky, but a malformed corpus must not hang — L7).
    fn expand(&self, ty: &Ty, depth: u32) -> Ty {
        if depth > 40 {
            return ty.clone();
        }
        match ty {
            Ty::App(name, args) => {
                let args: Vec<Ty> = args.iter().map(|a| self.expand(a, depth + 1)).collect();
                if let Some(def) = self.aliases.get(name.as_str()) {
                    let mut sub: HashMap<String, Ty> = HashMap::new();
                    for (p, arg) in def.params.iter().zip(args.iter()) {
                        sub.insert(p.clone(), arg.clone());
                    }
                    let substituted = substitute(&def.body, &sub);
                    return self.expand(&substituted, depth + 1);
                }
                Ty::App(name.clone(), args)
            }
            Ty::Fun(a, b) => Ty::Fun(
                Box::new(self.expand(a, depth + 1)),
                Box::new(self.expand(b, depth + 1)),
            ),
            Ty::Tuple(xs) => Ty::Tuple(xs.iter().map(|x| self.expand(x, depth + 1)).collect()),
            Ty::Record(fs, ext) => Ty::Record(
                fs.iter()
                    .map(|(n, t)| (n.clone(), self.expand(t, depth + 1)))
                    .collect(),
                ext.clone(),
            ),
            other => other.clone(),
        }
    }
}

/// Collect the `DefId`s that appear as the DIRECT base of a record-update
/// expression (`{ base | ... }` where `base` is `Res::Def`). These are exactly
/// the defs whose D2 `record_result_scheme_for` the lowering path's `Expr::Update`
/// arm consults (`infer.rs`); the salsa `infer_query` demands `record_result_sig`
/// only for these, so a body with no such update creates no dependency on any
/// other def's body (the incremental-backdating property). Mirrors the arm's own
/// guard exactly: only a base that is literally `Expr::Var(Res::Def(d))`.
pub fn update_base_defs(body: &hir::Body) -> Vec<DefId> {
    let mut out = Vec::new();
    for (_, expr) in body.exprs.iter() {
        if let hir::Expr::Update { base, .. } = expr {
            if let hir::Expr::Var(hir::Res::Def(d)) = &body.exprs[*base] {
                if !out.contains(d) {
                    out.push(*d);
                }
            }
        }
    }
    out
}

fn record_ctor_scheme(record: &Ty) -> Scheme {
    // A record alias `type alias R a = { f : ft, ... }` gets a positional ctor
    // `ft -> ... -> { f : ft, ... }` in _fieldIndex (declaration) order. The
    // RESULT is the record itself (aliases are transparent — everything else
    // expands `R` to the record too), so a constructed value unifies with any
    // `R`-typed slot.
    if let Ty::Record(fields, _) = record {
        let ty = fields.iter().rev().fold(record.clone(), |acc, (_, ft)| {
            Ty::Fun(Box::new(ft.clone()), Box::new(acc))
        });
        Scheme::generalize(ty)
    } else {
        Scheme::generalize(record.clone())
    }
}

fn substitute(ty: &Ty, sub: &HashMap<String, Ty>) -> Ty {
    match ty {
        Ty::Var(n) => sub.get(n.as_str()).cloned().unwrap_or_else(|| ty.clone()),
        Ty::Fun(a, b) => Ty::Fun(Box::new(substitute(a, sub)), Box::new(substitute(b, sub))),
        Ty::App(n, args) => Ty::App(n.clone(), args.iter().map(|a| substitute(a, sub)).collect()),
        Ty::Tuple(xs) => Ty::Tuple(xs.iter().map(|x| substitute(x, sub)).collect()),
        Ty::Record(fs, ext) => Ty::Record(
            fs.iter()
                .map(|(n, t)| (n.clone(), substitute(t, sub)))
                .collect(),
            ext.clone(),
        ),
        other => other.clone(),
    }
}

fn intern_value(db: &dyn SkyDb, m: ModuleId, name: &str) -> DefId {
    db.intern_def(m, &Name::new(name), DefKind::Value)
}

/// True iff peeling `n_params` arrows off `ty` leaves a residual whose free vars
/// include `"any"` — i.e. the declared RESULT position carries a wildcard (D1
/// clause 3). Peeling only the param arrows keeps ARGUMENT-position `any`
/// (`any -> any -> Int`) out of scope, preserving its per-occurrence semantics.
fn result_contains_any(ty: &Ty, n_params: usize) -> bool {
    let mut cur = ty;
    for _ in 0..n_params {
        match cur {
            Ty::Fun(_, b) => cur = b,
            _ => break,
        }
    }
    cur.free_vars().iter().any(|n| n.as_str() == "any")
}

// ---- F1c app-check-sig support: cycle detection, topo order, Unit spine ----

/// Does the ARROW PARAM SPINE of `ty` contain a `Ty::Unit` anywhere? Walks each
/// left-hand arrow argument (the param positions) and returns true if any param
/// contains a Unit. The result type is NOT inspected. Used by the F1c filter to
/// exclude `() -> X` kernel-shim spines (the getGitHubClientSecret landmine).
fn param_spine_contains_unit(ty: &Ty) -> bool {
    let mut cur = ty;
    while let Ty::Fun(a, b) = cur {
        if ty_contains_unit(a) {
            return true;
        }
        cur = b;
    }
    false
}

/// Does `ty` contain a `Ty::Unit` anywhere in its structure?
fn ty_contains_unit(ty: &Ty) -> bool {
    match ty {
        Ty::Unit => true,
        Ty::Fun(a, b) => ty_contains_unit(a) || ty_contains_unit(b),
        Ty::App(_, args) | Ty::Tuple(args) => args.iter().any(ty_contains_unit),
        Ty::Record(fields, _) => fields.iter().any(|(_, t)| ty_contains_unit(t)),
        Ty::Var(_) | Ty::Error => false,
    }
}

/// The set of app modules that sit in an import cycle (a strongly-connected
/// component of size > 1 in the dependency graph `deps_of`). These are skipped
/// by the F1c pass — inferring cyclic-module defs needs a fixpoint this narrow
/// subset deliberately defers. Tarjan's SCC over the app-module subgraph.
/// The APP-module import cycles: every strongly-connected component of size > 1
/// (plus self-loops) in the dependency graph `deps_of`, each returned as a group
/// so a diagnostic can name its members. Iterative Tarjan (L7 stack safety).
pub(crate) fn scc_cycle_groups(
    nodes: &[ModuleId],
    deps_of: &HashMap<ModuleId, Vec<ModuleId>>,
) -> Vec<Vec<ModuleId>> {
    #[derive(Default, Clone)]
    struct NodeState {
        index: Option<u32>,
        lowlink: u32,
        on_stack: bool,
    }
    let mut state: HashMap<ModuleId, NodeState> = HashMap::new();
    let mut index_counter: u32 = 0;
    let mut stack: Vec<ModuleId> = Vec::new();
    let mut groups: Vec<Vec<ModuleId>> = Vec::new();

    // Iterative Tarjan (avoids deep recursion on large corpora — L7 safety).
    // Frame carries the node and the index of the next successor to visit.
    for &start in nodes {
        if state.get(&start).and_then(|s| s.index).is_some() {
            continue;
        }
        let mut frames: Vec<(ModuleId, usize)> = vec![(start, 0)];
        while let Some(&(v, succ_i)) = frames.last() {
            if succ_i == 0 {
                let s = state.entry(v).or_default();
                s.index = Some(index_counter);
                s.lowlink = index_counter;
                s.on_stack = true;
                index_counter += 1;
                stack.push(v);
            }
            let succs = deps_of.get(&v).cloned().unwrap_or_default();
            if succ_i < succs.len() {
                // advance this frame's cursor before descending
                frames.last_mut().unwrap().1 += 1;
                let w = succs[succ_i];
                let w_index = state.get(&w).and_then(|s| s.index);
                match w_index {
                    None => frames.push((w, 0)),
                    Some(wi) => {
                        if state.get(&w).map(|s| s.on_stack).unwrap_or(false) {
                            let v_low = state.get(&v).unwrap().lowlink;
                            state.get_mut(&v).unwrap().lowlink = v_low.min(wi);
                        }
                    }
                }
            } else {
                // done with v: pop the frame, form an SCC if v is a root.
                frames.pop();
                let v_state = state.get(&v).unwrap().clone();
                if let Some((parent, _)) = frames.last().copied() {
                    let p_low = state.get(&parent).unwrap().lowlink;
                    state.get_mut(&parent).unwrap().lowlink = p_low.min(v_state.lowlink);
                }
                if Some(v_state.lowlink) == v_state.index {
                    let mut comp: Vec<ModuleId> = Vec::new();
                    while let Some(w) = stack.pop() {
                        state.get_mut(&w).unwrap().on_stack = false;
                        comp.push(w);
                        if w == v {
                            break;
                        }
                    }
                    // size > 1 → cycle; also treat a self-loop as cyclic.
                    let self_loop = deps_of.get(&v).map(|ds| ds.contains(&v)).unwrap_or(false);
                    if comp.len() > 1 || self_loop {
                        groups.push(comp);
                    }
                }
            }
        }
    }
    groups
}

/// Flat set of app modules that sit in an import cycle (the deferral logic reads
/// this; the Elm-like rejection reads the groups). Wrapper over `scc_cycle_groups`.
fn modules_in_cycle(
    nodes: &[ModuleId],
    deps_of: &HashMap<ModuleId, Vec<ModuleId>>,
) -> std::collections::HashSet<ModuleId> {
    scc_cycle_groups(nodes, deps_of)
        .into_iter()
        .flatten()
        .collect()
}

/// Build the APP-module import graph and return its cycles as groups. Same graph
/// + Tarjan the deferral logic in `infer_app_check_sigs` uses, exposed so the
/// checker (`check_modules`) can REJECT cycles (Elm-like posture) rather than
/// silently defer them to wildcard-flex inference.
pub(crate) fn app_import_cycle_groups(db: &dyn SkyDb) -> Vec<Vec<ModuleId>> {
    let path_to_pseudo: HashMap<&str, &str> = KERNEL_MODULES.iter().copied().collect();
    let app_mods: Vec<ModuleId> = db
        .module_ids()
        .into_iter()
        .filter(|m| !path_to_pseudo.contains_key(db.module_name(*m).to_string().as_str()))
        .collect();
    let app_set: std::collections::HashSet<ModuleId> = app_mods.iter().copied().collect();
    let mut deps_of: HashMap<ModuleId, Vec<ModuleId>> = HashMap::new();
    for &m in &app_mods {
        let mut ds: Vec<ModuleId> = Vec::new();
        let tree = db.module_parse(m).tree();
        for imp in tree.imports() {
            let Some(path) = imp.name().map(|n| n.text()) else {
                continue;
            };
            if let hir::ImportSource::Dep(dep) = db.classify_import(&path) {
                if app_set.contains(&dep) && dep != m && !ds.contains(&dep) {
                    ds.push(dep);
                }
            }
        }
        deps_of.insert(m, ds);
    }
    scc_cycle_groups(&app_mods, &deps_of)
}

/// Kahn topo-sort of the app modules (dependencies before dependents), EXCLUDING
/// any node in `skip`. Deterministic: ties broken by module-id order (insertion
/// order, L4). Nodes whose deps are all outside the working set become available
/// immediately. Any residual (should not occur once `skip` removes every cycle)
/// is appended in id order so no def is silently dropped.
fn topo_order(
    nodes: &[ModuleId],
    deps_of: &HashMap<ModuleId, Vec<ModuleId>>,
    skip: &std::collections::HashSet<ModuleId>,
) -> Vec<ModuleId> {
    let working: Vec<ModuleId> = nodes
        .iter()
        .copied()
        .filter(|m| !skip.contains(m))
        .collect();
    let working_set: std::collections::HashSet<ModuleId> = working.iter().copied().collect();

    // remaining in-degree = number of deps still inside the working set.
    let mut indeg: HashMap<ModuleId, usize> = HashMap::new();
    for &m in &working {
        let d = deps_of
            .get(&m)
            .map(|ds| ds.iter().filter(|x| working_set.contains(x)).count())
            .unwrap_or(0);
        indeg.insert(m, d);
    }
    // reverse edges: dep -> [importers] (within working set).
    let mut dependents: HashMap<ModuleId, Vec<ModuleId>> = HashMap::new();
    for &m in &working {
        if let Some(ds) = deps_of.get(&m) {
            for &dep in ds {
                if working_set.contains(&dep) {
                    dependents.entry(dep).or_default().push(m);
                }
            }
        }
    }

    let mut out: Vec<ModuleId> = Vec::new();
    let mut done: std::collections::HashSet<ModuleId> = std::collections::HashSet::new();
    loop {
        // pick all currently-ready nodes in id order for determinism.
        let mut ready: Vec<ModuleId> = working
            .iter()
            .copied()
            .filter(|m| !done.contains(m) && indeg.get(m).copied().unwrap_or(0) == 0)
            .collect();
        ready.sort_by_key(|m| m.0);
        if ready.is_empty() {
            break;
        }
        for m in ready {
            done.insert(m);
            out.push(m);
            if let Some(deps) = dependents.get(&m).cloned() {
                for d in deps {
                    if let Some(e) = indeg.get_mut(&d) {
                        *e = e.saturating_sub(1);
                    }
                }
            }
        }
    }
    // append any residual (defensive; skip should have broken all cycles).
    let mut rest: Vec<ModuleId> = working.into_iter().filter(|m| !done.contains(m)).collect();
    rest.sort_by_key(|m| m.0);
    out.extend(rest);
    out
}

// ---- AST → Ty ------------------------------------------------------------

/// Convert a `syntax::ast::Type` into a `Ty`. Qualified names collapse to their
/// final segment (name-based nominal equality — accept-parity honesty note).
pub fn ast_type_to_ty(t: &ast::Type) -> Ty {
    ast_type_to_ty_impl(t, false)
}

/// Like [`ast_type_to_ty`] but PRESERVES a qualified reference's qualifier in the
/// `Ty::App` name (`Counter.Msg` → `App("Counter.Msg")`) instead of collapsing to
/// the bare final segment. Used ONLY by the Go-emission type collection
/// (`lower::collect_types`) so a cross-module field type resolves to the correct
/// module-prefixed Go type (`Counter_Msg` vs a same-module `Main_Msg`). The
/// inference / sig world keeps calling [`ast_type_to_ty`] (bare) so unification
/// and the read-back oracle stay byte-identical.
pub fn ast_type_to_ty_qualified(t: &ast::Type) -> Ty {
    ast_type_to_ty_impl(t, true)
}

/// Format a qualified reference's name: `"Qual.Name"` when preserving (and a
/// qualifier is present), else the bare `"Name"` (historical collapse).
fn qual_name(preserve_qual: bool, qual: &str, name: &str) -> String {
    if preserve_qual && !qual.is_empty() {
        format!("{qual}.{name}")
    } else {
        name.to_string()
    }
}

fn ast_type_to_ty_impl(t: &ast::Type, preserve_qual: bool) -> Ty {
    match t {
        ast::Type::Var(v) => Ty::var(&first_lower(v.syntax()).unwrap_or_default()),
        ast::Type::Con(c) => Ty::App(
            Name::new(&first_upper(c.syntax()).unwrap_or_default()),
            Vec::new(),
        ),
        ast::Type::Qual(q) => {
            let (qual, name) = dotted_parts(q.syntax());
            Ty::App(
                Name::new(&qual_name(preserve_qual, &qual, &name)),
                Vec::new(),
            )
        }
        ast::Type::App(app) => {
            let parts = child_types(app.syntax());
            let Some((head, rest)) = parts.split_first() else {
                return Ty::Error;
            };
            let args: Vec<Ty> = rest
                .iter()
                .map(|x| ast_type_to_ty_impl(x, preserve_qual))
                .collect();
            match head {
                ast::Type::Con(c) => Ty::App(
                    Name::new(&first_upper(c.syntax()).unwrap_or_default()),
                    args,
                ),
                ast::Type::Qual(q) => {
                    let (qual, name) = dotted_parts(q.syntax());
                    Ty::App(Name::new(&qual_name(preserve_qual, &qual, &name)), args)
                }
                ast::Type::Var(v) => {
                    // higher-kinded var application (`f a`) — no HKT in Sky; keep
                    // the var, drop args (won't matter for accept-parity).
                    let _ = args;
                    Ty::var(&first_lower(v.syntax()).unwrap_or_default())
                }
                other => ast_type_to_ty_impl(other, preserve_qual),
            }
        }
        ast::Type::Fun(f) => {
            let kids = child_types(f.syntax());
            let from = kids
                .first()
                .map(|x| ast_type_to_ty_impl(x, preserve_qual))
                .unwrap_or(Ty::Error);
            let to = kids
                .get(1)
                .map(|x| ast_type_to_ty_impl(x, preserve_qual))
                .unwrap_or(Ty::Error);
            Ty::Fun(Box::new(from), Box::new(to))
        }
        ast::Type::Tuple(t) => Ty::Tuple(
            child_types(t.syntax())
                .iter()
                .map(|x| ast_type_to_ty_impl(x, preserve_qual))
                .collect(),
        ),
        ast::Type::Unit(_) => Ty::Unit,
        ast::Type::Paren(p) => child_types(p.syntax())
            .first()
            .map(|x| ast_type_to_ty_impl(x, preserve_qual))
            .unwrap_or(Ty::Unit),
        ast::Type::Record(r) => {
            let mut fields = Vec::new();
            for field in r
                .syntax()
                .children()
                .filter(|c| c.kind() == SyntaxKind::TypeRecordField)
            {
                let fname = first_lower(&field).unwrap_or_default();
                let fty = child_types(&field)
                    .first()
                    .map(|x| ast_type_to_ty_impl(x, preserve_qual))
                    .unwrap_or(Ty::Error);
                fields.push((Name::new(&fname), fty));
            }
            // Keep DECLARATION order — the positional record-alias constructor
            // binds args by `_fieldIndex`, not alphabetically. Unification sorts
            // independently (FlatTy::Record is a BTreeMap).
            let ext = r
                .syntax()
                .children()
                .find(|c| c.kind() == SyntaxKind::RowVar)
                .and_then(|rv| first_lower(&rv))
                .map(|n| Name::new(&n));
            Ty::Record(fields, ext)
        }
    }
}

/// The argument types of a union variant (for the lowerer's ADT emission).
pub fn variant_arg_types(variant_syntax: &SyntaxNode) -> Vec<Ty> {
    child_types(variant_syntax)
        .iter()
        .map(ast_type_to_ty)
        .collect()
}

/// Like [`variant_arg_types`] but preserves qualifiers on cross-module type
/// references (`Counter.Msg` → `App("Counter.Msg")`) so the Go-emission path can
/// resolve them to the declaring module's Go type. See [`ast_type_to_ty_qualified`].
pub fn variant_arg_types_qualified(variant_syntax: &SyntaxNode) -> Vec<Ty> {
    child_types(variant_syntax)
        .iter()
        .map(ast_type_to_ty_qualified)
        .collect()
}

/// The (field-name, field-type) pairs of a record alias in DECLARATION order
/// (`_fieldIndex` order — the positional ctor's calling convention).
pub fn record_alias_fields(alias_syntax: &SyntaxNode) -> Vec<(String, Ty)> {
    // find the outermost record node (first node carrying TypeRecordField
    // children) and take ITS direct fields — avoids picking up nested records.
    let record = alias_syntax.descendants().find(|n| {
        n.children()
            .any(|c| c.kind() == SyntaxKind::TypeRecordField)
    });
    let mut out = Vec::new();
    if let Some(record) = record {
        for node in record
            .children()
            .filter(|c| c.kind() == SyntaxKind::TypeRecordField)
        {
            let fname = first_lower(&node).unwrap_or_default();
            let fty = child_types(&node)
                .first()
                .map(ast_type_to_ty)
                .unwrap_or(Ty::Error);
            out.push((fname, fty));
        }
    }
    out
}

// ---- CST navigation (mirrors hir::cst; kept local so `ty` is self-contained) --

fn child_types(n: &SyntaxNode) -> Vec<ast::Type> {
    n.children().filter_map(ast::Type::cast).collect()
}

fn first_lower(n: &SyntaxNode) -> Option<String> {
    sig_token(n, SyntaxKind::LowerIdent)
}

fn first_upper(n: &SyntaxNode) -> Option<String> {
    sig_token(n, SyntaxKind::UpperIdent)
}

fn sig_token(n: &SyntaxNode, kind: SyntaxKind) -> Option<String> {
    n.children_with_tokens()
        .filter_map(|e| e.into_token())
        .filter(|t| !t.kind().is_trivia())
        .find(|t| t.kind() == kind)
        .map(|t| t.text().to_string())
}

fn dotted_parts(n: &SyntaxNode) -> (String, String) {
    let idents: Vec<String> = n
        .children_with_tokens()
        .filter_map(|e| e.into_token())
        .filter(|t| matches!(t.kind(), SyntaxKind::UpperIdent | SyntaxKind::LowerIdent))
        .map(|t| t.text().to_string())
        .collect();
    match idents.split_last() {
        Some((last, rest)) if !rest.is_empty() => (rest.join("."), last.clone()),
        Some((last, _)) => (String::new(), last.clone()),
        None => (String::new(), String::new()),
    }
}

fn decl_type_vars(n: &SyntaxNode) -> Vec<String> {
    n.children()
        .find(|c| c.kind() == SyntaxKind::TypeVarList)
        .map(|tvl| {
            tvl.children_with_tokens()
                .filter_map(|e| e.into_token())
                .filter(|t| t.kind() == SyntaxKind::LowerIdent)
                .map(|t| t.text().to_string())
                .collect()
        })
        .unwrap_or_default()
}

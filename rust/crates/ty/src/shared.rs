//! The shared-world corpus path (CI/test-architecture v2 §1.3, route C-2).
//!
//! # Why this exists
//!
//! A `sample(1)` profile of `xtask reject` (10,953 samples, 2026-08-10)
//! decomposed the measured 1.293 s per corpus case as:
//!
//! ```text
//! 0.975 s (75.4 %)  hir::resolve, re-run per stdlib module in resolve_type_names
//! 0.252 s (19.5 %)  the rest of ty::sig::World::build_decls
//! 0.063 s ( 4.9 %)  World::build's passes beyond declarations
//! 0.003 s ( 0.2 %)  the case's own work + SourceDb construction
//! ```
//!
//! The case's own work is 0.2 %. Essentially the entire cost is **re-deriving
//! the same 87-module stdlib world for every case**. `hir::SourceDb`'s `resolve`
//! memo (route C-1) removes the first line. This module removes the second and
//! third: the stdlib world is assembled **once per process**, and each case runs
//! the declaration and body passes over **its own modules only**, folded into a
//! fork of that prebuilt world.
//!
//! # The two correctness hazards, and what is done about them
//!
//! **`DefId` leakage across cases.** `hir::DefTable::intern` keys on
//! `(module.index(), name, kind)` and `ModuleId` is the *insertion index*. Reuse
//! one db across cases and consecutive cases intern the **same `DefId`** for
//! `Main.main` — so every `DefId`-keyed `World` channel (`value_sigs`,
//! `app_check_sigs`, `record_result_sigs`, …) silently carries case *N*'s
//! signature into case *N+1*. In a gate suite that is a soundness bug, not a
//! performance bug: the suite would report verdicts nobody asked for.
//!
//! Defence: every case is checked against a **fork of a pristine base** (the base
//! db and the base world are never mutated), so a case's world contains the
//! base's entries plus its own and nothing else. [`CaseCheck::case_def_ids`]
//! exposes what a case interned, for reporting.
//!
//! **Correction to v2 §1.4(a), measured.** v2 prescribes the gate as *"assert the
//! `DefId` sets they intern are disjoint"*. Forking does **not** make them
//! disjoint, and disjointness is not the property that matters: a fork clones the
//! base interner, so two forks each adding a `Main` at the same next index mint
//! the *same* `DefId` for `Main.main` by construction. The ids coincide across
//! two disjoint universes, which is harmless. The property that must hold — and
//! that the gate in `ty/tests/shared_world.rs` asserts, with an inline falsifier
//! exhibiting the real leak — is that case *N+1*'s **world** carries none of case
//! *N*'s entries.
//!
//! **A prebuilt stdlib world is WRONG for some cases.** Two constructions make
//! the prebuilt world unusable, and both are *detected before forking* and fall
//! back to a full rebuild as a **counted, reported** state — never silently:
//!
//! * [`Fallback::ShadowsStdlibModule`] — `SourceDb::add_module` overwrites on a
//!   name collision, so a case declaring `Std.Log` must not be checked against a
//!   world that already contains the real `Std.Log`'s declarations. This is the
//!   #164 axis and Layer 1 exercises it deliberately.
//! * [`Fallback::BareAliasCollision`] — the bare `aliases` table is
//!   last-writer-wins and is completed (pass 1a) *before* any signature expands
//!   (pass 2). In a whole-program build a case alias colliding on a bare name can
//!   therefore change how a **stdlib** signature expands. A world whose stdlib
//!   pass 2 has already run cannot reproduce that.
//!
//! Anything not detected here is asserted equal by the differential harness
//! (`xtask shared-world-diff`), which compares per-item verdicts between this
//! path and the whole-program path across the full reject and infer corpora.

use crate::sig::World;
use base::{DefId, ModuleId, Name};
use hir::{DefKind, DefLoc, ImportSource, ModuleExports, SkyDb, SourceDb};
use std::collections::HashSet;
use std::rc::Rc;

/// A [`SkyDb`] view that narrows `module_ids()` to a chosen subset while
/// delegating every other query to the underlying db.
///
/// This is the whole mechanism by which a pass becomes incremental. The sig
/// passes iterate `db.module_ids()` to decide *what to process*, but reach every
/// other module through `resolve` / `module_exports` / `classify_import` /
/// `module_by_name` to decide *what things mean*. Narrowing only the first
/// restricts the work without restricting the visibility — so a case module can
/// still import, and be checked against, the whole stdlib.
pub struct ScopedDb<'a> {
    inner: &'a dyn SkyDb,
    ids: Vec<ModuleId>,
}

impl<'a> ScopedDb<'a> {
    pub fn new(inner: &'a dyn SkyDb, ids: Vec<ModuleId>) -> Self {
        ScopedDb { inner, ids }
    }
}

impl SkyDb for ScopedDb<'_> {
    fn module_name(&self, m: ModuleId) -> &str {
        self.inner.module_name(m)
    }
    fn module_parse(&self, m: ModuleId) -> &syntax::Parse {
        self.inner.module_parse(m)
    }
    fn module_by_name(&self, name: &str) -> Option<ModuleId> {
        self.inner.module_by_name(name)
    }
    fn classify_import(&self, path: &str) -> ImportSource {
        self.inner.classify_import(path)
    }
    fn kernel_pseudo(&self, qualifier: &str) -> Option<&str> {
        self.inner.kernel_pseudo(qualifier)
    }
    fn module_exports(&self, m: ModuleId) -> Rc<ModuleExports> {
        self.inner.module_exports(m)
    }
    fn resolve(&self, m: ModuleId) -> Rc<hir::ResolveResult> {
        self.inner.resolve(m)
    }
    /// The narrowed set — the one method that differs.
    fn module_ids(&self) -> Vec<ModuleId> {
        self.ids.clone()
    }
    fn intern_def(&self, module: ModuleId, name: &Name, kind: DefKind) -> DefId {
        self.inner.intern_def(module, name, kind)
    }
    fn def_loc(&self, def: DefId) -> Option<DefLoc> {
        self.inner.def_loc(def)
    }
}

/// Why a case could not use the prebuilt world. Every variant is counted and
/// reported; none is silent.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum Fallback {
    /// A case module's name collides with a base (stdlib) module name. The
    /// prebuilt world contains the shadowed module's declarations, which the case
    /// intends to replace.
    ShadowsStdlibModule,
    /// A case module declares a type alias whose BARE name is already in the
    /// base world's last-writer-wins `aliases` table. See
    /// [`World::has_bare_alias`].
    BareAliasCollision,
}

impl Fallback {
    pub fn label(self) -> &'static str {
        match self {
            Fallback::ShadowsStdlibModule => "shadows-stdlib-module",
            Fallback::BareAliasCollision => "bare-alias-collision",
        }
    }
}

/// How a case's world was obtained. Reported per case so a run can never claim
/// the shared path was taken when it was not.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum WorldSource {
    /// Forked from the prebuilt base world; only the case's modules were
    /// processed.
    Shared,
    /// Rebuilt whole-program because the prebuilt world is not valid for this
    /// case.
    Rebuilt(Fallback),
}

/// A prebuilt base: the stdlib modules, their memoised resolutions, and the
/// world assembled from them.
///
/// The base is **pristine** — `check_case` never mutates it. Each case forks it.
pub struct SharedWorld {
    base_db: SourceDb,
    base_world: World,
    base_module_names: HashSet<String>,
    base_module_count: usize,
}

/// One case's checked result, plus how its world was obtained.
pub struct CaseCheck {
    pub out: crate::CheckOutput,
    pub source: WorldSource,
    /// The `DefId`s this case's own modules interned. Used by the
    /// `corpus.defid-disjoint` gate to prove no leakage between consecutive
    /// cases.
    pub case_def_ids: Vec<DefId>,
}

impl SharedWorld {
    /// Assemble the base from the stdlib module set. Done once per process.
    pub fn new(base_modules: &[(String, syntax::Parse)]) -> SharedWorld {
        let mut base_db = SourceDb::new();
        let mut base_module_names = HashSet::new();
        for (name, parse) in base_modules {
            base_db.add_module(name, parse.clone());
            base_module_names.insert(name.clone());
        }
        // Full whole-program build over the base module set: passes 1-8.
        let base_world = World::build(&base_db);
        let base_module_count = SkyDb::module_ids(&base_db).len();
        SharedWorld {
            base_db,
            base_world,
            base_module_names,
            base_module_count,
        }
    }

    /// Modules in the base (for reporting / the fallback-cap gate).
    pub fn base_module_count(&self) -> usize {
        self.base_module_count
    }

    /// Decide, **before forking**, whether the prebuilt world is valid for this
    /// case. Returns the reason it is not, if any.
    ///
    /// Both checks read only the case's own parses, so this is cheap and cannot
    /// itself perturb the base.
    pub fn fallback_reason(&self, case_modules: &[(String, syntax::Parse)]) -> Option<Fallback> {
        for (name, _) in case_modules {
            if self.base_module_names.contains(name) {
                return Some(Fallback::ShadowsStdlibModule);
            }
        }
        for (_, parse) in case_modules {
            for decl in parse.tree().decls() {
                if let syntax::ast::Decl::Alias(a) = &decl {
                    if let Some(n) = a.name().map(|t| t.text().to_string()) {
                        if self.base_world.has_bare_alias(&n) {
                            return Some(Fallback::BareAliasCollision);
                        }
                    }
                }
            }
        }
        None
    }

    /// Check one case. `case_modules` are the case's own modules (the entry
    /// module last, by convention); `to_check` names the modules to typecheck.
    ///
    /// The base is forked, never mutated — see the `DefId`-leakage note in the
    /// module docs.
    pub fn check_case(
        &self,
        case_modules: &[(String, syntax::Parse)],
        to_check: &[String],
    ) -> CaseCheck {
        match self.fallback_reason(case_modules) {
            Some(reason) => self.check_case_rebuilt(case_modules, to_check, reason),
            None => self.check_case_shared(case_modules, to_check),
        }
    }

    /// A **deliberately wrong** shared check, for falsifying the differential
    /// harness.
    ///
    /// It takes the shared path but skips the case's body-derived passes (5-8),
    /// so the case is checked against a world missing its own `app_check_sigs` /
    /// `any_result_check_sigs` pins. That is precisely the class of mistake an
    /// incremental world can make, and the differential harness must see it. A
    /// harness that reports "identical" for this is not comparing anything.
    ///
    /// Never reachable from `check_case`; only `xtask shared-world
    /// --inject-divergence` calls it.
    pub fn check_case_injected_divergence(
        &self,
        case_modules: &[(String, syntax::Parse)],
        to_check: &[String],
    ) -> CaseCheck {
        let mut db = self.base_db.clone();
        let mut case_ids = Vec::new();
        for (name, parse) in case_modules {
            case_ids.push(db.add_module(name, parse.clone()));
        }
        let mut world = self.base_world.clone();
        {
            let scoped = ScopedDb::new(&db, case_ids.clone());
            world.extend_decls(&scoped, false);
            // extend_bodies deliberately omitted — this is the injected defect.
        }
        let ids: Vec<ModuleId> = to_check
            .iter()
            .filter_map(|n| db.module_by_name(n))
            .collect();
        let case_def_ids = case_def_ids_of(&db, &case_ids);
        let out = crate::check::check_modules_with_world(&db, Rc::new(world), &ids);
        CaseCheck {
            out,
            source: WorldSource::Shared,
            case_def_ids,
        }
    }

    fn check_case_shared(
        &self,
        case_modules: &[(String, syntax::Parse)],
        to_check: &[String],
    ) -> CaseCheck {
        // Fork: the base db (with its memoised stdlib resolutions and its
        // interner) is cloned, so the case's `add_module` calls append at fresh
        // indices in a table no other case shares.
        let mut db = self.base_db.clone();
        let mut case_ids = Vec::new();
        for (name, parse) in case_modules {
            case_ids.push(db.add_module(name, parse.clone()));
        }

        let mut world = self.base_world.clone();
        {
            let scoped = ScopedDb::new(&db, case_ids.clone());
            // Declarations (passes 1-3) for the case's modules. Pass 4 seeds a
            // fixed stdlib table already present in the base.
            world.extend_decls(&scoped, false);
            // Body-derived channels (passes 5-8) for the case's modules.
            world.extend_bodies(&scoped);
        }

        let ids: Vec<ModuleId> = to_check
            .iter()
            .filter_map(|n| db.module_by_name(n))
            .collect();
        let case_def_ids = case_def_ids_of(&db, &case_ids);
        let out = crate::check::check_modules_with_world(&db, Rc::new(world), &ids);
        CaseCheck {
            out,
            source: WorldSource::Shared,
            case_def_ids,
        }
    }

    fn check_case_rebuilt(
        &self,
        case_modules: &[(String, syntax::Parse)],
        to_check: &[String],
        reason: Fallback,
    ) -> CaseCheck {
        // Whole-program: a fresh db over base + case, and a world built from
        // scratch. This is exactly what the non-shared path does.
        let mut db = SourceDb::new();
        for m in self.base_db.module_ids() {
            let name = self.base_db.module_name(m).to_string();
            db.add_module(&name, self.base_db.module_parse(m).clone());
        }
        let mut case_ids = Vec::new();
        for (name, parse) in case_modules {
            case_ids.push(db.add_module(name, parse.clone()));
        }
        let ids: Vec<ModuleId> = to_check
            .iter()
            .filter_map(|n| db.module_by_name(n))
            .collect();
        let case_def_ids = case_def_ids_of(&db, &case_ids);
        let out = crate::check_modules(&db, &ids);
        CaseCheck {
            out,
            source: WorldSource::Rebuilt(reason),
            case_def_ids,
        }
    }
}

/// Every `DefId` interned for a top-level def of the case's own modules.
fn case_def_ids_of(db: &SourceDb, case_ids: &[ModuleId]) -> Vec<DefId> {
    let mut out = Vec::new();
    for m in case_ids {
        for td in SkyDb::resolve(db, *m).top_defs.iter() {
            out.push(td.def);
        }
    }
    out.sort();
    out.dedup();
    out
}

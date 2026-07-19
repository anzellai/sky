//! `skydb` — the salsa database + the leaf queries. Ties inputs → parse → …;
//! the place "the whole compiler" is assembled (doc 02).
//!
//! **Stage A + Stage B (doc 12 §M0→M1, doc 01 "all edges are salsa queries").**
//! The M0 spike (one `source_text` input + a trivial `line_count` query) is now
//! promoted to the real leaf of the query DAG:
//!
//! * **Stage A — inputs.** The source set is modelled as salsa inputs: one
//!   [`SourceFile`] `#[salsa::input]` per module, holding `(file_id, text)` —
//!   the salsa-native encoding of the module set the hand-rolled
//!   `hir::db::SourceDb` used to hold in a `Vec<ModuleInfo>`. The driver
//!   `set_*`s these; everything downstream is a pure, memoised function of them.
//! * **Stage B — `parse` leaf query.** [`parse`] is `#[salsa::tracked]`: a pure,
//!   memoised function of a `SourceFile` input, matching the doc-01 data-flow
//!   node `parse(FileId) -> Lossless CST + parse diagnostics`.
//!
//! **Salsa version: real `salsa` 0.28** (the current jar-less API). Inputs are
//! salsa structs; tracked fns are the derived queries. `#![forbid(unsafe_code)]`
//! is deliberately absent from THIS crate (only): the salsa proc-macros expand
//! to `unsafe impl`s. Every frontend crate that authors compiler logic keeps
//! `forbid` — salsa is quarantined here, which is why the leaf queries live in
//! `skydb` rather than in the forbid-clean `hir`/`ty`/`lower` that consume them
//! (see the Stage-B report / doc 12 for the layering the resolve stage resolves).

use base::{DefId, ModuleId, Name};
use hir::{compute_exports, DefKind, DefLoc, ImportSource, ModuleExports, SkyDb};
use std::collections::HashMap;
use std::rc::Rc;

/// A registered module: its dotted name and the [`SourceFile`] input it parses
/// from. Indexed by `ModuleId` (the vector position), mirroring the eager
/// `hir::SourceDb`'s registry so `ModuleId` semantics are identical across both
/// database backends.
struct ModuleReg {
    name: String,
    file: SourceFile,
}

/// The Sky query database. Its only mutable state is salsa's storage plus the
/// append-only module registry (L1) — no global `IORef`/`unsafePerformIO`
/// equivalents anywhere in the compiler. The registry holds the module set
/// (dotted-name → `ModuleId` → `SourceFile` input) that the salsa queries key
/// off; it is set once by the driver at assembly time and read-only thereafter.
#[salsa::db]
#[derive(Default, Clone)]
pub struct SkyDatabase {
    storage: salsa::Storage<Self>,
    modules: Vec<ModuleReg>,
    by_name: HashMap<String, ModuleId>,
    kernel: HashMap<String, String>,
}

impl Clone for ModuleReg {
    fn clone(&self) -> Self {
        ModuleReg {
            name: self.name.clone(),
            file: self.file,
        }
    }
}

#[salsa::db]
impl salsa::Database for SkyDatabase {}

/// A definition's content-keyed identity — the salsa-native successor to the
/// hand-rolled `hir::DefTable` `IndexSet` interner (the resolve-stage purity
/// blocker). Keyed by `(module, name, kind)`, exactly the old interner's key, so
/// a value and a type of the same name in the same module stay distinct. Being
/// `#[salsa::interned]`, the identity is content-derived and order-independent by
/// construction: a pure `#[salsa::tracked]` query can mint one without mutating
/// shared state. The raw `salsa::Id` index is exposed as the compiler-wide
/// [`base::DefId`] `u32` so downstream (`ty`/`lower`/`sky-lsp`) is unchanged.
///
/// Determinism note: `DefId` *values* differ from the old insertion-order
/// scheme, but no consumer keys emitted output off the raw int (audited: codegen
/// never sees a `DefId`; every output ordering keys off names/spans/`Ty`). The
/// `repro` gate is the byte-stability guard for this change.
#[salsa::interned(no_lifetime)]
pub struct DefKey {
    #[returns(copy)]
    pub module: u32,
    pub name: String,
    #[returns(copy)]
    pub kind: DefKind,
}

/// Mint / recover the stable `DefId` for `(module, name, kind)` via salsa interning.
fn intern_def_id(db: &dyn salsa::Database, module: ModuleId, name: &Name, kind: DefKind) -> DefId {
    let key = DefKey::new(db, module.index(), name.as_str().to_string(), kind);
    DefId(salsa::plumbing::AsId::as_id(&key).index())
}

/// Recover a definition's `(module, name, kind)` location from its `DefId`.
/// Every `DefId` in circulation was minted by [`intern_def_id`], so the id always
/// names a live interned `DefKey`.
fn def_id_loc(db: &dyn salsa::Database, def: DefId) -> DefLoc {
    // Safe in practice: `def.0` is the index of a `DefKey` this db interned.
    let id = unsafe { salsa::Id::from_index(def.0) };
    let key = <DefKey as salsa::plumbing::FromId>::from_id(id);
    DefLoc {
        module: ModuleId(key.module(db)),
        name: Name::new(key.name(db)),
        kind: key.kind(db),
    }
}

/// `module_exports` as a `#[salsa::tracked]` query (doc 05 §7, §8) — the salsa
/// successor to `hir::SourceDb::module_exports` + its deleted `RefCell` memo.
/// Pure + memoised: a module's exports are a function of that module's own parse
/// (+ its `exposing` clause), never recursing into other modules, so the
/// cross-module query graph stays cycle-free (no 5-round fixpoint, L2). Interning
/// runs through salsa's content-keyed `DefKey` (the closure), so this is a pure
/// tracked query with no shared-`RefCell` mutation (the resolve-stage purity fix).
///
/// `no_eq`: `ModuleExports` has no `PartialEq`, matching `parse` — backdating is
/// a later LSP-incrementality refinement, not required on the cold build path.
///
/// Keyed by `(module, file)`: `file` (a salsa input) carries the memoisation +
/// invalidation edge (editing the file recomputes this query and nothing else),
/// while `module` pins the `ModuleId` the interned `DefKey`s mint under — kept
/// explicit rather than derived from `file_id` so it stays correct even when a
/// local module shadows a stdlib one (the override case reuses the shadowed
/// `ModuleId` but a distinct `SourceFile`).
#[salsa::tracked(no_eq)]
pub fn module_exports(
    db: &dyn salsa::Database,
    module: ModuleId,
    file: SourceFile,
) -> ModuleExports {
    let p = parse(db, file);
    let tree = p.tree();
    compute_exports(module, &tree, &mut |m, n, k| intern_def_id(db, m, n, k))
}

/// A source file's text keyed by its `file_id` — the Stage-A **input** (doc 01
/// `source_text(FileId)`). One per module; the module set is the collection of
/// these inputs. The driver `set_*`s them; every query is a pure function of
/// them (L2 — editing one file invalidates only what transitively depends on it).
#[salsa::input]
pub struct SourceFile {
    /// The interned file id (matches `base::FileId`'s raw index).
    #[returns(copy)]
    pub file_id: u32,
    /// The full source text. `returns(ref)` so the `parse` query borrows it
    /// rather than cloning the whole module body every call.
    #[returns(ref)]
    pub text: String,
}

/// The `parse` leaf query (Stage B, doc 01 / doc 04). Pure + memoised: a
/// `SourceFile` input → its lossless CST + parse diagnostics. Never panics;
/// broken input yields `ERROR` nodes + diagnostics inside the returned `Parse`
/// (L7, L8). This is the salsa-tracked successor to the driver's inline
/// `syntax::parse(text, FileId)` call.
///
/// `no_eq`: `syntax::Parse` (a rowan `GreenNode` + `Vec<Diagnostic>`) has no
/// `PartialEq`, so salsa cannot backdate on it. Correctness is unaffected — the
/// cold build path parses each file exactly once; the LSP rebuilds per request.
/// Wiring backdating (an `Eq` on `Parse`, or a green-node identity compare) is a
/// future incremental-LSP refinement, not a Stage-B requirement.
#[salsa::tracked(no_eq)]
pub fn parse(db: &dyn salsa::Database, file: SourceFile) -> syntax::Parse {
    syntax::parse(file.text(db), base::FileId(file.file_id(db)))
}

impl SkyDatabase {
    /// A fresh db with the kernel pseudo-module table populated (`Std.Db`,
    /// `Sky.Core.List`, …) — the salsa-backed peer of `hir::SourceDb::new`.
    pub fn with_kernel() -> Self {
        SkyDatabase {
            kernel: hir::KERNEL_MODULES
                .iter()
                .map(|(k, v)| ((*k).to_string(), (*v).to_string()))
                .collect(),
            ..Default::default()
        }
    }

    /// Mint a [`SourceFile`] input for a module's text. `file_id` is the module's
    /// eventual `ModuleId` index (the driver assigns them in load order); the
    /// `parse` + `module_exports` queries key off the returned handle. Creation is
    /// `&self` (salsa input), so this composes before the `&mut self`
    /// [`add_module`] registration.
    pub fn new_source(&self, file_id: u32, text: String) -> SourceFile {
        SourceFile::new(self, file_id, text)
    }

    /// Register a parsed module under its dotted name. A later add with the same
    /// name overrides (local modules shadow stdlib) — identical to
    /// `hir::SourceDb::add_module`.
    pub fn add_module(&mut self, name: &str, file: SourceFile) -> ModuleId {
        if let Some(&id) = self.by_name.get(name) {
            self.modules[id.index() as usize].file = file;
            return id;
        }
        let id = ModuleId(self.modules.len() as u32);
        self.modules.push(ModuleReg {
            name: name.to_string(),
            file,
        });
        self.by_name.insert(name.to_string(), id);
        id
    }

    /// The [`SourceFile`] input backing a registered module (for reading its
    /// parse-error diagnostics off the driver's build path).
    pub fn source_file(&self, m: ModuleId) -> SourceFile {
        self.modules[m.index() as usize].file
    }

    /// Intern an ordered module set as Stage-A [`SourceFile`] inputs. Returns the
    /// handles in input order, so a caller may treat position `i` as the module's
    /// ordinal (matching `base::ModuleId(i)`). Interning is `&self` (salsa input
    /// creation), so a shared reference to the db suffices.
    pub fn intern_module_set<I>(&self, sources: I) -> Vec<SourceFile>
    where
        I: IntoIterator<Item = String>,
    {
        sources
            .into_iter()
            .enumerate()
            .map(|(i, text)| SourceFile::new(self, i as u32, text))
            .collect()
    }
}

/// The salsa-backed implementation of the resolution-database interface. Every
/// method the forbid-clean frontend (`hir::resolve`, `ty`, `lower`) calls routes
/// to a salsa query or the append-only registry — the same surface
/// `hir::SourceDb` implements eagerly, so swapping one for the other is invisible
/// to every call site. This is the seam that lets salsa storage live here (out
/// of the `#![forbid(unsafe_code)]` crates) while the query authors stay clean.
impl SkyDb for SkyDatabase {
    fn module_name(&self, m: ModuleId) -> &str {
        &self.modules[m.index() as usize].name
    }
    fn module_parse(&self, m: ModuleId) -> &syntax::Parse {
        parse(self, self.modules[m.index() as usize].file)
    }
    fn module_by_name(&self, name: &str) -> Option<ModuleId> {
        self.by_name.get(name).copied()
    }
    fn classify_import(&self, path: &str) -> ImportSource {
        if let Some(id) = self.by_name.get(path) {
            return ImportSource::Dep(*id);
        }
        if let Some(pseudo) = self.kernel.get(path) {
            return ImportSource::Kernel(pseudo.clone());
        }
        ImportSource::Foreign(path.to_string())
    }
    fn kernel_pseudo(&self, qualifier: &str) -> Option<&str> {
        self.kernel.get(qualifier).map(String::as_str)
    }
    fn module_exports(&self, m: ModuleId) -> Rc<ModuleExports> {
        Rc::new(module_exports(self, m, self.modules[m.index() as usize].file).clone())
    }
    fn module_ids(&self) -> Vec<ModuleId> {
        (0..self.modules.len() as u32).map(ModuleId).collect()
    }
    fn intern_def(&self, module: ModuleId, name: &Name, kind: DefKind) -> DefId {
        intern_def_id(self, module, name, kind)
    }
    fn def_loc(&self, def: DefId) -> Option<DefLoc> {
        Some(def_id_loc(self, def))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn one_input_one_parse_query_end_to_end() {
        let db = SkyDatabase::default();
        let file = SourceFile::new(&db, 0, "module Main exposing (main)\n\nmain = 1\n".to_string());
        // The tracked query returns a reference to the memoised value in 0.28.
        let p = parse(&db, file);
        assert_eq!(p.error_node_count(), 0);
        // L8 losslessness through the salsa boundary: reprint == input text.
        assert_eq!(p.reprint(), "module Main exposing (main)\n\nmain = 1\n");
    }

    #[test]
    fn parse_through_salsa_matches_direct_parse() {
        // Byte-for-byte determinism gate at the leaf: routing a parse through the
        // salsa input+query must produce the identical CST the driver's inline
        // `syntax::parse` produced (the invariant the build path relies on).
        let src = "module M exposing (..)\n\ntype T = A | B\n\nf x =\n    case x of\n        A -> 1\n        B -> 2\n";
        let db = SkyDatabase::default();
        let file = SourceFile::new(&db, 3, src.to_string());
        let via_salsa = parse(&db, file);
        let direct = syntax::parse(src, base::FileId(3));
        assert_eq!(via_salsa.reprint(), direct.reprint());
        assert_eq!(via_salsa.error_node_count(), direct.error_node_count());
    }

    #[test]
    fn intern_module_set_preserves_order() {
        let db = SkyDatabase::default();
        let files = db.intern_module_set(["module A\n".to_string(), "module B\n".to_string()]);
        assert_eq!(files.len(), 2);
        assert_eq!(files[0].file_id(&db), 0);
        assert_eq!(files[1].file_id(&db), 1);
        assert_eq!(parse(&db, files[0]).reprint(), "module A\n");
        assert_eq!(parse(&db, files[1]).reprint(), "module B\n");
    }
}

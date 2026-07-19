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

/// The Sky query database. Its only mutable state is salsa's storage (L1) — no
/// global `IORef`/`unsafePerformIO` equivalents anywhere in the compiler.
#[salsa::db]
#[derive(Default, Clone)]
pub struct SkyDatabase {
    storage: salsa::Storage<Self>,
}

#[salsa::db]
impl salsa::Database for SkyDatabase {}

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

//! `skydb` — the salsa database + every query. Ties inputs → parse → resolve →
//! infer → lower; the only place "the whole compiler" is assembled (doc 02).
//!
//! This is the M0 salsa spike (doc 12 §M0, risk #M2): prove one input +
//! one derived query end-to-end so the query-engine architecture (doc 01,
//! laws L1 "the db *is* the state" and L2 "incremental for free") is wired
//! before any subsystem lands.
//!
//! **Salsa version: real `salsa` 0.28** (the current jar-less API), de-risked by
//! a standalone probe during M0. Inputs are salsa structs; tracked fns are the
//! derived queries. The full query DAG (doc 01) is threaded in M1+.

/// The Sky query database. Its only mutable state is salsa's storage (L1) — no
/// global `IORef`/`unsafePerformIO` equivalents anywhere in the compiler.
#[salsa::db]
#[derive(Default, Clone)]
pub struct SkyDatabase {
    storage: salsa::Storage<Self>,
}

#[salsa::db]
impl salsa::Database for SkyDatabase {}

/// The single M0 **input**: a source file's text keyed by its `file_id`.
///
/// This is the salsa-native encoding of `source_text(FileId) -> String` from
/// the data-flow diagram (doc 01). The driver `set_*`s inputs; everything else
/// is a pure, memoised function of them.
#[salsa::input]
pub struct SourceFile {
    /// The interned file id (matches `base::FileId`'s raw index).
    pub file_id: u32,
    /// The full source text.
    pub text: String,
}

/// The single M0 **derived query**: a trivial pure function of the input,
/// memoised + auto-invalidated by salsa. Proves the incremental core (L2).
///
/// M1 replaces this trivial body with the real `parse` query and grows the DAG
/// (parse → resolve → infer → lower) exactly as doc 01 lays it out.
#[salsa::tracked]
pub fn line_count(db: &dyn salsa::Database, file: SourceFile) -> usize {
    file.text(db).lines().count()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn one_input_one_query_end_to_end() {
        let db = SkyDatabase::default();
        let file = SourceFile::new(&db, 0, "module Main\n\nmain = 1\n".to_string());
        // Tracked queries return a reference to the memoised value in 0.28.
        assert_eq!(*line_count(&db, file), 3);
    }
}

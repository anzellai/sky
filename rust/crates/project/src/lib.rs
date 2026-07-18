//! `project` — `sky.toml`, module discovery, dependency graph, the driver that
//! runs the build + `go build`, stdlib embedding (doc 02, doc 08, doc 09).
//!
//! M0 stub: the driver entry point exists and assembles the query db. It sets an
//! input and reads a query end-to-end — proving the CLI/LSP-shared engine wiring
//! (doc 01). Real module discovery + `go build` land in M4/M5.

mod build;
pub use build::{build_example, emit_example_source, BuildOptions, BuildReport};

use skydb::{line_count, SkyDatabase, SourceFile};

/// The build driver's project handle. Owns the salsa db — the single state
/// holder (L1). The CLI and LSP are two front-ends over this same db (doc 01).
pub struct Project {
    db: SkyDatabase,
}

impl Default for Project {
    fn default() -> Self {
        Project::new()
    }
}

impl Project {
    pub fn new() -> Self {
        Project {
            db: SkyDatabase::default(),
        }
    }

    /// M0 smoke: set the source-text input for a file and read a derived query
    /// back, proving inputs → queries flow through the db (doc 01). M5 replaces
    /// this with real module discovery + lowering + `go build`.
    pub fn analyze(&mut self, file_id: u32, text: &str) -> usize {
        let file = SourceFile::new(&self.db, file_id, text.to_string());
        *line_count(&self.db, file)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn driver_runs_a_query_through_the_db() {
        let mut p = Project::new();
        assert_eq!(p.analyze(0, "a\nb\n"), 2);
    }
}

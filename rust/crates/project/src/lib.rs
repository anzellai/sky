//! `project` — `sky.toml`, module discovery, dependency graph, the driver that
//! runs the build + `go build`, stdlib embedding (doc 02, doc 08, doc 09).
//!
//! M0 stub: the driver entry point exists and assembles the query db. It sets an
//! input and reads a query end-to-end — proving the CLI/LSP-shared engine wiring
//! (doc 01). Real module discovery + `go build` land in M4/M5.

mod abi_guard;
mod build;
mod doc;
mod driver;
mod ffi_ops;
pub use build::{build_example, build_project, emit_example_source, BuildOptions, BuildReport};
pub use doc::{list_modules, render_doc_site, render_module};
pub use driver::{
    assets_root_for, is_compiler_repo_root, module_name_from_path, project_dir_for, repo_root_for,
    run_app,
};
/// Re-exported so `sky init` can scaffold an embedded template (`CLAUDE.md`)
/// when running standalone, outside the repo tree (doc 09 §E).
pub use ffi::extract_template;
pub use ffi_ops::{
    add as ffi_add, add_sky as ffi_add_sky, install as ffi_install, remove as ffi_remove,
    remove_sky as ffi_remove_sky, update as ffi_update, FfiReport,
};

use skydb::{parse, SkyDatabase, SourceFile};

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

    /// Stage-B smoke: set the source-text input for a file and pull the `parse`
    /// leaf query through the db, proving inputs → queries flow (doc 01). Returns
    /// the parsed module's `ERROR`-node count (0 for well-formed input). The
    /// build path (`build::assemble_and_emit_with`) drives the same input+query.
    pub fn analyze(&self, file_id: u32, text: &str) -> usize {
        let file = SourceFile::new(&self.db, file_id, text.to_string());
        parse(&self.db, file).error_node_count()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn driver_runs_a_query_through_the_db() {
        let p = Project::new();
        assert_eq!(p.analyze(0, "module Main exposing (main)\n\nmain = 1\n"), 0);
    }
}

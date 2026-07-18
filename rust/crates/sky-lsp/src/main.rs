//! `sky-lsp` — the LSP server over the *same* `skydb` (doc 02, doc 10). Per
//! law L2, the LSP is not a special case: it is the query engine with a
//! different driver. Hover/goto/completion/diagnostics/rename land in M6.
//!
//! M0 stub: version/help only, exercising the `skydb`/`ty`/`project`/`tower-lsp`
//! dependency edges so the DAG is proven to link. No async server yet.

use project::Project;
use tower_lsp::lsp_types::ServerCapabilities;

const VERSION: &str = "sky-lsp (rust bring-up) v0.0.0-m0";

fn main() {
    // Touch each downstream crate so the DAG is exercised (M0 wiring).
    let mut p = Project::new();
    let _ = p.analyze(0, "module Main\n");
    let _ = ty::Ty::Unit; // touch the `ty` crate (M3 replaced `infer_stub`)
    // The LSP shares the query db; capabilities are declared in M6.
    let _caps = ServerCapabilities::default();

    println!("{VERSION}");
    println!("(M0 skeleton — LSP will drive the same skydb as the CLI, per L2)");
}

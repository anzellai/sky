//! `sky` — the CLI binary: build/run/check/fmt/doc/test/watch/add/etc.
//! (doc 02, doc 10). A thin front-end over the `project` query db (doc 01) —
//! the LSP is the *same* engine with a different driver.
//!
//! M0 stub: version/help only, exercising the `project`/`fmt`/`testrunner`
//! dependency edges so the DAG is proven to link.

use fmt::format_source;
use project::Project;
use testrunner::run_stub;

const VERSION: &str = "sky (rust bring-up) v0.0.0-m0";

fn main() {
    let args: Vec<String> = std::env::args().skip(1).collect();
    match args.first().map(String::as_str) {
        Some("--version") | Some("version") => println!("{VERSION}"),
        _ => {
            // Touch each downstream crate so the DAG is exercised (M0 wiring).
            let mut p = Project::new();
            let lines = p.analyze(0, "module Main\n");
            let _ = format_source("module Main");
            let _ = run_stub(&[]);
            println!("{VERSION}");
            println!("usage: sky <build|run|check|fmt|doc|test|watch> [args]");
            println!("(M0 skeleton — analyzed {lines} line(s) through the query db)");
        }
    }
}

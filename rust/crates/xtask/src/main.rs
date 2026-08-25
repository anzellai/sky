//! `xtask` — dev automation: the M1 round-trip gate over the `examples/` corpus,
//! plus stubs for the differential-test (`diff`) and reproducibility (`repro`)
//! gates (doc 02, docs 11, 12).
//!
//! `xtask roundtrip` walks every `*.sky` file under `examples/` (excluding the
//! generated `sky-out/`, `.skycache/`, `.skydeps/` dirs), parses it, and asserts
//! the two M1 invariants:
//!   1. byte-exact round-trip: `reprint(green_tree) == source_bytes` (L8);
//!   2. zero `ERROR` nodes (the parser structured every construct).

mod build_run_gate;
mod ci_scan;
mod coerce_floor_gate;
mod config_matrix;
mod config_migrate_gate;
mod config_migration_gate;
mod config_surface;
mod corpus;
mod corpus_bench;
mod coverage_ledger;
mod denominators_gate;
mod divergences_gate;
mod fmt_gate;
mod fuzz_gate;
mod harness;
mod infer_gate;
mod lsp_gate;
mod reject_gate;
mod repro_gate;
mod resolve_gate;
mod s8_gate;
mod shared_world_gate;
mod welltyped_gate;

#[cfg(test)]
mod gate_manifest_test;

use std::path::{Path, PathBuf};

const VERSION: &str = "xtask (rust bring-up) v0.1.0-m1";

/// A subcommand entry point: argv tail (everything after the gate name) → exit
/// code.
type GateFn = fn(&[String]) -> i32;

/// The SINGLE source of truth for the xtask subcommand surface.
///
/// Both `main`'s dispatch and the `usage:` line are derived from this table, so
/// the help text can never drift from what actually runs. `gate_manifest_test`
/// additionally asserts that every gate name referenced from
/// `.github/workflows/**` and `scripts/**` appears here — a typo'd or renamed
/// gate in CI (`coerce_floor` for `coerce-floor`) fails `cargo test -p xtask`
/// instead of silently becoming a no-op step.
///
/// `--version` / `version` are handled separately in `main` (they are flags,
/// not gates, and must not appear in the gate usage list).
const GATES: &[(&str, GateFn)] = &[
    ("roundtrip", roundtrip),
    ("resolve", |args| resolve_gate::run(args, &repo_root())),
    ("infer", |args| infer_gate::run(args, &repo_root())),
    ("reject", |args| reject_gate::run(args, &repo_root())),
    ("build-run", |args| build_run_gate::run(args, &repo_root())),
    ("coerce-floor", |args| {
        coerce_floor_gate::run(args, &repo_root())
    }),
    ("divergences", |args| {
        divergences_gate::run(args, &repo_root())
    }),
    ("fmt", |args| fmt_gate::run(args, &repo_root())),
    ("fuzz", |args| fuzz_gate::run(args, &repo_root())),
    ("welltyped", |args| welltyped_gate::run(args, &repo_root())),
    ("repro", |args| repro_gate::run(args, &repo_root())),
    ("s8", |args| s8_gate::run(args, &repo_root())),
    ("lsp", |args| lsp_gate::run(args, &repo_root())),
    ("shared-world", |args| {
        shared_world_gate::run(args, &repo_root())
    }),
    ("corpus-bench", |args| {
        corpus_bench::run(args, &repo_root())
    }),
    ("corpus", |args| corpus::run(args, &repo_root())),
    ("denominators", |args| {
        denominators_gate::run(args, &repo_root())
    }),
    ("coverage-ledger", |args| {
        coverage_ledger::run(args, &repo_root())
    }),
    ("config-surface", |args| {
        config_surface::run(args, &repo_root())
    }),
    ("config-matrix", |args| {
        config_matrix::run(args, &repo_root())
    }),
    ("config-migration", |args| {
        config_migration_gate::run(args, &repo_root())
    }),
    ("config-migrate", |args| {
        config_migrate_gate::run(args, &repo_root())
    }),
    ("harness", |args| harness::run(args, &repo_root())),
    ("errloc", errloc),
    ("diff", diff_stub),
];

/// A stub must not report success. `xtask diff` is the differential gate's
/// name; wiring it into CI while it does nothing would give a permanently green
/// step that verifies nothing.
fn diff_stub(_args: &[String]) -> i32 {
    eprintln!("xtask diff: NOT IMPLEMENTED (stub) — would shell stage-0 + rust over the corpus");
    2
}

/// The `usage:` line, derived from [`GATES`] so it cannot drift from dispatch.
fn usage() -> String {
    let names: Vec<&str> = GATES.iter().map(|(name, _)| *name).collect();
    format!("usage: xtask <{}> [args]", names.join("|"))
}

fn main() {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let code = match args.first().map(String::as_str) {
        Some("--version") | Some("version") => {
            println!("{VERSION}");
            0
        }
        // An unrecognised subcommand MUST NOT exit 0. Every CI gate is invoked
        // as `cargo run -q -p xtask -- <name>`; while the fallback arm returned
        // 0, a typo'd or renamed gate ("coerce_floor" for "coerce-floor")
        // became a no-op that CI reported green — the gate silently stopped
        // running and nothing said so. Verified: `xtask coerce_floor` printed
        // usage and exited 0.
        other => match other.and_then(|name| GATES.iter().find(|(n, _)| *n == name)) {
            Some((_, run)) => run(&args[1..]),
            None => {
                eprintln!("{VERSION}");
                match other {
                    Some(name) => eprintln!("xtask: unknown subcommand `{name}`"),
                    None => eprintln!("xtask: no subcommand given"),
                }
                eprintln!("{}", usage());
                2
            }
        },
    };
    std::process::exit(code);
}

/// Locate the repo root by walking up from the crate manifest until an
/// `examples/` dir is found.
fn repo_root() -> PathBuf {
    let manifest = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    let mut dir = manifest.as_path();
    loop {
        if dir.join("examples").is_dir() {
            return dir.to_path_buf();
        }
        match dir.parent() {
            Some(p) => dir = p,
            None => return PathBuf::from("."),
        }
    }
}

fn is_generated(path: &Path) -> bool {
    path.components().any(|c| {
        matches!(
            c.as_os_str().to_str(),
            // Must match the coverage-ledger scanner's skip-set — a directory one
            // scanner enters and the other skips makes the roundtrip count differ
            // between a tree that has run the examples (so `.split/`, `sky-out-rust/`,
            // `sky-ffi/` exist — `.split/` alone holds ~24 spa-split `.sky` files)
            // and CI's fresh checkout. That drift showed up as roundtrip 203 local
            // vs 187 on CI.
            Some("sky-out")
                | Some("sky-out-rust")
                | Some(".skycache")
                | Some(".skydeps")
                | Some(".split")
                | Some("sky-ffi")
                | Some("node_modules")
        )
    })
}

/// Recursively collect `*.sky` regular files under `dir`, skipping generated
/// directories. Deterministic (sorted) order.
fn collect_sky(dir: &Path, out: &mut Vec<PathBuf>) {
    let mut entries: Vec<PathBuf> = match std::fs::read_dir(dir) {
        Ok(rd) => rd.filter_map(|e| e.ok().map(|e| e.path())).collect(),
        Err(_) => return,
    };
    entries.sort();
    for path in entries {
        if is_generated(&path) {
            continue;
        }
        if path.is_dir() {
            collect_sky(&path, out);
        } else if path.extension().and_then(|e| e.to_str()) == Some("sky") {
            out.push(path);
        }
    }
}

pub(crate) struct FileResult {
    pub(crate) rel: String,
    pub(crate) ok_roundtrip: bool,
    pub(crate) error_nodes: usize,
    pub(crate) diags: usize,
}

impl FileResult {
    /// The gate's per-file assertion: byte-exact reprint AND zero ERROR nodes.
    pub(crate) fn ok(&self) -> bool {
        self.ok_roundtrip && self.error_nodes == 0
    }
}

/// Parse + reprint every corpus file and return one [`FileResult`] each.
///
/// Extracted from [`roundtrip`] so `xtask harness`'s `roundtrip` gate consults
/// the SAME corpus discovery and the SAME two invariants as the CLI gate
/// (v2 §10 — one `collect_sky`, never a second copy that can drift).
pub(crate) fn roundtrip_scan(root: &Path) -> Vec<FileResult> {
    let examples = root.join("examples");
    let mut files = Vec::new();
    collect_sky(&examples, &mut files);

    let mut results = Vec::with_capacity(files.len());
    for path in &files {
        let src = match std::fs::read_to_string(path) {
            Ok(_s) => _s,
            Err(_) => {
                results.push(FileResult {
                    rel: rel(root, path),
                    ok_roundtrip: false,
                    error_nodes: usize::MAX,
                    diags: 0,
                });
                continue;
            }
        };
        let parse = syntax::parse(&src, base::FileId(0));
        let reprint = parse.reprint();
        results.push(FileResult {
            rel: rel(root, path),
            ok_roundtrip: reprint == src,
            error_nodes: parse.error_node_count(),
            diags: parse.errors().len(),
        });
    }
    results
}

fn roundtrip(args: &[String]) -> i32 {
    let verbose = args.iter().any(|a| a == "-v" || a == "--verbose");
    let root = repo_root();
    let examples = root.join("examples");

    let results = roundtrip_scan(&root);
    if results.is_empty() {
        eprintln!(
            "xtask roundtrip: no .sky files found under {}",
            examples.display()
        );
        return 1;
    }

    // ---- report ----
    let name_w = results
        .iter()
        .map(|r| r.rel.len())
        .max()
        .unwrap_or(4)
        .max(4);
    println!(
        "{:<width$}  {:>10}  {:>11}  {:>6}",
        "FILE",
        "ROUNDTRIP",
        "ERROR_NODES",
        "DIAGS",
        width = name_w
    );
    println!("{}", "-".repeat(name_w + 33));

    let mut total_err_nodes = 0usize;
    let mut rt_ok = 0usize;
    let mut failing: Vec<&FileResult> = Vec::new();
    for r in &results {
        let rt = if r.ok_roundtrip { "ok" } else { "MISMATCH" };
        if r.ok_roundtrip {
            rt_ok += 1;
        }
        if r.error_nodes != usize::MAX {
            total_err_nodes += r.error_nodes;
        }
        let is_fail = !r.ok_roundtrip || r.error_nodes > 0;
        if is_fail {
            failing.push(r);
        }
        if verbose || is_fail {
            let en = if r.error_nodes == usize::MAX {
                "read-err".to_string()
            } else {
                r.error_nodes.to_string()
            };
            println!(
                "{:<width$}  {:>10}  {:>11}  {:>6}",
                r.rel,
                rt,
                en,
                r.diags,
                width = name_w
            );
        }
    }

    println!("{}", "-".repeat(name_w + 33));
    println!(
        "TOTALS: {}/{} round-trip byte-exact | {} total ERROR nodes | {} files",
        rt_ok,
        results.len(),
        total_err_nodes,
        results.len()
    );

    let gate = rt_ok == results.len() && total_err_nodes == 0;
    if gate {
        println!("M1 GATE: PASS  (100% round-trip, zero error nodes)");
        0
    } else {
        println!(
            "M1 GATE: FAIL  ({} files fail round-trip or contain error nodes)",
            failing.len()
        );
        1
    }
}

/// Print each ERROR node in a file: line:col + the error text + the enclosing
/// context. Debug aid for closing the M1 gate.
fn errloc(args: &[String]) -> i32 {
    let Some(file) = args.first() else {
        eprintln!("usage: xtask errloc <file.sky> [limit]");
        return 1;
    };
    let limit: usize = args.get(1).and_then(|s| s.parse().ok()).unwrap_or(10);
    let src = match std::fs::read_to_string(file) {
        Ok(s) => s,
        Err(e) => {
            eprintln!("read error: {e}");
            return 1;
        }
    };
    let parse = syntax::parse(&src, base::FileId(0));
    let mut shown = 0;
    for node in parse.syntax().descendants() {
        if node.kind() != syntax::SyntaxKind::Error {
            continue;
        }
        let range = node.text_range();
        let start: usize = range.start().into();
        let (line, col) = line_col(&src, start);
        let text: String = node.text().to_string();
        let snippet: String = text.chars().take(60).collect();
        // enclosing parent kind for context
        let parent = node
            .parent()
            .map(|pn| format!("{:?}", pn.kind()))
            .unwrap_or_default();
        println!("{file}:{line}:{col}  ERROR in {parent}  text={snippet:?}");
        shown += 1;
        if shown >= limit {
            println!("... (showing first {limit})");
            break;
        }
    }
    if shown == 0 {
        println!("no ERROR nodes in {file}");
    }
    0
}

fn line_col(src: &str, offset: usize) -> (usize, usize) {
    let mut line = 1;
    let mut col = 1;
    for (i, ch) in src.char_indices() {
        if i >= offset {
            break;
        }
        if ch == '\n' {
            line += 1;
            col = 1;
        } else {
            col += 1;
        }
    }
    (line, col)
}

fn rel(root: &Path, path: &Path) -> String {
    path.strip_prefix(root)
        .unwrap_or(path)
        .to_string_lossy()
        .into_owned()
}

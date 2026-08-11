//! `xtask infer` — the M3 type-inference gate over the `examples/` corpus.
//!
//! For every example it parses + resolves + typechecks the entry module + its
//! Sky-source dep modules, loading the stdlib for signatures, and counts
//! TYPE-ERROR diagnostics (unify clashes — severity Error). Exhaustiveness
//! warnings (E3001) are reported separately, never as type errors.
//!
//! The corpus splits into NON-FFI examples (typechecked) and FFI examples
//! (05-mux-server, 11-fyne-stopwatch, 13-skyshop — need the doc-09 FFI surface,
//! not built; reported as BLOCKED).
//!
//! M3 PRIMARY GATE (accept-parity): zero type errors on the NON-FFI corpus — the
//! Haskell oracle compiles every example clean, so any type error the Rust
//! checker emits on an accepted program is a false-positive bug.

use base::ModuleId;
use hir::SourceDb;
use std::path::{Path, PathBuf};

/// Examples that need the Go-FFI surface (doc 09) to typecheck — skipped.
const FFI_BLOCKED: &[&str] = &["05-mux-server", "11-fyne-stopwatch", "13-skyshop"];

pub fn run(args: &[String], root: &Path) -> i32 {
    if let Some(file) = args.iter().find(|a| a.starts_with("--file=")) {
        return infer_file(file.trim_start_matches("--file="), root);
    }
    let verbose = args.iter().any(|a| a == "-v" || a == "--verbose");
    let dump = args
        .iter()
        .find(|a| a.starts_with("--dump="))
        .map(|a| a.trim_start_matches("--dump=").to_string());

    let stdlib = load_dir(&root.join("sky-stdlib"), "sky-stdlib");
    if stdlib.is_empty() {
        eprintln!(
            "infer: no stdlib modules under {}/sky-stdlib",
            root.display()
        );
        return 1;
    }

    let examples_root = root.join("examples");
    let mut example_dirs: Vec<PathBuf> = std::fs::read_dir(&examples_root)
        .map(|rd| {
            rd.filter_map(|e| e.ok().map(|e| e.path()))
                .filter(|p| p.is_dir())
                .collect()
        })
        .unwrap_or_default();
    example_dirs.sort();

    let mut rows: Vec<Row> = Vec::new();
    for dir in &example_dirs {
        let name = dir
            .file_name()
            .and_then(|s| s.to_str())
            .unwrap_or("?")
            .to_string();
        // Load ONLY the example's `src/` tree — the app modules the real build
        // resolves from `src/Main.sky`. Recursing the whole example dir wrongly
        // pulled in `test-fixtures/*.sky` (standalone stress fixtures with their
        // own `update`/records) + stray root files, which the build never checks;
        // that made this differential gate diverge from `sky check` (a fixture's
        // record-subset pattern false-positived under the precise List sigs while
        // the real app was clean). `build_run_gate` already scopes to `src/`.
        let src_dir = dir.join("src");
        let load_root = if src_dir.is_dir() {
            src_dir
        } else {
            dir.clone()
        };
        let locals = load_dir(&load_root, "src");
        if locals.is_empty() {
            continue;
        }
        let blocked = FFI_BLOCKED.contains(&name.as_str());
        if blocked {
            rows.push(Row {
                name,
                modules: locals.len(),
                type_errors: 0,
                warnings: 0,
                blocked: true,
                messages: Vec::new(),
            });
            continue;
        }
        rows.push(check_example(&name, &locals, &stdlib, dump.as_deref()));
    }

    print_table(&rows, verbose);
    verdict(&rows)
}

/// Debug: typecheck one file against the stdlib, printing types + errors.
fn infer_file(file: &str, root: &Path) -> i32 {
    let stdlib = load_dir(&root.join("sky-stdlib"), "sky-stdlib");
    let mut db = SourceDb::new();
    for (n, parse) in &stdlib {
        db.add_module(n, parse.clone());
    }
    // An unreadable path used to become an empty module, which typechecks
    // clean — so `xtask infer --file=/nonexistent.sky` printed
    // "---- errors (0) ----" and exited 0. Say what happened instead.
    let src = match std::fs::read_to_string(file) {
        Ok(s) => s,
        Err(e) => {
            eprintln!("xtask infer --file={file}: {e}");
            return 2;
        }
    };
    let parse = syntax::parse(&src, base::FileId(0));
    let name = parse
        .tree()
        .module_header()
        .and_then(|h| h.name())
        .map(|n| n.text())
        .filter(|s| !s.is_empty())
        .unwrap_or_else(|| "Main".to_string());
    let mid = db.add_module(&name, parse);
    let out = ty::check_modules(&db, &[mid]);
    println!("---- types ----");
    for dt in &out.def_types {
        let tag = if dt.declared { "decl " } else { "infer" };
        println!("  [{tag}] {} : {}", dt.name, dt.ty.render());
    }
    println!("---- errors ({}) ----", out.type_errors);
    for d in &out.diagnostics {
        if d.severity == diagnostics::Severity::Error {
            println!("  {}", d.message);
        }
    }
    println!("---- warnings ({}) ----", out.exhaustiveness_warnings);
    for d in &out.diagnostics {
        if d.severity == diagnostics::Severity::Warning {
            println!("  {}", d.message);
        }
    }
    // Report the verdict in the exit status too. This arm returned a bare 0,
    // so a file full of type errors was indistinguishable from a clean one to
    // any caller that checked the status rather than reading the output.
    if out.type_errors > 0 { 1 } else { 0 }
}

struct Row {
    name: String,
    modules: usize,
    type_errors: usize,
    warnings: usize,
    blocked: bool,
    messages: Vec<String>,
}

fn check_example(
    name: &str,
    locals: &[(String, syntax::Parse)],
    stdlib: &[(String, syntax::Parse)],
    dump: Option<&str>,
) -> Row {
    let mut db = SourceDb::new();
    for (n, parse) in stdlib {
        db.add_module(n, parse.clone());
    }
    let mut local_ids: Vec<ModuleId> = Vec::new();
    for (n, parse) in locals {
        local_ids.push(db.add_module(n, parse.clone()));
    }

    let out = ty::check_modules(&db, &local_ids);

    if dump == Some(name) {
        println!("\n---- inferred types for [{name}] ----");
        for dt in &out.def_types {
            let tag = if dt.declared { "decl" } else { "infer" };
            println!("  [{tag}] {}.{} : {}", dt.module, dt.name, dt.ty.render());
        }
        println!("---- end [{name}] ----\n");
    }

    let messages: Vec<String> = out
        .diagnostics
        .iter()
        .filter(|d| {
            d.severity == diagnostics::Severity::Error
                || d.severity == diagnostics::Severity::Warning
        })
        .map(|d| match d.severity {
            diagnostics::Severity::Warning => format!("(warn) {}", d.message),
            _ => d.message.clone(),
        })
        .collect();

    Row {
        name: name.to_string(),
        modules: locals.len(),
        type_errors: out.type_errors,
        warnings: out.exhaustiveness_warnings,
        blocked: false,
        messages,
    }
}

fn print_table(rows: &[Row], verbose: bool) {
    let w = rows.iter().map(|r| r.name.len()).max().unwrap_or(8).max(8);
    println!(
        "{:<w$}  {:>7}  {:>11}  {:>8}  {:>7}",
        "EXAMPLE",
        "MODULES",
        "TYPE_ERRORS",
        "WARNINGS",
        "STATUS",
        w = w
    );
    println!("{}", "-".repeat(w + 42));
    for r in rows {
        let status = if r.blocked {
            "BLOCKED"
        } else if r.type_errors == 0 {
            "clean"
        } else {
            "ERRORS"
        };
        let te = if r.blocked {
            "-".to_string()
        } else {
            r.type_errors.to_string()
        };
        let wn = if r.blocked {
            "-".to_string()
        } else {
            r.warnings.to_string()
        };
        println!(
            "{:<w$}  {:>7}  {:>11}  {:>8}  {:>7}",
            r.name,
            r.modules,
            te,
            wn,
            status,
            w = w
        );
        if r.type_errors > 0 || (verbose && !r.messages.is_empty()) {
            let limit = if verbose { usize::MAX } else { 6 };
            for m in r.messages.iter().take(limit) {
                println!("      · {m}");
            }
            if !verbose && r.messages.len() > limit {
                println!("      … (+{} more; -v for all)", r.messages.len() - limit);
            }
        }
    }
}

fn verdict(rows: &[Row]) -> i32 {
    let non_ffi: Vec<&Row> = rows.iter().filter(|r| !r.blocked).collect();
    let clean = non_ffi.iter().filter(|r| r.type_errors == 0).count();
    let total = non_ffi.len();
    let total_errors: usize = non_ffi.iter().map(|r| r.type_errors).sum();
    let total_warnings: usize = non_ffi.iter().map(|r| r.warnings).sum();
    let blocked: Vec<&str> = rows
        .iter()
        .filter(|r| r.blocked)
        .map(|r| r.name.as_str())
        .collect();

    println!("{}", "-".repeat(60));
    println!(
        "NON-FFI: {clean}/{total} examples clean | {total_errors} type errors | {total_warnings} exhaustiveness warnings"
    );
    println!("FFI-BLOCKED ({}): {}", blocked.len(), blocked.join(", "));

    if total_errors == 0 {
        println!("M3 GATE: PASS  (zero type errors on the non-FFI corpus — accept-parity)");
        0
    } else {
        println!(
            "M3 GATE: FAIL  ({total_errors} type errors across {} examples — false positives to close)",
            non_ffi.iter().filter(|r| r.type_errors > 0).count()
        );
        1
    }
}

// ---- module loading (mirrors resolve_gate) -------------------------------

fn load_dir(dir: &Path, root_marker: &str) -> Vec<(String, syntax::Parse)> {
    let mut files = Vec::new();
    collect_sky(dir, &mut files);
    let mut out = Vec::new();
    for path in files {
        let Ok(src) = std::fs::read_to_string(&path) else {
            continue;
        };
        let parse = syntax::parse(&src, base::FileId(0));
        let name = module_name(&parse, &path, root_marker);
        out.push((name, parse));
    }
    out
}

fn module_name(parse: &syntax::Parse, path: &Path, root_marker: &str) -> String {
    let tree = parse.tree();
    if let Some(n) = tree
        .module_header()
        .and_then(|h| h.name())
        .map(|n| n.text())
    {
        if !n.is_empty() {
            return n;
        }
    }
    let comps: Vec<&str> = path.iter().filter_map(|c| c.to_str()).collect();
    let start = comps
        .iter()
        .rposition(|c| *c == root_marker)
        .map(|i| i + 1)
        .unwrap_or(0);
    let mut segs: Vec<String> = comps[start..].iter().map(|s| s.to_string()).collect();
    if let Some(last) = segs.last_mut() {
        *last = last.trim_end_matches(".sky").to_string();
    }
    segs.join(".")
}

fn is_generated(path: &Path) -> bool {
    path.components().any(|c| {
        matches!(
            c.as_os_str().to_str(),
            Some("sky-out") | Some(".skycache") | Some(".skydeps")
        )
    })
}

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

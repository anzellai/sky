//! v0.19 kernel-surface gate.
//!
//! The kernel-only modules migrated to Layer-3 `.sky` in v0.19 (their app config
//! became a typed builder) are now the SINGLE source of truth for their
//! signatures + docs — read by the type-checker, LSP hover, and `sky doc`. This
//! gate makes drift a build error instead of a CLAUDE.md hope:
//!
//!   * each module MUST keep its `.sky` source and expose its key bindings
//!     (guards against an accidental delete / rename), and
//!   * every `Ffi.kernel "Sym"` alias it declares MUST have a matching
//!     `func Sym(` in `runtime-go/rt` (guards against a renamed/removed runtime
//!     symbol that would otherwise only break at runtime).
//!
//! Scoped to the migrated modules on purpose: a blanket scan of all 400+ stdlib
//! `Ffi.kernel` symbols has legitimate exceptions (e.g. `Task.run` / `succeed`
//! are lowered specially, not runtime funcs), which would false-positive here.

use std::fs;
use std::path::{Path, PathBuf};

fn repo_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .ancestors()
        .nth(3)
        .expect("repo root (rust/crates/project -> repo)")
        .to_path_buf()
}

/// Canonical kernel-only modules migrated to Layer-3 `.sky`, with the bindings
/// each MUST expose. Add a row here when a new kernel-only module is migrated.
const KERNEL_SURFACE: &[(&str, &[&str])] = &[
    (
        "sky-stdlib/Std/Jobs.sky",
        &["define", "enqueue", "enqueueIn", "cancel"],
    ),
    (
        "sky-stdlib/Std/Live.sky",
        &["app", "config", "route", "api", "lifecycle", "withHead"],
    ),
    (
        "sky-stdlib/Std/Tui.sky",
        &["app", "program", "config", "withOnKey"],
    ),
    (
        "sky-stdlib/Std/Cli.sky",
        &["program", "config", "withOnLine"],
    ),
    (
        "sky-stdlib/Std/Db/Schema.sky",
        &["table", "text", "bigInt", "createTable", "createSchema"],
    ),
    (
        "sky-stdlib/Std/Db/Table.sky",
        &["table", "primaryKey", "createTable", "all", "insert", "findBy", "enum", "codec"],
    ),
    (
        "sky-stdlib/Std/Db/Store.sky",
        &["fromCodec", "primaryKey", "create", "insert", "all", "findBy"],
    ),
];

/// Extract every `Ffi.kernel "Sym"` symbol from a `.sky` source.
fn ffi_symbols(src: &str) -> Vec<String> {
    let needle = "Ffi.kernel \"";
    let mut out = Vec::new();
    let mut rest = src;
    while let Some(i) = rest.find(needle) {
        rest = &rest[i + needle.len()..];
        if let Some(j) = rest.find('"') {
            out.push(rest[..j].to_string());
            rest = &rest[j + 1..];
        } else {
            break;
        }
    }
    out
}

/// True if any `runtime-go/rt/*.go` file defines `func <sym>(` (build tags are
/// irrelevant — a plain text scan finds it in every tagged variant).
fn runtime_defines(root: &Path, sym: &str) -> bool {
    let needle = format!("func {sym}(");
    let dir = root.join("runtime-go/rt");
    fs::read_dir(&dir)
        .expect("runtime-go/rt")
        .filter_map(Result::ok)
        .any(|e| {
            let p = e.path();
            p.extension().map_or(false, |x| x == "go")
                && fs::read_to_string(&p).map_or(false, |s| s.contains(&needle))
        })
}

#[test]
fn migrated_kernel_modules_have_sky_source_and_runtime_symbols() {
    let root = repo_root();
    for (rel, bindings) in KERNEL_SURFACE {
        let path = root.join(rel);
        let src = fs::read_to_string(&path)
            .unwrap_or_else(|e| panic!("{rel} must exist as Layer-3 .sky source: {e}"));

        for b in *bindings {
            assert!(
                src.contains(&format!("\n{b} :")) || src.contains(&format!("\n{b} =")),
                "{rel} must declare binding `{b}` (v0.19 kernel-surface gate — \
                 a migrated kernel module may not silently drop it)"
            );
        }

        let syms = ffi_symbols(&src);
        assert!(
            !syms.is_empty(),
            "{rel} must declare its bindings via `Ffi.kernel \"…\"`"
        );
        for sym in syms {
            assert!(
                runtime_defines(&root, &sym),
                "{rel}: Ffi.kernel \"{sym}\" has NO `func {sym}(` in runtime-go/rt — \
                 a renamed/removed runtime symbol would break at runtime; keep them \
                 in sync (v0.19 kernel-surface gate)"
            );
        }
    }
}

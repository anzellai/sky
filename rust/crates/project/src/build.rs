//! The `sky build` driver (doc 08 §"What codegen guarantees", doc 09 §A.1):
//! parse + resolve + typecheck + lower + emit → write `sky-out/main.go` + `go.mod`,
//! materialise a pruned copy of `runtime-go/rt` beside it, then run `go build`.
//! The runtime tree is copied wholesale, never regenerated (L10).

use hir::SourceDb;
use std::path::{Path, PathBuf};
use std::process::Command;

/// Where a build writes + what to do after emission.
pub struct BuildOptions {
    pub repo_root: PathBuf,
    pub example_dir: PathBuf,
    /// Output dir name under the example (kept distinct from the oracle's
    /// `sky-out/` so a comparison harness can hold both).
    pub out_dir_name: String,
    pub run: bool,
    /// stdin to feed the binary when `run` is set.
    pub stdin: Option<String>,
}

#[derive(Default)]
pub struct BuildReport {
    pub emitted: bool,
    pub go_build_ok: bool,
    pub go_build_stderr: String,
    pub warnings: Vec<String>,
    pub run_ok: Option<bool>,
    pub run_stdout: Option<String>,
    pub run_stderr: Option<String>,
    pub note: String,
}

/// The product of assembling the source db, lowering, and emitting Go — the
/// pure (no-IO-side-effect beyond reading source) front half of a build. Shared
/// by [`build_example`] (which then writes + `go build`s) and
/// [`emit_example_source`] (which only wants the emitted bytes, e.g. the
/// reproducibility gate).
struct Emitted {
    source: String,
    registry: ffi::FfiRegistry,
    ffi_used: std::collections::BTreeSet<String>,
    warnings: Vec<String>,
}

/// Assemble the source db (stdlib + example src), lower, and emit the Go source.
/// Returns `Err(note)` for every non-emit outcome (no stdlib, no src, no entry,
/// no `main`) so callers surface the same diagnostics. Deterministic: no wall
/// clock, no environment reads reach the emitted bytes.
fn assemble_and_emit(repo_root: &Path, example_dir: &Path) -> Result<Emitted, String> {
    // ---- assemble the source db (stdlib + example src) ----
    let mut db = SourceDb::new();
    let stdlib = load_dir(&repo_root.join("sky-stdlib"));
    if stdlib.is_empty() {
        return Err("no stdlib under sky-stdlib".into());
    }
    for (n, parse) in stdlib {
        db.add_module(&n, parse);
    }
    let locals = load_dir(&example_dir.join("src"));
    if locals.is_empty() {
        return Err("no .sky under src/".into());
    }
    let mut entry = None;
    for (n, parse) in locals {
        let id = db.add_module(&n, parse);
        if n == "Main" || n.ends_with(".Main") || n == "main" {
            entry = Some(id);
        }
    }
    let Some(entry) = entry else {
        return Err("no entry module named Main".into());
    };

    // ---- lower + emit ----
    let mut cfg = read_sky_toml_config(&example_dir.join("sky.toml"));
    // Load the pinned Go-FFI surface (doc 09): the committed `sky-ffi/`
    // directory is preferred; the oracle's gitignored `.skycache/` cache is the
    // fallback so a project that hasn't yet migrated to the committed layout
    // still builds. Absent both → an empty table (no FFI).
    let registry = load_ffi_surface(example_dir);
    cfg.ffi = build_ffi_table(&registry);
    let out = lower::lower_program_cfg(&db, entry, &cfg);
    if !out.entry_ok {
        return Err("lowering found no entry `main`".into());
    }
    let source = codegen::emit_program(&out.items);
    Ok(Emitted {
        source,
        registry,
        ffi_used: out.ffi_used,
        warnings: out.warnings,
    })
}

/// Emit the Go source for an example without writing anything or running
/// `go build`. Used by the reproducibility gate (`xtask repro`), which runs this
/// in a fresh process per sample so any `HashMap`/`HashSet` iteration that
/// reaches emitted output surfaces as a byte diff across runs (L4).
pub fn emit_example_source(repo_root: &Path, example_dir: &Path) -> Result<String, String> {
    assemble_and_emit(repo_root, example_dir).map(|e| e.source)
}

/// Build one example directory, returning a structured report (never panics).
pub fn build_example(opts: &BuildOptions) -> BuildReport {
    let mut report = BuildReport::default();

    let (source, registry, ffi_used) = match assemble_and_emit(&opts.repo_root, &opts.example_dir) {
        Ok(e) => {
            report.warnings = e.warnings;
            (e.source, e.registry, e.ffi_used)
        }
        Err(note) => {
            report.note = note;
            return report;
        }
    };

    // ---- write sky-out + materialise runtime ----
    let out_dir = opts.example_dir.join(&opts.out_dir_name);
    if let Err(e) = write_out(&opts.repo_root, &out_dir, &source) {
        report.note = format!("write failed: {e}");
        return report;
    }
    // Materialise the Go wrapper for every FFI package the program actually
    // calls into `sky-out/rt/` (package rt), so `rt.Go_<Pkg>_<fn>T` resolves.
    if let Err(e) = materialise_ffi_bindings(&registry, &ffi_used, &out_dir) {
        report.note = format!("ffi binding copy failed: {e}");
        return report;
    }
    // The base go.mod copied from `runtime-go` pins the stdlib's deps but NOT
    // project-specific FFI packages (`sky add github.com/gorilla/mux`). A
    // materialised binding that imports such a package fails `go build` until the
    // module is a `require` + present in go.sum — inject those now.
    if let Err(e) = inject_ffi_deps(&registry, &ffi_used, &out_dir, &opts.example_dir) {
        report.warnings.push(format!("ffi go.mod injection: {e}"));
    }
    report.emitted = true;

    // ---- go build ----
    let build = Command::new("go")
        .arg("build")
        .arg("-o")
        .arg("app")
        .arg(".")
        .current_dir(&out_dir)
        .env("GOFLAGS", "-mod=mod")
        .output();
    match build {
        Ok(o) => {
            report.go_build_ok = o.status.success();
            report.go_build_stderr = String::from_utf8_lossy(&o.stderr).to_string();
        }
        Err(e) => {
            report.go_build_stderr = format!("go build spawn failed: {e}");
            return report;
        }
    }

    // ---- run ----
    if opts.run && report.go_build_ok {
        use std::io::Write;
        let mut cmd = Command::new("./app");
        cmd.current_dir(&out_dir);
        cmd.stdin(std::process::Stdio::piped());
        cmd.stdout(std::process::Stdio::piped());
        cmd.stderr(std::process::Stdio::piped());
        if let Ok(mut child) = cmd.spawn() {
            if let Some(input) = &opts.stdin {
                if let Some(si) = child.stdin.as_mut() {
                    let _ = si.write_all(input.as_bytes());
                }
            }
            drop(child.stdin.take());
            match child.wait_with_output() {
                Ok(o) => {
                    report.run_ok = Some(o.status.success());
                    report.run_stdout = Some(String::from_utf8_lossy(&o.stdout).to_string());
                    report.run_stderr = Some(String::from_utf8_lossy(&o.stderr).to_string());
                }
                Err(e) => report.note = format!("run failed: {e}"),
            }
        }
    }

    report
}

/// Minimal `sky.toml` reader for the build-time `init()` defaults. Extracts
/// top-level `port` and the `[database]` `driver`/`path` — enough for the
/// runtime's `SKY_*` fallbacks (`Db.connect ()` resolves `SKY_DB_PATH`). A full
/// TOML parse isn't warranted for these flat keys; unknown shapes are ignored.
fn read_sky_toml_config(path: &Path) -> lower::LowerConfig {
    let mut cfg = lower::LowerConfig::default();
    let Ok(text) = std::fs::read_to_string(path) else {
        return cfg;
    };
    let mut section = String::new();
    for raw in text.lines() {
        let line = raw.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        if line.starts_with('[') && line.ends_with(']') {
            section = line.trim_matches(['[', ']']).trim().trim_matches('"').to_string();
            continue;
        }
        let Some((k, v)) = line.split_once('=') else {
            continue;
        };
        let key = k.trim();
        let val = v.trim().trim_matches('"').to_string();
        match (section.as_str(), key) {
            ("", "port") => cfg.port = Some(val),
            ("database", "driver") => cfg.extra_defaults.push(("DB_DRIVER".into(), val)),
            ("database", "path") => cfg.extra_defaults.push(("DB_PATH".into(), val)),
            ("live", "port") => cfg.port = Some(val),
            _ => {}
        }
    }
    cfg
}

// ---- FFI surface loading + binding materialisation (doc 09) --------------

/// Locate the pinned FFI surface for an example: prefer the committed
/// `sky-ffi/` layout (doc 09 §C.1); fall back to the oracle's `.skycache/`
/// cache. Returns the loaded registry (empty when neither exists).
fn load_ffi_surface(example_dir: &Path) -> ffi::FfiRegistry {
    let pinned_ffi = example_dir.join("sky-ffi");
    let pinned_go = pinned_ffi.join("go");
    if pinned_ffi.is_dir() {
        let reg = ffi::load_surface(&pinned_ffi, &pinned_go);
        if !reg.is_empty() {
            return reg;
        }
    }
    let cache_ffi = example_dir.join(".skycache").join("ffi");
    let cache_go = example_dir.join(".skycache").join("go");
    ffi::load_surface(&cache_ffi, &cache_go)
}

/// Project the loaded registry to the `lower::FfiTable` the lowerer consumes.
fn build_ffi_table(reg: &ffi::FfiRegistry) -> lower::FfiTable {
    let mut table = lower::FfiTable::default();
    for (module, pkg) in &reg.packages {
        table.mods.insert(
            module.clone(),
            lower::FfiModInfo {
                kernel_name: pkg.kernel_name.clone(),
                go_symbols: pkg.go_symbols.clone(),
                ffi_slots: pkg.ffi_slots.clone(),
            },
        );
    }
    table
}

/// Copy the Go wrapper for each called FFI package into `<out_dir>/rt/`.
fn materialise_ffi_bindings(
    reg: &ffi::FfiRegistry,
    used: &std::collections::BTreeSet<String>,
    out_dir: &Path,
) -> std::io::Result<()> {
    if used.is_empty() {
        return Ok(());
    }
    let rt_dir = out_dir.join("rt");
    std::fs::create_dir_all(&rt_dir)?;
    for module in used {
        let Some(pkg) = reg.resolve(module) else {
            continue;
        };
        let Some(src) = &pkg.binding_file else {
            continue;
        };
        if let Some(name) = src.file_name() {
            std::fs::copy(src, rt_dir.join(name))?;
        }
    }
    Ok(())
}

/// Add a `require` for every external Go-FFI package the program calls that the
/// base go.mod (copied from `runtime-go`) does not already pin. Stdlib packages
/// (`net/http`, `io`, `os`) never need a require and are skipped. The version is
/// taken from the oracle's committed `sky-out/go.mod` when present (exact match),
/// else resolved as `@latest`. `go get` handles go.mod + go.sum + the module
/// download in one step (offline via the module cache when populated).
fn inject_ffi_deps(
    reg: &ffi::FfiRegistry,
    used: &std::collections::BTreeSet<String>,
    out_dir: &Path,
    example_dir: &Path,
) -> std::io::Result<()> {
    let mut paths: Vec<String> = used
        .iter()
        .filter_map(|m| reg.resolve(m))
        .map(|p| p.go_package.trim().to_string())
        .filter(|p| is_external_module(p))
        .collect();
    paths.sort();
    paths.dedup();
    if paths.is_empty() {
        return Ok(());
    }

    let go_mod = out_dir.join("go.mod");
    let existing = std::fs::read_to_string(&go_mod).unwrap_or_default();
    let oracle_mod = std::fs::read_to_string(example_dir.join("sky-out").join("go.mod")).ok();

    for path in &paths {
        if module_required(&existing, path) {
            continue;
        }
        let spec = match oracle_mod.as_deref().and_then(|m| required_version(m, path)) {
            Some(v) => format!("{path}@{v}"),
            None => path.clone(),
        };
        // `go get <path>[@version]` edits go.mod, downloads the module, and writes
        // go.sum. Best-effort: a network/cache miss is surfaced by the subsequent
        // `go build` failure rather than aborting the emit.
        let _ = Command::new("go")
            .args(["get", &spec])
            .current_dir(out_dir)
            .env("GOFLAGS", "-mod=mod")
            .output();
    }
    Ok(())
}

/// A Go import path names an external module (needs a `require`) when its first
/// segment carries a dot (`github.com/…`, `gopkg.in/…`). Stdlib paths (`io`,
/// `net/http`, `os`) never do.
fn is_external_module(path: &str) -> bool {
    path.split('/').next().is_some_and(|head| head.contains('.'))
}

/// Whether `go.mod` text already pins `path` in a require directive.
fn module_required(go_mod: &str, path: &str) -> bool {
    go_mod
        .lines()
        .any(|l| l.split_whitespace().next() == Some(path) || l.trim() == path)
}

/// Extract the version pinned for `path` from a go.mod's require lines.
fn required_version(go_mod: &str, path: &str) -> Option<String> {
    for line in go_mod.lines() {
        let mut it = line.split_whitespace();
        if it.next() == Some(path) {
            if let Some(v) = it.next() {
                if v.starts_with('v') {
                    return Some(v.to_string());
                }
            }
        }
    }
    None
}

fn write_out(repo_root: &Path, out_dir: &Path, source: &str) -> std::io::Result<()> {
    std::fs::create_dir_all(out_dir)?;
    std::fs::write(out_dir.join("main.go"), source)?;
    // go.mod / go.sum from the runtime module (module `sky-app`).
    let rt_src = repo_root.join("runtime-go");
    std::fs::copy(rt_src.join("go.mod"), out_dir.join("go.mod"))?;
    let sum = rt_src.join("go.sum");
    if sum.exists() {
        std::fs::copy(sum, out_dir.join("go.sum"))?;
    }
    // materialise a pruned copy of runtime-go/rt (tests stripped).
    let rt_dst = out_dir.join("rt");
    materialise_rt(&rt_src.join("rt"), &rt_dst)?;
    Ok(())
}

/// Copy `rt/` wholesale, skipping `*_test.go` and testdata (doc 09 §A.1). Copies
/// recursively (the runtime has a `console_app/` subtree).
fn materialise_rt(src: &Path, dst: &Path) -> std::io::Result<()> {
    std::fs::create_dir_all(dst)?;
    for entry in std::fs::read_dir(src)? {
        let entry = entry?;
        let path = entry.path();
        let name = entry.file_name();
        let name_s = name.to_string_lossy().to_string();
        if path.is_dir() {
            // skip nested module dirs that carry their own go.mod would break the
            // build; the console_app is package main — skip it (not linked by rt).
            if name_s == "console_app" || name_s == "testdata" {
                continue;
            }
            materialise_rt(&path, &dst.join(&name_s))?;
        } else if name_s.ends_with("_test.go") {
            continue;
        } else if name_s.ends_with(".go") {
            std::fs::copy(&path, dst.join(&name_s))?;
        }
    }
    Ok(())
}

// ---- module loading (mirrors xtask/infer_gate) ---------------------------

fn load_dir(dir: &Path) -> Vec<(String, syntax::Parse)> {
    let mut files = Vec::new();
    collect_sky(dir, &mut files);
    let mut out = Vec::new();
    for path in files {
        let Ok(src) = std::fs::read_to_string(&path) else {
            continue;
        };
        let parse = syntax::parse(&src, base::FileId(0));
        let name = module_name(&parse, &path);
        out.push((name, parse));
    }
    out
}

fn module_name(parse: &syntax::Parse, path: &Path) -> String {
    let tree = parse.tree();
    if let Some(n) = tree.module_header().and_then(|h| h.name()).map(|n| n.text()) {
        if !n.is_empty() {
            return n;
        }
    }
    path.file_stem()
        .and_then(|s| s.to_str())
        .unwrap_or("Main")
        .to_string()
}

fn is_generated(path: &Path) -> bool {
    path.components().any(|c| {
        matches!(
            c.as_os_str().to_str(),
            Some("sky-out") | Some("sky-out-rust") | Some(".skycache") | Some(".skydeps")
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

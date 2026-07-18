#![forbid(unsafe_code)]
//! `sky` — the CLI binary: a thin front-end over the shared `project` build
//! driver (doc 01, doc 10). The same engine the LSP and `xtask` drive; this
//! binary just resolves a `<file>` argument to a project + repo root and calls
//! `project::build_example` / `build_project`, then formats/runs/tests as the
//! verb dictates. `sky check` ≡ `sky build` minus running (both run `go build`).

use std::io::Read;
use std::path::{Path, PathBuf};
use std::process::{Command, ExitCode};

use fmt::{format_source, is_formatted};
use project::{
    build_example, is_compiler_repo_root, project_dir_for, repo_root_for, run_app, BuildOptions,
};
use testrunner::run_test;

fn main() -> ExitCode {
    let args: Vec<String> = std::env::args().skip(1).collect();
    match args.first().map(String::as_str) {
        Some("--version") | Some("-V") | Some("version") => {
            println!("{}", version_string());
            ExitCode::SUCCESS
        }
        Some("--help") | Some("-h") | Some("help") | None => {
            print_help();
            ExitCode::SUCCESS
        }
        Some("build") => cmd_build(&args[1..], /*check_only=*/ false),
        Some("check") => cmd_build(&args[1..], /*check_only=*/ true),
        Some("run") => cmd_run(&args[1..]),
        Some("fmt") => cmd_fmt(&args[1..]),
        Some("test") => cmd_test(&args[1..]),
        Some("lsp") => cmd_lsp(&args[1..]),
        Some("clean") => cmd_clean(&args[1..]),
        // Verbs the bring-up does not implement yet — honest, explicit deferral.
        Some(
            verb @ ("doc" | "watch" | "db" | "add" | "remove" | "install" | "update" | "init"
            | "doctor" | "console" | "console-serve" | "upgrade" | "upgrade-claude" | "verify"),
        ) => {
            eprintln!(
                "sky {verb}: not yet implemented in the rust bring-up.\n\
                 Wired verbs: build, run, check, fmt, test, lsp, clean, version, help."
            );
            ExitCode::from(2)
        }
        Some(other) => {
            eprintln!("sky: unknown command `{other}`. Try `sky --help`.");
            ExitCode::from(2)
        }
    }
}

// ---- build / check -------------------------------------------------------

/// `sky build <file>` (and, with `check_only`, `sky check <file>`). Both emit Go
/// and run `go build`; build reports the produced binary, check reports "No
/// errors found." and never runs the program — the `sky check ≡ sky build`
/// invariant (doc 10).
fn cmd_build(args: &[String], check_only: bool) -> ExitCode {
    let (positional, out_override) = parse_out(args);
    let Some(file) = positional.first() else {
        eprintln!("usage: sky {} <file.sky> [--out <dir>]", verb(check_only));
        return ExitCode::from(2);
    };
    let file = Path::new(file);
    let Some((repo_root, project_dir)) = resolve(file) else {
        return ExitCode::FAILURE;
    };
    // Repo-root guard (task #662 / app/Main.hs:1293): refuse to write sky-out/
    // into the compiler repo root, which would overwrite the compiler binary.
    if is_compiler_repo_root(&project_dir) && out_override.is_none() {
        eprintln!(
            "sky {}: refusing to run from the Sky compiler repo root\n\
             (it contains sky-compiler.cabal; output would overwrite the compiler).\n\
             cd into an example or user project first, e.g.\n  \
             cd examples/01-hello-world && sky {} src/Main.sky",
            verb(check_only),
            verb(check_only),
        );
        return ExitCode::FAILURE;
    }

    let out_dir_name = out_override.unwrap_or_else(|| "sky-out".to_string());
    let opts = BuildOptions {
        repo_root,
        example_dir: project_dir.clone(),
        out_dir_name: out_dir_name.clone(),
        run: false,
        stdin: None,
    };
    let report = build_example(&opts);
    for w in &report.warnings {
        eprintln!("warning: {w}");
    }
    if !report.emitted {
        eprintln!("sky {}: {}", verb(check_only), report.note);
        return ExitCode::FAILURE;
    }
    println!("Running go build...");
    if !report.go_build_ok {
        if check_only {
            eprintln!(
                "Codegen produced Go that `go build` rejects.\n\
                 This is a compiler-side bug — the Sky type system accepted the\n\
                 program but Go did not.\n\nGo errors:\n{}",
                report.go_build_stderr
            );
        } else {
            eprintln!("go build failed:\n{}", report.go_build_stderr);
        }
        return ExitCode::FAILURE;
    }
    if check_only {
        println!("No errors found.");
    } else {
        println!("Compilation successful");
        println!("Build complete: {}/app", project_dir.join(&out_dir_name).display());
    }
    ExitCode::SUCCESS
}

fn verb(check_only: bool) -> &'static str {
    if check_only {
        "check"
    } else {
        "build"
    }
}

// ---- run -----------------------------------------------------------------

/// `sky run <file>` — build, then exec the produced binary with inherited
/// stdio, propagating its exit code.
fn cmd_run(args: &[String]) -> ExitCode {
    let (positional, out_override) = parse_out(args);
    let Some(file) = positional.first() else {
        eprintln!("usage: sky run <file.sky>");
        return ExitCode::from(2);
    };
    let file = Path::new(file);
    let Some((repo_root, project_dir)) = resolve(file) else {
        return ExitCode::FAILURE;
    };
    let out_dir_name = out_override.unwrap_or_else(|| "sky-out".to_string());
    let opts = BuildOptions {
        repo_root,
        example_dir: project_dir.clone(),
        out_dir_name: out_dir_name.clone(),
        run: false,
        stdin: None,
    };
    let report = build_example(&opts);
    for w in &report.warnings {
        eprintln!("warning: {w}");
    }
    if !report.emitted {
        eprintln!("sky run: {}", report.note);
        return ExitCode::FAILURE;
    }
    if !report.go_build_ok {
        eprintln!("sky run: go build failed:\n{}", report.go_build_stderr);
        return ExitCode::FAILURE;
    }
    let out_dir = project_dir.join(&out_dir_name);
    match run_app(&out_dir, &[]) {
        Ok(status) => ExitCode::from(status.code().unwrap_or(1) as u8),
        Err(e) => {
            eprintln!("sky run: could not launch binary: {e}");
            ExitCode::FAILURE
        }
    }
}

// ---- fmt -----------------------------------------------------------------

/// `sky fmt [--check] [--stdin|-] <file...>` — lossless, idempotent CST reprint
/// (doc 10 §"sky fmt"; bring-up scope: reprint, not opinionated re-layout).
fn cmd_fmt(args: &[String]) -> ExitCode {
    let check = args.iter().any(|a| a == "--check");
    let stdin_mode = args.iter().any(|a| a == "--stdin" || a == "-");
    let files: Vec<&String> = args
        .iter()
        .filter(|a| !a.starts_with("--") && a.as_str() != "-")
        .collect();

    if stdin_mode {
        let mut src = String::new();
        if std::io::stdin().read_to_string(&mut src).is_err() {
            eprintln!("sky fmt: could not read stdin");
            return ExitCode::FAILURE;
        }
        let out = format_source(&src);
        if check {
            return if out == src { ExitCode::SUCCESS } else { ExitCode::FAILURE };
        }
        print!("{out}");
        return ExitCode::SUCCESS;
    }

    if files.is_empty() {
        eprintln!("usage: sky fmt [--check] <file.sky ...>   |   sky fmt --stdin");
        return ExitCode::from(2);
    }

    let mut changed_or_error = false;
    for f in files {
        let path = Path::new(f);
        let Ok(src) = std::fs::read_to_string(path) else {
            eprintln!("sky fmt: could not read {f}");
            changed_or_error = true;
            continue;
        };
        if check {
            if !is_formatted(&src) {
                println!("would reformat: {f}");
                changed_or_error = true;
            }
            continue;
        }
        let out = format_source(&src);
        if out != src {
            if let Err(e) = std::fs::write(path, &out) {
                eprintln!("sky fmt: could not write {f}: {e}");
                changed_or_error = true;
            } else {
                println!("formatted: {f}");
            }
        }
    }
    if changed_or_error {
        ExitCode::FAILURE
    } else {
        ExitCode::SUCCESS
    }
}

// ---- test ----------------------------------------------------------------

/// `sky test <suite.sky>` — synthesise an entry importing the suite, build+run
/// via the shared driver, propagate the test binary's exit code.
fn cmd_test(args: &[String]) -> ExitCode {
    let (positional, out_override) = parse_out(args);
    let Some(file) = positional.first() else {
        eprintln!("usage: sky test <suite.sky>");
        return ExitCode::from(2);
    };
    let out_dir_name = out_override.unwrap_or_else(|| "sky-out".to_string());
    match run_test(Path::new(file), &out_dir_name) {
        Ok(run) => {
            if !run.note.is_empty() {
                eprintln!("sky test: {}", run.note);
            }
            match run.exit_code {
                Some(0) => ExitCode::SUCCESS,
                Some(n) => ExitCode::from(n as u8),
                None => ExitCode::FAILURE,
            }
        }
        Err(e) => {
            eprintln!("sky test: {e}");
            ExitCode::FAILURE
        }
    }
}

// ---- lsp -----------------------------------------------------------------

/// `sky lsp` — launch the (already built) `sky-lsp` JSON-RPC server over stdio.
/// Locates the sibling binary next to this executable and execs it, forwarding
/// stdin/stdout/stderr.
fn cmd_lsp(args: &[String]) -> ExitCode {
    let bin = match std::env::current_exe() {
        Ok(exe) => exe.with_file_name(if cfg!(windows) { "sky-lsp.exe" } else { "sky-lsp" }),
        Err(e) => {
            eprintln!("sky lsp: cannot locate executable dir: {e}");
            return ExitCode::FAILURE;
        }
    };
    if !bin.exists() {
        eprintln!(
            "sky lsp: sky-lsp binary not found next to `sky` (looked at {}).\n\
             Build it with: cargo build -p sky-lsp",
            bin.display()
        );
        return ExitCode::FAILURE;
    }
    match Command::new(&bin).args(args).status() {
        Ok(status) => ExitCode::from(status.code().unwrap_or(1) as u8),
        Err(e) => {
            eprintln!("sky lsp: failed to launch {}: {e}", bin.display());
            ExitCode::FAILURE
        }
    }
}

// ---- clean ---------------------------------------------------------------

/// `sky clean` — remove generated `sky-out/` + `.skycache/` in the current
/// project (cwd). Best-effort; absent dirs are a no-op.
fn cmd_clean(_args: &[String]) -> ExitCode {
    let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
    let mut removed = Vec::new();
    for name in ["sky-out", ".skycache", "dist"] {
        let dir = cwd.join(name);
        if dir.is_dir() && std::fs::remove_dir_all(&dir).is_ok() {
            removed.push(name);
        }
    }
    if removed.is_empty() {
        println!("clean: nothing to remove");
    } else {
        println!("clean: removed {}", removed.join(", "));
    }
    ExitCode::SUCCESS
}

// ---- shared helpers ------------------------------------------------------

/// Resolve a `<file>` to its (repo_root, project_dir). Prints a diagnostic and
/// returns `None` when the file is missing or the compiler assets can't be
/// located.
fn resolve(file: &Path) -> Option<(PathBuf, PathBuf)> {
    if !file.exists() {
        eprintln!("sky: no such file: {}", file.display());
        return None;
    }
    let Some(repo_root) = repo_root_for(file) else {
        eprintln!(
            "sky: could not locate compiler assets (sky-stdlib/ + runtime-go/)\n\
             above {}. Run from within the Sky repo tree.",
            file.display()
        );
        return None;
    };
    let project_dir = project_dir_for(file);
    Some((repo_root, project_dir))
}

/// Split `args` into positionals and an optional `--out <dir>` override.
fn parse_out(args: &[String]) -> (Vec<String>, Option<String>) {
    let mut positional = Vec::new();
    let mut out = None;
    let mut it = args.iter();
    while let Some(a) = it.next() {
        match a.as_str() {
            "--out" | "-o" => out = it.next().cloned(),
            s if s.starts_with("--out=") => out = Some(s["--out=".len()..].to_string()),
            s if s.starts_with('-') => { /* ignore unknown flags for forward-compat */ }
            s => positional.push(s.to_string()),
        }
    }
    (positional, out)
}

/// Version string, mirroring `skyVersionString` (`app/Main.hs:1273`): `sky dev`
/// for a `dev` `app/VERSION`, else `sky v<version>`.
fn version_string() -> String {
    let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
    let ver = repo_root_for(&cwd)
        .and_then(|root| std::fs::read_to_string(root.join("app").join("VERSION")).ok())
        .map(|s| s.trim().to_string());
    match ver.as_deref() {
        Some("dev") | None => "sky dev".to_string(),
        Some(v) => format!("sky v{v}"),
    }
}

fn print_help() {
    println!(
        "sky — the Sky compiler CLI (rust bring-up)\n\n\
         USAGE:\n  sky <command> [args]\n\n\
         WIRED COMMANDS:\n\
         \x20 build <file>     compile → sky-out/ + go build\n\
         \x20 check <file>     type-check + go build (no binary run)\n\
         \x20 run   <file>     build + execute\n\
         \x20 fmt   <file...>  format in place (--check / --stdin)\n\
         \x20 test  <file>     run a Sky.Test suite\n\
         \x20 lsp              launch the sky-lsp server (stdio)\n\
         \x20 clean            remove sky-out/ + .skycache/\n\
         \x20 version          print the version\n\n\
         DEFERRED (bring-up): doc, watch, db, add, remove, install, update,\n\
         \x20 init, doctor, console, upgrade, verify"
    );
}

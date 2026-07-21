#![forbid(unsafe_code)]
//! `sky` — the CLI binary: a thin front-end over the shared `project` build
//! driver (doc 01, doc 10). The same engine the LSP and `xtask` drive; this
//! binary just resolves a `<file>` argument to a project + repo root and calls
//! `project::build_example` / `build_project`, then formats/runs/tests as the
//! verb dictates. `sky check` ≡ `sky build` minus running (both run `go build`).

use std::io::Read;
use std::path::{Path, PathBuf};
use std::process::{Command, ExitCode, Stdio};
use std::time::{Duration, Instant};

use fmt::{format_source, is_formatted};
use project::{
    assets_root_for, build_example, is_compiler_repo_root, project_dir_for, repo_root_for, run_app,
    BuildOptions,
};
use testrunner::run_test;

mod bundled;

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
        Some("init") => cmd_init(&args[1..]),
        Some("doc") => cmd_doc(&args[1..]),
        Some("watch") => cmd_watch(&args[1..]),
        Some("db") => cmd_db(&args[1..]),
        Some("add") => cmd_add(&args[1..]),
        Some("remove") => cmd_remove(&args[1..]),
        Some("install") => cmd_install(&args[1..]),
        Some("update") => cmd_update(&args[1..]),
        // Rust-native verbs (no bundled Sky app needed): project/environment
        // health, template refresh, and build+run verification.
        Some("doctor") => cmd_doctor(&args[1..]),
        Some("upgrade-claude") => cmd_upgrade_claude(&args[1..]),
        Some("verify") => cmd_verify(&args[1..]),
        // Bundled-app verbs: build + spawn a bundled Sky/Go app from the repo
        // tree (`console`/`console-serve`/`doc --serve`/`doc --tui`).
        Some("console") => cmd_console(&args[1..]),
        Some("console-serve") => cmd_console_serve(&args[1..]),
        Some("upgrade") => cmd_upgrade(&args[1..]),
        Some(other) => {
            eprintln!("sky: unknown command `{other}`. Try `sky --help`.");
            ExitCode::from(2)
        }
    }
}

/// `sky upgrade` — self-update the `sky` binary. The Haskell `sky` downloads the
/// matching tagged release from GitHub (`anzellai/sky`) and replaces the binary.
/// The Rust compiler is a rewrite/dev build not yet published as a `sky` release,
/// so there is nothing newer to fetch — be honest about that (and how to update)
/// rather than a silent no-op or an "unimplemented" stub.
fn cmd_upgrade(_args: &[String]) -> ExitCode {
    let ver = version_string();
    println!("sky upgrade — current version: {ver}");
    if ver == "sky dev" || ver.contains("dev") {
        println!(
            "This is a rewrite/dev build of the Rust `sky`, not a published release, so \
             there is no newer binary to fetch.\n\
             Update it by rebuilding from source (in the sky repo):\n  \
             cargo build -p sky-cli --bin sky\n\
             Self-update from GitHub releases activates once the Rust `sky` ships a tagged \
             release."
        );
    } else {
        println!(
            "Self-update for released Rust `sky` builds is not wired yet; download the \
             latest release from https://github.com/anzellai/sky/releases."
        );
    }
    ExitCode::SUCCESS
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
    // Repo-root guard: refuse to write sky-out/ into the compiler repo root,
    // which would overwrite the oracle binary kept there.
    if is_compiler_repo_root(&project_dir) && out_override.is_none() {
        eprintln!(
            "sky {}: refusing to run from the Sky compiler repo root\n\
             (output would overwrite sky-out/).\n\
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
        out_dir_abs: None,
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
    if let Some(note) = &report.cgo_note {
        println!("go build {note}");
    }
    if check_only {
        println!("No errors found.");
    } else {
        println!("Compilation successful");
        println!(
            "Build complete: {}/app",
            project_dir.join(&out_dir_name).display()
        );
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
        out_dir_abs: None,
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
    if let Some(note) = &report.cgo_note {
        eprintln!("sky run: go build {note}");
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

/// `sky fmt [--check] [--stdin|-] <file...>` — opinionated, idempotent
/// re-layout (doc 10 §"sky fmt"), falling back to a lossless CST reprint for
/// any file where the opinionated pass would drop a comment or not be provably
/// idempotent (see `fmt::format_source`).
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
            return if out == src {
                ExitCode::SUCCESS
            } else {
                ExitCode::FAILURE
            };
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
        Ok(exe) => exe.with_file_name(if cfg!(windows) {
            "sky-lsp.exe"
        } else {
            "sky-lsp"
        }),
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

// ---- init ----------------------------------------------------------------

/// `sky init [name]` — scaffold a new project: `<name>/sky.toml`,
/// `<name>/src/Main.sky` (a hello-world), and `<name>/.gitignore`. Mirrors
/// `app/Main.hs`'s `Init` handler (name defaults to `sky-project`). The CLAUDE.md
/// coding guide is copied from the repo's `templates/CLAUDE.md` when reachable.
fn cmd_init(args: &[String]) -> ExitCode {
    let name = args
        .iter()
        .find(|a| !a.starts_with('-'))
        .cloned()
        .unwrap_or_else(|| "sky-project".to_string());
    let root = Path::new(&name);
    println!("Initialising project: {name}");

    if let Err(e) = std::fs::create_dir_all(root.join("src")) {
        eprintln!("sky init: could not create {}/src: {e}", root.display());
        return ExitCode::FAILURE;
    }

    let toml = format!(
        "# sky.toml — project configuration.\n\
         # Full reference: https://github.com/anzellai/sky#skytoml\n\n\
         name    = \"{name}\"\n\
         version = \"0.1.0\"\n\
         entry   = \"src/Main.sky\"\n\
         bin     = \"app\"\n\n\
         [source]\n\
         root = \"src\"\n\n\
         # [live]            # Sky.Live runtime (uncomment to configure)\n\
         # port         = 8000\n\
         # store        = \"memory\"   # memory | sqlite | postgres | redis\n\
         # storePath    = \"sky.db\"\n\
         # ttl          = 1800\n\n\
         # [auth]            # Std.Auth configuration (uncomment to use)\n\
         # driver     = \"jwt\"\n\
         # secret     = \"change-me\"\n\n\
         # [database]        # Std.Db configuration (uncomment to use)\n\
         # driver = \"sqlite\"\n\
         # path   = \"app.db\"\n\n\
         # [\"go.dependencies\"]        # `sky add <pkg>` records these here\n\n\
         # [dependencies]              # Sky-source dependencies (from git)\n"
    );
    let main_sky = format!(
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\n\n\
         main =\n    println \"Hello from {name}!\"\n"
    );
    let gitignore = "sky-out/\n.skycache/\n.skydeps/\n.env\n*.db\n*.db-shm\n*.db-wal\n";

    let writes = [
        (root.join("sky.toml"), toml),
        (root.join("src/Main.sky"), main_sky),
        (root.join(".gitignore"), gitignore.to_string()),
    ];
    for (path, body) in &writes {
        if let Err(e) = std::fs::write(path, body) {
            eprintln!("sky init: could not write {}: {e}", path.display());
            return ExitCode::FAILURE;
        }
    }

    // Best-effort CLAUDE.md: from the repo template in dev, else the copy
    // embedded in the binary (doc 09 §E) so `sky init` scaffolds it standalone.
    let mut copied_claude = false;
    let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
    let dst = root.join("CLAUDE.md");
    if let Some(repo_root) = repo_root_for(&cwd).or_else(|| repo_root_for(root)) {
        let tmpl = repo_root.join("templates").join("CLAUDE.md");
        if tmpl.is_file() && std::fs::copy(&tmpl, &dst).is_ok() {
            copied_claude = true;
        }
    }
    if !copied_claude {
        copied_claude = project::extract_template("CLAUDE.md", &dst);
    }

    println!("Created {}/", root.display());
    println!("  sky.toml");
    println!("  src/Main.sky");
    println!("  .gitignore");
    if copied_claude {
        println!("  CLAUDE.md");
    }
    println!();
    println!("Next: cd {name} && sky build src/Main.sky");
    ExitCode::SUCCESS
}

// ---- doc -----------------------------------------------------------------

/// `sky doc <Module>` — terminal docs for one module (exported bindings + type
/// signatures + `-- |` summaries). `--list` enumerates every module.
/// `--serve` / `--tui` are deferred (they spawn a bundled Sky app the bring-up
/// doesn't materialise).
fn cmd_doc(args: &[String]) -> ExitCode {
    let serve = args.iter().any(|a| a == "--serve");
    let tui = args.iter().any(|a| a == "--tui");
    if serve && tui {
        eprintln!("sky doc: --serve and --tui are incompatible (pick one).");
        return ExitCode::from(2);
    }
    if serve {
        return cmd_doc_serve(parse_port(args, 8030));
    }
    if tui {
        return cmd_doc_tui();
    }
    let list = args.iter().any(|a| a == "--list");
    let target = args.iter().find(|a| !a.starts_with('-')).cloned();

    // Resolve the project + repo root from cwd (doc reads stdlib + src/).
    let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
    let Some(repo_root) = assets_root_for(&cwd) else {
        return ExitCode::FAILURE;
    };
    let project_dir = project::project_dir_for(&cwd.join("_"));

    if list {
        println!("{}", project::list_modules(&repo_root, &project_dir));
        return ExitCode::SUCCESS;
    }
    let Some(module) = target else {
        eprintln!("usage: sky doc <Module>   |   sky doc --list");
        return ExitCode::from(2);
    };
    match project::render_module(&repo_root, &project_dir, &module) {
        Ok(page) => {
            print!("{page}");
            ExitCode::SUCCESS
        }
        Err(msg) => {
            eprintln!("{msg}");
            ExitCode::FAILURE
        }
    }
}

/// `sky doc --serve` renders a static doc-site from the project's stdlib and
/// `src/`, then builds and spawns the bundled `sky-doc-server` (Sky.Http.Server)
/// pointed at it via `SKY_DOC_DIR` on `SKY_LIVE_PORT`. Foreground; Ctrl-C stops.
/// Mirrors `app/Main.hs` `runDocServe`.
fn cmd_doc_serve(port: u16) -> ExitCode {
    let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
    let Some(repo_root) = assets_root_for(&cwd) else {
        return ExitCode::FAILURE;
    };
    let project_dir = project::project_dir_for(&cwd.join("_"));

    // Render the doc-site into the project's cache so the server has content.
    let doc_out = project_dir.join(".skycache").join("doc-out");
    if let Err(e) = project::render_doc_site(&repo_root, &project_dir, &doc_out) {
        eprintln!("sky doc: could not render doc-site: {e}");
        return ExitCode::FAILURE;
    }

    let Some(src_dir) = bundled::bundled_src_dir(&repo_root, "doc") else {
        return bundled_missing("doc");
    };
    let out_dir = match bundled::ensure_built(
        &repo_root,
        &src_dir,
        "doc",
        "live",
        bundled::ENTRY_LIVE,
        &version_slug(),
    ) {
        Ok(d) => d,
        Err(e) => {
            eprintln!("{e}");
            return ExitCode::FAILURE;
        }
    };

    println!(
        "sky doc: serving {} on http://127.0.0.1:{port} (Ctrl-C to stop)",
        doc_out.display()
    );
    spawn_foreground(
        &out_dir,
        &[
            ("SKY_LIVE_PORT".to_string(), port.to_string()),
            (
                "SKY_DOC_DIR".to_string(),
                doc_out.to_string_lossy().into_owned(),
            ),
        ],
    )
}

/// `sky doc --tui` — render the doc-site, then build + spawn the bundled
/// Sky.Tui doc browser pointed at it via `SKY_DOC_DIR`. Mirrors `runDocTui`.
fn cmd_doc_tui() -> ExitCode {
    let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
    let Some(repo_root) = assets_root_for(&cwd) else {
        return ExitCode::FAILURE;
    };
    let project_dir = project::project_dir_for(&cwd.join("_"));

    let doc_out = project_dir.join(".skycache").join("doc-out");
    if let Err(e) = project::render_doc_site(&repo_root, &project_dir, &doc_out) {
        eprintln!("sky doc: could not render doc-site: {e}");
        return ExitCode::FAILURE;
    }

    let Some(src_dir) = bundled::bundled_src_dir(&repo_root, "doc") else {
        return bundled_missing("doc");
    };
    let out_dir = match bundled::ensure_built(
        &repo_root,
        &src_dir,
        "doc",
        "tui",
        bundled::ENTRY_TUI,
        &version_slug(),
    ) {
        Ok(d) => d,
        Err(e) => {
            eprintln!("{e}");
            return ExitCode::FAILURE;
        }
    };

    println!("sky doc: starting terminal browser (Ctrl-C to exit)...");
    spawn_foreground(
        &out_dir,
        &[(
            "SKY_DOC_DIR".to_string(),
            doc_out.to_string_lossy().into_owned(),
        )],
    )
}

// ---- console -------------------------------------------------------------

/// `sky console [--port N] [--tui]` — build + spawn the bundled Sky Console
/// (`sky-bundled/console`): Sky.Live on `SKY_LIVE_PORT` (default 8025), or the
/// Sky.Tui backend with `--tui`. Foreground; Ctrl-C stops. Mirrors the
/// `SpawnSkyConsole` build+spawn shape (`app/Main.hs` `runConsole`).
fn cmd_console(args: &[String]) -> ExitCode {
    let tui = args.iter().any(|a| a == "--tui");
    let port = parse_port(args, 8025);

    let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
    let Some(repo_root) = assets_root_for(&cwd) else {
        return ExitCode::FAILURE;
    };
    let Some(src_dir) = bundled::bundled_src_dir(&repo_root, "console") else {
        return bundled_missing("console");
    };

    let (variant, entry): (&str, &str) = if tui {
        ("tui", bundled::ENTRY_TUI)
    } else {
        ("live", bundled::ENTRY_LIVE)
    };
    let out_dir = match bundled::ensure_built(
        &repo_root,
        &src_dir,
        "console",
        variant,
        entry,
        &version_slug(),
    ) {
        Ok(d) => d,
        Err(e) => {
            eprintln!("{e}");
            return ExitCode::FAILURE;
        }
    };

    if tui {
        println!("sky console: starting terminal console (Ctrl-C to exit)...");
        spawn_foreground(&out_dir, &[])
    } else {
        println!("sky console: serving on http://127.0.0.1:{port} (Ctrl-C to stop)");
        spawn_foreground(&out_dir, &[("SKY_LIVE_PORT".to_string(), port.to_string())])
    }
}

/// `sky console-serve` builds and spawns the standalone Sky Console Hub daemon
/// (OTLP receivers plus a SQLite hot store) from `runtime-go/cmd/sky-hub` (pure
/// Go, `CGO_ENABLED=0`). Flags: `--port N`, `--data-dir DIR`, `--auth MODE`, and
/// an optional `--tls-cert F` / `--tls-key F` pair. Mirrors `runConsoleServe`.
fn cmd_console_serve(args: &[String]) -> ExitCode {
    let port = parse_port(args, 4000);
    let data_dir = flag_value(args, "--data-dir").unwrap_or_else(|| "./skyhub-data".to_string());
    let auth = flag_value(args, "--auth").unwrap_or_else(|| "token".to_string());
    let tls_cert = flag_value(args, "--tls-cert");
    let tls_key = flag_value(args, "--tls-key");

    // Validate flag combinations up front (fail fast), mirroring the oracle.
    match (&tls_cert, &tls_key) {
        (Some(_), None) => {
            eprintln!("sky console-serve: --tls-cert set but --tls-key missing");
            return ExitCode::from(2);
        }
        (None, Some(_)) => {
            eprintln!("sky console-serve: --tls-key set but --tls-cert missing");
            return ExitCode::from(2);
        }
        _ => {}
    }
    if auth != "token" && auth != "off" && auth != "app" {
        eprintln!("sky console-serve: unknown --auth mode {auth} (want token|off|app)");
        return ExitCode::from(2);
    }

    let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
    let Some(repo_root) = assets_root_for(&cwd) else {
        return ExitCode::FAILURE;
    };
    let runtime_go = repo_root.join("runtime-go");
    if !runtime_go
        .join("cmd")
        .join("sky-hub")
        .join("main.go")
        .is_file()
    {
        eprintln!(
            "sky console-serve: runtime-go/cmd/sky-hub not found under {}.\n\
             The hub source is embedded in the binary and extracted on first use;\n\
             a missing source here means the embedded asset extraction failed.",
            repo_root.display()
        );
        return ExitCode::from(2);
    }

    // Build the hub binary into the per-version cache (one-time per version).
    let hub_dir = bundled::cache_root().join(format!("hub-{}", version_slug()));
    let hub_bin = hub_dir.join("sky-hub");
    if !hub_bin.is_file() {
        if let Err(e) = std::fs::create_dir_all(&hub_dir) {
            eprintln!("sky console-serve: could not create cache dir: {e}");
            return ExitCode::FAILURE;
        }
        println!(
            "sky console-serve: building hub daemon (one-time per version, into {})...",
            hub_dir.display()
        );
        // CGO_ENABLED=0: rt/hub transitively imports rt (webview.go, cgo+WebKit
        // on darwin); disabling cgo routes through webview_stub.go and dodges the
        // Apple ld_prime long-symbol assertion. The hub never calls webview.
        let status = Command::new("go")
            .args(["build", "-o"])
            .arg(&hub_bin)
            .arg("./cmd/sky-hub")
            .current_dir(&runtime_go)
            .env("CGO_ENABLED", "0")
            .status();
        match status {
            Ok(s) if s.success() => {}
            Ok(s) => {
                eprintln!(
                    "sky console-serve: go build sky-hub failed (exit {})",
                    s.code().unwrap_or(1)
                );
                return ExitCode::FAILURE;
            }
            Err(e) => {
                eprintln!("sky console-serve: could not launch go build: {e}");
                return ExitCode::FAILURE;
            }
        }
    }

    let mut child_args: Vec<String> = vec![
        "--port".to_string(),
        port.to_string(),
        "--data-dir".to_string(),
        data_dir,
        "--auth".to_string(),
        auth,
    ];
    if let (Some(c), Some(k)) = (tls_cert, tls_key) {
        child_args.extend(["--tls-cert".to_string(), c, "--tls-key".to_string(), k]);
    }
    let status = Command::new(&hub_bin).args(&child_args).status();
    match status {
        Ok(s) => propagate(s.code()),
        Err(e) => {
            eprintln!("sky console-serve: could not launch hub: {e}");
            ExitCode::FAILURE
        }
    }
}

// ---- bundled-app helpers -------------------------------------------------

/// Run the built `app` binary at `<out_dir>/app` with inherited stdio + `envs`,
/// foreground, propagating its exit code. Ctrl-C reaches the child (shared
/// process group) so the server stops cleanly; 130/143 (SIGINT/SIGTERM) map to
/// success — a user-initiated stop is not a failure.
fn spawn_foreground(out_dir: &Path, envs: &[(String, String)]) -> ExitCode {
    match run_app(out_dir, envs) {
        Ok(status) => propagate(status.code()),
        Err(e) => {
            eprintln!("sky: could not launch bundled app: {e}");
            ExitCode::FAILURE
        }
    }
}

/// Map a child exit code to an `ExitCode`, treating the signal-terminated cases
/// a foreground server hits on Ctrl-C (130 = SIGINT, 143 = SIGTERM) as success.
fn propagate(code: Option<i32>) -> ExitCode {
    match code {
        Some(0) | Some(130) | Some(143) | None => ExitCode::SUCCESS,
        Some(n) => ExitCode::from(n as u8),
    }
}

/// The message emitted when a bundled verb can't find its `sky-bundled/<name>`
/// source. The source is embedded in the binary and extracted on first use, so
/// this only fires if the embedded asset extraction failed.
fn bundled_missing(name: &str) -> ExitCode {
    eprintln!(
        "sky {name}: sky-bundled/{name} source not found.\n\
         The bundled app source is embedded in the binary and extracted on first\n\
         use; a missing source here means the embedded asset extraction failed."
    );
    ExitCode::from(2)
}

/// A filesystem-safe slug of the version string for cache-dir naming
/// (`sky v0.17.10` → `v0.17.10`, `sky dev` → `dev`).
fn version_slug() -> String {
    version_string()
        .trim_start_matches("sky ")
        .chars()
        .map(|c| {
            if c.is_ascii_alphanumeric() || c == '.' || c == '-' {
                c
            } else {
                '_'
            }
        })
        .collect()
}

/// Parse `--port N` / `-p N` / `--port=N` from `args`, falling back to `default`.
fn parse_port(args: &[String], default: u16) -> u16 {
    let mut it = args.iter();
    while let Some(a) = it.next() {
        if a == "--port" || a == "-p" {
            if let Some(v) = it.next() {
                if let Ok(n) = v.parse() {
                    return n;
                }
            }
        } else if let Some(v) = a.strip_prefix("--port=") {
            if let Ok(n) = v.parse() {
                return n;
            }
        }
    }
    default
}

/// Parse a `--flag VALUE` / `--flag=VALUE` string option from `args`.
fn flag_value(args: &[String], flag: &str) -> Option<String> {
    let eq = format!("{flag}=");
    let mut it = args.iter();
    while let Some(a) = it.next() {
        if a == flag {
            return it.next().cloned();
        } else if let Some(v) = a.strip_prefix(&eq) {
            return Some(v.to_string());
        }
    }
    None
}

// ---- db ------------------------------------------------------------------

/// `sky db status` / `sky db migrate` — build the project, then run it once with
/// `SKY_DB_OP` set so the runtime's `Db.migrate` reports/applies migrations and
/// exits before serving. Mirrors `app/Main.hs`'s `Db` handler (which sets the
/// same env var and runs the project). The Std.Db migration engine lives in the
/// Go runtime, so this is a thin build+run+env wrapper — no separate rust DB
/// introspection is needed.
fn cmd_db(args: &[String]) -> ExitCode {
    let op = match args.first().map(String::as_str) {
        Some("status") => "status",
        Some("migrate") => "migrate",
        _ => {
            eprintln!("usage: sky db <status|migrate> [file.sky]");
            return ExitCode::from(2);
        }
    };
    let (positional, out_override) = parse_out(&args[1..]);
    let file = positional
        .first()
        .cloned()
        .unwrap_or_else(|| "src/Main.sky".to_string());
    let file = Path::new(&file);
    let Some((repo_root, project_dir)) = resolve(file) else {
        return ExitCode::FAILURE;
    };
    if is_compiler_repo_root(&project_dir) && out_override.is_none() {
        eprintln!("sky db: refusing to run from the Sky compiler repo root");
        return ExitCode::FAILURE;
    }
    let out_dir_name = out_override.unwrap_or_else(|| "sky-out".to_string());
    let opts = BuildOptions {
        repo_root,
        example_dir: project_dir.clone(),
        out_dir_name: out_dir_name.clone(),
        out_dir_abs: None,
        run: false,
        stdin: None,
    };
    let report = build_example(&opts);
    for w in &report.warnings {
        eprintln!("warning: {w}");
    }
    if !report.emitted {
        eprintln!("sky db: {}", report.note);
        return ExitCode::FAILURE;
    }
    if !report.go_build_ok {
        eprintln!("sky db: go build failed:\n{}", report.go_build_stderr);
        return ExitCode::FAILURE;
    }
    let out_dir = project_dir.join(&out_dir_name);
    match run_app(&out_dir, &[("SKY_DB_OP".to_string(), op.to_string())]) {
        Ok(status) => ExitCode::from(status.code().unwrap_or(1) as u8),
        Err(e) => {
            eprintln!("sky db: could not launch binary: {e}");
            ExitCode::FAILURE
        }
    }
}

// ---- watch ---------------------------------------------------------------

/// `sky watch <file>` — file-watch the entry dir (+ `tests/` + `sky.toml`),
/// rebuild + restart the app on any `.sky`/`sky.toml` change. Generated trees
/// (`sky-out`, `.skycache`, `.skydeps`, `dist-newstyle`, `.git`, `node_modules`)
/// are excluded (Watch.hs's strict allowlist). Build-error policy: a failing
/// rebuild leaves the previously-running binary alive; the next successful
/// rebuild replaces it. Long-running by design; exits on Ctrl-C.
fn cmd_watch(args: &[String]) -> ExitCode {
    use std::sync::mpsc::channel;
    use std::time::{Duration, Instant};

    let no_run = args.iter().any(|a| a == "--no-run");
    let Some(file) = args.iter().find(|a| !a.starts_with('-')) else {
        eprintln!("usage: sky watch <file.sky> [--no-run]");
        return ExitCode::from(2);
    };
    let file = Path::new(file);
    let Some((repo_root, project_dir)) = resolve(file) else {
        return ExitCode::FAILURE;
    };
    if is_compiler_repo_root(&project_dir) {
        eprintln!("sky watch: refusing to run from the Sky compiler repo root");
        return ExitCode::FAILURE;
    }

    // Watched roots: the entry's directory, the project's tests/ (if present),
    // and the project root (to catch sky.toml). notify watches recursively; the
    // event filter prunes generated dirs + non-source files.
    let entry_dir = file
        .parent()
        .map(Path::to_path_buf)
        .unwrap_or_else(|| project_dir.clone());
    let mut roots: Vec<PathBuf> = vec![entry_dir.clone()];
    let tests_dir = project_dir.join("tests");
    if tests_dir.is_dir() {
        roots.push(tests_dir);
    }
    // The project root covers sky.toml; only add it if it isn't already covered.
    if !roots.iter().any(|r| project_dir.starts_with(r)) {
        roots.push(project_dir.clone());
    }
    roots.sort();
    roots.dedup();

    let (tx, rx) = channel::<()>();
    let handler = {
        let tx = tx.clone();
        move |res: notify::Result<notify::Event>| {
            if let Ok(event) = res {
                if event.paths.iter().any(|p| is_watched_change(p)) {
                    let _ = tx.send(());
                }
            }
        }
    };
    let mut watcher: notify::RecommendedWatcher = match notify::recommended_watcher(handler) {
        Ok(w) => w,
        Err(e) => {
            eprintln!("sky watch: could not create file watcher: {e}");
            return ExitCode::FAILURE;
        }
    };
    use notify::Watcher;
    for root in &roots {
        if let Err(e) = watcher.watch(root, notify::RecursiveMode::Recursive) {
            eprintln!("sky watch: could not watch {}: {e}", root.display());
        }
    }

    println!(
        "[watch] watching {} for changes (Ctrl-C to stop)",
        entry_dir.display()
    );
    let mut child = watch_build_and_spawn(&repo_root, &project_dir, file, no_run);

    // Debounce loop: coalesce a burst of save events, rebuild once.
    loop {
        // Block for the first change.
        if rx.recv().is_err() {
            break;
        }
        // Drain further events for a short debounce window.
        let deadline = Instant::now() + Duration::from_millis(200);
        while let Some(remaining) = deadline.checked_duration_since(Instant::now()) {
            if rx.recv_timeout(remaining).is_err() {
                break;
            }
        }
        println!("[watch] change detected — rebuilding…");
        // Build-error policy: only replace the running child when the rebuild
        // produced a fresh binary. A failing rebuild returns None → the old
        // binary keeps running.
        if let Some(fresh) = watch_build_and_spawn(&repo_root, &project_dir, file, no_run) {
            if let Some(mut old) = child.take() {
                let _ = old.kill();
                let _ = old.wait();
            }
            child = Some(fresh);
        }
    }
    if let Some(mut c) = child.take() {
        let _ = c.kill();
        let _ = c.wait();
    }
    ExitCode::SUCCESS
}

/// One watch iteration: build the entry, and on success (re)spawn the binary.
/// Returns the spawned child (`None` when the build failed or `--no-run`). On a
/// build failure it prints the error and returns `None` so the caller keeps the
/// previous binary alive.
fn watch_build_and_spawn(
    repo_root: &Path,
    project_dir: &Path,
    _file: &Path,
    no_run: bool,
) -> Option<std::process::Child> {
    let opts = BuildOptions {
        repo_root: repo_root.to_path_buf(),
        example_dir: project_dir.to_path_buf(),
        out_dir_name: "sky-out".to_string(),
        out_dir_abs: None,
        run: false,
        stdin: None,
    };
    let report = build_example(&opts);
    for w in &report.warnings {
        eprintln!("[watch] warning: {w}");
    }
    if !report.emitted {
        eprintln!(
            "[watch] build failed: {} (keeping previous binary)",
            report.note
        );
        return None;
    }
    if !report.go_build_ok {
        eprintln!(
            "[watch] go build failed (keeping previous binary):\n{}",
            report.go_build_stderr.trim()
        );
        return None;
    }
    println!("[watch] build ok");
    if no_run {
        return None;
    }
    let out_dir = project_dir.join("sky-out");
    match Command::new("./app").current_dir(&out_dir).spawn() {
        Ok(child) => Some(child),
        Err(e) => {
            eprintln!("[watch] could not launch binary: {e}");
            None
        }
    }
}

/// True when a changed path is a source file the watcher cares about: a `.sky`
/// file or `sky.toml`, and not inside a generated / VCS directory.
fn is_watched_change(path: &Path) -> bool {
    let excluded = path.components().any(|c| {
        matches!(
            c.as_os_str().to_str(),
            Some("sky-out")
                | Some("sky-out-rust")
                | Some(".skycache")
                | Some(".skydeps")
                | Some("dist-newstyle")
                | Some(".git")
                | Some("node_modules")
                | Some(".vscode")
                | Some(".idea")
        )
    });
    if excluded {
        return false;
    }
    let is_sky = path.extension().and_then(|e| e.to_str()) == Some("sky");
    let is_toml = path.file_name().and_then(|n| n.to_str()) == Some("sky.toml");
    is_sky || is_toml
}

// ---- FFI verbs (add / remove / install / update) -------------------------

use project::{ffi_add, ffi_install, ffi_remove, ffi_update, FfiReport};

/// Resolve `(repo_root, project_dir)` for an FFI verb run from the cwd. The
/// project dir is the cwd (where `sky.toml` + `sky-out/` live, matching the
/// oracle's cwd-relative behaviour); the repo root supplies the stdlib +
/// `tools/sky-ffi-inspect` source (bring-up reads assets from the repo tree).
fn resolve_ffi_ctx() -> Option<(PathBuf, PathBuf)> {
    let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
    // Dev reads the inspector source + runtime from the repo tree; standalone
    // extracts the embedded copy (ensure_inspector then `go build`s it, so FFI
    // works outside the repo). See doc 09 §E / §C.3.
    let repo_root = assets_root_for(&cwd)?;
    Some((repo_root, cwd))
}

fn emit_ffi_report(r: FfiReport) -> ExitCode {
    for line in &r.lines {
        println!("{line}");
    }
    if r.ok {
        ExitCode::SUCCESS
    } else {
        ExitCode::FAILURE
    }
}

fn cmd_add(args: &[String]) -> ExitCode {
    let Some(pkg) = args.iter().find(|a| !a.starts_with('-')) else {
        eprintln!("usage: sky add <go-import-path>");
        return ExitCode::from(2);
    };
    let Some((repo_root, project_dir)) = resolve_ffi_ctx() else {
        return ExitCode::FAILURE;
    };
    emit_ffi_report(ffi_add(&project_dir, &repo_root, pkg))
}

fn cmd_remove(args: &[String]) -> ExitCode {
    let Some(pkg) = args.iter().find(|a| !a.starts_with('-')) else {
        eprintln!("usage: sky remove <go-import-path>");
        return ExitCode::from(2);
    };
    let Some((_repo_root, project_dir)) = resolve_ffi_ctx() else {
        return ExitCode::FAILURE;
    };
    emit_ffi_report(ffi_remove(&project_dir, pkg))
}

fn cmd_install(_args: &[String]) -> ExitCode {
    let Some((repo_root, project_dir)) = resolve_ffi_ctx() else {
        return ExitCode::FAILURE;
    };
    emit_ffi_report(ffi_install(&project_dir, &repo_root))
}

fn cmd_update(_args: &[String]) -> ExitCode {
    let Some((repo_root, project_dir)) = resolve_ffi_ctx() else {
        return ExitCode::FAILURE;
    };
    emit_ffi_report(ffi_update(&project_dir, &repo_root))
}

// ---- doctor --------------------------------------------------------------

/// Severity of a doctor finding — drives the output prefix and the exit code.
#[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Debug)]
enum Severity {
    Info,
    Warn,
    Error,
}

/// A single diagnostic finding. Mirrors `Sky.Cli.Doctor.Finding`
/// (`src/Sky/Cli/Doctor.hs`): a short id, severity, message, a one-line manual
/// hint, and an optional safe auto-fix applied only under `--fix`.
struct Finding {
    check: &'static str,
    severity: Severity,
    message: String,
    hint: String,
    fix: Option<Fix>,
}

/// A safe remediation `--fix` may apply. Kept to non-destructive-to-source
/// actions (delete a regenerable cache dir, regen FFI) — never touches user
/// source or `sky.toml` (the oracle's invariant, `Doctor.hs` header).
enum Fix {
    RemoveDir(PathBuf),
    Install,
}

/// `sky doctor [--fix] [--verbose|-v]` — port of `Sky.Cli.Doctor.runDoctor`.
/// Runs the tractable subset of the oracle's checks against the nearest project
/// root: sky.toml present + non-empty, entry file exists, Go toolchain ≥ 1.22,
/// stdlib/runtime assets resolvable, stale `.skycache`/`sky-out`, missing FFI
/// bindings for domain-style imports, and the `SKY_AUTH_TOKEN_SECRET` gate when
/// `[live]`/`[auth]` is configured. Exit 0 = clean, 1 = at least one finding,
/// 2 = no sky.toml visible (diagnostic couldn't run).
fn cmd_doctor(args: &[String]) -> ExitCode {
    let do_fix = args.iter().any(|a| a == "--fix");
    let verbose = args.iter().any(|a| a == "--verbose" || a == "-v");

    let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
    let Some(root) = locate_project_root(&cwd) else {
        eprintln!("sky doctor: no sky.toml found in current directory or any ancestor.");
        eprintln!("            (cd into a project root and re-run, or `sky init` to start one.)");
        return ExitCode::from(2);
    };

    println!("sky doctor — checking {}", root.display());
    println!();

    let mut findings = run_all_checks(&root);
    findings.sort_by_key(|f| f.severity); // Info first, Error last (stable).

    for f in &findings {
        let prefix = match f.severity {
            Severity::Info => "·",
            Severity::Warn => "⚠",
            Severity::Error => "✗",
        };
        println!("{prefix} {}", f.message);
        println!("   ↳ {}", f.hint);
        if verbose {
            println!("   ↳ check-id: {}", f.check);
        }
        println!();
    }

    let mut applied: Vec<String> = Vec::new();
    if do_fix {
        println!("─── applying fixes ─────────────────────────────────────");
        for f in &findings {
            if let Some(fix) = &f.fix {
                applied.push(apply_fix(&root, f.check, fix));
            }
        }
    }
    for line in &applied {
        println!("{line}");
    }
    println!();

    if findings.is_empty() {
        println!("✓ no issues found.");
        return ExitCode::SUCCESS;
    }
    let count = |s: Severity| findings.iter().filter(|f| f.severity == s).count();
    let (n_err, n_warn, n_info) = (
        count(Severity::Error),
        count(Severity::Warn),
        count(Severity::Info),
    );
    let parts: Vec<String> = [(n_err, "errors"), (n_warn, "warnings"), (n_info, "info")]
        .iter()
        .filter(|(n, _)| *n > 0)
        .map(|(n, label)| format!("{n} {label}"))
        .collect();
    let issues = if parts.is_empty() {
        "no issues".to_string()
    } else {
        parts.join(", ")
    };
    if do_fix {
        let n = applied.len();
        println!(
            "{issues}; applied {n} auto-fix{}.",
            if n == 1 { "" } else { "es" }
        );
    } else {
        println!("{issues} — run with --fix to auto-apply safe remediations.");
    }
    ExitCode::from(1)
}

/// Nearest ancestor of `start` (inclusive) containing `sky.toml`.
fn locate_project_root(start: &Path) -> Option<PathBuf> {
    let start = start.canonicalize().unwrap_or_else(|_| start.to_path_buf());
    let mut dir: Option<&Path> = Some(start.as_path());
    while let Some(d) = dir {
        if d.join("sky.toml").is_file() {
            return Some(d.to_path_buf());
        }
        dir = d.parent();
    }
    None
}

fn run_all_checks(root: &Path) -> Vec<Finding> {
    let mut out = Vec::new();
    out.extend(check_sky_toml(root));
    out.extend(check_entry_file(root));
    out.extend(check_go_toolchain());
    out.extend(check_assets(root));
    out.extend(check_stale_cache(root));
    out.extend(check_stale_build(root));
    out.extend(check_missing_ffi(root));
    out.extend(check_auth_secret(root));
    out
}

/// sky.toml exists (root guarantees it) AND is non-empty / readable.
fn check_sky_toml(root: &Path) -> Vec<Finding> {
    let toml = root.join("sky.toml");
    match std::fs::metadata(&toml) {
        Err(e) => vec![Finding {
            check: "sky-toml-unreadable",
            severity: Severity::Error,
            message: format!("sky.toml could not be read: {e}"),
            hint: "ensure file permissions allow reading; recreate from `sky init` if corrupt"
                .into(),
            fix: None,
        }],
        Ok(m) if m.len() == 0 => vec![Finding {
            check: "sky-toml-empty",
            severity: Severity::Error,
            message: "sky.toml is empty".into(),
            hint: "minimal valid file:\n  name = \"myapp\"\n  entry = \"src/Main.sky\"".into(),
            fix: None,
        }],
        Ok(_) => Vec::new(),
    }
}

/// The entry `.sky` (sky.toml `entry`, default `src/Main.sky`) must exist.
fn check_entry_file(root: &Path) -> Vec<Finding> {
    let entry = toml_entry(root).unwrap_or_else(|| "src/Main.sky".to_string());
    let path = root.join(&entry);
    if path.is_file() {
        Vec::new()
    } else {
        vec![Finding {
            check: "entry-missing",
            severity: Severity::Error,
            message: format!("entry file `{entry}` does not exist"),
            hint: "create it, or fix the `entry = \"...\"` path in sky.toml".into(),
            fix: None,
        }]
    }
}

/// Go toolchain present + ≥ 1.22 (generics + range-over-func the runtime needs).
fn check_go_toolchain() -> Vec<Finding> {
    match Command::new("go").arg("version").output() {
        Err(_) => vec![Finding {
            check: "go-toolchain",
            severity: Severity::Error,
            message: "`go` not found on PATH".into(),
            hint: "install Go ≥ 1.22 (https://go.dev/dl/) and re-run".into(),
            fix: None,
        }],
        Ok(o) if o.status.success() => {
            let out = String::from_utf8_lossy(&o.stdout);
            match parse_go_version(&out) {
                Some((maj, minor)) if maj > 1 || (maj == 1 && minor >= 22) => Vec::new(),
                Some((maj, minor)) => vec![Finding {
                    check: "go-toolchain",
                    severity: Severity::Error,
                    message: format!("Go {maj}.{minor} is too old — Sky's runtime needs ≥ 1.22"),
                    hint: "upgrade Go: https://go.dev/dl/".into(),
                    fix: None,
                }],
                None => Vec::new(), // couldn't parse — don't false-positive.
            }
        }
        Ok(o) => vec![Finding {
            check: "go-toolchain",
            severity: Severity::Warn,
            message: format!(
                "`go version` failed: {}",
                String::from_utf8_lossy(&o.stderr)
                    .lines()
                    .next()
                    .unwrap_or("")
            ),
            hint: "check `go` is installed + on PATH".into(),
            fix: None,
        }],
    }
}

/// Parse the leading "go1.X.Y" from `go version` output → (major, minor).
fn parse_go_version(s: &str) -> Option<(u32, u32)> {
    let idx = s.find("go version go")? + "go version go".len();
    let rest = &s[idx..];
    let maj_str: String = rest.chars().take_while(|c| c.is_ascii_digit()).collect();
    let rest2 = &rest[maj_str.len()..];
    let min_str: String = rest2
        .strip_prefix('.')
        .map(|r| r.chars().take_while(|c| c.is_ascii_digit()).collect())
        .unwrap_or_default();
    Some((maj_str.parse().ok()?, min_str.parse().ok()?))
}

/// The stdlib + Go runtime asset root must be resolvable (repo tree or embedded).
/// Silent when healthy — only a missing asset root is a finding.
fn check_assets(root: &Path) -> Vec<Finding> {
    match assets_root_for(root) {
        Some(_) => Vec::new(),
        None => vec![Finding {
            check: "assets-root",
            severity: Severity::Error,
            message: "could not resolve the stdlib + Go runtime asset root".into(),
            hint: "run inside the Sky repo tree, or reinstall the `sky` binary (embedded assets missing)".into(),
            fix: None,
        }],
    }
}

/// `.skycache/` older than the newest `src/*.sky` → stale; safe to delete.
fn check_stale_cache(root: &Path) -> Vec<Finding> {
    let cache = root.join(".skycache");
    if !cache.is_dir() {
        return Vec::new();
    }
    match (newest_mtime(&cache), newest_sky_mtime(&root.join("src"))) {
        (Some(cm), Some(sm)) if sm > cm => vec![Finding {
            check: "stale-cache",
            severity: Severity::Warn,
            message: ".skycache/ is older than your src/*.sky files".into(),
            hint: "run `sky doctor --fix` to delete it (next build regenerates)".into(),
            fix: Some(Fix::RemoveDir(cache)),
        }],
        _ => Vec::new(),
    }
}

/// `sky-out/main.go` older than the newest `src/*.sky` → stale build (Info).
fn check_stale_build(root: &Path) -> Vec<Finding> {
    let out_dir = root.join("sky-out");
    let main_go = out_dir.join("main.go");
    if !main_go.is_file() {
        return Vec::new();
    }
    match (file_mtime(&main_go), newest_sky_mtime(&root.join("src"))) {
        (Some(gm), Some(sm)) if sm > gm => vec![Finding {
            check: "stale-build",
            severity: Severity::Info,
            message: "sky-out/main.go is older than your src/*.sky files".into(),
            hint: "run `sky build` to refresh, or `sky doctor --fix` to remove sky-out/".into(),
            fix: Some(Fix::RemoveDir(out_dir)),
        }],
        _ => Vec::new(),
    }
}

/// Domain-style imports (github.com/…, golang.org/…) with no matching cached
/// FFI surface → the build will fail with a cryptic "package not found".
fn check_missing_ffi(root: &Path) -> Vec<Finding> {
    let src = root.join("src");
    if !src.is_dir() {
        return Vec::new();
    }
    let mut imports: Vec<String> = Vec::new();
    let mut files = Vec::new();
    collect_sky_files(&src, &mut files);
    for f in &files {
        let Ok(c) = std::fs::read_to_string(f) else {
            continue;
        };
        for line in c.lines() {
            let mut it = line.split_whitespace();
            if it.next() == Some("import") {
                if let Some(pkg) = it.next() {
                    if is_ffi_path(pkg) && !imports.contains(&pkg.to_string()) {
                        imports.push(pkg.to_string());
                    }
                }
            }
        }
    }
    if imports.is_empty() {
        return Vec::new();
    }
    let ffi_cache = root.join(".skycache").join("ffi");
    let cached: Vec<String> = if ffi_cache.is_dir() {
        std::fs::read_dir(&ffi_cache)
            .map(|rd| {
                rd.filter_map(|e| e.ok().map(|e| e.file_name().to_string_lossy().into_owned()))
                    .collect()
            })
            .unwrap_or_default()
    } else {
        Vec::new()
    };
    let missing: Vec<String> = imports
        .into_iter()
        .filter(|imp| {
            let stem: String = imp.chars().take_while(|c| *c != '.').collect();
            !cached.iter().any(|f| f.contains(&stem))
        })
        .collect();
    missing
        .into_iter()
        .map(|pkg| Finding {
            check: "missing-ffi",
            severity: Severity::Warn,
            message: format!("import references {pkg} but no FFI bindings cached for it"),
            hint: "run `sky install` (regenerates `.skycache/ffi/`), or `sky doctor --fix`".into(),
            fix: Some(Fix::Install),
        })
        .collect()
}

fn is_ffi_path(p: &str) -> bool {
    p.contains(".com") || p.contains(".org") || p.contains(".io") || p.contains("google.golang")
}

/// When sky.toml declares `[live]`/`[auth]`, `SKY_AUTH_TOKEN_SECRET` must be
/// ≥ 32 bytes (the runtime hard-fails at boot otherwise).
fn check_auth_secret(root: &Path) -> Vec<Finding> {
    let Ok(c) = std::fs::read_to_string(root.join("sky.toml")) else {
        return Vec::new();
    };
    if !(c.contains("[live]") || c.contains("[auth]")) {
        return Vec::new();
    }
    match std::env::var("SKY_AUTH_TOKEN_SECRET") {
        Ok(s) if s.len() >= 32 => Vec::new(),
        Ok(s) => vec![Finding {
            check: "auth-secret-short",
            severity: Severity::Error,
            message: format!("SKY_AUTH_TOKEN_SECRET is {} bytes — must be ≥ 32", s.len()),
            hint: "export SKY_AUTH_TOKEN_SECRET=\"$(openssl rand -hex 32)\"".into(),
            fix: None,
        }],
        Err(_) => vec![Finding {
            check: "auth-secret-missing",
            severity: Severity::Warn,
            message: "SKY_AUTH_TOKEN_SECRET is unset (Sky.Live / Std.Auth in use)".into(),
            hint: "export SKY_AUTH_TOKEN_SECRET=\"$(openssl rand -hex 32)\"".into(),
            fix: None,
        }],
    }
}

/// Apply one `--fix` remediation, returning a status line.
fn apply_fix(root: &Path, check: &str, fix: &Fix) -> String {
    match fix {
        Fix::RemoveDir(dir) => match std::fs::remove_dir_all(dir) {
            Ok(()) => format!("✓ deleted {}", dir.display()),
            Err(e) => format!("✗ {check}: fix failed — {e}"),
        },
        Fix::Install => match assets_root_for(root) {
            Some(repo_root) => {
                let r = project::ffi_install(root, &repo_root);
                if r.ok {
                    format!("✓ {check}: ran `sky install`")
                } else {
                    format!("✗ {check}: `sky install` reported problems")
                }
            }
            None => format!("✗ {check}: could not resolve assets to run `sky install`"),
        },
    }
}

// ---- upgrade-claude ------------------------------------------------------

/// `sky upgrade-claude` — refresh the cwd's `./CLAUDE.md` from the template
/// (`templates/CLAUDE.md`, from the repo tree in dev or the embedded copy
/// standalone). Port of `Sky.Cli`'s `runUpgradeClaude` (`app/Main.hs:1848`):
/// always overwrites, backs any existing file up to `CLAUDE.md.bak`, and prints
/// the byte delta. Exit 0 on success, 1 if the template can't be located.
fn cmd_upgrade_claude(_args: &[String]) -> ExitCode {
    let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
    let target = cwd.join("CLAUDE.md");

    let Some(bytes) = template_claude_bytes(&cwd) else {
        eprintln!(
            "sky upgrade-claude: could not locate templates/CLAUDE.md\n\
             (run inside the Sky repo tree, or reinstall the `sky` binary)."
        );
        return ExitCode::FAILURE;
    };

    let existed = target.is_file();
    let old_size = if existed {
        std::fs::metadata(&target).map(|m| m.len()).unwrap_or(0)
    } else {
        0
    };
    if existed {
        let bak = cwd.join("CLAUDE.md.bak");
        if let Err(e) = std::fs::rename(&target, &bak) {
            eprintln!("sky upgrade-claude: could not back up existing CLAUDE.md: {e}");
            return ExitCode::FAILURE;
        }
    }
    if let Err(e) = std::fs::write(&target, &bytes) {
        eprintln!("sky upgrade-claude: could not write CLAUDE.md: {e}");
        return ExitCode::FAILURE;
    }
    let new_size = bytes.len();
    let verb = if existed { "Refreshed" } else { "Created" };
    println!(
        "{verb} CLAUDE.md ({old_size} → {new_size} bytes, from {})",
        version_string()
    );
    if existed {
        println!("  previous version saved as CLAUDE.md.bak");
    }
    ExitCode::SUCCESS
}

/// The template CLAUDE.md bytes: the repo `templates/CLAUDE.md` when running in
/// the repo tree, else the copy embedded in the binary (extracted to a temp
/// file and read back).
fn template_claude_bytes(start: &Path) -> Option<Vec<u8>> {
    if let Some(repo_root) = repo_root_for(start) {
        let tmpl = repo_root.join("templates").join("CLAUDE.md");
        if tmpl.is_file() {
            if let Ok(b) = std::fs::read(&tmpl) {
                return Some(b);
            }
        }
    }
    // Embedded fallback (standalone binary): extract to a temp file, read, drop.
    let tmp = std::env::temp_dir().join(format!("sky-claude-{}.md", std::process::id()));
    if project::extract_template("CLAUDE.md", &tmp) {
        let b = std::fs::read(&tmp).ok();
        let _ = std::fs::remove_file(&tmp);
        return b;
    }
    None
}

// ---- verify --------------------------------------------------------------

/// The runtime shape of a verify target, deciding how it is run.
#[derive(Clone, Copy, PartialEq, Eq)]
enum Shape {
    /// HTTP server / Sky.Live — long-running; probed for a live listener.
    Server,
    /// Sky.Tui / Sky.Webview — long-running interactive; run as no-panic.
    LongRunning,
    /// One-shot CLI — must exit cleanly (0, no panic) within the timeout.
    Cli,
}

/// `sky verify [target]` — build AND run each example (or the given project /
/// path), catching the "builds but crashes / hangs at runtime" class that a
/// build-only check misses. Reuses `project::build_example` + a bounded run
/// (thin user-facing wrapper; the exhaustive corpus gate lives in `xtask
/// build-run`). Builds into `sky-out-rust/` so it never clobbers an example's
/// `sky-out/` oracle binary. Non-zero exit on any failure.
fn cmd_verify(args: &[String]) -> ExitCode {
    let (positional, out_override) = parse_out(args);
    let out_dir_name = out_override.unwrap_or_else(|| "sky-out-rust".to_string());
    let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));

    let targets = match resolve_verify_targets(&cwd, positional.first().map(String::as_str)) {
        Ok(t) => t,
        Err(msg) => {
            eprintln!("sky verify: {msg}");
            return ExitCode::from(2);
        }
    };
    if targets.is_empty() {
        eprintln!("sky verify: no targets found");
        return ExitCode::from(2);
    }

    let mut failures = 0usize;
    for dir in &targets {
        let name = dir
            .file_name()
            .and_then(|n| n.to_str())
            .unwrap_or("project")
            .to_string();
        let Some(repo_root) = assets_root_for(dir) else {
            println!("  FAIL assets: {name}");
            failures += 1;
            continue;
        };
        if is_compiler_repo_root(dir) {
            // Guard: never build from the compiler repo root itself.
            continue;
        }
        // Build.
        let opts = BuildOptions {
            repo_root,
            example_dir: dir.clone(),
            out_dir_name: out_dir_name.clone(),
            out_dir_abs: None,
            run: false,
            stdin: None,
        };
        let report = build_example(&opts);
        if !report.emitted {
            println!("  FAIL build: {name} ({})", report.note.trim());
            failures += 1;
            continue;
        }
        if !report.go_build_ok {
            println!("  FAIL go-build: {name}");
            failures += 1;
            continue;
        }
        // Run (bounded).
        let out_dir = dir.join(&out_dir_name);
        match run_verify_target(&name, dir, &out_dir) {
            Ok(note) => println!(
                "  ok: {name}{}",
                if note.is_empty() {
                    String::new()
                } else {
                    format!(" ({note})")
                }
            ),
            Err(reason) => {
                println!("  FAIL run: {name} ({reason})");
                failures += 1;
            }
        }
    }

    println!();
    if failures == 0 {
        println!("verify: {} target(s) passed", targets.len());
        ExitCode::SUCCESS
    } else {
        println!("verify: {failures} of {} target(s) failed", targets.len());
        ExitCode::FAILURE
    }
}

/// Resolve the set of project dirs to verify from `cwd` + an optional target:
/// a named example under `cwd/examples`, an explicit path to a project, all
/// examples under `cwd/examples`, or `cwd` itself when it holds a `sky.toml`.
fn resolve_verify_targets(cwd: &Path, target: Option<&str>) -> Result<Vec<PathBuf>, String> {
    let examples = cwd.join("examples");
    if let Some(t) = target {
        // Explicit path to a project dir?
        let as_path = Path::new(t);
        if as_path.join("sky.toml").is_file() {
            // Absolutise so `.`/relative paths get a real file_name (target name)
            // and an absolute binary path for the spawn step (a relative `app`
            // under `current_dir(out_dir)` would double-nest and fail to spawn).
            let abs = as_path.canonicalize().unwrap_or_else(|_| cwd.join(as_path));
            return Ok(vec![abs]);
        }
        // Named example under examples/.
        let ex = examples.join(t);
        if ex.join("sky.toml").is_file() {
            return Ok(vec![ex]);
        }
        return Err(format!(
            "target `{t}` is not a project dir or a known example"
        ));
    }
    // No target: all examples if examples/ exists, else the cwd project.
    if examples.is_dir() {
        let mut dirs: Vec<PathBuf> = std::fs::read_dir(&examples)
            .map_err(|e| format!("reading examples/: {e}"))?
            .filter_map(|e| e.ok().map(|e| e.path()))
            .filter(|p| p.join("sky.toml").is_file())
            .collect();
        dirs.sort();
        return Ok(dirs);
    }
    if cwd.join("sky.toml").is_file() {
        return Ok(vec![cwd.to_path_buf()]);
    }
    Err("no examples/ directory and no sky.toml in the current directory".to_string())
}

/// Run a built target with a bounded watchdog, classifying failure. Server
/// shapes are probed for a live listener; CLI shapes must exit 0 without a
/// panic; long-running (TUI/Webview) shapes must not panic on start.
fn run_verify_target(name: &str, dir: &Path, out_dir: &Path) -> Result<String, String> {
    if is_gui_example(name) {
        // GUI (Fyne) needs a display + native toolkit at link/run time; the
        // build already succeeded — don't attempt a headless runtime probe.
        return Ok("gui build-only".into());
    }
    let shape = classify_shape(dir);
    let app = out_dir.join("app");
    if !app.is_file() {
        return Err("binary not produced".into());
    }

    match shape {
        Shape::Server => run_server_probe(&app, out_dir),
        Shape::Cli | Shape::LongRunning => run_process_bounded(&app, out_dir, shape),
    }
}

/// Spawn a server target, discover its listening port from its startup line
/// (falling back to the env port for servers that don't announce one), probe a
/// TCP listener, then kill it. Watchdog-bounded on every path.
fn run_server_probe(app: &Path, cwd: &Path) -> Result<String, String> {
    let env_port = free_port().unwrap_or(8000);
    let mut child = match Command::new(app)
        .current_dir(cwd)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .env("SKY_LIVE_PORT", env_port.to_string())
        .env("PORT", env_port.to_string())
        .env("SKY_LIVE_STORE", "memory")
        .env("SKY_CONSOLE_EMBED", "off")
        .env("SKY_DEV_BANNER", "off")
        .env("SKY_LIVE_BANNER", "off")
        .env("ENV", "dev")
        .spawn()
    {
        Ok(c) => c,
        Err(e) => return Err(format!("spawn: {e}")),
    };

    // Read stdout on a thread: parse the announced port live, and accumulate the
    // full text (delivered on EOF) for panic detection. A dev server may spawn a
    // `/_sky/console` grandchild that keeps the pipe fds open, so we never read
    // to EOF synchronously — the thread + bounded recv keep this bounded.
    let (port_rx, out_rx) = spawn_server_stdout(child.stdout.take());
    let err_rx = spawn_drain(child.stderr.take());

    // Wait up to 8s for the announced port; on Disconnected (stdout closed) the
    // server exited before announcing → crash. On Timeout, fall back to env_port
    // (servers that never print a line but do bind the env port).
    let port = match port_rx.recv_timeout(Duration::from_secs(8)) {
        Ok(p) => p,
        Err(std::sync::mpsc::RecvTimeoutError::Timeout) => env_port,
        Err(std::sync::mpsc::RecvTimeoutError::Disconnected) => {
            let _ = child.kill();
            let _ = child.wait();
            let logs = collect_drains(&[out_rx, err_rx]);
            return Err(panic_reason(&logs).unwrap_or_else(|| "server exited on start".into()));
        }
    };

    let deadline = Instant::now() + Duration::from_secs(6);
    let mut connected = false;
    while Instant::now() < deadline {
        if let Ok(Some(_)) = child.try_wait() {
            break; // exited before we connected
        }
        if std::net::TcpStream::connect(("127.0.0.1", port)).is_ok() {
            connected = true;
            break;
        }
        std::thread::sleep(Duration::from_millis(150));
    }
    let _ = child.kill();
    let _ = child.wait();
    let logs = collect_drains(&[out_rx, err_rx]);
    if let Some(r) = panic_reason(&logs) {
        return Err(r);
    }
    if connected {
        Ok(format!("server up on :{port}"))
    } else {
        Err(format!("no listener on :{port} within 6s"))
    }
}

/// Read a server's stdout on a thread: send the first announced listening port
/// over the first channel, and the full accumulated text on EOF over the second
/// (for panic detection). Mirrors `xtask build-run`'s port-lift heuristic.
#[allow(clippy::type_complexity)]
fn spawn_server_stdout(
    pipe: Option<impl Read + Send + 'static>,
) -> (
    std::sync::mpsc::Receiver<u16>,
    std::sync::mpsc::Receiver<String>,
) {
    use std::io::BufRead;
    let (port_tx, port_rx) = std::sync::mpsc::channel();
    let (text_tx, text_rx) = std::sync::mpsc::channel();
    if let Some(p) = pipe {
        std::thread::spawn(move || {
            let mut buf = String::new();
            let mut announced = false;
            let reader = std::io::BufReader::new(p);
            for line in reader.lines().map_while(Result::ok) {
                let low = line.to_lowercase();
                if !announced && (low.contains("listening") || low.contains("starting on port")) {
                    if let Some(port) = last_colon_number(&line).or_else(|| last_number(&line)) {
                        let _ = port_tx.send(port);
                        announced = true;
                    }
                }
                buf.push_str(&line);
                buf.push('\n');
            }
            let _ = text_tx.send(buf);
        });
    }
    (port_rx, text_rx)
}

/// Last `:PORT` in a line (`listening on 127.0.0.1:8000` → 8000).
fn last_colon_number(s: &str) -> Option<u16> {
    s.rsplit(':').find_map(|seg| {
        let digits: String = seg.chars().take_while(|c| c.is_ascii_digit()).collect();
        digits.parse().ok()
    })
}

/// Last bare number in a line (`Server starting on port 8080` → 8080).
fn last_number(s: &str) -> Option<u16> {
    let mut last = None;
    let mut cur = String::new();
    for c in s.chars() {
        if c.is_ascii_digit() {
            cur.push(c);
        } else if !cur.is_empty() {
            last = cur.parse().ok();
            cur.clear();
        }
    }
    if !cur.is_empty() {
        last = cur.parse().ok();
    }
    last
}

/// Run a one-shot / long-running target with a timeout. CLI must exit 0 without
/// a panic; long-running must survive the grace window without panicking.
fn run_process_bounded(app: &Path, cwd: &Path, shape: Shape) -> Result<String, String> {
    let mut child = match Command::new(app)
        .current_dir(cwd)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
    {
        Ok(c) => c,
        Err(e) => return Err(format!("spawn: {e}")),
    };
    let out_rx = spawn_drain(child.stdout.take());
    let err_rx = spawn_drain(child.stderr.take());
    let timeout = if shape == Shape::Cli {
        Duration::from_secs(60)
    } else {
        Duration::from_secs(3)
    };
    let deadline = Instant::now() + timeout;
    loop {
        match child.try_wait() {
            Ok(Some(status)) => {
                let _ = child.wait();
                let logs = collect_drains(&[out_rx, err_rx]);
                if let Some(r) = panic_reason(&logs) {
                    return Err(r);
                }
                return match status.code() {
                    Some(0) => Ok(String::new()),
                    Some(n) => Err(format!("exit {n}")),
                    None => Err("terminated by signal".into()),
                };
            }
            Ok(None) => {
                if Instant::now() >= deadline {
                    // CLI that never exits = hang (fail); long-running that
                    // stays up without panic = pass.
                    let _ = child.kill();
                    let _ = child.wait();
                    let logs = collect_drains(&[out_rx, err_rx]);
                    if let Some(r) = panic_reason(&logs) {
                        return Err(r);
                    }
                    return if shape == Shape::Cli {
                        Err("did not exit within 60s".into())
                    } else {
                        Ok("no-panic".into())
                    };
                }
                std::thread::sleep(Duration::from_millis(100));
            }
            Err(e) => return Err(format!("wait: {e}")),
        }
    }
}

/// Spawn a background thread that drains a child pipe to a String and sends it
/// on EOF. Keeps the main watchdog non-blocking even when a grandchild holds the
/// pipe fd open (the thread may then never finish — bounded by `collect_drains`).
fn spawn_drain(pipe: Option<impl Read + Send + 'static>) -> std::sync::mpsc::Receiver<String> {
    let (tx, rx) = std::sync::mpsc::channel();
    if let Some(mut p) = pipe {
        std::thread::spawn(move || {
            let mut s = String::new();
            let _ = p.read_to_string(&mut s);
            let _ = tx.send(s);
        });
    }
    rx
}

/// Collect whatever the drain threads have produced within a short bound, then
/// give up (a grandchild-held pipe may keep a thread alive indefinitely).
fn collect_drains(rxs: &[std::sync::mpsc::Receiver<String>]) -> String {
    let mut out = String::new();
    for rx in rxs {
        if let Ok(s) = rx.recv_timeout(Duration::from_millis(500)) {
            out.push_str(&s);
            out.push('\n');
        }
    }
    out
}

/// Extract a short reason from a Sky runtime panic line, if present.
fn panic_reason(s: &str) -> Option<String> {
    let line = s
        .lines()
        .find(|l| l.contains("panic:") || l.contains("panicKind="))?;
    if let Some(pos) = line.find("panicKind=") {
        let kind: String = line[pos + "panicKind=".len()..]
            .chars()
            .take_while(|c| !c.is_whitespace())
            .collect();
        return Some(format!("panic: {kind}"));
    }
    let after = line.split("panic:").nth(1).unwrap_or(line).trim();
    Some(format!(
        "panic: {}",
        after.chars().take(60).collect::<String>()
    ))
}

/// A free TCP port on loopback (bind :0, read the assigned port, drop).
fn free_port() -> Option<u16> {
    std::net::TcpListener::bind("127.0.0.1:0")
        .ok()
        .and_then(|l| l.local_addr().ok())
        .map(|a| a.port())
}

/// Classify a target's runtime shape by scanning its entry module's `main`
/// binding, falling back to a whole-`src/` scan. Mirrors the tokens
/// `xtask build-run`'s classifier keys on.
fn classify_shape(dir: &Path) -> Shape {
    let src = dir.join("src");
    let blob = read_src_blob(&src);
    if blob.contains("Server.listen")
        || blob.contains("HttpServer.listen")
        || blob.contains("listenAndServe")
        || blob.contains("Live.app")
        || (blob.contains("notFound") && blob.contains("routes"))
    {
        Shape::Server
    } else if blob.contains("Tui.app")
        || blob.contains("Tui.program")
        || blob.contains("Webview.app")
        || blob.contains("Webview.program")
    {
        Shape::LongRunning
    } else {
        Shape::Cli
    }
}

/// GUI (Fyne) examples: build-only at runtime (need a native display toolkit).
fn is_gui_example(name: &str) -> bool {
    name.contains("fyne") || name.contains("-gui")
}

fn read_src_blob(src: &Path) -> String {
    let mut files = Vec::new();
    collect_sky_files(src, &mut files);
    let mut blob = String::new();
    for f in &files {
        if let Ok(s) = std::fs::read_to_string(f) {
            blob.push_str(&s);
            blob.push('\n');
        }
    }
    blob
}

// ---- doctor/verify fs helpers --------------------------------------------

fn toml_entry(root: &Path) -> Option<String> {
    let c = std::fs::read_to_string(root.join("sky.toml")).ok()?;
    for line in c.lines() {
        let t = line.trim();
        if let Some(rest) = t.strip_prefix("entry") {
            let rest = rest.trim_start();
            if let Some(v) = rest.strip_prefix('=') {
                let v = v.trim();
                return Some(v.trim_matches(|c| c == '"' || c == '\'').to_string());
            }
        }
    }
    None
}

fn collect_sky_files(dir: &Path, out: &mut Vec<PathBuf>) {
    let Ok(rd) = std::fs::read_dir(dir) else {
        return;
    };
    for e in rd.flatten() {
        let p = e.path();
        if p.is_dir() {
            collect_sky_files(&p, out);
        } else if p.extension().and_then(|x| x.to_str()) == Some("sky") {
            out.push(p);
        }
    }
}

fn file_mtime(p: &Path) -> Option<std::time::SystemTime> {
    std::fs::metadata(p).and_then(|m| m.modified()).ok()
}

fn newest_mtime(dir: &Path) -> Option<std::time::SystemTime> {
    let mut newest: Option<std::time::SystemTime> = None;
    fn walk(dir: &Path, newest: &mut Option<std::time::SystemTime>) {
        let Ok(rd) = std::fs::read_dir(dir) else {
            return;
        };
        for e in rd.flatten() {
            let p = e.path();
            if p.is_dir() {
                walk(&p, newest);
            } else if let Some(t) = file_mtime(&p) {
                if newest.is_none() || Some(t) > *newest {
                    *newest = Some(t);
                }
            }
        }
    }
    walk(dir, &mut newest);
    newest
}

fn newest_sky_mtime(dir: &Path) -> Option<std::time::SystemTime> {
    let mut files = Vec::new();
    collect_sky_files(dir, &mut files);
    files.iter().filter_map(|f| file_mtime(f)).max()
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
    // Dev: assets live in the repo tree above `file`. Standalone: fall back to
    // the trees embedded in the binary, extracted to a cache dir (doc 09 §E).
    let repo_root = assets_root_for(file)?;
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
         \x20 init  [name]     scaffold a new project\n\
         \x20 doc   <Module>   print a module's exported bindings\n\
         \x20 doc   --serve|--tui  browse the docs (HTTP server / terminal)\n\
         \x20 console [--port N] [--tui]   run the Sky Console mini-app\n\
         \x20 console-serve [...]          run the Sky Console hub daemon\n\
         \x20 watch <file>     rebuild + restart on source change\n\
         \x20 db    <status|migrate> [file]  Std.Db migrations\n\
         \x20 add    <import-path>  inspect a Go pkg → commit its FFI surface\n\
         \x20 remove <import-path>  drop a Go pkg's FFI surface + dep\n\
         \x20 install               regen/verify committed FFI surfaces\n\
         \x20 update                bump Go deps + regen surfaces\n\
         \x20 doctor [--fix] [-v]  diagnose project / environment health\n\
         \x20 upgrade-claude       refresh ./CLAUDE.md from the embedded template\n\
         \x20 verify [target]      build + run each example / the project\n\
         \x20 version          print the version\n\n\
         DEFERRED (bring-up): upgrade"
    );
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn go_version_parses_major_minor() {
        assert_eq!(
            parse_go_version("go version go1.22.3 darwin/arm64"),
            Some((1, 22))
        );
        assert_eq!(
            parse_go_version("go version go1.21.0 linux/amd64"),
            Some((1, 21))
        );
        assert_eq!(parse_go_version("go version go2.0.1 x"), Some((2, 0)));
        assert_eq!(parse_go_version("garbage"), None);
    }

    #[test]
    fn ffi_path_detects_domain_imports() {
        assert!(is_ffi_path("github.com/stripe/stripe-go"));
        assert!(is_ffi_path("golang.org/x/term"));
        assert!(!is_ffi_path("Std.Db"));
        assert!(!is_ffi_path("Sky.Core.List"));
    }

    #[test]
    fn panic_reason_extracts_kind() {
        assert_eq!(
            panic_reason("boot ok\nSky panic: panicKind=DivisionByZero errId=abcd"),
            Some("panic: DivisionByZero".to_string())
        );
        assert!(panic_reason("all fine\nlistening on :8000").is_none());
    }

    #[test]
    fn toml_entry_reads_entry_key() {
        let dir = std::env::temp_dir().join(format!("sky-doctor-test-{}", std::process::id()));
        let _ = std::fs::create_dir_all(&dir);
        std::fs::write(
            dir.join("sky.toml"),
            "name = \"x\"\nentry = \"src/App.sky\"\n",
        )
        .unwrap();
        assert_eq!(toml_entry(&dir).as_deref(), Some("src/App.sky"));
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn parse_port_reads_all_forms_else_default() {
        let sp = |s: &str| s.split(' ').map(String::from).collect::<Vec<_>>();
        assert_eq!(parse_port(&sp("--port 9000"), 8025), 9000);
        assert_eq!(parse_port(&sp("-p 9001"), 8025), 9001);
        assert_eq!(parse_port(&sp("--port=9002"), 8025), 9002);
        assert_eq!(parse_port(&sp("--tui"), 8025), 8025);
        // A non-numeric value falls back to the default rather than aborting.
        assert_eq!(parse_port(&sp("--port abc"), 4000), 4000);
    }

    #[test]
    fn flag_value_reads_space_and_eq_forms() {
        let sp = |s: &str| s.split(' ').map(String::from).collect::<Vec<_>>();
        assert_eq!(
            flag_value(&sp("--data-dir /tmp/x"), "--data-dir").as_deref(),
            Some("/tmp/x")
        );
        assert_eq!(
            flag_value(&sp("--auth=off"), "--auth").as_deref(),
            Some("off")
        );
        assert_eq!(flag_value(&sp("--port 1"), "--auth"), None);
    }

    #[test]
    fn severity_orders_info_before_error() {
        let mut v = vec![Severity::Error, Severity::Info, Severity::Warn];
        v.sort();
        assert_eq!(v, vec![Severity::Info, Severity::Warn, Severity::Error]);
    }
}

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
    assets_root_for, build_example, is_compiler_repo_root, project_dir_for, repo_root_for, run_app,
    BuildOptions,
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
        Some("init") => cmd_init(&args[1..]),
        Some("doc") => cmd_doc(&args[1..]),
        Some("watch") => cmd_watch(&args[1..]),
        Some("db") => cmd_db(&args[1..]),
        Some("add") => cmd_add(&args[1..]),
        Some("remove") => cmd_remove(&args[1..]),
        Some("install") => cmd_install(&args[1..]),
        Some("update") => cmd_update(&args[1..]),
        // Verbs the bring-up does not implement yet — honest, explicit deferral.
        // `doctor`/`console`/`console-serve`/`upgrade`/`verify` spawn bundled
        // Sky apps or self-update the binary (a separate milestone).
        Some(
            verb @ ("doctor" | "console" | "console-serve" | "upgrade" | "upgrade-claude"
            | "verify"),
        ) => {
            eprintln!(
                "sky {verb}: not yet implemented in the rust bring-up.\n\
                 Wired verbs: build, run, check, fmt, test, lsp, clean, init, doc,\n\
                 watch, db, add, remove, install, update, version, help."
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
    if args.iter().any(|a| a == "--serve" || a == "--tui") {
        eprintln!(
            "sky doc --serve / --tui: deferred in the rust bring-up.\n\
             These spawn a bundled Sky.Http.Server / Sky.Tui app (a separate\n\
             milestone). The terminal renderer (`sky doc <Module>` / `--list`)\n\
             is available."
        );
        return ExitCode::from(2);
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
    let entry_dir = file.parent().map(Path::to_path_buf).unwrap_or_else(|| project_dir.clone());
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

    println!("[watch] watching {} for changes (Ctrl-C to stop)", entry_dir.display());
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
        eprintln!("[watch] build failed: {} (keeping previous binary)", report.note);
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
            Some("sky-out") | Some("sky-out-rust") | Some(".skycache") | Some(".skydeps")
                | Some("dist-newstyle") | Some(".git") | Some("node_modules") | Some(".vscode")
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
         \x20 watch <file>     rebuild + restart on source change\n\
         \x20 db    <status|migrate> [file]  Std.Db migrations\n\
         \x20 add    <import-path>  inspect a Go pkg → commit its FFI surface\n\
         \x20 remove <import-path>  drop a Go pkg's FFI surface + dep\n\
         \x20 install               regen/verify committed FFI surfaces\n\
         \x20 update                bump Go deps + regen surfaces\n\
         \x20 version          print the version\n\n\
         DEFERRED (bring-up): doctor, console, console-serve, upgrade, verify"
    );
}

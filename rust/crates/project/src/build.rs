//! The `sky build` driver (doc 08 §"What codegen guarantees", doc 09 §A.1):
//! parse + resolve + typecheck + lower + emit → write `sky-out/main.go` + `go.mod`,
//! materialise a pruned copy of `runtime-go/rt` beside it, then run `go build`.
//! The runtime tree is copied wholesale, never regenerated (L10).

use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

/// Hard ceiling on a single `go build` invocation (CLAUDE.md §3 — every
/// long-running command MUST be timeout-bounded). A wedged Go toolchain (stuck
/// linker, hung module fetch) is killed rather than hanging the CLI. 600 s is
/// deliberately generous — the Stripe-SDK-scale example (13-skyshop) builds well
/// under a minute; anything past 10 min is a genuine hang, not a slow build.
const GO_BUILD_TIMEOUT: Duration = Duration::from_secs(600);

/// Where a build writes + what to do after emission.
pub struct BuildOptions {
    pub repo_root: PathBuf,
    pub example_dir: PathBuf,
    /// Output dir name under the example (kept distinct from the oracle's
    /// `sky-out/` so a comparison harness can hold both).
    pub out_dir_name: String,
    /// When set, the build writes here VERBATIM instead of
    /// `example_dir.join(out_dir_name)`. This is how `sky test` (and any verb
    /// that synthesises a temporary entry) directs output to a scratch dir so
    /// it can NEVER clobber a project's real `sky-out/` (which, for the
    /// examples, holds their oracle binaries). `example_dir` still points at the
    /// real project so its `src/`, FFI surface, and go.mod version pins load
    /// correctly — only the *output* location moves.
    pub out_dir_abs: Option<PathBuf>,
    pub run: bool,
    /// stdin to feed the binary when `run` is set.
    pub stdin: Option<String>,
    /// The entry module name (from the entry file's `module <Name>` header).
    /// `None` falls back to the `Main`/`main` heuristic — so a renamed entry
    /// module (`module App`) still builds when the CLI derives + supplies it.
    pub entry_module: Option<String>,
    /// When true, emit the phased pipeline progress log (`-- Discovering
    /// modules` / `-- Parsing` / `-- Canonicalising` / `-- Type Checking` /
    /// `-- Generating Go` / `Sky lowering succeeded`) to stdout — the
    /// interactive `sky build`/`run`/`check` UX, mirroring the Haskell oracle.
    /// Batch gates + synthesised-entry verbs leave it `false` for quiet builds.
    pub progress: bool,
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
    /// One-line note on which cgo mode `go build` used (Some when it's worth
    /// surfacing — the cgo-forced Sky.Webview path, or a successful cgo retry
    /// after the preferred static build failed). `None` for the common
    /// static-first success. The CLI prints it; batch gates ignore it.
    pub cgo_note: Option<String>,
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
    /// True when the emitted `main.go` blank-imports `sky-app/rt/console_app`
    /// (Sky.Live / Sky.Http.Server app) — the driver must then materialise the
    /// `rt/console_app` subpackage so the import resolves at `go build`.
    console_needed: bool,
}

/// Assemble the source db (stdlib + example src), lower, and emit the Go source.
/// Returns `Err(note)` for every non-emit outcome (no stdlib, no src, no entry,
/// no `main`) so callers surface the same diagnostics. Deterministic: no wall
/// clock, no environment reads reach the emitted bytes.
fn assemble_and_emit(repo_root: &Path, example_dir: &Path) -> Result<Emitted, String> {
    assemble_and_emit_with(repo_root, example_dir, &[], None, false)
}

/// [`assemble_and_emit`] with two additive knobs used by `sky test`
/// (`testrunner`): `extra_dirs` are further source roots to load alongside
/// `src/` (e.g. the project's `tests/` tree holding the suite module), and
/// `entry_module` overrides the entry-detection heuristic with an explicit
/// module name (the synthesised `SkyTestEntry__`) so the entry is unambiguous
/// even when a real `Main` also exists under `src/`.
fn assemble_and_emit_with(
    repo_root: &Path,
    example_dir: &Path,
    extra_dirs: &[PathBuf],
    entry_module: Option<&str>,
    progress: bool,
) -> Result<Emitted, String> {
    // ---- assemble the source db (stdlib + example src) ----
    // Stage A/B (doc 01, doc 12): a salsa db holds the source set as `SourceFile`
    // inputs and the `parse` leaf query memoises each module's CST. Every parse
    // below flows through this input+query rather than an inline `syntax::parse`,
    // making the query DAG's leaf load-bearing on the build path. `next_id` mints
    // a distinct `file_id` per module in load order (no span/file-id reaches
    // emitted Go, so the routing is byte-identical to the prior inline parse).
    // The salsa db now holds the WHOLE module set (the resolve-stage port): each
    // module is a `SourceFile` input, `parse`/`module_exports` are tracked queries,
    // and `DefId`s are `#[salsa::interned]`. `load_dir` mints the inputs under a
    // shared `&db` borrow that closes before each `&mut db` registration.
    let mut db = skydb::SkyDatabase::with_kernel();
    let mut next_id: u32 = 0;
    let stdlib = load_dir(&db, &mut next_id, &repo_root.join("sky-stdlib"));
    if stdlib.is_empty() {
        return Err("no stdlib under sky-stdlib".into());
    }
    for (n, file, _p) in stdlib {
        db.add_module(&n, file);
    }
    // Sky-package dependencies (`[dependencies]` in sky.toml, e.g.
    // `github.com/anzellai/sky-tailwind`) are fetched as Sky *source* under
    // `.skydeps/<pkg>/src/`. Load those modules into the db so imports like
    // `import Tailwind exposing (..)` resolve to the real bindings rather than
    // falling through to a `Basics` kernel guess. Loaded BEFORE the example's
    // own src so a dep module named `Main` (packages ship their own demo entry)
    // is overwritten by — and never shadows — the example's real `Main`.
    // Read-only guard: `sky build` never network-clones. A declared
    // `[dependencies]` whose `.skydeps/<slug>/` tree is absent means the Sky
    // package was never fetched — surface an actionable error pointing at `sky
    // install` rather than silently mis-resolving `import <Pkg>` to a kernel
    // guess. An empty/absent `[dependencies]` section is a no-op (the ~48
    // no-Sky-dep examples must not regress).
    for (path, _spec) in crate::ffi_ops::read_sky_dependencies(&example_dir.join("sky.toml")) {
        let slug = path.replace('/', "_");
        if !example_dir.join(".skydeps").join(&slug).is_dir() {
            return Err(format!(
                "Sky dependency {path} not fetched — run 'sky install'"
            ));
        }
    }
    for (n, file) in load_skydeps(&db, &mut next_id, &example_dir.join(".skydeps")) {
        db.add_module(&n, file);
    }

    let mut locals = load_dir(&db, &mut next_id, &example_dir.join("src"));
    for dir in extra_dirs {
        locals.extend(load_dir(&db, &mut next_id, dir));
    }
    if locals.is_empty() {
        return Err("no .sky under src/".into());
    }
    // `FileId → display path` for every APP module — feeds the Elm-style renderer
    // so each diagnostic header carries `src/Main.sky:line:col` (matching the
    // oracle) instead of a bare `line:col`. Keyed by the module's `file_id` (its
    // eventual `ModuleId` index, which is exactly what a span's `file` carries).
    // Paths are shown relative to the project dir when possible.
    let path_map: std::collections::HashMap<base::FileId, String> = locals
        .iter()
        .map(|(_n, file, p)| {
            let disp = p
                .strip_prefix(example_dir)
                .unwrap_or(p)
                .to_string_lossy()
                .replace('\\', "/");
            (base::FileId(file.file_id(&db)), disp)
        })
        .collect();
    if progress {
        println!("-- Discovering modules");
        println!("   Found {} project module(s)", locals.len());
        println!("-- Parsing");
    }
    let mut entry = None;
    let mut check_ids: Vec<base::ModuleId> = Vec::new();
    // Parse-error gate accumulator (`[E0001]` class). Collected HERE, in the
    // app-module loop, reading each app module's parse (from the tracked `parse`
    // query, keyed by its `SourceFile` input) BEFORE `&mut db` registration — the
    // `&db` borrow closes before `add_module`. Only APP modules are gated: the
    // stdlib + `.skydeps` parse clean (the `roundtrip` gate asserts 0 ERROR nodes
    // across the whole corpus) and are trusted, exactly like the type/name/
    // exhaustive gates scope to `check_ids`.
    let mut parse_diags: Vec<diagnostics::Diagnostic> = Vec::new();
    for (n, file, _p) in locals {
        // A parser that RECOVERS from a syntax error (e.g. a bare operator
        // section `(+)`, which Sky has no grammar for) emits an `Expr::Error`
        // node that lowers to Go `nil` and panics at runtime — while `sky check`
        // otherwise reports success. The Haskell oracle rejects such a program
        // at exit 1 (`[E0001] PARSE ERROR`). Gate it here so `sky
        // check`/`build`/`run` all reject before lower/emit, upholding both
        // "at least compatible with the oracle" and `sky check ≡ sky build` →
        // "if it compiles it works".
        {
            let parse = skydb::parse(&db, file);
            if !parse.errors().is_empty() || parse.error_node_count() > 0 {
                if parse.errors().is_empty() {
                    // Recovery produced a structural ERROR node without an attached
                    // diagnostic (defensive — the recovery paths always pair the two).
                    parse_diags.push(diagnostics::Diagnostic::error(
                        "E0001",
                        format!(
                            "PARSE ERROR in module {n}: unstructured input (recovered ERROR node)"
                        ),
                    ));
                } else {
                    for d in parse.errors() {
                        if d.severity == diagnostics::Severity::Error {
                            parse_diags.push(d.clone());
                        }
                    }
                }
            }
        }
        let id = db.add_module(&n, file);
        // Every app-code module (the project's own `src/` + any `extra_dirs`
        // like `tests/`) is type-checked. Stdlib + `.skydeps` are trusted
        // signatures, never re-checked — mirrors the `xtask infer` gate, whose
        // zero-type-error accept-parity property this preserves.
        check_ids.push(id);
        let is_entry = match entry_module {
            Some(want) => n == want,
            None => n == "Main" || n.ends_with(".Main") || n == "main",
        };
        if is_entry {
            entry = Some(id);
        }
    }
    // `FileId → source text` for every checked app module — feeds the Elm-style
    // renderer (`Diagnostic::render_cli`) so each Sky-frontend diagnostic shows
    // its offending source line + caret instead of a flat `[code] message`. A
    // diagnostic span's `file.index()` is the module's `ModuleId` index, so the
    // map keys straight off `check_ids`.
    let src_map: std::collections::HashMap<base::FileId, String> = check_ids
        .iter()
        .map(|m| {
            (
                base::FileId(m.index()),
                db.source_file(*m).text(&db).to_string(),
            )
        })
        .collect();
    // Combined source provider: text (for the caret excerpt) + display path (for
    // the `path:line:col` header). Passed to every `render_diags` call below.
    let sources = CliSources {
        text: &src_map,
        paths: &path_map,
    };

    // Halt on any app-module parse error BEFORE entry detection, typecheck,
    // lower, and emit — a syntactically broken module can never be soundly
    // lowered, and reporting the parse error is more actionable than a
    // downstream "no entry module named Main" / type-clash message.
    if !parse_diags.is_empty() {
        return Err(render_diags(&parse_diags, &sources));
    }
    let Some(entry) = entry else {
        return Err(match entry_module {
            Some(want) => format!("no entry module named {want}"),
            None => "no entry module named Main".into(),
        });
    };

    if progress {
        println!("-- Canonicalising");
        println!("   Names resolved");
        println!("-- Type Checking");
    }

    // ---- typecheck (accept/reject gate) ----
    // `sky check ≡ sky build` (CLAUDE.md §8): an ill-typed program the oracle
    // rejects MUST NOT reach lowering/emit. `ty::check_modules` was previously
    // wired only into the LSP + the `xtask infer/reject` gates, so the shipped
    // CLI pipeline accepted programs like `1 + "x"` and emitted Go that panics at
    // runtime. Gate on TYPE-ERROR diagnostics (the `[E2001]` unify-clash class —
    // proven zero across the accept corpus by `xtask infer`, and safe for FFI
    // because a `Res::Foreign` reference infers to a fresh var, never clashing).
    // Halt HERE — before `write_out` + `go build` — so the failure surfaces as a
    // check-time diagnostic. Name-resolution + exhaustiveness handling stays with
    // the existing lowering path; only the type-clash hole is closed here.
    let checked = ty::check_modules(&db, &check_ids);
    if checked.type_errors > 0 {
        let ds: Vec<diagnostics::Diagnostic> = checked
            .diagnostics
            .iter()
            .filter(|d| {
                d.severity == diagnostics::Severity::Error
                    && (d.code.0 == "E2001" || d.code.0 == "E2007")
            })
            .cloned()
            .collect();
        return Err(render_diags(&ds, &sources));
    }
    // Name-resolution rejections (the `[E1xxx]` class over the checked app
    // modules): undefined names (`[E1001]`), duplicate top-level definitions
    // (`[E1002]`), non-linear patterns / duplicate binders (`[E1003]`), and
    // user ADTs/aliases shadowing a Prelude name (`[E1004]`). The oracle rejects
    // each (some only at `go build`: "x redeclared", "no new variables on left
    // side of :="; the shadow class at canonicalise time). Halt HERE — before
    // lower/emit — so a program Go would refuse, or that shadows the Prelude,
    // never reaches codegen (`sky check ≡ sky build`, CLAUDE.md §8). `check_ids`
    // is app-code only, so the canonical stdlib that legitimately defines
    // `Maybe`/`Result`/… is never gated. Undefined names are additionally
    // caught by the lowering `class_a` path (all modules), which stays intact.
    if checked.name_errors > 0 {
        let ds: Vec<diagnostics::Diagnostic> = checked
            .diagnostics
            .iter()
            .filter(|d| d.severity == diagnostics::Severity::Error && d.code.0.starts_with("E1"))
            .cloned()
            .collect();
        return Err(render_diags(&ds, &sources));
    }
    // Exhaustiveness gate (`[E3001]`): Sky treats a non-exhaustive `case` as a
    // HARD error (stronger than GHC-as-configured — self-host R1-D3, doc 06
    // §Exhaustiveness). The Haskell oracle rejects such a program at exit 1; the
    // Rust CLI must match, else a program that "compiles" panics at runtime the
    // moment the missing arm is hit (violates `sky check ≡ sky build` + "if it
    // compiles it works"). `exhaustive.rs` is conservative — it only forces
    // coverage on ADT/Bool heads and a wildcard/var/alias head suppresses — so
    // this gate cannot over-reject an exhaustive match. Gate on the E3001
    // diagnostics directly; the `exhaustiveness_warnings` counter (a separate
    // axis from `type_errors`, which the `infer` accept-parity gate counts) is
    // left untouched.
    let exhaustive_diags: Vec<diagnostics::Diagnostic> = checked
        .diagnostics
        .iter()
        .filter(|d| d.code.0 == "E3001")
        .cloned()
        .collect();
    if !exhaustive_diags.is_empty() {
        return Err(render_diags(&exhaustive_diags, &sources));
    }
    if progress {
        println!("   Types OK ({} module(s))", check_ids.len());
        println!("-- Generating Go");
    }

    // ---- lower + emit ----
    let mut cfg = read_sky_toml_config(&example_dir.join("sky.toml"));
    // Load the pinned Go-FFI surface (doc 09): the committed `sky-ffi/`
    // directory is preferred; the oracle's gitignored `.skycache/` cache is the
    // fallback so a project that hasn't yet migrated to the committed layout
    // still builds. Absent both → an empty table (no FFI).
    let registry = load_ffi_surface(example_dir);
    cfg.ffi = build_ffi_table(&registry);
    // Authoritative kernel arities, scanned from the runtime `rt.*` param counts
    // (`abi_guard::runtime_arities`, cached once per process). The lowerer uses
    // these to eta-expand partially-applied kernels correctly — the curried HM
    // type over-counts for function-returning kernels, so the runtime symbol's
    // actual parameter count is the only sound arity source.
    cfg.kernel_arity = crate::abi_guard::runtime_arities(repo_root).clone();
    // Stage E (doc 01 bottom-of-DAG): route lowering + codegen through the salsa
    // `go_program` tracked query, closing the query DAG below `infer`. The config
    // is a salsa **input** created once for this build; `go_program` reads the
    // whole module set's `type_world`/`resolve`/per-def `infer` through the db, so
    // it is memoised (re-demand is a cache hit) and invalidated natively by any
    // `SourceFile` edit (the LSP/incremental path). Emitted bytes are byte-for-byte
    // the prior eager `lower_program_cfg` + `emit_program` pair.
    let config = skydb::BuildConfig::new(&db, cfg);
    let prog = skydb::go_program(&db, entry, config);
    if !prog.entry_ok {
        return Err("lowering found no entry `main`".into());
    }
    // Hard lowering errors (e.g. a call to a Go-FFI function with no callable
    // wrapper) mean the emitted Go would not build. Abort here — before writing
    // sky-out and invoking `go build` — so the failure surfaces as a check-time
    // diagnostic, upholding the `sky check ≡ sky build` invariant.
    if !prog.errors.is_empty() {
        return Err(prog.errors.join("\n"));
    }
    // `entry_ok && errors.is_empty()` guarantees `go_program` emitted the source.
    // `go_program` returns a reference into the salsa memo, so clone the fields out.
    let source = prog
        .source
        .clone()
        .ok_or_else(|| "lowering produced no Go source".to_string())?;
    // ABI-symbol guard: reject any emitted `rt.X` the runtime does not export
    // BEFORE `go build`, so a codegen hole surfaces as a clean `[E4005]`
    // diagnostic instead of a confusing `undefined: rt.X` Go error. Upholds
    // `sky check ≡ sky build` and is the structural lock for codegen holes.
    let abi_diags =
        crate::abi_guard::check_abi_symbols(&source, crate::abi_guard::runtime_exports(repo_root));
    if !abi_diags.is_empty() {
        return Err(render_diags(&abi_diags, &sources));
    }
    Ok(Emitted {
        source,
        registry,
        ffi_used: prog.ffi_used.clone(),
        warnings: prog.warnings.clone(),
        console_needed: prog.console_needed,
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
    build_inner(opts, &[], opts.entry_module.as_deref())
}

/// Like [`build_example`], but with extra source roots (e.g. the project's
/// `tests/` tree) and an explicit entry-module override — the shape `sky test`
/// needs. Same write + `go build` + optional run tail as [`build_example`].
pub fn build_project(
    opts: &BuildOptions,
    extra_dirs: &[PathBuf],
    entry_module: Option<&str>,
) -> BuildReport {
    build_inner(opts, extra_dirs, entry_module)
}

fn build_inner(
    opts: &BuildOptions,
    extra_dirs: &[PathBuf],
    entry_module: Option<&str>,
) -> BuildReport {
    let mut report = BuildReport::default();

    let (source, registry, ffi_used, console_needed) = match assemble_and_emit_with(
        &opts.repo_root,
        &opts.example_dir,
        extra_dirs,
        entry_module,
        opts.progress,
    ) {
        Ok(e) => {
            report.warnings = e.warnings;
            (e.source, e.registry, e.ffi_used, e.console_needed)
        }
        Err(note) => {
            report.note = note;
            return report;
        }
    };

    // ---- write sky-out + materialise runtime ----
    // An explicit absolute output dir (scratch dir for synthesised-entry verbs
    // like `sky test`) wins; otherwise the conventional `<project>/<name>`.
    let out_dir = opts
        .out_dir_abs
        .clone()
        .unwrap_or_else(|| opts.example_dir.join(&opts.out_dir_name));
    if let Err(e) = write_out(&opts.repo_root, &out_dir, &source, console_needed) {
        report.note = format!("write failed: {e}");
        return report;
    }
    if opts.progress {
        // Match the oracle's audit §3.4 contract: `Sky lowering succeeded` prints
        // once the Go is written, BEFORE `go build` runs; the CLI only prints
        // `Compilation successful` after `go build` returns 0.
        println!("   Wrote {}/main.go", out_dir.display());
        println!("Sky lowering succeeded");
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
    if let Err(e) = inject_ffi_deps(
        &registry,
        &ffi_used,
        &out_dir,
        &opts.example_dir,
        &mut report.warnings,
    ) {
        report.warnings.push(format!("ffi go.mod injection: {e}"));
    }
    report.emitted = true;

    // ---- go build (two-phase cgo detection + bounded) ----
    // Prefer static binaries (CGO_ENABLED=0) so the common pure-Go app ships
    // without libSystem/CoreFoundation/Security dylib deps; retry with cgo when
    // the static build fails (an FFI package that needs cgo links on the retry).
    // A Sky.Webview program flips STRAIGHT to cgo on the first attempt — its
    // `webview_stub.go` compiles cleanly under CGO=0 and the app would silently
    // no-op at runtime, so the static-first probe must be skipped there. Matches
    // the oracle (`app/Main.hs`) + CLAUDE.md §"Sky.Webview" cgo-detect note.
    match run_go_build_detecting_cgo(&out_dir, &source) {
        Ok(outcome) => {
            report.go_build_ok = outcome.ok;
            report.go_build_stderr = outcome.stderr;
            report.cgo_note = outcome.cgo_note;
        }
        Err(e) => {
            report.go_build_stderr = e;
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

/// Result of the two-phase `go build` (never panics; `Err` is a spawn/timeout
/// failure that aborts the build entirely).
struct GoBuildOutcome {
    ok: bool,
    /// Combined stderr worth surfacing on failure (both attempts on the retry
    /// path so the user sees why static failed AND why cgo failed).
    stderr: String,
    /// See [`BuildReport::cgo_note`].
    cgo_note: Option<String>,
}

/// Run `go build` with static-first cgo detection. `source` is the emitted
/// `main.go` text; its containing `rt.Webview_app` reference is the signal that
/// the project links the system webview and MUST build with cgo.
fn run_go_build_detecting_cgo(out_dir: &Path, source: &str) -> Result<GoBuildOutcome, String> {
    // Sky.Webview: the stub (`webview_stub.go`, `!cgo || !darwin`) compiles fine
    // under CGO=0, producing a binary that silently no-ops on `Webview.app`.
    // Force cgo up front so the real WKWebView-backed `webview.go` links.
    if source.contains("rt.Webview_app") {
        let attempt = run_go_build_once(out_dir, "1")?;
        return Ok(GoBuildOutcome {
            ok: attempt.status_ok,
            stderr: attempt.stderr,
            cgo_note: Some(
                "(built with cgo — Sky.Webview requires it; the static build would link the stub and no-op at runtime)"
                    .to_string(),
            ),
        });
    }

    // Preferred path: static, pure-Go binary.
    let static_attempt = run_go_build_once(out_dir, "0")?;
    if static_attempt.status_ok {
        return Ok(GoBuildOutcome {
            ok: true,
            stderr: static_attempt.stderr,
            cgo_note: None,
        });
    }

    // The static build failed — an FFI package may require cgo. Retry with it.
    let cgo_attempt = run_go_build_once(out_dir, "1")?;
    if cgo_attempt.status_ok {
        return Ok(GoBuildOutcome {
            ok: true,
            stderr: cgo_attempt.stderr,
            cgo_note: Some(
                "(built with cgo — the preferred static CGO_ENABLED=0 build failed; an FFI package requires cgo)"
                    .to_string(),
            ),
        });
    }

    // Both failed: surface both diagnostics so the root cause is visible.
    Ok(GoBuildOutcome {
        ok: false,
        stderr: format!(
            "static build (CGO_ENABLED=0) failed:\n{}\ncgo retry (CGO_ENABLED=1) also failed:\n{}",
            static_attempt.stderr.trim_end(),
            cgo_attempt.stderr.trim_end()
        ),
        cgo_note: None,
    })
}

/// Single bounded `go build -o app .` under an explicit `CGO_ENABLED`. `Err` is
/// a spawn failure or the timeout tripping (a hung toolchain — CLAUDE.md §3); a
/// non-zero Go exit is a normal `status_ok = false` outcome, not an `Err`.
struct GoBuildAttempt {
    status_ok: bool,
    stderr: String,
}

fn run_go_build_once(out_dir: &Path, cgo: &str) -> Result<GoBuildAttempt, String> {
    let mut cmd = Command::new("go");
    cmd.arg("build")
        .arg("-o")
        .arg("app")
        .arg(".")
        .current_dir(out_dir)
        .env("GOFLAGS", "-mod=mod")
        .env("CGO_ENABLED", cgo);
    match run_bounded(cmd, GO_BUILD_TIMEOUT) {
        Ok(b) if b.timed_out => Err(format!(
            "go build (CGO_ENABLED={cgo}) exceeded {}s and was killed — the Go toolchain hung (stuck linker / module fetch). Partial stderr:\n{}",
            GO_BUILD_TIMEOUT.as_secs(),
            b.stderr.trim_end()
        )),
        Ok(b) => Ok(GoBuildAttempt {
            status_ok: b.status.map(|s| s.success()).unwrap_or(false),
            stderr: b.stderr,
        }),
        Err(e) => Err(format!("go build (CGO_ENABLED={cgo}) spawn failed: {e}")),
    }
}

/// Outcome of a bounded child process (shared bounded-run helper — factored so
/// the same kill-on-deadline machinery `sky verify` uses covers `go build`).
struct BoundedOutcome {
    /// `None` when the child was killed on timeout.
    status: Option<std::process::ExitStatus>,
    stderr: String,
    timed_out: bool,
}

/// Spawn `cmd`, drain its stderr on a background thread, and wait up to
/// `timeout` — killing the child (and reaping it) if the deadline passes. stdout
/// is piped-and-dropped so a chatty child can't block on a full pipe. Returns
/// the exit status (or `timed_out`) plus captured stderr.
fn run_bounded(mut cmd: Command, timeout: Duration) -> std::io::Result<BoundedOutcome> {
    cmd.stdout(Stdio::piped()).stderr(Stdio::piped());
    let mut child = cmd.spawn()?;
    let stdout = child.stdout.take();
    let stderr = child.stderr.take();
    let out_h = spawn_pipe_reader(stdout);
    let err_h = spawn_pipe_reader(stderr);

    let deadline = Instant::now() + timeout;
    loop {
        match child.try_wait()? {
            Some(status) => {
                let _ = out_h.join();
                let stderr = err_h.join().unwrap_or_default();
                return Ok(BoundedOutcome {
                    status: Some(status),
                    stderr,
                    timed_out: false,
                });
            }
            None => {
                if Instant::now() >= deadline {
                    let _ = child.kill();
                    let _ = child.wait();
                    let _ = out_h.join();
                    let stderr = err_h.join().unwrap_or_default();
                    return Ok(BoundedOutcome {
                        status: None,
                        stderr,
                        timed_out: true,
                    });
                }
                std::thread::sleep(Duration::from_millis(50));
            }
        }
    }
}

/// Drain a child pipe to a String on its own thread so `run_bounded` never
/// blocks on a full OS pipe buffer while polling the deadline.
fn spawn_pipe_reader(
    pipe: Option<impl std::io::Read + Send + 'static>,
) -> std::thread::JoinHandle<String> {
    std::thread::spawn(move || {
        let mut s = String::new();
        if let Some(mut p) = pipe {
            let _ = p.read_to_string(&mut s);
        }
        s
    })
}

/// Minimal `sky.toml` reader for the build-time `init()` defaults. Extracts
/// top-level `port`, the `[database]` `driver`/`path`, and the `[live]` runtime
/// keys — emitted as `rt.SetSkyDefault(<suffix>, value)` so the runtime honours
/// them WITHOUT the user setting the matching `SKY_*` env var. The suffix matches
/// the runtime's `skyGetenv`/env read (`store` → `LIVE_STORE`, read by
/// `chooseStore`; `static` → `LIVE_STATIC_DIR`; etc.). A full TOML parse isn't
/// warranted for these flat keys; unknown shapes are ignored.
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
            section = line
                .trim_matches(['[', ']'])
                .trim()
                .trim_matches('"')
                .to_string();
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
            // `[live]` runtime keys → the suffixes the runtime reads (live.go /
            // live_store.go). Without these, only the `SKY_LIVE_*` env vars were
            // honoured and the sky.toml keys were silently ignored.
            ("live", "static") => cfg.extra_defaults.push(("LIVE_STATIC_DIR".into(), val)),
            ("live", "store") => cfg.extra_defaults.push(("LIVE_STORE".into(), val)),
            ("live", "storePath") => cfg.extra_defaults.push(("LIVE_STORE_PATH".into(), val)),
            ("live", "ttl") => cfg.extra_defaults.push(("LIVE_TTL".into(), val)),
            ("live", "maxBodyBytes") => {
                cfg.extra_defaults.push(("LIVE_MAX_BODY_BYTES".into(), val))
            }
            // `[auth]` keys (canonical names per docs/sky-toml.md) → the suffixes
            // the runtime's fixed AUTH defaults use, so sky.toml overrides them
            // (the prologue emits these fallbacks AFTER extra_defaults). `secret`
            // is deliberately NOT seeded from sky.toml — it must come from env.
            ("auth", "cookieName") => cfg.extra_defaults.push(("AUTH_COOKIE".into(), val)),
            ("auth", "tokenTtl") => cfg.extra_defaults.push(("AUTH_TOKEN_TTL".into(), val)),
            ("auth", "driver") => cfg.extra_defaults.push(("AUTH_DRIVER".into(), val)),
            // [log] → the suffixes Std.Log reads (skyGetenv LOG_FORMAT/LOG_LEVEL).
            ("log", "format") => cfg.extra_defaults.push(("LOG_FORMAT".into(), val)),
            ("log", "level") => cfg.extra_defaults.push(("LOG_LEVEL".into(), val)),
            // [env] prefix re-namespaces every runtime SKY_* read; the compiler
            // must emit `rt.SetEnvPrefix(...)` for it to take effect (the Rust
            // compiler previously emitted nothing, so it was silently ignored).
            ("env", "prefix") => cfg.env_prefix = Some(val),
            _ => {}
        }
    }
    cfg
}

// ---- FFI surface loading + binding materialisation (doc 09) --------------

/// Locate the pinned FFI surface for an example: prefer the committed
/// `sky-ffi/` layout (doc 09 §C.1); fall back to the oracle's `.skycache/`
/// cache. Returns the loaded registry (empty when neither exists).
pub fn load_ffi_surface(example_dir: &Path) -> ffi::FfiRegistry {
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
                wrapper_params: pkg.wrapper_params.clone(),
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

/// Add a `require` for every external Go-FFI package the program calls, honoring
/// the version declared in the project's `sky.toml [go.dependencies]`. Stdlib
/// packages (`net/http`, `io`, `os`) never need a require and are skipped.
///
/// The declared spec is the source of truth — NOT `sky-out/go.mod`, which
/// `write_out` has already clobbered with `runtime-go/go.mod` by the time this
/// runs. Each used import path is matched to its declared module by LONGEST
/// import-path prefix (so `…/v84/customer` maps to the `…/v84` module root), and
/// `go get module@<spec>` is issued even when the inherited runtime go.mod
/// already `require`s a DIFFERENT version — the sky.toml pin must win. This runs
/// after `write_out`, so the sky.toml-sourced (re)pin lands last.
///
/// An UNdeclared transitive module (used but not in sky.toml) keeps `@latest`
/// with a warning; if it is already required in the inherited go.mod it is left
/// as-is.
fn inject_ffi_deps(
    reg: &ffi::FfiRegistry,
    used: &std::collections::BTreeSet<String>,
    out_dir: &Path,
    example_dir: &Path,
    warnings: &mut Vec<String>,
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

    // Declared Go deps from sky.toml — the authoritative version source. Reject a
    // dep whose spec is a non-Go range constraint rather than silently @latest it.
    let declared: Vec<(String, String)> =
        crate::ffi_ops::read_go_dependencies(&example_dir.join("sky.toml"))
            .into_iter()
            .filter(|(p, _)| is_external_module(p))
            .filter(|(p, spec)| match crate::ffi_ops::validate_spec(p, spec) {
                Ok(()) => true,
                Err(e) => {
                    warnings.push(e);
                    false
                }
            })
            .collect();

    let go_mod = out_dir.join("go.mod");
    let existing = std::fs::read_to_string(&go_mod).unwrap_or_default();

    for path in &paths {
        // Longest declared import-path prefix wins (module root over subpackage).
        let matched = declared
            .iter()
            .filter(|(module, _)| path == module || path.starts_with(&format!("{module}/")))
            .max_by_key(|(module, _)| module.len());

        match matched {
            Some((module, spec)) => {
                let target = format!("{module}@{}", crate::ffi_ops::spec_or_latest(spec));
                // `go get module@<spec>` edits go.mod (up- OR down-grade), writes
                // go.sum, and downloads — overriding any inherited require.
                let _ = Command::new("go")
                    .args(["get", &target])
                    .current_dir(out_dir)
                    .env("GOFLAGS", "-mod=mod")
                    .output();
            }
            None => {
                // Undeclared transitive: leave an existing require untouched;
                // otherwise pull @latest and warn (naming the module).
                if module_required(&existing, path) {
                    continue;
                }
                warnings.push(format!(
                    "ffi go.mod: {path} used but not declared in sky.toml [\"go.dependencies\"]; resolving @latest"
                ));
                let _ = Command::new("go")
                    .args(["get", path])
                    .current_dir(out_dir)
                    .env("GOFLAGS", "-mod=mod")
                    .output();
            }
        }
    }
    Ok(())
}

/// A Go import path names an external module (needs a `require`) when its first
/// segment carries a dot (`github.com/…`, `gopkg.in/…`). Stdlib paths (`io`,
/// `net/http`, `os`) never do.
fn is_external_module(path: &str) -> bool {
    path.split('/')
        .next()
        .is_some_and(|head| head.contains('.'))
}

/// Whether `go.mod` text already pins `path` in a require directive.
fn module_required(go_mod: &str, path: &str) -> bool {
    go_mod
        .lines()
        .any(|l| l.split_whitespace().next() == Some(path) || l.trim() == path)
}

fn write_out(
    repo_root: &Path,
    out_dir: &Path,
    source: &str,
    console_needed: bool,
) -> std::io::Result<()> {
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
    materialise_rt(&rt_src.join("rt"), &rt_dst, console_needed)?;
    Ok(())
}

/// Copy `rt/` wholesale, skipping `*_test.go` and testdata (doc 09 §A.1). Copies
/// recursively (the runtime has a `console_app/` subtree).
///
/// `console_needed` — when the emitted `main.go` blank-imports
/// `sky-app/rt/console_app` (Sky.Live / Sky.Http.Server app), the
/// `rt/console_app` subpackage MUST be materialised or `go build` fails to
/// resolve the import. For CLI / Tui / Webview binaries (no blank import) it is
/// skipped so the console stack stays out of the build (leanness — mirrors the
/// oracle, whose linker tree-shakes it away when unimported). `testdata` is
/// always skipped.
fn materialise_rt(src: &Path, dst: &Path, console_needed: bool) -> std::io::Result<()> {
    std::fs::create_dir_all(dst)?;
    for entry in std::fs::read_dir(src)? {
        let entry = entry?;
        let path = entry.path();
        let name = entry.file_name();
        let name_s = name.to_string_lossy().to_string();
        if path.is_dir() {
            if name_s == "testdata" {
                continue;
            }
            // `console_app` (package console_app) is only linked when the program
            // blank-imports it — materialise it exactly then, skip it otherwise.
            if name_s == "console_app" && !console_needed {
                continue;
            }
            materialise_rt(&path, &dst.join(&name_s), console_needed)?;
        } else if name_s.ends_with("_test.go") {
            continue;
        } else if name_s.ends_with(".go") {
            std::fs::copy(&path, dst.join(&name_s))?;
        }
    }
    Ok(())
}

// ---- module loading (mirrors xtask/infer_gate) ---------------------------

/// Load every Sky-source module from fetched package dependencies under
/// `.skydeps/<pkg>/src/`. A package ships its own demo `module Main`; those are
/// dropped here so they can never be mistaken for — nor shadow — the consuming
/// example's entry point. Enumeration is sorted (via `load_dir`) so the result
/// is deterministic. Absent `.skydeps/` → empty (the common no-deps case).
fn load_skydeps(
    sdb: &skydb::SkyDatabase,
    next_id: &mut u32,
    skydeps: &Path,
) -> Vec<(String, skydb::SourceFile)> {
    let mut out = Vec::new();
    for path in enumerate_skydep_files(skydeps) {
        let Ok(src) = std::fs::read_to_string(&path) else {
            continue;
        };
        let (n, file) = parse_via_salsa(sdb, next_id, src, &path);
        if n == "Main" || n == "main" {
            continue;
        }
        out.push((n, file));
    }
    out
}

/// Enumerate every `.sky` source file under `<skydeps>/<pkg>/src/`, sorted, over
/// all package directories (sorted). The single source of truth for which files
/// a project's fetched Sky dependencies contribute — shared by the build's
/// [`load_skydeps`] and the LSP's external-dep loader so the two never drift.
/// Absent `.skydeps/` → empty (the common no-deps case). `collect_sky` (via
/// `load_dir`) deliberately prunes any path under `.skydeps/`, so this walks the
/// dep `src/` trees directly with the unfiltered collector.
pub fn enumerate_skydep_files(skydeps: &Path) -> Vec<PathBuf> {
    let mut pkgs: Vec<PathBuf> = match std::fs::read_dir(skydeps) {
        Ok(rd) => rd
            .filter_map(|e| e.ok().map(|e| e.path()))
            .filter(|p| p.is_dir())
            .collect(),
        Err(_) => return Vec::new(),
    };
    pkgs.sort();
    let mut out = Vec::new();
    for pkg in pkgs {
        collect_sky_unfiltered(&pkg.join("src"), &mut out);
    }
    out
}

/// Route a module's source through the Stage-A salsa input + Stage-B `parse`
/// query (doc 01, doc 12): intern the text as a [`skydb::SourceFile`] and read
/// its name off the memoised `parse` leaf query. `next_id` mints a distinct
/// `file_id` per module (the module's load-order ordinal). Returns the module's
/// declared name (from its header, else file stem) + the `SourceFile` input
/// handle — the whole module set now lives in the salsa db (the resolve-stage
/// port), so `resolve`/`module_exports` key off these handles rather than cloned
/// `Parse`s. No `FileId`/span reaches emitted Go, so build + repro are unchanged.
/// A CLI source provider: the `FileId → text` map for the caret excerpt PLUS a
/// `FileId → display path` map so the header shows `src/Main.sky:line:col`
/// (matching the oracle) rather than a bare `line:col`.
struct CliSources<'a> {
    text: &'a std::collections::HashMap<base::FileId, String>,
    paths: &'a std::collections::HashMap<base::FileId, String>,
}

impl diagnostics::SourceProvider for CliSources<'_> {
    fn text(&self, file: base::FileId) -> Option<&str> {
        self.text.get(&file).map(String::as_str)
    }
    fn path(&self, file: base::FileId) -> Option<&str> {
        self.paths.get(&file).map(String::as_str)
    }
}

/// Render a batch of Sky-frontend diagnostics as Elm-style terminal blocks,
/// one per diagnostic, separated by a blank line. `sources` supplies each span's
/// source line for the caret excerpt + the module's display path for the header
/// (`Diagnostic::render_cli`). The joined string becomes the `BuildReport.note`
/// printed by `sky`.
fn render_diags(
    diags: &[diagnostics::Diagnostic],
    sources: &dyn diagnostics::SourceProvider,
) -> String {
    diags
        .iter()
        .map(|d| d.render_cli(sources))
        .collect::<Vec<_>>()
        .join("\n")
}

fn parse_via_salsa(
    db: &skydb::SkyDatabase,
    next_id: &mut u32,
    src: String,
    path: &Path,
) -> (String, skydb::SourceFile) {
    let file = db.new_source(*next_id, src);
    *next_id += 1;
    let name = module_name(skydb::parse(db, file), path);
    (name, file)
}

/// Recursively collect `*.sky` files, sorted, with no generated-dir pruning.
/// Used for `.skydeps/` trees which `collect_sky` deliberately excludes.
fn collect_sky_unfiltered(dir: &Path, out: &mut Vec<PathBuf>) {
    let mut entries: Vec<PathBuf> = match std::fs::read_dir(dir) {
        Ok(rd) => rd.filter_map(|e| e.ok().map(|e| e.path())).collect(),
        Err(_) => return,
    };
    entries.sort();
    for path in entries {
        if path.is_dir() {
            collect_sky_unfiltered(&path, out);
        } else if path.extension().and_then(|e| e.to_str()) == Some("sky") {
            out.push(path);
        }
    }
}

fn load_dir(
    sdb: &skydb::SkyDatabase,
    next_id: &mut u32,
    dir: &Path,
) -> Vec<(String, skydb::SourceFile, PathBuf)> {
    let mut files = Vec::new();
    collect_sky(dir, &mut files);
    let mut out = Vec::new();
    for path in files {
        let Ok(src) = std::fs::read_to_string(&path) else {
            continue;
        };
        let (name, file) = parse_via_salsa(sdb, next_id, src, &path);
        out.push((name, file, path));
    }
    out
}

fn module_name(parse: &syntax::Parse, path: &Path) -> String {
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

#[cfg(test)]
mod sky_toml_tests {
    use super::read_sky_toml_config;

    #[test]
    fn live_and_auth_keys_map_to_runtime_default_suffixes() {
        let dir = std::env::temp_dir().join(format!("skytoml-cfg-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("sky.toml");
        std::fs::write(
            &path,
            "name = \"x\"\n[env]\nprefix = \"FENCE\"\n[live]\nport = 9000\n\
             static = \"public\"\nstore = \"sqlite\"\nstorePath = \"s.db\"\nttl = \"24h\"\n\
             maxBodyBytes = 10485760\n[auth]\ncookieName = \"my_sid\"\ntokenTtl = 3600\n\
             driver = \"jwt\"\n[log]\nformat = \"json\"\nlevel = \"debug\"\n\
             [database]\ndriver = \"sqlite\"\npath = \"app.db\"\n",
        )
        .unwrap();
        let cfg = read_sky_toml_config(&path);

        assert_eq!(cfg.port.as_deref(), Some("9000"));
        let has = |suffix: &str, value: &str| {
            cfg.extra_defaults
                .iter()
                .any(|(s, v)| s == suffix && v == value)
        };
        // [live] keys → the suffixes the runtime reads (previously dropped).
        assert!(has("LIVE_STATIC_DIR", "public"), "{:?}", cfg.extra_defaults);
        assert!(has("LIVE_STORE", "sqlite"));
        assert!(has("LIVE_STORE_PATH", "s.db"));
        assert!(has("LIVE_TTL", "24h"));
        assert!(has("LIVE_MAX_BODY_BYTES", "10485760"));
        // [auth] keys override the fixed AUTH fallbacks.
        assert!(has("AUTH_COOKIE", "my_sid"));
        assert!(has("AUTH_TOKEN_TTL", "3600"));
        assert!(has("AUTH_DRIVER", "jwt"));
        // [log] keys → Std.Log's LOG_FORMAT / LOG_LEVEL.
        assert!(has("LOG_FORMAT", "json"));
        assert!(has("LOG_LEVEL", "debug"));
        // [database] (unchanged).
        assert!(has("DB_DRIVER", "sqlite"));
        assert!(has("DB_PATH", "app.db"));
        // [env] prefix is a dedicated field (emitted as rt.SetEnvPrefix).
        assert_eq!(cfg.env_prefix.as_deref(), Some("FENCE"));

        let _ = std::fs::remove_dir_all(&dir);
    }
}

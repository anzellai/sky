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
    /// `sky build --embed`: the PostgreSQL bundle archive to compile into the
    /// binary, already resolved and verified by the CLI (see
    /// `rust/crates/sky/src/db_embed.rs`). `None` — the ordinary build — links
    /// no bundle at all and actively REMOVES any archive a previous `--embed`
    /// build left in the out dir, so a binary never carries 25MB of PostgreSQL
    /// because of a flag someone passed yesterday.
    ///
    /// The path is resolved by the CLI rather than here because acquiring it can
    /// mean a network fetch and a checksum verification, and `project` is the
    /// library every gate and harness builds through — none of which should be
    /// able to reach the network as a side effect of compiling.
    pub embed_bundle: Option<PathBuf>,
    /// `sky build --wasm`: compile the emitted Go for `GOOS=js GOARCH=wasm`
    /// (a Sky.Spa client) instead of a native binary, and drop the matching
    /// `wasm_exec.js` beside it. The output is `<out>/main.wasm` +
    /// `<out>/wasm_exec.js` rather than the native `<out>/<bin>`. The native
    /// cgo-detection path is skipped: a wasm client links no system webview and
    /// must not flip to cgo.
    pub wasm: bool,
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
    /// The legacy-`sky.toml` → `withX` migration LIST for this project, when its
    /// `sky.toml` still carries a runtime key that has moved into typed app
    /// config. `None` for a fully-migrated (or never-legacy) project — the
    /// self-extinguishing property (design §8.2). The CLI prints it on the same
    /// stderr channel as `warning:`, on both `sky build` and `sky run`.
    pub migration_hint: Option<String>,
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
    /// The legacy-`sky.toml` → `withX` migration LIST (design §8.2), or `None`
    /// when nothing present has moved. Derived from the project's `sky.toml`
    /// alone — deterministic, no environment read — so `emit_example_source`
    /// stays reproducible and this never reaches emitted bytes.
    migration_hint: Option<String>,
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

    let source_root = configured_source_root(&example_dir);
    let mut locals = load_dir(&db, &mut next_id, &example_dir.join(&source_root));
    for dir in extra_dirs {
        locals.extend(load_dir(&db, &mut next_id, dir));
    }
    if locals.is_empty() {
        return Err(format!("no .sky under {source_root}/"));
    }
    // `FileId → display path` for every APP module — feeds the Elm-style renderer
    // so each diagnostic header carries `src/Main.sky:line:col` (matching the
    // oracle) instead of a bare `line:col`. MUST be keyed by the module's
    // `ModuleId` — the id `db.add_module` returns and the id a diagnostic span's
    // `file` carries — NOT by the `SourceFile`'s `file_id` (a load-order ordinal
    // minted by `next_id`). The two coincide for a project with no Sky
    // dependencies, but a `.skydeps` module that shares a name with a local one
    // (`add_module` returns the EXISTING id on re-add) — or kernel pre-population
    // — shifts them apart, and a span then resolves to the WRONG file's path
    // (e.g. `View/Common.sky` errors reported under `View/AppDetail.sky`). `path`
    // is captured in the per-module loop below where the real `ModuleId` is known;
    // this map is filled there, alongside `src_map`, so both key off the same id.
    let mut path_map: std::collections::HashMap<base::FileId, String> =
        std::collections::HashMap::new();
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
    for (n, file, p) in locals {
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
        // Key the display-path map by the REAL `ModuleId` (`id.index()`) — the
        // same id `src_map` and diagnostic spans use — so a Sky-frontend error
        // always names the file the span actually points at (see the note where
        // `path_map` is declared).
        let disp = p
            .strip_prefix(example_dir)
            .unwrap_or(&p)
            .to_string_lossy()
            .replace('\\', "/");
        path_map.insert(base::FileId(id.index()), disp);
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
    // Ambiguity (`[E1012]`) is reported BEFORE the type gate, because it is the
    // CAUSE and any type error under it is the consequence. When a bare name is
    // bound by two imports, the resolver still has to hand lowering one of them
    // so resolution stays total — and whichever it picks, the use site may then
    // fail to unify. Reporting that clash would tell the user their types are
    // wrong when the real defect is that the compiler could not tell which of two
    // `length`s they meant. Measured, not reasoned: `import Sky.Core.String
    // exposing (..)` + `import Sky.Core.List exposing (..)` with a bare `length`
    // printed `[E2001] type mismatch: List _ vs String` and never mentioned the
    // ambiguity. The other `[E1xxx]` name errors keep their existing position
    // after the type gate — only the cause/consequence inversion is fixed here.
    let ambiguous: Vec<diagnostics::Diagnostic> = checked
        .diagnostics
        .iter()
        .filter(|d| d.severity == diagnostics::Severity::Error && d.code.0 == "E1012")
        .cloned()
        .collect();
    if !ambiguous.is_empty() {
        return Err(render_diags(&ambiguous, &sources));
    }
    if checked.type_errors > 0 {
        // Select by the type-error BAND (`E2…`), not by an enumerated allowlist.
        // The allowlist that used to sit here (`E2001 || E2007`) was a
        // silent-failure generator: `type_errors > 0` decides that the build
        // FAILS, while this filter decides what the user is TOLD. The first new
        // code in the band — `[E2008]`, the unsupported-`Dict`-key check — made
        // `sky check` exit 1 with a completely EMPTY message. Measured, not
        // reasoned: that is exactly what it did before this line changed. Every
        // producer of `CheckOutput::type_errors` codes in the `E2…` band
        // (`ty::check`), so band membership is the honest predicate, and a
        // future `[E2009]` cannot reintroduce the hole.
        let ds: Vec<diagnostics::Diagnostic> = checked
            .diagnostics
            .iter()
            .filter(|d| d.severity == diagnostics::Severity::Error && d.code.0.starts_with("E2"))
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
    // Captured before `cfg` is moved into the lowering config — used after the
    // emit to report a `[database] driver` that contradicts its DSN.
    let db_driver_diag = db_driver_conflict(cfg.db_driver.as_deref(), cfg.db_dsn.as_deref());
    // Captured for the same reason as `db_driver_diag`: `cfg` is moved into the
    // lowering config below, and these are reported after the emit.
    let unknown_keys = cfg.unknown_config_keys.clone();
    // The legacy→`withX` migration LIST for this project (design §8.2): computed
    // from the runtime keys the parser actually saw, mapped through the ONE
    // migration table. Captured before `cfg` is moved. Self-extinguishing —
    // `None` once no migratable key remains, so a clean project prints nothing.
    let migration_hint = crate::config_migration::migration_hint(&cfg.present_runtime_config_keys);
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
    // The subset of kernel symbols whose Go func is VARIADIC — the one case the
    // param scan above mis-counts. A kernel ALIAS backed by one of these takes
    // its currying arity from the declared Sky signature instead (see
    // `LowerConfig.variadic_kernels`); every non-variadic alias keeps the scan.
    cfg.variadic_kernels = crate::abi_guard::runtime_variadic_kernels(repo_root).clone();
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
    // A `[database] driver` that contradicts the DSN it sits beside is reported
    // here rather than silently ignored — the key was decorative before this
    // (nothing read the `DB_DRIVER` it used to emit), so `driver = "postgres"`
    // next to `./app.db` opened SQLite without a word.
    let mut warnings = prog.warnings.clone();
    if let Some(w) = db_driver_diag {
        warnings.push(w);
    }
    // Same principle, applied to every runtime config section: a key that is
    // honoured by nothing is reported, not dropped.
    warnings.extend(unknown_config_keys(&unknown_keys));
    Ok(Emitted {
        source,
        registry,
        ffi_used: prog.ffi_used.clone(),
        warnings,
        migration_hint,
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

/// The lowering WARNINGS for an example, without writing anything or running
/// `go build` — the same list [`build_example`] surfaces on `BuildReport`.
/// Lets a regression test pin a lint's exact firing surface (both that it does
/// fire on the shape it exists for, and that it does NOT on a shape it used to
/// cry wolf on) in seconds, without a Go toolchain.
pub fn emit_example_warnings(repo_root: &Path, example_dir: &Path) -> Result<Vec<String>, String> {
    assemble_and_emit(repo_root, example_dir).map(|e| e.warnings)
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
            report.migration_hint = e.migration_hint;
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

    // Embed committed migrations (db/migrations/*.json) into the binary so a
    // deployed `SKY_DB_OP=migrate ./app` self-migrates with no source tree. Emits
    // a tiny generated `embedded_migrations.go` beside main.go; best-effort (a
    // failure warns, never fails the build).
    if let Err(e) = write_embedded_migrations(&opts.example_dir, &out_dir) {
        report.warnings.push(format!("embed migrations: {e}"));
    }

    // `sky build --embed`: stage the PostgreSQL bundle and the `go:embed` that
    // links it. NOT best-effort — a `--embed` build that quietly produced a
    // binary with no database in it is the one outcome this flag must never
    // have, so a failure here fails the build before `go build` runs.
    if let Err(e) = write_postgres_bundle(opts.embed_bundle.as_deref(), &out_dir) {
        // `emitted` is cleared as well as noted: the Go is on disk, but the
        // binary this build would have produced is not the one that was asked
        // for, and the CLI's "emitted" path goes on to report `go build`
        // results. A `--embed` build that cannot stage its bundle has failed.
        report.emitted = false;
        report.note = e;
        return report;
    }

    // ---- go build (two-phase cgo detection + bounded) ----
    // Prefer static binaries (CGO_ENABLED=0) so the common pure-Go app ships
    // without libSystem/CoreFoundation/Security dylib deps; retry with cgo when
    // the static build fails (an FFI package that needs cgo links on the retry).
    // A Sky.Webview program flips STRAIGHT to cgo on the first attempt — its
    // `webview_stub.go` compiles cleanly under CGO=0 and the app would silently
    // no-op at runtime, so the static-first probe must be skipped there. Matches
    // the oracle (`app/Main.hs`) + CLAUDE.md §"Sky.Webview" cgo-detect note.
    // Output binary name honours the sky.toml `bin` key (default `app`).
    let bin_name = configured_bin_name(&opts.example_dir);
    // `--wasm`: compile the client for the browser (GOOS=js GOARCH=wasm) and
    // drop the matching wasm_exec.js. The native cgo-detection path is skipped —
    // a Sky.Spa client imports `syscall/js` and must NOT native-build.
    if opts.wasm {
        match run_wasm_build(&out_dir) {
            Ok(()) => report.go_build_ok = true,
            Err(e) => report.go_build_stderr = e,
        }
        return report;
    }
    match run_go_build_detecting_cgo(&out_dir, &source, &bin_name) {
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
        let mut cmd = Command::new(format!("./{bin_name}"));
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

/// `sky build --wasm`: compile `out_dir` for `GOOS=js GOARCH=wasm` into
/// `out_dir/main.wasm`, then copy the toolchain's `wasm_exec.js` beside it — the
/// browser loader the emitted client is paired with (a loader from a different
/// toolchain, e.g. TinyGo, fails with a WebAssembly LinkError). Standard-Go wasm
/// has full reflect, so no de-reflection is needed; the bundle is larger but runs
/// in any browser / WKWebView / Android WebView.
fn run_wasm_build(out_dir: &Path) -> Result<(), String> {
    let out = Command::new("go")
        .current_dir(out_dir)
        .env("GOOS", "js")
        .env("GOARCH", "wasm")
        .args(["build", "-o", "main.wasm", "."])
        .output()
        .map_err(|e| format!("failed to run `go build` (GOOS=js GOARCH=wasm): {e}"))?;
    if !out.status.success() {
        return Err(String::from_utf8_lossy(&out.stderr).into_owned());
    }
    let goroot = Command::new("go")
        .args(["env", "GOROOT"])
        .output()
        .map_err(|e| format!("`go env GOROOT`: {e}"))?;
    let goroot = String::from_utf8_lossy(&goroot.stdout).trim().to_string();
    // Go 1.21+ ships wasm_exec.js under lib/wasm; older toolchains, misc/wasm.
    let candidates = [
        Path::new(&goroot).join("lib").join("wasm").join("wasm_exec.js"),
        Path::new(&goroot).join("misc").join("wasm").join("wasm_exec.js"),
    ];
    let src = candidates.iter().find(|p| p.exists()).ok_or_else(|| {
        format!("wasm_exec.js not found under GOROOT ({goroot}); looked in lib/wasm and misc/wasm")
    })?;
    let dest = out_dir.join("wasm_exec.js");
    // The GOROOT copy is read-only (0444); a prior build left a read-only dest,
    // so overwriting it would EPERM. Remove it first (best-effort).
    let _ = std::fs::remove_file(&dest);
    std::fs::copy(src, &dest).map_err(|e| format!("copy wasm_exec.js -> {}: {e}", dest.display()))?;
    Ok(())
}

/// Run `go build` with static-first cgo detection. `source` is the emitted
/// `main.go` text; its containing `rt.Webview_app` reference is the signal that
/// the project links the system webview and MUST build with cgo.
fn run_go_build_detecting_cgo(
    out_dir: &Path,
    source: &str,
    bin_name: &str,
) -> Result<GoBuildOutcome, String> {
    // Sky.Webview: the stub (`webview_stub.go`, `!cgo || !darwin`) compiles fine
    // under CGO=0, producing a binary that silently no-ops on `Webview.app`.
    // Force cgo up front so the real WKWebView-backed `webview.go` links.
    if source.contains("rt.Webview_app") || source.contains("rt.Webview_url") {
        let attempt = run_go_build_once(out_dir, "1", bin_name)?;
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
    let static_attempt = run_go_build_once(out_dir, "0", bin_name)?;
    if static_attempt.status_ok {
        return Ok(GoBuildOutcome {
            ok: true,
            stderr: static_attempt.stderr,
            cgo_note: None,
        });
    }

    // The static build failed — an FFI package may require cgo. Retry with it.
    let cgo_attempt = run_go_build_once(out_dir, "1", bin_name)?;
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

/// `GOFLAGS` for `go build`. Preserves whatever the user set (so a container's
/// `-buildvcs=false` survives — the old hard-coded `GOFLAGS=-mod=mod` OVERWROTE
/// the env, so Go then tried to VCS-stamp a Go-workspace synthetic parent repo
/// and failed), then forces the two Sky always needs: `-mod=mod` (Sky manages
/// the emitted `go.mod`) and `-buildvcs=false` (the emitted Go is generated —
/// there is never a source revision to stamp, and stamping breaks inside odd
/// git/workspace boundaries). Sky's values replace any conflicting user `-mod=`
/// / `-buildvcs=`; every other user flag is kept.
fn sky_build_goflags() -> String {
    sky_build_goflags_from(&std::env::var("GOFLAGS").unwrap_or_default())
}

fn sky_build_goflags_from(existing: &str) -> String {
    let mut flags: Vec<String> = existing
        .split_whitespace()
        .filter(|f| !f.starts_with("-mod=") && !f.starts_with("-buildvcs="))
        .map(String::from)
        .collect();
    flags.push("-mod=mod".to_string());
    flags.push("-buildvcs=false".to_string());
    flags.join(" ")
}

fn run_go_build_once(out_dir: &Path, cgo: &str, bin_name: &str) -> Result<GoBuildAttempt, String> {
    let mut cmd = Command::new("go");
    cmd.arg("build")
        .arg("-o")
        .arg(bin_name)
        .arg(".")
        .current_dir(out_dir)
        .env("GOFLAGS", sky_build_goflags())
        .env("CGO_ENABLED", cgo);
    // Unprivileged environments (unwritable $HOME) can't use Go's default
    // build/module caches — route them to the writable Sky cache (#7). No-op on
    // a normal setup.
    for (k, v) in ffi::inspect::go_env_for_constrained_home() {
        cmd.env(k, v);
    }
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
/// Extract a scalar `sky.toml` value: drop an inline `# comment` after the value
/// and strip a surrounding pair of double quotes. A `#` INSIDE a quoted string is
/// preserved (`"a#b"` → `a#b`). Without this, a `store = "postgres"   # note`
/// line seeded `LIVE_STORE=postgres"   # note` — which never matches
/// `case "postgres"` in the runtime and silently fell back (e.g. sessions to the
/// in-memory store on a raw-binary deploy).
fn parse_toml_scalar(raw: &str) -> String {
    let s = raw.trim();
    if let Some(rest) = s.strip_prefix('"') {
        if let Some(end) = rest.find('"') {
            return rest[..end].to_string();
        }
    }
    // Unquoted: an inline comment ends the value; then trim + strip stray quotes.
    let body = match s.find('#') {
        Some(i) => &s[..i],
        None => s,
    };
    body.trim().trim_matches('"').to_string()
}

/// The driver the RUNTIME will actually use for a connection string — a mirror
/// of `rt.detectDriver` (`runtime-go/rt/db_auth.go`), which is the only thing
/// that decides this. Kept in lockstep with it: a `postgres://` / `postgresql://`
/// URL or a libpq keyword DSN is Postgres, everything else is SQLite.
pub fn driver_for_dsn(dsn: &str) -> &'static str {
    let low = dsn.trim().to_ascii_lowercase();
    if low.starts_with("postgres://")
        || low.starts_with("postgresql://")
        || (low.contains("host=") && low.contains("user="))
    {
        "pgx"
    } else {
        "sqlite"
    }
}

/// True when a declared `[database] driver` names the same engine the DSN will
/// actually open. `pgx` is the runtime's internal name for Postgres; users write
/// `postgres` / `postgresql`.
fn driver_names_match(declared: &str, actual: &str) -> bool {
    let d = declared.trim().to_ascii_lowercase();
    match actual {
        "pgx" => matches!(d.as_str(), "pgx" | "postgres" | "postgresql"),
        other => d == other,
    }
}

/// The diagnostic for a `[database] driver` that contradicts the DSN it sits
/// beside — `driver = "postgres"` next to `path = "./app.db"` opens SQLite.
///
/// This is what makes the key load-bearing instead of decorative. It reports
/// rather than overrides: the DSN remains the single source of truth (it is what
/// `rt.detectDriver` and every downstream dialect branch use), so a build is
/// never silently rerouted to a different engine by a config key.
///
/// Returns `None` when there is no declared driver, no declared DSN (the DSN may
/// legitimately arrive at runtime via `SKY_DB_PATH` / `DATABASE_URL`), or the two
/// agree.
pub fn db_driver_conflict(declared: Option<&str>, dsn: Option<&str>) -> Option<String> {
    let declared = declared?;
    let dsn = dsn?;
    let actual = driver_for_dsn(dsn);
    if driver_names_match(declared, actual) {
        return None;
    }
    let shown = if actual == "pgx" { "postgres" } else { actual };
    Some(format!(
        "[database] driver = \"{declared}\" contradicts path/url \"{dsn}\", which \
         opens {shown}. The driver is derived from the connection string's shape, \
         not from this key — either give a {shown} connection string or correct \
         the driver. (Set the DSN via SKY_DB_PATH / DATABASE_URL to choose at run \
         time.)"
    ))
}

/// The legacy-`sky.toml` → `withX` migration LIST for a project directory, or
/// `None` when its `sky.toml` carries no migratable runtime key (design §8.2).
///
/// This is the SAME derivation the build path uses — it parses `sky.toml`
/// through [`read_sky_toml_config`] (so section tracking, key recognition and
/// the `store_path` alias all match exactly what a build honours) and maps the
/// recognised keys through the ONE table in [`crate::config_migration`]. Exposed
/// so a fixture gate can pin the LIST without a Go toolchain, and so a future
/// `sky config` verb reuses one derivation rather than a second parser (§1.3).
pub fn migration_hint_for(project_dir: &Path) -> Option<String> {
    let cfg = read_sky_toml_config(&project_dir.join("sky.toml"));
    crate::config_migration::migration_hint(&cfg.present_runtime_config_keys)
}

pub(crate) fn read_sky_toml_config(path: &Path) -> lower::LowerConfig {
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
        // Section header — tolerate a trailing inline comment after `]`.
        if line.starts_with('[') {
            if let Some(end) = line.find(']') {
                section = line[1..end].trim().trim_matches('"').to_string();
                continue;
            }
        }
        let Some((k, v)) = line.split_once('=') else {
            continue;
        };
        let key = k.trim();
        let val = parse_toml_scalar(v);
        // Record every key the migration LIST speaks to, for the legacy→`withX`
        // hint. Two sources, unioned: a key the runtime still HONOURS
        // (`accepted_config_keys` — the same set the match below arms on, so a
        // Moved/DefaultChanged key stays in lockstep with what is parsed), OR a
        // key the migration table names — which is how a REMOVED key survives
        // here after it is dropped from `accepted_config_keys`. `[auth]` is the
        // live case: it is no longer a runtime key (its parse arms and prologue
        // seeds are gone, so a residual `[auth]` key ALSO gets the standard
        // inert-key warning), yet its Removed migration rows must still fire the
        // "delete it, it does nothing" block. The table (`config_migration`)
        // classifies each into moved / changed / removed; a key in neither the
        // accepted set nor the table (pool knobs, `embedded`, `[env] prefix`)
        // produces no hint, so a legitimately sky.toml-only key never nags.
        if accepted_config_keys(&section).contains(&key)
            || crate::config_migration::lookup(&section, key).is_some()
        {
            cfg.present_runtime_config_keys
                .push((section.clone(), key.to_string(), val.clone()));
        }
        match (section.as_str(), key) {
            ("", "port") => cfg.port = Some(val),
            // `driver` is RECORDED, never emitted. Nothing in runtime-go reads
            // DB_DRIVER / SKY_DB_DRIVER; the driver comes from the DSN's shape.
            // Kept as a declared expectation and checked against the DSN below.
            ("database", "driver") => cfg.db_driver = Some(val),
            // `path` and `url` are aliases — both seed DB_PATH, which
            // `Db.connect ()` reads and `detectDriver` routes to sqlite or
            // postgres by DSN shape (`postgres://…` → pgx). `url` matches the
            // CLAUDE.md app-matrix wording; a bare `postgres://` DSN in either
            // key just works.
            ("database", "path" | "url") => {
                cfg.db_dsn = Some(val.clone());
                cfg.extra_defaults.push(("DB_PATH".into(), val))
            }
            // Connection-pool sizing + transaction isolation → the
            // suffixes db_pool.go reads. All four pool knobs are
            // PostgreSQL-only (SQLite is pinned to one connection by its
            // global writer lock and warns if these are set), and the
            // runtime's own deployment-aware defaults apply when they
            // are absent — these exist for the operator who knows their
            // server's max_connections budget.
            ("database", "maxOpenConns") => {
                cfg.extra_defaults.push(("DB_MAX_OPEN_CONNS".into(), val))
            }
            ("database", "maxIdleConns") => {
                cfg.extra_defaults.push(("DB_MAX_IDLE_CONNS".into(), val))
            }
            ("database", "connMaxLifetime") => {
                cfg.extra_defaults.push(("DB_CONN_MAX_LIFETIME".into(), val))
            }
            ("database", "connMaxIdleTime") => {
                cfg.extra_defaults.push(("DB_CONN_MAX_IDLE_TIME".into(), val))
            }
            // `isolation` raises the level Std.Db.transaction begins at.
            // Unset = the driver default (READ COMMITTED on PostgreSQL),
            // which is what shipped before this key existed and is not
            // changed by adding it. `txRetry` is the retry budget for a
            // 40001/40P01 conflict and is only safe when the transaction
            // body is REPLAYABLE — see resolveDbTxConfig in db_pool.go.
            ("database", "isolation") => {
                cfg.extra_defaults.push(("DB_ISOLATION".into(), val))
            }
            ("database", "txRetry") => cfg.extra_defaults.push(("DB_TX_RETRY".into(), val)),
            // `embedded` opts the project into the `sky db start` cluster
            // supervisor (docs/skydb/embedded-postgres.md). It is a TOOLCHAIN
            // key, not a runtime one: `sky run` reads it, starts the cluster and
            // injects the DSN as `<PREFIX>_DB_PATH` into the app's environment.
            // The binary itself must never learn which tier provisioned its DSN,
            // so nothing is emitted for it here.
            //
            // It is matched rather than left to the fall-through arm because that
            // arm reports every unmatched key in a recognised section as
            // honoured-by-nothing — which is exactly what `embedded` would look
            // like, while being the one key that opts the project in.
            ("database", "embedded") => {}
            // `postgresVersion` is the pin `sky db provision --embed` records, so
            // a project states which PostgreSQL it is developed against and gets
            // that one back on another machine. Toolchain-only for the same
            // reason as `embedded`: the app binary must never learn which tier
            // provisioned its DSN, let alone which build of the server it is.
            ("database", "postgresVersion") => {}
            // `[analytics] dbPath` → the Std.Analytics store override
            // (SKY_ANALYTICS_DB_PATH). Unset → analytics reuses the console DB
            // (SKY_CONSOLE_DB_PATH). See analytics_store.go.
            ("analytics", "dbPath" | "dbpath") => {
                cfg.extra_defaults.push(("ANALYTICS_DB_PATH".into(), val))
            }
            // `[analytics] retention` (e.g. "90d" / "720h") → prune events older
            // than the window so the store stays bounded. Unset → keep all.
            ("analytics", "retention") => {
                cfg.extra_defaults.push(("ANALYTICS_RETENTION".into(), val))
            }
            ("live", "port") => cfg.port = Some(val),
            // `[live]` runtime keys → the suffixes the runtime reads (live.go /
            // live_store.go). Without these, only the `SKY_LIVE_*` env vars were
            // honoured and the sky.toml keys were silently ignored.
            ("live", "static") => cfg.extra_defaults.push(("LIVE_STATIC_DIR".into(), val)),
            ("live", "store") => cfg.extra_defaults.push(("LIVE_STORE".into(), val)),
            ("live", "storePath") => cfg.extra_defaults.push(("LIVE_STORE_PATH".into(), val)),
            ("live", "ttl") => cfg.extra_defaults.push(("LIVE_TTL".into(), val)),
            // `input` = when the JS driver reports an input's value: "debounce"
            // (default) or "blur". The runtime hardcoded "debounce" behind a
            // `// or "blur"` comment while two examples carried this key, so
            // the setting existed on both sides and connected in neither.
            ("live", "input") => cfg.extra_defaults.push(("LIVE_INPUT_MODE".into(), val)),
            ("live", "maxBodyBytes") => {
                cfg.extra_defaults.push(("LIVE_MAX_BODY_BYTES".into(), val))
            }
            // `[jobs]` runtime keys → the suffixes jobs_kernel.go reads
            // (`skyGetenv("JOBS_STORE")` / `JOBS_STORE_PATH`).
            //
            // These existed as a CONFIG SECTION IN NAME ONLY. Nothing parsed
            // `[jobs]`, so the `_ => {}` arm below swallowed it — while
            // jobs_kernel.go's own error text told the operator to "set sky.toml
            // [jobs] store_path", and the production path made that a HARD
            // startup failure. Following that instruction produced a file the
            // compiler ignored and an app that then refused to start, with the
            // error still pointing at the key that had just been set.
            //
            // `store_path` is accepted alongside `storePath` precisely because
            // the runtime message named the snake_case spelling; both map to the
            // same suffix rather than leaving one of them silently inert.
            ("jobs", "store") => cfg.extra_defaults.push(("JOBS_STORE".into(), val)),
            ("jobs", "storePath" | "store_path") => {
                cfg.extra_defaults.push(("JOBS_STORE_PATH".into(), val))
            }
            // `[auth]` is GONE. The block (driver/cookieName/tokenTtl) was
            // parsed, seeded into every prologue, and read by NOTHING for four
            // minor versions (config-architecture §1.11; config-surface counted
            // it `seeded_without_reader = 3`). Std.Auth is a library that takes
            // its secret + TTL as Sky arguments — there is no framework layer to
            // wire these into. So there is no parse arm: a residual `[auth]` key
            // falls through to the `_` arm and gets the standard inert-key
            // warning, while its Removed migration row (config_migration) prints
            // "delete it — it does nothing". `SKY_AUTH_TOKEN_SECRET`, the one
            // auth-related name that matters, is an env convention `sky doctor`
            // knows about, never a sky.toml key.
            // [log] → the suffixes Std.Log reads (skyGetenv LOG_FORMAT/LOG_LEVEL).
            ("log", "format") => cfg.extra_defaults.push(("LOG_FORMAT".into(), val)),
            ("log", "level") => cfg.extra_defaults.push(("LOG_LEVEL".into(), val)),
            // [env] prefix re-namespaces every runtime SKY_* read; the compiler
            // must emit `rt.SetEnvPrefix(...)` for it to take effect (the Rust
            // compiler previously emitted nothing, so it was silently ignored).
            ("env", "prefix") => cfg.env_prefix = Some(val),
            // [security] csrf = false → SKY_CSRF, the switch rt already honours
            // (runtime-go/rt/csrf_middleware.go). The runtime half of this has
            // been complete since CSRF shipped — SetCsrfEnabled, IsCsrfEnabled
            // and the SKY_CSRF read all exist and are tested — but the compiler
            // half was never written, so three separate runtime comments
            // described `[security] csrf = false` as the way to turn CSRF off
            // while the key did nothing at all.
            //
            // Deliberately NOT wired: `[security] env`. Which environment a
            // binary is RUNNING in is not a property of how it was BUILT — one
            // artefact gets promoted dev → staging → prod, and a compile-time
            // answer cannot be right for all three. It stays an env var
            // (`ENV`, or `<PREFIX>_ENV`), which is what productionFromEnv
            // reads and what docs/skylive/overview.md documents. The key warns,
            // with a hint naming the variable that works.
            ("security", "csrf") => cfg.extra_defaults.push(("CSRF".into(), val)),
            // Everything else falls through and is RECORDED, not dropped — see
            // `unknown_config_keys`. The only silence is for sections consumed
            // by other tooling, where an unrecognised key is not evidence of a
            // mistake.
            _ => {
                if !is_externally_consumed_section(&section) {
                    cfg.unknown_config_keys.push((section.clone(), key.to_string()));
                }
            }
        }
    }
    cfg
}

/// The sky.toml sections consumed by something OTHER than the runtime-config
/// parser — the project metadata Sky reads elsewhere, and the dependency tables
/// handed to cargo/go tooling. An unrecognised key in one of these is not
/// evidence of a mistake, so it stays silent.
///
/// # Why this is an exclusion list, and not the inclusion list it used to be
///
/// This function was `is_runtime_config_section`, naming the seven sections
/// whose keys were checked. Everything else — every section NOT on the list —
/// was dropped without a word, which meant the warning was structurally unable
/// to report the single most likely config mistake: **a wrong section name**.
///
/// `[security] env` and `[security] csrf` were the live instance. `[security]`
/// was on no list, so both keys vanished silently while
/// `runtime-go/rt/observability.go:228` served a 401 telling the locked-out
/// operator to "set [security] env" — instructing them to do a thing that could
/// not work. A typo'd `[databse] path` and the equally-unparsed
/// `[observability] enabled` (claimed in a comment at observability.go:255)
/// failed exactly the same way.
///
/// Inverting the list closes the class: a key is now reported unless its
/// section is known to be somebody else's. New runtime sections are covered the
/// day they are added rather than the day someone remembers to list them.
fn is_externally_consumed_section(section: &str) -> bool {
    matches!(
        section,
        // Bare top-level keys (`port`, `bin`, `root`) — handled by the arms
        // above and by sky_toml_flag / configured_bin_name.
        ""
        | "project"
        | "source"
        | "dependencies"
        | "go.dependencies"
        | "lib"
    )
}

/// The keys each runtime config section actually honours — the same set the
/// `match` above arms on, kept adjacent so the two cannot drift apart silently.
fn accepted_config_keys(section: &str) -> &'static [&'static str] {
    match section {
        "live" => &[
            "port",
            "static",
            "store",
            "storePath",
            "ttl",
            "maxBodyBytes",
            "input",
        ],
        "database" => &[
            "driver",
            "path",
            "url",
            "maxOpenConns",
            "maxIdleConns",
            "connMaxLifetime",
            "connMaxIdleTime",
            "isolation",
            "txRetry",
            "embedded",
            "postgresVersion",
        ],
        // `auth` is deliberately absent — the inert `[auth]` block was removed
        // (config-architecture §1.11). A residual `[auth]` key is now reported by
        // `unknown_config_keys` (and its Removed migration row) rather than
        // silently seeded.
        "log" => &["format", "level"],
        "analytics" => &["dbPath", "retention"],
        "jobs" => &["store", "storePath", "store_path"],
        "env" => &["prefix"],
        // `env` is deliberately absent — see the parser arm. Deployment
        // environment is not a build-time constant.
        "security" => &["csrf"],
        _ => &[],
    }
}

/// A directed hint for a key that is inert but has a working equivalent, so the
/// warning can say what to do instead of only what not to do.
///
/// `[security] env` is the case that motivated this: the runtime's own 401 hint
/// pointed operators at it, and "this key does nothing" alone would leave them
/// exactly as stuck as before.
fn inert_key_hint(section: &str, key: &str) -> Option<&'static str> {
    match (section, key) {
        ("security", "env") => Some(
            "Set the `ENV` environment variable on the deployment instead \
             (`ENV=production`); Sky also accepts the namespaced `<PREFIX>_ENV`, \
             e.g. `SKY_ENV`. Which environment a binary runs in is a property of \
             the deployment, not of the build, so it is not a sky.toml key.",
        ),
        ("observability", "enabled") => Some(
            "There is no `[observability]` section. The console and metrics \
             endpoints gate on `ENV` (see docs/observability.md).",
        ),
        _ => None,
    }
}

/// A build warning per sky.toml key that sits in a runtime config section and is
/// honoured by nothing.
///
/// # Why this is a warning and not silence
///
/// Until now the parser's final arm was a bare `_ => {}`: any key it did not
/// recognise was dropped without a word. That is not a hypothetical hazard —
/// it shipped in the repository's own examples:
///
/// * `examples/08-notes-app` and `examples/12-skyvote` both set `[auth]`
///   `method`, `secret`, `session_ttl` and `email_verification`. Not one of the
///   four is parsed, and three of them are not keys at all. `session_ttl` is the
///   real setting `tokenTtl` under a spelling that does nothing, so both
///   examples advertise a 24-hour session and get the default.
/// * `[jobs]` was referenced by four comments in `runtime-go/rt/jobs_kernel.go`
///   and parsed by nobody, while the runtime's own error text instructed the
///   operator to "set sky.toml [jobs] store_path" — and, in production, made it
///   a hard startup failure. Doing as instructed changed nothing.
///
/// A silently-ignored key is the worst of both worlds: the config LOOKS set, so
/// nobody looks again, and the behaviour is the default. This mirrors
/// `db_driver_conflict`, which already refuses to let a `[database] driver`
/// contradict its DSN in silence.
///
/// Warning, not error: a project may legitimately carry keys a NEWER Sky honours
/// (downgrade), and failing the build over an inert key would be worse than the
/// key being inert. The message names the accepted keys so the fix is mechanical.
pub fn unknown_config_keys(keys: &[(String, String)]) -> Vec<String> {
    keys.iter()
        .map(|(section, key)| {
            let mut msg = format!(
                "sky.toml: `[{section}] {key}` is not a key Sky reads — it has no effect. "
            );
            let accepted = accepted_config_keys(section);
            if accepted.is_empty() {
                // Unknown SECTION, not merely an unknown key in a known one.
                // Naming the real sections is the useful thing here: the
                // likeliest cause is a typo or an invented section.
                msg.push_str(
                    "`[",
                );
                msg.push_str(section);
                msg.push_str(
                    "]` is not a section Sky reads. Runtime sections are: \
                     `[live]`, `[database]`, `[log]`, `[analytics]`, \
                     `[jobs]`, `[env]`, `[security]`. ",
                );
            } else {
                msg.push_str(&format!(
                    "Accepted keys in `[{section}]`: {}. ",
                    accepted.join(", ")
                ));
            }
            if let Some(hint) = inert_key_hint(section, key) {
                msg.push_str(hint);
                msg.push(' ');
            }
            msg.push_str("(See docs/sky-toml.md; keys are camelCase.)");
            msg
        })
        .collect()
}

/// Read a `[project]`-scoped (or bare top-level, or `[source]`-table) string key
/// from a project's `sky.toml`, returning `default` when absent. The build
/// driver uses this for `bin` (output binary name) and `root` (source-root dir)
/// — both documented in docs/sky-toml.md as `[project]` keys, also accepted at
/// the top level. `root` additionally accepts the `[source]` table form.
///
/// The value is sanitised to a single path segment: a value containing a path
/// separator or `..`, or an empty value, falls back to `default` — so a stray
/// `bin = "../x"` can never redirect `go build -o` outside the out dir.
pub fn sky_toml_project_key(project_dir: &Path, key: &str, default: &str) -> String {
    let Ok(text) = std::fs::read_to_string(project_dir.join("sky.toml")) else {
        return default.to_string();
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
        let scoped = section.is_empty() || section == "project" || section == "source";
        if scoped && k.trim() == key {
            // Take the value up to an inline `#` comment. When the value is a
            // quoted string, the comment starts after the closing quote; when
            // bare, at the first `#`. Without this, `bin = "srv"  # out` parsed
            // as `srv"  # out` and produced a garbage output-binary name.
            let raw_val = v.trim();
            let val = if let Some(rest) = raw_val.strip_prefix('"') {
                rest.split('"').next().unwrap_or("")
            } else {
                raw_val.split('#').next().unwrap_or("").trim()
            };
            if val.is_empty() || val.contains('/') || val.contains('\\') || val.contains("..") {
                return default.to_string();
            }
            return val.to_string();
        }
    }
    default.to_string()
}

/// Read a scalar key out of a named `sky.toml` section — `[database] embedded`,
/// `[env] prefix`, and anything else a *toolchain* verb needs to see.
///
/// [`sky_toml_project_key`] cannot serve this: it only ever looks at the
/// top-level / `[project]` / `[source]` scope, and it sanitises the value to a
/// single path segment (a DSN would come back as the default). The parsing rules
/// here are the ones [`read_sky_toml_config`] already applies to every runtime
/// key — same section tracking, same [`parse_toml_scalar`] — so a value read by a
/// verb and a value seeded into the app's environment cannot disagree about what
/// the file says.
///
/// Returns `None` when the file, the section or the key is absent. A key present
/// with an empty value returns `Some("")`, which is a *set* key: callers that
/// treat "declared" as meaningful (the embedded/DSN ambiguity check) must be able
/// to tell it from "absent".
pub fn sky_toml_section_key(project_dir: &Path, section: &str, key: &str) -> Option<String> {
    let text = std::fs::read_to_string(project_dir.join("sky.toml")).ok()?;
    let mut cur = String::new();
    for raw in text.lines() {
        let line = raw.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        if line.starts_with('[') {
            if let Some(end) = line.find(']') {
                cur = line[1..end].trim().trim_matches('"').to_string();
                continue;
            }
        }
        let Some((k, v)) = line.split_once('=') else {
            continue;
        };
        if cur == section && k.trim() == key {
            return Some(parse_toml_scalar(v));
        }
    }
    None
}

/// A `sky.toml` boolean. TOML's own spelling is bare `true`/`false`; `"true"`
/// and `yes`/`on`/`1` are accepted because a config file is written by hand and
/// an opt-in that silently reads as "off" is the worst possible failure — the
/// project looks configured and behaves as if it were not.
pub fn sky_toml_flag(project_dir: &Path, section: &str, key: &str) -> bool {
    matches!(
        sky_toml_section_key(project_dir, section, key)
            .map(|v| v.trim().to_ascii_lowercase())
            .as_deref(),
        Some("true" | "yes" | "on" | "1")
    )
}

/// Output binary name (`bin` key, default `app`) — the file `go build -o`
/// produces under the out dir and every run path launches.
pub fn configured_bin_name(project_dir: &Path) -> String {
    sky_toml_project_key(project_dir, "bin", "app")
}

/// Source-root directory (`root` key, default `src`) — where module discovery
/// walks for the project's own `.sky` files.
pub fn configured_source_root(project_dir: &Path) -> String {
    sky_toml_project_key(project_dir, "root", "src")
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

/// Escape a string into a Go double-quoted string literal. Used to embed the
/// migrations JSON (which can carry newlines from pretty-printing and, via a
/// user-typed text default, arbitrary characters) safely into generated Go.
fn go_string_literal(s: &str) -> String {
    let mut out = String::with_capacity(s.len() + 2);
    out.push('"');
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            c if (c as u32) < 0x20 => out.push_str(&format!("\\x{:02x}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
    out
}

/// Bake the project's committed `db/migrations/*.json` into a generated
/// `<out_dir>/embedded_migrations.go` (an `init()` that sets
/// `rt.SkyEmbeddedMigrations`), so a deployed `SKY_DB_OP=migrate ./app`
/// self-migrates with no source tree. When the project has no `db/migrations/`,
/// any stale generated file is removed so a binary never ships migrations the
/// project has dropped.
///
/// The `init()` sets the variable and nothing else. It used to CALL
/// `rt.MaybeApplyEmbeddedMigrationsAndExit` too, which could not work for an
/// `--embed` binary: Go runs every `init()` before `main`, and the cluster is
/// started from `main` — so the migration ran against a database that did not
/// exist yet. The call now lives in generated `main` immediately after
/// `rt.MaybeStartEmbeddedPostgres()` (`lower_main` in
/// `rust/crates/lower/src/lower.rs`, which documents why the start call cannot
/// move the other way to meet it). Setting the variable stays here, because a
/// plain assignment has no ordering requirement beyond "before `main` reads it",
/// which `init()` guarantees.
fn write_embedded_migrations(example_dir: &Path, out_dir: &Path) -> Result<(), String> {
    let migrations_dir = example_dir.join("db").join("migrations");
    let target = out_dir.join("embedded_migrations.go");
    if !migrations_dir.is_dir() {
        let _ = std::fs::remove_file(&target);
        return Ok(());
    }
    let mut files: Vec<PathBuf> = std::fs::read_dir(&migrations_dir)
        .map_err(|e| e.to_string())?
        .flatten()
        .map(|e| e.path())
        .filter(|p| p.extension().map(|x| x == "json").unwrap_or(false))
        .collect();
    files.sort();
    let bodies: Vec<String> = files
        .iter()
        .filter_map(|p| std::fs::read_to_string(p).ok())
        .map(|s| s.trim().to_string())
        .filter(|s| !s.is_empty())
        .collect();
    if bodies.is_empty() {
        let _ = std::fs::remove_file(&target);
        return Ok(());
    }
    let json = format!("[{}]", bodies.join(","));
    let go = format!(
        "package main\n\nimport rt \"sky-app/rt\"\n\n\
         // Generated by `sky build` from db/migrations/*.json — do not edit.\n\
         //\n\
         // This init() only SETS the variable. `main` calls\n\
         // rt.MaybeApplyEmbeddedMigrationsAndExit() after starting the embedded\n\
         // cluster; calling it from here would migrate before the database exists.\n\
         func init() {{\n\
         \trt.SkyEmbeddedMigrations = {}\n\
         }}\n",
        go_string_literal(&json)
    );
    std::fs::write(&target, go).map_err(|e| e.to_string())
}

/// The archive `sky build --embed` stages in the out dir, and the literal the
/// generated `//go:embed` directive is written against. Fixed, because a
/// `go:embed` path is a literal and cannot carry a version or a platform — see
/// `EMBEDDED_BUNDLE_FILENAME` in `rust/crates/sky/src/db_embed.rs`, and
/// `bundleIdentity` in `runtime-go/rt/pg_embed_bundle.go` for why the runtime's
/// extraction marker therefore keys on the archive's CONTENT and not this name.
pub const EMBEDDED_BUNDLE_FILENAME: &str = "postgres-bundle.tar.gz";
/// The generated file holding the `//go:embed` and the two assignments.
const EMBEDDED_BUNDLE_GO: &str = "pg_embed_bundle_gen.go";
/// Records which archive the staged copy came from, so a rebuild that changes
/// nothing does not re-copy 25MB.
const EMBEDDED_BUNDLE_STAMP: &str = ".sky-postgres-bundle";

/// Stage the PostgreSQL bundle for `sky build --embed`, or remove every trace of
/// a previous one.
///
/// Three properties, each of which has a way of going wrong that is invisible
/// until the binary is on a server:
///
/// - **The archive is embedded AS A TAR.** `go:embed` forces mode 0444 on every
///   file it carries and cannot represent a symlink at all, so embedding the
///   *extracted* tree yields a `postgres` that cannot be executed and a
///   `libpq.5.dylib` that does not exist. The tar has to survive intact into the
///   binary and be unpacked by the runtime.
/// - **`None` actively cleans up.** A build without `--embed` deletes the staged
///   archive, the generated Go and the stamp. Leaving them would keep 25MB of
///   PostgreSQL — and a `go:embed` of it — in every subsequent ordinary build of
///   that project, which is the "non-embed builds pay nothing" property failing
///   silently and expensively.
/// - **Re-staging is skipped when nothing changed.** The stamp records the
///   source path, length and mtime; a matching stamp with the archive still in
///   place means the copy is a no-op. This is what makes `sky build --embed`
///   twice cost one copy rather than two.
fn write_postgres_bundle(archive: Option<&Path>, out_dir: &Path) -> Result<(), String> {
    let staged = out_dir.join(EMBEDDED_BUNDLE_FILENAME);
    let generated = out_dir.join(EMBEDDED_BUNDLE_GO);
    let stamp = out_dir.join(EMBEDDED_BUNDLE_STAMP);

    let Some(archive) = archive else {
        for p in [&staged, &generated, &stamp] {
            let _ = std::fs::remove_file(p);
        }
        return Ok(());
    };

    let meta = std::fs::metadata(archive).map_err(|e| {
        format!(
            "sky build --embed: cannot read the PostgreSQL bundle {}: {e}",
            archive.display()
        )
    })?;
    if meta.len() == 0 {
        return Err(format!(
            "sky build --embed: the PostgreSQL bundle {} is empty.\n\
             An interrupted download or pack leaves exactly this, and embedding it \
             would produce a binary whose only failure is at first start.",
            archive.display()
        ));
    }
    let want = bundle_stamp(archive, &meta);
    let fresh = staged.is_file()
        && std::fs::read_to_string(&stamp).map(|s| s.trim() == want).unwrap_or(false);
    if !fresh {
        // Copy through a sibling and rename: a `go build` racing a half-written
        // 25MB archive embeds a truncated one, and gzip only notices at the far
        // end of the deploy.
        let tmp = out_dir.join(format!(".{EMBEDDED_BUNDLE_FILENAME}.part"));
        let _ = std::fs::remove_file(&tmp);
        std::fs::copy(archive, &tmp).map_err(|e| {
            let _ = std::fs::remove_file(&tmp);
            format!(
                "sky build --embed: cannot stage {} into {}: {e}",
                archive.display(),
                out_dir.display()
            )
        })?;
        std::fs::rename(&tmp, &staged)
            .map_err(|e| format!("sky build --embed: cannot stage {}: {e}", staged.display()))?;
        std::fs::write(&stamp, format!("{want}\n"))
            .map_err(|e| format!("sky build --embed: cannot write {}: {e}", stamp.display()))?;
    }

    let go = format!(
        "package main\n\n\
         // Generated by `sky build --embed` — do not edit.\n\
         //\n\
         // The bundle stays a TAR inside the embedded filesystem. `go:embed` forces\n\
         // mode 0444 on every file and cannot represent a symlink, so an embedded\n\
         // directory tree would give this binary a `postgres` it cannot execute and\n\
         // no `libpq.5.dylib` at all. rt unpacks it once, on first start.\n\n\
         import (\n\
         \t\"embed\"\n\n\
         \trt \"sky-app/rt\"\n\
         )\n\n\
         //go:embed {name}\n\
         var skyEmbeddedPostgresBundle embed.FS\n\n\
         func init() {{\n\
         \trt.EmbeddedPostgresBundle = skyEmbeddedPostgresBundle\n\
         \trt.EmbeddedPostgresBundleName = {lit}\n\
         }}\n",
        name = EMBEDDED_BUNDLE_FILENAME,
        lit = go_string_literal(EMBEDDED_BUNDLE_FILENAME),
    );
    // Only the assignment of the bundle lives in an `init()`. The two calls that
    // START and STOP a cluster are emitted into `func main()` (lower.rs
    // `lower_main`), because `[database] path`/`url` arrive as
    // `rt.SetSkyDefault` in the prologue `init()` and `--embed`'s ambiguity check
    // has to be able to see them.
    if std::fs::read_to_string(&generated).map(|s| s == go).unwrap_or(false) {
        return Ok(());
    }
    std::fs::write(&generated, go)
        .map_err(|e| format!("sky build --embed: cannot write {}: {e}", generated.display()))
}

fn bundle_stamp(archive: &Path, meta: &std::fs::Metadata) -> String {
    let mtime = meta
        .modified()
        .ok()
        .and_then(|t| t.duration_since(std::time::UNIX_EPOCH).ok())
        .map(|d| d.as_nanos())
        .unwrap_or(0);
    format!("{} {} {}", archive.display(), meta.len(), mtime)
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
    // Everything this call materialises at this level. Anything ELSE already in
    // `dst` is a leftover from a previous build with a different compiler and
    // gets pruned below.
    let mut produced: std::collections::BTreeSet<String> = std::collections::BTreeSet::new();
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
            produced.insert(name_s);
        } else if name_s.ends_with("_test.go") {
            continue;
        } else if name_s.ends_with(".go") {
            std::fs::copy(&path, dst.join(&name_s))?;
            produced.insert(name_s);
        }
    }
    prune_stale_rt(dst, &produced)?;
    Ok(())
}

/// Delete anything in a materialised `rt/` directory that this build did not
/// just write.
///
/// Without this, upgrading the compiler across a release that REMOVES a runtime
/// file leaves the old `.go` behind in an existing `sky-out/rt/`, and `go build`
/// then compiles a file whose helpers no longer exist:
///
///   rt/dict_key_display.go:31:30: undefined: dictKeyTagByte
///
/// The error names a file the user never wrote, about symbols that are correctly
/// absent, and it survives every rebuild until someone thinks to wipe `sky-out/`
/// — so the failure looks like a compiler bug rather than a stale artefact. The
/// examples are documented to build from a wiped slate, which is exactly why
/// nothing caught this: the wipe hid it.
///
/// Safe against the FFI wrappers that also live in `rt/`: `materialise_rt` runs
/// inside `write_out`, and `materialise_ffi_bindings` re-copies every binding
/// the program actually calls immediately afterwards in the same build. A
/// binding pruned here is either rewritten seconds later or genuinely no longer
/// used (`sky remove`), which is the case this also fixes.
fn prune_stale_rt(
    dst: &Path,
    produced: &std::collections::BTreeSet<String>,
) -> std::io::Result<()> {
    for entry in std::fs::read_dir(dst)? {
        let entry = entry?;
        let name_s = entry.file_name().to_string_lossy().to_string();
        if produced.contains(&name_s) {
            continue;
        }
        let path = entry.path();
        if path.is_dir() {
            std::fs::remove_dir_all(&path)?;
        } else if name_s.ends_with(".go") {
            // Only Go sources are ours to remove. A non-`.go` file in here was
            // not put there by the compiler, so leave it rather than deleting
            // something whose owner we cannot identify.
            std::fs::remove_file(&path)?;
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
mod embed_bundle_tests {
    use super::*;

    fn scratch(tag: &str) -> PathBuf {
        let d = std::env::temp_dir().join(format!("sky-p5b-{tag}-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&d);
        std::fs::create_dir_all(&d).unwrap();
        d
    }

    /// The `go:embed` directive, the `EmbeddedPostgresBundleName` assignment and
    /// the staged file must all name one literal. They are three separate
    /// strings in the generated Go; if they drift, the binary carries an archive
    /// under a name nothing opens, and the failure is at first start on the
    /// deployed host rather than here.
    #[test]
    fn the_generated_go_names_the_staged_archive_three_times_consistently() {
        let root = scratch("gen");
        let src = root.join("bundle.tar.gz");
        std::fs::write(&src, b"pretend gzip").unwrap();
        let out = root.join("sky-out");
        std::fs::create_dir_all(&out).unwrap();

        write_postgres_bundle(Some(&src), &out).unwrap();

        let staged = out.join(EMBEDDED_BUNDLE_FILENAME);
        assert!(staged.is_file(), "the archive was not staged");
        assert_eq!(std::fs::read(&staged).unwrap(), b"pretend gzip");

        let go = std::fs::read_to_string(out.join(EMBEDDED_BUNDLE_GO)).unwrap();
        assert!(go.contains(&format!("//go:embed {EMBEDDED_BUNDLE_FILENAME}")), "{go}");
        assert!(
            go.contains(&format!(
                "rt.EmbeddedPostgresBundleName = \"{EMBEDDED_BUNDLE_FILENAME}\""
            )),
            "{go}"
        );
        assert!(go.contains("rt.EmbeddedPostgresBundle = skyEmbeddedPostgresBundle"), "{go}");
        // The two calls that START a cluster belong in `func main()`, never here:
        // `[database] path`/`url` arrive as rt.SetSkyDefault in the prologue
        // `init()`, and from a second `init()` the ambiguity check cannot see
        // them (filename order decides which runs first).
        assert!(
            !go.contains("MaybeStartEmbeddedPostgres"),
            "the start call must not be emitted into an init(): {go}"
        );

        let _ = std::fs::remove_dir_all(&root);
    }

    /// A build without `--embed` must leave nothing behind. Otherwise the first
    /// `--embed` build in a project silently makes every later ordinary build
    /// 25MB heavier, which is the "non-embed builds pay nothing" property
    /// failing in the most expensive possible way.
    #[test]
    fn a_build_without_embed_removes_what_an_earlier_embed_build_left() {
        let root = scratch("clean");
        let src = root.join("bundle.tar.gz");
        std::fs::write(&src, b"pretend gzip").unwrap();
        let out = root.join("sky-out");
        std::fs::create_dir_all(&out).unwrap();

        write_postgres_bundle(Some(&src), &out).unwrap();
        assert!(out.join(EMBEDDED_BUNDLE_FILENAME).exists());

        write_postgres_bundle(None, &out).unwrap();
        for f in [EMBEDDED_BUNDLE_FILENAME, EMBEDDED_BUNDLE_GO, EMBEDDED_BUNDLE_STAMP] {
            assert!(!out.join(f).exists(), "{f} survived a non-embed build");
        }
        let _ = std::fs::remove_dir_all(&root);
    }

    /// The generated `embedded_migrations.go` SETS the migrations and does
    /// nothing else.
    ///
    /// It used to call `rt.MaybeApplyEmbeddedMigrationsAndExit()` from the same
    /// `init()`, which made `SKY_DB_OP=migrate ./app --embed` impossible by
    /// construction: Go runs every `init()` before `main`, the embedded cluster
    /// is started from `main`, so the migration ran against a database that did
    /// not exist yet and the binary exited saying so. The call belongs in
    /// `main`, immediately after the start (see `lower_main` in
    /// `rust/crates/lower/src/lower.rs` and
    /// `rust/crates/project/tests/embedded_main_prologue.rs`). The ASSIGNMENT
    /// stays here — a variable has no ordering requirement beyond "before `main`
    /// reads it", which is exactly what `init()` guarantees.
    #[test]
    fn the_generated_migrations_init_sets_them_and_does_not_apply_them() {
        let root = scratch("migrations");
        let out = root.join("sky-out");
        std::fs::create_dir_all(&out).unwrap();
        let mig = root.join("db").join("migrations");
        std::fs::create_dir_all(&mig).unwrap();
        std::fs::write(
            mig.join("0001_widgets.json"),
            r#"{"id":"0001_widgets","ops":[{"kind":"createTable","table":"widgets"}]}"#,
        )
        .unwrap();

        write_embedded_migrations(&root, &out).unwrap();
        let target = out.join("embedded_migrations.go");
        let go = std::fs::read_to_string(&target).unwrap();
        // Comment lines are stripped: the file explains WHY it does not apply the
        // migrations, and a gate that reads its own explanation as the thing it
        // forbids would make the file undocumentable.
        let code = go
            .lines()
            .filter(|l| !l.trim_start().starts_with("//"))
            .collect::<Vec<_>>()
            .join("\n");
        assert!(code.contains("rt.SkyEmbeddedMigrations = "), "{go}");
        assert!(
            code.contains("0001_widgets"),
            "the migration body is missing:\n{go}"
        );
        assert!(
            !code.contains("rt.MaybeApplyEmbeddedMigrationsAndExit()"),
            "the migration is applied from an init(), which runs BEFORE main — so \
             before `--embed` has started the database it is supposed to migrate:\n{go}"
        );

        // Dropping the migrations drops the generated file, so a rebuilt binary
        // never ships migrations the project no longer has.
        std::fs::remove_dir_all(root.join("db")).unwrap();
        write_embedded_migrations(&root, &out).unwrap();
        assert!(!target.exists(), "a stale embedded_migrations.go survived");

        let _ = std::fs::remove_dir_all(&root);
    }

    /// Two `--embed` builds with nothing changed copy the archive once.
    #[test]
    fn re_staging_an_unchanged_bundle_is_a_no_op() {
        let root = scratch("idem");
        let src = root.join("bundle.tar.gz");
        std::fs::write(&src, b"pretend gzip").unwrap();
        let out = root.join("sky-out");
        std::fs::create_dir_all(&out).unwrap();

        write_postgres_bundle(Some(&src), &out).unwrap();
        let staged = out.join(EMBEDDED_BUNDLE_FILENAME);
        let first = std::fs::metadata(&staged).unwrap().modified().unwrap();

        // Scribble on the staged copy: if it is re-copied, the scribble is gone.
        std::fs::write(&staged, b"scribbled").unwrap();
        write_postgres_bundle(Some(&src), &out).unwrap();
        assert_eq!(
            std::fs::read(&staged).unwrap(),
            b"scribbled",
            "an unchanged bundle was staged a second time"
        );
        let _ = first;

        // A CHANGED source is re-staged, though — that is the whole point of the
        // stamp carrying length and mtime rather than just the path.
        std::fs::write(&src, b"a different pretend gzip").unwrap();
        write_postgres_bundle(Some(&src), &out).unwrap();
        assert_eq!(std::fs::read(&staged).unwrap(), b"a different pretend gzip");

        let _ = std::fs::remove_dir_all(&root);
    }

    /// An interrupted download or pack leaves a zero-length archive. Embedding
    /// it produces a binary whose only failure is at first start, on the host.
    #[test]
    fn an_empty_or_missing_archive_fails_the_build_rather_than_being_embedded() {
        let root = scratch("empty");
        let out = root.join("sky-out");
        std::fs::create_dir_all(&out).unwrap();

        let empty = root.join("empty.tar.gz");
        std::fs::write(&empty, b"").unwrap();
        let e = write_postgres_bundle(Some(&empty), &out).unwrap_err();
        assert!(e.contains("empty"), "{e}");
        assert!(!out.join(EMBEDDED_BUNDLE_FILENAME).exists());

        let e = write_postgres_bundle(Some(&root.join("nope.tar.gz")), &out).unwrap_err();
        assert!(e.contains("cannot read"), "{e}");

        let _ = std::fs::remove_dir_all(&root);
    }
}

#[cfg(test)]
mod sky_toml_tests {
    use super::{
        accepted_config_keys, configured_bin_name, configured_source_root, db_driver_conflict,
        driver_for_dsn, read_sky_toml_config, sky_build_goflags_from, sky_toml_flag,
        sky_toml_section_key, unknown_config_keys,
    };

    #[test]
    fn goflags_preserve_user_flags_and_force_mod_and_buildvcs() {
        // Empty env → just Sky's two required flags.
        assert_eq!(sky_build_goflags_from(""), "-mod=mod -buildvcs=false");
        // A user's -buildvcs=false is honoured (dedup — Sky forces it anyway);
        // an unrelated flag is kept.
        assert_eq!(
            sky_build_goflags_from("-buildvcs=false -tags netgo"),
            "-tags netgo -mod=mod -buildvcs=false"
        );
        // A conflicting user -mod / -buildvcs is replaced by Sky's values.
        assert_eq!(
            sky_build_goflags_from("-mod=vendor -buildvcs=true"),
            "-mod=mod -buildvcs=false"
        );
    }

    #[test]
    fn live_keys_map_to_runtime_default_suffixes_and_auth_is_inert() {
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
        // [auth] is DELETED (§1.11): its keys seed NOTHING — no AUTH_* suffix is
        // emitted for any program. They are picked up by the inert-key warning
        // and their Removed migration rows instead.
        assert!(
            !cfg.extra_defaults.iter().any(|(s, _)| s.starts_with("AUTH_")),
            "no AUTH_* suffix may be seeded: {:?}",
            cfg.extra_defaults
        );
        // [log] keys → Std.Log's LOG_FORMAT / LOG_LEVEL.
        assert!(has("LOG_FORMAT", "json"));
        assert!(has("LOG_LEVEL", "debug"));
        // [database]. `path` is real — the runtime reads DB_PATH. `driver` is
        // NOT: nothing in runtime-go ever read DB_DRIVER / SKY_DB_DRIVER, and
        // the driver is chosen from the DSN's shape (`rt.detectDriver`,
        // runtime-go/rt/db_auth.go). Emitting it advertised a contract that did
        // not exist — two docs promised `SKY_DB_DRIVER` would select the driver.
        // The declared value is kept for the consistency check below, never
        // emitted as an env default.
        assert!(has("DB_PATH", "app.db"));
        assert!(
            !cfg.extra_defaults.iter().any(|(s, _)| s == "DB_DRIVER"),
            "DB_DRIVER must not be emitted — nothing reads it: {:?}",
            cfg.extra_defaults
        );
        assert_eq!(cfg.db_driver.as_deref(), Some("sqlite"));
        assert_eq!(cfg.db_dsn.as_deref(), Some("app.db"));
        // [env] prefix is a dedicated field (emitted as rt.SetEnvPrefix).
        assert_eq!(cfg.env_prefix.as_deref(), Some("FENCE"));

        let _ = std::fs::remove_dir_all(&dir);
    }

    /// `[jobs]` must reach the two suffixes `jobs_kernel.go` actually reads.
    ///
    /// It reached NOTHING before v0.19.14: no `jobs` arm existed, so the
    /// parser's `_ => {}` dropped the section — while the runtime's own degrade
    /// message instructed operators to "set sky.toml [jobs] store_path", and
    /// with `ENV=production` turned an unopenable store into a hard startup
    /// failure. Following the instruction changed nothing and the app still
    /// refused to start.
    #[test]
    fn jobs_section_seeds_the_store_env_defaults() {
        let dir = std::env::temp_dir().join("sky-jobs-toml-test");
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("sky.toml");
        std::fs::write(
            &path,
            "name = \"x\"\n[jobs]\nstore = \"postgres\"\n\
             storePath = \"postgres://u:p@h/db\"\n",
        )
        .unwrap();
        let cfg = read_sky_toml_config(&path);
        let has = |suffix: &str, value: &str| {
            cfg.extra_defaults
                .iter()
                .any(|(s, v)| s == suffix && v == value)
        };
        assert!(has("JOBS_STORE", "postgres"), "{:?}", cfg.extra_defaults);
        assert!(has("JOBS_STORE_PATH", "postgres://u:p@h/db"));
        // A section Sky reads in full must not also report itself unknown.
        assert!(
            cfg.unknown_config_keys.is_empty(),
            "{:?}",
            cfg.unknown_config_keys
        );

        // `store_path` is the spelling the runtime message used. It is accepted
        // rather than left inert, which is the whole point of this fix.
        std::fs::write(&path, "[jobs]\nstore_path = \"/tmp/j.db\"\n").unwrap();
        let cfg = read_sky_toml_config(&path);
        assert!(cfg
            .extra_defaults
            .iter()
            .any(|(s, v)| s == "JOBS_STORE_PATH" && v == "/tmp/j.db"));

        let _ = std::fs::remove_dir_all(&dir);
    }

    /// `[database]`'s pool + isolation keys must reach the suffixes
    /// `runtime-go/rt/db_pool.go` reads, and must not report themselves unknown.
    ///
    /// Before these existed, the PostgreSQL pool had no configuration surface at
    /// all — the runtime fell through on Go's `database/sql` defaults
    /// (`MaxOpenConns = 0`, i.e. unlimited) under a comment asserting those
    /// defaults were "already sane".
    #[test]
    fn database_pool_and_isolation_keys_seed_env_defaults() {
        let dir = std::env::temp_dir().join("sky-db-pool-toml-test");
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("sky.toml");
        std::fs::write(
            &path,
            "name = \"x\"\n[database]\nurl = \"postgres://u:p@h/db\"\n\
             maxOpenConns = 12\nmaxIdleConns = 12\n\
             connMaxLifetime = \"30m\"\nconnMaxIdleTime = \"5m\"\n\
             isolation = \"serializable\"\ntxRetry = 3\n",
        )
        .unwrap();
        let cfg = read_sky_toml_config(&path);
        let has = |suffix: &str, value: &str| {
            cfg.extra_defaults
                .iter()
                .any(|(s, v)| s == suffix && v == value)
        };
        assert!(has("DB_MAX_OPEN_CONNS", "12"), "{:?}", cfg.extra_defaults);
        assert!(has("DB_MAX_IDLE_CONNS", "12"));
        assert!(has("DB_CONN_MAX_LIFETIME", "30m"));
        assert!(has("DB_CONN_MAX_IDLE_TIME", "5m"));
        assert!(has("DB_ISOLATION", "serializable"));
        assert!(has("DB_TX_RETRY", "3"));
        assert!(
            cfg.unknown_config_keys.is_empty(),
            "{:?}",
            cfg.unknown_config_keys
        );

        let _ = std::fs::remove_dir_all(&dir);
    }

    /// `[database] embedded` is the embedded-PostgreSQL opt-in
    /// (docs/skydb/embedded-postgres.md). It is read by `sky run`, not by the
    /// app, so it must seed NO runtime default — and it must not be reported as
    /// a key Sky ignores, because the one key that opts a project in cannot also
    /// be the one key that warns it has no effect.
    #[test]
    fn the_embedded_opt_in_is_a_toolchain_key_that_seeds_nothing_and_warns_nothing() {
        let dir = std::env::temp_dir().join("sky-embedded-toml-test");
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("sky.toml");
        std::fs::write(&path, "name = \"x\"\n[database]\nembedded = true\n").unwrap();

        let cfg = read_sky_toml_config(&path);
        assert!(
            !cfg.extra_defaults.iter().any(|(s, _)| s.starts_with("DB_")),
            "`embedded` must not reach the app's environment: {:?}",
            cfg.extra_defaults
        );
        assert!(
            cfg.unknown_config_keys.is_empty(),
            "the opt-in key reports itself as honoured by nothing: {:?}",
            cfg.unknown_config_keys
        );
        assert!(sky_toml_flag(&dir, "database", "embedded"));
        assert!(!sky_toml_flag(&dir, "database", "isolation"));

        // Bare TOML `false`, the quoted spellings, and the absent case.
        for (text, want) in [
            ("[database]\nembedded = false\n", false),
            ("[database]\nembedded = \"true\"\n", true),
            ("[database]\nembedded = true  # dev only\n", true),
            ("[database]\npath = \"a.db\"\n", false),
            // Right key, wrong section: the flag is scoped, or a `[live]
            // embedded` would silently start a PostgreSQL.
            ("[live]\nembedded = true\n", false),
        ] {
            std::fs::write(&path, text).unwrap();
            assert_eq!(sky_toml_flag(&dir, "database", "embedded"), want, "{text:?}");
        }

        let _ = std::fs::remove_dir_all(&dir);
    }

    /// `sky_toml_section_key` must read a DSN back VERBATIM. The older
    /// `sky_toml_project_key` sanitises its value to a single path segment, so a
    /// `postgres://…` URL comes back as the default — a caller that used it for
    /// the embedded/DSN ambiguity check would see "no DSN declared" and start a
    /// cluster over the top of the operator's database.
    #[test]
    fn section_keys_come_back_verbatim_and_distinguish_empty_from_absent() {
        let dir = std::env::temp_dir().join("sky-section-key-test");
        std::fs::create_dir_all(&dir).unwrap();
        std::fs::write(
            dir.join("sky.toml"),
            "name = \"x\"\n[env]\nprefix = \"FENCE\"\n\
             [database]\nurl = \"postgres://u:p@host/db\"\npath = \"\"\n",
        )
        .unwrap();

        assert_eq!(
            sky_toml_section_key(&dir, "database", "url").as_deref(),
            Some("postgres://u:p@host/db")
        );
        assert_eq!(sky_toml_section_key(&dir, "env", "prefix").as_deref(), Some("FENCE"));
        // Set-but-empty is not the same as absent.
        assert_eq!(sky_toml_section_key(&dir, "database", "path").as_deref(), Some(""));
        assert_eq!(sky_toml_section_key(&dir, "database", "driver"), None);
        assert_eq!(sky_toml_section_key(&dir, "nosuch", "url"), None);

        let _ = std::fs::remove_dir_all(&dir);
    }

    /// A key in a runtime config section that Sky does not read must be
    /// REPORTED, not dropped.
    ///
    /// The `[auth]` keys below are not hypothetical: `examples/08-notes-app`
    /// and `examples/12-skyvote` both shipped exactly these. `[auth]` is now a
    /// DELETED section (its parse arms and prologue seeds are gone, §1.11), so
    /// EVERY key under it — including `cookieName`, once half-accepted — is
    /// reported as inert, and the message names `[auth]` as not a section Sky
    /// reads. A `[live]` typo (`prot`) is the other half of the class.
    #[test]
    fn unknown_keys_in_config_sections_are_reported() {
        let dir = std::env::temp_dir().join("sky-unknown-key-test");
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("sky.toml");
        std::fs::write(
            &path,
            "name = \"x\"\n[auth]\nmethod = \"password\"\n\
             session_ttl = \"24h\"\nemail_verification = true\n\
             cookieName = \"ok\"\n[live]\nprot = 9000\n\
             [source]\nroot = \"src\"\n[\"go.dependencies\"]\n\"os\" = \"latest\"\n",
        )
        .unwrap();
        let cfg = read_sky_toml_config(&path);

        let flagged: Vec<&str> = cfg
            .unknown_config_keys
            .iter()
            .map(|(_, k)| k.as_str())
            .collect();
        assert!(flagged.contains(&"method"), "{flagged:?}");
        assert!(flagged.contains(&"session_ttl"), "{flagged:?}");
        assert!(flagged.contains(&"email_verification"), "{flagged:?}");
        // A typo in a real section is the other half of the class.
        assert!(flagged.contains(&"prot"), "{flagged:?}");
        // `[auth]` is gone: even `cookieName`, once an accepted key, is now inert
        // and must be flagged. (It is ALSO named by its Removed migration row —
        // the two are complementary, not exclusive.)
        assert!(flagged.contains(&"cookieName"), "{flagged:?}");
        // Sections consumed elsewhere are out of scope — flagging `root` or a Go
        // module path would be noise, and noise is what gets warnings ignored.
        assert!(!flagged.contains(&"root"), "{flagged:?}");
        assert!(!flagged.contains(&"\"os\""), "{flagged:?}");

        // The message has to name what to do. An `[auth]` key now warns that
        // `[auth]` is not a section Sky reads (naming the real runtime sections),
        // rather than suggesting a sibling `[auth]` key — because there are none.
        let msgs = unknown_config_keys(&cfg.unknown_config_keys);
        let auth_msg = msgs
            .iter()
            .find(|m| m.contains("session_ttl"))
            .expect("session_ttl warned");
        assert!(auth_msg.contains("no effect"), "{auth_msg}");
        assert!(
            auth_msg.contains("not a section Sky reads"),
            "{auth_msg}"
        );
        // And the runtime-sections list it names must no longer advertise
        // `[auth]` as a section (it opens with the key `[auth] session_ttl`, so
        // we check the section LIST fragment specifically).
        assert!(!auth_msg.contains("`[database]`, `[auth]`"), "{auth_msg}");

        let _ = std::fs::remove_dir_all(&dir);
    }

    /// The accepted-key table must not drift from the `match` that implements
    /// it. If it does, the warning starts recommending a key the parser ignores
    /// — advice that is worse than no advice.
    ///
    /// This asserts the direction that matters: every key the table advertises
    /// is a key the parser actually honours.
    #[test]
    fn advertised_keys_are_keys_the_parser_honours() {
        let dir = std::env::temp_dir().join("sky-accepted-keys-test");
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("sky.toml");
        for section in [
            "live", "database", "auth", "log", "analytics", "jobs", "env", "security",
        ] {
            for key in accepted_config_keys(section) {
                std::fs::write(&path, format!("[{section}]\n{key} = \"v\"\n")).unwrap();
                let cfg = read_sky_toml_config(&path);
                assert!(
                    cfg.unknown_config_keys.is_empty(),
                    "`[{section}] {key}` is advertised as accepted but the parser \
                     does not handle it: {:?}",
                    cfg.unknown_config_keys
                );
            }
        }
        let _ = std::fs::remove_dir_all(&dir);
    }

    /// `[security]` was parsed by nothing AND absent from
    /// `is_runtime_config_section`, so its keys were dropped without even the
    /// unknown-key warning that covers every other runtime section.
    ///
    /// That silence had a victim: `runtime-go/rt/observability.go` serves a 401
    /// whose hint tells the locked-out operator to "set [security] env" — advice
    /// that could not work, given to someone already locked out of their own
    /// metrics endpoint.
    #[test]
    fn security_section_keys_are_not_silently_dropped() {
        let dir = std::env::temp_dir().join("sky-security-section-test");
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("sky.toml");

        // `[security] env` is NOT wired (deployment environment is not a
        // build-time constant — see the parser comment). It must therefore
        // WARN rather than vanish.
        std::fs::write(&path, "[security]\nenv = \"production\"\n").unwrap();
        let cfg = read_sky_toml_config(&path);
        assert!(
            cfg.unknown_config_keys
                .iter()
                .any(|(s, k)| s == "security" && k == "env"),
            "`[security] env` was dropped with no warning: {:?}",
            cfg.unknown_config_keys
        );
        let warnings = unknown_config_keys(&cfg.unknown_config_keys);
        assert!(
            warnings.iter().any(|w| w.contains("ENV")),
            "the `[security] env` warning must name the ENV environment \
             variable that DOES work: {warnings:?}"
        );

        // `[security] csrf` IS wired: the runtime half already exists
        // (rt.SetCsrfEnabled / SKY_CSRF), only the compiler half was missing.
        std::fs::write(&path, "[security]\ncsrf = false\n").unwrap();
        let cfg = read_sky_toml_config(&path);
        assert!(
            cfg.extra_defaults
                .iter()
                .any(|(k, v)| k == "CSRF" && v == "false"),
            "`[security] csrf = false` must seed the CSRF runtime default, \
             got extra_defaults {:?}",
            cfg.extra_defaults
        );
        assert!(
            cfg.unknown_config_keys.is_empty(),
            "`[security] csrf` is wired and must not warn: {:?}",
            cfg.unknown_config_keys
        );

        let _ = std::fs::remove_dir_all(&dir);
    }

    /// The root cause behind the `[security]` bug: a key in an UNRECOGNISED
    /// section was dropped in total silence, because the unknown-key warning
    /// only ever fired for sections already on the known list.
    ///
    /// So the warning could never tell you about the one mistake it is most
    /// important to catch — a section name that is wrong. A typo'd
    /// `[databse] path` and a whole-cloth `[observability] enabled` were
    /// equally invisible.
    #[test]
    fn keys_in_unknown_sections_warn() {
        let dir = std::env::temp_dir().join("sky-unknown-section-test");
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("sky.toml");

        for (section, key) in [
            ("databse", "path"),        // typo of an existing section
            ("observability", "enabled"), // claimed by observability.go, parsed by nothing
            ("totally_made_up", "x"),
        ] {
            std::fs::write(&path, format!("[{section}]\n{key} = \"v\"\n")).unwrap();
            let cfg = read_sky_toml_config(&path);
            assert!(
                cfg.unknown_config_keys
                    .iter()
                    .any(|(s, k)| s == section && k == key),
                "`[{section}] {key}` was dropped with no warning: {:?}",
                cfg.unknown_config_keys
            );
        }

        // …and the sections that are consumed elsewhere must stay quiet, or
        // every real project gets noise on every build.
        for (section, key) in [
            ("project", "bin"),
            ("source", "root"),
            ("dependencies", "somelib"),
            ("go.dependencies", "github.com/x/y"),
            ("lib", "name"),
            ("", "port"),
        ] {
            let body = if section.is_empty() {
                format!("{key} = \"v\"\n")
            } else {
                format!("[{section}]\n{key} = \"v\"\n")
            };
            std::fs::write(&path, body).unwrap();
            let cfg = read_sky_toml_config(&path);
            assert!(
                cfg.unknown_config_keys.is_empty(),
                "`[{section}] {key}` is consumed elsewhere and must not warn: {:?}",
                cfg.unknown_config_keys
            );
        }

        let _ = std::fs::remove_dir_all(&dir);
    }

    /// `driver_for_dsn` must stay a faithful mirror of `rt.detectDriver`
    /// (`runtime-go/rt/db_auth.go`) — it is the whole basis of the consistency
    /// check, so a drift here would make the check itself lie.
    #[test]
    fn driver_for_dsn_mirrors_the_runtime() {
        assert_eq!(driver_for_dsn("postgres://u:p@h/db"), "pgx");
        assert_eq!(driver_for_dsn("postgresql://u:p@h/db"), "pgx");
        assert_eq!(driver_for_dsn("POSTGRES://u:p@h/db"), "pgx");
        assert_eq!(driver_for_dsn("host=localhost user=app dbname=x"), "pgx");
        assert_eq!(driver_for_dsn("./app.db"), "sqlite");
        assert_eq!(driver_for_dsn("app.db"), "sqlite");
        assert_eq!(driver_for_dsn("file:x.db?cache=shared"), "sqlite");
    }

    /// The defect, pinned: `driver = "postgres"` beside a SQLite path used to be
    /// accepted in silence while the app opened SQLite. It must now be reported.
    #[test]
    fn contradicting_driver_and_dsn_is_reported() {
        let w = db_driver_conflict(Some("postgres"), Some("./app.db"))
            .expect("a postgres driver over a sqlite path must be reported");
        assert!(w.contains("sqlite"), "must name the driver actually used: {w}");
        assert!(w.contains("./app.db"), "must quote the DSN: {w}");

        let w = db_driver_conflict(Some("sqlite"), Some("postgres://u@h/db"))
            .expect("a sqlite driver over a postgres URL must be reported");
        assert!(w.contains("postgres"), "must name the driver actually used: {w}");
    }

    /// …and the converse, so the check cannot pass by shouting at everyone.
    #[test]
    fn agreeing_or_absent_driver_is_silent() {
        assert!(db_driver_conflict(Some("sqlite"), Some("./app.db")).is_none());
        // `pgx` is the runtime's internal name; users write postgres/postgresql.
        assert!(db_driver_conflict(Some("postgres"), Some("postgres://u@h/d")).is_none());
        assert!(db_driver_conflict(Some("postgresql"), Some("postgres://u@h/d")).is_none());
        assert!(db_driver_conflict(Some("pgx"), Some("postgres://u@h/d")).is_none());
        // No declared driver, or no declared DSN (it may arrive at run time via
        // SKY_DB_PATH / DATABASE_URL) → nothing to check.
        assert!(db_driver_conflict(None, Some("./app.db")).is_none());
        assert!(db_driver_conflict(Some("postgres"), None).is_none());
    }

    #[test]
    fn scalar_values_strip_inline_comments_and_quotes() {
        // Regression: a `store = "postgres"  # note` line used to seed
        // LIVE_STORE=`postgres"       # note`, which never matched `case
        // "postgres"` in the runtime → silent fallback (in-memory session store)
        // on a raw-binary deploy. Values must drop the inline comment + quotes;
        // a `#` INSIDE a quoted value is preserved.
        let dir = std::env::temp_dir().join(format!("skytoml-comment-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("sky.toml");
        std::fs::write(
            &path,
            "[live]   # section trailing comment\n\
             store        = \"postgres\"       # sessions in the shared Postgres\n\
             ttl          = 2592000          # 30 days\n\
             static       = public            # bare unquoted value + comment\n\
             storePath    = \"data/a#b.db\"    # a # inside quotes is preserved\n\
             [log]\n\
             format       = \"json\"           # structured\n\
             level        = debug\n",
        )
        .unwrap();
        let cfg = read_sky_toml_config(&path);
        let has = |suffix: &str, value: &str| {
            cfg.extra_defaults
                .iter()
                .any(|(s, v)| s == suffix && v == value)
        };
        assert!(has("LIVE_STORE", "postgres"), "{:?}", cfg.extra_defaults);
        assert!(has("LIVE_TTL", "2592000"));
        assert!(has("LIVE_STATIC_DIR", "public"));
        assert!(has("LIVE_STORE_PATH", "data/a#b.db")); // '#' inside quotes kept
        assert!(has("LOG_FORMAT", "json")); // quoted value + trailing comment
        assert!(has("LOG_LEVEL", "debug")); // bare value
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn project_key_reads_sections_and_sanitises() {
        let dir = std::env::temp_dir().join(format!("skytoml-projkey-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let write = |body: &str| std::fs::write(dir.join("sky.toml"), body).unwrap();

        // Top-level (bare) keys.
        write("name = \"x\"\nbin = \"myserver\"\nroot = \"lib\"\n");
        assert_eq!(configured_bin_name(&dir), "myserver");
        assert_eq!(configured_source_root(&dir), "lib");

        // [project] table form.
        write("[project]\nbin = \"srv\"\nroot = \"app_src\"\n");
        assert_eq!(configured_bin_name(&dir), "srv");
        assert_eq!(configured_source_root(&dir), "app_src");

        // [source] table form for root.
        write("name = \"x\"\n[source]\nroot = \"code\"\n");
        assert_eq!(configured_source_root(&dir), "code");

        // Defaults when absent.
        write("name = \"x\"\n");
        assert_eq!(configured_bin_name(&dir), "app");
        assert_eq!(configured_source_root(&dir), "src");

        // Inline comment after a quoted value is stripped (was a garbage name).
        write("bin = \"srv\"  # output name\nroot = \"code\"  # source dir\n");
        assert_eq!(configured_bin_name(&dir), "srv");
        assert_eq!(configured_source_root(&dir), "code");

        // Sanitisation: a path-escaping value falls back to the default so
        // `go build -o` can never write outside the out dir.
        write("bin = \"../evil\"\nroot = \"a/b\"\n");
        assert_eq!(configured_bin_name(&dir), "app");
        assert_eq!(configured_source_root(&dir), "src");

        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn database_url_is_an_alias_for_path() {
        let dir = std::env::temp_dir().join(format!("skytoml-dburl-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("sky.toml");
        std::fs::write(
            &path,
            "name = \"x\"\n[database]\ndriver = \"postgres\"\n\
             url = \"postgres://localhost/mydb\"\n",
        )
        .unwrap();
        let cfg = read_sky_toml_config(&path);
        // `url` seeds DB_PATH (detectDriver routes the postgres:// DSN to pgx).
        assert!(
            cfg.extra_defaults
                .iter()
                .any(|(s, v)| s == "DB_PATH" && v == "postgres://localhost/mydb"),
            "{:?}",
            cfg.extra_defaults
        );
        let _ = std::fs::remove_dir_all(&dir);
    }
}

#[cfg(test)]
mod materialise_rt_tests {
    use super::materialise_rt;
    use std::fs;
    use std::path::{Path, PathBuf};

    fn scratch(tag: &str) -> PathBuf {
        let d = std::env::temp_dir().join(format!("sky-rtmat-{tag}-{}", std::process::id()));
        let _ = fs::remove_dir_all(&d);
        fs::create_dir_all(&d).unwrap();
        d
    }

    fn write(p: &Path, name: &str, body: &str) {
        fs::create_dir_all(p).unwrap();
        fs::write(p.join(name), body).unwrap();
    }

    /// The upgrade path: a release REMOVES a runtime file, and an existing
    /// `sky-out/` must not keep compiling the old one.
    ///
    /// Before the prune, `dst` kept `dropped.go` forever and `go build` failed
    /// on symbols that were correctly deleted — an error naming a file the user
    /// never wrote, surviving every rebuild until someone wiped `sky-out/`.
    #[test]
    fn a_runtime_file_deleted_upstream_is_removed_from_an_existing_out_dir() {
        let root = scratch("del");
        let src = root.join("src");
        let dst = root.join("dst");

        write(&src, "kept.go", "package rt\n");
        write(&src, "dropped.go", "package rt\nfunc gone() {}\n");
        materialise_rt(&src, &dst, false).unwrap();
        assert!(dst.join("dropped.go").exists(), "setup: first build copies it");

        // The upgrade: upstream no longer ships `dropped.go`.
        fs::remove_file(src.join("dropped.go")).unwrap();
        write(&src, "added.go", "package rt\n");
        materialise_rt(&src, &dst, false).unwrap();

        assert!(dst.join("kept.go").exists(), "an unchanged file must survive");
        assert!(dst.join("added.go").exists(), "a new file must appear");
        assert!(
            !dst.join("dropped.go").exists(),
            "STALE RUNTIME FILE: `dropped.go` no longer exists upstream but was \
             left in the materialised rt/, so `go build` would compile it and \
             fail on helpers that are correctly gone"
        );

        let _ = fs::remove_dir_all(&root);
    }

    /// Same hazard one level down — `rt/` has subpackages.
    #[test]
    fn a_deleted_file_in_a_subpackage_is_also_removed() {
        let root = scratch("sub");
        let src = root.join("src");
        let dst = root.join("dst");

        write(&src.join("sub"), "a.go", "package sub\n");
        write(&src.join("sub"), "b.go", "package sub\n");
        materialise_rt(&src, &dst, false).unwrap();

        fs::remove_file(src.join("sub").join("b.go")).unwrap();
        materialise_rt(&src, &dst, false).unwrap();

        assert!(dst.join("sub").join("a.go").exists());
        assert!(
            !dst.join("sub").join("b.go").exists(),
            "the prune must recurse; a subpackage is where console_app lives"
        );

        let _ = fs::remove_dir_all(&root);
    }

    /// A whole subpackage that stops being materialised must not linger.
    /// `console_app` is skipped when the program does not blank-import it, so a
    /// CLI rebuilt from a Live app's out-dir would otherwise keep the console
    /// package sitting in its tree.
    #[test]
    fn a_subpackage_no_longer_materialised_is_removed() {
        let root = scratch("consoleapp");
        let src = root.join("src");
        let dst = root.join("dst");

        write(&src, "rt.go", "package rt\n");
        write(&src.join("console_app"), "app.go", "package console_app\n");

        materialise_rt(&src, &dst, true).unwrap();
        assert!(dst.join("console_app").join("app.go").exists());

        // Rebuilt as a program that does not need the console.
        materialise_rt(&src, &dst, false).unwrap();
        assert!(dst.join("rt.go").exists());
        assert!(
            !dst.join("console_app").exists(),
            "console_app was skipped this build, so the stale copy must go"
        );

        let _ = fs::remove_dir_all(&root);
    }

    /// The prune must not reach past what the compiler owns. Only `.go` files
    /// are removed; anything else in the directory has an owner we cannot
    /// identify, and deleting a user's file to fix our own staleness would be a
    /// far worse bug than the one being fixed.
    #[test]
    fn a_non_go_file_is_left_alone() {
        let root = scratch("nongo");
        let src = root.join("src");
        let dst = root.join("dst");

        write(&src, "rt.go", "package rt\n");
        materialise_rt(&src, &dst, false).unwrap();
        write(&dst, "notes.txt", "not ours\n");

        materialise_rt(&src, &dst, false).unwrap();
        assert!(
            dst.join("notes.txt").exists(),
            "only .go files are the compiler's to delete"
        );

        let _ = fs::remove_dir_all(&root);
    }

    /// `_test.go` files are never copied, so they must never be "pruned" in a
    /// way that makes the copy step and the prune step disagree.
    #[test]
    fn test_files_are_neither_copied_nor_resurrected() {
        let root = scratch("tests");
        let src = root.join("src");
        let dst = root.join("dst");

        write(&src, "rt.go", "package rt\n");
        write(&src, "rt_test.go", "package rt\n");
        materialise_rt(&src, &dst, false).unwrap();

        assert!(dst.join("rt.go").exists());
        assert!(!dst.join("rt_test.go").exists(), "tests are stripped");

        let _ = fs::remove_dir_all(&root);
    }
}

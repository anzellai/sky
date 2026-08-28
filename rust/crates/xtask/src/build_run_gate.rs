//! `xtask build-run` — the reproducible "examples run" gate.
//!
//! For each example the gate classifies its *app shape* from the entry module,
//! then verifies it end-to-end with a shape-appropriate strategy.
//!
//! CLI — build, `go build`, run (feed test stdin), normalise (strip ISO
//! timestamps), diff stdout vs the Haskell oracle binary.
//!
//! Live / Http — build, `go build`, start the server binary on a spare port
//! under a watchdog, wait-for-ready (parse the listening line), `curl /`,
//! normalise (sky-id / data-sky-* / script / style / cookies / csrf /
//! timestamps), diff vs the oracle server started the same way. Both servers
//! are ALWAYS killed (even on timeout).
//!
//! Tui — build, `go build`, run under a pty (`script`) feeding a quit key.
//! RUN = "no-panic" (exits without a Go panic). MATCH is not attempted (pty
//! frame diffing is unreliable).
//!
//! Webview — BUILD only (macOS GUI; can't headless-verify). RUN = n/a.
//!
//! FFI examples — the Rust `sky` generates their Go-package surfaces itself
//! (`sky install` shells out to the embedded introspector), so they build under
//! the Rust compiler: 05-mux-server (gorilla/mux) build+run+match; 13-skyshop
//! (76k Stripe symbols) builds+runs; 11-fyne-stopwatch is cgo build-only
//! (`Shape::Ffi`, native GUI). Small surfaces are committed (`sky-ffi/`, e.g.
//! 05); the large 11/13 surfaces are `.gitignore`d and regenerated on demand
//! (13's Stripe surface is 54 MB). "FFI-blocked" only applies when the local
//! `go` toolchain can't introspect a package.
//!
//! Usage:
//!   xtask build-run                       # CLI-family, build-only (fast)
//!   xtask build-run --all                 # whole corpus, build + classify
//!   xtask build-run --all --run           # whole corpus, full verify (run+match)
//!   xtask build-run --only=NAME[,NAME…]   # filter to named examples
//!   xtask build-run --shape cli|live|http|tui|webview   # filter by shape
//!   xtask build-run --run [-v]            # run + oracle match for the selection

use project::{build_example, BuildOptions};
use std::io::{BufRead, BufReader, Write};
use std::net::TcpListener;
use std::path::Path;
use std::process::{Child, Command, Stdio};
use std::sync::mpsc;
use std::time::{Duration, Instant};

/// CLI-family examples the gate targets by default (no `--all`).
const CLI_FAMILY: &[&str] = &[
    "01-hello-world",
    "02-go-stdlib",
    "14-task-demo",
    "07-todo-cli",
    "00-standard-libs",
    "20-cli-counter",
    // Regression for anzellai/sky#153 — cross-module parametric ADT codegen.
    "49-xmodule-adt",
    // Regression for the v0.18.1 open-row closure-param codegen fix.
    "50-open-row-closure",
    // Regression for anzellai/sky#155 — kernel-alias currying arity comes from
    // the declared Sky sig, not the Go runtime func's (variadic) param scan.
    "51-kernel-variadic-arity",
];

/// Heavy-FFI GUI examples that build but cannot be headless-verified (native
/// macOS GUI via cgo — the Fyne stopwatch opens a window + blocks on its event
/// loop). Classified as `Shape::Ffi`: emit + `go build` are exercised, run is
/// n/a — the exact build-only ceiling Webview already occupies. `13-skyshop`
/// (the 76k-symbol Stripe Sky.Live benchmark) is NOT here — it classifies as a
/// normal Live app and is run+matched like any other server.
const FFI_BUILD_ONLY: &[&str] = &["11-fyne-stopwatch"];

/// CLI examples whose stdout is inherently nondeterministic (live `Time.now`
/// timestamps + network calls to httpbin.org), so a byte-for-byte match against
/// the oracle's stdout is a guaranteed flake (the two binaries run at different
/// wall-clock and network states). Verified as RUN "no-panic" instead of "match"
/// — the build proves codegen, the run proves it doesn't crash; the exact output
/// can't be pinned. (02-go-stdlib: `Time.now |> Time.timeString` + `Http.get`.)
const NONDETERMINISTIC_OUTPUT: &[&str] = &["02-go-stdlib"];

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
enum Shape {
    Cli,
    Live,
    Http,
    Tui,
    Webview,
    /// A Sky.Spa client (`Spa.app`) — the TEA loop compiles to wasm and runs on
    /// the client (web / desktop / iOS / Android); the native binary is not a
    /// stdout CLI. Build-only here, like Webview — codegen is exercised, the
    /// wasm + auto-split behaviour is covered by `spa_split_flow` + the examples.
    Spa,
    Ffi,
}

impl Shape {
    fn label(self) -> &'static str {
        match self {
            Shape::Cli => "cli",
            Shape::Live => "live",
            Shape::Http => "http",
            Shape::Tui => "tui",
            Shape::Webview => "webview",
            Shape::Spa => "spa",
            Shape::Ffi => "ffi",
        }
    }
    fn from_flag(s: &str) -> Option<Shape> {
        match s {
            "cli" => Some(Shape::Cli),
            "live" => Some(Shape::Live),
            "http" => Some(Shape::Http),
            "tui" => Some(Shape::Tui),
            "webview" => Some(Shape::Webview),
            "spa" => Some(Shape::Spa),
            "ffi" => Some(Shape::Ffi),
            _ => None,
        }
    }
}

/// Per-example stdin to feed the binary (line-oriented CLI/TUI apps read stdin).
fn stdin_for(name: &str) -> Option<String> {
    match name {
        "20-cli-counter" => Some("+\n+\n-\nq\n".to_string()),
        _ => None,
    }
}

struct Row {
    name: String,
    shape: Shape,
    emitted: bool,
    build_ok: bool,
    run_ok: Option<bool>,
    /// Some(true/false) = matched/differed; None = not applicable.
    matched: Option<bool>,
    /// For TUI: RUN is "no-panic" rather than a stdout match.
    run_kind: &'static str, // "match" | "no-panic" | "n/a"
    blocker: String,
    /// Raw stdout captured from the RUST binary (CLI examples only, when run).
    /// The golden gate normalises this via `normalise_stdout` at compare time.
    rust_stdout: Option<String>,
    /// Raw stdout captured from the ORACLE binary (CLI examples, only when
    /// `--run` verified against an existing `sky-out/app`). Drives the local
    /// drift check (oracle_normalised vs committed golden).
    oracle_stdout: Option<String>,
}

/// `du -sk <dir>` → size in KiB.
fn du_kb(dir: &str) -> Option<u64> {
    let out = Command::new("du").args(["-sk", dir]).output().ok()?;
    String::from_utf8_lossy(&out.stdout)
        .split_whitespace()
        .next()?
        .parse()
        .ok()
}

/// Available space (KiB) on the filesystem holding the current dir. `df -k .`'s
/// Available column is field index 3 on both macOS and Linux.
fn df_avail_kb() -> Option<u64> {
    let out = Command::new("df").args(["-k", "."]).output().ok()?;
    let s = String::from_utf8_lossy(&out.stdout);
    s.lines().last()?.split_whitespace().nth(3)?.parse().ok()
}

/// Preflight disk-hygiene guard (CLAUDE.md §6). The whole-corpus sweep runs 50+
/// `go build`s that pile into the go-build cache; on a near-full disk that hits
/// ENOSPC mid-sweep — corrupting half-written artifacts AND wedging any Docker /
/// Postgres backed by the same volume. So: cap the cache first, then refuse to
/// start if free space is low, with a clear message instead of a cryptic
/// "no space left on device" halfway through.
fn preflight_disk_guard() -> Result<(), String> {
    let home = std::env::var("HOME").unwrap_or_default();
    let mut cache = format!("{home}/Library/Caches/go-build");
    if !Path::new(&cache).is_dir() {
        let xdg = std::env::var("XDG_CACHE_HOME").unwrap_or_else(|_| format!("{home}/.cache"));
        cache = format!("{xdg}/go-build");
    }
    // Prune only under REAL disk pressure — never on cache size alone.
    //
    // A full corpus sweep legitimately produces ~11 GB of go-build cache. Keying
    // the prune off size alone (`> 5 GB`) therefore wiped the cache at the start
    // of EVERY run, so every corpus build was cold: the gate deleted the exact
    // artifacts it was about to rebuild. On a machine with plenty of free space
    // that is pure cost, and it made `scripts/preflight-tag.sh` unpassable — its
    // rust-gate step is bounded by `timeout 3600`, and a cold full-corpus build
    // cannot finish inside an hour, so releases failed with no error text at all.
    //
    // The guard's actual purpose (AGENTS.md "Disk hygiene") is avoiding ENOSPC
    // mid-sweep, which is a function of FREE SPACE, not of how big the cache got.
    // Below we still refuse outright under 10 GB free; this prune gives the sweep
    // room to breathe before that, and only when space is genuinely tight.
    const PRUNE_CACHE_ABOVE_KB: u64 = 5 * 1024 * 1024; // 5 GB of cache …
    const PRUNE_WHEN_FREE_BELOW_KB: u64 = 30 * 1024 * 1024; // … but only under 30 GB free
    if Path::new(&cache).is_dir() {
        if let Some(kb) = du_kb(&cache) {
            let free_kb = df_avail_kb().unwrap_or(u64::MAX);
            if kb > PRUNE_CACHE_ABOVE_KB && free_kb < PRUNE_WHEN_FREE_BELOW_KB {
                eprintln!(
                    "[build-run] go-build cache is {} GB and only {} GB free — running 'go clean -cache'",
                    kb / 1024 / 1024,
                    free_kb / 1024 / 1024
                );
                let _ = Command::new("go").args(["clean", "-cache"]).status();
            }
        }
    }
    if let Some(free_kb) = df_avail_kb() {
        if free_kb < 10 * 1024 * 1024 {
            return Err(format!(
                "only {} GB free — refusing the corpus sweep (it fills the go-build cache and would risk ENOSPC mid-run). Reclaim space (`go clean -cache`, `df -h /`) and retry.",
                free_kb / 1024 / 1024
            ));
        }
    }
    Ok(())
}

/// Parse `--shard=I/N`: gate only slice `I` of `N`, interleaved by index. Mirrors
/// `repro_gate`'s flag so a fan-out reads the same everywhere. Rejects a malformed
/// or out-of-range spec by aborting — a shard that silently gates nothing (or the
/// wrong slice) is how a fan-out reports PASS over a corpus it never checked.
fn parse_shard(args: &[String]) -> Option<(usize, usize)> {
    let spec = args.iter().find_map(|a| a.strip_prefix("--shard="))?;
    let (i, n) = spec
        .split_once('/')
        .unwrap_or_else(|| panic!("build-run: --shard expects I/N (e.g. 0/2), got {spec:?}"));
    let i: usize = i
        .trim()
        .parse()
        .unwrap_or_else(|_| panic!("build-run: --shard index not a number: {i:?}"));
    let n: usize = n
        .trim()
        .parse()
        .unwrap_or_else(|_| panic!("build-run: --shard count not a number: {n:?}"));
    if n == 0 || i >= n {
        panic!("build-run: --shard=I/N requires 0 <= I < N (got {i}/{n})");
    }
    Some((i, n))
}

/// Keep only slice `I` of `N` (interleaved by index, stride `N`) when `shard` is
/// set. Interleave rather than a contiguous split so the few heavy examples
/// (19-skyforum, 26-ui-showcase, 13-skyshop …) spread across shards and they
/// finish in roughly equal wall-clock. Pure + separate from `run` so its
/// disjoint-and-total property is unit-tested.
fn apply_shard(names: Vec<String>, shard: Option<(usize, usize)>) -> Vec<String> {
    match shard {
        Some((i, n)) => names
            .into_iter()
            .enumerate()
            .filter(|(idx, _)| idx % n == i)
            .map(|(_, name)| name)
            .collect(),
        None => names,
    }
}

pub fn run(args: &[String], root: &Path) -> i32 {
    if let Err(msg) = preflight_disk_guard() {
        eprintln!("BUILD-RUN GATE: ABORTED — {msg}");
        return 1;
    }
    let only: Option<Vec<String>> = args
        .iter()
        .find_map(|a| a.strip_prefix("--only="))
        .map(|s| {
            s.split(',')
                .map(|x| x.trim().to_string())
                .filter(|x| !x.is_empty())
                .collect()
        });
    let shape_filter = args
        .iter()
        .position(|a| a == "--shape")
        .and_then(|i| args.get(i + 1))
        .and_then(|s| Shape::from_flag(s));
    let do_verify = args
        .iter()
        .any(|a| a == "--run" || a == "--oracle" || a == "--serve");
    let verbose = args.iter().any(|a| a == "-v" || a == "--verbose");
    let all = args.iter().any(|a| a == "--all");
    // `--golden`: compare each CLI example's normalised stdout to its committed
    // `golden/<name>.stdout` (the CI runtime-correctness gate; NEVER runs the
    // oracle). `--bless`: capture goldens, but ONLY the oracle-verified ones.
    let golden = args.iter().any(|a| a == "--golden");
    let bless = args.iter().any(|a| a == "--bless");

    let shard = parse_shard(args);
    let mut names: Vec<String> = match &only {
        Some(n) => n.clone(),
        None if all || shape_filter.is_some() => corpus(root),
        None => CLI_FAMILY.iter().map(|s| s.to_string()).collect(),
    };
    // `--shard=I/N`: CI-level fan-out. The dominant cost of this gate is the
    // per-example `go build`, bounded by one runner's cores, so splitting the
    // corpus across N sibling jobs is the only lever that adds real hardware
    // parallelism. Sharded BEFORE the shape filter, on the sorted name list, so
    // each shape's slice is disjoint across shards and their union is the whole
    // shape — `--shape live --run --shard=0/2` ∪ `--shard=1/2` runs every live
    // example exactly once. `corpus()` is already sorted; sort defensively for the
    // `--only`/CLI-family paths too so the partition is deterministic everywhere.
    if shard.is_some() {
        names.sort();
    }
    let names = apply_shard(names, shard);

    // The examples this invocation will actually touch, resolved up front so the
    // work can be handed out and the table still printed in corpus order.
    let selected: Vec<(String, Shape)> = names
        .iter()
        .filter_map(|name| {
            let dir = root.join("examples").join(name);
            if !dir.is_dir() {
                return None;
            }
            let shape = classify(&dir, name);
            if let Some(want) = shape_filter {
                if shape != want {
                    return None;
                }
            }
            Some((name.clone(), shape))
        })
        .collect();

    // Emit + `go build` for every example is the longest single step in CI —
    // 735s of the codegen-build job's 1338s, which is what puts the T1 tier over
    // its ceiling. Each example builds in its OWN directory, so the work is
    // independent; only GOCACHE is shared, and Go makes that safe for concurrent
    // use.
    //
    // RUNNING is a different matter and stays SERIAL. `verify_one` with
    // `do_verify` starts the built binary, and the server shapes bind a fixed
    // port — two at once would fight over it and produce a failure that says
    // nothing about the code. Those invocations (`--shape live --run` and
    // friends) measure 10-32s in CI, so there is nothing to win there anyway.
    let concurrent = !do_verify && !bless;
    let rows: Vec<Row> = if concurrent {
        build_run_parallel(root, &selected, do_verify, golden, bless, verbose, jobs(args))
    } else {
        selected
            .iter()
            .map(|(name, shape)| {
                let dir = root.join("examples").join(name);
                verify_one(root, &dir, name, *shape, do_verify, golden, bless, verbose)
            })
            .collect()
    };

    print_table(&rows);
    let base = gate_result(&rows);

    // ---- golden / bless phase (runtime-correctness) ----
    // `--bless` captures oracle-verified goldens and returns (a capture run is
    // not a gate). Otherwise, when `--golden` (compare) or `--run` (drift) is
    // active, run the golden gate over the committed snapshots.
    let gphase = if bless {
        bless_goldens(root, &rows, verbose)
    } else if golden || do_verify {
        golden_gate(root, &rows, golden, do_verify, only.is_some())
    } else {
        0
    };

    base.max(gphase)
}

fn corpus(root: &Path) -> Vec<String> {
    let mut ds: Vec<String> = std::fs::read_dir(root.join("examples"))
        .map(|rd| {
            rd.filter_map(|e| e.ok())
                .filter(|e| e.path().is_dir())
                .filter_map(|e| e.file_name().to_str().map(String::from))
                .filter(|n| root.join("examples").join(n).join("src").is_dir())
                // skip the non-example scratch dirs
                .filter(|n| n != "simple" && n != "test_pkg")
                .collect()
        })
        .unwrap_or_default();
    ds.sort();
    ds
}

// ---- Std.App dispatched entries -----------------------------------------

/// True when `dir`'s entry is a DISPATCHED Std.App entry: it `import`s Std.App
/// and calls the BARE `App.run` dispatcher (not a concrete `App.runLive` /
/// `App.runTui`). Such an entry is NOT buildable by the in-process
/// [`build_example`] — that emits the `App.run` runTui PLACEHOLDER, a binary
/// that `go build`s cleanly but fails at RUNTIME ("terminal:tui requires a
/// Std.Ui or String view" for an App.web app; "stdin is not a terminal" for an
/// App.app one). The CLI's `sky build` resolves the real target instead; see
/// [`verify_std_app`]. Mirrors `sky`'s own `is_std_app_dispatched_entry`.
fn is_std_app_dispatched(dir: &Path) -> bool {
    let Some(src) = entry_source(&dir.join("src")) else {
        return false;
    };
    let imports_app = src
        .lines()
        .any(|l| l.trim_start().starts_with("import Std.App"));
    imports_app && uses_bare_app_run(&src)
}

/// The source calls the bare `App.run` dispatcher — an `App.run` occurrence NOT
/// followed by an identifier character (so `App.runTui` / `App.runLive` do not
/// count). Mirrors `sky`'s `is_bare_app_run_at`.
fn uses_bare_app_run(src: &str) -> bool {
    let needle = "App.run";
    let mut from = 0;
    while let Some(i) = src[from..].find(needle) {
        let pos = from + i;
        match src.as_bytes().get(pos + needle.len()) {
            None => return true,
            Some(&c) if !(c.is_ascii_alphanumeric() || c == b'_') => return true,
            _ => {}
        }
        from = pos + needle.len();
    }
    false
}

/// The persisted `[app] target` from `dir/sky.toml`, if any. Hand-rolled to
/// match `sky`'s `parse_toml_app_target` (we never pulled in a TOML parser for
/// reads). `None` = no pin, so `sky build` defaults to `web` (Sky.Live).
fn app_target(dir: &Path) -> Option<String> {
    let toml = std::fs::read_to_string(dir.join("sky.toml")).ok()?;
    let mut in_app = false;
    for line in toml.lines() {
        let line = line.trim();
        if line.starts_with('#') {
            continue;
        }
        if line.starts_with('[') {
            in_app = line == "[app]";
            continue;
        }
        if in_app {
            if let Some(rest) = line.strip_prefix("target") {
                let v = rest
                    .trim_start()
                    .strip_prefix('=')?
                    .trim()
                    .trim_matches(|c| c == '"' || c == '\'');
                if !v.is_empty() {
                    return Some(v.to_string());
                }
            }
        }
    }
    None
}

/// The shape of the binary `sky build` will produce for a dispatched Std.App
/// entry, resolved from `[app] target` (default `web` = Sky.Live). `None` when
/// `dir` is not a dispatched Std.App entry — the caller then falls back to the
/// `main`-body scan. This is why a migrated `20-cli-counter` (`[app] target =
/// "terminal:cli"`) classifies as Cli and a `09-live-counter` (no pin) as Live,
/// even though both read `main = App.run appDef`.
fn std_app_dispatch_shape(dir: &Path) -> Option<Shape> {
    if !is_std_app_dispatched(dir) {
        return None;
    }
    Some(match app_target(dir).as_deref() {
        // web / tablet families + no pin → Sky.Live server (runLive).
        None | Some("web") | Some("tablet") => Shape::Live,
        Some("terminal:tui") => Shape::Tui,
        Some("terminal:cli") => Shape::Cli,
        // A bare `desktop` is a native Live window (runLiveWindow, cgo) — a GUI,
        // build-only like Webview. Every other family variant (`web:app`,
        // `mobile:*`, `desktop:mac|windows|linux`, `tablet:*`) is a wasm Spa
        // client — also build-only here.
        Some("desktop") => Shape::Webview,
        Some(_) => Shape::Spa,
    })
}

// ---- shape classification ------------------------------------------------

/// Detect an example's app shape from its entry module's `main` binding, using
/// the DEFAULT (no-args) branch for arg-dispatched multi-backend mains, and
/// falling back to a whole-example scan when the main body only delegates.
fn classify(dir: &Path, name: &str) -> Shape {
    if FFI_BUILD_ONLY.contains(&name) {
        return Shape::Ffi;
    }
    // A dispatched Std.App entry's shape is the target `sky build` resolves for
    // it, NOT the `App.run` the main-body scan would (mis)read as Live. Checked
    // before the scan so `[app] target` wins.
    if let Some(s) = std_app_dispatch_shape(dir) {
        return s;
    }
    let src = dir.join("src");
    let entry = entry_source(&src);
    let main_body = entry.as_deref().map(main_binding).unwrap_or_default();

    // Arg-dispatched mains (24 kitchen-sink, 38 multibackend): the no-args run
    // takes the wildcard `_ ->` arm, so classify by the LAST arm's call.
    let seg = if (main_body.contains("argsList") || main_body.contains("System.args"))
        && main_body.contains("case")
    {
        main_body
            .rsplit_once("_ ->")
            .map(|(_, tail)| tail.to_string())
            .unwrap_or(main_body.clone())
    } else {
        main_body.clone()
    };

    if let Some(s) = shape_of_segment(&seg) {
        return s;
    }
    // Fallback: the main body only delegates (e.g. `entry ()`); scan all src.
    whole_example_shape(&src).unwrap_or(Shape::Cli)
}

fn shape_of_segment(seg: &str) -> Option<Shape> {
    // Std.App unified entry (`main = App.run appDef`, built by `sky build` to its
    // DEFAULT target = web = Sky.Live). The gate builds the default target, so a
    // Std.App app runs as a Live server here regardless of which other targets it
    // also supports (terminal:tui/cli, desktop, web:app — those are covered by
    // std_app_flow + spa_split_flow). Checked FIRST because a migrated app may
    // still mention `App.route`/config that could otherwise mislead the scan.
    if seg.contains("App.run") {
        return Some(Shape::Live);
    }
    if seg.contains("Webview.app") || seg.contains("Webview.program") {
        return Some(Shape::Webview);
    }
    if seg.contains("Tui.app") || seg.contains("Tui.program") {
        return Some(Shape::Tui);
    }
    if seg.contains("Spa.app") {
        return Some(Shape::Spa);
    }
    if seg.contains("Live.app") {
        return Some(Shape::Live);
    }
    if seg.contains("Server.listen") || seg.contains("HttpServer.listen") {
        return Some(Shape::Http);
    }
    // Raw net/http server via FFI (`Http.listenAndServe`, e.g. 05-mux-server):
    // an HTTP shape driven by a Go-FFI listen call rather than Sky.Http.Server.
    if seg.contains("listenAndServe") {
        return Some(Shape::Http);
    }
    // bare `app { … routes … notFound … }` from
    // `import Std.Live exposing (app, route)`. Reached only after Webview/Tui/
    // Live.app are ruled out, so `notFound` here always means a bare Live app.
    if seg.contains("notFound") && (seg.contains("routes") || seg.contains("app")) {
        return Some(Shape::Live);
    }
    None
}

fn whole_example_shape(src: &Path) -> Option<Shape> {
    let mut files = Vec::new();
    collect_sky(src, &mut files);
    let mut blob = String::new();
    for f in &files {
        if let Ok(s) = std::fs::read_to_string(f) {
            blob.push_str(&s);
            blob.push('\n');
        }
    }
    if blob.contains("Server.listen")
        || blob.contains("HttpServer.listen")
        || blob.contains("listenAndServe")
    {
        Some(Shape::Http)
    } else if blob.contains("Live.app") {
        Some(Shape::Live)
    } else if blob.contains("Tui.app") || blob.contains("Tui.program") {
        Some(Shape::Tui)
    } else if blob.contains("Webview.app") {
        Some(Shape::Webview)
    } else {
        None
    }
}

/// Read the source of the entry module (the src file defining a top-level `main`).
fn entry_source(src: &Path) -> Option<String> {
    let mut files = Vec::new();
    collect_sky(src, &mut files);
    // prefer a file literally named Main.sky, else the first with a `main` def.
    files.sort();
    let mut fallback = None;
    for f in &files {
        let Ok(s) = std::fs::read_to_string(f) else {
            continue;
        };
        let has_main = s
            .lines()
            .any(|l| l.starts_with("main ") || l == "main" || l.starts_with("main="));
        if has_main {
            if f.file_name().and_then(|n| n.to_str()) == Some("Main.sky") {
                return Some(s);
            }
            if fallback.is_none() {
                fallback = Some(s);
            }
        }
    }
    fallback
}

/// Extract the text of the top-level `main` binding (from its `main`/`main :`
/// header to the next top-level definition).
fn main_binding(src: &str) -> String {
    let mut out = String::new();
    let mut in_main = false;
    for line in src.lines() {
        let starts_main = line.starts_with("main ")
            || line == "main"
            || line.starts_with("main=")
            || line.starts_with("main:");
        if starts_main {
            in_main = true;
            out.push_str(line);
            out.push('\n');
            continue;
        }
        if in_main {
            // a new top-level binding starts at column 0 with an identifier and
            // is not a continuation of main.
            let is_toplevel = !line.is_empty()
                && !line.starts_with(char::is_whitespace)
                && !line.starts_with("main");
            if is_toplevel {
                break;
            }
            out.push_str(line);
            out.push('\n');
        }
    }
    out
}

// ---- per-example verification -------------------------------------------

/// How many examples to build CONCURRENTLY. `--jobs N`, else `XTASK_BUILD_JOBS`,
/// else the machine's parallelism capped at 8 — each worker drives a `go build`
/// that already parallelises internally, so more than that oversubscribes.
fn jobs(args: &[String]) -> usize {
    args.iter()
        .position(|a| a == "--jobs")
        .and_then(|i| args.get(i + 1))
        .and_then(|s| s.parse::<usize>().ok())
        .or_else(|| {
            std::env::var("XTASK_BUILD_JOBS")
                .ok()
                .and_then(|s| s.parse::<usize>().ok())
        })
        .unwrap_or_else(|| {
            std::thread::available_parallelism()
                .map(|n| n.get())
                .unwrap_or(1)
                .min(8)
        })
        .max(1)
}

/// Build every selected example, up to `jobs` at a time, preserving corpus order
/// in the returned rows so the printed table does not reorder run to run.
#[allow(clippy::too_many_arguments)]
fn build_run_parallel(
    root: &Path,
    selected: &[(String, Shape)],
    do_verify: bool,
    golden: bool,
    bless: bool,
    verbose: bool,
    jobs: usize,
) -> Vec<Row> {
    let mut slots: Vec<Option<Row>> = (0..selected.len()).map(|_| None).collect();
    let next = std::sync::atomic::AtomicUsize::new(0);

    std::thread::scope(|scope| {
        let mut handles = Vec::new();
        for _ in 0..jobs.min(selected.len().max(1)) {
            handles.push(scope.spawn(|| {
                let mut done: Vec<(usize, Row)> = Vec::new();
                loop {
                    let i = next.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
                    let Some((name, shape)) = selected.get(i) else {
                        break;
                    };
                    let dir = root.join("examples").join(name);
                    done.push((
                        i,
                        verify_one(root, &dir, name, *shape, do_verify, golden, bless, verbose),
                    ));
                }
                done
            }));
        }
        for h in handles {
            // A panicking worker must abort the gate. Dropping it would remove
            // its examples from the table and shrink the denominator silently —
            // which is how a gate comes to report PASS over a corpus it never
            // finished building.
            for (i, row) in h.join().expect("build-run worker thread panicked") {
                slots[i] = Some(row);
            }
        }
    });

    slots
        .into_iter()
        .enumerate()
        .map(|(i, r)| r.unwrap_or_else(|| panic!("build-run: example {i} produced no row")))
        .collect()
}

fn verify_one(
    root: &Path,
    dir: &Path,
    name: &str,
    shape: Shape,
    do_verify: bool,
    golden: bool,
    bless: bool,
    verbose: bool,
) -> Row {
    // CLI runs through build_example's own run+stdin path; servers/tui build
    // without the blocking run (we drive the process ourselves). `--golden` /
    // `--bless` also need the RUST binary to run (they compare/capture its
    // stdout), so force the inline run for CLI even without `--run`.
    let want_inline_run = (do_verify || golden || bless) && shape == Shape::Cli;
    // Both the generated Go-FFI surface (`sky-ffi/`) and the fetched Sky deps
    // (`.skydeps/`) are gitignored build artifacts, so a fresh clone / CI has
    // neither. Generate/fetch whatever is ABSENT for EVERY dep-declaring example
    // — this is exactly what a user runs (`sky install`) before building an FFI
    // project. Previously the build-only sweep (`--all`, no `--run`) skipped this
    // to avoid the heavy surface regeneration, but that silently LEFT every
    // Go-FFI example (03/05/08/13, uuid / gorilla-mux / firestore / stripe …)
    // build-blocked and untested under `build-run --all`. Doing it here is
    // net-neutral in CI: the shape-specific run gates (golden / http / live)
    // regenerate the same surfaces later in the SAME job, and `ensure_ffi_surface`
    // no-ops when the surface is already present (`need` is false), so the cost
    // is paid once and reused, just moved earlier so `--all` is comprehensive.
    ensure_ffi_surface(root, dir);
    // A dispatched Std.App entry cannot go through the in-process build_example
    // below: that emits the `App.run` runTui placeholder (a binary that builds
    // but fails at runtime). Build it the way a user + example-sweep do — the
    // CLI's `sky build`, which resolves the real target and copies the binary to
    // <dir>/sky-out/app (build_std_app names THIS gate as a consumer) — and
    // verify that.
    if is_std_app_dispatched(dir) {
        return verify_std_app(root, dir, name, shape, do_verify, verbose);
    }
    let opts = BuildOptions {
        repo_root: root.to_path_buf(),
        example_dir: dir.to_path_buf(),
        out_dir_name: "sky-out-rust".into(),
        out_dir_abs: None,
        run: want_inline_run,
        stdin: stdin_for(name),
        entry_module: None,
        progress: false,
        embed_bundle: None,
        wasm: false,
    };
    let rep = build_example(&opts);
    let out_dir = dir.join("sky-out-rust");

    let mut blocker = String::new();
    if !rep.emitted {
        blocker = rep.note.clone();
    } else if !rep.go_build_ok {
        blocker = first_go_error(&rep.go_build_stderr);
    }
    if verbose && !rep.go_build_ok && !rep.go_build_stderr.is_empty() {
        eprintln!(
            "  [{name}] go build stderr:\n{}",
            indent(&rep.go_build_stderr, 6)
        );
    }

    let mut run_ok = None;
    let mut matched = None;
    let mut run_kind = "n/a";
    // Raw RUST stdout, captured whenever the inline CLI run fired (verify OR
    // golden/bless). The golden gate normalises + compares it. `run_ok` is set
    // here too, so a golden/bless-only run (no `--run`) still knows the binary ran.
    let rust_stdout = if want_inline_run {
        rep.run_stdout.clone()
    } else {
        None
    };
    let mut oracle_stdout = None;
    if want_inline_run && !do_verify {
        run_ok = rep.run_ok;
    }

    if do_verify && rep.go_build_ok {
        match shape {
            Shape::Cli if NONDETERMINISTIC_OUTPUT.contains(&name) => {
                // stdout is inherently nondeterministic (live `Time.now` +
                // network calls), so a byte-match against the oracle is a
                // guaranteed flake (Rust + oracle run at different wall-clock /
                // network states). Verify RUN succeeds (no Go panic) only — the
                // build already proved codegen; the output can't be pinned.
                run_kind = "no-panic";
                run_ok = rep.run_ok;
                matched = None;
                if rep.run_ok != Some(true) && blocker.is_empty() {
                    blocker = truncate(rep.run_stderr.as_deref().unwrap_or("run failed"), 60);
                }
            }
            Shape::Cli => {
                run_kind = "match";
                run_ok = rep.run_ok;
                if rep.run_ok == Some(true) {
                    // Run the oracle binary (if present), stash its raw stdout
                    // for the golden drift check, and compare normalised forms.
                    oracle_stdout = run_oracle_stdout(dir, stdin_for(name));
                    if let Some(oracle) = &oracle_stdout {
                        let rust = rep.run_stdout.as_deref().unwrap_or("");
                        matched = Some(normalise_stdout(oracle) == normalise_stdout(rust));
                    }
                    if matched == Some(false) && blocker.is_empty() {
                        blocker = "stdout != oracle".into();
                    }
                } else if blocker.is_empty() {
                    blocker = truncate(rep.run_stderr.as_deref().unwrap_or("run failed"), 60);
                }
            }
            Shape::Live | Shape::Http => {
                run_kind = "match";
                let (ok, m, note) = verify_server(dir, &out_dir, name, shape, verbose);
                run_ok = Some(ok);
                matched = m;
                if !note.is_empty() && blocker.is_empty() {
                    blocker = note;
                }
            }
            Shape::Tui => {
                run_kind = "no-panic";
                let (ok, note) = verify_tui(&out_dir, name);
                run_ok = Some(ok);
                matched = None;
                if !note.is_empty() && blocker.is_empty() {
                    blocker = note;
                }
            }
            Shape::Webview => {
                run_kind = "n/a";
                if blocker.is_empty() {
                    blocker = "macOS GUI — build-only".into();
                }
            }
            Shape::Spa => {
                // A Sky.Spa client — the native binary is not a stdout CLI; the
                // TEA loop runs as wasm. Build proves codegen; wasm + auto-split
                // behaviour is covered by spa_split_flow + the examples.
                run_kind = "n/a";
                if blocker.is_empty() {
                    blocker = "wasm/webview client — build-only".into();
                }
            }
            Shape::Ffi => {
                // Heavy-FFI GUI (Fyne): native macOS window + blocking event
                // loop — build is the ceiling, same as Webview.
                run_kind = "n/a";
                if blocker.is_empty() {
                    blocker = "macOS GUI (cgo) — build-only".into();
                }
            }
        }
    }

    Row {
        name: name.into(),
        shape,
        emitted: rep.emitted,
        build_ok: rep.go_build_ok,
        run_ok,
        matched,
        run_kind,
        blocker,
        rust_stdout,
        oracle_stdout,
    }
}

/// Build + verify a DISPATCHED Std.App entry via the CLI `sky build`, which the
/// in-process `build_example` cannot do (it emits the `App.run` runTui
/// placeholder). `sky build` resolves the `[app] target` (default web =
/// Sky.Live), builds the derived entry, and copies the binary to
/// `<dir>/sky-out/app`; we then drive it by the shape `std_app_dispatch_shape`
/// picked. The oracle (legacy Haskell) has no Std.App, so there is no stdout
/// match here — Live/Http probe the server, Tui/Cli assert no-panic, and the
/// client/GUI shapes (Spa/Webview) are build-only, exactly as in `verify_one`.
fn verify_std_app(
    root: &Path,
    dir: &Path,
    name: &str,
    shape: Shape,
    do_verify: bool,
    verbose: bool,
) -> Row {
    let sky = root.join("sky-out").join("sky");
    let build = Command::new(&sky)
        .arg("build")
        .arg("src/Main.sky")
        .current_dir(dir)
        .output();
    let mut row = Row {
        name: name.into(),
        shape,
        emitted: false,
        build_ok: false,
        run_ok: None,
        matched: None,
        run_kind: "n/a",
        blocker: String::new(),
        rust_stdout: None,
        oracle_stdout: None,
    };
    let out = match build {
        Ok(o) => o,
        Err(e) => {
            row.blocker = format!("sky build spawn: {e}");
            return row;
        }
    };
    row.emitted = true;
    if !out.status.success() {
        let combined = format!(
            "{}{}",
            String::from_utf8_lossy(&out.stdout),
            String::from_utf8_lossy(&out.stderr)
        );
        row.blocker = first_go_error(&combined);
        if row.blocker.is_empty() {
            row.blocker = "sky build (Std.App dispatch) failed".into();
        }
        return row;
    }
    if verbose {
        eprintln!("  [{name}] sky build (Std.App dispatch) → sky-out/app");
    }
    row.build_ok = true;
    // build_std_app copies the target binary to the standard <dir>/sky-out/app.
    let out_dir = dir.join("sky-out");
    if !do_verify {
        return row;
    }
    match shape {
        Shape::Live | Shape::Http => {
            row.run_kind = "match";
            let (ok, m, note) = verify_server(dir, &out_dir, name, shape, verbose);
            row.run_ok = Some(ok);
            row.matched = m;
            if !note.is_empty() {
                row.blocker = note;
            }
        }
        // A full-screen App.tui: assert it BOOTS + renders without a Go panic.
        // NOT "quits on a piped key" — a raw-mode TUI reader cannot be reliably
        // driven to quit through a pty from a harness (timing + input-mode
        // dependent), so requiring it makes every real TUI "hang". Surviving a
        // short grace window alive is the run-verifiable property.
        Shape::Tui => {
            row.run_kind = "no-panic";
            let (ok, note) = verify_tui_boots(&out_dir, name);
            row.run_ok = Some(ok);
            if !note.is_empty() {
                row.blocker = note;
            }
        }
        // A line-oriented App.cli: run it plainly (NOT under a pty — the pty
        // quit-key path hangs a line-oriented reader), feed the example's stdin,
        // close the pipe, and assert it exits without a Go panic. No oracle to
        // match, since Std.App has no legacy-Haskell equivalent.
        Shape::Cli => {
            row.run_kind = "no-panic";
            let (ok, note) = verify_cli_nopanic(&out_dir, name);
            row.run_ok = Some(ok);
            if !note.is_empty() {
                row.blocker = note;
            }
        }
        // wasm client / native GUI window — build-only, run covered by
        // spa_split_flow + std_app_flow (same ceiling as verify_one's arms).
        Shape::Spa | Shape::Webview | Shape::Ffi => {
            row.run_kind = "n/a";
        }
    }
    row
}

/// Assert a full-screen `App.tui` binary at `<out_dir>/app` BOOTS + renders its
/// first frame(s) without a Go panic. A TUI in raw mode cannot be reliably
/// driven to quit through a pty from a harness, so "quits on q" is the wrong
/// property (it makes every real TUI hang) — surviving a short grace window
/// alive is the right one. A best-effort quit key is still sent: if the app
/// honours it we get a clean early exit (also a pass); otherwise the grace
/// window elapses and we kill it. A panic — or a non-zero exit BEFORE the grace
/// (an ERROR before render) — is a failure.
fn verify_tui_boots(out_dir: &Path, _name: &str) -> (bool, String) {
    let app = out_dir.join("app");
    if !app.exists() {
        return (false, "no binary".into());
    }
    // Under a pty (`script -q /dev/null ./app`) so the runtime's isatty check
    // passes and it enters full-screen mode, same as verify_tui.
    let mut cmd = Command::new("script");
    cmd.arg("-q")
        .arg("/dev/null")
        .arg("./app")
        .current_dir(out_dir)
        .env("TERM", "xterm")
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    let mut child = match cmd.spawn() {
        Ok(c) => c,
        Err(e) => return (false, format!("spawn: {e}")),
    };
    if let Some(mut si) = child.stdin.take() {
        let _ = si.write_all(b"q\n\x03");
    }
    let deadline = Instant::now() + Duration::from_secs(3);
    loop {
        match child.try_wait() {
            Ok(Some(status)) => {
                let mut stderr = Vec::new();
                if let Some(mut e) = child.stderr.take() {
                    let _ = std::io::Read::read_to_end(&mut e, &mut stderr);
                }
                let se = String::from_utf8_lossy(&stderr);
                if se.contains("panic:") || se.contains("goroutine ") {
                    return (false, truncate(se.trim(), 60));
                }
                // Clean exit (`System.exit 0` on quit) passes; a non-zero exit
                // before render is a real failure.
                return if status.success() {
                    (true, String::new())
                } else {
                    (false, truncate(se.trim(), 60))
                };
            }
            Ok(None) => {
                if Instant::now() >= deadline {
                    // Alive through the grace window → booted + rendering.
                    let _ = child.kill();
                    let _ = child.wait();
                    return (true, String::new());
                }
                std::thread::sleep(Duration::from_millis(50));
            }
            Err(e) => return (false, format!("wait: {e}")),
        }
    }
}

/// Run a line-oriented `App.cli` binary at `<out_dir>/app` (a dispatched Std.App
/// build), feed the example's stdin then EOF, and assert it exits without a Go
/// panic. Not under a pty — a line reader hangs on the pty quit-key path
/// verify_tui uses. `wait_bounded` caps a wedged reader at 20s.
fn verify_cli_nopanic(out_dir: &Path, name: &str) -> (bool, String) {
    let app = out_dir.join("app");
    if !app.exists() {
        return (false, "no binary".into());
    }
    let mut cmd = Command::new(&app);
    cmd.current_dir(out_dir)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    let mut child = match cmd.spawn() {
        Ok(c) => c,
        Err(e) => return (false, format!("spawn: {e}")),
    };
    // Write the example's stdin (if any) then drop the handle → EOF, so a
    // stdin-reading loop terminates instead of blocking the bounded wait.
    if let Some(mut si) = child.stdin.take() {
        if let Some(data) = stdin_for(name) {
            let _ = si.write_all(data.as_bytes());
        }
    }
    match wait_bounded(&mut child, Duration::from_secs(20)) {
        Some(out) => {
            let stderr = String::from_utf8_lossy(&out.stderr);
            // A Go panic prints `panic:` + a `goroutine` dump; a Sky-level fatal
            // exits non-zero with an ERROR line. Either is a run failure.
            if stderr.contains("panic:") || stderr.contains("goroutine ") {
                (false, truncate(stderr.trim(), 60))
            } else if out.status.success() {
                (true, String::new())
            } else {
                (false, truncate(stderr.trim(), 60))
            }
        }
        None => (false, "cli hung".into()),
    }
}

/// Generate the FFI surface for an example that declares deps but whose surface
/// is absent (gitignored artifact — missing on a fresh clone). Idempotent + only
/// hits the network/inspector when a surface is genuinely missing, so a warm
/// tree is a no-op. Failures are non-fatal: the subsequent `build_example` will
/// surface the real blocker if the surface is still unresolved.
fn ensure_ffi_surface(root: &Path, dir: &Path) {
    let Ok(text) = std::fs::read_to_string(dir.join("sky.toml")) else {
        return;
    };
    let (declares_go, declares_sky) = declares_real_deps(&text);
    if !declares_go && !declares_sky {
        return;
    }
    // Present iff sky-ffi/ holds at least one generated kernel.json.
    let ffi_present = std::fs::read_dir(dir.join("sky-ffi"))
        .map(|rd| {
            rd.filter_map(|e| e.ok()).any(|e| {
                e.path()
                    .file_name()
                    .and_then(|n| n.to_str())
                    .is_some_and(|n| n.ends_with(".kernel.json"))
            })
        })
        .unwrap_or(false);
    let skydeps_present = dir.join(".skydeps").is_dir();
    let need = (declares_go && !ffi_present) || (declares_sky && !skydeps_present);
    if need {
        // Non-fatal: a failed install leaves the surface absent and the build
        // reports the concrete blocker.
        let _ = project::ffi_install(dir, root);
    }
}

/// Does `sky.toml` declare a REAL dependency (a non-comment `"pkg" = "..."`
/// entry) under `["go.dependencies"]` / `[dependencies]`? Returns `(go, sky)`.
/// Mirrors `project`'s section parser so a fresh-init project's COMMENTED
/// section headers never count as declared deps.
fn declares_real_deps(toml_text: &str) -> (bool, bool) {
    let (mut in_go, mut in_sky, mut go, mut sky) = (false, false, false, false);
    for raw in toml_text.lines() {
        let l = raw.trim();
        if l.starts_with('[') && l.ends_with(']') {
            in_go = l == "[\"go.dependencies\"]";
            in_sky = l == "[dependencies]";
            continue;
        }
        if l.is_empty() || l.starts_with('#') {
            continue;
        }
        if l.contains('=') {
            go |= in_go;
            sky |= in_sky;
        }
    }
    (go, sky)
}

// ---- CLI oracle compare (stdout) ----------------------------------------

/// Run the pre-built ORACLE binary at `<dir>/sky-out/app`, feed `stdin`, and
/// return its RAW stdout (un-normalised). `None` when no oracle binary exists
/// (e.g. a fresh CI checkout — the golden gate is deliberately oracle-free) or
/// the run failed. Used both by the `--run` oracle match and the `--bless`
/// capture (a golden is only written when rust == oracle here).
fn run_oracle_stdout(dir: &Path, stdin: Option<String>) -> Option<String> {
    let app = dir.join("sky-out").join("app");
    if !app.exists() {
        return None;
    }
    let mut cmd = Command::new(&app);
    cmd.current_dir(dir.join("sky-out"))
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    let mut child = cmd.spawn().ok()?;
    if let Some(data) = &stdin {
        if let Some(mut si) = child.stdin.take() {
            let _ = si.write_all(data.as_bytes());
        }
    } else {
        drop(child.stdin.take());
    }
    let out = wait_bounded(&mut child, Duration::from_secs(20))?;
    Some(String::from_utf8_lossy(&out.stdout).to_string())
}

/// Run the RUST binary at `<out_dir>/app` (the `sky-out-rust/` build), feed
/// `stdin`, return its RAW stdout. Used for the `--bless` double-capture guard
/// (run twice; refuse to bless if the two normalised captures differ).
fn run_rust_stdout(out_dir: &Path, stdin: Option<String>) -> Option<String> {
    let app = out_dir.join("app");
    if !app.exists() {
        return None;
    }
    let mut cmd = Command::new(&app);
    cmd.current_dir(out_dir)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    let mut child = cmd.spawn().ok()?;
    if let Some(data) = &stdin {
        if let Some(mut si) = child.stdin.take() {
            let _ = si.write_all(data.as_bytes());
        }
    } else {
        drop(child.stdin.take());
    }
    let out = wait_bounded(&mut child, Duration::from_secs(20))?;
    Some(String::from_utf8_lossy(&out.stdout).to_string())
}

// ---- server verification (Live / Http) ----------------------------------

/// Start rust + oracle server binaries (sequentially, same spare port), fetch
/// `/` from each, normalise, compare. Returns (run_ok, matched, blocker).
fn verify_server(
    dir: &Path,
    out_dir: &Path,
    name: &str,
    shape: Shape,
    verbose: bool,
) -> (bool, Option<bool>, String) {
    let port = free_port().unwrap_or(0);
    let rust_app = out_dir.join("app");
    let rust = serve_and_fetch(&rust_app, out_dir, port, shape, name);
    if verbose {
        eprintln!(
            "  [{name}] rust: started={} port={:?}",
            rust.started, rust.port
        );
    }
    if !rust.started {
        return (false, None, truncate(&rust.note, 60));
    }

    let oracle_app = dir.join("sky-out").join("app");
    if !oracle_app.exists() {
        // rust served but no oracle to compare against.
        return (true, None, "no oracle binary".into());
    }
    let oracle = serve_and_fetch(&oracle_app, &dir.join("sky-out"), port, shape, name);
    if verbose {
        eprintln!(
            "  [{name}] oracle: started={} port={:?}",
            oracle.started, oracle.port
        );
    }
    if !oracle.started {
        return (
            true,
            None,
            format!("oracle failed: {}", truncate(&oracle.note, 40)),
        );
    }

    let m = normalise_html(&rust.body) == normalise_html(&oracle.body);
    let note = if m {
        String::new()
    } else {
        "page != oracle".into()
    };
    (true, Some(m), note)
}

struct ServerRun {
    started: bool,
    port: Option<u16>,
    body: String,
    note: String,
}

/// Spawn a server binary, wait for its "listening" line, curl `/`, then ALWAYS
/// kill it. Watchdog-bounded; the child is killed on every exit path.
fn serve_and_fetch(app: &Path, cwd: &Path, spare: u16, _shape: Shape, name: &str) -> ServerRun {
    let mut cmd = Command::new(app);
    cmd.current_dir(cwd)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .env("SKY_LIVE_PORT", spare.to_string())
        .env("PORT", spare.to_string())
        .env("SKY_LIVE_STORE", "memory")
        .env("SKY_CONSOLE_EMBED", "off")
        .env("SKY_DEV_BANNER", "off")
        .env("SKY_LIVE_BANNER", "off")
        .env("ENV", "dev");
    // Per-example runtime env some servers require to boot at all. Name-scoped
    // so only the named example sees extra vars — every other example spawns
    // byte-identically to before. 36-composite-server refuses to start unless
    // SKY_AUTH_TOKEN_SECRET is >= 32 bytes (a hard runServer guard), and reads
    // its port from SKY_COMPOSITE_PORT rather than SKY_LIVE_PORT.
    for (k, v) in extra_server_env(name, spare) {
        cmd.env(k, v);
    }
    let mut child = match cmd.spawn() {
        Ok(c) => c,
        Err(e) => {
            return ServerRun {
                started: false,
                port: None,
                body: String::new(),
                note: format!("spawn: {e}"),
            }
        }
    };

    // Read stdout lines on a thread; report the first parsed listening port.
    // The tx drops when stdout hits EOF (process exited) → recv returns
    // Disconnected, so a server that dies on start is detected without waiting.
    let (tx, rx) = mpsc::channel::<u16>();
    if let Some(stdout) = child.stdout.take() {
        std::thread::spawn(move || {
            let reader = BufReader::new(stdout);
            for line in reader.lines().map_while(Result::ok) {
                // Sky.Live/Http print "listening on …:PORT"; a raw net/http FFI
                // server (05-mux-server) prints "Server starting on port PORT".
                // Accept either and lift the port from the last number/`:PORT`.
                let low = line.to_lowercase();
                if low.contains("listening") || low.contains("starting on port") {
                    if let Some(p) = last_colon_number(&line).or_else(|| last_number(&line)) {
                        let _ = tx.send(p);
                    }
                }
            }
        });
    }
    // Drain stderr on a thread so a panic reason is available for the note.
    let (etx, erx) = mpsc::channel::<String>();
    if let Some(stderr) = child.stderr.take() {
        std::thread::spawn(move || {
            let mut buf = String::new();
            let reader = BufReader::new(stderr);
            for line in reader.lines().map_while(Result::ok) {
                buf.push_str(&line);
                buf.push('\n');
            }
            let _ = etx.send(buf);
        });
    }

    // Wait for the listening line, or fall back to the spare port on timeout.
    // On Disconnected (stdout closed) the process already exited — bail fast.
    let port = match rx.recv_timeout(Duration::from_secs(8)) {
        Ok(p) => Some(p),
        Err(mpsc::RecvTimeoutError::Timeout) => Some(spare),
        Err(mpsc::RecvTimeoutError::Disconnected) => None,
    };

    // Poll curl until the page responds (or give up).
    let mut body = String::new();
    if let Some(port) = port {
        let deadline = Instant::now() + Duration::from_secs(6);
        while Instant::now() < deadline {
            if let Some(b) = curl_get(port) {
                if !b.trim().is_empty() {
                    body = b;
                    break;
                }
            }
            std::thread::sleep(Duration::from_millis(150));
        }
    }

    // ALWAYS kill + reap.
    let _ = child.kill();
    let _ = child.wait();

    let started = !body.is_empty();
    let note = if started {
        String::new()
    } else {
        let stderr = erx
            .recv_timeout(Duration::from_millis(500))
            .unwrap_or_default();
        panic_reason(&stderr).unwrap_or_else(|| match port {
            Some(p) => format!("no response on :{p}"),
            None => "server exited on start".into(),
        })
    };
    ServerRun {
        started,
        port,
        body,
        note,
    }
}

/// Extra runtime env a specific example needs to boot. Empty for every example
/// but the ones listed — keeps the default spawn path byte-identical.
fn extra_server_env(name: &str, spare: u16) -> Vec<(String, String)> {
    match name {
        "36-composite-server" => vec![
            // >= 32 bytes: runServer hard-fails and exits otherwise.
            (
                "SKY_AUTH_TOKEN_SECRET".into(),
                "gate-fixture-secret-0123456789abcdef".into(),
            ),
            // This server reads its port from SKY_COMPOSITE_PORT, not SKY_LIVE_PORT.
            ("SKY_COMPOSITE_PORT".into(), spare.to_string()),
        ],
        _ => Vec::new(),
    }
}

fn curl_get(port: u16) -> Option<String> {
    let url = format!("http://127.0.0.1:{port}/");
    let out = Command::new("curl")
        .arg("-s")
        .arg("--max-time")
        .arg("4")
        .arg(&url)
        .output()
        .ok()?;
    if out.status.success() {
        Some(String::from_utf8_lossy(&out.stdout).to_string())
    } else {
        None
    }
}

/// Grab a free TCP port by binding :0 and reading the assigned port.
fn free_port() -> Option<u16> {
    let l = TcpListener::bind("127.0.0.1:0").ok()?;
    l.local_addr().ok().map(|a| a.port())
}

/// Parse the trailing `:<digits>` of a "listening on …:PORT" line.
fn last_colon_number(s: &str) -> Option<u16> {
    let idx = s.rfind(':')?;
    let digits: String = s[idx + 1..]
        .chars()
        .take_while(|c| c.is_ascii_digit())
        .collect();
    digits.parse().ok()
}

/// The last run of ASCII digits anywhere in the line ("… on port 8000" → 8000).
fn last_number(s: &str) -> Option<u16> {
    let mut best: Option<u16> = None;
    let mut cur = String::new();
    for c in s.chars() {
        if c.is_ascii_digit() {
            cur.push(c);
        } else if !cur.is_empty() {
            best = cur.parse().ok().or(best);
            cur.clear();
        }
    }
    if !cur.is_empty() {
        best = cur.parse().ok().or(best);
    }
    best
}

// ---- TUI verification (pty, no-panic) ------------------------------------

/// Run the TUI binary under a pty (`script`), feed a quit key, and assert it
/// exits without a Go panic. Frame-diffing is not attempted (pty capture is
/// unreliable across terminals) — RUN reports "no-panic".
fn verify_tui(out_dir: &Path, _name: &str) -> (bool, String) {
    let app = out_dir.join("app");
    if !app.exists() {
        return (false, "no binary".into());
    }
    // macOS: `script -q /dev/null <cmd>` runs cmd in a pty; stdin is forwarded.
    // `wait_bounded` (below) bounds a wedged TUI — no external `timeout` wrapper
    // (absent on macOS, GNU coreutils only), which would also make this spawn a
    // redundant double-bound.
    let mut cmd = Command::new("script");
    cmd.arg("-q")
        .arg("/dev/null")
        .arg("./app")
        .current_dir(out_dir)
        .env("TERM", "xterm")
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    let mut child = match cmd.spawn() {
        Ok(c) => c,
        Err(e) => return (false, format!("spawn: {e}")),
    };
    if let Some(mut si) = child.stdin.take() {
        // 'q' then Ctrl-C as a fallback quit for apps that ignore 'q'.
        let _ = si.write_all(b"q\n\x03");
    }
    let out = match wait_bounded(&mut child, Duration::from_secs(10)) {
        Some(o) => o,
        None => return (false, "tui hung".into()),
    };
    let combined = format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    if has_panic(&combined) {
        return (false, "panic on start".into());
    }
    (true, String::new())
}

/// Extract a short reason from a Sky runtime panic line, if present.
fn panic_reason(stderr: &str) -> Option<String> {
    let line = stderr
        .lines()
        .find(|l| l.contains("Sky panic:") || l.contains("panicKind="))?;
    // prefer the `panicKind=<Kind>` token; else the text after "Sky panic:".
    if let Some(pos) = line.find("panicKind=") {
        let kind: String = line[pos + "panicKind=".len()..]
            .chars()
            .take_while(|c| !c.is_whitespace())
            .collect();
        return Some(format!("panic on start: {kind}"));
    }
    let after = line.split("Sky panic:").nth(1)?.trim();
    Some(format!("panic on start: {}", truncate(after, 40)))
}

fn has_panic(s: &str) -> bool {
    s.contains("panic:")
        || s.contains("goroutine ")
        || s.contains("runtime error:")
        || s.contains("[signal SIGSEGV")
}

// ---- process helper ------------------------------------------------------

/// Wait for a child up to `dur`. Returns its output ONLY when the child exited
/// on its own within the bound; `None` when it had to be killed on overrun, or
/// when waiting on it failed.
///
/// A TIMEOUT IS NOT A SUCCESSFUL RUN. This function previously returned
/// `Some(Output { status: ExitStatus::default(), .. })` on *every* path —
/// including the kill-on-overrun path — and `ExitStatus::default()` is
/// `success() == true` on Unix. Three consequences, all of them a gate that
/// could not fail:
///   * a wedged child was byte-for-byte indistinguishable from a clean exit 0;
///   * `verify_tui`'s `None => (false, "tui hung")` arm was unreachable dead
///     code, so a TUI that never exited passed the no-panic check;
///   * a hung CLI produced an EMPTY capture that `bless_goldens` then wrote as
///     a one-byte golden (see the empty-capture refusal there).
/// The status is now the child's REAL `ExitStatus` rather than a fabricated one.
fn wait_bounded(child: &mut Child, dur: Duration) -> Option<std::process::Output> {
    let deadline = Instant::now() + dur;
    let status = loop {
        match child.try_wait() {
            Ok(Some(st)) => break st,
            Ok(None) => {
                if Instant::now() >= deadline {
                    // Overran the bound: kill it and report a timeout. The
                    // partial output of a killed child is not a run result.
                    let _ = child.kill();
                    let _ = child.wait();
                    return None;
                }
                std::thread::sleep(Duration::from_millis(50));
            }
            Err(_) => return None,
        }
    };
    // Drain stdout/stderr after exit.
    let mut stdout = Vec::new();
    let mut stderr = Vec::new();
    if let Some(mut o) = child.stdout.take() {
        let _ = std::io::Read::read_to_end(&mut o, &mut stdout);
    }
    if let Some(mut e) = child.stderr.take() {
        let _ = std::io::Read::read_to_end(&mut e, &mut stderr);
    }
    Some(std::process::Output {
        status,
        stdout,
        stderr,
    })
}

// ---- normalisation -------------------------------------------------------

/// Strip volatile stdout bits (leading ISO-8601 log timestamps + wall-clock
/// `HH:MM:SS` tokens, which differ when the second ticks between the rust run
/// and the oracle run — e.g. `Current time: 12:21:10`).
fn normalise_stdout(s: &str) -> String {
    let deiso = s
        .lines()
        .map(strip_leading_timestamp)
        .collect::<Vec<_>>()
        .join("\n");
    strip_epoch_millis(&strip_uuids(&strip_clock_times(&deiso)))
        .trim()
        .to_string()
}

/// Replace a bare Unix epoch-millis integer (a maximal run of 13–19 ASCII
/// digits, not part of a longer alphanumeric token) with `<ts>`. `Time.now` /
/// `Time.unixMillis` output is volatile like a UUID or clock time — the compare
/// should assert structure, not the wall-clock instant. 13 digits covers
/// milliseconds through ~year 2286; the upper bound catches micro/nanos.
/// Ordinary business integers in the corpus are ≤ 6 digits (cents, counts,
/// ids), well under the floor, so this never masks meaningful values.
fn strip_epoch_millis(s: &str) -> String {
    let chars: Vec<char> = s.chars().collect();
    let mut out = String::with_capacity(s.len());
    let mut i = 0;
    while i < chars.len() {
        if chars[i].is_ascii_digit() {
            let start = i;
            while i < chars.len() && chars[i].is_ascii_digit() {
                i += 1;
            }
            let len = i - start;
            // Only a run bounded by non-digits on both sides (a standalone
            // number) and in the epoch-millis..nanos width band collapses.
            let prev_alnum = start > 0 && chars[start - 1].is_ascii_alphanumeric();
            let next_alnum = i < chars.len() && chars[i].is_ascii_alphanumeric();
            if (13..=19).contains(&len) && !prev_alnum && !next_alnum {
                out.push_str("<ts>");
            } else {
                out.extend(&chars[start..i]);
            }
        } else {
            out.push(chars[i]);
            i += 1;
        }
    }
    out
}

/// Replace any RFC-4122 UUID (`8-4-4-4-12` hex, hyphenated) with `<uuid>`.
/// A freshly generated UUID (`Uuid.newString`, `Uuid.v4`) is volatile output
/// like a timestamp — the structure is what a stdout/page compare should assert,
/// not the random value.
fn strip_uuids(s: &str) -> String {
    let chars: Vec<char> = s.chars().collect();
    let mut out = String::with_capacity(s.len());
    let mut i = 0;
    while i < chars.len() {
        if i + 36 <= chars.len() && looks_like_uuid(&chars[i..i + 36]) {
            out.push_str("<uuid>");
            i += 36;
        } else {
            out.push(chars[i]);
            i += 1;
        }
    }
    out
}

fn looks_like_uuid(c: &[char]) -> bool {
    if c.len() != 36 {
        return false;
    }
    for (i, ch) in c.iter().enumerate() {
        let ok = match i {
            8 | 13 | 18 | 23 => *ch == '-',
            _ => ch.is_ascii_hexdigit(),
        };
        if !ok {
            return false;
        }
    }
    true
}

/// Replace any `HH:MM:SS` run with `<time>` (structural; two-digit fields).
fn strip_clock_times(s: &str) -> String {
    let chars: Vec<char> = s.chars().collect();
    let mut out = String::with_capacity(s.len());
    let mut i = 0;
    while i < chars.len() {
        if i + 8 <= chars.len() && looks_like_clock(&chars[i..i + 8]) {
            out.push_str("<time>");
            i += 8;
        } else {
            out.push(chars[i]);
            i += 1;
        }
    }
    out
}

fn looks_like_clock(c: &[char]) -> bool {
    c.len() == 8
        && c[0].is_ascii_digit()
        && c[1].is_ascii_digit()
        && c[2] == ':'
        && c[3].is_ascii_digit()
        && c[4].is_ascii_digit()
        && c[5] == ':'
        && c[6].is_ascii_digit()
        && c[7].is_ascii_digit()
}

/// Normalise an HTML page for a shape-stable compare: drop runtime-assigned
/// ids, per-render tokens, and injected scripts that legitimately vary.
fn normalise_html(s: &str) -> String {
    let mut out = strip_between(s, "<script", "</script>");
    out = strip_between(&out, "<style", "</style>");
    out = strip_attr(&out, "sky-id");
    out = strip_attr_prefix(&out, "data-sky-");
    out = strip_iso_timestamps(&out);
    out = strip_clock_times(&out);
    out = strip_uuids(&out);
    out = strip_csrf(&out);
    // collapse runs of whitespace so cosmetic reflow doesn't diff.
    out.split_whitespace().collect::<Vec<_>>().join(" ")
}

/// Remove `attr="…"` occurrences (name exact).
fn strip_attr(s: &str, attr: &str) -> String {
    let needle = format!("{attr}=\"");
    strip_attr_needle(s, &needle)
}

/// Remove `prefix*="…"` occurrences (attributes whose name starts with prefix).
/// UTF-8 safe: only ever slices at byte offsets returned by `find`/`char_indices`.
fn strip_attr_prefix(s: &str, prefix: &str) -> String {
    let mut out = String::with_capacity(s.len());
    let mut i = 0;
    while i < s.len() {
        if s[i..].starts_with(prefix) {
            // scan to `="` then to the closing quote.
            if let Some(eq) = s[i..].find("=\"") {
                let after = i + eq + 2;
                if let Some(close) = s[after..].find('"') {
                    // ensure the span between prefix and `=` is an attr name (no spaces/>)
                    let name = &s[i..i + eq];
                    if !name.contains(|c: char| c.is_whitespace() || c == '>' || c == '<') {
                        i = after + close + 1;
                        continue;
                    }
                }
            }
        }
        // advance by one full char (never split a multibyte scalar).
        let ch = s[i..].chars().next().unwrap();
        out.push(ch);
        i += ch.len_utf8();
    }
    out
}

fn strip_attr_needle(s: &str, needle: &str) -> String {
    let mut out = String::with_capacity(s.len());
    let mut rest = s;
    while let Some(pos) = rest.find(needle) {
        out.push_str(&rest[..pos]);
        let after = &rest[pos + needle.len()..];
        match after.find('"') {
            Some(q) => rest = &after[q + 1..],
            None => {
                rest = "";
                break;
            }
        }
    }
    out.push_str(rest);
    out
}

/// Remove everything between `open` (up to its `>`) and `close`, inclusive.
fn strip_between(s: &str, open: &str, close: &str) -> String {
    let mut out = String::with_capacity(s.len());
    let mut rest = s;
    while let Some(pos) = rest.find(open) {
        out.push_str(&rest[..pos]);
        let after = &rest[pos..];
        match after.find(close) {
            Some(end) => rest = &after[end + close.len()..],
            None => {
                rest = "";
                break;
            }
        }
    }
    out.push_str(rest);
    out
}

/// Blank ISO-8601/RFC3339 timestamps anywhere in the text.
fn strip_iso_timestamps(s: &str) -> String {
    // structural: replace any `dddd-dd-ddTdd:dd:dd…Z`-shaped run with <ts>.
    let mut out = String::with_capacity(s.len());
    let chars: Vec<char> = s.chars().collect();
    let mut i = 0;
    while i < chars.len() {
        if i + 20 <= chars.len() && looks_like_iso(&chars[i..]) {
            out.push_str("<ts>");
            // advance past the timestamp (until the trailing 'Z').
            let mut j = i;
            while j < chars.len() && chars[j] != 'Z' {
                j += 1;
            }
            i = j + 1;
        } else {
            out.push(chars[i]);
            i += 1;
        }
    }
    out
}

fn looks_like_iso(c: &[char]) -> bool {
    c.len() >= 20
        && c[0..4].iter().all(|x| x.is_ascii_digit())
        && c[4] == '-'
        && c[5].is_ascii_digit()
        && c[6].is_ascii_digit()
        && c[7] == '-'
        && c[10] == 'T'
        && c[13] == ':'
        && c[16] == ':'
}

/// Blank csrf token values (`name="_csrf" value="…"` / `csrf=…`).
fn strip_csrf(s: &str) -> String {
    let mut out = strip_attr_needle(s, "_csrf\" value=\"");
    out = strip_attr_needle(&out, "csrf-token\" content=\"");
    out
}

fn strip_leading_timestamp(line: &str) -> String {
    let (head, rest) = match line.split_once(' ') {
        Some(p) => p,
        None => return line.to_string(),
    };
    if is_iso_timestamp(head) {
        format!("<ts> {rest}")
    } else {
        line.to_string()
    }
}

fn is_iso_timestamp(tok: &str) -> bool {
    let b = tok.as_bytes();
    tok.len() >= 20
        && tok.ends_with('Z')
        && b.get(4) == Some(&b'-')
        && b.get(7) == Some(&b'-')
        && b.get(10) == Some(&b'T')
        && b.get(13) == Some(&b':')
        && b.get(16) == Some(&b':')
}

// ---- misc ---------------------------------------------------------------

fn first_go_error(stderr: &str) -> String {
    stderr
        .lines()
        .find(|l| l.contains("error") || l.contains(".go:"))
        .unwrap_or("go build failed")
        .trim()
        .chars()
        .take(70)
        .collect()
}

fn truncate(s: &str, n: usize) -> String {
    let one = s.lines().next().unwrap_or("").trim();
    one.chars().take(n).collect()
}

fn indent(s: &str, n: usize) -> String {
    let pad = " ".repeat(n);
    s.lines()
        .map(|l| format!("{pad}{l}"))
        .collect::<Vec<_>>()
        .join("\n")
}

fn collect_sky(dir: &Path, out: &mut Vec<std::path::PathBuf>) {
    let mut entries: Vec<std::path::PathBuf> = match std::fs::read_dir(dir) {
        Ok(rd) => rd.filter_map(|e| e.ok().map(|e| e.path())).collect(),
        Err(_) => return,
    };
    entries.sort();
    for path in entries {
        let is_gen = path.components().any(|c| {
            matches!(
                c.as_os_str().to_str(),
                Some("sky-out") | Some("sky-out-rust") | Some(".skycache") | Some(".skydeps")
            )
        });
        if is_gen {
            continue;
        }
        if path.is_dir() {
            collect_sky(&path, out);
        } else if path.extension().and_then(|e| e.to_str()) == Some("sky") {
            out.push(path);
        }
    }
}

fn print_table(rows: &[Row]) {
    let w = rows.iter().map(|r| r.name.len()).max().unwrap_or(8).max(8);
    println!(
        "{:<w$}  {:>7}  {:>7}  {:>5}  {:>8}  {:>6}  BLOCKER",
        "EXAMPLE",
        "SHAPE",
        "EMITTED",
        "BUILD",
        "RUN",
        "MATCH",
        w = w
    );
    println!("{}", "-".repeat(w + 60));
    let (mut nb, mut nr, mut nm, mut denom, mut mdenom) = (0, 0, 0, 0, 0);
    for r in rows {
        let build = if r.build_ok {
            "ok"
        } else if r.emitted {
            "FAIL"
        } else {
            "-"
        };
        if r.build_ok {
            nb += 1;
        }
        if r.shape != Shape::Ffi {
            denom += 1;
        }
        let run = match r.run_ok {
            Some(true) => {
                nr += 1;
                if r.run_kind == "no-panic" {
                    "no-panic"
                } else {
                    "ok"
                }
            }
            Some(false) => "FAIL",
            None => "-",
        };
        let matchc = match r.matched {
            Some(true) => {
                nm += 1;
                mdenom += 1;
                "match"
            }
            Some(false) => {
                mdenom += 1;
                "DIFF"
            }
            None => "-",
        };
        let emitted = if r.emitted { "yes" } else { "no" };
        println!(
            "{:<w$}  {:>7}  {:>7}  {:>5}  {:>8}  {:>6}  {}",
            r.name,
            r.shape.label(),
            emitted,
            build,
            run,
            matchc,
            r.blocker,
            w = w
        );
    }
    println!("{}", "-".repeat(w + 60));
    let n = rows.len();
    println!(
        "TOTALS  |  build {nb}/{denom} (non-ffi)  |  run-ok {nr}/{denom}  |  run+match {nm}/{mdenom}  |  examples {n}"
    );
}

/// Gate: hello-world must build + run, AND no non-FFI example may emit yet fail
/// `go build`. Non-zero on regression.
fn gate_result(rows: &[Row]) -> i32 {
    // Hard floor: hello-world must build (+ run when verified).
    let hello_bad = matches!(
        rows.iter().find(|r| r.name == "01-hello-world"),
        Some(r) if !(r.build_ok && r.run_ok != Some(false))
    );
    if hello_bad {
        return 1;
    }

    // A non-FFI example that EMITTED but fails `go build` is a check≢build
    // regression — `sky check ≡ sky build` is a hard non-negotiable ("if it
    // compiles it works"). The gate MUST fail on it rather than let it silently
    // drop out of the `build N/N` count.
    //
    // Historically the gate asserted ONLY hello-world, so 26-ui-showcase (whose
    // `Input.multiline` config record has a func field — the func-field-record →
    // concrete-struct check≢build hole) failed `go build` yet the gate still
    // exited 0. Its red build never failed CI, so the regression rotted
    // undetected. Now EVERY emitting non-FFI example's `go build` is a hard gate
    // condition — this covers 26-ui-showcase and 47-func-field-record (the
    // regression lock for that class) going forward, with no silent exclusion.
    //
    // `Shape::Ffi` (native macOS GUI via cgo — e.g. 11-fyne-stopwatch) is the
    // documented build-only ceiling and is excluded, matching the `denom`
    // accounting in `print_table`. An example that does NOT emit (a genuine FFI
    // surface not yet expressible) is likewise not a `go build` regression.
    //
    // ONE further distinction: a NATIVE (cgo) LINK failure is NOT a check≢build
    // hole. The emitted Go is valid and the Go COMPILER accepts it; only the
    // final native link stage fails — e.g. a Sky.Webview example (macOS-cgo-only,
    // WKWebView) whose WebKit link fails on a particular runner
    // (`clang++: error: linker command failed`). That is an ENVIRONMENT ceiling
    // (the same category Webview/Fyne already occupy as "build-only"), not the
    // Sky compiler emitting Go that `go build` rejects. The gate must still
    // hard-fail on a GO-COMPILER rejection (`./main.go:…`, `cannot use…`) or a
    // GO-LINKER missing symbol (`undefined: rt.X` — the ABI-guard class, a real
    // hole), so `is_native_link_failure` matches ONLY the C/native toolchain and
    // never those. Native-link ceilings are logged (never silently dropped) so a
    // regression there stays visible without falsely reding a compiler gate.
    let (link_ceilings, build_regressions): (Vec<&str>, Vec<&str>) = rows
        .iter()
        .filter(|r| r.emitted && !r.build_ok && r.shape != Shape::Ffi)
        .map(|r| r.name.as_str())
        .partition(|name| {
            rows.iter()
                .find(|r| r.name == *name)
                .is_some_and(|r| is_native_link_failure(&r.blocker))
        });
    if !link_ceilings.is_empty() {
        eprintln!(
            "BUILD-RUN: {} example(s) hit a native cgo-link ceiling (emitted Go is \
             valid + compiles; only the native link failed — environment, not a \
             compiler hole): {}",
            link_ceilings.len(),
            link_ceilings.join(", ")
        );
    }
    if !build_regressions.is_empty() {
        eprintln!(
            "BUILD-RUN GATE: FAIL — {} non-FFI example(s) emitted but failed `go build` \
             at the Go-compile/link stage (check≢build hole): {}",
            build_regressions.len(),
            build_regressions.join(", ")
        );
        return 1;
    }

    // ---- run + oracle-match verdict ------------------------------------
    //
    // Until now this function consulted `run_ok` for `01-hello-world` ONLY, and
    // never consulted `matched` at all. Both fields were computed, stored on
    // every Row, printed in the table — and then ignored by the verdict. So the
    // three CI steps
    //     xtask build-run --shape live --run
    //     xtask build-run --shape http --run
    //     xtask build-run --shape tui  --run
    // could not fail: an app that panicked on boot, a server that never came
    // up, and a page that differed from the oracle all printed their row and
    // exited 0. `--shape cli --run` was covered only indirectly, via the
    // separate golden gate.
    //
    // The acceptable states are enumerated rather than assumed, because a gate
    // that fires on a known-acceptable state is as bad as one that never fires:
    //
    //   run_ok == None       ACCEPTABLE. The run was not attempted: no `--run`
    //                        flag, or `go build` failed (the block that sets
    //                        run_ok is guarded on `rep.go_build_ok`, so the
    //                        native-link ceilings partitioned off above never
    //                        reach here), or the shape is Webview/Ffi, whose
    //                        documented ceiling is build-only (a macOS GUI with
    //                        a blocking event loop cannot be run headless).
    //   run_ok == Some(true) ACCEPTABLE. It ran.
    //   run_ok == Some(false) FAIL. The binary was built and the gate tried to
    //                        run it and it did not run — a boot panic, a server
    //                        that never printed its listening line, a TUI that
    //                        hung (now reachable: see `wait_bounded`).
    //
    //   matched == None      ACCEPTABLE. No comparison was possible or intended:
    //                        run_kind "no-panic" (the NONDETERMINISTIC_OUTPUT
    //                        CLI set and every Tui row, where stdout genuinely
    //                        cannot be pinned), or no oracle binary exists (a
    //                        fresh CI checkout is deliberately oracle-free), or
    //                        the ORACLE failed to start (an oracle-side
    //                        environment problem is not a rust regression).
    //   matched == Some(true)  ACCEPTABLE. Rust output == oracle output.
    //   matched == Some(false) FAIL. Rust and the oracle both ran and produced
    //                        different output. That is a runtime-correctness
    //                        divergence with no benign reading.
    let run_failures: Vec<&str> = rows
        .iter()
        .filter(|r| r.run_ok == Some(false))
        .map(|r| r.name.as_str())
        .collect();
    let mismatches: Vec<&str> = rows
        .iter()
        .filter(|r| r.matched == Some(false))
        .map(|r| r.name.as_str())
        .collect();

    let mut failed = false;
    if !run_failures.is_empty() {
        failed = true;
        eprintln!(
            "BUILD-RUN GATE: FAIL — {} example(s) built but did NOT run: {}",
            run_failures.len(),
            run_failures
                .iter()
                .map(|n| {
                    let why = rows
                        .iter()
                        .find(|r| r.name == *n)
                        .map(|r| r.blocker.as_str())
                        .unwrap_or("");
                    if why.is_empty() {
                        (*n).to_string()
                    } else {
                        format!("{n} ({why})")
                    }
                })
                .collect::<Vec<_>>()
                .join(", ")
        );
    }
    if !mismatches.is_empty() {
        failed = true;
        eprintln!(
            "BUILD-RUN GATE: FAIL — {} example(s) whose output differs from the ORACLE \
             (runtime-correctness divergence): {}",
            mismatches.len(),
            mismatches.join(", ")
        );
    }
    if failed {
        return 1;
    }
    0
}

// ---- golden gate (runtime correctness) -----------------------------------
//
// Directory of committed per-example goldens (one file per example → readable
// PR diffs). Each holds the NORMALISED stdout (via `normalise_stdout`) of an
// oracle-verified run.
const GOLDEN_DIR_REL: &str = "rust/crates/xtask/golden";

fn golden_dir(root: &Path) -> std::path::PathBuf {
    root.join(GOLDEN_DIR_REL)
}

fn golden_file(root: &Path, name: &str) -> std::path::PathBuf {
    golden_dir(root).join(format!("{name}.stdout"))
}

/// Load a committed golden's contents (already normalised at bless time),
/// trimmed for a stable compare. `None` when the file does not exist.
fn load_golden(root: &Path, name: &str) -> Option<String> {
    std::fs::read_to_string(golden_file(root, name))
        .ok()
        .map(|s| s.trim_end().to_string())
}

/// In-scope for the golden gate: a deterministic CLI example. The inherently
/// nondeterministic ones (`NONDETERMINISTIC_OUTPUT`, e.g. 02-go-stdlib) are
/// never golden-compared — they short-circuit to no-panic in the run path.
fn golden_in_scope(row: &Row) -> bool {
    row.shape == Shape::Cli && !NONDETERMINISTIC_OUTPUT.contains(&row.name.as_str())
}

/// Names present in the golden directory (as `<name>.stdout` files).
fn committed_golden_names(root: &Path) -> Vec<String> {
    let mut ns: Vec<String> = std::fs::read_dir(golden_dir(root))
        .map(|rd| {
            rd.filter_map(|e| e.ok())
                .filter_map(|e| e.file_name().to_str().map(String::from))
                .filter_map(|f| f.strip_suffix(".stdout").map(String::from))
                .collect()
        })
        .unwrap_or_default();
    ns.sort();
    ns
}

/// A normalised golden that leaks a machine-specific path / home / hostname is
/// NOT portable — refuse to bless it. Returns the offending marker if any.
fn machine_leak(normalised: &str) -> Option<String> {
    for marker in ["/Users/", "/home/", "$HOME"] {
        if normalised.contains(marker) {
            return Some(marker.to_string());
        }
    }
    if let Ok(host) = std::env::var("HOSTNAME") {
        if !host.trim().is_empty() && normalised.contains(host.trim()) {
            return Some(format!("hostname {host}"));
        }
    }
    if let Ok(out) = Command::new("hostname").output() {
        let host = String::from_utf8_lossy(&out.stdout).trim().to_string();
        if !host.is_empty() && normalised.contains(&host) {
            return Some(format!("hostname {host}"));
        }
    }
    None
}

/// The `--golden` compare gate (CI runtime-correctness): for each in-scope CLI
/// example that emitted+built+ran, normalise its RUST stdout and compare to the
/// committed golden. NEVER runs the oracle — reads only committed goldens.
///
/// Additionally, when `do_verify` (local `--run`) captured an oracle stdout,
/// assert `oracle_normalised == golden` (drift detection) so a stale golden is
/// caught locally rather than in CI.
///
/// `golden_flag` gates the hard failures unique to `--golden` (mismatch,
/// missing-golden); pure `--run` only performs the additive drift check.
fn golden_gate(root: &Path, rows: &[Row], golden_flag: bool, do_verify: bool, subset: bool) -> i32 {
    let scope: Vec<&Row> = rows.iter().filter(|r| golden_in_scope(r)).collect();

    let mut matches: Vec<String> = Vec::new();
    let mut mismatches: Vec<String> = Vec::new();
    let mut missing_golden: Vec<String> = Vec::new();
    let mut not_run: Vec<String> = Vec::new();
    let mut empty: Vec<String> = Vec::new();
    let mut drift: Vec<String> = Vec::new();

    // header
    if golden_flag {
        let w = scope.iter().map(|r| r.name.len()).max().unwrap_or(8).max(8);
        println!("\ngolden gate — CLI runtime-output correctness (normalised stdout vs committed golden)\n");
        println!("{:<w$}  {:>8}  STATUS", "EXAMPLE", "GOLDEN", w = w);
        println!("{}", "-".repeat(w + 24));
        for r in &scope {
            let (has_golden, status) = golden_status(root, r);
            match status {
                GoldenStatus::Match => matches.push(r.name.clone()),
                GoldenStatus::Mismatch => mismatches.push(r.name.clone()),
                GoldenStatus::Missing => missing_golden.push(r.name.clone()),
                GoldenStatus::NotRun => not_run.push(r.name.clone()),
                GoldenStatus::Empty => empty.push(r.name.clone()),
            }
            println!(
                "{:<w$}  {:>8}  {}",
                r.name,
                if has_golden { "yes" } else { "-" },
                status.label(),
                w = w
            );
        }
        println!("{}", "-".repeat(w + 24));
    }

    // drift check (independent of the golden_flag print path): only when we
    // actually ran the oracle (local `--run` with an existing sky-out/app).
    if do_verify {
        for r in &scope {
            if let (Some(oracle_raw), Some(g)) = (&r.oracle_stdout, load_golden(root, &r.name)) {
                if normalise_stdout(oracle_raw) != g {
                    drift.push(r.name.clone());
                }
            }
        }
    }

    // golden files with no emitting example this run (env-tolerant: report only).
    let scope_names: std::collections::HashSet<&str> =
        scope.iter().map(|r| r.name.as_str()).collect();
    let orphans: Vec<String> = committed_golden_names(root)
        .into_iter()
        .filter(|n| !scope_names.contains(n.as_str()))
        .collect();

    // ---- report ----
    if !drift.is_empty() {
        eprintln!(
            "\ngolden gate: DRIFT — {} example(s) whose ORACLE output no longer matches the \
             committed golden (re-bless): {}",
            drift.len(),
            drift.join(", ")
        );
    }
    if !orphans.is_empty() && !subset {
        println!(
            "\ngolden gate: {} committed golden(s) had no emitting example this run \
             (env-tolerant, not gated): {}",
            orphans.len(),
            orphans.join(", ")
        );
    }

    let mut fail = false;
    if golden_flag {
        if !mismatches.is_empty() {
            fail = true;
            eprintln!(
                "\nGOLDEN GATE: FAIL — {} example(s) whose normalised stdout != committed golden \
                 (compiles but computes a different answer): {}",
                mismatches.len(),
                mismatches.join(", ")
            );
        }
        if !missing_golden.is_empty() {
            fail = true;
            eprintln!(
                "\nGOLDEN GATE: FAIL — {} emitting example(s) with NO golden \
                 (new example — bless to lock its runtime output): {}",
                missing_golden.len(),
                missing_golden.join(", ")
            );
        }
        if !not_run.is_empty() {
            fail = true;
            eprintln!(
                "\nGOLDEN GATE: FAIL — {} in-scope CLI example(s) did not build+run so no output \
                 could be compared: {}",
                not_run.len(),
                not_run.join(", ")
            );
        }
        if !empty.is_empty() {
            fail = true;
            eprintln!(
                "\nGOLDEN GATE: FAIL — {} example(s) whose golden and/or run output is EMPTY. \
                 An empty golden matches an empty run, so the pair is green forever while \
                 asserting nothing. Fix the example so it produces output, then re-bless: {}",
                empty.len(),
                empty.join(", ")
            );
        }
    }
    // drift is a hard failure regardless of the golden_flag (it means a stale
    // golden that would silently pass CI).
    if !drift.is_empty() {
        fail = true;
    }

    if golden_flag {
        if fail {
            println!("\nGOLDEN GATE: FAIL");
        } else {
            println!(
                "\nGOLDEN GATE: PASS  ({} CLI example(s) matched their committed golden)",
                matches.len()
            );
        }
    }

    if fail {
        1
    } else {
        0
    }
}

enum GoldenStatus {
    Match,
    Mismatch,
    Missing,
    NotRun,
    /// The committed golden (or the run it is compared against) is empty. An
    /// empty golden matches an empty run, so this pair is green forever while
    /// asserting nothing — it must be reported as a FAILURE, not a match.
    Empty,
}

impl GoldenStatus {
    fn label(&self) -> &'static str {
        match self {
            GoldenStatus::Match => "match",
            GoldenStatus::Mismatch => "MISMATCH",
            GoldenStatus::Missing => "MISSING-GOLDEN",
            GoldenStatus::NotRun => "did-not-run",
            GoldenStatus::Empty => "EMPTY-GOLDEN",
        }
    }
}

/// Classify one in-scope CLI row against its committed golden. Returns
/// `(golden_file_exists, status)`.
fn golden_status(root: &Path, row: &Row) -> (bool, GoldenStatus) {
    let rust = match (&row.rust_stdout, row.run_ok) {
        (Some(s), Some(true)) => s,
        _ => return (load_golden(root, &row.name).is_some(), GoldenStatus::NotRun),
    };
    let rust_norm = normalise_stdout(rust);
    match load_golden(root, &row.name) {
        // Checked BEFORE the equality arm: an empty golden and an empty run are
        // equal, so the `g == rust_norm` arm would call this a match. It is not
        // a match, it is two nothings agreeing.
        Some(g) if g.trim().is_empty() || rust_norm.trim().is_empty() => {
            (true, GoldenStatus::Empty)
        }
        Some(g) if g == rust_norm => (true, GoldenStatus::Match),
        Some(_) => (true, GoldenStatus::Mismatch),
        None => (false, GoldenStatus::Missing),
    }
}

// ---- bless (oracle-verified golden capture) ------------------------------
//
// For each in-scope CLI example: capture RUST twice (double-capture guard),
// run the ORACLE, and write the golden ONLY when
// rust_capture_1 == rust_capture_2 == oracle (all normalised). A missing oracle
// binary → skip+warn (never write an unverified golden). rust != oracle → refuse
// + report (a real runtime bug, not a bless).
fn bless_goldens(root: &Path, rows: &[Row], verbose: bool) -> i32 {
    let scope: Vec<&Row> = rows.iter().filter(|r| golden_in_scope(r)).collect();
    if let Err(e) = std::fs::create_dir_all(golden_dir(root)) {
        eprintln!("bless: cannot create {GOLDEN_DIR_REL}: {e}");
        return 1;
    }

    let mut written: Vec<String> = Vec::new();
    let mut skipped_no_oracle: Vec<String> = Vec::new();
    let mut refused_mismatch: Vec<(String, String)> = Vec::new();
    let mut refused_nondet: Vec<String> = Vec::new();
    let mut refused_leak: Vec<(String, String)> = Vec::new();
    let mut refused_empty: Vec<String> = Vec::new();
    let mut skipped_no_run: Vec<String> = Vec::new();

    println!("\nbless — capturing oracle-verified CLI goldens\n");

    for r in &scope {
        let name = r.name.as_str();
        // rust capture #1 comes from the inline run recorded on the row.
        let cap1 = match (&r.rust_stdout, r.run_ok) {
            (Some(s), Some(true)) => normalise_stdout(s),
            _ => {
                skipped_no_run.push(name.to_string());
                continue;
            }
        };
        // An EMPTY capture is never a golden. `format!("{}\n", cap1.trim_end())`
        // turns an empty capture into `"\n"`, so a run that produced no output
        // at all was committed as a one-byte golden and matched itself green
        // forever after. That is exactly how
        // `rust/crates/xtask/golden/55-store-partial-update.stdout` became 1
        // byte: the example's `sky.toml` was missing the `[database]` section
        // every other `Db.connect` example has, so `Db.connect ()` failed, the
        // whole Task chain short-circuited, and the app printed nothing. The
        // bless refused on no-run, nondeterminism, no-oracle, rust!=oracle and
        // machine-leak — but not on "asserted nothing".
        //
        // This check is deliberately placed BEFORE the oracle comparison: when
        // BOTH rust and oracle produce no output they agree, and agreement on
        // emptiness is not evidence of correctness.
        if cap1.trim().is_empty() {
            refused_empty.push(name.to_string());
            continue;
        }
        // rust capture #2: re-run the rust binary. Nondeterminism → refuse.
        let out_dir = root.join("examples").join(name).join("sky-out-rust");
        let cap2 = match run_rust_stdout(&out_dir, stdin_for(name)) {
            Some(s) => normalise_stdout(&s),
            None => {
                skipped_no_run.push(name.to_string());
                continue;
            }
        };
        if cap1 != cap2 {
            refused_nondet.push(name.to_string());
            continue;
        }
        // oracle capture — the verification anchor.
        let dir = root.join("examples").join(name);
        let oracle = match run_oracle_stdout(&dir, stdin_for(name)) {
            Some(s) => normalise_stdout(&s),
            None => {
                skipped_no_oracle.push(name.to_string());
                continue;
            }
        };
        if cap1 != oracle {
            refused_mismatch.push((
                name.to_string(),
                "rust_normalized != oracle_normalized".into(),
            ));
            continue;
        }
        // machine-specific leak guard.
        if let Some(marker) = machine_leak(&cap1) {
            refused_leak.push((name.to_string(), marker));
            continue;
        }
        // write it (verified: rust==rust==oracle, portable).
        let path = golden_file(root, name);
        let body = format!("{}\n", cap1.trim_end());
        match std::fs::write(&path, &body) {
            Ok(()) => {
                written.push(name.to_string());
                if verbose {
                    println!("  wrote  {name}  ({} bytes, oracle-verified)", body.len());
                }
            }
            Err(e) => {
                eprintln!("  FAILED to write {name}: {e}");
                return 1;
            }
        }
    }

    // ---- summary ----
    println!(
        "bless summary: {} written (oracle-verified)  |  {} skipped (no oracle)  |  \
         {} skipped (no run)  |  {} refused (nondeterministic)  |  {} refused (rust != oracle)  \
         |  {} refused (machine leak)  |  {} refused (empty capture)",
        written.len(),
        skipped_no_oracle.len(),
        skipped_no_run.len(),
        refused_nondet.len(),
        refused_mismatch.len(),
        refused_leak.len(),
        refused_empty.len(),
    );
    if !written.is_empty() {
        println!("  written: {}", written.join(", "));
    }
    if !skipped_no_oracle.is_empty() {
        println!(
            "  skipped (no oracle binary — build it to verify): {}",
            skipped_no_oracle.join(", ")
        );
    }
    if !skipped_no_run.is_empty() {
        println!(
            "  skipped (rust did not build+run): {}",
            skipped_no_run.join(", ")
        );
    }
    if !refused_nondet.is_empty() {
        eprintln!(
            "  refused (rust output nondeterministic across two captures): {}",
            refused_nondet.join(", ")
        );
    }
    if !refused_leak.is_empty() {
        for (n, m) in &refused_leak {
            eprintln!("  refused (machine-specific leak `{m}`): {n}");
        }
    }

    // A rust != oracle mismatch is a REAL runtime bug — fail hard, do not paper
    // over it (a bless must never write an unverified golden).
    if !refused_mismatch.is_empty() {
        eprintln!(
            "\nBLESS: REFUSED — {} example(s) where the RUST output differs from the ORACLE \
             (a real runtime-correctness bug, not a bless):",
            refused_mismatch.len()
        );
        for (n, why) in &refused_mismatch {
            eprintln!("  {n}: {why}");
        }
        return 1;
    }
    if !refused_nondet.is_empty() {
        return 1;
    }
    // An empty capture means the run asserted NOTHING. Blessing it commits a
    // one-byte golden that matches itself forever — the strongest form of a
    // gate that cannot fail. Treated as hard as a rust != oracle mismatch,
    // because in practice it has the same cause: the program did not work.
    if !refused_empty.is_empty() {
        eprintln!(
            "\nBLESS: REFUSED — {} example(s) produced NO output; an empty capture is not a \
             golden (the run asserted nothing). Fix the example so it actually runs, then \
             re-bless: {}",
            refused_empty.len(),
            refused_empty.join(", ")
        );
        return 1;
    }
    0
}

/// A NATIVE (cgo / C-toolchain) LINK failure — the emitted Go is valid and the
/// Go compiler accepted it; only the native link stage failed (e.g. a Sky.Webview
/// example's WebKit link on a runner missing the framework). This is an
/// environment build-only ceiling, NOT a `sky check ≢ sky build` compiler hole.
///
/// Deliberately EXCLUDES the two real-hole classes that also surface at "build"
/// time: a Go-COMPILER rejection (carries a `./main.go:LINE:COL:` diagnostic or
/// `cannot use`) and a Go-LINKER missing symbol (`undefined: rt.X` — the
/// ABI-guard class). Both must still hard-fail.
fn is_native_link_failure(blocker: &str) -> bool {
    let native = blocker.contains("linker command failed")
        || blocker.contains("clang: error")
        || blocker.contains("clang++: error")
        || blocker.contains("ld: ");
    let go_level = blocker.contains(".go:")
        || blocker.contains("undefined:")
        || blocker.contains("cannot use");
    native && !go_level
}

#[cfg(test)]
mod golden_gate_tests {
    use super::*;

    #[test]
    fn std_app_entry_classifies_as_live_not_cli() {
        // A migrated Std.App app's main is `App.run appDef`; the build-run gate
        // builds the DEFAULT target (web = Sky.Live), so it MUST classify as Live
        // — not fall through to the Cli default, which would run a web server as a
        // line-oriented CLI and hang. Regression for the entire Std.App example
        // migration (all 20 apps use `App.run`).
        assert_eq!(shape_of_segment("App.run appDef"), Some(Shape::Live));
        assert_eq!(shape_of_segment("    App.run appDef\n"), Some(Shape::Live));
        // The explicit backends still classify as before — no regression.
        assert_eq!(shape_of_segment("Spa.app cfg"), Some(Shape::Spa));
        assert_eq!(shape_of_segment("Tui.app cfg"), Some(Shape::Tui));
        assert_eq!(shape_of_segment("Live.app cfg"), Some(Shape::Live));
    }

    #[test]
    fn uses_bare_app_run_distinguishes_dispatcher_from_concrete_runners() {
        assert!(uses_bare_app_run("main =\n    App.run appDef\n"));
        assert!(uses_bare_app_run("main = App.run (App.app { init = init })\n"));
        assert!(uses_bare_app_run("    App.run\n        (App.app cfg |> App.web)\n"));
        // Concrete runners are NOT the bare dispatcher.
        assert!(!uses_bare_app_run("main = App.runTui appDef\n"));
        assert!(!uses_bare_app_run("main = App.runLive appDef\n"));
        assert!(!uses_bare_app_run("main = App.runCli appDef\n"));
        assert!(!uses_bare_app_run("import Std.Live exposing (app)\n"));
    }

    /// Stage a minimal dispatched Std.App example dir (entry + optional
    /// `[app] target`) in a unique temp dir, so the fs-reading helpers can be
    /// exercised end-to-end. No `Date`/`rand` (banned in some contexts) — the
    /// caller passes a unique tag.
    fn stage_std_app(tag: &str, target: Option<&str>) -> std::path::PathBuf {
        let dir = std::env::temp_dir().join(format!("sky-build-run-stdapp-{tag}"));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(dir.join("src")).unwrap();
        std::fs::write(
            dir.join("src").join("Main.sky"),
            "module Main exposing (main)\n\nimport Std.App as App\n\nmain =\n    App.run appDef\n",
        )
        .unwrap();
        let toml = match target {
            Some(t) => format!("name = \"x\"\nentry = \"src/Main.sky\"\n\n[app]\ntarget = \"{t}\"\n"),
            None => "name = \"x\"\nentry = \"src/Main.sky\"\n".to_string(),
        };
        std::fs::write(dir.join("sky.toml"), toml).unwrap();
        dir
    }

    #[test]
    fn dispatched_std_app_shape_follows_the_app_target() {
        // No pin → web = Sky.Live (a bare `sky build` builds the Live server).
        let d = stage_std_app("none", None);
        assert!(is_std_app_dispatched(&d));
        assert_eq!(std_app_dispatch_shape(&d), Some(Shape::Live));

        // A terminal pin builds a terminal binary, NOT a Live server — the very
        // bug that made `20-cli-counter` / `21-24` fail "server exited on start".
        assert_eq!(
            std_app_dispatch_shape(&stage_std_app("tcli", Some("terminal:cli"))),
            Some(Shape::Cli)
        );
        assert_eq!(
            std_app_dispatch_shape(&stage_std_app("ttui", Some("terminal:tui"))),
            Some(Shape::Tui)
        );
        // Web / tablet families stay Live; client + GUI targets are build-only.
        assert_eq!(
            std_app_dispatch_shape(&stage_std_app("web", Some("web"))),
            Some(Shape::Live)
        );
        assert_eq!(
            std_app_dispatch_shape(&stage_std_app("spa", Some("web:app"))),
            Some(Shape::Spa)
        );
        assert_eq!(
            std_app_dispatch_shape(&stage_std_app("mob", Some("mobile:ios"))),
            Some(Shape::Spa)
        );
        assert_eq!(
            std_app_dispatch_shape(&stage_std_app("desk", Some("desktop"))),
            Some(Shape::Webview)
        );

        // A non-dispatched dir (no Std.App / no bare App.run) opts out entirely,
        // so classify() falls back to the main-body scan.
        let plain = std::env::temp_dir().join("sky-build-run-stdapp-plain");
        let _ = std::fs::remove_dir_all(&plain);
        std::fs::create_dir_all(plain.join("src")).unwrap();
        std::fs::write(
            plain.join("src").join("Main.sky"),
            "module Main exposing (main)\n\nmain =\n    println \"hi\"\n",
        )
        .unwrap();
        std::fs::write(plain.join("sky.toml"), "name = \"x\"\n").unwrap();
        assert!(!is_std_app_dispatched(&plain));
        assert_eq!(std_app_dispatch_shape(&plain), None);
    }

    fn cli_row(name: &str, stdout: &str) -> Row {
        Row {
            name: name.into(),
            shape: Shape::Cli,
            emitted: true,
            build_ok: true,
            run_ok: Some(true),
            matched: None,
            run_kind: "match",
            blocker: String::new(),
            rust_stdout: Some(stdout.into()),
            oracle_stdout: None,
        }
    }

    // The teeth, as a unit test: a Row whose normalised stdout equals its
    // committed golden classifies Match; mutate one byte of the golden and it
    // classifies Mismatch. Same mechanism the `--golden` gate fails on.
    #[test]
    fn golden_status_matches_then_mismatches_on_mutation() {
        let root = std::env::temp_dir().join(format!("golden-gate-test-{}", std::process::id()));
        let _ = std::fs::create_dir_all(golden_dir(&root));

        let stdout = "count = 3\nresult: ok\n";
        let row = cli_row("99-fixture", stdout);
        let normalised = normalise_stdout(stdout);

        // committed golden equals the normalised output → Match.
        std::fs::write(golden_file(&root, "99-fixture"), format!("{normalised}\n")).unwrap();
        let (has, status) = golden_status(&root, &row);
        assert!(has, "golden file present");
        assert!(
            matches!(status, GoldenStatus::Match),
            "matching golden → Match"
        );

        // mutate one byte of the golden → Mismatch (the teeth).
        let mut mutated: Vec<u8> = normalised.into_bytes();
        assert!(!mutated.is_empty());
        mutated[0] ^= 0x20; // flip case of the first char — a one-byte change
        std::fs::write(golden_file(&root, "99-fixture"), {
            let mut v = mutated.clone();
            v.push(b'\n');
            v
        })
        .unwrap();
        let (_, status2) = golden_status(&root, &row);
        assert!(
            matches!(status2, GoldenStatus::Mismatch),
            "mutated golden → Mismatch"
        );

        // no golden file → Missing.
        let _ = std::fs::remove_file(golden_file(&root, "99-fixture"));
        let (has3, status3) = golden_status(&root, &row);
        assert!(!has3);
        assert!(
            matches!(status3, GoldenStatus::Missing),
            "absent golden → Missing"
        );

        let _ = std::fs::remove_dir_all(&root);
    }

    // A row that did not build+run cannot be compared → NotRun (never a false
    // Match against a stale golden).
    #[test]
    fn golden_status_not_run_when_no_stdout() {
        let root = std::env::temp_dir().join(format!("golden-gate-test-nr-{}", std::process::id()));
        let _ = std::fs::create_dir_all(golden_dir(&root));
        std::fs::write(golden_file(&root, "99-fixture"), "whatever\n").unwrap();
        let mut row = cli_row("99-fixture", "");
        row.run_ok = Some(false);
        row.rust_stdout = None;
        let (_, status) = golden_status(&root, &row);
        assert!(matches!(status, GoldenStatus::NotRun));
        let _ = std::fs::remove_dir_all(&root);
    }

    // 0d — the one-byte-golden class. An EMPTY committed golden matches an
    // EMPTY run byte-for-byte, so the equality arm called it `Match` and the
    // gate was green forever while asserting nothing. This is precisely how
    // `golden/55-store-partial-update.stdout` sat at 1 byte: the example's
    // sky.toml had no [database] section, `Db.connect ()` failed, the Task
    // chain short-circuited, and the app printed nothing.
    //
    // The bad input is constructed three ways — empty golden + empty run
    // (the historical case), empty golden + real run, real golden + empty run
    // — and none of them may classify Match.
    #[test]
    fn empty_golden_is_never_a_match() {
        let root = std::env::temp_dir().join(format!("golden-gate-test-empty-{}", std::process::id()));
        let _ = std::fs::create_dir_all(golden_dir(&root));

        // (a) empty golden + empty run — what 55-store-partial-update was.
        std::fs::write(golden_file(&root, "99-fixture"), "\n").unwrap();
        let (_, status) = golden_status(&root, &cli_row("99-fixture", ""));
        assert!(
            matches!(status, GoldenStatus::Empty),
            "empty golden + empty run must be Empty (was Match — green forever, asserting nothing)"
        );

        // (b) empty golden + a real run — the state right after the underlying
        // example defect is fixed but before a re-bless.
        let (_, status_b) = golden_status(&root, &cli_row("99-fixture", "name=Big Mug stock=7\n"));
        assert!(
            matches!(status_b, GoldenStatus::Empty),
            "empty golden + real output must not be Mismatch-or-Match, it must be Empty"
        );

        // (c) a real golden + an empty run — the app regressed to silence.
        std::fs::write(golden_file(&root, "99-fixture"), "name=Big Mug stock=7\n").unwrap();
        let (_, status_c) = golden_status(&root, &cli_row("99-fixture", "   \n  \n"));
        assert!(
            matches!(status_c, GoldenStatus::Empty),
            "a run that produced nothing must be Empty, not silently compared"
        );

        // Control: a real golden + the matching real run still classifies Match,
        // so the guard did not simply break the gate.
        let (_, status_ok) = golden_status(&root, &cli_row("99-fixture", "name=Big Mug stock=7\n"));
        assert!(
            matches!(status_ok, GoldenStatus::Match),
            "non-empty matching pair must still be Match"
        );

        let _ = std::fs::remove_dir_all(&root);
    }

    // The machine-specific leak guard refuses a golden carrying an absolute
    // home path (would make the snapshot non-portable across machines/CI).
    #[test]
    fn machine_leak_flags_home_path() {
        assert!(machine_leak("ok\n/Users/alice/project/out\n").is_some());
        assert!(machine_leak("ok\n/home/bob/x\n").is_some());
        assert!(machine_leak("count = 3\nresult: ok").is_none());
    }
}

// 0c — a timeout must be a distinct, propagated failure, not a fabricated
// success. Before the fix `wait_bounded` returned
// `Some(Output { status: ExitStatus::default(), .. })` on EVERY path including
// kill-on-overrun, and `ExitStatus::default().success()` is `true` on Unix.
#[cfg(test)]
mod wait_bounded_tests {
    use super::*;

    /// Construct the bad input: a child that never exits on its own.
    fn hung_child() -> Child {
        Command::new("sleep")
            .arg("30")
            .stdin(Stdio::null())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .spawn()
            .expect("spawn sleep")
    }

    #[test]
    fn a_hung_child_is_a_timeout_not_a_success() {
        let mut child = hung_child();
        let started = Instant::now();
        let out = wait_bounded(&mut child, Duration::from_millis(300));
        let elapsed = started.elapsed();

        assert!(
            out.is_none(),
            "a child that overran its bound must report None (a timeout); it previously \
             returned Some(Output) whose fabricated ExitStatus::default() reported success, \
             making a hung app indistinguishable from a clean exit 0"
        );
        // It was actually killed rather than waited out.
        assert!(
            elapsed < Duration::from_secs(5),
            "must kill on overrun, not block for the child's full lifetime (took {elapsed:?})"
        );
    }

    #[test]
    fn a_clean_child_still_returns_its_real_status() {
        // Control: the fix must not break the normal path, and the status must
        // now be the child's REAL status rather than a fabricated default.
        let mut ok = Command::new("sh")
            .args(["-c", "printf hello"])
            .stdin(Stdio::null())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .spawn()
            .expect("spawn sh");
        let out = wait_bounded(&mut ok, Duration::from_secs(10)).expect("clean child → Some");
        assert!(out.status.success(), "exit 0 child reports success");
        assert_eq!(String::from_utf8_lossy(&out.stdout), "hello");

        let mut bad = Command::new("sh")
            .args(["-c", "exit 3"])
            .stdin(Stdio::null())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .spawn()
            .expect("spawn sh");
        let out2 = wait_bounded(&mut bad, Duration::from_secs(10)).expect("clean child → Some");
        assert!(
            !out2.status.success(),
            "a child that exited 3 must NOT report success — before the fix every child \
             reported ExitStatus::default(), i.e. success, whatever it actually did"
        );
    }

    /// The consequence the fix unlocks: `verify_tui`'s `None => (false, \"tui hung\")`
    /// arm was unreachable dead code, because `wait_bounded` never returned None.
    #[test]
    fn tui_hung_branch_is_now_reachable() {
        let mut child = hung_child();
        assert!(
            wait_bounded(&mut child, Duration::from_millis(200)).is_none(),
            "verify_tui's `None => (false, \"tui hung\")` arm depends on this being None"
        );
    }
}

// 0e — `gate_result` computed `run_ok` and `matched` on every Row, printed
// them, and then ignored them in the verdict. The three `--shape … --run` CI
// steps therefore could not fail.
#[cfg(test)]
mod gate_result_verdict_tests {
    use super::*;

    fn row(name: &str, shape: Shape, run_ok: Option<bool>, matched: Option<bool>) -> Row {
        Row {
            name: name.into(),
            shape,
            emitted: true,
            build_ok: true,
            run_ok,
            matched,
            run_kind: "match",
            blocker: String::new(),
            rust_stdout: None,
            oracle_stdout: None,
        }
    }

    /// The hard floor must stay satisfied in every fixture, or we would be
    /// measuring the hello-world check instead of the new one.
    fn hello_ok() -> Row {
        row("01-hello-world", Shape::Cli, Some(true), Some(true))
    }

    #[test]
    fn shards_are_disjoint_and_total() {
        // For every corpus size and shard count, the union of all shards is the
        // whole set and no example lands in two shards. A drop is a false-green:
        // an example no shard gates is silently unbuilt/unrun across the fan-out.
        let names = |total: usize| -> Vec<String> {
            (0..total).map(|i| format!("{i:03}-ex")).collect()
        };
        for total in [0usize, 1, 2, 3, 7, 60, 61] {
            for n in 1..=4usize {
                let full = names(total);
                let mut union: Vec<String> = Vec::new();
                for i in 0..n {
                    union.extend(apply_shard(full.clone(), Some((i, n))));
                }
                union.sort();
                let mut expect = full.clone();
                expect.sort();
                assert_eq!(union.len(), expect.len(), "size={total} n={n}: overlap or drop");
                assert_eq!(union, expect, "size={total} n={n}: union != corpus");
            }
        }
        // `--shard=I/N` guardrails: I>=N and N==0 must abort, not silently gate 0.
        assert!(std::panic::catch_unwind(|| parse_shard(&["--shard=2/2".into()])).is_err());
        assert!(std::panic::catch_unwind(|| parse_shard(&["--shard=0/0".into()])).is_err());
        assert_eq!(parse_shard(&["--shard=1/3".into()]), Some((1, 3)));
        assert_eq!(parse_shard(&["--all".into()]), None);
    }

    #[test]
    fn a_healthy_run_still_passes() {
        let rows = vec![
            hello_ok(),
            row("19-skyforum", Shape::Live, Some(true), Some(true)),
            row("20-cli-counter", Shape::Cli, Some(true), Some(true)),
        ];
        assert_eq!(gate_result(&rows), 0, "all-green must still exit 0");
    }

    #[test]
    fn an_app_that_panics_on_boot_fails_the_gate() {
        let rows = vec![
            hello_ok(),
            row("19-skyforum", Shape::Live, Some(false), None),
        ];
        assert_eq!(
            gate_result(&rows),
            1,
            "a built example that did not run must fail; before the fix this exited 0"
        );
    }

    #[test]
    fn an_oracle_mismatch_fails_the_gate() {
        let rows = vec![
            hello_ok(),
            row("19-skyforum", Shape::Live, Some(true), Some(false)),
        ];
        assert_eq!(
            gate_result(&rows),
            1,
            "rust output != oracle output must fail; before the fix `matched` was never read"
        );
    }

    /// The other half of the contract: the gate must NOT fire on states that
    /// are genuinely acceptable, or it becomes a different kind of useless.
    #[test]
    fn acceptable_states_do_not_fail_the_gate() {
        let rows = vec![
            hello_ok(),
            // Not run at all (no --run flag / build-only invocation).
            row("07-todo-cli", Shape::Cli, None, None),
            // Documented build-only ceilings: a macOS GUI with a blocking event
            // loop cannot be run headless.
            row("11-fyne-stopwatch", Shape::Ffi, None, None),
            row("38-composite-ui", Shape::Webview, None, None),
            // Ran fine, but no comparison was possible: nondeterministic stdout,
            // or a fresh CI checkout with no oracle binary, or the ORACLE itself
            // failed to start (an oracle-side environment problem).
            row("02-go-stdlib", Shape::Cli, Some(true), None),
            row("30-tui-demo", Shape::Tui, Some(true), None),
        ];
        assert_eq!(
            gate_result(&rows),
            0,
            "None run_ok/matched are acceptable states and must not be failed"
        );
    }
}

#[cfg(test)]
mod gate_classification_tests {
    use super::is_native_link_failure;

    // A native cgo/WebKit LINK failure (Sky.Webview on a runner missing the
    // framework) — environment ceiling, NOT a compiler hole. This is the exact
    // shape example 38-composite-ui-multibackend produced on macOS CI.
    #[test]
    fn native_cgo_link_failure_is_a_ceiling() {
        assert!(is_native_link_failure(
            "clang++: error: linker command failed with exit code 1 (use -v to see invocation)"
        ));
        assert!(is_native_link_failure("ld: framework not found WebKit"));
    }

    // The real check≢build holes that ALSO surface at "go build" time must NOT be
    // classified as ceilings — they stay hard gate failures.
    #[test]
    fn go_compiler_and_linker_holes_are_not_ceilings() {
        // Go COMPILER rejection (the func-field / 26-ui-showcase class).
        assert!(!is_native_link_failure(
            "./main.go:71:114: cannot use struct{Flag any}{…} as struct{Flag bool} value"
        ));
        // Go LINKER missing symbol (the ABI-guard class).
        assert!(!is_native_link_failure("undefined: rt.RecordUpdate"));
        // A clean build has no blocker.
        assert!(!is_native_link_failure(""));
    }
}

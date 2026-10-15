//! How many packages `go build` compiles at once: its `-p` flag.
//!
//! `go build` runs up to one compile per CPU at the same time. Inside one
//! `go build`, the generated `main` package always compiles ALONE: every other
//! package is one of its dependencies, so they have all finished before it
//! starts. A limit on `-p` therefore protects no memory from `main`; what it
//! limits is the dependency compiles running beside one another. So `-p` is
//! sized from the DEPENDENCY compiles, never from `main`:
//!
//! * The largest dependency compile is the generated FFI wrapper package
//!   (`sky-out/skyffi`) when a project has a large one — measured: a 13.9 MB
//!   `skyffi` (examples/13-skyshop, Stripe + Firebase) compiled in 4.06 GB —
//!   else an ordinary package. It is the larger of the estimate from
//!   `skyffi`'s size, [`OTHER_COMPILE_BYTES`], and the largest dependency peak
//!   MEASURED on this project ([`DEP_PEAK_RECORD`]).
//! * Every other compile beside it is counted at [`TYPICAL_COMPILE_BYTES`].
//!   Measured with every compile and link process sampled at Go's default
//!   `-p 8` on a cold cache: examples/09-live-counter peaked at 1.32 GB in
//!   total (its largest single compile ~0.6 GB, so ~0.1 GB per other slot),
//!   examples/13-skyshop at 4.13 GB (its 4.06 GB `skyffi` and almost nothing
//!   beside it: the dependency graph leaves few compiles running next to the
//!   big packages). go1.26.1, darwin/arm64.
//! * `-p n`: the largest dependency plus `n - 1` others fit in the available
//!   memory less a 1 GB reserve. When one per CPU fits, Go keeps its default.
//! * When the largest is the generated FFI package, nothing but the console
//!   package and the build stamp can compile beside it ([`BESIDE_FFI_BYTES`]):
//!   it imports `sky-app/rt` and every bound Go package, so Go starts it after
//!   all of them. It must fit with the console beside it, and `-p n` is sized
//!   from the ordinary compiles that run before it. Counting `n - 1` others
//!   beside it gave `-p 2` with 6 GB available, and a cold
//!   examples/13-skyshop build 7 s slower than at `-p 4`, with the same peak.
//! * A small machine (under [`SMALL_ROOM_BYTES`] of room, an 8 GB laptop or
//!   runner) does not trust the 0.25 GB figure, which was measured on
//!   darwin/arm64 only: each other compile counts as [`OTHER_COMPILE_BYTES`]
//!   and `-p` is at most [`SMALL_ROOM_MAX_JOBS`].
//! * `SKY_GO_BUILD_JOBS=<n>` (or `sky build --go-jobs <n>`) sets `-p <n>`;
//!   `auto` (or unset) decides.
//! * Unknown available memory → `-p 1`: a slower build is recoverable, an
//!   out-of-memory one is not.
//!
//! Two peak records live in `sky-out/`, for two different decisions:
//! * [`GO_PEAK_RECORD`] — the largest Go process of any build, `main`'s compile
//!   and the link included, kept at its maximum. The Sky.Spa leg plan sizes the
//!   overlap of two legs with it (`sky`'s `leg_plan`).
//! * [`DEP_PEAK_RECORD`] — the largest Go process of a build in which
//!   dependency packages compiled and `main` did not (Go's action graph says
//!   which ran). Only such a build measures a dependency compile, so a cache-hit
//!   build or a build that recompiled `main` never writes it. It also holds the
//!   link, so it can over-state, never under-state.
//!
//! Before v0.27.9 `-p` was sized from the first record: after one large build
//! of a project every later build ran at `-p 1`, which serialised hundreds of
//! small dependency compiles (cold examples/13-skyshop: 115 s at `-p 1`
//! against 64 s at Go's default).
//!
//! The decision is printed under the `--timings` table.

use crate::memory::{gib, RESERVE_BYTES};
use std::path::Path;

/// The override. A whole number `>= 1` sets `go build -p`; `auto` or unset
/// lets `sky build` decide. `sky build --go-jobs <n>` sets it.
pub const ENV_JOBS: &str = "SKY_GO_BUILD_JOBS";

/// File in `sky-out/` holding the largest Go tool process of any build (kept
/// at its maximum). Read by the Sky.Spa leg plan.
pub const GO_PEAK_RECORD: &str = ".sky-go-peak-bytes";

/// File in `sky-out/` holding the largest Go tool process of a build whose
/// dependency packages compiled and whose `main` did not (kept at its
/// maximum). Read by [`plan`].
pub const DEP_PEAK_RECORD: &str = ".sky-go-dep-peak-bytes";

/// File in `sky-out/` holding the `sky` process's own peak (parse, typecheck,
/// lower) of the last build. The Sky.Spa leg plan adds it to the Go peak: the
/// `sky` process stays resident while its `go build` runs.
pub const FRONT_PEAK_RECORD: &str = ".sky-front-peak-bytes";

/// Go's action graph of the last build (`go build -debug-actiongraph`), from
/// which [`record_peaks`] learns which packages compiled.
pub const ACTION_GRAPH: &str = ".sky-go-actions.json";

/// The estimate's fixed part for `main`: the runtime package (`sky-app/rt`)
/// and the console package each compile in about 520 MB (measured, go1.26.1).
const ESTIMATE_BASE_BYTES: u64 = 500 << 20;

/// The estimate's per-byte part for `main`: peak bytes per byte of generated
/// `main.go`. Calibrated on the measured compile of package `main`: a 2.53 MB
/// `main.go` peaked at 2.2 GB (500 MB + 2.53 MB × 700 = 2.2 GB).
const ESTIMATE_BYTES_PER_GO_BYTE: u64 = 700;

/// The floor for the largest dependency compile: the largest ordinary one
/// measured (stripe-go 648 MB, `sky-app/rt` 594 MB, `modernc.org/sqlite/lib`
/// 557 MB), rounded up.
pub const OTHER_COMPILE_BYTES: u64 = 700 << 20;

/// Each compile running beside the largest one: 2.5 times the measured
/// average (~0.1 GB per slot at `-p 8`, see the module docs). Measured on
/// darwin/arm64 only, so it is used only where a wrong guess cannot exhaust
/// the machine: see [`SMALL_ROOM_BYTES`].
pub const TYPICAL_COMPILE_BYTES: u64 = 250 << 20;

/// Below this much room (available memory less the reserve) the 0.25 GB
/// figure is not trusted: it was measured on one platform, and a project with
/// several large FFI modules can run several large compiles side by side.
/// There every other compile is counted as large as the largest ordinary one
/// ([`OTHER_COMPILE_BYTES`]), and `-p` is capped at
/// [`SMALL_ROOM_MAX_JOBS`]. A 16 GB machine (about 12 GB room) is above it
/// and keeps Go's default; an 8 GB one is below it.
pub const SMALL_ROOM_BYTES: u64 = 6 << 30;

/// The `-p` cap below [`SMALL_ROOM_BYTES`].
pub const SMALL_ROOM_MAX_JOBS: usize = 4;

/// What compiles beside the generated FFI package: the console package
/// (`sky-app/rt/console_app`, about 520 MB) and the build stamp (a few lines).
/// Counted as the largest ordinary compile. `skyffi` dot-imports `sky-app/rt`
/// and every bound Go package, so Go starts it only after all of them; the
/// packages `main` imports outside that set are the only ones that can run
/// beside it, whatever `-p` is. Measured on a cold examples/13-skyshop build
/// (`go build -debug-trace`, 1,700 actions): of the 19.2 s `skyffi` compile,
/// `console_app` overlapped 2.4 s and the stamp 0.01 s, and nothing else.
pub const BESIDE_FFI_BYTES: u64 = OTHER_COMPILE_BYTES;

/// Peak bytes per byte of generated `skyffi` Go: 13.9 MB compiled in 4.06 GB
/// ((4.06 GB - 500 MB) / 13.9 MB ≈ 270).
const FFI_BYTES_PER_GO_BYTE: u64 = 270;

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct GoJobs {
    /// `Some(n)` → pass `-p n`; `None` → Go's default (one per CPU).
    pub jobs: Option<usize>,
    /// One line for the `--timings` report.
    pub reason: String,
}

impl GoJobs {
    /// The `go build` argument, if any.
    pub fn arg(&self) -> Option<String> {
        self.jobs.map(|n| format!("-p={n}"))
    }
}

/// The estimate of `main`'s compile from the size of the generated `main.go`.
/// Used by the Sky.Spa leg plan, not by `-p`.
pub fn estimate_from_go_source(main_go_bytes: u64) -> u64 {
    ESTIMATE_BASE_BYTES.saturating_add(main_go_bytes.saturating_mul(ESTIMATE_BYTES_PER_GO_BYTE))
}

/// The estimate of the generated `skyffi` package's compile (0 bytes: no
/// such package, no estimate).
pub fn estimate_ffi(skyffi_bytes: u64) -> u64 {
    if skyffi_bytes == 0 {
        return 0;
    }
    ESTIMATE_BASE_BYTES.saturating_add(skyffi_bytes.saturating_mul(FFI_BYTES_PER_GO_BYTE))
}

/// The source label [`largest_dep_peak`] gives when the generated FFI package
/// is the largest dependency compile. [`decide`] plans that case on its own.
pub const FFI_ESTIMATE_SOURCE: &str = "(estimated from skyffi)";

/// The largest dependency compile to plan with: the largest of an ordinary
/// package, the `skyffi` estimate and the measured record.
pub fn largest_dep_peak(recorded: Option<u64>, ffi_estimate: u64) -> (u64, &'static str) {
    let r = recorded.unwrap_or(0);
    if r > ffi_estimate && r > OTHER_COMPILE_BYTES {
        (r, "(measured on an earlier build)")
    } else if ffi_estimate > OTHER_COMPILE_BYTES {
        (ffi_estimate, FFI_ESTIMATE_SOURCE)
    } else {
        (OTHER_COMPILE_BYTES, "(largest measured dependency)")
    }
}

/// How many compiles fit in `room` bytes: the largest one plus as many others
/// as fit beside it, at least one. Each other compile is counted at
/// [`other_compile_bytes`] for that room, and below [`SMALL_ROOM_BYTES`] the
/// count is capped at [`SMALL_ROOM_MAX_JOBS`].
pub fn compiles_that_fit(room: u64, largest: u64) -> usize {
    if room <= largest {
        return 1;
    }
    let n = 1 + ((room - largest) / other_compile_bytes(room).max(1)) as usize;
    if room < SMALL_ROOM_BYTES {
        n.min(SMALL_ROOM_MAX_JOBS)
    } else {
        n
    }
}

/// What each compile beside the largest is counted at in `room` bytes.
pub fn other_compile_bytes(room: u64) -> u64 {
    if room < SMALL_ROOM_BYTES {
        OTHER_COMPILE_BYTES
    } else {
        TYPICAL_COMPILE_BYTES
    }
}

/// The decision, from its inputs. Pure, so every branch is unit-tested.
pub fn decide(
    user: Option<&str>,
    largest_dep: u64,
    peak_source: &str,
    available: Option<u64>,
    cpus: usize,
) -> Result<GoJobs, String> {
    let cpus = cpus.max(1);
    if let Some(n) = parse_user(user)? {
        return Ok(GoJobs {
            jobs: Some(n),
            reason: format!("-p {n} ({ENV_JOBS}={n})"),
        });
    }
    let largest = largest_dep.max(1);
    let Some(avail) = available else {
        return Ok(GoJobs {
            jobs: Some(1),
            reason: format!(
                "-p 1 (available memory unknown; largest dependency compile ~{} {peak_source})",
                gib(largest)
            ),
        });
    };
    let room = avail.saturating_sub(RESERVE_BYTES);
    if room <= largest {
        return Ok(GoJobs {
            jobs: Some(1),
            reason: format!(
                "-p 1 ({} available < the largest dependency compile ~{} {peak_source} + {} reserve)",
                gib(avail),
                gib(largest),
                gib(RESERVE_BYTES)
            ),
        });
    }
    let cap = if room < SMALL_ROOM_BYTES {
        format!(", at most {SMALL_ROOM_MAX_JOBS} on a small machine")
    } else {
        String::new()
    };
    let (n, what) = if peak_source == FFI_ESTIMATE_SOURCE {
        // The generated FFI package compiles after every package it imports,
        // with only the console and the stamp beside it (BESIDE_FFI_BYTES), so
        // `-p` does not change what runs beside it. `-p` limits the compiles
        // before it, which are ordinary packages.
        if room < largest.saturating_add(BESIDE_FFI_BYTES) {
            return Ok(GoJobs {
                jobs: Some(1),
                reason: format!(
                    "-p 1 ({} available < the generated FFI compile ~{} {peak_source} + the console compile beside it ~{} + {} reserve)",
                    gib(avail),
                    gib(largest),
                    gib(BESIDE_FFI_BYTES),
                    gib(RESERVE_BYTES)
                ),
            });
        }
        let n = compiles_that_fit(room, OTHER_COMPILE_BYTES);
        let what = format!(
            "{} available holds the generated FFI compile ~{} {peak_source} + the console compile beside it ~{}, and before it {} compiles of ~{} each + {} reserve{cap}",
            gib(avail),
            gib(largest),
            gib(BESIDE_FFI_BYTES),
            n,
            gib(other_compile_bytes(room)),
            gib(RESERVE_BYTES)
        );
        (n, what)
    } else {
        let n = compiles_that_fit(room, largest);
        let what = format!(
            "{} available holds the largest dependency compile ~{} {peak_source} + {} of ~{} each + {} reserve{cap}",
            gib(avail),
            gib(largest),
            n - 1,
            gib(other_compile_bytes(room)),
            gib(RESERVE_BYTES)
        );
        (n, what)
    };
    if n >= cpus {
        return Ok(GoJobs {
            jobs: None,
            reason: format!("-p {cpus} (Go's default: {what})"),
        });
    }
    Ok(GoJobs {
        jobs: Some(n),
        reason: format!("-p {n} ({what})"),
    })
}

/// A user's `-p`: `Some(n)` for a whole number `>= 1`, `None` for unset /
/// empty / `auto`, an error naming the setting otherwise.
pub fn parse_user(user: Option<&str>) -> Result<Option<usize>, String> {
    let Some(v) = user
        .map(str::trim)
        .filter(|v| !v.is_empty() && *v != "auto")
    else {
        return Ok(None);
    };
    match v.parse::<usize>() {
        Ok(n) if n >= 1 => Ok(Some(n)),
        _ => Err(format!(
            "{ENV_JOBS} (--go-jobs) must be a whole number >= 1 or `auto`, got `{v}`"
        )),
    }
}

/// Total bytes of `.go` files directly in `dir` (0 when absent).
fn go_bytes(dir: &Path) -> u64 {
    std::fs::read_dir(dir).map_or(0, |rd| {
        rd.flatten()
            .filter(|e| e.path().extension().is_some_and(|x| x == "go"))
            .map(|e| e.metadata().map_or(0, |m| m.len()))
            .sum()
    })
}

/// The largest dependency compile for the Go tree in `out_dir`.
pub fn largest_dep_for(out_dir: &Path) -> (u64, &'static str) {
    largest_dep_peak(
        crate::memory::recorded_peak(&out_dir.join(DEP_PEAK_RECORD)),
        estimate_ffi(go_bytes(&out_dir.join("skyffi"))),
    )
}

/// The decision for the Go tree in `out_dir`, from the environment, the
/// project's records and this machine.
pub fn plan(out_dir: &Path) -> Result<GoJobs, String> {
    let cpus = std::thread::available_parallelism().map_or(1, |n| n.get());
    plan_with(
        out_dir,
        std::env::var(ENV_JOBS).ok().as_deref(),
        &std::env::var("GOFLAGS").unwrap_or_default(),
        crate::memory::available_memory(),
        cpus,
    )
}

/// [`plan`] with the environment and the machine passed in.
pub fn plan_with(
    out_dir: &Path,
    user: Option<&str>,
    goflags: &str,
    available: Option<u64>,
    cpus: usize,
) -> Result<GoJobs, String> {
    // A `-p` the user already passes through `GOFLAGS` stands: a command-line
    // `-p` would silently replace it.
    if user.is_none() && goflags_set_p(goflags) {
        return Ok(GoJobs {
            jobs: None,
            reason: "-p from GOFLAGS".to_string(),
        });
    }
    let (largest, source) = largest_dep_for(out_dir);
    decide(user, largest, source, available, cpus)
}

/// True when a `GOFLAGS` value already sets `go build -p`.
pub fn goflags_set_p(goflags: &str) -> bool {
    goflags
        .split_whitespace()
        .any(|f| matches!(f.split('=').next(), Some("-p" | "--p")))
}

/// The `go build` flag that writes the action graph [`record_peaks`] reads.
/// A debug flag: it is not part of any package's cache key.
pub fn action_graph_flag(out_dir: &Path) -> String {
    // Absolute: `go` runs in `out_dir`, so a relative path would resolve twice.
    let file = out_dir.join(ACTION_GRAPH);
    let file = std::path::absolute(&file).unwrap_or(file);
    format!("-debug-actiongraph={}", file.to_string_lossy())
}

/// The generated build-stamp package (`build_stamp::STAMP_PKG`): a few lines
/// of Go. A build in which it is the only dependency that compiled (a new
/// commit, or a Sky.Spa SSR backend's second build that links the frontend's
/// wasm name) measured the link, not a dependency compile, so it is not
/// counted as one.
const STAMP_IMPORT_PATH: &str = "sky-app/skybuildinfo";

/// What compiled in a build, from Go's action graph: `(dependencies, main)`.
/// The stamp package alone is not a dependency compile. `None` when the graph
/// cannot be read.
pub fn compiled(action_graph: &str) -> Option<(bool, bool)> {
    let v: serde_json::Value = serde_json::from_str(action_graph).ok()?;
    let mut deps = false;
    let mut main = false;
    for a in v.as_array()? {
        if a.get("Mode").and_then(|m| m.as_str()) != Some("build") {
            continue;
        }
        let ran = a
            .get("Cmd")
            .and_then(|c| c.as_array())
            .is_some_and(|c| !c.is_empty());
        if !ran {
            continue;
        }
        match a.get("Package").and_then(|p| p.as_str()) {
            Some("sky-app") => main = true,
            Some(STAMP_IMPORT_PATH) => {}
            _ => deps = true,
        }
    }
    Some((deps, main))
}

/// Whether a build's largest Go process measured a dependency compile: some
/// dependency compiled and `main` did not (else it is `main`, or only the link).
pub fn dep_peak_is_measured(ran: Option<(bool, bool)>) -> bool {
    matches!(ran, Some((true, false)))
}

/// After ONE `go build` returns: record its largest tool process (for the leg
/// plan), the dependency peak when only dependencies compiled (for `-p`), and
/// this process's own peak. `go_peak` is that `go build` child's own peak
/// (`memory::wait_child`), never an earlier child's: a build that runs `go
/// build` twice records each run with its own peak and its own action graph.
/// Best-effort.
pub fn record_peaks(out_dir: &Path, go_peak: Option<u64>) {
    let graph = out_dir.join(ACTION_GRAPH);
    let ran = std::fs::read_to_string(&graph)
        .ok()
        .and_then(|t| compiled(&t));
    let _ = std::fs::remove_file(&graph);
    if let Some(p) = go_peak {
        crate::memory::record_peak(&out_dir.join(GO_PEAK_RECORD), p);
        if dep_peak_is_measured(ran) {
            crate::memory::record_peak(&out_dir.join(DEP_PEAK_RECORD), p);
        }
    }
    if let Some(p) = crate::timings::peak_rss_bytes() {
        crate::memory::record_peak(&out_dir.join(FRONT_PEAK_RECORD), p);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const GB: u64 = 1 << 30;
    const MB: u64 = 1 << 20;

    #[test]
    fn a_large_main_alone_keeps_gos_default() {
        // The 5.6 GB `main` compile of the pre-v0.25.18 target app, 12 GB
        // available, 8 CPUs. `main` compiles alone, so it does not size -p:
        // 11 GB holds the largest dependency (0.7 GB) + 42 others. Before
        // v0.27.9 `-p` was sized by that `main` and gave -p 1.
        let (largest, why) = largest_dep_peak(None, 0);
        let j = decide(None, largest, why, Some(12 * GB), 8).unwrap();
        assert_eq!(j.jobs, None, "{j:?}");
        assert!(j.reason.starts_with("-p 8 (Go's default"), "{j:?}");
    }

    #[test]
    fn a_large_ffi_package_compiles_beside_the_console_only() {
        // examples/13-skyshop: a 13.9 MB skyffi, measured at 4.06 GB.
        let est = estimate_ffi(13_928 * 1024);
        assert!((3900 * MB..4200 * MB).contains(&est), "{est}");
        let (largest, why) = largest_dep_peak(None, est);
        assert_eq!(why, FFI_ESTIMATE_SOURCE);
        // 6 GB available: 5 GB room (a small machine). The FFI compile (~4.1
        // GB) runs beside the console compile only (0.7 GB): 4.8 GB fits.
        // Before it, the ordinary compiles count 0.7 GB each: 1 + 6, capped
        // at 4. This was -p 2 (the FFI compile + one other at 0.7 GB), which
        // made a cold build 7 s slower (52.2 s of `go build` at -p 2, 45.8 s
        // at -p 4, 45.6 s at -p 8; the total of all Go processes peaked at
        // 3.9, 4.1 and 4.1 GB).
        let j = decide(None, largest, why, Some(6 * GB), 8).unwrap();
        assert_eq!(j.jobs, Some(4), "{j:?}");
        assert!(j.reason.contains("the console compile beside it"), "{j:?}");
        // 5.5 GB available: 4.5 GB room holds the FFI compile but not the
        // console compile beside it: one at a time.
        let j = decide(None, largest, why, Some(5632 * MB), 8).unwrap();
        assert_eq!(j.jobs, Some(1), "{j:?}");
        // A measured peak keeps the general rule: it may be any package, so
        // every other compile is counted beside it.
        let j = decide(
            None,
            largest,
            "(measured on an earlier build)",
            Some(6 * GB),
            8,
        )
        .unwrap();
        assert_eq!(j.jobs, Some(2), "{j:?}");
        // Plenty of memory: default.
        assert_eq!(
            decide(None, largest, why, Some(32 * GB), 8).unwrap().jobs,
            None
        );
        // Not even the largest fits: one at a time, and it says why.
        let j = decide(None, largest, why, Some(4 * GB), 8).unwrap();
        assert_eq!(j.jobs, Some(1));
        assert!(
            j.reason.contains("< the largest dependency compile"),
            "{j:?}"
        );
    }

    /// An 8 GB machine has about 5 GB available: the 0.25 GB per other
    /// compile (measured on darwin/arm64 only) is not trusted there. Before,
    /// 4 GB of room gave 14 compiles → Go's default -p 8, enough to exhaust
    /// the machine when several large FFI modules compile side by side.
    #[test]
    fn an_8gb_machine_sizes_the_other_compiles_conservatively() {
        let (largest, why) = largest_dep_peak(None, 0);
        // 5 GB available, 4 room: 0.7 + 4 others at 0.7 → 5, capped at 4.
        let j = decide(None, largest, why, Some(5 * GB), 8).unwrap();
        assert_eq!(j.jobs, Some(SMALL_ROOM_MAX_JOBS), "{j:?}");
        assert!(j.reason.contains("at most 4 on a small machine"), "{j:?}");
        assert!(j.reason.contains("of ~0.7 GB each"), "{j:?}");
        // A 2.5 GB measured dependency peak: 4 GB room leaves 1.5 GB → 2 others.
        let j = decide(None, 2560 * MB, "(measured)", Some(5 * GB), 8).unwrap();
        assert_eq!(j.jobs, Some(3), "{j:?}");
        // Two CPUs: the cap does not lift -p above Go's default.
        let j = decide(None, largest, why, Some(5 * GB), 2).unwrap();
        assert_eq!(j.jobs, None, "{j:?}");
    }

    /// The 16 GB / 4 vCPU CI runner (about 13 GB available) keeps Go's
    /// default, with or without a large `skyffi`.
    #[test]
    fn a_16gb_runner_keeps_gos_default() {
        let (largest, why) = largest_dep_peak(None, 0);
        let j = decide(None, largest, why, Some(13 * GB), 4).unwrap();
        assert_eq!(j.jobs, None, "{j:?}");
        let est = estimate_ffi(13_928 * 1024);
        let (largest, why) = largest_dep_peak(None, est);
        let j = decide(None, largest, why, Some(13 * GB), 4).unwrap();
        assert_eq!(j.jobs, None, "{j:?}");
    }

    #[test]
    fn unknown_memory_builds_one_package_at_a_time() {
        let j = decide(None, GB, "(estimated)", None, 8).unwrap();
        assert_eq!(j.jobs, Some(1));
        assert!(j.reason.contains("unknown"), "{j:?}");
    }

    #[test]
    fn the_env_override_wins_and_rejects_junk() {
        let j = decide(Some("3"), 50 * GB, "", Some(GB), 8).unwrap();
        assert_eq!(j.jobs, Some(3));
        assert_eq!(j.reason, "-p 3 (SKY_GO_BUILD_JOBS=3)");
        assert_eq!(
            decide(Some(" 12 "), GB, "", None, 4).unwrap().jobs,
            Some(12)
        );
        // `auto` and empty decide as if unset.
        assert_eq!(
            decide(Some("auto"), GB, "", Some(100 * GB), 4)
                .unwrap()
                .jobs,
            None
        );
        assert_eq!(decide(Some(""), GB, "", None, 4).unwrap().jobs, Some(1));
        for bad in ["0", "-2", "two", "1.5"] {
            let e = decide(Some(bad), GB, "", Some(100 * GB), 4).unwrap_err();
            assert!(e.contains(ENV_JOBS) && e.contains(bad), "{e}");
        }
    }

    #[test]
    fn the_largest_dependency_is_the_largest_of_its_three_sources() {
        assert_eq!(
            largest_dep_peak(None, 0),
            (OTHER_COMPILE_BYTES, "(largest measured dependency)")
        );
        assert_eq!(largest_dep_peak(Some(300 * MB), 0).0, OTHER_COMPILE_BYTES);
        assert_eq!(
            largest_dep_peak(Some(5 * GB), 4 * GB),
            (5 * GB, "(measured on an earlier build)")
        );
        assert_eq!(largest_dep_peak(Some(GB), 4 * GB).0, 4 * GB);
        assert_eq!(estimate_ffi(0), 0);
    }

    /// The record of a `main` compile never reaches `-p`: a project whose last
    /// build measured a 5.6 GB `main` (GO_PEAK_RECORD) keeps Go's default on a
    /// machine with room for its dependencies. Before v0.27.9 that record gave
    /// -p 1 for every later build of the project.
    #[test]
    fn a_main_peak_record_does_not_lower_p() {
        let d = std::env::temp_dir().join(format!("sky-gojobs-main-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&d);
        std::fs::create_dir_all(&d).unwrap();
        crate::memory::record_peak(&d.join(GO_PEAK_RECORD), 5600 * MB);
        std::fs::write(d.join("main.go"), vec![b'x'; 3 << 20]).unwrap();
        let j = plan_with(&d, None, "", Some(9 * GB), 8).unwrap();
        assert_eq!(j.jobs, None, "{j:?}");
        // A measured dependency peak does count: 7 GB room - 6 GB leaves 1 GB,
        // 4 others at 0.25 GB.
        crate::memory::record_peak(&d.join(DEP_PEAK_RECORD), 6 * GB);
        let j = plan_with(&d, None, "", Some(8 * GB), 8).unwrap();
        assert_eq!(j.jobs, Some(5), "{j:?}");
        let _ = std::fs::remove_dir_all(&d);
    }

    #[test]
    fn a_dep_peak_is_recorded_only_when_only_dependencies_compiled() {
        let graph = |pkgs: &[(&str, bool)]| -> String {
            let acts: Vec<String> = pkgs
                .iter()
                .map(|(p, ran)| {
                    format!(
                        r#"{{"ID":0,"Mode":"build","Package":"{p}","Cmd":{}}}"#,
                        if *ran { r#"["compile"]"# } else { "null" }
                    )
                })
                .chain(std::iter::once(
                    r#"{"ID":9,"Mode":"link","Package":"sky-app","Cmd":["link"]}"#.to_string(),
                ))
                .collect();
            format!("[{}]", acts.join(","))
        };
        let only_deps = graph(&[("sky-app/rt", true), ("sky-app", false)]);
        // Only the stamp package compiled (a new commit; a Sky.Spa SSR
        // backend's second build that links the frontend's wasm name): the
        // largest process was the link, not a dependency compile.
        let stamp_only = graph(&[
            ("sky-app/rt", false),
            ("sky-app/skybuildinfo", true),
            ("sky-app", false),
        ]);
        assert!(!dep_peak_is_measured(compiled(&stamp_only)));
        let stamp_and_deps = graph(&[("sky-app/rt", true), ("sky-app/skybuildinfo", true)]);
        assert!(dep_peak_is_measured(compiled(&stamp_and_deps)));
        let with_main = graph(&[("sky-app/rt", true), ("sky-app", true)]);
        let main_only = graph(&[("sky-app/rt", false), ("sky-app", true)]);
        let cached = graph(&[("sky-app/rt", false), ("sky-app", false)]);
        assert_eq!(compiled(&only_deps), Some((true, false)));
        assert!(dep_peak_is_measured(compiled(&only_deps)));
        assert!(!dep_peak_is_measured(compiled(&with_main)));
        assert!(!dep_peak_is_measured(compiled(&main_only)));
        assert!(!dep_peak_is_measured(compiled(&cached)));
        assert!(!dep_peak_is_measured(compiled("not json")));
    }

    /// S1: a Sky.Spa SSR backend builds twice in one process: everything
    /// first (a large `main`), then only the stamp package and the link. The
    /// second build's record must come from its own child and its own action
    /// graph, so the first build's `main` never becomes the dependency peak.
    #[test]
    fn a_stamp_only_second_build_does_not_raise_the_dep_record() {
        let d = std::env::temp_dir().join(format!("sky-gojobs-stamp-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&d);
        std::fs::create_dir_all(&d).unwrap();
        let write_graph = |pkgs: &[(&str, bool)]| {
            let acts: Vec<String> = pkgs
                .iter()
                .map(|(p, ran)| {
                    format!(
                        r#"{{"Mode":"build","Package":"{p}","Cmd":{}}}"#,
                        if *ran { r#"["compile"]"# } else { "null" }
                    )
                })
                .collect();
            std::fs::write(d.join(ACTION_GRAPH), format!("[{}]", acts.join(","))).unwrap();
        };
        // Build 1: dependencies and `main` compiled; `main` peaked at 5 GB.
        write_graph(&[("sky-app/rt", true), ("sky-app", true)]);
        record_peaks(&d, Some(5 * GB));
        assert_eq!(crate::memory::recorded_peak(&d.join(DEP_PEAK_RECORD)), None);
        assert_eq!(
            crate::memory::recorded_peak(&d.join(GO_PEAK_RECORD)),
            Some(5 * GB)
        );
        // Build 2: only the stamp package and the link, a 1.3 GB link.
        write_graph(&[
            ("sky-app/rt", false),
            ("sky-app/skybuildinfo", true),
            ("sky-app", false),
        ]);
        record_peaks(&d, Some(1300 * MB));
        assert_eq!(
            crate::memory::recorded_peak(&d.join(DEP_PEAK_RECORD)),
            None,
            "a stamp-only build must not record a dependency peak"
        );
        let _ = std::fs::remove_dir_all(&d);
    }

    #[test]
    fn a_p_in_goflags_is_detected() {
        assert!(goflags_set_p("-p=1"));
        assert!(goflags_set_p("-mod=mod -p=4 -trimpath"));
        assert!(goflags_set_p("--p=2"));
        assert!(!goflags_set_p(""));
        assert!(!goflags_set_p("-pgo=off -mod=mod"));
    }

    #[test]
    fn the_estimate_matches_its_calibration_point() {
        // The 2.53 MB main.go of the target app compiled in 2.2 GB.
        let est = estimate_from_go_source(2_534_562);
        assert!((2_000 << 20..2_450 << 20).contains(&est), "{est}");
        assert_eq!(estimate_from_go_source(0), 500 << 20);
    }
}

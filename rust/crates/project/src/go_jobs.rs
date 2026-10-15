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
//! * Every other compile beside it is counted at [`OTHER_COMPILE_BYTES`] (the
//!   largest measured: stripe-go 648 MB, `sky-app/rt` 594 MB,
//!   `modernc.org/sqlite/lib` 557 MB; go1.26.1, darwin/arm64).
//! * `-p n`: the largest dependency plus `n - 1` others fit in the available
//!   memory less a 1 GB reserve. When one per CPU fits, Go keeps its default.
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

/// A dependency compile other than the largest: the largest measured
/// (stripe-go 648 MB, `sky-app/rt` 594 MB, `modernc.org/sqlite/lib` 557 MB),
/// rounded up.
pub const OTHER_COMPILE_BYTES: u64 = 700 << 20;

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

/// The largest dependency compile to plan with: the largest of an ordinary
/// package, the `skyffi` estimate and the measured record.
pub fn largest_dep_peak(recorded: Option<u64>, ffi_estimate: u64) -> (u64, &'static str) {
    let r = recorded.unwrap_or(0);
    if r > ffi_estimate && r > OTHER_COMPILE_BYTES {
        (r, "(measured on an earlier build)")
    } else if ffi_estimate > OTHER_COMPILE_BYTES {
        (ffi_estimate, "(estimated from skyffi)")
    } else {
        (OTHER_COMPILE_BYTES, "(largest measured dependency)")
    }
}

/// How many compiles fit in `room` bytes: the largest one plus as many others
/// as fit beside it, at least one.
pub fn compiles_that_fit(room: u64, largest: u64) -> usize {
    if room <= largest {
        return 1;
    }
    1 + ((room - largest) / OTHER_COMPILE_BYTES.max(1)) as usize
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
    let n = compiles_that_fit(room, largest);
    let what = format!(
        "{} available holds the largest dependency compile ~{} {peak_source} + {} of ~{} each + {} reserve",
        gib(avail),
        gib(largest),
        n - 1,
        gib(OTHER_COMPILE_BYTES),
        gib(RESERVE_BYTES)
    );
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

/// What compiled in a build, from Go's action graph: `(dependencies, main)`.
/// `None` when the graph cannot be read.
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
        if a.get("Package").and_then(|p| p.as_str()) == Some("sky-app") {
            main = true;
        } else {
            deps = true;
        }
    }
    Some((deps, main))
}

/// Whether a build's largest Go process measured a dependency compile: some
/// dependency compiled and `main` did not (else it is `main`, or only the link).
pub fn dep_peak_is_measured(ran: Option<(bool, bool)>) -> bool {
    matches!(ran, Some((true, false)))
}

/// After `go build` returns: record its largest tool process (for the leg
/// plan), the dependency peak when only dependencies compiled (for `-p`), and
/// this process's own peak. Best-effort.
pub fn record_peaks(out_dir: &Path) {
    let graph = out_dir.join(ACTION_GRAPH);
    let ran = std::fs::read_to_string(&graph)
        .ok()
        .and_then(|t| compiled(&t));
    let _ = std::fs::remove_file(&graph);
    if let Some(p) = crate::memory::children_peak_rss() {
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
        // 11 GB holds the largest dependency (0.7 GB) + 15 others. Before
        // v0.27.9 `-p` was sized by that `main` and gave -p 1.
        let (largest, why) = largest_dep_peak(None, 0);
        let j = decide(None, largest, why, Some(12 * GB), 8).unwrap();
        assert_eq!(j.jobs, None, "{j:?}");
        assert!(j.reason.starts_with("-p 8 (Go's default"), "{j:?}");
    }

    #[test]
    fn a_large_ffi_package_limits_the_compiles_beside_it() {
        // examples/13-skyshop: a 13.9 MB skyffi, measured at 4.06 GB.
        let est = estimate_ffi(13_928 * 1024);
        assert!((3900 * MB..4200 * MB).contains(&est), "{est}");
        let (largest, why) = largest_dep_peak(None, est);
        assert_eq!(why, "(estimated from skyffi)");
        // 8 GB available: 7 GB room - ~4.1 GB leaves 4 others → -p 5 of 8.
        let j = decide(None, largest, why, Some(8 * GB), 8).unwrap();
        assert_eq!(j.jobs, Some(5), "{j:?}");
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
        // A measured dependency peak does count: 5 GB room - 3 GB = 2 others.
        crate::memory::record_peak(&d.join(DEP_PEAK_RECORD), 3 * GB);
        let j = plan_with(&d, None, "", Some(6 * GB), 8).unwrap();
        assert_eq!(j.jobs, Some(3), "{j:?}");
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

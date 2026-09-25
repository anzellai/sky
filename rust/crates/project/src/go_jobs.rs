//! How many packages `go build` compiles at once: its `-p` flag.
//!
//! `go build` runs up to one compile per CPU at the same time, and a compile of
//! a large generated Sky `main` package is the biggest process in a build (the
//! 22k-line Sky.Spa app that set the target: 5.6 GB before v0.25.18 changed the
//! emitted Go's shape, 2.2 GB after). Two Sky.Spa legs, or a large `main`
//! beside the runtime's packages, can therefore put several such compiles in
//! memory at once. So `sky build` limits `-p` to the number of compiles the
//! machine's available memory holds, and leaves Go's default when it holds one
//! per CPU:
//!
//! * The per-compile peak is an estimate from the size of the generated
//!   `main.go`, raised to the largest Go tool process MEASURED on this
//!   project's previous builds ([`GO_PEAK_RECORD`] in `sky-out/`) when that was
//!   more ([`per_compile_peak`]).
//! * `SKY_GO_BUILD_JOBS=<n>` sets `-p <n>`; `auto` (or unset) decides.
//! * Unknown available memory → `-p 1`, as the leg plan goes serial.
//!
//! The decision is printed under the `--timings` table.

use crate::memory::{gib, RESERVE_BYTES};
use std::path::Path;

/// The override. A whole number `>= 1` sets `go build -p`; `auto` or unset
/// lets `sky build` decide.
pub const ENV_JOBS: &str = "SKY_GO_BUILD_JOBS";

/// File in `sky-out/` holding the largest Go tool process of the last build.
pub const GO_PEAK_RECORD: &str = ".sky-go-peak-bytes";

/// File in `sky-out/` holding the `sky` process's own peak (parse, typecheck,
/// lower) of the last build. The Sky.Spa leg plan adds it to the Go peak: the
/// `sky` process stays resident while its `go build` runs.
pub const FRONT_PEAK_RECORD: &str = ".sky-front-peak-bytes";

/// The estimate's fixed part: the runtime package (`sky-app/rt`) and the
/// console package each compile in about 520 MB (measured, go1.26.1).
const ESTIMATE_BASE_BYTES: u64 = 500 << 20;

/// The estimate's per-byte part: peak bytes per byte of generated `main.go`.
/// Calibrated on the measured compile of package `main`: a 2.53 MB `main.go`
/// peaked at 2.2 GB (500 MB + 2.53 MB × 700 = 2.2 GB).
const ESTIMATE_BYTES_PER_GO_BYTE: u64 = 700;

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

/// The per-compile peak estimate from the size of the generated `main.go`.
pub fn estimate_from_go_source(main_go_bytes: u64) -> u64 {
    ESTIMATE_BASE_BYTES.saturating_add(main_go_bytes.saturating_mul(ESTIMATE_BYTES_PER_GO_BYTE))
}

/// The per-compile peak to plan with: the larger of the measured record and
/// the estimate. A record alone can under-state: a build whose `main` came from
/// Go's cache measured only the link. The estimate alone can under-state a
/// shape it was not calibrated on, which is what the record catches.
pub fn per_compile_peak(recorded: Option<u64>, estimate: u64) -> (u64, &'static str) {
    match recorded {
        Some(r) if r > estimate => (r, "(measured last build)"),
        _ => (estimate, "(estimated from main.go)"),
    }
}

/// The decision, from its inputs. Pure, so every branch is unit-tested.
pub fn decide(
    user: Option<&str>,
    per_compile_peak: u64,
    peak_source: &str,
    available: Option<u64>,
    cpus: usize,
) -> Result<GoJobs, String> {
    let cpus = cpus.max(1);
    if let Some(v) = user
        .map(str::trim)
        .filter(|v| !v.is_empty() && *v != "auto")
    {
        return match v.parse::<usize>() {
            Ok(n) if n >= 1 => Ok(GoJobs {
                jobs: Some(n),
                reason: format!("-p {n} ({ENV_JOBS}={n})"),
            }),
            _ => Err(format!(
                "{ENV_JOBS} must be a whole number >= 1 or `auto`, got `{v}`"
            )),
        };
    }
    let per = per_compile_peak.max(1);
    let Some(avail) = available else {
        return Ok(GoJobs {
            jobs: Some(1),
            reason: format!(
                "-p 1 (available memory unknown; each compile ~{} {peak_source})",
                gib(per)
            ),
        });
    };
    let slots = (avail.saturating_sub(RESERVE_BYTES) / per) as usize;
    if slots >= cpus {
        return Ok(GoJobs {
            jobs: None,
            reason: format!(
                "-p {cpus} (Go's default: {} available holds {cpus} compiles of ~{} {peak_source} + {} reserve)",
                gib(avail),
                gib(per),
                gib(RESERVE_BYTES)
            ),
        });
    }
    let n = slots.max(1);
    let why = if slots == 0 {
        format!(
            "{} available < one compile of ~{} {peak_source} + {} reserve",
            gib(avail),
            gib(per),
            gib(RESERVE_BYTES)
        )
    } else {
        format!(
            "{} available holds {slots} of {cpus} compiles of ~{} {peak_source} + {} reserve",
            gib(avail),
            gib(per),
            gib(RESERVE_BYTES)
        )
    };
    Ok(GoJobs {
        jobs: Some(n),
        reason: format!("-p {n} ({why})"),
    })
}

/// The decision for the Go tree in `out_dir`, from the environment, the
/// project's records and this machine.
pub fn plan(out_dir: &Path) -> Result<GoJobs, String> {
    let user = std::env::var(ENV_JOBS).ok();
    // A `-p` the user already passes through `GOFLAGS` stands: a command-line
    // `-p` would silently replace it.
    if user.is_none() && goflags_set_p(&std::env::var("GOFLAGS").unwrap_or_default()) {
        return Ok(GoJobs {
            jobs: None,
            reason: "-p from GOFLAGS".to_string(),
        });
    }
    let main_go = std::fs::metadata(out_dir.join("main.go")).map_or(0, |m| m.len());
    let (peak, source) = per_compile_peak(
        crate::memory::recorded_peak(&out_dir.join(GO_PEAK_RECORD)),
        estimate_from_go_source(main_go),
    );
    let cpus = std::thread::available_parallelism().map_or(1, |n| n.get());
    decide(
        user.as_deref(),
        peak,
        source,
        crate::memory::available_memory(),
        cpus,
    )
}

/// True when a `GOFLAGS` value already sets `go build -p`.
pub fn goflags_set_p(goflags: &str) -> bool {
    goflags
        .split_whitespace()
        .any(|f| matches!(f.split('=').next(), Some("-p" | "--p")))
}

/// After `go build` returns: record its largest tool process and this
/// process's own peak for the next build's decisions. Best-effort.
pub fn record_peaks(out_dir: &Path) {
    if let Some(p) = crate::memory::children_peak_rss() {
        crate::memory::record_peak(&out_dir.join(GO_PEAK_RECORD), p);
    }
    if let Some(p) = crate::timings::peak_rss_bytes() {
        crate::memory::record_peak(&out_dir.join(FRONT_PEAK_RECORD), p);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const GB: u64 = 1 << 30;

    #[test]
    fn room_for_a_compile_per_cpu_keeps_gos_default() {
        // 12 GB available, 2.2 GB compiles: 11 GB holds 5, so a 4-CPU machine
        // keeps Go's default of 4.
        let j = decide(None, 2200 << 20, "(measured)", Some(12 * GB), 4).unwrap();
        assert_eq!(j.jobs, None, "{j:?}");
        assert_eq!(j.arg(), None);
        assert!(j.reason.starts_with("-p 4 (Go's default"), "{j:?}");
    }

    #[test]
    fn too_little_memory_limits_the_parallel_compiles() {
        // The measured pre-v0.25.18 compile (5.6 GB) with 9 GB available on an
        // 8-CPU Mac: 8 GB holds one compile, so -p 1 — not the two overlapping
        // 5.6 GB compiles (11 GB) that crashed the machine.
        let j = decide(None, 5600 << 20, "(measured)", Some(9 * GB), 8).unwrap();
        assert_eq!(j.jobs, Some(1), "{j:?}");
        assert_eq!(j.arg().as_deref(), Some("-p=1"));
        // 2.2 GB compiles, 8 GB available: 7 GB holds 3 of 8.
        let j = decide(None, 2200 << 20, "(estimated)", Some(8 * GB), 8).unwrap();
        assert_eq!(j.jobs, Some(3), "{j:?}");
        assert!(j.reason.contains("holds 3 of 8"), "{j:?}");
        // Not even one fits: still one compile at a time, and it says why.
        let j = decide(None, 4 * GB, "(estimated)", Some(2 * GB), 8).unwrap();
        assert_eq!(j.jobs, Some(1));
        assert!(j.reason.contains("< one compile"), "{j:?}");
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
    fn the_peak_is_the_larger_of_the_record_and_the_estimate() {
        // A cache-hit build recorded only its link: the estimate stands.
        assert_eq!(per_compile_peak(Some(600 << 20), 2 * GB).0, 2 * GB);
        // A shape the estimate under-states: the measured record stands.
        let (p, why) = per_compile_peak(Some(5 * GB), 2 * GB);
        assert_eq!((p, why), (5 * GB, "(measured last build)"));
        assert_eq!(per_compile_peak(None, GB), (GB, "(estimated from main.go)"));
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

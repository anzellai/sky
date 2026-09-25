//! Serial or parallel: how a Sky.Spa build runs its two legs.
//!
//! A split build runs the backend (native) and frontend (wasm) legs as two child
//! `sky build`s. In parallel the wall-clock is max(backend, frontend); serially it
//! is their sum. But each leg is a whole Sky front end plus a Go toolchain, so
//! two at once can exhaust a small machine: a 2-core / 7 GB CI runner was killed
//! every time (exit 143), which is why `SKY_BUILD_SERIAL` exists.
//!
//! A leg needs two things in memory at the same time: its `sky` process (which
//! stays resident while its `go build` runs) and the Go compiles that build
//! runs. So the plan adds, per leg, the `sky` process's peak and the largest Go
//! tool process, and runs the legs in parallel only when the available memory
//! holds both legs plus a reserve, else serially.
//!   * Each leg's two peaks are estimated from the generated leg sources and
//!     raised to what this project's previous split build MEASURED when that
//!     was more (the child build records them in its `sky-out/`, see
//!     `project::go_jobs`). A record alone would under-state a build whose Go
//!     came from the cache.
//!   * In parallel, the plan also sets each leg's `go build -p`
//!     (`SKY_GO_BUILD_JOBS`) to the number of compiles per leg the memory left
//!     after both `sky` processes holds, unless the user set it.
//!   * Available memory is `project::memory::available_memory`.
//!   * Unknown memory → serial: a slower build is recoverable, an OOM-killed one
//!     is not.
//!   * `SKY_BUILD_SERIAL=1` forces serial, `SKY_BUILD_PARALLEL=1` forces parallel.

use project::go_jobs::{FRONT_PEAK_RECORD, GO_PEAK_RECORD};
use project::memory::{gib, recorded_peak, RESERVE_BYTES};
use std::path::Path;

pub use project::memory::available_memory;

/// The estimate of a leg's `sky` process: the stdlib before any app code, plus
/// a per-byte part of the generated leg source. Calibrated on the measured
/// 22k-line app (1.44 MB of leg source): its legs' `sky` processes peaked at
/// 737 MB and 690 MB (250 MB + 1.44 MB × 350 = 754 MB).
const FRONT_BASE_BYTES: u64 = 250 << 20;
const FRONT_BYTES_PER_SOURCE_BYTE: u64 = 350;

/// The estimate of a leg's largest Go tool process. Same app: the backend's
/// `main` compiled in 2.2 GB, the wasm frontend's in 1.1 GB
/// (500 MB + 1.44 MB × 1200 = 2.2 GB). Before v0.25.18's emitted-Go shape
/// change the backend compile was 5.6 GB; see `project::go_jobs`.
const GO_BASE_BYTES: u64 = 500 << 20;
const GO_BYTES_PER_SOURCE_BYTE: u64 = 1200;

/// What one leg needs in memory at once, in bytes.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct LegNeed {
    /// The leg's `sky` process (parse, typecheck, lower + emit).
    pub front: u64,
    /// The leg's largest Go tool process (`compile` / `link`).
    pub go: u64,
}

impl LegNeed {
    pub fn total(&self) -> u64 {
        self.front.saturating_add(self.go)
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Plan {
    pub parallel: bool,
    /// In parallel: the `go build -p` for each leg (`SKY_GO_BUILD_JOBS`), when
    /// fewer than one compile per CPU fit. `None`: each leg decides for itself.
    pub go_jobs: Option<usize>,
    /// One line for the build log and the `--timings` report.
    pub reason: String,
}

/// The decision, from its inputs. Pure, so every branch is unit-tested.
pub fn decide(
    force_serial: bool,
    force_parallel: bool,
    backend: LegNeed,
    frontend: LegNeed,
    peak_source: &str,
    available: Option<u64>,
    cpus: usize,
) -> Plan {
    if force_serial {
        return Plan {
            parallel: false,
            go_jobs: None,
            reason: "serial (SKY_BUILD_SERIAL is set)".to_string(),
        };
    }
    if force_parallel {
        return Plan {
            parallel: true,
            go_jobs: None,
            reason: "parallel (SKY_BUILD_PARALLEL is set)".to_string(),
        };
    }
    let legs = format!(
        "backend ~{} + {} Go, frontend ~{} + {} Go {peak_source}",
        gib(backend.front),
        gib(backend.go),
        gib(frontend.front),
        gib(frontend.go)
    );
    let need = backend
        .total()
        .saturating_add(frontend.total())
        .saturating_add(RESERVE_BYTES);
    match available {
        None => Plan {
            parallel: false,
            go_jobs: None,
            reason: format!("serial (available memory unknown; {legs})"),
        },
        Some(avail) if avail >= need => {
            // Compiles per leg: what is left after both `sky` processes and the
            // reserve, shared by one compile of each leg per slot.
            let left = avail
                .saturating_sub(RESERVE_BYTES)
                .saturating_sub(backend.front)
                .saturating_sub(frontend.front);
            let per_slot = backend.go.saturating_add(frontend.go).max(1);
            let slots = ((left / per_slot) as usize).max(1);
            let go_jobs = (slots < cpus.max(1)).then_some(slots);
            let jobs = match go_jobs {
                Some(n) => format!("; go build -p {n} per leg"),
                None => String::new(),
            };
            Plan {
                parallel: true,
                go_jobs,
                reason: format!(
                    "parallel ({} available >= {legs} + {} reserve{jobs})",
                    gib(avail),
                    gib(RESERVE_BYTES)
                ),
            }
        }
        Some(avail) => Plan {
            parallel: false,
            go_jobs: None,
            reason: format!(
                "serial ({} available < {legs} + {} reserve)",
                gib(avail),
                gib(RESERVE_BYTES)
            ),
        },
    }
}

/// The estimate of one leg from its generated source size.
pub fn estimate_from_source(leg_source_bytes: u64) -> LegNeed {
    LegNeed {
        front: FRONT_BASE_BYTES
            .saturating_add(leg_source_bytes.saturating_mul(FRONT_BYTES_PER_SOURCE_BYTE)),
        go: GO_BASE_BYTES.saturating_add(leg_source_bytes.saturating_mul(GO_BYTES_PER_SOURCE_BYTE)),
    }
}

/// A leg's need: per part, the larger of what its previous build measured and
/// the estimate from its `src/` (a no-change rebuild measures a Go cache hit,
/// not a compile). `true` when a measured record exceeded the estimate.
pub fn leg_need(leg_dir: &Path) -> (LegNeed, bool) {
    let out = leg_dir.join("sky-out");
    let est = estimate_from_source(sky_source_bytes(&leg_dir.join("src")));
    let front = recorded_peak(&out.join(FRONT_PEAK_RECORD)).unwrap_or(0);
    let go = recorded_peak(&out.join(GO_PEAK_RECORD)).unwrap_or(0);
    (
        LegNeed {
            front: front.max(est.front),
            go: go.max(est.go),
        },
        front > est.front || go > est.go,
    )
}

/// Total bytes of `.sky` files under `dir` (recursive; symlinks not followed).
pub fn sky_source_bytes(dir: &Path) -> u64 {
    let mut total = 0;
    let mut stack = vec![dir.to_path_buf()];
    while let Some(d) = stack.pop() {
        let Ok(rd) = std::fs::read_dir(&d) else {
            continue;
        };
        for e in rd.flatten() {
            let Ok(ft) = e.file_type() else { continue };
            if ft.is_dir() {
                stack.push(e.path());
            } else if ft.is_file() && e.path().extension().is_some_and(|x| x == "sky") {
                total += e.metadata().map(|m| m.len()).unwrap_or(0);
            }
        }
    }
    total
}

#[cfg(test)]
mod tests {
    use super::*;

    const GB: u64 = 1 << 30;
    const MB: u64 = 1 << 20;

    fn leg(front: u64, go: u64) -> LegNeed {
        LegNeed { front, go }
    }

    #[test]
    fn both_legs_plus_reserve_decide_parallel() {
        // The target app after v0.25.18, measured: backend 737 MB + 2.2 GB,
        // frontend 690 MB + 1.1 GB = 4.7 GB + 1 GB reserve. A 16 GB Mac with
        // 10 GB available runs the legs in parallel.
        let b = leg(737 * MB, 2200 * MB);
        let f = leg(690 * MB, 1100 * MB);
        let p = decide(false, false, b, f, "(measured)", Some(10 * GB), 8);
        assert!(p.parallel, "{p:?}");
        assert!(p.reason.starts_with("parallel ("), "{p:?}");
        // 10 GB - 1 GB - 1.4 GB leaves 7.6 GB = 2 slots of 3.3 GB: -p 2 per leg.
        assert_eq!(p.go_jobs, Some(2), "{p:?}");
        assert!(p.reason.contains("go build -p 2 per leg"), "{p:?}");
        // Exactly at the line is still parallel, with one compile per leg.
        let at = b.total() + f.total() + RESERVE_BYTES;
        let p = decide(false, false, b, f, "", Some(at), 8);
        assert!(p.parallel);
        assert_eq!(p.go_jobs, Some(1));
        // Plenty of memory: one compile per CPU fits, so Go keeps its default.
        let p = decide(false, false, b, f, "", Some(64 * GB), 8);
        assert_eq!((p.parallel, p.go_jobs), (true, None), "{p:?}");
    }

    #[test]
    fn too_little_memory_decides_serial() {
        // The same app before v0.25.18: the backend compile alone was 5.6 GB.
        let b = leg(737 * MB, 5600 * MB);
        let f = leg(690 * MB, 1100 * MB);
        // 0.7 + 5.5 + 0.7 + 1.1 + 1 GB reserve = 8.9 GB; 8 GB is not enough.
        let p = decide(false, false, b, f, "(measured)", Some(8 * GB), 8);
        assert!(!p.parallel, "{p:?}");
        assert!(p.reason.starts_with("serial ("), "{p:?}");
        assert_eq!(p.go_jobs, None);
        // One byte under the line.
        let at = b.total() + f.total() + RESERVE_BYTES;
        assert!(!decide(false, false, b, f, "", Some(at - 1), 8).parallel);
    }

    #[test]
    fn the_go_peak_is_counted_not_only_the_largest_process() {
        // Two legs whose largest process is 2 GB each: the old rule
        // (2 x 2 GB + 1 GB = 5 GB) said parallel at 5.5 GB; with each leg's
        // resident `sky` process (1.5 GB) the legs need 8 GB.
        let l = leg(1500 * MB, 2 * GB);
        let p = decide(false, false, l, l, "", Some(5500 * MB), 8);
        assert!(!p.parallel, "{p:?}");
    }

    #[test]
    fn unknown_memory_decides_serial() {
        let p = decide(
            false,
            false,
            leg(GB, GB),
            leg(GB, GB),
            "(estimated)",
            None,
            8,
        );
        assert!(!p.parallel);
        assert!(p.reason.contains("unknown"), "{p:?}");
    }

    #[test]
    fn the_env_overrides_win_serial_first() {
        let small = leg(GB / 2, GB / 2);
        let huge = leg(50 * GB, 50 * GB);
        assert!(!decide(true, false, small, small, "", Some(100 * GB), 8).parallel);
        assert!(decide(false, true, huge, huge, "", Some(GB), 8).parallel);
        assert!(decide(false, true, huge, huge, "", None, 8).parallel);
        // Both set: serial is the safe one.
        assert!(!decide(true, true, small, small, "", Some(100 * GB), 8).parallel);
    }

    #[test]
    fn the_estimate_matches_its_calibration_point() {
        // 1.44 MB of leg source: `sky` ~0.75 GB, Go ~2.2 GB.
        let est = estimate_from_source(1_437_559);
        assert!((700 * MB..800 * MB).contains(&est.front), "{est:?}");
        assert!((2_100 * MB..2_300 * MB).contains(&est.go), "{est:?}");
        assert_eq!(estimate_from_source(0), leg(250 * MB, 500 * MB));
    }

    #[test]
    fn a_leg_uses_its_records_else_the_estimate() {
        let d = std::env::temp_dir().join(format!("sky-legplan-{}", std::process::id()));
        let out = d.join("sky-out");
        std::fs::create_dir_all(&out).unwrap();
        std::fs::create_dir_all(d.join("src").join("sub")).unwrap();
        std::fs::write(d.join("src").join("a.sky"), "12345").unwrap();
        std::fs::write(d.join("src").join("sub").join("b.sky"), "678").unwrap();
        std::fs::write(d.join("src").join("c.go"), "ignored").unwrap();
        assert_eq!(sky_source_bytes(&d.join("src")), 8);
        // No records: estimated.
        assert_eq!(leg_need(&d), (estimate_from_source(8), false));
        // Records below the estimate (a cache-hit build): still the estimate.
        project::memory::record_peak(&out.join(GO_PEAK_RECORD), 300);
        project::memory::record_peak(&out.join(FRONT_PEAK_RECORD), 100);
        assert_eq!(leg_need(&d), (estimate_from_source(8), false));
        // A measured Go peak above the estimate wins for its part only.
        project::memory::record_peak(&out.join(GO_PEAK_RECORD), 9 * GB);
        let (need, measured) = leg_need(&d);
        assert!(measured);
        assert_eq!(need, leg(estimate_from_source(8).front, 9 * GB));
        let _ = std::fs::remove_dir_all(&d);
    }
}

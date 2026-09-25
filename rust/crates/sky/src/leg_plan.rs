//! Serial or parallel: how a Sky.Spa build runs its two legs.
//!
//! A split build runs the backend (native) and frontend (wasm) legs as two child
//! `sky build`s. In parallel the wall-clock is max(backend, frontend); serially it
//! is their sum. But each leg is a whole Sky front end plus a Go toolchain, and a
//! large app's leg peaks at several GB (a 22k-line app measured 6.9 GB max RSS),
//! so two at once can exhaust a small machine: a 2-core / 7 GB CI runner was
//! killed every time (exit 143), which is why `SKY_BUILD_SERIAL` exists.
//!
//! So the default is decided per build: run the legs in parallel only when the
//! available memory holds two leg peaks plus a reserve, else serially.
//!   * The per-leg peak is the one MEASURED on this project's previous split
//!     build (the children's max RSS, stored beside the backend's build output),
//!     else an estimate from the generated leg sources.
//!   * Available memory is `free + inactive + speculative` pages on macOS
//!     (`vm_stat`), `MemAvailable` on Linux, capped by the cgroup's remaining
//!     limit when one is set.
//!   * Unknown memory → serial: a slower build is recoverable, an OOM-killed one
//!     is not.
//!   * `SKY_BUILD_SERIAL=1` forces serial, `SKY_BUILD_PARALLEL=1` forces parallel.

use std::path::Path;

/// Memory kept free beyond the legs' peaks (the OS, the parent `sky`, an editor).
pub const RESERVE_BYTES: u64 = 1 << 30;

/// The estimate's fixed part: a leg's front end holds the whole stdlib before
/// any app code, and its `go build` / link needs a few hundred MB on its own.
const ESTIMATE_BASE_BYTES: u64 = 600 << 20;

/// The estimate's per-byte part: peak bytes per byte of generated leg source.
/// Calibrated on the measured peaks (max RSS of the leg process tree): a 1.44 MB
/// leg source peaked at 6.9 GB (600 MB + 1.44 MB × 4400 = 6.9 GB); smaller apps
/// fall below the line (a 485 kB leg measured 1.36 GB against 2.7 GB
/// estimated), so the estimate errs towards serial.
const ESTIMATE_BYTES_PER_SOURCE_BYTE: u64 = 4400;

/// File (inside the backend leg's `sky-out/`, which a restage keeps) that
/// records the last split build's measured per-leg peak, in bytes.
pub const PEAK_RECORD: &str = ".sky-leg-peak-bytes";

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Plan {
    pub parallel: bool,
    /// One line for the build log and the `--timings` report.
    pub reason: String,
}

fn gib(b: u64) -> String {
    format!("{:.1} GB", b as f64 / (1u64 << 30) as f64)
}

/// The decision, from its inputs. Pure, so every branch is unit-tested.
pub fn decide(
    force_serial: bool,
    force_parallel: bool,
    per_leg_peak: u64,
    peak_source: &str,
    available: Option<u64>,
) -> Plan {
    if force_serial {
        return Plan {
            parallel: false,
            reason: "serial (SKY_BUILD_SERIAL is set)".to_string(),
        };
    }
    if force_parallel {
        return Plan {
            parallel: true,
            reason: "parallel (SKY_BUILD_PARALLEL is set)".to_string(),
        };
    }
    let need = per_leg_peak.saturating_mul(2).saturating_add(RESERVE_BYTES);
    match available {
        None => Plan {
            parallel: false,
            reason: format!(
                "serial (available memory unknown; each leg ~{} {peak_source})",
                gib(per_leg_peak)
            ),
        },
        Some(avail) if avail >= need => Plan {
            parallel: true,
            reason: format!(
                "parallel ({} available >= 2 x ~{} per leg {peak_source} + {} reserve)",
                gib(avail),
                gib(per_leg_peak),
                gib(RESERVE_BYTES)
            ),
        },
        Some(avail) => Plan {
            parallel: false,
            reason: format!(
                "serial ({} available < 2 x ~{} per leg {peak_source} + {} reserve)",
                gib(avail),
                gib(per_leg_peak),
                gib(RESERVE_BYTES)
            ),
        },
    }
}

/// The per-leg peak estimate from the larger leg's generated source size.
pub fn estimate_from_source(largest_leg_source_bytes: u64) -> u64 {
    ESTIMATE_BASE_BYTES
        .saturating_add(largest_leg_source_bytes.saturating_mul(ESTIMATE_BYTES_PER_SOURCE_BYTE))
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

/// The recorded per-leg peak of the previous build, if any.
pub fn recorded_peak(record: &Path) -> Option<u64> {
    std::fs::read_to_string(record)
        .ok()?
        .trim()
        .parse::<u64>()
        .ok()
        .filter(|b| *b > 0)
}

/// `free + inactive + speculative` pages from `vm_stat` output, in bytes.
pub fn parse_vm_stat(text: &str) -> Option<u64> {
    let page: u64 = text
        .lines()
        .next()?
        .split("page size of ")
        .nth(1)?
        .split_whitespace()
        .next()?
        .parse()
        .ok()?;
    let pages = |key: &str| -> Option<u64> {
        let line = text.lines().find(|l| l.starts_with(key))?;
        line.split(':')
            .nth(1)?
            .trim()
            .trim_end_matches('.')
            .parse()
            .ok()
    };
    let free = pages("Pages free")?;
    let inactive = pages("Pages inactive").unwrap_or(0);
    let speculative = pages("Pages speculative").unwrap_or(0);
    Some((free + inactive + speculative) * page)
}

/// `MemAvailable` from `/proc/meminfo`, in bytes.
pub fn parse_meminfo_available(text: &str) -> Option<u64> {
    let line = text.lines().find(|l| l.starts_with("MemAvailable:"))?;
    let mut it = line.split_whitespace().skip(1);
    let n: u64 = it.next()?.parse().ok()?;
    match it.next() {
        Some("kB") | None => Some(n * 1024),
        _ => None,
    }
}

/// What a cgroup still allows: `limit - usage` (cgroup v2 `memory.max` /
/// `memory.current`, or v1 `limit_in_bytes` / `usage_in_bytes`). `None` when
/// there is no limit (`max`, or v1's near-u64::MAX "unlimited").
pub fn cgroup_headroom(limit: &str, usage: &str) -> Option<u64> {
    let limit: u64 = limit.trim().parse().ok()?;
    if limit >= (1u64 << 60) {
        return None;
    }
    let usage: u64 = usage.trim().parse().ok()?;
    Some(limit.saturating_sub(usage))
}

/// Available memory on this machine, or `None` when it cannot be read.
pub fn available_memory() -> Option<u64> {
    if cfg!(target_os = "macos") {
        let out = std::process::Command::new("vm_stat").output().ok()?;
        return parse_vm_stat(&String::from_utf8_lossy(&out.stdout));
    }
    if cfg!(target_os = "linux") {
        let host = std::fs::read_to_string("/proc/meminfo")
            .ok()
            .and_then(|t| parse_meminfo_available(&t));
        let read = |p: &str| std::fs::read_to_string(p).ok();
        let cgroup = match (
            read("/sys/fs/cgroup/memory.max"),
            read("/sys/fs/cgroup/memory.current"),
        ) {
            (Some(l), Some(u)) => cgroup_headroom(&l, &u),
            _ => match (
                read("/sys/fs/cgroup/memory/memory.limit_in_bytes"),
                read("/sys/fs/cgroup/memory/memory.usage_in_bytes"),
            ) {
                (Some(l), Some(u)) => cgroup_headroom(&l, &u),
                _ => None,
            },
        };
        return match (host, cgroup) {
            (Some(h), Some(c)) => Some(h.min(c)),
            (h, c) => h.or(c),
        };
    }
    None
}

/// The largest max RSS among this process's waited-for children (and their
/// descendants), in bytes: the per-leg peak of the legs just run.
#[cfg(unix)]
pub fn children_peak_rss() -> Option<u64> {
    use nix::sys::resource::{getrusage, UsageWho};
    let max = getrusage(UsageWho::RUSAGE_CHILDREN).ok()?.max_rss();
    let max = u64::try_from(max).ok().filter(|m| *m > 0)?;
    // macOS reports bytes, Linux kilobytes.
    Some(if cfg!(target_os = "macos") {
        max
    } else {
        max * 1024
    })
}

#[cfg(not(unix))]
pub fn children_peak_rss() -> Option<u64> {
    None
}

#[cfg(test)]
mod tests {
    use super::*;

    const GB: u64 = 1 << 30;

    #[test]
    fn two_leg_peaks_plus_reserve_decide_parallel() {
        // 16 GB machine with 10 GB available, legs of 2 GB: 2*2+1 = 5 <= 10.
        let p = decide(false, false, 2 * GB, "(measured)", Some(10 * GB));
        assert!(p.parallel, "{p:?}");
        assert!(p.reason.starts_with("parallel ("), "{p:?}");
        // Exactly at the line is still parallel.
        assert!(decide(false, false, 2 * GB, "", Some(5 * GB)).parallel);
    }

    #[test]
    fn too_little_memory_decides_serial() {
        // The measured 22k-line app: 6.9 GB per leg needs 14.8 GB; a 16 GB Mac
        // with 9 GB available builds serially.
        let p = decide(false, false, 6900 << 20, "(measured)", Some(9 * GB));
        assert!(!p.parallel, "{p:?}");
        assert!(p.reason.starts_with("serial ("), "{p:?}");
        // One byte under the line.
        assert!(!decide(false, false, 2 * GB, "", Some(5 * GB - 1)).parallel);
    }

    #[test]
    fn unknown_memory_decides_serial() {
        let p = decide(false, false, GB, "(estimated)", None);
        assert!(!p.parallel);
        assert!(p.reason.contains("unknown"), "{p:?}");
    }

    #[test]
    fn the_env_overrides_win_serial_first() {
        assert!(!decide(true, false, GB, "", Some(100 * GB)).parallel);
        assert!(decide(false, true, 50 * GB, "", Some(GB)).parallel);
        assert!(decide(false, true, 50 * GB, "", None).parallel);
        // Both set: serial is the safe one.
        assert!(!decide(true, true, GB, "", Some(100 * GB)).parallel);
    }

    #[test]
    fn the_estimate_matches_its_calibration_point() {
        // 1.44 MB of leg source measured a 6.9 GB peak.
        let est = estimate_from_source(1_437_559);
        assert!((6_500 << 20..7_300 << 20).contains(&est), "{est}");
        assert_eq!(estimate_from_source(0), 600 << 20);
    }

    #[test]
    fn vm_stat_counts_free_inactive_and_speculative_pages() {
        let text = "Mach Virtual Memory Statistics: (page size of 16384 bytes)\n\
                    Pages free:                               10000.\n\
                    Pages active:                            500000.\n\
                    Pages inactive:                           20000.\n\
                    Pages speculative:                         3000.\n\
                    Pages throttled:                              0.\n";
        assert_eq!(parse_vm_stat(text), Some((10000 + 20000 + 3000) * 16384));
        assert_eq!(parse_vm_stat("garbage"), None);
    }

    #[test]
    fn meminfo_and_cgroup_parse() {
        let mi = "MemTotal:       16384000 kB\nMemFree:          100000 kB\nMemAvailable:    8000000 kB\n";
        assert_eq!(parse_meminfo_available(mi), Some(8_000_000 * 1024));
        assert_eq!(parse_meminfo_available("MemTotal: 1 kB\n"), None);
        assert_eq!(
            cgroup_headroom("7516192768\n", "2516192768\n"),
            Some(5_000_000_000)
        );
        assert_eq!(cgroup_headroom("max\n", "123\n"), None);
        assert_eq!(cgroup_headroom("9223372036854771712", "1"), None);
    }

    #[test]
    fn a_recorded_peak_round_trips_and_rejects_junk() {
        let d = std::env::temp_dir().join(format!("sky-legplan-{}", std::process::id()));
        std::fs::create_dir_all(&d).unwrap();
        let f = d.join(PEAK_RECORD);
        std::fs::write(&f, "123456789\n").unwrap();
        assert_eq!(recorded_peak(&f), Some(123_456_789));
        std::fs::write(&f, "nope").unwrap();
        assert_eq!(recorded_peak(&f), None);
        std::fs::write(&f, "0").unwrap();
        assert_eq!(recorded_peak(&f), None);
        std::fs::write(d.join("a.sky"), "12345").unwrap();
        std::fs::create_dir_all(d.join("sub")).unwrap();
        std::fs::write(d.join("sub").join("b.sky"), "678").unwrap();
        std::fs::write(d.join("c.go"), "ignored").unwrap();
        assert_eq!(sky_source_bytes(&d), 8);
        let _ = std::fs::remove_dir_all(&d);
    }
}

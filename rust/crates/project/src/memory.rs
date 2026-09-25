//! Machine memory, and the per-project peak records the build keeps, for the
//! two decisions a build takes about memory: whether a Sky.Spa build runs its
//! legs in parallel (`sky`'s `leg_plan`), and how many packages `go build`
//! compiles at once (`go_jobs`).
//!
//! Available memory is `free + inactive + speculative` pages on macOS
//! (`vm_stat`), `MemAvailable` on Linux, capped by the cgroup's remaining limit
//! when one is set. `None` when it cannot be read — callers treat that as "no
//! room", because a slower build is recoverable and an out-of-memory one is not.

use std::path::Path;

/// Memory kept free beyond a build's peaks (the OS, an editor, the shell).
pub const RESERVE_BYTES: u64 = 1 << 30;

/// Version tag of the peak records. A peak measured by another compiler is not
/// this compiler's peak (v0.25.18 changed the emitted Go's shape and cut the
/// largest `go tool compile` from 5.6 GB to 2.2 GB), so a record written before
/// this tag, or under another tag, is ignored and re-measured. Bump it when the
/// emitted Go's compile cost changes.
pub const RECORD_EPOCH: &str = "sky-peak-v2";

/// `1.5 GB` — one decimal.
pub fn gib(b: u64) -> String {
    format!("{:.1} GB", b as f64 / (1u64 << 30) as f64)
}

/// A peak record's value in bytes, if the file holds a record of this epoch.
pub fn recorded_peak(record: &Path) -> Option<u64> {
    parse_record(&std::fs::read_to_string(record).ok()?)
}

/// `"<RECORD_EPOCH> <bytes>"` → bytes. Anything else (an older plain number,
/// another epoch, junk, zero) → `None`.
pub fn parse_record(text: &str) -> Option<u64> {
    let mut it = text.split_whitespace();
    if it.next()? != RECORD_EPOCH {
        return None;
    }
    it.next()?.parse::<u64>().ok().filter(|b| *b > 0)
}

/// Store `peak`, keeping the larger of it and the current record: a no-change
/// rebuild hits Go's cache and peaks far lower than an edit that recompiles, so
/// the latest figure alone would under-state the next build. Best-effort.
pub fn record_peak(record: &Path, peak: u64) {
    let keep = recorded_peak(record).map_or(peak, |p| p.max(peak));
    let _ = std::fs::write(record, format!("{RECORD_EPOCH} {keep}\n"));
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

/// The largest max RSS among this process's waited-for children and their
/// descendants, in bytes. After `go build` returns, that is the largest Go tool
/// process it ran (`compile` / `link` — the `go` command itself is small).
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
    fn a_record_of_another_epoch_is_ignored() {
        assert_eq!(parse_record("sky-peak-v2 123456789\n"), Some(123_456_789));
        // The pre-v0.25.18 format: a plain number, measured on the old shape.
        assert_eq!(parse_record("7340032000"), None);
        assert_eq!(parse_record("sky-peak-v1 7340032000"), None);
        assert_eq!(parse_record("sky-peak-v2 nope"), None);
        assert_eq!(parse_record("sky-peak-v2 0"), None);
        assert_eq!(parse_record(""), None);
    }

    #[test]
    fn a_recorded_peak_keeps_the_larger_value() {
        let d = std::env::temp_dir().join(format!("sky-memrec-{}", std::process::id()));
        std::fs::create_dir_all(&d).unwrap();
        let f = d.join("peak");
        // An old-format file is replaced, not kept.
        std::fs::write(&f, "9999999999").unwrap();
        record_peak(&f, 100);
        assert_eq!(recorded_peak(&f), Some(100));
        record_peak(&f, 50);
        assert_eq!(recorded_peak(&f), Some(100));
        record_peak(&f, 300);
        assert_eq!(recorded_peak(&f), Some(300));
        let _ = std::fs::remove_dir_all(&d);
    }
}

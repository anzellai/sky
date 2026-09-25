//! `SKY_TIMINGS=1` (or `sky build --timings`) — a per-phase wall-clock and
//! peak-memory report.
//!
//! A build records one line per phase it runs (parse, typecheck, lower + emit,
//! `go build`, the Sky.Spa split, the wasm bundle, precompression, …) and, when
//! timings are enabled, prints the table to stderr as the process exits. A
//! Sky.Spa build runs its backend and frontend legs as child `sky build`
//! processes; they inherit `SKY_TIMINGS`, so each leg prints its own table under
//! its own `== … ==` header, and the parent's table carries the leg wall-clock.
//!
//! Each line also carries the process's peak resident set size when the phase
//! closed. It is a high-water mark, so the first phase whose figure jumps is the
//! phase that allocated the memory.
//!
//! Recording is always cheap (a mutex push and one `getrusage` per phase, a
//! handful per build), so the phase guards stay in the code unconditionally;
//! only the report is gated.

use std::sync::Mutex;
use std::time::{Duration, Instant};

/// One recorded phase: its label, wall-clock, and the process peak RSS (bytes)
/// sampled when the phase closed (`None` where the platform cannot report it).
type PhaseRow = (String, Duration, Option<u64>);

static PHASES: Mutex<Vec<PhaseRow>> = Mutex::new(Vec::new());
static NOTES: Mutex<Vec<String>> = Mutex::new(Vec::new());

/// True when the user asked for the phase report (`SKY_TIMINGS` set to anything
/// but empty / `0` / `false` / `no`).
pub fn enabled() -> bool {
    std::env::var("SKY_TIMINGS")
        .map(|v| !matches!(v.trim(), "" | "0" | "false" | "no"))
        .unwrap_or(false)
}

/// Record one finished phase.
pub fn record(label: &str, d: Duration) {
    let rss = peak_rss_bytes();
    if let Ok(mut v) = PHASES.lock() {
        v.push((label.to_string(), d, rss));
    }
}

/// The process's peak resident set size so far, in bytes. `getrusage`'s
/// `ru_maxrss` is bytes on macOS and KiB on Linux; this normalises to bytes.
pub fn peak_rss_bytes() -> Option<u64> {
    #[cfg(unix)]
    {
        let u = nix::sys::resource::getrusage(nix::sys::resource::UsageWho::RUSAGE_SELF).ok()?;
        let raw = u64::try_from(u.max_rss()).ok()?;
        if cfg!(target_os = "macos") {
            Some(raw)
        } else {
            Some(raw.saturating_mul(1024))
        }
    }
    #[cfg(not(unix))]
    {
        None
    }
}

fn fmt_mb(b: Option<u64>) -> String {
    match b {
        Some(b) => format!("{:>6} MB", b / (1024 * 1024)),
        None => "     - MB".to_string(),
    }
}

/// Record a decision the build took (printed under the phase table).
pub fn note(text: impl Into<String>) {
    if let Ok(mut v) = NOTES.lock() {
        v.push(text.into());
    }
}

/// A running phase; records its elapsed time when dropped (or on [`Phase::end`]).
pub struct Phase {
    label: &'static str,
    start: Instant,
    done: bool,
}

impl Phase {
    /// Close the phase now (instead of at scope end).
    pub fn end(mut self) {
        self.finish();
    }
    fn finish(&mut self) {
        if !self.done {
            self.done = true;
            record(self.label, self.start.elapsed());
        }
    }
}

impl Drop for Phase {
    fn drop(&mut self) {
        self.finish();
    }
}

/// Start timing a phase.
pub fn phase(label: &'static str) -> Phase {
    Phase {
        label,
        start: Instant::now(),
        done: false,
    }
}

/// Render the recorded phases as a table (empty string when none were recorded).
pub fn render(scope: &str, total: Duration) -> String {
    let v = match PHASES.lock() {
        Ok(v) => v.clone(),
        Err(_) => return String::new(),
    };
    if v.is_empty() {
        return String::new();
    }
    let width = v.iter().map(|(l, _, _)| l.len()).max().unwrap_or(0).max(12);
    let mut out = format!("sky timings ({scope}):\n");
    out.push_str(&format!(
        "  {:<width$}  {:>9}  {:>9}\n",
        "phase",
        "wall",
        "peak RSS",
        width = width
    ));
    for (label, d, rss) in &v {
        out.push_str(&format!(
            "  {label:<width$}  {:>8.2}s  {}\n",
            d.as_secs_f64(),
            fmt_mb(*rss),
            width = width
        ));
    }
    out.push_str(&format!(
        "  {:<width$}  {:>8.2}s  {}\n",
        "total (wall)",
        total.as_secs_f64(),
        fmt_mb(peak_rss_bytes()),
        width = width
    ));
    if let Ok(notes) = NOTES.lock() {
        for n in notes.iter() {
            out.push_str(&format!("  note: {n}\n"));
        }
    }
    out
}

/// Print the table to stderr when timings are enabled.
pub fn report(scope: &str, total: Duration) {
    if !enabled() {
        return;
    }
    let s = render(scope, total);
    if !s.is_empty() {
        eprint!("\n{s}");
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn phases_render_in_order_with_total() {
        {
            let _p = phase("alpha");
        }
        record("beta", Duration::from_millis(1500));
        note("legs: serial (test)");
        let s = render("unit", Duration::from_secs(2));
        let a = s.find("alpha").expect("alpha rendered");
        let b = s.find("beta").expect("beta rendered");
        assert!(a < b, "phases keep their recording order:\n{s}");
        assert!(s.contains("1.50s"), "{s}");
        assert!(s.contains("total (wall)"), "{s}");
        assert!(s.contains("note: legs: serial (test)"), "{s}");
        assert!(s.contains("peak RSS"), "{s}");
    }

    #[cfg(unix)]
    #[test]
    fn peak_rss_is_reported_in_bytes() {
        // A running test binary is at least 1 MB resident and far below 1 TB;
        // a KiB/bytes unit slip lands outside that window.
        let b = peak_rss_bytes().expect("getrusage works on unix");
        assert!(b > 1024 * 1024 && b < (1u64 << 40), "{b}");
    }
}

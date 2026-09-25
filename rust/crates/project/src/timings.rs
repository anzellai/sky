//! `SKY_TIMINGS=1` (or `sky build --timings`) — a per-phase wall-clock report.
//!
//! A build records one line per phase it runs (parse, typecheck, lower + emit,
//! `go build`, the Sky.Spa split, the wasm bundle, precompression, …) and, when
//! timings are enabled, prints the table to stderr as the process exits. A
//! Sky.Spa build runs its backend and frontend legs as child `sky build`
//! processes; they inherit `SKY_TIMINGS`, so each leg prints its own table under
//! its own `== … ==` header, and the parent's table carries the leg wall-clock.
//!
//! Recording is always cheap (a mutex push per phase, a handful per build), so
//! the phase guards stay in the code unconditionally; only the report is gated.

use std::sync::Mutex;
use std::time::{Duration, Instant};

static PHASES: Mutex<Vec<(String, Duration)>> = Mutex::new(Vec::new());

/// True when the user asked for the phase report (`SKY_TIMINGS` set to anything
/// but empty / `0` / `false` / `no`).
pub fn enabled() -> bool {
    std::env::var("SKY_TIMINGS")
        .map(|v| !matches!(v.trim(), "" | "0" | "false" | "no"))
        .unwrap_or(false)
}

/// Record one finished phase.
pub fn record(label: &str, d: Duration) {
    if let Ok(mut v) = PHASES.lock() {
        v.push((label.to_string(), d));
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
    let width = v.iter().map(|(l, _)| l.len()).max().unwrap_or(0).max(5);
    let mut out = format!("sky timings ({scope}):\n");
    for (label, d) in &v {
        out.push_str(&format!(
            "  {label:<width$}  {:>8.2}s\n",
            d.as_secs_f64(),
            width = width
        ));
    }
    out.push_str(&format!(
        "  {:<width$}  {:>8.2}s\n",
        "total (wall)",
        total.as_secs_f64(),
        width = width
    ));
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
        let s = render("unit", Duration::from_secs(2));
        let a = s.find("alpha").expect("alpha rendered");
        let b = s.find("beta").expect("beta rendered");
        assert!(a < b, "phases keep their recording order:\n{s}");
        assert!(s.contains("1.50s"), "{s}");
        assert!(s.contains("total (wall)"), "{s}");
    }
}

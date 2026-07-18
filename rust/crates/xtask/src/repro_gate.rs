//! `xtask repro` — the reproducibility gate (doc 11 §3, law L4).
//!
//! Determinism is an *invariant, tested*, not a promise. The gate empirically
//! validates that "the same source compiles to byte-identical Go on any run":
//! it emits each corpus example's Go `N` times, **each emission in a fresh
//! process**, and asserts the `N` outputs are byte-identical.
//!
//! Why fresh processes: Rust's default `HashMap`/`HashSet` seed their hasher
//! from process-random `RandomState`, so any accidental map/set iteration that
//! reaches emitted output re-orders across processes and surfaces here as a
//! byte diff. The standard library is the adversary — we do not hand-construct
//! it (doc 11 §3).
//!
//! Determinism ≠ correctness: a byte-stable-but-wrong emitter (e.g. lexically
//! sorted fields) passes this gate and fails the oracle-parity gate. The two
//! are orthogonal; both are required. This gate proves *stable*, not *correct*.
//!
//! Gate set: the examples that currently BUILD (emit valid Go that `go build`
//! accepts), the same set `build-run --all` reports. Non-building examples are
//! still reported (for transparency) but do not count toward the gate.
//!
//! Usage:
//!   xtask repro                       # whole corpus, N=3 fresh emissions each
//!   xtask repro --seeds N             # N fresh emissions (N>=2; default 3)
//!   xtask repro --only=NAME[,NAME…]   # filter to named examples
//!   xtask repro -v                    # print the diverging lines on a failure
//!   xtask repro --no-build            # skip `go build`; gate every emitting example
//!   xtask repro --emit-worker=NAME    # (internal) single fresh emission → stdout

use project::{build_example, emit_example_source, BuildOptions};
use std::path::{Path, PathBuf};
use std::process::Command;

/// Default number of fresh-process emissions per example.
const DEFAULT_SEEDS: usize = 3;

/// Examples known to need an unported Go-FFI surface — they don't emit valid Go
/// yet, so they're excluded from the *build* set exactly as in `build-run`.
const FFI_BLOCKED: &[&str] = &["11-fyne-stopwatch", "13-skyshop"];

pub fn run(args: &[String], root: &Path) -> i32 {
    // ---- internal worker mode: one fresh emission, bytes → stdout ----
    if let Some(name) = args.iter().find_map(|a| a.strip_prefix("--emit-worker=")) {
        return emit_worker(root, name);
    }

    let seeds: usize = args
        .iter()
        .position(|a| a == "--seeds")
        .and_then(|i| args.get(i + 1))
        .and_then(|s| s.parse().ok())
        .unwrap_or(DEFAULT_SEEDS)
        .max(2);
    let only: Option<Vec<String>> = args
        .iter()
        .find_map(|a| a.strip_prefix("--only="))
        .map(|s| {
            s.split(',')
                .map(|x| x.trim().to_string())
                .filter(|x| !x.is_empty())
                .collect()
        });
    let verbose = args.iter().any(|a| a == "-v" || a == "--verbose");
    let no_build = args.iter().any(|a| a == "--no-build");

    let names: Vec<String> = match &only {
        Some(n) => n.clone(),
        None => corpus(root),
    };

    let worker = std::env::current_exe().unwrap_or_else(|_| PathBuf::from("xtask"));

    let mut rows = Vec::new();
    for name in &names {
        let dir = root.join("examples").join(name);
        if !dir.is_dir() {
            continue;
        }
        rows.push(check_one(&worker, root, &dir, name, seeds, no_build, verbose));
    }

    print_table(&rows, seeds);
    gate_result(&rows)
}

/// One fresh emission of an example's Go source, written raw to stdout. Runs in
/// a subprocess (spawned by the parent gate) so its `HashMap`/`HashSet` seed is
/// process-fresh. Exit 0 on emit; 2 (+ note on stderr) on any non-emit outcome.
fn emit_worker(root: &Path, name: &str) -> i32 {
    let dir = root.join("examples").join(name);
    match emit_example_source(root, &dir) {
        Ok(source) => {
            use std::io::Write;
            let _ = std::io::stdout().write_all(source.as_bytes());
            0
        }
        Err(note) => {
            eprintln!("emit-worker {name}: {note}");
            2
        }
    }
}

struct Row {
    name: String,
    /// go build accepted the emitted Go (the gate set), or `None` when `--no-build`.
    builds: Option<bool>,
    /// number of successful fresh emissions captured.
    samples: usize,
    /// all captured emissions were byte-identical.
    stable: bool,
    /// the first line at which two samples diverged (1-based), when unstable.
    first_diff: Option<(usize, String)>,
    note: String,
}

impl Row {
    /// This example counts toward the gate denominator: it emitted at least two
    /// samples AND (unless `--no-build`) `go build` accepted it.
    fn in_gate(&self) -> bool {
        self.samples >= 2 && self.builds.unwrap_or(true)
    }
}

fn check_one(
    worker: &Path,
    root: &Path,
    dir: &Path,
    name: &str,
    seeds: usize,
    no_build: bool,
    verbose: bool,
) -> Row {
    // FFI-blocked examples don't emit valid Go yet — record + skip (not in gate).
    if FFI_BLOCKED.contains(&name) {
        return Row {
            name: name.into(),
            builds: Some(false),
            samples: 0,
            stable: true,
            first_diff: None,
            note: "FFI-blocked".into(),
        };
    }

    // ---- N fresh-process emissions ----
    let mut samples: Vec<String> = Vec::with_capacity(seeds);
    let mut note = String::new();
    for i in 0..seeds {
        match emit_once(worker, root, name) {
            Ok(bytes) => samples.push(bytes),
            Err(e) => {
                if note.is_empty() {
                    note = format!("emit {i} failed: {e}");
                }
            }
        }
    }

    // ---- byte-stability across the samples ----
    let (stable, first_diff) = compare_samples(&samples);
    if verbose && !stable {
        if let Some((ln, _)) = &first_diff {
            eprintln!("  [{name}] first divergence at line {ln}:");
            report_divergence(&samples, *ln);
        }
    }

    // ---- does it build? (the gate set) ----
    let builds = if no_build {
        None
    } else {
        Some(go_builds(root, dir, name))
    };

    Row {
        name: name.into(),
        builds,
        samples: samples.len(),
        stable,
        first_diff,
        note,
    }
}

/// Spawn one fresh worker process, `timeout`-bounded, capturing its stdout as
/// the emitted Go source. A fresh process → a fresh `RandomState` seed.
fn emit_once(worker: &Path, root: &Path, name: &str) -> Result<String, String> {
    let out = Command::new("timeout")
        .arg("120")
        .arg(worker)
        .arg("repro")
        .arg(format!("--emit-worker={name}"))
        .current_dir(root)
        .output()
        .map_err(|e| format!("spawn: {e}"))?;
    if !out.status.success() {
        let err = String::from_utf8_lossy(&out.stderr);
        return Err(err.lines().next().unwrap_or("worker failed").to_string());
    }
    Ok(String::from_utf8_lossy(&out.stdout).into_owned())
}

/// Compare N captured emissions. `(true, None)` iff all identical; else
/// `(false, Some((line, kind)))` naming the first line where any sample diverges
/// from the first.
fn compare_samples(samples: &[String]) -> (bool, Option<(usize, String)>) {
    let Some(first) = samples.first() else {
        return (true, None); // no samples — handled as note elsewhere
    };
    let mut best: Option<(usize, String)> = None;
    for other in &samples[1..] {
        if other == first {
            continue;
        }
        if let Some((ln, kind)) = first_diff_line(first, other) {
            match &best {
                Some((b, _)) if *b <= ln => {}
                _ => best = Some((ln, kind)),
            }
        }
    }
    (best.is_none(), best)
}

/// The 1-based line number of the first difference between two texts, plus a
/// short description of the divergence (the two lines, truncated).
fn first_diff_line(a: &str, b: &str) -> Option<(usize, String)> {
    let mut al = a.lines();
    let mut bl = b.lines();
    let mut n = 0;
    loop {
        n += 1;
        match (al.next(), bl.next()) {
            (Some(x), Some(y)) if x == y => continue,
            (None, None) => return None,
            (x, y) => {
                let xs = x.unwrap_or("<eof>");
                let ys = y.unwrap_or("<eof>");
                return Some((n, format!("{:?} vs {:?}", trunc(xs, 48), trunc(ys, 48))));
            }
        }
    }
}

fn report_divergence(samples: &[String], line: usize) {
    for (i, s) in samples.iter().enumerate() {
        let l = s.lines().nth(line - 1).unwrap_or("<eof>");
        eprintln!("    sample {i}: {}", trunc(l, 80));
    }
}

fn trunc(s: &str, n: usize) -> String {
    s.chars().take(n).collect()
}

/// Run `build_example` once to decide whether the emitted Go actually builds.
/// This is the same driver `build-run` uses; it writes `sky-out-rust/` and runs
/// `go build`. Its own (parent-process) emission is intentionally not counted as
/// a repro sample — the samples all come from fresh worker processes.
fn go_builds(root: &Path, dir: &Path, name: &str) -> bool {
    let opts = BuildOptions {
        repo_root: root.to_path_buf(),
        example_dir: dir.to_path_buf(),
        out_dir_name: "sky-out-rust".into(),
        run: false,
        stdin: None,
    };
    // build_example never panics + returns a report; go build is the signal.
    let _ = name;
    build_example(&opts).go_build_ok
}

fn corpus(root: &Path) -> Vec<String> {
    let mut ds: Vec<String> = std::fs::read_dir(root.join("examples"))
        .map(|rd| {
            rd.filter_map(|e| e.ok())
                .filter(|e| e.path().is_dir())
                .filter_map(|e| e.file_name().to_str().map(String::from))
                .filter(|n| root.join("examples").join(n).join("src").is_dir())
                .filter(|n| n != "simple" && n != "test_pkg")
                .collect()
        })
        .unwrap_or_default();
    ds.sort();
    ds
}

fn print_table(rows: &[Row], seeds: usize) {
    let w = rows.iter().map(|r| r.name.len()).max().unwrap_or(8).max(8);
    println!("reproducibility gate — {seeds} fresh emissions per example (L4)\n");
    println!(
        "{:<w$}  {:>6}  {:>7}  {:>16}  DETAIL",
        "EXAMPLE",
        "BUILDS",
        "SAMPLES",
        "STABILITY",
        w = w
    );
    println!("{}", "-".repeat(w + 50));

    let mut stable_in_gate = 0usize;
    let mut gate_denom = 0usize;
    for r in rows {
        let builds = match r.builds {
            Some(true) => "yes",
            Some(false) => "no",
            None => "-",
        };
        let stability = if r.samples == 0 {
            "no-emit"
        } else if r.stable {
            "STABLE"
        } else {
            "NONDETERMINISTIC"
        };
        let detail = if let Some((ln, kind)) = &r.first_diff {
            format!("line {ln}: {kind}")
        } else if !r.note.is_empty() {
            r.note.clone()
        } else {
            "-".into()
        };
        if r.in_gate() {
            gate_denom += 1;
            if r.stable {
                stable_in_gate += 1;
            }
        }
        println!(
            "{:<w$}  {:>6}  {:>7}  {:>16}  {}",
            r.name,
            builds,
            r.samples,
            stability,
            detail,
            w = w
        );
    }

    println!("{}", "-".repeat(w + 50));
    // also surface emitting-but-not-building stability (informational)
    let emit_stable = rows.iter().filter(|r| r.samples >= 2 && r.stable).count();
    let emit_total = rows.iter().filter(|r| r.samples >= 2).count();
    println!(
        "TOTALS  |  byte-stable {stable_in_gate}/{gate_denom} building examples  |  \
         byte-stable {emit_stable}/{emit_total} emitting examples"
    );
}

/// Gate: every building example must be byte-stable across the fresh emissions.
fn gate_result(rows: &[Row]) -> i32 {
    let failing: Vec<&Row> = rows.iter().filter(|r| r.in_gate() && !r.stable).collect();
    if failing.is_empty() {
        // guard against a vacuous pass (nothing emitted at all).
        let any = rows.iter().any(|r| r.in_gate());
        if any {
            println!("REPRO GATE: PASS  (all building examples byte-stable)");
            0
        } else {
            println!("REPRO GATE: INCONCLUSIVE  (no building example produced samples)");
            1
        }
    } else {
        println!(
            "REPRO GATE: FAIL  ({} building example(s) nondeterministic: {})",
            failing.len(),
            failing
                .iter()
                .map(|r| r.name.as_str())
                .collect::<Vec<_>>()
                .join(", ")
        );
        1
    }
}

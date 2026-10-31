//! `xtask perf-gate` — the collection performance gate.
//!
//! Each program under `tests/perf/<name>/` is a small Sky CLI that takes a
//! size `n` as its first argument, does one collection workload of that size
//! and prints a result line. The gate builds every program with the compiler
//! linked into this binary (the in-process `project::build_example`, so the
//! compiler under test is always the tree's own, never a stale `sky-out/sky`),
//! then runs each one at `n` and at `4n` and checks four things:
//!
//! 1. **Growth ratio.** The minimum wall time of [`RUNS`] runs at `4n`, divided
//!    by the minimum at `n`, is at most [`MAX_GROWTH`] (6). Linear growth gives
//!    about 4 and `n log n` about 4.4; quadratic gives 16. The ratio is the
//!    same on a fast and a slow machine, so it catches a return to O(n²)
//!    anywhere.
//! 2. **Allocations per element.** The runtime prints `runtime.MemStats` at
//!    exit when `SKY_RT_MEMSTATS_ON_EXIT=1` (`runtime-go/rt/perf_hook.go`). The
//!    gate divides the DIFFERENCE of `mallocs` between the `4n` and the `n`
//!    run by the difference of their element counts, so the fixed start-up
//!    cost cancels. Each program has a budget. Allocation counts are
//!    deterministic, so a budget is a ratchet, not a flaky timing.
//! 3. **No erased cons on a typed tail.** The same exit line counts calls of
//!    the erased `rt.List_cons` with a typed slice tail (the O(n) reflect
//!    widen). It must be zero.
//! 4. **A loose absolute ceiling.** Every run at `4n` finishes inside
//!    [`CEILING_S`] (3 s, for shared CI runners). `--strict` uses the
//!    request's 1 s, for a labelled reference machine. A run that passes the
//!    ceiling is killed and counts as red.
//!
//! Program 4 (`tokenise`) also runs a `regex` partner on the same input and
//! requires the character-by-character tokeniser to stay within
//! [`MAX_PARTNER_RATIO`] (10x) of `Regex.findAll`.
//!
//! The gate never weakens itself to pass: a program that does not build, a
//! wrong result line, a timeout or a missing exit line is RED.

use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

use project::{build_example, BuildOptions};

/// Runs per size. The minimum is taken.
const RUNS: usize = 5;
/// The largest allowed `t(4n) / t(n)`.
pub const MAX_GROWTH: f64 = 6.0;
/// The ceiling for one run at `4n` on shared CI.
pub const CEILING_S: f64 = 3.0;
/// The ceiling under `--strict` (the request's "under 1 s").
pub const STRICT_CEILING_S: f64 = 1.0;
/// The largest allowed `t(tokenise) / t(regex)` at `4n`.
pub const MAX_PARTNER_RATIO: f64 = 10.0;

/// One perf program.
#[derive(Clone, Copy, Debug)]
pub struct Program {
    /// Directory under `tests/perf/`.
    pub name: &'static str,
    /// What it measures, for the report.
    pub what: &'static str,
    /// The base size; the gate also runs `4 * n`.
    pub n: u64,
    /// Elements processed per unit of `n` (8 for the eight-Task program).
    pub elems_per_n: u64,
    /// Extra arguments after `n`.
    pub extra_args: &'static [&'static str],
    /// The allowed mallocs per element (difference quotient, see module doc).
    pub malloc_budget: f64,
    /// The expected stdout for size `n`.
    pub expect: fn(u64) -> String,
    /// A partner run on the same input (program 4's regex).
    pub partner: Option<&'static [&'static str]>,
}

fn expect_n(n: u64) -> String {
    n.to_string()
}
fn expect_8n(n: u64) -> String {
    (8 * n).to_string()
}
fn expect_dict(n: u64) -> String {
    format!("{} {}", (n / 10).max(1), n)
}
fn expect_string(n: u64) -> String {
    format!("{} same", (n / 16) * 16)
}
fn expect_tokens(n: u64) -> String {
    // the 16-code-point sample holds three words
    ((n / 16) * 3).to_string()
}

/// The six programs of design 1e (as revised by the review).
pub const PROGRAMS: &[Program] = &[
    Program {
        name: "cons-reverse",
        what: "cons 1e6 Ints onto an accumulator, then List.reverse",
        n: 250_000,
        elems_per_n: 1,
        extra_args: &[],
        malloc_budget: 0.5,
        expect: expect_n,
        partner: None,
    },
    Program {
        name: "dict-counts",
        what: "List.foldl 1e6 Ints into a Dict of counts with 1e5 distinct keys",
        n: 250_000,
        elems_per_n: 1,
        extra_args: &[],
        malloc_budget: 8.0,
        expect: expect_dict,
        partner: None,
    },
    Program {
        name: "string-roundtrip",
        what: "String.toList then String.fromList over 4M mixed code points",
        n: 1_048_576,
        elems_per_n: 1,
        extra_args: &[],
        malloc_budget: 0.05,
        expect: expect_string,
        partner: None,
    },
    Program {
        name: "tokenise",
        what: "tokenise 4M code points with String.uncons + cons, vs Regex.findAll",
        n: 1_048_576,
        elems_per_n: 1,
        extra_args: &["uncons"],
        malloc_budget: 1.5,
        expect: expect_tokens,
        partner: Some(&["regex"]),
    },
    Program {
        name: "cons-unit",
        what: "cons 1e6 units onto a List () (zero-size elements)",
        n: 250_000,
        elems_per_n: 1,
        extra_args: &[],
        malloc_budget: 0.5,
        expect: expect_n,
        partner: None,
    },
    Program {
        name: "concurrent-cons",
        what: "eight parallel Tasks, each consing its own 1e5 list",
        n: 25_000,
        elems_per_n: 8,
        extra_args: &[],
        malloc_budget: 0.5,
        expect: expect_8n,
        partner: None,
    },
];

/// The counters from the runtime's exit line.
#[derive(Clone, Copy, Debug, Default, PartialEq)]
pub struct MemStats {
    pub mallocs: u64,
    pub total_alloc: u64,
    pub erased_typed_cons: u64,
}

/// Parse the `{"skyMemStats":{…}}` line out of a program's stderr.
pub fn parse_memstats(stderr: &str) -> Option<MemStats> {
    let line = stderr
        .lines()
        .rev()
        .find(|l| l.trim_start().starts_with("{\"skyMemStats\""))?;
    let v: serde_json::Value = serde_json::from_str(line.trim()).ok()?;
    let s = v.get("skyMemStats")?;
    Some(MemStats {
        mallocs: s.get("mallocs")?.as_u64()?,
        total_alloc: s.get("totalAlloc")?.as_u64()?,
        erased_typed_cons: s.get("erasedTypedCons")?.as_u64()?,
    })
}

/// Mallocs per element between two sizes. The start-up cost cancels.
pub fn mallocs_per_element(small: MemStats, big: MemStats, e_small: u64, e_big: u64) -> f64 {
    if e_big <= e_small {
        return f64::INFINITY;
    }
    let d = big.mallocs.saturating_sub(small.mallocs) as f64;
    d / (e_big - e_small) as f64
}

/// One measured run.
#[derive(Clone, Debug)]
struct RunResult {
    secs: f64,
    stdout: String,
    stats: Option<MemStats>,
    timed_out: bool,
    failed: Option<String>,
}

fn run_once(bin: &Path, cwd: &Path, args: &[String], ceiling: f64) -> RunResult {
    let started = Instant::now();
    let child = Command::new(bin)
        .args(args)
        .current_dir(cwd)
        .env("SKY_RT_MEMSTATS_ON_EXIT", "1")
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn();
    let mut child = match child {
        Ok(c) => c,
        Err(e) => {
            return RunResult {
                secs: 0.0,
                stdout: String::new(),
                stats: None,
                timed_out: false,
                failed: Some(format!("spawn {}: {e}", bin.display())),
            }
        }
    };
    // Kill at a little past the ceiling: the run is red either way, and a
    // quadratic program would otherwise run for minutes.
    let limit = Duration::from_secs_f64(ceiling * 1.5 + 0.5);
    loop {
        match child.try_wait() {
            Ok(Some(_)) => break,
            Ok(None) => {
                if started.elapsed() > limit {
                    let _ = child.kill();
                    let _ = child.wait();
                    return RunResult {
                        secs: started.elapsed().as_secs_f64(),
                        stdout: String::new(),
                        stats: None,
                        timed_out: true,
                        failed: None,
                    };
                }
                std::thread::sleep(Duration::from_millis(2));
            }
            Err(e) => {
                return RunResult {
                    secs: 0.0,
                    stdout: String::new(),
                    stats: None,
                    timed_out: false,
                    failed: Some(format!("wait: {e}")),
                }
            }
        }
    }
    let secs = started.elapsed().as_secs_f64();
    let out = child.wait_with_output();
    match out {
        Ok(o) => {
            let stdout = String::from_utf8_lossy(&o.stdout).trim().to_string();
            let stderr = String::from_utf8_lossy(&o.stderr).to_string();
            let failed = if o.status.success() {
                None
            } else {
                Some(format!(
                    "exit {:?}: {}",
                    o.status.code(),
                    stderr.lines().take(3).collect::<Vec<_>>().join(" | ")
                ))
            };
            RunResult {
                secs,
                stdout,
                stats: parse_memstats(&stderr),
                timed_out: false,
                failed,
            }
        }
        Err(e) => RunResult {
            secs,
            stdout: String::new(),
            stats: None,
            timed_out: false,
            failed: Some(format!("output: {e}")),
        },
    }
}

/// Best of [`RUNS`] at one size. Stops early on a timeout or a failure.
fn best_of(bin: &Path, cwd: &Path, args: &[String], ceiling: f64) -> RunResult {
    let mut best: Option<RunResult> = None;
    for _ in 0..RUNS {
        let r = run_once(bin, cwd, args, ceiling);
        if r.timed_out || r.failed.is_some() {
            return r;
        }
        best = match best {
            Some(b) if b.secs <= r.secs => Some(b),
            _ => Some(r),
        };
    }
    best.expect("RUNS > 0")
}

/// One program's verdict.
#[derive(Debug)]
pub struct Verdict {
    pub name: &'static str,
    pub problems: Vec<String>,
    pub report: String,
}

fn build(root: &Path, dir: &Path) -> Result<PathBuf, String> {
    let opts = BuildOptions {
        repo_root: root.to_path_buf(),
        example_dir: dir.to_path_buf(),
        out_dir_name: "sky-out".into(),
        out_dir_abs: None,
        run: false,
        stdin: None,
        entry_module: None,
        progress: false,
        embed_bundle: None,
        wasm: false,
    };
    let rep = build_example(&opts);
    if !rep.emitted {
        return Err(format!("does not compile: {}", first_lines(&rep.note, 6)));
    }
    if !rep.go_build_ok {
        return Err(format!(
            "go build failed: {}",
            first_lines(&rep.go_build_stderr, 6)
        ));
    }
    let bin = dir.join("sky-out").join("app");
    std::fs::canonicalize(&bin).map_err(|e| format!("{}: {e}", bin.display()))
}

fn first_lines(s: &str, n: usize) -> String {
    s.lines().take(n).collect::<Vec<_>>().join(" | ")
}

fn args_for(n: u64, extra: &[&str]) -> Vec<String> {
    let mut v = vec![n.to_string()];
    v.extend(extra.iter().map(|s| s.to_string()));
    v
}

/// Check one result against the expectations every run shares.
fn check_run(label: &str, r: &RunResult, want: &str, ceiling: f64, problems: &mut Vec<String>) {
    if r.timed_out {
        problems.push(format!(
            "{label}: killed after {:.2} s (ceiling {ceiling} s)",
            r.secs
        ));
        return;
    }
    if let Some(f) = &r.failed {
        problems.push(format!("{label}: {f}"));
        return;
    }
    if r.stdout != want {
        problems.push(format!(
            "{label}: printed {:?}, expected {:?}",
            r.stdout, want
        ));
    }
    match r.stats {
        None => problems.push(format!(
            "{label}: no skyMemStats exit line (SKY_RT_MEMSTATS_ON_EXIT=1)"
        )),
        Some(s) if s.erased_typed_cons > 0 => problems.push(format!(
            "{label}: {} erased rt.List_cons calls on a typed tail (O(n) reflect widen per cons)",
            s.erased_typed_cons
        )),
        _ => {}
    }
}

/// Measure one program. Never panics: every failure is a listed problem.
pub fn measure(root: &Path, p: &Program, ceiling: f64) -> Verdict {
    let dir = root.join("tests/perf").join(p.name);
    let mut problems = Vec::new();
    let bin = match build(root, &dir) {
        Ok(b) => b,
        Err(e) => {
            return Verdict {
                name: p.name,
                problems: vec![e],
                report: String::from("not built"),
            }
        }
    };
    let (n, n4) = (p.n, 4 * p.n);
    // The big size first: a quadratic program is then killed once instead of
    // being run five times at the small size first.
    let big = best_of(&bin, &dir, &args_for(n4, p.extra_args), ceiling);
    check_run("4n", &big, &(p.expect)(n4), ceiling, &mut problems);
    if big.timed_out || big.failed.is_some() {
        return Verdict {
            name: p.name,
            problems,
            report: format!("4n={n4}: killed or failed"),
        };
    }
    let small = best_of(&bin, &dir, &args_for(n, p.extra_args), ceiling);
    check_run("n", &small, &(p.expect)(n), ceiling, &mut problems);
    if big.secs > ceiling {
        problems.push(format!(
            "4n: {:.3} s is over the {ceiling} s ceiling",
            big.secs
        ));
    }
    let growth = if small.secs > 0.0 {
        big.secs / small.secs
    } else {
        f64::INFINITY
    };
    if growth > MAX_GROWTH {
        problems.push(format!(
            "growth t(4n)/t(n) = {growth:.2} > {MAX_GROWTH} (quadratic is 16)"
        ));
    }
    let per_elem = match (small.stats, big.stats) {
        (Some(a), Some(b)) => mallocs_per_element(a, b, n * p.elems_per_n, n4 * p.elems_per_n),
        _ => f64::INFINITY,
    };
    if per_elem > p.malloc_budget {
        problems.push(format!(
            "{per_elem:.3} mallocs per element > budget {}",
            p.malloc_budget
        ));
    }
    let mut report = format!(
        "t(n)={:.3}s t(4n)={:.3}s growth={growth:.2} mallocs/elem={per_elem:.3} (budget {})",
        small.secs, big.secs, p.malloc_budget
    );
    if let Some(partner) = p.partner {
        let pr = best_of(&bin, &dir, &args_for(n4, partner), ceiling);
        check_run("partner 4n", &pr, &(p.expect)(n4), ceiling, &mut problems);
        if pr.failed.is_none() && !pr.timed_out {
            let ratio = big.secs / pr.secs.max(1e-9);
            report.push_str(&format!(
                " partner={:.3}s ratio={ratio:.2} (max {MAX_PARTNER_RATIO})",
                pr.secs
            ));
            if ratio > MAX_PARTNER_RATIO {
                problems.push(format!(
                    "t(program)/t(partner) = {ratio:.2} > {MAX_PARTNER_RATIO}"
                ));
            }
        }
    }
    Verdict {
        name: p.name,
        problems,
        report,
    }
}

pub fn run(args: &[String], root: &Path) -> i32 {
    let strict = args.iter().any(|a| a == "--strict");
    let only: Option<Vec<String>> = args
        .iter()
        .position(|a| a == "--only")
        .and_then(|i| args.get(i + 1))
        .map(|s| s.split(',').map(str::to_string).collect());
    for a in args {
        if a.starts_with("--") && a != "--strict" && a != "--only" {
            eprintln!("perf-gate: unknown option {a}");
            return 2;
        }
    }
    let ceiling = if strict { STRICT_CEILING_S } else { CEILING_S };
    let chosen: Vec<&Program> = PROGRAMS
        .iter()
        .filter(|p| only.as_ref().is_none_or(|o| o.iter().any(|n| n == p.name)))
        .collect();
    if chosen.is_empty() {
        eprintln!("perf-gate: --only matched no program");
        return 2;
    }
    let mut red = 0;
    for p in &chosen {
        eprintln!("perf-gate: {} — {}", p.name, p.what);
        let v = measure(root, p, ceiling);
        if v.problems.is_empty() {
            println!("  PASS {:<18} {}", v.name, v.report);
        } else {
            red += 1;
            println!("  FAIL {:<18} {}", v.name, v.report);
            for pr in &v.problems {
                println!("       - {pr}");
            }
        }
    }
    let total = chosen.len();
    if red == 0 {
        println!("perf-gate: PASS ({total}/{total} programs, ceiling {ceiling} s)");
        0
    } else {
        println!("perf-gate: FAIL ({red} of {total} programs red, ceiling {ceiling} s)");
        1
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_the_exit_line_among_other_stderr() {
        let s = "log line\n{\"skyMemStats\":{\"erasedTypedCons\":3,\"mallocs\":10,\"totalAlloc\":99}}\n";
        assert_eq!(
            parse_memstats(s),
            Some(MemStats {
                mallocs: 10,
                total_alloc: 99,
                erased_typed_cons: 3
            })
        );
        assert_eq!(parse_memstats("no line here"), None);
    }

    #[test]
    fn the_start_up_cost_cancels_in_the_difference_quotient() {
        let a = MemStats {
            mallocs: 50_000 + 100,
            ..Default::default()
        };
        let b = MemStats {
            mallocs: 50_000 + 400,
            ..Default::default()
        };
        assert_eq!(mallocs_per_element(a, b, 100, 400), 1.0);
        assert!(mallocs_per_element(a, b, 400, 400).is_infinite());
    }

    #[test]
    fn every_program_has_a_project_and_a_budget() {
        let root = PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../.."));
        assert_eq!(PROGRAMS.len(), 6, "design 1e: programs 1 to 6");
        for p in PROGRAMS {
            let dir = root.join("tests/perf").join(p.name);
            assert!(
                dir.join("sky.toml").is_file() && dir.join("src/Main.sky").is_file(),
                "{} has no project under tests/perf",
                p.name
            );
            assert!(p.malloc_budget > 0.0 && p.malloc_budget.is_finite());
            assert!(p.n > 0 && p.elems_per_n > 0);
        }
    }

    #[test]
    fn a_quadratic_ratio_is_red_and_a_linear_one_is_green() {
        assert!(16.0 > MAX_GROWTH);
        assert!(4.4 <= MAX_GROWTH);
    }
}

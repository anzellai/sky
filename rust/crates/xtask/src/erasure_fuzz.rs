//! `xtask erasure-fuzz` — the ERASURE-BOUNDARY soundness fuzzer.
//!
//! ## The gap it closes
//!
//! The "if it compiles it works" contract says: a program the type-checker
//! ACCEPTS must `go build` and run without a panic. The bugs that violate it in
//! practice are all one shape — an **erasure round-trip**: a value with a known,
//! concrete Go shape (a function `func(string)Msg`, a record, an ADT payload, a
//! same-named-but-different type from another module) flows into an erased `any`
//! slot (a container constructor, a polymorphic builder field, a cross-module
//! converter) and comes back out with the WRONG Go shape. `sky check` passes;
//! `go build` errors or the runtime panics.
//!
//! Two existing gates each cover half of this and neither covers the seam:
//!   * `xtask welltyped` GENERATES well-typed programs but stops at the
//!     type-check boundary (killed at `-- Generating Go`) — it never `go build`s.
//!     Its type space also has no function type and no cross-module types, the
//!     exact shapes these bugs live in.
//!   * `xtask build-run` `go build`s + runs, but only the FIXED `examples/` set —
//!     never a generated program.
//!
//! So nothing generates a well-typed erasure-crossing program and then builds +
//! runs it. That is precisely why every bug of this class was found by hand,
//! building a real app that happened to use the shape. This gate makes that
//! search mechanical.
//!
//! ## What it does
//!
//! It emits a matrix of programs that are well-typed BY CONSTRUCTION and each
//! cross one erasure boundary, then for every one asserts the contract:
//!
//! ```text
//!   type-check ACCEPTS  ⟹  go build SUCCEEDS  ⟹  run does not panic
//! ```
//!
//! A program that type-checks but fails to build is a CODEGEN bug; one that
//! builds but panics is a RUNTIME bug. Either fails the gate with the program
//! and the diagnostic. A program the type-checker REJECTS is a generator defect
//! (the templates are meant to be well-typed), reported separately, never as a
//! compiler bug.
//!
//! ## Seeding
//!
//! Like the combinatorial corpus, the known defects are pinned coordinates:
//!   * `codegen_maybe_of_function_erasure` (fixed 7a0e5efc) — a function value in
//!     a `Maybe`/`List`/`Result`. Must now PASS (guards against regression).
//!   * `codegen_samename_crossmodule_type_collision` (OPEN) — a same-named type
//!     from a second module through a converter + a polymorphic map. Expected to
//!     REDISCOVER as a RUNTIME bug until the root-cause fix lands.
//!
//! ## Binary + running
//!
//! Discovers the `sky` binary via `SKY_BIN`, else `sky-out/sky`, else
//! `rust/target/release/sky`. Self-contained (no Haskell oracle), so it runs in
//! CI. Each build + run is timeout-bounded.

use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

const BUILD_TIMEOUT: Duration = Duration::from_secs(180);
const RUN_TIMEOUT: Duration = Duration::from_secs(20);

/// What became of one generated program.
#[derive(Debug, PartialEq, Eq)]
enum Outcome {
    /// Type-checked, built, ran clean.
    Passed,
    /// The type-checker rejected it — a GENERATOR defect (templates must be
    /// well-typed), not a compiler bug.
    IllTyped(String),
    /// Type-checked but `go build` failed — a codegen soundness bug.
    CodegenBug(String),
    /// Built but the run panicked — a runtime soundness bug.
    RuntimeBug(String),
    /// Built + ran clean but printed the WRONG value — a SILENT-correctness bug
    /// (compiles, runs, no panic, wrong answer). This is the class that a
    /// build-and-no-panic prover is blind to; a `Case` with `expect_stdout` turns
    /// erasure-fuzz into a full "if it compiles it works" check. It caught the
    /// open-row-alias field-drop break (age/city zeroed with no error).
    WrongValue(String),
    /// Build or run exceeded its wall clock.
    Timeout(String),
}

/// What a correct compiler should do with a case — and how a failure is scored.
#[derive(Clone, Copy, PartialEq, Eq)]
pub enum Expect {
    /// Must pass. A failure is a NEW soundness bug and FAILS the gate. Fixed
    /// classes (regression guards) and controls are `MustPass`.
    MustPass,
    /// A probe of a KNOWN-OPEN root-cause class (a cross-module collision
    /// candidate). Its pass/fail is DATA, not a gate condition: a failure is a
    /// tracked manifestation of the open class (gate stays green), a pass just
    /// means that coordinate does not manifest yet. When the class is fixed and
    /// every such case passes, they are promoted to `MustPass` (regression
    /// guards) — the gate then goes red if any regresses.
    KnownOpen,
}

/// A generated project: one entry `Main.sky` plus any sibling modules.
struct Case {
    id: String,
    /// `(relative-path-under-src, content)`; `Main.sky` is the entry.
    files: Vec<(String, String)>,
    expect: Expect,
    /// The EXACT value the program must print (its last non-empty stdout line),
    /// or `None` to check only build-succeeds + no-panic. `Some(_)` upgrades the
    /// case to a silent-correctness check — a clean run that prints the wrong
    /// value is a `WrongValue` bug, not a pass.
    expect_stdout: Option<String>,
    note: &'static str,
}

pub fn run(args: &[String], repo_root: &Path) -> i32 {
    let verbose = args.iter().any(|a| a == "-v" || a == "--verbose");

    let Some(sky) = find_sky_bin(repo_root) else {
        eprintln!(
            "erasure-fuzz: no `sky` binary found (set SKY_BIN, or `./scripts/build.sh` to make sky-out/sky)"
        );
        return 1;
    };
    println!("erasure-fuzz: using compiler {}", sky.display());

    let cases = generate_cases();
    println!("erasure-fuzz: {} generated cases\n", cases.len());

    let scratch = repo_root.join("target/erasure-fuzz");
    let _ = std::fs::remove_dir_all(&scratch);

    let mut bugs: Vec<(String, Outcome)> = Vec::new();
    let mut generator_defects: Vec<(String, String)> = Vec::new();
    let mut expected_open: Vec<String> = Vec::new();
    let mut passed = 0usize;

    // Write every case first (sequential, cheap), then evaluate in parallel —
    // each case is an isolated project dir, so the `sky build` + run of one never
    // touches another. Bounded to available_parallelism - 1 so the box stays
    // usable. Order is not meaningful (results keyed by id), so we sort for a
    // deterministic report.
    let mut writable: Vec<&Case> = Vec::new();
    for case in &cases {
        let dir = scratch.join(&case.id);
        if let Err(e) = write_case(&dir, case) {
            generator_defects.push((case.id.clone(), format!("write failed: {e}")));
        } else {
            writable.push(case);
        }
    }
    // Each worker runs a `sky build` → a `go build` whose `compile` child can peak
    // over 1 GB, so the fleet is capped conservatively (default 4) to stay under
    // the mem-guard system-memory floor — an over-subscribed fleet spikes swap and
    // gets its `sky` children killed mid-build, which then orphans their `go`
    // processes. Tune with SKY_FUZZ_JOBS.
    let default_jobs = std::thread::available_parallelism()
        .map(|n| n.get().saturating_sub(1).max(1))
        .unwrap_or(4)
        .min(4);
    let workers = std::env::var("SKY_FUZZ_JOBS")
        .ok()
        .and_then(|s| s.parse::<usize>().ok())
        .filter(|&n| n >= 1)
        .unwrap_or(default_jobs)
        .min(writable.len().max(1));
    let next = std::sync::atomic::AtomicUsize::new(0);
    let mut results: Vec<(String, Outcome)> = std::thread::scope(|scope| {
        let handles: Vec<_> = (0..workers)
            .map(|_| {
                let next = &next;
                let writable = &writable;
                let scratch = &scratch;
                let sky = &sky;
                scope.spawn(move || {
                    let mut local: Vec<(String, Outcome)> = Vec::new();
                    loop {
                        let i = next.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
                        let Some(case) = writable.get(i) else { break };
                        let dir = scratch.join(&case.id);
                        local.push((
                            case.id.clone(),
                            evaluate(sky, &dir, case.expect_stdout.as_deref()),
                        ));
                    }
                    local
                })
            })
            .collect();
        handles.into_iter().flat_map(|h| h.join().unwrap()).collect()
    });
    results.sort_by(|a, b| a.0.cmp(&b.0));

    let meta: std::collections::HashMap<&str, &Case> =
        cases.iter().map(|c| (c.id.as_str(), c)).collect();
    for (id, outcome) in results {
        let case = meta[id.as_str()];
        let is_bug = matches!(
            outcome,
            Outcome::CodegenBug(_)
                | Outcome::RuntimeBug(_)
                | Outcome::WrongValue(_)
                | Outcome::Timeout(_)
        );

        match &outcome {
            Outcome::Passed => {
                passed += 1;
                if verbose {
                    println!("  PASS  {}  ({})", case.id, case.note);
                }
            }
            Outcome::IllTyped(msg) => {
                generator_defects.push((case.id.clone(), msg.clone()));
            }
            _ if is_bug => match case.expect {
                Expect::MustPass => {
                    println!("  BUG   {}  ({})\n        {:?}", case.id, case.note, outcome);
                    bugs.push((case.id.clone(), outcome));
                }
                Expect::KnownOpen => {
                    println!("  OPEN  {} — known-open class manifests ({})", case.id, case.note);
                    expected_open.push(case.id.clone());
                }
            },
            _ => {}
        }
    }
    // Count how many known-open probes CURRENTLY PASS — when this reaches the
    // total number of known-open probes, the class is fixed and every one should
    // be promoted to `MustPass`.
    let known_open_total = cases.iter().filter(|c| c.expect == Expect::KnownOpen).count();
    let known_open_manifesting = expected_open.len();

    println!("\n─────────────────────────────────────────────");
    println!(
        "erasure-fuzz: {passed} passed · {} NEW bug(s) · {known_open_manifesting}/{known_open_total} known-open probes manifest · {} generator defect(s)",
        bugs.len(),
        generator_defects.len()
    );
    if known_open_total > 0 && known_open_manifesting == 0 {
        // Every known-open probe now PASSES — the class is fixed. Loud: the seeds
        // must be promoted from `KnownOpen` to `MustPass` so they guard the fix.
        println!(
            "\n  *** the known-open collision class is FIXED — all {known_open_total} probes pass. \
             Promote them from Expect::KnownOpen to Expect::MustPass so they guard the fix. ***"
        );
    }
    if !generator_defects.is_empty() {
        println!("\ngenerator defects (templates that did not type-check — fix the template, not the compiler):");
        for (id, msg) in &generator_defects {
            println!("  - {id}: {}", first_line(msg));
        }
    }
    if !bugs.is_empty() {
        println!("\nNEW soundness bugs (type-checked, then failed to build or panicked):");
        for (id, o) in &bugs {
            println!("  - {id}:\n{}", indent(&format!("{o:?}"), 6));
        }
        // A NEW bug (a MustPass case that failed) fails the gate; a KnownOpen
        // manifestation does not (it is expected until the class is fixed).
        return 1;
    }
    0
}

/// Evaluate a case, re-running ONCE on any bug/timeout verdict before trusting
/// it. A REAL soundness bug is deterministic — it reproduces on a fresh clean
/// build. A transient failure (parallel `go build` contending on the shared
/// GOCACHE, a resource blip) does NOT, so the second verdict is authoritative.
/// Without this, parallelism would spuriously false-positive the gate.
fn evaluate(sky: &Path, dir: &Path, expect_stdout: Option<&str>) -> Outcome {
    let first = evaluate_once(sky, dir, expect_stdout);
    match first {
        // A build/run/timeout failure retries once (filters a mem-guard-killed
        // flake). A WrongValue is DETERMINISTIC — the emitted Go computes it — so
        // it never retries; retrying would only hide it.
        Outcome::CodegenBug(_) | Outcome::RuntimeBug(_) | Outcome::Timeout(_) => {
            let _ = std::fs::remove_dir_all(dir.join("sky-out"));
            let _ = std::fs::remove_dir_all(dir.join(".skycache"));
            let _ = std::fs::remove_dir_all(dir.join(".skydeps"));
            evaluate_once(sky, dir, expect_stdout)
        }
        ok => ok,
    }
}

/// Build then, if it built, run — classifying at each boundary.
fn evaluate_once(sky: &Path, dir: &Path, expect_stdout: Option<&str>) -> Outcome {
    let _ = std::fs::remove_dir_all(dir.join("sky-out"));
    let (built, out) = build(sky, dir);
    let type_checked = out.contains("-- Generating Go") || out.contains("Compilation successful");
    if !type_checked {
        return Outcome::IllTyped(out);
    }
    match built {
        BuildResult::Timeout => return Outcome::Timeout(format!("build timed out\n{}", tail(&out))),
        BuildResult::Failed => {
            // Type-check passed (we saw the marker) but the whole build did not:
            // the failure is in codegen / `go build`. THE bug class.
            return Outcome::CodegenBug(tail(&out));
        }
        BuildResult::Ok => {}
    }
    // Built — now run it.
    run_binary(&dir.join("sky-out/app"), expect_stdout)
}

enum BuildResult {
    Ok,
    Failed,
    Timeout,
}

fn build(sky: &Path, dir: &Path) -> (BuildResult, String) {
    let child = Command::new(sky)
        .arg("build")
        .arg("src/Main.sky")
        .current_dir(dir)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn();
    let mut child = match child {
        Ok(c) => c,
        Err(e) => return (BuildResult::Failed, format!("spawn error: {e}")),
    };
    let deadline = Instant::now() + BUILD_TIMEOUT;
    loop {
        match child.try_wait() {
            Ok(Some(status)) => {
                let out = drain(&mut child);
                let r = if status.success() {
                    BuildResult::Ok
                } else {
                    BuildResult::Failed
                };
                return (r, out);
            }
            Ok(None) => {
                if Instant::now() >= deadline {
                    let _ = child.kill();
                    let _ = child.wait();
                    let out = drain(&mut child);
                    return (BuildResult::Timeout, out);
                }
                std::thread::sleep(Duration::from_millis(50));
            }
            Err(e) => return (BuildResult::Failed, format!("wait error: {e}")),
        }
    }
}

fn run_binary(bin: &Path, expect_stdout: Option<&str>) -> Outcome {
    if !bin.exists() {
        return Outcome::CodegenBug(format!("build reported success but {} is absent", bin.display()));
    }
    let child = Command::new(bin)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn();
    let mut child = match child {
        Ok(c) => c,
        Err(e) => return Outcome::RuntimeBug(format!("spawn error: {e}")),
    };
    let deadline = Instant::now() + RUN_TIMEOUT;
    loop {
        match child.try_wait() {
            Ok(Some(status)) => {
                let out = drain(&mut child);
                // A well-typed program with no effects should exit 0. A non-zero
                // exit carrying a panic signature is the runtime soundness bug.
                let panicked = out.contains("panic:")
                    || out.contains("CoerceFailure")
                    || out.contains("rt.Coerce")
                    || out.contains("goroutine ")
                    || out.contains("interface conversion");
                if !status.success() && panicked {
                    return Outcome::RuntimeBug(tail(&out));
                }
                // Silent-correctness check: a value-pinned case must print EXACTLY
                // its expected value (the last non-empty stdout line — the sole
                // `println`). A clean run with the wrong value is the compiles-but-
                // lies class (open-row alias field-drop), invisible to a build/no-
                // panic prover. Only checked on a clean exit — a panic/build fail
                // is already the more severe classification above.
                if let Some(exp) = expect_stdout {
                    let printed = out
                        .lines()
                        .map(str::trim)
                        .rev()
                        .find(|l| !l.is_empty())
                        .unwrap_or("");
                    if printed != exp {
                        return Outcome::WrongValue(format!("expected {exp:?}, printed {printed:?}"));
                    }
                }
                // A clean non-zero exit without a panic is not our class (the
                // template may legitimately exit non-zero); treat as passed but
                // keep it visible in verbose. Panic-on-zero is impossible.
                return Outcome::Passed;
            }
            Ok(None) => {
                if Instant::now() >= deadline {
                    let _ = child.kill();
                    let _ = child.wait();
                    // A hang is not the erasure class; surface as timeout.
                    return Outcome::Timeout("run did not exit".into());
                }
                std::thread::sleep(Duration::from_millis(50));
            }
            Err(e) => return Outcome::RuntimeBug(format!("wait error: {e}")),
        }
    }
}

// ── helpers ────────────────────────────────────────────────────────────────

fn find_sky_bin(root: &Path) -> Option<PathBuf> {
    if let Ok(p) = std::env::var("SKY_BIN") {
        let p = PathBuf::from(p);
        if p.exists() {
            return Some(p);
        }
    }
    for c in [root.join("sky-out/sky"), root.join("rust/target/release/sky")] {
        if c.exists() {
            return Some(c);
        }
    }
    None
}

fn write_case(dir: &Path, case: &Case) -> std::io::Result<()> {
    let src = dir.join("src");
    std::fs::create_dir_all(&src)?;
    std::fs::write(
        dir.join("sky.toml"),
        format!("name = \"{}\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n", case.id),
    )?;
    for (rel, content) in &case.files {
        std::fs::write(src.join(rel), content)?;
    }
    Ok(())
}

fn drain(child: &mut std::process::Child) -> String {
    use std::io::Read;
    let mut s = String::new();
    if let Some(mut o) = child.stdout.take() {
        let _ = o.read_to_string(&mut s);
    }
    if let Some(mut e) = child.stderr.take() {
        let _ = e.read_to_string(&mut s);
    }
    s
}

fn tail(s: &str) -> String {
    let lines: Vec<&str> = s.lines().collect();
    let start = lines.len().saturating_sub(20);
    lines[start..].join("\n")
}

fn first_line(s: &str) -> &str {
    s.lines().next().unwrap_or("")
}

fn indent(s: &str, n: usize) -> String {
    let pad = " ".repeat(n);
    s.lines().map(|l| format!("{pad}{l}")).collect::<Vec<_>>().join("\n")
}

// ── the templates (generation is in erasure_fuzz/templates.rs) ───────────────
mod templates;
use templates::generate_cases;

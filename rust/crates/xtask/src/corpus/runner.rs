//! Running a generated case, and the v2 §2.3 / §3.5 red-rate spike.
//!
//! # Why cases are BUILT AND RUN, not just type-checked
//!
//! A static verdict (does it type-check?) costs 1.02 ms/case on the shared world
//! and would let the corpus be enormous. It also cannot see the defect class this
//! corpus exists for. #166, #171, #173 and the `goty.rs` fieldset collision all
//! **compile clean and behave wrong** — v2 §3.1 says so in as many words. So the
//! value-asserting families pay `c_u` (measured 0.70 s/unit warm on this host,
//! `docs/ci-test-phase-2-3-results.md` §5) and run the program.
//!
//! That makes `N_iso · c_u` the whole cost model, exactly as Phase 3 concluded:
//! the static term stopped binding at 1.02 ms/case.

use super::gen::{Expect, GenCase};
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Mutex;
use std::time::{Duration, Instant};

/// How many cases build concurrently.
///
/// Bounded deliberately. The carry-in hazard (`.claude/AUTONOMOUS_GOAL.md`): the
/// example sweep spawns thousands of `xcrun` processes and exhausts the per-uid
/// process table, which kills mem-guard's ability to fork and makes unrelated
/// things fail. v2 §7.6 makes `EAGAIN` on spawn a FAIL, never a retry.
const WORKERS: usize = 4;

/// Per-case wall-clock ceiling. A case that hangs is a bug to bisect, not to wait
/// out.
const CASE_BUDGET: Duration = Duration::from_secs(120);

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Verdict {
    /// Built, ran, and printed exactly what the generator predicted.
    Green,
    /// Did not build. Carries the compiler's message.
    BuildFailed(String),
    /// Built and ran, but printed something else. **This is the
    /// "compiles clean, behaves wrong" class.**
    WrongValue { expected: String, got: String },
    /// Ran and exited non-zero, or was killed at the budget.
    Crashed(String),
    /// Rejected as the case declared (family R).
    RejectedAsDeclared,
    /// Expected a rejection and got an acceptance.
    UnexpectedlyAccepted,
}

impl Verdict {
    pub fn is_red(&self) -> bool {
        !matches!(self, Verdict::Green | Verdict::RejectedAsDeclared)
    }

    pub fn label(&self) -> &'static str {
        match self {
            Verdict::Green => "GREEN",
            Verdict::BuildFailed(_) => "BUILD-FAILED",
            Verdict::WrongValue { .. } => "WRONG-VALUE",
            Verdict::Crashed(_) => "CRASHED",
            Verdict::RejectedAsDeclared => "REJECTED-OK",
            Verdict::UnexpectedlyAccepted => "UNEXPECTEDLY-ACCEPTED",
        }
    }
}

#[derive(Clone, Debug)]
pub struct CaseResult {
    pub id: String,
    pub stratum: &'static str,
    pub verdict: Verdict,
    pub elapsed: Duration,
}

/// Materialise a case as a Sky project under `dir` and build + run it.
///
/// A module named `Helper.Inner.Values` becomes `src/Helper/Inner/Values.sky`,
/// which is how the driver discovers it.
pub fn run_case(sky: &Path, dir: &Path, case: &GenCase) -> Verdict {
    let src = dir.join("src");
    if let Err(e) = std::fs::create_dir_all(&src) {
        return Verdict::Crashed(format!("mkdir {}: {e}", src.display()));
    }
    for (name, source) in &case.modules {
        let rel: PathBuf = name.split('.').collect::<PathBuf>().with_extension("sky");
        let path = src.join(&rel);
        if let Some(parent) = path.parent() {
            if let Err(e) = std::fs::create_dir_all(parent) {
                return Verdict::Crashed(format!("mkdir {}: {e}", parent.display()));
            }
        }
        if let Err(e) = std::fs::write(&path, source) {
            return Verdict::Crashed(format!("write {}: {e}", path.display()));
        }
    }

    let entry_rel: PathBuf = case
        .entry
        .split('.')
        .collect::<PathBuf>()
        .with_extension("sky");
    let entry = src.join(entry_rel);

    // ---- build -----------------------------------------------------------
    let build = match run_bounded(
        Command::new(sky)
            .arg("build")
            .arg(&entry)
            .current_dir(dir),
        CASE_BUDGET,
    ) {
        Ok(o) => o,
        // v2 §7.6: a run that could not fork did not test anything.
        Err(e) => return Verdict::Crashed(format!("spawn failed: {e}")),
    };

    let declared_reject = matches!(case.expect, Expect::Reject { .. });

    if !build.status_ok {
        let msg = tail(&build.merged, 12);
        return if declared_reject {
            Verdict::RejectedAsDeclared
        } else {
            Verdict::BuildFailed(msg)
        };
    }
    if declared_reject {
        return Verdict::UnexpectedlyAccepted;
    }

    // ---- run -------------------------------------------------------------
    let app = dir.join("sky-out").join("app");
    if !app.exists() {
        return Verdict::Crashed(format!("no binary at {}", app.display()));
    }
    let run = match run_bounded(Command::new(&app).current_dir(dir), CASE_BUDGET) {
        Ok(o) => o,
        Err(e) => return Verdict::Crashed(format!("spawn failed: {e}")),
    };
    if !run.status_ok {
        return Verdict::Crashed(tail(&run.merged, 12));
    }

    match &case.expect {
        Expect::Accept { stdout } => {
            let got = run.stdout.trim().to_string();
            if &got == stdout {
                Verdict::Green
            } else {
                Verdict::WrongValue {
                    expected: stdout.clone(),
                    got,
                }
            }
        }
        Expect::Reject { .. } => Verdict::UnexpectedlyAccepted,
    }
}

struct Output {
    status_ok: bool,
    stdout: String,
    merged: String,
}

/// Run a command under a wall-clock ceiling, killing its process group on
/// expiry.
fn run_bounded(cmd: &mut Command, budget: Duration) -> Result<Output, String> {
    use std::io::Read;
    use std::process::Stdio;

    cmd.stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    #[cfg(unix)]
    {
        use std::os::unix::process::CommandExt;
        cmd.process_group(0);
    }

    let mut child = cmd.spawn().map_err(|e| e.to_string())?;
    let pid = child.id();
    let deadline = Instant::now() + budget;

    // Drain the pipes on threads so a chatty build cannot deadlock on a full
    // pipe buffer while we are polling for exit.
    let mut so = child.stdout.take().expect("piped");
    let mut se = child.stderr.take().expect("piped");
    let h1 = std::thread::spawn(move || {
        let mut s = String::new();
        let _ = so.read_to_string(&mut s);
        s
    });
    let h2 = std::thread::spawn(move || {
        let mut s = String::new();
        let _ = se.read_to_string(&mut s);
        s
    });

    let mut timed_out = false;
    let status = loop {
        match child.try_wait() {
            Ok(Some(st)) => break st,
            Ok(None) => {
                if Instant::now() >= deadline {
                    timed_out = true;
                    kill_group(pid);
                    break child.wait().map_err(|e| e.to_string())?;
                }
                std::thread::sleep(Duration::from_millis(20));
            }
            Err(e) => return Err(e.to_string()),
        }
    };

    let stdout = h1.join().unwrap_or_default();
    let stderr = h2.join().unwrap_or_default();
    Ok(Output {
        status_ok: status.success() && !timed_out,
        merged: format!("{stdout}{stderr}"),
        stdout,
    })
}

#[cfg(unix)]
fn kill_group(pid: u32) {
    use nix::sys::signal::{killpg, Signal};
    use nix::unistd::Pid;
    let pgid = Pid::from_raw(pid as i32);
    let _ = killpg(pgid, Signal::SIGTERM);
    std::thread::sleep(Duration::from_millis(300));
    let _ = killpg(pgid, Signal::SIGKILL);
}

#[cfg(not(unix))]
fn kill_group(_pid: u32) {}

fn tail(s: &str, n: usize) -> String {
    let lines: Vec<&str> = s.lines().filter(|l| !l.trim().is_empty()).collect();
    let start = lines.len().saturating_sub(n);
    lines[start..].join("\n")
}

/// Build `entry` in `dir` and run the resulting binary, returning its stdout.
///
/// Used by the isolation gate, which needs the *value* a case computed rather
/// than a pass/fail verdict — comparing verdicts alone would hide a batch that
/// changes an answer from one wrong value to a different wrong value.
pub fn build_and_run(sky: &Path, dir: &Path, entry: &Path) -> Result<String, String> {
    let build = run_bounded(
        Command::new(sky).arg("build").arg(entry).current_dir(dir),
        CASE_BUDGET,
    )?;
    if !build.status_ok {
        return Err(tail(&build.merged, 10));
    }
    let app = dir.join("sky-out").join("app");
    if !app.exists() {
        return Err(format!("no binary at {}", app.display()));
    }
    let run = run_bounded(Command::new(&app).current_dir(dir), CASE_BUDGET)?;
    if !run.status_ok {
        return Err(tail(&run.merged, 10));
    }
    Ok(run.stdout)
}

/// Build `entry` in `dir` without running the result.
///
/// Used by the witness gate, which needs the EMITTED GO rather than the value —
/// the axis witness is a property of what the compiler produced, not of what the
/// program printed.
pub fn build_only(sky: &Path, dir: &Path, entry: &Path) -> Result<(), String> {
    let build = run_bounded(
        Command::new(sky).arg("build").arg(entry).current_dir(dir),
        CASE_BUDGET,
    )?;
    if build.status_ok {
        Ok(())
    } else {
        Err(tail(&build.merged, 8))
    }
}

/// Materialise and run a single case, returning its printed value.
pub fn run_case_capture(sky: &Path, dir: &Path, case: &GenCase) -> Result<String, String> {
    let src = dir.join("src");
    std::fs::create_dir_all(&src).map_err(|e| e.to_string())?;
    for (name, source) in &case.modules {
        let rel: PathBuf = name.split('.').collect::<PathBuf>().with_extension("sky");
        let path = src.join(&rel);
        if let Some(parent) = path.parent() {
            std::fs::create_dir_all(parent).map_err(|e| e.to_string())?;
        }
        std::fs::write(&path, source).map_err(|e| e.to_string())?;
    }
    let entry_rel: PathBuf = case
        .entry
        .split('.')
        .collect::<PathBuf>()
        .with_extension("sky");
    Ok(build_and_run(sky, dir, &src.join(entry_rel))?.trim().to_string())
}

/// Where generated case projects are materialised.
///
/// **Outside the repository tree, deliberately.** `sky`'s project discovery walks
/// up to the nearest ancestor holding `sky-stdlib/` + `runtime-go/`; a case built
/// under the repo (e.g. in `.skycache/`) resolves its project root to the REPO
/// root and then reports `no .sky under src/`. The compiler embeds the runtime
/// and stdlib (`//go:embed`), so a project outside the tree builds fine.
///
/// Measured the hard way: the first spike run reported a 100 % red rate, all of
/// it this one path mistake and none of it the compiler.
pub fn scratch_root(tag: &str) -> PathBuf {
    std::env::temp_dir().join(format!("sky-corpus-{tag}"))
}

/// Locate the release `sky` binary.
///
/// **`CARGO_TARGET_DIR` is honoured FIRST, and that is not a convenience.**
/// `rust/target/` is where cargo writes only when nothing redirects it. When
/// `CARGO_TARGET_DIR` is set — a worktree isolating its build so two checkouts
/// do not clobber each other, which is the normal way to work on this repo —
/// `cargo build -p sky` writes THERE and `rust/target/release/sky` keeps
/// whatever stale binary it last held. Hard-coding `rust/target` therefore does
/// not fail; it silently compiles the corpus with a *different compiler than the
/// one under test*, and reports the verdict as if it were this tree's.
///
/// Measured, not hypothetical: a run against a 4-hour-stale binary reported
/// `stdlib_edge` 2 RED with `Std.Markdown` rendering `> quote` and `------`
/// literally — the behaviour of a stdlib revision that had already been
/// rewritten on disk. The source said one thing and the gate said another, and
/// the gate was reading an artefact. Re-run with the tree's own binary: 335/335
/// green. A gate that can silently test the wrong binary can just as easily
/// report PASS for a tree that is broken.
pub fn sky_binary(root: &Path) -> Option<PathBuf> {
    let mut candidates: Vec<PathBuf> = Vec::new();
    if let Some(target) = std::env::var_os("CARGO_TARGET_DIR") {
        candidates.push(PathBuf::from(target).join("release/sky"));
    }
    candidates.push(root.join("rust/target/release/sky"));
    candidates.push(root.join("sky-out/sky"));
    candidates.into_iter().find(|p| p.exists())
}

/// Run `cases` with bounded concurrency, reporting progress.
pub fn run_cases(root: &Path, cases: &[GenCase], scratch: &Path) -> Vec<CaseResult> {
    let Some(sky) = sky_binary(root) else {
        eprintln!(
            "corpus: no sky binary at rust/target/release/sky — build it first \
             (cd rust && cargo build --release -p sky). A run that cannot build \
             has not tested anything."
        );
        return Vec::new();
    };

    // Which binary produced this verdict, and how old is it. Printed ALWAYS, not
    // only on failure: the run that motivated this line looked entirely normal —
    // it named no path, and its 2 REDs described a stdlib revision that no longer
    // existed on disk. An age in the header turns "the gate disagrees with the
    // source" into a one-glance diagnosis instead of an investigation.
    let age = std::fs::metadata(&sky)
        .and_then(|m| m.modified())
        .ok()
        .and_then(|t| t.elapsed().ok())
        .map(|d| {
            let mins = d.as_secs() / 60;
            if mins >= 60 {
                format!("{}h{}m old", mins / 60, mins % 60)
            } else {
                format!("{mins}m old")
            }
        })
        .unwrap_or_else(|| "age unknown".to_string());
    eprintln!("  compiler under test: {} ({age})", sky.display());

    let _ = std::fs::remove_dir_all(scratch);
    let _ = std::fs::create_dir_all(scratch);

    let next = AtomicUsize::new(0);
    let results: Mutex<Vec<CaseResult>> = Mutex::new(Vec::new());
    let done = AtomicUsize::new(0);
    let total = cases.len();

    std::thread::scope(|s| {
        for w in 0..WORKERS {
            let next = &next;
            let results = &results;
            let done = &done;
            let sky = &sky;
            std::thread::Builder::new()
                .name(format!("corpus-{w}"))
                .spawn_scoped(s, move || loop {
                    let i = next.fetch_add(1, Ordering::SeqCst);
                    if i >= total {
                        break;
                    }
                    let case = &cases[i];
                    let dir = scratch.join(format!("case-{i:05}"));
                    let t = Instant::now();
                    let verdict = run_case(sky, &dir, case);
                    let elapsed = t.elapsed();
                    // Reclaim as we go: a few thousand Go build trees would
                    // otherwise breach the 5 GB disk-hygiene threshold.
                    let _ = std::fs::remove_dir_all(&dir);
                    let n = done.fetch_add(1, Ordering::SeqCst) + 1;
                    if verdict.is_red() {
                        println!("  [{n:>4}/{total}] {} {}", verdict.label(), case.id);
                    } else if n % 25 == 0 {
                        println!("  [{n:>4}/{total}] …");
                    }
                    results.lock().unwrap().push(CaseResult {
                        id: case.id.clone(),
                        stratum: case.stratum,
                        verdict,
                        elapsed,
                    });
                })
                .expect("spawn worker");
        }
    });

    let mut out = results.into_inner().unwrap();
    out.sort_by(|a, b| a.id.cmp(&b.id));
    out
}

/// v2 §2.3 / §3.5 — the mandatory red-rate spike.
///
/// > A generator aimed at the neighbourhoods of every historical bug **will find
/// > reds**. If 15 % of 5,000 generated cases fail on day one, the corpus cannot
/// > land as a required check and there is no policy for the reds.
///
/// The spike samples across every stratum — deterministically, by striding the
/// full case list, so the sample spans the axis space rather than clustering in
/// whichever stratum sorts first.
pub fn spike(root: &Path, n: usize) -> i32 {
    let all = super::behavioural_cases();
    let total = all.len();
    let stride = (total / n.max(1)).max(1);
    let sample: Vec<GenCase> = all.iter().step_by(stride).take(n).cloned().collect();

    println!("CORPUS SPIKE — v2 §2.3 / §3.5 red-rate measurement");
    println!("  generated corpus (N_min) : {total}");
    println!("  spike sample             : {} (stride {stride})", sample.len());
    println!("  mode                     : build + RUN (value assertions)");
    println!("  workers                  : {WORKERS}");
    println!();

    let scratch = scratch_root("spike");
    let t = Instant::now();
    let results = run_cases(root, &sample, &scratch);
    let wall = t.elapsed();
    if results.is_empty() {
        return 1;
    }

    report(&results, wall, sample.len());

    let reds = results.iter().filter(|r| r.verdict.is_red()).count();
    println!();
    println!(
        "  RED RATE: {reds}/{} = {:.1}%",
        results.len(),
        reds as f64 / results.len() as f64 * 100.0
    );
    println!();
    println!("  The spike is a MEASUREMENT, not a gate. A high red rate is a");
    println!("  finding to report — v2 §2.3 — not a reason to narrow the generator.");

    0
}

/// Run the corpus's BUILT-AND-RUN cases.
///
/// Deliberately `behavioural_cases()`, not `all_cases()`. Families R and E are
/// in the manifest — the single membership authority lists every case — but
/// their verdicts do not come from running a binary: an R program is ill-typed
/// by construction, and an E case's claim is a property of the emitted Go.
/// Building them here would spend `c_u` each to learn nothing, and would report
/// a family-R case's correct rejection as a `BUILD-FAILED` red.
pub fn run_all(root: &Path) -> i32 {
    let all = super::behavioural_cases();
    println!("CORPUS — Layer 1 (v2 §3)");
    println!(
        "  N_min : {} ({} behavioural, built + run here; the rest are family R \
         (`--reject`) and family E (`--emit-shape`), which never `go build`)",
        super::n_min(),
        all.len()
    );
    println!();
    let scratch = scratch_root("run");
    let t = Instant::now();
    let results = run_cases(root, &all, &scratch);
    let wall = t.elapsed();
    if results.is_empty() {
        return 1;
    }
    report(&results, wall, all.len());
    // ---- BLOCKED accounting (v2 §7.2) -----------------------------------
    //
    // A blocked case RAN. It is red because the product is broken, not because
    // the case is. It never contributes PASS; it fails the run once its expiry
    // has passed; and if it goes GREEN we say so loudly, because a defect that
    // quietly started working is a fact about the product that must be recorded
    // rather than absorbed.
    let by_id: std::collections::BTreeMap<&str, &GenCase> =
        all.iter().map(|c| (c.id.as_str(), c)).collect();

    let today = today_iso();
    let mut unexpected_red = Vec::new();
    let mut blocked_red = Vec::new();
    let mut blocked_now_green = Vec::new();
    let mut expired = Vec::new();

    for r in &results {
        let blocked = by_id.get(r.id.as_str()).and_then(|c| c.blocked.as_ref());
        match (blocked, r.verdict.is_red()) {
            (Some(b), true) => {
                if today.as_str() > b.expires {
                    expired.push((r.id.clone(), b.issue, b.expires));
                } else {
                    blocked_red.push((r.id.clone(), b.issue, b.expires));
                }
            }
            (Some(b), false) => blocked_now_green.push((r.id.clone(), b.issue)),
            (None, true) => unexpected_red.push(r.id.clone()),
            (None, false) => {}
        }
    }

    if !blocked_red.is_empty() {
        println!();
        println!("  ---- {} BLOCKED (known product defect, still red) ----", blocked_red.len());
        for (id, issue, expires) in &blocked_red {
            println!("  {id}");
            println!("      issue   {issue}");
            println!("      expires {expires}");
        }
    }
    if !blocked_now_green.is_empty() {
        println!();
        println!("  ---- {} BLOCKED case(s) NOW GREEN ----", blocked_now_green.len());
        for (id, issue) in &blocked_now_green {
            println!("  {id}  [{issue}]");
        }
        println!("  The defect appears fixed. Remove the block in the same commit that");
        println!("  confirms the fix — a stale block hides the next regression.");
    }
    if !expired.is_empty() {
        println!();
        println!("  ---- {} BLOCKED case(s) EXPIRED ----", expired.len());
        for (id, issue, expires) in &expired {
            println!("  {id}  [{issue}] expired {expires}");
        }
        println!("  A block is a deadline, not a parking space.");
    }

    println!();
    println!(
        "  {} green · {} blocked-red · {} unexpected-red · {} expired",
        results.len() - unexpected_red.len() - blocked_red.len() - expired.len(),
        blocked_red.len(),
        unexpected_red.len(),
        expired.len()
    );

    if !unexpected_red.is_empty() || !expired.is_empty() {
        println!(
            "\nCORPUS: FAIL ({} unexpected red, {} expired block(s))",
            unexpected_red.len(),
            expired.len()
        );
        1
    } else if !blocked_red.is_empty() {
        println!(
            "\nCORPUS: PASS with {} BLOCKED — every unblocked case produced the value \
             the generator predicted; the blocked cases reproduce known defects and \
             are counted, not silenced.",
            blocked_red.len()
        );
        0
    } else {
        println!(
            "\nCORPUS: PASS ({} cases, every value as the generator predicted)",
            results.len()
        );
        0
    }
}

/// Today as `YYYY-MM-DD`, for blocked-case expiry. Shared with
/// `emit_shape`, which applies the same BLOCKED contract per property.
pub fn today_iso() -> String {
    // Days since the Unix epoch → civil date (Howard Hinnant's algorithm).
    let secs = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0);
    let z = secs / 86_400 + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z.rem_euclid(146_097);
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let y = yoe + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = doy - (153 * mp + 2) / 5 + 1;
    let m = if mp < 10 { mp + 3 } else { mp - 9 };
    let y = if m <= 2 { y + 1 } else { y };
    format!("{y:04}-{m:02}-{d:02}")
}

fn report(results: &[CaseResult], wall: Duration, total: usize) {
    use std::collections::BTreeMap;

    let mut by_stratum: BTreeMap<&str, (usize, usize)> = BTreeMap::new();
    for r in results {
        let e = by_stratum.entry(r.stratum).or_insert((0, 0));
        e.0 += 1;
        if r.verdict.is_red() {
            e.1 += 1;
        }
    }

    println!();
    println!("  {:<24} {:>6} {:>6} {:>8}", "stratum", "cases", "red", "red %");
    println!("  {}", "-".repeat(48));
    for (s, (n, red)) in &by_stratum {
        println!(
            "  {s:<24} {n:>6} {red:>6} {:>7.1}%",
            *red as f64 / *n as f64 * 100.0
        );
    }

    let reds: Vec<&CaseResult> = results.iter().filter(|r| r.verdict.is_red()).collect();
    if !reds.is_empty() {
        println!();
        println!("  ---- {} RED ----", reds.len());
        for r in &reds {
            println!("  {} [{}]", r.id, r.verdict.label());
            match &r.verdict {
                Verdict::WrongValue { expected, got } => {
                    println!("      expected {expected:?}  got {got:?}");
                }
                Verdict::BuildFailed(m) | Verdict::Crashed(m) => {
                    // The LAST lines, not the first: the compiler prints a
                    // progress log to stdout and the diagnostic after it, so
                    // taking the head shows "-- Parsing" and hides the error.
                    let lines: Vec<&str> = m.lines().collect();
                    for line in lines.iter().rev().take(8).rev() {
                        println!("      {line}");
                    }
                }
                _ => {}
            }
        }
    }

    let per_case = wall.as_secs_f64() / total.max(1) as f64;
    println!();
    println!(
        "  wall-clock {:.1}s for {total} cases  ({:.2} s/case at {WORKERS} workers)",
        wall.as_secs_f64(),
        per_case
    );
}

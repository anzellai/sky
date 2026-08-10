//! `xtask harness` — the gate harness.
//!
//! Phase 1 of the CI/test overhaul (`docs/ci-test-architecture-v2.md` §7).
//!
//! **Built, not adopted.** The BlueDB gate registry on `feat/bluedb-v2` was the
//! proposed precedent for wholesale adoption; measured, it is 41 of 48 gates
//! stubbed, 7 of 48 mutations ever verified, a shell-out path where every
//! `Command` is `git`, a timeout that detaches a thread and kills nothing, and
//! a mutation probe with no timeout at all. What is adopted is the *shape* —
//! a static registry, a small closed set of states, a permanent canary, and the
//! const-evaluated non-empty `Mutations` constructor that makes a gate without
//! a falsifier a **build** error. Every behaviour is built here, with a
//! demonstration for each.
//!
//! # What the harness guarantees
//!
//! * **Rows come from the registry, not from the run.** A gate cannot vanish by
//!   not executing; it renders `NOT RUN`, and `NOT RUN` exits non-zero.
//! * **Bodies run in a child process group**, so a budget overrun is enforced
//!   by `killpg` and cannot leak a server holding a port into the next gate.
//! * **Results are generation-stamped**, so a straggler can never be read as a
//!   later gate's verdict.
//! * **`PASS` requires `assertions > 0`** and an exact expected count, so the
//!   `0/0 … GATE: PASS` and `>= 13`-against-63 classes are inexpressible.
//! * **A canary** proves the falsifier runner can say "this proved nothing".
//!
//! # Concurrency
//!
//! Gates run **sequentially**. That is a deliberate Phase-1 choice, not an
//! oversight: the measured failure that motivated this mandate is a parallel
//! sweep spawning thousands of `xcrun` processes and exhausting the per-uid
//! process table (2,167 of 2,472), which kills mem-guard's ability to fork. The
//! persistent-semaphore design of v2 §7.6 belongs with the Phase-6 topology
//! work, when there are measured runner numbers to budget against.

pub mod bodies;
pub mod child;
pub mod falsify;
pub mod layer2;
pub mod registry;
pub mod state;

use child::{result_path, run_gate_in_child, ChildResult};
use falsify::FalsifyOpts;
use registry::{Expect, Gate, GateCtx, Platform, Tier, GATES};
use state::{GateState, SuiteVerdict};
use std::path::{Path, PathBuf};
use std::time::Duration;

/// How long a recorded falsification proof stays fresh.
///
/// Beyond this, a passing gate renders `UNPROVEN` under `--require-proofs`:
/// the proof is unrevalidated, not known-broken (v2 §4.3's `UNVERIFIED-SINCE`).
const PROOF_WINDOW_DAYS: u64 = 30;

const PROOF_LEDGER: &str = "docs/coverage/falsifier-proofs.json";

pub struct Report {
    pub gate: &'static str,
    pub state: GateState,
    pub assertions: u64,
    pub expected: u64,
    pub elapsed_s: f64,
    pub detail: String,
}

pub fn run(args: &[String], root: &Path) -> i32 {
    let opts = match Opts::parse(args) {
        Ok(o) => o,
        Err(e) => {
            eprintln!("xtask harness: {e}\n{USAGE}");
            return 2;
        }
    };

    if opts.help {
        println!("{USAGE}");
        return 0;
    }

    // ---- child mode -------------------------------------------------------
    if let Some(gate_name) = &opts.exec_gate {
        return exec_gate(gate_name, &opts, root);
    }

    // ---- crash recovery, BEFORE anything is measured ----------------------
    // A previous `--verify-falsifiers` killed by a signal (budget `killpg`, CI
    // cancellation, an operator's ^C) leaves its mutation applied — `Drop` does
    // not run on a signal. Every later run then measures the mutation instead
    // of the change under test, and reports it as a compiler failure. Replay any
    // journal left behind, and say so loudly: a silently self-repairing harness
    // hides the fact that a previous run died mid-mutation.
    let restored = falsify::restore_orphans(root);
    if !restored.is_empty() {
        eprintln!(
            "xtask harness: a previous falsifier run died mid-mutation; \
             restored {} file(s) from the mutation journal:",
            restored.len()
        );
        for r in &restored {
            eprintln!("  {r}");
        }
    }

    if opts.list {
        return list(root);
    }

    if opts.verify_falsifiers {
        return run_falsifiers(&opts, root);
    }

    run_suite(&opts, root)
}

const USAGE: &str = "\
usage: xtask harness [options]

  --tier <T0|T1|T2|T3|T4|self>   run the gates declared for this tier (default T1)
  --only <name[,name...]>        run exactly these gates; every other gate renders
                                 NOT APPLICABLE (deliberate selection is not an unknown)
  --json <path>                  write the machine-readable run report
  --require-proofs               a gate whose falsification proof is missing or older
                                 than the declared window renders UNPROVEN
  --fail-fast                    stop after the first FAIL; gates not reached render
                                 NOT RUN, so the suite renders UNKNOWN
  --verify-falsifiers            apply each gate's declared mutation and prove the gate
                                 goes red; records proofs to docs/coverage/
  --list                         print the registry and exit
  -h, --help

exit codes: 0 PASS · 1 FAIL · 3 UNKNOWN (a NOT RUN or UNPROVEN gate) · 2 usage";

#[derive(Default)]
struct Opts {
    tier: Option<Tier>,
    only: Vec<String>,
    json: Option<PathBuf>,
    require_proofs: bool,
    fail_fast: bool,
    verify_falsifiers: bool,
    list: bool,
    help: bool,
    exec_gate: Option<String>,
    generation: u64,
    result: Option<PathBuf>,
}

impl Opts {
    fn parse(args: &[String]) -> Result<Opts, String> {
        fn value(args: &[String], i: &mut usize, flag: &str) -> Result<String, String> {
            *i += 1;
            args.get(*i)
                .cloned()
                .ok_or_else(|| format!("{flag} requires a value"))
        }

        let mut o = Opts::default();
        let mut i = 0;
        while i < args.len() {
            match args[i].as_str() {
                "--tier" => {
                    let v = value(args, &mut i, "--tier")?;
                    o.tier = Some(Tier::parse(&v).ok_or_else(|| format!("unknown tier `{v}`"))?);
                }
                "--only" => o.only.extend(
                    value(args, &mut i, "--only")?
                        .split(',')
                        .map(|s| s.trim().to_string())
                        .filter(|s| !s.is_empty()),
                ),
                "--json" => o.json = Some(PathBuf::from(value(args, &mut i, "--json")?)),
                "--result" => o.result = Some(PathBuf::from(value(args, &mut i, "--result")?)),
                "--generation" => {
                    o.generation = value(args, &mut i, "--generation")?
                        .parse()
                        .map_err(|_| "--generation must be a number".to_string())?
                }
                "--exec-gate" => o.exec_gate = Some(value(args, &mut i, "--exec-gate")?),
                "--require-proofs" => o.require_proofs = true,
                "--fail-fast" => o.fail_fast = true,
                "--verify-falsifiers" => o.verify_falsifiers = true,
                "--list" => o.list = true,
                "-h" | "--help" => o.help = true,
                other => return Err(format!("unknown option `{other}`")),
            }
            i += 1;
        }
        // An unknown gate name in `--only` must be an ERROR, never an empty
        // selection that trivially passes. This is the same class as `xtask`
        // exiting 0 on an unknown subcommand.
        for name in &o.only {
            if registry::find(name).is_none() {
                return Err(format!(
                    "unknown gate `{name}` (see `xtask harness --list`)"
                ));
            }
        }
        Ok(o)
    }
}

/// Is this gate selected to actually run?
fn selected(g: &Gate, o: &Opts) -> bool {
    if !g.platforms.contains(Platform::current()) {
        return false;
    }
    if !o.only.is_empty() {
        // Deliberate selection overrides the tier — including for self-test
        // gates, which is how they are exercised at all.
        return o.only.iter().any(|n| n == g.name);
    }
    g.tier == o.tier.unwrap_or(Tier::T1)
}

// ---------------------------------------------------------------------------
// child mode — run ONE body and write a generation-stamped result
// ---------------------------------------------------------------------------

fn exec_gate(name: &str, o: &Opts, root: &Path) -> i32 {
    let Some(gate) = registry::find(name) else {
        eprintln!("xtask harness: unknown gate `{name}`");
        return 2;
    };
    let Some(result) = &o.result else {
        eprintln!("xtask harness: --exec-gate requires --result");
        return 2;
    };

    let ctx = GateCtx {
        repo_root: root.to_path_buf(),
    };

    // A panicking body must produce a FAIL with the panic text, not silence.
    // Silence would render NOT RUN, which is true but much less useful than
    // "this body panicked, here is where".
    let outcome = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| (gate.body)(&ctx)));

    // Every result is stamped with the generation we were asked to answer for.
    // The parent discards anything carrying a different one, which is what
    // stops a straggler from a previous run being read as this gate's verdict.
    let r = match outcome {
        Ok(out) => ChildResult {
            generation: o.generation,
            passed: out.passed,
            assertions: out.assertions,
            detail: out.detail,
        },
        Err(p) => ChildResult {
            generation: o.generation,
            passed: false,
            assertions: 0,
            detail: format!("gate body PANICKED: {}", panic_text(p.as_ref())),
        },
    };

    if let Err(e) = child::write_result(result, &r) {
        eprintln!("xtask harness: cannot write result: {e}");
        return 2;
    }
    if r.passed {
        0
    } else {
        1
    }
}

fn panic_text(p: &(dyn std::any::Any + Send)) -> String {
    if let Some(s) = p.downcast_ref::<&str>() {
        (*s).to_string()
    } else if let Some(s) = p.downcast_ref::<String>() {
        s.clone()
    } else {
        "<non-string panic payload>".into()
    }
}

// ---------------------------------------------------------------------------
// suite mode
// ---------------------------------------------------------------------------

fn run_suite(o: &Opts, root: &Path) -> i32 {
    let exe = match std::env::current_exe() {
        Ok(e) => e,
        Err(e) => {
            eprintln!("xtask harness: cannot locate own binary: {e}");
            return 2;
        }
    };
    let scratch = bodies::scratch(root);
    let proofs = Proofs::load(root);
    let mut generation = 0u64;
    let mut reports: Vec<Report> = Vec::new();
    let mut aborted = false;

    // THE registry is the row source. Every gate gets a row whether or not it
    // ran — a gate cannot disappear by not executing.
    for g in GATES {
        if !selected(g, o) {
            reports.push(Report {
                gate: g.name,
                state: GateState::NotApplicable,
                assertions: 0,
                expected: g.expected,
                elapsed_s: 0.0,
                detail: not_applicable_reason(g, o),
            });
            continue;
        }
        // A declared block is decided BEFORE the gate is spawned, and before
        // --fail-fast, because whether the gate CAN run is a property of the
        // declaration, not of this run. Expiry is checked here so the block
        // turns itself into a FAIL on its deadline with nobody in the loop.
        if let Some(b) = registry::block_for(g.name) {
            let expired = registry::block_is_expired(b, registry::today_epoch_day());
            reports.push(Report {
                gate: g.name,
                state: if expired {
                    GateState::Fail
                } else {
                    GateState::Blocked
                },
                assertions: 0,
                expected: g.expected,
                elapsed_s: 0.0,
                detail: if expired {
                    format!(
                        "BLOCK EXPIRED {} — {} ({}). A block is a deadline, not a parking space: \
                         unblock the gate or re-declare the block with a new date and a reason \
                         that survives review.",
                        b.expires, b.reason, b.issue
                    )
                } else {
                    format!(
                        "blocked until {} — {} ({}). Never renders PASS; its surfaces count as \
                         UNCOVERED in the coverage ledger.",
                        b.expires, b.reason, b.issue
                    )
                },
            });
            if expired && o.fail_fast {
                aborted = true;
            }
            continue;
        }
        if aborted {
            // --fail-fast stopped us before reaching this gate. It is
            // registered and selected, and we do not know its verdict.
            reports.push(Report {
                gate: g.name,
                state: GateState::NotRun,
                assertions: 0,
                expected: g.expected,
                elapsed_s: 0.0,
                detail: "not reached — the run stopped at an earlier FAIL (--fail-fast)".into(),
            });
            continue;
        }

        generation += 1;
        let run = run_gate_in_child(
            &exe,
            root,
            g.name,
            generation,
            Duration::from_secs(g.budget_s),
            &result_path(&scratch, g.name, generation),
        );

        let (mut st, detail) = classify(g, &run);

        // A passing gate whose falsification is unproven is NOT a pass.
        if st == GateState::Pass && o.require_proofs && !proofs.fresh(g.name) {
            st = GateState::Unproven;
        }

        let assertions = run.result.as_ref().map(|r| r.assertions).unwrap_or(0);
        if st == GateState::Fail && o.fail_fast {
            aborted = true;
        }
        reports.push(Report {
            gate: g.name,
            state: st,
            assertions,
            expected: g.expected,
            elapsed_s: run.elapsed.as_secs_f64(),
            detail: if st == GateState::Unproven {
                format!("{detail} — but no falsification proof within {PROOF_WINDOW_DAYS}d")
            } else {
                detail
            },
        });
    }

    render(&reports);
    let verdict = SuiteVerdict::of(reports.iter().map(|r| r.state));
    println!("\nHARNESS VERDICT: {}", verdict.label());
    if verdict == SuiteVerdict::Unknown {
        println!(
            "  a run that cannot say whether a gate passed has not passed; \
             see the NOT RUN / UNPROVEN rows above"
        );
    }

    if let Some(p) = &o.json {
        if let Err(e) = write_json(p, &reports, verdict) {
            eprintln!("xtask harness: cannot write {}: {e}", p.display());
            return 2;
        }
    }
    verdict.exit_code()
}

fn not_applicable_reason(g: &Gate, o: &Opts) -> String {
    if !g.platforms.contains(Platform::current()) {
        format!(
            "declared for {} — this host is {}",
            g.platforms.labels().join("/"),
            Platform::current().label()
        )
    } else if !o.only.is_empty() {
        "not in --only".into()
    } else {
        format!(
            "declared {} — this run is {}",
            g.tier.label(),
            o.tier.unwrap_or(Tier::T1).label()
        )
    }
}

/// Turn a supervised run into a gate state.
///
/// The ordering is the contract:
/// * a **timeout is a FAIL**, and it is decided BEFORE the result file is
///   consulted — a body that wrote a green result and then hung must not be
///   able to buy its way out of its budget;
/// * a **spawn failure is a FAIL** (a run that could not fork tested nothing);
/// * `assertions == 0` or `!= expected` is a **FAIL** (vacuity, and shrinkage);
/// * "no usable result" is **NOT RUN** — genuinely unknown, never rounded up.
fn classify(g: &Gate, run: &child::ChildRun) -> (GateState, String) {
    if let Some(e) = &run.spawn_error {
        return (
            GateState::Fail,
            format!("could not spawn the gate body: {e}"),
        );
    }
    if run.timed_out {
        return (
            GateState::Fail,
            format!(
                "BUDGET EXCEEDED: killed at {}s (process group terminated)",
                g.budget_s
            ),
        );
    }
    if run.generation_mismatch {
        return (
            GateState::NotRun,
            "a result was found but stamped with another generation; it was DISCARDED".into(),
        );
    }
    let Some(r) = &run.result else {
        // The body died before it could say anything. Its output went straight
        // to the CI log (stdout/stderr are inherited, not piped — see
        // child.rs), so the evidence is above this table rather than in it.
        return (
            GateState::NotRun,
            format!(
                "the body produced no result (exit {:?}) — see its output above",
                run.exit_code
            ),
        );
    };
    if r.assertions == 0 {
        return (
            GateState::Fail,
            format!("VACUOUS: the gate reported zero assertions ({})", r.detail),
        );
    }
    if r.assertions != g.expected {
        return (
            GateState::Fail,
            format!(
                "expected EXACTLY {} assertions, got {} — {}",
                g.expected, r.assertions, r.detail
            ),
        );
    }
    if !r.passed {
        return (GateState::Fail, r.detail.clone());
    }
    (GateState::Pass, r.detail.clone())
}

fn render(reports: &[Report]) {
    let w = reports.iter().map(|r| r.gate.len()).max().unwrap_or(4).max(4);
    println!(
        "{:<w$}  {:<14}  {:>10}  {:>8}  DETAIL",
        "GATE",
        "STATE",
        "ASSERTIONS",
        "ELAPSED",
        w = w
    );
    println!("{}", "-".repeat(w + 50));
    for r in reports {
        let a = if r.state == GateState::NotApplicable {
            "-".to_string()
        } else {
            format!("{}/{}", r.assertions, r.expected)
        };
        println!(
            "{:<w$}  {:<14}  {:>10}  {:>7.1}s  {}",
            r.gate,
            r.state.label(),
            a,
            r.elapsed_s,
            r.detail,
            w = w
        );
    }
}

fn write_json(path: &Path, reports: &[Report], verdict: SuiteVerdict) -> std::io::Result<()> {
    if let Some(p) = path.parent() {
        std::fs::create_dir_all(p)?;
    }
    let rows: Vec<serde_json::Value> = reports
        .iter()
        .map(|r| {
            serde_json::json!({
                "gate": r.gate,
                "state": r.state.label(),
                "assertions": r.assertions,
                "expected": r.expected,
                "elapsed_s": r.elapsed_s,
                "detail": r.detail,
            })
        })
        .collect();
    let doc = serde_json::json!({
        "verdict": verdict.label(),
        "exit_code": verdict.exit_code(),
        "platform": Platform::current().label(),
        "gates": rows,
    });
    std::fs::write(path, serde_json::to_vec_pretty(&doc)?)
}

fn list(_root: &Path) -> i32 {
    let w = GATES.iter().map(|g| g.name.len()).max().unwrap_or(4);
    println!(
        "{:<w$}  {:<5}  {:<18}  {:>7}  {:>8}  {:<11}  SUMMARY",
        "GATE",
        "TIER",
        "PLATFORMS",
        "BUDGET",
        "EXPECTED",
        "FALSIFIER",
        w = w
    );
    println!("{}", "-".repeat(w + 70));
    for g in GATES {
        println!(
            "{:<w$}  {:<5}  {:<18}  {:>6}s  {:>8}  {:<11}  {}",
            g.name,
            g.tier.label(),
            g.platforms.labels().join(","),
            g.budget_s,
            g.expected,
            match g.expect {
                Expect::Falsifiable => "must-go-red",
                Expect::Vacuous => "MUST-BE-VACUOUS",
            },
            g.summary,
            w = w
        );
        for m in g.mutations.as_slice() {
            println!("{:<w$}    ↳ {}: {}", "", m.id, m.description, w = w);
        }
    }
    0
}

// ---------------------------------------------------------------------------
// falsifier mode
// ---------------------------------------------------------------------------

fn run_falsifiers(o: &Opts, root: &Path) -> i32 {
    let exe = match std::env::current_exe() {
        Ok(e) => e,
        Err(e) => {
            eprintln!("xtask harness: cannot locate own binary: {e}");
            return 2;
        }
    };
    let fopts = FalsifyOpts {
        exe,
        repo_root: root.to_path_buf(),
        scratch: bodies::scratch(root),
    };
    let mut generation = 1000u64;
    let mut all = Vec::new();

    for g in GATES {
        if !g.platforms.contains(Platform::current()) {
            continue;
        }
        if !o.only.is_empty() && !o.only.iter().any(|n| n == g.name) {
            continue;
        }
        // The hang self-test never passes by design, so its baseline can never
        // be green and falsifying it is meaningless. It is exercised by the
        // harness's own tests instead.
        if g.name == "selftest-hang" && o.only.is_empty() {
            continue;
        }
        // A blocked gate has no green baseline to falsify — by declaration it
        // does not run. Its own falsifying property (the expiry flipping it to
        // FAIL) is harness logic, not a gate assertion, and is proven by
        // `registry`'s and `state`'s unit tests instead of by a mutation run.
        if registry::block_for(g.name).is_some() {
            continue;
        }
        all.extend(falsify::verify_gate(g, &fopts, &mut generation));
    }

    let w = all.iter().map(|r| r.gate.len()).max().unwrap_or(4).max(4);
    println!("{:<w$}  {:<28}  {:<13}  DETAIL", "GATE", "MUTATION", "OUTCOME", w = w);
    println!("{}", "-".repeat(w + 60));
    for r in &all {
        println!(
            "{:<w$}  {:<28}  {:<13}  {}",
            r.gate,
            r.mutation,
            r.outcome.label(),
            r.detail,
            w = w
        );
    }

    let bad: Vec<&falsify::FalsifyReport> = all.iter().filter(|r| !r.as_declared).collect();
    // Record proofs BEFORE deciding, so a partial run still banks what it proved.
    if let Err(e) = Proofs::record(root, &all) {
        eprintln!("xtask harness: cannot write {PROOF_LEDGER}: {e}");
    }

    if bad.is_empty() {
        println!(
            "\nFALSIFIER GATE: PASS  ({} mutation(s) behaved as declared, canary included)",
            all.len()
        );
        0
    } else {
        println!("\nFALSIFIER GATE: FAIL");
        for r in &bad {
            println!(
                "  {} / {}: expected {}, got {} — {}",
                r.gate,
                r.mutation,
                match registry::find(r.gate).map(|g| g.expect) {
                    Some(Expect::Vacuous) => "VACUOUS",
                    _ => "PROVEN",
                },
                r.outcome.label(),
                r.detail
            );
        }
        1
    }
}

/// The falsification-proof ledger.
struct Proofs {
    entries: serde_json::Map<String, serde_json::Value>,
}

impl Proofs {
    fn load(root: &Path) -> Proofs {
        let entries = std::fs::read_to_string(root.join(PROOF_LEDGER))
            .ok()
            .and_then(|s| serde_json::from_str::<serde_json::Value>(&s).ok())
            .and_then(|v| v.get("gates").cloned())
            .and_then(|v| v.as_object().cloned())
            .unwrap_or_default();
        Proofs { entries }
    }

    /// Is this gate's falsification proven within the window?
    fn fresh(&self, gate: &str) -> bool {
        let Some(e) = self.entries.get(gate) else {
            return false;
        };
        if e.get("outcome").and_then(|o| o.as_str()) != Some("as-declared") {
            return false;
        }
        let at = e.get("proven_at_unix").and_then(|v| v.as_u64()).unwrap_or(0);
        let now = now_unix();
        now.saturating_sub(at) <= PROOF_WINDOW_DAYS * 86_400
    }

    fn record(root: &Path, reports: &[falsify::FalsifyReport]) -> std::io::Result<()> {
        let path = root.join(PROOF_LEDGER);
        if let Some(p) = path.parent() {
            std::fs::create_dir_all(p)?;
        }
        let mut map = Proofs::load(root).entries;
        let now = now_unix();
        for r in reports {
            map.insert(
                r.gate.to_string(),
                serde_json::json!({
                    "mutation": r.mutation,
                    "observed": r.outcome.label(),
                    "outcome": if r.as_declared { "as-declared" } else { "NOT-as-declared" },
                    "proven_at_unix": now,
                }),
            );
        }
        let doc = serde_json::json!({
            "note": "Written by `xtask harness --verify-falsifiers`. A gate absent here, \
                     or older than the declared window, renders UNPROVEN under \
                     `--require-proofs`.",
            "window_days": PROOF_WINDOW_DAYS,
            "gates": map,
        });
        std::fs::write(&path, serde_json::to_vec_pretty(&doc)?)
    }
}

fn now_unix() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
}

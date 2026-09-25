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
pub mod proof_inputs;
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

/// Redirects the proof ledger away from the tracked file.
///
/// `--verify-falsifiers` records what it proved, which is correct for the
/// COMMAND and wrong for a TEST: `tests/harness_e2e.rs` runs the canary through
/// the real binary, so an ordinary `cargo test -p xtask` rewrote
/// `docs/coverage/falsifier-proofs.json` and left the working tree dirty. A
/// checked-in proof that any test run refreshes is not evidence of anything —
/// it is a timestamp that follows the observer around, and it means `git status`
/// after a test run can never be trusted to be clean.
///
/// Set this to a scratch path and the run banks its proofs there instead.
const PROOF_LEDGER_ENV: &str = "SKY_PROOF_LEDGER";

/// Where this process should read and write the proof ledger.
fn proof_ledger_path(root: &Path) -> PathBuf {
    match std::env::var(PROOF_LEDGER_ENV) {
        Ok(p) if !p.trim().is_empty() => PathBuf::from(p),
        _ => root.join(PROOF_LEDGER),
    }
}

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

    if let Some(g) = &opts.explain_inputs {
        return explain_inputs(g, root);
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
                                 goes red; records proofs to docs/coverage/. Sweeps the
                                 whole registry, or the gates of `--tier`/`--only` when
                                 given, so nightly can verify one tier at a time.
                                 INCREMENTAL: a gate is re-proven only when the digest
                                 of its proof inputs changed (see --explain-inputs), or
                                 its proof is missing, not as declared, or out of the
                                 window. Every other gate is CARRIED and reported so.
  --all                          with --verify-falsifiers: re-prove every selected gate,
                                 carrying nothing (nightly and release use this)
  --explain-inputs <gate>        print the files and digest a gate's proof depends on
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
    all: bool,
    explain_inputs: Option<String>,
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
                "--all" => o.all = true,
                "--explain-inputs" => {
                    o.explain_inputs = Some(value(args, &mut i, "--explain-inputs")?)
                }
                "--list" => o.list = true,
                "-h" | "--help" => o.help = true,
                other => return Err(format!("unknown option `{other}`")),
            }
            i += 1;
        }
        // An unknown gate name in `--only` must be an ERROR, never an empty
        // selection that trivially passes. This is the same class as `xtask`
        // exiting 0 on an unknown subcommand.
        if o.all && !o.verify_falsifiers {
            return Err("--all only applies to --verify-falsifiers".into());
        }
        for name in o.only.iter().chain(o.explain_inputs.iter()) {
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
    // A proof whose inputs changed since it was taken proves nothing about the
    // gate as it now is. Digests are computed only when they are consulted.
    let digests = if o.require_proofs {
        let sel: Vec<&Gate> = GATES.iter().filter(|g| selected(g, o)).collect();
        proof_inputs::fingerprint(root, &sel)
    } else {
        Default::default()
    };
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
        let digest = digests.get(g.name).and_then(|d| d.as_ref().ok());
        if st == GateState::Pass && o.require_proofs && !proofs.fresh(g.name, digest) {
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
                format!(
                    "{detail} — but no falsification proof within {PROOF_WINDOW_DAYS}d \
                     whose inputs match the tree (`harness --verify-falsifiers --only {}`)",
                    g.name
                )
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
    let w = reports
        .iter()
        .map(|r| r.gate.len())
        .max()
        .unwrap_or(4)
        .max(4);
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

    let selection = falsifier_selection(o);

    // Digest every selected gate's proof inputs NOW, before any mutation is
    // applied, so a digest always describes the clean tree. A reverted mutation
    // restores byte-identical content, so a later digest would agree — but
    // "later" would be taken while a sibling's mutation could be journalled.
    let digests = proof_inputs::fingerprint(root, &selection);
    let proofs = Proofs::load(root);
    let now = now_unix();
    let mut carried: Vec<(&'static str, u64)> = Vec::new();
    let mut reproved: Vec<(&'static str, String)> = Vec::new();

    for g in selection {
        let digest = digests.get(g.name).and_then(|d| d.as_ref().ok());
        match decide(proofs.entries.get(g.name), g, digest, now, o.all) {
            Decision::Carry { proven_at } => {
                carried.push((g.name, proven_at));
                continue;
            }
            Decision::Reprove(why) => {
                let why = match digests.get(g.name) {
                    Some(Err(e)) => format!("{why} (inputs unresolvable: {e})"),
                    _ => why,
                };
                reproved.push((g.name, why));
            }
        }
        all.extend(falsify::verify_gate(g, &fopts, &mut generation));
    }

    let w = all.iter().map(|r| r.gate.len()).max().unwrap_or(4).max(4);
    println!(
        "{:<w$}  {:<28}  {:<13}  DETAIL",
        "GATE",
        "MUTATION",
        "OUTCOME",
        w = w
    );
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
    if let Err(e) = Proofs::record(root, &all, &digests) {
        eprintln!("xtask harness: cannot write {PROOF_LEDGER}: {e}");
    }

    println!(
        "\nRE-PROVEN {} gate(s), CARRIED {} gate(s){}",
        reproved.len(),
        carried.len(),
        if o.all {
            " (--all: nothing carried)"
        } else {
            " (inputs unchanged since their recorded proof)"
        }
    );
    for (g, why) in &reproved {
        println!("  re-proven  {g}: {why}");
    }
    for (g, at) in &carried {
        println!(
            "  carried    {g}: proof taken {}d ago, inputs digest unchanged",
            now.saturating_sub(*at) / 86_400
        );
    }

    if bad.is_empty() {
        println!(
            "\nFALSIFIER GATE: PASS  ({} mutation(s) behaved as declared, canary included; \
             {} gate(s) carried)",
            all.len(),
            carried.len()
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

/// Print the inputs a gate's proof depends on, and their digest.
fn explain_inputs(name: &str, root: &Path) -> i32 {
    let Some(g) = registry::find(name) else {
        eprintln!("xtask harness: unknown gate `{name}`");
        return 2;
    };
    match proof_inputs::inputs_for(root, g) {
        Ok(inp) => {
            println!("proof inputs of `{name}`:");
            for p in &inp.paths {
                println!("  {p}");
            }
            println!(
                "  + the registration and the body closure ({} bytes of source)",
                inp.registration.len()
            );
            println!(
                "  body closure: {}",
                inp.items.iter().cloned().collect::<Vec<_>>().join(", ")
            );
        }
        Err(e) => {
            println!("proof inputs of `{name}` cannot be resolved: {e}");
            println!("  an incremental run re-proves this gate every time");
            return 1;
        }
    }
    match proof_inputs::fingerprint(root, &[g]).remove(name) {
        Some(Ok(d)) => println!("digest {} over {} tracked file(s)", d.hash, d.files),
        Some(Err(e)) => println!("digest unavailable: {e}"),
        None => {}
    }
    0
}

/// Whether a falsifier run re-proves a gate or carries its recorded proof.
#[derive(Debug, PartialEq, Eq)]
enum Decision {
    Carry { proven_at: u64 },
    Reprove(String),
}

/// The carry rule. A proof is carried ONLY when every one of these holds:
/// `--all` is not set; it is not the canary (the runner's own self-check runs
/// every time, and costs nothing); the ledger has a record; the record is as
/// declared, against a mutation the registry still declares; it is within the
/// freshness window; and its recorded inputs digest equals the digest of the
/// tree now. A missing digest on EITHER side (a legacy record, or inputs that
/// cannot be resolved) re-proves.
fn decide(
    entry: Option<&serde_json::Value>,
    g: &Gate,
    digest: Option<&proof_inputs::Digest>,
    now: u64,
    force_all: bool,
) -> Decision {
    if force_all {
        return Decision::Reprove("--all".into());
    }
    if g.expect == Expect::Vacuous {
        return Decision::Reprove("the canary is re-run by every falsifier run".into());
    }
    let Some(e) = entry else {
        return Decision::Reprove("no recorded proof".into());
    };
    if e.get("outcome").and_then(|o| o.as_str()) != Some("as-declared") {
        return Decision::Reprove("the recorded proof is not as declared".into());
    }
    if !recorded_mutations_declared(e, g) {
        return Decision::Reprove("the recorded mutation(s) differ from the registry".into());
    }
    let at = e
        .get("proven_at_unix")
        .and_then(|v| v.as_u64())
        .unwrap_or(0);
    if now.saturating_sub(at) > PROOF_WINDOW_DAYS * 86_400 {
        return Decision::Reprove(format!("the proof is older than {PROOF_WINDOW_DAYS}d"));
    }
    let Some(d) = digest else {
        return Decision::Reprove("the inputs digest cannot be computed".into());
    };
    match e.get("inputs_hash").and_then(|h| h.as_str()) {
        None => Decision::Reprove("the proof records no inputs digest".into()),
        Some(h) if h != d.hash => Decision::Reprove("its inputs changed since the proof".into()),
        Some(_) => Decision::Carry { proven_at: at },
    }
}

/// Every mutation id the ledger entry records is one the gate still declares,
/// and — when the entry lists them all — it lists every declared one.
fn recorded_mutations_declared(e: &serde_json::Value, g: &Gate) -> bool {
    let declared: Vec<&str> = g.mutations.as_slice().iter().map(|m| m.id).collect();
    if let Some(list) = e.get("mutations").and_then(|m| m.as_array()) {
        let recorded: Vec<&str> = list.iter().filter_map(|v| v.as_str()).collect();
        return declared.iter().all(|d| recorded.contains(d))
            && recorded.iter().all(|r| declared.contains(r));
    }
    let one = e
        .get("mutation")
        .and_then(|m| m.as_str())
        .unwrap_or_default();
    // A legacy single-mutation record only vouches for a single-mutation gate.
    declared.len() == 1 && declared[0] == one
}

/// The gates a falsifier run considers, in registry order.
fn falsifier_selection(o: &Opts) -> Vec<&'static Gate> {
    let mut out = Vec::new();
    for g in GATES {
        if !g.platforms.contains(Platform::current()) {
            continue;
        }
        // A blocked gate has no green baseline to falsify — by declaration it
        // does not run. Its own falsifying property (the expiry flipping it to
        // FAIL) is harness logic, not a gate assertion, and is proven by
        // `registry`'s and `state`'s unit tests instead of by a mutation run.
        if registry::block_for(g.name).is_some() {
            continue;
        }
        // The canary proves the RUNNER can say "this proved nothing". It runs
        // in every falsifier run, whatever the selection — it is instant, and a
        // run whose runner is broken must not be able to report PASS.
        if g.expect == Expect::Vacuous {
            out.push(g);
            continue;
        }
        if !o.only.is_empty() {
            // Deliberate selection overrides everything, as for `run_suite`.
            if !o.only.iter().any(|n| n == g.name) {
                continue;
            }
        } else if let Some(tier) = o.tier {
            // `--tier` scopes the sweep to one tier, so a nightly job can verify
            // the falsifiers of exactly the gates it has the environment for —
            // the full-registry sweep needs every gate's world (Neovim, real
            // servers, a cold FFI install) at once and cannot fit one runner.
            // Applied ONLY when a tier is named: a bare `--verify-falsifiers`
            // with no `--tier` and no `--only` still sweeps the whole registry,
            // which is the behaviour `tests/harness_e2e.rs` and the release
            // path depend on.
            if g.tier != tier {
                continue;
            }
        }
        // The hang self-test never passes by design, so its baseline can never
        // be green and falsifying it is meaningless. It is exercised by the
        // harness's own tests instead.
        if g.name == "selftest-hang" && o.only.is_empty() {
            continue;
        }
        out.push(g);
    }
    out
}

/// The falsification-proof ledger.
struct Proofs {
    entries: serde_json::Map<String, serde_json::Value>,
}

impl Proofs {
    fn load(root: &Path) -> Proofs {
        let entries = std::fs::read_to_string(proof_ledger_path(root))
            .ok()
            .and_then(|s| serde_json::from_str::<serde_json::Value>(&s).ok())
            .and_then(|v| v.get("gates").cloned())
            .and_then(|v| v.as_object().cloned())
            .unwrap_or_default();
        Proofs { entries }
    }

    /// Is this gate's falsification proven within the window, AGAINST A
    /// MUTATION THE REGISTRY STILL DECLARES?
    ///
    /// The last clause is not decoration. A proof is evidence about a
    /// (gate, mutation) pair; renaming or replacing the mutation retires the
    /// evidence with it. Reading only `observed` let `config-matrix` render
    /// PROVEN under `--require-proofs` on a record taken against
    /// `config-matrix.claim-a-dead-builder-is-alive`, which commit `4a118e39`
    /// had deleted — the same defect the coverage ledger carried.
    fn fresh(&self, gate: &str, digest: Option<&proof_inputs::Digest>) -> bool {
        let Some(e) = self.entries.get(gate) else {
            return false;
        };
        if e.get("outcome").and_then(|o| o.as_str()) != Some("as-declared") {
            return false;
        }
        let recorded = e
            .get("mutation")
            .and_then(|m| m.as_str())
            .unwrap_or_default();
        let declared = registry::GATES
            .iter()
            .find(|g| g.name == gate)
            .is_some_and(|g| g.mutations.as_slice().iter().any(|m| m.id == recorded));
        if !declared {
            return false;
        }
        let at = e
            .get("proven_at_unix")
            .and_then(|v| v.as_u64())
            .unwrap_or(0);
        let now = now_unix();
        if now.saturating_sub(at) > PROOF_WINDOW_DAYS * 86_400 {
            return false;
        }
        // A proof that records the digest of its inputs vouches only for a tree
        // whose inputs still hash the same. A proof taken before digests were
        // recorded has nothing to compare and is judged by its age alone.
        match e.get("inputs_hash").and_then(|h| h.as_str()) {
            None => true,
            Some(h) => digest.is_some_and(|d| d.hash == h),
        }
    }

    /// Bank a run's outcomes, ONE record per gate.
    ///
    /// A gate with several mutations used to get one record per mutation, each
    /// overwriting the last — so a gate whose FIRST mutation stayed green and
    /// whose second went red was recorded as proven. The record is now the
    /// conjunction: as declared only when every mutation was.
    fn record(
        root: &Path,
        reports: &[falsify::FalsifyReport],
        digests: &std::collections::BTreeMap<String, Result<proof_inputs::Digest, String>>,
    ) -> std::io::Result<()> {
        let path = proof_ledger_path(root);
        if let Some(p) = path.parent() {
            std::fs::create_dir_all(p)?;
        }
        let mut map = Proofs::load(root).entries;
        let now = now_unix();
        let mut order: Vec<&'static str> = Vec::new();
        for r in reports {
            if !order.contains(&r.gate) {
                order.push(r.gate);
            }
        }
        for gate in order {
            let rs: Vec<&falsify::FalsifyReport> =
                reports.iter().filter(|r| r.gate == gate).collect();
            let inconclusive = |r: &falsify::FalsifyReport| {
                matches!(r.outcome, falsify::Falsified::Inconclusive(_))
            };
            // Real evidence against the gate: a mutation that ran and did not
            // behave as declared. It always overwrites.
            let defect = rs.iter().find(|r| !r.as_declared && !inconclusive(r));
            // AN INCONCLUSIVE RUN MUST NOT ERASE A RECORDED PROOF.
            //
            // `INCONCLUSIVE` means the run could not establish anything —
            // typically because the gate's BASELINE was red for an
            // environmental reason. It is not evidence that the mutation fails
            // to falsify; it is the absence of evidence either way.
            //
            // Demonstrated on this branch: a full `--verify-falsifiers` sweep on
            // a host with no `SKY_TEST_POSTGRES_DSN` reported INCONCLUSIVE for
            // `apps-ledger-postgres` and `apps-fleet` — correctly, they cannot
            // run without a server — and then OVERWROTE their `PROVEN` records
            // with `NOT-as-declared`. `--require-proofs` would then have
            // rendered both UNPROVEN and the coverage ledger would have scored
            // their surfaces down, all because of a missing env var on a
            // laptop. That is the same class as a `--bless` dropping a row for
            // a project that did not emit locally: an environment-dependent run
            // destroying a measurement taken somewhere it WAS possible.
            //
            // The existing record is left alone instead — including its inputs
            // digest, which describes the tree it WAS taken on, so an
            // incremental run re-attempts the gate next time rather than
            // carrying it. It carries its own 30-day freshness window, so a
            // proof that is never re-established still expires on its own.
            // Only the timestamp of the failed attempt is noted, so the attempt
            // is visible rather than silent.
            if defect.is_none() {
                if let Some(first_inconclusive) = rs.iter().find(|r| inconclusive(r)) {
                    if let Some(existing) = map.get_mut(gate) {
                        if existing.get("outcome").and_then(|o| o.as_str()) == Some("as-declared") {
                            if let Some(obj) = existing.as_object_mut() {
                                obj.insert(
                                    "last_inconclusive_at_unix".into(),
                                    serde_json::json!(now),
                                );
                            }
                            continue;
                        }
                    }
                    map.insert(
                        gate.to_string(),
                        serde_json::json!({
                            "mutation": first_inconclusive.mutation,
                            "mutations": rs.iter().map(|r| r.mutation).collect::<Vec<_>>(),
                            "observed": first_inconclusive.outcome.label(),
                            "outcome": "NOT-as-declared",
                            "proven_at_unix": now,
                        }),
                    );
                    continue;
                }
            }
            let (head, as_declared) = match defect {
                Some(d) => (*d, false),
                None => (rs[0], true),
            };
            let mut entry = serde_json::json!({
                "mutation": head.mutation,
                "mutations": rs.iter().map(|r| r.mutation).collect::<Vec<_>>(),
                "observed": head.outcome.label(),
                "outcome": if as_declared { "as-declared" } else { "NOT-as-declared" },
                "proven_at_unix": now,
            });
            // The digest of the inputs this proof was taken against. Absent when
            // it could not be computed, which makes the next incremental run
            // re-prove the gate and `--require-proofs` judge it by age.
            if let Some(Ok(d)) = digests.get(gate) {
                entry["inputs_hash"] = serde_json::json!(d.hash);
                entry["inputs_files"] = serde_json::json!(d.files);
            }
            map.insert(gate.to_string(), entry);
        }
        let doc = serde_json::json!({
            "note": "Written by `xtask harness --verify-falsifiers`. A gate absent here, \
                     older than the declared window, or whose `inputs_hash` no longer \
                     matches its proof inputs (`harness --explain-inputs <gate>`), \
                     renders UNPROVEN under `--require-proofs` and is re-proven by the \
                     next incremental run.",
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

#[cfg(test)]
mod proof_ledger_tests {
    use super::*;
    use falsify::{Falsified, FalsifyReport};

    fn report(gate: &'static str, outcome: Falsified) -> FalsifyReport {
        let as_declared = matches!(outcome, Falsified::Proven);
        FalsifyReport {
            gate,
            mutation: "m",
            outcome,
            as_declared,
            detail: String::new(),
        }
    }

    fn write_ledger(dir: &Path, body: &str) {
        std::fs::create_dir_all(dir.join("docs/coverage")).unwrap();
        std::fs::write(dir.join(PROOF_LEDGER), body).unwrap();
    }

    fn scratch_dir(name: &str) -> PathBuf {
        let d = std::env::temp_dir().join(format!("sky-proof-{name}-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&d);
        std::fs::create_dir_all(&d).unwrap();
        d
    }

    /// THE REGRESSION. An INCONCLUSIVE run must not erase a recorded proof.
    ///
    /// Observed for real: a full `--verify-falsifiers` sweep on a host with no
    /// `SKY_TEST_POSTGRES_DSN` overwrote `apps-ledger-postgres`' and
    /// `apps-fleet`'s `PROVEN` records with `NOT-as-declared`, because their
    /// baselines could not run at all. `--require-proofs` would then have
    /// rendered both UNPROVEN, and the coverage ledger would have scored their
    /// surfaces down — over a missing environment variable on a laptop.
    #[test]
    fn an_inconclusive_run_does_not_erase_a_recorded_proof() {
        let root = scratch_dir("keeps");
        write_ledger(
            &root,
            r#"{"gates":{"apps-fleet":{"mutation":"m","observed":"PROVEN",
                "outcome":"as-declared","proven_at_unix":1786369944}}}"#,
        );

        Proofs::record(
            &root,
            &[report(
                "apps-fleet",
                Falsified::Inconclusive("no DSN".into()),
            )],
            &Default::default(),
        )
        .unwrap();

        let after = Proofs::load(&root);
        let e = after
            .entries
            .get("apps-fleet")
            .expect("the row must survive");
        assert_eq!(e["outcome"], "as-declared", "the proof was erased");
        assert_eq!(e["observed"], "PROVEN");
        assert_eq!(
            e["proven_at_unix"], 1786369944,
            "the proof's age must not be refreshed"
        );
        // The failed attempt is visible rather than silent.
        assert!(e.get("last_inconclusive_at_unix").is_some());
    }

    /// The other direction: INCONCLUSIVE must still be RECORDED when there is
    /// no prior proof to protect. Silence would read as "never attempted".
    #[test]
    fn an_inconclusive_run_is_recorded_when_there_is_no_prior_proof() {
        let root = scratch_dir("fresh");
        write_ledger(&root, r#"{"gates":{}}"#);

        Proofs::record(
            &root,
            &[report(
                "apps-fleet",
                Falsified::Inconclusive("no DSN".into()),
            )],
            &Default::default(),
        )
        .unwrap();

        let after = Proofs::load(&root);
        let e = after.entries.get("apps-fleet").expect("must be recorded");
        assert_eq!(e["outcome"], "NOT-as-declared");
        assert!(
            !after.fresh("apps-fleet", None),
            "INCONCLUSIVE must never render a gate proven"
        );
    }

    /// A real VACUOUS on a `Falsifiable` gate is a genuine defect finding and
    /// MUST overwrite a prior proof — it is evidence, not the absence of it.
    /// Only INCONCLUSIVE is protective.
    #[test]
    fn a_vacuous_result_still_overwrites_a_prior_proof() {
        let root = scratch_dir("vacuous");
        write_ledger(
            &root,
            r#"{"gates":{"roundtrip":{"mutation":"m","observed":"PROVEN",
                "outcome":"as-declared","proven_at_unix":1786369944}}}"#,
        );

        Proofs::record(
            &root,
            &[report("roundtrip", Falsified::Vacuous)],
            &Default::default(),
        )
        .unwrap();

        let after = Proofs::load(&root);
        let e = after.entries.get("roundtrip").unwrap();
        assert_eq!(e["observed"], "VACUOUS");
        assert_eq!(e["outcome"], "NOT-as-declared");
        assert!(!after.fresh("roundtrip", None));
    }
}

#[cfg(test)]
mod incremental_tests {
    use super::*;
    use falsify::{Falsified, FalsifyReport};
    use proof_inputs::Digest;

    const NOW: u64 = 2_000_000_000;

    fn gate(name: &str) -> &'static Gate {
        registry::find(name).expect("registered")
    }

    fn digest(h: &str) -> Digest {
        Digest {
            hash: h.into(),
            files: 1,
        }
    }

    fn proof(mutation: &str, hash: Option<&str>, at: u64) -> serde_json::Value {
        let mut v = serde_json::json!({
            "mutation": mutation,
            "observed": "PROVEN",
            "outcome": "as-declared",
            "proven_at_unix": at,
        });
        if let Some(h) = hash {
            v["inputs_hash"] = serde_json::json!(h);
        }
        v
    }

    /// An unchanged input set carries the recorded proof: no re-run.
    #[test]
    fn an_unchanged_input_carries_the_proof() {
        let g = gate("reject");
        let e = proof("reject.neutralise-axis", Some("abc"), NOW - 60);
        assert_eq!(
            decide(Some(&e), g, Some(&digest("abc")), NOW, false),
            Decision::Carry {
                proven_at: NOW - 60
            }
        );
    }

    /// A changed input forces a re-proof.
    #[test]
    fn a_changed_input_forces_a_reproof() {
        let g = gate("reject");
        let e = proof("reject.neutralise-axis", Some("abc"), NOW - 60);
        assert!(matches!(
            decide(Some(&e), g, Some(&digest("abd")), NOW, false),
            Decision::Reprove(_)
        ));
    }

    /// A missing proof is re-proven.
    #[test]
    fn a_missing_proof_is_reproven() {
        assert!(matches!(
            decide(None, gate("reject"), Some(&digest("abc")), NOW, false),
            Decision::Reprove(_)
        ));
    }

    /// Every other way a carry could be wrong re-proves instead.
    #[test]
    fn anything_short_of_a_matching_current_proof_is_reproven() {
        let g = gate("reject");
        let m = "reject.neutralise-axis";
        let d = digest("abc");
        let mut not_declared = proof(m, Some("abc"), NOW);
        not_declared["outcome"] = serde_json::json!("NOT-as-declared");
        let cases: Vec<(&str, serde_json::Value, Option<&Digest>, bool)> = vec![
            ("--all", proof(m, Some("abc"), NOW), Some(&d), true),
            ("legacy record", proof(m, None, NOW), Some(&d), false),
            (
                "uncomputable digest",
                proof(m, Some("abc"), NOW),
                None,
                false,
            ),
            (
                "out of window",
                proof(m, Some("abc"), NOW - (PROOF_WINDOW_DAYS + 1) * 86_400),
                Some(&d),
                false,
            ),
            (
                "retired mutation",
                proof("reject.gone", Some("abc"), NOW),
                Some(&d),
                false,
            ),
            ("not as declared", not_declared, Some(&d), false),
        ];
        for (why, e, dg, all) in cases {
            assert!(
                matches!(decide(Some(&e), g, dg, NOW, all), Decision::Reprove(_)),
                "{why}: must re-prove"
            );
        }
    }

    /// The canary is never carried: it is the runner's own self-check, and a
    /// narrowed run still runs it.
    #[test]
    fn the_canary_is_never_carried() {
        let e = proof("canary.no-op", Some("abc"), NOW);
        assert!(matches!(
            decide(Some(&e), gate("canary"), Some(&digest("abc")), NOW, false),
            Decision::Reprove(_)
        ));
        let o = Opts {
            only: vec!["reject".into()],
            ..Default::default()
        };
        assert!(
            falsifier_selection(&o).iter().any(|g| g.name == "canary"),
            "a narrowed run must still run the canary"
        );
    }

    /// A multi-mutation gate is recorded as the CONJUNCTION of its mutations.
    /// Before, the last mutation's record overwrote the first's, so a gate
    /// whose first mutation stayed green could be recorded as proven.
    #[test]
    fn a_gate_is_proven_only_when_every_mutation_is() {
        let root = std::env::temp_dir().join(format!("sky-proof-conj-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&root);
        std::fs::create_dir_all(&root).unwrap();
        let mk = |mutation: &'static str, proven: bool| FalsifyReport {
            gate: "spa-diff-fuzz",
            mutation,
            outcome: if proven {
                Falsified::Proven
            } else {
                Falsified::Vacuous
            },
            as_declared: proven,
            detail: String::new(),
        };
        let mut digests = std::collections::BTreeMap::new();
        digests.insert("spa-diff-fuzz".to_string(), Ok(digest("abc")));

        let first_vacuous = [
            mk("spa-diff-fuzz.drop-msgarg-rename", false),
            mk("spa-diff-fuzz.drop-read-field", true),
        ];
        Proofs::record(&root, &first_vacuous, &digests).unwrap();
        let after = Proofs::load(&root);
        let e = &after.entries["spa-diff-fuzz"];
        assert_eq!(e["outcome"], "NOT-as-declared");
        assert_eq!(e["mutation"], "spa-diff-fuzz.drop-msgarg-rename");
        assert!(!after.fresh("spa-diff-fuzz", Some(&digest("abc"))));

        // Both proven: as declared, with the digest recorded and honoured.
        let both = [
            mk("spa-diff-fuzz.drop-msgarg-rename", true),
            mk("spa-diff-fuzz.drop-read-field", true),
        ];
        Proofs::record(&root, &both, &digests).unwrap();
        let after = Proofs::load(&root);
        assert_eq!(after.entries["spa-diff-fuzz"]["inputs_hash"], "abc");
        assert!(after.fresh("spa-diff-fuzz", Some(&digest("abc"))));
        assert!(
            !after.fresh("spa-diff-fuzz", Some(&digest("abd"))),
            "--require-proofs must not accept a proof whose inputs changed"
        );
        assert!(matches!(
            decide(
                after.entries.get("spa-diff-fuzz"),
                gate("spa-diff-fuzz"),
                Some(&digest("abc")),
                now_unix(),
                false
            ),
            Decision::Carry { .. }
        ));
    }
}

#[cfg(test)]
mod proof_ledger_location_tests {
    use super::{proof_ledger_path, PROOF_LEDGER, PROOF_LEDGER_ENV};
    use std::path::Path;

    /// All three cases in ONE test, deliberately.
    ///
    /// `std::env::set_var` mutates process-wide state, and cargo runs tests in
    /// the same process on multiple threads — as three separate tests these
    /// raced and two failed, which is a flaky gate rather than a broken
    /// behaviour. One sequential test is the honest shape for a process-global.
    #[test]
    fn the_proof_ledger_honours_an_explicit_path_and_nothing_else() {
        // 1. Default: the tracked file. Production behaviour must not change
        //    just because a redirect exists.
        std::env::remove_var(PROOF_LEDGER_ENV);
        assert_eq!(
            proof_ledger_path(Path::new("/repo")),
            Path::new("/repo").join(PROOF_LEDGER),
            "with no override the proof ledger must stay the checked-in file"
        );

        // 2. Redirected. This is what stops `cargo test -p xtask` rewriting a
        //    TRACKED file: `tests/harness_e2e.rs` drives `--verify-falsifiers`
        //    against the real repo, and recording a proof is correct for the
        //    command. Before the redirect, an ordinary test run left
        //    `docs/coverage/falsifier-proofs.json` modified, so `git status`
        //    after testing was never clean — and a dirty tree is how 1928 build
        //    artefacts were swept into a commit earlier in this cycle.
        std::env::set_var(PROOF_LEDGER_ENV, "/tmp/scratch-ledger.json");
        assert_eq!(
            proof_ledger_path(Path::new("/repo")),
            Path::new("/tmp/scratch-ledger.json"),
            "an explicit ledger path must be honoured, or the e2e suite writes \
             to the tracked file again"
        );

        // 3. An empty value is NOT a redirect. Otherwise `SKY_PROOF_LEDGER=` in
        //    a shell profile would silently send proofs to the filesystem root —
        //    the same shape as the `CARGO_TARGET_DIR` pointing at a binary
        //    directory that produced three false diagnoses this cycle.
        std::env::set_var(PROOF_LEDGER_ENV, "");
        assert_eq!(
            proof_ledger_path(Path::new("/repo")),
            Path::new("/repo").join(PROOF_LEDGER),
            "an empty override must fall back, not redirect to nowhere"
        );

        std::env::remove_var(PROOF_LEDGER_ENV);
    }
}

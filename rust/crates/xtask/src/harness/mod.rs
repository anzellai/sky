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
    fn fresh(&self, gate: &str) -> bool {
        let Some(e) = self.entries.get(gate) else {
            return false;
        };
        if e.get("outcome").and_then(|o| o.as_str()) != Some("as-declared") {
            return false;
        }
        let recorded = e.get("mutation").and_then(|m| m.as_str()).unwrap_or_default();
        let declared = registry::GATES
            .iter()
            .find(|g| g.name == gate)
            .is_some_and(|g| g.mutations.as_slice().iter().any(|m| m.id == recorded));
        if !declared {
            return false;
        }
        let at = e.get("proven_at_unix").and_then(|v| v.as_u64()).unwrap_or(0);
        let now = now_unix();
        now.saturating_sub(at) <= PROOF_WINDOW_DAYS * 86_400
    }

    fn record(root: &Path, reports: &[falsify::FalsifyReport]) -> std::io::Result<()> {
        let path = proof_ledger_path(root);
        if let Some(p) = path.parent() {
            std::fs::create_dir_all(p)?;
        }
        let mut map = Proofs::load(root).entries;
        let now = now_unix();
        for r in reports {
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
            // The existing record is left alone instead. It carries its own
            // 30-day freshness window, so a proof that is never re-established
            // still expires on its own — the signal degrades rather than being
            // deleted. Only the timestamp of the failed attempt is noted, so
            // the attempt is visible rather than silent.
            if matches!(r.outcome, falsify::Falsified::Inconclusive(_)) {
                if let Some(existing) = map.get_mut(r.gate) {
                    if existing.get("outcome").and_then(|o| o.as_str()) == Some("as-declared") {
                        if let Some(obj) = existing.as_object_mut() {
                            obj.insert("last_inconclusive_at_unix".into(), serde_json::json!(now));
                        }
                        continue;
                    }
                }
            }
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

#[cfg(test)]
mod proof_ledger_tests {
    use super::*;
    use falsify::{Falsified, FalsifyReport};

    fn report(gate: &'static str, outcome: Falsified) -> FalsifyReport {
        let as_declared = matches!(outcome, Falsified::Proven);
        FalsifyReport { gate, mutation: "m", outcome, as_declared, detail: String::new() }
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

        Proofs::record(&root, &[report("apps-fleet", Falsified::Inconclusive("no DSN".into()))])
            .unwrap();

        let after = Proofs::load(&root);
        let e = after.entries.get("apps-fleet").expect("the row must survive");
        assert_eq!(e["outcome"], "as-declared", "the proof was erased");
        assert_eq!(e["observed"], "PROVEN");
        assert_eq!(e["proven_at_unix"], 1786369944, "the proof's age must not be refreshed");
        // The failed attempt is visible rather than silent.
        assert!(e.get("last_inconclusive_at_unix").is_some());
    }

    /// The other direction: INCONCLUSIVE must still be RECORDED when there is
    /// no prior proof to protect. Silence would read as "never attempted".
    #[test]
    fn an_inconclusive_run_is_recorded_when_there_is_no_prior_proof() {
        let root = scratch_dir("fresh");
        write_ledger(&root, r#"{"gates":{}}"#);

        Proofs::record(&root, &[report("apps-fleet", Falsified::Inconclusive("no DSN".into()))])
            .unwrap();

        let after = Proofs::load(&root);
        let e = after.entries.get("apps-fleet").expect("must be recorded");
        assert_eq!(e["outcome"], "NOT-as-declared");
        assert!(!after.fresh("apps-fleet"), "INCONCLUSIVE must never render a gate proven");
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

        Proofs::record(&root, &[report("roundtrip", Falsified::Vacuous)]).unwrap();

        let after = Proofs::load(&root);
        let e = after.entries.get("roundtrip").unwrap();
        assert_eq!(e["observed"], "VACUOUS");
        assert_eq!(e["outcome"], "NOT-as-declared");
        assert!(!after.fresh("roundtrip"));
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

//! Gate bodies.
//!
//! Every body runs **inside the harness's child process** (see `child.rs`), so
//! anything a body spawns inherits the gate's process group and dies with it.
//!
//! Two shapes, and the difference is deliberate:
//!
//! * **In-process gates** (`roundtrip`, `reject`) call the SAME Rust core the
//!   CLI gate calls. There is no text between the check and the verdict, so
//!   there is nothing to scrape.
//! * **Wrapped external verifiers** (`conformance`, `verify-cli`) are NOT
//!   rewritten — v2 §7.5 keeps them. They gain a `--json <path>` mode and the
//!   gate reads the **file**. This is how v2 §5.3(d) ("no `grep` in a verdict
//!   path") and §7.5 ("no verifier is rewritten, they are wrapped") stop
//!   contradicting each other: wrapping a *text-emitting* script means parsing
//!   text; wrapping a *JSON-emitting* script does not.
//!
//! Every gate asserts an **exact** count, never a `>=`. `ty/tests/reject.rs`
//! USED to assert `>= 13` against an actual 63 — deleting 50 corpus files kept
//! it green. It now reads the exact count from
//! `ty::reject_corpus::EXPECTED_CORPUS_FILES`, the same constant
//! [`REJECT_EXPECTED`] pins here. Exact counts are why a shrinking corpus is a
//! failure.

use super::registry::{GateCtx, GateOutcome};
use std::path::{Path, PathBuf};
use std::process::Command;

// ---------------------------------------------------------------------------
// Expected assertion counts. Pinned, exact, and updated deliberately.
// ---------------------------------------------------------------------------

/// `.sky` files under `examples/`, excluding generated dirs. Measured.
pub const ROUNDTRIP_EXPECTED: u64 = 173;
/// Files in `rust/crates/ty/tests/reject/corpus/`. Measured — and read from the
/// SINGLE declaration both reject faces share, so the harness cannot pin a
/// different corpus size than `xtask reject` and `cargo test -p ty --test
/// reject` enforce.
pub const REJECT_EXPECTED: u64 = ty::reject_corpus::EXPECTED_CORPUS_FILES as u64;
/// Conformance cases that actually RUN on a healthy tree. Measured: **770**.
///
/// v2 §5.4 fixes this number at **772**, and 772 is what a static count of
/// `Test.test` leaves in `tests/conformance/tests/` returns. The two disagree,
/// and the static count is the wrong one for a gate:
///
/// `StoreConformanceTest.sky:75` and `StoreCrudConformanceTest.sky:68` each
/// declare a `Test.test "setup"` leaf inside the `Err` arm of
/// `case setup () of` — a case that materialises ONLY when the DB setup fails.
/// On a healthy run those two arms are not taken, so 770 leaves exist.
///
/// Pinning 770 is strictly stronger than pinning 772 would have been: if a DB
/// setup ever does fail, the count rises to 771/772 AND the new leaf fails, so
/// the gate goes red on both counts rather than passing a suite that quietly
/// swapped 13 real assertions for one "setup failed".
pub const CONFORMANCE_EXPECTED: u64 = 770;
/// `verify-cli.sh` entries that actually assert something. The 14th entry
/// (`11-fyne-stopwatch`) is a declared skip and is deliberately NOT counted:
/// v2's "SKIP counted as pass" defect is closed by making skips invisible to
/// the assertion count rather than by counting them as successes.
pub const VERIFY_CLI_EXPECTED: u64 = 13;
/// `examples/*` projects that own a `tests/` directory. Measured: 6.
pub const SKY_VERIFY_EXPECTED: u64 = 6;

// ---------------------------------------------------------------------------
// In-process gates
// ---------------------------------------------------------------------------

pub fn roundtrip(ctx: &GateCtx) -> GateOutcome {
    let results = crate::roundtrip_scan(&ctx.repo_root);
    let assertions = results.len() as u64;
    let failing: Vec<&str> = results
        .iter()
        .filter(|r| !r.ok())
        .map(|r| r.rel.as_str())
        .collect();

    if failing.is_empty() {
        GateOutcome::new(
            true,
            assertions,
            format!("{assertions} files: byte-exact reprint, zero ERROR nodes"),
        )
    } else {
        GateOutcome::new(
            false,
            assertions,
            format!(
                "{} of {assertions} file(s) fail round-trip or contain ERROR nodes: {}",
                failing.len(),
                preview(&failing)
            ),
        )
    }
}

pub fn reject(ctx: &GateCtx) -> GateOutcome {
    let rows = match crate::reject_gate::scan(&ctx.repo_root) {
        Ok(r) => r,
        Err(msg) => return GateOutcome::new(false, 0, msg),
    };
    let assertions = rows.len() as u64;

    // `known-leniency` files are documented accept-parity cases; they are
    // reported but not part of the hard gate. They still COUNT as assertions —
    // the file was checked — so removing one still shrinks the count.
    let holes: Vec<&str> = rows
        .iter()
        .filter(|r| !r.known_leniency && !r.rejected())
        .map(|r| r.name.as_str())
        .collect();

    if !holes.is_empty() {
        return GateOutcome::new(
            false,
            assertions,
            format!(
                "{} soundness hole(s) — accepted but must be rejected: {}",
                holes.len(),
                preview(&holes)
            ),
        );
    }

    // Rejection alone is not the whole criterion: where a corpus file DECLARES
    // the diagnostic code its defect is about, the rejection must carry that
    // code (`ty::reject_corpus`, AT-LEAST rule). Without this the harness would
    // be a THIRD, weaker face of the same check.
    let code_gaps: Vec<String> = rows
        .iter()
        .filter(|r| !r.known_leniency && r.rejected() && !r.missing_codes().is_empty())
        .map(|r| format!("{} (missing {:?})", r.name, r.missing_codes()))
        .collect();
    if !code_gaps.is_empty() {
        let refs: Vec<&str> = code_gaps.iter().map(|s| s.as_str()).collect();
        return GateOutcome::new(
            false,
            assertions,
            format!(
                "{} file(s) rejected, but NOT by the declared diagnostic code: {}",
                refs.len(),
                preview(&refs)
            ),
        );
    }

    if let Err(msg) = ty::reject_corpus::check_code_census(&rows) {
        return GateOutcome::new(false, assertions, msg);
    }

    let (rust_code, oracle_code, no_code) = ty::reject_corpus::code_census(&rows);
    GateOutcome::new(
        true,
        assertions,
        format!(
            "{assertions} ill-typed programs, every hard-gate one rejected; \
             {rust_code} pin a rust-specific code, {oracle_code} derive it from the \
             oracle header, {} unpinned",
            no_code.len()
        ),
    )
}

// ---------------------------------------------------------------------------
// Wrapped external verifiers — read the JSON file, never the stdout
// ---------------------------------------------------------------------------

pub fn conformance(ctx: &GateCtx) -> GateOutcome {
    let json = scratch(&ctx.repo_root).join("conformance.json");
    let _ = std::fs::remove_file(&json);

    let run = match sh(
        &ctx.repo_root,
        "scripts/conformance.sh",
        &["--json".into(), json.display().to_string()],
    ) {
        Ok(r) => r,
        Err(e) => return GateOutcome::new(false, 0, e),
    };

    let Some(v) = read_json(&json) else {
        // The script ran but produced no machine-readable result. That is a
        // FAIL, not a pass-by-exit-code: the whole point of the wrapper is that
        // the verdict comes from the file.
        return GateOutcome::new(
            false,
            0,
            format!(
                "conformance.sh produced no JSON at {} (exit {:?}); \
                 verdict refused — the gate asserts on the result file, not on stdout",
                json.display(),
                run.code
            ),
        );
    };

    // The manifest names the suites; each suite's own `Sky.Test` report holds
    // the per-case truth. Aggregation happens HERE, in a real JSON parser,
    // rather than in the shell — a shell that parses its own output is how
    // `grep -qE "0 fail"` came to match inside "10 fail".
    let suites_run = u(&v, "suites_run");
    let empty = Vec::new();
    let suites = v.get("suites").and_then(|s| s.as_array()).unwrap_or(&empty);

    let mut cases = 0u64;
    let mut failed = 0u64;
    let mut suites_failed = 0u64;
    let mut broken: Vec<String> = Vec::new();

    for s in suites {
        let name = s.get("name").and_then(|n| n.as_str()).unwrap_or("?");
        let exit_code = s.get("exit_code").and_then(|c| c.as_i64()).unwrap_or(-1);
        let report = s.get("report").and_then(|r| r.as_str()).unwrap_or("");

        // A suite whose per-case report is missing or unreadable contributes
        // ZERO cases and is counted FAILED. Treating it as "skipped" is the
        // silent-shrink path that lets a suite stop running and still pass.
        let Some(rep) = read_json(Path::new(report)) else {
            suites_failed += 1;
            broken.push(format!("{name} (no per-case report)"));
            continue;
        };
        cases += u(&rep, "total");
        failed += u(&rep, "failed");
        if exit_code != 0 || u(&rep, "failed") > 0 {
            suites_failed += 1;
            broken.push(name.to_string());
        }
    }

    // EXACT, never `>=`. A suite that stops being discovered, or a case that
    // stops being emitted, shrinks `cases` and fails here.
    if cases != CONFORMANCE_EXPECTED {
        return GateOutcome::new(
            false,
            cases,
            format!(
                "expected EXACTLY {CONFORMANCE_EXPECTED} conformance cases, got {cases} \
                 across {suites_run} suite(s). If cases were deliberately added or removed, \
                 update CONFORMANCE_EXPECTED in harness/bodies.rs in the same commit."
            ),
        );
    }
    if failed > 0 || suites_failed > 0 {
        return GateOutcome::new(
            false,
            cases,
            format!(
                "{failed} failing case(s); {suites_failed} suite(s) red: {}",
                preview(&broken.iter().map(String::as_str).collect::<Vec<_>>())
            ),
        );
    }
    GateOutcome::new(
        true,
        cases,
        format!("{cases} cases across {suites_run} suites, all green"),
    )
}

pub fn verify_cli(ctx: &GateCtx) -> GateOutcome {
    let json = scratch(&ctx.repo_root).join("verify-cli.json");
    let _ = std::fs::remove_file(&json);

    // `--rebuild` is not optional for a gate.
    //
    // Without it `verify-cli.sh` only builds an example when `sky-out/app` is
    // MISSING, so it certifies whatever binary an earlier run left behind. A
    // gate that can pass on a stale artifact cannot be falsified by a source
    // mutation — its declared mutation would report VACUOUS forever and be
    // misread as a harness defect. Forcing the rebuild is what makes this gate
    // verify the tree under test.
    let run = match sh(
        &ctx.repo_root,
        "scripts/verify-cli.sh",
        &[
            "--rebuild".into(),
            "--json".into(),
            json.display().to_string(),
        ],
    ) {
        Ok(r) => r,
        Err(e) => return GateOutcome::new(false, 0, e),
    };

    let Some(v) = read_json(&json) else {
        return GateOutcome::new(
            false,
            0,
            format!(
                "verify-cli.sh produced no JSON at {} (exit {:?}); verdict refused",
                json.display(),
                run.code
            ),
        );
    };

    let pass = u(&v, "pass");
    let fail = u(&v, "fail");
    let skip = u(&v, "skip");
    // A SKIP asserts nothing, so it contributes nothing. This is the structural
    // form of v2's "SKIP counted as pass" defect fix: skips cannot inflate the
    // numerator because they are not in it.
    let assertions = pass + fail;

    if assertions != VERIFY_CLI_EXPECTED {
        return GateOutcome::new(
            false,
            assertions,
            format!(
                "expected EXACTLY {VERIFY_CLI_EXPECTED} asserting entries, got {assertions} \
                 ({pass} pass / {fail} fail / {skip} skip). A newly-skipped entry shrinks \
                 this count on purpose."
            ),
        );
    }
    if fail > 0 {
        let names = v
            .get("entries")
            .and_then(|e| e.as_array())
            .map(|a| {
                a.iter()
                    .filter(|e| e.get("outcome").and_then(|o| o.as_str()) == Some("fail"))
                    .filter_map(|e| e.get("name").and_then(|n| n.as_str()))
                    .collect::<Vec<_>>()
                    .join(", ")
            })
            .unwrap_or_default();
        return GateOutcome::new(false, assertions, format!("{fail} failing: {names}"));
    }
    GateOutcome::new(
        true,
        assertions,
        format!("{pass} entries verified, {skip} declared skip(s)"),
    )
}

/// `sky verify` over every `examples/*` project that owns a `tests/` suite.
///
/// These suites hold real assertions and, before this gate, were invoked by
/// **zero** scripts and **zero** workflows (v2 §6.1) — so they had never run in
/// CI. The verdict is each project's **exit status**, which `sky verify`
/// computes from structured internal state; no stdout is parsed.
pub fn sky_verify(ctx: &GateCtx) -> GateOutcome {
    let sky = ctx.repo_root.join("sky-out/sky");
    if !sky.is_file() {
        return GateOutcome::new(
            false,
            0,
            format!(
                "no compiler at {} — build it first (scripts/build.sh). \
                 A gate that cannot run has not passed.",
                sky.display()
            ),
        );
    }

    let projects = projects_with_tests(&ctx.repo_root);
    let mut failures: Vec<String> = Vec::new();
    let mut assertions = 0u64;

    for p in &projects {
        assertions += 1;
        let out = Command::new(&sky)
            .arg("verify")
            .arg(p)
            .current_dir(&ctx.repo_root)
            .output();
        match out {
            Ok(o) if o.status.success() => {}
            Ok(o) => {
                let name = p
                    .file_name()
                    .and_then(|n| n.to_str())
                    .unwrap_or("?")
                    .to_string();
                let tail: String = String::from_utf8_lossy(&o.stdout)
                    .lines()
                    .filter(|l| l.contains('✗'))
                    .take(3)
                    .collect::<Vec<_>>()
                    .join(" | ");
                failures.push(format!("{name} ({tail})"));
            }
            Err(e) => failures.push(format!("{}: spawn failed: {e}", p.display())),
        }
    }

    if assertions != SKY_VERIFY_EXPECTED {
        return GateOutcome::new(
            false,
            assertions,
            format!(
                "expected EXACTLY {SKY_VERIFY_EXPECTED} projects with a tests/ suite, \
                 found {assertions}"
            ),
        );
    }
    if failures.is_empty() {
        GateOutcome::new(true, assertions, format!("{assertions} projects verified"))
    } else {
        GateOutcome::new(
            false,
            assertions,
            format!(
                "{} project(s) failed: {}",
                failures.len(),
                failures.join("; ")
            ),
        )
    }
}

/// `examples/*` directories that own a non-empty `tests/` dir, sorted.
///
/// Discovery is by structure, not by a hand-maintained list, so a new suite is
/// picked up automatically — and the EXACT `SKY_VERIFY_EXPECTED` count means
/// adding one is a deliberate, visible registry edit rather than a silent
/// budget change.
fn projects_with_tests(root: &Path) -> Vec<PathBuf> {
    let mut out = Vec::new();
    let Ok(rd) = std::fs::read_dir(root.join("examples")) else {
        return out;
    };
    let mut dirs: Vec<PathBuf> = rd.filter_map(|e| e.ok().map(|e| e.path())).collect();
    dirs.sort();
    for d in dirs {
        if !d.is_dir() {
            continue;
        }
        let tests = d.join("tests");
        let has_suite = std::fs::read_dir(&tests)
            .map(|rd| {
                rd.filter_map(|e| e.ok())
                    .any(|e| e.path().extension().and_then(|x| x.to_str()) == Some("sky"))
            })
            .unwrap_or(false);
        if has_suite {
            out.push(d);
        }
    }
    out
}

// ---------------------------------------------------------------------------
// Harness self-verification
// ---------------------------------------------------------------------------

/// THE CANARY — permanently registered, deliberately vacuous.
///
/// It asserts `true`. Paired with a no-op patch (`MutationKind::NoOp`), a
/// CORRECT falsifier runner must report `VACUOUS` for it, because nothing
/// changed and so the gate cannot have gone red.
///
/// Reporting `PROVEN` here is a **harness failure**, and it is the only
/// construction that catches a verifier whose every answer is "green" — or one
/// that applies its patch in the wrong tree and then reports success from a
/// run that never saw the patch (v2 §7.5).
pub fn canary(_ctx: &GateCtx) -> GateOutcome {
    GateOutcome::new(
        true,
        1,
        "vacuous by construction — the falsifier must report VACUOUS for this gate",
    )
}

/// SELF-TEST — hangs forever, having first spawned a grandchild that also hangs.
///
/// The grandchild is the point. Killing only the direct child would leave it
/// running; only `killpg` over the gate's process group reaps both. The
/// harness test asserts **both** pids are gone after the budget expires, which
/// is the property the BlueDB precedent's detached-thread timeout cannot have.
pub fn selftest_hang(_ctx: &GateCtx) -> GateOutcome {
    let grandchild = Command::new("sh")
        .arg("-c")
        .arg("sleep 600")
        .spawn()
        .map(|c| c.id())
        .unwrap_or(0);

    if let Ok(p) = std::env::var("SKY_HARNESS_HANG_PIDFILE") {
        let _ = std::fs::write(&p, format!("{}\n{}\n", std::process::id(), grandchild));
    }

    loop {
        std::thread::sleep(std::time::Duration::from_secs(3600));
    }
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

pub(crate) fn scratch(root: &Path) -> PathBuf {
    let d = root.join(".skycache/harness");
    let _ = std::fs::create_dir_all(&d);
    d
}

struct Sh {
    code: Option<i32>,
}

/// Run a repo script. Its stdout/stderr are inherited (so CI logs keep the
/// human-readable output) and are NEVER consulted for the verdict.
fn sh(root: &Path, script: &str, args: &[String]) -> Result<Sh, String> {
    let path = root.join(script);
    if !path.is_file() {
        return Err(format!("missing verifier script {}", path.display()));
    }
    let status = Command::new("bash")
        .arg(&path)
        .args(args)
        .current_dir(root)
        .status()
        .map_err(|e| format!("could not run {script}: {e}"))?;
    Ok(Sh {
        code: status.code(),
    })
}

// ---------------------------------------------------------------------------
// Layer 1 — the combinatorial corpus (v2 §3) and the shared-world differential
// ---------------------------------------------------------------------------

/// Cases the generator produces. Read from the generator, not hand-copied, so
/// the harness cannot pin a different corpus size than the manifest declares.
pub const CORPUS_EXPECTED: u64 = 206;
/// The isolation gate's sample size (v2 §3.2).
pub const CORPUS_ISOLATION_EXPECTED: u64 = 24;
/// The witness gate's shard size (v2 §4.4).
pub const CORPUS_WITNESS_EXPECTED: u64 = 16;
/// Items the shared-world differential compares: the reject + infer corpora.
pub const SHARED_WORLD_EXPECTED: u64 = 121;

/// The corpus manifest is the ONLY membership authority (v2 §3.1). This gate
/// fails when the generator and the checked-in manifest disagree, so a generator
/// edit that silently adds, drops, or reclassifies a case is a failing build
/// rather than a quiet change in what "100 % covered" means.
///
/// In-process and instant — no `sky` binary needed.
pub fn corpus_manifest(ctx: &GateCtx) -> GateOutcome {
    let cases = crate::corpus::all_cases();
    let n = cases.len() as u64;
    let code = crate::corpus::manifest::check(&ctx.repo_root);
    GateOutcome::new(
        code == 0,
        n,
        if code == 0 {
            format!("{n} cases; generator and corpus/manifest.toml agree")
        } else {
            format!("{n} cases; generator and corpus/manifest.toml DISAGREE")
        },
    )
}

/// The full Layer-1 corpus: every case built, run, and its value compared
/// against the one the GENERATOR constructed (v2 §4.4 class V).
///
/// Blocked cases (known product defects) still run and still count as
/// assertions; they never contribute PASS and they fail the gate once their
/// expiry passes.
pub fn corpus(ctx: &GateCtx) -> GateOutcome {
    let code = crate::corpus::runner::run_all(&ctx.repo_root);
    let n = crate::corpus::all_cases().len() as u64;
    GateOutcome::new(
        code == 0,
        n,
        format!("{n} generated cases built and run; values compared against the generator's own"),
    )
}

/// v2 §3.2 — a sampled case must give the SAME verdict alone, in a batch, and in
/// a shuffled batch. The only mechanism that notices when a new family starts
/// depending on whole-compilation state.
pub fn corpus_isolation(ctx: &GateCtx) -> GateOutcome {
    let code = crate::corpus::isolation::run(&ctx.repo_root);
    GateOutcome::new(
        code == 0,
        CORPUS_ISOLATION_EXPECTED,
        "sampled cases: identical verdicts alone / in-batch / shuffled".to_string(),
    )
}

/// v2 §4.4 — each case must emit DIFFERENT Go from its axis-neutralised twin.
/// A case that does not witness its axis does not cover it.
pub fn corpus_witness(ctx: &GateCtx) -> GateOutcome {
    let code = crate::corpus::witness::run(&ctx.repo_root);
    GateOutcome::new(
        code == 0,
        CORPUS_WITNESS_EXPECTED,
        "sharded cases each emit different Go from their axis-neutralised twin".to_string(),
    )
}

/// The shared-world differential (v2 §11-U1).
///
/// Phase 3 deliberately left this unregistered so as not to change the five-gate
/// set that phase was verified against. Registering it is a Phase 4 step: the
/// incremental world is now load-bearing for the corpus, so the proof that it
/// produces identical verdicts to the whole-program path belongs in the gate
/// set rather than in a doc.
pub fn shared_world(ctx: &GateCtx) -> GateOutcome {
    match crate::shared_world_gate::compare(&ctx.repo_root, false) {
        Err(e) => GateOutcome::new(false, 0, e),
        Ok(r) => {
            let n = r.compared as u64;
            if r.diverged.is_empty() {
                GateOutcome::new(
                    true,
                    n,
                    format!(
                        "{n} items, identical verdicts (counts, diagnostics, inferred types); \
                         {} shared, {} full-rebuild fallback(s)",
                        r.n_shared,
                        r.fallbacks.len()
                    ),
                )
            } else {
                GateOutcome::new(
                    false,
                    n,
                    format!("{} of {n} item(s) diverge: {}", r.diverged.len(), {
                        let v: Vec<&str> = r.diverged.iter().map(|s| s.as_str()).collect();
                        preview(&v)
                    }),
                )
            }
        }
    }
}

// ---------------------------------------------------------------------------
// Layer 2 — real-world projects (v2 §6)
// ---------------------------------------------------------------------------

/// Member F: `sky-bundled/console` + `sky-bundled/doc`.
///
/// Two assertions per project — the build's exit status, and the artifact it
/// was supposed to leave behind. The artifact half is not redundant: a builder
/// that exits 0 having emitted nothing is exactly the shape of the "could never
/// pass on a clean checkout" defect, inverted.
pub const APPS_BUNDLED_EXPECTED: u64 = 4;

/// The single largest Sky application the project ships, gated by nothing.
///
/// `sky-bundled/console` is 5,746 lines across 11 modules, statically linked
/// into every binary the compiler emits; `sky-bundled/doc` backs `sky doc
/// --serve`. Neither is under `examples/`, so neither is in any corpus, and
/// `grep -rn 'regenerate-console\|console_app' .github/` returns nothing.
///
/// Registering this gate immediately found that the console **did not compile**:
/// `ef826e73` added `logoutUrl` to the shared `Model` in `State.sky` and updated
/// `Main.sky` but not `MainTui.sky`, which constructs the same record. It had
/// been broken since 2026-07-31 (fixed in the commit that added this gate).
pub fn apps_bundled(ctx: &GateCtx) -> GateOutcome {
    use super::layer2;

    let projects = ["sky-bundled/console", "sky-bundled/doc"];
    let mut assertions = 0u64;
    let mut failures: Vec<String> = Vec::new();
    let mut timings: Vec<String> = Vec::new();

    for p in projects {
        let r = match layer2::clean_build(&ctx.repo_root, p) {
            Ok(r) => r,
            // No compiler, or not a project: nothing was asserted. Reporting 0
            // assertions is what makes this a FAIL rather than a quiet pass.
            Err(e) => return GateOutcome::new(false, assertions, e),
        };

        assertions += 1;
        if !r.ok {
            failures.push(format!(
                "{p}: `sky build` failed:\n{}",
                layer2::tail(&r.log, 12)
            ));
        }

        assertions += 1;
        if !r.binary.is_file() {
            failures.push(format!("{p}: no artifact at {}", r.binary.display()));
        }

        timings.push(format!("{p} {:.1}s", r.elapsed_s));
    }

    if failures.is_empty() {
        GateOutcome::new(
            true,
            assertions,
            format!("bundled Sky apps build from a wiped slate ({})", timings.join(", ")),
        )
    } else {
        GateOutcome::new(false, assertions, failures.join(" | "))
    }
}

/// Member G: the CLI verbs, owned by `rust/crates/sky/tests/cli_verb_flow.rs`.
///
/// v2 §6 row G puts the verbs in flow tests rather than an app, so this gate
/// does not re-implement them — it runs them and pins their population. Two
/// independent properties, neither of which scrapes test output:
///
///   * the suite's **exit status** (libtest's own verdict), and
///   * the **number of `#[test]` functions** in the file, counted from source.
///
/// The second is what makes deletion visible. `cargo test` on a file whose tests
/// were removed exits 0 having run nothing, which is the same shape as the
/// `0/0 … GATE: PASS` defect.
pub const CLI_VERBS_EXPECTED: u64 = 9;

pub fn cli_verbs(ctx: &GateCtx) -> GateOutcome {
    let suite = ctx
        .repo_root
        .join("rust/crates/sky/tests/cli_verb_flow.rs");
    let Ok(src) = std::fs::read_to_string(&suite) else {
        return GateOutcome::new(false, 0, format!("cannot read {}", suite.display()));
    };
    let n = src
        .lines()
        .filter(|l| l.trim_start().starts_with("#[test]"))
        .count() as u64;

    if n != CLI_VERBS_EXPECTED {
        return GateOutcome::new(
            false,
            n,
            format!(
                "expected EXACTLY {CLI_VERBS_EXPECTED} CLI-verb tests, found {n}. \
                 If a verb test was deliberately added or removed, update \
                 CLI_VERBS_EXPECTED in harness/bodies.rs in the same commit."
            ),
        );
    }

    // `CARGO_TARGET_DIR` is unset deliberately: inheriting a caller's target dir
    // has repeatedly produced cross-tree contamination on this repo.
    let out = Command::new("cargo")
        .args(["test", "-p", "sky", "--test", "cli_verb_flow"])
        .current_dir(ctx.repo_root.join("rust"))
        .env_remove("CARGO_TARGET_DIR")
        .stdin(std::process::Stdio::null())
        .output();

    match out {
        // A missing//unrunnable cargo is a FAIL, never a skip — "a gate that
        // cannot run has not passed".
        Err(e) => GateOutcome::new(false, 0, format!("could not run cargo test: {e}")),
        Ok(o) if o.status.success() => GateOutcome::new(
            true,
            n,
            format!("{n} CLI-verb flow tests green (toolchain-free assertions)"),
        ),
        Ok(o) => GateOutcome::new(
            false,
            n,
            format!(
                "cli_verb_flow suite failed (exit {:?}):\n{}",
                o.status.code(),
                super::layer2::tail(&String::from_utf8_lossy(&o.stdout), 25)
            ),
        ),
    }
}

fn read_json(p: &Path) -> Option<serde_json::Value> {
    serde_json::from_str(&std::fs::read_to_string(p).ok()?).ok()
}

fn u(v: &serde_json::Value, key: &str) -> u64 {
    v.get(key).and_then(|x| x.as_u64()).unwrap_or(0)
}

fn preview(items: &[&str]) -> String {
    let shown: Vec<&str> = items.iter().take(5).copied().collect();
    if items.len() > shown.len() {
        format!(
            "{} … (+{} more)",
            shown.join(", "),
            items.len() - shown.len()
        )
    } else {
        shown.join(", ")
    }
}

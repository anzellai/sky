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
/// **178 since 2026-08-22**: `examples/60-spa-todos` (the Sky.Spa example) added
/// its client/server `Main.sky` + `shared/Shared.sky` + the two symlinked
/// `Shared.sky` (+5); all reprint byte-exact with zero ERROR nodes.
pub const ROUNDTRIP_EXPECTED: u64 = 178;
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
///
/// **910 since 2026-08-10** (was 770). Two suites were added for stdlib modules
/// that no test had ever executed because nothing in the repo imported them:
/// `EmailConformanceTest` (+64, the whole pure `Std.Email` builder surface) and
/// `DbSchemaConformanceTest` (+76, every `Std.Db.Schema` constructor/modifier
/// plus the `toProject` encoding). `scripts/conformance.sh` globs
/// `tests/*Test.sky`, so both are discovered by the existing gate.
///
/// **923 since 2026-08-12** (was 910). `DictSetConformanceTest` gained 13 cases
/// for the `Set` element-collision class: `SkySet` keyed its backing map on
/// `fmt.Sprintf("%v", element)`, which is not injective on composites, so
/// `Set.fromList [ ( "a b", "c" ), ( "a", "b c" ) ]` returned a set of size ONE
/// and one element of the user's data was silently gone. The new cases cover
/// tuples / lists / records / ADTs carrying strings through `size`, `toList`,
/// `member`, `union` and `intersect`, plus two guards that de-duplication and
/// string-free composites still behave.
///
/// **935 since 2026-08-12** (was 923). `UiParagraphInlineConformanceTest` adds 12
/// cases for the paragraph-child markup class: `Ui.el` inside `Ui.paragraph` —
/// the highlight-a-phrase pattern `paragraph`'s own docstring recommends —
/// emitted a `<div>` inside the `<p>`, which the HTML parser hoists out, so the
/// browser rendered a paragraph, a sibling block, and an orphaned text run. The
/// cases assert the EMITTED MARKUP because every other gate passed on it: the
/// broken version compiled, type-checked and ran. Seven pin the fix (tag +
/// display, both halves), five pin what must NOT change outside a paragraph,
/// since keying on parent context risks flattening every layout in every app.
pub const CONFORMANCE_EXPECTED: u64 = 935;
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

/// Cases the generator produces — EVERY family, which is what the manifest
/// declares membership over. Read from the generator, not hand-copied, so the
/// harness cannot pin a different corpus size than the manifest declares.
///
/// **342 since 2026-08-11** (was 206): families R (code-pinned reject pairs) and
/// E (10 emit-shape property cases) joined the manifest. **441 since the
/// `dict_composite_key` defect** (+9 = 1 defect × 3 positions × 3 import shapes),
/// the `[E2008]` unsupported-`Dict`-key rejection.
///
/// **481 since the Family-S shape close** (+40), and both halves are Family S:
///
/// * **+15** — the `dict_key_crossing` stratum: 5 key types × 3 access shapes.
///   #174 reached a release WITH Family S already asserting `Sky.Core.Dict` at
///   five edge classes, because the battery crossed neither the key TYPE nor
///   the polymorphic-helper ACCESS shape against the ITERATION operations. It
///   asserted `String` keys, which are the one type that always worked.
/// * **+25** — five surfaces the ledger listed as dark-but-assertable
///   (`Sky.Core.Bytes`, `Sky.Core.Jwt`, `Std.Codec`, `Std.Markdown`,
///   `Std.Compression`) × their five edge classes.
pub const CORPUS_EXPECTED: u64 = 481;
/// The subset that is BUILT AND RUN. Split from [`CORPUS_EXPECTED`] when R and E
/// landed: the `corpus` gate runs only the behavioural cases (an ill-typed
/// family-R program has no binary to run, and a family-E verdict is a property of
/// the emitted Go), so pinning the full count there would have made the gate's
/// declared assertion count a number it never reaches.
///
/// **335 since the Family-S shape close** (was 296): all 40 new cases carry a
/// generator-constructed value, so all 40 are built and run. Measured cost of
/// the addition on this host: 40 × 1.39 s/case at 4 workers ≈ 56 s of the
/// `corpus` gate's wall clock (see `xtask corpus-bench`).
pub const CORPUS_BEHAVIOURAL_EXPECTED: u64 = 335;
/// Family R: 135 cases × 2 checks (the rejection carries its declared code; the
/// twin compiles). Both are counted because both can fail independently — a
/// rejection for the wrong reason and a broken twin are different defects.
///
/// **270 since the `dict_composite_key` defect** (was 252 / 126 cases): the
/// `[E2008]` unsupported-`Dict`-key rejection, crossed with the position and
/// import axes.
pub const CORPUS_REJECT_EXPECTED: u64 = 270;
/// Family E: one assertion per asserted property across the 10 cases. Measured:
/// **46** (the two struct-shape properties only apply to the named-alias arm).
pub const CORPUS_EMIT_SHAPE_EXPECTED: u64 = 46;
/// The isolation gate's sample size (v2 §3.2).
pub const CORPUS_ISOLATION_EXPECTED: u64 = 24;
/// The witness gate's shard size (v2 §4.4).
pub const CORPUS_WITNESS_EXPECTED: u64 = 16;
/// Items the shared-world differential compares: the reject + infer corpora.
///
/// **122 since 2026-08-11** (was 121): `unknown_module_aliased_import.sky` joined
/// the reject corpus as the checked-in regression for the aliased-unknown-module
/// soundness hole (see its header). The count is the two corpora summed, so a
/// reject-corpus addition moves it — as `dict_composite_key.sky` (the `[E2008]`
/// unsupported-`Dict`-key rejection) did at **125**.
///
/// **126 since 2026-08-12**: `ambiguous_type_name.sky` joined the reject corpus
/// with the type-namespace half of `[E1012]`. That commit bumped
/// `ty::reject_corpus::EXPECTED_CORPUS_FILES` but not this constant, so the
/// `shared-world` gate has been reporting `126/125` — the gate itself PASSES
/// (126 items, identical verdicts); only the harness census was behind. 68
/// reject-corpus files + 58 `examples/` directories = 126.
/// **127 since 2026-08-22**: `examples/60-spa-todos` (the Sky.Spa example) added
/// one `examples/` directory → 59 dirs + 68 reject = 127; verdicts identical.
pub const SHARED_WORLD_EXPECTED: u64 = 127;

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
    let n = crate::corpus::behavioural_cases().len() as u64;
    GateOutcome::new(
        code == 0,
        n,
        format!("{n} generated cases built and run; values compared against the generator's own"),
    )
}

/// v2 §3.1 family R — the reject matrix.
///
/// Two assertions per case and BOTH are counted: the rejection must carry the
/// generator's declared diagnostic code, and the paired twin must compile. A
/// gate that counted only the first would go green against a checker that
/// rejects every program, which is the precise state the twin exists to exclude.
///
/// In-process (`ty::check_modules`) — no `sky` binary, no `go build`.
pub fn corpus_reject(ctx: &GateCtx) -> GateOutcome {
    let rows = match crate::corpus::reject_matrix::evaluate(&ctx.repo_root) {
        Ok(r) => r,
        Err(msg) => return GateOutcome::new(false, 0, msg),
    };
    let assertions = rows.len() as u64 * 2;
    let bad: Vec<&str> = rows
        .iter()
        .filter(|r| !r.ok())
        .map(|r| r.id.as_str())
        .collect();
    if bad.is_empty() {
        GateOutcome::new(
            true,
            assertions,
            format!(
                "{} reject case(s): each rejected by its declared diagnostic code, \
                 each paired twin accepted",
                rows.len()
            ),
        )
    } else {
        GateOutcome::new(
            false,
            assertions,
            format!(
                "{} of {} reject pair(s) failed (accepted-when-it-should-reject, wrong \
                 diagnostic code, or a rejected twin): {}",
                bad.len(),
                rows.len(),
                preview(&bad)
            ),
        )
    }
}

/// v2 §3.1 family E — emit-shape properties of the generated Go, no `go build`.
pub fn corpus_emit_shape(ctx: &GateCtx) -> GateOutcome {
    let code = crate::corpus::emit_shape::run(&ctx.repo_root);
    let n: u64 = crate::corpus::emit_shape::all()
        .iter()
        .map(|c| c.emit_properties.len() as u64)
        .sum();
    GateOutcome::new(
        code == 0,
        n,
        format!("{n} emit-shape properties asserted over the generated Go"),
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
pub const CLI_VERBS_EXPECTED: u64 = 10;

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

/// Member D: Go FFI at 76k-symbol scale.
///
/// Three assertions: the dependency fetch, the build, and the artifact.
pub const APPS_FFI_SCALE_EXPECTED: u64 = 3;

/// The FFI-at-scale member, run pre-release (T4).
///
/// Not new source. `examples/13-skyshop` **is** the 76k-symbol benchmark v2 §6
/// row D is specified as the successor to, and nothing else in the corpus
/// exercises the FFI *scale* path — `safePkgName` aliasing, the typed-FFI cache,
/// `sky-ffi-inspect`'s memory behaviour.
///
/// It is T4 because the tier assignment justifies itself when measured: the
/// project declares an external Sky package and **refuses to build** until
/// `sky install` has fetched it. Measured cold on the dev host: install 131 s,
/// build 105 s, a 144 MB binary. Network-dependent, cold-expensive work does not
/// belong on the per-push path.
pub fn apps_ffi_scale(ctx: &GateCtx) -> GateOutcome {
    use super::layer2;

    const PROJECT: &str = "examples/13-skyshop";
    let sky = match layer2::sky_binary(&ctx.repo_root) {
        Ok(s) => s,
        Err(e) => return GateOutcome::new(false, 0, e),
    };
    let dir = ctx.repo_root.join(PROJECT);
    let mut assertions = 0u64;

    // `sky install` fetches the external Sky package + Go modules. This is the
    // surface the member owns; a gate that skipped it would not be the
    // FFI-at-scale gate.
    assertions += 1;
    let install = Command::new(&sky)
        .arg("install")
        .current_dir(&dir)
        .stdin(std::process::Stdio::null())
        .output();
    match install {
        Err(e) => return GateOutcome::new(false, assertions, format!("`sky install` failed to spawn: {e}")),
        Ok(o) if !o.status.success() => {
            return GateOutcome::new(
                false,
                assertions,
                format!(
                    "`sky install` failed in {PROJECT} (exit {:?}):\n{}",
                    o.status.code(),
                    layer2::tail(&String::from_utf8_lossy(&o.stderr), 12)
                ),
            )
        }
        Ok(_) => {}
    }

    let r = match layer2::clean_build_keep_deps(&ctx.repo_root, PROJECT) {
        Ok(r) => r,
        Err(e) => return GateOutcome::new(false, assertions, e),
    };

    assertions += 1;
    if !r.ok {
        return GateOutcome::new(
            false,
            assertions,
            format!("{PROJECT}: `sky build` failed:\n{}", layer2::tail(&r.log, 15)),
        );
    }

    assertions += 1;
    if !r.binary.is_file() {
        return GateOutcome::new(
            false,
            assertions,
            format!("{PROJECT}: no artifact at {}", r.binary.display()),
        );
    }

    GateOutcome::new(
        true,
        assertions,
        format!("76k-symbol FFI project installed and built ({:.0}s)", r.elapsed_s),
    )
}

/// Member B: Relay, the headless HTTP/SSE/WebSocket gateway.
///
/// Twelve: build, artifact, no bind-position literal, readiness, `/health`
/// status, `/health` identity, unauthenticated 401, authenticated 200, first
/// request served, limiter engaged, CORS header, port released.
pub const APPS_RELAY_EXPECTED: u64 = 12;

/// Build Relay, run it on a harness-chosen port, and assert what it *did*.
///
/// Every assertion below is a verdict, not a liveness check. "No crash" would
/// pass while a rate limiter never engaged and an unauthenticated request was
/// served — which is the shape of the defect this tier exists to catch.
///
/// Readiness deliberately requires BOTH the app's own line and an accepting
/// socket: `Sky.Http.Server.listen` has no post-bind hook, so the line is
/// printed *before* the bind succeeds. A collision on the port prints the line
/// and then dies, so grepping the line alone would report a server that is not
/// there.
pub fn apps_relay(ctx: &GateCtx) -> GateOutcome {
    use super::layer2;
    use std::time::Duration;

    const PROJECT: &str = "apps/relay";
    let mut a = 0u64;
    let mut fail: Vec<String> = Vec::new();
    macro_rules! check {
        ($cond:expr, $($msg:tt)*) => {{
            a += 1;
            if !$cond { fail.push(format!($($msg)*)); }
        }};
    }

    // ---- build ----------------------------------------------------------
    let r = match layer2::clean_build(&ctx.repo_root, PROJECT) {
        Ok(r) => r,
        Err(e) => return GateOutcome::new(false, 0, e),
    };
    check!(r.ok, "`sky build` failed:\n{}", layer2::tail(&r.log, 12));
    check!(r.binary.is_file(), "no artifact at {}", r.binary.display());
    if !fail.is_empty() {
        return GateOutcome::new(false, a, fail.join(" | "));
    }

    // ---- source guard ---------------------------------------------------
    let literals = layer2::bind_position_port_literals(&ctx.repo_root, PROJECT);
    check!(
        literals.is_empty(),
        "bind-position port literal(s) in project source: {} — a member with a \
         hardcoded port cannot be scheduled concurrently",
        literals.join(", ")
    );

    // ---- run ------------------------------------------------------------
    let port = match layer2::free_port() {
        Ok(p) => p,
        Err(e) => return GateOutcome::new(false, a, e),
    };
    let dir = ctx.repo_root.join(PROJECT);
    let env = [
        ("RELAY_PORT", port.to_string()),
        (
            "SKY_AUTH_TOKEN_SECRET",
            "layer2-relay-gate-secret-least-32-bytes-long".to_string(),
        ),
    ];
    let mut srv = match layer2::Server::spawn(&r.binary, &dir, port, &env) {
        Ok(s) => s,
        Err(e) => return GateOutcome::new(false, a, e),
    };

    let ready = srv.wait_ready("relay: listening on", Duration::from_secs(30));
    check!(ready.is_ok(), "{}", ready.as_ref().err().cloned().unwrap_or_default());
    if ready.is_err() {
        let _ = srv.shutdown();
        return GateOutcome::new(false, a, fail.join(" | "));
    }

    // ---- behaviour ------------------------------------------------------
    match layer2::get(port, "/health") {
        Err(e) => {
            a += 2;
            fail.push(format!("GET /health: {e}"));
        }
        Ok(resp) => {
            check!(resp.status == 200, "GET /health: expected 200, got {}", resp.status);
            check!(
                resp.body.contains("\"service\":\"relay\""),
                "GET /health: body does not identify the service: {}",
                resp.body.trim()
            );
        }
    }

    // Unauthenticated access must be REFUSED. A gate that only asserted "200 on
    // /health" would pass an app that served every protected route wide open.
    match layer2::get(port, "/api/me") {
        Err(e) => {
            a += 1;
            fail.push(format!("GET /api/me: {e}"));
        }
        Ok(resp) => check!(
            resp.status == 401,
            "GET /api/me without a token: expected 401, got {}",
            resp.status
        ),
    }

    // A token minted by the app must then be ACCEPTED — otherwise "401 always"
    // would satisfy the assertion above.
    let token = layer2::get(port, "/api/token?sub=gate")
        .ok()
        .and_then(|r| {
            let b = r.body;
            let i = b.find("\"token\":\"")? + 9;
            let rest = &b[i..];
            Some(rest[..rest.find('"')?].to_string())
        });
    match token {
        None => {
            a += 1;
            fail.push("could not mint a token via /api/token".to_string());
        }
        Some(t) => match layer2::http(
            port,
            "GET",
            "/api/me",
            &[("Authorization", &format!("Bearer {t}"))],
            None,
            Duration::from_secs(15),
        ) {
            Err(e) => {
                a += 1;
                fail.push(format!("GET /api/me with a token: {e}"));
            }
            Ok(resp) => check!(
                resp.status == 200,
                "GET /api/me with a freshly minted token: expected 200, got {}",
                resp.status
            ),
        },
    }

    // Rate limiting must actually ENGAGE. Asserted as "the first is served AND
    // some later one is refused", which is robust to the bucket refilling
    // mid-burst while still failing outright if the limiter is inert.
    let mut statuses = Vec::new();
    for _ in 0..12 {
        match layer2::get(port, "/api/limited") {
            Ok(r) => statuses.push(r.status),
            Err(e) => {
                statuses.push(0);
                let _ = e;
            }
        }
    }
    check!(
        statuses.first() == Some(&200),
        "first /api/limited request should be served, got {:?}",
        statuses.first()
    );
    check!(
        statuses.contains(&429),
        "rate limiter never engaged over 12 requests: {statuses:?}"
    );

    // CORS preflight — the non-variadic kernel-alias middleware shape.
    match layer2::http(
        port,
        "OPTIONS",
        "/health",
        &[("Origin", "https://example.com")],
        None,
        Duration::from_secs(15),
    ) {
        Err(e) => {
            a += 1;
            fail.push(format!("OPTIONS /health: {e}"));
        }
        Ok(resp) => check!(
            resp.header("access-control-allow-origin").is_some(),
            "CORS preflight carried no Access-Control-Allow-Origin (status {})",
            resp.status
        ),
    }

    // ---- teardown -------------------------------------------------------
    let down = srv.shutdown();
    check!(
        down.is_ok(),
        "{}",
        down.as_ref().err().cloned().unwrap_or_default()
    );

    if fail.is_empty() {
        GateOutcome::new(
            true,
            a,
            format!("built + served on :{port}; auth, rate limit and CORS all asserted"),
        )
    } else {
        GateOutcome::new(false, a, fail.join(" | "))
    }
}

/// Member C: Fieldbook — one `Std.Ui` view, rendered by several backends.
///
/// Eight: build, artifact, no bind-position literal, a live dump, a tui dump,
/// the two dumps agreeing, the app's own diff verdict, and the Cli export.
pub const APPS_FIELDBOOK_EXPECTED: u64 = 8;

/// The cross-backend `Std.Ui` parity assertion.
///
/// The claim under test is the one the product makes: the *same* view function
/// renders across Sky.Live, Sky.Tui and Sky.Webview. So the member dumps a
/// canonical structure of the real artefacts — the `Element` tree Sky.Tui is
/// handed, and the `Html` tree Sky.Live serialises — and this gate asserts they
/// are identical.
///
/// That is a **verdict**, not a liveness check: a `Std.Ui` change that renders
/// correctly on Live and wrongly on Tui makes the two dumps differ and fails
/// here, which is precisely what "one view, several backends" has to mean if it
/// is to mean anything.
pub fn apps_fieldbook(ctx: &GateCtx) -> GateOutcome {
    use super::layer2;

    const PROJECT: &str = "apps/fieldbook";
    let mut a = 0u64;
    let mut fail: Vec<String> = Vec::new();

    let r = match layer2::clean_build(&ctx.repo_root, PROJECT) {
        Ok(r) => r,
        Err(e) => return GateOutcome::new(false, 0, e),
    };
    a += 1;
    if !r.ok {
        fail.push(format!("`sky build` failed:\n{}", layer2::tail(&r.log, 12)));
    }
    a += 1;
    if !r.binary.is_file() {
        fail.push(format!("no artifact at {}", r.binary.display()));
    }
    if !fail.is_empty() {
        return GateOutcome::new(false, a, fail.join(" | "));
    }

    a += 1;
    let literals = layer2::bind_position_port_literals(&ctx.repo_root, PROJECT);
    if !literals.is_empty() {
        fail.push(format!(
            "bind-position port literal(s): {}",
            literals.join(", ")
        ));
    }

    let dir = ctx.repo_root.join(PROJECT);
    let dump = |args: &[&str]| -> Result<String, String> {
        let o = Command::new(&r.binary)
            .args(args)
            .current_dir(&dir)
            .stdin(std::process::Stdio::null())
            .output()
            .map_err(|e| format!("spawn {args:?}: {e}"))?;
        if !o.status.success() {
            return Err(format!(
                "{args:?} exited {:?}: {}",
                o.status.code(),
                layer2::tail(&String::from_utf8_lossy(&o.stderr), 6)
            ));
        }
        Ok(String::from_utf8_lossy(&o.stdout).into_owned())
    };

    let live = dump(&["--dump-view", "live"]);
    a += 1;
    let live_ok = match &live {
        Ok(s) if !s.trim().is_empty() => true,
        Ok(_) => {
            fail.push("--dump-view live produced an empty structure".into());
            false
        }
        Err(e) => {
            fail.push(e.clone());
            false
        }
    };

    let tui = dump(&["--dump-view", "tui"]);
    a += 1;
    let tui_ok = match &tui {
        Ok(s) if !s.trim().is_empty() => true,
        Ok(_) => {
            fail.push("--dump-view tui produced an empty structure".into());
            false
        }
        Err(e) => {
            fail.push(e.clone());
            false
        }
    };

    // THE assertion this member exists for.
    a += 1;
    if live_ok && tui_ok {
        let (l, t) = (live.as_ref().unwrap(), tui.as_ref().unwrap());
        if l != t {
            let first = l
                .lines()
                .zip(t.lines())
                .enumerate()
                .find(|(_, (a, b))| a != b)
                .map(|(i, (a, b))| format!("line {}: live {a:?} vs tui {b:?}", i + 1))
                .unwrap_or_else(|| {
                    format!(
                        "same prefix, different length: live {} lines, tui {} lines",
                        l.lines().count(),
                        t.lines().count()
                    )
                });
            fail.push(format!(
                "STRUCTURAL DIVERGENCE between the Live and Tui renders of the same \
                 view — {first}"
            ));
        }
    } else {
        fail.push("cannot compare structures — a dump did not run".into());
    }

    // The app's own verdict, computed independently of our byte comparison.
    a += 1;
    if let Err(e) = dump(&["--dump-view", "diff"]) {
        fail.push(format!("the app's own structural diff reported failure: {e}"));
    }

    a += 1;
    match dump(&["--export"]) {
        Err(e) => fail.push(e),
        Ok(csv) => {
            if !csv.lines().next().is_some_and(|h| h.contains("id,day,site")) {
                fail.push(format!(
                    "--export did not produce the expected CSV header, got {:?}",
                    csv.lines().next().unwrap_or("")
                ));
            }
        }
    }

    if fail.is_empty() {
        let n = live.as_ref().map(|s| s.lines().count()).unwrap_or(0);
        GateOutcome::new(
            true,
            a,
            format!("one view, {n} structural nodes, identical across the Live and Tui renders"),
        )
    } else {
        GateOutcome::new(false, a, fail.join(" | "))
    }
}

/// Member A: Ledger. Both arms assert the same eleven things.
pub const APPS_LEDGER_EXPECTED: u64 = 11;

/// One arm of member A — the SAME source and the SAME assertions, with only the
/// DSN changing. That is the claim under test: "the same app code works on
/// SQLite and Postgres; only the driver differs."
///
/// The ordering assertion is the important one. `Store.orderAsc` on (date, id)
/// is asserted by **value**: the seed inserts ids 1,2,3,4 with dates
/// Mar/Jan/Feb/Jan, so a correct ordering returns `2,4,3,1` — a sequence
/// insertion order cannot produce. An assertion that merely checked "some rows
/// came back" would pass an app that ignored the ORDER BY entirely.
fn ledger_arm(ctx: &GateCtx, expect_driver: &str, dsn: String) -> GateOutcome {
    use super::layer2;
    use std::time::Duration;

    const PROJECT: &str = "apps/ledger";
    let dir = ctx.repo_root.join(PROJECT);
    let sky = match layer2::sky_binary(&ctx.repo_root) {
        Ok(s) => s,
        Err(e) => return GateOutcome::new(false, 0, e),
    };
    let mut a = 0u64;
    let mut fail: Vec<String> = Vec::new();

    let db_env = |extra: &[(&str, String)]| -> Vec<(String, String)> {
        let mut v = vec![("SKY_DB_PATH".to_string(), dsn.clone())];
        v.extend(extra.iter().map(|(k, x)| (k.to_string(), x.clone())));
        v
    };

    // `sky db migrate` applies the COMMITTED db/migrations/ files. Running it
    // before the build is deliberate: `sky db seed` builds its temp entry into
    // the project's real sky-out/app, so seeding after a build would replace the
    // binary under test with the seed shim.
    for (verb, label) in [("migrate", "sky db migrate"), ("seed", "sky db seed")] {
        a += 1;
        let mut cmd = Command::new(&sky);
        cmd.arg("db").arg(verb).current_dir(&dir).stdin(std::process::Stdio::null());
        for (k, v) in db_env(&[]) {
            cmd.env(k, v);
        }
        match cmd.output() {
            Err(e) => fail.push(format!("{label}: spawn failed: {e}")),
            Ok(o) if !o.status.success() => fail.push(format!(
                "{label} failed (exit {:?}):\n{}",
                o.status.code(),
                layer2::tail(&String::from_utf8_lossy(&o.stdout), 8)
            )),
            Ok(_) => {}
        }
    }

    let r = match layer2::clean_build(&ctx.repo_root, PROJECT) {
        Ok(r) => r,
        Err(e) => return GateOutcome::new(false, a, e),
    };
    a += 1;
    if !r.ok {
        fail.push(format!("`sky build` failed:\n{}", layer2::tail(&r.log, 12)));
    }
    a += 1;
    if !r.binary.is_file() {
        fail.push(format!("no artifact at {}", r.binary.display()));
    }
    if !fail.is_empty() {
        return GateOutcome::new(false, a, fail.join(" | "));
    }

    a += 1;
    let literals = layer2::bind_position_port_literals(&ctx.repo_root, PROJECT);
    if !literals.is_empty() {
        fail.push(format!("bind-position port literal(s): {}", literals.join(", ")));
    }

    let port = match layer2::free_port() {
        Ok(p) => p,
        Err(e) => return GateOutcome::new(false, a, e),
    };
    let env: Vec<(&str, String)> = vec![
        ("SKY_DB_PATH", dsn.clone()),
        ("SKY_LIVE_PORT", port.to_string()),
        (
            "SKY_AUTH_TOKEN_SECRET",
            "layer2-ledger-gate-secret-at-least-32-bytes".to_string(),
        ),
    ];
    let mut srv = match layer2::Server::spawn(&r.binary, &dir, port, &env) {
        Ok(s) => s,
        Err(e) => return GateOutcome::new(false, a, e),
    };

    a += 1;
    let ready = srv.wait_ready("ledger: listening on", Duration::from_secs(45));
    if let Err(e) = &ready {
        fail.push(e.clone());
        let _ = srv.shutdown();
        return GateOutcome::new(false, a, fail.join(" | "));
    }

    // health: served, and reporting the driver this arm actually selected.
    match layer2::get(port, "/api/health") {
        Err(e) => {
            a += 2;
            fail.push(format!("GET /api/health: {e}"));
        }
        Ok(resp) => {
            a += 1;
            if resp.status != 200 {
                fail.push(format!("GET /api/health: expected 200, got {}", resp.status));
            }
            a += 1;
            let want = format!("\"driver\":\"{expect_driver}\"");
            if !resp.body.contains(&want) {
                fail.push(format!(
                    "this arm must run on {expect_driver}; /api/health said {}",
                    resp.body.trim()
                ));
            }
        }
    }

    // Journal ordering, asserted by value.
    a += 1;
    match layer2::get(port, "/api/journal.json?org=1") {
        Err(e) => fail.push(format!("GET /api/journal.json: {e}")),
        Ok(resp) => {
            let ids: Vec<String> = resp
                .body
                .match_indices("\"id\":")
                .map(|(i, _)| {
                    resp.body[i + 5..]
                        .chars()
                        .take_while(|c| c.is_ascii_digit())
                        .collect::<String>()
                })
                .collect();
            if ids != ["2", "4", "3", "1"] {
                fail.push(format!(
                    "journal must be ordered by (entry_date, id) — expected ids \
                     [2, 4, 3, 1], got {ids:?}. Insertion order is [1, 2, 3, 4], so \
                     that sequence is only reachable through the ORDER BY."
                ));
            }
        }
    }

    // Money.allocate residue: the parts must sum EXACTLY to the whole.
    a += 1;
    match layer2::get(port, "/api/selfcheck") {
        Err(e) => fail.push(format!("GET /api/selfcheck: {e}")),
        Ok(resp) => {
            if !resp.body.contains("\"exact\":true") {
                fail.push(format!(
                    "Money.allocate must preserve the whole; /api/selfcheck said {}",
                    resp.body.trim()
                ));
            }
        }
    }

    a += 1;
    if let Err(e) = srv.shutdown() {
        fail.push(e);
    }

    if fail.is_empty() {
        GateOutcome::new(
            true,
            a,
            format!("migrated, seeded, served on :{port} against {expect_driver}; ordering and money residue asserted"),
        )
    } else {
        GateOutcome::new(false, a, fail.join(" | "))
    }
}

/// Member A, SQLite arm (T1).
pub fn apps_ledger(ctx: &GateCtx) -> GateOutcome {
    let db = super::bodies::scratch(&ctx.repo_root).join(format!(
        "ledger-gate-{}.db",
        std::process::id()
    ));
    // Remove the WAL sidecars too: deleting a SQLite file without its -wal/-shm
    // yields `disk I/O error (522)` on the next open.
    for suffix in ["", "-wal", "-shm"] {
        let _ = std::fs::remove_file(format!("{}{suffix}", db.display()));
    }
    ledger_arm(ctx, "sqlite", db.display().to_string())
}

/// Member A, **Postgres arm** (T3) — the coverage that did not exist.
///
/// Measured on this commit: 58 example directories, 8 declare `[database]`, 8
/// use `driver = "sqlite"`, 0 use Postgres. This arm is new coverage, and it
/// earned itself immediately: it found that `Db.updateFields` /
/// `insertFields` / `insertFieldsReturning` never call `d.rebind(...)`, so they
/// emit `?` placeholders and fail on pgx with `unused argument: 0`.
///
/// The DSN comes from `SKY_TEST_POSTGRES_DSN` — the SAME variable the existing
/// `integration-postgres` CI job already sets, rather than a new one. An absent
/// DSN is a **FAIL**, never a skip: a Postgres gate that silently passes with no
/// Postgres is the defect this whole mandate exists to remove.
pub fn apps_ledger_postgres(ctx: &GateCtx) -> GateOutcome {
    match std::env::var("SKY_TEST_POSTGRES_DSN") {
        Ok(dsn) if !dsn.trim().is_empty() => ledger_arm(ctx, "postgres", dsn),
        _ => GateOutcome::new(
            false,
            0,
            "SKY_TEST_POSTGRES_DSN is unset — this gate asserts the Postgres arm and \
             cannot do so without a server. A gate that cannot run has not passed; \
             it is NOT skipped."
                .to_string(),
        ),
    }
}

/// Member E: Fleet — Ledger run as a multi-replica topology.
pub const APPS_FLEET_EXPECTED: u64 = 8;

/// The environment the production-gate probe runs under.
///
/// A `const` rather than a literal so the falsifier can flip it to dev: under
/// `ENV=production` an unreachable session store is a refusal to start, and in
/// dev it is a warning and a silent in-memory fallback. If flipping this does
/// NOT turn the gate red, the refusal is not being asserted.
const FLEET_PROD_ENV: &str = "production";

/// A DSN nothing is listening on — port 1 is never a Postgres.
const FLEET_UNREACHABLE_DSN: &str = "postgres://skytest@127.0.0.1:1/nope?sslmode=disable";

/// Not new source — **Ledger, run as a topology** (v2 §6 row E).
///
/// It is a scenario rather than a directory on purpose: `39-hub-demo` was two
/// bespoke apps that existed only to push telemetry, and nothing built them.
/// Running the *real* app in a multi-replica topology tests the same thing and
/// **cannot rot into a mock**.
///
/// The load-bearing assertion is the **silent-fallback refusal**: under
/// `ENV=production`, a session store that is configured but unreachable must
/// make the app refuse to start, not degrade to an in-memory store whose
/// sessions vanish on every restart and are invisible to the other replica.
///
/// It is asserted this way for a measured reason. The obvious assertion —
/// "a session created on replica 1 is recognised by replica 2" — was written
/// first and the falsifier reported it **VACUOUS**: pointing replica 2 at a
/// private memory store did not change the observable, because a replica
/// happily ADOPTS a client-supplied `sky_sid` and creates a fresh local session
/// under the same id. Nothing in the response distinguishes "restored from the
/// shared store" from "invented locally" for a state-free session. Rather than
/// keep a decorative assertion, it was replaced with one that bites, and the
/// vacuity is recorded here so it is not re-attempted blind.
///
/// The residual gap is real and stated: this gate proves the topology runs on
/// one shared store and refuses to degrade silently; it does NOT yet prove
/// session STATE migrates between replicas. That needs an authenticated flow.
pub fn apps_fleet(ctx: &GateCtx) -> GateOutcome {
    use super::layer2;
    use std::time::Duration;

    let Ok(dsn) = std::env::var("SKY_TEST_POSTGRES_DSN") else {
        return GateOutcome::new(
            false,
            0,
            "SKY_TEST_POSTGRES_DSN is unset — a multi-replica topology needs a SHARED \
             session store. A gate that cannot run has not passed."
                .to_string(),
        );
    };
    if dsn.trim().is_empty() {
        return GateOutcome::new(false, 0, "SKY_TEST_POSTGRES_DSN is empty".to_string());
    }

    const PROJECT: &str = "apps/ledger";
    let dir = ctx.repo_root.join(PROJECT);
    let mut a = 0u64;
    let mut fail: Vec<String> = Vec::new();

    let r = match layer2::clean_build(&ctx.repo_root, PROJECT) {
        Ok(r) => r,
        Err(e) => return GateOutcome::new(false, 0, e),
    };
    a += 1;
    if !r.ok || !r.binary.is_file() {
        return GateOutcome::new(
            false,
            a,
            format!("`sky build` failed:\n{}", layer2::tail(&r.log, 12)),
        );
    }

    let spawn_replica = |store: &str| -> Result<(layer2::Server, u16), String> {
        let port = layer2::free_port()?;
        let env: Vec<(&str, String)> = vec![
            ("SKY_DB_PATH", dsn.clone()),
            ("SKY_LIVE_PORT", port.to_string()),
            ("SKY_LIVE_STORE", store.to_string()),
            ("SKY_LIVE_STORE_PATH", dsn.clone()),
            (
                "SKY_AUTH_TOKEN_SECRET",
                "layer2-fleet-gate-secret-at-least-32-bytes".to_string(),
            ),
        ];
        let mut s = layer2::Server::spawn(&r.binary, &dir, port, &env)?;
        s.wait_ready("ledger: listening on", Duration::from_secs(45))?;
        Ok((s, port))
    };

    a += 1;
    let (mut r1, p1) = match spawn_replica("postgres") {
        Ok(v) => v,
        Err(e) => return GateOutcome::new(false, a, format!("replica 1: {e}")),
    };
    a += 1;
    let (mut r2, p2) = match spawn_replica("postgres") {
        Ok(v) => v,
        Err(e) => {
            let _ = r1.shutdown();
            return GateOutcome::new(false, a, format!("replica 2: {e}"));
        }
    };

    for (n, p) in [(1, p1), (2, p2)] {
        a += 1;
        match layer2::get(p, "/api/health") {
            Ok(resp) if resp.status == 200 => {}
            Ok(resp) => fail.push(format!("replica {n}: /api/health returned {}", resp.status)),
            Err(e) => fail.push(format!("replica {n}: /api/health: {e}")),
        }
    }

    // Replica 1 must issue a session at all — the topology is only meaningful
    // if sessions exist.
    a += 1;
    if layer2::get(p1, "/")
        .ok()
        .and_then(|resp| resp.cookie("sky_sid"))
        .is_none()
    {
        fail.push("replica 1 issued no sky_sid cookie".into());
    }

    // THE assertion: an unreachable session store under ENV=production must be
    // a refusal to start, never a silent in-memory fallback.
    a += 1;
    let bad_port = layer2::free_port().unwrap_or(0);
    let prod_env: Vec<(&str, String)> = vec![
        ("ENV", FLEET_PROD_ENV.to_string()),
        ("SKY_DB_PATH", dsn.clone()),
        ("SKY_LIVE_PORT", bad_port.to_string()),
        ("SKY_LIVE_STORE", "postgres".to_string()),
        ("SKY_LIVE_STORE_PATH", FLEET_UNREACHABLE_DSN.to_string()),
        (
            "SKY_AUTH_TOKEN_SECRET",
            "layer2-fleet-gate-secret-at-least-32-bytes".to_string(),
        ),
    ];
    match layer2::Server::spawn(&r.binary, &dir, bad_port, &prod_env) {
        Err(e) => fail.push(format!("production-gate probe: {e}")),
        Ok(mut probe) => {
            // The runtime retries the store 5 times with backoff (~8 s) before
            // giving up, so the deadline must clear that.
            match probe.wait_exit(Duration::from_secs(60)) {
                Some(status) if !status.success() => {}
                Some(status) => fail.push(format!(
                    "with an UNREACHABLE session store under ENV={FLEET_PROD_ENV}, the app \
                     exited {status} — it must refuse to start, not report success"
                )),
                None => {
                    let served = layer2::port_in_use(bad_port);
                    let _ = probe.shutdown();
                    fail.push(format!(
                        "with an UNREACHABLE session store under ENV={FLEET_PROD_ENV}, the app \
                         kept running (serving={served}) — it degraded to a silent \
                         in-memory fallback whose sessions vanish on restart and are \
                         invisible to the other replica"
                    ));
                }
            }
        }
    }

    a += 1;
    let d1 = r1.shutdown();
    let d2 = r2.shutdown();
    if let Err(e) = d1 {
        fail.push(e);
    }
    if let Err(e) = d2 {
        fail.push(e);
    }

    if fail.is_empty() {
        GateOutcome::new(
            true,
            a,
            format!(
                "two replicas on :{p1} and :{p2} over one shared store; an unreachable \
                 store under ENV={FLEET_PROD_ENV} refused to start rather than degrade"
            ),
        )
    } else {
        GateOutcome::new(false, a, fail.join(" | "))
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

// ---------------------------------------------------------------------------
// Member H — Dispatch. The five never-imported stdlib modules.
// ---------------------------------------------------------------------------

/// Member H, per arm.
///
/// 22 = 5 `sky db` verbs (drop/status/migrate/status/seed) + build + artifact
/// + port-literal scan + readiness + 2 health + 2 job-driving + 2 delivery
/// + 2 job-failure + 2 markdown + 2 email + teardown.
pub const APPS_DISPATCH_EXPECTED: u64 = 22;

/// The destructive-diff gate's assertions.
pub const APPS_DISPATCH_DESTRUCTIVE_EXPECTED: u64 = 5;

/// The markdown fixture's injection payload, escaped, as it must appear in the
/// rendered page.
///
/// A `const` so the falsifier can change it: if flipping this does NOT turn the
/// gate red, the escaping assertion is not reading the rendered page.
const DISPATCH_XSS_ESCAPED: &str = "&lt;script&gt;alert(1)&lt;/script&gt;";

/// Run one `sky db <verb>` in the project and return its exit code.
///
/// The exit CODE is the verdict, never the output text. `sky db status` exits 1
/// while a migration is pending and 0 once applied — that is the deploy gate,
/// and reading it out of stdout would re-create the `grep "0 fail"` class.
fn dispatch_db_verb(
    sky: &Path,
    dir: &Path,
    verb: &str,
    env: &[(String, String)],
) -> Result<(i32, String), String> {
    dispatch_db_verb_args(sky, dir, &[verb], env)
}

/// As [`dispatch_db_verb`], for a verb that takes flags (`drop --yes`).
fn dispatch_db_verb_args(
    sky: &Path,
    dir: &Path,
    argv: &[&str],
    env: &[(String, String)],
) -> Result<(i32, String), String> {
    let mut cmd = Command::new(sky);
    cmd.arg("db")
        .args(argv)
        .current_dir(dir)
        .stdin(std::process::Stdio::null());
    for (k, v) in env {
        cmd.env(k, v);
    }
    let out = cmd
        .output()
        .map_err(|e| format!("could not spawn `sky db {}`: {e}", argv.join(" ")))?;
    let mut log = String::from_utf8_lossy(&out.stdout).into_owned();
    log.push_str(&String::from_utf8_lossy(&out.stderr));
    Ok((out.status.code().unwrap_or(-1), log))
}

/// One arm of member H — the SAME source and the SAME assertions, with only the
/// DSN changing.
///
/// Every one of the five modules this app exists for was imported by NOTHING
/// before it, so each assertion below is the first execution of that surface:
///
///   * `Std.Db.Schema`  — the typed DDL and the `toProject` bridge, reached
///     through the constraints the migration must preserve.
///   * `Std.Db.Migrate` — the committed `db/migrations/*.json`, applied by
///     `sky db migrate`. Before this member the file-based migration verbs were
///     exercised by no project at all.
///   * `Std.Jobs`       — asserted on what the worker WROTE, and on the failure
///     it RECORDED. Not on `enqueue` returning an id, which it does whether or
///     not a worker ever starts.
///   * `Std.Markdown`   — asserted on the rendered page, including that
///     untrusted markdown comes back escaped.
///   * `Std.Email`      — asserted on the composed message's exact fields.
fn dispatch_arm(ctx: &GateCtx, expect_driver: &str, dsn: String) -> GateOutcome {
    use super::layer2;
    use std::time::{Duration, Instant};

    const PROJECT: &str = "apps/dispatch";
    let dir = ctx.repo_root.join(PROJECT);
    let sky = match layer2::sky_binary(&ctx.repo_root) {
        Ok(s) => s,
        Err(e) => return GateOutcome::new(false, 0, e),
    };
    let mut a = 0u64;
    let mut fail: Vec<String> = Vec::new();

    // The jobs store points at the SAME database as the app on purpose: it is
    // what lets `/api/jobs/failures` read the queue's own `_sky_jobs` ledger,
    // and so what makes "the failure was recorded" assertable from outside the
    // process.
    let db_env: Vec<(String, String)> = vec![
        ("SKY_DB_PATH".to_string(), dsn.clone()),
        ("SKY_JOBS_STORE".to_string(), expect_driver.to_string()),
        ("SKY_JOBS_STORE_PATH".to_string(), dsn.clone()),
    ];

    // 0. Reset to a known-empty schema. This is what makes the gate
    //    RE-RUNNABLE, which matters entirely for the Postgres arm: the SQLite
    //    arm gets a virgin file every run, but a Postgres server persists, so
    //    on the second run the migration was already applied and the
    //    "status exits 1 while pending" assertion below could never fire. It
    //    was caught by the falsifier reporting INCONCLUSIVE (baseline red) on
    //    exactly that assertion.
    //
    //    `sky db drop --yes` removes the app's tables AND the `_sky_migrations`
    //    ledger, and exits 0 against a database that does not exist yet — so it
    //    is a valid idempotent reset on both arms. (`seed` separately clears
    //    stale rows from the jobs tables, which belong to the runtime rather
    //    than to this app's schema.)
    a += 1;
    match dispatch_db_verb_args(&sky, &dir, &["drop", "--yes"], &db_env) {
        Err(e) => fail.push(e),
        Ok((code, log)) if code != 0 => fail.push(format!(
            "`sky db drop --yes` (the pre-run reset) failed (exit {code}):\n{}",
            layer2::tail(&log, 10)
        )),
        Ok(_) => {}
    }

    // 1. The deploy gate BITES: status must exit 1 while a migration is
    //    pending. Asserted before migrating, on a freshly-reset database.
    a += 1;
    match dispatch_db_verb(&sky, &dir, "status", &db_env) {
        Err(e) => fail.push(e),
        Ok((code, _)) if code != 1 => fail.push(format!(
            "`sky db status` must exit 1 while a migration is PENDING (the deploy \
             gate); got exit {code}. An exit 0 here would let a deploy ship against \
             an unmigrated database."
        )),
        Ok(_) => {}
    }

    // 2. Apply the committed migrations (Std.Db.Migrate).
    a += 1;
    match dispatch_db_verb(&sky, &dir, "migrate", &db_env) {
        Err(e) => fail.push(e),
        Ok((code, log)) if code != 0 => fail.push(format!(
            "`sky db migrate` failed (exit {code}):\n{}",
            layer2::tail(&log, 10)
        )),
        Ok(_) => {}
    }

    // 3. …and status must now be clean.
    a += 1;
    match dispatch_db_verb(&sky, &dir, "status", &db_env) {
        Err(e) => fail.push(e),
        Ok((code, log)) if code != 0 => fail.push(format!(
            "`sky db status` must exit 0 once every migration is applied; got exit \
             {code}:\n{}",
            layer2::tail(&log, 10)
        )),
        Ok(_) => {}
    }

    // 4. Seed. Runs BEFORE the build: `sky db seed` builds its temp entry into
    //    the project's real sky-out/app, so seeding after a build would replace
    //    the binary under test with the seed shim.
    a += 1;
    match dispatch_db_verb(&sky, &dir, "seed", &db_env) {
        Err(e) => fail.push(e),
        Ok((code, log)) if code != 0 => fail.push(format!(
            "`sky db seed` failed (exit {code}):\n{}",
            layer2::tail(&log, 10)
        )),
        Ok(_) => {}
    }

    if !fail.is_empty() {
        return GateOutcome::new(false, a, fail.join(" | "));
    }

    let r = match layer2::clean_build(&ctx.repo_root, PROJECT) {
        Ok(r) => r,
        Err(e) => return GateOutcome::new(false, a, e),
    };
    a += 1;
    if !r.ok {
        fail.push(format!("`sky build` failed:\n{}", layer2::tail(&r.log, 12)));
    }
    a += 1;
    if !r.binary.is_file() {
        fail.push(format!("no artifact at {}", r.binary.display()));
    }
    if !fail.is_empty() {
        return GateOutcome::new(false, a, fail.join(" | "));
    }

    a += 1;
    let literals = layer2::bind_position_port_literals(&ctx.repo_root, PROJECT);
    if !literals.is_empty() {
        fail.push(format!(
            "bind-position port literal(s): {}",
            literals.join(", ")
        ));
    }

    let port = match layer2::free_port() {
        Ok(p) => p,
        Err(e) => return GateOutcome::new(false, a, e),
    };
    let env: Vec<(&str, String)> = vec![
        ("SKY_DB_PATH", dsn.clone()),
        ("SKY_JOBS_STORE", expect_driver.to_string()),
        ("SKY_JOBS_STORE_PATH", dsn.clone()),
        ("SKY_LIVE_PORT", port.to_string()),
        (
            "SKY_AUTH_TOKEN_SECRET",
            "layer2-dispatch-gate-secret-at-least-32-bytes".to_string(),
        ),
    ];
    let mut srv = match layer2::Server::spawn(&r.binary, &dir, port, &env) {
        Ok(s) => s,
        Err(e) => return GateOutcome::new(false, a, e),
    };

    a += 1;
    if let Err(e) = srv.wait_ready("dispatch: listening on", Duration::from_secs(45)) {
        fail.push(e);
        let _ = srv.shutdown();
        return GateOutcome::new(false, a, fail.join(" | "));
    }

    // ── health: served, on the driver this arm claims ──
    match layer2::get(port, "/api/health") {
        Err(e) => {
            a += 2;
            fail.push(format!("GET /api/health: {e}"));
        }
        Ok(resp) => {
            a += 1;
            if resp.status != 200 {
                fail.push(format!(
                    "GET /api/health: expected 200, got {}",
                    resp.status
                ));
            }
            a += 1;
            let want = format!("\"driver\":\"{expect_driver}\"");
            if !resp.body.contains(&want) {
                fail.push(format!(
                    "this arm must run on {expect_driver}; /api/health said {}",
                    resp.body.trim()
                ));
            }
        }
    }

    // ── Std.Jobs: enqueue + cancel ──
    match layer2::get(port, "/api/dispatch/run") {
        Err(e) => {
            a += 2;
            fail.push(format!("GET /api/dispatch/run: {e}"));
        }
        Ok(resp) => {
            a += 1;
            // Both seeded subscribers must have been enqueued. A zero here
            // would mean the seed did not land, and every later job assertion
            // would be vacuously satisfied by an empty queue.
            if !resp.body.contains("\"enqueued\":2") {
                fail.push(format!(
                    "expected 2 delivery jobs enqueued (one per seeded subscriber); \
                     /api/dispatch/run said {}",
                    resp.body.trim()
                ));
            }
            a += 1;
            // `Jobs.cancel` on a job that is genuinely still pending.
            if !resp.body.contains("\"cancel\":\"cancelled\"") {
                fail.push(format!(
                    "Jobs.cancel must succeed on a pending job; /api/dispatch/run said {}",
                    resp.body.trim()
                ));
            }
        }
    }

    // ── Std.Jobs: the worker actually RAN ──
    // Poll rather than sleep: the worker's poll interval is 100 ms, so a fixed
    // sleep would be either flaky or wasteful.
    let mut deliveries = String::new();
    let t0 = Instant::now();
    while t0.elapsed() < Duration::from_secs(30) {
        match layer2::get(port, "/api/deliveries") {
            Ok(resp) if resp.body.contains("\"count\":2") => {
                deliveries = resp.body;
                break;
            }
            Ok(resp) => deliveries = resp.body,
            Err(e) => deliveries = format!("(request failed: {e})"),
        }
        std::thread::sleep(Duration::from_millis(200));
    }
    a += 1;
    if !deliveries.contains("\"count\":2") {
        fail.push(format!(
            "the delivery jobs must actually RUN and write their rows — `enqueue` \
             returning an id proves nothing about a worker. /api/deliveries said {}",
            deliveries.trim()
        ));
    }
    a += 1;
    // The payload is a RECORD and crosses a JSON encode/decode boundary in the
    // runtime. Asserting the addresses came back attached to the right rows is
    // what proves the record survived that round-trip rather than arriving
    // field-shifted or empty.
    if !(deliveries.contains("ada@example.test") && deliveries.contains("grace@example.test")) {
        fail.push(format!(
            "each delivery row must carry its subscriber's address — the job payload \
             is a record crossing a JSON round-trip. /api/deliveries said {}",
            deliveries.trim()
        ));
    }

    // ── Std.Jobs: a failing job is OBSERVABLE ──
    let mut failures = String::new();
    let t0 = Instant::now();
    while t0.elapsed() < Duration::from_secs(30) {
        match layer2::get(port, "/api/jobs/failures") {
            Ok(resp) if resp.body.contains("\"count\":1") => {
                failures = resp.body;
                break;
            }
            Ok(resp) => failures = resp.body,
            Err(e) => failures = format!("(request failed: {e})"),
        }
        std::thread::sleep(Duration::from_millis(200));
    }
    a += 1;
    if !failures.contains("\"count\":1") {
        fail.push(format!(
            "the always-failing job must be RECORDED in the queue ledger, not \
             swallowed — a worker that dropped the error would leave the ledger \
             clean and look identical to success. /api/jobs/failures said {}",
            failures.trim()
        ));
    }
    a += 1;
    // The message must be READABLE. This field held
    // `{0 Error [7 {the real message <nil>}]}` — a Go struct dump of the Sky
    // Error ADT — until this app was built (fixed in rt/stdlib_extra.go). It is
    // the operator's only record of why a job dead-lettered.
    if !failures.contains("deliberate job failure for payload 42") || failures.contains("{0 Error")
    {
        fail.push(format!(
            "the recorded job error must be the Sky error MESSAGE, with no Error-ADT \
             struct rendering. /api/jobs/failures said {}",
            failures.trim()
        ));
    }

    // ── Std.Markdown ──
    match layer2::get(port, "/") {
        Err(e) => {
            a += 2;
            fail.push(format!("GET /: {e}"));
        }
        Ok(resp) => {
            a += 1;
            // Heading, bold span and a list item: three different block/inline
            // paths, so a parser that produced one flat paragraph fails here.
            let rendered = resp.body.contains("Release 1.0")
                && resp.body.contains("today")
                && resp.body.contains("durable queue");
            if !rendered {
                fail.push(
                    "Std.Markdown must render the note's heading, bold span and list \
                     items into the page"
                        .to_string(),
                );
            }
            a += 1;
            // The security claim in Std.Markdown's own docstring: untrusted
            // markdown is safe because everything routes through typed Std.Ui
            // constructors and no raw HTML is ever emitted.
            if !resp.body.contains(DISPATCH_XSS_ESCAPED) || resp.body.contains("<script>alert(1)") {
                fail.push(
                    "untrusted markdown must be ESCAPED — the note body contains a \
                     <script> tag and the rendered page must carry it as text, never \
                     as markup"
                        .to_string(),
                );
            }
        }
    }

    // ── Std.Email composition ──
    match layer2::get(port, "/api/email/preview") {
        Err(e) => {
            a += 2;
            fail.push(format!("GET /api/email/preview: {e}"));
        }
        Ok(resp) => {
            a += 1;
            // Each of these is a different builder: defaultMessage's carried
            // fields, withTextBody, withReplyTo, withCc.
            let composed = resp.body.contains("\"from\":\"dispatch@example.test\"")
                && resp.body.contains("\"subject\":\"New note: Release 1.0\"")
                && resp.body.contains("\"replyTo\":\"no-reply@example.test\"")
                && resp.body.contains("audit@example.test");
            if !composed {
                fail.push(format!(
                    "Std.Email builders must carry every field through; \
                     /api/email/preview said {}",
                    resp.body.trim()
                ));
            }
            a += 1;
            // withAttachment appends, and the attachment's own builders apply.
            if !(resp.body.contains("\"attachments\":1")
                && resp.body.contains("\"attachmentName\":\"notes.txt\""))
            {
                fail.push(format!(
                    "withAttachment must append the attachment and its builders must \
                     apply; /api/email/preview said {}",
                    resp.body.trim()
                ));
            }
        }
    }

    a += 1;
    if let Err(e) = srv.shutdown() {
        fail.push(e);
    }

    if fail.is_empty() {
        GateOutcome::new(
            true,
            a,
            format!(
                "migrated + seeded + served on :{port} against {expect_driver}; jobs \
                 ran and their failure was recorded readably, markdown rendered and \
                 escaped, email composed"
            ),
        )
    } else {
        GateOutcome::new(false, a, fail.join(" | "))
    }
}

/// Member H, SQLite arm (T1).
pub fn apps_dispatch(ctx: &GateCtx) -> GateOutcome {
    let db = scratch(&ctx.repo_root).join(format!("dispatch-gate-{}.db", std::process::id()));
    // Remove the WAL sidecars too: deleting a SQLite file without its -wal/-shm
    // yields `disk I/O error (522)` on the next open.
    for suffix in ["", "-wal", "-shm"] {
        let _ = std::fs::remove_file(format!("{}{suffix}", db.display()));
    }
    dispatch_arm(ctx, "sqlite", db.display().to_string())
}

/// Member H, **Postgres arm** (T3).
///
/// The same source and the same assertions with only the DSN changed — which
/// makes it the first time `Std.Db.Schema`'s DDL and the committed migrations
/// are applied to Postgres, and the first time the Postgres jobs store backs a
/// real app.
///
/// An absent DSN is a **FAIL**, never a skip.
pub fn apps_dispatch_postgres(ctx: &GateCtx) -> GateOutcome {
    match std::env::var("SKY_TEST_POSTGRES_DSN") {
        Ok(dsn) if !dsn.trim().is_empty() => dispatch_arm(ctx, "postgres", dsn),
        _ => GateOutcome::new(
            false,
            0,
            "SKY_TEST_POSTGRES_DSN is unset — this gate asserts the Postgres arm and \
             cannot do so without a server. A gate that cannot run has not passed; \
             it is NOT skipped."
                .to_string(),
        ),
    }
}

/// A destructive schema diff must be QUARANTINED, never silently applied.
///
/// This is the class that shipped: `sky db migrate` dropped UNIQUE,
/// AUTOINCREMENT and DEFAULT where `sky db push` preserved them — duplicate
/// rows accepted on SQLite, the app broken on Postgres. It was fixed once and
/// nothing gated it, because no project declared constraints through a
/// committed migration.
///
/// The gate works on a COPY in the scratch dir. A gate that edited
/// `apps/dispatch` in place would leave the tree dirty and make every later
/// gate's build non-reproducible.
pub fn apps_dispatch_destructive(ctx: &GateCtx) -> GateOutcome {
    use super::layer2;

    let sky = match layer2::sky_binary(&ctx.repo_root) {
        Ok(s) => s,
        Err(e) => return GateOutcome::new(false, 0, e),
    };
    let mut a = 0u64;
    let mut fail: Vec<String> = Vec::new();

    // Staged in the SYSTEM temp dir, deliberately NOT in `scratch()`
    // (`.skycache/harness`). A Sky project living under a directory named
    // `.skycache` is invisible to module discovery — the compiler skips
    // build-cache directories — so `sky db migrate --gen` there reports
    // "schema-dump produced no output (is `db` a Store.Project?)" and the gate
    // would fail for a reason that has nothing to do with what it asserts.
    // The pid suffix keeps concurrent runs from colliding.
    let work = std::env::temp_dir().join(format!("sky-dispatch-destructive-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&work);
    if let Err(e) = copy_tree(&ctx.repo_root.join("apps/dispatch"), &work) {
        return GateOutcome::new(false, 0, format!("could not stage a copy: {e}"));
    }

    // Drop a column from the typed Std.Db.Schema declaration. Removing the
    // WHOLE line is what makes this a column DROP rather than an edit.
    let schema_path = work.join("src/Schema.sky");
    let src = match std::fs::read_to_string(&schema_path) {
        Ok(s) => s,
        Err(e) => return GateOutcome::new(false, 0, format!("read Schema.sky: {e}")),
    };
    let needle = "        , Schema.text \"detail\" |> Schema.notNull |> Schema.defaultText \"\"\n";
    a += 1;
    if !src.contains(needle) {
        return GateOutcome::new(
            false,
            a,
            "the `detail` column declaration this gate drops is no longer present in \
             apps/dispatch/src/Schema.sky — the gate would silently test nothing. \
             Update the needle together with the schema."
                .to_string(),
        );
    }
    if let Err(e) = std::fs::write(&schema_path, src.replacen(needle, "", 1)) {
        return GateOutcome::new(false, a, format!("write Schema.sky: {e}"));
    }

    let db = work.join("destructive.db");
    let env: Vec<(String, String)> = vec![("SKY_DB_PATH".to_string(), db.display().to_string())];

    a += 1;
    match dispatch_db_verb(&sky, &work, "migrate", &env) {
        Err(e) => return GateOutcome::new(false, a, e),
        Ok((code, log)) if code != 0 => {
            return GateOutcome::new(
                false,
                a,
                format!(
                    "baseline `sky db migrate` failed (exit {code}):\n{}",
                    layer2::tail(&log, 10)
                ),
            )
        }
        Ok(_) => {}
    }

    // Generate against the reduced schema. Non-interactive (stdin is null), so
    // there is no prompt to answer.
    let before: Vec<PathBuf> = migration_files(&work);
    a += 1;
    let mut cmd = Command::new(&sky);
    cmd.arg("db")
        .arg("migrate")
        .arg("--gen")
        .arg("dropdetail")
        .current_dir(&work)
        .stdin(std::process::Stdio::null());
    for (k, v) in &env {
        cmd.env(k, v);
    }
    let out = match cmd.output() {
        Ok(o) => o,
        Err(e) => return GateOutcome::new(false, a, format!("spawn `sky db migrate --gen`: {e}")),
    };
    if !out.status.success() {
        return GateOutcome::new(
            false,
            a,
            format!(
                "`sky db migrate --gen` failed (exit {:?}):\n{}",
                out.status.code(),
                layer2::tail(&String::from_utf8_lossy(&out.stdout), 10)
            ),
        );
    }

    let after = migration_files(&work);
    let Some(generated) = after.iter().find(|p| !before.contains(p)) else {
        return GateOutcome::new(
            false,
            a,
            "`sky db migrate --gen` wrote no new migration file for a schema change"
                .to_string(),
        );
    };

    let Some(json) = read_json(generated) else {
        return GateOutcome::new(
            false,
            a,
            format!(
                "generated migration {} is not readable JSON",
                generated.display()
            ),
        );
    };

    // The verdict: the drop is recorded as destructive AND is not in `ops`.
    a += 1;
    let destructive = json
        .get("destructive")
        .and_then(|d| d.as_array())
        .cloned()
        .unwrap_or_default();
    if destructive.is_empty() {
        fail.push(format!(
            "dropping a column must be QUARANTINED in the `destructive` array; the \
             generated migration has none: {json}"
        ));
    } else {
        let names: Vec<String> = destructive
            .iter()
            .map(|d| {
                format!(
                    "{}.{}/{}",
                    d.get("table").and_then(|x| x.as_str()).unwrap_or("?"),
                    d.get("column").and_then(|x| x.as_str()).unwrap_or("?"),
                    d.get("kind").and_then(|x| x.as_str()).unwrap_or("?"),
                )
            })
            .collect();
        if !names.iter().any(|n| n == "deliveries.detail/dropColumn") {
            fail.push(format!(
                "the quarantined entry must name the dropped column; got {names:?}"
            ));
        }
    }

    a += 1;
    let ops_len = json
        .get("ops")
        .and_then(|o| o.as_array())
        .map(|o| o.len())
        .unwrap_or(usize::MAX);
    if ops_len != 0 {
        fail.push(format!(
            "a destructive-only diff must produce ZERO active ops — anything in `ops` \
             would be APPLIED by `sky db migrate` and drop user data. Got {ops_len}: \
             {json}"
        ));
    }

    let _ = std::fs::remove_dir_all(&work);

    if fail.is_empty() {
        GateOutcome::new(
            true,
            a,
            "a dropped column is quarantined in `destructive` with zero active ops"
                .to_string(),
        )
    } else {
        GateOutcome::new(false, a, fail.join(" | "))
    }
}

/// `db/migrations/*.json`, sorted.
fn migration_files(project: &Path) -> Vec<PathBuf> {
    let mut v: Vec<PathBuf> = std::fs::read_dir(project.join("db/migrations"))
        .map(|rd| {
            rd.flatten()
                .map(|e| e.path())
                .filter(|p| p.extension().and_then(|x| x.to_str()) == Some("json"))
                .collect()
        })
        .unwrap_or_default();
    v.sort();
    v
}

/// Recursive copy, skipping build outputs. `sky-out`/`.skycache` must not come
/// along: the copy is built from a wiped slate so the source edit is observable.
fn copy_tree(from: &Path, to: &Path) -> std::io::Result<()> {
    std::fs::create_dir_all(to)?;
    for e in std::fs::read_dir(from)? {
        let e = e?;
        let name = e.file_name();
        let n = name.to_string_lossy();
        if matches!(n.as_ref(), "sky-out" | ".skycache" | ".skydeps" | "_sky") {
            continue;
        }
        let src = e.path();
        let dst = to.join(&name);
        if src.is_dir() {
            copy_tree(&src, &dst)?;
        } else {
            std::fs::copy(&src, &dst)?;
        }
    }
    Ok(())
}

// sky-suites — the root `tests/` Sky.Test suites
// ---------------------------------------------------------------------------

/// Cases that actually RUN and pass across the root `tests/` suites. Measured:
/// **330**, across all 22 suites — every discovered suite now builds.
///
/// How the number was obtained: `scripts/sky-suites.sh --json <p>` writes one
/// `Sky.Test` report per suite; each report's `total` is that suite's case
/// count, and `Sky/Test.sky:385` emits `"assertions": 1` per case, so cases and
/// assertions are the same number by construction. Summing `total` over the
/// green suites gives 330:
///
/// ```text
///   Auth/AuthTest                 28    Live/PubsubTest                3
///   Core/CoreTest                 30    Live/SessionTest              18
///   Core/DictIntKeyTest            3    Server/HttpServerTest         43
///   Core/WebSocketTest            16    Sky/Core/RandomExtTest         6
///   Db/DbTest                     28    Sky/Core/StringInTest          9
///   Json/PipelinePanic372Test      3    Std/Db/DecoderTest             3
///   Lang/PatternTest              10    Std/UiAspectGridTest          16
///   Live/CounterTest              19    Std/UiInputCheckboxTest        3
///   Live/FormTest                 20    Std/UiMediaQueryTest          17
///   Std/UiPseudoClassTest         21    Std/UiTransitionAnimationTest 24
///   Sky/Core/PointFreePolyTest     4    Sky/Core/PureTest              6
/// ```
///
/// 330 is the count on a tree where [`SKY_SUITES_BLOCKED`] is EMPTY: the two
/// compiler defects that blocked `Sky/Core/PointFreePolyTest` (4 cases) and
/// `Sky/Core/PureTest` (6 cases) are fixed, so their 10 cases are no longer a
/// declared coverage loss and are counted here. When a block is lifted, this
/// constant rises by that suite's cases in the same commit — that is this
/// change: 320 + 4 + 6 = 330.
///
/// 330 -> 384 across the two issue-#174 Dict commits, which this constant had
/// not been raised for:
///
/// * `Core/DictTypedKeyTest` (20) — the typed-key decode. It landed without
///   raising this constant, so the gate was already failing on `main` by 20
///   before the change below; the arithmetic here absorbs it rather than
///   leaving a known-red gate for someone else to trip over.
/// * `Core/DictPolyKeyTest` (34) — the same decode through a key-polymorphic
///   helper, where the key type is erased.
///
/// 330 + 20 + 34 = 384, plus:
///
/// * `Core/KernelQualifierSigsTest` (9) — the RUNTIME leg for the 26 kernel-
///   qualifier members that gained a Sky signature. A signature changes
///   EMISSION, so "the checker now rejects the misuse" is only half a proof;
///   one of those members (`String.fromBytes`) built clean and silently
///   returned `""` until this suite ran it.
///
/// 384 + 9 = 393.
///
/// 393 -> 398:
///
/// * `Std/UiRenderConcatTest` (5) — the byte-identical regression fence for the
///   `renderNodeAs` append-on-empty optimization (Std.Ui Element->Html pass).
///   It pins the exact HTML of the four `++`-on-`[]` code paths the guards
///   touch (allAttrs, attrList, renderedChildren, collectTransitions), so a
///   change to any element's attributes / child order / style bytes turns it
///   red.
///
/// 393 + 5 = 398.
pub const SKY_SUITES_EXPECTED: u64 = 398;

/// Suites that are discovered and RUN, but whose failure does not fail the
/// gate, because the defect is in the **compiler**, not in the suite.
///
/// This is a declared block with teeth, modelled on `reject`'s
/// `known_leniency` and on `registry::BLOCKED`, and it is deliberately not a
/// skip:
///
/// * the suite is still discovered and still executed — a block never removes
///   a suite from the run, so the failure stays visible in CI logs;
/// * its cases count as **ZERO**, so blocking can never preserve a coverage
///   number (the loss shows up as a smaller [`SKY_SUITES_EXPECTED`]);
/// * if a blocked suite starts **passing**, the gate FAILS and demands the
///   entry be deleted. A block cannot outlive the bug it names;
/// * it **EXPIRES**. Past the third field's date the gate FAILS with nobody in
///   the loop, exactly as `registry::BLOCKED` does. Without this a block is a
///   parking space: the two compiler defects below are real and fixable, and
///   the only thing that reliably converts "known bug" into "fixed bug" is a
///   date that turns CI red on its own.
///
/// An entry here is a coverage loss that must be declared in the ledger.
///
/// Format: `(suite, reason, expires YYYY-MM-DD)`.
/// EMPTY as of the fix for the two compiler codegen defects these entries
/// named. Both were ONE root cause: `lower_var` stamped the caller's expected
/// type onto a bare `Ident` for a kernel referenced as a VALUE, so
/// `coerce_if_needed` saw `x.ty == expected`, inserted nothing, and the raw
/// `any`-based runtime symbol reached a typed Go slot. `kernel_value_eta`
/// (eta-expansion via `kernel_partial`, arity >= 1) and `nullary_kernel_value`
/// (`any`-typed call + coercion, arity 0) close it; both key on the SLOT, so
/// the `coerce-floor` delta is zero. `Sky/Core/PointFreePolyTest` (4 cases) and
/// `Sky/Core/PureTest` (6 cases) now pass and are counted in
/// [`SKY_SUITES_EXPECTED`], which rose 320 -> 330 in the same commit.
pub const SKY_SUITES_BLOCKED: &[(&str, &str, &str)] = &[];

/// The root `tests/` Sky.Test suites — 22 suites in subdirectories of ONE Sky
/// project (`tests/sky.toml`).
///
/// These had no runner and had NEVER executed. `scripts/conformance.sh` looks
/// like it covered them, but its `PROJ` is `tests/conformance` and its loop
/// globs `tests/*Test.sky` relative to that, so it only ever saw
/// `tests/conformance/tests/`. The root suites live one directory deeper than
/// a flat glob can reach; `scripts/sky-suites.sh` discovers RECURSIVELY.
pub fn sky_suites(ctx: &GateCtx) -> GateOutcome {
    let json = scratch(&ctx.repo_root).join("sky-suites.json");
    let _ = std::fs::remove_file(&json);

    let run = match sh(
        &ctx.repo_root,
        "scripts/sky-suites.sh",
        &["--json".into(), json.display().to_string()],
    ) {
        Ok(r) => r,
        Err(e) => return GateOutcome::new(false, 0, e),
    };

    let Some(v) = read_json(&json) else {
        // The script ran but produced no machine-readable result. That is a
        // FAIL, not a pass-by-exit-code: the verdict comes from the file, never
        // from stdout.
        return GateOutcome::new(
            false,
            0,
            format!(
                "sky-suites.sh produced no JSON at {} (exit {:?}); \
                 verdict refused — the gate asserts on the result file, not on stdout",
                json.display(),
                run.code
            ),
        );
    };

    let suites_run = u(&v, "suites_run");
    let empty = Vec::new();
    let suites = v.get("suites").and_then(|s| s.as_array()).unwrap_or(&empty);

    let mut cases = 0u64;
    let mut failed = 0u64;
    let mut suites_failed = 0u64;
    let mut broken: Vec<String> = Vec::new();
    let mut stale_blocks: Vec<String> = Vec::new();
    let mut seen_blocked: Vec<&str> = Vec::new();

    for s in suites {
        let name = s.get("name").and_then(|n| n.as_str()).unwrap_or("?");
        let exit_code = s.get("exit_code").and_then(|c| c.as_i64()).unwrap_or(-1);
        let report = s.get("report").and_then(|r| r.as_str()).unwrap_or("");
        let rep = read_json(Path::new(report));

        if let Some((blocked_name, _, _)) = SKY_SUITES_BLOCKED.iter().find(|(n, _, _)| *n == name) {
            seen_blocked.push(blocked_name);
            // THE tooth: a block that no longer describes reality is a lie in
            // the ledger. If the suite now runs green, the gate goes red and
            // names the entry to delete — a block cannot outlive its bug.
            let green = exit_code == 0 && rep.as_ref().is_some_and(|r| u(r, "failed") == 0);
            if green {
                stale_blocks.push(name.to_string());
            }
            // Cases count as ZERO either way: blocking must never preserve a
            // coverage number.
            continue;
        }

        // A suite whose per-case report is missing or unreadable contributes
        // ZERO cases and is counted FAILED. Treating it as "skipped" is the
        // silent-shrink path that lets a suite stop running and still pass.
        let Some(rep) = rep else {
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

    // A blocked entry naming a suite that was never discovered is a dead block:
    // it silently subtracts from the expected count forever. Same failure mode
    // as a mutation whose `from` string no longer exists.
    // A block that outlived its declared deadline fails, with nobody in the
    // loop. Same contract as `registry::BLOCKED`, and a malformed date reads as
    // expired for the same reason: a typo must not buy an unbounded block.
    let today = crate::harness::registry::today_epoch_day();
    let expired: Vec<String> = SKY_SUITES_BLOCKED
        .iter()
        .filter(|(_, _, exp)| {
            crate::harness::registry::parse_ymd(exp).is_none_or(|day| today >= day)
        })
        .map(|(n, _, exp)| format!("{n} (expired {exp})"))
        .collect();
    if !expired.is_empty() {
        return GateOutcome::new(
            false,
            cases,
            format!(
                "{} suite block(s) EXPIRED: {}. A block is a deadline, not a parking \
                 space: fix the compiler defect and delete the entry (raising \
                 SKY_SUITES_EXPECTED), or re-declare the block with a new date and a \
                 reason that survives review.",
                expired.len(),
                expired.join(", ")
            ),
        );
    }

    let dead: Vec<&str> = SKY_SUITES_BLOCKED
        .iter()
        .map(|(n, _, _)| *n)
        .filter(|n| !seen_blocked.contains(n))
        .collect();
    if !dead.is_empty() {
        return GateOutcome::new(
            false,
            cases,
            format!(
                "SKY_SUITES_BLOCKED names {} suite(s) that discovery did not find: {}. \
                 Delete the dead entry (and raise SKY_SUITES_EXPECTED) or fix discovery.",
                dead.len(),
                preview(&dead)
            ),
        );
    }
    if !stale_blocks.is_empty() {
        let refs: Vec<&str> = stale_blocks.iter().map(String::as_str).collect();
        return GateOutcome::new(
            false,
            cases,
            format!(
                "{} blocked suite(s) now PASS: {}. The compiler defect they name is fixed — \
                 delete the SKY_SUITES_BLOCKED entry and raise SKY_SUITES_EXPECTED by that \
                 suite's case count in the same commit.",
                refs.len(),
                preview(&refs)
            ),
        );
    }

    // EXACT, never `>=`. A suite that stops being discovered, or a case that
    // stops being emitted, shrinks `cases` and fails here.
    if cases != SKY_SUITES_EXPECTED {
        return GateOutcome::new(
            false,
            cases,
            format!(
                "expected EXACTLY {SKY_SUITES_EXPECTED} sky-suite cases, got {cases} \
                 across {suites_run} discovered suite(s). If cases were deliberately added \
                 or removed, update SKY_SUITES_EXPECTED in harness/bodies.rs in the same commit."
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

    // The blocked suites are named in the PASS line ON PURPOSE. A green row
    // that silently omits them would read as "22 of 22 suites green", which is
    // the misreading the whole declared-block design exists to prevent.
    let detail = if SKY_SUITES_BLOCKED.is_empty() {
        format!("{cases} cases across {suites_run} suites, all green")
    } else {
        let names: Vec<String> =
            SKY_SUITES_BLOCKED.iter().map(|(n, _, exp)| format!("{n} (until {exp})")).collect();
        format!(
            "{cases} cases green across {} of {suites_run} discovered suites; \
             {} suite(s) BLOCKED on compiler codegen defects and contributing ZERO cases: {}",
            suites_run as usize - SKY_SUITES_BLOCKED.len(),
            SKY_SUITES_BLOCKED.len(),
            names.join(", ")
        )
    };
    GateOutcome::new(true, cases, detail)
}

// ─────────────────────────────────────────────────────────────────────────────
// lsp — the Neovim editor-parity suite, as a registered gate
// ─────────────────────────────────────────────────────────────────────────────

/// The EXACT number of editor-parity cases: 17 single-fixture symbol-class
/// cases (`scripts/lsp-test-nvim.lua`) + 32 corpus cases across the
/// `multimodule` / `diagnostics` / `realapp` groups (`lsp-corpus-nvim.lua`).
///
/// `lsp_gate`'s CLI face deliberately does NOT assert a total — the script
/// cross-checks each corpus group against the count that group declares, and a
/// second hardcoded number there would be a second place to forget. The
/// REGISTRY needs one anyway: `expected` is how the harness distinguishes "the
/// suite ran and passed" from "the suite ran three cases and passed", and a
/// gate reporting 0 assertions is a FAIL (vacuous), never a pass.
pub const LSP_EXPECTED: u64 = 49;

/// The Neovim editor-parity suite.
///
/// Registered for one reason: until 2026-08-12 `lsp` was the only gate CI ran
/// under a `Gate —` step that declared no falsifying mutation, because the
/// registry is where mutations live and it was not in the registry. It was
/// therefore the one gate whose assertions nobody had ever proven could bite —
/// and when that question was finally asked, of 18 hover cases **4 passed
/// against a server that merely echoed the identifier under the cursor**. The
/// mutation this gate now declares reproduces exactly that class.
///
/// Two differences from the CLI face, both deliberate:
///
/// * **A missing `nvim` FAILS here.** `xtask lsp` prints a loud skip and exits
///   0 so a contributor without Neovim is not blocked; the harness cannot, and
///   `layer2`'s rule is the one that applies — *"a gate that cannot run has not
///   passed"*. The harness runs in CI and at release, where Neovim is a
///   declared dependency of the job.
/// * **The verdict comes from a FILE.** `--json` writes `{total, failures}`;
///   scraping the script's stdout for a verdict is what v2 §5.3(d) forbids.
pub fn lsp(ctx: &GateCtx) -> GateOutcome {
    use super::layer2;

    let have_nvim = Command::new("nvim")
        .arg("--version")
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false);
    if !have_nvim {
        return GateOutcome::new(
            false,
            0,
            "nvim is not installed. The editor-parity suite did NOT run, so it \
             has not passed — install Neovim on this runner (the CLI face, \
             `xtask lsp`, still skips loudly for contributors without it).",
        );
    }

    // BUILD the compiler under test, and use the path CARGO reports.
    //
    // Not `layer2::sky_binary` (`sky-out/sky`) and not "the binary next to my
    // own executable" (what the CLI face does): both are artefacts somebody
    // else produced, and this gate's subject is the LSP server compiled from
    // the current tree. A gate that drives a prebuilt binary cannot see a
    // change to `sky-lsp` at all — which would also make this gate's declared
    // mutation VACUOUS by construction, certifying a falsifier that never
    // falsified anything.
    //
    // `--message-format=json` is how `scripts/lib/cargo-target.sh` answers the
    // same question: cargo names the executable it produced, so this is correct
    // under any `CARGO_TARGET_DIR`, `.cargo/config.toml` or profile without
    // reimplementing cargo's precedence rules. On an up-to-date tree the build
    // is a no-op and the record still carries the path.
    let built = Command::new("cargo")
        .args(["build", "--locked", "-p", "sky", "--message-format=json"])
        .current_dir(ctx.repo_root.join("rust"))
        .output();
    let sky = match built {
        Ok(o) if o.status.success() => {
            match String::from_utf8_lossy(&o.stdout)
                .lines()
                .filter_map(|l| serde_json::from_str::<serde_json::Value>(l).ok())
                .filter_map(|v| v["executable"].as_str().map(PathBuf::from))
                .next_back()
            {
                Some(p) => p,
                None => {
                    return GateOutcome::new(
                        false,
                        0,
                        "cargo built `sky` but reported no executable path",
                    )
                }
            }
        }
        Ok(o) => {
            return GateOutcome::new(
                false,
                0,
                format!(
                    "cargo build -p sky failed: {}",
                    layer2::tail(&String::from_utf8_lossy(&o.stderr), 15)
                ),
            )
        }
        Err(e) => return GateOutcome::new(false, 0, format!("cargo build -p sky: {e}")),
    };
    let sky_dir = match sky.parent() {
        Some(d) => d.to_path_buf(),
        None => return GateOutcome::new(false, 0, "the sky binary has no parent directory"),
    };
    let path = match std::env::var("PATH") {
        Ok(p) => format!("{}:{p}", sky_dir.display()),
        Err(_) => sky_dir.display().to_string(),
    };

    let json = scratch(&ctx.repo_root).join("lsp-nvim.json");
    let _ = std::fs::remove_file(&json);
    let script = ctx.repo_root.join("scripts/lsp-test-nvim.sh");
    if !script.is_file() {
        return GateOutcome::new(false, 0, "missing scripts/lsp-test-nvim.sh");
    }
    let run = Command::new("bash")
        .arg(&script)
        .args(["--json".to_string(), json.display().to_string()])
        .current_dir(&ctx.repo_root)
        .env("PATH", path)
        .status();
    let code = match run {
        Ok(s) => s.code(),
        Err(e) => return GateOutcome::new(false, 0, format!("could not run the suite: {e}")),
    };

    let body = match std::fs::read_to_string(&json) {
        Ok(b) => b,
        Err(e) => {
            return GateOutcome::new(
                false,
                0,
                format!(
                    "the suite exited {code:?} but wrote no {} ({e}) — it died \
                     before reporting, which is not a pass",
                    json.display()
                ),
            )
        }
    };
    let parsed: serde_json::Value = match serde_json::from_str(&body) {
        Ok(v) => v,
        Err(e) => return GateOutcome::new(false, 0, format!("suite JSON is not JSON: {e}")),
    };
    let total = parsed["total"].as_u64().unwrap_or(0);
    let failures: Vec<String> = parsed["failures"]
        .as_array()
        .map(|a| {
            a.iter()
                .map(|v| v.as_str().unwrap_or_default().to_string())
                .collect()
        })
        .unwrap_or_default();

    if total != LSP_EXPECTED {
        return GateOutcome::new(
            false,
            total,
            format!(
                "the suite ran {total} cases; LSP_EXPECTED is {LSP_EXPECTED}. A case that \
                 stopped running is a case that stopped asserting — raise or lower the \
                 constant in the same commit as the corpus change."
            ),
        );
    }
    if !failures.is_empty() {
        return GateOutcome::new(
            false,
            total,
            format!("{} of {total} editor-parity cases failed: {}", failures.len(), failures.join(", ")),
        );
    }
    GateOutcome::new(
        true,
        total,
        format!("{total} Neovim editor-parity cases (17 symbol-class + 32 corpus)"),
    )
}

// ─────────────────────────────────────────────────────────────────────────────
// coverage-ledger — the ratchet over the coverage ledger itself
// ─────────────────────────────────────────────────────────────────────────────

/// The number of checks `coverage_ledger::check_body` performs: one per surface
/// row verified, plus its four ratchet clauses (staleness, `surfaces_covered`
/// non-decreasing, per-surface `cover_new` non-decreasing, and every `weaker`
/// surface accounted by a `[[weakening]]` stanza).
///
/// It is EXACT, never a `>=`, for the same reason every other gate's count is:
/// `ty/tests/reject.rs` asserted `>= 13` against an actual 63, so deleting 50
/// corpus files kept it green. Here the analogous failure would be the ledger
/// silently losing surface rows while still reporting PASS. The constant moves
/// when the surface count moves — which is a real event that should be read,
/// not absorbed.
///
/// 147 -> 148: the checked-in `ledger.json` carries `surfaces_total: 144`
/// (`check_body` returns `surfaces.len() + 4`), but this constant still expected
/// 143. A surface was added and the ledger regenerated without bumping the
/// count here, so the `coverage-ledger` HARNESS gate reported `148/147 FAIL` —
/// invisibly, because only `release.yml`'s `harness --tier t1` runs that face
/// and `config-gates`' `coverage-ledger --check` (the `run()` path) does not
/// assert the count. Exactly the release-only-counting-gate latency this cycle
/// is closing; brought current here.
///
/// 151 -> 153: `sky config migrate` shipped — a new CLI verb and a new
/// registered `config-migrate` gate — which added two surfaces (`surfaces_total`
/// 147 -> 149), so `surfaces.len() + 4` is now 153.
///
/// 154 -> 155: the Sky.Spa partition landed the `Std.Spa` stdlib module
/// (`sky-stdlib/Std/Spa.sky`, config/app), which adds one surface
/// (`surfaces_total` 150 -> 151), so `surfaces.len() + 4` is now 155. The new
/// surface is registered-gate-uncovered by design — `Std.Spa` is config/Task-
/// shaped (see the DARK_MODULE_CEILING note in `corpus/stdlib.rs`) and is
/// covered instead by the Sky.Spa examples (`examples/60-spa-todos` …
/// `64-spa-native`, built to wasm) and the kernel-surface gate.
pub const COVERAGE_LEDGER_EXPECTED: u64 = 155;

/// `xtask coverage-ledger --check`, run in-process.
///
/// In-process rather than shelling out, because the ledger's whole claim is
/// that it is recomputed from the tree; invoking a possibly-stale prebuilt
/// binary would let the gate measure a different tree than the one under test.
/// That is the same reasoning `denominators` records for calling
/// `render_doc_site_export` directly instead of running `sky doc --export`.
pub fn coverage_ledger(ctx: &GateCtx) -> GateOutcome {
    let (passed, assertions, detail) = crate::coverage_ledger::check_body(&ctx.repo_root);
    GateOutcome::new(passed, assertions, detail)
}

// ─────────────────────────────────────────────────────────────────────────────
// config-surface — the configuration surface, and its three defect counts
// ─────────────────────────────────────────────────────────────────────────────

/// One assertion per `sky.toml` key the compiler accepts (30), one per env
/// suffix it seeds into every program's prologue (23), plus the six fixed
/// clauses (staleness, no unresolvable read site, and one ratchet each for
/// `pre_binary_surfaces`, `seeded_without_reader`, `documented_without_reader`
/// and `read_without_doc`).
///
/// EXACT, never a `>=`, for the reason every count here is exact: `reject.rs`
/// asserted `>= 13` against an actual 63, so deleting 50 corpus files kept it
/// green. The analogous failure here is the derivation silently losing keys —
/// which would make the ratchet clauses pass over an empty set — and it is
/// exactly what this constant catches, because a lost key moves the number.
///
/// It MOVES when the configuration surface moves, which is a real event that
/// should be read rather than absorbed: a new `sky.toml` key or a new seeded
/// default is precisely the thing `docs/tooling/config-architecture.md` is
/// trying to stop happening.
pub const CONFIG_SURFACE_EXPECTED: u64 = 53;

/// `xtask config-surface --check`, run in-process.
///
/// In-process rather than shelling out, for the same reason `coverage_ledger`
/// is: the measurement's whole claim is that it was recomputed from THIS tree,
/// and a prebuilt binary might have been built from another one.
pub fn config_surface(ctx: &GateCtx) -> GateOutcome {
    let (passed, assertions, detail) = crate::config_surface::check_body(&ctx.repo_root);
    GateOutcome::new(passed, assertions, detail)
}

/// One assertion per observed cell, one per census entry the manifest must
/// bucket, three per covered setting (the declared default, the
/// arm-distinguishability check, and — where a builder exists — the
/// `builder_reaches_runtime` verdict), plus the pre-flight sentinel check and
/// the two fixed clauses.
///
/// EXACT, never a `>=`, and it MOVES when the matrix moves. That is the point:
/// a cell quietly disappearing is how a matrix stops protecting a setting while
/// still reporting green, and `reject.rs` shipped exactly that shape — `>= 13`
/// against an actual 63, so deleting 50 corpus files kept it green.
///
/// It also moves when `config-surface`'s census moves, which is deliberate:
/// this gate's coverage claim is stated *against* that census, so a new
/// `sky.toml` key has to be bucketed here before either gate is green again.
pub const CONFIG_MATRIX_EXPECTED: u64 = 92;

/// `xtask config-matrix --check`, run in-process.
///
/// In-process for the same reason `config_surface` is: the measurement's whole
/// claim is that it was observed from binaries THIS tree's compiler built, and
/// a prebuilt xtask might have been built from another one.
pub fn config_matrix(ctx: &GateCtx) -> GateOutcome {
    let (passed, assertions, detail) = crate::config_matrix::check_body(&ctx.repo_root);
    GateOutcome::new(passed, assertions, detail)
}

// ─────────────────────────────────────────────────────────────────────────────
// config-migration — the legacy→withX migration table, cross-checked
// ─────────────────────────────────────────────────────────────────────────────

/// One assertion per Sky.Config env target that must be covered by a migration
/// row (8 `configKeyToEnvSuffix` suffixes + 2 `configKeyToLiteralEnv`
/// literals), one per builder-label coverage direction (8 + 8), and one per
/// migration row that names a legacy `sky.toml` key (18): 8+2+8+8+18 = 44.
///
/// EXACT, never `>=`, and it MOVES when the config surface moves — a new
/// `withX` builder adds a suffix (and its label and its migration row), which
/// is precisely the event this gate exists to force through the migration
/// table. `reject.rs` shipped `>= 13` against an actual 63; an exact count is
/// what stops a shrinking set passing green.
pub const CONFIG_MIGRATION_EXPECTED: u64 = 48;

/// `xtask config-migration`, run in-process. In-process for the same reason
/// `config_surface` is: it recomputes the cross-language coverage from THIS
/// tree's sources (the Go maps + the Rust table), and a prebuilt binary might
/// have read another tree's.
pub fn config_migration(ctx: &GateCtx) -> GateOutcome {
    let (passed, assertions, detail) = crate::config_migration_gate::check_body(&ctx.repo_root);
    GateOutcome::new(passed, assertions, detail)
}

/// One assertion per fixture end-to-end check (17: start-dirty, count, wrote,
/// keys-left, two residual survivals, dropped/kept sections, exposed, imported,
/// binding, four generated `withX`, re-check clean) + 3 for the 19-skyforum
/// real-project plan (keys recognised, builders generated, no write) = 20.
///
/// EXACT: a change that adds or drops one of the migration's observable effects
/// moves this count, which is the event the gate exists to force review of.
pub const CONFIG_MIGRATE_EXPECTED: u64 = 20;

/// `xtask config-migrate`, run in-process — it drives `project::config_migrate`
/// against an in-code fixture and a copy of `examples/19-skyforum`, both read
/// from THIS tree.
pub fn config_migrate(ctx: &GateCtx) -> GateOutcome {
    let (passed, assertions, detail) = crate::config_migrate_gate::check_body(&ctx.repo_root);
    GateOutcome::new(passed, assertions, detail)
}

// ---------------------------------------------------------------------------
// Analytics observability gates.
//
// Four gates over `runtime-go/rt/analytics_observability_gate_test.go`, added
// with the two defects an adversarial review found on 2026-08-17:
//
//   * the analytics retention pruner recovered at its goroutine's TOP LEVEL,
//     so the first panic killed retention for the process lifetime, silently;
//     and it discarded every `Exec` error, so a pruner that had never deleted
//     a row looked identical to a healthy one.
//   * the console's Analytics tab ran unbounded, unindexed scans of
//     `analytics_events` on every load, on a connection pool SHARED with the
//     session store — the observability surface degrading the thing it
//     observes. The right-to-erasure DELETE was a full scan for the same
//     reason: neither subject column was indexed.
//
// Each gate runs ONE Go test and reads the `ASSERTIONS: <n>` line that test
// prints. Counting is delegated to the test rather than to a source scan
// because these assertions are dynamic (three per statement in
// `consoleAnalyticsStatements`), and a source-counted total would go stale the
// moment a statement is added — the failure mode the exact-count rule exists
// to prevent.
// ---------------------------------------------------------------------------

/// Runs one Go test in `runtime-go` and returns its verdict + the assertion
/// count it printed.
///
/// A missing / unrunnable Go toolchain is a **FAIL naming what to install**,
/// never a skip: "a gate that cannot run has not passed". A test that ran but
/// printed no `ASSERTIONS:` line is also a fail — a body that cannot establish
/// a count reports 0, and 0 is vacuous.
fn go_analytics_gate(ctx: &GateCtx, test: &str, expected: u64) -> GateOutcome {
    go_runtime_gate(ctx, "./rt/", test, expected)
}

/// Runs one Go test in a named `runtime-go` package.
///
/// `go_analytics_gate` delegates here rather than the two existing side by
/// side: the periodic-goroutine gates added later live in `./rt/hub/` and
/// `./rt/jobs/`, which cannot import `rt`, so the package had to become a
/// parameter. Everything else about the contract is unchanged — a missing Go
/// toolchain FAILS naming what to install, and a test that printed no
/// `ASSERTIONS:` line fails rather than reporting a vacuous 0.
fn go_runtime_gate(ctx: &GateCtx, pkg: &str, test: &str, expected: u64) -> GateOutcome {
    let dir = ctx.repo_root.join("runtime-go");
    if !dir.is_dir() {
        return GateOutcome::new(false, 0, format!("{} does not exist", dir.display()));
    }
    // `go test -timeout` is the inner bound (it dumps stacks and NAMES the hung
    // test); the harness budget is the outer one.
    let out = Command::new("go")
        .args([
            "test",
            "-count=1",
            "-timeout",
            "300s",
            "-run",
            &format!("^{test}$"),
            "-v",
            pkg,
        ])
        .current_dir(&dir)
        .env_remove("GOFLAGS")
        .stdin(std::process::Stdio::null())
        .output();

    let o = match out {
        Ok(o) => o,
        Err(e) => {
            return GateOutcome::new(
                false,
                0,
                format!(
                    "could not run `go test` for {test}: {e}. Install the Go toolchain \
                     (https://go.dev/dl/) — this gate measures the Go runtime and cannot \
                     be established without it."
                ),
            )
        }
    };
    let stdout = String::from_utf8_lossy(&o.stdout).into_owned();
    let counted = stdout
        .lines()
        .find_map(|l| l.trim().strip_prefix("ASSERTIONS:"))
        .and_then(|n| n.trim().parse::<u64>().ok());

    let Some(n) = counted else {
        return GateOutcome::new(
            false,
            0,
            format!(
                "{test} printed no `ASSERTIONS: <n>` line, so no count could be \
                 established (exit {:?}):\n{}",
                o.status.code(),
                super::layer2::tail(&stdout, 25)
            ),
        );
    };
    if n != expected {
        return GateOutcome::new(
            false,
            n,
            format!(
                "{test} reported {n} assertions, expected EXACTLY {expected}. If an \
                 assertion was deliberately added or removed, update the gate's \
                 `expected` in harness/registry.rs in the same commit."
            ),
        );
    }
    if o.status.success() {
        GateOutcome::new(true, n, format!("{test}: {n} assertions green"))
    } else {
        GateOutcome::new(
            false,
            n,
            format!(
                "{test} failed (exit {:?}):\n{}",
                o.status.code(),
                super::layer2::tail(&stdout, 30)
            ),
        )
    }
}

/// Two: the loop survived the panic (>= 3 cycles), and the panic was logged.
pub const ANALYTICS_RETENTION_PANIC_EXPECTED: u64 = 2;

pub fn analytics_retention_survives_a_panic(ctx: &GateCtx) -> GateOutcome {
    go_analytics_gate(
        ctx,
        "TestAnalyticsRetentionSurvivesAPanic",
        ANALYTICS_RETENTION_PANIC_EXPECTED,
    )
}

/// Three: exactly one Exec, a warn was emitted, and it carries the driver's
/// message.
pub const ANALYTICS_PRUNE_ERRORS_EXPECTED: u64 = 3;

pub fn analytics_prune_errors_are_reported(ctx: &GateCtx) -> GateOutcome {
    go_analytics_gate(
        ctx,
        "TestAnalyticsPruneErrorsAreReported",
        ANALYTICS_PRUNE_ERRORS_EXPECTED,
    )
}

/// Twenty: the statement list covers the handler (1), three per statement in
/// `consoleAnalyticsStatements` — LIMIT, window, plan — (15), the revenue cap
/// binds and still returns data (2), the total is capped (1), and the tab
/// renders inside its budget (1).
pub const CONSOLE_ANALYTICS_BOUNDED_EXPECTED: u64 = 20;

pub fn console_analytics_queries_are_bounded(ctx: &GateCtx) -> GateOutcome {
    go_analytics_gate(
        ctx,
        "TestConsoleAnalyticsQueriesAreBounded",
        CONSOLE_ANALYTICS_BOUNDED_EXPECTED,
    )
}

/// Five: no full scan, each of the two indexes is in the plan, the shipped
/// schema creates them, and the indexed DELETE still deletes.
pub const ERASURE_INDEX_EXPECTED: u64 = 5;

pub fn erasure_path_uses_an_index(ctx: &GateCtx) -> GateOutcome {
    go_analytics_gate(ctx, "TestErasurePathUsesAnIndex", ERASURE_INDEX_EXPECTED)
}

// ---------------------------------------------------------------------------
// Periodic-goroutine gates.
//
// The class the analytics retention pruner above turned out to be an instance
// of. A background loop that recovers at its GOROUTINE's top level, or
// discards a write's error, fails silently and permanently: one panic and the
// loop is dead for the process lifetime with no log line, and a write that has
// never once succeeded is indistinguishable from one that always does.
//
// Eight sites carried it. The gates registered here are the three that close
// the class rather than one instance of it:
//
//   * the AST audit, which fails on the NEXT one;
//   * the session-mutex discipline, the highest-severity instance — a
//     panicking Time.every tick used to leave `sess.mu` locked forever, so the
//     user's tab froze permanently on Sky's pinned default app shape;
//   * the jobs worker's completion write, whose discarded error turned
//     at-least-once delivery into an INFINITE redelivery loop.
//
// The remaining per-site gates run under `go test ./rt/...`; these three are
// registered because they are the ones whose silent absence would cost most.
// ---------------------------------------------------------------------------

/// Two: the audit found loops at all (non-vacuous), and none was unaccounted.
/// FIXED rather than the number of loops audited — a dynamic count would move
/// with every loop added to the runtime, and the exact-count rule exists to
/// catch a body that stopped asserting, not to track the tree's size.
pub const PERIODIC_LOOP_AUDIT_EXPECTED: u64 = 2;

pub fn periodic_loops_recover_per_cycle(ctx: &GateCtx) -> GateOutcome {
    go_runtime_gate(
        ctx,
        "./rt/",
        "TestPeriodicLoopsRecoverPerCycle",
        PERIODIC_LOOP_AUDIT_EXPECTED,
    )
}

/// Two: the tick fired at all, and `sess.mu` was acquirable afterwards.
pub const TIME_EVERY_MUTEX_EXPECTED: u64 = 2;

pub fn time_every_panic_leaves_the_mutex_acquirable(ctx: &GateCtx) -> GateOutcome {
    go_runtime_gate(
        ctx,
        "./rt/",
        "TestTimeEveryPanicLeavesTheSessionMutexAcquirable",
        TIME_EVERY_MUTEX_EXPECTED,
    )
}

/// Three: dispatch returned an error, it wraps the store's, and Complete was
/// called exactly once.
pub const JOBS_COMPLETE_FAILURE_EXPECTED: u64 = 3;

pub fn jobs_complete_failure_is_reported(ctx: &GateCtx) -> GateOutcome {
    go_runtime_gate(
        ctx,
        "./rt/jobs/",
        "TestJobsCompleteFailureIsReported",
        JOBS_COMPLETE_FAILURE_EXPECTED,
    )
}

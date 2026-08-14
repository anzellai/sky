//! The G2.x engine gates — the ones whose subject is `runtime-go/bluedb`'s
//! commit path, and which therefore could not be written until P1 Stage 2
//! landed `committer.go` (see [`super::pending::p1_engine`]).
//!
//! # Why every body here runs `go test` behind THREE assertions
//!
//! `go test -run 'TestNoSuchThing'` **exits 0**. Reproduced on this repo, on
//! this Go toolchain. A gate body that shells out and classifies on the exit
//! status alone therefore reports PASS having executed nothing — which is the
//! precise shape of green lie this whole harness exists to make impossible, and
//! the one the plan names as the trap for these two gates specifically.
//!
//! No one of the three is sufficient, so all three are mandatory:
//!
//! 1. **The population is pinned in source, not discovered from the run.** Each
//!    gate declares the EXACT set of test function names it certifies, and
//!    cross-checks that set against the `func Test…` declarations parsed out of
//!    the Go file. A deleted test is a FAIL, not a smaller green run; an added
//!    one is a FAIL until it is recorded here. This is the mechanism
//!    `harness/bodies.rs`'s `CLI_VERBS_EXPECTED` already uses, sharpened from a
//!    count to a set (a count alone cannot see a rename, and a rename is how a
//!    test silently stops matching `-run`).
//! 2. **`-count=1`.** Without it Go serves `ok (cached)` from the test result
//!    cache, having run no test binary at all — an exit-0 with no execution,
//!    the same failure as (1) by a different road.
//! 3. **`-json`, parsed for exactly N `pass` actions.** Exit status is not
//!    evidence of what ran. The runner requires the set of tests that reported
//!    `Action:"pass"` to be EQUAL to the pinned set — not a superset, not a
//!    non-empty subset.
//!
//! # Why the failure detail carries the Go test output verbatim
//!
//! `mutations.rs` classifies a mutation by whether the gate's declared `expect`
//! string appears in its output. That string is copied from a REAL observed
//! failure of the Go assertion (never composed, per the plan's risk 6), so the
//! body must surface the test's own log lines rather than a summary of its own
//! authorship. A gate that paraphrased its subject's failure could be made to
//! emit the magic words without the property ever having been violated.

use std::collections::BTreeSet;
use std::process::Command;
use std::time::Duration;

use super::gates_g0::capped;
use super::registry::{Ctx, GateOutcome};

// ---------------------------------------------------------------------------
// The shared `go test` runner
// ---------------------------------------------------------------------------

/// Where the Go module lives, relative to `ctx.root()`.
const GO_MODULE_DIR: &str = "runtime-go";
/// The package under test.
const GO_PACKAGE: &str = "./bluedb/";

/// The build tag every shipped BlueDB build carries (G0.5). Running the gates
/// without it would certify a build configuration no app ever executes.
const ZSTD_TAG: &str = "pebblegozstd";

/// How many lines of a failing run are quoted into the findings. Bounded
/// because an unbounded quote is not a report: one C8 fixture emitted 121,145
/// lines, which is the same as emitting none.
const QUOTE_LINES: usize = 40;

pub(super) struct GoTestRun {
    /// Tests that reported `Action:"pass"` — the only evidence that counts.
    pub passed: BTreeSet<String>,
    /// Tests that reported `Action:"fail"`.
    pub failed: BTreeSet<String>,
    /// Per-test `Output` lines, in order, for every test that did not pass.
    pub failure_log: Vec<String>,
    /// Raw interleaved stdout+stderr, for the cases `-json` cannot describe
    /// (a build error, a panic that kills the binary, a timeout).
    pub raw: String,
    pub exit_ok: bool,
    pub timed_out: bool,
}

/// Build the **anchored, closed** `-run` pattern for a set of test names.
///
/// An unanchored pattern silently pulls in every test whose name has one of
/// these as a prefix, and then the "exactly N passes" assertion would be
/// measuring a population the gate never declared. So every level is `^(…)$`.
///
/// `go test -run` splits its argument on `/` and matches element *i* against
/// name level *i*, so a SUBTEST is addressed by a multi-level pattern
/// (`^(TestX)$/^(sub)$`). G2.13a and G2.13b need that: they certify two
/// different properties that live in one Go function, and a gate that ran the
/// whole function would go red under the *other* gate's mutation — which is
/// precisely the undifferentiated failure the seven-gate split exists to
/// prevent.
///
/// The set must be of UNIFORM depth (asserted by
/// `every_pinned_set_has_uniform_depth`): a mixed-depth set would emit a level
/// whose alternation omits the shallower names, and Go would then run them
/// unfiltered.
pub(super) fn run_pattern(tests: &[&str]) -> String {
    let depth = tests.iter().map(|t| t.split('/').count()).max().unwrap_or(1);
    let mut levels: Vec<String> = Vec::new();
    for i in 0..depth {
        let mut alts: Vec<&str> = Vec::new();
        for t in tests {
            if let Some(part) = t.split('/').nth(i) {
                if !alts.contains(&part) {
                    alts.push(part);
                }
            }
        }
        if alts.is_empty() {
            // Only reachable from a mixed-depth set. `^()$` matches the empty
            // string and would filter everything out; stopping here is the
            // conservative reading, and the uniform-depth test forbids the case.
            break;
        }
        levels.push(format!("^({})$", alts.join("|")));
    }
    levels.join("/")
}

/// Every strict ancestor of a (possibly nested) test name: `A/b/c` ⇒ `A`,
/// `A/b`. Empty for a top-level name, which is why
/// [`check_run_evidence`] behaves identically for a slash-free pinned set.
fn ancestors(name: &str) -> Vec<String> {
    let parts: Vec<&str> = name.split('/').collect();
    (1..parts.len()).map(|i| parts[..i].join("/")).collect()
}

/// Run exactly `tests` under `budget`, with the three anti-vacuity flags.
pub(super) fn go_test(ctx: &Ctx, tests: &[&str], budget: Duration) -> Result<GoTestRun, String> {
    let pattern = run_pattern(tests);

    let mut cmd = Command::new("go");
    cmd.arg("test")
        .arg("-count=1") // (2) defeat the result cache
        .arg("-tags")
        .arg(ZSTD_TAG)
        .arg("-json") // (3) machine-readable per-test verdicts
        .arg("-run")
        .arg(&pattern)
        .arg(GO_PACKAGE)
        .current_dir(ctx.path(GO_MODULE_DIR))
        // H3: the child must resolve the tree under test, never the developer's.
        // `current_dir` is derived from `ctx`, and nothing here reads an
        // absolute path or an ambient `cwd`.
        .env("GOTOOLCHAIN", "local");

    let run = capped(cmd, budget).map_err(|e| format!("could not run `go test`: {e}"))?;

    let mut passed = BTreeSet::new();
    let mut failed = BTreeSet::new();
    let mut output_by_test: Vec<(String, String)> = Vec::new();

    for line in run.out.lines() {
        let Ok(v) = serde_json::from_str::<serde_json::Value>(line) else {
            continue; // non-JSON noise (a go build error, a runtime panic)
        };
        let action = v.get("Action").and_then(|a| a.as_str()).unwrap_or("");
        // A package-level event has no `Test` field; only per-test events are
        // evidence about which tests executed.
        let Some(test) = v.get("Test").and_then(|t| t.as_str()) else {
            continue;
        };
        match action {
            "pass" => {
                passed.insert(test.to_string());
            }
            "fail" => {
                failed.insert(test.to_string());
            }
            "output" => {
                if let Some(o) = v.get("Output").and_then(|o| o.as_str()) {
                    output_by_test.push((test.to_string(), o.trim_end().to_string()));
                }
            }
            _ => {}
        }
    }

    let failure_log = output_by_test
        .into_iter()
        .filter(|(t, _)| !passed.contains(t))
        .map(|(t, o)| format!("{t}: {o}"))
        .take(QUOTE_LINES)
        .collect();

    Ok(GoTestRun {
        passed,
        failed,
        failure_log,
        raw: run.out,
        exit_ok: run.ok,
        timed_out: run.timed_out,
    })
}

/// A literal construct that must appear in a pinned function's EXECUTING body.
///
/// Two jobs, and the second is why this type moved down here from
/// `gates_g2_13.rs`:
///
/// 1. **Pinning a population `go_test_names` cannot see.** A sub-test name is a
///    `t.Run` argument, sometimes built by `fmt.Sprintf`, so the only honest way
///    to pin it in SOURCE is to pin the construct that generates it.
/// 2. **Making an emptied fixture a FAILURE.** An empty Go test function emits
///    `pass`. Every anti-vacuity assertion a gate carries proves a leaf RAN; none
///    of them proves its body ASSERTS anything, so a leaf could be gutted to `{}`
///    with its gate staying green. Anchoring a gate on the leaf's own PROPERTY
///    ASSERTION closes that at the source level: delete the body, or delete the
///    assertion out of it, and the gate goes red on the next run — no recorded
///    transcript required, and nothing to keep fresh.
///
/// The needle is searched for in a body passed through [`strip_go_comments`], so
/// commenting the assertion out does not satisfy it either.
pub struct SourceAnchor {
    /// The enclosing `func Test…`.
    pub func: &'static str,
    pub needle: &'static str,
    pub why: &'static str,
}

/// Check a set of [`SourceAnchor`]s against already-enumerated function bodies.
///
/// Shared by `run_audit_gate` and by G2.9a, which is not an `AuditGate` but needs
/// the identical pin for the identical reason.
pub(super) fn check_source_anchors(
    bodies: &[EnumeratedTest],
    anchors: &[SourceAnchor],
    gate_id: &str,
    source: &str,
) -> Vec<String> {
    let mut findings = Vec::new();
    for a in anchors {
        match bodies.iter().find(|f| f.test == a.func) {
            None => findings.push(format!(
                "{gate_id}: {source} has no `func {}` to anchor against",
                a.func
            )),
            Some(f) => {
                if !f.body.contains(a.needle) {
                    findings.push(format!(
                        "{source}::{} no longer contains `{}` in EXECUTING code — {}. An empty Go \
                         test function emits `pass`, so a gate that only runs its fixtures cannot \
                         tell a body that asserts this from a body that asserts nothing.",
                        a.func, a.needle, a.why
                    ));
                }
            }
        }
    }
    findings
}

/// The `t.Run` call, as it is spelled in the corpus. Shared with
/// `gates_g2_13.rs`'s sub-test site counts so the two cannot drift.
pub(super) const T_RUN: &str = "t.Run(";

/// **The comment-stripped body of ONE pinned LEAF** — the executing text a
/// gutted leaf would lose.
///
/// # Why the granularity is the leaf and not the function
///
/// Every per-leaf rule in this crate answers one question: *would emptying THIS
/// leaf's body be noticed?* An empty Go test emits `pass`, and so does a
/// `t.Run("…", func(t *testing.T) {})`. [`enumerate_injections`] attributes text
/// to the enclosing `func Test…`, which is the right unit for a leaf that IS a
/// function and the wrong one for a leaf that is a sub-test arm: the sibling
/// arm's assertions are in the same function body, so a needle found there says
/// nothing about the arm being asked about. `audit_test.go`'s GC fixture is the
/// concrete case — the counted-skip arm carries the assertion, the abort arm
/// carries a different one, and a function-level search cannot tell them apart.
///
/// # Resolution
///
/// `Parent` → the whole function body. `Parent/rest` → the closure of
/// `t.Run("rest", …)`, where `rest` is everything after the FIRST `/` (a
/// sub-test name may itself contain `/`, and two in this corpus do). A leaf
/// whose name is GENERATED (`t.Run(fmt.Sprintf(…), …)`) has no literal to find;
/// it resolves to the function's unique non-literal `t.Run` site, and only when
/// there is exactly one — an ambiguous function returns `None` rather than a
/// guess, and the caller reports that as missing evidence.
#[allow(dead_code)] // read by `every_pinned_leaf_is_reddened_by_a_recorded_mutation`
pub(super) fn leaf_body(bodies: &[EnumeratedTest], leaf: &str) -> Option<String> {
    let (func, sub) = match leaf.split_once('/') {
        Some((f, s)) => (f, Some(s)),
        None => (leaf, None),
    };
    let body = &bodies.iter().find(|f| f.test == func)?.body;
    match sub {
        None => Some(body.clone()),
        Some(name) => subtest_closure(body, name),
    }
}

/// The closure body of one `t.Run` arm inside an already-comment-stripped
/// function body. See [`leaf_body`] for the resolution rule.
#[allow(dead_code)] // reached through `leaf_body`
fn subtest_closure(body: &str, name: &str) -> Option<String> {
    if let Some(at) = body.find(&format!("{T_RUN}\"{name}\"")) {
        return closure_at(body, at);
    }
    // A generated name. The only honest resolution is a unique generated site.
    let mut generated: Vec<usize> = Vec::new();
    let mut from = 0;
    while let Some(i) = body[from..].find(T_RUN) {
        let at = from + i;
        if !body[at + T_RUN.len()..].starts_with('"') {
            generated.push(at);
        }
        from = at + T_RUN.len();
    }
    match generated.as_slice() {
        [only] => closure_at(body, *only),
        _ => None,
    }
}

/// The brace-matched body of the function literal that starts at the first `{`
/// at or after `at`.
///
/// String, raw-string and rune literals are skipped, because a `{` inside one is
/// not a block (`strip_go_comments` keeps literals, deliberately — the needles
/// live in `t.Fatalf` format strings). Scanning is byte-wise, which is safe on
/// UTF-8: every byte of a multi-byte sequence is >= 0x80 and can never be
/// mistaken for one of the ASCII delimiters.
#[allow(dead_code)] // reached through `leaf_body`
fn closure_at(body: &str, at: usize) -> Option<String> {
    let b = body.as_bytes();
    let mut i = at;
    let mut depth = 0usize;
    let mut start = 0usize;
    while i < b.len() {
        match b[i] {
            b'"' | b'`' | b'\'' => {
                i = skip_literal(b, i);
                continue;
            }
            b'{' => {
                if depth == 0 {
                    start = i + 1;
                }
                depth += 1;
            }
            b'}' => {
                if depth == 0 {
                    return None; // malformed; a guess here would be worse
                }
                depth -= 1;
                if depth == 0 {
                    return Some(body[start..i].to_string());
                }
            }
            _ => {}
        }
        i += 1;
    }
    None
}

/// The index just past the literal that opens at `i`.
#[allow(dead_code)] // reached through `leaf_body`
fn skip_literal(b: &[u8], i: usize) -> usize {
    let quote = b[i];
    let mut j = i + 1;
    while j < b.len() {
        if quote != b'`' && b[j] == b'\\' {
            j += 2;
            continue;
        }
        if b[j] == quote {
            return j + 1;
        }
        j += 1;
    }
    b.len()
}

/// One Go source with every COMMENT removed and every string literal kept.
///
/// **Why this exists.** Every source-side pin in this file and in
/// `gates_g2_13.rs` is a substring search over raw Go text — the fired-count
/// needles below, the `t.Run(` site counts, the `SourceAnchor` needles, the
/// `errorfs.InjectorFunc(` enumeration. A substring search over raw text is
/// satisfied by a COMMENT, so every one of those pins could be held up by text
/// that no longer executes. That is not hypothetical: an adversarial audit
/// commented out `crashsim_test.go`'s fired-count guard and **G2.6 still
/// reported PASS**, because the three needles were still present in the
/// commented-out lines.
///
/// [`go_test_names`] was already strict about this (column-0 declarations only,
/// with a unit test proving a `func Test…` in a comment does not count). The
/// same rigour is applied here, once, to the text every other pin reads.
///
/// Newlines are preserved so line-oriented parsing downstream is unaffected;
/// only comment CONTENT is dropped. String and rune literals are preserved
/// verbatim, which matters because one of the needles (`fired ZERO times`)
/// lives inside a `t.Fatalf` format string.
pub(super) fn strip_go_comments(src: &str) -> String {
    #[derive(Clone, Copy, PartialEq)]
    enum S {
        Code,
        Line,
        Block,
        Str,
        Raw,
        Rune,
    }
    let mut out = String::with_capacity(src.len());
    let mut state = S::Code;
    let mut escaped = false;
    let b: Vec<char> = src.chars().collect();
    let mut i = 0;
    while i < b.len() {
        let c = b[i];
        let next = b.get(i + 1).copied();
        match state {
            S::Code => match (c, next) {
                ('/', Some('/')) => {
                    state = S::Line;
                    i += 2;
                }
                ('/', Some('*')) => {
                    state = S::Block;
                    i += 2;
                }
                _ => {
                    state = match c {
                        '"' => S::Str,
                        '`' => S::Raw,
                        '\'' => S::Rune,
                        _ => S::Code,
                    };
                    out.push(c);
                    i += 1;
                }
            },
            S::Line => {
                if c == '\n' {
                    out.push('\n');
                    state = S::Code;
                }
                i += 1;
            }
            S::Block => {
                // A newline inside a block comment is kept: dropping it would
                // splice two unrelated lines together and could manufacture a
                // match that exists in neither.
                if c == '\n' {
                    out.push('\n');
                }
                if c == '*' && next == Some('/') {
                    state = S::Code;
                    i += 2;
                } else {
                    i += 1;
                }
            }
            S::Str | S::Rune => {
                out.push(c);
                let quote = if state == S::Str { '"' } else { '\'' };
                if escaped {
                    escaped = false;
                } else if c == '\\' {
                    escaped = true;
                } else if c == quote || c == '\n' {
                    // A newline terminates an interpreted literal in Go; treat
                    // it as a close rather than swallowing the rest of the file.
                    state = S::Code;
                }
                i += 1;
            }
            S::Raw => {
                out.push(c);
                if c == '`' {
                    state = S::Code;
                }
                i += 1;
            }
        }
    }
    out
}

/// Parse the `func TestXxx(` declarations out of a Go test source.
///
/// Deliberately syntactic and deliberately strict: it reads only declarations
/// at column 0, so a `func Test…` mentioned in a comment or a string does not
/// count, and a method with a receiver does not either.
pub(super) fn go_test_names(src: &str) -> BTreeSet<String> {
    let mut out = BTreeSet::new();
    for line in src.lines() {
        let Some(rest) = line.strip_prefix("func Test") else {
            continue;
        };
        let Some(open) = rest.find('(') else { continue };
        let name = &rest[..open];
        if !name.is_empty() && name.chars().all(|c| c.is_alphanumeric() || c == '_') {
            out.insert(format!("Test{name}"));
        }
    }
    out
}

/// Assertion (1): the population that will be run is the population that was
/// declared. Returns the findings; empty means the source agrees with the pin.
///
/// `where_` names the file so a finding says which pin to update.
pub(super) fn check_pinned_population(
    declared: &BTreeSet<String>,
    pinned: &[&str],
    where_: &str,
    pin_name: &str,
) -> Vec<String> {
    let pinned: BTreeSet<String> = pinned.iter().map(|s| s.to_string()).collect();
    let mut findings = Vec::new();

    for missing in pinned.difference(declared) {
        findings.push(format!(
            "{missing} is pinned in {pin_name} but no longer declared in {where_} — \
             `go test -run` would match nothing for it and STILL EXIT 0"
        ));
    }
    for extra in declared.difference(&pinned) {
        findings.push(format!(
            "{extra} is declared in {where_} but not pinned in {pin_name} — a new test in this \
             file is either part of the property this gate certifies (record it) or it is not \
             (move it); an unrecorded test is neither run nor accounted for"
        ));
    }
    findings
}

/// Assertion (3): exactly the pinned tests reported `pass`.
///
/// A pinned SUBTEST also produces a pass event for its parent, so the ancestors
/// of the pinned set are permitted in `passed` but are not required to be there
/// — the leaves are what the gate certifies. For a slash-free pinned set the
/// ancestor set is empty and this is exactly "passed == pinned".
pub(super) fn check_run_evidence(run: &GoTestRun, pinned: &[&str]) -> Vec<String> {
    let allowed: BTreeSet<String> = pinned
        .iter()
        .flat_map(|s| {
            std::iter::once(s.to_string()).chain(ancestors(s))
        })
        .collect();
    let pinned: BTreeSet<String> = pinned.iter().map(|s| s.to_string()).collect();
    let mut findings = Vec::new();

    if run.timed_out {
        findings.push("`go test` exceeded the gate's budget and its process group was killed".into());
    }

    for missing in pinned.difference(&run.passed) {
        findings.push(format!(
            "{missing} did not report a passing `go test -json` event ({})",
            if run.failed.contains(missing) {
                "it FAILED"
            } else {
                "it did not run at all — exit status is not evidence"
            }
        ));
    }
    for extra in run.passed.difference(&allowed) {
        findings.push(format!(
            "{extra} passed but is not in the pinned set — the `-run` anchor is leaking"
        ));
    }

    if findings.is_empty() && !run.exit_ok {
        findings.push(format!(
            "every pinned test passed yet `go test` exited non-zero — the package itself failed \
             (build error, TestMain, or a panic outside a test):\n{}",
            crate::harness::layer2::tail(&run.raw, 20)
        ));
    }
    findings
}

// ---------------------------------------------------------------------------
// G2.9a — durability on ack (embedded / pebble), arms (a)–(c)
// ---------------------------------------------------------------------------

/// The crash + durability corpus this gate certifies, pinned by NAME.
///
/// Scope is arms (a)–(c) — fsync before ack, survives crash, no reorder — all
/// of which are questions about the embedded pebble commit path. Arm (d)
/// (`durability="normal"`) and arm (e) (the sqlite WAL policy) belong to
/// **G2.9b**, which stays on `pending::p3_isolation`: the engine has no
/// durability knob to test (`pebble_engine.go`'s config carries no such field
/// and the committer hard-codes `Apply(pebble.Sync)`), and sqlite is not P1
/// substrate at all. Writing them here would mean writing a gate against
/// nothing, which is the failure mode `pending.rs` was narrowed to avoid.
///
/// Every name below is a `func Test…` in `crashsim_test.go`, and the gate
/// asserts that file declares EXACTLY these — see
/// [`check_pinned_population`].
pub const G2_9A_CRASH_TESTS: &[&str] = &[
    // (b) survives crash: every acked write is in the crash clone.
    "TestCrashAckedWritesSurvive",
    // (b) + concurrency: nothing acked under concurrent writer load is lost.
    "TestCrashConcurrentNoAckedLoss",
    // (c) no reorder: what survives a crash is a PREFIX of the commit history
    //     in commitTs order — a durable commit whose predecessor is not durable
    //     is a history that never happened. The boundary is pinned at both ends
    //     by the fixture (a commit acked before the clone, one acked after), so
    //     the prefix assertion is over a run that really straddles the image.
    "TestCrashDurablePrefixNoReorder",
    // (c) the restart FLOOR — a backward wall clock cannot re-issue a commitTs,
    //     so no key ever carries two versions at one timestamp across a restart.
    //     This is monotonicity, NOT the prefix property above: every commit in
    //     it is `Apply(pebble.Sync)`ed before the next begins, so it could not
    //     observe a reorder even in principle. The two together are arm (c).
    "TestCrashHLCNoReissue",
    // (b) atomicity: a multi-write commit is all-or-nothing on disk.
    "TestCrashNoTornBatch",
    // (a) fsync before ack: an injected WAL-fsync fault must produce an errored
    //     ack — a nil ack always means durable — and the engine must seal.
    "TestInjectedFaultsReopenConsistent",
    // (a) the seal contract: a REAL injected WAL-fsync fault seals the engine,
    //     and once sealed every write path refuses loudly. The premise used to
    //     be manufactured (`e.sealed.Store(true)` under a comment reading
    //     "simulate the post-fault state"), which is why this fixture survived
    //     this gate's own falsifier — it is now driven by the fault.
    "TestSealContractRefusesWrites",
];

const G2_9A_SOURCE: &str = "runtime-go/bluedb/crashsim_test.go";

/// **One anchor per pinned fixture, on that fixture's own PROPERTY ASSERTION.**
///
/// The pinned population, `-count=1` and the per-test `pass` events prove each of
/// the seven RAN. None of them proves a body still asserts anything, and an empty
/// Go test function emits `pass` — so before these anchors existed every one of
/// the seven could have been gutted to `{}` with this gate green and its
/// falsification still recorded `PROVEN`. That is the exact defect
/// `gates_g2_13.rs`'s `LEAF_COVERAGE` closes from the transcript side; this closes
/// it from the source side, which is strictly cheaper (no artefact to keep fresh)
/// and strictly earlier (a `cargo test` failure, not a full-tier one).
///
/// The needles are the assertions themselves, not incidental scaffolding: deleting
/// the assertion out of a fixture that still runs is the same vacuity as deleting
/// the fixture, and both are findings here.
///
/// Two fixtures carry TWO anchors, because their property has two halves that no
/// single revert reddens together — the seal contract refuses Commit *and* GC, and
/// the injected-fault fixture asserts an errored ack *and* that the acked writes
/// are readable after reopen.
pub const G2_9A_ANCHORS: &[SourceAnchor] = &[
    SourceAnchor {
        func: "TestCrashAckedWritesSurvive",
        needle: "acked write missing after restart: %d/%d acked writes absent from the crash clone",
        why: "that IS the acked⇒survives assertion (§7 invariant 1)",
    },
    SourceAnchor {
        func: "TestCrashNoTornBatch",
        needle: "torn batch: %d/100 writes recovered (must be all-or-nothing; acked ⇒ all)",
        why: "that IS the all-or-nothing assertion (§7 invariant 2)",
    },
    SourceAnchor {
        func: "TestCrashHLCNoReissue",
        needle: "restart floor violated: hi=%+v next=%+v (must be strictly greater despite backward clock)",
        why: "that IS the restart-floor assertion (§7 invariant 3) — a backward wall clock must not \
              re-issue a commitTs",
    },
    SourceAnchor {
        func: "TestSealContractRefusesWrites",
        needle: "sealed engine must refuse Commit with ErrSealed, got %v",
        why: "that IS the write half of the seal contract",
    },
    SourceAnchor {
        func: "TestSealContractRefusesWrites",
        needle: "sealed engine must refuse GC with ErrSealed, got %v",
        why: "that IS the GC half of the seal contract — `every write path` includes the one that \
              deletes",
    },
    SourceAnchor {
        func: "TestCrashDurablePrefixNoReorder",
        needle: "durable prefix has a HOLE: %q@%+v survived the crash while %q@%+v — acked ",
        why: "that IS the no-reorder assertion, and it is the ONLY thing in this gate that tests \
              arm (c)'s prefix clause; see LEAF_COVERAGE for why no recorded mutation reaches it",
    },
    SourceAnchor {
        func: "TestInjectedFaultsReopenConsistent",
        needle: "armed WAL-fsync fault produced NO errored ack",
        why: "that IS the fail-stop half of arm (a) — a nil ack must mean durable",
    },
    SourceAnchor {
        func: "TestInjectedFaultsReopenConsistent",
        needle: "ABSENT after reopen — acked⇒durable violated",
        why: "that IS the reopen half: every commit that acked nil before the fault must still be \
              readable from the store the fault left behind",
    },
    SourceAnchor {
        func: "TestCrashConcurrentNoAckedLoss",
        needle: "nil-acked concurrent writes unreadable at the recovered high-water",
        why: "that IS the concurrent-load arm of acked⇒survives",
    },
];

pub fn g2_9a_durability_on_ack(ctx: &Ctx) -> GateOutcome {
    let Some(src) = ctx.read(G2_9A_SOURCE) else {
        return GateOutcome::fail(
            format!("cannot read {G2_9A_SOURCE}"),
            vec!["the crash corpus is the gate's subject; without it there is nothing to certify".into()],
        );
    };

    // ── (1) the population is pinned, not discovered ──
    let declared = go_test_names(&src);
    let mut findings = check_pinned_population(
        &declared,
        G2_9A_CRASH_TESTS,
        G2_9A_SOURCE,
        "G2_9A_CRASH_TESTS (bluedb_gates/gates_g2.rs)",
    );

    // ── (1b) each pinned fixture still CARRIES its property assertion ──
    // Without this the seven anti-vacuity assertions above prove only that seven
    // functions ran; `func TestCrashAckedWritesSurvive(t *testing.T) {}` runs and
    // passes. See [`G2_9A_ANCHORS`].
    findings.extend(check_source_anchors(
        &enumerate_injections(&src),
        G2_9A_ANCHORS,
        "G2.9a",
        G2_9A_SOURCE,
    ));

    if !findings.is_empty() {
        return GateOutcome::fail(
            format!(
                "the crash corpus does not match its pinned population, or a pinned fixture no \
                 longer carries its property assertion ({} declared, {} pinned, {} anchor(s))",
                declared.len(),
                G2_9A_CRASH_TESTS.len(),
                G2_9A_ANCHORS.len()
            ),
            findings,
        );
    }

    // ── (2) + (3) run them, with the cache defeated and per-test evidence ──
    // 840s of the gate's 900s budget: the remainder covers this body's own
    // parsing and leaves headroom for `capped` to kill the group and reap.
    let run = match go_test(ctx, G2_9A_CRASH_TESTS, Duration::from_secs(840)) {
        Ok(r) => r,
        Err(e) => return GateOutcome::fail(e, vec!["a gate that cannot run has not passed".into()]),
    };

    findings.extend(check_run_evidence(&run, G2_9A_CRASH_TESTS));
    findings.extend(run.failure_log.iter().cloned());

    if findings.is_empty() {
        GateOutcome::pass(format!(
            "acked ⇒ durable: {} crash/durability tests pinned in source and observed passing via \
             `go test -json -count=1` (arms a–c, embedded/pebble)",
            G2_9A_CRASH_TESTS.len()
        ))
    } else {
        GateOutcome::fail(
            format!(
                "durability on ack is not proven: {}/{} pinned crash tests reported a passing event",
                run.passed.len(),
                G2_9A_CRASH_TESTS.len()
            ),
            findings,
        )
    }
}

// ---------------------------------------------------------------------------
// G2.6 — the substrate crash corpus (errorfs injection manifest)
// ---------------------------------------------------------------------------

/// One recorded fault-injection site.
///
/// The unit is `(file, test)` and NOT merely "a test that mentions errorfs",
/// because the distinction this gate is built on is the one the plan draws:
/// `vfs.NewCrashableMem` + `CrashClone` is crash **simulation** — it replays a
/// filesystem truncated at the last sync — while `errorfs` is fault
/// **injection**: it makes a named operation fail while the process keeps
/// running. Only the second can reach an error path. G2.9a's corpus is mostly
/// the first; this gate quantifies over the second, which is why the two gates
/// overlap in exactly one test and no more.
pub struct Injection {
    /// Repo-relative path to the Go test source.
    pub file: &'static str,
    /// The enclosing `func Test…`.
    pub test: &'static str,
    /// How many `errorfs.InjectorFunc(` constructions this test contains. A set
    /// comparison alone cannot see the second injector of a two-injector test
    /// being deleted; the count can.
    pub sites: usize,
    /// What the injector makes fail. Prose, rendered into the gate's detail so
    /// STATUS.md says what the corpus actually covers.
    pub fault: &'static str,
}

/// THE RECORDED MANIFEST.
///
/// Recorded here, in the gate, rather than in a doc: this is the pin the gate
/// compares against, and a pin that lives anywhere the gate does not read is a
/// pin that can drift silently. It is rendered into the gate's PASS detail and
/// therefore into `STATUS.md`, so the corpus is legible without reading Rust.
///
/// Recorded AFTER C8, as the plan requires: C8 added the MANIFEST-write
/// injector, so a manifest taken before it would have pinned a corpus with a
/// hole in it and then defended the hole.
///
/// Provenance of each row — the prior art had exactly ONE real injection site
/// (`TestInjectedFaultsReopenConsistent`); the other three are net-new in this
/// branch's C3 and C8:
pub const INJECTION_MANIFEST: &[Injection] = &[
    Injection {
        file: "runtime-go/bluedb/audit_test.go",
        test: "TestAuditH3ReaderGetSurfacesIoErrors",
        sites: 1,
        fault: "read/read-at of any *.sst — the reader must surface it as an error, not as absence (C3)",
    },
    Injection {
        file: "runtime-go/bluedb/audit_test.go",
        test: "TestAuditN3BackgroundFatalDoesNotKillTheProcess",
        sites: 1,
        fault: "write/sync of MANIFEST-* on a flush goroutine — a background pebble fatal must latch, not kill the process (C8)",
    },
    Injection {
        file: "runtime-go/bluedb/audit_test.go",
        test: "TestAuditN3SynchronousWalFaultStillErrorsTheAck",
        sites: 1,
        fault: "sync of the *.log WAL inside Apply — the fatal must be folded into the ack (C8)",
    },
    Injection {
        file: "runtime-go/bluedb/audit_test.go",
        test: "TestAuditH3ScanSurfacesIoErrorsAtTheCommitBoundary",
        sites: 1,
        fault: "read/read-at of any *.sst under a SCAN — the failed scan must reach the commit \
                boundary, not read as an empty collection (H3b)",
    },
    Injection {
        file: "runtime-go/bluedb/crashsim_test.go",
        test: "TestInjectedFaultsReopenConsistent",
        sites: 1,
        fault: "sync of the *.log WAL — the fail-stop durability regression, the ONE injection site inherited from the prior art",
    },
    Injection {
        file: "runtime-go/bluedb/crashsim_test.go",
        test: "TestSealContractRefusesWrites",
        sites: 1,
        fault: "sync of the *.log WAL — the fault that must SEAL the engine, so the seal contract stands on a real durability fault instead of a hand-set `sealed` flag",
    },
];

/// The substring each injection fixture must carry to prove the fault was
/// REACHED rather than merely armed.
///
/// This is the C3 lesson made structural. That commit's first H3 fixture passed
/// against the UNFIXED reader: a single row makes a one-block SSTable, `Open`'s
/// own meta reads pulled that block into the fresh cache, and by the time the
/// armed `Get` ran **zero filesystem operations occurred**. The injector was
/// armed at a door nobody walked through, and nothing in the test could tell.
///
/// It was caught by instrumenting the injector to COUNT, so counting is what is
/// required here. Two halves, both necessary: the counter must be incremented
/// where the fault is returned, and the test must FAIL at zero. Requiring only
/// the increment would accept a fixture that counts and never looks.
///
/// All three are searched for in a body that has been passed through
/// [`strip_go_comments`], because the rule is about code that RUNS. Searching
/// raw text made the rule satisfiable by a comment — commenting the guard out
/// of `crashsim_test.go` left G2.6 green, which is the same shape of vacuity
/// the rule exists to forbid, one level up.
const FIRED_COUNTER_INCREMENT: &str = "injected.Add(1)";
const FIRED_COUNTER_READ: &str = "injected.Load()";
const FIRED_ZERO_GUARD: &str = "fired ZERO times";

/// The three needles as ONE list, with the finding each one's absence produces.
///
/// It is a `const` rather than an inline array in the gate body because
/// `gates_g2_13::SOURCE_SIDE_FALSIFIERS` records G2.6's five fixtures as falsified
/// by this pin, and a claim about what a gate enforces must read the gate's own
/// list. Two copies would let the claim outlive the check.
pub(super) const G2_6_FIXTURE_PINS: &[(&str, &str)] = &[
    (
        FIRED_COUNTER_INCREMENT,
        "the injector does not count its invocations",
    ),
    (FIRED_COUNTER_READ, "nothing reads the invocation count"),
    (
        FIRED_ZERO_GUARD,
        "there is no assertion that the count is non-zero",
    ),
];

/// The construction the enumerator keys on.
const INJECTOR_CTOR: &str = "errorfs.InjectorFunc(";

#[derive(Debug, PartialEq, Eq)]
pub(super) struct EnumeratedTest {
    pub test: String,
    pub sites: usize,
    pub body: String,
}

/// Enumerate every `errorfs` injection site in one Go source, attributed to the
/// top-level function that contains it.
///
/// Attribution matters: an injector constructed outside a test (a shared helper,
/// a `var` at package scope) is not a site this gate can run, and counting it
/// would let the manifest be satisfied by a construction no test executes.
///
/// The source is passed through [`strip_go_comments`] FIRST, so the bodies this
/// returns — which every source-side pin in this file and in `gates_g2_13.rs`
/// then greps — contain only text that executes. Commented-out code is text
/// that does not, and a pin a comment can satisfy is not a pin.
pub(super) fn enumerate_injections(src: &str) -> Vec<EnumeratedTest> {
    let mut out: Vec<EnumeratedTest> = Vec::new();
    let src = strip_go_comments(src);

    for line in src.lines() {
        if let Some(rest) = line.strip_prefix("func ") {
            // `func (r *recv) Name(` is a method, not a top-level function; the
            // name we want is the one before the first `(` in either spelling.
            let test = match rest.strip_prefix('(') {
                Some(after_recv) => after_recv
                    .split_once(") ")
                    .map(|(_, m)| m.split('(').next().unwrap_or("").to_string())
                    .unwrap_or_default(),
                None => rest.split('(').next().unwrap_or("").to_string(),
            };
            out.push(EnumeratedTest {
                test,
                sites: 0,
                body: String::new(),
            });
        }
        if let Some(f) = out.last_mut() {
            f.body.push_str(line);
            f.body.push('\n');
            if line.contains(INJECTOR_CTOR) {
                f.sites += 1;
            }
        }
    }
    out.retain(|f| !f.test.is_empty());
    out
}

/// Where the corpus lives. The sweep DISCOVERS `*_test.go` here rather than
/// reading a list, because a list is a place for a new file carrying a new
/// injection site to hide. The discovered set is then cross-checked against
/// [`G2_6_SOURCES`], so discovery cannot silently shrink either.
const G2_6_TEST_DIR: &str = "runtime-go/bluedb";

/// The corpus files as recorded. Two-way check: a file here that is gone is a
/// FAIL (the sweep quietly stopped covering it), and a file on disk that is not
/// here is a FAIL (the sweep is covering something nobody accounted for).
const G2_6_SOURCES: &[&str] = &[
    "audit_test.go",
    "bench_test.go",
    "comparer_property_test.go",
    "comparer_test.go",
    "crashsim_test.go",
    "engine_test.go",
    "gc_test.go",
    "keys_test.go",
    "lock_test.go",
    "stage2_readset_test.go",
];

pub fn g2_6_injection_manifest(ctx: &Ctx) -> GateOutcome {
    let mut findings = Vec::new();

    // ── Discover the corpus, and reconcile it with the recorded list ──
    let mut on_disk: BTreeSet<String> = BTreeSet::new();
    match std::fs::read_dir(ctx.path(G2_6_TEST_DIR)) {
        Err(e) => {
            return GateOutcome::fail(
                format!("cannot read {G2_6_TEST_DIR}: {e}"),
                vec!["the crash corpus is this gate's subject; without it there is nothing to enumerate".into()],
            )
        }
        Ok(rd) => {
            for entry in rd.flatten() {
                let name = entry.file_name().to_string_lossy().to_string();
                if name.ends_with("_test.go") {
                    on_disk.insert(name);
                }
            }
        }
    }
    let recorded: BTreeSet<String> = G2_6_SOURCES.iter().map(|s| s.to_string()).collect();
    for gone in recorded.difference(&on_disk) {
        findings.push(format!(
            "{G2_6_TEST_DIR}/{gone} is recorded in G2_6_SOURCES but is not on disk — the sweep would \
             silently stop covering it"
        ));
    }
    for new in on_disk.difference(&recorded) {
        findings.push(format!(
            "{G2_6_TEST_DIR}/{new} is on disk but not recorded in G2_6_SOURCES — a new test file is \
             where a new injection site hides from a manifest"
        ));
    }

    // Read the WHOLE corpus, not just the manifest's files: an injection site
    // added in a file nobody thought to look at is exactly the unrecorded site
    // this gate exists to notice.
    let mut enumerated: Vec<(String, EnumeratedTest)> = Vec::new();
    for name in on_disk.union(&recorded) {
        let rel = format!("{G2_6_TEST_DIR}/{name}");
        let Some(src) = ctx.read(&rel) else { continue };
        for t in enumerate_injections(&src) {
            if t.sites > 0 {
                enumerated.push((rel.clone(), t));
            }
        }
    }

    let found_total: usize = enumerated.iter().map(|(_, t)| t.sites).sum();
    let recorded_total: usize = INJECTION_MANIFEST.iter().map(|m| m.sites).sum();

    // ── Manifest comparison ──
    for m in INJECTION_MANIFEST {
        match enumerated
            .iter()
            .find(|(f, t)| f == m.file && t.test == m.test)
        {
            None => findings.push(format!(
                "{}::{} is in the recorded manifest but constructs no {INJECTOR_CTOR} — the fault it \
                 injects ({}) is no longer exercised anywhere",
                m.file, m.test, m.fault
            )),
            Some((_, t)) => {
                if t.sites != m.sites {
                    findings.push(format!(
                        "{}::{} constructs {} injector(s), the manifest records {}",
                        m.file, m.test, t.sites, m.sites
                    ));
                }
                // ── The C3 rule: the fixture must prove it INJECTED. ──
                for (needle, why) in G2_6_FIXTURE_PINS.iter().copied() {
                    if !t.body.contains(needle) {
                        findings.push(format!(
                            "{}::{} is missing `{needle}` in EXECUTING code — {why}. An injection \
                             fixture that cannot prove it injected is indistinguishable from one \
                             that passed because nothing happened (C3: the first H3 fixture passed \
                             against the UNFIXED reader because caching meant ZERO filesystem \
                             operations occurred). Note the search is comment-blind: a guard that \
                             is present but COMMENTED OUT is text, not an assertion.",
                            m.file, m.test
                        ));
                    }
                }
            }
        }
    }
    for (f, t) in &enumerated {
        if !INJECTION_MANIFEST
            .iter()
            .any(|m| m.file == f && m.test == t.test)
        {
            findings.push(format!(
                "{f}::{} constructs {} injector(s) but is not in the recorded manifest — record it \
                 (with its fault and its fired-count assertion) or remove it; an unrecorded \
                 injection site is neither run by this gate nor accounted for by it",
                t.test, t.sites
            ));
        }
    }

    if !findings.is_empty() {
        // The declared assertion, worded for the case the mutation produces. The
        // other direction gets its own wording so the two cannot be confused —
        // `mutations.rs` matches on this string, and a message that fired for
        // "more sites" as well would prove the wrong thing.
        let detail = if found_total < recorded_total {
            format!(
                "fewer injection sites than the recorded manifest: {found_total} enumerated across \
                 {} corpus file(s), {recorded_total} recorded",
                on_disk.len()
            )
        } else if found_total > recorded_total {
            format!(
                "more injection sites than the recorded manifest: {found_total} enumerated, {recorded_total} recorded"
            )
        } else {
            format!("the injection corpus does not match the recorded manifest ({found_total} site(s) on both sides, attributed differently)")
        };
        return GateOutcome::fail(detail, findings);
    }

    // ── Run them, under the same three anti-vacuity assertions as G2.9a ──
    let tests: Vec<&str> = INJECTION_MANIFEST.iter().map(|m| m.test).collect();
    // Every manifest test must also be a real `func Test…` declaration, so the
    // `-run` anchor below is known to name something before it is used.
    for m in INJECTION_MANIFEST {
        let declared = ctx.read(m.file).map(|s| go_test_names(&s)).unwrap_or_default();
        if !declared.contains(m.test) {
            findings.push(format!(
                "{}::{} is recorded in the manifest but is not a `func Test…` declaration in that \
                 file — `go test -run` would match nothing for it and STILL EXIT 0",
                m.file, m.test
            ));
        }
    }
    if !findings.is_empty() {
        return GateOutcome::fail(
            "fewer injection sites than the recorded manifest: a recorded site names no runnable test"
                .to_string(),
            findings,
        );
    }

    // 1700s of the gate's 1800s budget. The MANIFEST fixture alone waits up to
    // 30s for a background fatal to latch and then up to 5s for a Close that is
    // expected never to return.
    let run = match go_test(ctx, &tests, Duration::from_secs(1700)) {
        Ok(r) => r,
        Err(e) => return GateOutcome::fail(e, vec!["a gate that cannot run has not passed".into()]),
    };

    findings.extend(check_run_evidence(&run, &tests));
    findings.extend(run.failure_log.iter().cloned());

    if findings.is_empty() {
        let covered: Vec<String> = INJECTION_MANIFEST
            .iter()
            .map(|m| format!("{} [{}]", m.test, m.fault))
            .collect();
        GateOutcome::pass(format!(
            "{recorded_total} errorfs injection site(s) enumerated across {} corpus file(s), all recorded, \
             all asserting a non-zero fired count, all observed passing via `go test -json -count=1`: {}",
            on_disk.len(),
            covered.join("; ")
        ))
    } else {
        GateOutcome::fail(
            format!(
                "the injection corpus did not run clean: {}/{} recorded fixtures reported a passing event",
                run.passed.len(),
                tests.len()
            ),
            findings,
        )
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn go_test_names_reads_declarations_and_nothing_else() {
        let src = "\
package bluedb\n\
// func TestInAComment(t *testing.T) — must not count\n\
const s = \"func TestInAString(\"\n\
func TestReal(t *testing.T) {}\n\
func (e *engine) TestMethod(t *testing.T) {}\n\
func BenchmarkThing(b *testing.B) {}\n\
func TestOther(t *testing.T) {\n}\n";
        let got = go_test_names(src);
        assert_eq!(
            got,
            ["TestOther", "TestReal"]
                .iter()
                .map(|s| s.to_string())
                .collect::<BTreeSet<_>>()
        );
    }

    /// The whole point of assertion (1): a DELETED test must be a finding, not
    /// a smaller green run. `go test -run` on a name that matches nothing exits
    /// 0, so without this the gate would pass having certified less.
    #[test]
    fn a_deleted_test_is_a_finding_not_a_smaller_green_run() {
        let declared: BTreeSet<String> = ["TestA"].iter().map(|s| s.to_string()).collect();
        let f = check_pinned_population(&declared, &["TestA", "TestB"], "x_test.go", "PIN");
        assert_eq!(f.len(), 1);
        assert!(f[0].contains("TestB"), "{f:?}");
        assert!(f[0].contains("STILL EXIT 0"), "{f:?}");
    }

    #[test]
    fn an_unrecorded_new_test_is_also_a_finding() {
        let declared: BTreeSet<String> =
            ["TestA", "TestNew"].iter().map(|s| s.to_string()).collect();
        let f = check_pinned_population(&declared, &["TestA"], "x_test.go", "PIN");
        assert_eq!(f.len(), 1);
        assert!(f[0].contains("TestNew"), "{f:?}");
    }

    fn run_with(passed: &[&str], failed: &[&str], exit_ok: bool) -> GoTestRun {
        GoTestRun {
            passed: passed.iter().map(|s| s.to_string()).collect(),
            failed: failed.iter().map(|s| s.to_string()).collect(),
            failure_log: vec![],
            raw: String::new(),
            exit_ok,
            timed_out: false,
        }
    }

    /// The reproduced defect, asserted directly: a run in which NOTHING
    /// executed but the process exited 0 must be RED.
    #[test]
    fn exit_zero_with_no_passing_events_is_red() {
        let f = check_run_evidence(&run_with(&[], &[], true), &["TestA", "TestB"]);
        assert_eq!(f.len(), 2, "{f:?}");
        assert!(
            f.iter().all(|s| s.contains("exit status is not evidence")),
            "{f:?}"
        );
    }

    #[test]
    fn a_top_level_set_builds_the_pattern_it_always_did() {
        assert_eq!(run_pattern(&["TestA", "TestB"]), "^(TestA|TestB)$");
        assert_eq!(run_pattern(&["TestA"]), "^(TestA)$");
    }

    /// The mechanism G2.13a and G2.13b are built on: two properties in ONE Go
    /// function, addressed separately, so neither goes red under the other's
    /// mutation.
    #[test]
    fn a_subtest_set_builds_one_anchored_level_per_depth() {
        assert_eq!(
            run_pattern(&["TestX/sub1", "TestX/sub2"]),
            "^(TestX)$/^(sub1|sub2)$"
        );
        assert_eq!(
            run_pattern(&["TestX/N1b/failed-scan"]),
            "^(TestX)$/^(N1b)$/^(failed-scan)$"
        );
    }

    #[test]
    fn ancestors_are_the_strict_prefixes_and_a_top_level_name_has_none() {
        assert!(ancestors("TestA").is_empty());
        assert_eq!(ancestors("A/b/c"), vec!["A".to_string(), "A/b".to_string()]);
    }

    /// The parent's own pass event is evidence about the parent, not a leak —
    /// but a SIBLING subtest passing still is. Without the second half the
    /// N1/N1b split would be undetectable.
    #[test]
    fn a_pinned_subtests_parent_may_pass_but_a_sibling_may_not() {
        assert!(check_run_evidence(
            &run_with(&["TestX", "TestX/sub1"], &[], true),
            &["TestX/sub1"]
        )
        .is_empty());
        let f = check_run_evidence(
            &run_with(&["TestX", "TestX/sub1", "TestX/other"], &[], true),
            &["TestX/sub1"],
        );
        assert_eq!(f.len(), 1, "{f:?}");
        assert!(f[0].contains("TestX/other") && f[0].contains("leaking"), "{f:?}");
    }

    #[test]
    fn a_leaking_run_anchor_is_red() {
        let f = check_run_evidence(&run_with(&["TestA", "TestAExtra"], &[], true), &["TestA"]);
        assert_eq!(f.len(), 1, "{f:?}");
        assert!(f[0].contains("leaking"), "{f:?}");
    }

    #[test]
    fn the_full_pinned_set_passing_is_green() {
        assert!(check_run_evidence(&run_with(&["TestA", "TestB"], &[], true), &["TestA", "TestB"]).is_empty());
    }

    // -- G2.6 ----------------------------------------------------------------

    #[test]
    fn injections_are_attributed_to_the_enclosing_test() {
        let src = "\
package bluedb\n\
func helper() {\n\
\tinj := errorfs.InjectorFunc(func(op errorfs.Op) error { return nil })\n\
}\n\
func TestOne(t *testing.T) {\n\
\tinj := errorfs.InjectorFunc(func(op errorfs.Op) error { return nil })\n\
}\n\
func TestNoInjection(t *testing.T) {\n\
\tfs := vfs.NewMem()\n\
}\n";
        let got = enumerate_injections(src);
        let with: Vec<(&str, usize)> = got
            .iter()
            .filter(|t| t.sites > 0)
            .map(|t| (t.test.as_str(), t.sites))
            .collect();
        // The helper's site is enumerated and attributed to `helper` — which is
        // what makes it visible as an UNRECORDED site rather than silently
        // satisfying a manifest row for a test that no longer injects.
        assert_eq!(with, vec![("helper", 1), ("TestOne", 1)]);
    }

    /// Comment content is dropped; string and rune literals — where one of the
    /// three fired-count needles actually lives — are not.
    #[test]
    fn strip_go_comments_drops_comments_and_keeps_literals() {
        let src = "\
a := 1 // injected.Add(1)\n\
/* injected.Load()\n\
   still a comment */ b := 2\n\
s := \"a // not a comment ‖ fired ZERO times\"\n\
r := `raw // kept`\n\
c := '\\'' // trailing\n";
        let got = strip_go_comments(src);
        assert!(!got.contains("injected.Add(1)"), "{got:?}");
        assert!(!got.contains("injected.Load()"), "{got:?}");
        assert!(!got.contains("still a comment"), "{got:?}");
        assert!(got.contains("fired ZERO times"), "{got:?}");
        assert!(got.contains("a // not a comment"), "{got:?}");
        assert!(got.contains("raw // kept"), "{got:?}");
        assert!(got.contains("b := 2"), "{got:?}");
        assert!(got.contains("c := '\\''"), "{got:?}");
        // Line structure survives, so the line-oriented parsers above are
        // unaffected by the rewrite.
        assert_eq!(got.lines().count(), src.lines().count(), "{got:?}");
    }

    /// **The reproduced defect.** An adversarial audit commented out
    /// `crashsim_test.go`'s fired-count guard and G2.6 still reported PASS: the
    /// three needles were searched for in RAW source, and commented-out text
    /// contains them just as well as executing text does.
    ///
    /// `go_test_names` was already strict about exactly this — its own unit test
    /// asserts a `func Test…` in a comment does not count — and the rigour was
    /// simply not carried across to the needles. This asserts it now.
    #[test]
    fn a_commented_out_fired_count_guard_does_not_satisfy_the_pin() {
        let armed = "\
func TestArmed(t *testing.T) {\n\
\tinj := errorfs.InjectorFunc(func(op errorfs.Op) error {\n\
\t\tinjected.Add(1)\n\
\t\treturn errorfs.ErrInjected\n\
\t})\n\
\tif n := injected.Load(); n == 0 {\n\
\t\tt.Fatalf(\"the injector fired ZERO times\")\n\
\t}\n\
}\n";
        // The mutation the audit performed: the guard is still THERE, in the
        // source, as text — it just no longer runs.
        let disarmed = "\
func TestArmed(t *testing.T) {\n\
\tinj := errorfs.InjectorFunc(func(op errorfs.Op) error {\n\
\t\tinjected.Add(1)\n\
\t\treturn errorfs.ErrInjected\n\
\t})\n\
\t// if n := injected.Load(); n == 0 {\n\
\t// \tt.Fatalf(\"the injector fired ZERO times\")\n\
\t// }\n\
}\n";
        let body_of = |src: &str| {
            enumerate_injections(src)
                .into_iter()
                .find(|t| t.test == "TestArmed")
                .expect("TestArmed")
                .body
        };
        let armed = body_of(armed);
        for needle in [FIRED_COUNTER_INCREMENT, FIRED_COUNTER_READ, FIRED_ZERO_GUARD] {
            assert!(armed.contains(needle), "armed fixture is missing `{needle}`");
        }
        let disarmed = body_of(disarmed);
        assert!(
            disarmed.contains(FIRED_COUNTER_INCREMENT),
            "the injector still counts, so that half must still be satisfied"
        );
        for needle in [FIRED_COUNTER_READ, FIRED_ZERO_GUARD] {
            assert!(
                !disarmed.contains(needle),
                "`{needle}` is only present in a COMMENT, and a commented-out guard asserts \
                 nothing — G2.6 must report the fixture as unable to prove it injected"
            );
        }
    }

    #[test]
    fn two_injectors_in_one_test_are_counted_separately() {
        let src = "\
func TestTwo(t *testing.T) {\n\
\ta := errorfs.InjectorFunc(f)\n\
\tb := errorfs.InjectorFunc(g)\n\
}\n";
        let got = enumerate_injections(src);
        assert_eq!(got.len(), 1);
        assert_eq!(got[0].sites, 2);
    }

    /// The shipped manifest must describe the shipped corpus. This runs from
    /// the crate's own source tree (not a `Ctx`), so a manifest that drifts
    /// fails `cargo test` as well as the gate.
    #[test]
    fn the_recorded_manifest_matches_the_corpus_on_disk() {
        let repo = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../../..")
            .canonicalize()
            .expect("repo root");
        let mut found: Vec<(String, String, usize)> = Vec::new();
        for entry in std::fs::read_dir(repo.join(G2_6_TEST_DIR)).expect("corpus dir").flatten() {
            let name = entry.file_name().to_string_lossy().to_string();
            if !name.ends_with("_test.go") {
                continue;
            }
            let src = std::fs::read_to_string(entry.path()).expect("read");
            for t in enumerate_injections(&src) {
                if t.sites > 0 {
                    found.push((format!("{G2_6_TEST_DIR}/{name}"), t.test.clone(), t.sites));
                }
            }
        }
        found.sort();
        let mut want: Vec<(String, String, usize)> = INJECTION_MANIFEST
            .iter()
            .map(|m| (m.file.to_string(), m.test.to_string(), m.sites))
            .collect();
        want.sort();
        assert_eq!(found, want, "INJECTION_MANIFEST has drifted from runtime-go/bluedb");
    }

    /// Every recorded fixture must carry the C3 fired-count assertion. Checked
    /// here as well as in the gate so the property is a build-time fact, not
    /// only a full-tier one.
    #[test]
    fn every_recorded_fixture_proves_its_injector_fired() {
        let repo = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../../..")
            .canonicalize()
            .expect("repo root");
        for m in INJECTION_MANIFEST {
            let src = std::fs::read_to_string(repo.join(m.file)).expect("read");
            let body = enumerate_injections(&src)
                .into_iter()
                .find(|t| t.test == m.test)
                .unwrap_or_else(|| panic!("{} not found in {}", m.test, m.file))
                .body;
            for needle in [FIRED_COUNTER_INCREMENT, FIRED_COUNTER_READ, FIRED_ZERO_GUARD] {
                assert!(
                    body.contains(needle),
                    "{}::{} is missing `{needle}`",
                    m.file,
                    m.test
                );
            }
        }
    }

    /// The declared `expect` string must be reachable from the body, and it
    /// must be the LOW branch only. If "fewer" also fired for "more", the
    /// mutation would prove the wrong direction.
    #[test]
    fn the_declared_expect_string_is_the_gate_s_own_wording() {
        let g = super::super::registry::find("G2.6").expect("G2.6 is registered");
        let expect = g.mutations.as_slice()[0].expect;
        let src = include_str!("gates_g2.rs");
        assert!(
            src.contains(expect),
            "G2.6 declares `{expect}`, which appears nowhere in the body that must emit it"
        );
    }

    /// G2.9b must stay pending: this gate covers arms (a)–(c) only, and the
    /// separation is what stops arm (d)/(e) being quietly folded into a green
    /// G2.9a. If someone re-points G2.9b at this body, this test says no.
    #[test]
    fn g2_9b_is_a_separate_gate_and_stays_pending() {
        let b = super::super::registry::find("G2.9b").expect("G2.9b is registered");
        assert_eq!(
            b.run as usize,
            super::super::pending::p3_isolation as usize,
            "G2.9b's arms (d) durability=normal and (e) sqlite WAL are P3 substrate; \
             they are not certified by G2.9a's crash corpus"
        );
    }
}

//! A live test may not decide on its own to do nothing.
//!
//! # What this is for
//!
//! Fourteen tests in `crates/sky/src/db_shared/live_tests.rs` cover the shared
//! PostgreSQL cluster's security boundary — "app A's credentials cannot reach
//! app B's database" among them. Each ended its environment probe with
//! `eprintln!(…); return;`. libtest captures `eprintln!` and prints the capture
//! only for tests that FAILED, so with a cluster and without one they printed
//! the same verdict line, differing in the wall clock and nothing else:
//!
//! ```text
//! test result: ok. 14 passed; 0 failed; 0 ignored; 0 measured; 167 filtered out; finished in 0.02s
//! test result: ok. 14 passed; 0 failed; 0 ignored; 0 measured; 167 filtered out; finished in 18.29s
//! ```
//!
//! `test-rest` is the only CI job that runs `cargo test --workspace` and it
//! installed no PostgreSQL, so the top line is what CI saw, every time.
//!
//! Four files had already worked this out and each grown its own `fn skip()`
//! writing to real stderr — the same six lines, four times, with one of them
//! carrying the comment "`integration-postgres` was green for months while
//! every live supervisor test in it skipped". That is the lesson learnt and
//! applied by hand, in four places, while fourteen other tests did not have it.
//! A convention that has to be remembered is not a mechanism.
//!
//! # The rules
//!
//! Each is a shape that has actually shipped in this repository, not a
//! hypothetical. They are deliberately about SHAPE rather than intent: a rule
//! that needs a judgement call is one somebody talks their way past.
//!
//! # What this does NOT catch
//!
//! A test that probes for an environment with a helper whose name is not in
//! [`PROBES`], and returns without printing anything at all, is invisible here —
//! there is nothing in the text to see. [`PROBES`] is therefore asserted to be
//! non-empty AND every entry asserted to still exist in the tree, so the list
//! rots loudly rather than quietly. Extending it is a one-line diff when the
//! next probe is written.

use std::path::{Path, PathBuf};

/// The one file allowed to define the skip mechanism.
const GATE_FILE: &str = "crates/sky/src/live_gate.rs";

/// This file. Every rule below has to name the shapes it forbids, so scanning
/// itself makes it report itself — a gate that fails on its own text is a gate
/// somebody deletes.
const SELF: &str = "crates/xtask/tests/live_tests_are_not_silently_skipped.rs";

/// Names by which a test asks whether a live environment is present. A test
/// body mentioning one of these is gating on the environment, and must do it
/// through the live gate.
const PROBES: &[&str] = &[
    "discover_pg_bins()",
    "find_pg_bin()",
    "postgres_is_discoverable(",
    "have_go()",
    "go_on_path()",
    "tool_on_path(",
];

/// A test body mentioning a probe satisfies the rule by mentioning one of
/// these: the gate itself, or a fixture constructor in the same file that is
/// gated internally.
const GATED_BY: &[&str] = &[
    "required(",
    "live_gate::",
    // `Fixture::new` / `fixture` in db_run_cluster_flow.rs and
    // db_cluster_flow.rs are deliberately NOT here: they return `Option` and
    // their callers do the gating, which rule 3 already sees directly. Listing
    // them would accept a call to a helper that gates nowhere.
    "provision_fixture(",
];

/// `Need::Network` is the one need the gate never enforces — an upstream that
/// is down is not a defect in this repository. That carve-out is exactly the
/// shape a future silent skip would be written in, so the sites that may use it
/// are listed rather than left to judgement. A third one is a reviewable diff.
const NETWORK_SITES: &[&str] = &["crates/sky/tests/cli_verb_flow.rs", "crates/sky/tests/ffi_verb_flow.rs"];

/// Where each delegating spelling in [`GATED_BY`] is proved to gate. See
/// `every_helper_that_satisfies_the_rule_by_delegation_gates_for_real`.
const DELEGATES: &[(&str, &str)] = &[("provision_fixture", "crates/sky/src/db_shared/live_tests.rs")];

/// The live gate's own contract test. It names every `Need` on purpose, so it
/// is exempt from the rules ABOUT `Need` — it is the thing that proves them.
const GATE_CONTRACT: &str = "crates/sky/tests/live_gate_contract.rs";

/// Tests that mention a probe for a reason that is not gating, with the reason
/// written down. Modelled on `workflows_parse.rs`'s
/// `NON_BLOCKING_PER_COMMIT_WORKFLOWS`: an exemption carries its justification
/// or it is a silent hole, and `every_probe_exemption_states_why` below asserts
/// both that the reason is real and that the test it names still exists.
const PROBE_NOT_A_GATE: &[(&str, &str, &str)] = &[(
    "crates/sky/tests/db_run_cluster_flow.rs",
    "an_explicit_dsn_alongside_embedded_refuses_to_run",
    "The claim is that `sky run` REFUSES an ambiguous [database] configuration, \
     which needs no cluster and no toolchain — the assertion is on the refusal \
     message and on `sky-out` not existing. `find_pg_bin()` appears only in the \
     cleanup, to stop a postmaster if the refusal failed to happen and one was \
     started anyway.",
)];

fn repo() -> PathBuf {
    PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../.."))
}

/// Every `.rs` file under the Rust workspace's crates, as (repo-relative path,
/// contents). Generated trees are excluded; `target/` is enormous and none of
/// it is ours.
fn sources() -> Vec<(String, String)> {
    fn walk(dir: &Path, out: &mut Vec<PathBuf>) {
        let Ok(rd) = std::fs::read_dir(dir) else { return };
        for e in rd.flatten() {
            let p = e.path();
            let name = e.file_name().to_string_lossy().to_string();
            if p.is_dir() {
                if name != "target" && !name.starts_with('.') {
                    walk(&p, out);
                }
            } else if p.extension().and_then(|x| x.to_str()) == Some("rs") {
                out.push(p);
            }
        }
    }
    let root = repo().join("rust/crates");
    let mut files = Vec::new();
    walk(&root, &mut files);
    files.sort();
    files
        .into_iter()
        .map(|p| {
            let rel = p
                .strip_prefix(repo().join("rust"))
                .unwrap_or(&p)
                .to_string_lossy()
                .replace('\\', "/");
            let text = std::fs::read_to_string(&p).unwrap_or_default();
            (rel, text)
        })
        .collect()
}

/// Split a file into `(test name, body)` for every `#[test]`. Crude on purpose:
/// a body runs to the next `\n}` at column zero, which is what `rustfmt`
/// guarantees for a top-level item and is enough to attribute a probe to a
/// test.
fn tests_in(text: &str) -> Vec<(String, String)> {
    let mut out = Vec::new();
    for (i, _) in text.match_indices("#[test]") {
        let rest = &text[i..];
        let end = rest.find("\n}\n").map(|e| e + 3).unwrap_or(rest.len());
        let body = &rest[..end];
        let name = body
            .find("fn ")
            .map(|f| {
                let after = &body[f + 3..];
                after.split(['(', '<', ' ']).next().unwrap_or("<unnamed>").to_string()
            })
            .unwrap_or_else(|| "<unnamed>".into());
        out.push((name, body.to_string()));
    }
    out
}

/// The gate itself must exist where the rules say it does. Without this, a
/// rename turns every rule below into a check on nothing.
#[test]
fn the_live_gate_exists_and_the_probe_list_is_live() {
    assert!(
        repo().join("rust").join(GATE_FILE).is_file(),
        "{GATE_FILE} is missing — every rule in this file is then checking a \
         mechanism that does not exist"
    );
    assert!(!PROBES.is_empty(), "PROBES is empty; the probe rule inspects nothing");
    let all: String = sources().iter().map(|(_, t)| t.as_str()).collect();
    for p in PROBES {
        assert!(
            all.contains(p),
            "the probe name `{p}` appears nowhere in the workspace. Either it was \
             renamed — in which case tests probing for a live environment under the \
             new name are no longer checked — or it is a typo that has been checking \
             nothing since it was written."
        );
    }
}

/// Rule 1 — the skip mechanism lives in exactly one place.
///
/// Four files had grown a private `fn skip()` with the same body. Four copies
/// of a convention is how fourteen tests came to be without it.
#[test]
fn no_file_defines_its_own_skip_helper() {
    let mut bad = Vec::new();
    for (path, text) in sources() {
        if path == GATE_FILE || path == SELF {
            continue;
        }
        if text.contains("fn skip(") {
            bad.push(path);
        }
    }
    assert!(
        bad.is_empty(),
        "these files define their own skip helper instead of using {GATE_FILE}:\n  {}\n\n\
         One mechanism, or it is a convention — and a convention is what fourteen \
         security tests were written without.",
        bad.join("\n  ")
    );
}

/// Rule 2 — a skip is never announced with `eprintln!`.
///
/// libtest captures it and prints the capture only for a test that FAILED, so
/// the announcement is invisible in precisely the runs it exists for.
#[test]
fn no_skip_is_announced_through_libtests_capture() {
    let mut bad = Vec::new();
    for (path, text) in sources() {
        if path == GATE_FILE || path == SELF || !path.contains("/tests/") && !path.contains("_tests.rs") {
            continue;
        }
        for (n, line) in text.lines().enumerate() {
            let l = line.to_lowercase();
            if l.contains("eprintln!") && (l.contains("skip") || l.contains("skipping")) {
                bad.push(format!("{path}:{}: {}", n + 1, line.trim()));
            }
        }
    }
    assert!(
        bad.is_empty(),
        "a skip announced with `eprintln!` is announced into libtest's output \
         capture, which is printed only for tests that FAILED — so it says nothing \
         in exactly the runs where it matters:\n  {}\n\n\
         Use `live_gate::required(Need::…, <probe>)`, which fails by default and \
         writes to the process's own stderr when SKY_LIVE_TESTS=skip.",
        bad.join("\n  ")
    );
}

/// Rule 3 — a test that asks whether a live environment is present must ask
/// through the gate.
#[test]
fn every_test_that_probes_for_a_live_environment_goes_through_the_gate() {
    let mut bad = Vec::new();
    let mut checked = 0usize;
    for (path, text) in sources() {
        if path == GATE_FILE || path == SELF {
            continue;
        }
        for (name, body) in tests_in(&text) {
            if !PROBES.iter().any(|p| body.contains(p)) {
                continue;
            }
            checked += 1;
            let exempt = PROBE_NOT_A_GATE.iter().any(|(f, t, _)| *f == path && *t == name);
            if !exempt && !GATED_BY.iter().any(|g| body.contains(g)) {
                bad.push(format!("{path}: {name}"));
            }
        }
    }
    assert!(
        checked >= 10,
        "only {checked} environment-probing test(s) found — the `#[test]` split is \
         wrong, and a rule that inspects nothing passes silently"
    );
    assert!(
        bad.is_empty(),
        "these tests decide for themselves whether their environment is present:\n  {}\n\n\
         Route the decision through `live_gate::required(Need::…, <probe>)`. A live \
         test that did not run has not passed, and `ok. 14 passed` said otherwise \
         for fourteen shared-cluster security tests in every CI job that ran them.",
        bad.join("\n  ")
    );
}

/// Rule 4 — the one need that is never enforced is used only where it was
/// agreed to be.
#[test]
fn the_network_carve_out_is_not_a_general_escape_hatch() {
    let mut found = Vec::new();
    for (path, text) in sources() {
        if path == GATE_FILE || path == SELF || path == GATE_CONTRACT {
            continue;
        }
        if text.contains("Need::Network") {
            found.push(path);
        }
    }
    found.sort();
    let mut want: Vec<String> = NETWORK_SITES.iter().map(|s| s.to_string()).collect();
    want.sort();
    assert_eq!(
        found, want,
        "`Need::Network` is the one need the live gate never enforces, so where it \
         is used is a decision and not a habit. Add the file to NETWORK_SITES with \
         the reason in the test's own comment, or gate on the thing that is really \
         missing."
    );
}

/// Rule 5 — an exemption carries its justification, and names something real.
///
/// `workflows_parse.rs` learnt this one first: an allowlist entry with an empty
/// reason is a silent exemption, which is the thing being prevented. A stale
/// entry is worse — it widens the hole it was carved for and reads like it is
/// still doing work.
#[test]
fn every_probe_exemption_states_why() {
    let files = sources();
    for (path, name, reason) in PROBE_NOT_A_GATE {
        assert!(
            reason.len() > 60,
            "`{path}: {name}` is exempted from the probe rule with no real reason \
             ({} chars). Write why the probe there is not a gate.",
            reason.len()
        );
        let Some((_, text)) = files.iter().find(|(p, _)| p == path) else {
            panic!("`{path}` is exempted but no such file exists — a stale exemption");
        };
        assert!(
            tests_in(text).iter().any(|(n, _)| n == name),
            "`{path}` has no test called `{name}`, but it is exempted from the probe \
             rule. Either it was renamed — in which case the test under the new name \
             is unchecked — or the exemption outlived what it was for."
        );
    }
}

/// Rule 6 — a helper that satisfies rule 3 by delegation must itself gate.
///
/// Rule 3 accepts `provision_fixture(…)` in a test body as evidence that the
/// test is gated, because the fixture constructor is where the probe lives.
/// That is only true while the constructor really does gate: strip the gate out
/// of `provision_fixture` and every caller still names it, so rule 3 stays
/// green over fourteen tests that once again decide for themselves. The
/// delegation is checked rather than assumed.
#[test]
fn every_helper_that_satisfies_the_rule_by_delegation_gates_for_real() {
    let files = sources();
    // Every entry of GATED_BY that is not the gate itself must appear here, so
    // adding a new delegating spelling forces a decision about where it is
    // proved rather than widening rule 3 by one line.
    for helper in GATED_BY {
        if *helper == "required(" || *helper == "live_gate::" {
            continue;
        }
        let name = helper.trim_end_matches('(');
        assert!(
            DELEGATES.iter().any(|(h, _)| *h == name),
            "`{helper}` is accepted by rule 3 as evidence that a test is gated, but \
             DELEGATES does not say where that gating lives, so nothing checks it."
        );
    }
    for (helper, path) in DELEGATES {
        let bare = helper.rsplit("::").next().unwrap_or(helper);
        let Some((_, text)) = files.iter().find(|(p, _)| p == path) else {
            panic!("`{path}` is named as the home of `{helper}` and does not exist");
        };
        let Some(at) = text.find(&format!("fn {bare}(")) else {
            panic!(
                "`{path}` is named as the home of `{helper}` but defines no \
                 `fn {bare}(` — a rename here silently un-checks every test that \
                 delegates to it"
            );
        };
        // The HELPER's own body, not the file's. A file-level check passes as
        // long as the live gate is named anywhere in it, and these files each
        // hold several gated tests alongside the helper — so stripping the gate
        // out of the helper alone left the file still mentioning it, and this
        // rule green over exactly the mutation it exists to catch.
        let rest = &text[at..];
        let body = &rest[..rest.find("\n}\n").map(|e| e + 3).unwrap_or(rest.len())];
        assert!(
            body.contains("required(") || body.contains("live_gate::"),
            "`{path}`'s `{helper}` is accepted by rule 3 as a gate, but its own body \
             names the live gate nowhere. Every test that delegates to it is \
             unchecked, and the delegation is a claim with nothing behind it."
        );
    }
    assert!(!DELEGATES.is_empty(), "DELEGATES is empty — this rule inspects nothing");
}

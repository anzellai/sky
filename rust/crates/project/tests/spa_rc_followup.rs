//! Two auto-split defects a real app run found in the v0.25.17 release
//! candidate (docs/history/v0.25.17/audit-register.md, section M).
//!
//! R1: a server branch whose command is a LET-BOUND
//! `Cmd.batch (List.map (\x -> Cmd.perform … Sent) xs)` read as opaque, so the
//! split treated every Msg as a possible follow-up and failed the build on an
//! unrelated constructor with no wire codec (`GotConfig`, a `Dict` argument).
//! The command is now read exactly; a command that still cannot be read warns
//! about such a constructor instead of failing the build.
//!
//! R3: a branch that rebuilds the model on one leaf (whole-model response) and
//! keeps it on another (`{ model | error = … }`) sent only its read fields, so
//! the keeping leaf answered with `init`'s `page` (a wrong password moved the
//! app to "/"). Its request now carries the whole model.
//!
//! Fixture: crates/sky/tests/fixtures/spa-rc-followup.

use project::{spa_partition, spa_split};
use std::path::PathBuf;

fn repo_root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    loop {
        if dir.join("sky-stdlib").is_dir() {
            return dir;
        }
        assert!(
            dir.pop(),
            "could not locate repo root (no sky-stdlib ancestor)"
        );
    }
}

fn fixture() -> PathBuf {
    repo_root().join("rust/crates/sky/tests/fixtures/spa-rc-followup")
}

fn analyze() -> spa_partition::SpaPartitionReport {
    spa_partition::analyze(&repo_root(), &fixture(), None)
        .unwrap_or_else(|e| panic!("analyze failed: {e}"))
}

#[test]
fn a_let_bound_batch_over_list_map_is_read_exactly() {
    let r = analyze();
    let f = r
        .follow_up
        .iter()
        .find(|f| f.branch == "SendAll")
        .unwrap_or_else(|| panic!("`SendAll` must be a follow-up branch: {:?}", r.follow_up));
    assert_eq!(
        f.ctors,
        Some(vec!["Sent".to_string()]),
        "the follow-ups of a let-bound `Cmd.batch (List.map (\\x -> Cmd.perform … Sent) xs)` are exactly `Sent`"
    );
    // A per-element perform is never a single-perform chain or client-result root.
    assert!(!r.chaining_branches.contains(&"SendAll".to_string()));
    assert!(!r.client_result.iter().any(|(root, _)| root == "SendAll"));
}

#[test]
fn an_unreadable_command_warns_about_an_uncodable_msg_instead_of_failing() {
    let out = std::env::temp_dir().join(format!("sky-spa-rc-followup-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&out);
    let rep = spa_split::generate(&repo_root(), &fixture(), None, &out, None, None)
        .unwrap_or_else(|e| panic!("the fixture must split (R1): {e}"));
    let warned = rep
        .warnings
        .iter()
        .find(|w| w.contains("`Blast`") && w.contains("`GotConfig`"))
        .unwrap_or_else(|| {
            panic!(
                "a build warning must name `Blast` and `GotConfig`: {:?}",
                rep.warnings
            )
        });
    assert!(warned.contains("SpaFollowUpOutsideWire"), "{warned}");
    let back = std::fs::read_to_string(out.join("backend/src/Main.sky")).unwrap();
    assert!(
        back.contains("GotConfig _ ->\n            spaFollowOutsideWire_ \"GotConfig\""),
        "the backend must log and drop a `GotConfig` follow-up, never send it:\n{back}"
    );
    assert!(back.contains("Ffi.kernel \"Spa_followUpOutsideWire\""));
    let _ = std::fs::remove_dir_all(&out);
}

#[test]
fn a_whole_model_response_with_a_keeping_leaf_sends_the_whole_model() {
    let r = analyze();
    let io = |m: &str| {
        r.branches
            .iter()
            .find(|b| b.msg.split_whitespace().next() == Some(m))
            .and_then(|b| b.io.clone())
            .unwrap_or_else(|| panic!("`{m}` must be a server branch"))
    };
    let sign_in = io("SignIn");
    assert!(
        sign_in.writes_whole_model,
        "the success leaf rebuilds the model"
    );
    assert!(
        sign_in.request_whole_model(),
        "the failure leaf keeps the client model, so `page` must ride the request: {sign_in:?}"
    );
    let reset = io("Reset");
    assert!(reset.writes_whole_model && reset.fresh_response);
    assert!(
        !reset.request_whole_model(),
        "every leaf of `Reset` is fresh: its request stays narrow: {reset:?}"
    );
}

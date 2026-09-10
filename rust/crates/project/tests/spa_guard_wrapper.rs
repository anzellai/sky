//! Soundness proof for the Sky.Spa guard-wrapper read/write-set narrowing.
//!
//! A common auth pattern wraps an `update` arm's real work in a higher-order
//! guard: `requireSession model (\_ -> CONT)`, where
//! `requireSession : Model -> (() -> ( Model, Cmd Msg )) -> ( Model, Cmd Msg )`.
//! Because the bare `model` flows into `requireSession` opaquely, the RPC I/O
//! analysis (`spa_partition::compute_branch_io`) used to classify the whole arm
//! `reads_whole_model` / `writes_whole_model`. Two real bugs followed: the RPC
//! `Req` carried the WHOLE model (incl. unresolved `List any` fields), and the
//! frontend sent bare `model`, which lacks the Msg-arg field the backend `Req`
//! expects (`record is missing field(s): id`).
//!
//! The fix recognises the guard-wrapper and narrows the arm's I/O to the UNION
//! of (i) the guard helper's own I/O on its model parameter and (ii) the inline
//! lambda continuation's I/O. This drives the real pipeline over
//! `crates/sky/tests/fixtures/spa-guard-wrapper` and asserts:
//!   * `Edit` narrows — write-set {label, picked}, read-set {session}, and it
//!     EXCLUDES the untouched `secret` / `basket`;
//!   * the negative guard `SaveAll` (a genuine opaque whole-model thread) stays
//!     whole — the narrow only fires when proven.

use project::spa_partition::{self, BranchIo, BranchVerdict};
use std::path::PathBuf;

fn repo_root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    loop {
        if dir.join("sky-stdlib").is_dir() {
            return dir;
        }
        assert!(dir.pop(), "could not locate repo root (no sky-stdlib ancestor)");
    }
}

fn fixture_dir() -> PathBuf {
    repo_root().join("rust/crates/sky/tests/fixtures/spa-guard-wrapper")
}

fn analyze() -> Vec<BranchVerdict> {
    let repo = repo_root();
    let proj = fixture_dir();
    let report = spa_partition::analyze(&repo, &proj, None)
        .unwrap_or_else(|e| panic!("analyze failed: {e}"));
    report.branches
}

fn branch<'a>(branches: &'a [BranchVerdict], msg: &str) -> &'a BranchVerdict {
    branches.iter().find(|b| b.msg == msg).unwrap_or_else(|| {
        panic!(
            "no `{msg}` branch in {:?}",
            branches.iter().map(|b| &b.msg).collect::<Vec<_>>()
        )
    })
}

fn io<'a>(branches: &'a [BranchVerdict], msg: &str) -> &'a BranchIo {
    branch(branches, msg)
        .io
        .as_ref()
        .unwrap_or_else(|| panic!("`{msg}` is not a SERVER branch (io == None)"))
}

#[test]
fn pure_client_arm_is_not_a_server_branch() {
    let bs = analyze();
    let b = branch(&bs, "Bump");
    assert!(!b.server, "Bump is pure/client, must not be SERVER: {}", b.reason);
    assert!(b.io.is_none(), "a CLIENT branch carries no RPC I/O");
}

#[test]
fn guard_wrapper_arm_narrows_write_set_to_the_continuation() {
    let bs = analyze();
    let b = branch(&bs, "Edit _");
    assert!(b.server, "Edit's continuation reaches File -> SERVER: {}", b.reason);
    let io = io(&bs, "Edit _");
    assert!(
        !io.writes_whole_model,
        "Edit's tail is `requireSession model (\\_ -> CONT)`; the write-set is the \
         continuation's {{label, picked}} unioned with the guard's own (empty) writes \
         — it MUST narrow, not stay whole"
    );
    assert_eq!(
        io.write_fields,
        vec!["label".to_string(), "picked".to_string()],
        "write-set = exactly the fields the inline continuation rewrites"
    );
    for untouched in ["secret", "basket"] {
        assert!(
            !io.write_fields.contains(&untouched.to_string()),
            "`{untouched}` is touched by NEITHER the guard nor the continuation — it MUST NOT ride the RPC response"
        );
    }
}

#[test]
fn guard_wrapper_arm_narrows_read_set_to_guard_plus_continuation() {
    let bs = analyze();
    let io = io(&bs, "Edit _");
    assert!(
        !io.reads_whole_model,
        "the read-set is the UNION of the guard's own reads {{session}} and the \
         continuation's reads — both narrow, so the arm MUST NOT read the whole model"
    );
    assert_eq!(
        io.read_fields,
        vec!["session".to_string()],
        "read-set = the guard's own `model.session` read; the continuation reads no model field"
    );
    for untouched in ["secret", "basket"] {
        assert!(
            !io.read_fields.contains(&untouched.to_string()),
            "`{untouched}` is read by NEITHER the guard nor the continuation — it MUST NOT ride the RPC request"
        );
    }
    assert_eq!(
        io.msg_args,
        vec!["id".to_string()],
        "the Msg arg `id` is captured from the arm pattern and rides the request"
    );
}

#[test]
fn genuine_opaque_whole_model_arm_stays_whole() {
    let bs = analyze();
    let io = io(&bs, "SaveAll _");
    assert!(
        io.reads_whole_model,
        "SaveAll threads `model` opaquely through `Result.withDefault` — its read-set is NOT provably narrow, so it MUST stay the whole model (the narrow only fires when proven)"
    );
    assert!(
        io.writes_whole_model,
        "SaveAll's returned model is an opaque `Result.withDefault` result, not a `{{ model | … }}` — its write-set MUST stay the whole model"
    );
    assert_eq!(
        io.msg_args,
        vec!["tagStr".to_string()],
        "SaveAll binds the Msg arg `tagStr`, which the backend `Req` carries alongside the whole model"
    );
}

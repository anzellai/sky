//! GAP #6 soundness proof for the Sky.Spa write-set / read-set narrowing.
//!
//! The write-set analysis (`spa_partition::compute_branch_io`) narrows a SERVER
//! branch's RPC response to specific Model fields ONLY when it can PROVE the
//! returned model is a field-preserving transform of `model`; on any doubt it
//! over-approximates to the whole model (sound — a bigger payload, never a wrong
//! value). Dropping a real write (under-approximating) is a correctness bug.
//!
//! This drives the real pipeline over `crates/sky/tests/fixtures/spa-writeset-narrow`
//! (a single app whose arms exercise BOTH directions) and asserts each arm's
//! derived read-set / write-set, especially:
//!   * `SaveNarrow` narrows to `{log, n, tag}` and EXCLUDES `extra`;
//!   * `SaveWhole` (a fresh `rebuild` record) stays the WHOLE model — the guard
//!     proving the narrow only fires when proven.

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
    repo_root().join("rust/crates/sky/tests/fixtures/spa-writeset-narrow")
}

fn analyze() -> Vec<BranchVerdict> {
    let repo = repo_root();
    let proj = fixture_dir();
    let report = spa_partition::analyze(&repo, &proj, None)
        .unwrap_or_else(|e| panic!("analyze failed: {e}"));
    report.branches
}

fn branch<'a>(branches: &'a [BranchVerdict], msg: &str) -> &'a BranchVerdict {
    branches
        .iter()
        .find(|b| b.msg == msg)
        .unwrap_or_else(|| panic!("no `{msg}` branch in {:?}", branches.iter().map(|b| &b.msg).collect::<Vec<_>>()))
}

fn io<'a>(branches: &'a [BranchVerdict], msg: &str) -> &'a BranchIo {
    branch(branches, msg)
        .io
        .as_ref()
        .unwrap_or_else(|| panic!("`{msg}` is not a SERVER branch (io == None)"))
}

#[test]
fn client_arm_is_not_a_server_branch() {
    let bs = analyze();
    let b = branch(&bs, "Bump");
    assert!(!b.server, "Bump is pure/client, must not be SERVER: {}", b.reason);
    assert!(b.io.is_none(), "a CLIENT branch carries no RPC I/O");
}

#[test]
fn save_narrow_field_preserving_chain_narrows_and_excludes_extra() {
    let bs = analyze();
    let b = branch(&bs, "SaveNarrow");
    assert!(b.server, "SaveNarrow reaches File -> SERVER");
    let io = io(&bs, "SaveNarrow");
    assert!(
        !io.writes_whole_model,
        "SaveNarrow returns a PURE field-preserving chain `noteLog (stamp {{ model | n = … }})` \
         — its write-set is statically {{log, n, tag}}, so it MUST narrow, not stay whole"
    );
    assert_eq!(
        io.write_fields,
        vec!["log".to_string(), "n".to_string(), "tag".to_string()],
        "write-set must be exactly the fields the chain rewrites"
    );
    assert!(
        !io.write_fields.contains(&"extra".to_string()),
        "`extra` is untouched by the chain — it MUST NOT ride the RPC response"
    );
}

#[test]
fn server_field_preserving_helper_narrows() {
    let bs = analyze();
    let io = io(&bs, "SaveServerNarrow");
    assert!(
        !io.writes_whole_model,
        "bumpServer is structurally field-preserving (writes only {{tag}}) — narrowable"
    );
    assert_eq!(
        io.write_fields,
        vec!["n".to_string(), "tag".to_string()],
        "write-set = {{n}} (inner update) ∪ {{tag}} (bumpServer)"
    );
}

#[test]
fn read_via_accessor_narrows_read_set() {
    let bs = analyze();
    let io = io(&bs, "ReadNarrow");
    assert!(!io.writes_whole_model, "ReadNarrow writes only {{log}}");
    assert_eq!(io.write_fields, vec!["log".to_string()]);
    assert!(
        !io.reads_whole_model,
        "ReadNarrow reads `model` ONLY via the pure accessor `pluck` (reads .tag) — read-set narrows to {{tag}}"
    );
    assert_eq!(io.read_fields, vec!["tag".to_string()]);
}

#[test]
fn whole_arm_delegate_inherits_helper_io_not_whole_model() {
    let bs = analyze();
    let io = io(&bs, "DelegateArm");
    assert!(
        !io.writes_whole_model,
        "DelegateArm delegates to `handle` (writes {{log}}) — it inherits that narrow write-set, not the whole model"
    );
    assert_eq!(io.write_fields, vec!["log".to_string()]);
    assert!(!io.reads_whole_model, "inherits handle's read-set {{n}}");
    assert_eq!(io.read_fields, vec!["n".to_string()]);
}

#[test]
fn let_bound_tuple_returned_by_name_narrows() {
    let bs = analyze();
    let io = io(&bs, "LetBound");
    assert!(
        !io.writes_whole_model,
        "LetBound returns a let-bound `( {{ model | tag = … }}, cmd )` by name — write-set narrows to {{tag}}"
    );
    assert_eq!(io.write_fields, vec!["tag".to_string()]);
}

#[test]
fn fresh_record_stays_whole_model_the_soundness_guard() {
    let bs = analyze();
    let io = io(&bs, "SaveWhole");
    assert!(
        io.writes_whole_model,
        "SaveWhole returns `rebuild model`, a FRESH record — NOT provably narrow, so the whole model MUST ride out"
    );
    // With whole-model, `extra` is (correctly) carried — the narrow did not fire.
}

#[test]
fn opaque_thread_stays_whole_model_both_ways() {
    let bs = analyze();
    let io = io(&bs, "OpaqueWhole");
    assert!(
        io.writes_whole_model,
        "OpaqueWhole returns `Result.withDefault model (Ok model)` — not a visible `{{ model | … }}`, so the whole model rides OUT"
    );
    assert!(
        io.reads_whole_model,
        "OpaqueWhole threads the whole `model` into `Result.withDefault` opaquely — no field is provably read, so the whole model rides IN"
    );
}

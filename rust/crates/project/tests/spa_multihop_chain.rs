//! Transitive (multi-hop) server-internal effect chaining for the Sky.Spa
//! auto-split.
//!
//! Pattern-1 chaining settles an all-server `Cmd.perform serverTask ToMsg` chain
//! inside the triggering RPC. A 2-hop chain (root -> continuation) already
//! settled. This test covers a 3+-hop chain where a continuation's OWN arm
//! delegates to a helper returning `( model, Cmd.batch [ perform …, perform … ] )`
//! -- so the further continuation Msgs live BEHIND a helper delegation. Before
//! this change the continuation was marked "dirty" (no isolable `( model, cmd )`
//! pair), the chain failed to settle, and the root fell to a fail-closed warning
//! (the darraghstudio `RunFinalize`/`OrderFinalized` deeper-chain warning).
//!
//! Drives the real pipeline over `crates/sky/tests/fixtures/spa-multihop-chain`
//! (positive) and `crates/sky/tests/fixtures/spa-multihop-native` (negative):
//!   * `Kick` -> `Fetched` (via helper `record`) -> `SavedA` + `LoggedB`: all
//!     server, so `Kick` chains and `Fetched`/`SavedA`/`LoggedB` are ALL
//!     server-internal; the write-set unions `busy`+`c`+`a`+`b` and NOT the
//!     untouched field.
//!   * the negative app's deepest hop reaches a `Std.Native` client effect, so
//!     the WHOLE root fails closed (no partial settle).

use project::spa_partition;
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

fn fixture_dir(name: &str) -> PathBuf {
    repo_root().join("rust/crates/sky/tests/fixtures").join(name)
}

fn analyze(name: &str) -> spa_partition::SpaPartitionReport {
    spa_partition::analyze(&repo_root(), &fixture_dir(name), None)
        .unwrap_or_else(|e| panic!("analyze failed: {e}"))
}

// ── Positive: the 3-hop all-server chain settles transitively ────────────────

#[test]
fn kick_is_a_chaining_root_across_a_helper_delegated_hop() {
    let r = analyze("spa-multihop-chain");
    assert!(
        r.chaining_branches.contains(&"Kick".to_string()),
        "`Kick` triggers an all-server chain whose deep hop is behind a helper -- \
         it must be a chaining ROOT; got chaining_branches={:?}, warnings={:?}",
        r.chaining_branches,
        r.server_chain_warnings
    );
    // No fail-closed warning: the whole chain settles.
    assert!(
        !r.server_chain_warnings.iter().any(|w| w.contains("Kick")
            || w.contains("Fetched")
            || w.contains("SavedA")
            || w.contains("LoggedB")),
        "the all-server multi-hop chain settles -- no fail-closed warning expected; got {:?}",
        r.server_chain_warnings
    );
}

#[test]
fn all_transitive_continuations_are_server_internal() {
    let r = analyze("spa-multihop-chain");
    for m in ["Fetched", "SavedA", "LoggedB"] {
        assert!(
            r.server_internal.contains(&m.to_string()),
            "`{m}` is reachable ONLY through `Kick`'s server chain (transitively) -- \
             it must be SERVER-INTERNAL; got server_internal={:?}",
            r.server_internal
        );
    }
    // The deep continuations reach server effects -- they must NOT be handed to
    // the client as pattern-2 (client-result) dispatches.
    for m in ["Fetched", "SavedA", "LoggedB"] {
        assert!(
            !r.client_result.iter().any(|(_, rm)| rm == m),
            "`{m}` reaches a server effect -- it must NOT be a client-result dispatch; got {:?}",
            r.client_result
        );
    }
}

#[test]
fn kick_writeset_unions_every_hop_and_excludes_the_untouched_field() {
    let r = analyze("spa-multihop-chain");
    let kick = r
        .branches
        .iter()
        .find(|b| b.msg == "Kick" || b.msg.split_whitespace().next() == Some("Kick"))
        .expect("no Kick branch");
    assert!(kick.server, "Kick performs a File task -> SERVER");
    let io = kick.io.as_ref().expect("Kick is a SERVER branch with I/O");
    assert!(
        !io.writes_whole_model,
        "the transitive write-set union MUST stay NARROW (not whole model); got whole={}",
        io.writes_whole_model
    );
    // The UNION over every reachable hop: Kick's own `busy`, `record`/`Fetched`'s
    // `c`, `SavedA`'s `a`, `LoggedB`'s `b`.
    for f in ["busy", "c", "a", "b"] {
        assert!(
            io.write_fields.contains(&f.to_string()),
            "`Kick`'s response write-set must gain `{f}` from the settled chain; got {:?}",
            io.write_fields
        );
    }
    // The untouched field is written by NO arm in the chain.
    assert!(
        !io.write_fields.contains(&"untouched".to_string()),
        "`untouched` is written by NO arm in the chain -- it must NOT be in the write-set; got {:?}",
        io.write_fields
    );
}

// ── Negative: a client leaf in the deepest hop fails the WHOLE root closed ────

#[test]
fn a_native_leaf_in_the_deepest_hop_fails_the_whole_root_closed() {
    let r = analyze("spa-multihop-native");
    // No partial settle: the root does NOT chain.
    assert!(
        !r.chaining_branches.contains(&"Kick".to_string()),
        "the deepest hop reaches a `Std.Native` client effect -- `Kick` must NOT chain \
         (no partial settle); got chaining_branches={:?}",
        r.chaining_branches
    );
    // Neither the mid nor the deep continuation is pruned from the wire.
    for m in ["Fetched", "SavedA"] {
        assert!(
            !r.server_internal.contains(&m.to_string()),
            "`{m}` sits on a chain that fails closed -- it must NOT be server-internal; got {:?}",
            r.server_internal
        );
    }
    // A fail-closed warning names the root.
    assert!(
        r.server_chain_warnings.iter().any(|w| w.contains("Kick")),
        "a fail-closed warning must name the root `Kick`; got {:?}",
        r.server_chain_warnings
    );
}

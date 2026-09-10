//! Guard-wrapped server-internal effect chaining for the Sky.Spa auto-split.
//!
//! Pattern-1 chaining settles an all-server `Cmd.perform serverTask ToMsg` chain
//! inside the triggering RPC. Before this change the chaining-ROOT detection
//! walked ONLY the DIRECT tail tuple, so a `Cmd.perform` returned THROUGH a
//! higher-order guard wrapper (`guard model (\_ -> ( …, Cmd.perform … ))`, the
//! darraghstudio `requireAdmin` shape) was invisible: the continuation Msg stayed
//! a BROKEN wire branch (`missing field(s)` / `Result Error String vs Error`).
//!
//! Drives the real pipeline over `crates/sky/tests/fixtures/spa-guarded-chain`:
//!   * `Trigger` (guard-wrapped, File write) → `Saved` (guard-wrapped, File read,
//!     ALL-server) → `Trigger` chains and `Saved` is SERVER-INTERNAL;
//!   * `Trigger`'s response write-set narrows to `{busy, log, note}` — the fields
//!     the chain writes — and MUST NOT include the untouched `count`.

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

fn fixture_dir() -> PathBuf {
    repo_root().join("rust/crates/sky/tests/fixtures/spa-guarded-chain")
}

fn analyze() -> spa_partition::SpaPartitionReport {
    spa_partition::analyze(&repo_root(), &fixture_dir(), None)
        .unwrap_or_else(|e| panic!("analyze failed: {e}"))
}

#[test]
fn guard_wrapped_all_server_chain_settles() {
    let r = analyze();
    // The guard-wrapped root chains.
    assert!(
        r.chaining_branches.contains(&"Trigger".to_string()),
        "`Trigger` returns a guard-wrapped `Cmd.perform` to an all-server continuation — \
         it must be a chaining ROOT; got chaining_branches={:?}",
        r.chaining_branches
    );
    // The guard-wrapped all-server continuation is server-internal (pruned wire).
    assert!(
        r.server_internal.contains(&"Saved".to_string()),
        "`Saved` is dispatched ONLY by `Trigger`'s guarded `Cmd.perform` and its own arm is \
         guard-wrapped + all-server — it must be SERVER-INTERNAL; got server_internal={:?}",
        r.server_internal
    );
    // It must NOT fall to pattern-2 (client-result) — the continuation runs a
    // SERVER effect, so its result may never be handed to the wasm client.
    assert!(
        !r.client_result.iter().any(|(_, m)| m == "Saved"),
        "`Saved` reaches a server effect — it must NOT be a client-result dispatch; got {:?}",
        r.client_result
    );
    // No fail-closed warning: the chain settles cleanly.
    assert!(
        !r.server_chain_warnings.iter().any(|w| w.contains("Trigger") || w.contains("Saved")),
        "the guard-wrapped all-server chain settles — no fail-closed warning expected; got {:?}",
        r.server_chain_warnings
    );
}

#[test]
fn trigger_writeset_unions_the_continuation_narrowly() {
    let r = analyze();
    let trig = r
        .branches
        .iter()
        .find(|b| b.msg == "Trigger" || b.msg.split_whitespace().next() == Some("Trigger"))
        .expect("no Trigger branch");
    assert!(trig.server, "Trigger reaches File -> SERVER");
    let io = trig.io.as_ref().expect("Trigger is a SERVER branch with I/O");
    // Soundness: the write-set must NOT be lost — it gains the continuation's
    // narrow writes, and must NOT over-approximate to the whole model.
    assert!(
        !io.writes_whole_model,
        "the guard-through write-set union MUST stay NARROW (not whole model); got whole={}",
        io.writes_whole_model
    );
    for f in ["busy", "log", "note"] {
        assert!(
            io.write_fields.contains(&f.to_string()),
            "`Trigger`'s response write-set must gain `{f}` from the settled chain; got {:?}",
            io.write_fields
        );
    }
    // The untouched field is never written — dropping the narrow union onto the
    // whole model would re-send `count` needlessly.
    assert!(
        !io.write_fields.contains(&"count".to_string()),
        "`count` is written by NO arm in the chain — it must NOT be in the write-set; got {:?}",
        io.write_fields
    );
}

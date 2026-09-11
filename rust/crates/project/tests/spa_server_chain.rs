//! Server-internal effect chaining for the Sky.Spa auto-split.
//!
//! A server RPC branch that returns `Cmd.perform serverTask ToMsg` (whose `ToMsg`
//! is dispatched ONLY server-side) must run the WHOLE chain inside its RPC and
//! answer with the final settled model — mirroring Sky.Live. Before this feature
//! the generated handler bound `( m2, _ )` and DISCARDED the command, so the read
//! ran nowhere and its write was silently dropped.
//!
//! Drives the real pipeline over `crates/sky/tests/fixtures/spa-server-chain`:
//!   * `Reload` (server, File read) → `Reloaded` (server-internal) → writes `note`;
//!   * `SyncCopy` (server) batches a server read with a `Std.Native` CLIENT effect
//!     → NOT chained (fail-closed), a warning is emitted.

use project::{spa_partition, spa_split};
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
    repo_root().join("rust/crates/sky/tests/fixtures/spa-server-chain")
}

fn analyze() -> spa_partition::SpaPartitionReport {
    spa_partition::analyze(&repo_root(), &fixture_dir(), None)
        .unwrap_or_else(|e| panic!("analyze failed: {e}"))
}

// ── Phase 1: classification ────────────────────────────────────────────────

#[test]
fn reloaded_is_server_internal_and_reload_chains() {
    let r = analyze();
    assert!(
        r.server_internal.contains(&"Reloaded".to_string()),
        "`Reloaded` is dispatched ONLY by `Reload`'s Cmd.perform — it must be SERVER-INTERNAL; got {:?}",
        r.server_internal
    );
    assert!(
        r.chaining_branches.contains(&"Reload".to_string()),
        "`Reload` returns a resolvable Cmd.perform chain — it must be a chaining branch; got {:?}",
        r.chaining_branches
    );
}

#[test]
fn server_classified_continuation_via_helper_is_server_internal() {
    // The darraghstudio `EmailSent` shape: `Ship` dispatches (through a HELPER
    // returning `Cmd.perform`) a `Shipped` continuation whose own arm reaches a
    // SERVER effect (`Log`). `Shipped` is server-CLASSIFIED yet dispatched only
    // server-side, so it must be SERVER-INTERNAL (removed from the wire set),
    // NOT a broken `Result Error String` wire branch.
    let r = analyze();
    assert!(
        r.server_internal.contains(&"Shipped".to_string()),
        "`Shipped` is a server-classified result Msg dispatched only via `Ship`'s helper `Cmd.perform` — it must be SERVER-INTERNAL; got {:?}",
        r.server_internal
    );
    assert!(
        r.chaining_branches.contains(&"Ship".to_string()),
        "`Ship` returns its command through a helper resolving to a clean perform chain — it must chain; got {:?}",
        r.chaining_branches
    );
    // `Ship`'s response write-set must gain `note` (written by `Shipped`).
    let ship = r.branches.iter().find(|b| b.msg == "Ship").expect("no Ship branch");
    let io = ship.io.as_ref().expect("Ship is a SERVER branch");
    assert!(
        io.write_fields.contains(&"note".to_string()) || io.writes_whole_model,
        "`Ship`'s write-set must gain `note` from the `Shipped` continuation; got {:?}",
        io.write_fields
    );
}

#[test]
fn synced_and_copied_are_not_server_internal_failclosed() {
    let r = analyze();
    // `SyncCopy` batches a Native client effect — it must NOT chain, so neither
    // continuation may be pruned from the client.
    assert!(
        !r.chaining_branches.contains(&"SyncCopy".to_string()),
        "`SyncCopy` mixes a server read with a Std.Native client effect — it must fail closed (not chain)"
    );
    for m in ["Synced", "Copied"] {
        assert!(
            !r.server_internal.contains(&m.to_string()),
            "`{m}` belongs to the un-chained `SyncCopy` branch — it MUST stay a client arm, not be pruned"
        );
    }
    assert!(
        r.server_chain_warnings.iter().any(|w| w.contains("SyncCopy")),
        "the un-chained `SyncCopy` branch must emit a fail-closed warning; got {:?}",
        r.server_chain_warnings
    );
}

// ── Phase 2: write-set union (soundness — the dropped-write bug) ────────────

#[test]
fn reload_writeset_gains_note_from_the_continuation() {
    let r = analyze();
    let reload = r
        .branches
        .iter()
        .find(|b| b.msg == "Reload")
        .expect("no Reload branch");
    assert!(reload.server, "Reload reaches File -> SERVER");
    let io = reload.io.as_ref().expect("Reload is a SERVER branch with I/O");
    assert!(
        io.write_fields.contains(&"note".to_string()) || io.writes_whole_model,
        "`Reload`'s response write-set MUST gain `note` (written by the server-internal `Reloaded` \
         continuation) — else the read runs but its write is silently dropped. got write_fields={:?}, whole={}",
        io.write_fields, io.writes_whole_model
    );
}

// ── Codegen: backend chains, frontend prunes ───────────────────────────────

fn generate(tag: &str) -> (PathBuf, spa_split::SpaSplitReport) {
    // A per-test unique dir — the codegen tests run in parallel threads of ONE
    // process, so a shared `pid`-only path would clobber a sibling mid-read.
    let out = std::env::temp_dir().join(format!("sky-spa-chain-{}-{tag}", std::process::id()));
    let _ = std::fs::remove_dir_all(&out);
    let report = spa_split::generate(&repo_root(), &fixture_dir(), None, &out, None, None)
        .unwrap_or_else(|e| panic!("generate failed: {e}"));
    (out, report)
}

fn read(p: &std::path::Path) -> String {
    std::fs::read_to_string(p).unwrap_or_else(|e| panic!("read {}: {e}", p.display()))
}

#[test]
fn backend_reload_handler_binds_and_settles_the_chain() {
    let (out, _r) = generate("backend");
    let back = read(&out.join("backend/src/Main.sky"));
    // Effect-not-dropped: the handler binds the returned command and settles it.
    assert!(
        back.contains("spaChainSettle_ m2 cmd update"),
        "reloadHandler must settle the Cmd.perform chain server-side, not discard it:\n{back}"
    );
    assert!(
        back.contains("spaChainSettle_ : model -> any -> any -> ( model, any )"),
        "the Spa_settleServerChain kernel alias must be emitted"
    );
    // The response is encoded from the FINAL settled model, over `note`.
    assert!(
        back.contains("Codec.toJson reloadRespCodec { note = mFinal.note }"),
        "Reload's response must carry the settled `note` from the chain's final model:\n{back}"
    );
    // The un-chained SyncCopy handler still DISCARDS its command (today's floor).
    let sync_block = back
        .split("syncCopyHandler req =")
        .nth(1)
        .unwrap_or("")
        .split("\n\n\n")
        .next()
        .unwrap_or("");
    assert!(
        sync_block.contains("( m2, _ ) =") && !sync_block.contains("spaChainSettle_"),
        "the fail-closed SyncCopy handler must keep today's behaviour (bind `_`, no chain):\n{sync_block}"
    );
    let _ = std::fs::remove_dir_all(&out);
}

#[test]
fn frontend_has_no_wire_leak_for_the_server_internal_msg() {
    let (out, _r) = generate("frontend");
    let front = read(&out.join("frontend/src/Main.sky"));
    // No /_rpc/Reloaded route, no Applied variant, no client arm, no union variant.
    assert!(
        !front.contains("/_rpc/Reloaded"),
        "server-internal `Reloaded` must have NO RPC route in the frontend"
    );
    assert!(
        !front.contains("AppliedReloaded"),
        "server-internal `Reloaded` must have NO Applied<Msg> variant"
    );
    assert!(
        !front.contains("| Reloaded "),
        "server-internal `Reloaded` must be pruned from the frontend Msg union"
    );
    // The un-chained SyncCopy's continuations MUST remain (fail-closed).
    assert!(
        front.contains("| Synced ") && front.contains("| Copied "),
        "fail-closed `Synced`/`Copied` must stay in the frontend Msg union"
    );
    // The triggering `Reload` still posts to its own RPC.
    assert!(
        front.contains("/_rpc/Reload\"") || front.contains("\"/_rpc/Reload\""),
        "Reload keeps its own wire branch"
    );
    // The server-CLASSIFIED continuation `Shipped` has no wire codec anywhere —
    // it is settled inside `Ship`'s RPC, never a `Result Error String` wire arg.
    let back = read(&out.join("backend/src/Main.sky"));
    let shared = read(&out.join("shared/Shared.sky"));
    assert!(
        !back.contains("shippedHandler") && !back.contains("/_rpc/Shipped"),
        "`Shipped` must have NO backend RPC handler/route (it settles inside Ship's RPC)"
    );
    assert!(
        !shared.contains("ShippedReq") && !shared.contains("ShippedResp"),
        "`Shipped` must have NO wire codec — it is server-internal, not a Result-typed wire branch"
    );
    let _ = std::fs::remove_dir_all(&out);
}

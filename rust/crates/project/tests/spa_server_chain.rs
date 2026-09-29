//! Server-internal effect chaining for the Sky.Spa auto-split.
//!
//! A server RPC branch that returns `Cmd.perform serverTask ToMsg` (whose `ToMsg`
//! is dispatched ONLY server-side) must run the WHOLE chain inside its RPC and
//! answer with the final settled model — mirroring Sky.Live. Before this feature
//! the generated handler bound `( m2, _ )` and DISCARDED the command, so the read
//! ran nowhere and its write was silently dropped.
//!
//! A continuation whose own arm is CLIENT (reaches no server effect) does not
//! settle on the server: it must run in the client when the task's result
//! arrives, on the model the client holds then, as Sky.Live runs it. Settled on
//! the server it read the send-time snapshot and overwrote what the client did
//! meanwhile. Such a root answers with the task result (pattern-2).
//!
//! Drives the real pipeline over `crates/sky/tests/fixtures/spa-server-chain`:
//!   * `Reload` (server, File read) → `Reloaded` (a CLIENT arm) → writes `note`
//!     in the client: `Reload` is a client-result root, `Reloaded` stays a client arm;
//!   * `Ship` (server) → `Shipped` (server-classified: `Log`) → the chain settles
//!     server-side, `Shipped` is server-internal, and `Ship` holds the queue;
//!   * `SyncCopy` (server) batches a server read with a `Std.Native` CLIENT effect
//!     → NOT chained (fail-closed); it is a FOLLOW-UP branch (SPA-3): its RPC
//!     runs the server read, the client runs the `Std.Native` leaf.

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

fn fixture_dir() -> PathBuf {
    repo_root().join("rust/crates/sky/tests/fixtures/spa-server-chain")
}

fn analyze() -> spa_partition::SpaPartitionReport {
    spa_partition::analyze(&repo_root(), &fixture_dir(), None)
        .unwrap_or_else(|e| panic!("analyze failed: {e}"))
}

// ── Phase 1: classification ────────────────────────────────────────────────

#[test]
fn effect_families_are_populated_for_a_direct_kernel_branch() {
    // `Reload`'s arm calls `File.readFile` directly — its BranchVerdict must carry
    // the `File` effect family. `SyncCopy` batches `File` + a `Native` client
    // effect, so its families include both. This is the structured effect data
    // the `sky doc --diagram` journey/wire slices render per branch.
    let r = analyze();
    let reload = r
        .branches
        .iter()
        .find(|b| b.msg == "Reload")
        .expect("no Reload branch");
    assert!(
        reload.effect_families.iter().any(|f| f == "File"),
        "Reload reaches File.readFile directly — effect_families must contain `File`; got {:?}",
        reload.effect_families
    );
    let sync = r
        .branches
        .iter()
        .find(|b| b.msg == "SyncCopy")
        .expect("no SyncCopy branch");
    assert!(
        sync.effect_families.iter().any(|f| f == "File")
            && sync.effect_families.iter().any(|f| f == "Native"),
        "SyncCopy batches File + Native — effect_families must contain both; got {:?}",
        sync.effect_families
    );
    // A branch that reaches no kernel directly has empty families.
    if let Some(pure) = r
        .branches
        .iter()
        .find(|b| !b.server && b.effect_families.is_empty())
    {
        assert!(pure.effect_families.is_empty());
    }
}

#[test]
fn reloaded_is_a_client_arm_and_reload_returns_its_result() {
    let r = analyze();
    assert!(
        !r.server_internal.contains(&"Reloaded".to_string()),
        "`Reloaded`'s arm is client — it must run in the client when the read ends, \
         not settle on the server from the send-time snapshot; got {:?}",
        r.server_internal
    );
    assert!(
        !r.chaining_branches.contains(&"Reload".to_string()),
        "`Reload`'s continuation is a client arm — it must not chain; got {:?}",
        r.chaining_branches
    );
    assert!(
        r.client_result
            .iter()
            .any(|(root, m)| root == "Reload" && m == "Reloaded"),
        "`Reload` must answer with the read's result for the client `Reloaded` arm; got {:?}",
        r.client_result
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
    let ship = r
        .branches
        .iter()
        .find(|b| b.msg == "Ship")
        .expect("no Ship branch");
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
    // SPA-3: no discard floor any more — `SyncCopy` is a FOLLOW-UP branch: its
    // RPC runs the server read and returns `Synced`, and the client runs the
    // `Std.Native` leaf itself (`native`).
    let fu = r
        .follow_up
        .iter()
        .find(|f| f.branch == "SyncCopy")
        .unwrap_or_else(|| {
            panic!(
                "`SyncCopy` must be a follow-up branch; got {:?}",
                r.follow_up
            )
        });
    assert!(
        fu.native,
        "`SyncCopy` batches a Std.Native leaf; got {fu:?}"
    );
    assert!(
        !r.server_chain_warnings
            .iter()
            .any(|w| w.contains("SyncCopy")),
        "the discard-and-warn floor is gone; got {:?}",
        r.server_chain_warnings
    );
}

// ── Phase 2: write-set union (soundness — the dropped-write bug) ────────────

#[test]
fn ship_writeset_gains_note_from_the_server_internal_continuation() {
    let r = analyze();
    let ship = r
        .branches
        .iter()
        .find(|b| b.msg == "Ship")
        .expect("no Ship branch");
    let io = ship.io.as_ref().expect("Ship is a SERVER branch with I/O");
    assert!(
        io.write_fields.contains(&"note".to_string()) || io.writes_whole_model,
        "`Ship`'s response write-set MUST gain `note` (written by the server-internal `Shipped` \
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
        back.contains("Codec.toJson shipRespCodec { note = mFinal.note }"),
        "Ship's response must carry the settled `note` from the chain's final model:\n{back}"
    );
    // Reload answers with the read's result; its client `Reloaded` arm applies it.
    let reload_block = back
        .split("reloadHandler req =")
        .nth(1)
        .unwrap_or("")
        .split("\n\n\n")
        .next()
        .unwrap_or("");
    assert!(
        reload_block.contains("spaRunPerform_ cmd") && !reload_block.contains("spaChainSettle_"),
        "the Reload handler must run the read and return its result (no chain):\n{reload_block}"
    );
    // SPA-3: the un-chained SyncCopy handler RUNS its command's server leaves
    // and returns the follow-up Msgs (no discard floor).
    let sync_block = back
        .split("syncCopyHandler req =")
        .nth(1)
        .unwrap_or("")
        .split("\n\n\n")
        .next()
        .unwrap_or("");
    assert!(
        sync_block.contains("( m2, cmd ) =")
            && sync_block.contains("spaEncodeFollows_ (spaFollowUps_ cmd)")
            && !sync_block.contains("spaChainSettle_"),
        "the SyncCopy handler must run its command and return the follow-ups (no chain):\n{sync_block}"
    );
    let _ = std::fs::remove_dir_all(&out);
}

#[test]
fn frontend_has_no_wire_leak_for_the_server_internal_msg() {
    let (out, _r) = generate("frontend");
    let front = read(&out.join("frontend/src/Main.sky"));
    // `Reloaded` is a client arm fed by Reload's result: no route of its own, no
    // Applied variant, but it stays in the union and runs in the client.
    assert!(
        !front.contains("/_rpc/Reloaded"),
        "`Reloaded` must have NO RPC route in the frontend"
    );
    assert!(
        !front.contains("AppliedReloaded"),
        "`Reloaded` must have NO Applied<Msg> variant"
    );
    assert!(
        front.contains("| Reloaded ") && front.contains("update (Reloaded resp.result) model"),
        "`Reloaded` stays a client arm, dispatched with Reload's result:\n{front}"
    );
    // The server-internal `Shipped` is pruned, and `Ship` holds the queue (its
    // chain settles on the server from the request).
    assert!(
        !front.contains("| Shipped ") && !front.contains("AppliedShipped"),
        "server-internal `Shipped` must be pruned from the frontend"
    );
    assert!(
        front.contains("Spa.rpcHold shipReqCodec") && front.contains("Spa.rpc reloadReqCodec"),
        "Ship (a chain root) holds; Reload (a client-result root) is async:\n{front}"
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

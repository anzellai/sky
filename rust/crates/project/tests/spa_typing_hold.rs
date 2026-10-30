//! v0.29.0: which input Msgs may run while a hold RPC is in flight.
//!
//! The Sky.Spa client holds every later Msg while a hold RPC runs (Sky.Live's
//! order). The auto-split lists a `(hold, input)` pair in `Spa.withHoldFree`
//! only when running the input Msg early gives the same model: the input arm
//! is client code with no command, and its model fields do not meet the
//! hold's result, its follow-ups or `subscriptions`
//! (`spa_partition::hold_free_pairs`).
//!
//! Drives the real pipeline over `crates/sky/tests/fixtures/spa-typing-hold`:
//!   * `SetQuery` writes `query`, which `subscriptions` reads together with
//!     `searchedFor`, which `RunSearch` writes: not free during `RunSearch`;
//!   * `SetNote` writes only `note`: free during both holds;
//!   * `SetQuery` is free during `SaveNote`, which writes only `savedNote`;
//!   * `SetCode` / `RunLookup` repeat the search pair through a `Std.Html`
//!     input: `SetCode` waits for `RunLookup` and is free during `SaveNote`.

use project::spa_partition;
use std::collections::BTreeSet;
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
    repo_root().join("rust/crates/sky/tests/fixtures/spa-typing-hold")
}

fn analyze() -> spa_partition::SpaPartitionReport {
    spa_partition::analyze(&repo_root(), &fixture_dir(), None)
        .unwrap_or_else(|e| panic!("analyze failed: {e}"))
}

fn pairs(r: &spa_partition::SpaPartitionReport) -> Vec<(String, String)> {
    spa_partition::hold_free_pairs(r, &spa_partition::GuardReads::NoGuard, false)
}

fn p(h: &str, c: &str) -> (String, String) {
    (h.to_string(), c.to_string())
}

#[test]
fn the_facts_the_table_is_built_from() {
    let r = analyze();
    let quiet: Vec<&str> = r.settle.quiet.iter().map(|s| s.as_str()).collect();
    for c in [
        "SetQuery",
        "SetNote",
        "RunSearch",
        "SaveNote",
        "SetCode",
        "RunLookup",
    ] {
        assert!(
            quiet.contains(&c),
            "`{c}` returns Cmd.none: it must be quiet; quiet = {quiet:?}"
        );
    }
    let subs: BTreeSet<String> = ["code", "lookedUp", "query", "searchedFor"]
        .iter()
        .map(|s| s.to_string())
        .collect();
    assert_eq!(
        r.settle.subscriptions_reads,
        Some(subs),
        "subscriptions reads both debounce pairs"
    );
    for h in ["RunSearch", "SaveNote", "RunLookup"] {
        let b = r.branches.iter().find(|b| b.msg == h).expect("branch");
        assert!(
            b.server,
            "`{h}` writes server data into the model: a server branch"
        );
    }
}

#[test]
fn only_commuting_input_msgs_are_hold_free() {
    let r = analyze();
    let got = pairs(&r);
    assert_eq!(
        got,
        vec![
            p("RunLookup", "SetNote"),
            p("RunSearch", "SetNote"),
            p("SaveNote", "SetCode"),
            p("SaveNote", "SetNote"),
            p("SaveNote", "SetQuery")
        ],
        "SetQuery and SetCode must wait for RunSearch (subscriptions reads a field each writes)"
    );
}

#[test]
fn anything_the_analysis_cannot_read_lists_nothing() {
    let r = analyze();
    assert!(
        spa_partition::hold_free_pairs(&r, &spa_partition::GuardReads::Whole, false).is_empty(),
        "a guard reads the model before every Msg: nothing is hold-free"
    );
    assert!(
        spa_partition::hold_free_pairs(
            &r,
            &spa_partition::GuardReads::Fields(BTreeSet::new()),
            false
        )
        .is_empty(),
        "any guard: nothing is hold-free"
    );
    assert!(
        spa_partition::hold_free_pairs(&r, &spa_partition::GuardReads::NoGuard, true).is_empty(),
        "App.withRpcError runs in Applied<Msg>: nothing is hold-free"
    );
    let mut no_subs = analyze();
    no_subs.settle.subscriptions_reads = None;
    assert!(
        pairs(&no_subs).is_empty(),
        "unreadable subscriptions: nothing is hold-free"
    );

    let mut loud = analyze();
    loud.settle.quiet.remove("SetNote");
    assert_eq!(
        pairs(&loud),
        vec![p("SaveNote", "SetCode"), p("SaveNote", "SetQuery")],
        "an input arm with a command is never hold-free"
    );

    let mut unread = analyze();
    unread.settle.continuations.insert("SaveNote".into(), None);
    assert_eq!(
        pairs(&unread),
        vec![p("RunLookup", "SetNote"), p("RunSearch", "SetNote")],
        "a hold whose follow-ups cannot be read frees nothing"
    );

    let mut whole = analyze();
    for b in whole.branches.iter_mut().filter(|b| b.msg == "RunSearch") {
        b.io.as_mut().unwrap().writes_whole_model = true;
    }
    assert_eq!(
        pairs(&whole),
        vec![
            p("RunLookup", "SetNote"),
            p("SaveNote", "SetCode"),
            p("SaveNote", "SetNote"),
            p("SaveNote", "SetQuery")
        ],
        "a hold that rewrites the whole model frees nothing"
    );

    let mut follow = analyze();
    follow
        .settle
        .continuations
        .insert("SaveNote".into(), Some(vec!["SetNote".into()]));
    assert_eq!(
        pairs(&follow),
        vec![
            p("RunLookup", "SetNote"),
            p("RunSearch", "SetNote"),
            p("SaveNote", "SetCode"),
            p("SaveNote", "SetQuery")
        ],
        "a follow-up of SaveNote that writes `note` blocks SetNote during SaveNote"
    );
}

// The client config carrying the table, and the wasm client building with
// it, is the build test `spa_hold_free_table_reaches_the_client`
// (rust/crates/sky/tests/spa_split_flow.rs).

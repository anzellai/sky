//! Acceptance test for `sky spa-partition` (Phase 1 of the Sky.Spa auto-split).
//!
//! Runs the read-only partition analysis over the crafted fixture under
//! `tests/fixtures/spa-partition` — which type-checks clean and exercises every
//! class — and asserts the EXACT client/server classification:
//!
//!   * `Inc` / `AddN`  → CLIENT (pure).
//!   * `UseBoot`       → SERVER (references the `Task.run (System.getenv …)` CAF).
//!   * `UseMode`       → SERVER (references the `System.getenvOr` pure-typed env
//!                       read — SEED 2b, caught only by kernel identity).
//!   * server-tainted top-level bindings = { bootId, mode }.
//!
//! This needs only the in-repo Sky stdlib source (no Go toolchain, no network),
//! so it is not a `live_gate` test.

use std::path::PathBuf;

fn repo_root() -> PathBuf {
    // CARGO_MANIFEST_DIR = <repo>/rust/crates/sky
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("..")
        .join("..")
        .join("..")
        .canonicalize()
        .expect("repo root")
}

fn fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-partition")
}

fn compose_fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/spa-partition-compose")
}

#[test]
fn fixture_partitions_into_the_expected_client_server_split() {
    let report = project::spa_partition::analyze(&repo_root(), &fixture_dir(), Some("Main"))
        .expect("analysis should succeed on a clean-typechecking fixture");

    assert_eq!(report.update_name.as_deref(), Some("Main.update"));
    assert!(report.whole_update.is_none(), "per-branch must be available");
    assert_eq!(report.branches.len(), 4, "four Msg branches");

    let find = |prefix: &str| {
        report
            .branches
            .iter()
            .find(|b| b.msg == prefix || b.msg.starts_with(&format!("{prefix} ")))
            .unwrap_or_else(|| panic!("branch {prefix} not found; got {:?}",
                report.branches.iter().map(|b| &b.msg).collect::<Vec<_>>()))
    };

    // Pure client-local transitions.
    assert!(!find("Inc").server, "Inc must be CLIENT");
    assert!(!find("AddN").server, "AddN must be CLIENT");

    // Effectful-origin-referencing branches.
    let use_boot = find("UseBoot");
    assert!(use_boot.server, "UseBoot must be SERVER");
    assert!(
        use_boot.reason.contains("bootId") && use_boot.reason.contains("System.getenv"),
        "UseBoot reason should name the CAF + its origin: {}",
        use_boot.reason
    );

    let use_mode = find("UseMode");
    assert!(use_mode.server, "UseMode must be SERVER");
    assert!(
        use_mode.reason.contains("mode") && use_mode.reason.contains("System.getenvOr"),
        "UseMode reason should name the env value + its origin: {}",
        use_mode.reason
    );

    // Server-tainted top-level bindings = exactly { bootId, mode }.
    let mut names: Vec<&str> = report.tainted.iter().map(|t| t.name.as_str()).collect();
    names.sort();
    assert_eq!(names, vec!["bootId", "mode"], "tainted bindings");
}

/// Msg-constant precision: an arm that COMPOSES another arm by calling
/// `update <LiteralMsg> model` inherits THAT arm's verdict, not `update`-as-a-
/// whole. A NON-arm helper that calls `update` stays conservative (server).
#[test]
fn compose_fixture_partitions_with_msg_constant_precision() {
    let report =
        project::spa_partition::analyze(&repo_root(), &compose_fixture_dir(), Some("Main"))
            .expect("analysis should succeed on a clean-typechecking fixture");

    assert_eq!(report.update_name.as_deref(), Some("Main.update"));
    assert!(report.whole_update.is_none(), "per-branch must be available");
    assert_eq!(report.branches.len(), 5, "five Msg branches");

    let find = |prefix: &str| {
        report
            .branches
            .iter()
            .find(|b| b.msg == prefix || b.msg.starts_with(&format!("{prefix} ")))
            .unwrap_or_else(|| {
                panic!(
                    "branch {prefix} not found; got {:?}",
                    report.branches.iter().map(|b| &b.msg).collect::<Vec<_>>()
                )
            })
    };

    // Base arms: DoServer references the effectful-origin CAF → SERVER; DoPure
    // is pure → CLIENT.
    assert!(find("DoServer").server, "DoServer must be SERVER");
    assert!(!find("DoPure").server, "DoPure must be CLIENT");

    // ComposeServer scoped-calls `update DoServer model` — composes a server arm.
    let compose_server = find("ComposeServer");
    assert!(compose_server.server, "ComposeServer must be SERVER");
    assert!(
        compose_server.reason.contains("composes DoServer"),
        "ComposeServer reason should name the composed arm: {}",
        compose_server.reason
    );

    // THE PRECISION WIN: ComposePure scoped-calls `update DoPure model` — DoPure
    // is pure, so ComposePure must NOT be over-marked server via `update`-as-a-
    // whole (the bug this feature fixes).
    assert!(
        !find("ComposePure").server,
        "ComposePure must be CLIENT (composes a pure arm); got reason: {}",
        find("ComposePure").reason
    );

    // SOUNDNESS: ViaHelper calls a NON-arm helper `h` that itself calls
    // `update DoServer model`. Helper-calls-update keeps the conservative
    // treatment → the helper is server-tainted → ViaHelper must stay SERVER.
    assert!(
        find("ViaHelper").server,
        "ViaHelper must stay SERVER (helper-calls-update is conservative); got reason: {}",
        find("ViaHelper").reason
    );

    // Exactly three server, two client branches.
    let server = report.branches.iter().filter(|b| b.server).count();
    let client = report.branches.iter().filter(|b| !b.server).count();
    assert_eq!((server, client), (3, 2), "3 SERVER / 2 CLIENT");
}

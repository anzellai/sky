//! The Sky.Spa split type-checks a program once.
//!
//! When a continuation's argument has no wire codec, the split analyses the
//! program a second time with that continuation kept on the server. Both
//! analyses read the same db, so they share one type check. This file holds
//! one test on purpose: it reads the process-wide front-end counters
//! (`timings::front_end_counts`), which a parallel test in the same binary
//! would move.

use project::{spa_split, timings};
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

#[test]
fn a_continuation_kept_on_the_server_does_not_check_the_program_twice() {
    // `Got (Result Error Status)` has no wire codec, so `Got` stays in
    // `Fetch`'s server chain (`spa_server_chain.rs` asserts the routing).
    let base = repo_root().join("rust/crates/sky/tests/fixtures/spa-client-crypto-ssr");
    let fixture = std::env::temp_dir().join(format!("sky-spa-check-once-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&fixture);
    std::fs::create_dir_all(fixture.join("src")).unwrap();
    std::fs::copy(base.join("sky.toml"), fixture.join("sky.toml")).unwrap();
    let src = std::fs::read_to_string(base.join("src/Main.sky"))
        .unwrap()
        .replacen(
            "    | Got (Result Error Http.HttpResponse)\n",
            "    | Got (Result Error Status)\n\n\ntype Status\n    = Up Int\n    | Down\n",
            1,
        )
        .replacen(
            "(Http.get \"http://127.0.0.1:9/pub\")",
            "(Http.get \"http://127.0.0.1:9/pub\" |> Task.map (\\r -> Up r.status))",
            1,
        );
    assert!(src.contains("type Status") && src.contains("Up r.status"));
    std::fs::write(fixture.join("src/Main.sky"), src).unwrap();

    let before = timings::front_end_counts();
    let built = spa_split::analyze_project(&repo_root(), &fixture, None)
        .unwrap_or_else(|e| panic!("analyze_project failed: {e}"));
    let after = timings::front_end_counts();
    assert!(
        built.server_internal.contains(&"Got".to_string()),
        "the fixture must keep `Got` on the server (the second analysis); got {:?}",
        built.server_internal
    );
    assert_eq!(
        (after.0 - before.0, after.1 - before.1),
        (1, 1),
        "one source load and one type check for both analyses"
    );
    let _ = std::fs::remove_dir_all(&fixture);
}

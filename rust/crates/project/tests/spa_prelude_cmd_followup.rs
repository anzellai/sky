//! `Cmd` reached through the PRELUDE must be analysed exactly like an imported
//! `Std.Cmd`.
//!
//! Through the prelude, `Cmd.perform` / `Cmd.batch` / `Cmd.none` resolve to
//! `Res::Kernel { Cmd, … }`, not to `Std.Cmd` defs. The command-leaf resolver
//! only knew the def form, so every such command read as opaque: a server branch
//! then lost its chaining, and every `Msg` constructor was treated as a possible
//! follow-up (the split demanded a wire codec for all of them and failed the
//! build on an unrelated constructor). Every earlier split fixture imported
//! `Std.Cmd`, which hid it.
//!
//! `spa-prelude-cmd` is `spa-multihop-chain` with the `import Std.Cmd as Cmd`
//! line removed. The two analyses must agree.

use project::spa_partition;
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

fn analyze(name: &str) -> spa_partition::SpaPartitionReport {
    let proj = repo_root()
        .join("rust/crates/sky/tests/fixtures")
        .join(name);
    spa_partition::analyze(&repo_root(), &proj, None)
        .unwrap_or_else(|e| panic!("analyze {name} failed: {e}"))
}

#[test]
fn prelude_cmd_is_analysed_like_imported_std_cmd() {
    let imported = analyze("spa-multihop-chain");
    let prelude = analyze("spa-prelude-cmd");
    assert_eq!(
        prelude.chaining_branches, imported.chaining_branches,
        "chaining roots differ when `Cmd` comes from the prelude"
    );
    assert_eq!(
        prelude.server_internal, imported.server_internal,
        "server-internal Msgs differ when `Cmd` comes from the prelude"
    );
    assert_eq!(
        format!("{:?}", prelude.follow_up),
        format!("{:?}", imported.follow_up),
        "follow-up branches differ when `Cmd` comes from the prelude"
    );
    assert!(
        prelude.chaining_branches.contains(&"Kick".to_string()),
        "`Kick` must still chain with a prelude `Cmd`; got {:?}",
        prelude.chaining_branches
    );
}

//! SA-12: an `update` with no top-level `case msg of`.
//!
//! * Wholly PURE (`update (Line l) model = …`): every Msg runs in the client, so
//!   the split keeps `update` as written — it used to refuse the whole app
//!   ("`update` has no resolvable `case msg of`").
//! * Reaching a server effect: the split cannot route it per Msg, so it fails
//!   with a message that names the change (write `case msg of`), not a bare
//!   "per-branch analysis unavailable".

use project::spa_split;
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

fn fixture(name: &str) -> PathBuf {
    repo_root()
        .join("rust/crates/sky/tests/fixtures")
        .join(name)
}

fn out_dir(tag: &str) -> PathBuf {
    let out = std::env::temp_dir().join(format!("sky-spa-nocase-{}-{tag}", std::process::id()));
    let _ = std::fs::remove_dir_all(&out);
    out
}

#[test]
fn a_pure_update_without_case_splits_and_keeps_update_verbatim() {
    let out = out_dir("pure");
    spa_split::generate(
        &repo_root(),
        &fixture("spa-update-no-case-pure"),
        None,
        &out,
        None,
        None,
    )
    .unwrap_or_else(|e| panic!("a pure no-case update must split: {e}"));
    let front = std::fs::read_to_string(out.join("frontend/src/Main.sky")).unwrap();
    assert!(
        front.contains("update (Line l) model ="),
        "the pure `update` must be kept verbatim in the client:\n{front}"
    );
    assert!(
        !front.contains("/_rpc/"),
        "no Msg reaches the server:\n{front}"
    );
    let _ = std::fs::remove_dir_all(&out);
}

#[test]
fn an_effectful_update_without_case_fails_naming_the_change() {
    let out = out_dir("effect");
    let err = spa_split::generate(
        &repo_root(),
        &fixture("spa-update-no-case-effect"),
        None,
        &out,
        None,
        None,
    )
    .err()
    .expect("an effectful no-case update cannot be split per Msg");
    assert!(
        err.contains("has no top-level `case msg of`")
            && err.contains("update msg model = case msg of"),
        "the error must name the change to make; got: {err}"
    );
    let _ = std::fs::remove_dir_all(&out);
}

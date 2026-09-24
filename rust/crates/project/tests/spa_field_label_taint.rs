//! A record field label that shares its name with a server-tainted binding is
//! not a reference to it. `init`'s model `{ blank | products = "" }` beside a
//! server-only `products` binding must still get the client model decoder:
//! `Spa.withModelDecoder` is what boots the client from the server's SSR seed
//! and restores its saved state. Before this test, a textual word match dropped
//! the decoder, and a real shop showed its products for half a second and then
//! repainted empty on every full page load.

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

#[test]
fn a_field_label_named_like_a_tainted_binding_keeps_the_model_decoder() {
    let fixture = repo_root().join("rust/crates/sky/tests/fixtures/spa-field-label-taint");
    let out = std::env::temp_dir().join(format!("sky-spa-field-label-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&out);
    spa_split::generate(&repo_root(), &fixture, None, &out, None, None)
        .unwrap_or_else(|e| panic!("the fixture must split: {e}"));
    let front = std::fs::read_to_string(out.join("frontend/src/Main.sky")).unwrap();
    assert!(
        front.contains("Spa.withModelDecoder spaModelDecoder_"),
        "`products` in `{{ blank | products = \"\" }}` is a field label, not the tainted \
         `products` binding; the client must keep its model decoder:\n{front}"
    );
    assert!(
        !front.contains("System.getenvOr"),
        "the tainted binding itself must still stay out of the client:\n{front}"
    );
    let _ = std::fs::remove_dir_all(&out);
}

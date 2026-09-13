//! `project::diagram::scaffold_mocks` — the outbound-HTTP inventory behind
//! `sky test --scaffold-mocks`.
//!
//! Read-only: it loads the same source db the build assembles, resolves the HIR,
//! and walks each project module for outbound `Sky.Core.Http` calls. It never
//! type-checks beyond the shared load, lowers, `go build`s, or writes a file
//! (the CLI writes the fixtures; this analysis only finds the calls).
//!
//! The regression this guards: the URL argument is almost never a bare string
//! literal in real code. It arrives via a `|>` pipeline (an `Expr::Binop`, NOT a
//! `Call`) and as a `base ++ "/path"` concat (a `++` whose literal suffix is the
//! host-independent match). An early version that only matched `Call` nodes with
//! a literal first argument found NOTHING on darraghstudio's Stripe calls.

use project::diagram::scaffold_mocks;
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

#[test]
fn finds_direct_get_and_piped_request_builder() {
    let root = repo_root();
    let dir = root.join("rust/crates/sky/tests/fixtures/scaffold-http");
    let r = scaffold_mocks(&root, &dir, None)
        .unwrap_or_else(|e| panic!("scaffold_mocks failed: {e}"));

    // The direct `Http.get "https://api.example.com/v1/things"` — a full literal.
    let get = r
        .calls
        .iter()
        .find(|c| c.method == "GET")
        .unwrap_or_else(|| panic!("no GET call found; got {:?}", r.calls));
    assert_eq!(
        get.url.as_deref(),
        Some("https://api.example.com/v1/things"),
        "the direct get URL literal must be recovered whole"
    );

    // The piped builder `defaultRequest (apiBase ++ "/charges") |> withMethod
    // "POST" |> request`: method from `withMethod`, URL from the `++` concat's
    // literal suffix (host-independent), NOT the `apiBase` var.
    let post = r
        .calls
        .iter()
        .find(|c| c.method == "POST")
        .unwrap_or_else(|| panic!("no POST call found; got {:?}", r.calls));
    assert_eq!(
        post.url.as_deref(),
        Some("/charges"),
        "the builder URL must be the literal path suffix of the `++` concat"
    );
}

/// A project that makes no outbound HTTP call yields an empty inventory with a
/// note — not an error, and not a spurious fixture.
#[test]
fn no_http_calls_is_empty_with_a_note() {
    let root = repo_root();
    // spa-derived-read is a pure Spa app: view/update, no outbound Http.
    let dir = root.join("rust/crates/sky/tests/fixtures/spa-derived-read");
    let r = scaffold_mocks(&root, &dir, None)
        .unwrap_or_else(|e| panic!("scaffold_mocks failed: {e}"));
    assert!(r.calls.is_empty(), "no outbound Http here; got {:?}", r.calls);
    assert!(!r.notes.is_empty(), "an empty inventory must carry an explanatory note");
}

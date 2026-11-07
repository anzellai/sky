//! Value-position Nav.scrollToTop must construct a Cmd, never leave a Go func
//! inside the erased Cmd slot. Exercise direct, CAF and batch lowering.
use std::path::{Path, PathBuf};

fn repo_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .ancestors()
        .nth(3)
        .unwrap()
        .to_path_buf()
}

fn function_body<'a>(source: &'a str, name: &str) -> &'a str {
    let head = format!("func {name}(");
    let start = source
        .find(&head)
        .unwrap_or_else(|| panic!("missing emitted function {name}"));
    let rest = &source[start..];
    let end = rest[1..]
        .find("\nfunc ")
        .into_iter()
        .chain(rest[1..].find("\nvar "))
        .min()
        .map(|at| at + 1)
        .unwrap_or(rest.len());
    &rest[..end]
}

#[test]
fn nav_scroll_to_top_is_invoked_in_direct_caf_and_batch_value_slots() {
    let dir = std::env::temp_dir().join(format!(
        "sky-nav-nullary-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(dir.join("sky.toml"), "[project]\nname=\"nav-nullary\"\n").unwrap();
    std::fs::write(dir.join("src/Main.sky"), r#"module Main exposing (main)
import Sky.Core.Prelude exposing (..)
import Sky.Core.List as List
import Std.Nav as Nav
import Std.Log exposing (println)

direct : () -> Cmd msg
direct _ =
    Nav.scrollToTop

cachedTop : Cmd msg
cachedTop =
    Nav.scrollToTop

batchedTop : () -> Cmd msg
batchedTop _ =
    Cmd.batch [ Cmd.none, Nav.scrollToTop, cachedTop, Nav.clearFragment ]

applyScroll : (String -> Cmd msg) -> Cmd msg
applyScroll f =
    f "target"

main : Task Error ()
main =
    println (String.fromInt (List.length [ direct (), cachedTop, batchedTop (), applyScroll Nav.scrollIntoView, applyScroll Nav.scrollIntoViewIfNeeded ]))
"#).unwrap();
    let emitted = project::emit_example_source(&repo_root(), &dir);
    let _ = std::fs::remove_dir_all(&dir);
    let go = emitted.unwrap_or_else(|error| panic!("valid nullary Nav fixture failed: {error}"));
    for function in ["Main_direct", "Main_cachedTop__cafInit", "Main_batchedTop"] {
        assert!(
            function_body(&go, function).contains("rt.Nav_scrollToTop()"),
            "{function}: zero-argument Cmd kernel must be INVOKED, not stored as a function"
        );
    }
    for (at, _) in go.match_indices("rt.Nav_scrollToTop") {
        assert!(
            go[at + "rt.Nav_scrollToTop".len()..].starts_with("()"),
            "bare zero-argument Nav kernel escaped into an erased value slot"
        );
    }
    assert!(
        !go.contains("rt.Nav_scrollIntoView()"),
        "one-argument scroll must remain a callable function value"
    );
    assert!(
        !go.contains("rt.Nav_scrollIntoViewIfNeeded()"),
        "one-argument visibility scroll must remain a callable function value"
    );
    assert!(
        go.contains("rt.Cmd_none()"),
        "existing nullary kernel control must remain invoked"
    );
    assert!(go.contains("rt.Cmd_batch("));
    assert!(
        go.contains("Std_Nav_clearFragment()"),
        "ordinary Sky nullary CAF control must remain called"
    );
}

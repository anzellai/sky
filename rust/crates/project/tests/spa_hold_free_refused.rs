//! `Spa.withHoldFree` is generated-only (v0.29.0). It tells the Sky.Spa client
//! that an input Msg may run while a hold RPC is in flight. The split lists a
//! pair only when it proves the two Msgs commute
//! (`spa_partition::hold_free_pairs`); a hand-written call could list an
//! unsafe pair and bring back the reordering the hold exists to prevent. So a
//! reference to it is refused with `[E1011]` in every project the split did
//! not generate (`[spa] generated = true`).

use project::{build_example, BuildOptions};
use std::path::{Path, PathBuf};

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

const MAIN: &str = "module Main exposing (main)\n\n\
import Sky.Core.Prelude exposing (..)\n\
import Sky.Core.Task as Task\n\
import Std.Spa as Spa\n\n\n\
free cfg =\n    Spa.withHoldFree [ \"Save Type\" ] cfg\n\n\n\
main =\n    Task.succeed ()\n";

fn project(tag: &str, toml_extra: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "sky-hold-free-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        format!(
            "name = \"hold-free\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n{toml_extra}"
        ),
    )
    .unwrap();
    std::fs::write(dir.join("src/Main.sky"), MAIN).unwrap();
    dir
}

fn build(repo: &Path, dir: &Path) -> String {
    let out = dir.join("sky-out-test");
    build_example(&BuildOptions {
        repo_root: repo.to_path_buf(),
        example_dir: dir.to_path_buf(),
        out_dir_name: "sky-out-test".to_string(),
        out_dir_abs: Some(out),
        run: false,
        stdin: None,
        entry_module: None,
        progress: false,
        embed_bundle: None,
        wasm: false,
    })
    .note
}

#[test]
fn a_hand_written_with_hold_free_is_refused() {
    let repo = repo_root();
    let dir = project("app", "");
    let note = build(&repo, &dir);
    assert!(
        note.contains("[E1011]") && note.contains("Spa.withHoldFree") && note.contains("generated"),
        "a hand-written Spa.withHoldFree must be refused with [E1011]; got: {note}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn the_split_generated_project_may_use_it() {
    let repo = repo_root();
    let dir = project("gen", "\n[spa]\ngenerated = true\nrole = \"frontend\"\n");
    let note = build(&repo, &dir);
    assert!(
        !note.contains("withHoldFree"),
        "the split's own output must not be refused; got: {note}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

/// A hand-written `Spa.app` whose config already calls `Spa.withHoldFree`: the
/// split refuses it rather than keep the user's list or merge its own into it.
/// The split type-checks the source first, so the checker's [E1011] stops it;
/// `gen_frontend` also refuses a `Spa.withHoldFree` it finds in `main`, as a
/// second guard that does not depend on that order.
const SPA_MAIN: &str = "module Main exposing (main)\n\n\
import Sky.Core.Prelude exposing (..)\n\
import Sky.Core.Task as Task\n\
import Std.Spa as Spa\n\
import Std.Cmd as Cmd\n\
import Std.Sub as Sub\n\
import Std.Html as Html exposing (Html)\n\n\n\
type alias Model =\n    { text : String }\n\n\n\
type Msg\n    = Type String\n\n\n\
init : () -> ( Model, Cmd Msg )\n\
init _ =\n    ( { text = \"\" }, Cmd.none )\n\n\n\
update : Msg -> Model -> ( Model, Cmd Msg )\n\
update msg model =\n    case msg of\n        Type s ->\n            ( { model | text = s }, Cmd.none )\n\n\n\
view : Model -> Html Msg\n\
view model =\n    Html.text model.text\n\n\n\
subscriptions : Model -> Sub Msg\n\
subscriptions _ =\n    Sub.none\n\n\n\
main : Task Error ()\n\
main =\n    Spa.app\n        (Spa.config\n            { init = init\n            , update = update\n            , view = view\n            , subscriptions = subscriptions\n            }\n            |> Spa.withHoldFree [ \"Save Type\" ]\n        )\n";

#[test]
fn the_split_never_keeps_a_source_hold_free_list() {
    let repo = repo_root();
    let dir = project("split", "");
    std::fs::write(dir.join("src/Main.sky"), SPA_MAIN).unwrap();
    let out = dir.join("split-out");
    let res = project::spa_split::generate(&repo, &dir, None, &out, None, None);
    match res {
        Err(e) => assert!(
            e.contains("[E1011]") && e.contains("Spa.withHoldFree"),
            "the split must refuse a source Spa.withHoldFree with [E1011]; got: {e}"
        ),
        Ok(_) => {
            let front =
                std::fs::read_to_string(out.join("frontend/src/Main.sky")).unwrap_or_default();
            panic!("the split kept a hand-written Spa.withHoldFree:\n{front}");
        }
    }
    let note = build(&repo, &dir);
    assert!(
        note.contains("[E1011]") && note.contains("Spa.withHoldFree"),
        "the checker refuses the same source; got: {note}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

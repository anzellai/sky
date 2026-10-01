//! Every loud v0.27.0 break that surfaces as a type error names the change,
//! shows the fix, and ends with its migration-guide anchor
//! (`see docs/migration/v0.27.md#<anchor>`).

use std::path::PathBuf;
use std::sync::OnceLock;

fn stdlib() -> &'static Vec<(String, syntax::Parse)> {
    static S: OnceLock<Vec<(String, syntax::Parse)>> = OnceLock::new();
    S.get_or_init(|| {
        let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
        while !dir.join("sky-stdlib").is_dir() {
            assert!(dir.pop(), "no sky-stdlib ancestor");
        }
        ty::reject_corpus::load_stdlib(&dir)
    })
}

const HEADER: &str = "module Main exposing (main)\n\n\
    import Sky.Core.Prelude exposing (..)\n\
    import Std.App as App\n\
    import Std.Ui as Ui exposing (Element)\n\
    import Std.Spa as Spa\n\
    import Std.Codec as Codec exposing (Codec)\n\n\
    type alias Model =\n    { n : Int }\n\n\
    type Msg\n    = Got (Result Error Int)\n\n";

/// The `[E2001]` hint for `body` appended to [`HEADER`].
fn hint(body: &str) -> String {
    let src = format!("{HEADER}{body}");
    let mut db = hir::SourceDb::new();
    for (n, p) in stdlib() {
        db.add_module(n, p.clone());
    }
    let m = db.add_module("Main", syntax::parse(&src, base::FileId(0)));
    let out = ty::check_modules(&db, &[m]);
    let d = out
        .diagnostics
        .iter()
        .find(|d| d.code.0 == "E2001")
        .unwrap_or_else(|| panic!("an E2001: {:?}", out.diagnostics));
    d.suggestion.clone().unwrap_or_default()
}

fn assert_link(h: &str, fix: &str, anchor: &str) {
    assert!(
        h.contains("since v0.27.0")
            && h.contains(fix)
            && h.ends_with(&format!("see docs/migration/v0.27.md#{anchor}")),
        "{anchor}: {h:?}"
    );
}

#[test]
fn init_taking_a_page_links_init_takes_unit() {
    let h = hint(
        "init : String -> ( Model, Cmd Msg )\ninit _ =\n    ( { n = 0 }, Cmd.none )\n\n\
         main =\n    App.app { init = init, update = \\_ m -> ( m, Cmd.none ), \
         view = \\_ -> Ui.none, subscriptions = \\_ -> Sub.none } |> App.run\n",
    );
    assert_link(&h, "init : () -> ( Model, Cmd Msg )", "init-takes-unit");
}

#[test]
fn a_spa_rpc_body_lambda_links_spa_rpc_body_value() {
    let h = hint(
        "c : Codec Int\nc =\n    Codec.int\n\n\
         cmd : Model -> Cmd Msg\ncmd model =\n    Spa.rpc c c \"/x\" (\\m -> m.n) Got\n\n\
         main =\n    cmd\n",
    );
    assert_link(
        &h,
        "Spa.rpc bodyCodec respCodec url body toMsg",
        "spa-rpc-body-value",
    );
}

#[test]
fn a_webopts_literal_missing_the_new_fields_links_webopts_record_literals() {
    let h = hint(
        "opts : App.WebOpts\nopts =\n    { port = 8000, store = Nothing, static = Nothing }\n\n\
         main =\n    opts\n",
    );
    assert_link(&h, "App.webDefaults", "webopts-record-literals");
}

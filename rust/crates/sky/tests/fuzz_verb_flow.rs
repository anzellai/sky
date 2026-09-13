//! End-to-end coverage for `sky fuzz` — the model-based no-panic fuzzer.
//!
//! The unit tests in `project::build::offline_db_plan_tests` gate the DB-plan
//! CLASSIFICATION without a Go toolchain. This file is the e2e leg: it drives the
//! real `sky` binary through the whole `generate_model_fuzz -> build -> run under
//! test mode` path, so the properties asserted are the ones a user gets.
//!
//! Two properties, both regressions:
//!
//!   * A real DB-backed SQLite app FUZZES OFFLINE. The substrate used to force
//!     `SKY_EMBED_POSTGRES` onto ANY `[database]` project; a SQLite app carries a
//!     compiled `<PREFIX>_DB_PATH`, and embed + a DSN is a conflict the runtime
//!     REFUSES, so the app never booted and `sky fuzz` FAILED before folding a
//!     single Msg (found by an adversarial Judge on `examples/12-skyvote`). The
//!     engine now decides the plan: a SQLite app has its path redirected to a
//!     scratch file instead.
//!
//!   * The net actually CATCHES a bug. A `sky fuzz` that reports PASS on an app
//!     whose `update` panics is worthless. An app whose branch hits a classified
//!     `DivisionByZero` must FAIL with a non-zero exit — otherwise the whole
//!     feature is vacuous.
//!
//! When no Go toolchain is discoverable the live gate FAILS (naming what to
//! install) rather than skipping; `SKY_LIVE_TESTS=skip` is the one opt-out.

use std::path::{Path, PathBuf};
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

fn scratch(tag: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "sky-fuzzverb-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(dir.join("src")).unwrap();
    dir
}

/// Scaffold a minimal Sky.Spa app. `sky_toml` and the `Main.sky` body are given
/// so each test controls the `[database]` block and the `update`.
fn project(tag: &str, sky_toml: &str, main_body: &str) -> PathBuf {
    let dir = scratch(tag);
    std::fs::write(dir.join("sky.toml"), sky_toml).unwrap();
    std::fs::write(dir.join("src").join("Main.sky"), main_body).unwrap();
    dir
}

/// Run `sky fuzz src/Main.sky --iters N` from `dir` with `DATABASE_URL` cleared
/// (so the offline-DB substrate is what provisions any database). Returns
/// (exit, stdout+stderr).
fn run_fuzz(dir: &Path, iters: u32) -> (i32, String) {
    let out = Command::new(SKY)
        .arg("fuzz")
        .arg("src/Main.sky")
        .arg("--iters")
        .arg(iters.to_string())
        .current_dir(dir)
        .env_remove("DATABASE_URL")
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky fuzz");
    let mut s = String::from_utf8_lossy(&out.stdout).into_owned();
    s.push_str(&String::from_utf8_lossy(&out.stderr));
    (out.status.code().unwrap_or(-1), s)
}

const SQLITE_TOML: &str = "name = \"fuzzsqlite\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n\
     [source]\nroot = \"src\"\n\n\
     [database]\ndriver = \"sqlite\"\npath = \"app.db\"\n";

const CLEAN_APP: &str = "module Main exposing (main)\n\n\
     import Sky.Core.Prelude exposing (..)\n\
     import Sky.Core.String as String\n\
     import Std.Spa as Spa\n\
     import Std.Cmd as Cmd\n\
     import Std.Sub as Sub\n\
     import Std.Ui as Ui\n\
     import Std.Html exposing (Html)\n\n\n\
     type alias Model =\n    { n : Int }\n\n\n\
     type Msg\n    = Bump\n    | Reset\n    | SetTo Int\n\n\n\
     init : () -> ( Model, Cmd Msg )\n\
     init _ =\n    ( { n = 0 }, Cmd.none )\n\n\n\
     update : Msg -> Model -> ( Model, Cmd Msg )\n\
     update msg model =\n    case msg of\n        \
     Bump ->\n            ( { model | n = model.n + 1 }, Cmd.none )\n\n        \
     Reset ->\n            ( { model | n = 0 }, Cmd.none )\n\n        \
     SetTo k ->\n            ( { model | n = k }, Cmd.none )\n\n\n\
     view : Model -> Html Msg\n\
     view model =\n    Ui.layout [] (Ui.column [] [ Ui.text (String.fromInt model.n) ])\n\n\n\
     subscriptions : Model -> Sub Msg\n\
     subscriptions _ =\n    Sub.none\n\n\n\
     main : Task Error ()\n\
     main =\n    Spa.app (Spa.config { init = init, update = update, view = view, subscriptions = subscriptions })\n";

/// A branch that hits a well-typed but classified `DivisionByZero`. The fuzzer
/// must catch it.
const PANIC_APP: &str = "module Main exposing (main)\n\n\
     import Sky.Core.Prelude exposing (..)\n\
     import Sky.Core.String as String\n\
     import Std.Spa as Spa\n\
     import Std.Cmd as Cmd\n\
     import Std.Sub as Sub\n\
     import Std.Ui as Ui\n\
     import Std.Html exposing (Html)\n\n\n\
     type alias Model =\n    { n : Int, r : Float }\n\n\n\
     type Msg\n    = Bump\n    | Boom\n\n\n\
     init : () -> ( Model, Cmd Msg )\n\
     init _ =\n    ( { n = 0, r = 0.0 }, Cmd.none )\n\n\n\
     update : Msg -> Model -> ( Model, Cmd Msg )\n\
     update msg model =\n    case msg of\n        \
     Bump ->\n            ( { model | n = model.n + 1 }, Cmd.none )\n\n        \
     Boom ->\n            ( { model | r = 1.0 / (model.r - model.r) }, Cmd.none )\n\n\n\
     view : Model -> Html Msg\n\
     view model =\n    Ui.layout [] (Ui.column [] [ Ui.text (String.fromInt model.n) ])\n\n\n\
     subscriptions : Model -> Sub Msg\n\
     subscriptions _ =\n    Sub.none\n\n\n\
     main : Task Error ()\n\
     main =\n    Spa.app (Spa.config { init = init, update = update, view = view, subscriptions = subscriptions })\n";

/// A real DB-backed SQLite app fuzzes offline: the substrate redirects its path
/// to a scratch file instead of forcing embedded Postgres (which would collide
/// with the compiled `SKY_DB_PATH` and FAIL before any Msg is folded).
#[test]
fn sqlite_db_app_fuzzes_offline() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = project("sqlite", SQLITE_TOML, CLEAN_APP);
    let (code, out) = run_fuzz(&dir, 60);

    assert!(
        !out.contains("will not choose between them") && !out.contains("SKY_EMBED_POSTGRES"),
        "a SQLite app must NOT be forced onto embedded Postgres; output:\n{out}"
    );
    assert_eq!(code, 0, "a SQLite DB app must fuzz offline and PASS; output:\n{out}");
    assert!(out.contains("no unclassified panic"), "output:\n{out}");
    // The run must be ephemeral: never touch the project's real database file.
    assert!(
        !dir.join("app.db").exists(),
        "the fuzz run wrote the project's real DB file instead of a scratch one"
    );

    let _ = std::fs::remove_dir_all(&dir);
}

/// The converse — the net must catch a bug. An app whose branch divides by zero
/// must FAIL with a non-zero exit, or `sky fuzz` PASS means nothing.
#[test]
fn a_panicking_update_fails_the_fuzz() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let toml = "name = \"fuzzpanic\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n\
         [source]\nroot = \"src\"\n";
    let dir = project("panic", toml, PANIC_APP);
    let (code, out) = run_fuzz(&dir, 200);

    assert_ne!(
        code, 0,
        "an app whose update panics MUST fail the fuzz; a PASS here means the net \
         is vacuous. output:\n{out}"
    );
    assert!(
        out.contains("FAIL") && out.contains("DivisionByZero"),
        "the failure must be reported as a caught panic; output:\n{out}"
    );

    let _ = std::fs::remove_dir_all(&dir);
}

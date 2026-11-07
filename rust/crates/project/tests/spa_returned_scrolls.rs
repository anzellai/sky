//! Actual parsed/checked auto-split regressions, not source keyword grants.
use project::spa_split;
use std::path::{Path, PathBuf};
fn root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .ancestors()
        .nth(3)
        .unwrap()
        .to_path_buf()
}
fn fixture(tag: &str, source: &str) -> PathBuf {
    let p = std::env::temp_dir().join(format!("sky-returned-scroll-{tag}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&p);
    std::fs::create_dir_all(p.join("src")).unwrap();
    std::fs::write(p.join("sky.toml"), "[project]\nname=\"returned-scrolls\"\n").unwrap();
    std::fs::write(p.join("src/Main.sky"), source).unwrap();
    p
}
fn original() -> String {
    r#"module Main exposing (main)
import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Sky.Core.Time as Time
import Std.Spa as Spa
import Std.Html as Html
import Std.Nav as Nav

type alias Model = { page : String, saved : Int }
type Msg = Go | Save | Saved (Result Error Int)
init : () -> ( Model, Cmd Msg )
init _ =
    ( { page = "home", saved = 0 }, Cmd.none )
update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Go ->
            ( model, Nav.pushUrl "/about" )
        Save ->
            ( model, Cmd.perform (Time.sleep 10 |> Task.map (\_ -> 7)) Saved )
        Saved (Ok n) ->
            ( { model | saved = n }, Cmd.none )
        Saved (Err _) ->
            ( model, Cmd.none )
view : Model -> Html.Html Msg
view model =
    Html.div [] [ Html.text model.page ]
subscriptions : Model -> Sub Msg
subscriptions _ =
    Sub.none
main : Task Error ()
main =
    Spa.app (Spa.config { init = init, update = update, view = view, subscriptions = subscriptions })
"#.to_string()
}

fn split(tag: &str, source: &str) -> (PathBuf, String, String, String) {
    let dir = fixture(tag, source);
    let out = dir.join("generated");
    spa_split::generate(&root(), &dir, None, &out, None, None)
        .unwrap_or_else(|e| panic!("valid scroll fixture failed: {e}"));
    let front = std::fs::read_to_string(out.join("frontend/src/Main.sky")).unwrap();
    let back = std::fs::read_to_string(out.join("backend/src/Main.sky")).unwrap();
    let shared = std::fs::read_to_string(out.join("shared/Shared.sky"))
        .or_else(|_| std::fs::read_to_string(out.join("shared/src/Shared.sky")))
        .unwrap();
    (dir, front, back, shared)
}
#[test]
fn server_computed_scroll_and_helper_batch_return_after_response_not_send_time() {
    let mut source = original().replace(
        "import Sky.Core.Time as Time",
        "import Sky.Core.Time as Time\nimport Sky.Core.File as File",
    );
    let start = source.find("        Save ->").unwrap();
    let end = source[start..].find("        Saved (Ok n)").unwrap() + start;
    source.replace_range(start..end,"        Save ->\n            let\n                target =\n                    Result.withDefault \"part-top\" (Task.run (File.readFile \"data/target.txt\"))\n            in\n                ( { model | saved = String.length target }, scrollHelper target )\n\n");
    source.push_str("\n\nscrollHelper : String -> Cmd Msg\nscrollHelper id =\n    Cmd.batch [ Nav.scrollIntoView id, Nav.scrollIntoViewIfNeeded id, scrollTop ]\n\n\nscrollTop : Cmd Msg\nscrollTop =\n    Nav.scrollToTop\n");
    let (dir, front, back, shared) = split("server-id", &source);
    assert!(shared.contains("spaScroll_ : String"));
    assert!(shared.contains("returned-scroll-wire-v1"));
    assert!(back.contains("spaCollectScrolls_ cmd"));
    assert!(back.contains("Server.withStatus 409"));
    assert!(back.contains("(spaScrollWireGuard_ saveHandler)"));
    assert!(back.contains("File.readFile \"data/target.txt\""));
    assert!(
        !front.contains("File.readFile"),
        "server read escaped into frontend"
    );
    assert!(front.contains("Spa.rpcScrollHold"));
    assert!(front.contains("Spa.returnedScrolls resp.spaScroll_"));
    assert!(front.contains("( spaScrollModel_, Cmd.batch"));
    assert!(!front.contains("{ model | saved = String.length target }"));
    std::fs::remove_dir_all(dir).unwrap();
}
#[test]
fn safe_init_keeps_rpc_plan_but_ssr_owned_scrolls_have_a_versioned_boot_path() {
    let source=original().replace("import Sky.Core.Time as Time","import Sky.Core.Time as Time\nimport Sky.Core.File as File")
  .replace("saved = 0 }, Cmd.none )","saved = 0 }, Cmd.batch [ Cmd.perform (File.readFile \"data/target.txt\" |> Task.map String.length) Saved, Nav.scrollToTop ] )");
    assert!(source.contains("File.readFile"), "fixture premise");
    let (dir, front, back, shared) = split("init", &source);
    assert!(front.contains("/_rpc/SpaInit_"));
    assert!(front.contains("SpaInitRpc_.rpcScrollHold"));
    assert!(
        !front.contains("File.readFile"),
        "server-only init task escaped into frontend"
    );
    assert!(front.contains("Spa.readInitScrolls spaInitJson_"));
    assert!(back.contains("spaInitScrollResponse_ spaInitCmd_"));
    assert!(back.contains("spaSsrScrollSettle_"));
    assert!(back.contains("spaSsrPageScrolls_"));
    assert!(shared.contains("SSR boot command data"));
    std::fs::remove_dir_all(dir).unwrap();
}
#[test]
fn importing_old_url_nav_does_not_opt_into_scroll_wire() {
    let (dir, front, back, shared) = split("old-url", &original());
    assert!(!shared.contains("spaScroll_"));
    assert!(!shared.contains("returned-scroll-wire"));
    assert!(!front.contains("Spa.returnedScrolls"));
    assert!(!front.contains("Spa.scrollRpcScope"));
    assert!(!back.contains("spaCollectScrolls_"));
    assert!(front.contains("Nav.pushUrl"));
    std::fs::remove_dir_all(dir).unwrap();
}

// Activate the new wire through a genuine, reachable pure Cmd constructor that
// is deliberately discarded. Existing server command shapes stay unchanged,
// so this exercises their real emitter/apply paths under the new protocol.
fn activate_wire(mut source: String) -> String {
    let line = source.find('\n').unwrap();
    source.insert_str(line + 1, "import Std.Nav as ScrollWireFixtureNav_\n");
    let marker = "main =\n";
    let at = source.rfind(marker).unwrap() + marker.len();
    let body = source[at..]
        .lines()
        .map(|line| format!("    {line}\n"))
        .collect::<String>();
    source.truncate(at);
    source.push_str("    let\n        _ = ScrollWireFixtureNav_.scrollToTop\n    in\n");
    source.push_str(&body);
    source
}
#[test]
fn existing_client_result_follow_whole_model_chain_and_push_shapes_keep_the_sideband() {
    for name in [
        "spa-client-result",
        "spa-rc-followup",
        "spa-multihop-chain",
        "spa-push-counter",
    ] {
        let source = std::fs::read_to_string(
            root()
                .join("rust/crates/sky/tests/fixtures")
                .join(name)
                .join("src/Main.sky"),
        )
        .unwrap();
        let (dir, front, back, shared) = split(name, &activate_wire(source));
        assert!(
            shared.contains("spaScroll_ : String"),
            "{name}: missing response field"
        );
        assert!(
            shared.contains("SSR boot command data"),
            "{name}: missing protocol version binding"
        );
        assert!(
            front.contains("Spa.returnedScrolls resp.spaScroll_"),
            "{name}: missing post-render apply"
        );
        assert!(
            back.contains("spaCollectScrolls_"),
            "{name}: missing actual command collection"
        );
        if name == "spa-multihop-chain" {
            assert!(
                back.contains("( mFinal, cmdScroll_ )"),
                "chain residual was discarded"
            );
            assert!(back.contains("spaCollectScrolls_ cmdScroll_"));
        }
        if name == "spa-client-result" {
            assert!(back.contains("spaScroll_ = scrolls_"));
            assert!(front.contains("case resp.result of"));
        }
        if name == "spa-push-counter" {
            assert!(back.contains("spaInterpretPublish spaBroker cmd"));
        }
        std::fs::remove_dir_all(dir).unwrap();
    }
}

#[test]
fn no_write_server_scroll_holds_later_msgs_without_fake_model_io() {
    let source = original()
        .replace("import Sky.Core.Time as Time", "import Sky.Core.Time as Time\nimport Sky.Core.File as File")
        .replace("Go | Save |", "Go | Next | Other | Save |")
        .replace("        Save ->", "        Next ->\n            ( { model | page = \"next\" }, Cmd.none )\n        Other ->\n            ( model, Cmd.perform (File.readFile \"other.txt\" |> Task.map String.length) Saved )\n        Save ->")
        .replace("( model, Cmd.perform (Time.sleep 10 |> Task.map (\\_ -> 7)) Saved )", "( model, serverScroll () )");
    let source = source + "\n\nserverScroll : () -> Cmd Msg\nserverScroll _ =\n    Nav.scrollIntoView (Result.withDefault \"same-id\" (Task.run (File.readFile \"target.txt\")))\n";
    assert!(
        source.contains("( model, serverScroll () )"),
        "fixture must have no server model write"
    );
    let (dir, front, back, _) = split("scroll-order", &source);
    assert!(
        front.contains("Spa.rpcScrollHold"),
        "scroll-only RPC must hold even with an empty write set"
    );
    assert!(front.contains("page = \"next\""));
    assert!(
        front.contains("Spa.rpc "),
        "independent perform RPC must remain asynchronous"
    );
    assert!(back.contains("spaCollectScrolls_ cmd"));
    assert!(!back.contains("writesWholeModel"));
    std::fs::remove_dir_all(dir).unwrap();
}

#[test]
fn mixed_server_scroll_and_url_residual_capture_origin_before_navigation() {
    let mut source = original().replace(
        "import Sky.Core.Time as Time",
        "import Sky.Core.Time as Time\nimport Sky.Core.File as File",
    );
    let start = source.find("        Save ->").unwrap();
    let end = source[start..].find("        Saved (Ok n)").unwrap() + start;
    source.replace_range(start..end, "        Save ->\n            let\n                target =\n                    Result.withDefault \"old-id\" (Task.run (File.readFile \"target.txt\"))\n            in\n                ( model, Cmd.batch [ Nav.scrollIntoView target, Nav.pushUrl \"/about\" ] )\n\n");
    let (dir, front, back, _) = split("mixed-nav-scroll", &source);
    assert!(
        front.contains("Spa.scrollRpcScope (Cmd.batch ["),
        "mixed residual must capture origin before it navigates"
    );
    assert!(front.contains("Spa.rpcScrollHold"));
    assert!(
        front.contains("Nav.pushUrl \"/about\""),
        "URL residual must remain immediate and unchanged"
    );
    assert!(!front.contains("File.readFile"));
    assert!(back.contains("spaCollectScrolls_ cmd"));
    std::fs::remove_dir_all(dir).unwrap();
}

#[test]
fn init_mixed_scroll_and_url_residual_capture_origin_before_navigation() {
    let source = original()
        .replace("import Sky.Core.Time as Time", "import Sky.Core.Time as Time\nimport Sky.Core.File as File")
        .replace("saved = 0 }, Cmd.none )", "saved = 0 }, Cmd.batch [ Cmd.perform (File.readFile \"target.txt\" |> Task.map String.length) Saved, Nav.scrollToTop, Nav.pushUrl \"/about\" ] )");
    let (dir, front, back, _) = split("init-mixed-nav-scroll", &source);
    assert!(
        !front.contains("File.readFile"),
        "server-only init task escaped into frontend"
    );
    assert!(front.contains("SpaInitRpc_.scrollRpcScope (SpaInitCmd_.batch ["));
    assert!(front.contains("SpaInitRpc_.rpcScrollHold"));
    assert!(front.contains("Nav.pushUrl \"/about\""));
    assert!(back.contains("spaInitScrollResponse_ spaInitCmd_"));
    std::fs::remove_dir_all(dir).unwrap();
}

#[test]
fn canonical_server_kernel_init_without_scroll_stays_on_server() {
    let source = original()
        .replace("import Sky.Core.Time as Time", "import Sky.Core.Time as Time\nimport Sky.Core.File as File")
        .replace("saved = 0 }, Cmd.none )", "saved = 0 }, Cmd.perform (File.readFile \"target.txt\" |> Task.map String.length) Saved )");
    let (dir, front, back, shared) = split("plain-server-init", &source);
    assert!(front.contains("/_rpc/SpaInit_"));
    assert!(
        !front.contains("File.readFile"),
        "direct canonical server init alias must not escape to Wasm"
    );
    assert!(back.contains("File.readFile \"target.txt\""));
    assert!(
        !shared.contains("spaScroll_"),
        "fix must not opt old apps into scroll protocol"
    );
    assert!(!front.contains("SpaInitRpc_.rpcScrollHold"));
    std::fs::remove_dir_all(dir).unwrap();
}

#[test]
fn pure_client_kernel_init_keeps_original_client_command() {
    let source = original().replace(
        "saved = 0 }, Cmd.none )",
        "saved = 0 }, Cmd.perform (Task.succeed (String.length \"plain\")) Saved )",
    );
    let (dir, front, _, shared) = split("pure-client-init", &source);
    assert!(front.contains("Task.succeed"));
    assert!(front.contains("String.length \"plain\""));
    assert!(
        !front.contains("/_rpc/SpaInit_"),
        "pure canonical kernel must not acquire server init RPC"
    );
    assert!(!shared.contains("spaScroll_"));
    std::fs::remove_dir_all(dir).unwrap();
}

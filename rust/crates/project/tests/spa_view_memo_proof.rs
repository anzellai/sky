//! Real canonical driver/source/actual-Go-ABI proof fixtures. No browser cache
//! behavior is inferred from these tests: runtime/browser differential gates
//! are a separate required layer.
use std::path::PathBuf;
fn root() -> PathBuf {
    let mut p = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    while !p.join("sky-stdlib").is_dir() {
        assert!(p.pop());
    }
    p
}
fn surface(dir: &std::path::Path) {
    std::fs::create_dir_all(dir.join("sky-ffi/go")).unwrap();
    let mut info: ffi::PackageInfo = serde_json::from_str(
        r#"{
        "pkg":"strings", "name":"strings", "functions":[{
            "name":"TrimSpace", "exported":true,
            "params":[{"name":"s","type":"string","sky3":"String"}],
            "results":[{"type":"string","sky3":"String"}]
        }]
    }"#,
    )
    .unwrap();
    ffi::inspect::normalize(&mut info);
    let generated = ffi::generate(&info);
    assert_eq!(
        ffi::surface_format_of(&generated.kernel_json),
        ffi::gen::SURFACE_FORMAT
    );
    for (path, text) in [
        (
            format!("sky-ffi/{}.kernel.json", generated.slug),
            generated.kernel_json,
        ),
        (format!("sky-ffi/{}.skyi", generated.slug), generated.skyi),
        (
            format!("sky-ffi/go/{}_bindings.go", generated.slug),
            generated.bindings_go,
        ),
    ] {
        std::fs::write(dir.join(path), text).unwrap();
    }
}
const SOURCE: &str = r#"module Main exposing (main)
import Sky.Core.Prelude exposing (..)
import Sky.Core.List as List
import Sky.Core.String as String
import Sky.Core.Task as Task
import Std.Spa as S
import Std.Html as H exposing (Html)
import Std.Html.Attributes as HA
import Std.Ui as Ui exposing (Element)

type alias Item = { id : String, name : String }
type alias Model = { items : List Item, brand : String, saved : String }
type Msg = Pick String | Ignored
blank : Model
blank = { items = [], brand = "", saved = "" }
init : () -> (Model, Cmd Msg)
init _ = (blank, Cmd.none)
update : Msg -> Model -> (Model, Cmd Msg)
update _ m = (m, Cmd.none)
row : String -> Int -> Item -> Element Msg
row saved index item =
    Ui.row [ Ui.onClick (Pick item.id), Ui.class saved ]
        [ Ui.text (String.fromInt index ++ item.name)
        , Ui.html (H.img [ HA.src item.id, HA.alt item.name ])
        ]
products : Model -> Element Msg
products m = Ui.column [] (List.indexedMap (row m.saved) m.items)
editor : Model -> Element Msg
editor m = Ui.column [] [ Ui.text m.brand, products m ]
view : Model -> Html Msg
view m = Ui.layout [] (editor m)
main = S.app (S.config { init = init, update = update, view = view, subscriptions = \_ -> Sub.none })
"#;
#[test]
fn canonical_mapped_rows_get_explicit_context_only_after_all_proofs() {
    let dir = std::env::temp_dir().join(format!("sky-view-memo-proof-{}", std::process::id()));
    struct Cleanup(PathBuf);
    impl Drop for Cleanup {
        fn drop(&mut self) {
            let _ = std::fs::remove_dir_all(&self.0);
        }
    }
    let _clean = Cleanup(dir.clone());
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(dir.join("sky.toml"),"[project]\nname=\"view-memo-proof\"\n[spa]\ngenerated = true\nrole=\"frontend\"\n[\"go.dependencies\"]\nstrings=\"latest\"\n").unwrap();
    surface(&dir);
    assert!(
        project::is_spa_generated_project(&dir),
        "fixture must enter the generated-project grant path"
    );
    for (name,source,extra,eligible) in [
        ("canonical useful rows",SOURCE.to_owned(),false,true),
        ("nonview owner caller",SOURCE.replace("update _ m = (m, Cmd.none)","update _ m = let ignored = editor m in (m, Cmd.none)"),false,false),
        ("forced task in row",SOURCE.replace("row saved index item =\n    Ui.row", "row saved index item =\n    let _ = Task.succeed () in\n    Ui.row"),false,false),
        ("recursive row",SOURCE.replace("row saved index item =\n    Ui.row", "row saved index item =\n    row saved index item\n\nunusedRow saved index item =\n    Ui.row"),false,false),
        ("cfg alias escape",SOURCE.replace("main = S.app (S.config", "main = let cfg = S.config").replace("subscriptions = \\_ -> Sub.none })", "subscriptions = \\_ -> Sub.none } in S.app cfg"),false,false),
        ("added std namespace",SOURCE.to_owned(),true,false),
        ("raw Go FFI in update",SOURCE.replace("import Std.Spa", "import Strings\nimport Sky.Core.Result as Result\nimport Std.Spa").replace("update _ m = (m, Cmd.none)","update _ m = ({ m | brand = Result.withDefault m.brand (Strings.trimSpace m.brand) }, Cmd.none)"),false,false),
    ] {
        std::fs::write(dir.join("src/Main.sky"),source).unwrap();
        let shadow=dir.join("src/Std"); if shadow.exists(){std::fs::remove_dir_all(&shadow).unwrap();}
        if extra {std::fs::create_dir_all(&shadow).unwrap();std::fs::write(shadow.join("Extra.sky"),"module Std.Extra exposing (marker)\nmarker = 0\n").unwrap();}
        let emitted=project::emit_example_source(&root(),&dir).unwrap_or_else(|e|panic!("{name}: {e}"));
        assert_eq!(emitted.contains("ViewMemo:"),eligible,"{name}");
        assert_eq!(emitted.contains("rt.SpaViewMemoRegion("),eligible,"{name}");
        if eligible {
            assert!(emitted.contains("ViewMemoTypes:"));
            assert!(emitted.contains("rt.SpaViewMemoCapture["));
            assert!(emitted.contains("rt.SpaViewMemoType[Main_Msg_Pick_V]"));
            assert!(emitted.contains("type Std_Ui_VAlign int"), "fixture must actually emit canonical enum");
            assert!(emitted.contains("rt.SpaViewMemoEnumType[Std_Ui_VAlign]"), "canonical result enum needs its exact emitted witness");
            assert!(!emitted.contains("rt.SpaViewMemoType[Std_Ui_VAlign]"), "raw named-int reflection tokens remain refused");
            assert!(emitted.contains("Main_products_sky_view_memo_private_"));
            assert!(emitted.contains("Main_editor_sky_view_memo_private_"));
            assert!(emitted.contains("Main_view_sky_view_memo_private_"));
            assert!(!emitted.contains("Main_row_sky_view_memo_private_"));
        }
    }
}

#[test]
fn tuple_discard_task_data_has_no_force_but_root_discard_does() {
    let dir = std::env::temp_dir().join(format!("sky-tuple-task-ir-{}", std::process::id()));
    struct Cleanup(PathBuf);
    impl Drop for Cleanup {
        fn drop(&mut self) {
            let _ = std::fs::remove_dir_all(&self.0);
        }
    }
    let _clean = Cleanup(dir.clone());
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"tuple-task-ir\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
    )
    .unwrap();
    std::fs::write(
        dir.join("src/Main.sky"),
        include_str!("../../sky/tests/fixtures/tuple-task-discard/Main.sky"),
    )
    .unwrap();
    let emitted = project::emit_example_source(&root(), &dir).unwrap();
    let function = |name: &str| {
        let marker = format!("func Main_{name}(");
        let start = emitted
            .find(&marker)
            .expect("actual typed helper must be emitted");
        let mut body = String::new();
        for line in emitted[start..].lines() {
            body.push_str(line);
            body.push('\n');
            if line == "}" {
                return body;
            }
        }
        panic!("emitted helper must have a complete body")
    };
    for name in ["nestedDiscard", "functionDiscard", "genericDiscard"] {
        assert!(
            !function(name).contains("rt.AnyTaskRun"),
            "data/function/unknown discard must remain latent: {name}"
        );
    }
    for name in [
        "rootDiscard",
        "aliasDiscard",
        "localDiscard",
        "lambdaDiscard",
        "tupleDiscard",
        "fieldDiscard",
        "directDiscard",
    ] {
        assert_eq!(
            function(name).matches("rt.AnyTaskRun").count(),
            1,
            "logical Task root discard must force exactly once: {name}"
        );
    }
}

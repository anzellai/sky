//! Private immutable reuse requires a closed frontend boundary. Ordinary split
//! RPCs remain eligible; raw reachable Go FFI conservatively keeps exact Full.
use std::path::{Path, PathBuf};
fn root() -> PathBuf {
    let mut p = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    while !p.join("sky-stdlib").is_dir() {
        assert!(p.pop());
    }
    p
}
fn scratch(tag: &str) -> PathBuf {
    let p = std::env::temp_dir().join(format!("sky-private-domain-{tag}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&p);
    std::fs::create_dir_all(p.join("src")).unwrap();
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
import Sky.Core.Result as Result
import Sky.Ffi as Plumbing
import Std.Codec as C
import Std.Spa as S
import Std.Html as H exposing (Html)

type alias Model = { text : String }
type Msg = Ignored
blank : Model
blank = { text = "" }
encoder : Model -> String
encoder m = C.toJson (C.auto blank) m
prepare : () -> C.Codec Model
prepare unit = C.auto blank
finish : C.Codec Model -> Model -> String
finish codec m = C.toJson codec m
install : (() -> C.Codec Model) -> (C.Codec Model -> Model -> String) -> (Model -> String) -> S.AppConfig Model Msg -> S.AppConfig Model Msg
install = Plumbing.kernel "Spa_withPreparedModelEncoder"
init : () -> (Model, Cmd Msg)
init _ = (blank, Cmd.none)
update : Msg -> Model -> (Model, Cmd Msg)
update _ m = (m, Cmd.none)
view : Model -> Html Msg
view m = H.text m.text
main = S.app (S.config { init = init, update = update, view = view, subscriptions = \_ -> Sub.none } |> install prepare finish encoder)
"#;
#[test]
fn raw_foreign_suppresses_prepared_and_closed_private_capabilities() {
    let dir = scratch("raw");
    surface(&dir);
    std::fs::write(dir.join("sky.toml"),"[project]\nname=\"raw-domain\"\n[spa]\ngenerated = true\nrole=\"frontend\"\n[\"go.dependencies\"]\nstrings=\"latest\"\n").unwrap();
    assert!(
        project::is_spa_generated_project(&dir),
        "fixture must enter the generated-project grant path"
    );
    for (source,eligible) in [
        (SOURCE.to_owned(),true),
        (SOURCE.replace("import Std.Codec", "import Strings\nimport Std.Codec").replace("update _ m = (m, Cmd.none)","update _ m = ({ m | text = Result.withDefault m.text (Strings.trimSpace m.text) }, Cmd.none)"),false),
        (SOURCE.replace("import Std.Codec", "import Strings\nimport Std.Codec").replace("view m = H.text m.text","view m = H.text (Result.withDefault m.text (Strings.trimSpace m.text))"),false),
    ] {
        std::fs::write(dir.join("src/Main.sky"),source).unwrap();
        let emitted=project::emit_example_source(&root(),&dir).unwrap();
        assert_eq!(emitted.contains("rt.SpaPreparedEncoderProof{"),eligible);
        assert!(!emitted.contains("rt.SpaAutoModelEncoder{"));
    }
    // A shadow with the correct textual stdlib name is still not an
    // original-source module grant, even though its types/APIs remain valid.
    std::fs::write(dir.join("src/Main.sky"), SOURCE).unwrap();
    std::fs::create_dir_all(dir.join("src/Std")).unwrap();
    let shadow = std::fs::read_to_string(root().join("sky-stdlib/Std/Codec.sky")).unwrap()
        + "\n-- local shadow\n";
    std::fs::write(dir.join("src/Std/Codec.sky"), shadow).unwrap();
    let error = project::emit_example_source(&root(), &dir)
        .err()
        .expect("project loader must reject stdlib replacement")
        .to_string();
    assert!(
        error.contains("Std.Codec"),
        "wrong stdlib replacement diagnostic: {error}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}
#[test]
fn owned_json_rpc_frontend_retains_generated_private_proofs() {
    let out = scratch("rpc");
    project::spa_split::generate(
        &root(),
        &root().join("rust/crates/sky/tests/fixtures/spa-multihop-chain"),
        None,
        &out,
        None,
        None,
    )
    .unwrap();
    let frontend = out.join("frontend");
    let source = std::fs::read_to_string(frontend.join("src/Main.sky")).unwrap();
    assert!(
        source.contains("Spa.rpc") || source.contains("Spa.rpcHold"),
        "fixture must exercise real split RPC path"
    );
    let emitted = project::emit_example_source(&root(), &frontend).unwrap();
    assert!(
        emitted.contains("rt.SpaPreparedEncoderProof{"),
        "owned RPC must not be confused with direct Go FFI"
    );
    let _ = std::fs::remove_dir_all(&out);
}

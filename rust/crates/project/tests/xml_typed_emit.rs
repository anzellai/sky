//! v0.29.0 regressions for Std.Xml.
//!
//! 1. The Std.Xml accessors go to their typed runtime twins, so a read of a
//!    parsed node narrows nothing at the call site (doc 14 origin R5, the
//!    kernel return, `lower.rs::kernel_call_lowered`; lever §5.3, a typed
//!    kernel entry point, `kernel_typed_return_twin`). `Xml.parse` over a
//!    proven String returns `rt.SkyResult[Sky_Core_Error_Error,
//!    Std_Xml_Node]` itself. runtime-go/rt/xml_kernels_test.go checks each
//!    twin against its erased kernel.
//!
//! 2. A type a Sky module declares with a kernel-handle name (`Decoder`,
//!    `Value`, `Cmd`, `Sub`) and constructors is that module's own type. The
//!    annotation `Decoder a` lowered to `any` (`goty.rs::app_to_go_with`, the
//!    kernel-opaque pre-check) while its pattern emitted the module's nominal,
//!    so `Std.Xml.Decode` (and any user module with a `type Decoder`) passed
//!    `sky check` and failed `go build`.

use project::emit_example_source;
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

fn emit(tag: &str, main: &str) -> String {
    let uniq = format!(
        "sky-xml-emit-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    );
    let dir = std::env::temp_dir().join(uniq);
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"xml-emit\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
    )
    .unwrap();
    std::fs::write(dir.join("src").join("Main.sky"), main).unwrap();
    let out = emit_example_source(&repo_root(), &dir);
    let _ = std::fs::remove_dir_all(&dir);
    out.unwrap_or_else(|e| panic!("emit failed: {e}"))
}

/// The emitted Go function `name`, from its `func` line to the next top-level
/// declaration.
fn func_body<'a>(src: &'a str, name: &str) -> &'a str {
    let head = format!("func {name}(");
    let start = src
        .find(&head)
        .unwrap_or_else(|| panic!("{name} is emitted:\n{src}"));
    let rest = &src[start..];
    let end = rest[1..]
        .find("\nfunc ")
        .or_else(|| rest[1..].find("\nvar "))
        .map(|i| i + 1)
        .unwrap_or(rest.len());
    &rest[..end]
}

const ACCESSORS: &str = "module Main exposing (main)\n\n\
import Sky.Core.Prelude exposing (..)\n\
import Std.Xml as Xml exposing (Node)\n\
import Std.Log exposing (println)\n\n\n\
describe : Node -> String\n\
describe n =\n    \
Xml.name n ++ Xml.namespace n ++ Xml.text n\n\n\n\
kids : Node -> List Node\n\
kids n =\n    \
Xml.children n ++ Xml.elements n ++ Xml.elementsNamed \"a\" n\n\n\n\
attrs : Node -> List ( String, String )\n\
attrs n =\n    \
Xml.attributes n\n\n\n\
href : Node -> Maybe String\n\
href n =\n    \
Xml.attribute \"href\" n\n\n\n\
parsed : String -> Result Error Node\n\
parsed s =\n    \
Xml.parse s\n\n\n\
main =\n    \
case parsed \"<a href='x'><a/></a>\" of\n        \
Ok n ->\n            \
println (describe n ++ String.fromInt (List.length (kids n) + List.length (attrs n)) ++ Maybe.withDefault \"\" (href n))\n\n        \
Err _ ->\n            \
println \"bad\"\n";

#[test]
fn xml_accessors_use_their_typed_twins_and_narrow_nothing() {
    let src = emit("accessors", ACCESSORS);
    for (f, twins) in [
        (
            "Main_describe",
            &["rt.Xml_nameT(", "rt.Xml_namespaceT(", "rt.Xml_textT("][..],
        ),
        (
            "Main_kids",
            &[
                "rt.Xml_childrenT(",
                "rt.Xml_elementsT(",
                "rt.Xml_elementsNamedT(",
            ][..],
        ),
        ("Main_attrs", &["rt.Xml_attributesT("][..]),
        ("Main_href", &["rt.Xml_attributeT("][..]),
        ("Main_parsed", &["rt.Xml_parseT("][..]),
    ] {
        let body = func_body(&src, f);
        for t in twins {
            assert!(body.contains(t), "{f} calls {t}:\n{body}");
        }
        for narrowing in [
            "rt.Coerce[",
            "rt.AsString(",
            "rt.AsListT[",
            "rt.MaybeCoerce[",
            "rt.ResultCoerce[",
        ] {
            assert!(
                !body.contains(narrowing),
                "{f} narrows nothing ({narrowing}):\n{body}"
            );
        }
    }
}

const USER_DECODER: &str = "module Main exposing (main)\n\n\
import Sky.Core.Prelude exposing (..)\n\
import Std.Log exposing (println)\n\n\n\
type Decoder a\n    = Decoder (Int -> a)\n\n\n\
run : Decoder a -> Int -> a\n\
run (Decoder f) n =\n    f n\n\n\n\
twice : Decoder Int -> Decoder Int\n\
twice d =\n    Decoder (\\n -> run d n * 2)\n\n\n\
main =\n    println (String.fromInt (run (twice (Decoder (\\n -> n + 1))) 3))\n";

#[test]
fn a_module_type_named_decoder_is_that_modules_type() {
    let src = emit("user-decoder", USER_DECODER);
    let twice = func_body(&src, "Main_twice");
    assert!(
        twice.starts_with("func Main_twice(v_0 Main_Decoder) Main_Decoder"),
        "the annotation lowers to the module's type, as the pattern does:\n{twice}"
    );
    let run = func_body(&src, "Main_run");
    assert!(run.contains("Main_Decoder"), "{run}");
}

const XML_DECODE: &str = "module Main exposing (main)\n\n\
import Sky.Core.Prelude exposing (..)\n\
import Std.Xml.Decode as XD\n\
import Std.Log exposing (println)\n\n\n\
main =\n    \
case XD.decodeString (XD.element \"b\" XD.int) \"<a><b>2</b></a>\" of\n        \
Ok n ->\n            println (String.fromInt n)\n\n        \
Err _ ->\n            println \"bad\"\n";

#[test]
fn std_xml_decode_decoder_parameters_are_its_own_type() {
    let src = emit("xml-decode", XML_DECODE);
    let element = func_body(&src, "Std_Xml_Decode_element");
    assert!(
        element.starts_with("func Std_Xml_Decode_element(v_0 string, v_1 Std_Xml_Decode_Decoder) Std_Xml_Decode_Decoder"),
        "{element}"
    );
}

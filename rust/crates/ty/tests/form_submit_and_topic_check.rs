//! **`[E2010]` (form-submit handler) and `[E2011]` (literal pub/sub topic).**
//!
//! Both close a "compiles, then fails at run time" hole without changing a
//! public signature: `onSubmit : a -> Attribute msg` and the `any` pub/sub
//! payload stay as they are, and the checker inspects the call sites instead.
//! Every reject is paired with the working program beside it, because an
//! over-rejecting checker is worse than the runtime failure it prevents.

use hir::SourceDb;
use std::path::PathBuf;
use ty::reject_corpus as rc;

fn repo_root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    loop {
        if dir.join("sky-stdlib").is_dir() {
            return dir;
        }
        if !dir.pop() {
            panic!("could not locate repo root (no sky-stdlib ancestor)");
        }
    }
}

fn check_all(mods: &[(&str, &str)]) -> ty::CheckOutput {
    let mut db = SourceDb::new();
    for (name, parse) in rc::load_stdlib(&repo_root()) {
        db.add_module(&name, parse);
    }
    let ids: Vec<_> = mods
        .iter()
        .map(|(n, s)| db.add_module(n, syntax::parse(s, base::FileId(0))))
        .collect();
    ty::check_modules(&db, &ids)
}

fn codes(out: &ty::CheckOutput, code: &str) -> Vec<String> {
    out.diagnostics
        .iter()
        .filter(|d| d.code.0 == code)
        .map(|d| d.message.clone())
        .collect()
}

fn errors(out: &ty::CheckOutput) -> Vec<String> {
    out.diagnostics
        .iter()
        .filter(|d| d.severity == diagnostics::Severity::Error)
        .map(|d| format!("[{}] {}", d.code.0, d.message))
        .collect()
}

const FORM_HEADER: &str = "module Main exposing (view)

import Sky.Core.Prelude exposing (..)
import Std.Ui as Ui exposing (Element)
import Std.Html as Html exposing (Html)
import Std.Html.Events as HE


type alias Typed =
    { title : String, age : Int, agree : Bool, note : Maybe String }


type alias Bad =
    { title : String, tags : List String }


type Msg
    = Save
    | SubmitTyped Typed
    | SubmitStr String
    | SubmitBad Bad
    | SubmitDict (Dict String String)


";

fn form_view(attr: &str) -> String {
    if attr.starts_with("HE.") {
        return format!(
            "{FORM_HEADER}view : Int -> Html Msg\nview _ =\n    Html.form [ {attr} ] []\n"
        );
    }
    format!(
        "{FORM_HEADER}view : Int -> Element Msg\nview _ =\n    Ui.form [ {attr} ] [ Ui.text \"x\" ]\n"
    )
}

#[test]
fn typed_record_plain_msg_and_dict_handlers_are_accepted() {
    for attr in [
        "Ui.onSubmit Save",
        "Ui.onSubmit SubmitTyped",
        "Ui.onSubmit SubmitDict",
        "Ui.onSubmit (\\f -> SubmitTyped f)",
        "Ui.onSubmit (\\_ -> Save)",
        "HE.onSubmit SubmitTyped",
    ] {
        let out = check_all(&[("Main", &form_view(attr))]);
        assert!(
            errors(&out).is_empty(),
            "`{attr}` must be accepted, got {:#?}",
            errors(&out)
        );
    }
}

#[test]
fn a_number_a_string_handler_and_a_list_field_are_rejected() {
    for (attr, needle) in [
        ("Ui.onSubmit 42", "Int"),
        ("Ui.onSubmit SubmitStr", "String"),
        ("Ui.onSubmit SubmitBad", "tags"),
        ("HE.onSubmit SubmitStr", "String"),
    ] {
        let out = check_all(&[("Main", &form_view(attr))]);
        let e = codes(&out, "E2010");
        assert_eq!(
            e.len(),
            1,
            "`{attr}` must be rejected once with [E2010], got {:#?}",
            errors(&out)
        );
        assert!(
            e[0].contains(needle),
            "`{attr}`: message names {needle}: {}",
            e[0]
        );
        assert!(out.type_errors > 0, "[E2010] must count as a type error");
    }
}

const PUB: &str = "module Pub exposing (chatTopic, send)

import Sky.Core.Prelude exposing (..)
import Std.Cmd as Cmd exposing (Cmd)


chatTopic : String
chatTopic =
    \"chat\"


send : String -> Cmd msg
send text =
    Cmd.publish chatTopic text
";

fn sub_module(decoder_ctor: &str, topic: &str) -> String {
    format!(
        "module Main exposing (subscriptions)

import Sky.Core.Prelude exposing (..)
import Std.Sub as Sub exposing (Sub)
import Pub


type Msg
    = GotStr String
    | GotInt Int


subscriptions : Int -> Sub Msg
subscriptions _ =
    Sub.subscribeTopic {topic} {decoder_ctor}
"
    )
}

#[test]
fn agreeing_topic_across_modules_is_accepted() {
    for topic in ["\"chat\"", "Pub.chatTopic"] {
        let out = check_all(&[("Pub", PUB), ("Main", &sub_module("GotStr", topic))]);
        assert!(
            errors(&out).is_empty(),
            "a matching publisher/subscriber on {topic} must be accepted: {:#?}",
            errors(&out)
        );
    }
}

#[test]
fn disagreeing_topic_across_modules_is_rejected_naming_both_sites() {
    for topic in ["\"chat\"", "Pub.chatTopic"] {
        let out = check_all(&[("Pub", PUB), ("Main", &sub_module("GotInt", topic))]);
        let e = codes(&out, "E2011");
        assert_eq!(
            e.len(),
            1,
            "one [E2011] for {topic}, got {:#?}",
            errors(&out)
        );
        assert!(
            e[0].contains("Pub") && e[0].contains("Main") && e[0].contains("\"chat\""),
            "the message names both sites and the topic: {}",
            e[0]
        );
        assert!(e[0].contains("String") && e[0].contains("Int"), "{}", e[0]);
    }
}

#[test]
fn a_different_topic_does_not_clash() {
    let out = check_all(&[("Pub", PUB), ("Main", &sub_module("GotInt", "\"nums\""))]);
    assert!(codes(&out, "E2011").is_empty(), "{:#?}", errors(&out));
}

//! The header-name lint (`lower/src/lower.rs`, `lint_header_dict_lookups`),
//! pinned in both directions.
//!
//! HTTP header names are case-insensitive (RFC 9110 section 5.1). HTTP/2 sends
//! them in lower case and a proxy may send any case. The runtime stores every
//! request and response header under its canonical name (`X-Sky-Api-Key`,
//! `X-Real-Ip`), so `Dict.get "X-Sky-Api-Key" req.headers` finds the header
//! whatever case the client used, and `Dict.get "x-sky-api-key" req.headers`
//! never matches. That miss was silent: the app read `Nothing` and refused a
//! valid API key. The lint names the canonical key and the lookups that ignore
//! case (`Server.header`, `Http.header`).
//!
//! It must not fire on a canonical key, on a key that is not a literal, or on a
//! Dict that is not a `headers` field.

use project::emit_example_warnings;
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

fn scratch(tag: &str, main: &str) -> PathBuf {
    let uniq = format!(
        "sky-hdrlint-{tag}-{}-{}",
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
        "name = \"hdr-lint\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
    )
    .unwrap();
    std::fs::write(dir.join("src").join("Main.sky"), main).unwrap();
    dir
}

fn header_warnings(tag: &str, body: &str) -> Vec<String> {
    // Reference every top-level def from `main`: lowering only visits what is
    // reachable from the entry point.
    let uses: Vec<String> = body
        .lines()
        .filter_map(|l| l.split_once(" : ").map(|(n, _)| n.trim().to_string()))
        .filter(|n| !n.is_empty() && !n.starts_with(' '))
        .map(|n| format!("        _ = {n}\n"))
        .collect();
    let uses = uses.concat();
    let main = format!(
        r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Dict as Dict
import Sky.Core.Http as Http
import Sky.Http.Server as Server
import Std.Log exposing (println)


{body}


main =
    let
{uses}    in
        println "ok"
"#
    );
    let repo = repo_root();
    let project = scratch(tag, &main);
    let warnings =
        emit_example_warnings(&repo, &project).unwrap_or_else(|e| panic!("emit failed: {e}"));
    let _ = std::fs::remove_dir_all(&project);
    warnings
        .into_iter()
        .filter(|w| w.contains("never matches an HTTP header"))
        .collect()
}

#[test]
fn a_lower_case_key_on_request_headers_warns_with_the_canonical_name() {
    let w = header_warnings(
        "lower",
        r#"apiKey : Server.Request -> Maybe String
apiKey req =
    Dict.get "x-sky-api-key" req.headers"#,
    );
    assert_eq!(w.len(), 1, "{w:?}");
    assert!(w[0].contains("`X-Sky-Api-Key`"), "{}", w[0]);
    assert!(w[0].contains("Server.header"), "{}", w[0]);
}

#[test]
fn an_upper_case_key_and_a_piped_lookup_warn() {
    let w = header_warnings(
        "upper",
        r#"apiKey : Server.Request -> Maybe String
apiKey req =
    req.headers |> Dict.get "X-SKY-API-KEY"


realIp : Server.Request -> Bool
realIp req =
    Dict.member "X-Real-IP" req.headers"#,
    );
    assert_eq!(w.len(), 2, "{w:?}");
    assert!(w.iter().any(|m| m.contains("`X-Sky-Api-Key`")), "{w:?}");
    assert!(w.iter().any(|m| m.contains("`X-Real-Ip`")), "{w:?}");
}

#[test]
fn a_response_header_lookup_warns_and_names_http_header() {
    let w = header_warnings(
        "resp",
        r#"etag : Http.HttpResponse -> Maybe String
etag resp =
    Dict.get "ETag" resp.headers"#,
    );
    assert_eq!(w.len(), 1, "{w:?}");
    assert!(
        w[0].contains("`Etag`") && w[0].contains("Http.header"),
        "{}",
        w[0]
    );
}

#[test]
fn canonical_keys_variables_and_other_dicts_do_not_warn() {
    let w = header_warnings(
        "quiet",
        r#"apiKey : Server.Request -> Maybe String
apiKey req =
    Dict.get "X-Sky-Api-Key" req.headers


byName : String -> Server.Request -> Maybe String
byName name req =
    Dict.get name req.headers


cookie : Server.Request -> Maybe String
cookie req =
    Dict.get "session_id" req.cookies


viaHelper : Server.Request -> Maybe String
viaHelper req =
    Server.header "x-sky-api-key" req"#,
    );
    assert!(w.is_empty(), "{w:?}");
}

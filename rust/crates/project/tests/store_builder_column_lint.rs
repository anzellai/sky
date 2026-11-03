//! The Store builder lint (`lower/src/store_lint.rs`, v0.29.2), pinned in both
//! directions.
//!
//! A literal column name given to a `Std.Db.Store` builder, to `findBy` or to
//! a query builder on a `Store.query` chain that the store's codec does not
//! have makes every verb on the store fail at run time. `sky check` names it
//! first, with the column list and a did-you-mean. It must not fire on a codec
//! column (field or snake name), on a declared external column, or when the
//! codec's columns are not visible in the source.

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
        "sky-storelint-{tag}-{}-{}",
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
        "name = \"store-lint\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
    )
    .unwrap();
    std::fs::write(dir.join("src").join("Main.sky"), main).unwrap();
    dir
}

/// The store lint's warnings for a module whose `stores` and `queries` defs
/// are reached from `main`.
fn store_warnings(tag: &str, body: &str) -> Vec<String> {
    let uses: Vec<String> = body
        .lines()
        .filter_map(|l| l.split_once(" : ").map(|(n, _)| n.trim().to_string()))
        .filter(|n| !n.is_empty() && !n.starts_with(' ') && !n.contains(' '))
        .map(|n| format!("        _ = {n}\n"))
        .collect();
    let uses = uses.concat();
    let main = format!(
        r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Std.Codec as Codec exposing (Codec)
import Std.Db as Db exposing (Db, SqlValue(..))
import Std.Db.Store as Store exposing (Store)
import Std.Log exposing (println)


type alias Lead =
    {{ id : String
    , email : String
    , createdAt : Int
    }}


blank : Lead
blank =
    {{ id = "", email = "", createdAt = 0 }}


leadCodec : Codec Lead
leadCodec =
    Codec.auto blank


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
        .filter(|w| w.contains("`Store."))
        .collect()
}

#[test]
fn an_unknown_column_in_a_codec_only_builder_warns_with_the_columns() {
    let w = store_warnings(
        "unknown",
        r#"leads : Store Lead
leads =
    Store.fromCodec "leads" leadCodec
        |> Store.primaryKey "id"
        |> Store.uniqueOn [ "emial" ]"#,
    );
    assert_eq!(w.len(), 1, "{w:?}");
    assert!(w[0].contains("`Store.uniqueOn \"emial\"`"), "{}", w[0]);
    assert!(w[0].contains("did you mean \"email\""), "{}", w[0]);
    assert!(w[0].contains("id, email, created_at"), "{}", w[0]);
}

#[test]
fn query_and_find_names_are_checked_against_the_store() {
    let w = store_warnings(
        "query",
        r#"leads : Store Lead
leads =
    Store.fromCodec "leads" leadCodec |> Store.primaryKey "id"


recent : Store.Query Lead
recent =
    Store.query leads
        |> Store.where_ (Store.and_ [ Store.eq "emali" (SqlString "a"), Store.gt "createdAt" (SqlInt 0) ])
        |> Store.orderDesc "created"


byMail : Db -> Task Error (Maybe Lead)
byMail db =
    Store.findBy db leads "mail" "a@b""#,
    );
    assert_eq!(w.len(), 3, "{w:?}");
    assert!(
        w.iter().any(|m| m.contains("`Store.eq \"emali\"`")),
        "{w:?}"
    );
    assert!(
        w.iter()
            .any(|m| m.contains("`Store.orderDesc \"created\"`")),
        "{w:?}"
    );
    assert!(
        w.iter().any(|m| m.contains("`Store.findBy \"mail\"`")),
        "{w:?}"
    );
}

#[test]
fn codec_columns_external_columns_and_unseen_codecs_do_not_warn() {
    let w = store_warnings(
        "quiet",
        r#"leads : Store Lead
leads =
    Store.fromCodec "leads" leadCodec
        |> Store.primaryKey "id"
        |> Store.unique "email"
        |> Store.index [ "createdAt", "created_at" ]
        |> Store.defaultWith "updated_at" (\_ -> SqlInt 0)
        |> Store.defaultNow "imported_at"
        |> Store.generated [ "updated_at" ]


objects : Store Lead
objects =
    Store.fromCodec
        "objects"
        (Codec.object Lead
            |> Codec.field "lead_id" .id Codec.string
            |> Codec.field "mail" .email Codec.string
            |> Codec.field "at" .createdAt Codec.int
            |> Codec.buildObject)
        |> Store.primaryKey "lead_id"
        |> Store.uniqueOn [ "mail" ]


unseen : Codec Lead -> Store Lead
unseen c =
    Store.fromCodec "unseen" c |> Store.uniqueOn [ "anything" ]


sorted : Store.Query Lead
sorted =
    Store.query leads |> Store.where_ (Store.gt "updated_at" (SqlInt 0)) |> Store.orderAsc "email""#,
    );
    assert!(w.is_empty(), "{w:?}");
}

#[test]
fn an_external_builder_warns_only_for_a_respelled_codec_column() {
    let w = store_warnings(
        "respelled",
        r#"leads : Store Lead
leads =
    Store.fromCodec "leads" leadCodec
        |> Store.defaultText "Email" "x"
        |> Store.defaultNow "Created_At"
        |> Store.defaultInt "visits" 0"#,
    );
    // "Email" resolves (its snake form is "email"); "visits" is an external
    // column; "Created_At" is created_at spelled another way.
    assert_eq!(w.len(), 1, "{w:?}");
    assert!(
        w[0].contains("`Store.defaultNow \"Created_At\"`"),
        "{}",
        w[0]
    );
    assert!(w[0].contains("Write \"created_at\""), "{}", w[0]);
}

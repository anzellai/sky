//! Exercise owner grants through the actual project source loader and emitter.
//! No Go build and no lower-config trust injection: module names alone cannot
//! manufacture canonical stdlib SourceFile identity.
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicUsize, Ordering};

fn root() -> PathBuf {
    let mut path = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    while !path.join("sky-stdlib").is_dir() {
        assert!(path.pop());
    }
    path
}

// A clock-derived name is not an ownership claim: parallel tests can observe
// the same timestamp, and create_dir_all accepts an already occupied fixture.
// Claim the root atomically, retrying stale paths without reading/modifying them.
fn reserve_fixture_directory(parent: &Path, next: &AtomicUsize) -> PathBuf {
    loop {
        let id = next.fetch_add(1, Ordering::Relaxed);
        let path = parent.join(format!("sky-project-owner-{}-{id}", std::process::id()));
        match std::fs::create_dir(&path) {
            Ok(()) => return path,
            Err(error) if error.kind() == std::io::ErrorKind::AlreadyExists => continue,
            Err(error) => panic!("cannot claim source-grant fixture directory: {error}"),
        }
    }
}

struct Fixture(PathBuf);
impl Fixture {
    fn new(generated: bool) -> Self {
        static NEXT: AtomicUsize = AtomicUsize::new(0);
        let path = reserve_fixture_directory(&std::env::temp_dir(), &NEXT);
        std::fs::create_dir(path.join("src")).unwrap();
        let mut config =
            "name = \"owner-grant-probe\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n"
                .to_string();
        if generated {
            config.push_str("[spa]\ngenerated = true\nrole = \"backend\"\n");
        }
        std::fs::write(path.join("sky.toml"), config).unwrap();
        Self(path)
    }
    fn source(&self, relative: &str, contents: &str) {
        let path = self.0.join("src").join(relative);
        std::fs::create_dir_all(path.parent().unwrap()).unwrap();
        std::fs::write(path, contents).unwrap();
    }
}
impl Drop for Fixture {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

const ALIASES: &str = r#"module Aliases exposing (Exact, Coin, Amount)
import Std.Decimal as Decimal
import Std.Money as Money
type alias Exact = Decimal.Decimal
type alias Coin = Money.Currency
type alias Amount = Money.Money
"#;
const MAIN: &str = r#"module Main exposing (main)
import Sky.Core.Prelude exposing (..)
import Sky.Core.Dict as Dict
import Std.Codec as Codec
import Std.Decimal as Decimal
import Std.Money as Money
import Std.Log as Log
import Aliases

type alias Amount = Aliases.Amount
type alias Row =
    { amount : Amount
    , exact : Aliases.Exact
    , coin : Aliases.Coin
    , nested : List (Maybe ( Aliases.Exact, Aliases.Coin ))
    , coins : Dict.Dict String Aliases.Coin
    }
blank : Row
blank =
    { amount = Money.zero Money.GBP
    , exact = Decimal.zero
    , coin = Money.GBP
    , nested = [ Just ( Decimal.one, Money.GBP ) ]
    , coins = Dict.fromList [ ( "home", Money.GBP ) ]
    }
main = Log.println (Codec.toJson (Codec.auto blank) blank)
"#;

#[test]
fn resolved_builtin_owners_follow_canonical_project_sources_and_aliases() {
    // Adding a harmless Std.* module disables blanket namespace trust but
    // must never revoke a different module's exact canonical SourceFile grant.
    // Generated/private auto-codec status is likewise independent of owners.
    for generated in [false, true] {
        for extra_namespace in [false, true] {
            let fixture = Fixture::new(generated);
            fixture.source("Aliases.sky", ALIASES);
            fixture.source("Main.sky", MAIN);
            if extra_namespace {
                fixture.source(
                    "Std/OfficeExtra.sky",
                    "module Std.OfficeExtra exposing (marker)\nmarker = 0\n",
                );
            }
            let emitted = project::emit_example_source(&root(), &fixture.0).expect(
                "canonical project aliases must emit without a manually supplied lower trust set",
            );
            for (name, owner) in [
                ("Std_Decimal_Decimal", "builtin:Std.Decimal.Decimal"),
                ("Std_Money_Money", "builtin:Std.Money.Money"),
                ("Std_Money_Currency", "builtin:Std.Money.Currency"),
            ] {
                let registration =
                    format!("rt.RegisterAdtOwner(\"main.{name}\", \"{name}\", \"{owner}\")");
                assert!(
                    emitted.contains(&registration),
                    "missing actual resolved-parent registration for {name}"
                );
            }
            // These field tags must name canonical parents, even though the
            // surface slots use local and imported transparent alias chains.
            for parent in [
                "Std_Decimal_Decimal",
                "Std_Money_Currency",
                "Std_Money_Money",
            ] {
                assert!(
                    emitted.contains(&format!(",{parent}")),
                    "transparent alias metadata lost {parent}"
                );
            }
            assert!(emitted.contains("rt.SkyDict[Std_Money_Currency]"));
            assert!(emitted.contains("rt.T2[Std_Decimal_Decimal, Std_Money_Currency]"));
        }
    }
}

#[test]
fn project_source_shadow_never_grants_builtin_owner() {
    // The project loader currently refuses stdlib replacement altogether.
    // Require that refusal at the real source boundary; this case must never
    // successfully emit a shadow with grants merely because its name matches.
    for generated in [false, true] {
        for (module, path, source) in [
            ("Std.Decimal", "Std/Decimal.sky", "module Std.Decimal exposing (Decimal)\ntype Decimal = Decimal__Internal String\n"),
            ("Std.Money", "Std/Money.sky", "module Std.Money exposing (Money, Currency)\ntype Currency = GBP\ntype Money = Money String Currency\n"),
        ] {
            let fixture = Fixture::new(generated);
            fixture.source("Aliases.sky", ALIASES);
            fixture.source("Main.sky", MAIN);
            fixture.source(path, source);
            let error = project::emit_example_source(&root(), &fixture.0)
                .expect_err("a distinct app SourceFile cannot replace the canonical stdlib source");
            assert!(error.contains("standard-library module") && error.contains("replace"), "{module}: expected source replacement refusal, got {error}");
        }
    }
}

// Force the collision deterministically instead of relying on two threads
// receiving the same clock tick. An occupied negative fixture must never be
// reused or deleted by allocation of a positive fixture.
#[test]
fn source_grant_fixture_claim_never_reuses_an_occupied_directory() {
    let parent = Fixture::new(false);
    let occupied = parent
        .0
        .join(format!("sky-project-owner-{}-0", std::process::id()));
    std::fs::create_dir(&occupied).unwrap();
    let negative = occupied.join("src/Std/Decimal.sky");
    std::fs::create_dir_all(negative.parent().unwrap()).unwrap();
    std::fs::write(&negative, "negative-fixture sentinel").unwrap();
    let next = AtomicUsize::new(0);
    let claimed = reserve_fixture_directory(&parent.0, &next);
    assert_ne!(claimed, occupied);
    assert!(!claimed.join("src/Std/Decimal.sky").exists());
    assert_eq!(
        std::fs::read_to_string(&negative).unwrap(),
        "negative-fixture sentinel"
    );
    assert_eq!(next.load(Ordering::Relaxed), 2);
}

// A custom __Internal constructor uses the existing opaque-type convention.
// Public auto derivation must not be widened merely to fit a runtime spoofing
// fixture; Money spelling collisions use ordinary serialisable Price instead.
#[test]
fn custom_internal_constructor_remains_opaque_to_auto_derivation() {
    let fixture = Fixture::new(false);
    fixture.source(
        "Main.sky",
        r#"module Main exposing (main)
import Std.Codec as Codec
import Std.Log as Log

type Exact = Decimal__Internal String

blank = { value = Decimal__Internal "keep" }
main = Log.println (Codec.toJson (Codec.auto blank) blank)
"#,
    );
    let error = project::emit_example_source(&root(), &fixture.0)
        .expect_err("custom opaque payload must not acquire auto derivation");
    assert!(
        error.contains("cannot be encoded") && error.contains("opaque"),
        "expected the existing opaque Encodable refusal, got {error}"
    );
}

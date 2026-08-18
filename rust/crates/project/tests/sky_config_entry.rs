//! The `Sky.Config` entry point: discovery, DCE rooting, emission, and the
//! type guard — proven on the emitted Go, no Go toolchain needed.
//!
//! `config` is a reserved entry point discovered exactly as `main` is
//! (`lower.rs`). Three properties, each with a distinct way of silently
//! failing (the crux the design's grill flagged, G2):
//!
//!   1. EMISSION. A program that declares `config` emits
//!      `rt.ApplyConfig(Main_config())` as the first statement of `main`, after
//!      the deferred panic guard and BEFORE `MaybeStartEmbeddedPostgres`.
//!   2. DCE ROOT. A `config` NOT referenced by `main` must still be lowered —
//!      `Main_config()` must exist, or `ApplyConfig` would call a pruned
//!      function and the config would silently do nothing.
//!   3. BYTE-STABILITY. A program with NO `config` binding emits no
//!      `ApplyConfig` at all, so every existing program's `main` is unchanged
//!      (repro / golden / coerce-floor baselines do not move).
//!
//! Plus the type guard: an ill-typed `config` is a build ERROR, not a silent
//! no-op.

use std::path::PathBuf;

fn repo_root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    loop {
        if dir.join("sky-stdlib").is_dir() {
            return dir;
        }
        assert!(dir.pop(), "could not locate repo root (no sky-stdlib ancestor)");
    }
}

fn scratch_project(tag: &str, main_src: &str) -> PathBuf {
    let uniq = format!(
        "sky-config-entry-{tag}-{}-{}",
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
        "name = \"config-entry\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
    )
    .unwrap();
    std::fs::write(dir.join("src").join("Main.sky"), main_src).unwrap();
    dir
}

fn emitted_main_body(source: &str) -> String {
    let start = source
        .find("\nfunc main() {")
        .unwrap_or_else(|| panic!("the emitted Go has no `func main()`:\n{source}"));
    let rest = &source[start + 1..];
    let end = rest
        .find("\n}\n")
        .unwrap_or_else(|| panic!("`func main()` is unterminated:\n{rest}"));
    rest[..end].to_string()
}

// A program whose `config` is NOT referenced by `main` — the DCE-root case.
const CONFIG_APP: &str = "\
module Main exposing (main, config)

import Std.Log exposing (println)
import Sky.Config as Config exposing (LogFormat(..), LogLevel(..))

config : Config.Config
config =
    Config.default
        |> Config.withLog Json Warn

main =
    println \"hi\"
";

const NO_CONFIG_APP: &str = "\
module Main exposing (main)

import Std.Log exposing (println)

main =
    println \"hi\"
";

// A user's OWN zero-param nominal type named `Config` (base `"Config"`, but
// declared in `Main`, so qualified `Main.Config`), with NO `Sky.Config` import.
// The entry-point discovery must NOT hijack it.
const USER_CONFIG_HIJACK_APP: &str = "\
module Main exposing (main)

import Std.Log exposing (println)

type Config = MkConfig Int

config : Config
config = MkConfig 5

main =
    println \"hi\"
";

// The real feature via an import ALIAS: `config : C.Config` where `C` aliases
// `Sky.Config`. The declaring module wins over the alias, so this resolves to
// the confident `Sky.Config.Config` and MUST be discovered.
const ALIASED_CONFIG_APP: &str = "\
module Main exposing (main, config)

import Std.Log exposing (println)
import Sky.Config as C exposing (LogFormat(..), LogLevel(..))

config : C.Config
config =
    C.default
        |> C.withLog Json Warn

main =
    println \"hi\"
";

// The real feature UNANNOTATED: the result type is INFERRED to the confident
// `Sky.Config.Config`, so it too must be discovered.
const UNANNOTATED_CONFIG_APP: &str = "\
module Main exposing (main, config)

import Std.Log exposing (println)
import Sky.Config as Config exposing (LogFormat(..), LogLevel(..))

config =
    Config.default
        |> Config.withLog Json Warn

main =
    println \"hi\"
";

#[test]
fn a_user_config_type_does_not_hijack_the_entry_point() {
    // THE HOLE. A user's own `type Config` + zero-param `config : Config` value,
    // with NO `Sky.Config` import, must NOT be treated as the config entry point.
    // Discovery keyed on the bare base `"Config"` (or on `ty::nominal::same`,
    // which treats a bare name as a wildcard) would emit
    // `rt.ApplyConfig(Main_config())` against a union value — a spurious call and
    // a byte-stability break. Requiring the confident qualified identity
    // `Sky.Config.Config` (the user's arrives as `Main.Config`) closes it.
    //
    // Mutation proof: revert the discovery guard back to
    // `base(n.as_str()) == "Config"` and this assertion goes red.
    let repo = repo_root();
    let project = scratch_project("userhijack", USER_CONFIG_HIJACK_APP);
    let source = project::emit_example_source(&repo, &project)
        .unwrap_or_else(|e| panic!("a user `type Config` app must still build: {e}"));
    let _ = std::fs::remove_dir_all(&project);

    assert!(
        !source.contains("ApplyConfig"),
        "a user's OWN `type Config` (no Sky.Config import) must NOT be discovered \
         as the config entry point — no ApplyConfig may be emitted:\n{source}"
    );
}

#[test]
fn an_import_aliased_config_is_discovered() {
    // The real feature must survive the fix: `config : C.Config` where `C`
    // aliases `Sky.Config` resolves to the declaring module `Sky.Config.Config`
    // and IS the entry point. Guards against over-correcting into a false
    // negative that breaks a real user idiom (the Judge flagged this as H4b).
    let repo = repo_root();
    let project = scratch_project("aliased", ALIASED_CONFIG_APP);
    let source = project::emit_example_source(&repo, &project)
        .unwrap_or_else(|e| panic!("emit failed: {e}"));
    let _ = std::fs::remove_dir_all(&project);

    assert!(
        source.contains("rt.ApplyConfig(Main_config())"),
        "an import-aliased `config : C.Config` (C = Sky.Config) MUST be \
         discovered and emit rt.ApplyConfig(Main_config()):\n{source}"
    );
}

#[test]
fn an_unannotated_config_is_discovered() {
    // The chosen, deliberate behaviour: an unannotated `config` whose body
    // produces a `Sky.Config.Config` is inferred to that confident qualified
    // type and IS discovered (emits ApplyConfig). This is the same confident
    // identity the annotated and aliased forms carry — not an accident of the
    // annotation being present.
    let repo = repo_root();
    let project = scratch_project("unannotated", UNANNOTATED_CONFIG_APP);
    let source = project::emit_example_source(&repo, &project)
        .unwrap_or_else(|e| panic!("emit failed: {e}"));
    let _ = std::fs::remove_dir_all(&project);

    assert!(
        source.contains("rt.ApplyConfig(Main_config())"),
        "an unannotated `config = Config.default |> …` inferred to \
         Sky.Config.Config MUST be discovered and emit \
         rt.ApplyConfig(Main_config()):\n{source}"
    );
}

#[test]
fn config_binding_emits_apply_config_first_in_main() {
    let repo = repo_root();
    let project = scratch_project("emit", CONFIG_APP);
    let source = project::emit_example_source(&repo, &project)
        .unwrap_or_else(|e| panic!("emit failed: {e}"));
    let _ = std::fs::remove_dir_all(&project);

    let body = emitted_main_body(&source);

    // The apply call is present and names the config accessor.
    assert!(
        body.contains("rt.ApplyConfig(Main_config())"),
        "`func main()` must call rt.ApplyConfig(Main_config()):\n{body}"
    );

    // Ordering: after the deferred panic guard, before the embedded-PG start —
    // so the config (a `withX` DSN, when present) is applied before the runtime
    // reads it.
    let at_defer = body.find("defer rt.LogPanicAndExit()").expect("no panic guard");
    let at_apply = body.find("rt.ApplyConfig(Main_config())").unwrap();
    let at_pg = body
        .find("rt.MaybeStartEmbeddedPostgres()")
        .expect("no embedded-PG start");
    assert!(
        at_defer < at_apply && at_apply < at_pg,
        "ApplyConfig must sit between the panic guard and the embedded-PG start:\n{body}"
    );
}

#[test]
fn config_not_referenced_by_main_is_still_lowered() {
    // DCE roots at `main`; `config` here is referenced by nothing. Without the
    // second DCE root, `Main_config` would be pruned and `ApplyConfig` would
    // call a function that does not exist — the silent no-op the grill flagged.
    let repo = repo_root();
    let project = scratch_project("dce", CONFIG_APP);
    let source = project::emit_example_source(&repo, &project)
        .unwrap_or_else(|e| panic!("emit failed: {e}"));
    let _ = std::fs::remove_dir_all(&project);

    assert!(
        source.contains("func Main_config()"),
        "the `config` accessor `Main_config` must be emitted even though `main` \
         never references it (DCE second root):\n{source}"
    );
    // And the kernel it flows through must be reachable too.
    assert!(
        source.contains("rt.Config_withLog") || source.contains("Config_withLog"),
        "the `Sky.Config.withLog` kernel must be reachable from `config`:\n{source}"
    );
    // `Sky.Config.default` is a nullary kernel VALUE — it must be CALLED
    // (`rt.Config_default()`), not passed as an uncalled `func() any`, so the
    // builder chain starts from the real empty map (matters the moment a builder
    // reads an existing key).
    assert!(
        source.contains("rt.Config_default()"),
        "`Sky.Config.default` must be emitted as a call `rt.Config_default()`:\n{source}"
    );
    assert!(
        !source.contains("rt.Config_default,") && !source.contains("rt.Config_default)"),
        "`Sky.Config.default` must not be passed uncalled (bare `func() any`):\n{source}"
    );
}

#[test]
fn no_config_binding_emits_no_apply_config() {
    // Byte-stability: a program without `config` must be UNCHANGED — no
    // ApplyConfig anywhere in the emitted file.
    let repo = repo_root();
    let project = scratch_project("stable", NO_CONFIG_APP);
    let source = project::emit_example_source(&repo, &project)
        .unwrap_or_else(|e| panic!("emit failed: {e}"));
    let _ = std::fs::remove_dir_all(&project);

    assert!(
        !source.contains("ApplyConfig"),
        "a program with no `config` binding must emit no ApplyConfig call:\n{source}"
    );
}

#[test]
fn an_unrelated_config_binding_is_left_alone() {
    // `config` is a common user identifier. Only a VALUE of type
    // `Sky.Config.Config` is the entry point; a helper named `config` of any
    // other type must be untouched — no ApplyConfig, and it still builds. This
    // is the backward-compatibility property (a real fixture, issue161, has a
    // `config : Session -> {...}` function).
    let src = "\
module Main exposing (main)

import Std.Log exposing (println)

config : Int -> Int
config n =
    n + 1

main =
    println (String.fromInt (config 41))
";
    let repo = repo_root();
    let project = scratch_project("unrelated", src);
    let source = project::emit_example_source(&repo, &project)
        .unwrap_or_else(|e| panic!("an unrelated `config` binding must still build: {e}"));
    let _ = std::fs::remove_dir_all(&project);

    assert!(
        !source.contains("ApplyConfig"),
        "a non-`Sky.Config.Config` `config` binding must NOT be treated as the \
         config entry point:\n{source}"
    );
}

#[test]
fn annotated_config_type_mismatch_is_a_build_error() {
    // The loud path for a genuine mistake: an annotated `config : Config.Config`
    // whose body does not produce one is a type error (surfaced identically in
    // build / run / test / LSP — it comes from the type checker), NOT a value
    // silently applied against.
    let bad = "\
module Main exposing (main, config)

import Std.Log exposing (println)
import Sky.Config as Config

config : Config.Config
config =
    5

main =
    println \"hi\"
";
    let repo = repo_root();
    let project = scratch_project("mismatch", bad);
    let result = project::emit_example_source(&repo, &project);
    let _ = std::fs::remove_dir_all(&project);

    assert!(
        result.is_err(),
        "an annotated `config : Config.Config` with a non-Config body must fail \
         the build, got Ok:\n{:?}",
        result.ok()
    );
}

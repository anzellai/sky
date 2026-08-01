//! Regression for conformance finding **C3** — cross-module same-named-type
//! collision in `case` pattern emission.
//!
//! Two modules that each declare `type Prim = Leaf String | Node Int` (same
//! type name AND same variant names) used to miscompile: a `case` on an
//! `Alpha.Prim` value emitted its variant type-assertions against
//! `Beta_Prim_Leaf_V` because the pattern lowerer resolved the bare constructor
//! name through a last-writer-wins `ctor_owner` map (Beta, interned last).
//! At runtime the Alpha value never matched a Beta variant struct → the
//! exhaustiveness-checked case fell through to `panic(rt.Unreachable("case"))`
//! (and, through the reflective codec `taggedUnion` decode path, an
//! `interface conversion: Alpha_Prim_Leaf_V is not Beta_Prim_Leaf_V` panic).
//!
//! The fix (lower.rs `ctor_union_owner`) prefers the pattern's resolved
//! `CtorRef.type_` DefId — module-correct — over the bare-name map. This test
//! drives the real emit pipeline and asserts each module's `case` arms assert
//! against ITS OWN `_V` variant structs.

use project::emit_example_source;
use std::path::{Path, PathBuf};

fn repo_root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    loop {
        if dir.join("sky-stdlib").is_dir() {
            return dir;
        }
        assert!(dir.pop(), "could not locate repo root (no sky-stdlib ancestor)");
    }
}

/// Materialise a throwaway multi-module project and return its dir.
fn scratch_multi(tag: &str, files: &[(&str, &str)]) -> PathBuf {
    let uniq = format!(
        "sky-c3-{tag}-{}-{}",
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
        "name = \"c3-xmodule\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
    )
    .unwrap();
    for (name, src) in files {
        std::fs::write(dir.join("src").join(name), src).unwrap();
    }
    dir
}

fn cleanup(dir: &Path) {
    let _ = std::fs::remove_dir_all(dir);
}

#[test]
fn cross_module_same_named_adt_case_pins_correct_variant() {
    let repo = repo_root();
    let project = scratch_multi(
        "same-variant",
        &[
            (
                "Alpha.sky",
                "module Alpha exposing (Prim(..), mk)\n\
                 type Prim = Leaf String | Node Int\n\
                 mk : Prim\n\
                 mk = Leaf \"a\"\n",
            ),
            (
                "Beta.sky",
                "module Beta exposing (Prim(..), mk)\n\
                 type Prim = Leaf String | Node Int\n\
                 mk : Prim\n\
                 mk = Node 7\n",
            ),
            (
                "Main.sky",
                "module Main exposing (main)\n\n\
                 import Sky.Core.Prelude exposing (..)\n\
                 import Std.Log exposing (println)\n\
                 import Alpha\n\
                 import Beta\n\n\
                 main =\n    \
                 let\n        \
                 _ = println (case Alpha.mk of\n                \
                 Alpha.Leaf s -> \"a Leaf \" ++ s\n                \
                 Alpha.Node n -> \"a Node \" ++ String.fromInt n)\n        \
                 _ = println (case Beta.mk of\n                \
                 Beta.Leaf s -> \"b Leaf \" ++ s\n                \
                 Beta.Node n -> \"b Node \" ++ String.fromInt n)\n    \
                 in\n    println \"done\"\n",
            ),
        ],
    );

    let source = emit_example_source(&repo, &project)
        .unwrap_or_else(|e| panic!("emit failed: {e}"));
    cleanup(&project);

    // Both modules' variant structs must be exercised by the `case` arms.
    // Pre-fix, the Alpha case asserted against `Beta_Prim_Leaf_V` (last-writer),
    // so `Alpha_Prim_Leaf_V` never appeared at a use site.
    assert!(
        source.contains(".(Alpha_Prim_Leaf_V)"),
        "the Alpha `case` must assert against Alpha_Prim_Leaf_V (C3 regression) — emitted:\n{source}"
    );
    assert!(
        source.contains(".(Beta_Prim_Leaf_V)"),
        "the Beta `case` must assert against Beta_Prim_Leaf_V — emitted:\n{source}"
    );

    // The Alpha `case` arms must not leak Beta's variant structs, and vice
    // versa. Check per-line to keep the assertion tight: no single line may
    // mix an `Alpha.mk` subject with a `Beta_Prim_*_V` assertion.
    for line in source.lines() {
        if line.contains("Alpha_mk()") {
            assert!(
                !line.contains("Beta_Prim_"),
                "an Alpha-subject case leaked a Beta variant struct (C3): {line}"
            );
        }
        if line.contains("Beta_mk()") {
            assert!(
                !line.contains("Alpha_Prim_"),
                "a Beta-subject case leaked an Alpha variant struct (C3): {line}"
            );
        }
    }
}

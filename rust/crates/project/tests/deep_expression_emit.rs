//! The whole front half (parse, resolve, infer, lower, emit) handles a deep
//! expression on a small thread stack.
//!
//! A left-nested `|>` pipeline is as deep as it is long, and every pass over
//! the expression tree recurses. A 1,600-step pipeline overflowed the 8 MiB
//! main-thread stack in lowering. Each recursive walker now grows its stack on
//! demand (`base::ensure_stack`), so the depth no longer depends on the thread
//! it runs on. This test emits the Go for a 4,000-step pipeline on a thread
//! with a 2 MiB stack, the size of an LSP worker; the `sky` binary's own
//! larger thread is not involved.
//!
//! Falsifier: make `base::ensure_stack` call `f()` directly and this test
//! binary aborts with "has overflowed its stack".

use std::path::PathBuf;

const SMALL_STACK: usize = 2 * 1024 * 1024;

/// The codec test's stack: well under what one unguarded walk down a
/// 1,500-arrow type needs in this profile, so a walker that recurses without
/// `base::ensure_stack` overflows here, as it does on a larger stack in an
/// optimised build.
const TINY_STACK: usize = 512 * 1024;

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

fn scratch_project(main_src: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "sky-deep-emit-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"deep-emit\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
    )
    .unwrap();
    std::fs::write(dir.join("src").join("Main.sky"), main_src).unwrap();
    dir
}

#[test]
fn a_four_thousand_step_pipeline_emits_on_a_2_mib_thread() {
    let src = format!(
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Core.String as String\n\
         import Std.Log exposing (println)\n\n\n\
         inc : Int -> Int\n\
         inc x =\n    x + 1\n\n\n\
         chain : Int\n\
         chain =\n    0\n{}\n\n\
         main =\n    println (String.fromInt chain)\n",
        "        |> inc\n".repeat(4000)
    );
    let repo = repo_root();
    let project = scratch_project(&src);
    let dir = project.clone();
    let emitted = std::thread::Builder::new()
        .stack_size(SMALL_STACK)
        .spawn(move || project::emit_example_source(&repo, &dir))
        .expect("spawn the small-stack thread")
        .join()
        .expect("emission must not panic");
    let _ = std::fs::remove_dir_all(&project);
    let go = emitted.unwrap_or_else(|e| panic!("emit failed: {e}"));
    // Every step reaches the Go: one call of `inc` per pipeline step.
    let calls = go.matches("Main_inc(").count();
    assert!(
        calls >= 4000,
        "expected one `inc` call per step, found {calls}"
    );
}

/// A generated record codec is as deep as its record is wide: the step types
/// are curried arrows with one argument per remaining field, and the type
/// layer's read-back table, owned rebuild and Go-type walk recurse down them.
/// A 1,500-field codec overflowed an 8 MiB stack in
/// `ty::tytable::TyTableBuilder::intern`. Check and lowering now grow the
/// stack on demand in those walkers too, so this runs on a 512 KiB thread.
///
/// Falsifier: drop `base::ensure_stack` from the type-table walkers and this
/// test binary aborts with "has overflowed its stack".
#[test]
fn a_fifteen_hundred_field_record_codec_checks_and_emits_on_a_small_thread() {
    let n = 1500;
    let fields: Vec<String> = (0..n).map(|i| format!("f{i} : String")).collect();
    let steps: Vec<String> = (0..n)
        .map(|i| format!("        |> Codec.field \"f{i}\" .f{i} Codec.string"))
        .collect();
    let blank: Vec<String> = (0..n).map(|i| format!("f{i} = \"\"")).collect();
    let src = format!(
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Codec as Codec exposing (Codec)\n\
         import Std.Log exposing (println)\n\n\n\
         type alias Big =\n    {{ {}\n    }}\n\n\n\
         bigCodec : Codec Big\nbigCodec =\n    Codec.object Big\n{}\n        |> Codec.buildObject\n\n\n\
         blank : Big\nblank =\n    {{ {} }}\n\n\n\
         main =\n    println (Codec.toJson bigCodec blank)\n",
        fields.join("\n    , "),
        steps.join("\n"),
        blank.join(", ")
    );
    let repo = repo_root();
    let project = scratch_project(&src);
    let dir = project.clone();
    let emitted = std::thread::Builder::new()
        .stack_size(TINY_STACK)
        .spawn(move || project::emit_example_source(&repo, &dir))
        .expect("spawn the small-stack thread")
        .join()
        .expect("check and emission must not panic");
    let _ = std::fs::remove_dir_all(&project);
    let go = emitted.unwrap_or_else(|e| panic!("emit failed: {e}"));
    assert!(
        go.contains("\"f1499\""),
        "the last field's codec step reaches the Go"
    );
}

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

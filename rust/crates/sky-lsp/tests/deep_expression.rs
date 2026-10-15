//! A deep expression does not overflow an LSP worker's stack.
//!
//! The server answers requests on tokio worker threads, which have a 2 MiB
//! stack (`server.rs`, `run`). Resolution and inference recurse over the
//! expression tree, and a left-nested `|>` pipeline is as deep as it is long, so
//! a 4,000-step pipeline in an open buffer aborted the whole server on its first
//! diagnostics or hover request. Each recursive walker now grows its stack on
//! demand (`base::ensure_stack`). This test runs the requests on a thread with
//! the same 2 MiB stack.
//!
//! Falsifier: make `base::ensure_stack` call `f()` directly and this test
//! binary aborts with "has overflowed its stack" (the codec case also aborts
//! when only the occurs check, `ty::unify::UnionFind::occurs_in`, loses its
//! guard).

use sky_lsp::Analysis;
use std::path::{Path, PathBuf};
use tower_lsp::lsp_types::{DiagnosticSeverity, HoverContents, Position, Url};

/// tokio's default worker thread stack.
const LSP_WORKER_STACK: usize = 2 * 1024 * 1024;

fn repo_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .ancestors()
        .find(|p| p.join("sky-stdlib").is_dir())
        .expect("sky-stdlib not found")
        .to_path_buf()
}

fn deep_source(steps: usize) -> String {
    format!(
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Core.String as String\n\
         import Std.Log exposing (println)\n\n\n\
         inc : Int -> Int\n\
         inc x =\n    x + 1\n\n\n\
         chain : Int\n\
         chain =\n    0\n{}\n\n\
         main =\n    println (String.fromInt chain)\n",
        "        |> inc\n".repeat(steps)
    )
}

#[test]
fn a_four_thousand_step_pipeline_on_a_2_mib_worker() {
    let root = repo_root();
    let worker = std::thread::Builder::new()
        .name("lsp-worker".to_string())
        .stack_size(LSP_WORKER_STACK)
        .spawn(move || {
            let src = deep_source(4000);
            let mut a = Analysis::new();
            a.load_stdlib(Some(&root));
            let u = Url::from_file_path("/tmp/lsp-deep-expression/src/Main.sky").unwrap();
            a.set_document(u.clone(), src.clone());
            let errors: Vec<String> = a
                .diagnostics(&u)
                .into_iter()
                .filter(|d| d.severity == Some(DiagnosticSeverity::ERROR))
                .map(|d| d.message)
                .collect();
            // Hover on `chain` at its declaration (`chain : Int`).
            let line = src
                .lines()
                .position(|l| l.starts_with("chain :"))
                .expect("the chain annotation") as u32;
            let hover = match a.hover(&u, Position { line, character: 1 }) {
                Some(h) => match h.contents {
                    HoverContents::Markup(m) => m.value,
                    _ => String::new(),
                },
                None => String::new(),
            };
            // And on the last `inc` of the pipeline, deepest in the tree.
            let last = src
                .lines()
                .collect::<Vec<_>>()
                .iter()
                .rposition(|l| l.trim() == "|> inc")
                .expect("a pipeline step") as u32;
            let deep_hover = a.hover(
                &u,
                Position {
                    line: last,
                    character: 12,
                },
            );
            (errors, hover, deep_hover.is_some())
        })
        .expect("spawn the worker thread");
    let (errors, hover, deep_hover) = worker.join().expect("the worker must not panic");
    assert!(
        errors.is_empty(),
        "the deep pipeline type-checks; no error diagnostics: {errors:?}"
    );
    assert!(
        hover.contains("Int"),
        "hover on `chain` shows its type: {hover:?}"
    );
    assert!(deep_hover, "hover on the deepest pipeline step answers");
}

fn codec_source(n: usize) -> String {
    let fields: Vec<String> = (0..n).map(|i| format!("f{i} : Int")).collect();
    let steps: String = (0..n)
        .map(|i| format!("        |> Codec.field \"f{i}\" .f{i} Codec.int\n"))
        .collect();
    format!(
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Codec as Codec exposing (Codec)\n\
         import Std.Log exposing (println)\n\n\n\
         type alias Big =\n    {{ {} }}\n\n\n\
         bigCodec : Codec Big\n\
         bigCodec =\n    Codec.object Big\n{steps}        |> Codec.buildObject\n\n\n\
         main =\n    println \"ok\"\n",
        fields.join(", ")
    )
}

/// The type-level spine: a record codec's pipeline has one step per field, and
/// each step's type is a curried constructor arrow as deep as the field count.
/// Unification and its occurs check recurse down that arrow, so a 1,500-field
/// codec overflowed a 2 MiB worker in `UnionFind::occurs_in` even once the
/// expression walkers were guarded.
#[test]
fn a_fifteen_hundred_field_record_codec_on_a_2_mib_worker() {
    let root = repo_root();
    let worker = std::thread::Builder::new()
        .name("lsp-worker".to_string())
        .stack_size(LSP_WORKER_STACK)
        .spawn(move || {
            let mut a = Analysis::new();
            a.load_stdlib(Some(&root));
            let u = Url::from_file_path("/tmp/lsp-deep-codec/src/Main.sky").unwrap();
            a.set_document(u.clone(), codec_source(1500));
            a.diagnostics(&u)
                .into_iter()
                .filter(|d| d.severity == Some(DiagnosticSeverity::ERROR))
                .map(|d| d.message)
                .collect::<Vec<_>>()
        })
        .expect("spawn the worker thread");
    let errors = worker.join().expect("the worker must not panic");
    assert!(
        errors.is_empty(),
        "the codec type-checks; no error diagnostics: {errors:?}"
    );
}

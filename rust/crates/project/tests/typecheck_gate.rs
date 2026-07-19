//! Driver-level accept/reject regression (the test whose ABSENCE let the bug
//! ship): the shipped `sky check`/`build`/`run` pipeline never invoked
//! `ty::check_modules`, so ill-typed programs the Haskell oracle rejects were
//! accepted (and, for `bad : Int = "text" + 1`, emitted Go that panics at
//! runtime). The `xtask reject` + `ty` unit gates tested `check_modules` in
//! ISOLATION and could not catch this — the hole was in the *driver seam*.
//!
//! This test drives the REAL `project::build_example` entry point (the exact
//! function `sky-cli` calls) on an ill-typed project and asserts the driver
//! halts BEFORE emit — so no Go is written and `go build` never runs. It needs
//! no `go` toolchain because a type error short-circuits ahead of emission.

use project::{build_example, BuildOptions};
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

/// Materialise a throwaway single-module project under a unique temp dir and
/// return its path. The caller owns cleanup.
fn scratch_project(tag: &str, main_src: &str) -> PathBuf {
    let uniq = format!(
        "sky-typecheck-gate-{tag}-{}-{}",
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
        "name = \"typecheck-gate\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
    )
    .unwrap();
    std::fs::write(dir.join("src").join("Main.sky"), main_src).unwrap();
    dir
}

fn opts_for(repo: &Path, project: &Path, out: &Path) -> BuildOptions {
    BuildOptions {
        repo_root: repo.to_path_buf(),
        example_dir: project.to_path_buf(),
        out_dir_name: "sky-out-test".to_string(),
        out_dir_abs: Some(out.to_path_buf()),
        run: false,
        stdin: None,
    }
}

/// An ill-typed program (`1 + "x"`) is REJECTED by the driver: no emit, no
/// `go build`, and the note carries the `[E2001]` type-mismatch diagnostic.
#[test]
fn driver_rejects_int_plus_string() {
    let repo = repo_root();
    let project = scratch_project(
        "arith",
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\n\
         main =\n    println (String.fromInt (1 + \"x\"))\n",
    );
    let out = project.join("sky-out-test");
    let report = build_example(&opts_for(&repo, &project, &out));

    assert!(
        !report.emitted,
        "driver EMITTED an ill-typed program (`1 + \"x\"`) — the type gate is not wired in; note: {}",
        report.note
    );
    assert!(!report.go_build_ok, "go build must not run on a type error");
    assert!(
        report.note.contains("E2001"),
        "expected an [E2001] type-mismatch diagnostic in the note, got: {}",
        report.note
    );
    // No `main.go` should have been written (emit is downstream of the gate).
    assert!(
        !out.join("main.go").exists(),
        "no Go should be emitted for a rejected program"
    );

    let _ = std::fs::remove_dir_all(&project);
}

/// The annotated `bad : Int = "text" + 1` case — the one that emitted
/// runtime-panicking Go — is likewise rejected before emit.
#[test]
fn driver_rejects_annotated_string_plus_int() {
    let repo = repo_root();
    let project = scratch_project(
        "annot",
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\n\
         bad : Int\n\
         bad =\n    \"text\" + 1\n\n\
         main =\n    println (String.fromInt bad)\n",
    );
    let out = project.join("sky-out-test");
    let report = build_example(&opts_for(&repo, &project, &out));

    assert!(
        !report.emitted,
        "driver EMITTED annotated `bad : Int = \"text\" + 1`; note: {}",
        report.note
    );
    assert!(
        report.note.contains("E2001"),
        "expected [E2001] in note, got: {}",
        report.note
    );

    let _ = std::fs::remove_dir_all(&project);
}

/// A non-exhaustive `case` (missing the `Nothing` arm) is REJECTED by the driver
/// with `[E3001]` — matching the Haskell oracle (exit 1). Sky treats a
/// non-exhaustive match as a HARD error; accepting it would emit Go that panics
/// at runtime the moment the missing arm is hit ("if it compiles it works").
#[test]
fn driver_rejects_non_exhaustive_case() {
    let repo = repo_root();
    let project = scratch_project(
        "nonexhaustive",
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\n\
         main =\n    case Just 5 of\n        Just n -> println (String.fromInt n)\n",
    );
    let out = project.join("sky-out-test");
    let report = build_example(&opts_for(&repo, &project, &out));

    assert!(
        !report.emitted,
        "driver EMITTED a non-exhaustive `case` — the exhaustiveness gate is not wired in; note: {}",
        report.note
    );
    assert!(!report.go_build_ok, "go build must not run on an exhaustiveness error");
    assert!(
        report.note.contains("E3001"),
        "expected an [E3001] non-exhaustive diagnostic in the note, got: {}",
        report.note
    );
    assert!(
        !out.join("main.go").exists(),
        "no Go should be emitted for a non-exhaustive program"
    );

    let _ = std::fs::remove_dir_all(&project);
}

/// The same match with an added wildcard `_ -> …` arm is EXHAUSTIVE and reaches
/// emission — proving the exhaustiveness gate does not over-reject (a covering
/// head suppresses the E3001).
#[test]
fn driver_accepts_exhaustive_case_with_wildcard() {
    let repo = repo_root();
    let project = scratch_project(
        "exhaustive-wildcard",
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\n\
         main =\n    case Just 5 of\n        Just n -> println (String.fromInt n)\n        _ -> println \"none\"\n",
    );
    let out = project.join("sky-out-test");
    let report = build_example(&opts_for(&repo, &project, &out));

    assert!(
        report.emitted,
        "driver failed to emit an EXHAUSTIVE `case` (wildcard arm present) — the exhaustiveness gate over-rejects; note: {}",
        report.note
    );

    let _ = std::fs::remove_dir_all(&project);
}

/// A well-typed program passes the type gate and reaches emission (the gate does
/// not over-reject). We stop at `emitted` rather than asserting `go build`
/// success so the test needs no `go` toolchain.
#[test]
fn driver_accepts_well_typed_and_emits() {
    let repo = repo_root();
    let project = scratch_project(
        "ok",
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\n\
         main =\n    println (String.fromInt (1 + 2))\n",
    );
    let out = project.join("sky-out-test");
    let report = build_example(&opts_for(&repo, &project, &out));

    assert!(
        report.emitted,
        "driver failed to emit a WELL-TYPED program — the type gate over-rejects; note: {}",
        report.note
    );

    let _ = std::fs::remove_dir_all(&project);
}

/// A duplicate top-level binding (`x = 1` then `x = 2`) is REJECTED before emit
/// with `[E1002]`. Sky has no multi-clause definitions; the oracle rejects such
/// a program (at `go build`: "x redeclared in this block"). The Rust resolver
/// used to last-wins-overwrite silently — accepting a program `go build`
/// refuses. This is the confirmed driver-seam gap this batch closes.
#[test]
fn driver_rejects_duplicate_toplevel_binding() {
    let repo = repo_root();
    let project = scratch_project(
        "dupbind",
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\n\
         x = 1\n\
         x = 2\n\n\
         main =\n    println (String.fromInt x)\n",
    );
    let out = project.join("sky-out-test");
    let report = build_example(&opts_for(&repo, &project, &out));

    assert!(
        !report.emitted,
        "driver EMITTED a duplicate top-level binding (`x` twice) — the name gate is not wired in; note: {}",
        report.note
    );
    assert!(!report.go_build_ok, "go build must not run on a redefinition");
    assert!(
        report.note.contains("E1002"),
        "expected an [E1002] duplicate-definition diagnostic in the note, got: {}",
        report.note
    );
    assert!(
        !out.join("main.go").exists(),
        "no Go should be emitted for a redefined binding"
    );

    let _ = std::fs::remove_dir_all(&project);
}

/// A non-linear parameter list (`f x x = …`) is REJECTED before emit with
/// `[E1003]` — the oracle rejects it at `go build` ("x redeclared").
#[test]
fn driver_rejects_duplicate_param() {
    let repo = repo_root();
    let project = scratch_project(
        "dupparam",
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\n\
         f x x =\n    x\n\n\
         main =\n    println (String.fromInt (f 1 2))\n",
    );
    let out = project.join("sky-out-test");
    let report = build_example(&opts_for(&repo, &project, &out));

    assert!(
        !report.emitted,
        "driver EMITTED a non-linear param list (`f x x`); note: {}",
        report.note
    );
    assert!(
        report.note.contains("E1003"),
        "expected [E1003] in note, got: {}",
        report.note
    );

    let _ = std::fs::remove_dir_all(&project);
}

/// A user ADT shadowing a Prelude name (`type Result a = Just a | Nothing`) is
/// REJECTED before emit with `[E1004]` — the oracle rejects it at canonicalise
/// time (audit §3.2 / CLAUDE.md v0.15.42).
#[test]
fn driver_rejects_prelude_shadow_adt() {
    let repo = repo_root();
    let project = scratch_project(
        "shadow",
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\n\
         type Result a\n    = Just a\n    | Nothing\n\n\
         main =\n    println \"hi\"\n",
    );
    let out = project.join("sky-out-test");
    let report = build_example(&opts_for(&repo, &project, &out));

    assert!(
        !report.emitted,
        "driver EMITTED an ADT shadowing Prelude `Result`/`Just`/`Nothing`; note: {}",
        report.note
    );
    assert!(
        report.note.contains("E1004"),
        "expected [E1004] in note, got: {}",
        report.note
    );

    let _ = std::fs::remove_dir_all(&project);
}

/// A literal-only `case` with no catch-all is REJECTED before emit with
/// `[E3001]` — an infinite-domain (Int) match can never be exhaustive without a
/// covering arm; the oracle rejects it ("Non-exhaustive patterns").
#[test]
fn driver_rejects_nonexhaustive_literal_case() {
    let repo = repo_root();
    let project = scratch_project(
        "litcase",
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\n\
         name n =\n    case n of\n        1 -> \"one\"\n        2 -> \"two\"\n\n\
         main =\n    println (name 1)\n",
    );
    let out = project.join("sky-out-test");
    let report = build_example(&opts_for(&repo, &project, &out));

    assert!(
        !report.emitted,
        "driver EMITTED a literal-only `case` with no catch-all; note: {}",
        report.note
    );
    assert!(
        report.note.contains("E3001"),
        "expected [E3001] in note, got: {}",
        report.note
    );

    let _ = std::fs::remove_dir_all(&project);
}

/// Legitimate lexical shadowing (an inner lambda re-using an outer param name)
/// is NOT a duplicate binder — the linearity gate flags only intra-group
/// duplicates. This program reaches emission (proves no over-rejection).
#[test]
fn driver_accepts_nested_shadowing() {
    let repo = repo_root();
    let project = scratch_project(
        "shadow-ok",
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\n\
         f x =\n    (\\x -> x + 1) x\n\n\
         main =\n    println (String.fromInt (f 5))\n",
    );
    let out = project.join("sky-out-test");
    let report = build_example(&opts_for(&repo, &project, &out));

    assert!(
        report.emitted,
        "driver failed to emit a program with legitimate nested shadowing — the linearity gate over-rejects; note: {}",
        report.note
    );

    let _ = std::fs::remove_dir_all(&project);
}

/// A bare operator section `(+)` (Sky has NO operator sections) is REJECTED
/// before emit with `[E0001]`. The parser RECOVERS from `(+)` and produces an
/// `Expr::Error` node that lowers to Go `nil`; without a parse-error gate
/// `sky check` reported success and `sky run` then panicked `NilDereference`
/// inside `Sky_Core_List_foldl` — a check-clean program crashing at runtime,
/// breaking both oracle parity (the Haskell rejects at exit 1) and
/// `sky check ≡ sky build` ("if it compiles it works"). This is the confirmed
/// driver-seam gap this change closes.
#[test]
fn driver_rejects_parse_error_op_section() {
    let repo = repo_root();
    let project = scratch_project(
        "opsection",
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Core.List as List\n\
         import Std.Log exposing (println)\n\n\
         main =\n    println (String.fromInt (List.foldl (+) 0 [ 1, 2, 3 ]))\n",
    );
    let out = project.join("sky-out-test");
    let report = build_example(&opts_for(&repo, &project, &out));

    assert!(
        !report.emitted,
        "driver EMITTED a program with a bare operator section `(+)` — the parse-error gate is not wired in; note: {}",
        report.note
    );
    assert!(!report.go_build_ok, "go build must not run on a parse error");
    assert!(
        report.note.contains("E0001"),
        "expected an [E0001] parse-error diagnostic in the note, got: {}",
        report.note
    );
    assert!(
        !out.join("main.go").exists(),
        "no Go should be emitted for a program with a parse error"
    );

    let _ = std::fs::remove_dir_all(&project);
}

/// The corrected program — a real `add` binding in place of the `(+)` section —
/// parses clean and reaches emission (proves the parse-error gate does not
/// over-reject: with 0 error nodes, well-formed code is unaffected).
#[test]
fn driver_accepts_corrected_named_fn_and_emits() {
    let repo = repo_root();
    let project = scratch_project(
        "opsection-fixed",
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Core.List as List\n\
         import Std.Log exposing (println)\n\n\
         add a b =\n    a + b\n\n\
         main =\n    println (String.fromInt (List.foldl add 0 [ 1, 2, 3 ]))\n",
    );
    let out = project.join("sky-out-test");
    let report = build_example(&opts_for(&repo, &project, &out));

    assert!(
        report.emitted,
        "driver failed to emit a WELL-FORMED program (real `add` instead of `(+)`) — the parse-error gate over-rejects; note: {}",
        report.note
    );

    let _ = std::fs::remove_dir_all(&project);
}

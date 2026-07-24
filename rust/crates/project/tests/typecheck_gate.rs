//! Driver-level accept/reject regression (the test whose ABSENCE let the bug
//! ship): the shipped `sky check`/`build`/`run` pipeline never invoked
//! `ty::check_modules`, so ill-typed programs the Haskell oracle rejects were
//! accepted (and, for `bad : Int = "text" + 1`, emitted Go that panics at
//! runtime). The `xtask reject` + `ty` unit gates tested `check_modules` in
//! ISOLATION and could not catch this — the hole was in the *driver seam*.
//!
//! This test drives the REAL `project::build_example` entry point (the exact
//! function `sky` calls) on an ill-typed project and asserts the driver
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
        assert!(
            dir.pop(),
            "could not locate repo root (no sky-stdlib ancestor)"
        );
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
        entry_module: None,
        progress: false,
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
        report.note.contains("TYPE ERROR") && report.note.contains("[E2001]"),
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
        report.note.contains("TYPE ERROR") && report.note.contains("[E2001]"),
        "expected [E2001] in note, got: {}",
        report.note
    );
    // Defect-1 regression: the E2001 caret must anchor at the offending RHS
    // expression, NOT the binding head. `bad =` is on line 7 and the RHS
    // `"text" + 1` is on line 8 — so the header location must read `:8:` (the
    // RHS line), the source-context window must show the `bad =` line above it,
    // and the caret must sit under a source line that carries `"text"`.
    assert!(
        report.note.contains("src/Main.sky:8:"),
        "E2001 caret must anchor on the RHS line (8), not the binding head; note: {}",
        report.note
    );
    assert!(
        report.note.contains("bad ="),
        "the context window should include the `bad =` line above the caret; note: {}",
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
    assert!(
        !report.go_build_ok,
        "go build must not run on an exhaustiveness error"
    );
    assert!(
        report.note.contains("MISSING PATTERNS"),
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

/// A point-free (arity-0) function-valued top-level def applied at a call site
/// (`inc = mkAdder 1` then `inc 41`) must EMIT and `go build`. The lowerer used
/// to wrap the already-forced CAF in an empty call → `Main_inc()()(41)`, which
/// `go build` rejects — a `sky check ≢ go build` hole.
#[test]
fn driver_pointfree_def_go_builds() {
    let repo = repo_root();
    let project = scratch_project(
        "pointfree",
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\n\
         mkAdder : Int -> Int -> Int\n\
         mkAdder n =\n    \\m -> n + m\n\n\
         inc : Int -> Int\n\
         inc =\n    mkAdder 1\n\n\
         main =\n    println (String.fromInt (inc 41))\n",
    );
    let out = project.join("sky-out-test");
    let report = build_example(&opts_for(&repo, &project, &out));
    assert!(
        report.emitted && report.go_build_ok,
        "point-free def must emit AND go build (was `Main_inc()()(41)`); note: {}",
        report.note
    );
    let _ = std::fs::remove_dir_all(&project);
}

/// A `let` group with an out-of-source-order forward reference
/// (`a = b + 1` before `b = 5`) must EMIT and `go build`. Sky allows the
/// forward reference; the lowerer used to emit the defs in source order, so Go
/// saw `v_a := v_b + 1` before `v_b` was declared (`undefined: v_1`). The
/// dependency topo-sort now emits `b` first.
#[test]
fn driver_let_forward_ref_go_builds() {
    let repo = repo_root();
    let project = scratch_project(
        "let-forward",
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\n\
         compute : Int -> Int\n\
         compute n =\n    let\n        c = a + b\n        a = n + 1\n        b = a * 2\n    in\n    c\n\n\
         main =\n    let\n        x = y + 1\n        y = 5\n    in\n    println (String.fromInt (x + compute 10))\n",
    );
    let out = project.join("sky-out-test");
    let report = build_example(&opts_for(&repo, &project, &out));
    assert!(
        report.emitted && report.go_build_ok,
        "forward-ref let must emit AND go build (was `undefined: v_N`); note: {}",
        report.note
    );
    let _ = std::fs::remove_dir_all(&project);
}

/// A `case` on a `Char` with char-literal patterns must EMIT and `go build`.
/// `Char` lowers to Go `rune`, but char literals + patterns used to lower to Go
/// string literals, so `go build` rejected `_subj == "a"` (rune vs string) — a
/// check≡build violation. Char literals/patterns now lower to `rune(<cp>)`.
#[test]
fn driver_char_pattern_go_builds() {
    let repo = repo_root();
    let project = scratch_project(
        "char-pat",
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Core.String as String\n\
         import Std.Log exposing (println)\n\n\
         classify : Char -> String\n\
         classify c =\n    case c of\n        'a' -> \"ay\"\n        _ -> \"other\"\n\n\
         firstOf : String -> String\n\
         firstOf s =\n    case String.toList s of\n        c :: _ -> classify c\n        [] -> \"empty\"\n\n\
         main =\n    let\n        _ = println (classify 'a')\n    in\n    println (firstOf \"abc\")\n",
    );
    let out = project.join("sky-out-test");
    let report = build_example(&opts_for(&repo, &project, &out));
    assert!(
        report.emitted && report.go_build_ok,
        "char-literal case must emit AND go build (was `_subj == \"a\"` rune/string clash); note: {}",
        report.note
    );
    let _ = std::fs::remove_dir_all(&project);
}

/// A partially-applied kernel (`String.append "hi "` — 1 arg to the 2-arg
/// `rt.String_append`) must EMIT and `go build`. The Go symbol isn't curried, so
/// a direct under-applied call was `not enough arguments`; the lowerer now
/// eta-expands it into a closure. Crucially, the eta-expansion is driven by the
/// kernel's RUNTIME param count (`abi_guard::runtime_arities`), not the curried
/// HM type — so a FULL application of a function-returning kernel (whose HM type
/// has more arrows than the runtime has params, e.g. a `Handler`-returning
/// middleware) is NOT mis-eta-expanded into an over-application. The full-app
/// side is covered by `examples/36-composite-server` in the build-run sweep;
/// this test locks the partial-app side.
#[test]
fn driver_kernel_partial_application_go_builds() {
    let repo = repo_root();
    let project = scratch_project(
        "kernel-partial",
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Core.List as List\n\
         import Std.Log exposing (println)\n\n\
         main =\n    let\n        g = String.append \"hi \"\n        tagged = List.map (String.append \">> \") [\"a\", \"b\"]\n    in\n    let\n        _ = println (g \"bob\")\n    in\n    println (String.join \", \" tagged)\n",
    );
    let out = project.join("sky-out-test");
    let report = build_example(&opts_for(&repo, &project, &out));
    assert!(
        report.emitted && report.go_build_ok,
        "partial kernel application must emit AND go build (was `not enough arguments`); note: {}",
        report.note
    );
    let _ = std::fs::remove_dir_all(&project);
}

/// Negative-literal `case` patterns must match their actual value — the prior
/// resolver stub lowered EVERY `-N` pattern to `Int(0)` (`_subj == 0`), a silent
/// miscompile: `case n of -1 -> …` never matched `-1` and wrongly matched `0`.
/// This BUILDS + RUNS the program and asserts the runtime output, since the bug
/// type-checks and `go build`s clean (the worst class — only visible at runtime).
#[test]
fn driver_negative_literal_patterns_match_at_runtime() {
    let repo = repo_root();
    let project = scratch_project(
        "neg-pat",
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\n\
         sign : Int -> String\n\
         sign n =\n    case n of\n        -1 -> \"neg-one\"\n        -5 -> \"neg-five\"\n        _ -> \"other\"\n\n\
         main =\n    let\n        _ = println (sign 0)\n        _ = println (sign (-1))\n        _ = println (sign (-5))\n    in\n    println (sign 7)\n",
    );
    let out = project.join("sky-out-test");
    let opts = BuildOptions {
        run: true,
        ..opts_for(&repo, &project, &out)
    };
    let report = build_example(&opts);
    let stdout = report.run_stdout.clone().unwrap_or_default();
    assert!(
        report.go_build_ok && report.run_ok == Some(true),
        "neg-pattern program must build + run; note: {} stderr: {:?}",
        report.note,
        report.run_stderr
    );
    // Order: sign 0 -> other, sign -1 -> neg-one, sign -5 -> neg-five, sign 7 -> other.
    assert_eq!(
        stdout.lines().collect::<Vec<_>>(),
        vec!["other", "neg-one", "neg-five", "other"],
        "negative patterns matched wrong values (was all `== 0`); stdout: {stdout:?}"
    );
    let _ = std::fs::remove_dir_all(&project);
}

/// Prelude `min` / `max` / `compare` must build AND run — they were registered
/// as valid HIR kernel names (so they type-checked) but had no codegen mapping
/// (`min`/`max` absent; `compare` → non-existent `rt.Basics_compare`), so a
/// well-typed `min 3 5` passed `sky check` then failed `go build` [E4005] — a
/// check≡build violation for documented primitives.
#[test]
fn driver_prelude_min_max_compare_build_and_run() {
    let repo = repo_root();
    let project = scratch_project(
        "min-max-compare",
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\n\
         main =\n    let\n        _ = println (String.fromInt (min 3 5))\n        _ = println (String.fromInt (max 3 5))\n    in\n    println (String.fromInt (compare 5 3))\n",
    );
    let out = project.join("sky-out-test");
    let opts = BuildOptions {
        run: true,
        ..opts_for(&repo, &project, &out)
    };
    let report = build_example(&opts);
    assert!(
        report.go_build_ok && report.run_ok == Some(true),
        "min/max/compare must build + run (was E4005); note: {} stderr: {:?}",
        report.note,
        report.run_stderr
    );
    assert_eq!(
        report
            .run_stdout
            .clone()
            .unwrap_or_default()
            .lines()
            .collect::<Vec<_>>(),
        vec!["3", "5", "1"],
        "min 3 5=3, max 3 5=5, compare 5 3=1; stdout: {:?}",
        report.run_stdout
    );
    let _ = std::fs::remove_dir_all(&project);
}

/// The entry module is derived from the file's `module <Name>` header, NOT
/// hardcoded to `Main`. A project whose entry declares `module App` must build
/// (the oracle builds it fine); the pre-fix driver hardcoded `n == "Main"` and
/// rejected it with `no entry module named Main`. `BuildOptions.entry_module`
/// carries the CLI-derived header name.
#[test]
fn driver_honours_non_main_entry_module() {
    let repo = repo_root();
    // A single-module project whose entry file declares `module App`, not Main.
    let uniq = format!(
        "sky-entry-mod-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    );
    let project = std::env::temp_dir().join(uniq);
    std::fs::create_dir_all(project.join("src")).unwrap();
    std::fs::write(
        project.join("sky.toml"),
        "name = \"entry-mod\"\nversion = \"0.1.0\"\nentry = \"src/App.sky\"\n",
    )
    .unwrap();
    std::fs::write(
        project.join("src").join("App.sky"),
        "module App exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\n\
         main =\n    println \"from App\"\n",
    )
    .unwrap();

    let out = project.join("sky-out-test");
    let opts = BuildOptions {
        entry_module: Some("App".to_string()),
        progress: false,
        ..opts_for(&repo, &project, &out)
    };
    let report = build_example(&opts);
    assert!(
        report.emitted,
        "driver rejected a non-`Main` entry module (`module App`) — entry detection is still hardcoded to Main; note: {}",
        report.note
    );

    let _ = std::fs::remove_dir_all(&project);
}

/// A `case` whose only arm is a wildcard (`case n of _ -> …`) never reads the
/// subject. The lowerer used to unconditionally bind `_subj := subj`, so Go
/// rejected it (`declared and not used: _subj`) — valid Sky emitted un-buildable
/// Go, a `sky check ≢ go build` hole. It must now EMIT and `go build` cleanly
/// (the subject is `_ = subj`-discarded when unread).
#[test]
fn driver_wildcard_case_subject_go_builds() {
    let repo = repo_root();
    let project = scratch_project(
        "wildcase",
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\n\
         g : Int -> Int\n\
         g n =\n    case n of\n        _ ->\n            7\n\n\
         main =\n    println (String.fromInt (g 3))\n",
    );
    let out = project.join("sky-out-test");
    let report = build_example(&opts_for(&repo, &project, &out));

    assert!(
        report.emitted,
        "well-typed wildcard-case program failed to emit; note: {}",
        report.note
    );
    assert!(
        report.go_build_ok,
        "`sky check ≢ go build` hole: emitted Go for an all-wildcard `case` did \
         not `go build` (unused `_subj`); note: {}",
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
    assert!(
        !report.go_build_ok,
        "go build must not run on a redefinition"
    );
    assert!(
        report.note.contains("DUPLICATE DEFINITION"),
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
        report.note.contains("DUPLICATE PATTERN VARIABLE"),
        "expected [E1003] in note, got: {}",
        report.note
    );

    let _ = std::fs::remove_dir_all(&project);
}

/// A user ADT shadowing a Prelude name (`type Result a = Just a | Nothing`) is
/// REJECTED before emit with `[E1004]` — the oracle rejects it at canonicalise
/// time (audit §3.2).
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
        report.note.contains("SHADOWED NAME"),
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
        report.note.contains("MISSING PATTERNS"),
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
    assert!(
        !report.go_build_ok,
        "go build must not run on a parse error"
    );
    assert!(
        report.note.contains("PARSE ERROR"),
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

/// F5 eta-expansion lock — a lambda spine that bottoms out in a func VALUE (not
/// a nested-lambda node) when the declared curried type carries MORE arrows than
/// the lambda binds. `makeF a = \_ -> add1` (where `add1 : Int -> Int`) has the
/// body `add1` — a func value, not a `\… -> …` node — so the `lower_lambda`
/// spine-absorption loop breaks early. Pre-fix the emitted closure had FEWER
/// params than the declared flat `func(any,int) int` type AND forced the
/// func-value body through the final scalar codomain via a spurious
/// `rt.AsInt(Main_add1)` — a runtime `TypeMismatch` panic (`rt.AsInt: … got
/// func(int) int`) the Haskell oracle never produces (it builds + runs → `3`).
///
/// The fix ETA-EXPANDS: it synthesises the missing params and APPLIES the body
/// (a func value) to them, so the emitted closure is `func(_ any, _eta0 int) int
/// { return Main_add1(_eta0) }` — arity matches the declared flat func type and
/// the func-value body is called, not coerced to a scalar. This is the
/// residual-half companion to `examples/41-nested-curry` (audit #7b), which
/// locks the nested-lambda-chain case.
#[test]
fn driver_eta_expands_func_value_lambda_body() {
    let repo = repo_root();
    let project = scratch_project(
        "eta-func-value",
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\n\
         add1 : Int -> Int\n\
         add1 x =\n    x + 1\n\n\
         makeF : Int -> Int -> (Int -> Int)\n\
         makeF a =\n    \\_ -> add1\n\n\
         main =\n    println (String.fromInt (makeF 1 5 2))\n",
    );
    let out = project.join("sky-out-test");
    let report = build_example(&opts_for(&repo, &project, &out));

    assert!(
        report.emitted,
        "driver failed to emit the eta-residual program; note: {}",
        report.note
    );
    let go_src = std::fs::read_to_string(out.join("main.go")).unwrap_or_default();
    // The pre-fix bug signature: the func-value body `add1` coerced to a scalar
    // via `rt.AsInt` (→ runtime TypeMismatch panic). Must be gone.
    assert!(
        !go_src.contains("rt.AsInt(Main_add1)"),
        "F5 REGRESSION — the eta-residual func-value body `add1` was coerced to a \
         scalar via rt.AsInt instead of being applied to the eta-expanded params.\n{go_src}"
    );
    // The fix synthesises eta params and applies the body to them.
    assert!(
        go_src.contains("_eta"),
        "F5 — eta-expansion did not synthesise the missing params.\n{go_src}"
    );

    let _ = std::fs::remove_dir_all(&project);
}

/// A typo of a Sky stdlib module (`Std.Lst` for `Std.List`) must be diagnosed
/// as an unknown Sky module, NOT as a missing Go-FFI package with a "run
/// sky install" hint (which can never fetch a Sky stdlib module). Regression
/// for the real-world sweep finding: the misleading FFI-surface error.
#[test]
fn driver_stdlib_module_typo_is_not_an_ffi_install_hint() {
    let repo = repo_root();
    let project = scratch_project(
        "stdlibtypo",
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Core.Task as Task\n\
         import Std.Lst as L\n\n\
         main =\n    let _ = L.length [1, 2, 3]\n    in Task.succeed ()\n",
    );
    let out = project.join("sky-out-test");
    let report = build_example(&opts_for(&repo, &project, &out));

    assert!(
        report.note.contains("unknown Sky module") && report.note.contains("Std.Lst"),
        "expected an unknown-Sky-module diagnostic, got: {}",
        report.note
    );
    assert!(
        !report.note.contains("Run `sky install`"),
        "a Sky-namespaced typo must NOT suggest `sky install` (it can't fetch stdlib); note: {}",
        report.note
    );

    let _ = std::fs::remove_dir_all(&project);
}

/// Elm semantics: a consumer cannot import a name the source module does not
/// expose. `Helper` exposes only `publicFn`; importing `privateFn` must be a
/// hard [E1011] error (was silently accepted — the export list wasn't enforced).
#[test]
fn driver_rejects_import_of_non_exported_name() {
    let repo = repo_root();
    let project = scratch_project(
        "notexposed",
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Core.Task as Task\n\
         import Std.Log exposing (println)\n\
         import Helper exposing (publicFn, privateFn)\n\n\
         main =\n    let _ = println (String.fromInt (publicFn 10))\n    in Task.succeed ()\n",
    );
    // A sibling module that keeps `privateFn` module-private.
    std::fs::write(
        project.join("src").join("Helper.sky"),
        "module Helper exposing (publicFn)\n\n\
         publicFn : Int -> Int\n\
         publicFn x =\n    x + 1\n\n\
         privateFn : Int -> Int\n\
         privateFn x =\n    x * 2\n",
    )
    .unwrap();

    let out = project.join("sky-out-test");
    let report = build_example(&opts_for(&repo, &project, &out));

    assert!(
        report.note.contains("[E1011]") && report.note.contains("does not expose"),
        "expected an [E1011] not-exposed diagnostic, got: {}",
        report.note
    );
    assert!(
        report.note.contains("privateFn") && report.note.contains("Helper"),
        "diagnostic should name the module and the private name; got: {}",
        report.note
    );
    // publicFn (legitimately exposed) must NOT be flagged.
    assert!(
        !report.note.contains("does not expose `publicFn`"),
        "publicFn is exported and must not be flagged; got: {}",
        report.note
    );

    let _ = std::fs::remove_dir_all(&project);
}

/// sky.toml `bin` renames the output binary: `go build -o <bin>` and the
/// artifact lands at `<out>/<bin>` (not the hardcoded `app`).
#[test]
fn driver_bin_key_renames_output_binary() {
    let repo = repo_root();
    let project = scratch_project(
        "binkey",
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Core.Task as Task\n\
         import Std.Log exposing (println)\n\n\
         main =\n    let _ = println \"hi\"\n    in Task.succeed ()\n",
    );
    std::fs::write(
        project.join("sky.toml"),
        "name = \"binkey\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\nbin = \"myserver\"\n",
    )
    .unwrap();
    let out = project.join("sky-out-test");
    let report = build_example(&opts_for(&repo, &project, &out));

    assert!(
        report.go_build_ok,
        "build must succeed; note: {}",
        report.note
    );
    assert!(
        out.join("myserver").is_file(),
        "binary must be produced at <out>/myserver (the sky.toml `bin`)"
    );
    assert!(
        !out.join("app").is_file(),
        "the hardcoded `app` name must NOT be produced when `bin` is set"
    );

    let _ = std::fs::remove_dir_all(&project);
}

/// sky.toml `root` relocates module discovery: a project whose sources live in
/// `lib/` (not `src/`) builds when `root = "lib"`.
#[test]
fn driver_source_root_relocates_discovery() {
    let repo = repo_root();
    // Build a project by hand with sources under `lib/` instead of `src/`.
    let uniq = format!("sky-srcroot-{}", std::process::id());
    let project = std::env::temp_dir().join(uniq);
    std::fs::create_dir_all(project.join("lib")).unwrap();
    std::fs::write(
        project.join("sky.toml"),
        "name = \"srcroot\"\nversion = \"0.1.0\"\nentry = \"lib/Main.sky\"\nroot = \"lib\"\n",
    )
    .unwrap();
    std::fs::write(
        project.join("lib").join("Main.sky"),
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Core.Task as Task\n\
         import Std.Log exposing (println)\n\
         import Helper exposing (greet)\n\n\
         main =\n    let _ = println (greet \"x\")\n    in Task.succeed ()\n",
    )
    .unwrap();
    std::fs::write(
        project.join("lib").join("Helper.sky"),
        "module Helper exposing (greet)\n\n\
         greet : String -> String\n\
         greet name =\n    \"hi \" ++ name\n",
    )
    .unwrap();

    let out = project.join("sky-out-test");
    let mut opts = opts_for(&repo, &project, &out);
    opts.entry_module = Some("Main".to_string());
    let report = build_example(&opts);

    assert!(
        report.go_build_ok,
        "project with sources under lib/ (root=\"lib\") must build; note: {}",
        report.note
    );

    let _ = std::fs::remove_dir_all(&project);
}

/// Regression: a type error must be reported under the file it actually occurs
/// in, even when a `.skydeps` module shares a NAME with a local module.
///
/// A diagnostic span carries the module's `ModuleId`. The display-path map used
/// by the Elm-style renderer USED to be keyed by the `SourceFile`'s `file_id`
/// (a load-order ordinal), on the assumption that `file_id == ModuleId`. That
/// holds for a project with no Sky dependencies, but a dep module that shares a
/// name with a local one makes `add_module` return the EXISTING id on re-add
/// (dedup), shifting `file_id` and `ModuleId` apart for every module loaded
/// after it — so an error resolved to the WRONG file's path (a sibling). This
/// sets up exactly that shift and asserts the error names its own file.
#[test]
fn diagnostic_names_correct_file_across_skydep_module_id_shift() {
    let repo = repo_root();
    let uniq = format!(
        "sky-typecheck-gate-skydepshift-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    );
    let dir = std::env::temp_dir().join(uniq);
    let src = dir.join("src");
    std::fs::create_dir_all(&src).unwrap();
    // A Sky dependency is declared; its source lives under `.skydeps/<slug>/src/`.
    let slug = "github.com_test_collidedep";
    let dep_src = dir.join(".skydeps").join(slug).join("src");
    std::fs::create_dir_all(&dep_src).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"skydep-shift\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n\
         [dependencies]\n\"github.com/test/collidedep\" = \"v0.1.0\"\n",
    )
    .unwrap();
    // The dep ships a module named `AaShared` …
    std::fs::write(
        dep_src.join("AaShared.sky"),
        "module AaShared exposing (depValue)\n\ndepValue : Int\ndepValue =\n    1\n",
    )
    .unwrap();
    // … and so does the local project (sorts FIRST among locals → the dedup, and
    // thus the file_id/ModuleId shift, applies to every later local module).
    std::fs::write(
        src.join("AaShared.sky"),
        "module AaShared exposing (localValue)\n\nlocalValue : Int\nlocalValue =\n    2\n",
    )
    .unwrap();
    // The module with the type error sorts LAST, so its ModuleId is shifted away
    // from its file_id — the exact condition that misnamed the file pre-fix.
    std::fs::write(
        src.join("ZzBroken.sky"),
        "module ZzBroken exposing (broken)\n\nimport Sky.Core.Prelude exposing (..)\n\n\
         broken : String -> a\nbroken s =\n    42\n",
    )
    .unwrap();
    std::fs::write(
        src.join("Main.sky"),
        "module Main exposing (main)\n\nimport Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\nimport AaShared\nimport ZzBroken\n\n\
         main =\n    println (String.fromInt AaShared.localValue)\n",
    )
    .unwrap();

    let out = dir.join("sky-out-test");
    let report = build_example(&opts_for(&repo, &dir, &out));

    assert!(
        report.note.contains("TYPE ERROR") && report.note.contains("[E2001]"),
        "expected the [E2001] rigid-var error, got: {}",
        report.note
    );
    // The error is in ZzBroken.sky — it MUST be named there, not under a sibling
    // (pre-fix it was reported under `src/Main.sky`, the module whose file_id
    // matched ZzBroken's shifted ModuleId).
    assert!(
        report.note.contains("src/ZzBroken.sky:"),
        "the type error must be reported under its own file src/ZzBroken.sky; note: {}",
        report.note
    );
    assert!(
        !report.note.contains("src/Main.sky:") && !report.note.contains("src/AaShared.sky:"),
        "the type error must NOT be misattributed to a sibling module; note: {}",
        report.note
    );

    let _ = std::fs::remove_dir_all(&dir);
}

/// Helper: build a single-file program and assert it type-checks AND `go build`
/// succeeds. Used by the boundary-codegen regressions (#161/#162/#163) — these
/// fail in the Haskell oracle too, so they can't be oracle-matched golden
/// examples; a Rust-only build assertion is the regression guard.
fn assert_single_file_builds(tag: &str, main_src: &str) {
    let repo = repo_root();
    let project = scratch_project(tag, main_src);
    let out = project.join("sky-out-test");
    let report = build_example(&opts_for(&repo, &project, &out));
    assert!(
        report.emitted && report.go_build_ok,
        "[{tag}] expected type-check + go build to succeed; note: {}, go_stderr: {}",
        report.note, report.go_build_stderr
    );
    let _ = std::fs::remove_dir_all(&project);
}

#[test]
fn issue161_field_accessor_in_pipeline_builds() {
    assert_single_file_builds(
        "i161",
        "module Main exposing (main)\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\
         type Session = Session Config\n\
         type alias Config = { terminalLineHeight : Int, wpmTarget : Int }\n\
         config : Session -> Config\nconfig (Session c) = c\n\
         terminalLineHeight : Session -> Int\n\
         terminalLineHeight session = session |> config |> .terminalLineHeight\n\
         main =\n    let\n        s = Session { terminalLineHeight = 24, wpmTarget = 60 }\n\
         \x20       _ = println (String.fromInt (terminalLineHeight s))\n    in ()\n",
    );
}

#[test]
fn issue162_recursive_let_binding_builds() {
    assert_single_file_builds(
        "i162",
        "module Main exposing (main)\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\
         takeWhile : (a -> Bool) -> List a -> List a\n\
         takeWhile predicate list =\n    let\n\
         \x20       helper memo xs = case xs of\n\
         \x20           [] -> List.reverse memo\n\
         \x20           x :: rest -> if predicate x then helper (x :: memo) rest else List.reverse memo\n\
         \x20   in helper [] list\n\
         main =\n    let\n        r = takeWhile (\\n -> n < 3) [ 1, 2, 3, 4, 1 ]\n\
         \x20       _ = println (String.fromInt (List.length r))\n    in ()\n",
    );
}

#[test]
fn issue163_polymorphic_list_into_lambda_pipe_builds() {
    assert_single_file_builds(
        "i163",
        "module Main exposing (main)\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\
         type Step = Typeable Char | EnterChar | End\n\
         dropWhile : (a -> Bool) -> List a -> List a\n\
         dropWhile p list = case list of\n\
         \x20   [] -> []\n\
         \x20   x :: xs -> if p x then dropWhile p xs else list\n\
         trimSteps : List Step -> Int\n\
         trimSteps steps = steps |> dropWhile (\\s -> s == EnterChar) |> (\\rest -> List.length rest)\n\
         main =\n    let\n        r = trimSteps [ EnterChar, Typeable 'a', End ]\n\
         \x20       _ = println (String.fromInt r)\n    in ()\n",
    );
}

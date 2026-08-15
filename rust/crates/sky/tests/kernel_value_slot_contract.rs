//! Regression: a KERNEL referenced as a *value* must be emitted with its REAL
//! Go type, not with the type of the slot it lands in.
//!
//! `sky check ≡ sky build` (AGENTS.md) is the contract these tests defend: both
//! verbs invoke `go build` on the emitted Go, so a program that type-checks
//! clean and then fails `go build` is a compiler defect, never a user error.
//!
//! WHY. Every runtime kernel is `any`-based — `func String_append(a, b any) any`,
//! `func Uuid_v7() any` (see the `lower::kernel` module doc). The lowerer's one
//! uniform rule is therefore "widen args to `any`, coerce the `any` return to
//! the call's typed slot". `lower_var` broke that rule for kernels referenced as
//! VALUES: it built the `GoExpr` with `actual` — the type of the slot the
//! reference lands in — as if the raw runtime symbol already had that type.
//! Because the node then *claimed* to be the expected type, `coerce_if_needed`
//! saw `x.ty == expected` and inserted nothing, and the untyped runtime symbol
//! reached a typed Go slot verbatim:
//!
//!   * arity ≥ 1, point-free (`joinStr = String.append`):
//!     `return rt.String_append` — `cannot use rt.String_append (value of type
//!     func(a any, b any) any) as func(string, string) string value`.
//!   * arity 0, in a `Task` slot (`freshId _ = Uuid.v7`):
//!     `return rt.Uuid_v7()` — `cannot use rt.Uuid_v7() (value of interface
//!     type any) as rt.SkyTask[Sky_Core_Error_Error, string] value`.
//!
//! Both are ONE root cause at three sites in `lower/src/lower.rs`: the
//! kernel-alias value arm, the `Res::Kernel` value arm, and
//! `nullary_kernel_value`. The fix makes each site honest about the symbol's
//! real Go type and bridges with the mechanisms that already exist —
//! `kernel_partial` (eta-expansion, as a partial application already uses) for
//! arity ≥ 1, and `coerce_if_needed` for arity 0.
//!
//! Each test has two legs, per the doctrine in `cli_verb_flow.rs` — a test that
//! skips and reports green is the defect this suite exists to kill:
//!
//!   * the EMITTED-GO leg always runs. `sky build` writes `sky-out/main.go`
//!     before it invokes `go build`, so the shape is checkable with no Go
//!     toolchain at all.
//!   * the BUILD leg runs only when `go` is on `PATH`, and asserts the thing
//!     that actually regressed: `go build` accepts the emitted Go.

use std::path::{Path, PathBuf};
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

fn scratch(tag: &str) -> PathBuf {
    let uniq = format!(
        "sky-kernelslot-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    );
    let dir = std::env::temp_dir().join(uniq);
    std::fs::create_dir_all(dir.join("src")).unwrap();
    dir
}

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

fn project(tag: &str, main_src: &str) -> PathBuf {
    let dir = scratch(tag);
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"kernelslot\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
    )
    .unwrap();
    std::fs::write(dir.join("src").join("Main.sky"), main_src).unwrap();
    dir
}

fn build(dir: &Path) -> String {
    let out = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky build");
    let mut s = String::from_utf8_lossy(&out.stdout).into_owned();
    s.push_str(&String::from_utf8_lossy(&out.stderr));
    s
}

fn emitted_go(dir: &Path, log: &str) -> String {
    let main_go = dir.join("sky-out").join("main.go");
    assert!(
        main_go.is_file(),
        "sky build must emit sky-out/main.go (build log: {log})"
    );
    std::fs::read_to_string(&main_go).unwrap()
}

/// The Go body of `func <name>(` in `src`, up to the closing brace column-0.
fn func_body<'a>(src: &'a str, name: &str) -> &'a str {
    let needle = format!("func {name}(");
    let at = src
        .find(&needle)
        .unwrap_or_else(|| panic!("emitted Go must define {name}:\n{src}"));
    let rest = &src[at..];
    let end = rest.find("\n}").map(|i| i + 2).unwrap_or(rest.len());
    &rest[..end]
}

// ===========================================================================
// Defect 1 — point-free kernel alias (arity ≥ 1) in a concretely-typed slot.
// ===========================================================================

const POINT_FREE: &str = "module Main exposing (main)\n\n\
     import Sky.Core.Prelude exposing (..)\n\
     import Sky.Core.String as String\n\
     import Std.Log exposing (println)\n\n\
     tickle : String -> String\n\
     tickle =\n    String.toUpper\n\n\
     joinStr : String -> String -> String\n\
     joinStr =\n    String.append\n\n\
     main =\n    println (joinStr (tickle \"hi\") \"lo\")\n";

/// Emission leg — the raw `any`-based runtime symbol must never appear as the
/// whole returned value of a concretely-typed Go function. `return
/// rt.String_append` in a `func(string, string) string` slot is the defect
/// verbatim.
#[test]
fn point_free_kernel_alias_is_not_emitted_as_a_bare_runtime_symbol() {
    let dir = project("pointfree-emit", POINT_FREE);
    let log = build(&dir);
    let src = emitted_go(&dir, &log);

    for (def, sym) in [("Main_joinStr", "rt.String_append"), ("Main_tickle", "rt.String_toUpper")] {
        let body = func_body(&src, def);
        assert!(
            !body.contains(&format!("return {sym}\n")) && !body.contains(&format!("{{ return {sym} }}")),
            "{def} must not return the bare `any`-based kernel symbol {sym} — it is a \
             func(any…) any and the slot is a concretely-typed Go func, which `go build` \
             rejects. Emitted:\n{body}"
        );
        assert!(
            body.contains("func("),
            "{def} must bridge the kernel through an eta-expanded closure whose params \
             carry the slot's concrete types. Emitted:\n{body}"
        );
    }
    let _ = std::fs::remove_dir_all(&dir);
}

/// Build leg — the contract itself: it type-checks, so it must `go build`.
#[test]
fn point_free_kernel_alias_go_builds() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = project("pointfree-build", POINT_FREE);
    let log = build(&dir);
    assert!(
        !log.contains("go build failed"),
        "`sky check` ≡ `sky build`: a point-free kernel alias type-checks, so the \
         emitted Go must compile. Build log:\n{log}"
    );
    assert!(
        dir.join("sky-out").join("app").is_file(),
        "build must produce sky-out/app. Log:\n{log}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

// ===========================================================================
// Defect 2 — nullary kernel value (arity 0) in a concrete `Task` slot.
// ===========================================================================

const NULLARY_TASK: &str = "module Main exposing (main)\n\n\
     import Sky.Core.Prelude exposing (..)\n\
     import Sky.Core.String as String\n\
     import Sky.Core.Task as Task\n\
     import Sky.Core.Uuid as Uuid\n\
     import Std.Log exposing (println)\n\n\
     freshId : () -> Task Error String\n\
     freshId _ =\n    Uuid.v7\n\n\
     main =\n\
     \x20   case Task.perform (freshId ()) of\n\
     \x20       Ok u ->\n            println (String.fromInt (String.length u))\n\n\
     \x20       Err _ ->\n            println \"err\"\n";

/// Emission leg — a nullary kernel returns Go `any`; landing it in a
/// `rt.SkyTask[...]` slot without a coercion is the defect verbatim.
#[test]
fn nullary_kernel_in_task_slot_is_coerced_not_returned_raw() {
    let dir = project("nullary-emit", NULLARY_TASK);
    let log = build(&dir);
    let src = emitted_go(&dir, &log);
    let body = func_body(&src, "Main_freshId");

    assert!(
        body.contains("rt.Uuid_v7()"),
        "Main_freshId must CALL the zero-arg runtime symbol. Emitted:\n{body}"
    );
    assert!(
        !body.contains("return rt.Uuid_v7()"),
        "a nullary kernel returns Go `any`; returning it raw into a \
         rt.SkyTask[...] slot is exactly what `go build` rejects \
         (\"need type assertion\"). Emitted:\n{body}"
    );
    assert!(
        body.contains("rt.TaskCoerceT["),
        "the `any` result must be narrowed into the concrete Task slot via \
         rt.TaskCoerceT. Emitted:\n{body}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

/// Build leg — the contract itself.
#[test]
fn nullary_kernel_in_task_slot_go_builds() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = project("nullary-build", NULLARY_TASK);
    let log = build(&dir);
    assert!(
        !log.contains("go build failed"),
        "`sky check` ≡ `sky build`: a nullary kernel in a Task slot type-checks, \
         so the emitted Go must compile. Build log:\n{log}"
    );
    assert!(
        dir.join("sky-out").join("app").is_file(),
        "build must produce sky-out/app. Log:\n{log}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

// ===========================================================================
// Guard boundaries — the shapes that were ALREADY correct must not change.
// `kernel_value_eta` returns `None` for them and `coerce_if_needed`
// short-circuits on an `any` slot, so their emission stays byte-identical.
// These walk D3 (annotation state) and D2 (map slots) of
// `docs/rust-rewrite/13-change-verification-and-edge-cases.md`.
// ===========================================================================

const BOUNDARIES: &str = "module Main exposing (main)\n\n\
     import Sky.Core.Prelude exposing (..)\n\
     import Sky.Core.String as String\n\
     import Sky.Core.List as List\n\
     import Sky.Core.Dict as Dict\n\
     import Std.Log exposing (println)\n\n\
     joinLoose =\n    String.append\n\n\
     idish : a -> a\n\
     idish =\n    identity\n\n\
     shout : List String -> List String\n\
     shout xs =\n    List.map String.toUpper xs\n\n\
     emptyCounts : Dict String Int\n\
     emptyCounts =\n    Dict.empty\n\n\
     main =\n\
     \x20   let\n\
     \x20       _ =\n            println (joinLoose \"a\" \"b\")\n\n\
     \x20       _ =\n            println (idish \"ident\")\n\n\
     \x20       _ =\n            println (String.join \",\" (shout [ \"x\", \"y\" ]))\n\n\
     \x20   in\n\
     \x20   println (String.fromInt (Dict.size (Dict.insert \"a\" 1 emptyCounts)))\n";

/// The decisive boundary: a kernel used as a HOF CALLBACK is only ever widened
/// (`any(rt.String_toUpper)` is valid Go), so it must keep the bare symbol. This
/// is the assertion that pins the fix to the SLOT rather than to the reference's
/// own inferred type — `String.toUpper` inside `List.map` has a perfectly
/// concrete inferred `func(string) string`, and an earlier version of the fix
/// eta-expanded it, widening the `coerce-floor` ratchet across 13 examples.
///
/// D3 note: annotation state does NOT change the point-free outcome here.
/// `String.append` is monomorphic, so `joinLoose = String.append` gets a
/// concrete slot with or without a signature — it is the same defect shape and
/// eta-expands either way.
#[test]
fn hof_callback_kernel_keeps_the_bare_symbol() {
    let dir = project("boundaries-emit", BOUNDARIES);
    let log = build(&dir);
    let src = emitted_go(&dir, &log);

    let shout = func_body(&src, "Main_shout");
    assert!(
        shout.contains("rt.String_toUpper") && !shout.contains("func(_p"),
        "a kernel passed as a HOF callback is only widened, so it must stay the \
         bare symbol — eta-expanding it adds a runtime coercion to output that \
         was already correct. Emitted:\n{shout}"
    );

    // D3 — the unannotated point-free alias is the SAME defect shape (monomorphic
    // kernel ⇒ concrete slot) and must be bridged, not left bare.
    let loose = func_body(&src, "Main_joinLoose");
    assert!(
        loose.contains("func(_p"),
        "an unannotated point-free alias of a MONOMORPHIC kernel still lands in a \
         concretely-typed slot, so it must be eta-expanded too. Emitted:\n{loose}"
    );

    // D2 — a nullary kernel in a CONCRETE-key map slot keeps its `rt.AsMapT`
    // narrowing (the pre-existing behaviour this change preserves).
    let counts = func_body(&src, "Main_emptyCounts");
    assert!(
        counts.contains("rt.AsMapT[") && counts.contains("rt.Dict_empty()"),
        "Dict.empty in a `Dict String Int` slot must narrow via rt.AsMapT. \
         Emitted:\n{counts}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

/// Build + behavioural leg for the boundary shapes — they must keep working.
#[test]
fn guard_boundary_shapes_still_build_and_behave() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = project("boundaries-run", BOUNDARIES);
    let log = build(&dir);
    let bin = dir.join("sky-out").join("app");
    assert!(
        !log.contains("go build failed") && bin.is_file(),
        "the already-correct kernel-value shapes must keep building. Log:\n{log}"
    );

    let out = Command::new(&bin).current_dir(&dir).output().expect("run app");
    let mut combined = String::from_utf8_lossy(&out.stdout).into_owned();
    combined.push_str(&String::from_utf8_lossy(&out.stderr));
    assert_eq!(out.status.code(), Some(0), "boundary app must run. Output:\n{combined}");
    for want in ["ab", "ident", "X,Y", "1"] {
        assert!(
            combined.contains(want),
            "boundary app must still print {want:?}. Output:\n{combined}"
        );
    }
    let _ = std::fs::remove_dir_all(&dir);
}

/// Behavioural leg — the eta-expanded / coerced forms must also be CORRECT at
/// runtime, not merely accepted by `go build`. A coercion that compiles and
/// panics is the same class of defect.
#[test]
fn kernel_value_slots_behave_at_runtime() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let src = "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Core.String as String\n\
         import Sky.Core.Task as Task\n\
         import Sky.Core.Uuid as Uuid\n\
         import Std.Log exposing (println)\n\n\
         joinStr : String -> String -> String\n\
         joinStr =\n    String.append\n\n\
         tickle : String -> String\n\
         tickle =\n    String.toUpper\n\n\
         freshId : () -> Task Error String\n\
         freshId _ =\n    Uuid.v7\n\n\
         main =\n\
         \x20   let\n\
         \x20       _ =\n            println (tickle (joinStr \"hi\" \"lo\"))\n\n\
         \x20   in\n\
         \x20   case Task.perform (freshId ()) of\n\
         \x20       Ok u ->\n            println (String.fromInt (String.length u))\n\n\
         \x20       Err _ ->\n            println \"err\"\n";
    let dir = project("behave", src);
    let log = build(&dir);
    let bin = dir.join("sky-out").join("app");
    assert!(bin.is_file(), "project must build (log:\n{log})");

    let out = Command::new(&bin).current_dir(&dir).output().expect("run app");
    let mut combined = String::from_utf8_lossy(&out.stdout).into_owned();
    combined.push_str(&String::from_utf8_lossy(&out.stderr));

    assert_eq!(
        out.status.code(),
        Some(0),
        "the app must run cleanly — a coercion that compiles then panics is the \
         same defect class. Output:\n{combined}"
    );
    assert!(
        combined.contains("HILO"),
        "the eta-expanded point-free kernels must compute `String.toUpper \
         (String.append \"hi\" \"lo\")` = HILO. Output:\n{combined}"
    );
    assert!(
        combined.contains("36"),
        "the coerced nullary kernel must yield a real 36-char uuid. Output:\n{combined}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

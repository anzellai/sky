//! Kernel-qualifier SIGNATURE coverage — the class gate behind `Path.join`.
//!
//! `Path.join "a" "b"` passed `sky check` and then handed the user a raw Go
//! error (*too many arguments in call to rt.Path_join, have (any, any), want
//! (any)*). Nothing about that was specific to `Path`. The chain is structural:
//!
//!   1. `hir::KERNEL_MODULES` makes every `Qualifier.member` under a kernel
//!      pseudo-module resolve to `Res::Kernel` with no `import` — the qualifier
//!      surface. `hir::KERNEL_FUNCTIONS` is what that surface ADVERTISES.
//!   2. `ty::sig` pass 2 gives a `Res::Kernel` reference a type only when some
//!      `.sky` module mapped to that pseudo declares `name : Type`. It keys on
//!      the (pseudo, Sky-name) pair — the `Ffi.kernel "Path_join"` string in the
//!      body is invisible to the checker.
//!   3. With no declaration, `Infer::infer_res` falls back to a bare flex var.
//!      A flex var absorbs any number of arrows, so the [E2007] arity gate —
//!      which self-disables on `Ty::Var` — never fires. The program type-checks.
//!   4. Lowering emits the over-applied runtime call, and `go build` speaks.
//!
//! So an advertised member with no `.sky` signature is an unchecked hole in
//! `sky check ≡ sky build` (AGENTS.md). This gate MEASURES that hole and freezes
//! it: the allowlist below is the exact set that is untyped today. Adding a new
//! advertised member without a signature fails here; declaring a signature for
//! one on the list fails here too, demanding the row be deleted. Either way the
//! set can only shrink.
//!
//! It is not the only defence — `lower::Ctx::reject_over_application` turns an
//! over-applied kernel into a Sky `[E2007]` for the members still on this list,
//! so no member of the class can reach `go build` with a raw error. This gate
//! exists so the list itself cannot quietly grow.
//!
//! The parse goes through `syntax::parse` + `ast::Decl::TypeAnno`, the SAME AST
//! node `ty::sig` reads. A regex over the source would be a second definition of
//! "has a signature", and a second definition is how two checks come to disagree.

use std::collections::{BTreeMap, BTreeSet};
use std::fs;
use std::path::{Path, PathBuf};

use syntax::ast::{self, AstNode};

fn repo_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .ancestors()
        .nth(3)
        .expect("repo root (rust/crates/project -> repo)")
        .to_path_buf()
}

/// Advertised kernel-qualifier members with NO Sky-level signature. Each row is
/// a program the checker cannot arity-check. **Ratchets DOWNWARD only.**
///
/// 94 rows when this gate was added; 68 now. The 26 that left were declared in
/// `sky-stdlib/` against the RUNTIME's real shape (verified by
/// `kernel_signature_runtime_arity.rs`, which compares every declaration's
/// arrow count with its `runtime-go/rt` parameter count): all 13 unsigned
/// `String` members, `Time.parse`/`parseISO8601`, `JsonDec.map5`,
/// `Db.findWhere`, `Server.body`/`method`/`path`/`formValue`, and the five
/// `Task` applicatives — which additionally needed `rt.Task_map2` … `rt.Task_-
/// andMap` to exist at all (they were advertised with no runtime symbol, so
/// every call was `[E4005]` at codegen).
///
/// What is LEFT is left for a reason, and the reason is per-category. None of
/// these is "not got to yet":
///
///   * **`Basics` / `List` / `Maybe` / `Result` (47 rows)** — 29 of these are
///     ALREADY arity-checked at the TYPE layer, with a source span, through
///     `ty::sig`'s `check_kernel_sigs` seeds and its pass-3 `Result` inference.
///     A `.sky` annotation would move them out of that CHECK-ONLY channel and
///     into `kernel_sigs`, which the LOWERER reads (`use_inferred = true`) —
///     the exact channel whose concrete pinning regressed the stdlib smoke test
///     with a runtime `CoerceFailure` (see `infer_unannotated_kernel`'s
///     `pseudo != "Result"` guard). High blast radius, no diagnostic gain.
///     `Basics.abs`/`min`/`max`/`negate`/`sqrt`/`compare` additionally MUST NOT
///     be pinned: the oracle accepts `abs "x"` / `min "a" 2`, so a signature
///     would make Rust stricter than the oracle — a divergence, not a fix
///     (verified against the absolute-path differential, 2026-07-20).
///   * **`Fmt.sprint`/`sprintf`/`sprintln`/`errorf` (4)** — Go-VARIADIC
///     (`args ...any`). HM has no variadic arrow, so no signature is both
///     truthful and useful. `Fmt` also has no Layer-3 `.sky` module, and one
///     under the bare name `Fmt` would mint a new public stdlib module.
///   * **`Io.writeString` (1)** — arity-OVERLOADED at runtime with DIFFERENT
///     return shapes: one argument yields a thunk (`Task`), two yield a bare
///     `Ok` (`Result`). No single HM type covers both.
///   * **`Context.background`/`todo`/`withCancel`/`withValue` (4)** — the value
///     is a Go `context.Context`; Sky has no type that names it, and the
///     pseudo-module has no Layer-3 source.
///   * **`Ffi.*` (7)** — `Sky.Ffi` has no `.sky` module BY CONSTRUCTION: every
///     stdlib module opens with `import Sky.Ffi as Ffi`, and `Ffi.kernel` is
///     the compiler primitive that makes the alias those modules are built from.
///   * **`Io.readBytes`, `List.parallelMap` (2)** — PHANTOM: advertised, no
///     runtime symbol, no Sky body. Both already produce the identical
///     `[E4005]` an unadvertised member produces, so deleting the row changes
///     nothing a user sees while shrinking the stdlib-coverage denominator
///     (`xtask::corpus::stdlib::kernel_inventory`). The honest repair is a
///     runtime implementation whose semantics nobody has specified.
///   * **`Log.with` (1)** — `rt.logAttrsToMap` accepts a Go map OR a flat
///     alternating slice, so the runtime is WIDER than any single HM type. The
///     `List a` its `infoWith` sibling carries is already narrower than the
///     runtime and would false-reject `Log.with "msg" someDict`, which
///     `test-files/log-test.sky` does today. The `*With` family shares the
///     defect and should be re-derived from `logAttrsToMap` together.
///   * **`Server.group`, `Server.use` (2)** — `rt.Server_use` is
///     `func(_ any, routes any) any { return routes }`: it DISCARDS its
///     middleware argument. Typing a no-op would stamp "checked" on a broken
///     API. `group` mutates `SkyRoute.Path` inside a `[]any` and moves with it.
///   * **`Db.getFieldOr` (1)** — the runtime accepts only `map[string]any`
///     (`db_auth.go`), while typed codegen hands row accessors
///     `map[string]string` once `query`/`findWhere` are declared
///     `List (Dict String String)`; its five sibling accessors all handle both.
///     So it silently returns the default for every field on the typed path.
///     Its return is also the stored value verbatim, so `a -> row -> String ->
///     a` would let `Db.getFieldOr 0 row "name"` type-check over a TEXT column
///     and panic in `AsInt`. Both are runtime defects; the signature waits on
///     the fix.
const UNTYPED_KERNEL_MEMBERS: &[(&str, &[&str])] = &[
    (
        "Basics",
        &[
            "abs", "always", "clamp", "compare", "fst", "identity", "max", "min", "modBy",
            "negate", "not", "snd", "sqrt", "toString",
        ],
    ),
    ("Context", &["background", "todo", "withCancel", "withValue"]),
    ("Db", &["getFieldOr"]),
    (
        "Ffi",
        &["call", "callPure", "callTask", "has", "isPure", "kernel", "toAny"],
    ),
    ("Fmt", &["errorf", "sprint", "sprintf", "sprintln"]),
    ("Io", &["readBytes", "writeString"]),
    (
        "List",
        &[
            "all",
            "any",
            "filterMap",
            "find",
            "foldl",
            "head",
            "member",
            "parallelMap",
            "sort",
            "sortBy",
            "tail",
        ],
    ),
    ("Log", &["with"]),
    (
        "Maybe",
        &[
            "andMap",
            "andThen",
            "combine",
            "map",
            "map2",
            "map3",
            "map4",
            "map5",
            "traverse",
            "withDefault",
        ],
    ),
    (
        "Result",
        &[
            "andMap",
            "andThen",
            "andThenTask",
            "combine",
            "map",
            "map2",
            "map3",
            "map4",
            "map5",
            "mapError",
            "traverse",
            "withDefault",
        ],
    ),
    ("Server", &["group", "use"]),
];

/// Top-level `name : Type` annotations in a `.sky` source — exactly the decls
/// `ty::sig` pass 2 turns into `kernel_sigs` entries.
fn declared_signatures(src: &str) -> BTreeSet<String> {
    let parse = syntax::parse(src, base::FileId(0));
    let Some(file) = ast::SourceFile::cast(parse.syntax()) else {
        return BTreeSet::new();
    };
    file.decls()
        .filter_map(|d| match d {
            ast::Decl::TypeAnno(a) => a.name().map(|t| t.text().to_string()),
            _ => None,
        })
        .collect()
}

/// pseudo-module → every signature declared by any `.sky` module mapped to it.
/// Several import paths share a pseudo (`Sky.Core.Time` and `Std.Time` are both
/// `Time`), and `ty::sig` unions them, so this does too.
fn signatures_by_pseudo(root: &Path) -> BTreeMap<&'static str, BTreeSet<String>> {
    let mut out: BTreeMap<&'static str, BTreeSet<String>> = BTreeMap::new();
    for (path, pseudo) in hir::KERNEL_MODULES {
        let f = root
            .join("sky-stdlib")
            .join(path.replace('.', "/"))
            .with_extension("sky");
        if let Ok(src) = fs::read_to_string(&f) {
            out.entry(pseudo).or_default().extend(declared_signatures(&src));
        }
    }
    out
}

#[test]
fn advertised_kernel_members_without_a_sky_signature_match_the_allowlist() {
    let root = repo_root();
    let declared = signatures_by_pseudo(&root);

    // Every pseudo-module the qualifier surface can name, read back through
    // `hir::kernel_functions` — the same accessor the `exposing (..)` binder
    // uses, so this cannot drift from what the surface advertises.
    let pseudos: BTreeSet<&'static str> = hir::KERNEL_MODULES.iter().map(|(_, p)| *p).collect();

    let mut actual: BTreeMap<&str, BTreeSet<String>> = BTreeMap::new();
    for pseudo in pseudos {
        let Some(members) = hir::kernel_functions(pseudo) else {
            continue;
        };
        let have = declared.get(pseudo);
        let missing: BTreeSet<String> = members
            .iter()
            .filter(|m| !have.map_or(false, |s| s.contains(**m)))
            .map(|m| (*m).to_string())
            .collect();
        if !missing.is_empty() {
            actual.insert(pseudo, missing);
        }
    }

    let expected: BTreeMap<&str, BTreeSet<String>> = UNTYPED_KERNEL_MEMBERS
        .iter()
        .map(|(p, ms)| (*p, ms.iter().map(|m| (*m).to_string()).collect()))
        .collect();

    if actual == expected {
        return;
    }

    let fmt = |m: &BTreeMap<&str, BTreeSet<String>>| {
        m.iter()
            .map(|(p, s)| format!("  {p}: {}", s.iter().cloned().collect::<Vec<_>>().join(", ")))
            .collect::<Vec<_>>()
            .join("\n")
    };
    let mut newly_untyped = Vec::new();
    let mut newly_typed = Vec::new();
    for (p, ms) in &actual {
        for m in ms {
            if !expected.get(p).map_or(false, |e| e.contains(m)) {
                newly_untyped.push(format!("{p}.{m}"));
            }
        }
    }
    for (p, ms) in &expected {
        for m in ms {
            if !actual.get(p).map_or(false, |a| a.contains(m)) {
                newly_typed.push(format!("{p}.{m}"));
            }
        }
    }

    panic!(
        "kernel-qualifier signature coverage moved.\n\n\
         NEWLY UNTYPED (a member advertised by `hir::KERNEL_FUNCTIONS` with no \
         `name : Type` in its pseudo-module's `.sky` source): {}\n\
         Such a member type-checks at ANY arity — `M.f a b c` infers a flex var, \
         the [E2007] gate self-disables on `Ty::Var`, and the defect surfaces as a \
         `go build` error. Declare the signature; do NOT extend the allowlist.\n\n\
         NEWLY TYPED (on the allowlist but now declared — good): {}\n\
         Delete those rows from `UNTYPED_KERNEL_MEMBERS` in the same commit; the \
         list ratchets DOWNWARD.\n\n\
         ---- expected ----\n{}\n\n---- actual ----\n{}\n",
        if newly_untyped.is_empty() {
            "none".into()
        } else {
            newly_untyped.join(", ")
        },
        if newly_typed.is_empty() {
            "none".into()
        } else {
            newly_typed.join(", ")
        },
        fmt(&expected),
        fmt(&actual),
    );
}

/// The `Path` row is gone, and that is the point of the fix it guards.
#[test]
fn path_join_and_safe_join_are_declared() {
    let root = repo_root();
    let src = fs::read_to_string(root.join("sky-stdlib/Sky/Core/Path.sky"))
        .expect("sky-stdlib/Sky/Core/Path.sky");
    let sigs = declared_signatures(&src);
    for m in ["join", "safeJoin", "base", "dir", "ext", "isAbsolute"] {
        assert!(
            sigs.contains(m),
            "Sky.Core.Path must declare `{m} : …` — without it `Path.{m}` infers a \
             flex var and `Path.{m} a b` reaches `go build` as a raw Go arity error \
             instead of a Sky [E2007]"
        );
    }
}

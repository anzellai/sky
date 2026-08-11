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

/// Advertised kernel-qualifier members with NO Sky-level signature, as of the
/// commit that added this gate. Each row is a program the checker cannot arity-
/// check. **Ratchets DOWNWARD only.**
///
/// Four kinds of row, and they want different fixes:
///
///   * **`Basics` / `List` / `Maybe` / `Result` combinators** — typed on the
///     CHECK path only, via `ty::sig`'s hardcoded `check_kernel_sigs` seeds.
///     `sky check` DOES arity-check these; the lowerer deliberately does not see
///     them (`use_inferred`), because pinning a concrete element type there
///     regressed the stdlib smoke test. Listed for completeness — they are the
///     least urgent.
///   * **No backing `.sky` module at all** (`Context`, `Ffi`, `Fmt`) — the
///     pseudo-module has no Layer-3 source, so there is nowhere for a signature
///     to live yet.
///   * **A module that exists but omits the member** (`Db.findWhere`,
///     `Io.writeString`, `Log.with`, `String.left`, `Time.parse`, …) — the
///     `Path.join` shape exactly. These are the ones to close: add the
///     annotation, delete the row.
///   * **`Server`** — kernel-only verbs whose types involve builder shapes the
///     `.sky` surface does not yet spell.
const UNTYPED_KERNEL_MEMBERS: &[(&str, &[&str])] = &[
    (
        "Basics",
        &[
            "abs", "always", "clamp", "compare", "fst", "identity", "max", "min", "modBy",
            "negate", "not", "snd", "sqrt", "toString",
        ],
    ),
    ("Context", &["background", "todo", "withCancel", "withValue"]),
    ("Db", &["findWhere", "getFieldOr"]),
    (
        "Ffi",
        &["call", "callPure", "callTask", "has", "isPure", "kernel", "toAny"],
    ),
    ("Fmt", &["errorf", "sprint", "sprintf", "sprintln"]),
    ("Io", &["readBytes", "writeString"]),
    ("JsonDec", &["map5"]),
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
    (
        "Server",
        &["body", "formValue", "group", "method", "path", "use"],
    ),
    (
        "String",
        &[
            "ellipsize",
            "fromBytes",
            "graphemes",
            "htmlEscape",
            "isValid",
            "left",
            "normalize",
            "normalizeNFD",
            "right",
            "slugify",
            "toBytes",
            "toChar",
            "truncate",
        ],
    ),
    ("Task", &["andMap", "map2", "map3", "map4", "map5"]),
    ("Time", &["parse", "parseISO8601"]),
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

//! Kernel signatures are checked against the RUNTIME, not against their docstring.
//!
//! `kernel_signature_coverage.rs` measures how many advertised kernel members
//! HAVE a Sky signature. It says nothing about whether those signatures are
//! TRUE. That is the more dangerous half: a member with no signature is
//! unchecked, but a member with a signature that disagrees with the Go runtime
//! is worse — the checker then confidently ACCEPTS a call that misbehaves, and
//! the arity gate fires (or fails to fire) on a fiction.
//!
//! This gate is the machine check. For every advertised kernel member with a
//! `name : Type` declaration in its pseudo-module's `.sky` source, it compares
//! the declaration's ARROW COUNT against the parameter count of the Go function
//! the lowerer actually calls — `lower::kernel::kernel_go_name` resolved through
//! `abi_guard::runtime_arities`, the same scan the lowerer uses to decide
//! partial application. Both numbers come from source that is compiled: the
//! `.sky` file through `syntax::parse`, the Go through `runtime-go/rt/*.go`. No
//! hand-maintained table sits between them.
//!
//! # What is deliberately NOT compared, and why
//!
//! Three classes make arrow-count ≠ param-count *correctly*, so each is skipped
//! with a reason rather than papered over with a fudge factor:
//!
//! 1. **Variadic runtime symbols** (`func Fmt_sprintf(format any, args ...any)`).
//!    A Go variadic tail is zero-or-more, so the param scan is not the currying
//!    arity — `abi_guard::runtime_variadic_kernels` exists precisely because the
//!    naive scan mis-counts them, and `lower` overrides them with the Sky sig.
//!    Comparing here would assert a number nobody believes.
//! 2. **An ARROW-BEARING type alias in the spine.** `Handler = Request -> Task
//!    Error Response` hides an arrow: `withCors : List String -> Handler ->
//!    Handler` reads as 2 arrows unexpanded and 3 expanded, while the runtime
//!    takes 2. The arrow count is not observable without expansion, so such an
//!    alias disqualifies the row.
//!
//!    Only aliases whose expansion CONTAINS an arrow are disqualifying. A
//!    record alias (`Request = { method : String, … }`) hides nothing — it is
//!    one parameter however you spell it. Skipping on *any* alias mention is
//!    the difference between checking `Server.body`/`method`/`path`/
//!    `formValue` and silently excluding all four, which is exactly what the
//!    first draft of this gate did.
//! 3. **A symbol the runtime scan does not know.** Some kernels are lowered
//!    specially rather than to a `func` of that name (`Task.run` →
//!    `rt.AnyTaskRun`, `Task.succeed` → `rt.AnyTaskSucceed`), and some
//!    advertised members have no runtime symbol at all. `kernel_go_name`
//!    resolves the routing; if the resolved symbol is absent from
//!    `runtime_arities` there is nothing to compare against.
//!
//! One tolerance is allowed, and only one: a signature whose FIRST parameter is
//! `()` may match either `arrows` or `arrows - 1`. That is not a fudge — it is
//! the zero-arg kernel-shim class (`readLine : () -> Task Error String` →
//! `func Io_readLine() any`), which `ty::infer::relax_unit_arg_spine` models on
//! the type side: such a kernel accepts a call with or without the unit.

use std::collections::{BTreeMap, BTreeSet, HashMap};
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

/// The direct `Type` children of a syntax node — the same shape `ty::sig`'s
/// `ast_type_to_ty` walks for `Type::Fun` (`kids[0] -> kids[1]`).
fn child_types(n: &syntax::SyntaxNode) -> Vec<ast::Type> {
    n.children().filter_map(ast::Type::cast).collect()
}

/// The number of top-level arrows in an annotation, walking the RIGHT spine
/// (`a -> b -> c` is 2). A parenthesised function argument (`(a -> b) -> …`) is
/// one parameter, not two, because the recursion only follows `kids[1]`.
fn arrow_count(t: &ast::Type) -> usize {
    match t {
        ast::Type::Fun(f) => {
            let kids = child_types(f.syntax());
            1 + kids.get(1).map_or(0, arrow_count)
        }
        ast::Type::Paren(p) => child_types(p.syntax())
            .first()
            .map_or(0, |_| 0), // a parenthesised whole-type is not a spine arrow
        _ => 0,
    }
}

/// Is the FIRST parameter of this annotation the unit type `()`?
fn leading_unit_param(t: &ast::Type) -> bool {
    match t {
        ast::Type::Fun(f) => matches!(
            child_types(f.syntax()).first(),
            Some(ast::Type::Unit(_))
        ),
        _ => false,
    }
}

/// Every identifier token in an annotation — used to test the spine for alias
/// mentions without reimplementing name resolution.
fn idents(t: &ast::Type) -> BTreeSet<String> {
    t.syntax()
        .descendants_with_tokens()
        .filter_map(|e| e.into_token())
        .map(|tok| tok.text().to_string())
        .filter(|s| s.chars().next().is_some_and(|c| c.is_ascii_uppercase()))
        .collect()
}

/// Every `type alias` declared anywhere in `sky-stdlib`, as name -> the set of
/// uppercase identifiers its body mentions, plus whether the body itself
/// contains a `->`.
fn stdlib_aliases(root: &Path) -> BTreeMap<String, (bool, BTreeSet<String>)> {
    let mut out: BTreeMap<String, (bool, BTreeSet<String>)> = BTreeMap::new();
    let mut stack = vec![root.join("sky-stdlib")];
    while let Some(dir) = stack.pop() {
        let Ok(rd) = fs::read_dir(&dir) else { continue };
        for e in rd.filter_map(|e| e.ok()) {
            let p = e.path();
            if p.is_dir() {
                stack.push(p);
            } else if p.extension().and_then(|x| x.to_str()) == Some("sky") {
                let Ok(src) = fs::read_to_string(&p) else { continue };
                let parse = syntax::parse(&src, base::FileId(0));
                let Some(file) = ast::SourceFile::cast(parse.syntax()) else {
                    continue;
                };
                for d in file.decls() {
                    if let ast::Decl::Alias(a) = d {
                        let (Some(n), Some(body)) = (a.name(), a.ty()) else {
                            continue;
                        };
                        let has_arrow = matches!(body, ast::Type::Fun(_))
                            || body
                                .syntax()
                                .descendants()
                                .any(|d| ast::TypeFun::can_cast(d.kind()));
                        out.insert(n.text().to_string(), (has_arrow, idents(&body)));
                    }
                }
            }
        }
    }
    out
}

/// Does this annotation mention an alias whose expansion contains an arrow —
/// transitively? Such an alias hides arrows the unexpanded count cannot see
/// (`Handler = Request -> Task Error Response`). A record or plain alias
/// (`Request`, `Db`) hides none and is compared normally.
fn mentions_arrow_bearing_alias(
    ty: &ast::Type,
    aliases: &BTreeMap<String, (bool, BTreeSet<String>)>,
) -> bool {
    let mut seen: BTreeSet<String> = BTreeSet::new();
    let mut queue: Vec<String> = idents(ty).into_iter().collect();
    while let Some(name) = queue.pop() {
        if !seen.insert(name.clone()) {
            continue;
        }
        let Some((has_arrow, refs)) = aliases.get(&name) else {
            continue;
        };
        if *has_arrow {
            return true;
        }
        queue.extend(refs.iter().cloned());
    }
    false
}

#[test]
fn every_declared_kernel_signature_matches_its_runtime_parameter_count() {
    let root = repo_root();
    let arities = project::abi_guard::runtime_arities(&root);
    let variadic = project::abi_guard::runtime_variadic_kernels(&root);
    let aliases = stdlib_aliases(&root);

    // pseudo-module -> the members its qualifier surface advertises.
    let advertised: HashMap<&str, &[&str]> = hir::KERNEL_MODULES
        .iter()
        .filter_map(|(_, p)| hir::kernel_functions(p).map(|ms| (*p, ms)))
        .collect();

    let mut mismatches: Vec<String> = Vec::new();
    let mut compared: BTreeSet<String> = BTreeSet::new();

    for (path, pseudo) in hir::KERNEL_MODULES {
        let f = root
            .join("sky-stdlib")
            .join(path.replace('.', "/"))
            .with_extension("sky");
        let Ok(src) = fs::read_to_string(&f) else {
            continue;
        };
        let parse = syntax::parse(&src, base::FileId(0));
        let Some(file) = ast::SourceFile::cast(parse.syntax()) else {
            continue;
        };
        for decl in file.decls() {
            let ast::Decl::TypeAnno(a) = decl else {
                continue;
            };
            let (Some(name), Some(ty)) = (a.name().map(|t| t.text().to_string()), a.ty()) else {
                continue;
            };
            // Only members the qualifier surface advertises: those are the ones
            // a user reaches with no import, and the ones the coverage census
            // counts.
            if !advertised
                .get(pseudo)
                .is_some_and(|ms| ms.contains(&name.as_str()))
            {
                continue;
            }
            let sym = lower::kernel::kernel_go_name(pseudo, &name);
            let sym = sym.strip_prefix("rt.").unwrap_or(&sym).to_string();
            if variadic.contains(&sym) {
                continue; // class 1
            }
            if mentions_arrow_bearing_alias(&ty, &aliases) {
                continue; // class 2
            }
            let Some(&params) = arities.get(&sym) else {
                continue; // class 3
            };
            let arrows = arrow_count(&ty);
            compared.insert(format!("{pseudo}.{name}"));
            let ok = arrows == params || (leading_unit_param(&ty) && arrows == params + 1);
            if !ok {
                mismatches.push(format!(
                    "  {pseudo}.{name} : declares {arrows} arrow(s), but `rt.{sym}` \
                     takes {params} parameter(s)   [{path}]"
                ));
            }
        }
    }

    // A skip rule that grows too broad makes this gate pass by checking less —
    // the first draft skipped on ANY alias mention and thereby excluded every
    // `Server.*` signature it was written to justify. These anchors span each
    // skip class's boundary (record-alias params, `Result`/`Task` returns,
    // 6-arity, a 2-arity applicative), so widening a skip past them fails here
    // instead of silently.
    for anchor in [
        "Server.body",
        "Server.method",
        "Server.path",
        "Server.formValue",
        "String.left",
        "String.graphemes",
        "Time.parse",
        "Db.findWhere",
        "JsonDec.map5",
        "Task.map2",
        "Task.andMap",
        "Path.join",
    ] {
        assert!(
            compared.contains(anchor),
            "`{anchor}` is declared and advertised, but this gate did not compare \
             it — a skip rule has grown broad enough to exclude a signature it \
             exists to check. Compared {} signature(s).",
            compared.len()
        );
    }
    assert!(
        compared.len() > 200,
        "the gate compared only {compared} signature(s) — it has stopped seeing \
         the surface it is supposed to check (a routing/scan change, or an \
         over-broad skip rule), and a gate that checks nothing passes silently",
        compared = compared.len()
    );
    assert!(
        mismatches.is_empty(),
        "kernel signature(s) disagree with the Go runtime they call. A signature \
         that over- or under-counts the runtime's parameters is worse than no \
         signature: the checker accepts (or rejects) on a fiction, and `sky check \
         ≡ sky build` breaks in whichever direction the fiction leans. Fix the \
         `.sky` declaration to match `runtime-go/rt`, or — if the runtime is the \
         thing that is wrong — fix the runtime.\n\n{}\n\ncompared {} \
         signature(s)",
        mismatches.join("\n"),
        compared.len()
    );
}

//! A kernel-implicit type name (`Request`, `Response`, `Handler`, …) has at
//! most ONE record alias in the stdlib.
//!
//! A program may write such a name bare, with no import that binds it. HIR
//! keeps it unresolved (`KERNEL_IMPLICIT_TYPES`), and `ty::sig::World::expand`
//! then expands it through the BARE alias table, which keeps the FIRST alias of
//! that name in load order. The stdlib loads sorted by path, so with two
//! declarations the answer is "whichever file sorts first". Today
//! `sky-stdlib/Sky/Http/Server.sky` sorts before every other declaration of
//! `Request`, so `Std.Ai.Store`'s private `Request` (an outgoing HTTP call) never
//! won; a new module that sorted earlier would silently retype every bare
//! `Request` in every program. The table cannot know which one the author
//! meant, so the stdlib must not offer it two.
//!
//! The parse goes through `syntax::parse` + `ast::Decl::Alias`, the same AST
//! node `ty::sig` reads to build the table.

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};

use syntax::ast::{self, AstNode};

fn sky_files(dir: &Path, out: &mut Vec<PathBuf>) {
    for e in std::fs::read_dir(dir).unwrap() {
        let p = e.unwrap().path();
        if p.is_dir() {
            sky_files(&p, out);
        } else if p.extension().is_some_and(|x| x == "sky") {
            out.push(p);
        }
    }
}

#[test]
fn each_kernel_implicit_type_name_has_at_most_one_stdlib_alias() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../../sky-stdlib");
    let mut files = Vec::new();
    sky_files(&root, &mut files);
    files.sort();
    let mut seen: BTreeMap<String, Vec<String>> = BTreeMap::new();
    for f in &files {
        let src = std::fs::read_to_string(f).unwrap();
        let tree = syntax::parse(&src, base::FileId(0)).tree();
        for decl in tree.decls() {
            if let ast::Decl::Alias(a) = &decl {
                let Some(name) = a.name().map(|t| t.text().to_string()) else {
                    continue;
                };
                if hir::KERNEL_IMPLICIT_TYPES.contains(&name.as_str()) {
                    let rel = f.strip_prefix(&root).unwrap().display().to_string();
                    seen.entry(name).or_default().push(rel);
                }
            }
        }
    }
    let collisions: Vec<String> = seen
        .iter()
        .filter(|(_, v)| v.len() > 1)
        .map(|(n, v)| format!("`{n}` in {}", v.join(", ")))
        .collect();
    assert!(
        collisions.is_empty(),
        "a kernel-implicit type name is declared as a record alias in more than one stdlib \
         module, so a bare use of it expands to whichever file sorts first: {}. Rename the \
         non-canonical alias.",
        collisions.join("; ")
    );
    assert!(
        seen.get("Request")
            .is_some_and(|v| v == &["Sky/Http/Server.sky"]),
        "`Request` must stay the Sky.Http.Server alias: {seen:?}"
    );
}

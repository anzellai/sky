//! Every exported member of a bare kernel qualifier's stdlib home resolves
//! with no `import`.
//!
//! WHY. `Task.andThen` works with no `import Sky.Core.Task`: `Task` is a kernel
//! pseudo-module and `andThen` is in its kernel table. The v0.32.0 helpers
//! `Task.bestEffort`, `Task.unless` and `Task.toResult` are pure Sky in
//! `sky-stdlib/Sky/Core/Task.sky` and have no kernel entry, so the same module
//! with no import got `[E1001] Undefined name: Task.bestEffort`. The docs
//! present them as `Task.bestEffort` with no import note.
//!
//! The fix reads the qualifier's home module (`hir::implicit_qualifier_homes`)
//! for any member the kernel table does not list, so the member set is the
//! home's real exports. These tests fail when a stdlib export is not reachable
//! through its implicit qualifier, kernel-backed or pure Sky, so a future
//! helper cannot fall through again.

use base::{DefId, FileId};
use hir::{Expr, Res, SourceDb};
use std::path::{Path, PathBuf};

fn repo_root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    loop {
        if dir.join("sky-stdlib").is_dir() {
            return dir;
        }
        if !dir.pop() {
            panic!("could not locate repo root (no sky-stdlib ancestor)");
        }
    }
}

fn collect_sky(dir: &Path, out: &mut Vec<PathBuf>) {
    let Ok(rd) = std::fs::read_dir(dir) else {
        return;
    };
    let mut entries: Vec<PathBuf> = rd.filter_map(|e| e.ok().map(|e| e.path())).collect();
    entries.sort();
    for p in entries {
        if p.is_dir() {
            collect_sky(&p, out);
        } else if p.extension().and_then(|e| e.to_str()) == Some("sky") {
            out.push(p);
        }
    }
}

/// A db holding the real stdlib.
fn stdlib_db() -> (SourceDb, u32) {
    let mut files = Vec::new();
    collect_sky(&repo_root().join("sky-stdlib"), &mut files);
    assert!(!files.is_empty(), "no stdlib .sky files");
    let mut db = SourceDb::new();
    let mut next = 0u32;
    for path in &files {
        let src = std::fs::read_to_string(path).unwrap();
        let parse = syntax::parse(&src, FileId(next));
        next += 1;
        let Some(name) = parse
            .tree()
            .module_header()
            .and_then(|h| h.name())
            .map(|n| n.text())
            .filter(|s| !s.is_empty())
        else {
            continue;
        };
        db.add_module(&name, parse);
    }
    (db, next)
}

/// The home the resolver uses for `qual`: the first candidate the db carries.
fn home_of(db: &SourceDb, qual: &str) -> Option<&'static str> {
    hir::implicit_qualifier_homes(qual)
        .into_iter()
        .find(|p| db.module_by_name(p).is_some())
}

/// The `(qualifier, home)` pairs the real stdlib yields. Pinned, so a change
/// to which module a bare qualifier reads is a reviewed decision.
const EXPECTED_HOMES: &[(&str, &str)] = &[
    ("Auth", "Std.Auth"),
    ("Basics", "Sky.Core.Basics"),
    ("Char", "Sky.Core.Char"),
    ("Cli", "Std.Cli"),
    ("Cmd", "Std.Cmd"),
    ("Crypto", "Sky.Core.Crypto"),
    ("Db", "Std.Db"),
    ("Dict", "Sky.Core.Dict"),
    ("Encoding", "Sky.Core.Encoding"),
    ("File", "Sky.Core.File"),
    ("Http", "Sky.Core.Http"),
    ("Io", "Sky.Core.Io"),
    ("Jobs", "Std.Jobs"),
    ("JsonDec", "Sky.Core.Json.Decode"),
    ("JsonDecP", "Sky.Core.Json.Decode.Pipeline"),
    ("JsonEnc", "Sky.Core.Json.Encode"),
    ("List", "Sky.Core.List"),
    ("Live", "Std.Live"),
    ("Log", "Std.Log"),
    ("Math", "Sky.Core.Math"),
    ("Maybe", "Sky.Core.Maybe"),
    ("Middleware", "Sky.Http.Middleware"),
    ("Path", "Sky.Core.Path"),
    ("Process", "Sky.Core.Process"),
    ("Random", "Sky.Core.Random"),
    ("RateLimit", "Sky.Http.RateLimit"),
    ("Regex", "Sky.Core.Regex"),
    ("Result", "Sky.Core.Result"),
    ("Server", "Sky.Http.Server"),
    ("Set", "Sky.Core.Set"),
    ("String", "Sky.Core.String"),
    ("Sub", "Std.Sub"),
    ("System", "Sky.Core.System"),
    ("Task", "Sky.Core.Task"),
    ("Time", "Sky.Core.Time"),
    ("Tui", "Std.Tui"),
    ("Uuid", "Sky.Core.Uuid"),
    ("Webview", "Std.Webview"),
];

#[test]
fn every_bare_kernel_qualifier_has_the_pinned_home() {
    let (db, _) = stdlib_db();
    let mut got: Vec<(&str, &str)> = hir::implicit_qualifiers()
        .filter_map(|q| home_of(&db, q).map(|h| (q, h)))
        .collect();
    got.sort();
    got.dedup();
    let mut want = EXPECTED_HOMES.to_vec();
    want.sort();
    assert_eq!(
        got, want,
        "the import-free home of a bare qualifier changed"
    );
}

/// One module per qualifier, one top-level value per exported member:
/// `ref_N = Q.member`. Returns the members in order, with each one's kind.
fn probe_module(qual: &str, ex: &hir::ModuleExports) -> (String, Vec<(String, bool)>) {
    let mut members: Vec<(String, bool)> = ex
        .values
        .iter()
        .map(|(n, _)| (n.as_str().to_string(), false))
        .collect();
    for u in &ex.unions {
        for c in &u.ctors {
            members.push((c.name.as_str().to_string(), true));
        }
    }
    let mut src = format!("module Probe{qual} exposing (..)\n\n");
    for (i, (m, _)) in members.iter().enumerate() {
        src.push_str(&format!("ref{i} =\n    {qual}.{m}\n\n"));
    }
    (src, members)
}

/// The one `Var` of the body of each `refN` definition, by `N`.
fn probe_results(db: &SourceDb, r: &hir::ResolveResult) -> Vec<(usize, Res)> {
    let defs = db.defs().borrow();
    let mut out = Vec::new();
    for (def, body) in &r.bodies {
        let Some(loc) = defs.loc(*def) else { continue };
        let Some(n) = loc.name.as_str().strip_prefix("ref") else {
            continue;
        };
        let Ok(i) = n.parse::<usize>() else { continue };
        for (_, e) in body.exprs.iter() {
            if let Expr::Var(res) = e {
                out.push((i, res.clone()));
            }
        }
    }
    out
}

#[test]
fn every_exported_member_resolves_through_its_implicit_qualifier() {
    let (mut db, mut next) = stdlib_db();
    let mut failures: Vec<String> = Vec::new();
    for &(qual, home) in EXPECTED_HOMES {
        let home_id = db.module_by_name(home).unwrap();
        let ex = db.module_exports(home_id);
        let (src, members) = probe_module(qual, &ex);
        let name = format!("Probe{qual}");
        db.add_module(&name, syntax::parse(&src, FileId(next)));
        next += 1;
        let m = db.module_by_name(&name).unwrap();
        let r = hir::resolve(&db, m);
        for a in &r.class_a {
            failures.push(format!("{qual}.{} (home {home}): {}", a.name, a.reason));
        }
        for d in &r.diagnostics {
            if d.code.0 == "E1001" {
                failures.push(format!("{home}: {}", d.message));
            }
        }
        let pseudo = hir::KERNEL_MODULES
            .iter()
            .find(|(k, _)| *k == qual)
            .map(|(_, p)| *p)
            .unwrap();
        let kernel = hir::kernel_functions(pseudo).unwrap_or(&[]);
        let prelude = hir::PRELUDE_QUALIFIERS
            .iter()
            .find(|(q, _)| *q == qual)
            .map(|(_, f)| *f)
            .unwrap_or(&[]);
        let results = probe_results(&db, &r);
        for (i, (member, is_ctor)) in members.iter().enumerate() {
            let res: Vec<&Res> = results
                .iter()
                .filter(|(n, _)| *n == i)
                .map(|(_, r)| r)
                .collect();
            let [res] = res.as_slice() else {
                failures.push(format!("{qual}.{member}: no single reference found"));
                continue;
            };
            let kernel_backed =
                kernel.contains(&member.as_str()) || prelude.contains(&member.as_str());
            let ok = match res {
                Res::Error => false,
                // A member the kernel table lists stays a kernel reference.
                Res::Kernel { .. } => kernel_backed,
                // Every other member is the home's own definition.
                Res::Def(d) => !is_ctor && ex.value(member) == Some(*d),
                Res::Ctor(c) => *is_ctor && ex.ctor(member).map(|(_, ct)| ct.def) == Some(c.def),
                _ => false,
            };
            if !ok {
                failures.push(format!("{qual}.{member} (home {home}) resolved to {res:?}"));
            }
        }
    }
    assert!(
        failures.is_empty(),
        "members not reachable through their implicit qualifier:\n  {}",
        failures.join("\n  ")
    );
}

#[test]
fn the_v0_32_task_helpers_resolve_with_no_import() {
    let (mut db, next) = stdlib_db();
    let main = "module Main exposing (main)\n\n\
                main =\n    \
                Task.bestEffort (Task.unless False (Task.toResult (Task.succeed ()) \
                |> Task.map (\\_ -> ())))\n";
    db.add_module("Main", syntax::parse(main, FileId(next)));
    let r = hir::resolve(&db, db.module_by_name("Main").unwrap());
    assert!(r.class_a.is_empty(), "class-a: {:?}", r.class_a);
    let task = db.module_exports(db.module_by_name("Sky.Core.Task").unwrap());
    let mut defs: Vec<DefId> = Vec::new();
    let mut kernel: Vec<String> = Vec::new();
    for body in r.bodies.values() {
        for (_, e) in body.exprs.iter() {
            match e {
                Expr::Var(Res::Def(d)) => defs.push(*d),
                Expr::Var(Res::Kernel { module, func }) => {
                    kernel.push(format!("{}.{}", module.as_str(), func.as_str()))
                }
                _ => {}
            }
        }
    }
    for f in ["bestEffort", "unless", "toResult"] {
        assert!(
            defs.contains(&task.value(f).unwrap()),
            "`Task.{f}` must name Sky.Core.Task.{f}"
        );
    }
    // The kernel members are unchanged: still kernel references.
    for f in ["Task.succeed", "Task.map"] {
        assert!(
            kernel.iter().any(|k| k == f),
            "`{f}` must stay a kernel ref"
        );
    }
}

#[test]
fn an_import_binding_the_qualifier_wins_over_the_implicit_home() {
    let (mut db, next) = stdlib_db();
    let mine = "module Mine exposing (bestEffort)\n\nbestEffort t =\n    t\n";
    let main = "module Main exposing (main)\n\
                import Mine as Task\n\n\
                main =\n    Task.bestEffort 1\n";
    db.add_module("Mine", syntax::parse(mine, FileId(next)));
    db.add_module("Main", syntax::parse(main, FileId(next + 1)));
    let r = hir::resolve(&db, db.module_by_name("Main").unwrap());
    assert!(r.class_a.is_empty(), "class-a: {:?}", r.class_a);
    let mine_def = db
        .module_exports(db.module_by_name("Mine").unwrap())
        .value("bestEffort")
        .unwrap();
    let std_def = db
        .module_exports(db.module_by_name("Sky.Core.Task").unwrap())
        .value("bestEffort")
        .unwrap();
    let defs: Vec<DefId> = r
        .bodies
        .values()
        .flat_map(|b| {
            b.exprs.iter().filter_map(|(_, e)| match e {
                Expr::Var(Res::Def(d)) => Some(*d),
                _ => None,
            })
        })
        .collect();
    assert!(defs.contains(&mine_def), "the alias `Task` must win");
    assert!(
        !defs.contains(&std_def),
        "the implicit home must not shadow it"
    );
}

#[test]
fn an_unknown_member_is_still_undefined() {
    let (mut db, next) = stdlib_db();
    let main = "module Main exposing (main)\n\nmain =\n    Task.noSuchHelper ()\n";
    db.add_module("Main", syntax::parse(main, FileId(next)));
    let r = hir::resolve(&db, db.module_by_name("Main").unwrap());
    assert!(
        r.class_a.iter().any(|a| a.name == "noSuchHelper"),
        "an unknown member must stay an error: {:?}",
        r.class_a
    );
}

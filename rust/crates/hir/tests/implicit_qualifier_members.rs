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

/// The E1001 messages of `Main` (one source line, no imports).
fn e1001_messages(main: &str) -> Vec<String> {
    let (mut db, next) = stdlib_db();
    db.add_module("Main", syntax::parse(main, FileId(next)));
    let r = hir::resolve(&db, db.module_by_name("Main").unwrap());
    r.diagnostics
        .iter()
        .filter(|d| d.code.0 == "E1001")
        .map(|d| d.message.clone())
        .collect()
}

/// `Time` with no import is `Sky.Core.Time`. `Time.zones` is an export of
/// `Std.Time`, the other stdlib module written `Time`. The bare qualifier does
/// not silently pick `Std.Time`, so it stays `[E1001]`, and the message names
/// the import that binds it.
#[test]
fn a_member_of_the_other_time_module_gets_an_import_hint() {
    for member in ["zones", "toParts", "fromParts"] {
        let msgs = e1001_messages(&format!(
            "module Main exposing (main)\n\nmain =\n    Time.{member}\n"
        ));
        let want = format!("`{member}` is in `Std.Time`: add `import Std.Time as Time`");
        assert!(
            msgs.iter().any(|m| m.contains(&want)),
            "`Time.{member}` must be E1001 with the hint {want:?}: {msgs:?}"
        );
    }
}

/// A member that neither `Sky.Core.Time` nor `Std.Time` exports gets no import
/// hint.
#[test]
fn a_member_of_no_time_module_gets_no_import_hint() {
    let msgs = e1001_messages("module Main exposing (main)\n\nmain =\n    Time.noSuchMember\n");
    assert!(!msgs.is_empty(), "`Time.noSuchMember` must be E1001");
    assert!(
        msgs.iter().all(|m| !m.contains("import")),
        "no import hint expected: {msgs:?}"
    );
}

/// Every pair of stdlib modules that share a bare qualifier's name, pinned.
/// A new pair is a reviewed decision: its members get the import hint above.
#[test]
fn the_stdlib_modules_that_share_a_bare_qualifier_are_pinned() {
    let (db, _) = stdlib_db();
    let mut got: Vec<(String, String)> = Vec::new();
    for q in hir::implicit_qualifiers() {
        let Some(home) = home_of(&db, q) else {
            continue;
        };
        for mid in db.module_ids() {
            let path = db.module_name(mid).to_string();
            if path == home || !(path.starts_with("Sky.") || path.starts_with("Std.")) {
                continue;
            }
            let segs: Vec<&str> = path.split('.').collect();
            let last = segs[segs.len() - 1];
            let two = (segs.len() >= 3).then(|| format!("{}{}", segs[segs.len() - 2], last));
            if last == q || two.as_deref() == Some(q) {
                got.push((q.to_string(), path));
            }
        }
    }
    got.sort();
    got.dedup();
    assert_eq!(got, vec![("Time".to_string(), "Std.Time".to_string())]);
}

// ─────────────────────────────────────────────────────────────────────────────
// Type names. A bare kernel qualifier in TYPE position used to accept ANY
// `Q.Name` as a nominal type with no module (`resolve.rs`, `type_qual`):
// `Time.Parts` with no import named `Std.Time`'s record (the import-free
// `Time` is `Sky.Core.Time`), and `Time.NoSuchType` was accepted. A type of the
// home resolves to it, a kernel-backed type the home itself uses (`Dict.Dict`)
// stays a kernel type, and every other name is `[E1001]`.
// ─────────────────────────────────────────────────────────────────────────────

/// The db and the resolve result of `Main` (one annotation `f : <ty> -> Int`).
fn resolve_annotation_in(ty: &str) -> (SourceDb, hir::ResolveResult) {
    let (mut db, next) = stdlib_db();
    let main = format!("module Main exposing (f)\n\nf : {ty} -> Int\nf x =\n    0\n");
    db.add_module("Main", syntax::parse(&main, FileId(next)));
    let r = hir::resolve(&db, db.module_by_name("Main").unwrap());
    (db, r)
}

fn resolve_annotation(ty: &str) -> hir::ResolveResult {
    resolve_annotation_in(ty).1
}

fn e1001(r: &hir::ResolveResult) -> Vec<String> {
    r.diagnostics
        .iter()
        .filter(|d| d.code.0 == "E1001")
        .map(|d| d.message.clone())
        .collect()
}

#[test]
fn a_type_exported_by_the_home_resolves_with_no_import() {
    let (db, r) = resolve_annotation_in("Process.Command");
    let home = db.module_by_name("Sky.Core.Process").unwrap();
    let want = db.module_exports(home).type_("Command").unwrap().0;
    assert!(e1001(&r).is_empty(), "{:?}", e1001(&r));
    assert!(r.class_a.is_empty(), "class-a: {:?}", r.class_a);
    assert_eq!(
        r.type_refs.get("Process.Command").map(|t| t.con),
        Some(want),
        "`Process.Command` must name Sky.Core.Process.Command"
    );
}

#[test]
fn every_exported_type_resolves_through_its_implicit_qualifier() {
    let (mut db, mut next) = stdlib_db();
    let mut failures: Vec<String> = Vec::new();
    for &(qual, home) in EXPECTED_HOMES {
        let home_id = db.module_by_name(home).unwrap();
        let ex = db.module_exports(home_id);
        let mut types: Vec<(String, DefId, u16)> = ex
            .unions
            .iter()
            .map(|u| (u.name.as_str().to_string(), u.def, u.arity))
            .chain(
                ex.aliases
                    .iter()
                    .map(|a| (a.name.as_str().to_string(), a.def, a.arity)),
            )
            .collect();
        types.sort_by(|a, b| a.0.cmp(&b.0));
        if types.is_empty() {
            continue;
        }
        let mut src = format!("module ProbeT{qual} exposing (..)\n\n");
        for (i, (t, _, arity)) in types.iter().enumerate() {
            let args: String = (0..*arity).map(|k| format!(" a{k}")).collect();
            src.push_str(&format!(
                "ref{i} : ({qual}.{t}{args}) -> Int\nref{i} x =\n    0\n\n"
            ));
        }
        let name = format!("ProbeT{qual}");
        db.add_module(&name, syntax::parse(&src, FileId(next)));
        next += 1;
        let r = hir::resolve(&db, db.module_by_name(&name).unwrap());
        for m in e1001(&r) {
            failures.push(format!("{home}: {m}"));
        }
        for (t, def, _) in &types {
            let got = r.type_refs.get(&format!("{qual}.{t}")).map(|tr| tr.con);
            if got != Some(*def) {
                failures.push(format!("{qual}.{t} (home {home}) resolved to {got:?}"));
            }
        }
    }
    assert!(
        failures.is_empty(),
        "types not reachable through their implicit qualifier:\n  {}",
        failures.join("\n  ")
    );
}

/// `Parts` is a type of `Std.Time`, not of the import-free `Sky.Core.Time`.
#[test]
fn a_type_of_the_other_time_module_gets_an_import_hint() {
    let r = resolve_annotation("Time.Parts");
    let msgs = e1001(&r);
    let want = "`Parts` is in `Std.Time`: add `import Std.Time as Time`";
    assert!(
        msgs.iter().any(|m| m.contains(want)),
        "`Time.Parts` must be E1001 with the hint {want:?}: {msgs:?}"
    );
    assert!(!r.type_refs.contains_key("Time.Parts"));
}

#[test]
fn an_unknown_type_of_a_bare_qualifier_is_undefined() {
    for ty in ["Time.NoSuchType", "Task.Nope", "Dict.Dictionary String Int"] {
        let msgs = e1001(&resolve_annotation(ty));
        assert!(!msgs.is_empty(), "`{ty}` must be E1001");
        assert!(
            msgs.iter().all(|m| !m.contains("import")),
            "`{ty}`: no import hint expected: {msgs:?}"
        );
    }
}

#[test]
fn an_import_binding_the_qualifier_wins_for_a_type() {
    let (mut db, next) = stdlib_db();
    let mine = "module Mine exposing (Parts)\n\ntype alias Parts =\n    { n : Int }\n";
    let main = "module Main exposing (f)\n\
                import Mine as Time\n\n\
                f : Time.Parts -> Int\nf p =\n    p.n\n";
    db.add_module("Mine", syntax::parse(mine, FileId(next)));
    db.add_module("Main", syntax::parse(main, FileId(next + 1)));
    let want = db
        .module_exports(db.module_by_name("Mine").unwrap())
        .type_("Parts")
        .unwrap()
        .0;
    let r = hir::resolve(&db, db.module_by_name("Main").unwrap());
    assert!(e1001(&r).is_empty(), "{:?}", e1001(&r));
    assert_eq!(r.type_refs.get("Time.Parts").map(|t| t.con), Some(want));
}

/// Kernel-backed types have no `.sky` declaration. The home module itself
/// uses each of them as a kernel type, so the bare qualifier keeps them.
#[test]
fn kernel_backed_types_of_a_bare_qualifier_still_resolve() {
    for ty in [
        "Dict.Dict String Int",
        "Set.Set Int",
        "Cmd.Cmd Int",
        "Sub.Sub Int",
        "Task.Task Error Int",
        "Maybe.Maybe Int",
        "Result.Result Error Int",
        "List.List Int",
        "JsonEnc.Value",
        "JsonDec.Decoder Int",
    ] {
        let r = resolve_annotation(ty);
        assert!(e1001(&r).is_empty(), "`{ty}`: {:?}", e1001(&r));
        assert!(r.class_a.is_empty(), "`{ty}` class-a: {:?}", r.class_a);
    }
}

/// The resolve result of `Main` with one `import` line and one annotation.
fn resolve_with_import(import: &str, ty: &str) -> hir::ResolveResult {
    let (mut db, next) = stdlib_db();
    let main = format!("module Main exposing (f)\n{import}\n\nf : {ty} -> Int\nf x =\n    0\n");
    db.add_module("Main", syntax::parse(&main, FileId(next)));
    hir::resolve(&db, db.module_by_name("Main").unwrap())
}

#[test]
fn kernel_backed_types_of_an_imported_stdlib_module_still_resolve() {
    for (import, ty) in [
        ("import Sky.Core.Dict as Dict", "Dict.Dict String Int"),
        ("import Sky.Core.Dict as D", "D.Dict String Int"),
        ("import Std.Db as Db", "Db.Db"),
        ("import Sky.Core.Maybe as M", "M.Maybe Int"),
        ("import Sky.Core.Task as Task", "Task.Task Error Int"),
        ("import Std.Sub as Sub", "Sub.Sub Int"),
        ("import Sky.Core.Json.Encode as JsonEnc", "JsonEnc.Value"),
        ("import Std.Time as Time", "Time.Parts"),
    ] {
        let r = resolve_with_import(import, ty);
        assert!(
            e1001(&r).is_empty(),
            "`{ty}` under `{import}`: {:?}",
            e1001(&r)
        );
        assert!(
            r.class_a.is_empty(),
            "`{ty}` under `{import}`: {:?}",
            r.class_a
        );
    }
}

/// A user module imported under a kernel qualifier's name is that module: a
/// type it does not export is an error.
#[test]
fn a_user_module_under_a_kernel_qualifier_name_is_checked() {
    let (mut db, next) = stdlib_db();
    let mine = "module Lib.Db exposing (Vote)\n\ntype alias Vote =\n    { n : Int }\n";
    let main = "module Main exposing (f, g)\nimport Lib.Db as Db\n\n\
                f : Db.Vote -> Int\nf v =\n    v.n\n\n\
                g : Db.Missing -> Int\ng m =\n    0\n";
    db.add_module("Lib.Db", syntax::parse(mine, FileId(next)));
    db.add_module("Main", syntax::parse(main, FileId(next + 1)));
    let r = hir::resolve(&db, db.module_by_name("Main").unwrap());
    let names: Vec<&str> = r.class_a.iter().map(|a| a.name.as_str()).collect();
    assert_eq!(names, vec!["Missing"], "{:?}", r.class_a);
}

#[test]
fn an_unknown_home_type_is_published_as_unresolved() {
    let r = resolve_annotation("Time.Parts");
    assert!(
        r.unresolved_type_refs.contains("Time.Parts"),
        "{:?}",
        r.unresolved_type_refs
    );
}

// ─────────────────────────────────────────────────────────────────────────────
// A qualified type under an import of a STDLIB module that the module does not
// export (`Ui.Html` under `import Std.Ui as Ui`) is still accepted in v0.32.x,
// as a nominal type with no module, which the type checker unifies with ANY
// type of that name. It is a warning now and `[E1001]` in the next minor.
// ─────────────────────────────────────────────────────────────────────────────

#[test]
fn an_unexported_stdlib_type_under_an_import_warns_with_its_home() {
    let r = resolve_with_import("import Std.Ui as Ui", "Ui.Html Int");
    assert!(e1001(&r).is_empty(), "still accepted: {:?}", e1001(&r));
    let msgs: Vec<&str> = r.type_warnings.iter().map(|(m, _)| m.as_str()).collect();
    assert_eq!(msgs.len(), 1, "{msgs:?}");
    assert!(
        msgs[0].contains("`Html` is not exported by `Std.Ui`: use `Html.Html` from `Std.Html`"),
        "{msgs:?}"
    );
}

#[test]
fn an_unexported_stdlib_type_with_no_home_warns_without_a_hint() {
    let r = resolve_with_import("import Std.Ui as Ui", "Ui.NoSuchThing");
    let msgs: Vec<&str> = r.type_warnings.iter().map(|(m, _)| m.as_str()).collect();
    assert_eq!(msgs.len(), 1, "{msgs:?}");
    assert!(
        msgs[0].contains("`NoSuchThing` is not exported by `Std.Ui`."),
        "{msgs:?}"
    );
}

#[test]
fn an_exported_or_kernel_backed_stdlib_type_does_not_warn() {
    for (import, ty) in [
        ("import Std.Ui as Ui", "Ui.Element Int"),
        ("import Std.Html as Html", "Html.Html Int"),
        ("import Sky.Core.Dict as D", "D.Dict String Int"),
        ("import Std.Db as Db", "Db.Db"),
        ("import Sky.Core.Task as T", "T.Task Error Int"),
    ] {
        let r = resolve_with_import(import, ty);
        assert!(r.type_warnings.is_empty(), "`{ty}`: {:?}", r.type_warnings);
    }
}

/// No Sky source in the tree uses an unexported stdlib type under an import:
/// the stdlib, the examples, the apps, the tests, the templates and the corpus.
#[test]
fn the_tree_has_no_unexported_stdlib_type_reference() {
    let root = repo_root();
    let (mut db, mut next) = stdlib_db();
    let mut failures: Vec<String> = Vec::new();
    for dir in [
        "sky-stdlib",
        "examples",
        "apps",
        "tests",
        "templates",
        "corpus",
        "sky-bundled",
    ] {
        let mut files = Vec::new();
        collect_sky(&root.join(dir), &mut files);
        for path in files {
            let p = path.to_string_lossy();
            if p.contains("/sky-out/") || p.contains("/.") {
                continue;
            }
            let Ok(src) = std::fs::read_to_string(&path) else {
                continue;
            };
            if !src.contains("import ") {
                continue;
            }
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
            // A stdlib file is already in the db; a project module replaces
            // any same-named module of an earlier project (`Main`).
            let id = if p.contains("/sky-stdlib/") {
                db.module_by_name(&name).unwrap()
            } else {
                db.add_module(&name, parse)
            };
            let r = hir::resolve(&db, id);
            for (m, _) in &r.type_warnings {
                failures.push(format!("{p}: {m}"));
            }
        }
    }
    assert!(failures.is_empty(), "{}", failures.join("\n"));
}

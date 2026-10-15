//! Differential tests for the table read-back (`Infer::read_back_ref`).
//!
//! The owned read-back (`Infer::read_back`, one `Ty` tree per recorded
//! expression) is the oracle. For every def of a large corpus — the whole
//! stdlib, every `examples/*` project, the accept and reject corpora, and
//! generated large-record programs — inference runs ONCE, and both read-backs
//! drain the same solved union-find. Every recorded expression and local must
//! read back to the same `Ty`, in both memo modes (top-level roots only, and
//! every untainted sub-root).

use super::*;
use hir::SourceDb;
use std::path::{Path, PathBuf};

fn repo_root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    while !dir.join("sky-stdlib").is_dir() {
        assert!(dir.pop(), "no sky-stdlib ancestor");
    }
    dir
}

/// Drain `infer`'s recorded vars both ways and compare. Returns a mismatch
/// description, or `None`.
fn compare_drains(infer: &mut Infer<'_>, what: &str) -> Option<String> {
    let exprs = infer.expr_vars.clone();
    let locals = infer.local_vars.clone();
    // Oracle: one owned read-back per entry, last write wins (the
    // `infer_def_typed` loop).
    let mut want_e: HashMap<ExprId, Ty> = HashMap::new();
    for (e, tv) in &exprs {
        let t = infer.read_back(*tv);
        want_e.insert(*e, t);
    }
    let mut want_l: HashMap<LocalId, Ty> = HashMap::new();
    for (l, tv) in &locals {
        let t = infer.read_back(*tv);
        want_l.insert(*l, t);
    }
    for share in [false, true] {
        let mut sink = ReadBackSink::new().with_subroot_sharing(share);
        let mut got_e: HashMap<ExprId, TyRef> = HashMap::new();
        for (e, tv) in &exprs {
            let r = infer.read_back_ref(*tv, &mut sink);
            got_e.insert(*e, r);
        }
        let mut got_l: HashMap<LocalId, TyRef> = HashMap::new();
        for (l, tv) in &locals {
            let r = infer.read_back_ref(*tv, &mut sink);
            got_l.insert(*l, r);
        }
        if got_e.len() != want_e.len() || got_l.len() != want_l.len() {
            return Some(format!("{what}: key sets differ (share={share})"));
        }
        for (e, t) in &want_e {
            let got = sink.get(got_e[e]);
            if &got != t {
                return Some(format!(
                    "{what}: expr {e:?} (share={share})\n  owned: {}\n  table: {}",
                    t.render(),
                    got.render()
                ));
            }
        }
        for (l, t) in &want_l {
            let got = sink.get(got_l[l]);
            if &got != t {
                return Some(format!(
                    "{what}: local {l:?} (share={share})\n  owned: {}\n  table: {}",
                    t.render(),
                    got.render()
                ));
            }
        }
    }
    None
}

/// Check every def of `check` (module ids in `db`) in the lowering mode
/// (`compute_body_types`), the tooling mode (`body_types_annotated`) and the
/// checker mode (`check_modules`). Returns (defs checked, mismatches).
fn check_program(db: &SourceDb, check: &[base::ModuleId], label: &str) -> (usize, Vec<String>) {
    let world = World::build(db);
    let mut n = 0usize;
    let mut bad = Vec::new();
    for &m in check {
        let resolved = db.resolve(m);
        let mut defs: Vec<_> = resolved.bodies.iter().collect();
        defs.sort_by_key(|(d, _)| **d);
        for (def, body) in defs {
            n += 1;
            let what = |mode: &str| format!("{label} {}::{def:?} [{mode}]", db.module_name(m));
            // Lowering mode.
            let mut inf = Infer::new(&world, db)
                .with_self_def(Some(*def))
                .with_inferred(true);
            if inf.infer_def_vars(body).is_some() {
                bad.extend(compare_drains(&mut inf, &what("lower")));
            }
            // Tooling mode (annotation-seeded).
            let mut inf = Infer::new(&world, db)
                .with_self_def(Some(*def))
                .with_inferred(true)
                .with_expected(world.value_sigs.get(def).cloned());
            if inf.infer_def_vars(body).is_some() {
                bad.extend(compare_drains(&mut inf, &what("tooling")));
            }
            // Checker mode (record_exprs, annotation gate).
            let mut inf = Infer::new(&world, db)
                .with_self_def(Some(*def))
                .with_record_exprs(true);
            match world.value_sigs.get(def) {
                Some(s) => {
                    let s = s.clone();
                    inf.infer_def_against(body, &s);
                }
                None => {
                    inf.infer_def(body);
                }
            }
            bad.extend(compare_drains(&mut inf, &what("check")));
        }
    }
    (n, bad)
}

fn stdlib_db() -> (SourceDb, Vec<base::ModuleId>) {
    let mut db = SourceDb::new();
    let mut ids = Vec::new();
    for (n, parse) in crate::reject_corpus::load_stdlib(&repo_root()) {
        ids.push(db.add_module(&n, parse));
    }
    (db, ids)
}

/// A program made of the stdlib plus `modules`; returns the ids of `modules`.
fn program(modules: Vec<(String, syntax::Parse)>) -> (SourceDb, Vec<base::ModuleId>) {
    let (mut db, _) = stdlib_db();
    let ids = modules
        .into_iter()
        .map(|(n, p)| db.add_module(&n, p))
        .collect();
    (db, ids)
}

fn parse_named(src: &str) -> (String, syntax::Parse) {
    let parse = syntax::parse(src, base::FileId(0));
    let name = parse
        .tree()
        .module_header()
        .and_then(|h| h.name())
        .map(|n| n.text())
        .filter(|s| !s.is_empty())
        .unwrap_or_else(|| "Main".to_string());
    (name, parse)
}

/// The generated large-record programs (the shapes that motivated the table).
fn generated() -> Vec<(String, String)> {
    let n = 120;
    let fields: Vec<String> = (0..n).map(|i| format!("f{i} : String")).collect();
    let steps: Vec<String> = (0..n)
        .map(|i| format!("        |> Codec.field \"f{i}\" .f{i} Codec.string"))
        .collect();
    let codec = format!(
        "module Main exposing (main)\n\nimport Sky.Core.Prelude exposing (..)\n\
         import Std.Codec as Codec exposing (Codec)\nimport Std.Log exposing (println)\n\n\
         type alias Big =\n    {{ {}\n    }}\n\n\
         bigCodec : Codec Big\nbigCodec =\n    Codec.object Big\n{}\n        |> Codec.buildObject\n\n\
         main =\n    println \"x\"\n",
        fields.join("\n    , "),
        steps.join("\n")
    );
    let mut mf = Vec::new();
    let mut arms = Vec::new();
    let mut reads = Vec::new();
    for k in 0..60 {
        mf.push(format!("f{k} : String"));
        mf.push(format!("n{k} : List Int"));
        arms.push(format!(
            "        Set{k} s ->\n            ( {{ model | f{k} = s ++ model.f{k}, n{k} = List.length model.n{k} :: model.n{k} }}, model.f{k} )\n"
        ));
        reads.push(format!("model.f{k}"));
    }
    let ctors: Vec<String> = (0..60).map(|k| format!("Set{k} String")).collect();
    let model = format!(
        "module Main exposing (main)\n\nimport Sky.Core.Prelude exposing (..)\n\
         import Sky.Core.List as List\nimport Sky.Core.String as String\n\
         import Std.Log exposing (println)\n\n\
         type alias Model =\n    {{ {}\n    }}\n\n\
         type Msg\n    = {}\n\n\
         update msg model =\n    case msg of\n{}\n\n\
         view model =\n    String.concat [ {} ]\n\n\
         helper m =\n    {{ m | f0 = \"a\" }}\n\n\
         main =\n    println \"x\"\n",
        mf.join("\n    , "),
        ctors.join("\n    | "),
        arms.join("\n"),
        reads.join(", ")
    );
    vec![("codec_chain".into(), codec), ("model_app".into(), model)]
}

fn on_big_stack<F: FnOnce() + Send + 'static>(f: F) {
    std::thread::Builder::new()
        .stack_size(512 * 1024 * 1024)
        .spawn(f)
        .expect("spawn")
        .join()
        .expect("test body panicked");
}

#[test]
fn table_readback_matches_owned_readback_stdlib() {
    on_big_stack(|| {
        let (db, ids) = stdlib_db();
        let (n, bad) = check_program(&db, &ids, "stdlib");
        assert!(n > 1000, "stdlib defs checked: {n}");
        assert!(
            bad.is_empty(),
            "{} mismatches:\n{}",
            bad.len(),
            bad.join("\n")
        );
    });
}

#[test]
fn table_readback_matches_owned_readback_examples() {
    on_big_stack(|| {
        let root = repo_root();
        let mut dirs: Vec<PathBuf> = std::fs::read_dir(root.join("examples"))
            .unwrap()
            .filter_map(|e| e.ok().map(|e| e.path()))
            .filter(|p| p.join("src").is_dir())
            .collect();
        dirs.sort();
        assert!(dirs.len() >= 50, "examples found: {}", dirs.len());
        let mut total = 0;
        let mut bad = Vec::new();
        for d in &dirs {
            let mods = crate::reject_corpus::load_dir(&d.join("src"), "src");
            let (db, ids) = program(mods);
            let label = d.file_name().unwrap().to_string_lossy().to_string();
            let (n, b) = check_program(&db, &ids, &label);
            total += n;
            bad.extend(b);
        }
        assert!(total > 500, "example defs checked: {total}");
        assert!(
            bad.is_empty(),
            "{} mismatches:\n{}",
            bad.len(),
            bad.join("\n")
        );
    });
}

#[test]
fn table_readback_matches_owned_readback_corpora_and_generated() {
    on_big_stack(|| {
        let root = repo_root();
        let crate_dir = Path::new(env!("CARGO_MANIFEST_DIR"));
        let mut files = Vec::new();
        crate::reject_corpus::collect_sky(&crate_dir.join("tests/reject/corpus"), &mut files);
        crate::reject_corpus::collect_sky(&crate_dir.join("tests/accept"), &mut files);
        assert!(files.len() >= 80, "corpus files: {}", files.len());
        let _ = root;
        let mut total = 0;
        let mut bad = Vec::new();
        for f in &files {
            let src = std::fs::read_to_string(f).unwrap();
            let (db, ids) = program(vec![parse_named(&src)]);
            let label = f.file_name().unwrap().to_string_lossy().to_string();
            let (n, b) = check_program(&db, &ids, &label);
            total += n;
            bad.extend(b);
        }
        for (label, src) in generated() {
            let (db, ids) = program(vec![parse_named(&src)]);
            let (n, b) = check_program(&db, &ids, &label);
            total += n;
            bad.extend(b);
        }
        assert!(total > 100, "defs checked: {total}");
        assert!(
            bad.is_empty(),
            "{} mismatches:\n{}",
            bad.len(),
            bad.join("\n")
        );
    });
}

/// B1: a cyclic union-find graph. `N = Fun(A, P)`, `A = List N`,
/// `P = Maybe N`. Read from `N`, the cycle guard cuts `A` and `P` short
/// (`List Error`, `Maybe Error`); read from `P` alone, `P` is the outer frame
/// and reads deeper. A memo that kept `P`'s value from inside `N`'s read would
/// serve the short form for the second read. The taint rule must refuse it.
#[test]
fn cyclic_subroot_is_not_shared() {
    let mut db = SourceDb::new();
    db.add_module(
        "Main",
        syntax::parse("module Main exposing (x)\n\nx = 1\n", base::FileId(0)),
    );
    let world = World::build(&db);
    let mut inf = Infer::new(&world, &db);
    let n = inf.uf.fresh_flex();
    let a = inf
        .uf
        .fresh(Content::Structure(FlatTy::App(Name::new("List"), vec![n])));
    let p = inf
        .uf
        .fresh(Content::Structure(FlatTy::App(Name::new("Maybe"), vec![n])));
    inf.uf
        .set_content_for_test(n, Content::Structure(FlatTy::Fun(a, p)));
    for share in [false, true] {
        for order in [[n, p, a], [p, n, a], [a, p, n]] {
            let mut sink = ReadBackSink::new().with_subroot_sharing(share);
            for v in order {
                let r = inf.read_back_ref(v, &mut sink);
                let want = inf.read_back(v);
                assert_eq!(
                    sink.get(r),
                    want,
                    "share={share} order={order:?} root={v:?}: {} vs {}",
                    sink.get(r).render(),
                    want.render()
                );
            }
        }
    }
    // The two reads of `P` really differ, so the test can tell.
    let mut sink = ReadBackSink::new().with_subroot_sharing(true);
    let from_n = inf.read_back_ref(n, &mut sink);
    let Ty::Fun(_, p_inside_n) = sink.get(from_n) else {
        panic!("N reads back as a function");
    };
    assert_ne!(*p_inside_n, inf.read_back(p));
}

/// A shared sub-root is read once: reading N arrows over one big record costs
/// the record once, not N times.
#[test]
fn shared_subroot_is_read_once() {
    let mut db = SourceDb::new();
    db.add_module(
        "Main",
        syntax::parse("module Main exposing (x)\n\nx = 1\n", base::FileId(0)),
    );
    let world = World::build(&db);
    let mut inf = Infer::new(&world, &db);
    let mut fields = std::collections::BTreeMap::new();
    for i in 0..200 {
        let s = inf
            .uf
            .fresh(Content::Structure(FlatTy::App(Name::new("String"), vec![])));
        fields.insert(Name::new(&format!("f{i}")), s);
    }
    let rec = inf
        .uf
        .fresh(Content::Structure(FlatTy::Record(fields, None)));
    let mut arrows = Vec::new();
    let mut cur = rec;
    for _ in 0..200 {
        cur = inf.uf.fresh(Content::Structure(FlatTy::Fun(rec, cur)));
        arrows.push(cur);
    }
    let mut sink = ReadBackSink::new().with_subroot_sharing(true);
    for &v in &arrows {
        inf.read_back_ref(v, &mut sink);
    }
    let steps = sink.frames;
    // 200 String leaves + 1 record + 200 arrows, each read once, plus one
    // memo-hit frame per child edge (2 per arrow).
    assert!(steps <= 200 + 1 + 200 + 2 * 200, "read-back steps: {steps}");
    for &v in &arrows {
        let r = inf.read_back_ref(v, &mut sink);
        assert_eq!(sink.get(r), inf.read_back(v));
    }
}

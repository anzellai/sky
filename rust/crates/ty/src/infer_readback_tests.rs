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

/// The checker's drain (`recorded_expr_table`) against the owned one
/// (`recorded_expr_types`): the same entries in the same push order, with
/// duplicates; the same `[E2008]` key list per entry from the memoised scan;
/// the same last-wins lookup per expression.
fn compare_checker_table(infer: &mut Infer<'_>, what: &str) -> Option<String> {
    let saved = infer.expr_vars.clone();
    let owned = infer.recorded_expr_types();
    infer.expr_vars = saved.clone();
    let (refs, table) = infer.recorded_expr_table();
    infer.expr_vars = saved;
    if owned.len() != refs.len() {
        return Some(format!(
            "{what}: checker table length {} vs {}",
            refs.len(),
            owned.len()
        ));
    }
    let mut memo = HashMap::new();
    for ((e1, t), (e2, r)) in owned.iter().zip(refs.iter()) {
        if e1 != e2 {
            return Some(format!(
                "{what}: checker table order differs at {e1:?} / {e2:?}"
            ));
        }
        let got = table.get(*r);
        if &got != t {
            return Some(format!(
                "{what}: checker expr {e1:?}\n  owned: {}\n  table: {}",
                t.render(),
                got.render()
            ));
        }
        let want_keys = crate::dictkey::unsupported_keys(t);
        let got_keys = crate::dictkey::unsupported_keys_ref(&table, *r, &mut memo);
        if want_keys != *got_keys {
            return Some(format!("{what}: Dict-key list differs at {e1:?}"));
        }
    }
    let want: HashMap<ExprId, Ty> = owned.into_iter().collect();
    let view = crate::tytable::ExprTypes::new(&refs, &table);
    for (e, t) in &want {
        if view.get(e).as_ref() != Some(t) {
            return Some(format!("{what}: last-wins lookup differs at {e:?}"));
        }
    }
    None
}

/// Which inference entry a def is driven through.
#[derive(Clone, Copy)]
enum Mode {
    /// `compute_body_types` (the lowerer's table).
    Lower,
    /// `body_types_annotated` (hover / completion).
    Tooling,
    /// `check_modules` (record_exprs + the annotation gate).
    Check,
}

/// Infer `body` in `mode`, with or without the record fast paths. Returns the
/// run's `Infer` for draining and a summary of everything a caller can
/// observe: the errors (code, message, span), the variables allocated, the
/// recorded types in push order, the obligations and inferred bounds.
fn drive<'a>(
    world: &'a World,
    db: &'a SourceDb,
    def: DefId,
    body: &Body,
    mode: Mode,
    no_fast: bool,
) -> (Infer<'a>, String) {
    let mut inf = Infer::new(world, db).with_self_def(Some(def));
    inf.no_fast_paths = no_fast;
    let result = match mode {
        Mode::Lower => {
            inf = inf.with_inferred(true);
            inf.infer_def_vars(body).map(|(v, ps)| {
                let mut s = inf.read_back(v).render();
                for p in ps {
                    s.push_str(&format!(" | {}", inf.read_back(p).render()));
                }
                s
            })
        }
        Mode::Tooling => {
            inf = inf
                .with_inferred(true)
                .with_expected(world.value_sigs.get(&def).cloned());
            inf.infer_def_vars(body)
                .map(|(v, _)| inf.read_back(v).render())
        }
        Mode::Check => {
            inf = inf.with_record_exprs(true);
            match world.value_sigs.get(&def) {
                Some(s) => {
                    let s = s.clone();
                    inf.infer_def_against(body, &s);
                    None
                }
                None => inf.infer_def(body).map(|t| t.render()),
            }
        }
    };
    let mut out = format!("result={result:?} vars={}\n", inf.uf.var_count());
    for e in &inf.errors {
        out.push_str(&format!("error {} {:?} {}\n", e.code, e.span, e.message));
    }
    for (e, tv) in inf.expr_vars.clone() {
        out.push_str(&format!("expr {e:?} {}\n", inf.read_back(tv).render()));
    }
    for (l, tv) in inf.local_vars.clone() {
        out.push_str(&format!("local {l:?} {}\n", inf.read_back(tv).render()));
    }
    out.push_str(&format!(
        "obligations={} bounds={:?} vr={:?}\n",
        inf.obligations.len(),
        inf.inferred_bounds,
        inf.last_vr.as_ref().map(|(t, v, _)| (t.render(), v.len()))
    ));
    (inf, out)
}

/// Check every def of `check` (module ids in `db`) in the lowering mode
/// (`compute_body_types`), the tooling mode (`body_types_annotated`) and the
/// checker mode (`check_modules`): the record fast paths change nothing a
/// caller can observe, and the table read-back equals the owned one. Returns
/// (defs checked, mismatches).
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
            for (mode, tag) in [
                (Mode::Lower, "lower"),
                (Mode::Tooling, "tooling"),
                (Mode::Check, "check"),
            ] {
                let what = format!("{label} {}::{def:?} [{tag}]", db.module_name(m));
                let (mut fast, fast_sum) = drive(&world, db, *def, body, mode, false);
                let (_, slow_sum) = drive(&world, db, *def, body, mode, true);
                if fast_sum != slow_sum {
                    let line = fast_sum
                        .lines()
                        .zip(slow_sum.lines())
                        .find(|(a, b)| a != b)
                        .map(|(a, b)| format!("fast: {a}\n  slow: {b}"))
                        .unwrap_or_else(|| "line counts differ".to_string());
                    bad.push(format!("{what}: fast path differs\n  {line}"));
                }
                if matches!(mode, Mode::Check) {
                    bad.extend(compare_checker_table(&mut fast, &what));
                }
                bad.extend(compare_drains(&mut fast, &what));
            }
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
    // Record accesses and updates that clash, miss a field, nest, or meet a
    // non-record: the fast path's error behaviour must equal the general one.
    let errors = "module Main exposing (main)\n\nimport Sky.Core.Prelude exposing (..)\n\n\
         type alias Inner =\n    { a : Int, b : String }\n\n\
         type alias Outer =\n    { inner : Inner, name : String, tags : List String }\n\n\
         o0 : Outer\no0 =\n    { inner = { a = 1, b = \"x\" }, name = \"n\", tags = [] }\n\n\
         clash1 : Outer -> Outer\nclash1 o =\n    { o | name = 3 }\n\n\
         clash2 o =\n    { o | name = 3, tags = \"t\" }\n\n\
         missing o =\n    { o | nope = 1 }\n\n\
         nested : Outer -> Outer\nnested o =\n    { o | inner = { a = o.inner.a + 1, b = o.inner.b } }\n\n\
         nestedBad : Outer -> Int\nnestedBad o =\n    o.inner.c\n\n\
         notRecord =\n    (5).field\n\n\
         poly r =\n    { r | name = r.name ++ \"!\" }\n\n\
         usePoly =\n    ( poly o0, poly { name = \"q\" }, (poly o0).inner.a )\n\n\
         sameTwice o =\n    ( o.name, o.name, { o | name = o.name, tags = o.tags } )\n\n\
         main =\n    clash1 o0\n";
    // Offending `Dict` keys in several places of one type and in several
    // expressions, nested in records, functions and other dicts' values.
    let dict_keys = "module Main exposing (main)\n\nimport Sky.Core.Prelude exposing (..)\n\
         import Sky.Core.Dict as Dict exposing (Dict)\n\n\
         type alias M =\n    { grid : Dict ( Int, Int ) String, nest : Dict String (Dict ( Int, Int ) Int), ok : Dict String Int }\n\n\
         m0 =\n    { grid = Dict.insert ( 1, 2 ) \"a\" Dict.empty, nest = Dict.empty, ok = Dict.empty }\n\n\
         f m =\n    ( m.grid, Dict.insert ( 3, 4 ) \"b\" m.grid, \\k -> Dict.get k m.grid, m.nest )\n\n\
         g =\n    Dict.insert { a = 1 } 2 Dict.empty\n\n\
         main =\n    f m0\n";
    vec![
        ("codec_chain".into(), codec),
        ("model_app".into(), model),
        ("record_errors".into(), errors.to_string()),
        ("dict_keys".into(), dict_keys.to_string()),
    ]
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

/// The fast record paths change no world channel: the stdlib world and every
/// example's world (value_sigs, inferred_sigs, the check-only channels, the
/// record-result and call-site record channels: `World`'s `==`) are equal
/// built with the fast paths on and off.
#[test]
fn world_is_the_same_with_and_without_fast_paths() {
    on_big_stack(|| {
        let root = repo_root();
        let mut programs: Vec<(String, SourceDb)> = vec![("stdlib".into(), stdlib_db().0)];
        let mut dirs: Vec<PathBuf> = std::fs::read_dir(root.join("examples"))
            .unwrap()
            .filter_map(|e| e.ok().map(|e| e.path()))
            .filter(|p| p.join("src").is_dir())
            .collect();
        dirs.sort();
        for d in &dirs {
            let (db, _) = program(crate::reject_corpus::load_dir(&d.join("src"), "src"));
            programs.push((d.file_name().unwrap().to_string_lossy().to_string(), db));
        }
        for (label, src) in generated() {
            programs.push((label, program(vec![parse_named(&src)]).0));
        }
        let mut bad = Vec::new();
        for (label, db) in &programs {
            let fast = World::build(db);
            FORCE_GENERAL_PATHS.with(|f| f.set(true));
            let slow = World::build(db);
            FORCE_GENERAL_PATHS.with(|f| f.set(false));
            if fast != slow {
                bad.push(label.clone());
            }
            // The test is not vacuous: the world has signatures.
            assert!(!fast.value_sigs.is_empty(), "{label}: empty world");
        }
        assert!(programs.len() > 50);
        assert!(bad.is_empty(), "worlds differ: {bad:?}");
    });
}

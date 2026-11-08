//! The linear #166 call-site harvest equals the per-callee one.
//!
//! `skydb::callsite_param_records_query` folds one summary per caller
//! (`caller_record_args_query`, one inference of each caller) over the callers
//! a syntactic reverse-call index lists. `ty::callsite_param_records_for` is the
//! original per-callee harvest: it scans every body and infers each caller
//! again for each callee. For every def that wants a harvest, the two must
//! return the same value: same records, same `None` for a conflict, same
//! length.

use base::{DefId, ModuleId};
use hir::SkyDb;
use skydb::SkyDatabase;
use std::path::PathBuf;
use ty::TyDb;

fn repo_root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    while !dir.join("sky-stdlib").is_dir() {
        assert!(dir.pop(), "no sky-stdlib ancestor");
    }
    dir
}

fn db_with(stdlib: &[(String, String)], app: &[(&str, String)]) -> (SkyDatabase, Vec<ModuleId>) {
    let mut db = SkyDatabase::with_kernel();
    let mut n = 0u32;
    for (name, text) in stdlib {
        let f = db.new_source(n, text.clone());
        db.add_module(name, f);
        n += 1;
    }
    let mut ids = Vec::new();
    for (name, text) in app {
        let f = db.new_source(n, text.clone());
        ids.push(db.add_module(name, f));
        n += 1;
    }
    (db, ids)
}

/// Compare the two harvests for every def of `modules` that wants one.
/// Returns how many defs were compared and how many had a `Some` slot.
fn compare(db: &SkyDatabase, modules: &[ModuleId]) -> (usize, usize) {
    let decls = db.type_world();
    let check = db.check_world();
    let mut compared = 0;
    let mut with_record = 0;
    for &m in modules {
        let resolved = db.resolve(m);
        let defs: Vec<DefId> = resolved.bodies.keys().copied().collect();
        for def in defs {
            let body = &resolved.bodies[&def];
            if !ty::wants_callsite_harvest(&decls, def, body) {
                continue;
            }
            let linear = skydb::callsite_param_records_query(db, def, m, db.source_file(m)).clone();
            let per_callee = ty::callsite_param_records_for(db, &check, def);
            let name = db.def_loc(def).map(|l| l.name.as_str().to_string());
            assert_eq!(
                linear, per_callee,
                "linear harvest differs from the per-callee harvest for {name:?}"
            );
            compared += 1;
            if linear.iter().any(|o| o.is_some()) {
                with_record += 1;
            }
        }
    }
    (compared, with_record)
}

fn stdlib() -> Vec<(String, String)> {
    ty::reject_corpus::load_stdlib(&repo_root())
        .into_iter()
        .map(|(n, p)| (n, p.syntax().text().to_string()))
        .collect()
}

/// Every case the per-callee fold distinguishes: one caller, many callers that
/// agree, callers that disagree (conflict), a record at the second and third
/// position only, an annotated open-row param, a self-recursive helper, a
/// helper called from a lambda and a `let`, and a helper in another module.
const LIB: &str = "module Lib exposing (Rec, touch, rename, both, shared)\n\n\
type alias Rec =\n    { a : Int, b : String }\n\n\
touch r =\n    { r | a = 1 }\n\n\
rename s r =\n    { r | b = s }\n\n\
both x p q =\n    ( { p | a = x }, { q | b = \"\" } )\n\n\
shared : { r | a : Int } -> { r | a : Int }\nshared r =\n    { r | a = 2 }\n";

const MAIN: &str = "module Main exposing (main)\n\n\
import Lib exposing (Rec, touch, rename, both, shared)\nimport Sky.Core.List as List\n\n\
type alias Other =\n    { a : Int, c : Bool }\n\n\
type alias Model =\n    { a : Int, b : String, c : Bool }\n\n\
rec0 : Rec\nrec0 =\n    { a = 0, b = \"\" }\n\n\
other0 : Other\nother0 =\n    { a = 0, c = True }\n\n\
model0 : Model\nmodel0 =\n    { a = 0, b = \"\", c = False }\n\n\
countdown n m =\n    if n <= 0 then\n        m\n\n    else\n        countdown (n - 1) { m | a = n }\n\n\
useA =\n    touch rec0\n\n\
useB =\n    let\n        r =\n            touch rec0\n    in\n    rename \"x\" r\n\n\
useC =\n    ( shared other0, shared rec0 )\n\n\
useD =\n    both 1 model0 rec0\n\n\
useE =\n    List.map (\\m -> rename \"y\" m) [ model0 ]\n\n\
useF =\n    countdown 3 model0\n\n\
main =\n    ( ( useA, useB, useC ), ( useD, useE, useF ) )\n";

#[test]
fn linear_harvest_equals_per_callee_harvest_on_fixture() {
    let std = stdlib();
    let (db, ids) = db_with(
        &std,
        &[("Lib", LIB.to_string()), ("Main", MAIN.to_string())],
    );
    let (compared, with_record) = compare(&db, &ids);
    eprintln!("fixture: compared {compared}, with a record {with_record}");
    // touch, rename, both, shared, countdown.
    assert!(
        compared >= 5,
        "expected every harvested helper to be compared, got {compared}"
    );
    assert!(
        with_record >= 2,
        "expected some harvests to find a record, got {with_record}"
    );
}

/// The growth-ratchet shape: N unannotated helpers called from one `update`.
#[test]
fn linear_harvest_equals_per_callee_harvest_on_helper_shape() {
    let m = 12;
    let n = 40;
    let fields: Vec<String> = (0..m).map(|i| format!("f{i} : String")).collect();
    let init: Vec<String> = (0..m).map(|i| format!("f{i} = \"\"")).collect();
    let ctors: Vec<String> = (0..n).map(|k| format!("Set{k} String")).collect();
    let mut helpers = String::new();
    let mut arms = Vec::new();
    for k in 0..n {
        helpers.push_str(&format!(
            "set{k} s model =\n    {{ model | f{} = s }}\n\n\n",
            k % m
        ));
        // An even arm passes the closed constant `init`; an odd arm passes
        // an update of `update`'s own `model`, which the lowering inference
        // leaves an open row (it does not seed params from the signature), so
        // only the even helpers find a record.
        if k % 2 == 0 {
            arms.push(format!("        Set{k} s ->\n            set{k} s init\n"));
        } else {
            arms.push(format!(
                "        Set{k} s ->\n            set{k} s {{ model | f{} = s }}\n",
                (k + 1) % m
            ));
        }
    }
    let src = format!(
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\n\n\
         type alias Model =\n    {{ {fields}\n    }}\n\n\n\
         type Msg\n    = {ctors}\n\n\n\
         init : Model\ninit =\n    {{ {init} }}\n\n\n\
         {helpers}\
         update : Msg -> Model -> Model\nupdate msg model =\n    case msg of\n{arms}\n\n\
         main =\n    (update (Set0 \"x\") init).f0\n",
        fields = fields.join("\n    , "),
        ctors = ctors.join("\n    | "),
        init = init.join(", "),
        arms = arms.join("\n"),
    );
    let std = stdlib();
    let (db, ids) = db_with(&std, &[("Main", src)]);
    let (compared, with_record) = compare(&db, &ids);
    assert_eq!(compared, n, "every helper wants a harvest");
    assert_eq!(
        with_record,
        n / 2,
        "every even helper's caller passes the closed Model"
    );
}

// A query-world must retain D2 but not eagerly infer every caller for pass 8.
// The eager world remains the independent all-channel oracle.
#[test]
fn query_world_omits_only_aggregate_harvest_and_preserves_d2() {
    let src = "module Main exposing (main)\n\n\
        type Flag = Keep\n\n\
        base = { keep = Keep, value = 0 }\n\n\
        touch m = { m | value = 1 }\n\n\
        forward _ = touch { base | value = 2 }\n\n\
        mixed m = { m | value = 3 }\n\n\
        first = mixed { value = 0, a = True }\n\n\
        second = mixed { value = 0, b = True }\n\n\
        main = case (forward ()).keep of\n    Keep -> \"kept\"\n";
    let std = stdlib();
    let (db, ids) = db_with(
        &std,
        &[
            ("Main", src.to_string()),
            (
                "Carrier",
                "module Carrier exposing (x)\nx = 0\n".to_string(),
            ),
        ],
    );
    let m = ids[0];
    let query = db.check_world();
    let eager = ty::World::build(&db);
    let eager_checked = ty::check_modules_with_world(&db, std::sync::Arc::new(eager.clone()), &[m]);
    assert_eq!(
        (
            eager_checked.type_errors,
            eager_checked.name_errors,
            eager_checked.exhaustiveness_warnings
        ),
        (0, 0, 0),
        "fixture must be accepted before aggregate-work assertions"
    );
    // These fail on the old query implementation, which runs bulk pass 8.
    assert!(
        query.callsite_param_records.is_empty(),
        "query ran aggregate harvest"
    );
    assert!(
        query.callsite_param_conflicts.is_empty(),
        "query retained bulk conflicts"
    );
    assert!(
        !eager.callsite_param_records.is_empty(),
        "fixture did not exercise harvest"
    );
    assert!(
        !eager.callsite_param_conflicts.is_empty(),
        "fixture did not exercise sticky conflicts"
    );
    let mut oracle = eager.clone();
    oracle.callsite_param_records.clear();
    oracle.callsite_param_conflicts.clear();
    assert!(*query == oracle, "a channel other than pass 8 changed");

    let resolved = db.resolve(m);
    let def = |name: &str| {
        resolved
            .top_defs
            .iter()
            .find(|d| d.name.as_str() == name)
            .unwrap()
            .def
    };
    let base = def("base");
    let touch = def("touch");
    assert!(
        query.record_result_sigs.contains_key(&base),
        "D2 was removed with pass 8"
    );
    let harvested = skydb::callsite_param_records_query(&db, touch, m, db.source_file(m));
    assert!(
        matches!(&harvested[0], Some(ty::Ty::Record(fields, None)) if fields.iter().any(|(n, _)| n.as_str() == "keep")),
        "CAF update argument lost its untouched field"
    );
    for d in resolved.bodies.keys().copied() {
        if ty::wants_callsite_harvest(&db.type_world(), d, &resolved.bodies[&d]) {
            assert_eq!(
                skydb::callsite_param_records_query(&db, d, m, db.source_file(m)),
                &ty::callsite_param_records_for(&db, &eager, d)
            );
            assert_eq!(
                skydb::callsite_param_records_query(&db, d, ids[1], db.source_file(ids[1])),
                &ty::callsite_param_records_for(&db, &eager, d)
            );
        }
        assert_eq!(
            ty::numeric::compute_numeric_elaboration(&query, &db, m, d),
            ty::numeric::compute_numeric_elaboration(&eager, &db, m, d)
        );
        let actual = db.body_types_of(m, d);
        let expected = ty::compute_body_types(&eager, &db, m, d);
        assert_eq!(actual.result, expected.result);
        let mut ae: Vec<_> = actual.exprs().collect();
        ae.sort_by_key(|(id, _)| format!("{id:?}"));
        let mut ee: Vec<_> = expected.exprs().collect();
        ee.sort_by_key(|(id, _)| format!("{id:?}"));
        assert_eq!(ae, ee);
        let mut al: Vec<_> = actual.locals().collect();
        al.sort_by_key(|(id, _)| format!("{id:?}"));
        let mut el: Vec<_> = expected.locals().collect();
        el.sort_by_key(|(id, _)| format!("{id:?}"));
        assert_eq!(al, el);
    }
    let a = ty::check_modules_with_world(&db, query, &[m]);
    let b = eager_checked;
    assert_eq!(
        (a.type_errors, a.name_errors, a.exhaustiveness_warnings),
        (b.type_errors, b.name_errors, b.exhaustiveness_warnings)
    );
    assert_eq!(
        format!("{:?}", a.diagnostics),
        format!("{:?}", b.diagnostics)
    );
    assert_eq!(format!("{:?}", a.def_types), format!("{:?}", b.def_types));
}
/// Body-only caller/CAF edits must invalidate demanded pass-8 summaries while
/// preserving D2 and every per-body/checker/numeric result. The eager World is
/// still the independent all-pass oracle; wrong-carrier demand exercises the
/// conservative per-callee fallback, not the indexed covered path.
#[test]
fn caf_update_harvest_matches_fresh_and_eager_across_body_revisions() {
    const CARRIER: &str = "module Carrier exposing (x)\nx = 0\n";
    fn source(base: &str, argument: &str) -> String {
        format!(
            "module Main exposing (main)\n\n\
             type Flag = Keep\n\n\
             base = {base}\n\n\
             touch m = {{ m | value = 1 }}\n\n\
             forward _ = touch {argument}\n\n\
             main = case (forward ()).keep of\n    Keep -> \"kept\"\n"
        )
    }
    fn body_table(types: &ty::BodyTypes) -> String {
        let mut expressions: Vec<_> = types
            .exprs()
            .map(|(id, ty)| (format!("{id:?}"), format!("{ty:?}")))
            .collect();
        expressions.sort();
        let mut locals: Vec<_> = types
            .locals()
            .map(|(id, ty)| (format!("{id:?}"), format!("{ty:?}")))
            .collect();
        locals.sort();
        format!(
            "{:?}\n{:?}\n{expressions:?}\n{locals:?}",
            types.result, types.signature
        )
    }
    type CheckObservation = (usize, usize, usize, String, Vec<String>);
    type BodyObservation = (
        String,
        Vec<Option<ty::Ty>>,
        String,
        ty::numeric::NumericElaboration,
    );
    fn checked(output: &ty::CheckOutput) -> CheckObservation {
        let mut defs: Vec<_> = output
            .def_types
            .iter()
            .map(|d| format!("{}::{} {:?} {}", d.module, d.name, d.ty, d.declared))
            .collect();
        defs.sort();
        (
            output.type_errors,
            output.name_errors,
            output.exhaustiveness_warnings,
            format!("{:?}", output.diagnostics),
            defs,
        )
    }
    // All projected body/numeric keys are local arena IDs; no cross-database
    // DefId/interner ordering is used in this fresh-database comparison.
    fn observe(
        db: &SkyDatabase,
        modules: &[ModuleId],
        expected_fields: &[&str],
    ) -> (Vec<BodyObservation>, CheckObservation) {
        let m = modules[0];
        let eager = ty::World::build(db);
        let eager_checked =
            ty::check_modules_with_world(db, std::sync::Arc::new(eager.clone()), &[m]);
        assert_eq!(
            (
                eager_checked.type_errors,
                eager_checked.name_errors,
                eager_checked.exhaustiveness_warnings
            ),
            (0, 0, 0),
            "every revision must be accepted by the unchanged eager checker"
        );
        let query = db.check_world();
        let mut without_pass8 = eager.clone();
        without_pass8.callsite_param_records.clear();
        without_pass8.callsite_param_conflicts.clear();
        assert!(
            *query == without_pass8,
            "query changed a channel other than pass 8"
        );
        let resolved = db.resolve(m);
        let def = |name: &str| {
            resolved
                .top_defs
                .iter()
                .find(|d| d.name.as_str() == name)
                .unwrap()
                .def
        };
        assert!(
            query.record_result_sigs.contains_key(&def("base")),
            "D2 CAF result missing"
        );
        let touch = def("touch");
        assert!(ty::wants_callsite_harvest(
            &db.type_world(),
            touch,
            &resolved.bodies[&touch]
        ));
        let harvest = skydb::callsite_param_records_query(db, touch, m, db.source_file(m));
        let Some(Some(ty::Ty::Record(fields, None))) = harvest.first() else {
            panic!("CAF caller must produce a closed record harvest")
        };
        let mut actual_fields: Vec<_> = fields.iter().map(|(n, _)| n.as_str()).collect();
        actual_fields.sort();
        let mut wanted_fields = expected_fields.to_vec();
        wanted_fields.sort();
        assert_eq!(
            actual_fields, wanted_fields,
            "stale caller or CAF field shape"
        );
        let mut observations = Vec::new();
        for (&d, body) in &resolved.bodies {
            let name = db.def_loc(d).unwrap().name.as_str().to_string();
            let wanted = ty::wants_callsite_harvest(&db.type_world(), d, body);
            let harvested = if wanted {
                let expected = ty::callsite_param_records_for(db, &eager, d);
                let covered = skydb::callsite_param_records_query(db, d, m, db.source_file(m));
                let fallback = skydb::callsite_param_records_query(
                    db,
                    d,
                    modules[1],
                    db.source_file(modules[1]),
                );
                assert_eq!(covered, &expected, "covered harvest differs for {name}");
                assert_eq!(
                    fallback, &expected,
                    "wrong-carrier fallback differs for {name}"
                );
                covered.clone()
            } else {
                Vec::new()
            };
            let actual = db.body_types_of(m, d);
            let expected = ty::compute_body_types(&eager, db, m, d);
            assert_eq!(
                body_table(&actual),
                body_table(&expected),
                "full body table differs for {name}"
            );
            let numeric = ty::numeric::compute_numeric_elaboration(&query, db, m, d);
            assert_eq!(
                numeric,
                ty::numeric::compute_numeric_elaboration(&eager, db, m, d),
                "numeric result differs for {name}"
            );
            observations.push((name, harvested, body_table(&actual), numeric));
        }
        observations.sort_by(|a, b| a.0.cmp(&b.0));
        let checker = checked(&ty::check_modules_with_world(db, query, &[m]));
        assert_eq!(checker, checked(&eager_checked));
        (observations, checker)
    }

    let initial = source("{ keep = Keep, value = 0 }", "{ base | value = 2 }");
    let caller_edit = source(
        "{ keep = Keep, value = 0 }",
        "{ keep = Keep, value = 2, extra = True }",
    );
    let caf_edit = source(
        "{ keep = Keep, value = 0, caption = \"caf\" }",
        "{ base | value = 2 }",
    );
    let std = stdlib();
    let (mut db, ids) = db_with(
        &std,
        &[("Main", initial.clone()), ("Carrier", CARRIER.to_string())],
    );
    let file = db.source_file(ids[0]);
    let mut first = None;
    for (revision, (text, fields)) in [
        (&initial, vec!["keep", "value"]),
        (&caller_edit, vec!["keep", "value", "extra"]),
        (&caf_edit, vec!["keep", "value", "caption"]),
        (&initial, vec!["keep", "value"]),
    ]
    .into_iter()
    .enumerate()
    {
        if revision != 0 {
            db.set_source_text(file, text.clone());
        }
        let actual = observe(&db, &ids, &fields);
        let (fresh, fresh_ids) = db_with(
            &std,
            &[("Main", text.clone()), ("Carrier", CARRIER.to_string())],
        );
        let expected = observe(&fresh, &fresh_ids, &fields);
        assert_eq!(actual, expected, "incremental vs fresh revision {revision}");
        // Checker acceptance alone cannot hide a stale table that happens to
        // remain accepted after changing the caller's untouched field set.
        if revision == 0 {
            first = Some(actual);
        } else if revision == 3 {
            assert_eq!(
                Some(actual),
                first,
                "revert did not restore initial results"
            );
        }
    }
}

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

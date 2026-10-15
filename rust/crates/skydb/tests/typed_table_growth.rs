//! Growth ratchet for type checking and lowering on large records.
//!
//! A record of N fields used by N expressions must cost O(N) work to type and
//! lower, not O(N²). Two shapes once grew quadratically:
//!
//! * a `Codec.object Ctor |> Codec.field … |> Codec.buildObject` chain over an
//!   N-field record — every step's type carries the rest of the constructor,
//!   and each step's type was read back, rebuilt and walked as its own tree;
//! * a TEA `Model` of N fields read (`model.f`) and updated (`{ model | f = x }`)
//!   by N handlers — every access unified the whole closed `Model` and every
//!   expression's type was read back as an owned copy of it.
//!
//! The test drives the real build path (`ty::check_modules`, then the
//! `go_program` query that runs inference for lowering and the lowerer) on each
//! shape at three sizes, and reads the work counters in `ty::counters`. Wall
//! time is noisy; the counters are exact. For each counter it compares the work
//! added by the second doubling with the work added by the first: linear work
//! gives a ratio near 2, quadratic work near 4. The bound is 2.3.
//!
//! The counters are process-wide, so the tests in this file run one at a
//! time (`SERIAL`).

use lower::LowerConfig;
use skydb::{go_program, BuildConfig, SkyDatabase};
use std::path::PathBuf;
use ty::counters::{snapshot, Snapshot};

fn repo_root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    while !dir.join("sky-stdlib").is_dir() {
        assert!(dir.pop(), "no sky-stdlib ancestor");
    }
    dir
}

/// Typecheck and lower `main_src` with the stdlib; return the counter deltas
/// of the two phases.
fn run(stdlib: &[(String, String)], main_src: &str) -> (Snapshot, Snapshot) {
    let mut db = SkyDatabase::with_kernel();
    let mut n = 0u32;
    for (name, text) in stdlib {
        let f = db.new_source(n, text.clone());
        db.add_module(name, f);
        n += 1;
    }
    let f = db.new_source(n, main_src.to_string());
    let main = db.add_module("Main", f);
    let s0 = snapshot();
    let out = ty::check_modules(&db, &[main]);
    let errors: Vec<String> = out
        .diagnostics
        .iter()
        .filter(|d| d.severity == diagnostics::Severity::Error)
        .map(|d| format!("[{}] {}", d.code.0, d.message))
        .collect();
    assert!(errors.is_empty(), "fixture must type-check: {errors:?}");
    let s1 = snapshot();
    let cfg = BuildConfig::new(&db, LowerConfig::default());
    let prog = go_program(&db, main, cfg);
    assert!(prog.entry_ok, "fixture must lower");
    let s2 = snapshot();
    (s1.since(&s0), s2.since(&s1))
}

/// An N-field record and its `Codec` built by one N-step pipeline.
fn codec_chain(n: usize) -> String {
    let fields: Vec<String> = (0..n).map(|i| format!("f{i} : String")).collect();
    let steps: Vec<String> = (0..n)
        .map(|i| format!("        |> Codec.field \"f{i}\" .f{i} Codec.string"))
        .collect();
    let blank: Vec<String> = (0..n).map(|i| format!("f{i} = \"\"")).collect();
    format!(
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Codec as Codec exposing (Codec)\n\
         import Std.Log exposing (println)\n\n\n\
         type alias Big =\n    {{ {fields}\n    }}\n\n\n\
         bigCodec : Codec Big\nbigCodec =\n    Codec.object Big\n{steps}\n        |> Codec.buildObject\n\n\n\
         blank : Big\nblank =\n    {{ {blank} }}\n\n\n\
         main =\n    println (Codec.toJson bigCodec blank)\n",
        fields = fields.join("\n    , "),
        steps = steps.join("\n"),
        blank = blank.join(", "),
    )
}

/// A TEA-shaped `Model` of 2N fields, N `update` arms that read and update it,
/// and a `view` that reads every field.
fn model_app(n: usize) -> String {
    let mut fields = Vec::new();
    let mut init = Vec::new();
    let mut ctors = Vec::new();
    let mut arms = Vec::new();
    let mut reads = Vec::new();
    for k in 0..n {
        fields.push(format!("f{k} : String"));
        fields.push(format!("n{k} : Int"));
        init.push(format!("f{k} = \"\""));
        init.push(format!("n{k} = 0"));
        ctors.push(format!("Set{k} String"));
        arms.push(format!(
            "        Set{k} s ->\n            {{ model | f{k} = s ++ model.f{k}, n{k} = model.n{k} + 1 }}\n"
        ));
        reads.push(format!("model.f{k}"));
    }
    format!(
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Core.String as String\n\
         import Std.Log exposing (println)\n\n\n\
         type alias Model =\n    {{ {fields}\n    }}\n\n\n\
         type Msg\n    = {ctors}\n\n\n\
         init : Model\ninit =\n    {{ {init} }}\n\n\n\
         update : Msg -> Model -> Model\nupdate msg model =\n    case msg of\n{arms}\n\n\
         view : Model -> String\nview model =\n    String.concat\n        [ {reads}\n        ]\n\n\n\
         main =\n    println (view (update (Set0 \"x\") init))\n",
        fields = fields.join("\n    , "),
        ctors = ctors.join("\n    | "),
        init = init.join(", "),
        arms = arms.join("\n"),
        reads = reads.join("\n        , "),
    )
}

/// Check that every counter of `phase` grows at most linearly over the three
/// sizes. Returns the failures as text.
fn growth_failures(
    shape: &str,
    phase: &str,
    points: &[(usize, Snapshot)],
    counters: &[&str],
) -> Vec<String> {
    let mut bad = Vec::new();
    for idx in 0..points[0].1.fields().len() {
        let name = points[0].1.fields()[idx].0;
        if !counters.contains(&name) {
            continue;
        }
        let v: Vec<u64> = points.iter().map(|(_, s)| s.fields()[idx].1).collect();
        let d1 = v[1].saturating_sub(v[0]);
        let d2 = v[2].saturating_sub(v[1]);
        // A counter that barely moves is constant work, not growth.
        let ok = d2 <= 1000 || (d2 as f64) <= 2.3 * (d1 as f64);
        let line = format!(
            "{shape}/{phase}/{name}: {} -> {} -> {} (increments {d1}, {d2}, ratio {:.2})",
            v[0],
            v[1],
            v[2],
            if d1 == 0 {
                f64::INFINITY
            } else {
                d2 as f64 / d1 as f64
            }
        );
        eprintln!("{line}");
        if !ok {
            bad.push(line);
        }
    }
    bad
}

/// The counters of the shared type table: read-back, owned rebuilds, the
/// `[E2008]` key scan, interning and the lowerer's Go-type mapping.
const TABLE_COUNTERS: &[&str] = &["readback", "owned_rebuild", "dictkey", "intern", "goty"];

/// The unifier's counters. Still quadratic on both shapes: binding an
/// instantiated variable to a large structure runs the occurs check over the
/// whole structure (`Codec.field`'s `r` and `b` against the record and the
/// rest of the constructor arrow), and the lowering path's update of an
/// annotated record param unifies a fresh instance of the declared record per
/// update (#166 / doc 13 D2). Kept as an ignored test so the gap stays visible.
const UNIFIER_COUNTERS: &[&str] = &["occurs", "record_fields"];

/// The counters are process-wide: the two tests below must not overlap.
static SERIAL: std::sync::Mutex<()> = std::sync::Mutex::new(());

#[test]
fn type_table_work_is_linear_in_record_size() {
    growth_test(TABLE_COUNTERS);
}

#[test]
#[ignore = "known quadratic: occurs check and D2 record instance per update (see UNIFIER_COUNTERS)"]
fn unifier_work_is_linear_in_record_size() {
    growth_test(UNIFIER_COUNTERS);
}

fn growth_test(counters: &'static [&'static str]) {
    let _serial = SERIAL.lock().unwrap_or_else(|p| p.into_inner());
    // A test thread's default stack is 2 MiB; give the fixtures room without
    // depending on every walker's on-demand stack growth.
    std::thread::Builder::new()
        .stack_size(512 * 1024 * 1024)
        .spawn(move || growth_body(counters))
        .expect("spawn")
        .join()
        .expect("growth check panicked");
}

fn growth_body(counters: &[&str]) {
    let root = repo_root();
    let stdlib: Vec<(String, String)> = ty::reject_corpus::load_stdlib(&root)
        .into_iter()
        .map(|(n, p)| (n, p.syntax().text().to_string()))
        .collect();
    let sizes = [100usize, 200, 400];
    let mut failures = Vec::new();
    for (shape, gen) in [
        ("codec_chain", codec_chain as fn(usize) -> String),
        ("model_app", model_app as fn(usize) -> String),
    ] {
        let runs: Vec<(usize, (Snapshot, Snapshot))> =
            sizes.iter().map(|&n| (n, run(&stdlib, &gen(n)))).collect();
        let check: Vec<(usize, Snapshot)> = runs.iter().map(|(n, (c, _))| (*n, *c)).collect();
        let lower: Vec<(usize, Snapshot)> = runs.iter().map(|(n, (_, l))| (*n, *l)).collect();
        failures.extend(growth_failures(shape, "typecheck", &check, counters));
        failures.extend(growth_failures(shape, "lower", &lower, counters));
    }
    assert!(
        failures.is_empty(),
        "work grows faster than linear:\n{}",
        failures.join("\n")
    );
}

// ---- #166 call-site harvest growth -------------------------------------------
//
// N helpers each update one field of a fixed 30-field `Model` param, and one
// `update` of N arms calls every helper on an updated `model` (so each arm
// does a fixed amount of record work). The lowering path harvests, for a
// helper that updates its param, the record each caller passes, by inferring
// the caller. Done per helper, that re-infers the N-arm `update` N times: work
// grows as N². The two tests below build the shape at N and 2N and require the
// lowering phase's `record_fields` work to grow at most 2.3 times.

const HELPER_MODEL_FIELDS: usize = 30;

/// `annotated`: each helper carries `setK : String -> Model -> Model`.
fn param_update_helpers(n: usize, annotated: bool) -> String {
    let m = HELPER_MODEL_FIELDS;
    let fields: Vec<String> = (0..m).map(|i| format!("f{i} : String")).collect();
    let init: Vec<String> = (0..m).map(|i| format!("f{i} = \"\"")).collect();
    let ctors: Vec<String> = (0..n).map(|k| format!("Set{k} String")).collect();
    let mut helpers = String::new();
    let mut arms = Vec::new();
    for k in 0..n {
        if annotated {
            helpers.push_str(&format!("set{k} : String -> Model -> Model\n"));
        }
        helpers.push_str(&format!(
            "set{k} s model =\n    {{ model | f{} = s }}\n\n\n",
            k % m
        ));
        arms.push(format!(
            "        Set{k} s ->\n            set{k} s {{ model | f{} = s }}\n",
            (k + 1) % m
        ));
    }
    format!(
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\n\n\
         type alias Model =\n    {{ {fields}\n    }}\n\n\n\
         type Msg\n    = {ctors}\n\n\n\
         init : Model\ninit =\n    {{ {init} }}\n\n\n\
         {helpers}\
         update : Msg -> Model -> Model\nupdate msg model =\n    case msg of\n{arms}\n\n\
         main =\n    println (update (Set0 \"x\") init).f0\n",
        fields = fields.join("\n    , "),
        ctors = ctors.join("\n    | "),
        init = init.join(", "),
        arms = arms.join("\n"),
    )
}

fn harvest_growth(annotated: bool) {
    let _serial = SERIAL.lock().unwrap_or_else(|p| p.into_inner());
    std::thread::Builder::new()
        .stack_size(512 * 1024 * 1024)
        .spawn(move || {
            let root = repo_root();
            let stdlib: Vec<(String, String)> = ty::reject_corpus::load_stdlib(&root)
                .into_iter()
                .map(|(n, p)| (n, p.syntax().text().to_string()))
                .collect();
            let fields = |n: usize| {
                let (_, lower) = run(&stdlib, &param_update_helpers(n, annotated));
                lower
                    .fields()
                    .iter()
                    .find(|(name, _)| *name == "record_fields")
                    .map(|(_, v)| *v)
                    .expect("record_fields counter")
            };
            let (a, b) = (fields(60), fields(120));
            let ratio = b as f64 / a as f64;
            eprintln!(
                "param_update_helpers(annotated={annotated})/lower/record_fields: N=60 {a}, N=120 {b}, ratio {ratio:.2}"
            );
            assert!(
                ratio <= 2.3,
                "lowering record_fields grows {ratio:.2}x from N=60 to N=120 \
                 (annotated={annotated}); the call-site harvest is re-inferring callers"
            );
        })
        .expect("spawn")
        .join()
        .expect("harvest growth check panicked");
}

#[test]
fn annotated_param_update_helpers_lower_linearly() {
    harvest_growth(true);
}

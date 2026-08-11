//! **Family R** — the reject matrix (v2 §3.1): rejection **by diagnostic code**,
//! each case carrying a paired **accepted twin**.
//!
//! # Why "it failed" is not a test
//!
//! A rejection assertion that only checks a non-zero exit passes in two states
//! that have nothing to do with correctness:
//!
//! * the compiler rejected the program for an entirely **unrelated** reason (a
//!   typo the generator introduced by accident, a stale stdlib, a parse error in
//!   the harness's own preamble), and
//! * the compiler is broken badly enough to reject **everything** — a checker
//!   that returned `Err` unconditionally would score 100 % on such a corpus.
//!
//! So every case here asserts two things that together exclude both states:
//!
//! 1. **The diagnostic CODE.** The rejection must carry the code the generator
//!    declared, under the same AT-LEAST rule the checked-in reject corpus uses
//!    (`ty::reject_corpus`): every declared code must appear among the
//!    verdict-contributing diagnostics; extra codes are permitted, because one
//!    defect legitimately cascades and punishing a diagnostic improvement is how
//!    a gate teaches authors to weaken their headers.
//! 2. **The twin is ACCEPTED.** The same program with the defect — and only the
//!    defect — repaired must type-check with no rejection-contributing
//!    diagnostic at all. That is the §4.4 reject-case witness, and it is what
//!    makes the pair falsifiable.
//!
//! # This is not a second reject gate
//!
//! `xtask reject` runs a **checked-in corpus of hand-written defects**
//! (`rust/crates/ty/tests/reject/corpus/*.sky`; the exact count is the ratchet
//! `ty::reject_corpus::EXPECTED_CORPUS_FILES`), each pinned to the Haskell
//! oracle's verdict. It is the *provenance* face: every file is a real defect
//! somebody hit, with a header recording what the oracle does.
//!
//! Family R is the **combinatorial** face of the same question: it takes a small
//! set of defect CLASSES and crosses each one against the axes this repository's
//! bugs actually moved along — how the expression is positioned, and how the
//! names around it entered scope. #164 (import-alias resolution) and the
//! stdlib-name collision class both came from that second dimension, and a
//! corpus of one-file-per-defect cannot reach it: 15 defects × 3 positions × 3
//! import shapes is 135 programs nobody is going to hand-write.
//!
//! Neither replaces the other, and neither is allowed a private copy of "what
//! counts as rejected": both call [`ty::reject_corpus`]'s single declaration
//! (v2 §1.5 — the two reject faces once drifted on exactly that and neither
//! knew).
//!
//! # The import axis is load-bearing, not decorative
//!
//! `witness.rs` records a real weakness in the `import_shape` stratum: its
//! `collision` axis is **inert**, because its non-`none` values add a local
//! binding that collides with nothing. A varied axis that cannot change the
//! answer spends budget and covers nothing.
//!
//! So here every program routes an integer it actually needs — `knownName` —
//! through the imported helper module, under whichever import shape the axis
//! selected. If import resolution breaks for a shape, the TWIN stops compiling
//! and the pair goes red. The axis cannot be inert, because the twin's
//! acceptance depends on it.

use super::axes::{Assignment, Axis, Stratum};
use super::gen::{Class, Expect, Family, GenCase, Isolation, Mode, Witness};
use std::path::Path;

// ---------------------------------------------------------------------------
// Axes — declared HERE, not in `axes.rs`
//
// `axes.rs::STRATA` is the full-cross list the value-asserting Layer-1 families
// share, and `witness.rs` panics for any stratum missing from its own
// `axis_under_test` table. Family R's witness is the twin, not an emit-shape
// fingerprint, so it deliberately does not join that list.
// ---------------------------------------------------------------------------

/// The ill-typed construct. Each value names ONE defect class and nothing else;
/// the twin repairs exactly it.
pub const DEFECT: Axis = Axis::new(
    "defect",
    &[
        "arity_over",
        "arity_under_as_value",
        "unknown_field",
        "wrong_field_type",
        "missing_field",
        "record_update_unknown_field",
        "record_update_wrong_type",
        "row_subset_missing",
        "nonexhaustive_case",
        "unknown_name",
        "unknown_module",
        "unexposed_name",
        "result_vs_maybe",
        "task_vs_pure",
        "dict_composite_key",
    ],
);

/// Where the ill-typed expression sits. The same three positions the value
/// families use, minimised to the ones that cannot themselves change the
/// verdict.
pub const RPOSITION: Axis = Axis::new("rposition", &["bare", "in_let", "in_lambda"]);

/// How the helper module's `answer` enters scope. `alias_not_last_segment` is
/// the #164 shape: the alias is NOT the module path's final segment, which is
/// what the qualifier heuristic that "fixed" #164 got wrong and regressed a real
/// app with.
pub const RIMPORT: Axis = Axis::new(
    "rimport",
    &["plain", "alias_not_last_segment", "exposing_list"],
);

pub const STRATUM: Stratum = Stratum {
    name: "reject_matrix",
    axes: &[DEFECT, RPOSITION, RIMPORT],
    coordinate: Some("anzellai/sky#164 + the reject corpus' unpinned classes"),
    // Whole-program name resolution IS the subject under test for the import
    // axis (v2 §3.2 family 3), and an ill-typed program must never be batched
    // with a neighbour whose verdict it could contaminate.
    isolated: true,
};

/// The helper module every case imports. It EXPOSES `answer` and deliberately
/// does not expose `label` — the `unexposed_name` defect needs a name that
/// exists but is not visible, which is a different rejection from a name that
/// does not exist at all.
const HELPER_NAME: &str = "Helper.Inner.Values";

fn helper_src() -> String {
    "module Helper.Inner.Values exposing (answer)\n\n\
     import Sky.Core.Prelude exposing (..)\n\n\n\
     answer : Int\nanswer =\n    42\n\n\n\
     label : String\nlabel =\n    \"v\"\n"
        .to_string()
}

/// `(import line, how `answer` is referenced)` for an import shape.
fn helper_import(shape: &str) -> (String, String) {
    match shape {
        "plain" => (format!("import {HELPER_NAME}"), "Values.answer".to_string()),
        // The alias is NOT the last path segment — the #164 regression shape.
        "alias_not_last_segment" => (
            format!("import {HELPER_NAME} as Inner"),
            "Inner.answer".to_string(),
        ),
        "exposing_list" => (
            format!("import {HELPER_NAME} exposing (answer)"),
            "answer".to_string(),
        ),
        other => panic!("reject_matrix: unknown rimport {other:?}"),
    }
}

/// How the *unexposed* name would be referenced under each import shape — and
/// the code that reference is rejected with.
///
/// The two are genuinely different rejections and the matrix says so rather than
/// flattening them: a QUALIFIED reference to an unexposed name is "not exported
/// by module" (`[E1001]`, `resolve_qual_var`'s `Dep` arm), while naming it in an
/// `exposing (…)` list is "not exposed" (`[E1011]`, `bind_exposing_dep`). A
/// single declared code for both would have been satisfied by whichever one the
/// compiler happened to emit.
fn unexposed(shape: &str) -> (String, String, &'static str) {
    match shape {
        "plain" => (
            format!("import {HELPER_NAME}"),
            "Values.label".to_string(),
            "E1001",
        ),
        "alias_not_last_segment" => (
            format!("import {HELPER_NAME} as Inner"),
            "Inner.label".to_string(),
            "E1001",
        ),
        "exposing_list" => (
            format!("import {HELPER_NAME} exposing (answer, label)"),
            "label".to_string(),
            "E1011",
        ),
        other => panic!("reject_matrix: unknown rimport {other:?}"),
    }
}

/// One side of a case: the program that is either rejected or accepted.
struct Side {
    /// Import lines beyond the fixed preamble and the helper import.
    extra_imports: Vec<String>,
    /// Overrides the helper import line the `rimport` axis chose (only
    /// `unexposed_name` needs this).
    helper_import_override: Option<String>,
    /// Declarations between `knownName` and `probe`.
    decls: String,
    /// The `String`-valued expression `probe` is defined as.
    expr: String,
}

impl Side {
    fn new(decls: impl Into<String>, expr: impl Into<String>) -> Side {
        Side {
            extra_imports: Vec::new(),
            helper_import_override: None,
            decls: decls.into(),
            expr: expr.into(),
        }
    }
}

const REC: &str = "type alias Rec =\n    { a : Int, b : String }\n\n\n";
const ADD: &str = "add : Int -> Int -> Int\nadd x y =\n    x + y\n\n\n";
const COLOR: &str = "type Color\n    = Red\n    | Green\n    | Blue\n\n\n";

/// The ill program, the repaired twin, and the diagnostic code the rejection
/// must carry.
///
/// **Every pair differs in the defect and nothing else.** That is asserted, not
/// asserted-by-comment: [`tests::twin_differs_minimally`] diffs the two rendered
/// sources line by line and fails if more than four lines moved.
fn defect(defect: &str, rimport: &str) -> (Side, Side, &'static str) {
    match defect {
        // ---- arity -------------------------------------------------------
        // Over-application has a DEDICATED diagnostic in Rust (`[E2007]`); the
        // oracle folds it into the generic `[E2001]` unify clash. Pinning
        // E2007 asserts the Rust expectation, exactly as the `-- rust:`
        // precedence rule in `ty::reject_corpus` requires — a gate must not
        // punish a diagnostic improvement.
        "arity_over" => (
            Side::new(ADD, "String.fromInt (add knownName 2 3)"),
            Side::new(ADD, "String.fromInt (add knownName 2)"),
            "E2007",
        ),
        // Under-application is LEGAL in Sky (currying); it is only an error
        // where a saturated value is required. So the defect is the USE, not
        // the call — and the twin saturates it.
        "arity_under_as_value" => (
            Side::new(ADD, "String.fromInt (add knownName)"),
            Side::new(ADD, "String.fromInt (add knownName 2)"),
            "E2001",
        ),

        // ---- record shapes (#166 / #171 territory) ------------------------
        "unknown_field" => (
            Side::new(
                format!("{REC}rec : Rec\nrec =\n    {{ a = knownName, b = \"x\" }}\n"),
                "String.fromInt rec.zzz",
            ),
            Side::new(
                format!("{REC}rec : Rec\nrec =\n    {{ a = knownName, b = \"x\" }}\n"),
                "String.fromInt rec.a",
            ),
            "E2001",
        ),
        "wrong_field_type" => (
            Side::new(
                format!("{REC}rec : Rec\nrec =\n    {{ a = \"nope\", b = \"x\" }}\n"),
                "String.fromInt rec.a",
            ),
            Side::new(
                format!("{REC}rec : Rec\nrec =\n    {{ a = knownName, b = \"x\" }}\n"),
                "String.fromInt rec.a",
            ),
            "E2001",
        ),
        "missing_field" => (
            Side::new(
                format!("{REC}rec : Rec\nrec =\n    {{ a = knownName }}\n"),
                "String.fromInt rec.a",
            ),
            Side::new(
                format!("{REC}rec : Rec\nrec =\n    {{ a = knownName, b = \"x\" }}\n"),
                "String.fromInt rec.a",
            ),
            "E2001",
        ),
        // The reject corpus has `record_update_nonexistent_field.sky` and
        // `record_update_wrong_type_field.sky` and BOTH declare no code — they
        // assert only "rejected", so any diagnostic satisfies them. These two
        // rows pin the code for the class.
        "record_update_unknown_field" => (
            Side::new(
                format!(
                    "{REC}rec : Rec\nrec =\n    {{ a = knownName, b = \"x\" }}\n\n\n\
                     upd : Rec\nupd =\n    {{ rec | zzz = 2 }}\n"
                ),
                "String.fromInt upd.a",
            ),
            Side::new(
                format!(
                    "{REC}rec : Rec\nrec =\n    {{ a = knownName, b = \"x\" }}\n\n\n\
                     upd : Rec\nupd =\n    {{ rec | a = 2 }}\n"
                ),
                "String.fromInt upd.a",
            ),
            "E2001",
        ),
        "record_update_wrong_type" => (
            Side::new(
                format!(
                    "{REC}rec : Rec\nrec =\n    {{ a = knownName, b = \"x\" }}\n\n\n\
                     upd : Rec\nupd =\n    {{ rec | a = \"s\" }}\n"
                ),
                "String.fromInt upd.a",
            ),
            Side::new(
                format!(
                    "{REC}rec : Rec\nrec =\n    {{ a = knownName, b = \"x\" }}\n\n\n\
                     upd : Rec\nupd =\n    {{ rec | a = 2 }}\n"
                ),
                "String.fromInt upd.a",
            ),
            "E2001",
        ),
        // A record literal that is a strict SUBSET of the declared parameter —
        // the `goty.rs:186-196` subset-resolution path, approached from the
        // reject side.
        "row_subset_missing" => (
            Side::new(
                format!("{REC}use : Rec -> Int\nuse r =\n    r.a\n"),
                "String.fromInt (use { a = knownName })",
            ),
            Side::new(
                format!("{REC}use : Rec -> Int\nuse r =\n    r.a\n"),
                "String.fromInt (use { a = knownName, b = \"x\" })",
            ),
            "E2001",
        ),

        // ---- exhaustiveness ----------------------------------------------
        // Sky treats a non-exhaustive `case` as a HARD rejection (self-host
        // R1-D3) even though the diagnostic carries `Severity::Warning`; the
        // criterion in `ty::reject_corpus` promotes `[E3001]` accordingly.
        "nonexhaustive_case" => (
            Side::new(
                format!(
                    "{COLOR}name : Color -> String\nname c =\n    case c of\n        \
                     Red ->\n            \"r\"\n\n        Green ->\n            \"g\"\n"
                ),
                "name Blue ++ String.fromInt knownName",
            ),
            Side::new(
                format!(
                    "{COLOR}name : Color -> String\nname c =\n    case c of\n        \
                     Red ->\n            \"r\"\n\n        Green ->\n            \"g\"\n\n        \
                     Blue ->\n            \"b\"\n"
                ),
                "name Blue ++ String.fromInt knownName",
            ),
            "E3001",
        ),

        // ---- names + modules ---------------------------------------------
        "unknown_name" => (
            Side::new("", "String.fromInt nosuchBinding"),
            Side::new("", "String.fromInt knownName"),
            "E1001",
        ),
        // FOUND BY THIS FAMILY, then fixed. `import Std.NoSuchModule as Nope`
        // followed by `Nope.answer` used to print "Names resolved", "Types OK",
        // emit Go, pass `go build`, and panic at run time with
        // `rt.AsInt: expected numeric value, got <nil>`. `classify_import`'s
        // `ImportSource::Foreign` arm is a TOTAL fallback, so a misspelled Sky
        // module became a Go-FFI package reference lowered to `nil`. Only the
        // CALL shape was checked, which is exactly why nobody saw it. See
        // `hir::resolve::process_import` and `hir::is_reserved_sky_namespace`.
        "unknown_module" => {
            let mut ill = Side::new("", "String.fromInt Nope.answer");
            ill.extra_imports.push("import Std.NoSuchModule as Nope".into());
            (ill, Side::new("", "String.fromInt knownName"), "E1001")
        }
        "unexposed_name" => {
            let (imp, reference, code) = unexposed(rimport);
            let mut ill = Side::new("", reference);
            ill.helper_import_override = Some(imp);
            (ill, Side::new("", "String.fromInt knownName"), code)
        }

        // ---- the two-level error surface ---------------------------------
        "result_vs_maybe" => (
            Side::new(
                "m : Maybe Int\nm =\n    Ok knownName\n",
                "String.fromInt (Maybe.withDefault 0 m)",
            ),
            Side::new(
                "m : Maybe Int\nm =\n    Just knownName\n",
                "String.fromInt (Maybe.withDefault 0 m)",
            ),
            "E2001",
        ),
        // The effect boundary: a `Task` where a pure value is required. AGENTS.md
        // states the rule ("every observable side effect returns `Task Error a`;
        // pure code is bare `a`") and nothing gated the confusion.
        "task_vs_pure" => (
            Side::new(
                "n : Int\nn =\n    Task.succeed knownName\n",
                "String.fromInt n",
            ),
            Side::new("n : Int\nn =\n    knownName\n", "String.fromInt n"),
            "E2001",
        ),

        // ---- Dict keys ----------------------------------------------------
        // A `Dict` keyed by a COMPOSITE. A Sky `Dict k v` is a Go
        // `map[string]v`, and `fmt.Sprintf("%v", key)` is NOT injective on a
        // tuple — ( "a b", "c" ) and ( "a", "b c" ) both render `{a b c}` — so
        // two distinct keys collide and no decoder can recover the original.
        // That used to be a RUNTIME panic (`rt.Dict: unsupported key type`) out
        // of a program `sky check` had passed; it is now `[E2008]` at check
        // time.
        //
        // The twin swaps the key type for `Int` — one of the five that round
        // trip — and changes nothing else, so this row asserts the check keys on
        // the KEY TYPE and not on "the program mentions Dict". Crossing it with
        // `rposition` earns its budget here specifically: the ill type is found
        // by scanning INFERRED per-expression types, and `in_let` / `in_lambda`
        // are the binding shapes where that recording could plausibly differ.
        "dict_composite_key" => {
            let dict_import = "import Sky.Core.Dict as Dict exposing (Dict)".to_string();
            let probe = "String.fromInt (Dict.size grid + knownName)";
            let ill_decls =
                "grid : Dict ( Int, Int ) String\ngrid =\n    Dict.insert ( 1, 2 ) \"w\" Dict.empty\n";
            let well_decls = "grid : Dict Int String\ngrid =\n    Dict.insert 1 \"w\" Dict.empty\n";
            let mut ill = Side::new(ill_decls, probe);
            ill.extra_imports.push(dict_import.clone());
            let mut well = Side::new(well_decls, probe);
            well.extra_imports.push(dict_import);
            (ill, well, "E2008")
        }

        other => panic!("reject_matrix: unknown defect {other:?}"),
    }
}

/// Wrap `expr` in the position the `rposition` axis selected. Every wrapper is
/// semantics-preserving on a well-typed `expr`, so the twin stays accepted in
/// all three — which is what makes the position a real axis rather than a second
/// defect.
fn position(shape: &str, expr: &str) -> String {
    match shape {
        "bare" => expr.to_string(),
        "in_let" => format!("let\n        v =\n            {expr}\n    in\n    v"),
        "in_lambda" => format!("(\\_ -> {expr}) ()"),
        other => panic!("reject_matrix: unknown rposition {other:?}"),
    }
}

fn render(side: &Side, rimport: &str, pos: &str) -> String {
    let (default_import, reference) = helper_import(rimport);
    let helper = side
        .helper_import_override
        .clone()
        .unwrap_or(default_import);
    let mut imports = vec![
        "import Sky.Core.Prelude exposing (..)".to_string(),
        "import Std.Log exposing (println)".to_string(),
        helper,
    ];
    imports.extend(side.extra_imports.iter().cloned());

    format!(
        "module Main exposing (main)\n\n{imports}\n\n\n\
         knownName : Int\nknownName =\n    {reference}\n\n\n\
         {decls}\n\
         probe : String\nprobe =\n    {body}\n\n\n\
         main =\n    println probe\n",
        imports = imports.join("\n"),
        decls = if side.decls.is_empty() {
            String::new()
        } else {
            format!("{}\n\n", side.decls)
        },
        body = position(pos, &side.expr),
    )
}

/// Build the Family-R case at `a`.
pub fn build(a: &Assignment) -> GenCase {
    let d = a.get(DEFECT);
    let rimport = a.get(RIMPORT);
    let pos = a.get(RPOSITION);
    let (ill, well, code) = defect(d, rimport);

    let modules = vec![
        (HELPER_NAME.to_string(), helper_src()),
        ("Main".to_string(), render(&ill, rimport, pos)),
    ];
    let twin = vec![
        (HELPER_NAME.to_string(), helper_src()),
        ("Main".to_string(), render(&well, rimport, pos)),
    ];

    GenCase {
        id: format!("{}/{}", STRATUM.name, a.slug()),
        stratum: STRATUM.name,
        family: Family::R,
        // Static: the program never builds or runs. That is the point — a
        // rejection is decided by the checker, in-process, at ~50 ms, which is
        // what lets the matrix be 126 programs instead of a dozen.
        mode: Mode::Static,
        isolation: Isolation::Unit,
        axes: a.clone(),
        // The generator CHOSE the code and CHOSE the twin before the compiler
        // ran; neither is read back from the compiler's output. A compiler that
        // rejected for the wrong reason, or that rejected everything, fails.
        // That is the same independence property class V names for a value, and
        // it is why these are not change-detectors.
        class: Class::V,
        witness: Witness::Diagnostic,
        coordinate: STRATUM.coordinate.map(|s| s.to_string()),
        modules,
        entry: "Main".to_string(),
        expect: Expect::Reject {
            code: code.to_string(),
        },
        // Never batched, so never rendered as a batch member.
        body: None,
        blocked: None,
        twin: Some(twin),
        emit_properties: Vec::new(),
    }
}

/// Every Family-R case: the full cross of the three axes.
pub fn all() -> Vec<GenCase> {
    super::axes::full_cross(&STRATUM).iter().map(build).collect()
}

// ---------------------------------------------------------------------------
// The gate
// ---------------------------------------------------------------------------

/// One case's outcome. Two independent checks per case, reported separately so a
/// failure names which half broke.
pub struct RowOutcome {
    pub id: String,
    pub rejected: bool,
    pub declared: String,
    pub observed: Vec<String>,
    pub twin_accepted: bool,
    pub twin_detail: String,
}

impl RowOutcome {
    pub fn ok(&self) -> bool {
        self.rejected && self.observed.iter().any(|c| c == &self.declared) && self.twin_accepted
    }
}

/// Evaluate every Family-R case against the checker.
///
/// Both halves go through `ty::reject_corpus::evaluate_modules` — the SINGLE
/// declaration of "rejected" that `xtask reject` and `cargo test -p ty --test
/// reject` also read. A private criterion here is how a third face would come to
/// disagree with the other two (v2 §1.5).
pub fn evaluate(root: &Path) -> Result<Vec<RowOutcome>, String> {
    let stdlib = ty::reject_corpus::load_stdlib(root);
    if stdlib.is_empty() {
        return Err(format!(
            "corpus.reject: no stdlib modules under {}/sky-stdlib — a gate that \
             cannot load the world has not checked anything",
            root.display()
        ));
    }

    let mut out = Vec::new();
    for case in all() {
        let Expect::Reject { code } = &case.expect else {
            unreachable!("every Family-R case declares a rejection");
        };
        let v = ty::reject_corpus::evaluate_modules(&case.id, &case.modules, &stdlib);
        let twin = case.twin.as_ref().expect("every Family-R case has a twin");
        let tv = ty::reject_corpus::evaluate_modules(&case.id, twin, &stdlib);
        out.push(RowOutcome {
            id: case.id.clone(),
            rejected: v.rejected(),
            declared: code.clone(),
            observed: v.observed_codes.clone(),
            twin_accepted: !tv.rejected(),
            twin_detail: if tv.rejected() {
                format!("{} [{}]", tv.signal(), tv.first_msg)
            } else {
                String::new()
            },
        });
    }
    Ok(out)
}

pub fn run(root: &Path) -> i32 {
    let rows = match evaluate(root) {
        Ok(r) => r,
        Err(msg) => {
            eprintln!("{msg}");
            return 1;
        }
    };

    println!("CORPUS REJECT MATRIX — v2 §3.1 family R (code-pinned rejection + accepted twin)");
    println!("  cases      : {}", rows.len());
    println!("  assertions : {} (one rejection + one twin per case)", rows.len() * 2);
    println!();

    let accepted_holes: Vec<&RowOutcome> = rows.iter().filter(|r| !r.rejected).collect();
    let wrong_code: Vec<&RowOutcome> = rows
        .iter()
        .filter(|r| r.rejected && !r.observed.contains(&r.declared))
        .collect();
    let twin_holes: Vec<&RowOutcome> = rows.iter().filter(|r| !r.twin_accepted).collect();

    if !accepted_holes.is_empty() {
        println!("  ---- {} SOUNDNESS HOLE(S): accepted, must be rejected ----", accepted_holes.len());
        for r in &accepted_holes {
            println!("  {}", r.id);
        }
        println!();
    }
    if !wrong_code.is_empty() {
        println!("  ---- {} rejected by the WRONG diagnostic ----", wrong_code.len());
        for r in &wrong_code {
            println!("  {}  declared {} observed {:?}", r.id, r.declared, r.observed);
        }
        println!("  A rejection that carries a different code is a rejection for a");
        println!("  different reason. Fix the checker, or — where Rust legitimately");
        println!("  emits a better code than the class expects — change the DECLARED");
        println!("  code in `defect()`, in a commit that says which diagnostic won.");
        println!();
    }
    if !twin_holes.is_empty() {
        println!("  ---- {} TWIN(S) REJECTED: the pair proves nothing ----", twin_holes.len());
        for r in &twin_holes {
            println!("  {}  twin: {}", r.id, r.twin_detail);
        }
        println!("  The twin repairs the defect and must compile. A rejected twin means");
        println!("  the case's rejection cannot be attributed to the defect under test —");
        println!("  the generated program is broken somewhere else, or the axis (import");
        println!("  shape / position) is itself broken.");
        println!();
    }

    let bad = accepted_holes.len() + wrong_code.len() + twin_holes.len();
    if bad == 0 {
        println!(
            "REJECT-MATRIX GATE: PASS ({} cases; every rejection carries its declared \
             code and every twin compiles)",
            rows.len()
        );
        0
    } else {
        println!("REJECT-MATRIX GATE: FAIL ({bad} defect(s))");
        1
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::BTreeSet;

    #[test]
    fn full_cross_is_the_product_of_the_axis_sizes() {
        let expected: usize = STRATUM.axes.iter().map(|a| a.values.len()).product();
        assert_eq!(all().len(), expected);
    }

    #[test]
    fn every_case_declares_a_code_and_carries_a_twin() {
        for c in all() {
            match &c.expect {
                Expect::Reject { code } => {
                    assert!(
                        code.starts_with('E') && code.len() == 5,
                        "{}: declared code {code:?} is not a diagnostic code",
                        c.id
                    );
                }
                Expect::Accept { .. } => panic!("{}: family R must declare a rejection", c.id),
            }
            let twin = c.twin.as_ref().unwrap_or_else(|| {
                panic!(
                    "{}: no accepted twin. Without one the rejection could be caused by \
                     anything, and a compiler that rejected every program would score 100 %.",
                    c.id
                )
            });
            assert_eq!(twin.len(), c.modules.len(), "{}: twin module count", c.id);
        }
    }

    /// The twin repairs the DEFECT and nothing else.
    ///
    /// This is the property the whole family rests on, so it is checked rather
    /// than promised: if the twin differed from the case in a dozen places, the
    /// pair would no longer isolate the defect and "the rejection is caused by
    /// the axis under test" would be an unsupported claim.
    ///
    /// The measure is the **symmetric multiset difference of lines**, not a
    /// positional comparison. A positional one is wrong for an INSERTION:
    /// repairing `nonexhaustive_case` adds a `Blue ->` arm, every later line
    /// shifts down, and a positional diff called that twelve changes when three
    /// lines were added. Six is the widest legitimate repair here (a replaced
    /// import line plus a replaced expression is four; an added `case` arm with
    /// its blank line is three).
    #[test]
    fn twin_differs_minimally() {
        use std::collections::BTreeMap;
        let mut worst = 0usize;
        for c in all() {
            let twin = c.twin.as_ref().unwrap();
            for ((n, a), (_, b)) in c.modules.iter().zip(twin.iter()) {
                let mut counts: BTreeMap<&str, i64> = BTreeMap::new();
                for l in a.lines() {
                    *counts.entry(l).or_default() += 1;
                }
                for l in b.lines() {
                    *counts.entry(l).or_default() -= 1;
                }
                let differing: usize =
                    counts.values().map(|v| v.unsigned_abs() as usize).sum();
                worst = worst.max(differing);
                assert!(
                    differing <= 6,
                    "{} module {n}: twin differs in {differing} line(s); a twin must \
                     repair the defect and nothing else",
                    c.id
                );
            }
        }
        assert!(worst > 0, "no twin differs from its case at all");
    }

    #[test]
    fn ids_are_unique() {
        let mut seen = BTreeSet::new();
        for c in all() {
            assert!(seen.insert(c.id.clone()), "duplicate case id {}", c.id);
        }
    }

    /// Every defect value is reachable and renders. A defect named in the axis
    /// but missing from `defect()` would panic at gate runtime instead of at
    /// `cargo test` — the exact failure mode `witness.rs` recorded when
    /// `fieldset_ctor` was added without an `axis_under_test` entry.
    #[test]
    fn every_defect_value_renders_under_every_import_shape() {
        for d in DEFECT.values {
            for i in RIMPORT.values {
                let (ill, well, code) = defect(d, i);
                assert!(!code.is_empty(), "{d}/{i}: no declared code");
                for p in RPOSITION.values {
                    assert!(!render(&ill, i, p).is_empty());
                    assert!(!render(&well, i, p).is_empty());
                }
            }
        }
    }
}

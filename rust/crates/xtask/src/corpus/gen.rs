//! The case generator — Sky source, and a **generator-constructed** expected
//! value.
//!
//! # The honesty constraint (v2 §4.4)
//!
//! Generated cases have no independent oracle. A generated case whose "expected"
//! value is whatever the compiler produced is a **change-detector, not a
//! correctness test** — it would not have caught #173 on the day #173 shipped,
//! because on that day the compiler's answer WAS the wrong answer. The Haskell
//! oracle cannot close this either: it is unavailable in CI, and
//! `welltyped_gate.rs:41-45` SKIPs with exit 0 without it.
//!
//! So this generator only ever asserts values it **constructed itself**:
//!
//! * it picks the literal `42` and writes it into a record field, then asserts
//!   that reading that field back yields `42`;
//! * it picks the update value `7`, then asserts the updated field is `7`.
//!
//! Neither number is read from the compiler. Both are known before the compiler
//! runs. That is what makes the assertion a correctness test rather than a
//! snapshot — and it is exactly the assertion that catches #166/#171 (an
//! un-updated field silently zeroed) and the `goty.rs` fieldset collision (a
//! field read resolving against the wrong struct).
//!
//! Cases that cannot carry such a value are labelled [`Class::D`] — a
//! change-detector — and v2 §5.5 **excludes them from the coverage number**.
//!
//! # The axis witness
//!
//! A case that varies an axis but asserts something independent of that axis
//! does not cover the axis; it only spends budget. For most axes here the VALUE
//! is deliberately axis-invariant (that is the property under test: moving a
//! record update into a tuple must not change what it computes), so the value is
//! the *correctness oracle* and the **emitted Go is the axis witness**. Both are
//! required; see `witness.rs`.

use super::axes::*;

/// v2 §3.1's five families.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Family {
    /// stdlib behaviour — every public symbol at its edge classes
    S,
    /// language matrix — accept/reject, inferred type, and (class V) the value
    L,
    /// emit shape — assertions on the generated Go; no `go build`
    E,
    /// reject matrix — rejection by diagnostic code, with a paired accepted twin
    R,
    /// deep sampler — randomised; the only mechanism that finds unconceived values
    F,
}

impl Family {
    pub fn label(self) -> &'static str {
        match self {
            Family::S => "S",
            Family::L => "L",
            Family::E => "E",
            Family::R => "R",
            Family::F => "F",
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Mode {
    Static,
    EmitShape,
    Behavioural,
}

impl Mode {
    pub fn label(self) -> &'static str {
        match self {
            Mode::Static => "static",
            Mode::EmitShape => "emit_shape",
            Mode::Behavioural => "behavioural",
        }
    }
}

/// v2 §3.2. `Unit` means one compilation unit, never batched — because the
/// case's verdict would otherwise depend on its neighbours.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Isolation {
    Batch,
    Unit,
}

impl Isolation {
    pub fn label(self) -> &'static str {
        match self {
            Isolation::Batch => "batch",
            Isolation::Unit => "unit",
        }
    }
}

/// v2 §4.4. `V` = the generator constructed the expected answer independently.
/// `D` = change-detector, **excluded from the coverage numerator**.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Class {
    V,
    D,
}

impl Class {
    pub fn label(self) -> &'static str {
        match self {
            Class::V => "V",
            Class::D => "D",
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Witness {
    Value,
    Shape,
    Diagnostic,
}

impl Witness {
    pub fn label(self) -> &'static str {
        match self {
            Witness::Value => "value",
            Witness::Shape => "shape",
            Witness::Diagnostic => "diagnostic",
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Expect {
    /// The program type-checks and prints exactly `stdout`.
    Accept { stdout: String },
    /// The program is rejected carrying `code`. Paired with an axis-neutralised
    /// twin that must be accepted (v2 §4.4) — without the twin the rejection
    /// could be caused by an unrelated error and the coverage claim is
    /// unfalsifiable.
    Reject { code: String },
}

/// One generated case.
#[derive(Clone, Debug)]
pub struct GenCase {
    pub id: String,
    pub stratum: &'static str,
    pub family: Family,
    pub mode: Mode,
    pub isolation: Isolation,
    pub axes: Assignment,
    pub class: Class,
    pub witness: Witness,
    pub coordinate: Option<String>,
    /// `(module name, source)`; the entry module last.
    pub modules: Vec<(String, String)>,
    pub entry: String,
    pub expect: Expect,
    /// Present for single-module cases; `None` for the multi-module
    /// `import_shape` stratum, which cannot be batched anyway (it is `Unit`).
    pub body: Option<Body>,
    /// A known product defect this case exhibits (v2 §7.2's `BLOCKED`).
    pub blocked: Option<Blocked>,
}

/// A case that is red because the PRODUCT is broken, not because the case is.
///
/// v2 §7.2's contract, applied per case: a blocked case **still runs**, it
/// **never contributes PASS**, its transition to green is reported, and once the
/// expiry passes it **FAILs**. That last clause is what stops a blocked row from
/// becoming a permanent excuse — the mechanism the mandate calls "a gate that
/// cannot fail is worse than no gate", pointed at our own escape hatch.
///
/// It is deliberately NOT a skip. Skipping is how `SKIP` came to be counted as
/// `pass` and how nightly reported "29 passed, 0 failed" with three examples
/// never built.
#[derive(Clone, Debug)]
pub struct Blocked {
    pub issue: &'static str,
    /// `YYYY-MM-DD`. After this date the case FAILS even while still red, so a
    /// defect cannot be parked indefinitely behind a green gate.
    pub expires: &'static str,
    pub reason: &'static str,
}

/// Known product defects the corpus currently exhibits.
///
/// Every entry here is a REPRODUCTION of a real bug, with a repro file under
/// `corpus/repro/`. Adding an entry is how a corpus find is landed without
/// either silencing it or blocking the branch — never a way to make a case
/// stop mattering.
fn blocked_reason(stratum: &str, a: &Assignment) -> Option<Blocked> {
    if stratum == "fieldset_ctor"
        && a.get(CONSTRUCTION) == "via_ctor_fn"
        && a.get(COLLIDER) == "stdlib_eventprop"
    {
        return Some(Blocked {
            issue: "sky/record-fieldset-collision-ctor (found by this corpus, 2026-08-10)",
            expires: "2026-11-10",
            reason:
                "A user record `{ key : String, value : String }` built through an \
                 ANNOTATED constructor function collides with the real \
                 `Std.Analytics.EventProp = { key : String, value : PropValue }` \
                 (sky-stdlib/Std/Analytics.sky:85-88). `record_fieldsets` \
                 (lower/src/lower.rs:246-266) is keyed on the sorted field-NAME \
                 vector, so both shapes land on `[key, value]`; the constructor's \
                 `value` parameter is then coerced to `PropValue`. `sky build` \
                 reports \"Types OK\" and the program panics at runtime: \
                 `rt.Coerce: expected rt.SkyADT, got string`. No import of \
                 Std.Analytics is needed — the stdlib is always in the \
                 compilation. Repro: corpus/repro/fieldset-ctor-stdlib-collision.sky",
        });
    }
    None
}

impl GenCase {
    pub fn entry_source(&self) -> &str {
        &self
            .modules
            .iter()
            .find(|(n, _)| *n == self.entry)
            .expect("entry module is present")
            .1
    }
}

// ---------------------------------------------------------------------------
// Shared vocabulary
//
// Every literal the assertions rest on is chosen HERE, by the generator, before
// any compiler runs. `SURVIVOR` is the load-bearing one: it is the value of a
// field the case never updates, so asserting it comes back intact is exactly the
// #166/#171 assertion.
// ---------------------------------------------------------------------------

/// The value written into the field under update.
const UPDATED: i64 = 7;
/// The value written into a field the case NEVER touches. If a record update
/// drops un-updated fields (#166), or a field read resolves against the wrong
/// struct (`goty.rs` fieldset collision), this comes back as `0` or the program
/// fails to build. Either way the assertion goes red.
const SURVIVOR: i64 = 42;

const PRELUDE: &str = "module Main exposing (main)\n\n\
                       import Sky.Core.Prelude exposing (..)\n\
                       import Std.Log exposing (println)\n\n\n";

const REC_DECL: &str = "type alias Rec =\n    { a : Int, b : String, c : Int }\n\n\n";

fn base_record() -> String {
    format!("{{ a = 1, b = \"x\", c = {SURVIVOR} }}")
}

// ---------------------------------------------------------------------------
// Stratum: record_update  (#166, #171)
// ---------------------------------------------------------------------------

/// Emit the record-update stratum.
///
/// The function under test updates field `a` to [`UPDATED`] and never mentions
/// `c`. `main` prints `a` then `c`. The expected stdout is `"7/42"` **by
/// construction** — both numbers are chosen above, neither is observed.
///
/// * `position` changes WHERE the update is built (bare / inside a tuple / a
///   list / a record field / a lambda / a let). #166 was `in_tuple`.
/// * `carrier` changes the higher-order context it is reached through. #171 was
///   `via_foldl`/`via_foldr`.
/// * `row` chooses a nominal parameter vs an explicit row variable.
/// * `annotation` writes or omits the signature.
fn record_update(a: &Assignment) -> (String, String, String) {
    let position = a.get(POSITION);
    let carrier = a.get(CARRIER);
    let row = a.get(ROW);
    let annotated = a.get(ANNOTATION) == "annotated";

    // The core update expression, and the type of what `bump` returns.
    let (body, ret_ty, extract) = match position {
        "bare" => ("{ r | a = 7 }".to_string(), "Rec".to_string(), "u".to_string()),
        "in_tuple" => (
            "( { r | a = 7 }, 0 )".to_string(),
            "( Rec, Int )".to_string(),
            "fst u".to_string(),
        ),
        "in_list" => (
            "[ { r | a = 7 } ]".to_string(),
            "List Rec".to_string(),
            format!("List.foldl (\\x _ -> x) {} u", base_record()),
        ),
        "in_record_field" => (
            "{ inner = { r | a = 7 }, tag = 0 }".to_string(),
            "{ inner : Rec, tag : Int }".to_string(),
            "u.inner".to_string(),
        ),
        "in_lambda" => (
            "(\\_ -> { r | a = 7 }) ()".to_string(),
            "Rec".to_string(),
            "u".to_string(),
        ),
        "in_let" => (
            "let\n        upd =\n            { r | a = 7 }\n    in\n    upd".to_string(),
            "Rec".to_string(),
            "u".to_string(),
        ),
        other => panic!("record_update: unknown position {other:?}"),
    };

    // `row` only reshapes the SIGNATURE, and only the nominal-return positions
    // can carry an explicit row variable (a tuple/list/wrapper return would need
    // the row variable to appear under a constructor, which is a different axis).
    let nominal_ret = ret_ty == "Rec";
    let sig = if !annotated {
        String::new()
    } else if row == "open_subset" && nominal_ret {
        "bump : { r | a : Int } -> { r | a : Int }\n".to_string()
    } else {
        format!("bump : Rec -> {ret_ty}\n")
    };

    // The carrier wraps the CALL, not the body, so `carrier` and `position` stay
    // independent axes — a confound here would make the pairwise coverage claim
    // meaningless.
    let call = match carrier {
        "direct" => "bump base".to_string(),
        "via_foldl" => "List.foldl (\\x _ -> bump x) (bump base) [ base ]".to_string(),
        "via_foldr" => "List.foldr (\\x _ -> bump x) (bump base) [ base ]".to_string(),
        "via_map" => {
            format!(
                "List.foldl (\\x _ -> x) (bump base) (List.map bump [ base ])"
            )
        }
        other => panic!("record_update: unknown carrier {other:?}"),
    };

    let decls = format!("{REC_DECL}{sig}bump r =\n    {body}\n");
    let check = format!(
        "let\n        base =\n            {base}\n\n        u =\n            {call}\n\n        res =\n            {extract}\n    in\n    String.fromInt res.a ++ \"/\" ++ String.fromInt res.c",
        base = base_record(),
    );

    (decls, check, format!("{UPDATED}/{SURVIVOR}"))
}

// ---------------------------------------------------------------------------
// Stratum: destructure  (#170, #172)
// ---------------------------------------------------------------------------

/// Destructure a subject whose type has been erased along `erasure`. #170/#172
/// were the same destructure working on a typed subject and failing on an
/// erased one. Asserts the projected field is [`SURVIVOR`] — constructed here.
fn destructure(a: &Assignment) -> (String, String, String) {
    let erasure = a.get(ERASURE);
    let position = a.get(POSITION);

    // How the pair reaches the destructuring site.
    let subject = match erasure {
        "direct" => "( base, 1 )".to_string(),
        "via_foldr" => "List.foldr (\\x _ -> x) ( base, 1 ) [ ( base, 1 ) ]".to_string(),
        "via_let" => "let\n            p0 =\n                ( base, 1 )\n        in\n        p0".to_string(),
        "via_fst_snd" => "( fst ( base, 1 ), snd ( base, 1 ) )".to_string(),
        "via_tuple_destructure" => {
            "let\n            ( r0, n0 ) =\n                ( base, 1 )\n        in\n        ( r0, n0 )"
                .to_string()
        }
        other => panic!("destructure: unknown erasure {other:?}"),
    };

    // Where the projection happens.
    let project = match position {
        "bare" => "(fst p).c".to_string(),
        "in_tuple" => "fst ( (fst p).c, 0 )".to_string(),
        "in_list" => "List.foldl (\\x _ -> x) 0 [ (fst p).c ]".to_string(),
        "in_record_field" => "{ v = (fst p).c }.v".to_string(),
        "in_lambda" => "(\\_ -> (fst p).c) ()".to_string(),
        "in_let" => {
            "let\n            ( rr, _ ) =\n                p\n        in\n        rr.c".to_string()
        }
        other => panic!("destructure: unknown position {other:?}"),
    };

    let check = format!(
        "let\n        base =\n            {base}\n\n        p =\n            {subject}\n\n        res =\n            {project}\n    in\n    String.fromInt res",
        base = base_record(),
    );

    (REC_DECL.to_string(), check, format!("{SURVIVOR}"))
}

// ---------------------------------------------------------------------------
// Stratum: type_nesting  (#173)
// ---------------------------------------------------------------------------

/// `Dict k (List Record)` and its neighbours. #173 was three defects in this
/// shape. The assertion is `List.length` of a list the generator constructed
/// (class-V form 2) or the field of a record it constructed (form 3).
fn type_nesting(a: &Assignment) -> (String, String, String) {
    let outer = a.get(OUTER);
    let inner = a.get(INNER);
    let elem = a.get(ELEM);

    // The element, and how to read SURVIVOR back out of one.
    //
    // `EL` is a placeholder for the bound element name, substituted below. It is
    // a distinct token rather than a bare `e` because a character-level
    // substitution mangles any reader that happens to contain that letter — the
    // first spike run generated `Risult.withDifault` exactly that way.
    const EL: &str = "@EL@";
    let (elem_ty, elem_lit, read_elem) = match elem {
        "int" => ("Int", format!("{SURVIVOR}"), EL.to_string()),
        "string" => (
            "String",
            format!("\"{SURVIVOR}\""),
            // `String.toInt : String -> Maybe Int` (verified with `sky doc
            // Sky.Core.String`). AGENTS.md:79 documented it as
            // `Result Error Int`; the spike caught the drift.
            format!("Maybe.withDefault 0 (String.toInt {EL})"),
        ),
        "record" => ("Rec", base_record(), format!("{EL}.c")),
        other => panic!("type_nesting: unknown elem {other:?}"),
    };
    let read_elem_as = |bound: &str| read_elem.replace(EL, bound);
    let read_elem = read_elem_as("e");

    // Wrap the element in the inner constructor.
    let (inner_ty, inner_lit, unwrap_inner) = match inner {
        "none" => (elem_ty.to_string(), elem_lit.clone(), "i".to_string()),
        "list" => (
            format!("List {elem_ty}"),
            format!("[ {elem_lit} ]"),
            format!("List.foldl (\\e _ -> {read_elem}) 0 i"),
        ),
        "maybe" => (
            format!("Maybe {elem_ty}"),
            format!("Just {elem_lit}"),
            format!("case i of\n                    Just e ->\n                        {read_elem}\n\n                    Nothing ->\n                        0"),
        ),
        other => panic!("type_nesting: unknown inner {other:?}"),
    };
    // When `inner = none` the outer constructor binds the element directly as
    // `i`, so the reader is re-rendered against that name.
    let unwrap_inner = if inner == "none" {
        read_elem_as("i")
    } else {
        unwrap_inner
    };

    // Wrap again in the outer constructor and unwrap it back down.
    let (outer_ty, outer_lit, unwrap_outer) = match outer {
        "dict" => (
            format!("Dict String ({inner_ty})"),
            format!("Dict.insert \"k\" ({inner_lit}) Dict.empty"),
            format!("case Dict.get \"k\" nested of\n            Just i ->\n                {unwrap_inner}\n\n            Nothing ->\n                0"),
        ),
        "list" => (
            format!("List ({inner_ty})"),
            format!("[ {inner_lit} ]"),
            format!("List.foldl (\\i _ -> {unwrap_inner}) 0 nested"),
        ),
        "maybe" => (
            format!("Maybe ({inner_ty})"),
            format!("Just ({inner_lit})"),
            format!("case nested of\n            Just i ->\n                {unwrap_inner}\n\n            Nothing ->\n                0"),
        ),
        "result" => (
            format!("Result Error ({inner_ty})"),
            format!("Ok ({inner_lit})"),
            format!("case nested of\n            Ok i ->\n                {unwrap_inner}\n\n            Err _ ->\n                0"),
        ),
        other => panic!("type_nesting: unknown outer {other:?}"),
    };

    let decls = format!("{REC_DECL}nested : {outer_ty}\nnested =\n    {outer_lit}\n");
    let check =
        format!("let\n        res =\n            {unwrap_outer}\n    in\n    String.fromInt res");

    (decls, check, format!("{SURVIVOR}"))
}

// ---------------------------------------------------------------------------
// Stratum: fieldset_collision  (goty.rs)   — ISOLATED
// ---------------------------------------------------------------------------

/// Two record aliases with the same field NAMES and different field TYPES.
///
/// `record_fieldsets` (`lower/src/lower.rs:246-266`) is keyed on the sorted
/// field-NAME vector and its own comment at `:256-258` records that such records
/// **collide there**. This stratum is `isolation = Unit` for exactly that
/// reason: batching two of these into one compilation unit makes each case's
/// verdict depend on its neighbour.
///
/// The assertion is a field read on each of the two colliding shapes. If the
/// wrong struct is selected, the `Int` read returns the `String` field's slot —
/// a build failure or a wrong value. Both are red.
fn fieldset_collision(a: &Assignment) -> (String, String, String) {
    let collision = a.get(COLLISION);
    let erasure = a.get(ERASURE);

    // The second alias, which is where the collision lives.
    let second = match collision {
        // Identical field names, different field types — the documented collision.
        "same_names_diff_types" => {
            "type alias KvB =\n    { key : String, value : String }\n\n\n"
        }
        // A strict subset of the first's field names — the `goty.rs:186-196` path.
        "subset" => "type alias KvB =\n    { key : String }\n\n\n",
        // No collision: the neutralised twin.
        "none" => "type alias KvB =\n    { label : String, count : Int }\n\n\n",
        // A name that also exists in the stdlib surface.
        "shadows_stdlib" => "type alias KvB =\n    { key : String, value : Bool }\n\n\n",
        other => panic!("fieldset_collision: unknown collision {other:?}"),
    };

    let read_b = match collision {
        "same_names_diff_types" => "b.value".to_string(),
        "subset" => "b.key".to_string(),
        "none" => "b.label".to_string(),
        "shadows_stdlib" => {
            "if b.value then\n                \"t\"\n\n            else\n                \"f\"".to_string()
        }
        _ => unreachable!(),
    };
    let b_lit = match collision {
        "same_names_diff_types" => "{ key = \"k\", value = \"s\" }",
        "subset" => "{ key = \"s\" }",
        "none" => "{ label = \"s\", count = 0 }",
        "shadows_stdlib" => "{ key = \"k\", value = True }",
        _ => unreachable!(),
    };
    // `shadows_stdlib` reads a Bool, so its printed form is "t"; every other
    // variant prints the String field, which the generator set to "s".
    let b_expected = if collision == "shadows_stdlib" { "t" } else { "s" };

    // How the Int-valued record reaches its field read.
    let read_a = match erasure {
        "direct" => "a0.value".to_string(),
        "via_foldr" => "List.foldr (\\x _ -> x.value) 0 [ a0 ]".to_string(),
        "via_let" => "let\n            a1 =\n                a0\n        in\n        a1.value".to_string(),
        "via_fst_snd" => "(fst ( a0, 0 )).value".to_string(),
        "via_tuple_destructure" => {
            "let\n            ( a1, _ ) =\n                ( a0, 0 )\n        in\n        a1.value"
                .to_string()
        }
        other => panic!("fieldset_collision: unknown erasure {other:?}"),
    };

    let decls = format!("type alias KvA =\n    {{ key : String, value : Int }}\n\n\n{second}");
    let check = format!(
        "let\n        a0 =\n            {{ key = \"k\", value = {SURVIVOR} }}\n\n        b =\n            {b_lit}\n\n        av =\n            {read_a}\n\n        bv =\n            {read_b}\n    in\n    String.fromInt av ++ \"/\" ++ bv"
    );

    (decls, check, format!("{SURVIVOR}/{b_expected}"))
}

// ---------------------------------------------------------------------------
// Stratum: fieldset_ctor   — ISOLATED
//
// The construction-site axis. This stratum found a LIVE defect (see
// `blocked_reason` below): a `{ key : String, value : String }` record built
// through an annotated constructor function collides with the real
// `Std.Analytics.EventProp = { key : String, value : PropValue }`, and the
// constructor's parameter is coerced to the wrong nominal type. `sky build`
// reports "Types OK"; the program panics at runtime with `CoerceFailure`.
// ---------------------------------------------------------------------------

fn fieldset_ctor(a: &Assignment) -> (String, String, String) {
    let construction = a.get(CONSTRUCTION);
    let collider = a.get(COLLIDER);

    // `stdlib_eventprop` uses the field-name set of the REAL
    // `Std.Analytics.EventProp`. `local` uses a name set that collides with
    // nothing in the stdlib — the neutralised twin.
    let (f1, f2) = match collider {
        "stdlib_eventprop" => ("key", "value"),
        "local" => ("gkey", "gvalue"),
        other => panic!("fieldset_ctor: unknown collider {other:?}"),
    };

    let decls = match construction {
        // Built inline at the use site: the literal carries its own types.
        "inline" => format!("type alias Kv =\n    {{ {f1} : String, {f2} : String }}\n"),
        // Built inside an ANNOTATED constructor function from its parameters.
        "via_ctor_fn" => format!(
            "type alias Kv =\n    {{ {f1} : String, {f2} : String }}\n\n\n\
             mk : String -> String -> Kv\nmk k v =\n    {{ {f1} = k, {f2} = v }}\n"
        ),
        other => panic!("fieldset_ctor: unknown construction {other:?}"),
    };

    let check = match construction {
        "inline" => format!(
            "let\n        kv =\n            {{ {f1} = \"a\", {f2} = \"{SURVIVOR}\" }}\n    in\n    kv.{f2}"
        ),
        "via_ctor_fn" => format!("(mk \"a\" \"{SURVIVOR}\").{f2}"),
        _ => unreachable!(),
    };

    (decls, check, format!("{SURVIVOR}"))
}

// ---------------------------------------------------------------------------
// Stratum: import_shape  (#164)   — ISOLATED
// ---------------------------------------------------------------------------

/// How a name enters scope, and what it collides with. #164 was an import-alias
/// collision; the qualifier heuristic that "fixed" it regressed a real app
/// because the alias was **not the last path segment** — hence that axis value.
///
/// `isolation = Unit` because whole-program name resolution IS the subject under
/// test (v2 §3.2 family 3).
///
/// Draws on REAL stdlib module names. v2 §3.1: a generated module graph that
/// collides against *fictional* names cannot reproduce #164, which required real
/// stdlib names in scope.
fn import_shape(a: &Assignment) -> (Vec<(String, String)>, String, String) {
    let shape = a.get(IMPORT_SHAPE);
    let collision = a.get(COLLISION);

    // A helper module the case imports. Its name deliberately varies so the
    // alias can be made NOT the last path segment.
    let helper_name = "Helper.Inner.Values";
    let helper_src = format!(
        "module Helper.Inner.Values exposing (answer, label)\n\n\
         import Sky.Core.Prelude exposing (..)\n\n\n\
         answer : Int\nanswer =\n    {SURVIVOR}\n\n\n\
         label : String\nlabel =\n    \"v\"\n"
    );

    // The import line, and how `answer` is referenced.
    let (import_line, reference) = match shape {
        "plain" => (
            format!("import {helper_name}"),
            "Values.answer".to_string(),
        ),
        "aliased" => (
            format!("import {helper_name} as Values"),
            "Values.answer".to_string(),
        ),
        // The #164 regression shape: the alias is NOT the module's last segment.
        "alias_not_last_segment" => (
            format!("import {helper_name} as Inner"),
            "Inner.answer".to_string(),
        ),
        "exposing_list" => (
            format!("import {helper_name} exposing (answer)"),
            "answer".to_string(),
        ),
        "exposing_all" => (
            format!("import {helper_name} exposing (..)"),
            "answer".to_string(),
        ),
        other => panic!("import_shape: unknown shape {other:?}"),
    };

    // What else is in scope, competing for the same name.
    let extra = match collision {
        "none" => String::new(),
        // A local binding with the same bare name as the imported one.
        "same_names_diff_types" | "subset" => {
            "\n\nanswer2 : Int\nanswer2 =\n    0\n".to_string()
        }
        // A local alias whose bare name matches a real stdlib module's segment.
        "shadows_stdlib" => "\n\nlabel2 : String\nlabel2 =\n    \"local\"\n".to_string(),
        other => panic!("import_shape: unknown collision {other:?}"),
    };

    let main_src = format!(
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\
         {import_line}\n{extra}\n\n\
         main =\n    println (String.fromInt {reference})\n"
    );

    (
        vec![
            (helper_name.to_string(), helper_src),
            ("Main".to_string(), main_src),
        ],
        "Main".to_string(),
        format!("{SURVIVOR}"),
    )
}

// ---------------------------------------------------------------------------
// Assembling a case
// ---------------------------------------------------------------------------

/// A single-module case's reusable pieces: its declarations, and the
/// `String`-valued expression whose result the generator predicted.
///
/// Keeping these separate from any one module wrapper is what lets the SAME case
/// be built two ways — standalone, and as one member of a batched compilation
/// unit — which is precisely what the v2 §3.2 isolation gate has to compare.
#[derive(Clone, Debug)]
pub struct Body {
    pub decls: String,
    pub check: String,
}

/// The standalone form: its own `Main`, printing the checked value.
pub fn standalone_module(b: &Body) -> String {
    format!(
        "{PRELUDE}{decls}\n\ncheckValue : String\ncheckValue =\n    {check}\n\n\nmain =\n    println checkValue\n",
        decls = b.decls,
        check = b.check,
    )
}

/// The batched form: a library module exposing `checkValue`, to be compiled
/// alongside its neighbours in ONE compilation unit.
pub fn batch_module(index: usize, b: &Body) -> (String, String) {
    let name = format!("Batch.Case{index:04}");
    let src = format!(
        "module {name} exposing (checkValue)\n\nimport Sky.Core.Prelude exposing (..)\n\n\n{decls}\n\ncheckValue : String\ncheckValue =\n    {check}\n",
        decls = b.decls,
        check = b.check,
    );
    (name, src)
}

/// Build the case at `assignment` in `stratum`.
pub fn build(stratum: &Stratum, assignment: &Assignment) -> GenCase {
    let (modules, entry, stdout, body) = match stratum.name {
        "import_shape" => {
            // The only multi-module stratum: the module graph IS the subject.
            let (mods, entry, out) = import_shape(assignment);
            (mods, entry, out, None)
        }
        name => {
            let (decls, check, out) = match name {
                "record_update" => record_update(assignment),
                "destructure" => destructure(assignment),
                "type_nesting" => type_nesting(assignment),
                "fieldset_collision" => fieldset_collision(assignment),
                "fieldset_ctor" => fieldset_ctor(assignment),
                other => panic!("no emitter for stratum {other:?}"),
            };
            let body = Body { decls, check };
            (
                vec![("Main".to_string(), standalone_module(&body))],
                "Main".to_string(),
                out,
                Some(body),
            )
        }
    };

    GenCase {
        id: format!("{}/{}", stratum.name, assignment.slug()),
        stratum: stratum.name,
        family: Family::L,
        // Every stratum here carries a generator-constructed value, so every one
        // is behavioural: the program is built AND RUN and its stdout compared.
        // A static-only verdict would miss the entire "compiles clean, behaves
        // wrong" class this corpus exists for.
        mode: Mode::Behavioural,
        isolation: if stratum.isolated {
            Isolation::Unit
        } else {
            Isolation::Batch
        },
        axes: assignment.clone(),
        class: Class::V,
        witness: Witness::Value,
        coordinate: stratum.coordinate.map(|s| s.to_string()),
        modules,
        entry,
        expect: Expect::Accept { stdout },
        body,
        blocked: blocked_reason(stratum.name, assignment),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Every point in every stratum's full cross must render without panicking,
    /// and must produce a non-empty expected stdout that the generator chose.
    #[test]
    fn every_point_in_every_stratum_renders() {
        for s in STRATA {
            for a in full_cross(s) {
                let c = build(s, &a);
                assert!(!c.modules.is_empty(), "{} {a}: no modules", s.name);
                match &c.expect {
                    Expect::Accept { stdout } => {
                        assert!(!stdout.is_empty(), "{} {a}: empty expected stdout", s.name);
                        // The load-bearing property: the expectation mentions a
                        // literal the GENERATOR chose, never one it observed.
                        assert!(
                            stdout.contains(&SURVIVOR.to_string())
                                || stdout.contains(&UPDATED.to_string()),
                            "{} {a}: expected stdout {stdout:?} rests on no generator-chosen literal",
                            s.name
                        );
                    }
                    Expect::Reject { .. } => {}
                }
            }
        }
    }

    /// Case ids are unique — a collision would silently drop a case from the
    /// manifest, which is the discovery-by-listing defect class this design
    /// exists to remove.
    #[test]
    fn generated_ids_are_unique() {
        let mut seen = std::collections::BTreeSet::new();
        for s in STRATA {
            for a in full_cross(s) {
                let c = build(s, &a);
                assert!(seen.insert(c.id.clone()), "duplicate case id {}", c.id);
            }
        }
    }

    /// The four families v2 §3.2 forbids from batching must be marked `Unit`.
    #[test]
    fn forbidden_families_are_isolated() {
        for s in STRATA {
            let a = full_cross(s).remove(0);
            let c = build(s, &a);
            if s.isolated {
                assert_eq!(
                    c.isolation,
                    Isolation::Unit,
                    "stratum {} is forbidden from batching but is not marked Unit",
                    s.name
                );
            }
        }
    }
}

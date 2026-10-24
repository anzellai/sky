//! Family L strata for the four v0.27.0 downstream round-3 defects.
//!
//! Each was "type-checks, then fails": three failed `go build` or the checker
//! on a well-typed program, one rejected a well-formed name. Each stratum's
//! pinned coordinate (`axes::pinned_coordinate`) is the downstream
//! reproduction's own axis assignment, so its distance-1 neighbourhood is the
//! set of combinations nobody had tried.
//!
//! Every expected value is chosen HERE, before any compiler runs (class V):
//! the payloads are `21` and `20`/`22`, so a correct program prints `42`
//! (and `42!` where a second type is involved).

use super::axes::{
    Assignment, ACC_POS, ANON_DEPTH, ANON_ROLE, AS_BINDER, EQ_CARRIER, EQ_PAIR, LET_BINDING,
    LET_SITE, LET_USES, LIST_IMPORT, NESTED_PAYLOAD, QUAL_PATH, QUAL_USE, STEP_SITE, TASK_CALLEE,
    TASK_LINK, TASK_RESULT, UNION_REP,
};
use super::gen::{Body, SURVIVOR, UPDATED};

// ---------------------------------------------------------------------------
// as_pattern_nesting
// ---------------------------------------------------------------------------

/// A `case` arm that matches a nested pattern inside a union payload, with an
/// `as` binding inside (`Wrap ((Ok req) as whole)`), outside
/// (`(Wrap (Ok req)) as whole`) or absent. The arm returns `req` plus a value
/// read back through the `as` binding, so the binding is not dead code: a
/// wrongly bound alias reads `0` (or fails to build) and the assertion goes red.
pub fn as_pattern_nesting(a: &Assignment) -> (Body, String) {
    let payload = a.get(NESTED_PAYLOAD);
    let (payload_ty, value, pat, other_arm, via_whole) = match payload {
        "result" => (
            "Result Error Int",
            "Ok 21",
            "Ok req",
            Some("Wrap (Err _) ->\n            0"),
            "Result.withDefault 0 whole",
        ),
        "maybe" => (
            "Maybe Int",
            "Just 21",
            "Just req",
            Some("Wrap Nothing ->\n            0"),
            "Maybe.withDefault 0 whole",
        ),
        "adt" => (
            "Probe",
            "Hit 21",
            "Hit req",
            Some("Wrap Miss ->\n            0"),
            "probeValue whole",
        ),
        "tuple" => ("( Int, Int )", "( 21, 0 )", "( req, _ )", None, "fst whole"),
        other => panic!("as_pattern_nesting: unknown payload {other:?}"),
    };
    let arm = match a.get(AS_BINDER) {
        "plain" => format!("Wrap ({pat}) ->\n            req + 21"),
        "as_inner" => format!("Wrap (({pat}) as whole) ->\n            req + {via_whole}"),
        "as_outer" => format!("(Wrap ({pat})) as whole ->\n            req + outerOf whole"),
        other => panic!("as_pattern_nesting: unknown binder {other:?}"),
    };
    let bag = a.get(UNION_REP) == "bag";
    let (keyed_ctor, keyed_arm, imports) = if bag {
        (
            "\n    | Keyed (Result Error Kx.SecretKey)",
            "\n\n        Keyed _ ->\n            0",
            "import Std.Crypto.Kx as Kx\n".to_string(),
        )
    } else {
        ("", "", String::new())
    };
    let other = other_arm
        .map(|o| format!("\n\n        {o}"))
        .unwrap_or_default();
    let decls = format!(
        "type Probe\n    = Hit Int\n    | Miss\n\n\n\
         type Msg\n    = Start\n    | Wrap ({payload_ty})\n    | Count Int{keyed_ctor}\n\n\n\
         probeValue : Probe -> Int\nprobeValue p =\n    case p of\n        Hit n ->\n            n\n\n        Miss ->\n            0\n\n\n\
         outerOf : Msg -> Int\nouterOf m =\n    case m of\n        Wrap _ ->\n            21\n\n        _ ->\n            0\n\n\n\
         describe : Msg -> Int\ndescribe msg =\n    case msg of\n        {arm}{other}\n\n        \
         Start ->\n            0\n\n        Count n ->\n            n{keyed_arm}\n"
    );
    let check = format!("String.fromInt (describe (Wrap ({value})))");
    (
        Body {
            imports,
            decls,
            check,
        },
        "42".to_string(),
    )
}

// ---------------------------------------------------------------------------
// task_slot
// ---------------------------------------------------------------------------

/// A Task-returning function called inside a `Task.andThen` chain whose next
/// link ignores the result — the slot inference leaves at `Task Error a` while
/// the function's Go type is `Task Error ()` / `Task Error Int`. The chain
/// computes `20 + 22` after the call, so a correct program prints `42`.
pub fn task_slot(a: &Assignment) -> (Body, String) {
    let unit = a.get(TASK_RESULT) == "unit";
    let res_ty = if unit { "()" } else { "Int" };
    let impl_body = if unit {
        "Task.succeed ()"
    } else {
        "Task.succeed n"
    };
    let fallback = if unit {
        "Task.succeed ()"
    } else {
        "Task.succeed 0"
    };
    // The record field's NAME differs by result type: batched cases share one
    // compilation, and two `{ step : … }` records with different field types
    // would make this stratum depend on the fieldset-collision class (which
    // `fieldset_collision` owns) instead of on the Task slot.
    let field = if unit { "step" } else { "stepInt" };
    let callee = a.get(TASK_CALLEE);
    let call = match callee {
        "param" | "lambda_arg" => "f c".to_string(),
        "record_field" => format!("h.{field} c"),
        "let_fn" => "g c".to_string(),
        other => panic!("task_slot: unknown callee {other:?}"),
    };
    let then = "Task.andThen (\\_ -> Task.succeed (c + 22))";
    let link = match a.get(TASK_LINK) {
        "middle" => format!("Task.succeed c |> Task.andThen (\\_ -> {call}) |> {then}"),
        "in_sequence" => format!("Task.sequence [ {call}, {call} ] |> {then}"),
        "on_error" => format!("{call} |> Task.onError (\\_ -> {fallback}) |> {then}"),
        "in_branch" => format!("(if c > 0 then {call} else {fallback}) |> {then}"),
        other => panic!("task_slot: unknown link {other:?}"),
    };
    let chain = format!(
        "Task.succeed 20\n        |> Task.andThen (\\c -> {link})\n        |> Task.map String.fromInt"
    );
    let (decls, arg) = match callee {
        "param" => (
            format!(
                "runWith : (Int -> Task Error {res_ty}) -> Task Error String\nrunWith f =\n    {chain}\n"
            ),
            format!(" (\\n -> {impl_body})"),
        ),
        "record_field" => (
            format!(
                "type alias Hooks =\n    {{ {field} : Int -> Task Error {res_ty} }}\n\n\n\
                 runWith : Hooks -> Task Error String\nrunWith h =\n    {chain}\n"
            ),
            format!(" {{ {field} = \\n -> {impl_body} }}"),
        ),
        "let_fn" => (
            format!(
                "runWith : Int -> Task Error String\nrunWith seed =\n    let\n        \
                 g n =\n            {impl_body}\n    in\n    {chain}\n"
            ),
            " 0".to_string(),
        ),
        "lambda_arg" => (
            format!(
                "runWith : (Int -> Task Error {res_ty}) -> Task Error String\nrunWith impl =\n    \
                 Task.succeed impl\n        |> Task.andThen\n            (\\f ->\n                \
                 {chain_inner}\n            )\n",
                chain_inner = chain.replace("\n        ", "\n                    ")
            ),
            format!(" (\\n -> {impl_body})"),
        ),
        other => panic!("task_slot: unknown callee {other:?}"),
    };
    let check = format!(
        "case Task.run (runWith{arg}) of\n        Ok s ->\n            s\n\n        Err _ ->\n            \"err\""
    );
    (
        Body {
            imports: "import Sky.Core.Task as Task\n".to_string(),
            decls,
            check,
        },
        "42".to_string(),
    )
}

// ---------------------------------------------------------------------------
// let_polymorphism
// ---------------------------------------------------------------------------

/// A let binding used at one type or at two. `one_type` always type-checked
/// and must keep building; `two_types` needs let-generalisation. The closure
/// form captures the outer `base = 22`, which stays monomorphic.
pub fn let_polymorphism(a: &Assignment) -> (Body, String) {
    let binding = a.get(LET_BINDING);
    let two = a.get(LET_USES) == "two_types";
    // The binding, at a 4-space-relative indent `{i}` / `{j}` filled per site.
    let bind = match binding {
        "function" => "pairUp x =\n{j}( x, x )",
        "lambda" => "pairUp =\n{j}\\x -> ( x, x )",
        "closure" => "pairUp x =\n{j}( x, base )",
        "value" => "none =\n{j}[]",
        other => panic!("let_polymorphism: unknown binding {other:?}"),
    };
    let total = "List.foldl (\\x acc -> x + acc) 0";
    let (usage, out) = match (binding, two) {
        ("function" | "lambda", false) => (
            "String.fromInt (fst (pairUp 20) + snd (pairUp 22))".to_string(),
            "42",
        ),
        ("function" | "lambda", true) => (
            "String.fromInt (fst (pairUp 42)) ++ fst (pairUp \"!\")".to_string(),
            "42!",
        ),
        ("closure", false) => (
            "String.fromInt (fst (pairUp 20) + snd (pairUp 0))".to_string(),
            "42",
        ),
        ("closure", true) => (
            "String.fromInt (fst (pairUp 20) + snd (pairUp \"s\")) ++ fst (pairUp \"!\")"
                .to_string(),
            "42!",
        ),
        ("value", false) => (format!("String.fromInt ({total} (20 :: 22 :: none))"), "42"),
        ("value", true) => (
            format!("String.fromInt ({total} (42 :: none)) ++ String.join \"\" (\"!\" :: none)"),
            "42!",
        ),
        other => panic!("let_polymorphism: unknown combination {other:?}"),
    };
    // A `let` block whose binders sit at `ind` spaces.
    let block = |ind: usize| -> String {
        let i = " ".repeat(ind);
        let j = " ".repeat(ind + 4);
        let o = " ".repeat(ind - 4);
        let b = bind.replace("{j}", &j);
        format!("let\n{i}base =\n{j}22\n\n{i}{b}\n{o}in\n{o}{usage}")
    };
    let check = match a.get(LET_SITE) {
        "in_def" => block(8),
        "in_case_branch" => format!(
            "case Just 0 of\n        Just _ ->\n            {}\n\n        Nothing ->\n            \"\"",
            block(16)
        ),
        "in_lambda" => format!(
            "let\n        run =\n            \\_ ->\n                {}\n    in\n    run ()",
            block(20)
        ),
        other => panic!("let_polymorphism: unknown site {other:?}"),
    };
    (
        Body {
            imports: String::new(),
            decls: String::new(),
            check,
        },
        out.to_string(),
    )
}

// ---------------------------------------------------------------------------
// qualified_field
// ---------------------------------------------------------------------------

/// `Module.value.field` on a real stdlib record value (`Std.App.webDefaults`,
/// whose `port` is `-1` and `csrf` is `True` by its documented defaults), under
/// three ways of naming the module. A stdlib module keeps the case batchable.
pub fn qualified_field(a: &Assignment) -> (Body, String) {
    let (imports, q) = match a.get(QUAL_PATH) {
        "alias" => ("import Std.App as App\n", "App"),
        "last_segment" => ("import Std.App\n", "App"),
        "short_alias" => ("import Std.App as A\n", "A"),
        other => panic!("qualified_field: unknown path {other:?}"),
    };
    // `port` is `-1`, so `43 + port` is the generator-chosen `42`; a wrong
    // field (or a zero value) reads anything else.
    let check = match a.get(QUAL_USE) {
        "arg" => format!("String.fromInt (add43 {q}.webDefaults.port)"),
        "operand" => format!("String.fromInt ({q}.webDefaults.port + 43)"),
        "pipeline" => format!("{q}.webDefaults.port |> add43 |> String.fromInt"),
        "in_lambda" => format!("(\\_ -> String.fromInt ({q}.webDefaults.port + 43)) ()"),
        "bool_field" => {
            format!("if {q}.webDefaults.csrf then\n        \"42\"\n\n    else\n        \"0\"")
        }
        other => panic!("qualified_field: unknown use {other:?}"),
    };
    let out = "42";
    (
        Body {
            imports: imports.to_string(),
            decls: "add43 : Int -> Int\nadd43 n =\n    n + 43\n".to_string(),
            check,
        },
        out.to_string(),
    )
}

// ---------------------------------------------------------------------------
// adt_equality (v0.27.7)
// ---------------------------------------------------------------------------

/// `==` and `/=` on two values of `type T = A | B | C Int | D Int`, bare or
/// nested in a record, a list, a `Maybe` or a tuple, with `T` sealed (one Go
/// struct per constructor) or on the `rt.SkyADT` bag (an extra constructor
/// whose payload has no static Go shape). Two values are equal exactly when
/// they have the same constructor and equal fields; the generator knows which
/// pairs those are. Each operator's answer prints as the generator literal 42
/// (True) or 7 (False), so the case prints `42/7` (`==` True, `/=` False) or
/// `7/42`.
pub fn adt_equality(a: &Assignment) -> (Body, String) {
    let (l, r, equal) = match a.get(EQ_PAIR) {
        "nullary_diff" => ("A", "B", false),
        "nullary_same" => ("A", "A", true),
        "fields_same" => ("(C 1)", "(C 1)", true),
        "fields_diff_payload" => ("(C 1)", "(C 2)", false),
        "fields_diff_ctor" => ("(C 1)", "(D 1)", false),
        "nullary_vs_fields" => ("A", "(C 0)", false),
        other => panic!("adt_equality: unknown pair {other:?}"),
    };
    let wrap = |v: &str| -> String {
        match a.get(EQ_CARRIER) {
            "bare" => v.to_string(),
            "in_record" => format!("{{ v = {v}, n = 1 }}"),
            "in_list" => format!("[ {v}, C 9 ]"),
            "in_maybe" => format!("Just {v}"),
            "in_tuple" => format!("( {v}, 1 )"),
            other => panic!("adt_equality: unknown carrier {other:?}"),
        }
    };
    let (lw, rw) = (wrap(l), wrap(r));
    let bag = a.get(UNION_REP) == "bag";
    let (keyed_ctor, imports) = if bag {
        (
            "\n    | Keyed (Result Error Kx.SecretKey)",
            "import Std.Crypto.Kx as Kx\n".to_string(),
        )
    } else {
        ("", String::new())
    };
    let decls = format!(
        "type T\n    = A\n    | B\n    | C Int\n    | D Int{keyed_ctor}\n\n\n\
         answer : Bool -> String\nanswer b =\n    if b then\n        \"{SURVIVOR}\"\n\n    else\n        \"{UPDATED}\"\n"
    );
    let check = format!("answer ({lw} == {rw}) ++ \"/\" ++ answer ({lw} /= {rw})");
    let out = if equal {
        format!("{SURVIVOR}/{UPDATED}")
    } else {
        format!("{UPDATED}/{SURVIVOR}")
    };
    (
        Body {
            imports,
            decls,
            check,
        },
        out,
    )
}

// ---------------------------------------------------------------------------
// lambda_record_param  (v0.27.14)
// ---------------------------------------------------------------------------

/// A two-param fold step where one param is an ANONYMOUS record and the other
/// a NAMED one. `anon_role = acc`: the list holds named `Item`s and the
/// accumulator is anonymous (the reported shape). `anon_role = elem`: the list
/// holds anonymous records and the accumulator is the named `Tally`.
///
/// The defect: a list combinator closure pins the param at the list element to
/// the list's Go element type, and the pin went to the first `struct{…}` param
/// whatever its position. A named element is not a `struct{…}`, so the
/// anonymous accumulator took the pin and was narrowed to `Item`: `sky check`
/// passed and `go build` failed on the accumulator's own fields. Each case
/// prints fields read back from the accumulator the fold built, so a param
/// narrowed to the wrong record cannot print the expected line.
pub fn lambda_record_param(a: &Assignment) -> (Body, String) {
    let anon_acc = match a.get(ANON_ROLE) {
        "acc" => true,
        "elem" => false,
        other => panic!("lambda_record_param: unknown anon_role {other:?}"),
    };
    let nested = match a.get(ANON_DEPTH) {
        "flat" => false,
        "nested" => true,
        other => panic!("lambda_record_param: unknown anon_depth {other:?}"),
    };
    // The answer is built from the two digits of 42, one per element, read
    // back in list order; the second number counts (or sums) the elements.
    let (decl, elem_ty, acc_ty, init, body, items, read, out) = match (anon_acc, nested) {
        (true, false) => (
            "type alias Item =\n    { path : String, label : String }\n",
            "Item",
            "{ seen : List String, n : Int }",
            "{ seen = [], n = 0 }",
            "{ seen = it.label :: acc.seen, n = acc.n + 1 }",
            "[ { path = \"p\", label = \"4\" }, { path = \"q\", label = \"2\" } ]",
            "String.join \"\" (List.reverse r.seen) ++ \"/\" ++ String.fromInt r.n",
            "42/2",
        ),
        (true, true) => (
            "type alias Item =\n    { path : String, label : String }\n",
            "Item",
            "{ inner : { seen : List String, n : Int }, last : String }",
            "{ inner = { seen = [], n = 0 }, last = \"\" }",
            "{ inner = { seen = it.label :: acc.inner.seen, n = acc.inner.n + 1 }, last = it.path }",
            "[ { path = \"p\", label = \"4\" }, { path = \"q\", label = \"2\" } ]",
            "String.join \"\" (List.reverse r.inner.seen) ++ \"/\" ++ String.fromInt r.inner.n ++ r.last",
            "42/2q",
        ),
        (false, false) => (
            "type alias Tally =\n    { seen : List String, n : Int }\n",
            "{ tag : String, w : Int }",
            "Tally",
            "{ seen = [], n = 0 }",
            "{ seen = it.tag :: acc.seen, n = acc.n + it.w }",
            "[ { tag = \"4\", w = 1 }, { tag = \"2\", w = 1 } ]",
            "String.join \"\" (List.reverse r.seen) ++ \"/\" ++ String.fromInt r.n",
            "42/2",
        ),
        (false, true) => (
            "type alias Tally =\n    { seen : List String, n : Int }\n",
            "{ meta : { tag : String }, w : Int }",
            "Tally",
            "{ seen = [], n = 0 }",
            "{ seen = it.meta.tag :: acc.seen, n = acc.n + it.w }",
            "[ { meta = { tag = \"4\" }, w = 1 }, { meta = { tag = \"2\" }, w = 1 } ]",
            "String.join \"\" (List.reverse r.seen) ++ \"/\" ++ String.fromInt r.n",
            "42/2",
        ),
    };
    // `second`: the kernel `List.foldl` (`\it acc -> …`). `first`: a local
    // fold whose step takes the accumulator first (`\acc it -> …`).
    let (fold, lam, fold_decl) = match a.get(ACC_POS) {
        "second" => ("List.foldl", "\\it acc ->", ""),
        "first" => (
            "foldAcc",
            "\\acc it ->",
            "foldAcc : (b -> a -> b) -> b -> List a -> b\nfoldAcc f z xs =\n    case xs of\n        [] ->\n            z\n\n        x :: rest ->\n            foldAcc f (f z x) rest\n\n\n",
        ),
        other => panic!("lambda_record_param: unknown acc_pos {other:?}"),
    };
    let fold_expr = match a.get(STEP_SITE) {
        "inline" => format!("{fold} ({lam} {body}) {init} xs"),
        "let_bound" => format!(
            "let\n        step =\n            {lam} {body}\n    in\n    {fold} step {init} xs"
        ),
        other => panic!("lambda_record_param: unknown step_site {other:?}"),
    };
    let decls = format!(
        "{decl}\n\n{fold_decl}collect : List {elem_ty} -> {acc_ty}\ncollect xs =\n    {fold_expr}\n"
    );
    let imports = match a.get(LIST_IMPORT) {
        "autoloaded" => String::new(),
        "explicit" => "import Sky.Core.List as List\n".to_string(),
        other => panic!("lambda_record_param: unknown list_import {other:?}"),
    };
    let check = format!("let\n        r =\n            collect {items}\n    in\n    {read}");
    (
        Body {
            imports,
            decls,
            check,
        },
        out.to_string(),
    )
}

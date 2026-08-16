//! Regression: a func VALUE flowing into a func SLOT of a different Go shape
//! must be ETA-EXPANDED at emit time, never narrowed with a runtime
//! `rt.Coerce[func(…)…]`.
//!
//! WHY. Go function types are nominal in their parameters and result, so
//! `rt.Coerce[func(any) bool](func(Attr) bool …)` can never satisfy its own
//! `v.(T)` fast path (`runtime-go/rt/rt.go`, `Coerce`). It falls through to
//! `makeFuncAdapter` → `adaptFuncValueWithCapture`, a `reflect.MakeFunc` thunk
//! that on EVERY invocation allocates a `[]reflect.Value`, re-boxes each
//! argument through `reflect.ValueOf(a.Interface())`, concatenates the captured
//! args, and `reflect.Value.Call`s. The wrapper is built once per enclosing
//! call; the reflect dispatch is paid once per ELEMENT VISIT.
//!
//! `Std.Ui`'s marker scan is the canonical victim — `Std.Ui.hasMarker` is
//! `List.any (\a -> isMarker name a) attrs`, and a layout probes six markers
//! per element:
//!
//! ```go
//! func Std_Ui_hasMarker(v_0 string, v_1 []Std_Ui_Attribute) bool {
//!     return Sky_Core_List_any_(rt.Coerce[func(any) bool](
//!         func(v_2 Std_Ui_Attribute) bool { return Std_Ui_isMarker(v_0, v_2) }),
//!         rt.AsListT[any](v_1))
//! }
//! ```
//!
//! This is doc-08 §5.3 category 6 ("polymorphic kernel-fn arg"), a
//! LOWERING-closeable site: both the value's shape and the slot's shape are
//! fully known at `coerce_if_needed`. It is NOT §8.3's TEA `reflect.MakeFunc`
//! dispatch, where the callee's shape is only known at runtime — that one is
//! genuinely irreducible and is deliberately left alone.
//!
//! ## The legs, and what each one cannot see
//!
//! 1. **Allocation count** (`hof_callback_costs_no_reflect_allocation_per_element`)
//!    — the load-bearing assertion, because it measures the DEFECT (a per-visit
//!    reflect allocation) rather than a spelling of the fix. `AllocsPerRun` is a
//!    counter, not a clock, so it does not flake with machine load the way a
//!    wall-clock budget would. It cannot see a change that keeps the allocation
//!    count and costs time some other way.
//! 2. **Emitted shape** (`hof_callback_is_eta_expanded_not_runtime_coerced`) —
//!    runs with no Go toolchain, and localises a failure to the emitter. It
//!    cannot see whether the eta-expansion narrows to the RIGHT type: the shape
//!    can be perfect and the value wrong.
//! 3. **Semantics** (`eta_expanded_callback_computes_the_same_answer`) — that
//!    the program still computes what it computed. On ONE program: the full
//!    `scripts/example-sweep.sh` is what covers the shapes this fixture lacks.
//! 4. **Arity guard rail** (`curried_partial_application_still_runs_and_is_correct`)
//!    — that the fix does NOT steal the uncurried→curried case from the runtime
//!    adapter, which is the one job `adaptFuncValueWithCapture` must keep.
//!
//! None of the four can see a regression in a shape this fixture does not
//! contain; that is what the example sweep and the `apps/` gates are for.

use std::path::{Path, PathBuf};
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

fn scratch(tag: &str) -> PathBuf {
    let uniq = format!(
        "sky-hofshape-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    );
    let dir = std::env::temp_dir().join(uniq);
    std::fs::create_dir_all(dir.join("src")).unwrap();
    dir
}

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

fn project(tag: &str, main_src: &str) -> PathBuf {
    let dir = scratch(tag);
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"hofshape\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
    )
    .unwrap();
    std::fs::write(dir.join("src").join("Main.sky"), main_src).unwrap();
    dir
}

fn build(dir: &Path) -> String {
    let out = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky build");
    let mut s = String::from_utf8_lossy(&out.stdout).into_owned();
    s.push_str(&String::from_utf8_lossy(&out.stderr));
    s
}

fn emitted_go(dir: &Path, log: &str) -> String {
    let main_go = dir.join("sky-out").join("main.go");
    assert!(
        main_go.is_file(),
        "sky build must emit sky-out/main.go (build log: {log})"
    );
    std::fs::read_to_string(&main_go).unwrap()
}

/// The Go body of `func <name>(` in `src`, up to the closing brace at column 0.
fn func_body<'a>(src: &'a str, name: &str) -> &'a str {
    let needle = format!("func {name}(");
    let at = src
        .find(&needle)
        .unwrap_or_else(|| panic!("emitted Go must define {name}:\n{src}"));
    let rest = &src[at..];
    let end = rest.find("\n}").map(|i| i + 2).unwrap_or(rest.len());
    &rest[..end]
}

/// Both callback shapes that reach `rt.Coerce[func…]` today:
///
///   * `hasMarker` — a LAMBDA closing over an outer param. This is
///     `Std.Ui.hasMarker` verbatim (`List.any (\a -> isMarker name a) attrs`).
///   * `anyWide` — a bare top-level FUNCTION passed point-free, so the func
///     value is an `Ident`, not a literal.
///
/// Both lower to a `func(Main_Attr) bool` landing in `List.any`'s erased
/// `func(any) bool` slot. `probe` is the six-marker scan a `Std.Ui` layout
/// performs per element.
const MARKER_SCAN: &str = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.List as List
import Sky.Core.String as String
import Std.Log exposing (println)


type Attr
    = Marker String
    | Width Int


isMarker : String -> Attr -> Bool
isMarker name a =
    case a of
        Marker m ->
            m == name

        Width _ ->
            False


hasMarker : String -> List Attr -> Bool
hasMarker name attrs =
    List.any (\a -> isMarker name a) attrs


isWide : Attr -> Bool
isWide a =
    case a of
        Width n ->
            n > 100

        Marker _ ->
            False


anyWide : List Attr -> Bool
anyWide attrs =
    List.any isWide attrs


sample : Int -> List Attr
sample n =
    [ Marker "grid", Width n, Marker "row", Width 200, Marker "col", Width 3 ]


score : Bool -> Int
score b =
    if b then
        1

    else
        0


probe : List Attr -> Int
probe attrs =
    score (hasMarker "grid" attrs)
        + score (hasMarker "row" attrs)
        + score (hasMarker "col" attrs)
        + score (hasMarker "absent-a" attrs)
        + score (hasMarker "absent-b" attrs)
        + score (anyWide attrs)


main =
    println (String.fromInt (probe (sample 7)))
"#;

/// `probe (sample 7)` — "grid", "row", "col" present (3), two absent (0),
/// `anyWide` true because `Width 200 > 100` (1).
const MARKER_SCAN_ANSWER: &str = "4";

/// A Go test dropped INTO the emitted module, so the allocation count is
/// measured on the real emitted code rather than on a hand-written imitation of
/// it. `sky-out` is a Go module (`module sky-app`) whose `main.go` is
/// `package main`, so a sibling `_test.go` compiles against the emitted
/// definitions directly.
///
/// `sample` is called OUTSIDE the measured closure: building the list is not
/// what is under test. The measured closure is one six-marker scan over six
/// attributes — 36 element visits — which is what a `Std.Ui` layout pays per
/// element.
const ALLOC_PROBE: &str = r#"package main

import "testing"

func TestHofDispatchAllocsPerScan(t *testing.T) {
	attrs := Main_sample(7)
	if got := Main_probe(attrs); got != 4 {
		t.Fatalf("fixture disagrees with the Sky program: probe = %v, want 4", got)
	}
	n := testing.AllocsPerRun(200, func() {
		Main_probe(attrs)
	})
	t.Logf("SKY_HOF_ALLOCS_PER_SCAN=%.0f", n)
}
"#;

/// Run the allocation probe against a freshly built project and return the
/// measured allocations per six-marker scan.
fn allocs_per_scan(tag: &str) -> f64 {
    let dir = project(tag, MARKER_SCAN);
    let log = build(&dir);
    assert!(
        !log.contains("go build failed"),
        "the fixture must build before its allocations mean anything. Log:\n{log}"
    );
    let out_dir = dir.join("sky-out");
    std::fs::write(out_dir.join("alloc_probe_test.go"), ALLOC_PROBE).unwrap();
    let out = Command::new("go")
        .args([
            "test",
            "-run",
            "TestHofDispatchAllocsPerScan",
            "-v",
            "-count=1",
            "-timeout",
            "300s",
            ".",
        ])
        .current_dir(&out_dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn go test");
    let mut text = String::from_utf8_lossy(&out.stdout).into_owned();
    text.push_str(&String::from_utf8_lossy(&out.stderr));
    assert!(
        out.status.success(),
        "the allocation probe must run. go test output:\n{text}"
    );
    let n = text
        .lines()
        .find_map(|l| l.split("SKY_HOF_ALLOCS_PER_SCAN=").nth(1))
        .unwrap_or_else(|| panic!("probe must report its count. go test output:\n{text}"))
        .trim()
        .parse::<f64>()
        .unwrap();
    let _ = std::fs::remove_dir_all(&dir);
    n
}

/// ALLOCATION LEG — the defect measured, not a spelling of the fix asserted.
///
/// With the `reflect.MakeFunc` adapter in place each of the 36 element visits
/// allocates a `[]reflect.Value` plus a re-boxed argument; without it the scan
/// pays only the list erasure (`rt.AsListT[any]`, once per `List.any` call, plus
/// the runtime's `any`-taking `SkyLen`/`SkyElem`). Measured: 318 → 126.
///
/// What this leg does NOT catch: a change that preserves the allocation count
/// while costing time elsewhere, and any shape this one fixture omits.
#[test]
fn hof_callback_costs_no_reflect_allocation_per_element() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let n = allocs_per_scan("marker-alloc");
    assert!(
        n <= ALLOC_BUDGET_PER_SCAN,
        "a six-marker scan over six attributes allocated {n} times, budget \
         {ALLOC_BUDGET_PER_SCAN}. That is the `reflect.MakeFunc` adapter back: \
         `rt.Coerce[func(any) bool]` cannot hit its `v.(T)` fast path because Go \
         func types are nominal in their params, so every element visit pays a \
         `[]reflect.Value` allocation. Eta-expand the callback at the slot's \
         shape in `lower.rs` `coerce_if_needed` instead."
    );
}

/// Measured on this fixture, M1, Go 1.25, and stable to the unit across repeat
/// runs: **318** allocations per scan with the `reflect.MakeFunc` adapter, **126**
/// without it.
///
/// 126 is not zero, and the gap is not a rounding error — it is the OTHER
/// erasure on this path, which the eta-expansion deliberately does not touch:
/// `rt.AsListT[any]` rebuilds the six-element list on each of the six
/// `List.any` calls, and the runtime's `SkyLen`/`SkyElem` helpers take `x any`,
/// so the slice header is re-boxed per access. Assert what was actually fixed.
///
/// The budget sits between the two measurements with ~1.6× clearance on each
/// side: comfortably above the honest cost so a runtime tweak to the list
/// helpers cannot flake it, and comfortably below the adapter's cost so the
/// regression it exists to catch cannot slip under it.
const ALLOC_BUDGET_PER_SCAN: f64 = 200.0;

/// EMISSION LEG — the defect verbatim, checkable with no Go toolchain. A func
/// value whose Go shape differs from its slot's ONLY in the params/result (same
/// arity) must be bridged by an emitted closure, not by `rt.Coerce[func…]`.
///
/// What this leg does NOT catch: an eta-expansion that narrows to the WRONG
/// type — the shape is right and the value is wrong. That is the semantics leg
/// and `scripts/example-sweep.sh`. Nor can it see the other route into
/// `reflect.Value.Call`: `rt.SkyCall` inside the runtime's own list loops,
/// which no assertion on emitted Go can reach.
#[test]
fn hof_callback_is_eta_expanded_not_runtime_coerced() {
    let dir = project("marker-emit", MARKER_SCAN);
    let log = build(&dir);
    let src = emitted_go(&dir, &log);

    for def in ["Main_hasMarker", "Main_anyWide"] {
        let body = func_body(&src, def);
        assert!(
            !body.contains("rt.Coerce[func("),
            "{def} must not narrow a func VALUE with a runtime coerce. Go func types \
             are nominal in their params, so `rt.Coerce[func(any) bool]` can never hit \
             its `v.(T)` fast path — it falls to `makeFuncAdapter`'s reflect.MakeFunc \
             thunk, paid once per ELEMENT VISIT. Eta-expand at the slot's shape \
             instead. Emitted:\n{body}"
        );
        assert!(
            body.contains("func(_e"),
            "{def} must bridge the callback through an eta-expanded closure whose \
             params carry the SLOT's types (`_e<n>`) and narrow inward. Emitted:\n{body}"
        );
    }
    let _ = std::fs::remove_dir_all(&dir);
}

/// SEMANTICS LEG — eta-expansion is a SHAPE change and must never be a
/// semantics change.
#[test]
fn eta_expanded_callback_computes_the_same_answer() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = project("marker-run", MARKER_SCAN);
    let log = build(&dir);
    assert!(
        !log.contains("go build failed"),
        "`sky check` ≡ `sky build`: the eta-expanded callback must compile. Log:\n{log}"
    );
    let app = dir.join("sky-out").join("app");
    assert!(app.is_file(), "build must produce sky-out/app. Log:\n{log}");
    let out = Command::new(&app)
        .current_dir(&dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("run sky-out/app");
    let stdout = String::from_utf8_lossy(&out.stdout).into_owned();
    assert_eq!(
        stdout.trim(),
        MARKER_SCAN_ANSWER,
        "eta-expansion is a SHAPE change, never a semantics change. stdout:\n{stdout}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

/// The guard rail in the other direction. An ARITY MISMATCH between the func
/// value and its slot is exactly what `adaptFuncValueWithCapture`'s
/// uncurried→curried branch exists for (`rt.go`, "Uncurried-to-curried
/// adaptation"): Sky curries, Go does not. Eta-expanding to the wrong arity
/// would emit a call `go build` rejects, so the runtime coerce must SURVIVE
/// there — the fix must not be greedy.
///
/// `List.foldl` takes a 2-ary reducer; `scaled k` is a partially applied 3-ary
/// function, so the runtime finishes the currying.
const CURRIED: &str = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.List as List
import Sky.Core.String as String
import Std.Log exposing (println)


scaled : Int -> Int -> Int -> Int
scaled k x acc =
    acc + (k * x)


total : Int -> List Int -> Int
total k xs =
    List.foldl (scaled k) 0 xs


main =
    println (String.fromInt (total 3 [ 1, 2, 3 ]))
"#;

#[test]
fn curried_partial_application_still_runs_and_is_correct() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = project("curried-run", CURRIED);
    let log = build(&dir);
    assert!(
        !log.contains("go build failed"),
        "a partially-applied reducer must still compile. Log:\n{log}"
    );
    let app = dir.join("sky-out").join("app");
    assert!(app.is_file(), "build must produce sky-out/app. Log:\n{log}");
    let out = Command::new(&app)
        .current_dir(&dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("run sky-out/app");
    let stdout = String::from_utf8_lossy(&out.stdout).into_owned();
    assert_eq!(
        stdout.trim(),
        "18",
        "3*(1+2+3) = 18 — eta-expansion must not steal the arity-mismatch case \
         from the runtime adapter. stdout:\n{stdout}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

// ───────────────────────────────────────────────────────────────────────────
// Stage 2 — the OTHER half of the same erasure, and the half the eta-expansion
// above deliberately left behind.
//
// Eta-expansion removed the `reflect.MakeFunc` ADAPTER wrapped around the
// callback. It did not remove the `rt.SkyCall` the erased runtime helper makes
// per element, nor the two container rebuilds around it. On a `[]Row` of n,
// `rt.AsListT[T](rt.List_mapAny(any(fn), any(xs)))` costs: one box for the
// slice header, `asList` reflect-walking and boxing every element, a
// `reflect.Value.Call` per element, a `[]any` result, and `AsListT` walking
// that back — ~7n+2 allocations where a Go `for` over the typed slice costs 1.
// `indexedMap` is worse again: it applies the index first, so each element also
// builds a curried closure.
//
// When the call site can PROVE the element type and the callback's Go shape,
// `Ctx::list_hof_typed` dispatches to the typed member of the helper family
// (`rt.List_mapT[A, B](fn func(A) B, xs []A) []B`) instead. One emit per
// definition, no monomorphisation: the specialisation is Go's own generic
// instantiation of one runtime function, not a copy of the loop per call site.
//
// The legs mirror the four above, plus one this change needs and the
// eta-expansion did not: a FALLBACK leg. The safety property is "a site the
// compiler cannot prove emits exactly what it emits today", and a gate that
// only checks the fast path fires cannot see the fast path firing where it
// must not.

/// Five call sites over a NOMINAL record list, one per callback bucket the
/// corpus census found:
///
///   * `bumped`  — a partially applied top-level def (`heavier 1`). 23% of
///     sites. `make_partial` hard-codes its remaining params to `any`, so this
///     is the bucket that needs `func_shape_eta` to retype the literal.
///   * `kept`    — a bare top-level def passed point-free. 53% of sites.
///   * `ids`     — a bare def returning `Maybe`, through `List.filterMap`.
///   * `idx`     — a bare def through `List.indexedMap`, the 2-ary shape.
///   * `lambdaed`— a lambda with an inline body. 14% of sites.
///
/// `Row` is a nominal record alias — `State_Comment_R` in the app this was
/// measured on, and precisely the shape issue #166 broke twice while every
/// corpus gate stayed green. A row-polymorphic record would erase to `any`
/// (`goty.rs`, OPEN row → `GoTy::Any`) and belongs in `LIST_HOF_ERASED` below.
const LIST_HOF: &str = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.List as List
import Sky.Core.String as String
import Std.Log exposing (println)


type alias Row =
    { id : Int
    , weight : Int
    , tag : String
    }


mkRow : Int -> Row
mkRow i =
    { id = i, weight = i * 2, tag = "r" }


sample : Int -> List Row
sample n =
    List.map mkRow (List.range 1 n)


heavier : Int -> Row -> Row
heavier k r =
    { r | weight = r.weight + k }


isBig : Row -> Bool
isBig r =
    r.weight > 6


bigId : Row -> Maybe Int
bigId r =
    if r.weight > 6 then
        Just r.id

    else
        Nothing


tagged : Int -> Row -> Int
tagged i r =
    i + r.weight


bumped : List Row -> List Row
bumped rows =
    List.map (heavier 1) rows


kept : List Row -> List Row
kept rows =
    List.filter isBig rows


ids : List Row -> List Int
ids rows =
    List.filterMap bigId rows


idx : List Row -> List Int
idx rows =
    List.indexedMap tagged rows


lambdaed : List Row -> List Int
lambdaed rows =
    List.map (\r -> r.weight + 1) rows


sum : List Int -> Int
sum xs =
    List.foldl (\x acc -> acc + x) 0 xs


answer : Int
answer =
    let
        rows =
            bumped (sample 8)
    in
    List.length (kept rows)
        + sum (ids rows)
        + sum (idx rows)
        + sum (lambdaed rows)


main =
    println (String.fromInt answer)
"#;

/// `sample 8` gives ids 1…8 with weights 2,4,…,16; `bumped` adds 1, so weights
/// 3,5,…,17.
///
///   * `kept` keeps weight > 6 — the last six rows. **length 6**
///   * `ids` yields those six rows' ids, 3…8. **sum 33**
///   * `idx` is `i + weight` over all eight, `i` 0-based: 3,6,9,12,15,18,21,24.
///     **sum 108**
///   * `lambdaed` is `weight + 1`: 4,6,…,18. **sum 88**
///
/// 6 + 33 + 108 + 88 = 235. Three of the four terms are SUMS rather than
/// lengths on purpose: a length is blind to every value-level mutation of a
/// typed helper — an off-by-one index in `List_indexedMapT`, the wrong element
/// handed to the callback, a `filterMap` that keeps the `Nothing`s — and this
/// leg exists to catch exactly those.
const LIST_HOF_ANSWER: &str = "235";

/// Two sites the specialisation must REFUSE, because the element type is a
/// type variable and lowers to `any`. Emitting `rt.List_filterT[any]` here
/// would be a lie about what is proven, and the erased helper's `asList` is
/// also the only thing that copes with a caller handing in a differently-typed
/// slice. These must keep the erased call, unchanged.
const LIST_HOF_ERASED: &str = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.List as List
import Sky.Core.String as String
import Std.Log exposing (println)


yes : a -> Bool
yes _ =
    True


same : a -> a
same x =
    x


keepAll : List a -> List a
keepAll xs =
    List.filter yes xs


echo : List a -> List a
echo xs =
    List.map same xs


main =
    println (String.fromInt (List.length (keepAll (echo [ 1, 2, 3 ]))))
"#;

/// The five traversals, measured together, over one list built OUTSIDE the
/// closure. 5 × 8 = 40 element visits — the same unit as the marker scan above.
const LIST_HOF_PROBE: &str = r#"package main

import "testing"

func TestListHofAllocsPerSweep(t *testing.T) {
	rows := Main_bumped(Main_sample(8))
	if len(rows) != 8 {
		t.Fatalf("fixture: bumped(sample(8)) has %d rows, want 8", len(rows))
	}
	// Anti-vacuity: a sweep over an empty list allocates almost nothing and
	// would pass any budget. Each traversal must have produced its elements.
	if n := len(Main_kept(rows)); n != 6 {
		t.Fatalf("fixture: kept has %d, want 6", n)
	}
	if n := len(Main_ids(rows)); n != 6 {
		t.Fatalf("fixture: ids has %d, want 6", n)
	}
	if n := len(Main_idx(rows)); n != 8 {
		t.Fatalf("fixture: idx has %d, want 8", n)
	}
	if n := len(Main_lambdaed(rows)); n != 8 {
		t.Fatalf("fixture: lambdaed has %d, want 8", n)
	}
	n := testing.AllocsPerRun(200, func() {
		_ = Main_bumped(rows)
		_ = Main_kept(rows)
		_ = Main_ids(rows)
		_ = Main_idx(rows)
		_ = Main_lambdaed(rows)
	})
	t.Logf("SKY_LIST_HOF_ALLOCS_PER_SWEEP=%.0f", n)
}
"#;

/// Build the list-HOF fixture and return allocations per five-traversal sweep.
///
/// Deliberately a near-copy of `allocs_per_scan` rather than a generalisation
/// of it: the two differ in the fixture, the probe, the marker string and the
/// assertions, and folding them together would leave a function whose every
/// line is a parameter.
fn allocs_per_sweep(tag: &str) -> f64 {
    let dir = project(tag, LIST_HOF);
    let log = build(&dir);
    assert!(
        !log.contains("go build failed"),
        "the fixture must build before its allocations mean anything. Log:\n{log}"
    );
    let out_dir = dir.join("sky-out");
    std::fs::write(out_dir.join("list_hof_probe_test.go"), LIST_HOF_PROBE).unwrap();
    let out = Command::new("go")
        .args([
            "test",
            "-run",
            "TestListHofAllocsPerSweep",
            "-v",
            "-count=1",
            "-timeout",
            "300s",
            ".",
        ])
        .current_dir(&out_dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn go test");
    let mut text = String::from_utf8_lossy(&out.stdout).into_owned();
    text.push_str(&String::from_utf8_lossy(&out.stderr));
    assert!(
        out.status.success(),
        "the allocation probe must run. go test output:\n{text}"
    );
    let n = text
        .lines()
        .find_map(|l| l.split("SKY_LIST_HOF_ALLOCS_PER_SWEEP=").nth(1))
        .unwrap_or_else(|| panic!("probe must report its count. go test output:\n{text}"))
        .trim()
        .parse::<f64>()
        .unwrap();
    let _ = std::fs::remove_dir_all(&dir);
    n
}

/// Measured on this fixture, M1, Go 1.26, stable to the unit across repeat
/// runs: **1030** allocations per sweep through the erased helpers, **51**
/// through the typed ones. 40 element visits, so ~24.5 allocations per visit
/// removed.
///
/// 51 is not zero and is not meant to be. What remains is (a) one result slice
/// per traversal, which a `for` loop cannot avoid, and (b) the redundant
/// `rt.Coerce[Row](_p0)` still sitting in the retyped callback body: the eta
/// retype gives the param the element's type but does not rewrite the body's
/// uses of it, so a struct element is re-boxed on the way into `Coerce`, whose
/// `v.(T)` then succeeds immediately. That is a separate peephole and is not
/// what this budget locks.
///
/// The budget sits between the two with ~2× clearance below and ~5× above: high
/// enough that a runtime tweak to the helpers cannot flake it, low enough that
/// the erased round trip cannot slip under it.
const ALLOC_BUDGET_PER_SWEEP: f64 = 110.0;

/// ALLOCATION LEG — as above, the defect rather than a spelling of the fix.
///
/// What it does NOT catch: a site shape absent from this fixture (that is the
/// example sweep), and the possibility that the typed helper computes something
/// else — the semantics leg and the fallback leg cover that.
#[test]
fn list_hof_over_a_known_element_type_does_not_allocate_per_element() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let n = allocs_per_sweep("listhof-alloc");
    assert!(
        n <= ALLOC_BUDGET_PER_SWEEP,
        "five list traversals over eight records allocated {n} times, budget \
         {ALLOC_BUDGET_PER_SWEEP}. That is the erased round trip back: \
         `rt.List_mapAny(any(fn), any(xs))` boxes the slice header, reflect-walks \
         every element into a fresh `[]any`, `reflect.Value.Call`s the callback \
         per element, and `rt.AsListT` walks the result back. A call site that \
         knows the element type and the callback's Go shape must dispatch to the \
         typed helper instead — `Ctx::list_hof_typed` in lower.rs."
    );
}

/// EMISSION LEG — the routing decision, checkable with no Go toolchain, and
/// localising a failure to the emitter. One assertion per callback bucket, so a
/// regression names which bucket stopped resolving rather than just "fewer
/// typed calls than expected".
#[test]
fn provable_list_hof_sites_route_to_the_typed_helper() {
    let dir = project("listhof-emit", LIST_HOF);
    let log = build(&dir);
    let src = emitted_go(&dir, &log);

    // (def, typed symbol, erased symbol, which census bucket it stands for)
    let cases = [
        ("Main_bumped", "rt.List_mapT[", "rt.List_mapAny(", "partially applied def"),
        ("Main_kept", "rt.List_filterT[", "rt.List_filterAny(", "bare top-level def"),
        ("Main_ids", "rt.List_filterMapT[", "rt.List_filterMap(", "bare def returning Maybe"),
        ("Main_idx", "rt.List_indexedMapT[", "rt.List_indexedMap(", "2-ary bare def"),
        ("Main_lambdaed", "rt.List_mapT[", "rt.List_mapAny(", "inline lambda"),
    ];
    for (def, typed, erased, bucket) in cases {
        let body = func_body(&src, def);
        assert!(
            body.contains(typed),
            "{def} ({bucket}) has a proven element type and callback shape, so it \
             must dispatch to {typed}…]. Emitted:\n{body}"
        );
        assert!(
            !body.contains(erased),
            "{def} ({bucket}) must not keep the erased `{erased}…)` alongside the \
             typed dispatch. Emitted:\n{body}"
        );
    }
    // The typed helper returns the element type, so the narrowing that used to
    // wrap every one of these call sites is gone with it.
    for def in ["Main_bumped", "Main_kept", "Main_ids", "Main_idx", "Main_lambdaed"] {
        let body = func_body(&src, def);
        assert!(
            !body.contains("rt.AsListT["),
            "{def} must not rebuild its result list: the typed helper already \
             returns `[]T`. Emitted:\n{body}"
        );
    }
    let _ = std::fs::remove_dir_all(&dir);
}

/// FALLBACK LEG — the safety property, asserted in the direction that can
/// actually fail. The specialisation's contract is "a site whose element type
/// or callback shape is not statically proven emits exactly what it emits
/// today", and the way to break that is not to emit too few typed calls but too
/// many: `rt.List_filterT[any]` over a `[]any` type-checks, runs, and quietly
/// claims a proof that was never made — and it loses `asList`, which is the
/// only thing that copes with a caller passing a differently-typed slice.
///
/// What this leg does NOT catch: byte-identity of the fallback. It proves the
/// erased symbol survives, not that every other byte around it is unchanged.
/// `xtask repro` and `xtask coerce-floor` cover the rest.
#[test]
fn unprovable_element_type_keeps_the_erased_helper() {
    let dir = project("listhof-erased", LIST_HOF_ERASED);
    let log = build(&dir);
    let src = emitted_go(&dir, &log);

    for (def, erased, typed) in [
        ("Main_keepAll", "rt.List_filterAny(", "rt.List_filterT["),
        ("Main_echo", "rt.List_mapAny(", "rt.List_mapT["),
    ] {
        let body = func_body(&src, def);
        assert!(
            body.contains(erased),
            "{def}'s element type is a TYPE VARIABLE, which lowers to `any`. \
             Nothing is proven here, so the erased `{erased}…)` must survive \
             unchanged. Emitted:\n{body}"
        );
        assert!(
            !body.contains(typed),
            "{def} must NOT be specialised: `{typed}any]` type-checks and runs \
             while claiming a proof that was never made, and drops the `asList` \
             widen that copes with a differently-typed slice. \
             `provable()` in lower.rs rejects `GoTy::Any`. Emitted:\n{body}"
        );
    }
    let _ = std::fs::remove_dir_all(&dir);
}

/// SEMANTICS LEG — a dispatch change must never be a semantics change, and the
/// empty case is the sharp edge. `List_filterMap` returns a NIL slice when
/// nothing matches, where the other three return a non-nil empty one; nil vs
/// empty is observable through `reflect.DeepEqual` and through JSON (`null` vs
/// `[]`). `List_filterMapT` matches what the erased helper produced AT A CALL
/// SITE — non-nil, because `rt.AsListT[T]` normalised the nil away — not what
/// the erased helper returned in isolation. See its comment in `rt.go`.
#[test]
fn typed_list_hof_computes_the_same_answer() {
    if !required(Need::Go, have_go()) {
        return;
    }
    for (tag, src, want) in [
        ("listhof-run", LIST_HOF, LIST_HOF_ANSWER),
        ("listhof-erased-run", LIST_HOF_ERASED, "3"),
    ] {
        let dir = project(tag, src);
        let log = build(&dir);
        assert!(
            !log.contains("go build failed"),
            "`sky check` ≡ `sky build`: the typed dispatch must compile. Log:\n{log}"
        );
        let app = dir.join("sky-out").join("app");
        assert!(app.is_file(), "build must produce sky-out/app. Log:\n{log}");
        let out = Command::new(&app)
            .current_dir(&dir)
            .stdin(std::process::Stdio::null())
            .output()
            .expect("run sky-out/app");
        let stdout = String::from_utf8_lossy(&out.stdout).into_owned();
        assert_eq!(
            stdout.trim(),
            want,
            "{tag}: dispatching to the typed list helper is a SHAPE change, never \
             a semantics change. stdout:\n{stdout}"
        );
        let _ = std::fs::remove_dir_all(&dir);
    }
}

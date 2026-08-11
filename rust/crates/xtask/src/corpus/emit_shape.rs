//! **Family E** — emit-shape assertions on the generated Go, with **no
//! `go build`** (v2 §3.1).
//!
//! > Family **E** is the highest-leverage idea either document had […]: #166,
//! > #171, #173 and the `goty.rs` fieldset collision are all "compiles clean,
//! > behaves wrong" — invisible to `build-run` and to the differential oracle,
//! > but **visible in the emitted Go**.
//!
//! The whole economic point is the missing `go build`. A behavioural case costs
//! `c_u` = 0.70 s (measured warm). An emit-shape case calls
//! `project::emit_example_source` — the same emit path `xtask repro` and
//! `xtask coerce-floor` use — and stops with the Go text in hand. That is
//! milliseconds, which is what lets shape assertions run at T1 density where a
//! built-and-run case cannot.
//!
//! # Properties, not fingerprints
//!
//! An "expected" blob that is whatever the compiler emitted is a
//! **change-detector**: it goes red on any edit and it was green on the day the
//! bug shipped, because on that day the compiler's answer WAS the expected one.
//! v2 §5.5 excludes such cases from the coverage numerator, and rightly.
//!
//! A **property** is different in kind. "This function contains zero runtime
//! narrowing calls" is a statement the generator makes *before* the compiler
//! runs, derived from the Sky source it just wrote — every type in that function
//! is concrete, so nothing in it needs narrowing. A compiler that emitted the
//! opposite answer fails it. That independence is exactly what [`Class::V`]
//! names, so property cases are classified V and fingerprint cases would be D.
//!
//! **We ship no D cases here**, because we ship no fingerprints. §4.4's class-V
//! enumeration is written for *value* assertions and does not name emit-shape;
//! the reading applied here, stated so it can be argued with rather than
//! discovered later, is that the class turns on whether the expectation was
//! constructed independently of the compiler's output — and a stated property
//! is, while a blob is not.
//!
//! # A property that cannot be violated is not a property
//!
//! Dead-code elimination is the trap. `sky build` emits only what `main`
//! reaches, so a case whose probe function is unreferenced emits **no function
//! at all** — and "this function contains zero narrowing calls" is then
//! vacuously true. That is the same shape as the 23 unconditionally-passing
//! gates this branch exists to remove.
//!
//! So every property is paired with a **presence probe**, and the presence probe
//! is checked FIRST. If the function under test is not in the emitted Go the
//! case FAILS — it does not pass, and it does not skip.

use super::axes::{Assignment, Axis, Stratum};
use super::gen::{Blocked, Class, Expect, Family, GenCase, Isolation, Mode, Witness};
use std::path::Path;

// ---------------------------------------------------------------------------
// The property catalogue
// ---------------------------------------------------------------------------

/// A property of the emitted Go, stated independently of what the compiler
/// produced.
pub struct Property {
    pub id: &'static str,
    pub what: &'static str,
}

/// Every property Family E can assert. Each maps to an invariant this repository
/// already enforces somewhere else, so E is a cheaper, denser face of a rule
/// that already exists rather than a new opinion.
pub const PROPERTIES: &[Property] = &[
    Property {
        id: "no-erasure",
        what: "the emitted function contains no type-variable erasure or reflect \
               dispatch (`rt.Coerce`, `rt.Field`, `rt.SkyCall`) — every type in it \
               is concrete, so none is reachable",
    },
    Property {
        id: "no-narrowing",
        what: "the emitted function contains NO runtime narrowing token at all \
               (the full `coerce-floor` tracked set) — the v0.17 typed-emit contract",
    },
    Property {
        id: "no-any-in-signature",
        what: "the emitted function's parameters and result are concrete Go types; \
               a fully-concrete Sky signature must not erase to `any`",
    },
    Property {
        id: "no-raw-type-assert",
        what: "the emitted function performs no raw `.(T)` type assertion — every \
               narrowing routes through an `rt.*` helper that recovers to a \
               classified panic (AGENTS.md: no raw `.(T)` on any-typed thunks)",
    },
    Property {
        id: "field-order-declared",
        what: "the emitted struct lists the record's fields in DECLARED order \
               (`_fieldIndex`), not alphabetically or by hash order",
    },
    Property {
        id: "fieldset-by-type",
        what: "two record aliases with identical field NAMES and different field \
               TYPES emit two DISTINCT Go structs, and the Int-valued one really \
               carries an `int` — the `goty.rs` collision that compiled clean and \
               panicked",
    },
];

pub fn property(id: &str) -> &'static Property {
    PROPERTIES
        .iter()
        .find(|p| p.id == id)
        .unwrap_or_else(|| panic!("emit_shape: unknown property {id:?}"))
}

// ---------------------------------------------------------------------------
// Axes
// ---------------------------------------------------------------------------

/// The Sky shape the probe function is written in. Each value is a different way
/// to reach the SAME concrete result type, so the properties must hold for all
/// of them — and where one shape breaks a property and its neighbours do not,
/// that difference is the finding.
pub const ESHAPE: Axis = Axis::new(
    "eshape",
    &[
        // `bump r = { r | a = 7 }` — record update, the #166 shape.
        "record_update",
        // `pick r = r.gamma` — a bare field read returned directly.
        "field_read",
        // `pick r = r.gamma + 0` — the same read pinned by an arithmetic context.
        "field_read_in_binop",
        // `pick (a, b) = a.gamma` — read through a tuple projection.
        "tuple_projection",
        // `wrap r = { inner = r }` — a record nested in a record.
        "nested_record",
    ],
);

/// How the probe's parameter type is written. `alias` is a named record alias;
/// `inline` is the same shape spelled structurally at the binding site. #164 and
/// the `goty.rs` collision both turned on which nominal a record resolved to, so
/// which spelling produced the type is a real axis, not decoration.
pub const ETYPE: Axis = Axis::new("etype", &["alias", "inline"]);

pub const STRATUM: Stratum = Stratum {
    name: "emit_shape",
    axes: &[ESHAPE, ETYPE],
    coordinate: Some("v0.17 typed-emit contract + goty.rs fieldset collision"),
    // `record_fieldsets` is built over the WHOLE compilation
    // (`lower/src/lower.rs:246-266`); batching two of these would let one case's
    // struct selection depend on its neighbour, which is the very thing the
    // `fieldset-by-type` property asserts about.
    isolated: true,
};

const SURVIVOR: i64 = 42;

/// The Sky program, the name of the function under test, and the properties this
/// coordinate asserts.
struct Probe {
    src: String,
    /// The emitted Go function name — `Main_<def>`; the presence probe.
    func: &'static str,
    properties: Vec<&'static str>,
}

fn rec_decl(etype: &str) -> (&'static str, &'static str) {
    // `(the parameter's written type, the literal used to build one)`.
    match etype {
        "alias" => (
            "Rec",
            "{ alpha = 1, beta = \"x\", gamma = 42 }",
        ),
        "inline" => (
            "{ alpha : Int, beta : String, gamma : Int }",
            "{ alpha = 1, beta = \"x\", gamma = 42 }",
        ),
        other => panic!("emit_shape: unknown etype {other:?}"),
    }
}

fn probe(a: &Assignment) -> Probe {
    let eshape = a.get(ESHAPE);
    let etype = a.get(ETYPE);
    let (ty, lit) = rec_decl(etype);
    let alias_decl = if etype == "alias" {
        "type alias Rec =\n    { alpha : Int, beta : String, gamma : Int }\n\n\n".to_string()
    } else {
        String::new()
    };

    // `main` CONSUMES the probe, always. Dead-code elimination removes an
    // unreferenced def, and a property asserted over a function that was never
    // emitted is vacuously true — the exact defect class this branch removes.
    let (decls, call, func, mut props) = match eshape {
        "record_update" => (
            format!("bump : {ty} -> {ty}\nbump r =\n    {{ r | alpha = 7 }}\n"),
            format!("(bump {lit}).gamma"),
            "Main_bump",
            vec!["no-erasure", "no-narrowing", "no-any-in-signature", "no-raw-type-assert"],
        ),
        "field_read" => (
            format!("pick : {ty} -> Int\npick r =\n    r.gamma\n"),
            format!("pick {lit}"),
            "Main_pick",
            vec!["no-erasure", "no-narrowing", "no-any-in-signature", "no-raw-type-assert"],
        ),
        "field_read_in_binop" => (
            format!("pick : {ty} -> Int\npick r =\n    r.gamma + 0\n"),
            format!("pick {lit}"),
            "Main_pick",
            vec!["no-erasure", "no-narrowing", "no-any-in-signature", "no-raw-type-assert"],
        ),
        "tuple_projection" => (
            format!("pick : ( {ty}, Int ) -> Int\npick p =\n    (fst p).gamma\n"),
            format!("pick ( {lit}, 0 )"),
            "Main_pick",
            vec!["no-erasure", "no-any-in-signature", "no-raw-type-assert"],
        ),
        "nested_record" => (
            format!(
                "wrap : {ty} -> {{ inner : {ty}, tag : Int }}\nwrap r =\n    \
                 {{ inner = r, tag = 0 }}\n"
            ),
            format!("(wrap {lit}).inner.gamma"),
            "Main_wrap",
            vec!["no-erasure", "no-any-in-signature", "no-raw-type-assert"],
        ),
        other => panic!("emit_shape: unknown eshape {other:?}"),
    };

    // The struct-shape properties only mean anything when a NAMED alias produced
    // a named Go struct to inspect.
    if etype == "alias" {
        props.push("field-order-declared");
        props.push("fieldset-by-type");
    }

    // The second alias is what makes `fieldset-by-type` non-trivial: identical
    // field NAMES, different field TYPES. It is referenced from `main` so DCE
    // cannot delete it. `Kv`'s names are also the field-name set of the real
    // `Std.Analytics.EventProp` (`{ key, value }`), which is in scope in every
    // compilation — the collision the corpus needed REAL stdlib names to
    // reproduce.
    let collider = if etype == "alias" {
        "type alias KvA =\n    { key : String, value : Int }\n\n\n\
         type alias KvB =\n    { key : String, value : String }\n\n\n\
         ka : KvA\nka =\n    { key = \"k\", value = 7 }\n\n\n\
         kb : KvB\nkb =\n    { key = \"k\", value = \"s\" }\n\n\n"
            .to_string()
    } else {
        String::new()
    };
    let collider_use = if etype == "alias" {
        " ++ String.fromInt ka.value ++ kb.value"
    } else {
        ""
    };

    let src = format!(
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\n\n\
         {alias_decl}{collider}{decls}\n\
         main =\n    println (String.fromInt ({call}){collider_use})\n"
    );

    Probe {
        src,
        func,
        properties: props,
    }
}

/// A property this coordinate is KNOWN to violate, with the reproduction.
///
/// v2 §7.2's BLOCKED contract, per property: the case still runs, it never
/// contributes PASS, a transition to green is reported loudly, and after the
/// expiry it FAILS. It is deliberately not a skip — a skip is how "SKIP counted
/// as pass" happened.
fn blocked_property(_a: &Assignment, _prop: &str) -> Option<Blocked> {
    // EMPTY, and that is the point. This family opened with two blocks, both
    // alias-only, both found by the gate itself on its first run:
    //
    //   * `emit_shape/field_read-alias [no-narrowing]` — `pick r = r.gamma`
    //     emitted `rt.AsInt(v_0.Gamma)` for a field the struct declares `int`,
    //     because `Expr::Access` typed the selector from the expected slot
    //     instead of the struct's own field type.
    //   * `emit_shape/tuple_projection-alias [no-erasure]` — `pick p = (fst
    //     p).gamma` on `( Rec, Int )` emitted
    //     `rt.AsInt(rt.Field(rt.Basics_fst(any(v_0)), "Gamma"))` while the
    //     parameter was already `rt.T2[Main_Rec_R, int]`.
    //
    // Both said "NOT fixed here" for the same reason: the fix moves emitted
    // bytes, and CLAUDE.md §0.3 rule 2 puts that call with the user. The user
    // authorised it; both are fixed in `lower/src/lower.rs` (`Expr::Access`
    // takes the declared field type; `tuple_projection` reads `.V0`/`.V1` off a
    // statically typed tuple), the gate reported both NOW GREEN, and the blocks
    // are deleted in the same commit — the contract's own instruction, since a
    // block that outlives its bug hides the next regression.
    //
    // Keep the hook. The next coordinate this family finds red is declared here,
    // with a date, and is charged as blocked rather than silenced.
    None
}

pub fn build(a: &Assignment) -> GenCase {
    let p = probe(a);
    // A coordinate is BLOCKED if ANY of the properties it asserts is known-red.
    // The gate still evaluates every property and says which one; the block only
    // decides whether the red is charged as a failure.
    let blocked = p
        .properties
        .iter()
        .find_map(|prop| blocked_property(a, prop));

    GenCase {
        id: format!("{}/{}", STRATUM.name, a.slug()),
        stratum: STRATUM.name,
        family: Family::E,
        mode: Mode::EmitShape,
        isolation: Isolation::Unit,
        axes: a.clone(),
        // Every assertion is a PROPERTY the generator stated before the compiler
        // ran, never a snapshot of what it emitted. See the module docstring for
        // why that makes it V rather than D.
        class: Class::V,
        witness: Witness::Shape,
        coordinate: STRATUM.coordinate.map(|s| s.to_string()),
        modules: vec![("Main".to_string(), p.src)],
        entry: "Main".to_string(),
        // Emit-shape cases never run, so there is no stdout to predict. The
        // `stdout` recorded here is the value the program WOULD print — carried so
        // the manifest row is self-describing and so a case can be promoted to a
        // behavioural family without being rewritten.
        expect: Expect::Accept {
            stdout: format!("{SURVIVOR}"),
        },
        body: None,
        blocked,
        twin: None,
        emit_properties: p.properties,
    }
}

pub fn all() -> Vec<GenCase> {
    super::axes::full_cross(&STRATUM).iter().map(build).collect()
}

// ---------------------------------------------------------------------------
// Checking a property against emitted Go
// ---------------------------------------------------------------------------

/// The `rt.*` identifiers that erase a static type: type-variable erasure
/// (`Coerce`, `Field`) and reflect dispatch (`SkyCall`). A monomorphic function
/// over concrete records cannot legitimately need any of them.
const ERASURE_TOKENS: &[&str] = &["Coerce", "Field", "SkyCall"];

/// Every runtime narrowing token `coerce-floor` tracks. Kept in step with
/// `coerce_floor_gate::TRACKED` by [`tests::narrowing_set_matches_coerce_floor`]
/// — two gates that disagree about what a narrowing IS would let one certify
/// what the other forbids.
const NARROWING_TOKENS: &[&str] = &[
    "Coerce",
    "CoerceString",
    "CoerceInt",
    "CoerceBool",
    "CoerceFloat",
    "AsInt",
    "AsFloat",
    "AsString",
    "AsBool",
    "AsRune",
    "AsIntOrZero",
    "AsFloatOrZero",
    "AsBoolOrFalse",
    "AsList",
    "AsListT",
    "AsListAny",
    "AsTuple2",
    "AsTuple2T",
    "AsTuple3",
    "AsTuple3T",
    "AsDict",
    "AsMapT",
    "AsMapAny",
    "Field",
    "SkyCall",
];

/// Extract the body of `func <name>(…) … { … }` from emitted Go.
///
/// Brace-counting from the header's opening `{`. The emitter never puts a `{` or
/// `}` inside a string literal in a function body it generates for these
/// programs (there are no string literals with braces in the generated Sky), so a
/// counter is exact here and a Go parser would be a dependency for nothing.
/// `None` means the function is ABSENT — which the caller must treat as a
/// failure, never as a satisfied property.
fn func_body<'a>(go: &'a str, name: &str) -> Option<&'a str> {
    let header = format!("func {name}(");
    let at = go.find(&header)?;
    let open = at + go[at..].find('{')?;
    let bytes = go.as_bytes();
    let mut depth = 0usize;
    for (i, b) in bytes.iter().enumerate().skip(open) {
        match b {
            b'{' => depth += 1,
            b'}' => {
                depth -= 1;
                if depth == 0 {
                    return Some(&go[open + 1..i]);
                }
            }
            _ => {}
        }
    }
    None
}

/// The `func <name>(…) <ret> {` header line.
fn func_header<'a>(go: &'a str, name: &str) -> Option<&'a str> {
    let header = format!("func {name}(");
    let at = go.find(&header)?;
    let open = at + go[at..].find('{')?;
    Some(&go[at..open])
}

/// Count `rt.<Ident>` occurrences whose identifier is EXACTLY one of `tokens`.
///
/// Word-boundary exact, matching `coerce_floor_gate::count_tokens`: `rt.Coerce`
/// and `rt.CoerceString` never alias, and an `rt` that is the tail of a longer
/// identifier (`art.Coerce`) is not counted.
fn count_rt_tokens(src: &str, tokens: &[&str]) -> Vec<String> {
    let set: std::collections::HashSet<&str> = tokens.iter().copied().collect();
    let bytes = src.as_bytes();
    let mut hits = Vec::new();
    let mut i = 0;
    while let Some(rel) = src[i..].find("rt.") {
        let at = i + rel;
        let prev_ok = at == 0 || !(bytes[at - 1].is_ascii_alphanumeric() || bytes[at - 1] == b'_');
        let id_start = at + 3;
        let mut j = id_start;
        while j < bytes.len() && (bytes[j].is_ascii_alphanumeric() || bytes[j] == b'_') {
            j += 1;
        }
        if prev_ok && j > id_start {
            let ident = &src[id_start..j];
            if set.contains(ident) {
                hits.push(format!("rt.{ident}"));
            }
        }
        i = j.max(at + 3);
    }
    hits
}

/// A raw Go type assertion — `x.(T)` — anywhere in `body`.
///
/// Detected as a `.` immediately followed by `(` where the preceding character
/// closes an expression (an identifier char, `)`, or `]`). That excludes a
/// method call's `.f(`, which always has an identifier between the dot and the
/// paren.
fn raw_type_asserts(body: &str) -> usize {
    let b = body.as_bytes();
    let mut n = 0;
    for i in 1..b.len().saturating_sub(1) {
        if b[i] == b'.' && b[i + 1] == b'(' {
            let p = b[i - 1];
            if p.is_ascii_alphanumeric() || p == b'_' || p == b')' || p == b']' {
                n += 1;
            }
        }
    }
    n
}

/// One property's verdict on one case.
pub struct PropOutcome {
    pub property: &'static str,
    pub ok: bool,
    pub detail: String,
}

/// Evaluate every property this case asserts against `go`.
///
/// The presence probe runs FIRST and its failure is reported per property, so a
/// DCE'd function can never read as "all properties satisfied".
pub fn check(case: &GenCase, go: &str) -> Vec<PropOutcome> {
    let func = func_name(case);
    let body = func_body(go, func);
    let header = func_header(go, func);

    case.emit_properties
        .iter()
        .map(|id| {
            let Some(body) = body else {
                return PropOutcome {
                    property: id,
                    ok: false,
                    detail: format!(
                        "`func {func}` is ABSENT from the emitted Go — dead-code \
                         elimination removed it, so this property was about to be \
                         vacuously true. A property over a function that was never \
                         emitted asserts nothing."
                    ),
                };
            };
            match *id {
                "no-erasure" => {
                    let hits = count_rt_tokens(body, ERASURE_TOKENS);
                    PropOutcome {
                        property: id,
                        ok: hits.is_empty(),
                        detail: format!("{func}: {} erasure call(s) {hits:?}", hits.len()),
                    }
                }
                "no-narrowing" => {
                    let hits = count_rt_tokens(body, NARROWING_TOKENS);
                    PropOutcome {
                        property: id,
                        ok: hits.is_empty(),
                        detail: format!("{func}: {} narrowing call(s) {hits:?}", hits.len()),
                    }
                }
                "no-any-in-signature" => {
                    let h = header.unwrap_or("");
                    let bad = h.split(|c: char| !(c.is_alphanumeric() || c == '_'))
                        .any(|w| w == "any");
                    PropOutcome {
                        property: id,
                        ok: !bad,
                        detail: format!("signature `{}`", h.trim()),
                    }
                }
                "no-raw-type-assert" => {
                    let n = raw_type_asserts(body);
                    PropOutcome {
                        property: id,
                        ok: n == 0,
                        detail: format!("{func}: {n} raw `.(T)` assertion(s)"),
                    }
                }
                // The generator WROTE the field order (alpha, beta, gamma) and
                // asserts the emitted struct preserves it. Nothing is read back
                // from the compiler to form the expectation.
                "field-order-declared" => {
                    let ok = struct_field_order(go, "Main_Rec_R")
                        .map(|fs| fs == vec!["Alpha", "Beta", "Gamma"])
                        .unwrap_or(false);
                    PropOutcome {
                        property: id,
                        ok,
                        detail: format!(
                            "Main_Rec_R fields {:?}, declared order [Alpha, Beta, Gamma]",
                            struct_field_order(go, "Main_Rec_R")
                        ),
                    }
                }
                // `KvA = { key : String, value : Int }` and
                // `KvB = { key : String, value : String }` share a field-name set
                // and differ in types. The emitted Go must carry BOTH structs and
                // KvA's `Value` must really be `int`. When this fails, a program
                // that type-checks panics at run time with
                // `rt.Coerce: expected …, got string`.
                "fieldset-by-type" => {
                    let a = struct_field_types(go, "Main_KvA_R");
                    let b = struct_field_types(go, "Main_KvB_R");
                    let ok = match (&a, &b) {
                        (Some(a), Some(b)) => {
                            a.iter().any(|(n, t)| n == "Value" && t == "int")
                                && b.iter().any(|(n, t)| n == "Value" && t == "string")
                        }
                        _ => false,
                    };
                    PropOutcome {
                        property: id,
                        ok,
                        detail: format!("Main_KvA_R {a:?} / Main_KvB_R {b:?}"),
                    }
                }
                other => PropOutcome {
                    property: id,
                    ok: false,
                    detail: format!("no checker for property {other:?}"),
                },
            }
        })
        .collect()
}

/// The emitted Go function this case's properties are about — recomputed from
/// the axes so the manifest does not have to carry it.
fn func_name(case: &GenCase) -> &'static str {
    match case.axes.get(ESHAPE) {
        "record_update" => "Main_bump",
        "nested_record" => "Main_wrap",
        _ => "Main_pick",
    }
}

/// `type <name> struct { A t `…`; B t `…` }` → the field names, in emitted order.
fn struct_field_order(go: &str, name: &str) -> Option<Vec<String>> {
    Some(
        struct_field_types(go, name)?
            .into_iter()
            .map(|(n, _)| n)
            .collect(),
    )
}

/// `type <name> struct { … }` → `(field name, Go type)` in emitted order.
fn struct_field_types(go: &str, name: &str) -> Option<Vec<(String, String)>> {
    let header = format!("type {name} struct {{");
    let at = go.find(&header)?;
    let open = at + header.len();
    let close = open + go[open..].find('}')?;
    Some(
        go[open..close]
            .split(';')
            .filter_map(|f| {
                // `Alpha int `sky:"alpha,int"``
                let f = f.trim();
                if f.is_empty() {
                    return None;
                }
                let mut it = f.split_whitespace();
                let n = it.next()?.to_string();
                let t = it.next()?.to_string();
                Some((n, t))
            })
            .collect(),
    )
}

// ---------------------------------------------------------------------------
// The gate
// ---------------------------------------------------------------------------

/// Materialise `case` under `dir` and emit its Go **without `go build`**.
///
/// `project::emit_example_source` is the exact path `xtask repro` and
/// `xtask coerce-floor` use. Reusing it is not politeness: a private emit path
/// here would be a second answer to "what does this program compile to", and the
/// two would drift (v2 §10).
fn emit(root: &Path, dir: &Path, case: &GenCase) -> Result<String, String> {
    let src = dir.join("src");
    std::fs::create_dir_all(&src).map_err(|e| e.to_string())?;
    for (name, source) in &case.modules {
        let rel: std::path::PathBuf = name
            .split('.')
            .collect::<std::path::PathBuf>()
            .with_extension("sky");
        let path = src.join(&rel);
        if let Some(p) = path.parent() {
            std::fs::create_dir_all(p).map_err(|e| e.to_string())?;
        }
        std::fs::write(&path, source).map_err(|e| e.to_string())?;
    }
    project::emit_example_source(root, dir)
}

pub fn run(root: &Path) -> i32 {
    let cases = all();
    let scratch = super::runner::scratch_root("emit-shape");
    let _ = std::fs::remove_dir_all(&scratch);

    println!("CORPUS EMIT SHAPE — v2 §3.1 family E (properties of the generated Go, no `go build`)");
    println!("  cases      : {}", cases.len());
    println!(
        "  assertions : {} (one per asserted property)",
        cases.iter().map(|c| c.emit_properties.len()).sum::<usize>()
    );
    println!();

    let today = super::runner::today_iso();
    let mut failures: Vec<String> = Vec::new();
    let mut blocked_red: Vec<String> = Vec::new();
    let mut blocked_now_green: Vec<String> = Vec::new();
    let mut expired: Vec<String> = Vec::new();
    let mut proven = 0usize;

    for (i, case) in cases.iter().enumerate() {
        let dir = scratch.join(format!("e{i:03}"));
        let go = match emit(root, &dir, case) {
            Ok(g) => g,
            Err(e) => {
                failures.push(format!("{}: emit failed: {}", case.id, first_line(&e)));
                let _ = std::fs::remove_dir_all(&dir);
                continue;
            }
        };
        let outcomes = check(case, &go);
        let _ = std::fs::remove_dir_all(&dir);

        for o in &outcomes {
            let blocked = case
                .blocked
                .as_ref()
                .filter(|_| blocked_covers(case, o.property));
            match (blocked, o.ok) {
                (Some(b), false) => {
                    if today.as_str() > b.expires {
                        expired.push(format!(
                            "{} [{}] expired {} — {}",
                            case.id, o.property, b.expires, o.detail
                        ));
                    } else {
                        blocked_red.push(format!(
                            "{} [{}] {} (expires {})",
                            case.id, o.property, o.detail, b.expires
                        ));
                    }
                }
                (Some(_), true) => {
                    blocked_now_green.push(format!("{} [{}]", case.id, o.property));
                    proven += 1;
                }
                (None, false) => {
                    failures.push(format!("{} [{}] {}", case.id, o.property, o.detail));
                }
                (None, true) => proven += 1,
            }
        }
    }
    let _ = std::fs::remove_dir_all(&scratch);

    println!("  properties satisfied : {proven}");
    if !blocked_red.is_empty() {
        println!();
        println!("  ---- {} BLOCKED (known product defect, still red) ----", blocked_red.len());
        for f in &blocked_red {
            println!("  {f}");
        }
    }
    if !blocked_now_green.is_empty() {
        println!();
        println!("  ---- {} BLOCKED propert(y/ies) NOW GREEN ----", blocked_now_green.len());
        for f in &blocked_now_green {
            println!("  {f}");
        }
        println!("  Remove the block in the same commit that confirms the fix —");
        println!("  a stale block hides the next regression.");
    }
    if !expired.is_empty() {
        println!();
        println!("  ---- {} BLOCKED propert(y/ies) EXPIRED ----", expired.len());
        for f in &expired {
            println!("  {f}");
        }
        println!("  A block is a deadline, not a parking space.");
    }
    if !failures.is_empty() {
        println!();
        println!("  ---- {} PROPERTY VIOLATION(S) ----", failures.len());
        for f in &failures {
            println!("  {f}");
        }
    }

    if !failures.is_empty() || !expired.is_empty() {
        println!();
        println!(
            "EMIT-SHAPE GATE: FAIL ({} violation(s), {} expired block(s))",
            failures.len(),
            expired.len()
        );
        1
    } else if proven == 0 {
        println!();
        println!("EMIT-SHAPE GATE: FAIL — no property was evaluated. A gate that checked");
        println!("  nothing has not passed.");
        1
    } else if !blocked_red.is_empty() {
        println!();
        println!(
            "EMIT-SHAPE GATE: PASS with {} BLOCKED — every unblocked property holds; the \
             blocked ones reproduce a known defect and are counted, not silenced.",
            blocked_red.len()
        );
        0
    } else {
        println!();
        println!("EMIT-SHAPE GATE: PASS ({proven} properties, none violated)");
        0
    }
}

/// Does this case's block cover `prop`? A case is blocked BY a property, so the
/// block must not absolve the others.
fn blocked_covers(case: &GenCase, prop: &str) -> bool {
    blocked_property(&case.axes, prop).is_some()
}

fn first_line(s: &str) -> String {
    s.lines().next().unwrap_or("").chars().take(140).collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn full_cross_is_the_product_of_the_axis_sizes() {
        let expected: usize = STRATUM.axes.iter().map(|a| a.values.len()).product();
        assert_eq!(all().len(), expected);
    }

    /// Every case asserts at least one property, and every property it names is
    /// in the catalogue with a checker.
    #[test]
    fn every_case_asserts_a_catalogued_property() {
        for c in all() {
            assert!(
                !c.emit_properties.is_empty(),
                "{}: asserts nothing — an emit-shape case with no property is a \
                 build that was thrown away",
                c.id
            );
            for p in &c.emit_properties {
                let _ = property(p);
            }
        }
    }

    /// `no-narrowing` must be a strict superset of `no-erasure`, and it must
    /// agree with `coerce-floor`'s tracked set. Two gates that disagree about
    /// what counts as a runtime narrowing would let one certify what the other
    /// forbids.
    #[test]
    fn narrowing_set_matches_coerce_floor() {
        for t in ERASURE_TOKENS {
            assert!(NARROWING_TOKENS.contains(t), "{t} missing from NARROWING_TOKENS");
        }
        let floor = crate::coerce_floor_gate::tracked_tokens();
        let mut a: Vec<&str> = NARROWING_TOKENS.to_vec();
        let mut b: Vec<&str> = floor.to_vec();
        a.sort_unstable();
        b.sort_unstable();
        assert_eq!(
            a, b,
            "emit-shape's narrowing set and coerce-floor's tracked set have \
             diverged; they must name the same runtime-narrowing surface"
        );
    }

    #[test]
    fn func_body_is_brace_balanced() {
        let go = "func Main_pick(v_0 R) int {\n\treturn func() int { return 1 }()\n}\n\
                  func Other() {}\n";
        let b = func_body(go, "Main_pick").expect("found");
        assert!(b.contains("return func() int { return 1 }()"));
        assert!(!b.contains("func Other"));
        assert!(func_body(go, "Main_absent").is_none());
    }

    /// The presence probe: an ABSENT function must fail every property, never
    /// satisfy them vacuously.
    #[test]
    fn an_absent_function_fails_every_property() {
        let c = build(&full_cross_first());
        let outcomes = check(&c, "package main\n");
        assert!(!outcomes.is_empty());
        for o in outcomes {
            assert!(!o.ok, "property {} passed on an empty program", o.property);
            assert!(o.detail.contains("ABSENT"), "{}", o.detail);
        }
    }

    #[test]
    fn raw_type_assert_detection() {
        assert_eq!(raw_type_asserts("x := v.(int)"), 1);
        assert_eq!(raw_type_asserts("x := f(v).(string)"), 1);
        assert_eq!(raw_type_asserts("x := xs[0].(int)"), 1);
        // A method call is not an assertion.
        assert_eq!(raw_type_asserts("x := v.Method(1)"), 0);
        assert_eq!(raw_type_asserts("x := rt.AsInt(v)"), 0);
    }

    #[test]
    fn struct_fields_parse_in_emitted_order() {
        let go = "type Main_Rec_R struct { Alpha int `sky:\"alpha,int\"`; \
                  Beta string `sky:\"beta,string\"`; Gamma int `sky:\"gamma,int\"` }\n";
        assert_eq!(
            struct_field_order(go, "Main_Rec_R").unwrap(),
            vec!["Alpha", "Beta", "Gamma"]
        );
        assert_eq!(
            struct_field_types(go, "Main_Rec_R").unwrap()[0],
            ("Alpha".to_string(), "int".to_string())
        );
    }

    fn full_cross_first() -> Assignment {
        super::super::axes::full_cross(&STRATUM).remove(0)
    }
}

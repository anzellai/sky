//! Family **S** — stdlib behaviour, every covered public symbol at its edge
//! classes (v2 §3.1).
//!
//! # Why this family exists
//!
//! The mandate: *"compiler + standard lib + built-in 100 % coverage with many
//! use cases … we could've caught them if our tests include different variation
//! of usage of those syntax + imports etc."*
//!
//! Before this family the whole Layer-1 corpus imported exactly two modules —
//! `Sky.Core.Prelude` and `Std.Log`. Every other stdlib module was dark to it.
//! The coverage ledger derives a module's `corpus`-gate coverage from the
//! imports its generator templates emit (`coverage_ledger.rs:2164-2170`), so
//! "dark to the corpus" was also *counted* as dark — correctly. This family is
//! what changes that number, and it changes it by asserting VALUES, not by
//! adding imports.
//!
//! # The honesty constraint, applied to a stdlib
//!
//! `gen.rs`'s module docstring states the rule: only assert values the
//! generator constructed itself, known before the compiler runs. For stdlib
//! behaviour there are exactly three legitimate sources of such a value, and
//! every expectation in this file comes from one of them:
//!
//! 1. **Universal, published constants.** `sha256 ""` is
//!    `e3b0c442…7852b855` because SHA-256 says so; `base64 "Hello"` is
//!    `SGVsbG8=` because RFC 4648 says so. No Sky artefact is consulted.
//! 2. **Elm semantics.** Sky is an Elm-family language (`AGENTS.md`), so
//!    `String.length "世界" == 2` (code points, not bytes), `List.take -1 xs ==
//!    []`, `modBy 3 -1 == 2` (floored) are known from the language family the
//!    surface deliberately copies.
//! 3. **The module's own written promise** — its `-- |` docstring, or, for the
//!    pure-Sky modules (`Sky.Core.Error`, `Sky.Core.Basics`, `Sky.Core.Maybe`,
//!    …), its Sky source, which IS the specification the compiler must
//!    faithfully compile. Asserting `Error.toString (Error.io "boom") == "IO:
//!    boom"` tests that the compiler compiles `Error.sky` correctly; it is not
//!    a snapshot of what the compiler happened to print.
//!
//! What is **NOT** a legitimate source, and appears nowhere below: the Go
//! runtime's implementation. Where a docstring and the Go body disagree, this
//! file asserts the **docstring**, because that is the promise the product
//! made. A resulting red is a finding, which is the point.
//!
//! Values that could only be obtained by running the compiler are not asserted
//! at all — they are omitted and named in the report, rather than admitted as
//! [`Class::D`] filler that would inflate the coverage numerator (v2 §5.5).
//!
//! # The axis witness
//!
//! `stdlib_edge`'s axis-under-test is `edge`, neutralised at `nominal`. Moving
//! from the nominal input to an empty / boundary / unicode / failure input
//! changes both the emitted Go (different literals, different branches) and the
//! value — so this stratum witnesses its axis by emit-shape AND carries an
//! independent oracle, which is the strongest form available here.
//!
//! `stdlib_import`'s axis-under-test is `shadow`, neutralised at `none`, for a
//! reason `witness.rs` already documents at length: **import syntax is erased by
//! name resolution**, so five spellings of the same import emit byte-identical
//! Go and no emit-shape witness for `import_shape` can ever exist. `shadow` is
//! the axis that does reach the compiler, and — unlike the inert `collision`
//! axis on the older `import_shape` stratum — its values produce genuinely
//! different programs with genuinely different, generator-predicted values.

use super::axes::*;
use super::gen::{Body, Expect};

// ---------------------------------------------------------------------------
// One assertion
// ---------------------------------------------------------------------------

/// One battery item: a Sky expression, and the `String` the generator predicts
/// it will render to.
///
/// `render` is the *rendering* of the expression down to a `String` — the case
/// prints `String`s only, so a `Bool` becomes `"T"`/`"F"`, a `Maybe Int` becomes
/// its digits or `"N"`, a `Result` becomes its payload or `"E"`. The rendering
/// is chosen by the constructor, so a battery author cannot accidentally
/// compare an `Int` against a `Bool`'s spelling.
#[derive(Clone, Debug)]
pub struct Check {
    /// The Sky expression, already rendered to `String`.
    body: String,
    /// The value the generator predicted, before any compiler ran.
    expect: String,
    /// `Module.symbol` this item exercises — the coverage claim, per item.
    covers: &'static [&'static str],
}

impl Check {
    /// The Sky expression, already rendered to `String`.
    pub fn body(&self) -> &str {
        &self.body
    }
    /// The value the generator predicted, before any compiler ran.
    pub fn expect(&self) -> &str {
        &self.expect
    }
    /// `Module.symbol` this item exercises — the coverage claim, per item.
    pub fn covers(&self) -> &'static [&'static str] {
        self.covers
    }
}

/// A `String`-valued expression.
pub fn s(covers: &'static [&'static str], expr: &str, expect: &str) -> Check {
    Check {
        body: expr.to_string(),
        expect: expect.to_string(),
        covers,
    }
}

/// An `Int`-valued expression.
pub fn i(covers: &'static [&'static str], expr: &str, expect: i64) -> Check {
    Check {
        body: format!("String.fromInt ({expr})"),
        expect: expect.to_string(),
        covers,
    }
}

/// A `Float`-valued expression, rendered through `String.fromFloat`.
pub fn f(covers: &'static [&'static str], expr: &str, expect: &str) -> Check {
    Check {
        body: format!("String.fromFloat ({expr})"),
        expect: expect.to_string(),
        covers,
    }
}

/// A `Bool`-valued expression. `"T"` / `"F"` rather than `toString`, so the
/// assertion does not also depend on how `Bool` renders.
pub fn bo(covers: &'static [&'static str], expr: &str, expect: bool) -> Check {
    Check {
        body: format!("if {expr} then\n        \"T\"\n\n    else\n        \"F\""),
        expect: if expect { "T" } else { "F" }.to_string(),
        covers,
    }
}

/// A `Maybe Int`. `Nothing` renders as `"N"`.
pub fn mi(covers: &'static [&'static str], expr: &str, expect: Option<i64>) -> Check {
    Check {
        body: format!(
            "case {expr} of\n        Just v ->\n            String.fromInt v\n\n        Nothing ->\n            \"N\""
        ),
        expect: expect.map(|v| v.to_string()).unwrap_or_else(|| "N".into()),
        covers,
    }
}

/// A `Maybe String`. `Nothing` renders as `"N"`.
pub fn ms(covers: &'static [&'static str], expr: &str, expect: Option<&str>) -> Check {
    Check {
        body: format!(
            "case {expr} of\n        Just v ->\n            v\n\n        Nothing ->\n            \"N\""
        ),
        expect: expect.unwrap_or("N").to_string(),
        covers,
    }
}

/// A `Result Error String`. `Err` renders as `"E"` — the case asserts THAT the
/// failure branch was taken, never the message, because a message is not a
/// value the generator constructed.
pub fn rs(covers: &'static [&'static str], expr: &str, expect: Option<&str>) -> Check {
    Check {
        body: format!(
            "case {expr} of\n        Ok v ->\n            v\n\n        Err _ ->\n            \"E\""
        ),
        expect: expect.unwrap_or("E").to_string(),
        covers,
    }
}

/// A `Result Error Int`.
pub fn ri(covers: &'static [&'static str], expr: &str, expect: Option<i64>) -> Check {
    Check {
        body: format!(
            "case {expr} of\n        Ok v ->\n            String.fromInt v\n\n        Err _ ->\n            \"E\""
        ),
        expect: expect.map(|v| v.to_string()).unwrap_or_else(|| "E".into()),
        covers,
    }
}

/// A `List Int`, rendered comma-joined.
pub fn li(covers: &'static [&'static str], expr: &str, expect: &str) -> Check {
    Check {
        body: format!("String.join \",\" (List.map String.fromInt ({expr}))"),
        expect: expect.to_string(),
        covers,
    }
}

/// A `List String`, rendered comma-joined.
pub fn ls(covers: &'static [&'static str], expr: &str, expect: &str) -> Check {
    Check {
        body: format!("String.join \",\" ({expr})"),
        expect: expect.to_string(),
        covers,
    }
}

/// The LENGTH of a list. Used where the elements' order or spelling is not a
/// promise the surface makes, so asserting them would be asserting the
/// implementation.
pub fn ln(covers: &'static [&'static str], expr: &str, expect: i64) -> Check {
    Check {
        body: format!("String.fromInt (List.length ({expr}))"),
        expect: expect.to_string(),
        covers,
    }
}

// ---------------------------------------------------------------------------
// The covered surfaces
// ---------------------------------------------------------------------------

/// One stdlib module this family covers, and how the generated case reaches it.
#[derive(Clone, Copy, Debug)]
pub struct Surface {
    /// The `surface` axis value.
    pub slug: &'static str,
    /// The real module path — drawn from `sky-stdlib/`, never invented. The
    /// ledger parses this out of the emitted import line to charge the `corpus`
    /// gate with the module.
    pub module: &'static str,
    /// Extra modules the battery needs in scope.
    pub also: &'static [&'static str],
}

const fn sf(slug: &'static str, module: &'static str) -> Surface {
    Surface {
        slug,
        module,
        also: &[],
    }
}

const fn sf2(slug: &'static str, module: &'static str, also: &'static [&'static str]) -> Surface {
    Surface { slug, module, also }
}

/// Every surface Family S covers today.
///
/// This is deliberately the **pure, value-assertable** part of the stdlib: a
/// module whose surface is `Task Error a` cannot be asserted from a plain
/// `checkValue : String`, and a module whose output is a `VNode` / `Element` is
/// covered by Layer 2, not here. What is NOT in this list is dark to Family S,
/// and the gap is reported rather than papered over.
pub const SURFACES: &[Surface] = &[
    sf("string", "Sky.Core.String"),
    sf("list", "Sky.Core.List"),
    sf("dict", "Sky.Core.Dict"),
    sf("set", "Sky.Core.Set"),
    sf("maybe", "Sky.Core.Maybe"),
    sf("result", "Sky.Core.Result"),
    sf("char", "Sky.Core.Char"),
    sf("encoding", "Sky.Core.Encoding"),
    sf("crypto", "Sky.Core.Crypto"),
    sf("secret", "Sky.Core.Secret"),
    sf("math", "Sky.Core.Math"),
    sf("basics", "Sky.Core.Basics"),
    sf("tostring", "Sky.Core.ToString"),
    sf("path", "Sky.Core.Path"),
    sf("error", "Sky.Core.Error"),
    sf("decimal", "Std.Decimal"),
    sf("money", "Std.Money"),
    sf("csv", "Std.Csv"),
    sf("regex", "Sky.Core.Regex"),
    sf2(
        "json",
        "Sky.Core.Json.Encode",
        &["Sky.Core.Json.Decode as Decode"],
    ),
    // ---- the five modules the ledger named as dark-but-assertable ----------
    //
    // `.claude/AUTONOMOUS_GOAL.md`: *"67 of 87 stdlib modules are dark to Family
    // S. Most are `Task`-valued or render `Element`s, which a value assertion
    // cannot reach. `Sky.Core.Bytes`, `Sky.Core.Jwt`, `Std.Codec`,
    // `Std.Markdown`, `Std.Compression` are pure and assertable — real,
    // closeable gaps."* These are those five, and two of them needed a way in
    // that did not exist for the modules above:
    //
    // * `Std.Compression` is entirely `Task Error a`. `Task.run : Task e a ->
    //   Result e a` is the bridge, and the operations are deterministic, so a
    //   `Result` assertion is a value assertion.
    // * `Std.Markdown` returns `Element msg`. The case walks the tree with its
    //   own fold over `Std.Ui`'s exposed constructors — which also makes the
    //   module's SECURITY promise ("never emits raw HTML, scripts, or event
    //   handlers") assertable, by counting `Raw` nodes.
    sf2("bytes", "Sky.Core.Bytes", &["Sky.Core.Maybe as Maybe"]),
    sf2("jwt", "Sky.Core.Jwt", &["Sky.Core.Json.Encode as Encode"]),
    sf("codec", "Std.Codec"),
    // `Std.Ui` for the `Element` fold (the `Raw`-node count) and for
    // `Ui.layout`; `Std.Html` for `render`, which is what turns the tree into
    // the HTML string the security assertions are actually about.
    sf2("markdown", "Std.Markdown", &["Std.Ui", "Std.Html as Html"]),
    sf2(
        "compression",
        "Std.Compression",
        &["Sky.Core.Task as Task", "Sky.Core.Bytes as Bytes"],
    ),
];

pub fn surface(slug: &str) -> &'static Surface {
    SURFACES
        .iter()
        .find(|s| s.slug == slug)
        .unwrap_or_else(|| panic!("no stdlib surface for slug {slug:?}"))
}

/// The qualifier a `import <module>` puts in scope: the module's LAST path
/// segment, unless the case imports it under an alias.
///
/// Used by the coverage report to turn a `covers` tag such as
/// `"String.toUpper"` back into the `(module, symbol)` pair the real inventory
/// is keyed on.
pub fn qualifier_of(module: &str) -> &str {
    match import_alias(module) {
        Some(a) => a,
        None => module.rsplit('.').next().expect("module has a segment"),
    }
}

// ---------------------------------------------------------------------------
// The coverage report
// ---------------------------------------------------------------------------

/// Which `(module, symbol)` pairs Family S actually asserts something about.
///
/// Derived from the per-item `covers` tags, not from the imports — importing a
/// module proves nothing about it. This is the numerator of any claim this
/// family makes, and it is deliberately smaller than "every module we import".
pub fn covered_symbols() -> std::collections::BTreeSet<(String, String)> {
    let mut out = std::collections::BTreeSet::new();
    for s in SURFACES {
        // The qualifier a `covers` tag is written against, mapped back to the
        // module that actually owns the symbol.
        let mut owners: Vec<(&str, &str)> = vec![(qualifier_of(s.module), s.module)];
        for m in s.also {
            // `also` entries may carry their own alias, e.g.
            // `Sky.Core.Json.Decode as Decode`.
            let (path, alias) = match m.split_once(" as ") {
                Some((p, a)) => (p, a),
                None => (*m, m.rsplit('.').next().unwrap()),
            };
            owners.push((alias, path));
        }
        for e in EDGE.values {
            for c in battery(s.slug, e) {
                for tag in c.covers {
                    let Some((q, sym)) = tag.split_once('.') else {
                        continue;
                    };
                    // `JsonEncode` / `JsonDecode` are written in the tags
                    // because `Encode` / `Decode` alone do not say which module
                    // a symbol belongs to.
                    // `Kernel.<Pseudo>.<name>` — a member of a KERNEL
                    // pseudo-module (`hir::KERNEL_FUNCTIONS`), not a symbol in
                    // any `.sky` module's `exposing` list. `toString`, `modBy`,
                    // `compare`, `negate`, `List.sort`, … are reachable in
                    // every Sky program and appear in NO `api/symbols.json`
                    // entry, because that manifest is built entirely from
                    // `sky-stdlib/**.sky` headers + `exposing` lists.
                    //
                    // Until 2026-08-11 they were tagged `Prelude.*` and mapped
                    // to `Sky.Core.Prelude`, a module the inventory does not
                    // contain — so they contributed to NEITHER the numerator
                    // nor the denominator and simply vanished. They are now
                    // attributed to a `kernel:<Pseudo>` namespace which
                    // [`kernel_inventory`] gives a real denominator, so the
                    // report divides them by something instead of dropping
                    // them. See `report`'s KERNEL section.
                    if q == "Kernel" {
                        if let Some((pseudo, name)) = sym.split_once('.') {
                            out.insert((format!("kernel:{pseudo}"), name.to_string()));
                        }
                        continue;
                    }
                    let module = match q {
                        // `JsonEncode` / `JsonDecode` are written in the tags
                        // because `Encode` / `Decode` alone do not say which
                        // module a symbol belongs to.
                        "JsonEncode" => "Sky.Core.Json.Encode",
                        "JsonDecode" => "Sky.Core.Json.Decode",
                        other => owners
                            .iter()
                            .find(|(alias, _)| *alias == other)
                            .map(|(_, m)| *m)
                            .unwrap_or(s.module),
                    };
                    out.insert((module.to_string(), sym.to_string()));
                }
            }
        }
    }
    // The `dict_key_crossing` stratum is Family S too, and its claim is the same
    // shape: `(module, symbol)` derived from per-item `covers` tags, never from
    // the fact that it imports `Sky.Core.Dict`.
    out.extend(super::dict_crossing::covered_symbols());
    out
}

/// The KERNEL pseudo-module surface: pseudo-module -> every member it
/// advertises, read from `hir::KERNEL_FUNCTIONS` through the same accessor the
/// `exposing (..)` binder uses.
///
/// **This is the answer to a denominator that omitted real symbols.**
/// `api/symbols.json` is built entirely from `sky-stdlib/**.sky` module headers
/// and `exposing` lists (`project::render_doc_site_export`), so a symbol that
/// exists only as a kernel-qualifier member — `toString`, `modBy`, `compare`,
/// `negate`, `List.sort`, `List.sortBy`, and 90-odd others — has no entry, is
/// invisible to `sky doc`, and cannot be divided by. The bridge that once
/// carried kernel metadata into the doc path (`project/src/kernel_api.rs`) was
/// deleted in `054f6d26`, so nothing has fed it since v0.19.
///
/// The honest repair is not to invent `.sky` signatures for them — that is a
/// per-module typing project with its own ratchet
/// (`project/tests/kernel_signature_coverage.rs`) and its own regression
/// history — but to COUNT them, in their own namespace, against their own
/// denominator. `hir` is the single source of truth for both, so this table
/// cannot drift from what the surface advertises.
pub fn kernel_inventory() -> std::collections::BTreeMap<String, std::collections::BTreeSet<String>>
{
    let mut out: std::collections::BTreeMap<String, std::collections::BTreeSet<String>> =
        Default::default();
    for (_, pseudo) in hir::KERNEL_MODULES {
        let Some(members) = hir::kernel_functions(pseudo) else {
            continue;
        };
        out.entry((*pseudo).to_string())
            .or_default()
            .extend(members.iter().map(|m| (*m).to_string()));
    }
    for (pseudo, member) in ROUTED_ONLY_KERNEL_MEMBERS {
        out.entry((*pseudo).to_string())
            .or_default()
            .insert((*member).to_string());
    }
    out
}

/// Kernel members that a user can CALL but that `hir::KERNEL_FUNCTIONS` does not
/// advertise — the surface is bounded by the LOWERER's routing table, not by
/// `hir`'s, and the two have drifted.
///
/// Found by this file's own `every_kernel_covers_tag_is_advertised_by_hir` test
/// on its first run. `List.sortWith` type-checks, lowers, builds and runs:
///
/// ```text
/// import Sky.Core.List as List
/// main = println (String.join "," (List.map String.fromInt
///          (List.sortWith (\a b -> b - a) [ 2, 10, 9 ])))   -- prints 10,9,2
/// ```
///
/// …yet it is in neither `KERNEL_FUNCTIONS` nor `PRELUDE_QUALIFIERS`. It
/// resolves because the qualifier surface is only checked at CODEGEN
/// (`[E4005] List has no member notAThing (the Sky runtime exports no
/// rt.List_notAThing)`), against `lower::kernel`'s table — which does route it
/// (`rust/crates/lower/src/kernel.rs:206`).
///
/// The consequence for a coverage number: a symbol users call is in no
/// inventory at all, so it can be neither covered nor reported as uncovered.
/// That is the same dishonesty class this whole section exists to remove, one
/// layer down, so it is DECLARED here rather than dropped — and the declaration
/// is checked from both ends (see
/// `every_routed_only_member_is_really_routed_and_really_unadvertised`), so a
/// row cannot rot into a lie either by the member disappearing or by `hir`
/// catching up.
///
/// The fix belongs in `hir` (advertise it) plus a `.sky` signature, which is the
/// declared "93 kernel members have no Sky signature" work with its own ratchet
/// (`project/tests/kernel_signature_coverage.rs`) — not a coverage-accounting
/// change, and not something to smuggle in here.
const ROUTED_ONLY_KERNEL_MEMBERS: &[(&str, &str)] = &[("List", "sortWith")];

/// The stdlib inventory: module → its public `exposing` surface, read from the
/// SAME `api/symbols.json` the coverage ledger uses (the `sky doc --export`
/// code path, in process — no `sky` binary, ~60 ms).
///
/// Extracted from [`report`] so the RATCHET below and the printed report share
/// one definition of the denominator. Two readings of "what is public" is how
/// a report and its gate come to disagree while both stay green.
pub fn inventory(
    root: &std::path::Path,
) -> Result<std::collections::BTreeMap<String, std::collections::BTreeSet<String>>, String> {
    let tmp = std::env::temp_dir().join(format!(
        "sky-familyS-inventory-{}-{:?}",
        std::process::id(),
        std::thread::current().id()
    ));
    let _ = std::fs::remove_dir_all(&tmp);
    let (proj, out) = (tmp.join("project"), tmp.join("site"));
    std::fs::create_dir_all(&proj)
        .and(std::fs::create_dir_all(&out))
        .map_err(|e| format!("cannot create {}: {e}", tmp.display()))?;
    let manifest = project::render_doc_site_export(root, &proj, &out)
        .map_err(|e| format!("sky doc --export code path FAILED: {e}"))
        .and_then(|()| {
            std::fs::read_to_string(out.join("api").join("symbols.json"))
                .map_err(|e| format!("no api/symbols.json: {e}"))
        });
    let _ = std::fs::remove_dir_all(&tmp);
    let json: serde_json::Value = serde_json::from_str(&manifest?)
        .map_err(|e| format!("symbols.json is not JSON: {e}"))?;
    let mut inventory: std::collections::BTreeMap<String, std::collections::BTreeSet<String>> =
        Default::default();
    for e in json["entries"].as_array().map(|a| a.as_slice()).unwrap_or(&[]) {
        let (m, n) = (
            e["module"].as_str().unwrap_or_default(),
            e["name"].as_str().unwrap_or_default(),
        );
        if !m.is_empty() && !n.is_empty() {
            inventory
                .entry(m.to_string())
                .or_default()
                .insert(n.to_string());
        }
    }
    Ok(inventory)
}

// ---------------------------------------------------------------------------
// THE RATCHET — what makes item 3's number load-bearing
// ---------------------------------------------------------------------------
//
// `.claude/AUTONOMOUS_GOAL.md` item 3 claims *"67 of 87 stdlib modules are dark
// to Family S … `Sky.Core.Bytes`, `Sky.Core.Jwt`, `Std.Codec`, `Std.Markdown`,
// `Std.Compression` are pure and assertable — real, closeable gaps."* Those five
// were closed, and the CASE COUNTS behind them are pinned exactly
// (`CORPUS_EXPECTED` / `CORPUS_BEHAVIOURAL_EXPECTED`). The MODULE number was
// not pinned by anything: `report` computed it, printed it, and returned 0 no
// matter what it said, and no workflow or script ran the subcommand at all. It
// could have gone back to 67 in silence.
//
// Two things are pinned here, and both are checked from `cargo test -p xtask`
// (already in CI: `cargo test --workspace --exclude ty --locked`) as well as by
// `report`'s exit code, so the number fails in CI without costing a tier gate.
// The whole computation is ~60 ms.
//
// WHY A SET AND A CEILING, NOT A PERCENTAGE. `kernel_signature_coverage.rs` is
// the precedent: it freezes the exact membership that is untyped today and
// ratchets DOWN only. A set says which module regressed; a percentage says only
// that something did.

/// Every stdlib module Family S asserts **at least one public symbol** in.
/// **Ratchets: this set may only GROW.**
///
/// A module dropping out is the regression item 3's number describes, and it is
/// checked by exact equality — so covering a new module also fails here, asking
/// for the row (and for item 3's number to be restated). Both directions matter:
/// an un-updated list is how a coverage claim goes stale while staying green.
///
/// This counts ASSERTION, not mention. `Sky.Core.Task`, `Std.Html` and `Std.Ui`
/// appear in `SURFACES`'s `also` lists — they are imported so the `markdown` /
/// `compression` batteries can reach `Task.run` and fold an `Element` — but no
/// battery asserts a value ABOUT a symbol of theirs, so they are NOT here. The
/// printed report used to count them as touched, which understated the dark set
/// by exactly those 3 (it said 59; the honest number is 62).
pub const ASSERTED_MODULES: &[&str] = &[
    "Sky.Core.Basics",
    "Sky.Core.Bytes",
    "Sky.Core.Char",
    "Sky.Core.Crypto",
    "Sky.Core.Dict",
    "Sky.Core.Encoding",
    "Sky.Core.Error",
    "Sky.Core.Json.Decode",
    "Sky.Core.Json.Encode",
    "Sky.Core.Jwt",
    "Sky.Core.List",
    "Sky.Core.Math",
    "Sky.Core.Maybe",
    "Sky.Core.Path",
    "Sky.Core.Regex",
    "Sky.Core.Result",
    "Sky.Core.Secret",
    "Sky.Core.Set",
    "Sky.Core.String",
    "Sky.Core.ToString",
    "Std.Codec",
    "Std.Compression",
    "Std.Csv",
    "Std.Decimal",
    "Std.Markdown",
    "Std.Money",
];

/// Stdlib modules with NO Family-S assertion at all — item 3's "dark" number.
/// **FAIL-ON-INCREASE**, like `coerce-floor`.
///
/// 87 modules in the inventory, 25 asserted ⇒ 62 dark. Item 3 was written when
/// it was 67 and predicted "~62"; this is that number, measured rather than
/// estimated. A new stdlib module that nothing asserts pushes it up and turns
/// this red — which is the intended conversation, not an accident: the module is
/// either coverable (cover it) or it is `Task`/`Element`-shaped (say so here and
/// raise the ceiling in the same commit).
///
/// Raised 62 → 63 (2026-08-18) for `Sky.Config`: it is config-shaped, not
/// value-producing. Its surface is `default` + `withX` builders that produce an
/// opaque `Config` consumed for its EFFECT by the compiler-emitted
/// `rt.ApplyConfig(Main_config())`, exactly the `Task`/`Element`-shaped case
/// this comment names — the Family-S value corpus has no value to assert on. Its
/// behaviour is covered instead by `runtime-go/rt/sky_config_test.go` (the
/// precedence gate + mutation proof + behavioural oracle) and
/// `rust/crates/project/tests/sky_config_entry.rs` (discovery / DCE / emission).
///
/// Raised 63 → 64 (2026-08-22) for `Std.Spa`: it is the client-side TEA entry
/// module (Sky.Spa), config-shaped in exactly the same way. Its surface is
/// `config` (produces an opaque `AppConfig` consumed for its EFFECT by
/// `Spa_app`) + `app` (returns `Task Error ()`, forced at `main`), so the
/// Family-S value corpus has no value to assert on. Its behaviour is covered
/// instead by the Sky.Spa examples (`examples/60-spa-todos` … `64-app-native` —
/// real Sky.Spa apps compiled to `GOOS=js GOARCH=wasm`) and the kernel-surface
/// gate
/// (`rust/crates/project/tests/kernel_surface.rs`, which pins `Std/Spa.sky`'s
/// `config`/`app` bindings + `Spa_config`/`Spa_app` runtime symbols in sync).
///
/// Raised 64 → 66 (2026-08-24) for `Std.Native` + `Std.Bundle` (exp/spa):
///   * `Std.Native` is client device capabilities, every binding an effect
///     (`geolocation`/`clipboard*`/`share`/`storage*`/`notify`/`pick*`/`bridge`,
///     each `Ffi.kernel "Native_*"` → `Task Error a`). There is no pure value for
///     a Family-S corpus to assert; the effects are covered e2e by
///     `examples/64-app-native` (browser + iOS + Android) and the `!js` Err-stub
///     tests in `runtime-go/rt/native_test.go`.
///   * `Std.Bundle` is BUILD-TIME packaging identity — an opaque `Bundle` built
///     by the `withX` builder, read at `sky build --target` time, not at runtime.
///     It has no runtime value semantics to assert; it is covered by the bundle
///     unit tests + `--target` build tests in `rust/crates/sky/src/main.rs`.
///
/// Raised 66 → 67 (2026-08-27) for `Std.App` (unified-app-builder):
///   * `Std.App` is the app builder — `App.app`/`web`/`cli`/`tui` produce an
///     opaque `App` value, refined by `withX` builders, run by `App.run` to a
///     `Task Error ()`; the view is an `Element`/`Html`/`String`. There is no
///     pure value a Family-S corpus can assert. It is covered by the `--target`
///     dispatch + view-adapter build/run tests (`rust/crates/sky` `*_flow.rs`,
///     `check_std_app`) and the migrated example sweep, not here.
///   * `Sky.Core.Secret`, added in the same window, is NOT dark — its
///     `reveal ∘ fromString` boundary is a pure value assertion (`secret_battery`).
pub const DARK_MODULE_CEILING: usize = 67;

/// The five modules item 3 named, with the EXACT number of their public symbols
/// Family S asserts. **Exact, never `>=`** (registry.rs: *"`ty/tests/reject.rs`
/// USED to assert `>= 13` against an actual 63 — deleting 50 corpus files kept
/// it green"*).
///
/// [`ASSERTED_MODULES`] alone would let a module keep its seat with one surviving
/// assertion after the rest were deleted. These five are the closure item 3 is
/// measured on, so they are pinned symbol-count-exact as well.
pub const ITEM3_ASSERTED_COUNTS: &[(&str, usize)] = &[
    ("Sky.Core.Bytes", 11),
    ("Sky.Core.Jwt", 13),
    ("Std.Codec", 18),
    ("Std.Compression", 4),
    ("Std.Markdown", 2),
];

/// Modules with >= 1 asserted PUBLIC symbol, and the count per module.
///
/// Intersected with the inventory on purpose: a `covers` tag naming a symbol
/// that is not public must not buy a module a seat.
pub fn asserted_per_module(
    inventory: &std::collections::BTreeMap<String, std::collections::BTreeSet<String>>,
) -> std::collections::BTreeMap<String, usize> {
    let covered = covered_symbols();
    let mut out = std::collections::BTreeMap::new();
    for (module, public) in inventory {
        let n = covered
            .iter()
            .filter(|(m, s)| m == module && public.contains(s))
            .count();
        if n > 0 {
            out.insert(module.clone(), n);
        }
    }
    out
}

/// Check the three pins above. Empty means the ratchet holds.
///
/// Shared by [`report`]'s exit code and by the `cargo test` below, so the CLI
/// and CI cannot disagree about whether coverage regressed.
pub fn ratchet_failures(
    inventory: &std::collections::BTreeMap<String, std::collections::BTreeSet<String>>,
) -> Vec<String> {
    check_pins(&asserted_per_module(inventory), inventory.len())
}

/// The pin check itself, over plain data.
///
/// Split from [`ratchet_failures`] so the tests can hand it a perturbed
/// coverage picture directly. A ratchet whose only test feeds it the real,
/// passing tree proves nothing about whether it can fail.
pub fn check_pins(
    asserted: &std::collections::BTreeMap<String, usize>,
    inventory_modules: usize,
) -> Vec<String> {
    let mut fails = Vec::new();

    let pinned: std::collections::BTreeSet<&str> = ASSERTED_MODULES.iter().copied().collect();
    let live: std::collections::BTreeSet<&str> = asserted.keys().map(|s| s.as_str()).collect();
    for lost in pinned.difference(&live) {
        fails.push(format!(
            "REGRESSION: `{lost}` is pinned in ASSERTED_MODULES but Family S now asserts \
             NOTHING public in it. The dark-module count went UP. Restore the battery's \
             `covers` tags — do not delete the row."
        ));
    }
    for gained in live.difference(&pinned) {
        fails.push(format!(
            "STALE PIN: Family S now asserts symbols in `{gained}`, which is not in \
             ASSERTED_MODULES. Add the row and restate item 3's dark-module number \
             (this is good news the pin has to record, or the claim goes stale)."
        ));
    }

    let dark = inventory_modules.saturating_sub(asserted.len());
    if dark > DARK_MODULE_CEILING {
        fails.push(format!(
            "REGRESSION: {dark} of {inventory_modules} stdlib modules are dark to Family S; \
             the ceiling is {DARK_MODULE_CEILING} (FAIL-ON-INCREASE). Cover the new module, \
             or raise the ceiling in the same commit with the reason."
        ));
    }

    for (module, want) in ITEM3_ASSERTED_COUNTS {
        let got = asserted.get(*module).copied().unwrap_or(0);
        if got != *want {
            fails.push(format!(
                "ITEM-3 MODULE `{module}`: Family S asserts {got} public symbols, pinned at \
                 {want}. Lower means the closure thinned out; higher means it grew and the \
                 pin must be raised in the same commit."
            ));
        }
    }
    fails
}

/// Print what Family S covers, against the SAME stdlib inventory the coverage
/// ledger uses ([`inventory`]), and RETURN A VERDICT.
///
/// One inventory, not two — v2 §5.3's denominator contract. A second,
/// hand-maintained symbol table would drift, and a coverage number computed
/// against a drifting denominator records a coin toss as a fact.
///
/// The report names the UNCOVERED symbols of every module the family touches.
/// That is the point: a family that imports 20 modules and asserts 300 things
/// about them is not "the stdlib covered", and the honest number is the one
/// that says so out loud.
///
/// The exit code is [`ratchet_failures`]. Until 2026-08-12 this function ended
/// `0` unconditionally and no workflow or script invoked it, so the number it
/// printed could regress in silence.
///
/// One inventory, not two — v2 §5.3's denominator contract. A second,
/// hand-maintained symbol table would drift, and a coverage number computed
/// against a drifting denominator records a coin toss as a fact.
///
/// The report names the UNCOVERED symbols of every module the family touches.
/// That is the point: a family that imports 20 modules and asserts 300 things
/// about them is not "the stdlib covered", and the honest number is the one
/// that says so out loud.
pub fn report(root: &std::path::Path) -> i32 {
    let inventory = match inventory(root) {
        Ok(i) => i,
        Err(e) => {
            eprintln!("corpus.stdlib: {e}");
            return 1;
        }
    };

    let covered = covered_symbols();
    println!("FAMILY S — stdlib coverage (against api/symbols.json, the ledger's own inventory)");
    println!();
    println!(
        "  {:<26} {:>9} {:>9} {:>7}",
        "module", "asserted", "public", "%"
    );
    println!("  {}", "-".repeat(56));

    let mut tot_cov = 0usize;
    let mut tot_pub = 0usize;
    let mut gaps: Vec<(String, Vec<String>)> = Vec::new();
    let mut modules: Vec<&str> = SURFACES.iter().map(|s| s.module).collect();
    for s in SURFACES {
        for m in s.also {
            modules.push(m.split_once(" as ").map(|(p, _)| p).unwrap_or(m));
        }
    }
    modules.sort();
    modules.dedup();

    for m in &modules {
        let public = inventory.get(*m).cloned().unwrap_or_default();
        let mine: std::collections::BTreeSet<&str> = covered
            .iter()
            .filter(|(mm, _)| mm == m)
            .map(|(_, s)| s.as_str())
            .collect();
        // Only count assertions against symbols the inventory agrees exist —
        // a tag naming a symbol that is not public would otherwise inflate the
        // numerator with something no user can call.
        let hit = public.iter().filter(|s| mine.contains(s.as_str())).count();
        tot_cov += hit;
        tot_pub += public.len();
        println!(
            "  {m:<26} {hit:>9} {:>9} {:>6.0}%",
            public.len(),
            if public.is_empty() {
                100.0
            } else {
                hit as f64 / public.len() as f64 * 100.0
            }
        );
        let missing: Vec<String> = public
            .iter()
            .filter(|s| !mine.contains(s.as_str()))
            .cloned()
            .collect();
        if !missing.is_empty() {
            gaps.push((m.to_string(), missing));
        }
    }

    println!("  {}", "-".repeat(56));
    println!(
        "  {:<26} {tot_cov:>9} {tot_pub:>9} {:>6.0}%",
        "TOTAL (touched modules)",
        if tot_pub == 0 {
            0.0
        } else {
            tot_cov as f64 / tot_pub as f64 * 100.0
        }
    );
    let all_modules = inventory.len();
    // ASSERTS, not "touches". This line used to divide by `modules.len()` — the
    // SURFACES list, which includes `also` imports (`Sky.Core.Task`, `Std.Html`,
    // `Std.Ui`) that no battery asserts anything about. Counting an import as
    // coverage understated the dark set by 3 and made the reported number
    // kinder than the truth. `ASSERTED_MODULES` is what the ratchet pins.
    let asserted_modules = asserted_per_module(&inventory);
    println!();
    println!(
        "  Family S ASSERTS a public symbol in {} of {all_modules} stdlib modules. The other \
         {} are dark to this family (ceiling {DARK_MODULE_CEILING}, FAIL-ON-INCREASE).",
        asserted_modules.len(),
        all_modules.saturating_sub(asserted_modules.len())
    );
    if !gaps.is_empty() {
        println!();
        println!("  ---- symbols with no Family-S assertion ----");
        for (m, missing) in &gaps {
            println!("  {m} ({}):", missing.len());
            println!("      {}", missing.join(", "));
        }
    }
    println!();
    println!(
        "  This number counts a symbol only when a battery item asserts a value \
         ABOUT it. Importing a module is not covering it."
    );

    // ---- the KERNEL surface, counted separately and never merged -----------
    //
    // Two inventories with two denominators, reported apart. Merging them would
    // dilute one number with the other's shape; omitting the kernel one is what
    // the ledger's own gap list called out, and it is what this section closes.
    let kernel = kernel_inventory();
    let mut k_cov = 0usize;
    let mut k_pub = 0usize;
    let mut k_gaps: Vec<(String, Vec<String>)> = Vec::new();
    println!();
    println!(
        "KERNEL pseudo-modules (hir::KERNEL_FUNCTIONS) — a SEPARATE denominator."
    );
    println!(
        "  These members are reachable in every Sky program and appear in NO"
    );
    println!(
        "  `api/symbols.json` entry: that manifest is built from `sky-stdlib/**.sky`"
    );
    println!(
        "  `exposing` lists alone. Reported here so the count is stated rather than"
    );
    println!("  silently dropped. NOT added to the stdlib totals above.");
    println!();
    println!("  {:<26} {:>9} {:>9} {:>7}", "pseudo-module", "asserted", "advertised", "%");
    println!("  {}", "-".repeat(56));
    for (pseudo, members) in &kernel {
        let key = format!("kernel:{pseudo}");
        let mine: std::collections::BTreeSet<&str> = covered
            .iter()
            .filter(|(mm, _)| *mm == key)
            .map(|(_, s)| s.as_str())
            .collect();
        let hit = members.iter().filter(|s| mine.contains(s.as_str())).count();
        k_cov += hit;
        k_pub += members.len();
        if hit > 0 {
            println!(
                "  {pseudo:<26} {hit:>9} {:>9} {:>6.0}%",
                members.len(),
                hit as f64 / members.len() as f64 * 100.0
            );
        }
        let missing: Vec<String> = members
            .iter()
            .filter(|s| !mine.contains(s.as_str()))
            .cloned()
            .collect();
        if !missing.is_empty() && hit > 0 {
            k_gaps.push((pseudo.clone(), missing));
        }
    }
    println!("  {}", "-".repeat(56));
    println!(
        "  {:<26} {k_cov:>9} {k_pub:>9} {:>6.0}%",
        "TOTAL (all kernel pseudos)",
        if k_pub == 0 {
            0.0
        } else {
            k_cov as f64 / k_pub as f64 * 100.0
        }
    );
    if !k_gaps.is_empty() {
        println!();
        println!("  ---- kernel members with no Family-S assertion, in a pseudo-module we touch ----");
        for (m, missing) in &k_gaps {
            println!("  {m} ({}):", missing.len());
            println!("      {}", missing.join(", "));
        }
    }

    // ---- the verdict --------------------------------------------------------
    //
    // This subcommand used to end `0` here regardless of every number above it,
    // and nothing in `.github/**` or `scripts/**` invoked it. It printed a
    // coverage claim that nothing could falsify. Now it has a verdict, and the
    // same verdict is asserted from `cargo test -p xtask` so CI carries it.
    let fails = ratchet_failures(&inventory);
    println!();
    if fails.is_empty() {
        println!(
            "corpus.stdlib: PASS — {} asserted module(s), {} dark (ceiling {DARK_MODULE_CEILING}), \
             item-3 module counts exact.",
            asserted_modules.len(),
            all_modules.saturating_sub(asserted_modules.len())
        );
        return 0;
    }
    eprintln!("corpus.stdlib: FAIL — the stdlib-coverage ratchet does not hold:");
    for f in &fails {
        eprintln!("  * {f}");
    }
    1
}

#[cfg(test)]
mod ratchet_tests {
    //! The CI face of the ratchet.
    //!
    //! `cargo test --workspace --exclude ty --locked` already runs on every push
    //! (`.github/workflows/rust-ci.yml`), so item 3's module number now fails a
    //! per-push job without adding a gate to the T1 tier or a second in the
    //! workflow to forget to add. The whole check is ~60 ms: the inventory comes
    //! from `project::render_doc_site_export` in process, not from a `sky`
    //! binary, so there is nothing to build and nothing to go stale.

    fn repo_root() -> std::path::PathBuf {
        // crates/xtask -> crates -> rust -> repo
        std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .ancestors()
            .nth(3)
            .expect("repo root from crates/xtask")
            .to_path_buf()
    }

    /// The pins hold: no module lost its assertions, the dark count did not
    /// rise, and the five modules item 3 named still assert exactly what they
    /// asserted when it was closed.
    #[test]
    fn stdlib_coverage_ratchet_holds() {
        let inv = super::inventory(&repo_root()).expect("stdlib inventory");
        let fails = super::ratchet_failures(&inv);
        assert!(
            fails.is_empty(),
            "the Family-S stdlib-coverage ratchet broke:\n  * {}",
            fails.join("\n  * ")
        );
    }

    /// The live tree's asserted picture, as [`super::check_pins`] sees it.
    fn live() -> std::collections::BTreeMap<String, usize> {
        let inv = super::inventory(&repo_root()).expect("stdlib inventory");
        super::asserted_per_module(&inv)
    }

    /// The inventory really does contain the 87 modules the dark count divides
    /// by, and the five item-3 modules are really in it.
    ///
    /// Without this, every arm below could be measuring an empty inventory and
    /// agreeing with itself.
    #[test]
    fn the_inventory_is_real() {
        let inv = super::inventory(&repo_root()).expect("stdlib inventory");
        assert!(
            inv.len() >= 80,
            "the stdlib inventory collapsed to {} modules — the denominator is wrong, \
             not the coverage",
            inv.len()
        );
        for (m, _) in super::ITEM3_ASSERTED_COUNTS {
            assert!(inv.contains_key(*m), "item-3 module `{m}` is not in the inventory");
        }
    }

    /// ARM 1 — a pinned module losing every assertion is a REGRESSION.
    #[test]
    fn losing_a_module_is_caught() {
        let mut a = live();
        assert!(a.remove("Std.Markdown").is_some(), "Std.Markdown must start asserted");
        let fails = super::check_pins(&a, 87);
        assert!(
            fails.iter().any(|f| f.contains("REGRESSION") && f.contains("Std.Markdown")),
            "a module that went dark must be reported, got: {fails:?}"
        );
    }

    /// ARM 2 — covering a module the pin does not list is a STALE PIN, so the
    /// dark number cannot silently improve either.
    #[test]
    fn covering_a_new_module_is_caught() {
        let mut a = live();
        a.insert("Std.Email".to_string(), 3);
        let fails = super::check_pins(&a, 87);
        assert!(
            fails.iter().any(|f| f.contains("STALE PIN") && f.contains("Std.Email")),
            "a newly covered module must demand its row, got: {fails:?}"
        );
    }

    /// ARM 3 — the dark ceiling is FAIL-ON-INCREASE.
    #[test]
    fn a_new_dark_module_is_caught() {
        let a = live();
        // One more uncovered module than the tree actually has: derived from the
        // real inventory so this arm does not rot when a module is added (adding
        // `Sky.Config` moved the total 87 → 88, which is exactly the ceiling
        // bump this arm must stay one ahead of).
        let one_more = super::inventory(&repo_root()).expect("stdlib inventory").len() + 1;
        let fails = super::check_pins(&a, one_more);
        assert!(
            fails.iter().any(|f| f.contains("dark to Family S")),
            "one more uncovered stdlib module must breach the ceiling, got: {fails:?}"
        );
    }

    /// ARM 4 — an item-3 module keeping its seat while its assertions are
    /// gutted is caught by the exact count, which [`super::ASSERTED_MODULES`]
    /// alone would miss.
    #[test]
    fn thinning_an_item3_module_is_caught() {
        let mut a = live();
        a.insert("Std.Codec".to_string(), 1);
        let fails = super::check_pins(&a, 87);
        assert!(
            fails.iter().any(|f| f.contains("ITEM-3 MODULE") && f.contains("Std.Codec")),
            "an item-3 module down to one assertion must be reported, got: {fails:?}"
        );
    }
}

// ---------------------------------------------------------------------------
// The batteries
// ---------------------------------------------------------------------------

/// The battery for one `(surface, edge)` point.
///
/// An empty battery means the point is **not admissible** — the surface has no
/// meaningful case in that edge class (there is no unicode edge for
/// `Sky.Core.Math`). `axes::admissible` drops those points so the manifest
/// records exactly the cases that exist, rather than padding the count with
/// vacuous ones.
pub fn battery(slug: &str, edge: &str) -> Vec<Check> {
    match slug {
        "string" => string_battery(edge),
        "list" => list_battery(edge),
        "dict" => dict_battery(edge),
        "set" => set_battery(edge),
        "maybe" => maybe_battery(edge),
        "result" => result_battery(edge),
        "char" => char_battery(edge),
        "encoding" => encoding_battery(edge),
        "crypto" => crypto_battery(edge),
        "secret" => secret_battery(edge),
        "math" => math_battery(edge),
        "basics" => basics_battery(edge),
        "tostring" => tostring_battery(edge),
        "path" => path_battery(edge),
        "error" => error_battery(edge),
        "decimal" => decimal_battery(edge),
        "money" => money_battery(edge),
        "csv" => csv_battery(edge),
        "regex" => regex_battery(edge),
        "json" => json_battery(edge),
        "bytes" => bytes_battery(edge),
        "jwt" => jwt_battery(edge),
        "codec" => codec_battery(edge),
        "markdown" => markdown_battery(edge),
        "compression" => compression_battery(edge),
        other => panic!("no battery for surface {other:?}"),
    }
}

// --- Sky.Core.Bytes --------------------------------------------------------
//
// The module's whole reason to exist is that `String.length` / `String.slice`
// are RUNE-based and a byte buffer needs BYTE semantics — its docstring says so
// in as many words ("`Bytes.length "世界"` is 6 bytes"). So the unicode edge is
// not decoration here: it is the only place the promise can be checked at all,
// and a `Bytes` that delegated to `String` would pass every ASCII case above it.
//
// Hex and base64 expectations are RFC 4648 / the hex encoding of UTF-8 code
// points — published constants, not observations.

fn bytes_battery(edge: &str) -> Vec<Check> {
    match edge {
        "nominal" => vec![
            i(&["Bytes.length"], "Bytes.length \"abc\"", 3),
            s(&["Bytes.toHex"], "Bytes.toHex \"abc\"", "616263"),
            ms(&["Bytes.fromHex"], "Bytes.fromHex \"616263\"", Some("abc")),
            // RFC 4648 §4: "Hello" is SGVsbG8= — five bytes, one pad char.
            s(&["Bytes.toBase64"], "Bytes.toBase64 \"Hello\"", "SGVsbG8="),
            ms(&["Bytes.fromBase64"], "Bytes.fromBase64 \"SGVsbG8=\"", Some("Hello")),
            s(&["Bytes.append"], "Bytes.append \"a\" \"b\"", "ab"),
            // End-exclusive, byte-indexed.
            s(&["Bytes.slice"], "Bytes.slice 1 3 \"abcd\"", "bc"),
            s(&["Bytes.fromString"], "Bytes.fromString \"xy\"", "xy"),
            ms(&["Bytes.toString"], "Bytes.toString \"abc\"", Some("abc")),
            bo(&["Bytes.isEmpty"], "Bytes.isEmpty \"a\"", false),
            // `fromHex` is documented case-insensitive; `toHex` is documented
            // lowercase. Both halves in one item.
            ms(
                &["Bytes.fromHex", "Bytes.toHex"],
                "Maybe.map Bytes.toHex (Bytes.fromHex \"ABC1\")",
                Some("abc1"),
            ),
        ],
        "empty" => vec![
            i(&["Bytes.length", "Bytes.empty"], "Bytes.length Bytes.empty", 0),
            bo(&["Bytes.isEmpty", "Bytes.empty"], "Bytes.isEmpty Bytes.empty", true),
            s(&["Bytes.toHex", "Bytes.empty"], "Bytes.toHex Bytes.empty", ""),
            s(&["Bytes.toBase64", "Bytes.empty"], "Bytes.toBase64 Bytes.empty", ""),
            ms(&["Bytes.fromHex"], "Bytes.fromHex \"\"", Some("")),
            ms(&["Bytes.fromBase64"], "Bytes.fromBase64 \"\"", Some("")),
            s(&["Bytes.append", "Bytes.empty"], "Bytes.append Bytes.empty \"a\"", "a"),
        ],
        "boundary" => vec![
            // Negative indices count from the end (the docstring's promise).
            s(&["Bytes.slice"], "Bytes.slice 1 -1 \"abcd\"", "bc"),
            s(&["Bytes.slice"], "Bytes.slice 0 0 \"abcd\"", ""),
            s(&["Bytes.slice"], "Bytes.slice 0 4 \"abcd\"", "abcd"),
            // hex and base64 are inverses, by the docstring, so a round trip
            // over a non-printable byte must be the identity.
            ms(
                &["Bytes.fromHex", "Bytes.toHex"],
                "Maybe.map Bytes.toHex (Bytes.fromHex \"00ff10\")",
                Some("00ff10"),
            ),
            ms(
                &["Bytes.fromBase64", "Bytes.toBase64"],
                "Maybe.map Bytes.toBase64 (Bytes.fromBase64 \"AP8Q\")",
                Some("AP8Q"),
            ),
        ],
        "unicode" => vec![
            // The load-bearing one. `String.length "世界"` is 2 (code points);
            // `Bytes.length` promises 6 (UTF-8 bytes). A `Bytes` that delegated
            // to `String` is red here and green everywhere else.
            i(&["Bytes.length"], "Bytes.length \"世界\"", 6),
            s(&["Bytes.toHex"], "Bytes.toHex \"世\"", "e4b896"),
            // Slicing on BYTE indices lands exactly on the first code point,
            // which `String.slice 0 3` would not.
            ms(&["Bytes.slice", "Bytes.toString"], "Bytes.toString (Bytes.slice 0 3 \"世界\")", Some("世")),
            i(&["Bytes.length", "Bytes.fromString"], "Bytes.length (Bytes.fromString \"🎉\")", 4),
        ],
        "failure" => vec![
            // Odd length, and a non-hex character.
            ms(&["Bytes.fromHex"], "Bytes.fromHex \"6\"", None),
            ms(&["Bytes.fromHex"], "Bytes.fromHex \"zz\"", None),
            ms(&["Bytes.fromBase64"], "Bytes.fromBase64 \"!!!!\"", None),
            // `toString` is Nothing on invalid UTF-8 — 0xFF is never a valid
            // lead byte. Constructed through `fromHex` so the case never has to
            // spell an invalid byte in Sky source.
            ms(
                &["Bytes.toString"],
                "Maybe.andThen Bytes.toString (Bytes.fromHex \"ff\")",
                None,
            ),
        ],
        _ => vec![],
    }
}

// --- Sky.Core.Jwt ----------------------------------------------------------
//
// The `nominal` battery asserts a COMPLETE HS256 TOKEN, byte for byte, and the
// bytes were computed outside this repository:
//
//   header  = {"alg":"HS256","typ":"JWT"}      (the shape `Jwt.encode` builds)
//   payload = {"sub":"u1"}                     (the shape `Jwt.subject` builds)
//   token   = b64url(header) "." b64url(payload) "." b64url(HMAC-SHA256(k, …))
//
// computed with `openssl dgst -sha256 -hmac k`. That makes it a genuine
// third-party oracle for `encode` — RFC 7515's algorithm, not the compiler's
// answer — which is the strongest form of expectation this corpus admits.
//
// `decode`'s time checks are asserted AT their boundaries, because the module's
// own source fixes which side of each is inclusive: `exp` fails when
// `now >= exp`, `nbf` fails when `now < nbf`.

/// The token the generator predicts, computed from RFC 7515 + RFC 4648 outside
/// this repository. See the battery's note.
const JWT_HS256_TOKEN: &str = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJ1MSJ9.\
                               _EcUsalm3HB8fiInqvnvLgcAJUDMbPwG8idbTrQ9n_0";

fn jwt_battery(edge: &str) -> Vec<Check> {
    match edge {
        "nominal" => vec![
            rs(
                &["Jwt.encode", "Jwt.hs256", "Jwt.claims", "Jwt.subject"],
                "Jwt.encode (Jwt.hs256 (Secret.unsafeFromString \"k\")) (Jwt.subject \"u1\" Jwt.claims)",
                Some(JWT_HS256_TOKEN),
            ),
            // `decode` hands back the VERIFIED payload JSON.
            rs(
                &["Jwt.decode"],
                "Jwt.decode (Jwt.hs256 (Secret.unsafeFromString \"k\")) 0 tok",
                Some("{\"sub\":\"u1\"}"),
            ),
            // The registered-claim builders, in the order `withClaim` appends.
            rs(
                &[
                    "Jwt.issuer",
                    "Jwt.audience",
                    "Jwt.issuedAt",
                    "Jwt.jwtId",
                    "Jwt.withClaim",
                ],
                "Jwt.decode (Jwt.hs256 (Secret.unsafeFromString \"k\")) 0 (signed (Jwt.claims |> Jwt.issuer \"iss1\" \
                 |> Jwt.audience \"aud1\" |> Jwt.issuedAt 5 |> Jwt.jwtId \"id1\" \
                 |> Jwt.withClaim \"role\" (Encode.string \"admin\")))",
                Some("{\"iss\":\"iss1\",\"aud\":\"aud1\",\"iat\":5,\"jti\":\"id1\",\"role\":\"admin\"}"),
            ),
        ],
        "empty" => vec![
            // No claims at all is a valid token with an empty JSON object
            // payload — not an error, and not `null`.
            rs(
                &["Jwt.encode", "Jwt.claims"],
                "Jwt.decode (Jwt.hs256 (Secret.unsafeFromString \"k\")) 0 (signed Jwt.claims)",
                Some("{}"),
            ),
            // An empty secret is a legal HMAC key.
            rs(
                &["Jwt.hs256"],
                "Jwt.decode (Jwt.hs256 (Secret.unsafeFromString \"\")) 0 (rok (Jwt.encode (Jwt.hs256 (Secret.unsafeFromString \"\")) Jwt.claims))",
                Some("{}"),
            ),
        ],
        "boundary" => vec![
            // `exp`: `now >= exp` is EXPIRED, so equality fails and one second
            // earlier passes. A `>` instead of `>=` passes the second and fails
            // the first.
            rs(
                &["Jwt.expiresAt", "Jwt.decode"],
                "Jwt.decode (Jwt.hs256 (Secret.unsafeFromString \"k\")) 99 (signed (Jwt.expiresAt 100 Jwt.claims))",
                Some("{\"exp\":100}"),
            ),
            rs(
                &["Jwt.expiresAt", "Jwt.decode"],
                "Jwt.decode (Jwt.hs256 (Secret.unsafeFromString \"k\")) 100 (signed (Jwt.expiresAt 100 Jwt.claims))",
                None,
            ),
            // `nbf`: `now < nbf` is NOT-YET-VALID, so equality PASSES.
            rs(
                &["Jwt.notBefore", "Jwt.decode"],
                "Jwt.decode (Jwt.hs256 (Secret.unsafeFromString \"k\")) 100 (signed (Jwt.notBefore 100 Jwt.claims))",
                Some("{\"nbf\":100}"),
            ),
            rs(
                &["Jwt.notBefore", "Jwt.decode"],
                "Jwt.decode (Jwt.hs256 (Secret.unsafeFromString \"k\")) 99 (signed (Jwt.notBefore 100 Jwt.claims))",
                None,
            ),
        ],
        "unicode" => vec![
            // base64url is byte-oriented, so a multi-byte claim value must
            // survive the round trip exactly.
            rs(
                &["Jwt.subject", "Jwt.decode"],
                "Jwt.decode (Jwt.hs256 (Secret.unsafeFromString \"k\")) 0 (signed (Jwt.subject \"世界\" Jwt.claims))",
                Some("{\"sub\":\"世界\"}"),
            ),
        ],
        "failure" => vec![
            // The security-relevant branches. Each must be `Err`, and a token
            // verifier that returned `Ok` on any of them is a vulnerability
            // rather than a bug.
            rs(&["Jwt.decode"], "Jwt.decode (Jwt.hs256 (Secret.unsafeFromString \"wrong\")) 0 tok", None),
            rs(&["Jwt.decode"], "Jwt.decode (Jwt.hs256 (Secret.unsafeFromString \"k\")) 0 (tok ++ \"x\")", None),
            rs(&["Jwt.decode"], "Jwt.decode (Jwt.hs256 (Secret.unsafeFromString \"k\")) 0 \"abc\"", None),
            rs(&["Jwt.decode"], "Jwt.decode (Jwt.hs256 (Secret.unsafeFromString \"k\")) 0 \"\"", None),
            // RS256 with a key that is not a PEM cannot sign.
            rs(&["Jwt.encode", "Jwt.rs256"], "Jwt.encode (Jwt.rs256 (Secret.unsafeFromString \"not-a-pem\")) Jwt.claims", None),
        ],
        _ => vec![],
    }
}

// --- Std.Codec -------------------------------------------------------------
//
// Every expectation is JSON (RFC 8259) or the module's own written promise:
// `auto` snake_cases a column name and `autoCamel` "keeps camelCase … priceMinor
// stays", `maybe`'s `Nothing` "encodes as JSON null", `map` adapts "via a
// bijection (`to` on decode, `from` on encode)", `fromJsonSafe` rejects "input
// longer than maxChars BEFORE parsing".

fn codec_battery(edge: &str) -> Vec<Check> {
    match edge {
        "nominal" => vec![
            s(&["Codec.toJson", "Codec.int"], "Codec.toJson Codec.int 5", "5"),
            s(&["Codec.toJson", "Codec.string"], "Codec.toJson Codec.string \"a\"", "\"a\""),
            s(&["Codec.bool"], "Codec.toJson Codec.bool True", "true"),
            s(&["Codec.float"], "Codec.toJson Codec.float 1.5", "1.5"),
            s(&["Codec.list"], "Codec.toJson (Codec.list Codec.int) [ 1, 2 ]", "[1,2]"),
            s(&["Codec.maybe"], "Codec.toJson (Codec.maybe Codec.int) (Just 3)", "3"),
            ri(&["Codec.fromJson"], "Codec.fromJson Codec.int \"5\"", Some(5)),
            // The record path: object / field / buildObject.
            s(
                &["Codec.object", "Codec.field", "Codec.buildObject"],
                "Codec.toJson pcodec { name = \"x\", priceMinor = 9 }",
                "{\"name\":\"x\",\"priceMinor\":9}",
            ),
            rs(
                &["Codec.fromJson", "Codec.buildObject"],
                "Result.map (\\p -> p.name ++ \"/\" ++ String.fromInt p.priceMinor) \
                 (Codec.fromJson pcodec \"{\\\"name\\\":\\\"y\\\",\\\"priceMinor\\\":4}\")",
                Some("y/4"),
            ),
            // A nullary enum is stored as its readable TEXT name.
            s(&["Codec.enum"], "Codec.toJson colourCodec Blue", "\"blue\""),
            // `map`'s bijection: `from` runs on ENCODE, so 5 encodes as 4.
            s(
                &["Codec.map"],
                "Codec.toJson (Codec.map (\\n -> n + 1) (\\n -> n - 1) Codec.int) 5",
                "4",
            ),
            // `toValue` is the encoder as a plain function.
            s(&["Codec.toValue"], "Encode.encode 0 (Codec.toValue Codec.int 7)", "7"),
        ],
        "empty" => vec![
            s(&["Codec.list"], "Codec.toJson (Codec.list Codec.int) emptyInts", "[]"),
            s(&["Codec.string"], "Codec.toJson Codec.string \"\"", "\"\""),
            // `Nothing` is JSON null, not an omitted key and not "".
            s(&["Codec.maybe"], "Codec.toJson (Codec.maybe Codec.int) Nothing", "null"),
        ],
        "boundary" => vec![
            // `fromJsonSafe` compares `String.length s > maxChars`, so an input
            // of EXACTLY `maxChars` is accepted and one over is not. Both sides
            // of the inequality, because an off-by-one here silently changes a
            // DoS guard.
            ri(&["Codec.fromJsonSafe"], "Codec.fromJsonSafe 1 Codec.int \"5\"", Some(5)),
            ri(&["Codec.fromJsonSafe"], "Codec.fromJsonSafe 0 Codec.int \"5\"", None),
            // `auto` derives snake_case column / key names; `autoCamel` keeps
            // the camelCase spelling. The two differ on exactly one field, so
            // this pair is what distinguishes them.
            s(
                &["Codec.auto"],
                "Codec.toJson (Codec.auto blank) { name = \"x\", priceMinor = 9 }",
                "{\"name\":\"x\",\"price_minor\":9}",
            ),
            s(
                &["Codec.autoCamel"],
                "Codec.toJson (Codec.autoCamel blank) { name = \"x\", priceMinor = 9 }",
                "{\"name\":\"x\",\"priceMinor\":9}",
            ),
            // `shape` is what the DB backend derives columns from — a record
            // codec must report its columns in declaration order, a scalar must
            // report its column TYPE.
            ls(
                &["Codec.shape"],
                "List.map fst (recordCols (Codec.shape pcodec))",
                "name,priceMinor",
            ),
            s(&["Codec.shape", "Codec.int"], "scalarTag (Codec.shape Codec.int)", "int"),
        ],
        "unicode" => vec![
            s(&["Codec.toJson", "Codec.string"], "Codec.toJson Codec.string \"世界\"", "\"世界\""),
            rs(
                &["Codec.fromJson", "Codec.string"],
                "Codec.fromJson Codec.string \"\\\"世界\\\"\"",
                Some("世界"),
            ),
        ],
        "failure" => vec![
            // A type mismatch is an `Err`, never a zero value.
            ri(&["Codec.fromJson", "Codec.int"], "Codec.fromJson Codec.int \"\\\"x\\\"\"", None),
            ri(&["Codec.fromJson"], "Codec.fromJson Codec.int \"{\"", None),
            // A missing record field is an `Err`, not a default.
            rs(
                &["Codec.fromJson", "Codec.field"],
                "Result.map (\\p -> p.name) (Codec.fromJson pcodec \"{\\\"name\\\":\\\"y\\\"}\")",
                None,
            ),
            // An enum name outside the table is an `Err`, not the first variant.
            rs(
                &["Codec.enum"],
                "Result.map colourName (Codec.fromJson colourCodec \"\\\"green\\\"\")",
                None,
            ),
        ],
        _ => vec![],
    }
}

// --- Std.Markdown ----------------------------------------------------------
//
// `render : String -> Element msg`, so the case folds the tree with its own
// walk over `Std.Ui`'s exposed constructors. Two things become assertable that
// way, and the second is the important one:
//
//   `mdText`  — the text content, in order. Enough to say a marker was CONSUMED
//               (`# Title` renders "Title", not "# Title").
//   `mdRaw`   — the number of `Raw` nodes. The module's headline promise is
//               *"the parser never emits raw HTML, scripts, or event handlers …
//               safe to feed UNTRUSTED markdown"*, and `Ui.Raw` is the ONLY
//               constructor that could carry any. Counting it is that promise,
//               stated as a number.
//
// The `failure` edge is where "thin" is made concrete: the module documents
// blockquotes and images as unsupported, and this battery pins what they
// actually do instead — a `> quote` keeps its marker as literal text, an
// `![alt](img.png)` renders as `!` plus the link text. Those are the DECLARED
// gaps, asserted so they cannot change silently.

fn markdown_battery(edge: &str) -> Vec<Check> {
    match edge {
        "nominal" => vec![
            s(&["Markdown.render"], "mdText \"hello\"", "hello"),
            // Every header level consumes its marker.
            s(&["Markdown.render"], "mdText \"# Title\"", "Title"),
            s(&["Markdown.render"], "mdText \"###### H6\"", "H6"),
            s(&["Markdown.render"], "mdText \"**bold**\"", "bold"),
            s(&["Markdown.render"], "mdText \"*it*\"", "it"),
            s(&["Markdown.render"], "mdText \"`code`\"", "code"),
            // A link renders its TEXT, never its URL.
            s(&["Markdown.render"], "mdText \"[txt](http://x)\"", "txt"),
            s(&["Markdown.render"], "mdText \"```\\nfn x\\n```\"", "fn x"),
            s(&["Markdown.renderInline"], "mdInline \"**b** and `c`\"", "b and c"),
        ],
        "empty" => vec![
            s(&["Markdown.render"], "mdText \"\"", ""),
            i(&["Markdown.render"], "mdRaw \"\"", 0),
            s(&["Markdown.renderInline"], "mdInline \"\"", ""),
            // A horizontal rule is a block with no text.
            s(&["Markdown.render"], "mdText \"---\"", ""),
        ],
        "boundary" => vec![
            // Bullet and ordered lists keep their items in order, with the
            // marker replaced by the renderer's own glyph.
            s(&["Markdown.render"], "mdText \"- a\\n- b\"", "•a•b"),
            s(&["Markdown.render"], "mdText \"1. a\\n2. b\"", "1.a2.b"),
            // Tables ARE rendered — cells in row-major order. The module's
            // docstring said they were "deliberately not supported in v1" while
            // `Block` carried a `TableBlock` and the parser handled it; the
            // docstring is corrected in the same commit as this case.
            s(
                &["Markdown.render"],
                "mdText \"| a | b |\\n| --- | --- |\\n| 1 | 2 |\"",
                "ab12",
            ),
            // ---- constructs that were DECLARED unsupported and now are not --
            //
            // A blockquote consumes its `>` marker (it used to survive as
            // literal text) and its lines join like any wrapped paragraph.
            s(&["Markdown.render"], "mdText \"> quote\"", "quote"),
            s(&["Markdown.render"], "mdText \"> a\\n> b\"", "a b"),
            // Inline markup inside a quote is parsed, so the quote is a block,
            // not an escaped string.
            s(&["Markdown.render"], "mdText \"> **q**\"", "q"),
            // An image is an `img`, so it contributes NO text node — the `!alt`
            // fallback text is gone. The `src` and `alt` are asserted on the
            // rendered HTML, which is where an image is observable at all.
            s(&["Markdown.render"], "mdText \"![alt](img.png)\"", ""),
            bo(
                &["Markdown.render"],
                "String.contains \"src=\\\"img.png\\\"\" (mdHtml \"![alt](img.png)\")",
                true,
            ),
            bo(
                &["Markdown.render"],
                "String.contains \"alt=\\\"alt\\\"\" (mdHtml \"![alt](img.png)\")",
                true,
            ),
            // A malformed image degrades to text rather than swallowing the
            // line — the `!` is kept and the rest parses as ordinary content.
            s(&["Markdown.render"], "mdText \"![alt](broken\"", "![alt](broken"),
            // An ordered list past the tenth item. The prefix test used to be
            // equality against `"1. "`…`"10. "`, so an eleventh item silently
            // became a paragraph mid-list.
            s(
                &["Markdown.render"],
                "mdText \"9. i\\n10. j\\n11. k\"",
                "1.i2.j3.k",
            ),
            // A rule is a RUN of 3+, not one of five hard-coded strings —
            // `------` used to render as a paragraph.
            s(&["Markdown.render"], "mdText \"------\"", ""),
            s(&["Markdown.render"], "mdText \"****\"", ""),
        ],
        "unicode" => vec![
            s(&["Markdown.render"], "mdText \"世界 **粗体**\"", "世界 粗体"),
            i(&["Markdown.render"], "mdRaw \"世界 **粗体**\"", 0),
        ],
        "failure" => vec![
            // **The security promise, as a number.** Untrusted markdown must
            // produce ZERO `Raw` nodes — `Ui.Raw` is the only constructor that
            // can carry unescaped HTML — and the script must survive as literal
            // TEXT, which `Std.Ui` escapes on render.
            i(&["Markdown.render"], "mdRaw \"<script>alert(1)</script>\"", 0),
            s(
                &["Markdown.render"],
                "mdText \"<script>alert(1)</script>\"",
                "<script>alert(1)</script>",
            ),
            i(&["Markdown.render"], "mdRaw \"[x](javascript:alert(1))\"", 0),
            // **The href half of the same promise, and it did NOT hold.**
            // Zero `Raw` nodes is necessary and not sufficient: a link's URL
            // becomes an `href` attribute, and a `javascript:` URL executes on
            // navigation without needing a single metacharacter — so
            // HTML-escaping, which is all `Std.Ui` had, leaves it intact.
            // `Markdown.render "[x](javascript:alert(1))"` emitted
            // `<a href="javascript:alert(1)">` verbatim against a docstring
            // that says "safe to feed UNTRUSTED markdown … no
            // bluemonday-equivalent sanitiser needed".
            //
            // Neutralised in the RUNTIME, at the one place every attribute
            // enters a `VNode` (`rt.SafeAttrURL`), so `Std.Ui`, `Std.Html` and
            // `Std.Markdown` are covered by one guard on both the server-render
            // and the diff/patch path. The assertion is therefore made on the
            // RENDERED HTML — which is the level the promise is about, and the
            // only level that can see a fix living in the renderer.
            //
            // The casing / whitespace variants are here because a browser
            // normalises a URL before resolving its scheme, and a naive prefix
            // test catches none of them.
            bo(&["Markdown.render"], "hasBlank \"[x](javascript:alert(1))\"", true),
            bo(&["Markdown.render"], "hasBlank \"[x](JaVaScRiPt:alert(1))\"", true),
            bo(&["Markdown.render"], "hasBlank \"[x](  javascript:alert(1))\"", true),
            bo(&["Markdown.render"], "hasBlank \"[x](data:text/html,hello)\"", true),
            // The complementary assertion: the payload is GONE, not merely
            // accompanied by an `about:blank` somewhere else in the document.
            bo(&["Markdown.render"], "hasScript \"[x](javascript:alert(1))\"", false),
            bo(&["Markdown.render"], "hasScript \"[x](java\\tscript:alert(1))\"", false),
            // …and an ordinary link is untouched, which is what stops the
            // guard from being a blanket "block every URL" that would pass
            // every assertion above while breaking every app.
            bo(&["Markdown.render"], "hasBlank \"[x](https://ok.example/p)\"", false),
            bo(
                &["Markdown.render"],
                "String.contains \"href=\\\"https://ok.example/p\\\"\" (mdHtml \"[x](https://ok.example/p)\")",
                true,
            ),
            // An image's URL is a URL-bearing attribute too, so the SAME guard
            // must cover it. It does — `src` is on `urlBearingAttr`'s allowlist
            // — but images were not parsed when that guard was written, so
            // nothing had ever asserted it. Adding the construct adds the
            // obligation.
            //
            // `hasBlankSrc`, not `hasBlank`: an image renders `src=`, and
            // `hasBlank` only looks at `href=`, so it answers False here whether
            // the URL was neutralised OR the image was dropped entirely. That
            // distinction is the whole assertion.
            bo(&["Markdown.render"], "hasBlankSrc \"![x](javascript:alert(1))\"", true),
            bo(&["Markdown.render"], "hasScript \"![x](javascript:alert(1))\"", false),
            // `data:image/…` is the one `data:` form the guard permits, and an
            // inline image is exactly why. Blanket-blocking it would pass every
            // assertion above while breaking the feature — so this asserts the
            // src SURVIVES, not merely that it is not blanked.
            bo(
                &["Markdown.render"],
                "hasBlankSrc \"![x](data:image/png;base64,iVBOR)\"",
                false,
            ),
            bo(
                &["Markdown.render"],
                "String.contains \"src=\\\"data:image/png;base64,iVBOR\\\"\" \
                 (mdHtml \"![x](data:image/png;base64,iVBOR)\")",
                true,
            ),
        ],
        _ => vec![],
    }
}

// --- Std.Compression -------------------------------------------------------
//
// Every operation is `Task Error String`, which is why this module was dark:
// `checkValue : String` cannot hold a `Task`. `Task.run : Task e a -> Result e a`
// is the bridge, and gzip / zstd are deterministic, so the `Result` carries a
// value the generator can predict.
//
// Two kinds of expectation, both published:
//
//   * The CONTAINER HEADER. RFC 1952 §2.3.1 fixes gzip's first bytes as
//     `1f 8b` (magic) then `08` (deflate); RFC 8478 §3.1.1 fixes zstd's frame
//     magic as `0xFD2FB528` little-endian, i.e. `28 b5 2f fd`. Neither comes
//     from this repository.
//   * The IDENTITY. `gunzip . gzip == id` is what a compressor means; it needs
//     no oracle at all, and it is the assertion that catches a codec that
//     round-trips through the wrong window size or drops the final block.

fn compression_battery(edge: &str) -> Vec<Check> {
    match edge {
        "nominal" => vec![
            // RFC 1952 §2.3.1: ID1=0x1f, ID2=0x8b, CM=8 (deflate).
            s(&["Compression.gzip"], "headOf (Compression.gzip \"hello\")", "1f8b08"),
            s(
                &["Compression.gzip", "Compression.gunzip"],
                "runOf (Task.andThen Compression.gunzip (Compression.gzip \"hello\"))",
                "hello",
            ),
            // RFC 8478 §3.1.1: frame magic 0xFD2FB528, little-endian.
            s(
                &["Compression.zstdCompress"],
                "headOf (Compression.zstdCompress \"hello\")",
                "28b52f",
            ),
            s(
                &["Compression.zstdCompress", "Compression.zstdDecompress"],
                "runOf (Task.andThen Compression.zstdDecompress (Compression.zstdCompress \"hello\"))",
                "hello",
            ),
        ],
        "empty" => vec![
            // The empty input is a real gzip stream, not an error and not "".
            s(&["Compression.gzip"], "headOf (Compression.gzip \"\")", "1f8b08"),
            s(
                &["Compression.gzip", "Compression.gunzip"],
                "runOf (Task.andThen Compression.gunzip (Compression.gzip \"\"))",
                "",
            ),
            s(
                &["Compression.zstdCompress", "Compression.zstdDecompress"],
                "runOf (Task.andThen Compression.zstdDecompress (Compression.zstdCompress \"\"))",
                "",
            ),
        ],
        "boundary" => vec![
            // A highly compressible input is the case a broken window size or a
            // dropped final block shows up in; the identity must still hold.
            s(
                &["Compression.gzip", "Compression.gunzip"],
                "runOf (Task.andThen Compression.gunzip (Compression.gzip (String.repeat 500 \"ab\")))",
                "1000",
            ),
            s(
                &["Compression.zstdCompress", "Compression.zstdDecompress"],
                "runOf (Task.andThen Compression.zstdDecompress (Compression.zstdCompress (String.repeat 500 \"ab\")))",
                "1000",
            ),
        ],
        "unicode" => vec![
            // gzip and zstd are BYTE codecs, so multi-byte code points must
            // survive unchanged rather than being re-encoded.
            s(
                &["Compression.gzip", "Compression.gunzip"],
                "runOf (Task.andThen Compression.gunzip (Compression.gzip \"世界🎉\"))",
                "世界🎉",
            ),
            s(
                &["Compression.zstdCompress", "Compression.zstdDecompress"],
                "runOf (Task.andThen Compression.zstdDecompress (Compression.zstdCompress \"世界🎉\"))",
                "世界🎉",
            ),
        ],
        "failure" => vec![
            // Input that is not a container at all must FAIL, not return the
            // input unchanged and not panic.
            s(&["Compression.gunzip"], "runOf (Compression.gunzip \"not gzip at all\")", "E"),
            s(&["Compression.zstdDecompress"], "runOf (Compression.zstdDecompress \"nope\")", "E"),
            s(&["Compression.gunzip"], "runOf (Compression.gunzip \"\")", "E"),
            // A TRUNCATED gzip stream — a real header, then nothing. The
            // docstring promises `gunzip` "fails on truncated" input, and a
            // decoder that stopped at the header would return "" instead.
            s(
                &["Compression.gunzip"],
                "runOf (Task.andThen (\\b -> Compression.gunzip (Bytes.slice 0 6 b)) (Compression.gzip \"hello\"))",
                "E",
            ),
        ],
        _ => vec![],
    }
}

// --- Sky.Core.String -------------------------------------------------------
//
// Rune-vs-byte is the load-bearing distinction here and it is why the `unicode`
// edge exists at all: `String.length` promises code points. A byte-counting
// regression passes every ASCII case in the `nominal` battery and fails only
// here.

fn string_battery(edge: &str) -> Vec<Check> {
    match edge {
        "nominal" => vec![
            s(&["String.toUpper"], "String.toUpper \"ab\"", "AB"),
            s(&["String.toLower"], "String.toLower \"AB\"", "ab"),
            s(&["String.reverse"], "String.reverse \"abc\"", "cba"),
            i(&["String.length"], "String.length \"abc\"", 3),
            s(&["String.append"], "String.append \"a\" \"b\"", "ab"),
            s(&["String.concat"], "String.concat [ \"a\", \"b\" ]", "ab"),
            s(&["String.join"], "String.join \"-\" [ \"a\", \"b\" ]", "a-b"),
            ls(&["String.split"], "String.split \",\" \"a,b\"", "a,b"),
            s(&["String.replace"], "String.replace \"a\" \"b\" \"aa\"", "bb"),
            s(&["String.slice"], "String.slice 1 3 \"abcde\"", "bc"),
            s(&["String.trim"], "String.trim \"  a  \"", "a"),
            s(&["String.trimStart"], "String.trimStart \"  a\"", "a"),
            s(&["String.trimEnd"], "String.trimEnd \"a  \"", "a"),
            s(&["String.repeat"], "String.repeat 3 \"a\"", "aaa"),
            s(&["String.padLeft"], "String.padLeft 3 '0' \"7\"", "007"),
            s(&["String.padRight"], "String.padRight 3 '.' \"a\"", "a.."),
            s(&["String.dropLeft"], "String.dropLeft 1 \"abc\"", "bc"),
            s(&["String.dropRight"], "String.dropRight 1 \"abc\"", "ab"),
            s(&["String.fromInt"], "String.fromInt 42", "42"),
            s(&["String.fromChar"], "String.fromChar 'a'", "a"),
            s(&["String.fromList"], "String.fromList (String.toList \"ab\")", "ab"),
            ln(&["String.toList"], "String.toList \"abc\"", 3),
            ln(&["String.words"], "String.words \"a b c\"", 3),
            ln(&["String.lines"], "String.lines \"a\\nb\"", 2),
            mi(&["String.toInt"], "String.toInt \"42\"", Some(42)),
            bo(&["String.isEmpty"], "String.isEmpty \"a\"", false),
            bo(&["String.contains"], "String.contains \"b\" \"abc\"", true),
            bo(&["String.startsWith"], "String.startsWith \"a\" \"abc\"", true),
            bo(&["String.endsWith"], "String.endsWith \"c\" \"abc\"", true),
            // The haystack-first pipeline companions. Their docstrings state
            // the exact equivalence, so the expected value is the promise.
            bo(&["String.containsIn"], "String.containsIn \"hello world\" \"world\"", true),
            bo(&["String.startsWithIn"], "String.startsWithIn \"/api/users\" \"/api\"", true),
            bo(&["String.endsWithIn"], "String.endsWithIn \"image.png\" \".png\"", true),
            bo(&["String.equalFold"], "String.equalFold \"AB\" \"ab\"", true),
            s(&["String.casefold"], "String.casefold \"AB\"", "ab"),
            bo(&["String.isEmail"], "String.isEmail \"a@b.com\"", true),
            bo(&["String.isUrl"], "String.isUrl \"https://example.com\"", true),
            f(&["String.fromFloat"], "1.5", "1.5"),
            f(&["String.toFloat"], "Maybe.withDefault 0.0 (String.toFloat \"1.5\")", "1.5"),
        ],
        // Elm's answers on the empty string, which Sky's surface copies.
        "empty" => vec![
            i(&["String.length"], "String.length \"\"", 0),
            bo(&["String.isEmpty"], "String.isEmpty \"\"", true),
            s(&["String.reverse"], "String.reverse \"\"", ""),
            s(&["String.toUpper"], "String.toUpper \"\"", ""),
            s(&["String.trim"], "String.trim \"\"", ""),
            s(&["String.concat"], "String.concat []", ""),
            s(&["String.join"], "String.join \",\" []", ""),
            s(&["String.repeat"], "String.repeat 0 \"a\"", ""),
            s(&["String.slice"], "String.slice 0 0 \"abc\"", ""),
            // `String.split sep ""` is one empty piece, not zero pieces.
            ln(&["String.split"], "String.split \",\" \"\"", 1),
            // `String.words ""` is `[]`; `String.lines ""` is `[\"\"]`. The two
            // differ, and that difference is exactly the kind of thing an
            // ASCII-happy-path suite never notices.
            ln(&["String.words"], "String.words \"\"", 0),
            ln(&["String.lines"], "String.lines \"\"", 1),
            ln(&["String.toList"], "String.toList \"\"", 0),
            s(&["String.fromList"], "String.fromList []", ""),
            // The empty substring is contained in, and prefixes, everything.
            bo(&["String.contains"], "String.contains \"\" \"abc\"", true),
            bo(&["String.startsWith"], "String.startsWith \"\" \"abc\"", true),
            bo(&["String.endsWith"], "String.endsWith \"\" \"abc\"", true),
        ],
        // Negative and oversized counts. `dropLeft`/`dropRight` are the two
        // functions in this module that WRITE their edge contract down, so
        // these two assertions are the module's own promise quoted back.
        "boundary" => vec![
            s(&["String.dropLeft"], "String.dropLeft 99 \"abc\"", ""),
            s(&["String.dropLeft"], "String.dropLeft -1 \"abc\"", "abc"),
            s(&["String.dropRight"], "String.dropRight 99 \"abc\"", ""),
            s(&["String.dropRight"], "String.dropRight -1 \"abc\"", "abc"),
            s(&["String.repeat"], "String.repeat -1 \"a\"", ""),
            // padLeft/padRight never truncate.
            s(&["String.padLeft"], "String.padLeft 2 '0' \"abc\"", "abc"),
            s(&["String.padRight"], "String.padRight 2 '0' \"abc\"", "abc"),
            // slice: the end index clamps to the length; start past end is "".
            s(&["String.slice"], "String.slice 0 99 \"abc\"", "abc"),
            s(&["String.slice"], "String.slice 2 1 \"abc\"", ""),
            s(&["String.slice"], "String.slice 0 0 \"abc\"", ""),
            // Int is 64-bit: a value past the 32-bit boundary round-trips.
            mi(&["String.toInt"], "String.toInt \"2147483648\"", Some(2147483648)),
            s(&["String.fromInt"], "String.fromInt -7", "-7"),
        ],
        // `String` is a sequence of CODE POINTS. Every assertion here is a
        // byte-vs-rune trap.
        "unicode" => vec![
            i(&["String.length"], "String.length \"世界\"", 2),
            i(&["String.length"], "String.length \"🎉\"", 1),
            s(&["String.toUpper"], "String.toUpper \"é\"", "É"),
            s(&["String.toLower"], "String.toLower \"É\"", "é"),
            s(&["String.reverse"], "String.reverse \"abé\"", "éba"),
            s(&["String.slice"], "String.slice 0 1 \"世界\"", "世"),
            ln(&["String.toList"], "String.toList \"世界\"", 2),
            s(&["String.fromChar"], "String.fromChar '世'", "世"),
            bo(&["String.contains"], "String.contains \"界\" \"世界\"", true),
            bo(&["String.equalFold"], "String.equalFold \"É\" \"é\"", true),
            s(&["String.padLeft"], "String.padLeft 3 'x' \"世\"", "xx世"),
        ],
        // The failure branch of everything that has one, plus the two
        // validators whose whole job is to say no.
        "failure" => vec![
            mi(&["String.toInt"], "String.toInt \"abc\"", None),
            mi(&["String.toInt"], "String.toInt \"\"", None),
            mi(&["String.toInt"], "String.toInt \"1.5\"", None),
            f(&["String.toFloat"], "Maybe.withDefault -1.0 (String.toFloat \"abc\")", "-1"),
            bo(&["String.isEmail"], "String.isEmail \"not-an-email\"", false),
            bo(&["String.isEmail"], "String.isEmail \"\"", false),
            // A `javascript:` URL must not validate — this one is a security
            // boundary, not a nicety.
            bo(&["String.isUrl"], "String.isUrl \"javascript:alert(1)\"", false),
            bo(&["String.isUrl"], "String.isUrl \"/relative/path\"", false),
            bo(&["String.isUrl"], "String.isUrl \"\"", false),
        ],
        _ => vec![],
    }
}

// --- Sky.Core.List ---------------------------------------------------------

fn list_battery(edge: &str) -> Vec<Check> {
    match edge {
        "nominal" => vec![
            i(&["List.length"], "List.length [ 1, 2, 3 ]", 3),
            li(&["List.reverse"], "List.reverse [ 1, 2, 3 ]", "3,2,1"),
            li(&["List.take"], "List.take 2 [ 1, 2, 3 ]", "1,2"),
            li(&["List.drop"], "List.drop 1 [ 1, 2, 3 ]", "2,3"),
            li(&["List.append"], "List.append [ 1 ] [ 2 ]", "1,2"),
            li(&["List.concat"], "List.concat [ [ 1 ], [ 2, 3 ] ]", "1,2,3"),
            li(&["List.range"], "List.range 1 3", "1,2,3"),
            li(&["List.map"], "List.map (\\x -> x * 2) [ 1, 2 ]", "2,4"),
            li(&["List.filter"], "List.filter (\\x -> x > 1) [ 1, 2, 3 ]", "2,3"),
            li(&["List.cons"], "List.cons 0 [ 1 ]", "0,1"),
            li(&["List.concatMap"], "List.concatMap (\\x -> [ x, x ]) [ 1 ]", "1,1"),
            // `indexedMap` passes INDEX first, element second.
            li(&["List.indexedMap"], "List.indexedMap (\\ix x -> ix + x) [ 10, 20 ]", "10,21"),
            // `foldl`/`foldr` both call `fn element acc`. `foldr` on
            // subtraction distinguishes the two directions: 1-(2-(3-0)) = 2.
            i(&["List.foldl"], "List.foldl (\\x acc -> x + acc) 0 [ 1, 2, 3 ]", 6),
            i(&["List.foldr"], "List.foldr (\\x acc -> x - acc) 0 [ 1, 2, 3 ]", 2),
            bo(&["List.member"], "List.member 2 [ 1, 2 ]", true),
            bo(&["List.any"], "List.any (\\x -> x > 2) [ 1, 3 ]", true),
            bo(&["List.all"], "List.all (\\x -> x > 0) [ 1, 3 ]", true),
            bo(&["List.isEmpty"], "List.isEmpty [ 1 ]", false),
            mi(&["List.find"], "List.find (\\x -> x > 1) [ 1, 2, 3 ]", Some(2)),
            mi(&["List.head"], "List.head [ 7, 8 ]", Some(7)),
            ln(&["List.zip"], "List.zip [ 1, 2 ] [ 3, 4 ]", 2),
            // ---- the ORDERING surface -------------------------------------
            //
            // `List.sort` / `List.sortBy` / `List.sortWith` had ZERO cases in
            // this battery, in any edge class, at any element type — and no
            // `.sky` signature either, so nothing anywhere asserted an order.
            // `List.sort [ 10, 9, 2 ]` returned `10, 2, 9`: it compared
            // `fmt.Sprintf("%v", …)`, i.e. it sorted the RENDERING rather than
            // the value. `List String` was correct, which is exactly why it
            // survived — the rendering of a string IS the string, the same
            // reason every `String`-keyed `Dict` assertion passed through #174.
            //
            // So the element type is an axis of BEHAVIOUR for the ordering ops,
            // and the multi-digit set below is what tells lexical from ordinal.
            li(&["Kernel.List.sort"], "List.sort [ 10, 9, 2 ]", "2,9,10"),
            ls(&["Kernel.List.sort"], "List.sort [ \"b\", \"a\", \"c\" ]", "a,b,c"),
            li(&["Kernel.List.sortBy"], "List.sortBy identity [ 10, 9, 2 ]", "2,9,10"),
            // A projection to a DIFFERENT type than the element: sort strings
            // by their length, which is the shape `sortBy` exists for.
            ls(
                &["Kernel.List.sortBy"],
                "List.sortBy String.length [ \"ccc\", \"a\", \"bb\" ]",
                "a,bb,ccc",
            ),
            // `sortWith` takes the caller's comparator, so it was always
            // correct — it is here so the fix is pinned as not having changed
            // the one ordering entry point that never used the rendering.
            li(
                &["Kernel.List.sortWith"],
                "List.sortWith (\\a b -> b - a) [ 2, 10, 9 ]",
                "10,9,2",
            ),
        ],
        "empty" => vec![
            i(&["List.length"], "List.length emptyInts", 0),
            bo(&["List.isEmpty"], "List.isEmpty emptyInts", true),
            ln(&["List.reverse"], "List.reverse emptyInts", 0),
            ln(&["List.take"], "List.take 2 emptyInts", 0),
            ln(&["List.drop"], "List.drop 2 emptyInts", 0),
            ln(&["List.map"], "List.map (\\x -> x) emptyInts", 0),
            ln(&["List.filter"], "List.filter (\\_ -> True) emptyInts", 0),
            ln(&["List.concat"], "List.concat [ emptyInts ]", 0),
            i(&["List.foldl"], "List.foldl (\\x acc -> x + acc) 0 emptyInts", 0),
            i(&["List.foldr"], "List.foldr (\\x acc -> x + acc) 0 emptyInts", 0),
            // The identity/unit answers: `any` on nothing is False, `all` on
            // nothing is True. Getting this pair backwards is a classic.
            bo(&["List.any"], "List.any (\\_ -> True) emptyInts", false),
            bo(&["List.all"], "List.all (\\_ -> False) emptyInts", true),
            bo(&["List.member"], "List.member 1 emptyInts", false),
            ln(&["List.zip"], "List.zip emptyInts [ 1 ]", 0),
        ],
        "boundary" => vec![
            // Elm clamps both directions rather than erroring.
            ln(&["List.take"], "List.take 0 [ 1, 2 ]", 0),
            li(&["List.take"], "List.take 99 [ 1, 2 ]", "1,2"),
            ln(&["List.take"], "List.take -1 [ 1, 2 ]", 0),
            li(&["List.drop"], "List.drop 0 [ 1, 2 ]", "1,2"),
            ln(&["List.drop"], "List.drop 99 [ 1, 2 ]", 0),
            li(&["List.drop"], "List.drop -1 [ 1, 2 ]", "1,2"),
            // `range` is inclusive and empty when lo > hi.
            li(&["List.range"], "List.range 3 3", "3"),
            ln(&["List.range"], "List.range 3 1", 0),
            // `zip` truncates to the shorter side.
            ln(&["List.zip"], "List.zip [ 1, 2, 3 ] [ 4 ]", 1),
            // A single-element list: `tail` is `Just []`, not `Nothing`.
            ln(&["List.tail"], "Maybe.withDefault [ 9, 9 ] (List.tail [ 1 ])", 0),
            li(&["List.take"], "List.take 1 [ 5 ]", "5"),
            // ---- the ordering surface, at its boundaries -------------------
            //
            // Negatives are where the rendered compare is at its worst: "-1"
            // sorts BEFORE "-20", so `[ -1, -20, 3 ]` came back unchanged and
            // looked plausible.
            li(&["Kernel.List.sort"], "List.sort [ -1, -20, 3 ]", "-20,-1,3"),
            // Floats: "10.5" < "2.5" lexically.
            s(
                &["Kernel.List.sort"],
                "String.join \",\" (List.map String.fromFloat (List.sort [ 10.5, 9.5, 2.5 ]))",
                "2.5,9.5,10.5",
            ),
            // Elm orders LISTS of comparables lexicographically, shorter-is-less
            // on a common prefix. The dispatch already implemented this; the
            // sort path did not reach it.
            s(
                &["Kernel.List.sort"],
                "String.join \"/\" (List.map (\\xs -> String.join \",\" (List.map String.fromInt xs)) (List.sort [ [ 2 ], [ 10 ], [ 1, 0 ] ]))",
                "1,0/2/10",
            ),
            // Duplicates: an ordering that is not a consistent total order lets
            // `sort` return anything at all, so a repeated element is a real
            // boundary rather than a filler case.
            li(&["Kernel.List.sort"], "List.sort [ 2, 1, 2, 1 ]", "1,1,2,2"),
            ln(&["Kernel.List.sort"], "List.sort emptyInts", 0),
            li(&["Kernel.List.sort"], "List.sort [ 7 ]", "7"),
        ],
        "unicode" => vec![
            i(&["List.length"], "List.length (String.toList \"世界🎉\")", 3),
            s(
                &["List.reverse", "List.map"],
                "String.join \"\" (List.reverse (List.map String.fromChar (String.toList \"世界\")))",
                "界世",
            ),
            ls(&["List.filter"], "List.filter (\\x -> x /= \"b\") [ \"é\", \"b\" ]", "é"),
            // A `Char` is a Go `rune`, and the rendered form of a rune is its
            // DECIMAL CODE POINT — so the rendered order had nothing to do with
            // the code-point order: 'a'(97), 'é'(233), '界'(30028) sorted as
            // "233" < "30028" < "97", i.e. é, 界, a. This is the sharpest cell
            // of the element-type × operation crossing for `List`.
            s(
                &["Kernel.List.sort"],
                "String.fromList (List.sort [ '界', 'é', 'a' ])",
                "aé界",
            ),
            s(
                &["Kernel.List.sortBy"],
                "String.fromList (List.sortBy identity [ '界', 'é', 'a' ])",
                "aé界",
            ),
        ],
        "failure" => vec![
            mi(&["List.head"], "List.head emptyInts", None),
            mi(&["List.find"], "List.find (\\x -> x > 9) [ 1, 2 ]", None),
            // `tail []` is Nothing; `tail [x]` is `Just []`.
            bo(
                &["List.tail"],
                "case List.tail emptyInts of\n            Just _ ->\n                False\n\n            Nothing ->\n                True",
                true,
            ),
        ],
        _ => vec![],
    }
}

// --- Sky.Core.Dict ---------------------------------------------------------
//
// Ordering: `Dict` in the Elm family is an ORDERED map — `toList` / `keys` /
// `values` / `foldl` walk keys ascending. The `Sky.Core.Dict` docstrings say
// "(unsorted)", which contradicts both Elm and the module's own `Dict Int`
// key-fidelity promise. This battery asserts the ORDERED contract, because that
// is the contract the surface inherits; the stale docstrings are fixed in the
// same commit.

fn dict_battery(edge: &str) -> Vec<Check> {
    match edge {
        "nominal" => vec![
            i(&["Dict.size"], "Dict.size (Dict.fromList [ ( \"a\", 1 ), ( \"b\", 2 ) ])", 2),
            mi(&["Dict.get"], "Dict.get \"a\" (Dict.fromList [ ( \"a\", 1 ) ])", Some(1)),
            bo(&["Dict.member"], "Dict.member \"a\" (Dict.fromList [ ( \"a\", 1 ) ])", true),
            mi(
                &["Dict.insert"],
                "Dict.get \"k\" (Dict.insert \"k\" 9 Dict.empty)",
                Some(9),
            ),
            i(
                &["Dict.remove"],
                "Dict.size (Dict.remove \"a\" (Dict.fromList [ ( \"a\", 1 ), ( \"b\", 2 ) ]))",
                1,
            ),
            ls(
                &["Dict.keys"],
                "Dict.keys (Dict.fromList [ ( \"b\", 2 ), ( \"a\", 1 ) ])",
                "a,b",
            ),
            li(
                &["Dict.values"],
                "Dict.values (Dict.fromList [ ( \"b\", 2 ), ( \"a\", 1 ) ])",
                "1,2",
            ),
            ln(&["Dict.toList"], "Dict.toList (Dict.fromList [ ( \"a\", 1 ) ])", 1),
            // `map` gets (key, value) and rebuilds the values only.
            mi(
                &["Dict.map"],
                "Dict.get \"a\" (Dict.map (\\_ v -> v + 1) (Dict.fromList [ ( \"a\", 1 ) ]))",
                Some(2),
            ),
            // `foldl` gets (key, value, acc) and walks ascending.
            i(
                &["Dict.foldl"],
                "Dict.foldl (\\_ v acc -> v + acc) 0 (Dict.fromList [ ( \"a\", 1 ), ( \"b\", 2 ) ])",
                3,
            ),
            bo(&["Dict.isEmpty"], "Dict.isEmpty (Dict.fromList [ ( \"a\", 1 ) ])", false),
        ],
        "empty" => vec![
            i(&["Dict.size", "Dict.empty"], "Dict.size Dict.empty", 0),
            bo(&["Dict.isEmpty"], "Dict.isEmpty emptyDict", true),
            ln(&["Dict.keys"], "Dict.keys emptyDict", 0),
            ln(&["Dict.values"], "Dict.values emptyDict", 0),
            ln(&["Dict.toList"], "Dict.toList emptyDict", 0),
            i(&["Dict.foldl"], "Dict.foldl (\\_ v acc -> v + acc) 0 emptyDict", 0),
            bo(&["Dict.member"], "Dict.member \"a\" emptyDict", false),
            // `remove` on an absent key is a no-op, not an error.
            i(&["Dict.remove"], "Dict.size (Dict.remove \"a\" emptyDict)", 0),
        ],
        "boundary" => vec![
            // `fromList`: later pairs overwrite earlier ones.
            mi(
                &["Dict.fromList"],
                "Dict.get \"a\" (Dict.fromList [ ( \"a\", 1 ), ( \"a\", 2 ) ])",
                Some(2),
            ),
            i(&["Dict.fromList"], "Dict.size (Dict.fromList [ ( \"a\", 1 ), ( \"a\", 2 ) ])", 1),
            // `insert` replaces.
            mi(
                &["Dict.insert"],
                "Dict.get \"a\" (Dict.insert \"a\" 2 (Dict.fromList [ ( \"a\", 1 ) ]))",
                Some(2),
            ),
            // `union` is LEFT-biased.
            mi(
                &["Dict.union"],
                "Dict.get \"a\" (Dict.union (Dict.fromList [ ( \"a\", 1 ) ]) (Dict.fromList [ ( \"a\", 2 ) ]))",
                Some(1),
            ),
            i(
                &["Dict.union"],
                "Dict.size (Dict.union (Dict.fromList [ ( \"a\", 1 ) ]) (Dict.fromList [ ( \"b\", 2 ) ]))",
                2,
            ),
            // A single-entry dict.
            i(&["Dict.size"], "Dict.size (Dict.insert \"k\" 1 Dict.empty)", 1),
            // `Dict Int` keys are ordered NUMERICALLY, not by their stringified
            // form — "10" sorts before "2" lexically and must not here. This is
            // the module's own documented key-fidelity promise.
            li(
                &["Dict.keys"],
                "Dict.keys (Dict.fromList [ ( 10, 0 ), ( 2, 0 ) ])",
                "2,10",
            ),
        ],
        "unicode" => vec![
            mi(
                &["Dict.get"],
                "Dict.get \"世\" (Dict.fromList [ ( \"世\", 1 ) ])",
                Some(1),
            ),
            ls(
                &["Dict.keys"],
                "Dict.keys (Dict.fromList [ ( \"é\", 1 ) ])",
                "é",
            ),
        ],
        "failure" => vec![
            mi(&["Dict.get"], "Dict.get \"missing\" (Dict.fromList [ ( \"a\", 1 ) ])", None),
            mi(&["Dict.get"], "Dict.get \"a\" emptyDict", None),
        ],
        _ => vec![],
    }
}

// --- Sky.Core.Set ----------------------------------------------------------

fn set_battery(edge: &str) -> Vec<Check> {
    match edge {
        "nominal" => vec![
            i(&["Set.size"], "Set.size (Set.fromList [ 1, 2 ])", 2),
            bo(&["Set.member"], "Set.member 1 (Set.fromList [ 1 ])", true),
            i(&["Set.insert"], "Set.size (Set.insert 3 (Set.fromList [ 1 ]))", 2),
            i(&["Set.remove"], "Set.size (Set.remove 1 (Set.fromList [ 1, 2 ]))", 1),
            li(&["Set.toList"], "Set.toList (Set.fromList [ 2, 1 ])", "1,2"),
            li(
                &["Set.union"],
                "Set.toList (Set.union (Set.fromList [ 1 ]) (Set.fromList [ 2 ]))",
                "1,2",
            ),
            li(
                &["Set.intersect"],
                "Set.toList (Set.intersect (Set.fromList [ 1, 2 ]) (Set.fromList [ 2, 3 ]))",
                "2",
            ),
            li(
                &["Set.diff"],
                "Set.toList (Set.diff (Set.fromList [ 1, 2 ]) (Set.fromList [ 2 ]))",
                "1",
            ),
        ],
        "empty" => vec![
            i(&["Set.size", "Set.empty"], "Set.size emptySet", 0),
            i(&["Set.empty"], "Set.size (Set.insert 1 Set.empty)", 1),
            ln(&["Set.toList"], "Set.toList emptySet", 0),
            bo(&["Set.member"], "Set.member 1 emptySet", false),
            i(&["Set.remove"], "Set.size (Set.remove 1 emptySet)", 0),
            i(&["Set.union"], "Set.size (Set.union emptySet (Set.fromList [ 1 ]))", 1),
            i(&["Set.intersect"], "Set.size (Set.intersect emptySet (Set.fromList [ 1 ]))", 0),
            i(&["Set.diff"], "Set.size (Set.diff emptySet (Set.fromList [ 1 ]))", 0),
        ],
        "boundary" => vec![
            // `fromList` de-duplicates; `insert` of a present element is a
            // no-op. A set that grew on a duplicate would pass every nominal
            // case.
            i(&["Set.fromList"], "Set.size (Set.fromList [ 1, 1, 1 ])", 1),
            i(&["Set.insert"], "Set.size (Set.insert 1 (Set.fromList [ 1 ]))", 1),
            i(&["Set.remove"], "Set.size (Set.remove 9 (Set.fromList [ 1 ]))", 1),
            // Self-operations: A ∩ A = A, A \ A = ∅, A ∪ A = A.
            i(&["Set.intersect"], "Set.size (Set.intersect (Set.fromList [ 1, 2 ]) (Set.fromList [ 1, 2 ]))", 2),
            i(&["Set.diff"], "Set.size (Set.diff (Set.fromList [ 1, 2 ]) (Set.fromList [ 1, 2 ]))", 0),
            i(&["Set.union"], "Set.size (Set.union (Set.fromList [ 1 ]) (Set.fromList [ 1 ]))", 1),
        ],
        "unicode" => vec![
            bo(&["Set.member"], "Set.member \"世\" (Set.fromList [ \"世\" ])", true),
            i(&["Set.fromList"], "Set.size (Set.fromList [ \"é\", \"é\" ])", 1),
        ],
        "failure" => vec![bo(
            &["Set.member"],
            "Set.member 9 (Set.fromList [ 1, 2 ])",
            false,
        )],
        _ => vec![],
    }
}

// --- Sky.Core.Maybe --------------------------------------------------------

fn maybe_battery(edge: &str) -> Vec<Check> {
    match edge {
        "nominal" => vec![
            i(&["Maybe.withDefault"], "Maybe.withDefault 0 (Just 5)", 5),
            mi(&["Maybe.map"], "Maybe.map (\\x -> x + 1) (Just 1)", Some(2)),
            mi(&["Maybe.andThen"], "Maybe.andThen (\\x -> Just (x * 2)) (Just 3)", Some(6)),
            mi(&["Maybe.map2"], "Maybe.map2 (\\a b -> a + b) (Just 1) (Just 2)", Some(3)),
            mi(
                &["Maybe.map3"],
                "Maybe.map3 (\\a b c -> a + b + c) (Just 1) (Just 2) (Just 3)",
                Some(6),
            ),
            mi(
                &["Maybe.map4"],
                "Maybe.map4 (\\a b c d -> a + b + c + d) (Just 1) (Just 2) (Just 3) (Just 4)",
                Some(10),
            ),
            mi(
                &["Maybe.map5"],
                "Maybe.map5 (\\a b c d e -> a + b + c + d + e) (Just 1) (Just 2) (Just 3) (Just 4) (Just 5)",
                Some(15),
            ),
            // `andMap` takes the VALUE first and the wrapped function second.
            mi(&["Maybe.andMap"], "Maybe.andMap (Just 3) (Just (\\x -> x + 1))", Some(4)),
            bo(&["Maybe.isJust"], "Maybe.isJust (Just 1)", true),
            bo(&["Maybe.isNothing"], "Maybe.isNothing (Just 1)", false),
            ln(&["Maybe.combine"], "Maybe.withDefault [] (Maybe.combine [ Just 1, Just 2 ])", 2),
            // `combine` preserves the ORIGINAL order — the implementation
            // accumulates reversed and flips once, so an off-by-one flip is
            // exactly what this catches.
            li(
                &["Maybe.combine"],
                "Maybe.withDefault [] (Maybe.combine [ Just 1, Just 2, Just 3 ])",
                "1,2,3",
            ),
        ],
        // `combine []` is `Just []`, NOT `Nothing`. The unit of the fold.
        "empty" => vec![
            bo(
                &["Maybe.combine"],
                "Maybe.isJust (Maybe.combine emptyMaybes)",
                true,
            ),
            ln(
                &["Maybe.combine"],
                "Maybe.withDefault [ 9 ] (Maybe.combine emptyMaybes)",
                0,
            ),
        ],
        "failure" => vec![
            i(&["Maybe.withDefault"], "Maybe.withDefault 7 nothingInt", 7),
            mi(&["Maybe.map"], "Maybe.map (\\x -> x + 1) nothingInt", None),
            mi(&["Maybe.andThen"], "Maybe.andThen (\\x -> Just x) nothingInt", None),
            // Any Nothing short-circuits the whole mapN.
            mi(&["Maybe.map2"], "Maybe.map2 (\\a b -> a + b) (Just 1) nothingInt", None),
            mi(&["Maybe.map2"], "Maybe.map2 (\\a b -> a + b) nothingInt (Just 1)", None),
            mi(&["Maybe.andMap"], "Maybe.andMap nothingInt (Just (\\x -> x + 1))", None),
            bo(&["Maybe.isJust"], "Maybe.isJust nothingInt", false),
            bo(&["Maybe.isNothing"], "Maybe.isNothing nothingInt", true),
            // One Nothing anywhere collapses `combine`.
            bo(
                &["Maybe.combine"],
                "Maybe.isNothing (Maybe.combine [ Just 1, nothingInt, Just 3 ])",
                true,
            ),
            // An `andThen` that itself returns Nothing.
            mi(&["Maybe.andThen"], "Maybe.andThen (\\_ -> nothingInt) (Just 1)", None),
        ],
        _ => vec![],
    }
}

// --- Sky.Core.Result -------------------------------------------------------

fn result_battery(edge: &str) -> Vec<Check> {
    match edge {
        "nominal" => vec![
            i(&["Result.withDefault"], "Result.withDefault 0 (okInt 5)", 5),
            ri(&["Result.map"], "Result.map (\\x -> x + 1) (okInt 1)", Some(2)),
            ri(&["Result.andThen"], "Result.andThen (\\x -> okInt (x * 2)) (okInt 3)", Some(6)),
            // `mapError` leaves an Ok untouched.
            ri(&["Result.mapError"], "Result.mapError (\\e -> e) (okInt 4)", Some(4)),
            ri(&["Result.map2"], "Result.map2 (\\a b -> a + b) (okInt 1) (okInt 2)", Some(3)),
            ri(
                &["Result.map3"],
                "Result.map3 (\\a b c -> a + b + c) (okInt 1) (okInt 2) (okInt 3)",
                Some(6),
            ),
            ri(
                &["Result.map4"],
                "Result.map4 (\\a b c d -> a + b + c + d) (okInt 1) (okInt 2) (okInt 3) (okInt 4)",
                Some(10),
            ),
            ri(
                &["Result.map5"],
                "Result.map5 (\\a b c d e -> a + b + c + d + e) (okInt 1) (okInt 2) (okInt 3) (okInt 4) (okInt 5)",
                Some(15),
            ),
            ri(&["Result.andMap"], "Result.andMap (okInt 3) (okFn (\\x -> x + 1))", Some(4)),
            li(
                &["Result.combine"],
                "Result.withDefault [] (Result.combine [ okInt 1, okInt 2, okInt 3 ])",
                "1,2,3",
            ),
        ],
        // `combine []` is `Ok []`.
        "empty" => vec![ln(
            &["Result.combine"],
            "Result.withDefault [ 9 ] (Result.combine emptyResults)",
            0,
        )],
        "failure" => vec![
            i(&["Result.withDefault"], "Result.withDefault 7 errInt", 7),
            ri(&["Result.map"], "Result.map (\\x -> x + 1) errInt", None),
            ri(&["Result.andThen"], "Result.andThen (\\x -> okInt x) errInt", None),
            ri(&["Result.map2"], "Result.map2 (\\a b -> a + b) (okInt 1) errInt", None),
            ri(&["Result.map2"], "Result.map2 (\\a b -> a + b) errInt (okInt 1)", None),
            ri(&["Result.andMap"], "Result.andMap errInt (okFn (\\x -> x + 1))", None),
            ri(&["Result.combine"], "Result.combine [ okInt 1, errInt ] |> Result.map List.length", None),
            // `mapError` rewrites the error and keeps it an Err.
            s(
                &["Result.mapError"],
                "case Result.mapError (\\_ -> Error.io \"rewritten\") errInt of\n        Ok _ ->\n            \"unexpected-ok\"\n\n        Err e ->\n            Error.toString e",
                "IO: rewritten",
            ),
            // An `andThen` that itself fails.
            ri(&["Result.andThen"], "Result.andThen (\\_ -> errInt) (okInt 1)", None),
        ],
        _ => vec![],
    }
}

// --- Sky.Core.Char ---------------------------------------------------------

fn char_battery(edge: &str) -> Vec<Check> {
    match edge {
        "nominal" => vec![
            bo(&["Char.isAlpha"], "Char.isAlpha 'a'", true),
            bo(&["Char.isDigit"], "Char.isDigit '7'", true),
            bo(&["Char.isLower"], "Char.isLower 'a'", true),
            bo(&["Char.isUpper"], "Char.isUpper 'A'", true),
            // NOTE the Elm divergence the module documents: these return a
            // one-rune STRING, not a Char.
            s(&["Char.toUpper"], "Char.toUpper 'a'", "A"),
            s(&["Char.toLower"], "Char.toLower 'A'", "a"),
            i(&["Char.toCode"], "Char.toCode 'A'", 65),
            s(&["Char.fromCode"], "String.fromChar (Char.fromCode 65)", "A"),
            // The round-trip the docstring promises.
            i(&["Char.toCode", "Char.fromCode"], "Char.toCode (Char.fromCode 122)", 122),
        ],
        "boundary" => vec![
            i(&["Char.toCode"], "Char.toCode '0'", 48),
            i(&["Char.toCode"], "Char.toCode 'z'", 122),
            bo(&["Char.isAlpha"], "Char.isAlpha '7'", false),
            bo(&["Char.isDigit"], "Char.isDigit 'a'", false),
            bo(&["Char.isUpper"], "Char.isUpper 'a'", false),
            bo(&["Char.isLower"], "Char.isLower 'A'", false),
        ],
        "unicode" => vec![
            // U+4E16 = 19990 decimal. A byte-indexing regression cannot produce
            // this number.
            i(&["Char.toCode"], "Char.toCode '世'", 19990),
            s(&["Char.fromCode"], "String.fromChar (Char.fromCode 19990)", "世"),
            s(&["Char.toUpper"], "Char.toUpper 'é'", "É"),
            bo(&["Char.isAlpha"], "Char.isAlpha 'é'", true),
            bo(&["Char.isDigit"], "Char.isDigit '世'", false),
        ],
        // The module's own promise: an out-of-range code point yields U+FFFD.
        "failure" => vec![
            s(&["Char.fromCode"], "String.fromChar (Char.fromCode -1)", "\u{fffd}"),
            s(&["Char.fromCode"], "String.fromChar (Char.fromCode 1114112)", "\u{fffd}"),
        ],
        _ => vec![],
    }
}

// --- Sky.Core.Encoding -----------------------------------------------------
//
// Every expectation here comes from RFC 4648 (base64) or from plain hex, both
// of which are computable by hand and were.

fn encoding_battery(edge: &str) -> Vec<Check> {
    match edge {
        "nominal" => vec![
            s(&["Encoding.base64Encode"], "Encoding.base64Encode \"Hello\"", "SGVsbG8="),
            s(&["Encoding.base64Encode"], "Encoding.base64Encode \"a\"", "YQ=="),
            s(&["Encoding.base64Encode"], "Encoding.base64Encode \"ab\"", "YWI="),
            s(&["Encoding.base64Encode"], "Encoding.base64Encode \"abc\"", "YWJj"),
            rs(&["Encoding.base64Decode"], "Encoding.base64Decode \"SGVsbG8=\"", Some("Hello")),
            s(&["Encoding.hexEncode"], "Encoding.hexEncode \"ab\"", "6162"),
            rs(&["Encoding.hexDecode"], "Encoding.hexDecode \"6162\"", Some("ab")),
            // Round-trips are independent of the escaping convention, so they
            // hold whatever `urlEncode` chooses for a space.
            rs(
                &["Encoding.urlEncode", "Encoding.urlDecode"],
                "Encoding.urlDecode (Encoding.urlEncode \"a b/c?d=e&f\")",
                Some("a b/c?d=e&f"),
            ),
            bo(
                &["Encoding.urlEncode"],
                "String.contains \"%2F\" (Encoding.urlEncode \"a/b\")",
                true,
            ),
        ],
        "empty" => vec![
            s(&["Encoding.base64Encode"], "Encoding.base64Encode \"\"", ""),
            rs(&["Encoding.base64Decode"], "Encoding.base64Decode \"\"", Some("")),
            s(&["Encoding.hexEncode"], "Encoding.hexEncode \"\"", ""),
            rs(&["Encoding.hexDecode"], "Encoding.hexDecode \"\"", Some("")),
            s(&["Encoding.urlEncode"], "Encoding.urlEncode \"\"", ""),
        ],
        "unicode" => vec![
            // UTF-8 of 世 is E4 B8 96; base64 of those three bytes is "5LiW".
            s(&["Encoding.base64Encode"], "Encoding.base64Encode \"世\"", "5LiW"),
            s(&["Encoding.hexEncode"], "Encoding.hexEncode \"世\"", "e4b896"),
            rs(&["Encoding.base64Decode"], "Encoding.base64Decode \"5LiW\"", Some("世")),
            rs(&["Encoding.hexDecode"], "Encoding.hexDecode \"e4b896\"", Some("世")),
            // hex is BYTE-wise: three bytes, six hex digits — while
            // `String.length "世"` is 1. The two must not agree.
            i(&["Encoding.hexEncode"], "String.length (Encoding.hexEncode \"世\")", 6),
        ],
        "failure" => vec![
            // Odd-length hex cannot be a whole number of bytes.
            rs(&["Encoding.hexDecode"], "Encoding.hexDecode \"abc\"", None),
            rs(&["Encoding.hexDecode"], "Encoding.hexDecode \"zz\"", None),
            rs(&["Encoding.base64Decode"], "Encoding.base64Decode \"!!!!\"", None),
        ],
        _ => vec![],
    }
}

// --- Sky.Core.Crypto -------------------------------------------------------
//
// The published digests. These are the strongest class-V assertions in the
// whole corpus: they are fixed by FIPS 180-4 / RFC 1321 and no Sky artefact was
// consulted to obtain them.

fn crypto_battery(edge: &str) -> Vec<Check> {
    match edge {
        "nominal" => vec![
            s(
                &["Crypto.sha256"],
                "Crypto.sha256 \"abc\"",
                "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
            ),
            s(
                &["Crypto.sha1"],
                "Crypto.sha1 \"abc\"",
                "a9993e364706816aba3e25717850c26c9cd0d89d",
            ),
            s(&["Crypto.md5"], "Crypto.md5 \"abc\"", "900150983cd24fb0d6963f7d28e17f72"),
            // Digest widths are fixed by the algorithm.
            i(&["Crypto.sha256"], "String.length (Crypto.sha256 \"x\")", 64),
            i(&["Crypto.sha512"], "String.length (Crypto.sha512 \"x\")", 128),
            i(&["Crypto.sha1"], "String.length (Crypto.sha1 \"x\")", 40),
            i(&["Crypto.md5"], "String.length (Crypto.md5 \"x\")", 32),
            i(&["Crypto.hmacSha256"], "String.length (Crypto.hmacSha256 \"k\" \"m\")", 64),
            i(&["Crypto.hmacSha512"], "String.length (Crypto.hmacSha512 \"k\" \"m\")", 128),
            // A MAC must depend on the KEY as well as the message.
            bo(
                &["Crypto.hmacSha256"],
                "Crypto.hmacSha256 \"k1\" \"m\" /= Crypto.hmacSha256 \"k2\" \"m\"",
                true,
            ),
            bo(&["Crypto.constantTimeEqual"], "Crypto.constantTimeEqual \"abc\" \"abc\"", true),
            // AEAD round-trip. The ciphertext itself is unpredictable (a fresh
            // nonce per call, as documented), so the round-trip IS the
            // assertion — and it is a real one: a broken tag check or a wrong
            // key derivation breaks it.
            rs(
                &["Crypto.aesGcmEncrypt", "Crypto.aesGcmDecrypt", "Crypto.aesKeyFromPassword"],
                "Result.andThen (\\ct -> Crypto.aesGcmDecrypt aesKey ct) (Crypto.aesGcmEncrypt aesKey \"secret-payload\")",
                Some("secret-payload"),
            ),
            rs(
                &["Crypto.chacha20Encrypt", "Crypto.chacha20Decrypt", "Crypto.chachaKeyFromPassword"],
                "Result.andThen (\\ct -> Crypto.chacha20Decrypt chachaKey ct) (Crypto.chacha20Encrypt chachaKey \"secret-payload\")",
                Some("secret-payload"),
            ),
        ],
        // The empty-input digests, all four published.
        "empty" => vec![
            s(
                &["Crypto.sha256"],
                "Crypto.sha256 \"\"",
                "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
            ),
            s(
                &["Crypto.sha1"],
                "Crypto.sha1 \"\"",
                "da39a3ee5e6b4b0d3255bfef95601890afd80709",
            ),
            s(&["Crypto.md5"], "Crypto.md5 \"\"", "d41d8cd98f00b204e9800998ecf8427e"),
            i(&["Crypto.sha512"], "String.length (Crypto.sha512 \"\")", 128),
            bo(&["Crypto.constantTimeEqual"], "Crypto.constantTimeEqual \"\" \"\"", true),
        ],
        "unicode" => vec![
            // SHA-256 hashes BYTES, so the digest of "世" is the digest of its
            // three UTF-8 bytes. Only the width is asserted independently, plus
            // the property that different code points hash differently.
            i(&["Crypto.sha256"], "String.length (Crypto.sha256 \"世界\")", 64),
            bo(
                &["Crypto.sha256"],
                "Crypto.sha256 \"世\" /= Crypto.sha256 \"界\"",
                true,
            ),
        ],
        "failure" => vec![
            bo(&["Crypto.constantTimeEqual"], "Crypto.constantTimeEqual \"a\" \"b\"", false),
            // Differing lengths are not equal.
            bo(&["Crypto.constantTimeEqual"], "Crypto.constantTimeEqual \"a\" \"ab\"", false),
            // A tampered ciphertext must NEVER decrypt to garbage — it must
            // fail the tag check. This is the whole point of an AEAD.
            rs(
                &["Crypto.aesGcmDecrypt"],
                "Crypto.aesGcmDecrypt aesKey \"not-a-valid-ciphertext\"",
                None,
            ),
            rs(
                &["Crypto.chacha20Decrypt"],
                "Crypto.chacha20Decrypt chachaKey \"not-a-valid-ciphertext\"",
                None,
            ),
            // An unparseable PEM is an Err, not a panic.
            rs(
                &["Crypto.rsaSha256Sign"],
                "Crypto.rsaSha256Sign \"not-a-pem\" \"msg\"",
                None,
            ),
            bo(
                &["Crypto.rsaSha256Verify"],
                "Crypto.rsaSha256Verify \"not-a-pem\" \"msg\" \"sig\"",
                false,
            ),
        ],
        _ => vec![],
    }
}

// --- Sky.Core.Secret -------------------------------------------------------
//
// The wrap/reveal boundary is a pure, deterministic value crossing:
// `reveal (fromString s) == s`. It is a real assertion end-to-end — it drives
// the `Secret_fromString` + `Secret_reveal` FFI kernels through codegen and
// proves the opaque wrapper is byte-transparent through the ONE greppable exit.
// A String LITERAL to `fromString` is a compile error (the no-committed-secret
// lint), so the corpus uses `unsafeFromString` — the lint-exempt test twin.
fn secret_battery(edge: &str) -> Vec<Check> {
    match edge {
        "nominal" => vec![
            s(
                &["Secret.reveal", "Secret.unsafeFromString"],
                "Secret.reveal (Secret.unsafeFromString \"hunter2\")",
                "hunter2",
            ),
            s(
                &["Secret.reveal", "Secret.unsafeFromString"],
                "Secret.reveal (Secret.unsafeFromString \"p@ss w0rd with spaces\")",
                "p@ss w0rd with spaces",
            ),
        ],
        // The empty secret still round-trips — the wrapper adds no framing, so
        // the revealed string is length-0 (asserted as an Int so the expectation
        // is non-empty, not the vacuous "" a raw reveal would produce).
        "empty" => vec![i(
            &["Secret.reveal", "Secret.unsafeFromString"],
            "String.length (Secret.reveal (Secret.unsafeFromString \"\"))",
            0,
        )],
        _ => vec![],
    }
}

// --- Sky.Core.Math ---------------------------------------------------------

fn math_battery(edge: &str) -> Vec<Check> {
    match edge {
        "nominal" => vec![
            i(&["Math.abs"], "Math.abs 5", 5),
            i(&["Math.min"], "Math.min 1 2", 1),
            i(&["Math.max"], "Math.max 1 2", 2),
            f(&["Math.sqrt"], "Math.sqrt 4.0", "2"),
            f(&["Math.pow"], "Math.pow 2.0 3.0", "8"),
            f(&["Math.hypot"], "Math.hypot 3.0 4.0", "5"),
            f(&["Math.cbrt"], "Math.cbrt 8.0", "2"),
            i(&["Math.floor"], "Math.floor 3.7", 3),
            i(&["Math.ceil"], "Math.ceil 3.2", 4),
            i(&["Math.trunc"], "Math.trunc 3.7", 3),
            i(&["Math.round"], "Math.round 3.4", 3),
            i(&["Math.log10"], "Math.floor (Math.log10 100.0)", 2),
            i(&["Math.log2"], "Math.round (Math.log2 8.0)", 3),
            i(&["Math.exp"], "Math.floor (Math.exp 1.0)", 2),
            i(&["Math.log"], "Math.round (Math.log Math.e)", 1),
            i(&["Math.pi"], "Math.floor Math.pi", 3),
            i(&["Math.e"], "Math.floor Math.e", 2),
            i(&["Math.phi"], "Math.floor Math.phi", 1),
            i(&["Math.sqrt2"], "Math.floor Math.sqrt2", 1),
            f(&["Math.mod"], "Math.mod 5.0 3.0", "2"),
            f(&["Math.remainder"], "Math.remainder 5.0 3.0", "-1"),
            i(&["Math.sin"], "Math.round (Math.sin 0.0)", 0),
            i(&["Math.cos"], "Math.round (Math.cos 0.0)", 1),
            i(&["Math.tan"], "Math.round (Math.tan 0.0)", 0),
            i(&["Math.atan2"], "Math.round (Math.atan2 0.0 1.0)", 0),
            i(&["Math.asin"], "Math.round (Math.asin 0.0)", 0),
            i(&["Math.acos"], "Math.round (Math.acos 1.0)", 0),
            i(&["Math.atan"], "Math.round (Math.atan 0.0)", 0),
            i(&["Math.sinh"], "Math.round (Math.sinh 0.0)", 0),
            i(&["Math.cosh"], "Math.round (Math.cosh 0.0)", 1),
            i(&["Math.tanh"], "Math.round (Math.tanh 0.0)", 0),
            i(&["Math.asinh"], "Math.round (Math.asinh 0.0)", 0),
            i(&["Math.acosh"], "Math.round (Math.acosh 1.0)", 0),
            i(&["Math.atanh"], "Math.round (Math.atanh 0.0)", 0),
            i(&["Math.exp2"], "Math.round (Math.exp2 3.0)", 8),
            bo(&["Math.isNaN"], "Math.isNaN 1.0", false),
        ],
        "boundary" => vec![
            i(&["Math.abs"], "Math.abs 0", 0),
            i(&["Math.abs"], "Math.abs -5", 5),
            // Rounding DIRECTION at the half and on negatives — Elm's `round`
            // is half away from zero, and floor/ceil/trunc must disagree on
            // negatives or one of them is wrong.
            i(&["Math.round"], "Math.round 2.5", 3),
            i(&["Math.round"], "Math.round -2.5", -3),
            i(&["Math.floor"], "Math.floor -3.2", -4),
            i(&["Math.ceil"], "Math.ceil -3.7", -3),
            i(&["Math.trunc"], "Math.trunc -3.7", -3),
            i(&["Math.floor"], "Math.floor 3.0", 3),
            i(&["Math.ceil"], "Math.ceil 3.0", 3),
            i(&["Math.min"], "Math.min -1 1", -1),
            i(&["Math.max"], "Math.max -1 1", 1),
            f(&["Math.sqrt"], "Math.sqrt 0.0", "0"),
            f(&["Math.pow"], "Math.pow 2.0 0.0", "1"),
        ],
        // `nan == nan` is False by IEEE 754, which is precisely why `isNaN`
        // exists — the module says so.
        "failure" => vec![
            bo(&["Math.isNaN", "Math.nan"], "Math.isNaN Math.nan", true),
            bo(&["Math.nan"], "Math.nan == Math.nan", false),
            bo(&["Math.inf"], "Math.inf > 0.0", true),
            bo(&["Math.isNaN", "Math.inf"], "Math.isNaN Math.inf", false),
        ],
        _ => vec![],
    }
}

// --- Sky.Core.Basics -------------------------------------------------------

fn basics_battery(edge: &str) -> Vec<Check> {
    match edge {
        "nominal" => vec![
            i(&["Basics.identity"], "identity 5", 5),
            i(&["Basics.always"], "always 1 2", 1),
            bo(&["Basics.not"], "not True", false),
            bo(&["Basics.not"], "not False", true),
            i(&["Basics.fst"], "fst ( 1, 2 )", 1),
            i(&["Basics.snd"], "snd ( 1, 2 )", 2),
            i(&["Basics.clamp"], "clamp 1 3 2", 2),
            s(&["Kernel.Basics.toString"], "toString 42", "42"),
            // `compare` and `negate` are kernel `Basics` members too, and until
            // now Family S asserted NEITHER — they were named in the ledger's
            // gap list as "asserted but uncountable", which was true of
            // `toString`/`modBy` and not of these two. Elm's `compare` is
            // LT/EQ/GT as -1/0/1.
            i(&["Kernel.Basics.compare"], "compare 1 2", -1),
            i(&["Kernel.Basics.compare"], "compare 2 2", 0),
            i(&["Kernel.Basics.compare"], "compare 3 2", 1),
            i(&["Kernel.Basics.negate"], "negate 5", -5),
        ],
        "boundary" => vec![
            // `clamp lo hi x` — low arg first.
            i(&["Basics.clamp"], "clamp 1 3 5", 3),
            i(&["Basics.clamp"], "clamp 1 3 0", 1),
            i(&["Basics.clamp"], "clamp 1 3 1", 1),
            i(&["Basics.clamp"], "clamp 1 3 3", 3),
            // Elm's `modBy` is FLOORED and divisor-first: `modBy 3 -1 == 2`,
            // not `-1`. Truncated `%` would give `-1` and pass every positive
            // case above.
            i(&["Kernel.Basics.modBy"], "modBy 3 7", 1),
            i(&["Kernel.Basics.modBy"], "modBy 3 -1", 2),
            i(&["Kernel.Basics.modBy"], "modBy 3 0", 0),
            s(&["Kernel.Basics.toString"], "toString -7", "-7"),
            // `negate 0` must not render `-0`, and `compare` on the equal
            // boundary must be exactly 0 rather than merely "not less".
            i(&["Kernel.Basics.negate"], "negate 0", 0),
            i(&["Kernel.Basics.negate"], "negate -5", 5),
            i(&["Kernel.Basics.compare"], "compare -20 -1", -1),
        ],
        _ => vec![],
    }
}

// --- Sky.Core.ToString -----------------------------------------------------

fn tostring_battery(edge: &str) -> Vec<Check> {
    match edge {
        "nominal" => vec![
            s(&["ToString.fromInt"], "ToString.fromInt 42", "42"),
            s(&["ToString.fromFloat"], "ToString.fromFloat 3.14", "3.14"),
            // The module deliberately gives capitalised `True`/`False`.
            s(&["ToString.fromBool"], "ToString.fromBool True", "True"),
            s(&["ToString.fromBool"], "ToString.fromBool False", "False"),
            // The Unix epoch is 00:00:00 UTC by definition of the epoch.
            s(&["ToString.fromTime"], "ToString.fromTime 0", "00:00:00"),
        ],
        "boundary" => vec![
            s(&["ToString.fromInt"], "ToString.fromInt 0", "0"),
            s(&["ToString.fromInt"], "ToString.fromInt -7", "-7"),
            // A whole float renders without a fractional part, and without
            // exponent form.
            s(&["ToString.fromFloat"], "ToString.fromFloat 1.0", "1"),
            s(&["ToString.fromFloat"], "ToString.fromFloat -0.5", "-0.5"),
            s(&["ToString.fromFloat"], "ToString.fromFloat 0.0", "0"),
        ],
        _ => vec![],
    }
}

// --- Sky.Core.Path ---------------------------------------------------------

fn path_battery(edge: &str) -> Vec<Check> {
    match edge {
        "nominal" => vec![
            s(&["Path.base"], "Path.base \"/a/b.txt\"", "b.txt"),
            s(&["Path.dir"], "Path.dir \"/a/b.txt\"", "/a"),
            // `ext` keeps the leading dot.
            s(&["Path.ext"], "Path.ext \"/a/b.txt\"", ".txt"),
            bo(&["Path.isAbsolute"], "Path.isAbsolute \"/a\"", true),
            bo(&["Path.isAbsolute"], "Path.isAbsolute \"a/b\"", false),
        ],
        "boundary" => vec![
            s(&["Path.base"], "Path.base \"/\"", "/"),
            s(&["Path.dir"], "Path.dir \"/\"", "/"),
            // A bare name has no directory: "." by the POSIX convention.
            s(&["Path.dir"], "Path.dir \"foo\"", "."),
            s(&["Path.ext"], "Path.ext \"noext\"", ""),
            s(&["Path.ext"], "Path.ext \"a.tar.gz\"", ".gz"),
            s(&["Path.base"], "Path.base \"a/b/\"", "b"),
        ],
        "empty" => vec![
            s(&["Path.base"], "Path.base \"\"", "."),
            s(&["Path.dir"], "Path.dir \"\"", "."),
            s(&["Path.ext"], "Path.ext \"\"", ""),
            bo(&["Path.isAbsolute"], "Path.isAbsolute \"\"", false),
        ],
        "unicode" => vec![
            s(&["Path.base"], "Path.base \"/a/世界.txt\"", "世界.txt"),
            s(&["Path.ext"], "Path.ext \"/a/файл.тхт\"", ".тхт"),
        ],
        _ => vec![],
    }
}

// --- Sky.Core.Error --------------------------------------------------------
//
// `Sky.Core.Error` is 100 % pure Sky, so its `.sky` source IS the
// specification. These assertions test that the compiler compiles that
// specification faithfully — the "compiles clean, behaves wrong" target.

fn error_battery(edge: &str) -> Vec<Check> {
    match edge {
        "nominal" => vec![
            s(&["Error.io", "Error.toString"], "Error.toString (Error.io \"boom\")", "IO: boom"),
            s(
                &["Error.network", "Error.toString"],
                "Error.toString (Error.network \"down\")",
                "Network: down",
            ),
            // `kindLabel Ffi` is "FFI", not "Ffi" — an initialism the label
            // table uppercases and the constructor name does not.
            s(&["Error.ffi", "Error.toString"], "Error.toString (Error.ffi \"x\")", "FFI: x"),
            s(&["Error.decode", "Error.toString"], "Error.toString (Error.decode \"d\")", "Decode: d"),
            s(
                &["Error.invalidInput", "Error.toString"],
                "Error.toString (Error.invalidInput \"i\")",
                "InvalidInput: i",
            ),
            s(&["Error.conflict", "Error.toString"], "Error.toString (Error.conflict \"c\")", "Conflict: c"),
            s(
                &["Error.unavailable", "Error.toString"],
                "Error.toString (Error.unavailable \"u\")",
                "Unavailable: u",
            ),
            s(
                &["Error.unexpected", "Error.toString"],
                "Error.toString (Error.unexpected \"e\")",
                "Unexpected: e",
            ),
            s(&["Error.kindLabel"], "Error.kindLabel Io", "IO"),
            s(&["Error.kindLabel"], "Error.kindLabel Ffi", "FFI"),
            s(&["Error.kindLabel"], "Error.kindLabel NotFound", "NotFound"),
            // `withMessage` replaces the message and KEEPS the kind.
            s(
                &["Error.withMessage"],
                "Error.toString (Error.withMessage \"new\" (Error.io \"old\"))",
                "IO: new",
            ),
            s(&["Error.mkInfo"], "(Error.mkInfo \"m\").message", "m"),
        ],
        // The three arity-0 error VALUES, which are values and not functions —
        // and whose default messages the module fixes.
        "boundary" => vec![
            s(&["Error.timeout"], "Error.toString Error.timeout", "Timeout: operation timed out"),
            s(&["Error.notFound"], "Error.toString Error.notFound", "NotFound: not found"),
            s(
                &["Error.permissionDenied"],
                "Error.toString Error.permissionDenied",
                "PermissionDenied: permission denied",
            ),
            // An EMPTY message still gets the `"<Kind>: "` prefix — the
            // separator is not conditional on there being something after it.
            //
            // The `++ "."` is load-bearing: the runner compares
            // `stdout.trim()`, so a case whose last item ends in a space can
            // never match its own expectation. Discovered by this battery's
            // first full run, which went red on `"IO: "` vs `"IO:"` — the
            // compiler was right and the assertion was unstateable. Anchoring
            // the trailing space with a visible character makes it stateable
            // wherever the item lands in the join order.
            s(&["Error.io"], "Error.toString (Error.io \"\") ++ \".\"", "IO: ."),
        ],
        "failure" => vec![
            // The retryable partition, both sides. A transient kind that stops
            // being retryable silently disables every caller's retry loop.
            bo(&["Error.isRetryable"], "Error.isRetryable Error.timeout", true),
            bo(&["Error.isRetryable"], "Error.isRetryable (Error.network \"x\")", true),
            bo(&["Error.isRetryable"], "Error.isRetryable (Error.unavailable \"x\")", true),
            bo(&["Error.isRetryable"], "Error.isRetryable (Error.io \"x\")", false),
            bo(&["Error.isRetryable"], "Error.isRetryable Error.notFound", false),
            bo(&["Error.isRetryable"], "Error.isRetryable (Error.invalidInput \"x\")", false),
            // `withDetails` attaches details without disturbing the message.
            s(
                &["Error.withDetails"],
                "Error.toString (Error.withDetails (HttpStatus 404) (Error.network \"n\"))",
                "Network: n",
            ),
        ],
        "unicode" => vec![s(
            &["Error.toString"],
            "Error.toString (Error.io \"世界 failed\")",
            "IO: 世界 failed",
        )],
        _ => vec![],
    }
}

// --- Std.Decimal -----------------------------------------------------------

fn decimal_battery(edge: &str) -> Vec<Check> {
    match edge {
        "nominal" => vec![
            s(&["Decimal.fromInt", "Decimal.toString"], "Dec.toString (Dec.fromInt 5)", "5"),
            s(&["Decimal.add"], "Dec.toString (Dec.add (Dec.fromInt 1) (Dec.fromInt 2))", "3"),
            s(&["Decimal.sub"], "Dec.toString (Dec.sub (Dec.fromInt 3) (Dec.fromInt 1))", "2"),
            s(&["Decimal.mul"], "Dec.toString (Dec.mul (Dec.fromInt 3) (Dec.fromInt 4))", "12"),
            rs(&["Decimal.div"], "Result.map Dec.toString (Dec.div (Dec.fromInt 6) (Dec.fromInt 3))", Some("2")),
            s(&["Decimal.neg"], "Dec.toString (Dec.neg (Dec.fromInt 5))", "-5"),
            s(&["Decimal.abs"], "Dec.toString (Dec.abs (Dec.fromInt -5))", "5"),
            // `toStringFixed` KEEPS trailing zeros; `toString` drops them.
            s(&["Decimal.toStringFixed"], "Dec.toStringFixed 2 (Dec.fromInt 3)", "3.00"),
            s(&["Decimal.fromMinor"], "Dec.toString (Dec.fromMinor 2 12345)", "123.45"),
            i(&["Decimal.toMinor"], "Dec.toMinor 2 (Dec.fromMinor 2 314)", 314),
            i(&["Decimal.toInt"], "Dec.toInt (Dec.fromMinor 2 350)", 3),
            i(&["Decimal.compare"], "Dec.compare (Dec.fromInt 1) (Dec.fromInt 2)", -1),
            i(&["Decimal.compare"], "Dec.compare (Dec.fromInt 2) (Dec.fromInt 2)", 0),
            i(&["Decimal.compare"], "Dec.compare (Dec.fromInt 3) (Dec.fromInt 2)", 1),
            bo(&["Decimal.eq"], "Dec.eq (Dec.fromInt 2) (Dec.fromInt 2)", true),
            bo(&["Decimal.lt"], "Dec.lt (Dec.fromInt 1) (Dec.fromInt 2)", true),
            bo(&["Decimal.gt"], "Dec.gt (Dec.fromInt 3) (Dec.fromInt 2)", true),
            bo(&["Decimal.gte"], "Dec.gte (Dec.fromInt 2) (Dec.fromInt 2)", true),
            bo(&["Decimal.lte"], "Dec.lte (Dec.fromInt 2) (Dec.fromInt 2)", true),
            bo(&["Decimal.neq"], "Dec.neq (Dec.fromInt 1) (Dec.fromInt 2)", true),
            s(&["Decimal.min"], "Dec.toString (Dec.min (Dec.fromInt 1) (Dec.fromInt 2))", "1"),
            s(&["Decimal.max"], "Dec.toString (Dec.max (Dec.fromInt 1) (Dec.fromInt 2))", "2"),
            rs(&["Decimal.fromString"], "Result.map Dec.toString (Dec.fromString \"1.25\")", Some("1.25")),
            s(&["Decimal.sum"], "Dec.toString (Dec.sum [ Dec.fromInt 1, Dec.fromInt 2 ])", "3"),
            s(&["Decimal.percentOf"], "Dec.toString (Dec.percentOf (Dec.fromInt 20) (Dec.fromInt 100))", "20"),
            s(&["Decimal.addPercent"], "Dec.toString (Dec.addPercent (Dec.fromInt 10) (Dec.fromInt 100))", "110"),
            s(&["Decimal.subPercent"], "Dec.toString (Dec.subPercent (Dec.fromInt 10) (Dec.fromInt 100))", "90"),
            s(&["Decimal.fromFloat"], "Dec.toString (Dec.fromFloat 1.5)", "1.5"),
            f(&["Decimal.toFloat"], "Dec.toFloat (Dec.fromInt 2)", "2"),
            s(&["Decimal.zero"], "Dec.toString Dec.zero", "0"),
            s(&["Decimal.one"], "Dec.toString Dec.one", "1"),
            s(&["Decimal.oneHundred"], "Dec.toString Dec.oneHundred", "100"),
            s(
                &["Decimal.formatWith"],
                "Dec.formatWith \",\" \".\" 2 (Dec.fromMinor 2 123456789)",
                "1,234,567.89",
            ),
        ],
        // `sum []` is the additive identity.
        "empty" => vec![
            s(&["Decimal.sum"], "Dec.toString (Dec.sum emptyDecimals)", "0"),
            bo(&["Decimal.isZero"], "Dec.isZero Dec.zero", true),
        ],
        // The two rounding modes MUST differ at the exact half. `round` is
        // banker's (half to EVEN); `roundHalfUp` is half away from zero. A
        // money system that gets this wrong loses a cent per transaction.
        "boundary" => vec![
            s(&["Decimal.round"], "Dec.toString (Dec.round 0 (Dec.fromMinor 1 25))", "2"),
            s(&["Decimal.round"], "Dec.toString (Dec.round 0 (Dec.fromMinor 1 35))", "4"),
            s(&["Decimal.roundHalfUp"], "Dec.toString (Dec.roundHalfUp 0 (Dec.fromMinor 1 25))", "3"),
            s(&["Decimal.roundHalfUp"], "Dec.toString (Dec.roundHalfUp 0 (Dec.fromMinor 1 35))", "4"),
            s(&["Decimal.truncate"], "Dec.toString (Dec.truncate 0 (Dec.fromMinor 1 19))", "1"),
            s(&["Decimal.floor"], "Dec.toString (Dec.floor (Dec.fromMinor 1 19))", "1"),
            s(&["Decimal.ceil"], "Dec.toString (Dec.ceil (Dec.fromMinor 1 11))", "2"),
            s(&["Decimal.floor"], "Dec.toString (Dec.floor (Dec.fromMinor 1 -19))", "-2"),
            s(&["Decimal.ceil"], "Dec.toString (Dec.ceil (Dec.fromMinor 1 -19))", "-1"),
            // Scale-insensitive equality: 2.50 == 2.5.
            bo(&["Decimal.eq"], "Dec.eq (Dec.fromMinor 2 250) (Dec.fromMinor 1 25)", true),
            bo(&["Decimal.isPositive"], "Dec.isPositive Dec.zero", false),
            bo(&["Decimal.isNegative"], "Dec.isNegative Dec.zero", false),
            bo(&["Decimal.isNegative"], "Dec.isNegative (Dec.fromInt -1)", true),
        ],
        "failure" => vec![
            rs(&["Decimal.fromString"], "Result.map Dec.toString (Dec.fromString \"abc\")", None),
            rs(&["Decimal.fromString"], "Result.map Dec.toString (Dec.fromString \"\")", None),
            // Division by zero is an Err, never a panic and never an infinity.
            rs(&["Decimal.div"], "Result.map Dec.toString (Dec.div Dec.one Dec.zero)", None),
            rs(&["Decimal.mod"], "Result.map Dec.toString (Dec.mod Dec.one Dec.zero)", None),
        ],
        _ => vec![],
    }
}

// --- Std.Money -------------------------------------------------------------

fn money_battery(edge: &str) -> Vec<Check> {
    match edge {
        "nominal" => vec![
            s(&["Money.format", "Money.fromMinor"], "Money.format (Money.fromMinor USD 1234)", "$12.34"),
            s(&["Money.formatWithCode"], "Money.formatWithCode (Money.fromMinor USD 1234)", "12.34 USD"),
            s(&["Money.format"], "Money.format (Money.fromMajor USD 5)", "$5.00"),
            i(&["Money.toMinor"], "Money.toMinor (Money.fromMinor USD 1234)", 1234),
            s(&["Money.add"], "Money.format (Money.add (Money.fromMinor USD 100) (Money.fromMinor USD 250))", "$3.50"),
            s(&["Money.sub"], "Money.format (Money.sub (Money.fromMinor USD 500) (Money.fromMinor USD 250))", "$2.50"),
            s(&["Money.neg"], "Money.format (Money.neg (Money.fromMinor USD 100))", "-$1.00"),
            s(&["Money.abs"], "Money.format (Money.abs (Money.fromMinor USD -100))", "$1.00"),
            s(&["Money.currencyCode"], "Money.currencyCode USD", "USD"),
            s(&["Money.symbol"], "Money.symbol USD", "$"),
            i(&["Money.minorUnits"], "Money.minorUnits USD", 2),
            bo(&["Money.eq"], "Money.eq (Money.fromMinor USD 100) (Money.fromMinor USD 100)", true),
            bo(&["Money.lt"], "Money.lt (Money.fromMinor USD 100) (Money.fromMinor USD 200)", true),
            bo(&["Money.isPositive"], "Money.isPositive (Money.fromMinor USD 1)", true),
            bo(&["Money.knownCurrency"], "Money.knownCurrency USD", true),
            bo(&["Money.isKnownCode"], "Money.isKnownCode \"usd\"", true),
            s(&["Money.parseCurrency"], "Money.currencyCode (Money.parseCurrency \" eur \")", "EUR"),
            s(&["Money.zero"], "Money.format (Money.zero USD)", "$0.00"),
            s(&["Money.sumOf"], "Money.format (Money.sumOf USD [ Money.fromMinor USD 100, Money.fromMinor USD 200 ])", "$3.00"),
            // Allocation must be LOSSLESS: the residue lands at the front and
            // the parts sum back to the whole.
            ls(
                &["Money.allocate"],
                "List.map Money.format (Money.allocate 3 (Money.fromMinor USD 10000))",
                "$33.34,$33.33,$33.33",
            ),
            i(
                &["Money.allocate"],
                "List.foldl (\\m acc -> Money.toMinor m + acc) 0 (Money.allocate 3 (Money.fromMinor USD 10000))",
                10000,
            ),
            s(&["Money.fromMajor"], "Money.format (Money.fromMajor USD 7)", "$7.00"),
            s(&["Money.currency"], "Money.currencyCode (Money.currency (Money.fromMinor EUR 1))", "EUR"),
            s(&["Money.amount"], "Dec.toString (Money.amount (Money.fromMinor USD 250))", "2.5"),
            s(&["Money.zeroOf"], "Money.format (Money.zeroOf (Money.fromMinor EUR 500))", "€0.00"),
            s(&["Money.mul"], "Money.format (Money.mul (Dec.fromInt 3) (Money.fromMinor USD 100))", "$3.00"),
            s(&["Money.percentOf"], "Money.format (Money.percentOf (Dec.fromInt 20) (Money.fromMinor USD 10000))", "$20.00"),
            s(&["Money.addPercent"], "Money.format (Money.addPercent (Dec.fromInt 10) (Money.fromMinor USD 10000))", "$110.00"),
            s(&["Money.subPercent"], "Money.format (Money.subPercent (Dec.fromInt 10) (Money.fromMinor USD 10000))", "$90.00"),
            s(&["Money.currencyName"], "Money.currencyName (CurrencyRaw \"ZZZ\")", "ZZZ"),
            i(&["Money.compare"], "Money.compare (Money.fromMinor USD 100) (Money.fromMinor USD 200)", -1),
            i(&["Money.compare"], "Money.compare (Money.fromMinor USD 200) (Money.fromMinor USD 200)", 0),
            bo(&["Money.neq"], "Money.neq (Money.fromMinor USD 100) (Money.fromMinor USD 200)", true),
            bo(&["Money.gt"], "Money.gt (Money.fromMinor USD 200) (Money.fromMinor USD 100)", true),
            bo(&["Money.gte"], "Money.gte (Money.fromMinor USD 200) (Money.fromMinor USD 200)", true),
            bo(&["Money.lte"], "Money.lte (Money.fromMinor USD 200) (Money.fromMinor USD 200)", true),
            bo(&["Money.isNegative"], "Money.isNegative (Money.fromMinor USD -1)", true),
            // The FX registry. `setRate` auto-registers the inverse, so a
            // round-trip through both directions must return the original.
            // These touch process-global state, which is safe here because
            // every corpus case is its own program.
            bo(&["Money.hasRate"], "Money.hasRate USD USD", true),
            rs(
                &["Money.setRate", "Money.getRate"],
                "Result.andThen (\\_ -> Result.map Dec.toString (Money.getRate USD EUR)) (Money.setRate USD EUR (Dec.fromInt 2))",
                Some("2"),
            ),
            rs(
                &["Money.setRate", "Money.convert"],
                "Result.andThen (\\_ -> Result.map Money.format (Money.convert EUR (Money.fromMinor USD 100))) (Money.setRate USD EUR (Dec.fromInt 2))",
                Some("€2.00"),
            ),
            // `clearRates` really clears: the rate registered above is gone.
            rs(
                &["Money.clearRates"],
                "Result.andThen (\\_ -> Result.andThen (\\_ -> Result.map Dec.toString (Money.getRate USD EUR)) (Money.clearRates ())) (Money.setRate USD EUR (Dec.fromInt 2))",
                None,
            ),
        ],
        // Zero-decimal and three-decimal currencies. A hard-coded 2 breaks both.
        "boundary" => vec![
            i(&["Money.minorUnits"], "Money.minorUnits JPY", 0),
            i(&["Money.minorUnits"], "Money.minorUnits BHD", 3),
            i(&["Money.minorUnits"], "Money.minorUnits BTC", 8),
            s(&["Money.format"], "Money.format (Money.fromMinor JPY 1234)", "¥1234"),
            s(&["Money.formatWithCode"], "Money.formatWithCode (Money.fromMinor JPY 1234)", "1234 JPY"),
            s(&["Money.format"], "Money.format (Money.zero JPY)", "¥0"),
            // Allocation of an amount that does not divide evenly, and of a
            // negative total.
            i(
                &["Money.allocate"],
                "List.foldl (\\m acc -> Money.toMinor m + acc) 0 (Money.allocate 7 (Money.fromMinor USD 100))",
                100,
            ),
            i(
                &["Money.allocate"],
                "List.foldl (\\m acc -> Money.toMinor m + acc) 0 (Money.allocate 3 (Money.fromMinor USD -100))",
                -100,
            ),
            ln(&["Money.allocate"], "Money.allocate 1 (Money.fromMinor USD 100)", 1),
            bo(&["Money.isZero"], "Money.isZero (Money.zero USD)", true),
            bo(&["Money.isPositive"], "Money.isPositive (Money.zero USD)", false),
        ],
        "empty" => vec![
            s(&["Money.sumOf"], "Money.format (Money.sumOf USD emptyMoneys)", "$0.00"),
            // `allocate` with a non-positive part count is the empty list, not
            // a divide-by-zero.
            ln(&["Money.allocate"], "Money.allocate 0 (Money.fromMinor USD 100)", 0),
            ln(&["Money.allocate"], "Money.allocate -1 (Money.fromMinor USD 100)", 0),
        ],
        "failure" => vec![
            rs(&["Money.fromString"], "Result.map Money.format (Money.fromString USD \"abc\")", None),
            bo(&["Money.knownCurrency"], "Money.knownCurrency (CurrencyRaw \"ZZZ\")", false),
            bo(&["Money.isKnownCode"], "Money.isKnownCode \"ZZZ\"", false),
            // An unknown code still formats rather than crashing, and its
            // symbol falls back to the code.
            s(&["Money.symbol"], "Money.symbol (CurrencyRaw \"ZZZ\")", "ZZZ"),
            i(&["Money.minorUnits"], "Money.minorUnits (CurrencyRaw \"ZZZ\")", 2),
            // Currency mismatch on `add` is SILENT and returns the left
            // operand. That is a sharp edge, it is what the module does, and
            // pinning it is how a future change to it becomes visible.
            s(
                &["Money.add"],
                "Money.format (Money.add (Money.fromMinor USD 100) (Money.fromMinor EUR 100))",
                "$1.00",
            ),
            bo(
                &["Money.eq"],
                "Money.eq (Money.fromMinor USD 100) (Money.fromMinor EUR 100)",
                false,
            ),
        ],
        _ => vec![],
    }
}

// --- Std.Csv ---------------------------------------------------------------
//
// Newlines are rendered as `;` so the whole battery stays one comparable line.

fn csv_battery(edge: &str) -> Vec<Check> {
    match edge {
        "nominal" => vec![
            s(
                &["Csv.encode"],
                "String.replace \"\\n\" \";\" (Csv.encode { header = [ \"a\", \"b\" ], rows = [ [ \"1\", \"2\" ] ] })",
                "a,b;1,2;",
            ),
            ln(
                &["Csv.parse"],
                "Result.withDefault [] (Result.map .header (Csv.parse \"a,b\\n1,2\"))",
                2,
            ),
            ln(
                &["Csv.parse"],
                "Result.withDefault [] (Result.map .rows (Csv.parse \"a,b\\n1,2\"))",
                1,
            ),
            s(&["Csv.defaultCsv"], "Csv.encode Csv.defaultCsv", ""),
            ls(
                &["Csv.withHeader"],
                "(Csv.withHeader [ \"x\" ] Csv.defaultCsv).header",
                "x",
            ),
            ln(
                &["Csv.withRows"],
                "(Csv.withRows [ [ \"1\" ] ] Csv.defaultCsv).rows",
                1,
            ),
            s(
                &["Csv.encodeWithDelimiter"],
                "String.replace \"\\n\" \";\" (Csv.encodeWithDelimiter \";\" { header = [ \"a\", \"b\" ], rows = [] })",
                "a;b;",
            ),
            ln(
                &["Csv.parseWithDelimiter"],
                "Result.withDefault [] (Result.map .header (Csv.parseWithDelimiter \"\\t\" \"a\\tb\"))",
                2,
            ),
        ],
        // An empty document parses to an empty document, and encodes back to
        // the empty string — not to a stray newline.
        "empty" => vec![
            ln(&["Csv.parse"], "Result.withDefault [ \"x\" ] (Result.map .header (Csv.parse \"\"))", 0),
            ln(&["Csv.parse"], "Result.withDefault [ [ \"x\" ] ] (Result.map .rows (Csv.parse \"\"))", 0),
            s(&["Csv.encode"], "Csv.encode { header = [], rows = [] }", ""),
        ],
        // RFC 4180 quoting: a cell containing the delimiter, a quote, or a
        // newline must come back out intact.
        "boundary" => vec![
            s(
                &["Csv.encode"],
                "String.replace \"\\n\" \";\" (Csv.encode { header = [], rows = [ [ \"a,b\" ] ] })",
                "\"a,b\";",
            ),
            s(
                &["Csv.encode"],
                "String.replace \"\\n\" \";\" (Csv.encode { header = [], rows = [ [ \"say \\\"hi\\\"\" ] ] })",
                "\"say \"\"hi\"\"\";",
            ),
            // Round-trip through parse: a quoted cell holding the delimiter is
            // ONE cell, not two.
            ln(
                &["Csv.parse", "Csv.encode"],
                "Result.withDefault [] (Result.map .header (Csv.parse (Csv.encode { header = [ \"a,b\" ], rows = [] })))",
                1,
            ),
            // Header-only input: no rows.
            ln(&["Csv.parse"], "Result.withDefault [ [ \"x\" ] ] (Result.map .rows (Csv.parse \"a,b\"))", 0),
            // An empty cell survives.
            ln(&["Csv.parse"], "Result.withDefault [] (Result.map .header (Csv.parse \"a,,b\"))", 3),
        ],
        "unicode" => vec![
            ls(
                &["Csv.parse"],
                "Result.withDefault [] (Result.map .header (Csv.parse \"世界,é\"))",
                "世界,é",
            ),
            s(
                &["Csv.encode"],
                "String.replace \"\\n\" \";\" (Csv.encode { header = [ \"世\" ], rows = [] })",
                "世;",
            ),
        ],
        "failure" => vec![
            // A bare quote inside an unquoted field is malformed.
            rs(
                &["Csv.parse"],
                "Result.map (\\c -> String.join \",\" c.header) (Csv.parse \"a,\\\"b\\\"c\")",
                None,
            ),
        ],
        _ => vec![],
    }
}

// --- Sky.Core.Regex --------------------------------------------------------

fn regex_battery(edge: &str) -> Vec<Check> {
    match edge {
        "nominal" => vec![
            bo(&["Regex.match"], "Regex.match \"^a\" \"abc\"", true),
            bo(&["Regex.match"], "Regex.match \"c$\" \"abc\"", true),
            ms(&["Regex.find"], "Regex.find \"b+\" \"abbbc\"", Some("bbb")),
            ln(&["Regex.findAll"], "Regex.findAll \"a\" \"aba\"", 2),
            ls(&["Regex.findAll"], "Regex.findAll \"[0-9]+\" \"a1b22c\"", "1,22"),
            s(&["Regex.replace"], "Regex.replace \"a\" \"X\" \"aba\"", "XbX"),
            // `$1` group expansion in the replacement.
            s(&["Regex.replace"], "Regex.replace \"(a)(b)\" \"$2$1\" \"ab\"", "ba"),
            ls(&["Regex.split"], "Regex.split \",\" \"a,b,c\"", "a,b,c"),
            ln(&["Regex.split"], "Regex.split \"[,;]\" \"a,b;c\"", 3),
            // `find` returns the WHOLE match span, never a capture group.
            ms(&["Regex.find"], "Regex.find \"(foo)(bar)\" \"xxfoobaryy\"", Some("foobar")),
        ],
        "empty" => vec![
            ln(&["Regex.findAll"], "Regex.findAll \"a\" \"\"", 0),
            ms(&["Regex.find"], "Regex.find \"a\" \"\"", None),
            bo(&["Regex.match"], "Regex.match \"a\" \"\"", false),
            // Splitting an empty string yields one empty piece.
            ln(&["Regex.split"], "Regex.split \",\" \"\"", 1),
            s(&["Regex.replace"], "Regex.replace \"a\" \"X\" \"\"", ""),
        ],
        "boundary" => vec![
            // A match at the very start / end produces an empty leading /
            // trailing element.
            ln(&["Regex.split"], "Regex.split \",\" \",a\"", 2),
            ln(&["Regex.split"], "Regex.split \",\" \"a,\"", 2),
            // No match leaves the subject whole.
            ln(&["Regex.split"], "Regex.split \"z\" \"abc\"", 1),
            s(&["Regex.replace"], "Regex.replace \"z\" \"X\" \"abc\"", "abc"),
            ls(&["Regex.findAll"], "Regex.findAll \"a*\" \"b\"", ","),
        ],
        "unicode" => vec![
            bo(&["Regex.match"], "Regex.match \"世\" \"世界\"", true),
            ms(&["Regex.find"], "Regex.find \"[世界]+\" \"a世界b\"", Some("世界")),
            s(&["Regex.replace"], "Regex.replace \"世\" \"X\" \"世界\"", "X界"),
        ],
        "failure" => vec![
            bo(&["Regex.match"], "Regex.match \"z\" \"abc\"", false),
            ms(&["Regex.find"], "Regex.find \"z\" \"abc\"", None),
            ln(&["Regex.findAll"], "Regex.findAll \"z\" \"abc\"", 0),
        ],
        _ => vec![],
    }
}

// --- Sky.Core.Json.Encode / .Decode ----------------------------------------

fn json_battery(edge: &str) -> Vec<Check> {
    match edge {
        "nominal" => vec![
            s(&["JsonEncode.int", "JsonEncode.encode"], "Encode.encode 0 (Encode.int 1)", "1"),
            s(&["JsonEncode.string"], "Encode.encode 0 (Encode.string \"a\")", "\"a\""),
            s(&["JsonEncode.bool"], "Encode.encode 0 (Encode.bool True)", "true"),
            s(&["JsonEncode.null"], "Encode.encode 0 Encode.null", "null"),
            s(
                &["JsonEncode.list"],
                "Encode.encode 0 (Encode.list Encode.int [ 1, 2 ])",
                "[1,2]",
            ),
            // Object key order is INSERTION order, not alphabetical.
            s(
                &["JsonEncode.object"],
                "Encode.encode 0 (Encode.object [ ( \"b\", Encode.int 2 ), ( \"a\", Encode.int 1 ) ])",
                "{\"b\":2,\"a\":1}",
            ),
            ri(&["JsonDecode.int", "JsonDecode.decodeString"], "Decode.decodeString Decode.int \"42\"", Some(42)),
            rs(&["JsonDecode.string"], "Decode.decodeString Decode.string \"\\\"hi\\\"\"", Some("hi")),
            ri(
                &["JsonDecode.field"],
                "Decode.decodeString (Decode.field \"a\" Decode.int) \"{\\\"a\\\":1}\"",
                Some(1),
            ),
            ri(
                &["JsonDecode.at"],
                "Decode.decodeString (Decode.at [ \"a\", \"b\" ] Decode.int) \"{\\\"a\\\":{\\\"b\\\":2}}\"",
                Some(2),
            ),
            ri(
                &["JsonDecode.index"],
                "Decode.decodeString (Decode.index 1 Decode.int) \"[7,8]\"",
                Some(8),
            ),
            ri(&["JsonDecode.map"], "Decode.decodeString (Decode.map (\\x -> x + 1) Decode.int) \"1\"", Some(2)),
            ri(&["JsonDecode.succeed"], "Decode.decodeString (Decode.succeed 9) \"null\"", Some(9)),
            ri(
                &["JsonDecode.andThen"],
                "Decode.decodeString (Decode.andThen (\\x -> Decode.succeed (x * 2)) Decode.int) \"3\"",
                Some(6),
            ),
            ri(
                &["JsonDecode.oneOf"],
                "Decode.decodeString (Decode.oneOf [ Decode.int, Decode.succeed 0 ]) \"\\\"x\\\"\"",
                Some(0),
            ),
            ri(
                &["JsonDecode.map2"],
                "Decode.decodeString (Decode.map2 (\\a b -> a + b) (Decode.field \"a\" Decode.int) (Decode.field \"b\" Decode.int)) \"{\\\"a\\\":1,\\\"b\\\":2}\"",
                Some(3),
            ),
            ln(
                &["JsonDecode.list"],
                "Result.withDefault [] (Decode.decodeString (Decode.list Decode.int) \"[1,2,3]\")",
                3,
            ),
            bo(&["JsonDecode.bool"], "Result.withDefault False (Decode.decodeString Decode.bool \"true\")", true),
            f(&["JsonDecode.float"], "Result.withDefault 0.0 (Decode.decodeString Decode.float \"1.5\")", "1.5"),
            ri(
                &["JsonDecode.map3"],
                "Decode.decodeString (Decode.map3 (\\a b c -> a + b + c) (Decode.index 0 Decode.int) (Decode.index 1 Decode.int) (Decode.index 2 Decode.int)) \"[1,2,3]\"",
                Some(6),
            ),
            ri(
                &["JsonDecode.map4"],
                "Decode.decodeString (Decode.map4 (\\a b c d -> a + b + c + d) (Decode.index 0 Decode.int) (Decode.index 1 Decode.int) (Decode.index 2 Decode.int) (Decode.index 3 Decode.int)) \"[1,2,3,4]\"",
                Some(10),
            ),
        ],
        // The empty object and the empty list must be `{}` / `[]`, NOT `null`.
        // A nil-slice regression produces `null` and every non-empty case still
        // passes.
        "empty" => vec![
            s(&["JsonEncode.object"], "Encode.encode 0 (Encode.object [])", "{}"),
            s(&["JsonEncode.list"], "Encode.encode 0 (Encode.list Encode.int emptyInts)", "[]"),
            s(&["JsonEncode.string"], "Encode.encode 0 (Encode.string \"\")", "\"\""),
            ln(
                &["JsonDecode.list"],
                "Result.withDefault [ 9 ] (Decode.decodeString (Decode.list Decode.int) \"[]\")",
                0,
            ),
        ],
        "boundary" => vec![
            // `indent = 0` is compact — no spaces, no newlines.
            s(
                &["JsonEncode.encode"],
                "Encode.encode 0 (Encode.object [ ( \"a\", Encode.list Encode.int [ 1 ] ) ])",
                "{\"a\":[1]}",
            ),
            // An indent > 0 must actually indent.
            bo(
                &["JsonEncode.encode"],
                "String.contains \"\\n\" (Encode.encode 2 (Encode.object [ ( \"a\", Encode.int 1 ) ]))",
                true,
            ),
            s(&["JsonEncode.int"], "Encode.encode 0 (Encode.int -7)", "-7"),
            // A whole float still encodes as a JSON number.
            s(&["JsonEncode.float"], "Encode.encode 0 (Encode.float 1.5)", "1.5"),
            // `int` accepts an integral-valued number in any JSON spelling but
            // REJECTS a fractional one.
            ri(&["JsonDecode.int"], "Decode.decodeString Decode.int \"3.0\"", Some(3)),
            // Escaping: a quote and a backslash must survive a round-trip.
            s(&["JsonEncode.string"], "Encode.encode 0 (Encode.string \"a\\\"b\")", "\"a\\\"b\""),
            // `at []` applies the inner decoder to the root.
            ri(&["JsonDecode.at"], "Decode.decodeString (Decode.at [] Decode.int) \"5\"", Some(5)),
        ],
        "unicode" => vec![
            s(&["JsonEncode.string"], "Encode.encode 0 (Encode.string \"世\")", "\"世\""),
            rs(
                &["JsonDecode.string"],
                "Decode.decodeString Decode.string (Encode.encode 0 (Encode.string \"世界🎉\"))",
                Some("世界🎉"),
            ),
        ],
        "failure" => vec![
            ri(&["JsonDecode.int"], "Decode.decodeString Decode.int \"\\\"x\\\"\"", None),
            ri(&["JsonDecode.int"], "Decode.decodeString Decode.int \"1.5\"", None),
            rs(&["JsonDecode.string"], "Decode.decodeString Decode.string \"1\"", None),
            // Invalid JSON text.
            ri(&["JsonDecode.decodeString"], "Decode.decodeString Decode.int \"{\"", None),
            // A missing field is an Err — there is no implicit `maybe`.
            ri(
                &["JsonDecode.field"],
                "Decode.decodeString (Decode.field \"nope\" Decode.int) \"{\\\"a\\\":1}\"",
                None,
            ),
            // An out-of-range index is an Err, never a panic.
            ri(&["JsonDecode.index"], "Decode.decodeString (Decode.index 5 Decode.int) \"[1]\"", None),
            ri(&["JsonDecode.index"], "Decode.decodeString (Decode.index -1 Decode.int) \"[1]\"", None),
            // `fail` always fails; `oneOf []` has nothing that can match.
            ri(&["JsonDecode.fail"], "Decode.decodeString (Decode.fail \"nope\") \"1\"", None),
            ri(&["JsonDecode.oneOf"], "Decode.decodeString (Decode.oneOf emptyIntDecoders) \"1\"", None),
        ],
        _ => vec![],
    }
}

// ---------------------------------------------------------------------------
// Per-surface fixtures
// ---------------------------------------------------------------------------

/// Top-level bindings a surface's battery refers to.
///
/// These exist because an empty literal has no type to infer from. `[]` alone
/// is ambiguous; `emptyInts : List Int` is not — and writing the annotation
/// once, here, keeps the battery entries readable.
fn fixtures(slug: &str) -> &'static str {
    match slug {
        "list" => "emptyInts : List Int\nemptyInts =\n    []\n",
        "dict" => "emptyDict : Dict String Int\nemptyDict =\n    Dict.empty\n",
        "set" => "emptySet : Set Int\nemptySet =\n    Set.empty\n",
        "maybe" => {
            "nothingInt : Maybe Int\nnothingInt =\n    Nothing\n\n\n\
             emptyMaybes : List (Maybe Int)\nemptyMaybes =\n    []\n"
        }
        "result" => {
            "okInt : Int -> Result Error Int\nokInt n =\n    Ok n\n\n\n\
             okFn : (Int -> Int) -> Result Error (Int -> Int)\nokFn fn =\n    Ok fn\n\n\n\
             errInt : Result Error Int\nerrInt =\n    Err (Error.io \"boom\")\n\n\n\
             emptyResults : List (Result Error Int)\nemptyResults =\n    []\n"
        }
        "crypto" => {
            // The AEAD keys are `Sky.Core.Secret` since the Secret migration; the
            // password arg is a Secret too (a literal to `fromString` is a compile
            // error, so the corpus uses the lint-exempt `unsafeFromString`).
            "aesKey : Secret\naesKey =\n    Crypto.aesKeyFromPassword (Secret.unsafeFromString \"pw\") \"salt-1234567890\"\n\n\n\
             chachaKey : Secret\nchachaKey =\n    Crypto.chachaKeyFromPassword (Secret.unsafeFromString \"pw\") \"salt-1234567890\"\n"
        }
        "decimal" => "emptyDecimals : List Decimal\nemptyDecimals =\n    []\n",
        "money" => "emptyMoneys : List Money\nemptyMoneys =\n    []\n",
        "json" => {
            "emptyInts : List Int\nemptyInts =\n    []\n\n\n\
             emptyIntDecoders : List (Decoder Int)\nemptyIntDecoders =\n    []\n"
        }
        // `tok` is the fixed token every `Jwt.decode` item verifies against, and
        // `signed` is "encode these claims with the same key, or die" — the
        // decode assertions are about DECODE, so an encode failure must not be
        // laundered into a decode `Err` that would pass a `None` expectation.
        "jwt" => {
            "tok : String\ntok =\n    signed (Jwt.subject \"u1\" Jwt.claims)\n\n\n\
             signed : Jwt.Claims -> String\nsigned c =\n    rok (Jwt.encode (Jwt.hs256 (Secret.unsafeFromString \"k\")) c)\n\n\n\
             rok : Result Error String -> String\nrok r =\n    case r of\n\
             \x20       Ok v ->\n            v\n\n        Err _ ->\n            \"ENCODE-FAILED\"\n"
        }
        "codec" => {
            "type alias P =\n    { name : String, priceMinor : Int }\n\n\n\
             type Colour\n    = Red\n    | Blue\n\n\n\
             blank : P\nblank =\n    { name = \"\", priceMinor = 0 }\n\n\n\
             emptyInts : List Int\nemptyInts =\n    []\n\n\n\
             pcodec : Codec P\npcodec =\n    Codec.buildObject\n        (Codec.object P\n\
             \x20           |> Codec.field \"name\" .name Codec.string\n\
             \x20           |> Codec.field \"priceMinor\" .priceMinor Codec.int\n        )\n\n\n\
             colourCodec : Codec Colour\ncolourCodec =\n    Codec.enum [ ( Red, \"red\" ), ( Blue, \"blue\" ) ]\n\n\n\
             colourName : Colour -> String\ncolourName c =\n    case c of\n\
             \x20       Red ->\n            \"red\"\n\n        Blue ->\n            \"blue\"\n\n\n\
             recordCols : Shape -> List ( String, ColType )\nrecordCols sh =\n    case sh of\n\
             \x20       SRecord cols ->\n            cols\n\n        _ ->\n            []\n\n\n\
             scalarTag : Shape -> String\nscalarTag sh =\n    case sh of\n\
             \x20       SScalar CInt ->\n            \"int\"\n\n        SScalar _ ->\n            \"other\"\n\n\
             \x20       _ ->\n            \"not-scalar\"\n"
        }
        // The Element fold. `mdRaw` is the module's security promise expressed
        // as a number: `Ui.Raw` is the only constructor that can carry
        // unescaped HTML, so "never emits raw HTML" IS "this count is zero".
        "markdown" => {
            "mdText : String -> String\nmdText src =\n    uiText (Markdown.render src)\n\n\n\
             mdInline : String -> String\nmdInline src =\n    uiText (Markdown.renderInline src)\n\n\n\
             mdRaw : String -> Int\nmdRaw src =\n    uiRaw (Markdown.render src)\n\n\n\
             mdHtml : String -> String\nmdHtml src =\n    Html.render (Ui.layout [] (Markdown.render src))\n\n\n\
             hasBlank : String -> Bool\nhasBlank src =\n    String.contains \"href=\\\"about:blank\\\"\" (mdHtml src)\n\n\n\
             hasBlankSrc : String -> Bool\nhasBlankSrc src =\n    String.contains \"src=\\\"about:blank\\\"\" (mdHtml src)\n\n\n\
             hasScript : String -> Bool\nhasScript src =\n    String.contains \"javascript:\" (mdHtml src)\n\n\n\
             uiText : Element msg -> String\nuiText el =\n    case el of\n\
             \x20       Empty ->\n            \"\"\n\n        Text t ->\n            t\n\n\
             \x20       Node _ _ kids ->\n            String.concat (List.map uiText kids)\n\n\
             \x20       TaggedNode _ _ _ kids ->\n            String.concat (List.map uiText kids)\n\n\
             \x20       Raw _ ->\n            \"<RAW>\"\n\n\n\
             uiRaw : Element msg -> Int\nuiRaw el =\n    case el of\n\
             \x20       Empty ->\n            0\n\n        Text _ ->\n            0\n\n\
             \x20       Node _ _ kids ->\n            List.foldl (\\k acc -> acc + uiRaw k) 0 kids\n\n\
             \x20       TaggedNode _ _ _ kids ->\n            List.foldl (\\k acc -> acc + uiRaw k) 0 kids\n\n\
             \x20       Raw _ ->\n            1\n"
        }
        // `Task.run` is the bridge from `Task Error String` to a value.
        // `runOf` renders the length for inputs too large to spell as an
        // expectation, so the identity is still asserted without pasting 1000
        // characters into a table.
        "compression" => {
            "runOf : Task Error String -> String\nrunOf tk =\n    case Task.run tk of\n\
             \x20       Ok v ->\n            if String.length v > 64 then\n\
             \x20               String.fromInt (String.length v)\n\n            else\n                v\n\n\
             \x20       Err _ ->\n            \"E\"\n\n\n\
             headOf : Task Error String -> String\nheadOf tk =\n    case Task.run tk of\n\
             \x20       Ok v ->\n            Bytes.toHex (Bytes.slice 0 3 v)\n\n\
             \x20       Err _ ->\n            \"E\"\n"
        }
        _ => "",
    }
}

/// Extra imports a surface's battery needs beyond its own module.
fn extra_imports(slug: &str) -> &'static [&'static str] {
    match slug {
        // The Money battery states amounts and FX rates as `Decimal`s.
        "money" => &["Std.Decimal as Dec"],
        // `Result.mapError` is asserted against `Error.toString`, and `errInt`
        // is built with `Error.io`.
        "result" => &["Sky.Core.Error as Error"],
        // The AEAD keys are `Sky.Core.Secret` since the Secret migration; the
        // battery builds them with `Secret.unsafeFromString`.
        "crypto" => &["Sky.Core.Secret as Secret"],
        // `Jwt.hs256` takes a `Secret` signing key (Secret migration); the
        // battery wraps the literal key with `Secret.unsafeFromString`.
        "jwt" => &["Sky.Core.Secret as Secret"],
        // `Result.map` lifts a projection over a decode result, so a failure
        // stays a failure instead of being papered over by a default.
        "codec" => &["Sky.Core.Result as Result", "Sky.Core.Json.Encode as Encode"],
        // `Std.Money` needs `Decimal` only through its own re-exports; the
        // battery uses the `Currency` constructors, which `Std.Money` exposes.
        _ => &[],
    }
}

// ---------------------------------------------------------------------------
// Assembling a Family-S case
// ---------------------------------------------------------------------------

/// How a surface is imported and what qualifier the battery uses.
///
/// `Std.Decimal` is imported `as Dec` and `Sky.Core.Json.Encode` `as Encode`
/// because their last segments (`Decimal`, `Encode`) either collide with the
/// TYPE name the module exports or are too generic to read. Both are real
/// import shapes a user writes, so exercising them here is not a compromise.
fn import_alias(module: &str) -> Option<&'static str> {
    match module {
        "Std.Decimal" => Some("Dec"),
        "Sky.Core.Json.Encode" => Some("Encode"),
        _ => None,
    }
}

/// The `exposing (...)` clause a surface's battery needs.
///
/// Qualified access (`Money.format`) comes free with a plain import, but a
/// **constructor** does not: `USD`, `HttpStatus`, `Io` are values of a union the
/// module exports, and they only enter scope through an explicit
/// `exposing (Type(..))`. Discovered by the first build sweep of this family,
/// which failed six cases with `Undefined name: USD` / `NotFound` /
/// `HttpStatus` — a real property of the language the batteries now respect.
fn import_exposing(module: &str) -> Option<&'static str> {
    match module {
        // `Currency(..)` for the 59 currency constructors; `Money(..)` so the
        // `List Money` fixture can name the type.
        "Std.Money" => Some("Money(..), Currency(..)"),
        // `ErrorKind(..)` for `kindLabel Io`; `ErrorDetails(..)` for
        // `withDetails (HttpStatus 404)`.
        "Sky.Core.Error" => Some("ErrorKind(..), ErrorDetails(..)"),
        // `Codec` so the fixture can name the type; `Shape(..)` / `ColType(..)`
        // so `Codec.shape`'s result can be taken apart at all.
        "Std.Codec" => Some("Codec, Shape(..), ColType(..)"),
        // `Element(..)` so the Markdown case can FOLD the tree rather than
        // asserting a rendered string — which is what makes the `Raw`-node
        // count (the security promise) statable.
        "Std.Ui" => Some("Element(..)"),
        _ => None,
    }
}

fn import_line(module: &str) -> String {
    let alias = import_alias(module)
        .map(|a| format!(" as {a}"))
        .unwrap_or_default();
    let exposing = import_exposing(module)
        .map(|e| format!(" exposing ({e})"))
        .unwrap_or_default();
    format!("import {module}{alias}{exposing}\n")
}

/// Build the `stdlib_edge` case at `(surface, edge)`.
///
/// The battery becomes one `sN : String` top-level binding per item — rather
/// than a single deeply-nested expression — so a red case names the item that
/// went wrong in its diff position, and so no item's layout can disturb its
/// neighbour's.
pub fn stdlib_edge(a: &Assignment) -> (Body, String) {
    let slug = a.get(SURFACE);
    let edge = a.get(EDGE);
    let sf = surface(slug);
    let checks = battery(slug, edge);
    assert!(
        !checks.is_empty(),
        "stdlib_edge: surface {slug:?} has no battery at edge {edge:?}; the point \
         should have been filtered by axes::admissible, and a case with nothing \
         to assert is exactly the vacuity this corpus exists to prevent"
    );

    let mut imports = import_line(sf.module);
    for m in sf.also {
        // An `also` entry that already carries its own `as` alias is written
        // verbatim; a bare module path goes through `import_line` so any
        // `exposing (…)` clause it needs (`Std.Ui exposing (Element(..))`)
        // applies to it exactly as it does to the surface's own module.
        if m.contains(" as ") {
            imports.push_str(&format!("import {m}\n"));
        } else {
            imports.push_str(&import_line(m));
        }
    }
    for m in extra_imports(slug) {
        imports.push_str(&format!("import {m}\n"));
    }

    let mut decls = String::new();
    let fx = fixtures(slug);
    if !fx.is_empty() {
        decls.push_str(fx);
        decls.push_str("\n\n");
    }
    for (n, c) in checks.iter().enumerate() {
        decls.push_str(&format!("s{n} : String\ns{n} =\n    {}\n\n\n", c.body));
    }

    let items: Vec<String> = (0..checks.len()).map(|n| format!("s{n}")).collect();
    let check = format!(
        "String.join \"|\"\n        [ {}\n        ]",
        items.join("\n        , ")
    );

    let expected: Vec<&str> = checks.iter().map(|c| c.expect.as_str()).collect();
    (Body { imports, decls, check }, expected.join("|"))
}

// ---------------------------------------------------------------------------
// stdlib_import — the import-shape stratum, against REAL stdlib modules
// ---------------------------------------------------------------------------

/// The value a shadowing local returns. Chosen here, before any compiler runs.
const SHADOWED: i64 = 99;

/// Build the `stdlib_import` case at `(import_shape, shadow)`.
///
/// v2 §3.1: *"a generated hostile module graph that collides against fictional
/// stdlib names cannot reproduce #164 or the fieldset collision, both of which
/// required real stdlib names in scope."* The existing `import_shape` stratum
/// collides two LOCAL names and `witness.rs` records that its `collision` axis
/// is therefore **inert**. This one collides against `Sky.Core.String.length`
/// and `Sky.Core.List.length` — two real stdlib modules that genuinely export
/// the same bare name — and against a local definition of that same name.
///
/// The three `shadow` values produce three DIFFERENT programs with three
/// different generator-predicted values, so this stratum witnesses its
/// axis-under-test by emit-shape and asserts class-V values at the same time.
/// What a `stdlib_import` point compiles to.
///
/// Most points are one module carrying a [`Body`]; the ambiguity point is a
/// module GRAPH and expects a rejection, so the two shapes are distinguished
/// here rather than by a sentinel value inside `Body`.
pub enum ImportCase {
    /// One module, built from `body`, expected to print `stdout`.
    Single { body: Body, stdout: String },
    /// A module graph, expected to be REJECTED.
    Graph {
        modules: Vec<(String, String)>,
        entry: String,
        expect: Expect,
    },
}

/// The accepted TWIN for a `stdlib_import` case that expects rejection.
///
/// Family R's `every_reject_row_declares_a_twin` covers this stratum too, and
/// the rule earns its reach: without a twin, "the compiler rejected it" is
/// satisfied just as well by a compiler that rejects every two-module graph.
///
/// The twin here is the ambiguity case with the ambiguity REMOVED — `Ambig.Beta`
/// stops exposing `label`, so exactly one binding is in scope and the program
/// must compile and print `ALPHA`. Everything else is byte-identical, so a twin
/// failure means the graph shape is broken, not that ambiguity was detected.
///
/// Returns `None` for the accepting cases, which need no twin: they ARE the
/// positive evidence.
pub fn import_twin(a: &Assignment) -> Option<Vec<(String, String)>> {
    if a.get(SHADOW) != "ambiguous_exposing_all" {
        return None;
    }
    Some(vec![
        (
            "Ambig.Alpha".to_string(),
            "module Ambig.Alpha exposing (..)\n\n\
             import Sky.Core.Prelude exposing (..)\n\n\n\
             label : String\nlabel =\n    \"ALPHA\"\n"
                .to_string(),
        ),
        (
            "Ambig.Beta".to_string(),
            // Exposes something ELSE, so `label` has exactly one binding.
            "module Ambig.Beta exposing (other)\n\n\
             import Sky.Core.Prelude exposing (..)\n\n\n\
             other : String\nother =\n    \"BETA\"\n"
                .to_string(),
        ),
        (
            "Main".to_string(),
            "module Main exposing (main)\n\n\
             import Sky.Core.Prelude exposing (..)\n\
             import Std.Log exposing (println)\n\
             import Ambig.Alpha exposing (..)\n\
             import Ambig.Beta exposing (..)\n\n\n\
             main =\n    println label\n"
                .to_string(),
        ),
    ])
}

pub fn stdlib_import(a: &Assignment) -> ImportCase {
    let shape = a.get(IMPORT_SHAPE);
    let shadow = a.get(SHADOW);

    // ---- the ambiguity point ---------------------------------------------
    //
    // Two modules, both `exposing (..)`, both exporting `label : String`. The
    // reference is unqualified, so it names both. The correct answer is a
    // diagnostic; what happens today is that the LAST import silently wins, so
    // the value the program computes is a function of import ORDER. See
    // `gen::blocked_reason` for the measurement and `corpus/repro/` for the
    // hand-written reproduction.
    if shadow == "ambiguous_exposing_all" {
        let module = |name: &str, value: &str| {
            (
                name.to_string(),
                format!(
                    "module {name} exposing (..)\n\n\
                     import Sky.Core.Prelude exposing (..)\n\n\n\
                     label : String\nlabel =\n    \"{value}\"\n"
                ),
            )
        };
        let main = "module Main exposing (main)\n\n\
                    import Sky.Core.Prelude exposing (..)\n\
                    import Std.Log exposing (println)\n\
                    import Ambig.Alpha exposing (..)\n\
                    import Ambig.Beta exposing (..)\n\n\n\
                    main =\n    println label\n";
        return ImportCase::Graph {
            modules: vec![
                module("Ambig.Alpha", "ALPHA"),
                module("Ambig.Beta", "BETA"),
                ("Main".to_string(), main.to_string()),
            ],
            entry: "Main".to_string(),
            // The runner's verdict is rejection-vs-acceptance; the code names
            // the diagnostic the ambiguity is now reported under.
            expect: Expect::Reject {
                code: "E1012".to_string(),
            },
        };
    }

    // How `Sky.Core.String` enters scope, and the qualifier (or bare name) the
    // case reads `length` through.
    let (import, read_len): (String, String) = match shape {
        "plain" => (
            "import Sky.Core.String\n".into(),
            "String.length".into(),
        ),
        "aliased" => (
            "import Sky.Core.String as Str\n".into(),
            "Str.length".into(),
        ),
        // #164's regression shape: the alias is NOT the module's last segment,
        // which is what broke a real app when a qualifier heuristic "fixed" the
        // original bug.
        "alias_not_last_segment" => (
            "import Sky.Core.String as Core\n".into(),
            "Core.length".into(),
        ),
        "exposing_list" => (
            "import Sky.Core.String exposing (length)\n".into(),
            "length".into(),
        ),
        "exposing_all" => (
            "import Sky.Core.String exposing (..)\n".into(),
            "length".into(),
        ),
        other => panic!("stdlib_import: unknown shape {other:?}"),
    };

    let mut imports = import;
    let mut decls = String::new();
    let mut items: Vec<(String, String)> = Vec::new();

    match shadow {
        "none" => {
            items.push((format!("String.fromInt ({read_len} \"abc\")"), "3".into()));
        }
        // A local top-level binding with the SAME bare name as the imported
        // symbol. The qualified read must still reach the module; the bare read
        // must reach the local. Only admissible where the import does not also
        // bind the bare name (see `axes::admissible`), because a local
        // definition competing with an explicitly-exposed import is a language
        // question this generator does not get to answer for itself.
        "local_shadow" => {
            decls.push_str(&format!(
                "length : String -> Int\nlength _ =\n    {SHADOWED}\n\n\n"
            ));
            items.push((format!("String.fromInt ({read_len} \"abc\")"), "3".into()));
            items.push((
                "String.fromInt (length \"abc\")".into(),
                SHADOWED.to_string(),
            ));
        }
        // Two REAL stdlib modules that both export `length`, both in scope
        // under the same import shape, both read through their own qualifier.
        // This is the stdlib-name-collision class, with real names.
        "cross_stdlib" => {
            imports.push_str(&match shape {
                "plain" => "import Sky.Core.List\n".to_string(),
                "aliased" => "import Sky.Core.List as Lst\n".to_string(),
                "alias_not_last_segment" => "import Sky.Core.List as Seq\n".to_string(),
                "exposing_list" => "import Sky.Core.List exposing (reverse)\n".to_string(),
                "exposing_all" => "import Sky.Core.List exposing (..)\n".to_string(),
                _ => unreachable!(),
            });
            let list_len = match shape {
                "aliased" => "Lst.length",
                "alias_not_last_segment" => "Seq.length",
                _ => "List.length",
            };
            // BOTH reads are QUALIFIED here, including under the `exposing`
            // shapes where `read_len` would otherwise be the bare name.
            //
            // That is not a convenience. With `Sky.Core.String exposing (..)`
            // and `Sky.Core.List exposing (..)` both in scope, the bare name
            // `length` is exported by both modules, so it is AMBIGUOUS and the
            // compiler now rejects it with `[E1012]` (doc 05 §6b). These cases
            // assert a VALUE, so they must not be written in a form the language
            // rejects; the ambiguity itself is asserted by the
            // `ambiguous_exposing_all` case above and by
            // `rust/crates/ty/tests/reject/corpus/ambiguous_unqualified_name.sky`.
            let string_len = match shape {
                "aliased" => "Str.length",
                "alias_not_last_segment" => "Core.length",
                _ => "String.length",
            };
            items.push((format!("String.fromInt ({string_len} \"abcd\")"), "4".into()));
            items.push((
                format!("String.fromInt ({list_len} [ 1, 2, 3 ])"),
                "3".into(),
            ));
        }
        other => panic!("stdlib_import: unknown shadow {other:?}"),
    }

    for (n, (expr, _)) in items.iter().enumerate() {
        decls.push_str(&format!("s{n} : String\ns{n} =\n    {expr}\n\n\n"));
    }
    let names: Vec<String> = (0..items.len()).map(|n| format!("s{n}")).collect();
    let check = format!(
        "String.join \"/\"\n        [ {}\n        ]",
        names.join("\n        , ")
    );
    let expected: Vec<&str> = items.iter().map(|(_, e)| e.as_str()).collect();

    ImportCase::Single {
        body: Body {
            imports,
            decls,
            check,
        },
        stdout: expected.join("/"),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Read a stdlib module's `exposing (...)` list from source.
    ///
    /// Deliberately the SOURCE and not a hand-copied table: the coverage claim
    /// is only worth something if it drifts when the stdlib does, and a table
    /// checked in beside the batteries would not.
    fn exposing(root: &std::path::Path, module: &str) -> Option<Vec<String>> {
        let p = root
            .join("sky-stdlib")
            .join(module.replace('.', "/"))
            .with_extension("sky");
        let src = std::fs::read_to_string(p).ok()?;
        let head = src.split_once(&format!("module {module} exposing"))?.1;
        // `exposing (..)` re-exports everything; no list to check against.
        let body = head.split_once('(')?.1;
        let mut depth = 1i32;
        let mut list = String::new();
        for ch in body.chars() {
            match ch {
                '(' => depth += 1,
                ')' => {
                    depth -= 1;
                    if depth == 0 {
                        break;
                    }
                }
                _ => {}
            }
            list.push(ch);
        }
        if list.trim() == ".." {
            return None;
        }
        Some(
            list.split(',')
                .map(|s| s.trim().trim_end_matches("(..)").trim().to_string())
                .filter(|s| !s.is_empty() && !s.starts_with("--"))
                .collect(),
        )
    }

    fn repo_root() -> std::path::PathBuf {
        std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .ancestors()
            .nth(3)
            .expect("repo root")
            .to_path_buf()
    }

    /// Every `covers` tag must name a symbol its module actually exposes.
    ///
    /// Without this a battery could claim coverage of `String.slugify` — a real
    /// kernel name that `Sky.Core.String` does not export — and the coverage
    /// report would count it. A coverage numerator that can name things the
    /// denominator does not contain is not a measurement.
    #[test]
    fn every_covers_tag_names_a_symbol_its_module_exposes() {
        let root = repo_root();
        let mut unknown = Vec::new();
        for (module, sym) in covered_symbols() {
            // `kernel:<Pseudo>` names a kernel pseudo-module, which has no
            // `.sky` source and therefore no `exposing` list to contradict.
            // Those tags are checked against `hir` instead — see
            // `every_kernel_covers_tag_is_advertised_by_hir`, which is exactly
            // as strong: a typo fails there.
            if module.starts_with("kernel:") {
                continue;
            }
            let Some(list) = exposing(&root, &module) else {
                // `exposing (..)` — every top-level declaration is public, so
                // there is no list to contradict.
                continue;
            };
            if !list.iter().any(|n| *n == sym) {
                unknown.push(format!("{module}.{sym}"));
            }
        }
        assert!(
            unknown.is_empty(),
            "these `covers` tags name symbols their module does not expose: {unknown:?}"
        );
    }

    /// Every `Kernel.<Pseudo>.<name>` tag must name a member `hir` actually
    /// advertises for that pseudo-module.
    ///
    /// This is the kernel half of the `covers`-tag contract, and it is the
    /// mechanism that makes those symbols COUNTABLE rather than merely
    /// asserted: a tag that named nothing would previously have been mapped to
    /// `Sky.Core.Prelude`, a module no inventory contains, and would have
    /// vanished from both the numerator and the denominator without a word.
    #[test]
    fn every_kernel_covers_tag_is_advertised_by_hir() {
        let inv = kernel_inventory();
        let mut unknown = Vec::new();
        for (module, sym) in covered_symbols() {
            let Some(pseudo) = module.strip_prefix("kernel:") else {
                continue;
            };
            match inv.get(pseudo) {
                Some(members) if members.contains(&sym) => {}
                _ => unknown.push(format!("{pseudo}.{sym}")),
            }
        }
        assert!(
            unknown.is_empty(),
            "these `Kernel.*` covers tags name members no kernel pseudo-module \
             advertises (hir::KERNEL_FUNCTIONS): {unknown:?}"
        );
    }

    /// Both halves of every [`ROUTED_ONLY_KERNEL_MEMBERS`] row are checked, so
    /// the row cannot rot into a lie in either direction: the member must still
    /// be ROUTED by the lowerer (or the assertion is aimed at nothing), and it
    /// must still be UNADVERTISED by `hir` (or the row is stale and the symbol
    /// belongs in the ordinary inventory).
    #[test]
    fn every_routed_only_member_is_really_routed_and_really_unadvertised() {
        for (pseudo, member) in ROUTED_ONLY_KERNEL_MEMBERS {
            assert!(
                lower::kernel::kernel_go_name_opt(pseudo, member).is_some(),
                "{pseudo}.{member} is declared routed-only but `lower::kernel` \
                 does not route it — the row names nothing"
            );
            let advertised = hir::kernel_functions(pseudo)
                .map_or(false, |ms| ms.contains(member));
            assert!(
                !advertised,
                "{pseudo}.{member} IS advertised by hir::KERNEL_FUNCTIONS now — \
                 delete the ROUTED_ONLY_KERNEL_MEMBERS row; the drift it records \
                 has been closed"
            );
        }
    }

    /// The four symbols the ledger's gap list named — `toString`, `modBy`,
    /// `compare`, `negate` — are asserted AND countable.
    ///
    /// They were the worked example of a denominator that omits real symbols:
    /// asserted by Family S, tagged `Prelude.*`, mapped to a module that is in
    /// no inventory, and therefore invisible to every number the ledger prints.
    /// This test is what stops that regressing to invisible again.
    #[test]
    fn the_four_uncountable_basics_are_now_counted() {
        let covered = covered_symbols();
        for sym in ["toString", "modBy", "compare", "negate"] {
            assert!(
                covered.contains(&("kernel:Basics".to_string(), sym.to_string())),
                "`{sym}` is a kernel `Basics` member Family S asserts, but it is \
                 not attributed to the kernel namespace — so no denominator can \
                 see it"
            );
        }
        // And the inventory that divides them is non-empty and real.
        let inv = kernel_inventory();
        let basics = inv.get("Basics").expect("hir advertises a `Basics` pseudo-module");
        for sym in ["toString", "modBy", "compare", "negate"] {
            assert!(basics.contains(sym), "hir no longer advertises Basics.{sym}");
        }
    }

    /// The coverage claim is non-trivial: the batteries assert something about
    /// a substantial number of distinct public symbols. A regression that
    /// emptied the tables would otherwise leave every other test green — they
    /// all quantify over whatever the tables happen to contain.
    #[test]
    fn the_coverage_claim_is_not_empty() {
        let covered = covered_symbols();
        assert!(
            covered.len() >= 250,
            "Family S asserts against only {} distinct (module, symbol) pairs; \
             the tables have shrunk and the coverage claim with them",
            covered.len()
        );
        // …and it spans every surface, not just the big ones.
        for s in SURFACES {
            let owner = if s.slug == "json" {
                "Sky.Core.Json."
            } else {
                s.module
            };
            assert!(
                covered.iter().any(|(m, _)| m.starts_with(owner)),
                "surface {} contributes no covered symbol",
                s.slug
            );
        }
    }

    /// Every surface's module path must be a module that actually exists in
    /// `sky-stdlib/`. A battery aimed at a module that is not there would fail
    /// as a build error rather than as the coverage claim it is.
    #[test]
    fn every_surface_names_a_real_stdlib_module() {
        let root = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .ancestors()
            .nth(3)
            .expect("repo root")
            .to_path_buf();
        for s in SURFACES {
            let p = root
                .join("sky-stdlib")
                .join(s.module.replace('.', "/"))
                .with_extension("sky");
            assert!(
                p.exists(),
                "surface {:?} names module {:?}, which is not at {}",
                s.slug,
                s.module,
                p.display()
            );
        }
    }

    /// Surface slugs are unique, and every slug is a value of the `surface`
    /// axis. A slug in one and not the other silently drops a whole module
    /// from the corpus.
    #[test]
    fn surface_slugs_match_the_axis_exactly() {
        let slugs: std::collections::BTreeSet<&str> = SURFACES.iter().map(|s| s.slug).collect();
        assert_eq!(slugs.len(), SURFACES.len(), "duplicate surface slug");
        let axis: std::collections::BTreeSet<&str> = SURFACE.values.iter().copied().collect();
        assert_eq!(
            slugs, axis,
            "the `surface` axis and the SURFACES table disagree"
        );
    }

    /// No battery item may contain the separator the case joins on, in either
    /// its expression or its expected value — a `|` inside a value would shift
    /// every later item's diff position and make a red case unreadable.
    #[test]
    fn no_expected_value_contains_the_join_separator() {
        for s in SURFACES {
            for e in EDGE.values {
                for c in battery(s.slug, e) {
                    assert!(
                        !c.expect.contains('|'),
                        "{}/{}: expected value {:?} contains the join separator",
                        s.slug,
                        e,
                        c.expect
                    );
                }
            }
        }
    }

    /// No expectation may start or end with whitespace.
    ///
    /// The runner compares `stdout.trim()`, so an item whose expected value has
    /// a leading or trailing space is UNSTATEABLE when it lands first or last
    /// in the join order — the case goes red with the compiler in the right and
    /// the assertion in the wrong. `Error.toString (Error.io "")` hit exactly
    /// this on the family's first full run. Anchor the space with a visible
    /// character (`++ "."`) rather than dropping the assertion.
    #[test]
    fn no_expected_value_has_leading_or_trailing_whitespace() {
        for s in SURFACES {
            for e in EDGE.values {
                for c in battery(s.slug, e) {
                    assert_eq!(
                        c.expect.trim(),
                        c.expect,
                        "{}/{}: expected value {:?} has leading/trailing whitespace, \
                         which `stdout.trim()` erases",
                        s.slug,
                        e,
                        c.expect
                    );
                }
            }
        }
    }

    /// Every admissible point has at least one assertion, and every
    /// inadmissible point has none. This is the anti-vacuity invariant: a case
    /// that asserts nothing would still build, still run, and still report
    /// GREEN.
    #[test]
    fn admissibility_agrees_with_the_batteries() {
        for s in SURFACES {
            for e in EDGE.values {
                let n = battery(s.slug, e).len();
                let a = Assignment::new().with(SURFACE, s.slug).with(EDGE, e);
                let adm = admissible("stdlib_edge", &a);
                assert_eq!(
                    adm,
                    n > 0,
                    "surface {} edge {e}: admissible = {adm} but battery has {n} item(s)",
                    s.slug
                );
            }
        }
    }

    /// Every surface must have a `nominal` battery — the neutral value the
    /// witness gate compares against. Without it the gate has no baseline to
    /// build and would report the case as un-witnessable rather than as
    /// mis-declared.
    #[test]
    fn every_surface_has_a_nominal_battery() {
        for s in SURFACES {
            assert!(
                !battery(s.slug, "nominal").is_empty(),
                "surface {} has no `nominal` battery to neutralise against",
                s.slug
            );
        }
    }

    /// The whole point, stated as a test: a Family-S case's expected output is
    /// the concatenation of per-item expectations the GENERATOR chose, and it
    /// is never empty.
    #[test]
    fn every_case_carries_a_generator_constructed_expectation() {
        for s in SURFACES {
            for e in EDGE.values {
                let a = Assignment::new().with(SURFACE, s.slug).with(EDGE, e);
                if !admissible("stdlib_edge", &a) {
                    continue;
                }
                let (body, expected) = stdlib_edge(&a);
                assert!(!expected.is_empty(), "{}/{e}: empty expectation", s.slug);
                assert!(
                    body.check.contains("String.join"),
                    "{}/{e}: check does not join its items",
                    s.slug
                );
                assert_eq!(
                    expected.split('|').count(),
                    battery(s.slug, e).len(),
                    "{}/{e}: expectation has a different item count than the battery",
                    s.slug
                );
            }
        }
    }

    /// The `stdlib_import` stratum's three `shadow` values must produce three
    /// DIFFERENT expected values at the same import shape. If they did not, the
    /// axis would be inert — which is the defect `witness.rs` records against
    /// the older `import_shape` stratum, and this stratum exists to not repeat.
    #[test]
    fn the_shadow_axis_is_not_inert() {
        for shape in IMPORT_SHAPE.values {
            let mut seen = std::collections::BTreeSet::new();
            for sh in SHADOW.values {
                let a = Assignment::new().with(IMPORT_SHAPE, shape).with(SHADOW, sh);
                if !admissible("stdlib_import", &a) {
                    continue;
                }
                let expected = match stdlib_import(&a) {
                    ImportCase::Single { stdout, .. } => stdout,
                    // The rejection point asserts a diagnostic, not a value;
                    // it is distinct from every value point by construction.
                    ImportCase::Graph { .. } => format!("<reject:{sh}>"),
                };
                assert!(
                    seen.insert(expected.clone()),
                    "shape {shape}: shadow value {sh} produces the same expectation \
                     {expected:?} as another — the axis is inert there"
                );
            }
        }
    }
}

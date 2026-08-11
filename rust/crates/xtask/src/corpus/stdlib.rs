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

/// A `String`-valued expression.
fn s(covers: &'static [&'static str], expr: &str, expect: &str) -> Check {
    Check {
        body: expr.to_string(),
        expect: expect.to_string(),
        covers,
    }
}

/// An `Int`-valued expression.
fn i(covers: &'static [&'static str], expr: &str, expect: i64) -> Check {
    Check {
        body: format!("String.fromInt ({expr})"),
        expect: expect.to_string(),
        covers,
    }
}

/// A `Float`-valued expression, rendered through `String.fromFloat`.
fn f(covers: &'static [&'static str], expr: &str, expect: &str) -> Check {
    Check {
        body: format!("String.fromFloat ({expr})"),
        expect: expect.to_string(),
        covers,
    }
}

/// A `Bool`-valued expression. `"T"` / `"F"` rather than `toString`, so the
/// assertion does not also depend on how `Bool` renders.
fn bo(covers: &'static [&'static str], expr: &str, expect: bool) -> Check {
    Check {
        body: format!("if {expr} then\n        \"T\"\n\n    else\n        \"F\""),
        expect: if expect { "T" } else { "F" }.to_string(),
        covers,
    }
}

/// A `Maybe Int`. `Nothing` renders as `"N"`.
fn mi(covers: &'static [&'static str], expr: &str, expect: Option<i64>) -> Check {
    Check {
        body: format!(
            "case {expr} of\n        Just v ->\n            String.fromInt v\n\n        Nothing ->\n            \"N\""
        ),
        expect: expect.map(|v| v.to_string()).unwrap_or_else(|| "N".into()),
        covers,
    }
}

/// A `Maybe String`. `Nothing` renders as `"N"`.
fn ms(covers: &'static [&'static str], expr: &str, expect: Option<&str>) -> Check {
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
fn rs(covers: &'static [&'static str], expr: &str, expect: Option<&str>) -> Check {
    Check {
        body: format!(
            "case {expr} of\n        Ok v ->\n            v\n\n        Err _ ->\n            \"E\""
        ),
        expect: expect.unwrap_or("E").to_string(),
        covers,
    }
}

/// A `Result Error Int`.
fn ri(covers: &'static [&'static str], expr: &str, expect: Option<i64>) -> Check {
    Check {
        body: format!(
            "case {expr} of\n        Ok v ->\n            String.fromInt v\n\n        Err _ ->\n            \"E\""
        ),
        expect: expect.map(|v| v.to_string()).unwrap_or_else(|| "E".into()),
        covers,
    }
}

/// A `List Int`, rendered comma-joined.
fn li(covers: &'static [&'static str], expr: &str, expect: &str) -> Check {
    Check {
        body: format!("String.join \",\" (List.map String.fromInt ({expr}))"),
        expect: expect.to_string(),
        covers,
    }
}

/// A `List String`, rendered comma-joined.
fn ls(covers: &'static [&'static str], expr: &str, expect: &str) -> Check {
    Check {
        body: format!("String.join \",\" ({expr})"),
        expect: expect.to_string(),
        covers,
    }
}

/// The LENGTH of a list. Used where the elements' order or spelling is not a
/// promise the surface makes, so asserting them would be asserting the
/// implementation.
fn ln(covers: &'static [&'static str], expr: &str, expect: i64) -> Check {
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
                    let module = match q {
                        "JsonEncode" => "Sky.Core.Json.Encode",
                        "JsonDecode" => "Sky.Core.Json.Decode",
                        // `toString`, `modBy`, `compare`, `negate` and friends
                        // are reachable in EVERY Sky program but belong to no
                        // module's `exposing` list — they come from the kernel
                        // `Basics` pseudo-module (`hir/src/kernel.rs`
                        // BUILTIN_VARS / KERNEL_FUNCTIONS). So they appear in
                        // no `api/symbols.json` entry, `sky doc` cannot show
                        // them, and the coverage denominator cannot count them.
                        //
                        // Family S still ASSERTS them — `modBy 3 -1 == 2` is one
                        // of the sharpest edges in the language — but it does
                        // not claim them as covered surface, because there is no
                        // denominator entry to divide by. Recorded here so the
                        // discrepancy is visible rather than quietly rounded
                        // into `Sky.Core.Basics`, which does not export them.
                        "Prelude" => "Sky.Core.Prelude",
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
    out
}

/// Print what Family S covers, against the SAME stdlib inventory the coverage
/// ledger uses (`api/symbols.json`, via the `sky doc --export` code path).
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
    let tmp = std::env::temp_dir().join(format!("sky-familyS-report-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&tmp);
    let (proj, out) = (tmp.join("project"), tmp.join("site"));
    if std::fs::create_dir_all(&proj).and(std::fs::create_dir_all(&out)).is_err() {
        eprintln!("corpus.stdlib: cannot create {}", tmp.display());
        return 1;
    }
    let manifest = project::render_doc_site_export(root, &proj, &out)
        .map_err(|e| format!("sky doc --export code path FAILED: {e}"))
        .and_then(|()| {
            std::fs::read_to_string(out.join("api").join("symbols.json"))
                .map_err(|e| format!("no api/symbols.json: {e}"))
        });
    let _ = std::fs::remove_dir_all(&tmp);
    let manifest = match manifest {
        Ok(m) => m,
        Err(e) => {
            eprintln!("corpus.stdlib: {e}");
            return 1;
        }
    };
    let json: serde_json::Value = match serde_json::from_str(&manifest) {
        Ok(v) => v,
        Err(e) => {
            eprintln!("corpus.stdlib: symbols.json is not JSON: {e}");
            return 1;
        }
    };
    let mut inventory: std::collections::BTreeMap<String, std::collections::BTreeSet<String>> =
        Default::default();
    for e in json["entries"].as_array().map(|a| a.as_slice()).unwrap_or(&[]) {
        let (m, n) = (
            e["module"].as_str().unwrap_or_default(),
            e["name"].as_str().unwrap_or_default(),
        );
        if !m.is_empty() && !n.is_empty() {
            inventory.entry(m.to_string()).or_default().insert(n.to_string());
        }
    }

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
    println!();
    println!(
        "  Family S touches {} of {all_modules} stdlib modules. The other {} are \
         NOT covered by this family at all.",
        modules.len(),
        all_modules.saturating_sub(modules.len())
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
    0
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
        other => panic!("no battery for surface {other:?}"),
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
        ],
        "unicode" => vec![
            i(&["List.length"], "List.length (String.toList \"世界🎉\")", 3),
            s(
                &["List.reverse", "List.map"],
                "String.join \"\" (List.reverse (List.map String.fromChar (String.toList \"世界\")))",
                "界世",
            ),
            ls(&["List.filter"], "List.filter (\\x -> x /= \"b\") [ \"é\", \"b\" ]", "é"),
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
            s(&["Prelude.toString"], "toString 42", "42"),
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
            i(&["Prelude.modBy"], "modBy 3 7", 1),
            i(&["Prelude.modBy"], "modBy 3 -1", 2),
            i(&["Prelude.modBy"], "modBy 3 0", 0),
            s(&["Prelude.toString"], "toString -7", "-7"),
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
            "aesKey : String\naesKey =\n    Crypto.aesKeyFromPassword \"pw\" \"salt-1234567890\"\n\n\n\
             chachaKey : String\nchachaKey =\n    Crypto.chachaKeyFromPassword \"pw\" \"salt-1234567890\"\n"
        }
        "decimal" => "emptyDecimals : List Decimal\nemptyDecimals =\n    []\n",
        "money" => "emptyMoneys : List Money\nemptyMoneys =\n    []\n",
        "json" => {
            "emptyInts : List Int\nemptyInts =\n    []\n\n\n\
             emptyIntDecoders : List (Decoder Int)\nemptyIntDecoders =\n    []\n"
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
        // The AEAD round-trips go through `Result.andThen`.
        "crypto" => &[],
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
        imports.push_str(&format!("import {m}\n"));
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

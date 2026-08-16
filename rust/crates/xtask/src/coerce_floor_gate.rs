//! `xtask coerce-floor` — the runtime-coercion FLOOR-LOCK gate.
//!
//! The emitted Go leans on a fixed set of runtime *narrowing / erasure* helpers
//! — `rt.Coerce[T]`, the `rt.As*` scalar/list/tuple/dict casts, `rt.Field`
//! (reflect field access), `rt.SkyCall` (reflect HOF dispatch). Their origins,
//! and which of them are floor, are catalogued in
//! `docs/rust-rewrite/14-runtime-narrowing-taxonomy.md` ("doc 14" throughout
//! this module): FFI return, wire decode, TEA dispatch, type-variable
//! erasure. They are *sound* (each recovers to an `Err`/panic-classified path,
//! never UB) but each one is a place the compiler gave up static type info. The
//! fewer, the better.
//!
//! Nothing bounds how MANY the compiler emits. A codegen change can silently
//! WIDEN the floor — emit more coercions than needed — and no existing gate
//! fails (repro proves the output is *stable*, build-run proves it *builds*;
//! neither counts coercions). The typed-tuple / record-alias work changed these
//! counts with no guard. This gate locks them.
//!
//! # Why the census is CLASSIFIED and not a single number
//!
//! Until 2026-08-15 this gate ratcheted one raw token count per project. That
//! number stopped tracking the property it proxies, and it did so in the one
//! direction a ratchet cannot survive: it failed a change that *reduced* runtime
//! narrowing cost.
//!
//! `rt.Coerce[func(…)…]` applied to a func of a different shape can never satisfy
//! its own `v.(T)` fast path — Go function types are nominal in their parameters
//! — so every one of them falls through to `makeFuncAdapter` →
//! `adaptFuncValueWithCapture` (`runtime-go/rt/rt.go:5899-5907`), a
//! `reflect.MakeFunc` thunk that allocates a `[]reflect.Value` and re-boxes each
//! argument on **every invocation**. `rt.Coerce[Concrete]` is one assertion that
//! succeeds. The two are not the same unit, and the raw count added them up.
//!
//! Eta-expanding an N-ary callback at its slot's shape (doc 14 §5.1)
//! therefore trades ONE never-succeeding token for N
//! succeeding ones. Measured on the `Std.Ui` marker scan: 318 → 126 allocations
//! per scan, 1.36× end-to-end throughput — while the raw count ROSE on 9
//! projects. Nine "WIDENED" verdicts on the change that closed the category.
//!
//! So a token is counted **into its cost class**, and each class ratchets on the
//! terms that class actually obeys:
//!
//! | class | what it costs | closeable? | ratchet |
//! |---|---|---|---|
//! | `adapter` | a `reflect.MakeFunc` thunk, paid per INVOCATION of the adapted func — i.e. once per element of every traversal it reaches. Unbounded in the site count. | **yes** — doc 14 §5.1, both shapes are known at `coerce_if_needed` | **EXACT MATCH, and `--bless` refuses to raise it.** Monotone down, by construction. |
//! | `dispatch` | `rt.SkyCall` — reflect call dispatch, paid per call. | **no** — doc 14 §4.3 floor, the callee's shape exists only at runtime | fail-on-increase |
//! | `narrow` | at most one type assertion (or one container rebuild) per evaluation of the site. Bounded by the site's own evaluation count. | per doc 14 §3 origin | fail-on-increase |
//!
//! `adapter` is the only class held to exact match. That is deliberate: it is the
//! class that is *statically closeable*, so any drift — up OR down — is a fact
//! about the compiler that must be recorded rather than absorbed. A decrease is a
//! win and blessing it locks the win in; an increase is a regression and blessing
//! it is **refused**, so a new never-succeeding coerce cannot be normalised by
//! re-running `--bless`. Raising it requires hand-editing the golden, which shows
//! up as a reviewable diff with a reason next to it.
//!
//! # Mechanics
//!
//!   * For each example that emits valid Go, re-emit (reusing the exact
//!     `emit_example_source` path repro/build-run use — no recompilation
//!     reimplementation) and count the tracked `rt.*` narrowing tokens in the
//!     emitted `main.go` (the whole emitted artifact — `rt/` is copied wholesale
//!     from the shared runtime, identical for every example, so it is out of
//!     scope, exactly as repro's byte-compare scope is).
//!   * A golden entry with no emitting example (or vice-versa) is REPORTED, never
//!     silently ignored.
//!
//! Determinism: the emitted Go is byte-stable (repro's invariant), so the token
//! census is byte-stable too.
//!
//! ## What this gate does NOT catch
//!
//! Stated plainly, because a gate whose blind spots are undocumented gets trusted
//! for things it never measured:
//!
//!   * **Cost per token within a class.** `rt.AsListT[T]` rebuilds a slice
//!     element-by-element and `rt.Coerce[int]` is one assertion; both are one
//!     `narrow`. The classes separate cost ORDERS, not cost.
//!   * **How often a site runs.** A `narrow` on a hot loop outweighs fifty on
//!     start-up paths. Nothing here weights by execution frequency; that is what
//!     `docs/perf/runs/` measurements are for.
//!   * **An `adapter` whose source is `any`.** `rt.Coerce[func…]` with an
//!     `any`-typed source MAY hit `v.(T)` if the dynamic shape happens to match.
//!     It is counted as `adapter` anyway — the emission cannot prove it will, and
//!     over-counting the expensive class is the safe direction.
//!   * **Anything outside the emitted `main.go`**, including the runtime itself.
//!     `dispatch` is 0 on every row today for exactly this reason — `rt.SkyCall`
//!     is reached from inside the runtime, not emitted by the lowering. The
//!     column is an armed tripwire for a lowering that starts emitting it, not a
//!     record of one that was eliminated. Do not read `dispatch=0` as progress.
//!   * **Whether a narrowing is CORRECT.** This is a cost census. Soundness is
//!     `xtask repro` / `build-run` / the panic-class gates.
//!
//! Usage:
//!   xtask coerce-floor            # re-emit + re-count; gate each class
//!   xtask coerce-floor --bless    # regenerate the golden (REFUSES to raise `adapter`)
//!   xtask coerce-floor -v         # print the per-family breakdown for each example
//!   xtask coerce-floor --only=NAME[,NAME…]   # filter to named examples

use project::emit_example_source;
use std::collections::BTreeMap;
use std::path::{Path, PathBuf};

/// The tracked runtime narrowing / erasure token families, each mapped (in the
/// doc comment) to its doc 14 §4 floor category. A token is counted iff the identifier
/// immediately following `rt.` is EXACTLY one of these (word-boundary matched via
/// full-identifier extraction — `rt.Coerce` and `rt.CoerceString` are distinct,
/// and `rt.CoerceX` for an untracked `X` counts as neither).
///
/// Category legend (floor origin, doc 14 §4):
///   FFI     — Go FFI return value narrowed from `any`.
///   WIRE    — gob/JSON/form wire decode into a typed shape.
///   TEA     — TEA dispatch: `(model, cmd)` tuple + HOF reflect call.
///   ERASE   — type-variable erasure (polymorphic slot lowered to `any`).
///
/// Excluded from the runtime `As*`/`Coerce*` surface: `AssertConsoleInvariantOrExit`
/// (a console assertion/exit, not a value narrowing) — it never erases a type.
const TRACKED: &[&str] = &[
    // general type-var erasure + record-alias / TEA payload narrowing
    "Coerce", // ERASE
    // typed wire-decode coercions
    "CoerceString", // WIRE
    "CoerceInt",    // WIRE
    "CoerceBool",   // WIRE
    "CoerceFloat",  // WIRE
    // scalar narrowing from `any` (FFI return / heterogeneous slice)
    "AsInt",    // FFI
    "AsFloat",  // FFI
    "AsString", // FFI
    "AsBool",   // FFI
    "AsRune",   // FFI
    // lenient (display-only) scalar narrowing — still a runtime narrowing
    "AsIntOrZero",   // FFI
    "AsFloatOrZero", // FFI
    "AsBoolOrFalse", // FFI
    // list narrowing (FFI return / wire decode)
    "AsList",    // FFI
    "AsListT",   // FFI
    "AsListAny", // FFI
    // tuple dispatch (TEA `update` returns `(model, cmd)`)
    "AsTuple2",  // TEA
    "AsTuple2T", // TEA
    "AsTuple3",  // TEA
    "AsTuple3T", // TEA
    // dict / map narrowing (wire decode / FFI)
    "AsDict",   // WIRE
    "AsMapT",   // WIRE
    "AsMapAny", // WIRE
    // reflect field access (type-var erasure) + reflect HOF dispatch (TEA)
    "Field",   // ERASE
    "SkyCall", // TEA
];

/// The tracked set, for the ONE other gate that must agree with it.
///
/// `corpus::emit_shape`'s `no-narrowing` property asserts that a fully-typed
/// emitted function contains none of these. If the two lists drifted, one gate
/// would certify a token the other forbids and the pair would silently stop
/// describing the same surface — so `emit_shape`'s
/// `narrowing_set_matches_coerce_floor` test reads this accessor instead of
/// keeping a copy.
pub fn tracked_tokens() -> &'static [&'static str] {
    TRACKED
}

/// Location of the committed golden (repo-root relative). One line per example:
/// `<example-name>\t<total-count>`, sorted by name. Kept minimal + deterministic.
const GOLDEN_REL: &str = "rust/crates/xtask/coerce_floor.golden";

pub fn run(args: &[String], root: &Path) -> i32 {
    let bless = args.iter().any(|a| a == "--bless");
    let rekey = args.iter().any(|a| a == "--rekey");
    let verbose = args.iter().any(|a| a == "-v" || a == "--verbose");
    let only: Option<Vec<String>> = args
        .iter()
        .find_map(|a| a.strip_prefix("--only="))
        .map(|s| {
            s.split(',')
                .map(|x| x.trim().to_string())
                .filter(|x| !x.is_empty())
                .collect()
        });

    let names: Vec<String> = match &only {
        Some(n) => n.clone(),
        None => corpus(root),
    };

    // ---- emit + count each example (reuse the shared emit path) ----
    // `counts`: examples that emitted valid Go (eligible for the golden).
    // `no_emit`: examples that did not emit (FFI-blocked / unported surface) —
    // reported, not gated (they can't have a stable count).
    let mut counts: BTreeMap<String, Counts> = BTreeMap::new();
    let mut no_emit: Vec<(String, String)> = Vec::new();
    for name in &names {
        let dir = dir_for_key(root, name);
        if !dir.is_dir() {
            continue;
        }
        match emit_example_source(root, &dir) {
            Ok(source) => {
                counts.insert(name.clone(), count_tokens(&source));
            }
            Err(note) => {
                no_emit.push((name.clone(), first_line(&note)));
            }
        }
    }

    if rekey {
        // Conservation is asserted BEFORE the golden is overwritten. Blessing
        // first and checking after would leave a lost row already committed to
        // disk, which is precisely the failure the assertion exists to prevent.
        match assert_conservation(root, &counts) {
            Ok(note) => println!("coerce-floor --rekey: {note}"),
            Err(problems) => {
                eprintln!(
                    "coerce-floor --rekey: CONSERVATION FAILED\n  {problems}\n\n\
                     A re-key may add rows and may move them between keys. It may NOT \
                     shrink the measured surface. Nothing was written."
                );
                return 1;
            }
        }
        return bless_golden(root, &counts, &no_emit);
    }

    if bless {
        return bless_golden(root, &counts, &no_emit);
    }

    // ---- load golden + diff ----
    let golden = match load_golden(root) {
        Ok(g) => g,
        Err(e) => {
            eprintln!(
                "coerce-floor: cannot read golden at {GOLDEN_REL}: {e}\n\
                 run `xtask coerce-floor --bless` to create it."
            );
            return 1;
        }
    };

    diff_and_gate(&counts, &no_emit, &golden, &only, verbose)
}

/// The cost class a narrowing token falls into. See the module header table.
///
/// The classes separate cost ORDERS — "paid once per evaluation of this site"
/// versus "paid once per invocation of a function this site produced" — because
/// that is the distinction a single count destroyed. They do not rank cost
/// within a class.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
enum Class {
    /// The site produces a `reflect.MakeFunc` thunk. Statically closeable (doc 14 §5.1).
    Adapter,
    /// `rt.SkyCall` — reflect call dispatch. doc 14 §4.3 irreducible floor.
    Dispatch,
    /// A value narrowing: one assertion (or one container rebuild) per evaluation.
    Narrow,
}

/// The three per-class counts. This — not a single total — is what the golden
/// stores and what the gate compares.
#[derive(Default, Clone, Copy, PartialEq, Eq, Debug)]
pub struct Classed {
    adapter: usize,
    dispatch: usize,
    narrow: usize,
}

impl Classed {
    fn total(&self) -> usize {
        self.adapter + self.dispatch + self.narrow
    }
    fn bump(&mut self, c: Class) {
        match c {
            Class::Adapter => self.adapter += 1,
            Class::Dispatch => self.dispatch += 1,
            Class::Narrow => self.narrow += 1,
        }
    }
}

/// Per-example token census: the per-class counts plus the per-family breakdown
/// (for diagnostics). Only the per-class counts are stored in the golden.
#[derive(Default, Clone)]
struct Counts {
    by_class: Classed,
    per_family: BTreeMap<&'static str, usize>,
}

impl Counts {
    fn total(&self) -> usize {
        self.by_class.total()
    }
}

/// Which cost class a tracked token belongs to, decided from the emitted text.
///
/// `ty_arg` is the token's rendered generic argument list with the brackets
/// stripped (`rt.Coerce[func(any) bool]` → `func(any) bool`), or `None` for a
/// token that takes none (`rt.Field`, `rt.AsInt`).
///
/// The func test is a `func(` PREFIX on the rendered type, which is exactly what
/// `codegen::render_ty` emits for a `GoTy::Func` and cannot be produced by any
/// other `GoTy` arm. `rt.AsListT[func(…)…]` counts as `adapter` too: `AsListT`
/// narrows each element into the func slot through `narrowReflectValue`
/// (`rt.go:2338-2341`), which is the same per-element reflect cost wearing a
/// different name.
fn classify(ident: &str, ty_arg: Option<&str>) -> Class {
    if ident == "SkyCall" {
        return Class::Dispatch;
    }
    match ty_arg {
        Some(t) if t.starts_with("func(") => Class::Adapter,
        _ => Class::Narrow,
    }
}

/// The balanced `[…]` type-argument group starting at `at`, brackets stripped.
/// `None` when the token is not immediately followed by `[`, or the group is
/// unterminated. Depth-counted, so a nested generic
/// (`rt.Coerce[rt.SkyResult[X, Y]]`) yields the whole outer group.
fn type_arg_at(source: &str, at: usize) -> Option<&str> {
    let bytes = source.as_bytes();
    if at >= bytes.len() || bytes[at] != b'[' {
        return None;
    }
    let mut depth = 0usize;
    let mut k = at;
    while k < bytes.len() {
        match bytes[k] {
            b'[' => depth += 1,
            b']' => {
                depth -= 1;
                if depth == 0 {
                    return Some(&source[at + 1..k]);
                }
            }
            _ => {}
        }
        k += 1;
    }
    None
}

/// Count the tracked `rt.<Family>` tokens in an emitted Go source, by cost class.
/// Word-boundary correct: at each `rt.` occurrence we extract the MAXIMAL
/// trailing identifier (`[A-Za-z0-9_]+`) and match it EXACTLY against the tracked
/// set — so `rt.Coerce` and `rt.CoerceString` never alias, and `rt.CoerceZ`
/// (untracked) is counted as neither. A leading identifier char before `rt` (e.g.
/// `xrt.`) disqualifies the match so we never count a `rt` embedded in a longer
/// ident.
fn count_tokens(source: &str) -> Counts {
    let tracked: std::collections::HashSet<&'static str> = TRACKED.iter().copied().collect();
    let bytes = source.as_bytes();
    let mut counts = Counts::default();
    let mut i = 0;
    while let Some(rel) = source[i..].find("rt.") {
        let at = i + rel;
        // reject `rt` that is the tail of a longer identifier (foo_rt.X, art.X).
        let prev_ok = at == 0 || !is_ident_byte(bytes[at - 1]);
        let id_start = at + 3; // past "rt."
        let mut j = id_start;
        while j < bytes.len() && is_ident_byte(bytes[j]) {
            j += 1;
        }
        if prev_ok && j > id_start {
            let ident = &source[id_start..j];
            if let Some(fam) = tracked.get(ident) {
                counts.by_class.bump(classify(ident, type_arg_at(source, j)));
                *counts.per_family.entry(*fam).or_insert(0) += 1;
            }
        }
        // advance past this ident so we never re-scan it.
        i = j.max(at + 3);
    }
    counts
}

fn is_ident_byte(b: u8) -> bool {
    b.is_ascii_alphanumeric() || b == b'_'
}

// ---- golden I/O ----------------------------------------------------------

fn golden_path(root: &Path) -> PathBuf {
    root.join(GOLDEN_REL)
}

/// Parse the golden into `name -> Classed`. Blank lines + `#`-comments ignored.
///
/// Every data line MUST carry all three named columns. A v1 line (`name\t42`) is
/// a hard error naming the migration, never a 42 quietly landing in whichever
/// column happens to be second: a census that silently mis-reads its own golden
/// would arm the ratchet against numbers nobody wrote.
fn load_golden(root: &Path) -> std::io::Result<BTreeMap<String, Classed>> {
    let text = std::fs::read_to_string(golden_path(root))?;
    let mut map = BTreeMap::new();
    for (n, line) in text.lines().enumerate() {
        let line = line.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        let mut it = line.split('\t');
        let Some(name) = it.next() else { continue };
        let cols: Vec<&str> = it.map(|c| c.trim()).filter(|c| !c.is_empty()).collect();
        let mut got = Classed::default();
        let mut seen = [false; 3];
        for col in &cols {
            let Some((k, v)) = col.split_once('=') else {
                return Err(bad_line(n + 1, line, "column is not `<class>=<count>`"));
            };
            let Ok(v) = v.parse::<usize>() else {
                return Err(bad_line(n + 1, line, "count is not a non-negative integer"));
            };
            match k {
                "adapter" => (got.adapter, seen[0]) = (v, true),
                "dispatch" => (got.dispatch, seen[1]) = (v, true),
                "narrow" => (got.narrow, seen[2]) = (v, true),
                _ => return Err(bad_line(n + 1, line, "unknown class name")),
            }
        }
        if !seen.iter().all(|s| *s) {
            return Err(bad_line(
                n + 1,
                line,
                "needs all three columns: adapter=<n>\\tdispatch=<n>\\tnarrow=<n>",
            ));
        }
        map.insert(name.trim().to_string(), got);
    }
    Ok(map)
}

fn bad_line(n: usize, line: &str, why: &str) -> std::io::Error {
    std::io::Error::new(
        std::io::ErrorKind::InvalidData,
        format!(
            "{GOLDEN_REL}:{n}: {why}\n  line: {line:?}\n  \
             The golden is CLASSIFIED (adapter / dispatch / narrow) as of \
             2026-08-15; the single-count v1 format is no longer readable, \
             deliberately — a v1 count silently landing in one class would arm \
             the ratchet against a number nobody wrote. Regenerate with \
             `cargo run -p xtask -- coerce-floor --bless`."
        ),
    )
}

/// Write the current counts as the new golden (sorted, one line per example).
/// THE conservation assertion (v2 §9.2), run once, on a re-keying commit.
///
/// # Why a second, inverted ratchet exists
///
/// In normal operation a DECREASE in a count is good — the floor tightened —
/// and the gate reports it and lets you ratchet down. Across a **re-keying**
/// the same decrease means something else entirely: measured surface was
/// *lost*, not moved. A re-key that silently drops rows would retire the
/// soundness ratchet over exactly as much of its surface as it dropped, and the
/// normal ratchet would call that an improvement.
///
/// So on a re-key, and only on a re-key, both aggregates must not fall:
///
/// ```text
///     sum(new tokens) >= sum(old tokens)   and   count(new rows) >= count(old rows)
/// ```
///
/// The normal FAIL-ON-INCREASE ratchet resumes immediately afterwards. The two
/// must never be conflated, which is why this lives behind its own flag rather
/// than inside `--bless`.
///
/// # The aggregate form is NOT sufficient — measured, not reasoned
///
/// v2 §9.2 specifies exactly the two aggregate inequalities above. Implemented
/// literally, the first re-key on this branch **passed** them — rows 52 -> 55,
/// tokens 7,177 -> 9,333 — while silently dropping the rows for
/// `03-tea-external` and `11-fyne-stopwatch`. Three large Layer-2 rows arriving
/// paid for two small rows leaving, and both aggregates went up.
///
/// A ratchet that can lose a locked floor as long as some other row grew is not
/// a ratchet. So a third clause is asserted here, and it is the one with teeth:
/// **no key that had a row may end up without one.** A key may CHANGE (that is
/// what re-keying is) but the change must be declared, not inferred from a
/// disappearance.
fn assert_conservation(root: &Path, counts: &BTreeMap<String, Counts>) -> Result<String, String> {
    let old = load_golden(root).map_err(|e| {
        format!(
            "--rekey needs an existing golden to conserve against, and {GOLDEN_REL} \
             could not be read: {e}"
        )
    })?;
    let old_rows = old.len();
    let old_tokens: usize = old.values().map(|c| c.total()).sum();

    // `bless_golden` carries a still-present-but-non-emitting project forward at
    // its last measured floor, so conservation must be judged against the rows
    // that will actually be WRITTEN, not against this run's measurements alone.
    let written = rows_to_write(root, counts, &old);
    let new_rows = written.len();
    let new_tokens: usize = written.values().map(|c| c.total()).sum();

    let mut problems = Vec::new();

    // Clause 3 first: it is the one that actually bites.
    let vanished: Vec<&str> = old
        .keys()
        .filter(|k| !written.contains_key(*k))
        .map(|s| s.as_str())
        .collect();
    if !vanished.is_empty() {
        problems.push(format!(
            "{} row(s) VANISHED with no successor: {}\n  \
             each of these had a locked floor and now has none. If the project was \
             deliberately retired, retire its row in its own commit with the reason; \
             a re-key must not be how a floor disappears.",
            vanished.len(),
            vanished.join(", ")
        ));
    }
    if new_tokens < old_tokens {
        problems.push(format!(
            "token total FELL {old_tokens} -> {new_tokens} (-{}): across a re-key that means \
             measured surface was LOST, not moved",
            old_tokens - new_tokens
        ));
    }
    if new_rows < old_rows {
        problems.push(format!(
            "row count FELL {old_rows} -> {new_rows}"
        ));
    }

    if problems.is_empty() {
        Ok(format!(
            "CONSERVED: every one of the {old_rows} existing row(s) still has a floor; \
             rows {old_rows} -> {new_rows} (+{}), tokens {old_tokens} -> {new_tokens} (+{})",
            new_rows - old_rows,
            new_tokens - old_tokens
        ))
    } else {
        Err(problems.join("\n  "))
    }
}

/// The rows a bless would WRITE: this run's measurements, plus a still-present
/// project's last measured floor carried forward (see `bless_golden`). Shared
/// with `assert_conservation` so the two can never judge different sets.
fn rows_to_write(
    root: &Path,
    counts: &BTreeMap<String, Counts>,
    old: &BTreeMap<String, Classed>,
) -> BTreeMap<String, Classed> {
    let mut rows: BTreeMap<String, Classed> =
        counts.iter().map(|(k, c)| (k.clone(), c.by_class)).collect();
    for (name, v) in old {
        if !rows.contains_key(name) && dir_for_key(root, name).is_dir() {
            rows.insert(name.clone(), *v);
        }
    }
    rows
}

/// THE monotone lock on the `adapter` class.
///
/// `adapter` counts emissions that produce a `reflect.MakeFunc` thunk. Per doc 14
/// §5.1 that category is *statically closeable* — both
/// the value's Go shape and the slot's Go shape are known at `coerce_if_needed`,
/// so an adapter that appears is an adapter the lowering failed to eta-expand.
///
/// A ratchet you can re-bless upwards is not a ratchet, and re-blessing is what
/// anyone does first when a gate goes red. So blessing REFUSES to raise this
/// column, and nothing is written when it would. There is deliberately no flag,
/// no allowlist and no per-row exception: the whole defect this classification
/// exists to fix was a raw count with an accounted exception absorbing a real
/// signal. If a genuinely inexpressible shape ever needs to be admitted, hand-
/// edit the golden — that lands as a reviewable diff someone has to justify,
/// which is the point.
fn assert_adapter_monotone(
    old: &BTreeMap<String, Classed>,
    new: &BTreeMap<String, Classed>,
) -> Result<(), String> {
    let risen: Vec<String> = new
        .iter()
        .filter_map(|(k, n)| {
            let o = old.get(k)?;
            (n.adapter > o.adapter).then(|| {
                format!("{k}: adapter {} -> {} (+{})", o.adapter, n.adapter, n.adapter - o.adapter)
            })
        })
        .collect();
    if risen.is_empty() {
        return Ok(());
    }
    Err(format!(
        "{} row(s) would RAISE the `adapter` count, and blessing cannot do that:\n  {}\n\n\
         Each of these is a `reflect.MakeFunc` thunk the lowering used to elide and \
         now emits — a cost paid per INVOCATION of the adapted function, i.e. once \
         per element of every traversal it reaches. Per doc 14 §5.1 this category \
         is statically closeable: both shapes are known at `coerce_if_needed` \
         (rust/crates/lower/src/lower.rs, `func_shape_eta`), so an adapter appearing \
         means the eta-expansion did not fire. Find out why — arity mismatch and a \
         non-symbol source are its documented `None` returns — and fix it there.\n\
         Nothing was written.",
        risen.len(),
        risen.join("\n  ")
    ))
}

fn bless_golden(
    root: &Path,
    counts: &BTreeMap<String, Counts>,
    no_emit: &[(String, String)],
) -> i32 {
    let carried = load_golden(root).unwrap_or_default();
    let rows = rows_to_write(root, counts, &carried);
    if let Err(why) = assert_adapter_monotone(&carried, &rows) {
        eprintln!("\ncoerce-floor --bless: REFUSED — {why}");
        return 1;
    }

    let mut out = String::new();
    out.push_str(
        "# coerce-floor golden — emitted-Go runtime-narrowing token census, BY COST CLASS.\n\
         #\n\
         # adapter  — the site produces a `reflect.MakeFunc` thunk (`rt.Coerce[func(…)…]`).\n\
         #            Cost is paid per INVOCATION of the adapted func — once per element of\n\
         #            every traversal it reaches. Statically closeable (doc 14 §5.1).\n\
         #            EXACT MATCH: any drift fails. `--bless` REFUSES to raise it.\n\
         # dispatch — `rt.SkyCall`, reflect call dispatch. doc 14 §4.3 irreducible floor.\n\
         #            FAIL-ON-INCREASE.\n\
         # narrow   — a value narrowing: at most one assertion (or one container rebuild)\n\
         #            per evaluation of the site. FAIL-ON-INCREASE.\n\
         #\n\
         # A DECREASE in dispatch/narrow is fine (floor tightened) — re-bless to ratchet down.\n\
         # Regenerate with: cargo run -p xtask -- coerce-floor --bless\n\
         # Re-key (adds/moves rows) with: cargo run -p xtask -- coerce-floor --rekey,\n\
         #   which additionally asserts sum(tokens) and count(rows) did not FALL.\n\
         # Format: <key>\\tadapter=<n>\\tdispatch=<n>\\tnarrow=<n>\n\
         #   <key> without `/` is an examples/ directory; with `/` it is a\n\
         #   repo-root-relative Layer-2 project path (apps/manifest.toml).\n\
         #\n\
         # `dispatch` is 0 for every row: `rt.SkyCall` is reached from the runtime, not\n\
         # emitted into main.go by this compiler. The column is an armed tripwire for a\n\
         # lowering that starts emitting it, NOT a record of one that was eliminated.\n\
         #\n\
         # ── Recorded transition, 2026-08-15 — eta-expansion of func-slot values ──\n\
         # `perf(lower): eta-expand a func value into a func slot, not rt.Coerce`\n\
         # replaced a func-shape `rt.Coerce` with a closure at the slot's shape whose\n\
         # parameters narrow inward. A 1-ary callback trades one coarse token for one\n\
         # precise one; an N-ary callback trades ONE for N — so `adapter` falls and\n\
         # `narrow` RISES on the projects whose callbacks are N-ary.\n\
         #\n\
         # That rise is the trade, not a regression. The token removed could never\n\
         # satisfy its own `v.(T)` fast path and allocated a []reflect.Value on every\n\
         # invocation; the tokens added are assertions that succeed. Measured on the\n\
         # Std.Ui marker scan: 318 -> 126 allocations per scan, 1.36x end-to-end\n\
         # throughput (docs/perf/runs/hof-dispatch-20260815/).\n\
         #\n\
         # Measured across the 56 projects that emit both before (b131e751) and after,\n\
         # by running THIS census against both compilers:\n\
         #     adapter  269 -> 24   (-245, -91%); 24 projects driven to 0; 0 rose\n\
         #     narrow  8055 -> 8234 (+179)\n\
         #     total   8324 -> 8258 (-66)\n\
         # The 24 residual adapters are `func_shape_eta`'s documented `None` returns —\n\
         # arity mismatch, non-symbol source, and a param/result whose narrowing would\n\
         # REBUILD a slice or map (trading an O(1) reflect box for an O(n) copy). They\n\
         # are locked here exactly, so closing any of them shows up as a gate failure\n\
         # asking to record the win.\n",
    );
    // A project that did not emit HERE keeps the floor it was last measured at.
    //
    // This is not politeness, it is a correctness requirement. `no_emit` means
    // "cannot be measured in THIS environment" — 03-tea-external, 08-notes-app
    // and 11-fyne-stopwatch have no generated FFI surface until their Go deps
    // are fetched, and 13-skyshop needs a network `sky install`. Dropping their
    // rows on a bless run would silently retire their locked floor on whichever
    // machine happened to run the bless, and the loss would read as "the corpus
    // shrank" rather than as an error. Carrying the old value forward keeps the
    // ratchet armed; if the project is genuinely GONE from disk the row is
    // dropped, which is a real deletion and is what the conservation assertion
    // is there to catch.
    let carried_forward: Vec<String> = rows
        .keys()
        .filter(|k| !counts.contains_key(*k))
        .cloned()
        .collect();

    // BTreeMap already sorted by name → deterministic output.
    let mut tot = Classed::default();
    for (name, c) in &rows {
        out.push_str(&format!(
            "{name}\tadapter={}\tdispatch={}\tnarrow={}\n",
            c.adapter, c.dispatch, c.narrow
        ));
        tot.adapter += c.adapter;
        tot.dispatch += c.dispatch;
        tot.narrow += c.narrow;
    }
    match std::fs::write(golden_path(root), &out) {
        Ok(()) => {
            println!(
                "coerce-floor: blessed {} project(s) → {GOLDEN_REL}\n  \
                 adapter={}  dispatch={}  narrow={}  (total {})",
                rows.len(),
                tot.adapter,
                tot.dispatch,
                tot.narrow,
                tot.total()
            );
            if !no_emit.is_empty() {
                println!(
                    "  ({} project(s) did not emit here: {})",
                    no_emit.len(),
                    no_emit
                        .iter()
                        .map(|(n, _)| n.as_str())
                        .collect::<Vec<_>>()
                        .join(", ")
                );
            }
            if !carried_forward.is_empty() {
                println!(
                    "  ({} row(s) CARRIED FORWARD at their last measured floor, \
                     because the project exists but did not emit here: {})",
                    carried_forward.len(),
                    carried_forward.join(", ")
                );
            }
            0
        }
        Err(e) => {
            eprintln!("coerce-floor: failed to write golden: {e}");
            1
        }
    }
}

// ---- diff + gate ---------------------------------------------------------

/// One project's verdict, per class. `adapter` is exact-match; the other two are
/// fail-on-increase.
struct Row {
    name: String,
    now: Classed,
    was: Classed,
}

fn cell(now: usize, was: usize) -> String {
    match now.cmp(&was) {
        std::cmp::Ordering::Greater => format!("{now}(+{})", now - was),
        std::cmp::Ordering::Less => format!("{now}(-{})", was - now),
        std::cmp::Ordering::Equal => format!("{now}"),
    }
}

fn diff_and_gate(
    counts: &BTreeMap<String, Counts>,
    no_emit: &[(String, String)],
    golden: &BTreeMap<String, Classed>,
    only: &Option<Vec<String>>,
    verbose: bool,
) -> i32 {
    println!(
        "coerce-floor gate — runtime-narrowing token census BY COST CLASS\n\
         \x20 adapter  reflect.MakeFunc thunk, paid per invocation — EXACT MATCH\n\
         \x20 dispatch rt.SkyCall, doc 14 §4.3 floor              — fail-on-increase\n\
         \x20 narrow   one assertion per evaluation                 — fail-on-increase\n"
    );

    let w = counts
        .keys()
        .chain(golden.keys())
        .map(|s| s.len())
        .max()
        .unwrap_or(8)
        .max(8);
    println!(
        "{:<w$}  {:>10}  {:>10}  {:>12}  STATUS",
        "PROJECT",
        "ADAPTER",
        "DISPATCH",
        "NARROW",
        w = w
    );
    println!("{}", "-".repeat(w + 48));

    // `adapter` drift in EITHER direction is a fact to record, so both are
    // collected; up is a regression, down is an unrecorded win.
    let mut adapter_rose: Vec<Row> = Vec::new();
    let mut adapter_fell: Vec<Row> = Vec::new();
    let mut widened: Vec<Row> = Vec::new();
    let mut tightened: Vec<Row> = Vec::new();
    let mut missing_from_golden: Vec<String> = Vec::new();
    let mut total_now = Classed::default();

    for (name, c) in counts {
        let n = c.by_class;
        total_now.adapter += n.adapter;
        total_now.dispatch += n.dispatch;
        total_now.narrow += n.narrow;
        match golden.get(name) {
            Some(&g) => {
                let row = || Row {
                    name: name.clone(),
                    now: n,
                    was: g,
                };
                let mut tags: Vec<&str> = Vec::new();
                match n.adapter.cmp(&g.adapter) {
                    std::cmp::Ordering::Greater => {
                        adapter_rose.push(row());
                        tags.push("ADAPTER-REGRESSED");
                    }
                    std::cmp::Ordering::Less => {
                        adapter_fell.push(row());
                        tags.push("ADAPTER-TIGHTENED");
                    }
                    std::cmp::Ordering::Equal => {}
                }
                if n.dispatch > g.dispatch || n.narrow > g.narrow {
                    widened.push(row());
                    tags.push("WIDENED");
                } else if n.dispatch < g.dispatch || n.narrow < g.narrow {
                    tightened.push(row());
                    tags.push("tightened");
                }
                if tags.is_empty() {
                    tags.push("ok");
                }
                println!(
                    "{:<w$}  {:>10}  {:>10}  {:>12}  {}",
                    name,
                    cell(n.adapter, g.adapter),
                    cell(n.dispatch, g.dispatch),
                    cell(n.narrow, g.narrow),
                    tags.join(" + "),
                    w = w
                );
            }
            None => {
                missing_from_golden.push(name.clone());
                println!(
                    "{:<w$}  {:>10}  {:>10}  {:>12}  NOT-IN-GOLDEN",
                    name,
                    n.adapter,
                    n.dispatch,
                    n.narrow,
                    w = w
                );
            }
        }
        if verbose && !c.per_family.is_empty() {
            let fams: Vec<String> = c
                .per_family
                .iter()
                .map(|(k, v)| format!("{k}={v}"))
                .collect();
            println!("        {}", fams.join("  "));
        }
    }

    // golden entries with no emitting example this run. A golden entry whose
    // example is in `no_emit` is NOT a true orphan — it EXISTS but couldn't emit
    // in THIS environment (an FFI example whose surface is absent here: 11-fyne /
    // 13-skyshop commit no surface — gitignored — so they emit locally but not on
    // a fresh CI checkout). Those are environment-dependent non-emissions, already
    // reported via `no_emit`; excluding them keeps the golden portable local↔CI.
    // A genuinely REMOVED/renamed example is absent from the corpus entirely
    // (neither `counts` nor `no_emit`), so it still surfaces as a true orphan.
    let no_emit_set: std::collections::HashSet<&str> =
        no_emit.iter().map(|(n, _)| n.as_str()).collect();
    let golden_orphans: Vec<&String> = golden
        .keys()
        .filter(|k| !counts.contains_key(*k))
        .filter(|k| !no_emit_set.contains(k.as_str()))
        .collect();

    println!("{}", "-".repeat(w + 48));
    let gt = golden.values().fold(Classed::default(), |mut a, c| {
        a.adapter += c.adapter;
        a.dispatch += c.dispatch;
        a.narrow += c.narrow;
        a
    });
    println!(
        "TOTALS  |  {} project(s) counted\n  \
         now    adapter={}  dispatch={}  narrow={}  (total {})\n  \
         golden adapter={}  dispatch={}  narrow={}  (total {})",
        counts.len(),
        total_now.adapter,
        total_now.dispatch,
        total_now.narrow,
        total_now.total(),
        gt.adapter,
        gt.dispatch,
        gt.narrow,
        gt.total()
    );

    // ---- report the non-fatal observations ----
    if !tightened.is_empty() {
        println!(
            "\ncoerce-floor: {} project(s) TIGHTENED dispatch/narrow \
             (re-bless to ratchet down):",
            tightened.len()
        );
        for r in &tightened {
            println!(
                "  {}: dispatch {} -> {}, narrow {} -> {}",
                r.name, r.was.dispatch, r.now.dispatch, r.was.narrow, r.now.narrow
            );
        }
        println!("  hint: `cargo run -p xtask -- coerce-floor --bless`");
    }
    if !no_emit.is_empty() {
        println!(
            "\ncoerce-floor: {} example(s) did not emit (not gated):",
            no_emit.len()
        );
        for (n, why) in no_emit {
            println!("  {n}: {why}");
        }
    }

    // ---- gate: fail on any increase, or on a golden/corpus mismatch ----
    // A `--only` run intentionally sees a subset, so golden-orphans there are
    // expected (the other examples were filtered out); don't treat them as a
    // failure in that mode. `missing_from_golden` (a NEW example not yet blessed)
    // is always a failure — a new example must be blessed so its floor is locked.
    let subset = only.is_some();

    let mut fail = false;

    // ---- the `adapter` class: exact match, both directions ----
    if !adapter_rose.is_empty() {
        fail = true;
        eprintln!(
            "\nCOERCE-FLOOR GATE: FAIL — {} project(s) emit MORE reflect.MakeFunc adapters:",
            adapter_rose.len()
        );
        for r in &adapter_rose {
            eprintln!(
                "  {}: adapter {} -> {}  (+{})",
                r.name,
                r.was.adapter,
                r.now.adapter,
                r.now.adapter - r.was.adapter
            );
        }
        eprintln!(
            "  Each of these is a `rt.Coerce[func(…)…]`. Go function types are nominal in\n\
             \x20 their parameters, so it cannot satisfy its own `v.(T)` fast path: it falls to\n\
             \x20 `makeFuncAdapter` -> `adaptFuncValueWithCapture` (runtime-go/rt/rt.go:5899),\n\
             \x20 a reflect.MakeFunc thunk allocating a []reflect.Value on EVERY invocation —\n\
             \x20 once per element of every traversal it reaches, not once per call site.\n\
             \x20\n\
             \x20 Per doc 14 §5.1 this category is statically\n\
             \x20 CLOSEABLE: both the value's Go shape and the slot's Go shape are known at\n\
             \x20 `coerce_if_needed`. So an adapter appearing means `func_shape_eta`\n\
             \x20 (rust/crates/lower/src/lower.rs) declined to eta-expand. Its documented\n\
             \x20 `None` returns are: source is not itself a Go func; arities differ; source\n\
             \x20 is not a literal/ident/selector; a param or result narrowing would REBUILD\n\
             \x20 a slice or map. Find which, and fix it there.\n\
             \x20\n\
             \x20 The golden does not move for this. `--bless` REFUSES to raise it."
        );
    }
    if !adapter_fell.is_empty() {
        fail = true;
        eprintln!(
            "\nCOERCE-FLOOR GATE: FAIL — {} project(s) emit FEWER reflect.MakeFunc adapters \
             than the golden records:",
            adapter_fell.len()
        );
        for r in &adapter_fell {
            eprintln!(
                "  {}: adapter {} -> {}  (-{})",
                r.name,
                r.was.adapter,
                r.now.adapter,
                r.was.adapter - r.now.adapter
            );
        }
        eprintln!(
            "  This is a WIN, and it fails the gate because an unrecorded win leaves slack a\n\
             \x20 later regression can hide in: if the golden still says 13 and the compiler\n\
             \x20 now emits 0, thirteen adapters can come back without a single gate going red.\n\
             \x20 `adapter` is exact-match for exactly this reason.\n\
             \x20\n\
             \x20 Lock it in: `cargo run -p xtask -- coerce-floor --bless`"
        );
    }

    // ---- `dispatch` / `narrow`: fail-on-increase ----
    if !widened.is_empty() {
        fail = true;
        eprintln!(
            "\nCOERCE-FLOOR GATE: FAIL — {} project(s) WIDENED dispatch/narrow:",
            widened.len()
        );
        for r in &widened {
            eprintln!(
                "  {}: dispatch {} -> {}, narrow {} -> {}  [adapter {} -> {}]",
                r.name,
                r.was.dispatch,
                r.now.dispatch,
                r.was.narrow,
                r.now.narrow,
                r.was.adapter,
                r.now.adapter
            );
        }
        // Cause (3) is machine-detectable now that the classes are separate: a
        // narrow rise paired with an adapter fall IS the trade. Say so rather
        // than leaving the reader to re-derive it — that re-derivation is what
        // cost this branch a full investigation.
        let traded: Vec<&Row> = widened
            .iter()
            .filter(|r| r.now.narrow > r.was.narrow && r.now.adapter < r.was.adapter)
            .collect();
        if !traded.is_empty() {
            eprintln!(
                "\n  NOTE — {} of these look like cause (3) below: `narrow` rose while\n\
                 \x20 `adapter` FELL in the same project. That is the eta trade, not a\n\
                 \x20 regression: {}",
                traded.len(),
                traded
                    .iter()
                    .map(|r| r.name.as_str())
                    .collect::<Vec<_>>()
                    .join(", ")
            );
        }
        eprintln!(
            "\n  the emitted Go carries MORE runtime narrowing than the locked floor.\n\
             THREE different causes produce this, and they need DIFFERENT fixes — find out\n\
             which before touching the golden:\n\
             \x20 (1) a CODEGEN change gave up type info it used to keep. This is the one the\n\
             \x20     gate exists for. The fix is in the compiler; the golden does not move.\n\
             \x20 (2) a STDLIB or app SOURCE change added code, and the new code pays the same\n\
             \x20     per-call-site kernel-ABI narrowing every existing line pays. Nothing got\n\
             \x20     less typed — there is simply more of it. The golden moves, with a written\n\
             \x20     justification naming what the tokens bought.\n\
             \x20 (3) a COARSE token was traded for N PRECISE ones. A `rt.Coerce[func(a,b) r]`\n\
             \x20     is ONE token that never succeeds and costs a reflect.MakeFunc thunk per\n\
             \x20     invocation; eta-expanding it at the slot's shape removes that token and\n\
             \x20     adds one succeeding assertion PER PARAMETER. A 1-ary callback trades 1\n\
             \x20     for 1; an N-ary callback trades 1 for N, so `narrow` RISES while the cost\n\
             \x20     it proxies FALLS. Signature: `adapter` fell in the same project (the gate\n\
             \x20     flags this above). Measured precedent (2026-08-15, the 56 projects that\n\
             \x20     emit both before and after): adapter 269 -> 24, narrow 8055 -> 8234,\n\
             \x20     total 8324 -> 8258; 318 -> 126 allocations per Std.Ui marker scan and\n\
             \x20     1.36x throughput (docs/perf/runs/hof-dispatch-20260815/). The golden\n\
             \x20     moves, and the transition is recorded in its header.\n\
             \x20\n\
             \x20 To tell (1) from (2), bisect the SOURCE, not the commit: revert the suspect\n\
             \x20 .sky file to its last-green content and re-run with `--only=<name>`. If the\n\
             \x20 count returns to the golden, the compiler is innocent — it is (2). Confirm by\n\
             \x20 checking WHERE the tokens landed (`-v`, and diff the emitted Go per function):\n\
             \x20 under (2) every delta sits in a function whose source changed; under (1) they\n\
             \x20 are scattered through functions nobody touched. (3) is distinguished by the\n\
             \x20 adapter column, not by bisection.\n\
             \x20\n\
             if the widening is (2) or (3) — INTENTIONAL + justified — re-bless: \
             `cargo run -p xtask -- coerce-floor --bless`"
        );
    }
    if !missing_from_golden.is_empty() {
        fail = true;
        eprintln!(
            "\nCOERCE-FLOOR GATE: FAIL — {} emitting example(s) absent from the golden \
             (new example — bless to lock its floor): {}",
            missing_from_golden.len(),
            missing_from_golden.join(", ")
        );
    }
    if !golden_orphans.is_empty() && !subset {
        fail = true;
        eprintln!(
            "\nCOERCE-FLOOR GATE: FAIL — {} golden entr(y/ies) have no emitting example \
             (removed/renamed example — re-bless): {}",
            golden_orphans.len(),
            golden_orphans
                .iter()
                .map(|s| s.as_str())
                .collect::<Vec<_>>()
                .join(", ")
        );
    }

    if fail {
        1
    } else if counts.is_empty() {
        eprintln!("\nCOERCE-FLOOR GATE: INCONCLUSIVE — no example emitted a count.");
        1
    } else {
        println!(
            "\nCOERCE-FLOOR GATE: PASS  (adapter exact at {}, no project widened \
             dispatch/narrow)",
            total_now.adapter
        );
        0
    }
}

// ---- corpus --------------------------------------------------------------

/// Resolve a golden KEY to the project directory it names.
///
/// Two key shapes, deliberately distinguishable by eye and by code:
///
/// * a bare name (`26-ui-showcase`) is an `examples/` directory — the original
///   keying, left untouched so the existing 52 rows keep their identity and
///   their history across this change;
/// * a key containing `/` (`apps/ledger`) is a repo-root-relative project path
///   — how Layer-2 members joined the ratchet.
///
/// Mixing the two is a deliberate choice over a mass re-key. Re-keying every
/// row would have made every historical floor unattributable to its old value
/// in one commit, for no gain: the point of adding Layer 2 is that the members
/// contributed **zero** to the soundness ratchet while being the corpus that
/// now carries regression duty for the product surfaces.
fn dir_for_key(root: &Path, key: &str) -> PathBuf {
    if key.contains('/') {
        root.join(key)
    } else {
        root.join("examples").join(key)
    }
}

/// The ratchet's corpus: every emitting `examples/` project PLUS every Layer-2
/// member with Sky source.
///
/// Layer-2 members are read from `apps/manifest.toml`, the declared membership
/// authority (`docs/ci-layer2-members.md` Decision 2) — **not** by `read_dir` on
/// `apps/`, because discovery-by-listing is how `39-hub-demo` became invisible
/// to six gates at once.
fn corpus(root: &Path) -> Vec<String> {
    let mut ds: Vec<String> = std::fs::read_dir(root.join("examples"))
        .map(|rd| {
            rd.filter_map(|e| e.ok())
                .filter(|e| e.path().is_dir())
                .filter_map(|e| e.file_name().to_str().map(String::from))
                .filter(|n| root.join("examples").join(n).join("src").is_dir())
                .filter(|n| n != "simple" && n != "test_pkg")
                .collect()
        })
        .unwrap_or_default();

    for key in layer2_keys(root) {
        // Member D's declared path IS `examples/13-skyshop`, and member E is a
        // scenario over member A's directory. Both would otherwise double-count.
        let already = ds.iter().any(|n| dir_for_key(root, n) == dir_for_key(root, &key));
        if !already {
            ds.push(key);
        }
    }

    ds.sort();
    ds.dedup();
    ds
}

/// Layer-2 member project paths, from the declared manifest.
///
/// Deliberately a small hand parser rather than a TOML dependency: this gate
/// decides whether CI is green and its dependency surface is kept minimal. It
/// reads `path = "..."` from each `[[member]]` block and keeps the ones that
/// are actually Sky projects (`src/` present) — which drops member G
/// (`rust/crates/sky/tests`, flow tests, not a project) without naming it.
fn layer2_keys(root: &Path) -> Vec<String> {
    let Ok(src) = std::fs::read_to_string(root.join("apps/manifest.toml")) else {
        return Vec::new();
    };
    let mut out = Vec::new();
    let mut in_member = false;
    for line in src.lines() {
        let t = line.trim();
        if t.starts_with("[[member]]") {
            in_member = true;
            continue;
        }
        if t.starts_with('[') {
            in_member = false;
            continue;
        }
        if !in_member {
            continue;
        }
        let Some(rest) = t.strip_prefix("path") else { continue };
        let Some(v) = rest.split('=').nth(1) else { continue };
        let p = v.trim().trim_matches('"').to_string();
        if p.is_empty() {
            continue;
        }
        // `sky-bundled` holds two projects side by side rather than one `src/`.
        if root.join(&p).join("src").is_dir() {
            out.push(p);
        } else {
            for sub in ["console", "doc"] {
                if root.join(&p).join(sub).join("src").is_dir() {
                    out.push(format!("{p}/{sub}"));
                }
            }
        }
    }
    out.sort();
    out.dedup();
    out
}

fn first_line(s: &str) -> String {
    s.lines().next().unwrap_or("").chars().take(70).collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    // Word-boundary correctness: `rt.Coerce` and `rt.CoerceString` are distinct
    // families; an untracked `rt.CoerceZ` counts as neither; a `rt` embedded in a
    // longer identifier (`art.Coerce`) is not counted.
    #[test]
    fn counts_are_word_boundary_exact() {
        let src = "\
x := rt.Coerce[int](a)
y := rt.CoerceString(b)
z := rt.CoerceZ(c)
w := art.Coerce(d)
v := rt.AsInt(e) + rt.AsIntOrZero(f)
u := rt.AsList[int](g); t := rt.AsListT[int](h)
";
        let c = count_tokens(src);
        assert_eq!(
            c.per_family.get("Coerce").copied(),
            Some(1),
            "rt.Coerce only"
        );
        assert_eq!(c.per_family.get("CoerceString").copied(), Some(1));
        assert_eq!(
            c.per_family.get("AsInt").copied(),
            Some(1),
            "AsInt != AsIntOrZero"
        );
        assert_eq!(c.per_family.get("AsIntOrZero").copied(), Some(1));
        assert_eq!(
            c.per_family.get("AsList").copied(),
            Some(1),
            "AsList != AsListT"
        );
        assert_eq!(c.per_family.get("AsListT").copied(), Some(1));
        // rt.CoerceZ (untracked) and art.Coerce (embedded rt) contribute nothing.
        assert_eq!(c.total(), 6);
    }

    // ── The defect this gate has, stated as a test. ────────────────────────
    //
    // A raw token count cannot tell a cheap narrowing from an expensive one, and
    // the two are not interchangeable. `rt.Coerce[func(…)…]` applied to a func of
    // a different shape can NEVER satisfy its own `v.(T)` fast path — Go function
    // types are nominal in their parameters — so it falls to `makeFuncAdapter` →
    // `adaptFuncValueWithCapture`, a `reflect.MakeFunc` thunk that allocates on
    // every INVOCATION (`runtime-go/rt/rt.go:5899-5907`, doc 14 §5.1).
    // A `rt.Coerce[Concrete]` is a single `v.(T)`
    // assertion that succeeds.
    //
    // So eta-expanding an N-ary callback into its slot's shape trades ONE
    // never-succeeding token for N succeeding ones. Runtime narrowing cost falls;
    // the raw count RISES. The census, asked which emission is cheaper, gets it
    // backwards — and the ratchet built on it fails a change that improved the
    // very property it exists to protect.
    #[test]
    fn the_census_separates_reflect_adapters_from_value_narrowings() {
        // BEFORE: a 2-ary Sky callback reaching an erased `func(any, any) any`
        // slot, emitted as one coarse func-shape coerce.
        let with_adapter = "rt.Coerce[func(any, any) any](step)";
        // AFTER: eta-expanded at the slot's shape. No func-shape coerce survives;
        // each parameter is narrowed precisely, once per invocation, by assertion.
        let without_adapter = "func(_e0 any, _e1 any) any { \
             return step(rt.Coerce[Acc](_e0), rt.Coerce[Item](_e1)) }";

        let (a, b) = (count_tokens(with_adapter), count_tokens(without_adapter));

        // The RAW total still rises, and always will. That is a fact about the
        // emission, not a defect — it is why the total alone cannot be the ratchet.
        assert!(
            b.total() > a.total(),
            "the eta trade adds tokens: {} -> {}",
            a.total(),
            b.total()
        );

        // What the classified census says, which is the thing that is true: the
        // per-invocation reflect cost went to zero, and what replaced it is N
        // assertions that succeed.
        assert_eq!(a.by_class.adapter, 1, "the coarse form is one adapter");
        assert_eq!(a.by_class.narrow, 0);
        assert_eq!(b.by_class.adapter, 0, "eta-expansion leaves no adapter");
        assert_eq!(b.by_class.narrow, 2, "one precise narrowing per parameter");
    }

    // `rt.SkyCall` is reflect dispatch (doc 14 §4.3 floor) and must not be filed as a
    // value narrowing — it is paid per call, not per evaluation of a site.
    #[test]
    fn skycall_is_dispatch_not_narrow() {
        let c = count_tokens("rt.SkyCall(f, a) + rt.Coerce[Msg](x) + rt.AsInt(y)");
        assert_eq!(c.by_class.dispatch, 1);
        assert_eq!(c.by_class.narrow, 2);
        assert_eq!(c.by_class.adapter, 0);
    }

    // The func test reads the RENDERED type argument, so it must survive the
    // shapes `codegen::render_ty` actually produces: nested generics inside the
    // brackets, and non-func targets that merely start with a bracketed type.
    #[test]
    fn func_targeting_is_read_from_the_rendered_type_argument() {
        let c = count_tokens(
            "rt.Coerce[func(any) rt.SkyResult[Error, int]](f)\n\
             rt.Coerce[rt.SkyResult[Error, func(any) int]](g)\n\
             rt.Coerce[map[string]int](m)\n\
             rt.AsListT[func(any) bool](xs)\n\
             rt.Coerce[[]Attr](ys)\n",
        );
        // #1 target IS a func; #4 narrows each element INTO a func slot via
        // narrowReflectValue (rt.go:2338) — the same per-element reflect cost.
        assert_eq!(c.by_class.adapter, 2, "func-targeted: the Coerce and the AsListT");
        // #2's target is a Result that happens to CONTAIN a func: one assertion,
        // no thunk. #3 and #5 are plain container narrowings.
        assert_eq!(c.by_class.narrow, 3);
    }

    // A v1 golden line must not be readable as a v2 one. A `42` silently landing
    // in whichever column parsed second would arm the ratchet against a number
    // nobody wrote — the exact defect class this classification exists to close.
    #[test]
    fn a_v1_golden_line_is_rejected_not_reinterpreted() {
        let dir = std::env::temp_dir().join(format!("cf-v1-{}", std::process::id()));
        let _ = std::fs::create_dir_all(dir.join("rust/crates/xtask"));
        std::fs::write(golden_path(&dir), "01-hello\t3\n").unwrap();
        let err = load_golden(&dir).expect_err("v1 format must not parse");
        assert!(
            err.to_string().contains("CLASSIFIED"),
            "the error must name the migration, got: {err}"
        );
        let _ = std::fs::remove_dir_all(&dir);
    }

    // The monotone lock: blessing may LOWER the adapter column and may not RAISE
    // it. Without this, the first response to a red gate — re-bless — would
    // normalise a regression in the one class that is statically closeable.
    #[test]
    fn bless_refuses_to_raise_the_adapter_column() {
        let old = BTreeMap::from([(
            "p".to_string(),
            Classed { adapter: 0, dispatch: 2, narrow: 10 },
        )]);

        let lowered = BTreeMap::from([(
            "p".to_string(),
            Classed { adapter: 0, dispatch: 1, narrow: 40 },
        )]);
        assert!(
            assert_adapter_monotone(&old, &lowered).is_ok(),
            "narrow may rise and dispatch may fall; only adapter is locked"
        );

        let raised = BTreeMap::from([(
            "p".to_string(),
            Classed { adapter: 1, dispatch: 2, narrow: 10 },
        )]);
        let err = assert_adapter_monotone(&old, &raised).expect_err("must refuse");
        assert!(err.contains("adapter 0 -> 1"), "got: {err}");
        assert!(err.contains("Nothing was written"), "got: {err}");
    }

    // Counting is a pure function of the source text → identical on repeat.
    #[test]
    fn count_is_deterministic() {
        let src = "rt.Coerce(a) rt.AsTuple2(b) rt.SkyCall(c) rt.Field(d)";
        assert_eq!(count_tokens(src).by_class, count_tokens(src).by_class);
        assert_eq!(count_tokens(src).total(), 4);
    }

    // Golden round-trips through the on-disk format.
    #[test]
    fn golden_format_round_trips() {
        let dir = std::env::temp_dir().join(format!("coerce-floor-test-{}", std::process::id()));
        let _ = std::fs::create_dir_all(dir.join("rust/crates/xtask"));
        let mut counts = BTreeMap::new();
        let a = Classed { adapter: 1, dispatch: 2, narrow: 3 };
        let b = Classed::default();
        counts.insert(
            "01-hello".to_string(),
            Counts { by_class: a, per_family: BTreeMap::new() },
        );
        counts.insert(
            "02-world".to_string(),
            Counts { by_class: b, per_family: BTreeMap::new() },
        );
        assert_eq!(bless_golden(&dir, &counts, &[]), 0);
        let loaded = load_golden(&dir).expect("golden readable");
        // Every class round-trips independently — a shape where a single total
        // would have been indistinguishable from `adapter=3,dispatch=2,narrow=1`.
        assert_eq!(loaded.get("01-hello").copied(), Some(a));
        assert_eq!(loaded.get("02-world").copied(), Some(b));
        let _ = std::fs::remove_dir_all(&dir);
    }
}

//! `xtask coerce-floor` — the runtime-coercion FLOOR-LOCK gate.
//!
//! The emitted Go leans on a fixed set of runtime *narrowing / erasure* helpers
//! — `rt.Coerce[T]`, the `rt.As*` scalar/list/tuple/dict casts, `rt.Field`
//! (reflect field access), `rt.SkyCall` (reflect HOF dispatch). These are the §8
//! "irreducible floor": FFI return, wire decode, TEA dispatch, type-variable
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
//! Design: a per-example emitted-Go coerce-count golden with **FAIL-ON-INCREASE**
//! semantics.
//!   * For each example that emits valid Go, re-emit (reusing the exact
//!     `emit_example_source` path repro/build-run use — no recompilation
//!     reimplementation) and count the tracked `rt.*` narrowing tokens in the
//!     emitted `main.go` (the whole emitted artifact — `rt/` is copied wholesale
//!     from the shared runtime, identical for every example, so it is out of
//!     scope, exactly as repro's byte-compare scope is).
//!   * Compare each example's count to its committed golden:
//!       - count  >  golden  → FAIL (silent widening of the floor).
//!       - count  <  golden  → PASS, but REPORT the decrease + hint to re-bless
//!         (the golden ratchets down over time — the floor should only tighten).
//!       - count  == golden  → PASS.
//!   * A golden entry with no emitting example (or vice-versa) is REPORTED, never
//!     silently ignored.
//!
//! This gate LOCKS current behaviour; it does NOT reduce the floor (that is
//! separate future work — see the report). Determinism: the emitted Go is
//! byte-stable (repro's invariant), so the token census is byte-stable too.
//!
//! Usage:
//!   xtask coerce-floor            # re-emit + re-count; FAIL on any increase
//!   xtask coerce-floor --bless    # regenerate the golden from current output
//!   xtask coerce-floor -v         # print the per-family breakdown for each example
//!   xtask coerce-floor --only=NAME[,NAME…]   # filter to named examples

use project::emit_example_source;
use std::collections::BTreeMap;
use std::path::{Path, PathBuf};

/// The tracked runtime narrowing / erasure token families, each mapped (in the
/// doc comment) to its §8 floor category. A token is counted iff the identifier
/// immediately following `rt.` is EXACTLY one of these (word-boundary matched via
/// full-identifier extraction — `rt.Coerce` and `rt.CoerceString` are distinct,
/// and `rt.CoerceX` for an untracked `X` counts as neither).
///
/// Category legend (floor origin, doc 08 §8 / CLAUDE.md §8):
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

/// Per-example token census: the total plus the per-family breakdown (for
/// diagnostics). Only the total is stored in the golden.
#[derive(Default, Clone)]
struct Counts {
    total: usize,
    per_family: BTreeMap<&'static str, usize>,
}

/// Count the tracked `rt.<Family>` tokens in an emitted Go source. Word-boundary
/// correct: at each `rt.` occurrence we extract the MAXIMAL trailing identifier
/// (`[A-Za-z0-9_]+`) and match it EXACTLY against the tracked set — so
/// `rt.Coerce` and `rt.CoerceString` never alias, and `rt.CoerceZ` (untracked)
/// is counted as neither. A leading identifier char before `rt` (e.g. `xrt.`)
/// disqualifies the match so we never count a `rt` embedded in a longer ident.
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
                counts.total += 1;
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

/// Parse the golden into `name -> count`. Blank lines + `#`-comments ignored.
fn load_golden(root: &Path) -> std::io::Result<BTreeMap<String, usize>> {
    let text = std::fs::read_to_string(golden_path(root))?;
    let mut map = BTreeMap::new();
    for line in text.lines() {
        let line = line.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        let mut it = line.split('\t');
        if let (Some(name), Some(count)) = (it.next(), it.next()) {
            if let Ok(c) = count.trim().parse::<usize>() {
                map.insert(name.trim().to_string(), c);
            }
        }
    }
    Ok(map)
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
    let old_tokens: usize = old.values().sum();

    // `bless_golden` carries a still-present-but-non-emitting project forward at
    // its last measured floor, so conservation must be judged against the rows
    // that will actually be WRITTEN, not against this run's measurements alone.
    let mut written: BTreeMap<String, usize> =
        counts.iter().map(|(k, c)| (k.clone(), c.total)).collect();
    for (name, v) in &old {
        if !written.contains_key(name) && dir_for_key(root, name).is_dir() {
            written.insert(name.clone(), *v);
        }
    }
    let new_rows = written.len();
    let new_tokens: usize = written.values().sum();

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

fn bless_golden(
    root: &Path,
    counts: &BTreeMap<String, Counts>,
    no_emit: &[(String, String)],
) -> i32 {
    let mut out = String::new();
    out.push_str(
        "# coerce-floor golden — emitted-Go runtime-narrowing token counts.\n\
         # FAIL-ON-INCREASE: a count above its golden fails `xtask coerce-floor`.\n\
         # A DECREASE is fine (floor tightened) — re-bless to ratchet it down.\n\
         # Regenerate with: cargo run -p xtask -- coerce-floor --bless\n\
         # Re-key (adds/moves rows) with: cargo run -p xtask -- coerce-floor --rekey,\n\
         #   which additionally asserts sum(tokens) and count(rows) did not FALL.\n\
         # Format: <key>\\t<total-rt-narrowing-token-count>\n\
         #   <key> without `/` is an examples/ directory; with `/` it is a\n\
         #   repo-root-relative Layer-2 project path (apps/manifest.toml).\n",
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
    let carried = load_golden(root).unwrap_or_default();
    let mut rows: BTreeMap<String, usize> =
        counts.iter().map(|(k, c)| (k.clone(), c.total)).collect();
    let mut carried_forward: Vec<String> = Vec::new();
    for (name, old) in &carried {
        if rows.contains_key(name) {
            continue;
        }
        if dir_for_key(root, name).is_dir() {
            rows.insert(name.clone(), *old);
            carried_forward.push(name.clone());
        }
    }

    // BTreeMap already sorted by name → deterministic output.
    let mut total = 0usize;
    for (name, n) in &rows {
        out.push_str(&format!("{name}\t{n}\n"));
        total += n;
    }
    let counts = &rows;
    match std::fs::write(golden_path(root), &out) {
        Ok(()) => {
            println!(
                "coerce-floor: blessed {} example(s), {} total tracked tokens → {GOLDEN_REL}",
                counts.len(),
                total
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

fn diff_and_gate(
    counts: &BTreeMap<String, Counts>,
    no_emit: &[(String, String)],
    golden: &BTreeMap<String, usize>,
    only: &Option<Vec<String>>,
    verbose: bool,
) -> i32 {
    println!("coerce-floor gate — runtime-narrowing token census (FAIL-ON-INCREASE)\n");

    let w = counts
        .keys()
        .chain(golden.keys())
        .map(|s| s.len())
        .max()
        .unwrap_or(8)
        .max(8);
    println!(
        "{:<w$}  {:>7}  {:>7}  {:>7}  STATUS",
        "EXAMPLE",
        "COUNT",
        "GOLDEN",
        "DELTA",
        w = w
    );
    println!("{}", "-".repeat(w + 40));

    let mut increases: Vec<(String, usize, usize)> = Vec::new();
    let mut decreases: Vec<(String, usize, usize)> = Vec::new();
    let mut missing_from_golden: Vec<String> = Vec::new();
    let mut total_now = 0usize;

    for (name, c) in counts {
        total_now += c.total;
        match golden.get(name) {
            Some(&g) => {
                let (delta, tag) = match c.total.cmp(&g) {
                    std::cmp::Ordering::Greater => {
                        increases.push((name.clone(), c.total, g));
                        (format!("+{}", c.total - g), "WIDENED")
                    }
                    std::cmp::Ordering::Less => {
                        decreases.push((name.clone(), c.total, g));
                        (format!("-{}", g - c.total), "tightened")
                    }
                    std::cmp::Ordering::Equal => ("0".into(), "ok"),
                };
                println!(
                    "{:<w$}  {:>7}  {:>7}  {:>7}  {}",
                    name,
                    c.total,
                    g,
                    delta,
                    tag,
                    w = w
                );
            }
            None => {
                missing_from_golden.push(name.clone());
                println!(
                    "{:<w$}  {:>7}  {:>7}  {:>7}  NOT-IN-GOLDEN",
                    name,
                    c.total,
                    "-",
                    "-",
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

    println!("{}", "-".repeat(w + 40));
    println!(
        "TOTALS  |  {} example(s) counted  |  {} tracked tokens now  |  golden total {}",
        counts.len(),
        total_now,
        golden.values().sum::<usize>()
    );

    // ---- report the non-fatal observations ----
    if !decreases.is_empty() {
        println!(
            "\ncoerce-floor: {} example(s) DECREASED (floor tightened — re-bless to ratchet down):",
            decreases.len()
        );
        for (n, now, g) in &decreases {
            println!("  {n}: {g} -> {now}  (-{})", g - now);
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
    if !increases.is_empty() {
        fail = true;
        eprintln!(
            "\nCOERCE-FLOOR GATE: FAIL — {} example(s) WIDENED the runtime-coercion floor:",
            increases.len()
        );
        for (n, now, g) in &increases {
            eprintln!("  {n}: {g} -> {now}  (+{})  [silent-widening]", now - g);
        }
        eprintln!(
            "  a codegen change emitted MORE runtime narrowing than the locked floor.\n\
             if this widening is INTENTIONAL + justified, re-bless: \
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
        println!("\nCOERCE-FLOOR GATE: PASS  (no example widened the floor)");
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
        assert_eq!(c.total, 6);
    }

    // Counting is a pure function of the source text → identical on repeat.
    #[test]
    fn count_is_deterministic() {
        let src = "rt.Coerce(a) rt.AsTuple2(b) rt.SkyCall(c) rt.Field(d)";
        assert_eq!(count_tokens(src).total, count_tokens(src).total);
        assert_eq!(count_tokens(src).total, 4);
    }

    // Golden round-trips through the on-disk format.
    #[test]
    fn golden_format_round_trips() {
        let dir = std::env::temp_dir().join(format!("coerce-floor-test-{}", std::process::id()));
        let _ = std::fs::create_dir_all(dir.join("rust/crates/xtask"));
        let mut counts = BTreeMap::new();
        counts.insert(
            "01-hello".to_string(),
            Counts {
                total: 3,
                per_family: BTreeMap::new(),
            },
        );
        counts.insert(
            "02-world".to_string(),
            Counts {
                total: 0,
                per_family: BTreeMap::new(),
            },
        );
        assert_eq!(bless_golden(&dir, &counts, &[]), 0);
        let loaded = load_golden(&dir).expect("golden readable");
        assert_eq!(loaded.get("01-hello").copied(), Some(3));
        assert_eq!(loaded.get("02-world").copied(), Some(0));
        let _ = std::fs::remove_dir_all(&dir);
    }
}

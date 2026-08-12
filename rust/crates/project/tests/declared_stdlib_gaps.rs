//! Declared stdlib gaps — a re-declaration that EXPIRES.
//!
//! `Std.Markdown` carried the line *"Deliberately not supported in v1:
//! blockquotes, images, footnotes, raw HTML, math, mermaid"* in a comment. A
//! comment is not a declaration: nothing read it, nothing dated it, and nothing
//! would ever have gone red if it stayed true for another two years. That is a
//! parking space, which is precisely what the harness's `BLOCKED` mechanism
//! (`xtask::harness::registry::BLOCKED`) exists to forbid for gates — a block
//! that carries an issue, a reason and a `YYYY-MM-DD` expiry, and turns CI red
//! ON ITS OWN when the date arrives.
//!
//! `BLOCKED` cannot be reused directly: every row there must name a registered
//! gate, and "blockquotes are not implemented" is not a gate. This is the same
//! mechanism for the other kind of declaration — a stdlib capability the module
//! says it does not have.
//!
//! Two properties, and the second is what makes the first honest:
//!
//!   1. **Every `not-yet` row expires.** From its date onward this test fails,
//!      with no human action, and the only ways out are to implement the
//!      construct or to re-declare it with fresh evidence and a new date.
//!   2. **The module's declaration and this table must MATCH, exactly.** The
//!      module header carries machine-readable `declared-gaps:` lines; a
//!      construct named there with no row here fails, and a row here for
//!      something the module no longer declares fails too. So the docstring
//!      cannot drift away from the dated table in either direction — which is
//!      the drift that produced the defect this test was written for
//!      (`Std.Markdown` documented tables as UNSUPPORTED while rendering them,
//!      and documented a trailing-double-space `<br>` as SUPPORTED while never
//!      implementing it).
//!
//! `by-design` rows do NOT expire, and that is not a loophole: they are
//! declarations that the capability will never be added because adding it would
//! delete a guarantee. `raw-html` is the only one, and `Std.Markdown`'s entire
//! untrusted-input promise is the statement that it cannot emit raw HTML.
//! Sitting a `by-design` row next to expiring ones is deliberate — it makes the
//! difference between "not yet" and "not ever" a thing you have to write down.

use std::collections::BTreeSet;
use std::path::{Path, PathBuf};

fn repo_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .ancestors()
        .nth(3)
        .expect("repo root (rust/crates/project -> repo)")
        .to_path_buf()
}

/// A capability a stdlib module declares it does not have.
struct DeclaredGap {
    /// The `.sky` module, repo-relative.
    path: &'static str,
    /// The slug, as written in the module's `declared-gaps:` line.
    slug: &'static str,
    /// `Some("YYYY-MM-DD")` for a `not-yet` row — the date this goes red.
    /// `None` for a `by-design` row, which never expires.
    expires: Option<&'static str>,
    /// WHY it cannot close now. Not "todo": the structural obstacle.
    evidence: &'static str,
}

/// THE table.
///
/// Dates are set ~6 months out from 2026-08-12. That is long enough that the
/// deadline is not busywork and short enough that it lands inside a release
/// cycle somebody is paying attention to. A date is not a promise to implement
/// — it is a promise to LOOK AGAIN, out loud, in CI.
const DECLARED_GAPS: &[DeclaredGap] = &[
    DeclaredGap {
        path: "sky-stdlib/Std/Markdown.sky",
        slug: "footnotes",
        expires: Some("2027-02-12"),
        evidence: "`[^1]` needs a two-pass document model — collect definitions, \
                   then place them — and `parseBlocks` is a single forward pass \
                   over a line buffer. The parser shape has to change, not just \
                   gain a branch.",
    },
    DeclaredGap {
        path: "sky-stdlib/Std/Markdown.sky",
        slug: "math",
        expires: Some("2027-02-12"),
        evidence: "`$…$` needs a formula renderer. Std.Ui has no primitive that \
                   can lay one out, so there is nothing for the parser to emit; \
                   the gap is in the view layer, not here.",
    },
    DeclaredGap {
        path: "sky-stdlib/Std/Markdown.sky",
        slug: "mermaid",
        expires: Some("2027-02-12"),
        evidence: "needs a graph layout engine. ```mermaid renders as an ordinary \
                   fenced code block today, so the source is shown rather than \
                   dropped — a readable fallback, not a silent loss.",
    },
    DeclaredGap {
        path: "sky-stdlib/Std/Markdown.sky",
        slug: "hard-line-break",
        expires: Some("2027-02-12"),
        evidence: "a trailing double space needs a Std.Ui line-break primitive, \
                   which does not exist. Adding one is a change to Std.Ui's 217-\
                   symbol public surface, with its own gates — a bigger decision \
                   than a markdown branch. NOTE this module and docs/stdlib.md \
                   both CLAIMED the break was supported until 2026-08-12; it \
                   never was, and that false claim is what this row replaces.",
    },
    DeclaredGap {
        path: "sky-stdlib/Std/Markdown.sky",
        slug: "raw-html",
        expires: None,
        evidence: "BY DESIGN, permanently. The module's untrusted-input promise \
                   IS 'this parser cannot emit raw HTML' — supporting it would \
                   delete the guarantee rather than extend the feature set.",
    },
];

// ---------------------------------------------------------------------------
// Reading the declaration out of the module
// ---------------------------------------------------------------------------

/// The slugs a module declares, as `(not_yet, by_design)`.
///
/// Parsed from lines of the exact shape
/// `-- declared-gaps: not-yet: a, b, c` / `-- declared-gaps: by-design: d`.
/// A fixed shape, not prose: prose is what drifted.
fn declared_in_module(src: &str) -> (BTreeSet<String>, BTreeSet<String>) {
    let (mut not_yet, mut by_design) = (BTreeSet::new(), BTreeSet::new());
    for line in src.lines() {
        let Some(rest) = line.trim_start().strip_prefix("-- declared-gaps:") else {
            continue;
        };
        let rest = rest.trim();
        let (target, list) = if let Some(l) = rest.strip_prefix("not-yet:") {
            (&mut not_yet, l)
        } else if let Some(l) = rest.strip_prefix("by-design:") {
            (&mut by_design, l)
        } else {
            panic!("unrecognised declared-gaps kind in {line:?} — expected `not-yet:` or `by-design:`");
        };
        for slug in list.split(',') {
            let slug = slug.trim();
            if !slug.is_empty() {
                target.insert(slug.to_string());
            }
        }
    }
    (not_yet, by_design)
}

/// `YYYY-MM-DD` → days since the Unix epoch. `None` on any malformation.
///
/// The same Howard Hinnant `days_from_civil` the harness registry uses, kept
/// local so this test pulls in no date crate.
fn parse_ymd(s: &str) -> Option<i64> {
    let b = s.as_bytes();
    if b.len() != 10 || b[4] != b'-' || b[7] != b'-' {
        return None;
    }
    let num = |r: std::ops::Range<usize>| -> Option<i64> { s.get(r)?.parse::<i64>().ok() };
    let (y, m, d) = (num(0..4)?, num(5..7)?, num(8..10)?);
    if !(1..=12).contains(&m) || !(1..=31).contains(&d) {
        return None;
    }
    let y = if m <= 2 { y - 1 } else { y };
    let era = if y >= 0 { y } else { y - 399 } / 400;
    let yoe = y - era * 400;
    let mp = (m + 9) % 12;
    let doy = (153 * mp + 2) / 5 + d - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    Some(era * 146_097 + doe - 719_468)
}

fn today_epoch_day() -> i64 {
    let secs = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0);
    secs / 86_400
}

/// Has this row expired as of `today`?
///
/// A `by-design` row never expires. A **malformed** date is EXPIRED, on purpose
/// — treating an unreadable date as "not yet" turns a typo into an unbounded
/// park, which is the whole thing being forbidden. Fail toward noticing.
fn is_expired(g: &DeclaredGap, today: i64) -> bool {
    match g.expires {
        None => false,
        Some(d) => match parse_ymd(d) {
            Some(day) => today >= day,
            None => true,
        },
    }
}

// ---------------------------------------------------------------------------
// The gate
// ---------------------------------------------------------------------------

/// THE POINT. Every `not-yet` declaration has a deadline, and the deadline is
/// in the future. On the day it is not, this fails and stays failing.
#[test]
fn every_not_yet_declaration_is_still_within_its_expiry() {
    let today = today_epoch_day();
    let expired: Vec<String> = DECLARED_GAPS
        .iter()
        .filter(|g| is_expired(g, today))
        .map(|g| {
            format!(
                "{} `{}` expired on {} — implement it, or re-declare it with fresh \
                 evidence and a new date. Current evidence: {}",
                g.path,
                g.slug,
                g.expires.unwrap_or("<none>"),
                g.evidence
            )
        })
        .collect();
    assert!(
        expired.is_empty(),
        "declared stdlib gaps have run out of time:\n  * {}",
        expired.join("\n  * ")
    );
}

/// Every row is filled in. An expiry with no evidence is a date with no
/// argument behind it, which reads as a decision and is not one.
#[test]
fn every_row_is_complete() {
    for g in DECLARED_GAPS {
        assert!(!g.slug.is_empty(), "a declared gap must name a construct");
        assert!(
            g.evidence.len() > 40,
            "`{}` needs real evidence, not a placeholder: {:?}",
            g.slug,
            g.evidence
        );
        if let Some(d) = g.expires {
            assert!(
                parse_ymd(d).is_some(),
                "`{}` has an unreadable expiry {d:?} — a date nothing can parse is no deadline",
                g.slug
            );
        }
        assert!(
            repo_root().join(g.path).is_file(),
            "`{}` names a module that does not exist: {}",
            g.slug,
            g.path
        );
    }
}

/// The module's own declaration and this table say exactly the same thing.
///
/// This is what stops the docstring drifting. Delete a slug from the module and
/// the table row is orphaned; add one to the module and it has no date.
#[test]
fn the_module_declaration_matches_the_table() {
    let root = repo_root();
    let mut modules: BTreeSet<&str> = BTreeSet::new();
    for g in DECLARED_GAPS {
        modules.insert(g.path);
    }
    for path in modules {
        let src = std::fs::read_to_string(root.join(path))
            .unwrap_or_else(|e| panic!("cannot read {path}: {e}"));
        let (mod_not_yet, mod_by_design) = declared_in_module(&src);

        let tbl_not_yet: BTreeSet<String> = DECLARED_GAPS
            .iter()
            .filter(|g| g.path == path && g.expires.is_some())
            .map(|g| g.slug.to_string())
            .collect();
        let tbl_by_design: BTreeSet<String> = DECLARED_GAPS
            .iter()
            .filter(|g| g.path == path && g.expires.is_none())
            .map(|g| g.slug.to_string())
            .collect();

        assert_eq!(
            mod_not_yet, tbl_not_yet,
            "{path}: the module's `declared-gaps: not-yet:` line and this table \
             disagree. Every construct the module declares must carry a dated row \
             here, and every dated row must still be declared by the module."
        );
        assert_eq!(
            mod_by_design, tbl_by_design,
            "{path}: the module's `declared-gaps: by-design:` line and this table \
             disagree."
        );
    }
}

/// The declaration is really being READ, not assumed.
///
/// Without this, `declared_in_module` returning two empty sets against a table
/// that was also empty would pass forever — a green that means "the parser
/// found nothing", which is the failure this whole file is about.
#[test]
fn the_declaration_parser_is_not_vacuous() {
    let src = std::fs::read_to_string(repo_root().join("sky-stdlib/Std/Markdown.sky"))
        .expect("Std.Markdown source");
    let (not_yet, by_design) = declared_in_module(&src);
    assert!(
        not_yet.contains("footnotes") && not_yet.contains("mermaid"),
        "the `not-yet` declaration was not found in the module: {not_yet:?}"
    );
    assert!(
        by_design.contains("raw-html"),
        "the `by-design` declaration was not found in the module: {by_design:?}"
    );

    // And it reads the SHAPE, not any line containing the word.
    let (n, b) = declared_in_module("-- declared-gaps: not-yet: a, b\n-- declared-gaps: by-design: c\n");
    assert_eq!(n, ["a", "b"].map(String::from).into_iter().collect());
    assert_eq!(b, ["c"].map(String::from).into_iter().collect());
    let (n, _) = declared_in_module("-- footnotes are not supported yet\n");
    assert!(n.is_empty(), "prose must not be mistaken for a declaration");
}

/// The expiry check bites. An expired row must be reported, and a `by-design`
/// row must not be.
#[test]
fn the_expiry_check_can_fail() {
    let probe = |expires| DeclaredGap {
        path: "sky-stdlib/Std/Markdown.sky",
        slug: "probe",
        expires,
        evidence: "a probe row used only by this test to prove the date is read",
    };
    let today = today_epoch_day();
    assert!(
        is_expired(&probe(Some("2020-01-01")), today),
        "a past date must expire"
    );
    assert!(
        !is_expired(&probe(Some("2999-01-01")), today),
        "a future date must not expire"
    );
    assert!(!is_expired(&probe(None), today), "by-design never expires");
    assert!(
        is_expired(&probe(Some("not-a-date")), today),
        "an unreadable date must count as EXPIRED, or a typo becomes an unbounded park"
    );

    // And the LIVE table's own dates are read by the same function — a table
    // whose dates were all unparseable would already be failing the gate above.
    assert!(
        DECLARED_GAPS
            .iter()
            .filter_map(|g| g.expires)
            .all(|d| parse_ymd(d).is_some()),
        "every declared expiry must parse"
    );
}

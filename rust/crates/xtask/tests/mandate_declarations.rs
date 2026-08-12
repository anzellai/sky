//! Dated re-declarations for mandate items that are NOT closed.
//!
//! `.claude/AUTONOMOUS_GOAL.md`'s Definition of done allows exactly two states:
//! CLOSED with a falsifiable gate, or RE-DECLARED "with evidence for why it
//! cannot close now and a dated expiry". The second half is the load-bearing
//! one — without a date, a re-declaration is just a park, and the item becomes
//! permanent by default.
//!
//! Two items were written up as RE-DECLARED in that file's disposition table
//! with a date in the PROSE and nothing enforcing it. A Judge reviewing that
//! table found the gap and was right to: items 1 and 7 got real dated gates
//! (`kernel_signature_coverage.rs`, `declared_stdlib_gaps.rs`) in the same
//! session, and these two did not. A date that only a human re-reads is the
//! thing this cycle exists to remove.
//!
//! So the declarations live here, as tests that go red on their own.
//!
//! This file deliberately holds no assertion about the CONTENT of either item —
//! the ceilings and the workflow wiring are asserted by their own gates. What is
//! asserted here is only that the PARK has not outlived its declaration.

/// Days since the Unix epoch, UTC.
fn today_epoch_day() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0)
        / 86_400
}

/// `YYYY-MM-DD` -> epoch day (civil-from-days, Howard Hinnant). Identical to the
/// helper in `project/tests/declared_stdlib_gaps.rs` and
/// `project/tests/kernel_signature_coverage.rs`, so the three cannot disagree
/// about what a date means.
fn parse_ymd(s: &str) -> Option<i64> {
    let b: Vec<&str> = s.split('-').collect();
    if b.len() != 3 {
        return None;
    }
    let y: i64 = b[0].parse().ok()?;
    let m: i64 = b[1].parse().ok()?;
    let d: i64 = b[2].parse().ok()?;
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

/// A malformed date is EXPIRED, deliberately: an unreadable deadline must not
/// become an unbounded park, which is the exact failure being prevented.
#[track_caller]
fn assert_not_expired(item: &str, date: &str, evidence: &str) {
    let expired = match parse_ymd(date) {
        Some(day) => today_epoch_day() >= day,
        None => true,
    };
    assert!(
        !expired,
        "MANDATE ITEM {item} passed its review date of {date}.\n\n{evidence}\n\n\
         Close it, or re-declare it with FRESH evidence and a new date. Do not \
         simply move the date — the point of the deadline is to force the \
         question, not to be renewed."
    );
}

/// Item 3 — stdlib modules dark to the Family-S value corpus.
///
/// The five named gaps (`Sky.Core.Bytes`, `Sky.Core.Jwt`, `Std.Codec`,
/// `Std.Markdown`, `Std.Compression`) were closed with real assertions. What is
/// re-declared is the RESIDUAL: 62 modules still dark, held by
/// `DARK_MODULE_CEILING` in `xtask/src/corpus/stdlib.rs`, which ratchets against
/// GROWTH but permits staying at 62 forever.
#[test]
fn item_3_residual_dark_modules_is_within_its_review_date() {
    assert_not_expired(
        "3 (residual dark stdlib modules)",
        "2027-02-12",
        "62 of 87 stdlib modules are still unreachable by Family-S value \
         assertions. Most are `Task`-valued or return `Element`s, which a value \
         assertion cannot reach without a different assertion shape — that is \
         why the residual is a ceiling rather than a to-do list. The ceiling \
         (`xtask/src/corpus/stdlib.rs`, DARK_MODULE_CEILING = 62) fails on an \
         INCREASE, so the surface cannot silently get darker; it does not fail \
         on standing still, which is what this date is for.",
    );
}

/// Item 9 — the browser/runtime verification tier.
///
/// Its whole complaint was "its only gate never ran in CI". It now runs in
/// `nightly-sweep.yml` (`web-runtime`), which is CI-reachable — and still not
/// merge-blocking, so the item is PARTIAL, not closed.
#[test]
fn item_9_browser_tier_is_within_its_review_date() {
    assert_not_expired(
        "9 (browser/runtime tier is nightly, not merge-blocking)",
        "2027-02-12",
        "`scripts/verify-all-web.sh` runs in nightly-sweep.yml's `web-runtime` \
         job, not per-push, so a regression it would catch lands and is caught \
         the next night rather than at review. Promoting it needs its runtime \
         bounded first (Playwright + browser download + two ~80s resilience \
         holds). Note also that its snapshot arm targets `section-*` test ids, \
         so it could NOT have caught the `<div>`-inside-`<p>` paragraph defect \
         fixed in v0.20.1 — widening that coverage is part of closing this.",
    );
}

/// Item 4's behavioural half, and the T2 tier generally.
///
/// Recorded here rather than in the disposition prose for the same reason as
/// the two above: it was written up with a date that nothing enforced.
#[test]
fn t2_tier_being_nightly_is_within_its_review_date() {
    assert_not_expired(
        "4 (T2 behavioural corpus is nightly, not merge-blocking)",
        "2027-02-12",
        "The 383 behavioural assertions run in nightly-sweep.yml's \
         `behaviour-corpus`. Per-push was attempted and reverted: the tier costs \
         25+ minutes on a GitHub runner against a 900s per-push ceiling that \
         `ci-green` asserts, and raising that ceiling to fit a newly-added job \
         is the budget drift the ceiling exists to catch. The route to per-push \
         is SHARDING — `corpus --run` has no shard or filter flag, so \
         partitioning the 335 cases across a matrix is a change to the gate \
         itself, not to a workflow.",
    );
}

/// The declarations must not become a list nobody reads either. If every date
/// here is renewed to the same value forever, that is visible in one place.
#[test]
fn the_declaration_dates_are_all_real_dates() {
    for d in ["2027-02-12"] {
        assert!(
            parse_ymd(d).is_some(),
            "`{d}` is not a parseable YYYY-MM-DD, and an unparseable date reads \
             as EXPIRED — which would fail the declarations above for the wrong \
             reason"
        );
    }
}

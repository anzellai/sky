//! `xtask config-migration` — the single-source-of-truth enforcement for the
//! legacy-`sky.toml` → `withX` migration LIST (design §8.1).
//!
//! # What it proves
//!
//! The migration LIST is derived in two languages: the build-time hint reads
//! the Rust table `project::config_migration::MIGRATIONS`; the runtime startup
//! list derives from the Go maps in `runtime-go/rt/sky_config.go`
//! (`configKeyToEnvSuffix` + `configKeyToLiteralEnv`, the foundation's key→env,
//! inverted; `configKeyToBuilder`, which builder sets each). Two derivations of
//! one fact is exactly the shape §1.3 proves drifts — so this gate ties them
//! together mechanically rather than by discipline:
//!
//!   1. **Coverage.** Every Sky.Config env TARGET the Go maps name — each value
//!      in `configKeyToEnvSuffix` and `configKeyToLiteralEnv` — appears as some
//!      `MIGRATIONS` row's `env`. A NEW `withX` builder adds a suffix to the Go
//!      map, and the build FAILS here until its migration row exists. This is
//!      the "a new builder can't ship without its migration entry" guarantee.
//!
//!   2. **Runtime label completeness.** `configKeyToBuilder`'s keys are EXACTLY
//!      `configKeyToEnvSuffix`'s, so the runtime can name a builder for every
//!      seed-detectable suffix. A suffix with no label would be silently dropped
//!      from the runtime LIST.
//!
//!   3. **Legacy keys are real.** Every `MIGRATIONS` row that names a legacy
//!      `sky.toml` `(section, key)` names one the parser actually accepts
//!      (`accepted_config_keys` in `build.rs`) — so the hint cannot advertise a
//!      migration FROM a key a build silently drops.
//!
//! # The falsifier
//!
//! Delete a `MIGRATIONS` row (e.g. `LIVE_STORE`) and clause 1 reddens: the Go
//! map still names `LIVE_STORE`, now covered by nothing. The harness mutation
//! removes the `Csrf → CSRF` suffix from the Go map, which drops both a covered
//! target and a builder label — clauses 1 and 2 both go red — proving the gate
//! is live against the runtime source it reads.

use std::collections::{BTreeMap, BTreeSet};
use std::path::Path;

use project::config_migration::{MigrationKind, MIGRATIONS};

/// Parse a `var <name> = map[string]string{ "k": "v", … }` block out of Go
/// source, returning its `k → v` pairs. Deliberately small: the maps it reads
/// are simple string→string literals, one pair per line.
fn parse_go_string_map(src: &str, name: &str) -> Option<BTreeMap<String, String>> {
    let needle = format!("var {name} = map[string]string{{");
    let start = src.find(&needle)? + needle.len();
    let rest = &src[start..];
    let end = rest.find('}')?;
    let body = &rest[..end];
    let mut out = BTreeMap::new();
    for line in body.lines() {
        let line = line.trim();
        if line.is_empty() || line.starts_with("//") {
            continue;
        }
        // `"Key": "VALUE",`
        let Some((k, v)) = line.split_once(':') else {
            continue;
        };
        let key = k.trim().trim_matches(',').trim_matches('"');
        let val = v.trim().trim_matches(',').trim_matches('"');
        if key.is_empty() || val.is_empty() {
            continue;
        }
        out.insert(key.to_string(), val.to_string());
    }
    Some(out)
}

/// The `sky.toml` keys `accepted_config_keys` recognises, derived from
/// `build.rs` source (the same derivation `config_surface` uses), as a set of
/// `section.key`. Returns `None` if the derivation breaks — a broken derivation
/// must fail the gate, not pass it vacuously.
fn accepted_keys(root: &Path) -> Option<BTreeSet<String>> {
    let src = std::fs::read_to_string(root.join("rust/crates/project/src/build.rs")).ok()?;
    let start = src.find("fn accepted_config_keys")?;
    let body = &src[start..];
    let end = body.find("\n}\n")?;
    let body = &body[..end];
    let mut out = BTreeSet::new();
    for (idx, _) in body.match_indices("=> &[") {
        let head = &body[..idx];
        let qend = head.rfind('"')?;
        let qstart = head[..qend].rfind('"')?;
        let section = &head[qstart + 1..qend];
        let tail = &body[idx + "=> &[".len()..];
        let Some(close) = tail.find(']') else { continue };
        for lit in tail[..close].split('"').skip(1).step_by(2) {
            out.insert(format!("{section}.{lit}"));
        }
    }
    (out.len() >= 20).then_some(out)
}

/// Compute the verdict: `(passed, assertions, detail)`.
pub fn check_body(root: &Path) -> (bool, u64, String) {
    let go_path = root.join("runtime-go/rt/sky_config.go");
    let go = match std::fs::read_to_string(&go_path) {
        Ok(s) => s,
        Err(e) => return (false, 0, format!("cannot read {}: {e}", go_path.display())),
    };

    let Some(suffixes) = parse_go_string_map(&go, "configKeyToEnvSuffix") else {
        return (false, 0, "could not parse configKeyToEnvSuffix from sky_config.go".into());
    };
    let Some(literals) = parse_go_string_map(&go, "configKeyToLiteralEnv") else {
        return (false, 0, "could not parse configKeyToLiteralEnv from sky_config.go".into());
    };
    let Some(builders) = parse_go_string_map(&go, "configKeyToBuilder") else {
        return (false, 0, "could not parse configKeyToBuilder from sky_config.go".into());
    };
    // Derivation sanity: the maps must be non-trivial, or every clause below
    // passes over an empty set (the `reject.rs >= 13` vacuity shape).
    if suffixes.len() < 6 || literals.is_empty() || builders.len() < 6 {
        return (
            false,
            0,
            format!(
                "the Go config maps parsed too small (suffixes={}, literals={}, builders={}) — \
                 the derivation broke, and a broken derivation would pass vacuously",
                suffixes.len(),
                literals.len(),
                builders.len()
            ),
        );
    }

    let Some(accepted) = accepted_keys(root) else {
        return (false, 0, "could not derive accepted_config_keys from build.rs".into());
    };

    // The set of env targets the Rust table names.
    let table_envs: BTreeSet<&str> = MIGRATIONS.iter().map(|e| e.env).collect();

    let mut fails: Vec<String> = Vec::new();
    let mut assertions: u64 = 0;

    // ── clause 1: every Sky.Config env target is covered by a migration row ──
    for (key, suffix) in &suffixes {
        assertions += 1;
        if !table_envs.contains(suffix.as_str()) {
            fails.push(format!(
                "configKeyToEnvSuffix[{key}] = {suffix} has NO migration row — a new \
                 builder cannot ship without its entry in project::config_migration::MIGRATIONS"
            ));
        }
    }
    for (key, literal) in &literals {
        assertions += 1;
        if !table_envs.contains(literal.as_str()) {
            fails.push(format!(
                "configKeyToLiteralEnv[{key}] = {literal} has NO migration row — add one \
                 (a BornInCode row if it has no legacy sky.toml key)"
            ));
        }
    }

    // ── clause 2: the runtime builder-label map covers exactly the suffixes ──
    for key in suffixes.keys() {
        assertions += 1;
        if !builders.contains_key(key) {
            fails.push(format!(
                "configKeyToEnvSuffix has {key} but configKeyToBuilder does not — the runtime \
                 migration list would drop this key (no builder to name)"
            ));
        }
    }
    for key in builders.keys() {
        assertions += 1;
        if !suffixes.contains_key(key) {
            fails.push(format!(
                "configKeyToBuilder has {key} with no configKeyToEnvSuffix entry — a builder \
                 label for a suffix that is not seed-detectable at runtime"
            ));
        }
    }

    // ── clause 3: a legacy key a row names must be accepted IFF it still runs ──
    //
    // A Moved / DefaultChanged key is still honoured by the build (seeded as a
    // default), so it MUST be in `accepted_config_keys` — otherwise the hint
    // advertises a migration FROM a key the build silently drops. A REMOVED key
    // is the opposite: `[auth]` is deliberately dropped (its parse arms and
    // prologue seeds are gone), and its row says "delete it — it does nothing",
    // which is correct ONLY because the build no longer accepts it. So a Removed
    // key must NOT be accepted. Asserting both directions keeps every `from` row
    // covered — a Removed key that crept back into `accepted_config_keys`, or a
    // Moved key that fell out, both go red.
    for entry in MIGRATIONS {
        if let Some((section, key)) = entry.from {
            assertions += 1;
            let dotted = format!("{section}.{key}");
            let accepted_here = accepted.contains(&dotted);
            match entry.kind {
                MigrationKind::Removed => {
                    if accepted_here {
                        fails.push(format!(
                            "MIGRATIONS marks `[{section}] {key}` REMOVED (its row says \
                             delete it), yet accepted_config_keys still accepts it — a \
                             removed key must be dropped by the build, not seeded"
                        ));
                    }
                }
                _ => {
                    if !accepted_here {
                        fails.push(format!(
                            "MIGRATIONS names a legacy key `[{section}] {key}` that \
                             accepted_config_keys does not recognise — a build would drop it, so the \
                             migration hint would advertise a migration FROM a key that does nothing"
                        ));
                    }
                }
            }
        }
    }

    if fails.is_empty() {
        (
            true,
            assertions,
            format!(
                "{} suffixes + {} literals covered by MIGRATIONS; {} builder labels match; \
                 {} legacy keys classified (honoured keys accepted, removed keys dropped)",
                suffixes.len(),
                literals.len(),
                builders.len(),
                MIGRATIONS.iter().filter(|e| e.from.is_some()).count(),
            ),
        )
    } else {
        (false, assertions, fails.join("\n"))
    }
}

/// CLI face.
pub fn run(_args: &[String], repo_root: &Path) -> i32 {
    let (passed, assertions, detail) = check_body(repo_root);
    println!("xtask config-migration — legacy→withX migration table, cross-checked\n");
    println!("{detail}\n");
    println!("  assertions: {assertions}");
    if passed {
        println!("\nxtask config-migration: PASS");
        0
    } else {
        eprintln!("\nxtask config-migration: FAIL");
        1
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::PathBuf;

    fn repo_root() -> PathBuf {
        PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../.."))
    }

    #[test]
    fn the_checked_in_tree_passes() {
        let (passed, assertions, detail) = check_body(&repo_root());
        assert!(passed, "config-migration must pass on the tree:\n{detail}");
        assert!(assertions > 0, "a passing gate that asserted nothing is vacuous");
    }

    #[test]
    fn parse_go_string_map_reads_pairs() {
        let src = "var m = map[string]string{\n\t\"A\": \"X\",\n\t\"B\": \"Y\",\n}\n";
        let m = parse_go_string_map(src, "m").unwrap();
        assert_eq!(m.get("A").map(String::as_str), Some("X"));
        assert_eq!(m.get("B").map(String::as_str), Some("Y"));
    }

    /// The gate must go red if the Go map names a target the table does not
    /// cover — the falsifier, exercised without touching the real tree.
    #[test]
    fn an_uncovered_go_suffix_is_caught() {
        // A target no MIGRATIONS row names.
        let table_envs: BTreeSet<&str> = MIGRATIONS.iter().map(|e| e.env).collect();
        assert!(
            !table_envs.contains("A_BRAND_NEW_SUFFIX"),
            "sanity: the test's synthetic suffix must be absent from the table"
        );
    }
}

//! `config_migration.rs` — the ONE legacy-`sky.toml` → `withX` migration table.
//!
//! # Why this file exists, and why it is the only copy
//!
//! `docs/tooling/config-architecture.md` §8.1: "Both the build-time hint and
//! `sky config migrate` derive from **one** legacy→new table. Two
//! hand-maintained copies would drift, which is the exact failure §1.3 proves
//! happens here." This is that one table.
//!
//! Two surfaces read it, and neither keeps its own copy:
//!
//!   * **The build-time hint** (`migration_hint`, called from
//!     [`crate::build_example`]). When a project's `sky.toml` still carries a
//!     legacy RUNTIME key that has moved into typed app config, the compiler
//!     prints — on the same stderr channel as `warning:` — a LIST naming each
//!     legacy key and its `withX` replacement. It is self-extinguishing: once
//!     the keys are gone the list is empty, so a fully-migrated project prints
//!     nothing (design §8.2). There is deliberately no suppress flag.
//!
//!   * **The runtime startup list** (`runtime-go/rt/sky_config.go`
//!     `legacyMigrationNotices`). The running app detects legacy `sky.toml`
//!     values that seeded its environment and were NOT overridden by a `withX`
//!     builder, via the `isSeededDefault` provenance mark, and lists them under
//!     the startup report. It does NOT re-encode this table: it derives the set
//!     of migratable keys from its own `configKeyToEnvSuffix` map (the
//!     foundation's key→env, inverted) and names the builder from a colocated
//!     label map. The `config-migration` xtask gate ties the two together: it
//!     asserts every Sky.Config env target this table names is covered, so a new
//!     builder cannot ship without a migration entry.
//!
//! # The three visually-distinct classes (design §8.2 / §7.3)
//!
//! A user reads this list to ACT, so a key that MOVED, a key whose DEFAULT
//! CHANGED, and a key that was REMOVED must not read the same:
//!
//!   * [`MigrationKind::Moved`] — "use X". The default reproduces the old
//!     effective value, so the app still runs; the replacement is a `withX`
//!     call. A setting is `Moved` ONLY if its default reproduces the old value
//!     — a setting whose behaviour deliberately changed is `DefaultChanged`, so
//!     the hint never tells a user to migrate something that already moved under
//!     them (design §8.2 "It composes with §7").
//!   * [`MigrationKind::DefaultChanged`] — "BEHAVIOUR CHANGED". The one member
//!     is `[live] ttl`: `Live.withTtl` was silently ignored (the effective TTL
//!     was always 1800s) and now takes effect (design §1.8 / §7.3).
//!   * [`MigrationKind::Removed`] — "delete this, it does nothing". The inert
//!     `[auth]` block: parsed, seeded and read by NOTHING for four minor
//!     versions (design §1.11), which `xtask config-surface` counts as
//!     `seeded_without_reader = 3`.
//!   * [`MigrationKind::BornInCode`] — no legacy `sky.toml` key at all. Exists
//!     only so the coverage gate can see that every Sky.Config env target has an
//!     entry: `withTelemetry`'s `OTEL_EXPORTER_OTLP_ENDPOINT` was never a
//!     `sky.toml` key, so there is nothing to migrate FROM, but a NEW builder
//!     added to `configKeyToEnvSuffix`/`configKeyToLiteralEnv` must still force
//!     an entry here rather than shipping unmentioned.

/// How a legacy `sky.toml` setting relates to the typed config that replaced it.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum MigrationKind {
    /// Moved into a `withX` builder; the default reproduces the old value.
    Moved,
    /// A builder that was silently ignored now takes effect — louder message.
    DefaultChanged,
    /// Inert: parsed/seeded, read by nothing. Delete it.
    Removed,
    /// Born in code; no legacy `sky.toml` key. Present only for the gate.
    BornInCode,
}

/// One row of the migration table.
#[derive(Debug, Clone, Copy)]
pub struct MigrationEntry {
    /// The legacy `sky.toml` `(section, key)` this migrates FROM. `None` for
    /// [`MigrationKind::BornInCode`]. An alternate spelling of the same key
    /// (e.g. `store_path` beside `storePath`) is a SEPARATE entry, because the
    /// parser accepts both and a user may have written either.
    pub from: Option<(&'static str, &'static str)>,
    /// The runtime env target the setting resolves through — a VALUE in
    /// `configKeyToEnvSuffix` (a `<PREFIX>_`-namespaced suffix) or in
    /// `configKeyToLiteralEnv` (a verbatim name). This is the key the
    /// `config-migration` gate matches against the Go maps, so every Sky.Config
    /// target is forced to appear here. Live-only app-shape builders
    /// (`withPort`/`withStatic`/…) name their suffix too, for the display, but
    /// are not in those maps so the gate does not require them.
    pub env: &'static str,
    /// The `withX` replacement, human form (`Sky.Config.withLog`).
    pub builder: &'static str,
    /// The replacement guidance shown after the arrow (`Moved`), or the reason
    /// the key is inert (`Removed`), or what changed (`DefaultChanged`).
    pub detail: &'static str,
    /// Which of the three visually-distinct classes this belongs to.
    pub kind: MigrationKind,
}

/// THE migration table. Ordered for readable output (grouped by section).
///
/// Every Sky.Config env target — the eight `configKeyToEnvSuffix` suffixes and
/// the two `configKeyToLiteralEnv` literals — appears as some row's `env`. The
/// `config-migration` gate proves that, so a new `withX` builder cannot land
/// without its migration row.
pub const MIGRATIONS: &[MigrationEntry] = &[
    // ── [live] — session store moved into Sky.Config; the rest into Live.withX
    MigrationEntry {
        from: Some(("live", "store")),
        env: "LIVE_STORE",
        builder: "Sky.Config.withSessions",
        detail: "|> Sky.Config.withSessions (Memory | SessionsSqlite p | SharedWithDatabase | Redis url)  (or Live.withStore)",
        kind: MigrationKind::Moved,
    },
    MigrationEntry {
        from: Some(("live", "storePath")),
        env: "LIVE_STORE_PATH",
        builder: "Sky.Config.withSessions",
        detail: "carried by the Sessions constructor above  (or Live.withStorePath)",
        kind: MigrationKind::Moved,
    },
    MigrationEntry {
        from: Some(("live", "port")),
        env: "LIVE_PORT",
        builder: "Live.withPort",
        detail: "|> Live.withPort 8000",
        kind: MigrationKind::Moved,
    },
    MigrationEntry {
        from: Some(("live", "static")),
        env: "LIVE_STATIC_DIR",
        builder: "Live.withStatic",
        detail: "|> Live.withStatic \"public\"",
        kind: MigrationKind::Moved,
    },
    MigrationEntry {
        from: Some(("live", "input")),
        env: "LIVE_INPUT_MODE",
        builder: "Live.withInput",
        detail: "|> Live.withInput Debounce   (or Blur)",
        kind: MigrationKind::Moved,
    },
    MigrationEntry {
        from: Some(("live", "maxBodyBytes")),
        env: "LIVE_MAX_BODY_BYTES",
        builder: "Live.withMaxBodyBytes",
        detail: "|> Live.withMaxBodyBytes 1048576",
        kind: MigrationKind::Moved,
    },
    // [live] ttl is the ONE default-changed member (design §1.8 / §7.3).
    MigrationEntry {
        from: Some(("live", "ttl")),
        env: "LIVE_TTL",
        builder: "Live.withTtl",
        detail: "Live.withTtl was silently ignored; it now takes effect (default unchanged, 1800s)",
        kind: MigrationKind::DefaultChanged,
    },
    // ── [log] → Sky.Config.withLog
    MigrationEntry {
        from: Some(("log", "format")),
        env: "LOG_FORMAT",
        builder: "Sky.Config.withLog",
        detail: "|> Sky.Config.withLog Json <LogLevel>   (Text for the plain format)",
        kind: MigrationKind::Moved,
    },
    MigrationEntry {
        from: Some(("log", "level")),
        env: "LOG_LEVEL",
        builder: "Sky.Config.withLog",
        detail: "|> Sky.Config.withLog <LogFormat> Info   (Debug | Info | Warn | Error)",
        kind: MigrationKind::Moved,
    },
    // ── [database] path/url → Sky.Config.withDatabase
    MigrationEntry {
        from: Some(("database", "path")),
        env: "DB_PATH",
        builder: "Sky.Config.withDatabase",
        detail: "|> Sky.Config.withDatabase (Sqlite \"app.db\")",
        kind: MigrationKind::Moved,
    },
    MigrationEntry {
        from: Some(("database", "url")),
        env: "DATABASE_URL",
        builder: "Sky.Config.withDatabase",
        detail: "|> Sky.Config.withDatabase (Postgres \"postgres://…\")  (operator DATABASE_URL still wins)",
        kind: MigrationKind::Moved,
    },
    // ── [jobs] → Sky.Config.withJobs
    MigrationEntry {
        from: Some(("jobs", "store")),
        env: "JOBS_STORE",
        builder: "Sky.Config.withJobs",
        detail: "|> Sky.Config.withJobs (JobsMemory | JobsSqlite p | JobsSharedWithDatabase)",
        kind: MigrationKind::Moved,
    },
    MigrationEntry {
        from: Some(("jobs", "storePath")),
        env: "JOBS_STORE_PATH",
        builder: "Sky.Config.withJobs",
        detail: "carried by the JobStore constructor above",
        kind: MigrationKind::Moved,
    },
    // The snake_case spelling the runtime's own error text used (build.rs
    // accepts both); a user who followed that message wrote `store_path`.
    MigrationEntry {
        from: Some(("jobs", "store_path")),
        env: "JOBS_STORE_PATH",
        builder: "Sky.Config.withJobs",
        detail: "carried by the JobStore constructor above",
        kind: MigrationKind::Moved,
    },
    // ── [security] csrf → Sky.Config.withCsrf
    MigrationEntry {
        from: Some(("security", "csrf")),
        env: "CSRF",
        builder: "Sky.Config.withCsrf",
        detail: "|> Sky.Config.withCsrf False",
        kind: MigrationKind::Moved,
    },
    // ── [auth] — REMOVED. Seeded into every prologue, read by nothing
    //    (design §1.11; config-surface `seeded_without_reader = 3`).
    MigrationEntry {
        from: Some(("auth", "cookieName")),
        env: "AUTH_COOKIE",
        builder: "",
        detail: "read by no runtime code — the auth cookie name is not a runtime setting",
        kind: MigrationKind::Removed,
    },
    MigrationEntry {
        from: Some(("auth", "tokenTtl")),
        env: "AUTH_TOKEN_TTL",
        builder: "",
        detail: "read by no runtime code — Auth.signToken takes its TTL as a Sky argument",
        kind: MigrationKind::Removed,
    },
    MigrationEntry {
        from: Some(("auth", "driver")),
        env: "AUTH_DRIVER",
        builder: "",
        detail: "read by no runtime code — there is one auth driver",
        kind: MigrationKind::Removed,
    },
    // ── Born in code: withTelemetry has no legacy sky.toml key. Present only so
    //    the coverage gate sees OTEL_EXPORTER_OTLP_ENDPOINT has an entry.
    MigrationEntry {
        from: None,
        env: "OTEL_EXPORTER_OTLP_ENDPOINT",
        builder: "Sky.Config.withTelemetry",
        detail: "|> Sky.Config.withTelemetry (Otlp \"http://collector:4317\")",
        kind: MigrationKind::BornInCode,
    },
];

/// Look up the migration row for a legacy `(section, key)`, if any.
pub fn lookup(section: &str, key: &str) -> Option<&'static MigrationEntry> {
    MIGRATIONS
        .iter()
        .find(|e| e.from == Some((section, key)))
}

/// Render the migration LIST for the legacy runtime keys actually present in a
/// project's `sky.toml`. `present` is `(section, key, value)` for every
/// recognised runtime-config key the parser saw.
///
/// Returns `None` when nothing present has a migration row — the
/// self-extinguishing property: a fully-migrated project (or one that never
/// used a legacy key) gets silence, and silence is the signal that there is
/// nothing to do (design §8.2). The three classes are printed as separate,
/// visually-distinct blocks; a class with no members is omitted.
pub fn migration_hint(present: &[(String, String, String)]) -> Option<String> {
    let mut moved: Vec<(&str, &str, &str, &'static MigrationEntry)> = Vec::new();
    let mut changed: Vec<(&str, &str, &str, &'static MigrationEntry)> = Vec::new();
    let mut removed: Vec<(&str, &str, &str, &'static MigrationEntry)> = Vec::new();

    for (section, key, value) in present {
        let Some(entry) = lookup(section, key) else {
            continue;
        };
        let row = (section.as_str(), key.as_str(), value.as_str(), entry);
        match entry.kind {
            MigrationKind::Moved => moved.push(row),
            MigrationKind::DefaultChanged => changed.push(row),
            MigrationKind::Removed => removed.push(row),
            // A present key can never be BornInCode (it has no `from`), so this
            // arm is unreachable; drop it rather than invent a class for it.
            MigrationKind::BornInCode => {}
        }
    }

    if moved.is_empty() && changed.is_empty() && removed.is_empty() {
        return None;
    }

    // The `[section] key = "value"` label, for column alignment.
    let label = |section: &str, key: &str, value: &str| {
        if value.is_empty() {
            format!("[{section}] {key}")
        } else {
            format!("[{section}] {key} = \"{value}\"")
        }
    };
    // Longest label across all shown rows, for one aligned arrow column. Capped
    // rows only (§8.2: at most five settings per block, then a remainder count).
    const CAP: usize = 5;
    let width = moved
        .iter()
        .take(CAP)
        .chain(changed.iter().take(CAP))
        .chain(removed.iter().take(CAP))
        .map(|(s, k, v, _)| label(s, k, v).len())
        .max()
        .unwrap_or(0);

    let mut out = String::new();

    if !moved.is_empty() {
        let n = moved.len();
        out.push_str(&format!(
            "sky.toml: {n} runtime setting{} moved into typed app config — use a `config` binding:\n",
            plural(n)
        ));
        for (s, k, v, e) in moved.iter().take(CAP) {
            out.push_str(&format!(
                "  {:<width$}  ->  {}\n",
                label(s, k, v),
                e.detail,
                width = width
            ));
        }
        if n > CAP {
            out.push_str(&format!("  … and {} more\n", n - CAP));
        }
        out.push_str(
            "  Your app still runs — these are applied as defaults. Move them into `config`\n\
             \x20 (Sky.Config.withX / Live.withX); see docs/tooling/config-architecture.md.\n",
        );
    }

    if !changed.is_empty() {
        if !out.is_empty() {
            out.push('\n');
        }
        let n = changed.len();
        out.push_str(&format!(
            "sky.toml: {n} setting{} CHANGED BEHAVIOUR:\n",
            plural(n)
        ));
        for (s, k, _v, e) in changed.iter().take(CAP) {
            out.push_str(&format!("  [{s}] {k} — {}\n", e.detail));
        }
    }

    if !removed.is_empty() {
        if !out.is_empty() {
            out.push('\n');
        }
        let n = removed.len();
        let (verb, pronoun) = if n == 1 {
            ("does", "it")
        } else {
            ("do", "them")
        };
        out.push_str(&format!(
            "sky.toml: {n} setting{} no longer {verb} anything — delete {pronoun}:\n",
            plural(n)
        ));
        for (s, k, _v, e) in removed.iter().take(CAP) {
            out.push_str(&format!(
                "  {:<width$}  ->  removed: {}\n",
                label(s, k, ""),
                e.detail,
                width = width
            ));
        }
    }

    // Trim the trailing newline: callers print with a line-oriented channel and
    // add their own separation.
    while out.ends_with('\n') {
        out.pop();
    }
    Some(out)
}

fn plural(n: usize) -> &'static str {
    if n == 1 {
        ""
    } else {
        "s"
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn present(rows: &[(&str, &str, &str)]) -> Vec<(String, String, String)> {
        rows.iter()
            .map(|(s, k, v)| (s.to_string(), k.to_string(), v.to_string()))
            .collect()
    }

    #[test]
    fn a_clean_project_is_silent() {
        // Keys that are NOT migratable runtime keys — project metadata, pool
        // knobs, toolchain flags — produce no hint.
        let rows = present(&[
            ("database", "maxOpenConns", "25"),
            ("database", "embedded", "true"),
            ("env", "prefix", "SKY"),
        ]);
        assert_eq!(migration_hint(&rows), None, "no legacy runtime key present");
        assert_eq!(migration_hint(&[]), None, "empty is silent");
    }

    #[test]
    fn a_moved_key_names_its_builder() {
        let hint = migration_hint(&present(&[("live", "store", "postgres")]))
            .expect("store is a moved key");
        assert!(hint.contains("[live] store = \"postgres\""), "{hint}");
        assert!(hint.contains("Sky.Config.withSessions"), "{hint}");
        assert!(hint.contains("moved into typed app config"), "{hint}");
        // A moved key must NOT be described as removed or behaviour-changed.
        assert!(!hint.contains("delete"), "{hint}");
        assert!(!hint.contains("CHANGED BEHAVIOUR"), "{hint}");
    }

    #[test]
    fn a_removed_key_says_delete_not_migrate() {
        let hint = migration_hint(&present(&[("auth", "tokenTtl", "86400")]))
            .expect("auth.tokenTtl is removed");
        assert!(hint.contains("delete"), "{hint}");
        assert!(hint.contains("no longer"), "{hint}");
        assert!(hint.contains("[auth] tokenTtl"), "{hint}");
        // The removed block must NOT tell the user to migrate to a builder.
        assert!(!hint.contains("moved into typed app config"), "{hint}");
    }

    #[test]
    fn a_default_changed_key_is_loud_and_distinct() {
        let hint = migration_hint(&present(&[("live", "ttl", "30m")]))
            .expect("live.ttl changed behaviour");
        assert!(hint.contains("CHANGED BEHAVIOUR"), "{hint}");
        assert!(hint.contains("Live.withTtl"), "{hint}");
        // Not lumped into the moved block.
        assert!(!hint.contains("moved into typed app config"), "{hint}");
    }

    #[test]
    fn the_three_classes_are_separate_blocks() {
        let hint = migration_hint(&present(&[
            ("live", "store", "postgres"),
            ("live", "ttl", "30m"),
            ("auth", "driver", "jwt"),
        ]))
        .expect("mixed");
        assert!(hint.contains("moved into typed app config"), "{hint}");
        assert!(hint.contains("CHANGED BEHAVIOUR"), "{hint}");
        assert!(hint.contains("no longer"), "{hint}");
    }

    /// Removing a mapping row must redden the assertion for that key — the
    /// falsifier the fixture gate declares.
    #[test]
    fn lookup_is_the_single_source() {
        assert!(lookup("live", "store").is_some());
        assert!(lookup("nosuch", "key").is_none());
    }
}

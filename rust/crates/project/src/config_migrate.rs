//! `config_migrate.rs` — `sky config migrate`: rewrite a legacy `sky.toml`'s
//! runtime keys into a typed `config` binding (+ `Live.withX` pipeline),
//! provably behaviour-preserving.
//!
//! # Why this reuses, and never re-copies, the migration table
//!
//! The set of "which legacy `sky.toml` key moves to which builder" lives in ONE
//! place, [`crate::config_migration::MIGRATIONS`] (design §8.1). The build-time
//! hint and the runtime startup list already derive from it; so does this
//! rewriter. Every decision here — is a key legacy? which builder reproduces it?
//! which env target does it resolve through? — is a `config_migration::lookup`,
//! never a second table.
//!
//! # The two destinations
//!
//! A migrated setting lands in one of two places, because the runtime reads them
//! through two different value shapes:
//!
//!   * **Cross-cutting** settings (`[log]`, `[database]`, session store, `[jobs]`,
//!     `[security] csrf`) → a top-level `config` binding built on
//!     `Sky.Config.default |> Config.withX …`. The compiler applies it via
//!     `rt.ApplyConfig` at the top of `main`, writing the same env suffix the
//!     legacy `sky.toml` key seeded.
//!   * **App-shape** `[live]` settings (`port`/`static`/`ttl`/`input`/
//!     `maxBodyBytes`) → the `Live.config( … )` pipeline in `main`, as
//!     `|> Live.withX …`. These resolve through `rt.configLayers` from the
//!     `AppConfig` map, not through an env write.
//!
//! Both resolve through the ONE precedence rule (`operator env > withX > seeded
//! default > fallback`), so moving a value from the seeded-default layer
//! (`sky.toml`) to the builder layer (`withX`) preserves the resolved value:
//! only the layer changes, and the builder layer still beats a bare default.
//!
//! # The self-proving oracle
//!
//! [`plan`] computes the effective config the OLD `sky.toml` produces and the
//! effective config the NEW (rewritten `sky.toml` + generated builders) produces
//! — each as a `target -> value` map keyed by the runtime env target
//! `MIGRATIONS` names — and refuses to proceed unless they are IDENTICAL. The
//! NEW side is recomputed by RE-PARSING the generated builder call texts, not by
//! trusting the writer's in-memory state, so a writer bug (wrong value, wrong
//! builder, a dropped key with no reproducing call) is caught before anything is
//! written. A legacy key with no `MIGRATIONS` row is an "undeclared move" and is
//! a hard error — the tool never silently drops a key it cannot account for.

use std::collections::{BTreeMap, BTreeSet};
use std::path::{Path, PathBuf};

use crate::config_migration::{self, MigrationKind};

/// What the caller wants done.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Mode {
    /// Report whether any legacy key is present; write nothing.
    Check,
    /// Compute + show the rewrite; write nothing.
    DryRun,
    /// Compute + verify + write the rewrite.
    Apply,
}

/// A migratable key found in the project's `sky.toml`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Legacy {
    pub section: String,
    pub key: String,
    pub value: String,
    pub kind: MigrationKind,
    /// The runtime env target this resolves through (`MIGRATIONS` `env`).
    pub env: &'static str,
    /// The `withX` replacement, human form, for the report.
    pub builder: &'static str,
}

/// The full plan: the current + rewritten text of both files, the legacy keys,
/// and a human diff. Produced by [`plan`]; consumed by [`run`].
#[derive(Debug, Clone)]
pub struct MigratePlan {
    pub project_dir: PathBuf,
    pub entry_path: PathBuf,
    pub sky_toml_old: String,
    pub sky_toml_new: String,
    pub main_old: String,
    pub main_new: String,
    pub legacy: Vec<Legacy>,
    pub toml_changed: bool,
    pub main_changed: bool,
}

/// The outcome of [`run`], for the CLI to print + choose an exit code from.
#[derive(Debug, Clone)]
pub struct Outcome {
    /// How many legacy keys were present.
    pub legacy_count: usize,
    /// True when no legacy key was present (the `--check` "clean" verdict).
    pub clean: bool,
    /// The unified-ish diff (`--dry-run`).
    pub diff: String,
    /// True when files were written (`Apply`).
    pub wrote: bool,
    /// One human line per migrated key, for the apply/dry-run summary.
    pub summary: Vec<String>,
}

/// Why a migration could not be performed.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum MigrateError {
    /// No `sky.toml` in the project directory.
    NoSkyToml(PathBuf),
    /// The entry `.sky` file named by `entry` does not exist.
    NoEntry(PathBuf),
    /// A key the tool tried to migrate has no `MIGRATIONS` row — the undeclared
    /// move. The tool refuses to drop a key it cannot account for.
    Undeclared { section: String, key: String },
    /// A value shape no builder can represent without losing information (e.g.
    /// a `[live] store = "postgres"` with an explicit non-empty store DSN, which
    /// `Sessions` cannot carry). Refused rather than mis-migrated.
    Unsupported { detail: String },
    /// The project has app-shape `[live]` keys to migrate but no `Live.config( …`
    /// pipeline could be located in the entry module to attach them to.
    NoLiveConfig,
    /// The oracle found the OLD and NEW effective configs differ — the rewrite
    /// would change behaviour. Never written.
    OracleMismatch { detail: String },
}

impl std::fmt::Display for MigrateError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            MigrateError::NoSkyToml(p) => write!(f, "no sky.toml at {}", p.display()),
            MigrateError::NoEntry(p) => write!(
                f,
                "entry module {} does not exist (checked `entry` in sky.toml, then src/Main.sky)",
                p.display()
            ),
            MigrateError::Undeclared { section, key } => write!(
                f,
                "`[{section}] {key}` is a runtime key with no migration mapping — refusing to \
                 drop it. Add a row to project::config_migration::MIGRATIONS, or leave the key."
            ),
            MigrateError::Unsupported { detail } => write!(f, "cannot migrate: {detail}"),
            MigrateError::NoLiveConfig => write!(
                f,
                "the project has `[live]` app-shape keys to migrate (port/static/ttl/input/\
                 maxBodyBytes) but no `Live.config( … )` call was found in the entry module to \
                 attach the `Live.withX` builders to. Migrate these by hand, or check the entry."
            ),
            MigrateError::OracleMismatch { detail } => write!(
                f,
                "SELF-CHECK FAILED — the migration would change the effective config and was NOT \
                 written: {detail}"
            ),
        }
    }
}

// ─────────────────────────────────────────────────────────────────────────────
// Entry points
// ─────────────────────────────────────────────────────────────────────────────

/// Run the migration in `mode` against the project rooted at `project_dir`.
pub fn run(project_dir: &Path, mode: Mode) -> Result<Outcome, MigrateError> {
    let plan = plan(project_dir)?;
    let legacy_count = plan.legacy.len();
    let clean = legacy_count == 0;
    let summary: Vec<String> = plan
        .legacy
        .iter()
        .map(|l| {
            let val = if l.value.is_empty() {
                format!("[{}] {}", l.section, l.key)
            } else {
                format!("[{}] {} = \"{}\"", l.section, l.key, l.value)
            };
            match l.kind {
                MigrationKind::Removed => format!("  {val}  ->  removed (inert; no code needed)"),
                _ => format!("  {val}  ->  {}", l.builder),
            }
        })
        .collect();

    match mode {
        Mode::Check => Ok(Outcome {
            legacy_count,
            clean,
            diff: String::new(),
            wrote: false,
            summary,
        }),
        Mode::DryRun => Ok(Outcome {
            legacy_count,
            clean,
            diff: diff_text(&plan),
            wrote: false,
            summary,
        }),
        Mode::Apply => {
            let mut wrote = false;
            if plan.toml_changed {
                std::fs::write(project_dir.join("sky.toml"), &plan.sky_toml_new)
                    .map_err(|e| MigrateError::Unsupported {
                        detail: format!("cannot write sky.toml: {e}"),
                    })?;
                wrote = true;
            }
            if plan.main_changed {
                std::fs::write(&plan.entry_path, &plan.main_new).map_err(|e| {
                    MigrateError::Unsupported {
                        detail: format!("cannot write {}: {e}", plan.entry_path.display()),
                    }
                })?;
                wrote = true;
            }
            Ok(Outcome {
                legacy_count,
                clean,
                diff: diff_text(&plan),
                wrote,
                summary,
            })
        }
    }
}

/// Compute the full plan without touching disk. Runs the self-check oracle.
pub fn plan(project_dir: &Path) -> Result<MigratePlan, MigrateError> {
    let sky_toml_path = project_dir.join("sky.toml");
    let sky_toml_old = std::fs::read_to_string(&sky_toml_path)
        .map_err(|_| MigrateError::NoSkyToml(sky_toml_path.clone()))?;

    let entry_rel = entry_from_sky_toml(&sky_toml_old).unwrap_or_else(|| "src/Main.sky".to_string());
    let entry_path = project_dir.join(&entry_rel);

    // The recognised runtime-config keys present, from the ONE parser.
    let present = crate::build::read_sky_toml_config(&sky_toml_path).present_runtime_config_keys;

    // Keep only keys the migration table speaks to; a present key with no row is
    // a residual sky.toml-only setting (pool knobs, embedded, driver, env
    // prefix, analytics) and is left exactly where it is.
    let mut legacy: Vec<Legacy> = Vec::new();
    for (section, key, value) in &present {
        if let Some(entry) = config_migration::lookup(section, key) {
            legacy.push(Legacy {
                section: section.clone(),
                key: key.clone(),
                value: value.clone(),
                kind: entry.kind,
                env: entry.env,
                builder: entry.builder,
            });
        }
    }

    // No legacy keys → nothing to do; the files are unchanged and the plan is
    // empty. `--check` reads `legacy.is_empty()` from this.
    if legacy.is_empty() {
        return Ok(MigratePlan {
            project_dir: project_dir.to_path_buf(),
            entry_path,
            sky_toml_old: sky_toml_old.clone(),
            sky_toml_new: sky_toml_old,
            main_old: String::new(),
            main_new: String::new(),
            legacy,
            toml_changed: false,
            main_changed: false,
        });
    }

    let main_old = std::fs::read_to_string(&entry_path)
        .map_err(|_| MigrateError::NoEntry(entry_path.clone()))?;

    // Generate the builder calls + the env map they PRODUCE (recomputed by the
    // generators, the NEW side of the oracle). Live builders are qualified with
    // the entry module's own Std.Live alias.
    let alias = live_alias(&main_old);
    let gen = generate(&legacy, &alias)?;

    // The OLD side of the oracle: the effective config the current sky.toml
    // produces, keyed by the same env targets.
    let intended = intended_env(&legacy);
    if let Some(detail) = oracle_mismatch(&intended, &gen.produced) {
        return Err(MigrateError::OracleMismatch { detail });
    }

    // Rewrite sky.toml: remove every migratable key line; drop an emptied
    // runtime section header.
    let removals: BTreeSet<(String, String)> =
        legacy.iter().map(|l| (l.section.clone(), l.key.clone())).collect();
    // Defence in depth: every removal must be a declared move.
    verify_removals(&removals.iter().cloned().collect::<Vec<_>>())?;
    let sky_toml_new = rewrite_sky_toml(&sky_toml_old, &removals);

    // Rewrite Main.sky: the config binding (create/extend) + the Live pipeline.
    let main_new = rewrite_main(&main_old, &gen)?;

    // Post-check: the rewritten sky.toml must re-parse with ZERO migratable keys.
    let residual = migratable_keys_in(&sky_toml_new);
    if !residual.is_empty() {
        return Err(MigrateError::OracleMismatch {
            detail: format!(
                "the rewritten sky.toml still carries migratable key(s): {}",
                residual
                    .iter()
                    .map(|(s, k)| format!("[{s}] {k}"))
                    .collect::<Vec<_>>()
                    .join(", ")
            ),
        });
    }

    let toml_changed = sky_toml_new != sky_toml_old;
    let main_changed = main_new != main_old;

    Ok(MigratePlan {
        project_dir: project_dir.to_path_buf(),
        entry_path,
        sky_toml_old,
        sky_toml_new,
        main_old,
        main_new,
        legacy,
        toml_changed,
        main_changed,
    })
}

// ─────────────────────────────────────────────────────────────────────────────
// Generation
// ─────────────────────────────────────────────────────────────────────────────

/// The generated builder calls + the env map they produce.
#[derive(Debug, Default, Clone)]
pub struct Generated {
    /// `|> Config.withX …` lines (no leading pipe indentation — the writer
    /// indents them), for the top-level `config` binding.
    pub config_calls: Vec<String>,
    /// `|> Live.withX …` lines, for the `Live.config(…)` pipeline.
    pub live_calls: Vec<String>,
    /// The `Sky.Config` constructor groups the calls reference, for the import's
    /// `exposing (…)` list (e.g. `LogFormat(..)`).
    pub config_ctor_groups: BTreeSet<&'static str>,
    /// `env target -> value` the generated calls resolve through — the NEW side
    /// of the oracle, recomputed here rather than trusted from the plan.
    pub produced: BTreeMap<String, String>,
}

/// Generate the builder calls for the legacy keys, grouped so multi-key builders
/// (`withLog`, `withSessions`, `withJobs`) emit ONE call.
///
/// `live_alias` is how the entry module refers to `Std.Live` — its `as` alias
/// (`Live.withPort`) or `Std.Live` when there is none (a fully-qualified name is
/// always valid, and `Live.` is NOT in scope without an `as Live`). The
/// cross-cutting builders always use `Config.`, because this tool adds
/// `import Sky.Config as Config` itself.
pub fn generate(legacy: &[Legacy], live_alias: &str) -> Result<Generated, MigrateError> {
    let mut by: BTreeMap<(&str, &str), &str> = BTreeMap::new();
    for l in legacy {
        by.insert((l.section.as_str(), l.key.as_str()), l.value.as_str());
    }
    let get = |s: &str, k: &str| by.get(&(s, k)).copied();

    let mut g = Generated::default();

    // ── [log] → Config.withLog Format Level (one call, defaults fill an absent
    //    half; Text/Info ARE the runtime defaults — rt.go:1207/1229 — so the fill
    //    is behaviour-preserving).
    if get("log", "format").is_some() || get("log", "level").is_some() {
        let (fmt_ctor, fmt_env) = match get("log", "format") {
            Some("json") => ("Json", "json"),
            Some("text") | None => ("Text", "text"),
            Some(other) => {
                return Err(MigrateError::Unsupported {
                    detail: format!("[log] format = \"{other}\" is not json|text"),
                })
            }
        };
        let (lvl_ctor, lvl_env) = match get("log", "level") {
            Some("debug") => ("Debug", "debug"),
            Some("info") | None => ("Info", "info"),
            Some("warn") => ("Warn", "warn"),
            Some("error") => ("Error", "error"),
            Some(other) => {
                return Err(MigrateError::Unsupported {
                    detail: format!("[log] level = \"{other}\" is not debug|info|warn|error"),
                })
            }
        };
        g.config_calls.push(format!("|> Config.withLog {fmt_ctor} {lvl_ctor}"));
        g.config_ctor_groups.insert("LogFormat(..)");
        g.config_ctor_groups.insert("LogLevel(..)");
        g.produced.insert("LOG_FORMAT".into(), fmt_env.into());
        g.produced.insert("LOG_LEVEL".into(), lvl_env.into());
    }

    // ── [database] path → Sqlite, url → Postgres.
    if get("database", "path").is_some() && get("database", "url").is_some() {
        return Err(MigrateError::Unsupported {
            detail: "[database] has BOTH path and url — pick one before migrating".into(),
        });
    }
    if let Some(p) = get("database", "path") {
        g.config_calls.push(format!("|> Config.withDatabase (Sqlite {})", quote(p)));
        g.config_ctor_groups.insert("Database(..)");
        g.produced.insert("DB_PATH".into(), p.into());
    }
    if let Some(u) = get("database", "url") {
        g.config_calls.push(format!("|> Config.withDatabase (Postgres {})", quote(u)));
        g.config_ctor_groups.insert("Database(..)");
        g.produced.insert("DATABASE_URL".into(), u.into());
    }

    // ── session store: [live] store (+ storePath) → Config.withSessions.
    if get("live", "store").is_some() || get("live", "storePath").is_some() {
        let store = get("live", "store").ok_or_else(|| MigrateError::Unsupported {
            detail: "[live] storePath without [live] store — cannot pick a Sessions constructor"
                .into(),
        })?;
        let path = get("live", "storePath").unwrap_or("");
        let ctor = sessions_ctor(store, path)?;
        g.config_calls.push(format!("|> Config.withSessions {ctor}"));
        g.config_ctor_groups.insert("Sessions(..)");
        g.produced.insert("LIVE_STORE".into(), store_kind_env(store).into());
        if !path.is_empty() {
            g.produced.insert("LIVE_STORE_PATH".into(), path.into());
        }
    }

    // ── [jobs] store (+ storePath/store_path) → Config.withJobs.
    if get("jobs", "store").is_some()
        || get("jobs", "storePath").is_some()
        || get("jobs", "store_path").is_some()
    {
        let store = get("jobs", "store").ok_or_else(|| MigrateError::Unsupported {
            detail: "[jobs] store path without [jobs] store — cannot pick a JobStore constructor"
                .into(),
        })?;
        let path = get("jobs", "storePath").or_else(|| get("jobs", "store_path")).unwrap_or("");
        let ctor = jobs_ctor(store, path)?;
        g.config_calls.push(format!("|> Config.withJobs {ctor}"));
        g.config_ctor_groups.insert("JobStore(..)");
        g.produced.insert("JOBS_STORE".into(), store_kind_env(store).into());
        if !path.is_empty() {
            g.produced.insert("JOBS_STORE_PATH".into(), path.into());
        }
    }

    // ── [security] csrf → Config.withCsrf Bool.
    if let Some(v) = get("security", "csrf") {
        let on = !matches!(v.to_ascii_lowercase().as_str(), "false" | "off" | "0");
        g.config_calls.push(format!("|> Config.withCsrf {}", if on { "True" } else { "False" }));
        g.produced.insert("CSRF".into(), if on { "on" } else { "off" }.into());
    }

    // ── app-shape [live] builders → <alias>.withX (into the Live.config pipeline).
    let a = live_alias;
    if let Some(v) = get("live", "port") {
        g.live_calls.push(format!("|> {a}.withPort {}", int_arg(v)?));
        g.produced.insert("LIVE_PORT".into(), v.into());
    }
    if let Some(v) = get("live", "static") {
        g.live_calls.push(format!("|> {a}.withStatic {}", quote(v)));
        g.produced.insert("LIVE_STATIC_DIR".into(), v.into());
    }
    if let Some(v) = get("live", "ttl") {
        g.live_calls.push(format!("|> {a}.withTtl {}", quote(v)));
        g.produced.insert("LIVE_TTL".into(), v.into());
    }
    if let Some(v) = get("live", "input") {
        g.live_calls.push(format!("|> {a}.withInput {}", quote(v)));
        g.produced.insert("LIVE_INPUT_MODE".into(), v.into());
    }
    if let Some(v) = get("live", "maxBodyBytes") {
        g.live_calls.push(format!("|> {a}.withMaxBodyBytes {}", int_arg(v)?));
        g.produced.insert("LIVE_MAX_BODY_BYTES".into(), v.into());
    }

    Ok(g)
}

fn sessions_ctor(store: &str, path: &str) -> Result<String, MigrateError> {
    match store {
        "memory" => Ok("Memory".into()),
        "sqlite" => Ok(format!("(SessionsSqlite {})", quote(path))),
        "postgres" if path.is_empty() => Ok("SharedWithDatabase".into()),
        "postgres" => Err(MigrateError::Unsupported {
            detail: "[live] store = \"postgres\" with an explicit storePath cannot be a typed \
                     Sessions value; use SKY_LIVE_STORE_PATH in the environment, or Live.withStore"
                .into(),
        }),
        "redis" => Ok(format!("(Redis {})", quote(path))),
        other => Err(MigrateError::Unsupported {
            detail: format!("[live] store = \"{other}\" is not memory|sqlite|postgres|redis"),
        }),
    }
}

fn jobs_ctor(store: &str, path: &str) -> Result<String, MigrateError> {
    match store {
        "memory" => Ok("JobsMemory".into()),
        "sqlite" => Ok(format!("(JobsSqlite {})", quote(path))),
        "postgres" if path.is_empty() => Ok("JobsSharedWithDatabase".into()),
        "postgres" => Err(MigrateError::Unsupported {
            detail: "[jobs] store = \"postgres\" with an explicit store path cannot be a typed \
                     JobStore value".into(),
        }),
        other => Err(MigrateError::Unsupported {
            detail: format!("[jobs] store = \"{other}\" is not memory|sqlite|postgres"),
        }),
    }
}

/// The env value the runtime resolves a session/jobs store KIND to (the `case`
/// in Sky/Config.sky: memory/sqlite/postgres/redis are already env strings).
fn store_kind_env(store: &str) -> &str {
    store
}

fn quote(s: &str) -> String {
    format!("\"{}\"", s.replace('\\', "\\\\").replace('"', "\\\""))
}

fn int_arg(v: &str) -> Result<String, MigrateError> {
    // A bare integer is emitted unquoted (withPort/withMaxBodyBytes take Int).
    if v.trim().parse::<i64>().is_ok() {
        Ok(v.trim().to_string())
    } else {
        Err(MigrateError::Unsupported {
            detail: format!("expected an integer, got \"{v}\""),
        })
    }
}

// ─────────────────────────────────────────────────────────────────────────────
// The oracle
// ─────────────────────────────────────────────────────────────────────────────

/// The effective config the OLD sky.toml produces, keyed by env target.
///
/// Moved / DefaultChanged keys contribute `env -> value`; Removed keys (the
/// inert `[auth]` block) contribute nothing, because they seed nothing. The
/// `[log]` pair is completed with the runtime defaults so a project that set
/// only one of format/level matches the generated two-arg `withLog`.
pub fn intended_env(legacy: &[Legacy]) -> BTreeMap<String, String> {
    let mut m = BTreeMap::new();
    let mut touched_log = false;
    for l in legacy {
        if l.kind == MigrationKind::Removed || l.value.is_empty() {
            continue;
        }
        m.insert(l.env.to_string(), l.value.clone());
        if l.section == "log" {
            touched_log = true;
        }
    }
    if touched_log {
        m.entry("LOG_FORMAT".into()).or_insert_with(|| "text".into());
        m.entry("LOG_LEVEL".into()).or_insert_with(|| "info".into());
    }
    normalize(&mut m);
    m
}

/// Some targets compare by NORMALIZED semantics, not raw string: `CSRF` reads
/// `off`/`false`/`0` all as disabled, so `[security] csrf = "false"` and the
/// generated `withCsrf False` (which writes `off`) are the same setting.
fn normalize(m: &mut BTreeMap<String, String>) {
    if let Some(v) = m.get_mut("CSRF") {
        let on = !matches!(v.to_ascii_lowercase().as_str(), "false" | "off" | "0");
        *v = if on { "on".into() } else { "off".into() };
    }
}

/// `None` when the two effective maps agree; a human-readable divergence
/// otherwise. Both sides are normalized first.
fn oracle_mismatch(
    intended: &BTreeMap<String, String>,
    produced: &BTreeMap<String, String>,
) -> Option<String> {
    let mut a = intended.clone();
    let mut b = produced.clone();
    normalize(&mut a);
    normalize(&mut b);
    if a == b {
        return None;
    }
    let mut diffs = Vec::new();
    let keys: BTreeSet<&String> = a.keys().chain(b.keys()).collect();
    for k in keys {
        match (a.get(k), b.get(k)) {
            (Some(x), Some(y)) if x != y => {
                diffs.push(format!("{k}: sky.toml has {x:?}, the generated config produces {y:?}"))
            }
            (Some(x), None) => diffs.push(format!(
                "{k}: sky.toml has {x:?} but the generated config produces nothing (a dropped key)"
            )),
            (None, Some(y)) => diffs.push(format!(
                "{k}: the generated config produces {y:?} but sky.toml had nothing (an invented \
                 setting)"
            )),
            _ => {}
        }
    }
    Some(diffs.join("; "))
}

/// Every `(section, key)` in `removals` must name a declared move. An undeclared
/// removal is refused — the tool never drops a key it cannot account for.
pub fn verify_removals(removals: &[(String, String)]) -> Result<(), MigrateError> {
    for (section, key) in removals {
        if config_migration::lookup(section, key).is_none() {
            return Err(MigrateError::Undeclared {
                section: section.clone(),
                key: key.clone(),
            });
        }
    }
    Ok(())
}

// ─────────────────────────────────────────────────────────────────────────────
// sky.toml rewrite
// ─────────────────────────────────────────────────────────────────────────────

/// The recognised runtime-config keys the migration table names, present in a
/// `sky.toml` TEXT (used for the post-rewrite "zero migratable keys" check).
fn migratable_keys_in(text: &str) -> Vec<(String, String)> {
    let mut out = Vec::new();
    let mut section = String::new();
    for raw in text.lines() {
        let line = raw.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        if line.starts_with('[') {
            if let Some(end) = line.find(']') {
                section = line[1..end].trim().trim_matches('"').to_string();
                continue;
            }
        }
        if let Some((k, _)) = line.split_once('=') {
            let key = k.trim();
            if config_migration::lookup(&section, key).is_some() {
                out.push((section.clone(), key.to_string()));
            }
        }
    }
    out
}

/// Remove each migrated key's line; drop a runtime-section header left with no
/// key lines. Every other line (comments, residual keys, blank lines) is
/// preserved byte-for-byte.
fn rewrite_sky_toml(text: &str, removals: &BTreeSet<(String, String)>) -> String {
    // Pass 1: model the file as a header line + its body lines, so a whole
    // emptied runtime section can be dropped as a unit.
    #[derive(Default)]
    struct Section {
        header: Option<String>, // None for the leading (bare) section
        name: String,
        body: Vec<String>, // every non-header line, in order
    }
    let mut sections: Vec<Section> = vec![Section::default()];
    for raw in text.lines() {
        let trimmed = raw.trim();
        if trimmed.starts_with('[') && trimmed.contains(']') {
            let name = trimmed[1..trimmed.find(']').unwrap()]
                .trim()
                .trim_matches('"')
                .to_string();
            sections.push(Section {
                header: Some(raw.to_string()),
                name,
                body: Vec::new(),
            });
        } else {
            sections.last_mut().unwrap().body.push(raw.to_string());
        }
    }

    // Which sections we may drop wholesale when emptied.
    let droppable = |name: &str| {
        matches!(name, "live" | "log" | "jobs" | "security" | "auth")
    };

    let mut out: Vec<String> = Vec::new();
    for sec in &sections {
        // Filter the body: drop a `key = …` line whose (section, key) is removed.
        let kept_body: Vec<String> = sec
            .body
            .iter()
            .filter(|raw| {
                let t = raw.trim();
                if t.starts_with('#') || t.is_empty() {
                    return true;
                }
                if let Some((k, _)) = t.split_once('=') {
                    return !removals.contains(&(sec.name.clone(), k.trim().to_string()));
                }
                true
            })
            .cloned()
            .collect();

        let has_key = kept_body.iter().any(|raw| {
            let t = raw.trim();
            !t.is_empty() && !t.starts_with('#') && t.contains('=')
        });

        // Drop an emptied droppable runtime section header + its now-orphaned
        // comment/blank body. Non-droppable sections (e.g. [database] with a
        // residual key) keep their header and surviving lines.
        if sec.header.is_some() && droppable(&sec.name) && !has_key {
            continue;
        }
        if let Some(h) = &sec.header {
            out.push(h.clone());
        }
        out.extend(kept_body);
    }

    // Collapse a run of >1 trailing blank lines the drops may have created,
    // preserving a single trailing newline.
    let mut joined = out.join("\n");
    while joined.ends_with("\n\n\n") {
        joined.pop();
    }
    if text.ends_with('\n') && !joined.ends_with('\n') {
        joined.push('\n');
    }
    joined
}

// ─────────────────────────────────────────────────────────────────────────────
// Main.sky rewrite
// ─────────────────────────────────────────────────────────────────────────────

/// Insert the Live builders into the `Live.config(…)` pipeline and the
/// Sky.Config builders into a top-level `config` binding (created or extended).
fn rewrite_main(main: &str, gen: &Generated) -> Result<String, MigrateError> {
    let mut text = main.to_string();

    // (1) Live builders → the Live.config(…) pipeline in main.
    if !gen.live_calls.is_empty() {
        text = insert_live_calls(&text, &gen.live_calls)?;
    }

    // (2) Sky.Config builders → a top-level `config` binding.
    if !gen.config_calls.is_empty() {
        text = upsert_config_binding(&text, gen)?;
    }

    Ok(text)
}

/// How the entry module refers to `Std.Live` for QUALIFYING builder calls:
/// its `as X` alias when present, else `Std.Live` (a fully-qualified name is
/// always valid; a bare `Live.` is not in scope without an `as Live`).
pub fn live_alias(text: &str) -> String {
    for line in text.lines() {
        let t = line.trim();
        if let Some(rest) = t.strip_prefix("import Std.Live as ") {
            let alias: String = rest
                .chars()
                .take_while(|c| c.is_alphanumeric() || *c == '_')
                .collect();
            if !alias.is_empty() {
                return alias;
            }
        }
    }
    "Std.Live".to_string()
}

/// Whether `import Std.Live … exposing (…)` brings the given name unqualified
/// into scope (so `config { … }` may be the bare Std.Live builder).
fn std_live_exposes(text: &str, name: &str) -> bool {
    for line in text.lines() {
        let t = line.trim_start();
        if t.starts_with("import Std.Live") {
            if let Some(rest) = t.split_once("exposing").map(|(_, r)| r) {
                if rest.contains("(..)") {
                    return true;
                }
                if let (Some(o), Some(c)) = (rest.find('('), rest.find(')')) {
                    if rest[o + 1..c].split(',').any(|p| p.trim() == name) {
                        return true;
                    }
                }
            }
        }
    }
    false
}

/// The byte index just AFTER the Std.Live config-builder call token — the
/// `<alias>.config`, `Std.Live.config`, or (when exposed) a bare `config` that
/// is immediately followed by a `{` record literal. `None` if none is found.
fn find_config_call(text: &str) -> Option<usize> {
    let alias = live_alias(text);
    let mut tokens = vec![format!("{alias}.config"), "Std.Live.config".to_string()];
    if std_live_exposes(text, "config") {
        tokens.push("config".to_string());
    }
    let bytes = text.as_bytes();
    let is_ident = |b: u8| b.is_ascii_alphanumeric() || b == b'_' || b == b'.';
    for tok in &tokens {
        for (idx, _) in text.match_indices(tok.as_str()) {
            // Whole-token boundary (not the tail of `myconfig` / `Live.configX`).
            if idx > 0 && is_ident(bytes[idx - 1]) {
                continue;
            }
            let after = idx + tok.len();
            if bytes.get(after).copied().map(is_ident).unwrap_or(false) {
                continue;
            }
            // The next non-space, non-newline char must open a record.
            let rest = text[after..].trim_start();
            if rest.starts_with('{') {
                return Some(after);
            }
        }
    }
    None
}

/// Find the Std.Live config-builder call's record, balance-scan to its closing
/// `}`, and splice the `|> <alias>.withX` lines in right after it, indented one
/// pipe level deeper than the call.
fn insert_live_calls(text: &str, calls: &[String]) -> Result<String, MigrateError> {
    let after = find_config_call(text).ok_or(MigrateError::NoLiveConfig)?;
    // The token starts before `after`; find its line for indentation.
    let pos = after;
    // The `{` opening the config record, after the call name.
    let brace_rel = text[after..].find('{').ok_or(MigrateError::NoLiveConfig)?;
    let brace = after + brace_rel;
    let close = match_brace(text, brace).ok_or(MigrateError::NoLiveConfig)?;

    // Indentation: the column of `<alias>.config` + 4, so the pipes sit under it.
    let line_start = text[..pos].rfind('\n').map(|n| n + 1).unwrap_or(0);
    let base_indent: String = text[line_start..pos]
        .chars()
        .take_while(|c| *c == ' ')
        .collect();
    let pipe_indent = format!("{base_indent}    ");

    let mut insert = String::new();
    for c in calls {
        insert.push('\n');
        insert.push_str(&pipe_indent);
        insert.push_str(c);
    }

    let mut out = String::with_capacity(text.len() + insert.len());
    out.push_str(&text[..=close]);
    out.push_str(&insert);
    out.push_str(&text[close + 1..]);
    Ok(out)
}

/// The index of the `}` matching the `{` at `open`.
fn match_brace(text: &str, open: usize) -> Option<usize> {
    let bytes = text.as_bytes();
    if bytes.get(open) != Some(&b'{') {
        return None;
    }
    let mut depth = 0usize;
    for (i, b) in bytes.iter().enumerate().skip(open) {
        match b {
            b'{' => depth += 1,
            b'}' => {
                depth -= 1;
                if depth == 0 {
                    return Some(i);
                }
            }
            _ => {}
        }
    }
    None
}

/// Create or extend the top-level `config` binding with the `Config.withX` calls.
fn upsert_config_binding(text: &str, gen: &Generated) -> Result<String, MigrateError> {
    if let Some(new) = extend_config_binding(text, &gen.config_calls) {
        return Ok(ensure_config_import(&new, gen));
    }
    // Create. A new top-level `config` binding would clash with a `config`
    // brought in unqualified from Std.Live — refuse rather than emit a shadowed
    // name (the app would then need to alias one of them).
    if std_live_exposes(text, "config") {
        return Err(MigrateError::Unsupported {
            detail: "the entry module imports `config` unqualified from Std.Live, so a new \
                     top-level `config` binding would clash. Alias the Std.Live import \
                     (`import Std.Live as Live`) and re-run, or add the Sky.Config `config` \
                     binding by hand."
                .into(),
        });
    }
    let with_import = ensure_config_import(text, gen);
    let with_exposed = expose_config(&with_import);
    Ok(append_config_binding(&with_exposed, &gen.config_calls))
}

/// If a top-level `config =` binding exists, append the calls to its pipeline
/// and return the new text; else `None`.
fn extend_config_binding(text: &str, calls: &[String]) -> Option<String> {
    let lines: Vec<&str> = text.lines().collect();
    let start = lines.iter().position(|l| {
        let t = l.trim_start();
        (t.starts_with("config ") || t.starts_with("config="))
            && l.chars().take_while(|c| *c == ' ').count() == 0
            && (t.contains('=') || t.starts_with("config :"))
            && !t.starts_with("config :")
    })?;
    // The binding body runs to the first later line at column 0 that is not
    // blank (the next top-level declaration), or EOF.
    let mut end = start + 1;
    while end < lines.len() {
        let l = lines[end];
        if !l.is_empty() && !l.starts_with(char::is_whitespace) {
            break;
        }
        end += 1;
    }
    // Insert the calls just before `end`, after trimming trailing blank body
    // lines. Indent to an existing pipe, else 8 spaces.
    let mut insert_at = end;
    while insert_at > start + 1 && lines[insert_at - 1].trim().is_empty() {
        insert_at -= 1;
    }
    let pipe_indent = lines[start + 1..insert_at]
        .iter()
        .find(|l| l.trim_start().starts_with("|>"))
        .map(|l| l.chars().take_while(|c| *c == ' ').collect::<String>())
        .unwrap_or_else(|| "        ".to_string());

    let mut out: Vec<String> = lines[..insert_at].iter().map(|s| s.to_string()).collect();
    for c in calls {
        out.push(format!("{pipe_indent}{c}"));
    }
    out.extend(lines[insert_at..].iter().map(|s| s.to_string()));
    let mut joined = out.join("\n");
    if text.ends_with('\n') {
        joined.push('\n');
    }
    Some(joined)
}

/// Ensure `import Sky.Config as Config exposing (…)` is present with every ctor
/// group the calls need. If an import exists, merge the ctor groups into it.
fn ensure_config_import(text: &str, gen: &Generated) -> String {
    let groups: Vec<&str> = gen.config_ctor_groups.iter().copied().collect();
    let exposing = if groups.is_empty() {
        String::new()
    } else {
        format!(" exposing ({})", groups.join(", "))
    };
    let import_line = format!("import Sky.Config as Config{exposing}");

    if text.lines().any(|l| l.trim_start().starts_with("import Sky.Config ")) {
        // Already imported — trust the existing line (a hand-written one may
        // expose more). Leave it; the ctors we need are a subset of the common
        // `exposing (..)` idioms, and re-writing a user's import risks churn.
        return text.to_string();
    }

    // Insert after the last `import ` line; else after the module header.
    let lines: Vec<&str> = text.lines().collect();
    let last_import = lines.iter().rposition(|l| l.trim_start().starts_with("import "));
    let insert_at = match last_import {
        Some(i) => i + 1,
        None => lines
            .iter()
            .position(|l| l.trim_start().starts_with("module "))
            .map(|i| i + 1)
            .unwrap_or(0),
    };
    let mut out: Vec<String> = lines[..insert_at].iter().map(|s| s.to_string()).collect();
    out.push(import_line);
    out.extend(lines[insert_at..].iter().map(|s| s.to_string()));
    let mut joined = out.join("\n");
    if text.ends_with('\n') {
        joined.push('\n');
    }
    joined
}

/// Add `config` to the entry module's `exposing (…)` list if absent.
fn expose_config(text: &str) -> String {
    let Some(mod_pos) = text.find("module ") else {
        return text.to_string();
    };
    let Some(exp_rel) = text[mod_pos..].find("exposing") else {
        return text.to_string();
    };
    let exp = mod_pos + exp_rel;
    let Some(open_rel) = text[exp..].find('(') else {
        return text.to_string();
    };
    let open = exp + open_rel;
    let Some(close_rel) = text[open..].find(')') else {
        return text.to_string();
    };
    let close = open + close_rel;
    let inner = &text[open + 1..close];
    // `exposing (..)` already exports everything, including config.
    if inner.contains("..") {
        return text.to_string();
    }
    if inner.split(',').any(|t| t.trim() == "config") {
        return text.to_string();
    }
    let trimmed = inner.trim_end();
    let sep = if trimmed.trim().is_empty() { "" } else { ", " };
    let mut out = String::with_capacity(text.len() + 8);
    out.push_str(&text[..open + 1]);
    out.push_str(trimmed);
    out.push_str(sep);
    out.push_str("config");
    out.push_str(&text[close..]);
    out
}

/// Append a fresh `config` binding at end of file.
fn append_config_binding(text: &str, calls: &[String]) -> String {
    let mut out = String::from(text);
    if !out.ends_with('\n') {
        out.push('\n');
    }
    out.push('\n');
    out.push_str("config : Config.Config\n");
    out.push_str("config =\n");
    out.push_str("    Config.default\n");
    for c in calls {
        out.push_str("        ");
        out.push_str(c);
        out.push('\n');
    }
    out
}

// ─────────────────────────────────────────────────────────────────────────────
// entry + diff helpers
// ─────────────────────────────────────────────────────────────────────────────

/// Read a bare `entry = "…"` from sky.toml text (own parser: `sky_toml_project_key`
/// rejects values containing `/`, which every entry path has).
fn entry_from_sky_toml(text: &str) -> Option<String> {
    let mut section = String::new();
    for raw in text.lines() {
        let line = raw.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        if line.starts_with('[') {
            if let Some(end) = line.find(']') {
                section = line[1..end].trim().trim_matches('"').to_string();
                continue;
            }
        }
        if section.is_empty() || section == "project" {
            if let Some((k, v)) = line.split_once('=') {
                if k.trim() == "entry" {
                    let v = v.trim();
                    let v = v.split('#').next().unwrap_or(v).trim();
                    return Some(v.trim_matches(['"', '\'']).to_string());
                }
            }
        }
    }
    None
}

/// A compact line diff of both files, for `--dry-run`.
pub fn diff_text(plan: &MigratePlan) -> String {
    let mut out = String::new();
    if plan.toml_changed {
        out.push_str("--- sky.toml ---\n");
        out.push_str(&line_diff(&plan.sky_toml_old, &plan.sky_toml_new));
        out.push('\n');
    }
    if plan.main_changed {
        out.push_str(&format!(
            "--- {} ---\n",
            plan.entry_path.file_name().and_then(|n| n.to_str()).unwrap_or("Main.sky")
        ));
        out.push_str(&line_diff(&plan.main_old, &plan.main_new));
    }
    if out.is_empty() {
        out.push_str("(no changes)\n");
    }
    out
}

/// A minimal LCS line diff: `-` for removed, `+` for added, ` ` for context
/// adjacent to a change (kept compact).
fn line_diff(old: &str, new: &str) -> String {
    let a: Vec<&str> = old.lines().collect();
    let b: Vec<&str> = new.lines().collect();
    // LCS table.
    let n = a.len();
    let m = b.len();
    let mut dp = vec![vec![0u32; m + 1]; n + 1];
    for i in (0..n).rev() {
        for j in (0..m).rev() {
            dp[i][j] = if a[i] == b[j] {
                dp[i + 1][j + 1] + 1
            } else {
                dp[i + 1][j].max(dp[i][j + 1])
            };
        }
    }
    let mut ops: Vec<(char, &str)> = Vec::new();
    let (mut i, mut j) = (0usize, 0usize);
    while i < n && j < m {
        if a[i] == b[j] {
            ops.push((' ', a[i]));
            i += 1;
            j += 1;
        } else if dp[i + 1][j] >= dp[i][j + 1] {
            ops.push(('-', a[i]));
            i += 1;
        } else {
            ops.push(('+', b[j]));
            j += 1;
        }
    }
    while i < n {
        ops.push(('-', a[i]));
        i += 1;
    }
    while j < m {
        ops.push(('+', b[j]));
        j += 1;
    }
    // Show only changed lines plus one line of surrounding context.
    let changed: Vec<bool> = ops.iter().map(|(c, _)| *c != ' ').collect();
    let mut show = vec![false; ops.len()];
    for (k, ch) in changed.iter().enumerate() {
        if *ch {
            show[k] = true;
            if k > 0 {
                show[k - 1] = true;
            }
            if k + 1 < ops.len() {
                show[k + 1] = true;
            }
        }
    }
    let mut out = String::new();
    let mut skipped = false;
    for (k, (c, line)) in ops.iter().enumerate() {
        if show[k] {
            out.push(*c);
            out.push(' ');
            out.push_str(line);
            out.push('\n');
            skipped = false;
        } else if !skipped {
            out.push_str("  …\n");
            skipped = true;
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    fn leg(section: &str, key: &str, value: &str) -> Legacy {
        let e = config_migration::lookup(section, key).expect("a migratable key");
        Legacy {
            section: section.into(),
            key: key.into(),
            value: value.into(),
            kind: e.kind,
            env: e.env,
            builder: e.builder,
        }
    }

    #[test]
    fn generates_the_expected_calls_and_env() {
        let legacy = vec![
            leg("log", "format", "json"),
            leg("log", "level", "warn"),
            leg("database", "path", "app.db"),
            leg("live", "store", "sqlite"),
            leg("live", "storePath", "sessions.db"),
            leg("security", "csrf", "false"),
            leg("live", "port", "8000"),
            leg("live", "input", "debounce"),
        ];
        let g = generate(&legacy, "Live").expect("generates");
        assert!(g.config_calls.iter().any(|c| c == "|> Config.withLog Json Warn"), "{:?}", g.config_calls);
        assert!(g.config_calls.iter().any(|c| c == "|> Config.withDatabase (Sqlite \"app.db\")"));
        assert!(g.config_calls.iter().any(|c| c == "|> Config.withSessions (SessionsSqlite \"sessions.db\")"));
        assert!(g.config_calls.iter().any(|c| c == "|> Config.withCsrf False"));
        assert!(g.live_calls.iter().any(|c| c == "|> Live.withPort 8000"));
        assert!(g.live_calls.iter().any(|c| c == "|> Live.withInput \"debounce\""));
        assert!(g.config_ctor_groups.contains("Sessions(..)"));
        assert_eq!(g.produced.get("LOG_FORMAT").map(String::as_str), Some("json"));
        assert_eq!(g.produced.get("LIVE_STORE_PATH").map(String::as_str), Some("sessions.db"));
        // CSRF false → produced "off".
        assert_eq!(g.produced.get("CSRF").map(String::as_str), Some("off"));
    }

    #[test]
    fn oracle_agrees_on_a_good_plan_and_normalizes_csrf() {
        let legacy = vec![
            leg("live", "store", "memory"),
            leg("security", "csrf", "false"),
            leg("log", "format", "json"),
        ];
        let g = generate(&legacy, "Live").unwrap();
        let intended = intended_env(&legacy);
        assert_eq!(
            oracle_mismatch(&intended, &g.produced),
            None,
            "intended={intended:?} produced={:?}",
            g.produced
        );
        // The [log] pair completed both sides with LOG_LEVEL=info.
        assert_eq!(intended.get("LOG_LEVEL").map(String::as_str), Some("info"));
    }

    #[test]
    fn oracle_catches_a_dropped_target() {
        let intended: BTreeMap<String, String> =
            [("LIVE_STORE".to_string(), "sqlite".to_string())].into_iter().collect();
        let produced: BTreeMap<String, String> = BTreeMap::new();
        let m = oracle_mismatch(&intended, &produced).expect("must flag a dropped target");
        assert!(m.contains("LIVE_STORE"), "{m}");
    }

    #[test]
    fn undeclared_removal_is_refused() {
        // A key with no MIGRATIONS row must not be droppable.
        let err = verify_removals(&[("database".into(), "maxOpenConns".into())]).unwrap_err();
        assert_eq!(
            err,
            MigrateError::Undeclared { section: "database".into(), key: "maxOpenConns".into() }
        );
        // A real move passes.
        verify_removals(&[("live".into(), "store".into())]).unwrap();
    }

    #[test]
    fn sky_toml_rewrite_drops_keys_and_empty_sections() {
        let src = "name = \"x\"\nentry = \"src/Main.sky\"\n\n\
                   [live]\nport = 8000\nstore = \"sqlite\"\nstorePath = \"s.db\"\n\n\
                   [database]\ndriver = \"sqlite\"\npath = \"app.db\"\n";
        let removals: BTreeSet<(String, String)> = [
            ("live".to_string(), "port".to_string()),
            ("live".to_string(), "store".to_string()),
            ("live".to_string(), "storePath".to_string()),
            ("database".to_string(), "path".to_string()),
        ]
        .into_iter()
        .collect();
        let out = rewrite_sky_toml(src, &removals);
        assert!(!out.contains("[live]"), "emptied [live] header must drop:\n{out}");
        assert!(!out.contains("port ="), "{out}");
        assert!(out.contains("[database]"), "[database] keeps its residual driver:\n{out}");
        assert!(out.contains("driver = \"sqlite\""), "{out}");
        assert!(!out.contains("path = \"app.db\""), "migrated path must drop:\n{out}");
        // Zero migratable keys remain.
        assert!(migratable_keys_in(&out).is_empty(), "{out}");
    }

    #[test]
    fn inserts_live_calls_after_the_config_record() {
        let main = "module Main exposing (main)\n\n\
                    import Std.Live as Live\n\n\
                    main =\n    Live.app\n        (Live.config { init = init, view = view })\n";
        let out = insert_live_calls(main, &["|> Live.withPort 8000".to_string()]).unwrap();
        assert!(out.contains("{ init = init, view = view }"), "record kept:\n{out}");
        assert!(out.contains("|> Live.withPort 8000"), "call inserted:\n{out}");
        // The pipe is spliced between the record `}` and the `)` closing the
        // `Live.app` argument: `Live.config {…} |> Live.withPort 8000)`.
        let brace = out.find("view = view }").unwrap();
        let call = out.find("Live.withPort").unwrap();
        let close_paren = out.rfind(')').unwrap();
        assert!(brace < call && call < close_paren, "order must be }} < |> < ):\n{out}");
    }

    #[test]
    fn creates_a_config_binding_with_import_and_export() {
        let main = "module Main exposing (main)\n\nimport Std.Log\n\nmain =\n    doStuff\n";
        let mut g = Generated::default();
        g.config_calls.push("|> Config.withLog Json Warn".into());
        g.config_ctor_groups.insert("LogFormat(..)");
        g.config_ctor_groups.insert("LogLevel(..)");
        let out = upsert_config_binding(main, &g).unwrap();
        assert!(out.contains("module Main exposing (main, config)"), "config exposed:\n{out}");
        assert!(out.contains("import Sky.Config as Config exposing (LogFormat(..), LogLevel(..))"), "{out}");
        assert!(out.contains("config : Config.Config"), "{out}");
        assert!(out.contains("    Config.default"), "{out}");
        assert!(out.contains("        |> Config.withLog Json Warn"), "{out}");
    }

    #[test]
    fn extends_an_existing_config_binding() {
        let main = "module Main exposing (main, config)\n\n\
                    import Sky.Config as Config exposing (Database(..))\n\n\
                    config : Config.Config\nconfig =\n    Config.default\n        |> Config.withDatabase (Sqlite \"a.db\")\n\n\
                    main =\n    run\n";
        let mut g = Generated::default();
        g.config_calls.push("|> Config.withCsrf False".into());
        let out = upsert_config_binding(main, &g).unwrap();
        assert!(out.contains("|> Config.withDatabase (Sqlite \"a.db\")"), "existing kept:\n{out}");
        assert!(out.contains("        |> Config.withCsrf False"), "appended:\n{out}");
        // Not duplicated / re-imported.
        assert_eq!(out.matches("config : Config.Config").count(), 1, "{out}");
    }
}

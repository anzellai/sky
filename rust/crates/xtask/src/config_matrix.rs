//! `xtask config-matrix` — the dual-path fixture matrix (design §7.2).
//!
//! # What this answers
//!
//! `docs/tooling/config-architecture.md` §7 says every default must reproduce
//! today's behaviour, and §12 risk 2 says the failure is invisible: the app
//! compiles, runs, and quietly does something else. §12.2 says the matrix
//! "must exist before the first default is written". This is that instrument.
//! It moves nothing.
//!
//! # The property, and why it is observed rather than computed
//!
//! For every covered setting, the matrix records the **effective value** —
//! what a real binary, built by the real compiler and launched, reports it is
//! using — for every combination of the layers that can supply it: the
//! environment, `sky.toml`, and a `withX` builder.
//!
//! The value is read from the app's OWN startup output at the point of
//! consumption: `selectStore` prints the ttl, path and idle-evict window it is
//! about to build a session store with (`live_store.go:1784-1828`), and
//! `liveAppRun` prints the port it is about to bind (`live.go:4329`). Both
//! lines are printed by the code that uses the value, not by a resolver that
//! returns it.
//!
//! That distinction is the entire design. This repository's dominant gate
//! defect is proving two functions agree while the value that reaches the
//! runtime comes from a third place; a matrix that re-implemented the
//! resolver, or that asked a `--sky-config` printer sitting in `main` before
//! the app config exists, would be that defect wearing the uniform of its
//! cure. §7.2's own suggestion — compare via `--sky-config` — has exactly that
//! hole for builders, and `--sky-config` does not exist in the tree anyway
//! (verified: the string appears only in prose). See "What §7.2 asked for and
//! what this does instead", below.
//!
//! # Shape of a run
//!
//! Five fixture projects are generated into a temp dir outside the repo and
//! built by this tree's compiler:
//!
//! | fixture | `sky.toml` keys | builder calls |
//! |---|---|---|
//! | `base`         | none | none |
//! | `toml`         | all  | none |
//! | `builder`      | none | all  |
//! | `toml_builder` | all  | all  |
//! | `prefix`       | `[env] prefix` only | none |
//!
//! Each of the first four is run twice — once with no setting in the
//! environment, once with every setting in the environment — giving eight
//! runs, and each run reports EVERY covered setting at once. Cells are then
//! read off: a setting with three layers has 8, one with two has 4.
//!
//! The child is spawned with a **cleared environment** (`spawn_isolated`).
//! A developer with `SKY_LIVE_TTL` exported would otherwise contaminate every
//! cell, and the contamination would look like a real difference.
//!
//! # What makes a cell non-vacuous
//!
//! Three guards, each aimed at a failure this repo has actually shipped:
//!
//! * **Distinct sentinels, checked before anything is built.** Every arm of a
//!   setting sets a different value. Two arms sharing one would make the cell
//!   compare a constant to itself — five gates did that this week, two of them
//!   reporting `ok` in 0.023 s.
//! * **Distinguishable observations, checked after.** `unset`, `env` and
//!   `toml` must OBSERVE three different values. Distinct inputs that produce
//!   equal outputs mean the cell cannot say which layer won, and that is a
//!   gate failure, not a curiosity.
//! * **No silent non-observation.** A probe that matches nothing, or matches
//!   the wrong number of times, FAILS. It never records "absent" and moves on.
//!   `-` is recorded only where the probe matched and the field is genuinely
//!   not in the line.
//!
//! # `builder_reaches_runtime`
//!
//! Each builder setting declares whether its value reaches the runtime, and
//! the gate VERIFIES the declaration against the builder-only cell. `live.ttl`
//! declares `false`: `lower.rs:822` seeds `LIVE_TTL=1800` into every program
//! unconditionally, `SetSkyDefault` is set-if-unset, and `parseTTL` takes the
//! first parseable of `[env, toml]` — so `Live.withTtl` cannot win. The
//! builder-only cell sets `41m` and observes `30m0s`.
//!
//! The declaration is checked in BOTH directions. Claiming a live builder is
//! dead hides a regression exactly as well as claiming a dead one is live.
//! That two-way check is what lets stage 3 fix `withTtl` safely: the fix flips
//! the flag, the observed cells move, and the gate refuses both unless a
//! `[[default-changed]]` row authorises the move.
//!
//! # Pre-binary surfaces
//!
//! Stage 1 counted 14 (`docs/coverage/config-surface.json`), and §4.3.1 notes
//! that §4.4's `./sky-out/app --sky-config` answer needs a binary `sky db
//! start` does not have. This matrix covers exactly one of them — `[env]
//! prefix`, via `[[prefix_check]]`, because it shapes the NAMES every other
//! cell depends on and a stage-3 mistake there would invalidate the whole
//! table while every individual cell still passed.
//!
//! The rest are **out of scope for this stage, deliberately**, and the reason
//! is not cost: a pre-binary reader has no effective value in the sense this
//! matrix compares. `resolve_max_open_conns` does not resolve a value the
//! runtime then uses; it sizes a `postgresql.conf` the runtime never reads.
//! Comparing it needs a different instrument — observe what the CLI WRITES or
//! REFUSES (the generated `postgresql.conf`, the `--embed`-with-DSN ambiguity
//! error, the `sky db reset` confirmation line) — and folding that into the
//! cell model would mean one gate with two incompatible notions of "effective
//! value". It is named here so stage 3 does not mistake this gate's green for
//! cover it does not give. `[[deferred]]` rows carry the per-setting detail.
//!
//! # What §7.2 asked for and what this does instead
//!
//! §7.2 specifies a legacy path behind `SKY_CONFIG_LEGACY=1` so both paths
//! exist in one binary, and a comparison of the two. **There is no second path
//! yet** — this stage moves nothing — so a gate that ran "both paths" today
//! would run the current path twice and compare its output to itself. That is
//! the vacuity the design's own risk 8 warns about.
//!
//! So the dual path is realised over TIME rather than over a flag: the current
//! path's effective values are recorded in `docs/coverage/config-matrix.json`,
//! and that file is the baseline every later run is compared against. Any
//! unlisted difference fails. When stage 3 introduces the new path, the same
//! harness compares it against the same baseline, which is the comparison §7.2
//! wanted; `SKY_CONFIG_LEGACY=1` then becomes one more arm rather than a
//! precondition. The baseline is live from the first commit — a default
//! changed today reddens this gate today.
//!
//! # What this gate does NOT catch
//!
//! Stated plainly, in the shape of stage 1's own list:
//!
//! 1. **It covers 4 settings of the 30+23 census.** Everything else is in
//!    `[[deferred]]` or `[[unobservable]]` with a reason. The bucket counts
//!    are ratcheted so coverage cannot quietly get worse, but a ratchet on
//!    "how much is uncovered" is not coverage.
//! 2. **It cannot see a setting that has no consumer.** `[auth]` produces no
//!    effective value because nothing reads it, so there is nothing to
//!    compare. Only `config-surface`'s `seeded_without_reader` sees that.
//! 3. **It observes through two startup lines.** A setting whose consumer
//!    prints nothing is invisible to it, and rewording either line breaks the
//!    probe (loudly — the occurrence check fails, it does not silently
//!    observe nothing).
//! 4. **It sets every covered setting at once.** Cross-talk between settings
//!    is recorded rather than isolated: if `live.store` ever changed how
//!    `live.ttl` resolves, the matrix would show the changed value without
//!    attributing it. Distinct sentinels make such an effect visible; they do
//!    not name its cause.
//! 5. **It pins what it does not measure.** `LIVE_STORE=sqlite` is a declared
//!    harness constant, so the store KIND's precedence branch is not measured
//!    by any cell — only the path branch beside it.
//! 6. **It proves a value reaches a consumer, not that the consumer is
//!    right.** `selectStore` printing `ttl=30m0s` proves the store was built
//!    with 30 minutes; it does not prove a session actually expires then.
//! 7. **It is blind to a THIRD reader of the same name.** §1.7's
//!    `csrf_middleware.go:82` reads `LIVE_TTL` with a 30-day default. The
//!    matrix records the two readers that print; a reader that resolves the
//!    same variable silently is outside it, which is precisely how the CSRF
//!    collision survived.
//! 8. **It measures this platform.** The fixtures are built and run locally;
//!    a value that resolves differently on another OS is not compared.

use serde_json::{json, Map, Value};
use std::collections::{BTreeMap, BTreeSet};
use std::path::{Path, PathBuf};
use std::time::{Duration, SystemTime};

use crate::harness::layer2;

// ---------------------------------------------------------------------------
// Probes — the observation points, and how many times each must appear.
// ---------------------------------------------------------------------------

/// `liveAppRun`'s bind announcement (`live.go:4329`). Exactly one per process.
const PROBE_LISTENING: &str = "Sky.Live listening on :";
const LISTENING_OCCURRENCES: usize = 1;

/// `selectStore`'s banner (`live_store.go:1784-1828`), printed by the code
/// that is about to build the store out of these values.
///
/// TWO per process, and the second is not noise: the inline dev console mounts
/// as a Sky.Live sub-app with a session store of its own
/// (`subapp_inprocess.go:400`), which is §1.7's third `LIVE_TTL` reader made
/// visible. Lines are keyed by store kind — the app's is `sqlite` (pinned by
/// the harness constant), the console's `memory` — so the two are never
/// confused by print order.
const PROBE_STORE_BANNER: &str = "[sky.live] session store: ";
const STORE_BANNER_OCCURRENCES: usize = 2;

/// How long an app may take to print its readiness line.
const READY_TIMEOUT: Duration = Duration::from_secs(45);

/// How long to let the stderr pipe catch up with the stdout readiness line
/// before reading the log. See the note in [`run_fixture`].
const BANNER_SETTLE: Duration = Duration::from_secs(10);

/// How long to allow between the app ANNOUNCING a port and that port actually
/// being bound. The announcement is `fmt.Printf`ed from the same goroutine that
/// is about to call `ListenAndServe`, so this is slack, not a wait.
const BIND_CONFIRM: Duration = Duration::from_secs(10);

// ---------------------------------------------------------------------------
// A TOML subset, hand-rolled
// ---------------------------------------------------------------------------
//
// The workspace has no TOML parser (design §1.1) and `config_surface.rs` reads
// its inputs the same way. The manifest is written to this subset on purpose:
// `[[stanza]]` headers, one `key = value` per line, values are quoted strings
// (with `\"` escapes), single-line string arrays, bare integers or bare
// booleans. Anything else is a parse error rather than a silent skip.

#[derive(Clone, Debug, PartialEq)]
enum Val {
    Str(String),
    List(Vec<String>),
    Bool(bool),
}

impl Val {
    fn as_str(&self) -> Option<&str> {
        match self {
            Val::Str(s) => Some(s),
            _ => None,
        }
    }
    fn as_list(&self) -> Option<&[String]> {
        match self {
            Val::List(v) => Some(v),
            _ => None,
        }
    }
    fn as_bool(&self) -> Option<bool> {
        match self {
            Val::Bool(b) => Some(*b),
            _ => None,
        }
    }
}

type Stanza = BTreeMap<String, Val>;

fn parse_manifest(text: &str) -> Result<Vec<(String, Stanza)>, String> {
    let mut out: Vec<(String, Stanza)> = Vec::new();
    for (n, raw) in text.lines().enumerate() {
        let line = strip_comment(raw).trim().to_string();
        if line.is_empty() {
            continue;
        }
        if let Some(name) = line.strip_prefix("[[").and_then(|s| s.strip_suffix("]]")) {
            out.push((name.trim().to_string(), Stanza::new()));
            continue;
        }
        let Some(eq) = line.find('=') else {
            return Err(format!("config-matrix.toml:{}: not `key = value`: {line}", n + 1));
        };
        let key = line[..eq].trim().to_string();
        let val = parse_value(line[eq + 1..].trim())
            .ok_or_else(|| format!("config-matrix.toml:{}: unparseable value: {line}", n + 1))?;
        let Some((_, st)) = out.last_mut() else {
            return Err(format!("config-matrix.toml:{}: `{key}` before any stanza", n + 1));
        };
        if st.insert(key.clone(), val).is_some() {
            return Err(format!("config-matrix.toml:{}: duplicate key `{key}`", n + 1));
        }
    }
    Ok(out)
}

/// Drop a trailing `#` comment, respecting quotes and `\"`.
fn strip_comment(line: &str) -> &str {
    let b = line.as_bytes();
    let (mut i, mut in_str, mut esc) = (0usize, false, false);
    while i < b.len() {
        match b[i] {
            b'\\' if in_str => esc = !esc,
            b'"' if !esc => in_str = !in_str,
            b'#' if !in_str => return &line[..i],
            _ => esc = false,
        }
        if b[i] != b'\\' {
            esc = false;
        }
        i += 1;
    }
    line
}

fn parse_value(s: &str) -> Option<Val> {
    if s == "true" {
        return Some(Val::Bool(true));
    }
    if s == "false" {
        return Some(Val::Bool(false));
    }
    if let Some(inner) = s.strip_prefix('[').and_then(|x| x.strip_suffix(']')) {
        let inner = inner.trim();
        if inner.is_empty() {
            return Some(Val::List(Vec::new()));
        }
        let mut items = Vec::new();
        for part in split_top(inner) {
            items.push(unquote(part.trim())?);
        }
        return Some(Val::List(items));
    }
    if s.starts_with('"') {
        return Some(Val::Str(unquote(s)?));
    }
    // A bare integer is accepted and kept as text — every sentinel is compared
    // and rendered as text, so parsing it to a number and back would only add
    // a place for `8000` and `8000.0` to diverge.
    if !s.is_empty() && s.bytes().all(|c| c.is_ascii_digit()) {
        return Some(Val::Str(s.to_string()));
    }
    None
}

fn split_top(s: &str) -> Vec<&str> {
    let (mut out, mut start, mut in_str, mut esc) = (Vec::new(), 0usize, false, false);
    let b = s.as_bytes();
    for i in 0..b.len() {
        match b[i] {
            b'\\' if in_str => esc = !esc,
            b'"' if !esc => {
                in_str = !in_str;
                esc = false;
            }
            b',' if !in_str => {
                out.push(&s[start..i]);
                start = i + 1;
                esc = false;
            }
            _ => esc = false,
        }
    }
    out.push(&s[start..]);
    out
}

fn unquote(s: &str) -> Option<String> {
    let inner = s.strip_prefix('"')?.strip_suffix('"')?;
    let mut out = String::new();
    let mut esc = false;
    for c in inner.chars() {
        if esc {
            out.push(c);
            esc = false;
        } else if c == '\\' {
            esc = true;
        } else {
            out.push(c);
        }
    }
    Some(out)
}

// ---------------------------------------------------------------------------
// The declared matrix
// ---------------------------------------------------------------------------

struct Setting {
    id: String,
    census: Vec<String>,
    toml_section: Option<String>,
    toml_key: Option<String>,
    env_suffix: Option<String>,
    builder: Option<String>,
    builder_kind: String,
    probe: String,
    probe_key: Option<String>,
    field_after: String,
    field_until: String,
    expect_unset: String,
    set_env: Option<String>,
    set_toml: Option<String>,
    set_builder: Option<String>,
    builder_reaches_runtime: Option<bool>,
}

struct PrefixCheck {
    prefix: String,
    census: Vec<String>,
    wrong_env: String,
    wrong_value: String,
    right_env: String,
    right_value: String,
    expect_wrong: String,
    expect_right: String,
}

struct Manifest {
    constants: Vec<(String, String)>,
    settings: Vec<Setting>,
    prefix: PrefixCheck,
    deferred: Vec<String>,
    unobservable: Vec<String>,
    listed: Vec<Listed>,
    deferred_stanzas: usize,
    bucket_changes: Vec<BucketChange>,
}

/// A `[[default-changed]]` / `[[moved]]` row: the only way an observed value
/// may differ from the baseline.
struct Listed {
    kind: String,
    cell: String,
    from: String,
    to: String,
}

fn need<'a>(st: &'a Stanza, k: &str, what: &str) -> Result<&'a Val, String> {
    st.get(k).ok_or_else(|| format!("{what}: missing `{k}`"))
}

fn need_str(st: &Stanza, k: &str, what: &str) -> Result<String, String> {
    need(st, k, what)?
        .as_str()
        .map(str::to_string)
        .ok_or_else(|| format!("{what}: `{k}` must be a string"))
}

fn opt_str(st: &Stanza, k: &str) -> Option<String> {
    st.get(k).and_then(Val::as_str).map(str::to_string)
}

fn load_manifest(root: &Path) -> Result<Manifest, String> {
    let path = manifest_path(root);
    let text = std::fs::read_to_string(&path)
        .map_err(|e| format!("cannot read {}: {e}", path.display()))?;
    let stanzas = parse_manifest(&text)?;

    let mut constants = Vec::new();
    let mut settings = Vec::new();
    let mut prefix: Option<PrefixCheck> = None;
    let mut deferred = Vec::new();
    let mut unobservable = Vec::new();
    let mut listed = Vec::new();
    let mut deferred_stanzas = 0usize;
    let mut bucket_changes: Vec<BucketChange> = Vec::new();

    for (name, st) in &stanzas {
        match name.as_str() {
            "harness_constant" => {
                let what = "[[harness_constant]]";
                let reason = need_str(st, "reason", what)?;
                if reason.trim().len() < 40 {
                    return Err(format!(
                        "{what}: `reason` is {} chars. A value the harness pins is a value the \
                         matrix is NOT measuring; say what that costs.",
                        reason.trim().len()
                    ));
                }
                constants.push((need_str(st, "env", what)?, need_str(st, "value", what)?));
            }
            "setting" => {
                let id = need_str(st, "id", "[[setting]]")?;
                let what = format!("[[setting]] {id}");
                let s = Setting {
                    census: need(st, "census", &what)?
                        .as_list()
                        .ok_or_else(|| format!("{what}: `census` must be a list"))?
                        .to_vec(),
                    toml_section: opt_str(st, "toml_section"),
                    toml_key: opt_str(st, "toml_key"),
                    env_suffix: opt_str(st, "env_suffix"),
                    builder: opt_str(st, "builder"),
                    builder_kind: opt_str(st, "builder_kind").unwrap_or_default(),
                    probe: need_str(st, "probe", &what)?,
                    probe_key: opt_str(st, "probe_key"),
                    field_after: opt_str(st, "field_after").unwrap_or_default(),
                    field_until: opt_str(st, "field_until").unwrap_or_default(),
                    expect_unset: need_str(st, "expect_unset", &what)?,
                    set_env: opt_str(st, "set_env"),
                    set_toml: opt_str(st, "set_toml"),
                    set_builder: opt_str(st, "set_builder"),
                    builder_reaches_runtime: st
                        .get("builder_reaches_runtime")
                        .and_then(Val::as_bool),
                    id,
                };
                if s.builder.is_some() != s.builder_reaches_runtime.is_some() {
                    return Err(format!(
                        "{what}: a setting with a builder must declare \
                         `builder_reaches_runtime`, and one without must not."
                    ));
                }
                if s.builder.is_some() && s.set_builder.is_none() {
                    return Err(format!("{what}: a builder arm needs `set_builder`"));
                }
                if s.env_suffix.is_some() && s.set_env.is_none() {
                    return Err(format!("{what}: an env arm needs `set_env`"));
                }
                if s.toml_key.is_some() && s.set_toml.is_none() {
                    return Err(format!("{what}: a toml arm needs `set_toml`"));
                }
                settings.push(s);
            }
            "prefix_check" => {
                let what = "[[prefix_check]]";
                prefix = Some(PrefixCheck {
                    prefix: need_str(st, "prefix", what)?,
                    census: need(st, "census", what)?
                        .as_list()
                        .ok_or_else(|| format!("{what}: `census` must be a list"))?
                        .to_vec(),
                    wrong_env: need_str(st, "wrong_env", what)?,
                    wrong_value: need_str(st, "wrong_value", what)?,
                    right_env: need_str(st, "right_env", what)?,
                    right_value: need_str(st, "right_value", what)?,
                    expect_wrong: need_str(st, "expect_wrong", what)?,
                    expect_right: need_str(st, "expect_right", what)?,
                });
            }
            "deferred" | "unobservable" => {
                let what = format!("[[{name}]]");
                let ids = need(st, "ids", &what)?
                    .as_list()
                    .ok_or_else(|| format!("{what}: `ids` must be a list"))?
                    .to_vec();
                if ids.is_empty() {
                    return Err(format!("{what}: an empty `ids` buys free coverage"));
                }
                let reason = need_str(st, "reason", &what)?;
                if reason.trim().len() < 80 {
                    return Err(format!(
                        "{what} {ids:?}: `reason` is {} chars. \"not covered\" is not a reason; \
                         say what an observation would have to be.",
                        reason.trim().len()
                    ));
                }
                if name == "deferred" {
                    need_str(st, "needs", &what)?;
                    deferred_stanzas += 1;
                    deferred.extend(ids);
                } else {
                    unobservable.extend(ids);
                }
            }
            "default-changed" | "moved" => {
                let what = format!("[[{name}]]");
                // All five fields, always. An empty stanza would buy a free
                // behaviour change, which is the whole thing the split exists
                // to prevent.
                let reason = need_str(st, "reason", &what)?;
                if reason.trim().len() < 40 {
                    return Err(format!("{what}: `reason` is too short to be a decision"));
                }
                need_str(st, "commit", &what)?;
                let (from, to) = (need_str(st, "from", &what)?, need_str(st, "to", &what)?);
                // The two stanza kinds are not interchangeable, and this is
                // where the difference is enforced. `[[moved]]` says a setting
                // changed LAYER — which value supplies it — and that the
                // effective value did not change; a `[[moved]]` whose endpoints
                // differ is a `[[default-changed]]` filed under a name that
                // does not have to explain itself.
                if name == "moved" && from != to {
                    return Err(format!(
                        "{what} {}: from {from:?} != to {to:?}. A [[moved]] setting keeps its \
                         effective value; if the value changed too, that is a \
                         [[default-changed]] and needs its own reason.",
                        need_str(st, "cell", &what)?
                    ));
                }
                listed.push(Listed {
                    kind: name.clone(),
                    cell: need_str(st, "cell", &what)?,
                    from,
                    to,
                });
            }
            "bucket-change" => {
                let what = "[[bucket-change]]";
                let metric = need_str(st, "metric", what)?;
                if !RATCHETED.iter().any(|(m, _)| *m == metric) {
                    return Err(format!(
                        "{what}: `metric = {metric:?}` is not ratcheted. Accounting for a rise \
                         in a metric nothing ratchets buys nothing and hides the typo."
                    ));
                }
                let reason = need_str(st, "reason", what)?;
                if reason.trim().len() < 80 {
                    return Err(format!(
                        "{what} {metric}: `reason` is {} chars. Raising an uncovered count is \
                         a decision to stop measuring something; say what and why.",
                        reason.trim().len()
                    ));
                }
                need_str(st, "commit", what)?;
                let num = |k: &str| -> Result<u64, String> {
                    need_str(st, k, what)?
                        .trim()
                        .parse::<u64>()
                        .map_err(|_| format!("{what}: `{k}` must be a whole number"))
                };
                let (from, to) = (num("from")?, num("to")?);
                if to <= from {
                    return Err(format!(
                        "{what} {metric}: to {to} is not above from {from}. A \
                         [[bucket-change]] authorises a RISE; a fall needs no authorisation."
                    ));
                }
                bucket_changes.push(BucketChange { metric, from, to });
            }
            other => return Err(format!("config-matrix.toml: unknown stanza `[[{other}]]`")),
        }
    }

    let prefix = prefix.ok_or("config-matrix.toml: no [[prefix_check]]")?;
    if settings.is_empty() {
        return Err("config-matrix.toml: no [[setting]] — the matrix would be empty".into());
    }
    Ok(Manifest {
        constants,
        settings,
        prefix,
        deferred,
        unobservable,
        listed,
        deferred_stanzas,
        bucket_changes,
    })
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

/// The arm combination a fixture is built for.
#[derive(Clone, Copy, PartialEq, Eq)]
struct Build {
    toml: bool,
    builder: bool,
}

impl Build {
    fn name(self) -> &'static str {
        match (self.toml, self.builder) {
            (false, false) => "base",
            (true, false) => "toml",
            (false, true) => "builder",
            (true, true) => "toml_builder",
        }
    }
}

const BUILDS: [Build; 4] = [
    Build { toml: false, builder: false },
    Build { toml: true, builder: false },
    Build { toml: false, builder: true },
    Build { toml: true, builder: true },
];

fn sky_toml(m: &Manifest, b: Build, env_prefix: Option<&str>) -> String {
    let mut s = String::from("name = \"cfgmatrix\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n");
    if let Some(p) = env_prefix {
        s.push_str(&format!("\n[env]\nprefix = \"{p}\"\n"));
    }
    if !b.toml {
        return s;
    }
    let mut by_section: BTreeMap<&str, Vec<(&str, &str)>> = BTreeMap::new();
    for st in &m.settings {
        if let (Some(sec), Some(key), Some(val)) =
            (&st.toml_section, &st.toml_key, &st.set_toml)
        {
            by_section.entry(sec).or_default().push((key, val));
        }
    }
    for (sec, kvs) in by_section {
        s.push_str(&format!("\n[{sec}]\n"));
        for (k, v) in kvs {
            // The port is the one numeric key in the covered set; every other
            // value is a duration or a path and must stay quoted.
            if v.bytes().all(|c| c.is_ascii_digit()) {
                s.push_str(&format!("{k} = {v}\n"));
            } else {
                s.push_str(&format!("{k} = \"{v}\"\n"));
            }
        }
    }
    s
}

fn main_sky(m: &Manifest, b: Build) -> String {
    let mut builders = String::new();
    if b.builder {
        for st in &m.settings {
            let (Some(call), Some(val)) = (&st.builder, &st.set_builder) else {
                continue;
            };
            let arg = if st.builder_kind == "int" {
                val.clone()
            } else {
                format!("\"{val}\"")
            };
            builders.push_str(&format!("\n            |> {call} {arg}"));
        }
    }
    format!(
        r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Html exposing (div, text)
import Std.Live as Live
import Std.Cmd as Cmd
import Std.Sub as Sub

type alias Model =
    {{ page : Page }}

type Page
    = Home

type Msg
    = Noop

init : a -> ( Model, Cmd Msg )
init _req =
    ( {{ page = Home }}, Cmd.none )

update msg model =
    case msg of
        Noop ->
            ( model, Cmd.none )

subscriptions _model =
    Sub.none

view _model =
    div [] [ text "cfgmatrix" ]

main =
    Live.app
        (Live.config
            {{ init = init
            , update = update
            , view = view
            , subscriptions = subscriptions
            , routes = [ Live.route "/" Home ]
            , notFound = Home
            }}{builders})
"#
    )
}

fn write_fixture(dir: &Path, toml: &str, main: &str) -> Result<(), String> {
    std::fs::create_dir_all(dir.join("src"))
        .map_err(|e| format!("cannot create {}: {e}", dir.display()))?;
    std::fs::write(dir.join("sky.toml"), toml).map_err(|e| format!("write sky.toml: {e}"))?;
    std::fs::write(dir.join("src/Main.sky"), main).map_err(|e| format!("write Main.sky: {e}"))?;
    Ok(())
}

/// Where a built compiler may live, in preference order.
fn sky_binary_candidates(root: &Path) -> Vec<PathBuf> {
    let mut candidates: Vec<PathBuf> = Vec::new();
    if let Some(t) = std::env::var_os("CARGO_TARGET_DIR") {
        candidates.push(PathBuf::from(t).join("release/sky"));
    }
    candidates.push(root.join("rust/target/release/sky"));
    // `scripts/build.sh:80` installs the cargo-built Rust binary here, so this
    // is the same artefact, not the retired Haskell compiler.
    candidates.push(root.join("sky-out/sky"));
    candidates
}

/// The trees whose contents decide what a compiled `sky` binary DOES: the
/// compiler crates, the Go runtime it embeds, and the Sky stdlib it ships.
///
/// `rust/crates/xtask` is excluded deliberately — editing the gate does not
/// change what an already-built compiler emits, and demanding a compiler
/// rebuild for a comment in this file would train people to bypass the check.
const MEASURED_SOURCE_ROOTS: &[(&str, &[&str])] = &[
    ("rust/crates", &["rs"]),
    ("runtime-go", &["go"]),
    ("sky-stdlib", &["sky", "skyi"]),
];

/// The newest mtime among [`MEASURED_SOURCE_ROOTS`], with the file that carries
/// it — the witness, so a staleness message can name what moved.
fn newest_source_mtime(root: &Path) -> Result<(SystemTime, PathBuf), String> {
    let mut newest = SystemTime::UNIX_EPOCH;
    let mut witness = PathBuf::new();
    let mut seen = 0usize;
    for (rel, exts) in MEASURED_SOURCE_ROOTS {
        let dir = root.join(rel);
        if !dir.is_dir() {
            return Err(format!(
                "{} does not exist — the gate cannot establish that the compiler it is \
                 about to measure was built from this tree, and a measurement of an \
                 unknown artefact is not a measurement.",
                dir.display()
            ));
        }
        walk_newest(&dir, exts, &mut newest, &mut witness, &mut seen)?;
    }
    // A walk that found nothing would make every binary look fresh — the
    // vacuity this whole gate exists to refuse.
    if seen < 100 {
        return Err(format!(
            "the source walk found only {seen} files under {:?}. A walk that finds \
             nothing makes any binary look fresh.",
            MEASURED_SOURCE_ROOTS.iter().map(|(r, _)| *r).collect::<Vec<_>>()
        ));
    }
    Ok((newest, witness))
}

fn walk_newest(
    dir: &Path,
    exts: &[&str],
    newest: &mut SystemTime,
    witness: &mut PathBuf,
    seen: &mut usize,
) -> Result<(), String> {
    let entries =
        std::fs::read_dir(dir).map_err(|e| format!("cannot read {}: {e}", dir.display()))?;
    for entry in entries {
        let entry = entry.map_err(|e| format!("cannot read an entry of {}: {e}", dir.display()))?;
        let path = entry.path();
        let name = entry.file_name();
        let name = name.to_string_lossy();
        // Build output and vendored deps are not sources; walking them would
        // make the check depend on its own artefacts.
        if name == "target" || name == "node_modules" || name.starts_with('.') {
            continue;
        }
        if path.is_dir() {
            if dir.ends_with("crates") && name == "xtask" {
                continue;
            }
            walk_newest(&path, exts, newest, witness, seen)?;
            continue;
        }
        let Some(ext) = path.extension().and_then(|e| e.to_str()) else {
            continue;
        };
        if !exts.contains(&ext) {
            continue;
        }
        // Go test files are not compiled into the embedded runtime.
        if ext == "go" && name.ends_with("_test.go") {
            continue;
        }
        *seen += 1;
        let m = entry
            .metadata()
            .and_then(|md| md.modified())
            .map_err(|e| format!("cannot stat {}: {e}", path.display()))?;
        if m > *newest {
            *newest = m;
            *witness = path;
        }
    }
    Ok(())
}

/// The compiler binary this gate measures, PROVEN to have been built from the
/// tree it is measuring.
///
/// # The defect this closes
///
/// This used to be "find the first candidate that is a file". So the gate
/// measured whatever binary happened to be on disk. Demonstrated: reverting the
/// stage-3 precedence fix in `runtime-go/` WITHOUT rebuilding produced
/// `config-matrix: OK` in 49 s; the same edit after a 17.8 s `cargo build`
/// produced six findings including
/// `VERDICT live.ttl.builder_reaches_runtime: true -> false`. CI was covered by
/// ORDERING in the workflow — a build step that happens to run first — not by
/// the gate, and an ordering nobody asserts is not a property.
///
/// The registry's own comment recorded the consequence: a source mutation was
/// rejected as a falsifier because it "would leave it measuring the unmutated
/// tree", so the declared falsifier could only ever be a lie in the gate's own
/// TOML. Establishing freshness here is what makes a SOURCE mutation a legal
/// falsifier again.
fn sky_binary(root: &Path) -> Result<PathBuf, String> {
    let (newest, witness) = newest_source_mtime(root)?;

    let fresh = |p: &Path| -> bool {
        std::fs::metadata(p)
            .and_then(|md| md.modified())
            .map(|m| m >= newest)
            .unwrap_or(false)
    };

    if let Some(p) = sky_binary_candidates(root).into_iter().find(|p| fresh(p)) {
        return Ok(p);
    }

    // Stale or absent. Build it — measuring a binary older than the sources
    // whose behaviour is under test measures the wrong tree, and refusing
    // outright would leave a source mutation unfalsifiable.
    build_compiler(root)?;

    let Some(p) = sky_binary_candidates(root).into_iter().find(|p| p.is_file()) else {
        return Err(
            "`cargo build --release -p sky` reported success and produced no binary at \
             rust/target/release/sky. A gate that cannot find what it measures has not \
             passed."
                .to_string(),
        );
    };
    if !fresh(&p) {
        return Err(format!(
            "{} is still older than {} after a rebuild. The gate measures what a compiler \
             built from THIS tree does; an older binary answers for a different tree.",
            p.display(),
            witness.display()
        ));
    }
    Ok(p)
}

fn build_compiler(root: &Path) -> Result<(), String> {
    let manifest = root.join("rust/Cargo.toml");
    let out = std::process::Command::new("cargo")
        .args(["build", "--release", "-p", "sky", "--manifest-path"])
        .arg(&manifest)
        .stdin(std::process::Stdio::null())
        .output()
        .map_err(|e| {
            format!(
                "the compiler is stale or absent and `cargo` could not be spawned to \
                 rebuild it ({e}). Install a Rust toolchain, or build it yourself with \
                 `cargo build --release -p sky`. A gate whose prerequisite is missing \
                 FAILS; it does not skip."
            )
        })?;
    if out.status.success() {
        return Ok(());
    }
    let mut log = String::from_utf8_lossy(&out.stdout).into_owned();
    log.push_str(&String::from_utf8_lossy(&out.stderr));
    Err(format!(
        "the compiler is stale and `cargo build --release -p sky` failed:\n{}",
        tail(&log, 30)
    ))
}

fn build_fixture(sky: &Path, dir: &Path) -> Result<(), String> {
    let out = std::process::Command::new(sky)
        .arg("build")
        .arg("src/Main.sky")
        .current_dir(dir)
        .stdin(std::process::Stdio::null())
        .output()
        .map_err(|e| format!("could not spawn `sky build` in {}: {e}", dir.display()))?;
    if out.status.success() {
        return Ok(());
    }
    let mut log = String::from_utf8_lossy(&out.stdout).into_owned();
    log.push_str(&String::from_utf8_lossy(&out.stderr));
    Err(format!(
        "fixture {} failed to build:\n{}",
        dir.display(),
        tail(&log, 30)
    ))
}

fn tail(s: &str, n: usize) -> String {
    let lines: Vec<&str> = s.lines().collect();
    lines[lines.len().saturating_sub(n)..].join("\n")
}

// ---------------------------------------------------------------------------
// Observation
// ---------------------------------------------------------------------------

/// The captures of one probe: for `listening`, a single unkeyed remainder; for
/// `store_banner`, one remainder per store kind.
struct Observed {
    listening: Vec<String>,
    store_banner: BTreeMap<String, String>,
    store_banner_count: usize,
}

fn observe(log: &str) -> Observed {
    let mut listening = Vec::new();
    let mut store_banner = BTreeMap::new();
    let mut count = 0usize;
    for line in log.lines() {
        if let Some(i) = line.find(PROBE_LISTENING) {
            listening.push(line[i + PROBE_LISTENING.len()..].trim().to_string());
        }
        if let Some(i) = line.find(PROBE_STORE_BANNER) {
            count += 1;
            let rest = line[i + PROBE_STORE_BANNER.len()..].trim().to_string();
            let kind = rest.split_whitespace().next().unwrap_or("").to_string();
            store_banner.insert(kind, rest);
        }
    }
    Observed {
        listening,
        store_banner,
        store_banner_count: count,
    }
}

/// Pull one setting's field out of a probe's capture.
///
/// `Err` means the probe itself did not produce the capture this setting reads
/// — a non-observation, which FAILS. `Ok("-")` means the probe matched and the
/// field is genuinely absent from the line, which is a legitimate value.
fn extract(st: &Setting, o: &Observed) -> Result<String, String> {
    let capture = match st.probe.as_str() {
        "listening" => o
            .listening
            .first()
            .cloned()
            .ok_or_else(|| format!("{}: probe `listening` matched no line", st.id))?,
        "store_banner" => {
            let key = st.probe_key.as_deref().unwrap_or("");
            o.store_banner.get(key).cloned().ok_or_else(|| {
                format!(
                    "{}: probe `store_banner` has no line for store kind {key:?} (saw {:?})",
                    st.id,
                    o.store_banner.keys().collect::<Vec<_>>()
                )
            })?
        }
        other => return Err(format!("{}: unknown probe `{other}`", st.id)),
    };
    if st.field_after.is_empty() {
        return Ok(capture);
    }
    let Some(i) = capture.find(&st.field_after) else {
        return Ok("-".to_string());
    };
    let rest = &capture[i + st.field_after.len()..];
    let end = rest
        .find(|c: char| st.field_until.contains(c))
        .unwrap_or(rest.len());
    Ok(rest[..end].to_string())
}

// ---------------------------------------------------------------------------
// Running one cell-set
// ---------------------------------------------------------------------------

/// Every port any arm of `live.port` (or the prefix check) could make the app
/// bind. The preflight checks ALL of them, because the run does not know in
/// advance which one it will observe.
fn port_sentinels(m: &Manifest) -> Vec<u16> {
    let mut out: Vec<u16> = Vec::new();
    let mut push = |s: &str| {
        if let Ok(p) = s.parse::<u16>() {
            if !out.contains(&p) {
                out.push(p);
            }
        }
    };
    for st in &m.settings {
        if st.probe != "listening" {
            continue;
        }
        push(&st.expect_unset);
        for v in [&st.set_env, &st.set_toml, &st.set_builder].into_iter().flatten() {
            push(v);
        }
    }
    for v in [
        &m.prefix.wrong_value,
        &m.prefix.right_value,
        &m.prefix.expect_wrong,
        &m.prefix.expect_right,
    ] {
        push(v);
    }
    out
}

/// Run one fixture and observe it.
///
/// # Why the port is not passed in
///
/// It used to be. `run_fixture` took `expect_port`, computed by
/// [`expected_port`] — the gate's OWN re-implementation of `resolveLivePort` —
/// and `wait_ready` then blocked until the app bound THAT port. So the
/// `live.port` row compared the gate's constants to themselves: inverting the
/// real precedence surfaced as a 45-second readiness timeout naming neither the
/// setting nor the precedence, and **no cell difference was ever produced**.
/// The other three settings were observed genuinely, at consumption
/// (`live_store.go:1798`); this one was not.
///
/// The port is now observed the same way: readiness is the announcement alone,
/// the announced port is adopted, and it is then confirmed genuinely bound — so
/// a precedence inversion changes the `live.port` cell and fails as a named
/// difference.
fn run_fixture(dir: &Path, env: &[(String, String)]) -> Result<Observed, String> {
    let pairs: Vec<(&str, String)> = env.iter().map(|(k, v)| (k.as_str(), v.clone())).collect();
    let mut server = layer2::Server::spawn_isolated_unbound(&dir.join("sky-out/app"), dir, &pairs)?;
    let ready = server.wait_ready(PROBE_LISTENING, READY_TIMEOUT);

    // The announcement is a CLAIM. Adopt it, then confirm the app really holds
    // that port — otherwise a binary that printed a number it never bound would
    // read as a clean observation, and teardown would assert the release of a
    // port nothing ever took.
    if ready.is_ok() {
        let announced = observe(&server.log())
            .listening
            .first()
            .and_then(|s| s.trim().parse::<u16>().ok());
        match announced {
            Some(p) => {
                server.adopt_port(p);
                let t0 = std::time::Instant::now();
                while !layer2::port_in_use(p) && t0.elapsed() < BIND_CONFIRM {
                    std::thread::sleep(Duration::from_millis(25));
                }
                if !layer2::port_in_use(p) {
                    let _ = server.shutdown();
                    return Err(format!(
                        "the app announced `{PROBE_LISTENING}{p}` and nothing is listening on \
                         {p} after {BIND_CONFIRM:?}. The announcement is the gate's \
                         observation of `live.port`; an announcement the process does not \
                         back is not an observation."
                    ));
                }
            }
            None => {
                let _ = server.shutdown();
                return Err(format!(
                    "the app's readiness line did not carry a port number, so `live.port` \
                     has no observed value. Saw: {:?}",
                    observe(&server.log()).listening
                ));
            }
        }
    }

    // The two probes arrive on DIFFERENT pipes: `Sky.Live listening on :N` is
    // `fmt.Printf` on stdout (live.go:4329) and the store banner is
    // `log.Printf` on stderr (live_store.go:1784-1828), each drained by its own
    // reader thread. The app emits the banner first, but "readiness on stdout"
    // says nothing about how far stderr has been drained, so reading the log
    // the instant the listening line lands is a race — one this gate would lose
    // only under load, i.e. on CI and never here.
    //
    // Settle on the banner count instead of sleeping. A run that never reaches
    // it falls through with whatever it saw, and the caller's occurrence check
    // reports the shortfall as the failure it is — this waits for the expected
    // observation, it does not manufacture one.
    let settle = std::time::Instant::now();
    let mut log = server.log();
    while ready.is_ok()
        && observe(&log).store_banner_count < STORE_BANNER_OCCURRENCES
        && settle.elapsed() < BANNER_SETTLE
    {
        std::thread::sleep(Duration::from_millis(25));
        log = server.log();
    }

    let shut = server.shutdown();
    ready?;
    shut?;
    Ok(observe(&log))
}

/// The arms a cell sets, rendered for the cell's name.
fn arms_name(env: bool, toml: bool, builder: bool) -> String {
    let mut parts = Vec::new();
    if env {
        parts.push("env");
    }
    if toml {
        parts.push("toml");
    }
    if builder {
        parts.push("builder");
    }
    if parts.is_empty() {
        "unset".to_string()
    } else {
        parts.join("+")
    }
}

// ---------------------------------------------------------------------------
// The measurement
// ---------------------------------------------------------------------------

struct Measured {
    doc: Value,
    findings: Vec<String>,
    assertions: u64,
}

/// Clauses checked once per run rather than once per cell: the probe-occurrence
/// check over all ten runs, and the baseline comparison (staleness, the
/// deferred ratchet, and the unlisted-difference scan, which stand or fall
/// together against one checked-in document).
const FIXED_CLAUSES: u64 = 2;

fn compute(root: &Path) -> Result<Measured, String> {
    let m = load_manifest(root)?;
    let sky = sky_binary(root)?;
    let mut findings: Vec<String> = Vec::new();
    let mut assertions: u64 = 0;

    // --- pre-flight: the sentinels must differ ------------------------------
    //
    // Before anything is built, because a matrix whose arms share a value
    // cannot answer the only question it exists to answer, and finding that
    // out after five builds and ten runs teaches nothing extra.
    assertions += 1;
    for st in &m.settings {
        let mut seen: BTreeMap<&str, &str> = BTreeMap::new();
        let arms: [(&str, Option<&String>); 4] = [
            ("unset", Some(&st.expect_unset)),
            ("env", st.set_env.as_ref()),
            ("toml", st.set_toml.as_ref()),
            ("builder", st.set_builder.as_ref()),
        ];
        for (arm, v) in arms {
            let Some(v) = v else { continue };
            if let Some(prev) = seen.insert(v.as_str(), arm) {
                findings.push(format!(
                    "SENTINEL — {}: arms `{prev}` and `{arm}` both use {v:?}. The cell could \
                     not say which layer won: it would compare a constant to itself.",
                    st.id
                ));
            }
        }
    }
    if !findings.is_empty() {
        return Ok(Measured {
            doc: json!({}),
            findings,
            assertions,
        });
    }

    // --- census completeness ------------------------------------------------
    //
    // Ties this instrument to stage 1's census: a setting the compiler accepts
    // or seeds must be in exactly one bucket, so coverage cannot shrink by
    // omission and a new setting cannot arrive unclassified.
    let census = read_census(root)?;
    let mut accounted: BTreeMap<String, &str> = BTreeMap::new();
    for st in &m.settings {
        for c in &st.census {
            accounted.insert(c.clone(), "setting");
        }
    }
    for c in &m.prefix.census {
        accounted.insert(c.clone(), "prefix_check");
    }
    for c in &m.deferred {
        accounted.insert(c.clone(), "deferred");
    }
    for c in &m.unobservable {
        accounted.insert(c.clone(), "unobservable");
    }
    let mut unaccounted: Vec<String> = Vec::new();
    for c in &census {
        assertions += 1;
        if !accounted.contains_key(c) {
            unaccounted.push(c.clone());
        }
    }
    if !unaccounted.is_empty() {
        findings.push(format!(
            "CENSUS — {} setting(s) the compiler accepts or seeds are in no bucket of \
             config-matrix.toml: {}. Put each in [[setting]], [[deferred]] or \
             [[unobservable]] with a reason. A setting in no bucket is one nobody decided \
             about.",
            unaccounted.len(),
            unaccounted.join(", ")
        ));
    }
    let stray: Vec<&String> = accounted
        .keys()
        .filter(|k| !census.contains(*k) && !k.is_empty())
        .collect();

    // --- pre-flight: no sentinel port is already held -----------------------
    //
    // Once per run rather than once per cell, because the run no longer knows
    // in advance which port it will observe — that is the point of observing it
    // rather than predicting it. A leftover listener on any of them would be
    // indistinguishable from the app binding it.
    let held: Vec<u16> = port_sentinels(&m)
        .into_iter()
        .filter(|p| layer2::port_in_use(*p))
        .collect();
    if !held.is_empty() {
        return Err(format!(
            "port(s) {held:?} are already in use, so a run that binds one of them cannot be \
             observed and the matrix will not guess. Two likely causes: a leftover listener \
             (8000 is the default this repo's examples bind), or a CONCURRENT \
             `config-matrix` run — the sentinel ports come from the manifest and cannot be \
             ephemeral, because for `live.port` the port IS the observed value. Free them, \
             or serialise the two runs."
        ));
    }

    // --- build the fixtures -------------------------------------------------
    let scratch = scratch_dir();
    sweep_stale_scratch();
    let _ = std::fs::remove_dir_all(&scratch);
    let mut dirs: BTreeMap<&str, PathBuf> = BTreeMap::new();
    for b in BUILDS {
        let dir = scratch.join(b.name());
        write_fixture(&dir, &sky_toml(&m, b, None), &main_sky(&m, b))?;
        build_fixture(&sky, &dir)?;
        dirs.insert(b.name(), dir);
    }
    let pfx_build = Build { toml: false, builder: false };
    let pfx_dir = scratch.join("prefix");
    write_fixture(
        &pfx_dir,
        &sky_toml(&m, pfx_build, Some(&m.prefix.prefix)),
        &main_sky(&m, pfx_build),
    )?;
    build_fixture(&sky, &pfx_dir)?;

    // --- the eight runs -----------------------------------------------------
    let mut cells: Map<String, Value> = Map::new();
    let mut subapp: Map<String, Value> = Map::new();
    // observed[(env, toml, builder)][setting id]
    let mut observed: BTreeMap<(bool, bool, bool), BTreeMap<String, String>> = BTreeMap::new();

    for b in BUILDS {
        for env_on in [false, true] {
            let mut env: Vec<(String, String)> = m
                .constants
                .iter()
                .map(|(k, v)| (format!("SKY_{k}"), v.clone()))
                .collect();
            if env_on {
                for st in &m.settings {
                    if let (Some(sfx), Some(v)) = (&st.env_suffix, &st.set_env) {
                        env.push((format!("SKY_{sfx}"), v.clone()));
                    }
                }
            }
            let dir = &dirs[b.name()];
            let o = run_fixture(dir, &env)?;

            // Probe occurrence — a probe that matched the wrong number of
            // times has not observed; it has guessed.
            if o.listening.len() != LISTENING_OCCURRENCES {
                findings.push(format!(
                    "PROBE — `listening` matched {}x in the {}/{} run, expected {}",
                    o.listening.len(),
                    b.name(),
                    if env_on { "env" } else { "noenv" },
                    LISTENING_OCCURRENCES
                ));
            }
            if o.store_banner_count != STORE_BANNER_OCCURRENCES {
                findings.push(format!(
                    "PROBE — `store_banner` matched {}x in the {}/{} run, expected {} (the \
                     app's own store and the inline console sub-app's)",
                    o.store_banner_count,
                    b.name(),
                    if env_on { "env" } else { "noenv" },
                    STORE_BANNER_OCCURRENCES
                ));
            }

            let mut row: BTreeMap<String, String> = BTreeMap::new();
            for st in &m.settings {
                // A cell exists only for arm combinations the setting supports;
                // recording a cell for an axis it does not have would duplicate
                // another cell and inflate the count with nothing.
                if (b.toml && st.toml_key.is_none())
                    || (b.builder && st.builder.is_none())
                    || (env_on && st.env_suffix.is_none())
                {
                    continue;
                }
                let v = extract(st, &o)?;
                let name = format!("{}/{}", st.id, arms_name(env_on, b.toml, b.builder));
                assertions += 1;
                cells.insert(name, Value::String(v.clone()));
                row.insert(st.id.clone(), v);
            }
            observed.insert((env_on, b.toml, b.builder), row);

            // §1.7's other reader, recorded rather than asserted: the inline
            // console sub-app resolves the same LIVE_TTL through
            // subapp_inprocess.go:400 and gets its own answer.
            if let Some(line) = o.store_banner.get("memory") {
                subapp.insert(
                    format!("{}/{}", b.name(), if env_on { "env" } else { "noenv" }),
                    Value::String(line.clone()),
                );
            }
        }
    }
    // --- per-setting verdicts ----------------------------------------------
    let mut verdicts: Map<String, Value> = Map::new();
    for st in &m.settings {
        let at = |e: bool, t: bool, bl: bool| -> Option<String> {
            observed.get(&(e, t, bl)).and_then(|r| r.get(&st.id)).cloned()
        };
        let unset = at(false, false, false);
        let env_only = st.env_suffix.as_ref().and_then(|_| at(true, false, false));
        let toml_only = st.toml_key.as_ref().and_then(|_| at(false, true, false));
        let builder_only = st.builder.as_ref().and_then(|_| at(false, false, true));

        // The declared default, checked against a running binary rather than
        // against a source literal. §7.2 asks for the literal check as a
        // "cheaper companion"; this is the same check without the brittleness,
        // because a refactor that moves the literal cannot move the observation.
        assertions += 1;
        match &unset {
            Some(v) if *v == st.expect_unset => {}
            Some(v) => findings.push(format!(
                "DEFAULT — {}: declared `expect_unset = {:?}`, a binary with no layer set \
                 observes {v:?}.",
                st.id, st.expect_unset
            )),
            None => findings.push(format!("DEFAULT — {}: no unset cell was observed", st.id)),
        }

        // Distinguishability. Distinct inputs that produce equal outputs mean
        // the cell cannot attribute the value to a layer.
        assertions += 1;
        let single: Vec<(&str, &String)> = [("unset", unset.as_ref()), ("env", env_only.as_ref()), ("toml", toml_only.as_ref())]
            .into_iter()
            .filter_map(|(n, v)| v.map(|v| (n, v)))
            .collect();
        for i in 0..single.len() {
            for j in i + 1..single.len() {
                if single[i].1 == single[j].1 {
                    findings.push(format!(
                        "INDISTINGUISHABLE — {}: arms `{}` and `{}` set different values and \
                         both observe {:?}. The cell cannot say which layer won.",
                        st.id, single[i].0, single[j].0, single[i].1
                    ));
                }
            }
        }

        // builder_reaches_runtime, verified in BOTH directions.
        if let (Some(declared), Some(b_obs), Some(u_obs)) =
            (st.builder_reaches_runtime, builder_only.as_ref(), unset.as_ref())
        {
            assertions += 1;
            let reached = b_obs != u_obs;
            if reached != declared {
                findings.push(format!(
                    "BUILDER — {}: declares `builder_reaches_runtime = {declared}`; a binary \
                     whose ONLY layer is `{} {}` observes {b_obs:?} against an unset \
                     observation of {u_obs:?}, so the builder {}. Change the declaration and \
                     list the difference, or fix the precedence — do not leave them \
                     disagreeing.",
                    st.id,
                    st.builder.as_deref().unwrap_or(""),
                    st.set_builder.as_deref().unwrap_or(""),
                    if reached { "IS reaching the runtime" } else { "is IGNORED" }
                ));
            }
            verdicts.insert(
                format!("{}.builder_reaches_runtime", st.id),
                Value::Bool(reached),
            );
        }
    }

    // --- who won, derived --------------------------------------------------
    //
    // A cell records a VALUE; WHICH LAYER supplied it is the question §3 is
    // about, and reading that off a table of durations by eye is exactly the
    // hand-count this repo forbids. Each multi-layer cell is attributed by
    // matching its observation against the single-layer observations, so the
    // answer comes out of the same measurement rather than being asserted
    // beside it. `unexplained` means the combination produced a value NO single
    // layer produces — cross-talk, and worth knowing about.
    let mut winners: Map<String, Value> = Map::new();
    for st in &m.settings {
        let single = |e: bool, t: bool, bl: bool| -> Option<String> {
            observed.get(&(e, t, bl)).and_then(|r| r.get(&st.id)).cloned()
        };
        let refs: Vec<(&str, Option<String>)> = vec![
            ("env", st.env_suffix.as_ref().and_then(|_| single(true, false, false))),
            ("toml", st.toml_key.as_ref().and_then(|_| single(false, true, false))),
            ("builder", st.builder.as_ref().and_then(|_| single(false, false, true))),
        ];
        let unset_obs = single(false, false, false);
        for (e, t, bl) in [
            (true, true, false),
            (true, false, true),
            (false, true, true),
            (true, true, true),
        ] {
            if (e && st.env_suffix.is_none())
                || (t && st.toml_key.is_none())
                || (bl && st.builder.is_none())
            {
                continue;
            }
            let Some(got) = single(e, t, bl) else { continue };
            let mut hits: Vec<&str> = Vec::new();
            for (name, on) in [("env", e), ("toml", t), ("builder", bl)] {
                if on && refs.iter().any(|(n, v)| *n == name && v.as_ref() == Some(&got)) {
                    hits.push(name);
                }
            }
            let label = if hits.len() == 1 {
                hits[0].to_string()
            } else if hits.is_empty() {
                if unset_obs.as_ref() == Some(&got) {
                    "none — every layer set, the default won".to_string()
                } else {
                    "unexplained".to_string()
                }
            } else {
                hits.join("|")
            };
            winners.insert(format!("{}/{}", st.id, arms_name(e, t, bl)), Value::String(label));
        }
    }

    // --- the prefix check ---------------------------------------------------
    for (env_name, value, expect, label) in [
        (
            m.prefix.wrong_env.clone(),
            m.prefix.wrong_value.clone(),
            m.prefix.expect_wrong.clone(),
            "default_namespace_is_inert",
        ),
        (
            m.prefix.right_env.clone(),
            m.prefix.right_value.clone(),
            m.prefix.expect_right.clone(),
            "prefixed_namespace_wins",
        ),
    ] {
        let mut env: Vec<(String, String)> = m
            .constants
            .iter()
            .map(|(k, v)| (format!("{}{k}", m.prefix.prefix), v.clone()))
            .collect();
        env.push((env_name.clone(), value.clone()));
        let o = run_fixture(&pfx_dir, &env)?;
        let got = o
            .listening
            .first()
            .cloned()
            .ok_or_else(|| format!("prefix_check/{label}: probe `listening` matched no line"))?;
        assertions += 1;
        cells.insert(format!("env.prefix/{label}"), Value::String(got.clone()));
        if got != expect {
            findings.push(format!(
                "PREFIX — {label}: with `[env] prefix = {:?}` and {env_name}={value}, the app \
                 bound :{got}, expected :{expect}.",
                m.prefix.prefix
            ));
        }
    }

    let _ = std::fs::remove_dir_all(&scratch);

    // --- the document -------------------------------------------------------
    let doc = json!({
        "_about": "Effective configuration values, OBSERVED from running binaries built by \
                   this tree's compiler. The baseline every later run is compared against \
                   (docs/tooling/config-architecture.md §7.2). Any difference fails unless a \
                   [[default-changed]] or [[moved]] row in rust/crates/xtask/config-matrix.toml \
                   lists it.",
        "_do_not_hand_edit": "Regenerate with `cargo run -p xtask -- config-matrix`.",
        "_generated_by": "xtask config-matrix",
        "cells": Value::Object(cells.clone()),
        "winners": Value::Object(winners),
        "verdicts": Value::Object(verdicts.clone()),
        "console_subapp_store": Value::Object(subapp),
        "stray_census_ids": stray.iter().map(|s| Value::String((*s).clone())).collect::<Vec<_>>(),
        "summary": {
            "covered_settings": m.settings.len(),
            "cells": cells.len(),
            "deferred_settings": m.deferred.len(),
            "deferred_stanzas": m.deferred_stanzas,
            "unobservable_settings": m.unobservable.len(),
            // The sum, ratcheted. Without it a relabel between the two buckets
            // above lowered one number and raised none, and read as progress.
            "uncovered_settings": m.deferred.len() + m.unobservable.len(),
            "census_entries": census.len(),
            "listed_differences": m.listed.len(),
        }
    });

    Ok(Measured {
        doc,
        findings,
        assertions: assertions + FIXED_CLAUSES,
    })
}

// `expected_port` USED TO LIVE HERE, and it was the defect. It re-implemented
// `resolveLivePort` inside the gate, `run_fixture` waited for the app to bind
// what it predicted, and the `live.port` row therefore compared the gate's
// constants to themselves. It is gone: the port is observed at consumption,
// like the other three settings, and the only thing the manifest's port values
// are still used for is the preflight in [`port_sentinels`] — a check that a
// stale listener is not holding a port the run might need.

/// The census: every `sky.toml` key the compiler accepts and every env suffix
/// it seeds, as stage 1 measured them.
fn read_census(root: &Path) -> Result<BTreeSet<String>, String> {
    let p = root.join("docs/coverage/config-surface.json");
    let text = std::fs::read_to_string(&p).map_err(|e| {
        format!(
            "cannot read {} ({e}) — run `cargo run -p xtask -- config-surface` first. \
             The matrix's coverage claim is stated against that census; without it there is \
             nothing to be complete with respect to.",
            p.display()
        )
    })?;
    let v: Value = serde_json::from_str(&text).map_err(|e| format!("{}: {e}", p.display()))?;
    let mut out = BTreeSet::new();
    for field in ["accepted_sky_toml_keys", "seeded_suffixes"] {
        let arr = v
            .get(field)
            .and_then(Value::as_array)
            .ok_or_else(|| format!("{}: no `{field}` array", p.display()))?;
        for e in arr {
            if let Some(s) = e.as_str() {
                out.insert(s.to_string());
            }
        }
    }
    Ok(out)
}

/// Where the fixtures are generated: OUTSIDE the repo, and keyed by pid.
///
/// Outside, so a fixture can never be mistaken for tracked content. Keyed by
/// pid, because several agents run this repo's gates at once and a fixed path
/// would have one run's `sky build` land in another's tree — worktree isolation
/// protects the source and does nothing for a hard-coded scratch path.
fn scratch_dir() -> PathBuf {
    std::env::temp_dir().join(format!("{SCRATCH_PREFIX}{}", std::process::id()))
}

const SCRATCH_PREFIX: &str = "sky-config-matrix-";

/// Remove fixtures left by a run that failed before its own cleanup.
///
/// Only for pids that are gone — a live sibling's scratch is left alone, for
/// the same reason the end-of-mission `pkill` patterns must be scoped: killing
/// a concurrent agent's in-flight work produces a red indistinguishable from a
/// real regression, and it is the victim who investigates.
fn sweep_stale_scratch() {
    let Ok(entries) = std::fs::read_dir(std::env::temp_dir()) else {
        return;
    };
    for e in entries.flatten() {
        let name = e.file_name();
        let Some(name) = name.to_str() else { continue };
        let Some(pid) = name.strip_prefix(SCRATCH_PREFIX) else {
            continue;
        };
        let Ok(pid) = pid.parse::<i32>() else { continue };
        if pid == std::process::id() as i32 {
            continue;
        }
        #[cfg(unix)]
        {
            use nix::sys::signal::kill;
            use nix::unistd::Pid;
            if kill(Pid::from_raw(pid), None).is_ok() {
                continue; // still running — not ours to remove
            }
        }
        let _ = std::fs::remove_dir_all(e.path());
    }
}

fn manifest_path(root: &Path) -> PathBuf {
    root.join("rust/crates/xtask/config-matrix.toml")
}

fn out_path(root: &Path) -> PathBuf {
    root.join("docs/coverage/config-matrix.json")
}

// ---------------------------------------------------------------------------
// Comparison against the baseline
// ---------------------------------------------------------------------------

/// Cell-by-cell difference against the checked-in baseline, minus anything a
/// `[[default-changed]]` / `[[moved]]` row lists with matching endpoints.
///
/// Endpoints are matched exactly, so one row cannot silently authorise a later
/// different change to the same cell — the same rule `[[config-surface-rise]]`
/// applies to a metric.
fn unlisted_differences(base: &Value, cur: &Value, listed: &[Listed]) -> Vec<String> {
    let mut out = Vec::new();
    let (b, c) = (base.get("cells"), cur.get("cells"));
    let (Some(Value::Object(b)), Some(Value::Object(c))) = (b, c) else {
        return vec!["baseline has no `cells` object — regenerate it".into()];
    };
    for (k, cv) in c {
        match b.get(k) {
            None => out.push(format!(
                "NEW CELL `{k}` = {cv} — a cell the baseline does not have. Regenerate."
            )),
            Some(bv) if bv != cv => {
                let (from, to) = (render(bv), render(cv));
                // Only `[[default-changed]]` authorises a value difference;
                // `[[moved]]` asserts the value did NOT move, so it can never
                // excuse one.
                let listed_here = listed.iter().any(|l| {
                    l.kind == "default-changed" && l.cell == *k && l.from == from && l.to == to
                });
                if !listed_here {
                    out.push(format!(
                        "UNLISTED — cell `{k}`: {from:?} -> {to:?}. Either this is a \
                         behaviour change nobody decided on, or it needs a \
                         [[default-changed]] row in config-matrix.toml with from/to/reason/\
                         commit."
                    ));
                }
            }
            _ => {}
        }
    }
    for k in b.keys() {
        if !c.contains_key(k) {
            out.push(format!(
                "LOST CELL `{k}` — a cell that used to be measured no longer is. A cell that \
                 stops being computed cannot be seen to regress."
            ));
        }
    }
    // The verdicts move with the cells; a flipped builder verdict must be as
    // loud as a changed value — and, like a changed value, must be LISTABLE.
    //
    // Stage 2 compared the whole `verdicts` object and emitted one finding
    // ending "list it", with no way to do so: no stanza named a verdict, so
    // the check could never be satisfied. A legitimate fix could therefore
    // never make the gate green, which makes the instruction unfollowable and
    // the check a wall rather than a ratchet. Fixing `withTtl` is the first
    // event that exercised it.
    //
    // Compared per key now, and a `[[default-changed]]` whose `cell` is the
    // verdict key authorises exactly that one flip. Both directions still
    // fail unlisted: claiming a live builder is dead hides a regression as
    // well as the reverse, which is the property stage 2 was protecting.
    // The console sub-app's store line, compared rather than merely recorded.
    //
    // Stage 2 wrote this table with the comment "recorded rather than
    // asserted" — §1.7's third LIVE_TTL reader, captured for information. That
    // left a hole exactly the width of the thing this gate exists to catch:
    // stage 3 moved two of these values and NOTHING went red, because no
    // comparison read them. They are a second consumer resolving the same
    // setting, which is the entire reason they were worth recording; a value
    // worth recording is worth failing on.
    let empty = Map::new();
    let bs = base.get("console_subapp_store").and_then(Value::as_object).unwrap_or(&empty);
    let cs = cur.get("console_subapp_store").and_then(Value::as_object).unwrap_or(&empty);
    for (k, cval) in cs {
        let cell = format!("console_subapp_store/{k}");
        match bs.get(k) {
            None => out.push(format!(
                "NEW SUB-APP CELL `{cell}` = {cval} — not in the baseline. Regenerate."
            )),
            Some(bval) if bval != cval => {
                let (from, to) = (render(bval), render(cval));
                let listed_here = listed.iter().any(|l| {
                    l.kind == "default-changed" && l.cell == cell && l.from == from && l.to == to
                });
                if !listed_here {
                    out.push(format!(
                        "UNLISTED — sub-app cell `{cell}`: {from:?} -> {to:?}. The inline \
                         console resolves the same LIVE_TTL through subapp_inprocess.go and \
                         gets its own answer; a change to it is a change to a real consumer. \
                         Needs a [[default-changed]] row with from/to/reason/commit."
                    ));
                }
            }
            _ => {}
        }
    }
    for k in bs.keys() {
        if !cs.contains_key(k) {
            out.push(format!(
                "LOST SUB-APP CELL `console_subapp_store/{k}` — it stopped being measured."
            ));
        }
    }

    let bv = base.get("verdicts").and_then(Value::as_object).unwrap_or(&empty);
    let cv = cur.get("verdicts").and_then(Value::as_object).unwrap_or(&empty);
    for (k, cval) in cv {
        let before = bv.get(k);
        if before == Some(cval) {
            continue;
        }
        let from = before.map(render).unwrap_or_else(|| "<absent>".into());
        let to = render(cval);
        let listed_here = listed.iter().any(|l| {
            l.kind == "default-changed" && l.cell == *k && l.from == from && l.to == to
        });
        if !listed_here {
            out.push(format!(
                "VERDICT `{k}`: {from} -> {to}. A builder that started or stopped reaching \
                 the runtime is exactly the §7.3 class. Authorise it with a \
                 [[default-changed]] row whose `cell` is `{k}`, from = {from:?}, to = {to:?}."
            ));
        }
    }
    for k in bv.keys() {
        if !cv.contains_key(k) {
            out.push(format!(
                "LOST VERDICT `{k}` — a verdict that used to be computed no longer is. A \
                 verdict that stops being computed cannot be seen to regress."
            ));
        }
    }
    out
}

fn render(v: &Value) -> String {
    v.as_str().map(str::to_string).unwrap_or_else(|| v.to_string())
}

/// Counts that may FALL (progress) and may not RISE (regression).
///
/// # Why all three, and not just `deferred_settings`
///
/// One ratcheted metric was RELABEL-EVADABLE. `deferred_settings` was the only
/// number under a ratchet, so moving four ids from `[[deferred]]` to
/// `[[unobservable]]` — an unratcheted bucket — with identical reason text read
/// `deferred_settings: 41 -> 37`, i.e. AS PROGRESS. Nothing was covered; the
/// ids changed which paragraph they sat under. The only objection the run
/// emitted was "regenerate", which is unauthorised and unlogged.
///
/// So the sum is ratcheted (a move between buckets cannot lower it) AND each
/// bucket individually (a move RAISES the destination and fails there). The
/// only way past is a `[[bucket-change]]` stanza that names the metric, the
/// endpoints, a reason and a commit — the same accounting `[[default-changed]]`
/// already demands of an observed value.
const RATCHETED: &[(&str, &str)] = &[
    (
        "deferred_settings",
        "a setting moved into [[deferred]] is one the matrix stopped protecting; stage 3 moves \
         settings, and the bucket must not become the place they go to avoid being measured",
    ),
    (
        "unobservable_settings",
        "[[unobservable]] is the stronger claim — that no effective value EXISTS to compare — \
         so it must be at least as hard to enter as [[deferred]]. Unratcheted, it was the \
         drain a deferred id could be relabelled into while the deferred count fell and read \
         as progress",
    ),
    (
        "uncovered_settings",
        "the total the matrix does NOT cover, deferred plus unobservable. Ratcheting the sum \
         is what makes a bucket-to-bucket relabel a no-op instead of an improvement",
    ),
];

/// An authorised rise in a [`RATCHETED`] metric — `[[bucket-change]]`.
struct BucketChange {
    metric: String,
    from: u64,
    to: u64,
}

fn ratchet(baseline: Option<&Value>, current: &Value, allowed: &[BucketChange]) -> Vec<String> {
    let Some(base) = baseline else {
        return Vec::new();
    };
    let mut out = Vec::new();
    for (metric, why) in RATCHETED {
        let b = base.get("summary").and_then(|s| s.get(metric)).and_then(Value::as_u64);
        let c = current.get("summary").and_then(|s| s.get(metric)).and_then(Value::as_u64);
        match (b, c) {
            (Some(b), Some(c)) if c > b => {
                let accounted = allowed
                    .iter()
                    .any(|a| a.metric == *metric && a.from == b && a.to == c);
                if !accounted {
                    out.push(format!(
                        "RATCHET — `{metric}` rose {b} -> {c}. {why}. If this rise is \
                         deliberate, account for it with a [[bucket-change]] stanza in \
                         rust/crates/xtask/config-matrix.toml naming `metric`, `from = \
                         \"{b}\"`, `to = \"{c}\"`, a `reason` and a `commit`."
                    ));
                }
            }
            (Some(_), None) => out.push(format!(
                "RATCHET — `{metric}` disappeared from the recomputed document. A metric that \
                 stops being computed cannot be seen to regress."
            )),
            // A metric absent from the BASELINE needs no arm of its own: the
            // recomputed document always carries every RATCHETED metric, so a
            // baseline without one differs from it and the STALE clause fires.
            // Failing here as well would deadlock the regeneration that fixes
            // it — a new metric could never acquire its first baseline.
            _ => {}
        }
    }
    out
}

// ---------------------------------------------------------------------------
// Entry points
// ---------------------------------------------------------------------------

/// `xtask config-matrix [--check]`.
///
/// No flag: measure, report, compare, and WRITE the baseline. `--check`:
/// measure, compare, write nothing — including the staleness clause, so a
/// tree whose baseline was never regenerated fails rather than passes.
pub fn run(args: &[String], repo_root: &Path) -> i32 {
    let check_only = args.iter().any(|a| a == "--check");
    let m = match compute(repo_root) {
        Ok(m) => m,
        Err(e) => {
            eprintln!("config-matrix: {e}");
            return 1;
        }
    };
    let baseline = std::fs::read_to_string(out_path(repo_root))
        .ok()
        .and_then(|t| serde_json::from_str::<Value>(&t).ok());

    let mut fails = m.findings.clone();
    let manifest = load_manifest(repo_root);
    let allowed: &[BucketChange] = match &manifest {
        Ok(mf) => &mf.bucket_changes,
        Err(_) => &[],
    };
    fails.extend(ratchet(baseline.as_ref(), &m.doc, allowed));
    if let (Some(base), Ok(mf)) = (baseline.as_ref(), &manifest) {
        fails.extend(unlisted_differences(base, &m.doc, &mf.listed));
    }

    println!("config-matrix — {} assertions", m.assertions);
    if let Some(s) = m.doc.get("summary") {
        println!("{}", serde_json::to_string_pretty(s).unwrap_or_default());
    }
    if let Some(Value::Object(v)) = m.doc.get("verdicts") {
        for (k, val) in v {
            println!("  {k} = {val}");
        }
    }

    if check_only {
        if baseline.as_ref() != Some(&m.doc) && fails.is_empty() {
            fails.push(
                "STALE — docs/coverage/config-matrix.json does not match this tree. \
                 Regenerate with `cargo run -p xtask -- config-matrix`."
                    .to_string(),
            );
        }
    } else if fails.is_empty() {
        let text = serde_json::to_string_pretty(&m.doc).unwrap_or_default() + "\n";
        if let Err(e) = std::fs::write(out_path(repo_root), text) {
            eprintln!("config-matrix: cannot write baseline: {e}");
            return 1;
        }
        println!("wrote {}", out_path(repo_root).display());
    }

    if fails.is_empty() {
        println!("config-matrix: OK");
        0
    } else {
        for f in &fails {
            eprintln!("  {f}");
        }
        eprintln!("config-matrix: {} finding(s)", fails.len());
        1
    }
}

/// The harness-gate face: measure, compare, verify — writing nothing.
pub fn check_body(repo_root: &Path) -> (bool, u64, String) {
    let m = match compute(repo_root) {
        Ok(m) => m,
        Err(e) => return (false, 0, format!("config-matrix could not run: {e}")),
    };
    let baseline = std::fs::read_to_string(out_path(repo_root))
        .ok()
        .and_then(|t| serde_json::from_str::<Value>(&t).ok());
    let mut fails = m.findings.clone();
    let manifest = load_manifest(repo_root);
    fails.extend(ratchet(
        baseline.as_ref(),
        &m.doc,
        manifest.as_ref().map(|mf| mf.bucket_changes.as_slice()).unwrap_or(&[]),
    ));
    match (&baseline, manifest) {
        (Some(base), Ok(mf)) => fails.extend(unlisted_differences(base, &m.doc, &mf.listed)),
        (None, _) => fails.push(
            "no docs/coverage/config-matrix.json — the baseline every cell is compared \
             against is missing, so nothing was compared."
                .into(),
        ),
        (_, Err(e)) => fails.push(e),
    }
    if fails.is_empty() && baseline.as_ref() != Some(&m.doc) {
        fails.push(
            "STALE — docs/coverage/config-matrix.json does not match this tree.".to_string(),
        );
    }
    let detail = if fails.is_empty() {
        format!(
            "{} cells observed from running binaries; every one matches the baseline",
            m.doc
                .get("summary")
                .and_then(|s| s.get("cells"))
                .and_then(Value::as_u64)
                .unwrap_or(0)
        )
    } else {
        fails.join(" | ")
    };
    (fails.is_empty(), m.assertions, detail)
}

// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;

    fn root() -> PathBuf {
        let mut d = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
        while !d.join("examples").is_dir() {
            d = d.parent().expect("repo root").to_path_buf();
        }
        d
    }

    #[test]
    fn the_manifest_parses_and_declares_a_matrix() {
        let m = load_manifest(&root()).expect("manifest parses");
        assert!(!m.settings.is_empty(), "no settings");
        assert!(!m.constants.is_empty(), "no harness constants declared");
    }

    /// The guard that stops the whole gate being the failure it exists to
    /// catch. Two arms sharing a sentinel would compare a constant to itself.
    #[test]
    fn every_settings_arms_use_distinct_sentinels() {
        let m = load_manifest(&root()).expect("manifest parses");
        for st in &m.settings {
            let mut seen: BTreeSet<&str> = BTreeSet::new();
            seen.insert(st.expect_unset.as_str());
            for v in [&st.set_env, &st.set_toml, &st.set_builder].into_iter().flatten() {
                assert!(
                    seen.insert(v.as_str()),
                    "{}: two arms share the sentinel {v:?}",
                    st.id
                );
            }
        }
    }

    /// The declaration this gate exists to make checkable, pinned so a careless
    /// edit to the manifest cannot quietly assert a live builder is dead — or
    /// a dead one live — without the observation that would contradict it.
    ///
    /// This asserted `Some(false)` for `live.ttl` through stages 1 and 2, and
    /// its message said that if stage 3 fixed it, the observed cells moved
    /// too and both belonged in one commit with a `[[default-changed]]`.
    /// Stage 3 fixed it, the cells moved, and they are listed. The assertion
    /// is now the property the stage set out to establish, stated positively
    /// over ALL settings — so a regression in any one of them fails here and
    /// not only in the end-to-end run.
    #[test]
    fn every_settings_builder_reaches_the_runtime() {
        let m = load_manifest(&root()).expect("manifest parses");
        let mut checked = 0;
        for st in &m.settings {
            if st.builder.is_none() {
                continue;
            }
            checked += 1;
            assert_eq!(
                st.builder_reaches_runtime,
                Some(true),
                "{}: declares its builder does NOT reach the runtime. Stage 3 made the \
                 four settings share one precedence rule (operator env > builder > seeded \
                 default > fallback), under which every withX wins against a default it \
                 used to lose to. A `false` here is either a regression or a new setting \
                 that skipped the shared resolver in live_config_precedence.go",
                st.id
            );
        }
        assert_eq!(
            checked, 4,
            "expected 4 settings with builders; a loop that checks nothing passes"
        );
    }

    #[test]
    fn every_census_entry_is_in_exactly_one_bucket() {
        let root = root();
        let m = load_manifest(&root).expect("manifest parses");
        let census = read_census(&root).expect("census readable");
        let mut counts: BTreeMap<&str, usize> = BTreeMap::new();
        for st in &m.settings {
            for c in &st.census {
                *counts.entry(c.as_str()).or_default() += 1;
            }
        }
        for c in m.prefix.census.iter().chain(&m.deferred).chain(&m.unobservable) {
            *counts.entry(c.as_str()).or_default() += 1;
        }
        for c in &census {
            let n = counts.get(c.as_str()).copied().unwrap_or(0);
            assert_eq!(n, 1, "census entry {c:?} is in {n} buckets, must be exactly 1");
        }
    }

    #[test]
    fn a_probe_that_matches_nothing_is_an_error_not_an_absence() {
        let st = Setting {
            id: "x".into(),
            census: vec![],
            toml_section: None,
            toml_key: None,
            env_suffix: None,
            builder: None,
            builder_kind: String::new(),
            probe: "store_banner".into(),
            probe_key: Some("sqlite".into()),
            field_after: "ttl=".into(),
            field_until: ",)".into(),
            expect_unset: "30m0s".into(),
            set_env: None,
            set_toml: None,
            set_builder: None,
            builder_reaches_runtime: None,
        };
        let empty = observe("nothing to see here\n");
        assert!(extract(&st, &empty).is_err(), "a missing probe must be an error");

        // Present probe, absent field — a legitimate value, distinct from the
        // above, which is what stops `-` being used to paper over a broken probe.
        let present = observe("[sky.live] session store: sqlite @ a.db (idleEvict=off)\n");
        assert_eq!(extract(&st, &present).as_deref(), Ok("-"));
    }

    #[test]
    fn fields_are_extracted_from_the_consumers_own_line() {
        let o = observe(
            "2026/01/01 [sky.live] session store: sqlite @ cfgmx_env.db (ttl=37m0s, idleEvict=6m0s)\n\
             2026/01/01 [sky.live] session store: memory (ttl=30m0s)\n\
             Sky.Live listening on :19811\n",
        );
        assert_eq!(o.store_banner_count, 2);
        assert_eq!(o.listening, vec!["19811".to_string()]);
        let mk = |after: &str, until: &str, key: &str| Setting {
            id: "x".into(),
            census: vec![],
            toml_section: None,
            toml_key: None,
            env_suffix: None,
            builder: None,
            builder_kind: String::new(),
            probe: "store_banner".into(),
            probe_key: Some(key.into()),
            field_after: after.into(),
            field_until: until.into(),
            expect_unset: String::new(),
            set_env: None,
            set_toml: None,
            set_builder: None,
            builder_reaches_runtime: None,
        };
        assert_eq!(extract(&mk("ttl=", ",)", "sqlite"), &o).unwrap(), "37m0s");
        assert_eq!(extract(&mk("@ ", " ", "sqlite"), &o).unwrap(), "cfgmx_env.db");
        assert_eq!(extract(&mk("idleEvict=", ",)", "sqlite"), &o).unwrap(), "6m0s");
        // The console sub-app's own store is keyed separately, so print order
        // can never make one reader's answer stand in for the other's (§1.7).
        assert_eq!(extract(&mk("ttl=", ",)", "memory"), &o).unwrap(), "30m0s");
    }

    /// The verdict path, which through stage 2 could fail but never pass: the
    /// finding said "list it" and no stanza could name a verdict, so a
    /// legitimate fix left the gate permanently red. Both halves are asserted
    /// here — an unlisted flip still fails, and a listed one is authorised —
    /// because a check that cannot be satisfied and a check that cannot fail
    /// are both useless, in opposite directions.
    #[test]
    fn a_verdict_flip_must_be_listed_and_can_be() {
        // A `cells` object on both sides, identical: without one the function
        // short-circuits on "baseline has no `cells` object", which would make
        // the first assertion below pass for a reason that has nothing to do
        // with verdicts.
        let base = json!({
            "cells": {"live.ttl/unset": "30m0s"},
            "verdicts": {"live.ttl.builder_reaches_runtime": false},
        });
        let cur = json!({
            "cells": {"live.ttl/unset": "30m0s"},
            "verdicts": {"live.ttl.builder_reaches_runtime": true},
        });
        assert_eq!(unlisted_differences(&base, &cur, &[]).len(), 1);
        let listed = [Listed {
            kind: "default-changed".into(),
            cell: "live.ttl.builder_reaches_runtime".into(),
            from: "false".into(),
            to: "true".into(),
        }];
        assert!(unlisted_differences(&base, &cur, &listed).is_empty());
        // Both directions. A listing that authorised false->true must not
        // silently authorise the regression back the other way.
        assert_eq!(unlisted_differences(&cur, &base, &listed).len(), 1);
    }

    /// The console sub-app's line is a second consumer of the same LIVE_TTL,
    /// and stage 2 recorded it without comparing it. Stage 3 moved two of its
    /// values and nothing went red — so the comparison is now asserted here
    /// as well as end to end.
    #[test]
    fn a_moved_subapp_cell_must_be_listed() {
        let base = json!({
            "cells": {"live.ttl/unset": "30m0s"},
            "console_subapp_store": {"toml/noenv": "memory (ttl=38m0s)"},
        });
        let cur = json!({
            "cells": {"live.ttl/unset": "30m0s"},
            "console_subapp_store": {"toml/noenv": "memory (ttl=30m0s)"},
        });
        assert_eq!(unlisted_differences(&base, &cur, &[]).len(), 1);
        let listed = [Listed {
            kind: "default-changed".into(),
            cell: "console_subapp_store/toml/noenv".into(),
            from: "memory (ttl=38m0s)".into(),
            to: "memory (ttl=30m0s)".into(),
        }];
        assert!(unlisted_differences(&base, &cur, &listed).is_empty());
    }

    /// A verdict that stops being computed cannot be seen to regress, so its
    /// disappearance is itself a finding rather than a quiet pass.
    #[test]
    fn a_lost_verdict_is_a_finding() {
        let base = json!({
            "cells": {"live.ttl/unset": "30m0s"},
            "verdicts": {"live.ttl.builder_reaches_runtime": true},
        });
        let cur = json!({"cells": {"live.ttl/unset": "30m0s"}, "verdicts": {}});
        assert_eq!(unlisted_differences(&base, &cur, &[]).len(), 1);
    }

    #[test]
    fn an_unlisted_difference_fails_and_a_listed_one_does_not() {
        let base = json!({"cells": {"live.ttl/unset": "30m0s"}});
        let cur = json!({"cells": {"live.ttl/unset": "41m0s"}});
        assert_eq!(unlisted_differences(&base, &cur, &[]).len(), 1);
        let listed = [Listed {
            kind: "default-changed".into(),
            cell: "live.ttl/unset".into(),
            from: "30m0s".into(),
            to: "41m0s".into(),
        }];
        assert!(unlisted_differences(&base, &cur, &listed).is_empty());
        // Endpoints are exact: the same row must not authorise a later,
        // different change to the same cell.
        let later = json!({"cells": {"live.ttl/unset": "9m0s"}});
        assert_eq!(unlisted_differences(&base, &later, &listed).len(), 1);
    }

    #[test]
    fn a_lost_cell_is_a_failure() {
        let base = json!({"cells": {"a": "1", "b": "2"}});
        let cur = json!({"cells": {"a": "1"}});
        let d = unlisted_differences(&base, &cur, &[]);
        assert_eq!(d.len(), 1, "{d:?}");
        assert!(d[0].contains("LOST CELL"), "{d:?}");
    }

    #[test]
    fn the_toml_fixture_quotes_what_must_be_quoted() {
        let m = load_manifest(&root()).expect("manifest parses");
        let t = sky_toml(&m, Build { toml: true, builder: false }, None);
        assert!(t.contains("port = 19812"), "a numeric key must be bare:\n{t}");
        assert!(t.contains("ttl = \"38m\""), "a duration must be quoted:\n{t}");
        // And the base fixture must set nothing, or the unset arm is not unset.
        let base = sky_toml(&m, Build { toml: false, builder: false }, None);
        assert!(!base.contains("[live]"), "the base fixture must set no keys:\n{base}");
    }

    #[test]
    fn the_builder_fixture_calls_every_declared_builder() {
        let m = load_manifest(&root()).expect("manifest parses");
        let src = main_sky(&m, Build { toml: false, builder: true });
        for st in &m.settings {
            if let (Some(call), Some(v)) = (&st.builder, &st.set_builder) {
                assert!(src.contains(call.as_str()), "{call} missing from the fixture:\n{src}");
                assert!(src.contains(v.as_str()), "{v} missing from the fixture:\n{src}");
            }
        }
        let base = main_sky(&m, Build { toml: false, builder: false });
        assert!(!base.contains("|>"), "the base fixture must call no builder:\n{base}");
    }

    #[test]
    fn comments_are_stripped_outside_strings_only() {
        assert_eq!(strip_comment(r#"a = "x # y" # z"#).trim(), r#"a = "x # y""#);
        assert_eq!(strip_comment("# whole line").trim(), "");
        assert_eq!(strip_comment("a = 1").trim(), "a = 1");
    }

    #[test]
    fn escaped_quotes_survive_the_parser() {
        let v = parse_value(r#""he said \"hi\"""#).unwrap();
        assert_eq!(v.as_str(), Some(r#"he said "hi""#));
        let l = parse_value(r#"["a", "b,c"]"#).unwrap();
        assert_eq!(l.as_list().unwrap(), ["a".to_string(), "b,c".to_string()]);
    }

    /// `expected_port_follows_resolve_live_port` USED TO BE HERE, asserting
    /// that the gate's re-implementation of `resolveLivePort` returned the
    /// manifest's own sentinels — the self-comparison in unit-test form. Both
    /// it and the function are gone; the port is observed at consumption.
    ///
    /// What replaces it is the property the manifest's port values are still
    /// FOR: the preflight must cover every port any arm could make the app
    /// bind, because the run no longer predicts which one that will be. A
    /// sentinel missing from this set is a port a stale listener could hold
    /// while the run blamed the value.
    #[test]
    fn the_port_preflight_covers_every_arm_of_every_listening_setting() {
        let m = load_manifest(&root()).expect("manifest parses");
        let got = port_sentinels(&m);
        let mut expected: Vec<u16> = Vec::new();
        for st in &m.settings {
            if st.probe != "listening" {
                continue;
            }
            for v in [Some(&st.expect_unset), st.set_env.as_ref(), st.set_toml.as_ref(), st.set_builder.as_ref()]
                .into_iter()
                .flatten()
            {
                let p: u16 = v.parse().unwrap_or_else(|_| panic!("{}: {v:?} is not a port", st.id));
                assert!(got.contains(&p), "{}: sentinel {p} is not preflighted", st.id);
                if !expected.contains(&p) {
                    expected.push(p);
                }
            }
        }
        for v in [&m.prefix.expect_wrong, &m.prefix.expect_right, &m.prefix.wrong_value, &m.prefix.right_value] {
            let p: u16 = v.parse().expect("prefix_check port");
            assert!(got.contains(&p), "prefix_check sentinel {p} is not preflighted");
            if !expected.contains(&p) {
                expected.push(p);
            }
        }
        // Exact, not `>=`: a preflight that grew a port nobody declared would
        // be checking something the run cannot produce.
        assert_eq!(
            got.len(),
            expected.len(),
            "preflight set {got:?} differs from the declared sentinels {expected:?}"
        );
        assert!(!expected.is_empty(), "no sentinels — the loop asserted nothing");
    }
}

//! How many PostgreSQL backends one Sky app process demands — and what a
//! cluster's `max_connections` therefore has to be.
//!
//! # Why this is its own module
//!
//! Three places size a PostgreSQL server: the embedded cluster
//! (`runtime-go/rt/pg_embed_conf.go`), the shared production cluster
//! (`db_shared::tuning_block`) and the development cluster
//! (`db_cluster::sky_conf_block`). All three had independently derived the
//! number from "the app's pool", and all three were wrong in the same way,
//! because a Sky app process does not open one pool. It opens FOUR:
//!
//!   * the app's own `Db.connect` pool, and
//!   * one pool per entry in [`AUX_POOL_CONSUMERS`] — the pools the RUNTIME
//!     opens for its own purposes.
//!
//! Counting only the first left every one of those clusters short across a
//! band of ordinary machines: at 8 cores the process demands 60 backends while
//! the dev cluster offered 50, of which 3 are reserved for the superuser. The
//! app exhausted the database it had just started, under load, having
//! configured nothing to deserve it.
//!
//! The arithmetic therefore lives in ONE place, and the sizing functions read
//! it. Adding a fifth runtime pool is a change to [`AUX_POOL_CONSUMERS`], and
//! every cluster's ceiling moves with it.
//!
//! # The arithmetic is a function of the APP POOL, not of `cpus`
//!
//! Every function here used to take `cpus`. That framing hid the second defect
//! in the same family: the app's pool is `deployment-aware default THEN the
//! `<PREFIX>_DB_MAX_OPEN_CONNS` / `sky.toml [database] maxOpenConns` override`,
//! and the aux pools are shares OF THE APP POOL — so nothing here is a function
//! of `cpus` once the operator has touched the documented knob. At
//! `maxOpenConns = 64` on one core the process opens 92 backends; every sizing
//! in this module said 20.
//!
//! So the input is [`PoolInputs`]: the machine AND the knob. `cpus` now enters
//! the arithmetic in exactly one place — the default the resolver starts from.
//!
//! # How this side learns the knob
//!
//! It resolves the same sources, in the same precedence, that the process it is
//! about to launch will resolve (`runtime-go/rt/dotenv.go`,
//! `runtime-go/rt/env_prefix.go`):
//!
//!   1. the CLI's own process environment — `sky db start`, `sky run` and
//!      `sky db provision` launch (or are launched beside) the app, so a knob
//!      exported in that shell is the knob the app will read;
//!   2. the project's `.env`, which `rt`'s `init()` loads without overriding
//!      anything already in the environment;
//!   3. `sky.toml [database] maxOpenConns`, which the compiler emits as
//!      `rt.SetSkyDefault("DB_MAX_OPEN_CONNS", …)` — a DEFAULT, so the two
//!      above outrank it.
//!
//! The prefix is `sky.toml [env] prefix` (trailing `_` trimmed, empty → `SKY`),
//! because that is the only place `rt.SetEnvPrefix` can come from.
//!
//! What this cannot see is a knob set for the app's process alone and not for
//! the provisioning command — a systemd unit's `Environment=` on a shared host,
//! say. That case is what `sky db provision --shared --max-connections` is for,
//! and it is stated rather than guessed.
//!
//! # The Go side is the original, and a FIXTURE ties the two together
//!
//! `runtime-go/rt/db_pool.go` sizes the pools themselves, and this module
//! mirrors it. Two implementations of one number is one too many, and here it is
//! unavoidable — the Go runtime cannot be called from the Rust CLI that
//! writes a cluster's `postgresql.conf` before any Go has run.
//!
//! What is avoidable is mirroring by ASSERTION. This module used to open with a
//! sentence claiming it mirrored the Go "exactly"; the claim was prose, nothing
//! checked it, and the two did diverge. They are now tied by
//! `runtime-go/rt/testdata/db_pool_sizing.tsv`: the Go gate
//! (`TestThePoolSizingFixtureMatchesTheGoArithmetic`) and the Rust gate
//! (`the_fixture_matches_the_go_arithmetic`) each assert their own
//! implementation reproduces every row of it. A change on either side that the
//! other has not followed turns one of the two red.
//!
//! That fixture swept `cpus` alone for one release, which is how the knob defect
//! survived it: both languages reproduced each other's `f(cpus)` faithfully and
//! both were wrong together. It now sweeps `cpus` × the knob, including the
//! values that are not plain positive integers.

use std::path::Path;

/// The env suffix the app's pool ceiling is read from.
const MAX_OPEN_CONNS_SUFFIX: &str = "DB_MAX_OPEN_CONNS";

/// The inputs the pool arithmetic actually reads.
///
/// Not `cpus` alone. See the module header: once the operator sets the
/// documented pool knob, none of the four terms is a function of the machine.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PoolInputs {
    /// Cores on the host the app will run on.
    pub cpus: u32,
    /// The raw `<PREFIX>_DB_MAX_OPEN_CONNS` the process will read, exactly as
    /// the environment / `.env` / `sky.toml` delivers it, or `None` when the
    /// knob is unset.
    ///
    /// Raw, not pre-parsed, because the parse is part of the arithmetic the Go
    /// runtime performs and therefore part of what this module must reproduce:
    /// whitespace is trimmed, an unparseable value falls back to the default
    /// with a warning, and zero or negative means UNLIMITED.
    pub app_max_open: Option<String>,
}

impl PoolInputs {
    /// The inputs with the knob unset — what sky DERIVES from the machine.
    pub fn derived(cpus: u32) -> Self {
        PoolInputs { cpus, app_max_open: None }
    }

    /// The inputs a project's app process will see.
    ///
    /// `project` is the directory holding `sky.toml`; pass `None` when sizing a
    /// cluster that serves no single project (`sky db provision --shared`),
    /// where only the process environment is knowable.
    pub fn resolve(cpus: u32, project: Option<&Path>) -> Self {
        PoolInputs { cpus, app_max_open: resolve_max_open_conns(project) }
    }
}

/// Read the pool knob the way the app's own runtime will.
fn resolve_max_open_conns(project: Option<&Path>) -> Option<String> {
    let toml = project.map(|p| read_text(&p.join("sky.toml"))).unwrap_or_default();
    let name = format!("{}_{MAX_OPEN_CONNS_SUFFIX}", env_prefix(&toml));

    // 1. The process environment. PRESENCE decides, not emptiness: `rt`'s
    //    `SetEnvDefault` skips a variable that is set at all, so an explicitly
    //    empty one suppresses the sky.toml default and then reads as unset.
    if let Ok(v) = std::env::var(&name) {
        return Some(v);
    }
    // 2. The project's `.env`, loaded by `rt`'s init() without overriding the
    //    environment above.
    if let Some(p) = project {
        if let Some(v) = dotenv_value(&read_text(&p.join(".env")), &name) {
            return Some(v);
        }
    }
    // 3. `sky.toml [database] maxOpenConns`, emitted as a SetSkyDefault.
    toml_value(&toml, "database", "maxOpenConns")
}

fn read_text(p: &Path) -> String {
    std::fs::read_to_string(p).unwrap_or_default()
}

/// `[env] prefix`, trailing `_` trimmed, empty → `SKY` — `rt::SetEnvPrefix`'s
/// own rule.
fn env_prefix(toml: &str) -> String {
    match toml_value(toml, "env", "prefix") {
        Some(p) => {
            let p = p.trim_end_matches('_');
            if p.is_empty() { "SKY".to_string() } else { p.to_string() }
        }
        None => "SKY".to_string(),
    }
}

/// A scalar out of a `[section] key = value` line.
///
/// Deliberately the same tolerant scan `project::build`'s `read_sky_toml_config`
/// performs, because the value this must find is the value THAT function will
/// emit into the binary's prologue. A stricter parser here would disagree with
/// the compiler about the same file.
fn toml_value(toml: &str, section: &str, key: &str) -> Option<String> {
    let mut current = String::new();
    for raw in toml.lines() {
        let line = raw.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        if line.starts_with('[') {
            if let Some(end) = line.find(']') {
                current = line[1..end].trim().trim_matches('"').to_string();
                continue;
            }
        }
        let Some((k, v)) = line.split_once('=') else { continue };
        if current == section && k.trim() == key {
            return Some(parse_toml_scalar(v));
        }
    }
    None
}

/// Strip an inline comment and matching quotes — `project::build`'s
/// `parse_toml_scalar`, kept behaviourally identical for the reason above.
fn parse_toml_scalar(v: &str) -> String {
    let mut s = v.trim();
    if !s.starts_with('"') && !s.starts_with('\'') {
        if let Some(i) = s.find('#') {
            s = s[..i].trim();
        }
    }
    s.trim().trim_matches('"').trim_matches('\'').to_string()
}

/// A `KEY=VALUE` out of a `.env`, matching `rt`'s tolerant loader: blank lines
/// and `#` comments ignored, one matching quote pair stripped.
fn dotenv_value(text: &str, name: &str) -> Option<String> {
    for raw in text.lines() {
        let line = raw.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        let Some((k, v)) = line.split_once('=') else { continue };
        if k.trim() != name {
            continue;
        }
        let v = v.trim();
        let v = if v.len() >= 2
            && ((v.starts_with('"') && v.ends_with('"')) || (v.starts_with('\'') && v.ends_with('\'')))
        {
            &v[1..v.len() - 1]
        } else {
            v
        };
        return Some(v.to_string());
    }
    None
}

/// One PostgreSQL pool the Sky RUNTIME opens for its own purposes, as distinct
/// from the app's `Db.connect`.
///
/// `max_open` is the size that consumer asks for — NOT one shared "aux pool
/// size" applied to all of them. The two large consumers acquire the shared
/// pool (a quarter-share plus both background caps) while telemetry acquires
/// its own fixed four, and flattening that into `aux × 3` under-counted the
/// process's real demand by ten backends at one core.
///
/// The parameter is the APP POOL, not the core count: every runtime pool is a
/// share of the app's, and the app's follows the operator's knob.
pub struct AuxPoolConsumer {
    pub name: &'static str,
    pub max_open: fn(u32) -> u32,
}

/// Every PostgreSQL pool the Sky RUNTIME opens for its own purposes.
///
/// This is a list rather than a literal `3` so that adding a fourth runtime
/// pool is a change to THIS line, and every server sizing that reads it moves
/// with it. Mirrors `dbAuxPoolConsumers` in `runtime-go/rt/db_pool.go`.
pub const AUX_POOL_CONSUMERS: [AuxPoolConsumer; 3] = [
    AuxPoolConsumer {
        name: "analytics", // runtime-go/rt/analytics_store.go
        max_open: shared_aux_pool_size,
    },
    AuxPoolConsumer {
        name: "live-sessions", // runtime-go/rt/live_store.go (pgx path)
        max_open: shared_aux_pool_size,
    },
    AuxPoolConsumer {
        name: "telemetry", // runtime-go/rt/telemetry/persist.go
        max_open: telemetry_pool_size,
    },
];

/// The consumer names, for the diagnostics and the fixture.
pub fn aux_pool_consumer_names() -> Vec<&'static str> {
    AUX_POOL_CONSUMERS.iter().map(|c| c.name).collect()
}

/// The analytics writer's cap on a shared pool. Mirrors `dbAnalyticsShare`.
pub const ANALYTICS_SHARE: u32 = 2;

/// The telemetry writer's cap on a shared pool. Mirrors `telemetry.Share`.
pub const TELEMETRY_SHARE: u32 = 2;

/// `superuser_reserved_connections`, whose PostgreSQL default is 3.
///
/// Those slots are NOT available to an ordinary role, so a cluster with
/// `max_connections = 52` can serve 49 application connections. Leaving them
/// out of the arithmetic is a three-connection error in the direction that
/// produces an outage.
pub const SUPERUSER_RESERVED: u32 = 3;

/// Slots kept for the human: a `psql` session, a backup, a migration, a
/// monitoring agent.
///
/// Without it the first thing an operator does when an app is struggling —
/// connect and look — is the one thing that cannot be done.
pub const OPERATOR_HEADROOM: u32 = 5;

/// The window in which TWO copies of one app hold pools against the cluster.
///
/// That window is not exotic, it is every restart: `sky watch` rebuilding and
/// relaunching, a rolling deploy bringing the new process up before the old
/// one has drained, a supervisor restarting a crashed app while its
/// connections are still being reaped. Sizing for exactly one process makes
/// every restart under load a `too many clients` incident, and the arithmetic
/// that produced it looks correct in isolation.
pub const RESTART_OVERLAP_FACTOR: u32 = 2;

/// Bounds on a SHARED cluster's `max_connections`.
///
/// The floor keeps a tiny host usable at all. The ceiling stops a very large
/// host from being handed a number whose per-backend memory is no longer a
/// rounding error — beyond it the operator is running a database server for a
/// fleet and should state the number with `--max-connections`.
///
/// Both bound what sky DERIVES from the host. Neither bounds what the operator
/// asked for through the pool knob — see [`shared_cluster_max_connections`].
pub const SHARED_MAX_CONNECTIONS_FLOOR: u32 = 25;
pub const SHARED_MAX_CONNECTIONS_CEILING: u32 = 500;

/// Bounds on the DEVELOPMENT cluster's `max_connections`.
///
/// The floor is the historical value: a laptop cluster that was comfortable at
/// 50 should not shrink because the arithmetic now derives a smaller number on
/// a 2-core machine. The ceiling keeps `sky db start` cheap — a development
/// cluster serves ONE app and several idle project clusters must stay
/// affordable side by side.
pub const DEV_MAX_CONNECTIONS_FLOOR: u32 = 50;
pub const DEV_MAX_CONNECTIONS_CEILING: u32 = 100;

fn clamp(v: u32, lo: u32, hi: u32) -> u32 {
    v.clamp(lo, hi)
}

/// The app's own `Db.connect` pool ceiling: 4 connections per CPU, floored at
/// 4 and capped at 32 — BEFORE the operator's knob.
///
/// Mirrors `defaultPostgresPoolConfigFor(cpus, false).MaxOpenConns`.
pub fn default_app_pool_size(cpus: u32) -> u32 {
    let cpus = cpus.max(1);
    clamp(cpus * 4, 4, 32)
}

/// The app's `Db.connect` pool ceiling as the process will actually enforce it,
/// and whether the operator asked for an UNLIMITED pool.
///
/// Mirrors `dbAppPoolMaxOpenFor(cpus, false)` — including the parse: the raw
/// value is trimmed, an unparseable one falls back to the default, and zero or
/// negative is `database/sql`'s "unlimited". No finite `max_connections` covers
/// an unbounded pool, so the sizing substitutes the default and the caller says
/// so in the file it generates rather than printing a claim that cannot be true.
pub fn app_pool_size_resolved(i: &PoolInputs) -> (u32, bool) {
    let default = default_app_pool_size(i.cpus);
    let Some(raw) = i.app_max_open.as_deref() else { return (default, false) };
    let trimmed = raw.trim();
    if trimmed.is_empty() {
        return (default, false);
    }
    let Ok(n) = trimmed.parse::<i64>() else { return (default, false) };
    if n <= 0 {
        return (default, true);
    }
    (u32::try_from(n).unwrap_or(u32::MAX), false)
}

/// The app pool's ceiling — the number every other term is a share of.
pub fn app_pool_size(i: &PoolInputs) -> u32 {
    app_pool_size_resolved(i).0
}

/// One runtime pool's ceiling when it does NOT share: a quarter of the app
/// pool, floored at 2 and capped at 8. Mirrors `dbAuxPoolSizeFrom(app)`.
pub fn aux_pool_size_from(app: u32) -> u32 {
    clamp(app / 4, 2, 8)
}

/// The size the two large runtime consumers ACTUALLY ask for: the unshared
/// ceiling plus both background caps.
///
/// The addition is the bulkhead argument. Sizing the shared pool at merely
/// `aux` and capping the background writers inside it would take connections
/// away from the session store: on a small machine `aux` is 2, two caps of 2
/// consume the whole pool, and the request path is guaranteed nothing. Mirrors
/// `dbSharedAuxPoolSizeFrom(app)`.
pub fn shared_aux_pool_size(app: u32) -> u32 {
    aux_pool_size_from(app) + ANALYTICS_SHARE + TELEMETRY_SHARE
}

/// Telemetry's pool is a fixed small size, not a share of the app's — it is a
/// single batching goroutine. Mirrors `telemetry.PoolMaxConns`.
pub fn telemetry_pool_size(_app: u32) -> u32 {
    4
}

/// The maximum number of PostgreSQL backends ONE Sky app process can hold open
/// at once, given an app pool of `app`.
///
/// This is the WORST case, deliberately: it assumes every runtime pool
/// resolves to a different DSN and therefore does NOT share a pool with the
/// app's. When they do share — the normal case, since "one database for
/// everything" is what `DATABASE_URL` and `sky db provision --embed` both
/// produce — real demand is `app + aux`, well inside this. Sizing a server for
/// the worst case its client can produce is the right direction to be wrong
/// in; the alternative is a cluster that works until someone points telemetry
/// at a second database.
///
/// Mirrors `dbProcessConnectionDemandFrom(app)`.
pub fn process_connection_demand_from(app: u32) -> u32 {
    app + AUX_POOL_CONSUMERS.iter().map(|c| (c.max_open)(app)).sum::<u32>()
}

/// The demand for a set of inputs. Mirrors `dbProcessConnectionDemand`.
pub fn process_connection_demand(i: &PoolInputs) -> u32 {
    process_connection_demand_from(app_pool_size(i))
}

/// The demand sky DERIVES from the machine alone, the operator's knob
/// deliberately ignored. Mirrors `dbDerivedProcessConnectionDemand`.
///
/// It exists so the cluster sizings can clamp what they derive without clamping
/// what the operator explicitly asked for.
pub fn derived_process_connection_demand(cpus: u32) -> u32 {
    process_connection_demand(&PoolInputs::derived(cpus))
}

/// How much the operator's knob adds to the demand over what sky would derive.
fn operator_excess(i: &PoolInputs) -> u32 {
    process_connection_demand(i).saturating_sub(derived_process_connection_demand(i.cpus))
}

/// How many Sky apps a shared cluster on this host is sized to serve by
/// default.
///
/// A shared cluster is not an app's private database — `--app <name>` exists
/// precisely because several apps live on one host — so sizing it for one
/// process reintroduces the defect one level up.
///
/// **One app per four cores, capped at four apps.** The rationale is the app's
/// own pool arithmetic: a Sky process asks for 4 connections per core and
/// expects to actually use several cores, so packing more than one app into
/// four cores means they are all CPU-starved before they are
/// connection-starved. The cap stops a 64-core host from being pre-sized for
/// sixteen tenants it does not have; an operator who genuinely runs a fleet
/// states the number with `--max-connections`, which is why that flag remains.
///
/// The choice is deliberately conservative in the OTHER direction too, because
/// `max_connections` divides the `work_mem` budget
/// (`mem_mb * 1024 / (max_connections * 8)`): sizing for sixteen tenants on an
/// 8-core host would squeeze `work_mem` to its floor for the two apps that
/// actually run there. One-per-four-cores keeps the derived default within
/// sight of the flat 200 it replaces on a mid-sized host, while making it
/// track the host in both directions.
pub fn expected_apps_per_host(cpus: u32) -> u32 {
    clamp(cpus.max(1) / 4, 1, 4)
}

/// The default `max_connections` for a SHARED production cluster.
///
/// `demand × apps × overlap + reserved + headroom`, clamped. Every factor is a
/// named constant above with the reason it exists; the flat `200` this
/// replaces was derived from nothing at all, and was simultaneously an
/// over-commitment on a 2-core VM and a ceiling a 32-core host could reach.
///
/// The clamp is applied to the MACHINE-DERIVED demand, and whatever the
/// operator's pool knob adds on top passes through it — see
/// [`dev_cluster_max_connections`] for the argument.
pub fn shared_cluster_max_connections(i: &PoolInputs) -> u32 {
    let apps = expected_apps_per_host(i.cpus);
    let n = derived_process_connection_demand(i.cpus) * apps * RESTART_OVERLAP_FACTOR
        + SUPERUSER_RESERVED
        + OPERATOR_HEADROOM;
    clamp(n, SHARED_MAX_CONNECTIONS_FLOOR, SHARED_MAX_CONNECTIONS_CEILING)
        + operator_excess(i) * apps * RESTART_OVERLAP_FACTOR
}

/// The `max_connections` for a DEVELOPMENT cluster (`sky db start`).
///
/// A development cluster serves ONE project, so there is no multi-app
/// multiplier — but it must still cover that one process's whole demand plus
/// the superuser slots, which the flat `50` stopped doing at 8 cores.
///
/// The restart-overlap factor is not applied either: `sky watch` kills the old
/// process before starting the new one, so the operating system has already
/// reaped its sockets. What IS kept is the operator headroom, because a
/// development cluster is the one an engineer actually opens `psql` against
/// while the app is running.
///
/// # The clamps bound what sky DERIVES, not what the operator asked for
///
/// [`DEV_MAX_CONNECTIONS_CEILING`] exists to keep several idle project clusters
/// affordable side by side. It was never meant to overrule an operator who
/// states the process's size through the documented pool knob — and once the
/// demand follows that knob, a flat clamp would do exactly that: at
/// `maxOpenConns = 200` the process demands 228 backends and a cluster clamped
/// to 100 strangles it, silently, having been told the number.
///
/// So the clamp governs the machine-derived part and the operator's excess
/// passes through. A generated cluster is therefore never smaller than the
/// process it serves. An operator who asks for a pool no PostgreSQL can serve
/// gets a cluster that refuses to start with PostgreSQL's own diagnosis, which
/// is a better failure than an app that cannot get a connection and no file
/// anywhere admitting why.
pub fn dev_cluster_max_connections(i: &PoolInputs) -> u32 {
    let n = derived_process_connection_demand(i.cpus) + SUPERUSER_RESERVED + OPERATOR_HEADROOM;
    clamp(n, DEV_MAX_CONNECTIONS_FLOOR, DEV_MAX_CONNECTIONS_CEILING) + operator_excess(i)
}

#[cfg(test)]
mod tests {
    use super::*;

    /// The core range every gate below sweeps. 1 is the smallest container
    /// anyone runs; 64 is past the point where both pool clamps have
    /// saturated, so nothing new happens above it.
    const CPUS: std::ops::RangeInclusive<u32> = 1..=64;

    fn inputs(cpus: u32, knob: Option<&str>) -> PoolInputs {
        PoolInputs { cpus, app_max_open: knob.map(str::to_string) }
    }

    // ---- the historical formulas, kept as witnesses -----------------------
    //
    // These are NOT the implementation. They are what the two clusters used
    // before this module existed, retained so the gates can prove they DETECT
    // the defect rather than merely passing today. If a witness stops
    // violating the property, the property has been weakened and the gate no
    // longer proves anything.

    /// `runtime-go/rt/pg_embed_conf.go`, before the fix: `4*cpus + 20`,
    /// reasoned about "the app's own pool".
    fn historical_embed_max_connections(cpus: u32) -> u32 {
        (4 * cpus + 20).clamp(25, 200)
    }

    /// `db_cluster::sky_conf_block`, before the fix: a flat 50, derived from
    /// nothing.
    fn historical_dev_max_connections(_cpus: u32) -> u32 {
        50
    }

    /// The demand as it read BEFORE the knob was part of the frame: the app
    /// term from the defaults, the aux terms from the resolver.
    fn historical_knob_blind_demand(i: &PoolInputs) -> u32 {
        let app = app_pool_size(i);
        default_app_pool_size(i.cpus)
            + AUX_POOL_CONSUMERS.iter().map(|c| (c.max_open)(app)).sum::<u32>()
    }

    // ---- gate 1: the dev cluster covers its one process --------------------

    #[test]
    fn the_development_cluster_covers_one_process_demand_plus_the_reserved_slots() {
        for cpus in CPUS {
            for knob in KNOBS {
                let i = inputs(cpus, *knob);
                let demand = process_connection_demand(&i);
                let max_conn = dev_cluster_max_connections(&i);
                assert!(
                    demand + SUPERUSER_RESERVED <= max_conn,
                    "cpus={cpus} knob={knob:?}: one process demands {demand} backends and \
                     {SUPERUSER_RESERVED} are reserved for the superuser, but the development \
                     cluster offers max_connections = {max_conn}. The app would exhaust the \
                     database sky just started for it."
                );
            }
        }
    }

    // ---- gate 2: the shared cluster covers the apps it is sized for --------

    #[test]
    fn the_shared_cluster_default_covers_every_app_it_is_sized_for() {
        for cpus in CPUS {
            for knob in KNOBS {
                let i = inputs(cpus, *knob);
                let demand = process_connection_demand(&i);
                let apps = expected_apps_per_host(cpus);
                let max_conn = shared_cluster_max_connections(&i);
                assert!(
                    demand * apps + SUPERUSER_RESERVED <= max_conn,
                    "cpus={cpus} knob={knob:?}: {apps} app(s) demand {} backends and \
                     {SUPERUSER_RESERVED} are reserved for the superuser, but the shared cluster \
                     defaults to max_connections = {max_conn}.",
                    demand * apps
                );
                // …and the restart-overlap factor must actually be paid for: a
                // rolling restart of every app must still fit.
                assert!(
                    demand * apps * RESTART_OVERLAP_FACTOR + SUPERUSER_RESERVED <= max_conn,
                    "cpus={cpus} knob={knob:?}: max_connections = {max_conn} does not cover the \
                     restart-overlap window for {apps} app(s) demanding {demand} each."
                );
            }
        }
    }

    // ---- gate 3: the demand counts ALL the pools ---------------------------

    #[test]
    fn the_demand_counts_every_pool_the_process_opens_not_just_the_apps() {
        assert!(
            !AUX_POOL_CONSUMERS.is_empty(),
            "the runtime opens pools that nothing accounts for"
        );
        for cpus in CPUS {
            for knob in KNOBS {
                let i = inputs(cpus, *knob);
                let app = app_pool_size(&i);
                let demand = process_connection_demand(&i);
                assert!(
                    demand > app,
                    "cpus={cpus} knob={knob:?}: demand {demand} is not greater than the app pool \
                     alone ({app}) — the {} runtime pool(s) ({}) have stopped being counted, which \
                     is exactly the defect this module exists to prevent.",
                    AUX_POOL_CONSUMERS.len(),
                    aux_pool_consumer_names().join(", "),
                );
                let per_consumer: u32 =
                    AUX_POOL_CONSUMERS.iter().map(|c| (c.max_open)(app)).sum();
                assert_eq!(
                    demand,
                    app + per_consumer,
                    "cpus={cpus} knob={knob:?}: the demand no longer equals the app pool plus what \
                     each consumer asks for"
                );
            }
        }
    }

    // ---- gate 4: the witnesses must still fail -----------------------------

    #[test]
    fn the_historical_formulas_violate_the_property_this_gate_enforces() {
        let embed_failures: Vec<u32> = CPUS
            .filter(|&c| {
                derived_process_connection_demand(c) + SUPERUSER_RESERVED
                    > historical_embed_max_connections(c)
            })
            .collect();
        assert!(
            !embed_failures.is_empty(),
            "`4*cpus + 20` no longer violates the demand property anywhere in 1..=64. Either the \
             pool arithmetic changed or the property has been weakened — as written, the gate can \
             no longer detect the defect it was built for."
        );

        let dev_failures: Vec<u32> = CPUS
            .filter(|&c| {
                derived_process_connection_demand(c) + SUPERUSER_RESERVED
                    > historical_dev_max_connections(c)
            })
            .collect();
        assert!(
            dev_failures.contains(&8),
            "a flat max_connections = 50 must fail the property at 8 cores — the single most \
             common instance size, where one process demands {} backends. It did not: {dev_failures:?}",
            derived_process_connection_demand(8),
        );
        // And the fixed formulas must pass where the historical ones fail, or
        // the gate is measuring something other than the fix.
        for cpus in embed_failures.iter().chain(dev_failures.iter()) {
            let i = PoolInputs::derived(*cpus);
            assert!(
                process_connection_demand(&i) + SUPERUSER_RESERVED
                    <= dev_cluster_max_connections(&i),
                "cpus={cpus}: the historical formula fails here and the replacement does too"
            );
        }
    }

    /// The knob-blind demand — the shape this module shipped before the env
    /// axis existed — must still be WRONG somewhere, or the gates above have
    /// stopped being able to detect it.
    #[test]
    fn the_knob_blind_demand_violates_the_property_this_gate_enforces() {
        let mut broken = Vec::new();
        for cpus in CPUS {
            for knob in KNOBS {
                let i = inputs(cpus, *knob);
                if historical_knob_blind_demand(&i) != process_connection_demand(&i) {
                    broken.push((cpus, *knob));
                }
            }
        }
        assert!(
            !broken.is_empty(),
            "reading the app term from the DEFAULTS while the aux terms follow the knob no longer \
             disagrees with the real demand anywhere in the swept space — the env axis has stopped \
             being exercised and these gates would not catch the defect they were written for"
        );
        // The measured case from the grill: one core, knob = 64.
        let i = inputs(1, Some("64"));
        assert_eq!(process_connection_demand(&i), 92);
        assert_eq!(historical_knob_blind_demand(&i), 32);
    }

    // ---- gate 5: the two languages agree ----------------------------------

    /// The table the Go runtime generates from ITS arithmetic, embedded at
    /// compile time so this gate cannot run against a stale copy.
    const GO_FIXTURE: &str = include_str!("../../../../runtime-go/rt/testdata/db_pool_sizing.tsv");

    /// The knob values the fixture sweeps, in its order. Restated here ONLY so
    /// the gates above can sweep the same axis; the fixture itself carries the
    /// authoritative values and the cross-language gate reads them from it.
    const KNOBS: &[Option<&str>] = &[
        None,
        Some(""),
        Some("1"),
        Some("8"),
        Some("23"),
        Some("32"),
        Some("64"),
        Some("200"),
        Some("0"),
        Some("-4"),
        Some("lots"),
        Some("  12 "),
    ];

    /// Undo Go's `%q`: the fixture's knob column is Go-quoted, or a bare `-`
    /// when the knob is unset.
    fn unquote(col: &str) -> Option<String> {
        if col == "-" {
            return None;
        }
        let inner = col.strip_prefix('"').and_then(|s| s.strip_suffix('"')).unwrap_or_else(|| {
            panic!("the knob column {col:?} is neither `-` nor a Go-quoted string")
        });
        let mut out = String::with_capacity(inner.len());
        let mut chars = inner.chars();
        while let Some(c) = chars.next() {
            if c != '\\' {
                out.push(c);
                continue;
            }
            match chars.next() {
                Some('t') => out.push('\t'),
                Some('n') => out.push('\n'),
                Some(other) => out.push(other),
                None => panic!("trailing backslash in {col:?}"),
            }
        }
        Some(out)
    }

    /// This module sizes clusters the Go runtime will later connect to, and it
    /// used to claim — in prose, checked by nothing — that it "MIRRORS"
    /// `runtime-go/rt/db_pool.go` "exactly". It did not: the Go side sizes the
    /// two large runtime pools as a quarter-share PLUS both background caps,
    /// and this side multiplied one quarter-share by the consumer count. Every
    /// cluster the CLI sized was short, and the "twice over for restart
    /// overlap" sentence in the generated conf was false.
    ///
    /// This is the gate that makes the mirroring a fact. Regenerate the fixture
    /// with `cd runtime-go && go test ./rt/ -run TestThePoolSizingFixture
    /// -update-pool-fixture` and follow the change here.
    #[test]
    fn the_fixture_matches_the_go_arithmetic() {
        let mut rows = 0usize;
        let mut saw_consumers = false;
        let mut knobs_seen: Vec<Option<String>> = Vec::new();
        for line in GO_FIXTURE.lines() {
            let line = line.trim_end_matches('\n');
            if line.trim().is_empty() || line.starts_with('#') {
                continue;
            }
            let cols: Vec<&str> = line.split('\t').collect();
            if cols[0] == "consumers" {
                saw_consumers = true;
                let go_names: Vec<&str> = cols[1].split(',').collect();
                assert_eq!(
                    go_names,
                    aux_pool_consumer_names(),
                    "the Go runtime opens pools {go_names:?} but this module counts {:?} — \
                     every cluster sized here is wrong by the difference",
                    aux_pool_consumer_names(),
                );
                continue;
            }
            assert_eq!(
                cols.len(),
                9,
                "row {line:?} has {} columns; this gate expects 9 (cpus, knob, app, aux, shared, \
                 telemetry, demand, derived demand, unlimited)",
                cols.len()
            );
            let n = |i: usize| -> u32 {
                cols[i]
                    .parse()
                    .unwrap_or_else(|e| panic!("column {i} of {line:?}: {e}"))
            };
            let cpus = n(0);
            let knob = unquote(cols[1]);
            let (app, aux, shared, tel, demand, derived) = (n(2), n(3), n(4), n(5), n(6), n(7));
            let unlimited = match cols[8] {
                "yes" => true,
                "no" => false,
                other => panic!("column 8 of {line:?} is {other:?}, want yes|no"),
            };
            if !knobs_seen.contains(&knob) {
                knobs_seen.push(knob.clone());
            }
            let i = PoolInputs { cpus, app_max_open: knob.clone() };

            let (got_app, got_unlimited) = app_pool_size_resolved(&i);
            assert_eq!(got_app, app, "cpus={cpus} knob={knob:?}: app pool");
            assert_eq!(
                got_unlimited, unlimited,
                "cpus={cpus} knob={knob:?}: the two sides disagree about whether the operator \
                 asked for an UNLIMITED pool, so one of the two generated files describes a \
                 cluster the other would not have written"
            );
            assert_eq!(
                aux_pool_size_from(app),
                aux,
                "cpus={cpus} knob={knob:?}: unshared aux pool"
            );
            assert_eq!(
                shared_aux_pool_size(app),
                shared,
                "cpus={cpus} knob={knob:?}: the shared pool the analytics and session stores acquire"
            );
            assert_eq!(
                telemetry_pool_size(app),
                tel,
                "cpus={cpus} knob={knob:?}: telemetry's own pool"
            );
            assert_eq!(
                process_connection_demand(&i),
                demand,
                "cpus={cpus} knob={knob:?}: this module would size a cluster for {} backends \
                 while the Go runtime opens pools totalling {demand}",
                process_connection_demand(&i),
            );
            assert_eq!(
                derived_process_connection_demand(cpus),
                derived,
                "cpus={cpus} knob={knob:?}: the machine-derived demand, which is what the cluster \
                 sizings are allowed to clamp"
            );
            rows += 1;
        }
        assert!(
            saw_consumers,
            "the fixture carries no consumer list — it is not the file this gate expects"
        );
        let want_knobs: Vec<Option<String>> =
            KNOBS.iter().map(|k| k.map(str::to_string)).collect();
        assert_eq!(
            knobs_seen, want_knobs,
            "the fixture sweeps a different set of pool-knob values than this module's gates do — \
             the axis they claim to share is not shared"
        );
        assert_eq!(
            rows,
            CPUS.count() * KNOBS.len(),
            "the fixture covers {rows} (core count × knob) pairs; this gate sweeps {}",
            CPUS.count() * KNOBS.len()
        );
    }

    // ---- the sizing must actually track the host ---------------------------

    #[test]
    fn the_shared_default_tracks_the_host_rather_than_being_flat() {
        assert!(
            shared_cluster_max_connections(&PoolInputs::derived(1))
                < shared_cluster_max_connections(&PoolInputs::derived(32)),
            "a 1-core host and a 32-core host must not be handed the same number: {} vs {}",
            shared_cluster_max_connections(&PoolInputs::derived(1)),
            shared_cluster_max_connections(&PoolInputs::derived(32)),
        );
        for cpus in CPUS {
            let n = shared_cluster_max_connections(&PoolInputs::derived(cpus));
            assert!(
                (SHARED_MAX_CONNECTIONS_FLOOR..=SHARED_MAX_CONNECTIONS_CEILING).contains(&n),
                "cpus={cpus}: max_connections = {n} escaped its bounds"
            );
        }
    }

    // ---- the knob is read from the sources the app will read ---------------

    /// The resolution is the half of this that no arithmetic gate can see: the
    /// numbers can be perfect and still be computed from a knob the process
    /// will not use.
    #[test]
    fn the_knob_is_read_with_the_runtimes_own_precedence() {
        let dir = std::env::temp_dir().join(format!(
            "sky-poolinputs-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        std::fs::create_dir_all(&dir).unwrap();

        // sky.toml alone.
        std::fs::write(
            dir.join("sky.toml"),
            "name = \"x\"\n[database]\nmaxOpenConns = 40  # a comment\n",
        )
        .unwrap();
        assert_eq!(
            PoolInputs::resolve(4, Some(&dir)).app_max_open.as_deref(),
            Some("40"),
            "`sky.toml [database] maxOpenConns` is emitted into the binary's prologue as a \
             SetSkyDefault, so the cluster must be sized for it"
        );

        // `.env` outranks sky.toml, as rt's loader does.
        std::fs::write(dir.join(".env"), "# comment\nSKY_DB_MAX_OPEN_CONNS=\"55\"\n").unwrap();
        assert_eq!(PoolInputs::resolve(4, Some(&dir)).app_max_open.as_deref(), Some("55"));

        // A custom [env] prefix renames the variable the runtime reads, so the
        // `.env` line above stops matching.
        std::fs::write(
            dir.join("sky.toml"),
            "name = \"x\"\n[env]\nprefix = \"FENCE_\"\n[database]\nmaxOpenConns = 40\n",
        )
        .unwrap();
        assert_eq!(
            PoolInputs::resolve(4, Some(&dir)).app_max_open.as_deref(),
            Some("40"),
            "with prefix = FENCE the runtime reads FENCE_DB_MAX_OPEN_CONNS, so a .env line \
             naming SKY_DB_MAX_OPEN_CONNS is not the knob this process will read"
        );
        std::fs::write(dir.join(".env"), "FENCE_DB_MAX_OPEN_CONNS=55\n").unwrap();
        assert_eq!(PoolInputs::resolve(4, Some(&dir)).app_max_open.as_deref(), Some("55"));

        // No project at all (`sky db provision --shared`) → the process
        // environment only, and nothing invented from a file that is not there.
        assert_eq!(PoolInputs::resolve(4, None).app_max_open, resolve_max_open_conns(None));

        let _ = std::fs::remove_dir_all(&dir);
    }

    /// The values that are not plain positive integers, resolved exactly as
    /// `dbEnvInt` + `resolveDbPoolConfigFor` resolve them. Pinned here as well
    /// as in the fixture because the fixture proves AGREEMENT, not correctness:
    /// if both sides misread `-4` the fixture is happy.
    #[test]
    fn the_knobs_edge_values_resolve_the_way_the_runtime_resolves_them() {
        let at = |knob: Option<&str>| app_pool_size_resolved(&inputs(2, knob));
        assert_eq!(at(None), (8, false), "unset → the 4-per-CPU default");
        assert_eq!(at(Some("")), (8, false), "empty → unset");
        assert_eq!(at(Some("   ")), (8, false), "whitespace → unset");
        assert_eq!(at(Some(" 12 ")), (12, false), "trimmed, as strconv.Atoi requires");
        assert_eq!(at(Some("lots")), (8, false), "unparseable → the default, with a warning");
        assert_eq!(at(Some("0")), (8, true), "0 is database/sql for UNLIMITED");
        assert_eq!(at(Some("-4")), (8, true), "negative is folded to 0 by the resolver");
        assert_eq!(at(Some("1")), (1, false), "an explicit 1 is honoured, not floored");
    }
}

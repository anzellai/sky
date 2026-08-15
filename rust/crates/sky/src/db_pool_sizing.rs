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
//! band of ordinary machines: at 8 cores the process demands 56 backends while
//! the dev cluster offered 50, of which 3 are reserved for the superuser. The
//! app exhausted the database it had just started, under load, having
//! configured nothing to deserve it.
//!
//! The arithmetic therefore lives in ONE place, and the sizing functions read
//! it. Adding a fifth runtime pool is a change to [`AUX_POOL_CONSUMERS`], and
//! every cluster's ceiling moves with it.
//!
//! # The Go side is the original, and a FIXTURE ties the two together
//!
//! `runtime-go/rt/db_pool.go` sizes the pools themselves, and this module
//! mirrors it. Two implementations of one number is one too many, and here it
//! is unavoidable — the Go runtime cannot be called from the Rust CLI that
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

/// One PostgreSQL pool the Sky RUNTIME opens for its own purposes, as distinct
/// from the app's `Db.connect`.
///
/// `max_open` is the size that consumer asks for — NOT one shared "aux pool
/// size" applied to all of them. The two large consumers acquire the shared
/// pool (a quarter-share plus both background caps) while telemetry acquires
/// its own fixed four, and flattening that into `aux × 3` under-counted the
/// process's real demand by ten backends at one core.
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
/// 4 and capped at 32.
///
/// Mirrors `defaultPostgresPoolConfigFor(cpus, false).MaxOpenConns`.
pub fn app_pool_size(cpus: u32) -> u32 {
    let cpus = cpus.max(1);
    clamp(cpus * 4, 4, 32)
}

/// One runtime pool's ceiling when it does NOT share: a quarter of the app
/// pool, floored at 2 and capped at 8. Mirrors `dbAuxPoolMaxOpenFor(cpus,
/// false)`.
pub fn aux_pool_size(cpus: u32) -> u32 {
    clamp(app_pool_size(cpus) / 4, 2, 8)
}

/// The size the two large runtime consumers ACTUALLY ask for: the unshared
/// ceiling plus both background caps.
///
/// The addition is the bulkhead argument. Sizing the shared pool at merely
/// `aux` and capping the background writers inside it would take connections
/// away from the session store: on a small machine `aux` is 2, two caps of 2
/// consume the whole pool, and the request path is guaranteed nothing. Mirrors
/// `dbSharedAuxPoolMaxOpenFor(cpus, false)`.
pub fn shared_aux_pool_size(cpus: u32) -> u32 {
    aux_pool_size(cpus) + ANALYTICS_SHARE + TELEMETRY_SHARE
}

/// Telemetry's pool is a fixed small size, not a share of the app's — it is a
/// single batching goroutine. Mirrors `telemetry.PoolMaxConns`.
pub fn telemetry_pool_size(_cpus: u32) -> u32 {
    4
}

/// The maximum number of PostgreSQL backends ONE Sky app process can hold open
/// at once on a machine with `cpus` cores.
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
/// The sum is taken over what each consumer actually asks for. Deriving it from
/// one `aux_pool_size` multiplied by the consumer count — which is what this
/// function did — under-reported by 10 backends at 1 core and 4 at 8, so the
/// "twice over for restart overlap" claim printed into every generated conf was
/// false at every core count.
///
/// Mirrors `dbProcessConnectionDemand(cpus, false)`.
pub fn process_connection_demand(cpus: u32) -> u32 {
    app_pool_size(cpus)
        + AUX_POOL_CONSUMERS
            .iter()
            .map(|c| (c.max_open)(cpus))
            .sum::<u32>()
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
pub fn shared_cluster_max_connections(cpus: u32) -> u32 {
    let n = process_connection_demand(cpus) * expected_apps_per_host(cpus) * RESTART_OVERLAP_FACTOR
        + SUPERUSER_RESERVED
        + OPERATOR_HEADROOM;
    clamp(n, SHARED_MAX_CONNECTIONS_FLOOR, SHARED_MAX_CONNECTIONS_CEILING)
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
pub fn dev_cluster_max_connections(cpus: u32) -> u32 {
    let n = process_connection_demand(cpus) + SUPERUSER_RESERVED + OPERATOR_HEADROOM;
    clamp(n, DEV_MAX_CONNECTIONS_FLOOR, DEV_MAX_CONNECTIONS_CEILING)
}

#[cfg(test)]
mod tests {
    use super::*;

    /// The core range every gate below sweeps. 1 is the smallest container
    /// anyone runs; 64 is past the point where both pool clamps have
    /// saturated, so nothing new happens above it.
    const CPUS: std::ops::RangeInclusive<u32> = 1..=64;

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

    // ---- gate 1: the dev cluster covers its one process --------------------

    #[test]
    fn the_development_cluster_covers_one_process_demand_plus_the_reserved_slots() {
        for cpus in CPUS {
            let demand = process_connection_demand(cpus);
            let max_conn = dev_cluster_max_connections(cpus);
            assert!(
                demand + SUPERUSER_RESERVED <= max_conn,
                "cpus={cpus}: one process demands {demand} backends and {SUPERUSER_RESERVED} are \
                 reserved for the superuser, but the development cluster offers max_connections = \
                 {max_conn}. The app would exhaust the database sky just started for it."
            );
        }
    }

    // ---- gate 2: the shared cluster covers the apps it is sized for --------

    #[test]
    fn the_shared_cluster_default_covers_every_app_it_is_sized_for() {
        for cpus in CPUS {
            let demand = process_connection_demand(cpus);
            let apps = expected_apps_per_host(cpus);
            let max_conn = shared_cluster_max_connections(cpus);
            assert!(
                demand * apps + SUPERUSER_RESERVED <= max_conn,
                "cpus={cpus}: {apps} app(s) demand {} backends and {SUPERUSER_RESERVED} are \
                 reserved for the superuser, but the shared cluster defaults to max_connections = \
                 {max_conn}.",
                demand * apps
            );
            // …and the restart-overlap factor must actually be paid for: a
            // rolling restart of every app must still fit.
            assert!(
                demand * apps * RESTART_OVERLAP_FACTOR + SUPERUSER_RESERVED <= max_conn,
                "cpus={cpus}: max_connections = {max_conn} does not cover the restart-overlap \
                 window for {apps} app(s) demanding {demand} each."
            );
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
            let app = app_pool_size(cpus);
            let demand = process_connection_demand(cpus);
            assert!(
                demand > app,
                "cpus={cpus}: demand {demand} is not greater than the app pool alone ({app}) — \
                 the {} runtime pool(s) ({}) have stopped being counted, which is exactly the \
                 defect this module exists to prevent.",
                AUX_POOL_CONSUMERS.len(),
                aux_pool_consumer_names().join(", "),
            );
            let per_consumer: u32 = AUX_POOL_CONSUMERS.iter().map(|c| (c.max_open)(cpus)).sum();
            assert_eq!(
                demand,
                app + per_consumer,
                "cpus={cpus}: the demand no longer equals the app pool plus what each \
                 consumer asks for"
            );
        }
    }

    // ---- gate 4: the witnesses must still fail -----------------------------

    #[test]
    fn the_historical_formulas_violate_the_property_this_gate_enforces() {
        let embed_failures: Vec<u32> = CPUS
            .filter(|&c| {
                process_connection_demand(c) + SUPERUSER_RESERVED > historical_embed_max_connections(c)
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
                process_connection_demand(c) + SUPERUSER_RESERVED > historical_dev_max_connections(c)
            })
            .collect();
        assert!(
            dev_failures.contains(&8),
            "a flat max_connections = 50 must fail the property at 8 cores — the single most \
             common instance size, where one process demands {} backends. It did not: {dev_failures:?}",
            process_connection_demand(8),
        );
        // And the fixed formulas must pass where the historical ones fail, or
        // the gate is measuring something other than the fix.
        for cpus in embed_failures.iter().chain(dev_failures.iter()) {
            assert!(
                process_connection_demand(*cpus) + SUPERUSER_RESERVED <= dev_cluster_max_connections(*cpus),
                "cpus={cpus}: the historical formula fails here and the replacement does too"
            );
        }
    }

    // ---- gate 5: the two languages agree ----------------------------------

    /// The table the Go runtime generates from ITS arithmetic, embedded at
    /// compile time so this gate cannot run against a stale copy.
    const GO_FIXTURE: &str = include_str!("../../../../runtime-go/rt/testdata/db_pool_sizing.tsv");

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
        for line in GO_FIXTURE.lines() {
            let line = line.trim();
            if line.is_empty() || line.starts_with('#') {
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
            let n = |i: usize| -> u32 {
                cols[i]
                    .parse()
                    .unwrap_or_else(|e| panic!("column {i} of {line:?}: {e}"))
            };
            let (cpus, app, aux, shared, tel, demand) = (n(0), n(1), n(2), n(3), n(4), n(5));
            assert_eq!(app_pool_size(cpus), app, "cpus={cpus}: app pool");
            assert_eq!(aux_pool_size(cpus), aux, "cpus={cpus}: unshared aux pool");
            assert_eq!(
                shared_aux_pool_size(cpus),
                shared,
                "cpus={cpus}: the shared pool the analytics and session stores acquire"
            );
            assert_eq!(
                telemetry_pool_size(cpus),
                tel,
                "cpus={cpus}: telemetry's own pool"
            );
            assert_eq!(
                process_connection_demand(cpus),
                demand,
                "cpus={cpus}: this module would size a cluster for {} backends while the Go \
                 runtime opens pools totalling {demand}",
                process_connection_demand(cpus),
            );
            rows += 1;
        }
        assert!(
            saw_consumers,
            "the fixture carries no consumer list — it is not the file this gate expects"
        );
        assert_eq!(
            rows,
            CPUS.count(),
            "the fixture covers {rows} core counts; this gate sweeps {}",
            CPUS.count()
        );
    }

    // ---- the sizing must actually track the host ---------------------------

    #[test]
    fn the_shared_default_tracks_the_host_rather_than_being_flat() {
        assert!(
            shared_cluster_max_connections(1) < shared_cluster_max_connections(32),
            "a 1-core host and a 32-core host must not be handed the same number: {} vs {}",
            shared_cluster_max_connections(1),
            shared_cluster_max_connections(32),
        );
        for cpus in CPUS {
            let n = shared_cluster_max_connections(cpus);
            assert!(
                (SHARED_MAX_CONNECTIONS_FLOOR..=SHARED_MAX_CONNECTIONS_CEILING).contains(&n),
                "cpus={cpus}: max_connections = {n} escaped its bounds"
            );
        }
    }
}

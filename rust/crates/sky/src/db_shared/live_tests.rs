//! The live gates: a real cluster, real roles, real credentials.
//!
//! `tests.rs` proves the SQL was *generated*. That is not the claim P6 makes.
//! The claim is that **app A's credentials cannot reach app B's database**, and
//! the only evidence for it is an attempt: connect as A, with A's own password,
//! to B's database, and observe PostgreSQL refuse. Everything here does that
//! against a cluster this code provisioned.
//!
//! The probes are made twice over, deliberately:
//!
//! * through [`crate::pg_wire`], which asserts on the **SQLSTATE** (`42501`
//!   insufficient_privilege, `28P01` invalid_password) rather than on English
//!   text that a differently-configured failure could also produce;
//! * through **`pg_dump`**, a real libpq client from the PostgreSQL distribution
//!   itself, handed the DSN exactly as sky printed it. If sky's own client were
//!   wrong about what happened, that probe would disagree.
//!
//! These run only when a PostgreSQL is discoverable; otherwise they return with
//! a note, the convention `db_flow.rs` and `db_cluster_flow.rs` already use.

use super::*;
use crate::pg_wire::{Conn, Target};

/// One cluster per run, torn down whatever happens: a leaked postmaster holds a
/// SysV shared-memory id, and this machine has 32 of them.
struct Fixture {
    layout: Layout,
    bins: PgBins,
    user: String,
}

impl Drop for Fixture {
    fn drop(&mut self) {
        let _ = stop_postmaster(&self.bins, &self.layout);
        let _ = std::fs::remove_dir_all(&self.layout.state_dir);
    }
}

fn scratch_state_dir(tag: &str) -> PathBuf {
    // NOT `std::env::temp_dir()`: `state_dir_error` refuses ephemeral paths, and
    // it is right to — this directory would hold every app's only copy of its
    // data. A hidden directory under $HOME is durable, outside any Sky project,
    // and short enough that the socket path stays inside `sun_path`.
    let home = std::env::var("HOME").expect("HOME");
    PathBuf::from(home).join(format!(
        ".sky-p6-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ))
}

/// The DSN sky prints, and the password inside it. Parsed out of the message the
/// operator reads, which is the only place the password ever exists.
fn dsn_and_password(out: &str, app: &str) -> (String, String) {
    let dsn = out
        .lines()
        .map(str::trim)
        .find(|l| l.starts_with("postgresql://"))
        .unwrap_or_else(|| panic!("no DSN in:\n{out}"))
        .to_string();
    let after = dsn.strip_prefix(&format!("postgresql://{app}:")).unwrap_or_else(|| panic!("{dsn}"));
    let pw = after.split('@').next().unwrap().to_string();
    (dsn, pw)
}

fn connect_as(layout: &Layout, user: &str, db: &str, pw: &str) -> Result<Conn, crate::pg_wire::Error> {
    Conn::connect(
        &Target::Unix(layout.socket_dir.clone(), DEFAULT_PORT),
        user,
        db,
        Some(pw),
    )
}

/// `pg_dump` against a DSN: an independent, real libpq client's verdict on the
/// same question the wire client is asked.
fn pg_dump(bins: &PgBins, dsn: &str, extra: &[&str]) -> std::process::Output {
    Command::new(bins.tool("pg_dump"))
        .arg(dsn)
        .args(extra)
        .output()
        .expect("pg_dump")
}

fn provision_fixture(tag: &str) -> Option<(Fixture, String, String, String, String)> {
    let Ok(bins) = db_cluster::discover_pg_bins() else {
        eprintln!("no PostgreSQL discoverable — skipping the live shared-cluster gate");
        return None;
    };
    // Small enough to be a good neighbour to whatever else is on this machine.
    // The variable exists for containers, where /proc/meminfo is the host's.
    std::env::set_var("SKY_PG_TUNE_MEM_MB", "512");
    let state = scratch_state_dir(tag);
    assert_eq!(
        state_dir_error(&state),
        None,
        "the scratch state dir is itself refused; the fixture is wrong, not the code"
    );
    let user = os_user().expect("id -un");
    let opts = Opts {
        state_dir: Some(state.clone()),
        service: true,
        backup: true,
        start: true,
        max_connections: 30,
        ..Opts::default()
    };
    let out = provision_cluster(&opts).unwrap_or_else(|e| panic!("provision failed:\n{e}"));
    assert!(out.contains("cluster ready"), "{out}");
    let fx = Fixture {
        layout: Layout::new(&state),
        bins,
        user: user.clone(),
    };

    let app_opts = |name: &str| Opts {
        state_dir: Some(state.clone()),
        app: Some(name.to_string()),
        ..Opts::default()
    };
    let a = provision_app(&app_opts("alpha")).unwrap_or_else(|e| panic!("alpha:\n{e}"));
    let b = provision_app(&app_opts("beta")).unwrap_or_else(|e| panic!("beta:\n{e}"));
    let (a_dsn, a_pw) = dsn_and_password(&a, "alpha");
    let (b_dsn, b_pw) = dsn_and_password(&b, "beta");
    // `--app` leaves the cluster as it found it, and it found it running.
    assert!(cluster_running(&fx.layout), "the app provision stopped a cluster it did not start");
    Some((fx, a_dsn, a_pw, b_dsn, b_pw))
}

/// THE gate for this phase.
///
/// Four probes and two positive controls. The positive controls matter as much
/// as the refusals: "connection refused" is also what a broken cluster says, and
/// a gate that only asserts failure passes just as well against a database that
/// does not work at all.
///
/// The falsifying mutation is `app_cluster_sql`'s
/// `REVOKE ALL ON DATABASE … FROM PUBLIC`: with it deleted, `PUBLIC` retains its
/// default `CONNECT`, alpha connects to beta's database, and probe 1 fails. The
/// second mutation is `pg_hba`'s `scram-sha-256` → `trust`, with which probe 2
/// connects as beta using alpha's password and reads beta's rows.
#[test]
fn an_apps_credentials_cannot_reach_another_apps_database() {
    let Some((fx, a_dsn, a_pw, b_dsn, b_pw)) = provision_fixture("iso") else {
        return;
    };

    // --- positive control: each app owns and can use its own database --------
    for (app, pw) in [("alpha", &a_pw), ("beta", &b_pw)] {
        let mut c = connect_as(&fx.layout, app, app, pw)
            .unwrap_or_else(|e| panic!("{app} cannot reach its own database: {e}"));
        c.execute("CREATE TABLE secrets (v text)")
            .unwrap_or_else(|e| panic!("{app} cannot create a table it owns: {e}"));
        c.execute(&format!("INSERT INTO secrets VALUES ('{app}-secret')")).unwrap();
        assert_eq!(
            c.scalar("SELECT v FROM secrets").unwrap().as_deref(),
            Some(format!("{app}-secret").as_str())
        );
    }

    // --- probe 1: alpha's own credentials, beta's database -------------------
    // On the failing branch the probe goes on to READ, so the panic message is
    // the evidence rather than a claim about it: with the REVOKE removed this
    // prints beta's table list, taken out of beta's database by alpha.
    match connect_as(&fx.layout, "alpha", "beta", &a_pw) {
        Err(e) => assert_eq!(e.sqlstate(), Some("42501"), "expected insufficient_privilege, got: {e}"),
        Ok(mut c) => {
            let seen = c.query("SELECT tablename FROM pg_tables WHERE schemaname = 'public'");
            panic!("alpha connected to beta's database and read {seen:?}");
        }
    }

    // --- probe 2: alpha's password, claiming to be beta ----------------------
    // Authentication is the floor the REVOKEs stand on: under `trust` any local
    // process may simply claim to be beta, and every grant behind it is
    // decoration. So this probe reads beta's row on the failing branch.
    match connect_as(&fx.layout, "beta", "beta", &a_pw) {
        Err(e) => assert_eq!(e.sqlstate(), Some("28P01"), "expected invalid_password, got: {e}"),
        Ok(mut c) => {
            let leaked = c.scalar("SELECT v FROM secrets");
            panic!("alpha's password authenticated as beta, which then read {leaked:?}");
        }
    }

    // --- probe 3: the maintenance database is not a way in -------------------
    match connect_as(&fx.layout, "alpha", "postgres", &a_pw) {
        Err(e) => assert_eq!(e.sqlstate(), Some("42501"), "got: {e}"),
        Ok(mut c) => {
            let seen = c.query("SELECT datname FROM pg_database");
            panic!("alpha connected to the postgres database and read {seen:?}");
        }
    }

    // --- probe 4: the same two questions, asked by a real libpq client -------
    // pg_dump is from the PostgreSQL distribution and knows nothing about sky's
    // wire client; if that client were lying about what happened, this disagrees.
    let cross = a_dsn.replace("/alpha?", "/beta?").replace("@/alpha", "@/beta");
    let out = pg_dump(&fx.bins, &cross, &["--schema-only"]);
    assert!(!out.status.success(), "pg_dump read beta's schema with alpha's DSN");
    let text = String::from_utf8_lossy(&out.stderr).to_string();
    assert!(
        text.contains("permission denied for database"),
        "refused for the wrong reason:\n{text}"
    );

    let impostor = b_dsn.replace(&b_pw, &a_pw);
    let out = pg_dump(&fx.bins, &impostor, &["--schema-only"]);
    assert!(!out.status.success(), "alpha's password authenticated as beta through libpq");

    // Positive control for the client itself: the DSN sky printed does work.
    let out = pg_dump(&fx.bins, &a_dsn, &["--schema-only"]);
    assert!(
        out.status.success(),
        "the DSN sky printed does not work:\n{}",
        String::from_utf8_lossy(&out.stderr)
    );
    assert!(String::from_utf8_lossy(&out.stdout).contains("CREATE TABLE public.secrets"));

    // --- probe 5: the app role is a member of no role at all -----------------
    // Probes 1-4 ask two questions: can A *connect* to B's database, and does
    // A's password authenticate *as* B. A leak that needs neither is invisible
    // to both. `GRANT pg_monitor TO alpha` is the shape: it crosses no database
    // boundary, it changes no password, and it lets alpha read beta's in-flight
    // SQL out of `pg_stat_activity` — which is cluster-wide, and application SQL
    // routinely carries literals.
    //
    // The membership count is the primary assertion because it is the general
    // form: a role that belongs to NO role cannot have been granted any
    // cluster-wide capability, whichever one a future edit happens to pick.
    let mut held = connect_as(&fx.layout, "beta", "beta", &b_pw).expect("beta");
    held.execute("SELECT 'p6-canary-4111111111111111 someone@example.test'").unwrap();
    let mut a = connect_as(&fx.layout, "alpha", "alpha", &a_pw).expect("alpha");
    assert_eq!(
        a.scalar("SELECT count(*) FROM pg_auth_members WHERE member = 'alpha'::regrole")
            .unwrap()
            .as_deref(),
        Some("0"),
        "alpha is a member of a role: whatever that role may do cluster-wide, alpha may"
    );
    let seen = a
        .query("SELECT datname, query FROM pg_stat_activity WHERE datname IS DISTINCT FROM current_database()")
        .unwrap();
    assert!(
        !format!("{seen:?}").contains("p6-canary"),
        "alpha read another database's SQL, literals and all, out of pg_stat_activity: {seen:?}"
    );
    drop(a);
    drop(held);
}

/// An ADOPTED cluster — one already running when `--shared` reaches it — must
/// end up enforcing the `pg_hba.conf` sky just wrote, not the one it read at
/// startup.
///
/// Adoption is the documented primary case: "a shared cluster may be an
/// operator's existing server, so it is applied rather than assumed". Writing
/// the file is not applying it. `pg_hba.conf` is read by the postmaster at
/// startup and on SIGHUP, so a cluster that was already up keeps enforcing
/// whatever it had — indefinitely, while every artefact on disk reviews
/// correctly.
///
/// The asymmetry is what makes this specifically silent. An adopted `md5`
/// cluster fails loudly (`pg_wire` refuses md5), and a cluster sky starts itself
/// reads the new file. Only `trust`/`peer` adoption is quiet — the one case
/// where quiet is fatal, because under `trust` every REVOKE behind it is
/// decoration.
///
/// The falsifying mutation is deleting the reload from `provision_cluster`:
/// then a wrong password connects, and this test says what it read.
#[test]
fn an_adopted_running_cluster_ends_up_enforcing_the_hba_sky_wrote() {
    let Ok(bins) = db_cluster::discover_pg_bins() else {
        eprintln!("no PostgreSQL discoverable — skipping the adopted-cluster gate");
        return;
    };
    std::env::set_var("SKY_PG_TUNE_MEM_MB", "512");
    let state = scratch_state_dir("adopt");
    let layout = Layout::new(&state);
    let user = os_user().expect("id -un");

    // The operator's server, as a hand-rolled or distribution initdb leaves it:
    // `trust` for local connections, and a socket where sky will look for it.
    std::fs::create_dir_all(&layout.socket_dir).unwrap();
    std::fs::create_dir_all(&layout.log_dir).unwrap();
    let out = Command::new(bins.tool("initdb"))
        .arg("-D")
        .arg(&layout.data_dir)
        .args(["--encoding=UTF8", "--locale=C", "--auth-local=trust", "--auth-host=trust"])
        .arg(format!("--username={user}"))
        .stdout(Stdio::null())
        .output()
        .expect("initdb");
    assert!(out.status.success(), "{}", String::from_utf8_lossy(&out.stderr));
    let conf = layout.data_dir.join("postgresql.conf");
    let mut text = std::fs::read_to_string(&conf).unwrap();
    text.push_str(&format!(
        "\nlisten_addresses = ''\nport = {DEFAULT_PORT}\nunix_socket_directories = '{}'\n\
         unix_socket_permissions = 0777\nshared_buffers = 32MB\n",
        layout.socket_dir.display()
    ));
    std::fs::write(&conf, text).unwrap();

    let fx = Fixture { layout, bins, user: user.clone() };
    start_postmaster(&fx.bins, &fx.layout).expect("the operator's cluster did not start");
    assert!(cluster_running(&fx.layout));
    // Precondition: it really is a `trust` cluster. Without this the test could
    // pass against a cluster that never accepted a wrong password in the first
    // place, and prove nothing.
    connect_as(&fx.layout, &user, "postgres", "not-the-password")
        .expect("the fixture did not produce a trust cluster; the test is wrong, not the code");

    provision_cluster(&Opts { state_dir: Some(state.clone()), ..Opts::default() })
        .unwrap_or_else(|e| panic!("provision failed:\n{e}"));
    assert!(cluster_running(&fx.layout), "provisioning stopped a cluster it did not start");

    let out = provision_app(&Opts {
        state_dir: Some(state.clone()),
        app: Some("gamma".into()),
        ..Opts::default()
    })
    .unwrap_or_else(|e| panic!("gamma:\n{e}"));
    let (_, pw) = dsn_and_password(&out, "gamma");
    // Positive control: the DSN sky printed does work. A cluster that refuses
    // everything would satisfy the refusal below for the wrong reason.
    connect_as(&fx.layout, "gamma", "gamma", &pw).expect("the DSN sky printed does not work");

    match connect_as(&fx.layout, "gamma", "gamma", "not-the-password") {
        Err(e) => assert_eq!(e.sqlstate(), Some("28P01"), "expected invalid_password, got: {e}"),
        Ok(mut c) => {
            let who = c.scalar("SELECT current_user").unwrap();
            let seen = c.query("SELECT tablename FROM pg_tables WHERE schemaname = 'public'");
            panic!(
                "a wrong password connected as {who:?} and read {seen:?}: the adopted cluster is \
                 still enforcing the pg_hba.conf it started with"
            );
        }
    }
    // And with no password at all, which is what `trust` really means.
    assert!(
        Conn::connect(&Target::Unix(fx.layout.socket_dir.clone(), DEFAULT_PORT), "gamma", "gamma", None)
            .is_err(),
        "gamma connected with no password at all"
    );
}

/// `--app <name>` must never ISSUE CREDENTIALS for a role that is not sky's
/// alone — whoever created it.
///
/// Three shapes, and the test carries all three because the code answers them
/// in three different ways:
///
/// 1. **A role sky did not create.** `RESERVED` names the cluster's built-in
///    furniture; it cannot name the account that ran `--shared`, which is the
///    bootstrap superuser, nor an operator's `analytics` / previous tenant's
///    role. For all of them the pre-existing branch used to `ALTER ROLE …
///    PASSWORD` and print the result as an app DSN — handing the new app the
///    old one's identity and giving the operator's role a password it did not
///    choose.
/// 2. **A role holding an elevated ATTRIBUTE.** All five are exercised, one
///    role each. Three of them — `CREATEDB`, `REPLICATION`, `BYPASSRLS` — had
///    no test at all: their arms could be deleted from the refusal with every
///    gate still green, and a pre-existing `REPLICATION` role handed out as an
///    app credential streams the whole cluster's WAL, which is every app's
///    data. The role sky CREATES is then asserted to hold none of them, in the
///    general form (every boolean column of `pg_roles` bar the two an app needs)
///    so that a sixth attribute in a future PostgreSQL is covered without an
///    edit here.
/// 3. **A role holding privilege by MEMBERSHIP.** `GRANT beta TO alpha` leaves
///    all five attributes false. `--app alpha --rotate-password` then succeeded
///    and printed a DSN that reads beta's private data. Membership is the other
///    half of "may this role do more than its own database", and the refusal
///    read only `rol*` columns. Asserted at the moment credentials are ISSUED —
///    a re-provision — because creation-time is not when this arises: the grant
///    happens to a role that already exists.
///
/// The falsifying mutations: restoring the `ALTER ROLE` on the pre-existing
/// branch (the `rolpassword` comparisons fail, having been rotated); deleting
/// any one of the five attribute arms; deleting the `pg_auth_members` query.
#[test]
fn provisioning_an_app_over_a_role_sky_did_not_create_is_refused() {
    let Some((fx, _a_dsn, a_pw, _b_dsn, b_pw)) = provision_fixture("adopt-role") else {
        return;
    };
    let app_opts = |name: &str| Opts {
        state_dir: Some(fx.layout.state_dir.clone()),
        app: Some(name.to_string()),
        ..Opts::default()
    };

    // 1. The bootstrap superuser — the account that ran `--shared`.
    let e = provision_app(&app_opts(&fx.user)).expect_err("the superuser was provisioned as an app");
    assert!(e.contains("already exists"), "{e}");
    let mut admin = admin_conn(&fx.layout, DEFAULT_PORT, &fx.user, "postgres").unwrap();
    assert!(
        admin
            .scalar(&format!("SELECT 1 FROM pg_database WHERE datname = {}", quote_literal(&fx.user)))
            .unwrap()
            .is_none(),
        "a database was created for the superuser before the refusal"
    );

    // 2. An operator's own role that may do more than its own database. Not the
    //    superuser, and not a name sky could have reserved.
    let ops_pw = generate_password();
    admin
        .execute(&format!(
            "CREATE ROLE ops LOGIN PASSWORD {} CREATEROLE",
            quote_literal(&ops_pw)
        ))
        .unwrap();
    // 3. And a plain pre-existing role: a previous tenant's, or another tool's.
    let old_pw = generate_password();
    admin
        .execute(&format!("CREATE ROLE tenantx LOGIN PASSWORD {}", quote_literal(&old_pw)))
        .unwrap();

    let verifier = |admin: &mut Conn, role: &str| {
        admin
            .scalar(&format!(
                "SELECT rolpassword FROM pg_authid WHERE rolname = {}",
                quote_literal(role)
            ))
            .unwrap()
    };
    let ops_before = verifier(&mut admin, "ops");
    let tenant_before = verifier(&mut admin, "tenantx");
    assert!(ops_before.is_some() && tenant_before.is_some());

    let e = provision_app(&app_opts("ops")).expect_err("a CREATEROLE role was provisioned as an app");
    assert!(e.contains("CREATEROLE") || e.contains("more than its own database"), "{e}");
    let e = provision_app(&app_opts("tenantx")).expect_err("a foreign role was provisioned as an app");
    assert!(e.contains("sky did not create") || e.contains("already exists"), "{e}");

    let mut admin = admin_conn(&fx.layout, DEFAULT_PORT, &fx.user, "postgres").unwrap();
    assert_eq!(verifier(&mut admin, "ops"), ops_before, "the operator's role was given a new password");
    assert_eq!(verifier(&mut admin, "tenantx"), tenant_before, "the pre-existing role was rotated");
    // The refusals are not a blanket one: a name nobody has taken still works.
    let out = provision_app(&app_opts("gamma")).expect("a fresh app name was refused");
    let (_, pw) = dsn_and_password(&out, "gamma");
    connect_as(&fx.layout, "gamma", "gamma", &pw).expect("the fresh app's DSN does not work");

    // --- shape 2: every elevated attribute ------------------------------------
    // `ops` above covers CREATEROLE and the superuser covers SUPERUSER, but
    // CREATEDB, REPLICATION and BYPASSRLS had nothing observing them at all.
    //
    // The PROMOTED case comes first because it is the one where deleting an arm
    // is exploitable rather than merely differently-worded. Everything else
    // about alpha is sky's own — sky's role comment, an apps_file entry — so
    // every ownership question says yes and the attribute check is the only
    // thing between `--rotate-password` and a DSN for a role that reads the
    // whole cluster. With an arm deleted the refusal does not happen at all.
    for attr in ["SUPERUSER", "CREATEROLE", "CREATEDB", "REPLICATION", "BYPASSRLS"] {
        let before = verifier(&mut admin, "alpha");
        admin.execute(&format!("ALTER ROLE alpha {attr}")).unwrap();
        let e = provision_app(&Opts { rotate: true, ..app_opts("alpha") }).expect_err(&format!(
            "alpha was rotated after being promoted to {attr}; the DSN reads every database"
        ));
        panic_if_not_refused(&e, attr);
        assert_eq!(
            verifier(&mut admin, "alpha"),
            before,
            "alpha's password was rotated despite holding {attr}"
        );
        admin.execute(&format!("ALTER ROLE alpha NO{attr}")).unwrap();
    }

    // And the same five held by a role sky did NOT create, which is the shape
    // an operator's `analytics` or `replication` account has.
    for (attr, role) in [
        ("SUPERUSER", "elev_super"),
        ("CREATEROLE", "elev_createrole"),
        ("CREATEDB", "elev_createdb"),
        ("REPLICATION", "elev_replication"),
        ("BYPASSRLS", "elev_bypassrls"),
    ] {
        let pw = generate_password();
        admin
            .execute(&format!(
                "CREATE ROLE {} LOGIN PASSWORD {} {attr}",
                quote_ident(role),
                quote_literal(&pw)
            ))
            .unwrap();
        let before = verifier(&mut admin, role);
        let e = provision_app(&app_opts(role))
            .expect_err(&format!("a {attr} role was provisioned as an app"));
        panic_if_not_refused(&e, attr);
        assert_eq!(
            verifier(&mut admin, role),
            before,
            "the {attr} role was given a new password"
        );
    }

    // The other side of the same claim: the role sky CREATES holds none of
    // them. Read generically out of the catalog rather than from a list here,
    // so a boolean attribute added in a future PostgreSQL is covered without an
    // edit. `rolcanlogin` and `rolinherit` are excluded because an app role
    // needs both.
    let attrs = admin
        .scalar(
            "SELECT string_agg(column_name, ' OR ' ORDER BY column_name) \
             FROM information_schema.columns \
             WHERE table_schema = 'pg_catalog' AND table_name = 'pg_roles' \
               AND data_type = 'boolean' \
               AND column_name NOT IN ('rolcanlogin', 'rolinherit')",
        )
        .unwrap()
        .expect("pg_roles has no boolean columns; the query is wrong, not the code");
    assert!(
        attrs.split(" OR ").count() >= 5,
        "only {} elevated attribute(s) found ({attrs}) — the catalog query is not reading pg_roles",
        attrs.split(" OR ").count()
    );
    assert_eq!(
        admin
            .scalar(&format!(
                "SELECT ({attrs})::text FROM pg_roles WHERE rolname = 'alpha'"
            ))
            .unwrap()
            .as_deref(),
        Some("false"),
        "the role sky created holds an elevated attribute ({attrs})"
    );

    // --- shape 3: privilege by membership, at the moment credentials issue ----
    // alpha is sky's own role, carries sky's marker, is in apps_file, and holds
    // no elevated attribute. Every question the refusal used to ask says yes.
    let alpha_before = verifier(&mut admin, "alpha");
    for grant in ["beta", "pg_monitor"] {
        admin.execute(&format!("GRANT {} TO alpha", quote_ident(grant))).unwrap();
        let e = provision_app(&Opts { rotate: true, ..app_opts("alpha") }).expect_err(&format!(
            "alpha was re-provisioned while a member of {grant}; the DSN reads its data"
        ));
        assert!(
            e.contains("member of"),
            "refused for the wrong reason after GRANT {grant}:\n{e}"
        );
        assert_eq!(
            verifier(&mut admin, "alpha"),
            alpha_before,
            "alpha's password was rotated despite the refusal"
        );
        admin.execute(&format!("REVOKE {} FROM alpha", quote_ident(grant))).unwrap();
    }
    // Positive control: with the membership gone, the same call works. Without
    // this the refusal above could be a rotate path that is simply broken.
    let out = provision_app(&Opts { rotate: true, ..app_opts("alpha") })
        .expect("alpha could not be rotated even with no membership");
    let (_, rotated) = dsn_and_password(&out, "alpha");
    assert_ne!(rotated, a_pw);
    connect_as(&fx.layout, "alpha", "alpha", &rotated).expect("the rotated DSN does not work");
    connect_as(&fx.layout, "beta", "beta", &b_pw).expect("beta was disturbed");
}

/// A refusal has to name the reason. Asserting only `is_err()` passes against a
/// cluster that has fallen over, and asserting the whole message pins prose.
fn panic_if_not_refused(err: &str, attr: &str) {
    assert!(
        err.contains(attr) || err.contains("more than its own database"),
        "a {attr} role was refused, but for some other reason:\n{err}"
    );
}

/// `--app <name>` must not take over a DATABASE sky did not create — and the
/// case that reached the operator's data is the one where the role is ABSENT.
///
/// The role refusal is reached only when a role of that name exists. An
/// operator's server routinely has the other combination: a `metrics` database
/// created by hand, owned by whatever account made it, with no `metrics` login
/// role at all. That skipped the refusal, skipped `CREATE DATABASE` (it exists),
/// and ran the rest against their data — `REVOKE ALL ON DATABASE metrics FROM
/// PUBLIC`, which takes their own role's `CONNECT` away, and `ALTER SCHEMA
/// public OWNER TO metrics`, which hands the schema to the new app whose DSN the
/// same command prints. An outage and a handover, reported as success. Adopting
/// an operator's existing server is the documented primary case for `--shared`,
/// so this is reachable by design.
///
/// The assertion is the GENERAL form rather than a list of the two statements
/// that did the damage: every database sky did not create is byte-identical
/// afterwards — owner, ACL, and (for the one under attack) the `public` schema's
/// owner. A different statement doing the same thing is caught by the same test.
///
/// The falsifying mutation is deleting the `refuse_a_database_sky_does_not_own`
/// call from `provision_app_inner`, or the `COMMENT ON DATABASE` from
/// `app_cluster_sql` that makes the question answerable.
#[test]
fn an_app_run_does_not_take_over_a_database_sky_did_not_create() {
    let Some((fx, _a_dsn, _a_pw, _b_dsn, _b_pw)) = provision_fixture("adopt-db") else {
        return;
    };
    let mut admin = admin_conn(&fx.layout, DEFAULT_PORT, &fx.user, "postgres").unwrap();

    // The operator's server, as `--shared` finds it: a database with no role of
    // the same name, owned by an ordinary account of theirs.
    let ops_pw = generate_password();
    admin
        .execute(&format!("CREATE ROLE opsowner LOGIN PASSWORD {}", quote_literal(&ops_pw)))
        .unwrap();
    admin.execute("CREATE DATABASE metrics OWNER opsowner").unwrap();
    assert!(
        admin
            .scalar("SELECT 1 FROM pg_roles WHERE rolname = 'metrics'")
            .unwrap()
            .is_none(),
        "the fixture created a metrics ROLE; then this is the case the role refusal already covers"
    );

    // Positive control: the operator's app works right now. Without this the
    // refusal below could be satisfied by a database that never worked.
    connect_as(&fx.layout, "opsowner", "metrics", &ops_pw)
        .expect("the fixture's own role cannot reach its own database; the test is wrong");

    let databases = |c: &mut Conn| {
        c.query(
            "SELECT datname, pg_get_userbyid(datdba), coalesce(datacl::text, '<default>') \
             FROM pg_database ORDER BY datname",
        )
        .unwrap()
    };
    let before = databases(&mut admin);
    let public_owner = |fx: &Fixture| {
        admin_conn(&fx.layout, DEFAULT_PORT, &fx.user, "metrics")
            .unwrap()
            .scalar("SELECT pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname = 'public'")
            .unwrap()
    };
    let nspowner_before = public_owner(&fx);

    let e = provision_app(&Opts {
        state_dir: Some(fx.layout.state_dir.clone()),
        app: Some("metrics".into()),
        ..Opts::default()
    })
    .expect_err("an operator's existing database was provisioned as an app");
    assert!(
        e.contains("did not create the database"),
        "refused for the wrong reason:\n{e}"
    );

    let mut admin = admin_conn(&fx.layout, DEFAULT_PORT, &fx.user, "postgres").unwrap();
    assert_eq!(
        databases(&mut admin),
        before,
        "a database sky did not create had its owner or ACL changed by a run that failed"
    );
    assert_eq!(
        public_owner(&fx),
        nspowner_before,
        "the public schema of a database sky did not create changed owner"
    );
    // The outage, asked directly: the operator's own role still connects.
    connect_as(&fx.layout, "opsowner", "metrics", &ops_pw)
        .expect("the operator's role lost CONNECT on its own database");
    assert!(
        admin
            .scalar("SELECT 1 FROM pg_roles WHERE rolname = 'metrics'")
            .unwrap()
            .is_none(),
        "a role was created for an app whose provision was refused"
    );

    // The refusal is not a blanket one. A database sky DID create re-provisions,
    // which is the property the marker has to carry — and it is the marker that
    // carries it, not `apps_file`: the entry is removed first.
    std::fs::write(&fx.layout.apps_file, "").unwrap();
    provision_app(&Opts {
        state_dir: Some(fx.layout.state_dir.clone()),
        app: Some("alpha".into()),
        ..Opts::default()
    })
    .expect("sky refused a database it created itself, with only the marker to go on");
    assert_eq!(
        admin
            .scalar(&format!(
                "SELECT shobj_description(oid, 'pg_database') FROM pg_database WHERE datname = 'alpha'"
            ))
            .unwrap()
            .as_deref(),
        Some(DB_MARKER),
        "the database sky created carries no marker, so the refusal above rests on apps_file alone"
    );
}

/// `unix_socket_permissions = 0777` and the 0755 on the socket directory are the
/// entire mechanism for "several apps, under several accounts, on one host".
/// Nothing else observes them: with both tightened to 0700 every generated
/// artefact still reviews correctly, every unit test still passes, and every app
/// running as a different account fails at connect with `Permission denied`.
///
/// A single-account test machine cannot run an app as another user, but the mode
/// bits ARE the kernel's access control — asserting them is exact, not a proxy.
#[test]
fn the_socket_is_reachable_by_an_app_running_as_another_account() {
    let Ok(bins) = db_cluster::discover_pg_bins() else {
        eprintln!("no PostgreSQL discoverable — skipping the socket-mode gate");
        return;
    };
    std::env::set_var("SKY_PG_TUNE_MEM_MB", "512");
    let state = scratch_state_dir("sock");
    provision_cluster(&Opts {
        state_dir: Some(state.clone()),
        start: true,
        max_connections: 20,
        ..Opts::default()
    })
    .unwrap_or_else(|e| panic!("provision failed:\n{e}"));
    let fx = Fixture { layout: Layout::new(&state), bins, user: os_user().unwrap() };
    assert!(cluster_running(&fx.layout), "--start left no cluster running");

    use std::os::unix::fs::PermissionsExt;
    let dir = std::fs::metadata(&fx.layout.socket_dir).unwrap().permissions().mode() & 0o777;
    assert_ne!(
        dir & 0o001,
        0,
        "the socket directory is {dir:o}: an app under another account cannot traverse into it"
    );
    let sock = crate::pg_wire::socket_file(&fx.layout.socket_dir, DEFAULT_PORT);
    let mode = std::fs::metadata(&sock)
        .unwrap_or_else(|e| panic!("no socket at {}: {e}", sock.display()))
        .permissions()
        .mode()
        & 0o777;
    assert_ne!(
        mode & 0o002,
        0,
        "the socket is {mode:o}: connecting to a unix socket needs write, so every app under \
         another account fails with Permission denied"
    );
}

/// A backup is only a backup if it restores. The dump is taken by the generated
/// script exactly as the timer would run it, then restored into a fresh database
/// and read back.
#[test]
fn the_generated_backup_produces_a_restorable_dump() {
    let Some((fx, _a_dsn, a_pw, _b_dsn, b_pw)) = provision_fixture("bak") else {
        return;
    };
    let mut c = connect_as(&fx.layout, "alpha", "alpha", &a_pw).expect("alpha");
    c.execute("CREATE TABLE ledger (id int, note text)").unwrap();
    c.execute("INSERT INTO ledger VALUES (1, 'the row that must survive')").unwrap();
    drop(c);

    // A file old enough to be past the retention window, to prove the retention
    // line does something. `find -mtime` is the only part of the script that
    // cannot be observed from one run.
    let stale = fx.layout.backup_dir.join("alpha-20200101T000000Z.dump");
    std::fs::write(&stale, b"old").unwrap();
    let touched = Command::new("touch")
        .args(["-t", "202001010000"])
        .arg(&stale)
        .status()
        .expect("touch");
    assert!(touched.success());

    let script = fx.layout.service_dir.join("sky-postgres-backup.sh");
    let out = Command::new("/bin/sh").arg(&script).output().expect("sh");
    assert!(
        out.status.success(),
        "the backup script failed:\n{}",
        String::from_utf8_lossy(&out.stderr)
    );
    assert!(!stale.exists(), "the retention window did not delete a 2020 dump");

    let dumps: Vec<PathBuf> = std::fs::read_dir(&fx.layout.backup_dir)
        .unwrap()
        .filter_map(Result::ok)
        .map(|e| e.path())
        .filter(|p| p.file_name().map(|n| n.to_string_lossy().starts_with("alpha-")).unwrap_or(false))
        .collect();
    assert_eq!(dumps.len(), 1, "expected one alpha dump, got {dumps:?}");
    // Both apps are dumped, from the list `--app` maintains — not from a list
    // baked in when the timer was generated.
    assert!(fx.layout.backup_dir.join("..").exists());
    assert!(
        std::fs::read_dir(&fx.layout.backup_dir)
            .unwrap()
            .filter_map(Result::ok)
            .any(|e| e.file_name().to_string_lossy().starts_with("beta-")),
        "beta was provisioned after the timer was generated and was not dumped"
    );

    // --- the dumps are readable by this account and nobody else --------------
    // A dump is every row of an app's database, and `globals-*.sql` is every
    // role's SCRAM verifier. World-readable, another OS account reads both with
    // no authentication, no `CONNECT` and no SQL — the cross-tenant read this
    // phase exists to prevent, taken from the filesystem instead of the server.
    // `umask 077` in the script and the 0700 on the directory are the entire
    // mechanism; these three assertions are what observes them.
    use std::os::unix::fs::PermissionsExt;
    let dir_mode = std::fs::metadata(&fx.layout.backup_dir).unwrap().permissions().mode() & 0o777;
    assert_eq!(
        dir_mode & 0o077,
        0,
        "the backup directory is {dir_mode:o}: another account may list and read every dump"
    );
    let mut checked = 0;
    for e in std::fs::read_dir(&fx.layout.backup_dir).unwrap().filter_map(Result::ok) {
        let name = e.file_name().to_string_lossy().to_string();
        if !(name.ends_with(".dump") || (name.starts_with("globals-") && name.ends_with(".sql"))) {
            continue;
        }
        let mode = e.metadata().unwrap().permissions().mode() & 0o777;
        assert_eq!(mode, 0o600, "{name} is {mode:o}, not 0600");
        checked += 1;
    }
    assert!(checked >= 3, "expected two dumps and a globals file, checked {checked}");

    // Restore into a database that has never seen this data.
    let mut admin = admin_conn(&fx.layout, DEFAULT_PORT, &fx.user, "postgres").unwrap();
    admin.execute("CREATE DATABASE restored").unwrap();
    let dsn = format!(
        "postgresql:///restored?host={}",
        fx.layout.socket_dir.display()
    );
    let out = Command::new(fx.bins.tool("pg_restore"))
        .args(["--dbname", &dsn, "--no-owner", "--no-privileges"])
        .arg(&dumps[0])
        .output()
        .expect("pg_restore");
    assert!(
        out.status.success(),
        "pg_restore failed:\n{}",
        String::from_utf8_lossy(&out.stderr)
    );
    let mut r = admin_conn(&fx.layout, DEFAULT_PORT, &fx.user, "restored").unwrap();
    assert_eq!(
        r.scalar("SELECT note FROM ledger WHERE id = 1").unwrap().as_deref(),
        Some("the row that must survive"),
        "the dump restored, and the row was not in it"
    );
    drop(r);
    drop(admin);

    // --- the recovery an operator actually performs ---------------------------
    // The restore above is the one that proves the dump holds the data, and it
    // is also the shape that quietly undoes the boundary: `pg_restore --dbname
    // <a database made by hand>` skips the archive's DATABASE-section entries,
    // where the `REVOKE … FROM PUBLIC` lives. The recovered database then
    // carries `PUBLIC`'s default `CONNECT` and every app role on the cluster can
    // read it — the cross-tenant read this phase exists to prevent, reintroduced
    // by the recovery of the app it protects.
    //
    // So the documented path is run against the real thing: alpha is dropped as
    // a disaster would drop it, rebuilt with `pg_restore --create`, and beta is
    // then asked. The falsifying mutation is `app_cluster_sql`'s `REVOKE ALL ON
    // DATABASE … FROM PUBLIC` — with it deleted there is no ACL in the archive
    // to carry, and beta reads the recovered database.
    let mut admin = admin_conn(&fx.layout, DEFAULT_PORT, &fx.user, "postgres").unwrap();
    admin.execute("DROP DATABASE alpha").expect("alpha could not be dropped");
    let out = Command::new(fx.bins.tool("pg_restore"))
        .args([
            "--create",
            "--dbname",
            &format!("postgresql:///postgres?host={}", fx.layout.socket_dir.display()),
        ])
        .arg(&dumps[0])
        .output()
        .expect("pg_restore --create");
    assert!(
        out.status.success(),
        "the documented restore failed:\n{}",
        String::from_utf8_lossy(&out.stderr)
    );
    let mut a = connect_as(&fx.layout, "alpha", "alpha", &a_pw).expect("alpha after the restore");
    assert_eq!(
        a.scalar("SELECT note FROM ledger WHERE id = 1").unwrap().as_deref(),
        Some("the row that must survive"),
        "the recovered database does not hold the row"
    );
    drop(a);
    match connect_as(&fx.layout, "beta", "alpha", &b_pw) {
        Err(e) => assert_eq!(e.sqlstate(), Some("42501"), "expected insufficient_privilege, got: {e}"),
        Ok(mut c) => {
            let seen = c.query("SELECT note FROM ledger");
            panic!("beta read the RECOVERED alpha and got {seen:?}: the restore lost the REVOKE");
        }
    }
}

/// Re-running `--app` must CONVERGE a database whose ACL has drifted back to
/// the PUBLIC default — that is what `app_regrant_sql` is for, and nothing named
/// it, by text or by effect.
///
/// The drift is not hypothetical and the repo already documents the way in: a
/// `pg_restore` into a rebuilt database, which is precisely what the backup
/// test's own recovery path does. `CREATE DATABASE` gives `PUBLIC` `CONNECT`
/// and `TEMPORARY` by default, so a database recreated from a dump has every
/// app role able to connect to it again until something takes them away. The
/// documented remedy is to re-run `--app`, and `app_regrant_sql` is the two
/// statements that make that true.
///
/// Asserted by EFFECT, not by matching the SQL: the ACL is reset to the default,
/// beta is shown to reach alpha (the positive control — without it the refusal
/// afterwards proves nothing), `--app alpha` is re-run, and beta is refused with
/// `42501` again.
///
/// The falsifying mutation is deleting the `REVOKE` from `app_regrant_sql`.
#[test]
fn re_provisioning_converges_a_database_acl_that_drifted_back_to_the_default() {
    let Some((fx, _a_dsn, _a_pw, _b_dsn, b_pw)) = provision_fixture("regrant") else {
        return;
    };
    let mut admin = admin_conn(&fx.layout, DEFAULT_PORT, &fx.user, "postgres").unwrap();

    // Precondition: the boundary is up. This is the state the whole phase claims.
    match connect_as(&fx.layout, "beta", "alpha", &b_pw) {
        Err(e) => assert_eq!(e.sqlstate(), Some("42501"), "expected insufficient_privilege: {e}"),
        Ok(_) => panic!("beta reached alpha's database before the ACL was even touched"),
    }

    // The drift: what `CREATE DATABASE` leaves behind, which is what a database
    // rebuilt from a dump has.
    admin.execute("GRANT CONNECT, TEMPORARY ON DATABASE alpha TO PUBLIC").unwrap();
    // Positive control: the reset really did open it. Without this, a re-run
    // that did nothing at all would still pass the assertion below.
    let opened = connect_as(&fx.layout, "beta", "alpha", &b_pw)
        .expect("the ACL reset did not open the database; the fixture is wrong, not the code");
    drop(opened);

    provision_app(&Opts {
        state_dir: Some(fx.layout.state_dir.clone()),
        app: Some("alpha".into()),
        ..Opts::default()
    })
    .expect("re-provisioning alpha failed");

    match connect_as(&fx.layout, "beta", "alpha", &b_pw) {
        Err(e) => assert_eq!(
            e.sqlstate(),
            Some("42501"),
            "beta was refused, but not for want of privilege: {e}"
        ),
        Ok(mut c) => {
            let seen = c.query("SELECT tablename FROM pg_tables WHERE schemaname = 'public'");
            panic!(
                "beta still reaches alpha's database after a re-provision, and read {seen:?}: \
                 the convergence path does not converge"
            );
        }
    }
    // And alpha itself still works — the REVOKE did not take the owner with it.
    let out = provision_app(&Opts {
        state_dir: Some(fx.layout.state_dir.clone()),
        app: Some("alpha".into()),
        rotate: true,
        ..Opts::default()
    })
    .expect("--rotate-password after the re-provision failed");
    let (_, pw) = dsn_and_password(&out, "alpha");
    connect_as(&fx.layout, "alpha", "alpha", &pw).expect("alpha lost access to its own database");
}

/// `reload_hba` claims to PROVE the reload took. The proof needs one test in
/// which it did not.
///
/// The existing adopted-cluster gate observes the OUTCOME — a wrong password is
/// refused afterwards — and against a cluster where the reload genuinely
/// happens, that passes whatever the proof does. So the proof itself was
/// unobserved, and it is one character from vacuous: `pg_conf_load_time() >
/// before` compares a monotonic clock against its own earlier reading, and with
/// `>=` the first poll returns true unconditionally. The 15-second loop and the
/// refusal it guards become dead code, and an adopted cluster that silently kept
/// its `trust` rules would be reported ready.
///
/// Driving the failure needs a cluster where `pg_reload_conf()` succeeds and the
/// load time does NOT advance — killing the postmaster would give an `Err` for
/// the wrong reason, proving only that a dead server errors. So
/// `pg_conf_load_time` is shadowed with a constant: `search_path` names
/// `pg_catalog` explicitly and puts a schema of sky's own in front of it, which
/// is the one way to get in front of a built-in. Every backend then reads the
/// same frozen value, `before` included, and `frozen > frozen` is false while
/// `frozen >= frozen` is true. That is exactly the mutation, and nothing else
/// about the cluster is disturbed.
///
/// Generalising, since this is a class rather than one helper: every "and prove
/// it took" needs one test in which the thing did not take.
#[test]
fn a_reload_that_cannot_be_proved_to_have_taken_is_refused() {
    let Some((fx, _a_dsn, _a_pw, _b_dsn, _b_pw)) = provision_fixture("reload") else {
        return;
    };
    // Positive control first: against the cluster as it stands, the proof
    // succeeds. Without this the refusal below could be a reload_hba that never
    // returns Ok at all.
    reload_hba(&fx.layout, DEFAULT_PORT, &fx.user).expect("a real reload could not be proved");

    let mut admin = admin_conn(&fx.layout, DEFAULT_PORT, &fx.user, "postgres").unwrap();
    admin.execute("CREATE SCHEMA sky_frozen_clock").unwrap();
    admin
        .execute(
            "CREATE FUNCTION sky_frozen_clock.pg_conf_load_time() RETURNS timestamptz \
             LANGUAGE sql IMMUTABLE AS $$ SELECT '2001-01-01 00:00:00+00'::timestamptz $$",
        )
        .unwrap();
    // pg_catalog is searched FIRST unless it is named, so naming it is what puts
    // the shadow in front of it.
    admin
        .execute("ALTER DATABASE postgres SET search_path = sky_frozen_clock, pg_catalog, public")
        .unwrap();

    // The fixture works: a NEW connection reads the frozen value.
    let frozen = admin_conn(&fx.layout, DEFAULT_PORT, &fx.user, "postgres")
        .unwrap()
        .scalar("SELECT pg_conf_load_time()::text")
        .unwrap()
        .expect("no load time");
    assert!(
        frozen.starts_with("2001-01-01"),
        "the shadow is not in effect ({frozen}); the test is wrong, not the code"
    );

    let e = reload_hba(&fx.layout, DEFAULT_PORT, &fx.user)
        .expect_err("a reload that cannot be shown to have taken was reported as having taken");
    assert!(
        e.contains("did not take within 15s"),
        "refused for the wrong reason:\n{e}"
    );

    // Restore, and prove the restoration: the same call succeeds again, so the
    // refusal above was the shadow and not some state the test left behind.
    admin.execute("ALTER DATABASE postgres RESET search_path").unwrap();
    admin.execute("DROP SCHEMA sky_frozen_clock CASCADE").unwrap();
    reload_hba(&fx.layout, DEFAULT_PORT, &fx.user)
        .expect("reload_hba stayed broken after the shadow was removed");
}

/// The generated launchd jobs are handed to `plutil -lint`, Apple's own parser.
/// The systemd unit has no equivalent validator on this platform, which is why
/// `tests.rs` checks its structure instead — and why the report says so.
#[test]
fn the_generated_launchd_jobs_pass_apples_own_parser() {
    if !cfg!(target_os = "macos") {
        return;
    }
    let dir = scratch_state_dir("plist");
    std::fs::create_dir_all(&dir).unwrap();
    let spec = ServiceSpec {
        layout: Layout::new(&dir),
        postgres: PathBuf::from("/opt/pg/bin/postgres"),
        pg_dump: PathBuf::from("/opt/pg/bin/pg_dump"),
        pg_dumpall: PathBuf::from("/opt/pg/bin/pg_dumpall"),
        user: "skypg".into(),
        listen: Listen::default(),
        backup_keep_days: 14,
        backup_at: (3, 30),
    };
    for (name, body) in [
        ("org.sky.postgres.plist", launchd_plist(&spec)),
        ("org.sky.postgres-backup.plist", launchd_backup_plist(&spec)),
    ] {
        let p = dir.join(name);
        std::fs::write(&p, body).unwrap();
        let out = Command::new("plutil").arg("-lint").arg(&p).output().expect("plutil");
        assert!(
            out.status.success(),
            "{name} is not a valid plist:\n{}\n{}",
            String::from_utf8_lossy(&out.stdout),
            String::from_utf8_lossy(&out.stderr)
        );
    }
    // The wrapper is a shell script, and `sh -n` is its parser.
    let w = dir.join("sky-postgres-run.sh");
    std::fs::write(&w, launchd_wrapper(&spec)).unwrap();
    let out = Command::new("/bin/sh").arg("-n").arg(&w).output().expect("sh -n");
    assert!(out.status.success(), "{}", String::from_utf8_lossy(&out.stderr));
    let b = dir.join("backup.sh");
    std::fs::write(&b, backup_script(&spec)).unwrap();
    let out = Command::new("/bin/sh").arg("-n").arg(&b).output().expect("sh -n");
    assert!(out.status.success(), "{}", String::from_utf8_lossy(&out.stderr));
    let _ = std::fs::remove_dir_all(&dir);
}

/// The launchd wrapper's whole reason for existing, exercised against a live
/// postmaster: SIGTERM to the wrapper must produce a **fast** shutdown, not the
/// smart shutdown PostgreSQL performs on a SIGTERM it receives directly (which
/// waits for every client, forever). launchd itself cannot be driven from a
/// test, but the thing the wrapper has to get right can be.
#[test]
fn the_launchd_wrapper_turns_sigterm_into_a_fast_shutdown() {
    let Ok(bins) = db_cluster::discover_pg_bins() else {
        eprintln!("no PostgreSQL discoverable — skipping the wrapper shutdown gate");
        return;
    };
    std::env::set_var("SKY_PG_TUNE_MEM_MB", "512");
    let state = scratch_state_dir("wrap");
    let opts = Opts {
        state_dir: Some(state.clone()),
        service: true,
        max_connections: 20,
        ..Opts::default()
    };
    provision_cluster(&opts).unwrap_or_else(|e| panic!("provision failed:\n{e}"));
    let fx = Fixture { layout: Layout::new(&state), bins, user: os_user().unwrap() };
    assert!(!cluster_running(&fx.layout), "the fixture wanted a stopped cluster");

    let wrapper = fx.layout.service_dir.join("sky-postgres-run.sh");
    let mut child = Command::new("/bin/sh")
        .arg(&wrapper)
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .expect("wrapper");

    // Wait for the postmaster to accept connections, then hold one open: a smart
    // shutdown would block on exactly this connection, which is the failure the
    // wrapper exists to prevent.
    let mut held = None;
    for _ in 0..100 {
        std::thread::sleep(std::time::Duration::from_millis(200));
        if let Ok(c) = admin_conn(&fx.layout, DEFAULT_PORT, &fx.user, "postgres") {
            held = Some(c);
            break;
        }
    }
    assert!(held.is_some(), "the wrapper never brought the cluster up");

    let pid = child.id() as i32;
    nix::sys::signal::kill(nix::unistd::Pid::from_raw(pid), nix::sys::signal::Signal::SIGTERM)
        .expect("kill");

    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(30);
    let mut exited = false;
    while std::time::Instant::now() < deadline {
        if let Ok(Some(_)) = child.try_wait() {
            exited = true;
            break;
        }
        std::thread::sleep(std::time::Duration::from_millis(100));
    }
    if !exited {
        let _ = child.kill();
    }
    // Reaped on every path, including the failing one: a zombie wrapper would
    // outlive the test run. `wait` after a `try_wait` that already reaped simply
    // returns the stored status.
    let _ = child.wait();
    assert!(
        exited,
        "the wrapper did not exit within 30s of SIGTERM — PostgreSQL took it as a \
         SMART shutdown and is waiting for the open connection"
    );
    assert!(
        !cluster_running(&fx.layout),
        "the wrapper exited while the postmaster was still running"
    );
    drop(held);
}

/// Re-running `--app` for an app that exists must not print a DSN, because the
/// password it would have to name cannot be read back — PostgreSQL stores a
/// SCRAM verifier. Printing one would mean inventing it, and the operator would
/// paste a password nothing accepts.
#[test]
fn re_provisioning_an_app_is_idempotent_and_prints_no_invented_dsn() {
    let Some((fx, _a_dsn, a_pw, _b, _bp)) = provision_fixture("idem") else {
        return;
    };
    let again = provision_app(&Opts {
        state_dir: Some(fx.layout.state_dir.clone()),
        app: Some("alpha".into()),
        ..Opts::default()
    })
    .expect("second --app");
    assert!(again.contains("already exists"), "{again}");
    assert!(!again.contains("postgresql://"), "a DSN was invented:\n{again}");
    // The original password still works.
    connect_as(&fx.layout, "alpha", "alpha", &a_pw).expect("the re-run changed the password");

    let rotated = provision_app(&Opts {
        state_dir: Some(fx.layout.state_dir.clone()),
        app: Some("alpha".into()),
        rotate: true,
        ..Opts::default()
    })
    .expect("--rotate-password");
    let (_, new_pw) = dsn_and_password(&rotated, "alpha");
    assert_ne!(new_pw, a_pw);
    connect_as(&fx.layout, "alpha", "alpha", &new_pw).expect("the rotated password does not work");
    assert!(
        connect_as(&fx.layout, "alpha", "alpha", &a_pw).is_err(),
        "the old password still works after a rotation"
    );
    // The app list the backup script reads has each app once, however many times
    // it was provisioned.
    let apps = std::fs::read_to_string(&fx.layout.apps_file).unwrap();
    assert_eq!(apps.lines().filter(|l| *l == "alpha").count(), 1, "{apps}");
}

/// `verify_the_server_reads_skys_files` is the guard on the whole hardening
/// story, and nothing drove it.
///
/// `if false && !configuration_paths_agree(…)` left the crate at 342 passed, 0
/// failed. What it guards is not exotic: a cluster can be told where its
/// configuration lives, and every Debian-family package does exactly that —
/// both files under `/etc/postgresql`, the copies in the data directory inert.
/// **Adoption of an existing cluster is the documented primary case.** Without
/// this check sky writes a hardened `pg_hba.conf` nobody reads, reloads it
/// successfully, and reports a cluster ready whose authentication it never
/// changed.
///
/// Both settings are driven, because they need different mechanisms and a test
/// of one says nothing about the other:
///
/// * `hba_file` is settable in `postgresql.conf`;
/// * `config_file` is not — it only exists on the postmaster's command line,
///   which is why this phase restarts through `pg_ctl -o`. `hba_file` has to be
///   pinned back to the data directory in that phase, or it would move with
///   `config_file` (it defaults to the config file's directory) and the first
///   arm would fire before the second was ever reached.
#[test]
fn a_cluster_that_reads_its_configuration_from_elsewhere_is_refused() {
    let Some((fx, _a_dsn, _a_pw, _b_dsn, _b_pw)) = provision_fixture("cfgpath") else {
        return;
    };
    let port = DEFAULT_PORT;
    let conf = fx.layout.data_dir.join("postgresql.conf");
    let original_conf = std::fs::read_to_string(&conf).expect("postgresql.conf");

    // Positive control: as provisioned, sky and the server agree.
    verify_the_server_reads_skys_files(&fx.layout, port, &fx.user)
        .expect("the cluster sky just provisioned does not read sky's files");

    // The distribution-package shape, minus the distribution: a second copy of
    // pg_hba.conf somewhere else, and a cluster told to read that one.
    let etc = fx.layout.state_dir.join("etc");
    std::fs::create_dir_all(&etc).unwrap();
    let shadow_hba = etc.join("pg_hba.conf");
    std::fs::copy(fx.layout.data_dir.join("pg_hba.conf"), &shadow_hba).unwrap();

    // ── phase 1: hba_file elsewhere ────────────────────────────────
    std::fs::write(
        &conf,
        format!("{original_conf}\nhba_file = '{}'\n", shadow_hba.display()),
    )
    .unwrap();
    stop_postmaster(&fx.bins, &fx.layout).expect("stop");
    start_postmaster(&fx.bins, &fx.layout).expect("restart with a shadowed hba_file");

    let e = verify_the_server_reads_skys_files(&fx.layout, port, &fx.user).expect_err(
        "sky would have hardened a pg_hba.conf this cluster does not read, and reported it ready",
    );
    assert!(
        e.contains("reads its hba_file from") && e.contains(shadow_hba.to_str().unwrap()),
        "refused, but not for the shadowed hba_file:\n{e}"
    );

    // ── phase 2: config_file elsewhere, hba_file pinned back ───────
    let shadow_conf = etc.join("postgresql.conf");
    std::fs::write(
        &shadow_conf,
        format!(
            "{original_conf}\ndata_directory = '{}'\nhba_file = '{}'\n",
            fx.layout.data_dir.display(),
            fx.layout.data_dir.join("pg_hba.conf").display()
        ),
    )
    .unwrap();
    std::fs::write(&conf, &original_conf).unwrap();
    stop_postmaster(&fx.bins, &fx.layout).expect("stop");
    let out = Command::new(fx.bins.tool("pg_ctl"))
        .arg("-D")
        .arg(&fx.layout.data_dir)
        .arg("-l")
        .arg(fx.layout.log_file())
        .arg("-o")
        .arg(format!("-c config_file={}", shadow_conf.display()))
        .args(["-w", "-t", "60", "start"])
        .output()
        .expect("pg_ctl");
    assert!(
        out.status.success(),
        "the cluster did not restart with an external config_file:\n{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );

    let e = verify_the_server_reads_skys_files(&fx.layout, port, &fx.user)
        .expect_err("an external config_file was accepted");
    assert!(
        e.contains("reads its config_file from") && e.contains(shadow_conf.to_str().unwrap()),
        "refused, but not for the shadowed config_file:\n{e}"
    );

    // Restore, and prove the restoration — otherwise both refusals above could
    // be some state the test left behind rather than the shadowing.
    stop_postmaster(&fx.bins, &fx.layout).expect("stop");
    std::fs::write(&conf, &original_conf).unwrap();
    start_postmaster(&fx.bins, &fx.layout).expect("restart unshadowed");
    verify_the_server_reads_skys_files(&fx.layout, port, &fx.user)
        .expect("the check stayed broken after the shadowing was removed");
}

/// `reload_hba`'s OTHER half. The existing gate drives the load-time proof; the
/// parse arm in front of it was never reached.
///
/// `if false &&` on the `pg_hba_file_rules` branch left the crate green, and
/// what it protects is the reason the load-time proof is not sufficient on its
/// own: a `pg_hba.conf` the postmaster cannot parse is logged and DISCARDED,
/// the previous rules are kept, and `pg_conf_load_time()` advances anyway —
/// because the reload did happen, it just kept nothing. So the load-time proof
/// alone would report a cluster hardened that is still running whatever rules
/// it started with, which on an adopted cluster means `trust`.
#[test]
fn a_pg_hba_conf_that_does_not_parse_is_refused_rather_than_reloaded() {
    let Some((fx, _a_dsn, _a_pw, _b_dsn, _b_pw)) = provision_fixture("hbaparse") else {
        return;
    };
    let port = DEFAULT_PORT;
    let hba = fx.layout.data_dir.join("pg_hba.conf");
    let good = std::fs::read_to_string(&hba).expect("pg_hba.conf");

    // Positive control, so the refusal below cannot be a reload_hba that never
    // returns Ok at all.
    reload_hba(&fx.layout, port, &fx.user).expect("a real reload could not be proved");

    // A line PostgreSQL parses and rejects — `pg_hba_file_rules.error` carries
    // the reason. A syntactically absent field would be reported the same way,
    // but naming a method that does not exist is what a hand-edited file
    // actually looks like.
    std::fs::write(&hba, format!("{good}\nlocal all all not-an-auth-method\n")).unwrap();

    let e = reload_hba(&fx.layout, port, &fx.user)
        .expect_err("a pg_hba.conf the server cannot parse was reloaded and reported as taken");
    assert!(
        e.contains("does not parse") && e.contains("keep the rules it started with"),
        "refused for the wrong reason:\n{e}"
    );

    // Restore, and prove it: the same call succeeds again.
    std::fs::write(&hba, &good).unwrap();
    reload_hba(&fx.layout, port, &fx.user)
        .expect("reload_hba stayed broken after the bad line was removed");
}

/// `--app` against a cluster nobody hardened.
///
/// Its only guard was that `PG_VERSION` exists. It ran none of what `--shared`
/// runs — not `verify_the_server_reads_skys_files`, not `reload_hba`, not the
/// hardening SQL — and then printed a DSN and the sentence "refused by every
/// database sky provisioned but alpha". Against a cluster still carrying
/// `initdb`'s `local all all trust`, that sentence is false in the way that
/// matters: any local process may connect as any role by claiming to be it, and
/// every REVOKE behind it is decoration.
///
/// Reachable by operator deviation from the documented `--shared`-first flow —
/// a `--state-dir` pointed at an existing cluster — which is exactly the
/// deviation an operator makes when they already have a PostgreSQL and think
/// `--app` is the part they need.
///
/// The refusal is by ATTEMPT, so this test drives it the same way: put the
/// cluster back on `trust`, reload, and require `--app` to refuse. Reading the
/// file back would prove what the file says; a running postmaster enforces what
/// it read at startup, which is why the file is not the question.
#[test]
fn an_app_is_refused_against_a_cluster_that_does_not_ask_for_a_password() {
    let Some((fx, _a_dsn, _a_pw, _b_dsn, _b_pw)) = provision_fixture("unhardened") else {
        return;
    };
    let hba = fx.layout.data_dir.join("pg_hba.conf");
    let hardened = std::fs::read_to_string(&hba).expect("pg_hba.conf");

    // Positive control: as hardened, --app works.
    provision_app(&Opts {
        state_dir: Some(fx.layout.state_dir.clone()),
        app: Some("gamma".into()),
        ..Opts::default()
    })
    .expect("--app failed against the cluster sky hardened");

    // initdb's own file, which is what an operator's un-provisioned cluster is
    // running: first match wins, and this one matches everything.
    std::fs::write(
        &hba,
        "local   all             all                                     trust\n\
         host    all             all             127.0.0.1/32            trust\n",
    )
    .unwrap();
    let mut admin = admin_conn(&fx.layout, DEFAULT_PORT, &fx.user, "postgres").unwrap();
    admin.scalar("SELECT pg_reload_conf()").unwrap();
    // The fixture works: the cluster really is on trust now.
    connect_as(&fx.layout, "gamma", "gamma", "not-gammas-password")
        .expect("the fixture did not put the cluster on trust; the test is wrong, not the code");

    let e = provision_app(&Opts {
        state_dir: Some(fx.layout.state_dir.clone()),
        app: Some("delta".into()),
        ..Opts::default()
    })
    .expect_err("sky issued app credentials, and the boundary claim, against a `trust` cluster");
    assert!(
        e.contains("accepted a connection as") && e.contains("password that is not delta's"),
        "refused for the wrong reason:\n{e}"
    );

    // Restore, and prove the restoration.
    std::fs::write(&hba, &hardened).unwrap();
    admin.scalar("SELECT pg_reload_conf()").unwrap();
    provision_app(&Opts {
        state_dir: Some(fx.layout.state_dir.clone()),
        app: Some("delta".into()),
        ..Opts::default()
    })
    .expect("--app stayed broken after the cluster was hardened again");
}

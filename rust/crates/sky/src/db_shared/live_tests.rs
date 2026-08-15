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
}

/// A backup is only a backup if it restores. The dump is taken by the generated
/// script exactly as the timer would run it, then restored into a fresh database
/// and read back.
#[test]
fn the_generated_backup_produces_a_restorable_dump() {
    let Some((fx, _a_dsn, a_pw, _b_dsn, _b_pw)) = provision_fixture("bak") else {
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

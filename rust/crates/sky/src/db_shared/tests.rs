//! Unit gates for the shared-cluster derivations.
//!
//! These prove the *artefacts* — the SQL, the conf block, the unit files, the
//! backup script. They deliberately do NOT claim the security boundary holds:
//! reading the generated `REVOKE` back and asserting it says `REVOKE` proves the
//! string was generated, not that PostgreSQL refuses anything. That claim is
//! `tests/db_shared_flow.rs`'s, and it is made by connecting as one app's role
//! and attempting to read another app's database.

use super::*;

/// A host with `posix_fadvise`, i.e. Linux — the tuning that carries
/// `effective_io_concurrency`. The platform without it has its own gate below.
fn facts(gib: u64, cpus: u32) -> HostFacts {
    HostFacts { mem_bytes: gib * 1024 * 1024 * 1024, cpus, posix_fadvise: true }
}

fn spec() -> ServiceSpec {
    ServiceSpec {
        layout: Layout::new(Path::new("/var/lib/sky")),
        postgres: PathBuf::from("/opt/pg/bin/postgres"),
        pg_dump: PathBuf::from("/opt/pg/bin/pg_dump"),
        pg_dumpall: PathBuf::from("/opt/pg/bin/pg_dumpall"),
        user: "skypg".to_string(),
        listen: Listen::default(),
        backup_keep_days: 14,
        backup_at: (3, 30),
    }
}

// ---- layout and refusals -------------------------------------------------

/// The socket directory is a SIBLING of the data directory, never a child.
/// PostgreSQL requires the data directory to be 0700, so a socket inside it is
/// unreachable by any other user — which is every app on a shared host.
#[test]
fn the_socket_directory_is_not_inside_the_data_directory() {
    let l = Layout::new(Path::new("/var/lib/sky"));
    assert_eq!(l.data_dir, Path::new("/var/lib/sky/pg"));
    assert_eq!(l.socket_dir, Path::new("/var/lib/sky/run"));
    assert!(!l.socket_dir.starts_with(&l.data_dir));
}

#[test]
fn an_ephemeral_state_directory_is_refused() {
    for dir in ["/tmp/sky", "/var/tmp/sky", "/dev/shm/sky", "/var/folders/xy/sky"] {
        let e = state_dir_error(Path::new(dir)).unwrap_or_else(|| panic!("{dir} was accepted"));
        assert!(e.contains("which the system empties"), "{dir}: {e}");
    }
    // The default is not one of them.
    assert_eq!(state_dir_error(&default_state_dir()), None);
}

#[test]
fn a_relative_state_directory_is_refused() {
    let e = state_dir_error(Path::new("sky-state")).unwrap();
    assert!(e.contains("absolute"), "{e}");
}

/// `pg_ctl start` hands one string to `/bin/sh -c`, so a `$(…)` anywhere in the
/// paths it interpolates is executed. Phase 3 closed this for the per-project
/// supervisor; the same predicate has to cover the state directory, which is
/// operator-supplied and therefore the least trustworthy path of the lot.
#[test]
fn a_shell_unsafe_state_directory_is_refused() {
    let e = state_dir_error(Path::new("/srv/inj$(touch pwned)dir")).unwrap();
    assert!(e.contains("/bin/sh"), "{e}");
}

#[test]
fn a_state_directory_inside_a_sky_project_is_refused() {
    let dir = std::env::temp_dir().join(format!("sky-p6-proj-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(dir.join("state")).unwrap();
    std::fs::write(dir.join("sky.toml"), "[project]\nname = \"x\"\n").unwrap();
    // Resolve through /private on macOS so the ephemeral check is not what fires.
    let inside = dir.canonicalize().unwrap().join("state");
    let e = state_dir_error(&inside).unwrap();
    assert!(
        e.contains("inside the Sky project") || e.contains("which the system empties"),
        "{e}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

// ---- tuning --------------------------------------------------------------

/// The whole point of the phase's tuning: it is NOT the development profile.
/// `db_cluster::sky_conf_block` pins `shared_buffers = 32MB` so several idle
/// project clusters stay cheap; a cluster serving every app on a host that did
/// the same would serve production traffic out of 32MB of cache.
#[test]
fn production_tuning_is_derived_from_the_host_not_pinned_small() {
    let big = tuning_block(
        &facts(64, 16),
        200,
        &Listen::default(),
        Path::new("/var/lib/sky/run"),
    );
    let small = tuning_block(
        &facts(2, 2),
        100,
        &Listen::default(),
        Path::new("/var/lib/sky/run"),
    );
    assert!(big.contains("shared_buffers = 8192MB"), "{big}");
    assert!(small.contains("shared_buffers = 512MB"), "{small}");
    assert!(big.contains("max_worker_processes = 16"));
    assert!(small.contains("max_worker_processes = 8"));
    assert!(!big.contains("shared_buffers = 32MB"));
    assert!(db_cluster::sky_conf_block().contains("shared_buffers = 32MB"));
}

/// The development profile's rule, restated: nothing here may change what a
/// query means, or an app tested against one cluster is not tested against the
/// other. This is a whitelist, so a future setting has to be considered.
#[test]
fn the_tuning_block_sets_no_semantic_setting() {
    let block = tuning_block(
        &facts(8, 4),
        200,
        &Listen::default(),
        Path::new("/var/lib/sky/run"),
    );
    for forbidden in [
        "fsync",
        "synchronous_commit",
        "wal_level",
        "full_page_writes",
        "default_transaction_isolation",
        "standard_conforming_strings",
        "search_path",
        "datestyle",
        "timezone",
        "lc_numeric",
    ] {
        assert!(
            !block.to_ascii_lowercase().contains(forbidden),
            "the tuning block sets {forbidden}, which changes what a query means:\n{block}"
        );
    }
}

/// Re-tuning replaces the block. An append-only block would leave the previous
/// values above the new ones — which PostgreSQL happens to resolve correctly
/// (last occurrence wins) and no operator can read.
#[test]
fn re_tuning_replaces_the_managed_block_rather_than_stacking_it() {
    let base = "# PostgreSQL configuration\nlisten_addresses = 'localhost'\n";
    let first = apply_managed_block(base, &tuning_block(
        &facts(4, 4),
        100,
        &Listen::default(),
        Path::new("/var/lib/sky/run"),
    ));
    let second = apply_managed_block(&first, &tuning_block(
        &facts(32, 8),
        200,
        &Listen::default(),
        Path::new("/var/lib/sky/run"),
    ));
    assert_eq!(second.matches(CONF_BEGIN).count(), 1, "{second}");
    assert_eq!(second.matches(CONF_END).count(), 1);
    assert!(second.contains("shared_buffers = 8192MB"), "{second}");
    assert!(!second.contains("shared_buffers = 1024MB"), "{second}");
    // Whatever the operator had is still there.
    assert!(second.contains("# PostgreSQL configuration"));
    // And it is a fixed point.
    let third = apply_managed_block(&second, &tuning_block(
        &facts(32, 8),
        200,
        &Listen::default(),
        Path::new("/var/lib/sky/run"),
    ));
    assert_eq!(second, third);
}

/// An interrupted write can leave a begin marker with no end. Merging the new
/// block with that wreckage would produce a file with two `shared_buffers` and
/// no way to tell which was meant.
#[test]
fn a_truncated_managed_block_is_replaced_not_merged() {
    let broken = format!("port = 5432\n{CONF_BEGIN}\nshared_buffers = 99MB\n");
    let fixed = apply_managed_block(&broken, &tuning_block(
        &facts(4, 2),
        100,
        &Listen::default(),
        Path::new("/var/lib/sky/run"),
    ));
    assert!(!fixed.contains("99MB"), "{fixed}");
    assert_eq!(fixed.matches(CONF_BEGIN).count(), 1);
}

/// A tuning block that sets `effective_io_concurrency` on a platform without
/// `posix_fadvise` does not produce a slower cluster — it produces a cluster
/// that **will not start**: PostgreSQL treats it as a configuration error and
/// exits `FATAL` before accepting a connection. macOS is such a platform, so an
/// unconditional `200` breaks the first machine anyone tries this on. (It did:
/// the live gate below found it, four tests at once.)
#[test]
fn a_platform_without_posix_fadvise_gets_no_effective_io_concurrency() {
    let mut h = facts(8, 4);
    assert!(tuning_block(&h, 100, &Listen::default(), Path::new("/run")).contains("effective_io_concurrency = 200"));
    h.posix_fadvise = false;
    let block = tuning_block(&h, 100, &Listen::default(), Path::new("/run"));
    assert!(
        !block.lines().any(|l| !l.trim_start().starts_with('#') && l.contains("effective_io_concurrency")),
        "{block}"
    );
    // And what the current host reports is what the current host is.
    assert_eq!(detect_host().posix_fadvise, cfg!(target_os = "linux"));
}

#[test]
fn conf_settings_are_read_with_the_last_occurrence_winning() {
    let conf = "port = 5432\n# port = 9999\nport = 6000\nlisten_addresses = ''\n";
    assert_eq!(conf_setting(conf, "port").as_deref(), Some("6000"));
    assert_eq!(conf_setting(conf, "listen_addresses").as_deref(), Some(""));
    assert_eq!(conf_setting(conf, "nope"), None);
}

// ---- pg_hba --------------------------------------------------------------

/// The ordering trap. `initdb` writes `local all all trust`; a `scram-sha-256`
/// rule appended below it is NEVER REACHED, so every app would authenticate with
/// `trust` and any local process could connect as any role. sky therefore
/// generates the whole file, and this asserts the order it generates.
#[test]
fn the_superuser_rule_precedes_the_app_rule_and_nothing_uses_trust() {
    let hba = pg_hba("skypg", &Listen::default());
    let rules: Vec<&str> = hba
        .lines()
        .filter(|l| !l.trim_start().starts_with('#') && !l.trim().is_empty())
        .collect();
    assert_eq!(rules.len(), 3, "{hba}");
    assert!(rules[0].starts_with("local") && rules[0].contains("skypg") && rules[0].ends_with("peer"));
    assert!(rules[2].contains(" all ") && rules[2].ends_with("scram-sha-256"));
    assert!(
        !hba.lines().any(|l| !l.trim_start().starts_with('#') && (l.contains("trust") || l.contains("md5"))),
        "a trust or md5 rule would make every REVOKE decoration:\n{hba}"
    );
}

#[test]
fn tcp_rules_appear_only_when_a_listen_address_was_asked_for() {
    let socket_only = pg_hba("skypg", &Listen::default());
    assert!(!socket_only.contains("host    all"));
    let tcp = pg_hba("skypg", &Listen { addr: Some("127.0.0.1".into()), port: 5432 });
    assert!(tcp.contains("host    all             all             127.0.0.1/32            scram-sha-256"));
    assert!(!tcp.lines().any(|l| !l.trim_start().starts_with('#') && l.contains("0.0.0.0/0")));
}

// ---- the app SQL ---------------------------------------------------------

/// `PUBLIC` is the trap. Every role is implicitly a member and may connect to
/// every database by default, so database-per-app plus role-per-app buys nothing
/// until it is revoked. This asserts the revoke is present and that it precedes
/// the grant — a `GRANT` before a `REVOKE ALL` would be undone by it.
#[test]
fn the_app_sql_revokes_public_before_granting_the_app() {
    let sql = app_cluster_sql("alpha", "hunter2");
    let joined = sql.join(";\n");
    let revoke = joined.find("REVOKE ALL ON DATABASE").expect(&joined);
    let grant = joined.find("GRANT CONNECT, TEMPORARY").expect(&joined);
    assert!(revoke < grant, "the grant would be undone by the revoke:\n{joined}");
    assert!(joined.contains("CREATE DATABASE \"alpha\" OWNER \"alpha\""));
    for attr in ["NOSUPERUSER", "NOCREATEDB", "NOCREATEROLE", "NOREPLICATION", "NOBYPASSRLS"] {
        assert!(joined.contains(attr), "the role is created without {attr}:\n{joined}");
    }
}

/// Before PostgreSQL 15 `PUBLIC` holds CREATE on every `public` schema —
/// including `template1`'s, which is copied into every database created after
/// it. The bundle pins 18.6, where the default is already closed, but a shared
/// cluster may be an operator's existing server of any version.
#[test]
fn template1_is_hardened_as_well_as_postgres() {
    let t1 = cluster_hardening_sql("template1");
    assert!(t1.iter().any(|s| s == "REVOKE ALL ON SCHEMA public FROM PUBLIC"), "{t1:?}");
    let pg = cluster_hardening_sql("postgres");
    assert!(pg.iter().any(|s| s.contains("REVOKE ALL ON DATABASE postgres FROM PUBLIC")), "{pg:?}");
    assert!(pg.iter().any(|s| s.contains("REVOKE ALL ON DATABASE template1 FROM PUBLIC")), "{pg:?}");
}

#[test]
fn app_names_that_cannot_be_a_database_a_role_and_a_filename_are_refused() {
    for bad in ["", "Alpha", "1alpha", "al pha", "al-pha", "al'pha", "../etc", "pg_toast", "postgres"] {
        assert!(validate_app_name(bad).is_err(), "{bad:?} was accepted");
    }
    for good in ["alpha", "a", "my_app_2", "a123"] {
        validate_app_name(good).unwrap_or_else(|e| panic!("{good:?}: {e}"));
    }
}

/// The account that ran `sky db provision --shared` is the cluster's bootstrap
/// SUPERUSER (`initdb --username=…`), and it is not in `RESERVED` because it is
/// not a fixed name. Provisioning an app of that name finds the role present,
/// resets its password, and prints it as an app DSN — handing an app the keys to
/// every other app's data, and giving the operator's superuser a password it did
/// not have. `--app deploy` on a host provisioned by `deploy` is the realistic
/// shape of it.
#[test]
fn the_account_that_provisioned_the_cluster_is_not_a_usable_app_name() {
    let me = os_user().expect("id -un");
    // Only meaningful if the name could otherwise pass; a user called `Anzel` or
    // `postgres` is already refused by the charset rule or by RESERVED.
    if me.chars().all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == '_')
        && me.starts_with(|c: char| c.is_ascii_lowercase())
        && !RESERVED.contains(&me.as_str())
    {
        let e = validate_app_name(&me).expect_err(&format!("{me:?} was accepted as an app name"));
        assert!(e.contains("bootstrap superuser"), "{e}");
        // And through the parser the user actually types.
        let e = parse_args(&["--shared".into(), "--app".into(), me.clone()]).unwrap_err();
        assert!(e.contains("bootstrap superuser"), "{e}");
    }
    // A name that is not this account is unaffected.
    validate_app_name("alpha").unwrap();
}

/// `--backup-keep 0` makes the retention line `find -mtime +0`, which deletes
/// every dump older than 24 hours — i.e. everything but the one taken minutes
/// ago, on a schedule, unattended. `--max-connections` is range-checked; this
/// was not.
#[test]
fn a_retention_window_that_deletes_the_backups_is_refused() {
    let e = parse_args(&["--shared".into(), "--backup-keep".into(), "0".into()]).unwrap_err();
    assert!(e.contains("--backup-keep"), "{e}");
    assert!(e.contains("delete"), "the message does not say what 0 would do:\n{e}");
    assert!(parse_args(&["--shared".into(), "--backup-keep".into(), "1".into()]).is_ok());
    assert!(parse_args(&["--shared".into(), "--backup-keep".into(), "3650".into()]).is_ok());
    assert!(parse_args(&["--shared".into(), "--backup-keep".into(), "3651".into()]).is_err());
}

#[test]
fn identifiers_and_literals_are_quoted() {
    assert_eq!(quote_ident("a\"b"), "\"a\"\"b\"");
    assert_eq!(quote_literal("it's"), "'it''s'");
    let sql = rotate_sql("alpha", "a'b");
    assert_eq!(sql, "ALTER ROLE \"alpha\" PASSWORD 'a''b'");
}

/// A generated password goes straight into a `postgresql://` URI. A character
/// needing percent-encoding there produces a DSN that fails when pasted, which
/// an operator debugs as an authentication problem.
#[test]
fn generated_passwords_are_url_safe_and_not_repeated() {
    let a = generate_password();
    let b = generate_password();
    assert_eq!(a.len(), 32);
    assert!(a.chars().all(|c| c.is_ascii_alphanumeric()), "{a}");
    assert_ne!(a, b);
}

#[test]
fn the_dsn_is_the_shape_the_runtime_classifies_as_postgres() {
    let socket = app_dsn("alpha", "pw", Path::new("/var/lib/sky/run"), &Listen::default());
    assert_eq!(socket, "postgresql://alpha:pw@/alpha?host=/var/lib/sky/run");
    let tcp = app_dsn("alpha", "pw", Path::new("/x"), &Listen { addr: Some("127.0.0.1".into()), port: 6000 });
    assert_eq!(tcp, "postgresql://alpha:pw@127.0.0.1:6000/alpha");
    let odd_port = app_dsn("alpha", "pw", Path::new("/x"), &Listen { addr: None, port: 6000 });
    assert_eq!(odd_port, "postgresql://alpha:pw@/alpha?host=/x&port=6000");
    // An IPv6 address MUST be bracketed. Unbracketed, libpq reads everything
    // after the first colon as the port and refuses the DSN with `invalid
    // integer value ":1:5432" for connection option "port"` — an error that
    // names the port, so the operator debugs the wrong thing entirely. `pg_hba`
    // emits a `::1/128` rule whenever `--listen` is given, so this address is
    // one sky explicitly supports.
    let v6 = app_dsn("alpha", "pw", Path::new("/x"), &Listen { addr: Some("::1".into()), port: DEFAULT_PORT });
    assert_eq!(v6, "postgresql://alpha:pw@[::1]:5432/alpha");
    // Already bracketed by the operator is not bracketed twice.
    let v6b = app_dsn("alpha", "pw", Path::new("/x"), &Listen { addr: Some("[::1]".into()), port: 6000 });
    assert_eq!(v6b, "postgresql://alpha:pw@[::1]:6000/alpha");
}

/// `listen_addresses` states what the SERVER binds. `*`, `0.0.0.0` and `::` are
/// not addresses a client can dial, so a DSN carrying one names nowhere — and
/// the IPv6 wildcard has to become the IPv6 loopback, not the IPv4 one: a
/// postmaster bound to `::` on a host without an IPv4 mapping is not reachable
/// at 127.0.0.1 at all.
#[test]
fn a_wildcard_bind_address_becomes_an_address_a_client_can_dial() {
    let dir = std::env::temp_dir().join(format!("sky-p6-listen-{}", std::process::id()));
    let l = Layout::new(&dir);
    std::fs::create_dir_all(&l.data_dir).unwrap();
    let write = |addr: &str| {
        std::fs::write(
            l.data_dir.join("postgresql.conf"),
            format!("listen_addresses = '{addr}'\nport = 5432\n"),
        )
        .unwrap()
    };
    for wildcard in ["*", "0.0.0.0"] {
        write(wildcard);
        assert_eq!(effective_listen(&l).addr.as_deref(), Some("127.0.0.1"), "{wildcard}");
    }
    write("::");
    assert_eq!(effective_listen(&l).addr.as_deref(), Some("::1"));
    write("::1");
    assert_eq!(effective_listen(&l).addr.as_deref(), Some("::1"));
    let _ = std::fs::remove_dir_all(&dir);
}

// ---- service units -------------------------------------------------------

/// The single most consequential line in the systemd unit. systemd's default
/// stop signal is SIGTERM; PostgreSQL reads SIGTERM as SMART shutdown, which
/// waits for every client to disconnect with no timeout. A cluster with one live
/// app connection would therefore never stop cleanly, be SIGKILLed at
/// TimeoutStopSec, and perform crash recovery on every single reboot.
#[test]
fn the_systemd_unit_stops_postgres_with_sigint_not_sigterm() {
    let u = systemd_service(&spec());
    assert!(u.contains("\nKillSignal=SIGINT\n"), "{u}");
    assert!(u.contains("\nTimeoutStopSec=120\n"));
    assert!(!u.contains("Type=notify"), "the bundle is built --without-systemd");
}

/// A structural check of the INI shape, since `systemd-analyze verify` does not
/// exist on the machine this was developed on. It asserts the three sections a
/// unit needs to be installable and startable, and that ExecStart is absolute —
/// systemd rejects a relative one at load time with a message that names the
/// unit and not the reason.
#[test]
fn the_systemd_unit_is_structurally_well_formed() {
    let u = systemd_service(&spec());
    for section in ["[Unit]", "[Service]", "[Install]"] {
        assert_eq!(u.matches(section).count(), 1, "{section} in:\n{u}");
    }
    let exec = u.lines().find(|l| l.starts_with("ExecStart=")).unwrap();
    assert!(exec.strip_prefix("ExecStart=").unwrap().starts_with('/'), "{exec}");
    assert!(u.contains("WantedBy=multi-user.target"));
    // Every non-comment line inside a section is `key=value`.
    for line in u.lines() {
        let t = line.trim();
        if t.is_empty() || t.starts_with('#') || t.starts_with('[') {
            continue;
        }
        assert!(t.contains('='), "not a key=value line: {t:?}");
    }
    assert!(u.contains(&format!("ReadWritePaths={}", spec().layout.state_dir.display())));
}

/// launchd has no `KillSignal`, so the plist cannot ask for SIGINT and the
/// wrapper exists to convert it. If the plist ever ran `postgres` directly, the
/// SIGTERM launchd sends would mean smart shutdown and every reboot would end in
/// crash recovery — so this asserts the indirection is still there.
#[test]
fn the_launchd_job_runs_the_wrapper_that_converts_sigterm_to_sigint() {
    let s = spec();
    let p = launchd_plist(&s);
    assert!(p.contains(&s.wrapper_path().display().to_string()), "{p}");
    assert!(!p.contains("/opt/pg/bin/postgres"), "the plist bypasses the wrapper:\n{p}");
    let w = launchd_wrapper(&s);
    assert!(w.contains("kill -INT"), "{w}");
    assert!(w.contains("trap"), "{w}");
    // Two waits: the first returns the moment the trap runs, with the postmaster
    // still checkpointing.
    assert_eq!(w.matches("wait \"$pg\"").count(), 2, "{w}");
}

#[test]
fn the_launchd_plist_escapes_xml_and_names_a_log() {
    let mut s = spec();
    s.layout = Layout::new(Path::new("/var/lib/sky&co"));
    let p = launchd_plist(&s);
    assert!(p.contains("sky&amp;co"), "{p}");
    assert!(!p.contains("sky&co"), "{p}");
    assert!(p.contains("<key>StandardErrorPath</key>"));
}

// ---- backup --------------------------------------------------------------

/// The app list is read at RUN time. Baking it in at generation time would mean
/// the fifth app is silently not backed up until someone regenerates the timer —
/// and nobody discovers that until a restore.
#[test]
fn the_backup_script_reads_the_app_list_at_run_time() {
    let s = backup_script(&spec());
    assert!(s.contains("APPS=\"/var/lib/sky/apps\""), "{s}");
    assert!(s.contains("while IFS= read -r db"), "{s}");
    assert!(s.contains("--format=custom"), "{s}");
    // Renamed into place: a `.part` from an interrupted run must never be
    // mistaken for a backup, which is discovered during a restore or not at all.
    assert!(s.contains(".part"), "{s}");
    assert!(s.contains("mv \"$tmp\""), "{s}");
    assert!(s.contains("set -eu"), "{s}");
    assert!(s.contains("-mtime \"+$KEEP_DAYS\" -delete"), "{s}");
}

/// A `pg_dump` of one database restores into a cluster with no `alpha` role by
/// failing on every `OWNER TO`. `pg_dumpall` is not in Sky's own bundle, so the
/// script states the gap in its log rather than producing a backup that cannot
/// be restored unattended.
#[test]
fn the_backup_script_dumps_roles_when_it_can_and_says_so_when_it_cannot() {
    let s = backup_script(&spec());
    assert!(s.contains("if [ -x \"$PG_DUMPALL\" ]"), "{s}");
    assert!(s.contains("--globals-only"), "{s}");
    assert!(s.contains("role definitions are NOT in this backup"), "{s}");
}

/// A `--format=custom` dump of one database carries no database-level ACL, so a
/// restore into a hand-made database yields a database with `PUBLIC`'s default
/// `CONNECT` — readable by every app role on the cluster. That is the
/// cross-tenant read the whole phase exists to prevent, reintroduced by the
/// recovery path. `--create` puts the `CREATE DATABASE` and its ACL in the dump,
/// so `pg_restore --create` rebuilds the database hardened.
#[test]
fn the_dump_carries_the_databases_own_acl_so_a_restore_is_hardened() {
    let s = backup_script(&spec());
    assert!(s.contains("--create"), "the dump carries no database ACL:\n{s}");
}

#[test]
fn the_backup_timer_survives_a_host_that_was_off() {
    let t = systemd_backup_timer(&spec());
    assert!(t.contains("OnCalendar=*-*-* 03:30:00 UTC"), "{t}");
    assert!(t.contains("Persistent=true"), "{t}");
    assert!(t.contains("WantedBy=timers.target"), "{t}");
    let p = launchd_backup_plist(&spec());
    assert!(p.contains("<key>StartCalendarInterval</key>"), "{p}");
    assert!(p.contains("<key>Hour</key>\n    <integer>3</integer>"), "{p}");
}

// ---- argument parsing ----------------------------------------------------

#[test]
fn embed_and_shared_are_not_run_together() {
    let e = parse_args(&["--shared".into(), "--embed".into()]).unwrap_err();
    assert!(e.contains("different jobs"), "{e}");
}

#[test]
fn listen_and_port_are_refused_on_an_app_provision() {
    let e = parse_args(&["--shared".into(), "--app".into(), "alpha".into(), "--port".into(), "6000".into()])
        .unwrap_err();
    assert!(e.contains("describe the CLUSTER"), "{e}");
}

#[test]
fn a_bad_app_name_is_refused_at_parse_time_not_at_the_server() {
    let e = parse_args(&["--shared".into(), "--app".into(), "Bad Name".into()]).unwrap_err();
    assert!(e.contains("not a usable app name"), "{e}");
}

#[test]
fn backup_at_takes_hh_mm() {
    assert_eq!(parse_hh_mm("03:30").unwrap(), (3, 30));
    assert_eq!(parse_hh_mm("23:59").unwrap(), (23, 59));
    assert!(parse_hh_mm("24:00").is_err());
    assert!(parse_hh_mm("0330").is_err());
}

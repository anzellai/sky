//! End-to-end regression for `sky db provision --shared` — the shared-cluster
//! service mode (embedded-Postgres phase 6), driven through the REAL binary.
//!
//! The unit and live gates inside the crate (`src/db_shared/tests.rs`,
//! `src/db_shared/live_tests.rs`) prove the derivations and the security
//! boundary by calling the provisioning functions directly. What they cannot
//! prove is that a user typing the verb reaches them: `sky db provision` is
//! parsed by phase 3's arg parser, and `--shared` is routed out of it. A
//! regression there would leave every gate in this phase green while
//! `sky db provision --shared` printed "unknown argument".
//!
//! So this file spends a real `sky` process on each claim, and asks a real
//! `pg_dump` — not sky's own client — whether the boundary holds.

use std::io::Write;
use std::path::{Path, PathBuf};
use std::process::{Command, Output};

/// Say — VISIBLY — that a live test did not run. `eprintln!` would go through
/// libtest's output capture, which is printed only for a test that FAILED, so a
/// skipped live test would report `... ok` and say nothing about having skipped.
fn skip(reason: &str) {
    let mut e = std::io::stderr();
    let _ = writeln!(e, "SKIPPED (live): {reason}");
}

const SKY: &str = env!("CARGO_BIN_EXE_sky");

fn find_pg_bin() -> Option<PathBuf> {
    let complete = |d: &Path| {
        ["initdb", "pg_ctl", "postgres", "pg_dump"].iter().all(|b| d.join(b).is_file())
    };
    if let Ok(v) = std::env::var("SKY_POSTGRES_BIN") {
        let d = PathBuf::from(v);
        if complete(&d) {
            return Some(d);
        }
    }
    if let Ok(path) = std::env::var("PATH") {
        for d in std::env::split_paths(&path) {
            if complete(&d) {
                return Some(d);
            }
        }
    }
    let mut roots: Vec<PathBuf> = Vec::new();
    for prefix in ["/opt/homebrew/opt", "/usr/local/opt"] {
        if let Ok(rd) = std::fs::read_dir(prefix) {
            for e in rd.filter_map(Result::ok) {
                if e.file_name().to_string_lossy().starts_with("postgresql") {
                    roots.push(e.path().join("bin"));
                }
            }
        }
    }
    for v in (9..=20).rev() {
        roots.push(PathBuf::from(format!("/usr/lib/postgresql/{v}/bin")));
    }
    roots.sort();
    roots.reverse();
    roots.into_iter().find(|d| complete(d))
}

/// A durable scratch root. NOT `std::env::temp_dir()`: `--shared` refuses an
/// ephemeral state directory, and correctly — it would hold every app's only
/// copy of its data.
fn scratch(tag: &str) -> PathBuf {
    let home = std::env::var("HOME").expect("HOME");
    PathBuf::from(home).join(format!(
        ".sky-p6-flow-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ))
}

fn sky(args: &[&str], state: &Path) -> Output {
    let mut c = Command::new(SKY);
    c.arg("db").arg("provision").args(args);
    c.env("SKY_HOME", state.join("skyhome"));
    // Small enough to be a good neighbour to anything else on this machine.
    c.env("SKY_PG_TUNE_MEM_MB", "512");
    if let Some(bin) = find_pg_bin() {
        c.env("SKY_POSTGRES_BIN", bin);
    }
    c.output().expect("sky")
}

fn text(o: &Output) -> String {
    format!(
        "{}{}",
        String::from_utf8_lossy(&o.stdout),
        String::from_utf8_lossy(&o.stderr)
    )
}

/// Stops the cluster and removes the tree, whatever the test did. A leaked
/// postmaster holds a SysV shared-memory id, and this platform has 32.
struct Scratch(PathBuf);

impl Drop for Scratch {
    fn drop(&mut self) {
        if let Some(bin) = find_pg_bin() {
            let _ = Command::new(bin.join("pg_ctl"))
                .arg("-D")
                .arg(self.0.join("pg"))
                .args(["-m", "immediate", "-w", "stop"])
                .output();
        }
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

// ---- refusals, which need no server --------------------------------------

#[test]
fn the_verb_is_routed_and_not_eaten_by_the_embed_parser() {
    let state = scratch("route");
    let out = sky(&["--shared", "--app", "Bad Name"], &state);
    let t = text(&out);
    assert!(!t.contains("unknown argument: --shared"), "the verb never reached phase 6:\n{t}");
    assert!(t.contains("not a usable app name"), "{t}");
    assert_eq!(out.status.code(), Some(2), "{t}");
}

#[test]
fn embed_and_shared_together_are_refused_before_anything_is_written() {
    let state = scratch("both");
    let out = sky(&["--shared", "--embed"], &state);
    assert!(text(&out).contains("different jobs"), "{}", text(&out));
    assert!(!state.exists(), "a refused invocation created {}", state.display());
}

#[test]
fn an_ephemeral_state_directory_is_refused_by_the_binary() {
    let state = scratch("eph");
    let out = sky(&["--shared", "--state-dir", "/tmp/sky-shared"], &state);
    let t = text(&out);
    assert!(t.contains("which the system empties"), "{t}");
    assert!(!Path::new("/tmp/sky-shared").exists());
}

/// The account that ran `--shared` is the cluster's bootstrap superuser, and its
/// name is not a constant, so it cannot sit in the reserved list with `postgres`
/// and `template1`. `--app deploy` on a host provisioned by `deploy` used to
/// print a DSN whose role was superuser. The refusal is at parse time, which is
/// the only place it can be reached before a connection — so it is asked of the
/// binary rather than of the function.
#[test]
fn provisioning_an_app_named_after_this_account_is_refused_by_the_binary() {
    let out = Command::new("id").arg("-un").output().expect("id -un");
    let me = String::from_utf8_lossy(&out.stdout).trim().to_string();
    if me.is_empty()
        || !me.starts_with(|c: char| c.is_ascii_lowercase())
        || !me.chars().all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == '_')
    {
        // A name already refused by the charset rule proves nothing about this one.
        return;
    }
    let state = scratch("self");
    let out = sky(&["--shared", "--app", &me], &state);
    let t = text(&out);
    assert!(t.contains("bootstrap superuser"), "{t}");
    assert_eq!(out.status.code(), Some(2), "{t}");
    assert!(!state.exists(), "a refused invocation created {}", state.display());
}

#[test]
fn provisioning_an_app_before_the_cluster_says_so() {
    let state = scratch("noclu");
    let out = sky(
        &["--shared", "--app", "alpha", "--state-dir", &state.display().to_string()],
        &state,
    );
    let t = text(&out);
    assert!(t.contains("there is no cluster at"), "{t}");
    assert!(t.contains("sky db provision --shared --state-dir"), "{t}");
    let _ = std::fs::remove_dir_all(&state);
}

// ---- the live cycle -------------------------------------------------------

/// One `sky db provision --shared`, one `--app`, and then the question this
/// phase exists to answer — asked of `pg_dump`, a real libpq client that knows
/// nothing about sky.
#[test]
fn the_binary_provisions_a_cluster_whose_apps_cannot_read_each_other() {
    let Some(bin) = find_pg_bin() else {
        skip("no PostgreSQL discoverable — the live shared-cluster flow did not run");
        return;
    };
    let state = scratch("live");
    let guard = Scratch(state.clone());
    let sd = state.display().to_string();

    let out = sky(
        &["--shared", "--state-dir", &sd, "--service", "--backup", "--start", "--max-connections", "30"],
        &state,
    );
    let t = text(&out);
    assert!(out.status.success(), "provision failed:\n{t}");
    assert!(t.contains("cluster ready"), "{t}");

    // The artefacts the operator was promised.
    assert!(state.join("pg/PG_VERSION").is_file(), "{t}");
    let conf = std::fs::read_to_string(state.join("pg/postgresql.conf")).unwrap();
    assert!(conf.contains("sky shared cluster: managed block"), "the conf was not tuned");
    assert!(conf.contains("shared_buffers = 128MB"), "not tuned from SKY_PG_TUNE_MEM_MB:\n{conf}");
    let hba = std::fs::read_to_string(state.join("pg/pg_hba.conf")).unwrap();
    assert!(
        !hba.lines().any(|l| !l.trim_start().starts_with('#') && l.contains("trust")),
        "a trust rule survived:\n{hba}"
    );
    let unit = if cfg!(target_os = "macos") { "org.sky.postgres.plist" } else { "sky-postgres.service" };
    assert!(state.join("service").join(unit).is_file(), "no {unit}:\n{t}");
    assert!(state.join("service/sky-postgres-backup.sh").is_file());
    assert!(t.contains("Install the service (as root):"), "{t}");

    let mut dsns = Vec::new();
    for app in ["alpha", "beta"] {
        let out = sky(&["--shared", "--app", app, "--state-dir", &sd], &state);
        let t = text(&out);
        assert!(out.status.success(), "{app}:\n{t}");
        let dsn = t
            .lines()
            .map(str::trim)
            .find(|l| l.starts_with("postgresql://"))
            .unwrap_or_else(|| panic!("no DSN for {app}:\n{t}"))
            .to_string();
        assert!(dsn.contains(&format!("://{app}:")), "{dsn}");
        dsns.push(dsn);
    }

    let pg_dump = |dsn: &str| {
        Command::new(bin.join("pg_dump"))
            .arg(dsn)
            .arg("--schema-only")
            .output()
            .expect("pg_dump")
    };

    // Positive control first: a DSN that does not work would make every refusal
    // below meaningless.
    let ok = pg_dump(&dsns[0]);
    assert!(
        ok.status.success(),
        "the DSN sky printed does not work:\n{}",
        String::from_utf8_lossy(&ok.stderr)
    );

    // alpha's own credentials, beta's database.
    let cross = dsns[0].replace("/alpha?", "/beta?");
    assert_ne!(cross, dsns[0], "the cross-tenant DSN was not built");
    let denied = pg_dump(&cross);
    assert!(
        !denied.status.success(),
        "alpha read beta's database:\n{}",
        String::from_utf8_lossy(&denied.stdout)
    );
    assert!(
        String::from_utf8_lossy(&denied.stderr).contains("permission denied for database"),
        "refused for the wrong reason:\n{}",
        String::from_utf8_lossy(&denied.stderr)
    );

    // alpha's password, claiming to be beta.
    let alpha_pw = dsns[0].split(':').nth(2).unwrap().split('@').next().unwrap().to_string();
    let beta_pw = dsns[1].split(':').nth(2).unwrap().split('@').next().unwrap().to_string();
    let impostor = dsns[1].replace(&beta_pw, &alpha_pw);
    assert_ne!(impostor, dsns[1]);
    let denied = pg_dump(&impostor);
    assert!(
        !denied.status.success(),
        "alpha's password authenticated as beta:\n{}",
        String::from_utf8_lossy(&denied.stdout)
    );

    drop(guard);
}

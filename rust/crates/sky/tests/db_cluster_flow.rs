//! End-to-end regression for the `sky db start` / `ps` / `stop` cluster
//! supervisor (embedded-Postgres phase 2).
//!
//! The unit tests in `db_cluster.rs` prove the *derivations* — socket path,
//! registry reaping, binary precedence, message text. They cannot prove that
//! PostgreSQL accepts what those derivations produce, and that gap is exactly
//! where the socket-path constraint lives: a path that a unit test measures as
//! "under the limit" still has to be one `bind(2)` will take.
//!
//! So this file drives the REAL `sky` binary against a REAL PostgreSQL, and it
//! does so from a **pathologically deep project directory** — deep enough that a
//! socket placed inside the project would overflow `sockaddr_un`. If the hashed
//! socket path is ever regressed to a project-relative one, this test is the
//! thing that fails, on every machine, rather than on the one unlucky user's.
//!
//! When no PostgreSQL is discoverable the live tests early-return with a note
//! rather than failing, matching the toolchain-gated convention in `db_flow.rs`.
//! The tests that need no server (discovery failure, `ps` on an unknown project)
//! always run.

use std::path::{Path, PathBuf};
use std::process::{Command, Output};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

/// The socket name PostgreSQL uses inside the socket directory.
const SOCKET_BASENAME: &str = ".s.PGSQL.5432";

// ---- environment discovery ----------------------------------------------

/// A PostgreSQL `bin` directory holding all three required binaries, if this
/// machine has one. Honours `SKY_POSTGRES_BIN` so CI can point at a specific
/// installation, then looks at PATH, then at the usual Homebrew/apt locations —
/// `postgresql@N` kegs are deliberately NOT symlinked onto PATH by Homebrew, so
/// PATH alone would miss the most common macOS install.
fn find_pg_bin() -> Option<PathBuf> {
    let complete = |d: &Path| {
        ["initdb", "pg_ctl", "postgres"].iter().all(|b| d.join(b).is_file())
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
                let name = e.file_name().to_string_lossy().to_string();
                if name.starts_with("postgresql") {
                    roots.push(e.path().join("bin"));
                }
            }
        }
    }
    for v in (9..=20).rev() {
        roots.push(PathBuf::from(format!("/usr/lib/postgresql/{v}/bin")));
    }
    // Newest first, so a machine with 14 and 17 installed tests against 17.
    roots.sort();
    roots.reverse();
    roots.into_iter().find(|d| complete(d))
}

// ---- scratch fixtures ----------------------------------------------------

fn unique(tag: &str) -> String {
    format!(
        "sky-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    )
}

/// A project directory nested deeply enough that a socket inside it would
/// overflow `sun_path`. The assertion below is the point of the fixture: if the
/// path ever stops being long enough, the test would silently stop exercising
/// the constraint it exists for.
fn deep_scratch_project(tag: &str) -> PathBuf {
    let mut dir = std::env::temp_dir().join(unique(tag));
    for i in 0..5 {
        dir.push(format!("deeply-nested-project-directory-{i}"));
    }
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"cluster-flow\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
    )
    .unwrap();

    let naive_socket = dir.join(".skydata").join("pg").join(SOCKET_BASENAME);
    assert!(
        naive_socket.as_os_str().len() > 107,
        "fixture is not deep enough to exercise the sockaddr_un limit ({} bytes); \
         this test would pass vacuously",
        naive_socket.as_os_str().len()
    );
    dir
}

/// The root of the whole fixture (the temp dir above the nesting), for cleanup.
fn fixture_root(project: &Path) -> PathBuf {
    let mut p = project.to_path_buf();
    for _ in 0..5 {
        p = p.parent().unwrap().to_path_buf();
    }
    p
}

struct Fixture {
    project: PathBuf,
    sky_home: PathBuf,
    pg_bin: PathBuf,
}

impl Fixture {
    fn sky(&self, args: &[&str]) -> Output {
        Command::new(SKY)
            .args(args)
            .current_dir(&self.project)
            // An isolated registry: the test must not write to the developer's
            // own ~/.sky/clusters.json, and must not read clusters it did not create.
            .env("SKY_HOME", &self.sky_home)
            .env("SKY_POSTGRES_BIN", &self.pg_bin)
            // Force the /tmp fallback so the assertion below is about the path
            // this code derives, not about whatever the CI runner sets.
            .env_remove("XDG_RUNTIME_DIR")
            .output()
            .expect("failed to run sky")
    }

    fn registry(&self) -> serde_json::Value {
        let text = std::fs::read_to_string(self.sky_home.join("clusters.json"))
            .expect("registry was never written");
        serde_json::from_str(&text).expect("registry is not valid JSON")
    }

    fn data_dir(&self) -> PathBuf {
        self.project.join(".skydata").join("pg")
    }

    fn postmaster_pid(&self) -> Option<i32> {
        std::fs::read_to_string(self.data_dir().join("postmaster.pid"))
            .ok()?
            .lines()
            .next()?
            .trim()
            .parse()
            .ok()
    }
}

impl Drop for Fixture {
    fn drop(&mut self) {
        // Never leave a postmaster running: it would hold a socket in /tmp and a
        // data dir under the temp tree for the rest of the session.
        let _ = Command::new(self.pg_bin.join("pg_ctl"))
            .arg("-D")
            .arg(self.data_dir())
            .args(["-m", "immediate", "-w", "-t", "20", "stop"])
            .output();
        let _ = std::fs::remove_dir_all(fixture_root(&self.project));
        let _ = std::fs::remove_dir_all(&self.sky_home);
    }
}

fn fixture(tag: &str) -> Option<Fixture> {
    let pg_bin = find_pg_bin()?;
    Some(Fixture {
        project: deep_scratch_project(tag),
        sky_home: std::env::temp_dir().join(unique(&format!("{tag}-home"))),
        pg_bin,
    })
}

fn stdout(o: &Output) -> String {
    String::from_utf8_lossy(&o.stdout).to_string()
}
fn stderr(o: &Output) -> String {
    String::from_utf8_lossy(&o.stderr).to_string()
}
fn both(o: &Output) -> String {
    format!("{}{}", stdout(o), stderr(o))
}

// ---- live cycle ----------------------------------------------------------

/// The headline: `start` → `ps` → `stop`, from a project path too deep for a
/// project-local socket, against a real PostgreSQL.
#[test]
fn start_ps_stop_cycle_against_a_real_postgres_from_a_deep_project_path() {
    let Some(fx) = fixture("cluster") else {
        eprintln!("skipping: no PostgreSQL found (set SKY_POSTGRES_BIN to run this test)");
        return;
    };

    // --- start ---
    let out = fx.sky(&["db", "start"]);
    assert!(out.status.success(), "sky db start failed:\n{}", both(&out));
    let started = stdout(&out);
    assert!(started.contains("running"), "{started}");

    let pid = fx.postmaster_pid().expect("no postmaster.pid after a successful start");
    assert!(pid > 0);

    // The socket really is where the hashing put it, and it really is a socket.
    let reg = fx.registry();
    let key = fx.project.canonicalize().unwrap().display().to_string();
    let entry = reg["clusters"]
        .get(&key)
        .unwrap_or_else(|| panic!("no registry entry for {key}; registry was:\n{reg:#}"));
    let socket_dir = PathBuf::from(entry["socket_dir"].as_str().unwrap());
    assert!(
        socket_dir.to_string_lossy().starts_with("/tmp/sky-"),
        "socket dir is not the hashed fallback: {}",
        socket_dir.display()
    );
    assert!(
        !socket_dir.starts_with(&fx.project),
        "the socket was placed inside the project — the sun_path overflow is back: {}",
        socket_dir.display()
    );
    let socket = socket_dir.join(SOCKET_BASENAME);
    assert!(socket.exists(), "PostgreSQL never created {}", socket.display());
    assert!(
        socket.as_os_str().len() <= 92,
        "socket path is {} bytes: {}",
        socket.as_os_str().len(),
        socket.display()
    );
    assert_eq!(entry["pid"].as_i64(), Some(i64::from(pid)));
    assert!(!entry["pg_version"].as_str().unwrap().is_empty());

    // The cluster is tuned small, and this is the setting that decides whether N
    // idle project clusters cost tens or hundreds of megabytes.
    let conf = std::fs::read_to_string(fx.data_dir().join("postgresql.conf")).unwrap();
    assert!(conf.contains("shared_buffers = 32MB"), "postgresql.conf was not tuned");
    assert!(conf.contains("listen_addresses = ''"), "cluster may be listening on TCP");

    // --- start again: a success no-op, not an error ---
    let again = fx.sky(&["db", "start"]);
    assert!(
        again.status.success(),
        "starting an already-running cluster must succeed:\n{}",
        both(&again)
    );
    assert!(stdout(&again).contains("already running"), "{}", stdout(&again));
    assert_eq!(fx.postmaster_pid(), Some(pid), "the second start spawned a new postmaster");

    // --- ps ---
    let ps = fx.sky(&["db", "ps"]);
    assert!(ps.status.success(), "{}", both(&ps));
    let table = stdout(&ps);
    assert!(table.contains("running"), "{table}");
    assert!(table.contains(&pid.to_string()), "{table}");

    let ps_all = fx.sky(&["db", "ps", "--all"]);
    assert!(ps_all.status.success(), "{}", both(&ps_all));
    assert!(stdout(&ps_all).contains(&key), "--all did not list this project:\n{}", stdout(&ps_all));

    // --- stop ---
    let stop = fx.sky(&["db", "stop"]);
    assert!(stop.status.success(), "sky db stop failed:\n{}", both(&stop));
    assert!(fx.postmaster_pid().is_none(), "postmaster.pid survived a clean stop");
    assert!(!socket.exists(), "the socket survived a clean stop");

    // The registry entry stays (the data dir is still there) but must never
    // report a pid for a process that is gone.
    let after = fx.registry();
    assert_eq!(after["clusters"][&key]["pid"].as_i64(), Some(0));
    let ps_after = fx.sky(&["db", "ps"]);
    assert!(stdout(&ps_after).contains("stopped"), "{}", stdout(&ps_after));
    assert!(
        !stdout(&ps_after).contains(&pid.to_string()),
        "`ps` printed a dead pid:\n{}",
        stdout(&ps_after)
    );

    // --- stop again: idempotent ---
    let stop2 = fx.sky(&["db", "stop"]);
    assert!(stop2.status.success(), "a second stop must be a no-op:\n{}", both(&stop2));
}

/// A `SIGKILL`ed postmaster leaves `postmaster.pid` behind. The next start must
/// recognise it as stale and clear it — refusing to boot here is the failure
/// mode the design brief names.
#[test]
fn a_sigkilled_postmaster_leaves_a_stale_pidfile_that_the_next_start_clears() {
    let Some(fx) = fixture("stale") else {
        eprintln!("skipping: no PostgreSQL found (set SKY_POSTGRES_BIN to run this test)");
        return;
    };

    let out = fx.sky(&["db", "start"]);
    assert!(out.status.success(), "{}", both(&out));
    let pid = fx.postmaster_pid().expect("no postmaster.pid");

    // SIGKILL: no shutdown handler runs, so the pid file is left on disk.
    let killed = Command::new("kill").args(["-9", &pid.to_string()]).status().unwrap();
    assert!(killed.success());
    // The children notice the postmaster's death through their end of the
    // postmaster-death pipe and exit; wait for the pid itself to go.
    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(20);
    while Command::new("kill").args(["-0", &pid.to_string()]).status().map(|s| s.success()).unwrap_or(false) {
        assert!(std::time::Instant::now() < deadline, "SIGKILLed postmaster never went away");
        std::thread::sleep(std::time::Duration::from_millis(100));
    }
    assert!(
        fx.data_dir().join("postmaster.pid").is_file(),
        "fixture invalid: SIGKILL did not leave a stale pid file, so this test proves nothing"
    );

    // With the postmaster gone, `ps` must report stopped — NOT running off the
    // stale pid file.
    let ps = fx.sky(&["db", "ps"]);
    assert!(stdout(&ps).contains("stopped"), "stale pid reported as running:\n{}", stdout(&ps));

    // And a start must clear it and come back up, giving out shared memory a
    // moment if the auxiliary processes are still detaching.
    let mut restart = fx.sky(&["db", "start"]);
    for _ in 0..10 {
        if restart.status.success() {
            break;
        }
        std::thread::sleep(std::time::Duration::from_millis(500));
        restart = fx.sky(&["db", "start"]);
    }
    assert!(
        restart.status.success(),
        "start after a SIGKILL must clear the stale pid file and boot:\n{}",
        both(&restart)
    );
    let new_pid = fx.postmaster_pid().expect("no postmaster.pid after restart");
    assert_ne!(new_pid, pid);

    let _ = fx.sky(&["db", "stop"]);
}

/// A data directory from a different PostgreSQL major must be reported, never
/// started against. Forged by rewriting `PG_VERSION`, because the alternative —
/// installing two PostgreSQL majors — is not something a test can assume.
#[test]
fn a_major_version_mismatch_is_reported_and_never_attempted() {
    let Some(fx) = fixture("mismatch") else {
        eprintln!("skipping: no PostgreSQL found (set SKY_POSTGRES_BIN to run this test)");
        return;
    };

    let out = fx.sky(&["db", "start"]);
    assert!(out.status.success(), "{}", both(&out));
    let stop = fx.sky(&["db", "stop"]);
    assert!(stop.status.success(), "{}", both(&stop));

    let real = std::fs::read_to_string(fx.data_dir().join("PG_VERSION")).unwrap();
    let real_major: u32 = real.trim().split('.').next().unwrap().parse().unwrap();
    let other = if real_major > 9 { real_major - 1 } else { real_major + 1 };
    std::fs::write(fx.data_dir().join("PG_VERSION"), format!("{other}\n")).unwrap();

    let out = fx.sky(&["db", "start"]);
    assert!(!out.status.success(), "a version mismatch must fail:\n{}", both(&out));
    let msg = stderr(&out);
    assert!(msg.contains("major mismatch"), "{msg}");
    assert!(msg.contains(&format!("PostgreSQL {other}")), "{msg}");
    assert!(msg.contains(&format!("PostgreSQL {real_major}")), "{msg}");
    assert!(msg.contains("pg_upgrade"), "the message must name the way forward:\n{msg}");
    assert!(fx.postmaster_pid().is_none(), "a mismatched cluster was started anyway");

    // Restore so the fixture's Drop can clean up.
    std::fs::write(fx.data_dir().join("PG_VERSION"), real).unwrap();
}

// ---- no-server paths (always run) ---------------------------------------

/// With nothing discoverable, the failure must tell the reader every place that
/// was looked and give them something to run.
#[test]
fn missing_binaries_produce_an_actionable_message() {
    let project = deep_scratch_project("nobins");
    let home = std::env::temp_dir().join(unique("nobins-home"));
    let empty = std::env::temp_dir().join(unique("nobins-bin"));
    std::fs::create_dir_all(&empty).unwrap();

    // An explicit override pointing at a directory with no binaries in it.
    let out = Command::new(SKY)
        .args(["db", "start"])
        .current_dir(&project)
        .env("SKY_HOME", &home)
        .env("SKY_POSTGRES_BIN", &empty)
        .output()
        .unwrap();
    assert!(!out.status.success());
    let msg = stderr(&out);
    assert!(msg.contains("SKY_POSTGRES_BIN"), "{msg}");
    assert!(msg.contains("pg_ctl"), "{msg}");

    // And with no override at all and an empty PATH, the full precedence list.
    let out = Command::new(SKY)
        .args(["db", "start"])
        .current_dir(&project)
        .env("SKY_HOME", &home)
        .env_remove("SKY_POSTGRES_BIN")
        .env("PATH", &empty)
        .output()
        .unwrap();
    assert!(!out.status.success());
    let msg = stderr(&out);
    for needle in ["SKY_POSTGRES_BIN", "postgres/<version>/bin", "$PATH", "sky db provision --embed"] {
        assert!(msg.contains(needle), "the not-found message never mentions {needle}:\n{msg}");
    }

    let _ = std::fs::remove_dir_all(fixture_root(&project));
    let _ = std::fs::remove_dir_all(&home);
    let _ = std::fs::remove_dir_all(&empty);
}

/// `sky db ps` in a project that has never had a cluster says so, and says what
/// to do about it, rather than printing an empty table or failing.
#[test]
fn ps_on_a_project_with_no_cluster_says_so() {
    let project = deep_scratch_project("nocluster");
    let home = std::env::temp_dir().join(unique("nocluster-home"));

    let out = Command::new(SKY)
        .args(["db", "ps"])
        .current_dir(&project)
        .env("SKY_HOME", &home)
        .output()
        .unwrap();
    assert!(out.status.success(), "{}", both(&out));
    let msg = stdout(&out);
    assert!(msg.contains("no cluster"), "{msg}");
    assert!(msg.contains("sky db start"), "{msg}");

    let _ = std::fs::remove_dir_all(fixture_root(&project));
    let _ = std::fs::remove_dir_all(&home);
}

/// The cluster verbs must not have displaced the migration verbs they sit
/// beside — `sky db init` and `sky db status` are older and still theirs.
#[test]
fn the_cluster_verbs_did_not_take_over_the_migration_verbs() {
    let project = deep_scratch_project("verbs");
    let out = Command::new(SKY)
        .args(["db", "init"])
        .current_dir(&project)
        .output()
        .unwrap();
    assert!(out.status.success(), "{}", both(&out));
    assert!(
        project.join("db").join("migrations").is_dir(),
        "`sky db init` no longer scaffolds migrations:\n{}",
        both(&out)
    );

    // And an unknown db verb still lists both families.
    let out = Command::new(SKY)
        .args(["db", "nonsense"])
        .current_dir(&project)
        .output()
        .unwrap();
    let usage = stderr(&out);
    assert!(usage.contains("start|stop"), "{usage}");
    assert!(usage.contains("migrate"), "{usage}");

    let _ = std::fs::remove_dir_all(fixture_root(&project));
}

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
    // PostgreSQL removes the socket file but not its directory. Without the
    // cleanup, every project ever started leaves an empty directory in /tmp that
    // nothing will ever collect.
    assert!(
        !socket_dir.exists(),
        "an empty socket directory was left behind: {}",
        socket_dir.display()
    );

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

/// A `SIGKILL`ed postmaster leaves `postmaster.pid` behind, naming a pid the
/// kernel is free to hand to something else. The next start must recognise that
/// as stale and clear it.
///
/// **This gate was vacuous in its first form, and the mutation that proved so is
/// worth recording.** PostgreSQL clears a pid file naming a plainly-dead process
/// *itself* (`CreateLockFile` in `miscinit.c`), so a test that SIGKILLs the
/// postmaster and stops there passes with sky's handling deleted — it is
/// asserting PostgreSQL's behaviour, not sky's. The case that genuinely needs
/// sky is the **recycled** pid: a live, unrelated process wearing the dead
/// postmaster's number. PostgreSQL then sees a live pid, concludes another
/// postmaster is running, and refuses to start *permanently*.
///
/// The impostor is deliberately named `postgres-helper`, so this also gates the
/// second leg of the liveness check: a substring test on the command line calls
/// that a postmaster, and sky would then refuse to clear the pid file at all.
#[test]
fn a_recycled_pid_in_a_stale_pidfile_does_not_wedge_the_next_start() {
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
    let pidfile = fx.data_dir().join("postmaster.pid");
    assert!(
        pidfile.is_file(),
        "fixture invalid: SIGKILL did not leave a stale pid file, so this test proves nothing"
    );

    // Recycle the pid: a live process, not a postmaster, whose name would fool a
    // substring test.
    let helper_bin = std::env::temp_dir().join(unique("postgres-helper")).join("postgres-helper");
    std::fs::create_dir_all(helper_bin.parent().unwrap()).unwrap();
    // A script, NOT a copy of /bin/sleep: on macOS a copied platform binary
    // fails its code-signature check and is killed the moment it execs, which
    // would leave this test asserting the plainly-dead-pid case — the vacuous
    // one PostgreSQL handles by itself. `sleep` is a child rather than an
    // `exec`, so the process holding the pid keeps the impostor's name.
    std::fs::write(&helper_bin, "#!/bin/sh\nsleep 120\n").unwrap();
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&helper_bin, std::fs::Permissions::from_mode(0o755)).unwrap();
    }
    // stdio nulled: the `sleep` grandchild would otherwise inherit the test
    // harness's captured stdout and hold it open for its whole lifetime, which
    // stalls the run long after this test has finished.
    let mut helper = Command::new(&helper_bin)
        .stdin(std::process::Stdio::null())
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .spawn()
        .unwrap();
    let helper_pid = helper.id();
    // The fixture is only a fixture while the impostor is ALIVE and wears a name
    // a substring test would fall for. Both are asserted, because either one
    // silently failing turns this gate back into the vacuous version.
    let impostor_cmd = String::from_utf8_lossy(
        &Command::new("ps")
            .args(["-o", "command=", "-p", &helper_pid.to_string()])
            .output()
            .unwrap()
            .stdout,
    )
    .trim()
    .to_string();
    assert!(
        !impostor_cmd.is_empty(),
        "the impostor process died immediately; this test would then be asserting \
         the dead-pid case PostgreSQL already handles, and would prove nothing"
    );
    assert!(
        impostor_cmd.contains("postgres"),
        "the impostor ({impostor_cmd}) does not carry `postgres` in its command line, \
         so it no longer exercises the substring-versus-executable check"
    );
    let stale = std::fs::read_to_string(&pidfile).unwrap();
    let mut lines: Vec<String> = stale.lines().map(str::to_string).collect();
    lines[0] = helper_pid.to_string();
    std::fs::write(&pidfile, format!("{}\n", lines.join("\n"))).unwrap();

    // With a live impostor holding the number, `ps` must still report stopped —
    // NOT running off the recycled pid.
    let ps = fx.sky(&["db", "ps"]);
    assert!(
        stdout(&ps).contains("stopped"),
        "a recycled pid was reported as a running database:\n{}",
        stdout(&ps)
    );

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
        "start after a SIGKILL with a RECYCLED pid must clear the stale pid file \
         and boot — PostgreSQL will not, it refuses while that pid is alive:\n{}",
        both(&restart)
    );
    let new_pid = fx.postmaster_pid().expect("no postmaster.pid after restart");
    assert_ne!(new_pid, pid);
    assert_ne!(new_pid, helper_pid as i32);

    let _ = fx.sky(&["db", "stop"]);
    // The impostor's own `sleep` child too — scoped to this test's pid, never a
    // pattern that could reach another agent's or another test's processes.
    let _ = Command::new("pkill").args(["-P", &helper_pid.to_string()]).status();
    let _ = helper.kill();
    let _ = helper.wait();
    let _ = std::fs::remove_dir_all(helper_bin.parent().unwrap());
}

/// `pg_ctl start` hands ONE command string to `/bin/sh -c` — the executable, the
/// `-D` data directory, the `-o` post-options and the `-l` log file, all
/// interpolated into it (`start_postmaster`, `pg_ctl.c`). P5a verified against
/// PostgreSQL 14.21 that a `$(…)` in ANY of the three is executed.
///
/// `-D` and `-l` are derived from the PROJECT PATH, which is the user's — and,
/// for anyone who checks out a repository, someone else's. So the gate is a
/// project directory whose name carries a command substitution, and the
/// assertion is that the command did not run.
///
/// The stand-in `pg_ctl` here reproduces exactly that one mechanism, so the test
/// needs no PostgreSQL and still fails if sky ever stops refusing: with the check
/// removed, sky reaches this `pg_ctl`, the marker appears, and the assertion
/// below is what catches it.
#[test]
fn a_project_path_carrying_a_command_substitution_is_refused_not_executed() {
    let root = std::env::temp_dir().join(unique("inject"));
    // `pwned` is relative: the shell pg_ctl spawns inherits sky's cwd, which is
    // the project directory, and a directory name cannot contain a slash.
    let project = root.join("inj$(touch pwned)dir");
    std::fs::create_dir_all(project.join("src")).unwrap();
    std::fs::write(project.join("sky.toml"), "name = \"inj\"\nentry = \"src/Main.sky\"\n").unwrap();

    let bin = root.join("bin");
    std::fs::create_dir_all(&bin).unwrap();
    let exec = |p: PathBuf, body: &str| {
        std::fs::write(&p, body).unwrap();
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            std::fs::set_permissions(&p, std::fs::Permissions::from_mode(0o755)).unwrap();
        }
    };
    // A faithful stand-in for start_postmaster(): build ONE string out of the
    // executable, -o, -D and -l, and hand it to `/bin/sh -c`. Double quotes are
    // what pg_ctl.c uses, and they do not stop `$(…)` from running.
    exec(
        bin.join("pg_ctl"),
        "#!/bin/sh\n\
         DATA=; LOG=; OPTS=\n\
         while [ $# -gt 0 ]; do\n\
         \x20 case \"$1\" in\n\
         \x20   --version) echo 'pg_ctl (PostgreSQL) 14.21'; exit 0 ;;\n\
         \x20   -D) DATA=$2; shift 2 ;;\n\
         \x20   -l) LOG=$2; shift 2 ;;\n\
         \x20   -o) OPTS=$2; shift 2 ;;\n\
         \x20   *) shift ;;\n\
         \x20 esac\n\
         done\n\
         CMD=\"\\\"postgres\\\" $OPTS -D \\\"$DATA\\\" >> \\\"$LOG\\\" 2>&1\"\n\
         /bin/sh -c \"$CMD\"\n\
         exit 1\n",
    );
    // Enough of an initdb that, WITHOUT the refusal, the start path reaches
    // pg_ctl — otherwise a passing test could just mean initdb failed first.
    exec(
        bin.join("initdb"),
        "#!/bin/sh\n\
         while [ $# -gt 0 ]; do case \"$1\" in -D) D=$2; shift 2 ;; *) shift ;; esac; done\n\
         mkdir -p \"$D\" && echo 14 > \"$D/PG_VERSION\" && echo '# stub' > \"$D/postgresql.conf\"\n",
    );
    exec(bin.join("postgres"), "#!/bin/sh\necho 'postgres (PostgreSQL) 14.21'\n");

    let home = root.join("home");
    let out = Command::new(SKY)
        .args(["db", "start"])
        .current_dir(&project)
        .env("SKY_HOME", &home)
        .env("SKY_POSTGRES_BIN", &bin)
        .output()
        .unwrap();

    assert!(
        !project.join("pwned").exists(),
        "COMMAND INJECTION: a `$(…)` in the project path was executed through \
         pg_ctl's /bin/sh.\n{}",
        both(&out)
    );
    assert!(!out.status.success(), "the start should have been refused:\n{}", both(&out));
    let msg = stderr(&out);
    assert!(msg.contains("/bin/sh"), "the refusal must say why:\n{msg}");
    assert!(msg.contains("$(touch pwned)"), "the refusal must name the path:\n{msg}");
    // Refused BEFORE initdb: a cluster that can never be started must not have
    // been created.
    assert!(
        !project.join(".skydata").join("pg").join("PG_VERSION").exists(),
        "a data directory was initialised for a cluster that can never start"
    );

    let _ = std::fs::remove_dir_all(&root);
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

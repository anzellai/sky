//! Layer-2 flow coverage for CROSS-REPLICA Sky.Live session continuity — the
//! two-binary HTTP cookie-handoff path (v1 hardening, gate B10).
//!
//! # What is already proven, and the gap this closes
//!
//! Cross-replica model continuity is architecturally REAL for the durable
//! stores: every dispatch gob-serialises the whole TEA Model into the shared
//! session store synchronously on the event path (`live.go` `store.Set` after
//! `dispatch`), and a cache-cold store instance decodes it back
//! (`live_store.go` `postgresStore.Get`). The STORE level is already covered by
//! `runtime-go/rt/live_store_postgres_test.go`
//! (`TestPostgresStore_CrossInstanceRoundTrip`): a second `newPostgresStore`
//! with an empty `memCache` loads + decodes what the first one wrote.
//!
//! What NO test exercised is the property a multi-replica deployment actually
//! depends on: two SEPARATE `./app` binaries, both pointed at one shared
//! Postgres session store, and a browser cookie that MOVES from replica A to
//! replica B (an LB reshuffle / failover / rolling deploy — the "session MOVES
//! instances" case in `docs/skylive/architecture.md`). That is the compile →
//! emit → `go build` → run → real-Postgres → HTTP path, end to end, across a
//! process boundary — the leg the Go rt store test structurally cannot make.
//!
//! # What is asserted
//!
//!   1. On replica A: `GET /` establishes an `sky_sid` session, then N dispatched
//!      events mutate the Model to a DISTINCTIVE value (a counter incremented to
//!      7), each persisted to shared Postgres synchronously on the event path.
//!   2. On replica B (which has never seen this sid, so its `memCache` is cold):
//!      `GET /` with the SAME cookie renders A's MUTATED value — proving B loaded
//!      the Model from the shared store, not a fresh `init`. This is the full
//!      model-mutation continuity assertion (the direct-send fallback in the
//!      task brief was NOT needed).
//!   3. Negative control: a FRESH cookie on B renders the `init` value (0), not
//!      7 — so the continuity assertion distinguishes "B loaded A's state" from
//!      "B always renders 7".
//!
//! # Topology note (why single-owner, not multi-homing)
//!
//! Sessions are single-owner and sticky-by-cookie; a session MOVE/failover is
//! supported (the new instance loads the current Model from the shared store),
//! concurrent dual-homing is NOT (`docs/skylive/architecture.md` §"Sessions are
//! single-owner"). This gate proves the MOVE, exactly: B is only ever handed the
//! cookie AFTER A has finished mutating, and B is cache-cold for that sid — the
//! failover case. A replica that has already cached a session keeps serving its
//! cached Model (that is what single-ownership means), so the handoff is a
//! cache-cold load by construction, mirroring the store test's fresh reader.
//!
//! When no PostgreSQL or no Go toolchain is discoverable the live gate FAILS
//! (naming what to install) rather than skipping — `SKY_LIVE_TESTS=skip` is the
//! one documented opt-out (see `live_gate.rs`).

use std::io::Read;
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Output, Stdio};
use std::time::{Duration, Instant};

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{gate_if_postgres_cannot_start, required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

/// Ceiling on any single blocking `sky` invocation (the `db start` + the one
/// `sky build`). Generous on purpose: it rules out the UNBOUNDED case that
/// consumes a CI job rather than failing, not a slow machine (see the longer
/// note in `db_cluster_flow.rs`).
const SKY_LIMIT: Duration = Duration::from_secs(420);

/// A minimal-but-real Sky.Live app carrying OBSERVABLE state: a counter, an
/// event (`Increment`) that mutates it, and a `Std.Ui` view that renders the
/// value into the HTML body (`XRCOUNT=<n>`) so `curl` can read it. Uses the
/// preferred `Std.App` builder (App.app + App.withNotFound + App.run), NOT the
/// deprecated `Std.Live`.
const APP_SRC: &str = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.String as String
import Std.Cmd as Cmd
import Std.Sub as Sub
import Std.App as App
import Std.Ui as Ui
import Std.Ui exposing (Element)


type alias Model =
    { count : Int }


type Msg
    = Increment


init : a -> ( Model, Cmd Msg )
init _req =
    ( { count = 0 }, Cmd.none )


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Increment ->
            ( { model | count = model.count + 1 }, Cmd.none )


subscriptions : Model -> Sub.Sub Msg
subscriptions _ =
    Sub.none


view : Model -> Element Msg
view model =
    Ui.column
        []
        [ Ui.text ("XRCOUNT=" ++ String.fromInt model.count)
        , Ui.button [] { onPress = Just Increment, label = Ui.text "inc" }
        ]


appDef =
    App.app
        { init = init
        , update = update
        , view = view
        , subscriptions = subscriptions
        }
        |> App.withNotFound ()


main =
    App.run appDef
"#;

// ── environment discovery ────────────────────────────────────────────────

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .output()
        .map(|o| o.status.success())
        .unwrap_or(false)
}

/// A PostgreSQL `bin` directory holding `initdb` + `pg_ctl` + `postgres`.
/// Mirrors `db_cluster_flow.rs` / `auth_db_lifecycle_flow.rs`: Homebrew's
/// `postgresql@N` kegs are not symlinked onto PATH, so PATH alone misses the
/// most common macOS install.
fn find_pg_bin() -> Option<PathBuf> {
    let complete =
        |d: &Path| ["initdb", "pg_ctl", "postgres"].iter().all(|b| d.join(b).is_file());
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
    roots.sort();
    roots.reverse();
    roots.into_iter().find(|d| complete(d))
}

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

// ── the shared Postgres cluster (one, both replicas point at it) ──────────

/// A single external Postgres cluster provisioned via `sky db start`, cleaned up
/// on Drop. Deliberately NOT `[database] embedded = true` per-app: that would
/// make each app supervise its OWN cluster, defeating the point. One shared
/// cluster, its DSN handed to both replicas via env.
struct Cluster {
    project: PathBuf,
    sky_home: PathBuf,
    pg_bin: PathBuf,
}

impl Cluster {
    /// Provision the cluster and return `(cluster, dsn)`, or `None` when no
    /// PostgreSQL is discoverable (the caller then routes through the live gate).
    fn start() -> Option<(Cluster, String)> {
        let pg_bin = find_pg_bin()?;
        let project = std::env::temp_dir().join(unique("xrcluster"));
        std::fs::create_dir_all(project.join("src")).unwrap();
        std::fs::write(
            project.join("sky.toml"),
            "name = \"xrcluster\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
        )
        .unwrap();
        let sky_home = std::env::temp_dir().join(unique("xrcluster-home"));
        let cl = Cluster { project, sky_home, pg_bin };

        let out = cl.sky_db(&["db", "start"]);
        if !out.status.success() {
            let log = both(&out);
            // An unavailable PostgreSQL (e.g. SysV shm exhausted) routes through
            // the gate; anything else is a real defect and panics.
            if gate_if_postgres_cannot_start(&log) {
                return None;
            }
            panic!("sky db start failed:\n{log}");
        }

        // The DSN for a socket-only cluster: `postgresql:///postgres?host=<dir>`
        // (pgx honours `?host=`), matching `db_cluster::dsn_for_socket_dir`. The
        // `sky` crate is a binary, so this integration test cannot import that
        // fn; the socket dir is read from the registry `sky db start` wrote.
        let socket_dir = cl.socket_dir_from_registry();
        let dsn = format!("postgresql:///postgres?host={}", socket_dir.display());
        Some((cl, dsn))
    }

    /// Run a `sky db …` verb to completion, bounded by [`SKY_LIMIT`], with an
    /// isolated `SKY_HOME` registry and a pinned `SKY_POSTGRES_BIN`.
    fn sky_db(&self, args: &[&str]) -> Output {
        run_bounded(
            Command::new(SKY)
                .args(args)
                .current_dir(&self.project)
                .env("SKY_HOME", &self.sky_home)
                .env("SKY_POSTGRES_BIN", &self.pg_bin)
                .env_remove("XDG_RUNTIME_DIR"),
            &format!("sky {}", args.join(" ")),
        )
    }

    fn data_dir(&self) -> PathBuf {
        self.project.join(".skydata").join("pg")
    }

    fn socket_dir_from_registry(&self) -> PathBuf {
        let text = std::fs::read_to_string(self.sky_home.join("clusters.json"))
            .expect("registry was never written by `sky db start`");
        let reg: serde_json::Value =
            serde_json::from_str(&text).expect("registry is not valid JSON");
        let key = self.project.canonicalize().unwrap().display().to_string();
        let entry = reg["clusters"].get(&key).unwrap_or_else(|| {
            panic!("no registry entry for {key}; registry was:\n{reg:#}")
        });
        PathBuf::from(entry["socket_dir"].as_str().expect("socket_dir missing from registry entry"))
    }
}

impl Drop for Cluster {
    fn drop(&mut self) {
        // Never leave a postmaster (or its data dir / socket) behind.
        let _ = Command::new(self.pg_bin.join("pg_ctl"))
            .arg("-D")
            .arg(self.data_dir())
            .args(["-m", "immediate", "-w", "-t", "20", "stop"])
            .output();
        let _ = std::fs::remove_dir_all(&self.project);
        let _ = std::fs::remove_dir_all(&self.sky_home);
    }
}

// ── the app: built once, launched as two replicas ────────────────────────

/// The compiled Sky.Live app, built once. Owns the project dir; removed on Drop.
struct App {
    project: PathBuf,
    app_bin: PathBuf,
}

impl App {
    fn build() -> App {
        let project = std::env::temp_dir().join(unique("xrapp"));
        std::fs::create_dir_all(project.join("src")).unwrap();
        std::fs::write(
            project.join("sky.toml"),
            "name = \"xrflow\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
        )
        .unwrap();
        std::fs::write(project.join("src").join("Main.sky"), APP_SRC).unwrap();

        let build = run_bounded(
            Command::new(SKY).args(["build", "src/Main.sky"]).current_dir(&project),
            "sky build src/Main.sky",
        );
        assert!(
            build.status.success(),
            "sky build of the cross-replica fixture failed:\n{}",
            both(&build),
        );
        let app_bin = project.join("sky-out").join("app");
        assert!(
            app_bin.is_file(),
            "sky build reported success but produced no binary at {}",
            app_bin.display()
        );
        App { project, app_bin }
    }

    /// Launch one replica on `port`, pointed at the shared Postgres session
    /// store. Waits for the "Sky.Live listening" line before returning.
    fn launch_replica(&self, tag: &str, port: u16, dsn: &str) -> Replica {
        let log_path = std::env::temp_dir().join(unique(&format!("xr-{tag}")));
        let log = std::fs::File::create(&log_path).unwrap();
        let child = Command::new(&self.app_bin)
            .current_dir(&self.project)
            .env("SKY_LIVE_PORT", port.to_string())
            .env("SKY_LIVE_STORE", "postgres")
            .env("SKY_LIVE_STORE_PATH", dsn)
            // Belt to the SKY_LIVE_STORE_PATH braces: an inherited DSN must not
            // be mistaken for a second source of truth.
            .env_remove("DATABASE_URL")
            .env_remove("SKY_DB_PATH")
            .stdin(Stdio::null())
            .stdout(log.try_clone().unwrap())
            .stderr(log)
            .spawn()
            .unwrap_or_else(|e| panic!("failed to spawn replica {tag} on :{port}: {e}"));
        let mut r = Replica { child, port, log_path };
        if !r.wait_for_listening(60) {
            let log = r.read_log();
            panic!("replica {tag} never reported listening on :{port}\nlog:\n{log}");
        }
        // The postgres session store connects asynchronously with a retry
        // (two replicas racing the CREATE TABLE / CREATE TYPE is expected and
        // self-heals); require it is actually on postgres before we assert
        // anything about cross-replica persistence, so a silent fall-back to a
        // per-process memory store can never masquerade as continuity.
        if !r.wait_for_log("session store: postgres", 60) {
            let log = r.read_log();
            panic!("replica {tag} did not bring up the POSTGRES session store — a \
                    memory fallback would make this gate vacuous\nlog:\n{log}");
        }
        r
    }
}

impl Drop for App {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.project);
    }
}

struct Replica {
    child: Child,
    port: u16,
    log_path: PathBuf,
}

impl Replica {
    fn wait_for_listening(&mut self, tries: u32) -> bool {
        self.wait_for_log(&format!("Sky.Live listening on :{}", self.port), tries)
    }

    fn wait_for_log(&mut self, needle: &str, tries: u32) -> bool {
        for _ in 0..tries {
            // A replica that died is never going to print the line.
            if let Ok(Some(status)) = self.child.try_wait() {
                panic!(
                    "replica on :{} exited early ({status}) before logging {needle:?}\nlog:\n{}",
                    self.port,
                    self.read_log()
                );
            }
            if self.read_log().contains(needle) {
                return true;
            }
            std::thread::sleep(Duration::from_millis(250));
        }
        false
    }

    fn read_log(&self) -> String {
        let mut buf = String::new();
        let _ = std::fs::File::open(&self.log_path).and_then(|mut f| f.read_to_string(&mut buf));
        buf
    }
}

impl Drop for Replica {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
        let _ = std::fs::remove_file(&self.log_path);
    }
}

// ── bounded process runner ───────────────────────────────────────────────

/// Run a `Command` to completion, but never for longer than [`SKY_LIMIT`].
/// `Command::output()` blocks until every inherited copy of the child's stdout
/// pipe is closed, so a descendant that outlives the command turns one test into
/// an unbounded wait; this bounds it. Output is captured to files so a timeout
/// still yields the partial transcript.
fn run_bounded(cmd: &mut Command, what: &str) -> Output {
    let out_path = std::env::temp_dir().join(unique("xr-cmd-out"));
    let err_path = std::env::temp_dir().join(unique("xr-cmd-err"));
    let mut child = cmd
        .stdin(Stdio::null())
        .stdout(Stdio::from(std::fs::File::create(&out_path).unwrap()))
        .stderr(Stdio::from(std::fs::File::create(&err_path).unwrap()))
        .spawn()
        .unwrap_or_else(|e| panic!("failed to run `{what}`: {e}"));
    let deadline = Instant::now() + SKY_LIMIT;
    let status = loop {
        match child.try_wait().unwrap() {
            Some(s) => break s,
            None if Instant::now() >= deadline => {
                let _ = child.kill();
                let _ = child.wait();
                panic!(
                    "`{what}` did not finish within {}s — killed.\n--- stdout ---\n{}\n--- stderr ---\n{}",
                    SKY_LIMIT.as_secs(),
                    std::fs::read_to_string(&out_path).unwrap_or_default(),
                    std::fs::read_to_string(&err_path).unwrap_or_default(),
                );
            }
            None => std::thread::sleep(Duration::from_millis(50)),
        }
    };
    let out = Output {
        status,
        stdout: std::fs::read(&out_path).unwrap_or_default(),
        stderr: std::fs::read(&err_path).unwrap_or_default(),
    };
    let _ = std::fs::remove_file(&out_path);
    let _ = std::fs::remove_file(&err_path);
    out
}

fn both(o: &Output) -> String {
    format!(
        "{}{}",
        String::from_utf8_lossy(&o.stdout),
        String::from_utf8_lossy(&o.stderr)
    )
}

// ── curl helpers ─────────────────────────────────────────────────────────

/// `GET path`, returning the response body. `-c jar` saves cookies when `save`,
/// `-b jar` always sends them.
fn curl_get(port: u16, path: &str, jar: &Path, save: bool) -> String {
    let url = format!("http://127.0.0.1:{port}{path}");
    let mut args: Vec<String> =
        vec!["-s".into(), "--max-time".into(), "30".into(), "-b".into(), jar.display().to_string()];
    if save {
        args.push("-c".into());
        args.push(jar.display().to_string());
    }
    args.push(url);
    let out = Command::new("curl").args(&args).output().expect("run curl GET");
    String::from_utf8_lossy(&out.stdout).to_string()
}

/// `GET path` with NO cookie jar at all (the negative-control's fresh browser).
fn curl_get_fresh(port: u16, path: &str) -> String {
    let url = format!("http://127.0.0.1:{port}{path}");
    let out = Command::new("curl")
        .args(["-s", "--max-time", "30", &url])
        .output()
        .expect("run curl GET (fresh)");
    String::from_utf8_lossy(&out.stdout).to_string()
}

/// POST a Sky.Live event to `/_sky/event` via the handler-id path, carrying the
/// session cookie (`-b jar`) and the double-submit CSRF token header. Returns
/// the HTTP status code.
fn curl_post_event(port: u16, jar: &Path, csrf: &str, handler_id: &str) -> String {
    let url = format!("http://127.0.0.1:{port}/_sky/event");
    let body = format!("{{\"sessionId\":\"\",\"msg\":\"\",\"args\":[],\"handlerId\":\"{handler_id}\"}}");
    let out = Command::new("curl")
        .args([
            "-s",
            "--max-time",
            "30",
            "-o",
            "/dev/null",
            "-w",
            "%{http_code}",
            "-b",
            &jar.display().to_string(),
            "-H",
            "Content-Type: application/json",
            "-H",
            &format!("X-Sky-Csrf: {csrf}"),
            "-X",
            "POST",
            &url,
            "-d",
            &body,
        ])
        .output()
        .expect("run curl POST");
    String::from_utf8_lossy(&out.stdout).trim().to_string()
}

/// Read a cookie value out of a curl Netscape cookie jar (value is the 7th
/// tab-separated field). Returns the LAST occurrence.
fn cookie_from_jar(jar: &Path, name: &str) -> Option<String> {
    let text = std::fs::read_to_string(jar).ok()?;
    let mut found = None;
    for line in text.lines() {
        let cols: Vec<&str> = line.split('\t').collect();
        if cols.len() >= 7 && cols[5] == name {
            found = Some(cols[6].to_string());
        }
    }
    found
}

/// The counter value the app rendered into the body (`XRCOUNT=<n>`), if any.
fn rendered_count(body: &str) -> Option<i64> {
    let idx = body.find("XRCOUNT=")?;
    let rest = &body[idx + "XRCOUNT=".len()..];
    let digits: String = rest.chars().take_while(|c| c.is_ascii_digit()).collect();
    digits.parse().ok()
}

/// The `data-sky-hid` of the app's onClick button, read from the rendered HTML.
/// The view has exactly one clickable element, so the first `data-sky-hid`
/// attribute is it. Read from the page rather than hard-coded so a view-shape
/// change fails loudly here rather than silently dispatching nothing.
fn button_handler_id(body: &str) -> Option<String> {
    let key = "data-sky-hid=\"";
    let idx = body.find(key)?;
    let rest = &body[idx + key.len()..];
    let end = rest.find('"')?;
    Some(rest[..end].to_string())
}

// ── the gate ─────────────────────────────────────────────────────────────

#[test]
fn a_session_moves_from_replica_a_to_replica_b_over_a_shared_postgres_store() {
    // Both a Go toolchain (to `go build` the emitted app) and PostgreSQL (the
    // shared store) are required. Absent either, gate loudly — `required`
    // panics under the default mode; `SKY_LIVE_TESTS=skip` is the one opt-out.
    if !have_go() {
        required(Need::Go, false);
        return;
    }
    let Some((_cluster, dsn)) = Cluster::start() else {
        required(Need::Postgres, false);
        return;
    };

    // Build the app ONCE, launch it twice as two independent replicas on two
    // different non-8000 ports, BOTH pointed at the one shared Postgres store.
    let app = App::build();
    let replica_a = app.launch_replica("A", 8811, &dsn);
    let replica_b = app.launch_replica("B", 8812, &dsn);

    // ── On replica A: establish a session, then mutate the Model. ──
    let jar = std::env::temp_dir().join(unique("xr-jar"));
    let initial = curl_get(replica_a.port, "/", &jar, true);
    let init_count = rendered_count(&initial).unwrap_or_else(|| {
        panic!("replica A's initial GET / did not render an XRCOUNT marker:\n{initial}")
    });
    assert_eq!(init_count, 0, "a fresh session should start at the init value (0)");

    let sid = cookie_from_jar(&jar, "sky_sid")
        .expect("replica A's GET / did not set an sky_sid cookie");
    let csrf = cookie_from_jar(&jar, "__sky_csrf")
        .expect("replica A's GET / did not set a __sky_csrf cookie");
    let handler_id = button_handler_id(&initial)
        .expect("could not find the button's data-sky-hid in the rendered page");

    // Dispatch 7 Increment events to A. 7 is distinctive: not the init value,
    // not a single click that a stray retry could reproduce. Each dispatch does
    // a synchronous store.Set on the event path, persisting to shared Postgres.
    const TARGET: i64 = 7;
    for i in 1..=TARGET {
        let status = curl_post_event(replica_a.port, &jar, &csrf, &handler_id);
        assert_eq!(
            status, "200",
            "event #{i} to replica A returned HTTP {status}, not 200 — the dispatch \
             did not reach the handler (session/CSRF/handler-id mismatch)\nreplica A log:\n{}",
            replica_a.read_log(),
        );
    }

    // Sanity: replica A itself now serves the mutated value (its own memCache).
    let on_a = curl_get(replica_a.port, "/", &jar, true);
    assert_eq!(
        rendered_count(&on_a),
        Some(TARGET),
        "replica A did not reflect its own {TARGET} dispatches:\n{on_a}",
    );

    // ── The headline: the SAME cookie hits replica B (a DIFFERENT process that
    //    has never seen this sid, so its memCache is cold). B must render A's
    //    mutated Model — proving it loaded the Model from the shared Postgres
    //    store, not a fresh init. This is the session MOVE / failover path. ──
    let on_b = curl_get(replica_b.port, "/", &jar, false);
    let b_count = rendered_count(&on_b).unwrap_or_else(|| {
        panic!(
            "replica B's GET / did not render an XRCOUNT marker — the cross-replica \
             load produced no page:\n{on_b}\nreplica B log:\n{}",
            replica_b.read_log()
        )
    });
    assert_eq!(
        b_count, TARGET,
        "CROSS-REPLICA CONTINUITY BROKEN: replica A mutated the session (sid={sid}) to \
         {TARGET} and persisted it to the shared Postgres store, but replica B — a \
         separate process, cache-cold for this sid — rendered {b_count}. A session that \
         MOVED instances (LB reshuffle / failover / rolling deploy) lost its Model.\n\
         replica B log:\n{}",
        replica_b.read_log(),
    );

    // ── Negative control: a FRESH cookie on B renders the init value, not
    //    TARGET — so the assertion above measures continuity, not a replica
    //    that always renders 7. ──
    let fresh_on_b = curl_get_fresh(replica_b.port, "/");
    assert_eq!(
        rendered_count(&fresh_on_b),
        Some(0),
        "a FRESH session on replica B should render the init value (0), not the \
         handed-off value — otherwise the continuity assertion proves nothing:\n{fresh_on_b}",
    );

    let _ = std::fs::remove_file(&jar);
    // replica_a / replica_b / _cluster clean themselves up on Drop (kill the
    // app processes, stop the postmaster, remove the temp trees).
}

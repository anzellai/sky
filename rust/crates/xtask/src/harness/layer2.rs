//! Layer 2 support — building and driving real Sky projects.
//!
//! Layer 1 is fast, static, and structurally blind to a whole class:
//! session/SSE lifecycle, CSRF-idle strand, session hijack, `liveInto` silently
//! not updating on a SQL backend, "compiles clean, behaves wrong" at runtime,
//! multi-replica topology (v2 §3.4, §6). Layer 2 exists for that class and
//! nothing else, so its helpers are about *running a real binary and asserting
//! what it did* — not about parsing.
//!
//! Four operational rules are encoded here rather than left to each gate,
//! because each of them is a defect this repo has actually shipped:
//!
//! * **Unique ports.** [`free_port`] binds `:0` and reads the port back. 14
//!   examples currently share `8000`, and the sweep's probe asserted only that
//!   *something* answered — so a squatter left by an earlier run satisfied it
//!   (v2 §7.6: "the harness binds `:0`, reads the actual port, passes it in the
//!   env").
//! * **Process-group kill.** [`Server`] spawns with `process_group(0)` and tears
//!   down with `killpg`. `kill -9 $!` kills the subshell and leaves the app
//!   holding the port; the next gate then measures the squatter.
//! * **The port must be observed released.** Teardown is `killpg` *and* asserts
//!   the port is free again ([`Server::shutdown`]). A gate that reports done
//!   while its server still holds the port poisons every later gate.
//! * **Assert the verdict, not "no crash".** Helpers return the response so a
//!   gate can assert *content*. `liveInto` on a SQL backend must either deliver
//!   or fail loudly — "no crash" passes while it silently never updates
//!   (v2 §6.1).
//!
//! Gate bodies already run inside the harness's own process group (`child.rs`),
//! so a budget overrun reaps anything spawned here. [`Server`]'s own teardown is
//! the *non*-timeout path: the gate finished and must not leak.

use std::io::{Read, Write};
use std::net::{Shutdown, SocketAddr, TcpListener, TcpStream};
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::time::{Duration, Instant};

// ---------------------------------------------------------------------------
// The compiler under test
// ---------------------------------------------------------------------------

/// The compiler a Layer-2 gate builds with.
///
/// A missing compiler is a hard error, never a skip. "A gate that cannot run has
/// not passed" — the same rule `sky_verify` applies.
pub fn sky_binary(root: &Path) -> Result<PathBuf, String> {
    let sky = root.join("sky-out/sky");
    if !sky.is_file() {
        return Err(format!(
            "no compiler at {} — build it first (scripts/build.sh). \
             A gate that cannot run has not passed.",
            sky.display()
        ));
    }
    Ok(sky)
}

// ---------------------------------------------------------------------------
// Building a project
// ---------------------------------------------------------------------------

pub struct BuildReport {
    pub project: String,
    pub ok: bool,
    pub elapsed_s: f64,
    pub binary: PathBuf,
    /// Combined stdout+stderr, kept for the failure detail only. It is NEVER
    /// consulted for the verdict — the verdict is the exit status plus the
    /// presence of the artifact.
    pub log: String,
}

/// Build a project from a **wiped slate**, the same contract `examples/*` carry.
///
/// The wipe is what makes a source mutation observable. `verify-cli.sh` without
/// `--rebuild` certifies whatever binary an earlier run left behind, so its
/// declared mutation would report `VACUOUS` forever and be misread as a harness
/// defect. A Layer-2 gate that reused artifacts would have the same hole.
pub fn clean_build(root: &Path, project_rel: &str) -> Result<BuildReport, String> {
    build_wiping(root, project_rel, &["sky-out", ".skycache", ".skydeps"])
}

/// As [`clean_build`], but keeps `.skydeps`.
///
/// For a project whose dependencies were just fetched over the network by
/// `sky install`: wiping `.skydeps` would undo the fetch and make the gate
/// re-download on every build. The compiled outputs are still wiped, so a source
/// mutation is still recompiled — which is the property the falsifier needs.
pub fn clean_build_keep_deps(root: &Path, project_rel: &str) -> Result<BuildReport, String> {
    build_wiping(root, project_rel, &["sky-out", ".skycache"])
}

fn build_wiping(root: &Path, project_rel: &str, wipe: &[&str]) -> Result<BuildReport, String> {
    let sky = sky_binary(root)?;
    let dir = root.join(project_rel);
    if !dir.join("sky.toml").is_file() {
        return Err(format!(
            "{project_rel} is not a Sky project (no sky.toml at {})",
            dir.display()
        ));
    }

    for stale in wipe {
        let _ = std::fs::remove_dir_all(dir.join(stale));
    }

    let entry = entry_of(&dir).unwrap_or_else(|| "src/Main.sky".to_string());
    let t0 = Instant::now();
    let out = Command::new(&sky)
        .arg("build")
        .arg(&entry)
        .current_dir(&dir)
        .stdin(Stdio::null())
        .output()
        .map_err(|e| format!("could not spawn `sky build` in {project_rel}: {e}"))?;
    let elapsed_s = t0.elapsed().as_secs_f64();

    let mut log = String::from_utf8_lossy(&out.stdout).into_owned();
    log.push_str(&String::from_utf8_lossy(&out.stderr));

    Ok(BuildReport {
        project: project_rel.to_string(),
        ok: out.status.success(),
        elapsed_s,
        binary: dir.join("sky-out/app"),
        log,
    })
}

/// `entry = "..."` from the project's `sky.toml`.
fn entry_of(dir: &Path) -> Option<String> {
    let toml = std::fs::read_to_string(dir.join("sky.toml")).ok()?;
    for line in toml.lines() {
        let line = line.trim();
        let Some(rest) = line.strip_prefix("entry") else {
            continue;
        };
        let Some(eq) = rest.find('=') else { continue };
        let v = rest[eq + 1..].trim().trim_matches('"');
        if !v.is_empty() {
            return Some(v.to_string());
        }
    }
    None
}

// ---------------------------------------------------------------------------
// Ports
// ---------------------------------------------------------------------------

/// A port nothing is listening on, obtained by binding `:0` and reading it back.
///
/// This is inherently a race against another process taking the port between the
/// close and the app's bind, which is why callers pass it to the app and then
/// wait for the app's OWN readiness line rather than assuming success.
pub fn free_port() -> Result<u16, String> {
    let l = TcpListener::bind("127.0.0.1:0")
        .map_err(|e| format!("cannot bind an ephemeral port: {e}"))?;
    let port = l
        .local_addr()
        .map_err(|e| format!("cannot read back the bound port: {e}"))?
        .port();
    drop(l);
    Ok(port)
}

/// Is anything accepting connections on this port?
pub fn port_in_use(port: u16) -> bool {
    let addr: SocketAddr = ([127, 0, 0, 1], port).into();
    TcpStream::connect_timeout(&addr, Duration::from_millis(250)).is_ok()
}

/// Wait for a port to stop accepting connections.
///
/// The assertion half of "teardown is `killpg` **and** asserts the port is
/// released" (v2 §7.6).
pub fn wait_port_released(port: u16, deadline: Duration) -> bool {
    let t0 = Instant::now();
    while t0.elapsed() < deadline {
        if !port_in_use(port) {
            return true;
        }
        std::thread::sleep(Duration::from_millis(50));
    }
    !port_in_use(port)
}

// ---------------------------------------------------------------------------
// Running a project
// ---------------------------------------------------------------------------

/// A running Sky app, owned by its own process group.
pub struct Server {
    child: Child,
    pub port: u16,
    /// Everything the app wrote to stdout+stderr, drained by a reader thread.
    log: std::sync::Arc<std::sync::Mutex<String>>,
    shut: bool,
}

impl Server {
    /// Spawn a built app with `env` applied, in its **own process group**.
    ///
    /// `process_group(0)` is the whole point: `kill(child)` on a shell wrapper
    /// leaves the real server holding the port. The group is what `killpg`
    /// reaches.
    pub fn spawn(binary: &Path, dir: &Path, port: u16, env: &[(&str, String)]) -> Result<Server, String> {
        if !binary.is_file() {
            return Err(format!("no app binary at {}", binary.display()));
        }
        let mut cmd = Command::new(binary);
        cmd.current_dir(dir)
            .stdin(Stdio::null())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped());
        for (k, v) in env {
            cmd.env(k, v);
        }
        #[cfg(unix)]
        {
            use std::os::unix::process::CommandExt;
            cmd.process_group(0);
        }

        let mut child = cmd
            .spawn()
            .map_err(|e| format!("could not spawn {}: {e}", binary.display()))?;

        // Drain both pipes. An app that fills a 64 KiB pipe buffer and blocks on
        // write looks exactly like a hang, and we would blame the wrong thing.
        let log = std::sync::Arc::new(std::sync::Mutex::new(String::new()));
        for pipe in [
            child.stdout.take().map(Pipe::Out),
            child.stderr.take().map(Pipe::Err),
        ]
        .into_iter()
        .flatten()
        {
            let sink = std::sync::Arc::clone(&log);
            std::thread::spawn(move || {
                let mut buf = [0u8; 4096];
                let mut r: Box<dyn Read + Send> = match pipe {
                    Pipe::Out(o) => Box::new(o),
                    Pipe::Err(e) => Box::new(e),
                };
                while let Ok(n) = r.read(&mut buf) {
                    if n == 0 {
                        break;
                    }
                    if let Ok(mut g) = sink.lock() {
                        g.push_str(&String::from_utf8_lossy(&buf[..n]));
                    }
                }
            });
        }

        Ok(Server {
            child,
            port,
            log,
            shut: false,
        })
    }

    /// Everything the app has printed so far.
    pub fn log(&self) -> String {
        self.log.lock().map(|g| g.clone()).unwrap_or_default()
    }

    /// Wait until the app prints `needle` **and** the port accepts connections.
    ///
    /// Both halves matter. The readiness line alone can precede the actual bind;
    /// the port alone can be satisfied by a squatter from an earlier run. If the
    /// process dies first, that is reported immediately rather than waited out.
    pub fn wait_ready(&mut self, needle: &str, deadline: Duration) -> Result<(), String> {
        let t0 = Instant::now();
        while t0.elapsed() < deadline {
            if let Ok(Some(status)) = self.child.try_wait() {
                return Err(format!(
                    "app exited before becoming ready (status {status}); output:\n{}",
                    tail(&self.log(), 40)
                ));
            }
            if self.log().contains(needle) && port_in_use(self.port) {
                return Ok(());
            }
            std::thread::sleep(Duration::from_millis(50));
        }
        Err(format!(
            "app did not become ready within {:?} (looking for {needle:?} on port {}); output:\n{}",
            deadline,
            self.port,
            tail(&self.log(), 40)
        ))
    }

    /// `killpg` the group, then assert the port came back.
    ///
    /// Returns an error when the port is STILL held after the kill — a leaked
    /// listener is a gate failure, not a cosmetic one, because it silently
    /// satisfies the next gate's "something answers" probe.
    pub fn shutdown(&mut self) -> Result<(), String> {
        if self.shut {
            return Ok(());
        }
        self.shut = true;
        kill_group(&mut self.child);
        let _ = self.child.wait();
        if !wait_port_released(self.port, Duration::from_secs(10)) {
            return Err(format!(
                "port {} still held after killpg — a leaked listener would satisfy \
                 the next gate's probe",
                self.port
            ));
        }
        Ok(())
    }
}

enum Pipe {
    Out(std::process::ChildStdout),
    Err(std::process::ChildStderr),
}

impl Drop for Server {
    fn drop(&mut self) {
        if !self.shut {
            kill_group(&mut self.child);
            let _ = self.child.wait();
        }
    }
}

/// `killpg` — not `kill`. The negative-pid form targets the group, so a server
/// spawned behind a wrapper dies with it.
#[cfg(unix)]
fn kill_group(child: &mut Child) {
    use nix::sys::signal::{killpg, Signal};
    use nix::unistd::Pid;

    let pgid = Pid::from_raw(child.id() as i32);
    let _ = killpg(pgid, Signal::SIGTERM);
    let t0 = Instant::now();
    while t0.elapsed() < Duration::from_secs(5) {
        if killpg(pgid, None::<Signal>).is_err() {
            return;
        }
        std::thread::sleep(Duration::from_millis(50));
    }
    let _ = killpg(pgid, Signal::SIGKILL);
}

#[cfg(not(unix))]
fn kill_group(child: &mut Child) {
    let _ = child.kill();
}

// ---------------------------------------------------------------------------
// A minimal HTTP client
// ---------------------------------------------------------------------------

pub struct Response {
    pub status: u16,
    pub body: String,
    pub raw: String,
}

impl Response {
    pub fn header(&self, name: &str) -> Option<String> {
        let lname = name.to_ascii_lowercase();
        self.raw
            .split("\r\n\r\n")
            .next()?
            .lines()
            .skip(1)
            .find_map(|l| {
                let (k, v) = l.split_once(':')?;
                (k.trim().to_ascii_lowercase() == lname).then(|| v.trim().to_string())
            })
    }
}

/// One HTTP/1.1 request over a raw socket.
///
/// Deliberately dependency-free: adding `reqwest` to `xtask` for four requests
/// would pull a TLS stack into the gate runner.
pub fn http(
    port: u16,
    method: &str,
    path: &str,
    headers: &[(&str, &str)],
    body: Option<&str>,
    timeout: Duration,
) -> Result<Response, String> {
    let addr: SocketAddr = ([127, 0, 0, 1], port).into();
    let mut s = TcpStream::connect_timeout(&addr, timeout)
        .map_err(|e| format!("connect 127.0.0.1:{port}{path}: {e}"))?;
    s.set_read_timeout(Some(timeout)).ok();
    s.set_write_timeout(Some(timeout)).ok();

    let body = body.unwrap_or("");
    let mut req = format!(
        "{method} {path} HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\nConnection: close\r\n"
    );
    for (k, v) in headers {
        req.push_str(&format!("{k}: {v}\r\n"));
    }
    if !body.is_empty() {
        req.push_str(&format!("Content-Length: {}\r\n", body.len()));
    }
    req.push_str("\r\n");
    req.push_str(body);

    s.write_all(req.as_bytes())
        .map_err(|e| format!("write {path}: {e}"))?;
    let _ = s.shutdown(Shutdown::Write);

    let mut raw = Vec::new();
    s.read_to_end(&mut raw).map_err(|e| format!("read {path}: {e}"))?;
    let raw = String::from_utf8_lossy(&raw).into_owned();

    let status = raw
        .split_whitespace()
        .nth(1)
        .and_then(|c| c.parse::<u16>().ok())
        .ok_or_else(|| format!("no HTTP status line in response to {path}: {}", tail(&raw, 5)))?;
    let body = raw
        .split_once("\r\n\r\n")
        .map(|(_, b)| b.to_string())
        .unwrap_or_default();

    Ok(Response { status, body, raw })
}

pub fn get(port: u16, path: &str) -> Result<Response, String> {
    http(port, "GET", path, &[], None, Duration::from_secs(15))
}

// ---------------------------------------------------------------------------
// Source-level guards
// ---------------------------------------------------------------------------

/// Bind-position port literals in a project's Sky source.
///
/// v2 §7.6: "a gate forbids bind-position port literals in project source." 14
/// examples hardcode `8000`, which is why the sweep could run them in parallel
/// and have them fight over one port while the probe reported OK. A Layer-2
/// project takes its port from the environment or it is not schedulable.
///
/// Returns the offending `file:line` sites.
pub fn bind_position_port_literals(root: &Path, project_rel: &str) -> Vec<String> {
    let mut hits = Vec::new();
    let dir = root.join(project_rel);
    let mut files = Vec::new();
    collect_sky(&dir, &mut files);
    files.sort();

    for f in files {
        let Ok(src) = std::fs::read_to_string(&f) else {
            continue;
        };
        for (i, line) in src.lines().enumerate() {
            let code = line.split("--").next().unwrap_or(line);
            for verb in ["Server.listen", "Live.app", "Tui.app", "Webview.app"] {
                let Some(at) = code.find(verb) else { continue };
                let rest = &code[at + verb.len()..];
                // The first token after the verb. A literal there is a hardcoded
                // bind port; an identifier is a value the app computed (from env).
                if let Some(tok) = rest.split_whitespace().next() {
                    if tok.chars().next().is_some_and(|c| c.is_ascii_digit()) {
                        hits.push(format!(
                            "{}:{}",
                            f.strip_prefix(root).unwrap_or(&f).display(),
                            i + 1
                        ));
                    }
                }
            }
        }
    }
    hits
}

fn collect_sky(dir: &Path, out: &mut Vec<PathBuf>) {
    let Ok(rd) = std::fs::read_dir(dir) else { return };
    for e in rd.flatten() {
        let p = e.path();
        let name = p.file_name().and_then(|n| n.to_str()).unwrap_or("");
        if p.is_dir() {
            if matches!(name, "sky-out" | ".skycache" | ".skydeps") {
                continue;
            }
            collect_sky(&p, out);
        } else if p.extension().and_then(|x| x.to_str()) == Some("sky") {
            out.push(p);
        }
    }
}

// ---------------------------------------------------------------------------

pub fn tail(s: &str, n: usize) -> String {
    let lines: Vec<&str> = s.lines().collect();
    let start = lines.len().saturating_sub(n);
    lines[start..].join("\n")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn free_port_returns_a_port_nothing_holds() {
        let p = free_port().expect("a free port");
        assert!(p > 0);
        assert!(!port_in_use(p), "port {p} was reported free but is in use");
    }

    #[test]
    fn wait_port_released_is_true_for_an_unheld_port() {
        let p = free_port().expect("a free port");
        assert!(wait_port_released(p, Duration::from_millis(200)));
    }

    #[test]
    fn wait_port_released_is_false_while_a_listener_holds_it() {
        // The negative control for the teardown assertion: if this could not
        // report `false`, `Server::shutdown`'s port check would be decorative.
        let l = TcpListener::bind("127.0.0.1:0").expect("bind");
        let p = l.local_addr().unwrap().port();
        assert!(!wait_port_released(p, Duration::from_millis(300)));
        drop(l);
        assert!(wait_port_released(p, Duration::from_secs(2)));
    }

    #[test]
    fn bind_position_literal_is_detected_and_an_env_port_is_not() {
        let tmp = std::env::temp_dir().join(format!("sky-l2-{}", std::process::id()));
        let src = tmp.join("proj/src");
        std::fs::create_dir_all(&src).unwrap();
        std::fs::write(tmp.join("proj/sky.toml"), "name=\"p\"\n").unwrap();

        std::fs::write(src.join("Bad.sky"), "main =\n    Server.listen 8000 routes\n").unwrap();
        let hits = bind_position_port_literals(&tmp, "proj");
        assert_eq!(hits.len(), 1, "hardcoded 8000 must be caught: {hits:?}");

        std::fs::write(src.join("Bad.sky"), "main =\n    Server.listen port routes\n").unwrap();
        let hits = bind_position_port_literals(&tmp, "proj");
        assert!(hits.is_empty(), "an env-derived port must be allowed: {hits:?}");

        let _ = std::fs::remove_dir_all(&tmp);
    }
}

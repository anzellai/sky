//! `xtask build-run` — the reproducible "examples run" gate.
//!
//! For each example the gate classifies its *app shape* from the entry module,
//! then verifies it end-to-end with a shape-appropriate strategy.
//!
//! CLI — build, `go build`, run (feed test stdin), normalise (strip ISO
//! timestamps), diff stdout vs the Haskell oracle binary.
//!
//! Live / Http — build, `go build`, start the server binary on a spare port
//! under a watchdog, wait-for-ready (parse the listening line), `curl /`,
//! normalise (sky-id / data-sky-* / script / style / cookies / csrf /
//! timestamps), diff vs the oracle server started the same way. Both servers
//! are ALWAYS killed (even on timeout).
//!
//! Tui — build, `go build`, run under a pty (`script`) feeding a quit key.
//! RUN = "no-panic" (exits without a Go panic). MATCH is not attempted (pty
//! frame diffing is unreliable).
//!
//! Webview — BUILD only (macOS GUI; can't headless-verify). RUN = n/a.
//!
//! FFI-blocked (05/11/13) — skipped; BLOCKER = "FFI-blocked".
//!
//! Usage:
//!   xtask build-run                       # CLI-family, build-only (fast)
//!   xtask build-run --all                 # whole corpus, build + classify
//!   xtask build-run --all --run           # whole corpus, full verify (run+match)
//!   xtask build-run --only=NAME[,NAME…]   # filter to named examples
//!   xtask build-run --shape cli|live|http|tui|webview   # filter by shape
//!   xtask build-run --run [-v]            # run + oracle match for the selection

use project::{build_example, BuildOptions};
use std::io::{BufRead, BufReader, Write};
use std::net::TcpListener;
use std::path::Path;
use std::process::{Child, Command, Stdio};
use std::sync::mpsc;
use std::time::{Duration, Instant};

/// CLI-family examples the gate targets by default (no `--all`).
const CLI_FAMILY: &[&str] = &[
    "01-hello-world",
    "02-go-stdlib",
    "14-task-demo",
    "07-todo-cli",
    "00-standard-libs",
    "20-cli-counter",
];

/// Examples blocked on unported Go-FFI surfaces (skip entirely).
const FFI_BLOCKED: &[&str] = &["05-mux-server", "11-fyne-stopwatch", "13-skyshop"];

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
enum Shape {
    Cli,
    Live,
    Http,
    Tui,
    Webview,
    Ffi,
}

impl Shape {
    fn label(self) -> &'static str {
        match self {
            Shape::Cli => "cli",
            Shape::Live => "live",
            Shape::Http => "http",
            Shape::Tui => "tui",
            Shape::Webview => "webview",
            Shape::Ffi => "ffi",
        }
    }
    fn from_flag(s: &str) -> Option<Shape> {
        match s {
            "cli" => Some(Shape::Cli),
            "live" => Some(Shape::Live),
            "http" => Some(Shape::Http),
            "tui" => Some(Shape::Tui),
            "webview" => Some(Shape::Webview),
            "ffi" => Some(Shape::Ffi),
            _ => None,
        }
    }
}

/// Per-example stdin to feed the binary (line-oriented CLI/TUI apps read stdin).
fn stdin_for(name: &str) -> Option<String> {
    match name {
        "20-cli-counter" => Some("+\n+\n-\nq\n".to_string()),
        _ => None,
    }
}

struct Row {
    name: String,
    shape: Shape,
    emitted: bool,
    build_ok: bool,
    run_ok: Option<bool>,
    /// Some(true/false) = matched/differed; None = not applicable.
    matched: Option<bool>,
    /// For TUI: RUN is "no-panic" rather than a stdout match.
    run_kind: &'static str, // "match" | "no-panic" | "n/a"
    blocker: String,
}

pub fn run(args: &[String], root: &Path) -> i32 {
    let only: Option<Vec<String>> = args
        .iter()
        .find_map(|a| a.strip_prefix("--only="))
        .map(|s| s.split(',').map(|x| x.trim().to_string()).filter(|x| !x.is_empty()).collect());
    let shape_filter = args
        .iter()
        .position(|a| a == "--shape")
        .and_then(|i| args.get(i + 1))
        .and_then(|s| Shape::from_flag(s));
    let do_verify = args.iter().any(|a| a == "--run" || a == "--oracle" || a == "--serve");
    let verbose = args.iter().any(|a| a == "-v" || a == "--verbose");
    let all = args.iter().any(|a| a == "--all");

    let names: Vec<String> = match &only {
        Some(n) => n.clone(),
        None if all || shape_filter.is_some() => corpus(root),
        None => CLI_FAMILY.iter().map(|s| s.to_string()).collect(),
    };

    let mut rows = Vec::new();
    for name in &names {
        let dir = root.join("examples").join(name);
        if !dir.is_dir() {
            continue;
        }
        let shape = classify(&dir, name);
        if let Some(want) = shape_filter {
            if shape != want {
                continue;
            }
        }
        let row = verify_one(root, &dir, name, shape, do_verify, verbose);
        rows.push(row);
    }

    print_table(&rows);
    gate_result(&rows)
}

fn corpus(root: &Path) -> Vec<String> {
    let mut ds: Vec<String> = std::fs::read_dir(root.join("examples"))
        .map(|rd| {
            rd.filter_map(|e| e.ok())
                .filter(|e| e.path().is_dir())
                .filter_map(|e| e.file_name().to_str().map(String::from))
                .filter(|n| root.join("examples").join(n).join("src").is_dir())
                // skip the non-example scratch dirs
                .filter(|n| n != "simple" && n != "test_pkg")
                .collect()
        })
        .unwrap_or_default();
    ds.sort();
    ds
}

// ---- shape classification ------------------------------------------------

/// Detect an example's app shape from its entry module's `main` binding, using
/// the DEFAULT (no-args) branch for arg-dispatched multi-backend mains, and
/// falling back to a whole-example scan when the main body only delegates.
fn classify(dir: &Path, name: &str) -> Shape {
    if FFI_BLOCKED.contains(&name) {
        return Shape::Ffi;
    }
    let src = dir.join("src");
    let entry = entry_source(&src);
    let main_body = entry.as_deref().map(main_binding).unwrap_or_default();

    // Arg-dispatched mains (24 kitchen-sink, 38 multibackend): the no-args run
    // takes the wildcard `_ ->` arm, so classify by the LAST arm's call.
    let seg = if (main_body.contains("argsList") || main_body.contains("System.args"))
        && main_body.contains("case")
    {
        main_body
            .rsplit_once("_ ->")
            .map(|(_, tail)| tail.to_string())
            .unwrap_or(main_body.clone())
    } else {
        main_body.clone()
    };

    if let Some(s) = shape_of_segment(&seg) {
        return s;
    }
    // Fallback: the main body only delegates (e.g. `entry ()`); scan all src.
    whole_example_shape(&src).unwrap_or(Shape::Cli)
}

fn shape_of_segment(seg: &str) -> Option<Shape> {
    if seg.contains("Webview.app") || seg.contains("Webview.program") {
        return Some(Shape::Webview);
    }
    if seg.contains("Tui.app") || seg.contains("Tui.program") {
        return Some(Shape::Tui);
    }
    if seg.contains("Live.app") {
        return Some(Shape::Live);
    }
    if seg.contains("Server.listen") || seg.contains("HttpServer.listen") {
        return Some(Shape::Http);
    }
    // bare `app { … routes … notFound … }` from
    // `import Std.Live exposing (app, route)`. Reached only after Webview/Tui/
    // Live.app are ruled out, so `notFound` here always means a bare Live app.
    if seg.contains("notFound") && (seg.contains("routes") || seg.contains("app")) {
        return Some(Shape::Live);
    }
    None
}

fn whole_example_shape(src: &Path) -> Option<Shape> {
    let mut files = Vec::new();
    collect_sky(src, &mut files);
    let mut blob = String::new();
    for f in &files {
        if let Ok(s) = std::fs::read_to_string(f) {
            blob.push_str(&s);
            blob.push('\n');
        }
    }
    if blob.contains("Server.listen") || blob.contains("HttpServer.listen") {
        Some(Shape::Http)
    } else if blob.contains("Live.app") {
        Some(Shape::Live)
    } else if blob.contains("Tui.app") || blob.contains("Tui.program") {
        Some(Shape::Tui)
    } else if blob.contains("Webview.app") {
        Some(Shape::Webview)
    } else {
        None
    }
}

/// Read the source of the entry module (the src file defining a top-level `main`).
fn entry_source(src: &Path) -> Option<String> {
    let mut files = Vec::new();
    collect_sky(src, &mut files);
    // prefer a file literally named Main.sky, else the first with a `main` def.
    files.sort();
    let mut fallback = None;
    for f in &files {
        let Ok(s) = std::fs::read_to_string(f) else { continue };
        let has_main = s.lines().any(|l| l.starts_with("main ") || l == "main" || l.starts_with("main="));
        if has_main {
            if f.file_name().and_then(|n| n.to_str()) == Some("Main.sky") {
                return Some(s);
            }
            if fallback.is_none() {
                fallback = Some(s);
            }
        }
    }
    fallback
}

/// Extract the text of the top-level `main` binding (from its `main`/`main :`
/// header to the next top-level definition).
fn main_binding(src: &str) -> String {
    let mut out = String::new();
    let mut in_main = false;
    for line in src.lines() {
        let starts_main = line.starts_with("main ")
            || line == "main"
            || line.starts_with("main=")
            || line.starts_with("main:");
        if starts_main {
            in_main = true;
            out.push_str(line);
            out.push('\n');
            continue;
        }
        if in_main {
            // a new top-level binding starts at column 0 with an identifier and
            // is not a continuation of main.
            let is_toplevel = !line.is_empty()
                && !line.starts_with(char::is_whitespace)
                && !line.starts_with("main");
            if is_toplevel {
                break;
            }
            out.push_str(line);
            out.push('\n');
        }
    }
    out
}

// ---- per-example verification -------------------------------------------

fn verify_one(
    root: &Path,
    dir: &Path,
    name: &str,
    shape: Shape,
    do_verify: bool,
    verbose: bool,
) -> Row {
    // FFI / Webview: emit+build only (Webview needs cgo/GUI; FFI is skipped).
    if shape == Shape::Ffi {
        return Row {
            name: name.into(),
            shape,
            emitted: false,
            build_ok: false,
            run_ok: None,
            matched: None,
            run_kind: "n/a",
            blocker: "FFI-blocked".into(),
        };
    }

    // CLI runs through build_example's own run+stdin path; servers/tui build
    // without the blocking run (we drive the process ourselves).
    let want_inline_run = do_verify && shape == Shape::Cli;
    let opts = BuildOptions {
        repo_root: root.to_path_buf(),
        example_dir: dir.to_path_buf(),
        out_dir_name: "sky-out-rust".into(),
        run: want_inline_run,
        stdin: stdin_for(name),
    };
    let rep = build_example(&opts);
    let out_dir = dir.join("sky-out-rust");

    let mut blocker = String::new();
    if !rep.emitted {
        blocker = rep.note.clone();
    } else if !rep.go_build_ok {
        blocker = first_go_error(&rep.go_build_stderr);
    }
    if verbose && !rep.go_build_ok && !rep.go_build_stderr.is_empty() {
        eprintln!("  [{name}] go build stderr:\n{}", indent(&rep.go_build_stderr, 6));
    }

    let mut run_ok = None;
    let mut matched = None;
    let mut run_kind = "n/a";

    if do_verify && rep.go_build_ok {
        match shape {
            Shape::Cli => {
                run_kind = "match";
                run_ok = rep.run_ok;
                if rep.run_ok == Some(true) {
                    matched = compare_cli_oracle(
                        dir,
                        rep.run_stdout.as_deref().unwrap_or(""),
                        stdin_for(name),
                    );
                    if matched == Some(false) && blocker.is_empty() {
                        blocker = "stdout != oracle".into();
                    }
                } else if blocker.is_empty() {
                    blocker = truncate(rep.run_stderr.as_deref().unwrap_or("run failed"), 60);
                }
            }
            Shape::Live | Shape::Http => {
                run_kind = "match";
                let (ok, m, note) = verify_server(dir, &out_dir, name, shape, verbose);
                run_ok = Some(ok);
                matched = m;
                if !note.is_empty() && blocker.is_empty() {
                    blocker = note;
                }
            }
            Shape::Tui => {
                run_kind = "no-panic";
                let (ok, note) = verify_tui(&out_dir, name);
                run_ok = Some(ok);
                matched = None;
                if !note.is_empty() && blocker.is_empty() {
                    blocker = note;
                }
            }
            Shape::Webview => {
                run_kind = "n/a";
                if blocker.is_empty() {
                    blocker = "macOS GUI — build-only".into();
                }
            }
            Shape::Ffi => {}
        }
    }

    Row {
        name: name.into(),
        shape,
        emitted: rep.emitted,
        build_ok: rep.go_build_ok,
        run_ok,
        matched,
        run_kind,
        blocker,
    }
}

// ---- CLI oracle compare (stdout) ----------------------------------------

fn compare_cli_oracle(dir: &Path, rust_stdout: &str, stdin: Option<String>) -> Option<bool> {
    let app = dir.join("sky-out").join("app");
    if !app.exists() {
        return None;
    }
    let mut cmd = Command::new(&app);
    cmd.current_dir(dir.join("sky-out"))
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    let mut child = cmd.spawn().ok()?;
    if let Some(data) = &stdin {
        if let Some(mut si) = child.stdin.take() {
            let _ = si.write_all(data.as_bytes());
        }
    } else {
        drop(child.stdin.take());
    }
    let out = wait_bounded(&mut child, Duration::from_secs(20))?;
    let oracle = String::from_utf8_lossy(&out.stdout).to_string();
    Some(normalise_stdout(&oracle) == normalise_stdout(rust_stdout))
}

// ---- server verification (Live / Http) ----------------------------------

/// Start rust + oracle server binaries (sequentially, same spare port), fetch
/// `/` from each, normalise, compare. Returns (run_ok, matched, blocker).
fn verify_server(
    dir: &Path,
    out_dir: &Path,
    name: &str,
    shape: Shape,
    verbose: bool,
) -> (bool, Option<bool>, String) {
    let port = free_port().unwrap_or(0);
    let rust_app = out_dir.join("app");
    let rust = serve_and_fetch(&rust_app, out_dir, port, shape);
    if verbose {
        eprintln!("  [{name}] rust: started={} port={:?}", rust.started, rust.port);
    }
    if !rust.started {
        return (false, None, truncate(&rust.note, 60));
    }

    let oracle_app = dir.join("sky-out").join("app");
    if !oracle_app.exists() {
        // rust served but no oracle to compare against.
        return (true, None, "no oracle binary".into());
    }
    let oracle = serve_and_fetch(&oracle_app, &dir.join("sky-out"), port, shape);
    if verbose {
        eprintln!("  [{name}] oracle: started={} port={:?}", oracle.started, oracle.port);
    }
    if !oracle.started {
        return (true, None, format!("oracle failed: {}", truncate(&oracle.note, 40)));
    }

    let m = normalise_html(&rust.body) == normalise_html(&oracle.body);
    let note = if m { String::new() } else { "page != oracle".into() };
    (true, Some(m), note)
}

struct ServerRun {
    started: bool,
    port: Option<u16>,
    body: String,
    note: String,
}

/// Spawn a server binary, wait for its "listening" line, curl `/`, then ALWAYS
/// kill it. Watchdog-bounded; the child is killed on every exit path.
fn serve_and_fetch(app: &Path, cwd: &Path, spare: u16, _shape: Shape) -> ServerRun {
    let mut cmd = Command::new(app);
    cmd.current_dir(cwd)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .env("SKY_LIVE_PORT", spare.to_string())
        .env("PORT", spare.to_string())
        .env("SKY_LIVE_STORE", "memory")
        .env("SKY_CONSOLE_EMBED", "off")
        .env("SKY_DEV_BANNER", "off")
        .env("SKY_LIVE_BANNER", "off")
        .env("ENV", "dev");
    let mut child = match cmd.spawn() {
        Ok(c) => c,
        Err(e) => {
            return ServerRun { started: false, port: None, body: String::new(), note: format!("spawn: {e}") }
        }
    };

    // Read stdout lines on a thread; report the first parsed listening port.
    // The tx drops when stdout hits EOF (process exited) → recv returns
    // Disconnected, so a server that dies on start is detected without waiting.
    let (tx, rx) = mpsc::channel::<u16>();
    if let Some(stdout) = child.stdout.take() {
        std::thread::spawn(move || {
            let reader = BufReader::new(stdout);
            for line in reader.lines().map_while(Result::ok) {
                if line.to_lowercase().contains("listening") {
                    if let Some(p) = last_colon_number(&line) {
                        let _ = tx.send(p);
                    }
                }
            }
        });
    }
    // Drain stderr on a thread so a panic reason is available for the note.
    let (etx, erx) = mpsc::channel::<String>();
    if let Some(stderr) = child.stderr.take() {
        std::thread::spawn(move || {
            let mut buf = String::new();
            let reader = BufReader::new(stderr);
            for line in reader.lines().map_while(Result::ok) {
                buf.push_str(&line);
                buf.push('\n');
            }
            let _ = etx.send(buf);
        });
    }

    // Wait for the listening line, or fall back to the spare port on timeout.
    // On Disconnected (stdout closed) the process already exited — bail fast.
    let port = match rx.recv_timeout(Duration::from_secs(8)) {
        Ok(p) => Some(p),
        Err(mpsc::RecvTimeoutError::Timeout) => Some(spare),
        Err(mpsc::RecvTimeoutError::Disconnected) => None,
    };

    // Poll curl until the page responds (or give up).
    let mut body = String::new();
    if let Some(port) = port {
        let deadline = Instant::now() + Duration::from_secs(6);
        while Instant::now() < deadline {
            if let Some(b) = curl_get(port) {
                if !b.trim().is_empty() {
                    body = b;
                    break;
                }
            }
            std::thread::sleep(Duration::from_millis(150));
        }
    }

    // ALWAYS kill + reap.
    let _ = child.kill();
    let _ = child.wait();

    let started = !body.is_empty();
    let note = if started {
        String::new()
    } else {
        let stderr = erx.recv_timeout(Duration::from_millis(500)).unwrap_or_default();
        panic_reason(&stderr).unwrap_or_else(|| match port {
            Some(p) => format!("no response on :{p}"),
            None => "server exited on start".into(),
        })
    };
    ServerRun { started, port, body, note }
}

fn curl_get(port: u16) -> Option<String> {
    let url = format!("http://127.0.0.1:{port}/");
    let out = Command::new("curl")
        .arg("-s")
        .arg("--max-time")
        .arg("4")
        .arg(&url)
        .output()
        .ok()?;
    if out.status.success() {
        Some(String::from_utf8_lossy(&out.stdout).to_string())
    } else {
        None
    }
}

/// Grab a free TCP port by binding :0 and reading the assigned port.
fn free_port() -> Option<u16> {
    let l = TcpListener::bind("127.0.0.1:0").ok()?;
    l.local_addr().ok().map(|a| a.port())
}

/// Parse the trailing `:<digits>` of a "listening on …:PORT" line.
fn last_colon_number(s: &str) -> Option<u16> {
    let idx = s.rfind(':')?;
    let digits: String = s[idx + 1..].chars().take_while(|c| c.is_ascii_digit()).collect();
    digits.parse().ok()
}

// ---- TUI verification (pty, no-panic) ------------------------------------

/// Run the TUI binary under a pty (`script`), feed a quit key, and assert it
/// exits without a Go panic. Frame-diffing is not attempted (pty capture is
/// unreliable across terminals) — RUN reports "no-panic".
fn verify_tui(out_dir: &Path, _name: &str) -> (bool, String) {
    let app = out_dir.join("app");
    if !app.exists() {
        return (false, "no binary".into());
    }
    // macOS: `script -q /dev/null <cmd>` runs cmd in a pty; stdin is forwarded.
    // Bound the whole thing with `timeout` so a wedged TUI can't hang the gate.
    let mut cmd = Command::new("timeout");
    cmd.arg("6")
        .arg("script")
        .arg("-q")
        .arg("/dev/null")
        .arg("./app")
        .current_dir(out_dir)
        .env("TERM", "xterm")
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    let mut child = match cmd.spawn() {
        Ok(c) => c,
        Err(e) => return (false, format!("spawn: {e}")),
    };
    if let Some(mut si) = child.stdin.take() {
        // 'q' then Ctrl-C as a fallback quit for apps that ignore 'q'.
        let _ = si.write_all(b"q\n\x03");
    }
    let out = match wait_bounded(&mut child, Duration::from_secs(10)) {
        Some(o) => o,
        None => return (false, "tui hung".into()),
    };
    let combined = format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    if has_panic(&combined) {
        return (false, "panic on start".into());
    }
    (true, String::new())
}

/// Extract a short reason from a Sky runtime panic line, if present.
fn panic_reason(stderr: &str) -> Option<String> {
    let line = stderr.lines().find(|l| l.contains("Sky panic:") || l.contains("panicKind="))?;
    // prefer the `panicKind=<Kind>` token; else the text after "Sky panic:".
    if let Some(pos) = line.find("panicKind=") {
        let kind: String = line[pos + "panicKind=".len()..]
            .chars()
            .take_while(|c| !c.is_whitespace())
            .collect();
        return Some(format!("panic on start: {kind}"));
    }
    let after = line.split("Sky panic:").nth(1)?.trim();
    Some(format!("panic on start: {}", truncate(after, 40)))
}

fn has_panic(s: &str) -> bool {
    s.contains("panic:")
        || s.contains("goroutine ")
        || s.contains("runtime error:")
        || s.contains("[signal SIGSEGV")
}

// ---- process helper ------------------------------------------------------

/// Wait for a child up to `dur`, killing it if it overruns. Returns its output.
fn wait_bounded(child: &mut Child, dur: Duration) -> Option<std::process::Output> {
    let deadline = Instant::now() + dur;
    loop {
        match child.try_wait() {
            Ok(Some(_)) => break,
            Ok(None) => {
                if Instant::now() >= deadline {
                    let _ = child.kill();
                    let _ = child.wait();
                    break;
                }
                std::thread::sleep(Duration::from_millis(50));
            }
            Err(_) => break,
        }
    }
    // Drain stdout/stderr after exit.
    let mut stdout = Vec::new();
    let mut stderr = Vec::new();
    if let Some(mut o) = child.stdout.take() {
        let _ = std::io::Read::read_to_end(&mut o, &mut stdout);
    }
    if let Some(mut e) = child.stderr.take() {
        let _ = std::io::Read::read_to_end(&mut e, &mut stderr);
    }
    Some(std::process::Output {
        status: std::process::ExitStatus::default(),
        stdout,
        stderr,
    })
}

// ---- normalisation -------------------------------------------------------

/// Strip volatile stdout bits (leading ISO-8601 log timestamps + wall-clock
/// `HH:MM:SS` tokens, which differ when the second ticks between the rust run
/// and the oracle run — e.g. `Current time: 12:21:10`).
fn normalise_stdout(s: &str) -> String {
    let deiso = s
        .lines()
        .map(strip_leading_timestamp)
        .collect::<Vec<_>>()
        .join("\n");
    strip_uuids(&strip_clock_times(&deiso)).trim().to_string()
}

/// Replace any RFC-4122 UUID (`8-4-4-4-12` hex, hyphenated) with `<uuid>`.
/// A freshly generated UUID (`Uuid.newString`, `Uuid.v4`) is volatile output
/// like a timestamp — the structure is what a stdout/page compare should assert,
/// not the random value.
fn strip_uuids(s: &str) -> String {
    let chars: Vec<char> = s.chars().collect();
    let mut out = String::with_capacity(s.len());
    let mut i = 0;
    while i < chars.len() {
        if i + 36 <= chars.len() && looks_like_uuid(&chars[i..i + 36]) {
            out.push_str("<uuid>");
            i += 36;
        } else {
            out.push(chars[i]);
            i += 1;
        }
    }
    out
}

fn looks_like_uuid(c: &[char]) -> bool {
    if c.len() != 36 {
        return false;
    }
    for (i, ch) in c.iter().enumerate() {
        let ok = match i {
            8 | 13 | 18 | 23 => *ch == '-',
            _ => ch.is_ascii_hexdigit(),
        };
        if !ok {
            return false;
        }
    }
    true
}

/// Replace any `HH:MM:SS` run with `<time>` (structural; two-digit fields).
fn strip_clock_times(s: &str) -> String {
    let chars: Vec<char> = s.chars().collect();
    let mut out = String::with_capacity(s.len());
    let mut i = 0;
    while i < chars.len() {
        if i + 8 <= chars.len() && looks_like_clock(&chars[i..i + 8]) {
            out.push_str("<time>");
            i += 8;
        } else {
            out.push(chars[i]);
            i += 1;
        }
    }
    out
}

fn looks_like_clock(c: &[char]) -> bool {
    c.len() == 8
        && c[0].is_ascii_digit()
        && c[1].is_ascii_digit()
        && c[2] == ':'
        && c[3].is_ascii_digit()
        && c[4].is_ascii_digit()
        && c[5] == ':'
        && c[6].is_ascii_digit()
        && c[7].is_ascii_digit()
}

/// Normalise an HTML page for a shape-stable compare: drop runtime-assigned
/// ids, per-render tokens, and injected scripts that legitimately vary.
fn normalise_html(s: &str) -> String {
    let mut out = strip_between(s, "<script", "</script>");
    out = strip_between(&out, "<style", "</style>");
    out = strip_attr(&out, "sky-id");
    out = strip_attr_prefix(&out, "data-sky-");
    out = strip_iso_timestamps(&out);
    out = strip_clock_times(&out);
    out = strip_uuids(&out);
    out = strip_csrf(&out);
    // collapse runs of whitespace so cosmetic reflow doesn't diff.
    out.split_whitespace().collect::<Vec<_>>().join(" ")
}

/// Remove `attr="…"` occurrences (name exact).
fn strip_attr(s: &str, attr: &str) -> String {
    let needle = format!("{attr}=\"");
    strip_attr_needle(s, &needle)
}

/// Remove `prefix*="…"` occurrences (attributes whose name starts with prefix).
/// UTF-8 safe: only ever slices at byte offsets returned by `find`/`char_indices`.
fn strip_attr_prefix(s: &str, prefix: &str) -> String {
    let mut out = String::with_capacity(s.len());
    let mut i = 0;
    while i < s.len() {
        if s[i..].starts_with(prefix) {
            // scan to `="` then to the closing quote.
            if let Some(eq) = s[i..].find("=\"") {
                let after = i + eq + 2;
                if let Some(close) = s[after..].find('"') {
                    // ensure the span between prefix and `=` is an attr name (no spaces/>)
                    let name = &s[i..i + eq];
                    if !name.contains(|c: char| c.is_whitespace() || c == '>' || c == '<') {
                        i = after + close + 1;
                        continue;
                    }
                }
            }
        }
        // advance by one full char (never split a multibyte scalar).
        let ch = s[i..].chars().next().unwrap();
        out.push(ch);
        i += ch.len_utf8();
    }
    out
}

fn strip_attr_needle(s: &str, needle: &str) -> String {
    let mut out = String::with_capacity(s.len());
    let mut rest = s;
    while let Some(pos) = rest.find(needle) {
        out.push_str(&rest[..pos]);
        let after = &rest[pos + needle.len()..];
        match after.find('"') {
            Some(q) => rest = &after[q + 1..],
            None => {
                rest = "";
                break;
            }
        }
    }
    out.push_str(rest);
    out
}

/// Remove everything between `open` (up to its `>`) and `close`, inclusive.
fn strip_between(s: &str, open: &str, close: &str) -> String {
    let mut out = String::with_capacity(s.len());
    let mut rest = s;
    while let Some(pos) = rest.find(open) {
        out.push_str(&rest[..pos]);
        let after = &rest[pos..];
        match after.find(close) {
            Some(end) => rest = &after[end + close.len()..],
            None => {
                rest = "";
                break;
            }
        }
    }
    out.push_str(rest);
    out
}

/// Blank ISO-8601/RFC3339 timestamps anywhere in the text.
fn strip_iso_timestamps(s: &str) -> String {
    // structural: replace any `dddd-dd-ddTdd:dd:dd…Z`-shaped run with <ts>.
    let mut out = String::with_capacity(s.len());
    let chars: Vec<char> = s.chars().collect();
    let mut i = 0;
    while i < chars.len() {
        if i + 20 <= chars.len() && looks_like_iso(&chars[i..]) {
            out.push_str("<ts>");
            // advance past the timestamp (until the trailing 'Z').
            let mut j = i;
            while j < chars.len() && chars[j] != 'Z' {
                j += 1;
            }
            i = j + 1;
        } else {
            out.push(chars[i]);
            i += 1;
        }
    }
    out
}

fn looks_like_iso(c: &[char]) -> bool {
    c.len() >= 20
        && c[0..4].iter().all(|x| x.is_ascii_digit())
        && c[4] == '-'
        && c[5].is_ascii_digit()
        && c[6].is_ascii_digit()
        && c[7] == '-'
        && c[10] == 'T'
        && c[13] == ':'
        && c[16] == ':'
}

/// Blank csrf token values (`name="_csrf" value="…"` / `csrf=…`).
fn strip_csrf(s: &str) -> String {
    let mut out = strip_attr_needle(s, "_csrf\" value=\"");
    out = strip_attr_needle(&out, "csrf-token\" content=\"");
    out
}

fn strip_leading_timestamp(line: &str) -> String {
    let (head, rest) = match line.split_once(' ') {
        Some(p) => p,
        None => return line.to_string(),
    };
    if is_iso_timestamp(head) {
        format!("<ts> {rest}")
    } else {
        line.to_string()
    }
}

fn is_iso_timestamp(tok: &str) -> bool {
    let b = tok.as_bytes();
    tok.len() >= 20
        && tok.ends_with('Z')
        && b.get(4) == Some(&b'-')
        && b.get(7) == Some(&b'-')
        && b.get(10) == Some(&b'T')
        && b.get(13) == Some(&b':')
        && b.get(16) == Some(&b':')
}

// ---- misc ---------------------------------------------------------------

fn first_go_error(stderr: &str) -> String {
    stderr
        .lines()
        .find(|l| l.contains("error") || l.contains(".go:"))
        .unwrap_or("go build failed")
        .trim()
        .chars()
        .take(70)
        .collect()
}

fn truncate(s: &str, n: usize) -> String {
    let one = s.lines().next().unwrap_or("").trim();
    one.chars().take(n).collect()
}

fn indent(s: &str, n: usize) -> String {
    let pad = " ".repeat(n);
    s.lines().map(|l| format!("{pad}{l}")).collect::<Vec<_>>().join("\n")
}

fn collect_sky(dir: &Path, out: &mut Vec<std::path::PathBuf>) {
    let mut entries: Vec<std::path::PathBuf> = match std::fs::read_dir(dir) {
        Ok(rd) => rd.filter_map(|e| e.ok().map(|e| e.path())).collect(),
        Err(_) => return,
    };
    entries.sort();
    for path in entries {
        let is_gen = path.components().any(|c| {
            matches!(
                c.as_os_str().to_str(),
                Some("sky-out") | Some("sky-out-rust") | Some(".skycache") | Some(".skydeps")
            )
        });
        if is_gen {
            continue;
        }
        if path.is_dir() {
            collect_sky(&path, out);
        } else if path.extension().and_then(|e| e.to_str()) == Some("sky") {
            out.push(path);
        }
    }
}

fn print_table(rows: &[Row]) {
    let w = rows.iter().map(|r| r.name.len()).max().unwrap_or(8).max(8);
    println!(
        "{:<w$}  {:>7}  {:>7}  {:>5}  {:>8}  {:>6}  BLOCKER",
        "EXAMPLE", "SHAPE", "EMITTED", "BUILD", "RUN", "MATCH",
        w = w
    );
    println!("{}", "-".repeat(w + 60));
    let (mut nb, mut nr, mut nm, mut denom, mut mdenom) = (0, 0, 0, 0, 0);
    for r in rows {
        let build = if r.build_ok { "ok" } else if r.emitted { "FAIL" } else { "-" };
        if r.build_ok {
            nb += 1;
        }
        if r.shape != Shape::Ffi {
            denom += 1;
        }
        let run = match r.run_ok {
            Some(true) => {
                nr += 1;
                if r.run_kind == "no-panic" { "no-panic" } else { "ok" }
            }
            Some(false) => "FAIL",
            None => "-",
        };
        let matchc = match r.matched {
            Some(true) => {
                nm += 1;
                mdenom += 1;
                "match"
            }
            Some(false) => {
                mdenom += 1;
                "DIFF"
            }
            None => "-",
        };
        let emitted = if r.emitted { "yes" } else { "no" };
        println!(
            "{:<w$}  {:>7}  {:>7}  {:>5}  {:>8}  {:>6}  {}",
            r.name,
            r.shape.label(),
            emitted,
            build,
            run,
            matchc,
            r.blocker,
            w = w
        );
    }
    println!("{}", "-".repeat(w + 60));
    let n = rows.len();
    println!(
        "TOTALS  |  build {nb}/{denom} (non-ffi)  |  run-ok {nr}/{denom}  |  run+match {nm}/{mdenom}  |  examples {n}"
    );
}

/// Gate: hello-world must build (+ run when verified). Non-zero on regression.
fn gate_result(rows: &[Row]) -> i32 {
    match rows.iter().find(|r| r.name == "01-hello-world") {
        Some(r) if r.build_ok && r.run_ok != Some(false) => 0,
        Some(_) => 1,
        None => 0, // hello-world not in this selection; nothing to assert.
    }
}

//! A script that starts a server stops it on every exit path.
//!
//! # The defect this exists to remove
//!
//! A gate e2e run left its app server listening for hours, holding a port on
//! the development machine. The harnesses stopped their servers on the path
//! where every step passed, and on no other:
//!
//! * the Node verifiers (`scripts/*.mjs`) spawn the app and kill it in a
//!   `finally`. A signal from the shell or CI, a `process.exit` from a check,
//!   or an error thrown before the `try` ends Node without reaching it, and
//!   the app is orphaned;
//! * shell scripts started a server with `&`, kept `$!`, and killed it at the
//!   end of the happy path, with no trap, or started it inside a subshell
//!   `( cd … && app ) &`, so `$!` was the subshell and killing it left the app
//!   running.
//!
//! # The rules
//!
//! 1. A shell script that keeps a background PID (`NAME=$!`, `NAME+=($!)` or
//!    `echo $!`)
//!    installs traps that together cover EXIT, INT and TERM.
//! 2. Every `spawn(` in a Node script is `guardChild(spawn(`, from
//!    [`GUARD`], which kills the child on `exit` (a `process.exit`, the end of
//!    the loop, an uncaught error, a rejected promise) and on SIGINT, SIGTERM
//!    and SIGHUP.
//! 3. [`GUARD`] does what rule 2 relies on: run under Node, a guarded child is
//!    gone after its parent throws, rejects, calls `process.exit`, or takes a
//!    SIGTERM. An unguarded child outlives a throw (asserted too, so the test
//!    proves it can see a leak).
//!
//! # What this does NOT catch
//!
//! * A server started without keeping its PID (a bare `app &`). There is
//!   nothing to trap on; such a line has no exact PID to stop, which is its
//!   own defect, and review has to find it.
//! * A trap whose body does not kill the PID it should. The rule checks that
//!   the traps exist, not what they do.
//! * SIGKILL of a Node harness. It cannot be caught; `with_timeout` kills the
//!   whole process group for that case.

use std::path::{Path, PathBuf};
use std::process::Command;

#[path = "../../sky/src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

/// The guard every Node spawn goes through.
const GUARD: &str = "scripts/lib/child-guard.mjs";

/// Frozen records of past runs, not live gates.
const FROZEN_PREFIXES: &[&str] = &["docs/history/", "docs/perf/runs/"];

fn repo() -> PathBuf {
    PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../.."))
}

/// Every file under the repository with one of `exts`, as (repo-relative
/// path, contents). Build output and dot-directories are skipped.
fn files_with(exts: &[&str]) -> Vec<(String, String)> {
    fn walk(dir: &Path, exts: &[&str], out: &mut Vec<PathBuf>) {
        let Ok(rd) = std::fs::read_dir(dir) else {
            return;
        };
        for e in rd.flatten() {
            let p = e.path();
            let name = e.file_name().to_string_lossy().to_string();
            if p.is_dir() {
                let skip = matches!(
                    name.as_str(),
                    "target" | "node_modules" | "sky-out" | "local-target" | ".split"
                ) || name.starts_with('.');
                if !skip {
                    walk(&p, exts, out);
                }
            } else if p
                .extension()
                .and_then(|x| x.to_str())
                .is_some_and(|x| exts.contains(&x))
            {
                out.push(p);
            }
        }
    }
    let root = repo();
    let mut files = Vec::new();
    walk(&root, exts, &mut files);
    files.sort();
    files
        .into_iter()
        .filter_map(|p| {
            let rel = p
                .strip_prefix(&root)
                .ok()?
                .to_string_lossy()
                .replace('\\', "/");
            if FROZEN_PREFIXES.iter().any(|f| rel.starts_with(f)) {
                return None;
            }
            let text = std::fs::read_to_string(&p).ok()?;
            Some((rel, text))
        })
        .collect()
}

/// The code part of a shell line (a `#` comment dropped; a `#` inside a word
/// such as `${#x}` or `$#` is kept).
fn shell_code(line: &str) -> &str {
    let b = line.as_bytes();
    for i in 0..b.len() {
        if b[i] == b'#' && (i == 0 || b[i - 1] == b' ' || b[i - 1] == b'\t') {
            return &line[..i];
        }
    }
    line
}

/// True when `code` keeps a background PID: `NAME=$!`, `NAME+=($!)` or
/// `echo $!`.
fn keeps_background_pid(code: &str) -> bool {
    if code.contains("echo $!") || code.contains("=($!)") {
        return true;
    }
    let b = code.as_bytes();
    let mut from = 0;
    while let Some(off) = code[from..].find("=$!") {
        let at = from + off;
        if at > 0 && (b[at - 1].is_ascii_alphanumeric() || b[at - 1] == b'_') {
            return true;
        }
        from = at + 3;
    }
    false
}

/// The signals named by the `trap` lines of a script.
fn trapped_signals(text: &str) -> Vec<String> {
    let mut out = Vec::new();
    for line in text.lines() {
        let code = shell_code(line).trim();
        let Some(rest) = code.strip_prefix("trap ") else {
            continue;
        };
        // `trap - EXIT` only removes a trap.
        if rest.trim_start().starts_with("- ") {
            continue;
        }
        // The signal list follows the (possibly quoted) handler: take the
        // words after the last quote, or after the first word when unquoted.
        let tail = match rest.rfind(['\'', '"']) {
            Some(i) => &rest[i + 1..],
            None => rest.split_once(' ').map(|(_, t)| t).unwrap_or(""),
        };
        out.extend(tail.split_whitespace().map(str::to_string));
    }
    out
}

#[test]
fn a_script_that_keeps_a_background_pid_traps_exit_int_and_term() {
    let scripts = files_with(&["sh"]);
    assert!(
        scripts.iter().any(|(p, _)| p == "scripts/example-e2e.sh"),
        "the scan must see scripts/ (found {} shell scripts)",
        scripts.len()
    );
    let mut bad = Vec::new();
    let mut checked = 0;
    for (rel, text) in &scripts {
        let keeps = text.lines().any(|l| keeps_background_pid(shell_code(l)));
        if !keeps {
            continue;
        }
        checked += 1;
        let sigs = trapped_signals(text);
        let missing: Vec<&str> = ["EXIT", "INT", "TERM"]
            .into_iter()
            .filter(|s| !sigs.iter().any(|t| t == s || t == &format!("SIG{s}")))
            .collect();
        if !missing.is_empty() {
            bad.push(format!("{rel}: no trap for {}", missing.join(", ")));
        }
    }
    assert!(
        checked >= 5,
        "only {checked} scripts keep a background PID; the scan has gone blind"
    );
    assert!(
        bad.is_empty(),
        "a script starts a background process and does not stop it on every exit \
         path. Record the PID and add `trap <stop> EXIT` plus INT and TERM traps \
         that kill that exact PID:\n  {}",
        bad.join("\n  ")
    );
}

#[test]
fn the_shell_rule_sees_the_shapes_it_forbids() {
    assert!(keeps_background_pid("APP_PID=$!"));
    assert!(keeps_background_pid("    local pid=$!"));
    assert!(keeps_background_pid("    echo $!"));
    assert!(keeps_background_pid("PIDS+=($!)"));
    assert!(!keeps_background_pid("die \"fork failed: $!\\n\""));
    let text = "app &\nPID=$!\ntrap 'kill $PID' EXIT\n";
    assert_eq!(trapped_signals(text), vec!["EXIT"]);
    let text = "trap cleanup EXIT INT TERM\ntrap - EXIT\n";
    assert_eq!(trapped_signals(text), vec!["EXIT", "INT", "TERM"]);
    assert_eq!(
        trapped_signals("trap 'stop; exit 130' INT # on Ctrl-C\n"),
        vec!["INT"]
    );
}

/// The `spawn(` calls in `text` that are not `guardChild(spawn(`.
fn unguarded_spawns(text: &str) -> Vec<usize> {
    let mut out = Vec::new();
    for (n, line) in text.lines().enumerate() {
        let mut from = 0;
        while let Some(off) = line[from..].find("spawn(") {
            let at = from + off;
            let prev = line[..at].chars().last();
            let is_call = !prev.is_some_and(|c| c.is_alphanumeric() || c == '_' || c == '.');
            let trimmed = line.trim_start();
            let comment = trimmed.starts_with("//") || trimmed.starts_with('*');
            if is_call && !comment && !line[..at].ends_with("guardChild(") {
                out.push(n + 1);
            }
            from = at + 6;
        }
    }
    out
}

#[test]
fn every_node_spawn_is_guarded() {
    let scripts = files_with(&["mjs", "js"]);
    let mut bad = Vec::new();
    let mut guarded = 0;
    for (rel, text) in &scripts {
        if rel == GUARD || !(rel.starts_with("scripts/") || rel.starts_with("apps/")) {
            continue;
        }
        guarded += text.matches("guardChild(spawn(").count();
        for line in unguarded_spawns(text) {
            bad.push(format!("{rel}:{line}"));
        }
    }
    assert!(
        guarded >= 20,
        "only {guarded} guarded spawns under scripts/; the scan has gone blind"
    );
    assert!(
        bad.is_empty(),
        "a Node script spawns a process that outlives it when it exits another way \
         than through its `finally`. Wrap it: `guardChild(spawn(…))`, importing \
         guardChild from {GUARD}:\n  {}",
        bad.join("\n  ")
    );
    assert_eq!(unguarded_spawns("const p = spawn(APP, []);"), vec![1]);
    assert!(unguarded_spawns("const p = guardChild(spawn(APP, []));").is_empty());
    assert!(unguarded_spawns("const r = spawnSync(\"go\", []);").is_empty());
}

fn node_on_path() -> bool {
    Command::new("node")
        .arg("--version")
        .output()
        .map(|o| o.status.success())
        .unwrap_or(false)
}

/// Whether `pid` is a live process.
fn alive(pid: u32) -> bool {
    Command::new("kill")
        .args(["-0", &pid.to_string()])
        .output()
        .map(|o| o.status.success())
        .unwrap_or(false)
}

fn wait_gone(pid: u32) -> bool {
    for _ in 0..50 {
        if !alive(pid) {
            return true;
        }
        std::thread::sleep(std::time::Duration::from_millis(100));
    }
    false
}

/// Run a Node program that spawns `sleep 300` (guarded or not) and ends by
/// `how`; returns the child's PID.
fn run_parent(dir: &Path, guarded: bool, how: &str) -> u32 {
    let guard = repo().join(GUARD).canonicalize().unwrap();
    let spawn = if guarded {
        "guardChild(spawn(\"sleep\", [\"300\"]))"
    } else {
        "spawn(\"sleep\", [\"300\"])"
    };
    let end = match how {
        "throw" => "setTimeout(() => { throw new Error(\"boom\"); }, 100);",
        "reject" => "setTimeout(() => Promise.reject(new Error(\"rejected\")), 100);",
        "exit" => "setTimeout(() => process.exit(3), 100);",
        "wait" => "setTimeout(() => {}, 60000);",
        other => panic!("{other}"),
    };
    let prog = dir.join(format!("parent-{how}-{guarded}.mjs"));
    std::fs::write(
        &prog,
        format!(
            "import {{ spawn }} from \"node:child_process\";\n\
             import {{ guardChild }} from {:?};\n\
             const c = {spawn};\n\
             console.log(c.pid);\n{end}\n",
            guard.to_string_lossy()
        ),
    )
    .unwrap();
    let mut parent = Command::new("node")
        .arg(&prog)
        .stdout(std::process::Stdio::piped())
        .stderr(std::process::Stdio::null())
        .spawn()
        .expect("spawn node");
    let mut line = String::new();
    {
        use std::io::BufRead;
        let out = parent.stdout.take().unwrap();
        std::io::BufReader::new(out).read_line(&mut line).unwrap();
    }
    let child: u32 = line.trim().parse().expect("child pid");
    if how == "wait" {
        std::thread::sleep(std::time::Duration::from_millis(200));
        let _ = Command::new("kill")
            .args(["-TERM", &parent.id().to_string()])
            .status();
    }
    let _ = parent.wait();
    child
}

#[test]
fn the_guard_kills_the_child_on_every_exit_path() {
    if !required(Need::Node, node_on_path()) {
        return;
    }
    let dir = std::env::temp_dir().join(format!(
        "sky-child-guard-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(&dir).unwrap();

    // Without the guard a thrown error orphans the child: the leak is real,
    // and this test can see it.
    let orphan = run_parent(&dir, false, "throw");
    let leaked = alive(orphan);
    let _ = Command::new("kill")
        .args(["-KILL", &orphan.to_string()])
        .status();
    assert!(leaked, "an unguarded child should outlive a throw");

    for how in ["throw", "reject", "exit", "wait"] {
        let child = run_parent(&dir, true, how);
        let gone = wait_gone(child);
        if !gone {
            let _ = Command::new("kill")
                .args(["-KILL", &child.to_string()])
                .status();
        }
        assert!(gone, "a guarded child outlived its parent ({how})");
    }
    let _ = std::fs::remove_dir_all(&dir);
}

// ---------------------------------------------------------------------------
// A verifier that hits an error ENDS, and an e2e script bounds every verifier.
//
// Release run 36942837671 (head 7d6c883c): `gate-web` step 7 ran from 00:14
// until the job limit cancelled it at 01:07, against 6-9 min on green runs of
// the same code. `scripts/spa-hydration-verify.mjs` (the first stage of
// `spa-restore-e2e.sh`) launched Chromium inside a `try`, and its `catch` only
// set `process.exitCode = 1`; its `finally` killed the app server but neither
// closed the browser nor called `process.exit`. A launched browser keeps
// Node's event loop alive, so ONE transient error after the launch (a
// navigation or click over its 30 s budget on a loaded runner) left Node
// running for ever, and `spa-restore-e2e.sh` called it with no bound. Measured:
// a script of that shape that throws after `chromium.launch` is still alive
// 40 s later, until `with_timeout` kills it (exit 124).
// ---------------------------------------------------------------------------

/// The body of the LAST `finally { … }` block in `text` (brace-matched), or
/// `None` when there is no `finally`.
fn last_finally_block(text: &str) -> Option<&str> {
    let at = text.rfind("finally")?;
    let open = at + text[at..].find('{')?;
    let mut depth = 0usize;
    for (i, c) in text[open..].char_indices() {
        match c {
            '{' => depth += 1,
            '}' => {
                depth -= 1;
                if depth == 0 {
                    return Some(&text[open + 1..open + i]);
                }
            }
            _ => {}
        }
    }
    None
}

/// A Node script that launches a browser and tears down in a `finally` must,
/// in that `finally` or after it, either close the browser or end the process. `None`
/// when the script complies (or does not apply); `Some(reason)` otherwise.
fn browser_teardown_gap(text: &str) -> Option<&'static str> {
    if !text.contains(".launch(") {
        return None;
    }
    let block = last_finally_block(text)?;
    // `process.exit` in the block, or in the top-level code after it (which
    // runs whenever the `catch` swallowed the error).
    let from = text.rfind("finally").unwrap_or(0);
    let ends = text[from..].contains("process.exit(");
    let closes = block.contains("browser.close(") || block.contains("browser?.close(");
    if ends || closes {
        None
    } else {
        Some("neither its last `finally` nor the code after it closes the browser or calls `process.exit`")
    }
}

#[test]
fn a_browser_verifier_ends_on_its_error_path() {
    let scripts = files_with(&["mjs", "js"]);
    let mut bad = Vec::new();
    let mut seen = 0;
    for (rel, text) in &scripts {
        if !rel.starts_with("scripts/") {
            continue;
        }
        if text.contains(".launch(") && last_finally_block(text).is_some() {
            seen += 1;
        }
        if let Some(why) = browser_teardown_gap(text) {
            bad.push(format!("{rel}: {why}"));
        }
    }
    assert!(
        seen >= 20,
        "only {seen} browser verifiers with a `finally` under scripts/; the scan has gone blind"
    );
    assert!(
        bad.is_empty(),
        "a launched browser keeps Node alive, so a verifier whose error path does \
         not close it hangs for ever instead of failing. End the last `finally` \
         with `process.exit(process.exitCode ?? 1)` (or close the browser there):\n  {}",
        bad.join("\n  ")
    );
    // The scanner sees the shape that hung release run 36942837671 ...
    let hung = "const b = await chromium.launch();\ntry { await go(); } catch (e) { process.exitCode = 1; } finally {\n  proc.kill(\"SIGKILL\");\n}\n";
    assert!(browser_teardown_gap(hung).is_some());
    let after = format!("{hung}process.exit(process.exitCode ?? 1);\n");
    assert!(browser_teardown_gap(&after).is_none());
    // ... and passes both fixes.
    let exits = hung.replace(
        "proc.kill(\"SIGKILL\");",
        "proc.kill(\"SIGKILL\");\n  process.exit(process.exitCode ?? 1);",
    );
    assert!(browser_teardown_gap(&exits).is_none());
    let closes = hung.replace(
        "proc.kill(\"SIGKILL\");",
        "if (b) { await b.close(); }\n  try { await browser?.close(); } catch {}",
    );
    assert!(browser_teardown_gap(&closes).is_none());
}

/// Lines of an e2e shell script that run `node` with no `with_timeout` bound,
/// as 1-based line numbers. A continuation line (after a `\`) is not a command.
fn unbounded_node_calls(text: &str) -> Vec<usize> {
    let mut out = Vec::new();
    let mut continued = false;
    for (n, line) in text.lines().enumerate() {
        let code = shell_code(line);
        let was_continued = continued;
        continued = code.trim_end().ends_with('\\');
        if was_continued {
            continue;
        }
        let words: Vec<&str> = code.split_whitespace().collect();
        let Some(first) = words
            .iter()
            .position(|w| !matches!(*w, "if" | "!" | "then" | "do" | "&&" | "||"))
        else {
            continue;
        };
        if words[first] == "node" {
            out.push(n + 1);
        }
    }
    out
}

#[test]
fn every_e2e_script_bounds_its_node_verifiers() {
    let scripts = files_with(&["sh"]);
    let mut bad = Vec::new();
    let mut bounded = 0;
    for (rel, text) in &scripts {
        if !(rel.starts_with("scripts/") && rel.ends_with("-e2e.sh")) {
            continue;
        }
        bounded += text.matches("with_timeout").count();
        for line in unbounded_node_calls(text) {
            bad.push(format!("{rel}:{line}"));
        }
    }
    assert!(
        bounded >= 20,
        "only {bounded} with_timeout calls in scripts/*-e2e.sh; the scan has gone blind"
    );
    assert!(
        bad.is_empty(),
        "an e2e script runs a Node verifier with no bound, so a verifier that never \
         ends hangs the CI job until its own limit cancels it, with no log. Run it \
         as `with_timeout <secs> node …` (source scripts/lib/with-timeout.sh):\n  {}",
        bad.join("\n  ")
    );
    assert_eq!(
        unbounded_node_calls("node \"$ROOT/scripts/x.mjs\" \"$APP\"\n"),
        vec![1]
    );
    assert_eq!(
        unbounded_node_calls("if ! node x.mjs; then exit 1; fi\n"),
        vec![1]
    );
    assert!(unbounded_node_calls("with_timeout 300 node x.mjs \\\n  node-arg\n").is_empty());
    assert!(unbounded_node_calls("# node x.mjs\nrequire_tool node \"x\"\n").is_empty());
}

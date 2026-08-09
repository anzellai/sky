//! Running a gate body in a **child process**, with a budget the harness can
//! actually enforce.
//!
//! v2 §7.3 makes this a blocking design requirement, and the reasons are
//! specific rather than stylistic:
//!
//! 1. **"Kill the process group" is unimplementable from a thread.** A
//!    thread's spawned children are not reachable as a group. Our gates spawn
//!    `go build`s, servers, PTYs and browsers; a timeout that leaks a process
//!    holding a port poisons every later gate. The BlueDB precedent this phase
//!    was originally meant to *adopt* detaches a thread and kills nothing —
//!    `grep -E "kill|process_group|setsid|pgid"` over all 8 of its files
//!    returns zero hits.
//! 2. **An orphaned worker can write a result after its gate was recorded
//!    FAIL**, corrupting a *later* gate's verdict. That is worse than the leak,
//!    because it produces a wrong *green* attributed to the wrong gate.
//!
//! Both are closed here: the body is `fork`+`setpgid`'d into its own process
//! group (`process_group(0)`), the runner waits with a deadline, expiry
//! escalates `killpg(SIGTERM)` → `killpg(SIGKILL)`, and the result is read from
//! a **generation-stamped** file whose generation must match the gate currently
//! being awaited. Timeouts live in the harness and never in GNU `timeout`,
//! which is absent on every macOS runner — the exact hole that leaves
//! `conformance.sh` running unbounded there today.

use std::io::ErrorKind;
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::time::{Duration, Instant};

#[cfg(unix)]
use std::os::unix::process::CommandExt;

/// What the child wrote, verbatim, before any interpretation.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ChildResult {
    pub generation: u64,
    pub passed: bool,
    pub assertions: u64,
    pub detail: String,
}

/// The outcome of supervising one gate body.
#[derive(Debug)]
pub struct ChildRun {
    /// `None` when the child never produced a usable result — timed out,
    /// crashed, wrote nothing, or wrote a result stamped with the wrong
    /// generation.
    pub result: Option<ChildResult>,
    pub timed_out: bool,
    pub exit_code: Option<i32>,
    pub elapsed: Duration,
    /// Set when the process could not be spawned at all. A run that could not
    /// fork did not test anything (v2 §7.6), so this is a FAIL, never a retry.
    pub spawn_error: Option<String>,
    /// Set when a result file existed but carried a generation other than the
    /// one being awaited. The result is **discarded**, never used.
    pub generation_mismatch: bool,
    pub stdout: String,
    pub stderr: String,
}

impl ChildRun {
    fn spawn_failed(msg: String, elapsed: Duration) -> ChildRun {
        ChildRun {
            result: None,
            timed_out: false,
            exit_code: None,
            elapsed,
            spawn_error: Some(msg),
            generation_mismatch: false,
            stdout: String::new(),
            stderr: String::new(),
        }
    }
}

/// How often the supervisor polls the child. Small enough that a 3 s budget is
/// enforced to within ~2 %, large enough not to spin a core.
const POLL: Duration = Duration::from_millis(25);
/// Grace between `SIGTERM` and `SIGKILL` to the process group.
const TERM_GRACE: Duration = Duration::from_millis(2000);

/// Supervise one gate body in its own process group.
///
/// `exe` is this xtask binary; the child re-enters through
/// `harness --exec-gate`. Re-executing ourselves (rather than threading a
/// closure) is what makes the body a real process with a real process group.
pub fn run_gate_in_child(
    exe: &Path,
    repo_root: &Path,
    gate: &str,
    generation: u64,
    budget: Duration,
    result_path: &Path,
) -> ChildRun {
    let started = Instant::now();

    // A stale file from an earlier generation must never be mistaken for this
    // run's result. The path is already generation-unique; removing it as well
    // means "no file" is unambiguous.
    let _ = std::fs::remove_file(result_path);

    let mut cmd = Command::new(exe);
    cmd.arg("harness")
        .arg("--exec-gate")
        .arg(gate)
        .arg("--generation")
        .arg(generation.to_string())
        .arg("--result")
        .arg(result_path)
        .current_dir(repo_root)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());

    // The whole point. `process_group(0)` makes the child a process-group
    // leader (its pgid == its pid), so every process it spawns — and every
    // process *those* spawn — inherits the group and is reachable by one
    // `killpg`. Without it, `kill(child)` leaves the `go build`, the server and
    // the browser running.
    #[cfg(unix)]
    cmd.process_group(0);

    let child = match cmd.spawn() {
        Ok(c) => c,
        Err(e) => {
            // `EAGAIN` here means the per-uid process table is exhausted. v2
            // §7.6 removed the `RLIMIT_NPROC` pre-flight as TOCTOU and replaced
            // it with exactly this: the spawn failure IS the signal, and it is
            // a FAIL.
            let kind = if e.kind() == ErrorKind::WouldBlock {
                "EAGAIN (process table exhausted)"
            } else {
                "spawn failed"
            };
            return ChildRun::spawn_failed(format!("{kind}: {e}"), started.elapsed());
        }
    };

    supervise(child, budget, started, result_path, generation)
}

fn supervise(
    mut child: Child,
    budget: Duration,
    started: Instant,
    result_path: &Path,
    generation: u64,
) -> ChildRun {
    let pid = child.id();
    let deadline = started + budget;
    let mut timed_out = false;

    let status = loop {
        match child.try_wait() {
            Ok(Some(status)) => break Some(status),
            Ok(None) => {}
            Err(e) => {
                return ChildRun::spawn_failed(format!("wait failed: {e}"), started.elapsed());
            }
        }
        if Instant::now() >= deadline {
            timed_out = true;
            kill_group(pid);
            // Reap so the child never becomes a zombie holding a slot in the
            // process table the next gate needs.
            break child.wait().ok();
        }
        std::thread::sleep(POLL);
    };

    let elapsed = started.elapsed();
    let (stdout, stderr) = drain(&mut child);

    // Read the result BEFORE deciding anything, so a generation mismatch is
    // observable and reportable rather than silently absent.
    let (result, generation_mismatch) = read_result(result_path, generation);

    ChildRun {
        result,
        timed_out,
        exit_code: status.and_then(|s| s.code()),
        elapsed,
        spawn_error: None,
        generation_mismatch,
        stdout,
        stderr,
    }
}

fn drain(child: &mut Child) -> (String, String) {
    use std::io::Read;
    let mut out = String::new();
    let mut err = String::new();
    if let Some(mut s) = child.stdout.take() {
        let _ = s.read_to_string(&mut out);
    }
    if let Some(mut s) = child.stderr.take() {
        let _ = s.read_to_string(&mut err);
    }
    (out, err)
}

/// Escalating kill of the child's entire process group.
///
/// `killpg` — not `kill`. The negative-pid form targets the group, so the gate
/// body, its `go build`, its server and its PTY all die together. This is the
/// line the precedent does not have.
#[cfg(unix)]
fn kill_group(pid: u32) {
    use nix::sys::signal::{killpg, Signal};
    use nix::unistd::Pid;

    let pgid = Pid::from_raw(pid as i32);
    // Polite first: a server gets the chance to release its port.
    //
    // NEGATIVE CONTROL, run 2026-08-09: replacing this `killpg` with a plain
    // `kill` of the direct child leaves the gate body's `sleep 600` grandchild
    // ALIVE (observed: body gone, pid 34429 still `sleep`). That orphan is the
    // leaked-server-holding-a-port class. The group form is load-bearing.
    let _ = killpg(pgid, Signal::SIGTERM);

    let grace_until = Instant::now() + TERM_GRACE;
    while Instant::now() < grace_until {
        // Signal 0: no signal delivered, error-checking only. ESRCH == "no such
        // process group" == everything in it is gone.
        if killpg(pgid, None::<Signal>).is_err() {
            return;
        }
        std::thread::sleep(POLL);
    }
    let _ = killpg(pgid, Signal::SIGKILL);
}

#[cfg(not(unix))]
fn kill_group(_pid: u32) {
    // Windows has no process groups in the POSIX sense. Gates that spawn
    // process trees declare `UNIX` applicability for exactly this reason, so a
    // Windows runner renders them NOT APPLICABLE rather than running them with
    // an unenforceable budget.
}

/// Read a generation-stamped result.
///
/// Returns `(result, generation_mismatch)`. A result whose generation does not
/// match the gate currently being awaited is **discarded** — this is the
/// mechanism that stops a late writer from a previous run corrupting a later
/// gate's verdict.
fn read_result(path: &Path, expected_generation: u64) -> (Option<ChildResult>, bool) {
    let Ok(raw) = std::fs::read_to_string(path) else {
        return (None, false);
    };
    let Ok(v) = serde_json::from_str::<serde_json::Value>(&raw) else {
        return (None, false);
    };
    let generation = v.get("generation").and_then(|g| g.as_u64()).unwrap_or(0);
    if generation != expected_generation {
        return (None, true);
    }
    let passed = v.get("passed").and_then(|p| p.as_bool()).unwrap_or(false);
    let assertions = v.get("assertions").and_then(|a| a.as_u64()).unwrap_or(0);
    let detail = v
        .get("detail")
        .and_then(|d| d.as_str())
        .unwrap_or("")
        .to_string();
    (
        Some(ChildResult {
            generation,
            passed,
            assertions,
            detail,
        }),
        false,
    )
}

/// Where a gate's generation-stamped result file lives.
pub fn result_path(dir: &Path, gate: &str, generation: u64) -> PathBuf {
    dir.join(format!("{gate}.gen{generation}.json"))
}

/// Write a result from inside the child. Written to a temp file and renamed so
/// the parent can never observe a half-written record.
pub fn write_result(path: &Path, r: &ChildResult) -> std::io::Result<()> {
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent)?;
    }
    let body = serde_json::json!({
        "generation": r.generation,
        "passed": r.passed,
        "assertions": r.assertions,
        "detail": r.detail,
    });
    let tmp = path.with_extension("json.tmp");
    std::fs::write(&tmp, serde_json::to_vec_pretty(&body)?)?;
    std::fs::rename(&tmp, path)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn tmpdir(tag: &str) -> PathBuf {
        let d = std::env::temp_dir().join(format!(
            "sky-harness-test-{tag}-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        std::fs::create_dir_all(&d).unwrap();
        d
    }

    #[test]
    fn a_result_stamped_with_another_generation_is_discarded() {
        // The corrupting-a-later-gate scenario, made concrete: a straggler from
        // generation 7 must not be read as generation 8's answer.
        let dir = tmpdir("gen");
        let p = dir.join("r.json");
        write_result(
            &p,
            &ChildResult {
                generation: 7,
                passed: true,
                assertions: 99,
                detail: "stale writer".into(),
            },
        )
        .unwrap();

        let (result, mismatch) = read_result(&p, 8);
        assert!(mismatch, "the generation mismatch must be reported");
        assert!(
            result.is_none(),
            "a stale result must be DISCARDED, not returned"
        );

        // Sanity: the same file IS readable at its own generation, so the test
        // above is not passing because the file was unparseable.
        let (ok, mismatch) = read_result(&p, 7);
        assert!(!mismatch);
        assert_eq!(ok.unwrap().assertions, 99);

        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn a_missing_result_is_not_a_pass() {
        let dir = tmpdir("missing");
        let (result, mismatch) = read_result(&dir.join("nope.json"), 1);
        assert!(result.is_none());
        assert!(!mismatch);
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn a_truncated_result_is_not_a_pass() {
        let dir = tmpdir("trunc");
        let p = dir.join("r.json");
        std::fs::write(&p, "{\"generation\": 1, \"passed\": tr").unwrap();
        let (result, _) = read_result(&p, 1);
        assert!(result.is_none(), "unparseable JSON must not yield a result");
        let _ = std::fs::remove_dir_all(&dir);
    }
}

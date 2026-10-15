//! A process a test starts, and everything that process starts, ends with the
//! test, on every exit path.
//!
//! WHY THIS EXISTS. The flow tests start real servers (`sky-out/app`, a
//! split's `backend/sky-out/app`, `sky run`). They used to hold a bare
//! `std::process::Child` and call `child.kill()` on the happy path. A
//! `Child` that is dropped does NOT kill its process, so any panic between
//! the spawn and that `kill()` (a readiness timeout under load, an `unwrap`
//! on a refused connection, an `assert!`) left the server running under
//! pid 1, holding its port and every descriptor it inherited. Three times
//! such an orphan held the shared heavy-build lock, whose descriptor every
//! child of the locked command inherits. And `child.kill()` signals one pid:
//! a process the server started itself (an engine, a `sky run` child) was
//! never reached even on the happy path.
//!
//! `ChildGroup` closes each path:
//!
//! * The child runs in a process group of its own (`process_group(0)`), so
//!   one `killpg` reaches it and every descendant that stays in its group.
//! * `Drop` ends the group: SIGTERM (a Sky app stops the engines it started
//!   on SIGTERM), a bounded wait, then SIGKILL to the whole group, then a
//!   reap. `Drop` runs on a normal return AND while a panic unwinds.
//! * A reaper process joins the child's group and blocks reading a pipe whose
//!   only writer is this test process. When the test process ends by ANY
//!   means, including SIGKILL (a test harness timeout), the kernel closes
//!   the pipe, the read returns, and the reaper kills the group. The reaper
//!   is in the child's group, not the test's, so a signal to the test's own
//!   group (nextest, `with_timeout`) does not take it down first.
//!
//! `tests/child_group_flow.rs` proves each path: a grandchild of a guarded
//! child is gone after a normal drop, after a panic, and after the test
//! process itself is killed with SIGKILL.

#![allow(dead_code)]

use std::os::unix::process::CommandExt;
use std::process::{Child, Command, ExitStatus, Stdio};
use std::time::{Duration, Instant};

use nix::sys::signal::{killpg, Signal};
use nix::unistd::Pid;

/// How long `Drop` waits after SIGTERM before it sends SIGKILL to the group.
const TERM_GRACE: Duration = Duration::from_secs(5);

pub struct ChildGroup {
    child: Child,
    reaper: Option<Child>,
    done: bool,
}

impl ChildGroup {
    /// Start `cmd` in a process group of its own, with a reaper that ends
    /// the group if this test process dies first.
    pub fn spawn(cmd: &mut Command) -> std::io::Result<ChildGroup> {
        cmd.process_group(0);
        let child = cmd.spawn()?;
        let pgid = child.id() as i32;
        // `read` returns at EOF: when the last writer of its stdin (this
        // process) is gone. Then the whole group goes, the reaper with it.
        let reaper = Command::new("/bin/sh")
            .args([
                "-c",
                "read _ignored; kill -9 -\"$1\" 2>/dev/null",
                "sky-test-reaper",
                &pgid.to_string(),
            ])
            .stdin(Stdio::piped())
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .process_group(pgid)
            .spawn();
        let reaper = match reaper {
            Ok(r) => Some(r),
            Err(e) => {
                // The child already exited, so its group no longer exists.
                // Nothing of it can outlive the test; report anything else.
                let mut g = ChildGroup {
                    child,
                    reaper: None,
                    done: false,
                };
                if g.child.try_wait()?.is_none() {
                    g.kill();
                    return Err(e);
                }
                return Ok(g);
            }
        };
        Ok(ChildGroup {
            child,
            reaper,
            done: false,
        })
    }

    /// The child's pid, which is also its process group id.
    pub fn id(&self) -> u32 {
        self.child.id()
    }

    /// The child, for its pipes and `try_wait`.
    pub fn child(&mut self) -> &mut Child {
        &mut self.child
    }

    /// Send `sig` to the whole group.
    pub fn signal(&self, sig: Signal) {
        let _ = killpg(Pid::from_raw(self.child.id() as i32), sig);
    }

    /// SIGTERM the group, wait up to `grace` for the child, then SIGKILL the
    /// group and reap. Returns the child's exit status. Idempotent.
    pub fn stop_within(&mut self, grace: Duration) -> Option<ExitStatus> {
        if self.done {
            return self.child.try_wait().ok().flatten();
        }
        self.signal(Signal::SIGTERM);
        let deadline = Instant::now() + grace;
        while Instant::now() < deadline {
            if let Ok(Some(_)) = self.child.try_wait() {
                break;
            }
            std::thread::sleep(Duration::from_millis(25));
        }
        self.finish()
    }

    /// SIGTERM, a grace of five seconds, then SIGKILL to the group.
    pub fn stop(&mut self) -> Option<ExitStatus> {
        self.stop_within(TERM_GRACE)
    }

    /// SIGKILL the group at once and reap.
    pub fn kill(&mut self) -> Option<ExitStatus> {
        if self.done {
            return self.child.try_wait().ok().flatten();
        }
        self.finish()
    }

    /// Wait for the child to exit on its own (it was asked to stop by other
    /// means), then end what is left of its group.
    pub fn wait(&mut self) -> Option<ExitStatus> {
        let status = self.child.wait().ok();
        self.finish();
        status
    }

    fn finish(&mut self) -> Option<ExitStatus> {
        // The group: the child (if it still runs), every descendant still
        // in the group, and the reaper.
        self.signal(Signal::SIGKILL);
        let status = self.child.wait().ok();
        if let Some(mut r) = self.reaper.take() {
            let _ = r.kill();
            let _ = r.wait();
        }
        self.done = true;
        status
    }
}

impl Drop for ChildGroup {
    fn drop(&mut self) {
        if !self.done {
            self.stop();
        }
    }
}

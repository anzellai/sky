//! Say why a process ended when a signal ended it.
//!
//! A memory watchdog (or any other process) that sends SIGTERM / SIGKILL to a
//! build leaves no output behind: the user saw exit 143 and nothing else, and
//! `sky build --target web:app` printed "frontend failed to build" with no
//! reason. Two parts close that:
//!
//! * [`describe`] turns the exit status of a child `sky` waits on (a child
//!   `sky` build leg, `go build`) into one line that names the signal and says
//!   where it came from, with the knobs that make a build use less memory.
//! * [`install_sigterm_notice`] gives `sky` itself a SIGTERM handler that writes
//!   one line to stderr and then ends the process by the same signal, so the
//!   exit status stays 143.
//!
//! The handler lives here, not in the `sky` binary crate, because that crate
//! forbids `unsafe` and installing a handler needs it (as `memory.rs` needs it
//! for `wait4`).

use std::process::ExitStatus;

/// The two knobs that make a build use less memory: one `go build` compile at
/// a time (`crate::go_jobs::ENV_JOBS`), and the Sky.Spa backend and frontend
/// legs one after the other (`SKY_BUILD_SERIAL`, read by `sky`'s `leg_plan`).
pub const LOW_MEMORY_HINT: &str =
    "Free memory, or set SKY_GO_BUILD_JOBS=1 and SKY_BUILD_SERIAL=1, then try again.";

/// The signal that ended a process, when a signal ended it.
pub fn signal_of(status: &ExitStatus) -> Option<i32> {
    #[cfg(unix)]
    {
        use std::os::unix::process::ExitStatusExt;
        status.signal()
    }
    #[cfg(not(unix))]
    {
        let _ = status;
        None
    }
}

/// The conventional name of a signal number (`15` → `SIGTERM`).
pub fn signal_name(sig: i32) -> String {
    let name = match sig {
        1 => "SIGHUP",
        2 => "SIGINT",
        3 => "SIGQUIT",
        4 => "SIGILL",
        6 => "SIGABRT",
        8 => "SIGFPE",
        9 => "SIGKILL",
        11 => "SIGSEGV",
        13 => "SIGPIPE",
        14 => "SIGALRM",
        15 => "SIGTERM",
        _ => return format!("signal {sig}"),
    };
    name.to_string()
}

/// One line for `what` (e.g. "frontend build") ended by signal `sig`.
///
/// SIGTERM, SIGKILL and SIGHUP come from another process: sky sends none of
/// them to a build child (a `go build` past its deadline is reported as a
/// timeout, not here). SIGINT is Ctrl-C. Any other signal is the process's own
/// crash, so the line names the signal only.
pub fn describe_signal(what: &str, sig: i32) -> String {
    let name = signal_name(sig);
    match sig {
        9 | 15 | 1 => format!(
            "{what} stopped by {name} from another process (a memory watchdog?). {LOW_MEMORY_HINT}"
        ),
        2 => format!("{what} stopped by {name} (Ctrl-C)."),
        _ => format!("{what} ended by {name}."),
    }
}

/// [`describe_signal`] for an exit status, or `None` when no signal ended the
/// process (a normal exit, success or failure, says why on its own).
pub fn describe(what: &str, status: &ExitStatus) -> Option<String> {
    signal_of(status).map(|sig| describe_signal(what, sig))
}

/// The line `sky` writes when it receives SIGTERM itself.
pub const SIGTERM_NOTICE: &str = "sky: stopped by SIGTERM from another process\n";

/// Install the SIGTERM handler for the `sky` process: write
/// [`SIGTERM_NOTICE`] to stderr, then end the process by SIGTERM (default
/// action), so a parent still sees the status 143.
///
/// Only the calling process is affected. A handler is reset to the default
/// action on `exec`, so every child (`go build`, a `sky run` app, a supervised
/// PostgreSQL) keeps its own SIGTERM behaviour. A process that ignores SIGTERM
/// (started with it set to `SIG_IGN`) keeps ignoring it.
pub fn install_sigterm_notice() {
    #[cfg(unix)]
    unix::install();
}

#[cfg(unix)]
mod unix {
    extern "C" fn on_sigterm(_sig: libc::c_int) {
        let msg = super::SIGTERM_NOTICE.as_bytes();
        // SAFETY: only async-signal-safe calls (POSIX.1-2017 §2.4.3): `write`,
        // `signal` and `raise`. `msg` is a 'static byte string. SA_RESETHAND
        // already restored the default action on entry; `signal` sets it again
        // in case the platform ignored the flag. The re-raised SIGTERM is
        // blocked while this handler runs, and is delivered with the default
        // action as soon as it returns, which ends the process with status 143.
        unsafe {
            libc::write(2, msg.as_ptr().cast(), msg.len());
            libc::signal(libc::SIGTERM, libc::SIG_DFL);
            libc::raise(libc::SIGTERM);
        }
    }

    pub(super) fn install() {
        // SAFETY: `sigaction` with a zeroed struct (a valid value of the plain
        // C type), an empty mask and a handler that only makes async-signal-
        // safe calls. The old action is read first so an inherited SIG_IGN is
        // kept (`nohup`-style callers).
        unsafe {
            let mut old: libc::sigaction = std::mem::zeroed();
            if libc::sigaction(libc::SIGTERM, std::ptr::null(), &mut old) != 0 {
                return;
            }
            if old.sa_sigaction == libc::SIG_IGN {
                return;
            }
            let mut act: libc::sigaction = std::mem::zeroed();
            act.sa_sigaction = on_sigterm as extern "C" fn(libc::c_int) as libc::sighandler_t;
            libc::sigemptyset(&mut act.sa_mask);
            act.sa_flags = libc::SA_RESETHAND;
            libc::sigaction(libc::SIGTERM, &act, std::ptr::null_mut());
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_sigterm_names_the_signal_its_source_and_the_knobs() {
        let line = describe_signal("frontend build", 15);
        assert_eq!(
            line,
            "frontend build stopped by SIGTERM from another process (a memory watchdog?). \
             Free memory, or set SKY_GO_BUILD_JOBS=1 and SKY_BUILD_SERIAL=1, then try again."
        );
        assert!(describe_signal("go build", 9)
            .starts_with("go build stopped by SIGKILL from another process"));
        assert_eq!(
            describe_signal("backend build", 2),
            "backend build stopped by SIGINT (Ctrl-C)."
        );
        assert_eq!(
            describe_signal("go build", 11),
            "go build ended by SIGSEGV."
        );
        assert_eq!(
            describe_signal("go build", 40),
            "go build ended by signal 40."
        );
    }

    #[cfg(unix)]
    #[test]
    fn describe_reads_the_signal_from_an_exit_status() {
        use std::os::unix::process::ExitStatusExt;
        // A raw wait status of N (N < 128) is "terminated by signal N".
        let killed = ExitStatus::from_raw(15);
        assert_eq!(
            describe("frontend build", &killed).as_deref(),
            Some(describe_signal("frontend build", 15).as_str())
        );
        // exit(1) is raw 1 << 8: a normal failure, no signal line.
        assert_eq!(
            describe("frontend build", &ExitStatus::from_raw(1 << 8)),
            None
        );
        assert_eq!(describe("frontend build", &ExitStatus::from_raw(0)), None);
    }

    /// The knobs the hint names are the ones the code reads.
    #[test]
    fn the_hint_names_real_knobs() {
        assert!(LOW_MEMORY_HINT.contains(&format!("{}=1", crate::go_jobs::ENV_JOBS)));
        assert!(LOW_MEMORY_HINT.contains("SKY_BUILD_SERIAL=1"));
    }
}

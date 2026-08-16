//! The one place a live test is allowed to not run.
//!
//! # The defect this exists to remove
//!
//! `db_shared/live_tests.rs` holds fourteen tests covering the shared-cluster
//! security boundary itself — "app A's credentials cannot reach app B's
//! database", "a cluster that does not ask for a password is refused", "a
//! `pg_hba.conf` that does not parse is refused rather than reloaded". Each
//! began `let Ok(bins) = discover_pg_bins() else { eprintln!(…); return; }`.
//! Run with and without a PostgreSQL on the machine, they reported:
//!
//! ```text
//! ### no postgres discoverable (the conditions in CI's `test-rest`) ###
//! test result: ok. 14 passed; 0 failed; 0 ignored; 0 measured; 167 filtered out; finished in 0.02s
//!
//! ### SKY_POSTGRES_BIN=/opt/homebrew/bin ###
//! test result: ok. 14 passed; 0 failed; 0 ignored; 0 measured; 167 filtered out; finished in 18.29s
//! ```
//!
//! Byte-identical verdicts. `0 ignored`. The only difference between "the
//! security boundary was proven" and "nothing happened" is the wall clock, and
//! nothing reads the wall clock. `test-rest` is the only job that runs
//! `cargo test --workspace`, and it installs no PostgreSQL — so every one of
//! those fourteen had been reporting `ok` in CI without asserting anything.
//!
//! # The mechanism
//!
//! **A live test that did not run has not passed.** The default is
//! [`Mode::Require`]: an unmet need is a PANIC naming the need, not a silent
//! `return`. Skipping is possible, and requires saying so out loud:
//!
//! ```text
//! SKY_LIVE_TESTS=skip cargo test --workspace
//! ```
//!
//! That inverts the failure direction. Before, a machine or a CI job without
//! the environment produced a green run with no coverage in it; now it
//! produces a red run that names what is missing and how to install it, and
//! the person who genuinely cannot install it types one environment variable
//! whose name appears in the failure message. CI provisions the environment
//! instead (see `.github/workflows/rust-ci.yml`, job `test-rest`), so in CI
//! the skip path is unreachable — which is the property that was missing.
//!
//! # What this does NOT do
//!
//! It does not make a skip show up as `ignored` in libtest's verdict line.
//! libtest has no runtime "skipped" outcome — `#[ignore]` is decided at compile
//! time and cannot consult the environment — so a `SKY_LIVE_TESTS=skip` run
//! still prints `N passed`. What it does instead is remove the case where
//! nobody CHOSE that: an unrun live test is now either a panic or something a
//! human asked for by name. The marker line also goes to the process's own
//! stderr rather than through `eprintln!`, because libtest captures `eprintln!`
//! and prints the capture only for tests that FAILED — the reason a skipped
//! live test previously said nothing at all.

#![allow(dead_code)] // included by several test crates; each uses a subset

use std::io::Write;

/// An environment a live test needs, and cannot fake.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Need {
    /// A real PostgreSQL installation — `initdb`, `pg_ctl`, `postgres`.
    Postgres,
    /// A Go toolchain, for the tests that run a real `go build` on emitted Go.
    Go,
    /// The `sqlite3` command-line client, used to read a database back
    /// independently of Sky's own driver.
    Sqlite3,
    /// Reachable network. **The one need that is never required** — see
    /// [`required`].
    Network,
}

impl Need {
    /// How CI provides it. Quoted verbatim in the panic, because "postgres is
    /// missing" is only useful next to "here is the line that installs it".
    pub fn how_to_get_it(self) -> &'static str {
        match self {
            Need::Postgres => {
                "install PostgreSQL and point SKY_POSTGRES_BIN at its `bin` directory \
                 (`brew install postgresql@16`, or `apt-get install postgresql-16` whose \
                 binaries land in /usr/lib/postgresql/16/bin and are NOT on PATH), or run \
                 `sky db provision --embed`"
            }
            Need::Go => "install a Go toolchain (the compiler shells out to a real `go build`)",
            Need::Sqlite3 => "install the `sqlite3` command-line client",
            Need::Network => "unreachable network",
        }
    }

    fn label(self) -> &'static str {
        match self {
            Need::Postgres => "PostgreSQL",
            Need::Go => "a Go toolchain",
            Need::Sqlite3 => "the sqlite3 client",
            Need::Network => "network",
        }
    }
}

/// What an unmet need means.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Mode {
    /// The default. An unmet need fails the test.
    Require,
    /// Explicitly asked for, by `SKY_LIVE_TESTS=skip`.
    Skip,
}

/// Read the mode. An unrecognised value is an error rather than a silent
/// fallback to either side: `SKY_LIVE_TESTS=1` meaning "require" to its author
/// and "skip" to this function is exactly how a gate ends up not running.
pub fn mode() -> Mode {
    mode_from(std::env::var("SKY_LIVE_TESTS").ok().as_deref())
}

/// The parse, separated from the environment read so it can be tested without
/// mutating a process-wide variable that every live test in the binary is
/// concurrently reading.
pub fn mode_from(raw: Option<&str>) -> Mode {
    match raw {
        None | Some("") | Some("require") => Mode::Require,
        Some("skip") => Mode::Skip,
        Some(other) => panic!(
            "SKY_LIVE_TESTS={other:?} is not a mode. Use `require` (the default — an \
             unmet environment fails the live tests) or `skip` (an unmet environment \
             lets them return without asserting)."
        ),
    }
}

/// Gate a live test on an environment it needs. Returns `true` when the test
/// must proceed.
///
/// `available` is the CALLER's own probe, not one this module performs. The
/// gate must consult exactly the thing the test consults — a gate with its own
/// idea of "is PostgreSQL here" can disagree with the code under it, and then
/// it is asserting something about its own probe rather than about the test.
///
/// [`Need::Network`] is the single need that is never required, and it is worth
/// saying why in code rather than leaving it to be inferred: an upstream that
/// is down is not a defect in this repository, and a gate that goes red for it
/// is a gate that gets turned off. Which tests may use it is not left to
/// judgement either — `rust/crates/xtask/tests/live_tests_are_not_silently_skipped.rs`
/// holds the list, so a third one is a reviewable diff rather than a habit.
#[track_caller]
pub fn required(need: Need, available: bool) -> bool {
    required_in(mode(), need, available)
}

/// [`required`], with the caller's own account of WHY the need is unmet.
///
/// # Why a reason is worth a second entry point
///
/// "Not available" was only ever produced by one probe — "the binaries are not
/// discoverable" — so the label alone said everything there was to say.
///
/// It does not any more, and the difference is expensive. On a host whose 32
/// SysV shared-memory ids are all held (macOS ships `kern.sysv.shmmni = 32`),
/// PostgreSQL is fully installed and discoverable and **cannot start**:
///
/// ```text
/// FATAL:  could not create shared memory segment: No space left on device
/// DETAIL:  Failed system call was shmget(key=496045126, size=56, 03600).
/// ```
///
/// Thirteen shared-cluster SECURITY tests failed that way in one
/// `cargo test --workspace`, and a red run naming thirteen security tests is
/// indistinguishable from a real regression — it took reading the gate's source
/// to establish it was the host. `why` is what puts the machine's answer in the
/// failure instead.
#[track_caller]
pub fn required_because(need: Need, available: bool, why: &str) -> bool {
    required_in_because(mode(), need, available, why)
}

/// [`required`] with the mode passed in, so the behaviour can be asserted
/// without depending on the environment the assertion is running under — a
/// test of a skip mechanism that itself skips proves nothing.
#[track_caller]
pub fn required_in(mode: Mode, need: Need, available: bool) -> bool {
    required_in_because(mode, need, available, "")
}

/// [`required_in`] carrying the caller's reason. See [`required_because`].
#[track_caller]
pub fn required_in_because(mode: Mode, need: Need, available: bool, why: &str) -> bool {
    if available {
        return true;
    }
    let at = std::panic::Location::caller();
    let because = if why.trim().is_empty() {
        String::new()
    } else {
        format!("\nThe machine's answer:\n{}\n", why.trim())
    };
    if need != Need::Network && mode == Mode::Require {
        panic!(
            "live gate: this test needs {} and it is not available.\n\
             {}\
             \n\
             A live test that did not run has not passed — that is why this is a \
             failure and not a `return`. Fourteen shared-cluster security tests \
             reported `ok. 14 passed` in every CI job that ran them, having asserted \
             nothing.\n\
             \n\
             To run it: {}.\n\
             To skip it deliberately: SKY_LIVE_TESTS=skip cargo test …\n\
             \n\
             at {}:{}",
            need.label(),
            because,
            need.how_to_get_it(),
            at.file(),
            at.line(),
        );
    }
    // Deliberately NOT `eprintln!`: libtest captures it and prints the capture
    // only for a test that FAILED, so a skipped live test says nothing at all.
    let mut e = std::io::stderr();
    let _ = writeln!(
        e,
        "SKIPPED (live): {} is not available{} — {}:{}",
        need.label(),
        if why.trim().is_empty() {
            String::new()
        } else {
            format!(" ({})", first_line(why))
        },
        at.file(),
        at.line()
    );
    false
}

/// Does `err` describe the ENVIRONMENT being unable to run a postmaster, rather
/// than this code being wrong?
///
/// # Why this is here and not at a call site
///
/// Availability used to be modelled as DISCOVERY — `discover_pg_bins()`
/// succeeding — and a probe that models the thing can only ever see the
/// unavailability the model contains. On a host whose 32 SysV shared-memory ids
/// (`kern.sysv.shmmni`) were all held by sibling processes, PostgreSQL was
/// installed, discoverable, and could not start:
///
/// ```text
/// FATAL:  could not create shared memory segment: No space left on device
/// DETAIL:  Failed system call was shmget(key=496045126, size=56, 03600).
/// ```
///
/// Discovery succeeded, the gate was bypassed, and thirteen shared-cluster
/// SECURITY tests panicked inside `initdb`. `SKY_LIVE_TESTS=skip` could not
/// reach them, because the code path that reads the mode was never entered —
/// and thirteen red security tests are indistinguishable from a real
/// regression.
///
/// The patterns are PostgreSQL's own diagnostics for resource exhaustion AT
/// STARTUP, not a generic "no space" match: a disk that is genuinely full says
/// `could not write` from a different call, and that is a real failure this must
/// not launder. Anything unrecognised returns `None` — the default stays "this
/// is a defect".
pub fn postgres_cannot_start(err: &str) -> Option<String> {
    const EXHAUSTED: &[&str] = &[
        // SysV shm ids exhausted — `shmget` ENOSPC. macOS ships shmmni = 32.
        "could not create shared memory segment",
        // The semaphore equivalent, `semget` ENOSPC, same class.
        "could not create semaphores",
        // POSIX shm / mmap variant on hosts using dynamic_shared_memory_type.
        "could not resize shared memory segment",
    ];
    // The MATCHED line leads. A caller's one-line marker takes the head of the
    // reason, and `initdb`'s first line is "initdb failed:" — true and useless.
    for p in EXHAUSTED {
        if let Some(hit) = err.lines().map(str::trim).find(|l| l.contains(p)) {
            return Some(format!("{hit}\n\nin full:\n{}", err.trim()));
        }
    }
    None
}

/// Classify `err` and, when it is the environment, route it through the gate.
///
/// Returns `true` when the caller should stop: under the default `require` mode
/// this has already panicked, so a `true` return is only ever reached under
/// `SKY_LIVE_TESTS=skip`. `false` means the failure is NOT an unavailable
/// environment and the caller must treat it as the defect it is.
#[track_caller]
pub fn gate_if_postgres_cannot_start(err: &str) -> bool {
    match postgres_cannot_start(err) {
        Some(why) => {
            required_because(Need::Postgres, false, &why);
            true
        }
        None => false,
    }
}

/// The first non-empty line of `s`, trimmed to something a one-line marker can
/// carry. A multi-line `initdb` transcript is the reason, not the headline.
fn first_line(s: &str) -> String {
    let l = s.lines().map(str::trim).find(|l| !l.is_empty()).unwrap_or("");
    if l.chars().count() > 140 {
        format!("{}…", l.chars().take(140).collect::<String>())
    } else {
        l.to_string()
    }
}


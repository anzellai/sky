//! `support/child_group.rs`: no process a test starts survives the test.
//!
//! A guarded child starts a grandchild (`sleep`) and the test then leaves by
//! each exit path a flow test can take: a normal drop, a panic, and the test
//! process itself killed with SIGKILL (a harness timeout). After each, neither
//! the child nor the grandchild may still run.
//!
//! Before `ChildGroup`, the flow tests held a bare `Child` and called
//! `kill()` on the happy path: a panic left the server running under pid 1,
//! and even the happy path never reached a process the server started.

#![cfg(unix)]

#[path = "support/child_group.rs"]
mod child_group;

use child_group::ChildGroup;
use std::io::{BufRead, BufReader};
use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

use nix::sys::signal::{kill, Signal};
use nix::unistd::Pid;

const HELPER_ENV: &str = "SKY_CHILD_GROUP_HELPER";

fn alive(pid: i32) -> bool {
    kill(Pid::from_raw(pid), None).is_ok()
}

fn gone_within(pid: i32, budget: Duration) -> bool {
    let deadline = Instant::now() + budget;
    while Instant::now() < deadline {
        if !alive(pid) {
            return true;
        }
        std::thread::sleep(Duration::from_millis(25));
    }
    !alive(pid)
}

/// A child that starts a grandchild and reports the grandchild's pid.
fn family() -> (ChildGroup, i32, i32) {
    let mut g = ChildGroup::spawn(
        Command::new("/bin/sh")
            .args(["-c", "sleep 600 & echo $!; wait"])
            .stdout(Stdio::piped())
            .stderr(Stdio::null())
            .stdin(Stdio::null()),
    )
    .expect("spawn the family");
    let mut line = String::new();
    BufReader::new(g.child().stdout.take().unwrap())
        .read_line(&mut line)
        .unwrap();
    let grandchild: i32 = line.trim().parse().expect("grandchild pid");
    let child = g.id() as i32;
    assert!(alive(child) && alive(grandchild));
    (g, child, grandchild)
}

#[test]
fn a_dropped_group_takes_its_grandchildren() {
    let (g, child, grandchild) = family();
    drop(g);
    assert!(!alive(child), "the child {child} survived the drop");
    assert!(
        gone_within(grandchild, Duration::from_secs(5)),
        "the grandchild {grandchild} survived the drop"
    );
}

#[test]
fn a_panic_takes_the_group() {
    let pids = std::sync::Mutex::new((0, 0));
    let r = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
        let (_g, child, grandchild) = family();
        *pids.lock().unwrap() = (child, grandchild);
        panic!("a test failed while its server ran");
    }));
    assert!(r.is_err());
    let (child, grandchild) = *pids.lock().unwrap();
    assert!(
        gone_within(child, Duration::from_secs(5)),
        "the child {child} survived the panic"
    );
    assert!(
        gone_within(grandchild, Duration::from_secs(5)),
        "the grandchild {grandchild} survived the panic"
    );
}

/// Run by `a_killed_test_process_takes_the_group` in a process of its own:
/// start a family, report it, and wait to be killed.
#[test]
#[ignore = "helper: run by a_killed_test_process_takes_the_group"]
fn helper_holds_a_family_until_killed() {
    if std::env::var_os(HELPER_ENV).is_none() {
        return;
    }
    let (_g, child, grandchild) = family();
    println!("FAMILY {child} {grandchild}");
    std::thread::sleep(Duration::from_secs(600));
}

#[test]
fn a_killed_test_process_takes_the_group() {
    let mut helper = Command::new(std::env::current_exe().unwrap())
        .args([
            "--exact",
            "helper_holds_a_family_until_killed",
            "--ignored",
            "--nocapture",
            "--test-threads=1",
        ])
        .env(HELPER_ENV, "1")
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .stdin(Stdio::null())
        .spawn()
        .expect("start the helper");
    let mut reader = BufReader::new(helper.stdout.take().unwrap());
    let mut line = String::new();
    let (child, grandchild) = loop {
        line.clear();
        if reader.read_line(&mut line).unwrap() == 0 {
            let _ = helper.kill();
            let _ = helper.wait();
            panic!("the helper ended without reporting its family");
        }
        // libtest writes `test <name> ... ` on the same line first.
        if let Some(rest) = line.trim().split("FAMILY ").nth(1) {
            let mut it = rest.split(' ').map(|p| p.parse::<i32>().unwrap());
            break (it.next().unwrap(), it.next().unwrap());
        }
    };
    // SIGKILL: no destructor, no unwinding, no handler runs in the helper.
    kill(Pid::from_raw(helper.id() as i32), Signal::SIGKILL).unwrap();
    let _ = helper.wait();
    let child_gone = gone_within(child, Duration::from_secs(10));
    let grandchild_gone = gone_within(grandchild, Duration::from_secs(10));
    // Ours to end if the guard failed: the pids the helper reported.
    for pid in [child, grandchild] {
        if alive(pid) {
            let _ = kill(Pid::from_raw(pid), Signal::SIGKILL);
        }
    }
    assert!(
        child_gone,
        "the child {child} survived its test process's SIGKILL"
    );
    assert!(
        grandchild_gone,
        "the grandchild {grandchild} survived its test process's SIGKILL"
    );
}

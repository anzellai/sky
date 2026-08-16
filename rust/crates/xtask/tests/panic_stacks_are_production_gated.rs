//! A Go stack trace must not reach a production log by accident.
//!
//! `logPanicFrame` (Sky.Http.Server) was hardened to print a compact
//! `method path (kind)` line in production and keep the full frame in
//! `.skylog/panic.log`. Nothing else was. `runtime-go/rt/live.go` contained
//! ZERO occurrences of the production predicate, and its top-level handler
//! dumped `debugStack()` unconditionally. Same panic, `ENV=production`:
//!
//! ```text
//! Sky.Http.Server  "[sky.http] panic GET /checkout (*errors.errorString)"     53 bytes
//! Sky.Live         "[sky.live] panic handling GET /checkout: boom\ngoroutine 26 [running]:..."  1252 bytes
//! ```
//!
//! Sky.Live is the PINNED DEFAULT app shape (AGENTS.md), so the leak was the
//! common case, not the edge one — and a Go stack names internal paths, package
//! layout and frame addresses in whatever log aggregator the deploy ships to.
//!
//! The gate is structural rather than behavioural because the failure is one of
//! COVERAGE: the hardened path existed and worked, and eight other sites simply
//! did not call it. A behavioural test proves the site it exercises; only an
//! enumeration proves there is no ninth.
//!
//! Rule: `debug.Stack()` may be called from exactly one place in the runtime —
//! `runtime-go/rt/panic_log.go`, which owns the dev/production policy. Every
//! other site routes through `LogRecoveredPanic` / `panicStackForLog` /
//! `capturePanicStack`. Comments may quote the call (this file's own docstring
//! does, and a fix is documented by naming what it removed).

use std::fs;
use std::path::{Path, PathBuf};

fn repo_root() -> PathBuf {
    let mut d = Path::new(env!("CARGO_MANIFEST_DIR")).to_path_buf();
    while !d.join("examples").is_dir() {
        d = d.parent().expect("repo root not found").to_path_buf();
    }
    d
}

/// The one file allowed to capture a stack. Path relative to repo root.
const STACK_OWNER: &str = "runtime-go/rt/panic_log.go";

fn walk_go(dir: &Path, out: &mut Vec<PathBuf>) {
    let Ok(entries) = fs::read_dir(dir) else { return };
    for e in entries.flatten() {
        let p = e.path();
        if p.is_dir() {
            walk_go(&p, out);
        } else if p.extension().and_then(|e| e.to_str()) == Some("go")
            && !p
                .file_name()
                .and_then(|n| n.to_str())
                .is_some_and(|n| n.ends_with("_test.go"))
        {
            out.push(p);
        }
    }
}

/// A line that only talks about the call (comment) is exempt.
fn is_comment(line: &str) -> bool {
    let t = line.trim_start();
    t.starts_with("//") || t.starts_with('*') || t.starts_with("/*")
}

#[test]
fn stack_capture_lives_only_in_the_hardened_path() {
    let root = repo_root();
    let runtime = root.join("runtime-go");
    assert!(
        runtime.is_dir(),
        "runtime-go/ not found under {} — the gate cannot establish a verdict",
        root.display()
    );

    let owner = root.join(STACK_OWNER);
    assert!(
        owner.is_file(),
        "{STACK_OWNER} is missing: the production stack-trace policy has no owner, \
         so every debug.Stack() call in the runtime is ungoverned"
    );

    let mut files = Vec::new();
    walk_go(&runtime, &mut files);
    assert!(
        files.len() > 20,
        "expected to scan the Go runtime, found only {} files",
        files.len()
    );

    let mut offenders: Vec<String> = Vec::new();
    for f in &files {
        if f == &owner {
            continue;
        }
        let Ok(src) = fs::read_to_string(f) else { continue };
        for (i, line) in src.lines().enumerate() {
            if is_comment(line) {
                continue;
            }
            if line.contains("debug.Stack()") || line.contains("debugStack()") {
                let rel = f.strip_prefix(&root).unwrap_or(f);
                offenders.push(format!("{}:{}: {}", rel.display(), i + 1, line.trim()));
            }
        }
    }

    assert!(
        offenders.is_empty(),
        "these sites capture a Go stack outside the production-gated path in \
         {STACK_OWNER}, so they leak internal frames into a production log:\n  {}\n\n\
         Route them through rt.LogRecoveredPanic (stderr + .skylog/panic.log) or \
         panicStackForLog (structured-log field).",
        offenders.join("\n  ")
    );
}

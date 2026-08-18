//! A script may not consume `sky-out/sky` without establishing that it is the
//! compiler built from the tree the script is about to measure.
//!
//! # The defect this exists to remove
//!
//! Fifteen scripts consumed the compiler at `sky-out/sky`. Not one checked that
//! it was current. The binary is installed there by exactly one line —
//! `scripts/build.sh:80` — so a workflow that builds with a bare
//! `cargo build --release -p sky` leaves `rust/target/release/sky` fresh and
//! `sky-out/sky` untouched, and every consumer then measures whatever
//! `scripts/build.sh` last produced.
//!
//! The loud direction cost a diagnosis on 2026-08-16: a sweep run after
//! `cargo build --release -p sky` reported every example failing and 22 of 22
//! conformance suites FAILED, on
//!
//! ```text
//! ./main.go:19:42: not enough arguments in call to rt.RegisterAdtTag
//!     have (string, number)
//!     want (string, string, int)
//! ```
//!
//! The tree was consistent; the two-argument call came from a `sky-out/sky`
//! built before that change. The QUIET direction is the one that ships: a stale
//! binary that happens to PASS certifies source it never compiled, on the
//! repository's most load-bearing verification, and nothing would catch it.
//! `scripts/build.sh:77` already carried a comment about an earlier incident
//! where the build "installed a pre-fix compiler" — closed by hand, not by a
//! gate.
//!
//! `rust/crates/xtask/src/config_matrix.rs` had the identical defect until it
//! was fixed: it measured whatever binary was on disk, so reverting a
//! `runtime-go/` fix WITHOUT rebuilding produced `config-matrix: OK` in 49 s.
//! This file is that fix generalised to the scripts, and modelled on
//! `scripts_bound_time_portably.rs`, which does the same job for a bare
//! `timeout`.
//!
//! # The rules
//!
//! 1. A script with a non-comment reference to `sky-out/sky` also references
//!    the freshness check — unless it is a declared PRODUCER, which installs
//!    the binary itself immediately before use.
//! 1b. Every declared PRODUCER still CONTAINS its install line. The exemption
//!    was prose-only: renaming every `install_binary` call in
//!    `scripts/build.sh` left all tests here green, so a producer that
//!    stopped installing kept its consume-without-check exemption forever —
//!    the exclusion outliving its excuse.
//! 2. A script that CALLS `require_fresh_compiler` / `sky_compiler_freshness`
//!    also sources the library. An unsourced function is `command not found`.
//! 3. The shell library's source roots and
//!    `config_matrix.rs::MEASURED_SOURCE_ROOTS` name the same trees. Two
//!    definitions of "what the compiler is built from" is how one of them ends
//!    up missing `sky-bundled/`, which is exactly what had happened.
//! 4. The check is falsifiable: against a synthetic tree it passes for a
//!    current binary, fails for one older than a source file, and refuses to
//!    answer when the source walk finds nothing.
//!
//! # What this does NOT catch
//!
//! * A `.github/workflows/*.yml` `run:` block that invokes `sky-out/sky`
//!   directly. Every current reference there is an `install_binary` followed by
//!   `--version` in the same step — producers — and the sweeps those jobs run
//!   are scripts, which now carry the check themselves. A novel workflow step
//!   that compiled something directly would slip through this scan.
//! * A binary from a DIFFERENT tree that happens to be newer. mtime answers "is
//!   this older than the sources", not "was it built from them" — the same
//!   instrument `config_matrix.rs` uses, single-sourced rather than doubled.
//! * A consumer that reaches the compiler under some name this scan does not
//!   know. The scan keys on the literal `sky-out/sky`, which is the repo's own
//!   convention and the path every current consumer spells out.

use std::path::{Path, PathBuf};
use std::process::Command;

/// The one shell file that defines what "fresh" means.
const LIB: &str = "scripts/lib/fresh-compiler.sh";
/// Its Node face — it runs [`LIB`] rather than reimplementing it.
const LIB_MJS: &str = "scripts/lib/fresh-compiler.mjs";
/// The Rust gate that carries the same source-root list.
const CONFIG_MATRIX: &str = "rust/crates/xtask/src/config_matrix.rs";
/// This file. Every rule below quotes the shapes it forbids, so a scan that
/// included it would report itself.
const SELF: &str = "rust/crates/xtask/tests/gates_measure_a_fresh_compiler.rs";

/// Scripts that INSTALL the compiler at `sky-out/sky` and then use it, so the
/// binary is current by construction. Each entry is (path, why).
///
/// This list is short on purpose. "It builds it first" is a claim a reader can
/// check in one line of the named file; anything vaguer belongs in the check,
/// not in an exemption.
const PRODUCERS: &[(&str, &str)] = &[
    (
        "scripts/build.sh",
        "installs it (install_binary … \"$ROOT/sky-out/sky\") before every use below",
    ),
    (
        "scripts/preflight-tag.sh",
        "installs it from cargo_bin_path immediately before the release checks",
    ),
];

/// Frozen records of a past run, excluded for the same reason
/// `scripts_bound_time_portably.rs` excludes them: rewriting them would falsify
/// the record. `legacy-*` are retired compilers kept as oracles.
const FROZEN_PREFIXES: &[&str] = &[
    "docs/history/",
    "docs/perf/runs/",
    "legacy-haskell-compiler/",
    "legacy-sky-compiler/",
    "legacy-ts-compiler/",
];

fn repo() -> PathBuf {
    PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../.."))
}

fn is_frozen(rel: &str) -> bool {
    FROZEN_PREFIXES.iter().any(|p| rel.starts_with(p))
}

/// Every script in the tree that could consume a compiler, as
/// (repo-relative path, contents). Shell, Node and Lua — the three languages
/// the current consumers are written in.
fn scripts() -> Vec<(String, String)> {
    fn walk(dir: &Path, out: &mut Vec<PathBuf>) {
        let Ok(rd) = std::fs::read_dir(dir) else { return };
        for e in rd.flatten() {
            let p = e.path();
            let name = e.file_name().to_string_lossy().to_string();
            if p.is_dir() {
                let skip = matches!(
                    name.as_str(),
                    "target" | "node_modules" | "sky-out" | "local-target" | "_verify" | "_site"
                ) || name.starts_with('.');
                if !skip {
                    walk(&p, out);
                }
                continue;
            }
            if matches!(
                p.extension().and_then(|x| x.to_str()),
                Some("sh") | Some("mjs") | Some("js") | Some("lua")
            ) {
                out.push(p);
            }
        }
    }
    let root = repo();
    let mut files = Vec::new();
    walk(&root, &mut files);
    files.sort();
    files
        .into_iter()
        .filter_map(|p| {
            let rel = p.strip_prefix(&root).ok()?.to_string_lossy().replace('\\', "/");
            let text = std::fs::read_to_string(&p).ok()?;
            Some((rel, text))
        })
        .collect()
}

/// True when `line` is a comment in shell (`#`), Lua (`--`) or JS (`//`).
///
/// A comment that MENTIONS the path is not a consumer, and several of the most
/// useful comments in this tree do exactly that: `scripts/lib/cargo-target.sh`
/// and `scripts/lsp-test-nvim.lua` both narrate stale-binary incidents by
/// naming `sky-out/sky`. Flagging those would push people to delete the record
/// of why the check exists.
fn is_comment(line: &str) -> bool {
    let t = line.trim_start();
    t.starts_with('#') || t.starts_with("--") || t.starts_with("//") || t.starts_with('*')
}

#[test]
fn every_consumer_of_the_installed_compiler_checks_that_it_is_fresh() {
    let mut offenders = Vec::new();
    for (rel, text) in scripts() {
        if rel == LIB || rel == LIB_MJS || is_frozen(&rel) {
            continue;
        }
        if PRODUCERS.iter().any(|(p, _)| *p == rel) {
            continue;
        }
        let consumes: Vec<String> = text
            .lines()
            .enumerate()
            .filter(|(_, l)| !is_comment(l) && l.contains("sky-out/sky"))
            .map(|(n, l)| format!("{rel}:{}: {}", n + 1, l.trim()))
            .collect();
        if consumes.is_empty() {
            continue;
        }
        let checks = text
            .lines()
            .any(|l| !is_comment(l) && l.contains("fresh-compiler"));
        if !checks {
            offenders.extend(consumes);
        }
    }
    assert!(
        offenders.is_empty(),
        "these scripts consume the installed compiler without establishing that it was \
         built from this tree. `cargo build --release -p sky` writes \
         rust/target/release/sky and does NOT install it to sky-out/sky, so the binary \
         they read can predate every change they claim to verify — which is how a sweep \
         once reported 22 of 22 conformance suites FAILED on a consistent tree, and how \
         a green run can certify source that was never compiled.\n\
         Source {LIB} and call `require_fresh_compiler \"$SKY\" \"$ROOT\"` (Node: import \
         {LIB_MJS}), or add the script to PRODUCERS in {SELF} if it installs the binary \
         itself.\n\n  {}",
        offenders.join("\n  ")
    );
}

/// Rule 1b: the PRODUCERS exemption must keep earning itself. Each entry's
/// "why" says "it installs the binary in one checkable line" — so check that
/// line. A producer whose install step disappears (renamed helper, refactor,
/// deletion) must FAIL here, naming the file, and either regain its install
/// step or move to the consumer list and take the freshness check.
///
/// Falsifying mutation (verified red): rename `install_binary` in
/// scripts/build.sh — before this test existed, all 10 tests in this file
/// stayed green under that mutation.
#[test]
fn every_declared_producer_still_installs_the_compiler() {
    let root = repo();
    let mut offenders = Vec::new();
    for (rel, why) in PRODUCERS {
        let text = match std::fs::read_to_string(root.join(rel)) {
            Ok(t) => t,
            Err(e) => {
                offenders.push(format!(
                    "{rel}: unreadable ({e}) — a producer that no longer exists must lose its exemption, not keep it"
                ));
                continue;
            }
        };
        // Word-match the helper, not substring-match it: the first draft of
        // this check used `contains("install_binary")`, and the very mutation
        // it exists to catch — renaming the call to `install_binary_gone` —
        // still contained that substring and stayed green.
        let installs = text.lines().any(|l| {
            !is_comment(l)
                && l.contains("sky-out/sky")
                && l.split_whitespace().any(|w| w == "install_binary")
        });
        if !installs {
            offenders.push(format!(
                "{rel}: declared a producer (\"{why}\") but no non-comment line installs \
                 sky-out/sky via install_binary"
            ));
        }
    }
    assert!(
        offenders.is_empty(),
        "these PRODUCERS no longer install the compiler they are exempted for. The \
         exemption exists because \"it builds it first\" is checkable in one line of the \
         named file — that line is gone, so the file now consumes sky-out/sky with \
         neither an install step nor a freshness check. Restore the `install_binary … \
         sky-out/sky` step, or remove the file from PRODUCERS in {SELF} so rule 1 \
         requires the freshness check.\n\n  {}",
        offenders.join("\n  ")
    );
}

#[test]
fn every_script_that_calls_the_check_sources_the_library() {
    let mut offenders = Vec::new();
    for (rel, text) in scripts() {
        if rel == LIB || rel == LIB_MJS || rel == SELF {
            continue;
        }
        let calls = text.lines().any(|l| {
            !is_comment(l)
                && (l.contains("require_fresh_compiler ")
                    || l.contains("sky_compiler_freshness ")
                    || l.contains("requireFreshCompiler("))
        });
        if !calls {
            continue;
        }
        let sources = text
            .lines()
            .any(|l| !is_comment(l) && (l.contains("fresh-compiler.sh") || l.contains("fresh-compiler.mjs")));
        if !sources {
            offenders.push(rel);
        }
    }
    assert!(
        offenders.is_empty(),
        "these scripts call the freshness check without sourcing/importing it. An \
         unsourced function is `command not found` — under `set -u` with no `-e` that is \
         a non-zero status a caller can swallow, which is the shape of every defect this \
         file is about:\n  {}",
        offenders.join("\n  ")
    );
}

/// Every shell script must PARSE. A check written into a file bash cannot read
/// is a check that never runs.
///
/// This is not a hypothetical tightening. `scripts/regenerate-console.sh` — the
/// generator that writes a CHECKED-IN file — did not parse at all, and had not
/// for as long as its own comments have been there. Its `awk` program is a
/// single-quoted shell word, and three apostrophes inside it (`binary's`,
/// `console's`, `app's`) closed that quote early, after which bash read the
/// remaining awk source as shell and reported
/// `syntax error near unexpected token '('`. Wiring a freshness check into that
/// file would have been wiring it into a script that dies before reaching the
/// `awk` — the same nothing-ran shape, one layer down.
///
/// Nothing in the tree checked this, which is why it survived: `bash -n` costs
/// milliseconds and is the whole gate.
///
/// It parses with **`/bin/bash`** — bash 3.2 on macOS — not with whatever
/// `bash` PATH resolves to. Stock macOS ships 3.2 and nothing else, and
/// `#!/usr/bin/env bash` resolves to it the moment a nix shell supplying bash 5
/// is not on PATH. That is not a hypothetical on this repository: the entire
/// `scripts/lib/with-timeout.sh` mechanism exists because a nix shell went away
/// and took `timeout` with it. Running the check under the newest bash
/// available would have passed `scripts/skylive-observe-remote.sh`, which bash
/// 3.2 could not parse at all.
#[test]
fn every_shell_script_parses() {
    let root = repo();
    let mut offenders = Vec::new();
    for (rel, _) in scripts() {
        if !rel.ends_with(".sh") || is_frozen(&rel) {
            continue;
        }
        let out = Command::new("/bin/bash")
            .arg("-n")
            .arg(root.join(&rel))
            .output()
            .expect("run bash -n");
        if !out.status.success() {
            offenders.push(format!(
                "{rel}: {}",
                String::from_utf8_lossy(&out.stderr).trim().replace('\n', "\n    ")
            ));
        }
    }
    assert!(
        offenders.is_empty(),
        "these scripts do not parse. bash reads a script incrementally, so a parse error \
         partway down is invisible until execution reaches it — and then the script dies \
         having done part of its job. A gate written below such a line never runs:\n  {}",
        offenders.join("\n  ")
    );
}

/// The shell library and `config_matrix.rs` must name the same trees.
///
/// They did not. `MEASURED_SOURCE_ROOTS` carried `rust/crates`, `runtime-go`
/// and `sky-stdlib`; `rust/crates/ffi/build.rs` also stages `templates/`,
/// `sky-bundled/` and `tools/sky-ffi-inspect/` into the embed, so an edit to
/// any of those three changed the binary and was invisible to the Rust gate's
/// freshness check. One list is a definition; two lists are a race.
#[test]
fn the_shell_library_and_config_matrix_measure_the_same_trees() {
    let root = repo();

    let sh = std::fs::read_to_string(root.join(LIB)).expect("read the shell library");
    let block = sh
        .split("_SKY_COMPILER_INPUT_ROOTS='")
        .nth(1)
        .and_then(|s| s.split('\'').next())
        .expect("the shell library must declare _SKY_COMPILER_INPUT_ROOTS='<root>:<min>…'");
    let mut shell_roots: Vec<String> = block
        .lines()
        .filter_map(|l| l.split(':').next())
        .filter(|s| !s.trim().is_empty())
        .map(|s| s.trim().to_string())
        .collect();

    let rs = std::fs::read_to_string(root.join(CONFIG_MATRIX)).expect("read config_matrix.rs");
    let block = rs
        .split("const MEASURED_SOURCE_ROOTS")
        .nth(1)
        .and_then(|s| s.split("];").next())
        .expect("config_matrix.rs must declare MEASURED_SOURCE_ROOTS");
    let mut rust_roots: Vec<String> = block
        .lines()
        .filter_map(|l| {
            let l = l.trim();
            let rest = l.strip_prefix("(\"")?;
            Some(rest.split('"').next()?.to_string())
        })
        .collect();

    shell_roots.sort();
    rust_roots.sort();
    assert!(
        !shell_roots.is_empty() && !rust_roots.is_empty(),
        "parsed no roots — shell {shell_roots:?}, rust {rust_roots:?}. A parse that finds \
         nothing would make any two lists agree."
    );
    assert_eq!(
        shell_roots, rust_roots,
        "{LIB} and {CONFIG_MATRIX} disagree about what the compiler is built from. \
         `rust/crates/ffi/build.rs` is the authority: it stages sky-stdlib/, runtime-go/, \
         tools/sky-ffi-inspect/, templates/ and sky-bundled/ into the embed. Update both."
    );
}

/// Every declared source root must actually contribute files.
///
/// A root that is named but resolves to nothing is worse than a root that is
/// missing: the list LOOKS complete, and the tree it was supposed to watch is
/// unwatched. `config_matrix.rs` guards the aggregate (`seen < 100`), which one
/// large root satisfies on its own — `rust/crates` alone contributes 148 — so
/// `templates` or `tools/sky-ffi-inspect` could contribute zero and the
/// aggregate would never notice. This checks each root separately.
#[test]
fn every_declared_source_root_contributes_files() {
    let root = repo();
    let sh = std::fs::read_to_string(root.join(LIB)).expect("read the shell library");
    let block = sh
        .split("_SKY_COMPILER_INPUT_ROOTS='")
        .nth(1)
        .and_then(|s| s.split('\'').next())
        .expect("the shell library must declare _SKY_COMPILER_INPUT_ROOTS");

    let mut empty = Vec::new();
    for line in block.lines() {
        let (rel, min) = match line.split_once(':') {
            Some((r, m)) if !r.trim().is_empty() => (r.trim(), m.trim()),
            _ => continue,
        };
        let dir = root.join(rel);
        assert!(dir.is_dir(), "declared source root '{rel}' does not exist at {}", dir.display());

        // Ask the library itself, so this counts exactly what the check counts.
        let out = Command::new("/bin/bash")
            .arg("-c")
            .arg(format!(
                "source {lib} && _sky_compiler_inputs_in_root {root} {rel} | wc -l",
                lib = root.join(LIB).display(),
                root = root.display(),
            ))
            .output()
            .expect("count the inputs under one root");
        let n: usize = String::from_utf8_lossy(&out.stdout).trim().parse().unwrap_or(0);
        let min: usize = min.parse().unwrap_or(0);
        if n < min {
            empty.push(format!("{rel}: {n} file(s), floor is {min}"));
        }
    }
    assert!(
        empty.is_empty(),
        "these declared source roots contribute fewer files than their floor. A root that \
         contributes nothing is a tree nobody is watching, and every binary looks fresh \
         against it:\n  {}",
        empty.join("\n  ")
    );
}

// ─── The check is falsifiable ────────────────────────────────────────────
//
// Everything above is a text scan, and a text scan proves only that a call is
// written. These run the library against a synthetic tree — one whose files
// this test creates, so it never touches the real sources a sibling worktree
// may be building from.

/// Build a synthetic repo whose shape satisfies the library's per-root minimums.
/// Returns the root.
fn synthetic_tree(tag: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "sky-fresh-compiler-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map(|d| d.as_nanos())
            .unwrap_or(0)
    ));
    let _ = std::fs::remove_dir_all(&dir);

    let write = |rel: &str, body: &str| {
        let p = dir.join(rel);
        std::fs::create_dir_all(p.parent().unwrap()).expect("mkdir -p");
        std::fs::write(&p, body).expect("write fixture file");
    };

    // The per-root floors in the library are 100/50/50/1/5/1.
    for i in 0..110 {
        write(&format!("rust/crates/ty/src/f{i}.rs"), "fn f() {}\n");
    }
    write("rust/Cargo.toml", "[workspace]\n");
    for i in 0..60 {
        write(&format!("sky-stdlib/Std/M{i}.sky", ), "module M exposing (..)\n");
    }
    write("runtime-go/go.mod", "module sky-app\n");
    for i in 0..60 {
        write(&format!("runtime-go/rt/f{i}.go"), "package rt\n");
    }
    write("runtime-go/cmd/sky-hub/main.go", "package main\n");
    write("templates/CLAUDE.md", "# template\n");
    for i in 0..6 {
        write(&format!("sky-bundled/console/src/M{i}.sky"), "module M exposing (..)\n");
    }
    write("tools/sky-ffi-inspect/main.go", "package main\n");

    // Age every source deliberately. Leaving them at "now" makes the ordering
    // under test depend on how many microseconds the walk above took and on the
    // filesystem's timestamp granularity — a test that is green by luck is the
    // thing this file exists to refuse.
    fn age_all(dir: &Path, t: std::time::SystemTime) {
        let Ok(rd) = std::fs::read_dir(dir) else { return };
        for e in rd.flatten() {
            let p = e.path();
            if p.is_dir() {
                age_all(&p, t);
            } else if let Ok(f) = std::fs::File::options().write(true).open(&p) {
                let _ = f.set_modified(t);
            }
        }
    }
    age_all(&dir, std::time::SystemTime::now() - std::time::Duration::from_secs(600));

    // The library resolves the repo root from its own path when the caller does
    // not pass one; these tests always pass one, so a copy is not needed.
    dir
}

/// Run the library directly against a binary + tree. Returns (status, stderr).
fn run_check(bin: &Path, tree: &Path) -> (i32, String) {
    let out = Command::new("/bin/bash")
        .arg(repo().join(LIB))
        .arg(bin)
        .arg(tree)
        .output()
        .expect("run the freshness library");
    (
        out.status.code().unwrap_or(-1),
        String::from_utf8_lossy(&out.stderr).into_owned(),
    )
}

/// Create a file and give it an mtime `secs` in the past, so the ordering under
/// test does not depend on filesystem timestamp granularity.
fn touch_at(p: &Path, secs_ago: u64) {
    std::fs::create_dir_all(p.parent().unwrap()).ok();
    if !p.exists() {
        std::fs::write(p, "#!/bin/sh\nexit 0\n").expect("write");
    }
    let t = std::time::SystemTime::now() - std::time::Duration::from_secs(secs_ago);
    let f = std::fs::File::options().write(true).open(p).expect("open to set mtime");
    f.set_modified(t).expect("set mtime");
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = std::fs::set_permissions(p, std::fs::Permissions::from_mode(0o755));
    }
}

#[test]
fn a_binary_newer_than_every_source_passes() {
    let tree = synthetic_tree("green");
    let bin = tree.join("sky-out/sky");
    touch_at(&bin, 0);

    let (status, stderr) = run_check(&bin, &tree);
    assert_eq!(status, 0, "a current binary must pass. stderr:\n{stderr}");
    let _ = std::fs::remove_dir_all(&tree);
}

#[test]
fn a_source_edit_with_no_rebuild_fails_and_names_the_fix() {
    let tree = synthetic_tree("stale");
    let bin = tree.join("sky-out/sky");
    touch_at(&bin, 60);

    // Green first, so the red below is attributable to the edit and not to the
    // fixture. A red that was already red proves nothing.
    let (green, _) = run_check(&bin, &tree);
    assert_eq!(green, 0, "the fixture must start green");

    // THE MUTATION: a source file changes, nobody rebuilds.
    touch_at(&tree.join("runtime-go/rt/f7.go"), 0);

    let (status, stderr) = run_check(&bin, &tree);
    assert_eq!(status, 1, "a stale binary must fail. stderr:\n{stderr}");
    assert!(
        stderr.contains("runtime-go/rt/f7.go"),
        "the failure must NAME the file that moved, so the reader does not have to \
         bisect for it. stderr:\n{stderr}"
    );
    assert!(
        stderr.contains("./scripts/build.sh"),
        "the failure must name the command that fixes it — AGENTS.md: a gate whose \
         prerequisite is missing fails NAMING what to install. stderr:\n{stderr}"
    );

    // And green again once the binary is rebuilt.
    touch_at(&bin, 0);
    let (after, stderr) = run_check(&bin, &tree);
    assert_eq!(after, 0, "a rebuilt binary must pass again. stderr:\n{stderr}");
    let _ = std::fs::remove_dir_all(&tree);
}

/// A consumer under `set -euo pipefail` must still SEE the message.
///
/// Found while proving this change: `scripts/build-docs-site.sh` sets `-e`, and
/// `require_fresh_compiler` called `sky_compiler_freshness` bare. A shell
/// function that RETURNS non-zero in command position under `set -e` kills the
/// script on the spot — so the consumer exited 1 with **completely empty
/// output**, a correct verdict delivered as an unexplained failure. A reader
/// hitting that has no reason to suspect the compiler and every reason to
/// suspect the change they just made.
///
/// The fix is `|| rc=$?` at both call sites. This test is the falsifier: revert
/// either to a bare call and it goes red.
#[test]
fn the_failure_is_visible_to_a_consumer_running_under_set_e() {
    let tree = synthetic_tree("set-e");
    let bin = tree.join("sky-out/sky");
    touch_at(&bin, 300);
    touch_at(&tree.join("sky-stdlib/Std/M3.sky"), 0);

    // A consumer shaped exactly like scripts/build-docs-site.sh.
    let consumer = tree.join("consumer.sh");
    std::fs::write(
        &consumer,
        format!(
            "#!/usr/bin/env bash\n\
             set -euo pipefail\n\
             source {lib}\n\
             require_fresh_compiler {bin} {tree}\n\
             echo 'REACHED THE BODY'\n",
            lib = repo().join(LIB).display(),
            bin = bin.display(),
            tree = tree.display(),
        ),
    )
    .expect("write consumer");

    let out = Command::new("/bin/bash").arg(&consumer).output().expect("run consumer");
    let stderr = String::from_utf8_lossy(&out.stderr).into_owned();
    let stdout = String::from_utf8_lossy(&out.stdout).into_owned();

    assert_eq!(out.status.code(), Some(1), "the consumer must fail");
    assert!(
        !stdout.contains("REACHED THE BODY"),
        "the consumer must not proceed past the check. stdout:\n{stdout}"
    );
    assert!(
        stderr.contains("is older than the source"),
        "a `set -e` consumer must still be TOLD why it failed. An exit status with no \
         message is how a stale compiler gets mistaken for a broken change. stderr was:\n\
         {stderr:?}"
    );
    assert!(stderr.contains("./scripts/build.sh"), "stderr:\n{stderr}");
    let _ = std::fs::remove_dir_all(&tree);
}

#[test]
fn an_absent_binary_fails_and_names_the_fix() {
    let tree = synthetic_tree("absent");
    let (status, stderr) = run_check(&tree.join("sky-out/sky"), &tree);
    assert_eq!(status, 1, "an absent compiler must fail. stderr:\n{stderr}");
    assert!(stderr.contains("./scripts/build.sh"), "stderr:\n{stderr}");
    let _ = std::fs::remove_dir_all(&tree);
}

#[test]
fn a_walk_that_finds_nothing_refuses_to_answer() {
    let tree = synthetic_tree("vacuous");
    let bin = tree.join("sky-out/sky");
    touch_at(&bin, 0);
    assert_eq!(run_check(&bin, &tree).0, 0, "the fixture must start green");

    // THE MUTATION: the sources go away. Without the per-root floor this walk
    // would find nothing newer than the binary and report PASS — every binary
    // is fresh against an empty tree. That is the exact vacuity this whole
    // change exists to refuse, so it must be an ERROR (2), not a pass.
    std::fs::remove_dir_all(tree.join("runtime-go/rt")).expect("rm the sources");

    let (status, stderr) = run_check(&bin, &tree);
    assert_eq!(
        status, 2,
        "an unmeasurable tree must refuse to answer, not pass. stderr:\n{stderr}"
    );
    assert!(
        stderr.contains("runtime-go"),
        "the refusal must name the root it could not measure. stderr:\n{stderr}"
    );
    let _ = std::fs::remove_dir_all(&tree);
}

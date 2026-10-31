//! No CI workflow and no enumerating gate touches a CI-excluded example.
//!
//! USER RULE (anzellai/sky#299, set by the user on 2026-10-27):
//! `examples/76-ai-shell-command` is EXCLUDED from CI in general. Its `train`
//! fine-tunes a model and its `eval` drives a local model engine, which takes
//! far too long for CI, so no CI job may build, train, evaluate or run it.
//!
//! The list of excluded examples is `src/ci_excluded.rs`, included here by
//! path. This test fails when:
//!
//!   * a workflow under `.github/workflows/` names an excluded example;
//!   * a gate or script that enumerates `examples/` does not skip it by name
//!     (the known enumerators are listed below, and a scan finds new ones: a
//!     Rust file or a script that walks `examples/` and is not in a list fails
//!     until it skips the example, or is listed with a reviewed reason);
//!   * the real `scripts/example-sweep.sh --list` would build it;
//!   * the real `scripts/gates-for-change.sh` plans a gate for a change to it
//!     (only the incremental falsifier step, which every plan holds and which
//!     never reads the example, may appear).

use std::collections::BTreeSet;
use std::path::{Path, PathBuf};
use std::process::Command;

#[path = "../src/ci_excluded.rs"]
#[allow(dead_code)]
mod ci_excluded;

fn repo() -> PathBuf {
    PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../.."))
        .canonicalize()
        .expect("repo root resolves")
}

fn read(rel: &str) -> String {
    std::fs::read_to_string(repo().join(rel)).unwrap_or_else(|e| panic!("read {rel}: {e}"))
}

/// The lines of a source that are code, not comments (`//`, `#`).
fn code_lines(text: &str) -> Vec<&str> {
    text.lines()
        .filter(|l| {
            let t = l.trim_start();
            !(t.starts_with("//")
                || t.starts_with('#')
                || t.starts_with("/*")
                || t.starts_with('*'))
        })
        .collect()
}

/// Every Rust file that enumerates `examples/` as a corpus, and how it skips
/// the excluded examples. An `xtask` source calls `crate::ci_excluded`; a file
/// in another crate (or a test that includes the list by path) names the
/// example.
const RUST_ENUMERATORS: &[(&str, &str)] = &[
    (
        "rust/crates/xtask/src/main.rs",
        "ci_excluded::is_ci_excluded",
    ),
    (
        "rust/crates/xtask/src/resolve_gate.rs",
        "ci_excluded::is_ci_excluded",
    ),
    (
        "rust/crates/xtask/src/infer_gate.rs",
        "ci_excluded::is_ci_excluded",
    ),
    (
        "rust/crates/xtask/src/repro_gate.rs",
        "ci_excluded::is_ci_excluded",
    ),
    (
        "rust/crates/xtask/src/build_run_gate.rs",
        "ci_excluded::is_ci_excluded",
    ),
    (
        "rust/crates/xtask/src/fuzz_gate.rs",
        "ci_excluded::is_ci_excluded",
    ),
    (
        "rust/crates/xtask/src/s8_gate.rs",
        "ci_excluded::is_ci_excluded",
    ),
    (
        "rust/crates/xtask/src/fmt_gate.rs",
        "ci_excluded::is_ci_excluded",
    ),
    (
        "rust/crates/xtask/src/sky_fmt_check.rs",
        "ci_excluded::is_ci_excluded",
    ),
    (
        "rust/crates/xtask/src/shared_world_gate.rs",
        "ci_excluded::is_ci_excluded",
    ),
    (
        "rust/crates/xtask/src/denominators_gate.rs",
        "ci_excluded::is_ci_excluded",
    ),
    (
        "rust/crates/xtask/src/coverage_ledger.rs",
        "ci_excluded::is_ci_excluded",
    ),
    (
        "rust/crates/xtask/src/coerce_floor_gate.rs",
        "ci_excluded::is_ci_excluded",
    ),
    (
        "rust/crates/xtask/src/harness/bodies.rs",
        "ci_excluded::is_ci_excluded",
    ),
    (
        "rust/crates/xtask/src/harness/proof_inputs.rs",
        "ci_excluded::is_ci_excluded",
    ),
    (
        "rust/crates/xtask/tests/every_example_has_a_fresh_build_gate.rs",
        "ci_excluded::is_ci_excluded",
    ),
    (
        "rust/crates/xtask/tests/readme_samples_are_built.rs",
        "ci_excluded::is_ci_excluded",
    ),
    (
        "rust/crates/ty/src/infer_readback_tests.rs",
        "76-ai-shell-command",
    ),
    (
        "rust/crates/project/src/harvest_identity_tests.rs",
        "76-ai-shell-command",
    ),
];

/// Rust files that mention `examples/` in a walk-like way but do not use it as
/// a CI corpus, each with the reviewed reason.
const RUST_NOT_ENUMERATORS: &[(&str, &str)] = &[
    (
        "rust/crates/sky/src/main.rs",
        "`sky verify` with no target in a directory that holds `examples/` is a user \
         command, not a gate; `no_ci_runs_sky_verify_over_every_example` below proves \
         that no workflow or script calls it that way",
    ),
    (
        "rust/crates/xtask/src/ci_excluded.rs",
        "the exclusion list itself",
    ),
    (
        "rust/crates/xtask/tests/ci_excluded_examples.rs",
        "this test",
    ),
];

/// Scripts that enumerate `examples/` generically, all of which must name the
/// excluded example.
const SCRIPT_ENUMERATORS: &[&str] = &[
    "scripts/example-sweep.sh",
    "scripts/gates-for-change.sh",
    "scripts/build.sh",
    "scripts/example-e2e.sh",
];

#[test]
fn the_list_holds_the_shell_command_example() {
    assert!(ci_excluded::is_ci_excluded("76-ai-shell-command"));
    assert!(
        repo()
            .join("examples/76-ai-shell-command/sky.toml")
            .is_file(),
        "the excluded example is gone: drop it from src/ci_excluded.rs too"
    );
    for name in ci_excluded::CI_EXCLUDED_EXAMPLES {
        assert!(
            repo().join("examples").join(name).is_dir(),
            "src/ci_excluded.rs names examples/{name}, which does not exist"
        );
    }
}

#[test]
fn no_workflow_names_an_excluded_example() {
    let dir = repo().join(".github/workflows");
    let mut checked = 0;
    for entry in std::fs::read_dir(&dir).expect("read .github/workflows") {
        let path = entry.expect("dir entry").path();
        let ext = path.extension().and_then(|e| e.to_str()).unwrap_or("");
        if ext != "yml" && ext != "yaml" {
            continue;
        }
        checked += 1;
        let text = std::fs::read_to_string(&path).expect("read workflow");
        for name in ci_excluded::CI_EXCLUDED_EXAMPLES {
            assert!(
                !text.contains(name),
                "{} names examples/{name}, which the user excluded from CI \
                 (anzellai/sky#299): no CI job may build, train, evaluate or run it",
                path.display()
            );
        }
    }
    assert!(
        checked >= 5,
        "found only {checked} workflows: the scan is broken"
    );
}

#[test]
fn every_rust_enumerator_skips_the_excluded_examples() {
    for (rel, needle) in RUST_ENUMERATORS {
        let text = read(rel);
        let code = code_lines(&text).join("\n");
        assert!(
            code.contains(needle),
            "{rel} enumerates examples/ but does not skip the CI-excluded examples \
             (expected `{needle}` in its code). USER RULE (anzellai/sky#299): \
             examples/76-ai-shell-command is excluded from CI in general."
        );
        assert!(
            text.contains("anzellai/sky#299"),
            "{rel} skips the excluded examples without the comment that cites the \
             user rule (anzellai/sky#299)"
        );
    }
}

/// Does a line of Rust code walk `examples/`?
fn walks_examples(line: &str) -> bool {
    let l = line.replace(' ', "");
    (l.contains("read_dir(") && l.contains("\"examples\""))
        || l.contains("read_dir(&examples")
        || l.contains("read_dir(examples")
        || l.contains("subdirs(\"examples\")")
        || (l.contains("for") && l.contains("in[") && l.contains("\"examples\""))
        || l.contains("collect_sky(&examples")
        || l.contains("letexamples_root=")
        || (l.contains("\"examples\"") && l.contains("ls-files"))
}

fn rust_files(dir: &Path, out: &mut Vec<PathBuf>) {
    let Ok(rd) = std::fs::read_dir(dir) else {
        return;
    };
    for e in rd.flatten() {
        let p = e.path();
        let name = p.file_name().and_then(|n| n.to_str()).unwrap_or("");
        if p.is_dir() {
            if name != "target" && !name.starts_with('.') {
                rust_files(&p, out);
            }
        } else if name.ends_with(".rs") {
            out.push(p);
        }
    }
}

#[test]
fn a_new_rust_walker_of_examples_must_skip_the_excluded_examples() {
    let root = repo();
    let mut files = Vec::new();
    rust_files(&root.join("rust/crates"), &mut files);
    let known: BTreeSet<&str> = RUST_ENUMERATORS
        .iter()
        .map(|(f, _)| *f)
        .chain(RUST_NOT_ENUMERATORS.iter().map(|(f, _)| *f))
        .collect();
    let mut found = 0;
    let mut unknown = Vec::new();
    for path in &files {
        let rel = path
            .strip_prefix(&root)
            .unwrap()
            .to_string_lossy()
            .replace('\\', "/");
        let text = std::fs::read_to_string(path).unwrap_or_default();
        if code_lines(&text).iter().any(|l| walks_examples(l)) {
            found += 1;
            if !known.contains(rel.as_str()) {
                unknown.push(rel);
            }
        }
    }
    assert!(
        found >= 10,
        "the scan found only {found} walkers of examples/: it is broken"
    );
    assert!(
        unknown.is_empty(),
        "these files walk examples/ and are not known to this test:\n  {}\n\
         USER RULE (anzellai/sky#299): examples/76-ai-shell-command is excluded from \
         CI in general. Skip it by name (`crate::ci_excluded::is_ci_excluded` in \
         xtask) with a comment citing the rule, and add the file to \
         RUST_ENUMERATORS in this test.",
        unknown.join("\n  ")
    );
}

/// Does a line of shell walk or copy `examples/` as a whole?
fn script_walks_examples(line: &str) -> bool {
    line.contains("examples/*")
        || line.contains("examples/.\"")
        || line.contains("/examples/\" \"")
        || line.contains("find examples")
        || line.contains("ls examples")
}

#[test]
fn every_script_that_walks_examples_skips_the_excluded_examples() {
    let root = repo();
    let mut unknown = Vec::new();
    let mut found = 0;
    let mut scripts: Vec<PathBuf> = Vec::new();
    for dir in ["scripts", "scripts/lib", "scripts/ai"] {
        if let Ok(rd) = std::fs::read_dir(root.join(dir)) {
            for e in rd.flatten() {
                let p = e.path();
                let ext = p.extension().and_then(|x| x.to_str()).unwrap_or("");
                if p.is_file() && (ext == "sh" || ext == "mjs" || ext == "js") {
                    scripts.push(p);
                }
            }
        }
    }
    for path in &scripts {
        let rel = path
            .strip_prefix(&root)
            .unwrap()
            .to_string_lossy()
            .replace('\\', "/");
        let text = std::fs::read_to_string(path).unwrap_or_default();
        if code_lines(&text).iter().any(|l| script_walks_examples(l)) {
            found += 1;
            if !SCRIPT_ENUMERATORS.contains(&rel.as_str()) {
                unknown.push(rel.clone());
            }
            for name in ci_excluded::CI_EXCLUDED_EXAMPLES {
                assert!(
                    code_lines(&text).iter().any(|l| l.contains(name)),
                    "{rel} walks examples/ but does not skip examples/{name} by name. \
                     USER RULE (anzellai/sky#299): it is excluded from CI in general."
                );
            }
        }
    }
    assert!(
        found >= 3,
        "the scan found only {found} scripts walking examples/: it is broken"
    );
    assert!(
        unknown.is_empty(),
        "these scripts walk examples/ and are not known to this test: {unknown:?}. \
         Skip examples/76-ai-shell-command by name with a comment citing \
         anzellai/sky#299, then add the script to SCRIPT_ENUMERATORS."
    );
    for rel in SCRIPT_ENUMERATORS {
        assert!(
            read(rel).contains("anzellai/sky#299"),
            "{rel} skips the excluded example without citing the user rule"
        );
    }
}

#[test]
fn the_example_sweep_never_lists_an_excluded_example() {
    let out = Command::new("bash")
        .arg("scripts/example-sweep.sh")
        .arg("--list")
        .current_dir(repo())
        .output()
        .expect("run example-sweep.sh --list");
    assert!(out.status.success(), "example-sweep.sh --list failed");
    let names: Vec<String> = String::from_utf8_lossy(&out.stdout)
        .lines()
        .map(|l| l.trim().to_string())
        .filter(|l| !l.is_empty())
        .collect();
    assert!(
        names.len() > 20,
        "example-sweep.sh --list printed only {} names",
        names.len()
    );
    for name in ci_excluded::CI_EXCLUDED_EXAMPLES {
        assert!(
            !names.iter().any(|n| n == name),
            "example-sweep.sh would build examples/{name}, which is excluded from CI"
        );
    }
}

#[test]
fn a_change_to_an_excluded_example_plans_no_gate() {
    let dir = std::env::temp_dir().join(format!("sky-ci-excluded-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(dir.join("scripts/lib")).unwrap();
    for rel in ["scripts/gates-for-change.sh", "scripts/lib/with-timeout.sh"] {
        std::fs::copy(repo().join(rel), dir.join(rel)).unwrap();
    }
    let git = |args: &[&str]| {
        let ok = Command::new("git")
            .arg("-C")
            .arg(&dir)
            .args(args)
            .output()
            .expect("git runs")
            .status
            .success();
        assert!(ok, "git {args:?}");
    };
    git(&["init", "-q", "-b", "main"]);
    git(&["config", "user.email", "t@example.invalid"]);
    git(&["config", "user.name", "t"]);
    git(&["add", "-A"]);
    git(&["commit", "-q", "-m", "base"]);
    for rel in [
        "examples/76-ai-shell-command/src/Main.sky",
        "examples/76-ai-shell-command/README.md",
    ] {
        let p = dir.join(rel);
        std::fs::create_dir_all(p.parent().unwrap()).unwrap();
        std::fs::write(p, "changed\n").unwrap();
    }
    let out = Command::new("bash")
        .arg(dir.join("scripts/gates-for-change.sh"))
        .args(["--dry-run", "--base", "main"])
        .output()
        .expect("the script runs");
    let text = format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    let _ = std::fs::remove_dir_all(&dir);
    assert!(out.status.success(), "dry-run failed:\n{text}");
    for gate in [
        "example-sweep",
        "roundtrip",
        "sky-fmt-check",
        "fmt-clean",
        "build-run",
        "harness --only",
        "--tier",
        "conformance",
        "UNMAPPED",
    ] {
        assert!(
            !text.contains(gate),
            "a change to examples/76-ai-shell-command plans `{gate}`, but the example \
             is excluded from CI (anzellai/sky#299):\n{text}"
        );
    }
}

#[test]
fn no_ci_runs_sky_verify_over_every_example() {
    // `sky verify` with no target, run where `examples/` exists, verifies every
    // example. No workflow and no script may call it that way.
    let root = repo();
    let mut texts: Vec<(String, String)> = Vec::new();
    for dir in [".github/workflows", "scripts"] {
        for e in std::fs::read_dir(root.join(dir))
            .expect("read dir")
            .flatten()
        {
            let p = e.path();
            if p.is_file() {
                texts.push((
                    p.display().to_string(),
                    std::fs::read_to_string(&p).unwrap_or_default(),
                ));
            }
        }
    }
    for (name, text) in texts {
        for line in code_lines(&text) {
            let t = line
                .trim_end()
                .trim_end_matches(['"', '\'', ';', '\\', ')'])
                .trim_end();
            assert!(
                !(t.ends_with("sky verify") || t.ends_with("sky\" verify")),
                "{name} runs `sky verify` with no target, which verifies every example \
                 including the CI-excluded ones: {line}"
            );
        }
    }
}

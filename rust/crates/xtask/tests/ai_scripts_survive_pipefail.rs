//! The AI scripts never cut a pipeline short under `set -o pipefail`.
//!
//! `producer | grep -q x` and `producer | head -1` stop reading early. The
//! producer then dies of SIGPIPE, and under pipefail the whole pipeline fails.
//! Whether it does depends on timing: the trainer bundle passed on a branch
//! push and failed on the `sky-trainer-v0.1.0` tag run with
//! `grep: write error: Broken pipe` in `test-trainer.sh`'s `first_loss`. In a
//! check such as `readelf | grep -q cblas_sgemm` the same race turns a symbol
//! that is present into a reported miss. The scripts read a whole stream into
//! a string first (`grep -q x <<<"$(producer)"`), or use one `sed` that reads
//! to the end.

use std::path::{Path, PathBuf};

fn scripts(dir: &Path, out: &mut Vec<PathBuf>) {
    for entry in std::fs::read_dir(dir).unwrap() {
        let path = entry.unwrap().path();
        if path.is_dir() {
            scripts(&path, out);
        } else if path.extension().is_some_and(|e| e == "sh") {
            out.push(path);
        }
    }
}

/// A pipeline stage after `|` that may exit before its input ends.
fn early_exit_stage(line: &str) -> bool {
    line.split('|').skip(1).any(|stage| {
        let words: Vec<&str> = stage.split_whitespace().collect();
        match words.first() {
            Some(&"head") => true,
            Some(&"grep") => words[1..]
                .iter()
                .take_while(|w| w.starts_with('-'))
                .any(|w| !w.starts_with("--") && (w.contains('q') || w.contains('m'))),
            _ => false,
        }
    })
}

fn offenders(text: &str) -> Vec<String> {
    if !text.contains("pipefail") {
        return Vec::new();
    }
    text.lines()
        .enumerate()
        .filter(|(_, l)| !l.trim_start().starts_with('#'))
        // `||` is not a pipe.
        .filter(|(_, l)| early_exit_stage(&l.replace("||", "  ")))
        .map(|(i, l)| format!("{}: {}", i + 1, l.trim()))
        .collect()
}

#[test]
fn no_ai_script_cuts_a_pipeline_short_under_pipefail() {
    let root = PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../.."));
    let mut files = Vec::new();
    scripts(&root.join("scripts/ai"), &mut files);
    assert!(!files.is_empty(), "no scripts found under scripts/ai");
    let mut bad = Vec::new();
    for f in &files {
        for o in offenders(&std::fs::read_to_string(f).unwrap()) {
            bad.push(format!(
                "{}:{o}",
                f.strip_prefix(&root).unwrap_or(f).display()
            ));
        }
    }
    assert!(
        bad.is_empty(),
        "these pipelines stop reading early and fail under pipefail when the \
         producer gets SIGPIPE; read the stream first (grep -q x <<<\"$(cmd)\") \
         or use one sed that reads to the end:\n{}",
        bad.join("\n")
    );
}

#[test]
fn the_check_catches_each_early_exit_shape() {
    let script = |body: &str| format!("set -euo pipefail\n{body}\n");
    for body in [
        "f() { grep x \"$1\" | head -1; }",
        "readelf -sW lib.so | grep -q ' cblas_sgemm$' || bad=1",
        "deps a | grep -Eq 'blas'",
        "list | grep -qx \"$api\"",
        "log | grep -m1 err",
    ] {
        assert_eq!(offenders(&script(body)).len(), 1, "missed: {body}");
    }
    for body in [
        "grep -q x <<<\"$(readelf -sW lib.so)\" || bad=1",
        "sed -n '/x/{p;q;}' file",
        "grep -E error log | sed -n 1,40p",
        "a || b",
        "# readelf | grep -q x",
        "grep --quiet-ish x | sort",
    ] {
        assert!(offenders(&script(body)).is_empty(), "false alarm: {body}");
    }
    assert!(
        offenders("head | grep -q x\n").is_empty(),
        "no pipefail, no race"
    );
}

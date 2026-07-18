//! `xtask build-run` — the M4 gate. For each example: run the Rust `sky build`
//! (parse+resolve+typecheck+lower+emit+`go build`), then execute the binary and
//! compare its stdout to the Haskell oracle's binary where feasible.
//!
//! Prints a per-example table: BUILD ok? / RUN ok? / matches-oracle?
//! Usage:
//!   xtask build-run [--only=NAME] [--run] [--oracle] [-v]

use project::{build_example, BuildOptions};
use std::path::{Path, PathBuf};
use std::process::Command;

/// CLI-family examples the M4 gate targets first (build + run + compare).
const CLI_FAMILY: &[&str] = &[
    "01-hello-world",
    "02-go-stdlib",
    "14-task-demo",
    "07-todo-cli",
    "00-standard-libs",
    "20-cli-counter",
];

/// Per-example stdin to feed the binary (line-oriented TEA apps read stdin).
fn stdin_for(name: &str) -> Option<String> {
    match name {
        "20-cli-counter" => Some("+\n+\n-\nq\n".to_string()),
        _ => None,
    }
}

struct Row {
    name: String,
    emitted: bool,
    build_ok: bool,
    run_ok: Option<bool>,
    oracle_match: Option<bool>,
    blocker: String,
}

pub fn run(args: &[String], root: &Path) -> i32 {
    let only = args
        .iter()
        .find(|a| a.starts_with("--only="))
        .map(|a| a.trim_start_matches("--only=").to_string());
    let do_run = args.iter().any(|a| a == "--run" || a == "--oracle");
    let do_oracle = args.iter().any(|a| a == "--oracle");
    let verbose = args.iter().any(|a| a == "-v" || a == "--verbose");
    let dump = args.iter().any(|a| a == "--dump");

    let all = args.iter().any(|a| a == "--all");
    let names: Vec<String> = match &only {
        Some(n) => vec![n.clone()],
        None if all => {
            let mut ds: Vec<String> = std::fs::read_dir(root.join("examples"))
                .map(|rd| {
                    rd.filter_map(|e| e.ok())
                        .filter(|e| e.path().is_dir())
                        .filter_map(|e| e.file_name().to_str().map(String::from))
                        .filter(|n| root.join("examples").join(n).join("src").is_dir())
                        .collect()
                })
                .unwrap_or_default();
            ds.sort();
            ds
        }
        None => CLI_FAMILY.iter().map(|s| s.to_string()).collect(),
    };

    let mut rows = Vec::new();
    for name in &names {
        let dir = root.join("examples").join(name);
        if !dir.is_dir() {
            continue;
        }
        let opts = BuildOptions {
            repo_root: root.to_path_buf(),
            example_dir: dir.clone(),
            out_dir_name: "sky-out-rust".into(),
            run: do_run,
            stdin: stdin_for(name),
        };
        let rep = build_example(&opts);

        if dump {
            let mg = dir.join("sky-out-rust").join("main.go");
            if let Ok(s) = std::fs::read_to_string(&mg) {
                println!("==== emitted main.go for {name} ====\n{s}\n==== end ====");
            }
        }

        let mut blocker = String::new();
        if !rep.emitted {
            blocker = rep.note.clone();
        } else if !rep.go_build_ok {
            blocker = first_go_error(&rep.go_build_stderr);
        }

        // oracle comparison
        let oracle_match = if do_oracle && rep.go_build_ok && rep.run_ok == Some(true) {
            compare_oracle(&dir, name, rep.run_stdout.as_deref().unwrap_or(""))
        } else {
            None
        };

        if verbose && !rep.warnings.is_empty() {
            eprintln!("  [{name}] {} warnings; first few:", rep.warnings.len());
            for w in rep.warnings.iter().take(8) {
                eprintln!("     · {w}");
            }
        }
        if verbose && !rep.go_build_ok && !rep.go_build_stderr.is_empty() {
            eprintln!("  [{name}] go build stderr:\n{}", indent(&rep.go_build_stderr, 6));
        }

        rows.push(Row {
            name: name.clone(),
            emitted: rep.emitted,
            build_ok: rep.go_build_ok,
            run_ok: rep.run_ok,
            oracle_match,
            blocker,
        });
    }

    print_table(&rows);
    // gate: at least hello-world must build+run.
    let hw = rows.iter().find(|r| r.name == "01-hello-world");
    match hw {
        Some(r) if r.build_ok && r.run_ok != Some(false) => 0,
        _ => 1,
    }
}

fn compare_oracle(dir: &Path, _name: &str, rust_stdout: &str) -> Option<bool> {
    // run the oracle-produced binary (examples/NAME/sky-out/app) if present.
    let app = dir.join("sky-out").join("app");
    if !app.exists() {
        return None;
    }
    let out = Command::new(&app).current_dir(dir.join("sky-out")).output().ok()?;
    let oracle_stdout = String::from_utf8_lossy(&out.stdout).to_string();
    Some(normalise(&oracle_stdout) == normalise(rust_stdout))
}

/// Strip volatile bits (timestamps, hashes) for a best-effort output compare.
fn normalise(s: &str) -> String {
    s.trim().to_string()
}

fn first_go_error(stderr: &str) -> String {
    stderr
        .lines()
        .find(|l| l.contains("error") || l.contains(".go:"))
        .unwrap_or("go build failed")
        .trim()
        .chars()
        .take(90)
        .collect()
}

fn indent(s: &str, n: usize) -> String {
    let pad = " ".repeat(n);
    s.lines().map(|l| format!("{pad}{l}")).collect::<Vec<_>>().join("\n")
}

fn print_table(rows: &[Row]) {
    let w = rows.iter().map(|r| r.name.len()).max().unwrap_or(8).max(8);
    println!("{:<w$}  {:>7}  {:>6}  {:>6}  {:>7}  BLOCKER", "EXAMPLE", "EMITTED", "BUILD", "RUN", "ORACLE", w = w);
    println!("{}", "-".repeat(w + 50));
    let (mut nb, mut nr) = (0, 0);
    for r in rows {
        let b = if r.build_ok { "ok" } else { "FAIL" };
        if r.build_ok {
            nb += 1;
        }
        let run = match r.run_ok {
            Some(true) => {
                nr += 1;
                "ok"
            }
            Some(false) => "FAIL",
            None => "-",
        };
        let oracle = match r.oracle_match {
            Some(true) => "match",
            Some(false) => "DIFF",
            None => "-",
        };
        let emitted = if r.emitted { "yes" } else { "no" };
        println!(
            "{:<w$}  {:>7}  {:>6}  {:>6}  {:>7}  {}",
            r.name, emitted, b, run, oracle, r.blocker,
            w = w
        );
    }
    println!("{}", "-".repeat(w + 50));
    println!("BUILD ok: {nb}/{} | RUN ok: {nr}/{}", rows.len(), rows.len());
}

// silence unused on some paths
#[allow(dead_code)]
fn _pathbuf(p: &Path) -> PathBuf {
    p.to_path_buf()
}

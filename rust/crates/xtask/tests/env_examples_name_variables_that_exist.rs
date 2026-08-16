//! A scaffolded `.env` may not name a `SKY_*` variable nothing reads.
//!
//! # The defect this exists to remove
//!
//! `examples/13-skyshop/.env.example` shipped this:
//!
//! ```text
//! # Session
//! SKY_LIVE_SESSION_STORE=sqlite
//! SKY_LIVE_SESSION_PATH=skyshop_sessions.db
//! ```
//!
//! The variables the runtime reads are `SKY_LIVE_STORE` and
//! `SKY_LIVE_STORE_PATH` (`runtime-go/rt/live_store.go`, via
//! `skyGetenv("LIVE_STORE")`). `SKY_LIVE_SESSION_*` is the pre-rewrite
//! namespace; it survives today only in the two retired compiler trees and is
//! read by nothing in `runtime-go/` or `rust/`.
//!
//! SkyShop itself still got sqlite sessions, because its `sky.toml` sets
//! `[live] store` and `storePath` and those seed the same defaults. That is
//! what made the dead lines survive: the example worked, so nobody looked
//! again. The hazard is the reader, who copies a "# Session" block that
//! demonstrably belongs in a `.env` into a project whose `sky.toml` has no
//! `[live] store` — and there it silently degrades to memory sessions, losing
//! every session on restart.
//!
//! The tell was in the same file: `SKY_LIVE_PORT=4000` on line 4 DOES override
//! `sky.toml`'s `port = 8000`. Correctly-named variables in that file win;
//! these two did nothing.
//!
//! # The rule
//!
//! Every `SKY_*` variable named in a tracked `.env*` file is a variable
//! something actually reads — or it is listed in [`CONSUMED_ELSEWHERE`] with a
//! reason.
//!
//! The set of real names is DERIVED from the sources at test time, never
//! hardcoded here: a hardcoded list would rot the first time a variable was
//! renamed, which is the exact failure mode under test.

use std::collections::BTreeSet;
use std::path::{Path, PathBuf};

fn repo_root() -> PathBuf {
    PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../.."))
}

/// `SKY_*` names that legitimately appear in a `.env` while nothing in
/// `runtime-go/` or `rust/` reads them. Each needs a reason, because the
/// default answer is "then it is a typo".
const CONSUMED_ELSEWHERE: &[(&str, &str)] = &[(
    "SKY_AUTH_TOKEN_SECRET",
    "Read by USER code via System.getenvOr, never by the runtime — deliberately, \
     so the secret never reaches a runtime log. runtime-go/rt/startup_report.go:70 \
     documents the omission and startup_report_test.go:110 asserts the startup \
     report does NOT name it.",
)];

/// Every `SKY_`-prefixed variable the runtime or compiler actually reads.
///
/// Sources, all parsed rather than assumed:
///
/// * `runtime-go/rt/*.go` — `skyGetenv("X")` / `skyLookupEnv("X")` /
///   `skyEnvName("X")` take a SUFFIX and prepend the configured prefix, so
///   suffix `LIVE_STORE` means the variable `SKY_LIVE_STORE`.
/// * `runtime-go/rt/*.go` — literal `os.Getenv("SKY_X")` for the handful of
///   reads that bypass the prefix helper.
/// * `rust/crates/project/src/build.rs` — suffixes seeded from `sky.toml` via
///   `extra_defaults`, which become `SKY_*` defaults in the generated `init()`.
fn variables_that_are_read(root: &Path) -> BTreeSet<String> {
    let mut names = BTreeSet::new();

    let rt_dir = root.join("runtime-go/rt");
    let entries = std::fs::read_dir(&rt_dir)
        .unwrap_or_else(|e| panic!("cannot read {}: {e}", rt_dir.display()));
    for entry in entries {
        let path = entry.expect("dir entry").path();
        if path.extension().and_then(|e| e.to_str()) != Some("go") {
            continue;
        }
        let src = std::fs::read_to_string(&path).expect("read go source");

        // skyGetenv("SUFFIX") / skyLookupEnv("SUFFIX") / skyEnvName("SUFFIX")
        for helper in ["skyGetenv(\"", "skyLookupEnv(\"", "skyEnvName(\""] {
            for (idx, _) in src.match_indices(helper) {
                let rest = &src[idx + helper.len()..];
                if let Some(end) = rest.find('"') {
                    let suffix = &rest[..end];
                    if is_env_token(suffix) {
                        names.insert(format!("SKY_{suffix}"));
                    }
                }
            }
        }

        // Literal os.Getenv("SKY_...") / os.LookupEnv("SKY_...")
        for helper in ["os.Getenv(\"", "os.LookupEnv(\""] {
            for (idx, _) in src.match_indices(helper) {
                let rest = &src[idx + helper.len()..];
                if let Some(end) = rest.find('"') {
                    let name = &rest[..end];
                    if name.starts_with("SKY_") && is_env_token(name) {
                        names.insert(name.to_string());
                    }
                }
            }
        }
    }

    // Suffixes the compiler seeds from sky.toml.
    let build_rs = root.join("rust/crates/project/src/build.rs");
    let src = std::fs::read_to_string(&build_rs).expect("read build.rs");
    for (idx, _) in src.match_indices("extra_defaults.push((\"") {
        let rest = &src[idx + "extra_defaults.push((\"".len()..];
        if let Some(end) = rest.find('"') {
            let suffix = &rest[..end];
            if is_env_token(suffix) {
                names.insert(format!("SKY_{suffix}"));
            }
        }
    }

    assert!(
        names.len() > 20,
        "the derivation found only {} variables — it has broken, and a broken \
         derivation would make this gate pass vacuously. Found: {names:?}",
        names.len()
    );
    names
}

fn common_prefix_len(a: &str, b: &str) -> usize {
    a.bytes().zip(b.bytes()).take_while(|(x, y)| x == y).count()
}

fn is_env_token(s: &str) -> bool {
    !s.is_empty()
        && s.chars()
            .all(|c| c.is_ascii_uppercase() || c.is_ascii_digit() || c == '_')
}

/// Tracked `.env*` files. `git ls-files` rather than a directory walk, so a
/// developer's untracked local `.env` is never read.
fn tracked_env_files(root: &Path) -> Vec<PathBuf> {
    let out = std::process::Command::new("git")
        .arg("-C")
        .arg(root)
        .args(["ls-files", "-z", "*.env*", ".env*", "**/.env*"])
        .output()
        .expect("git ls-files");
    assert!(out.status.success(), "git ls-files failed");
    String::from_utf8_lossy(&out.stdout)
        .split('\0')
        .filter(|s| !s.is_empty())
        .map(|rel| root.join(rel))
        .filter(|p| p.is_file())
        .collect()
}

#[test]
fn env_examples_name_variables_that_exist() {
    let root = repo_root();
    let known = variables_that_are_read(&root);
    let files = tracked_env_files(&root);

    assert!(
        !files.is_empty(),
        "no tracked .env files found — the gate would pass vacuously"
    );

    let mut failures = Vec::new();
    for file in &files {
        let text = match std::fs::read_to_string(file) {
            Ok(t) => t,
            Err(_) => continue,
        };
        for (lineno, line) in text.lines().enumerate() {
            let line = line.trim();
            if line.is_empty() || line.starts_with('#') {
                continue;
            }
            let Some((name, _)) = line.split_once('=') else {
                continue;
            };
            let name = name.trim().trim_start_matches("export ").trim();
            if !name.starts_with("SKY_") || !is_env_token(name) {
                continue;
            }
            if known.contains(name) || CONSUMED_ELSEWHERE.iter().any(|(n, _)| *n == name) {
                continue;
            }
            let rel = file.strip_prefix(&root).unwrap_or(file);
            // Suggest by longest shared prefix — for SKY_LIVE_SESSION_STORE
            // that puts SKY_LIVE_STORE and SKY_LIVE_STORE_PATH at the top,
            // which is the actual answer.
            let mut ranked: Vec<(usize, &String)> = known
                .iter()
                .map(|k| (common_prefix_len(name, k), k))
                .filter(|(n, _)| *n > 4)
                .collect();
            ranked.sort_by(|a, b| b.0.cmp(&a.0).then(a.1.cmp(b.1)));
            let near: Vec<String> = ranked.iter().take(3).map(|(_, k)| (*k).clone()).collect();
            failures.push(format!(
                "{}:{}: `{name}` is not read by anything. Closest real names: {}",
                rel.display(),
                lineno + 1,
                if near.is_empty() {
                    "(none)".to_string()
                } else {
                    near.join(", ")
                }
            ));
        }
    }

    assert!(
        failures.is_empty(),
        "a scaffolded .env names {} variable(s) nothing reads. Copy that file \
         and the setting silently does nothing:\n  {}",
        failures.len(),
        failures.join("\n  ")
    );
}

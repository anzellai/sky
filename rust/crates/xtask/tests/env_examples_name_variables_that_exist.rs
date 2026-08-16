//! A scaffolded `.env` may not name a variable nothing reads.
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
//! # The defect the FIRST version of this gate had
//!
//! Its reach was exactly its blind spot. It derived "variables that exist" by
//! the `SKY_` prefix and then checked only `SKY_`-prefixed names, so the two
//! sets were the same set and the gate was 100% of itself. Demonstrated:
//! appending `ENV=totally_bogus`, `DATABASE_URL=nope` and
//! `ENVV_TYPO_THAT_IS_REAL=x` to a tracked `.env` gave `test result: ok`, while
//! the control `SKY_BOGUS_CONTROL=x` FAILED.
//!
//! Tracked `.env` files carry 13 non-`SKY_` variables — `ENV` (twice),
//! `DATABASE_URL`, `STRIPE_API_KEY`, `SMTP_*`, `BLOG_ADMIN_PASSWORD` — every
//! secret among them, and `ENV` is the very variable the gate's own commit was
//! about. Two smaller holes in the same derivation: the `read_dir` over
//! `runtime-go/rt` was NON-RECURSIVE, so `rt/hub`, `rt/jobs`, `rt/telemetry`,
//! `rt/dbshare`, `rt/console_app` and `cmd/sky-hub` were invisible; and the
//! vacuity guard was a FLOOR (`names.len() > 20`) rather than an exact
//! statement of what the gate covered.
//!
//! # The rule
//!
//! EVERY variable named in a tracked `.env*` file — whatever its prefix — is a
//! variable something actually reads: the Go runtime, the Rust compiler, or
//! Sky source inside the same project. Or it is listed in
//! [`CONSUMED_ELSEWHERE`] with a reason.
//!
//! The set of real names is DERIVED from the sources at test time, never
//! hardcoded here: a hardcoded list would rot the first time a variable was
//! renamed, which is the exact failure mode under test. What IS pinned is the
//! size of what the gate checked — see [`EXPECTED_ASSIGNMENTS`] — and a
//! sentinel per extraction route, so a derivation that silently stops finding
//! one class of read fails instead of passing wider.

use std::collections::{BTreeMap, BTreeSet};
use std::path::{Path, PathBuf};

fn repo_root() -> PathBuf {
    PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../.."))
}

/// The EXACT number of `NAME=value` assignments the gate must check across
/// every tracked `.env*` file.
///
/// Exact, not a floor. A floor is satisfied by a gate that quietly stopped
/// reading one of the files; this number changes when a variable is added or
/// removed, and changing it is a one-line edit next to the change that caused
/// it. `>= n` is how `ty/tests/reject.rs` came to assert 13 against an actual
/// 63, where deleting 50 corpus files kept it green.
const EXPECTED_ASSIGNMENTS: usize = 17;

/// Names that legitimately appear in a `.env` while nothing in `runtime-go/`,
/// `rust/` or the project's own Sky source reads them. Each needs a reason,
/// because the default answer is "then it is a typo".
const CONSUMED_ELSEWHERE: &[(&str, &str)] = &[(
    "SKY_AUTH_TOKEN_SECRET",
    "Read by USER code via System.getenvOr, never by the runtime — deliberately, \
     so the secret never reaches a runtime log. runtime-go/rt/startup_report.go:70 \
     documents the omission and startup_report_test.go:110 asserts the startup \
     report does NOT name it. `sky doctor` knows the convention; nothing in \
     runtime-go/ reads the name.",
)];

/// One sentinel per extraction route. If a route breaks, its sentinel vanishes
/// and the gate fails — where a count alone would just get smaller and a floor
/// would not notice at all.
const ROUTE_SENTINELS: &[(&str, &str)] = &[
    ("SKY_LIVE_STORE", "skyGetenv(\"SUFFIX\") in runtime-go/rt"),
    ("ENV", "literal os.Getenv(\"NAME\") in runtime-go/rt"),
    ("DATABASE_URL", "literal os.Getenv of a NON-SKY_ name — the class the gate used to skip"),
    ("SKY_LIVE_TTL", "extra_defaults seeded from sky.toml by rust/crates/project/src/build.rs"),
    (
        "OTEL_EXPORTER_OTLP_ENDPOINT",
        "runtime-go/rt/telemetry/otel.go:415 — a read one directory BELOW runtime-go/rt, \
         reachable only by a recursive walk",
    ),
    (
        "SKY_CONSOLE_HUB_TOKEN",
        "runtime-go/rt/hub/hub.go:105 — another subdirectory the old non-recursive \
         read_dir could not see",
    ),
];

fn is_env_token(s: &str) -> bool {
    !s.is_empty()
        && s.chars()
            .all(|c| c.is_ascii_uppercase() || c.is_ascii_digit() || c == '_')
}

/// Collect every `"ARG"` that follows one of `helpers` in `src`, mapped by `f`.
fn scan_calls(src: &str, helpers: &[&str], names: &mut BTreeSet<String>, f: impl Fn(&str) -> Option<String>) {
    for helper in helpers {
        for (idx, _) in src.match_indices(helper) {
            let rest = &src[idx + helper.len()..];
            if let Some(end) = rest.find('"') {
                if let Some(name) = f(&rest[..end]) {
                    names.insert(name);
                }
            }
        }
    }
}

/// Every file under `dir` with one of `exts`, recursively.
///
/// RECURSIVE. The previous version called `read_dir` on `runtime-go/rt` and
/// stopped there, which is the same non-recursive shape an earlier audit round
/// had already found and removed once.
fn files_under(dir: &Path, exts: &[&str], out: &mut Vec<PathBuf>) {
    let Ok(entries) = std::fs::read_dir(dir) else {
        panic!("cannot read {}", dir.display());
    };
    for entry in entries {
        let path = entry.expect("dir entry").path();
        let name = path.file_name().map(|n| n.to_string_lossy().into_owned()).unwrap_or_default();
        if name.starts_with('.') || name == "target" || name == "node_modules" {
            continue;
        }
        if path.is_dir() {
            files_under(&path, exts, out);
            continue;
        }
        if path.extension().and_then(|e| e.to_str()).is_some_and(|e| exts.contains(&e)) {
            out.push(path);
        }
    }
}

/// Every variable the runtime or the compiler actually reads.
///
/// Sources, all parsed rather than assumed:
///
/// * `runtime-go/**/*.go` (recursively, test files excluded) —
///   `skyGetenv("X")` / `skyLookupEnv("X")` / `skyEnvName("X")` take a SUFFIX
///   and prepend the configured prefix, so suffix `LIVE_STORE` means the
///   variable `SKY_LIVE_STORE`.
/// * `runtime-go/**/*.go` — literal `os.Getenv("X")` / `os.LookupEnv("X")` for
///   the reads that bypass the prefix helper. **Any** name, not only `SKY_*`:
///   `ENV` and `DATABASE_URL` are read exactly this way.
/// * `rust/crates/**/*.rs` — `std::env::var("X")` / `var_os` / `env::var`.
/// * `rust/crates/project/src/build.rs` — suffixes seeded from `sky.toml` via
///   `extra_defaults`, which become `SKY_*` defaults in the generated `init()`.
fn variables_that_are_read(root: &Path) -> BTreeSet<String> {
    let mut names = BTreeSet::new();

    let mut go: Vec<PathBuf> = Vec::new();
    files_under(&root.join("runtime-go"), &["go"], &mut go);
    let go: Vec<PathBuf> = go
        .into_iter()
        .filter(|p| !p.to_string_lossy().ends_with("_test.go"))
        .collect();
    assert!(
        go.len() > 100,
        "the Go walk found only {} non-test files under runtime-go/ — it has broken",
        go.len()
    );
    for path in &go {
        let src = std::fs::read_to_string(path).expect("read go source");
        scan_calls(
            &src,
            &["skyGetenv(\"", "skyLookupEnv(\"", "skyEnvName(\""],
            &mut names,
            |s| is_env_token(s).then(|| format!("SKY_{s}")),
        );
        scan_calls(
            &src,
            &["os.Getenv(\"", "os.LookupEnv(\"", "Getenv(\"", "LookupEnv(\""],
            &mut names,
            |s| is_env_token(s).then(|| s.to_string()),
        );
    }

    let mut rs: Vec<PathBuf> = Vec::new();
    files_under(&root.join("rust/crates"), &["rs"], &mut rs);
    assert!(
        rs.len() > 50,
        "the Rust walk found only {} files under rust/crates — it has broken",
        rs.len()
    );
    for path in &rs {
        let src = std::fs::read_to_string(path).expect("read rust source");
        scan_calls(
            &src,
            &["env::var(\"", "env::var_os(\"", "env::remove_var(\"", "env::set_var(\""],
            &mut names,
            |s| is_env_token(s).then(|| s.to_string()),
        );
    }

    // Suffixes the compiler seeds from sky.toml.
    let build_rs = root.join("rust/crates/project/src/build.rs");
    let src = std::fs::read_to_string(&build_rs).expect("read build.rs");
    scan_calls(&src, &["extra_defaults.push((\""], &mut names, |s| {
        is_env_token(s).then(|| format!("SKY_{s}"))
    });

    for (sentinel, route) in ROUTE_SENTINELS {
        assert!(
            names.contains(*sentinel),
            "the derivation lost `{sentinel}`, the sentinel for: {route}. A route that \
             stops finding reads makes this gate pass WIDER, not narrower — which is \
             exactly the shape it exists to refuse."
        );
    }
    names
}

/// Names read by Sky source under `dir` — `System.getenv "X"` /
/// `System.getenvOr "X"`.
///
/// Scoped to the project that owns the `.env`, not global: a variable read by a
/// DIFFERENT example is not a reason for this one to advertise it.
fn sky_reads_under(dir: &Path) -> BTreeSet<String> {
    let mut names = BTreeSet::new();
    let mut sky: Vec<PathBuf> = Vec::new();
    if dir.is_dir() {
        files_under(dir, &["sky", "skyi"], &mut sky);
    }
    for path in &sky {
        let Ok(src) = std::fs::read_to_string(path) else {
            continue;
        };
        scan_calls(
            &src,
            &["System.getenv \"", "System.getenvOr \"", "getenv \"", "getenvOr \""],
            &mut names,
            |s| is_env_token(s).then(|| s.to_string()),
        );
    }
    names
}

fn common_prefix_len(a: &str, b: &str) -> usize {
    a.bytes().zip(b.bytes()).take_while(|(x, y)| x == y).count()
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

    // Sky reads are resolved per owning project and cached, because several
    // `.env` files can share one.
    let mut per_project: BTreeMap<PathBuf, BTreeSet<String>> = BTreeMap::new();
    let mut checked = 0usize;
    let mut failures = Vec::new();

    for file in &files {
        let text = match std::fs::read_to_string(file) {
            Ok(t) => t,
            Err(_) => continue,
        };
        let project = file.parent().unwrap_or(&root).to_path_buf();
        let local = per_project
            .entry(project.clone())
            .or_insert_with(|| sky_reads_under(&project));

        for (lineno, line) in text.lines().enumerate() {
            let line = line.trim();
            if line.is_empty() || line.starts_with('#') {
                continue;
            }
            let Some((name, _)) = line.split_once('=') else {
                continue;
            };
            let name = name.trim().trim_start_matches("export ").trim();
            if !is_env_token(name) {
                continue;
            }
            // EVERY name, whatever its prefix. Restricting this to `SKY_` is
            // what made the gate's reach equal to its blind spot.
            checked += 1;
            if known.contains(name)
                || local.contains(name)
                || CONSUMED_ELSEWHERE.iter().any(|(n, _)| *n == name)
            {
                continue;
            }
            let rel = file.strip_prefix(&root).unwrap_or(file);
            // Suggest by longest shared prefix — for SKY_LIVE_SESSION_STORE
            // that puts SKY_LIVE_STORE and SKY_LIVE_STORE_PATH at the top,
            // which is the actual answer.
            let mut ranked: Vec<(usize, &String)> = known
                .iter()
                .chain(local.iter())
                .map(|k| (common_prefix_len(name, k), k))
                .filter(|(n, _)| *n > 4)
                .collect();
            ranked.sort_by(|a, b| b.0.cmp(&a.0).then(a.1.cmp(b.1)));
            let near: Vec<String> = ranked.iter().take(3).map(|(_, k)| (*k).clone()).collect();
            failures.push(format!(
                "{}:{}: `{name}` is not read by anything — not the runtime, not the \
                 compiler, not the Sky source under {}. Closest real names: {}",
                rel.display(),
                lineno + 1,
                project.strip_prefix(&root).unwrap_or(&project).display(),
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

    assert_eq!(
        checked, EXPECTED_ASSIGNMENTS,
        "the gate checked {checked} assignments across {} tracked .env file(s), and \
         EXPECTED_ASSIGNMENTS says {EXPECTED_ASSIGNMENTS}. If you added or removed a \
         variable, update the constant in the same commit; if you did not, a file \
         stopped being read and this gate just got quieter.",
        files.len()
    );
}

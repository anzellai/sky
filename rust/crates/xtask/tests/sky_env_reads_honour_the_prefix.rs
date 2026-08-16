//! A project with `[env] prefix` must be able to reach every Sky-internal
//! setting.
//!
//! `docs/sky-toml.md` promises that all Sky-internal namespaces — `LIVE_*`,
//! `AUTH_*`, `LOG_*`, `DB_*`, `ENV`, `STATIC_DIR` — move with the configured
//! prefix, so `[env] prefix = "FENCE"` makes the runtime read
//! `FENCE_LIVE_PORT`. The runtime reads them through `skyGetenv`
//! (`runtime-go/rt/env_prefix.go`), which prepends the prefix.
//!
//! Several sites did not. `crossOriginIframeMode()` read raw
//! `os.Getenv("SKY_LIVE_FRAME_ANCESTORS")` — and that switch is what puts
//! `SameSite=None; Secure` on BOTH the session and CSRF cookies, so a project
//! with a custom prefix could not turn cross-origin embedding on AT ALL. The
//! same defect had already been found and fixed for `SKY_ENV`, in the same
//! functions; the adjacent switch was left raw.
//!
//! The failure mode is why this is a gate and not a review note: nothing
//! errors. The variable is simply never found, the feature silently stays off,
//! and the operator who set `FENCE_LIVE_FRAME_ANCESTORS` has no way to tell
//! that from "the feature does not work".
//!
//! The gate classifies EVERY `os.Getenv("SKY_…")` / `os.LookupEnv("SKY_…")` in
//! the runtime, so a newly-added read is a conscious decision rather than
//! something a later grill discovers:
//!
//!   * a read in a prefix-affected namespace must go through `skyGetenv` —
//!     there is no allowlist for these;
//!   * every other read must appear in `FIXED_NAME_READS` below with a stated
//!     reason. Adding a variable without classifying it fails the build.

use std::collections::BTreeSet;
use std::fs;
use std::path::{Path, PathBuf};

fn repo_root() -> PathBuf {
    let mut d = Path::new(env!("CARGO_MANIFEST_DIR")).to_path_buf();
    while !d.join("examples").is_dir() {
        d = d.parent().expect("repo root not found").to_path_buf();
    }
    d
}

/// Namespaces `docs/sky-toml.md` says the `[env] prefix` moves. A raw read of
/// one of these is unreachable for any project that sets a prefix.
const PREFIXED_NAMESPACES: &[&str] = &["LIVE_", "AUTH_", "LOG_", "DB_", "STATIC_DIR"];

/// `ENV` is prefix-affected as a whole name, not a namespace prefix.
const PREFIXED_EXACT: &[&str] = &["ENV"];

/// Reads that are deliberately fixed-name, each with the reason.
///
/// The common thread: these are set by something OUTSIDE the app — `sky run`,
/// a console hub, a control plane, a container orchestrator — which cannot
/// know the app's `[env] prefix`, because the prefix is declared inside the
/// app's own `sky.toml`. Namespacing them would break the injector that sets
/// them. `docs/sky-toml.md` enumerates the prefix-affected namespaces and
/// deliberately does not include these.
const FIXED_NAME_READS: &[(&str, &str)] = &[
    ("SKY_ADMIN_TOKEN", "operator-set bearer for /_sky/metrics; set by the deploy, not the app"),
    ("SKY_METRICS_TOKEN", "back-compat alias for SKY_ADMIN_TOKEN"),
    ("SKY_CONSOLE_AUTH", "console gate mode; set by the deploy"),
    ("SKY_CONSOLE_DB_PATH", "console store location; set by the host"),
    ("SKY_CONSOLE_EMBED", "console embed toggle; set by the deploy"),
    ("SKY_CONSOLE_EMBED_ORIGIN", "console iframe allowlist; set by the embedding plane"),
    ("SKY_CONSOLE_HUB_DB", "hub daemon setting; the hub is not a Sky app"),
    ("SKY_CONSOLE_HUB_MAX_PAYLOAD", "hub daemon setting"),
    ("SKY_CONSOLE_HUB_PRUNE_INTERVAL_SECONDS", "hub daemon setting"),
    ("SKY_CONSOLE_HUB_QUIET", "hub daemon setting"),
    ("SKY_CONSOLE_HUB_RETENTION_HOURS", "hub daemon setting"),
    ("SKY_CONSOLE_HUB_TOKEN", "hub daemon setting"),
    ("SKY_CONSOLE_INTERNAL_TOKEN", "console handshake secret; injected by the control plane"),
    ("SKY_CONSOLE_TOKEN", "console bearer; set by the deploy"),
    ("SKY_CONSOLE_TOKEN_SECRET", "back-compat alias for SKY_ADMIN_TOKEN"),
    ("SKY_CONSOLE_URL", "dev-banner link target; set by the launcher"),
    ("SKY_CSRF", "CSRF kill switch; set by the deploy while debugging"),
    ("SKY_DEV_BANNER", "startup-banner toggle; set by the launcher"),
    ("SKY_EMAIL_DRY_RUN", "test-harness switch; set by the runner"),
    ("SKY_INGEST_TOKEN", "OTLP ingest bearer; shared between app and collector"),
    ("SKY_OBSERVABILITY_BUFFER", "push-exporter tuning; set by the collector deploy"),
    ("SKY_OBSERVABILITY_PUSH_INTERVAL_MS", "push-exporter tuning"),
    ("SKY_PARENT_URL", "control-plane callback URL; injected by the plane"),
    ("SKY_PROFILE_DIR", "`sky run --profile` output dir; set by the CLI"),
    ("SKY_PROFILE_TIMEOUT", "`sky run --profile` bound; set by the CLI"),
    ("SKY_RUNTIME_MODE", "serverless/VM hint; set by the platform"),
    ("SKY_SERVICE_NAME", "OTel resource attribute; peer of OTEL_SERVICE_NAME"),
    ("SKY_STREAM_DEBUG", "developer trace switch"),
    ("SKY_TUI_LOG", "developer trace switch"),
    ("SKY_TUI_QUIET", "developer trace switch"),
    ("SKY_WEBVIEW_DEBUG", "developer trace switch"),
];

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

fn is_comment(line: &str) -> bool {
    let t = line.trim_start();
    t.starts_with("//") || t.starts_with('*') || t.starts_with("/*")
}

/// Every `SKY_…` name read through os.Getenv / os.LookupEnv on this line.
fn env_names(line: &str) -> Vec<String> {
    let mut found = Vec::new();
    for call in ["os.Getenv(\"", "os.LookupEnv(\""] {
        let mut rest = line;
        while let Some(i) = rest.find(call) {
            let after = &rest[i + call.len()..];
            if let Some(end) = after.find('"') {
                let name = &after[..end];
                if name.starts_with("SKY_") || name == "ENV" {
                    found.push(name.to_string());
                }
                rest = &after[end..];
            } else {
                break;
            }
        }
    }
    found
}

fn is_prefix_affected(name: &str) -> bool {
    // Bare `ENV` is the unprefixed name a user types; it is the documented
    // first lookup and stays raw. `SKY_ENV` is the namespaced one.
    let Some(suffix) = name.strip_prefix("SKY_") else {
        return false;
    };
    PREFIXED_EXACT.contains(&suffix) || PREFIXED_NAMESPACES.iter().any(|ns| suffix.starts_with(ns))
}

#[test]
fn prefix_affected_env_reads_go_through_sky_getenv() {
    let root = repo_root();
    let runtime = root.join("runtime-go");
    assert!(
        runtime.is_dir(),
        "runtime-go/ not found under {} — the gate cannot establish a verdict",
        root.display()
    );

    let mut files = Vec::new();
    walk_go(&runtime, &mut files);
    assert!(
        files.len() > 20,
        "expected to scan the Go runtime, found only {} files",
        files.len()
    );

    let mut unreachable_under_prefix: Vec<String> = Vec::new();
    let mut unclassified: BTreeSet<String> = BTreeSet::new();
    let allowed: BTreeSet<&str> = FIXED_NAME_READS.iter().map(|(n, _)| *n).collect();

    for f in &files {
        let Ok(src) = fs::read_to_string(f) else { continue };
        for (i, line) in src.lines().enumerate() {
            if is_comment(line) {
                continue;
            }
            for name in env_names(line) {
                if name == "ENV" {
                    continue; // documented unprefixed first lookup
                }
                let rel = f.strip_prefix(&root).unwrap_or(f);
                if is_prefix_affected(&name) {
                    unreachable_under_prefix.push(format!(
                        "{}:{}: {} — use skyGetenv(\"{}\")",
                        rel.display(),
                        i + 1,
                        name,
                        name.trim_start_matches("SKY_")
                    ));
                } else if !allowed.contains(name.as_str()) {
                    unclassified.insert(format!("{} ({}:{})", name, rel.display(), i + 1));
                }
            }
        }
    }

    assert!(
        unreachable_under_prefix.is_empty(),
        "these reads are in a prefix-affected namespace but bypass skyGetenv, so a \
         project with `[env] prefix` cannot reach them at all — the setting silently \
         stays at its default and nothing reports it:\n  {}",
        unreachable_under_prefix.join("\n  ")
    );

    assert!(
        unclassified.is_empty(),
        "these SKY_* reads are neither prefix-affected nor listed in FIXED_NAME_READS:\n  {}\n\n\
         Classify each one: route it through skyGetenv if `[env] prefix` should move it, \
         or add it to FIXED_NAME_READS with the reason it is set from outside the app.",
        unclassified.into_iter().collect::<Vec<_>>().join("\n  ")
    );

    // A stale allowlist hides the next defect: an entry for a variable nobody
    // reads any more looks like coverage and is not.
    let mut read_names: BTreeSet<String> = BTreeSet::new();
    for f in &files {
        let Ok(src) = fs::read_to_string(f) else { continue };
        for line in src.lines() {
            if !is_comment(line) {
                read_names.extend(env_names(line));
            }
        }
    }
    let stale: Vec<&str> = FIXED_NAME_READS
        .iter()
        .map(|(n, _)| *n)
        .filter(|n| !read_names.contains(*n))
        .collect();
    assert!(
        stale.is_empty(),
        "FIXED_NAME_READS lists variables the runtime no longer reads: {stale:?}"
    );
}

//! The one-time "Sky upgraded X -> Y" notice.
//!
//! The first run of a new `sky` version on a machine prints, once, what the
//! upgrade changed without a compile error (the SILENT bullets of the
//! migration guide, embedded at build time by `build.rs` from
//! `docs/migration/v0.27.md`) and the guide's link. The version is recorded
//! in the user cache dir (`~/.cache/sky/last-version`, or
//! `$XDG_CACHE_HOME/sky/`, `%LOCALAPPDATA%\sky\` on Windows), so the next run
//! prints nothing. In `--format json` it is one NDJSON `notice` record on
//! stdout instead of text on stderr, so the stream stays machine-readable.
//! The text goes to stderr only when stderr is a terminal: a run in a script,
//! CI or a pipe neither prints nor records it, so a person still sees it once.
//!
//! It never prints for `sky --version`, `--help`, `sky lsp` (an editor owns
//! that process) or the hidden update-check worker, and nothing is recorded
//! then either, so the next real command still shows it once.

use std::path::PathBuf;
use std::sync::Mutex;

/// The SILENT bullets, from `docs/migration/v0.27.md` (see `build.rs`).
pub const SILENT_BULLETS: &str = include_str!(concat!(env!("OUT_DIR"), "/migration_silent.txt"));

/// A version change to report.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Notice {
    /// The version the cache last recorded; `None` when this machine has no
    /// record (a first install, or an upgrade from a version that kept none).
    pub from: Option<String>,
    pub to: String,
}

static PENDING: Mutex<Option<Notice>> = Mutex::new(None);

/// This binary's version, `sky v<workspace version>`, without the commit a
/// source build carries, so a rebuild at a new commit of the same version
/// does not look like an upgrade. Unlike `sky --version` it never depends on
/// the working directory.
pub fn this_version() -> String {
    format!("sky v{}", env!("CARGO_PKG_VERSION"))
}

/// The file that records the last version run on this machine.
fn record_path() -> Option<PathBuf> {
    let base = if cfg!(windows) {
        std::env::var_os("LOCALAPPDATA").map(PathBuf::from)
    } else {
        std::env::var_os("XDG_CACHE_HOME")
            .filter(|v| !v.is_empty())
            .map(PathBuf::from)
            .or_else(|| std::env::var_os("HOME").map(|h| PathBuf::from(h).join(".cache")))
    };
    base.map(|b| b.join("sky").join("last-version"))
}

/// True for the invocations that never show the notice.
fn is_quiet_verb(verb: Option<&str>) -> bool {
    match verb {
        None | Some("--version" | "-V" | "version" | "--help" | "-h" | "help" | "lsp") => true,
        // Hidden workers (`__update-check`, `__warm-go-cache`, which `sky
        // upgrade` runs on the new binary): recording there would use up the
        // notice before the user's first real command.
        Some(v) => v.starts_with("__"),
    }
}

/// Record `current` and return the notice to show, when this is the first run
/// of `current` on this machine. Recording happens first: a cache that cannot
/// be written shows nothing, rather than the same notice on every run.
pub fn check(verb: Option<&str>, current: &str) -> Option<Notice> {
    if is_quiet_verb(verb) {
        return None;
    }
    let path = record_path()?;
    let last = std::fs::read_to_string(&path)
        .ok()
        .map(|s| s.trim().to_string())
        .filter(|s| !s.is_empty());
    if last.as_deref() == Some(current) {
        return None;
    }
    std::fs::create_dir_all(path.parent()?).ok()?;
    std::fs::write(&path, format!("{current}\n")).ok()?;
    Some(Notice {
        from: last,
        to: current.to_string(),
    })
}

/// Keep `n` for the json stream ([`take_pending`]).
pub fn set_pending(n: Notice) {
    if let Ok(mut p) = PENDING.lock() {
        *p = Some(n);
    }
}

/// The notice a json command emits as its `notice` record, once.
pub fn take_pending() -> Option<Notice> {
    PENDING.lock().ok()?.take()
}

/// The text the notice prints on stderr.
pub fn text(n: &Notice) -> String {
    let head = match &n.from {
        Some(from) => format!("Sky upgraded {from} -> {}.", n.to),
        None => format!("Sky upgraded to {}.", n.to),
    };
    format!(
        "{head} Changes that compile but behave differently:\n{SILENT_BULLETS}\nThe full migration guide: {}\n",
        project::MIGRATION_GUIDE
    )
}

/// The NDJSON `notice` record (see `json_out`).
pub fn json_line(n: &Notice) -> String {
    let bullets: Vec<String> = SILENT_BULLETS
        .lines()
        .map(|l| crate::json_out::enc(l.trim_start_matches("- ")))
        .collect();
    crate::json_out::obj(&[
        ("kind", crate::json_out::enc("notice")),
        ("schema", crate::json_out::SCHEMA.to_string()),
        (
            "from",
            n.from
                .as_deref()
                .map(crate::json_out::enc)
                .unwrap_or_else(|| "null".to_string()),
        ),
        ("to", crate::json_out::enc(&n.to)),
        ("message", crate::json_out::enc(&text(n))),
        ("silent", format!("[{}]", bullets.join(","))),
        ("guide", crate::json_out::enc(project::MIGRATION_GUIDE)),
    ])
}

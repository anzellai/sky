#![forbid(unsafe_code)]
//! `sky` — the CLI binary: a thin front-end over the shared `project` build
//! driver (doc 01, doc 10). The same engine the LSP and `xtask` drive; this
//! binary just resolves a `<file>` argument to a project + repo root and calls
//! `project::build_example` / `build_project`, then formats/runs/tests as the
//! verb dictates. `sky check` ≡ `sky build` minus running (both run `go build`).

use std::io::{IsTerminal, Read};
use std::path::{Path, PathBuf};
use std::process::{Command, ExitCode, Stdio};

mod db_cluster;
mod db_embed;
mod db_migrate;
mod db_pool_sizing;
mod db_provision;
/// The shared host cluster (`sky db provision --shared`, phase 6) speaks the
/// PostgreSQL wire protocol over a **unix domain socket** (see [`pg_wire`]),
/// so the whole module is unix-only. On non-unix a stub keeps the CLI verb
/// dispatchable — [`db_shared::cmd_shared`] exists and returns a clear error.
#[cfg(unix)]
mod db_shared;
#[cfg(not(unix))]
#[path = "db_shared_windows.rs"]
mod db_shared;
/// Test-only: the one place a live test is allowed to not run. Also `#[path]`-
/// included by the integration tests under `tests/`, which cannot import from a
/// binary crate.
#[cfg(test)]
mod live_gate;
mod pg_managed_conf;
/// A PostgreSQL wire-protocol client over a **unix domain socket**, used only
/// by the shared host cluster (phase 6). `std::os::unix` does not exist on
/// Windows, so the module — and every path that reaches it — is unix-only.
#[cfg(unix)]
mod pg_wire;
use std::time::{Duration, Instant};

use fmt::{format_source, is_formatted};
use project::{
    assets_root_for, build_example, build_project, is_compiler_repo_root, project_dir_for,
    repo_root_for, run_app, BuildOptions,
};
use testrunner::run_test;

mod bundled;

fn main() -> ExitCode {
    let args: Vec<String> = std::env::args().skip(1).collect();

    // Hidden worker: refresh the update-check cache, then exit. Spawned detached
    // by `maybe_notify_update`; never user-invoked (absent from help).
    if args.first().map(String::as_str) == Some("__update-check") {
        run_update_check_refresh();
        return ExitCode::SUCCESS;
    }
    // Best-effort "newer version available" nudge — cached, non-blocking, TTY-only.
    maybe_notify_update(args.first().map(String::as_str));

    match args.first().map(String::as_str) {
        Some("--version") | Some("-V") | Some("version") => {
            println!("{}", version_string());
            ExitCode::SUCCESS
        }
        Some("--help") | Some("-h") | Some("help") | None => {
            print_help();
            ExitCode::SUCCESS
        }
        Some("build") => cmd_build(&args[1..], /*check_only=*/ false),
        Some("check") => cmd_build(&args[1..], /*check_only=*/ true),
        Some("run") => cmd_run(&args[1..]),
        Some("fmt") => cmd_fmt(&args[1..]),
        Some("test") => cmd_test(&args[1..]),
        Some("lsp") => cmd_lsp(&args[1..]),
        Some("clean") => cmd_clean(&args[1..]),
        Some("init") => cmd_init(&args[1..]),
        Some("doc") => cmd_doc(&args[1..]),
        Some("watch") => cmd_watch(&args[1..]),
        Some("config") => cmd_config(&args[1..]),
        Some("db") => cmd_db(&args[1..]),
        Some("add") => cmd_add(&args[1..]),
        Some("remove") => cmd_remove(&args[1..]),
        Some("install") => cmd_install(&args[1..]),
        Some("update") => cmd_update(&args[1..]),
        // Rust-native verbs (no bundled Sky app needed): project/environment
        // health, template refresh, and build+run verification.
        Some("doctor") => cmd_doctor(&args[1..]),
        Some("upgrade-claude") => cmd_upgrade_claude(&args[1..]),
        Some("verify") => cmd_verify(&args[1..]),
        // Bundled-app verbs: build + spawn a bundled Sky/Go app from the repo
        // tree (`console`/`console-serve`/`doc --serve`/`doc --tui`).
        Some("console") => cmd_console(&args[1..]),
        Some("console-serve") => cmd_console_serve(&args[1..]),
        Some("upgrade") => cmd_upgrade(&args[1..]),
        Some(other) => {
            eprintln!("sky: unknown command `{other}`. Try `sky --help`.");
            ExitCode::from(2)
        }
    }
}

/// `sky upgrade [--force]` — self-update the `sky` binary from the latest GitHub
/// release (`anzellai/sky`), mirroring the Haskell `sky`. Resolves the platform
/// asset (`sky-darwin-arm64` / `sky-linux-x64` / `sky-linux-arm64` /
/// `sky-windows-x64`, per `.github/workflows/release.yml`), downloads the
/// packaged tarball, and atomically replaces the running binary in place.
///
/// A dev build refuses by default (self-replacing a local dev binary with a
/// published release would throw away local work); `--force` overrides for users
/// who explicitly want the latest published binary.
fn cmd_upgrade(args: &[String]) -> ExitCode {
    let force = args.iter().any(|a| a == "--force");
    // `--notes` previews the release notes for (current, latest] WITHOUT upgrading.
    let notes_only = args.iter().any(|a| a == "--notes");
    let ver = version_string();
    let is_dev = ver == "sky dev" || ver.contains("dev");
    let current_tuple = parse_semver(ver.trim_start_matches("sky v"));

    println!("sky upgrade — current version: {ver}");

    // `sky upgrade --notes` — just show what changed, don't touch the binary.
    if notes_only {
        let tag = match latest_release_tag() {
            Ok(t) => t,
            Err(e) => {
                eprintln!("sky upgrade --notes: {e}");
                return ExitCode::FAILURE;
            }
        };
        let Some(to) = parse_semver(&tag) else {
            eprintln!("sky upgrade --notes: could not parse latest tag `{tag}`");
            return ExitCode::FAILURE;
        };
        match fetch_releases() {
            Ok(rels) => {
                if current_tuple == Some(to) {
                    println!("Already on the latest release ({tag}). Recent notes:");
                    print_release_notes(&rels, None, to);
                } else {
                    print_release_notes(&rels, current_tuple, to);
                }
                ExitCode::SUCCESS
            }
            Err(e) => {
                eprintln!("sky upgrade --notes: {e}");
                ExitCode::FAILURE
            }
        }
    } else {
        cmd_upgrade_install(args, force, ver, is_dev, current_tuple)
    }
}

/// The install path of `sky upgrade` (factored out so `--notes` can short-circuit
/// above without the download machinery).
fn cmd_upgrade_install(
    _args: &[String],
    force: bool,
    ver: String,
    is_dev: bool,
    current_tuple: Option<(u32, u32, u32)>,
) -> ExitCode {

    let Some(artifact) = platform_artifact() else {
        eprintln!(
            "sky upgrade: no prebuilt binary is published for this platform ({}/{}).\n\
             Build from source in the sky repo:  cargo build -p sky --release --bin sky",
            std::env::consts::OS,
            std::env::consts::ARCH,
        );
        return ExitCode::FAILURE;
    };

    if is_dev && !force {
        println!(
            "This is a rewrite/dev build of the Rust `sky`, not a published release.\n\
             Rebuild from source (in the sky repo):  cargo build -p sky --release --bin sky\n\
             Or run `sky upgrade --force` to install the latest published release anyway."
        );
        return ExitCode::SUCCESS;
    }

    println!("Checking the latest release on github.com/anzellai/sky …");
    let tag = match latest_release_tag() {
        Ok(t) => t,
        Err(e) => {
            eprintln!(
                "sky upgrade: {e}\n\
                 Download the latest release manually from \
                 https://github.com/anzellai/sky/releases"
            );
            return ExitCode::FAILURE;
        }
    };

    // Skip the download when the running binary is already the latest tag (a
    // forced dev upgrade always proceeds so `--force` is never a silent no-op).
    let latest = format!("sky v{}", tag.trim_start_matches('v'));
    if !is_dev && ver == latest {
        println!("Already up to date ({ver}). Run `sky upgrade --notes` to review recent release notes.");
        return ExitCode::SUCCESS;
    }

    println!("Downloading {artifact} @ {tag} …");
    match download_and_replace_binary(&tag, artifact) {
        Ok(dest) => {
            println!("Upgraded to {tag} — {}", dest.display());
            // Print the notes for every version between the old binary and the new
            // one (best-effort — never fail the upgrade if the notes fetch fails).
            if let (Ok(rels), Some(to)) = (fetch_releases(), parse_semver(&tag)) {
                print_release_notes(&rels, current_tuple, to);
            }
            ExitCode::SUCCESS
        }
        Err(e) => {
            eprintln!(
                "sky upgrade: {e}\n\
                 Download {artifact} from \
                 https://github.com/anzellai/sky/releases/tag/{tag} and replace the binary \
                 manually."
            );
            ExitCode::FAILURE
        }
    }
}

/// The published release asset base-name for the host platform, or `None` when
/// no prebuilt binary is published (build-from-source path). Matches the
/// `matrix.artifact` values in `.github/workflows/release.yml`.
fn platform_artifact() -> Option<&'static str> {
    match (std::env::consts::OS, std::env::consts::ARCH) {
        ("macos", "aarch64") => Some("sky-darwin-arm64"),
        ("linux", "x86_64") => Some("sky-linux-x64"),
        ("linux", "aarch64") => Some("sky-linux-arm64"),
        ("windows", "x86_64") => Some("sky-windows-x64"),
        _ => None,
    }
}

/// Query the GitHub API for the latest `anzellai/sky` release tag. Shells out to
/// `curl` (ubiquitous on macOS + Linux) rather than pulling a TLS stack into the
/// compiler. Returns the `tag_name` (e.g. `v0.18.0`).
fn latest_release_tag() -> Result<String, String> {
    let out = std::process::Command::new("curl")
        .args([
            "-fsSL",
            "--max-time",
            "10",
            "-H",
            "Accept: application/vnd.github+json",
            "https://api.github.com/repos/anzellai/sky/releases/latest",
        ])
        .output()
        .map_err(|e| format!("could not run curl ({e}); is curl installed?"))?;
    if !out.status.success() {
        return Err("could not reach the GitHub releases API".into());
    }
    let body = String::from_utf8_lossy(&out.stdout);
    // Minimal field extraction (no serde): find `"tag_name"` then its string
    // value. The API response always quotes both key and value.
    json_string_field(&body, "tag_name")
        .ok_or_else(|| "no `tag_name` in the GitHub API response".to_string())
}

/// Extract a top-level `"key": "value"` string from a JSON blob without a JSON
/// dependency. Returns the first match's value.
fn json_string_field(json: &str, key: &str) -> Option<String> {
    let needle = format!("\"{key}\"");
    let start = json.find(&needle)? + needle.len();
    let rest = &json[start..];
    let colon = rest.find(':')?;
    let after = &rest[colon + 1..];
    let q1 = after.find('"')?;
    let after_q1 = &after[q1 + 1..];
    let q2 = after_q1.find('"')?;
    Some(after_q1[..q2].to_string())
}

/// A GitHub release, for printing notes on upgrade. `body` is the release's
/// markdown notes.
#[derive(serde::Deserialize)]
struct GhRelease {
    tag_name: String,
    #[serde(default)]
    body: String,
    #[serde(default)]
    prerelease: bool,
    #[serde(default)]
    draft: bool,
}

/// Parse a `vMAJOR.MINOR.PATCH` (or bare `MAJOR.MINOR.PATCH`) tag into a
/// comparable tuple. Extra suffixes (`-rc1`) are ignored on the patch.
fn parse_semver(tag: &str) -> Option<(u32, u32, u32)> {
    let t = tag.trim().trim_start_matches('v');
    let mut it = t.split('.');
    let major = it.next()?.parse().ok()?;
    let minor = it.next()?.parse().ok()?;
    let patch_field = it.next().unwrap_or("0");
    // strip any `-rc1` / `+meta` suffix from the patch
    let patch = patch_field
        .split(|c: char| c == '-' || c == '+')
        .next()
        .unwrap_or("0")
        .parse()
        .ok()?;
    Some((major, minor, patch))
}

/// Fetch every published `anzellai/sky` release (for notes). Best-effort — a
/// failure returns `Err` and callers just skip printing notes.
fn fetch_releases() -> Result<Vec<GhRelease>, String> {
    let out = std::process::Command::new("curl")
        .args([
            "-fsSL",
            "-H",
            "Accept: application/vnd.github+json",
            "https://api.github.com/repos/anzellai/sky/releases?per_page=100",
        ])
        .output()
        .map_err(|e| format!("could not run curl ({e})"))?;
    if !out.status.success() {
        return Err("could not reach the GitHub releases API".into());
    }
    serde_json::from_slice::<Vec<GhRelease>>(&out.stdout)
        .map_err(|e| format!("could not parse releases: {e}"))
}

/// Print the notes for every release in `(from, to]` (ascending), so a
/// multi-version jump surfaces every intervening changelog. `from = None` (a dev
/// build with no known version) prints only the target `to`'s notes. Flags any
/// release whose notes carry a Breaking / Migration heading.
fn print_release_notes(releases: &[GhRelease], from: Option<(u32, u32, u32)>, to: (u32, u32, u32)) {
    let mut in_range: Vec<&GhRelease> = releases
        .iter()
        .filter(|r| !r.draft)
        .filter_map(|r| parse_semver(&r.tag_name).map(|v| (v, r)))
        .filter(|(v, _)| {
            *v <= to
                && match from {
                    Some(f) => *v > f,
                    // dev / unknown current: only show the exact target
                    None => *v == to,
                }
        })
        .collect::<Vec<_>>()
        .into_iter()
        .map(|(_, r)| r)
        .collect();
    in_range.sort_by_key(|r| parse_semver(&r.tag_name).unwrap_or((0, 0, 0)));
    if in_range.is_empty() {
        return;
    }
    let n = in_range.len();
    println!(
        "\n══════════ release notes ({} release{}) ══════════",
        n,
        if n == 1 { "" } else { "s" }
    );
    for r in in_range {
        println!("\n### {}{}", r.tag_name, if r.prerelease { "  (pre-release)" } else { "" });
        if body_has_breaking(&r.body) {
            println!("⚠  contains BREAKING changes / a migration section — read before deploying.");
        }
        let body = r.body.trim();
        if body.is_empty() {
            println!("(no notes)");
        } else {
            println!("{body}");
        }
    }
    println!("\n════════════════════════════════════════════════");
}

/// True when the notes contain a markdown heading whose text mentions a breaking
/// change or a migration.
fn body_has_breaking(body: &str) -> bool {
    body.lines().any(|l| {
        let t = l.trim_start();
        if !t.starts_with('#') {
            return false;
        }
        let lower = t.trim_start_matches('#').to_lowercase();
        lower.contains("breaking") || lower.contains("migrat")
    })
}

// ---- background update check (nudge, cached) -----------------------------

/// Refresh + nudge intervals. The cache is refreshed at most once per
/// `CHECK_INTERVAL`, and the "upgrade available" line prints at most once per
/// `NUDGE_INTERVAL`, so neither the GitHub API nor the user is hammered.
const CHECK_INTERVAL_SECS: u64 = 24 * 60 * 60;
const NUDGE_INTERVAL_SECS: u64 = 24 * 60 * 60;

/// Persisted update-check state (`~/.cache/sky/update-check.json`).
#[derive(serde::Serialize, serde::Deserialize, Default, Clone)]
struct UpdateCache {
    #[serde(default)]
    last_check: u64,
    #[serde(default)]
    last_nudge: u64,
    #[serde(default)]
    latest: Option<String>,
}

fn unix_now() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
}

/// `~/.cache/sky/update-check.json` (XDG on Linux, `%LOCALAPPDATA%\sky` on
/// Windows). `None` when no home/cache dir is discoverable — the check simply
/// no-ops then.
fn update_cache_path() -> Option<PathBuf> {
    let base = if cfg!(windows) {
        std::env::var_os("LOCALAPPDATA").map(PathBuf::from)
    } else {
        std::env::var_os("XDG_CACHE_HOME")
            .map(PathBuf::from)
            .or_else(|| std::env::var_os("HOME").map(|h| PathBuf::from(h).join(".cache")))
    };
    base.map(|b| b.join("sky").join("update-check.json"))
}

fn read_update_cache() -> Option<UpdateCache> {
    let s = std::fs::read_to_string(update_cache_path()?).ok()?;
    serde_json::from_str(&s).ok()
}

fn write_update_cache(c: &UpdateCache) {
    let Some(path) = update_cache_path() else { return };
    if let Some(dir) = path.parent() {
        let _ = std::fs::create_dir_all(dir);
    }
    if let Ok(s) = serde_json::to_string(c) {
        let _ = std::fs::write(path, s);
    }
}

/// Pure: is a newer version known, and have we held off nudging long enough?
fn should_nudge(
    current: (u32, u32, u32),
    latest: (u32, u32, u32),
    last_nudge: u64,
    now: u64,
) -> bool {
    latest > current && now.saturating_sub(last_nudge) >= NUDGE_INTERVAL_SECS
}

/// Pure: the nudge line to print (or `None`), given the current version, the
/// cache, and the clock. Factored out so the visible message is unit-testable
/// without a TTY / network.
fn nudge_line(
    current: (u32, u32, u32),
    current_display: &str,
    cache: &UpdateCache,
    now: u64,
) -> Option<String> {
    let latest_str = cache.latest.as_ref()?;
    let latest = parse_semver(latest_str)?;
    if !should_nudge(current, latest, cache.last_nudge, now) {
        return None;
    }
    Some(format!(
        "\n  A new sky release is available: {current_display} \u{2192} {latest_str}\n  \
         Run `sky upgrade` to update (`sky upgrade --notes` to see what's new).\n"
    ))
}

/// Pure: is the cached check old enough to refresh?
fn cache_is_stale(last_check: u64, now: u64) -> bool {
    now.saturating_sub(last_check) >= CHECK_INTERVAL_SECS
}

/// Best-effort "a newer sky is available" nudge. Never blocks (the network
/// refresh runs in a detached child; the nudge prints from the cached result of a
/// prior refresh) and never perturbs machine-readable output — it prints to
/// stderr, only when stderr is a TTY, only for a released build, and never for
/// commands whose I/O must stay clean (`lsp`, `fmt`, `--version`, `upgrade`).
/// `SKY_NO_UPDATE_CHECK` disables it entirely.
fn maybe_notify_update(cmd: Option<&str>) {
    if std::env::var_os("SKY_NO_UPDATE_CHECK").is_some() {
        return;
    }
    if !std::io::stderr().is_terminal() {
        return;
    }
    match cmd {
        // Skip: stdio-protocol / machine output / self-referential / no-op.
        Some("lsp") | Some("fmt") | Some("upgrade") | Some("--version") | Some("-V")
        | Some("version") | Some("--help") | Some("-h") | Some("help")
        | Some("__update-check") | None => return,
        _ => {}
    }
    let ver = version_string();
    if ver == "sky dev" || ver.contains("dev") {
        return; // a dev build has no meaningful published version to compare
    }
    let Some(current) = parse_semver(ver.trim_start_matches("sky v")) else {
        return;
    };

    let cache = read_update_cache();
    let now = unix_now();

    // Nudge from the cached latest (rate-limited).
    if let Some(c) = cache.as_ref() {
        if let Some(msg) = nudge_line(current, ver.trim_start_matches("sky "), c, now) {
            eprint!("{msg}");
            let mut updated = c.clone();
            updated.last_nudge = now;
            write_update_cache(&updated);
        }
    }

    // Refresh the cache in the background when stale. Optimistically bump
    // `last_check` first so concurrent invocations don't all spawn a worker.
    let last_check = cache.as_ref().map(|c| c.last_check).unwrap_or(0);
    if cache_is_stale(last_check, now) {
        let mut c = cache.clone().unwrap_or_default();
        c.last_check = now;
        write_update_cache(&c);
        spawn_background_update_check();
    }
}

/// Fire-and-forget: re-invoke this binary's hidden `__update-check` worker,
/// detached, stdio to null, so the network fetch runs without blocking or
/// touching the terminal.
fn spawn_background_update_check() {
    if let Ok(exe) = std::env::current_exe() {
        let _ = Command::new(exe)
            .arg("__update-check")
            .stdin(std::process::Stdio::null())
            .stdout(std::process::Stdio::null())
            .stderr(std::process::Stdio::null())
            .spawn();
    }
}

/// The hidden `__update-check` worker: fetch the latest tag, write the cache,
/// exit. All best-effort; `last_check` is bumped even on failure so a persistent
/// network problem doesn't respawn a worker on every invocation.
fn run_update_check_refresh() {
    let mut c = read_update_cache().unwrap_or_default();
    c.last_check = unix_now();
    if let Ok(tag) = latest_release_tag() {
        c.latest = Some(tag.trim_start_matches('v').to_string());
    }
    write_update_cache(&c);
}

/// Download the release tarball for `artifact` @ `tag`, extract the binary, and
/// atomically replace the running executable. Returns the replaced path.
fn download_and_replace_binary(tag: &str, artifact: &str) -> Result<PathBuf, String> {
    let is_windows = artifact.contains("windows");
    if is_windows {
        return Err(
            "in-place self-update is not supported on Windows (a running .exe can't be \
             replaced); download and swap the binary manually"
                .into(),
        );
    }
    let url = format!("https://github.com/anzellai/sky/releases/download/{tag}/{artifact}.tar.gz");
    let tmp = std::env::temp_dir().join(format!("sky-upgrade-{}", std::process::id()));
    std::fs::create_dir_all(&tmp).map_err(|e| format!("could not create temp dir: {e}"))?;
    let archive = tmp.join(format!("{artifact}.tar.gz"));

    // Download the tarball.
    let dl = std::process::Command::new("curl")
        .args(["-fSL", "-o"])
        .arg(&archive)
        .arg(&url)
        .status()
        .map_err(|e| format!("could not run curl ({e})"))?;
    if !dl.success() {
        let _ = std::fs::remove_dir_all(&tmp);
        return Err(format!("download failed ({url})"));
    }

    // Extract (tarball holds `<artifact>` + `sky-ffi-inspect-<artifact>`).
    let ex = std::process::Command::new("tar")
        .arg("xzf")
        .arg(&archive)
        .arg("-C")
        .arg(&tmp)
        .status()
        .map_err(|e| format!("could not run tar ({e})"))?;
    if !ex.success() {
        let _ = std::fs::remove_dir_all(&tmp);
        return Err("could not extract the release tarball".into());
    }

    let new_bin = tmp.join(artifact);
    if !new_bin.is_file() {
        let _ = std::fs::remove_dir_all(&tmp);
        return Err(format!("release tarball did not contain `{artifact}`"));
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = std::fs::set_permissions(&new_bin, std::fs::Permissions::from_mode(0o755));
    }

    // Atomically replace the running executable. On Unix, renaming over the
    // running binary is safe (the live process keeps its open inode). Stage the
    // new binary as a sibling of the destination so the final rename is
    // same-filesystem (atomic); fall back to a copy across filesystems.
    let cur = std::env::current_exe().map_err(|e| format!("could not locate current exe: {e}"))?;
    let staged = cur.with_extension("sky-upgrade-new");
    if std::fs::rename(&new_bin, &staged).is_err() {
        std::fs::copy(&new_bin, &staged).map_err(|e| {
            format!(
                "could not stage the new binary next to {}: {e}",
                cur.display()
            )
        })?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let _ = std::fs::set_permissions(&staged, std::fs::Permissions::from_mode(0o755));
        }
    }
    std::fs::rename(&staged, &cur).map_err(|e| {
        let _ = std::fs::remove_file(&staged);
        format!(
            "could not replace {} ({e}); you may need elevated permissions",
            cur.display()
        )
    })?;
    let _ = std::fs::remove_dir_all(&tmp);
    Ok(cur)
}

// ---- build / check -------------------------------------------------------

/// Resolve the entry `.sky` when a command is invoked with no file argument:
/// walk up from the current directory to the nearest `sky.toml`, read its
/// `entry` field (default `src/Main.sky`), and return that path. `Ok(None)`
/// means no `sky.toml` was found; `Err` means one was found but its entry file
/// is missing.
fn entry_from_sky_toml() -> Result<Option<PathBuf>, PathBuf> {
    let Ok(cwd) = std::env::current_dir() else {
        return Ok(None);
    };
    let mut dir = cwd.as_path();
    loop {
        let manifest = dir.join("sky.toml");
        if manifest.is_file() {
            let entry_rel = std::fs::read_to_string(&manifest)
                .ok()
                .and_then(|s| parse_toml_entry(&s))
                .unwrap_or_else(|| "src/Main.sky".to_string());
            let entry = dir.join(entry_rel);
            return if entry.is_file() {
                Ok(Some(entry))
            } else {
                Err(entry)
            };
        }
        match dir.parent() {
            Some(p) => dir = p,
            None => return Ok(None),
        }
    }
}

/// Extract the top-level `entry = "..."` value from a `sky.toml` (the entry key
/// lives above any `[section]` header).
fn parse_toml_entry(toml: &str) -> Option<String> {
    for line in toml.lines() {
        let line = line.trim();
        if line.starts_with('[') {
            break;
        }
        if let Some(rest) = line.strip_prefix("entry") {
            let val = rest
                .trim_start()
                .strip_prefix('=')?
                .trim()
                .trim_matches(|c| c == '"' || c == '\'');
            if !val.is_empty() {
                return Some(val.to_string());
            }
        }
    }
    None
}

/// Resolve the positional entry file, falling back to `sky.toml`'s `entry` when
/// omitted. Returns the exit code to use on failure.
fn resolve_entry_arg(positional: &[String], usage: &str) -> Result<PathBuf, ExitCode> {
    if let Some(f) = positional.first() {
        return Ok(PathBuf::from(f));
    }
    match entry_from_sky_toml() {
        Ok(Some(entry)) => Ok(entry),
        Ok(None) => {
            eprintln!("{usage}");
            Err(ExitCode::from(2))
        }
        Err(missing) => {
            eprintln!("sky: sky.toml entry '{}' not found", missing.display());
            Err(ExitCode::FAILURE)
        }
    }
}

/// `sky build <file>` (and, with `check_only`, `sky check <file>`). Both emit Go
/// and run `go build`; build reports the produced binary, check reports "No
/// errors found." and never runs the program — the `sky check ≡ sky build`
/// invariant (doc 10).
/// The entry module's declared name, from its `module <Name> exposing …` header
/// — so a renamed entry module (`module App`, not `Main`) still builds. `None`
/// when the file is unreadable or has no header (the build falls back to the
/// `Main` heuristic).
fn entry_module_name(file: &Path) -> Option<String> {
    let text = std::fs::read_to_string(file).ok()?;
    for line in text.lines() {
        if let Some(rest) = line.trim_start().strip_prefix("module ") {
            let name = rest.split_whitespace().next().unwrap_or("");
            if !name.is_empty() {
                return Some(name.to_string());
            }
        }
    }
    None
}

fn cmd_build(args: &[String], check_only: bool) -> ExitCode {
    let (positional, out_override) = parse_out(args);
    let embed = args.iter().any(|a| a == "--embed");
    // Sky.Spa client build: `--wasm` compiles the emitted Go for the browser
    // (GOOS=js GOARCH=wasm) + drops wasm_exec.js; `--target <t>` bundles that
    // client for a delivery surface (web / desktop / ios / android). See
    // `cmd_build_target`.
    let wasm = args.iter().any(|a| a == "--wasm");
    let target = args
        .iter()
        .position(|a| a == "--target")
        .and_then(|i| args.get(i + 1))
        .cloned();
    if let Some(t) = &target {
        if !matches!(t.as_str(), "web" | "desktop" | "ios" | "android" | "tablet") {
            eprintln!(
                "sky build --target: unknown target `{t}`\n  \
                 supported: web · desktop · ios · android · tablet (= responsive web)"
            );
            return ExitCode::FAILURE;
        }
        // Every Sky.Spa delivery target is a wasm client under a native/browser
        // shell, so --target implies --wasm.
        //
        // Verify the platform toolchain BEFORE the (slower) wasm build, so a
        // missing SDK is the first thing the user sees — not a half-built bundle.
        let toolchain = match t.as_str() {
            "ios" => detect_ios_toolchain(),
            "android" => detect_android_toolchain(),
            _ => Ok(()),
        };
        if let Err(hint) = toolchain {
            eprintln!("{hint}");
            return ExitCode::FAILURE;
        }
    }
    let wasm = wasm || target.is_some();
    let file = match resolve_entry_arg(
        &positional,
        &format!(
            "usage: sky {} <file.sky> [--out <dir>]  (or run inside a project directory with a sky.toml)",
            verb(check_only)
        ),
    ) {
        Ok(f) => f,
        Err(code) => return code,
    };
    let file = file.as_path();
    let Some((repo_root, project_dir)) = resolve(file) else {
        return ExitCode::FAILURE;
    };
    // Repo-root guard: refuse to write sky-out/ into the compiler repo root,
    // which would overwrite the oracle binary kept there.
    if is_compiler_repo_root(&project_dir) && out_override.is_none() {
        eprintln!(
            "sky {}: refusing to run from the Sky compiler repo root\n\
             (output would overwrite sky-out/).\n\
             cd into an example or user project first, e.g.\n  \
             cd examples/01-hello-world && sky {} src/Main.sky",
            verb(check_only),
            verb(check_only),
        );
        return ExitCode::FAILURE;
    }

    // `--embed` is resolved BEFORE anything is compiled. Acquiring the bundle
    // can mean a download, and finding out at the far end of a build that the
    // target platform has no PostgreSQL published for it is the wrong end.
    let embed_bundle = if embed {
        let platform = match db_embed::target_platform() {
            Ok(p) => p,
            Err(e) => {
                eprintln!("{e}");
                return ExitCode::FAILURE;
            }
        };
        match db_embed::resolve_bundle_archive(&project_dir, platform) {
            Ok(p) => Some(p),
            Err(e) => {
                eprintln!("{e}");
                return ExitCode::FAILURE;
            }
        }
    } else {
        None
    };

    let out_dir_name = out_override.unwrap_or_else(|| "sky-out".to_string());
    let opts = BuildOptions {
        repo_root,
        example_dir: project_dir.clone(),
        out_dir_name: out_dir_name.clone(),
        out_dir_abs: None,
        run: false,
        stdin: None,
        entry_module: entry_module_name(file),
        progress: true,
        embed_bundle,
        wasm,
    };
    let report = build_example(&opts);
    for w in &report.warnings {
        eprintln!("warning: {w}");
    }
    // The legacy→`withX` migration LIST (design §8.2): printed on the same
    // stderr channel as the warnings above, self-extinguishing (silent once the
    // keys are gone). Not `warning:`-prefixed — it is a distinct block a user
    // reads to act, and the three classes inside it (moved / removed / changed)
    // are already visually distinct.
    if let Some(hint) = &report.migration_hint {
        eprintln!("\n{hint}\n");
    }
    if !report.emitted {
        eprintln!("sky {}: {}", verb(check_only), report.note);
        return ExitCode::FAILURE;
    }
    println!(
        "{}",
        if wasm {
            "Building wasm client (GOOS=js GOARCH=wasm)..."
        } else {
            "Running go build..."
        }
    );
    if !report.go_build_ok {
        if check_only {
            eprintln!(
                "Codegen produced Go that `go build` rejects.\n\
                 This is a compiler-side bug — the Sky type system accepted the\n\
                 program but Go did not.\n\nGo errors:\n{}",
                report.go_build_stderr
            );
        } else if wasm {
            eprintln!("wasm build failed:\n{}", report.go_build_stderr);
        } else {
            eprintln!("go build failed:\n{}", report.go_build_stderr);
        }
        return ExitCode::FAILURE;
    }
    if let Some(note) = &report.cgo_note {
        println!("go build {note}");
    }
    if check_only {
        println!("No errors found.");
        return ExitCode::SUCCESS;
    }
    println!("Compilation successful");
    let out_dir = opts
        .out_dir_abs
        .clone()
        .unwrap_or_else(|| project_dir.join(&out_dir_name));
    if wasm {
        println!("Build complete: {out_dir_name}/main.wasm  (+ {out_dir_name}/wasm_exec.js)");
    } else {
        let bin_name = project::configured_bin_name(&project_dir);
        println!("Build complete: {out_dir_name}/{bin_name}");
    }
    // `--target`: bundle the freshly-built wasm client for a delivery surface.
    if let Some(t) = &target {
        return cmd_build_target(t, &project_dir, &out_dir);
    }
    ExitCode::SUCCESS
}

/// `sky build --target <t>`: take the freshly-built wasm client (`out_dir`) and
/// stage a servable web bundle in `<project>/dist/` (index.html + main.wasm +
/// wasm_exec.js), then, per delivery surface, either finish (web/tablet), point
/// the user at the native shell (desktop), or — for ios/android — verify the
/// platform toolchain is installed, warning + exiting if it is not.
fn cmd_build_target(target: &str, project_dir: &Path, out_dir: &Path) -> ExitCode {
    // (Platform toolchains for ios/android were verified in cmd_build before the
    // build ran — see the --target parse block there.)
    // Stage the servable bundle (shared by every surface).
    let dist = project_dir.join("dist");
    if let Err(e) = stage_web_bundle(out_dir, &dist) {
        eprintln!("sky build --target {target}: {e}");
        return ExitCode::FAILURE;
    }
    let dist_name = "dist";
    println!("Bundled web client → {dist_name}/ (index.html + main.wasm + wasm_exec.js)");

    match target {
        "web" | "tablet" => {
            println!(
                "\nServe it with any static host, or from a Sky backend:\n  \
                 Server.static \"/\" \"../{dist_name}\"   -- serves the client same-origin with your /api routes\n\
                 (tablet == responsive web — Std.Ui adapts to the viewport)"
            );
        }
        "desktop" => {
            println!(
                "\nWrap it in a native desktop window with a tiny Sky.Webview shell:\n  \
                 Webview.url \"http://127.0.0.1:<port>/\" (Webview.defaultWindow |> Webview.withTitle \"App\")\n\
                 point it at the backend that serves this bundle. macOS/Windows/Linux."
            );
        }
        "ios" => {
            println!(
                "\niOS toolchain OK. Host the bundle from your backend and load it in a\n\
                 WKWebView app (see examples/60-spa-todos/mobile-ios for the shell + build)."
            );
        }
        "android" => {
            println!(
                "\nAndroid toolchain OK. Host the bundle from your backend and load it in a\n\
                 WebView app (see examples/60-spa-todos/mobile-android + build-apk.sh)."
            );
        }
        _ => {}
    }
    ExitCode::SUCCESS
}

/// Copy `main.wasm` + `wasm_exec.js` from the wasm build dir into `dist/`, and
/// write the standard Go-wasm `index.html` bootstrap if one isn't already there.
fn stage_web_bundle(out_dir: &Path, dist: &Path) -> Result<(), String> {
    std::fs::create_dir_all(dist).map_err(|e| format!("create {}: {e}", dist.display()))?;
    for f in ["main.wasm", "wasm_exec.js"] {
        let src = out_dir.join(f);
        if !src.exists() {
            return Err(format!(
                "{f} not found in {} — did the wasm build run? (this is an internal error)",
                out_dir.display()
            ));
        }
        std::fs::copy(&src, dist.join(f)).map_err(|e| format!("copy {f}: {e}"))?;
    }
    let index = dist.join("index.html");
    if !index.exists() {
        std::fs::write(&index, WASM_INDEX_HTML).map_err(|e| format!("write index.html: {e}"))?;
    }
    Ok(())
}

const WASM_INDEX_HTML: &str = r#"<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>Sky.Spa</title>
  </head>
  <body>
    <div id="app"></div>
    <script src="wasm_exec.js"></script>
    <script>
      const go = new Go();
      WebAssembly.instantiateStreaming(fetch("main.wasm"), go.importObject).then((res) => {
        go.run(res.instance);
      });
    </script>
  </body>
</html>
"#;

/// Verify an iOS build toolchain is present (full Xcode + the iPhone Simulator
/// SDK), returning an actionable install message otherwise.
fn detect_ios_toolchain() -> Result<(), String> {
    let ok = Command::new("xcrun")
        .args(["--sdk", "iphonesimulator", "--show-sdk-path"])
        .output()
        .map(|o| o.status.success() && !o.stdout.is_empty())
        .unwrap_or(false);
    if ok {
        Ok(())
    } else {
        Err("sky build --target ios: no iOS toolchain found.\n  \
             Install the full Xcode (the App Store) — Command Line Tools alone is not\n  \
             enough — then run `xcodebuild -downloadPlatform iOS` once for the simulator\n  \
             runtime. (`xcrun --sdk iphonesimulator --show-sdk-path` must succeed.)"
            .to_string())
    }
}

/// Verify an Android build toolchain is present (the SDK, via ANDROID_HOME /
/// ANDROID_SDK_ROOT or `adb` on PATH), returning an actionable install message.
fn detect_android_toolchain() -> Result<(), String> {
    let has_sdk = std::env::var_os("ANDROID_HOME").is_some()
        || std::env::var_os("ANDROID_SDK_ROOT").is_some()
        || Command::new("adb")
            .arg("version")
            .output()
            .map(|o| o.status.success())
            .unwrap_or(false);
    if has_sdk {
        Ok(())
    } else {
        Err("sky build --target android: no Android SDK found.\n  \
             Install Android Studio (or the command-line tools) and set ANDROID_HOME to\n  \
             the SDK path (e.g. ~/Library/Android/sdk). `adb` should be on your PATH."
            .to_string())
    }
}

fn verb(check_only: bool) -> &'static str {
    if check_only {
        "check"
    } else {
        "build"
    }
}

// ---- run -----------------------------------------------------------------

/// `sky run <file>` — build, then exec the produced binary with inherited
/// stdio, propagating its exit code.
fn cmd_run(args: &[String]) -> ExitCode {
    // Pre-run DB steps (composed by re-invoking this binary's own `db`
    // subcommands, so they reuse the exact migrate/seed logic + env inheritance).
    // Order: db push/migrate, then seed, then serve — the container-entrypoint
    // "migrate-then-serve" shape.
    // `--embed` is a BUILD flag, and `parse_out` swallows anything it does not
    // recognise — so without this it would be accepted in silence and do
    // nothing, which is the exact failure mode `--embed` exists to refuse. Say
    // what to use instead rather than just rejecting.
    if args.iter().any(|a| a == "--embed") {
        eprintln!(
            "sky run: --embed is a `sky build` flag, not a `sky run` one.\n\
             \n\
             `sky run` already supervises a development cluster: set\n\
             \x20 [database]\n\
             \x20 embedded = true\n\
             in sky.toml and it starts one, injects the DSN and stops it on exit.\n\
             \n\
             To produce a binary that carries its own PostgreSQL, build it:\n\
             \x20 sky build --embed src/Main.sky\n\
             \x20 ./sky-out/app --embed"
        );
        return ExitCode::from(2);
    }
    let db_push = args.iter().any(|a| a == "--db-push");
    let db_migrate = args.iter().any(|a| a == "--db-migrate");
    let db_seed = args.iter().any(|a| a == "--db-seed");
    let args: Vec<String> = args
        .iter()
        .filter(|a| !matches!(a.as_str(), "--db-push" | "--db-migrate" | "--db-seed"))
        .cloned()
        .collect();
    let args = args.as_slice();
    let (args, profile) = parse_profile(args);
    let (positional, out_override) = parse_out(&args);
    let file = match resolve_entry_arg(
        &positional,
        "usage: sky run <file.sky> [--profile [--profile-dir <dir>] [--profile-timeout <dur>]]  (or run inside a project directory with a sky.toml)",
    ) {
        Ok(f) => f,
        Err(code) => return code,
    };
    let file = file.as_path();
    let Some((repo_root, project_dir)) = resolve(file) else {
        return ExitCode::FAILURE;
    };
    // The configuration is judged BEFORE the build: a project whose
    // `embedded = true` contradicts an explicit DSN is misconfigured, and making
    // the user sit through a compile to be told so is a worse way to learn it.
    // The cluster itself is started AFTER, so a project that does not compile
    // does not cycle a PostgreSQL up and down on every attempt.
    let embedded = match db_cluster::check_run_config(&project_dir, "sky run") {
        Ok(e) => e,
        Err(e) => {
            eprintln!("{e}");
            return ExitCode::FAILURE;
        }
    };
    let out_dir_name = out_override.unwrap_or_else(|| "sky-out".to_string());
    let opts = BuildOptions {
        repo_root,
        example_dir: project_dir.clone(),
        out_dir_name: out_dir_name.clone(),
        out_dir_abs: None,
        run: false,
        stdin: None,
        entry_module: entry_module_name(file),
        progress: true,
        embed_bundle: None,
        wasm: false,
    };
    let report = build_example(&opts);
    for w in &report.warnings {
        eprintln!("warning: {w}");
    }
    // Same migration LIST as `sky build` — the person performing an upgrade
    // often runs `sky run` (design §8.2, "sky build as well as sky run").
    if let Some(hint) = &report.migration_hint {
        eprintln!("\n{hint}\n");
    }
    if !report.emitted {
        eprintln!("sky run: {}", report.note);
        return ExitCode::FAILURE;
    }
    if !report.go_build_ok {
        eprintln!("sky run: go build failed:\n{}", report.go_build_stderr);
        return ExitCode::FAILURE;
    }
    if let Some(note) = &report.cgo_note {
        eprintln!("sky run: go build {note}");
    }
    let mut envs: Vec<(String, String)> = Vec::new();
    // The lease lives for the rest of this function — dropping it releases this
    // run's reference and, if nothing else holds one, stops the cluster.
    let cluster = if embedded {
        match db_cluster::acquire_for_run(&project_dir) {
            Ok(c) => {
                println!("{}", c.banner("sky run:"));
                envs.extend(c.envs());
                Some(c)
            }
            Err(e) => {
                eprintln!("{e}");
                return ExitCode::FAILURE;
            }
        }
    } else {
        None
    };
    if let Some(p) = &profile {
        // A relative dir resolves against the app's cwd (the project root, where
        // `run_app` runs it) → profiles land in `<project>/profile/` by default.
        let dir = p.dir.clone().unwrap_or_else(|| "profile".to_string());
        envs.push(("SKY_PROFILE_DIR".to_string(), dir.clone()));
        if let Some(t) = &p.timeout {
            envs.push(("SKY_PROFILE_TIMEOUT".to_string(), t.clone()));
        }
        println!(
            "Profiling enabled — writing to {dir}/ (cpu.pprof, heap.pprof, goroutines.txt, REPORT.md){}.",
            p.timeout
                .as_ref()
                .map(|t| format!("; hang dump after {t}"))
                .unwrap_or_default()
        );
    }
    // Run the requested DB steps before serving. Each re-invokes this binary's
    // own `sky db <op>` in the project dir; a failure aborts the run.
    for (flag, op) in [(db_push, "push"), (db_migrate, "migrate"), (db_seed, "seed")] {
        if !flag {
            continue;
        }
        let exe = match std::env::current_exe() {
            Ok(e) => e,
            Err(e) => {
                eprintln!("sky run --db-{op}: could not locate sky binary: {e}");
                return ExitCode::FAILURE;
            }
        };
        let mut step = Command::new(&exe);
        step.arg("db").arg(op).current_dir(&project_dir);
        // The migrate/seed steps talk to the SAME database the app is about to,
        // so they need the cluster's DSN too. Without this they would fall back
        // to whatever `sky.toml` declares — which, under `embedded = true`, is
        // nothing — and the app would boot onto an unmigrated cluster.
        if let Some(c) = &cluster {
            for (k, v) in c.envs() {
                step.env(k, v);
            }
        }
        let ok = step.status().map(|s| s.success()).unwrap_or(false);
        if !ok {
            eprintln!("sky run --db-{op}: DB step failed — not starting the app.");
            return ExitCode::FAILURE;
        }
    }
    println!("Build complete, running...");
    let out_dir = project_dir.join(&out_dir_name);
    match run_app(&out_dir, &envs) {
        Ok(status) => ExitCode::from(status.code().unwrap_or(1) as u8),
        Err(e) => {
            eprintln!("sky run: could not launch binary: {e}");
            ExitCode::FAILURE
        }
    }
}

// ---- fmt -----------------------------------------------------------------

/// `sky fmt [--check] [--stdin|-] <file...>` — opinionated, idempotent
/// re-layout (doc 10 §"sky fmt"), falling back to a lossless CST reprint for
/// any file where the opinionated pass would drop a comment or not be provably
/// idempotent (see `fmt::format_source`).
fn cmd_fmt(args: &[String]) -> ExitCode {
    let check = args.iter().any(|a| a == "--check");
    let stdin_mode = args.iter().any(|a| a == "--stdin" || a == "-");
    let files: Vec<&String> = args
        .iter()
        .filter(|a| !a.starts_with("--") && a.as_str() != "-")
        .collect();

    if stdin_mode {
        let mut src = String::new();
        if std::io::stdin().read_to_string(&mut src).is_err() {
            eprintln!("sky fmt: could not read stdin");
            return ExitCode::FAILURE;
        }
        let out = format_source(&src);
        if check {
            return if out == src {
                ExitCode::SUCCESS
            } else {
                ExitCode::FAILURE
            };
        }
        print!("{out}");
        return ExitCode::SUCCESS;
    }

    if files.is_empty() {
        eprintln!("usage: sky fmt [--check] <file.sky ...>   |   sky fmt --stdin");
        return ExitCode::from(2);
    }

    let mut changed_or_error = false;
    for f in files {
        let path = Path::new(f);
        let Ok(src) = std::fs::read_to_string(path) else {
            eprintln!("sky fmt: could not read {f}");
            changed_or_error = true;
            continue;
        };
        if check {
            if !is_formatted(&src) {
                println!("would reformat: {f}");
                changed_or_error = true;
            }
            continue;
        }
        let out = format_source(&src);
        if out != src {
            if let Err(e) = std::fs::write(path, &out) {
                eprintln!("sky fmt: could not write {f}: {e}");
                changed_or_error = true;
            } else {
                println!("formatted: {f}");
            }
        }
    }
    if changed_or_error {
        ExitCode::FAILURE
    } else {
        ExitCode::SUCCESS
    }
}

// ---- test ----------------------------------------------------------------

/// `sky test <suite.sky>` — synthesise an entry importing the suite, build+run
/// via the shared driver, propagate the test binary's exit code.
fn cmd_test(args: &[String]) -> ExitCode {
    let (positional, out_override) = parse_out(args);
    let Some(file) = positional.first() else {
        eprintln!("usage: sky test <suite.sky>");
        return ExitCode::from(2);
    };
    let out_dir_name = out_override.unwrap_or_else(|| "sky-out".to_string());
    match run_test(Path::new(file), &out_dir_name) {
        Ok(run) => {
            if !run.note.is_empty() {
                eprintln!("sky test: {}", run.note);
            }
            match run.exit_code {
                Some(0) => ExitCode::SUCCESS,
                Some(n) => ExitCode::from(n as u8),
                None => ExitCode::FAILURE,
            }
        }
        Err(e) => {
            eprintln!("sky test: {e}");
            ExitCode::FAILURE
        }
    }
}

// ---- lsp -----------------------------------------------------------------

/// `sky lsp` — launch the (already built) `sky-lsp` JSON-RPC server over stdio.
/// Locates the sibling binary next to this executable and execs it, forwarding
/// stdin/stdout/stderr.
fn cmd_lsp(_args: &[String]) -> ExitCode {
    // Run the LSP server inline — the transport + analysis engine are linked into
    // this binary, so `sky lsp` works from a single installed `sky` with no
    // separate `sky-lsp` process to locate or ship.
    sky_lsp::run();
    ExitCode::SUCCESS
}

// ---- clean ---------------------------------------------------------------

/// `sky clean` — remove generated `sky-out/` + `.skycache/` in the current
/// project (cwd). Best-effort; absent dirs are a no-op.
fn cmd_clean(_args: &[String]) -> ExitCode {
    let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
    let mut removed = Vec::new();
    for name in ["sky-out", ".skycache", ".skydeps", "dist"] {
        let dir = cwd.join(name);
        if dir.is_dir() && std::fs::remove_dir_all(&dir).is_ok() {
            removed.push(name);
        }
    }
    if removed.is_empty() {
        println!("clean: nothing to remove");
    } else {
        println!("clean: removed {}", removed.join(", "));
    }
    ExitCode::SUCCESS
}

// ---- init ----------------------------------------------------------------

/// `sky init [name]` — scaffold a new project: `<name>/sky.toml`,
/// `<name>/src/Main.sky` (a hello-world), and `<name>/.gitignore`. Mirrors
/// `app/Main.hs`'s `Init` handler (name defaults to `sky-project`). The CLAUDE.md
/// coding guide is copied from the repo's `templates/CLAUDE.md` when reachable.
/// True if the args request help (`--help` / `-h`) — checked BEFORE a verb acts,
/// so `sky init --help` prints help instead of scaffolding a `sky-project` (#6).
fn wants_help(args: &[String]) -> bool {
    args.iter().any(|a| a == "--help" || a == "-h")
}

fn cmd_init(args: &[String]) -> ExitCode {
    if wants_help(args) {
        println!(
            "sky init [name] [--production]\n\n\
             Scaffold a new Sky project in ./<name> (default: sky-project):\n  \
             sky.toml, src/Main.sky, .gitignore, docker-compose.yml, .env.example, AGENTS.md, CLAUDE.md.\n\n\
             Default is SQLite + in-memory sessions — zero setup, `sky run` and go.\n\
             The production path (one Postgres for app data + sessions + analytics +\n\
             telemetry) is documented inline in sky.toml + ready in docker-compose.yml.\n\n\
             Arguments:\n  \
             name          Project directory + name (default: sky-project)\n\n\
             Options:\n  \
             --production  Scaffold production-grade (Postgres) config ACTIVE from day 1\n                \
             (aliases: --postgres, --prod). Use when you know you'll scale.\n  \
             -h, --help    Show this help and exit (does NOT create a project)."
        );
        return ExitCode::SUCCESS;
    }
    let name = args
        .iter()
        .find(|a| !a.starts_with('-'))
        .cloned()
        .unwrap_or_else(|| "sky-project".to_string());
    let root = Path::new(&name);
    println!("Initialising project: {name}");

    if let Err(e) = std::fs::create_dir_all(root.join("src")) {
        eprintln!("sky init: could not create {}/src: {e}", root.display());
        return ExitCode::FAILURE;
    }

    // `--production` (aka `--postgres` / `--prod`) scaffolds the Postgres
    // one-DB-for-everything config ACTIVE from day 1 — for apps that KNOW they'll
    // scale (multi-instance). Default keeps SQLite: zero setup, ideal for a
    // prototype / playground / single-instance small app, with the production
    // path documented inline + a ready-to-use docker-compose.yml.
    let production = args
        .iter()
        .any(|a| a == "--production" || a == "--postgres" || a == "--prod");
    // Postgres identifiers can't contain '-', so derive a safe db/user/role name.
    let pg = name.replace(['-', '.', ' '], "_");

    let live_db_block = if production {
        format!(
            "[live]\n\
             port  = 8000\n\
             store = \"postgres\"        # sessions in the shared Postgres (DATABASE_URL)\n\
             ttl   = 1800\n\n\
             [database]\n\
             driver = \"postgres\"       # no path → falls back to DATABASE_URL (.env)\n\n\
             [analytics]\n\
             retention = \"180d\"        # prune old events so the table stays bounded\n\n\
             # PRODUCTION-GRADE scaffold. `docker compose up -d` starts Postgres; copy\n\
             # .env.example → .env (set DATABASE_URL + the secret). ONE connection string\n\
             # wires app data + sessions + analytics + telemetry into one database.\n\
             # For a quick local run WITHOUT Docker: set store=\"memory\" + driver=\"sqlite\"\n\
             # path=\"app.db\". Use BIGINT (not INTEGER) for millisecond timestamps.\n"
        )
    } else {
        "# ── Local dev: SQLite + in-memory sessions. Zero setup — just `sky run`.\n\
         #    Ideal for a prototype, playground, or single-instance small app.\n\
         [live]\n\
         port  = 8000\n\
         store = \"memory\"          # dev sessions (memory | sqlite | postgres | redis)\n\n\
         [database]\n\
         driver = \"sqlite\"\n\
         path   = \"app.db\"\n\n\
         # ── PRODUCTION (scaling / multi-instance): one Postgres for everything.\n\
         #    `docker compose up -d`, copy .env.example → .env, then uncomment below.\n\
         #    ONE DATABASE_URL (.env) wires app data + sessions + analytics + telemetry\n\
         #    into a single database — no separate paths. Also set ENV=production\n\
         #    (locks the dev console; see the production gate in AGENTS.md).\n\
         #    Use BIGINT (not INTEGER) for millisecond timestamps on Postgres.\n\
         #    Know you'll scale? Scaffold production-grade: `sky init <name> --production`.\n\
         #\n\
         # [live]\n\
         # store = \"postgres\"\n\
         # [database]\n\
         # driver = \"postgres\"      # falls back to DATABASE_URL\n\
         # [analytics]\n\
         # retention = \"180d\"\n"
            .to_string()
    };

    let toml = format!(
        "# sky.toml — project configuration.\n\
         # Full reference: https://github.com/anzellai/sky#skytoml\n\n\
         name    = \"{name}\"\n\
         version = \"0.1.0\"\n\
         entry   = \"src/Main.sky\"\n\
         bin     = \"app\"\n\n\
         [source]\n\
         root = \"src\"\n\n\
         {live_db_block}\n\
         # [auth]            # Std.Auth (uncomment to use)\n\
         # driver     = \"jwt\"\n\
         # cookieName = \"sky_sid\"       # secret from SKY_AUTH_TOKEN_SECRET (>=32 bytes)\n\n\
         # [\"go.dependencies\"]         # `sky add <pkg>` records these\n\
         # \"github.com/google/uuid\" = \"latest\"\n"
    );
    let main_sky = format!(
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\n\n\
         main =\n    println \"Hello from {name}!\"\n"
    );
    // `.skydata/` holds the local PostgreSQL cluster `sky db start` supervises —
    // a whole data directory, WAL included. Committing it would put a binary
    // database (and its `postmaster.pid`) into git.
    let gitignore =
        "sky-out/\n.skycache/\n.skydeps/\n.skydata/\n.env\n*.db\n*.db-shm\n*.db-wal\n";

    // docker-compose.yml — always scaffolded so the production path is one command
    // away, whether or not you start on Postgres. Host port 5433 avoids clashing
    // with a default local Postgres on 5432.
    let compose = format!(
        "# Production data store — ONE Postgres for everything: app data (Std.Db) +\n\
         # Sky.Live sessions + Std.Analytics + console telemetry.\n\
         #\n\
         # Dev needs NONE of this — `sky run` works on SQLite + in-memory (sky.toml).\n\
         # This is the production path (or dev-on-your-prod-backend from day 1).\n\
         #\n\
         #   docker compose up -d        # start Postgres (host 5433 -> container 5432)\n\
         #   cp .env.example .env         # then set DATABASE_URL + the secret\n\
         #   docker compose down          # stop (keeps data)  |  down -v to wipe\n\
         #\n\
         # Change the LEFT port (5433) if it's taken by another Postgres.\n\
         services:\n  \
         postgres:\n    \
         image: postgres:16-alpine\n    \
         container_name: {name}-pg\n    \
         restart: unless-stopped\n    \
         environment:\n      \
         POSTGRES_USER: {pg}\n      \
         POSTGRES_PASSWORD: {pg}\n      \
         POSTGRES_DB: {pg}\n    \
         ports:\n      \
         - \"5433:5432\"\n    \
         volumes:\n      \
         - {pg}-pgdata:/var/lib/postgresql/data\n    \
         healthcheck:\n      \
         test: [\"CMD-SHELL\", \"pg_isready -U {pg} -d {pg}\"]\n      \
         interval: 5s\n      \
         timeout: 3s\n      \
         retries: 10\n\n\
         volumes:\n  \
         {pg}-pgdata:\n"
    );

    // .env.example — copy to `.env`. Production vars are ACTIVE in --production
    // mode (so `cp .env.example .env` runs immediately) and COMMENTED otherwise
    // (dev needs none of them; they document the on-ramp).
    let c = if production { "" } else { "# " };
    let env_example = format!(
        "# Copy to `.env` (gitignored) and edit. Sky auto-loads .env at startup\n\
         # (shell env is never overridden; `System.loadEnv` re-loads explicitly).\n\
         # Precedence: process env > .env > Live.withX builder calls > sky.toml.\n\n\
         # Production gate — locks the dev console/banner, requires the auth secret.\n\
         {c}ENV=production\n\n\
         # ── ONE database for everything (Postgres from docker-compose.yml) ──\n\
         # This single URL wires app data + sessions + analytics + telemetry into one DB.\n\
         {c}DATABASE_URL=postgres://{pg}:{pg}@localhost:5433/{pg}?sslmode=disable\n\
         {c}SKY_LIVE_STORE=postgres\n\
         {c}SKY_ANALYTICS_RETENTION=180d\n\n\
         # Secret (never commit a real value). Generate: openssl rand -hex 32\n\
         {c}SKY_AUTH_TOKEN_SECRET=change-me-to-a-32-byte-random-secret\n"
    );

    let writes = [
        (root.join("sky.toml"), toml),
        (root.join("src/Main.sky"), main_sky),
        (root.join(".gitignore"), gitignore.to_string()),
        (root.join("docker-compose.yml"), compose),
        (root.join(".env.example"), env_example),
    ];
    for (path, body) in &writes {
        if let Err(e) = std::fs::write(path, body) {
            eprintln!("sky init: could not write {}: {e}", path.display());
            return ExitCode::FAILURE;
        }
    }

    // Best-effort AI coding guide: AGENTS.md is the agent-agnostic source of
    // truth (Claude/Copilot/Cursor/…); CLAUDE.md is a thin entry point that
    // imports it (`@AGENTS.md`). Copy BOTH so the scaffold works for any tool and
    // the import resolves. Prefer the repo template in dev, else the copy
    // embedded in the binary (doc 09 §E) so `sky init` scaffolds standalone.
    let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
    let repo_root = repo_root_for(&cwd).or_else(|| repo_root_for(root));
    let copy_template = |name: &str| -> bool {
        let dst = root.join(name);
        if let Some(rr) = &repo_root {
            let tmpl = rr.join("templates").join(name);
            if tmpl.is_file() && std::fs::copy(&tmpl, &dst).is_ok() {
                return true;
            }
        }
        project::extract_template(name, &dst)
    };
    let copied_agents = copy_template("AGENTS.md");
    let copied_claude = copy_template("CLAUDE.md");

    println!("Created {}/", root.display());
    println!("  sky.toml");
    println!("  src/Main.sky");
    println!("  .gitignore");
    println!("  docker-compose.yml   (production Postgres — optional)");
    println!("  .env.example         (copy to .env for production)");
    if copied_agents {
        println!("  AGENTS.md            (AI coding guide — source of truth)");
    }
    if copied_claude {
        println!("  CLAUDE.md            (Claude Code entry point → @AGENTS.md)");
    }
    println!();
    if production {
        println!("Production-grade scaffold (Postgres). Start the database, then run:");
        println!("  cd {name}");
        println!("  docker compose up -d");
        println!("  cp .env.example .env      # set DATABASE_URL + the secret");
        println!("  sky run src/Main.sky");
        println!();
        println!("One DATABASE_URL wires app data + sessions + analytics + telemetry into");
        println!("one Postgres. For a quick run without Docker, switch to sqlite in sky.toml.");
    } else {
        println!("Next: cd {name} && sky run src/Main.sky   # SQLite + in-memory, zero setup");
        println!();
        println!("Going to production / need to scale? See the commented block in sky.toml +");
        println!("docker-compose.yml, or scaffold Postgres from day 1: sky init {name} --production");
    }
    ExitCode::SUCCESS
}

// ---- doc -----------------------------------------------------------------

/// `sky doc <Module>` — terminal docs for one module (exported bindings + type
/// signatures + `-- |` summaries). `--list` enumerates every module.
/// `--serve` / `--tui` are deferred (they spawn a bundled Sky app the bring-up
/// doesn't materialise).
fn cmd_doc(args: &[String]) -> ExitCode {
    let serve = args.iter().any(|a| a == "--serve");
    let tui = args.iter().any(|a| a == "--tui");
    if serve && tui {
        eprintln!("sky doc: --serve and --tui are incompatible (pick one).");
        return ExitCode::from(2);
    }
    if serve {
        return cmd_doc_serve(parse_port(args, 8030));
    }
    if tui {
        return cmd_doc_tui();
    }
    // `sky doc --export <dir>` renders the SAME static doc-site `--serve` serves
    // (index.html + m/<module>.html + api/symbols.json + client-side search) to
    // `<dir>`, then exits — no server. This is the auto-generated, from-source
    // API reference the docs site + a CI GitHub-Pages deploy consume, so the
    // published API tracks the stdlib on every build with zero hand-maintenance.
    if let Some(dir) = args
        .iter()
        .position(|a| a == "--export")
        .and_then(|i| args.get(i + 1))
        .cloned()
        .or_else(|| {
            args.iter()
                .find_map(|a| a.strip_prefix("--export=").map(str::to_string))
        })
    {
        let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
        let Some(repo_root) = assets_root_for(&cwd) else {
            return ExitCode::FAILURE;
        };
        let project_dir = project::project_dir_for(&cwd.join("_"));
        let out = PathBuf::from(&dir);
        // Export variant: reference.html + relative links + top nav (the static
        // Pages site), vs render_doc_site's serve-oriented index.html.
        if let Err(e) = project::render_doc_site_export(&repo_root, &project_dir, &out) {
            eprintln!("sky doc --export: could not render doc-site into {dir}: {e}");
            return ExitCode::FAILURE;
        }
        if !out.join("api").join("symbols.json").is_file() {
            eprintln!("sky doc --export: render produced no api/symbols.json under {dir}");
            return ExitCode::FAILURE;
        }
        // Teaching layer: the curated guide pages (docs/, excluding history +
        // roadmaps + legacy), the "Learn Sky" tour (docs/learn/), and the
        // hand-written landing page. Together with render_doc_site's reference.html
        // + m/*.html, this is the full site: landing → Learn / Reference / Guides.
        if let Err(e) = project::render_guides(&repo_root, &out) {
            eprintln!("sky doc --export: could not render guides: {e}");
            return ExitCode::FAILURE;
        }
        if let Err(e) = project::render_learn_tour(&repo_root, &out) {
            eprintln!("sky doc --export: could not render learn tour: {e}");
            return ExitCode::FAILURE;
        }
        if let Err(e) = project::render_landing(&out) {
            eprintln!("sky doc --export: could not render landing page: {e}");
            return ExitCode::FAILURE;
        }
        let guides = std::fs::read_dir(out.join("guide")).map(|d| d.count()).unwrap_or(0);
        let lessons = std::fs::read_dir(out.join("learn")).map(|d| d.count()).unwrap_or(0);
        println!(
            "Exported Sky doc-site to {dir}/ (landing + reference + m/*.html + {} guide page(s) + {lessons}-lesson tour)",
            guides.saturating_sub(1)
        );
        return ExitCode::SUCCESS;
    }
    let list = args.iter().any(|a| a == "--list");
    let target = args.iter().find(|a| !a.starts_with('-')).cloned();

    // Resolve the project + repo root from cwd (doc reads stdlib + src/).
    let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
    let Some(repo_root) = assets_root_for(&cwd) else {
        return ExitCode::FAILURE;
    };
    let project_dir = project::project_dir_for(&cwd.join("_"));

    if list {
        println!("{}", project::list_modules(&repo_root, &project_dir));
        return ExitCode::SUCCESS;
    }
    let Some(module) = target else {
        eprintln!("usage: sky doc <Module>   |   sky doc --list");
        return ExitCode::from(2);
    };
    match project::render_module(&repo_root, &project_dir, &module) {
        Ok(page) => {
            print!("{page}");
            ExitCode::SUCCESS
        }
        Err(msg) => {
            eprintln!("{msg}");
            ExitCode::FAILURE
        }
    }
}

/// `sky doc --serve` renders a static doc-site from the project's stdlib and
/// `src/`, then builds and spawns the bundled `sky-doc-server` (Sky.Http.Server)
/// pointed at it via `SKY_DOC_DIR` on `SKY_LIVE_PORT`. Foreground; Ctrl-C stops.
/// Mirrors `app/Main.hs` `runDocServe`.
/// Render the doc-site into `<project>/.skycache/doc-out` and return its
/// ABSOLUTE path. The bundled doc app (serve/tui) is spawned in its own build
/// dir and reads `$SKY_DOC_DIR/api/symbols.json`, so a relative path would
/// resolve against the wrong cwd — the "failed to read .skycache/doc-out/api/
/// symbols.json" the user hit. Canonicalising here makes `SKY_DOC_DIR` absolute
/// regardless of how the project dir resolved, and the existence check turns a
/// silent render gap into an actionable error.
fn prepare_doc_out(repo_root: &Path, project_dir: &Path) -> Result<PathBuf, ExitCode> {
    let doc_out = project_dir.join(".skycache").join("doc-out");
    if let Err(e) = project::render_doc_site(repo_root, project_dir, &doc_out) {
        eprintln!("sky doc: could not render doc-site: {e}");
        return Err(ExitCode::FAILURE);
    }
    let doc_out = std::fs::canonicalize(&doc_out).unwrap_or(doc_out);
    if !doc_out.join("api").join("symbols.json").is_file() {
        eprintln!(
            "sky doc: the doc-site render produced no api/symbols.json under {} \
             — the project's modules may have failed to parse for docs.",
            doc_out.display()
        );
        return Err(ExitCode::FAILURE);
    }
    Ok(doc_out)
}

fn cmd_doc_serve(port: u16) -> ExitCode {
    let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
    let Some(repo_root) = assets_root_for(&cwd) else {
        return ExitCode::FAILURE;
    };
    let project_dir = project::project_dir_for(&cwd.join("_"));

    // Render the doc-site into the project's cache so the server has content.
    let doc_out = match prepare_doc_out(&repo_root, &project_dir) {
        Ok(d) => d,
        Err(code) => return code,
    };

    let Some(src_dir) = bundled::bundled_src_dir(&repo_root, "doc") else {
        return bundled_missing("doc");
    };
    let out_dir = match bundled::ensure_built(
        &repo_root,
        &src_dir,
        "doc",
        "live",
        bundled::ENTRY_LIVE,
        &version_slug(),
    ) {
        Ok(d) => d,
        Err(e) => {
            eprintln!("{e}");
            return ExitCode::FAILURE;
        }
    };

    println!(
        "sky doc: serving {} on http://127.0.0.1:{port} (Ctrl-C to stop)",
        doc_out.display()
    );
    spawn_foreground(
        &out_dir,
        &[
            ("SKY_LIVE_PORT".to_string(), port.to_string()),
            (
                "SKY_DOC_DIR".to_string(),
                doc_out.to_string_lossy().into_owned(),
            ),
        ],
    )
}

/// `sky doc --tui` — render the doc-site, then build + spawn the bundled
/// Sky.Tui doc browser pointed at it via `SKY_DOC_DIR`. Mirrors `runDocTui`.
fn cmd_doc_tui() -> ExitCode {
    let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
    let Some(repo_root) = assets_root_for(&cwd) else {
        return ExitCode::FAILURE;
    };
    let project_dir = project::project_dir_for(&cwd.join("_"));

    let doc_out = match prepare_doc_out(&repo_root, &project_dir) {
        Ok(d) => d,
        Err(code) => return code,
    };

    let Some(src_dir) = bundled::bundled_src_dir(&repo_root, "doc") else {
        return bundled_missing("doc");
    };
    let out_dir = match bundled::ensure_built(
        &repo_root,
        &src_dir,
        "doc",
        "tui",
        bundled::ENTRY_TUI,
        &version_slug(),
    ) {
        Ok(d) => d,
        Err(e) => {
            eprintln!("{e}");
            return ExitCode::FAILURE;
        }
    };

    println!("sky doc: starting terminal browser (Ctrl-C to exit)...");
    spawn_foreground(
        &out_dir,
        &[(
            "SKY_DOC_DIR".to_string(),
            doc_out.to_string_lossy().into_owned(),
        )],
    )
}

// ---- console -------------------------------------------------------------

/// `sky console [--port N] [--tui]` — build + spawn the bundled Sky Console
/// (`sky-bundled/console`): Sky.Live on `SKY_LIVE_PORT` (default 8025), or the
/// Sky.Tui backend with `--tui`. Foreground; Ctrl-C stops. Mirrors the
/// `SpawnSkyConsole` build+spawn shape (`app/Main.hs` `runConsole`).
fn cmd_console(args: &[String]) -> ExitCode {
    let tui = args.iter().any(|a| a == "--tui");
    let port = parse_port(args, 8025);

    let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
    let Some(repo_root) = assets_root_for(&cwd) else {
        return ExitCode::FAILURE;
    };
    let Some(src_dir) = bundled::bundled_src_dir(&repo_root, "console") else {
        return bundled_missing("console");
    };

    let (variant, entry): (&str, &str) = if tui {
        ("tui", bundled::ENTRY_TUI)
    } else {
        ("live", bundled::ENTRY_LIVE)
    };
    let out_dir = match bundled::ensure_built(
        &repo_root,
        &src_dir,
        "console",
        variant,
        entry,
        &version_slug(),
    ) {
        Ok(d) => d,
        Err(e) => {
            eprintln!("{e}");
            return ExitCode::FAILURE;
        }
    };

    if tui {
        println!("sky console: starting terminal console (Ctrl-C to exit)...");
        spawn_foreground(&out_dir, &[])
    } else {
        println!("sky console: serving on http://127.0.0.1:{port} (Ctrl-C to stop)");
        spawn_foreground(&out_dir, &[("SKY_LIVE_PORT".to_string(), port.to_string())])
    }
}

/// `sky console-serve` builds and spawns the standalone Sky Console Hub daemon
/// (OTLP receivers plus a SQLite hot store) from `runtime-go/cmd/sky-hub` (pure
/// Go, `CGO_ENABLED=0`). Flags: `--port N`, `--data-dir DIR`, `--auth MODE`, and
/// an optional `--tls-cert F` / `--tls-key F` pair. Mirrors `runConsoleServe`.
fn cmd_console_serve(args: &[String]) -> ExitCode {
    let port = parse_port(args, 4000);
    let data_dir = flag_value(args, "--data-dir").unwrap_or_else(|| "./skyhub-data".to_string());
    let auth = flag_value(args, "--auth").unwrap_or_else(|| "token".to_string());
    let tls_cert = flag_value(args, "--tls-cert");
    let tls_key = flag_value(args, "--tls-key");

    // Validate flag combinations up front (fail fast), mirroring the oracle.
    match (&tls_cert, &tls_key) {
        (Some(_), None) => {
            eprintln!("sky console-serve: --tls-cert set but --tls-key missing");
            return ExitCode::from(2);
        }
        (None, Some(_)) => {
            eprintln!("sky console-serve: --tls-key set but --tls-cert missing");
            return ExitCode::from(2);
        }
        _ => {}
    }
    if auth != "token" && auth != "off" && auth != "app" {
        eprintln!("sky console-serve: unknown --auth mode {auth} (want token|off|app)");
        return ExitCode::from(2);
    }

    let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
    let Some(repo_root) = assets_root_for(&cwd) else {
        return ExitCode::FAILURE;
    };
    let runtime_go = repo_root.join("runtime-go");
    if !runtime_go
        .join("cmd")
        .join("sky-hub")
        .join("main.go")
        .is_file()
    {
        eprintln!(
            "sky console-serve: runtime-go/cmd/sky-hub not found under {}.\n\
             The hub source is embedded in the binary and extracted on first use;\n\
             a missing source here means the embedded asset extraction failed.",
            repo_root.display()
        );
        return ExitCode::from(2);
    }

    // Build the hub binary into the per-version cache (one-time per version).
    let hub_dir = bundled::cache_root().join(format!("hub-{}", version_slug()));
    let hub_bin = hub_dir.join("sky-hub");
    if !hub_bin.is_file() {
        if let Err(e) = std::fs::create_dir_all(&hub_dir) {
            eprintln!("sky console-serve: could not create cache dir: {e}");
            return ExitCode::FAILURE;
        }
        println!(
            "sky console-serve: building hub daemon (one-time per version, into {})...",
            hub_dir.display()
        );
        // CGO_ENABLED=0: rt/hub transitively imports rt (webview.go, cgo+WebKit
        // on darwin); disabling cgo routes through webview_stub.go and dodges the
        // Apple ld_prime long-symbol assertion. The hub never calls webview.
        let status = Command::new("go")
            .args(["build", "-o"])
            .arg(&hub_bin)
            .arg("./cmd/sky-hub")
            .current_dir(&runtime_go)
            .env("CGO_ENABLED", "0")
            .status();
        match status {
            Ok(s) if s.success() => {}
            Ok(s) => {
                eprintln!(
                    "sky console-serve: go build sky-hub failed (exit {})",
                    s.code().unwrap_or(1)
                );
                return ExitCode::FAILURE;
            }
            Err(e) => {
                eprintln!("sky console-serve: could not launch go build: {e}");
                return ExitCode::FAILURE;
            }
        }
    }

    let mut child_args: Vec<String> = vec![
        "--port".to_string(),
        port.to_string(),
        "--data-dir".to_string(),
        data_dir,
        "--auth".to_string(),
        auth,
    ];
    if let (Some(c), Some(k)) = (tls_cert, tls_key) {
        child_args.extend(["--tls-cert".to_string(), c, "--tls-key".to_string(), k]);
    }
    let status = Command::new(&hub_bin).args(&child_args).status();
    match status {
        Ok(s) => propagate(s.code()),
        Err(e) => {
            eprintln!("sky console-serve: could not launch hub: {e}");
            ExitCode::FAILURE
        }
    }
}

// ---- bundled-app helpers -------------------------------------------------

/// Run the built `app` binary at `<out_dir>/app` with inherited stdio + `envs`,
/// foreground, propagating its exit code. Ctrl-C reaches the child (shared
/// process group) so the server stops cleanly; 130/143 (SIGINT/SIGTERM) map to
/// success — a user-initiated stop is not a failure.
fn spawn_foreground(out_dir: &Path, envs: &[(String, String)]) -> ExitCode {
    match run_app(out_dir, envs) {
        Ok(status) => propagate(status.code()),
        Err(e) => {
            eprintln!("sky: could not launch bundled app: {e}");
            ExitCode::FAILURE
        }
    }
}

/// Map a child exit code to an `ExitCode`, treating the signal-terminated cases
/// a foreground server hits on Ctrl-C (130 = SIGINT, 143 = SIGTERM) as success.
fn propagate(code: Option<i32>) -> ExitCode {
    match code {
        Some(0) | Some(130) | Some(143) | None => ExitCode::SUCCESS,
        Some(n) => ExitCode::from(n as u8),
    }
}

/// The message emitted when a bundled verb can't find its `sky-bundled/<name>`
/// source. The source is embedded in the binary and extracted on first use, so
/// this only fires if the embedded asset extraction failed.
fn bundled_missing(name: &str) -> ExitCode {
    eprintln!(
        "sky {name}: sky-bundled/{name} source not found.\n\
         The bundled app source is embedded in the binary and extracted on first\n\
         use; a missing source here means the embedded asset extraction failed."
    );
    ExitCode::from(2)
}

/// A filesystem-safe slug of the version string for cache-dir naming
/// (`sky v0.17.10` → `v0.17.10`, `sky dev` → `dev`).
fn version_slug() -> String {
    version_string()
        .trim_start_matches("sky ")
        .chars()
        .map(|c| {
            if c.is_ascii_alphanumeric() || c == '.' || c == '-' {
                c
            } else {
                '_'
            }
        })
        .collect()
}

/// Parse `--port N` / `-p N` / `--port=N` from `args`, falling back to `default`.
fn parse_port(args: &[String], default: u16) -> u16 {
    let mut it = args.iter();
    while let Some(a) = it.next() {
        if a == "--port" || a == "-p" {
            if let Some(v) = it.next() {
                if let Ok(n) = v.parse() {
                    return n;
                }
            }
        } else if let Some(v) = a.strip_prefix("--port=") {
            if let Ok(n) = v.parse() {
                return n;
            }
        }
    }
    default
}

/// Parse a `--flag VALUE` / `--flag=VALUE` string option from `args`.
fn flag_value(args: &[String], flag: &str) -> Option<String> {
    let eq = format!("{flag}=");
    let mut it = args.iter();
    while let Some(a) = it.next() {
        if a == flag {
            return it.next().cloned();
        } else if let Some(v) = a.strip_prefix(&eq) {
            return Some(v.to_string());
        }
    }
    None
}

// ---- db ------------------------------------------------------------------

fn extract_between<'a>(s: &'a str, begin: &str, end: &str) -> Option<&'a str> {
    let start = s.find(begin)? + begin.len();
    let rest = &s[start..];
    let stop = rest.find(end)?;
    Some(&rest[..stop])
}

/// `sky db migrate --gen [name]` — derive the target schema from the project's
/// `db` (via a temp, DB-free schema-dump entry), diff it against
/// `db/schema.json`, and write a migration file + updated snapshot. Additive ops
/// are active; destructive ops are quarantined (docs/v0.19/auto-migration-architecture.md).
/// Print a prompt (no newline) and read one line from stdin. Empty on EOF.
fn prompt_line(prompt: &str) -> String {
    use std::io::Write as _;
    print!("{prompt}");
    let _ = std::io::stdout().flush();
    let mut line = String::new();
    let _ = std::io::stdin().read_line(&mut line);
    line
}

fn cmd_db_gen(args: &[String]) -> ExitCode {
    let positional: Vec<&String> = args.iter().filter(|a| !a.starts_with("--")).collect();
    let name = positional.first().map(|s| s.as_str()).unwrap_or("migration");
    let file = Path::new("src/Main.sky");
    let entry_module = entry_module_name(file).unwrap_or_else(|| "Main".into());

    // 1-2. Synthesise + build the DB-free schema-dump entry. Routed through the
    // shared helper so it lands in a scratch dir — this used to build into the
    // project's real `sky-out/`, replacing the app binary with `SkyDbGen`.
    let gen_code = format!(
        "module SkyDbGen exposing (main)\n\nimport {entry_module} exposing (db)\nimport Std.Db.Store as Store\n\nmain =\n    Store.dumpSchema db\n"
    );
    let Some((bin, project_dir, _scratch)) = build_temp_db_entry(
        &format!(
            "sky db --gen: build failed — does module {entry_module} `exposing (db)` with `db = Store.project [...]`?\nsky db --gen"
        ),
        "SkyDbGen",
        "_skydbgen.sky",
        &gen_code,
    ) else {
        return ExitCode::FAILURE;
    };

    // 3. Run the dump binary, capture stdout.
    let output = Command::new(&bin).current_dir(&project_dir).output();
    let stdout = match output {
        Ok(o) => String::from_utf8_lossy(&o.stdout).into_owned(),
        Err(e) => {
            eprintln!("sky db --gen: dump run failed: {e}");
            return ExitCode::FAILURE;
        }
    };
    let Some(json) = extract_between(&stdout, "SKY_SCHEMA_BEGIN", "SKY_SCHEMA_END") else {
        eprintln!("sky db --gen: schema-dump produced no output (is `db` a Store.Project?)");
        return ExitCode::FAILURE;
    };
    let target: db_migrate::Schema = match serde_json::from_str(json.trim()) {
        Ok(s) => s,
        Err(e) => {
            eprintln!("sky db --gen: bad schema JSON: {e}");
            return ExitCode::FAILURE;
        }
    };

    // 4. Read the committed snapshot.
    let db_dir = project_dir.join("db");
    let snapshot_path = db_dir.join("schema.json");
    let snapshot: db_migrate::Schema = std::fs::read_to_string(&snapshot_path)
        .ok()
        .and_then(|s| serde_json::from_str(&s).ok())
        .unwrap_or_default();

    // 5. Diff.
    let mut d = db_migrate::diff(&target, &snapshot);
    if d.is_empty() {
        println!("sky db --gen: no schema changes — nothing to generate.");
        return ExitCode::SUCCESS;
    }

    // 5b. Interactive resolution — only on a TTY. In CI / non-interactive runs the
    //     safe defaults stand (drops quarantined, required columns get a zero
    //     backfill), so scripted gen is deterministic and never blocks on a prompt.
    if std::io::stdin().is_terminal() {
        for dec in d.drop_decisions() {
            let hint = if dec.rename_candidates.is_empty() {
                String::new()
            } else {
                format!(" (new column(s) here: {})", dec.rename_candidates.join(", "))
            };
            println!("\nColumn {}.{} was removed{hint}.", dec.table, dec.column);
            let ans = prompt_line("  (r)enamed, (d)ropped for good, or (s)kip [s]? ");
            match ans.trim().to_lowercase().chars().next() {
                Some('r') => {
                    let to = if dec.rename_candidates.len() == 1 {
                        dec.rename_candidates[0].clone()
                    } else {
                        prompt_line("    new column name: ").trim().to_string()
                    };
                    if to.is_empty() {
                        println!("    no target given — left quarantined.");
                    } else {
                        d.rename(&dec.table, &dec.column, &to);
                        println!("    → renameColumn {} → {to}", dec.column);
                    }
                }
                Some('d') => {
                    d.confirm_drop(&dec.table, &dec.column);
                    println!("    → dropColumn {} (data lost on apply)", dec.column);
                }
                _ => println!("    left quarantined (inert)."),
            }
        }
        for (table, column, kind, cur) in d.defaulted_adds() {
            let ans = prompt_line(&format!(
                "Backfill default for existing rows in {table}.{column} ({kind}) [{cur}]: "
            ));
            let t = ans.trim();
            if !t.is_empty() {
                match db_migrate::parse_default(&kind, t) {
                    Some(v) => d.set_default(&table, &column, v),
                    None => println!("  (couldn't parse '{t}' as {kind} — keeping {cur})"),
                }
            }
        }
        if d.is_empty() {
            println!("sky db --gen: all changes resolved away — nothing to generate.");
            return ExitCode::SUCCESS;
        }
    }

    // 6. Write migration + snapshot.
    let ts = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0);
    let id = format!("{ts}_{name}");
    let migrations_dir = db_dir.join("migrations");
    if let Err(e) = std::fs::create_dir_all(&migrations_dir) {
        eprintln!("sky db --gen: cannot create db/migrations: {e}");
        return ExitCode::FAILURE;
    }
    let mig_path = migrations_dir.join(format!("{id}.json"));
    if let Err(e) = std::fs::write(&mig_path, db_migrate::migration_file_json(&id, &d)) {
        eprintln!("sky db --gen: cannot write migration: {e}");
        return ExitCode::FAILURE;
    }
    if let Err(e) = std::fs::write(
        &snapshot_path,
        serde_json::to_string_pretty(&target).unwrap_or_default(),
    ) {
        eprintln!("sky db --gen: cannot write snapshot: {e}");
        return ExitCode::FAILURE;
    }

    println!(
        "sky db --gen: wrote db/migrations/{id}.json ({} additive op(s)) + updated db/schema.json",
        d.ops.len()
    );
    if !d.destructive.is_empty() {
        eprintln!(
            "\n⚠  {} destructive change(s) QUARANTINED in the `destructive` array (NOT applied):",
            d.destructive.len()
        );
        for w in &d.warnings {
            eprintln!("   - {w}");
        }
        eprintln!("   Review the file; move an entry into `ops` (or edit a drop into a renameColumn) to activate.");
    }
    ExitCode::SUCCESS
}

/// `sky db migrate` in a file-based project — apply the committed
/// `db/migrations/*.json` files through the checksummed `_sky_migrations` ledger
/// (`Std.Db.Migrate.migrateOps`), at most once each, dialect-correct for the live
/// connection. Non-interactive + idempotent: only the active `ops` of each file
/// apply; the quarantined `destructive` array is ignored by the runtime.
fn cmd_db_apply(_args: &[String]) -> ExitCode {
    let file = Path::new("src/Main.sky");
    let Some((_repo_root, project_dir)) = resolve(file) else {
        return ExitCode::FAILURE;
    };
    if is_compiler_repo_root(&project_dir) {
        eprintln!("sky db: refusing to run from the Sky compiler repo root");
        return ExitCode::FAILURE;
    }

    // 1. Collect db/migrations/*.json (sorted by filename = chronological), wrap
    //    the raw file bodies into one JSON array the runtime parses as [{id,ops}].
    let migrations_dir = project_dir.join("db").join("migrations");
    let mut files: Vec<PathBuf> = std::fs::read_dir(&migrations_dir)
        .into_iter()
        .flatten()
        .flatten()
        .map(|e| e.path())
        .filter(|p| p.extension().map(|x| x == "json").unwrap_or(false))
        .collect();
    files.sort();
    if files.is_empty() {
        println!("sky db migrate: no migration files in db/migrations — run `sky db migrate --gen` first.");
        return ExitCode::SUCCESS;
    }
    let bodies: Vec<String> = files
        .iter()
        .filter_map(|p| std::fs::read_to_string(p).ok())
        .map(|s| s.trim().to_string())
        .filter(|s| !s.is_empty())
        .collect();
    let apply_json = format!("[{}]", bodies.join(","));
    let apply_path = project_dir.join("db").join("_apply.json");
    if let Err(e) = std::fs::write(&apply_path, &apply_json) {
        eprintln!("sky db migrate: cannot stage migrations: {e}");
        return ExitCode::FAILURE;
    }

    // 2. Write a temp entry that reads the staged file, connects, and applies.
    let entry_module = entry_module_name(file).unwrap_or_else(|| "Main".into());
    let _ = &entry_module; // apply entry is self-contained; project only supplies config/env
    let apply_code = r#"module SkyDbApply exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Sky.Core.File as File
import Sky.Core.String as String
import Sky.Core.List as List
import Std.Db as Db
import Std.Db.Migrate as Migrate
import Std.Log exposing (println)


main : Task Error ()
main =
    File.readFile "db/_apply.json"
        |> Task.andThen applyAll


applyAll : String -> Task Error ()
applyAll json =
    Db.connect ()
        |> Task.andThen (\conn -> Migrate.migrateOps conn json)
        |> Task.andThen report


report : List String -> Task Error ()
report applied =
    let
        _ =
            println ("sky db migrate: applied " ++ String.fromInt (List.length applied) ++ " migration(s)")
    in
    Task.succeed ()
"#;
    // 3. Synthesise + build it. Routed through the shared helper so it lands in
    // a scratch dir — this used to build into the project's real `sky-out/`,
    // replacing the app binary with `SkyDbApply`.
    let Some((bin, project_dir, _scratch)) =
        build_temp_db_entry("sky db migrate", "SkyDbApply", "_skydbapply.sky", apply_code)
    else {
        let _ = std::fs::remove_file(&apply_path);
        return ExitCode::FAILURE;
    };

    // 4. Run — the app's Db.connect reads the project's DB config from the env
    //    (SKY_DB_PATH / DATABASE_URL), inherited from this process.
    let status = Command::new(&bin).current_dir(&project_dir).status();
    let _ = std::fs::remove_file(&apply_path);
    match status {
        Ok(s) if s.success() => ExitCode::SUCCESS,
        Ok(_) => ExitCode::FAILURE,
        Err(e) => {
            eprintln!("sky db migrate: apply run failed: {e}");
            ExitCode::FAILURE
        }
    }
}

/// `sky db init` — scaffold the file-based migration layout (`db/migrations/` +
/// an empty snapshot) so `sky db migrate --gen` has somewhere to write and
/// `sky db migrate` routes to the file-based applier. Idempotent.
fn cmd_db_init() -> ExitCode {
    let db_dir = Path::new("db");
    let migrations = db_dir.join("migrations");
    if let Err(e) = std::fs::create_dir_all(&migrations) {
        eprintln!("sky db init: cannot create db/migrations: {e}");
        return ExitCode::FAILURE;
    }
    let snapshot = db_dir.join("schema.json");
    if !snapshot.exists() {
        if let Err(e) = std::fs::write(&snapshot, "{\"tables\":[]}\n") {
            eprintln!("sky db init: cannot write db/schema.json: {e}");
            return ExitCode::FAILURE;
        }
    }
    println!(
        "sky db init: ready.\n  db/migrations/   committed migration files\n  db/schema.json   type-derived snapshot (do not hand-edit)\n\nNext: define `db : Store.Project` in your entry module, then\n  sky db migrate --gen init"
    );
    ExitCode::SUCCESS
}

/// Shared temp-entry helper: write `code` as `src/<module>.sky`, build it, and on
/// success return the built binary path (caller runs it). Removes the temp source
/// whether or not the build succeeds. `None` → build failed (message already
/// printed with `label`).
/// Owns a `sky db` helper build's scratch dir and removes it when the caller is
/// done with the binary. Callers bind it (`let (_bin, _dir, _scratch) = …`) so
/// the dir outlives the `Command` that runs the helper.
struct DbScratch(PathBuf);

impl Drop for DbScratch {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

/// A private scratch directory for one `sky db` helper build. Same shape as
/// `testrunner::scratch_dir` — pid + monotonic nanos, so concurrent invocations
/// never collide.
fn db_scratch_dir(tag: &str) -> PathBuf {
    std::env::temp_dir().join(format!(
        "sky-db-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map(|d| d.as_nanos())
            .unwrap_or(0)
    ))
}

/// Build a synthesised `sky db` helper entry — **entirely inside a scratch
/// dir**, never the project's own tree.
///
/// Both halves of that used to be wrong. The synthesised `.sky` was written into
/// the user's `src/` (so an aborted run left `src/_skydbseed.sky` behind, where
/// module discovery picks it up on the next build), and the build ran with
/// `out_dir_abs: None`, i.e. straight into the project's real `sky-out/`. Any db
/// verb therefore REPLACED `sky-out/app` with the helper program: run
/// `sky db status`, and the binary you were about to test is gone — or still
/// there and silently a different program, so the test that follows exercises
/// `SkyDbStatus` and passes.
///
/// `sky test` already solved this; `BuildOptions::out_dir_abs`'s own doc comment
/// names it as the mechanism. The project dir is still `example_dir`, so the
/// project's `src/`, FFI surface and go.mod pins load normally — only the synth
/// entry and the output move.
fn build_temp_db_entry(
    label: &str,
    module: &str,
    filename: &str,
    code: &str,
) -> Option<(PathBuf, PathBuf, DbScratch)> {
    let file = Path::new("src/Main.sky");
    let (repo_root, project_dir) = resolve(file)?;
    if is_compiler_repo_root(&project_dir) {
        eprintln!("{label}: refusing to run from the Sky compiler repo root");
        return None;
    }
    let scratch = db_scratch_dir(module);
    if let Err(e) = std::fs::create_dir_all(&scratch) {
        eprintln!("{label}: cannot create scratch dir: {e}");
        return None;
    }
    let src = scratch.join(filename);
    if let Err(e) = std::fs::write(&src, code) {
        eprintln!("{label}: cannot write temp entry: {e}");
        let _ = std::fs::remove_dir_all(&scratch);
        return None;
    }
    let out_dir = scratch.join("sky-out");
    let opts = BuildOptions {
        repo_root,
        example_dir: project_dir.clone(),
        out_dir_name: "sky-out".into(),
        out_dir_abs: Some(out_dir.clone()),
        run: false,
        stdin: None,
        entry_module: Some(module.to_string()),
        progress: false,
        embed_bundle: None,
        wasm: false,
    };
    let report = build_project(&opts, &[scratch.clone()], Some(module));
    if !(report.emitted && report.go_build_ok) {
        eprintln!("{label}: build failed\n{}\n{}", report.note, report.go_build_stderr);
        let _ = std::fs::remove_dir_all(&scratch);
        return None;
    }
    let bin = out_dir.join(project::configured_bin_name(&project_dir));
    Some((bin, project_dir, DbScratch(scratch)))
}

/// `sky db status` (file-based) — list committed `db/migrations/*.json` and mark
/// each applied (present in the live `_sky_migrations` ledger) or pending, and
/// flag any pending file that carries quarantined destructive ops. Exits non-zero
/// when anything is pending — usable as a "is this DB up to date?" deploy gate.
fn cmd_db_status(_args: &[String]) -> ExitCode {
    // Temp entry prints the ledger's applied ids between markers (empty on a
    // fresh DB with no ledger table yet).
    let code = r#"module SkyDbStatus exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Sky.Core.List as List
import Sky.Core.Dict as Dict
import Std.Db as Db
import Std.Log exposing (println)


main : Task Error ()
main =
    Db.connect ()
        |> Task.andThen queryApplied
        |> Task.andThen printApplied


queryApplied : Db -> Task Error (List String)
queryApplied conn =
    Db.query conn "SELECT name FROM _sky_migrations ORDER BY name" []
        |> Task.map (List.map (\row -> Maybe.withDefault "" (Dict.get "name" row)))
        |> Task.onError (\_ -> Task.succeed [])


printApplied : List String -> Task Error ()
printApplied ids =
    let
        _ =
            println "SKY_APPLIED_BEGIN"

        _ =
            println (String.join "\n" ids)

        _ =
            println "SKY_APPLIED_END"
    in
    Task.succeed ()
"#;
    let Some((bin, project_dir, _scratch)) = build_temp_db_entry("sky db status", "SkyDbStatus", "_skydbstatus.sky", code)
    else {
        return ExitCode::FAILURE;
    };
    let output = Command::new(&bin).current_dir(&project_dir).output();
    let stdout = match output {
        Ok(o) => String::from_utf8_lossy(&o.stdout).into_owned(),
        Err(e) => {
            eprintln!("sky db status: run failed: {e}");
            return ExitCode::FAILURE;
        }
    };
    let applied: std::collections::HashSet<String> =
        extract_between(&stdout, "SKY_APPLIED_BEGIN", "SKY_APPLIED_END")
            .unwrap_or("")
            .lines()
            .map(|l| l.trim().to_string())
            .filter(|l| !l.is_empty())
            .collect();

    // List committed migration files (sorted = chronological).
    let migrations_dir = project_dir.join("db").join("migrations");
    let mut files: Vec<PathBuf> = std::fs::read_dir(&migrations_dir)
        .into_iter()
        .flatten()
        .flatten()
        .map(|e| e.path())
        .filter(|p| p.extension().map(|x| x == "json").unwrap_or(false))
        .collect();
    files.sort();

    println!("migrations (db/migrations) — {} applied:", applied.len());
    let mut pending = 0;
    for p in &files {
        let id = p.file_stem().and_then(|s| s.to_str()).unwrap_or("").to_string();
        let has_destructive = std::fs::read_to_string(p)
            .ok()
            .and_then(|s| serde_json::from_str::<serde_json::Value>(&s).ok())
            .and_then(|v| v.get("destructive").cloned())
            .map(|d| d.as_array().map(|a| !a.is_empty()).unwrap_or(false))
            .unwrap_or(false);
        let quarantine = if has_destructive {
            "  ⚠ has quarantined destructive ops"
        } else {
            ""
        };
        if applied.contains(&id) {
            println!("  ✓ {id}  applied{quarantine}");
        } else {
            pending += 1;
            println!("  ○ {id}  PENDING{quarantine}");
        }
    }
    if pending == 0 {
        println!("\nup to date.");
        ExitCode::SUCCESS
    } else {
        println!("\n{pending} pending — run `sky db migrate`.");
        ExitCode::from(1)
    }
}

/// `sky db seed` — run the entry module's `seed : Db -> Task Error ()` against the
/// live DB (after `sky db migrate`). The project opts in by defining + exposing
/// `seed`; absence is a clear build error.
fn cmd_db_seed(_args: &[String]) -> ExitCode {
    let file = Path::new("src/Main.sky");
    let entry_module = entry_module_name(file).unwrap_or_else(|| "Main".into());
    let code = format!(
        r#"module SkyDbSeed exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Std.Db as Db
import {entry_module} exposing (seed)
import Std.Log exposing (println)


main : Task Error ()
main =
    Db.connect ()
        |> Task.andThen seed
        |> Task.andThen done


done : () -> Task Error ()
done _ =
    let
        _ =
            println "sky db seed: done"
    in
    Task.succeed ()
"#
    );
    let Some((bin, project_dir, _scratch)) = build_temp_db_entry("sky db seed", "SkyDbSeed", "_skydbseed.sky", &code)
    else {
        eprintln!(
            "sky db seed: your entry module must define + expose `seed : Db -> Task Error ()`."
        );
        return ExitCode::FAILURE;
    };
    match Command::new(&bin).current_dir(&project_dir).status() {
        Ok(s) if s.success() => ExitCode::SUCCESS,
        Ok(_) => ExitCode::FAILURE,
        Err(e) => {
            eprintln!("sky db seed: run failed: {e}");
            ExitCode::FAILURE
        }
    }
}

/// `sky db push` — sync the live DB to the current types with NO migration files:
/// create each missing table + add new columns for every store in `db :
/// Store.Project`. The fast dev loop (Prisma-style `db push`); production uses the
/// committed `db/migrations/` via `sky db migrate`.
fn cmd_db_push(_args: &[String]) -> ExitCode {
    let file = Path::new("src/Main.sky");
    let entry_module = entry_module_name(file).unwrap_or_else(|| "Main".into());
    let code = format!(
        r#"module SkyDbPush exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Sky.Core.String as String
import Sky.Core.List as List
import Std.Db as Db
import Std.Db.Store as Store
import {entry_module} exposing (db)
import Std.Log exposing (println)


main : Task Error ()
main =
    Db.connect ()
        |> Task.andThen (\conn -> Store.pushProject conn db)
        |> Task.andThen report


report : List String -> Task Error ()
report applied =
    let
        _ =
            println ("sky db push: applied " ++ String.fromInt (List.length applied) ++ " change(s)")
    in
    Task.succeed ()
"#
    );
    let Some((bin, project_dir, _scratch)) = build_temp_db_entry("sky db push", "SkyDbPush", "_skydbpush.sky", &code)
    else {
        eprintln!("sky db push: your entry module must expose `db : Store.Project`.");
        return ExitCode::FAILURE;
    };
    match Command::new(&bin).current_dir(&project_dir).status() {
        Ok(s) if s.success() => ExitCode::SUCCESS,
        Ok(_) => ExitCode::FAILURE,
        Err(e) => {
            eprintln!("sky db push: run failed: {e}");
            ExitCode::FAILURE
        }
    }
}

/// Whether the DB destructive verb is `reset` (empty data, keep schema) or `drop`
/// (remove tables + the ledger).
#[derive(Clone, Copy, PartialEq)]
enum DbDestructive {
    Reset,
    Drop,
}

impl DbDestructive {
    fn verb(self) -> &'static str {
        match self {
            DbDestructive::Reset => "reset",
            DbDestructive::Drop => "drop",
        }
    }
}

/// Read the DB driver from `sky.toml` for the confirmation prompt. Mirrors
/// `read_sky_toml_config`'s `[database]` handling: `driver` (default `sqlite`);
/// a `postgres://`/`postgresql://` DSN in `path`/`url` also implies postgres.
fn db_driver_label() -> String {
    let text = match std::fs::read_to_string("sky.toml") {
        Ok(t) => t,
        Err(_) => return "sqlite".to_string(),
    };
    let mut section = String::new();
    let mut driver: Option<String> = None;
    let mut dsn: Option<String> = None;
    for raw in text.lines() {
        let line = raw.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        if line.starts_with('[') && line.ends_with(']') {
            section = line.trim_matches(['[', ']']).trim().trim_matches('"').to_string();
            continue;
        }
        let Some((k, v)) = line.split_once('=') else {
            continue;
        };
        let (key, val) = (k.trim(), v.trim().trim_matches('"').to_string());
        if section == "database" {
            match key {
                "driver" => driver = Some(val),
                "path" | "url" => dsn = Some(val),
                _ => {}
            }
        }
    }
    // The DSN decides, because the DSN is what the runtime decides from
    // (`rt.detectDriver`). The declared `[database] driver` used to WIN here,
    // which made this prompt lie in exactly the dangerous direction: with
    // `driver = "postgres"` beside `./app.db` it announced "this will drop
    // everything in postgres" while the drop ran against SQLite. The declared
    // key is only a fallback for when no DSN is configured in sky.toml.
    let d = match dsn.as_deref() {
        Some(s) => project::driver_for_dsn(s).to_string(),
        None => driver.unwrap_or_else(|| "sqlite".into()),
    };
    match d.to_lowercase().as_str() {
        "postgres" | "postgresql" | "pgx" | "pg" => "postgres".to_string(),
        _ => "sqlite".to_string(),
    }
}

/// True when the runtime environment reads as production — refuse a destructive
/// DB op there unless `--yes` is explicit. Reuses the runtime's gate wording:
/// `ENV` then `SKY_ENV`; production when in {production, prod, staging}.
fn is_production_env() -> bool {
    let raw = std::env::var("ENV")
        .ok()
        .or_else(|| std::env::var("SKY_ENV").ok())
        .unwrap_or_default();
    matches!(raw.to_lowercase().as_str(), "production" | "prod" | "staging")
}

/// `sky db reset [table]` / `sky db drop [table]` — destructive data/schema
/// wipes over the project's declared `db : Store.Project`. `reset` EMPTIES the
/// tables (keeps schema + `_sky_migrations`, resets autoincrement); `drop`
/// removes the tables (drop-all also removes `_sky_migrations` for a fresh
/// "never migrated" state). A positional `table` scopes to that one table.
///
/// The confirmation prompt + `--yes` parsing + production guard live here, BEFORE
/// building/running the generated Sky entry (which imports the project's `db` for
/// the all-tables case, or calls the single-table verb directly).
fn cmd_db_reset_drop(args: &[String], op: DbDestructive) -> ExitCode {
    let verb = op.verb();
    // Split flags from the optional positional table name.
    let mut assume_yes = false;
    let mut table: Option<String> = None;
    for a in args {
        match a.as_str() {
            "--yes" | "-y" => assume_yes = true,
            s if s.starts_with('-') => {
                eprintln!("sky db {verb}: unknown flag `{s}`");
                return ExitCode::from(2);
            }
            s => {
                if table.is_some() {
                    eprintln!("sky db {verb}: too many arguments (expected at most one table name)");
                    return ExitCode::from(2);
                }
                if !s.chars().all(|c| c.is_ascii_alphanumeric() || c == '_') || s.is_empty() {
                    eprintln!("sky db {verb}: invalid table name `{s}` (only [A-Za-z0-9_])");
                    return ExitCode::from(2);
                }
                table = Some(s.to_string());
            }
        }
    }

    let driver = db_driver_label();

    // Determine the table count for the prompt. Single-table → 1. All-tables →
    // build the entry once and run it in info mode to read the project's count.
    let entry_module = entry_module_name(Path::new("src/Main.sky")).unwrap_or_else(|| "Main".into());
    let single = table.is_some();
    let code = gen_db_reset_drop_entry(op, &entry_module, table.as_deref());
    let module = if op == DbDestructive::Reset { "SkyDbReset" } else { "SkyDbDrop" };
    let filename = if op == DbDestructive::Reset { "_skydbreset.sky" } else { "_skydbdrop.sky" };
    let label = format!("sky db {verb}");
    let Some((bin, project_dir, _scratch)) = build_temp_db_entry(&label, module, filename, &code) else {
        eprintln!(
            "sky db {verb}: your entry module must expose `db : Store.Project`{}.",
            if single { " (or pass a table name)" } else { "" }
        );
        return ExitCode::FAILURE;
    };

    let count: usize = if single {
        1
    } else {
        match run_db_entry_count(&bin, &project_dir) {
            Some(n) => n,
            None => {
                eprintln!("sky db {verb}: could not determine the project's table count");
                return ExitCode::FAILURE;
            }
        }
    };

    if count == 0 {
        println!("sky db {verb}: no tables to {verb}.");
        return ExitCode::SUCCESS;
    }

    // Production guard + confirmation.
    if !assume_yes {
        if is_production_env() {
            eprintln!(
                "sky db {verb}: refusing to run in production (ENV/SKY_ENV) without --yes."
            );
            return ExitCode::FAILURE;
        }
        if !std::io::stdin().is_terminal() {
            eprintln!(
                "sky db {verb}: not a TTY — pass --yes to confirm this destructive operation."
            );
            return ExitCode::FAILURE;
        }
        let scope = match &table {
            Some(t) => format!("table \"{t}\""),
            None => format!("{count} table(s)"),
        };
        print!("This will {verb} {scope} in {driver} — type 'yes' to continue: ");
        use std::io::Write;
        let _ = std::io::stdout().flush();
        let mut answer = String::new();
        if std::io::stdin().read_line(&mut answer).is_err() || answer.trim() != "yes" {
            println!("sky db {verb}: aborted.");
            return ExitCode::FAILURE;
        }
    }

    // Apply.
    match Command::new(&bin)
        .current_dir(&project_dir)
        .env("SKY_DB_MODE", "apply")
        .status()
    {
        Ok(s) if s.success() => ExitCode::SUCCESS,
        Ok(_) => ExitCode::FAILURE,
        Err(e) => {
            eprintln!("sky db {verb}: run failed: {e}");
            ExitCode::FAILURE
        }
    }
}

/// Run the already-built entry in info mode and parse `__SKY_DB_COUNT__ <n>`.
fn run_db_entry_count(bin: &Path, project_dir: &Path) -> Option<usize> {
    let out = Command::new(bin)
        .current_dir(project_dir)
        .env("SKY_DB_MODE", "info")
        .output()
        .ok()?;
    let text = String::from_utf8_lossy(&out.stdout);
    for line in text.lines() {
        if let Some(rest) = line.trim().strip_prefix("__SKY_DB_COUNT__ ") {
            if let Ok(n) = rest.trim().parse::<usize>() {
                return Some(n);
            }
        }
    }
    None
}

/// Generate the temp Sky entry for `sky db reset` / `sky db drop`. In `info` mode
/// it prints `__SKY_DB_COUNT__ <n>` (no DB connection); in `apply` mode
/// (SKY_DB_MODE=apply) it connects and runs the reset/drop, reporting the applied
/// statement count. The single-table variant needs no project `db` binding.
fn gen_db_reset_drop_entry(op: DbDestructive, entry_module: &str, table: Option<&str>) -> String {
    let verb = op.verb();
    match table {
        Some(name) => {
            // Single table — no `db` import needed.
            let call = match op {
                DbDestructive::Reset => format!("Store.resetTable conn \"{name}\""),
                DbDestructive::Drop => format!("Store.dropTable conn \"{name}\""),
            };
            format!(
                r#"module {module} exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Sky.Core.String as String
import Sky.Core.List as List
import Sky.Core.System as System
import Std.Db as Db
import Std.Db.Store as Store
import Std.Log exposing (println)


main : Task Error ()
main =
    if System.getenvOr "SKY_DB_MODE" "info" == "apply" then
        Db.connect ()
            |> Task.andThen (\conn -> {call})
            |> Task.andThen report
    else
        info


info : Task Error ()
info =
    let
        _ =
            println "__SKY_DB_COUNT__ 1"
    in
    Task.succeed ()


report : List String -> Task Error ()
report applied =
    let
        _ =
            println ("sky db {verb}: applied " ++ String.fromInt (List.length applied) ++ " statement(s)")
    in
    Task.succeed ()
"#,
                module = if op == DbDestructive::Reset { "SkyDbReset" } else { "SkyDbDrop" },
            )
        }
        None => {
            // All declared tables — import the project's `db : Store.Project`.
            let call = match op {
                DbDestructive::Reset => "Store.resetProject conn db",
                DbDestructive::Drop => "Store.dropProject conn db",
            };
            format!(
                r#"module {module} exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Sky.Core.String as String
import Sky.Core.List as List
import Sky.Core.System as System
import Std.Db as Db
import Std.Db.Store as Store
import {entry_module} exposing (db)
import Std.Log exposing (println)


main : Task Error ()
main =
    if System.getenvOr "SKY_DB_MODE" "info" == "apply" then
        Db.connect ()
            |> Task.andThen (\conn -> {call})
            |> Task.andThen report
    else
        info


info : Task Error ()
info =
    let
        _ =
            println ("__SKY_DB_COUNT__ " ++ String.fromInt (Store.projectTableCount db))
    in
    Task.succeed ()


report : List String -> Task Error ()
report applied =
    let
        _ =
            println ("sky db {verb}: applied " ++ String.fromInt (List.length applied) ++ " statement(s)")
    in
    Task.succeed ()
"#,
                module = if op == DbDestructive::Reset { "SkyDbReset" } else { "SkyDbDrop" },
            )
        }
    }
}

/// `sky db status` / `sky db migrate` — build the project, then run it once with
/// `SKY_DB_OP` set so the runtime's `Db.migrate` reports/applies migrations and
/// exits before serving. Mirrors `app/Main.hs`'s `Db` handler (which sets the
/// same env var and runs the project). The Std.Db migration engine lives in the
/// Go runtime, so this is a thin build+run+env wrapper — no separate rust DB
/// introspection is needed.
/// `sky config migrate [--dry-run|--check]` — rewrite a legacy `sky.toml`'s
/// runtime keys into a typed `config` binding (+ `Live.withX` pipeline), reusing
/// the ONE `project::config_migration::MIGRATIONS` table. Operates on the
/// current directory's project.
fn cmd_config(args: &[String]) -> ExitCode {
    match args.first().map(String::as_str) {
        Some("migrate") => cmd_config_migrate(&args[1..]),
        Some(other) => {
            eprintln!("sky config: unknown subcommand `{other}`. Try `sky config migrate`.");
            ExitCode::from(2)
        }
        None => {
            eprintln!("sky config: missing subcommand. Usage: `sky config migrate [--dry-run|--check]`.");
            ExitCode::from(2)
        }
    }
}

fn cmd_config_migrate(args: &[String]) -> ExitCode {
    use project::config_migrate::{self, Mode};
    let check = args.iter().any(|a| a == "--check");
    let dry_run = args.iter().any(|a| a == "--dry-run");
    if check && dry_run {
        eprintln!("sky config migrate: --check and --dry-run are mutually exclusive.");
        return ExitCode::from(2);
    }
    let mode = if check {
        Mode::Check
    } else if dry_run {
        Mode::DryRun
    } else {
        Mode::Apply
    };
    let project_dir = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));

    let outcome = match config_migrate::run(&project_dir, mode) {
        Ok(o) => o,
        Err(e) => {
            eprintln!("sky config migrate: {e}");
            return ExitCode::FAILURE;
        }
    };

    if check {
        if outcome.clean {
            println!("sky config migrate --check: clean — no legacy sky.toml runtime keys.");
            return ExitCode::SUCCESS;
        }
        eprintln!(
            "sky config migrate --check: {} legacy runtime key(s) still in sky.toml:",
            outcome.legacy_count
        );
        for line in &outcome.summary {
            eprintln!("{line}");
        }
        eprintln!("Run `sky config migrate` to move them into a typed `config` binding.");
        return ExitCode::FAILURE;
    }

    if outcome.clean {
        println!("sky config migrate: nothing to do — no legacy sky.toml runtime keys.");
        return ExitCode::SUCCESS;
    }

    if dry_run {
        println!("sky config migrate --dry-run — {} legacy key(s), no files written:\n", outcome.legacy_count);
        for line in &outcome.summary {
            println!("{line}");
        }
        println!("\n{}", outcome.diff);
        return ExitCode::SUCCESS;
    }

    // Apply.
    println!("sky config migrate — moved {} legacy key(s) into typed config:", outcome.legacy_count);
    for line in &outcome.summary {
        println!("{line}");
    }
    if outcome.wrote {
        println!("\nWrote sky.toml and the entry module. Review with `git diff`, then `sky check`.");
    }
    ExitCode::SUCCESS
}

fn cmd_db(args: &[String]) -> ExitCode {
    // `sky db migrate --gen [name]` — file-based migration generation (no DB).
    if args.first().map(String::as_str) == Some("migrate") && args.iter().any(|a| a == "--gen") {
        return cmd_db_gen(&args[1..]);
    }
    // `sky db init` — scaffold the file-based migration layout.
    if args.first().map(String::as_str) == Some("init") {
        return cmd_db_init();
    }
    // Cluster supervision (embedded-Postgres phase 2). These are the ONLY `sky db`
    // verbs that do not build the project: they manage the PostgreSQL process the
    // project talks to, not its schema. `start`/`stop`/`ps` rather than the
    // obvious `status`, because `sky db status` and `sky db init` already belong
    // to the migration engine above and quietly changing what they mean would
    // break every project using them.
    match args.first().map(String::as_str) {
        Some("start") => return db_cluster::cmd_start(&args[1..]),
        Some("stop") => return db_cluster::cmd_stop(&args[1..]),
        Some("ps") => return db_cluster::cmd_ps(&args[1..]),
        // `sky db provision --embed` — fetch the PostgreSQL bundle into
        // ~/.sky/postgres/<version>, which is the middle entry of the discovery
        // order above. It is grouped with the cluster verbs, not the migration
        // ones, for the same reason: it manages the SERVER, not the schema.
        Some("provision") => return db_provision::cmd_provision(&args[1..]),
        _ => {}
    }
    let file_based = Path::new("db").join("migrations").is_dir();
    // `sky db migrate` in a file-based project (db/migrations/ present) → apply the
    // committed migration files.
    if args.first().map(String::as_str) == Some("migrate") && file_based {
        return cmd_db_apply(&args[1..]);
    }
    // `sky db status` in a file-based project → compare committed files vs the ledger.
    if args.first().map(String::as_str) == Some("status") && file_based {
        return cmd_db_status(&args[1..]);
    }
    // `sky db seed` — run the entry module's `seed : Db -> Task Error ()`.
    if args.first().map(String::as_str) == Some("seed") {
        return cmd_db_seed(&args[1..]);
    }
    // `sky db push` — sync the live DB to the types with no migration files.
    if args.first().map(String::as_str) == Some("push") {
        return cmd_db_push(&args[1..]);
    }
    // `sky db reset [table]` — empty data from the declared tables (keep schema).
    if args.first().map(String::as_str) == Some("reset") {
        return cmd_db_reset_drop(&args[1..], DbDestructive::Reset);
    }
    // `sky db drop [table]` — drop the declared tables (+ the ledger for drop-all).
    if args.first().map(String::as_str) == Some("drop") {
        return cmd_db_reset_drop(&args[1..], DbDestructive::Drop);
    }
    let op = match args.first().map(String::as_str) {
        Some("status") => "status",
        Some("migrate") => "migrate",
        _ => {
            eprintln!(
                "usage: sky db <status|migrate [--gen [name]]|push|seed|reset [table]|drop [table]|init> [file.sky]\n\
                 \x20      sky db <start|stop [--all]|ps [--all]>    local PostgreSQL cluster\n\
                 \x20      sky db provision --embed                  fetch PostgreSQL into ~/.sky\n\
                 \x20      sky db provision --shared [--service]     one shared cluster for this host\n\
                 \x20      sky db provision --shared --app <name>    a database + role for one app"
            );
            return ExitCode::from(2);
        }
    };
    let (positional, out_override) = parse_out(&args[1..]);
    let file = positional
        .first()
        .cloned()
        .unwrap_or_else(|| "src/Main.sky".to_string());
    let file = Path::new(&file);
    let Some((repo_root, project_dir)) = resolve(file) else {
        return ExitCode::FAILURE;
    };
    if is_compiler_repo_root(&project_dir) && out_override.is_none() {
        eprintln!("sky db: refusing to run from the Sky compiler repo root");
        return ExitCode::FAILURE;
    }
    let out_dir_name = out_override.unwrap_or_else(|| "sky-out".to_string());
    let opts = BuildOptions {
        repo_root,
        example_dir: project_dir.clone(),
        out_dir_name: out_dir_name.clone(),
        out_dir_abs: None,
        run: false,
        stdin: None,
        entry_module: entry_module_name(file),
        progress: false,
        embed_bundle: None,
        wasm: false,
    };
    let report = build_example(&opts);
    for w in &report.warnings {
        eprintln!("warning: {w}");
    }
    if !report.emitted {
        eprintln!("sky db: {}", report.note);
        return ExitCode::FAILURE;
    }
    if !report.go_build_ok {
        eprintln!("sky db: go build failed:\n{}", report.go_build_stderr);
        return ExitCode::FAILURE;
    }
    let out_dir = project_dir.join(&out_dir_name);
    match run_app(&out_dir, &[("SKY_DB_OP".to_string(), op.to_string())]) {
        Ok(status) => ExitCode::from(status.code().unwrap_or(1) as u8),
        Err(e) => {
            eprintln!("sky db: could not launch binary: {e}");
            ExitCode::FAILURE
        }
    }
}

// ---- watch ---------------------------------------------------------------

/// `sky watch <file>` — file-watch the entry dir (+ `tests/` + `sky.toml`),
/// rebuild + restart the app on any `.sky`/`sky.toml` change. Generated trees
/// (`sky-out`, `.skycache`, `.skydeps`, `dist-newstyle`, `.git`, `node_modules`)
/// are excluded (Watch.hs's strict allowlist). Build-error policy: a failing
/// rebuild leaves the previously-running binary alive; the next successful
/// rebuild replaces it. Long-running by design; exits on Ctrl-C.
fn cmd_watch(args: &[String]) -> ExitCode {
    use std::sync::mpsc::channel;
    use std::time::{Duration, Instant};

    let opts = match WatchOpts::parse(args) {
        Ok(o) => o,
        Err(msg) => {
            eprintln!("{msg}");
            return ExitCode::from(2);
        }
    };
    let Some(file) = opts.file.as_deref() else {
        eprintln!(
            "usage: sky watch <file.sky> [--no-run] [--clear] [--debounce=MS]\n       \
             [--interval=MS] [--kill-timeout=MS] [--watch=PATH ...]"
        );
        return ExitCode::from(2);
    };
    let no_run = opts.no_run;
    let file = Path::new(file);
    let Some((repo_root, project_dir)) = resolve(file) else {
        return ExitCode::FAILURE;
    };
    if is_compiler_repo_root(&project_dir) {
        eprintln!("sky watch: refusing to run from the Sky compiler repo root");
        return ExitCode::FAILURE;
    }
    // ONE lease for the whole watch session, not one per rebuild: restarting the
    // app must not cycle its database underneath it. Held until the loop ends.
    // Unlike `sky run`, the cluster is taken before the first build, because a
    // watch session survives a failing build and keeps watching.
    let cluster = match db_cluster::check_run_config(&project_dir, "sky watch")
        .and_then(|on| if on { db_cluster::acquire_for_run(&project_dir).map(Some) } else { Ok(None) })
    {
        Ok(c) => c,
        Err(e) => {
            eprintln!("{e}");
            return ExitCode::FAILURE;
        }
    };
    let app_envs: Vec<(String, String)> = cluster
        .as_ref()
        .map(|c| {
            println!("{}", c.banner("[watch]"));
            c.envs()
        })
        .unwrap_or_default();

    // Watched roots: the entry's directory, the project's tests/ (if present),
    // the project root (to catch sky.toml), plus any `--watch=PATH` extras.
    // notify watches recursively; the event filter prunes generated dirs +
    // non-source files.
    let entry_dir = file
        .parent()
        .map(Path::to_path_buf)
        .unwrap_or_else(|| project_dir.clone());
    let mut roots: Vec<PathBuf> = vec![entry_dir.clone()];
    let tests_dir = project_dir.join("tests");
    if tests_dir.is_dir() {
        roots.push(tests_dir);
    }
    // The project root covers sky.toml; only add it if it isn't already covered.
    if !roots.iter().any(|r| project_dir.starts_with(r)) {
        roots.push(project_dir.clone());
    }
    for extra in &opts.extra_watch {
        roots.push(extra.clone());
    }
    roots.sort();
    roots.dedup();

    let (tx, rx) = channel::<()>();
    let handler = {
        let tx = tx.clone();
        move |res: notify::Result<notify::Event>| {
            if let Ok(event) = res {
                if event.paths.iter().any(|p| is_watched_change(p)) {
                    let _ = tx.send(());
                }
            }
        }
    };
    // `--interval=MS` selects the polling backend (meaningful on network / fuse
    // filesystems where native fs-events don't fire); otherwise the superior
    // event-driven backend. Boxed behind `dyn Watcher` so both share one path.
    let mut watcher: Box<dyn notify::Watcher> = match opts.interval_ms {
        Some(ms) => {
            let cfg = notify::Config::default().with_poll_interval(Duration::from_millis(ms));
            match notify::PollWatcher::new(handler, cfg) {
                Ok(w) => Box::new(w),
                Err(e) => {
                    eprintln!("sky watch: could not create poll watcher: {e}");
                    return ExitCode::FAILURE;
                }
            }
        }
        None => match notify::recommended_watcher(handler) {
            Ok(w) => Box::new(w),
            Err(e) => {
                eprintln!("sky watch: could not create file watcher: {e}");
                return ExitCode::FAILURE;
            }
        },
    };
    for root in &roots {
        if let Err(e) = watcher.watch(root, notify::RecursiveMode::Recursive) {
            eprintln!("sky watch: could not watch {}: {e}", root.display());
        }
    }

    println!(
        "[watch] watching {} for changes (Ctrl-C to stop)",
        entry_dir.display()
    );
    let mut child = watch_build_and_spawn(&repo_root, &project_dir, file, no_run, &app_envs);

    // Debounce loop: coalesce a burst of save events, rebuild once.
    loop {
        // Block for the first change.
        if rx.recv().is_err() {
            break;
        }
        // Drain further events for the debounce window (`--debounce=MS`).
        let deadline = Instant::now() + Duration::from_millis(opts.debounce_ms);
        while let Some(remaining) = deadline.checked_duration_since(Instant::now()) {
            if rx.recv_timeout(remaining).is_err() {
                break;
            }
        }
        if opts.clear {
            // Clear screen + home cursor (ANSI) so each rebuild starts fresh.
            use std::io::Write as _;
            print!("\x1b[2J\x1b[H");
            let _ = std::io::stdout().flush();
        }
        println!("[watch] change detected — rebuilding…");
        // Build-error policy: only replace the running child when the rebuild
        // produced a fresh binary. A failing rebuild returns None → the old
        // binary keeps running.
        if let Some(fresh) = watch_build_and_spawn(&repo_root, &project_dir, file, no_run, &app_envs)
        {
            if let Some(old) = child.take() {
                terminate_child(old, opts.kill_timeout_ms);
            }
            child = Some(fresh);
        }
    }
    if let Some(c) = child.take() {
        terminate_child(c, 0);
    }
    ExitCode::SUCCESS
}

/// Parsed `sky watch` options. Mirrors the oracle's `watchOptsParser` +
/// `docs/tooling/cli.md` §watch: `--no-run`, `--clear`, `--debounce=MS`,
/// `--interval=MS`, `--kill-timeout=MS`, and repeatable `--watch=PATH`.
struct WatchOpts {
    file: Option<String>,
    no_run: bool,
    clear: bool,
    debounce_ms: u64,
    interval_ms: Option<u64>,
    kill_timeout_ms: u64,
    extra_watch: Vec<PathBuf>,
}

impl WatchOpts {
    fn parse(args: &[String]) -> Result<WatchOpts, String> {
        let mut o = WatchOpts {
            file: None,
            no_run: false,
            clear: false,
            debounce_ms: 150,
            interval_ms: None,
            kill_timeout_ms: 5000,
            extra_watch: Vec::new(),
        };
        for a in args {
            if let Some(v) = a.strip_prefix("--debounce=") {
                o.debounce_ms = v
                    .parse()
                    .map_err(|_| format!("sky watch: invalid --debounce value: {v}"))?;
            } else if let Some(v) = a.strip_prefix("--interval=") {
                o.interval_ms = Some(
                    v.parse()
                        .map_err(|_| format!("sky watch: invalid --interval value: {v}"))?,
                );
            } else if let Some(v) = a.strip_prefix("--kill-timeout=") {
                o.kill_timeout_ms = v
                    .parse()
                    .map_err(|_| format!("sky watch: invalid --kill-timeout value: {v}"))?;
            } else if let Some(v) = a.strip_prefix("--watch=") {
                o.extra_watch.push(PathBuf::from(v));
            } else if a == "--no-run" {
                o.no_run = true;
            } else if a == "--clear" {
                o.clear = true;
            } else if a.starts_with('-') {
                return Err(format!("sky watch: unknown flag: {a}"));
            } else if o.file.is_none() {
                o.file = Some(a.clone());
            }
        }
        Ok(o)
    }
}

/// Terminate a spawned child, honouring `--kill-timeout` (SIGTERM grace before
/// SIGKILL on Unix). A `timeout_ms` of 0 kills immediately (session teardown).
fn terminate_child(mut child: std::process::Child, timeout_ms: u64) {
    #[cfg(unix)]
    if timeout_ms > 0 {
        // Ask the child to exit cleanly first (SIGTERM), then wait up to the
        // grace window, escalating to SIGKILL only if it's still alive.
        let _ = nix::sys::signal::kill(
            nix::unistd::Pid::from_raw(child.id() as i32),
            nix::sys::signal::Signal::SIGTERM,
        );
        let deadline = std::time::Instant::now() + std::time::Duration::from_millis(timeout_ms);
        loop {
            match child.try_wait() {
                Ok(Some(_)) => return,
                Ok(None) => {
                    if std::time::Instant::now() >= deadline {
                        break;
                    }
                    std::thread::sleep(std::time::Duration::from_millis(20));
                }
                Err(_) => break,
            }
        }
    }
    let _ = child.kill();
    let _ = child.wait();
}

/// One watch iteration: build the entry, and on success (re)spawn the binary.
/// Returns the spawned child (`None` when the build failed or `--no-run`). On a
/// build failure it prints the error and returns `None` so the caller keeps the
/// previous binary alive.
fn watch_build_and_spawn(
    repo_root: &Path,
    project_dir: &Path,
    file: &Path,
    no_run: bool,
    app_envs: &[(String, String)],
) -> Option<std::process::Child> {
    let opts = BuildOptions {
        repo_root: repo_root.to_path_buf(),
        example_dir: project_dir.to_path_buf(),
        out_dir_name: "sky-out".to_string(),
        out_dir_abs: None,
        run: false,
        stdin: None,
        entry_module: entry_module_name(file),
        progress: false,
        embed_bundle: None,
        wasm: false,
    };
    let report = build_example(&opts);
    for w in &report.warnings {
        eprintln!("[watch] warning: {w}");
    }
    if !report.emitted {
        eprintln!(
            "[watch] build failed: {} (keeping previous binary)",
            report.note
        );
        return None;
    }
    if !report.go_build_ok {
        eprintln!(
            "[watch] go build failed (keeping previous binary):\n{}",
            report.go_build_stderr.trim()
        );
        return None;
    }
    println!("[watch] build ok");
    if no_run {
        return None;
    }
    let out_dir = project_dir.join("sky-out");
    let bin_name = project::configured_bin_name(project_dir);
    let mut cmd = Command::new(format!("./{bin_name}"));
    cmd.current_dir(&out_dir);
    // The embedded cluster's DSN, when the project has one. EVERY respawn gets
    // it: a rebuild replaces the process, and a replacement that lost its DSN
    // would fail to connect while the cluster it was meant to use sat running.
    for (k, v) in app_envs {
        cmd.env(k, v);
    }
    match cmd.spawn() {
        Ok(child) => Some(child),
        Err(e) => {
            eprintln!("[watch] could not launch binary: {e}");
            None
        }
    }
}

/// True when a changed path is a source file the watcher cares about: a `.sky`
/// file or `sky.toml`, and not inside a generated / VCS directory.
fn is_watched_change(path: &Path) -> bool {
    let excluded = path.components().any(|c| {
        matches!(
            c.as_os_str().to_str(),
            Some("sky-out")
                | Some("sky-out-rust")
                | Some(".skycache")
                | Some(".skydeps")
                | Some("dist-newstyle")
                | Some(".git")
                | Some("node_modules")
                | Some(".vscode")
                | Some(".idea")
        )
    });
    if excluded {
        return false;
    }
    let is_sky = path.extension().and_then(|e| e.to_str()) == Some("sky");
    let is_toml = path.file_name().and_then(|n| n.to_str()) == Some("sky.toml");
    is_sky || is_toml
}

// ---- FFI verbs (add / remove / install / update) -------------------------

use project::{
    ffi_add, ffi_add_sky, ffi_add_smart, ffi_install, ffi_remove, ffi_remove_sky, ffi_remove_smart,
    ffi_update, FfiReport,
};

/// Resolve `(repo_root, project_dir)` for an FFI verb run from the cwd. The
/// project dir is the cwd (where `sky.toml` + `sky-out/` live, matching the
/// oracle's cwd-relative behaviour); the repo root supplies the stdlib +
/// `tools/sky-ffi-inspect` source (bring-up reads assets from the repo tree).
fn resolve_ffi_ctx() -> Option<(PathBuf, PathBuf)> {
    let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
    // Dev reads the inspector source + runtime from the repo tree; standalone
    // extracts the embedded copy (ensure_inspector then `go build`s it, so FFI
    // works outside the repo). See doc 09 §E / §C.3.
    let repo_root = assets_root_for(&cwd)?;
    Some((repo_root, cwd))
}

fn emit_ffi_report(r: FfiReport) -> ExitCode {
    for line in &r.lines {
        println!("{line}");
    }
    if r.ok {
        ExitCode::SUCCESS
    } else {
        ExitCode::FAILURE
    }
}

fn cmd_add(args: &[String]) -> ExitCode {
    let force_go = args.iter().any(|a| a == "--go");
    let force_sky = args.iter().any(|a| a == "--sky");
    if force_go && force_sky {
        eprintln!("sky add: choose one of --go / --sky, not both");
        return ExitCode::from(2);
    }
    let Some(raw) = args.iter().find(|a| !a.starts_with('-')) else {
        eprintln!("usage: sky add [--go|--sky] <import-path>[@version]");
        return ExitCode::from(2);
    };
    // Split an optional version off the LAST `@` — import paths never contain one,
    // so `github.com/foo/bar@v1.2.3` → (`github.com/foo/bar`, `v1.2.3`).
    let (pkg, spec) = match raw.rfind('@') {
        Some(i) => (&raw[..i], Some(&raw[i + 1..])),
        None => (raw.as_str(), None),
    };
    let Some((repo_root, project_dir)) = resolve_ffi_ctx() else {
        return ExitCode::FAILURE;
    };
    // Routing: `--go` forces the Go FFI path ([go.dependencies] + sky-ffi/);
    // `--sky` forces the Sky external-package path ([dependencies] + .skydeps/);
    // neither → smart-resolve (Go-first probe, Sky on miss).
    let report = match (force_go, force_sky) {
        (true, _) => ffi_add(&project_dir, &repo_root, pkg, spec),
        (_, true) => ffi_add_sky(&project_dir, pkg, spec),
        (false, false) => ffi_add_smart(&project_dir, &repo_root, pkg, spec),
    };
    emit_ffi_report(report)
}

fn cmd_remove(args: &[String]) -> ExitCode {
    let force_go = args.iter().any(|a| a == "--go");
    let force_sky = args.iter().any(|a| a == "--sky");
    if force_go && force_sky {
        eprintln!("sky remove: choose one of --go / --sky, not both");
        return ExitCode::from(2);
    }
    let Some(pkg) = args.iter().find(|a| !a.starts_with('-')) else {
        eprintln!("usage: sky remove [--go|--sky] <import-path>");
        return ExitCode::from(2);
    };
    let Some((_repo_root, project_dir)) = resolve_ffi_ctx() else {
        return ExitCode::FAILURE;
    };
    // `--go`/`--sky` force a path; neither → route by which sky.toml section
    // declares the package (deterministic, local — no probe needed for remove).
    let report = match (force_go, force_sky) {
        (true, _) => ffi_remove(&project_dir, pkg),
        (_, true) => ffi_remove_sky(&project_dir, pkg),
        (false, false) => ffi_remove_smart(&project_dir, pkg),
    };
    emit_ffi_report(report)
}

fn cmd_install(_args: &[String]) -> ExitCode {
    let Some((repo_root, project_dir)) = resolve_ffi_ctx() else {
        return ExitCode::FAILURE;
    };
    emit_ffi_report(ffi_install(&project_dir, &repo_root))
}

fn cmd_update(_args: &[String]) -> ExitCode {
    let Some((repo_root, project_dir)) = resolve_ffi_ctx() else {
        return ExitCode::FAILURE;
    };
    emit_ffi_report(ffi_update(&project_dir, &repo_root))
}

// ---- doctor --------------------------------------------------------------

/// Severity of a doctor finding — drives the output prefix and the exit code.
#[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Debug)]
enum Severity {
    Info,
    Warn,
    Error,
}

/// A single diagnostic finding. Mirrors `Sky.Cli.Doctor.Finding`
/// (`src/Sky/Cli/Doctor.hs`): a short id, severity, message, a one-line manual
/// hint, and an optional safe auto-fix applied only under `--fix`.
struct Finding {
    check: &'static str,
    severity: Severity,
    message: String,
    hint: String,
    fix: Option<Fix>,
}

/// A safe remediation `--fix` may apply. Kept to non-destructive-to-source
/// actions (delete a regenerable cache dir, regen FFI) — never touches user
/// source or `sky.toml` (the oracle's invariant, `Doctor.hs` header).
enum Fix {
    RemoveDir(PathBuf),
    Install,
    /// Pre-warm `~/.sky/postgres/<version>` for a project that has opted into an
    /// embedded cluster. Network-touching, like `Install` (which fetches Go
    /// modules); source-preserving, unlike an edit to `sky.toml` — the pin is
    /// deliberately not written by a `--fix`.
    ProvisionPostgres,
}

/// `sky doctor [--fix] [--verbose|-v]` — port of `Sky.Cli.Doctor.runDoctor`.
/// Runs the tractable subset of the oracle's checks against the nearest project
/// root: sky.toml present + non-empty, entry file exists, Go toolchain ≥ 1.22,
/// stdlib/runtime assets resolvable, stale `.skycache`/`sky-out`, missing FFI
/// bindings for domain-style imports, and the `SKY_AUTH_TOKEN_SECRET` gate when
/// `[live]`/`[auth]` is configured. Exit 0 = clean, 1 = at least one finding,
/// 2 = no sky.toml visible (diagnostic couldn't run).
fn cmd_doctor(args: &[String]) -> ExitCode {
    let do_fix = args.iter().any(|a| a == "--fix");
    let verbose = args.iter().any(|a| a == "--verbose" || a == "-v");

    let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
    let Some(root) = locate_project_root(&cwd) else {
        eprintln!("sky doctor: no sky.toml found in current directory or any ancestor.");
        eprintln!("            (cd into a project root and re-run, or `sky init` to start one.)");
        return ExitCode::from(2);
    };

    println!("sky doctor — checking {}", root.display());
    println!();

    let mut findings = run_all_checks(&root);
    findings.sort_by_key(|f| f.severity); // Info first, Error last (stable).

    for f in &findings {
        let prefix = match f.severity {
            Severity::Info => "·",
            Severity::Warn => "⚠",
            Severity::Error => "✗",
        };
        println!("{prefix} {}", f.message);
        println!("   ↳ {}", f.hint);
        if verbose {
            println!("   ↳ check-id: {}", f.check);
        }
        println!();
    }

    let mut applied: Vec<String> = Vec::new();
    if do_fix {
        println!("─── applying fixes ─────────────────────────────────────");
        for f in &findings {
            if let Some(fix) = &f.fix {
                applied.push(apply_fix(&root, f.check, fix));
            }
        }
    }
    for line in &applied {
        println!("{line}");
    }
    println!();

    if findings.is_empty() {
        println!("✓ no issues found.");
        return ExitCode::SUCCESS;
    }
    let count = |s: Severity| findings.iter().filter(|f| f.severity == s).count();
    let (n_err, n_warn, n_info) = (
        count(Severity::Error),
        count(Severity::Warn),
        count(Severity::Info),
    );
    let parts: Vec<String> = [(n_err, "errors"), (n_warn, "warnings"), (n_info, "info")]
        .iter()
        .filter(|(n, _)| *n > 0)
        .map(|(n, label)| format!("{n} {label}"))
        .collect();
    let issues = if parts.is_empty() {
        "no issues".to_string()
    } else {
        parts.join(", ")
    };
    if do_fix {
        let n = applied.len();
        println!(
            "{issues}; applied {n} auto-fix{}.",
            if n == 1 { "" } else { "es" }
        );
    } else {
        println!("{issues} — run with --fix to auto-apply safe remediations.");
    }
    ExitCode::from(1)
}

/// Nearest ancestor of `start` (inclusive) containing `sky.toml`.
fn locate_project_root(start: &Path) -> Option<PathBuf> {
    let start = start.canonicalize().unwrap_or_else(|_| start.to_path_buf());
    let mut dir: Option<&Path> = Some(start.as_path());
    while let Some(d) = dir {
        if d.join("sky.toml").is_file() {
            return Some(d.to_path_buf());
        }
        dir = d.parent();
    }
    None
}

fn run_all_checks(root: &Path) -> Vec<Finding> {
    let mut out = Vec::new();
    out.extend(check_sky_toml(root));
    out.extend(check_entry_file(root));
    out.extend(check_go_toolchain());
    out.extend(check_assets(root));
    out.extend(check_stale_cache(root));
    out.extend(check_stale_build(root));
    out.extend(check_missing_ffi(root));
    out.extend(check_auth_secret(root));
    out.extend(check_embedded_postgres(root));
    out
}

/// A project opted into `[database] embedded = true` needs a PostgreSQL the
/// toolchain can supervise. Reporting that at `doctor` time — where the reader is
/// already asking "is this machine set up" — beats discovering it at the first
/// `sky run`, and the `--fix` pre-warms the cache so the first run is not also
/// the first download.
///
/// The fix deliberately does NOT record the pin: `Fix` is contracted to leave
/// user source and `sky.toml` alone, and pinning a version is a decision the
/// project makes, not a remediation.
fn check_embedded_postgres(root: &Path) -> Vec<Finding> {
    if !project::sky_toml_flag(root, "database", "embedded") {
        return Vec::new();
    }
    if db_cluster::postgres_is_discoverable(root) {
        return Vec::new();
    }
    let version = db_provision::pinned_version(root)
        .unwrap_or_else(|| db_provision::DEFAULT_PG_VERSION.to_string());
    vec![Finding {
        check: "embedded-postgres-missing",
        severity: Severity::Warn,
        message: format!(
            "[database] embedded = true, but no PostgreSQL {version} is available to \
             supervise (nothing at $SKY_POSTGRES_BIN, in ~/.sky/postgres, or on PATH)"
        ),
        hint: "run `sky db provision --embed` (or `sky doctor --fix`) to fetch one".into(),
        fix: Some(Fix::ProvisionPostgres),
    }]
}

/// sky.toml exists (root guarantees it) AND is non-empty / readable.
fn check_sky_toml(root: &Path) -> Vec<Finding> {
    let toml = root.join("sky.toml");
    match std::fs::metadata(&toml) {
        Err(e) => vec![Finding {
            check: "sky-toml-unreadable",
            severity: Severity::Error,
            message: format!("sky.toml could not be read: {e}"),
            hint: "ensure file permissions allow reading; recreate from `sky init` if corrupt"
                .into(),
            fix: None,
        }],
        Ok(m) if m.len() == 0 => vec![Finding {
            check: "sky-toml-empty",
            severity: Severity::Error,
            message: "sky.toml is empty".into(),
            hint: "minimal valid file:\n  name = \"myapp\"\n  entry = \"src/Main.sky\"".into(),
            fix: None,
        }],
        Ok(_) => Vec::new(),
    }
}

/// The entry `.sky` (sky.toml `entry`, default `src/Main.sky`) must exist.
fn check_entry_file(root: &Path) -> Vec<Finding> {
    let entry = toml_entry(root).unwrap_or_else(|| "src/Main.sky".to_string());
    let path = root.join(&entry);
    if path.is_file() {
        Vec::new()
    } else {
        vec![Finding {
            check: "entry-missing",
            severity: Severity::Error,
            message: format!("entry file `{entry}` does not exist"),
            hint: "create it, or fix the `entry = \"...\"` path in sky.toml".into(),
            fix: None,
        }]
    }
}

/// Go toolchain present + ≥ 1.22 (generics + range-over-func the runtime needs).
fn check_go_toolchain() -> Vec<Finding> {
    match Command::new("go").arg("version").output() {
        Err(_) => vec![Finding {
            check: "go-toolchain",
            severity: Severity::Error,
            message: "`go` not found on PATH".into(),
            hint: "install Go ≥ 1.22 (https://go.dev/dl/) and re-run".into(),
            fix: None,
        }],
        Ok(o) if o.status.success() => {
            let out = String::from_utf8_lossy(&o.stdout);
            match parse_go_version(&out) {
                Some((maj, minor)) if maj > 1 || (maj == 1 && minor >= 22) => Vec::new(),
                Some((maj, minor)) => vec![Finding {
                    check: "go-toolchain",
                    severity: Severity::Error,
                    message: format!("Go {maj}.{minor} is too old — Sky's runtime needs ≥ 1.22"),
                    hint: "upgrade Go: https://go.dev/dl/".into(),
                    fix: None,
                }],
                None => Vec::new(), // couldn't parse — don't false-positive.
            }
        }
        Ok(o) => vec![Finding {
            check: "go-toolchain",
            severity: Severity::Warn,
            message: format!(
                "`go version` failed: {}",
                String::from_utf8_lossy(&o.stderr)
                    .lines()
                    .next()
                    .unwrap_or("")
            ),
            hint: "check `go` is installed + on PATH".into(),
            fix: None,
        }],
    }
}

/// Parse the leading "go1.X.Y" from `go version` output → (major, minor).
fn parse_go_version(s: &str) -> Option<(u32, u32)> {
    let idx = s.find("go version go")? + "go version go".len();
    let rest = &s[idx..];
    let maj_str: String = rest.chars().take_while(|c| c.is_ascii_digit()).collect();
    let rest2 = &rest[maj_str.len()..];
    let min_str: String = rest2
        .strip_prefix('.')
        .map(|r| r.chars().take_while(|c| c.is_ascii_digit()).collect())
        .unwrap_or_default();
    Some((maj_str.parse().ok()?, min_str.parse().ok()?))
}

/// The stdlib + Go runtime asset root must be resolvable (repo tree or embedded).
/// Silent when healthy — only a missing asset root is a finding.
fn check_assets(root: &Path) -> Vec<Finding> {
    match assets_root_for(root) {
        Some(_) => Vec::new(),
        None => vec![Finding {
            check: "assets-root",
            severity: Severity::Error,
            message: "could not resolve the stdlib + Go runtime asset root".into(),
            hint: "run inside the Sky repo tree, or reinstall the `sky` binary (embedded assets missing)".into(),
            fix: None,
        }],
    }
}

/// `.skycache/` older than the newest `src/*.sky` → stale; safe to delete.
fn check_stale_cache(root: &Path) -> Vec<Finding> {
    let cache = root.join(".skycache");
    if !cache.is_dir() {
        return Vec::new();
    }
    match (newest_mtime(&cache), newest_sky_mtime(&root.join("src"))) {
        (Some(cm), Some(sm)) if sm > cm => vec![Finding {
            check: "stale-cache",
            severity: Severity::Warn,
            message: ".skycache/ is older than your src/*.sky files".into(),
            hint: "run `sky doctor --fix` to delete it (next build regenerates)".into(),
            fix: Some(Fix::RemoveDir(cache)),
        }],
        _ => Vec::new(),
    }
}

/// `sky-out/main.go` older than the newest `src/*.sky` → stale build (Info).
fn check_stale_build(root: &Path) -> Vec<Finding> {
    let out_dir = root.join("sky-out");
    let main_go = out_dir.join("main.go");
    if !main_go.is_file() {
        return Vec::new();
    }
    match (file_mtime(&main_go), newest_sky_mtime(&root.join("src"))) {
        (Some(gm), Some(sm)) if sm > gm => vec![Finding {
            check: "stale-build",
            severity: Severity::Info,
            message: "sky-out/main.go is older than your src/*.sky files".into(),
            hint: "run `sky build` to refresh, or `sky doctor --fix` to remove sky-out/".into(),
            fix: Some(Fix::RemoveDir(out_dir)),
        }],
        _ => Vec::new(),
    }
}

/// Domain-style imports (github.com/…, golang.org/…) with no matching cached
/// FFI surface → the build will fail with a cryptic "package not found".
fn check_missing_ffi(root: &Path) -> Vec<Finding> {
    let src = root.join("src");
    if !src.is_dir() {
        return Vec::new();
    }
    let mut imports: Vec<String> = Vec::new();
    let mut files = Vec::new();
    collect_sky_files(&src, &mut files);
    for f in &files {
        let Ok(c) = std::fs::read_to_string(f) else {
            continue;
        };
        for line in c.lines() {
            let mut it = line.split_whitespace();
            if it.next() == Some("import") {
                if let Some(pkg) = it.next() {
                    if is_ffi_path(pkg) && !imports.contains(&pkg.to_string()) {
                        imports.push(pkg.to_string());
                    }
                }
            }
        }
    }
    if imports.is_empty() {
        return Vec::new();
    }
    let ffi_cache = root.join(".skycache").join("ffi");
    let cached: Vec<String> = if ffi_cache.is_dir() {
        std::fs::read_dir(&ffi_cache)
            .map(|rd| {
                rd.filter_map(|e| e.ok().map(|e| e.file_name().to_string_lossy().into_owned()))
                    .collect()
            })
            .unwrap_or_default()
    } else {
        Vec::new()
    };
    let missing: Vec<String> = imports
        .into_iter()
        .filter(|imp| {
            let stem: String = imp.chars().take_while(|c| *c != '.').collect();
            !cached.iter().any(|f| f.contains(&stem))
        })
        .collect();
    missing
        .into_iter()
        .map(|pkg| Finding {
            check: "missing-ffi",
            severity: Severity::Warn,
            message: format!("import references {pkg} but no FFI bindings cached for it"),
            hint: "run `sky install` (regenerates `.skycache/ffi/`), or `sky doctor --fix`".into(),
            fix: Some(Fix::Install),
        })
        .collect()
}

fn is_ffi_path(p: &str) -> bool {
    p.contains(".com") || p.contains(".org") || p.contains(".io") || p.contains("google.golang")
}

/// When sky.toml declares `[live]`/`[auth]`, `SKY_AUTH_TOKEN_SECRET` must be
/// ≥ 32 bytes (the runtime hard-fails at boot otherwise).
fn check_auth_secret(root: &Path) -> Vec<Finding> {
    let Ok(c) = std::fs::read_to_string(root.join("sky.toml")) else {
        return Vec::new();
    };
    // Only an UNCOMMENTED `[live]`/`[auth]` section header counts — a bare
    // `contains("[live]")` also matches the COMMENTED `# [live]` template lines
    // that `sky init` scaffolds, so it warned on every pristine project.
    let declares_live_or_auth = c.lines().any(|line| {
        let t = line.trim();
        t == "[live]" || t == "[auth]"
    });
    if !declares_live_or_auth {
        return Vec::new();
    }
    match std::env::var("SKY_AUTH_TOKEN_SECRET") {
        Ok(s) if s.len() >= 32 => Vec::new(),
        Ok(s) => vec![Finding {
            check: "auth-secret-short",
            severity: Severity::Error,
            message: format!("SKY_AUTH_TOKEN_SECRET is {} bytes — must be ≥ 32", s.len()),
            hint: "export SKY_AUTH_TOKEN_SECRET=\"$(openssl rand -hex 32)\"".into(),
            fix: None,
        }],
        Err(_) => vec![Finding {
            check: "auth-secret-missing",
            severity: Severity::Warn,
            message: "SKY_AUTH_TOKEN_SECRET is unset (Sky.Live / Std.Auth in use)".into(),
            hint: "export SKY_AUTH_TOKEN_SECRET=\"$(openssl rand -hex 32)\"".into(),
            fix: None,
        }],
    }
}

/// Apply one `--fix` remediation, returning a status line.
fn apply_fix(root: &Path, check: &str, fix: &Fix) -> String {
    match fix {
        Fix::RemoveDir(dir) => match std::fs::remove_dir_all(dir) {
            Ok(()) => format!("✓ deleted {}", dir.display()),
            Err(e) => format!("✗ {check}: fix failed — {e}"),
        },
        Fix::Install => match assets_root_for(root) {
            Some(repo_root) => {
                let r = project::ffi_install(root, &repo_root);
                if r.ok {
                    format!("✓ {check}: ran `sky install`")
                } else {
                    format!("✗ {check}: `sky install` reported problems")
                }
            }
            None => format!("✗ {check}: could not resolve assets to run `sky install`"),
        },
        Fix::ProvisionPostgres => {
            let opts = db_provision::Opts {
                version: db_provision::pinned_version(root),
                no_pin: true,
                ..Default::default()
            };
            match db_provision::provision(&opts) {
                Ok(db_provision::Outcome::Installed { version, .. }) => {
                    format!("✓ {check}: provisioned PostgreSQL {version}")
                }
                Ok(db_provision::Outcome::AlreadyPresent { version, .. }) => {
                    format!("✓ {check}: PostgreSQL {version} was already provisioned")
                }
                Err(e) => format!("✗ {check}: {e}"),
            }
        }
    }
}

// ---- upgrade-claude ------------------------------------------------------

/// `sky upgrade-claude` — refresh the cwd's `./CLAUDE.md` from the template
/// (`templates/CLAUDE.md`, from the repo tree in dev or the embedded copy
/// standalone). Port of `Sky.Cli`'s `runUpgradeClaude` (`app/Main.hs:1848`):
/// always overwrites, backs any existing file up to `CLAUDE.md.bak`, and prints
/// the byte delta. Exit 0 on success, 1 if the template can't be located.
fn cmd_upgrade_claude(_args: &[String]) -> ExitCode {
    let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));

    // Refresh BOTH the agent-agnostic source of truth (AGENTS.md) and the thin
    // Claude Code entry point (CLAUDE.md → @AGENTS.md). CLAUDE.md alone would
    // leave the imported guide stale. Each is backed up to `<name>.bak`.
    let mut any = false;
    for name in ["AGENTS.md", "CLAUDE.md"] {
        let Some(bytes) = template_md_bytes(&cwd, name) else {
            eprintln!(
                "sky upgrade-claude: could not locate templates/{name}\n\
                 (run inside the Sky repo tree, or reinstall the `sky` binary)."
            );
            return ExitCode::FAILURE;
        };
        let target = cwd.join(name);
        let existed = target.is_file();
        let old_size = if existed {
            std::fs::metadata(&target).map(|m| m.len()).unwrap_or(0)
        } else {
            0
        };
        if existed {
            let bak = cwd.join(format!("{name}.bak"));
            if let Err(e) = std::fs::rename(&target, &bak) {
                eprintln!("sky upgrade-claude: could not back up existing {name}: {e}");
                return ExitCode::FAILURE;
            }
        }
        if let Err(e) = std::fs::write(&target, &bytes) {
            eprintln!("sky upgrade-claude: could not write {name}: {e}");
            return ExitCode::FAILURE;
        }
        let verb = if existed { "Refreshed" } else { "Created" };
        println!("{verb} {name} ({old_size} → {} bytes)", bytes.len());
        if existed {
            println!("  previous version saved as {name}.bak");
        }
        any = true;
    }
    if any {
        println!("(from {})", version_string());
    }
    ExitCode::SUCCESS
}

/// The template CLAUDE.md bytes: the repo `templates/CLAUDE.md` when running in
/// the repo tree, else the copy embedded in the binary (extracted to a temp
/// file and read back).
fn template_md_bytes(start: &Path, name: &str) -> Option<Vec<u8>> {
    if let Some(repo_root) = repo_root_for(start) {
        let tmpl = repo_root.join("templates").join(name);
        if tmpl.is_file() {
            if let Ok(b) = std::fs::read(&tmpl) {
                return Some(b);
            }
        }
    }
    // Embedded fallback (standalone binary): extract to a temp file, read, drop.
    let tmp = std::env::temp_dir().join(format!("sky-tmpl-{}-{}", std::process::id(), name));
    if project::extract_template(name, &tmp) {
        let b = std::fs::read(&tmp).ok();
        let _ = std::fs::remove_file(&tmp);
        return b;
    }
    None
}

// ---- verify --------------------------------------------------------------

/// The runtime shape of a verify target, deciding how it is run.
#[derive(Clone, Copy, PartialEq, Eq)]
enum Shape {
    /// HTTP server / Sky.Live — long-running; probed for a live listener.
    Server,
    /// Sky.Tui / Sky.Webview — long-running interactive; run as no-panic.
    LongRunning,
    /// One-shot CLI — must exit cleanly (0, no panic) within the timeout.
    Cli,
}

/// `sky verify [target]` — build AND run each example (or the given project /
/// path), catching the "builds but crashes / hangs at runtime" class that a
/// build-only check misses. Reuses `project::build_example` + a bounded run
/// (thin user-facing wrapper; the exhaustive corpus gate lives in `xtask
/// build-run`). Builds into `sky-out-rust/` so it never clobbers an example's
/// `sky-out/` oracle binary. Non-zero exit on any failure.
fn cmd_verify(args: &[String]) -> ExitCode {
    if wants_help(args) {
        println!(
            "sky verify [project]\n\n\
             In a project (a dir with sky.toml), run the full pre-release gate:\n  \
             1. fmt     — every .sky file is already `sky fmt`-clean\n  \
             2. check   — type-checks + `go build`s (the production build)\n  \
             3. test    — every tests/*.sky suite passes\n\n\
             In the compiler repo (an examples/ dir), build AND run every example.\n\
             Non-zero exit if any phase fails."
        );
        return ExitCode::SUCCESS;
    }
    let (positional, out_override) = parse_out(args);
    let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));

    // Single-project gate: an explicit project path, or the cwd is itself a
    // project and there's no examples/ sweep to run. (A named example, or the
    // compiler repo's examples/ dir, keeps the build+run sweep below.)
    if let Some(dir) = single_project_target(&cwd, positional.first().map(String::as_str)) {
        return verify_project_gate(&dir, out_override);
    }

    let out_dir_name = out_override.unwrap_or_else(|| "sky-out-rust".to_string());
    let targets = match resolve_verify_targets(&cwd, positional.first().map(String::as_str)) {
        Ok(t) => t,
        Err(msg) => {
            eprintln!("sky verify: {msg}");
            return ExitCode::from(2);
        }
    };
    if targets.is_empty() {
        eprintln!("sky verify: no targets found");
        return ExitCode::from(2);
    }

    let mut failures = 0usize;
    for dir in &targets {
        let name = dir
            .file_name()
            .and_then(|n| n.to_str())
            .unwrap_or("project")
            .to_string();
        let Some(repo_root) = assets_root_for(dir) else {
            println!("  FAIL assets: {name}");
            failures += 1;
            continue;
        };
        if is_compiler_repo_root(dir) {
            // Guard: never build from the compiler repo root itself.
            continue;
        }
        // Build.
        let opts = BuildOptions {
            repo_root,
            example_dir: dir.clone(),
            out_dir_name: out_dir_name.clone(),
            out_dir_abs: None,
            run: false,
            stdin: None,
            entry_module: None,
            progress: false,
            embed_bundle: None,
            wasm: false,
        };
        let report = build_example(&opts);
        if !report.emitted {
            println!("  FAIL build: {name} ({})", report.note.trim());
            failures += 1;
            continue;
        }
        if !report.go_build_ok {
            println!("  FAIL go-build: {name}");
            failures += 1;
            continue;
        }
        // Run (bounded).
        let out_dir = dir.join(&out_dir_name);
        match run_verify_target(&name, dir, &out_dir) {
            Ok(note) => println!(
                "  ok: {name}{}",
                if note.is_empty() {
                    String::new()
                } else {
                    format!(" ({note})")
                }
            ),
            Err(reason) => {
                println!("  FAIL run: {name} ({reason})");
                failures += 1;
            }
        }
    }

    println!();
    if failures == 0 {
        println!("verify: {} target(s) passed", targets.len());
        ExitCode::SUCCESS
    } else {
        println!("verify: {failures} of {} target(s) failed", targets.len());
        ExitCode::FAILURE
    }
}

/// The single project a `sky verify` should run the full gate on: an explicit
/// path holding a `sky.toml`, or `cwd` itself when it's a project AND there's no
/// `examples/` dir (which would mean the compiler repo → the build+run sweep).
/// A named `examples/<x>` target returns `None` so the sweep path handles it.
fn single_project_target(cwd: &Path, target: Option<&str>) -> Option<PathBuf> {
    match target {
        Some(t) => {
            let p = Path::new(t);
            if p.join("sky.toml").is_file() {
                Some(p.canonicalize().unwrap_or_else(|_| cwd.join(p)))
            } else {
                None
            }
        }
        None => {
            if cwd.join("sky.toml").is_file() && !cwd.join("examples").is_dir() {
                Some(cwd.to_path_buf())
            } else {
                None
            }
        }
    }
}

/// The full project pre-release gate (#11): fmt-clean, type-check + build, tests.
fn verify_project_gate(dir: &Path, out_override: Option<String>) -> ExitCode {
    let out_dir_name = out_override.unwrap_or_else(|| "sky-out".to_string());
    let name = dir
        .file_name()
        .and_then(|n| n.to_str())
        .unwrap_or("project");
    println!("Verifying {name} — fmt, type-check, build, tests\n");

    let Some(repo_root) = assets_root_for(dir) else {
        eprintln!("  ✗ could not locate the Sky stdlib + runtime");
        return ExitCode::FAILURE;
    };
    let mut failed: Vec<&str> = Vec::new();

    // 1. fmt — every project .sky file must already be `sky fmt`-clean.
    let files = project_sky_files(dir);
    let mut unformatted = Vec::new();
    for f in &files {
        if let Ok(src) = std::fs::read_to_string(f) {
            if !fmt::is_formatted(&src) {
                unformatted.push(f.clone());
            }
        }
    }
    if unformatted.is_empty() {
        println!("  ✓ fmt      ({} file(s) clean)", files.len());
    } else {
        println!(
            "  ✗ fmt      {} file(s) need `sky fmt`:",
            unformatted.len()
        );
        for f in unformatted.iter().take(10) {
            println!("             {}", rel_display(dir, f));
        }
        failed.push("fmt");
    }

    // 2. check + build — type-checks + `go build`s + emits the (production)
    //    binary. This single build covers both "type checking" and "production
    //    build": `sky check` ≡ `sky build` minus the artefact.
    let opts = BuildOptions {
        repo_root,
        example_dir: dir.to_path_buf(),
        out_dir_name: out_dir_name.clone(),
        out_dir_abs: None,
        run: false,
        stdin: None,
        entry_module: None,
        progress: false,
        embed_bundle: None,
        wasm: false,
    };
    let report = build_example(&opts);
    if report.emitted && report.go_build_ok {
        println!("  ✓ check    (type-checks + builds → {out_dir_name}/)");
    } else if !report.emitted {
        println!("  ✗ check    {}", report.note.trim());
        failed.push("check");
    } else {
        println!(
            "  ✗ build    go build failed:\n{}",
            report.go_build_stderr.trim()
        );
        failed.push("build");
    }

    // 3. tests — run every tests/*.sky suite (only when a build succeeded, so a
    //    type error isn't reported twice).
    let suites = test_suites(dir);
    if suites.is_empty() {
        println!("  – tests    (none under tests/)");
    } else if failed.contains(&"check") {
        println!("  – tests    (skipped — fix the type error first)");
    } else {
        let mut test_fail = 0;
        for suite in &suites {
            match testrunner::run_test(suite, &out_dir_name) {
                Ok(run) if run.exit_code == Some(0) => {}
                Ok(run) => {
                    println!(
                        "  ✗ test     {} ({})",
                        rel_display(dir, suite),
                        if run.note.is_empty() {
                            "failed".into()
                        } else {
                            run.note
                        }
                    );
                    test_fail += 1;
                }
                Err(e) => {
                    println!("  ✗ test     {}: {e}", rel_display(dir, suite));
                    test_fail += 1;
                }
            }
        }
        if test_fail == 0 {
            println!("  ✓ tests    ({} suite(s) passed)", suites.len());
        } else {
            failed.push("tests");
        }
    }

    println!();
    if failed.is_empty() {
        println!("✓ verify passed — ready to ship");
        ExitCode::SUCCESS
    } else {
        println!("✗ verify failed: {}", failed.join(", "));
        ExitCode::FAILURE
    }
}

/// Project `.sky` files under the configured source root + `tests/`, skipping
/// generated dirs. Used by `sky verify`'s fmt phase.
fn project_sky_files(dir: &Path) -> Vec<PathBuf> {
    let mut out = Vec::new();
    let root = project::configured_source_root(dir);
    walk_sky(&dir.join(root), &mut out);
    walk_sky(&dir.join("tests"), &mut out);
    out.sort();
    out
}

fn test_suites(dir: &Path) -> Vec<PathBuf> {
    let mut out = Vec::new();
    walk_sky(&dir.join("tests"), &mut out);
    out.sort();
    out
}

fn walk_sky(dir: &Path, out: &mut Vec<PathBuf>) {
    let Ok(entries) = std::fs::read_dir(dir) else {
        return;
    };
    for e in entries.flatten() {
        let p = e.path();
        let name = p.file_name().and_then(|n| n.to_str()).unwrap_or("");
        if p.is_dir() {
            if !matches!(name, "sky-out" | "sky-out-rust" | ".skycache" | ".skydeps" | ".git") {
                walk_sky(&p, out);
            }
        } else if name.ends_with(".sky") {
            out.push(p);
        }
    }
}

fn rel_display(base: &Path, p: &Path) -> String {
    p.strip_prefix(base)
        .unwrap_or(p)
        .display()
        .to_string()
}

/// Resolve the set of project dirs to verify from `cwd` + an optional target:
/// a named example under `cwd/examples`, an explicit path to a project, all
/// examples under `cwd/examples`, or `cwd` itself when it holds a `sky.toml`.
fn resolve_verify_targets(cwd: &Path, target: Option<&str>) -> Result<Vec<PathBuf>, String> {
    let examples = cwd.join("examples");
    if let Some(t) = target {
        // Explicit path to a project dir?
        let as_path = Path::new(t);
        if as_path.join("sky.toml").is_file() {
            // Absolutise so `.`/relative paths get a real file_name (target name)
            // and an absolute binary path for the spawn step (a relative `app`
            // under `current_dir(out_dir)` would double-nest and fail to spawn).
            let abs = as_path.canonicalize().unwrap_or_else(|_| cwd.join(as_path));
            return Ok(vec![abs]);
        }
        // Named example under examples/.
        let ex = examples.join(t);
        if ex.join("sky.toml").is_file() {
            return Ok(vec![ex]);
        }
        return Err(format!(
            "target `{t}` is not a project dir or a known example"
        ));
    }
    // No target: all examples if examples/ exists, else the cwd project.
    if examples.is_dir() {
        let mut dirs: Vec<PathBuf> = std::fs::read_dir(&examples)
            .map_err(|e| format!("reading examples/: {e}"))?
            .filter_map(|e| e.ok().map(|e| e.path()))
            .filter(|p| p.join("sky.toml").is_file())
            .collect();
        dirs.sort();
        return Ok(dirs);
    }
    if cwd.join("sky.toml").is_file() {
        return Ok(vec![cwd.to_path_buf()]);
    }
    Err("no examples/ directory and no sky.toml in the current directory".to_string())
}

/// Run a built target with a bounded watchdog, classifying failure. Server
/// shapes are probed for a live listener; CLI shapes must exit 0 without a
/// panic; long-running (TUI/Webview) shapes must not panic on start.
fn run_verify_target(name: &str, dir: &Path, out_dir: &Path) -> Result<String, String> {
    if is_gui_example(name) {
        // GUI (Fyne) needs a display + native toolkit at link/run time; the
        // build already succeeded — don't attempt a headless runtime probe.
        return Ok("gui build-only".into());
    }
    let shape = classify_shape(dir);
    let app = out_dir.join(project::configured_bin_name(dir));
    if !app.is_file() {
        return Err("binary not produced".into());
    }

    match shape {
        Shape::Server => run_server_probe(&app, out_dir),
        Shape::Cli | Shape::LongRunning => run_process_bounded(&app, out_dir, shape),
    }
}

/// Spawn a server target, discover its listening port from its startup line
/// (falling back to the env port for servers that don't announce one), probe a
/// TCP listener, then kill it. Watchdog-bounded on every path.
fn run_server_probe(app: &Path, cwd: &Path) -> Result<String, String> {
    let env_port = free_port().unwrap_or(8000);
    let mut child = match Command::new(app)
        .current_dir(cwd)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .env("SKY_LIVE_PORT", env_port.to_string())
        .env("PORT", env_port.to_string())
        .env("SKY_LIVE_STORE", "memory")
        .env("SKY_CONSOLE_EMBED", "off")
        .env("SKY_DEV_BANNER", "off")
        .env("SKY_LIVE_BANNER", "off")
        .env("ENV", "dev")
        .spawn()
    {
        Ok(c) => c,
        Err(e) => return Err(format!("spawn: {e}")),
    };

    // Read stdout on a thread: parse the announced port live, and accumulate the
    // full text (delivered on EOF) for panic detection. A dev server may spawn a
    // `/_sky/console` grandchild that keeps the pipe fds open, so we never read
    // to EOF synchronously — the thread + bounded recv keep this bounded.
    let (port_rx, out_rx) = spawn_server_stdout(child.stdout.take());
    let err_rx = spawn_drain(child.stderr.take());

    // Wait up to 8s for the announced port; on Disconnected (stdout closed) the
    // server exited before announcing → crash. On Timeout, fall back to env_port
    // (servers that never print a line but do bind the env port).
    let port = match port_rx.recv_timeout(Duration::from_secs(8)) {
        Ok(p) => p,
        Err(std::sync::mpsc::RecvTimeoutError::Timeout) => env_port,
        Err(std::sync::mpsc::RecvTimeoutError::Disconnected) => {
            let _ = child.kill();
            let _ = child.wait();
            let logs = collect_drains(&[out_rx, err_rx]);
            return Err(panic_reason(&logs).unwrap_or_else(|| "server exited on start".into()));
        }
    };

    let deadline = Instant::now() + Duration::from_secs(6);
    let mut connected = false;
    while Instant::now() < deadline {
        if let Ok(Some(_)) = child.try_wait() {
            break; // exited before we connected
        }
        if std::net::TcpStream::connect(("127.0.0.1", port)).is_ok() {
            connected = true;
            break;
        }
        std::thread::sleep(Duration::from_millis(150));
    }
    let _ = child.kill();
    let _ = child.wait();
    let logs = collect_drains(&[out_rx, err_rx]);
    if let Some(r) = panic_reason(&logs) {
        return Err(r);
    }
    if connected {
        Ok(format!("server up on :{port}"))
    } else {
        Err(format!("no listener on :{port} within 6s"))
    }
}

/// Read a server's stdout on a thread: send the first announced listening port
/// over the first channel, and the full accumulated text on EOF over the second
/// (for panic detection). Mirrors `xtask build-run`'s port-lift heuristic.
#[allow(clippy::type_complexity)]
fn spawn_server_stdout(
    pipe: Option<impl Read + Send + 'static>,
) -> (
    std::sync::mpsc::Receiver<u16>,
    std::sync::mpsc::Receiver<String>,
) {
    use std::io::BufRead;
    let (port_tx, port_rx) = std::sync::mpsc::channel();
    let (text_tx, text_rx) = std::sync::mpsc::channel();
    if let Some(p) = pipe {
        std::thread::spawn(move || {
            let mut buf = String::new();
            let mut announced = false;
            let reader = std::io::BufReader::new(p);
            for line in reader.lines().map_while(Result::ok) {
                let low = line.to_lowercase();
                if !announced && (low.contains("listening") || low.contains("starting on port")) {
                    if let Some(port) = last_colon_number(&line).or_else(|| last_number(&line)) {
                        let _ = port_tx.send(port);
                        announced = true;
                    }
                }
                buf.push_str(&line);
                buf.push('\n');
            }
            let _ = text_tx.send(buf);
        });
    }
    (port_rx, text_rx)
}

/// Last `:PORT` in a line (`listening on 127.0.0.1:8000` → 8000).
fn last_colon_number(s: &str) -> Option<u16> {
    s.rsplit(':').find_map(|seg| {
        let digits: String = seg.chars().take_while(|c| c.is_ascii_digit()).collect();
        digits.parse().ok()
    })
}

/// Last bare number in a line (`Server starting on port 8080` → 8080).
fn last_number(s: &str) -> Option<u16> {
    let mut last = None;
    let mut cur = String::new();
    for c in s.chars() {
        if c.is_ascii_digit() {
            cur.push(c);
        } else if !cur.is_empty() {
            last = cur.parse().ok();
            cur.clear();
        }
    }
    if !cur.is_empty() {
        last = cur.parse().ok();
    }
    last
}

/// Run a one-shot / long-running target with a timeout. CLI must exit 0 without
/// a panic; long-running must survive the grace window without panicking.
fn run_process_bounded(app: &Path, cwd: &Path, shape: Shape) -> Result<String, String> {
    let mut child = match Command::new(app)
        .current_dir(cwd)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
    {
        Ok(c) => c,
        Err(e) => return Err(format!("spawn: {e}")),
    };
    let out_rx = spawn_drain(child.stdout.take());
    let err_rx = spawn_drain(child.stderr.take());
    let timeout = if shape == Shape::Cli {
        Duration::from_secs(60)
    } else {
        Duration::from_secs(3)
    };
    let deadline = Instant::now() + timeout;
    loop {
        match child.try_wait() {
            Ok(Some(status)) => {
                let _ = child.wait();
                let logs = collect_drains(&[out_rx, err_rx]);
                if let Some(r) = panic_reason(&logs) {
                    return Err(r);
                }
                return match status.code() {
                    Some(0) => Ok(String::new()),
                    Some(n) => Err(format!("exit {n}")),
                    None => Err("terminated by signal".into()),
                };
            }
            Ok(None) => {
                if Instant::now() >= deadline {
                    // CLI that never exits = hang (fail); long-running that
                    // stays up without panic = pass.
                    let _ = child.kill();
                    let _ = child.wait();
                    let logs = collect_drains(&[out_rx, err_rx]);
                    if let Some(r) = panic_reason(&logs) {
                        return Err(r);
                    }
                    return if shape == Shape::Cli {
                        Err("did not exit within 60s".into())
                    } else {
                        Ok("no-panic".into())
                    };
                }
                std::thread::sleep(Duration::from_millis(100));
            }
            Err(e) => return Err(format!("wait: {e}")),
        }
    }
}

/// Spawn a background thread that drains a child pipe to a String and sends it
/// on EOF. Keeps the main watchdog non-blocking even when a grandchild holds the
/// pipe fd open (the thread may then never finish — bounded by `collect_drains`).
fn spawn_drain(pipe: Option<impl Read + Send + 'static>) -> std::sync::mpsc::Receiver<String> {
    let (tx, rx) = std::sync::mpsc::channel();
    if let Some(mut p) = pipe {
        std::thread::spawn(move || {
            let mut s = String::new();
            let _ = p.read_to_string(&mut s);
            let _ = tx.send(s);
        });
    }
    rx
}

/// Collect whatever the drain threads have produced within a short bound, then
/// give up (a grandchild-held pipe may keep a thread alive indefinitely).
fn collect_drains(rxs: &[std::sync::mpsc::Receiver<String>]) -> String {
    let mut out = String::new();
    for rx in rxs {
        if let Ok(s) = rx.recv_timeout(Duration::from_millis(500)) {
            out.push_str(&s);
            out.push('\n');
        }
    }
    out
}

/// Extract a short reason from a Sky runtime panic line, if present.
fn panic_reason(s: &str) -> Option<String> {
    let line = s
        .lines()
        .find(|l| l.contains("panic:") || l.contains("panicKind="))?;
    if let Some(pos) = line.find("panicKind=") {
        let kind: String = line[pos + "panicKind=".len()..]
            .chars()
            .take_while(|c| !c.is_whitespace())
            .collect();
        return Some(format!("panic: {kind}"));
    }
    let after = line.split("panic:").nth(1).unwrap_or(line).trim();
    Some(format!(
        "panic: {}",
        after.chars().take(60).collect::<String>()
    ))
}

/// A free TCP port on loopback (bind :0, read the assigned port, drop).
fn free_port() -> Option<u16> {
    std::net::TcpListener::bind("127.0.0.1:0")
        .ok()
        .and_then(|l| l.local_addr().ok())
        .map(|a| a.port())
}

/// Classify a target's runtime shape by scanning its entry module's `main`
/// binding, falling back to a whole-`src/` scan. Mirrors the tokens
/// `xtask build-run`'s classifier keys on.
fn classify_shape(dir: &Path) -> Shape {
    let src = dir.join("src");
    let blob = read_src_blob(&src);
    if blob.contains("Server.listen")
        || blob.contains("HttpServer.listen")
        || blob.contains("listenAndServe")
        || blob.contains("Live.app")
        || (blob.contains("notFound") && blob.contains("routes"))
    {
        Shape::Server
    } else if blob.contains("Tui.app")
        || blob.contains("Tui.program")
        || blob.contains("Webview.app")
        || blob.contains("Webview.program")
    {
        Shape::LongRunning
    } else {
        Shape::Cli
    }
}

/// GUI (Fyne) examples: build-only at runtime (need a native display toolkit).
fn is_gui_example(name: &str) -> bool {
    name.contains("fyne") || name.contains("-gui")
}

fn read_src_blob(src: &Path) -> String {
    let mut files = Vec::new();
    collect_sky_files(src, &mut files);
    let mut blob = String::new();
    for f in &files {
        if let Ok(s) = std::fs::read_to_string(f) {
            blob.push_str(&s);
            blob.push('\n');
        }
    }
    blob
}

// ---- doctor/verify fs helpers --------------------------------------------

fn toml_entry(root: &Path) -> Option<String> {
    let c = std::fs::read_to_string(root.join("sky.toml")).ok()?;
    for line in c.lines() {
        let t = line.trim();
        if let Some(rest) = t.strip_prefix("entry") {
            let rest = rest.trim_start();
            if let Some(v) = rest.strip_prefix('=') {
                let v = v.trim();
                return Some(v.trim_matches(|c| c == '"' || c == '\'').to_string());
            }
        }
    }
    None
}

fn collect_sky_files(dir: &Path, out: &mut Vec<PathBuf>) {
    let Ok(rd) = std::fs::read_dir(dir) else {
        return;
    };
    for e in rd.flatten() {
        let p = e.path();
        if p.is_dir() {
            collect_sky_files(&p, out);
        } else if p.extension().and_then(|x| x.to_str()) == Some("sky") {
            out.push(p);
        }
    }
}

fn file_mtime(p: &Path) -> Option<std::time::SystemTime> {
    std::fs::metadata(p).and_then(|m| m.modified()).ok()
}

fn newest_mtime(dir: &Path) -> Option<std::time::SystemTime> {
    let mut newest: Option<std::time::SystemTime> = None;
    fn walk(dir: &Path, newest: &mut Option<std::time::SystemTime>) {
        let Ok(rd) = std::fs::read_dir(dir) else {
            return;
        };
        for e in rd.flatten() {
            let p = e.path();
            if p.is_dir() {
                walk(&p, newest);
            } else if let Some(t) = file_mtime(&p) {
                if newest.is_none() || Some(t) > *newest {
                    *newest = Some(t);
                }
            }
        }
    }
    walk(dir, &mut newest);
    newest
}

fn newest_sky_mtime(dir: &Path) -> Option<std::time::SystemTime> {
    let mut files = Vec::new();
    collect_sky_files(dir, &mut files);
    files.iter().filter_map(|f| file_mtime(f)).max()
}

// ---- shared helpers ------------------------------------------------------

/// Resolve a `<file>` to its (repo_root, project_dir). Prints a diagnostic and
/// returns `None` when the file is missing or the compiler assets can't be
/// located.
fn resolve(file: &Path) -> Option<(PathBuf, PathBuf)> {
    if !file.exists() {
        eprintln!("sky: no such file: {}", file.display());
        return None;
    }
    // Dev: assets live in the repo tree above `file`. Standalone: fall back to
    // the trees embedded in the binary, extracted to a cache dir (doc 09 §E).
    let repo_root = assets_root_for(file)?;
    let project_dir = project_dir_for(file);
    Some((repo_root, project_dir))
}

/// Split `args` into positionals and an optional `--out <dir>` override.
/// Runtime-profiling options for `sky run --profile` (see `runtime-go/rt/profile.go`).
struct ProfileOpts {
    dir: Option<String>,
    timeout: Option<String>,
}

/// Strip `--profile[-dir <d>|-timeout <t>]` from the arg list (consuming their
/// values so `parse_out`/`resolve_entry_arg` don't mistake them for the entry
/// file) and return the remaining args + the parsed options.
fn parse_profile(args: &[String]) -> (Vec<String>, Option<ProfileOpts>) {
    let mut rest = Vec::new();
    let mut enabled = false;
    let mut dir = None;
    let mut timeout = None;
    let mut it = args.iter();
    while let Some(a) = it.next() {
        match a.as_str() {
            "--profile" => enabled = true,
            "--profile-dir" => {
                enabled = true;
                dir = it.next().cloned();
            }
            s if s.starts_with("--profile-dir=") => {
                enabled = true;
                dir = Some(s["--profile-dir=".len()..].to_string());
            }
            "--profile-timeout" => {
                enabled = true;
                timeout = it.next().cloned();
            }
            s if s.starts_with("--profile-timeout=") => {
                enabled = true;
                timeout = Some(s["--profile-timeout=".len()..].to_string());
            }
            other => rest.push(other.to_string()),
        }
    }
    (
        rest,
        enabled.then_some(ProfileOpts { dir, timeout }),
    )
}

fn parse_out(args: &[String]) -> (Vec<String>, Option<String>) {
    let mut positional = Vec::new();
    let mut out = None;
    let mut it = args.iter();
    while let Some(a) = it.next() {
        match a.as_str() {
            "--out" | "-o" => out = it.next().cloned(),
            s if s.starts_with("--out=") => out = Some(s["--out=".len()..].to_string()),
            // `--target <value>` (sky build): consume the value so it is not
            // mistaken for the entry file. cmd_build reads --target from `args`.
            "--target" => {
                it.next();
            }
            s if s.starts_with('-') => { /* ignore unknown flags for forward-compat */ }
            s => positional.push(s.to_string()),
        }
    }
    (positional, out)
}

/// Version string: `sky v<version>` for a release, else `sky dev`.
///
/// Release builds bake the tag in at compile time (the release workflow sets
/// `SKY_BUILD_VERSION`); this is the only source a standalone published binary
/// has, since it carries no repo tree. Dev builds fall back to the legacy
/// `app/VERSION` file if present (content is `dev`), otherwise report `sky dev`.
fn version_string() -> String {
    if let Some(v) = option_env!("SKY_BUILD_VERSION") {
        let v = v.trim().trim_start_matches('v');
        if !v.is_empty() && v != "dev" {
            return format!("sky v{v}");
        }
    }
    let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
    let ver = repo_root_for(&cwd)
        .and_then(|root| {
            std::fs::read_to_string(
                root.join("legacy-haskell-compiler")
                    .join("app")
                    .join("VERSION"),
            )
            .ok()
        })
        .map(|s| s.trim().to_string());
    match ver.as_deref() {
        Some("dev") | Some("") | None => "sky dev".to_string(),
        Some(v) => format!("sky v{v}"),
    }
}

fn print_help() {
    println!(
        "sky — the Sky compiler CLI (rust bring-up)\n\n\
         USAGE:\n  sky <command> [args]\n\n\
         WIRED COMMANDS:\n\
         \x20 build <file>     compile → sky-out/ + go build (--embed bundles PostgreSQL)\n\
         \x20 check <file>     type-check + go build (no binary run)\n\
         \x20 run   <file>     build + execute\n\
         \x20 fmt   <file...>  format in place (--check / --stdin)\n\
         \x20 test  <file>     run a Sky.Test suite\n\
         \x20 lsp              launch the sky-lsp server (stdio)\n\
         \x20 clean            remove sky-out/ + .skycache/\n\
         \x20 init  [name]     scaffold a new project\n\
         \x20 doc   <Module>   print a module's exported bindings\n\
         \x20 doc   --serve|--tui  browse the docs (HTTP server / terminal)\n\
         \x20 console [--port N] [--tui]   run the Sky Console mini-app\n\
         \x20 console-serve [...]          run the Sky Console hub daemon\n\
         \x20 watch <file>     rebuild + restart on source change\n\
         \x20 config migrate [--dry-run|--check]  rewrite legacy sky.toml → typed config\n\
         \x20 db    <status|migrate> [file]  Std.Db migrations\n\
         \x20 db    <start|stop|ps>          local PostgreSQL cluster (--all for ps/stop)\n\
         \x20 db    provision --embed        fetch PostgreSQL into ~/.sky/postgres\n\
         \x20 add    <import-path>  inspect a Go pkg → commit its FFI surface\n\
         \x20 remove <import-path>  drop a Go pkg's FFI surface + dep\n\
         \x20 install               regen/verify committed FFI surfaces\n\
         \x20 update                bump Go deps + regen surfaces\n\
         \x20 doctor [--fix] [-v]  diagnose project / environment health\n\
         \x20 upgrade-claude       refresh ./CLAUDE.md from the embedded template\n\
         \x20 verify [target]      build + run each example / the project\n\
         \x20 version          print the version\n\n\
         DEFERRED (bring-up): upgrade"
    );
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parse_semver_handles_v_prefix_and_suffixes() {
        assert_eq!(parse_semver("v0.19.7"), Some((0, 19, 7)));
        assert_eq!(parse_semver("0.19.7"), Some((0, 19, 7)));
        assert_eq!(parse_semver("v1.0"), Some((1, 0, 0)));
        assert_eq!(parse_semver("v0.20.0-rc1"), Some((0, 20, 0)));
        assert_eq!(parse_semver("dev"), None);
        assert_eq!(parse_semver("sky dev"), None);
        // ordering the notes range relies on: a newer tag compares greater
        assert!(parse_semver("v0.19.10") > parse_semver("v0.19.9"));
        assert!(parse_semver("v0.20.0") > parse_semver("v0.19.99"));
    }

    #[test]
    fn should_nudge_only_when_newer_and_rate_limit_elapsed() {
        let day = NUDGE_INTERVAL_SECS;
        // newer + never nudged → yes
        assert!(should_nudge((0, 18, 10), (0, 19, 0), 0, day + 1));
        // newer but nudged recently → no
        assert!(!should_nudge((0, 18, 10), (0, 19, 0), day, day + 100));
        // newer + last nudge a full interval ago → yes
        assert!(should_nudge((0, 18, 10), (0, 19, 0), 0, day));
        // same version → no
        assert!(!should_nudge((0, 19, 0), (0, 19, 0), 0, 10 * day));
        // current is newer than "latest" (dev ahead of release) → no
        assert!(!should_nudge((0, 20, 0), (0, 19, 0), 0, 10 * day));
    }

    #[test]
    fn nudge_line_shows_versions_when_newer() {
        let day = NUDGE_INTERVAL_SECS;
        let cache = UpdateCache { last_check: 0, last_nudge: 0, latest: Some("0.19.0".into()) };
        let msg = nudge_line((0, 18, 10), "v0.18.10", &cache, day).expect("should nudge");
        assert!(msg.contains("v0.18.10") && msg.contains("0.19.0"));
        assert!(msg.contains("sky upgrade"));
        // already current → no line
        assert!(nudge_line((0, 19, 0), "v0.19.0", &cache, day).is_none());
        // newer but nudged recently → no line
        let recent = UpdateCache { last_nudge: day, ..cache.clone() };
        assert!(nudge_line((0, 18, 10), "v0.18.10", &recent, day + 1).is_none());
        // no cached latest → no line
        let empty = UpdateCache::default();
        assert!(nudge_line((0, 18, 10), "v0.18.10", &empty, day).is_none());
    }

    #[test]
    fn cache_is_stale_after_interval() {
        assert!(cache_is_stale(0, CHECK_INTERVAL_SECS));
        assert!(cache_is_stale(0, CHECK_INTERVAL_SECS + 1));
        assert!(!cache_is_stale(100, 100));
        assert!(!cache_is_stale(100, 100 + CHECK_INTERVAL_SECS - 1));
        // clock skew (now < last_check) must not underflow → not stale
        assert!(!cache_is_stale(1_000_000, 0));
    }

    #[test]
    fn body_has_breaking_detects_migration_headings_only() {
        assert!(body_has_breaking("# Notes\n## ⚠ Breaking changes\n- x"));
        assert!(body_has_breaking("### Migration\nrun sky db migrate"));
        assert!(body_has_breaking("## MIGRATING from v0.18"));
        // a body that only mentions the words in prose (not a heading) does not trip
        assert!(!body_has_breaking("This release has no breaking changes."));
        assert!(!body_has_breaking("**Full Changelog**: https://…"));
        assert!(!body_has_breaking(""));
    }

    #[test]
    fn wants_help_detects_help_flags_only() {
        // #6: `--help`/`-h` are recognised so `sky init --help` shows help instead
        // of scaffolding `sky-project`. A plain name (or no args) does not.
        assert!(wants_help(&["--help".to_string()]));
        assert!(wants_help(&["-h".to_string()]));
        assert!(wants_help(&["myproj".to_string(), "--help".to_string()]));
        assert!(!wants_help(&["myproj".to_string()]));
        assert!(!wants_help(&[]));
    }

    #[test]
    fn verify_walk_sky_skips_generated_dirs() {
        // #11: the fmt phase must not scan generated output (sky-out/.skycache).
        let dir = std::env::temp_dir().join(format!("sky-verify-walk-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(dir.join("src")).unwrap();
        std::fs::create_dir_all(dir.join("sky-out")).unwrap();
        std::fs::create_dir_all(dir.join(".skycache")).unwrap();
        std::fs::write(dir.join("src/Main.sky"), "module Main exposing (main)\n").unwrap();
        std::fs::write(dir.join("sky-out/gen.sky"), "x\n").unwrap();
        std::fs::write(dir.join(".skycache/c.sky"), "x\n").unwrap();
        let mut out = Vec::new();
        walk_sky(&dir, &mut out);
        assert_eq!(out.len(), 1, "only src/Main.sky, not generated dirs: {out:?}");
        assert!(out[0].ends_with("Main.sky"));
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn verify_single_project_routing() {
        // #11: cwd-with-sky.toml (no examples/) → single-project gate; a cwd with
        // examples/ → None (the build+run sweep handles it).
        let dir = std::env::temp_dir().join(format!("sky-verify-route-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        std::fs::write(dir.join("sky.toml"), "name=\"x\"\n").unwrap();
        assert!(single_project_target(&dir, None).is_some());
        std::fs::create_dir_all(dir.join("examples")).unwrap();
        assert!(
            single_project_target(&dir, None).is_none(),
            "a repo with examples/ runs the sweep, not the single-project gate"
        );
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn profile_flags_stripped_from_args() {
        // `sky run app.sky --profile --profile-timeout 30s` → the entry file is
        // still the only positional; the profile flags + their values are consumed.
        let (rest, opts) = parse_profile(&[
            "app.sky".to_string(),
            "--profile".to_string(),
            "--profile-timeout".to_string(),
            "30s".to_string(),
        ]);
        assert_eq!(rest, vec!["app.sky".to_string()]);
        let opts = opts.expect("profiling enabled");
        assert_eq!(opts.timeout.as_deref(), Some("30s"));
    }

    #[test]
    fn upgrade_json_tag_extraction() {
        // The shape the GitHub releases API returns.
        let body = r#"{"url":"...","tag_name": "v0.18.0","name":"Sky v0.18.0"}"#;
        assert_eq!(
            json_string_field(body, "tag_name").as_deref(),
            Some("v0.18.0")
        );
        // Missing key → None (caller surfaces an actionable error).
        assert_eq!(json_string_field("{}", "tag_name"), None);
    }

    #[test]
    fn upgrade_platform_artifact_is_host_specific() {
        // Whatever the host, the artifact (when Some) matches a real release
        // asset base-name from .github/workflows/release.yml.
        if let Some(a) = platform_artifact() {
            assert!(
                [
                    "sky-darwin-arm64",
                    "sky-linux-x64",
                    "sky-linux-arm64",
                    "sky-windows-x64"
                ]
                .contains(&a),
                "unexpected artifact name: {a}"
            );
        }
    }

    #[test]
    fn toml_entry_parsed_and_scoped_above_sections() {
        // Standard shape.
        assert_eq!(
            parse_toml_entry("name = \"x\"\nentry = \"src/Main.sky\"\n\n[live]\nport = 8000\n"),
            Some("src/Main.sky".to_string())
        );
        // Custom path + single quotes + extra spacing.
        assert_eq!(
            parse_toml_entry("entry   =   'app/Start.sky'\n"),
            Some("app/Start.sky".to_string())
        );
        // No top-level entry key → None (caller applies the src/Main.sky default).
        assert_eq!(
            parse_toml_entry("name = \"x\"\n[source]\nroot = \"src\"\n"),
            None
        );
        // An `entry` inside a section must NOT be picked up (scan stops at `[`).
        assert_eq!(
            parse_toml_entry("name = \"x\"\n[weird]\nentry = \"nope.sky\"\n"),
            None
        );
    }

    #[test]
    fn go_version_parses_major_minor() {
        assert_eq!(
            parse_go_version("go version go1.22.3 darwin/arm64"),
            Some((1, 22))
        );
        assert_eq!(
            parse_go_version("go version go1.21.0 linux/amd64"),
            Some((1, 21))
        );
        assert_eq!(parse_go_version("go version go2.0.1 x"), Some((2, 0)));
        assert_eq!(parse_go_version("garbage"), None);
    }

    #[test]
    fn ffi_path_detects_domain_imports() {
        assert!(is_ffi_path("github.com/stripe/stripe-go"));
        assert!(is_ffi_path("golang.org/x/term"));
        assert!(!is_ffi_path("Std.Db"));
        assert!(!is_ffi_path("Sky.Core.List"));
    }

    #[test]
    fn panic_reason_extracts_kind() {
        assert_eq!(
            panic_reason("boot ok\nSky panic: panicKind=DivisionByZero errId=abcd"),
            Some("panic: DivisionByZero".to_string())
        );
        assert!(panic_reason("all fine\nlistening on :8000").is_none());
    }

    #[test]
    fn toml_entry_reads_entry_key() {
        let dir = std::env::temp_dir().join(format!("sky-doctor-test-{}", std::process::id()));
        let _ = std::fs::create_dir_all(&dir);
        std::fs::write(
            dir.join("sky.toml"),
            "name = \"x\"\nentry = \"src/App.sky\"\n",
        )
        .unwrap();
        assert_eq!(toml_entry(&dir).as_deref(), Some("src/App.sky"));
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn parse_port_reads_all_forms_else_default() {
        let sp = |s: &str| s.split(' ').map(String::from).collect::<Vec<_>>();
        assert_eq!(parse_port(&sp("--port 9000"), 8025), 9000);
        assert_eq!(parse_port(&sp("-p 9001"), 8025), 9001);
        assert_eq!(parse_port(&sp("--port=9002"), 8025), 9002);
        assert_eq!(parse_port(&sp("--tui"), 8025), 8025);
        // A non-numeric value falls back to the default rather than aborting.
        assert_eq!(parse_port(&sp("--port abc"), 4000), 4000);
    }

    #[test]
    fn flag_value_reads_space_and_eq_forms() {
        let sp = |s: &str| s.split(' ').map(String::from).collect::<Vec<_>>();
        assert_eq!(
            flag_value(&sp("--data-dir /tmp/x"), "--data-dir").as_deref(),
            Some("/tmp/x")
        );
        assert_eq!(
            flag_value(&sp("--auth=off"), "--auth").as_deref(),
            Some("off")
        );
        assert_eq!(flag_value(&sp("--port 1"), "--auth"), None);
    }

    #[test]
    fn severity_orders_info_before_error() {
        let mut v = vec![Severity::Error, Severity::Info, Severity::Warn];
        v.sort();
        assert_eq!(v, vec![Severity::Info, Severity::Warn, Severity::Error]);
    }

    // ---- `sky watch` option parsing ------------------------------------
    //
    // The watch verb path itself is not exercised by any test (it spawns a
    // long-lived file watcher + child process). Its argument parser and the
    // watched-path allowlist ARE pure and are the parts that decide behaviour,
    // so pin them here.

    fn sw(s: &str) -> Vec<String> {
        s.split_whitespace().map(String::from).collect()
    }

    #[test]
    fn watch_opts_defaults() {
        let o = WatchOpts::parse(&[]).expect("empty parse");
        assert_eq!(o.file, None);
        assert!(!o.no_run);
        assert!(!o.clear);
        assert_eq!(o.debounce_ms, 150);
        assert_eq!(o.interval_ms, None);
        assert_eq!(o.kill_timeout_ms, 5000);
        assert!(o.extra_watch.is_empty());
    }

    #[test]
    fn watch_opts_positional_file_and_bare_flags() {
        let o = WatchOpts::parse(&sw("src/Main.sky --no-run --clear")).unwrap();
        assert_eq!(o.file.as_deref(), Some("src/Main.sky"));
        assert!(o.no_run);
        assert!(o.clear);
        // First non-flag positional wins; a second one is ignored (not an error).
        let o2 = WatchOpts::parse(&sw("a.sky b.sky")).unwrap();
        assert_eq!(o2.file.as_deref(), Some("a.sky"));
    }

    #[test]
    fn watch_opts_valued_flags_eq_form() {
        let o = WatchOpts::parse(&sw(
            "src/Main.sky --debounce=300 --interval=1000 --kill-timeout=2500 --watch=extra",
        ))
        .unwrap();
        assert_eq!(o.debounce_ms, 300);
        assert_eq!(o.interval_ms, Some(1000));
        assert_eq!(o.kill_timeout_ms, 2500);
        assert_eq!(o.extra_watch, vec![PathBuf::from("extra")]);
    }

    #[test]
    fn watch_opts_multiple_watch_dirs_accumulate() {
        let o = WatchOpts::parse(&sw("--watch=one --watch=two --watch=three")).unwrap();
        assert_eq!(
            o.extra_watch,
            vec![
                PathBuf::from("one"),
                PathBuf::from("two"),
                PathBuf::from("three"),
            ]
        );
    }

    #[test]
    fn watch_opts_invalid_numeric_values_error() {
        assert!(WatchOpts::parse(&sw("--debounce=abc")).is_err());
        assert!(WatchOpts::parse(&sw("--interval=x")).is_err());
        assert!(WatchOpts::parse(&sw("--kill-timeout=-5")).is_err()); // u64 rejects negatives
    }

    #[test]
    fn watch_opts_unknown_flag_errors() {
        // WatchOpts has no Debug impl, so match on the Result rather than
        // .unwrap_err() (which would require T: Debug).
        match WatchOpts::parse(&sw("--bogus")) {
            Err(err) => assert!(err.contains("unknown flag"), "got: {err}"),
            Ok(_) => panic!("expected an error for --bogus"),
        }
        // A bare positional after a valid file is fine; an unknown FLAG is not.
        assert!(WatchOpts::parse(&sw("src/Main.sky --nope")).is_err());
    }

    #[test]
    fn is_watched_change_accepts_sky_and_toml() {
        assert!(is_watched_change(Path::new("src/Main.sky")));
        assert!(is_watched_change(Path::new("src/nested/View.sky")));
        assert!(is_watched_change(Path::new("sky.toml")));
        assert!(is_watched_change(Path::new("/abs/project/sky.toml")));
        // Non-source files never trigger a rebuild.
        assert!(!is_watched_change(Path::new("README.md")));
        assert!(!is_watched_change(Path::new("Cargo.toml")));
        assert!(!is_watched_change(Path::new("src/data.json")));
    }

    #[test]
    fn is_watched_change_excludes_generated_dirs() {
        // Generated / vendor dirs are excluded even when they contain .sky files.
        for p in [
            "sky-out/main.sky",
            "sky-out-rust/x.sky",
            ".skycache/lowered/Main.sky",
            ".skydeps/foo.sky",
            "dist-newstyle/build/x.sky",
            ".git/hooks/x.sky",
            "node_modules/pkg/a.sky",
            ".vscode/x.sky",
            ".idea/x.sky",
            "project/sky-out/nested/App.sky",
        ] {
            assert!(!is_watched_change(Path::new(p)), "should exclude {p}");
        }
    }

    // ---- `sky run --profile` flag parsing ------------------------------

    #[test]
    fn parse_profile_absent_is_none() {
        let (rest, prof) = parse_profile(&sw("src/Main.sky --db-seed"));
        assert!(prof.is_none());
        assert_eq!(rest, sw("src/Main.sky --db-seed"));
    }

    #[test]
    fn parse_profile_bare_flag_enables() {
        let (rest, prof) = parse_profile(&sw("src/Main.sky --profile"));
        let p = prof.expect("profile enabled");
        assert_eq!(p.dir, None);
        assert_eq!(p.timeout, None);
        // The --profile flag is consumed; the entry file passes through.
        assert_eq!(rest, sw("src/Main.sky"));
    }

    #[test]
    fn parse_profile_dir_and_timeout_space_and_eq_forms() {
        let (rest, prof) =
            parse_profile(&sw("app.sky --profile-dir /tmp/p --profile-timeout 30s"));
        let p = prof.unwrap();
        assert_eq!(p.dir.as_deref(), Some("/tmp/p"));
        assert_eq!(p.timeout.as_deref(), Some("30s"));
        assert_eq!(rest, sw("app.sky"));

        let (_r2, p2) = parse_profile(&sw("--profile-dir=/tmp/q --profile-timeout=5s"));
        let p2 = p2.unwrap();
        assert_eq!(p2.dir.as_deref(), Some("/tmp/q"));
        assert_eq!(p2.timeout.as_deref(), Some("5s"));
    }

    #[test]
    fn parse_profile_dir_alone_implies_enabled() {
        // Passing only --profile-dir (without a bare --profile) still turns
        // profiling on.
        let (_rest, prof) = parse_profile(&sw("app.sky --profile-dir out"));
        assert!(prof.is_some());
    }
}

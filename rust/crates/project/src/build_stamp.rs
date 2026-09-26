//! The build identity an app reports at `/_sky/buildinfo` and in the Sky
//! Console header: the Sky version that compiled it, the commit it was built
//! from, when that source was made, and where the commit came from.
//!
//! **Where it lives.** The stamp is written into the GENERATED GO SOURCE: a
//! `skybuildinfo/skybuildinfo.go` package whose `init` calls
//! `rt.SetBuildStamp(...)` (runtime-go/rt/buildinfo_stamp.go), linked by a
//! blank import in `sky_buildinfo.go` beside `main.go`. Any `go build` of the
//! emitted tree carries it: the one `sky build` runs, a manual cross-compile,
//! a Dockerfile, a custom CI.
//!
//! v0.25.20 passed the same values only as `-X` linker flags on the `go build`
//! that `sky build` ran itself. A real app's deploy extracted `git archive HEAD`
//! (no `.git`), ran `sky build --target web:app`, then cross-compiled the
//! backend's `sky-out/` with its own `CGO_ENABLED=0 GOOS=linux go build`. The
//! flags never reached that binary and production reported
//! `{"commit":"dev","builtAt":"unknown","skyVersion":"dev"}`.
//!
//! **How it is resolved** — once, at the user's project root (the directory
//! with the user's `sky.toml`), never from a generated
//! `.skyapp/.../.split/{backend,frontend}` directory (those are rewritten on
//! every build). `sky build` / `sky run` / `sky spa-split` pin the resolved
//! stamp in [`PINNED_ENV`] so every child build they spawn (a Std.App target's
//! derived entry, both legs of a Sky.Spa split, a desktop shell) embeds the
//! same identity. See [`resolve_with`] for the order.
//!
//! **Never the wall clock.** A value that changes on every build changes the
//! generated file, and Go then recompiles and re-links the binary on a
//! no-change rebuild (`web_app_no_change_rebuild_reuses_outputs` guards this).

use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};

/// The generated Go file in the `main` package: a blank import of [`STAMP_PKG`].
pub const STAMP_GO_FILE: &str = "sky_buildinfo.go";

/// The generated package (a directory under the emitted tree, module `sky-app`)
/// whose `init` records the stamp.
pub const STAMP_PKG: &str = "skybuildinfo";

/// Internal: the stamp a parent `sky` resolved at the user's project root,
/// inherited by the child builds it spawns. Not a user setting — the user
/// overrides are [`COMMIT_OVERRIDE_ENV`] and [`EPOCH_OVERRIDE_ENV`].
pub const PINNED_ENV: &str = "SKY_BUILD_STAMP_PINNED";

/// Optional commit override (any build, e.g. a Docker context with no `.git`
/// and no CI variables).
pub const COMMIT_OVERRIDE_ENV: &str = "SKY_BUILD_COMMIT";

/// Optional built-at override, Unix seconds. Not `SOURCE_DATE_EPOCH`: a nix
/// shell exports it as 1980-01-01 for every build, and the console would show
/// that date.
pub const EPOCH_OVERRIDE_ENV: &str = "SKY_BUILD_EPOCH";

/// The commit variables CI systems set by default, in lookup order. The first
/// non-empty value that looks like a hex commit id wins; a non-hex value (a
/// branch name, a placeholder) is skipped.
pub const CI_COMMIT_VARS: &[&str] = &[
    "GITHUB_SHA",            // GitHub Actions
    "CI_COMMIT_SHA",         // GitLab CI
    "BITBUCKET_COMMIT",      // Bitbucket Pipelines
    "CIRCLE_SHA1",           // CircleCI
    "BUILDKITE_COMMIT",      // Buildkite
    "GIT_COMMIT",            // Jenkins
    "SOURCE_VERSION",        // Heroku, AWS CodeBuild
    "COMMIT_SHA",            // Google Cloud Build
    "VERCEL_GIT_COMMIT_SHA", // Vercel
    "RENDER_GIT_COMMIT",     // Render
    "CF_PAGES_COMMIT_SHA",   // Cloudflare Pages
    "DRONE_COMMIT_SHA",      // Drone
    "TRAVIS_COMMIT",         // Travis CI
    "SEMAPHORE_GIT_SHA",     // Semaphore
    "BUILD_SOURCEVERSION",   // Azure Pipelines
];

/// Directory names never read as source inputs: build outputs, caches, fetched
/// dependencies and VCS metadata. Any dot-entry is skipped as well (a hidden
/// file is never a compiler input, and `.DS_Store` must not move the identity).
const EXCLUDED_DIRS: &[&str] = &[
    "sky-out",
    ".skyapp",
    ".skycache",
    ".skydeps",
    ".split",
    "node_modules",
    ".git",
    "dist",
];

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct AppBuildStamp {
    /// The compiler's release version (`v0.25.21`), or `dev` for a local build.
    pub sky_version: String,
    /// A 12-hex commit id, an override value, or `src-<sha256[:12]>`.
    pub commit: String,
    /// RFC 3339 UTC.
    pub built_at: String,
    /// `git`, `ci:<VAR>`, `content` or `override`.
    pub source: String,
}

/// The compiler's own version, baked at release (`SKY_BUILD_VERSION`, set by
/// release.yml), else `dev`.
pub fn compiler_version() -> String {
    option_env!("SKY_BUILD_VERSION")
        .map(|v| v.trim().trim_start_matches('v').to_string())
        .filter(|v| !v.is_empty() && v != "dev")
        .map(|v| format!("v{}", clean(&v)))
        .unwrap_or_else(|| "dev".to_string())
}

/// The stamp for a build of `project_root`: the one a parent `sky` pinned, else
/// resolved here.
pub fn resolve_build_stamp(project_root: &Path) -> AppBuildStamp {
    inherited_pin(project_root)
        .unwrap_or_else(|| resolve_with(project_root, &|k| std::env::var(k).ok()))
}

/// The stamp a parent `sky` pinned, when `project_root` lies inside the project
/// it was resolved for. The generated `.skyapp/` / `.split/` legs live under the
/// user's project, so they take it; a different project built by a process
/// that merely inherited the variable (an app started by `sky run` that itself
/// runs `sky build`) resolves its own identity.
fn inherited_pin(project_root: &Path) -> Option<AppBuildStamp> {
    let (root, stamp) = std::env::var(PINNED_ENV)
        .ok()
        .and_then(|v| decode_pinned(&v))?;
    canonical(project_root).starts_with(&root).then_some(stamp)
}

/// Resolve at `project_root` and pin the result in this process's environment,
/// so every child `sky` this command spawns embeds the same identity. A pin a
/// parent made for an enclosing project is kept as it is. Call once, at the top
/// of a build verb, before any thread is spawned.
pub fn pin_build_stamp(project_root: &Path) -> AppBuildStamp {
    if let Some(stamp) = inherited_pin(project_root) {
        return stamp;
    }
    let stamp = resolve_with(project_root, &|k| std::env::var(k).ok());
    std::env::set_var(PINNED_ENV, encode_pinned(&canonical(project_root), &stamp));
    stamp
}

fn canonical(p: &Path) -> PathBuf {
    p.canonicalize().unwrap_or_else(|_| p.to_path_buf())
}

/// The resolution order, with the environment injected for tests.
///
/// Commit:
///   1. `SKY_BUILD_COMMIT` (override) → source `override`;
///   2. `git rev-parse HEAD` in the project directory or any parent (the
///      project may be a subdirectory of a repository) → `git`;
///   3. the first [`CI_COMMIT_VARS`] entry holding a hex commit id → `ci:<VAR>`;
///   4. `src-<sha256[:12]>` over the project's source inputs → `content`.
///
/// Built-at:
///   1. `SKY_BUILD_EPOCH` (override, Unix seconds);
///   2. the commit time of `HEAD` when git resolves;
///   3. the newest modification time of the source inputs (`git archive | tar
///      -x` sets every file's mtime to the commit time, so an archive build
///      reports its commit time).
pub fn resolve_with(project_root: &Path, env: &dyn Fn(&str) -> Option<String>) -> AppBuildStamp {
    let get = |k: &str| {
        env(k)
            .map(|v| v.trim().to_string())
            .filter(|v| !v.is_empty())
    };
    let git = git_head(project_root);
    let mut inputs: Option<SourceInputs> = None;
    let mut inputs_of =
        |root: &Path| -> SourceInputs { inputs.get_or_insert_with(|| source_inputs(root)).clone() };

    let (commit, source) = if let Some(c) = get(COMMIT_OVERRIDE_ENV)
        .map(|c| clean(&c))
        .filter(|c| !c.is_empty())
    {
        (c, "override".to_string())
    } else if let Some((sha, _)) = &git {
        (sha.clone(), "git".to_string())
    } else if let Some((var, sha)) = ci_commit(&get) {
        (sha, format!("ci:{var}"))
    } else {
        (inputs_of(project_root).identity, "content".to_string())
    };

    let secs = get(EPOCH_OVERRIDE_ENV)
        .and_then(|s| s.parse::<u64>().ok())
        .or_else(|| git.as_ref().map(|(_, ct)| *ct))
        .or_else(|| inputs_of(project_root).newest_mtime);

    AppBuildStamp {
        sky_version: compiler_version(),
        commit,
        built_at: secs
            .map(rfc3339_utc)
            .unwrap_or_else(|| "unknown".to_string()),
        source,
    }
}

/// The first CI commit variable holding a hex commit id, shortened to 12.
pub fn ci_commit(get: &dyn Fn(&str) -> Option<String>) -> Option<(&'static str, String)> {
    CI_COMMIT_VARS.iter().find_map(|var| {
        get(var)
            .filter(|v| is_hex_commit(v))
            .map(|v| (*var, short(&v)))
    })
}

/// A git object id: 7 to 64 hex digits (SHA-1 is 40, SHA-256 64).
pub fn is_hex_commit(s: &str) -> bool {
    (7..=64).contains(&s.len()) && s.bytes().all(|b| b.is_ascii_hexdigit())
}

fn short(sha: &str) -> String {
    sha.to_ascii_lowercase().chars().take(12).collect()
}

/// `(sha[:12], commit time)` of `HEAD` in `dir` or any parent. `None` outside a
/// repository, in one with no commits, or when git is absent or refuses the
/// directory (e.g. "dubious ownership" in a container).
fn git_head(dir: &Path) -> Option<(String, u64)> {
    let out = Command::new("git")
        .arg("-C")
        .arg(dir)
        .args(["log", "-1", "--format=%H %ct", "HEAD"])
        .stdin(Stdio::null())
        .stderr(Stdio::null())
        .output()
        .ok()
        .filter(|o| o.status.success())?;
    let text = String::from_utf8_lossy(&out.stdout);
    let mut it = text.split_whitespace();
    let sha = it.next().filter(|s| is_hex_commit(s))?;
    let ct = it.next()?.parse::<u64>().ok()?;
    Some((short(sha), ct))
}

#[derive(Debug, Clone)]
struct SourceInputs {
    identity: String,
    newest_mtime: Option<u64>,
}

/// The project's source inputs — the source root tree (`[source] root`,
/// default `src`), `sky.toml` and `sky.lock` — hashed as sorted relative paths
/// plus bytes into `src-<sha256[:12]>`, with the newest file mtime.
fn source_inputs(root: &Path) -> SourceInputs {
    let mut files: Vec<(String, PathBuf)> = Vec::new();
    for top in ["sky.toml", "sky.lock"] {
        let p = root.join(top);
        if p.is_file() {
            files.push((top.to_string(), p));
        }
    }
    let src_rel = crate::configured_source_root(root);
    let src_rel = src_rel.trim_matches('/');
    let src_dir = if src_rel.is_empty() || src_rel == "." {
        root.to_path_buf()
    } else {
        root.join(src_rel)
    };
    walk(root, &src_dir, &mut files);
    files.sort_by(|a, b| a.0.cmp(&b.0));
    files.dedup_by(|a, b| a.0 == b.0);

    let mut h = crate::sha256::Sha256::new();
    let mut newest: Option<u64> = None;
    for (rel, path) in &files {
        let Ok(bytes) = std::fs::read(path) else {
            continue;
        };
        h.update(rel.as_bytes());
        h.update(&[0]);
        h.update(&(bytes.len() as u64).to_be_bytes());
        h.update(&bytes);
        let m = std::fs::metadata(path)
            .and_then(|m| m.modified())
            .ok()
            .and_then(|t| t.duration_since(std::time::UNIX_EPOCH).ok())
            .map(|d| d.as_secs());
        newest = newest.max(m);
    }
    let digest = h.finish();
    let hex: String = digest.iter().map(|b| format!("{b:02x}")).collect();
    SourceInputs {
        identity: format!("src-{}", &hex[..12]),
        newest_mtime: newest,
    }
}

fn walk(root: &Path, dir: &Path, out: &mut Vec<(String, PathBuf)>) {
    let Ok(entries) = std::fs::read_dir(dir) else {
        return;
    };
    for e in entries.flatten() {
        let name = e.file_name().to_string_lossy().into_owned();
        if name.starts_with('.') {
            continue;
        }
        let path = e.path();
        let Ok(ft) = e.file_type() else { continue };
        if ft.is_dir() {
            if EXCLUDED_DIRS.contains(&name.as_str()) {
                continue;
            }
            walk(root, &path, out);
        } else if path.is_file() {
            let rel = path
                .strip_prefix(root)
                .unwrap_or(&path)
                .components()
                .map(|c| c.as_os_str().to_string_lossy().into_owned())
                .collect::<Vec<_>>()
                .join("/");
            out.push((rel, path));
        }
    }
}

/// Reduce a value to characters that are safe in a Go string literal, an env
/// value and a log line, capped at 64.
fn clean(s: &str) -> String {
    s.chars()
        .filter(|c| c.is_ascii_alphanumeric() || matches!(c, '.' | '-' | '_' | ':' | '+'))
        .take(64)
        .collect()
}

/// `v1|version|commit|built_at|source|root`. Every field but the root is
/// `clean`ed (it holds no `|`), and the root is last so a `|` inside it
/// survives the split.
fn encode_pinned(root: &Path, s: &AppBuildStamp) -> String {
    format!(
        "v1|{}|{}|{}|{}|{}",
        s.sky_version,
        s.commit,
        s.built_at,
        s.source,
        root.display()
    )
}

fn decode_pinned(v: &str) -> Option<(PathBuf, AppBuildStamp)> {
    let parts: Vec<&str> = v.splitn(6, '|').collect();
    match parts.as_slice() {
        ["v1", version, commit, built_at, source, root]
            if !commit.is_empty() && !root.is_empty() =>
        {
            Some((
                PathBuf::from(root),
                AppBuildStamp {
                    sky_version: clean(version),
                    commit: clean(commit),
                    built_at: clean(built_at),
                    source: clean(source),
                },
            ))
        }
        _ => None,
    }
}

/// The generated `skybuildinfo/skybuildinfo.go`: the stamp's own package.
/// Deterministic for a given stamp, so a no-change rebuild writes identical
/// bytes and Go's cache hits. It is a separate package so that a new commit
/// recompiles this one small file and re-links, and never recompiles the
/// (large) `main` package.
pub fn go_source(s: &AppBuildStamp) -> String {
    format!(
        "// Code generated by sky build. DO NOT EDIT.\n\
         //\n\
         // The build identity reported at /_sky/buildinfo and in the Sky Console\n\
         // header. It is Go source, not a linker flag, so any `go build` of the\n\
         // emitted directory carries it. A `-ldflags \"-X sky-app/rt.buildCommit=...\"`\n\
         // still overrides it field by field.\n\
         \n\
         package {STAMP_PKG}\n\
         \n\
         import \"sky-app/rt\"\n\
         \n\
         func init() {{\n\
         \trt.SetBuildStamp({:?}, {:?}, {:?}, {:?})\n\
         }}\n",
        clean(&s.sky_version),
        clean(&s.commit),
        clean(&s.built_at),
        clean(&s.source),
    )
}

/// The generated `sky_buildinfo.go` in the `main` package: a blank import of
/// the stamp package, so `go build .` links it. Constant bytes.
pub fn go_main_import() -> String {
    format!(
        "// Code generated by sky build. DO NOT EDIT.\n\
         //\n\
         // Links the build identity (see {STAMP_PKG}/{STAMP_PKG}.go).\n\
         \n\
         package main\n\
         \n\
         import _ \"sky-app/{STAMP_PKG}\"\n"
    )
}

/// Write the generated files into `out_dir` (the emitted `package main`
/// directory), leaving each untouched when its bytes already match.
pub fn write_go_stamp(out_dir: &Path, s: &AppBuildStamp) -> std::io::Result<()> {
    let pkg = out_dir.join(STAMP_PKG);
    std::fs::create_dir_all(&pkg)?;
    write_if_changed(&pkg.join(format!("{STAMP_PKG}.go")), &go_source(s))?;
    write_if_changed(&out_dir.join(STAMP_GO_FILE), &go_main_import())
}

fn write_if_changed(path: &Path, src: &str) -> std::io::Result<()> {
    if std::fs::read_to_string(path).ok().as_deref() == Some(src) {
        return Ok(());
    }
    std::fs::write(path, src)
}

/// `YYYY-MM-DDTHH:MM:SSZ` for a Unix time (no chrono dependency; the civil-date
/// step is Howard Hinnant's civil_from_days, as in diagram.rs::today_utc).
pub fn rfc3339_utc(secs: u64) -> String {
    let days = (secs / 86_400) as i64;
    let rem = secs % 86_400;
    let z = days + 719_468;
    let era = if z >= 0 { z } else { z - 146_096 } / 146_097;
    let doe = z - era * 146_097;
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let y = yoe + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = doy - (153 * mp + 2) / 5 + 1;
    let m = if mp < 10 { mp + 3 } else { mp - 9 };
    let y = if m <= 2 { y + 1 } else { y };
    format!(
        "{y:04}-{m:02}-{d:02}T{:02}:{:02}:{:02}Z",
        rem / 3600,
        (rem % 3600) / 60,
        rem % 60
    )
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashMap;

    fn env_of(pairs: &[(&str, &str)]) -> impl Fn(&str) -> Option<String> {
        let m: HashMap<String, String> = pairs
            .iter()
            .map(|(k, v)| (k.to_string(), v.to_string()))
            .collect();
        move |k: &str| m.get(k).cloned()
    }

    fn tmp(tag: &str) -> PathBuf {
        let d = std::env::temp_dir().join(format!(
            "sky-stamp-unit-{tag}-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        std::fs::create_dir_all(d.join("src")).unwrap();
        std::fs::write(d.join("sky.toml"), "name = \"x\"\n").unwrap();
        std::fs::write(
            d.join("src").join("Main.sky"),
            "module Main exposing (main)\n",
        )
        .unwrap();
        d
    }

    /// A directory git will not discover a repository from, whatever the host's
    /// temp dir sits in.
    fn outside_git(d: &Path) -> bool {
        git_head(d).is_none()
    }

    #[test]
    fn rfc3339_formats_known_instants() {
        assert_eq!(rfc3339_utc(0), "1970-01-01T00:00:00Z");
        assert_eq!(rfc3339_utc(1_790_000_000), "2026-09-21T14:13:20Z");
    }

    #[test]
    fn hex_commit_validation() {
        assert!(is_hex_commit("0123456789abcdef0123456789abcdef01234567"));
        assert!(is_hex_commit("ABCDEF1"));
        assert!(!is_hex_commit("abc123")); // too short
        assert!(!is_hex_commit("main"));
        assert!(!is_hex_commit("refs/heads/main"));
        assert!(!is_hex_commit("0123456789abcdefg")); // non-hex digit
        assert!(!is_hex_commit(&"a".repeat(65)));
    }

    #[test]
    fn ci_table_takes_first_hex_value_and_skips_non_hex() {
        let sha = "0123456789ABCDEF0123456789abcdef01234567";
        // A non-hex GITHUB_SHA is skipped; the next valid variable wins.
        let get = env_of(&[("GITHUB_SHA", "not-a-sha"), ("CIRCLE_SHA1", sha)]);
        assert_eq!(
            ci_commit(&get),
            Some(("CIRCLE_SHA1", "0123456789ab".to_string()))
        );
        // Table order decides between two valid values.
        let get = env_of(&[
            ("BUILD_SOURCEVERSION", "fedcba9876543210"),
            ("GITHUB_SHA", sha),
        ]);
        assert_eq!(ci_commit(&get).map(|c| c.0), Some("GITHUB_SHA"));
        // Every documented system is in the table.
        for v in [
            "GITHUB_SHA",
            "CI_COMMIT_SHA",
            "BITBUCKET_COMMIT",
            "CIRCLE_SHA1",
            "BUILDKITE_COMMIT",
            "GIT_COMMIT",
            "SOURCE_VERSION",
            "COMMIT_SHA",
            "VERCEL_GIT_COMMIT_SHA",
            "RENDER_GIT_COMMIT",
            "CF_PAGES_COMMIT_SHA",
            "DRONE_COMMIT_SHA",
            "TRAVIS_COMMIT",
            "SEMAPHORE_GIT_SHA",
            "BUILD_SOURCEVERSION",
        ] {
            let get = env_of(&[(v, sha)]);
            assert_eq!(ci_commit(&get).map(|c| c.0), Some(v), "{v} must be read");
        }
        assert_eq!(ci_commit(&env_of(&[])), None);
    }

    #[test]
    fn resolution_order_override_then_ci_then_content() {
        let d = tmp("order");
        if !outside_git(&d) {
            // The host temp dir sits inside a repository; the git leg is
            // covered by the integration tests instead.
            let _ = std::fs::remove_dir_all(&d);
            return;
        }
        let sha = "89abcdef0123456789abcdef0123456789abcdef";
        let s = resolve_with(
            &d,
            &env_of(&[
                ("SKY_BUILD_COMMIT", "release-7"),
                ("SKY_BUILD_EPOCH", "1790000000"),
                ("GITHUB_SHA", sha),
            ]),
        );
        assert_eq!(
            (s.commit.as_str(), s.source.as_str()),
            ("release-7", "override")
        );
        assert_eq!(s.built_at, "2026-09-21T14:13:20Z");

        let s = resolve_with(&d, &env_of(&[("GITHUB_SHA", sha)]));
        assert_eq!(
            (s.commit.as_str(), s.source.as_str()),
            ("89abcdef0123", "ci:GITHUB_SHA")
        );

        // A placeholder CI value is not a commit: fall through to content.
        let s = resolve_with(&d, &env_of(&[("GITHUB_SHA", "unknown")]));
        assert_eq!(s.source, "content");
        assert!(
            s.commit.starts_with("src-") && s.commit.len() == 16,
            "{}",
            s.commit
        );
        assert_ne!(s.built_at, "unknown");
        assert!(!s.sky_version.is_empty());
        let _ = std::fs::remove_dir_all(&d);
    }

    #[test]
    fn content_identity_is_stable_and_tracks_source_bytes_only() {
        let d = tmp("content");
        let none = env_of(&[]);
        let a = source_inputs(&d).identity;
        // Build outputs, caches and hidden files do not move it.
        for junk in [
            "sky-out",
            ".skyapp",
            ".skycache",
            "src/.split",
            "node_modules",
        ] {
            std::fs::create_dir_all(d.join(junk)).unwrap();
            std::fs::write(d.join(junk).join("x.go"), "package main").unwrap();
        }
        std::fs::write(d.join("src").join(".DS_Store"), "junk").unwrap();
        assert_eq!(source_inputs(&d).identity, a);
        // A source byte does.
        std::fs::write(
            d.join("src").join("Main.sky"),
            "module Main exposing (main)\n-- x\n",
        )
        .unwrap();
        let b = source_inputs(&d).identity;
        assert_ne!(a, b);
        // So does sky.lock.
        std::fs::write(d.join("sky.lock"), "lock").unwrap();
        assert_ne!(source_inputs(&d).identity, b);
        // The resolved content stamp never reads the wall clock: two resolves
        // a second apart agree.
        if outside_git(&d) {
            let x = resolve_with(&d, &none);
            std::thread::sleep(std::time::Duration::from_millis(1100));
            assert_eq!(x, resolve_with(&d, &none));
        }
        let _ = std::fs::remove_dir_all(&d);
    }

    #[test]
    fn pinned_stamp_round_trips_and_rejects_junk() {
        let s = AppBuildStamp {
            sky_version: "v0.25.21".into(),
            commit: "0123456789ab".into(),
            built_at: "2026-09-21T14:13:20Z".into(),
            source: "ci:GITHUB_SHA".into(),
        };
        let root = Path::new("/w/my|app");
        assert_eq!(
            decode_pinned(&encode_pinned(root, &s)),
            Some((root.to_path_buf(), s))
        );
        assert_eq!(decode_pinned("garbage"), None);
        assert_eq!(decode_pinned("v1|v||t|git|/w"), None);
        assert_eq!(decode_pinned("v1|v|c|t|git"), None);
    }

    #[test]
    fn go_source_is_deterministic_and_cannot_be_injected() {
        let s = AppBuildStamp {
            sky_version: "v0.25.21".into(),
            commit: "x\"); panic(\"boom".into(),
            built_at: "2026-09-21T14:13:20Z".into(),
            source: "override".into(),
        };
        let a = go_source(&s);
        assert_eq!(a, go_source(&s));
        assert!(a.contains("rt.SetBuildStamp(\"v0.25.21\", \"xpanicboom\", \"2026-09-21T14:13:20Z\", \"override\")"), "{a}");
        assert!(a.starts_with("// Code generated"));
        assert!(a.contains("package skybuildinfo"), "{a}");
    }

    /// A new commit rewrites only the small stamp package; the file in the
    /// (large) `main` package is constant, so Go never recompiles `main` for it.
    #[test]
    fn write_go_stamp_keeps_the_main_package_file_constant() {
        let d = tmp("gofiles");
        let mut s = AppBuildStamp {
            sky_version: "dev".into(),
            commit: "0123456789ab".into(),
            built_at: "2026-09-21T14:13:20Z".into(),
            source: "git".into(),
        };
        write_go_stamp(&d, &s).unwrap();
        let main_a = std::fs::read_to_string(d.join(STAMP_GO_FILE)).unwrap();
        assert!(
            main_a.contains("import _ \"sky-app/skybuildinfo\""),
            "{main_a}"
        );
        s.commit = "ba9876543210".into();
        write_go_stamp(&d, &s).unwrap();
        assert_eq!(
            std::fs::read_to_string(d.join(STAMP_GO_FILE)).unwrap(),
            main_a
        );
        let pkg = std::fs::read_to_string(d.join("skybuildinfo").join("skybuildinfo.go")).unwrap();
        assert!(pkg.contains("ba9876543210"), "{pkg}");
        let _ = std::fs::remove_dir_all(&d);
    }
}

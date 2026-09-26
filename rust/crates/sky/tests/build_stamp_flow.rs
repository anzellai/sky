//! The build identity (`/_sky/buildinfo`: commit, builtAt, skyVersion, source)
//! is automatic for every normal build path, with no configuration.
//!
//! v0.25.20 stamped it only as `-X` linker flags on the `go build` that `sky
//! build` ran itself. A real app's deploy (GitHub Actions) extracted
//! `git archive HEAD` into a directory with no `.git`, ran `sky build`, then
//! cross-compiled `sky-out/` with its own plain `go build` — and production
//! reported `{"commit":"dev","builtAt":"unknown","skyVersion":"dev"}`.
//!
//! These tests reproduce that flow and its neighbours on a small HTTP server
//! app. The same flow through `--target web:app` (both split legs) is the
//! heavy `web_app_archive_build_reports_the_ci_commit` in spa_target_flow.rs.

use std::io::{Read, Write};
use std::net::TcpStream;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::Mutex;
use std::time::Duration;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

/// One heavy build at a time (16 GB hosts).
static BUILD_LOCK: Mutex<()> = Mutex::new(());

/// 2026-09-21T14:13:20Z.
const COMMIT_EPOCH: u64 = 1_790_000_000;
const COMMIT_DATE: &str = "2026-09-21T14:13:20Z";

/// Every variable the resolver reads, cleared from each child so the host's own
/// CI (GitHub Actions sets GITHUB_SHA) cannot leak into a scenario.
const STAMP_ENV: &[&str] = &[
    "SKY_BUILD_COMMIT",
    "SKY_BUILD_EPOCH",
    "SKY_BUILD_STAMP_PINNED",
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
];

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .output()
        .map(|o| o.status.success())
        .unwrap_or(false)
}

fn scratch(tag: &str) -> PathBuf {
    let d = std::env::temp_dir().join(format!(
        "sky-stamp-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(&d).unwrap();
    d
}

/// A minimal HTTP server project listening on `port`.
fn write_project(dir: &Path, port: u16) {
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"stamp\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
    )
    .unwrap();
    std::fs::write(
        dir.join("src").join("Main.sky"),
        format!(
            "module Main exposing (main)\n\n\
             import Sky.Core.Prelude exposing (..)\n\
             import Sky.Core.Task as Task\n\
             import Sky.Http.Server as Server\n\n\n\
             main =\n    Server.listen {port}\n        [ Server.get \"/\" (\\_ -> Task.succeed (Server.text \"ok\")) ]\n"
        ),
    )
    .unwrap();
}

/// Run git with a fixed identity and date, and no user or system config.
fn git(dir: &Path, args: &[&str]) {
    let out = Command::new("git")
        .args([
            "-c",
            "commit.gpgsign=false",
            "-c",
            "init.defaultBranch=main",
        ])
        .args(args)
        .current_dir(dir)
        .env("GIT_CONFIG_GLOBAL", "/dev/null")
        .env("GIT_CONFIG_NOSYSTEM", "1")
        .env("GIT_AUTHOR_NAME", "t")
        .env("GIT_AUTHOR_EMAIL", "t@example.test")
        .env("GIT_COMMITTER_NAME", "t")
        .env("GIT_COMMITTER_EMAIL", "t@example.test")
        .env("GIT_AUTHOR_DATE", format!("@{COMMIT_EPOCH} +0000"))
        .env("GIT_COMMITTER_DATE", format!("@{COMMIT_EPOCH} +0000"))
        .output()
        .expect("git is required for this test (install git)");
    assert!(
        out.status.success(),
        "git {args:?}: {}",
        String::from_utf8_lossy(&out.stderr)
    );
}

fn head_sha(dir: &Path) -> String {
    let out = Command::new("git")
        .args(["rev-parse", "HEAD"])
        .current_dir(dir)
        .output()
        .unwrap();
    String::from_utf8_lossy(&out.stdout).trim().to_string()
}

/// A `Command` with every stamp variable cleared and git discovery stopped at
/// `ceiling`, so a host checkout above the temp dir is never found.
fn clean_cmd(program: &str, ceiling: &Path) -> Command {
    let mut c = Command::new(program);
    for v in STAMP_ENV {
        c.env_remove(v);
    }
    c.env("GIT_CEILING_DIRECTORIES", ceiling);
    c
}

fn sky_build(dir: &Path, ceiling: &Path, envs: &[(&str, &str)]) {
    let mut c = clean_cmd(SKY, ceiling);
    c.args(["build", "src/Main.sky"]).current_dir(dir);
    for (k, v) in envs {
        c.env(k, v);
    }
    let out = c.output().expect("run sky build");
    assert!(
        out.status.success(),
        "sky build failed:\n{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
}

/// A plain `go build` of `sky-out/`, the way a deploy script cross-compiles it:
/// no Sky involvement, no flags.
fn plain_go_build(sky_out: &Path, ceiling: &Path, out: &str, goos_arch: Option<(&str, &str)>) {
    let mut c = clean_cmd("go", ceiling);
    c.args(["build", "-o", out, "."])
        .current_dir(sky_out)
        .env("CGO_ENABLED", "0");
    if let Some((os, arch)) = goos_arch {
        c.env("GOOS", os).env("GOARCH", arch);
    }
    let o = c.output().expect("run go build");
    assert!(
        o.status.success(),
        "plain go build of sky-out failed:\n{}",
        String::from_utf8_lossy(&o.stderr)
    );
}

fn http_get(port: u16, path: &str) -> Option<String> {
    let mut s = TcpStream::connect(("127.0.0.1", port)).ok()?;
    s.set_read_timeout(Some(Duration::from_secs(5))).ok()?;
    write!(
        s,
        "GET {path} HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n"
    )
    .ok()?;
    let mut buf = String::new();
    s.read_to_string(&mut buf).ok()?;
    buf.split_once("\r\n\r\n").map(|(_, b)| b.to_string())
}

/// Start `bin`, read `/_sky/buildinfo`, stop it (by its own PID).
fn buildinfo(bin: &Path, cwd: &Path, port: u16) -> serde_json::Value {
    let mut child = Command::new(bin)
        .current_dir(cwd)
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .spawn()
        .expect("spawn the app");
    let mut body = None;
    for _ in 0..200 {
        std::thread::sleep(Duration::from_millis(100));
        if let Some(b) = http_get(port, "/_sky/buildinfo") {
            if b.trim_start().starts_with('{') {
                body = Some(b);
                break;
            }
        }
    }
    let _ = child.kill();
    let _ = child.wait();
    let body = body.unwrap_or_else(|| panic!("{} never served /_sky/buildinfo", bin.display()));
    // The body may be chunked; take the JSON object.
    let start = body.find('{').unwrap();
    let end = body.rfind('}').unwrap();
    serde_json::from_str(&body[start..=end]).unwrap_or_else(|e| panic!("{e}: {body}"))
}

fn field<'a>(v: &'a serde_json::Value, k: &str) -> &'a str {
    v.get(k).and_then(|x| x.as_str()).unwrap_or("")
}

/// (a) The real-app deploy flow: `git archive HEAD | tar -x` (no `.git`),
/// GITHUB_SHA set (GitHub Actions always sets it), `sky build`, then a PLAIN
/// `go build` of sky-out. The binary reports the CI commit and the commit date.
#[test]
fn archive_build_with_ci_sha_then_plain_go_build_reports_the_commit() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let _g = BUILD_LOCK.lock().unwrap_or_else(|e| e.into_inner());
    let base = scratch("archive");
    let repo = base.join("repo");
    let port = 9661;
    write_project(&repo, port);
    git(&repo, &["init", "-q"]);
    git(&repo, &["add", "-A"]);
    git(&repo, &["commit", "-q", "-m", "app"]);
    let sha = head_sha(&repo);
    let tar = base.join("src.tar");
    git(
        &repo,
        &[
            "archive",
            "--format=tar",
            "-o",
            tar.to_str().unwrap(),
            "HEAD",
        ],
    );
    let build = base.join("build");
    std::fs::create_dir_all(&build).unwrap();
    let ok = Command::new("tar")
        .arg("-xf")
        .arg(&tar)
        .arg("-C")
        .arg(&build)
        .status()
        .unwrap()
        .success();
    assert!(ok, "tar -x failed");
    assert!(!build.join(".git").exists());

    sky_build(&build, &base, &[("GITHUB_SHA", &sha)]);
    let sky_out = build.join("sky-out");
    plain_go_build(&sky_out, &base, "app-plain", None);
    let bi = buildinfo(&sky_out.join("app-plain"), &build, port);
    assert_eq!(field(&bi, "commit"), &sha[..12], "{bi}");
    assert_eq!(field(&bi, "builtAt"), COMMIT_DATE, "{bi}");
    assert_eq!(field(&bi, "source"), "ci:GITHUB_SHA", "{bi}");
    assert!(
        !field(&bi, "skyVersion").is_empty() && field(&bi, "skyVersion") != "unknown",
        "{bi}"
    );
    let _ = std::fs::remove_dir_all(&base);
}

/// (b) No git and no CI variables: a content identity `src-<hash>` that is
/// stable across two builds and moves when a source byte does; built-at is the
/// newest source mtime.
#[test]
fn no_git_no_ci_reports_a_stable_content_identity() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let _g = BUILD_LOCK.lock().unwrap_or_else(|e| e.into_inner());
    let base = scratch("content");
    let proj = base.join("p");
    let port = 9662;
    write_project(&proj, port);
    // Pin the source mtimes, as `tar -x` of an archive would.
    for f in ["sky.toml", "src/Main.sky"] {
        let ok = Command::new("touch")
            .args(["-t", "202609211413.20"])
            .arg(proj.join(f))
            .env("TZ", "UTC")
            .status()
            .unwrap()
            .success();
        assert!(ok);
    }
    let bin = proj.join("sky-out").join("app");

    sky_build(&proj, &base, &[]);
    let first = buildinfo(&bin, &proj, port);
    assert!(field(&first, "commit").starts_with("src-"), "{first}");
    assert_eq!(field(&first, "commit").len(), 16, "{first}");
    assert_eq!(field(&first, "source"), "content", "{first}");
    assert_eq!(field(&first, "builtAt"), COMMIT_DATE, "{first}");

    std::thread::sleep(Duration::from_millis(1100));
    sky_build(&proj, &base, &[]);
    let second = buildinfo(&bin, &proj, port);
    assert_eq!(first, second, "a no-change rebuild must stamp the same");

    let main = proj.join("src").join("Main.sky");
    let mut src = std::fs::read_to_string(&main).unwrap();
    src.push_str("\n-- a changed byte\n");
    std::fs::write(&main, src).unwrap();
    sky_build(&proj, &base, &[]);
    let third = buildinfo(&bin, &proj, port);
    assert_ne!(
        field(&third, "commit"),
        field(&first, "commit"),
        "a source change must move the content identity"
    );
    assert_ne!(field(&third, "builtAt"), COMMIT_DATE, "newest mtime moved");
    let _ = std::fs::remove_dir_all(&base);
}

/// (c) The project is a subdirectory of a git repository: the repository's
/// HEAD and its commit time.
#[test]
fn project_in_a_repo_subdirectory_reports_git_head() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let _g = BUILD_LOCK.lock().unwrap_or_else(|e| e.into_inner());
    let base = scratch("subdir");
    let repo = base.join("repo");
    let proj = repo.join("apps").join("web");
    let port = 9663;
    write_project(&proj, port);
    git(&repo, &["init", "-q"]);
    git(&repo, &["add", "-A"]);
    git(&repo, &["commit", "-q", "-m", "app"]);
    let sha = head_sha(&repo);

    // A CI variable naming a different commit loses to the checkout's HEAD.
    sky_build(
        &proj,
        &base,
        &[("GITHUB_SHA", "ffffffffffffffffffffffffffffffffffffffff")],
    );
    let bi = buildinfo(&proj.join("sky-out").join("app"), &proj, port);
    assert_eq!(field(&bi, "commit"), &sha[..12], "{bi}");
    assert_eq!(field(&bi, "builtAt"), COMMIT_DATE, "{bi}");
    assert_eq!(field(&bi, "source"), "git", "{bi}");
    let _ = std::fs::remove_dir_all(&base);
}

/// The optional overrides still work, and a user's own `-ldflags -X` wins
/// over the generated stamp.
#[test]
fn overrides_and_user_ldflags_take_precedence() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let _g = BUILD_LOCK.lock().unwrap_or_else(|e| e.into_inner());
    let base = scratch("override");
    let proj = base.join("p");
    let port = 9664;
    write_project(&proj, port);
    sky_build(
        &proj,
        &base,
        &[
            ("SKY_BUILD_COMMIT", "release-42"),
            ("SKY_BUILD_EPOCH", "1790000000"),
        ],
    );
    let sky_out = proj.join("sky-out");
    let bi = buildinfo(&sky_out.join("app"), &proj, port);
    assert_eq!(field(&bi, "commit"), "release-42", "{bi}");
    assert_eq!(field(&bi, "builtAt"), COMMIT_DATE, "{bi}");
    assert_eq!(field(&bi, "source"), "override", "{bi}");

    let mut c = clean_cmd("go", &base);
    let o = c
        .args([
            "build",
            "-ldflags=-X sky-app/rt.buildCommit=userpin",
            "-o",
            "app-ld",
            ".",
        ])
        .current_dir(&sky_out)
        .env("CGO_ENABLED", "0")
        .output()
        .unwrap();
    assert!(o.status.success(), "{}", String::from_utf8_lossy(&o.stderr));
    let bi = buildinfo(&sky_out.join("app-ld"), &proj, port);
    assert_eq!(field(&bi, "commit"), "userpin", "{bi}");
    assert_eq!(field(&bi, "source"), "ldflags", "{bi}");
    assert_eq!(field(&bi, "builtAt"), COMMIT_DATE, "{bi}");
    let _ = std::fs::remove_dir_all(&base);
}

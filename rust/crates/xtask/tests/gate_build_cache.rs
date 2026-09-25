//! `scripts/lib/gate-build-cache.sh` must never serve a stale artefact.
//!
//! The cache lets the example sweep, the browser gate, the e2e scripts and
//! `xtask build-run` reuse a project build instead of repeating it. Reuse is
//! only sound when EVERY input that decides the build is identical, so these
//! tests drive the real library against a stand-in compiler and assert the
//! direction that matters: a changed source file misses, a rebuilt compiler
//! misses, `SKY_GATE_CACHE=off` never reads, a network-floating project is
//! never cached, a non-clean build never stores — and an identical rerun hits.

use std::path::{Path, PathBuf};
use std::process::Command;

fn repo() -> PathBuf {
    PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../.."))
}

struct World {
    unclean: bool,
    root: PathBuf,
    sky: PathBuf,
    project: PathBuf,
    cache: PathBuf,
}

/// A stand-in `sky`: `build` writes `sky-out/app` from the project's source and
/// a build counter, so a test can tell a restored artefact from a fresh build.
fn world(tag: &str) -> World {
    let root = std::env::temp_dir().join(format!("sky-gate-cache-{tag}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&root);
    std::fs::create_dir_all(root.join("bin")).unwrap();
    let sky = root.join("bin/sky");
    write_sky(&sky, "v1");
    let project = root.join("proj");
    std::fs::create_dir_all(project.join("src")).unwrap();
    std::fs::write(project.join("sky.toml"), "name = \"proj\"\n").unwrap();
    std::fs::write(project.join("src/Main.sky"), "main = 1\n").unwrap();
    World {
        unclean: false,
        cache: root.join("cache"),
        root,
        sky,
        project,
    }
}

fn write_sky(path: &Path, version: &str) {
    let counter = path.with_file_name("builds");
    std::fs::write(
        path,
        format!(
            "#!/bin/bash\n\
             # stand-in compiler {version}\n\
             n=$(cat '{c}' 2>/dev/null || echo 0); n=$((n+1)); echo $n > '{c}'\n\
             mkdir -p sky-out && {{ echo '{version}'; cat src/Main.sky; echo build-$n; }} > sky-out/app\n\
             if [ -f big.bin ]; then cp big.bin sky-out/; fi\n",
            c = counter.display()
        ),
    )
    .unwrap();
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o755)).unwrap();
    }
}

impl World {
    fn build(&self, extra_env: &[(&str, &str)], flags: &[&str]) -> (bool, String) {
        let lib = repo().join("scripts/lib/gate-build-cache.sh");
        // `/bin/bash` is 3.2 on stock macOS — the compatibility floor the
        // repo's scripts hold to — so the library is exercised under it there.
        let shell = if Path::new("/bin/bash").exists() {
            "/bin/bash"
        } else {
            "bash"
        };
        let mut cmd = Command::new(shell);
        cmd.arg(&lib)
            .arg("build")
            .arg(&self.sky)
            .arg(&self.project)
            .args(flags)
            .args(if self.unclean {
                &[][..]
            } else {
                &["--clean"][..]
            })
            .args(["--", "build", "src/Main.sky"])
            .env("SKY_GATE_CACHE_DIR", &self.cache)
            .env("SKY_GATE_CACHE", "on")
            .env("SKY_GATE_CACHE_MIN_FREE_MB", "0");
        for (k, v) in extra_env {
            cmd.env(k, v);
        }
        let out = cmd.output().expect("bash runs");
        (
            out.status.success(),
            String::from_utf8_lossy(&out.stderr).into_owned(),
        )
    }

    fn app(&self) -> String {
        std::fs::read_to_string(self.project.join("sky-out/app")).unwrap_or_default()
    }

    fn builds(&self) -> u32 {
        std::fs::read_to_string(self.root.join("bin/builds"))
            .ok()
            .and_then(|s| s.trim().parse().ok())
            .unwrap_or(0)
    }
}

impl Drop for World {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.root);
    }
}

#[test]
fn an_identical_rerun_hits_and_restores_the_artefact() {
    let w = world("hit");
    let (ok, log) = w.build(&[], &[]);
    assert!(ok && log.contains("MISS"), "{log}");
    let first = w.app();
    std::fs::remove_dir_all(w.project.join("sky-out")).unwrap();

    let (ok, log) = w.build(&[], &[]);
    assert!(
        ok && log.contains("HIT"),
        "an identical rerun must hit: {log}"
    );
    assert_eq!(w.builds(), 1, "a hit must not build");
    assert_eq!(w.app(), first, "a hit restores the stored artefact");
}

#[test]
fn a_changed_source_file_misses() {
    let w = world("src");
    w.build(&[], &[]);
    std::fs::write(w.project.join("src/Main.sky"), "main = 2\n").unwrap();
    let (ok, log) = w.build(&[], &[]);
    assert!(ok && log.contains("MISS"), "a source edit must miss: {log}");
    assert!(
        w.app().contains("main = 2"),
        "the artefact is from the new source"
    );
    assert_eq!(w.builds(), 2);
}

#[test]
fn a_new_file_in_the_project_misses() {
    let w = world("newfile");
    w.build(&[], &[]);
    std::fs::write(w.project.join("src/Extra.sky"), "x = 1\n").unwrap();
    let (_, log) = w.build(&[], &[]);
    assert!(log.contains("MISS"), "{log}");
}

#[test]
fn a_rebuilt_compiler_misses() {
    let w = world("compiler");
    w.build(&[], &[]);
    write_sky(&w.sky, "v2");
    let (ok, log) = w.build(&[], &[]);
    assert!(
        ok && log.contains("MISS"),
        "a rebuilt compiler must miss: {log}"
    );
    assert!(
        w.app().starts_with("v2"),
        "the artefact is from the new compiler"
    );
}

#[test]
fn different_arguments_or_environment_miss() {
    let w = world("args");
    w.build(&[], &[]);
    let (_, log) = w.build(&[("SKY_SOMETHING", "1")], &[]);
    assert!(log.contains("MISS"), "a SKY_* change must miss: {log}");
    let (_, log) = w.build(&[], &["--artefact", "sky-out", "--artefact", "other"]);
    assert!(
        log.contains("MISS"),
        "a different artefact set must miss: {log}"
    );
}

#[test]
fn off_never_reads_or_writes() {
    let w = world("off");
    let (ok, log) = w.build(&[("SKY_GATE_CACHE", "off")], &[]);
    assert!(ok && log.contains("OFF"), "{log}");
    let (_, log) = w.build(&[("SKY_GATE_CACHE", "off")], &[]);
    assert!(log.contains("OFF"), "{log}");
    assert_eq!(w.builds(), 2, "off builds every time");
    assert!(!w.cache.join("entries").exists(), "off wrote to the cache");
}

#[test]
fn a_project_with_floating_go_dependencies_is_never_cached() {
    let w = world("godeps");
    std::fs::write(
        w.project.join("sky.toml"),
        "name = \"proj\"\n\n[go.dependencies]\n\"github.com/google/uuid\" = \"latest\"\n",
    )
    .unwrap();
    w.build(&[], &[]);
    let (_, log) = w.build(&[], &[]);
    assert!(log.contains("UNCACHEABLE"), "{log}");
    assert_eq!(w.builds(), 2);
}

#[test]
fn a_build_that_is_not_clean_slate_never_stores() {
    let mut w = world("nostore");
    w.unclean = true;
    w.build(&[], &[]);
    let (_, log) = w.build(&[], &[]);
    assert!(
        log.contains("MISS"),
        "a call without --clean must not have stored: {log}"
    );
    assert_eq!(w.builds(), 2);
}

#[test]
fn a_failed_build_is_not_stored() {
    let w = world("fail");
    std::fs::write(&w.sky, "#!/bin/bash\nexit 3\n").unwrap();
    let (ok, _) = w.build(&[], &[]);
    assert!(!ok, "the build's failure is the call's failure");
    assert!(
        std::fs::read_dir(w.cache.join("entries"))
            .map(|d| d.count() == 0)
            .unwrap_or(true),
        "a failed build left a cache entry"
    );
}

#[test]
fn the_cache_is_pruned_to_its_bound() {
    let w = world("prune");
    for i in 0..3 {
        std::fs::write(w.project.join("src/Main.sky"), format!("main = {i}\n")).unwrap();
        std::fs::write(w.project.join("big.bin"), vec![b'x'; 700 * 1024]).unwrap();
        w.build(
            &[("SKY_GATE_CACHE_MAX_MB", "1")],
            &["--artefact", "sky-out"],
        );
    }
    let kb: u64 = String::from_utf8_lossy(
        &Command::new("du")
            .args(["-sk"])
            .arg(w.cache.join("entries"))
            .output()
            .unwrap()
            .stdout,
    )
    .split_whitespace()
    .next()
    .and_then(|s| s.parse().ok())
    .unwrap_or(u64::MAX);
    assert!(kb <= 1024, "cache is {kb} KB, bound is 1024 KB");
}

/// The key leaves `SKY_RUNTIME_DIR` out, because the sweep exports it and the
/// browser gate does not, and only the retired Haskell compiler read it. That
/// is safe only while no Rust compiler source reads it: if one starts to, the
/// variable changes what a build produces and must be back in the key.
#[test]
fn the_env_var_the_key_ignores_is_read_by_no_compiler_source() {
    fn walk(dir: &Path, hits: &mut Vec<String>) {
        let Ok(rd) = std::fs::read_dir(dir) else {
            return;
        };
        for e in rd.flatten() {
            let p = e.path();
            if p.is_dir() {
                if p.file_name().is_some_and(|n| n == "target" || n == "tests") {
                    continue;
                }
                walk(&p, hits);
            } else if p.extension().is_some_and(|x| x == "rs") {
                let text = std::fs::read_to_string(&p).unwrap_or_default();
                if text.contains("SKY_RUNTIME_DIR") {
                    hits.push(p.display().to_string());
                }
            }
        }
    }
    let mut hits = Vec::new();
    walk(&repo().join("rust/crates"), &mut hits);
    hits.retain(|h| !h.contains("/xtask/"));
    assert!(
        hits.is_empty(),
        "a compiler source now reads SKY_RUNTIME_DIR, which scripts/lib/gate-build-cache.sh \
         leaves out of its key — put it back in the key: {hits:?}"
    );
}

/// On a GitHub Actions runner the cache is off unless asked for: each CI job
/// builds a project once on a fresh disk, so a store only spends disk.
#[test]
fn a_ci_runner_defaults_to_off() {
    let w = world("ci");
    let lib = repo().join("scripts/lib/gate-build-cache.sh");
    let out = Command::new("bash")
        .arg(&lib)
        .arg("build")
        .arg(&w.sky)
        .arg(&w.project)
        .args(["--clean", "--", "build", "src/Main.sky"])
        .env("SKY_GATE_CACHE_DIR", &w.cache)
        .env_remove("SKY_GATE_CACHE")
        .env("GITHUB_ACTIONS", "true")
        .output()
        .unwrap();
    let log = String::from_utf8_lossy(&out.stderr);
    assert!(out.status.success() && log.contains("OFF"), "{log}");
    assert!(!w.cache.join("entries").exists());
}

/// A nearly full disk gets no new entry: the build still succeeds and its
/// output is used, but nothing is stored.
#[test]
fn a_nearly_full_disk_is_not_stored_to() {
    let w = world("floor");
    let floor = [("SKY_GATE_CACHE_MIN_FREE_MB", "999999999")];
    let (ok, log) = w.build(&floor, &[]);
    assert!(ok && log.contains("not stored"), "{log}");
    let (_, log) = w.build(&floor, &[]);
    assert!(
        log.contains("MISS"),
        "nothing was stored, so a rerun misses: {log}"
    );
}

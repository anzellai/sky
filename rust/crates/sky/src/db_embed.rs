//! `sky build --embed` — where the PostgreSQL bundle the binary carries comes
//! from.
//!
//! P5b's one decision, and its reasoning.
//!
//! **`sky build --embed` provisions on demand.** It does not require a prior
//! `sky db provision --embed`. The design document's rule is "no runtime fetch
//! on a production path", and a *build* is not a production path: it happens on
//! a developer's machine or a CI runner, both of which already fetch Go modules
//! and Sky dependencies, and the alternative — refusing to build until a second
//! command has been run — makes the first `sky build --embed` on a clean
//! checkout fail with an instruction rather than a binary. The property the
//! rule protects is that a *deployed* `./app --embed` never reaches the network;
//! that property is untouched, because everything the app needs is inside it by
//! then.
//!
//! Two things follow from that decision, and both are load-bearing:
//!
//! - **An already-provisioned cache is never re-downloaded.** A machine that has
//!   run `sky db provision --embed` holds the EXTRACTED tree, not the archive,
//!   and `go:embed` cannot take the tree (mode 0444 for every file, no symlinks
//!   at all — the unpacked `postgres` would not be executable and
//!   `libpq.5.dylib` would not exist). So for the host platform the tree is
//!   re-tarred into the bundle cache instead of being fetched again, and a
//!   machine that provisioned once builds `--embed` offline forever after.
//! - **Cross-compilation asks for the other platform's bundle by name.** The
//!   same fetch that serves the host serves `GOOS=linux GOARCH=arm64`, so a
//!   cross-build gets the right PostgreSQL rather than the host's. What it can
//!   never do is silently embed the host's: a target with no bundle is refused
//!   with the platform named.

use std::path::{Path, PathBuf};
use std::process::Command;

use crate::db_provision;

/// Where `sky build --embed` keeps verified archives, beside — not inside —
/// `postgres/<version>/`, which `sky db start`'s discovery enumerates. A
/// `.tar.gz` sitting in a directory that is scanned for `bin/` is at best noise
/// and at worst a candidate.
const BUNDLE_CACHE_DIR: &str = "postgres-bundles";

/// The build target's platform, in P2b's bundle vocabulary.
///
/// `go build` inherits this process's environment, so `GOOS` / `GOARCH` in the
/// environment ARE the cross-compilation lever for the whole `sky build`
/// pipeline; reading the same two variables is what keeps the embedded bundle
/// and the compiled binary talking about one machine.
pub fn target_platform() -> Result<&'static str, String> {
    platform_for_go_env(
        &std::env::var("GOOS").unwrap_or_default(),
        &std::env::var("GOARCH").unwrap_or_default(),
    )
}

/// [`target_platform`]'s decision, with the environment passed in — so the
/// cross-compilation rules are testable without mutating process state that
/// every other test in this binary shares.
pub fn platform_for_go_env(goos: &str, goarch: &str) -> Result<&'static str, String> {
    let goos = goos.trim();
    let goarch = goarch.trim();
    if goos.is_empty() && goarch.is_empty() {
        return db_provision::platform_tag().ok_or_else(|| {
            db_provision::unsupported_platform_message(std::env::consts::OS, std::env::consts::ARCH)
        });
    }
    // One of the two set alone still cross-compiles — Go defaults the other to
    // the host — so resolve each independently rather than demanding both.
    let os = if goos.is_empty() { host_goos() } else { goos };
    let arch = if goarch.is_empty() {
        host_goarch()
    } else {
        goarch
    };
    match (os, arch) {
        ("linux", "amd64") => Ok("linux-amd64"),
        ("linux", "arm64") => Ok("linux-arm64"),
        ("darwin", "amd64") => Ok("darwin-amd64"),
        ("darwin", "arm64") => Ok("darwin-arm64"),
        _ => Err(format!(
            "sky build --embed: no PostgreSQL bundle is published for GOOS={os} GOARCH={arch}.\n\
             \n\
             --embed makes the binary platform-specific, so the bundle has to match the\n\
             target and not this machine. Sky publishes bundles for:\n\
             \x20 linux-amd64  linux-arm64  darwin-amd64  darwin-arm64\n\
             \n\
             Build for one of those, or drop --embed and give the deployed app a DSN\n\
             (an external PostgreSQL costs nothing architecturally — the app only ever\n\
             consumes a DSN)."
        )),
    }
}

/// True when the target is this machine. Only then can an already-extracted
/// provision cache stand in for a download.
pub fn target_is_host(platform: &str) -> bool {
    db_provision::platform_tag() == Some(platform)
}

fn host_goos() -> &'static str {
    match std::env::consts::OS {
        "macos" => "darwin",
        other => other,
    }
}

fn host_goarch() -> &'static str {
    match std::env::consts::ARCH {
        "x86_64" => "amd64",
        "aarch64" => "arm64",
        other => other,
    }
}

/// The PostgreSQL version this build embeds: `[database] postgresVersion` when
/// the project pins one, else the version this `sky` was built against. The pin
/// is P3's, read through P3's reader, so a project cannot be developed against
/// one major and shipped carrying another.
pub fn version_for(project_dir: &Path) -> String {
    db_provision::pinned_version(project_dir)
        .unwrap_or_else(|| db_provision::DEFAULT_PG_VERSION.to_string())
}

fn cache_archive_path(home: &Path, version: &str, platform: &str) -> PathBuf {
    home.join(BUNDLE_CACHE_DIR)
        .join(db_provision::asset_name(version, platform))
}

/// Resolve a `.tar.gz` holding the PostgreSQL distribution for `platform`,
/// fetching or re-packing it if the cache does not already have one.
///
/// Order, most local first:
///
/// 1. the bundle cache — a previously fetched or re-packed archive;
/// 2. for the HOST platform only, `$SKY_HOME/postgres/<version>/` re-tarred;
/// 3. the release, fetched and checksum-verified.
pub fn resolve_bundle_archive(project_dir: &Path, platform: &str) -> Result<PathBuf, String> {
    resolve_bundle_archive_in(&db_provision::sky_home(), project_dir, platform, None)
}

/// [`resolve_bundle_archive`] with `$SKY_HOME` passed in rather than read.
pub fn resolve_bundle_archive_in(
    home: &Path,
    project_dir: &Path,
    platform: &str,
    base_url: Option<&str>,
) -> Result<PathBuf, String> {
    let version = version_for(project_dir);
    if version.contains('/') || version.contains("..") || version.trim().is_empty() {
        return Err(format!(
            "sky build --embed: {version:?} is not a version ([database] postgresVersion)"
        ));
    }
    let cached = cache_archive_path(home, &version, platform);
    if cached
        .metadata()
        .map(|m| m.is_file() && m.len() > 0)
        .unwrap_or(false)
    {
        return Ok(cached);
    }

    if target_is_host(platform) {
        let tree = home.join("postgres").join(&version);
        if db_provision::bundle_is_complete(&tree.join("bin")) {
            repack(&tree, &cached)?;
            return Ok(cached);
        }
    }

    db_provision::fetch_verified_archive(&version, platform, &cached, base_url).map_err(|e| {
        if target_is_host(platform) {
            e
        } else {
            format!(
                "{e}\n\
                 \n\
                 This is a cross-build: the target is {platform} and this machine is {}.\n\
                 Sky will not embed this machine's PostgreSQL into a {platform} binary —\n\
                 the result would fail to exec on the first start, on the deployed host.\n\
                 Copy the {platform} bundle across and place it at\n\
                 \x20 {}\n\
                 or build on a {platform} machine.",
                db_provision::platform_tag().unwrap_or("an unsupported platform"),
                cached.display(),
            )
        }
    })?;
    Ok(cached)
}

/// Re-pack an extracted provision cache into an archive `go:embed` can carry.
///
/// `tar` is shelled out to rather than written in Rust for the same reason P3
/// shells out to extract: it is the tool that already gets modes, symlinks and
/// long paths right, and a hand-rolled writer that got symlinks wrong would
/// produce a bundle whose `libpq.5.dylib` is a 0-byte regular file — a failure
/// that surfaces on the deployed host, not here.
///
/// The archive is written to a sibling and renamed, so an interrupted re-pack
/// never leaves a truncated `.tar.gz` in the cache for the next build to embed.
fn repack(tree: &Path, dest: &Path) -> Result<(), String> {
    let parent = dest
        .parent()
        .ok_or_else(|| format!("sky build --embed: {} has no parent", dest.display()))?;
    std::fs::create_dir_all(parent)
        .map_err(|e| format!("sky build --embed: cannot create {}: {e}", parent.display()))?;
    let tmp = parent.join(format!(
        ".{}.{}.part",
        dest.file_name()
            .and_then(|n| n.to_str())
            .unwrap_or("bundle"),
        std::process::id()
    ));
    let _ = std::fs::remove_file(&tmp);
    let (base, name) = (
        tree.parent()
            .ok_or_else(|| format!("sky build --embed: {} has no parent", tree.display()))?,
        tree.file_name()
            .ok_or_else(|| format!("sky build --embed: {} has no name", tree.display()))?,
    );
    println!(
        "sky build --embed: packing {} (no download needed)",
        tree.display()
    );
    let st = Command::new("tar")
        .arg("-czf")
        .arg(&tmp)
        .arg("-C")
        .arg(base)
        .arg(name)
        .status()
        .map_err(|e| format!("sky build --embed: could not run tar ({e})"))?;
    if !st.success() {
        let _ = std::fs::remove_file(&tmp);
        return Err(format!(
            "sky build --embed: could not pack {} (tar exit {})",
            tree.display(),
            st.code()
                .map(|c| c.to_string())
                .unwrap_or_else(|| "signal".into())
        ));
    }
    std::fs::rename(&tmp, dest).map_err(|e| {
        let _ = std::fs::remove_file(&tmp);
        format!("sky build --embed: cannot install {}: {e}", dest.display())
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    /// The name is a literal in three languages: the generated `//go:embed`
    /// directive, the `rt.EmbeddedPostgresBundleName` assignment beside it, and
    /// the runtime's own default. A `go:embed` path cannot be computed, so if
    /// these ever disagree the binary carries an archive under a name nothing
    /// opens — and the failure is at first start on the deployed host.
    #[test]
    fn the_embedded_name_is_the_one_the_generated_go_embed_writes() {
        assert_eq!(project::EMBEDDED_BUNDLE_FILENAME, "postgres-bundle.tar.gz");
    }

    #[test]
    fn goos_and_goarch_select_the_targets_bundle_not_the_hosts() {
        assert_eq!(
            platform_for_go_env("linux", "arm64").unwrap(),
            "linux-arm64"
        );
        assert_eq!(
            platform_for_go_env("darwin", "amd64").unwrap(),
            "darwin-amd64"
        );
        // Only one of the two set still cross-compiles — Go defaults the other.
        assert!(platform_for_go_env("linux", "")
            .unwrap()
            .starts_with("linux-"));
        assert!(platform_for_go_env("", "amd64")
            .unwrap()
            .ends_with("-amd64"));
    }

    /// The refusal is the point: embedding the host's binaries into a binary
    /// destined for another platform produces an app that fails to exec its own
    /// database on the deployed host, hours after the build that caused it.
    #[test]
    fn a_target_with_no_bundle_is_refused_and_the_refusal_names_a_way_out() {
        for (os, arch) in [
            ("plan9", "amd64"),
            ("windows", "amd64"),
            ("linux", "riscv64"),
        ] {
            let err = platform_for_go_env(os, arch).unwrap_err();
            assert!(err.contains(os), "{err}");
            assert!(err.contains(arch), "{err}");
            assert!(
                err.contains("linux-amd64") && err.contains("darwin-arm64"),
                "the refusal must name what IS published: {err}"
            );
        }
    }

    #[test]
    fn an_unset_environment_targets_this_machine() {
        let p = platform_for_go_env("", "").unwrap();
        assert!(target_is_host(p), "{p} should be the host platform");
    }

    #[test]
    fn the_pin_decides_the_version_and_the_default_stands_in() {
        let dir = std::env::temp_dir().join(format!("sky-p5b-ver-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        std::fs::write(dir.join("sky.toml"), "[database]\nembedded = true\n").unwrap();
        assert_eq!(version_for(&dir), db_provision::DEFAULT_PG_VERSION);
        std::fs::write(
            dir.join("sky.toml"),
            "[database]\npostgresVersion = \"17.4\"\n",
        )
        .unwrap();
        assert_eq!(version_for(&dir), "17.4");
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn a_cached_archive_is_reused_without_touching_the_network() {
        let home = std::env::temp_dir().join(format!("sky-p5b-home-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&home);
        let project = home.join("proj");
        std::fs::create_dir_all(&project).unwrap();
        std::fs::write(
            project.join("sky.toml"),
            "[database]\npostgresVersion = \"18.6\"\n",
        )
        .unwrap();

        let cached = cache_archive_path(&home, "18.6", "linux-amd64");
        std::fs::create_dir_all(cached.parent().unwrap()).unwrap();
        std::fs::write(&cached, b"not really a tarball, but non-empty").unwrap();

        // No release server is running, and no `postgres/18.6/bin` exists: if
        // this resolved anywhere but the cache it would have to fail.
        let got = resolve_bundle_archive_in(
            &home,
            &project,
            "linux-amd64",
            Some("file:///nonexistent-sky-p5b"),
        )
        .unwrap();
        assert_eq!(got, cached);

        // An EMPTY cache file is not a bundle. A zero-length `.tar.gz` is what
        // an interrupted write leaves, and embedding it would put a binary in
        // production whose only failure is at first start.
        std::fs::write(&cached, b"").unwrap();
        assert!(resolve_bundle_archive_in(
            &home,
            &project,
            "linux-amd64",
            Some("file:///nonexistent-sky-p5b")
        )
        .is_err());

        let _ = std::fs::remove_dir_all(&home);
    }

    /// The re-pack path: a provisioned cache is turned into an archive without
    /// a download, and the archive round-trips through `tar` with the
    /// executable bit intact — which is the whole reason the bundle stays a tar
    /// inside the embedded FS.
    #[test]
    fn a_provisioned_cache_is_repacked_rather_than_downloaded_again() {
        let home = std::env::temp_dir().join(format!("sky-p5b-repack-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&home);
        let bin = home.join("postgres").join("18.6").join("bin");
        std::fs::create_dir_all(&bin).unwrap();
        for b in ["initdb", "pg_ctl", "postgres"] {
            let p = bin.join(b);
            std::fs::write(&p, "#!/bin/sh\nexit 0\n").unwrap();
            #[cfg(unix)]
            {
                use std::os::unix::fs::PermissionsExt;
                std::fs::set_permissions(&p, std::fs::Permissions::from_mode(0o755)).unwrap();
            }
        }
        let project = home.join("proj");
        std::fs::create_dir_all(&project).unwrap();
        std::fs::write(
            project.join("sky.toml"),
            "[database]\npostgresVersion = \"18.6\"\n",
        )
        .unwrap();

        let host = db_provision::platform_tag().expect("this platform must be supported");
        let got =
            resolve_bundle_archive_in(&home, &project, host, Some("file:///nonexistent-sky-p5b"))
                .unwrap();
        assert_eq!(got, cache_archive_path(&home, "18.6", host));
        assert!(got.metadata().unwrap().len() > 0);

        // Unpack it again and check the mode survived.
        let back = home.join("back");
        std::fs::create_dir_all(&back).unwrap();
        let st = Command::new("tar")
            .arg("-xzf")
            .arg(&got)
            .arg("-C")
            .arg(&back)
            .arg("--strip-components=1")
            .status()
            .unwrap();
        assert!(st.success());
        assert!(
            db_provision::bundle_is_complete(&back.join("bin")),
            "the re-packed archive lost the executable bit or a binary"
        );

        // Second call is the cache hit — same path, no second tar.
        let again =
            resolve_bundle_archive_in(&home, &project, host, Some("file:///nonexistent-sky-p5b"))
                .unwrap();
        assert_eq!(again, got);

        let _ = std::fs::remove_dir_all(&home);
    }
}

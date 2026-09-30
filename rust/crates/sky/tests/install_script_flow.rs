//! `install.sh` and the `Dockerfile` install a release binary only after it
//! matches the release's published `checksums.txt` (DL, v0.27.0).
//!
//! `install.sh` is run for real against a local `file://` release fixture (no
//! network), through `SKY_INSTALL_BASE_URL`. The `Dockerfile` cannot be built
//! here (no docker in the gate), so its check is asserted on the text: the
//! manifest is fetched and `sha256sum -c` runs before the archive is unpacked.

use std::path::{Path, PathBuf};
use std::process::Command;

fn repo_root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    while !dir.join("install.sh").is_file() {
        assert!(dir.pop(), "no repo root above the sky crate");
    }
    dir
}

fn scratch(tag: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "sky-install-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(&dir).unwrap();
    dir
}

/// The artifact name `install.sh` derives from `uname`.
fn artifact() -> String {
    let os = match std::env::consts::OS {
        "macos" => "darwin",
        other => other,
    };
    let arch = match std::env::consts::ARCH {
        "x86_64" => "x64",
        "aarch64" => "arm64",
        other => other,
    };
    format!("sky-{os}-{arch}")
}

fn sha256(path: &Path) -> String {
    for (tool, args) in [("sha256sum", vec![]), ("shasum", vec!["-a", "256"])] {
        if let Ok(out) = Command::new(tool).args(&args).arg(path).output() {
            if out.status.success() {
                let s = String::from_utf8_lossy(&out.stdout);
                return s.split_whitespace().next().unwrap().to_string();
            }
        }
    }
    panic!("neither sha256sum nor shasum is installed");
}

/// A release fixture `<base>/v9.9.9/{<artifact>.tar.gz, checksums.txt}` whose
/// `sky` prints `fixture-sky`. `digest` overrides the listed checksum.
fn release(base: &Path, digest: Option<&str>) {
    let rel = base.join("v9.9.9");
    let stage = base.join("stage");
    std::fs::create_dir_all(&rel).unwrap();
    std::fs::create_dir_all(&stage).unwrap();
    let a = artifact();
    std::fs::write(stage.join(&a), "#!/bin/sh\necho fixture-sky\n").unwrap();
    let archive = rel.join(format!("{a}.tar.gz"));
    let ok = Command::new("tar")
        .arg("czf")
        .arg(&archive)
        .arg("-C")
        .arg(&stage)
        .arg(&a)
        .status()
        .unwrap()
        .success();
    assert!(ok, "tar");
    let real = sha256(&archive);
    std::fs::write(
        rel.join("checksums.txt"),
        format!("{}  {a}.tar.gz\n", digest.unwrap_or(&real)),
    )
    .unwrap();
}

fn install(base: &Path, dir: &Path) -> (bool, String) {
    let out = Command::new("sh")
        .arg(repo_root().join("install.sh"))
        .args(["--version", "9.9.9", "--dir"])
        .arg(dir)
        .env("SKY_INSTALL_BASE_URL", format!("file://{}", base.display()))
        .env_remove("SKY_VERSION")
        .env_remove("SKY_INSTALL_DIR")
        .env_remove("INSTALL_DIR")
        .stdin(std::process::Stdio::null())
        .output()
        .expect("run install.sh");
    let mut s = String::from_utf8_lossy(&out.stdout).into_owned();
    s.push_str(&String::from_utf8_lossy(&out.stderr));
    (out.status.success(), s)
}

#[test]
fn install_sh_installs_a_verified_release() {
    let base = scratch("ok");
    release(&base, None);
    let bin = base.join("bin");
    std::fs::create_dir_all(&bin).unwrap();
    let (ok, log) = install(&base, &bin);
    assert!(ok, "{log}");
    assert!(bin.join("sky").is_file(), "{log}");
    let _ = std::fs::remove_dir_all(&base);
}

#[test]
fn install_sh_refuses_a_checksum_mismatch() {
    let base = scratch("bad");
    release(&base, Some(&"0".repeat(64)));
    let bin = base.join("bin");
    std::fs::create_dir_all(&bin).unwrap();
    let (ok, log) = install(&base, &bin);
    assert!(!ok, "a mismatched archive must be refused:\n{log}");
    assert!(
        log.contains("does not match its published checksum")
            && log.contains("docs/migration/v0.27.md#upgrades-and-installs-verify-checksums"),
        "{log}"
    );
    assert!(!bin.join("sky").exists(), "nothing is installed:\n{log}");
    let _ = std::fs::remove_dir_all(&base);
}

#[test]
fn install_sh_refuses_a_release_without_checksums() {
    let base = scratch("none");
    release(&base, None);
    std::fs::remove_file(base.join("v9.9.9/checksums.txt")).unwrap();
    let bin = base.join("bin");
    std::fs::create_dir_all(&bin).unwrap();
    let (ok, log) = install(&base, &bin);
    assert!(!ok, "no manifest must be refused:\n{log}");
    assert!(!bin.join("sky").exists(), "nothing is installed:\n{log}");
    let _ = std::fs::remove_dir_all(&base);
}

#[test]
fn the_dockerfile_checks_the_archive_before_unpacking_it() {
    let text = std::fs::read_to_string(repo_root().join("Dockerfile")).unwrap();
    let sums = text
        .find("$BASE/checksums.txt")
        .expect("the Dockerfile fetches checksums.txt");
    let check = text
        .find("sha256sum -c want.sha256")
        .expect("the Dockerfile runs sha256sum -c");
    let unpack = text
        .find("tar xzf")
        .expect("the Dockerfile unpacks the archive");
    assert!(
        sums < check && check < unpack,
        "manifest, then sha256sum -c, then tar"
    );
}

//! The tag pre-push hook that `scripts/install-git-hooks.sh` generates must
//! FAIL CLOSED.
//!
//! It refuses a `v*` tag push unless `scripts/preflight-tag.sh` stamped
//! `last-preflight-pass` within the last 30 minutes. The hook used to read the
//! stamp's mtime with `stat -f %m F || stat -c %Y F`. With GNU coreutils first
//! on PATH (common on macOS dev machines) `stat -f` means `--file-system`: it
//! prints a multi-line block and exits 0, so the block became the "mtime", the
//! arithmetic and the `[ -gt ]` test both errored, and the hook printed
//! "Preflight stamp fresh ( s old); allowing tag push." These tests generate
//! the hook into a throwaway repository and drive it with stand-in `stat` /
//! `date` tools on PATH.

use std::io::Write;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::time::{SystemTime, UNIX_EPOCH};

fn repo() -> PathBuf {
    PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../.."))
}

fn shell() -> &'static str {
    // `/bin/bash` is 3.2 on stock macOS, the floor the repo's scripts hold to.
    if Path::new("/bin/bash").exists() {
        "/bin/bash"
    } else {
        "bash"
    }
}

fn real_date() -> &'static str {
    if Path::new("/bin/date").exists() {
        "/bin/date"
    } else {
        "/usr/bin/date"
    }
}

fn now_secs() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap()
        .as_secs()
}

fn write_exe(path: &Path, body: &str) {
    std::fs::write(path, body).unwrap();
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o755)).unwrap();
    }
}

/// A throwaway git repository with the hook installed by the real installer.
struct World {
    root: PathBuf,
    repo: PathBuf,
    fakebin: PathBuf,
}

impl World {
    fn new(tag: &str) -> World {
        let root = std::env::temp_dir().join(format!("sky-pre-push-{tag}-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&root);
        let repo_dir = root.join("repo");
        let fakebin = root.join("fakebin");
        std::fs::create_dir_all(repo_dir.join("scripts")).unwrap();
        std::fs::create_dir_all(&fakebin).unwrap();
        let ok = Command::new("git")
            .args(["init", "-q"])
            .current_dir(&repo_dir)
            .status()
            .expect("git is required for this test")
            .success();
        assert!(ok, "git init failed");
        std::fs::copy(
            repo().join("scripts/install-git-hooks.sh"),
            repo_dir.join("scripts/install-git-hooks.sh"),
        )
        .unwrap();
        let out = Command::new(shell())
            .arg(repo_dir.join("scripts/install-git-hooks.sh"))
            // Run from OUTSIDE the repo: the installer must still put the
            // hook in the repo's own hooks dir, not under "$PWD/.git".
            .current_dir(&root)
            .output()
            .unwrap();
        assert!(
            out.status.success(),
            "installer failed: {}",
            String::from_utf8_lossy(&out.stderr)
        );
        assert!(repo_dir.join(".git/hooks/pre-push").exists());
        World {
            root,
            repo: repo_dir,
            fakebin,
        }
    }

    fn stamp(&self) -> PathBuf {
        self.repo.join(".git/last-preflight-pass")
    }

    fn touch_stamp(&self) {
        std::fs::write(self.stamp(), "").unwrap();
    }

    /// Run the hook as `git push` would for `refs/tags/v9.9.9`. Returns the
    /// exit success and the combined output.
    fn push(&self, remote_ref: &str) -> (bool, String) {
        let path = format!(
            "{}:{}",
            self.fakebin.display(),
            std::env::var("PATH").unwrap_or_default()
        );
        let mut child = Command::new(shell())
            .arg(self.repo.join(".git/hooks/pre-push"))
            .args(["origin", "https://example.invalid/repo.git"])
            .current_dir(&self.repo)
            .env("PATH", path)
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .spawn()
            .unwrap();
        let zero = "0".repeat(40);
        writeln!(
            child.stdin.take().unwrap(),
            "{remote_ref} {zero} {remote_ref} {zero}"
        )
        .unwrap();
        let out = child.wait_with_output().unwrap();
        let text = format!(
            "{}{}",
            String::from_utf8_lossy(&out.stdout),
            String::from_utf8_lossy(&out.stderr)
        );
        (out.status.success(), text)
    }
}

impl Drop for World {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.root);
    }
}

/// A `stat` that answers every form with a GNU-style `--file-system` block and
/// exit 0 — what `stat -f %m` prints with GNU coreutils first on PATH — and a
/// `date -r` that prints a human date. No candidate yields an integer, so the
/// hook must refuse. The pre-fix hook allowed the push here.
#[test]
fn non_integer_mtime_refuses_the_tag_push() {
    let w = World::new("junk");
    w.touch_stamp();
    write_exe(
        &w.fakebin.join("stat"),
        "#!/bin/sh\n\
         echo '  File: \"/tmp/x\"'\n\
         echo '    ID: 1000000000000000 Namelen: 255     Type: apfs'\n\
         echo 'Block size: 4096       Fundamental block size: 4096'\n\
         exit 0\n",
    );
    write_exe(
        &w.fakebin.join("date"),
        &format!(
            "#!/bin/sh\n\
             if [ \"$1\" = -r ]; then echo 'Sat Oct  4 12:00:00 BST 2026'; exit 0; fi\n\
             exec {} \"$@\"\n",
            real_date()
        ),
    );
    let (ok, out) = w.push("refs/tags/v9.9.9");
    assert!(
        !ok,
        "hook must refuse on a non-integer mtime; output:\n{out}"
    );
    assert!(out.contains("Refusing to push tag"), "output:\n{out}");
    assert!(!out.contains("allowing tag push"), "output:\n{out}");
}

/// GNU coreutils first on PATH: `stat -c %Y` answers correctly, `stat -f`
/// prints the file-system block. A fresh stamp must be read through the GNU
/// form and allowed, with an integer age in the message.
#[test]
fn gnu_stat_first_on_path_reads_a_fresh_stamp() {
    let w = World::new("gnu");
    w.touch_stamp();
    write_exe(
        &w.fakebin.join("stat"),
        &format!(
            "#!/bin/sh\n\
             if [ \"$1\" = -c ]; then echo {}; exit 0; fi\n\
             echo '  File: \"/tmp/x\"'\n\
             echo '    ID: 1000000000000000 Namelen: 255     Type: apfs'\n\
             exit 0\n",
            now_secs()
        ),
    );
    let (ok, out) = w.push("refs/tags/v9.9.9");
    assert!(ok, "fresh stamp must be allowed; output:\n{out}");
    assert!(out.contains("allowing tag push"), "output:\n{out}");
    assert!(!out.contains("( s old)"), "age must be an integer:\n{out}");
}

/// A stale stamp (older than 30 minutes) refuses, and so does a stamp whose
/// mtime is in the future.
#[test]
fn stale_or_future_stamp_refuses() {
    let w = World::new("stale");
    w.touch_stamp();
    for mtime in [now_secs() - 3600, now_secs() + 3600] {
        write_exe(
            &w.fakebin.join("stat"),
            &format!("#!/bin/sh\nif [ \"$1\" = -c ]; then echo {mtime}; exit 0; fi\nexit 1\n"),
        );
        let (ok, out) = w.push("refs/tags/v9.9.9");
        assert!(!ok, "mtime {mtime} must refuse; output:\n{out}");
        assert!(out.contains("Refusing to push tag"), "output:\n{out}");
    }
}

/// No stamp at all refuses; a branch push is never gated.
#[test]
fn missing_stamp_refuses_and_branch_push_passes() {
    let w = World::new("missing");
    let (ok, out) = w.push("refs/tags/v9.9.9");
    assert!(!ok, "missing stamp must refuse; output:\n{out}");
    let (ok, out) = w.push("refs/heads/feature");
    assert!(ok, "a branch push is not gated; output:\n{out}");
}

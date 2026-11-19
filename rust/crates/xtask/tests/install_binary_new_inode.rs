//! `install_binary` in `scripts/lib/cargo-target.sh` must write a NEW inode.
//!
//! `scripts/build.sh` installs `sky-out/sky` through it. It used `cp -f`, which
//! rewrites the destination in place. On macOS the kernel caches the code
//! signature of a binary that has run, by inode: the rewritten binary no longer
//! matched it and was killed (SIGKILL, exit 137) the moment it started, so
//! `build.sh` died at its own `sky-out/sky --version` line. The fix removes the
//! old file first, so the copy lands on a new inode.
//!
//! The test keeps a hard link to the old file. The old inode then stays alive,
//! so a fresh copy cannot be given the same inode number, and an in-place
//! rewrite shows through the link. Both directions are asserted.
#![cfg(unix)]

use std::os::unix::fs::MetadataExt;
use std::path::{Path, PathBuf};
use std::process::Command;

fn repo() -> PathBuf {
    PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../.."))
}

fn shell() -> &'static str {
    // `/bin/bash` is 3.2 on stock macOS, the floor the scripts must parse on.
    if Path::new("/bin/bash").exists() {
        "/bin/bash"
    } else {
        "bash"
    }
}

#[test]
fn install_binary_writes_a_new_inode_and_leaves_the_old_one_alone() {
    let root = std::env::temp_dir().join(format!("sky-install-binary-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&root);
    std::fs::create_dir_all(root.join("out")).unwrap();
    let src = root.join("new-sky");
    let dest = root.join("out/sky");
    let held = root.join("held-old-sky");
    std::fs::write(&src, "#!/bin/sh\necho new\n").unwrap();
    std::fs::write(&dest, "#!/bin/sh\necho old\n").unwrap();
    std::fs::hard_link(&dest, &held).unwrap();
    let old_ino = std::fs::metadata(&dest).unwrap().ino();

    let lib = repo().join("scripts/lib/cargo-target.sh");
    let out = Command::new(shell())
        .arg("-c")
        .arg("set -e; source \"$1\"; install_binary \"$2\" \"$3\"")
        .arg("install-binary-test")
        .arg(&lib)
        .arg(&src)
        .arg(&dest)
        .output()
        .expect("bash runs");
    assert!(
        out.status.success(),
        "install_binary failed:\n{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );

    let meta = std::fs::metadata(&dest).unwrap();
    let new_ino = meta.ino();
    let dest_text = std::fs::read_to_string(&dest).unwrap();
    let held_text = std::fs::read_to_string(&held).unwrap();
    let _ = std::fs::remove_dir_all(&root);
    assert_eq!(
        dest_text, "#!/bin/sh\necho new\n",
        "the destination has the new content"
    );
    assert_ne!(
        new_ino, old_ino,
        "install_binary must copy to a NEW inode: rewriting the old one in place \
         (`cp -f`) gets the new binary killed on macOS for a stale code signature"
    );
    assert_eq!(
        held_text, "#!/bin/sh\necho old\n",
        "the old inode must not be rewritten"
    );
    assert!(
        meta.mode() & 0o111 != 0,
        "the installed binary is executable"
    );
}

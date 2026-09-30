//! Private scratch directories and files.
//!
//! `sky upgrade` (F-9) and the App Store Connect key copy (F-11) both need a
//! scratch place that no other local user can pre-create, read or swap. A
//! `temp_dir()/<name>-<pid>` made with `create_dir_all` accepts a directory an
//! attacker created first (a shared `/tmp` on Linux) and is world-readable
//! until a later `chmod`. Here the directory gets a random name, is created
//! with mode 0700 in one call, and the call FAILS if the path exists. A file is
//! created with mode 0600 and `create_new`, never widened then narrowed.

use std::path::{Path, PathBuf};

/// Create `path` as a directory only its owner can use (0700 on unix).
/// Fails if anything already exists at `path`, including a directory another
/// user made: that is exactly the case this refuses.
pub fn create_private_dir(path: &Path) -> std::io::Result<()> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::DirBuilderExt;
        std::fs::DirBuilder::new().mode(0o700).create(path)
    }
    #[cfg(not(unix))]
    {
        std::fs::DirBuilder::new().create(path)
    }
}

/// A fresh private directory `<parent>/<prefix><random>` (see
/// [`create_private_dir`]). The name carries 64 random bits, so it cannot be
/// predicted and pre-created.
pub fn private_dir_in(parent: &Path, prefix: &str) -> Result<PathBuf, String> {
    let dir = parent.join(format!("{prefix}{}", random_hex()));
    create_private_dir(&dir).map_err(|e| {
        format!(
            "could not create a private directory {}: {e}",
            dir.display()
        )
    })?;
    Ok(dir)
}

/// Write `bytes` to a NEW file at `path` that only its owner can read (0600 on
/// unix). Fails if the path exists.
pub fn write_private_file(path: &Path, bytes: &[u8]) -> std::io::Result<()> {
    use std::io::Write;
    let mut opts = std::fs::OpenOptions::new();
    opts.write(true).create_new(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        opts.mode(0o600);
    }
    let mut f = opts.open(path)?;
    f.write_all(bytes)?;
    f.sync_all()
}

fn random_hex() -> String {
    #[cfg(unix)]
    let bytes = crate::pg_wire::random_bytes(8);
    #[cfg(not(unix))]
    let bytes = {
        let n = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map(|d| d.as_nanos())
            .unwrap_or(0)
            ^ u128::from(std::process::id());
        n.to_le_bytes()[..8].to_vec()
    };
    bytes.iter().map(|b| format!("{b:02x}")).collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn scratch(tag: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!(
            "sky-privfs-{tag}-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        std::fs::create_dir_all(&dir).unwrap();
        dir
    }

    #[test]
    fn a_private_dir_refuses_an_existing_path_and_is_0700() {
        let base = scratch("dir");
        let taken = base.join("taken");
        std::fs::create_dir(&taken).unwrap();
        assert!(
            create_private_dir(&taken).is_err(),
            "a pre-created directory must be refused, not reused"
        );
        let fresh = private_dir_in(&base, "w-").unwrap();
        assert_ne!(fresh, private_dir_in(&base, "w-").unwrap());
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mode = std::fs::metadata(&fresh).unwrap().permissions().mode() & 0o777;
            assert_eq!(mode, 0o700, "created 0700 from the start");
        }
        let _ = std::fs::remove_dir_all(&base);
    }

    #[test]
    fn a_private_file_is_0600_and_never_overwrites() {
        let base = scratch("file");
        let f = base.join("key.p8");
        write_private_file(&f, b"secret").unwrap();
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mode = std::fs::metadata(&f).unwrap().permissions().mode() & 0o777;
            assert_eq!(mode, 0o600);
        }
        assert!(write_private_file(&f, b"other").is_err());
        let _ = std::fs::remove_dir_all(&base);
    }
}

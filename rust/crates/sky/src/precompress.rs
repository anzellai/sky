//! Precompressed web assets (`<file>.gz`, `<file>.br`) with a content-addressed
//! cache.
//!
//! `sky build --target web` (and every Sky.Spa build, whose frontend leg is one)
//! writes `main.<hash>.wasm.gz` and `.br` beside the hashed wasm so a static host
//! (`file_server { precompressed br gzip }`) can serve them. brotli at quality 11
//! is the slow part of the whole build: measured on a 9.5 MB wasm it takes longer
//! than the frontend's parse, typecheck, lowering and `go build` together. The
//! compressed bytes are a pure function of the input bytes and the tool
//! invocation, so they are cached under the input's SHA-256: a rebuild whose
//! wasm did not change copies the stored result instead of compressing again.
//!
//! The cache lives in `~/.cache/sky/precompress` (or `$XDG_CACHE_HOME/sky/…`),
//! outside the project, because a Std.App `--target web:app` build restages its
//! whole generated project on every build. It keeps the newest
//! [`CACHE_KEEP`] entries. Every cache failure falls back to running the tool,
//! so the cache can never fail a build.

use std::path::{Path, PathBuf};
use std::process::Command;

/// How many cached results to keep (newest by mtime). A multi-MB wasm is
/// ~2 MB compressed, so this bounds the cache to tens of MB.
pub const CACHE_KEEP: usize = 32;

/// One compressor invocation. `args` are passed before the file path and must
/// keep the input (`-k`) and overwrite an existing output (`-f`).
pub struct Tool {
    pub program: &'static str,
    pub args: &'static [&'static str],
    /// Output extension: the tool writes `<file>.<ext>`.
    pub ext: &'static str,
}

pub const GZIP: Tool = Tool {
    program: "gzip",
    args: &["-9", "-k", "-f"],
    ext: "gz",
};

pub const BROTLI: Tool = Tool {
    program: "brotli",
    args: &["-q", "11", "-k", "-f"],
    ext: "br",
};

/// What [`compress`] did.
#[derive(Debug, PartialEq, Eq)]
pub enum Outcome {
    /// Copied from the cache; the tool did not run.
    CacheHit,
    /// The tool ran and succeeded.
    Compressed,
    /// The tool is missing or failed; no output was written.
    Failed,
}

/// The default cache dir, or `None` when it cannot be created.
pub fn default_cache_dir() -> Option<PathBuf> {
    let d = crate::bundled::cache_root().join("precompress");
    std::fs::create_dir_all(&d).ok().map(|_| d)
}

/// The cache key for `bytes` under `tool`: the content hash plus everything
/// about the invocation that changes the output.
fn cache_key(bytes: &[u8], tool: &Tool) -> String {
    let content = crate::db_provision::sha256_hex(bytes);
    let invocation = crate::db_provision::sha256_hex(
        format!("{} {}", tool.program, tool.args.join(" ")).as_bytes(),
    );
    format!("{}-{}.{}", &content[..32], &invocation[..8], tool.ext)
}

/// Write `<file>.<ext>` for `tool`, from the cache when the same bytes were
/// compressed before, else by running the tool (and then storing the result).
pub fn compress(file: &Path, tool: &Tool, cache: Option<&Path>) -> Outcome {
    let mut out_name = file.as_os_str().to_owned();
    out_name.push(".");
    out_name.push(tool.ext);
    let out = PathBuf::from(out_name);
    let cached = cache.and_then(|dir| {
        let bytes = std::fs::read(file).ok()?;
        Some(dir.join(cache_key(&bytes, tool)))
    });
    if let Some(c) = &cached {
        let hit = std::fs::metadata(c).map(|m| m.len() > 0).unwrap_or(false);
        if hit && std::fs::copy(c, &out).is_ok() {
            // Touch so pruning keeps what is in use.
            let _ = std::fs::File::options()
                .append(true)
                .open(c)
                .and_then(|f| f.set_modified(std::time::SystemTime::now()));
            return Outcome::CacheHit;
        }
    }
    let ok = Command::new(tool.program)
        .args(tool.args)
        .arg(file)
        .status()
        .map(|s| s.success())
        .unwrap_or(false);
    if !ok {
        return Outcome::Failed;
    }
    if let (Some(c), Some(dir)) = (&cached, cache) {
        // Store via a temp name + rename so a concurrent build never reads a
        // half-written entry.
        let tmp = dir.join(format!(".tmp-{}-{}", std::process::id(), tool.ext));
        if std::fs::copy(&out, &tmp).is_ok() && std::fs::rename(&tmp, c).is_err() {
            let _ = std::fs::remove_file(&tmp);
        }
        prune(dir, CACHE_KEEP);
    }
    Outcome::Compressed
}

/// Keep only the newest `keep` entries in `dir`. Best-effort.
fn prune(dir: &Path, keep: usize) {
    let Ok(rd) = std::fs::read_dir(dir) else {
        return;
    };
    let mut entries: Vec<(std::time::SystemTime, PathBuf)> = rd
        .flatten()
        .filter(|e| !e.file_name().to_string_lossy().starts_with('.'))
        .filter_map(|e| {
            let m = e.metadata().ok()?;
            if !m.is_file() {
                return None;
            }
            Some((m.modified().ok()?, e.path()))
        })
        .collect();
    if entries.len() <= keep {
        return;
    }
    entries.sort_by(|a, b| b.0.cmp(&a.0));
    for (_, p) in entries.into_iter().skip(keep) {
        let _ = std::fs::remove_file(p);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A fake compressor: a shell script that copies `<file>` to `<file>.fk`
    /// and appends a line to a run log, so a test can count how often it ran.
    fn fake_tool(dir: &Path) -> (Tool, PathBuf) {
        use std::os::unix::fs::PermissionsExt;
        let log = dir.join("runs.log");
        let script = dir.join("fakezip");
        std::fs::write(
            &script,
            format!(
                "#!/bin/sh\nfor a in \"$@\"; do f=\"$a\"; done\necho run >> '{}'\ncp \"$f\" \"$f.fk\"\n",
                log.display()
            ),
        )
        .unwrap();
        std::fs::set_permissions(&script, std::fs::Permissions::from_mode(0o755)).unwrap();
        let program: &'static str =
            Box::leak(script.to_string_lossy().into_owned().into_boxed_str());
        (
            Tool {
                program,
                args: &["-k", "-f"],
                ext: "fk",
            },
            log,
        )
    }

    fn runs(log: &Path) -> usize {
        std::fs::read_to_string(log)
            .map(|s| s.lines().count())
            .unwrap_or(0)
    }

    fn scratch(name: &str) -> PathBuf {
        let d = std::env::temp_dir().join(format!("sky-precompress-{name}-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&d);
        std::fs::create_dir_all(d.join("cache")).unwrap();
        d
    }

    #[test]
    fn unchanged_bytes_are_served_from_the_cache_without_running_the_tool() {
        let d = scratch("hit");
        let (tool, log) = fake_tool(&d);
        let cache = d.join("cache");
        let f = d.join("main.abc.wasm");
        std::fs::write(&f, b"wasm bytes v1").unwrap();

        assert_eq!(compress(&f, &tool, Some(&cache)), Outcome::Compressed);
        assert_eq!(runs(&log), 1);
        let first = std::fs::read(d.join("main.abc.wasm.fk")).unwrap();

        // A rebuild writes the same bytes again (the staged tree is recreated).
        std::fs::remove_file(d.join("main.abc.wasm.fk")).unwrap();
        assert_eq!(compress(&f, &tool, Some(&cache)), Outcome::CacheHit);
        assert_eq!(runs(&log), 1, "a cache hit must not run the compressor");
        assert_eq!(std::fs::read(d.join("main.abc.wasm.fk")).unwrap(), first);

        // Changed bytes miss the cache and compress again.
        std::fs::write(&f, b"wasm bytes v2").unwrap();
        assert_eq!(compress(&f, &tool, Some(&cache)), Outcome::Compressed);
        assert_eq!(runs(&log), 2);
        assert_eq!(
            std::fs::read(d.join("main.abc.wasm.fk")).unwrap(),
            b"wasm bytes v2"
        );
        let _ = std::fs::remove_dir_all(&d);
    }

    #[test]
    fn no_cache_dir_always_runs_the_tool() {
        let d = scratch("nocache");
        let (tool, log) = fake_tool(&d);
        let f = d.join("a.wasm");
        std::fs::write(&f, b"x").unwrap();
        assert_eq!(compress(&f, &tool, None), Outcome::Compressed);
        assert_eq!(compress(&f, &tool, None), Outcome::Compressed);
        assert_eq!(runs(&log), 2);
        let _ = std::fs::remove_dir_all(&d);
    }

    #[test]
    fn a_missing_tool_fails_without_writing_output() {
        let d = scratch("missing");
        let tool = Tool {
            program: "sky-no-such-compressor",
            args: &[],
            ext: "zz",
        };
        let f = d.join("a.wasm");
        std::fs::write(&f, b"x").unwrap();
        assert_eq!(compress(&f, &tool, Some(&d.join("cache"))), Outcome::Failed);
        assert!(!d.join("a.wasm.zz").exists());
        let _ = std::fs::remove_dir_all(&d);
    }

    #[test]
    fn prune_keeps_the_newest_entries() {
        let d = scratch("prune");
        let cache = d.join("cache");
        for i in 0..5 {
            let p = cache.join(format!("e{i}"));
            std::fs::write(&p, b"x").unwrap();
            let t = std::time::SystemTime::UNIX_EPOCH + std::time::Duration::from_secs(1000 + i);
            std::fs::File::options()
                .append(true)
                .open(&p)
                .unwrap()
                .set_modified(t)
                .unwrap();
        }
        prune(&cache, 2);
        let mut left: Vec<String> = std::fs::read_dir(&cache)
            .unwrap()
            .flatten()
            .map(|e| e.file_name().to_string_lossy().into_owned())
            .collect();
        left.sort();
        assert_eq!(left, vec!["e3", "e4"]);
        let _ = std::fs::remove_dir_all(&d);
    }
}

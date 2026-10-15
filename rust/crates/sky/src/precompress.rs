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
//! Several assets are compressed at once (each in its own thread), so every
//! store goes through its own temporary name, and every hit is checked by
//! decompressing the stored entry and comparing it with the input: an entry
//! that does not decompress to exactly the input is removed and the tool runs.
//! (Before v0.27.9 the temporary name was `.tmp-<pid>-<ext>`, one per process
//! and tool, so two brotli threads of one build could store each other's
//! output under the wrong key, and every later build would have served it.)
//!
//! The cache lives in `~/.cache/sky/precompress` (or `$XDG_CACHE_HOME/sky/…`),
//! outside the project, because a Std.App `--target web:app` build restages its
//! whole generated project on every build. It keeps the newest
//! [`CACHE_KEEP`] entries. Every cache failure falls back to running the tool,
//! so the cache can never fail a build.

use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::sync::atomic::{AtomicU64, Ordering};

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
    /// Arguments that decompress stdin to stdout, to check a cached entry.
    pub decompress: &'static [&'static str],
}

pub const GZIP: Tool = Tool {
    program: "gzip",
    args: &["-9", "-k", "-f"],
    ext: "gz",
    decompress: &["-d", "-c"],
};

pub const BROTLI: Tool = Tool {
    program: "brotli",
    args: &["-q", "11", "-k", "-f"],
    ext: "br",
    decompress: &["-d", "-c"],
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
    // An earlier build's output may be read-only: a compressor keeps its
    // input's permissions, and Go's `wasm_exec.js` is read-only when GOROOT is
    // (a Nix store). Neither the cache copy nor the tool can open it for
    // writing, so remove it first; the directory is ours and writable.
    let _ = std::fs::remove_file(&out);
    let input = cache.and_then(|_| std::fs::read(file).ok());
    let cached = cache
        .zip(input.as_ref())
        .map(|(dir, bytes)| dir.join(cache_key(bytes, tool)));
    if let (Some(c), Some(bytes)) = (&cached, &input) {
        let hit = std::fs::metadata(c).map(|m| m.len() > 0).unwrap_or(false);
        if hit && !decompresses_to(c, bytes, tool) {
            // Not this input's result (a store raced by another writer, a
            // torn or foreign file): drop it and compress.
            let _ = std::fs::remove_file(c);
        } else if hit && std::fs::copy(c, &out).is_ok() {
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
        // half-written entry. The name is unique to this call (process, and a
        // counter across this process's threads), so two compressions running
        // at once never share it.
        let tmp = store_tmp_name(dir, tool);
        if std::fs::copy(&out, &tmp).is_ok() && std::fs::rename(&tmp, c).is_err() {
            let _ = std::fs::remove_file(&tmp);
        }
        prune(dir, CACHE_KEEP);
    }
    Outcome::Compressed
}

/// Distinguishes the temporary store names of one process's calls.
static TMP_SEQ: AtomicU64 = AtomicU64::new(0);

/// A temporary name in `dir` for one store, unique to this call: the process
/// and a counter across its threads.
fn store_tmp_name(dir: &Path, tool: &Tool) -> PathBuf {
    dir.join(format!(
        ".tmp-{}-{}-{}",
        std::process::id(),
        TMP_SEQ.fetch_add(1, Ordering::Relaxed),
        tool.ext
    ))
}

/// Whether the cached `entry` decompresses (with `tool`) to exactly `input`.
/// Any failure to check is a "no": the caller compresses again.
fn decompresses_to(entry: &Path, input: &[u8], tool: &Tool) -> bool {
    let Ok(stored) = std::fs::File::open(entry) else {
        return false;
    };
    let Ok(child) = Command::new(tool.program)
        .args(tool.decompress)
        .stdin(stored)
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
    else {
        return false;
    };
    match child.wait_with_output() {
        Ok(o) => o.status.success() && o.stdout == input,
        Err(_) => false,
    }
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
                "#!/bin/sh\nif [ \"$1\" = -d ]; then exec cat; fi\nfor a in \"$@\"; do f=\"$a\"; done\necho run >> '{}'\ncp \"$f\" \"$f.fk\"\n",
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
                decompress: &["-d"],
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

    /// Regression: a compressor keeps the input's permissions on its output, so
    /// a READ-ONLY input (Go's `wasm_exec.js`, copied from a read-only GOROOT
    /// such as a Nix store) left a read-only `.br` / `.gz`, and the next build
    /// could not overwrite it: the tool failed ("Permission denied"), the build
    /// warned that brotli was missing, and the stale output stayed on disk.
    #[test]
    fn a_read_only_output_from_an_earlier_build_is_replaced() {
        use std::os::unix::fs::PermissionsExt;
        let d = scratch("readonly");
        let (tool, _log) = fake_tool(&d);
        let f = d.join("wasm_exec.js");
        let out = d.join("wasm_exec.js.fk");
        for cache in [None, Some(d.join("cache"))] {
            std::fs::write(&out, b"stale").unwrap();
            std::fs::set_permissions(&out, std::fs::Permissions::from_mode(0o444)).unwrap();
            std::fs::write(&f, b"fresh glue").unwrap();
            assert_ne!(
                compress(&f, &tool, cache.as_deref()),
                Outcome::Failed,
                "a read-only output must not fail the compressor (cache: {cache:?})"
            );
            assert_eq!(std::fs::read(&out).unwrap(), b"fresh glue");
            let _ = std::fs::set_permissions(&out, std::fs::Permissions::from_mode(0o644));
        }
        let _ = std::fs::remove_dir_all(&d);
    }

    /// Regression (B1): several assets compressed at once by one process. The
    /// temporary store name was `.tmp-<pid>-<ext>`, shared by every thread of
    /// the process, so one thread could rename another's output into its own
    /// cache key. Deterministic: every store's temporary name is its own,
    /// for the same tool, in the same process, on any thread. (Whether two
    /// threads actually raced depended on timing, so the test checks the
    /// cause, not a race; `a_cache_entry_that_is_not_the_inputs_result_is_replaced`
    /// covers an entry that was poisoned before the fix.)
    #[test]
    fn every_store_has_its_own_temporary_name() {
        let dir = Path::new("/cache");
        let names: Vec<PathBuf> = std::thread::scope(|s| {
            let hs: Vec<_> = (0..8)
                .map(|_| s.spawn(|| store_tmp_name(dir, &BROTLI)))
                .collect();
            hs.into_iter().map(|h| h.join().unwrap()).collect()
        });
        let mut unique = names.clone();
        unique.sort();
        unique.dedup();
        assert_eq!(unique.len(), names.len(), "{names:?}");
        assert_ne!(store_tmp_name(dir, &GZIP), store_tmp_name(dir, &GZIP));
    }

    /// A cached entry is used only when it decompresses to the input. A wrong
    /// entry (stored before the fix above, or by any other writer) is replaced.
    #[test]
    fn a_cache_entry_that_is_not_the_inputs_result_is_replaced() {
        let d = scratch("poisoned");
        let (tool, log) = fake_tool(&d);
        let cache = d.join("cache");
        let f = d.join("main.abc.wasm");
        std::fs::write(&f, b"the real wasm").unwrap();
        let entry = cache.join(cache_key(b"the real wasm", &tool));
        std::fs::write(&entry, b"another file's output").unwrap();
        assert_eq!(compress(&f, &tool, Some(&cache)), Outcome::Compressed);
        assert_eq!(runs(&log), 1, "a wrong entry must not be served");
        assert_eq!(
            std::fs::read(d.join("main.abc.wasm.fk")).unwrap(),
            b"the real wasm"
        );
        assert_eq!(std::fs::read(&entry).unwrap(), b"the real wasm");
        // And now it is a hit.
        assert_eq!(compress(&f, &tool, Some(&cache)), Outcome::CacheHit);
        let _ = std::fs::remove_dir_all(&d);
    }

    /// The real tools: compressing through the cache, hit or miss, writes the
    /// same bytes as `gzip -9` / `brotli -q 11` run by hand.
    #[test]
    fn the_output_is_the_tools_own_output() {
        let d = scratch("bytes");
        let f = d.join("main.0123.wasm");
        let content: Vec<u8> = (0..300_000u32).map(|i| (i * 7 % 251) as u8).collect();
        std::fs::write(&f, &content).unwrap();
        let have_brotli = Command::new("brotli").arg("--version").output().is_ok();
        let tools: &[&Tool] =
            if crate::live_gate::required(crate::live_gate::Need::Brotli, have_brotli) {
                &[&GZIP, &BROTLI]
            } else {
                &[&GZIP]
            };
        for tool in tools {
            let reference = d.join("ref");
            std::fs::create_dir_all(&reference).unwrap();
            let rf = reference.join("main.0123.wasm");
            std::fs::copy(&f, &rf).unwrap();
            assert!(Command::new(tool.program)
                .args(tool.args)
                .arg(&rf)
                .status()
                .unwrap()
                .success());
            let want =
                std::fs::read(reference.join(format!("main.0123.wasm.{}", tool.ext))).unwrap();
            let out = d.join(format!("main.0123.wasm.{}", tool.ext));
            for expect in [Outcome::Compressed, Outcome::CacheHit] {
                assert_eq!(compress(&f, tool, Some(&d.join("cache"))), expect);
                assert_eq!(
                    std::fs::read(&out).unwrap(),
                    want,
                    "{} ({expect:?})",
                    tool.program
                );
            }
            let _ = std::fs::remove_dir_all(&reference);
        }
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
            decompress: &[],
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

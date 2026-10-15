//! Precompressed web assets (`<file>.gz`, `<file>.br`) with a content-addressed
//! cache.
//!
//! `sky build --target web` (and every Sky.Spa build, whose frontend leg is one)
//! writes `main.<hash>.wasm.gz` and `.br` beside the hashed wasm so a static host
//! (`file_server { precompressed br gzip }`, or the Sky backend's own static
//! mounts) can serve them. brotli at quality 11 is the slow part of the whole
//! build: `brotli -q 11` took 81 s on a 52 MB wasm, longer than the rest of an
//! edit rebuild. So the `.br` is written in process by the `brotli` crate, a
//! large input in parallel parts ([`brotli_chunks`]): 26 s for a file 0.3%
//! larger, and standard brotli that any decoder reads. `--precompress=fast`
//! trades ~18% of size for seconds, `--precompress=off` writes nothing
//! ([`Level`]). gzip stays the `gzip` tool. The compressed bytes are a pure
//! function of the input bytes and the invocation, so they are cached under
//! the input's SHA-256: a rebuild whose wasm did not change copies the stored
//! result instead of compressing again.
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

/// gzip for `--precompress=fast`: -6 is ~2% larger than -9 on a Go wasm and
/// about three times quicker.
pub const GZIP_FAST: Tool = Tool {
    program: "gzip",
    args: &["-6", "-k", "-f"],
    ext: "gz",
    decompress: &["-d", "-c"],
};

/// brotli, run in this process by the `brotli` crate (pinned in Cargo.toml).
/// Its output is standard brotli: `brotli -d`, browsers, Node's zlib and Go's
/// decoders read it.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Brotli {
    pub quality: u32,
    pub lgwin: u32,
    /// Split a large input into [`brotli_chunks`] parts compressed at once
    /// (the crate's `compress_multi`). Each part may refer back into the
    /// parts before it, so the result stays within a fraction of a percent of
    /// one-thread output (measured +0.3% on a 52 MB wasm at quality 11) at a
    /// third of the time.
    pub parallel: bool,
}

/// `--precompress=max`: quality 11, the 16 MB window, in parallel.
pub const BROTLI_MAX: Brotli = Brotli {
    quality: 11,
    lgwin: 24,
    parallel: true,
};

/// `--precompress=fast`: quality 9, one thread. On a 52 MB wasm it takes
/// about 1.5 s against 26 s for `max`, for a file ~18% larger.
pub const BROTLI_FAST: Brotli = Brotli {
    quality: 9,
    lgwin: 24,
    parallel: false,
};

/// Names the brotli encoder in the cache key, so a new crate version (whose
/// bytes may differ) never reuses an older version's entries. Kept equal to
/// the version pinned in Cargo.toml by a test.
pub const BROTLI_CRATE: &str = "brotli 9.0.0";

/// The input bytes per parallel part, and the most parts. The count depends
/// only on the input's length, never on the machine, so the bytes (and the
/// cache key) are the same on every machine.
pub const BROTLI_CHUNK_BYTES: usize = 6 << 20;
pub const BROTLI_MAX_CHUNKS: usize = 8;

/// How many parts a parallel brotli splits `len` input bytes into.
pub fn brotli_chunks(len: usize) -> usize {
    len.div_ceil(BROTLI_CHUNK_BYTES).clamp(1, BROTLI_MAX_CHUNKS)
}

/// How one `.gz` / `.br` variant is made.
#[derive(Clone, Copy)]
pub enum Codec<'a> {
    /// An external tool (gzip).
    Tool(&'a Tool),
    /// brotli in this process.
    Brotli(Brotli),
}

impl Level {
    /// The gzip and brotli codecs of this level (`None` for `off`).
    pub fn codecs(self) -> Option<(Codec<'static>, Codec<'static>)> {
        match self {
            Level::Max => Some((Codec::Tool(&GZIP), Codec::Brotli(BROTLI_MAX))),
            Level::Fast => Some((Codec::Tool(&GZIP_FAST), Codec::Brotli(BROTLI_FAST))),
            Level::Off => None,
        }
    }
}

impl Codec<'_> {
    fn ext(&self) -> &str {
        match self {
            Codec::Tool(t) => t.ext,
            Codec::Brotli(_) => "br",
        }
    }

    /// Everything about the invocation that changes the output bytes.
    fn invocation(&self) -> String {
        match self {
            Codec::Tool(t) => format!("{} {}", t.program, t.args.join(" ")),
            Codec::Brotli(b) => format!(
                "{BROTLI_CRATE} q{} w{} parallel={} chunk={} max={}",
                b.quality, b.lgwin, b.parallel, BROTLI_CHUNK_BYTES, BROTLI_MAX_CHUNKS
            ),
        }
    }

    /// Write `out` from `file` (whose bytes are `input` when already read).
    fn produce(&self, file: &Path, out: &Path, input: Option<&[u8]>) -> bool {
        match self {
            Codec::Tool(t) => Command::new(t.program)
                .args(t.args)
                .arg(file)
                .status()
                .map(|s| s.success())
                .unwrap_or(false),
            Codec::Brotli(b) => {
                let owned;
                let bytes = match input {
                    Some(i) => i,
                    None => match std::fs::read(file) {
                        Ok(v) => {
                            owned = v;
                            &owned
                        }
                        Err(_) => return false,
                    },
                };
                match brotli_compress(bytes, *b) {
                    Some(c) => std::fs::write(out, c).is_ok(),
                    None => false,
                }
            }
        }
    }

    /// Whether the cached `entry` decompresses to exactly `input`. Any failure
    /// to check is a "no": the caller compresses again.
    fn decompresses_to(&self, entry: &Path, input: &[u8]) -> bool {
        match self {
            Codec::Tool(t) => tool_decompresses_to(entry, input, t),
            Codec::Brotli(_) => {
                let Ok(stored) = std::fs::read(entry) else {
                    return false;
                };
                let mut out = Vec::with_capacity(input.len());
                brotli::BrotliDecompress(&mut &stored[..], &mut out).is_ok() && out == input
            }
        }
    }
}

/// Compress `input` with `b`. `None` when the encoder fails (or panics: the
/// build then warns and ships without a `.br`, as with a missing tool).
pub fn brotli_compress(input: &[u8], b: Brotli) -> Option<Vec<u8>> {
    let parts = if b.parallel {
        brotli_chunks(input.len())
    } else {
        1
    };
    brotli_compress_parts(input, b, parts)
}

/// [`brotli_compress`] in `chunks` parts compressed at once (1: one thread).
fn brotli_compress_parts(input: &[u8], b: Brotli, chunks: usize) -> Option<Vec<u8>> {
    use brotli::enc::threading::{Owned, SendAlloc};
    use brotli::enc::{BrotliEncoderParams, StandardAlloc, UnionHasher};

    /// The crate's parallel encoder takes its input by value.
    struct Input(Vec<u8>);
    impl brotli::SliceWrapper<u8> for Input {
        fn slice(&self) -> &[u8] {
            &self.0
        }
    }

    let mut params = BrotliEncoderParams::default();
    params.quality = b.quality as i32;
    params.lgwin = b.lgwin as i32;
    params.size_hint = input.len();
    std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
        if chunks == 1 {
            let mut out = Vec::new();
            brotli::BrotliCompress(&mut &input[..], &mut out, &params).ok()?;
            return Some(out);
        }
        let mut out =
            vec![0u8; brotli::enc::BrotliEncoderMaxCompressedSizeMulti(input.len(), chunks)];
        let mut allocs: Vec<_> = (0..chunks)
            .map(|_| SendAlloc::new(StandardAlloc::default(), UnionHasher::Uninit))
            .collect();
        let n = brotli::enc::compress_multi_no_threadpool(
            &params,
            &mut Owned::new(Input(input.to_vec())),
            &mut out,
            &mut allocs[..],
        )
        .ok()?;
        out.truncate(n);
        Some(out)
    }))
    .ok()
    .flatten()
}

/// How much work `sky build` spends on the `.br` / `.gz` bundle variants:
/// `--precompress=max|fast|off` on the command line, else `SKY_PRECOMPRESS`,
/// else [`Level::Max`]. `--no-precompress` is `--precompress=off`.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Level {
    /// The smallest files (the default): brotli quality 11, gzip -9.
    Max,
    /// A quicker brotli for a fast edit-rebuild loop; the files are a little
    /// larger (see [`Level::brotli`]). gzip is the same as `max`.
    Fast,
    /// No `.br` / `.gz` files (an earlier build's are removed). A static host
    /// then compresses on the fly (the Sky backend gzips each response).
    Off,
}

/// The environment variable read when the command line names no level.
pub const ENV: &str = "SKY_PRECOMPRESS";

impl Level {
    pub fn parse(s: &str) -> Option<Self> {
        match s.trim().to_ascii_lowercase().as_str() {
            "max" => Some(Level::Max),
            "fast" => Some(Level::Fast),
            "off" => Some(Level::Off),
            _ => None,
        }
    }

    pub fn name(self) -> &'static str {
        match self {
            Level::Max => "max",
            Level::Fast => "fast",
            Level::Off => "off",
        }
    }

    /// The level for a build: the last `--precompress=<level>` (or
    /// `--precompress <level>`, or `--no-precompress`) in `args`, else `env`
    /// (the value of [`ENV`]), else `max`. An unknown level is an error, never
    /// a silent default.
    pub fn from_args(args: &[String], env: Option<&str>) -> Result<Self, String> {
        let bad = |v: &str, from: &str| {
            format!("{from} {v:?} is not a precompress level: use max, fast or off")
        };
        let mut flag = None;
        let mut it = args.iter();
        while let Some(a) = it.next() {
            if a == "--no-precompress" {
                flag = Some(Level::Off);
            } else if let Some(v) = a.strip_prefix("--precompress=") {
                flag = Some(Level::parse(v).ok_or_else(|| bad(v, "--precompress"))?);
            } else if a == "--precompress" {
                let v = it
                    .next()
                    .ok_or("--precompress needs a level: --precompress=max|fast|off")?;
                flag = Some(Level::parse(v).ok_or_else(|| bad(v, "--precompress"))?);
            }
        }
        if let Some(l) = flag {
            return Ok(l);
        }
        match env.map(str::trim).filter(|v| !v.is_empty()) {
            None => Ok(Level::Max),
            Some(v) => Level::parse(v).ok_or_else(|| bad(v, ENV)),
        }
    }

    /// The command-line form a child `sky build` is given, so it compresses
    /// at this level whatever its environment says.
    pub fn flag(self) -> String {
        format!("--precompress={}", self.name())
    }
}

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

/// The cache key for `bytes` under `codec`: the content hash plus everything
/// about the invocation that changes the output.
fn cache_key(bytes: &[u8], codec: &Codec) -> String {
    let content = crate::db_provision::sha256_hex(bytes);
    let invocation = crate::db_provision::sha256_hex(codec.invocation().as_bytes());
    format!("{}-{}.{}", &content[..32], &invocation[..8], codec.ext())
}

/// Write `<file>.<ext>` for `codec`, from the cache when the same bytes were
/// compressed before, else by compressing (and then storing the result).
pub fn compress(file: &Path, codec: &Codec, cache: Option<&Path>) -> Outcome {
    let mut out_name = file.as_os_str().to_owned();
    out_name.push(".");
    out_name.push(codec.ext());
    let out = PathBuf::from(out_name);
    // An earlier build's output may be read-only: a compressor keeps its
    // input's permissions, and Go's `wasm_exec.js` is read-only when GOROOT is
    // (a Nix store). Neither the cache copy nor the tool can open it for
    // writing, so remove it first; the directory is ours and writable.
    let _ = std::fs::remove_file(&out);
    let input = cache.and_then(|_| std::fs::read(file).ok());
    let cached = cache
        .zip(input.as_ref())
        .map(|(dir, bytes)| dir.join(cache_key(bytes, codec)));
    if let (Some(c), Some(bytes)) = (&cached, &input) {
        let hit = std::fs::metadata(c).map(|m| m.len() > 0).unwrap_or(false);
        if hit && !codec.decompresses_to(c, bytes) {
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
    if !codec.produce(file, &out, input.as_deref()) {
        let _ = std::fs::remove_file(&out);
        return Outcome::Failed;
    }
    if let (Some(c), Some(dir)) = (&cached, cache) {
        // Store via a temp name + rename so a concurrent build never reads a
        // half-written entry. The name is unique to this call (process, and a
        // counter across this process's threads), so two compressions running
        // at once never share it.
        let tmp = store_tmp_name(dir, codec.ext());
        if std::fs::copy(&out, &tmp).is_ok() && std::fs::rename(&tmp, c).is_err() {
            let _ = std::fs::remove_file(&tmp);
        }
        prune(dir, CACHE_KEEP);
    }
    Outcome::Compressed
}

/// The manifest a build writes in a bundle directory, listing the variants it
/// produced. The Sky backend serves a `.br` / `.gz` only when this file lists
/// it and the source and the variant still have the size and SHA-256 recorded
/// here (`runtime-go/rt/static_precompressed.go`), so a variant the build did
/// not write, or one of an earlier version of its source, is never sent.
pub const MANIFEST: &str = "sky-precompress.json";

/// The variants of a file: (HTTP content coding, file extension).
const VARIANTS: [(&str, &str); 2] = [("br", "br"), ("gzip", "gz")];

/// Remove every `.br` / `.gz` of `files` and the manifest of each of their
/// directories. Every level runs this first, so `off` leaves no variant of an
/// earlier build behind and a level that fails to write one leaves none.
pub fn remove_variants(files: &[PathBuf]) {
    for f in files {
        for (_, ext) in VARIANTS {
            let mut v = f.as_os_str().to_owned();
            v.push(".");
            v.push(ext);
            let _ = std::fs::remove_file(PathBuf::from(v));
        }
        if let Some(dir) = f.parent() {
            let _ = std::fs::remove_file(dir.join(MANIFEST));
        }
    }
}

/// Write [`MANIFEST`] in the directory of `files` (all in one directory):
/// each existing file's size and SHA-256, and those of each variant written
/// beside it, keyed by content coding. `level` is recorded for people reading
/// it. Written through a temporary name, so a server never reads half of it.
pub fn write_manifest(files: &[PathBuf], level: Level) -> std::io::Result<()> {
    let Some(dir) = files.first().and_then(|f| f.parent()) else {
        return Ok(());
    };
    let entry = |p: &Path| -> Option<serde_json::Value> {
        let bytes = std::fs::read(p).ok()?;
        Some(serde_json::json!({
            "size": bytes.len(),
            "sha256": crate::db_provision::sha256_hex(&bytes),
        }))
    };
    let mut listed = serde_json::Map::new();
    for f in files {
        let (Some(name), Some(mut e)) = (f.file_name(), entry(f)) else {
            continue;
        };
        let mut encodings = serde_json::Map::new();
        for (coding, ext) in VARIANTS {
            let mut v = f.as_os_str().to_owned();
            v.push(".");
            v.push(ext);
            if let Some(ve) = entry(Path::new(&v)) {
                encodings.insert(coding.to_string(), ve);
            }
        }
        if encodings.is_empty() {
            continue;
        }
        e["encodings"] = serde_json::Value::Object(encodings);
        listed.insert(name.to_string_lossy().into_owned(), e);
    }
    let body = serde_json::json!({
        "version": 1,
        "level": level.name(),
        "files": listed,
    });
    let tmp = store_tmp_name(dir, "manifest");
    std::fs::write(
        &tmp,
        serde_json::to_vec_pretty(&body).map_err(std::io::Error::other)?,
    )?;
    std::fs::rename(&tmp, dir.join(MANIFEST)).inspect_err(|_| {
        let _ = std::fs::remove_file(&tmp);
    })
}

/// Distinguishes the temporary store names of one process's calls.
static TMP_SEQ: AtomicU64 = AtomicU64::new(0);

/// A temporary name in `dir` for one store, unique to this call: the process
/// and a counter across its threads.
fn store_tmp_name(dir: &Path, ext: &str) -> PathBuf {
    dir.join(format!(
        ".tmp-{}-{}-{}",
        std::process::id(),
        TMP_SEQ.fetch_add(1, Ordering::Relaxed),
        ext
    ))
}

/// Whether the cached `entry` decompresses (with `tool`) to exactly `input`.
fn tool_decompresses_to(entry: &Path, input: &[u8], tool: &Tool) -> bool {
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

        assert_eq!(
            compress(&f, &Codec::Tool(&tool), Some(&cache)),
            Outcome::Compressed
        );
        assert_eq!(runs(&log), 1);
        let first = std::fs::read(d.join("main.abc.wasm.fk")).unwrap();

        // A rebuild writes the same bytes again (the staged tree is recreated).
        std::fs::remove_file(d.join("main.abc.wasm.fk")).unwrap();
        assert_eq!(
            compress(&f, &Codec::Tool(&tool), Some(&cache)),
            Outcome::CacheHit
        );
        assert_eq!(runs(&log), 1, "a cache hit must not run the compressor");
        assert_eq!(std::fs::read(d.join("main.abc.wasm.fk")).unwrap(), first);

        // Changed bytes miss the cache and compress again.
        std::fs::write(&f, b"wasm bytes v2").unwrap();
        assert_eq!(
            compress(&f, &Codec::Tool(&tool), Some(&cache)),
            Outcome::Compressed
        );
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
                compress(&f, &Codec::Tool(&tool), cache.as_deref()),
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
                .map(|_| s.spawn(|| store_tmp_name(dir, "br")))
                .collect();
            hs.into_iter().map(|h| h.join().unwrap()).collect()
        });
        let mut unique = names.clone();
        unique.sort();
        unique.dedup();
        assert_eq!(unique.len(), names.len(), "{names:?}");
        assert_ne!(store_tmp_name(dir, "gz"), store_tmp_name(dir, "gz"));
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
        let entry = cache.join(cache_key(b"the real wasm", &Codec::Tool(&tool)));
        std::fs::write(&entry, b"another file's output").unwrap();
        assert_eq!(
            compress(&f, &Codec::Tool(&tool), Some(&cache)),
            Outcome::Compressed
        );
        assert_eq!(runs(&log), 1, "a wrong entry must not be served");
        assert_eq!(
            std::fs::read(d.join("main.abc.wasm.fk")).unwrap(),
            b"the real wasm"
        );
        assert_eq!(std::fs::read(&entry).unwrap(), b"the real wasm");
        // And now it is a hit.
        assert_eq!(
            compress(&f, &Codec::Tool(&tool), Some(&cache)),
            Outcome::CacheHit
        );
        let _ = std::fs::remove_dir_all(&d);
    }

    /// gzip: compressing through the cache, hit or miss, writes the same bytes
    /// as `gzip -9` (`gzip -6` for `fast`) run by hand.
    #[test]
    fn the_gzip_output_is_the_tools_own_output() {
        let d = scratch("bytes");
        let f = d.join("main.0123.wasm");
        let content: Vec<u8> = (0..300_000u32).map(|i| (i * 7 % 251) as u8).collect();
        std::fs::write(&f, &content).unwrap();
        for tool in [&GZIP, &GZIP_FAST] {
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
            let want = std::fs::read(reference.join("main.0123.wasm.gz")).unwrap();
            let out = d.join("main.0123.wasm.gz");
            for expect in [Outcome::Compressed, Outcome::CacheHit] {
                assert_eq!(
                    compress(&f, &Codec::Tool(tool), Some(&d.join("cache"))),
                    expect
                );
                assert_eq!(
                    std::fs::read(&out).unwrap(),
                    want,
                    "{:?} ({expect:?})",
                    tool.args
                );
            }
            let _ = std::fs::remove_dir_all(&reference);
            let _ = std::fs::remove_dir_all(d.join("cache"));
            std::fs::create_dir_all(d.join("cache")).unwrap();
        }
        let _ = std::fs::remove_dir_all(&d);
    }

    /// Input shaped like a Go wasm for the brotli checks: repetitive code with
    /// varied constants, large enough that `max` splits it into several parts.
    fn wasm_like(len: usize) -> Vec<u8> {
        let mut v = Vec::with_capacity(len);
        let mut x: u32 = 0x1234_5678;
        while v.len() < len {
            x ^= x << 13;
            x ^= x >> 17;
            x ^= x << 5;
            let op = [0x20u8, 0x21, 0x41, 0x28, 0x36, 0x6a, 0x10, 0x0b][(x & 7) as usize];
            v.extend_from_slice(&[op, (x >> 8) as u8 & 0x3f, 0x00, (x >> 16) as u8 & 0x0f]);
            if x % 97 == 0 {
                v.extend_from_slice(b"runtime.gcWriteBarrier sky-app/rt.Coerce ");
            }
        }
        v.truncate(len);
        v
    }

    /// brotli (in process): the `.br` decodes with the standard `brotli -d`
    /// (the reference decoder browsers use) to exactly the input, through the
    /// cache, hit or miss; a parallel `max` is within 1% of the size `brotli -q
    /// 11` itself writes; and the bytes are the same on every run.
    #[test]
    fn the_brotli_output_is_standard_and_q11_sized() {
        let have_brotli = Command::new("brotli").arg("--version").output().is_ok();
        let check_cli = crate::live_gate::required(crate::live_gate::Need::Brotli, have_brotli);
        let d = scratch("brotli");
        let f = d.join("main.0123.wasm");
        // Small enough to stay quick: through `compress` it is one part; the
        // parallel encoder is checked on it directly below, in three parts.
        let content = wasm_like(900_000);
        std::fs::write(&f, &content).unwrap();
        let out = d.join("main.0123.wasm.br");
        let codec = Codec::Brotli(BROTLI_MAX);
        let mut first = None;
        for expect in [Outcome::Compressed, Outcome::CacheHit] {
            assert_eq!(compress(&f, &codec, Some(&d.join("cache"))), expect);
            let br = std::fs::read(&out).unwrap();
            let mut dec = Vec::new();
            brotli::BrotliDecompress(&mut &br[..], &mut dec).unwrap();
            assert!(dec == content, "the crate's decoder ({expect:?})");
            if check_cli {
                let o = Command::new("brotli")
                    .args(["-d", "-c"])
                    .arg(&out)
                    .output()
                    .unwrap();
                assert!(
                    o.status.success() && o.stdout == content,
                    "brotli -d ({expect:?})"
                );
            }
            first.get_or_insert(br);
        }
        assert_eq!(
            brotli_compress(&content, BROTLI_MAX).as_ref(),
            first.as_ref(),
            "the bytes are deterministic"
        );
        let again = brotli_compress_parts(&content, BROTLI_MAX, 3).unwrap();
        assert_eq!(
            brotli_compress_parts(&content, BROTLI_MAX, 3),
            Some(again.clone()),
            "the parallel bytes are deterministic"
        );
        let mut dec = Vec::new();
        brotli::BrotliDecompress(&mut &again[..], &mut dec).unwrap();
        assert!(dec == content, "the parallel output decodes");
        if check_cli {
            let p = d.join("parts.br");
            std::fs::write(&p, &again).unwrap();
            let o = Command::new("brotli")
                .args(["-d", "-c"])
                .arg(&p)
                .output()
                .unwrap();
            assert!(
                o.status.success() && o.stdout == content,
                "brotli -d of the parallel output"
            );
        }
        if check_cli {
            let o = Command::new("brotli")
                .args(["-q", "11", "-c"])
                .arg(&f)
                .output()
                .unwrap();
            assert!(o.status.success());
            let (ours, cli) = (again.len() as f64, o.stdout.len() as f64);
            assert!(
                ours <= cli * 1.01,
                "parallel q11 is {ours} bytes, brotli -q 11 is {cli}: more than 1% larger"
            );
        }
        // `fast` is standard brotli too.
        let fast = brotli_compress(&content, BROTLI_FAST).unwrap();
        let mut dec = Vec::new();
        brotli::BrotliDecompress(&mut &fast[..], &mut dec).unwrap();
        assert!(dec == content);
        let _ = std::fs::remove_dir_all(&d);
    }

    /// A large input in four parallel parts at quality 11 and the 16 MB
    /// window: every part after the first may refer back into the parts before
    /// it (the cross-part references the one-part test above cannot reach),
    /// and the reference decoder (`brotli -d`, what browsers use) and the
    /// crate's decoder both give exactly the input. Ignored by default: about
    /// 0.8 GB of memory and 25 s (measured: 808 MB peak, 23 s wall, 65 s CPU). Run it with
    /// `cargo test --release -p sky --bin sky -- --ignored large_parallel_brotli`.
    #[test]
    #[ignore = "about 0.8 GB and 25 s; run with --ignored"]
    fn a_large_parallel_brotli_decodes_to_the_input() {
        let have_brotli = Command::new("brotli").arg("--version").output().is_ok();
        let check_cli = crate::live_gate::required(crate::live_gate::Need::Brotli, have_brotli);
        let content = wasm_like(24 << 20);
        assert_eq!(brotli_chunks(content.len()), 4, "four parts");
        let br = brotli_compress(&content, BROTLI_MAX).expect("compresses");
        let mut dec = Vec::with_capacity(content.len());
        brotli::BrotliDecompress(&mut &br[..], &mut dec).unwrap();
        assert!(dec == content, "the crate's decoder");
        if check_cli {
            let d = scratch("large");
            let p = d.join("large.br");
            std::fs::write(&p, &br).unwrap();
            let o = Command::new("brotli")
                .args(["-d", "-c"])
                .arg(&p)
                .output()
                .unwrap();
            assert!(o.status.success(), "brotli -d failed");
            assert_eq!(o.stdout.len(), content.len());
            assert!(o.stdout == content, "brotli -d gives the input");
            let _ = std::fs::remove_dir_all(&d);
        }
    }

    /// The cache key names the pinned crate version: a version bump can never
    /// reuse an older encoder's entries without the key changing.
    #[test]
    fn the_brotli_cache_key_names_the_pinned_crate() {
        let toml = include_str!("../Cargo.toml");
        let pin = BROTLI_CRATE.replace("brotli ", "brotli = \"=");
        assert!(toml.contains(&pin), "Cargo.toml must pin {pin}\"");
        assert!(Codec::Brotli(BROTLI_MAX)
            .invocation()
            .contains(BROTLI_CRATE));
        assert_ne!(
            cache_key(b"x", &Codec::Brotli(BROTLI_MAX)),
            cache_key(b"x", &Codec::Brotli(BROTLI_FAST))
        );
    }

    #[test]
    fn brotli_parts_depend_only_on_the_length() {
        assert_eq!(brotli_chunks(0), 1);
        assert_eq!(brotli_chunks(60_000), 1);
        assert_eq!(brotli_chunks(BROTLI_CHUNK_BYTES), 1);
        assert_eq!(brotli_chunks(BROTLI_CHUNK_BYTES + 1), 2);
        assert_eq!(brotli_chunks(52 << 20), BROTLI_MAX_CHUNKS);
        assert_eq!(brotli_chunks(usize::MAX / 2), BROTLI_MAX_CHUNKS);
    }

    #[test]
    fn no_cache_dir_always_runs_the_tool() {
        let d = scratch("nocache");
        let (tool, log) = fake_tool(&d);
        let f = d.join("a.wasm");
        std::fs::write(&f, b"x").unwrap();
        assert_eq!(compress(&f, &Codec::Tool(&tool), None), Outcome::Compressed);
        assert_eq!(compress(&f, &Codec::Tool(&tool), None), Outcome::Compressed);
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
        assert_eq!(
            compress(&f, &Codec::Tool(&tool), Some(&d.join("cache"))),
            Outcome::Failed
        );
        assert!(!d.join("a.wasm.zz").exists());
        let _ = std::fs::remove_dir_all(&d);
    }

    fn args(a: &[&str]) -> Vec<String> {
        a.iter().map(|s| s.to_string()).collect()
    }

    #[test]
    fn the_level_comes_from_the_flag_then_the_environment_then_max() {
        use Level::*;
        assert_eq!(Level::from_args(&args(&["src/Main.sky"]), None), Ok(Max));
        assert_eq!(Level::from_args(&args(&[]), Some("fast")), Ok(Fast));
        assert_eq!(Level::from_args(&args(&[]), Some(" OFF ")), Ok(Off));
        assert_eq!(Level::from_args(&args(&[]), Some("")), Ok(Max));
        assert_eq!(
            Level::from_args(&args(&["--precompress=fast"]), Some("off")),
            Ok(Fast),
            "the command line wins over the environment"
        );
        assert_eq!(
            Level::from_args(&args(&["--precompress", "off"]), None),
            Ok(Off)
        );
        assert_eq!(
            Level::from_args(&args(&["--no-precompress"]), Some("max")),
            Ok(Off)
        );
        assert_eq!(
            Level::from_args(&args(&["--no-precompress", "--precompress=max"]), None),
            Ok(Max),
            "the last flag wins"
        );
        for l in [Max, Fast, Off] {
            assert_eq!(Level::from_args(&args(&[&l.flag()]), None), Ok(l));
        }
    }

    #[test]
    fn an_unknown_level_is_an_error() {
        let e = Level::from_args(&args(&["--precompress=best"]), None).unwrap_err();
        assert!(e.contains("max, fast or off"), "{e}");
        let e = Level::from_args(&args(&[]), Some("11")).unwrap_err();
        assert!(e.contains(ENV), "{e}");
        assert!(Level::from_args(&args(&["--precompress"]), None).is_err());
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

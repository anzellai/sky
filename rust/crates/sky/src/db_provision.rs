//! `sky db provision --embed` — fetch Sky's own PostgreSQL bundle into the
//! versioned cache `db_cluster`'s discovery already looks in, and record the pin.
//!
//! Phase 3 of `docs/skydb/embedded-postgres.md`. P2 *discovers*
//! (`SKY_POSTGRES_BIN` → `~/.sky/postgres/<version>/bin` → `PATH`); this module
//! populates the middle entry. P2b *builds* the bundles
//! (`scripts/skydb/build-postgres-bundle.sh` + `.github/workflows/postgres-bundle.yml`);
//! nothing here invents a format — the asset name, the archive layout and the
//! checksum manifest are all consumed exactly as that job publishes them:
//!
//! ```text
//! postgres-bundle-v<version>/            (the release tag)
//!   postgres-<version>-<platform>.tar.gz (one top-level dir: bin/ lib/ share/)
//!   sbom-<platform>.json
//!   SHA256SUMS                           (`sha256sum -- *.tar.gz *.json`)
//! ```
//!
//! Three properties are load-bearing, and each has a gate that has been observed
//! failing:
//!
//! 1. **The checksum is verified against the bytes on disk, before anything is
//!    extracted.** A corrupted or truncated download must never reach the cache.
//! 2. **The install is atomic.** The archive is extracted into a staging
//!    directory *outside* `~/.sky/postgres/` and renamed into place, so an
//!    interrupted provision cannot leave a half-populated
//!    `~/.sky/postgres/<version>/bin` for discovery to find and fail on later.
//!    Staging inside `postgres/` would be worse than useless: `cached_versions`
//!    reads that directory, so a partial tree there is a *candidate*.
//! 3. **Provisioning what is already provisioned is a fast success**, with no
//!    request made at all.

use std::path::{Path, PathBuf};
use std::process::{Command, ExitCode};

/// The PostgreSQL the bundles are built from. Kept in step with
/// `PG_VERSION` in `scripts/skydb/build-postgres-bundle.sh` by
/// `default_version_matches_the_bundle_build_script`.
pub const DEFAULT_PG_VERSION: &str = "18.6";

/// The release tag `.github/workflows/postgres-bundle.yml` publishes under
/// (`push: tags: ['postgres-bundle-v*']`).
const TAG_PREFIX: &str = "postgres-bundle-v";

const DEFAULT_RELEASE_BASE: &str = "https://github.com/anzellai/sky/releases/download";

/// The manifest name the publish job writes (`sha256sum -- *.tar.gz *.json`).
const CHECKSUM_FILE: &str = "SHA256SUMS";

/// What a bundle must contain to be a bundle. The same three
/// `db_cluster::REQUIRED_BINS` checks for, so a provision that "succeeds" and a
/// discovery that then fails cannot disagree.
const REQUIRED_BINS: [&str; 3] = ["initdb", "pg_ctl", "postgres"];

/// Staging + download scratch lives here — a sibling of `postgres/`, never
/// inside it, and on the same filesystem so the final rename is atomic.
const STAGING_DIR: &str = ".provision-tmp";

/// Rough size of an extracted bundle (~77 MB) plus its archive (~25 MB), plus
/// slack. Refusing up front beats a half-written cache and an ENOSPC backtrace.
const NEEDED_MB: u64 = 400;

// ---- platform ------------------------------------------------------------

/// The platform string P2b names its artifacts with. `None` is a platform Sky
/// has no bundle for — which must be an actionable refusal, never a download of
/// something that cannot run.
pub fn platform_tag_for(os: &str, arch: &str) -> Option<&'static str> {
    match (os, arch) {
        ("linux", "x86_64") => Some("linux-amd64"),
        ("linux", "aarch64") => Some("linux-arm64"),
        ("macos", "x86_64") => Some("darwin-amd64"),
        ("macos", "aarch64") => Some("darwin-arm64"),
        _ => None,
    }
}

pub fn platform_tag() -> Option<&'static str> {
    platform_tag_for(std::env::consts::OS, std::env::consts::ARCH)
}

/// Windows is out of scope by decision, not by omission (the bundle job's matrix
/// comment records why: no PE scanner backend for the licence gate). Saying so
/// is the difference between a user filing a bug and a user picking a way out.
pub fn unsupported_platform_message(os: &str, arch: &str) -> String {
    let windows_note = if os == "windows" {
        "\nWindows needs an MSVC PostgreSQL build and a PE licence scanner; it is\n\
         out of scope for this phase (see .github/workflows/postgres-bundle.yml).\n"
    } else {
        ""
    };
    format!(
        "sky db provision: no PostgreSQL bundle is built for {os}/{arch}.\n\
         {windows_note}\n\
         Sky publishes bundles for: linux-amd64, linux-arm64, darwin-amd64, darwin-arm64.\n\
         \n\
         On this platform, use a system PostgreSQL instead:\n\
         \x20 • install it and put its bin dir on PATH, or\n\
         \x20 • point sky at one:  SKY_POSTGRES_BIN=/path/to/postgresql/bin sky db start"
    )
}

// ---- naming (consumed from P2b, never invented here) ---------------------

pub fn bundle_dir_name(version: &str, platform: &str) -> String {
    format!("postgres-{version}-{platform}")
}

pub fn asset_name(version: &str, platform: &str) -> String {
    format!("{}.tar.gz", bundle_dir_name(version, platform))
}

pub fn release_tag(version: &str) -> String {
    format!("{TAG_PREFIX}{version}")
}

/// The directory holding the assets: `$SKY_POSTGRES_BUNDLE_URL` when set (a
/// local mirror, a file:// tree, or an air-gapped host), else the GitHub release.
pub fn base_url(version: &str, override_base: Option<&str>) -> String {
    let base = override_base
        .map(str::trim)
        .filter(|b| !b.is_empty())
        .map(str::to_string)
        .unwrap_or_else(|| format!("{DEFAULT_RELEASE_BASE}/{}", release_tag(version)));
    base.trim_end_matches('/').to_string()
}

/// Pull one file's hash out of a `sha256sum` manifest. The format is
/// `<64 hex>  <name>` (two spaces; `*name` in binary mode), and the manifest
/// names every asset in the release, not just ours.
pub fn parse_sha256sums(text: &str, want: &str) -> Option<String> {
    for line in text.lines() {
        let line = line.trim();
        let Some((hash, name)) = line.split_once(char::is_whitespace) else {
            continue;
        };
        let name = name.trim().trim_start_matches('*');
        if name == want && is_sha256_hex(hash) {
            return Some(hash.to_ascii_lowercase());
        }
    }
    None
}

pub fn is_sha256_hex(s: &str) -> bool {
    s.len() == 64 && s.chars().all(|c| c.is_ascii_hexdigit())
}

// ---- the pin -------------------------------------------------------------

/// The key `sky.toml` records the pin under. It lives in `[database]` — the
/// section that already owns `driver`, `path`/`url`, the pool knobs, `isolation`
/// and `embedded` — because a second section describing the same subsystem would
/// leave a reader two places to look and no rule for which wins.
pub const PIN_KEY: &str = "postgresVersion";

/// Read a project's pinned PostgreSQL version.
pub fn pinned_version(project_dir: &Path) -> Option<String> {
    project::sky_toml_section_key(project_dir, "database", PIN_KEY)
        .map(|v| v.trim().to_string())
        .filter(|v| !v.is_empty())
}

/// Write the pin into `sky.toml`, preserving everything else in the file. Returns
/// `None` when the file already records this exact version (so a re-provision
/// does not rewrite a file and dirty a git tree for nothing).
pub fn pinned_sky_toml(text: &str, version: &str) -> Option<String> {
    // Located first, edited second. A single streaming pass has to decide where
    // the key goes before it knows whether the section already carries one, and
    // the "append on the way out of the section" placement that falls out of
    // that lands the key after the section's trailing blank line — visually
    // inside the NEXT section, which is exactly the kind of edit that makes a
    // tool untrustworthy with someone's config file.
    let mut lines: Vec<String> = text.lines().map(str::to_string).collect();
    let mut header: Option<usize> = None;
    let mut key_at: Option<usize> = None;
    let mut section = String::new();
    for (i, raw) in lines.iter().enumerate() {
        let line = raw.trim();
        if line.starts_with('[') && line.contains(']') {
            if let Some(end) = line.find(']') {
                section = line[1..end].trim().trim_matches('"').to_string();
            }
            if section == "database" && header.is_none() {
                header = Some(i);
            }
            continue;
        }
        if section == "database" && key_at.is_none() {
            if let Some((k, _)) = line.split_once('=') {
                if k.trim() == PIN_KEY {
                    key_at = Some(i);
                }
            }
        }
    }
    let pin = format!("{PIN_KEY} = \"{version}\"");
    match (key_at, header) {
        (Some(i), _) => {
            if lines[i].trim() == pin {
                return None; // already pinned to exactly this
            }
            lines[i] = pin;
        }
        (None, Some(h)) => lines.insert(h + 1, pin),
        (None, None) => {
            if !lines.last().map(|l| l.trim().is_empty()).unwrap_or(true) {
                lines.push(String::new());
            }
            lines.push("[database]".to_string());
            lines.push(pin);
        }
    }
    let mut s = lines.join("\n");
    s.push('\n');
    Some(s)
}

// ---- SHA-256 -------------------------------------------------------------
//
// Implemented here rather than shelling out to `sha256sum`/`shasum`: the whole
// point of this check is that it cannot be skipped or fooled, and a shell-out
// makes the verdict depend on which of two differently-spelled tools happens to
// exist and on parsing its output. It is also the only way the digest itself can
// carry NIST test vectors as a unit test.

const K: [u32; 64] = [
    0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
    0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
    0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
    0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
    0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
    0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
    0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
    0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
];

pub struct Sha256 {
    h: [u32; 8],
    buf: [u8; 64],
    buf_len: usize,
    len_bits: u64,
}

impl Default for Sha256 {
    fn default() -> Self {
        Self::new()
    }
}

impl Sha256 {
    pub fn new() -> Sha256 {
        Sha256 {
            h: [
                0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab,
                0x5be0cd19,
            ],
            buf: [0u8; 64],
            buf_len: 0,
            len_bits: 0,
        }
    }

    pub fn update(&mut self, mut data: &[u8]) {
        self.len_bits = self.len_bits.wrapping_add((data.len() as u64) * 8);
        if self.buf_len > 0 {
            let take = (64 - self.buf_len).min(data.len());
            self.buf[self.buf_len..self.buf_len + take].copy_from_slice(&data[..take]);
            self.buf_len += take;
            data = &data[take..];
            if self.buf_len == 64 {
                let block = self.buf;
                self.compress(&block);
                self.buf_len = 0;
            }
        }
        while data.len() >= 64 {
            let mut block = [0u8; 64];
            block.copy_from_slice(&data[..64]);
            self.compress(&block);
            data = &data[64..];
        }
        if !data.is_empty() {
            self.buf[..data.len()].copy_from_slice(data);
            self.buf_len = data.len();
        }
    }

    pub fn finish(mut self) -> [u8; 32] {
        let bits = self.len_bits;
        self.update_no_len(&[0x80]);
        while self.buf_len != 56 {
            self.update_no_len(&[0]);
        }
        self.update_no_len(&bits.to_be_bytes());
        let mut out = [0u8; 32];
        for (i, w) in self.h.iter().enumerate() {
            out[i * 4..i * 4 + 4].copy_from_slice(&w.to_be_bytes());
        }
        out
    }

    fn update_no_len(&mut self, data: &[u8]) {
        let saved = self.len_bits;
        self.update(data);
        self.len_bits = saved;
    }

    fn compress(&mut self, block: &[u8; 64]) {
        let mut w = [0u32; 64];
        for i in 0..16 {
            w[i] = u32::from_be_bytes([
                block[i * 4],
                block[i * 4 + 1],
                block[i * 4 + 2],
                block[i * 4 + 3],
            ]);
        }
        for i in 16..64 {
            let s0 = w[i - 15].rotate_right(7) ^ w[i - 15].rotate_right(18) ^ (w[i - 15] >> 3);
            let s1 = w[i - 2].rotate_right(17) ^ w[i - 2].rotate_right(19) ^ (w[i - 2] >> 10);
            w[i] = w[i - 16]
                .wrapping_add(s0)
                .wrapping_add(w[i - 7])
                .wrapping_add(s1);
        }
        let [mut a, mut b, mut c, mut d, mut e, mut f, mut g, mut h] = self.h;
        for i in 0..64 {
            let s1 = e.rotate_right(6) ^ e.rotate_right(11) ^ e.rotate_right(25);
            let ch = (e & f) ^ ((!e) & g);
            let t1 = h
                .wrapping_add(s1)
                .wrapping_add(ch)
                .wrapping_add(K[i])
                .wrapping_add(w[i]);
            let s0 = a.rotate_right(2) ^ a.rotate_right(13) ^ a.rotate_right(22);
            let maj = (a & b) ^ (a & c) ^ (b & c);
            let t2 = s0.wrapping_add(maj);
            h = g;
            g = f;
            f = e;
            e = d.wrapping_add(t1);
            d = c;
            c = b;
            b = a;
            a = t1.wrapping_add(t2);
        }
        for (i, v) in [a, b, c, d, e, f, g, h].iter().enumerate() {
            self.h[i] = self.h[i].wrapping_add(*v);
        }
    }
}

/// One-shot digest. The provision path hashes files in chunks
/// ([`sha256_file`]); this exists so the primitive itself can be held against
/// the published NIST vectors, which is the only way to know the comparison the
/// checksum gate makes is a comparison of the right thing.
#[allow(dead_code)]
pub fn sha256_hex(data: &[u8]) -> String {
    let mut s = Sha256::new();
    s.update(data);
    hex(&s.finish())
}

fn hex(bytes: &[u8]) -> String {
    let mut out = String::with_capacity(bytes.len() * 2);
    for b in bytes {
        out.push_str(&format!("{b:02x}"));
    }
    out
}

/// Digest a file in chunks — the archive is ~25 MB and the extracted tree ~77 MB;
/// there is no reason to hold either in memory to hash it.
pub fn sha256_file(path: &Path) -> Result<String, String> {
    use std::io::Read;
    let mut f = std::fs::File::open(path)
        .map_err(|e| format!("cannot read {} to check it: {e}", path.display()))?;
    let mut h = Sha256::new();
    let mut buf = vec![0u8; 64 * 1024];
    loop {
        let n = f
            .read(&mut buf)
            .map_err(|e| format!("cannot read {}: {e}", path.display()))?;
        if n == 0 {
            break;
        }
        h.update(&buf[..n]);
    }
    Ok(hex(&h.finish()))
}

/// The refusal a bad download produces. Names both digests and the source, so the
/// reader can tell a corrupted transfer from a bundle that is not the one we
/// pinned.
pub fn checksum_mismatch_message(source: &str, expected: &str, actual: &str) -> String {
    format!(
        "sky db provision: CHECKSUM MISMATCH — refusing to install.\n\
         \x20 source:   {source}\n\
         \x20 expected: {expected}\n\
         \x20 actual:   {actual}\n\
         \n\
         The download is corrupt, truncated, or not the artifact this version of\n\
         sky pins. Nothing has been extracted and the cache is untouched.\n\
         Re-run to retry; if it repeats, the published bundle or the mirror is wrong."
    )
}

// ---- fetching ------------------------------------------------------------

/// Fetch `url` to `dest`. `file://` is handled directly rather than through curl
/// so an offline install and the test harness do not depend on curl's protocol
/// support; everything else shells out to curl, the same choice `sky upgrade`
/// makes rather than pulling a TLS stack into the compiler.
fn fetch(url: &str, dest: &Path, quiet: bool) -> Result<(), String> {
    if let Some(path) = url.strip_prefix("file://") {
        let src = Path::new(path);
        if !src.is_file() {
            return Err(format!("no such file: {}", src.display()));
        }
        std::fs::copy(src, dest).map_err(|e| format!("cannot copy {}: {e}", src.display()))?;
        return Ok(());
    }
    let mut cmd = Command::new("curl");
    cmd.arg(if quiet { "-fsSL" } else { "-fL" });
    if !quiet {
        cmd.arg("--progress-bar");
    }
    cmd.args(["--retry", "2", "--max-time", "900", "-o"])
        .arg(dest)
        .arg(url);
    let status = cmd
        .status()
        .map_err(|e| format!("could not run curl ({e}); is curl installed?"))?;
    if status.success() {
        return Ok(());
    }
    let _ = std::fs::remove_file(dest);
    Err(curl_failure_message(url, status.code()))
}

/// curl's exit codes separate "the network is not there" from "the server said
/// no", and those are different problems with different ways out.
pub fn curl_failure_message(url: &str, code: Option<i32>) -> String {
    let offline = matches!(code, Some(5 | 6 | 7 | 28 | 35));
    if offline {
        format!(
            "sky db provision: cannot reach {url} (curl exit {}).\n\
             \n\
             This looks like no network. Nothing has been installed.\n\
             To provision offline, copy the bundle onto this machine and install it\n\
             from the local file:\n\
             \x20 sky db provision --embed --from ./postgres-<version>-<platform>.tar.gz \\\n\
             \x20                          --checksum <sha256 from the release's SHA256SUMS>\n\
             or point sky at an existing installation instead:\n\
             \x20 SKY_POSTGRES_BIN=/path/to/postgresql/bin sky db start",
            code.map(|c| c.to_string()).unwrap_or_else(|| "signal".into())
        )
    } else {
        format!(
            "sky db provision: download failed for {url} (curl exit {}).\n\
             If that URL 404s, this version of sky pins a bundle that has not been\n\
             published for your platform. Check\n\
             \x20 https://github.com/anzellai/sky/releases\n\
             or install a system PostgreSQL and point SKY_POSTGRES_BIN at it.",
            code.map(|c| c.to_string()).unwrap_or_else(|| "signal".into())
        )
    }
}

// ---- the verb ------------------------------------------------------------

#[derive(Debug, Default)]
pub struct Opts {
    pub version: Option<String>,
    pub from: Option<PathBuf>,
    pub checksum: Option<String>,
    pub base_url: Option<String>,
    pub force: bool,
    /// Suppress the sky.toml pin (doctor's pre-warm: a fix must not edit source).
    pub no_pin: bool,
}

pub enum Outcome {
    AlreadyPresent { version: String, bin_dir: PathBuf },
    Installed { version: String, bin_dir: PathBuf, pinned: bool },
}

const USAGE: &str = "usage: sky db provision --embed [--version <v>] [--from <archive.tar.gz>]\n\
     \x20                       [--checksum <sha256>] [--force]\n\
     \n\
     Fetches Sky's PostgreSQL bundle into ~/.sky/postgres/<version>/ (or\n\
     $SKY_HOME), verifies it against the release checksum manifest, and records\n\
     the pin in sky.toml.\n\
     \n\
     \x20 --from      install from a local bundle archive instead of downloading\n\
     \x20             (offline installs; requires --checksum or a sibling SHA256SUMS)\n\
     \x20 --checksum  the expected sha256 of the archive\n\
     \x20 --force     re-install even when this version is already provisioned\n\
     \n\
     \x20 $SKY_POSTGRES_BUNDLE_URL overrides the release URL the bundle is fetched from.";

pub fn cmd_provision(args: &[String]) -> ExitCode {
    let opts = match parse_args(args) {
        Ok(o) => o,
        Err(e) => {
            eprintln!("{e}\n\n{USAGE}");
            return ExitCode::from(2);
        }
    };
    match provision(&opts) {
        Ok(Outcome::AlreadyPresent { version, bin_dir }) => {
            println!(
                "sky db provision: PostgreSQL {version} is already provisioned.\n\
                 \x20 {}\n(nothing downloaded; pass --force to re-install)",
                bin_dir.display()
            );
            ExitCode::SUCCESS
        }
        Ok(Outcome::Installed { version, bin_dir, pinned }) => {
            println!(
                "sky db provision: PostgreSQL {version} installed.\n\
                 \x20 {}{}\n\nNext: sky db start",
                bin_dir.display(),
                if pinned {
                    format!("\n\x20 pinned in sky.toml ([database] {PIN_KEY} = \"{version}\")")
                } else {
                    String::new()
                }
            );
            ExitCode::SUCCESS
        }
        Err(e) => {
            eprintln!("{e}");
            ExitCode::FAILURE
        }
    }
}

fn parse_args(args: &[String]) -> Result<Opts, String> {
    let mut o = Opts::default();
    let mut embed = false;
    let mut it = args.iter();
    while let Some(a) = it.next() {
        match a.as_str() {
            "--embed" => embed = true,
            "--force" => o.force = true,
            "--no-pin" => o.no_pin = true,
            "--version" => {
                o.version = Some(it.next().ok_or("--version needs a value")?.clone());
            }
            "--from" => {
                o.from = Some(PathBuf::from(it.next().ok_or("--from needs a path")?));
            }
            "--checksum" => {
                o.checksum = Some(it.next().ok_or("--checksum needs a sha256")?.clone());
            }
            "--url" => {
                o.base_url = Some(it.next().ok_or("--url needs a URL")?.clone());
            }
            other => return Err(format!("unknown argument: {other}")),
        }
    }
    if !embed {
        // The verb exists to serve `--embed`; accepting a bare `sky db provision`
        // would leave a reader guessing what else it might one day provision.
        return Err("sky db provision: --embed is required".into());
    }
    if let Some(c) = &o.checksum {
        if !is_sha256_hex(c.trim()) {
            return Err(format!("--checksum is not a sha256 hex digest: {c}"));
        }
    }
    Ok(o)
}

pub fn sky_home() -> PathBuf {
    if let Some(h) = std::env::var_os("SKY_HOME").filter(|h| !h.is_empty()) {
        return PathBuf::from(h);
    }
    let home = std::env::var_os("HOME")
        .or_else(|| std::env::var_os("USERPROFILE"))
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from("."));
    home.join(".sky")
}

/// Every required binary present (and, on unix, executable). Also the
/// idempotency predicate: a cache entry counts as provisioned only when it is
/// complete, so a truncated one is re-provisioned rather than trusted.
pub fn bundle_is_complete(bin_dir: &Path) -> bool {
    REQUIRED_BINS.iter().all(|b| {
        let p = bin_dir.join(b);
        let Ok(m) = std::fs::metadata(&p) else {
            return false;
        };
        if !m.is_file() {
            return false;
        }
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            if m.permissions().mode() & 0o111 == 0 {
                return false;
            }
        }
        true
    })
}

/// Fetch the release archive for `version`/`platform` and verify it against the
/// release's own `SHA256SUMS`, leaving it at `dest`.
///
/// This is the verb's download-and-verify half with the extract-and-install half
/// removed, because `sky build --embed` needs the **archive**, not the
/// extracted tree: `go:embed` forces mode 0444 on every file and cannot
/// represent a symlink at all, so an embedded directory tree yields a
/// non-executable `postgres` and no `libpq.5.dylib`. The tar has to survive
/// intact all the way into the binary.
///
/// The order is the verb's order and for the verb's reason: the manifest is
/// fetched first so nothing is downloaded without something to check it
/// against, and the digest is taken from the bytes that landed rather than the
/// ones we meant to write. `dest` is written only after the comparison passes.
pub fn fetch_verified_archive(
    version: &str,
    platform: &str,
    dest: &Path,
    base_url_override: Option<&str>,
) -> Result<(), String> {
    let parent = dest
        .parent()
        .ok_or_else(|| format!("sky build --embed: {} has no parent", dest.display()))?;
    std::fs::create_dir_all(parent)
        .map_err(|e| format!("sky build --embed: cannot create {}: {e}", parent.display()))?;
    check_free_space(parent)?;

    let asset = asset_name(version, platform);
    let base = base_url(version, base_url_override.or(env_base().as_deref()));
    let tmp = parent.join(format!(".{asset}.{}.part", std::process::id()));
    let _ = std::fs::remove_file(&tmp);

    let sums_url = format!("{base}/{CHECKSUM_FILE}");
    let sums_path = parent.join(format!(".{CHECKSUM_FILE}.{}.part", std::process::id()));
    let _ = std::fs::remove_file(&sums_path);
    let sums_result = fetch(&sums_url, &sums_path, true).and_then(|()| {
        std::fs::read_to_string(&sums_path)
            .map_err(|e| format!("sky build --embed: cannot read {}: {e}", sums_path.display()))
    });
    let _ = std::fs::remove_file(&sums_path);
    let sums = sums_result.map_err(|e| {
        format!("{e}\n(the checksum manifest is fetched first — nothing is downloaded unverified)")
    })?;
    let expected = parse_sha256sums(&sums, &asset).ok_or_else(|| {
        format!(
            "sky build --embed: {sums_url} does not list {asset}.\n\
             That release does not carry a bundle for {platform}."
        )
    })?;

    let url = format!("{base}/{asset}");
    println!("sky build --embed: fetching {url}");
    fetch(&url, &tmp, false)?;
    let actual = match sha256_file(&tmp) {
        Ok(a) => a,
        Err(e) => {
            let _ = std::fs::remove_file(&tmp);
            return Err(e);
        }
    };
    if actual != expected.to_ascii_lowercase() {
        let _ = std::fs::remove_file(&tmp);
        return Err(checksum_mismatch_message(&url, &expected, &actual));
    }
    std::fs::rename(&tmp, dest).map_err(|e| {
        let _ = std::fs::remove_file(&tmp);
        format!("sky build --embed: cannot install {}: {e}", dest.display())
    })
}

pub fn provision(opts: &Opts) -> Result<Outcome, String> {
    let Some(platform) = platform_tag() else {
        return Err(unsupported_platform_message(
            std::env::consts::OS,
            std::env::consts::ARCH,
        ));
    };
    let project = std::env::current_dir().ok();
    let version = opts
        .version
        .clone()
        .or_else(|| project.as_deref().and_then(pinned_version))
        .unwrap_or_else(|| DEFAULT_PG_VERSION.to_string());
    if version.contains('/') || version.contains("..") || version.trim().is_empty() {
        return Err(format!("sky db provision: {version:?} is not a version"));
    }

    let home = sky_home();
    let dest = home.join("postgres").join(&version);
    let bin_dir = dest.join("bin");

    // Idempotent: already there is a success, and reaches no network at all.
    if !opts.force && bundle_is_complete(&bin_dir) {
        maybe_pin(opts, project.as_deref(), &version);
        return Ok(Outcome::AlreadyPresent { version, bin_dir });
    }

    let staging_root = home.join(STAGING_DIR);
    prune_stale_staging(&staging_root);
    std::fs::create_dir_all(&staging_root)
        .map_err(|e| format!("sky db provision: cannot create {}: {e}", staging_root.display()))?;
    check_free_space(&staging_root)?;
    let work = staging_root.join(format!("{version}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&work);
    std::fs::create_dir_all(&work)
        .map_err(|e| format!("sky db provision: cannot create {}: {e}", work.display()))?;
    let guard = Scratch(work.clone());

    let asset = asset_name(&version, platform);
    let (archive, source, expected) = match &opts.from {
        Some(local) => {
            let archive = local.clone();
            if !archive.is_file() {
                return Err(format!(
                    "sky db provision: no such bundle archive: {}",
                    archive.display()
                ));
            }
            let expected = match &opts.checksum {
                Some(c) => c.trim().to_ascii_lowercase(),
                // A sibling SHA256SUMS is how the release publishes checksums, so a
                // copied-across release directory just works.
                None => local_sums_entry(&archive)?,
            };
            (archive, local.display().to_string(), expected)
        }
        None => {
            let base = base_url(&version, opts.base_url.as_deref().or(env_base().as_deref()));
            let sums_url = format!("{base}/{CHECKSUM_FILE}");
            let sums_path = work.join(CHECKSUM_FILE);
            fetch(&sums_url, &sums_path, true).map_err(|e| {
                format!("{e}\n(the checksum manifest is fetched first — nothing is downloaded unverified)")
            })?;
            let sums = std::fs::read_to_string(&sums_path)
                .map_err(|e| format!("sky db provision: cannot read {}: {e}", sums_path.display()))?;
            let expected = parse_sha256sums(&sums, &asset).ok_or_else(|| {
                format!(
                    "sky db provision: {sums_url} does not list {asset}.\n\
                     That release does not carry a bundle for this platform."
                )
            })?;
            let url = format!("{base}/{asset}");
            let archive = work.join(&asset);
            println!("sky db provision: fetching {url}");
            fetch(&url, &archive, false)?;
            (archive, url, expected)
        }
    };

    // ── THE GATE ──────────────────────────────────────────────────────────
    // Hash the bytes that are on disk, and compare, BEFORE tar is ever invoked.
    // Order is the whole content of this check: verifying after extraction would
    // mean a corrupt archive had already written into the tree, and verifying the
    // bytes we *meant* to write rather than the ones that landed would mean a
    // truncated transfer passes.
    let actual = sha256_file(&archive)?;
    if actual != expected.to_ascii_lowercase() {
        if opts.from.is_none() {
            let _ = std::fs::remove_file(&archive);
        }
        return Err(checksum_mismatch_message(&source, &expected, &actual));
    }

    // Extract into staging — never into `postgres/`, which discovery reads.
    let tree = work.join("tree");
    std::fs::create_dir_all(&tree)
        .map_err(|e| format!("sky db provision: cannot create {}: {e}", tree.display()))?;
    let st = Command::new("tar")
        .arg("-xzf")
        .arg(&archive)
        .arg("-C")
        .arg(&tree)
        .arg("--strip-components=1")
        .status()
        .map_err(|e| format!("sky db provision: could not run tar ({e})"))?;
    if !st.success() {
        return Err(format!(
            "sky db provision: could not extract {} (tar exit {}).\n\
             The cache is untouched — nothing was installed.",
            archive.display(),
            st.code().map(|c| c.to_string()).unwrap_or_else(|| "signal".into())
        ));
    }
    if !bundle_is_complete(&tree.join("bin")) {
        return Err(format!(
            "sky db provision: the archive did not contain a usable bundle \
             (need bin/{}).\nThe cache is untouched — nothing was installed.",
            REQUIRED_BINS.join(", bin/")
        ));
    }

    // Atomic install: one rename, on the same filesystem.
    std::fs::create_dir_all(home.join("postgres"))
        .map_err(|e| format!("sky db provision: cannot create {}: {e}", home.join("postgres").display()))?;
    let displaced = dest.with_file_name(format!("{version}.replaced-{}", std::process::id()));
    if dest.exists() {
        std::fs::rename(&dest, &displaced)
            .map_err(|e| format!("sky db provision: cannot move aside {}: {e}", dest.display()))?;
    }
    match std::fs::rename(&tree, &dest) {
        Ok(()) => {
            let _ = std::fs::remove_dir_all(&displaced);
        }
        Err(e) => {
            // Put back whatever we displaced before reporting; a failed re-install
            // must not have deleted the working one.
            if displaced.exists() {
                let _ = std::fs::rename(&displaced, &dest);
            }
            // A concurrent provision that won the race left a complete tree; that
            // is the end state we wanted, so it is a success, not a collision.
            if bundle_is_complete(&bin_dir) {
                drop(guard);
                maybe_pin(opts, project.as_deref(), &version);
                return Ok(Outcome::AlreadyPresent { version, bin_dir });
            }
            return Err(format!(
                "sky db provision: cannot install into {}: {e}",
                dest.display()
            ));
        }
    }
    drop(guard);

    let pinned = maybe_pin(opts, project.as_deref(), &version);
    Ok(Outcome::Installed { version, bin_dir, pinned })
}

fn env_base() -> Option<String> {
    std::env::var("SKY_POSTGRES_BUNDLE_URL")
        .ok()
        .filter(|v| !v.trim().is_empty())
}

/// Record the pin. Best-effort by design: a successful install must not be
/// reported as a failure because the project directory is read-only or because
/// the command was run outside a project at all.
fn maybe_pin(opts: &Opts, project: Option<&Path>, version: &str) -> bool {
    if opts.no_pin {
        return false;
    }
    let Some(dir) = project else { return false };
    let path = dir.join("sky.toml");
    let Ok(text) = std::fs::read_to_string(&path) else {
        return false;
    };
    match pinned_sky_toml(&text, version) {
        None => true, // already pinned to this version
        Some(updated) => std::fs::write(&path, updated).is_ok(),
    }
}

/// The checksum for a locally-copied archive, taken from a sibling `SHA256SUMS`.
/// With neither that nor `--checksum` this REFUSES rather than installing
/// unverified bytes — an offline install is exactly where a silently corrupted
/// copy is most likely and least visible.
fn local_sums_entry(archive: &Path) -> Result<String, String> {
    let name = archive
        .file_name()
        .map(|n| n.to_string_lossy().to_string())
        .unwrap_or_default();
    let sums = archive.with_file_name(CHECKSUM_FILE);
    if let Ok(text) = std::fs::read_to_string(&sums) {
        if let Some(h) = parse_sha256sums(&text, &name) {
            return Ok(h);
        }
    }
    Err(format!(
        "sky db provision: no checksum for {name}.\n\
         An archive is never installed unverified. Either:\n\
         \x20 • put the release's {CHECKSUM_FILE} next to it (it must list {name}), or\n\
         \x20 • pass it explicitly:  --checksum <sha256>"
    ))
}

/// Delete scratch left by a provision that was killed. Staging is outside
/// `postgres/`, so this is only about disk, never about correctness — which is
/// why an hour-old floor is safe: it cannot reap a concurrent run's work.
fn prune_stale_staging(root: &Path) {
    let Ok(rd) = std::fs::read_dir(root) else {
        return;
    };
    for e in rd.flatten() {
        let stale = e
            .metadata()
            .and_then(|m| m.modified())
            .ok()
            .and_then(|t| t.elapsed().ok())
            .map(|age| age.as_secs() > 3600)
            .unwrap_or(false);
        if stale {
            let _ = std::fs::remove_dir_all(e.path());
        }
    }
}

fn check_free_space(dir: &Path) -> Result<(), String> {
    let Ok(out) = Command::new("df").arg("-Pm").arg(dir).output() else {
        return Ok(()); // no df: not a reason to refuse
    };
    let text = String::from_utf8_lossy(&out.stdout);
    let Some(free) = text
        .lines()
        .nth(1)
        .and_then(|l| l.split_whitespace().nth(3))
        .and_then(|f| f.parse::<u64>().ok())
    else {
        return Ok(());
    };
    if free < NEEDED_MB {
        return Err(format!(
            "sky db provision: only {free} MB free at {} — a PostgreSQL bundle needs \
             about {NEEDED_MB} MB to download and extract.",
            dir.display()
        ));
    }
    Ok(())
}

/// Removes the staging directory on every exit path, including `?`.
struct Scratch(PathBuf);

impl Drop for Scratch {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    // --- naming: consumed from P2b, not invented ---

    #[test]
    fn asset_and_tag_match_what_the_bundle_workflow_publishes() {
        assert_eq!(
            asset_name("18.6", "darwin-arm64"),
            "postgres-18.6-darwin-arm64.tar.gz"
        );
        assert_eq!(release_tag("18.6"), "postgres-bundle-v18.6");
        assert_eq!(
            base_url("18.6", None),
            "https://github.com/anzellai/sky/releases/download/postgres-bundle-v18.6"
        );
        assert_eq!(base_url("18.6", Some("http://h/x/")), "http://h/x");
    }

    /// The version this binary asks for and the version CI builds are two files
    /// apart; a bump to one and not the other is a 404 for every user, and the
    /// only place it could be caught is here.
    #[test]
    fn default_version_matches_the_bundle_build_script() {
        let script = Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../../../scripts/skydb/build-postgres-bundle.sh");
        let text = std::fs::read_to_string(&script)
            .unwrap_or_else(|e| panic!("cannot read {}: {e}", script.display()));
        let line = text
            .lines()
            .find(|l| l.trim_start().starts_with("PG_VERSION="))
            .expect("build-postgres-bundle.sh no longer sets PG_VERSION");
        assert!(
            line.contains(DEFAULT_PG_VERSION),
            "sky pins PostgreSQL {DEFAULT_PG_VERSION} but the bundle build script says: {line}"
        );
    }

    #[test]
    fn platforms_are_exactly_the_four_the_matrix_builds() {
        assert_eq!(platform_tag_for("linux", "x86_64"), Some("linux-amd64"));
        assert_eq!(platform_tag_for("linux", "aarch64"), Some("linux-arm64"));
        assert_eq!(platform_tag_for("macos", "x86_64"), Some("darwin-amd64"));
        assert_eq!(platform_tag_for("macos", "aarch64"), Some("darwin-arm64"));
        assert_eq!(platform_tag_for("windows", "x86_64"), None);
        assert_eq!(platform_tag_for("freebsd", "x86_64"), None);
    }

    #[test]
    fn an_unsupported_platform_names_a_way_out_not_just_a_refusal() {
        let m = unsupported_platform_message("windows", "x86_64");
        assert!(m.contains("SKY_POSTGRES_BIN"), "{m}");
        assert!(m.contains("out of scope"), "{m}");
        let m2 = unsupported_platform_message("freebsd", "x86_64");
        assert!(!m2.contains("MSVC"), "{m2}");
        assert!(m2.contains("SKY_POSTGRES_BIN"), "{m2}");
    }

    // --- the checksum manifest ---

    #[test]
    fn sha256sums_are_parsed_in_sha256sum_format() {
        let a = "a".repeat(64);
        let b = "b".repeat(64);
        let text = format!(
            "{a}  postgres-18.6-linux-amd64.tar.gz\n\
             {b}  postgres-18.6-darwin-arm64.tar.gz\n\
             {}  sbom-linux-amd64.json\n",
            "c".repeat(64)
        );
        assert_eq!(
            parse_sha256sums(&text, "postgres-18.6-darwin-arm64.tar.gz"),
            Some(b)
        );
        assert_eq!(parse_sha256sums(&text, "postgres-18.6-linux-arm64.tar.gz"), None);
    }

    #[test]
    fn binary_mode_stars_and_uppercase_hashes_are_accepted() {
        let up = "A".repeat(64);
        let text = format!("{up} *postgres-18.6-linux-amd64.tar.gz\n");
        assert_eq!(
            parse_sha256sums(&text, "postgres-18.6-linux-amd64.tar.gz"),
            Some("a".repeat(64))
        );
    }

    #[test]
    fn a_manifest_line_that_is_not_a_digest_is_not_believed() {
        let text = "notahash  postgres-18.6-linux-amd64.tar.gz\n";
        assert_eq!(parse_sha256sums(text, "postgres-18.6-linux-amd64.tar.gz"), None);
    }

    // --- SHA-256 ---

    #[test]
    fn sha256_matches_the_nist_vectors() {
        assert_eq!(
            sha256_hex(b""),
            "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
        );
        assert_eq!(
            sha256_hex(b"abc"),
            "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
        );
        assert_eq!(
            sha256_hex(b"abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq"),
            "248d6a61d20638b8e5c026930c3e6039a33ce45964ff2167f6ecedd419db06c1"
        );
        // A million 'a's — exercises the multi-block + length-encoding paths that
        // a short vector never reaches.
        let mut h = Sha256::new();
        for _ in 0..1000 {
            h.update(&[b'a'; 1000]);
        }
        assert_eq!(
            hex(&h.finish()),
            "cdc76e5c9914fb9281a1c7e284d73e67f1809a48a497200e046d39ccc7112cd0"
        );
    }

    /// Chunked updates must produce the same digest as one shot — the file path
    /// hashes in 64 KB reads, and a buffering bug there would make every
    /// comparison wrong in the same direction (and so, potentially, agree).
    #[test]
    fn chunked_and_whole_digests_agree() {
        let data: Vec<u8> = (0..5000u32).map(|i| (i % 251) as u8).collect();
        let one = sha256_hex(&data);
        for chunk in [1usize, 7, 63, 64, 65, 1024] {
            let mut h = Sha256::new();
            for part in data.chunks(chunk) {
                h.update(part);
            }
            assert_eq!(hex(&h.finish()), one, "chunk size {chunk}");
        }
    }

    #[test]
    fn a_mismatch_message_names_both_digests_and_the_source() {
        let m = checksum_mismatch_message("http://x/a.tar.gz", "aa", "bb");
        assert!(m.contains("http://x/a.tar.gz") && m.contains("aa") && m.contains("bb"), "{m}");
        assert!(m.contains("Nothing has been extracted"), "{m}");
    }

    #[test]
    fn no_network_and_a_404_are_different_messages() {
        let offline = curl_failure_message("http://x/a", Some(6));
        assert!(offline.contains("--from"), "{offline}");
        assert!(offline.contains("no network"), "{offline}");
        let http = curl_failure_message("http://x/a", Some(22));
        assert!(!http.contains("no network"), "{http}");
        assert!(http.contains("releases"), "{http}");
    }

    // --- the pin ---

    #[test]
    fn the_pin_is_written_into_an_existing_database_section() {
        let src = "name = \"app\"\n\n[database]\nembedded = true\ndriver = \"postgres\"\n\n[live]\nport = 8000\n";
        let out = pinned_sky_toml(src, "18.6").expect("should have changed");
        assert!(out.contains("[database]\npostgresVersion = \"18.6\"\nembedded = true"), "{out}");
        assert!(out.contains("[live]\nport = 8000"), "{out}");
        // idempotent: the second write is a no-op
        assert_eq!(pinned_sky_toml(&out, "18.6"), None);
    }

    #[test]
    fn an_existing_pin_is_replaced_not_duplicated() {
        let src = "[database]\npostgresVersion = \"17.2\"\nembedded = true\n";
        let out = pinned_sky_toml(src, "18.6").unwrap();
        assert!(out.contains("postgresVersion = \"18.6\""), "{out}");
        assert!(!out.contains("17.2"), "{out}");
        assert_eq!(out.matches("postgresVersion").count(), 1, "{out}");
    }

    #[test]
    fn a_project_with_no_database_section_gains_one() {
        let src = "name = \"app\"\nentry = \"src/Main.sky\"\n";
        let out = pinned_sky_toml(src, "18.6").unwrap();
        assert!(out.ends_with("[database]\npostgresVersion = \"18.6\"\n"), "{out}");
        assert!(out.starts_with("name = \"app\""), "{out}");
    }

    /// `[database]` last in the file, with the key absent, must not append the
    /// key after a LATER section — there isn't one, but the same walk handles
    /// both and this is the arm that would silently write into `[live]`.
    #[test]
    fn a_key_added_to_a_mid_file_database_section_stays_in_it() {
        let src = "[database]\ndriver = \"postgres\"\n\n[live]\nport = 1\n";
        let out = pinned_sky_toml(src, "18.6").unwrap();
        let db = out.find("[database]").unwrap();
        let live = out.find("[live]").unwrap();
        let pin = out.find("postgresVersion").unwrap();
        assert!(db < pin && pin < live, "{out}");
    }

    // --- args ---

    #[test]
    fn embed_is_required_and_checksums_are_validated_up_front() {
        let a = |v: &[&str]| v.iter().map(|s| s.to_string()).collect::<Vec<_>>();
        assert!(parse_args(&a(&[])).is_err());
        assert!(parse_args(&a(&["--embed"])).is_ok());
        assert!(parse_args(&a(&["--embed", "--checksum", "nope"])).is_err());
        assert!(parse_args(&a(&["--embed", "--checksum", &"a".repeat(64)])).is_ok());
        assert!(parse_args(&a(&["--embed", "--version"])).is_err());
        assert!(parse_args(&a(&["--embed", "--wat"])).is_err());
        let o = parse_args(&a(&["--embed", "--version", "18.6", "--force"])).unwrap();
        assert_eq!(o.version.as_deref(), Some("18.6"));
        assert!(o.force);
    }
}

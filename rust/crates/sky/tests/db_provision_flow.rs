//! End-to-end regression for `sky db provision --embed` (embedded-Postgres
//! phase 3).
//!
//! The unit tests in `db_provision.rs` prove the *derivations* — the asset name,
//! the manifest parse, the digest against the NIST vectors, the sky.toml edit.
//! They cannot prove the ORDER of operations, and the order is the whole content
//! of this feature: a checksum computed after extraction, or over the bytes we
//! meant to write rather than the ones that landed, is a check that passes while
//! a corrupt bundle is installed. So this file drives the REAL `sky` binary
//! against a REAL HTTP server (through the real `curl` path) and asserts what is
//! on disk afterwards.
//!
//! No real release artifact is fetched: the bundle here is a stand-in with
//! executable stubs where `initdb`/`pg_ctl`/`postgres` go, laid out exactly as
//! `scripts/skydb/build-postgres-bundle.sh` lays out a real one
//! (`postgres-<version>-<platform>/{bin,lib,share}` inside the tar, with a
//! `SHA256SUMS` beside it). Everything sky does with it — fetch, verify,
//! extract, rename, pin, discover — is the production path.

use std::io::{Read, Write};
use std::net::{TcpListener, TcpStream};
use std::path::{Path, PathBuf};
use std::process::{Command, Output};
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;

const SKY: &str = env!("CARGO_BIN_EXE_sky");

/// A version no real machine has provisioned, so a stray `~/.sky` or a sibling
/// test cannot make one of these pass by accident.
const V: &str = "99.1";

fn unique(tag: &str) -> String {
    format!(
        "sky-prov-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    )
}

fn stdout(o: &Output) -> String {
    String::from_utf8_lossy(&o.stdout).to_string()
}
fn stderr(o: &Output) -> String {
    String::from_utf8_lossy(&o.stderr).to_string()
}
fn both(o: &Output) -> String {
    format!("{}{}", stdout(o), stderr(o))
}

/// The platform string the running binary will ask for. Mirrors
/// `db_provision::platform_tag`; the test has to name the same asset the binary
/// will request, and there is no lib target to share the function through.
fn platform() -> &'static str {
    match (std::env::consts::OS, std::env::consts::ARCH) {
        ("linux", "x86_64") => "linux-amd64",
        ("linux", "aarch64") => "linux-arm64",
        ("macos", "x86_64") => "darwin-amd64",
        ("macos", "aarch64") => "darwin-arm64",
        (os, arch) => panic!("this test does not run on {os}/{arch}"),
    }
}

/// sha256 via the system tool, deliberately NOT via sky's own implementation:
/// a fixture that computed the expected value with the code under test could
/// only ever prove that code agrees with itself.
fn system_sha256(path: &Path) -> String {
    for (bin, args) in [("sha256sum", vec![]), ("shasum", vec!["-a", "256"])] {
        let Ok(out) = Command::new(bin).args(&args).arg(path).output() else {
            continue;
        };
        if out.status.success() {
            let text = String::from_utf8_lossy(&out.stdout).to_string();
            if let Some(h) = text.split_whitespace().next() {
                return h.to_ascii_lowercase();
            }
        }
    }
    panic!("neither sha256sum nor shasum is available to check the fixture");
}

// ---- the stand-in bundle -------------------------------------------------

/// Build `postgres-<V>-<platform>.tar.gz` + `SHA256SUMS` into `dir`, laid out
/// the way the bundle build script lays out a real one. `bulk_mb` pads the tree
/// so extraction takes long enough to be interrupted on purpose.
fn build_bundle(dir: &Path, bulk_mb: usize) -> PathBuf {
    let name = format!("postgres-{V}-{}", platform());
    let stage = dir.join("stage");
    let tree = stage.join(&name);
    std::fs::create_dir_all(tree.join("bin")).unwrap();
    std::fs::create_dir_all(tree.join("lib")).unwrap();
    std::fs::create_dir_all(tree.join("share").join("extension")).unwrap();

    // `initdb`, `pg_ctl` and `postgres` are what discovery requires and what
    // `sky db start` interrogates. These stubs answer `--version` and, for
    // initdb, produce a data directory shaped enough for the next step.
    write_exec(
        &tree.join("bin").join("pg_ctl"),
        &format!(
            "#!/bin/sh\n\
             case \"$1\" in --version) echo 'pg_ctl (PostgreSQL) {V}'; exit 0;; esac\n\
             exit 1\n"
        ),
    );
    write_exec(
        &tree.join("bin").join("initdb"),
        &format!("#!/bin/sh\necho 'initdb (PostgreSQL) {V}'\nexit 1\n"),
    );
    write_exec(
        &tree.join("bin").join("postgres"),
        &format!("#!/bin/sh\necho 'postgres (PostgreSQL) {V}'\n"),
    );
    std::fs::write(tree.join("share").join("postgres.bki"), b"stub").unwrap();
    std::fs::write(
        tree.join("share").join("extension").join("vector.control"),
        b"stub",
    )
    .unwrap();
    std::fs::write(tree.join("BUNDLE.json"), format!("{{\"postgres_version\":\"{V}\"}}")).unwrap();

    if bulk_mb > 0 {
        // Incompressible, so the archive is genuinely of this size and gzip
        // cannot make the extraction instantaneous.
        let block: Vec<u8> = (0..1024 * 1024u32).map(|i| (i.wrapping_mul(2654435761) >> 13) as u8).collect();
        for i in 0..bulk_mb {
            std::fs::write(tree.join("lib").join(format!("bulk-{i}.so")), &block).unwrap();
        }
    }

    let archive = dir.join(format!("{name}.tar.gz"));
    let st = Command::new("tar")
        .arg("-czf")
        .arg(&archive)
        .arg("-C")
        .arg(&stage)
        .arg(&name)
        .status()
        .unwrap();
    assert!(st.success(), "could not build the fixture archive");
    std::fs::remove_dir_all(&stage).unwrap();
    write_sums(dir, &archive, &system_sha256(&archive));
    archive
}

fn write_sums(dir: &Path, archive: &Path, hash: &str) {
    std::fs::write(
        dir.join("SHA256SUMS"),
        format!(
            "{hash}  {}\n{}  sbom-{}.json\n",
            archive.file_name().unwrap().to_string_lossy(),
            "0".repeat(64),
            platform()
        ),
    )
    .unwrap();
}

fn write_exec(path: &Path, body: &str) {
    std::fs::write(path, body).unwrap();
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o755)).unwrap();
    }
}

// ---- a local HTTP server -------------------------------------------------

/// Serves `dir` over HTTP and counts requests, so "provisioning twice does not
/// download twice" can be asserted as a fact about the wire rather than inferred
/// from a timing or a log line.
struct Server {
    port: u16,
    hits: Arc<AtomicUsize>,
    stop: Arc<AtomicUsize>,
}

impl Server {
    fn start(dir: PathBuf) -> Server {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let port = listener.local_addr().unwrap().port();
        let hits = Arc::new(AtomicUsize::new(0));
        let stop = Arc::new(AtomicUsize::new(0));
        let (h, s) = (hits.clone(), stop.clone());
        std::thread::spawn(move || {
            for conn in listener.incoming() {
                if s.load(Ordering::SeqCst) == 1 {
                    return;
                }
                let Ok(conn) = conn else { continue };
                h.fetch_add(1, Ordering::SeqCst);
                let _ = serve_one(conn, &dir);
            }
        });
        Server { port, hits, stop }
    }

    fn base(&self) -> String {
        format!("http://127.0.0.1:{}", self.port)
    }

    fn hits(&self) -> usize {
        self.hits.load(Ordering::SeqCst)
    }
}

impl Drop for Server {
    fn drop(&mut self) {
        self.stop.store(1, Ordering::SeqCst);
        // Unblock the accept loop.
        let _ = TcpStream::connect(("127.0.0.1", self.port));
    }
}

fn serve_one(mut conn: TcpStream, dir: &Path) -> std::io::Result<()> {
    let mut buf = [0u8; 2048];
    let n = conn.read(&mut buf)?;
    let head = String::from_utf8_lossy(&buf[..n]).to_string();
    let path = head
        .split_whitespace()
        .nth(1)
        .unwrap_or("/")
        .trim_start_matches('/')
        .to_string();
    // No traversal: the fixture only ever asks for a flat name.
    let file = dir.join(path.split('/').next_back().unwrap_or(""));
    match std::fs::read(&file) {
        Ok(body) => {
            conn.write_all(
                format!(
                    "HTTP/1.1 200 OK\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
                    body.len()
                )
                .as_bytes(),
            )?;
            conn.write_all(&body)?;
        }
        Err(_) => {
            conn.write_all(b"HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")?;
        }
    }
    conn.flush()
}

// ---- fixture -------------------------------------------------------------

struct Fx {
    project: PathBuf,
    sky_home: PathBuf,
    serve_dir: PathBuf,
    root: PathBuf,
}

impl Fx {
    fn new(tag: &str) -> Fx {
        let root = std::env::temp_dir().join(unique(tag));
        let project = root.join("project");
        std::fs::create_dir_all(project.join("src")).unwrap();
        std::fs::write(
            project.join("sky.toml"),
            "name = \"prov\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n\
             [database]\nembedded = true\n",
        )
        .unwrap();
        let serve_dir = root.join("release");
        std::fs::create_dir_all(&serve_dir).unwrap();
        Fx {
            sky_home: root.join("home"),
            project,
            serve_dir,
            root,
        }
    }

    fn sky(&self, args: &[&str], base: Option<&str>) -> Output {
        let mut c = Command::new(SKY);
        c.args(args)
            .current_dir(&self.project)
            .env("SKY_HOME", &self.sky_home)
            .env_remove("SKY_POSTGRES_BIN")
            .env_remove("SKY_POSTGRES_BUNDLE_URL");
        if let Some(b) = base {
            c.env("SKY_POSTGRES_BUNDLE_URL", b);
        }
        c.output().expect("failed to run sky")
    }

    fn cache_bin(&self) -> PathBuf {
        self.sky_home.join("postgres").join(V).join("bin")
    }

    fn cache_is_complete(&self) -> bool {
        ["initdb", "pg_ctl", "postgres"]
            .iter()
            .all(|b| self.cache_bin().join(b).is_file())
    }

    fn sky_toml(&self) -> String {
        std::fs::read_to_string(self.project.join("sky.toml")).unwrap()
    }
}

impl Drop for Fx {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.root);
    }
}

// ---- the happy path ------------------------------------------------------

/// A real download (real curl, real socket), verified, installed atomically,
/// pinned — and then actually found by the discovery `sky db start` uses.
#[test]
fn a_bundle_is_downloaded_verified_installed_and_pinned() {
    let fx = Fx::new("happy");
    build_bundle(&fx.serve_dir, 0);
    let srv = Server::start(fx.serve_dir.clone());

    let out = fx.sky(&["db", "provision", "--embed", "--version", V], Some(&srv.base()));
    assert!(out.status.success(), "provision failed:\n{}", both(&out));
    assert!(fx.cache_is_complete(), "the bundle is not in the cache:\n{}", both(&out));

    // The executable bit survives the install. A bundle whose `postgres` arrives
    // mode 0444 looks provisioned and cannot be run — P5a hit exactly that on the
    // go:embed path, and the discovery here would happily select it.
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let mode = std::fs::metadata(fx.cache_bin().join("postgres")).unwrap().permissions().mode();
        assert!(mode & 0o111 != 0, "postgres came out non-executable (mode {mode:o})");
    }

    // The pin is recorded, in the section that already owns the database config.
    let toml = fx.sky_toml();
    assert!(toml.contains(&format!("postgresVersion = \"{V}\"")), "{toml}");
    assert!(toml.contains("[database]"), "{toml}");
    assert!(toml.contains("embedded = true"), "the pin clobbered the rest of sky.toml:\n{toml}");

    // Nothing is left in scratch.
    let staging = fx.sky_home.join(".provision-tmp");
    assert!(
        !staging.exists() || std::fs::read_dir(&staging).unwrap().next().is_none(),
        "provision left scratch behind in {}",
        staging.display()
    );

    // And the whole point: `sky db start` now finds it without a system
    // PostgreSQL. PATH is emptied so the ONLY candidate is the cache.
    let start = Command::new(SKY)
        .args(["db", "start"])
        .current_dir(&fx.project)
        .env("SKY_HOME", &fx.sky_home)
        .env_remove("SKY_POSTGRES_BIN")
        .env("PATH", fx.root.join("empty"))
        .output()
        .unwrap();
    let msg = both(&start);
    assert!(
        !msg.contains("no PostgreSQL binaries found"),
        "the provisioned cache is not on the discovery path:\n{msg}"
    );
    assert!(
        msg.contains(&format!("PostgreSQL {}", V.split('.').next().unwrap())),
        "discovery did not interrogate the provisioned binaries:\n{msg}"
    );
    drop(srv);
}

// ---- THE gate: a corrupt archive is never installed ----------------------

/// The single most important property here. The manifest names the checksum of
/// the bundle that was built; the bytes served are not that bundle. Verifying in
/// the wrong order — or over the wrong bytes — is the classic silent pass, and
/// it looks identical to a working implementation from the outside.
#[test]
fn a_corrupted_download_is_rejected_and_nothing_is_extracted() {
    let fx = Fx::new("corrupt");
    let archive = build_bundle(&fx.serve_dir, 0);
    let good = system_sha256(&archive);

    // Corrupt the served bytes, leaving SHA256SUMS naming the good bundle. This
    // is what a truncated transfer or a tampered mirror produces.
    let mut bytes = std::fs::read(&archive).unwrap();
    bytes.truncate(bytes.len() / 2);
    std::fs::write(&archive, &bytes).unwrap();
    assert_ne!(system_sha256(&archive), good, "the fixture did not actually corrupt anything");

    let srv = Server::start(fx.serve_dir.clone());
    let out = fx.sky(&["db", "provision", "--embed", "--version", V], Some(&srv.base()));

    assert!(!out.status.success(), "a corrupt bundle was accepted:\n{}", both(&out));
    let msg = stderr(&out);
    assert!(msg.contains("CHECKSUM MISMATCH"), "{msg}");
    assert!(msg.contains(&good), "the message must name what was expected:\n{msg}");

    // Nothing extracted, nothing staged, nothing pinned.
    assert!(
        !fx.sky_home.join("postgres").join(V).exists(),
        "a corrupt bundle reached the cache"
    );
    assert!(!fx.cache_is_complete());
    let staging = fx.sky_home.join(".provision-tmp");
    assert!(
        !staging.exists() || std::fs::read_dir(&staging).unwrap().next().is_none(),
        "a corrupt bundle left an extracted tree in {}",
        staging.display()
    );
    assert!(
        !fx.sky_toml().contains("postgresVersion"),
        "a failed provision pinned a version anyway:\n{}",
        fx.sky_toml()
    );
    drop(srv);
}

/// The same refusal for a locally-supplied archive: `--from` with an explicit
/// `--checksum` that does not match must not install, and `--from` with NO
/// checksum available at all must refuse rather than install unverified bytes.
#[test]
fn a_local_archive_is_verified_too_and_is_never_installed_unchecked() {
    let fx = Fx::new("local");
    let archive = build_bundle(&fx.serve_dir, 0);
    let good = system_sha256(&archive);

    // (a) No checksum anywhere → refuse, and say how to supply one.
    let lone = fx.root.join("lonely");
    std::fs::create_dir_all(&lone).unwrap();
    let copied = lone.join(archive.file_name().unwrap());
    std::fs::copy(&archive, &copied).unwrap();
    let out = fx.sky(
        &["db", "provision", "--embed", "--version", V, "--from", copied.to_str().unwrap()],
        None,
    );
    assert!(!out.status.success(), "installed without any checksum:\n{}", both(&out));
    assert!(stderr(&out).contains("--checksum"), "{}", stderr(&out));
    assert!(!fx.cache_is_complete());

    // (b) A wrong explicit checksum → refuse.
    let out = fx.sky(
        &[
            "db", "provision", "--embed", "--version", V,
            "--from", copied.to_str().unwrap(),
            "--checksum", &"a".repeat(64),
        ],
        None,
    );
    assert!(!out.status.success(), "installed against a wrong checksum:\n{}", both(&out));
    assert!(stderr(&out).contains("CHECKSUM MISMATCH"), "{}", stderr(&out));
    assert!(!fx.cache_is_complete());

    // (c) The right one → installed, offline, with no server anywhere.
    let out = fx.sky(
        &[
            "db", "provision", "--embed", "--version", V,
            "--from", copied.to_str().unwrap(),
            "--checksum", &good,
        ],
        None,
    );
    assert!(out.status.success(), "offline install failed:\n{}", both(&out));
    assert!(fx.cache_is_complete());
    // The local archive is the user's file and must survive being installed from.
    assert!(copied.is_file(), "--from consumed the user's archive");
}

/// The release's own `SHA256SUMS`, copied alongside the archive, is enough — an
/// air-gapped install is "copy the release directory across", not "find the hash
/// by hand".
#[test]
fn a_sibling_sha256sums_is_enough_for_an_offline_install() {
    let fx = Fx::new("sibling");
    let archive = build_bundle(&fx.serve_dir, 0);
    let out = fx.sky(
        &["db", "provision", "--embed", "--version", V, "--from", archive.to_str().unwrap()],
        None,
    );
    assert!(out.status.success(), "{}", both(&out));
    assert!(fx.cache_is_complete());
}

// ---- interrupted installs ------------------------------------------------

/// An extract that dies partway must leave NOTHING that discovery would find.
/// Deterministic version: the archive is truncated *and* its manifest names the
/// truncated bytes, so the checksum passes and `tar` is the thing that fails —
/// exactly the state a kill during extraction produces.
#[test]
fn an_extract_that_fails_partway_leaves_no_usable_cache() {
    let fx = Fx::new("partial");
    let archive = build_bundle(&fx.serve_dir, 8);
    let mut bytes = std::fs::read(&archive).unwrap();
    bytes.truncate(bytes.len() / 3);
    std::fs::write(&archive, &bytes).unwrap();
    // The manifest agrees with the corrupt bytes: the checksum gate passes and
    // the failure lands in tar.
    write_sums(&fx.serve_dir, &archive, &system_sha256(&archive));

    let srv = Server::start(fx.serve_dir.clone());
    let out = fx.sky(&["db", "provision", "--embed", "--version", V], Some(&srv.base()));
    assert!(!out.status.success(), "a broken archive reported success:\n{}", both(&out));

    assert!(
        !fx.sky_home.join("postgres").join(V).exists(),
        "a half-extracted tree was installed into the cache"
    );
    let staging = fx.sky_home.join(".provision-tmp");
    assert!(
        !staging.exists() || std::fs::read_dir(&staging).unwrap().next().is_none(),
        "the failed extract was left in {}",
        staging.display()
    );

    // And discovery agrees there is nothing here — which is the failure mode
    // this is really about: a half-populated bin dir that `sky db start` finds
    // and then fails on, confusingly, much later.
    let start = Command::new(SKY)
        .args(["db", "start"])
        .current_dir(&fx.project)
        .env("SKY_HOME", &fx.sky_home)
        .env_remove("SKY_POSTGRES_BIN")
        .env("PATH", fx.root.join("empty"))
        .output()
        .unwrap();
    assert!(
        stderr(&start).contains("no PostgreSQL binaries found"),
        "discovery found something in a cache that must be empty:\n{}",
        both(&start)
    );
    drop(srv);
}

/// The real thing: SIGKILL a provision in flight. The invariant asserted is the
/// one that matters and that holds wherever the kill lands — the cache entry is
/// either complete or absent, never half-populated.
#[test]
fn a_killed_provision_never_leaves_a_half_populated_cache() {
    let fx = Fx::new("killed");
    build_bundle(&fx.serve_dir, 24);
    let srv = Server::start(fx.serve_dir.clone());

    let mut child = Command::new(SKY)
        .args(["db", "provision", "--embed", "--version", V])
        .current_dir(&fx.project)
        .env("SKY_HOME", &fx.sky_home)
        .env("SKY_POSTGRES_BUNDLE_URL", srv.base())
        .env_remove("SKY_POSTGRES_BIN")
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .spawn()
        .unwrap();

    // Kill once EXTRACTION has started — not merely once the download has —
    // because unpacking is the phase that can leave a tree behind. Polled rather
    // than slept, so the window does not depend on the machine.
    let staging = fx.sky_home.join(".provision-tmp");
    let dest = fx.sky_home.join("postgres").join(V);
    let extracting = || {
        if dest.exists() {
            return true;
        }
        std::fs::read_dir(&staging)
            .map(|rd| rd.flatten().any(|e| e.path().join("tree").exists()))
            .unwrap_or(false)
    };
    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(120);
    while !extracting() {
        if std::time::Instant::now() > deadline {
            let _ = child.kill();
            panic!("provision never reached extraction");
        }
        std::thread::sleep(std::time::Duration::from_millis(1));
    }
    let _ = child.kill();
    let _ = child.wait();

    if dest.exists() {
        // The kill landed after the rename: the install completed, and what is
        // there must be a whole bundle.
        assert!(
            fx.cache_is_complete(),
            "the cache holds a partial {} — discovery would select it and fail later",
            dest.display()
        );
    }
    // Either way, nothing partial may sit under postgres/ where discovery looks.
    if let Ok(rd) = std::fs::read_dir(fx.sky_home.join("postgres")) {
        for e in rd.flatten() {
            let bin = e.path().join("bin");
            assert!(
                ["initdb", "pg_ctl", "postgres"].iter().all(|b| bin.join(b).is_file()),
                "a partial install is visible to discovery at {}",
                e.path().display()
            );
        }
    }
    drop(srv);
}

// ---- idempotency ---------------------------------------------------------

/// Provisioning what is already provisioned must be a fast success that touches
/// the network zero times. Asserted against the server's own request count, not
/// inferred.
#[test]
fn re_provisioning_is_a_no_op_that_makes_no_request() {
    let fx = Fx::new("idem");
    build_bundle(&fx.serve_dir, 0);
    let srv = Server::start(fx.serve_dir.clone());

    let first = fx.sky(&["db", "provision", "--embed", "--version", V], Some(&srv.base()));
    assert!(first.status.success(), "{}", both(&first));
    let after_first = srv.hits();
    assert!(after_first >= 2, "expected a manifest + an archive request, saw {after_first}");

    let mtime = |p: &Path| std::fs::metadata(p).unwrap().modified().unwrap();
    let before = mtime(&fx.cache_bin().join("postgres"));

    let second = fx.sky(&["db", "provision", "--embed", "--version", V], Some(&srv.base()));
    assert!(second.status.success(), "{}", both(&second));
    assert!(
        stdout(&second).contains("already provisioned"),
        "{}",
        stdout(&second)
    );
    assert_eq!(
        srv.hits(),
        after_first,
        "re-provisioning went back to the network"
    );
    assert_eq!(
        mtime(&fx.cache_bin().join("postgres")),
        before,
        "re-provisioning rewrote the cache"
    );

    // Even with the server gone entirely, it still succeeds.
    drop(srv);
    let third = fx.sky(&["db", "provision", "--embed", "--version", V], Some("http://127.0.0.1:1"));
    assert!(third.status.success(), "offline re-provision failed:\n{}", both(&third));
}

/// A cached tree whose binaries are not executable is NOT provisioned. It looks
/// provisioned to a file-exists check, and discovery would then select a
/// `postgres` that cannot be run — P5a hit exactly this class on the `go:embed`
/// path, where every embedded file arrives mode 0444.
#[test]
#[cfg(unix)]
fn a_non_executable_cached_bundle_is_not_mistaken_for_a_provisioned_one() {
    use std::os::unix::fs::PermissionsExt;
    let fx = Fx::new("mode");
    build_bundle(&fx.serve_dir, 0);
    std::fs::create_dir_all(fx.cache_bin()).unwrap();
    for b in ["initdb", "pg_ctl", "postgres"] {
        let p = fx.cache_bin().join(b);
        std::fs::write(&p, "#!/bin/sh\nexit 0\n").unwrap();
        std::fs::set_permissions(&p, std::fs::Permissions::from_mode(0o644)).unwrap();
    }

    let srv = Server::start(fx.serve_dir.clone());
    let out = fx.sky(&["db", "provision", "--embed", "--version", V], Some(&srv.base()));
    assert!(out.status.success(), "{}", both(&out));
    assert!(
        !stdout(&out).contains("already provisioned"),
        "an unrunnable cache was reported as provisioned:\n{}",
        stdout(&out)
    );
    let mode = std::fs::metadata(fx.cache_bin().join("postgres")).unwrap().permissions().mode();
    assert!(mode & 0o111 != 0, "postgres is still not executable (mode {mode:o})");
    drop(srv);
}

// ---- refusals ------------------------------------------------------------

/// A release that carries no bundle for this platform must say so, not download
/// something else. The manifest is fetched FIRST, so this is decided before a
/// single byte of any archive is transferred.
#[test]
fn a_release_without_this_platforms_bundle_is_reported_not_guessed() {
    let fx = Fx::new("noasset");
    build_bundle(&fx.serve_dir, 0);
    // A manifest for a different platform only.
    std::fs::write(
        fx.serve_dir.join("SHA256SUMS"),
        format!("{}  postgres-{V}-somewhere-else.tar.gz\n", "b".repeat(64)),
    )
    .unwrap();

    let srv = Server::start(fx.serve_dir.clone());
    let out = fx.sky(&["db", "provision", "--embed", "--version", V], Some(&srv.base()));
    assert!(!out.status.success(), "{}", both(&out));
    let msg = stderr(&out);
    assert!(msg.contains("does not list"), "{msg}");
    assert!(msg.contains(&format!("postgres-{V}-{}.tar.gz", platform())), "{msg}");
    assert!(!fx.cache_is_complete());
    drop(srv);
}

/// No network at all: the message has to hand the reader the offline route
/// rather than a curl exit code.
#[test]
fn an_unreachable_release_names_the_offline_route() {
    let fx = Fx::new("offline");
    // Port 1 on loopback: connection refused, immediately, on every platform.
    let out = fx.sky(
        &["db", "provision", "--embed", "--version", V],
        Some("http://127.0.0.1:1"),
    );
    assert!(!out.status.success());
    let msg = stderr(&out);
    assert!(msg.contains("--from"), "the offline route is not named:\n{msg}");
    assert!(msg.contains("SKY_POSTGRES_BIN"), "{msg}");
    assert!(!fx.cache_is_complete());
}

/// `--embed` is the whole verb; a bare `sky db provision` must not guess.
#[test]
fn the_verb_requires_embed_and_prints_usage() {
    let fx = Fx::new("usage");
    let out = fx.sky(&["db", "provision"], None);
    assert_eq!(out.status.code(), Some(2), "{}", both(&out));
    assert!(stderr(&out).contains("--embed"), "{}", stderr(&out));
    assert!(stderr(&out).contains("usage:"), "{}", stderr(&out));
}

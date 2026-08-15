//! `sky db start` / `sky db stop` / `sky db ps` — the per-project PostgreSQL
//! cluster supervisor (embedded-Postgres phase 2, see
//! `docs/skydb/embedded-postgres.md`).
//!
//! The shape of the thing:
//!
//! * **One cluster per project**, data dir `.skydata/pg/` inside the project, so
//!   `rm -rf .skydata` resets exactly one project and two projects pinned to
//!   different PostgreSQL majors never fight.
//! * **A unix socket, never a TCP port.** Port allocation is the classic race
//!   between two `sky db start`s and the classic way a development database ends
//!   up reachable from the network. A socket has neither problem.
//! * **The socket lives OUTSIDE the project**, in a short hashed directory
//!   (`$XDG_RUNTIME_DIR/sky/<hash>/`, else `/tmp/sky-<hash>/`). This is not
//!   tidiness: `sockaddr_un.sun_path` is 108 bytes on Linux and 104 on macOS, so
//!   a socket inside a deeply-nested project overflows the kernel's limit and
//!   fails with an error that names neither the project nor the limit. The hash
//!   makes the path length independent of how deep the project is.
//! * **A machine-level registry** at `~/.sky/clusters.json` so `sky db ps --all`
//!   can see clusters this invocation did not start. Processes die without
//!   deregistering, so every read reaps: an entry whose data dir is gone is
//!   dropped, and an entry whose recorded pid is dead — or has been recycled by
//!   an unrelated process — is never reported as running.
//!
//! Where the binaries come from is deliberately *discovered*, not built: the
//! fetch-and-pin (`sky db provision --embed`) is phase 3. Precedence is
//! `SKY_POSTGRES_BIN` → `~/.sky/postgres/<version>/bin` → `PATH`.

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};
use std::process::{Command, ExitCode, Stdio};

use serde::{Deserialize, Serialize};

// ---- constants -----------------------------------------------------------

/// The three binaries a cluster cannot be supervised without. `psql` is *not*
/// required — it is a client convenience, and demanding it would reject an
/// otherwise perfectly usable server-only distribution.
pub const REQUIRED_BINS: [&str; 3] = ["initdb", "pg_ctl", "postgres"];

/// PostgreSQL names its socket `.s.PGSQL.<port>`. The port number survives in
/// the *filename* even for a socket-only cluster, and 5432 is the value every
/// client library assumes when it is given a socket directory and no port. The
/// directory is per-project, so there is nothing to collide with.
pub const SOCKET_BASENAME: &str = ".s.PGSQL.5432";

/// The longest socket path we will hand to PostgreSQL. The real kernel ceiling
/// is 107 bytes on Linux and 103 on macOS (`sizeof(sun_path) - 1`); this sits
/// well under both so that a future suffix — a second socket, a lock file that
/// PostgreSQL creates alongside (`.s.PGSQL.5432.lock`, five bytes longer) —
/// cannot silently push a working configuration over the edge.
pub const MAX_SOCKET_PATH: usize = 92;

/// Marker for the tuning block appended to a generated `postgresql.conf`.
/// Its presence is what makes `ensure_sky_conf` idempotent.
pub const SKY_CONF_MARKER: &str = "# --- sky db: development cluster tuning (managed by sky) ---";

const REGISTRY_FILE: &str = "clusters.json";
const LOCK_FILE: &str = "clusters.lock";
/// A lock older than this is assumed to belong to a process that died holding
/// it. Registry updates take milliseconds; a minute is not a close call.
const LOCK_STALE_SECS: u64 = 60;

// ---- hashing -------------------------------------------------------------

// FNV-1a, 128-bit. Chosen over `std::hash::DefaultHasher` deliberately:
// `DefaultHasher`'s output is explicitly NOT guaranteed stable across Rust
// releases, and this hash is *persisted* — it names a socket directory recorded
// in a registry that outlives the binary that wrote it. A rustc upgrade must
// not orphan every running cluster on the machine.
const FNV128_OFFSET: u128 = 0x6c62_272e_07bb_0142_62b8_2175_6295_c58d;
const FNV128_PRIME: u128 = 0x0000_0000_0100_0000_0000_0000_0000_013b;

fn fnv1a128(bytes: &[u8]) -> u128 {
    let mut h = FNV128_OFFSET;
    for b in bytes {
        h ^= u128::from(*b);
        h = h.wrapping_mul(FNV128_PRIME);
    }
    h
}

/// A stable 16-hex-character digest of a path. 64 bits of a 128-bit hash: a
/// machine would need on the order of 4 billion concurrent Sky projects before
/// two shared a socket directory.
///
/// Named `path_hash` rather than `project_hash` because the *project* is not
/// what is hashed. See [`socket_dir_for_data_dir`].
pub fn path_hash(p: &Path) -> String {
    let h = fnv1a128(p.as_os_str().as_encoded_bytes());
    format!("{:016x}", (h >> 64) as u64)
}

/// Resolve symlinks as far as the path exists, then re-append the components
/// that do not.
///
/// The socket-directory hash is taken over a path that is often only partly on
/// disk — `.skydata/pg` does not exist until the first `initdb` — so plain
/// `canonicalize` cannot be used. Both this and the Go side
/// (`resolvedPath` in `runtime-go/rt/pg_embed_bundle.go`) must agree, because
/// on macOS `/tmp/x` and `/private/tmp/x` are the same directory and hash
/// differently.
pub fn resolved_path(p: &Path) -> PathBuf {
    let mut tail: Vec<std::ffi::OsString> = Vec::new();
    let mut cur = p.to_path_buf();
    loop {
        if let Ok(c) = cur.canonicalize() {
            let mut out = c;
            for t in tail.iter().rev() {
                out.push(t);
            }
            return out;
        }
        let Some(name) = cur.file_name().map(|f| f.to_os_string()) else {
            return p.to_path_buf();
        };
        let Some(parent) = cur.parent().map(Path::to_path_buf) else {
            return p.to_path_buf();
        };
        if parent.as_os_str().is_empty() || parent == cur {
            return p.to_path_buf();
        }
        tail.push(name);
        cur = parent;
    }
}

// ---- socket path derivation ---------------------------------------------

/// Where the cluster serving `data_dir` listens.
///
/// **The hash input is the PostgreSQL DATA DIRECTORY, not the project.** This is
/// the one input, and the reason it is that one: `./app --embed --data-dir
/// /var/lib/app` has no project at all, so a project-keyed derivation cannot be
/// computed on the runtime side. Keying on the data directory also gives the
/// property that actually matters — one postmaster per data directory, one
/// socket per postmaster — which a project key loses the moment two data
/// directories live under one project.
///
/// P5b found the two sides disagreeing: Rust hashed the project path while Go
/// hashed `<dataRoot>/pg`, so `./app --embed` in a project whose cluster
/// `sky db start` had already brought up probed a socket directory that did not
/// exist, spent 60s in `waitReady` and exited 1 with a healthy postmaster
/// running throughout. The pinned-constant gates on both sides
/// (`the_socket_directory_for_a_pinned_project_is_a_pinned_constant` here,
/// `TestTheSocketDirectoryForAPinnedProjectIsAPinnedConstant` in Go) exist
/// because a test that compares the two implementations to each other can drift
/// together; a literal cannot.
///
/// `xdg_runtime_dir` and `fallback_base` are parameters rather than reads of the
/// ambient environment so the derivation — including the pathological-depth case
/// that motivates the whole design — is testable without touching process state.
///
/// The `XDG_RUNTIME_DIR` branch is itself length-checked: it is the *user's*
/// value, it is frequently a long per-session path, and a runtime dir that
/// cannot host a socket must degrade to `/tmp` rather than produce a cluster
/// that fails to bind.
pub fn socket_dir_for_data_dir(
    data_dir: &Path,
    xdg_runtime_dir: Option<&str>,
    fallback_base: &Path,
) -> PathBuf {
    let hash = path_hash(&resolved_path(data_dir));
    if let Some(xdg) = xdg_runtime_dir.map(str::trim).filter(|x| !x.is_empty()) {
        let base = Path::new(xdg);
        if base.is_absolute() {
            let cand = base.join("sky").join(&hash);
            if socket_path_len(&cand) <= MAX_SOCKET_PATH {
                return cand;
            }
        }
    }
    fallback_base.join(format!("sky-{hash}"))
}

/// The byte length of the socket FILE inside `socket_dir` — what the kernel
/// actually measures. Callers that check the directory length instead are off by
/// fourteen bytes, which is exactly the size of the mistake that makes this fail
/// on someone else's machine and not on yours.
pub fn socket_path_len(socket_dir: &Path) -> usize {
    socket_dir.join(SOCKET_BASENAME).as_os_str().as_encoded_bytes().len()
}

/// The project-shaped entry point: `sky db start` / `sky run` know a project,
/// and the data directory they derive from it (`<project>/.skydata/pg`) is the
/// same one `./app --embed` resolves from its own cwd. Funnelling both through
/// [`socket_dir_for_data_dir`] is what makes the two agree.
pub fn socket_dir_for_project(
    project: &Path,
    xdg_runtime_dir: Option<&str>,
    fallback_base: &Path,
) -> PathBuf {
    socket_dir_for_data_dir(&data_dir_for(project), xdg_runtime_dir, fallback_base)
}

/// The ambient-environment version of [`socket_dir_for_project`]. `/tmp` is
/// hard-coded as the fallback rather than `std::env::temp_dir()` because on
/// macOS the latter is a ~49-byte per-user `$TMPDIR` under `/var/folders/`,
/// which spends half the socket budget before the hash is even appended.
fn socket_dir_real(project: &Path) -> PathBuf {
    let xdg = std::env::var("XDG_RUNTIME_DIR").ok();
    let fallback = if cfg!(unix) {
        PathBuf::from("/tmp")
    } else {
        std::env::temp_dir()
    };
    socket_dir_for_project(project, xdg.as_deref(), &fallback)
}

// ---- registry ------------------------------------------------------------

/// One Sky-managed cluster as recorded on disk. Paths are stored as strings
/// because this is a wire format: a `PathBuf` that fails to round-trip through
/// JSON on a foreign platform would corrupt every other entry in the file.
#[derive(Serialize, Deserialize, Clone, Debug, PartialEq, Eq)]
pub struct ClusterEntry {
    pub data_dir: String,
    pub socket_dir: String,
    /// The postmaster pid as last observed. `0` means "known not running" — the
    /// value a reap writes back, so a dead pid is never reported as live.
    pub pid: i32,
    pub pg_version: String,
    #[serde(default)]
    pub started_at: u64,
    /// True when a user asked for this cluster by name (`sky db start`). An
    /// explicit cluster is PERSISTENT: `sky run` may use it, but must never take
    /// it down on the way out. That distinction is the whole reason the two verbs
    /// exist separately — one is ephemeral, one is the mode for running
    /// `./sky-out/app` repeatedly.
    #[serde(default)]
    pub explicit: bool,
    /// The live `sky run` / `sky watch` invocations currently depending on this
    /// cluster. Empty *and* not explicit is the only state in which a `sky run`
    /// exit stops the postmaster.
    #[serde(default)]
    pub refs: Vec<RunRef>,
}

/// One `sky run` (or `sky watch`) holding a project's cluster up.
///
/// A pid alone is not a reference. A `sky run` that is `SIGKILL`ed leaves its
/// entry behind, the kernel is free to hand that pid to something else, and a
/// ref believed on pid alone would then pin the cluster up for the rest of the
/// session — the same failure P2 already solved for `postmaster.pid`, in a new
/// place. So the holder's own `ps -o command=` line is recorded at acquire time
/// and must still match at check time.
#[derive(Serialize, Deserialize, Clone, Debug, PartialEq, Eq)]
pub struct RunRef {
    pub pid: i32,
    /// The holder's command line as `ps` reported it when the ref was taken.
    /// Empty when `ps` was unavailable, in which case aliveness is all we have.
    #[serde(default)]
    pub cmd: String,
    #[serde(default)]
    pub since: u64,
}

/// Drop every ref whose holder is gone — or whose pid now belongs to something
/// else entirely.
///
/// The liveness predicate is injected so the recycled-pid case can be tested
/// without arranging for the operating system to actually reuse a pid, which is
/// not something a test can make happen on demand.
pub fn prune_refs(refs: &[RunRef], live: &dyn Fn(&RunRef) -> bool) -> Vec<RunRef> {
    refs.iter().filter(|r| live(r)).cloned().collect()
}

/// The real predicate behind [`prune_refs`]: both legs, in the same order and
/// with the same degradation as [`is_postgres_process`].
fn ref_is_live(r: &RunRef) -> bool {
    if !process_alive(r.pid) {
        return false;
    }
    match process_command(r.pid) {
        // A different command at the same pid means the pid was recycled: the
        // `sky run` that took this ref is gone.
        Some(c) => r.cmd.is_empty() || c == r.cmd,
        // No `ps` (a minimal container): fall back to bare aliveness rather than
        // dropping a ref that is probably real and tearing a live app's database
        // out from under it.
        None => true,
    }
}

/// This process, as a ref. Captured once at acquire time — reading it later
/// would defeat the point, since the whole question is whether the pid still
/// names *this* program.
fn self_ref() -> RunRef {
    let pid = std::process::id() as i32;
    RunRef {
        pid,
        cmd: process_command(pid).unwrap_or_default(),
        since: now_secs(),
    }
}

/// `~/.sky/clusters.json`. A `BTreeMap` keyed by canonical project path: the key
/// enforces one cluster per project structurally, and the ordering makes
/// `sky db ps --all` output stable between runs.
#[derive(Serialize, Deserialize, Clone, Debug, PartialEq, Eq)]
pub struct Registry {
    pub version: u32,
    #[serde(default)]
    pub clusters: BTreeMap<String, ClusterEntry>,
}

impl Default for Registry {
    fn default() -> Self {
        Registry { version: 1, clusters: BTreeMap::new() }
    }
}

/// What the machine says about a registry entry, as opposed to what the registry
/// says about itself.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Liveness {
    /// A postmaster is serving this data dir, at the carried pid. The pid may
    /// differ from the recorded one — someone can restart a cluster with bare
    /// `pg_ctl` — in which case the registry is refreshed rather than distrusted.
    Running(i32),
    /// The data dir is initialised but nothing is serving it.
    Stopped,
    /// The data dir is gone: the project was deleted, or `.skydata` was wiped.
    /// Nothing here is recoverable, so the entry is dropped.
    Vanished,
}

impl Registry {
    pub fn load(sky_home: &Path) -> Registry {
        let path = sky_home.join(REGISTRY_FILE);
        let Ok(text) = std::fs::read_to_string(&path) else {
            return Registry::default();
        };
        // A corrupt registry is recoverable (every entry can be re-derived from a
        // running cluster's postmaster.pid); refusing to run because of it is not.
        serde_json::from_str(&text).unwrap_or_default()
    }

    pub fn save(&self, sky_home: &Path) -> Result<(), String> {
        std::fs::create_dir_all(sky_home)
            .map_err(|e| format!("cannot create {}: {e}", sky_home.display()))?;
        let path = sky_home.join(REGISTRY_FILE);
        let tmp = sky_home.join(format!("{REGISTRY_FILE}.tmp.{}", std::process::id()));
        let text = serde_json::to_string_pretty(self)
            .map_err(|e| format!("cannot serialise the cluster registry: {e}"))?;
        std::fs::write(&tmp, format!("{text}\n"))
            .map_err(|e| format!("cannot write {}: {e}", tmp.display()))?;
        // Rename, not write-in-place: a crash mid-write would otherwise leave a
        // truncated registry and orphan every cluster listed after the cut.
        std::fs::rename(&tmp, &path).map_err(|e| {
            let _ = std::fs::remove_file(&tmp);
            format!("cannot update {}: {e}", path.display())
        })
    }

    /// Reconcile the registry with reality and report what is actually there.
    ///
    /// `Vanished` entries are removed. `Stopped` entries are kept — an
    /// initialised-but-idle project cluster is worth listing — but their pid is
    /// zeroed, which is the mechanism that stops a dead pid ever being printed as
    /// running. `Running` entries adopt the observed pid.
    ///
    /// The probe is injected so the reaping logic can be tested without a
    /// PostgreSQL installation, a filesystem, or a real process to kill.
    pub fn reap_with(
        &mut self,
        probe: &dyn Fn(&str, &ClusterEntry) -> Liveness,
    ) -> Vec<(String, ClusterEntry, Liveness)> {
        let mut out = Vec::new();
        let mut dead = Vec::new();
        for (project, entry) in self.clusters.iter_mut() {
            match probe(project, entry) {
                Liveness::Running(pid) => {
                    entry.pid = pid;
                    out.push((project.clone(), entry.clone(), Liveness::Running(pid)));
                }
                Liveness::Stopped => {
                    entry.pid = 0;
                    out.push((project.clone(), entry.clone(), Liveness::Stopped));
                }
                Liveness::Vanished => dead.push(project.clone()),
            }
        }
        for p in dead {
            self.clusters.remove(&p);
        }
        out
    }
}

/// `~/.sky`, or `$SKY_HOME` when set. The override exists so the end-to-end test
/// can drive a real cluster through a real registry without writing to the
/// developer's own `~/.sky/clusters.json`.
fn sky_home() -> PathBuf {
    if let Some(h) = std::env::var_os("SKY_HOME").filter(|h| !h.is_empty()) {
        return PathBuf::from(h);
    }
    let home = std::env::var_os("HOME")
        .or_else(|| std::env::var_os("USERPROFILE"))
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from("."));
    home.join(".sky")
}

/// A whole-file advisory lock around a registry read-modify-write, so two
/// `sky db start`s in different projects cannot lose each other's entry.
struct RegistryLock {
    path: PathBuf,
}

impl Drop for RegistryLock {
    fn drop(&mut self) {
        let _ = std::fs::remove_file(&self.path);
    }
}

fn acquire_registry_lock(sky_home: &Path) -> Result<RegistryLock, String> {
    std::fs::create_dir_all(sky_home)
        .map_err(|e| format!("cannot create {}: {e}", sky_home.display()))?;
    let path = sky_home.join(LOCK_FILE);
    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(5);
    loop {
        match std::fs::OpenOptions::new().write(true).create_new(true).open(&path) {
            Ok(_) => return Ok(RegistryLock { path }),
            Err(e) if e.kind() == std::io::ErrorKind::AlreadyExists => {
                // A process that was SIGKILLed while holding the lock leaves the
                // file behind. Break a lock that is older than any legitimate
                // hold could be, rather than deadlocking every later invocation.
                if lock_is_stale(&path) {
                    let _ = std::fs::remove_file(&path);
                    continue;
                }
                if std::time::Instant::now() >= deadline {
                    return Err(format!(
                        "another sky process is holding {}.\nIf no other `sky db` command is \
                         running, delete that file and retry.",
                        path.display()
                    ));
                }
                std::thread::sleep(std::time::Duration::from_millis(25));
            }
            Err(e) => return Err(format!("cannot lock {}: {e}", path.display())),
        }
    }
}

fn lock_is_stale(path: &Path) -> bool {
    std::fs::metadata(path)
        .and_then(|m| m.modified())
        .ok()
        .and_then(|t| t.elapsed().ok())
        .map(|age| age.as_secs() > LOCK_STALE_SECS)
        .unwrap_or(false)
}

fn now_secs() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
}

// ---- binary discovery ----------------------------------------------------

/// A located PostgreSQL installation.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PgBins {
    pub bin_dir: PathBuf,
    /// The full reported version, e.g. `14.21`.
    pub version: String,
    /// The major, which is the number that has to match a data directory.
    pub major: u32,
}

impl PgBins {
    pub fn tool(&self, name: &str) -> PathBuf {
        self.bin_dir.join(name)
    }
}

/// Candidate `bin` directories in the precedence the design brief fixes:
/// explicit override, then the phase-3 cache, then the system installation.
///
/// `cache_versions` is passed in rather than read off the disk so the ordering
/// rule — newest cached major first — is testable, and so this function stays
/// pure.
///
/// `pin` is the project's `sky.toml` `[database] postgresVersion`, which
/// `sky db provision --embed` records. It orders the CACHE GROUP only: a pinned
/// version that is provisioned wins over a newer one, because otherwise the pin
/// would be decorative — a project that states which PostgreSQL it is developed
/// against would still get whichever the machine provisioned last. It does not
/// jump the explicit `SKY_POSTGRES_BIN` override, and a pin with nothing
/// provisioned for it simply is not a candidate.
pub fn bin_dir_candidates(
    env_override: Option<&str>,
    cache_versions: &[String],
    sky_home: &Path,
    path_var: Option<&str>,
    pin: Option<&str>,
) -> Vec<PathBuf> {
    let mut out = Vec::new();
    if let Some(o) = env_override.map(str::trim).filter(|o| !o.is_empty()) {
        out.push(PathBuf::from(o));
    }
    let pin = pin.map(str::trim).filter(|p| !p.is_empty());
    let mut versions: Vec<&String> = cache_versions.iter().collect();
    // Newest first, so a project that has provisioned 16 alongside an old 14 gets
    // 16. Compared numerically per component: "9.6" must not sort above "14".
    versions.sort_by_key(|v| std::cmp::Reverse(version_key(v)));
    if let Some(p) = pin {
        versions.sort_by_key(|v| v.as_str() != p);
    }
    for v in versions {
        out.push(sky_home.join("postgres").join(v).join("bin"));
    }
    if let Some(p) = path_var {
        for dir in std::env::split_paths(p) {
            if !dir.as_os_str().is_empty() {
                out.push(dir);
            }
        }
    }
    out
}

fn version_key(v: &str) -> Vec<u32> {
    v.split(['.', '-'])
        .map(|p| p.trim_start_matches(|c: char| !c.is_ascii_digit()))
        .map(|p| p.parse::<u32>().unwrap_or(0))
        .collect()
}

/// The first candidate that holds every required binary. Split from
/// [`bin_dir_candidates`] so precedence can be tested against a fake filesystem.
pub fn pick_bin_dir(candidates: &[PathBuf], has_required: &dyn Fn(&Path) -> bool) -> Option<PathBuf> {
    candidates.iter().find(|d| has_required(d)).cloned()
}

fn dir_has_required_bins(dir: &Path) -> bool {
    REQUIRED_BINS.iter().all(|b| {
        let p = dir.join(b);
        p.is_file() || dir.join(format!("{b}.exe")).is_file()
    })
}

fn cached_versions(sky_home: &Path) -> Vec<String> {
    let Ok(rd) = std::fs::read_dir(sky_home.join("postgres")) else {
        return Vec::new();
    };
    rd.filter_map(Result::ok)
        .filter(|e| e.path().is_dir())
        .filter_map(|e| e.file_name().into_string().ok())
        .collect()
}

/// What to print when there is no PostgreSQL to supervise. Every line is an
/// action the reader can take; "not found" on its own would send them to the
/// source to work out what was even looked for.
pub fn no_binaries_message(sky_home: &Path) -> String {
    format!(
        "sky db: no PostgreSQL binaries found (need {}).\n\
         \n\
         Looked, in order:\n\
         \x20 1. $SKY_POSTGRES_BIN            (unset or incomplete)\n\
         \x20 2. {}/postgres/<version>/bin    (nothing provisioned yet)\n\
         \x20 3. $PATH\n\
         \n\
         Fix it with one of:\n\
         \x20 • install PostgreSQL and put its bin dir on PATH\n\
         \x20     macOS:  brew install postgresql@16\n\
         \x20     Debian: apt install postgresql\n\
         \x20 • point sky at an existing installation:\n\
         \x20     SKY_POSTGRES_BIN=/opt/homebrew/opt/postgresql@16/bin sky db start\n\
         \x20 • let sky fetch its own build of PostgreSQL:\n\
         \x20     sky db provision --embed",
        REQUIRED_BINS.join(", "),
        sky_home.display(),
    )
}

/// Is there anything for the supervisor to run at all?
///
/// The same candidate list [`discover_pg_bins`] walks, without the `pg_ctl
/// --version` interrogation — `sky doctor` wants the cheap answer to "is this
/// machine set up", and takes the project explicitly rather than off the cwd.
pub fn postgres_is_discoverable(project: &Path) -> bool {
    let home = sky_home();
    let env_override = std::env::var("SKY_POSTGRES_BIN").ok();
    let path_var = std::env::var("PATH").ok();
    let pin = crate::db_provision::pinned_version(project);
    let cands = bin_dir_candidates(
        env_override.as_deref(),
        &cached_versions(&home),
        &home,
        path_var.as_deref(),
        pin.as_deref(),
    );
    pick_bin_dir(&cands, &dir_has_required_bins).is_some()
}

/// Locate a usable installation, then ask it its version. Discovery and
/// interrogation are separate steps because a directory can hold all three
/// binaries and still refuse to run (wrong architecture, missing libs), and that
/// deserves a different message than "not found".
fn discover_pg_bins() -> Result<PgBins, String> {
    let home = sky_home();
    let env_override = std::env::var("SKY_POSTGRES_BIN").ok();
    let path_var = std::env::var("PATH").ok();
    let pin = current_project_dir()
        .ok()
        .and_then(|p| crate::db_provision::pinned_version(&p));
    let cands = bin_dir_candidates(
        env_override.as_deref(),
        &cached_versions(&home),
        &home,
        path_var.as_deref(),
        pin.as_deref(),
    );

    // An explicit override that does not hold the binaries is a typo, not a
    // reason to silently fall through to a different PostgreSQL — that would
    // hand the user a cluster from an installation they did not choose.
    if let Some(o) = env_override.as_deref().map(str::trim).filter(|o| !o.is_empty()) {
        if !dir_has_required_bins(Path::new(o)) {
            return Err(format!(
                "sky db: SKY_POSTGRES_BIN={o} does not contain {}.\n\
                 Point it at a PostgreSQL `bin` directory (the one holding pg_ctl).",
                REQUIRED_BINS.join(", ")
            ));
        }
    }

    let Some(bin_dir) = pick_bin_dir(&cands, &dir_has_required_bins) else {
        return Err(no_binaries_message(&home));
    };
    let out = Command::new(bin_dir.join("pg_ctl"))
        .arg("--version")
        .output()
        .map_err(|e| format!("sky db: cannot run {}/pg_ctl: {e}", bin_dir.display()))?;
    let text = String::from_utf8_lossy(&out.stdout);
    let (version, major) = parse_pg_version(&text).ok_or_else(|| {
        format!(
            "sky db: {}/pg_ctl reported a version string sky cannot parse: {}",
            bin_dir.display(),
            text.trim()
        )
    })?;
    Ok(PgBins { bin_dir, version, major })
}

/// `"pg_ctl (PostgreSQL) 14.21 (Homebrew)"` → `("14.21", 14)`.
pub fn parse_pg_version(out: &str) -> Option<(String, u32)> {
    let tok = out
        .split_whitespace()
        .find(|t| t.chars().next().is_some_and(|c| c.is_ascii_digit()))?;
    // Take the LEADING digit/dot run rather than trimming the trailing junk:
    // trimming leaves `18beta1` and `17rc1` intact (the `1` is a digit), and the
    // parse then fails — a pre-release server would be rejected with a message
    // about an unparseable version rather than simply working.
    let tok: String = tok.chars().take_while(|c| c.is_ascii_digit() || *c == '.').collect();
    let tok = tok.trim_end_matches('.');
    let major = tok.split('.').next()?.parse().ok()?;
    Some((tok.to_string(), major))
}

/// A data directory's `PG_VERSION` holds its major and nothing else — `14`, or
/// `9.6` for the pre-10 scheme where the major was two components.
pub fn parse_pg_version_file(text: &str) -> Option<u32> {
    text.trim().split('.').next()?.parse().ok()
}

/// Refuse rather than attempt. A postmaster pointed at a data directory from a
/// different major does not migrate it, and the raw refusal ("database files are
/// incompatible with server") names neither the two versions nor the way out.
pub fn version_mismatch_message(data_dir: &Path, dir_major: u32, bin_major: u32, bin_dir: &Path) -> String {
    format!(
        "sky db: PostgreSQL major mismatch — this cluster cannot be started.\n\
         \n\
         \x20 data directory: {}  (initialised by PostgreSQL {dir_major})\n\
         \x20 binaries:       {}  (PostgreSQL {bin_major})\n\
         \n\
         A {bin_major} server will not open a {dir_major} data directory, and starting one\n\
         against it would not upgrade it. Choose one:\n\
         \x20 • run the matching server:  SKY_POSTGRES_BIN=<path to {dir_major}'s bin> sky db start\n\
         \x20 • migrate with pg_upgrade, keeping the data\n\
         \x20 • discard the data:  sky db stop && rm -rf {}",
        data_dir.display(),
        bin_dir.display(),
        data_dir.display(),
    )
}

// ---- process probing -----------------------------------------------------

fn process_alive(pid: i32) -> bool {
    if pid <= 0 {
        return false;
    }
    #[cfg(unix)]
    {
        nix::sys::signal::kill(nix::unistd::Pid::from_raw(pid), None).is_ok()
    }
    #[cfg(not(unix))]
    {
        true
    }
}

/// Does this command line belong to a postmaster?
///
/// Pid reuse is the whole reason this exists. After a `SIGKILL` the stale
/// `postmaster.pid` still names a pid, and the kernel is free to hand that
/// number to something else; `kill(pid, 0)` then says "alive" about a shell.
/// Checking the command line closes that, and it is the difference between
/// `sky db ps` reporting a database that is not there and reporting the truth.
/// Matched on the EXECUTABLE, not on a substring of the whole command line. A
/// substring test says yes to `./app --embed --data-dir /var/lib/postgres-data`
/// and to `go test -run TestStopPostgresOnSignal` — P5a's own test process was
/// classified as a postmaster that way. Since this is the second leg of the
/// two-legged liveness check, the consequences are not cosmetic: `sky db ps`
/// reports a database that is not there, and a start refuses for as long as the
/// recycled pid lives.
pub fn command_looks_like_postgres(cmd: &str) -> bool {
    let argv0 = cmd.split_whitespace().next().unwrap_or("");
    let base = argv0.rsplit('/').next().unwrap_or("");
    // A postmaster that has rewritten its process title shows up as
    // `postgres: …`; the trailing colon is part of the title, not the name.
    let base = base
        .trim_end_matches(':')
        .trim_end_matches(".exe")
        .to_ascii_lowercase();
    matches!(base.as_str(), "postgres" | "postmaster")
}

fn process_command(pid: i32) -> Option<String> {
    let out = Command::new("ps")
        .args(["-o", "command=", "-p", &pid.to_string()])
        .output()
        .ok()?;
    if !out.status.success() {
        return None;
    }
    let s = String::from_utf8_lossy(&out.stdout).trim().to_string();
    if s.is_empty() {
        None
    } else {
        Some(s)
    }
}

fn is_postgres_process(pid: i32) -> bool {
    if !process_alive(pid) {
        return false;
    }
    // `ps` absent (a minimal container) → fall back to bare aliveness rather than
    // declaring a running cluster dead.
    process_command(pid).is_none_or(|c| command_looks_like_postgres(&c))
}

/// PostgreSQL's `postmaster.pid`: pid on line 1, data directory on line 2, start
/// time on line 3, port on line 4, socket directory on line 5.
pub fn parse_postmaster_pid(text: &str) -> Option<i32> {
    text.lines().next()?.trim().parse().ok()
}

fn read_postmaster_pid(data_dir: &Path) -> Option<i32> {
    let text = std::fs::read_to_string(data_dir.join("postmaster.pid")).ok()?;
    parse_postmaster_pid(&text)
}

/// The real probe behind [`Registry::reap_with`].
fn probe_entry(_project: &str, entry: &ClusterEntry) -> Liveness {
    probe_data_dir(Path::new(&entry.data_dir))
}

fn probe_data_dir(data_dir: &Path) -> Liveness {
    if !data_dir.join("PG_VERSION").is_file() {
        return Liveness::Vanished;
    }
    match read_postmaster_pid(data_dir) {
        Some(pid) if is_postgres_process(pid) => Liveness::Running(pid),
        _ => Liveness::Stopped,
    }
}

// ---- configuration -------------------------------------------------------

/// The tuning block appended to a freshly-initialised `postgresql.conf`.
///
/// These are resource knobs only — nothing here changes what a query means, so a
/// development cluster stays a faithful rehearsal of production, which is the
/// entire point of running PostgreSQL locally instead of SQLite. `wal_level` in
/// particular is left at its default for that reason.
///
/// `shared_buffers` at 32MB is the difference between "several idle project
/// clusters cost tens of megabytes each" and "hundreds": PostgreSQL's own
/// default is 128MB, and it is allocated up front whether or not the cluster
/// ever serves a query.
pub fn sky_conf_block() -> String {
    format!(
        "\n{SKY_CONF_MARKER}\n\
         # Resource sizing only — no setting here changes query semantics, so a\n\
         # development cluster behaves exactly as production does.\n\
         # `sky db start` passes -k <socket dir> on every start, because the hashed\n\
         # socket path is re-derived from the environment and must not be frozen here.\n\
         listen_addresses = ''\n\
         shared_buffers = 32MB\n\
         max_connections = 50\n\
         work_mem = 4MB\n\
         maintenance_work_mem = 32MB\n\
         max_wal_size = 256MB\n\
         min_wal_size = 64MB\n\
         autovacuum_max_workers = 1\n"
    )
}

/// Append the tuning block unless it is already there. Returns `None` when the
/// file is already tuned, so a re-run neither duplicates settings nor grows the
/// file without bound.
pub fn ensure_sky_conf(conf: &str) -> Option<String> {
    if conf.contains(SKY_CONF_MARKER) {
        return None;
    }
    Some(format!("{conf}{}", sky_conf_block()))
}

// ---- error translation ---------------------------------------------------

/// Turn PostgreSQL's own start failures into Sky-level ones.
///
/// The raw strings are accurate and unhelpful: "another server might be running"
/// does not say which data directory, and gives no command to run next. Anything
/// unrecognised returns `None` and is surfaced verbatim — inventing a friendly
/// message for an error we have not classified would hide it.
pub fn translate_pg_start_error(stderr: &str, data_dir: &Path, socket_dir: &Path) -> Option<String> {
    let s = stderr.to_ascii_lowercase();
    if s.contains("another server might be running") || s.contains("lock file") && s.contains("postmaster.pid") {
        return Some(format!(
            "sky db start: another PostgreSQL server is already using this data directory.\n\
             \x20 data directory: {}\n\
             Run `sky db ps` to see what sky knows about, or `sky db stop` to shut it down.\n\
             If you are certain nothing is running, the pid file is stale — remove\n\
             {} and retry.",
            data_dir.display(),
            data_dir.join("postmaster.pid").display(),
        ));
    }
    if s.contains("database files are incompatible") || s.contains("was initialized with") {
        return Some(format!(
            "sky db start: these PostgreSQL binaries cannot open {}.\n\
             The data directory was created by a different PostgreSQL major.\n\
             Run `sky db ps` for the recorded version, or point SKY_POSTGRES_BIN at the\n\
             matching installation.",
            data_dir.display(),
        ));
    }
    if s.contains("could not create lock file") || s.contains("could not bind") {
        return Some(format!(
            "sky db start: PostgreSQL could not create its socket in {}.\n\
             That directory is derived from the project path and must be writable.\n\
             Check permissions, or set XDG_RUNTIME_DIR to a writable directory.",
            socket_dir.display(),
        ));
    }
    if s.contains("permission denied") {
        return Some(format!(
            "sky db start: permission denied while starting the cluster in {}.\n\
             PostgreSQL requires the data directory to be owned by the user running it\n\
             and to be mode 0700.",
            data_dir.display(),
        ));
    }
    None
}

// ---- project resolution --------------------------------------------------

/// The project whose cluster a bare `sky db start` refers to: the nearest
/// ancestor of the cwd holding a `sky.toml`, else the cwd itself.
fn current_project_dir() -> Result<PathBuf, String> {
    let cwd = std::env::current_dir().map_err(|e| format!("sky db: cannot read the working directory: {e}"))?;
    let dir = project::project_dir_for(&cwd.join("_"));
    // Canonicalised because it is the registry key AND the hash input: `/tmp/x`
    // and `/private/tmp/x` are the same project on macOS and must not produce two
    // registry entries and two socket directories.
    Ok(dir.canonicalize().unwrap_or(dir))
}

fn data_dir_for(project: &Path) -> PathBuf {
    project.join(".skydata").join("pg")
}

fn log_path_for(project: &Path) -> PathBuf {
    project.join(".skydata").join("postgres.log")
}

// ---- verbs ---------------------------------------------------------------

/// `sky db start` — bring this project's cluster up, initialising it on first
/// use. Starting an already-running cluster is a success no-op: the verb states
/// a desired end state, and scripts that call it before every task must not have
/// to distinguish "started it" from "it was already there".
pub fn cmd_start(args: &[String]) -> ExitCode {
    if let Some(unknown) = args.iter().find(|a| a.starts_with('-')) {
        eprintln!("usage: sky db start\nunknown flag: {unknown}");
        return ExitCode::from(2);
    }
    match start_impl() {
        Ok(msg) => {
            println!("{msg}");
            ExitCode::SUCCESS
        }
        Err(e) => {
            eprintln!("{e}");
            ExitCode::FAILURE
        }
    }
}

fn start_impl() -> Result<String, String> {
    let project = current_project_dir()?;
    let started = start_cluster(&project)?;
    // `sky db start` is the EXPLICIT verb, so the entry is marked persistent:
    // from here on a `sky run` may lean on this cluster but must never stop it.
    upsert(&project, &started, &|e| e.explicit = true)?;
    if started.already_running {
        return Ok(format!(
            "sky db start: already running (pid {}).\n{}",
            started.pid,
            connection_hint(&started.socket_dir)
        ));
    }
    Ok(format!(
        "sky db start: PostgreSQL {} running (pid {}).\n\
         \x20 data:   {}\n\
         \x20 socket: {}\n\
         \x20 log:    {}\n{}",
        started.version,
        started.pid,
        started.data_dir.display(),
        started.socket_dir.display(),
        log_path_for(&project).display(),
        connection_hint(&started.socket_dir)
    ))
}

/// What a start left behind, whether or not this invocation was the one that
/// caused it.
pub struct Started {
    pub pid: i32,
    pub data_dir: PathBuf,
    pub socket_dir: PathBuf,
    pub version: String,
    /// True when the cluster was already up and nothing was spawned.
    pub already_running: bool,
}

/// Bring a project's cluster to "running", initialising it on first use.
///
/// Split out of [`start_impl`] so `sky run` reaches the same code — initdb, the
/// tuned conf, the stale-pid interlock, the version check, the socket-path
/// checks — rather than a second, subtly different implementation of it. It does
/// NOT touch the registry: the caller decides what the resulting entry means
/// (explicit and persistent, or ephemeral and ref-counted).
fn start_cluster(project: &Path) -> Result<Started, String> {
    if project::is_compiler_repo_root(project) {
        return Err("sky db: refusing to run a cluster from the Sky compiler repo root".to_string());
    }
    let bins = discover_pg_bins()?;
    let data_dir = data_dir_for(project);
    let socket_dir = socket_dir_real(project);

    // Refuse BEFORE initdb. A project whose path cannot be handed to pg_ctl can
    // never be started, and initialising a cluster first would leave the user a
    // 40MB data directory for a database they will never be able to run.
    if let Some(msg) = pg_ctl_shell_safety_error(&data_dir, &log_path_for(project), &socket_dir) {
        return Err(msg);
    }

    // Already up? Report it and stop — no initdb, no second postmaster.
    if let Liveness::Running(pid) = probe_data_dir(&data_dir) {
        return Ok(Started {
            pid,
            data_dir,
            socket_dir,
            version: bins.version,
            already_running: true,
        });
    }

    if data_dir.join("PG_VERSION").is_file() {
        let text = std::fs::read_to_string(data_dir.join("PG_VERSION"))
            .map_err(|e| format!("sky db start: cannot read {}: {e}", data_dir.join("PG_VERSION").display()))?;
        let dir_major = parse_pg_version_file(&text).ok_or_else(|| {
            format!(
                "sky db start: {} is not a PostgreSQL version ({text:?}).\n\
                 The data directory looks corrupt; remove {} to re-initialise.",
                data_dir.join("PG_VERSION").display(),
                data_dir.display()
            )
        })?;
        if dir_major != bins.major {
            return Err(version_mismatch_message(&data_dir, dir_major, bins.major, &bins.bin_dir));
        }
        // Initialised, not running, and the pid file survives → the postmaster was
        // killed rather than stopped. Clearing it here is what turns the next start
        // from a refusal into a start.
        clear_stale_pidfile(&data_dir)?;
    } else if data_dir.exists() && dir_is_nonempty(&data_dir) {
        return Err(format!(
            "sky db start: {} exists but is not a PostgreSQL data directory\n\
             (no PG_VERSION). A previous initdb probably failed part-way.\n\
             Remove it and retry:  rm -rf {}",
            data_dir.display(),
            data_dir.display()
        ));
    } else {
        run_initdb(&bins, &data_dir)?;
    }

    prepare_socket_dir(&socket_dir)?;
    let pid = match run_pg_ctl_start(&bins, project, &data_dir, &socket_dir) {
        Ok(pid) => pid,
        // Two `sky run`s racing on one project both see "stopped" and both call
        // `pg_ctl start`; the loser is told another server might be running. The
        // registry lock cannot close that window without holding it across a
        // 60-second start and blocking every other project's `sky db ps`. So the
        // loser re-probes, and adopts the winner's postmaster — which is the same
        // "already running is a success no-op" rule the verb states everywhere
        // else, applied to a race instead of to a second invocation.
        Err(e) => match probe_data_dir(&data_dir) {
            Liveness::Running(pid) => pid,
            _ => return Err(e),
        },
    };
    Ok(Started {
        pid,
        data_dir,
        socket_dir,
        version: bins.version,
        already_running: false,
    })
}

fn connection_hint(socket_dir: &Path) -> String {
    format!(
        "\nConnect with:\n\
         \x20 psql -h {sock} postgres\n\
         \x20 DSN: {dsn}",
        sock = socket_dir.display(),
        dsn = dsn_for_socket_dir(socket_dir)
    )
}

/// The DSN an app is handed for a socket-only development cluster.
///
/// `postgresql://` (not the libpq keyword form) because that is the shape both
/// `rt.detectDriver` and the compiler's `driver_for_dsn` classify as Postgres
/// from the prefix alone; `?host=<dir>` is libpq's documented way to name a unix
/// socket DIRECTORY, and pgx honours it. No user and no password: local auth is
/// `trust` and the client library defaults the role to the OS user, which is the
/// superuser `initdb` created.
///
/// The database is `postgres` — the one `initdb` always creates. A
/// database-per-app (and the role-per-app boundary that makes it worth having)
/// is the shared-cluster problem, and it is P6's.
pub fn dsn_for_socket_dir(socket_dir: &Path) -> String {
    format!("postgresql:///postgres?host={}", socket_dir.display())
}

fn dir_is_nonempty(dir: &Path) -> bool {
    std::fs::read_dir(dir).map(|mut r| r.next().is_some()).unwrap_or(false)
}

/// Remove a `postmaster.pid` left behind by a `SIGKILL`.
///
/// Only when the pid it names is genuinely not a live postgres — the check is
/// the safety interlock. Deleting a live postmaster's pid file would let a
/// second postmaster open the same data directory, which is how a development
/// database gets corrupted.
fn clear_stale_pidfile(data_dir: &Path) -> Result<(), String> {
    let pidfile = data_dir.join("postmaster.pid");
    let Some(pid) = read_postmaster_pid(data_dir) else {
        if pidfile.is_file() {
            let _ = std::fs::remove_file(&pidfile);
        }
        return Ok(());
    };
    if is_postgres_process(pid) {
        return Err(format!(
            "sky db start: a live process (pid {pid}) still holds {}.\n\
             Stop it first:  sky db stop",
            data_dir.display()
        ));
    }
    std::fs::remove_file(&pidfile)
        .map_err(|e| format!("sky db start: cannot clear the stale pid file {}: {e}", pidfile.display()))?;
    eprintln!("sky db start: cleared a stale postmaster.pid (pid {pid} is gone)");
    Ok(())
}

fn run_initdb(bins: &PgBins, data_dir: &Path) -> Result<(), String> {
    if let Some(parent) = data_dir.parent() {
        std::fs::create_dir_all(parent)
            .map_err(|e| format!("sky db start: cannot create {}: {e}", parent.display()))?;
    }
    // Verb-neutral: `sky run` reaches this too, and a progress line announcing a
    // command the user did not type reads as a bug in the tool.
    println!("sky db: initialising a PostgreSQL {} cluster in {}", bins.major, data_dir.display());
    let out = Command::new(bins.tool("initdb"))
        .arg("-D")
        .arg(data_dir)
        .args(["--encoding=UTF8", "--locale=C"])
        // The cluster is reachable only through a 0700 directory owned by this
        // user, so local trust costs nothing and spares every `psql` a password
        // prompt. Host auth is rejected outright: `listen_addresses = ''` already
        // means no TCP listener exists, and this is the belt to that's braces.
        .args(["--auth-local=trust", "--auth-host=reject"])
        .stdout(Stdio::null())
        .output()
        .map_err(|e| format!("sky db start: cannot run initdb: {e}"))?;
    if !out.status.success() {
        // A half-written data directory would be diagnosed as "not a PostgreSQL
        // data directory" on the next run, sending the user after the wrong bug.
        let _ = std::fs::remove_dir_all(data_dir);
        return Err(format!(
            "sky db start: initdb failed:\n{}",
            String::from_utf8_lossy(&out.stderr).trim()
        ));
    }
    tune_conf(data_dir)
}

fn tune_conf(data_dir: &Path) -> Result<(), String> {
    let conf_path = data_dir.join("postgresql.conf");
    let conf = std::fs::read_to_string(&conf_path)
        .map_err(|e| format!("sky db start: cannot read {}: {e}", conf_path.display()))?;
    if let Some(tuned) = ensure_sky_conf(&conf) {
        std::fs::write(&conf_path, tuned)
            .map_err(|e| format!("sky db start: cannot write {}: {e}", conf_path.display()))?;
    }
    Ok(())
}

/// `pg_ctl start` builds ONE command string and hands it to `/bin/sh -c` (see
/// `start_postmaster` in `pg_ctl.c`), so every path it interpolates is
/// shell-interpreted. A path carrying a quote, a `$(…)` or a backtick would
/// either break the start or execute something. Rejecting it is the only honest
/// option; quoting cannot make it safe.
pub fn socket_dir_is_shell_safe(dir: &Path) -> bool {
    !dir.to_string_lossy()
        .contains(['\'', '"', '`', '$', '\\', ' ', '\t', '\n', ';', '&', '|', '(', ')', '<', '>', '*', '?'])
}

/// The socket directory is NOT the only argument that goes through that shell.
/// `start_postmaster` interpolates the executable, `-D`, the `-o` post-options
/// **and** `-l` into the single string it passes to `/bin/sh -c`; P5a verified
/// against PostgreSQL 14.21 that pointing each of the three at a path containing
/// `$(touch …)` runs it. All three fired.
///
/// The socket path is derived by sky and is safe by construction (bar
/// `$XDG_RUNTIME_DIR`); `-D` and `-l` are derived from the **project directory**,
/// which is the user's — and, for anyone who checks out a repository, someone
/// else's. So the same predicate has to cover all three, and a failure has to be
/// a refusal naming the offending path rather than an attempt to quote it.
///
/// `pg_ctl stop` does not shell out; only `start` does.
pub fn pg_ctl_shell_safety_error(data_dir: &Path, log: &Path, socket_dir: &Path) -> Option<String> {
    let (path, role) = [
        (data_dir, "the data directory, passed as pg_ctl -D"),
        (log, "the server log, passed as pg_ctl -l"),
        (socket_dir, "the socket directory, passed as -o \"-k …\""),
    ]
    .into_iter()
    .find(|(p, _)| !socket_dir_is_shell_safe(p))?;
    Some(format!(
        "sky db start: refusing to start — a path sky must hand to pg_ctl contains\n\
         characters that a shell would interpret:\n\
         \x20 {}\n\
         \x20 ({role})\n\
         \n\
         pg_ctl runs the postmaster through `/bin/sh -c`, building one command line\n\
         out of the executable, -D, -o and -l (start_postmaster in pg_ctl.c), so a\n\
         `$(…)`, a backtick or a quote in any of them would be EXECUTED. That cannot\n\
         be made safe by quoting, so sky refuses instead.\n\
         \n\
         Move the project to a path without any of  ' \" ` $ \\ ; & | ( ) < > * ? space\n\
         (or, if the offending path is the socket directory, set XDG_RUNTIME_DIR to a\n\
         plain path) and retry.",
        path.display()
    ))
}

fn prepare_socket_dir(socket_dir: &Path) -> Result<(), String> {
    std::fs::create_dir_all(socket_dir).map_err(|e| {
        format!(
            "sky db start: cannot create the socket directory {}: {e}",
            socket_dir.display()
        )
    })?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        // The socket is the access control: anything that can reach it can talk to
        // the database as its superuser, because local auth is trust.
        let _ = std::fs::set_permissions(socket_dir, std::fs::Permissions::from_mode(0o700));
    }
    if !socket_dir_is_shell_safe(socket_dir) {
        return Err(format!(
            "sky db start: the derived socket directory contains characters sky will not\n\
             pass through a shell:\n\
             \x20 {}\n\
             pg_ctl runs the postmaster via /bin/sh, so this cannot be quoted safely.\n\
             Set XDG_RUNTIME_DIR to a plain path and retry.",
            socket_dir.display()
        ));
    }
    let len = socket_path_len(socket_dir);
    if len > MAX_SOCKET_PATH {
        return Err(format!(
            "sky db start: the derived socket path is {len} bytes, over the {MAX_SOCKET_PATH}-byte limit:\n\
             \x20 {}\n\
             Set XDG_RUNTIME_DIR to a shorter directory and retry.",
            socket_dir.join(SOCKET_BASENAME).display()
        ));
    }
    Ok(())
}

fn run_pg_ctl_start(bins: &PgBins, project: &Path, data_dir: &Path, socket_dir: &Path) -> Result<i32, String> {
    let log = log_path_for(project);
    // The guarantee belongs at the call site, not only at the caller that happens
    // to reach it today: this is the one place a path crosses into `/bin/sh`.
    if let Some(msg) = pg_ctl_shell_safety_error(data_dir, &log, socket_dir) {
        return Err(msg);
    }
    let out = Command::new(bins.tool("pg_ctl"))
        .arg("-D")
        .arg(data_dir)
        .arg("-l")
        .arg(&log)
        // `-k` is the postmaster's unix_socket_directories. Passed per start rather
        // than written into postgresql.conf so the hashed path is re-derived from
        // the current environment every time.
        .arg("-o")
        // Single-quoted because pg_ctl interpolates this into a `/bin/sh` command
        // line. `prepare_socket_dir` has already rejected anything a single quote
        // could not contain.
        .arg(format!("-k '{}'", socket_dir.display()))
        .args(["-w", "-t", "60"])
        .arg("start")
        .output()
        .map_err(|e| format!("sky db start: cannot run pg_ctl: {e}"))?;
    if !out.status.success() {
        let stderr = format!(
            "{}{}",
            String::from_utf8_lossy(&out.stderr),
            String::from_utf8_lossy(&out.stdout)
        );
        if let Some(msg) = translate_pg_start_error(&stderr, data_dir, socket_dir) {
            return Err(msg);
        }
        let tail = std::fs::read_to_string(&log)
            .map(|t| t.lines().rev().take(20).collect::<Vec<_>>().into_iter().rev().collect::<Vec<_>>().join("\n"))
            .unwrap_or_default();
        return Err(format!(
            "sky db start: pg_ctl start failed:\n{}\n{}",
            stderr.trim(),
            if tail.is_empty() { String::new() } else { format!("--- {} ---\n{tail}", log.display()) }
        ));
    }
    read_postmaster_pid(data_dir).ok_or_else(|| {
        format!(
            "sky db start: pg_ctl reported success but {} has no pid",
            data_dir.join("postmaster.pid").display()
        )
    })
}

/// Record what a start observed, then let the caller say what it MEANS.
///
/// The observed facts (data dir, socket, pid, version) are overwritten; the
/// interpretation (`explicit`, `refs`) is merged, because it belongs to whoever
/// set it. That is what keeps `sky db start` from clearing a `sky run`'s
/// reference, and a `sky run` from clearing the persistence a `sky db start`
/// asked for. Stale refs are pruned on the way through, so every writer to the
/// registry is also a reaper of dead run-references.
fn upsert(project: &Path, started: &Started, f: &dyn Fn(&mut ClusterEntry)) -> Result<(), String> {
    let home = sky_home();
    let _lock = acquire_registry_lock(&home)?;
    let mut reg = Registry::load(&home);
    reg.reap_with(&probe_entry);
    let e = reg
        .clusters
        .entry(project.display().to_string())
        .or_insert_with(|| ClusterEntry {
            data_dir: String::new(),
            socket_dir: String::new(),
            pid: 0,
            pg_version: String::new(),
            started_at: 0,
            explicit: false,
            refs: Vec::new(),
        });
    e.data_dir = started.data_dir.display().to_string();
    e.socket_dir = started.socket_dir.display().to_string();
    e.pid = started.pid;
    e.pg_version = started.version.clone();
    e.started_at = now_secs();
    e.refs = prune_refs(&e.refs, &ref_is_live);
    f(e);
    reg.save(&home)
}

// ---- `sky run` integration: ref-counted, ephemeral clusters ---------------

/// A `sky run` / `sky watch` invocation's claim on a project's cluster.
///
/// Held for as long as the app is running. Dropping it releases the reference
/// and — only if nothing else holds one and no `sky db start` asked for
/// persistence — stops the cluster.
pub struct RunLease {
    project: PathBuf,
    pid: i32,
    /// The DSN to inject into the app's environment, under `env_name`.
    pub dsn: String,
    pub env_name: String,
    pub socket_dir: PathBuf,
    pub version: String,
    pub already_running: bool,
}

/// Bring this project's cluster up for a `sky run`, and take a reference to it.
///
/// The reference is what makes concurrency safe: a second `sky run` on the same
/// project adds its own, and the first one's exit then finds the list non-empty
/// and leaves the postmaster alone.
pub fn acquire_for_run(project: &Path) -> Result<RunLease, String> {
    let started = start_cluster(project)?;
    let me = self_ref();
    upsert(project, &started, &|e| {
        // Replace rather than append when this pid is somehow already listed: a
        // ref list is a set keyed on the holder, and a double-acquire must not
        // need a double-release.
        e.refs.retain(|r| r.pid != me.pid);
        e.refs.push(me.clone());
    })?;
    Ok(RunLease {
        project: project.to_path_buf(),
        pid: me.pid,
        dsn: dsn_for_socket_dir(&started.socket_dir),
        env_name: dsn_env_name(project),
        socket_dir: started.socket_dir,
        version: started.version,
        already_running: started.already_running,
    })
}

impl Drop for RunLease {
    fn drop(&mut self) {
        if let Err(e) = release_run_ref(&self.project, self.pid) {
            // A failed release leaves a cluster up, which is recoverable
            // (`sky db stop`) and must not change the app's exit code.
            eprintln!("sky run: could not release the embedded cluster: {e}");
        }
    }
}

/// Drop this process's reference, and stop the cluster if that was the last
/// thing keeping it up.
///
/// The registry lock is held ACROSS the `pg_ctl stop`. That is deliberate: the
/// decision "no one else needs this" and the shutdown that acts on it have to be
/// one atomic step, or a `sky run` starting in the gap would take a reference to
/// a postmaster that is already on its way down. A fast shutdown of an idle
/// development cluster is sub-second, so the window a concurrent `sky db ps`
/// waits on is nothing like the lock's five-second patience.
fn release_run_ref(project: &Path, pid: i32) -> Result<(), String> {
    let home = sky_home();
    let _lock = acquire_registry_lock(&home)?;
    let mut reg = Registry::load(&home);
    let key = project.display().to_string();
    let Some(entry) = reg.clusters.get_mut(&key) else {
        return Ok(());
    };
    entry.refs = prune_refs(&entry.refs, &ref_is_live)
        .into_iter()
        .filter(|r| r.pid != pid)
        .collect();
    let data_dir = PathBuf::from(&entry.data_dir);
    let socket_dir = PathBuf::from(&entry.socket_dir);
    // `sky db start` said "keep this up"; an ephemeral run does not get to
    // overrule that, however it was that the cluster came to be running.
    let keep = entry.explicit || !entry.refs.is_empty();
    if keep || !matches!(probe_data_dir(&data_dir), Liveness::Running(_)) {
        return reg.save(&home);
    }
    let bins = discover_pg_bins()?;
    let out = Command::new(bins.tool("pg_ctl"))
        .arg("-D")
        .arg(&data_dir)
        .args(["-m", "fast", "-w", "-t", "20", "stop"])
        .output()
        .map_err(|e| format!("cannot run pg_ctl: {e}"))?;
    if !out.status.success() {
        reg.save(&home)?;
        return Err(String::from_utf8_lossy(&out.stderr).trim().to_string());
    }
    if let Some(e) = reg.clusters.get_mut(&key) {
        e.pid = 0;
    }
    remove_socket_dir_if_empty(&socket_dir);
    reg.save(&home)
}

// ---- opting a project in, and refusing to guess -------------------------

/// Does this project want `sky run` to supervise a cluster for it?
///
/// The opt-in is `[database] embedded = true`, in the section that already owns
/// `driver` / `path` / `url` / the pool knobs / `isolation`. The design brief
/// sketched a `[data]` section; `[database]` is what P1 actually landed, and a
/// second section describing the same subsystem would leave a reader with two
/// places to look and no rule for which wins.
pub fn project_uses_embedded(project: &Path) -> bool {
    project::sky_toml_flag(project, "database", "embedded")
}

/// The environment variable an app reads its DSN from — `SKY_DB_PATH`, or the
/// project's own namespace when `sky.toml` declares `[env] prefix`. Injecting
/// the unprefixed name into a prefixed app would set a variable nothing reads,
/// and the app would then fail with "no path given" while the cluster it was
/// meant to use sat there running.
pub fn dsn_env_name(project: &Path) -> String {
    let prefix = project::sky_toml_section_key(project, "env", "prefix")
        .map(|p| p.trim().trim_end_matches('_').to_string())
        .filter(|p| !p.is_empty())
        .unwrap_or_else(|| "SKY".to_string());
    format!("{prefix}_DB_PATH")
}

/// An explicit DSN alongside `embedded = true` is an ambiguity, and it is
/// reported rather than resolved — the same rule the design brief fixes for
/// `./app --embed`.
///
/// There is no defensible precedence here. Preferring the cluster means an app
/// silently writes to a throwaway local data directory while its author believes
/// it is talking to the server they named. Preferring the DSN means
/// `embedded = true` is a line of configuration that does nothing. Both are worse
/// than stopping.
///
/// Sources are checked in the order a reader would suspect them, and only the
/// first is reported: a stack of four complaints about the same mistake is
/// harder to act on than one.
pub fn embedded_dsn_conflict(verb: &str, sources: &[(String, Option<String>)]) -> Option<String> {
    let (name, value) = sources
        .iter()
        .find_map(|(n, v)| v.as_ref().filter(|v| !v.trim().is_empty()).map(|v| (n, v)))?;
    // "unset" is the verb for an environment variable and nonsense for a config
    // line; a fix instruction the reader has to translate is half an instruction.
    let clear = if name.starts_with("sky.toml") {
        format!("delete `{name}`")
    } else {
        format!("unset {name}")
    };
    Some(format!(
        "{verb}: this project is configured for an embedded PostgreSQL cluster\n\
         (sky.toml `[database] embedded = true`) and also carries an explicit\n\
         connection string:\n\
         \n\
         \x20 {name} = {value}\n\
         \n\
         Sky will not choose between them. Starting the cluster anyway would write\n\
         to a local data directory while you believed the app was talking to the\n\
         database you named — the failure that only shows up once the data is in\n\
         the wrong place.\n\
         \n\
         Pick one:\n\
         \x20 • use the connection string: remove `embedded = true` from sky.toml\n\
         \x20 • use the cluster:           {clear}"
    ))
}

/// The pre-build half of the `sky run` / `sky watch` entry point: is this
/// project opted in, and is its configuration coherent?
///
/// Split from [`acquire_for_run`] on purpose. The refusal has to come BEFORE the
/// compile, so a misconfigured project is not made to sit through one; the
/// *start* has to come after it, so a project with a syntax error does not cycle
/// a PostgreSQL up and back down on every failed build.
///
/// `Ok(false)` means "not opted in" — the overwhelmingly common case, and it
/// costs one read of the `sky.toml` the build is about to read anyway: no
/// registry, no `pg_ctl --version`, no filesystem probe.
pub fn check_run_config(project: &Path, verb: &str) -> Result<bool, String> {
    if !project_uses_embedded(project) {
        return Ok(false);
    }
    let dsn_env = dsn_env_name(project);
    let sources = vec![
        (dsn_env.clone(), std::env::var(&dsn_env).ok()),
        // Not namespaced: `rt.Db_connect` falls back to a bare `DATABASE_URL`
        // whatever the prefix, so it is just as capable of pointing the app
        // somewhere else.
        ("DATABASE_URL".to_string(), std::env::var("DATABASE_URL").ok()),
        (
            "sky.toml [database] path".to_string(),
            project::sky_toml_section_key(project, "database", "path"),
        ),
        (
            "sky.toml [database] url".to_string(),
            project::sky_toml_section_key(project, "database", "url"),
        ),
    ];
    if let Some(msg) = embedded_dsn_conflict(verb, &sources) {
        return Err(msg);
    }
    Ok(true)
}

impl RunLease {
    /// The environment the app is launched with. One variable: the app consumes
    /// a DSN and never learns which tier provisioned it.
    pub fn envs(&self) -> Vec<(String, String)> {
        vec![(self.env_name.clone(), self.dsn.clone())]
    }

    /// What to tell the user, so a cluster appearing in `sky db ps` is never a
    /// surprise and the socket is copy-pasteable into `psql`.
    ///
    /// `prefix` is used VERBATIM — `sky run:` and `[watch]` are punctuated
    /// differently, and appending a colon to the second produced `[watch]:`.
    pub fn banner(&self, prefix: &str) -> String {
        format!(
            "{prefix} embedded PostgreSQL {} {} — {}",
            self.version,
            if self.already_running {
                "already running"
            } else {
                "started"
            },
            self.socket_dir.display()
        )
    }
}

/// `sky db stop [--all]` — `pg_ctl stop -m fast`: refuse new connections, roll
/// back what is in flight, exit. Not `-m smart`, which waits for every client to
/// disconnect and turns a stop into a hang.
pub fn cmd_stop(args: &[String]) -> ExitCode {
    let all = args.iter().any(|a| a == "--all");
    if let Some(unknown) = args.iter().find(|a| a.starts_with('-') && a.as_str() != "--all") {
        eprintln!("usage: sky db stop [--all]\nunknown flag: {unknown}");
        return ExitCode::from(2);
    }
    match stop_impl(all) {
        Ok(msg) => {
            println!("{msg}");
            ExitCode::SUCCESS
        }
        Err(e) => {
            eprintln!("{e}");
            ExitCode::FAILURE
        }
    }
}

fn stop_impl(all: bool) -> Result<String, String> {
    let home = sky_home();
    let _lock = acquire_registry_lock(&home)?;
    let mut reg = Registry::load(&home);
    let live = reg.reap_with(&probe_entry);

    let targets: Vec<(String, ClusterEntry)> = if all {
        live.iter()
            .filter(|(_, _, l)| matches!(l, Liveness::Running(_)))
            .map(|(p, e, _)| (p.clone(), e.clone()))
            .collect()
    } else {
        let project = current_project_dir()?;
        let key = project.display().to_string();
        match probe_data_dir(&data_dir_for(&project)) {
            Liveness::Running(pid) => {
                let entry = reg.clusters.get(&key).cloned().unwrap_or(ClusterEntry {
                    data_dir: data_dir_for(&project).display().to_string(),
                    socket_dir: socket_dir_real(&project).display().to_string(),
                    pid,
                    pg_version: String::new(),
                    started_at: 0,
                    explicit: false,
                    refs: Vec::new(),
                });
                vec![(key, entry)]
            }
            // Idempotent by design: `sky db stop` states a desired end state, and
            // a script that runs it in a trap must not fail because the cluster
            // already went down.
            _ => {
                reg.save(&home)?;
                return Ok(format!("sky db stop: no cluster running for {}", project.display()));
            }
        }
    };

    if targets.is_empty() {
        reg.save(&home)?;
        return Ok("sky db stop: no Sky-managed clusters are running".to_string());
    }

    let bins = discover_pg_bins()?;
    let mut stopped = Vec::new();
    let mut failed = Vec::new();
    for (project, entry) in targets {
        let out = Command::new(bins.tool("pg_ctl"))
            .arg("-D")
            .arg(&entry.data_dir)
            .args(["-m", "fast", "-w", "-t", "60", "stop"])
            .output();
        match out {
            Ok(o) if o.status.success() => {
                if let Some(e) = reg.clusters.get_mut(&project) {
                    e.pid = 0;
                    // The user stopped it by hand, so neither the persistence a
                    // `sky db start` asked for nor any `sky run` reference
                    // survives. Leaving `explicit` set would make the NEXT
                    // cluster — the one a later `sky run` starts — permanent by
                    // inheritance, and leaving refs would make it unstoppable.
                    e.explicit = false;
                    e.refs.clear();
                }
                remove_socket_dir_if_empty(Path::new(&entry.socket_dir));
                stopped.push(project);
            }
            Ok(o) => failed.push(format!(
                "  {project}: {}",
                String::from_utf8_lossy(&o.stderr).trim()
            )),
            Err(e) => failed.push(format!("  {project}: cannot run pg_ctl: {e}")),
        }
    }
    reg.save(&home)?;

    if !failed.is_empty() {
        return Err(format!(
            "sky db stop: {} cluster(s) stopped, {} failed:\n{}",
            stopped.len(),
            failed.len(),
            failed.join("\n")
        ));
    }
    Ok(format!(
        "sky db stop: stopped {} cluster(s):\n{}",
        stopped.len(),
        stopped.iter().map(|p| format!("  {p}")).collect::<Vec<_>>().join("\n")
    ))
}

/// A clean shutdown removes the socket file but leaves its directory, so
/// without this every project ever started accumulates an empty directory in
/// `/tmp` that nothing will ever clean up.
///
/// `remove_dir` — never `remove_dir_all`. It fails harmlessly on a non-empty
/// directory, which is the interlock: if anything is still in there, another
/// postmaster is using it and this is not our directory to delete.
fn remove_socket_dir_if_empty(socket_dir: &Path) {
    let _ = std::fs::remove_dir(socket_dir);
}

/// `sky db ps [--all]` — what is actually running, having first reconciled the
/// registry with the machine.
pub fn cmd_ps(args: &[String]) -> ExitCode {
    let all = args.iter().any(|a| a == "--all");
    if let Some(unknown) = args.iter().find(|a| a.starts_with('-') && a.as_str() != "--all") {
        eprintln!("usage: sky db ps [--all]\nunknown flag: {unknown}");
        return ExitCode::from(2);
    }
    match ps_impl(all) {
        Ok(msg) => {
            println!("{msg}");
            ExitCode::SUCCESS
        }
        Err(e) => {
            eprintln!("{e}");
            ExitCode::FAILURE
        }
    }
}

fn ps_impl(all: bool) -> Result<String, String> {
    let home = sky_home();
    let _lock = acquire_registry_lock(&home)?;
    let mut reg = Registry::load(&home);
    let mut rows = reg.reap_with(&probe_entry);
    reg.save(&home)?;

    if !all {
        let project = current_project_dir()?;
        let key = project.display().to_string();
        rows.retain(|(p, _, _)| *p == key);
        if rows.is_empty() {
            // Unregistered but initialised: the registry can be deleted without
            // taking the cluster with it, so fall back to probing the data dir.
            let data_dir = data_dir_for(&project);
            return Ok(match probe_data_dir(&data_dir) {
                Liveness::Vanished => format!(
                    "sky db ps: no cluster for {} — run `sky db start`",
                    project.display()
                ),
                l => render_table(&[(
                    key,
                    ClusterEntry {
                        data_dir: data_dir.display().to_string(),
                        socket_dir: socket_dir_real(&project).display().to_string(),
                        pid: if let Liveness::Running(p) = l { p } else { 0 },
                        pg_version: String::new(),
                        started_at: 0,
                        explicit: false,
                        refs: Vec::new(),
                    },
                    l,
                )]),
            });
        }
    }
    if rows.is_empty() {
        return Ok("sky db ps: no Sky-managed clusters on this machine".to_string());
    }
    Ok(render_table(&rows))
}

fn render_table(rows: &[(String, ClusterEntry, Liveness)]) -> String {
    let mut out = String::from("PROJECT  STATUS  PID  VERSION  SOCKET\n");
    let widths = rows.iter().fold([7usize, 6, 3, 7], |mut w, (p, e, l)| {
        w[0] = w[0].max(p.len());
        w[1] = w[1].max(status_word(*l).len());
        w[2] = w[2].max(pid_word(*l).len());
        w[3] = w[3].max(e.pg_version.len().max(1));
        w
    });
    out.clear();
    out.push_str(&format!(
        "{:<w0$}  {:<w1$}  {:<w2$}  {:<w3$}  {}\n",
        "PROJECT",
        "STATUS",
        "PID",
        "VERSION",
        "SOCKET",
        w0 = widths[0],
        w1 = widths[1],
        w2 = widths[2],
        w3 = widths[3]
    ));
    for (p, e, l) in rows {
        let ver = if e.pg_version.is_empty() { "-" } else { &e.pg_version };
        out.push_str(&format!(
            "{:<w0$}  {:<w1$}  {:<w2$}  {:<w3$}  {}\n",
            p,
            status_word(*l),
            pid_word(*l),
            ver,
            e.socket_dir,
            w0 = widths[0],
            w1 = widths[1],
            w2 = widths[2],
            w3 = widths[3]
        ));
    }
    out.trim_end().to_string()
}

fn status_word(l: Liveness) -> &'static str {
    match l {
        Liveness::Running(_) => "running",
        Liveness::Stopped => "stopped",
        Liveness::Vanished => "gone",
    }
}

fn pid_word(l: Liveness) -> String {
    match l {
        Liveness::Running(p) => p.to_string(),
        _ => "-".to_string(),
    }
}

// ---- tests ---------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;

    // --- socket path derivation ---

    /// THE cross-implementation gate. Its twin is
    /// `TestTheSocketDirectoryForAPinnedProjectIsAPinnedConstant` in
    /// `runtime-go/rt/pg_embed_socket_test.go`, and both assert this same
    /// literal rather than comparing the two implementations to each other —
    /// two implementations compared only to each other can drift together, and
    /// that is precisely what they did. This side hashed the PROJECT path while
    /// the Go side hashed `<dataRoot>/pg`, so `./app --embed` in a project whose
    /// cluster was already up probed a socket directory that did not exist.
    ///
    /// `/sky/pinned/project` does not exist, so `resolved_path` is an identity
    /// here and the constant holds on any machine.
    #[test]
    fn the_socket_directory_for_a_pinned_project_is_a_pinned_constant() {
        let d = socket_dir_for_project(Path::new("/sky/pinned/project"), None, Path::new("/tmp"));
        assert_eq!(
            d,
            PathBuf::from("/tmp/sky-3b7c436bcb7e1ee0"),
            "if this changed deliberately, the Go twin in pg_embed_socket_test.go must \
             change in the same commit — the two name one directory for one cluster"
        );
        // And the input really is the data directory, not the project.
        assert_eq!(
            d,
            socket_dir_for_data_dir(
                Path::new("/sky/pinned/project/.skydata/pg"),
                None,
                Path::new("/tmp")
            )
        );
    }

    /// A project reached through a symlink is one project. `current_project_dir`
    /// canonicalises before hashing; the Go side resolves too, and this asserts
    /// the derivation itself does not depend on which name the caller used.
    #[test]
    fn a_symlinked_data_directory_hashes_as_its_real_path() {
        let real = std::env::temp_dir().join(format!("sky-p5b-real-{}", std::process::id()));
        let link = std::env::temp_dir().join(format!("sky-p5b-link-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&real);
        let _ = std::fs::remove_file(&link);
        std::fs::create_dir_all(&real).unwrap();
        #[cfg(unix)]
        std::os::unix::fs::symlink(&real, &link).unwrap();
        let via_link = socket_dir_for_data_dir(&link.join("pg"), None, Path::new("/tmp"));
        let via_real = socket_dir_for_data_dir(&real.join("pg"), None, Path::new("/tmp"));
        let _ = std::fs::remove_file(&link);
        let _ = std::fs::remove_dir_all(&real);
        assert_eq!(via_link, via_real, "a symlinked data dir hashed differently");
    }

    /// `.skydata/pg` does not exist until the first `initdb`, so the resolver
    /// must keep components that are not on disk. One that gave up on a missing
    /// leaf would hash the parent and put every project in one socket directory.
    #[test]
    fn resolved_path_keeps_components_that_do_not_exist_yet() {
        let root = std::env::temp_dir();
        let got = resolved_path(&root.join("sky-p5b-nope").join("deeper"));
        assert!(got.ends_with("sky-p5b-nope/deeper"), "{}", got.display());
        assert_ne!(
            socket_dir_for_data_dir(&root.join("one").join("pg"), None, Path::new("/tmp")),
            socket_dir_for_data_dir(&root.join("two").join("pg"), None, Path::new("/tmp")),
        );
    }

    /// `dsn_env_name` had no test, and every flow fixture used the default
    /// prefix — so it could be reduced to a hard-coded `"SKY_DB_PATH"` with the
    /// whole suite green. On a project with `[env] prefix = "FENCE"` that means
    /// `sky run` injects `SKY_DB_PATH` while the app reads `FENCE_DB_PATH`: the
    /// app fails to connect with its database healthy, and the `--embed`
    /// ambiguity check inspects the wrong variable into the bargain.
    #[test]
    fn the_injected_dsn_variable_follows_the_projects_env_prefix() {
        let dir = std::env::temp_dir().join(format!("sky-p5b-prefix-{}-{}", std::process::id(), now_secs()));
        std::fs::create_dir_all(&dir).unwrap();

        std::fs::write(dir.join("sky.toml"), "[database]\nembedded = true\n").unwrap();
        assert_eq!(dsn_env_name(&dir), "SKY_DB_PATH");

        std::fs::write(
            dir.join("sky.toml"),
            "[env]\nprefix = \"FENCE\"\n\n[database]\nembedded = true\n",
        )
        .unwrap();
        assert_eq!(dsn_env_name(&dir), "FENCE_DB_PATH");

        // A prefix written with the separator already on it must not double it.
        std::fs::write(dir.join("sky.toml"), "[env]\nprefix = \"FENCE_\"\n").unwrap();
        assert_eq!(dsn_env_name(&dir), "FENCE_DB_PATH");

        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn socket_dir_is_stable_and_project_specific() {
        let a = Path::new("/Users/dev/projects/alpha");
        let b = Path::new("/Users/dev/projects/beta");
        assert_eq!(
            socket_dir_for_project(a, None, Path::new("/tmp")),
            socket_dir_for_project(a, None, Path::new("/tmp"))
        );
        assert_ne!(
            socket_dir_for_project(a, None, Path::new("/tmp")),
            socket_dir_for_project(b, None, Path::new("/tmp"))
        );
    }

    #[test]
    fn socket_dir_uses_xdg_runtime_dir_when_it_fits() {
        let p = Path::new("/Users/dev/app");
        let d = socket_dir_for_project(p, Some("/run/user/1000"), Path::new("/tmp"));
        assert!(d.starts_with("/run/user/1000/sky"), "{}", d.display());
        assert!(socket_path_len(&d) <= MAX_SOCKET_PATH);
    }

    #[test]
    fn socket_dir_ignores_a_relative_or_blank_xdg_runtime_dir() {
        let p = Path::new("/Users/dev/app");
        for xdg in [Some(""), Some("   "), Some("relative/run"), None] {
            let d = socket_dir_for_project(p, xdg, Path::new("/tmp"));
            // String prefix, not `Path::starts_with`: the latter compares whole
            // components, so `/tmp/sky-abc` does NOT start with `/tmp/sky-`.
            assert!(d.to_string_lossy().starts_with("/tmp/sky-"), "{xdg:?} → {}", d.display());
        }
    }

    #[test]
    fn socket_dir_falls_back_when_xdg_runtime_dir_is_itself_too_long() {
        // A real shape: a per-session runtime dir on a host with a long hostname.
        let xdg = format!("/run/user/1000/{}", "verylongsessiondirectory".repeat(4));
        let d = socket_dir_for_project(Path::new("/Users/dev/app"), Some(&xdg), Path::new("/tmp"));
        assert!(d.to_string_lossy().starts_with("/tmp/sky-"), "{}", d.display());
        assert!(socket_path_len(&d) <= MAX_SOCKET_PATH);
    }

    /// THE case the hashed-path design exists for. A socket placed inside a
    /// project this deep would be ~400 bytes and `bind(2)` would fail with
    /// ENAMETOOLONG — an error naming neither the project nor the limit.
    #[test]
    fn pathologically_deep_project_still_yields_a_bindable_socket_path() {
        let mut deep = PathBuf::from("/Users/somebody/Development/workspaces");
        for i in 0..40 {
            deep.push(format!("nested-directory-level-{i:02}"));
        }
        let naive = deep.join(".skydata").join("pg").join(SOCKET_BASENAME);
        assert!(
            naive.as_os_str().as_encoded_bytes().len() > 107,
            "the fixture must actually overflow sun_path, else this test proves nothing \
             (got {} bytes)",
            naive.as_os_str().as_encoded_bytes().len()
        );

        for xdg in [None, Some("/run/user/1000")] {
            let d = socket_dir_for_project(&deep, xdg, Path::new("/tmp"));
            assert!(
                socket_path_len(&d) <= MAX_SOCKET_PATH,
                "deep project produced a {}-byte socket path: {}",
                socket_path_len(&d),
                d.join(SOCKET_BASENAME).display()
            );
            // And it is still specific to this project, not a shared bucket.
            assert!(d.to_string_lossy().contains(&path_hash(&data_dir_for(&deep))));
        }
    }

    #[test]
    fn a_shell_hostile_socket_dir_is_rejected_rather_than_quoted() {
        // The paths this module derives are always safe.
        assert!(socket_dir_is_shell_safe(Path::new("/tmp/sky-0123456789abcdef")));
        assert!(socket_dir_is_shell_safe(Path::new("/run/user/1000/sky/0123456789abcdef")));
        // A user-supplied XDG_RUNTIME_DIR is not.
        for hostile in [
            "/run/user/$(whoami)/sky/abc",
            "/run/my runtime/sky/abc",
            "/run/it's/sky/abc",
            "/run/a;rm -rf ~/sky/abc",
        ] {
            assert!(!socket_dir_is_shell_safe(Path::new(hostile)), "accepted {hostile}");
        }
    }

    /// `-D` and `-l` go through the SAME `/bin/sh -c` as `-o "-k …"`, and unlike
    /// the socket path they are derived from the project directory — the user's
    /// path, and for anyone who checks out a repository, someone else's.
    #[test]
    fn every_path_pg_ctl_shells_out_is_checked_not_just_the_socket() {
        let safe = Path::new("/tmp/plain/pg");
        let log = Path::new("/tmp/plain/postgres.log");
        let sock = Path::new("/tmp/sky-0123456789abcdef");
        assert!(pg_ctl_shell_safety_error(safe, log, sock).is_none());

        let hostile = Path::new("/tmp/x$(touch /tmp/pwned)/.skydata/pg");
        let m = pg_ctl_shell_safety_error(hostile, log, sock).expect("-D was not checked");
        assert!(m.contains("pg_ctl -D"), "{m}");
        assert!(m.contains("$(touch /tmp/pwned)"), "{m}");

        let hostile_log = Path::new("/tmp/x`id`/.skydata/postgres.log");
        let m = pg_ctl_shell_safety_error(safe, hostile_log, sock).expect("-l was not checked");
        assert!(m.contains("pg_ctl -l"), "{m}");

        let hostile_sock = Path::new("/run/user/$(id -u)/sky/abc");
        let m = pg_ctl_shell_safety_error(safe, log, hostile_sock).expect("-k was not checked");
        assert!(m.contains("-k"), "{m}");
    }

    #[test]
    fn socket_path_len_measures_the_socket_file_not_the_directory() {
        let d = Path::new("/tmp/sky-0123456789abcdef");
        assert_eq!(socket_path_len(d), d.as_os_str().len() + 1 + SOCKET_BASENAME.len());
    }

    // --- registry ---

    fn entry(data: &str, pid: i32) -> ClusterEntry {
        ClusterEntry {
            data_dir: data.to_string(),
            socket_dir: "/tmp/sky-abc".to_string(),
            pid,
            pg_version: "16.2".to_string(),
            started_at: 1_700_000_000,
            explicit: false,
            refs: Vec::new(),
        }
    }

    fn run_ref(pid: i32, cmd: &str) -> RunRef {
        RunRef { pid, cmd: cmd.to_string(), since: 1_700_000_000 }
    }

    #[test]
    fn registry_round_trips_through_json() {
        let mut reg = Registry::default();
        reg.clusters.insert("/p/alpha".into(), entry("/p/alpha/.skydata/pg", 4242));
        reg.clusters.insert("/p/beta".into(), entry("/p/beta/.skydata/pg", 4343));
        let json = serde_json::to_string(&reg).unwrap();
        let back: Registry = serde_json::from_str(&json).unwrap();
        assert_eq!(reg, back);
    }

    #[test]
    fn registry_round_trips_through_a_real_file() {
        let dir = std::env::temp_dir().join(format!("sky-reg-{}-{}", std::process::id(), now_secs()));
        let mut reg = Registry::default();
        reg.clusters.insert("/p/alpha".into(), entry("/p/alpha/.skydata/pg", 4242));
        reg.save(&dir).unwrap();
        assert_eq!(Registry::load(&dir), reg);
        // No leftover temp file beside it.
        let strays: Vec<_> = std::fs::read_dir(&dir)
            .unwrap()
            .filter_map(Result::ok)
            .map(|e| e.file_name().to_string_lossy().to_string())
            .filter(|n| n.contains(".tmp."))
            .collect();
        assert!(strays.is_empty(), "left temp files behind: {strays:?}");
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn a_missing_registry_loads_as_empty_rather_than_failing() {
        let dir = std::env::temp_dir().join("sky-registry-that-does-not-exist-9e1f");
        let _ = std::fs::remove_dir_all(&dir);
        assert_eq!(Registry::load(&dir), Registry::default());
    }

    #[test]
    fn a_corrupt_registry_loads_as_empty_rather_than_bricking_the_verb() {
        let dir = std::env::temp_dir().join(format!("sky-reg-bad-{}-{}", std::process::id(), now_secs()));
        std::fs::create_dir_all(&dir).unwrap();
        std::fs::write(dir.join(REGISTRY_FILE), "{ this is not json").unwrap();
        assert_eq!(Registry::load(&dir), Registry::default());
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn reap_drops_vanished_clears_dead_pids_and_adopts_live_ones() {
        let mut reg = Registry::default();
        reg.clusters.insert("/p/live".into(), entry("/p/live/.skydata/pg", 100));
        reg.clusters.insert("/p/dead".into(), entry("/p/dead/.skydata/pg", 200));
        reg.clusters.insert("/p/gone".into(), entry("/p/gone/.skydata/pg", 300));
        // A cluster restarted outside sky: alive, but at a pid the registry never saw.
        reg.clusters.insert("/p/moved".into(), entry("/p/moved/.skydata/pg", 400));

        let rows = reg.reap_with(&|project, e| match project {
            "/p/live" => Liveness::Running(e.pid),
            "/p/dead" => Liveness::Stopped,
            "/p/gone" => Liveness::Vanished,
            _ => Liveness::Running(999),
        });

        assert!(!reg.clusters.contains_key("/p/gone"), "a vanished data dir must be dropped");
        assert_eq!(reg.clusters["/p/live"].pid, 100);
        // The load-bearing assertion: a dead pid is ERASED, so nothing downstream
        // can print it as a running process.
        assert_eq!(reg.clusters["/p/dead"].pid, 0);
        assert_eq!(reg.clusters["/p/moved"].pid, 999);

        let statuses: Vec<_> = rows.iter().map(|(p, _, l)| (p.as_str(), *l)).collect();
        assert_eq!(
            statuses,
            vec![
                ("/p/dead", Liveness::Stopped),
                ("/p/live", Liveness::Running(100)),
                ("/p/moved", Liveness::Running(999)),
            ]
        );
    }

    #[test]
    fn reaped_registry_never_reports_a_stale_pid_as_running() {
        let mut reg = Registry::default();
        reg.clusters.insert("/p/killed".into(), entry("/p/killed/.skydata/pg", 31337));
        let rows = reg.reap_with(&|_, _| Liveness::Stopped);
        let table = render_table(&rows);
        assert!(table.contains("stopped"), "{table}");
        assert!(!table.contains("31337"), "a reaped pid leaked into `ps` output:\n{table}");
    }

    // --- run references (the `sky run` ref count) ---

    /// The headline invariant P4 exists for. Two `sky run`s hold refs; the first
    /// one's exit removes ITS ref and finds the list still non-empty, so the
    /// second one's database is not stopped underneath it.
    #[test]
    fn a_second_runs_ref_survives_the_first_runs_exit() {
        let mut e = entry("/p/app/.skydata/pg", 4242);
        e.refs = vec![run_ref(101, "sky run src/Main.sky"), run_ref(202, "sky run src/Main.sky")];

        // pid 101 exits: prune (both still alive) then drop its own ref.
        let remaining: Vec<RunRef> = prune_refs(&e.refs, &|_| true)
            .into_iter()
            .filter(|r| r.pid != 101)
            .collect();

        assert_eq!(remaining.len(), 1, "the second run's reference was lost: {remaining:?}");
        assert_eq!(remaining[0].pid, 202);
        assert!(
            !(e.explicit || remaining.is_empty()),
            "with a live reference outstanding the cluster must be kept"
        );
    }

    /// And the other half: the LAST run's exit does stop it, or `sky run` would
    /// leak a cluster per project for the rest of the session.
    #[test]
    fn the_last_ref_leaving_releases_the_cluster() {
        let mut e = entry("/p/app/.skydata/pg", 4242);
        e.refs = vec![run_ref(202, "sky run src/Main.sky")];
        let remaining: Vec<RunRef> = prune_refs(&e.refs, &|_| true)
            .into_iter()
            .filter(|r| r.pid != 202)
            .collect();
        assert!(remaining.is_empty());
        assert!(!e.explicit && remaining.is_empty(), "nothing is holding it: it must stop");
    }

    /// A cluster a user asked for by name stays up whatever `sky run` does with
    /// it. This is the documented difference between the two verbs, and it is
    /// checked BEFORE the ref list — an explicit cluster with no refs at all
    /// must still survive.
    #[test]
    fn an_explicitly_started_cluster_is_never_stopped_by_a_run_exit() {
        let mut e = entry("/p/app/.skydata/pg", 4242);
        e.explicit = true;
        e.refs = vec![run_ref(303, "sky run src/Main.sky")];
        let remaining: Vec<RunRef> = prune_refs(&e.refs, &|_| true)
            .into_iter()
            .filter(|r| r.pid != 303)
            .collect();
        assert!(remaining.is_empty(), "the run's own ref should have gone");
        assert!(
            e.explicit || !remaining.is_empty(),
            "`sky db start` asked for persistence and a `sky run` exit overrode it"
        );
    }

    /// A `SIGKILL`ed `sky run` never releases its reference. If a dead pid
    /// counted, that project's cluster would be pinned up for the rest of the
    /// session and `sky run` would have created an unstoppable database — the
    /// stale-`postmaster.pid` failure, one layer up.
    #[test]
    fn a_dead_or_recycled_ref_holder_does_not_pin_the_cluster() {
        let refs = vec![
            run_ref(101, "sky run src/Main.sky"), // gone
            run_ref(202, "sky run src/Main.sky"), // pid recycled by something else
            run_ref(303, "sky run src/Main.sky"), // genuinely still running
        ];
        // The probe mirrors `ref_is_live`: aliveness first, then identity.
        let alive = |r: &RunRef| r.pid != 101;
        let command = |pid: i32| match pid {
            202 => Some("/bin/zsh -l".to_string()),
            _ => Some("sky run src/Main.sky".to_string()),
        };
        let live = |r: &RunRef| alive(r) && command(r.pid).is_none_or(|c| c == r.cmd);

        let kept = prune_refs(&refs, &live);
        assert_eq!(kept.len(), 1, "stale refs survived: {kept:?}");
        assert_eq!(kept[0].pid, 303);
    }

    /// [`prune_refs`] takes its predicate as a parameter, so the tests above
    /// prove the arithmetic and nothing about the predicate the product uses.
    /// This one drives the real [`ref_is_live`] against real pids.
    #[test]
    fn the_real_liveness_predicate_checks_identity_and_not_just_the_pid() {
        // This process, recorded as it actually is: a reference that must hold.
        assert!(ref_is_live(&self_ref()), "a live holder's own reference was dropped");
        // Same pid, different program — the recycled-pid case, forged by
        // recording a command line this process does not have.
        let impostor = RunRef {
            pid: std::process::id() as i32,
            cmd: "/bin/zsh -l".to_string(),
            since: 0,
        };
        assert!(
            !ref_is_live(&impostor) || process_command(impostor.pid).is_none(),
            "a recycled pid counted as a live reference; only aliveness was checked"
        );
        assert!(!ref_is_live(&run_ref(0, "sky run")), "pid 0 is never a reference");
        assert!(!ref_is_live(&run_ref(-1, "sky run")));
    }

    /// Where there is no `ps` to ask, a live pid is believed. Dropping refs we
    /// cannot verify would tear a running app's database out from under it,
    /// which is a far worse outcome than keeping a cluster up too long.
    #[test]
    fn without_ps_a_live_pid_is_still_a_reference() {
        let refs = vec![run_ref(303, "sky run src/Main.sky")];
        let live = |r: &RunRef| r.pid > 0 && Option::<String>::None.is_none_or(|c| c == r.cmd);
        assert_eq!(prune_refs(&refs, &live).len(), 1);
    }

    /// An entry written by P2 has neither field. It must load, not be discarded
    /// — a registry that fails to parse orphans every cluster listed in it.
    #[test]
    fn a_pre_p4_registry_entry_loads_with_no_refs_and_no_persistence() {
        let json = r#"{"version":1,"clusters":{"/p/app":{"data_dir":"/p/app/.skydata/pg",
            "socket_dir":"/tmp/sky-abc","pid":4242,"pg_version":"14.21","started_at":1700000000}}}"#;
        let reg: Registry = serde_json::from_str(json).expect("a P2 registry must still load");
        let e = &reg.clusters["/p/app"];
        assert_eq!(e.pid, 4242);
        assert!(!e.explicit, "an entry with no recorded intent must not be treated as persistent");
        assert!(e.refs.is_empty());
    }

    // --- opting in, and the DSN ---

    #[test]
    fn the_injected_dsn_is_one_the_runtime_routes_to_postgres() {
        let dsn = dsn_for_socket_dir(Path::new("/tmp/sky-0123456789abcdef"));
        assert_eq!(dsn, "postgresql:///postgres?host=/tmp/sky-0123456789abcdef");
        // The compiler and the runtime both classify by prefix; if this ever
        // stopped starting with `postgresql://` the app would open SQLite at a
        // path named after a DSN and "work" until the first Postgres-only query.
        assert_eq!(project::driver_for_dsn(&dsn), "pgx");
    }

    #[test]
    fn an_explicit_dsn_alongside_embedded_is_refused_and_names_its_source() {
        let sources = vec![
            ("SKY_DB_PATH".to_string(), None),
            ("DATABASE_URL".to_string(), Some("postgres://prod/db".to_string())),
            ("sky.toml [database] path".to_string(), Some("./app.db".to_string())),
        ];
        let m = embedded_dsn_conflict("sky run", &sources).expect("an explicit DSN must be refused");
        assert!(m.starts_with("sky run:"), "{m}");
        // The FIRST set source, and only it: four complaints about one mistake
        // are harder to act on than one.
        assert!(m.contains("DATABASE_URL = postgres://prod/db"), "{m}");
        assert!(!m.contains("./app.db"), "every source was reported at once:\n{m}");
        // Both ways out are named, because either may be the intended one.
        assert!(m.contains("remove `embedded = true`"), "{m}");
        assert!(m.contains("unset DATABASE_URL"), "{m}");

        // A sky.toml source is fixed by editing a file, not by unsetting it.
        let toml_only = vec![(
            "sky.toml [database] path".to_string(),
            Some("./app.db".to_string()),
        )];
        let m = embedded_dsn_conflict("sky run", &toml_only).unwrap();
        assert!(m.contains("delete `sky.toml [database] path`"), "{m}");
        assert!(!m.contains("unset"), "a config line cannot be unset:\n{m}");
    }

    #[test]
    fn no_declared_dsn_is_no_conflict_and_a_blank_one_is_not_a_dsn() {
        assert_eq!(
            embedded_dsn_conflict("sky run", &[("SKY_DB_PATH".to_string(), None)]),
            None
        );
        // Exported-but-empty is how a shell profile clears a variable; treating
        // it as "set" would refuse to run for a value that configures nothing.
        assert_eq!(
            embedded_dsn_conflict("sky run", &[("SKY_DB_PATH".to_string(), Some("  ".into()))]),
            None
        );
    }

    // --- process identity ---

    #[test]
    fn a_recycled_pid_running_something_else_is_not_a_postgres() {
        assert!(command_looks_like_postgres(
            "/opt/homebrew/opt/postgresql@16/bin/postgres -D /p/.skydata/pg"
        ));
        assert!(command_looks_like_postgres("postmaster -D /var/lib/pgsql"));
        // A postmaster that has rewritten its process title.
        assert!(command_looks_like_postgres("postgres: checkpointer"));
        // The pid-reuse case: same number, entirely different program.
        assert!(!command_looks_like_postgres("/bin/zsh -l"));
        assert!(!command_looks_like_postgres("node server.js"));

        // A SUBSTRING test says yes to every one of these, and each is a real
        // process someone runs on a machine that also runs `sky db ps`. The
        // consequence is not cosmetic: a recycled pid matching one of them makes
        // `ps` report a database that is not there, and makes `sky db start`
        // refuse for as long as that process lives.
        for impostor in [
            "./app --embed --data-dir /var/lib/postgres-data",
            "go test -run TestStopPostgresOnSignal ./rt",
            "/usr/bin/tail -f /var/log/postgres.log",
            "/bin/sh -c pg_ctl start -D /x/postgres",
            "vim runtime-go/rt/pg_embed.go",
        ] {
            assert!(
                !command_looks_like_postgres(impostor),
                "classified as a postmaster: {impostor}"
            );
        }
    }

    #[test]
    fn postmaster_pid_parses_the_first_line_only() {
        let f = "41234\n/Users/dev/app/.skydata/pg\n1755100000\n5432\n/tmp/sky-abc\n";
        assert_eq!(parse_postmaster_pid(f), Some(41234));
        assert_eq!(parse_postmaster_pid(""), None);
        assert_eq!(parse_postmaster_pid("not-a-pid\n/data\n"), None);
    }

    #[test]
    fn a_dead_pid_is_not_alive() {
        // pid 0 / negatives are never a postmaster; the guard keeps `kill(0, ...)`
        // (which signals the whole process group) from ever being reached.
        assert!(!process_alive(0));
        assert!(!process_alive(-1));
        assert!(process_alive(std::process::id() as i32));
    }

    // --- binary discovery ---

    #[test]
    fn binary_discovery_precedence_is_override_then_cache_then_path() {
        let home = Path::new("/home/dev/.sky");
        let cands = bin_dir_candidates(
            Some("/opt/pg/bin"),
            &["14.21".to_string(), "16.2".to_string()],
            home,
            Some("/usr/bin:/usr/local/bin"),
            None,
        );
        assert_eq!(
            cands,
            vec![
                PathBuf::from("/opt/pg/bin"),
                // Newest cached major first.
                PathBuf::from("/home/dev/.sky/postgres/16.2/bin"),
                PathBuf::from("/home/dev/.sky/postgres/14.21/bin"),
                PathBuf::from("/usr/bin"),
                PathBuf::from("/usr/local/bin"),
            ]
        );
    }

    #[test]
    fn cached_versions_sort_numerically_not_lexically() {
        let home = Path::new("/h/.sky");
        let cands = bin_dir_candidates(
            None,
            &["9.6".to_string(), "14".to_string(), "16".to_string()],
            home,
            None,
            None,
        );
        assert_eq!(
            cands,
            vec![
                PathBuf::from("/h/.sky/postgres/16/bin"),
                PathBuf::from("/h/.sky/postgres/14/bin"),
                // Lexically "9.6" > "16"; numerically it is the oldest.
                PathBuf::from("/h/.sky/postgres/9.6/bin"),
            ]
        );
    }

    #[test]
    fn an_absent_override_and_empty_cache_leave_only_path() {
        let cands = bin_dir_candidates(Some("   "), &[], Path::new("/h/.sky"), Some("/usr/bin"), None);
        assert_eq!(cands, vec![PathBuf::from("/usr/bin")]);
    }

    /// The pin `sky db provision --embed` writes into `sky.toml` has to CHOOSE,
    /// or it is decoration: a project that states which PostgreSQL it is
    /// developed against would otherwise still get whichever one the machine
    /// happened to provision last.
    #[test]
    fn a_pinned_version_wins_inside_the_cache_but_never_over_the_override() {
        let home = Path::new("/h/.sky");
        let cached = ["14.21".to_string(), "16.2".to_string(), "18.6".to_string()];
        let cands = bin_dir_candidates(None, &cached, home, Some("/usr/bin"), Some("14.21"));
        assert_eq!(
            cands,
            vec![
                PathBuf::from("/h/.sky/postgres/14.21/bin"),
                // The rest keep the newest-first order.
                PathBuf::from("/h/.sky/postgres/18.6/bin"),
                PathBuf::from("/h/.sky/postgres/16.2/bin"),
                PathBuf::from("/usr/bin"),
            ]
        );
        // SKY_POSTGRES_BIN is the operator's deliberate choice and outranks a pin.
        let over = bin_dir_candidates(Some("/opt/pg/bin"), &cached, home, None, Some("14.21"));
        assert_eq!(over[0], PathBuf::from("/opt/pg/bin"));
        assert_eq!(over[1], PathBuf::from("/h/.sky/postgres/14.21/bin"));
        // A pin with nothing provisioned for it is simply not a candidate — it
        // must not synthesise a directory that does not exist.
        let absent = bin_dir_candidates(None, &cached, home, None, Some("17.0"));
        assert_eq!(absent[0], PathBuf::from("/h/.sky/postgres/18.6/bin"));
        assert_eq!(absent.len(), 3);
    }

    #[test]
    fn pick_bin_dir_takes_the_first_complete_candidate() {
        let cands = vec![
            PathBuf::from("/empty"),
            PathBuf::from("/partial"),
            PathBuf::from("/good"),
            PathBuf::from("/also-good"),
        ];
        let has = |d: &Path| d == Path::new("/good") || d == Path::new("/also-good");
        assert_eq!(pick_bin_dir(&cands, &has), Some(PathBuf::from("/good")));
        assert_eq!(pick_bin_dir(&cands, &|_: &Path| false), None);
    }

    #[test]
    fn the_not_found_message_names_every_lookup_and_a_way_out() {
        let m = no_binaries_message(Path::new("/home/dev/.sky"));
        for needle in [
            "SKY_POSTGRES_BIN",
            "/home/dev/.sky/postgres/<version>/bin",
            "$PATH",
            "sky db provision --embed",
            "pg_ctl",
        ] {
            assert!(m.contains(needle), "the not-found message never mentions {needle}:\n{m}");
        }
    }

    #[test]
    fn pg_version_strings_parse_across_distributions() {
        assert_eq!(parse_pg_version("pg_ctl (PostgreSQL) 14.21 (Homebrew)"), Some(("14.21".into(), 14)));
        assert_eq!(parse_pg_version("pg_ctl (PostgreSQL) 18.2"), Some(("18.2".into(), 18)));
        assert_eq!(parse_pg_version("initdb (PostgreSQL) 9.6.24"), Some(("9.6.24".into(), 9)));
        assert_eq!(parse_pg_version("pg_ctl (PostgreSQL) 16.3 (Debian 16.3-1.pgdg120+1)"), Some(("16.3".into(), 16)));
        assert_eq!(parse_pg_version("pg_ctl: command not found"), None);
        // Pre-releases. Trimming the TRAILING non-digits leaves `18beta1` whole
        // (its last character is a digit) and the parse then fails, so anyone
        // testing against a beta got "sky cannot parse this version" instead of a
        // working cluster.
        assert_eq!(parse_pg_version("pg_ctl (PostgreSQL) 18beta1"), Some(("18".into(), 18)));
        assert_eq!(parse_pg_version("pg_ctl (PostgreSQL) 17rc1"), Some(("17".into(), 17)));
        assert_eq!(parse_pg_version("pg_ctl (PostgreSQL) 16.3-1.pgdg120+1"), Some(("16.3".into(), 16)));
    }

    #[test]
    fn pg_version_file_parses_both_major_schemes() {
        assert_eq!(parse_pg_version_file("14\n"), Some(14));
        assert_eq!(parse_pg_version_file("16"), Some(16));
        assert_eq!(parse_pg_version_file("9.6\n"), Some(9));
        assert_eq!(parse_pg_version_file(""), None);
        assert_eq!(parse_pg_version_file("garbage"), None);
    }

    #[test]
    fn the_version_mismatch_message_names_both_versions_and_never_suggests_starting() {
        let m = version_mismatch_message(Path::new("/p/.skydata/pg"), 14, 16, Path::new("/opt/pg16/bin"));
        assert!(m.contains("PostgreSQL 14"));
        assert!(m.contains("PostgreSQL 16"));
        assert!(m.contains("pg_upgrade"));
        assert!(m.contains("SKY_POSTGRES_BIN"));
    }

    // --- configuration ---

    #[test]
    fn the_tuning_block_keeps_a_development_cluster_small() {
        let b = sky_conf_block();
        assert!(b.contains("shared_buffers = 32MB"), "{b}");
        assert!(b.contains("listen_addresses = ''"), "nothing may be exposed on TCP:\n{b}");
        // Semantics-affecting settings are deliberately absent: a development
        // cluster must behave exactly as production does.
        assert!(!b.contains("fsync"), "fsync=off would make dev diverge from prod:\n{b}");
        assert!(!b.contains("wal_level"), "{b}");
        assert!(!b.contains("unix_socket_directories"), "the socket dir is passed per start, not frozen:\n{b}");
    }

    #[test]
    fn tuning_a_conf_file_is_idempotent() {
        let base = "# PostgreSQL configuration file\nmax_connections = 100\n";
        let once = ensure_sky_conf(base).expect("first pass must tune");
        assert!(once.contains(SKY_CONF_MARKER));
        assert_eq!(ensure_sky_conf(&once), None, "a second pass must not duplicate the block");
        assert_eq!(once.matches("shared_buffers").count(), 1);
    }

    // --- error translation ---

    #[test]
    fn a_double_start_is_reported_in_sky_terms() {
        let raw = "pg_ctl: another server might be running; trying to start server anyway\n\
                   FATAL:  lock file \"postmaster.pid\" already exists";
        let m = translate_pg_start_error(raw, Path::new("/p/.skydata/pg"), Path::new("/tmp/sky-a"))
            .expect("a double start must be translated, not passed through raw");
        assert!(m.starts_with("sky db start:"), "{m}");
        assert!(m.contains("/p/.skydata/pg"), "the message must name the data directory:\n{m}");
        assert!(m.contains("sky db stop"), "the message must offer the way out:\n{m}");
    }

    #[test]
    fn an_incompatible_data_dir_is_reported_in_sky_terms() {
        let raw = "FATAL:  database files are incompatible with server";
        let m = translate_pg_start_error(raw, Path::new("/p/pg"), Path::new("/tmp/sky-a")).unwrap();
        assert!(m.contains("different PostgreSQL major"), "{m}");
    }

    #[test]
    fn a_socket_directory_failure_points_at_the_socket_directory() {
        let raw = "FATAL:  could not create lock file \"/tmp/sky-a/.s.PGSQL.5432.lock\": Permission denied";
        let m = translate_pg_start_error(raw, Path::new("/p/pg"), Path::new("/tmp/sky-a")).unwrap();
        assert!(m.contains("/tmp/sky-a"), "{m}");
        assert!(m.contains("XDG_RUNTIME_DIR"), "{m}");
    }

    #[test]
    fn an_unrecognised_error_is_not_dressed_up() {
        // Passing `None` is what makes the caller surface the raw text. Inventing a
        // friendly message for an unclassified failure would bury it.
        assert_eq!(
            translate_pg_start_error("FATAL: the disk is on fire", Path::new("/p"), Path::new("/tmp/s")),
            None
        );
    }

    // --- rendering ---

    #[test]
    fn the_ps_table_aligns_and_marks_a_stopped_cluster() {
        let rows = vec![
            ("/p/alpha".to_string(), entry("/p/alpha/.skydata/pg", 1234), Liveness::Running(1234)),
            ("/p/a-much-longer-project-path".to_string(), entry("/p/b/.skydata/pg", 0), Liveness::Stopped),
        ];
        let t = render_table(&rows);
        let lines: Vec<&str> = t.lines().collect();
        assert!(lines[0].starts_with("PROJECT"));
        assert!(lines[1].contains("running") && lines[1].contains("1234"));
        assert!(lines[2].contains("stopped") && lines[2].contains(" - "));
        // Columns line up: every row is padded to the same header offsets.
        let socket_col = lines[0].find("SOCKET").unwrap();
        for l in &lines[1..] {
            assert!(l.len() > socket_col, "row shorter than the SOCKET column: {l:?}");
        }
    }
}

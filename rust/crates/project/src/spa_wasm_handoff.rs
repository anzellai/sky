//! How a Sky.Spa build hands the frontend's wasm file name to a backend leg
//! that is already building.
//!
//! An SSR backend names the frontend's `main.<hash>.wasm` in its first-paint
//! page. The hash exists only once the frontend leg has built. Before v0.27.9
//! the parent `sky build` therefore built the legs in order (frontend, then
//! backend) and wrote the name into the generated backend's Sky source, so the
//! backend's `main` package changed, and recompiled, with every frontend change.
//!
//! Now the name reaches the backend as a line in its generated `skybuildinfo`
//! package (`rt.SetSpaBuiltWasmName`, read by `Spa_ssrWasmNameBuilt`), a tiny
//! package of its own. The backend leg runs while the frontend builds: its
//! Sky front end, and — when the name is not there yet — a first `go build`
//! without the name, which compiles everything, `main` included. When the name
//! arrives it rewrites the tiny package and builds again: one small compile
//! and the link.
//!
//! The protocol, through one file in the split's out dir:
//! * The parent picks a nonce for this build ([`new_nonce`]), deletes the file
//!   before either leg starts, and passes both to the backend child
//!   (`--spa-await-wasm-name <file> --spa-await-nonce <nonce>`).
//! * When the frontend leg ends, the parent writes `<nonce> ok <name>` or
//!   `<nonce> failed` through a temp file and a rename ([`deliver`]), so the
//!   backend never reads half a line.
//! * The backend ([`wait`]) ignores a line with another nonce (a stale file of
//!   an earlier build), stops when its parent process is gone, and gives up
//!   after the `go build` time limit.
//! * `failed` (the frontend did not build) builds the backend without a name,
//!   as before: its SSR page then finds the wasm by scanning the dist.

use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};

/// Where a backend leg waits for the frontend's wasm name.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Await {
    pub file: PathBuf,
    pub nonce: String,
}

/// The CLI flags the parent passes to the backend child.
pub const FLAG_FILE: &str = "--spa-await-wasm-name";
pub const FLAG_NONCE: &str = "--spa-await-nonce";

/// The file's name in the split's out dir.
pub const FILE_NAME: &str = ".sky-spa-wasm-name";

static AWAIT: std::sync::OnceLock<Await> = std::sync::OnceLock::new();

/// Set by the CLI from [`FLAG_FILE`] / [`FLAG_NONCE`]: this process's native
/// build waits for the name before its final `go build`.
pub fn set(a: Await) {
    let _ = AWAIT.set(a);
}

/// The wait this process was asked for, if any.
pub fn get() -> Option<&'static Await> {
    AWAIT.get()
}

/// What the frontend leg delivered.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Delivered {
    /// The frontend built; its wasm file name.
    Name(String),
    /// The frontend did not build (or has no wasm): no name.
    Failed,
}

/// A nonce unique to one parent build.
pub fn new_nonce() -> String {
    let t = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map_or(0, |d| d.as_nanos());
    format!("{}-{t:x}", std::process::id())
}

/// Whether `name` is a plain `main.<hex>.wasm` (or `main.wasm`) file name —
/// the only value that may reach generated source.
pub fn is_plain_wasm_name(name: &str) -> bool {
    name == "main.wasm"
        || (name.starts_with("main.")
            && name.ends_with(".wasm")
            && name.len() > "main..wasm".len()
            && name["main.".len()..name.len() - ".wasm".len()]
                .chars()
                .all(|c| c.is_ascii_hexdigit()))
}

/// Remove a file an earlier build left. Call before either leg starts.
pub fn clear(file: &Path) {
    let _ = std::fs::remove_file(file);
}

/// Write the delivery for `nonce` (temp file + rename).
pub fn deliver(file: &Path, nonce: &str, what: &Delivered) -> std::io::Result<()> {
    let line = match what {
        Delivered::Name(n) if is_plain_wasm_name(n) => format!("{nonce} ok {n}\n"),
        _ => format!("{nonce} failed\n"),
    };
    let tmp = file.with_extension(format!("tmp-{}", std::process::id()));
    std::fs::write(&tmp, line)?;
    std::fs::rename(&tmp, file).inspect_err(|_| {
        let _ = std::fs::remove_file(&tmp);
    })
}

/// The delivery for `nonce` in `text`, if it is one.
pub fn parse(text: &str, nonce: &str) -> Option<Delivered> {
    let mut it = text.split_whitespace();
    if it.next()? != nonce {
        return None;
    }
    match (it.next()?, it.next()) {
        ("ok", Some(n)) if is_plain_wasm_name(n) => Some(Delivered::Name(n.to_string())),
        ("failed", None) => Some(Delivered::Failed),
        _ => None,
    }
}

/// The delivery, if it is there now.
pub fn ready(a: &Await) -> Option<Delivered> {
    parse(&std::fs::read_to_string(&a.file).ok()?, &a.nonce)
}

/// Wait for the delivery. `parent_alive` is polled with it; `Err` when the
/// parent is gone or `limit` passes.
pub fn wait_with(
    a: &Await,
    limit: Duration,
    poll: Duration,
    parent_alive: &dyn Fn() -> bool,
) -> Result<Delivered, String> {
    let start = Instant::now();
    loop {
        if let Some(d) = ready(a) {
            return Ok(d);
        }
        if !parent_alive() {
            return Err(
                "the parent `sky build` exited before the frontend leg delivered its wasm name"
                    .to_string(),
            );
        }
        if start.elapsed() >= limit {
            return Err(format!(
                "the frontend leg did not deliver its wasm name within {}s ({})",
                limit.as_secs(),
                a.file.display()
            ));
        }
        std::thread::sleep(poll);
    }
}

/// [`wait_with`] for this process: the parent is alive while it is still this
/// process's parent.
pub fn wait(a: &Await, limit: Duration) -> Result<Delivered, String> {
    let parent = parent_pid();
    wait_with(a, limit, Duration::from_millis(50), &|| {
        parent_pid() == parent
    })
}

#[cfg(unix)]
fn parent_pid() -> u32 {
    std::os::unix::process::parent_id()
}

#[cfg(not(unix))]
fn parent_pid() -> u32 {
    0
}

#[cfg(test)]
mod tests {
    use super::*;

    fn scratch(tag: &str) -> PathBuf {
        let d = std::env::temp_dir().join(format!("sky-handoff-{tag}-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&d);
        std::fs::create_dir_all(&d).unwrap();
        d
    }

    #[test]
    fn a_delivery_round_trips_and_a_stale_one_is_ignored() {
        let d = scratch("rt");
        let a = Await {
            file: d.join(FILE_NAME),
            nonce: "n1".into(),
        };
        assert_eq!(ready(&a), None);
        // An earlier build's line (another nonce) is not this build's.
        deliver(&a.file, "n0", &Delivered::Name("main.00ff.wasm".into())).unwrap();
        assert_eq!(ready(&a), None);
        deliver(&a.file, "n1", &Delivered::Name("main.0a1b2c.wasm".into())).unwrap();
        assert_eq!(ready(&a), Some(Delivered::Name("main.0a1b2c.wasm".into())));
        deliver(&a.file, "n1", &Delivered::Failed).unwrap();
        assert_eq!(ready(&a), Some(Delivered::Failed));
        clear(&a.file);
        assert!(!a.file.exists());
        let _ = std::fs::remove_dir_all(&d);
    }

    #[test]
    fn only_a_plain_wasm_name_is_delivered() {
        for bad in ["main..wasm", "x.wasm", "main.zz.wasm", "main.0a\".wasm", ""] {
            assert!(!is_plain_wasm_name(bad), "{bad}");
            assert_eq!(parse(&format!("n ok {bad}"), "n"), None, "{bad}");
        }
        assert!(is_plain_wasm_name("main.wasm"));
        // A name that is not plain is delivered as a failure, never as text.
        let d = scratch("plain");
        let f = d.join(FILE_NAME);
        deliver(&f, "n", &Delivered::Name("../x.wasm".into())).unwrap();
        assert_eq!(std::fs::read_to_string(&f).unwrap(), "n failed\n");
        let _ = std::fs::remove_dir_all(&d);
    }

    #[test]
    fn the_wait_ends_on_delivery_parent_death_or_the_limit() {
        let d = scratch("wait");
        let a = Await {
            file: d.join(FILE_NAME),
            nonce: "n".into(),
        };
        let poll = Duration::from_millis(5);
        // The parent is gone: an error at once, not the whole limit.
        let t = Instant::now();
        let e = wait_with(&a, Duration::from_secs(60), poll, &|| false).unwrap_err();
        assert!(e.contains("parent"), "{e}");
        assert!(t.elapsed() < Duration::from_secs(5));
        // The limit.
        let e = wait_with(&a, Duration::from_millis(30), poll, &|| true).unwrap_err();
        assert!(e.contains("did not deliver"), "{e}");
        // A delivery that arrives while waiting.
        std::thread::scope(|s| {
            s.spawn(|| {
                std::thread::sleep(Duration::from_millis(40));
                deliver(&a.file, "n", &Delivered::Name("main.ab.wasm".into())).unwrap();
            });
            let got = wait_with(&a, Duration::from_secs(10), poll, &|| true).unwrap();
            assert_eq!(got, Delivered::Name("main.ab.wasm".into()));
        });
        let _ = std::fs::remove_dir_all(&d);
    }
}

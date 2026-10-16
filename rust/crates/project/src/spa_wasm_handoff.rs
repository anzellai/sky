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
//! The protocol is a pipe: the backend child's STDIN, which the parent holds.
//! * The parent starts the backend child with [`FLAG`] and a piped stdin, and
//!   gives the pipe to a [`Sender`] ([`Sender::attach`]).
//! * When the frontend leg ends, the parent writes one line, `ok <name>` or
//!   `failed`, and closes the pipe ([`Sender::deliver`]). A delivery made
//!   before the backend child started is held and written when it attaches.
//! * The frontend thread holds a [`DeliverGuard`]: if it ends without a
//!   delivery (a panic), the guard delivers `failed`.
//! * The backend ([`listen`], [`wait`]) reads the line on a thread of its own.
//!   An end of file with no line — the parent exited or was killed, or closed
//!   the pipe without a delivery — ends the wait at once with an error, on
//!   every platform. There is no file that another build of the same project
//!   could delete, and no parent pid to poll.
//! * The wait ends at most the `go build` time limit after the backend leg
//!   STARTED, its first `go build` included, so the worst case is one time
//!   limit, not two.
//! * `failed` (the frontend did not build) builds the backend without a name,
//!   as before: its SSR page then finds the wasm by scanning the dist.

use std::io::{BufRead, Read, Write};
use std::sync::{Arc, Condvar, Mutex};
use std::time::{Duration, Instant};

/// The CLI flag the parent passes to the backend child: read the frontend's
/// wasm name from stdin before the final `go build`.
pub const FLAG: &str = "--spa-await-wasm-name-on-stdin";

/// What the frontend leg delivered.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Delivered {
    /// The frontend built; its wasm file name.
    Name(String),
    /// The frontend did not build (or has no wasm): no name.
    Failed,
}

/// What the backend has read so far.
#[derive(Debug, Clone, PartialEq, Eq)]
enum Inbox {
    Pending,
    Got(Delivered),
    /// End of file (or a line that is not a delivery) before a delivery.
    Closed(String),
}

type Shared = Arc<(Mutex<Inbox>, Condvar)>;

/// The backend side: the line the parent writes to this process's stdin.
#[derive(Debug)]
pub struct Await {
    started: Instant,
    inbox: Shared,
}

static AWAIT: std::sync::OnceLock<Await> = std::sync::OnceLock::new();

/// Read the delivery from `reader` on a thread of its own. The time limit of
/// [`wait`] counts from this call.
pub fn listen(reader: impl Read + Send + 'static) -> Await {
    let inbox: Shared = Arc::new((Mutex::new(Inbox::Pending), Condvar::new()));
    let shared = Arc::clone(&inbox);
    std::thread::spawn(move || {
        let mut line = String::new();
        let got = match std::io::BufReader::new(reader).read_line(&mut line) {
            Ok(0) => Inbox::Closed("the pipe closed with no delivery".to_string()),
            Ok(_) => parse(&line).map_or_else(
                || Inbox::Closed(format!("an unreadable delivery `{}`", line.trim())),
                Inbox::Got,
            ),
            Err(e) => Inbox::Closed(format!("reading the pipe failed: {e}")),
        };
        let (m, cv) = &*shared;
        if let Ok(mut s) = m.lock() {
            *s = got;
        }
        cv.notify_all();
    });
    Await {
        started: Instant::now(),
        inbox,
    }
}

/// Set by the CLI when it sees [`FLAG`]: this process's native build waits
/// for the name (read from stdin) before its final `go build`.
pub fn set(a: Await) {
    let _ = AWAIT.set(a);
}

/// The wait this process was asked for, if any.
pub fn get() -> Option<&'static Await> {
    AWAIT.get()
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

/// The line for a delivery. A name that is not plain is delivered as a
/// failure, never as text.
pub fn line(what: &Delivered) -> String {
    match what {
        Delivered::Name(n) if is_plain_wasm_name(n) => format!("ok {n}\n"),
        _ => "failed\n".to_string(),
    }
}

/// The delivery in `text`, if it is one.
pub fn parse(text: &str) -> Option<Delivered> {
    let mut it = text.split_whitespace();
    match (it.next()?, it.next(), it.next()) {
        ("ok", Some(n), None) if is_plain_wasm_name(n) => Some(Delivered::Name(n.to_string())),
        ("failed", None, None) => Some(Delivered::Failed),
        _ => None,
    }
}

/// The delivery, if it has arrived.
///
/// Used before the backend's first `go build` to skip that build when the
/// name is already there (the serial path writes it as the child starts). A
/// line still in the pipe at that moment only costs the extra build; the
/// result is the same.
pub fn ready(a: &Await) -> Option<Delivered> {
    match a.inbox.0.lock().ok()?.clone() {
        Inbox::Got(d) => Some(d),
        _ => None,
    }
}

/// Wait for the delivery until `limit` after [`listen`]. `Err` at once when the
/// pipe closes with no delivery (the parent is gone, or failed before it
/// delivered), and when the limit passes.
pub fn wait(a: &Await, limit: Duration) -> Result<Delivered, String> {
    let deadline = a.started + limit;
    let (m, cv) = &*a.inbox;
    let mut s = m
        .lock()
        .map_err(|_| "the wasm-name reader failed".to_string())?;
    loop {
        match &*s {
            Inbox::Got(d) => return Ok(d.clone()),
            Inbox::Closed(why) => {
                return Err(format!(
                    "the parent `sky build` did not hand over the frontend's wasm name ({why}): \
                     it exited, or its frontend leg ended without a result"
                ))
            }
            Inbox::Pending => {}
        }
        let now = Instant::now();
        if now >= deadline {
            return Err(format!(
                "the frontend leg did not deliver its wasm name within {}s of the backend leg's start",
                limit.as_secs()
            ));
        }
        s = cv
            .wait_timeout(s, deadline - now)
            .map_err(|_| "the wasm-name reader failed".to_string())?
            .0;
    }
}

/// The parent side: the backend child's stdin, and the delivery for it.
#[derive(Default)]
pub struct Sender {
    state: Mutex<SendState>,
}

#[derive(Default)]
struct SendState {
    pipe: Option<Box<dyn Write + Send>>,
    pending: Option<Delivered>,
    delivered: bool,
}

impl SendState {
    /// Write the pending delivery when both it and the pipe are here, then
    /// close the pipe (the backend reads one line, then end of file).
    fn flush(&mut self) -> std::io::Result<()> {
        if self.pipe.is_none() || self.pending.is_none() {
            return Ok(());
        }
        let (Some(mut pipe), Some(what)) = (self.pipe.take(), self.pending.take()) else {
            return Ok(());
        };
        pipe.write_all(line(&what).as_bytes())?;
        pipe.flush()
    }
}

impl Sender {
    pub fn new() -> Self {
        Self::default()
    }

    /// The backend child's stdin. A delivery already made is written now.
    /// An error means the child has already exited; its own output says why.
    pub fn attach(&self, pipe: impl Write + Send + 'static) -> std::io::Result<()> {
        let Ok(mut s) = self.state.lock() else {
            return Ok(());
        };
        s.pipe = Some(Box::new(pipe));
        s.flush()
    }

    /// Deliver `what`, once (a later delivery is ignored). Written now when
    /// the backend child is attached, else held until it is.
    pub fn deliver(&self, what: Delivered) -> std::io::Result<()> {
        let Ok(mut s) = self.state.lock() else {
            return Ok(());
        };
        if s.delivered {
            return Ok(());
        }
        s.delivered = true;
        s.pending = Some(what);
        s.flush()
    }

    /// A guard that delivers [`Delivered::Failed`] when it is dropped before
    /// a delivery: the frontend thread ending early (a panic) never leaves
    /// the backend waiting.
    pub fn guard(&self) -> DeliverGuard<'_> {
        DeliverGuard(self)
    }
}

/// See [`Sender::guard`].
pub struct DeliverGuard<'a>(&'a Sender);

impl Drop for DeliverGuard<'_> {
    fn drop(&mut self) {
        let _ = self.0.deliver(Delivered::Failed);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_delivery_round_trips_through_its_line() {
        let n = Delivered::Name("main.0a1b2c.wasm".into());
        assert_eq!(parse(&line(&n)), Some(n));
        assert_eq!(parse(&line(&Delivered::Failed)), Some(Delivered::Failed));
        assert_eq!(parse("ok main.ab.wasm extra"), None);
        assert_eq!(parse(""), None);
    }

    #[test]
    fn only_a_plain_wasm_name_is_delivered() {
        for bad in ["main..wasm", "x.wasm", "main.zz.wasm", "main.0a\".wasm", ""] {
            assert!(!is_plain_wasm_name(bad), "{bad}");
            assert_eq!(parse(&format!("ok {bad}")), None, "{bad}");
        }
        assert!(is_plain_wasm_name("main.wasm"));
        // A name that is not plain is delivered as a failure, never as text.
        assert_eq!(line(&Delivered::Name("../x.wasm".into())), "failed\n");
    }

    /// A delivery made before the backend child is attached (the serial
    /// path: the frontend first) is written when it attaches.
    #[test]
    fn a_delivery_waits_for_the_pipe_and_is_written_once() {
        let (r, w) = std::io::pipe().unwrap();
        let a = listen(r);
        let tx = Sender::new();
        tx.deliver(Delivered::Name("main.ab.wasm".into())).unwrap();
        // A second delivery is ignored.
        tx.deliver(Delivered::Failed).unwrap();
        tx.attach(w).unwrap();
        assert_eq!(
            wait(&a, Duration::from_secs(10)).unwrap(),
            Delivered::Name("main.ab.wasm".into())
        );
        assert_eq!(ready(&a), Some(Delivered::Name("main.ab.wasm".into())));
    }

    /// S3 / N2: the parent closes the pipe with no delivery (it exited, was
    /// killed, or its delivery failed). The backend's wait ends at once, with
    /// an error that says so, not after the whole time limit. Before, the
    /// backend polled a file and its parent's pid (always 0 on Windows), and
    /// a delivery that failed to write left it waiting the full 600 s.
    #[test]
    fn a_closed_pipe_ends_the_wait_at_once() {
        let (r, w) = std::io::pipe().unwrap();
        let a = listen(r);
        let t = Instant::now();
        drop(w);
        let e = wait(&a, Duration::from_secs(600)).unwrap_err();
        assert!(e.contains("did not hand over"), "{e}");
        assert!(t.elapsed() < Duration::from_secs(5), "{:?}", t.elapsed());
    }

    /// S3: the frontend thread panics before it delivers. Its guard delivers
    /// `failed`, so the backend builds without a name (the dist scan), at
    /// once.
    #[test]
    fn a_frontend_thread_that_panics_delivers_failed() {
        let (r, w) = std::io::pipe().unwrap();
        let a = listen(r);
        let tx = Sender::new();
        tx.attach(w).unwrap();
        let t = Instant::now();
        let joined = std::thread::scope(|s| {
            s.spawn(|| {
                let _guard = tx.guard();
                panic!("the frontend leg failed");
            })
            .join()
        });
        assert!(joined.is_err());
        assert_eq!(
            wait(&a, Duration::from_secs(600)).unwrap(),
            Delivered::Failed
        );
        assert!(t.elapsed() < Duration::from_secs(5));
        // A guard dropped after a delivery changes nothing.
        let tx = Sender::new();
        let (r, w) = std::io::pipe().unwrap();
        let a = listen(r);
        tx.attach(w).unwrap();
        {
            let _g = tx.guard();
            tx.deliver(Delivered::Name("main.cd.wasm".into())).unwrap();
        }
        assert_eq!(
            wait(&a, Duration::from_secs(10)).unwrap(),
            Delivered::Name("main.cd.wasm".into())
        );
    }

    /// N2: the time limit counts from the backend leg's start, not from the
    /// end of its first `go build`.
    #[test]
    fn the_limit_counts_from_the_legs_start() {
        let (r, _w) = std::io::pipe().unwrap();
        let a = listen(r);
        std::thread::sleep(Duration::from_millis(300));
        let t = Instant::now();
        let e = wait(&a, Duration::from_millis(300)).unwrap_err();
        assert!(e.contains("did not deliver"), "{e}");
        assert!(
            t.elapsed() < Duration::from_millis(200),
            "{:?}",
            t.elapsed()
        );
    }
}

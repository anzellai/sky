//! A minimal PostgreSQL frontend/backend protocol client.
//!
//! # Why this exists at all
//!
//! P6 has to *execute* SQL — `CREATE ROLE`, `CREATE DATABASE`, the `REVOKE`s
//! that are the whole point of the phase — and it has to be able to *attempt a
//! cross-tenant read as an app's own role* so the boundary can be gated by
//! observation rather than by reading the SQL back. Neither is possible without
//! a client, and the shipped bundle deliberately has none:
//!
//! * `psql` is excluded on licence grounds (it links GNU readline; see the
//!   "Licensing and distribution" section of `docs/skydb/embedded-postgres.md`),
//!   and that exclusion is a decision, not an oversight to be helpfully undone.
//! * `createdb` / `createuser` are not in the shipped set either
//!   (`scripts/skydb/build-postgres-bundle.sh` ships exactly `postgres`,
//!   `initdb`, `pg_ctl`, `pg_dump`, `pg_restore`), and they could not run the
//!   `REVOKE`s in any case.
//! * `postgres --single` can run SQL, but only against a *stopped* cluster —
//!   which would mean taking every other app on a shared host down to add one.
//!
//! So sky speaks the protocol itself. It is version 3.0, unchanged since
//! PostgreSQL 7.4, and the subset needed here is small: a startup packet, the
//! authentication exchange, and simple queries.
//!
//! # Why SCRAM, and why authentication is not optional
//!
//! The security property P6 exists for — *app A's credentials must not reach
//! app B's database* — is only as strong as the authentication in front of it.
//! Under `trust` (what a development cluster uses, where the 0700 socket
//! directory IS the access control) any local process may simply *claim* to be
//! app B, and every `REVOKE` behind that is decoration. A shared cluster
//! therefore authenticates app roles with SCRAM-SHA-256, and this client
//! implements the client half of RFC 5802/7677 so the gate can connect as a real
//! role with a real password.
//!
//! `md5` is deliberately not implemented: PostgreSQL has deprecated it, sky's
//! own `pg_hba.conf` never asks for it, and a client that quietly supported it
//! would let a mis-edited `pg_hba.conf` downgrade the whole cluster in silence.
//! The refusal names the method instead. **Cleartext** (`AuthenticationCleartext
//! Password`, a `password` line in `pg_hba.conf`) is refused on the same ground
//! and with more reason, being strictly weaker than md5: it puts an app's
//! credentials on the wire in the clear. This client answered it until the
//! remediation of phase 6 — an inconsistency, since the weaker of the two
//! methods was the one that was accepted.

use std::io::{Read, Write};
use std::os::unix::net::UnixStream;
use std::path::{Path, PathBuf};
use std::time::Duration;

use crate::db_provision::Sha256;

/// Where a connection goes.
///
/// Unix sockets only, and that is a decision rather than an omission: sky
/// administers a cluster it provisioned **on this host**, where the socket
/// always exists (`unix_socket_directories` is in the managed conf block) and a
/// TCP path would mean a second way in that `pg_hba.conf` would have to be
/// widened for. An app's own DSN may still be TCP — that connection is libpq's,
/// not this client's.
#[derive(Debug, Clone)]
pub enum Target {
    /// The socket *directory* — libpq's `host=/dir` — not the socket file, plus
    /// the port, which names the socket (`.s.PGSQL.<port>`) even when nothing is
    /// listening on TCP.
    Unix(PathBuf, u16),
}

/// What the server said went wrong, with its SQLSTATE.
///
/// The code is kept separate from the message because the gates assert on it:
/// `42501` (insufficient_privilege) is the cross-tenant refusal, `28P01`
/// (invalid_password) is the impersonation refusal, and a test that matched on
/// English text would pass on a server that failed for an unrelated reason.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PgError {
    pub severity: String,
    pub code: String,
    pub message: String,
}

#[derive(Debug)]
pub enum Error {
    /// The transport failed — no server, no socket, a closed connection.
    Io(String),
    /// The server refused, with its own SQLSTATE.
    Db(PgError),
    /// The server said something this client does not implement or understand.
    Protocol(String),
}

impl std::fmt::Display for Error {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Error::Io(e) => write!(f, "{e}"),
            Error::Db(e) => write!(f, "{}: {} (SQLSTATE {})", e.severity, e.message, e.code),
            Error::Protocol(e) => write!(f, "{e}"),
        }
    }
}

impl Error {
    /// The SQLSTATE, when the failure came from the server.
    ///
    /// Only the gates read this — provisioning surfaces the whole message — but
    /// it is what makes those gates assert on `42501` rather than on English
    /// text that a differently-broken cluster could also produce.
    #[cfg_attr(not(test), allow(dead_code))]
    pub fn sqlstate(&self) -> Option<&str> {
        match self {
            Error::Db(e) => Some(e.code.as_str()),
            _ => None,
        }
    }
}

/// One row of a simple query, values rendered in their text format. `None` is
/// SQL NULL — distinct from the empty string, which is what a bare `String`
/// would flatten it into.
pub type Row = Vec<Option<String>>;

/// A connected, authenticated session.
pub struct Conn {
    stream: UnixStream,
}

/// The socket file PostgreSQL creates inside its socket directory.
pub fn socket_file(dir: &Path, port: u16) -> PathBuf {
    dir.join(format!(".s.PGSQL.{port}"))
}

impl Conn {
    /// Connect, authenticate, and wait for the first `ReadyForQuery`.
    ///
    /// `password` is `None` for `peer`/`trust` logins (sky's own administrative
    /// connection over the socket). A server that asks for a password when none
    /// was supplied is an error naming that fact, rather than a hang or a
    /// protocol violation.
    pub fn connect(
        target: &Target,
        user: &str,
        database: &str,
        password: Option<&str>,
    ) -> Result<Conn, Error> {
        let Target::Unix(dir, port) = target;
        let path = socket_file(dir, *port);
        let stream = UnixStream::connect(&path)
            .map_err(|e| Error::Io(format!("cannot connect to {}: {e}", path.display())))?;
        // A postmaster that accepted the connection and then wedged would
        // otherwise hang a provisioning run with no output at all.
        let _ = stream.set_read_timeout(Some(Duration::from_secs(30)));
        let _ = stream.set_write_timeout(Some(Duration::from_secs(30)));
        let mut c = Conn { stream };
        c.startup(user, database)?;
        c.authenticate(user, password)?;
        Ok(c)
    }

    /// Connect to a socket directory, the shape every administrative call in
    /// `db_shared` uses.
    pub fn connect_socket(dir: &Path, port: u16, user: &str, database: &str) -> Result<Conn, Error> {
        Conn::connect(&Target::Unix(dir.to_path_buf(), port), user, database, None)
    }

    fn startup(&mut self, user: &str, database: &str) -> Result<(), Error> {
        let mut body = Vec::new();
        body.extend_from_slice(&196_608i32.to_be_bytes()); // protocol 3.0
        for (k, v) in [
            ("user", user),
            ("database", database),
            ("application_name", "sky"),
            ("client_encoding", "UTF8"),
        ] {
            body.extend_from_slice(k.as_bytes());
            body.push(0);
            body.extend_from_slice(v.as_bytes());
            body.push(0);
        }
        body.push(0);
        // The startup packet is the ONE message with no type byte.
        let mut msg = ((body.len() + 4) as i32).to_be_bytes().to_vec();
        msg.extend_from_slice(&body);
        self.write_all(&msg)
    }

    fn authenticate(&mut self, user: &str, password: Option<&str>) -> Result<(), Error> {
        loop {
            let (tag, body) = self.read_message()?;
            match tag {
                b'R' => {
                    let code = be_i32(&body, 0)?;
                    match code {
                        0 => {} // AuthenticationOk — keep reading to ReadyForQuery.
                        3 => {
                            // Cleartext, refused for the same reason as md5
                            // below and more so: it is strictly weaker. A
                            // `password` line in pg_hba.conf puts every app's
                            // credentials on the wire in the clear, and
                            // answering it would let a mis-edited pg_hba.conf
                            // downgrade the whole cluster in silence — the file
                            // would read as a downgrade nobody's client
                            // complained about.
                            return Err(Error::Protocol(format!(
                                "the server asked {user} for a CLEARTEXT password, which sky does \
                                 not send — a shared cluster's pg_hba.conf must ask for \
                                 scram-sha-256"
                            )));
                        }
                        10 => {
                            let pw = password.ok_or_else(|| {
                                Error::Protocol(format!(
                                    "the server asked {user} for a SCRAM password and none was supplied"
                                ))
                            })?;
                            self.scram(&body[4..], pw)?;
                        }
                        5 => {
                            return Err(Error::Protocol(
                                "the server requested md5 authentication, which sky does not \
                                 implement — a shared cluster's pg_hba.conf must ask for \
                                 scram-sha-256"
                                    .into(),
                            ))
                        }
                        other => {
                            return Err(Error::Protocol(format!(
                                "unsupported authentication request {other}"
                            )))
                        }
                    }
                }
                b'E' => return Err(Error::Db(parse_error(&body))),
                b'Z' => return Ok(()),
                b'S' | b'K' | b'N' => {} // ParameterStatus / BackendKeyData / Notice
                other => {
                    return Err(Error::Protocol(format!(
                        "unexpected message '{}' during authentication",
                        other as char
                    )))
                }
            }
        }
    }

    /// The client half of SCRAM-SHA-256 (RFC 5802, PostgreSQL's binding in
    /// RFC 7677). `-PLUS` (channel binding) is not offered: it only means
    /// anything over TLS, and a shared cluster's app connections are unix-socket
    /// or loopback.
    fn scram(&mut self, mechanisms: &[u8], password: &str) -> Result<(), Error> {
        let mechs: Vec<String> = mechanisms
            .split(|b| *b == 0)
            .filter(|s| !s.is_empty())
            .map(|s| String::from_utf8_lossy(s).to_string())
            .collect();
        if !mechs.iter().any(|m| m == "SCRAM-SHA-256") {
            return Err(Error::Protocol(format!(
                "the server offered {mechs:?}; sky implements SCRAM-SHA-256"
            )));
        }
        let nonce = b64_encode(&random_bytes(18));
        let client_first_bare = format!("n=,r={nonce}");
        let mut initial = b"SCRAM-SHA-256\0".to_vec();
        let payload = format!("n,,{client_first_bare}");
        initial.extend_from_slice(&(payload.len() as i32).to_be_bytes());
        initial.extend_from_slice(payload.as_bytes());
        self.send(b'p', &initial)?;

        let (tag, body) = self.read_message()?;
        if tag == b'E' {
            return Err(Error::Db(parse_error(&body)));
        }
        if tag != b'R' || be_i32(&body, 0)? != 11 {
            return Err(Error::Protocol("expected a SASLContinue".into()));
        }
        let server_first = String::from_utf8_lossy(&body[4..]).to_string();
        let (mut sr, mut salt_b64, mut iters) = (String::new(), String::new(), 0u32);
        for kv in server_first.split(',') {
            match kv.split_once('=') {
                Some(("r", v)) => sr = v.to_string(),
                Some(("s", v)) => salt_b64 = v.to_string(),
                Some(("i", v)) => iters = v.parse().unwrap_or(0),
                _ => {}
            }
        }
        if !sr.starts_with(&nonce) || iters == 0 {
            return Err(Error::Protocol(format!(
                "the server's SCRAM first message is malformed: {server_first}"
            )));
        }
        let salt = b64_decode(&salt_b64)
            .ok_or_else(|| Error::Protocol("the SCRAM salt is not base64".into()))?;

        let salted = pbkdf2_sha256(password.as_bytes(), &salt, iters);
        let client_key = hmac_sha256(&salted, b"Client Key");
        let stored_key = sha256(&client_key);
        let client_final_bare = format!("c=biws,r={sr}"); // "biws" = base64("n,,")
        let auth_message = format!("{client_first_bare},{server_first},{client_final_bare}");
        let client_sig = hmac_sha256(&stored_key, auth_message.as_bytes());
        let proof: Vec<u8> = client_key
            .iter()
            .zip(client_sig.iter())
            .map(|(a, b)| a ^ b)
            .collect();
        let client_final = format!("{client_final_bare},p={}", b64_encode(&proof));
        self.send(b'p', client_final.as_bytes())?;

        // Mutual authentication: verify the server proved it holds the same
        // secret. Skipping this would accept an impostor server.
        let (tag, body) = self.read_message()?;
        if tag == b'E' {
            return Err(Error::Db(parse_error(&body)));
        }
        if tag != b'R' || be_i32(&body, 0)? != 12 {
            return Err(Error::Protocol("expected a SASLFinal".into()));
        }
        let server_final = String::from_utf8_lossy(&body[4..]).to_string();
        let v = server_final
            .split(',')
            .find_map(|kv| kv.strip_prefix("v="))
            .ok_or_else(|| Error::Protocol(format!("no server signature in {server_final}")))?;
        let server_key = hmac_sha256(&salted, b"Server Key");
        let expect = b64_encode(&hmac_sha256(&server_key, auth_message.as_bytes()));
        if v != expect {
            return Err(Error::Protocol(
                "the server's SCRAM signature did not verify".into(),
            ));
        }
        Ok(())
    }

    /// Run one or more statements through the simple-query protocol and collect
    /// every row.
    ///
    /// Simple query rather than extended: the statements here are DDL built from
    /// validated identifiers, and `CREATE DATABASE` cannot run inside the
    /// implicit transaction an extended-protocol batch would create.
    pub fn query(&mut self, sql: &str) -> Result<Vec<Row>, Error> {
        let mut m = sql.as_bytes().to_vec();
        m.push(0);
        self.send(b'Q', &m)?;
        let mut rows = Vec::new();
        let mut failure: Option<PgError> = None;
        loop {
            let (tag, body) = self.read_message()?;
            match tag {
                b'D' => rows.push(parse_data_row(&body)?),
                b'E' => failure = Some(parse_error(&body)),
                // Keep reading to ReadyForQuery even after an error: leaving the
                // unread bytes in the socket would desynchronise every later
                // statement on this connection.
                b'Z' => break,
                _ => {}
            }
        }
        match failure {
            Some(e) => Err(Error::Db(e)),
            None => Ok(rows),
        }
    }

    /// Run a statement for effect. Kept separate so provisioning code reads as
    /// a list of statements rather than a list of ignored results.
    pub fn execute(&mut self, sql: &str) -> Result<(), Error> {
        self.query(sql).map(|_| ())
    }

    /// The single scalar of a single-row, single-column query.
    pub fn scalar(&mut self, sql: &str) -> Result<Option<String>, Error> {
        Ok(self.query(sql)?.into_iter().next().and_then(|r| r.into_iter().next().flatten()))
    }

    fn send(&mut self, tag: u8, body: &[u8]) -> Result<(), Error> {
        let mut msg = vec![tag];
        msg.extend_from_slice(&((body.len() + 4) as i32).to_be_bytes());
        msg.extend_from_slice(body);
        self.write_all(&msg)
    }

    fn write_all(&mut self, bytes: &[u8]) -> Result<(), Error> {
        self.stream
            .write_all(bytes)
            .and_then(|()| self.stream.flush())
            .map_err(|e| Error::Io(format!("write to PostgreSQL failed: {e}")))
    }

    fn read_message(&mut self) -> Result<(u8, Vec<u8>), Error> {
        let mut head = [0u8; 5];
        self.stream
            .read_exact(&mut head)
            .map_err(|e| Error::Io(format!("read from PostgreSQL failed: {e}")))?;
        let len = i32::from_be_bytes([head[1], head[2], head[3], head[4]]);
        if !(4..=(64 * 1024 * 1024)).contains(&len) {
            return Err(Error::Protocol(format!("implausible message length {len}")));
        }
        let mut body = vec![0u8; (len - 4) as usize];
        self.stream
            .read_exact(&mut body)
            .map_err(|e| Error::Io(format!("read from PostgreSQL failed: {e}")))?;
        Ok((head[0], body))
    }
}

fn be_i32(b: &[u8], at: usize) -> Result<i32, Error> {
    if b.len() < at + 4 {
        return Err(Error::Protocol("truncated message".into()));
    }
    Ok(i32::from_be_bytes([b[at], b[at + 1], b[at + 2], b[at + 3]]))
}

fn parse_data_row(body: &[u8]) -> Result<Row, Error> {
    let n = i16::from_be_bytes([
        *body.first().ok_or_else(|| Error::Protocol("short DataRow".into()))?,
        *body.get(1).ok_or_else(|| Error::Protocol("short DataRow".into()))?,
    ]);
    let mut at = 2usize;
    let mut row = Vec::with_capacity(n.max(0) as usize);
    for _ in 0..n.max(0) {
        let len = be_i32(body, at)?;
        at += 4;
        if len < 0 {
            row.push(None);
            continue;
        }
        let end = at + len as usize;
        if end > body.len() {
            return Err(Error::Protocol("truncated DataRow value".into()));
        }
        row.push(Some(String::from_utf8_lossy(&body[at..end]).to_string()));
        at = end;
    }
    Ok(row)
}

/// ErrorResponse is a set of type-tagged, null-terminated fields.
pub fn parse_error(body: &[u8]) -> PgError {
    let mut e = PgError {
        severity: String::new(),
        code: String::new(),
        message: String::new(),
    };
    let mut at = 0usize;
    while at < body.len() && body[at] != 0 {
        let kind = body[at];
        at += 1;
        let end = body[at..].iter().position(|b| *b == 0).map(|p| at + p).unwrap_or(body.len());
        let value = String::from_utf8_lossy(&body[at..end]).to_string();
        match kind {
            b'S' | b'V' if e.severity.is_empty() => e.severity = value,
            b'C' => e.code = value,
            b'M' => e.message = value,
            _ => {}
        }
        at = end + 1;
    }
    e
}

// ---- crypto primitives ---------------------------------------------------
//
// SHA-256 is P3's (`db_provision::Sha256`) rather than a second copy: it already
// carries the NIST vectors as a unit test, and two implementations of one digest
// is exactly how a hash silently diverges.

fn sha256(data: &[u8]) -> [u8; 32] {
    let mut h = Sha256::new();
    h.update(data);
    h.finish()
}

/// HMAC-SHA-256 (RFC 2104). The key is at most 32 bytes everywhere SCRAM uses
/// it, so the >64-byte key branch is the digest of the key, per the RFC.
pub fn hmac_sha256(key: &[u8], msg: &[u8]) -> [u8; 32] {
    let mut k = [0u8; 64];
    if key.len() > 64 {
        k[..32].copy_from_slice(&sha256(key));
    } else {
        k[..key.len()].copy_from_slice(key);
    }
    let mut ipad = [0x36u8; 64];
    let mut opad = [0x5cu8; 64];
    for i in 0..64 {
        ipad[i] ^= k[i];
        opad[i] ^= k[i];
    }
    let mut inner = Sha256::new();
    inner.update(&ipad);
    inner.update(msg);
    let inner = inner.finish();
    let mut outer = Sha256::new();
    outer.update(&opad);
    outer.update(&inner);
    outer.finish()
}

/// PBKDF2-HMAC-SHA-256 with `dkLen == hLen`, which is the only shape SCRAM-SHA-256
/// needs (one block, no counter loop beyond `i = 1`).
pub fn pbkdf2_sha256(password: &[u8], salt: &[u8], iterations: u32) -> [u8; 32] {
    let mut salted = salt.to_vec();
    salted.extend_from_slice(&1u32.to_be_bytes());
    let mut u = hmac_sha256(password, &salted);
    let mut out = u;
    for _ in 1..iterations {
        u = hmac_sha256(password, &u);
        for i in 0..32 {
            out[i] ^= u[i];
        }
    }
    out
}

const B64: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

pub fn b64_encode(data: &[u8]) -> String {
    let mut out = String::new();
    for chunk in data.chunks(3) {
        let b = [chunk[0], *chunk.get(1).unwrap_or(&0), *chunk.get(2).unwrap_or(&0)];
        let n = ((b[0] as u32) << 16) | ((b[1] as u32) << 8) | b[2] as u32;
        out.push(B64[(n >> 18) as usize & 63] as char);
        out.push(B64[(n >> 12) as usize & 63] as char);
        out.push(if chunk.len() > 1 { B64[(n >> 6) as usize & 63] as char } else { '=' });
        out.push(if chunk.len() > 2 { B64[n as usize & 63] as char } else { '=' });
    }
    out
}

pub fn b64_decode(s: &str) -> Option<Vec<u8>> {
    let mut acc = 0u32;
    let mut bits = 0u32;
    let mut out = Vec::new();
    for c in s.bytes() {
        if c == b'=' {
            break;
        }
        let v = B64.iter().position(|b| *b == c)? as u32;
        acc = (acc << 6) | v;
        bits += 6;
        if bits >= 8 {
            bits -= 8;
            out.push((acc >> bits) as u8);
        }
    }
    Some(out)
}

/// Cryptographic randomness for SCRAM nonces and generated passwords.
///
/// `/dev/urandom` directly: the crate forbids `unsafe`, so `getrandom(2)` is out
/// without a dependency, and a fallback to a time-seeded PRNG would silently
/// turn a generated database password into something guessable. A machine
/// without `/dev/urandom` is one sky must refuse to generate a credential on,
/// so this panics rather than degrading — it is called only from paths that are
/// about to write a credential.
pub fn random_bytes(n: usize) -> Vec<u8> {
    let mut buf = vec![0u8; n];
    let mut f = std::fs::File::open("/dev/urandom")
        .expect("/dev/urandom is required to generate a credential");
    f.read_exact(&mut buf)
        .expect("/dev/urandom is required to generate a credential");
    buf
}

#[cfg(test)]
mod tests {
    use super::*;

    /// The weaker of the two password methods was the one this client answered.
    /// `md5` was refused with the reasoning that a client which quietly supports
    /// a weaker method lets a mis-edited `pg_hba.conf` downgrade the cluster in
    /// silence — and cleartext, which sends the password itself, was answered.
    ///
    /// Asserted against a socket that speaks the protocol far enough to ask, so
    /// the evidence is what sky put on the wire: the failing branch sends the
    /// password, and the server half counts the bytes.
    #[test]
    fn a_cleartext_password_request_is_refused_and_no_password_is_sent() {
        use std::io::{Read, Write};
        use std::os::unix::net::UnixListener;

        let dir = std::env::temp_dir().join(format!("sky-pgwire-clear-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        let listener = UnixListener::bind(socket_file(&dir, 5432)).unwrap();

        let server = std::thread::spawn(move || {
            let (mut s, _) = listener.accept().unwrap();
            // The startup packet: the one message with no type byte.
            let mut head = [0u8; 4];
            s.read_exact(&mut head).unwrap();
            let len = i32::from_be_bytes(head) as usize;
            s.read_exact(&mut vec![0u8; len - 4]).unwrap();
            // AuthenticationCleartextPassword.
            let mut msg = vec![b'R'];
            msg.extend_from_slice(&8i32.to_be_bytes());
            msg.extend_from_slice(&3i32.to_be_bytes());
            s.write_all(&msg).unwrap();
            s.flush().unwrap();
            // Whatever comes back. A client that answers sends a 'p' message
            // carrying the password; one that refuses closes the socket.
            s.set_read_timeout(Some(Duration::from_secs(5))).unwrap();
            let mut got = Vec::new();
            let mut buf = [0u8; 256];
            if let Ok(n) = s.read(&mut buf) {
                got.extend_from_slice(&buf[..n]);
            }
            got
        });

        let e = match Conn::connect(
            &Target::Unix(dir.clone(), 5432),
            "alpha",
            "alpha",
            Some("a-password-that-must-not-be-sent"),
        ) {
            Err(e) => e,
            Ok(_) => panic!("sky accepted cleartext authentication"),
        };
        assert!(format!("{e}").contains("CLEARTEXT"), "refused for another reason: {e}");

        let on_the_wire = server.join().unwrap();
        assert!(
            !String::from_utf8_lossy(&on_the_wire).contains("a-password-that-must-not-be-sent"),
            "sky put the password on the wire in the clear: {on_the_wire:?}"
        );
        let _ = std::fs::remove_dir_all(&dir);
    }

    /// RFC 4231 test case 2, the one every HMAC implementation is checked
    /// against. A wrong HMAC would make SCRAM fail as "invalid password", which
    /// is indistinguishable from the refusal the security gate is asserting —
    /// so this vector is what keeps that gate honest.
    #[test]
    fn hmac_sha256_matches_the_rfc_4231_vector() {
        let mac = hmac_sha256(b"Jefe", b"what do ya want for nothing?");
        assert_eq!(
            mac.iter().map(|b| format!("{b:02x}")).collect::<String>(),
            "5bdcc146bf60754e6a042426089575c75a003f089d2739839dec58b964ec3843"
        );
    }

    /// RFC 6070's PBKDF2 vectors are HMAC-SHA1; the SHA-256 equivalents are the
    /// widely-published ones for the same inputs.
    #[test]
    fn pbkdf2_sha256_matches_the_published_vector() {
        let dk = pbkdf2_sha256(b"password", b"salt", 1);
        assert_eq!(
            dk.iter().map(|b| format!("{b:02x}")).collect::<String>(),
            "120fb6cffcf8b32c43e7225256c4f837a86548c92ccc35480805987cb70be17b"
        );
        let dk = pbkdf2_sha256(b"password", b"salt", 4096);
        assert_eq!(
            dk.iter().map(|b| format!("{b:02x}")).collect::<String>(),
            "c5e478d59288c841aa530db6845c4c8d962893a001ce4e11a4963873aa98134a"
        );
    }

    /// The full SCRAM-SHA-256 exchange from RFC 7677 §3, which is what proves
    /// the client proof is assembled in the right order — an HMAC that is
    /// individually correct still fails if `AuthMessage` is built wrong.
    #[test]
    fn scram_client_proof_matches_rfc_7677() {
        let client_first_bare = "n=user,r=rOprNGfwEbeRWgbNEkqO";
        let server_first = "r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0,\
                            s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096";
        let client_final_bare = "c=biws,r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0";
        let salt = b64_decode("W22ZaJ0SNY7soEsUEjb6gQ==").unwrap();
        let salted = pbkdf2_sha256(b"pencil", &salt, 4096);
        let client_key = hmac_sha256(&salted, b"Client Key");
        let stored = sha256(&client_key);
        let auth = format!("{client_first_bare},{server_first},{client_final_bare}");
        let sig = hmac_sha256(&stored, auth.as_bytes());
        let proof: Vec<u8> = client_key.iter().zip(sig.iter()).map(|(a, b)| a ^ b).collect();
        assert_eq!(b64_encode(&proof), "dHzbZapWIk4jUhN+Ute9ytag9zjfMHgsqmmiz7AndVQ=");
        let server_key = hmac_sha256(&salted, b"Server Key");
        assert_eq!(
            b64_encode(&hmac_sha256(&server_key, auth.as_bytes())),
            "6rriTRBi23WpRR/wtup+mMhUZUn/dB5nLTJRsjl95G4="
        );
    }

    #[test]
    fn base64_round_trips_every_padding_length() {
        for n in 0..8 {
            let data: Vec<u8> = (0..n).map(|i| (i * 37 + 11) as u8).collect();
            let enc = b64_encode(&data);
            assert_eq!(b64_decode(&enc).unwrap(), data, "n = {n}");
        }
        assert_eq!(b64_encode(b"pencil"), "cGVuY2ls");
    }

    /// An ErrorResponse is what every refusal in the security gate arrives as,
    /// and the gate asserts on the SQLSTATE rather than the English text.
    #[test]
    fn error_responses_yield_their_sqlstate() {
        let mut body = Vec::new();
        for (k, v) in [
            (b'S', "FATAL"),
            (b'C', "42501"),
            (b'M', "permission denied for database alpha"),
        ] {
            body.push(k);
            body.extend_from_slice(v.as_bytes());
            body.push(0);
        }
        body.push(0);
        let e = parse_error(&body);
        assert_eq!(e.code, "42501");
        assert_eq!(e.severity, "FATAL");
        assert_eq!(e.message, "permission denied for database alpha");
    }

    #[test]
    fn random_bytes_are_not_a_constant() {
        assert_ne!(random_bytes(32), random_bytes(32));
    }
}

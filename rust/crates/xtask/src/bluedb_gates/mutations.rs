//! `--verify-mutations` — the scratch-worktree mutation runner (§9.4).
//!
//! A gate does not count until it has been proven falsifiable **by mutation**:
//! reintroduce the defect, watch the gate go red, restore, record both outputs.
//!
//! # H3 — why the runner needs its own falsifier
//!
//! §9.4 step 3's emphasis is the whole of H3. If the runner applies the patch in
//! the scratch worktree but **builds or runs the gate against the developer's
//! tree** — an absolute `CARGO_TARGET_DIR`, an inherited `cwd`, a Go build that
//! resolves `runtime-go/` from the repo root — then the mutated code never
//! executes, every gate stays green under mutation, and every mutation reports
//! `PROVEN` forever. The verifier that certifies every other gate would itself
//! be unfalsifiable.
//!
//! Four independent mechanisms stop that here:
//!
//! 1. **The binary is rebuilt inside the worktree** with `CARGO_TARGET_DIR`
//!    pointed at the scratch root, and the runner asserts the binary it is
//!    about to execute lives under the scratch root.
//! 2. **The child prints the root it resolved** (`PROBE root=…`) and the runner
//!    asserts that root is inside the worktree. Because `repo_root()` derives
//!    from `env!("CARGO_MANIFEST_DIR")`, a binary built from the dev tree
//!    reports the dev tree — and is rejected.
//! 3. **The dev tree is checked for contamination** after every `git apply`:
//!    the patch's declared `targets` must be exactly as clean as they were
//!    before the run.
//! 4. **HEAD skew is refused up front** ([`head_skew`]). The worktree is HEAD,
//!    so an uncommitted change to anything the probe compiles or reads is
//!    invisible to it — while the parent, which classifies its output, is built
//!    from exactly those files. The runner will not start against a tree that
//!    disagrees with HEAD.
//! 5. **The canary `G0.C`** — asserts `true`, paired with a no-op patch. A
//!    correct verifier reports `VACUOUS`; `PROVEN` is a harness FAIL, because a
//!    gate that cannot go red cannot have been proven. The canary's patch also
//!    touches a sentinel path, so the runner can assert the *worktree* was
//!    modified and the *dev tree* was not.
//!
//! # A case §9.4 does not name
//!
//! §9.4's table assumes the gate is GREEN before the patch, and classifies on
//! the exit code alone. That is not sufficient. A gate can be red for an
//! unrelated reason — G0.4 is red on four pre-existing dead config keys, G0.7
//! on fifty-eight untagged citations — and under §9.4's rule every such gate
//! "goes red" under any patch and reports `PROVEN` without the patch having
//! done anything. That is the same green lie the canary exists to catch, one
//! level down.
//!
//! The runner therefore classifies on the **discriminating assertion**, not on
//! the exit code: the mutation's `expect` string must be **absent from the
//! baseline output and present after the patch**. A mutation whose assertion
//! already fires before the patch is `INCONCLUSIVE-BASELINE-RED` — it proves
//! nothing — and a patch that does not make the assertion fire is `VACUOUS`.
//! This proves falsifiability of the specific property even when the gate has
//! other, unrelated failures, which is exactly the situation P0 ships in.

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};
use std::process::Command;

use super::gates_g0::expected_path;
use super::registry::{Mutation, CANARY_ID, REGISTRY};
use super::state::{GateState as Ledger, ProofOutcome};

/// Removes the scratch worktree on every exit path, including panic.
struct WorktreeGuard {
    repo: PathBuf,
    scratch: PathBuf,
    wt: PathBuf,
}

impl WorktreeGuard {
    fn create(repo: &Path) -> Result<WorktreeGuard, String> {
        // Outside the repo working tree, so it can never pollute `git status`.
        let scratch = std::env::temp_dir().join(format!(
            "sky-bluedb-mutverify-{}-{}",
            std::process::id(),
            now_millis()
        ));
        std::fs::create_dir_all(&scratch).map_err(|e| format!("scratch mkdir: {e}"))?;
        let wt = scratch.join("wt");

        let out = Command::new("git")
            .args(["worktree", "add", "--detach", "--quiet"])
            .arg(&wt)
            .arg("HEAD")
            .current_dir(repo)
            .output()
            .map_err(|e| format!("git worktree add: {e}"))?;
        if !out.status.success() {
            let _ = std::fs::remove_dir_all(&scratch);
            return Err(format!(
                "git worktree add failed: {}",
                String::from_utf8_lossy(&out.stderr).trim()
            ));
        }

        Ok(WorktreeGuard {
            repo: repo.to_path_buf(),
            scratch,
            wt,
        })
    }

    fn reset(&self) -> Result<(), String> {
        run_ok(Command::new("git").args(["reset", "--hard", "--quiet"]).current_dir(&self.wt))?;
        run_ok(Command::new("git").args(["clean", "-fdq"]).current_dir(&self.wt))
    }
}

impl Drop for WorktreeGuard {
    fn drop(&mut self) {
        let _ = Command::new("git")
            .args(["worktree", "remove", "--force"])
            .arg(&self.wt)
            .current_dir(&self.repo)
            .output();
        let _ = Command::new("git")
            .args(["worktree", "prune"])
            .current_dir(&self.repo)
            .output();
        let _ = std::fs::remove_dir_all(&self.scratch);
    }
}

fn now_millis() -> u128 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis())
        .unwrap_or(0)
}

fn run_ok(cmd: &mut Command) -> Result<(), String> {
    let out = cmd.output().map_err(|e| e.to_string())?;
    if out.status.success() {
        Ok(())
    } else {
        Err(String::from_utf8_lossy(&out.stderr).trim().to_string())
    }
}

pub fn head_sha(root: &Path) -> String {
    Command::new("git")
        .args(["rev-parse", "--short=8", "HEAD"])
        .current_dir(root)
        .output()
        .ok()
        .filter(|o| o.status.success())
        .map(|o| String::from_utf8_lossy(&o.stdout).trim().to_string())
        .unwrap_or_else(|| "unknown".into())
}

/// Files the harness WRITES ITSELF. A change to one of them is not evidence
/// that a proof decayed — it is evidence that the harness ran.
///
/// `Mutation.targets` exists precisely so that `UNVERIFIED-SINCE` fires when a
/// proof's SUBJECT moves and stays quiet otherwise, and `registry.rs` already
/// hand-excludes `gate-state.tsv` from G0.6's `targets` for exactly this reason
/// (see the comment on `G0.6/corrupt-expected`). That hand-exclusion does not
/// reach one level down, and the same defect has now been found three times in
/// three different generated files. The set is therefore enumerated here, with
/// the reason each member is in it:
///
/// * **`docs/bluedb/gate-state.tsv`** ([`super::state::STATE_PATH`]) — the
///   proof ledger. `--verify-mutations` rewrites it at the end of every run, so
///   any target set naming it decays on the act of taking the proof.
/// * **`docs/bluedb/mutations/*.expected.txt`** — the recorded RED outputs.
///   G0.6's subject *is* `docs/bluedb/mutations`, and the runner writes these
///   files in that directory on every run, so each re-derivation of G0.6's
///   proof invalidated itself at the next commit.
/// * **`docs/bluedb/STATUS.md`** ([`super::status::STATUS_PATH`]) — G0.1's
///   declared target, and the file the fast tier regenerates on every run. Its
///   header carries the HEAD sha and a timestamp, so it is byte-different on
///   *every* run: there is no resting state. A committed-and-fresh `STATUS.md`
///   always decayed G0.1's proof; an uncommitted one is stale. Observed twice —
///   commit `2e391295` took the finding count 3 → 4, and it re-fired
///   immediately after `522b04e1`.
///
/// **`*.patch` is deliberately NOT here.** A patch is hand-authored evidence,
/// not an output; editing one MUST decay the proof it belongs to, because the
/// proof is a statement about that exact patch.
///
/// Excluding the generated outputs does not weaken detection, because the
/// staleness clock was never the mechanism guarding them. It is a *hint*; the
/// two real checks are strictly stronger and untouched. If a patch stops
/// applying, `--verify-mutations` reports `MUTATION-STALE`; if a gate stops
/// detecting the defect its patch reintroduces, it reports `VACUOUS`. A
/// corrupted `*.expected.txt` is additionally caught head-on by G0.6's own
/// assertion — "recorded expected output does not contain the declared
/// assertion" — and G0.1's patch anchors on `STATUS.md`'s stable `Legend:`
/// line, so regenerating the file cannot break the mutation.
///
/// The governing rule, the one `registry.rs` already records for
/// `gate-state.tsv`: a signal that always fires is a signal nobody reads.
fn harness_generated(path: &str) -> bool {
    path == super::state::STATE_PATH
        || path == super::status::STATUS_PATH
        || (path.starts_with("docs/bluedb/mutations/") && path.ends_with(".expected.txt"))
}

/// Does a `git diff --name-only` listing name anything that is not a harness
/// output? Split out of [`targets_moved`] so the predicate is testable without
/// a repository to diff.
fn diff_moves_a_target(diff_names: &str) -> bool {
    diff_names
        .lines()
        .map(str::trim)
        .filter(|l| !l.is_empty())
        .any(|p| !harness_generated(p))
}

/// MAJOR-17: has any of the mutation's declared `targets` changed between the
/// sha the proof was taken at and `HEAD`?
///
/// A whole-tree "has anything changed" probe would mark everything unverified
/// after every commit, and a signal that always fires is a signal nobody reads —
/// which is why `Mutation` carries `targets` at all. For the same reason the
/// diff is filtered through [`harness_generated`]: a target set that contains
/// the harness's own outputs would decay on the act of taking the proof.
///
/// An unresolvable sha is treated as moved: unknown provenance is not evidence
/// of freshness.
pub fn targets_moved(root: &Path, sha: &str, targets: &[&str]) -> bool {
    let mut cmd = Command::new("git");
    cmd.args(["diff", "--name-only", sha, "HEAD", "--"])
        .args(targets)
        .current_dir(root);
    match cmd.output() {
        Ok(o) if o.status.success() => diff_moves_a_target(&String::from_utf8_lossy(&o.stdout)),
        _ => true,
    }
}

// ---------------------------------------------------------------------------
// Gate-body decay — the half of MAJOR-17 `targets` cannot express
// ---------------------------------------------------------------------------

/// Every gate body lives here. Derived from `file!()` rather than written out,
/// so moving this module moves the scan with it.
pub const GATES_DIR: &str = "rust/crates/xtask/src/bluedb_gates";

/// A tiny Rust scanner: enough to skip the things a brace counter must not
/// count. Comments (nesting, as Rust's do), strings, raw strings, byte strings
/// and char literals — while NOT mistaking a lifetime (`'static`) for one.
///
/// It is not a parser and does not need to be. Every consumer below asks only
/// two questions — "where does this brace close?" and "which identifiers appear
/// in call position?" — and both are answered correctly by a lexer that knows
/// where the literals are.
struct Scan<'a> {
    b: &'a [u8],
    i: usize,
}

impl<'a> Scan<'a> {
    fn new(s: &'a str) -> Scan<'a> {
        Scan {
            b: s.as_bytes(),
            i: 0,
        }
    }

    fn at(&self, off: usize) -> u8 {
        *self.b.get(self.i + off).unwrap_or(&0)
    }

    /// Advance past one literal / comment if we are sitting at the start of
    /// one. Returns true if something was skipped.
    fn skip_trivia(&mut self) -> bool {
        match (self.at(0), self.at(1)) {
            (b'/', b'/') => {
                while self.i < self.b.len() && self.b[self.i] != b'\n' {
                    self.i += 1;
                }
                true
            }
            (b'/', b'*') => {
                let mut depth = 0usize;
                while self.i < self.b.len() {
                    if self.at(0) == b'/' && self.at(1) == b'*' {
                        depth += 1;
                        self.i += 2;
                    } else if self.at(0) == b'*' && self.at(1) == b'/' {
                        depth -= 1;
                        self.i += 2;
                        if depth == 0 {
                            break;
                        }
                    } else {
                        self.i += 1;
                    }
                }
                true
            }
            (b'"', _) => {
                self.i += 1;
                while self.i < self.b.len() {
                    match self.b[self.i] {
                        b'\\' => self.i += 2,
                        b'"' => {
                            self.i += 1;
                            break;
                        }
                        _ => self.i += 1,
                    }
                }
                true
            }
            (b'r', c) if c == b'"' || c == b'#' => {
                // `r"…"` / `r#"…"#` — only when `r` starts a token, or the `r`
                // of `for` would open a raw string.
                if self.i > 0 && is_ident_byte(self.b[self.i - 1]) {
                    return false;
                }
                let mut hashes = 0usize;
                while self.at(1 + hashes) == b'#' {
                    hashes += 1;
                }
                if self.at(1 + hashes) != b'"' {
                    return false;
                }
                self.i += 2 + hashes;
                loop {
                    if self.i >= self.b.len() {
                        break;
                    }
                    if self.b[self.i] == b'"' {
                        let mut n = 0usize;
                        while n < hashes && self.at(1 + n) == b'#' {
                            n += 1;
                        }
                        if n == hashes {
                            self.i += 1 + hashes;
                            break;
                        }
                    }
                    self.i += 1;
                }
                true
            }
            (b'\'', _) => {
                // A char literal closes a fixed distance in; a LIFETIME does
                // not, and consuming to the next `'` there would swallow real
                // code. So the close is computed, never searched for.
                //
                // Both halves matter. `'{'` and `'}'` appear in this crate and
                // would unbalance a brace counter that did not skip them; and
                // `'"'` — `.trim_matches('"')` — opens a string literal in any
                // scanner that gets the close position wrong by one, which
                // silently swallows everything up to the next quote. That bug
                // was here: it truncated the scan of `gates_g0.rs` and
                // `gates_g2.rs` mid-file, and the gates defined after the
                // truncation point reported "body is not defined".
                let closes = if self.at(1) == b'\\' {
                    // `'\n'`, `'\''`, `'\u{1F600}'`
                    let mut j = 3usize;
                    if self.at(2) == b'u' {
                        while j < 14 && self.at(j) != b'}' {
                            j += 1;
                        }
                        j += 1;
                    }
                    (self.at(j) == b'\'').then_some(j)
                } else {
                    // One UTF-8 scalar: a lead byte plus its continuations.
                    let mut len = 1usize;
                    while len < 4 && (self.at(1 + len) & 0xc0) == 0x80 {
                        len += 1;
                    }
                    (self.at(1 + len) == b'\'').then_some(1 + len)
                };
                match closes {
                    Some(n) => {
                        self.i += n + 1;
                        true
                    }
                    None => false,
                }
            }
            _ => false,
        }
    }
}

fn is_ident_byte(c: u8) -> bool {
    c.is_ascii_alphanumeric() || c == b'_'
}

/// Byte offset just past the `}` that closes the `{` at `open`.
fn matching_brace(src: &str, open: usize) -> Option<usize> {
    let mut s = Scan::new(src);
    s.i = open;
    let mut depth = 0usize;
    while s.i < s.b.len() {
        if s.skip_trivia() {
            continue;
        }
        match s.b[s.i] {
            b'{' => depth += 1,
            b'}' => {
                depth -= 1;
                if depth == 0 {
                    return Some(s.i + 1);
                }
            }
            _ => {}
        }
        s.i += 1;
    }
    None
}

/// Every **module-level** `fn` in one file, as
/// `(name, signature-through-closing-brace)`.
///
/// The signature is deliberately part of the recorded text: changing a gate's
/// return type or its parameters changes what it can assert just as surely as
/// changing its statements.
///
/// # Why module level only
///
/// Calls are resolved by BARE NAME — the closure has no type information — and
/// associated functions make that catastrophic. `new` is defined on `Ctx`,
/// `Mutations`, `GateBodyIndex` and `Scan`; `load`, `save`, `label` and `parse`
/// are each defined two or three times. Merging them into one bucket puts every
/// `impl` in this directory inside every gate's closure, and the first
/// measurement showed exactly that: adding one unrelated `fn new` moved all
/// fifty-eight proofs at once. That is the signal-that-always-fires failure, and
/// it arrives via a name nobody was thinking about.
///
/// Module-level functions do not have that problem, because a call to one is
/// spelled with its own name. Every gate body is one; so is every helper they
/// share. The cost is that a weakening hidden inside an `impl` method is not
/// seen — `GateState::load` is the realistic example — and that is stated rather
/// than papered over. Nothing in this harness decides a gate's verdict from an
/// `impl` method today: they are constructors, accessors and the ledger
/// codec, and the ledger has its own integrity seal.
///
/// `#[cfg(test)] mod tests` is excluded by the same rule, which is correct: a
/// unit test is not part of the gate's decision.
fn fn_bodies(src: &str) -> Vec<(String, String)> {
    let mut out = Vec::new();
    let mut s = Scan::new(src);
    let mut depth = 0usize;
    while s.i < s.b.len() {
        if s.skip_trivia() {
            continue;
        }
        match s.b[s.i] {
            b'{' => {
                depth += 1;
                s.i += 1;
                continue;
            }
            b'}' => {
                depth = depth.saturating_sub(1);
                s.i += 1;
                continue;
            }
            _ => {}
        }
        if !is_ident_byte(s.b[s.i]) || (s.i > 0 && is_ident_byte(s.b[s.i - 1])) {
            s.i += 1;
            continue;
        }
        let start = s.i;
        while s.i < s.b.len() && is_ident_byte(s.b[s.i]) {
            s.i += 1;
        }
        if &src[start..s.i] != "fn" || depth != 0 {
            continue;
        }
        // `fn` <ws> NAME
        while s.i < s.b.len() && s.b[s.i].is_ascii_whitespace() {
            s.i += 1;
        }
        let ns = s.i;
        while s.i < s.b.len() && is_ident_byte(s.b[s.i]) {
            s.i += 1;
        }
        if ns == s.i {
            continue;
        }
        let name = src[ns..s.i].to_string();
        // The body's `{`. Nothing between the name and it can contain one in
        // this crate (no const generics with block defaults), and a `where`
        // clause holds only bounds.
        let mut probe = Scan::new(src);
        probe.i = s.i;
        let mut open = None;
        while probe.i < probe.b.len() {
            if probe.skip_trivia() {
                continue;
            }
            if probe.b[probe.i] == b'{' {
                open = Some(probe.i);
                break;
            }
            if probe.b[probe.i] == b';' {
                break; // a trait method declaration, or `fn` in a fn-pointer type
            }
            probe.i += 1;
        }
        let Some(open) = open else { continue };
        let Some(end) = matching_brace(src, open) else {
            continue;
        };
        out.push((name, src[ns..end].to_string()));
        s.i = end;
    }
    out
}

/// The identifiers appearing in call position in `body` — `foo(`, `.foo(`,
/// `Type::foo(`. All three reduce to the same question the closure asks: does
/// this name resolve to a function defined in the gate harness?
///
/// `format!(` and friends do not qualify: a macro's `!` sits between the name
/// and the paren.
fn called_names(body: &str) -> Vec<String> {
    let mut out = Vec::new();
    let mut s = Scan::new(body);
    while s.i < s.b.len() {
        if s.skip_trivia() {
            continue;
        }
        if !is_ident_byte(s.b[s.i]) || (s.i > 0 && is_ident_byte(s.b[s.i - 1])) {
            s.i += 1;
            continue;
        }
        let start = s.i;
        while s.i < s.b.len() && is_ident_byte(s.b[s.i]) {
            s.i += 1;
        }
        let mut j = s.i;
        while j < s.b.len() && s.b[j].is_ascii_whitespace() {
            j += 1;
        }
        if j < s.b.len() && s.b[j] == b'(' {
            out.push(body[start..s.i].to_string());
        }
    }
    out
}

/// The harness's own source, as one tree sees it.
struct GateSources {
    /// repo-relative path -> text
    files: BTreeMap<String, String>,
}

impl GateSources {
    /// name -> every body defined under that name, sorted, so the digest does
    /// not depend on file order.
    fn defs(&self) -> BTreeMap<String, Vec<String>> {
        let mut defs: BTreeMap<String, Vec<String>> = BTreeMap::new();
        for text in self.files.values() {
            for (name, body) in fn_bodies(text) {
                defs.entry(name).or_default().push(body);
            }
        }
        for v in defs.values_mut() {
            v.sort();
        }
        defs
    }

    /// `run: gates_g0::g0_6_mutations_verified` for one gate id, read out of
    /// `registry.rs`. Returns the function name.
    ///
    /// Parsed from source rather than declared on `Gate`, because a declared
    /// field is a second place to be wrong: an author who weakens a body and
    /// re-points the field would silence the very check this exists to make.
    /// The `run` pointer is the one thing that cannot lie — it is what the
    /// harness actually calls.
    fn entry_fn(&self, gate_id: &str) -> Option<String> {
        let reg = self.files.get(&format!("{GATES_DIR}/registry.rs"))?;
        let needle = format!("id: \"{gate_id}\",");
        let at = reg.find(&needle)?;
        let rest = &reg[at + needle.len()..];
        // Stop at the next gate, so a gate that somehow lost its `run` cannot
        // borrow the next one's.
        let block = match rest.find("\n        id: \"") {
            Some(n) => &rest[..n],
            None => rest,
        };
        let r = block.find("run: ")? + "run: ".len();
        let path = block[r..].split(',').next()?.trim();
        Some(path.rsplit("::").next()?.trim().to_string())
    }
}

/// The digest of everything gate `gate_id` executes, and how many distinct
/// function bodies that was.
///
/// The closure is transitive over calls *within the gate harness*: the entry
/// function, everything it calls that this crate module defines, and so on. It
/// stops at the crate boundary — `std`, `Command`, the compiler crates — which
/// is the right stopping point, because those are not where a gate is weakened.
fn body_digest(sources: &GateSources, gate_id: &str) -> Result<BTreeMap<String, String>, String> {
    let entry = sources
        .entry_fn(gate_id)
        .ok_or_else(|| format!("no `run:` for {gate_id} in {GATES_DIR}/registry.rs"))?;
    let defs = sources.defs();
    if !defs.contains_key(&entry) {
        return Err(format!("{gate_id}'s body `{entry}` is not defined in {GATES_DIR}"));
    }

    let mut seen: BTreeMap<String, Vec<String>> = BTreeMap::new();
    let mut queue = vec![entry.clone()];
    while let Some(name) = queue.pop() {
        let Some(bodies) = defs.get(&name) else {
            continue;
        };
        if seen.contains_key(&name) {
            continue;
        }
        seen.insert(name.clone(), bodies.clone());
        for body in bodies {
            for callee in called_names(body) {
                if !seen.contains_key(&callee) && defs.contains_key(&callee) {
                    queue.push(callee);
                }
            }
        }
    }

    Ok(seen
        .into_iter()
        .map(|(name, bodies)| {
            let digest = super::sha256::hex(bodies.join("\0").as_bytes());
            (name, digest)
        })
        .collect())
}

/// Reads the harness's own source out of one tree — the working tree, or a
/// commit.
fn sources_at(root: &Path, sha: Option<&str>) -> Result<GateSources, String> {
    let mut files = BTreeMap::new();
    match sha {
        None => {
            let rd = std::fs::read_dir(root.join(GATES_DIR))
                .map_err(|e| format!("reading {GATES_DIR}: {e}"))?;
            for e in rd.flatten() {
                let p = e.path();
                if p.extension().and_then(|x| x.to_str()) != Some("rs") {
                    continue;
                }
                let Some(name) = p.file_name().and_then(|x| x.to_str()) else {
                    continue;
                };
                let text = std::fs::read_to_string(&p)
                    .map_err(|e| format!("reading {}: {e}", p.display()))?;
                files.insert(format!("{GATES_DIR}/{name}"), text);
            }
        }
        Some(sha) => {
            let ls = Command::new("git")
                .args(["ls-tree", "--name-only", sha, "--"])
                .arg(format!("{GATES_DIR}/"))
                .current_dir(root)
                .output()
                .map_err(|e| format!("git ls-tree: {e}"))?;
            if !ls.status.success() {
                return Err(format!("git ls-tree at {sha} failed"));
            }
            for path in String::from_utf8_lossy(&ls.stdout).lines() {
                let path = path.trim();
                if !path.ends_with(".rs") {
                    continue;
                }
                let show = Command::new("git")
                    .arg("show")
                    .arg(format!("{sha}:{path}"))
                    .current_dir(root)
                    .output()
                    .map_err(|e| format!("git show: {e}"))?;
                if !show.status.success() {
                    return Err(format!("git show {sha}:{path} failed"));
                }
                files.insert(
                    path.to_string(),
                    String::from_utf8_lossy(&show.stdout).to_string(),
                );
            }
        }
    }
    if files.is_empty() {
        return Err(format!(
            "no gate source found under {GATES_DIR} at {}",
            sha.unwrap_or("the working tree")
        ));
    }
    Ok(files_into(files))
}

fn files_into(files: BTreeMap<String, String>) -> GateSources {
    GateSources { files }
}

/// `a`, `b` and 3 more — a finding that lists forty names is a finding nobody
/// reads either.
fn name_list(names: &[&String]) -> String {
    let shown: Vec<&str> = names.iter().take(4).map(|s| s.as_str()).collect();
    if names.len() > shown.len() {
        format!("`{}` and {} more", shown.join("`, `"), names.len() - shown.len())
    } else {
        format!("`{}`", shown.join("`, `"))
    }
}

/// **The gate-body decay check** — MAJOR-17 at function granularity.
///
/// `targets_moved` asks whether the SUBJECT a mutation reintroduces a defect
/// into has moved. It cannot ask the other question, and the other question is
/// the dangerous one: *has the gate that caught the defect been changed since it
/// caught it?* A proof is a statement about a gate body. Edit the body — make an
/// assertion vacuous, drop a branch, widen a tolerance — and the recorded
/// `PROVEN` describes code that no longer exists, while `G0.6` keeps rendering
/// it. Only ONE of the ninety registered mutations names anything under
/// `{GATES_DIR}` in its `targets`, so today that edit is invisible.
///
/// # Why this is not the signal that always fires
///
/// The repo has hit that counter-pressure twice, and the rule it wrote down —
/// *a signal that always fires is a signal nobody reads* — rules out the two
/// obvious implementations:
///
/// * **Naming the file** (`gates_g0.rs` in `targets`) decays all eight G0 proofs
///   whenever any one of the eight gates is touched, and this file has grown by
///   223 lines since the shas the current proofs were taken at. That is
///   `audit_test.go`'s shape: one file, twenty mutations, noise on every edit.
/// * **Diffing the whole harness** decays everything on every commit.
///
/// This compares the **transitive call closure of the gate's own `run`
/// function**, byte for byte, between the sha the proof was taken at and the
/// tree as it stands. Adding a new gate to `gates_g0.rs` does not touch the
/// closure of any existing one, so it fires on nothing. Renaming a local in
/// G0.2's body decays G0.2's proof and no other. Editing a helper that four
/// gates share decays exactly those four — which is not noise: a helper four
/// gates route their decision through is four gate bodies.
///
/// The signal therefore fires precisely when the code that produced a verdict
/// has changed, and the remedy is the honest one: re-derive the proof with
/// `--verify-mutations`. An unreadable tree at either end counts as moved —
/// unknown provenance is not evidence of freshness (cf. [`targets_moved`]).
pub struct GateBodyIndex {
    root: PathBuf,
    /// `None` = the tree could not be read; the reason is carried instead.
    now: Result<GateSources, String>,
    at_sha: BTreeMap<String, Result<GateSources, String>>,
    digests: BTreeMap<(String, String), Result<BTreeMap<String, String>, String>>,
}

impl GateBodyIndex {
    pub fn new(root: &Path) -> GateBodyIndex {
        GateBodyIndex {
            root: root.to_path_buf(),
            now: sources_at(root, None),
            at_sha: BTreeMap::new(),
            digests: BTreeMap::new(),
        }
    }

    fn digest_at(
        &mut self,
        gate_id: &str,
        sha: Option<&str>,
    ) -> Result<BTreeMap<String, String>, String> {
        let key = (gate_id.to_string(), sha.unwrap_or("").to_string());
        if let Some(v) = self.digests.get(&key) {
            return v.clone();
        }
        let root = self.root.clone();
        let v = match sha {
            None => self
                .now
                .as_ref()
                .map_err(|e| e.clone())
                .and_then(|s| body_digest(s, gate_id)),
            Some(sha) => {
                let entry = self
                    .at_sha
                    .entry(sha.to_string())
                    .or_insert_with(|| sources_at(&root, Some(sha)));
                entry
                    .as_ref()
                    .map_err(|e| e.clone())
                    .and_then(|s| body_digest(s, gate_id))
            }
        };
        self.digests.insert(key, v.clone());
        v
    }

    /// `None` = the gate's implementation is byte-identical to what it was when
    /// the proof was taken. `Some(reason)` = it is not, or we cannot tell.
    pub fn moved(&mut self, gate_id: &str, sha: &str) -> Option<String> {
        let then = self.digest_at(gate_id, Some(sha));
        let now = self.digest_at(gate_id, None);
        match (then, now) {
            (Ok(a), Ok(b)) => {
                if a == b {
                    return None;
                }
                let changed: Vec<&String> = a
                    .keys()
                    .filter(|k| b.get(*k).is_some_and(|v| v != &a[*k]))
                    .collect();
                let gone: Vec<&String> = a.keys().filter(|k| !b.contains_key(*k)).collect();
                let new: Vec<&String> = b.keys().filter(|k| !a.contains_key(*k)).collect();
                let mut parts = Vec::new();
                if !changed.is_empty() {
                    parts.push(format!("changed {}", name_list(&changed)));
                }
                if !gone.is_empty() {
                    parts.push(format!("no longer called {}", name_list(&gone)));
                }
                if !new.is_empty() {
                    parts.push(format!("now also calls {}", name_list(&new)));
                }
                Some(format!("the gate body {}", parts.join("; ")))
            }
            (Err(e), _) | (_, Err(e)) => Some(format!(
                "the gate body could not be read, which is not evidence of freshness: {e}"
            )),
        }
    }
}

/// The paths whose movement decays this mutation's proof: the ones its author
/// declared, **plus its own patch file**.
///
/// The patch is not an optional extra. A proof is "this patch, applied to this
/// gate, produced this RED output" — so the patch is the one input every proof
/// depends on by construction, and `harness_generated` already refuses to filter
/// `*.patch` for precisely that reason ("a patch is hand-authored evidence, not
/// an output; editing one MUST decay the proof it belongs to"). But that refusal
/// only ever mattered for a mutation whose declared `targets` happened to reach
/// the patch directory, and most do not.
///
/// `G0.1/hand-edit-status` is the case where the gap was total. Its sole
/// declared target is `docs/bluedb/STATUS.md`, which [`harness_generated`]
/// filters **unconditionally** — the fast tier regenerates the file on every
/// run with a fresh sha and timestamp, so it has no resting state and a target
/// naming it would decay on the act of running. The consequence was that G0.1's
/// proof could never decay through this route at all: rewrite the patch, and the
/// ledger still said PROVEN.
///
/// Deriving the patch into the target set closes that without a registry entry
/// to keep in sync, and without an exemption to argue for: after this, G0.1
/// decays when its patch changes (here) and when its gate body changes
/// ([`GateBodyIndex`]), which between them are the whole of what its proof
/// asserts. `STATUS.md` itself stays filtered, and should: the gate does not
/// read the file's CONTENT for the proof, it reads whether a hand edit is
/// detectable, which is a property of the body and the patch.
pub fn effective_targets(m: &Mutation) -> Vec<&'static str> {
    let mut t: Vec<&'static str> = m.targets.to_vec();
    if !t.contains(&m.patch) {
        t.push(m.patch);
    }
    t
}

/// `git status --porcelain` restricted to the given paths.
fn status_of(root: &Path, paths: &[&str]) -> String {
    Command::new("git")
        .args(["status", "--porcelain", "--"])
        .args(paths)
        .current_dir(root)
        .output()
        .map(|o| String::from_utf8_lossy(&o.stdout).to_string())
        .unwrap_or_default()
}

struct ProbeResult {
    state: String,
    root: String,
    output: String,
    exit_ok: bool,
}

/// The trees the probe compiles or reads. It takes ALL of them from the scratch
/// worktree, and `git worktree add --detach HEAD` pins that to the last COMMIT.
const MEASURED_FROM_HEAD: &[&str] = &[
    "rust",
    "runtime-go",
    "sky-stdlib",
    "examples",
    "docs/bluedb",
];

/// The paths under [`MEASURED_FROM_HEAD`] that legitimately differ from HEAD
/// during a run, because the runner reads or writes them in the DEVELOPER's
/// tree by design:
///
/// * `gate-state.tsv` and `*.expected.txt` are the runner's own outputs — it
///   writes them mid-run, so including them would make every second run refuse.
/// * `mutations/*.patch` is read from the developer's tree and applied INTO the
///   worktree, so an uncommitted patch is the one actually measured. There is
///   no skew to warn about; that is the whole point of `--verify-mutations`
///   while authoring a falsification.
fn read_from_the_dev_tree(p: &str) -> bool {
    p == "docs/bluedb/gate-state.tsv"
        || p.ends_with(".expected.txt")
        || (p.starts_with("docs/bluedb/mutations/") && p.ends_with(".patch"))
}

/// `XY <path>`, or `XY <orig> -> <new>` for a rename. The NEW path is the one
/// that exists to be measured.
fn porcelain_path(line: &str) -> Option<String> {
    let rest = line.get(3..)?.trim();
    if rest.is_empty() {
        return None;
    }
    Some(match rest.split_once(" -> ") {
        Some((_, new)) => new.trim_matches('"').to_string(),
        None => rest.trim_matches('"').to_string(),
    })
}

/// **HEAD skew** — the working-tree changes the probe cannot see.
///
/// Everything the probe compiles or reads comes from the scratch worktree, and
/// that worktree is HEAD. A change that exists only in the developer's working
/// tree is therefore INVISIBLE to the child — while the PARENT, which applies
/// the patch, classifies the output and decides PROVEN or VACUOUS, is the
/// binary the developer just built FROM that working tree. The two run
/// different code, silently, and the verdict describes a program nobody wrote.
///
/// This is not hypothetical. G0.3's falsification reported `VACUOUS` for a full
/// session against a fix that was already written: `sky_compiler`'s
/// `SKY_BLUEDB_COMPILER` support was uncommitted, so the parent lent the probe a
/// compiler and the child — built from a HEAD that had never heard of the
/// variable — found none in the pristine worktree and went red with "neither
/// rust/target/release/sky nor sky-out/sky exists". Red for the wrong reason is
/// exactly what the discriminating classifier is built to refuse, so it refused,
/// correctly, and every attempt to debug it read the parent's source and found
/// nothing wrong with it. Committing the fix — nothing else — turned it
/// `PROVEN`.
///
/// The failure was silent in the direction that wastes a session, and the same
/// skew in the other direction (an uncommitted gate body that cannot fail) would
/// mint a `PROVEN` for code that is not in the repository. So the runner
/// refuses to start rather than measure a tree that is not the one under the
/// developer's cursor. An unrunnable `git status` counts as skew: unknown
/// provenance is not evidence of freshness (cf. [`targets_moved`]).
fn head_skew(root: &Path) -> Vec<String> {
    let out = Command::new("git")
        .args(["status", "--porcelain", "--"])
        .args(MEASURED_FROM_HEAD)
        .current_dir(root)
        .output();
    let o = match out {
        Ok(o) if o.status.success() => o,
        Ok(o) => {
            return vec![format!(
                "!! `git status` failed: {}",
                String::from_utf8_lossy(&o.stderr).trim()
            )]
        }
        Err(e) => return vec![format!("!! `git status` could not run: {e}")],
    };
    String::from_utf8_lossy(&o.stdout)
        .lines()
        .filter(|l| !l.trim().is_empty())
        .filter(|l| porcelain_path(l).is_none_or(|p| !read_from_the_dev_tree(&p)))
        .map(|l| l.trim_end().to_string())
        .collect()
}

/// Does this mutation change the COMPILER, rather than the tree it compiles?
///
/// This decides whether the probe may borrow a prebuilt `sky` from the
/// developer's tree, and it is the whole safety argument for doing so.
///
/// A gate like G0.3 must build its subject, and the scratch worktree is a fresh
/// `git worktree add` with no build artefacts — so there is no compiler at all.
/// That is why G0.3's falsification was VACUOUS: the gate went red with "no
/// compiler exists" rather than its declared assertion, and the discriminating
/// classifier correctly refused to call that PROVEN.
///
/// Borrowing is safe **only** when the mutation lives outside the compiler's
/// own source. The compiler resolves `sky-stdlib/` and `runtime-go/` by walking
/// up from the project directory, so with the subject inside the worktree those
/// assets — and any mutation to them — come from the worktree. The tool is
/// external; the thing under test never is.
///
/// When the mutation patches compiler source, a prebuilt binary would NOT
/// contain it: the probe would measure an unmutated compiler while reporting a
/// mutated tree. That is a silently weakened proof, which is worse than the
/// vacuity it replaces. Then we lend nothing and the gate falls back to in-root
/// paths.
///
/// The question is answered from the PATCH, never from the declared `targets`.
/// The two look interchangeable and are not: `targets` drives the
/// `UNVERIFIED-SINCE` decay check and is deliberately broader than the diff —
/// G0.3's name `rust/crates/project/src/build.rs`, because a change there could
/// invalidate the proof, even though the patch does not touch it. Reading
/// `targets` here would conflate "could this proof be stale?" with "does this
/// patch modify the compiler?" and leave G0.3 permanently vacuous.
///
/// The patch is exact rather than heuristic: `git apply` changes precisely what
/// the diff headers name, so there is no undeclared path to miss.
fn mutation_touches_compiler(root: &Path, m: &Mutation) -> bool {
    let Ok(patch) = std::fs::read_to_string(root.join(m.patch)) else {
        // Unreadable patch: assume the worst. A missing patch is already
        // MUTATION-STALE elsewhere; it must not also buy an external compiler.
        return true;
    };
    patch.lines().any(|l| {
        l.starts_with("diff --git ")
            && l.split_whitespace().skip(2).any(|p| {
                p.trim_start_matches("a/")
                    .trim_start_matches("b/")
                    .starts_with("rust/")
            })
    })
}

/// A prebuilt `sky` from the DEVELOPER's tree, for gates that must compile
/// something. Only ever consulted via [`mutation_touches_compiler`]. Returns
/// `None` rather than a bad path, so an absent compiler stays an honest gate
/// failure instead of becoming a confusing one.
fn dev_tree_compiler(root: &Path) -> Option<PathBuf> {
    ["rust/target/release/sky", "sky-out/sky"]
        .iter()
        .map(|c| root.join(c))
        .find(|p| p.is_file())
}

/// Build `xtask` **inside the worktree** and run one gate there.
fn probe(
    guard: &WorktreeGuard,
    gate_id: &str,
    verbose: bool,
    compiler: Option<&Path>,
) -> Result<ProbeResult, String> {
    let target_dir = guard.scratch.join("target");
    let build = Command::new("cargo")
        .args(["build", "--quiet", "-p", "xtask"])
        .current_dir(guard.wt.join("rust"))
        .env("CARGO_TARGET_DIR", &target_dir)
        .output()
        .map_err(|e| format!("cargo build in worktree: {e}"))?;
    if !build.status.success() {
        return Err(format!(
            "cargo build failed in the scratch worktree:\n{}",
            tail(&String::from_utf8_lossy(&build.stderr), 40)
        ));
    }

    let bin = target_dir.join("debug").join("xtask");
    // H3 mechanism 1 — the binary we are about to run must live under the
    // scratch root, never in the developer's target dir.
    if !bin.starts_with(&guard.scratch) || !bin.exists() {
        return Err(format!(
            "refusing to run {}: the mutation probe binary must live under the scratch root {}",
            bin.display(),
            guard.scratch.display()
        ));
    }

    let mut cmd = Command::new(&bin);
    cmd.args(["bluedb-gates", "--mutation-probe", &format!("--only={gate_id}")])
        .current_dir(&guard.wt);
    // The TOOL may come from outside the worktree; the SUBJECT never does. See
    // `mutation_touches_compiler` for why that is safe here and refused there.
    if let Some(c) = compiler {
        cmd.env("SKY_BLUEDB_COMPILER", c);
    }
    let out = cmd.output().map_err(|e| format!("probe run: {e}"))?;

    let text = format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    if verbose {
        println!("{}", tail(&text, 40));
    }

    let mut state = String::new();
    let mut root = String::new();
    for line in text.lines() {
        if let Some(rest) = line.strip_prefix("PROBE ") {
            for kv in rest.split_whitespace() {
                if let Some(v) = kv.strip_prefix("state=") {
                    state = v.to_string();
                }
                if let Some(v) = kv.strip_prefix("root=") {
                    root = v.to_string();
                }
            }
        }
    }
    if state.is_empty() {
        return Err(format!(
            "probe emitted no `PROBE state=` line:\n{}",
            tail(&text, 40)
        ));
    }

    Ok(ProbeResult {
        state,
        root,
        output: text,
        exit_ok: out.status.success(),
    })
}

fn tail(s: &str, n: usize) -> String {
    let lines: Vec<&str> = s.lines().collect();
    lines[lines.len().saturating_sub(n)..].join("\n")
}

pub struct VerifyReport {
    pub outcomes: BTreeMap<String, ProofOutcome>,
    pub notes: Vec<String>,
    pub canary_ok: bool,
    pub failures: Vec<String>,
}

/// Run every registered mutation. Returns the report; the caller writes the
/// ledger and decides the exit code.
pub fn verify_all(root: &Path, verbose: bool, only: Option<&str>) -> Result<VerifyReport, String> {
    // The probe measures HEAD. Refuse before spending an hour measuring code
    // the developer did not write — see `head_skew`.
    let skew = head_skew(root);
    if !skew.is_empty() {
        return Err(format!(
            "the working tree differs from HEAD in {} path(s) the mutation probe measures:\n{}\n\n\
             The probe runs in a `git worktree add --detach HEAD`, so it compiles and reads \
             the last COMMIT — none of the above. The parent process that classifies its \
             output is the binary you just built from these files, so the two would run \
             different code and the verdict would describe neither. Commit (or stash) them \
             and re-run.",
            skew.len(),
            skew.iter()
                .map(|l| format!("  {l}"))
                .collect::<Vec<_>>()
                .join("\n")
        ));
    }

    let guard = WorktreeGuard::create(root)?;
    println!(
        "verify-mutations: scratch worktree {} (target dir {}/target)",
        guard.wt.display(),
        guard.scratch.display()
    );

    let sha = head_sha(root);
    let mut report = VerifyReport {
        outcomes: BTreeMap::new(),
        notes: Vec::new(),
        canary_ok: false,
        failures: Vec::new(),
    };

    for gate in REGISTRY {
        if let Some(o) = only {
            if gate.id != o {
                continue;
            }
        }
        for m in gate.mutations.as_slice() {
            let outcome = verify_one(root, &guard, gate.id, m, verbose, &mut report);
            let label = outcome.map(|o| o.label().to_string());
            match (outcome, label) {
                (Some(o), Some(l)) => {
                    println!("  {:<34} {}", m.id, l);
                    report.outcomes.insert(m.id.to_string(), o);

                    let want = if gate.id == CANARY_ID {
                        ProofOutcome::Vacuous
                    } else {
                        ProofOutcome::Proven
                    };
                    if gate.id == CANARY_ID {
                        report.canary_ok = o == ProofOutcome::Vacuous;
                        if o == ProofOutcome::Proven {
                            report.failures.push(format!(
                                "HARNESS FAIL: the canary {} reported PROVEN. A gate that asserts `true` cannot go red, so the runner is not measuring what it claims (H3).",
                                m.id
                            ));
                        } else if o != ProofOutcome::Vacuous {
                            report.failures.push(format!(
                                "HARNESS FAIL: the canary {} reported {} — expected VACUOUS.",
                                m.id,
                                o.label()
                            ));
                        }
                    } else if o != want {
                        report.failures.push(format!(
                            "{}: {} (required: {})",
                            m.id,
                            o.label(),
                            want.label()
                        ));
                    }
                }
                _ => {
                    println!("  {:<34} PENDING (gate not implemented yet)", m.id);
                }
            }
        }
    }

    // Record the sha each verdict was taken at.
    let mut ledger = Ledger::load(root);
    for (id, o) in &report.outcomes {
        ledger.proofs.insert(id.clone(), (*o, sha.clone()));
    }

    // A row for an id no gate declares is a live credential for a proof nobody
    // can re-derive, and this loop is where it would otherwise be re-committed.
    // Report it and fail; do NOT remove it. See `GateState::orphans` for why
    // pruning here would make deleting a patch file a way to go green.
    let registered: Vec<&str> = REGISTRY
        .iter()
        .flat_map(|g| g.mutations.as_slice().iter().map(|m| m.id))
        .collect();
    for id in ledger.orphans(registered) {
        let (o, at) = ledger.proofs[&id].clone();
        report.failures.push(format!(
            "ORPHAN PROOF: {} records {} @ {at} but no gate in the REGISTRY declares that \
             mutation id. It is checked by nothing, rendered by nothing, and re-committed by \
             every run — and re-registering the id would resurrect it as a proof that was never \
             re-derived. Delete the row from {} by hand (this run will not remove it: an \
             auto-pruning ledger makes deleting a patch file a way to silence a PROVEN).",
            id,
            o.label(),
            super::state::STATE_PATH
        ));
    }

    ledger
        .save(root)
        .map_err(|e| format!("writing the proof ledger: {e}"))?;

    Ok(report)
}

/// `None` means the gate has no implementable body yet (its baseline is
/// `NOT RUN`), so the mutation is not attempted. That is never a pass: a
/// `NOT RUN` gate already renders its goal `UNKNOWN`.
fn verify_one(
    root: &Path,
    guard: &WorktreeGuard,
    gate_id: &str,
    m: &Mutation,
    verbose: bool,
    report: &mut VerifyReport,
) -> Option<ProofOutcome> {
    if let Err(e) = guard.reset() {
        report.failures.push(format!("{}: worktree reset failed: {e}", m.id));
        return Some(ProofOutcome::MutationStale);
    }

    // A gate that builds something needs a compiler, and the scratch worktree
    // has none. Lend the dev tree's — but only when the mutation is not IN the
    // compiler, or the proof would be measuring an unmutated tool.
    let compiler = if mutation_touches_compiler(root, m) {
        None
    } else {
        dev_tree_compiler(root)
    };

    // --- baseline, measured in the worktree ------------------------------
    let base = match probe(guard, gate_id, verbose, compiler.as_deref()) {
        Ok(p) => p,
        Err(e) => {
            report.failures.push(format!("{}: baseline probe failed: {e}", m.id));
            return Some(ProofOutcome::MutationStale);
        }
    };
    if let Some(bad) = wrong_tree(guard, &base.root) {
        report.failures.push(format!("{}: {bad}", m.id));
        return Some(ProofOutcome::WrongTree);
    }
    if base.state == "NOT_RUN" {
        return None;
    }
    // The discriminating assertion must not already be firing.
    if m.expect != "<never>" && base.output.contains(m.expect) {
        report.notes.push(format!(
            "{}: the assertion {:?} already fires before the patch — the mutation proves nothing about it",
            m.id, m.expect
        ));
        return Some(ProofOutcome::InconclusiveBaselineRed);
    }
    if m.expect == "<never>" && !base.exit_ok {
        report.notes.push(format!(
            "{}: gate {gate_id} was already RED before the patch and declares no discriminating assertion",
            m.id
        ));
        return Some(ProofOutcome::InconclusiveBaselineRed);
    }

    // --- apply, in the worktree only -------------------------------------
    let patch = root.join(m.patch);
    if !patch.exists() {
        report
            .notes
            .push(format!("{}: no patch file at {}", m.id, m.patch));
        return Some(ProofOutcome::MutationStale);
    }
    let dev_before = status_of(root, m.targets);
    let applied = Command::new("git")
        .args(["apply", "--whitespace=nowarn"])
        .arg(&patch)
        .current_dir(&guard.wt)
        .output()
        .map_err(|e| e.to_string());
    match applied {
        Ok(o) if o.status.success() => {}
        Ok(o) => {
            report.notes.push(format!(
                "{}: patch no longer applies: {}",
                m.id,
                String::from_utf8_lossy(&o.stderr).trim()
            ));
            return Some(ProofOutcome::MutationStale);
        }
        Err(e) => {
            report.notes.push(format!("{}: git apply: {e}", m.id));
            return Some(ProofOutcome::MutationStale);
        }
    }

    // H3 mechanism 3 — the patch must have landed in the worktree and NOT in
    // the developer's tree.
    if status_of(&guard.wt, m.targets).trim().is_empty() {
        report.failures.push(format!(
            "{}: `git apply` reported success but the worktree is unchanged at {:?}",
            m.id, m.targets
        ));
        return Some(ProofOutcome::WrongTree);
    }
    let dev_after = status_of(root, m.targets);
    if dev_after != dev_before {
        report.failures.push(format!(
            "HARNESS FAIL: {} contaminated the developer's tree at {:?} — the mutation runner must never modify the tree it is certifying (H3)",
            m.id, m.targets
        ));
        return Some(ProofOutcome::WrongTree);
    }

    // --- the canary's sentinel arm ---------------------------------------
    if gate_id == CANARY_ID {
        let sentinel = "docs/bluedb/mutations/CANARY_TOUCHED";
        if !guard.wt.join(sentinel).exists() {
            report.failures.push(format!(
                "HARNESS FAIL: the canary patch applied but {sentinel} is absent from the worktree — the runner is not writing where it thinks it is (H3)"
            ));
            return Some(ProofOutcome::WrongTree);
        }
        if root.join(sentinel).exists() {
            report.failures.push(format!(
                "HARNESS FAIL: {sentinel} appeared in the DEVELOPER's tree — the runner applied the canary patch to the wrong tree (H3)"
            ));
            return Some(ProofOutcome::WrongTree);
        }
    }

    // --- run the mutated gate, built from and executed in the worktree ----
    let red = match probe(guard, gate_id, verbose, compiler.as_deref()) {
        Ok(p) => p,
        Err(e) => {
            report
                .failures
                .push(format!("{}: mutated probe failed: {e}", m.id));
            return Some(ProofOutcome::MutationStale);
        }
    };
    if let Some(bad) = wrong_tree(guard, &red.root) {
        report.failures.push(format!("{}: {bad}", m.id));
        return Some(ProofOutcome::WrongTree);
    }

    if m.expect == "<never>" {
        // `<never>` is classified on the EXIT CODE alone — the one place in this
        // runner with no discriminating assertion. That is correct for the
        // canary and for nothing else, so a non-canary gate reaching here is a
        // harness FAIL rather than a verdict. `registry.rs`'s
        // `the_never_sentinel_is_the_canary_s_alone` refuses it at `cargo test`
        // time; this refuses it at run time, because a static check cannot see a
        // registry a future refactor builds dynamically.
        if gate_id != CANARY_ID {
            report.failures.push(format!(
                "HARNESS FAIL: {} declares the `<never>` sentinel on {gate_id}, which is not the \
                 canary. `<never>` is classified on the exit code with NO discriminating \
                 assertion, and is exempted from G0.6's recorded-output check and from pairwise \
                 discrimination — on any gate but the canary that is a falsification requirement \
                 opted out of by spelling",
                m.id
            ));
            return Some(ProofOutcome::MutationStale);
        }
        // The canary: it asserts `true`, so staying green IS the correct
        // answer, and going red would mean the runner is not measuring what it
        // claims.
        return Some(if red.exit_ok {
            ProofOutcome::Vacuous
        } else {
            ProofOutcome::Proven
        });
    }

    if red.exit_ok || !red.output.contains(m.expect) {
        report.notes.push(format!(
            "{}: the patch applied but the assertion {:?} did not fire — the gate does not detect the defect it claims to",
            m.id, m.expect
        ));
        return Some(ProofOutcome::Vacuous);
    }

    // Record the RED output verbatim (§9.4 — "the proof is a patch plus two
    // recorded outputs"). Written to the dev tree, where it is committed.
    let expected = root.join(expected_path(m.patch));
    if let Some(parent) = expected.parent() {
        let _ = std::fs::create_dir_all(parent);
    }
    let _ = std::fs::write(&expected, normalise(&red.output, guard));

    Some(ProofOutcome::Proven)
}

/// Recorded RED outputs are committed artefacts, so they must be
/// **deterministic**: the scratch worktree path is unique per run, and leaving
/// it in the file made every proof anchored on it go `MUTATION-STALE` on the
/// next run — and leaked a developer's absolute paths into the repo.
fn normalise(output: &str, guard: &WorktreeGuard) -> String {
    let mut s = output.to_string();
    for p in [
        std::fs::canonicalize(&guard.wt).unwrap_or_else(|_| guard.wt.clone()),
        guard.wt.clone(),
    ] {
        s = s.replace(&p.to_string_lossy().to_string(), "<scratch-worktree>");
    }
    for p in [
        std::fs::canonicalize(&guard.scratch).unwrap_or_else(|_| guard.scratch.clone()),
        guard.scratch.clone(),
    ] {
        s = s.replace(&p.to_string_lossy().to_string(), "<scratch-root>");
    }
    s
}

/// H3 mechanism 2 — the child must have resolved a root inside the worktree.
fn wrong_tree(guard: &WorktreeGuard, reported_root: &str) -> Option<String> {
    if reported_root.is_empty() {
        return Some("the probe did not report the root it resolved".to_string());
    }
    let reported = std::fs::canonicalize(reported_root).unwrap_or_else(|_| PathBuf::from(reported_root));
    let wt = std::fs::canonicalize(&guard.wt).unwrap_or_else(|_| guard.wt.clone());
    if reported.starts_with(&wt) {
        None
    } else {
        Some(format!(
            "HARNESS FAIL: the probe resolved root {} — outside the scratch worktree {}. The mutated code never executed, so every mutation would report PROVEN forever (H3).",
            reported.display(),
            wt.display()
        ))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn tail_keeps_the_last_lines() {
        assert_eq!(tail("a\nb\nc", 2), "b\nc");
        assert_eq!(tail("a", 5), "a");
    }

    #[test]
    fn unresolvable_sha_counts_as_moved() {
        // Conservative by construction: unknown provenance is not freshness.
        assert!(targets_moved(
            Path::new("."),
            "0000000000000000000000000000000000000000",
            &["docs"]
        ));
    }

    /// The half that stops the proof invalidating itself. All three members of
    /// the generated set are here because each was a separate instance of the
    /// same defect:
    ///
    /// * `--verify-mutations` WRITES the `*.expected.txt` files under G0.6's
    ///   declared target `docs/bluedb/mutations`;
    /// * it rewrites `gate-state.tsv` at the end of every run;
    /// * the fast tier regenerates `STATUS.md`, G0.1's declared target, whose
    ///   header carries the HEAD sha and a timestamp — so it differs on every
    ///   run and G0.1 had no resting state at all.
    ///
    /// If a diff naming any of them counted as movement, the corresponding
    /// proof would decay at the next commit and `UNVERIFIED-SINCE` would be
    /// permanently on.
    #[test]
    fn a_regenerated_harness_output_is_not_a_moved_target() {
        assert!(harness_generated(
            "docs/bluedb/mutations/G0.2.rt-imports-bluedb.expected.txt"
        ));
        assert!(harness_generated("docs/bluedb/gate-state.tsv"));
        assert!(harness_generated("docs/bluedb/STATUS.md"));
        // The constants, so a path rename cannot silently drop a member.
        assert!(harness_generated(super::super::state::STATE_PATH));
        assert!(harness_generated(super::super::status::STATUS_PATH));
        assert!(!diff_moves_a_target(
            "docs/bluedb/mutations/G0.2.rt-imports-bluedb.expected.txt\n\
             docs/bluedb/mutations/G0.5.untagged-go-build-site.expected.txt\n\
             docs/bluedb/gate-state.tsv\n\
             docs/bluedb/STATUS.md\n"
        ));
        // G0.1's own resting state: a full-tier run touches exactly these, and
        // its proof must survive committing them.
        assert!(!diff_moves_a_target(
            "docs/bluedb/STATUS.md\ndocs/bluedb/gate-state.tsv\n"
        ));
        assert!(!diff_moves_a_target(""));
    }

    /// The other half, without which the exclusion would be a hole rather than
    /// a filter: a `*.patch` is HAND-AUTHORED evidence, not an output. The proof
    /// is a statement about that exact patch, so editing one must decay it —
    /// even though it lives in the same directory as the recorded outputs.
    #[test]
    fn a_hand_authored_patch_is_a_moved_target() {
        assert!(!harness_generated(
            "docs/bluedb/mutations/G0.2.rt-imports-bluedb.patch"
        ));
        assert!(diff_moves_a_target(
            "docs/bluedb/mutations/G0.2.rt-imports-bluedb.patch\n"
        ));
        // …and it still counts when buried among regenerated outputs, which is
        // the shape the real diff takes — including `STATUS.md`, the newest
        // member of the generated set, which must not shadow it.
        assert!(diff_moves_a_target(
            "docs/bluedb/mutations/G0.1.hand-edit-status.expected.txt\n\
             docs/bluedb/mutations/G0.2.rt-imports-bluedb.patch\n\
             docs/bluedb/STATUS.md\n\
             docs/bluedb/gate-state.tsv\n"
        ));
        // G0.1's own patch is the sharp case: its target STATUS.md is now
        // generated, but the patch that mutates it is still hand-authored
        // evidence, so editing the patch must still decay G0.1's proof.
        assert!(!harness_generated(
            "docs/bluedb/mutations/G0.1.hand-edit-status.patch"
        ));
        assert!(diff_moves_a_target(
            "docs/bluedb/STATUS.md\n\
             docs/bluedb/mutations/G0.1.hand-edit-status.patch\n"
        ));
        // Neither is the subject the gates actually guard.
        assert!(diff_moves_a_target("runtime-go/rt/live_store.go\n"));
    }

    #[test]
    fn porcelain_paths_survive_renames_quotes_and_untracked() {
        assert_eq!(
            porcelain_path(" M rust/crates/xtask/src/a.rs").unwrap(),
            "rust/crates/xtask/src/a.rs"
        );
        assert_eq!(
            porcelain_path("?? docs/bluedb/x.expected.txt").unwrap(),
            "docs/bluedb/x.expected.txt"
        );
        assert_eq!(
            porcelain_path("R  docs/a.md -> docs/b.md").unwrap(),
            "docs/b.md"
        );
        assert_eq!(
            porcelain_path("A  \"docs/with space.md\"").unwrap(),
            "docs/with space.md"
        );
        assert_eq!(porcelain_path(""), None);
    }

    /// The runner writes `gate-state.tsv` and `*.expected.txt` into the dev tree
    /// AS IT RUNS, and reads `*.patch` from there by design. If those counted as
    /// skew, the first run would poison the second and `--verify-mutations`
    /// could never be run twice.
    #[test]
    fn the_runners_own_artefacts_are_not_head_skew() {
        assert!(read_from_the_dev_tree("docs/bluedb/gate-state.tsv"));
        assert!(read_from_the_dev_tree(
            "docs/bluedb/mutations/G0.3.persistglue-unconditional.expected.txt"
        ));
        assert!(read_from_the_dev_tree(
            "docs/bluedb/mutations/G0.3.persistglue-unconditional.patch"
        ));
        // …but the gate bodies, the runtime, the stdlib and the witnesses are
        // measured from HEAD, so a working-tree-only edit to any of them is the
        // skew that made G0.3's proof read VACUOUS for a whole session.
        assert!(!read_from_the_dev_tree(
            "rust/crates/xtask/src/bluedb_gates/gates_g0.rs"
        ));
        assert!(!read_from_the_dev_tree("runtime-go/rt/rt.go"));
        assert!(!read_from_the_dev_tree("docs/bluedb/v2-architecture.md"));
    }

    /// A `git status` we cannot run is skew, not freshness — the same rule
    /// `targets_moved` applies to an unresolvable sha.
    #[test]
    fn an_unrunnable_git_status_counts_as_skew() {
        assert!(!head_skew(Path::new("/nonexistent-bluedb-root")).is_empty());
    }

    /// `G0.1/hand-edit-status` is the mutation whose declared target set could
    /// not decay AT ALL: its only entry is `STATUS.md`, which
    /// [`harness_generated`] filters unconditionally. Deriving the patch into
    /// the set is what closes it — and the derivation applies to every mutation,
    /// so there is no per-gate exemption to keep true.
    #[test]
    fn a_mutations_own_patch_is_always_one_of_its_targets() {
        let g0_1 = REGISTRY
            .iter()
            .flat_map(|g| g.mutations.as_slice())
            .find(|m| m.id == "G0.1/hand-edit-status")
            .expect("G0.1 declares its mutation");
        assert_eq!(g0_1.targets, &["docs/bluedb/STATUS.md"]);
        // Every declared target of G0.1 is filtered as a harness output, which
        // is why the decay check was silent for it.
        assert!(g0_1.targets.iter().all(|t| harness_generated(t)));

        let effective = effective_targets(g0_1);
        assert!(effective.contains(&g0_1.patch));
        assert!(!harness_generated(g0_1.patch));
        assert!(diff_moves_a_target(g0_1.patch));

        // Declared targets are kept, never replaced, and the patch is not
        // duplicated if an author has already named it.
        for m in REGISTRY.iter().flat_map(|g| g.mutations.as_slice()) {
            let e = effective_targets(m);
            assert!(m.targets.iter().all(|t| e.contains(t)), "{}", m.id);
            assert_eq!(e.iter().filter(|t| **t == m.patch).count(), 1, "{}", m.id);
        }
    }

    // -- the gate-body decay scanner ------------------------------------

    #[test]
    fn the_scanner_skips_what_a_brace_counter_must_not_count() {
        // Braces inside literals, the case that made this a lexer.
        let src = "fn a() { let x = '{'; let y = \"}}}\"; let z = r#\"{\"#; }\nfn b() {}\n";
        let names: Vec<String> = fn_bodies(src).into_iter().map(|(n, _)| n).collect();
        assert_eq!(names, vec!["a", "b"]);

        // `'\"'` — the one that opened a string and swallowed the rest of the
        // file, so every gate defined below it read as "not defined".
        let src = "fn a() { s.trim_matches('\"'); }\nfn b() { }\nfn c() {}\n";
        let names: Vec<String> = fn_bodies(src).into_iter().map(|(n, _)| n).collect();
        assert_eq!(names, vec!["a", "b", "c"]);

        // A lifetime is not a char literal: consuming to the next `'` here
        // would eat the following fn.
        let src = "fn a<'x>(s: &'x str) -> &'x str { s }\nfn b() {}\n";
        let names: Vec<String> = fn_bodies(src).into_iter().map(|(n, _)| n).collect();
        assert_eq!(names, vec!["a", "b"]);

        // Comments (nesting), escapes, and a `fn` inside a string.
        let src = "/* { /* } */ */ fn a() { /* } */ let s = \"fn nope() {\"; let c = '\\''; }\n";
        let names: Vec<String> = fn_bodies(src).into_iter().map(|(n, _)| n).collect();
        assert_eq!(names, vec!["a"]);
    }

    /// Associated functions are excluded ON PURPOSE — see [`fn_bodies`]. `new`
    /// alone is defined four times in this directory, and merging them by bare
    /// name put every `impl` inside every gate's closure.
    #[test]
    fn only_module_level_fns_enter_the_closure() {
        let src = "fn a() {}\nimpl X { fn new() -> X { X } }\n#[cfg(test)] mod t { fn helper() {} }\nfn b() {}\n";
        let names: Vec<String> = fn_bodies(src).into_iter().map(|(n, _)| n).collect();
        assert_eq!(names, vec!["a", "b"]);
    }

    #[test]
    fn call_position_is_what_counts() {
        let calls = called_names("fn a() { helper(1); format!(\"x\"); let f = other; s.trim(); }");
        assert!(calls.contains(&"helper".to_string()));
        assert!(calls.contains(&"trim".to_string()));
        // A macro's `!` sits between the name and the paren.
        assert!(!calls.contains(&"format".to_string()));
        // A value mention is not a call.
        assert!(!calls.contains(&"other".to_string()));
    }

    /// The regression that the truncation bug produced, as a standing check:
    /// EVERY registered gate's body must resolve in the working tree. A scanner
    /// that silently stops mid-file reports "the body could not be read", which
    /// [`GateBodyIndex::moved`] treats as moved — so the failure mode of a
    /// broken scanner is a decay signal on every proof at once, which is
    /// precisely the noise this check exists to avoid.
    #[test]
    fn every_registered_gate_body_resolves() {
        let root = crate::repo_root();
        let sources = sources_at(&root, None).expect("the gate sources must be readable");
        let mut broken = Vec::new();
        for gate in REGISTRY {
            if let Err(e) = body_digest(&sources, gate.id) {
                broken.push(e);
            }
        }
        assert!(broken.is_empty(), "{}", broken.join("\n"));
    }

    /// The closure must be the gate's own, not the directory's. If a helper
    /// rename or a new `impl` could put every gate in every closure, the decay
    /// check would fire on everything and be worthless.
    #[test]
    fn a_gates_closure_is_not_the_whole_directory() {
        let root = crate::repo_root();
        let sources = sources_at(&root, None).expect("the gate sources must be readable");
        let all = sources.defs().len();
        let one = body_digest(&sources, "G0.1").expect("G0.1 resolves");
        assert!(
            one.len() * 4 < all,
            "G0.1's closure is {} of {all} module-level fns — the closure is not discriminating",
            one.len()
        );
        // …and two unrelated gates must not have the same closure.
        let other = body_digest(&sources, "G0.2").expect("G0.2 resolves");
        assert_ne!(one, other);
    }

    #[test]
    fn wrong_tree_rejects_a_root_outside_the_worktree() {
        let guard = WorktreeGuard {
            repo: PathBuf::from("/repo"),
            scratch: PathBuf::from("/scratch"),
            wt: PathBuf::from("/scratch/wt"),
        };
        assert!(wrong_tree(&guard, "/repo").is_some());
        assert!(wrong_tree(&guard, "").is_some());
        assert!(wrong_tree(&guard, "/scratch/wt").is_none());
        // Drop would try to remove a non-existent worktree; harmless.
        std::mem::forget(guard);
    }
}

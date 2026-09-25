//! What a falsification proof depends on — and so when it must be re-taken.
//!
//! `harness --verify-falsifiers` applies each gate's declared mutation and
//! proves the gate goes red. A full sweep of the registry costs over an hour on
//! a laptop, and almost all of it re-establishes proofs nothing has touched.
//!
//! A proof is a statement about a small set of inputs:
//!
//! * the gate's **registration** — name, tier, platforms, budget, the exact
//!   assertion count, the declared expectation, and every mutation's id,
//!   description, target path, `from` and `to` text;
//! * the **mutation targets** — the files the mutations rewrite;
//! * the gate's **body source** — its function, and the transitive closure of
//!   the helper items it names in `harness/bodies.rs` and `harness/layer2.rs`,
//!   plus every `xtask` module it calls into (`crate::corpus::…` pulls in all of
//!   `src/corpus/`);
//! * the gate's **fixtures and scripts** — every repo path named by a string
//!   literal in that body closure (`"scripts/conformance.sh"`,
//!   `"apps/ledger"`, `"rust/crates/sky/tests/fixtures/spa-guard"`), and,
//!   transitively, every repo path a named script names in turn;
//! * the **falsifier runner** itself (`harness/falsify.rs`, `harness/child.rs`).
//!
//! [`fingerprint`] hashes all of it to one digest per gate. The ledger records
//! the digest next to the proof; an incremental run re-proves a gate only when
//! its digest changed, its proof is missing, not as declared, or older than the
//! freshness window. **Anything that cannot be hashed makes the gate re-prove**
//! — an unresolvable body, a git failure — so the error direction is always
//! "do the work", never "carry a proof".
//!
//! What is deliberately NOT an input: the compiler, runtime and stdlib as a
//! whole. A mutation proof asserts that the gate's own assertion bites its own
//! declared mutation; a compiler change that breaks a gate turns its BASELINE
//! red, which the ordinary gate run catches. Nightly and release pass `--all`,
//! which re-proves every gate regardless, so a whole-system interaction is
//! still re-proven on the release path.
//!
//! Only TRACKED files are hashed (their working-tree content). The ledger is
//! committed with the tree, so the digest must be a function of what a commit
//! contains; a stray untracked file in a laptop checkout must not make CI
//! disagree with the recorded digest.

use super::registry::{Gate, MutationKind};
use std::collections::{BTreeMap, BTreeSet, HashMap};
use std::io::Write;
use std::path::Path;
use std::process::{Command, Stdio};

/// Files every proof depends on: the runner that applies mutations and reads
/// verdicts. A change to either can change what "PROVEN" means.
const RUNNER_SOURCES: &[&str] = &[
    "rust/crates/xtask/src/harness/falsify.rs",
    "rust/crates/xtask/src/harness/child.rs",
];

/// Where gate bodies live. Items in these files form the closure namespace.
/// `main.rs` is the crate root: a body that calls `crate::roundtrip_scan`
/// reaches an item there.
const BODY_SOURCES: &[&str] = &[
    "rust/crates/xtask/src/harness/bodies.rs",
    "rust/crates/xtask/src/harness/layer2.rs",
    "rust/crates/xtask/src/main.rs",
];

/// Trees that are the TOOLCHAIN, not a gate's fixtures. A script that builds
/// the compiler names `rust/`, `runtime-go/` and `sky-stdlib/` (the embed
/// roots), and following those would make every proof depend on the whole
/// compiler — which is what `--all` at nightly and release is for. A script's
/// reference into one of these trees is not followed. A gate BODY's own string
/// literal is still honoured (a Go runtime gate names `runtime-go` because that
/// is the package it tests), except for the compiler's own source trees.
const TOOLCHAIN_ROOTS: &[&str] = &[
    "rust",
    "runtime-go",
    "sky-stdlib",
    "tools",
    "docs",
    ".github",
    "node_modules",
    "legacy-haskell-compiler",
    "legacy-sky-compiler",
    "legacy-ts-compiler",
];

/// Is `rel` inside a toolchain tree? Test fixtures under a crate's `tests/`
/// directory are fixtures, not toolchain.
fn in_toolchain(rel: &str) -> bool {
    if is_crate_test_fixture(rel) {
        return false;
    }
    TOOLCHAIN_ROOTS
        .iter()
        .any(|r| rel == *r || rel.starts_with(&format!("{r}/")))
}

fn is_crate_test_fixture(rel: &str) -> bool {
    let parts: Vec<&str> = rel.split('/').collect();
    parts.len() >= 4 && parts[0] == "rust" && parts[1] == "crates" && parts[3] == "tests"
}

/// A directory a Rust literal may name without dragging in the compiler: the
/// crate sources (`rust`, `rust/crates`, `rust/crates/<c>`, `…/<c>/src/…`) and
/// the legacy compilers are refused as DIRECTORIES; a named FILE is kept.
fn rust_literal_allowed(tracked: &Tracked, rel: &str) -> bool {
    let is_dir = !tracked.files.contains(rel);
    if !is_dir || is_crate_test_fixture(rel) {
        return true;
    }
    let parts: Vec<&str> = rel.split('/').collect();
    let compiler_tree = parts[0] == "rust"
        && (parts.len() <= 3 || (parts.len() >= 4 && parts[1] == "crates" && parts[3] == "src"));
    !(compiler_tree
        || parts[0].starts_with("legacy-")
        || parts[0] == "node_modules"
        || parts[0] == ".github")
}

const REGISTRY_SOURCE: &str = "rust/crates/xtask/src/harness/registry.rs";
const XTASK_SRC: &str = "rust/crates/xtask/src";

/// Extensions of files whose own path references are followed transitively.
const SCRIPT_EXTS: &[&str] = &["sh", "mjs", "js", "cjs", "py", "lua"];

/// The resolved inputs of one gate's proof, before hashing.
#[derive(Debug, Default, Clone)]
pub struct Inputs {
    /// The registration fingerprint (text, hashed with the files).
    pub registration: String,
    /// Repo-relative paths (files or directories) whose tracked files count.
    pub paths: BTreeSet<String>,
    /// The body-source items in the closure, by name.
    pub items: BTreeSet<String>,
}

/// Every top-level item of the body sources, by name. Items with the same name
/// in two files (a private `fn get` in both) are concatenated, which only makes
/// the closure larger — the conservative direction.
fn body_items(root: &Path) -> Result<HashMap<String, String>, String> {
    let mut items: HashMap<String, String> = HashMap::new();
    for rel in BODY_SOURCES {
        let text = std::fs::read_to_string(root.join(rel))
            .map_err(|e| format!("cannot read {rel}: {e}"))?;
        for (name, chunk) in split_items(&text) {
            // `use` and `mod` declarations carry no logic, and `main` is the
            // subcommand dispatcher that names every module in the crate —
            // following any of them would make every proof depend on all of
            // `xtask`.
            let head = chunk
                .trim_start_matches("pub(crate) ")
                .trim_start_matches("pub ");
            if head.starts_with("use ") || head.starts_with("mod ") || name == "main" {
                continue;
            }
            items
                .entry(name)
                .or_default()
                .push_str(&strip_rust_comments(&chunk));
        }
    }
    Ok(items)
}

/// Split Rust source into top-level items: a chunk starts at a column-0 line
/// that opens a `fn` / `const` / `static` / `struct` / `enum` / `impl` / `mod`
/// / `type` / `use` item, and runs to the next one. Doc comments above an item
/// fall into the PREVIOUS chunk, which only widens that chunk's closure.
fn split_items(text: &str) -> Vec<(String, String)> {
    const KEYWORDS: &[&str] = &[
        "fn ", "const ", "static ", "struct ", "enum ", "impl", "mod ", "type ", "use ",
    ];
    let mut out: Vec<(String, String)> = Vec::new();
    for line in text.split_inclusive('\n') {
        let mut rest = line;
        for prefix in ["pub(crate) ", "pub(super) ", "pub "] {
            if let Some(r) = rest.strip_prefix(prefix) {
                rest = r;
                break;
            }
        }
        let opens =
            !line.starts_with(char::is_whitespace) && KEYWORDS.iter().any(|k| rest.starts_with(k));
        if opens {
            let after_kw = rest.split_once(' ').map(|(_, r)| r).unwrap_or("");
            let name: String = after_kw
                .trim_start_matches(|c: char| c == '<' || c == '\'' || c.is_whitespace())
                .chars()
                .take_while(|c| c.is_alphanumeric() || *c == '_')
                .collect();
            out.push((name, String::new()));
        }
        if let Some(last) = out.last_mut() {
            last.1.push_str(line);
        }
    }
    out
}

/// Remove `//` and `/* */` comments outside string literals. A comment neither
/// calls a helper nor names a fixture, and an edited comment must not force a
/// re-proof; a word like `corpus` in a comment used to pull the `corpus` gate's
/// body into an unrelated gate's closure.
fn strip_rust_comments(text: &str) -> String {
    let mut out = String::with_capacity(text.len());
    let mut chars = text.chars().peekable();
    let mut in_str = false;
    while let Some(c) = chars.next() {
        if in_str {
            out.push(c);
            match c {
                '\\' => {
                    if let Some(n) = chars.next() {
                        out.push(n);
                    }
                }
                '"' => in_str = false,
                _ => {}
            }
            continue;
        }
        match (c, chars.peek()) {
            ('"', _) => {
                in_str = true;
                out.push(c);
            }
            ('/', Some('/')) => {
                for d in chars.by_ref() {
                    if d == '\n' {
                        out.push('\n');
                        break;
                    }
                }
            }
            ('/', Some('*')) => {
                chars.next();
                let mut prev = ' ';
                for d in chars.by_ref() {
                    if prev == '*' && d == '/' {
                        break;
                    }
                    prev = d;
                }
            }
            // A char literal holding a quote must not open a string.
            ('\'', Some('"')) => {
                out.push(c);
                if let Some(n) = chars.next() {
                    out.push(n);
                }
            }
            _ => out.push(c),
        }
    }
    out
}

/// The text with every string literal's CONTENT blanked, so a word inside a
/// message (`"… roundtrip …"`) is not read as a call to the item of that name.
fn code_only(text: &str) -> String {
    let mut out = String::with_capacity(text.len());
    let mut in_str = false;
    let mut chars = text.chars();
    while let Some(c) = chars.next() {
        if in_str {
            match c {
                '\\' => {
                    chars.next();
                }
                '"' => {
                    in_str = false;
                    out.push(c);
                }
                _ => {}
            }
            continue;
        }
        if c == '"' {
            in_str = true;
        }
        out.push(c);
    }
    out
}

/// Free identifiers in code: a token right after `.` is a field or a method
/// (`ctx.repo_root`), not the free item of the same name (`fn repo_root`).
fn identifiers(text: &str) -> BTreeSet<&str> {
    let is_id = |c: char| c.is_alphanumeric() || c == '_';
    let mut out = BTreeSet::new();
    let mut start: Option<usize> = None;
    let mut prev_non_id = ' ';
    for (i, c) in text
        .char_indices()
        .chain(std::iter::once((text.len(), ' ')))
    {
        match (is_id(c), start) {
            (true, None) => start = Some(i),
            (false, Some(s)) => {
                if prev_non_id != '.' {
                    out.insert(&text[s..i]);
                }
                start = None;
                if !c.is_whitespace() {
                    prev_non_id = c;
                } else {
                    prev_non_id = ' ';
                }
            }
            (false, None) => {
                if !c.is_whitespace() {
                    prev_non_id = c;
                }
            }
            (true, Some(_)) => {}
        }
    }
    out
}

/// Identifiers immediately followed by `::` — the module paths a chunk calls.
fn module_prefixes(text: &str) -> BTreeSet<String> {
    let mut out = BTreeSet::new();
    let bytes = text.as_bytes();
    let mut i = 0;
    while let Some(off) = text[i..].find("::") {
        let at = i + off;
        let mut s = at;
        while s > 0 && (bytes[s - 1].is_ascii_alphanumeric() || bytes[s - 1] == b'_') {
            s -= 1;
        }
        if s < at {
            out.insert(text[s..at].to_string());
        }
        i = at + 2;
    }
    out
}

/// String literals in Rust source. Naive (escapes handled, raw strings read as
/// ordinary ones), which errs towards extracting MORE candidates.
fn string_literals(text: &str) -> Vec<String> {
    let mut out = Vec::new();
    let mut chars = text.chars().peekable();
    while let Some(c) = chars.next() {
        if c != '"' {
            continue;
        }
        let mut lit = String::new();
        while let Some(d) = chars.next() {
            match d {
                '\\' => {
                    chars.next();
                }
                '"' => break,
                _ => lit.push(d),
            }
        }
        out.push(lit);
    }
    out
}

/// Path-like tokens in a script: maximal runs of path characters that contain a
/// `/`. `$ROOT/scripts/lib/x.sh` yields `scripts/lib/x.sh` once the `$ROOT`
/// prefix is cut at the `$`/`{`/`}` boundary.
fn script_path_tokens(text: &str) -> Vec<String> {
    let is_path = |c: char| c.is_ascii_alphanumeric() || matches!(c, '_' | '-' | '.' | '/');
    let mut out = Vec::new();
    let mut cur = String::new();
    for c in text.chars().chain(std::iter::once(' ')) {
        if is_path(c) {
            cur.push(c);
        } else {
            if cur.contains('/') {
                out.push(std::mem::take(&mut cur));
            }
            cur.clear();
        }
    }
    out
}

/// The repo's tracked files. Every resolution is made against THIS set, never
/// against the filesystem, so a digest cannot depend on a build artefact or a
/// scratch file that exists in one checkout and not in another.
pub struct Tracked {
    files: BTreeSet<String>,
}

impl Tracked {
    pub fn load(root: &Path) -> Result<Tracked, String> {
        let out = Command::new("git")
            .arg("-C")
            .arg(root)
            .args(["ls-files", "-z"])
            .output()
            .map_err(|e| format!("git ls-files: {e}"))?;
        if !out.status.success() {
            return Err(format!(
                "git ls-files failed: {}",
                String::from_utf8_lossy(&out.stderr).trim()
            ));
        }
        Ok(Tracked {
            files: out
                .stdout
                .split(|b| *b == 0)
                .filter(|s| !s.is_empty())
                .map(|s| String::from_utf8_lossy(s).into_owned())
                .collect(),
        })
    }

    /// Is `rel` a tracked file, or a directory that holds one?
    fn names(&self, rel: &str) -> bool {
        self.under(rel).next().is_some()
    }

    /// Tracked files equal to `rel` or beneath it.
    fn under(&self, rel: &str) -> impl Iterator<Item = &String> {
        let dir = format!("{rel}/");
        let exact = self.files.get(rel).into_iter();
        let below = self
            .files
            .range(dir.clone()..)
            .take_while(move |f| f.starts_with(&dir));
        exact.chain(below)
    }
}

/// Normalise a candidate and keep it iff it names tracked content. `base` (a
/// script's own directory, repo-relative) is tried before the repo root.
fn resolve_candidate(tracked: &Tracked, base: &str, raw: &str) -> Option<String> {
    let cut = raw.split(['{', '$', '*', '?']).next().unwrap_or("");
    let mut cand = cut.trim().trim_end_matches('/').trim_end_matches('.');
    while let Some(r) = cand.strip_prefix("./") {
        cand = r;
    }
    let cand = cand.trim_start_matches('/').trim_end_matches('/');
    if cand.is_empty() || cand == "." || cand.contains("..") {
        return None;
    }
    let mut tries = Vec::new();
    if !base.is_empty() {
        tries.push(format!("{base}/{cand}"));
    }
    tries.push(cand.to_string());
    tries.into_iter().find(|t| tracked.names(t))
}

/// The body path a gate's registry entry names, e.g. `bodies::conformance`.
fn body_path_for(registry_text: &str, gate: &str) -> Option<String> {
    let needle = format!("name: \"{gate}\",");
    let start = registry_text.find(&needle)?;
    // The entry ends at the next gate's `name:`; `body:` must be inside it.
    let tail = &registry_text[start + needle.len()..];
    let end = tail.find("\n        name: \"").unwrap_or(tail.len());
    let entry = &tail[..end];
    let at = entry.find("body: ")?;
    let v: String = entry[at + 6..]
        .chars()
        .take_while(|c| c.is_alphanumeric() || *c == '_' || *c == ':')
        .collect();
    (!v.is_empty()).then_some(v)
}

/// The registration fingerprint. Everything declarative about the gate.
pub fn registration(g: &Gate) -> String {
    let mut s = format!(
        "gate={}\ntier={}\nplatforms={}\nbudget_s={}\nexpected={}\nexpect={:?}\n",
        g.name,
        g.tier.label(),
        g.platforms.labels().join(","),
        g.budget_s,
        g.expected,
        g.expect
    );
    for m in g.mutations.as_slice() {
        s.push_str(&format!(
            "mutation={}\ndescription={}\nkind={:?}\n",
            m.id, m.description, m.kind
        ));
    }
    s
}

/// Resolve every input of one gate's proof.
pub fn inputs_for(root: &Path, g: &Gate) -> Result<Inputs, String> {
    let registry_text = std::fs::read_to_string(root.join(REGISTRY_SOURCE))
        .map_err(|e| format!("cannot read {REGISTRY_SOURCE}: {e}"))?;
    let items = body_items(root)?;
    let tracked = Tracked::load(root)?;
    inputs_with(root, &tracked, g, &registry_text, &items)
}

fn inputs_with(
    root: &Path,
    tracked: &Tracked,
    g: &Gate,
    registry_text: &str,
    items: &HashMap<String, String>,
) -> Result<Inputs, String> {
    let mut paths: BTreeSet<String> = RUNNER_SOURCES.iter().map(|s| s.to_string()).collect();
    for m in g.mutations.as_slice() {
        if let MutationKind::ReplaceOnce { path, .. } = m.kind {
            paths.insert(path.to_string());
        }
    }

    // ---- the body closure ------------------------------------------------
    let body = body_path_for(registry_text, g.name)
        .ok_or_else(|| format!("cannot find `body:` for gate `{}` in the registry", g.name))?;
    let body_fn = body.rsplit("::").next().unwrap_or(&body).to_string();
    if !items.contains_key(&body_fn) {
        return Err(format!(
            "gate `{}` names body `{body}`, which is not an item of {}",
            g.name,
            BODY_SOURCES.join(" / ")
        ));
    }
    let mut seen: BTreeSet<String> = BTreeSet::new();
    let mut work = vec![body_fn];
    let mut closure_text = String::new();
    while let Some(name) = work.pop() {
        if !seen.insert(name.clone()) {
            continue;
        }
        let Some(text) = items.get(&name) else {
            continue;
        };
        closure_text.push_str(text);
        let code = code_only(text);
        for id in identifiers(&code) {
            if items.contains_key(id) && !seen.contains(id) {
                work.push(id.to_string());
            }
        }
    }
    // Which body-source files contributed: hash those items by content through
    // the closure text itself (below), not the whole file — so an unrelated
    // gate's body edit does not force this proof.
    let mut registration = registration(g);
    registration.push_str("body-closure=\n");
    registration.push_str(&closure_text);

    // `xtask` modules the closure calls into. Their string literals name
    // fixtures exactly as a body's do (`shared_world_gate.rs` walks
    // `examples/`), so they are scanned below as Rust sources.
    let mut rust_sources: Vec<String> = Vec::new();
    for m in module_prefixes(&code_only(&closure_text)) {
        let file = format!("{XTASK_SRC}/{m}.rs");
        let dir = format!("{XTASK_SRC}/{m}");
        if tracked.names(&file) {
            paths.insert(file.clone());
            rust_sources.push(file);
        } else if m == "harness" {
            // `crate::harness::…` — the harness's own modules. Each is the
            // registry (fingerprinted per gate above), a body source (the
            // closure above) or the runner (always an input), plus
            // `mod.rs`/`state.rs`, which decide rendering, not what bites.
            paths.insert(format!("{XTASK_SRC}/harness/state.rs"));
        } else if tracked.names(&dir) {
            rust_sources.extend(tracked.under(&dir).filter(|f| f.ends_with(".rs")).cloned());
            paths.insert(dir);
        }
    }

    // Fixtures and scripts named by string literals — the closure's own, and
    // those of every xtask module it calls into.
    let mut literals = string_literals(&closure_text);
    for f in &rust_sources {
        if let Ok(text) = std::fs::read_to_string(root.join(f)) {
            literals.extend(string_literals(&strip_rust_comments(&text)));
        }
    }
    // Depth 0: what the gate names itself (literals, mutation targets). A
    // depth-0 script's references are followed ONE level: `conformance.sh`
    // pulls in the `lib/*.sh` it sources and the suites it runs, and those are
    // hashed; their own references (a library's "run ./scripts/build.sh" hint)
    // are not followed further.
    let mut scripts: Vec<(String, u8)> = paths.iter().map(|p| (p.clone(), 0)).collect();
    for lit in literals {
        if let Some(rel) = resolve_candidate(tracked, "", &lit) {
            if rust_literal_allowed(tracked, &rel) && paths.insert(rel.clone()) {
                scripts.push((rel, 0));
            }
        }
    }

    let mut followed: BTreeSet<String> = BTreeSet::new();
    while let Some((rel, depth)) = scripts.pop() {
        if depth > 0 || !followed.insert(rel.clone()) {
            continue;
        }
        let is_script = tracked.files.contains(&rel)
            && Path::new(&rel)
                .extension()
                .and_then(|e| e.to_str())
                .is_some_and(|e| SCRIPT_EXTS.contains(&e));
        if !is_script {
            continue;
        }
        let Ok(text) = std::fs::read_to_string(root.join(&rel)) else {
            continue;
        };
        // Whole-line comments name nothing the script runs.
        let code: String = text
            .lines()
            .filter(|l| {
                let t = l.trim_start();
                !(t.starts_with('#') || t.starts_with("//") || t.starts_with("--"))
            })
            .collect::<Vec<_>>()
            .join("\n");
        let base = rel
            .rsplit_once('/')
            .map(|(d, _)| d)
            .unwrap_or("")
            .to_string();
        for tok in script_path_tokens(&code) {
            // `$ROOT/examples/x` tokenises as `ROOT/examples/x`: try the token,
            // then each suffix after a `/`, and keep the first that resolves.
            let mut cands = vec![tok.as_str()];
            cands.extend(tok.match_indices('/').map(|(i, _)| &tok[i + 1..]));
            let hit = cands
                .into_iter()
                .find_map(|c| resolve_candidate(tracked, &base, c));
            if let Some(r) = hit {
                if !in_toolchain(&r) && paths.insert(r.clone()) {
                    scripts.push((r, depth + 1));
                }
            }
        }
    }

    Ok(Inputs {
        registration,
        paths,
        items: seen,
    })
}

/// The tracked files under the given paths.
fn tracked_files(tracked: &Tracked, paths: &BTreeSet<String>) -> BTreeSet<String> {
    paths
        .iter()
        .flat_map(|p| tracked.under(p).cloned().collect::<Vec<_>>())
        // The proof ledger is the OUTPUT of a falsifier run. A gate that reads it
        // (`coverage-ledger`) would otherwise change digest on every run that
        // records any proof, and could never be carried.
        .filter(|f| f.as_str() != super::PROOF_LEDGER)
        .collect()
}

/// Blob hashes of working-tree content, one `git hash-object` for the batch.
/// A tracked file that is absent from the working tree hashes as `MISSING`.
fn blob_hashes(root: &Path, files: &BTreeSet<String>) -> Result<BTreeMap<String, String>, String> {
    let mut out = BTreeMap::new();
    let present: Vec<&String> = files.iter().filter(|f| root.join(f).is_file()).collect();
    for f in files {
        if !root.join(f).is_file() {
            out.insert(f.clone(), "MISSING".to_string());
        }
    }
    if present.is_empty() {
        return Ok(out);
    }
    let mut child = Command::new("git")
        .arg("-C")
        .arg(root)
        .args(["hash-object", "--no-filters", "--stdin-paths"])
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .map_err(|e| format!("git hash-object: {e}"))?;
    {
        let mut stdin = child.stdin.take().ok_or("git hash-object: no stdin")?;
        let list: String = present.iter().map(|f| format!("{f}\n")).collect();
        stdin
            .write_all(list.as_bytes())
            .map_err(|e| format!("git hash-object stdin: {e}"))?;
    }
    let res = child
        .wait_with_output()
        .map_err(|e| format!("git hash-object: {e}"))?;
    if !res.status.success() {
        return Err(format!(
            "git hash-object failed: {}",
            String::from_utf8_lossy(&res.stderr).trim()
        ));
    }
    let hashes: Vec<String> = String::from_utf8_lossy(&res.stdout)
        .lines()
        .map(str::to_string)
        .collect();
    if hashes.len() != present.len() {
        return Err(format!(
            "git hash-object returned {} hashes for {} files",
            hashes.len(),
            present.len()
        ));
    }
    for (f, h) in present.into_iter().zip(hashes) {
        out.insert(f.clone(), h);
    }
    Ok(out)
}

/// Hash arbitrary text with the same object hash git uses.
fn hash_text(root: &Path, text: &str) -> Result<String, String> {
    let mut child = Command::new("git")
        .arg("-C")
        .arg(root)
        .args(["hash-object", "--no-filters", "--stdin"])
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .map_err(|e| format!("git hash-object: {e}"))?;
    {
        let mut stdin = child.stdin.take().ok_or("git hash-object: no stdin")?;
        stdin
            .write_all(text.as_bytes())
            .map_err(|e| format!("git hash-object stdin: {e}"))?;
    }
    let res = child
        .wait_with_output()
        .map_err(|e| format!("git hash-object: {e}"))?;
    if !res.status.success() {
        return Err("git hash-object --stdin failed".into());
    }
    Ok(String::from_utf8_lossy(&res.stdout).trim().to_string())
}

/// One gate's digest, with the number of files it covers.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Digest {
    pub hash: String,
    pub files: usize,
}

/// Digest every gate in `gates`. A gate whose inputs cannot be resolved maps to
/// `Err` — the caller MUST re-prove it.
pub fn fingerprint(root: &Path, gates: &[&Gate]) -> BTreeMap<String, Result<Digest, String>> {
    let mut out = BTreeMap::new();
    let registry_text = std::fs::read_to_string(root.join(REGISTRY_SOURCE))
        .map_err(|e| format!("cannot read {REGISTRY_SOURCE}: {e}"));
    let items = body_items(root);
    let tracked = Tracked::load(root);
    let (registry_text, items, tracked) = match (registry_text, items, tracked) {
        (Ok(r), Ok(i), Ok(t)) => (r, i, t),
        (Err(e), _, _) | (_, Err(e), _) | (_, _, Err(e)) => {
            for g in gates {
                out.insert(g.name.to_string(), Err(e.clone()));
            }
            return out;
        }
    };
    // Resolve per gate, then hash the union of files ONCE.
    let mut resolved: Vec<(&Gate, Result<(Inputs, BTreeSet<String>), String>)> = Vec::new();
    let mut union: BTreeSet<String> = BTreeSet::new();
    for g in gates {
        let r = inputs_with(root, &tracked, g, &registry_text, &items).map(|inp| {
            let files = tracked_files(&tracked, &inp.paths);
            (inp, files)
        });
        if let Ok((_, files)) = &r {
            union.extend(files.iter().cloned());
        }
        resolved.push((g, r));
    }
    let blobs = blob_hashes(root, &union);
    for (g, r) in resolved {
        let d = match (r, &blobs) {
            (Err(e), _) => Err(e),
            (_, Err(e)) => Err(e.clone()),
            (Ok((inp, files)), Ok(blobs)) => {
                let mut manifest = inp.registration.clone();
                manifest.push_str("paths=\n");
                for p in &inp.paths {
                    manifest.push_str(p);
                    manifest.push('\n');
                }
                manifest.push_str("files=\n");
                for f in &files {
                    let h = blobs.get(f).map(String::as_str).unwrap_or("UNHASHED");
                    manifest.push_str(&format!("{h} {f}\n"));
                }
                hash_text(root, &manifest).map(|hash| Digest {
                    hash,
                    files: files.len(),
                })
            }
        };
        out.insert(g.name.to_string(), d);
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::harness::registry::{
        Expect, GateCtx, GateOutcome, Mutation, Mutations, Tier, ALL_PLATFORMS,
    };

    fn body(_: &GateCtx) -> GateOutcome {
        GateOutcome::new(true, 1, "")
    }

    static GATE: Gate = Gate {
        name: "t-gate",
        tier: Tier::T1,
        platforms: ALL_PLATFORMS,
        budget_s: 10,
        expected: 1,
        expect: Expect::Falsifiable,
        summary: "",
        mutations: Mutations::new(&[Mutation {
            id: "t-gate.m",
            description: "d",
            kind: MutationKind::ReplaceOnce {
                path: "fixtures/target.txt",
                from: "a",
                to: "b",
            },
        }]),
        body,
    };

    fn git(root: &Path, args: &[&str]) {
        let ok = Command::new("git")
            .arg("-C")
            .arg(root)
            .args(args)
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .status()
            .unwrap()
            .success();
        assert!(ok, "git {args:?}");
    }

    fn write(root: &Path, rel: &str, body: &str) {
        let p = root.join(rel);
        std::fs::create_dir_all(p.parent().unwrap()).unwrap();
        std::fs::write(p, body).unwrap();
    }

    /// A miniature repo with one gate whose body names a script, which names a
    /// fixture directory; plus files the gate does NOT depend on.
    fn repo(tag: &str) -> std::path::PathBuf {
        let root =
            std::env::temp_dir().join(format!("sky-proof-inputs-{tag}-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&root);
        std::fs::create_dir_all(&root).unwrap();
        git(&root, &["init", "-q"]);
        git(&root, &["config", "user.email", "t@example.invalid"]);
        git(&root, &["config", "user.name", "t"]);
        write(
            &root,
            REGISTRY_SOURCE,
            "    Gate {\n        name: \"t-gate\",\n        body: bodies::t_gate,\n    },\n    Gate {\n        name: \"other\",\n        body: bodies::other,\n    },\n",
        );
        write(
            &root,
            BODY_SOURCES[0],
            "pub fn t_gate(ctx: &GateCtx) -> GateOutcome {\n    helper(\"scripts/run.sh\")\n}\n\nfn helper(s: &str) -> GateOutcome {\n    todo!()\n}\n\npub fn other(ctx: &GateCtx) -> GateOutcome {\n    todo!(\"apps/unrelated\")\n}\n",
        );
        write(&root, BODY_SOURCES[1], "pub fn free_port() {}\n");
        write(&root, BODY_SOURCES[2], "fn main() {}\n");
        for r in RUNNER_SOURCES {
            write(&root, r, "// runner\n");
        }
        write(
            &root,
            "scripts/run.sh",
            "#!/bin/bash\nsky build \"$ROOT/fixtures/app\"\n",
        );
        write(&root, "fixtures/app/src/Main.sky", "main = 1\n");
        write(&root, "fixtures/target.txt", "a\n");
        write(&root, "apps/unrelated/x.sky", "x = 1\n");
        git(&root, &["add", "-A"]);
        git(&root, &["commit", "-q", "-m", "init"]);
        root
    }

    fn digest(root: &Path) -> Digest {
        fingerprint(root, &[&GATE])
            .remove("t-gate")
            .unwrap()
            .expect("the inputs resolve")
    }

    #[test]
    fn the_resolved_inputs_follow_the_body_the_script_and_the_mutation_target() {
        let root = repo("resolve");
        let inp = inputs_for(&root, &GATE).unwrap();
        for want in ["scripts/run.sh", "fixtures/app", "fixtures/target.txt"] {
            assert!(
                inp.paths.contains(want),
                "{want} missing from {:?}",
                inp.paths
            );
        }
        assert!(
            !inp.paths.iter().any(|p| p.starts_with("apps/unrelated")),
            "another gate's fixture leaked into this gate's inputs: {:?}",
            inp.paths
        );
        assert!(
            inp.registration.contains("fn helper"),
            "helper not in closure"
        );
        assert!(
            !inp.registration.contains("pub fn other"),
            "another gate's body is in the closure"
        );
    }

    #[test]
    fn an_unchanged_tree_gives_the_same_digest() {
        let root = repo("same");
        assert_eq!(digest(&root), digest(&root));
    }

    #[test]
    fn a_changed_fixture_changes_the_digest() {
        let root = repo("fixture");
        let before = digest(&root);
        write(&root, "fixtures/app/src/Main.sky", "main = 2\n");
        assert_ne!(before.hash, digest(&root).hash);
    }

    #[test]
    fn a_changed_script_mutation_target_or_body_changes_the_digest() {
        for (rel, body) in [
            (
                "scripts/run.sh",
                "#!/bin/bash\nsky build \"$ROOT/fixtures/app\" -v\n",
            ),
            ("fixtures/target.txt", "a\nmore\n"),
            (RUNNER_SOURCES[0], "// runner changed\n"),
        ] {
            let root = repo("changed");
            let before = digest(&root);
            write(&root, rel, body);
            assert_ne!(
                before.hash,
                digest(&root).hash,
                "editing {rel} was not seen"
            );
        }
        let root = repo("helper");
        let before = digest(&root);
        let p = root.join(BODY_SOURCES[0]);
        let t = std::fs::read_to_string(&p).unwrap().replace(
            "todo!()\n}\n\npub fn other",
            "todo!(\"x\")\n}\n\npub fn other",
        );
        std::fs::write(&p, t).unwrap();
        assert_ne!(
            before.hash,
            digest(&root).hash,
            "a helper edit was not seen"
        );
    }

    #[test]
    fn an_unrelated_change_keeps_the_digest() {
        let root = repo("unrelated");
        let before = digest(&root);
        write(&root, "apps/unrelated/x.sky", "x = 2\n");
        // Another gate's body changes too.
        let p = root.join(BODY_SOURCES[0]);
        let t = std::fs::read_to_string(&p)
            .unwrap()
            .replace("todo!(\"apps/unrelated\")", "todo!(\"apps/unrelated\") ");
        std::fs::write(&p, t).unwrap();
        assert_eq!(before, digest(&root));
    }

    #[test]
    fn an_unresolvable_body_is_an_error_not_a_digest() {
        let root = repo("nobody");
        write(
            &root,
            REGISTRY_SOURCE,
            "    Gate {\n        name: \"x\",\n    },\n",
        );
        assert!(fingerprint(&root, &[&GATE])["t-gate"].is_err());
    }

    #[test]
    fn items_split_at_top_level_only() {
        let items = split_items("pub fn a() {\n    fn inner() {}\n}\nconst B: u8 = 1;\n");
        let names: Vec<&str> = items.iter().map(|(n, _)| n.as_str()).collect();
        assert_eq!(names, ["a", "B"]);
    }
}

//! `xtask config-surface` — the measurement the config-architecture design
//! calls for FIRST, and the ratchet that keeps the answer honest.
//!
//! # What this exists to answer
//!
//! `docs/tooling/config-architecture.md` §12 risk 1:
//!
//! > **The residual surface may be larger than §4.3.** The honest measurement
//! > has not been done: enumerate every setting a CLI verb reads with no binary
//! > available. If that set is large, `sky.toml` keeps a runtime surface and
//! > §10's schema returns for it. **Do this first.**
//!
//! That is the entry condition for the whole redesign, and the repository rule
//! is that no ledger or verdict may quote a number a script did not produce
//! (AGENTS.md, "Coverage ledgers — three files, all generated"). So the answer
//! is computed here, from the sources, and written to
//! `docs/coverage/config-surface.json`.
//!
//! # The three defect classes it measures
//!
//! Each is a class the design documents as live, and each is derived from the
//! tree rather than transcribed — a transcribed list rots the first time a name
//! is changed, which is the exact failure under test.
//!
//! **1. Pre-binary reads (the residual surface).** A `sky.toml` key read by a
//! CLI verb *before any app binary exists* cannot move into the app's own code,
//! because there is no app to ask. `sky db start` sizes `postgresql.conf` from
//! `[database] maxOpenConns` in a tree that has never been built. The design's
//! §4.4 names one such casualty and proposes `./sky-out/app --sky-config` for
//! it; the measurement is how many more there are.
//!
//! **2. Seeded-but-unread env suffixes (the write-only class).** The compiler
//! emits `rt.SetSkyDefault(<SUFFIX>, <value>)` into every program's prologue
//! (`lower.rs` `prologue_init`). A suffix nothing in `runtime-go/` reads is a
//! setting that is parsed, validated, emitted — and ignored. `[auth]` was
//! exactly that for four minor versions (design §1.11), and two examples
//! shipped a `session_ttl` key advertising a 24-hour session while getting the
//! default (design §1.4).
//!
//! **3. Documentation reconciliation.** A documented name nothing reads is a
//! setting a user will set and watch do nothing; a read name in no live doc is
//! a knob no user can discover. Design §1.12 measured 25 and 46 by hand and
//! records that an *earlier* hand audit got 31 and 48 and made a false claim
//! about the DB pool knobs. Hand-counting this surface has already failed
//! twice, which is the argument for computing it.
//!
//! # What it is NOT
//!
//! It is not a schema, and it does not decide where a setting *should* live.
//! It reports where each setting is read from today, and it refuses to let any
//! of the three counts get worse without that being a deliberate, recorded act.

use std::collections::{BTreeMap, BTreeSet};
use std::path::{Path, PathBuf};

use serde_json::{json, Value};

// ─────────────────────────────────────────────────────────────────────────────
// Declared taxonomies — hand-written, and each pinned by a test below.
// ─────────────────────────────────────────────────────────────────────────────

/// `sky.toml` surfaces read pre-binary through a reader that takes NO literal
/// key argument, so the mechanical scan cannot see them.
///
/// These are structural reads — a section header's presence, a whole table's
/// entries — not scalar keys. Each needs a citation, because the default answer
/// to "why is this hand-written" is "then the scan is incomplete".
/// `(sky.toml surface, file that reads it, symbol that reads it, why it is
/// pre-binary)`. The file+symbol pair is VERIFIED at compute time — a citation
/// that stops being true fails the gate rather than quietly outliving its
/// reason.
const STRUCTURAL_PRE_BINARY: &[(&str, &str, &str, &str)] = &[
    (
        "entry",
        "rust/crates/sky/src/main.rs",
        "fn parse_toml_entry",
        "its own parser, no key argument. Decides WHAT is compiled, so it is \
         read before any compile. `sky doctor` reads it through a SECOND, \
         looser parser (`toml_entry`) that does not stop at the first section.",
    ),
    (
        "dependencies",
        "rust/crates/project/src/ffi_ops.rs",
        "fn read_dependencies",
        "reads the whole table. `sky add`/`remove`/`install`/`update` and the \
         LSP's unfetched-dep hints all run with no binary in existence.",
    ),
    (
        "go.dependencies",
        "rust/crates/project/src/ffi_ops.rs",
        "fn read_dependencies",
        "same reader, same verbs; materialises the go.mod pins a build needs \
         before it can start.",
    ),
    (
        "lib",
        "rust/crates/project/src/ffi_ops.rs",
        "fn is_sky_package_root",
        "tests for the `[lib]` HEADER only; no key is read. `sky add`'s \
         auto-probe uses it to tell a Sky package from a Go module.",
    ),
    (
        "database.driver",
        "rust/crates/sky/src/main.rs",
        "fn db_driver_label",
        "a THIRD hand-rolled scalar parser, whose doc comment claims to mirror \
         `read_sky_toml_config` and does not (design §1.3: on `driver = \
         \"postgres\"  # prod` it returns `postgres\"  # prod`). Read before \
         the build, for the `sky db reset`/`drop` confirmation prompt.",
    ),
    (
        "live",
        "rust/crates/sky/src/main.rs",
        "fn check_auth_secret",
        "`sky doctor` tests for the `[live]` / `[auth]` section HEADERS inline, \
         with no key read at all, to decide whether to check for an auth \
         secret. Neither section is a key the scan can see.",
    ),
];

/// Env suffixes the compiler seeds that nothing in `runtime-go/` reads.
///
/// This list is the design's §1.11 finding made mechanical. It is deliberately
/// an ACCOUNTING list, not an exemption: every entry is a defect awaiting the
/// migration table's `[[removed]]` stanza, and the ratchet forbids the count
/// growing. An entry is removed when the setting is either wired to a reader or
/// deleted from the emission path.
/// Functions that take a `<PREFIX>_`-namespaced env SUFFIX as a string literal
/// and read it. `skyGetenv` and friends are the primitives; the rest are thin
/// wrappers, and a wrapper's literal argument is just as much a read as a
/// direct one.
///
/// This list is not open-ended guesswork: [`NON_LITERAL_PREFIXED_READS`] below
/// pins every site where a primitive is called with something other than a
/// literal, so a NEW wrapper cannot appear without failing the gate until it is
/// named here.
const PREFIXED_READ_HELPERS: &[&str] = &[
    "skyGetenv(\"",
    "skyLookupEnv(\"",
    "skyEnvName(\"",
    "dbEnvInt(\"",
    "dbEnvDuration(\"",
    "skyEnvSynchronousCommitOff(\"",
    // The Sky.Live precedence resolver (stage 3). Every `live.*` setting is
    // read through it — `configLayers("LIVE_TTL", …)` IS a read of
    // `SKY_LIVE_TTL`, and the bare `skyGetenv("LIVE_TTL")` calls it replaced
    // are gone. This entry is what the gate demanded when it went red with
    // "the runtime-read derivation lost LIVE_TTL": a new wrapper is invisible
    // to the derivation until it is declared, which is precisely the
    // behaviour stage 1 built this list for.
    "configLayers(\"",
];

/// Every site where a prefixed-read primitive is called with a non-literal
/// suffix. Each is either the primitive's own definition or a wrapper whose
/// literal-taking form is in [`PREFIXED_READ_HELPERS`].
///
/// The gate fails on any site not listed, because an unlisted one means a
/// suffix is being read that the derivation cannot see — and an invisible read
/// makes the seeded-without-reader count report a defect that is not there, or
/// miss one that is.
const NON_LITERAL_PREFIXED_READS: &[(&str, &str)] = &[
    (
        "runtime-go/rt/env_prefix.go",
        "the primitives themselves — skyEnvName/skyLookupEnv/skyGetenv/SetSkyDefault",
    ),
    (
        "runtime-go/rt/db_pool.go",
        "dbEnvInt / dbEnvDuration wrappers, and the dbPoolEnvSuffixes warn loop \
         whose members are all read through a literal dbEnvInt/dbEnvDuration call",
    ),
    (
        "runtime-go/rt/analytics_writer.go",
        "skyEnvSynchronousCommitOff wrapper",
    ),
    (
        "runtime-go/rt/live_config_precedence.go",
        "the configLayers wrapper — it takes the suffix as a parameter and \
         resolves it with skyEnvName(suffix), so the read is non-literal HERE \
         while every caller passes a literal (resolveTTL/resolveIdleEvict/\
         resolveStoreKind/resolveStorePath, and resolveLivePort, which calls \
         configLayers directly)",
    ),
    (
        "runtime-go/rt/sky_config.go",
        "ApplyConfig resolves each Sky.Config key's env suffix with \
         skyEnvName(suffix), so the write is non-literal HERE while every suffix \
         is a literal VALUE in the configKeyToEnvSuffix table (LOG_FORMAT / \
         LOG_LEVEL / DB_PATH / LIVE_STORE(_PATH) / JOBS_STORE(_PATH) / CSRF — \
         all already-seeded suffixes). It is a seed-aware WRITE of the withX \
         layer, not a new read of a hidden suffix. The literal builders \
         (DATABASE_URL / OTEL_EXPORTER_OTLP_ENDPOINT, in configKeyToLiteralEnv) \
         are written verbatim, not via skyEnvName, so they are not prefixed \
         reads at all",
    ),
];

/// Empty by design. This list once held the whole `[auth]` block
/// (`AUTH_COOKIE` / `AUTH_TOKEN_TTL` / `AUTH_DRIVER`) — parsed, seeded into every
/// prologue, and read by nothing (design §1.11). That block is now DELETED: the
/// `build.rs` parse arms and the `lower.rs` `SetSkyDefault` seeds are gone, so
/// none of the three is emitted any longer and `seeded_without_reader` falls to
/// 0. An entry belongs here only while a suffix is BOTH seeded and unread; a
/// newly-seeded, unread suffix must be wired to a reader or deleted, and lands
/// here (with a citation) only as a deliberately-recorded, ratcheted defect.
const SEEDED_WITHOUT_READER: &[(&str, &str)] = &[];

// ─────────────────────────────────────────────────────────────────────────────
// Derivation
// ─────────────────────────────────────────────────────────────────────────────

/// One argument of a call, as written.
#[derive(Debug, Clone, PartialEq, Eq)]
enum Arg {
    /// A string literal — the key is right there.
    Lit(String),
    /// Anything else, as written: an identifier, an expression, a path.
    Expr(String),
}

/// The top-level arguments of the call whose `(` sits at `open_paren`, split on
/// depth-1 commas and kept POSITIONAL — so argument 2 of
/// `sky_toml_section_key(dir, section, key)` is the key whether or not
/// argument 1 happened to be a literal.
///
/// Returns `None` when the parens do not balance within the slice: a truncated
/// read is reported as unresolved rather than guessed at.
fn call_args(src: &str, open_paren: usize) -> Option<Vec<Arg>> {
    let bytes = src.as_bytes();
    if bytes.get(open_paren) != Some(&b'(') {
        return None;
    }
    let mut depth = 0usize;
    let mut i = open_paren;
    let mut out: Vec<Arg> = Vec::new();
    let mut cur = String::new();
    let mut cur_lit: Option<String> = None;

    let flush = |cur: &mut String, cur_lit: &mut Option<String>, out: &mut Vec<Arg>| {
        let text = cur.trim().to_string();
        if text.is_empty() && cur_lit.is_none() {
            return;
        }
        match cur_lit.take() {
            // A lone string literal, with nothing else in the argument.
            Some(lit) if text.is_empty() => out.push(Arg::Lit(lit)),
            _ => out.push(Arg::Expr(text)),
        }
        cur.clear();
    };

    while i < bytes.len() {
        match bytes[i] {
            b'(' | b'[' | b'{' => {
                depth += 1;
                if depth > 1 {
                    cur.push(bytes[i] as char);
                }
            }
            b')' | b']' | b'}' => {
                depth -= 1;
                if depth == 0 {
                    flush(&mut cur, &mut cur_lit, &mut out);
                    return Some(out);
                }
                cur.push(bytes[i] as char);
            }
            b',' if depth == 1 => flush(&mut cur, &mut cur_lit, &mut out),
            b'"' => {
                let mut j = i + 1;
                let mut lit = String::new();
                while j < bytes.len() {
                    match bytes[j] {
                        b'\\' => {
                            j += 2;
                            lit.push('?'); // never a config key; marks "escaped"
                            continue;
                        }
                        b'"' => break,
                        c => lit.push(c as char),
                    }
                    j += 1;
                }
                if depth == 1 && cur.trim().is_empty() && cur_lit.is_none() {
                    cur_lit = Some(lit);
                } else {
                    cur.push_str("<str>");
                }
                i = j;
            }
            c => cur.push(c as char),
        }
        i += 1;
    }
    None
}

/// `const NAME: &str = "value";` declarations in a file, so a key named by a
/// constant is still a key the scan can see. `db_provision.rs` names the
/// PostgreSQL pin key that way (`PIN_KEY`), and it is a genuine pre-binary read.
fn str_consts(src: &str) -> BTreeMap<String, String> {
    let mut out = BTreeMap::new();
    for (idx, _) in src.match_indices("const ") {
        let rest = &src[idx + "const ".len()..];
        let Some(colon) = rest.find(':') else { continue };
        let name = rest[..colon].trim();
        if name.is_empty() || !name.chars().all(|c| c.is_ascii_uppercase() || c == '_') {
            continue;
        }
        let Some(eq) = rest.find('=') else { continue };
        if eq < colon {
            continue;
        }
        let tail = rest[eq + 1..].trim_start();
        let Some(body) = tail.strip_prefix('"') else {
            continue;
        };
        let Some(end) = body.find('"') else { continue };
        out.insert(name.to_string(), body[..end].to_string());
    }
    out
}

/// Resolve an argument to the string it names, if that is knowable statically:
/// a literal, or an identifier bound to a `const … &str`, possibly written as a
/// path (`db_provision::PIN_KEY`).
fn resolve_arg(arg: &Arg, consts: &BTreeMap<String, String>) -> Option<String> {
    match arg {
        Arg::Lit(s) => Some(s.clone()),
        Arg::Expr(e) => {
            let last = e.rsplit("::").next().unwrap_or(e).trim();
            consts.get(last).cloned()
        }
    }
}

/// Byte ranges covered by `#[cfg(test)]` items.
///
/// A test's read of `sky.toml` is not a deployment surface — `build.rs`'s own
/// unit tests call `sky_toml_section_key(&dir, "nosuch", "url")`, and counting
/// that as residual surface would inflate the very number this gate exists to
/// report honestly.
fn cfg_test_ranges(src: &str) -> Vec<(usize, usize)> {
    let mut out = Vec::new();
    for (idx, _) in src.match_indices("#[cfg(test)]") {
        let Some(rel_open) = src[idx..].find('{') else {
            continue;
        };
        let open = idx + rel_open;
        let bytes = src.as_bytes();
        let mut depth = 0usize;
        let mut i = open;
        while i < bytes.len() {
            match bytes[i] {
                b'{' => depth += 1,
                b'}' => {
                    depth -= 1;
                    if depth == 0 {
                        out.push((idx, i));
                        break;
                    }
                }
                _ => {}
            }
            i += 1;
        }
    }
    out
}

fn in_any(ranges: &[(usize, usize)], idx: usize) -> bool {
    ranges.iter().any(|(a, b)| idx >= *a && idx <= *b)
}

/// The name of the function whose body contains `idx`, best-effort: the nearest
/// preceding top-level `fn` declaration.
fn enclosing_fn(src: &str, idx: usize) -> Option<&str> {
    let head = &src[..idx];
    let at = head.rfind("\nfn ").map(|p| p + 4).into_iter().chain(
        head.rfind("\npub fn ").map(|p| p + 8),
    ).chain(
        head.rfind("\n    fn ").map(|p| p + 8),
    ).chain(
        head.rfind("\n    pub fn ").map(|p| p + 12),
    ).max()?;
    let rest = &src[at..];
    let end = rest.find(|c: char| !(c.is_alphanumeric() || c == '_'))?;
    Some(&rest[..end])
}

/// A pre-binary `sky.toml` read discovered mechanically.
#[derive(Debug, Clone, PartialEq, Eq, PartialOrd, Ord)]
struct PreBinaryRead {
    /// `section.key`, or a bare key for the project-scoped reader.
    key: String,
    site: String,
    reader: String,
}

/// The reader functions whose literal arguments name a `sky.toml` key, and
/// where in the argument list the section and key sit.
///
/// `sky_toml_project_key(dir, key, default)` — the key is argument 0 of the
/// string literals and the scope is bare/`[project]`/`[source]`.
/// `sky_toml_section_key(dir, section, key)` / `sky_toml_flag(..)` /
/// `toml_value(text, section, key)` — literals 0 and 1 are section and key.
const KEY_READERS: &[(&str, bool)] = &[
    ("sky_toml_project_key", false),
    ("sky_toml_section_key", true),
    ("sky_toml_flag", true),
    ("toml_value", true),
];

/// Crates whose code runs with no app binary in existence.
///
/// `project` is included because its `sky_toml_*` helpers are called from the
/// CLI; `lower`/`codegen` are not, because by the time they run a compile is
/// already under way.
const PRE_BINARY_CRATES: &[&str] = &["rust/crates/sky/src", "rust/crates/project/src"];

fn scan_pre_binary_reads(root: &Path) -> Result<(Vec<PreBinaryRead>, Vec<String>), String> {
    let mut found = Vec::new();
    let mut unresolved = Vec::new();

    for crate_dir in PRE_BINARY_CRATES {
        let dir = root.join(crate_dir);
        for path in rust_sources(&dir)? {
            let rel = path
                .strip_prefix(root)
                .unwrap_or(&path)
                .to_string_lossy()
                .replace('\\', "/");
            let src = std::fs::read_to_string(&path)
                .map_err(|e| format!("cannot read {}: {e}", path.display()))?;
            let consts = str_consts(&src);
            let test_ranges = cfg_test_ranges(&src);

            for (reader, sectioned) in KEY_READERS {
                let needle = format!("{reader}(");
                for (idx, _) in src.match_indices(&needle) {
                    if in_any(&test_ranges, idx) {
                        continue;
                    }
                    // Skip the definition itself.
                    let before = &src[..idx];
                    if before.ends_with("fn ") || before.ends_with("pub fn ") {
                        continue;
                    }
                    // Skip a doc-comment or ordinary-comment mention.
                    let line_start = before.rfind('\n').map(|n| n + 1).unwrap_or(0);
                    let line_head = src[line_start..idx].trim_start();
                    if line_head.starts_with("//") || line_head.starts_with('*') {
                        continue;
                    }
                    // A reader forwarding to another reader inside its own body
                    // is plumbing, not a read: `sky_toml_flag` passes its own
                    // parameters straight through to `sky_toml_section_key`, and
                    // the key it eventually reads is named at ITS caller.
                    if let Some(owner) = enclosing_fn(&src, idx) {
                        if KEY_READERS.iter().any(|(r, _)| *r == owner) {
                            continue;
                        }
                    }
                    let lineno = before.bytes().filter(|b| *b == b'\n').count() + 1;
                    let site = format!("{rel}:{lineno}");

                    let Some(args) = call_args(&src, idx + needle.len() - 1) else {
                        unresolved.push(format!("{site}: {reader}(…) — parens do not balance"));
                        continue;
                    };
                    // Positional: (project_dir, section, key) for the sectioned
                    // readers, (project_dir, key, default) for the project one.
                    let key = if *sectioned {
                        match (
                            args.get(1).and_then(|a| resolve_arg(a, &consts)),
                            args.get(2).and_then(|a| resolve_arg(a, &consts)),
                        ) {
                            (Some(s), Some(k)) => format!("{s}.{k}"),
                            _ => {
                                unresolved.push(format!(
                                    "{site}: {reader}(…) names its section/key with something \
                                     the scan cannot resolve to a string — the residual-surface \
                                     count cannot see which key this reads"
                                ));
                                continue;
                            }
                        }
                    } else {
                        match args.get(1).and_then(|a| resolve_arg(a, &consts)) {
                            Some(k) => k,
                            None => {
                                unresolved.push(format!(
                                    "{site}: {reader}(…) names its key with something the scan \
                                     cannot resolve to a string"
                                ));
                                continue;
                            }
                        }
                    };
                    found.push(PreBinaryRead {
                        key,
                        site,
                        reader: (*reader).to_string(),
                    });
                }
            }
        }
    }

    found.sort();
    found.dedup();
    unresolved.sort();
    Ok((found, unresolved))
}

fn rust_sources(dir: &Path) -> Result<Vec<PathBuf>, String> {
    let mut out = Vec::new();
    let mut stack = vec![dir.to_path_buf()];
    while let Some(d) = stack.pop() {
        let entries =
            std::fs::read_dir(&d).map_err(|e| format!("cannot read {}: {e}", d.display()))?;
        for entry in entries {
            let path = entry.map_err(|e| e.to_string())?.path();
            if path.is_dir() {
                stack.push(path);
            } else if path.extension().and_then(|e| e.to_str()) == Some("rs") {
                out.push(path);
            }
        }
    }
    out.sort();
    Ok(out)
}

/// The `sky.toml` keys `read_sky_toml_config` accepts, derived from
/// `accepted_config_keys` in `build.rs` rather than listed here.
fn accepted_keys(root: &Path) -> Result<BTreeSet<String>, String> {
    let path = root.join("rust/crates/project/src/build.rs");
    let src = std::fs::read_to_string(&path)
        .map_err(|e| format!("cannot read {}: {e}", path.display()))?;
    let start = src
        .find("fn accepted_config_keys")
        .ok_or_else(|| "build.rs no longer defines `accepted_config_keys` — the derivation this gate rests on has broken".to_string())?;
    let body = &src[start..];
    let end = body
        .find("\n}\n")
        .ok_or_else(|| "cannot find the end of `accepted_config_keys`".to_string())?;
    let body = &body[..end];

    let mut out = BTreeSet::new();
    // Arms look like:  "live" => &["port", "static", …],
    for (idx, _) in body.match_indices("=> &[") {
        let head = &body[..idx];
        let Some(qend) = head.rfind('"') else { continue };
        let Some(qstart) = head[..qend].rfind('"') else {
            continue;
        };
        let section = &head[qstart + 1..qend];
        let tail = &body[idx + "=> &[".len()..];
        let Some(close) = tail.find(']') else { continue };
        for lit in tail[..close].split('"').skip(1).step_by(2) {
            out.insert(format!("{section}.{lit}"));
        }
    }
    if out.len() < 20 {
        return Err(format!(
            "derived only {} accepted keys from `accepted_config_keys` — the \
             derivation has broken, and a broken derivation makes every count \
             below meaningless. Found: {out:?}",
            out.len()
        ));
    }
    Ok(out)
}

/// Env suffixes the compiler seeds into every program's prologue.
///
/// Two sources, both emission sites: `build.rs`'s `extra_defaults` (the
/// `sky.toml`-derived ones) and `lower.rs`'s hardcoded `SetSkyDefault` calls.
fn seeded_suffixes(root: &Path) -> Result<BTreeSet<String>, String> {
    let mut out = BTreeSet::new();

    let build_rs = root.join("rust/crates/project/src/build.rs");
    let src = std::fs::read_to_string(&build_rs)
        .map_err(|e| format!("cannot read {}: {e}", build_rs.display()))?;
    for (idx, _) in src.match_indices("extra_defaults.push((\"") {
        let rest = &src[idx + "extra_defaults.push((\"".len()..];
        if let Some(end) = rest.find('"') {
            if is_env_token(&rest[..end]) {
                out.insert(rest[..end].to_string());
            }
        }
    }

    let lower_rs = root.join("rust/crates/lower/src/lower.rs");
    let src = std::fs::read_to_string(&lower_rs)
        .map_err(|e| format!("cannot read {}: {e}", lower_rs.display()))?;
    for (idx, _) in src.match_indices("\"rt.SetSkyDefault\", &[\"") {
        let rest = &src[idx + "\"rt.SetSkyDefault\", &[\"".len()..];
        if let Some(end) = rest.find('"') {
            if is_env_token(&rest[..end]) {
                out.insert(rest[..end].to_string());
            }
        }
    }

    if out.len() < 15 {
        return Err(format!(
            "derived only {} seeded suffixes — the derivation has broken. Found: {out:?}",
            out.len()
        ));
    }
    Ok(out)
}

/// Env suffixes and literal names the Go runtime actually reads.
///
/// Returns `(prefixed_suffixes, literal_sky_names, unresolved_sites)`. Test
/// files are excluded: a name read only by a test is not a name a deployment
/// can set.
type RuntimeReads = (BTreeSet<String>, BTreeSet<String>, Vec<String>);

fn runtime_reads(root: &Path) -> Result<RuntimeReads, String> {
    let mut suffixes = BTreeSet::new();
    let mut literals = BTreeSet::new();
    let mut unresolved = Vec::new();

    let dir = root.join("runtime-go");
    let mut stack = vec![dir.clone()];
    let mut files = Vec::new();
    while let Some(d) = stack.pop() {
        let entries =
            std::fs::read_dir(&d).map_err(|e| format!("cannot read {}: {e}", d.display()))?;
        for entry in entries {
            let path = entry.map_err(|e| e.to_string())?.path();
            if path.is_dir() {
                stack.push(path);
                continue;
            }
            let name = path.file_name().and_then(|n| n.to_str()).unwrap_or("");
            if name.ends_with(".go") && !name.ends_with("_test.go") {
                files.push(path);
            }
        }
    }
    files.sort();

    for path in files {
        let src = std::fs::read_to_string(&path)
            .map_err(|e| format!("cannot read {}: {e}", path.display()))?;
        for helper in PREFIXED_READ_HELPERS {
            for (idx, _) in src.match_indices(helper) {
                let rest = &src[idx + helper.len()..];
                if let Some(end) = rest.find('"') {
                    if is_env_token(&rest[..end]) {
                        suffixes.insert(rest[..end].to_string());
                    }
                }
            }
        }

        // A primitive called with something other than a literal reads a suffix
        // the scan above cannot see. Every such site must be declared.
        let rel = path
            .strip_prefix(root)
            .unwrap_or(&path)
            .to_string_lossy()
            .replace('\\', "/");
        for prim in ["skyGetenv(", "skyLookupEnv(", "skyEnvName("] {
            for (idx, _) in src.match_indices(prim) {
                let after = &src[idx + prim.len()..];
                if after.starts_with('"') {
                    continue;
                }
                // A mention inside a comment is prose, not a read.
                let line_start = src[..idx].rfind('\n').map(|n| n + 1).unwrap_or(0);
                let head = src[line_start..idx].trim_start();
                if head.starts_with("//") || head.starts_with('*') {
                    continue;
                }
                if NON_LITERAL_PREFIXED_READS.iter().any(|(f, _)| *f == rel) {
                    continue;
                }
                let lineno = src[..idx].bytes().filter(|b| *b == b'\n').count() + 1;
                unresolved.push(format!(
                    "{rel}:{lineno}: `{prim}…)` is called with a non-literal suffix \
                     from a file not listed in NON_LITERAL_PREFIXED_READS"
                ));
            }
        }

        for helper in ["os.Getenv(\"", "os.LookupEnv(\""] {
            for (idx, _) in src.match_indices(helper) {
                let rest = &src[idx + helper.len()..];
                if let Some(end) = rest.find('"') {
                    let name = &rest[..end];
                    if name.starts_with("SKY_") && is_env_token(name) {
                        literals.insert(name.to_string());
                    }
                }
            }
        }
    }

    if suffixes.len() < 20 {
        return Err(format!(
            "derived only {} runtime-read suffixes — the derivation has broken, \
             and a broken derivation would make the seeded-without-reader count \
             pass vacuously. Found: {suffixes:?}",
            suffixes.len()
        ));
    }
    unresolved.sort();
    Ok((suffixes, literals, unresolved))
}

/// `SKY_*` names appearing anywhere in the tracked tree OUTSIDE `docs/`.
///
/// This is the denominator for "is this documented name read by anything at
/// all" — deliberately wider than the Go runtime, because a name read only by
/// a script or by a Rust CLI verb is still a real name a user can set. Design
/// §1.12 classifies exactly those categories inside its count of 25.
fn names_used_outside_docs(root: &Path) -> Result<BTreeSet<String>, String> {
    let out = std::process::Command::new("git")
        .arg("-C")
        .arg(root)
        .args(["grep", "-h", "-oI", "-E", "SKY_[A-Z0-9_]+", "--", ".", ":!docs/"])
        .output()
        .map_err(|e| format!("git grep failed to spawn: {e}"))?;
    // `git grep` exits 1 when it matches nothing, which here would mean the
    // derivation broke rather than that the tree is clean.
    let text = String::from_utf8_lossy(&out.stdout);
    let names: BTreeSet<String> = text
        .lines()
        .map(|l| l.trim().to_string())
        .filter(|l| is_env_token(l) && l.len() > 4 && !l.ends_with('_'))
        .collect();
    if names.len() < 40 {
        return Err(format!(
            "found only {} SKY_* names outside docs/ — the derivation has \
             broken, and a broken derivation makes the documentation \
             reconciliation pass vacuously",
            names.len()
        ));
    }
    Ok(names)
}

/// `SKY_*` names appearing in the LIVE docs. `docs/history/` is excluded: it is
/// frozen per-version material and is deliberately not gated (AGENTS.md).
fn documented_names(root: &Path) -> Result<BTreeSet<String>, String> {
    let mut out = BTreeSet::new();
    let dir = root.join("docs");
    let mut stack = vec![dir];
    while let Some(d) = stack.pop() {
        let entries =
            std::fs::read_dir(&d).map_err(|e| format!("cannot read {}: {e}", d.display()))?;
        for entry in entries {
            let path = entry.map_err(|e| e.to_string())?.path();
            if path.is_dir() {
                if path.file_name().and_then(|n| n.to_str()) == Some("history") {
                    continue;
                }
                stack.push(path);
                continue;
            }
            if path.extension().and_then(|e| e.to_str()) != Some("md") {
                continue;
            }
            let src = std::fs::read_to_string(&path)
                .map_err(|e| format!("cannot read {}: {e}", path.display()))?;
            for (idx, _) in src.match_indices("SKY_") {
                let rest = &src[idx..];
                let end = rest
                    .find(|c: char| !(c.is_ascii_uppercase() || c.is_ascii_digit() || c == '_'))
                    .unwrap_or(rest.len());
                let name = &rest[..end];
                if name.len() > 4 && !name.ends_with('_') {
                    out.insert(name.to_string());
                }
            }
        }
    }
    Ok(out)
}

/// A prefixed suffix `LIVE_TTL` is the variable `SKY_LIVE_TTL` under the default
/// prefix; a literal name is already whole.
fn read_names_of(suffixes: &BTreeSet<String>, literals: &BTreeSet<String>) -> BTreeSet<String> {
    suffixes
        .iter()
        .map(|s| format!("SKY_{s}"))
        .chain(literals.iter().cloned())
        .collect()
}

fn is_env_token(s: &str) -> bool {
    !s.is_empty()
        && s.chars()
            .all(|c| c.is_ascii_uppercase() || c.is_ascii_digit() || c == '_')
}

// ─────────────────────────────────────────────────────────────────────────────
// The document
// ─────────────────────────────────────────────────────────────────────────────

struct Measured {
    doc: Value,
    /// Human-readable violations found while computing (not ratchet failures).
    findings: Vec<String>,
    /// One assertion per accepted key + one per seeded suffix + the fixed clauses.
    assertions: u64,
}

/// The fixed ratchet/structural clauses, asserted once each regardless of how
/// many keys exist:
///   1. staleness — the checked-in document matches the recomputation
///   2. no unresolved reader call site
///   3. `pre_binary` count did not rise
///   4. `seeded_without_reader` count did not rise
///   5. `documented_without_reader` count did not rise
///   6. `read_without_doc` count did not rise
const FIXED_CLAUSES: u64 = 6;

fn compute(root: &Path) -> Result<Measured, String> {
    let accepted = accepted_keys(root)?;
    let (pre_binary_reads, mut unresolved) = scan_pre_binary_reads(root)?;
    let seeded = seeded_suffixes(root)?;
    let (read_suffixes, read_literals, go_unresolved) = runtime_reads(root)?;
    unresolved.extend(go_unresolved);
    unresolved.sort();
    let documented = documented_names(root)?;
    let used_outside_docs = names_used_outside_docs(root)?;

    let mut findings = Vec::new();

    // ---- 1. the residual surface -------------------------------------------
    let mut pre_binary: BTreeMap<String, Vec<String>> = BTreeMap::new();
    for r in &pre_binary_reads {
        pre_binary
            .entry(r.key.clone())
            .or_default()
            .push(format!("{} ({})", r.site, r.reader));
    }
    for (key, file, symbol, why) in STRUCTURAL_PRE_BINARY {
        // Verify the citation before trusting it. A declared entry whose reader
        // has been renamed or deleted is a list outliving its reason, which is
        // the failure mode every hand-written taxonomy in this repo carries.
        match std::fs::read_to_string(root.join(file)) {
            Ok(src) if src.contains(symbol) => {}
            Ok(_) => findings.push(format!(
                "STALE CITATION: STRUCTURAL_PRE_BINARY claims `{key}` is read by \
                 `{symbol}` in {file}, and that symbol is no longer there. Either \
                 the reader moved (update the citation) or the surface is no \
                 longer pre-binary (remove the entry)."
            )),
            Err(e) => findings.push(format!(
                "STALE CITATION: STRUCTURAL_PRE_BINARY cites {file} for `{key}`, \
                 which cannot be read: {e}"
            )),
        }
        pre_binary
            .entry((*key).to_string())
            .or_default()
            .push(format!("structural: {file} {symbol} — {why}"));
    }

    // ---- 2. seeded but unread ----------------------------------------------
    let mut seeded_unread = Vec::new();
    for suffix in &seeded {
        if read_suffixes.contains(suffix) {
            continue;
        }
        if read_literals.contains(&format!("SKY_{suffix}")) {
            continue;
        }
        seeded_unread.push(suffix.clone());
    }
    let accounted: BTreeSet<&str> = SEEDED_WITHOUT_READER.iter().map(|(n, _)| *n).collect();
    for suffix in &seeded_unread {
        if !accounted.contains(suffix.as_str()) {
            findings.push(format!(
                "SEEDED, READ BY NOTHING: the compiler emits \
                 `rt.SetSkyDefault(\"{suffix}\", …)` into every program and no \
                 non-test file under runtime-go/ reads it. A user who sets it \
                 gets silence. Wire a reader, delete the emission, or record it \
                 in SEEDED_WITHOUT_READER with a citation."
            ));
        }
    }
    // An accounted entry that is now read is a fixed defect whose accounting
    // must be removed, or the list becomes a place things hide.
    for (suffix, _) in SEEDED_WITHOUT_READER {
        if !seeded_unread.iter().any(|s| s == suffix) {
            findings.push(format!(
                "STALE ACCOUNTING: `{suffix}` is listed in SEEDED_WITHOUT_READER \
                 but is no longer both seeded and unread. Remove the entry."
            ));
        }
    }

    // ---- 3. documentation reconciliation ------------------------------------
    // "Read" for the RUNTIME dimension is what runtime-go reads; "used" for the
    // DOCUMENTATION dimension is the whole tracked tree outside docs/, because
    // a name a script or a CLI verb reads is still a name a user can set.
    let read_names: BTreeSet<String> = read_names_of(&read_suffixes, &read_literals);
    // The used-set is the literal `SKY_*` occurrences UNIONED WITH the suffix
    // form, because `skyGetenv("LIVE_TTL")` is a read of `SKY_LIVE_TTL` and a
    // literal scan cannot see it. Design §1.12 records that omitting this union
    // is what misled an earlier audit into calling the DB pool knobs
    // undocumented when they are documented and read.
    let mut used: BTreeSet<String> = used_outside_docs.clone();
    used.extend(read_names_of(&read_suffixes, &read_literals));
    let documented_without_reader: Vec<String> = documented.difference(&used).cloned().collect();
    let read_without_doc: Vec<String> = read_names.difference(&documented).cloned().collect();

    // ---- 4. classification totality ----------------------------------------
    // Every accepted key is one of: read pre-binary (residual), seeded into the
    // prologue (movable to code), or compile-time-only (a no-op or a
    // diagnostic). A key in none of those is unclassified, which means the
    // measurement does not cover it.
    let seeded_from_toml: BTreeSet<String> = accepted
        .iter()
        .filter(|k| {
            // A key is "emitted" when read_sky_toml_config pushes it into
            // extra_defaults; the mapping is many-to-one, so the honest test is
            // whether the key's section is one the parser seeds from at all.
            let section = k.split('.').next().unwrap_or("");
            matches!(
                section,
                "live" | "database" | "log" | "analytics" | "jobs" | "security" | "env"
            )
        })
        .cloned()
        .collect();
    let mut unclassified = Vec::new();
    for key in &accepted {
        let residual = pre_binary.contains_key(key)
            || pre_binary.contains_key(key.split('.').nth(1).unwrap_or(""));
        if !residual && !seeded_from_toml.contains(key) {
            unclassified.push(key.clone());
        }
    }
    for key in &unclassified {
        findings.push(format!(
            "UNCLASSIFIED: `{key}` is accepted by read_sky_toml_config but is \
             neither read pre-binary nor seeded from a section the parser seeds. \
             The measurement does not cover it."
        ));
    }

    for u in &unresolved {
        findings.push(format!(
            "UNRESOLVED READ SITE: {u}. The residual-surface count is a LOWER \
             BOUND until this is resolved."
        ));
    }

    let assertions = accepted.len() as u64 + seeded.len() as u64 + FIXED_CLAUSES;

    let doc = json!({
        "_generated_by": "cargo run -p xtask -- config-surface",
        "_about": "The configuration surface, measured. See docs/tooling/config-architecture.md §12 risk 1 and rust/crates/xtask/src/config_surface.rs.",
        "_do_not_hand_edit": true,
        "summary": {
            "accepted_sky_toml_keys": accepted.len(),
            "pre_binary_surfaces": pre_binary.len(),
            "seeded_suffixes": seeded.len(),
            "runtime_read_suffixes": read_suffixes.len(),
            "runtime_read_literal_sky_names": read_literals.len(),
            "seeded_without_reader": seeded_unread.len(),
            "documented_names": documented.len(),
            "documented_without_reader": documented_without_reader.len(),
            "read_without_doc": read_without_doc.len(),
            "unresolved_read_sites": unresolved.len(),
            "unclassified_keys": unclassified.len(),
        },
        "pre_binary": pre_binary,
        "accepted_sky_toml_keys": accepted.iter().cloned().collect::<Vec<_>>(),
        "seeded_suffixes": seeded.iter().cloned().collect::<Vec<_>>(),
        "seeded_without_reader": seeded_unread,
        "runtime_read_suffixes": read_suffixes.iter().cloned().collect::<Vec<_>>(),
        "runtime_read_literal_sky_names": read_literals.iter().cloned().collect::<Vec<_>>(),
        "documented_without_reader": documented_without_reader,
        "read_without_doc": read_without_doc,
        "unresolved_read_sites": unresolved,
        "unclassified_keys": unclassified,
    });

    Ok(Measured {
        doc,
        findings,
        assertions,
    })
}

// ─────────────────────────────────────────────────────────────────────────────
// The ratchet
// ─────────────────────────────────────────────────────────────────────────────

/// Counts that may FALL (progress) and may not RISE (regression).
const RATCHETED: &[(&str, &str)] = &[
    (
        "pre_binary_surfaces",
        "a new pre-binary read makes the residual surface larger — the thing \
         design §12 risk 1 says to measure before committing to the split",
    ),
    (
        "seeded_without_reader",
        "a new setting that is emitted and read by nothing is the `[auth]` \
         defect recurring (design §1.11)",
    ),
    (
        "documented_without_reader",
        "a new documented name nothing reads is a setting a user will set and \
         watch do nothing (design §1.12)",
    ),
    (
        "read_without_doc",
        "a new read name in no live doc is a knob no user can discover \
         (design §1.12)",
    ),
];

/// An authorised rise, read from `docs/coverage/removals.toml`.
///
/// The ratchet cannot tell "the surface grew" from "the measurement got
/// better", and both raise the number. So a rise is allowed only when a person
/// has written down which it was — pinned to the EXACT `from` and `to`, so one
/// stanza cannot silently authorise a second, different rise later.
#[derive(Debug, PartialEq, Eq)]
struct AuthorisedRise {
    metric: String,
    from: u64,
    to: u64,
}

fn parse_authorised_rises(text: &str) -> Result<Vec<AuthorisedRise>, String> {
    let mut out = Vec::new();
    let mut in_stanza = false;
    let mut fields: BTreeMap<String, String> = BTreeMap::new();

    let finish = |fields: &BTreeMap<String, String>,
                  out: &mut Vec<AuthorisedRise>|
     -> Result<(), String> {
        if fields.is_empty() {
            return Ok(());
        }
        for required in ["metric", "from", "to", "reason", "owner", "commit"] {
            let v = fields.get(required).map(String::as_str).unwrap_or("");
            if v.is_empty() {
                return Err(format!(
                    "a [[config-surface-rise]] stanza is missing `{required}`. All six \
                     fields are required — an empty stanza would buy a free rise, which \
                     is exactly what this ledger exists to prevent. Stanza so far: {fields:?}"
                ));
            }
        }
        let parse = |k: &str| -> Result<u64, String> {
            fields[k]
                .parse::<u64>()
                .map_err(|_| format!("[[config-surface-rise]] `{k}` is not a number: {}", fields[k]))
        };
        out.push(AuthorisedRise {
            metric: fields["metric"].clone(),
            from: parse("from")?,
            to: parse("to")?,
        });
        Ok(())
    };

    for raw in text.lines() {
        let line = raw.trim();
        if line.starts_with("#") {
            continue;
        }
        if line.starts_with("[[") {
            finish(&fields, &mut out)?;
            fields.clear();
            in_stanza = line == "[[config-surface-rise]]";
            continue;
        }
        if !in_stanza {
            continue;
        }
        if let Some((k, v)) = line.split_once('=') {
            let v = v.trim().trim_matches('"').trim().to_string();
            fields.insert(k.trim().to_string(), v);
        }
    }
    finish(&fields, &mut out)?;
    Ok(out)
}

fn ratchet(baseline: Option<&Value>, current: &Value, authorised: &[AuthorisedRise]) -> Vec<String> {
    let Some(base) = baseline else {
        return Vec::new();
    };
    let mut fails = Vec::new();
    for (metric, why) in RATCHETED {
        let b = base
            .get("summary")
            .and_then(|s| s.get(metric))
            .and_then(Value::as_u64);
        let c = current
            .get("summary")
            .and_then(|s| s.get(metric))
            .and_then(Value::as_u64);
        match (b, c) {
            (Some(b), Some(c))
                if c > b
                    && !authorised.iter().any(|a| {
                        a.metric == *metric && a.from == b && a.to == c
                    }) =>
            {
                fails.push(format!(
                    "RATCHET — `{metric}` rose {b} -> {c}.\n  Why this is gated: {why}.\n  \
                     If the rise is real, fix it. If the MEASUREMENT improved and the \
                     surface did not, say so in docs/coverage/removals.toml:\n\
                     \n    [[config-surface-rise]]\n    metric = \"{metric}\"\n    \
                     from   = {b}\n    to     = {c}\n    reason = \"…\"\n    \
                     owner  = \"…\"\n    commit = \"…\""
                ))
            }
            (Some(_), None) => fails.push(format!(
                "RATCHET — `{metric}` disappeared from the recomputed document. \
                 A metric that stops being computed cannot be seen to regress."
            )),
            _ => {}
        }
    }
    fails
}

fn diff(base: &Value, cur: &Value) -> String {
    let mut lines = Vec::new();
    if let (Some(b), Some(c)) = (
        base.get("summary").and_then(Value::as_object),
        cur.get("summary").and_then(Value::as_object),
    ) {
        for (k, bv) in b {
            match c.get(k) {
                Some(cv) if cv != bv => lines.push(format!("  summary.{k}: {bv} -> {cv}")),
                None => lines.push(format!("  summary.{k}: {bv} -> (gone)")),
                _ => {}
            }
        }
        for k in c.keys() {
            if !b.contains_key(k) {
                lines.push(format!("  summary.{k}: (new)"));
            }
        }
    }
    if lines.is_empty() {
        lines.push("  (the summary matches; a detail list differs)".to_string());
    }
    lines.join("\n")
}

// ─────────────────────────────────────────────────────────────────────────────
// Entry points
// ─────────────────────────────────────────────────────────────────────────────

fn out_path(root: &Path) -> PathBuf {
    root.join("docs/coverage/config-surface.json")
}

/// Authorised rises live in the same ledger as the denominator removals, so a
/// reviewer looking for "what was written down about coverage moving" has one
/// file to read.
fn read_authorised_rises(root: &Path) -> Result<Vec<AuthorisedRise>, String> {
    let path = root.join("docs/coverage/removals.toml");
    let text = std::fs::read_to_string(&path)
        .map_err(|e| format!("cannot read {}: {e}", path.display()))?;
    parse_authorised_rises(&text)
}

pub fn run(args: &[String], repo_root: &Path) -> i32 {
    let check_only = args.iter().any(|a| a == "--check");
    let measured = match compute(repo_root) {
        Ok(m) => m,
        Err(e) => {
            eprintln!("xtask config-surface: FAILED to compute\n{e}");
            return 1;
        }
    };
    let path = out_path(repo_root);
    let baseline: Option<Value> = std::fs::read_to_string(&path)
        .ok()
        .and_then(|s| serde_json::from_str(&s).ok());

    print_report(&measured);

    let authorised = match read_authorised_rises(repo_root) {
        Ok(a) => a,
        Err(e) => {
            eprintln!("xtask config-surface: {e}");
            return 1;
        }
    };
    let mut fails = measured.findings.clone();
    fails.extend(ratchet(baseline.as_ref(), &measured.doc, &authorised));

    if check_only {
        match &baseline {
            None => fails.push(format!(
                "STALE — {} does not exist. Run `cargo run -p xtask -- config-surface`.",
                path.display()
            )),
            Some(base) if *base != measured.doc => fails.push(format!(
                "STALE — {} does not match the recomputed measurement:\n{}\n\
                 Run `cargo run -p xtask -- config-surface` and read the diff \
                 before committing it.",
                path.display(),
                diff(base, &measured.doc)
            )),
            _ => {}
        }
    }

    if !fails.is_empty() {
        eprintln!();
        for f in &fails {
            eprintln!("{f}\n");
        }
        eprintln!(
            "xtask config-surface{}: FAIL — {} violation(s).",
            if check_only { " --check" } else { "" },
            fails.len()
        );
        return 1;
    }

    if check_only {
        println!("\nxtask config-surface --check: PASS — the checked-in measurement is current and the ratchet holds.");
        return 0;
    }

    if let Some(parent) = path.parent() {
        if let Err(e) = std::fs::create_dir_all(parent) {
            eprintln!("cannot create {}: {e}", parent.display());
            return 1;
        }
    }
    let text = format!(
        "{}\n",
        serde_json::to_string_pretty(&measured.doc).expect("serialise")
    );
    if let Err(e) = std::fs::write(&path, text) {
        eprintln!("cannot write {}: {e}", path.display());
        return 1;
    }
    println!("\nxtask config-surface: wrote {}", path.display());
    0
}

fn print_report(m: &Measured) {
    let s = &m.doc["summary"];
    println!("xtask config-surface — the configuration surface, measured\n");
    println!(
        "  sky.toml keys accepted by the compiler      {}",
        s["accepted_sky_toml_keys"]
    );
    println!(
        "  surfaces read PRE-BINARY (residual)         {}   <- design §12 risk 1",
        s["pre_binary_surfaces"]
    );
    println!(
        "  env suffixes seeded into every prologue      {}",
        s["seeded_suffixes"]
    );
    println!(
        "  env suffixes the runtime reads               {}",
        s["runtime_read_suffixes"]
    );
    println!(
        "  seeded and read by NOTHING                   {}   <- design §1.11",
        s["seeded_without_reader"]
    );
    println!(
        "  documented names with no reader              {}   <- design §1.12",
        s["documented_without_reader"]
    );
    println!(
        "  read names in no live doc                    {}   <- design §1.12",
        s["read_without_doc"]
    );
    println!(
        "\n  assertions: {} ({} accepted keys + {} seeded suffixes + {FIXED_CLAUSES} fixed clauses)",
        m.assertions, s["accepted_sky_toml_keys"], s["seeded_suffixes"]
    );
}

/// The harness-gate face: recompute, ratchet, and verify the checked-in
/// document — writing nothing.
pub fn check_body(repo_root: &Path) -> (bool, u64, String) {
    let measured = match compute(repo_root) {
        Ok(m) => m,
        Err(e) => return (false, 0, format!("could not compute: {e}")),
    };
    let path = out_path(repo_root);
    let baseline: Option<Value> = std::fs::read_to_string(&path)
        .ok()
        .and_then(|s| serde_json::from_str(&s).ok());

    let authorised = match read_authorised_rises(repo_root) {
        Ok(a) => a,
        Err(e) => {
            eprintln!("xtask config-surface: {e}");
            return (false, 0, e);
        }
    };
    let mut fails = measured.findings.clone();
    fails.extend(ratchet(baseline.as_ref(), &measured.doc, &authorised));
    match &baseline {
        None => fails.push(format!("STALE — {} does not exist", path.display())),
        Some(base) if *base != measured.doc => fails.push(format!(
            "STALE — {} does not match the recomputed measurement:\n{}",
            path.display(),
            diff(base, &measured.doc)
        )),
        _ => {}
    }

    if fails.is_empty() {
        let s = &measured.doc["summary"];
        (
            true,
            measured.assertions,
            format!(
                "{} accepted keys and {} seeded suffixes verified; {} pre-binary \
                 surfaces, {} seeded-without-reader; ratchet holds",
                s["accepted_sky_toml_keys"],
                s["seeded_suffixes"],
                s["pre_binary_surfaces"],
                s["seeded_without_reader"],
            ),
        )
    } else {
        (false, measured.assertions, fails.join("\n"))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn repo_root() -> PathBuf {
        PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../.."))
    }

    fn args_of(src: &str, call: &str) -> Vec<Arg> {
        let idx = src.find(call).unwrap() + call.len() - 1;
        call_args(src, idx).unwrap()
    }

    #[test]
    fn call_args_are_positional() {
        let src = r#"let x = sky_toml_section_key(dir, "database", "embedded");"#;
        assert_eq!(
            args_of(src, "sky_toml_section_key("),
            vec![
                Arg::Expr("dir".into()),
                Arg::Lit("database".into()),
                Arg::Lit("embedded".into())
            ]
        );
    }

    /// The positional split is what makes `sky_toml_project_key(dir, "bin",
    /// "app")` read `bin` rather than mistaking the DEFAULT for the key.
    #[test]
    fn call_args_do_not_confuse_a_default_for_a_key() {
        let src = r#"sky_toml_project_key(project_dir, "bin", "app")"#;
        let args = args_of(src, "sky_toml_project_key(");
        assert_eq!(args.get(1), Some(&Arg::Lit("bin".into())));
        assert_eq!(args.get(2), Some(&Arg::Lit("app".into())));
    }

    #[test]
    fn call_args_survive_nested_calls() {
        let src = r#"f(g(a, b), "env", "prefix")"#;
        assert_eq!(
            args_of(src, "f("),
            vec![
                Arg::Expr("g(a, b)".into()),
                Arg::Lit("env".into()),
                Arg::Lit("prefix".into())
            ]
        );
    }

    #[test]
    fn call_args_report_unbalanced() {
        assert!(call_args(r#"f("a", "b""#, 1).is_none());
    }

    #[test]
    fn a_key_named_by_a_const_resolves() {
        let src = r#"
pub const PIN_KEY: &str = "postgresVersion";
pub fn pinned_version(d: &Path) -> Option<String> {
    project::sky_toml_section_key(d, "database", PIN_KEY)
}
"#;
        let consts = str_consts(src);
        assert_eq!(consts.get("PIN_KEY").map(String::as_str), Some("postgresVersion"));
        let args = args_of(src, "sky_toml_section_key(");
        assert_eq!(
            resolve_arg(&args[2], &consts).as_deref(),
            Some("postgresVersion"),
            "a key named by a const must resolve, or `sky db start`'s read of \
             [database] postgresVersion is invisible to the measurement"
        );
    }

    /// `build.rs`'s own unit tests read `sky_toml_section_key(&dir, "nosuch",
    /// "url")`. Counting that as residual surface inflated the pre-binary count
    /// by three the first time this gate ran.
    #[test]
    fn cfg_test_bodies_are_not_deployment_surface() {
        let src = "fn real() { sky_toml_flag(d, \"database\", \"embedded\"); }\n\
                   #[cfg(test)]\nmod tests {\n    fn t() { sky_toml_flag(d, \"nosuch\", \"url\"); }\n}\n";
        let ranges = cfg_test_ranges(src);
        let real = src.find("\"database\"").unwrap();
        let fake = src.find("\"nosuch\"").unwrap();
        assert!(!in_any(&ranges, real), "a real read must not be excluded");
        assert!(in_any(&ranges, fake), "a #[cfg(test)] read must be excluded");
    }

    #[test]
    fn enclosing_fn_finds_the_owner() {
        let src = "\npub fn sky_toml_flag(a: u8) -> bool {\n    sky_toml_section_key(a, b, c)\n}\n";
        let idx = src.find("sky_toml_section_key(").unwrap();
        assert_eq!(enclosing_fn(src, idx), Some("sky_toml_flag"));
    }

    /// The derivations must find a real surface. A silent derivation break
    /// would make every count zero and every ratchet clause vacuous — which is
    /// the failure mode `docs/tooling/gate-harness.md` exists to forbid.
    #[test]
    fn the_derivations_are_not_vacuous() {
        let root = repo_root();
        let accepted = accepted_keys(&root).expect("accepted keys");
        assert!(
            accepted.contains("live.ttl"),
            "the accepted-key derivation lost `[live] ttl`: {accepted:?}"
        );
        assert!(
            !accepted.contains("auth.tokenTtl"),
            "the inert `[auth]` block was removed (design §1.11); \
             `auth.tokenTtl` must NOT be an accepted runtime key"
        );

        let seeded = seeded_suffixes(&root).expect("seeded suffixes");
        assert!(
            seeded.contains("LIVE_TTL"),
            "the seeded-suffix derivation lost LIVE_TTL, which lower.rs emits \
             unconditionally for every program"
        );

        let (read, _, go_unresolved) = runtime_reads(&root).expect("runtime reads");
        assert!(
            read.contains("LIVE_TTL"),
            "the runtime-read derivation lost LIVE_TTL"
        );
        assert!(
            read.contains("DB_MAX_OPEN_CONNS"),
            "the runtime-read derivation lost DB_MAX_OPEN_CONNS, which is read \
             through the `dbEnvInt` wrapper rather than a bare `skyGetenv` — the \
             blind spot PREFIXED_READ_HELPERS exists to close"
        );
        assert!(
            go_unresolved.is_empty(),
            "a prefixed env read is invisible to the derivation: {go_unresolved:?}"
        );

        let (pre, _) = scan_pre_binary_reads(&root).expect("pre-binary scan");
        assert!(
            pre.iter().any(|r| r.key == "database.postgresVersion"),
            "the pre-binary scan lost `[database] postgresVersion`, which \
             `sky db start` reads with no binary in existence"
        );
    }

    /// Every declared taxonomy entry must still describe the tree. A list that
    /// outlives its reason is where defects hide.
    #[test]
    fn declared_taxonomies_carry_reasons() {
        for (name, file, symbol, why) in STRUCTURAL_PRE_BINARY {
            assert!(
                why.len() > 40,
                "STRUCTURAL_PRE_BINARY `{name}` needs a citation, not a label"
            );
            assert!(
                symbol.starts_with("fn "),
                "STRUCTURAL_PRE_BINARY `{name}` must cite a function, got `{symbol}`"
            );
            let src = std::fs::read_to_string(repo_root().join(file))
                .unwrap_or_else(|e| panic!("STRUCTURAL_PRE_BINARY `{name}` cites {file}: {e}"));
            assert!(
                src.contains(symbol),
                "STRUCTURAL_PRE_BINARY `{name}` cites `{symbol}` in {file}, which is not there"
            );
        }
        for (name, why) in SEEDED_WITHOUT_READER {
            assert!(
                why.len() > 40,
                "SEEDED_WITHOUT_READER `{name}` needs a citation, not a label"
            );
        }
    }

    #[test]
    fn the_ratchet_goes_red_on_a_rise() {
        let base = json!({"summary": {"pre_binary_surfaces": 3, "seeded_without_reader": 3,
                                      "documented_without_reader": 25, "read_without_doc": 46}});
        let worse = json!({"summary": {"pre_binary_surfaces": 4, "seeded_without_reader": 3,
                                       "documented_without_reader": 25, "read_without_doc": 46}});
        let better = json!({"summary": {"pre_binary_surfaces": 2, "seeded_without_reader": 3,
                                        "documented_without_reader": 25, "read_without_doc": 46}});
        assert_eq!(ratchet(Some(&base), &worse, &[]).len(), 1);
        assert!(ratchet(Some(&base), &better, &[]).is_empty());
    }

    /// A rise passes only when a stanza names the EXACT from/to. This is what
    /// stops one authorisation quietly covering a later, different rise.
    #[test]
    fn an_authorised_rise_passes_and_only_that_one() {
        let base = json!({"summary": {"pre_binary_surfaces": 12, "seeded_without_reader": 3,
                                      "documented_without_reader": 10, "read_without_doc": 29}});
        let to14 = json!({"summary": {"pre_binary_surfaces": 14, "seeded_without_reader": 3,
                                      "documented_without_reader": 10, "read_without_doc": 29}});
        let to15 = json!({"summary": {"pre_binary_surfaces": 15, "seeded_without_reader": 3,
                                      "documented_without_reader": 10, "read_without_doc": 29}});
        let ok = [AuthorisedRise {
            metric: "pre_binary_surfaces".into(),
            from: 12,
            to: 14,
        }];
        assert!(ratchet(Some(&base), &to14, &ok).is_empty());
        assert_eq!(
            ratchet(Some(&base), &to15, &ok).len(),
            1,
            "a stanza for 12->14 must not authorise 12->15"
        );
    }

    #[test]
    fn an_incomplete_rise_stanza_is_refused() {
        let text = "[[config-surface-rise]]\nmetric = \"pre_binary_surfaces\"\nfrom = 12\nto = 14\n";
        let err = parse_authorised_rises(text).unwrap_err();
        assert!(
            err.contains("reason"),
            "a stanza with no reason must be refused, got: {err}"
        );
    }

    #[test]
    fn a_complete_rise_stanza_parses() {
        let text = "# a comment\n\
                    [[removal]]\nsymbol = \"x\"\n\
                    [[config-surface-rise]]\n\
                    metric = \"pre_binary_surfaces\"\nfrom   = 12\nto     = 14\n\
                    reason = \"the measurement improved\"\nowner  = \"me\"\ncommit = \"abc\"\n";
        assert_eq!(
            parse_authorised_rises(text).unwrap(),
            vec![AuthorisedRise {
                metric: "pre_binary_surfaces".into(),
                from: 12,
                to: 14
            }]
        );
    }

    /// The checked-in ledger must parse. A malformed stanza that only surfaced
    /// on the next rise would make the ratchet fail for the wrong reason.
    #[test]
    fn the_checked_in_ledger_parses() {
        read_authorised_rises(&repo_root()).expect("docs/coverage/removals.toml");
    }

    #[test]
    fn a_metric_that_stops_being_computed_fails() {
        let base = json!({"summary": {"pre_binary_surfaces": 3, "seeded_without_reader": 3,
                                      "documented_without_reader": 25, "read_without_doc": 46}});
        let gone = json!({"summary": {"seeded_without_reader": 3,
                                      "documented_without_reader": 25, "read_without_doc": 46}});
        assert_eq!(ratchet(Some(&base), &gone, &[]).len(), 1);
    }
}

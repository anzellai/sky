//! `xtask divergences` — enforces the known-divergences ledger.
//!
//! `known-divergences.toml` (repo root) is the authoritative list of INTENTIONAL
//! differences between the Rust compiler and the Haskell oracle; the contract is
//! "Rust matches the oracle EXCEPT these entries; anything unlisted is a bug"
//! (see the file header + docs/rust-rewrite/known-divergences.md).
//!
//! This gate keeps the ledger HONEST for the `rust-stricter` direction — cases
//! the oracle ACCEPTS but Rust REJECTS (which fall between the accept-parity and
//! reject-parity gates, so nothing else catches a regression). For each fixture
//! under `crates/xtask/divergence-fixtures/*.sky` it:
//!
//!   1. reads the machine-readable header
//!      `-- divergence: <id> code=<CODE> rust=<REJECT|ACCEPT>`,
//!   2. cross-checks that `<id>` is documented in known-divergences.toml (so the
//!      fixture and the human ledger can't drift), and
//!   3. re-runs the Rust checker on the fixture (in-process, against the real
//!      stdlib, exactly like `xtask reject`) and asserts the outcome matches:
//!      a REJECT entry must produce the ledgered diagnostic `<CODE>`; an ACCEPT
//!      entry must type-check clean.
//!
//! If someone regresses the intentional divergence (e.g. Rust stops enforcing
//! `exposing` on stdlib, so D001 starts being ACCEPTED), this gate fails instead
//! of the regression sliding through silently. The oracle side of each entry is
//! documentation, verified at authoring time with a differential probe against
//! the absolute oracle path (the oracle isn't available in CI).

use hir::SourceDb;
use std::path::{Path, PathBuf};

pub fn run(_args: &[String], repo_root: &Path) -> i32 {
    let ledger_path = repo_root.join("known-divergences.toml");
    let ledger = std::fs::read_to_string(&ledger_path).unwrap_or_default();
    if ledger.trim().is_empty() {
        eprintln!(
            "DIVERGENCES GATE: no known-divergences.toml at {}",
            ledger_path.display()
        );
        return 1;
    }

    let stdlib = load_dir(&repo_root.join("sky-stdlib"), "sky-stdlib");
    if stdlib.is_empty() {
        eprintln!("DIVERGENCES GATE: no stdlib modules under sky-stdlib/");
        return 1;
    }

    let fixtures_dir = repo_root.join("rust/crates/xtask/divergence-fixtures");
    let mut fixtures = Vec::new();
    collect_sky(&fixtures_dir, &mut fixtures);
    fixtures.sort();
    if fixtures.is_empty() {
        eprintln!(
            "DIVERGENCES GATE: no fixtures under {}",
            fixtures_dir.display()
        );
        return 1;
    }

    let mut fails: Vec<String> = Vec::new();
    let mut checked = 0usize;
    // The ids the gate ACTUALLY re-verified this run (a fixture with that header
    // id was found + type-checked). Used below to close the ledger→fixture
    // direction: every ledgered divergence MUST have been re-verified here.
    let mut verified_ids: Vec<String> = Vec::new();

    for f in &fixtures {
        let name = f
            .file_name()
            .and_then(|s| s.to_str())
            .unwrap_or("?")
            .to_string();
        let src = std::fs::read_to_string(f).unwrap_or_default();

        let Some(hdr) = parse_header(&src) else {
            fails.push(format!(
                "{name}: missing/invalid `-- divergence: <id> code=<CODE> rust=<REJECT|ACCEPT>` header"
            ));
            continue;
        };

        // (2) ledger cross-reference: the human ledger must document this id.
        if !ledger.contains(&format!("id = \"{}\"", hdr.id)) {
            fails.push(format!(
                "{name}: id `{}` not documented in known-divergences.toml",
                hdr.id
            ));
        }
        verified_ids.push(hdr.id.clone());

        // (3) re-verify the Rust behaviour in-process.
        let outcome = check_rust(&src, &stdlib);
        checked += 1;
        match hdr.rust {
            Expect::Reject => {
                if !outcome.rejected {
                    fails.push(format!(
                        "{name} [{}]: expected REJECT [{}], but the Rust checker ACCEPTED it \
                         (intentional divergence regressed?)",
                        hdr.id, hdr.code
                    ));
                } else if !outcome.codes.iter().any(|c| c == &hdr.code) {
                    fails.push(format!(
                        "{name} [{}]: rejected, but NOT with the ledgered code [{}] — got [{}]",
                        hdr.id,
                        hdr.code,
                        outcome.codes.join(", ")
                    ));
                }
            }
            Expect::Accept => {
                if outcome.rejected {
                    fails.push(format!(
                        "{name} [{}]: expected ACCEPT, but the Rust checker rejected it [{}]",
                        hdr.id,
                        outcome.codes.join(", ")
                    ));
                }
            }
        }
    }

    // (4) ledger→fixture completeness: EVERY `[[divergence]]` entry in the ledger
    // must have been re-verified by a fixture above, and its declared `fixture =`
    // path must exist on disk. This closes the reverse of check (2): (2) stops an
    // UNDOCUMENTED fixture; (4) stops a documented divergence with NO fixture — a
    // "silently-dropped intentional rejection", where someone records that Rust
    // intentionally rejects X in the ledger but never ships a fixture, so the
    // gate never re-verifies that Rust still rejects X. Without (4) the ledger
    // could grow entries the gate does not actually constrain.
    let ledger_entries = parse_ledger_entries(&ledger);
    if ledger_entries.is_empty() {
        fails.push(
            "known-divergences.toml has no parseable `[[divergence]]` `id = \"…\"` entries".into(),
        );
    }
    for entry in &ledger_entries {
        if !verified_ids.contains(&entry.id) {
            fails.push(format!(
                "ledger id `{}` has NO divergence-fixture that the gate re-verified \
                 (add a fixture under rust/crates/xtask/divergence-fixtures/ whose header \
                 reads `-- divergence: {} code=<CODE> rust=<REJECT|ACCEPT>` — a silently-\
                 dropped intentional rejection is otherwise unconstrained)",
                entry.id, entry.id
            ));
        }
        if let Some(fx) = &entry.fixture {
            if !repo_root.join(fx).exists() {
                fails.push(format!(
                    "ledger id `{}` declares `fixture = \"{}\"` but that file does not exist",
                    entry.id, fx
                ));
            }
        }
    }

    if fails.is_empty() {
        println!(
            "DIVERGENCES GATE: PASS  ({checked} ledgered divergence(s) re-verified; \
             {} ledger entr(y/ies) ↔ fixtures in sync, both directions)",
            ledger_entries.len()
        );
        return 0;
    }

    println!("DIVERGENCES GATE: FAIL");
    for m in &fails {
        println!("  {m}");
    }
    println!(
        "\nknown-divergences.toml is the authoritative Rust-vs-oracle ledger; \
         a failure here means an intentional divergence regressed OR a fixture \
         is undocumented. Re-verify with a differential probe (sky-out/sky) and \
         update the ledger + fixture together."
    );
    1
}

enum Expect {
    Reject,
    Accept,
}

struct Header {
    id: String,
    code: String,
    rust: Expect,
}

/// Parse the `-- divergence: <id> code=<CODE> rust=<REJECT|ACCEPT>` header line.
fn parse_header(src: &str) -> Option<Header> {
    let line = src
        .lines()
        .find(|l| l.trim_start().starts_with("-- divergence:"))?;
    let after = line.split("divergence:").nth(1)?.trim();
    let mut id = None;
    let mut code = None;
    let mut rust = None;
    for (i, tok) in after.split_whitespace().enumerate() {
        if i == 0 {
            id = Some(tok.to_string());
        } else if let Some(v) = tok.strip_prefix("code=") {
            code = Some(v.to_string());
        } else if let Some(v) = tok.strip_prefix("rust=") {
            rust = match v {
                "REJECT" => Some(Expect::Reject),
                "ACCEPT" => Some(Expect::Accept),
                _ => None,
            };
        }
    }
    Some(Header {
        id: id?,
        code: code?,
        rust: rust?,
    })
}

/// A `[[divergence]]` block from the ledger — just the fields this gate needs
/// for the completeness cross-check.
struct LedgerEntry {
    id: String,
    fixture: Option<String>,
}

/// Parse `known-divergences.toml`'s `[[divergence]]` blocks into `(id, fixture)`
/// pairs. Deliberately a small line scanner (no toml dep): each block starts at
/// `[[divergence]]`, and within it the first `id = "…"` / `fixture = "…"` lines
/// are captured. This mirrors the existing `ledger.contains("id = \"…\"")`
/// string-match style already used for the forward cross-reference.
fn parse_ledger_entries(ledger: &str) -> Vec<LedgerEntry> {
    let mut entries: Vec<LedgerEntry> = Vec::new();
    let mut cur: Option<LedgerEntry> = None;
    let quoted = |line: &str, key: &str| -> Option<String> {
        let rest = line.trim_start().strip_prefix(key)?.trim_start();
        let rest = rest.strip_prefix('=')?.trim_start();
        let rest = rest.strip_prefix('"')?;
        let end = rest.find('"')?;
        Some(rest[..end].to_string())
    };
    for line in ledger.lines() {
        let t = line.trim_start();
        if t.starts_with("[[divergence]]") {
            if let Some(e) = cur.take() {
                entries.push(e);
            }
            cur = Some(LedgerEntry {
                id: String::new(),
                fixture: None,
            });
            continue;
        }
        if let Some(e) = cur.as_mut() {
            if e.id.is_empty() {
                if let Some(v) = quoted(t, "id") {
                    e.id = v;
                    continue;
                }
            }
            if e.fixture.is_none() {
                if let Some(v) = quoted(t, "fixture") {
                    e.fixture = Some(v);
                }
            }
        }
    }
    if let Some(e) = cur.take() {
        entries.push(e);
    }
    entries.retain(|e| !e.id.is_empty());
    entries
}

struct Outcome {
    rejected: bool,
    codes: Vec<String>,
}

/// Run the Rust front-end (parse + resolve + typecheck) on `src` with the stdlib
/// loaded, mirroring `xtask reject`'s `check_one`. Returns whether it rejected +
/// the error diagnostic codes seen (parse, resolve, and type phases).
fn check_rust(src: &str, stdlib: &[(String, syntax::Parse)]) -> Outcome {
    let mut db = SourceDb::new();
    for (n, parse) in stdlib {
        db.add_module(n, parse.clone());
    }
    let parse = syntax::parse(src, base::FileId(0));

    let mut codes: Vec<String> = Vec::new();
    let parse_err = parse
        .errors()
        .iter()
        .filter(|d| d.severity == diagnostics::Severity::Error)
        .inspect(|d| codes.push(d.code.0.to_string()))
        .count()
        .max(parse.error_node_count().min(1));

    let mname = parse
        .tree()
        .module_header()
        .and_then(|h| h.name())
        .map(|n| n.text())
        .filter(|s| !s.is_empty())
        .unwrap_or_else(|| "Main".to_string());
    let mid = db.add_module(&mname, parse);
    let out = ty::check_modules(&db, &[mid]);
    for d in &out.diagnostics {
        if d.severity == diagnostics::Severity::Error {
            codes.push(d.code.0.to_string());
        }
    }
    codes.sort();
    codes.dedup();

    let rejected = parse_err > 0 || out.name_errors > 0 || out.type_errors > 0;
    Outcome { rejected, codes }
}

// ---- module loading (mirrors reject_gate / infer_gate) --------------------

fn load_dir(dir: &Path, root_marker: &str) -> Vec<(String, syntax::Parse)> {
    let mut files = Vec::new();
    collect_sky(dir, &mut files);
    files.sort();
    let mut out = Vec::new();
    for path in files {
        let Ok(src) = std::fs::read_to_string(&path) else {
            continue;
        };
        let parse = syntax::parse(&src, base::FileId(0));
        let name = parse
            .tree()
            .module_header()
            .and_then(|h| h.name())
            .map(|n| n.text())
            .filter(|s| !s.is_empty())
            .unwrap_or_else(|| {
                path.strip_prefix(dir)
                    .unwrap_or(&path)
                    .with_extension("")
                    .to_string_lossy()
                    .replace(['/', '\\'], ".")
                    .trim_start_matches(&format!("{root_marker}."))
                    .to_string()
            });
        out.push((name, parse));
    }
    out
}

fn collect_sky(dir: &Path, out: &mut Vec<PathBuf>) {
    let Ok(rd) = std::fs::read_dir(dir) else {
        return;
    };
    let mut entries: Vec<PathBuf> = rd.filter_map(|e| e.ok().map(|e| e.path())).collect();
    entries.sort();
    for path in entries {
        if path.is_dir() {
            collect_sky(&path, out);
        } else if path.extension().and_then(|s| s.to_str()) == Some("sky") {
            out.push(path);
        }
    }
}

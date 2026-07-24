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

    if fails.is_empty() {
        println!(
            "DIVERGENCES GATE: PASS  ({checked} ledgered divergence(s) re-verified; \
             fixtures ↔ known-divergences.toml in sync)"
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

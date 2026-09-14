//! Coverage for `Std.Ai.Memory.Pg` — long-term agent memory on PostgreSQL +
//! pgvector, against a REAL cluster (the embedded bundle ships pgvector).
//!
//! The `ai-memory-pg` fixture (`[database] embedded = true`) stores three
//! passages with 3-dim embeddings and recalls them:
//!
//!     memory search=a hybrid=b
//!
//! which proves (1) `search` returns the vector-nearest passage ("a" for a query
//! near a's vector) and (2) `hybridSearch` lets a keyword override the vector (the
//! same query plus the keyword "banana", only in b's text, recalls "b").
//!
//! Gated on a discoverable pgvector-carrying PostgreSQL. The `.Pg` module needs
//! the `vector` extension, which a stock system PostgreSQL usually lacks, so the
//! test looks for the provisioned embedded bundle (`sky db provision --embed`,
//! which ships pgvector) or a `SKY_POSTGRES_BIN` whose lib carries `vector`. When
//! neither is present it early-returns with a note, matching the other live tests.
//! Also needs a `go` toolchain.

use std::path::{Path, PathBuf};
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

/// True when a PostgreSQL carrying the `vector` extension is reachable. Checks a
/// `SKY_POSTGRES_BIN`'s sibling `lib` first, then every provisioned embedded
/// bundle under `~/.sky/postgres/*/lib`. pgvector's shared object is
/// `vector.dylib` (macOS) or `vector.so` (Linux).
fn have_pgvector() -> bool {
    if let Ok(bin) = std::env::var("SKY_POSTGRES_BIN") {
        if lib_dir_has_vector(Path::new(&bin).parent()) {
            return true;
        }
    }
    if let Ok(home) = std::env::var("HOME") {
        let root = PathBuf::from(home).join(".sky/postgres");
        if let Ok(entries) = std::fs::read_dir(&root) {
            for e in entries.flatten() {
                if lib_dir_has_vector(Some(&e.path())) {
                    return true;
                }
            }
        }
    }
    false
}

/// Does `<base>/lib` (or `<base>/lib/postgresql`) contain a `vector.{dylib,so}`?
fn lib_dir_has_vector(base: Option<&Path>) -> bool {
    let Some(base) = base else { return false };
    for lib in [base.join("lib"), base.join("lib/postgresql")] {
        if let Ok(entries) = std::fs::read_dir(&lib) {
            for e in entries.flatten() {
                let n = e.file_name();
                let n = n.to_string_lossy();
                if n == "vector.dylib" || n == "vector.so" {
                    return true;
                }
            }
        }
    }
    false
}

fn stage_fixture() -> PathBuf {
    let fixture = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/ai-memory-pg");
    let dir = std::env::temp_dir().join(format!(
        "sky-ai-memory-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    copy_dir(&fixture, &dir);
    dir
}

fn copy_dir(from: &Path, to: &Path) {
    std::fs::create_dir_all(to).unwrap();
    for entry in std::fs::read_dir(from).unwrap() {
        let entry = entry.unwrap();
        let dest = to.join(entry.file_name());
        if entry.file_type().unwrap().is_dir() {
            copy_dir(&entry.path(), &dest);
        } else {
            std::fs::copy(entry.path(), dest).unwrap();
        }
    }
}

// Heavy real-DB e2e leg: starts a real embedded PostgreSQL cluster and needs the
// pgvector extension. Excluded from the per-commit T1 tier (it would blow the
// test-sky latency budget) and run NIGHTLY via `cargo test -p sky -- --ignored`,
// where nightly-sweep.yml installs postgresql-16-pgvector. This is the sanctioned
// placement for `#[ignore]`d heavy postgres legs (rust-ci.yml test-sky notes).
#[test]
#[ignore = "heavy: real embedded postgres + pgvector; runs nightly via --ignored"]
fn memory_recalls_by_vector_and_hybrid() {
    if !required(Need::Go, have_go()) {
        return;
    }
    if !required(Need::Postgres, have_pgvector()) {
        return;
    }
    let dir = stage_fixture();
    let out = Command::new(SKY)
        .args(["run", "src/Main.sky"])
        .current_dir(&dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky run");
    let mut stdout = String::from_utf8_lossy(&out.stdout).into_owned();
    stdout.push_str(&String::from_utf8_lossy(&out.stderr));

    assert!(
        out.status.success(),
        "ai-memory-pg fixture exited non-zero:\n{stdout}"
    );
    assert!(
        stdout.contains("memory search=a hybrid=b"),
        "pure-vector search should recall 'a' and keyword-hybrid should recall 'b'; got:\n{stdout}"
    );

    // Best-effort: stop the per-project cluster the fixture started.
    let _ = Command::new(SKY).args(["db", "stop"]).current_dir(&dir).output();
    let _ = std::fs::remove_dir_all(&dir);
}

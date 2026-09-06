//! Structural anti-omission: EVERY example that ships Sky source must be
//! fresh-BUILT by at least one gate.
//!
//! anzellai/sky#195 — a broken `sky build` of a SPA example — shipped green in
//! v0.23.0 because the example was fresh-built by NO gate:
//!   * `scripts/example-sweep.sh` builds from a hand-maintained `EXAMPLES=(…)`
//!     bash array; the SPA / `Std.App` auto-split examples were simply ABSENT
//!     from it, so the full sweep never built them.
//!   * `xtask build-run`'s `corpus()` discovers examples via `read_dir` FILTERED
//!     to a top-level `src/`. A hand-authored multi-project split — `60-spa-todos`
//!     (`client/`, `server/`) and `39-hub-demo` (`billing-app/`, `frontend-app/`)
//!     — has no top-level `src/`, so it escaped discovery ENTIRELY.
//!
//! The compiler bug itself is fixed. This test closes the CLASS: a new or renamed
//! example can no longer silently escape every fresh-build gate. It fails loudly,
//! naming the un-covered example and exactly how to wire it in — so the omission
//! becomes a deliberate, reviewed decision, never a silent one.
//!
//! It PARSES (does not copy) the `MANUAL_SPLIT` registry from `build_run_gate.rs`
//! and the `EXAMPLES=(…)` array from `example-sweep.sh`, the same "parsed, not
//! copied" contract `gates_measure_a_fresh_compiler.rs` uses for `build.rs`'s
//! `stage()` calls, so this test cannot drift from the gates it checks.

use std::collections::BTreeSet;
use std::path::{Path, PathBuf};
use std::process::Command;

fn repo_root() -> PathBuf {
    Path::new(concat!(env!("CARGO_MANIFEST_DIR"), "/../../.."))
        .canonicalize()
        .expect("repo root resolves")
}

/// Immediate children of `examples/` that ship a buildable Sky project — a dir
/// with at least one git-TRACKED `sky.toml` manifest anywhere inside it. git is
/// the authority on what actually ships: a dir carrying only local build output
/// (`sky-out/`, `data/`, `sky-out-rust/` — e.g. the untracked `58-persist-*` /
/// `59-persist-live` scratch trees) has no tracked files and is correctly NOT an
/// example. Using tracked-`sky.toml` rather than on-disk contents is what keeps a
/// developer's local scratch from either tripping this gate or masking a real gap.
fn tracked_examples(root: &Path) -> BTreeSet<String> {
    let out = Command::new("git")
        .arg("-C")
        .arg(root)
        .args(["ls-files", "examples/"])
        .output()
        .expect("git ls-files examples/");
    assert!(
        out.status.success(),
        "git ls-files failed: {}",
        String::from_utf8_lossy(&out.stderr)
    );
    let mut ex = BTreeSet::new();
    for line in String::from_utf8_lossy(&out.stdout).lines() {
        let Some(rest) = line.strip_prefix("examples/") else {
            continue;
        };
        let Some((name, tail)) = rest.split_once('/') else {
            continue;
        };
        // A buildable Sky project is identified by its `sky.toml` — either at the
        // example root (`<name>/sky.toml`) or in a sub-project of a manual split
        // (`<name>/client/sky.toml`, `<name>/billing-app/sky.toml`, …).
        if tail == "sky.toml" || tail.ends_with("/sky.toml") {
            ex.insert(name.to_string());
        }
    }
    ex
}

/// The build-run gate's automatic discovery predicate: `corpus()` keys on a
/// top-level `src/` directory. An example with one is fresh-built with no further
/// wiring.
fn has_top_level_src(root: &Path, name: &str) -> bool {
    root.join("examples").join(name).join("src").is_dir()
}

/// The example names covered by `build_run_gate.rs`'s `MANUAL_SPLIT` registry —
/// parsed from the source so this test tracks the gate, not a stale copy. The
/// registry's non-name literals (sub-dir names, artifact paths like
/// `.split/backend/sky-out/app`) are collected too but discarded by the
/// intersection with the real example set in the test body.
fn manual_split_names(root: &Path) -> BTreeSet<String> {
    let path = root.join("rust/crates/xtask/src/build_run_gate.rs");
    let src = std::fs::read_to_string(&path).expect("read build_run_gate.rs");
    let start = src
        .find("const MANUAL_SPLIT")
        .expect("MANUAL_SPLIT const present in build_run_gate.rs");
    let block = &src[start..];
    let end = block
        .find("];")
        .expect("MANUAL_SPLIT const terminator `];`");
    let block = &block[..end];
    let mut names = BTreeSet::new();
    let mut rest = block;
    while let Some(i) = rest.find('"') {
        rest = &rest[i + 1..];
        if let Some(j) = rest.find('"') {
            names.insert(rest[..j].to_string());
            rest = &rest[j + 1..];
        } else {
            break;
        }
    }
    names
}

/// The example names in `scripts/example-sweep.sh`'s `EXAMPLES=(…)` array. Each
/// entry is `"NAME:kind[:port[:path]]"`; the name is the part before the first
/// `:`. Bounded to the array region (`EXAMPLES=(` → the closing `)` on its own
/// line) so unrelated quoted strings elsewhere in the script are not miscounted.
fn example_sweep_names(root: &Path) -> BTreeSet<String> {
    let src =
        std::fs::read_to_string(root.join("scripts/example-sweep.sh")).expect("read example-sweep.sh");
    let start = src
        .find("EXAMPLES=(")
        .expect("EXAMPLES=( array in example-sweep.sh");
    let mut names = BTreeSet::new();
    for line in src[start..].lines().skip(1) {
        let l = line.trim();
        if l == ")" {
            break; // end of the array literal
        }
        if let Some(q) = l.strip_prefix('"') {
            let name = q.split([':', '"']).next().unwrap_or("");
            if !name.is_empty() {
                names.insert(name.to_string());
            }
        }
    }
    names
}

/// Examples deliberately NOT fresh-built by build-run / example-sweep / the
/// manual-split registry, each with the reason and where it IS built. This list
/// exists so that such a decision is EXPLICIT and reviewed — an entry here is a
/// signed-off exception, never a silent escape. It MUST stay empty unless a real
/// case appears (format: `(example, reason)`).
const BUILD_ONLY_ELSEWHERE: &[(&str, &str)] = &[];

#[test]
fn every_example_is_covered_by_a_fresh_build_gate() {
    let root = repo_root();
    let examples = tracked_examples(&root);
    let manual = manual_split_names(&root);
    let sweep = example_sweep_names(&root);
    let allow: BTreeSet<&str> = BUILD_ONLY_ELSEWHERE.iter().map(|(n, _)| *n).collect();

    // Anti-vacuity: a parser that silently returned an empty set would make the
    // coverage check below pass over nothing — the exact vacuity class that lets
    // a "green" gate certify a corpus it never inspected. Prove each source
    // parsed real data before trusting it.
    assert!(
        !examples.is_empty(),
        "found no tracked examples — tracked_examples() parser is broken"
    );
    assert!(
        manual.iter().any(|n| examples.contains(n)),
        "MANUAL_SPLIT parser matched no real example — it has drifted from \
         build_run_gate.rs (parsed names: {manual:?})"
    );
    assert!(
        sweep.len() > 5,
        "example-sweep.sh parser found only {} entries — parser is broken",
        sweep.len()
    );

    let uncovered: Vec<String> = examples
        .iter()
        .filter(|name| {
            !(has_top_level_src(&root, name)   // build-run corpus() auto-discovery
                || manual.contains(*name)      // build-run MANUAL_SPLIT registry
                || sweep.contains(*name)       // scripts/example-sweep.sh
                || allow.contains(name.as_str())) // reviewed exception
        })
        .cloned()
        .collect();

    assert!(
        uncovered.is_empty(),
        "these examples ship Sky source but are fresh-built by NO gate — the exact \
         omission that let anzellai/sky#195 ship green in v0.23.0:\n    {}\n\n\
         Wire each into ONE of:\n  \
         * give it a top-level `src/` (build-run `corpus()` discovers it \
         automatically), or\n  \
         * add it to `MANUAL_SPLIT` in rust/crates/xtask/src/build_run_gate.rs \
         (for a hand-authored multi-project split — client/server/sub-apps with \
         no top-level `src/`), or\n  \
         * add it to the `EXAMPLES=(…)` array in scripts/example-sweep.sh, or\n  \
         * (last resort, reviewed) list it in `BUILD_ONLY_ELSEWHERE` in this test \
         with a reason.",
        uncovered.join(", ")
    );
}

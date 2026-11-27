//! Every pass after type inference reads a def's body through ONE accessor,
//! `ty::TyDb::elaborated_body_of` (docs/design/let-bind-lines.md §5). That body
//! has each bind line's placeholder replaced by the `andThen` the check path
//! chose, so no later pass chooses again or reads a placeholder.
//!
//! This scan fails when a crate that runs after inference reads the resolved
//! HIR bodies (`resolve(m).bodies`) in a file, or more often in a file, than
//! the allow-list below says. Each entry names why the read is allowed: a pass
//! that runs before or inside inference (resolution, world construction, the
//! checker itself), the accessor's own implementation, a read that takes only
//! the keys, or test code. A new read goes through the accessor instead.

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};

/// `(file, raw body reads, why it is allowed)`.
const ALLOWED: &[(&str, usize, &str)] = &[
    (
        "ty/src/bind.rs",
        4,
        "the accessor itself, and the check-path inference that makes the choice",
    ),
    (
        "ty/src/db.rs",
        3,
        "the accessor's eager implementation and the inference that elaborates its own body",
    ),
    (
        "ty/src/check.rs",
        4,
        "the checker: it infers the raw body (the bind arm makes the choice) and scans \
         for named callees before inference; its scans after inference read the \
         elaborated body",
    ),
    (
        "ty/src/sig.rs",
        11,
        "world construction (SCC order, harvests, pass 7): it builds the world the \
         choice is made in, so it cannot read the choice; bind lines get conservative \
         candidate edges",
    ),
    (
        "ty/src/obligations.rs",
        2,
        "world construction (page carriers, bound fixpoint): bind lines get the \
         candidate `andThen` edges",
    ),
    (
        "ty/src/retained_body.rs",
        4,
        "world construction targets and their own test",
    ),
    (
        "ty/src/pubsub_topic.rs",
        1,
        "reads the root string literal of a zero-argument def, which a bind line never is",
    ),
    ("ty/src/infer.rs", 2, "test code"),
    ("ty/src/infer_readback_tests.rs", 7, "test code"),
    (
        "skydb/src/lib.rs",
        7,
        "the accessor's salsa implementation, the syntactic call index and the \
         syntactic harvest gate, and infer_query, which elaborates its own body",
    ),
    (
        "lower/src/lower.rs",
        6,
        "DefBody (built from the accessor) and test code",
    ),
    ("lower/src/lower/spa_capability_domain.rs", 1, "test code"),
    ("lower/src/lower/spa_model.rs", 2, "test code"),
    (
        "lower/src/lower/view_memo/ownership.rs",
        2,
        "test code (the pass reads the elaborated DefEntry map)",
    ),
    (
        "lower/src/lower/view_memo/purity.rs",
        4,
        "the elaborated DefEntry map passed in, and test code",
    ),
    ("project/src/harvest_identity_tests.rs", 1, "test code"),
    ("project/src/spa_partition.rs", 1, "the keys only"),
    ("sky-lsp/src/lib.rs", 1, "the keys only"),
];

/// The crates whose code runs after type inference (`ty` included: its passes
/// after inference are held to the same rule, and its others are listed).
const CRATES: &[&str] = &[
    "ty", "lower", "codegen", "project", "skydb", "sky-lsp", "sky", "xtask",
];

fn crates_dir() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .parent()
        .expect("crates dir")
        .to_path_buf()
}

fn rust_files(dir: &Path, out: &mut Vec<PathBuf>) {
    let Ok(rd) = std::fs::read_dir(dir) else {
        return;
    };
    let mut entries: Vec<PathBuf> = rd.filter_map(|e| e.ok()).map(|e| e.path()).collect();
    entries.sort();
    for p in entries {
        if p.is_dir() {
            rust_files(&p, out);
        } else if p.extension().is_some_and(|e| e == "rs") {
            out.push(p);
        }
    }
}

/// Raw body reads in `src`: `.bodies` as a field (not in a comment).
fn reads(src: &str) -> usize {
    src.lines()
        .map(|l| match l.find("//") {
            Some(i) => &l[..i],
            None => l,
        })
        .map(|l| {
            let mut n = 0;
            let mut rest = l;
            while let Some(i) = rest.find(".bodies") {
                let after = &rest[i + ".bodies".len()..];
                if !after.starts_with(|c: char| c.is_alphanumeric() || c == '_') {
                    n += 1;
                }
                rest = after;
            }
            n
        })
        .sum()
}

#[test]
fn passes_after_inference_read_bodies_through_the_accessor() {
    let root = crates_dir();
    let mut found: BTreeMap<String, usize> = BTreeMap::new();
    for c in CRATES {
        let mut files = Vec::new();
        rust_files(&root.join(c).join("src"), &mut files);
        for f in files {
            let src = std::fs::read_to_string(&f).unwrap();
            let n = reads(&src);
            if n > 0 {
                let rel = f
                    .strip_prefix(&root)
                    .unwrap()
                    .to_string_lossy()
                    .replace('\\', "/");
                found.insert(rel, n);
            }
        }
    }
    let allowed: BTreeMap<String, usize> = ALLOWED
        .iter()
        .map(|(f, n, _)| (f.to_string(), *n))
        .collect();
    let mut problems = Vec::new();
    for (f, n) in &found {
        match allowed.get(f) {
            None => problems.push(format!(
                "{f}: {n} raw `.bodies` read(s); read the body with \
                 `ty::TyDb::elaborated_body_of` / `ty::elaborated_bodies` \
                 (docs/design/let-bind-lines.md §5)"
            )),
            Some(a) if n > a => problems.push(format!(
                "{f}: {n} raw `.bodies` reads, the allow-list has {a}; read the new one \
                 through `ty::TyDb::elaborated_body_of`"
            )),
            Some(a) if n < a => problems.push(format!(
                "{f}: {n} raw `.bodies` reads, fewer than the allow-list's {a}: lower the \
                 entry to {n}"
            )),
            _ => {}
        }
    }
    for f in allowed.keys() {
        if !found.contains_key(f) {
            problems.push(format!("{f}: no raw `.bodies` read left: remove its entry"));
        }
    }
    assert!(problems.is_empty(), "{}", problems.join("\n"));
}

/// The scan bites: a raw read in a new file is reported.
#[test]
fn the_scan_counts_a_raw_read() {
    assert_eq!(reads("let b = resolved.bodies.get(&d);"), 1);
    assert_eq!(reads("let b = resolved\n    .bodies\n    .get(&d);"), 1);
    assert_eq!(reads("// resolve(m).bodies in a comment"), 0);
    assert_eq!(reads("self.bodies_seen += 1;"), 0);
}

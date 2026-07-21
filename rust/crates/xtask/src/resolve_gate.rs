//! `xtask resolve` — the M2 name-resolution gate over the `examples/` corpus.
//!
//! For every example it parses + resolves the entry module + its Sky-source dep
//! modules against the stdlib, then classifies every unresolved-name diagnostic:
//!
//!   (a) GENUINE resolver gap — a name that should resolve from Sky source /
//!       stdlib / Prelude / kernel but didn't. A BUG. The gate requires zero.
//!   (b) GO-FFI-PACKAGE reference — a name imported from a Go package (`sky
//!       add`), which needs the FFI surface (doc 09, not built yet). EXPECTED.
//!
//! M2 GATE: zero class-(a) unresolved-name errors across the corpus.

use hir::{ClassA, ClassB, SourceDb};
use std::collections::BTreeSet;
use std::path::{Path, PathBuf};

pub fn run(_args: &[String], root: &Path) -> i32 {
    let stdlib = load_dir(&root.join("sky-stdlib"), "sky-stdlib");
    if stdlib.is_empty() {
        eprintln!(
            "resolve: no stdlib modules found under {}/sky-stdlib",
            root.display()
        );
        return 1;
    }

    let examples_root = root.join("examples");
    let mut example_dirs: Vec<PathBuf> = std::fs::read_dir(&examples_root)
        .map(|rd| {
            rd.filter_map(|e| e.ok().map(|e| e.path()))
                .filter(|p| p.is_dir())
                .collect()
        })
        .unwrap_or_default();
    example_dirs.sort();

    let mut rows: Vec<ExampleRow> = Vec::new();
    let mut all_class_a: Vec<(String, ClassA)> = Vec::new();
    let mut all_class_b: BTreeSet<(String, String, String)> = BTreeSet::new(); // (package, example, qualified-name)

    // ---- stdlib self-resolution (a resolver bug in stdlib is also class-a) ----
    rows.push(resolve_group(
        "stdlib",
        &stdlib,
        &stdlib,
        &mut all_class_a,
        &mut all_class_b,
    ));

    // ---- each example ----
    for dir in &example_dirs {
        let name = dir
            .file_name()
            .and_then(|s| s.to_str())
            .unwrap_or("?")
            .to_string();
        let locals = load_dir(dir, "src");
        if locals.is_empty() {
            continue;
        }
        rows.push(resolve_group(
            &name,
            &locals,
            &stdlib,
            &mut all_class_a,
            &mut all_class_b,
        ));
    }

    print_table(&rows);
    print_class_a(&all_class_a);
    print_class_b(&all_class_b);

    let total_a: usize = rows.iter().map(|r| r.class_a).sum();
    let total_b: usize = rows.iter().map(|r| r.class_b).sum();
    println!("\nTOTALS: class-(a) genuine gaps = {total_a} | class-(b) FFI refs = {total_b}");
    if total_a == 0 {
        println!("M2 GATE: PASS  (zero class-(a) unresolved names across the corpus)");
        0
    } else {
        println!("M2 GATE: FAIL  ({total_a} class-(a) unresolved names — resolver bug)");
        1
    }
}

struct ExampleRow {
    name: String,
    modules: usize,
    class_a: usize,
    class_b: usize,
}

/// Build a per-group db (locals over stdlib) and resolve every local module.
fn resolve_group(
    group: &str,
    locals: &[(String, syntax::Parse)],
    stdlib: &[(String, syntax::Parse)],
    all_a: &mut Vec<(String, ClassA)>,
    all_b: &mut BTreeSet<(String, String, String)>,
) -> ExampleRow {
    let mut db = SourceDb::new();
    // stdlib first, locals second (locals shadow stdlib on name collision).
    for (name, parse) in stdlib {
        db.add_module(name, parse.clone());
    }
    let mut local_ids = Vec::new();
    for (name, parse) in locals {
        local_ids.push(db.add_module(name, parse.clone()));
    }

    let mut a_set: BTreeSet<(Option<String>, String, String)> = BTreeSet::new();
    let mut b_set: BTreeSet<(String, String)> = BTreeSet::new();
    for id in local_ids {
        let r = hir::resolve(&db, id);
        for a in r.class_a {
            a_set.insert((a.qualifier.clone(), a.name.clone(), a.reason.clone()));
            all_a.push((group.to_string(), a));
        }
        for b in r.class_b {
            let qn = qualified(&b);
            b_set.insert((b.package.clone(), qn.clone()));
            all_b.insert((b.package.clone(), group.to_string(), qn));
        }
    }
    ExampleRow {
        name: group.to_string(),
        modules: locals.len(),
        class_a: a_set.len(),
        class_b: b_set.len(),
    }
}

fn qualified(b: &ClassB) -> String {
    match &b.qualifier {
        Some(q) => format!("{q}.{}", b.name),
        None => b.name.clone(),
    }
}

fn print_table(rows: &[ExampleRow]) {
    let w = rows.iter().map(|r| r.name.len()).max().unwrap_or(7).max(7);
    println!(
        "{:<w$}  {:>7}  {:>8}  {:>8}",
        "EXAMPLE",
        "MODULES",
        "CLASS_A",
        "CLASS_B",
        w = w
    );
    println!("{}", "-".repeat(w + 30));
    for r in rows {
        println!(
            "{:<w$}  {:>7}  {:>8}  {:>8}",
            r.name,
            r.modules,
            r.class_a,
            r.class_b,
            w = w
        );
    }
}

fn print_class_a(all: &[(String, ClassA)]) {
    if all.is_empty() {
        println!("\nclass-(a) genuine gaps: NONE");
        return;
    }
    // dedupe (example, qualified-name, reason)
    let mut set: BTreeSet<(String, String, String)> = BTreeSet::new();
    for (ex, a) in all {
        let qn = match &a.qualifier {
            Some(q) => format!("{q}.{}", a.name),
            None => a.name.clone(),
        };
        set.insert((ex.clone(), qn, a.reason.clone()));
    }
    println!("\nclass-(a) GENUINE RESOLVER GAPS ({} unique):", set.len());
    for (ex, qn, reason) in &set {
        println!("  [{ex}] {qn}  — {reason}");
    }
}

fn print_class_b(all: &BTreeSet<(String, String, String)>) {
    if all.is_empty() {
        println!("\nclass-(b) FFI references: NONE");
        return;
    }
    // group by package
    let mut packages: BTreeSet<&String> = BTreeSet::new();
    for (pkg, _, _) in all {
        packages.insert(pkg);
    }
    println!(
        "\nclass-(b) GO-FFI REFERENCES (M3/doc-09 dependency) — {} refs across {} packages:",
        all.len(),
        packages.len()
    );
    for pkg in &packages {
        let examples: BTreeSet<&String> = all
            .iter()
            .filter(|(p, _, _)| &p == pkg)
            .map(|(_, e, _)| e)
            .collect();
        let names: BTreeSet<&String> = all
            .iter()
            .filter(|(p, _, _)| &p == pkg)
            .map(|(_, _, n)| n)
            .collect();
        let ex_list: Vec<&str> = examples.iter().map(|s| s.as_str()).collect();
        let name_list: Vec<&str> = names.iter().take(8).map(|s| s.as_str()).collect();
        let more = if names.len() > 8 {
            format!(" … (+{} more)", names.len() - 8)
        } else {
            String::new()
        };
        println!(
            "  {pkg}  [{}]  {{{}}}{more}",
            ex_list.join(", "),
            name_list.join(", ")
        );
    }
}

// ---- module loading ------------------------------------------------------

/// Parse every `*.sky` under `dir` (skipping generated dirs) into
/// `(module-name, parse)`. `root_marker` is the path segment under which a
/// header-less module's name is derived from its relative path.
fn load_dir(dir: &Path, root_marker: &str) -> Vec<(String, syntax::Parse)> {
    let mut files = Vec::new();
    collect_sky(dir, &mut files);
    let mut out = Vec::new();
    for path in files {
        let Ok(src) = std::fs::read_to_string(&path) else {
            continue;
        };
        let parse = syntax::parse(&src, base::FileId(0));
        let name = module_name(&parse, &path, root_marker);
        out.push((name, parse));
    }
    out
}

fn module_name(parse: &syntax::Parse, path: &Path, root_marker: &str) -> String {
    let tree = parse.tree();
    if let Some(n) = tree
        .module_header()
        .and_then(|h| h.name())
        .map(|n| n.text())
    {
        if !n.is_empty() {
            return n;
        }
    }
    // header-less: derive from the path relative to the last `root_marker` segment.
    let comps: Vec<&str> = path.iter().filter_map(|c| c.to_str()).collect();
    let start = comps
        .iter()
        .rposition(|c| *c == root_marker)
        .map(|i| i + 1)
        .unwrap_or(0);
    let mut segs: Vec<String> = comps[start..].iter().map(|s| s.to_string()).collect();
    if let Some(last) = segs.last_mut() {
        *last = last.trim_end_matches(".sky").to_string();
    }
    segs.join(".")
}

fn is_generated(path: &Path) -> bool {
    path.components().any(|c| {
        matches!(
            c.as_os_str().to_str(),
            Some("sky-out") | Some(".skycache") | Some(".skydeps")
        )
    })
}

fn collect_sky(dir: &Path, out: &mut Vec<PathBuf>) {
    let mut entries: Vec<PathBuf> = match std::fs::read_dir(dir) {
        Ok(rd) => rd.filter_map(|e| e.ok().map(|e| e.path())).collect(),
        Err(_) => return,
    };
    entries.sort();
    for path in entries {
        if is_generated(&path) {
            continue;
        }
        if path.is_dir() {
            collect_sky(&path, out);
        } else if path.extension().and_then(|e| e.to_str()) == Some("sky") {
            out.push(path);
        }
    }
}

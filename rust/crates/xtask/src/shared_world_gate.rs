//! `xtask shared-world` — the differential harness for route C-2
//! (CI/test-architecture v2 §1.3, §11-U1).
//!
//! # What this proves, and why it exists before any speed claim
//!
//! Route C-2 assembles the 87-module stdlib `World` **once per process** and
//! folds each case's own modules into a fork of it, instead of re-deriving the
//! whole world per case. That is only admissible if it changes **nothing**.
//! v2 §11-U1 names this the design's largest technical risk and states the exit
//! criterion explicitly: *"Phase 3's differential harness asserts identical
//! per-item verdicts over the reject + infer corpora. If verdicts diverge, C-2
//! is not viable as specified."*
//!
//! So this harness runs **both paths over every item of both corpora** and
//! compares a fingerprint far stronger than the gate verdict:
//!
//! * `type_errors`, `name_errors`, `exhaustiveness_warnings`
//! * every diagnostic, as `code|severity|message`, sorted
//! * every inferred/declared def type, as `module.name|declared|rendered`, sorted
//!
//! A gate could pass on equal counts while the checker had silently inferred a
//! different type; comparing the rendered type table closes that. The comparison
//! is exact — there is no tolerance and no allowlist.
//!
//! # `--inject-divergence`
//!
//! A differential harness that cannot fail is worth nothing (v2 §4, the mandate's
//! "a gate that cannot fail is worse than no gate"). `--inject-divergence`
//! deliberately corrupts the shared path — it skips the case's body-derived
//! passes — and the harness must report the divergence and exit non-zero. Run it
//! to prove the comparison is live.

use std::collections::BTreeSet;
use std::path::{Path, PathBuf};
use ty::shared::{Fallback, SharedWorld, WorldSource};

/// The comparable fingerprint of one case's check.
#[derive(PartialEq, Eq)]
struct Verdict {
    type_errors: usize,
    name_errors: usize,
    exhaustiveness: usize,
    diagnostics: Vec<String>,
    def_types: Vec<String>,
}

impl Verdict {
    fn of(out: &ty::CheckOutput) -> Verdict {
        let mut diagnostics: Vec<String> = out
            .diagnostics
            .iter()
            .map(|d| {
                format!(
                    "{}|{:?}|{}",
                    d.code.0,
                    d.severity,
                    d.message.replace('\n', " ")
                )
            })
            .collect();
        diagnostics.sort();
        let mut def_types: Vec<String> = out
            .def_types
            .iter()
            .map(|t| format!("{}.{}|{}|{}", t.module, t.name, t.declared, t.ty.render()))
            .collect();
        def_types.sort();
        Verdict {
            type_errors: out.type_errors,
            name_errors: out.name_errors,
            exhaustiveness: out.exhaustiveness_warnings,
            diagnostics,
            def_types,
        }
    }

    /// A human-readable account of the FIRST difference — enough to act on.
    fn explain(&self, other: &Verdict) -> String {
        if self.type_errors != other.type_errors {
            return format!(
                "type_errors {} vs {}",
                self.type_errors, other.type_errors
            );
        }
        if self.name_errors != other.name_errors {
            return format!("name_errors {} vs {}", self.name_errors, other.name_errors);
        }
        if self.exhaustiveness != other.exhaustiveness {
            return format!(
                "exhaustiveness {} vs {}",
                self.exhaustiveness, other.exhaustiveness
            );
        }
        if self.diagnostics != other.diagnostics {
            let a: BTreeSet<&String> = self.diagnostics.iter().collect();
            let b: BTreeSet<&String> = other.diagnostics.iter().collect();
            let only_a: Vec<&&String> = a.difference(&b).take(2).collect();
            let only_b: Vec<&&String> = b.difference(&a).take(2).collect();
            return format!("diagnostics differ: whole-program-only {only_a:?}; shared-only {only_b:?}");
        }
        let a: BTreeSet<&String> = self.def_types.iter().collect();
        let b: BTreeSet<&String> = other.def_types.iter().collect();
        let only_a: Vec<&&String> = a.difference(&b).take(2).collect();
        let only_b: Vec<&&String> = b.difference(&a).take(2).collect();
        format!("def_types differ: whole-program-only {only_a:?}; shared-only {only_b:?}")
    }
}

struct Item {
    corpus: &'static str,
    name: String,
    modules: Vec<(String, syntax::Parse)>,
    to_check: Vec<String>,
}

pub fn run(args: &[String], root: &Path) -> i32 {
    let inject = args.iter().any(|a| a == "--inject-divergence");
    let quiet = args.iter().any(|a| a == "-q" || a == "--quiet");

    let stdlib = crate::reject_gate::load_dir_pub(&root.join("sky-stdlib"), "sky-stdlib");
    if stdlib.is_empty() {
        eprintln!("shared-world: no stdlib modules under {}/sky-stdlib", root.display());
        return 1;
    }

    let mut items = Vec::new();
    items.extend(reject_items(root));
    items.extend(infer_items(root));
    if items.is_empty() {
        eprintln!("shared-world: no corpus items discovered — nothing was compared");
        return 1;
    }

    let shared = SharedWorld::new(&stdlib);

    let mut compared = 0usize;
    let mut diverged: Vec<String> = Vec::new();
    let mut n_shared = 0usize;
    let mut fallbacks: Vec<(String, Fallback)> = Vec::new();

    for item in &items {
        // --- reference path: exactly what the gates do today ---
        let mut db = hir::SourceDb::new();
        for (n, p) in &stdlib {
            db.add_module(n, p.clone());
        }
        let mut ids = Vec::new();
        for (n, p) in &item.modules {
            let id = db.add_module(n, p.clone());
            if item.to_check.iter().any(|t| t == n) {
                ids.push(id);
            }
        }
        let reference = Verdict::of(&ty::check_modules(&db, &ids));

        // --- shared-world path ---
        let case = if inject {
            shared.check_case_injected_divergence(&item.modules, &item.to_check)
        } else {
            shared.check_case(&item.modules, &item.to_check)
        };
        let candidate = Verdict::of(&case.out);

        match case.source {
            WorldSource::Shared => n_shared += 1,
            WorldSource::Rebuilt(r) => fallbacks.push((item.name.clone(), r)),
        }

        compared += 1;
        if reference != candidate {
            diverged.push(format!(
                "{}/{}: {}",
                item.corpus,
                item.name,
                reference.explain(&candidate)
            ));
        }
    }

    if !quiet {
        println!("SHARED-WORLD DIFFERENTIAL — whole-program vs shared-world, per item");
        println!("  corpora            : reject + infer");
        println!("  items compared     : {compared}");
        println!("  shared world used  : {n_shared}");
        println!("  full-rebuild falls : {}", fallbacks.len());
        for (name, r) in &fallbacks {
            println!("      {name}  [{}]", r.label());
        }
        println!("  stdlib base modules: {}", shared.base_module_count());
    }

    if diverged.is_empty() {
        if inject {
            println!(
                "SHARED-WORLD GATE: FAIL  (--inject-divergence corrupted the shared path and \
                 the harness saw NOTHING across {compared} items — the comparison is dead)"
            );
            return 1;
        }
        println!(
            "SHARED-WORLD GATE: PASS  ({compared} items, identical verdicts \
             — counts, diagnostics and inferred type tables)"
        );
        0
    } else {
        println!("---- {} divergence(s) ----", diverged.len());
        for d in diverged.iter().take(30) {
            println!("  {d}");
        }
        if inject {
            println!(
                "SHARED-WORLD GATE: injected divergence DETECTED in {}/{} items \
                 — the comparison is live",
                diverged.len(),
                compared
            );
            return 0;
        }
        println!(
            "SHARED-WORLD GATE: FAIL  ({} of {compared} items diverge — \
             C-2 is not viable as specified (v2 §11-U1))",
            diverged.len()
        );
        1
    }
}

/// The reject corpus: one single-module case per file.
fn reject_items(root: &Path) -> Vec<Item> {
    let dir = root.join("rust/crates/ty/tests/reject/corpus");
    let mut files: Vec<PathBuf> = match std::fs::read_dir(&dir) {
        Ok(rd) => rd
            .filter_map(|e| e.ok().map(|e| e.path()))
            .filter(|p| p.extension().and_then(|e| e.to_str()) == Some("sky"))
            .collect(),
        Err(_) => return Vec::new(),
    };
    files.sort();
    files
        .iter()
        .map(|f| {
            let src = std::fs::read_to_string(f).unwrap_or_default();
            let parse = syntax::parse(&src, base::FileId(0));
            let mname = parse
                .tree()
                .module_header()
                .and_then(|h| h.name())
                .map(|n| n.text())
                .filter(|s| !s.is_empty())
                .unwrap_or_else(|| "Main".to_string());
            Item {
                corpus: "reject",
                name: f
                    .file_name()
                    .and_then(|s| s.to_str())
                    .unwrap_or("?")
                    .to_string(),
                modules: vec![(mname.clone(), parse)],
                to_check: vec![mname],
            }
        })
        .collect()
}

/// The infer corpus: one multi-module case per example `src/` tree.
fn infer_items(root: &Path) -> Vec<Item> {
    let examples = root.join("examples");
    let mut dirs: Vec<PathBuf> = match std::fs::read_dir(&examples) {
        Ok(rd) => rd
            .filter_map(|e| e.ok().map(|e| e.path()))
            .filter(|p| p.is_dir())
            .collect(),
        Err(_) => return Vec::new(),
    };
    dirs.sort();
    let mut out = Vec::new();
    for dir in dirs {
        let name = dir
            .file_name()
            .and_then(|s| s.to_str())
            .unwrap_or("?")
            .to_string();
        let src_dir = dir.join("src");
        let load_root = if src_dir.is_dir() { src_dir } else { dir.clone() };
        let locals = crate::reject_gate::load_dir_pub(&load_root, "src");
        if locals.is_empty() {
            continue;
        }
        let to_check: Vec<String> = locals.iter().map(|(n, _)| n.clone()).collect();
        out.push(Item {
            corpus: "infer",
            name,
            modules: locals,
            to_check,
        });
    }
    out
}

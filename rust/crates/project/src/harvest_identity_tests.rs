//! Differential: on the example projects, the linear #166 call-site harvest
//! (`skydb::callsite_param_records_query`, one inference per caller) returns
//! exactly what the per-callee harvest (`ty::callsite_param_records_for`)
//! returns, for every def that wants a harvest.
//!
//! Two tests share one body:
//!
//! * the per-push test runs over every example project that loads WITHOUT a
//!   fetched dependency. The examples it cannot load are pinned in
//!   [`NEEDS_FETCH`] with the reason, and each must fail to load for exactly
//!   that reason (or load, when the dependency has been fetched locally). A
//!   project that newly fails to load, a stale exclusion, or a drop in the
//!   covered counts fails the test;
//! * the `#[ignore]`d test runs over EVERY example project and requires every
//!   one to load. The release workflow runs it in the `gate-sweep` shard that
//!   sweeps the pinned examples, after the sweep's `sky install` has fetched
//!   their dependencies.

use hir::SkyDb;
use std::path::{Path, PathBuf};
use ty::TyDb;

/// Examples that need a fetched dependency to load, and why. Per-push CI runs
/// no `sky install`, so the per-push test cannot load these. Each must also be
/// in the release sweep's example list (`scripts/example-sweep.sh --list`),
/// whose `sky install` fetches the dependency before the full test runs.
const NEEDS_FETCH: &[(&str, &str)] = &[(
    "13-skyshop",
    "its Sky dependency github.com/anzellai/sky-tailwind is fetched by `sky install`, \
     which per-push CI does not run",
)];

/// The number of example projects the per-push test covers (every project
/// with a `sky.toml` directly under an example, or one level below it, minus
/// [`NEEDS_FETCH`]). A change means an example stopped loading, was added or
/// was removed: account for it here in the same change.
const COVERED_WITHOUT_FETCH: usize = 68;

/// The number of defs that want a harvest across those projects, and how many
/// of them find a closed record. A drop means the differential lost coverage.
const COMPARED_WITHOUT_FETCH: usize = 15;
const WITH_RECORD_WITHOUT_FETCH: usize = 8;

/// The same counts over every example project, the [`NEEDS_FETCH`] ones
/// included (13-skyshop alone holds ten of the harvested defs).
const COMPARED_ALL: usize = 25;
const WITH_RECORD_ALL: usize = 18;

fn repo_root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    while !dir.join("sky-stdlib").is_dir() {
        assert!(dir.pop(), "no sky-stdlib ancestor");
    }
    dir
}

/// Every example project: a directory under `examples/` with a `sky.toml`, or
/// a sub-directory of one example with its own `sky.toml` (a multi-app
/// example). Sorted, as `(name, dir)` with `name` relative to `examples/`.
fn example_projects(root: &Path) -> Vec<(String, PathBuf)> {
    let ex = root.join("examples");
    let mut out = Vec::new();
    let mut tops: Vec<PathBuf> = std::fs::read_dir(&ex)
        .expect("examples dir")
        .filter_map(|e| e.ok().map(|e| e.path()))
        .filter(|p| p.is_dir())
        .collect();
    tops.sort();
    for top in tops {
        let top_name = top.file_name().unwrap().to_string_lossy().to_string();
        if top.join("sky.toml").is_file() {
            out.push((top_name.clone(), top.clone()));
        }
        let mut subs: Vec<PathBuf> = std::fs::read_dir(&top)
            .into_iter()
            .flatten()
            .filter_map(|e| e.ok().map(|e| e.path()))
            .filter(|p| p.is_dir() && p.join("sky.toml").is_file())
            .collect();
        subs.sort();
        for sub in subs {
            let sub_name = sub.file_name().unwrap().to_string_lossy().to_string();
            out.push((format!("{top_name}/{sub_name}"), sub));
        }
    }
    out
}

struct Outcome {
    loaded: Vec<String>,
    unloaded: Vec<(String, String)>,
    compared: usize,
    with_record: usize,
}

/// Load each project and compare the two harvests for every def that wants
/// one. A mismatch panics naming the project and the def.
fn differential(root: &Path, projects: &[(String, PathBuf)]) -> Outcome {
    let mut out = Outcome {
        loaded: Vec::new(),
        unloaded: Vec::new(),
        compared: 0,
        with_record: 0,
    };
    for (name, dir) in projects {
        let (db, _entry, _ids) = match crate::build::load_source_db(root, dir, None) {
            Ok(x) => x,
            Err(e) => {
                out.unloaded.push((name.clone(), e));
                continue;
            }
        };
        out.loaded.push(name.clone());
        let decls = db.type_world();
        let check = db.check_world();
        for m in db.module_ids() {
            let resolved = db.resolve(m);
            for (&def, body) in resolved.bodies.iter() {
                if !ty::wants_callsite_harvest(&decls, def, body) {
                    continue;
                }
                let linear =
                    skydb::callsite_param_records_query(&db, def, m, db.source_file(m)).clone();
                let per_callee = ty::callsite_param_records_for(&db, &check, def);
                assert_eq!(
                    linear,
                    per_callee,
                    "{name}: linear harvest differs for {:?}",
                    db.def_loc(def).map(|l| l.name)
                );
                out.compared += 1;
                if linear.iter().any(|o| o.is_some()) {
                    out.with_record += 1;
                }
            }
        }
    }
    eprintln!(
        "projects loaded {} of {}, harvests compared {}, with a record {}",
        out.loaded.len(),
        projects.len(),
        out.compared,
        out.with_record
    );
    out
}

fn needs_fetch(name: &str) -> bool {
    NEEDS_FETCH.iter().any(|(n, _)| *n == name)
}

#[test]
fn linear_harvest_equals_per_callee_harvest_on_examples_without_fetched_dependencies() {
    let root = repo_root();
    let all = example_projects(&root);
    for (n, why) in NEEDS_FETCH {
        assert!(
            all.iter().any(|(name, _)| name == n),
            "NEEDS_FETCH names `{n}`, which is not an example project: remove it"
        );
        assert!(
            why.len() > 40,
            "NEEDS_FETCH `{n}`: say why it needs a fetch"
        );
    }
    // The pinned examples are covered by the full test in the release sweep,
    // which fetches dependencies only for the examples it sweeps.
    let listed = std::process::Command::new("bash")
        .arg(root.join("scripts/example-sweep.sh"))
        .arg("--list")
        .current_dir(&root)
        .output()
        .expect("run scripts/example-sweep.sh --list");
    assert!(
        listed.status.success(),
        "scripts/example-sweep.sh --list failed: {}",
        String::from_utf8_lossy(&listed.stderr)
    );
    let listed = String::from_utf8_lossy(&listed.stdout).to_string();
    for (n, _) in NEEDS_FETCH {
        assert!(
            listed.lines().any(|l| l.trim() == *n),
            "`{n}` needs a fetched dependency but the release sweep does not list it, so \
             no release job fetches it before the full harvest differential runs"
        );
    }

    let out = differential(&root, &all);
    // An excluded example either fails to load because its dependency is not
    // fetched, or loads because it was fetched locally. Every other example
    // must load.
    let bad: Vec<String> = out
        .unloaded
        .iter()
        .filter(|(name, err)| !(needs_fetch(name) && err.contains("not fetched")))
        .map(|(name, err)| format!("{name}: {err}"))
        .collect();
    assert!(
        bad.is_empty(),
        "example projects that do not load without a fetch, and are not pinned in \
         NEEDS_FETCH for that reason: {bad:?}"
    );
    // Count only what per-push CI covers, so a locally fetched dependency does
    // not move the pin.
    let covered = out.loaded.iter().filter(|n| !needs_fetch(n)).count();
    assert_eq!(
        covered, COVERED_WITHOUT_FETCH,
        "the differential covers {covered} example projects without a fetch, pinned \
         {COVERED_WITHOUT_FETCH}: account for the change in COVERED_WITHOUT_FETCH"
    );
    // The harvest counts are pinned for the per-push set; with a dependency
    // fetched locally the set is larger, so only the lower bound applies.
    assert!(
        out.compared >= COMPARED_WITHOUT_FETCH && out.with_record >= WITH_RECORD_WITHOUT_FETCH,
        "the differential compared {} harvests ({} with a record), pinned at least \
         {COMPARED_WITHOUT_FETCH} ({WITH_RECORD_WITHOUT_FETCH}): coverage dropped",
        out.compared,
        out.with_record
    );
}

/// Every example project, with every dependency fetched. The release workflow
/// runs it (`gate-sweep`, in the shard that sweeps the [`NEEDS_FETCH`]
/// examples) after the sweep's `sky install`. Locally: `sky install` in each
/// [`NEEDS_FETCH`] example, then run with `--ignored`.
#[test]
#[ignore = "needs every example's dependencies fetched (`sky install`); the release \
            workflow's gate-sweep runs it after the sweep has fetched them"]
fn linear_harvest_equals_per_callee_harvest_on_every_example() {
    let root = repo_root();
    let all = example_projects(&root);
    let out = differential(&root, &all);
    assert!(
        out.unloaded.is_empty(),
        "every example project must load (a dependency that is not fetched needs \
         `sky install` in that example): {:?}",
        out.unloaded
    );
    assert!(
        out.compared >= COMPARED_ALL && out.with_record >= WITH_RECORD_ALL,
        "the full differential compared {} harvests ({} with a record), pinned at \
         least {COMPARED_ALL} ({WITH_RECORD_ALL}): coverage dropped",
        out.compared,
        out.with_record
    );
}

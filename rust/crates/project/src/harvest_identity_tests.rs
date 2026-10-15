//! Differential: on every example project, the linear #166 call-site harvest
//! (`skydb::callsite_param_records_query`, one inference per caller) returns
//! exactly what the per-callee harvest (`ty::callsite_param_records_for`)
//! returns, for every def that wants a harvest.

use hir::SkyDb;
use std::path::PathBuf;
use ty::TyDb;

fn repo_root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    while !dir.join("sky-stdlib").is_dir() {
        assert!(dir.pop(), "no sky-stdlib ancestor");
    }
    dir
}

#[test]
fn linear_harvest_equals_per_callee_harvest_on_every_example() {
    let root = repo_root();
    let mut dirs: Vec<PathBuf> = std::fs::read_dir(root.join("examples"))
        .expect("examples dir")
        .filter_map(|e| e.ok().map(|e| e.path()))
        .filter(|p| p.join("sky.toml").is_file())
        .collect();
    dirs.sort();
    let mut loaded = 0usize;
    let mut unloaded = Vec::new();
    let mut compared = 0usize;
    let mut with_record = 0usize;
    for dir in &dirs {
        let (db, _entry, _ids) = match crate::build::load_source_db(&root, dir, None) {
            Ok(x) => x,
            Err(e) => {
                unloaded.push(format!("{}: {e}", dir.display()));
                continue;
            }
        };
        loaded += 1;
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
                    "{}: linear harvest differs for {:?}",
                    dir.display(),
                    db.def_loc(def).map(|l| l.name)
                );
                compared += 1;
                if linear.iter().any(|o| o.is_some()) {
                    with_record += 1;
                }
            }
        }
    }
    eprintln!(
        "examples loaded {loaded} of {}, harvests compared {compared}, with a record {with_record}",
        dirs.len()
    );
    // An example whose Sky dependency is not fetched is a missing
    // prerequisite (`sky install` in that example), not a pass: it fails the
    // test unless the run says out loud that live prerequisites are skipped.
    let skip = match std::env::var("SKY_LIVE_TESTS") {
        Err(_) => false,
        Ok(v) if v == "skip" => true,
        Ok(_) => panic!("SKY_LIVE_TESTS must be unset or `skip`"),
    };
    let mut failures = Vec::new();
    for u in &unloaded {
        if skip && u.contains("not fetched") {
            eprintln!("  UNMEASURED (SKY_LIVE_TESTS=skip): {u}");
        } else {
            failures.push(u.clone());
        }
    }
    assert!(
        failures.is_empty(),
        "every example must load for the differential (a dependency that is not \
         fetched needs `sky install` in that example): {failures:?}"
    );
    assert!(compared > 0, "no example has a harvested def");
}

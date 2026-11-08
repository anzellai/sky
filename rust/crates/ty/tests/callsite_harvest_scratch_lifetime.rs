//! Selected readbacks must remain valid after solved inference scratch drops.
use hir::{SkyDb, SourceDb};

#[test]
fn harvested_records_preserve_repetition_conflicts_and_per_caller_equivalence() {
    let mut db = SourceDb::new();
    let source = "module Main exposing (..)\nsame value = value\nmixed value = value\nmain = { first = same { a = 1 }, second = same { a = 2 }, third = mixed { a = 1 }, fourth = mixed { b = \"other\" }, fifth = mixed { a = 3 } }\n";
    let parsed = syntax::parse(source, base::FileId(0));
    assert!(parsed.errors().is_empty() && parsed.error_node_count() == 0);
    let module = db.add_module("Main", parsed);
    let resolved = db.resolve(module);
    assert!(resolved.class_a.is_empty());
    let world = ty::World::build(&db);
    let id = |name: &str| {
        resolved
            .top_defs
            .iter()
            .find(|d| d.name.as_str() == name)
            .unwrap()
            .def
    };
    let same = id("same");
    let mixed = id("mixed");
    assert!(
        matches!(&world.callsite_param_records[&same][0], Some(ty::Ty::Record(fields, None)) if fields.len() == 1 && fields.iter().any(|(name, _)| name.as_str() == "a"))
    );
    assert_eq!(
        world.callsite_param_records[&mixed],
        vec![None],
        "a later matching record must not erase a prior conflict"
    );
    assert!(world.callsite_param_conflicts.contains(&(mixed, 0)));
    for callee in [same, mixed] {
        let per_callee = ty::callsite_param_records_for(&db, &world, callee);
        assert_eq!(world.callsite_param_records[&callee], per_callee);
        let summaries: Vec<_> = resolved
            .bodies
            .values()
            .map(|body| ty::caller_param_records(&db, &world, body, |def| def == callee))
            .collect();
        let folded = ty::fold_param_records(summaries.iter().filter_map(|s| {
            s.iter()
                .find(|(def, _)| *def == callee)
                .map(|(_, slots)| slots.as_slice())
        }));
        assert_eq!(folded, per_callee);
    }
}

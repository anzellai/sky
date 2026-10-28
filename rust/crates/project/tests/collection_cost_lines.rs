//! Every exported function of the five collection modules states its cost.
//!
//! v0.29.0 made `::` amortised O(1), the String walkers O(k) and added a
//! persistent `Array`; it also stated what stays O(n) (a `Dict` write, a head
//! replacement). A cost a user cannot see is a cost they find in production,
//! so each exported function of `Sky.Core.List`, `Dict`, `Set`, `Array` and
//! `String` carries a `-- Cost:` line in its doc comment, which `sky doc`
//! shows. This test fails when an exported function has none: a new function
//! cannot land without its cost.

use std::path::PathBuf;

const MODULES: &[&str] = &["List", "Dict", "Set", "Array", "String"];

fn stdlib_file(m: &str) -> PathBuf {
    PathBuf::from(concat!(
        env!("CARGO_MANIFEST_DIR"),
        "/../../../sky-stdlib/Sky/Core"
    ))
    .join(format!("{m}.sky"))
}

/// The lower-case names in the module's `exposing ( … )` list.
fn exported_functions(src: &str) -> Vec<String> {
    let start = src.find("exposing").expect("a module header");
    let open = start + src[start..].find('(').expect("exposing (");
    let close = open
        + src[open..]
            .find("\n    )")
            .expect("the closing paren of exposing");
    src[open + 1..close]
        .split([',', '\n'])
        .map(|s| s.trim())
        .filter(|s| !s.is_empty() && !s.starts_with("--"))
        .filter(|s| s.chars().next().is_some_and(|c| c.is_ascii_lowercase()))
        .map(str::to_string)
        .collect()
}

/// The doc-comment lines directly above `name :` (contiguous `--` lines).
fn doc_above(src: &str, name: &str) -> Option<Vec<String>> {
    let lines: Vec<&str> = src.lines().collect();
    // The signature line, or for an unannotated def its `name args =` line.
    let at = lines
        .iter()
        .position(|l| l.starts_with(&format!("{name} :")))
        .or_else(|| {
            lines.iter().position(|l| {
                l.starts_with(&format!("{name} ")) && l.trim_end().ends_with('=')
            })
        })?;
    let mut doc = Vec::new();
    let mut i = at;
    while i > 0 && lines[i - 1].starts_with("--") {
        doc.push(lines[i - 1].to_string());
        i -= 1;
    }
    Some(doc)
}

#[test]
fn every_exported_collection_function_states_its_cost() {
    let mut missing = Vec::new();
    let mut counted = 0;
    for m in MODULES {
        let path = stdlib_file(m);
        let src = std::fs::read_to_string(&path)
            .unwrap_or_else(|e| panic!("read {}: {e}", path.display()));
        let names = exported_functions(&src);
        assert!(names.len() >= 5, "{m}: parsed only {names:?}");
        for n in names {
            counted += 1;
            match doc_above(&src, &n) {
                None => missing.push(format!("Sky.Core.{m}.{n}: no signature or definition line")),
                Some(doc) if !doc.iter().any(|l| l.contains("Cost:")) => {
                    missing.push(format!("Sky.Core.{m}.{n}"))
                }
                _ => {}
            }
        }
    }
    assert!(counted > 100, "only {counted} exported functions found");
    assert!(
        missing.is_empty(),
        "{} exported collection function(s) have no `-- Cost:` line in their doc \
         comment (state it, e.g. `-- Cost: O(n).`):\n  {}",
        missing.len(),
        missing.join("\n  ")
    );
}

#[test]
fn the_parser_sees_a_missing_cost_line() {
    let src = "module X exposing\n    ( a\n    , b\n    )\n\n-- | A.\n--\n-- Cost: O(1).\na : Int\na =\n    1\n\n\n-- | B.\nb : Int\nb =\n    2\n";
    assert_eq!(exported_functions(src), vec!["a", "b"]);
    assert!(doc_above(src, "a")
        .unwrap()
        .iter()
        .any(|l| l.contains("Cost:")));
    assert!(!doc_above(src, "b")
        .unwrap()
        .iter()
        .any(|l| l.contains("Cost:")));
}

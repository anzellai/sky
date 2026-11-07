//! Immutable record field catalog; payloads remain authoritative typed data.
use super::retained_types::RetainedTy;
use super::*;
#[cfg(test)]
use std::sync::Arc;

pub(super) type RecordFields<'a> = HashMap<String, Vec<(String, RetainedTy<'a>)>>;

pub(super) fn build(decls: &[TypeDecl]) -> RecordFields<'_> {
    let mut fields = HashMap::new();
    for d in decls {
        if let TypeDeclKind::Record(fs) = &d.kind {
            fields.insert(
                d.go_name.clone(),
                fs.iter()
                    .map(|(n, t)| (capitalize(n), RetainedTy::Borrowed(t)))
                    .collect(),
            );
        }
    }
    fields
}

/// Exact original catalog construction, retained only as a test reference.
#[cfg(test)]
pub(super) fn owned_reference(decls: &[TypeDecl]) -> RecordFields<'_> {
    let mut fields = HashMap::new();
    for d in decls {
        if let TypeDeclKind::Record(fs) = &d.kind {
            fields.insert(
                d.go_name.clone(),
                fs.iter()
                    .map(|(n, t)| (capitalize(n), RetainedTy::Shared(Arc::new(t.clone()))))
                    .collect(),
            );
        }
    }
    fields
}

#[cfg(test)]
#[derive(Debug)]
pub(super) struct Snapshot {
    entries: Vec<Entry>,
}
#[cfg(test)]
#[derive(Debug)]
struct Entry {
    go_name: String,
    fields: Vec<(String, Ty)>,
    roots_identical: Vec<bool>,
    leaves_identical: Vec<bool>,
}
#[cfg(test)]
fn leaf(ty: &Ty) -> &Ty {
    match ty {
        Ty::Record(fs, _) if !fs.is_empty() => leaf(&fs[0].1),
        Ty::Fun(a, _) => leaf(a),
        Ty::Tuple(xs) | Ty::App(_, xs) if !xs.is_empty() => leaf(&xs[0]),
        _ => ty,
    }
}

/// Inspect the catalog used by the real lower invocation, while declaration
/// owners and stored payloads are both alive. The original builder is a value
/// oracle; identities are checked only after all names/types/order match.
#[cfg(test)]
pub(super) fn inspect(decls: &[TypeDecl], env: &TypeEnv, fields: &RecordFields<'_>) -> Snapshot {
    let original = owned_reference(decls);
    assert_eq!(fields.len(), original.len());
    for (name, expected) in &original {
        let actual = &fields[name];
        assert_eq!(actual.len(), expected.len(), "field count {name}");
        for ((an, at), (en, et)) in actual.iter().zip(expected) {
            assert_eq!(an, en, "capitalized name/order {name}");
            assert_eq!(&**at, &**et, "complete declared payload {name}.{an}");
        }
    }
    let mut entries = Vec::new();
    for d in decls {
        let TypeDeclKind::Record(fs) = &d.kind else {
            continue;
        };
        assert_eq!(
            env.record_templates.get(&d.go_name),
            Some(fs),
            "authoritative environment templates unchanged"
        );
        let actual = &fields[&d.go_name];
        assert_eq!(actual.len(), fs.len());
        entries.push(Entry {
            go_name: d.go_name.clone(),
            fields: actual
                .iter()
                .map(|(n, t)| (n.clone(), (**t).clone()))
                .collect(),
            roots_identical: actual
                .iter()
                .zip(fs)
                .map(|((_, a), (_, b))| std::ptr::eq(&**a, b))
                .collect(),
            leaves_identical: actual
                .iter()
                .zip(fs)
                .map(|((_, a), (_, b))| std::ptr::eq(leaf(a), leaf(b)))
                .collect(),
        });
    }
    Snapshot { entries }
}

#[cfg(test)]
mod tests {
    use super::*;
    use base::FileId;
    use hir::SourceDb;
    const WIDTH: usize = 96;

    fn source() -> String {
        let fields = (0..WIDTH)
            .map(|i| format!("field{i} : {{ entries : List (Int, String), active : Bool }}"))
            .collect::<Vec<_>>()
            .join(", ");
        let values = (0..WIDTH)
            .map(|i| format!("field{i} = {{ entries = [ ({i}, \"item\") ], active = True }}"))
            .collect::<Vec<_>>()
            .join(", ");
        format!(
            r#"module Main exposing (..)

type alias Model = {{ {fields} }}
blank : Model
blank = {{ {values} }}

type alias Tiny a = {{ value : a, label : String }}
type alias Other = {{ value : String, label : String }}
type alias FunBox a = {{ apply : a -> a, label : String }}

makeTiny : Int -> Tiny Int
makeTiny n = Tiny n "ctor"
readTiny : Tiny Int -> Int
readTiny box = box.value
updateTiny : Tiny Int -> Tiny Int
updateTiny box = {{ box | value = 9 }}
patternTiny : Tiny Int -> String
patternTiny {{ label }} = label
literalTiny : Tiny Int
literalTiny = {{ value = 4, label = "literal" }}
other : Other
other = {{ value = "text", label = "other" }}
funBox : FunBox Int
funBox = {{ apply = \x -> x + 1, label = "function" }}
main = {{ model = blank, made = makeTiny 2, read = readTiny literalTiny, changed = updateTiny literalTiny, label = patternTiny literalTiny, collision = other.value, callback = funBox.apply 3 }}
"#
        )
    }

    #[test]
    fn real_wide_nested_catalog_borrows_declared_payloads_with_complete_output_parity() {
        let src = source();
        let parsed = syntax::parse(&src, FileId(0));
        assert!(
            parsed.errors().is_empty() && parsed.error_node_count() == 0,
            "catalog source must parse"
        );
        let mut db = SourceDb::new();
        let module = db.add_module("Main", parsed);
        let checked = ty::check_modules(&db, &[module]);
        assert_eq!(
            (checked.type_errors, checked.name_errors),
            (0, 0),
            "catalog source must really typecheck: {:?}",
            checked.diagnostics
        );
        let mut snapshot = None;
        let mut observer = |s| snapshot = Some(s);
        let actual = super::super::lower_program_impl(
            &db,
            module,
            &LowerConfig::default(),
            None,
            Some(&mut observer),
            false,
        );
        assert!(
            actual.entry_ok && actual.errors.is_empty(),
            "actual catalog really lowers: {:?}",
            actual.errors
        );
        let reference = super::super::lower_program_impl(
            &db,
            module,
            &LowerConfig::default(),
            None,
            None,
            true,
        );
        assert!(
            reference.entry_ok && reference.errors.is_empty(),
            "original catalog really lowers: {:?}",
            reference.errors
        );
        assert_eq!(
            actual.items, reference.items,
            "complete GoIR output must match original owned catalog"
        );
        assert_eq!(actual.warnings, reference.warnings);
        assert_eq!(actual.errors, reference.errors);
        assert_eq!(actual.ffi_used, reference.ffi_used);
        assert_eq!(actual.console_needed, reference.console_needed);
        let snapshot = snapshot.expect("actual catalog construction inspected");
        let model = snapshot
            .entries
            .iter()
            .find(|e| e.go_name == "Main_Model_R")
            .expect("real named Model");
        assert_eq!(model.fields.len(), WIDTH, "nonempty wide premise");
        assert!(
            model
                .fields
                .iter()
                .all(|(_, t)| matches!(t,Ty::Record(inner,None) if inner.len()==2)),
            "every wide payload is a real nested record"
        );
        let tiny = snapshot
            .entries
            .iter()
            .find(|e| e.go_name == "Main_Tiny_R")
            .expect("generic Tiny");
        let other = snapshot
            .entries
            .iter()
            .find(|e| e.go_name == "Main_Other_R")
            .expect("same-field-name Other");
        assert_eq!(
            tiny.fields.iter().map(|(n, _)| n).collect::<Vec<_>>(),
            other.fields.iter().map(|(n, _)| n).collect::<Vec<_>>(),
            "name-collision premise"
        );
        assert_ne!(
            tiny.fields, other.fields,
            "same names must retain different typed templates"
        );
        assert!(
            model.roots_identical.iter().all(|b| *b),
            "catalog cloned authoritative field payload roots"
        );
        assert!(
            model.leaves_identical.iter().all(|b| *b),
            "catalog cloned nested payload subtrees"
        );
        assert!(
            snapshot
                .entries
                .iter()
                .all(|e| e.roots_identical.iter().all(|b| *b)),
            "every catalog field must borrow its declared Ty"
        );
    }
}

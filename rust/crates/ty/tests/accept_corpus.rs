//! The accept corpus: checked-in programs the checker MUST accept, the
//! complement of `tests/reject/corpus/`. Each file carries its own `-- ffi:`
//! directives (the same mechanism the reject corpus uses for a pinned Go-FFI
//! surface), so a rule that starts over-rejecting correct FFI code goes red
//! here, while the reject corpus pins the programs it must refuse.
//!
//! Every file is evaluated by `ty::reject_corpus::evaluate_modules`, the one
//! criterion both corpora share. The count is exact: adding or removing a
//! fixture updates [`EXPECTED_ACCEPT_FILES`] in the same commit.

use std::path::PathBuf;
use ty::reject_corpus::{evaluate_modules, load_stdlib};

/// Files in `tests/accept/`. **1 since v0.27.0 judge round 2**
/// (`ffi_result_handled.sky`, the accept side of
/// `ffi_result_used_as_payload.sky`).
const EXPECTED_ACCEPT_FILES: usize = 1;

fn root() -> PathBuf {
    let mut dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    while !dir.join("sky-stdlib").is_dir() {
        assert!(dir.pop(), "no sky-stdlib ancestor");
    }
    dir
}

#[test]
fn accept_corpus_is_accepted() {
    let stdlib = load_stdlib(&root());
    let dir = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/accept");
    let mut files: Vec<PathBuf> = std::fs::read_dir(&dir)
        .unwrap()
        .filter_map(|e| e.ok().map(|e| e.path()))
        .filter(|p| p.extension().is_some_and(|x| x == "sky"))
        .collect();
    files.sort();
    assert_eq!(
        files.len(),
        EXPECTED_ACCEPT_FILES,
        "accept corpus size changed: update EXPECTED_ACCEPT_FILES in the same commit"
    );
    for f in &files {
        let src = std::fs::read_to_string(f).unwrap();
        let name = f.file_name().unwrap().to_string_lossy().to_string();
        assert!(
            src.lines().next() == Some("-- gate: accept"),
            "{name}: the first line must be `-- gate: accept`"
        );
        if name.starts_with("ffi_") {
            // An FFI fixture that pins no binding would check nothing FFI.
            assert!(
                !ty::ffi_sig::surface_from_directives(&src).is_empty(),
                "{name}: an ffi_ fixture must pin its bindings with `-- ffi:` lines"
            );
        }
        let v = evaluate_modules(&name, &[(String::new(), src)], &stdlib);
        assert!(
            !v.rejected(),
            "{name} must be accepted; observed {:?}: {}",
            v.observed_codes,
            v.first_msg
        );
    }
}

/// The fixture is not vacuous: without its `-- ffi:` lines the same program's
/// Go calls have no pinned signature, and with one pinned argument type
/// changed the same calls no longer type-check.
#[test]
fn the_ffi_fixture_depends_on_its_pinned_signatures() {
    let stdlib = load_stdlib(&root());
    let src = std::fs::read_to_string(
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/accept/ffi_result_handled.sky"),
    )
    .unwrap();
    let bare = src.replace(
        "-- ffi: Pkg encodedLen : Int -> Result Error Int",
        "-- ffi: Pkg encodedLen : String -> Result Error Int",
    );
    assert_ne!(src, bare);
    let v = evaluate_modules("bare", &[(String::new(), bare)], &stdlib);
    assert!(
        v.rejected() && v.observed_codes.contains(&"E2001".to_string()),
        "a changed pinned signature must change the verdict: {:?}",
        v.observed_codes
    );
}

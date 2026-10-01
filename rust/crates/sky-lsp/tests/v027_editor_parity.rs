//! The editor agrees with `sky check` on the v0.27.0 surface.
//!
//! Each test pins one place where the LSP showed less than the CLI, or
//! something the CLI did not: a migration hint that never reached the editor,
//! a hover that printed `any` or dropped a `comparable` bound, a cascade
//! `[E2007]` under an `[E1012]`, a go-to-definition on a Go binding that
//! returned nothing, and a server that did not exit on `exit`. It also covers
//! the v0.27.0 diagnostics no other crate test drives through the editor
//! (`[E2012]`, `[E2013]`, `[E1016]` and the comparable bound).
//!
//! The FFI fixture is the real format-3 surface `sky install` generates for
//! the Go standard library package `encoding/hex` (`tests/fixtures/hex/`).

use sky_lsp::Analysis;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU32, Ordering};
use tower_lsp::lsp_types::{Diagnostic, HoverContents, NumberOrString, Position, Url};

fn repo_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .ancestors()
        .find(|p| p.join("sky-stdlib").is_dir())
        .expect("sky-stdlib not found above the crate")
        .to_path_buf()
}

static COUNTER: AtomicU32 = AtomicU32::new(0);

/// A fresh project in a unique temp dir with the `encoding/hex` surface and
/// `src/Main.sky` holding `main_src`.
fn project(main_src: &str) -> PathBuf {
    std::env::set_var("SKY_STDLIB_DIR", repo_root().join("sky-stdlib"));
    let n = COUNTER.fetch_add(1, Ordering::SeqCst);
    let root = std::env::temp_dir().join(format!("sky-lsp-v027-{}-{n}", std::process::id()));
    let _ = std::fs::remove_dir_all(&root);
    std::fs::create_dir_all(root.join("src")).unwrap();
    std::fs::create_dir_all(root.join("sky-ffi/go")).unwrap();
    std::fs::write(
        root.join("sky.toml"),
        "name = \"v027\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n\
         [\"go.dependencies\"]\n\"encoding/hex\" = \"latest\"\n",
    )
    .unwrap();
    let fx = Path::new(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/hex");
    for (from, to) in [
        ("hex.kernel.json", "sky-ffi/hex.kernel.json"),
        ("hex.skyi", "sky-ffi/hex.skyi"),
        ("hex_bindings.go", "sky-ffi/go/hex_bindings.go"),
    ] {
        std::fs::copy(fx.join(from), root.join(to)).unwrap();
    }
    std::fs::write(root.join("src/Main.sky"), main_src).unwrap();
    root
}

fn main_url(root: &Path) -> Url {
    Url::from_file_path(root.join("src/Main.sky")).unwrap()
}

fn analysis(root: &Path, main_src: &str) -> Analysis {
    let mut a = Analysis::new();
    a.ensure_project_for(&root.join("src/Main.sky"));
    a.set_document(main_url(root), main_src.to_string());
    a
}

fn code_of(d: &Diagnostic) -> String {
    match &d.code {
        Some(NumberOrString::String(s)) => s.clone(),
        Some(NumberOrString::Number(n)) => n.to_string(),
        None => String::new(),
    }
}

/// The 0-based position of byte `needle_start + plus` in `text`.
fn pos_in(text: &str, needle: &str, plus: usize) -> Position {
    let byte = text.find(needle).expect("needle") + plus;
    let before = &text[..byte];
    let line = before.matches('\n').count() as u32;
    let col = before.rsplit('\n').next().unwrap().encode_utf16().count() as u32;
    Position {
        line,
        character: col,
    }
}

fn hover_text(a: &Analysis, url: &Url, pos: Position) -> String {
    let h = a.hover(url, pos).expect("a hover");
    match h.contents {
        HoverContents::Markup(m) => m.value,
        other => format!("{other:?}"),
    }
}

// ---- B5: the migration hint reaches the editor ----------------------------

const FFI_RESULT_MISUSE: &str = "module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.String as String
import Encoding.Hex as Hex


n : Int
n =
    String.length (Hex.invalidByteErrorError 3)


main =
    ()
";

#[test]
fn ffi_result_misuse_carries_its_migration_hint_to_the_editor() {
    let root = project(FFI_RESULT_MISUSE);
    let a = analysis(&root, FFI_RESULT_MISUSE);
    let diags = a.diagnostics(&main_url(&root));
    let d = diags
        .iter()
        .find(|d| code_of(d) == "E2001")
        .unwrap_or_else(|| panic!("an [E2001] for the bare FFI Result: {diags:#?}"));
    assert!(
        d.message
            .contains("see docs/migration/v0.27.md#ffi-result-enforced"),
        "the v0.27.0 fix hint `sky check` prints must reach the editor too: {:?}",
        d.message
    );
}

// ---- B1: hover on an `any` annotation shows the filled type ---------------

#[test]
fn hover_on_an_any_annotation_shows_the_type_the_body_fills_in() {
    let src = "module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.String as String


toLabel : Int -> any
toLabel n =
    String.fromInt n


main =
    String.length (toLabel 3)
";
    let root = project(src);
    let a = analysis(&root, src);
    let url = main_url(&root);
    for (label, pos) in [
        ("use", pos_in(src, "toLabel 3", 0)),
        ("declaration", pos_in(src, "toLabel n =", 0)),
    ] {
        let h = hover_text(&a, &url, pos);
        assert!(
            h.contains("toLabel : Int -> String") && !h.contains("any"),
            "the {label} hover must show the type the checker filled in for `any`: {h}"
        );
    }
}

// ---- B2: hover keeps the comparable bound of an inferred signature --------

#[test]
fn hover_on_an_inferred_signature_keeps_the_comparable_bound() {
    let src = "module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.List as List


sortedNames xs =
    List.sort xs


main =
    sortedNames [ 3, 1 ]
";
    let root = project(src);
    let a = analysis(&root, src);
    let h = hover_text(&a, &main_url(&root), pos_in(src, "sortedNames xs", 0));
    assert!(
        h.contains("sortedNames : List comparable -> List comparable"),
        "an inferred bounded variable must show its bound: {h}"
    );
}

// ---- B3: no cascade under an ambiguous name -------------------------------

#[test]
fn an_ambiguous_name_publishes_only_e1012() {
    let src = "module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Html.Attributes exposing (..)
import Sky.Core.Json.Decode exposing (..)


v =
    value \"x\"


main =
    ()
";
    let root = project(src);
    let a = analysis(&root, src);
    let diags = a.diagnostics(&main_url(&root));
    let codes: Vec<String> = diags.iter().map(code_of).collect();
    assert_eq!(
        codes,
        vec!["E1012".to_string()],
        "`sky check` reports only the ambiguity; the editor must agree: {diags:#?}"
    );
}

// ---- B4: go to definition on a Go binding ---------------------------------

#[test]
fn goto_definition_on_a_go_binding_opens_its_surface_line() {
    let src = "module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Encoding.Hex as Hex


n =
    Hex.encodedLen 4


main =
    ()
";
    let root = project(src);
    let a = analysis(&root, src);
    let loc = a
        .goto(&main_url(&root), pos_in(src, "encodedLen", 2))
        .expect("go to definition on a Go binding must land somewhere");
    let path = loc.uri.to_file_path().unwrap();
    assert_eq!(
        path.canonicalize().unwrap(),
        root.join("sky-ffi/hex.skyi").canonicalize().unwrap(),
        "the binding's catalogue is the generated `.skyi`"
    );
    let skyi = std::fs::read_to_string(&path).unwrap();
    let line = skyi.lines().nth(loc.range.start.line as usize).unwrap();
    assert!(
        line.contains("EncodedLen : Int -> Result Error Int"),
        "the location must be the binding's own line: {line:?}"
    );
    // Without a `.skyi`, the pinned `kernel.json` entry is the definition.
    std::fs::remove_file(root.join("sky-ffi/hex.skyi")).unwrap();
    let a = analysis(&root, src);
    let loc = a
        .goto(&main_url(&root), pos_in(src, "encodedLen", 2))
        .expect("the kernel.json entry when the .skyi is absent");
    let path = loc.uri.to_file_path().unwrap();
    let kj = std::fs::read_to_string(&path).unwrap();
    assert!(path.ends_with("sky-ffi/hex.kernel.json"), "{path:?}");
    assert!(
        kj.lines()
            .nth(loc.range.start.line as usize)
            .unwrap()
            .contains("\"name\": \"encodedLen\""),
        "the location must be the binding's own entry"
    );
}

// ---- coverage: the v0.27.0 codes through the editor -----------------------

fn sibling_diags(root: &Path, file: &str, src: &str) -> Vec<Diagnostic> {
    let mut a = analysis(root, "module Main exposing (main)\n\n\nmain =\n    ()\n");
    let url = Url::from_file_path(root.join("src").join(file)).unwrap();
    a.set_document(url.clone(), src.to_string());
    a.diagnostics(&url)
}

fn one_with(diags: &[Diagnostic], code: &str, needles: &[&str]) {
    let hits: Vec<&Diagnostic> = diags.iter().filter(|d| code_of(d) == code).collect();
    assert_eq!(hits.len(), 1, "exactly one [{code}]: {diags:#?}");
    for n in needles {
        assert!(
            hits[0].message.contains(n),
            "[{code}] must say {n:?}: {:?}",
            hits[0].message
        );
    }
}

#[test]
fn value_restriction_is_published_as_e2012() {
    let root = project("module Main exposing (main)\n\n\nmain =\n    ()\n");
    let src = "module ValueRestr exposing (..)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Std.Sync as Sync


cell =
    Task.run (Sync.newRef [])


use =
    case cell of
        Ok r ->
            Task.run (Sync.set [ 1 ] r)

        Err e ->
            Err e
";
    let diags = sibling_diags(&root, "ValueRestr.sky", src);
    one_with(
        &diags,
        "E2012",
        &["cell", "docs/migration/v0.27.md#value-restriction"],
    );
}

#[test]
fn a_sky_value_for_a_go_interface_is_published_as_e2013() {
    let root = project("module Main exposing (main)\n\n\nmain =\n    ()\n");
    let src = "module GoIface exposing (..)

import Sky.Core.Prelude exposing (..)
import Encoding.Hex as Hex


bad =
    Hex.newEncoder \"x\"
";
    let diags = sibling_diags(&root, "GoIface.sky", src);
    one_with(&diags, "E2013", &["see docs/migration/v0.27.md#"]);
    let d = diags.iter().find(|d| code_of(d) == "E2013").unwrap();
    assert_eq!(
        d.range.start,
        pos_in(src, "\"x\"", 0),
        "the [E2013] sits on the offending argument"
    );
}

#[test]
fn a_recursive_alias_is_published_as_e1016() {
    let root = project("module Main exposing (main)\n\n\nmain =\n    ()\n");
    let src = "module RecAlias exposing (..)

import Sky.Core.Prelude exposing (..)


type alias Node =
    { next : Maybe Node }
";
    let diags = sibling_diags(&root, "RecAlias.sky", src);
    one_with(&diags, "E1016", &["Node"]);
}

#[test]
fn ordering_a_union_that_holds_a_function_names_the_comparable_bound() {
    let root = project("module Main exposing (main)\n\n\nmain =\n    ()\n");
    let src = "module CmpFn exposing (..)

import Sky.Core.Prelude exposing (..)
import Sky.Core.List as List


type Op
    = Op (Int -> Int)


s =
    List.sort [ Op identity ]
";
    let diags = sibling_diags(&root, "CmpFn.sky", src);
    one_with(&diags, "E2001", &["Op", "comparable"]);
}

// ---- B6: the server exits on `exit` ---------------------------------------

fn frame(body: &str) -> Vec<u8> {
    format!("Content-Length: {}\r\n\r\n{body}", body.len()).into_bytes()
}

/// Run the server, send `msgs`, keep stdin OPEN, and return its exit code
/// (`None` if it is still running after the deadline).
fn exit_code_with_open_stdin(msgs: &[&str]) -> Option<i32> {
    use std::io::Write;
    let mut child = std::process::Command::new(env!("CARGO_BIN_EXE_sky-lsp"))
        .stdin(std::process::Stdio::piped())
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .spawn()
        .unwrap();
    let mut stdin = child.stdin.take().unwrap();
    for m in msgs {
        stdin.write_all(&frame(m)).unwrap();
    }
    stdin.flush().unwrap();
    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(10);
    let code = loop {
        if let Some(st) = child.try_wait().unwrap() {
            break st.code();
        }
        if std::time::Instant::now() > deadline {
            let _ = child.kill();
            let _ = child.wait();
            break None;
        }
        std::thread::sleep(std::time::Duration::from_millis(50));
    };
    drop(stdin);
    code
}

const INIT: &str = r#"{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"processId":null,"rootUri":null,"capabilities":{}}}"#;
const INITIALIZED: &str = r#"{"jsonrpc":"2.0","method":"initialized","params":{}}"#;
const SHUTDOWN: &str = r#"{"jsonrpc":"2.0","id":2,"method":"shutdown"}"#;
const EXIT: &str = r#"{"jsonrpc":"2.0","method":"exit"}"#;

#[test]
fn the_server_exits_on_exit_while_stdin_stays_open() {
    assert_eq!(
        exit_code_with_open_stdin(&[INIT, INITIALIZED, SHUTDOWN, EXIT]),
        Some(0),
        "after `shutdown` then `exit` the server must exit with 0, stdin still open"
    );
    assert_eq!(
        exit_code_with_open_stdin(&[INIT, INITIALIZED, EXIT]),
        Some(1),
        "`exit` without a prior `shutdown` exits with 1 (LSP spec)"
    );
}

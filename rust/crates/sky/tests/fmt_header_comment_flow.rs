//! `sky fmt` with a comment the printer cannot place in the module header or
//! the imports: the header region is kept as written and `sky fmt` warns,
//! naming its first line and the comment's line. Every declaration below is
//! formatted, and `sky fmt --check` fails while one of them is unformatted.

use std::path::PathBuf;
use std::process::{Command, Output};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

const BODY: &str = "\nf x =\n  x + 2\n\n\nmain =\n  println \"x\"\n";

fn scratch(tag: &str) -> PathBuf {
    static SEQ: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
    let dir = std::env::temp_dir().join(format!(
        "sky-fmt-header-{tag}-{}-{}",
        std::process::id(),
        SEQ.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
    ));
    std::fs::create_dir_all(&dir).unwrap();
    dir
}

fn fmt(dir: &PathBuf, args: &[&str]) -> Output {
    Command::new(SKY)
        .args(args)
        .current_dir(dir)
        .output()
        .expect("run sky fmt")
}

/// `header` is kept; `warning` is the text the warning must hold.
fn case(tag: &str, header: &str, warning: &str) {
    let dir = scratch(tag);
    let src = format!("{header}{BODY}");
    std::fs::write(dir.join("Main.sky"), &src).unwrap();

    let check = fmt(&dir, &["fmt", "--check", "Main.sky"]);
    let stderr = String::from_utf8_lossy(&check.stderr);
    assert!(
        !check.status.success(),
        "--check must fail on an unformatted declaration below a kept header\n{stderr}"
    );
    assert!(stderr.contains(warning), "{stderr}");

    let write = fmt(&dir, &["fmt", "Main.sky"]);
    let stderr = String::from_utf8_lossy(&write.stderr);
    assert!(write.status.success(), "{stderr}");
    assert!(stderr.contains(warning), "{stderr}");
    let out = std::fs::read_to_string(dir.join("Main.sky")).unwrap();
    assert!(out.starts_with(header.trim_end()), "{out}");
    assert!(out.contains("f x =\n    x + 2\n"), "{out}");
    assert!(out.contains("main =\n    println \"x\"\n"), "{out}");

    // Formatted now: `--check` passes, still with the warning.
    let again = fmt(&dir, &["fmt", "--check", "Main.sky"]);
    let stderr = String::from_utf8_lossy(&again.stderr);
    assert!(again.status.success(), "{stderr}");
    assert!(stderr.contains(warning), "{stderr}");
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn a_trailing_comment_on_the_module_line() {
    case(
        "module",
        "module Main exposing (main) -- hdr\n\nimport Std.Log exposing (println)\n\n",
        "Main.sky:1: the module header and imports are kept as written, because formatting \
         would move the comment at the end of line 1. Move that comment to its own line above",
    );
}

#[test]
fn a_trailing_comment_on_an_import_line() {
    case(
        "import",
        "module Main exposing (main)\n\nimport Std.Log exposing (println) -- prelude\nimport Sky.Core.List as List\n\n",
        "Main.sky:1: the module header and imports are kept as written, because formatting \
         would move the comment at the end of line 3.",
    );
}

#[test]
fn a_comment_inside_the_module_exposing_list() {
    case(
        "modexp",
        "module Main exposing\n    ( main\n    -- the helper\n    , f\n    )\n\nimport Std.Log exposing (println)\n\n",
        "Main.sky:1: the module header and imports are kept as written, because the comment \
         on line 3 cannot be placed by the formatter.",
    );
}

#[test]
fn a_comment_inside_an_import_exposing_list() {
    case(
        "impexp",
        "module Main exposing (main)\n\nimport Std.Log\n    exposing\n        ( println\n        -- print a line\n        )\n\n",
        "Main.sky:1: the module header and imports are kept as written, because the comment \
         on line 6 cannot be placed by the formatter.",
    );
}

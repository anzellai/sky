//! The Go that Sky emits must stay cheap for the Go toolchain to compile.
//!
//! v0.25.18: building a 22k-line Sky.Spa app, one `go tool compile` of its
//! generated `main` package peaked at 5.6 GB, and the embedded console's package
//! at 4.8 GB unless inlining was turned off for it. Two such compiles at once
//! crashed a 16 GB Mac. Two generated shapes drove it, both closure nests:
//!
//!   * every `if` / `case` / `let` in tail position was an immediately-called
//!     closure (`return func() T { … }()`), so an `else if` chain or a `case`
//!     of `case`s nested one closure per level, and Go's inliner copied the
//!     nested bodies once per enclosing level (a 106 kB `update` became 7,871
//!     closure bodies);
//!   * a record constructor used as a curried value (`Codec.object Record`)
//!     was one closure per field, each capturing every field before it — 74
//!     levels for a 74-field record.
//!
//! This test builds a synthetic program with both shapes at scale (24 wide
//! records with codecs, 24 `case`-of-`case` functions, 24 long `else if`
//! chains) and pins the fix two ways:
//!   1. structurally — no function in the emitted `main.go` nests closures
//!      deeper than [`MAX_CLOSURE_DEPTH`] (the old shapes nested 60 deep here);
//!   2. by measurement — the largest Go tool process of the build, which
//!      `sky build` records in `sky-out/.sky-go-peak-bytes`, stays under
//!      [`COMPILE_BUDGET_BYTES`] (measured on go1.26.1: 2.07 GB before the fix,
//!      0.71 GB after).
//!
//! The program also runs, and prints the value its source computes, so the
//! reshaped Go is checked for behaviour, not only for shape.

use std::path::{Path, PathBuf};
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

/// Deepest closure nesting allowed in any emitted function. After the fix the
/// synthetic program nests 4 deep and a real 22k-line app 6; before it, 60+.
const MAX_CLOSURE_DEPTH: usize = 10;

/// Largest Go tool process allowed while building the synthetic program.
const COMPILE_BUDGET_BYTES: u64 = 1200 << 20;

const RECORDS: usize = 24;
const FIELDS: usize = 60;
const ARMS: usize = 60;

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

fn scratch(tag: &str) -> PathBuf {
    let uniq = format!(
        "sky-goshape-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    );
    let dir = std::env::temp_dir().join(uniq);
    std::fs::create_dir_all(dir.join("src")).unwrap();
    dir
}

/// The synthetic program. `nonce` makes `main.go` differ from every earlier
/// run, so Go compiles package `main` rather than taking it from its cache
/// (a cache hit would measure nothing).
fn program(nonce: &str) -> String {
    let mut s = String::from(
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Core.String as String\n\
         import Std.Codec as Codec exposing (Codec)\n\
         import Std.Log exposing (println)\n\n",
    );
    for r in 0..RECORDS {
        let fields: Vec<String> = (0..FIELDS)
            .map(|i| format!("f{i} : {}", if i % 2 == 0 { "Int" } else { "String" }))
            .collect();
        s += &format!("type alias Wide{r} =\n    {{ {} }}\n\n", fields.join(", "));
        s += &format!("wide{r}Codec : Codec Wide{r}\nwide{r}Codec =\n    Codec.object Wide{r}\n");
        for i in 0..FIELDS {
            let c = if i % 2 == 0 {
                "Codec.int"
            } else {
                "Codec.string"
            };
            s += &format!("        |> Codec.field \"f{i}\" .f{i} {c}\n");
        }
        s += "        |> Codec.buildObject\n\n";
        let ctors: Vec<String> = (0..ARMS).map(|i| format!("M{r}x{i} Int")).collect();
        s += &format!("type Msg{r}\n    = {}\n\n", ctors.join("\n    | "));
        s += &format!("step{r} : Msg{r} -> Int -> Int\nstep{r} msg n =\n    case msg of\n");
        for i in 0..ARMS {
            s += &format!(
                "        M{r}x{i} k ->\n            case k of\n                0 ->\n                    n + {i}\n\n\
                 \x20               _ ->\n                    let\n                        d = k * {}\n                    in\n\
                 \x20                   if d > 100 then\n                        n - d\n\n                    else\n                        n + d\n\n",
                i + 1
            );
        }
        s += &format!("pick{r} : String -> Int\npick{r} s =\n");
        for i in 0..ARMS {
            let kw = if i == 0 { "if" } else { "else if" };
            s += &format!("    {kw} s == \"a{i}\" then\n        {i}\n\n");
        }
        s += "    else\n        -1\n\n";
        // `{}` lacks every field, so the decode fails and the score is
        // pick "a3" (3) + step (M x2 5) 1 (5 * 3 = 15, so 1 + 15 = 16) = 19.
        s += &format!(
            "score{r} : Int\nscore{r} =\n    case Codec.fromJson wide{r}Codec \"{{}}\" of\n        Ok _ ->\n            1\n\n\
             \x20       Err _ ->\n            pick{r} \"a3\" + step{r} (M{r}x2 5) 1\n\n"
        );
    }
    let scores: Vec<String> = (0..RECORDS).map(|r| format!("score{r}")).collect();
    s += &format!("nonce : String\nnonce =\n    \"{nonce}\"\n\n");
    s += &format!(
        "main =\n    println (String.fromInt ({}) ++ \" \" ++ String.slice 0 0 nonce)\n",
        scores.join(" + ")
    );
    s
}

/// Deepest nesting of function-literal bodies in each top-level Go function,
/// as `(depth, function name)`, deepest first. A body opens at the `{` after
/// `func(…) …` at the literal's own parenthesis level; string, raw-string and
/// rune literals are skipped, and a `func` type with no body (a parameter or
/// field type) is dropped at the next `,` `;` `}` on its level.
fn closure_depths(go: &str) -> Vec<(usize, String)> {
    let mut out = Vec::new();
    for chunk in go.split("\nfunc ").skip(1) {
        let name: String = chunk
            .chars()
            .take_while(|c| c.is_alphanumeric() || *c == '_')
            .collect();
        let b = chunk.as_bytes();
        let (mut i, mut depth, mut max) = (0usize, 0usize, 0usize);
        let mut stack: Vec<bool> = Vec::new();
        let mut pending: Option<i64> = None; // paren level of a `func(` awaiting its body
        let mut par: i64 = 0;
        while i < b.len() {
            match b[i] {
                q @ (b'"' | b'\'') => {
                    i += 1;
                    while i < b.len() && b[i] != q {
                        if b[i] == b'\\' {
                            i += 1;
                        }
                        i += 1;
                    }
                }
                b'`' => {
                    i += 1;
                    while i < b.len() && b[i] != b'`' {
                        i += 1;
                    }
                }
                b'f' if chunk[i..].starts_with("func(")
                    && (i == 0 || !(b[i - 1].is_ascii_alphanumeric() || b[i - 1] == b'_')) =>
                {
                    pending = Some(par);
                    i += 3; // the `(` is read next
                }
                b'(' => par += 1,
                b')' => par -= 1,
                b'{' => {
                    let opens = pending == Some(par);
                    if opens {
                        depth += 1;
                        max = max.max(depth);
                        pending = None;
                    }
                    stack.push(opens);
                }
                b'}' => {
                    if pending == Some(par) {
                        pending = None;
                    }
                    if stack.pop() == Some(true) {
                        depth -= 1;
                    }
                }
                b',' | b';' if pending == Some(par) => pending = None,
                _ => {}
            }
            i += 1;
        }
        out.push((max, name));
    }
    out.sort_by(|a, b| b.cmp(a));
    out
}

fn build(dir: &Path) -> String {
    let out = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky build");
    assert!(
        out.status.success(),
        "sky build failed:\n{}\n{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    String::from_utf8_lossy(&out.stderr).into_owned()
}

#[test]
fn the_closure_depth_scanner_sees_nests_and_ignores_func_types() {
    let go = "package main\n\
              func A(f func(any) any, s struct{ G func(int) int }) int {\n\
              \treturn func() int { if true { return 1 }; return func() int { return 2 }() }()\n}\n\
              func B() any {\n\treturn func(a any) any { return any(func(b any) any { return any(func(c any) any { return \"}{func(\" }) }) }\n}\n\
              func C() string {\n\treturn `func(` + \"{\" + string('{')\n}\n";
    let d = closure_depths(go);
    assert_eq!(
        d,
        vec![
            (3, "B".to_string()),
            (2, "A".to_string()),
            (0, "C".to_string())
        ]
    );
}

#[cfg(unix)]
#[test]
fn emitted_go_has_no_deep_closure_nests_and_compiles_under_budget() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = scratch("shapes");
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"goshape\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
    )
    .unwrap();
    let nonce = format!(
        "{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    );
    std::fs::write(dir.join("src").join("Main.sky"), program(&nonce)).unwrap();
    let log = build(&dir);

    // Behaviour: the reshaped Go computes what the source says.
    let run = Command::new(dir.join("sky-out").join("app"))
        .current_dir(&dir)
        .output()
        .expect("run the built app");
    let stdout = String::from_utf8_lossy(&run.stdout);
    assert_eq!(
        stdout.trim(),
        format!("{}", 19 * RECORDS),
        "the app must print {} (24 x 19)",
        19 * RECORDS
    );

    // 1. Shape.
    let go = std::fs::read_to_string(dir.join("sky-out").join("main.go")).unwrap();
    let depths = closure_depths(&go);
    let deepest = &depths[0];
    assert!(
        deepest.0 <= MAX_CLOSURE_DEPTH,
        "{} nests closures {} deep (limit {MAX_CLOSURE_DEPTH}); the deepest five: {:?}. \
         A tail `if`/`case`/`let` must be statements, not `return func() T {{…}}()`, and a \
         curried value of arity >= 3 must be one `rt.CurryN` closure (lower/src/shape.rs, \
         Ctx::curried_any).",
        deepest.1,
        deepest.0,
        &depths[..depths.len().min(5)]
    );

    // 2. Measurement: the largest Go tool process of this build.
    let rec = std::fs::read_to_string(dir.join("sky-out").join(".sky-go-peak-bytes"))
        .unwrap_or_else(|e| panic!("sky build must record its Go peak: {e}\n{log}"));
    let peak = project::memory::parse_record(&rec)
        .unwrap_or_else(|| panic!("unreadable Go peak record {rec:?}"));
    assert!(
        peak <= COMPILE_BUDGET_BYTES,
        "the largest Go tool process took {} MB (budget {} MB; 2070 MB before the v0.25.18 \
         emission fix, 710 MB after, on go1.26.1). A generated shape is expensive for the \
         Go compiler again.",
        peak >> 20,
        COMPILE_BUDGET_BYTES >> 20
    );
    let _ = std::fs::remove_dir_all(&dir);
}

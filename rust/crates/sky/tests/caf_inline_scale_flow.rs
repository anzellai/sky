//! Counted guard: the Go back end compiles a memoised top-level value (a CAF)
//! once, not once per caller.
//!
//! A CAF is emitted as a one-line accessor over `rt.LazyCaf`. Go inlines that
//! accessor into every caller. When the accessor held the initialiser as a
//! closure literal (`return X__caf.Get(func() T { … })`), each inlined copy
//! carried the closure, and every closure nested in it, so the back end
//! compiled the whole tree once per call site. A generated Sky.Spa backend at
//! 200 Model fields compiled 100,681 functions (82,216 of them copies of one
//! codec initialiser) and took 62 s and 6.1 GB in `go build`. The initialiser
//! is now a named function passed by name (`X__caf.Get(X__cafInit)`), and Go
//! inlines only direct calls.
//!
//! The program below has one CAF holding `n` lambdas and `n` functions that
//! read it. With the copying, the compiled-function count grows as `n * n`
//! (about 4x per doubling of `n`). Without it, it grows as `n` (about 2x). The
//! test builds the program at `n` and `2n` and reads the count from the Go
//! compiler's own `-bench` report (`be:compilefuncs … N funcs`), which is
//! deterministic for a given source and Go version.

use std::path::{Path, PathBuf};
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

/// The largest growth of the compiled-function count allowed when `n`
/// doubles. With the initialiser outlined this program measures 190 -> 310
/// (1.63x) for n = 40 -> 80; with the closure literal in the accessor it
/// measured 1,830 -> 6,790 (3.71x), go1.26.
const MAX_GROWTH_PER_DOUBLING: f64 = 2.3;

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
        "sky-cafscale-{tag}-{}-{}",
        (u64::from(std::process::id()) << 32) | {
            static SEQ: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
            SEQ.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
        },
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    );
    std::env::temp_dir().join(uniq)
}

/// A program with one CAF of `n` lambdas, read by `n` functions.
fn program(n: usize) -> String {
    let mut s = String::from(
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Core.List as List\n\
         import Sky.Core.String as String\n\
         import Std.Log exposing (println)\n\n\n\
         table : List (Int -> Int)\n\
         table =\n",
    );
    for k in 1..=n {
        let lead = if k == 1 { "[" } else { "," };
        s.push_str(&format!("    {lead} \\x -> x * {k} + {k}\n"));
    }
    s.push_str("    ]\n");
    for k in 1..=n {
        s.push_str(&format!(
            "\n\nuse{k} : Int -> Int\nuse{k} v =\n    List.foldl (\\f acc -> f acc) (v + {k}) table\n"
        ));
    }
    s.push_str("\n\nmain =\n    println (String.fromInt (0");
    for k in 1..=n {
        s.push_str(&format!(" + use{k} {k}"));
    }
    s.push_str("))\n");
    s
}

/// Build the program at size `n` and return the Go back end's compiled
/// function count for the `main` package.
fn compiled_funcs(n: usize) -> usize {
    let dir = scratch(&n.to_string());
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"caf-scale\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n\
         [source]\nroot = \"src\"\n",
    )
    .unwrap();
    std::fs::write(dir.join("src/Main.sky"), program(n)).unwrap();

    let out = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(&dir)
        .output()
        .expect("spawn sky build");
    assert!(
        out.status.success(),
        "sky build failed at n={n}:\n{}\n{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );

    let go_dir = dir.join("sky-out");
    let bench = dir.join("bench.txt");
    // The bench path is unique per run, so the compile action is never a Go
    // build-cache hit (a cached compile writes no report).
    let out = Command::new("go")
        .args([
            "build",
            "-trimpath",
            &format!("-gcflags=sky-app=-bench={}", bench.display()),
            "-o",
            if cfg!(windows) { "NUL" } else { "/dev/null" },
            ".",
        ])
        .env("GOFLAGS", "-mod=mod -buildvcs=false")
        .env("CGO_ENABLED", "0")
        .current_dir(&go_dir)
        .output()
        .expect("spawn go build");
    assert!(
        out.status.success(),
        "go build failed at n={n}:\n{}",
        String::from_utf8_lossy(&out.stderr)
    );
    let funcs = parse_compilefuncs(&bench);
    let _ = std::fs::remove_dir_all(&dir);
    funcs
}

/// The `N` of the last `be:compilefuncs … N funcs …` line in a `-bench` report.
fn parse_compilefuncs(bench: &Path) -> usize {
    let text = std::fs::read_to_string(bench)
        .unwrap_or_else(|e| panic!("no -bench report at {}: {e}", bench.display()));
    let line = text
        .lines()
        .filter(|l| l.contains("be:compilefuncs"))
        .next_back()
        .unwrap_or_else(|| panic!("no be:compilefuncs line in:\n{text}"));
    let words: Vec<&str> = line.split_whitespace().collect();
    let at = words
        .iter()
        .position(|w| *w == "funcs")
        .unwrap_or_else(|| panic!("no `funcs` field in: {line}"));
    words[at - 1]
        .parse()
        .unwrap_or_else(|e| panic!("bad count in `{line}`: {e}"))
}

#[test]
fn parse_compilefuncs_reads_the_count() {
    let dir = scratch("parse");
    std::fs::create_dir_all(&dir).unwrap();
    let f = dir.join("b.txt");
    std::fs::write(
        &f,
        "BenchmarkCompile:main:be:compilefuncs  1  44290568291 ns/op  81.50 %  100681 funcs  2273 funcs/s\n",
    )
    .unwrap();
    assert_eq!(parse_compilefuncs(&f), 100_681);
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn caf_initialiser_is_compiled_once_not_per_caller() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let n = 40;
    let small = compiled_funcs(n);
    let large = compiled_funcs(2 * n);
    let growth = large as f64 / small as f64;
    eprintln!(
        "compiled funcs: n={n}: {small}, n={}: {large}, growth {growth:.2}x",
        2 * n
    );
    assert!(
        growth <= MAX_GROWTH_PER_DOUBLING,
        "doubling the callers of one CAF grew the compiled-function count \
         {growth:.2}x ({small} -> {large}); at most {MAX_GROWTH_PER_DOUBLING}x is \
         allowed. A CAF accessor that the Go inliner copies must not carry its \
         initialiser as a closure literal (lower.rs, CAF memoisation)."
    );
}

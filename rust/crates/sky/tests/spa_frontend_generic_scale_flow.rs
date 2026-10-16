//! Counted guard: a Sky.Spa wasm frontend does not compile the runtime's
//! reflect narrowing once per record type.
//!
//! The client narrows each server RPC's response to its own record
//! (`rt.ResultCoerceOk[Error, Shared_LoadKResp_R](…, rt.Coerce[…])`). Go
//! compiles a generic function once per type shape, in the package that
//! instantiates it, and each Sky record alias is its own shape (its field
//! names and `sky:` tags are part of the struct type). `rt.Coerce`,
//! `rt.coerceInner` and `rt.ResultCoerce` used to carry their whole reflect
//! fallback in the generic body, so the app's `main` package compiled that
//! fallback once per response type. A generated web:app with 400 response
//! types spent 8.3 s of the 10.8 s its wasm `go build` spent in SSA on those
//! copies (14.4 s wall, 4.3 GB peak). The fallback is now non-generic and
//! compiled once in `rt`; the generic shells keep only the type assertions.
//!
//! The program below is a Sky.Spa app with `n` server arms, each writing one
//! Model field, so the split gives it `n` distinct response records. The test
//! builds its wasm frontend at `n` and `2n` and reads, from the Go compiler's
//! own `-S` listing of package `main`, the machine-code bytes of the runtime's
//! generic instances compiled there. The bytes added per response type must
//! stay under [`MAX_INSTANCE_BYTES_PER_TYPE`]. It also reports the compiled
//! function count and the largest function, and bounds their growth.

use std::path::{Path, PathBuf};
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

/// Bytes of runtime generic instances compiled into package `main` per added
/// response type. Measured on go1.26.1 (GOOS=js GOARCH=wasm), n = 20 -> 40:
/// 2,509 with the reflect fallback in the generic bodies (67,930 -> 118,110
/// bytes), 164 without it (6,389 -> 9,669).
const MAX_INSTANCE_BYTES_PER_TYPE: usize = 800;

/// Linear growth doubles; the margin covers the fixed parts.
const MAX_GROWTH_PER_DOUBLING: f64 = 2.3;

const N: usize = 20;

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
    std::env::temp_dir().join(format!(
        "sky-spa-generic-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ))
}

/// A Sky.Spa app with `n` server arms. `LoadK` writes field `fK` from a
/// server read (`System.getenvOr`), so the split routes it to an RPC whose
/// response record carries `fK` alone: `n` distinct response types.
fn program(n: usize) -> String {
    let mut s = String::from(
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Core.System as System\n\
         import Sky.Core.String as String\n\
         import Std.Spa as Spa\n\
         import Std.Cmd as Cmd\n\
         import Std.Sub as Sub\n\
         import Std.Ui as Ui\n\
         import Std.Html exposing (Html)\n\n\n\
         type alias Model =\n    { count : Int\n",
    );
    for k in 1..=n {
        s.push_str(&format!("    , f{k} : String\n"));
    }
    s.push_str("    }\n\n\ntype Msg\n    = Bump\n");
    for k in 1..=n {
        s.push_str(&format!("    | Load{k}\n"));
    }
    s.push_str("\n\ninit : () -> ( Model, Cmd Msg )\ninit _ =\n    ( { count = 0\n");
    for k in 1..=n {
        s.push_str(&format!("      , f{k} = \"\"\n"));
    }
    s.push_str("      }\n    , Cmd.none\n    )\n\n\n");
    s.push_str(
        "update : Msg -> Model -> ( Model, Cmd Msg )\nupdate msg model =\n    case msg of\n        \
         Bump ->\n            ( { model | count = model.count + 1 }, Cmd.none )\n",
    );
    for k in 1..=n {
        s.push_str(&format!(
            "\n        Load{k} ->\n            ( {{ model | f{k} = System.getenvOr \"F{k}\" \"{k}\" }}, Cmd.none )\n"
        ));
    }
    s.push_str(
        "\n\nview : Model -> Html Msg\nview model =\n    Ui.layout []\n        (Ui.column []\n            \
         [ Ui.button [] { onPress = Just Bump, label = Ui.text (String.fromInt model.count) }\n",
    );
    for k in 1..=n {
        s.push_str(&format!(
            "            , Ui.button [] {{ onPress = Just Load{k}, label = Ui.text model.f{k} }}\n"
        ));
    }
    s.push_str(
        "            ]\n        )\n\n\n\
         subscriptions : Model -> Sub Msg\nsubscriptions _ =\n    Sub.none\n\n\n\
         main : Task Error ()\nmain =\n    Spa.app\n        (Spa.config\n            \
         { init = init\n            , update = update\n            , view = view\n            \
         , subscriptions = subscriptions\n            }\n        )\n",
    );
    s
}

#[derive(Debug, Clone, Copy)]
struct Measure {
    /// Machine-code bytes of `sky-app/rt.<generic>[…]` instances in `main`.
    rt_instance_bytes: usize,
    /// Every function `main` compiled.
    funcs: usize,
    /// Size of the largest function `main` compiled.
    largest: usize,
}

fn run(cmd: &mut Command, what: &str) -> String {
    let out = cmd.output().unwrap_or_else(|e| panic!("spawn {what}: {e}"));
    assert!(
        out.status.success(),
        "{what} failed:\n{}\n{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    let mut text = String::from_utf8_lossy(&out.stdout).into_owned();
    text.push_str(&String::from_utf8_lossy(&out.stderr));
    text
}

fn measure(n: usize) -> Measure {
    let dir = scratch(&n.to_string());
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"spa-generic-scale\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n\
         [source]\nroot = \"src\"\n",
    )
    .unwrap();
    std::fs::write(dir.join("src/Main.sky"), program(n)).unwrap();
    run(
        Command::new(SKY)
            .args(["spa-split", "src/Main.sky", "--out", ".split"])
            .current_dir(&dir),
        &format!("sky spa-split at n={n}"),
    );
    let split = dir.join(".split");
    run(
        Command::new(SKY)
            .args([
                "build",
                "--target",
                "web",
                "--no-precompress",
                "frontend/src/Main.sky",
            ])
            .current_dir(&split),
        &format!("sky build (wasm frontend) at n={n}"),
    );
    let go_dir = split.join("frontend/sky-out");
    // The -bench path is unique per run, so the compile of `main` is never a
    // Go build-cache hit (a cached compile prints no listing).
    let bench = dir.join("bench.txt");
    let listing = run(
        Command::new("go")
            .args([
                "build",
                "-trimpath",
                &format!("-gcflags=sky-app=-S -bench={}", bench.display()),
                "-o",
                if cfg!(windows) { "NUL" } else { "/dev/null" },
                ".",
            ])
            .env("GOOS", "js")
            .env("GOARCH", "wasm")
            .env("GOFLAGS", "-mod=mod -buildvcs=false")
            .env("CGO_ENABLED", "0")
            .current_dir(&go_dir),
        &format!("go build -S (wasm frontend) at n={n}"),
    );
    let m = parse_listing(&listing);
    let _ = std::fs::remove_dir_all(&dir);
    m
}

/// Read every `<name> STEXT … size=<bytes> …` header of a `-S` listing.
fn parse_listing(text: &str) -> Measure {
    let mut m = Measure {
        rt_instance_bytes: 0,
        funcs: 0,
        largest: 0,
    };
    for line in text.lines() {
        let Some(at) = line.find(" STEXT") else {
            continue;
        };
        if line.starts_with('\t') || line.starts_with(' ') {
            continue;
        }
        let name = &line[..at];
        let Some(sz) = line[at..]
            .split_whitespace()
            .find_map(|w| w.strip_prefix("size="))
            .and_then(|v| v.parse::<usize>().ok())
        else {
            continue;
        };
        m.funcs += 1;
        m.largest = m.largest.max(sz);
        if name.starts_with("sky-app/rt.") && name.contains('[') {
            m.rt_instance_bytes += sz;
        }
    }
    assert!(m.funcs > 0, "no STEXT headers in the -S listing");
    m
}

#[test]
fn parse_listing_reads_text_symbols() {
    let text = "main.Main_view STEXT size=120 args=0x8 locals=0x0\n\
                \t0x0000 00000 (main.go:1)\tTEXT\tmain.Main_view(SB)\n\
                sky-app/rt.Coerce[go.shape.struct { X int \"sky:\\\"x\\\"\" }] STEXT dupok size=40 args=0x10\n\
                main.init STEXT size=300 args=0x0 locals=0x0\n";
    let m = parse_listing(text);
    assert_eq!(m.funcs, 3);
    assert_eq!(m.largest, 300);
    assert_eq!(m.rt_instance_bytes, 40);
}

#[test]
fn spa_frontend_compiles_the_reflect_fallback_once_not_per_record() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let small = measure(N);
    let large = measure(2 * N);
    let per_type = large
        .rt_instance_bytes
        .saturating_sub(small.rt_instance_bytes)
        / N;
    let growth = |a: usize, b: usize| b as f64 / a as f64;
    eprintln!(
        "n={N}: {small:?}\nn={}: {large:?}\nruntime generic instance bytes per response type: {per_type}",
        2 * N
    );
    assert!(
        per_type <= MAX_INSTANCE_BYTES_PER_TYPE,
        "each response record type added {per_type} bytes of runtime generic \
         instances to the wasm frontend's `main` package (at most \
         {MAX_INSTANCE_BYTES_PER_TYPE}). The reflect fallback of rt.Coerce / \
         rt.coerceInner / rt.ResultCoerce / rt.MaybeCoerce must stay out of the \
         generic body (runtime-go/rt/rt.go: coerceHead, coerceTail, \
         coerceInnerSlow, reflectResultFields), or Go compiles it once per type."
    );
    assert!(
        growth(small.funcs, large.funcs) <= MAX_GROWTH_PER_DOUBLING,
        "compiled functions grew {:.2}x from n={N} to n={}",
        growth(small.funcs, large.funcs),
        2 * N
    );
    assert!(
        growth(small.largest, large.largest) <= MAX_GROWTH_PER_DOUBLING,
        "the largest function grew {:.2}x from n={N} to n={}",
        growth(small.largest, large.largest),
        2 * N
    );
}

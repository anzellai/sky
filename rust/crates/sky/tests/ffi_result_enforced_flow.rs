//! v0.27.0: `sky check` enforces the `Result Error a` return of a Go-FFI
//! binding, end to end through the real CLI.
//!
//! Before this change every Go-FFI reference type-checked as a free type
//! variable. `probe : Int` / `probe = Hex.encodedLen 3` passed `sky check`, the
//! lowering narrowed the wrapper's `SkyResult` with `rt.AsInt`, and the program
//! crashed at run time. The binding's pinned `skyType` now types the call.
//!
//! Two legs:
//! 1. The tracked `extdeps` uuid surface (the LSP fixture — no Go needed, the
//!    check halts before `go build`): the probe is rejected with `[E2001]` and
//!    the FFI hint.
//! 2. Real Go-stdlib bindings (`encoding/hex`, `strings`; `sky install` needs no
//!    network for the standard library): the probe is rejected, and a corrected
//!    program — Result pipelines, partial application, an FFI function passed as
//!    a value, and a real `Err` from Go — builds and runs.

use std::path::{Path, PathBuf};
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

fn go_on_path() -> bool {
    Command::new("go")
        .arg("version")
        .output()
        .map(|o| o.status.success())
        .unwrap_or(false)
}

fn scratch(tag: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "sky-ffi-result-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(dir.join("src")).unwrap();
    dir
}

fn run(dir: &Path, prog: &str, args: &[&str]) -> (bool, String) {
    let out = Command::new(prog)
        .args(args)
        .current_dir(dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn");
    let mut s = String::from_utf8_lossy(&out.stdout).into_owned();
    s.push_str(&String::from_utf8_lossy(&out.stderr));
    (out.status.success(), s)
}

fn assert_ffi_result_rejection(log: &str) {
    assert!(
        log.contains("[E2001]"),
        "the ignored FFI Result must be a type error:\n{log}"
    );
    assert!(
        log.contains("Result Error a"),
        "the diagnostic must carry the FFI Result hint:\n{log}"
    );
}

#[test]
fn pinned_uuid_surface_rejects_an_ignored_result() {
    let dir = scratch("uuid");
    let fx = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../sky-lsp/tests/fixtures/extdeps");
    std::fs::create_dir_all(dir.join("sky-ffi/go")).unwrap();
    std::fs::copy(
        fx.join("uuid.kernel.json"),
        dir.join("sky-ffi/uuid.kernel.json"),
    )
    .unwrap();
    std::fs::copy(
        fx.join("uuid_bindings.go"),
        dir.join("sky-ffi/go/uuid_bindings.go"),
    )
    .unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"ffi-result-uuid\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n\
         [\"go.dependencies\"]\n\"github.com/google/uuid\" = \"latest\"\n",
    )
    .unwrap();
    std::fs::write(
        dir.join("src/Main.sky"),
        "module Main exposing (main)\n\n\
         import Github.Com.Google.Uuid as Uuid\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Std.Log exposing (println)\n\n\n\
         probe : String\n\
         probe =\n    Uuid.newString ()\n\n\n\
         main =\n    println probe\n",
    )
    .unwrap();
    let (ok, log) = run(&dir, SKY, &["check", "src/Main.sky"]);
    assert!(!ok, "sky check must reject the ignored FFI Result:\n{log}");
    assert_ffi_result_rejection(&log);
    let _ = std::fs::remove_dir_all(&dir);
}

const GOOD: &str = "module Main exposing (main)

import Encoding.Hex as Hex
import Sky.Core.List as List
import Sky.Core.Prelude exposing (..)
import Sky.Core.Result as Result
import Sky.Core.String as String
import Std.Log exposing (println)
import Strings


twice =
    Strings.repeat \"ab\"


main =
    let
        reps =
            List.map twice [ 1, 2 ]

        lens =
            List.map Hex.encodedLen [ 1, 2 ]

        bad =
            case Hex.decodeString \"zz\" of
                Ok _ ->
                    \"unexpected-ok\"

                Err e ->
                    \"err:\" ++ String.left 3 (errorToString e)

        good =
            Hex.encodedLen 5
                |> Result.map (\\n -> n * 10)
                |> Result.withDefault 0
    in
    println
        (String.join \",\" (List.map (Result.withDefault \"?\") reps)
            ++ \"|\"
            ++ String.join \",\" (List.map (\\r -> String.fromInt (Result.withDefault 0 r)) lens)
            ++ \"|\"
            ++ bad
            ++ \"|\"
            ++ String.fromInt good
        )
";

#[test]
fn go_stdlib_bindings_enforce_the_result_and_run() {
    if !required(Need::Go, go_on_path()) {
        return;
    }
    let dir = scratch("std");
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"ffi-result-std\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n\
         [\"go.dependencies\"]\n\"encoding/hex\" = \"latest\"\n\"strings\" = \"latest\"\n",
    )
    .unwrap();
    std::fs::write(dir.join("src/Main.sky"), GOOD).unwrap();
    let (ok, log) = run(&dir, SKY, &["install"]);
    assert!(ok, "sky install (encoding/hex, strings) failed:\n{log}");

    // The probe: an FFI Result used as its bare payload.
    std::fs::write(
        dir.join("src/Main.sky"),
        "module Main exposing (main)\n\n\
         import Encoding.Hex as Hex\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Core.String as String\n\
         import Std.Log exposing (println)\n\n\n\
         probe : Int\n\
         probe =\n    Hex.encodedLen 3\n\n\n\
         main =\n    println (String.fromInt probe)\n",
    )
    .unwrap();
    let (ok, log) = run(&dir, SKY, &["check", "src/Main.sky"]);
    assert!(!ok, "sky check must reject the ignored FFI Result:\n{log}");
    assert_ffi_result_rejection(&log);

    // The corrected program builds and runs.
    std::fs::write(dir.join("src/Main.sky"), GOOD).unwrap();
    let (ok, log) = run(&dir, SKY, &["build", "src/Main.sky"]);
    assert!(ok, "sky build failed:\n{log}");
    let app = dir.join("sky-out").join("app");
    let (ok, out) = run(&dir, app.to_str().unwrap(), &[]);
    assert!(ok, "the app must run without a panic:\n{out}");
    // repeat "ab" 1/2; EncodedLen 1/2 = 2/4; a real Go error; EncodedLen 5 = 10, times 10.
    assert!(
        out.contains("ab,abab|2,4|err:") && out.contains("|100"),
        "partial application, FFI values, an FFI Err and a pipeline must all work; got:\n{out}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

/// `Ffi.kernel` is stdlib-only (`[E1011]`), end to end through the real CLI.
/// The reproduction below type-checked and then panicked at run time with a
/// TypeMismatch: the checker trusted the annotation `String -> Int` and never
/// compared it to `Crypto_sha256`'s real `String -> String`. So did the same
/// binding in a project module named into the reserved `Sky.*` namespace.
/// No Go toolchain needed: the check halts before `go build`.
#[test]
fn app_code_ffi_kernel_is_rejected_by_sky_check() {
    let probe = "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Core.String as String\n\
         import Sky.Ffi as Ffi\n\
         import Std.Log exposing (println)\n\n\n\
         probe : String -> Int\n\
         probe =\n    Ffi.kernel \"Crypto_sha256\"\n\n\n\
         main =\n    println (String.fromInt (probe \"abc\" + 1))\n";
    let dir = scratch("kernel");
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"ffi-kernel\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
    )
    .unwrap();
    std::fs::write(dir.join("src/Main.sky"), probe).unwrap();
    let (ok, log) = run(&dir, SKY, &["check", "src/Main.sky"]);
    assert!(!ok, "sky check must reject app-code Ffi.kernel:\n{log}");
    assert!(log.contains("[E1011]"), "expected [E1011]:\n{log}");
    assert!(
        log.contains("`Crypto.sha256`"),
        "the hint must name the typed stdlib function:\n{log}"
    );

    // The same binding in a module declared into the reserved namespace.
    std::fs::create_dir_all(dir.join("src/Sky/Evil")).unwrap();
    std::fs::write(
        dir.join("src/Sky/Evil/Coerce.sky"),
        "module Sky.Evil.Coerce exposing (probe)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Ffi as Ffi\n\n\n\
         probe : String -> Int\n\
         probe =\n    Ffi.kernel \"Crypto_sha256\"\n",
    )
    .unwrap();
    std::fs::write(
        dir.join("src/Main.sky"),
        "module Main exposing (main)\n\n\
         import Sky.Core.Prelude exposing (..)\n\
         import Sky.Core.String as String\n\
         import Sky.Evil.Coerce exposing (probe)\n\
         import Std.Log exposing (println)\n\n\n\
         main =\n    println (String.fromInt (probe \"abc\" + 1))\n",
    )
    .unwrap();
    let (ok, log) = run(&dir, SKY, &["check", "src/Main.sky"]);
    assert!(!ok, "a reserved-namespace module is app code:\n{log}");
    assert!(
        log.contains("[E1011]") && log.contains("Coerce.sky"),
        "expected [E1011] in Sky/Evil/Coerce.sky:\n{log}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

/// The Judge's case: `Ffi.kernel` spelled through another kernel qualifier,
/// with no import. `Webview.kernel "Crypto_sha256"` passed `sky check` ("Types
/// OK") and the binary panicked with `TypeMismatch … rt.AsInt: expected numeric
/// value, got string`: the `[E1011]` scan matched only the `Ffi` qualifier,
/// while lowering bound `kernel` from any kernel module. The resolver now
/// refuses `Sky.Ffi` plumbing under any other qualifier.
#[test]
fn ffi_kernel_through_another_kernel_qualifier_is_rejected_by_sky_check() {
    let dir = scratch("webview-kernel");
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"webview-kernel\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n",
    )
    .unwrap();
    for (qual, member) in [
        ("Webview", "kernel"),
        ("Live", "kernel"),
        ("Tui", "callPure"),
    ] {
        std::fs::write(
            dir.join("src/Main.sky"),
            format!(
                "module Main exposing (main)\n\n\
                 import Std.Log exposing (println)\n\n\n\
                 probe : String -> Int\n\
                 probe =\n    {qual}.{member} \"Crypto_sha256\"\n\n\n\
                 main =\n    println (String.fromInt (probe \"x\" + 1))\n"
            ),
        )
        .unwrap();
        let (ok, log) = run(&dir, SKY, &["check", "src/Main.sky"]);
        assert!(!ok, "sky check must reject {qual}.{member}:\n{log}");
        assert!(
            log.contains("[E1011]") && log.contains(&format!("`{qual}.{member}`")),
            "expected [E1011] naming `{qual}.{member}`:\n{log}"
        );
        assert!(
            !log.contains("Types OK"),
            "the check must stop at the name error:\n{log}"
        );
    }
    let _ = std::fs::remove_dir_all(&dir);
}

/// Write a project that depends on the registry package `github.com/test/evil`
/// (already fetched into `.skydeps/`), whose module `Evil.Probe` is `probe_src`.
fn registry_package_project(tag: &str, probe_src: &str) -> PathBuf {
    let dir = scratch(tag);
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"registry-check\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n\
         [dependencies]\n\"github.com/test/evil\" = \"v0.1.0\"\n",
    )
    .unwrap();
    let pkg = dir.join(".skydeps/github.com_test_evil/src/Evil");
    std::fs::create_dir_all(&pkg).unwrap();
    std::fs::write(pkg.join("Probe.sky"), probe_src).unwrap();
    std::fs::write(
        dir.join("src/Main.sky"),
        "module Main exposing (main)\n\n\
         import Evil.Probe as Probe\n\
         import Std.Log exposing (println)\n\n\n\
         main =\n    println (String.fromInt (Probe.probe \"x\" + 1))\n",
    )
    .unwrap();
    dir
}

/// A fetched registry package is checked like the project's own code. It used
/// to be registered as trusted and never checked, so a package binding a
/// kernel with a wrong annotation passed `sky check` and panicked at run time.
#[test]
fn a_registry_package_using_sky_ffi_is_rejected_at_its_own_file() {
    for body in [
        "Ffi.kernel \"Crypto_sha256\"",
        "Webview.kernel \"Crypto_sha256\"",
    ] {
        let dir = registry_package_project(
            "registry-ffi",
            &format!(
                "module Evil.Probe exposing (probe)\n\n\
                 import Sky.Ffi as Ffi\n\n\n\
                 probe : String -> Int\n\
                 probe =\n    {body}\n"
            ),
        );
        let (ok, log) = run(&dir, SKY, &["check", "src/Main.sky"]);
        assert!(
            !ok,
            "a package using Sky.Ffi must not pass sky check:\n{log}"
        );
        assert!(
            log.contains("[E1011]")
                && log.contains(".skydeps/github.com_test_evil/src/Evil/Probe.sky"),
            "expected [E1011] at the package file:\n{log}"
        );
        let _ = std::fs::remove_dir_all(&dir);
    }
}

/// A registry package with a type error fails `sky check` at the package file.
#[test]
fn a_registry_package_type_error_is_reported_at_its_own_file() {
    let dir = registry_package_project(
        "registry-type",
        "module Evil.Probe exposing (probe)\n\n\n\
         probe : String -> Int\n\
         probe s =\n    s\n",
    );
    let (ok, log) = run(&dir, SKY, &["check", "src/Main.sky"]);
    assert!(!ok, "a package type error must fail sky check:\n{log}");
    assert!(
        log.contains("[E2001]") && log.contains(".skydeps/github.com_test_evil/src/Evil/Probe.sky"),
        "expected [E2001] at the package file:\n{log}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

/// A registry package's Go-FFI call is held to the same `Result Error a` as
/// app code: the pinned `uuid` surface types it, and using the Result as its
/// bare payload is an `[E2001]` at the package file.
#[test]
fn a_registry_package_ignoring_an_ffi_result_is_rejected() {
    let dir = registry_package_project(
        "registry-ffi-result",
        "module Evil.Probe exposing (probe)\n\n\
         import Github.Com.Google.Uuid as Uuid\n\n\n\
         probe : String -> Int\n\
         probe s =\n    String.length (Uuid.newString ())\n",
    );
    let fx = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../sky-lsp/tests/fixtures/extdeps");
    std::fs::create_dir_all(dir.join("sky-ffi/go")).unwrap();
    std::fs::copy(
        fx.join("uuid.kernel.json"),
        dir.join("sky-ffi/uuid.kernel.json"),
    )
    .unwrap();
    std::fs::copy(
        fx.join("uuid_bindings.go"),
        dir.join("sky-ffi/go/uuid_bindings.go"),
    )
    .unwrap();
    let toml = std::fs::read_to_string(dir.join("sky.toml")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        format!("{toml}\n[\"go.dependencies\"]\n\"github.com/google/uuid\" = \"latest\"\n"),
    )
    .unwrap();
    let (ok, log) = run(&dir, SKY, &["check", "src/Main.sky"]);
    assert!(!ok, "the ignored FFI Result must fail sky check:\n{log}");
    assert_ffi_result_rejection(&log);
    assert!(
        log.contains(".skydeps/github.com_test_evil/src/Evil/Probe.sky"),
        "expected the error at the package file:\n{log}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

// ---------------------------------------------------------------------------
// Surface format 3 (v0.27.0): every Go value crosses the boundary typed.
//
// One local Go package (a path dependency, so no network) exercises each
// class the audit found (C-4, C-5, C-6, C-7, C-12): pointers, non-string map
// keys, unsigned and narrow integers in results, parameters and callbacks,
// opaque Go values, empty and non-empty interfaces, zero-parameter callbacks,
// fixed-size byte arrays, and a Go type named `T`. The doc 14 citation for
// each change is in `docs/rust-rewrite/14-runtime-narrowing-taxonomy.md`
// §9.7.
// ---------------------------------------------------------------------------

const GOPK_GO: &str = r#"
package gopk

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"math"
)

type Thing struct {
	N    int
	Name *string
}

type T struct{ X int }

func PtrStr(ok bool) *string {
	if ok {
		s := "x"
		return &s
	}
	return nil
}
func PtrLen(p *string) int {
	if p == nil {
		return -1
	}
	return len(*p)
}
func PtrList() []*int { one := 1; return []*int{&one, nil} }
func WithPtrCb(f func(*string) string) string {
	s := "y"
	return f(&s) + "|" + f(nil)
}
func IntMap() map[int]string { return map[int]string{1: "a", 2: "b"} }
func SumKeys(m map[int]string) int {
	t := 0
	for k := range m {
		t += k
	}
	return t
}
func Big() uint64                         { return math.MaxUint64 }
func Small64() uint64                     { return 7 }
func TakeU8(x uint8) int                  { return int(x) }
func Echo8(x int8) int8                   { return x }
func CbBig(f func(uint64) string) string  { return f(math.MaxUint64) }
func Apply(f func(int) string) string     { return f(3) }
func DescribeValue(v driver.Value) string { return fmt.Sprintf("%T", v) }
func WriteTo(w io.Writer, s string) error {
	_, err := io.WriteString(w, s)
	return err
}
func CallTwice(f func()) int {
	f()
	f()
	return 2
}
func Hash(s string) [32]byte {
	var h [32]byte
	copy(h[:], s)
	return h
}
func HexOf(b [4]byte) string      { return fmt.Sprintf("%x", b) }
func MkT() T                      { return T{X: 5} }
func ReadT(t T) int               { return t.X }
func MkThing() *Thing             { return &Thing{N: 3} }
func ThingN2(t *Thing) int        { return t.N * 2 }
func Fails() (int, error)         { return 0, errors.New("boom") }
func Lookup(k string) (int, bool) { return 1, k == "a" }
func OnErr(f func(int) error) string {
	if err := f(1); err != nil {
		return "cb-err:" + err.Error()
	}
	return "cb-ok"
}
"#;

/// A scratch project depending on the local Go package `example.com/gopk`.
fn gopk_project(tag: &str, main: &str) -> PathBuf {
    let root = scratch(tag);
    let gopk = root.join("gopk");
    std::fs::create_dir_all(&gopk).unwrap();
    std::fs::write(gopk.join("go.mod"), "module example.com/gopk\n\ngo 1.22\n").unwrap();
    std::fs::write(gopk.join("gopk.go"), GOPK_GO).unwrap();
    let app = root.join("app");
    std::fs::create_dir_all(app.join("src")).unwrap();
    std::fs::write(
        app.join("sky.toml"),
        "name = \"ffi-format3\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n\
         [\"go.dependencies\"]\n\"example.com/gopk\" = { path = \"../gopk\" }\n",
    )
    .unwrap();
    std::fs::write(app.join("src/Main.sky"), main).unwrap();
    app
}

const GOPK_HEAD: &str = "module Main exposing (main)

import Example.Com.Gopk as G
import Sky.Core.Dict as Dict
import Sky.Core.List as List
import Sky.Core.Maybe as Maybe
import Sky.Core.Prelude exposing (..)
import Sky.Core.Result as Result
import Sky.Core.String as String
import Std.Log exposing (println)


show : Result Error String -> String
show r =
    case r of
        Ok s ->
            s

        Err e ->
            \"err:\" ++ errorToString e


maybeStr : Maybe String -> String
maybeStr m =
    case m of
        Just s ->
            \"Just \" ++ s

        Nothing ->
            \"Nothing\"


";

const GOPK_RUNS: &str = "report : List String
report =
    [ \"ptr \" ++ show (G.ptrStr True |> Result.map maybeStr) ++ \",\" ++ show (G.ptrStr False |> Result.map maybeStr)
    , \"ptrLen \" ++ show (G.ptrLen (Just \"abc\") |> Result.map String.fromInt) ++ \",\" ++ show (G.ptrLen Nothing |> Result.map String.fromInt)
    , \"ptrList \" ++ show (G.ptrList () |> Result.map (\\xs -> String.join \",\" (List.map (\\m -> Maybe.withDefault \"none\" (Maybe.map String.fromInt m)) xs)))
    , \"ptrCb \" ++ show (G.withPtrCb (\\m -> maybeStr m))
    , \"intMap \" ++ show (G.intMap () |> Result.map (\\d -> String.join \",\" (Dict.values d) ++ \" get1=\" ++ Maybe.withDefault \"none\" (Dict.get 1 d)))
    , \"sumKeys \" ++ show (G.sumKeys (Dict.fromList [ ( 3, \"c\" ), ( 4, \"d\" ) ]) |> Result.map String.fromInt)
    , \"big \" ++ show (G.big () |> Result.map String.fromInt)
    , \"small \" ++ show (G.small64 () |> Result.map String.fromInt)
    , \"u8 \" ++ show (G.takeU8 200 |> Result.map String.fromInt) ++ \",\" ++ show (G.takeU8 300 |> Result.map String.fromInt)
    , \"i8 \" ++ show (G.echo8 -129 |> Result.map String.fromInt)
    , \"cbBig \" ++ show (G.cbBig (\\n -> String.fromInt n))
    , \"apply \" ++ show (G.apply (\\n -> String.fromInt (n * 2)))
    , \"desc \" ++ show (G.describeValue \"s\")
    , \"writeTo \" ++ show (G.mkThing () |> Result.andThen (\\t -> G.writeTo t \"x\") |> Result.map (\\_ -> \"ok\"))
    , \"callTwice \" ++ show (G.callTwice (\\_ -> ()) |> Result.map String.fromInt)
    , \"hash \" ++ show (G.hash \"ab\" |> Result.map (\\b -> String.fromInt (String.length b)))
    , \"hexOf \" ++ show (G.hexOf \"wxyz\") ++ \",\" ++ show (G.hexOf \"abc\")
    , \"t \" ++ show (G.mkT () |> Result.andThen G.readT |> Result.map String.fromInt)
    , \"thing \" ++ show (G.mkThing () |> Result.andThen G.thingN2 |> Result.map String.fromInt)
    ]


main =
    println (String.join \"\\n\" report)
";

/// C-4, C-5, C-6, C-7, C-12 at run time: every value converts, and every
/// value that cannot is an `Err` the program handles — no panic, no wrapped
/// or truncated number, no empty Dict.
#[test]
fn format3_go_values_convert_or_are_err() {
    if !required(Need::Go, go_on_path()) {
        return;
    }
    let app = gopk_project("f3-runs", &format!("{GOPK_HEAD}{GOPK_RUNS}"));
    let (ok, log) = run(&app, SKY, &["install"]);
    assert!(ok, "sky install (path dep) failed:\n{log}");
    let kj = std::fs::read_to_string(app.join("sky-ffi/gopk.kernel.json")).unwrap();
    assert!(kj.contains("\"surfaceFormat\": 3"), "{kj}");
    for want in [
        "\"skyType\": \"Bool -> Result Error (Maybe String)\"",
        "\"skyType\": \"() -> Result Error (Dict Int String)\"",
        "\"skyType\": \"() -> Result Error go@Example.Com.Gopk.Thing\"",
        "\"skyType\": \"(Int -> String) -> Result Error String\"",
        "\"skyType\": \"(() -> ()) -> Result Error Int\"",
        "\"skyType\": \"any -> Result Error String\"",
        "\"skyType\": \"goi@Io.Writer -> String -> Result Error ()\"",
        "\"skyType\": \"go@Example.Com.Gopk.T -> Result Error Int\"",
    ] {
        assert!(kj.contains(want), "missing {want} in:\n{kj}");
    }
    let (ok, log) = run(&app, SKY, &["build", "src/Main.sky"]);
    assert!(ok, "sky build failed:\n{log}");
    let bin = app.join("sky-out").join("app");
    let (ok, out) = run(&app, bin.to_str().unwrap(), &[]);
    assert!(ok, "the app must run without a panic:\n{out}");
    assert!(
        !out.contains("panic:"),
        "no conversion may surface as a panic:\n{out}"
    );
    let line = |p: &str| -> String {
        out.lines()
            .find(|l| l.starts_with(p))
            .unwrap_or_else(|| panic!("no `{p}` line in:\n{out}"))
            .to_string()
    };
    assert_eq!(line("ptr "), "ptr Just x,Nothing");
    assert_eq!(line("ptrLen "), "ptrLen 3,-1");
    assert_eq!(line("ptrList "), "ptrList 1,none");
    assert_eq!(line("ptrCb "), "ptrCb Just y|Nothing");
    assert_eq!(line("intMap "), "intMap a,b get1=a");
    assert_eq!(line("sumKeys "), "sumKeys 7");
    assert!(line("big ").contains("err:") && line("big ").contains("out of range for Int"));
    assert_eq!(line("small "), "small 7");
    let u8l = line("u8 ");
    assert!(
        u8l.starts_with("u8 200,err:") && u8l.contains("300 is out of range for uint8"),
        "{u8l}"
    );
    assert!(
        line("i8 ").contains("-129 is out of range for int8"),
        "{}",
        line("i8 ")
    );
    assert!(
        line("cbBig ").contains("out of range for Int"),
        "{}",
        line("cbBig ")
    );
    assert_eq!(line("apply "), "apply 6");
    assert_eq!(line("desc "), "desc string");
    assert!(
        line("writeTo ").contains("does not implement io.Writer"),
        "{}",
        line("writeTo ")
    );
    assert_eq!(line("callTwice "), "callTwice 2");
    assert_eq!(line("hash "), "hash 32");
    let hex = line("hexOf ");
    assert!(
        hex.starts_with("hexOf 7778797a,err:") && hex.contains("needs exactly 4 bytes"),
        "{hex}"
    );
    assert_eq!(line("t "), "t 5");
    assert_eq!(line("thing "), "thing 6");
    let _ = std::fs::remove_dir_all(app.parent().unwrap());
}

/// Each compile-time class is a type error with its migration hint:
/// (C-6) an opaque Go value used as an Int; (C-12) a callback returning the
/// wrong type; (C-4) a Go pointer read as its target; ([E2012]) a Sky value
/// passed where a Go interface is required.
#[test]
fn format3_misuse_is_a_type_error_with_a_migration_hint() {
    if !required(Need::Go, go_on_path()) {
        return;
    }
    let app = gopk_project(
        "f3-reject",
        &format!("{GOPK_HEAD}main =\n    println \"x\"\n"),
    );
    let (ok, log) = run(&app, SKY, &["install"]);
    assert!(ok, "sky install (path dep) failed:\n{log}");
    for (body, code, anchor) in [
        (
            "asInt : Int\nasInt =\n    case G.mkThing () of\n        Ok t ->\n            t\n\n        Err _ ->\n            0\n\n\nmain =\n    println (String.fromInt asInt)\n",
            "[E2001]",
            "#ffi-opaque-go-types",
        ),
        (
            "main =\n    println (show (G.apply (\\n -> n * 2)))\n",
            "[E2001]",
            "#ffi-callback-result",
        ),
        (
            "name : String\nname =\n    case G.ptrStr True of\n        Ok s ->\n            s\n\n        Err _ ->\n            \"\"\n\n\nmain =\n    println name\n",
            "[E2001]",
            "#ffi-pointer-is-maybe",
        ),
        (
            "main =\n    println (show (G.writeTo \"not a writer\" \"x\" |> Result.map (\\_ -> \"ok\")))\n",
            "[E2012]",
            "#ffi-go-interface-params",
        ),
    ] {
        std::fs::write(app.join("src/Main.sky"), format!("{GOPK_HEAD}{body}")).unwrap();
        let (ok, log) = run(&app, SKY, &["check", "src/Main.sky"]);
        assert!(!ok, "sky check must reject:\n{body}\n{log}");
        assert!(log.contains(code), "want {code}:\n{log}");
        assert!(
            log.contains("v0.27.0") && log.contains(&format!("see docs/migration/v0.27.md{anchor}")),
            "the diagnostic must name the change and link {anchor}:\n{log}"
        );
    }
    let _ = std::fs::remove_dir_all(app.parent().unwrap());
}

/// A surface generated before format 3 is refused only for a binding its old
/// wrapper converts unsoundly, with the `sky install` fix; a binding that
/// passes only strings, ints, floats and bools still builds, with a warning.
#[test]
fn an_old_surface_is_refused_only_where_a_binding_needs_format3() {
    if !required(Need::Go, go_on_path()) {
        return;
    }
    let app = gopk_project(
        "f3-old",
        &format!("{GOPK_HEAD}main =\n    println (show (G.apply (\\n -> String.fromInt n)))\n"),
    );
    // A format-2 surface, as an older sky wrote it.
    std::fs::create_dir_all(app.join("sky-ffi/go")).unwrap();
    std::fs::write(
        app.join("sky-ffi/gopk.kernel.json"),
        "{\n  \"moduleName\": \"Example.Com.Gopk\",\n  \"kernelName\": \"Go_Gopk\",\n  \
         \"package\": \"example.com/gopk\",\n  \"surfaceFormat\": 2,\n  \"functions\": [\n    \
         {\"name\": \"readT\", \"arity\": 1, \"skyType\": \"T@example.com/gopk -> Result Error Int\"},\n    \
         {\"name\": \"ptrStr\", \"arity\": 1, \"skyType\": \"Bool -> Result Error String\"},\n    \
         {\"name\": \"takeU8\", \"arity\": 1, \"skyType\": \"Int -> Result Error Int\"},\n    \
         {\"name\": \"hexOf\", \"arity\": 1, \"skyType\": \"Bytes -> Result Error String\"},\n    \
         {\"name\": \"apply\", \"arity\": 1, \"skyType\": \"(Int -> String) -> Result Error String\"},\n    \
         {\"name\": \"ptrLen\", \"arity\": 1, \"skyType\": \"String -> Result Error Int\"}\n  ]\n}\n",
    )
    .unwrap();
    std::fs::write(
        app.join("sky-ffi/go/gopk_bindings.go"),
        "// Code generated by sky-ffi-inspect from example.com/gopk. DO NOT EDIT.\n\
         // Surface format 2 (sky install regenerates a surface stamped otherwise).\n\n\
         package skyffi\n\nimport (\n\t. \"sky-app/rt\"\n\tpkg \"example.com/gopk\"\n\t\"fmt\"\n)\n\n\
         func Go_Gopk_applyT(arg0 func(int) string) (out SkyResult[any, string]) {\n\
         \tdefer SkyFfiRecoverT(&out)()\n\tout = Ok[any,string](pkg.Apply(arg0))\n\treturn\n}\n\n\
         func Go_Gopk_ptrStrT(arg0 bool) (out SkyResult[any, *string]) {\n\
         \tdefer SkyFfiRecoverT(&out)()\n\tout = Ok[any,*string](pkg.PtrStr(arg0))\n\treturn\n}\n\n\
         func Go_Gopk_takeU8T(arg0 uint8) (out SkyResult[any, int]) {\n\
         \tdefer SkyFfiRecoverT(&out)()\n\tout = Ok[any,int](pkg.TakeU8(arg0))\n\treturn\n}\n\n\
         var _ = fmt.Sprintf\n",
    )
    .unwrap();
    // `apply` takes a Go func, which the old wrapper narrows outside its
    // recover and whose result it never typed: refused, with the fix named.
    let (ok, log) = run(&app, SKY, &["check", "src/Main.sky"]);
    assert!(!ok, "a stale binding must be refused:\n{log}");
    assert!(
        log.contains("predates surface format 3")
            && log.contains("sky install")
            && log.contains("see docs/migration/v0.27.md#ffi-surface-format-3"),
        "the refusal must name the fix:\n{log}"
    );
    assert!(
        log.contains("surface format 2"),
        "the outdated surface is also reported as a warning:\n{log}"
    );
    // An all-native binding of the same old surface still builds.
    std::fs::write(
        app.join("sky-ffi/go/gopk_bindings.go"),
        "// Code generated by sky-ffi-inspect from example.com/gopk. DO NOT EDIT.\n\
         // Surface format 2 (sky install regenerates a surface stamped otherwise).\n\n\
         package skyffi\n\nimport (\n\t. \"sky-app/rt\"\n\tpkg \"example.com/gopk\"\n\t\"fmt\"\n)\n\n\
         func Go_Gopk_ptrLenT(arg0 string) (out SkyResult[any, int]) {\n\
         \tdefer SkyFfiRecoverT(&out)()\n\ts := arg0\n\tout = Ok[any,int](pkg.PtrLen(&s))\n\treturn\n}\n\n\
         var _ = fmt.Sprintf\n",
    )
    .unwrap();
    std::fs::write(
        app.join("src/Main.sky"),
        format!("{GOPK_HEAD}main =\n    println (show (G.ptrLen \"abcd\" |> Result.map String.fromInt))\n"),
    )
    .unwrap();
    let (ok, log) = run(&app, SKY, &["build", "src/Main.sky"]);
    assert!(
        ok,
        "an all-native binding of an old surface still builds:\n{log}"
    );
    assert!(
        log.contains("surface format 2"),
        "with the outdated-surface warning:\n{log}"
    );
    let _ = std::fs::remove_dir_all(app.parent().unwrap());
}

//! Generated typed `Codec.auto` codecs (v0.29.9, `lower/auto_codec.rs` and
//! `runtime-go/rt/codec_auto_gen.go`), proved on a compiled program.
//!
//! The fixture `tests/fixtures/auto-codec` declares every shape of the closed
//! set. The test builds it, runs it (a `Codec.toJson` / `Codec.fromJson` round
//! trip on the generated path), then runs `gotest/auto_codec_test.go` inside
//! the emitted Go package: seeded random models and mutated documents (type
//! flips, missing fields, null, 1.0, huge ints, duplicate keys, invalid UTF-8,
//! U+2028, HTML characters, -0, 1e21) must give the reflective walker's tree,
//! bytes, value and error string exactly, and a short native Go fuzz run of
//! the decoder must find no difference.
//!
//! Needs a `go` toolchain.

use std::path::{Path, PathBuf};
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

fn copy_dir(from: &Path, to: &Path) {
    std::fs::create_dir_all(to).unwrap();
    for entry in std::fs::read_dir(from).unwrap() {
        let entry = entry.unwrap();
        let dest = to.join(entry.file_name());
        if entry.file_type().unwrap().is_dir() {
            copy_dir(&entry.path(), &dest);
        } else {
            std::fs::copy(entry.path(), dest).unwrap();
        }
    }
}

fn stage_fixture() -> PathBuf {
    let fixture = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/auto-codec");
    let dir = std::env::temp_dir().join(format!(
        "sky-auto-codec-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    copy_dir(&fixture, &dir);
    dir
}

fn run(cmd: &mut Command, what: &str) -> String {
    let out = cmd
        .stdin(std::process::Stdio::null())
        .output()
        .unwrap_or_else(|e| panic!("spawn {what}: {e}"));
    let mut log = String::from_utf8_lossy(&out.stdout).into_owned();
    log.push_str(&String::from_utf8_lossy(&out.stderr));
    assert!(out.status.success(), "{what} failed:\n{log}");
    log
}

#[test]
fn generated_codecs_match_the_reflective_walker() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = stage_fixture();
    run(
        Command::new(SKY)
            .args(["build", "src/Main.sky"])
            .current_dir(&dir),
        "sky build",
    );
    let main_go = std::fs::read_to_string(dir.join("sky-out/main.go")).unwrap();
    assert!(
        main_go.contains("rt.RegisterAutoCodec[Main_Model_R](true")
            && main_go.contains("rt.RegisterAutoCodec[Main_Model_R](false"),
        "the fixture's Model must get generated codecs for both key styles"
    );
    assert!(
        !main_go.contains("rt.RegisterAutoCodec[Main_Mixed_R]"),
        "a record with a tuple must keep the reflective path"
    );

    let stdout = run(
        Command::new(dir.join("sky-out/app")).current_dir(&dir),
        "the built app",
    );
    let lines: Vec<&str> = stdout.lines().collect();
    assert_eq!(lines.last().copied(), Some("round trip ok"), "{stdout}");
    assert!(
        lines[0].starts_with(r#"{"items":[{"title":"a \u003cb\u003e""#)
            // A Dict Int key is its decimal (v0.29.9), never the runtime's
            // stored key (`"\u0001i10"`), on the generated path.
            && lines[0].contains(r#""by_num":{"10":"ten","2":"two"}"#)
            && !lines[0].contains(r#"\u0001"#)
            && lines[0].contains(r#""nested":3"#)
            && lines[0].contains(r#""price_minor":0"#)
            && lines[1].contains(r#""priceMinor":0"#)
            && lines[2] == r#"{"pair":{"v0":1,"v1":"one"},"count":2}"#,
        "{stdout}"
    );

    std::fs::copy(
        dir.join("gotest/auto_codec_test.go"),
        dir.join("sky-out/auto_codec_test.go"),
    )
    .unwrap();
    let out_dir = dir.join("sky-out");
    run(
        Command::new("go")
            .args(["test", "-count=1", "-timeout=600s", "-run", "Test", "."])
            .current_dir(&out_dir),
        "go test (differential)",
    );
    run(
        Command::new("go")
            .args([
                "test",
                "-count=1",
                "-timeout=600s",
                "-run",
                "^$",
                "-fuzz",
                "^FuzzGeneratedDecode$",
                "-fuzztime",
                "15s",
                "-parallel",
                "2",
                ".",
            ])
            .current_dir(&out_dir),
        "go test (native fuzz of the decoder)",
    );
    let _ = std::fs::remove_dir_all(&dir);
}

//! `sky test --coverage` and `sky coverage report`, end to end.
//!
//! The fixture's `Shape.describe` has a two-arm `case`. The suite exercises
//! the `Circle` arm only. `twice` is reachable (the `Square` arm calls it) but
//! never runs, and `area` is reached by no code path of the suite. The report
//! must show exactly one arm hit and one missed, `describe` hit, `twice` and
//! `area` not hit, and only the app module unless `--all`. Needs a `go`
//! toolchain (`go build -cover`, `go tool covdata`).

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
    let fixture = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/coverage-arms");
    let dir = std::env::temp_dir().join(format!(
        "sky-coverage-flow-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    copy_dir(&fixture, &dir);
    dir
}

fn sky(dir: &Path, args: &[&str]) -> (bool, String, String) {
    let out = Command::new(SKY)
        .args(args)
        .current_dir(dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky");
    (
        out.status.success(),
        String::from_utf8_lossy(&out.stdout).into_owned(),
        String::from_utf8_lossy(&out.stderr).into_owned(),
    )
}

#[test]
fn coverage_reports_functions_and_case_arms_per_module() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = stage_fixture();

    let (ok, out, err) = sky(&dir, &["test", "tests/ShapeTest.sky", "--coverage"]);
    assert!(ok, "the suite must pass:\n{out}\n{err}");
    assert!(out.contains("1 passed, 0 failed"), "{out}\n{err}");
    assert!(
        err.contains("coverage: 1 module(s), functions 1/3 (33.3%), case arms 1/2 (50.0%)"),
        "summary line:\n{err}"
    );
    assert!(dir.join(".sky-coverage/runs").is_dir());

    let (ok, json, err) = sky(
        &dir,
        &["coverage", "report", "--by-module", "--format", "json"],
    );
    assert!(ok, "{err}");
    let v: serde_json::Value = serde_json::from_str(&json).expect("report is JSON");
    assert_eq!(v["version"], 1);
    assert_eq!(v["runs"], 1);
    let modules = v["modules"].as_array().unwrap();
    assert_eq!(modules.len(), 1, "only the app module by default: {json}");
    let m = &modules[0];
    assert_eq!(m["module"], "Shape");
    assert_eq!(m["file"], "src/Shape.sky");
    assert_eq!(m["source"], "app");
    assert_eq!(
        (&m["functionsHit"], &m["functionsTotal"]),
        (&serde_json::json!(1), &serde_json::json!(3))
    );
    assert_eq!(
        (&m["armsHit"], &m["armsTotal"]),
        (&serde_json::json!(1), &serde_json::json!(2))
    );
    let functions = m["functions"].as_array().unwrap();
    let by_name = |n: &str| functions.iter().find(|f| f["name"] == n).unwrap().clone();
    let describe = by_name("describe");
    assert_eq!(describe["hit"], true);
    assert_eq!(describe["line"], 12);
    assert_eq!(
        describe["arms"],
        serde_json::json!([
            { "index": 0, "line": 15, "hit": true },
            { "index": 1, "line": 18, "hit": false }
        ]),
        "exactly one arm hit and one missed"
    );
    assert_eq!(by_name("twice")["hit"], false);
    assert_eq!(by_name("area")["hit"], false);
    assert_eq!(by_name("twice")["arms"], serde_json::json!([]));

    // The JSON shape is stable: these keys, no more, no fewer.
    let keys =
        |o: &serde_json::Value| -> Vec<String> { o.as_object().unwrap().keys().cloned().collect() };
    assert_eq!(
        keys(&v),
        [
            "armsHit",
            "armsTotal",
            "functionsHit",
            "functionsTotal",
            "modules",
            "runs",
            "version"
        ]
    );
    assert_eq!(
        keys(m),
        [
            "armsHit",
            "armsTotal",
            "file",
            "functions",
            "functionsHit",
            "functionsTotal",
            "module",
            "source"
        ]
    );
    assert_eq!(keys(&describe), ["arms", "hit", "line", "name"]);
    assert_eq!(keys(&describe["arms"][0]), ["hit", "index", "line"]);

    // The same report twice is the same bytes.
    let (_, again, _) = sky(&dir, &["coverage", "report", "--format", "json"]);
    assert_eq!(json, again);

    // Text: the missed arm and the functions that never ran are named.
    let (ok, text, err) = sky(&dir, &["coverage", "report"]);
    assert!(ok, "{err}");
    assert!(
        text.contains("src/Shape.sky:12 describe  ran  arms not run: #1 line 18"),
        "{text}"
    );
    assert!(text.contains("src/Shape.sky:23 twice  never ran"), "{text}");
    assert!(text.contains("src/Shape.sky:28 area  never ran"), "{text}");

    // `--all` adds the test suite and the stdlib functions the program ran.
    let (ok, all, err) = sky(&dir, &["coverage", "report", "--all", "--format", "json"]);
    assert!(ok, "{err}");
    let v: serde_json::Value = serde_json::from_str(&all).unwrap();
    let sources: Vec<&str> = v["modules"]
        .as_array()
        .unwrap()
        .iter()
        .map(|m| m["source"].as_str().unwrap())
        .collect();
    assert!(
        sources.contains(&"test") && sources.contains(&"stdlib"),
        "{sources:?}"
    );

    // A second `sky test --coverage` starts fresh: one run, not two.
    let (ok, _, err) = sky(&dir, &["test", "tests/ShapeTest.sky", "--coverage"]);
    assert!(ok, "{err}");
    let (_, json, _) = sky(&dir, &["coverage", "report", "--format", "json"]);
    let v: serde_json::Value = serde_json::from_str(&json).unwrap();
    assert_eq!(v["runs"], 1);

    // Two suites, two runs, merged by Sky identity: each suite hits one arm,
    // so together both arms and `twice` ran. `area` still never ran.
    let (ok, out, err) = sky(
        &dir,
        &[
            "test",
            "tests/ShapeTest.sky",
            "tests/SquareTest.sky",
            "--coverage=cov-two",
        ],
    );
    assert!(ok, "{out}\n{err}");
    assert!(
        err.contains("functions 2/3 (66.7%), case arms 2/2 (100.0%)"),
        "{err}"
    );
    let (_, json, _) = sky(&dir, &["coverage", "report", "cov-two", "--format", "json"]);
    let v: serde_json::Value = serde_json::from_str(&json).unwrap();
    assert_eq!(v["runs"], 2);
    assert_eq!(v["armsHit"], 2);
    let names: Vec<(&str, bool)> = v["modules"][0]["functions"]
        .as_array()
        .unwrap()
        .iter()
        .map(|f| (f["name"].as_str().unwrap(), f["hit"].as_bool().unwrap()))
        .collect();
    assert_eq!(
        names,
        [("describe", true), ("twice", true), ("area", false)]
    );

    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn a_report_with_no_data_says_how_to_make_it() {
    let dir = stage_fixture();
    let (ok, _, err) = sky(&dir, &["coverage", "report"]);
    assert!(!ok);
    assert!(err.contains("run `sky test --coverage` first"), "{err}");
    let _ = std::fs::remove_dir_all(&dir);
}

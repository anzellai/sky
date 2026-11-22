//! Std.I18n catalogues are checked by `sky check` (v0.31.0).
//!
//! `sky check` (≡ `sky build`) reads `[i18n]` in sky.toml and every catalogue
//! under its folder, through the runtime's own checker
//! (`runtime-go/rt/i18n/skyi18ncheck`, project/src/i18n.rs). Each case below is
//! one project with one defect and asserts the code the check reports, its
//! location, and that the build stops. The good project asserts the build
//! passes, embeds the catalogue and records the CLDR build tags of its
//! locales.
//!
//! The checker is Go, so every case needs a Go toolchain and fails, never
//! skips, without one (live_gate.rs).

use std::path::{Path, PathBuf};
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

const MAIN: &str = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.I18n as I18n
import Std.Log exposing (println)


main =
    println (I18n.t I18n.defaultLocale "shop.title" [])
"#;

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
    let dir = std::env::temp_dir().join(format!(
        "sky-i18n-flow-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(dir.join("src")).unwrap();
    dir
}

/// A project: the [i18n] section text and catalogue files (path → content).
fn project(tag: &str, i18n: &str, files: &[(&str, &str)]) -> PathBuf {
    let dir = scratch(tag);
    std::fs::write(
        dir.join("sky.toml"),
        format!(
            "name = \"i18nflow\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n\n{i18n}"
        ),
    )
    .unwrap();
    std::fs::write(dir.join("src").join("Main.sky"), MAIN).unwrap();
    for (path, text) in files {
        let p = dir.join(path);
        std::fs::create_dir_all(p.parent().unwrap()).unwrap();
        std::fs::write(p, text).unwrap();
    }
    dir
}

fn check(dir: &Path) -> (bool, String) {
    let out = Command::new(SKY)
        .args(["check", "src/Main.sky"])
        .current_dir(dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky check");
    let mut s = String::from_utf8_lossy(&out.stdout).into_owned();
    s.push_str(&String::from_utf8_lossy(&out.stderr));
    (out.status.success(), s)
}

const TWO: &str = "[i18n]\nlocales = [\"en-GB\", \"fr\"]\ndefault = \"en-GB\"\n";
const EN_GB: &str = r#"{ "shop": { "title": "Shop", "greet": "Hello {name}" } }"#;

/// (name, [i18n] section, files, code, a part of the location, a part of the message)
type Case = (
    &'static str,
    &'static str,
    &'static [(&'static str, &'static str)],
    &'static str,
    &'static str,
    &'static str,
);

const CASES: &[Case] = &[
    (
        "unknown key",
        "[i18n]\nlocales = [\"en-GB\"]\ndefault = \"en-GB\"\nlocale = \"en\"\n",
        &[("i18n/en-GB/a.json", EN_GB)],
        "E5001",
        "sky.toml:",
        "no key `locale`",
    ),
    (
        "default not listed",
        "[i18n]\nlocales = [\"en-GB\"]\ndefault = \"fr\"\n",
        &[("i18n/en-GB/a.json", EN_GB)],
        "E5001",
        "sky.toml",
        "not in locales",
    ),
    (
        "unknown locale",
        "[i18n]\nlocales = [\"en-GB\", \"tlh\"]\ndefault = \"en-GB\"\n",
        &[("i18n/en-GB/a.json", EN_GB)],
        "E5002",
        "sky.toml",
        "no locale data",
    ),
    (
        "invalid JSON",
        TWO,
        &[
            ("i18n/en-GB/a.json", EN_GB),
            ("i18n/fr/a.json", "{ \"shop.title\": \"Boutique\", }"),
        ],
        "E5003",
        "i18n/fr/a.json",
        "not valid JSON",
    ),
    (
        "duplicate key",
        TWO,
        &[
            ("i18n/en-GB/a.json", EN_GB),
            ("i18n/fr/a.json", "{ \"shop.title\": \"A\" }"),
            ("i18n/fr/b.json", "{ \"shop\": { \"title\": \"B\" } }"),
        ],
        "E5003",
        "i18n/fr/",
        "defined twice",
    ),
    (
        "message syntax",
        TWO,
        &[
            ("i18n/en-GB/a.json", EN_GB),
            (
                "i18n/fr/a.json",
                "{\n  \"shop.greet\": \"Bonjour {name\"\n}",
            ),
        ],
        "E5004",
        "i18n/fr/a.json:2",
        "message \"shop.greet\"",
    ),
    (
        "unknown function",
        TWO,
        &[
            ("i18n/en-GB/a.json", EN_GB),
            (
                "i18n/fr/a.json",
                "{ \"shop.greet\": \"Bonjour {$name :datetime}\" }",
            ),
        ],
        "E5005",
        "i18n/fr/a.json",
        "unknown function :datetime",
    ),
    (
        "key not in the default locale",
        TWO,
        &[
            ("i18n/en-GB/a.json", EN_GB),
            ("i18n/fr/a.json", "{ \"shop.extra\": \"En plus\" }"),
        ],
        "E5006",
        "i18n/fr/a.json",
        "not in the default locale en-GB",
    ),
    (
        "unknown placeholder",
        TWO,
        &[
            ("i18n/en-GB/a.json", EN_GB),
            ("i18n/fr/a.json", "{ \"shop.greet\": \"Bonjour {nom}\" }"),
        ],
        "E5007",
        "i18n/fr/a.json",
        "{nom}",
    ),
    (
        "plural categories",
        "[i18n]\nlocales = [\"en-GB\", \"ar\"]\ndefault = \"en-GB\"\n",
        &[
            (
                "i18n/en-GB/a.json",
                r#"{ "shop.title": "Shop", "n": { "one": "{count} item", "other": "{count} items" } }"#,
            ),
            (
                "i18n/ar/a.json",
                r#"{ "n": { "one": "عنصر", "other": "{count}" } }"#,
            ),
        ],
        "E5008",
        "i18n/ar/a.json",
        "zero, two, few, many",
    ),
];

#[test]
fn every_catalogue_defect_fails_check_with_its_code() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let mut failures = Vec::new();
    for (i, (name, section, files, code, loc, text)) in CASES.iter().enumerate() {
        let dir = project(&format!("case{i}"), section, files);
        let (ok, log) = check(&dir);
        if ok {
            failures.push(format!("{name}: sky check passed; want {code}\n{log}"));
        } else if !log.contains(&format!("[{code}]")) {
            failures.push(format!("{name}: no [{code}] in the output\n{log}"));
        } else if !log.contains(loc) {
            failures.push(format!("{name}: the location {loc} is missing\n{log}"));
        } else if !log.contains(text) {
            failures.push(format!("{name}: the message lacks {text:?}\n{log}"));
        } else if !log.contains("docs/errors/i18n-catalogues.md#") {
            failures.push(format!("{name}: no link to the error page\n{log}"));
        }
        let _ = std::fs::remove_dir_all(&dir);
    }
    assert!(failures.is_empty(), "{}", failures.join("\n\n"));
    println!("ASSERTIONS: {}", CASES.len());
}

#[test]
fn a_good_catalogue_builds_with_its_cldr_tags() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = project(
        "good",
        "[i18n]\nlocales = [\"en-GB\", \"zh-Hans\"]\ndefault = \"en-GB\"\n",
        &[
            ("i18n/en-GB/a.json", EN_GB),
            ("i18n/zh-Hans/a.json", r#"{ "shop.title": "商店" }"#),
        ],
    );
    let out = Command::new(SKY)
        .args(["build", "src/Main.sky"])
        .current_dir(&dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky build");
    let log = format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    assert!(out.status.success(), "build failed:\n{log}");
    // Coverage: zh-Hans has one of the default locale's two keys.
    assert!(
        log.contains("zh-Hans has 1 of 2 keys"),
        "no coverage line:\n{log}"
    );
    let tags = std::fs::read_to_string(dir.join("sky-out").join("sky-build-tags.txt")).unwrap();
    for t in [
        "sky_i18n",
        "sky_cldr_en_GB",
        "sky_cldr_en_001",
        "sky_cldr_zh_Hans",
        "sky_cldr_zh",
    ] {
        assert!(tags.lines().any(|l| l == t), "tag {t} missing: {tags}");
    }
    let gen = std::fs::read_to_string(dir.join("sky-out").join("i18n_catalogue_gen.go")).unwrap();
    assert!(gen.contains("skyi18n.Install("), "{gen}");
    let run = Command::new(dir.join("sky-out").join("app"))
        .current_dir(&dir)
        .output()
        .expect("run app");
    assert_eq!(
        String::from_utf8_lossy(&run.stdout).trim(),
        "Shop",
        "the binary renders from the embedded catalogue"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn a_program_that_does_not_use_i18n_gets_no_tag() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = scratch("none");
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"plain\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
    )
    .unwrap();
    std::fs::write(
        dir.join("src").join("Main.sky"),
        "module Main exposing (main)\n\nimport Sky.Core.Prelude exposing (..)\nimport Std.Log exposing (println)\n\n\nmain =\n    println \"hi\"\n",
    )
    .unwrap();
    let (ok, log) = check(&dir);
    assert!(ok, "{log}");
    assert!(
        !dir.join("sky-out").join("sky-build-tags.txt").exists()
            && !dir.join("sky-out").join("i18n_catalogue_gen.go").exists(),
        "a program that does not use Std.I18n must not compile it in"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

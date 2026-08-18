//! `sky config migrate` proven end-to-end on a fixture project through the REAL
//! parse + rewrite path: a legacy `sky.toml` + a minimal `Live.app` `Main.sky`
//! are written to a scratch dir, migrated, and the results asserted — the
//! rewritten `sky.toml` has zero legacy keys, the new `Main.sky` carries the
//! right builders in the right destinations, and a second `--check` is clean.
//!
//! Falsifier (declared for the harness): break the sky.toml line removal so a
//! migrated key survives → `migrate_apply_moves_every_legacy_key` reddens on
//! the "zero legacy keys remain" assertion.

use std::path::PathBuf;

use project::config_migrate::{self, Mode};

fn scratch(tag: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "sky-cfg-migrate-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(dir.join("src")).unwrap();
    dir
}

const LEGACY_TOML: &str = "name = \"shop\"\n\
     version = \"0.1.0\"\n\
     entry = \"src/Main.sky\"\n\
     \n\
     [source]\n\
     root = \"src\"\n\
     \n\
     [live]\n\
     port = 8000\n\
     store = \"sqlite\"\n\
     storePath = \"sessions.db\"\n\
     \n\
     [log]\n\
     format = \"json\"\n\
     level = \"warn\"\n\
     \n\
     [security]\n\
     csrf = false\n\
     \n\
     [database]\n\
     driver = \"sqlite\"\n\
     path = \"shop.db\"\n\
     maxOpenConns = 25\n";

const LEGACY_MAIN: &str = "module Main exposing (main)\n\
     \n\
     import Std.Live as Live\n\
     import Std.Ui as Ui\n\
     \n\
     main =\n\
     \x20   Live.app\n\
     \x20       (Live.config { init = init, update = update, view = view })\n";

#[test]
fn migrate_apply_moves_every_legacy_key() {
    let dir = scratch("apply");
    std::fs::write(dir.join("sky.toml"), LEGACY_TOML).unwrap();
    std::fs::write(dir.join("src/Main.sky"), LEGACY_MAIN).unwrap();

    // Before: legacy keys present.
    let before = config_migrate::run(&dir, Mode::Check).expect("check runs");
    assert!(!before.clean, "the fixture must start with legacy keys");
    assert!(before.legacy_count >= 6, "count={}", before.legacy_count);

    // Apply.
    let out = config_migrate::run(&dir, Mode::Apply).expect("apply runs");
    assert!(out.wrote, "apply must write");

    let new_toml = std::fs::read_to_string(dir.join("sky.toml")).unwrap();
    let new_main = std::fs::read_to_string(dir.join("src/Main.sky")).unwrap();

    // sky.toml: every migratable key is gone; residual keys survive.
    for legacy in ["port =", "store =", "storePath =", "format =", "level =", "csrf =", "path ="] {
        assert!(!new_toml.contains(legacy), "migrated `{legacy}` must be gone:\n{new_toml}");
    }
    assert!(new_toml.contains("maxOpenConns = 25"), "residual pool knob kept:\n{new_toml}");
    assert!(new_toml.contains("driver = \"sqlite\""), "residual driver kept:\n{new_toml}");
    assert!(new_toml.contains("[database]"), "[database] header kept (has residuals):\n{new_toml}");
    assert!(!new_toml.contains("[live]"), "emptied [live] dropped:\n{new_toml}");
    assert!(!new_toml.contains("[log]"), "emptied [log] dropped:\n{new_toml}");
    assert!(!new_toml.contains("[security]"), "emptied [security] dropped:\n{new_toml}");

    // Main.sky: Sky.Config binding created + exposed + imported.
    assert!(new_main.contains("module Main exposing (main, config)"), "{new_main}");
    assert!(new_main.contains("import Sky.Config as Config exposing ("), "{new_main}");
    assert!(new_main.contains("config : Config.Config"), "{new_main}");
    assert!(new_main.contains("Config.default"), "{new_main}");
    assert!(new_main.contains("|> Config.withLog Json Warn"), "{new_main}");
    assert!(new_main.contains("|> Config.withSessions (SessionsSqlite \"sessions.db\")"), "{new_main}");
    assert!(new_main.contains("|> Config.withDatabase (Sqlite \"shop.db\")"), "{new_main}");
    assert!(new_main.contains("|> Config.withCsrf False"), "{new_main}");
    // Live builder went into the Live.config pipeline, not the config binding.
    assert!(new_main.contains("|> Live.withPort 8000"), "{new_main}");

    // After: a re-check is clean.
    let after = config_migrate::run(&dir, Mode::Check).expect("re-check runs");
    assert!(after.clean, "after apply, no legacy key may remain");
    assert_eq!(after.legacy_count, 0);

    std::fs::remove_dir_all(&dir).ok();
}

#[test]
fn dry_run_shows_a_diff_and_writes_nothing() {
    let dir = scratch("dryrun");
    std::fs::write(dir.join("sky.toml"), LEGACY_TOML).unwrap();
    std::fs::write(dir.join("src/Main.sky"), LEGACY_MAIN).unwrap();

    let toml_before = std::fs::read_to_string(dir.join("sky.toml")).unwrap();
    let main_before = std::fs::read_to_string(dir.join("src/Main.sky")).unwrap();

    let out = config_migrate::run(&dir, Mode::DryRun).expect("dry-run runs");
    assert!(!out.wrote, "dry-run must not write");
    assert!(out.diff.contains("- port = 8000"), "diff shows the removed key:\n{}", out.diff);
    assert!(out.diff.contains("+ config : Config.Config"), "diff shows the new binding:\n{}", out.diff);

    // Files unchanged on disk.
    assert_eq!(std::fs::read_to_string(dir.join("sky.toml")).unwrap(), toml_before);
    assert_eq!(std::fs::read_to_string(dir.join("src/Main.sky")).unwrap(), main_before);

    std::fs::remove_dir_all(&dir).ok();
}

#[test]
fn a_clean_project_is_already_migrated() {
    let dir = scratch("clean");
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"x\"\nentry = \"src/Main.sky\"\n\n[database]\nmaxOpenConns = 25\nembedded = true\n",
    )
    .unwrap();
    std::fs::write(dir.join("src/Main.sky"), "module Main exposing (main)\n\nmain = run\n").unwrap();

    let out = config_migrate::run(&dir, Mode::Check).expect("check runs");
    assert!(out.clean, "pool knobs / embedded are residual, not legacy");
    assert_eq!(out.legacy_count, 0);

    std::fs::remove_dir_all(&dir).ok();
}

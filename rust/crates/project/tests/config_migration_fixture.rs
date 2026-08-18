//! The legacy-`sky.toml` → `withX` migration LIST, proven through the REAL
//! parse path (`read_sky_toml_config` → `present_runtime_config_keys` →
//! `config_migration::migration_hint`) on a fixture `sky.toml`, no Go toolchain
//! needed. The `config_migration` unit tests cover the mapping in isolation;
//! this file covers the other half — that a written `sky.toml` is parsed, its
//! runtime keys recognised, and the LIST produced — which the unit tests bypass.
//!
//! Design §8.2: the hint fires ONLY while a legacy key is present
//! (self-extinguishing), names each key's `withX` replacement, and is silent on
//! a clean project.
//!
//! Falsifier (declared for the harness): delete a row from
//! `project::config_migration::MIGRATIONS` and `a_legacy_project_lists_each_replacement`
//! reddens for that key.

use std::path::PathBuf;

fn scratch(tag: &str, sky_toml: &str) -> PathBuf {
    let uniq = format!(
        "sky-migration-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    );
    let dir = std::env::temp_dir().join(uniq);
    std::fs::create_dir_all(&dir).unwrap();
    std::fs::write(dir.join("sky.toml"), sky_toml).unwrap();
    dir
}

/// A project whose `sky.toml` still carries legacy runtime keys prints a LIST
/// naming each key's `withX` replacement — the primary deliverable.
#[test]
fn a_legacy_project_lists_each_replacement() {
    let dir = scratch(
        "legacy",
        "name = \"shop\"\n\
         entry = \"src/Main.sky\"\n\
         \n\
         [live]\n\
         store = \"postgres\"\n\
         storePath = \"sessions.db\"\n\
         \n\
         [log]\n\
         format = \"json\"\n\
         \n\
         [database]\n\
         path = \"app.db\"\n",
    );
    let hint = project::migration_hint_for(&dir).expect("legacy keys present → a LIST");

    // Header + the self-extinguishing framing.
    assert!(
        hint.contains("moved into typed app config"),
        "no moved-block header:\n{hint}"
    );
    // Each legacy key is named with its literal `[section] key = "value"` form.
    assert!(hint.contains("[live] store = \"postgres\""), "{hint}");
    assert!(hint.contains("[live] storePath = \"sessions.db\""), "{hint}");
    assert!(hint.contains("[log] format = \"json\""), "{hint}");
    assert!(hint.contains("[database] path = \"app.db\""), "{hint}");
    // Each names its `withX` replacement — the thing the user acts on.
    assert!(hint.contains("Sky.Config.withSessions"), "store→withSessions:\n{hint}");
    assert!(hint.contains("Sky.Config.withLog"), "format→withLog:\n{hint}");
    assert!(hint.contains("Sky.Config.withDatabase"), "path→withDatabase:\n{hint}");

    std::fs::remove_dir_all(&dir).ok();
}

/// A fully-migrated (or never-legacy) project prints NOTHING — the silence that
/// makes the hint's presence meaningful (design §8.2). Only non-migratable,
/// legitimately-sky.toml-only keys are present here.
#[test]
fn a_clean_project_is_silent() {
    let dir = scratch(
        "clean",
        "name = \"shop\"\n\
         entry = \"src/Main.sky\"\n\
         \n\
         [database]\n\
         maxOpenConns = 25\n\
         embedded = true\n\
         postgresVersion = \"16.4\"\n\
         \n\
         [env]\n\
         prefix = \"SKY\"\n",
    );
    assert_eq!(
        project::migration_hint_for(&dir),
        None,
        "pool knobs / embedded / postgresVersion / env prefix are not migratable — must be silent"
    );
    std::fs::remove_dir_all(&dir).ok();
}

/// The three classes are visually distinct on a mixed project: a moved key says
/// "use X", a removed key says "delete", a default-changed key says "CHANGED
/// BEHAVIOUR" — a user must be able to tell them apart at a glance.
#[test]
fn moved_removed_and_changed_are_distinct() {
    let dir = scratch(
        "mixed",
        "name = \"shop\"\n\
         entry = \"src/Main.sky\"\n\
         \n\
         [live]\n\
         store = \"memory\"\n\
         ttl = \"30m\"\n\
         \n\
         [auth]\n\
         tokenTtl = \"24h\"\n",
    );
    let hint = project::migration_hint_for(&dir).expect("mixed keys present");

    assert!(hint.contains("moved into typed app config"), "moved block:\n{hint}");
    assert!(hint.contains("CHANGED BEHAVIOUR"), "changed block:\n{hint}");
    assert!(hint.contains("no longer"), "removed block:\n{hint}");
    // The removed key must be told to delete, not migrate to a builder.
    assert!(hint.contains("[auth] tokenTtl"), "{hint}");
    // The changed key names its builder and is not in the moved block's list.
    assert!(hint.contains("Live.withTtl"), "{hint}");

    std::fs::remove_dir_all(&dir).ok();
}

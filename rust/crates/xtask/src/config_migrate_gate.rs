//! `xtask config-migrate` — proves `sky config migrate` actually migrates: a
//! legacy fixture project is rewritten end-to-end (via the real
//! `project::config_migrate` path) and the result asserted, and a COPY of the
//! canonical multi-module Live app `examples/19-skyforum` is dry-planned to
//! prove the rewriter proposes ZERO undeclared changes on a real project.
//!
//! # What it proves
//!
//!   1. **Every legacy key leaves `sky.toml`.** After migrating the fixture, the
//!      rewritten `sky.toml` carries zero migration-table keys; residual
//!      sky.toml-only keys (pool knobs, driver) survive; emptied runtime
//!      sections are dropped.
//!   2. **The builders land in the right destination.** Cross-cutting settings
//!      become `Config.withX` in a created `config` binding; app-shape `[live]`
//!      settings become `<alias>.withX` in the `Live.config(…)` pipeline.
//!   3. **A re-check is clean** — the migration is idempotent-complete.
//!   4. **Zero undeclared changes on a real project.** Planning a migration of
//!      `examples/19-skyforum` succeeds (its `[live] port`/`input` both have
//!      migration rows) and writes nothing — the self-check oracle passing IS
//!      the "no undeclared move" guarantee.
//!
//! # The falsifier
//!
//! `examples/19-skyforum/sky.toml`'s `port = 8000` → `porto = 8000`: the real
//! project then carries only one migratable key, and clause 4's "the plan finds
//! both port and input" assertion reddens. The gate reads that file at run time
//! (it copies it), so the mutation is observable without a rebuild.

use std::path::{Path, PathBuf};

use project::config_migrate::{self, Mode};

fn scratch(tag: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "sky-cfgmig-gate-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    let _ = std::fs::create_dir_all(dir.join("src"));
    dir
}

const FIXTURE_TOML: &str = "name = \"shop\"\n\
     entry = \"src/Main.sky\"\n\
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

const FIXTURE_MAIN: &str = "module Main exposing (main)\n\
     \n\
     import Std.Live as Live\n\
     \n\
     main =\n\
     \x20   Live.app\n\
     \x20       (Live.config { init = init, update = update, view = view })\n";

/// Compute the verdict: `(passed, assertions, detail)`.
pub fn check_body(root: &Path) -> (bool, u64, String) {
    let mut a: u64 = 0;
    let mut fails: Vec<String> = Vec::new();
    let check = |cond: bool, msg: &str, a: &mut u64, fails: &mut Vec<String>| {
        *a += 1;
        if !cond {
            fails.push(msg.to_string());
        }
    };

    // ── clauses 1-3: the in-code fixture, migrated end-to-end ────────────────
    let dir = scratch("fixture");
    if std::fs::write(dir.join("sky.toml"), FIXTURE_TOML).is_err()
        || std::fs::write(dir.join("src/Main.sky"), FIXTURE_MAIN).is_err()
    {
        return (false, 0, format!("could not write the fixture under {}", dir.display()));
    }

    match config_migrate::run(&dir, Mode::Check) {
        Ok(o) => {
            check(!o.clean, "fixture must start with legacy keys", &mut a, &mut fails);
            check(o.legacy_count == 7, "fixture must have 7 legacy keys", &mut a, &mut fails);
        }
        Err(e) => {
            let _ = std::fs::remove_dir_all(&dir);
            return (false, a, format!("fixture Check failed: {e}"));
        }
    }

    match config_migrate::run(&dir, Mode::Apply) {
        Ok(o) => check(o.wrote, "apply must write", &mut a, &mut fails),
        Err(e) => {
            let _ = std::fs::remove_dir_all(&dir);
            return (false, a, format!("fixture Apply failed: {e}"));
        }
    }

    let new_toml = std::fs::read_to_string(dir.join("sky.toml")).unwrap_or_default();
    let new_main = std::fs::read_to_string(dir.join("src/Main.sky")).unwrap_or_default();

    check(
        !new_toml.contains("port =")
            && !new_toml.contains("store =")
            && !new_toml.contains("format =")
            && !new_toml.contains("csrf =")
            && !new_toml.contains("path ="),
        "every migrated key must leave sky.toml",
        &mut a,
        &mut fails,
    );
    check(new_toml.contains("maxOpenConns = 25"), "residual pool knob must survive", &mut a, &mut fails);
    check(new_toml.contains("driver = \"sqlite\""), "residual driver must survive", &mut a, &mut fails);
    check(!new_toml.contains("[live]"), "emptied [live] must drop", &mut a, &mut fails);
    check(new_toml.contains("[database]"), "[database] with residuals must stay", &mut a, &mut fails);

    check(new_main.contains("module Main exposing (main, config)"), "config must be exposed", &mut a, &mut fails);
    check(new_main.contains("import Sky.Config as Config exposing ("), "Sky.Config must be imported", &mut a, &mut fails);
    check(new_main.contains("config : Config.Config"), "a config binding must be created", &mut a, &mut fails);
    check(new_main.contains("|> Config.withLog Json Warn"), "withLog must be generated", &mut a, &mut fails);
    check(new_main.contains("|> Config.withSessions (SessionsSqlite \"sessions.db\")"), "withSessions must carry the path", &mut a, &mut fails);
    check(new_main.contains("|> Config.withDatabase (Sqlite \"shop.db\")"), "withDatabase must be generated", &mut a, &mut fails);
    check(new_main.contains("|> Config.withCsrf False"), "withCsrf must be generated", &mut a, &mut fails);
    check(new_main.contains("|> Live.withPort 8000"), "Live.withPort must land in the pipeline", &mut a, &mut fails);

    match config_migrate::run(&dir, Mode::Check) {
        Ok(o) => check(o.clean, "a re-check after apply must be clean", &mut a, &mut fails),
        Err(e) => fails.push(format!("re-check failed: {e}")),
    }
    let _ = std::fs::remove_dir_all(&dir);

    // ── clause 4: zero undeclared changes on a real project ──────────────────
    let forum = root.join("examples/19-skyforum");
    let forum_toml = forum.join("sky.toml");
    let forum_main = forum.join("src/Main.sky");
    if !forum_toml.exists() || !forum_main.exists() {
        fails.push(format!(
            "examples/19-skyforum is missing ({}) — the real-project clause cannot run",
            forum.display()
        ));
        return finish(a, fails);
    }
    let copy = scratch("forum");
    let _ = std::fs::copy(&forum_toml, copy.join("sky.toml"));
    let _ = std::fs::copy(&forum_main, copy.join("src/Main.sky"));
    let toml_before = std::fs::read_to_string(copy.join("sky.toml")).unwrap_or_default();

    match config_migrate::plan(&copy) {
        Ok(plan) => {
            // Every legacy key it would touch has a migration row — the plan
            // succeeding through the oracle IS "zero undeclared changes".
            let keys: Vec<(String, String)> = plan
                .legacy
                .iter()
                .map(|l| (l.section.clone(), l.key.clone()))
                .collect();
            check(
                keys.contains(&("live".into(), "port".into()))
                    && keys.contains(&("live".into(), "input".into())),
                "19-skyforum's [live] port + input must be recognised as migratable",
                &mut a,
                &mut fails,
            );
            check(
                plan.main_new.contains(".withPort 8000") && plan.main_new.contains(".withInput"),
                "the plan must generate the Live builders for 19-skyforum",
                &mut a,
                &mut fails,
            );
            // Dry planning writes nothing.
            let toml_after = std::fs::read_to_string(copy.join("sky.toml")).unwrap_or_default();
            check(toml_before == toml_after, "planning must not write", &mut a, &mut fails);
        }
        Err(e) => {
            a += 1;
            fails.push(format!("planning 19-skyforum failed (an undeclared/unsupported move?): {e}"));
        }
    }
    let _ = std::fs::remove_dir_all(&copy);

    finish(a, fails)
}

fn finish(a: u64, fails: Vec<String>) -> (bool, u64, String) {
    if fails.is_empty() {
        (
            true,
            a,
            format!("{a} assertions: fixture migrated end-to-end + 19-skyforum plans with 0 undeclared changes"),
        )
    } else {
        (false, a, fails.join("\n"))
    }
}

/// CLI face.
pub fn run(_args: &[String], repo_root: &Path) -> i32 {
    let (passed, assertions, detail) = check_body(repo_root);
    println!("xtask config-migrate — the sky config migrate rewriter, proven end-to-end\n");
    println!("{detail}\n");
    println!("  assertions: {assertions}");
    if passed {
        println!("\nxtask config-migrate: PASS");
        0
    } else {
        eprintln!("\nxtask config-migrate: FAIL");
        1
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn repo_root() -> PathBuf {
        PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../.."))
    }

    #[test]
    fn the_checked_in_tree_passes() {
        let (passed, assertions, detail) = check_body(&repo_root());
        assert!(passed, "config-migrate must pass on the tree:\n{detail}");
        assert!(assertions > 0, "a passing gate that asserted nothing is vacuous");
    }
}

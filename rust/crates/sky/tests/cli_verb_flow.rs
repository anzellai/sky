//! Member G of the Layer-2 corpus — the CLI verbs (`docs/ci-test-architecture-v2.md` §6, row G).
//!
//! **Why these are flow tests and not an app.** v2 §6: "Making a project
//! responsible for `sky doctor` couples a CLI verb's coverage to an app's build
//! health. Flow tests own the verbs directly, in-process, in seconds."
//!
//! **Why every assertion here is toolchain-free.** The existing `*_flow.rs`
//! tests early-`return` when `go` is absent from `PATH`, and CI's `test-rest`
//! job has no `actions/setup-go` step — so `db_flow`, `ffi_verb_flow`,
//! `profile_flow` and `doc_serve` all take that branch and report green having
//! asserted nothing. That is the "SKIP counted as pass" class living inside the
//! test suite itself. Everything below asserts on exit codes, usage text and
//! scaffolded files, so it has the same value on a bare CI runner as it does
//! locally.
//!
//! Verbs covered here: `init`, `clean`, `watch` (argument validation), `package` (the release and TestFlight-upload refusals), `config migrate`,
//! `install`, `update`, `upgrade`, `db` (dispatch + `init`), and unknown-verb
//! dispatch. `doctor` is owned by `doctor_flow.rs`, `doc` by `doc_flow.rs`,
//! `add`/`remove` by `ffi_verb_flow.rs`, `db migrate/push` by `db_flow.rs`.

use std::path::{Path, PathBuf};
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

/// Run `sky <args...>` in `dir` with stdin closed, so any interactive prompt
/// takes its non-TTY default. Returns (exit_code, stdout+stderr).
fn run_sky(dir: &Path, args: &[&str]) -> (i32, String) {
    let out = Command::new(SKY)
        .args(args)
        .current_dir(dir)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn sky");
    let mut s = String::from_utf8_lossy(&out.stdout).into_owned();
    s.push_str(&String::from_utf8_lossy(&out.stderr));
    (out.status.code().unwrap_or(-1), s)
}

/// A unique empty scratch directory. Tests must never inherit the runner's cwd:
/// `sky clean` is `remove_dir_all` on `sky-out`/`.skycache`/`.skydeps`/`dist`
/// relative to cwd, with no project-root check.
fn scratch(tag: &str) -> PathBuf {
    let uniq = format!(
        "sky-cli-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    );
    let dir = std::env::temp_dir().join(uniq);
    std::fs::create_dir_all(&dir).unwrap();
    dir
}

// ---------------------------------------------------------------------------
// init
// ---------------------------------------------------------------------------

#[test]
fn init_scaffolds_a_buildable_project_layout() {
    let dir = scratch("init");
    let (code, out) = run_sky(&dir, &["init", "myapp"]);
    assert_eq!(code, 0, "sky init should succeed; output:\n{out}");

    // The three files the verb's own help text promises unconditionally. The
    // rest of the scaffold (docker-compose.yml, .env.example, AGENTS.md,
    // CLAUDE.md) is deliberately NOT asserted: those are template-sourced and
    // documented as best-effort, and asserting them would make this test fail
    // for a reason that is not the verb's contract.
    for f in ["sky.toml", "src/Main.sky", ".gitignore"] {
        assert!(
            dir.join("myapp").join(f).is_file(),
            "sky init did not create {f}; output:\n{out}"
        );
    }

    // Whitespace-tolerant: the template aligns its `=` signs, so an exact
    // `entry = "..."` match is a test bug, not a scaffold bug.
    let toml = std::fs::read_to_string(dir.join("myapp/sky.toml")).unwrap();
    let declares_entry = toml.lines().any(|l| {
        let l = l.trim();
        !l.starts_with('#')
            && l.starts_with("entry")
            && l.contains('=')
            && l.contains("src/Main.sky")
    });
    assert!(
        declares_entry,
        "scaffolded sky.toml must point at the scaffolded entry; got:\n{toml}"
    );

    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn init_help_does_not_scaffold() {
    // Regression: `sky init --help` used to treat `--help` as the project name
    // and scaffold `./--help`.
    let dir = scratch("inithelp");
    let (code, out) = run_sky(&dir, &["init", "--help"]);
    assert_eq!(code, 0, "sky init --help should exit 0; output:\n{out}");
    assert!(out.contains("sky init"), "help text expected; got:\n{out}");

    let entries: Vec<_> = std::fs::read_dir(&dir).unwrap().flatten().collect();
    assert!(
        entries.is_empty(),
        "sky init --help must not scaffold anything, found: {:?}",
        entries.iter().map(|e| e.file_name()).collect::<Vec<_>>()
    );

    let _ = std::fs::remove_dir_all(&dir);
}

// ---------------------------------------------------------------------------
// clean
// ---------------------------------------------------------------------------

#[test]
fn clean_reports_and_removes_only_generated_dirs() {
    let dir = scratch("clean");

    // Nothing to do — and it must say so rather than claiming a removal.
    let (code, out) = run_sky(&dir, &["clean"]);
    assert_eq!(code, 0, "clean on an empty dir should succeed; got:\n{out}");
    assert!(
        out.contains("nothing to remove"),
        "expected the no-op message; got:\n{out}"
    );

    // Generated dirs go; a source dir and a file must survive. `sky clean` has
    // no project-root guard, so "removes ONLY the generated set" is the whole
    // safety property.
    std::fs::create_dir_all(dir.join("sky-out")).unwrap();
    std::fs::create_dir_all(dir.join(".skycache")).unwrap();
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(dir.join("src/Main.sky"), "module Main exposing (main)\n").unwrap();
    std::fs::write(dir.join("sky.toml"), "name = \"x\"\n").unwrap();

    let (code, out) = run_sky(&dir, &["clean"]);
    assert_eq!(code, 0, "clean should succeed; got:\n{out}");
    assert!(
        out.contains("removed") && out.contains("sky-out") && out.contains(".skycache"),
        "clean must name what it removed; got:\n{out}"
    );
    assert!(!dir.join("sky-out").exists(), "sky-out survived clean");
    assert!(!dir.join(".skycache").exists(), ".skycache survived clean");
    assert!(
        dir.join("src/Main.sky").is_file(),
        "clean deleted source — it must only remove generated trees"
    );
    assert!(dir.join("sky.toml").is_file(), "clean deleted sky.toml");

    let _ = std::fs::remove_dir_all(&dir);
}

// ---------------------------------------------------------------------------
// watch — argument validation only
// ---------------------------------------------------------------------------

#[test]
fn watch_rejects_bad_invocations_without_starting() {
    // `watch` is long-running by design (it exits only on Ctrl-C), so its
    // testable, non-daemon surface is argument validation. Both paths must exit
    // 2 — a usage error that exits 0 is how a mistyped CI step becomes a
    // permanently green no-op.
    let dir = scratch("watch");

    let (code, out) = run_sky(&dir, &["watch"]);
    assert_eq!(code, 2, "watch with no file must exit 2; got:\n{out}");
    assert!(
        out.contains("usage: sky watch"),
        "expected usage text; got:\n{out}"
    );

    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(dir.join("src/Main.sky"), "module Main exposing (main)\n").unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"w\"\nentry = \"src/Main.sky\"\n",
    )
    .unwrap();

    let (code, out) = run_sky(&dir, &["watch", "src/Main.sky", "--kill-timeout=abc"]);
    assert_eq!(
        code, 2,
        "a non-numeric --kill-timeout must exit 2 rather than silently defaulting; got:\n{out}"
    );
    assert!(
        out.contains("--kill-timeout"),
        "the diagnostic must name the offending flag; got:\n{out}"
    );

    let _ = std::fs::remove_dir_all(&dir);
}

// ---------------------------------------------------------------------------
// db — dispatch + the DB-free scaffold
// ---------------------------------------------------------------------------

#[test]
fn db_rejects_an_unknown_subcommand() {
    // `xtask` exiting 0 on an unknown subcommand made a typo'd CI gate a
    // permanently green no-op. The same property is asserted here for `sky db`.
    let dir = scratch("dbbad");
    let (code, out) = run_sky(&dir, &["db", "bogus"]);
    assert_eq!(
        code, 2,
        "unknown `sky db` subcommand must exit 2; got:\n{out}"
    );
    assert!(
        out.contains("usage: sky db"),
        "expected usage text; got:\n{out}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn db_init_scaffolds_the_file_based_migration_layout() {
    let dir = scratch("dbinit");
    let (code, out) = run_sky(&dir, &["init", "app"]);
    assert_eq!(code, 0, "sky init failed:\n{out}");
    let proj = dir.join("app");

    let (code, out) = run_sky(&proj, &["db", "init"]);
    assert_eq!(code, 0, "sky db init should succeed; got:\n{out}");
    assert!(
        proj.join("db/migrations").is_dir(),
        "sky db init must create db/migrations/; output:\n{out}"
    );

    let _ = std::fs::remove_dir_all(&dir);
}

// ---------------------------------------------------------------------------
// install / update / upgrade — the network-free paths
// ---------------------------------------------------------------------------

#[test]
fn install_and_update_are_clean_no_ops_without_dependencies() {
    // With no `["go.dependencies"]` both verbs must be network-free no-ops that
    // SAY they did nothing. Exiting 0 silently would be indistinguishable from
    // having installed something.
    let dir = scratch("install");
    let (code, out) = run_sky(&dir, &["init", "app"]);
    assert_eq!(code, 0, "sky init failed:\n{out}");
    let proj = dir.join("app");

    let (code, out) = run_sky(&proj, &["install"]);
    assert_eq!(
        code, 0,
        "install on an empty dep set should succeed; got:\n{out}"
    );
    assert!(
        out.contains("nothing to do"),
        "install must say it did nothing; got:\n{out}"
    );

    let (code, out) = run_sky(&proj, &["update"]);
    assert_eq!(
        code, 0,
        "update on an empty dep set should succeed; got:\n{out}"
    );
    assert!(
        out.contains("no declared surfaces"),
        "update must say it had nothing to regenerate; got:\n{out}"
    );

    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn upgrade_refuses_to_replace_a_dev_build() {
    // `sky upgrade` self-replaces the running binary, so the ONLY safe thing to
    // assert is the refusal path — which is also the one that matters: a dev
    // build silently overwriting itself with a published release mid-session
    // would swap the compiler under a running verification.
    let dir = scratch("upgrade");
    let (code, out) = run_sky(&dir, &["upgrade"]);

    if out.contains("dial tcp")
        || out.contains("no such host")
        || out.contains("network is unreachable")
        || out.contains("Temporary failure in name resolution")
    {
        required(Need::Network, false);
        let _ = std::fs::remove_dir_all(&dir);
        return;
    }

    assert_eq!(code, 0, "upgrade's refusal path should exit 0; got:\n{out}");
    assert!(
        out.contains("--force"),
        "the refusal must tell the operator how to override it; got:\n{out}"
    );
    assert!(
        out.contains("dev build") || out.contains("not a published release"),
        "the refusal must say WHY it refused; got:\n{out}"
    );

    let _ = std::fs::remove_dir_all(&dir);
}

// ---------------------------------------------------------------------------
// dispatch
// ---------------------------------------------------------------------------

#[test]
fn unknown_verb_exits_two() {
    let dir = scratch("unknown");
    let (code, out) = run_sky(&dir, &["bogusverb"]);
    assert_eq!(
        code, 2,
        "an unknown verb must exit non-zero — a CI step with a typo'd verb that \
         exits 0 is a permanently green no-op; got:\n{out}"
    );
    assert!(
        out.contains("unknown command"),
        "expected an unknown-command diagnostic; got:\n{out}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

// ---------------------------------------------------------------------------
// `--embed` is a build flag
// ---------------------------------------------------------------------------

/// `parse_out` swallows every flag it does not recognise, "for forward
/// compatibility". That makes a misplaced `--embed` on `sky run` a silent
/// no-op — the user asks for a self-contained database and gets an ordinary
/// build, with nothing said. Silently ignoring `--embed` is the precise failure
/// mode the flag exists to refuse, so `sky run` names the two things that do
/// work instead.
#[test]
fn embed_on_run_is_refused_and_points_at_what_does_work() {
    let dir = scratch("embed-on-run");
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"x\"\nentry = \"src/Main.sky\"\n",
    )
    .unwrap();
    std::fs::write(
        dir.join("src").join("Main.sky"),
        "module Main exposing (main)\n\nmain = ()\n",
    )
    .unwrap();

    let (code, out) = run_sky(&dir, &["run", "--embed", "src/Main.sky"]);
    assert_eq!(
        code, 2,
        "a misplaced --embed must not be swallowed; got:\n{out}"
    );
    assert!(
        out.contains("embedded = true"),
        "the refusal must name the sky.toml key that does work; got:\n{out}"
    );
    assert!(
        out.contains("sky build --embed"),
        "the refusal must name the verb that does take --embed; got:\n{out}"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

// ---------------------------------------------------------------------------
// package
// ---------------------------------------------------------------------------

/// `sky package --release` builds a store artefact, so it refuses — before any
/// build, with the fix named — a release it cannot ship: no `--release`, a
/// target that is not a native shell, a backend address that is the
/// development default or plain http, and an Android release with no upload
/// key. Every refusal here happens before a toolchain is needed.
#[test]
fn package_refuses_a_release_it_cannot_ship() {
    let dir = scratch("package");
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"shop\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
    )
    .unwrap();
    std::fs::write(
        dir.join("src/Main.sky"),
        "module Main exposing (main)\n\nimport Std.Spa as Spa\n\nmain =\n    0\n",
    )
    .unwrap();
    let package = |args: &[&str], env: &[(&str, &str)]| -> (i32, String) {
        let mut cmd = Command::new(SKY);
        cmd.arg("package")
            .args(args)
            .current_dir(&dir)
            .stdin(std::process::Stdio::null())
            .env_remove("SKY_APP_URL")
            .env_remove("SKY_ANDROID_KEYSTORE")
            .env_remove("SKY_ANDROID_KEYSTORE_PASSWORD")
            .env_remove("SKY_ANDROID_KEY_ALIAS")
            .env_remove("SKY_PACKAGE_RELEASE");
        for (k, v) in env {
            cmd.env(k, v);
        }
        let out = cmd.output().expect("spawn sky package");
        let mut s = String::from_utf8_lossy(&out.stdout).into_owned();
        s.push_str(&String::from_utf8_lossy(&out.stderr));
        (out.status.code().unwrap_or(-1), s)
    };

    let (code, out) = package(&["--target", "mobile:ios"], &[]);
    assert_eq!(code, 2, "no --release is a usage error:\n{out}");
    assert!(out.contains("--release"), "{out}");

    let (code, out) = package(&["--release", "--target", "web"], &[]);
    assert_eq!(code, 1, "{out}");
    assert!(out.contains("not a native shell"), "{out}");

    let (code, out) = package(&["--release", "--target", "desktop:windows"], &[]);
    assert_eq!(code, 1, "{out}");
    assert!(out.contains("macOS"), "{out}");

    let (code, out) = package(&["--release", "--target", "mobile:ios"], &[]);
    assert_eq!(code, 1, "{out}");
    assert!(
        out.contains("App.withAppUrl") && out.contains("SKY_APP_URL"),
        "the development default address must be refused, naming the fix:\n{out}"
    );

    let (code, out) = package(
        &["--release", "--target", "mobile:android"],
        &[("SKY_APP_URL", "http://shop.example.test/")],
    );
    assert_eq!(code, 1, "{out}");
    assert!(out.contains("plain http"), "{out}");

    let (code, out) = package(
        &["--release", "--target", "mobile:android"],
        &[("SKY_APP_URL", "https://shop.example.test/")],
    );
    assert_eq!(code, 1, "{out}");
    assert!(
        out.contains("SKY_ANDROID_KEYSTORE") && out.contains("upload key"),
        "an Android release without signing must name the variables:\n{out}"
    );
    assert!(
        !dir.join(".split").exists(),
        "a refused release must not start a build"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

/// `sky package --release --upload testflight` refuses, before any build and
/// before any `xcrun altool` call, an upload it cannot make, naming the fix:
/// an unknown destination, a non-iOS target, `--ipa` without `--upload`, no
/// `Bundle.withId`, no `Bundle.withBuild`, missing App Store Connect key
/// variables, a key path that is not a `.p8`, no distribution signing (the
/// build would be the unsigned `.ipa`), a development identity and a
/// development profile. The upload itself, with a fake `xcrun`, is proven in
/// `native_shell_flow.rs` (`testflight_upload_validates_then_uploads_through_altool`).
#[test]
fn package_upload_refuses_before_any_network_call() {
    let dir = scratch("package-upload");
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"shop\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
    )
    .unwrap();
    let main = |bundle_steps: &str| {
        std::fs::write(
            dir.join("src/Main.sky"),
            format!(
                "module Main exposing (main, bundle)\n\nimport Std.Bundle as Bundle exposing (Bundle)\nimport Std.Spa as Spa\n\nbundle : Bundle\nbundle =\n    Bundle.default\n{bundle_steps}\n\nmain =\n    0\n"
            ),
        )
        .unwrap();
    };
    // A fake xcrun that only records: no call may reach it.
    let log = dir.join("xcrun.log");
    let fake = dir.join("fake-xcrun");
    std::fs::write(
        &fake,
        "#!/bin/sh\necho \"$*\" >> \"$FAKE_XCRUN_LOG\"\nexit 0\n",
    )
    .unwrap();
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&fake, std::fs::Permissions::from_mode(0o755)).unwrap();
    }
    let key = dir.join("AuthKey_ABC123DEF4.p8");
    std::fs::write(
        &key,
        "-----BEGIN PRIVATE KEY-----\nFAKE\n-----END PRIVATE KEY-----\n",
    )
    .unwrap();
    let not_key = dir.join("notes.txt");
    std::fs::write(&not_key, "not a key").unwrap();
    let dev_profile = dir.join("dev.mobileprovision");
    std::fs::write(
        &dev_profile,
        "<?xml version=\"1.0\"?><plist version=\"1.0\"><dict><key>ProvisionedDevices</key>\
         <array><string>00008110</string></array></dict></plist>",
    )
    .unwrap();
    let key_s = key.display().to_string();
    let not_key_s = not_key.display().to_string();
    let dev_profile_s = dev_profile.display().to_string();
    let asc: [(&str, &str); 3] = [
        ("SKY_ASC_KEY_ID", "ABC123DEF4"),
        ("SKY_ASC_ISSUER_ID", "57246542-96fe-1a63-e053-0824d011072a"),
        ("SKY_ASC_KEY_PATH", &key_s),
    ];
    let package = |args: &[&str], env: &[(&str, &str)]| -> (i32, String) {
        let mut cmd = Command::new(SKY);
        cmd.arg("package")
            .args(args)
            .current_dir(&dir)
            .stdin(std::process::Stdio::null())
            .env("SKY_APP_URL", "https://shop.example.test/")
            .env("SKY_XCRUN", &fake)
            .env("FAKE_XCRUN_LOG", &log);
        for v in [
            "SKY_PACKAGE_RELEASE",
            "SKY_ASC_KEY_ID",
            "SKY_ASC_ISSUER_ID",
            "SKY_ASC_KEY_PATH",
            "SKY_IOS_SIGN_IDENTITY",
            "SKY_IOS_PROVISIONING_PROFILE",
        ] {
            cmd.env_remove(v);
        }
        for (k, v) in env {
            cmd.env(k, v);
        }
        let out = cmd.output().expect("spawn sky package");
        let mut s = String::from_utf8_lossy(&out.stdout).into_owned();
        s.push_str(&String::from_utf8_lossy(&out.stderr));
        (out.status.code().unwrap_or(-1), s)
    };
    let upload = [
        "--release",
        "--target",
        "mobile:ios",
        "--upload",
        "testflight",
    ];

    main("        |> Bundle.withId \"com.example.shop\"\n        |> Bundle.withBuild 3");
    let (code, out) = package(
        &["--release", "--target", "mobile:ios", "--upload", "play"],
        &asc,
    );
    assert_eq!(code, 2, "{out}");
    assert!(out.contains("--upload testflight"), "{out}");
    let (code, out) = package(
        &[
            "--release",
            "--target",
            "mobile:android",
            "--upload",
            "testflight",
        ],
        &asc,
    );
    assert_eq!(code, 2, "{out}");
    assert!(out.contains("mobile:ios or tablet:ipad"), "{out}");
    let (code, out) = package(
        &["--release", "--target", "mobile:ios", "--ipa", "App.ipa"],
        &asc,
    );
    assert_eq!(code, 2, "{out}");
    assert!(out.contains("--upload"), "{out}");

    main("        |> Bundle.withBuild 3");
    let (code, out) = package(&upload, &asc);
    assert_eq!(code, 1, "{out}");
    assert!(
        out.contains("Bundle.withId"),
        "no bundle id must be refused:\n{out}"
    );

    main("        |> Bundle.withId \"com.example.shop\"");
    let (code, out) = package(&upload, &asc);
    assert_eq!(code, 1, "{out}");
    assert!(
        out.contains("Bundle.withBuild") && out.contains("raise it for every upload"),
        "no build number must be refused, saying it must increase:\n{out}"
    );

    main("        |> Bundle.withId \"com.example.shop\"\n        |> Bundle.withBuild 3");
    let (code, out) = package(&upload, &[]);
    assert_eq!(code, 1, "{out}");
    assert!(
        out.contains("SKY_ASC_KEY_ID")
            && out.contains("SKY_ASC_ISSUER_ID")
            && out.contains("SKY_ASC_KEY_PATH")
            && out.contains("App Store Connect API"),
        "missing key variables must be named with the setup:\n{out}"
    );
    let (code, out) = package(&upload, &[asc[0], asc[1], ("SKY_ASC_KEY_PATH", &not_key_s)]);
    assert_eq!(code, 1, "{out}");
    assert!(out.contains("-----BEGIN PRIVATE KEY-----"), "{out}");
    assert!(
        !out.contains("not a key"),
        "the key file's contents are never printed:\n{out}"
    );

    let (code, out) = package(&upload, &asc);
    assert_eq!(code, 1, "{out}");
    assert!(
        out.contains("SKY_IOS_SIGN_IDENTITY") && out.contains("unsigned"),
        "an upload without distribution signing must be refused:\n{out}"
    );
    let mut signed = asc.to_vec();
    signed.push(("SKY_IOS_PROVISIONING_PROFILE", &dev_profile_s));
    signed.push((
        "SKY_IOS_SIGN_IDENTITY",
        "Apple Development: Ada (TEAMID1234)",
    ));
    let (code, out) = package(&upload, &signed);
    assert_eq!(code, 1, "{out}");
    assert!(out.contains("development identity"), "{out}");
    signed.pop();
    signed.push((
        "SKY_IOS_SIGN_IDENTITY",
        "Apple Distribution: Ada (TEAMID1234)",
    ));
    let (code, out) = package(&upload, &signed);
    assert_eq!(code, 1, "{out}");
    assert!(out.contains("lists devices"), "{out}");

    assert!(
        !log.exists(),
        "a refused upload must not run xcrun at all:\n{}",
        std::fs::read_to_string(&log).unwrap_or_default()
    );
    assert!(
        !dir.join("sky-out").join("release").exists(),
        "a refused upload must not start a build"
    );
    let _ = std::fs::remove_dir_all(&dir);
}

// ---------------------------------------------------------------------------
// config migrate
// ---------------------------------------------------------------------------

/// `sky config migrate`: `--check` is clean on a project with no legacy keys,
/// names a legacy runtime key and exits 1 when one is left, and `--dry-run`
/// shows the move into a typed `config` binding without writing a file. An
/// unknown `config` subcommand is a usage error. (The rewriter itself is
/// proven end to end by the `config-migrate` gate.)
#[test]
fn config_migrate_checks_and_previews_without_writing() {
    let dir = scratch("config-migrate");
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::write(
        dir.join("src/Main.sky"),
        "module Main exposing (main)\n\nmain =\n    0\n",
    )
    .unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"x\"\nentry = \"src/Main.sky\"\n",
    )
    .unwrap();
    let (code, out) = run_sky(&dir, &["config", "migrate", "--check"]);
    assert_eq!(code, 0, "no legacy keys is clean:\n{out}");
    assert!(out.contains("clean"), "{out}");

    let legacy = "name = \"x\"\nentry = \"src/Main.sky\"\n\n[log]\nlevel = \"debug\"\n";
    std::fs::write(dir.join("sky.toml"), legacy).unwrap();
    let (code, out) = run_sky(&dir, &["config", "migrate", "--check"]);
    assert_eq!(code, 1, "a legacy key left fails --check:\n{out}");
    assert!(
        out.contains("[log] level") && out.contains("withLog"),
        "{out}"
    );

    let (code, out) = run_sky(&dir, &["config", "migrate", "--dry-run"]);
    assert_eq!(code, 0, "{out}");
    assert!(
        out.contains("Config.withLog"),
        "the preview shows the builder:\n{out}"
    );
    assert_eq!(
        std::fs::read_to_string(dir.join("sky.toml")).unwrap(),
        legacy,
        "--dry-run must not write sky.toml"
    );

    let (code, out) = run_sky(&dir, &["config", "frobnicate"]);
    assert_eq!(code, 2, "{out}");
    let _ = std::fs::remove_dir_all(&dir);
}

/// F-17: CLI misuse is refused, not silently accepted. `sky package
/// --upload` with no value is a usage error (it used to run a plain release),
/// `sky fuzz --help` prints usage and exits 0 (it exited 2), and `sky doc --api
/// openapi` outside a project refuses (it printed an empty spec, exit 0).
#[test]
fn cli_misuse_is_refused_not_ignored() {
    let dir = scratch("misuse");
    std::fs::create_dir_all(&dir).unwrap();
    let (code, out) = run_sky(
        &dir,
        &[
            "package",
            "--release",
            "--target",
            "mobile:android",
            "--upload",
        ],
    );
    assert_eq!(code, 2, "{out}");
    assert!(out.contains("--upload needs a value"), "{out}");
    let (code, out) = run_sky(&dir, &["fuzz", "--help"]);
    assert_eq!(code, 0, "{out}");
    assert!(out.contains("usage: sky fuzz"), "{out}");
    let (code, out) = run_sky(&dir, &["doc", "--api", "openapi"]);
    assert_ne!(code, 0, "{out}");
    assert!(out.contains("no sky.toml"), "{out}");
    let _ = std::fs::remove_dir_all(&dir);
}

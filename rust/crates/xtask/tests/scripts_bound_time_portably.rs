//! A script may not assume `timeout` exists, and the shim must work when it
//! does not.
//!
//! # The defect this exists to remove
//!
//! GNU coreutils `timeout` is not a given. macOS ships neither it nor
//! `gtimeout`; on this machine it came from a nix shell, and when that shell
//! went away the binary went with it. Fourteen scripts invoked a bare
//! `timeout`, and the release gate's own verification then reported this:
//!
//! ```text
//! $ cd runtime-go && timeout 1200 env CGO_ENABLED=1 go test -race ./rt/... | tail -8
//! (eval):1: command not found: timeout
//! [exited with code 0]
//! ```
//!
//! The race detector never ran. `command not found` went to stderr, the
//! pipeline's status came from `tail`, and exit 0 read as a pass.
//!
//! Three call sites turned the missing binary into a GREEN result rather than
//! a red one — `verify-cli.sh` tui-start (five examples printed "✓ (no panic)"
//! without starting the binary), `test-ci.sh` phase_compiler_build (the
//! compiler build silently did nothing and the phase returned 0), and
//! `grill-mutation-matrix.sh` run_suite (an empty log, from which zero
//! failures were counted as every gate surviving every mutation). The other
//! eleven failed loudly but misleadingly.
//!
//! Six scripts had ALREADY hit this on macOS runners and each grown its own
//! fallback — four variants, two of which returned 137 where GNU returns 124,
//! one carrying a comment conceding it could not map to 124 reliably. Six
//! copies of a workaround is the shape of a missing mechanism.
//!
//! # The rules
//!
//! 1. No shell script invokes `timeout` or `gtimeout` as a command. The one
//!    file allowed to name them is [`SHIM`].
//! 2. Every script that calls `with_timeout` sources [`SHIM`] — a function
//!    that is not in scope is `command not found`, which is where this
//!    started.
//! 3. The shim resolves and works with no `timeout` on PATH, passing the
//!    command's exit status through unchanged and reporting 124 on expiry.
//!    Run under a PATH built for the purpose, since the host currently has a
//!    real `timeout` and the failure is invisible while it does.
//! 4. With no bounding mechanism available at all, the shim FAILS naming what
//!    to install. It never runs the command unbounded, and never returns 0.
//!
//! # What this does NOT catch
//!
//! * A script that bounds nothing. Rule 1 is about a `timeout` that is not
//!   there; a long command with no bound at all is invisible to a text scan,
//!   and stays a matter for review.
//! * A caller that discards `with_timeout`'s status. That is precisely how the
//!   three silent sites failed, and the fix is at each call site (`|| true`
//!   into an unchecked capture, `&&` short-circuit, an empty log read as zero
//!   failures). No text rule distinguishes a `|| true` that is deliberate from
//!   one that is a hole, so those three carry their own assertions instead:
//!   `verify-cli.sh` treats 125/127 as a harness fault, `test-ci.sh` checks
//!   the binary exists afterwards, `grill-mutation-matrix.sh` requires the log
//!   to contain a `go test` verdict line.
//! * Perl behaving differently on a platform not tested here. The fallback is
//!   exercised on whatever host runs `cargo test`; a second platform's
//!   `waitpid`/`setpgid` semantics are covered by CI running this on both.
//! * A `timeout` reached through a variable (`$TIMEOUT_BIN "$secs" …`). That
//!   is the exact shape of the six deleted copies, so rule 1's pattern is
//!   deliberately not narrowed to literal command position — but a novel
//!   indirection spelled some other way would slip through.

use std::path::{Path, PathBuf};
use std::process::Command;

/// The one file allowed to name the `timeout` binaries.
const SHIM: &str = "scripts/lib/with-timeout.sh";

/// This file. Every rule below quotes the shapes it forbids, so a scan that
/// included it would report itself.
const SELF: &str = "rust/crates/xtask/tests/scripts_bound_time_portably.rs";

/// Scripts that are frozen records of a past run rather than live gates:
/// `docs/history/` is excluded from doc gating for the same reason, and a
/// dated `docs/perf/runs/<date>/` directory is the artefact of one
/// measurement. Rewriting them would falsify the record. Anything outside
/// these prefixes is live and must comply.
const FROZEN_PREFIXES: &[&str] = &["docs/history/", "docs/perf/runs/"];

fn repo() -> PathBuf {
    PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../.."))
}

/// Every shell script in the tree, plus the CI workflows (a `run:` block is a
/// shell script in YAML), as (repo-relative path, contents).
fn shell_scripts() -> Vec<(String, String)> {
    fn walk(dir: &Path, out: &mut Vec<PathBuf>) {
        let Ok(rd) = std::fs::read_dir(dir) else { return };
        for e in rd.flatten() {
            let p = e.path();
            let name = e.file_name().to_string_lossy().to_string();
            if p.is_dir() {
                // `target`, `node_modules` and `sky-out` are build output;
                // `.git` is not source. Everything else is in scope, which is
                // how `apps/fieldbook/verify.sh` and `tools/probe-sweep.sh`
                // were found after `scripts/` looked finished.
                let skip = matches!(name.as_str(), "target" | "node_modules" | "sky-out" | "local-target")
                    || name.starts_with('.');
                if !skip {
                    walk(&p, out);
                }
            } else if p.extension().and_then(|x| x.to_str()) == Some("sh") {
                out.push(p);
            }
            // `.github/` starts with a dot and is skipped above; its workflows
            // are added separately by `shell_scripts`, because a `run:` block
            // is a shell script that happens to live in YAML.
        }
    }
    let root = repo();
    let mut files = Vec::new();
    walk(&root, &mut files);
    // CI workflows: a `run:` block is a shell script in YAML, and reaches for
    // `timeout` just as readily.
    if let Ok(rd) = std::fs::read_dir(root.join(".github/workflows")) {
        for e in rd.flatten() {
            let p = e.path();
            if matches!(p.extension().and_then(|x| x.to_str()), Some("yml") | Some("yaml")) {
                files.push(p);
            }
        }
    }
    files.sort();
    files
        .into_iter()
        .filter_map(|p| {
            let rel = p.strip_prefix(&root).ok()?.to_string_lossy().replace('\\', "/");
            let text = std::fs::read_to_string(&p).ok()?;
            Some((rel, text))
        })
        .collect()
}

fn is_frozen(rel: &str) -> bool {
    FROZEN_PREFIXES.iter().any(|p| rel.starts_with(p))
}

/// True when `line` invokes `timeout`/`gtimeout` as a command.
///
/// Naive quote counting is not enough, and the first version of this function
/// proved it: `if out="$( ( cd "$PROJ" && timeout 300 …` has three double
/// quotes before the match, so a parity test called it "inside a string" and
/// let a reintroduced bare `timeout` through. That is a gate reporting PASS on
/// a mutation it exists to catch — the same failure this whole file is about,
/// one level up. So the scan tracks shell state properly: `$(` and a backtick
/// open a fresh quoting context, `'` suppresses everything, `\` escapes.
///
/// A command position is the start of the line or the point just after an
/// unquoted `;`, `&`, `|`, `(`, `{` or a `&&`/`||` operator. The word there is
/// the command, and it must be followed by whitespace and a duration-shaped
/// argument — which is what keeps `timeout-minutes:`, `--timeout 5` and
/// `health-timeout 5s` out.
fn invokes_timeout(line: &str) -> bool {
    if line.trim_start().starts_with('#') {
        return false;
    }
    let b = line.as_bytes();
    let mut in_single = false;
    let mut in_double = false;
    // Saved (in_single, in_double) for each `$(` / backtick we are inside.
    let mut nest: Vec<(bool, bool)> = Vec::new();
    let mut at_cmd = true; // start of line is a command position
    let mut i = 0usize;

    while i < b.len() {
        let c = b[i] as char;

        if !in_single && c == '\\' {
            i += 2;
            continue;
        }
        if in_single {
            if c == '\'' {
                in_single = false;
            }
            i += 1;
            continue;
        }
        // Entering or leaving a quote does NOT end a command position:
        // `SKY_POSTGRES_BIN="$SKY_POSTGRES_BIN" timeout 1800 …` is an
        // environment prefix, and clearing the flag here hid it. Quoted
        // CONTENT is skipped below without ever reaching the word scan, which
        // is what keeps prose out.
        if c == '\'' {
            in_single = true;
            i += 1;
            continue;
        }
        if c == '"' {
            in_double = !in_double;
            i += 1;
            continue;
        }
        // A command substitution starts a fresh context even inside "…".
        if c == '$' && i + 1 < b.len() && b[i + 1] == b'(' {
            // `$((` is arithmetic, not a command context.
            if i + 2 < b.len() && b[i + 2] == b'(' {
                i += 3;
                at_cmd = false;
                continue;
            }
            nest.push((in_single, in_double));
            in_single = false;
            in_double = false;
            at_cmd = true;
            i += 2;
            continue;
        }
        if c == '`' {
            nest.push((in_single, in_double));
            in_single = false;
            in_double = false;
            at_cmd = true;
            i += 1;
            continue;
        }
        if c == ')' && !in_double {
            if let Some((s, d)) = nest.pop() {
                in_single = s;
                in_double = d;
            }
            // `case` arms are `pattern) cmd`, and `X=$(foo) cmd` is an
            // assignment prefix — both put the next word in command position.
            at_cmd = true;
            i += 1;
            continue;
        }
        if in_double {
            // Quoted content is skipped without word-scanning, so it can never
            // be read as a command. It must not clear the flag either: in
            // `FOO="$FOO" timeout 1800 …` the quoted VALUE sits between the
            // assignment and the command, and clearing here hid that line.
            i += 1;
            continue;
        }
        if matches!(c, ';' | '&' | '|' | '(' | '{' | '!') {
            at_cmd = true;
            i += 1;
            continue;
        }
        if c.is_whitespace() {
            i += 1;
            continue;
        }

        // A word starts here.
        let start = i;
        while i < b.len() && !(b[i] as char).is_whitespace() {
            let ch = b[i] as char;
            if matches!(ch, ';' | '&' | '|' | '(' | ')' | '"' | '\'' | '`') {
                break;
            }
            i += 1;
        }
        let word = &line[start..i];
        if at_cmd && (word == "timeout" || word == "gtimeout") {
            let rest = &line[i..];
            let arg = rest.trim_start();
            // Whitespace then a duration: a literal number, "$VAR", $VAR, or a
            // $(( … )) arithmetic expansion.
            let had_space = arg.len() < rest.len();
            if had_space
                && (arg.starts_with(|c: char| c.is_ascii_digit())
                    || arg.starts_with('"')
                    || arg.starts_with('$'))
            {
                return true;
            }
        }
        // Shell keywords and env-assignment prefixes keep the next word in
        // command position: `if timeout 5 …`, `FOO=bar timeout 5 …`.
        at_cmd = matches!(
            word,
            "if" | "then" | "else" | "elif" | "while" | "until" | "do" | "!" | "time" | "command" | "exec"
        ) || word.contains('=')
            // A YAML key: `run: timeout 900 …` is a one-line shell step, and
            // the shell after `run:` is exactly as able to reach for a
            // `timeout` that is not installed on the runner.
            || word.ends_with(':');
    }
    false
}

/// The scanner's own regression set. Every line here is real: the POSITIVE
/// cases are shapes that were in the tree, the NEGATIVE cases are shapes that
/// a cruder scanner reported (or missed) on this repository. The M1 line in
/// particular is the mutation that the first quote-parity version let through.
#[test]
fn the_scanner_sees_the_shapes_this_repository_actually_contains() {
    let must_flag = [
        r#"        timeout 600 "$SKY" build src/Main.sky"#,
        r#"if ! ( cd "$d" && timeout 900 "$ROOT/sky-out/sky" install >/dev/null 2>&1 ); then"#,
        r#"    | timeout $(( DURATION + 180 )) gcloud compute ssh "$I" --command 'bash -s' \"#,
        r#"( cd rust && timeout 3600 bash -c 'cargo test --workspace' ) || fail "gates""#,
        r#"    SKY_POSTGRES_BIN="$SKY_POSTGRES_BIN" timeout 1800 go test -v ./rt/... -count=1 ) \"#,
        r#"out=$( ( cd "$d" && echo "$input" | timeout 10 "$bin" 2>"$e" ) || echo "__EXIT_$?")"#,
        // M1: three double quotes precede the match. Quote parity called this
        // "inside a string" and passed the mutation.
        r#"  if out="$( ( cd "$PROJ" && timeout 300 "$SKY" check "src/$last.sky" ) 2>&1 )"; then"#,
        r#"CODE=$(timeout 20 curl -s -o dumps/page.html -w '%{http_code}' "http://127.0.0.1:$P/")"#,
        r#"          run: timeout 900 cargo test --workspace"#,
    ];
    for line in must_flag {
        assert!(invokes_timeout(line), "should have been flagged as a bare timeout: {line}");
    }

    let must_not_flag = [
        // Prose inside a string — the false positive the first version hit.
        r#"echo "[run] node $RUNNER (port $PORT, timeout 120s, TMPDIR=$TMPDIR)""#,
        r#"# timeout 600 go test ./rt/...  (what this used to be)"#,
        r#"    timeout-minutes: 30"#,
        r#"            timeout: 30"#,
        r#"          --health-timeout 5s"#,
        r#"        with_timeout 600 "$SKY" build src/Main.sky"#,
        r#"echo 'no timeout 5 here, it is quoted'"#,
        r#"local secs="$1"; shift   # BUILD_TIMEOUT / RUN_TIMEOUT"#,
        r#"SKY_HTTP_CLIENT_TIMEOUT=5s with_timeout 10 "$bin""#,
    ];
    for line in must_not_flag {
        assert!(!invokes_timeout(line), "should NOT have been flagged: {line}");
    }
}

#[test]
fn no_script_invokes_a_bare_timeout_binary() {
    let mut offenders = Vec::new();
    for (rel, text) in shell_scripts() {
        if rel == SHIM || is_frozen(&rel) {
            continue;
        }
        for (n, line) in text.lines().enumerate() {
            if invokes_timeout(line) {
                offenders.push(format!("{rel}:{}: {}", n + 1, line.trim()));
            }
        }
    }
    assert!(
        offenders.is_empty(),
        "these scripts invoke `timeout`/`gtimeout` directly. The binary is absent on \
         stock macOS and was absent on this repo's own dev host, where a missing \
         `timeout` made `go test -race` report exit 0 having run nothing.\n\
         Source {SHIM} and call `with_timeout <secs> <cmd...>` instead.\n\n  {}",
        offenders.join("\n  ")
    );
}

#[test]
fn every_script_that_calls_with_timeout_sources_the_shim() {
    let mut offenders = Vec::new();
    for (rel, text) in shell_scripts() {
        if rel == SHIM {
            continue;
        }
        let calls = text
            .lines()
            .any(|l| !l.trim_start().starts_with('#') && l.contains("with_timeout "));
        if !calls {
            continue;
        }
        // `source …/with-timeout.sh` or the POSIX `. …/with-timeout.sh`.
        let sources = text
            .lines()
            .any(|l| !l.trim_start().starts_with('#') && l.contains("with-timeout.sh"));
        if !sources {
            offenders.push(rel);
        }
    }
    assert!(
        offenders.is_empty(),
        "these scripts call `with_timeout` without sourcing {SHIM}. An unsourced \
         function is `command not found`, which is the defect this shim exists to \
         remove:\n  {}",
        offenders.join("\n  ")
    );
}

/// A `source` line that names a path which does not exist is worse than no
/// `source` line: the script still LOOKS wired, and `with_timeout` is still
/// `command not found` at run time.
///
/// This is not hypothetical. A search-and-replace while writing these very
/// changes emitted `source "/scripts/lib/with-timeout.sh"` into two scripts —
/// the leading `$REPO_ROOT` eaten by the tool doing the edit. The rule above
/// passed on both, because the text does contain "with-timeout.sh". So the
/// path is resolved, not just matched.
#[test]
fn every_lib_source_line_names_a_file_that_exists() {
    let root = repo();
    let mut offenders = Vec::new();
    for (rel, text) in shell_scripts() {
        // `.sh` only. A workflow `run:` block resolves relative paths against
        // the job's `working-directory`, not against the YAML file, so the
        // same arithmetic would be wrong there — and wrong in the direction
        // that invents failures.
        if !rel.ends_with(".sh") {
            continue;
        }
        let script_dir = root.join(&rel).parent().unwrap().to_path_buf();
        for (n, line) in text.lines().enumerate() {
            let t = line.trim_start();
            if t.starts_with('#') {
                continue;
            }
            if !(t.starts_with("source ") || t.starts_with(". ")) {
                continue;
            }
            if !t.contains("scripts/lib/") {
                continue;
            }
            // The path, with surrounding quotes stripped.
            let arg = t.splitn(2, char::is_whitespace).nth(1).unwrap_or("").trim();
            let arg = arg.trim_matches('"').trim_matches('\'');
            // A leading `$VAR` / `${VAR}` is the repo root in every current
            // spelling ($ROOT / $REPO / $REPO_ROOT). Anything else stays as
            // written and is resolved relative to the script's directory.
            let resolved = if let Some(rest) = arg.strip_prefix('$') {
                let rest = rest.trim_start_matches('{');
                match rest.find('/') {
                    Some(slash) => root.join(&rest[slash + 1..]),
                    None => continue,
                }
            } else {
                script_dir.join(arg)
            };
            if !resolved.is_file() {
                offenders.push(format!("{rel}:{}: {} -> {}", n + 1, t, resolved.display()));
            }
        }
    }
    assert!(
        offenders.is_empty(),
        "these `source` lines name a file that does not exist. The script reads as \
         wired and is not:\n  {}",
        offenders.join("\n  ")
    );
}

/// Build a PATH directory holding only the tools named, so a probe can run
/// with `timeout` provably absent. Returns the directory; it is cleaned up by
/// the caller going out of scope only if the test passes, which is fine —
/// these live under the target dir.
fn path_dir_with(name: &str, tools: &[&str]) -> PathBuf {
    let dir = std::env::temp_dir().join(format!("sky-with-timeout-{name}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).expect("create probe PATH dir");
    for tool in tools {
        let src = which(tool).unwrap_or_else(|| panic!("this test needs `{tool}` on PATH"));
        let dst = dir.join(tool);
        #[cfg(unix)]
        std::os::unix::fs::symlink(&src, &dst).expect("symlink probe tool");
    }
    dir
}

fn which(tool: &str) -> Option<PathBuf> {
    let path = std::env::var_os("PATH")?;
    std::env::split_paths(&path).map(|d| d.join(tool)).find(|c| c.is_file())
}

/// Run `script` in bash with PATH set to `path_dir` and nothing else.
/// `--noprofile --norc` matters: without it bash reads the user's profile,
/// which puts the real PATH back and makes the probe silently meaningless —
/// the same class of invisible non-run this whole file is about.
fn bash_with_path(path_dir: &Path, script: &str) -> (i32, String) {
    let out = Command::new("/bin/bash")
        .args(["--noprofile", "--norc", "-c", script])
        .env_clear()
        .env("PATH", path_dir)
        .current_dir(repo())
        .output()
        .expect("run bash");
    let mut text = String::from_utf8_lossy(&out.stdout).into_owned();
    text.push_str(&String::from_utf8_lossy(&out.stderr));
    (out.status.code().unwrap_or(-1), text)
}

#[test]
fn the_shim_works_with_no_timeout_binary_on_path() {
    let dir = path_dir_with("perl-only", &["perl"]);
    let src = format!("source {SHIM}");

    // The probe is worthless if `timeout` is reachable, so prove it is not
    // before drawing any conclusion from what follows.
    let (rc, out) = bash_with_path(&dir, "command -v timeout gtimeout; echo rc=$?");
    assert!(
        !out.contains("/timeout") && !out.contains("/gtimeout"),
        "probe PATH still reaches a timeout binary, so this test proves nothing: {out} (rc={rc})"
    );

    // Exit status passes through unchanged. A shim that swallowed a non-zero
    // exit would recreate the reported bug one layer down.
    let (rc, out) = bash_with_path(&dir, &format!("{src}; with_timeout 5 /bin/sh -c 'exit 7'"));
    assert_eq!(rc, 7, "non-zero exit must pass through unchanged, got {rc}: {out}");

    let (rc, out) = bash_with_path(&dir, &format!("{src}; with_timeout 5 /bin/sh -c 'exit 0'"));
    assert_eq!(rc, 0, "zero exit must pass through unchanged, got {rc}: {out}");

    // The command actually runs, and its stdout reaches the caller.
    let (rc, out) = bash_with_path(&dir, &format!("{src}; with_timeout 5 /bin/echo RAN"));
    assert_eq!(rc, 0, "{out}");
    assert!(out.contains("RAN"), "the command's stdout must reach the caller: {out}");

    // Expiry: 124, and the sleep is genuinely killed rather than waited out.
    let started = std::time::Instant::now();
    let (rc, out) = bash_with_path(&dir, &format!("{src}; with_timeout 1 /bin/sleep 30"));
    let elapsed = started.elapsed();
    assert_eq!(rc, 124, "expiry must report 124 (GNU's convention), got {rc}: {out}");
    assert!(
        elapsed < std::time::Duration::from_secs(10),
        "expiry must KILL, not wait: /bin/sleep 30 bounded at 1s took {elapsed:?}"
    );

    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn the_shim_refuses_rather_than_running_unbounded_or_pretending() {
    // No timeout, no gtimeout, no perl: nothing can bound the command.
    let dir = path_dir_with("nothing", &[]);
    let (rc, out) = bash_with_path(
        &dir,
        &format!("source {SHIM}; with_timeout 5 /bin/echo SHOULD-NOT-RUN"),
    );
    assert_ne!(rc, 0, "an unbounded command must not report success: {out}");
    assert_eq!(rc, 127, "expected 127 (cannot execute), got {rc}: {out}");
    assert!(
        !out.contains("SHOULD-NOT-RUN"),
        "the command must not run unbounded — a wedged `sky test` on a runner with no \
         timeout-minutes burned GitHub's 6-hour default once already: {out}"
    );
    // Naming the fix is the difference between a red run somebody can act on
    // and a red run somebody reruns.
    for expected in ["no 'timeout'", "coreutils", "perl"] {
        assert!(out.contains(expected), "failure must name what to install ({expected:?}): {out}");
    }
    let _ = std::fs::remove_dir_all(&dir);
}

/// A script that fans out through `xargs -P` into fresh `bash -c` shells has
/// to carry the shim across that boundary: an exported worker function calling
/// an unexported `with_timeout` is `command not found` in every worker, and
/// `example-sweep.sh` builds every example that way. The old code exported
/// `run_with_timeout` and `TIMEOUT_CMD` for exactly this reason; the
/// replacement must not lose it.
#[test]
fn a_script_that_forks_workers_exports_the_shim_to_them() {
    for (rel, text) in shell_scripts() {
        // `xargs -P` only. An earlier version also matched the word
        // "parallel", and flagged `test-ci.sh` for a COMMENT reading "every
        // parallel step reads MAX_WORKERS" — a gate inventing a failure is a
        // gate people learn to ignore. GNU `parallel` is not used in this
        // repository; if it ever is, add it here.
        let forks = text.contains("xargs -P");
        let calls = text
            .lines()
            .any(|l| !l.trim_start().starts_with('#') && l.contains("with_timeout "));
        if !(forks && calls) {
            continue;
        }
        assert!(
            text.contains("export -f") && text.contains("with_timeout"),
            "{rel} fans out through `xargs -P` into fresh shells AND calls with_timeout, \
             but does not `export -f with_timeout`. Every worker would get `command not \
             found` and bound nothing."
        );
        assert!(
            text.contains("_sky_with_timeout_resolve"),
            "{rel} exports `with_timeout` but not its resolver \
             `_sky_with_timeout_resolve`, which the function calls on first use."
        );
        assert!(
            text.contains("_SKY_WITH_TIMEOUT_PERL_PROG"),
            "{rel} exports the shim's functions but not \
             `_SKY_WITH_TIMEOUT_PERL_PROG`, so a worker on a host with no `timeout` \
             binary would have an empty fallback program."
        );
    }
}

/// Run `script` under a shell, with the repo's real PATH left intact.
///
/// `-i` is deliberate for zsh: the alias that breaks resolution lives in
/// `~/.zshrc`, and a non-interactive zsh does not read it. A probe that could
/// not see the alias would pass on the broken shim, which is the failure mode
/// this whole file exists to prevent. The alias is injected explicitly below
/// instead, so the probe does not depend on whose machine it runs on.
fn shell_script(shell: &str, script: &str) -> (i32, String) {
    let out = Command::new(shell)
        .args(["-c", script])
        .current_dir(repo())
        .output()
        .unwrap_or_else(|e| panic!("run {shell}: {e}"));
    let mut text = String::from_utf8_lossy(&out.stdout).into_owned();
    text.push_str(&String::from_utf8_lossy(&out.stderr));
    (out.status.code().unwrap_or(-1), text)
}

/// `command -v NAME` answers "is this name runnable", not "which executable
/// FILE is it". The shim used to exec whatever it returned, and both shells can
/// return something unexec'able:
///
///   * zsh prints the ALIAS DEFINITION for an alias. The user's `~/.zshrc` has
///     `alias timeout=gtimeout`, so at their prompt the shim died with
///     `command not found: alias timeout=gtimeout` and returned **127** — the
///     status GNU `timeout` uses for "the command could not be executed", so
///     the failure read as the BOUNDED command being missing rather than the
///     bound. A shim whose whole purpose is that gates are bounded rather than
///     silently unrun, unrunnable in the shell the repository is driven from.
///   * BOTH shells print a bare name for a shell FUNCTION, which the shim would
///     then exec — recursing into the function rather than bounding anything.
///
/// Bash hides the alias case (its `command -v` ignores aliases), which is why
/// this survived review. Both shapes are asserted here, in both shells, so
/// neither can come back.
#[test]
fn the_shim_survives_a_shadowed_timeout_name() {
    // A shell function named `timeout`, in whichever shells exist. `command -v`
    // returns the bare name `timeout` for this in bash AND zsh.
    for shell in ["/bin/bash", "/bin/zsh"] {
        if !Path::new(shell).is_file() {
            continue;
        }
        let script = format!(
            "timeout() {{ echo SHADOW-FUNCTION-RAN; return 99; }}; \
             source {SHIM}; with_timeout 5 echo BOUNDED; echo rc=$?"
        );
        let (_, out) = shell_script(shell, &script);
        assert!(
            out.contains("BOUNDED") && out.contains("rc=0"),
            "{shell}: a shell function named `timeout` broke the shim.\n{out}"
        );
        assert!(
            !out.contains("SHADOW-FUNCTION-RAN"),
            "{shell}: the shim exec'd the shell FUNCTION `timeout` rather than an \
             executable file — `command -v` returned the bare name and it was \
             trusted as a path.\n{out}"
        );
    }

    // An alias named `timeout`. Only zsh's `command -v` reports these, and only
    // this shape produced the user's `rc=127`.
    if Path::new("/bin/zsh").is_file() {
        let script = format!(
            "alias timeout=/definitely/not/a/real/binary; \
             source {SHIM}; with_timeout 5 echo BOUNDED; echo rc=$?"
        );
        let (_, out) = shell_script("/bin/zsh", &script);
        assert!(
            out.contains("BOUNDED") && out.contains("rc=0"),
            "zsh: an `alias timeout=…` broke the shim — `command -v` returned the \
             alias DEFINITION and it was trusted as a path. This is the exact \
             failure reported from the user's prompt (rc=127).\n{out}"
        );
    }
}

/// The shim is sourced by bash scripts and typed at a zsh prompt, so its
/// contract — status passthrough, 124 on expiry — must hold in both. Asserted
/// against the observable behaviour rather than the internals implementing it.
///
/// # What this does NOT catch
///
/// The shim's other shell-dependency was an unquoted `$VAR` holding `-k 10`,
/// which bash splits into two words and zsh does not:
///
///     K="-k 10"; probe $K 5 true
///     bash -> argc=4  [-k] [10] [5] [true]
///     zsh  -> argc=3  [-k 10] [5] [true]
///
/// This test passes with that fault reinstated, and would on any host with GNU
/// coreutils. Measured: `gtimeout "-k 10" 1 sleep 5` returns 124 exactly as
/// `gtimeout -k 10 1 sleep 5` does, because getopt hands the parser the optarg
/// `" 10"` and it skips leading whitespace. The fault is real but LATENT — it
/// needs a `timeout` with a stricter parser (busybox/toybox) to surface, and
/// this repository's CI has none. The shim was fixed anyway, because resting on
/// a coincidence between a shell's splitting rules and an implementation's
/// leniency is not a contract; but no gate here proves it stays fixed, and
/// saying so is better than implying coverage that does not exist.
#[test]
fn the_shim_honours_its_contract_in_every_shell_present() {
    // `.`, not `source`. `source` is a bash/zsh extension; `/bin/sh` is bash in
    // sh-mode on macOS and dash on Debian, and dash has only the POSIX `.`. So
    // the `/bin/sh` arm read as covered on the machine this test was written on
    // and, the first time CI installed enough PostgreSQL for the test binary
    // ahead of it to stop failing early, reported
    //
    //     /bin/sh: 1: source: not found
    //     /bin/sh: 1: with_timeout: not found
    //     left: 127  right: 42
    //
    // — which accuses the shim of losing an exit status when nothing had been
    // sourced at all. The shim itself is clean under dash (verified: 42 / 124 /
    // 0, on both the `timeout` and the perl path). One spelling every shell in
    // the list accepts makes the third arm test dash instead of testing bash
    // twice.
    let mut ran = 0;
    for shell in ["/bin/bash", "/bin/zsh", "/bin/sh"] {
        if !Path::new(shell).is_file() {
            continue;
        }
        ran += 1;
        let (rc, out) = shell_script(
            shell,
            &format!(". {SHIM}; with_timeout 5 /bin/sh -c 'exit 42'"),
        );
        assert_eq!(rc, 42, "{shell}: exit status must pass through unchanged.\n{out}");

        let (rc, out) = shell_script(shell, &format!(". {SHIM}; with_timeout 1 sleep 30"));
        assert_eq!(rc, 124, "{shell}: expiry must report 124.\n{out}");

        let (rc, out) =
            shell_script(shell, &format!(". {SHIM}; with_timeout 5 /bin/sh -c 'exit 0'"));
        assert_eq!(rc, 0, "{shell}: success must report 0.\n{out}");
    }
    assert!(ran > 0, "no shell was found to probe, so this gate proved nothing");
}

#[test]
fn the_shim_exists_and_this_gate_names_it_correctly() {
    // A rule whose subject has moved is a rule that passes vacuously.
    assert!(repo().join(SHIM).is_file(), "{SHIM} is missing — the rules above have no subject");
    assert!(repo().join(SELF).is_file(), "{SELF} does not name this file");
    assert!(
        !shell_scripts().is_empty(),
        "the scan found no shell scripts at all, so every rule above passed vacuously"
    );
}

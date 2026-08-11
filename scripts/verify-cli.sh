#!/usr/bin/env bash
# v0.13.x runtime verification — CLI + Sky.Cli + Sky.Tui examples.
#
# For pure-CLI apps: invoke the binary, optionally with stdin / args,
# capture stdout/stderr, assert exit 0 + no panic / "runtime error".
#
# For Sky.Tui + Sky.Cli: spawn briefly, observe no immediate panic,
# kill. Full keystroke interaction needs a PTY (Sky.Tui v1 uses
# `golang.org/x/term`'s raw-mode entry). Build-only-verified TUI
# examples are flagged with `pty-skip`.
#
# Fyne (11-fyne-stopwatch) is `gui-skip` — needs X11.

#
# Options:
#   --json <path>  write a machine-readable per-entry result file
#   --rebuild      rebuild every example before verifying it
#
# `--rebuild` matters more than it looks. Without it this script only builds an
# example when `sky-out/app` is MISSING, so it certifies whatever binary an
# earlier run happened to leave behind — a source change can be verified green
# by a stale artefact. Any caller using this as a GATE must pass --rebuild, or
# the gate cannot be falsified by a source mutation.
set -u

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ARTEFACT_DIR="$REPO_ROOT/.skycache/verify"

JSON_OUT=""
REBUILD=0
while [ $# -gt 0 ]; do
    case "$1" in
        --json)
            [ $# -ge 2 ] || { echo "verify-cli: --json requires a path" >&2; exit 2; }
            JSON_OUT="$2"; shift 2 ;;
        --rebuild) REBUILD=1; shift ;;
        *) echo "verify-cli: unknown option $1" >&2; exit 2 ;;
    esac
done

# (example, mode, stdin-or-args, expected-stdout-substring)
#   mode: cli | cli-stdin | tui-start | cli-args | skip-gui
CLI_TESTS=(
    "00-standard-libs    cli          ''            'passed'"
    "01-hello-world      cli          ''            'Hello'"
    "02-go-stdlib        cli          ''            ''"
    "03-tea-external     cli          ''            ''"
    "04-local-pkg        cli          ''            ''"
    "06-json             cli          ''            ''"
    "07-todo-cli         cli-args     'list'        ''"
    "14-task-demo        cli          ''            ''"
)

# Sky.Tui / Sky.Cli — start briefly then kill, look for panic-free
# startup. The runtime enters raw-mode on a TTY, but our spawn here
# doesn't allocate a PTY, so the Tui runtime takes the friendly
# `TERM=dumb / non-TTY stdin` exit path. Sky.Cli reads stdin lines.
TUI_TESTS=(
    "20-cli-counter      tui-start    ''            ''"
    "21-tui-stopwatch    tui-start    ''            ''"
    "22-tui-stopwatch-ui tui-start    ''            ''"
    "23-tui-todo         tui-start    ''            ''"
    "24-tui-kitchen-sink tui-start    ''            ''"
)

GUI_TESTS=(
    "11-fyne-stopwatch   skip-gui     ''            ''"
)

pass=0
fail=0
skip=0
FAILS=()
SKIPS=()
ENTRIES=""

# Record one entry for the machine-readable report. `reason` is a fixed code,
# never free text, so the file needs no JSON escaping and the caller can match
# on it.
record() { # record <name> <mode> <outcome> <reason>
    [ -n "$JSON_OUT" ] || return 0
    ENTRIES="$ENTRIES{\"name\":\"$1\",\"mode\":\"$2\",\"outcome\":\"$3\",\"reason\":\"$4\"},
"
}

run_test() {
    local name="$1" mode="$2" input="$3" expect="$4"
    local bin="$REPO_ROOT/examples/$name/sky-out/app"

    # Decide skips BEFORE touching the binary. `skip-gui` is evaluated in the
    # `case $mode` block further down, which used to sit after the binary
    # check — so a GUI example the script intends to SKIP was first failed for
    # having no binary, and (with the build-if-missing logic below) would even
    # be built pointlessly. 11-fyne-stopwatch cannot be built at all on macOS:
    # fyne needs cgo, while the FFI inspector pins GOOS=linux/amd64 with
    # CGO_ENABLED=0, so its surface cannot be generated. Skipping is the
    # script's own stated intent (see the header note).
    if [ "$mode" = "skip-gui" ]; then
        echo "⊘ $name — GUI app, skipped (needs a display; FFI surface needs cgo)"
        skip=$((skip+1)); SKIPS+=("$name")
        record "$name" "$mode" "skip" "gui-needs-display"
        return
    fi

    # Force a rebuild so the verdict is about the tree under test, not about
    # whatever binary a previous run left in place.
    if [ "$REBUILD" -eq 1 ]; then
        rm -f "$bin"
    fi

    # Build it if it isn't there, instead of failing.
    #
    # This script used to hard-fail on a missing binary, which meant it could
    # only ever pass on artifacts left behind by some EARLIER run. Six of its
    # entries (00-standard-libs, 20-cli-counter, 21/22/23/24-tui-*) are not in
    # scripts/example-sweep.sh's table — the only thing that builds
    # `sky-out/app` — so on a clean checkout, or in a fresh git worktree, those
    # six could never pass. That is how a release gate reported red for a
    # reason having nothing to do with the release.
    if [ ! -f "$bin" ]; then
        echo "  … $name — binary missing, building it"
        # `sky install` FIRST. An example with external Go FFI deps has no
        # committed `sky-ffi/` surface — it is a generated build artefact — so
        # `sky build` alone fails with "has no generated FFI surface". Under the
        # old build-only-if-missing behaviour a stale binary hid this; forcing a
        # rebuild exposed it on 03-tea-external (github.com/google/uuid +
        # joho/godotenv). It is a no-op for examples whose deps are Go stdlib.
        if ! ( cd "$REPO_ROOT/examples/$name" && timeout 900 "$REPO_ROOT/sky-out/sky" install >/dev/null 2>&1 ); then
            echo "✗ $name — sky install failed (see: cd examples/$name && sky install)"
            fail=$((fail+1)); FAILS+=("$name")
            record "$name" "$mode" "fail" "install-failed"
            return
        fi
        if ! ( cd "$REPO_ROOT/examples/$name" && timeout 900 "$REPO_ROOT/sky-out/sky" build src/Main.sky >/dev/null 2>&1 ); then
            echo "✗ $name — build failed (see: cd examples/$name && sky build src/Main.sky)"
            fail=$((fail+1)); FAILS+=("$name")
            record "$name" "$mode" "fail" "build-failed"
            return
        fi
    fi
    if [ ! -f "$bin" ]; then
        echo "✗ $name — binary still missing after build at $bin"
        fail=$((fail+1)); FAILS+=("$name")
        record "$name" "$mode" "fail" "binary-missing"
        return
    fi
    local out errfile artefact
    artefact="$ARTEFACT_DIR/$name"
    mkdir -p "$artefact"
    errfile="$artefact/stderr.log"

    case "$mode" in
        cli)
            out=$( ( cd "$REPO_ROOT/examples/$name" && timeout 10 "$bin" 2>"$errfile" ) || echo "__EXIT_$?")
            ;;
        cli-stdin)
            out=$( ( cd "$REPO_ROOT/examples/$name" && echo "$input" | timeout 10 "$bin" 2>"$errfile" ) || echo "__EXIT_$?")
            ;;
        cli-args)
            out=$( ( cd "$REPO_ROOT/examples/$name" && timeout 10 "$bin" $input 2>"$errfile" ) || echo "__EXIT_$?")
            ;;
        tui-start)
            # Spawn briefly; the runtime should exit cleanly on non-TTY stdin.
            out=$( ( cd "$REPO_ROOT/examples/$name" && timeout 3 "$bin" 2>"$errfile" </dev/null || true) )
            ;;
        skip-gui)
            echo "⊘ $name — GUI app, skipped (needs X11)"
            skip=$((skip+1)); SKIPS+=("$name")
            record "$name" "$mode" "skip" "gui-needs-x11"
            return
            ;;
        *)
            echo "✗ $name — unknown mode $mode"
            fail=$((fail+1)); FAILS+=("$name")
            record "$name" "$mode" "fail" "unknown-mode"
            return
            ;;
    esac

    # Panic detection
    local err=""
    [ -f "$errfile" ] && err=$(cat "$errfile")
    if echo "$out$err" | grep -qE 'panic:|runtime error:|interface conversion:'; then
        echo "✗ $name — runtime panic"
        echo "$out$err" | grep -E 'panic:|runtime error:|interface conversion:' | head -2 | sed 's/^/   /'
        fail=$((fail+1)); FAILS+=("$name")
        record "$name" "$mode" "fail" "runtime-panic"
        return
    fi

    # Expected stdout substring (when given)
    if [ -n "$expect" ]; then
        if echo "$out" | grep -qF "$expect"; then
            echo "✓ $name (output matched '$expect')"
            pass=$((pass+1))
            record "$name" "$mode" "pass" "output-matched"
        else
            echo "✗ $name — output missing '$expect'"
            echo "$out" | head -3 | sed 's/^/   /'
            fail=$((fail+1)); FAILS+=("$name")
            record "$name" "$mode" "fail" "output-missing"
        fi
    else
        echo "✓ $name (no panic)"
        pass=$((pass+1))
        record "$name" "$mode" "pass" "no-panic"
    fi
}

echo "=== CLI examples ==="
for entry in "${CLI_TESTS[@]}"; do
    # Strip the surrounding quotes so we can pass to run_test
    eval "set -- $entry"
    run_test "$@"
done

echo ""
echo "=== TUI / Sky.Cli examples ==="
for entry in "${TUI_TESTS[@]}"; do
    eval "set -- $entry"
    run_test "$@"
done

echo ""
echo "=== GUI examples ==="
for entry in "${GUI_TESTS[@]}"; do
    eval "set -- $entry"
    run_test "$@"
done

echo ""
echo "VERIFY: $pass pass / $fail fail / $skip skip"
[ ${#FAILS[@]} -eq 0 ] || echo "FAILED: ${FAILS[*]}"
[ ${#SKIPS[@]} -eq 0 ] || echo "SKIPPED: ${SKIPS[*]}"

if [ -n "$JSON_OUT" ]; then
    {
        printf '{\n  "entries": [\n'
        printf '%s' "$ENTRIES" | sed '$ s/,$//'
        printf '  ],\n  "pass": %d,\n  "fail": %d,\n  "skip": %d\n}\n' \
            "$pass" "$fail" "$skip"
    } >| "$JSON_OUT"
fi

# `exit $fail` truncates modulo 256 — with 256 failures this script would exit
# 0. There are 14 entries so it cannot happen today, but the gate reads the
# JSON, not this status, and a status that CAN encode success on failure should
# not be the thing anyone relies on.
[ "$fail" -eq 0 ] || exit 1
exit 0

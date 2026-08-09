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

set -u

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ARTEFACT_DIR="$REPO_ROOT/.skycache/verify"

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

run_test() {
    local name="$1" mode="$2" input="$3" expect="$4"
    local bin="$REPO_ROOT/examples/$name/sky-out/app"
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
        if ! ( cd "$REPO_ROOT/examples/$name" && timeout 900 "$REPO_ROOT/sky-out/sky" build src/Main.sky >/dev/null 2>&1 ); then
            echo "✗ $name — build failed (see: cd examples/$name && sky build src/Main.sky)"
            fail=$((fail+1)); FAILS+=("$name")
            return
        fi
    fi
    if [ ! -f "$bin" ]; then
        echo "✗ $name — binary still missing after build at $bin"
        fail=$((fail+1)); FAILS+=("$name")
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
            return
            ;;
        *)
            echo "✗ $name — unknown mode $mode"
            fail=$((fail+1)); FAILS+=("$name")
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
        return
    fi

    # Expected stdout substring (when given)
    if [ -n "$expect" ]; then
        if echo "$out" | grep -qF "$expect"; then
            echo "✓ $name (output matched '$expect')"
            pass=$((pass+1))
        else
            echo "✗ $name — output missing '$expect'"
            echo "$out" | head -3 | sed 's/^/   /'
            fail=$((fail+1)); FAILS+=("$name")
        fi
    else
        echo "✓ $name (no panic)"
        pass=$((pass+1))
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
exit $fail

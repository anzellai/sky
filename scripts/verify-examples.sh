#!/usr/bin/env bash
# scripts/verify-examples.sh — runtime verification for every example.
#
# Builds each example, runs it briefly, and fails if any of these
# conditions hold:
#
#   * non-zero exit from `sky build`
#   * non-zero exit from the built binary (CLI examples only)
#   * panic / runtime crash in stderr of the built binary
#   * server examples: HTTP probe returns non-2xx OR the process emits
#     "panic:" / "runtime error:" within the probe window
#   * CLI examples with an `expected.txt`: stdout differs from expected
#
# The sweep is a superset of `scripts/example-sweep.sh`: it builds AND
# runs, with runtime panic detection on top. Prefer this over
# `example-sweep.sh --build-only` when enforcing the "if it compiles,
# it works" gate end-to-end.
#
# Usage:
#     scripts/verify-examples.sh                   # all examples
#     scripts/verify-examples.sh 01-hello-world    # one
#     scripts/verify-examples.sh --build-only      # skip runtime phase

set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

SKY="$ROOT/sky-out/sky"
[[ -x "$SKY" ]] || { echo "missing $SKY — run cabal install first" >&2; exit 2; }

export SKY_RUNTIME_DIR="$ROOT/runtime-go"

BUILD_ONLY=0
TARGET=""
for arg in "$@"; do
    case "$arg" in
        --build-only) BUILD_ONLY=1 ;;
        --help|-h)
            sed -n '2,25p' "$0"; exit 0 ;;
        *)
            TARGET="$arg" ;;
    esac
done

# classification — mirrors scripts/example-sweep.sh
is_server() {
    case "$1" in
        05-mux-server|08-notes-app|09-live-counter|10-live-component \
        |12-skyvote|13-skyshop|15-http-server|16-skychess|17-skymon \
        |18-job-queue) return 0 ;;
        *) return 1 ;;
    esac
}

is_gui() {
    case "$1" in
        11-fyne-stopwatch) return 0 ;;
        *) return 1 ;;
    esac
}

PASS=0
FAIL=0
FAILURES=()

kill_port() {
    local port="$1"
    local pids
    pids=$(lsof -ti tcp:"$port" 2>/dev/null || true)
    [[ -n "$pids" ]] && kill -9 $pids 2>/dev/null || true
}

check_panic_log() {
    local log="$1"
    grep -Eq 'panic:|runtime error:|\[sky\.live\] panic' "$log" 2>/dev/null
}

verify_one() {
    local dir="$1"
    local name
    name=$(basename "$dir")
    local log="/tmp/sky-verify-$name.log"

    # 1. Clean + build
    (cd "$dir" && rm -rf sky-out .skycache)
    if ! (cd "$dir" && "$SKY" build src/Main.sky >"$log" 2>&1); then
        echo "  FAIL build: $name (see $log)"
        FAILURES+=("$name:build")
        return 1
    fi

    if [[ "$BUILD_ONLY" == "1" ]]; then
        echo "  build-only ok: $name"
        return 0
    fi

    # 2. Runtime
    if is_gui "$name"; then
        echo "  gui skipped runtime: $name (build ok)"
        return 0
    fi

    local bin="$dir/sky-out/app"
    if [[ ! -x "$bin" ]]; then
        echo "  FAIL missing bin: $name"
        FAILURES+=("$name:nobin")
        return 1
    fi

    if is_server "$name"; then
        # Boot, probe port, kill.
        local port
        port=$(grep -E '^port[[:space:]]*=' "$dir/sky.toml" 2>/dev/null | head -1 | sed -E 's/[^0-9]//g')
        port="${port:-8000}"
        kill_port "$port"
        # Launch app directly (not via subshell) so $pid is the real app.
        ( cd "$dir" && exec ./sky-out/app ) >"$log" 2>&1 &
        local pid=$!
        # Wait up to 10s for the port to accept.
        local tries=0
        local code="---"
        while (( tries < 20 )); do
            code=$(curl -s -o /dev/null -w "%{http_code}" --max-time 1 "http://localhost:$port/" 2>/dev/null)
            if [[ "$code" =~ ^(2|3)[0-9][0-9]$ ]]; then
                break
            fi
            sleep 0.5
            tries=$((tries+1))
        done
        [[ -z "$code" ]] && code="---"
        kill "$pid" 2>/dev/null
        wait "$pid" 2>/dev/null
        kill_port "$port"

        if check_panic_log "$log"; then
            echo "  FAIL panic: $name (see $log)"
            FAILURES+=("$name:panic")
            return 1
        fi
        if [[ ! "$code" =~ ^(2|3)[0-9][0-9]$ ]]; then
            echo "  FAIL http$code: $name (see $log)"
            FAILURES+=("$name:http$code")
            return 1
        fi
        echo "  runtime ok: $name (http $code)"
        return 0
    fi

    # CLI example: run and capture output. macOS doesn't ship `timeout`
    # natively; use `gtimeout` from coreutils if present, else run bare.
    local expected="$dir/expected.txt"
    local runner=""
    if command -v gtimeout >/dev/null 2>&1; then runner="gtimeout 15"
    elif command -v timeout >/dev/null 2>&1; then runner="timeout 15"
    fi
    local out
    if ! out=$(cd "$dir" && $runner "./sky-out/app" 2>"$log"); then
        echo "  FAIL exit: $name (see $log)"
        FAILURES+=("$name:exit")
        return 1
    fi
    if check_panic_log "$log"; then
        echo "  FAIL panic: $name (see $log)"
        FAILURES+=("$name:panic")
        return 1
    fi
    if [[ -f "$expected" ]]; then
        if ! diff -q <(echo "$out") "$expected" >/dev/null 2>&1; then
            echo "  FAIL expected.txt mismatch: $name"
            FAILURES+=("$name:expected")
            return 1
        fi
        echo "  runtime ok: $name (expected.txt matched)"
    else
        echo "  runtime ok: $name"
    fi
    return 0
}

# Walk examples
if [[ -n "$TARGET" ]]; then
    if [[ -d "examples/$TARGET" ]]; then
        verify_one "examples/$TARGET" && PASS=$((PASS+1)) || FAIL=$((FAIL+1))
    else
        echo "no such example: $TARGET" >&2; exit 2
    fi
else
    for dir in examples/*/; do
        dir="${dir%/}"
        [[ -f "$dir/sky.toml" ]] || continue
        if verify_one "$dir"; then
            PASS=$((PASS+1))
        else
            FAIL=$((FAIL+1))
        fi
    done
fi

echo
echo "verify: $PASS passed, $FAIL failed"
if (( FAIL > 0 )); then
    for f in "${FAILURES[@]}"; do echo "  - $f"; done
    exit 1
fi
exit 0

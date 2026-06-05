#!/usr/bin/env bash
# scripts/example-sweep.sh — canonical 20-example regression fence
# (19 historical + 27-multi-session-chat for the pub/sub umbrella).
#
# Builds every example from a clean slate. Optionally runs non-server
# examples (asserting exit 0 + non-empty stdout) and probes server
# examples (HTTP 200 on the configured port).
#
# Flags:
#   --build-only      only clean-build every example (default: runtime too)
#   --no-clean        keep existing sky-out/ .skycache/ (faster iteration)
#   --workdir DIR     copy examples/* into DIR/examples/ and run there
#                     instead of mutating the in-tree examples/. Fixes
#                     bug #381: cabal-test ExampleSweep racing TypedFfi
#                     by rm -rf'ing the in-tree sky-out/ + .skycache/go
#                     directories TypedFfi reads from. Cleaned up on
#                     exit via trap.
#   --jobs N          override parallel worker count. Default: read from
#                     scripts/lib/concurrency.sh (CPU/mem-aware). Set 1
#                     to force sequential mode (debugging / CI fallback).
#
# Exit 0 on full pass; non-zero and a failure list on any failure.
#
# v0.16.5 parallel mode: per-example work happens in xargs -P workers.
# Each worker writes a one-line result file to $RESULTS_DIR/$name.result
# (OK / SKIP <reason> / FAIL <message>) and the coordinator aggregates
# at the end. Server examples bind their pre-assigned port from the
# EXAMPLES table — no port allocator needed because each example owns
# a distinct port in the table.

set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# shellcheck source=lib/concurrency.sh
source "$ROOT/scripts/lib/concurrency.sh"

BUILD_ONLY=0
CLEAN=1
WORKDIR=""
JOBS_OVERRIDE=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        --build-only) BUILD_ONLY=1; shift ;;
        --no-clean)   CLEAN=0; shift ;;
        --workdir)
            shift
            [[ $# -gt 0 ]] || { echo "--workdir requires a directory argument" >&2; exit 2; }
            WORKDIR="$1"
            shift ;;
        --workdir=*)
            WORKDIR="${1#--workdir=}"
            shift ;;
        --jobs)
            shift
            [[ $# -gt 0 ]] || { echo "--jobs requires a count argument" >&2; exit 2; }
            JOBS_OVERRIDE="$1"
            shift ;;
        --jobs=*)
            JOBS_OVERRIDE="${1#--jobs=}"
            shift ;;
        --help|-h)
            sed -n '2,30p' "$0"; exit 0 ;;
        *) echo "unknown flag: $1" >&2; exit 2 ;;
    esac
done

if [[ -n "$JOBS_OVERRIDE" ]]; then
    MAX_WORKERS="$JOBS_OVERRIDE"
else
    MAX_WORKERS=$(compute_max_workers)
fi

SKY="$ROOT/sky-out/sky"
[[ -x "$SKY" ]] || { echo "missing $SKY — run scripts/build.sh first" >&2; exit 2; }

export SKY_RUNTIME_DIR="$ROOT/runtime-go"

# Workdir mode (bug #381): copy examples/* into $WORKDIR/examples/ and
# point EXAMPLES_ROOT at the copies. Stops cabal-test ExampleSweep from
# rm -rf'ing the in-tree sky-out/ + .skycache/go directories that
# TypedFfi / UnreachableGate specs read from while the sweep is mid-
# flight.
#
# Preserves $ROOT for SKY_RUNTIME_DIR (the runtime + stdlib live there
# and are NEVER mutated by the sweep). Only example dirs get copied.
#
# After a successful sweep we MIRROR THE WORKDIR BACK to the in-tree
# examples/. Two reasons:
#   1. Consumer specs (Sky.Build.TypedFfi, Sky.Build.UnreachableGate,
#      Sky.Build.SkyshopCompiles) read in-tree paths. Without the
#      mirror, a fresh checkout that runs `cabal test` would have
#      empty `examples/*/sky-out/` and the consumer specs would
#      cascade-fail.
#   2. Standalone `sky verify`/CI users expect the sweep to leave
#      the in-tree examples/ in a built state.
# The mirror is END-OF-SWEEP atomic from the consumer specs' point of
# view (sequential hspec ordering: ExampleSweep `it` block completes,
# THEN TypedFfi `it` block starts). No race possible.
#
# Cleanup: trap on EXIT removes $WORKDIR/examples — caller's wider
# workdir tree (if any) is left intact.
EXAMPLES_ROOT="$ROOT/examples"
if [[ -n "$WORKDIR" ]]; then
    mkdir -p "$WORKDIR/examples"
    EXAMPLES_ROOT="$WORKDIR/examples"
    # On macOS `cp -R src/. dst/` copies CONTENTS of src into dst (BSD
    # quirk). Use rsync where available for predictable exclude rules;
    # otherwise fall back to a per-example cp + post-prune of dirs the
    # sweep would rm anyway. We exclude `sky-out`, `.skycache/lowered`,
    # `.skycache/go` (the sweep recreates them) but KEEP `.skycache/ffi`
    # (15+ min to regenerate for skyshop's 76k FFI symbols), `.skydeps`,
    # and `sky.lock`.
    if command -v rsync >/dev/null 2>&1; then
        rsync -a \
            --exclude='sky-out' \
            --exclude='.skycache/lowered' \
            --exclude='.skycache/go' \
            "$ROOT/examples/" "$EXAMPLES_ROOT/"
    else
        cp -R "$ROOT/examples/." "$EXAMPLES_ROOT/"
        find "$EXAMPLES_ROOT" -mindepth 2 -maxdepth 3 \
            \( -name 'sky-out' -o -path '*/.skycache/lowered' \
               -o -path '*/.skycache/go' \) \
            -prune -exec rm -rf {} + 2>/dev/null || true
    fi
    # Cleanup on exit. Scope the rm to the examples subtree we created.
    # If the caller's WORKDIR was empty before we touched it (only the
    # `examples/` we added) we also remove the parent so $TMPDIR stays
    # clean across runs. `trap` runs on EXIT / SIGTERM / SIGINT.
    cleanup_workdir() {
        rm -rf "$EXAMPLES_ROOT" 2>/dev/null || true
        # Only remove the parent if it's empty (caller may have passed
        # a shared scratch dir they're populating in parallel — never
        # blow away their other contents).
        rmdir "$WORKDIR" 2>/dev/null || true
    }
    trap cleanup_workdir EXIT INT TERM
    echo "  [workdir] copied examples → $EXAMPLES_ROOT"
fi

# Mirror workdir builds back to in-tree examples/. Runs on success at
# the bottom of the script. Decoupled into a helper here so the trap
# arm + the success-path call can share the same logic.
mirror_back_to_intree() {
    [[ -z "$WORKDIR" ]] && return 0  # No workdir → nothing to mirror.
    # rsync just the sweep's built artifacts back. We DON'T touch
    # source-tree files (src/, sky.toml, …) — the workdir's copies are
    # byte-identical to the originals (we copied them in earlier).
    # Mirroring those would be a no-op but burns IO; selective rsync
    # also makes the operation visibly atomic in the in-tree mtimes.
    if command -v rsync >/dev/null 2>&1; then
        rsync -a --include='*/' \
            --include='sky-out/***' \
            --include='.skycache/lowered/***' \
            --include='.skycache/go/***' \
            --exclude='*' \
            "$EXAMPLES_ROOT/" "$ROOT/examples/"
    else
        # Per-example cp of just the sweep-produced subtrees.
        for d in "$EXAMPLES_ROOT"/*/; do
            local name
            name="$(basename "$d")"
            for sub in sky-out .skycache/lowered .skycache/go; do
                if [[ -d "$d/$sub" ]]; then
                    rm -rf "$ROOT/examples/$name/$sub" 2>/dev/null || true
                    mkdir -p "$ROOT/examples/$name/$(dirname "$sub")" 2>/dev/null || true
                    cp -R "$d/$sub" "$ROOT/examples/$name/$sub"
                fi
            done
        done
    fi
}

# Cross-platform `timeout`. macOS doesn't ship GNU coreutils, so the
# bare `timeout` binary is missing on default GitHub `macos-latest`
# runners — without this shim the CLI sweep step would fail every
# example with exit 127 ("command not found") interpreted as a
# non-zero app exit. Order: GNU `timeout` (Linux + nix Macs) →
# Homebrew `gtimeout` → portable bg-pid + sleep + kill fallback.
if command -v timeout >/dev/null 2>&1; then
    TIMEOUT_CMD="timeout"
elif command -v gtimeout >/dev/null 2>&1; then
    TIMEOUT_CMD="gtimeout"
else
    TIMEOUT_CMD=""
fi

run_with_timeout() {
    # run_with_timeout SECONDS CMD [ARGS...]
    local secs="$1"; shift
    if [[ -n "$TIMEOUT_CMD" ]]; then
        "$TIMEOUT_CMD" "$secs" "$@"
        return $?
    fi
    # No GNU timeout available → portable fallback. Spawn the command
    # in the background, race a sleeping killer against it, surface
    # the command's real exit on natural completion or 124 (matching
    # GNU timeout's convention) on enforced kill.
    "$@" &
    local cmd_pid=$!
    ( sleep "$secs" && kill -KILL "$cmd_pid" 2>/dev/null ) &
    local killer_pid=$!
    local rc=0
    wait "$cmd_pid" 2>/dev/null; rc=$?
    # If the killer fired, the wait above sees the killed status.
    # We can't reliably distinguish "killed by us" vs "user killed"
    # from rc alone, so check whether killer is still alive: if it
    # has already exited it likely fired (rc=124 convention).
    if ! kill -0 "$killer_pid" 2>/dev/null; then
        # killer already exited → either fired (and killed us), or
        # raced with natural completion. Conservative: report 124
        # only when rc indicates a kill signal.
        if [[ $rc -gt 128 ]]; then rc=124; fi
    fi
    kill -KILL "$killer_pid" 2>/dev/null
    wait "$killer_pid" 2>/dev/null
    return $rc
}

# Examples are classified by runtime behaviour.
# server examples: start a listener; probe HTTP; kill after probe.
# gui examples: require a display; build-only (skip runtime).
# cli examples: exit 0, stdout non-empty.
#
# Entries: "name:kind[:port][:path]"
declare -a EXAMPLES=(
    "01-hello-world:cli"
    "02-go-stdlib:cli"
    "03-tea-external:cli"
    "04-local-pkg:cli"
    "05-mux-server:server:8000:/"
    "06-json:cli"
    "07-todo-cli:cli"
    "08-notes-app:server:8000:/"
    "09-live-counter:server:8000:/"
    "10-live-component:server:8000:/"
    "11-fyne-stopwatch:gui"
    "12-skyvote:server:8000:/"
    "13-skyshop:server:8000:/"
    "14-task-demo:cli"
    "15-http-server:server:8000:/"
    "16-skychess:server:8000:/"
    "17-skymon:server:8000:/"
    "18-job-queue:server:8000:/"
    "19-skyforum:server:8000:/"
    # 26 — Std.Ui kitchen-sink showcase. Server-shaped because the
    # Sky.Live runtime serves the HTML; scripts/verify-ui-showcase.sh
    # runs the deep visual-regression sweep separately.
    "26-ui-showcase:server:8000:/"
    "27-multi-session-chat:server:8000:/"
    "30-sse-server-demo:server:8000:/"
    # 32 — SSE relay (#373): Sky.Http.Server handler synchronously
    # drains an upstream Http.Stream + re-emits via Server.Stream.emit
    # chunk-for-chunk. Uses port 8001 to avoid colliding with peers.
    "32-sse-relay:server:8001:/"
    # 33 — WebSocket echo (#388 v0.15.46). Server upgrades incoming
    # GET /ws and echoes back. Sweep checks plain HTTP GET / for the
    # index page, NOT the /ws upgrade itself (which curl can't speak).
    "33-websocket-echo:server:8033:/"
    # 29 — Sky.Webview spike: Three.js + WebGL2 under the new
    # loopback-asset pipeline (bug #370). Same gui-kind skip
    # semantics as 31 (display + macOS-only cgo).
    "29-webview-threejs-spike:gui"
    # 31 — Sky.Webview MVP. Native desktop window; build-only sweep
    # (running needs a display, same skip semantics as the Fyne GUI
    # example). v0.1 is macOS only.
    "31-webview-stopwatch-ui:gui"
)

# Per-worker result files. Each call to run_example writes ONE LINE
# to $RESULTS_DIR/$name.result of the form:
#   OK\n
#   SKIP <reason>\n
#   FAIL <message>\n
# The coordinator at the end of the script aggregates and reports.
RESULTS_DIR=$(mktemp -d "${TMPDIR:-/tmp}/sky-sweep-results.XXXXXX")
trap 'rm -rf "$RESULTS_DIR" 2>/dev/null || true' EXIT

run_example() {
    local name="$1" kind="$2" port="${3:-}" path="${4:-/}"
    local dir="$EXAMPLES_ROOT/$name"
    local result_file="$RESULTS_DIR/$name.result"

    [[ -d "$dir" ]] || { printf 'FAIL missing directory\n' > "$result_file"; return; }

    # GUI examples (Fyne) need X11/GTK dev libs on Linux. On a headless
    # CI runner without them, the Go build pulls in cgo deps that fail
    # at link time. Honoured by `sky verify` / `sky test` too.
    # Set SKIP_GUI_LINUX=0 in an env with the libs installed to override.
    if [[ "$kind" == "gui" && "$(uname -s)" == "Linux" && "${SKIP_GUI_LINUX:-1}" == "1" ]]; then
        printf 'SKIP GUI example on Linux (set SKIP_GUI_LINUX=0 to run)\n' > "$result_file"
        return
    fi

    (
        cd "$dir"
        if [[ $CLEAN -eq 1 ]]; then
            # Clean the generated output and the source-hashed lowered
            # cache, but keep `.skycache/ffi/` (FFI bindings — regenerating
            # these for skyshop costs 15+ min of Stripe+Firebase
            # introspection each sweep) and `.skydeps/` (Sky-package
            # lockfile). The compiler invalidates `ffi/` entries on
            # upstream Go module change via content hash, so keeping
            # them between sweeps is safe.
            rm -rf sky-out .skycache/lowered .skycache/go
        fi
        if [[ -f sky.toml ]] && grep -qE '^\["?go\.dependencies"?\]' sky.toml; then
            "$SKY" install >/tmp/sky-install-"$name".log 2>&1 || { echo "install failed"; exit 2; }
        fi
        "$SKY" build src/Main.sky >/tmp/sky-build-"$name".log 2>&1
    ) || { printf 'FAIL build failed — /tmp/sky-build-%s.log\n' "$name" > "$result_file"; return; }

    if [[ $BUILD_ONLY -eq 1 || "$kind" == "gui" ]]; then
        printf 'OK\n' > "$result_file"
        return
    fi

    local bin="$dir/sky-out/app"
    [[ -x "$bin" ]] || { printf 'FAIL %s missing\n' "$bin" > "$result_file"; return; }

    case "$kind" in
        cli)
            local out rc=0
            # #367 — keep HTTP client tail-latency under the per-example
            # budget (10s). The runtime's default is 30s which exceeds
            # the sweep budget and flakes on slow upstreams (httpbin
            # was the trigger). 5s is plenty for a healthy connection
            # and surfaces a graceful Err on a wedged one.
            out=$( (cd "$dir" && SKY_HTTP_CLIENT_TIMEOUT=5s run_with_timeout 10 "$bin") 2>&1 ) || rc=$?
            if [[ $rc -ne 0 ]]; then
                printf 'FAIL cli non-zero exit (rc=%s) — last 20 lines: %s\n' "$rc" "$(printf '%s' "$out" | tail -20 | tr '\n' ' | ')" > "$result_file"
                return
            fi
            [[ -n "$out" ]] || { printf 'FAIL empty stdout\n' > "$result_file"; return; }
            printf 'OK\n' > "$result_file" ;;
        server)
            local pid log url
            log=$(mktemp)
            url="http://127.0.0.1:${port}${path}"
            (cd "$dir" && "$bin" >"$log" 2>&1) &
            pid=$!
            local ok=0 tries=0
            while [[ $tries -lt 30 ]]; do
                if curl -s -o /dev/null -w '%{http_code}' --max-time 1 "$url" 2>/dev/null | grep -qE '^(2|3)[0-9][0-9]$'; then
                    ok=1; break
                fi
                sleep 0.2; tries=$((tries+1))
            done
            kill -9 "$pid" 2>/dev/null
            wait "$pid" 2>/dev/null
            if [[ $ok -eq 1 ]]; then
                printf 'OK\n' > "$result_file"
            else
                printf 'FAIL no HTTP 2xx/3xx at %s — log %s\n' "$url" "$log" > "$result_file"
            fi
            rm -f "$log" ;;
        *) printf 'FAIL unknown kind %s\n' "$kind" > "$result_file" ;;
    esac
}

# Worker entry — invoked via xargs -P. Single arg: colon-encoded entry
# from $EXAMPLES (name:kind[:port[:path]]). We re-source the script's
# globals via env — RESULTS_DIR, EXAMPLES_ROOT, SKY, SKY_RUNTIME_DIR,
# CLEAN, BUILD_ONLY — all already exported in the parent shell.
sweep_worker() {
    local entry="$1"
    local name kind port path
    IFS=':' read -r name kind port path <<<"$entry"
    run_example "$name" "$kind" "$port" "${path:-/}"
}

# Export the worker + helper functions + env for xargs subshells.
export -f run_example sweep_worker run_with_timeout
export EXAMPLES_ROOT SKY SKY_RUNTIME_DIR CLEAN BUILD_ONLY
export RESULTS_DIR SKIP_GUI_LINUX TIMEOUT_CMD

# Display banner + parallel summary.
echo
printf '  building %d examples with %d worker(s)\n' "${#EXAMPLES[@]}" "$MAX_WORKERS"
describe_concurrency | sed 's/^/  /'
echo

# Print one "starting" line per example so progress is visible while
# workers run.  Each worker still writes to $RESULTS_DIR.
for entry in "${EXAMPLES[@]}"; do
    IFS=':' read -r name kind _port _path <<<"$entry"
    printf '   %-22s %s\n' "$name" "$kind"
done

# Fan out across xargs -P workers. Each subshell receives one entry
# string on stdin; sweep_worker parses it and writes its result.
printf '%s\n' "${EXAMPLES[@]}" \
    | xargs -P "$MAX_WORKERS" -I {} bash -c 'sweep_worker "$@"' _ {}

# Aggregate results.
pass=0; fail=0
declare -a failures=()
for entry in "${EXAMPLES[@]}"; do
    IFS=':' read -r name _ _ _ <<<"$entry"
    local_result="$RESULTS_DIR/$name.result"
    if [[ ! -r "$local_result" ]]; then
        failures+=("$name: NO RESULT (worker crashed or never ran)")
        fail=$((fail+1))
        continue
    fi
    line=$(head -1 "$local_result")
    case "$line" in
        OK)        pass=$((pass+1)) ;;
        SKIP\ *)   pass=$((pass+1)) ;;
        FAIL\ *)   failures+=("$name: ${line#FAIL }"); fail=$((fail+1)) ;;
        *)         failures+=("$name: malformed result '$line'"); fail=$((fail+1)) ;;
    esac
done

echo
echo "sweep: $pass passed, $fail failed"
if [[ $fail -gt 0 ]]; then
    printf '  - %s\n' "${failures[@]}"
    # Dump the build log for every failed example so CI shows the real
    # compile error, not just "example foo: build failed". Without this
    # the failure message points at a /tmp path the CI runner no longer
    # has by the time the log is archived.
    for f in "${failures[@]}"; do
        name="${f%%:*}"
        log="/tmp/sky-build-$name.log"
        if [[ -r "$log" ]]; then
            echo
            echo "─── $log ───"
            tail -60 "$log"
        fi
    done
    exit 1
fi

# Sweep succeeded; mirror workdir builds back to in-tree examples/.
# No-op when --workdir was not passed. See mirror_back_to_intree()
# header above the trap for the contract + race semantics.
mirror_back_to_intree
if [[ -n "$WORKDIR" ]]; then
    echo "  [workdir] mirrored builds back to $ROOT/examples/"
fi

# ─── post-sweep hygiene: keep go-build cache from growing without bound ───
# CLAUDE.md §6 — a full sweep of 30+ examples adds 5-15 GB of incremental
# go-build entries that auto-prune doesn't catch on macOS. Reclaim
# aggressively when cache exceeds 5 GB. Safe: fresh builds always work.
#
# Cross-platform path detection — Linux uses $XDG_CACHE_HOME or
# ~/.cache/go-build; the macOS-only hardcoded path made `du` exit
# non-zero on Linux CI (v0.16.3 549d4701 build-and-test failure).
go_cache_dir="${HOME}/Library/Caches/go-build"
[[ -d "$go_cache_dir" ]] || go_cache_dir="${XDG_CACHE_HOME:-$HOME/.cache}/go-build"
if [[ -d "$go_cache_dir" ]]; then
    cache_kb=$(du -sk "$go_cache_dir" 2>/dev/null | awk '{print $1}')
    cache_kb=${cache_kb:-0}
    if [[ "$cache_kb" -gt 5242880 ]]; then
        cache_gb=$(( cache_kb / 1048576 ))
        echo "  [hygiene] go-build cache is ${cache_gb} GB — running 'go clean -cache'"
        go clean -cache 2>/dev/null || true
    fi
fi

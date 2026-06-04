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
#
# Exit 0 on full pass; non-zero and a failure list on any failure.

set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

BUILD_ONLY=0
CLEAN=1
WORKDIR=""
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
        --help|-h)
            sed -n '2,21p' "$0"; exit 0 ;;
        *) echo "unknown flag: $1" >&2; exit 2 ;;
    esac
done

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

pass=0; fail=0
declare -a failures=()

run_example() {
    local name="$1" kind="$2" port="${3:-}" path="${4:-/}"
    local dir="$EXAMPLES_ROOT/$name"
    [[ -d "$dir" ]] || { failures+=("$name: missing directory"); fail=$((fail+1)); return; }

    # GUI examples (Fyne) need X11/GTK dev libs on Linux. On a headless
    # CI runner without them, the Go build pulls in cgo deps that fail
    # at link time. Honoured by `sky verify` / `sky test` too.
    # Set SKIP_GUI_LINUX=0 in an env with the libs installed to override.
    if [[ "$kind" == "gui" && "$(uname -s)" == "Linux" && "${SKIP_GUI_LINUX:-1}" == "1" ]]; then
        echo "  [skip] $name: GUI example on Linux (set SKIP_GUI_LINUX=0 to run)"
        pass=$((pass+1))
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
    ) || { failures+=("$name: build failed — /tmp/sky-build-$name.log"); fail=$((fail+1)); return; }

    if [[ $BUILD_ONLY -eq 1 || "$kind" == "gui" ]]; then
        pass=$((pass+1)); return
    fi

    local bin="$dir/sky-out/app"
    [[ -x "$bin" ]] || { failures+=("$name: $bin missing"); fail=$((fail+1)); return; }

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
                failures+=("$name: cli non-zero exit (rc=$rc) — last 20 lines: $(printf '%s' "$out" | tail -20 | tr '\n' ' | ')")
                fail=$((fail+1)); return
            fi
            [[ -n "$out" ]] || { failures+=("$name: empty stdout"); fail=$((fail+1)); return; }
            pass=$((pass+1)) ;;
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
                pass=$((pass+1))
            else
                failures+=("$name: no HTTP 2xx/3xx at $url — log $log")
                fail=$((fail+1))
            fi
            rm -f "$log" ;;
        *) failures+=("$name: unknown kind '$kind'"); fail=$((fail+1)) ;;
    esac
}

for entry in "${EXAMPLES[@]}"; do
    IFS=':' read -r name kind port path <<<"$entry"
    printf '   %-22s %s\n' "$name" "$kind"
    run_example "$name" "$kind" "$port" "$path"
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

#!/usr/bin/env bash
# scripts/example-sweep.sh — canonical 20-example regression fence.
#
# Builds every example from a clean slate. Optionally runs non-server
# examples (asserting exit 0 + non-empty stdout) and probes server
# examples (HTTP 200 on the configured port).
#
# Flags:
#   --build-only   only clean-build every example (default: runtime too)
#   --no-clean     keep existing sky-out/ .skycache/ (faster iteration)
#
# Exit 0 on full pass; non-zero and a failure list on any failure.

set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

BUILD_ONLY=0
CLEAN=1
for arg in "$@"; do
    case "$arg" in
        --build-only) BUILD_ONLY=1 ;;
        --no-clean)   CLEAN=0 ;;
        --help|-h)
            sed -n '2,15p' "$0"; exit 0 ;;
        *) echo "unknown flag: $arg" >&2; exit 2 ;;
    esac
done

SKY="$ROOT/sky-out/sky"
[[ -x "$SKY" ]] || { echo "missing $SKY — run scripts/build.sh first" >&2; exit 2; }

export SKY_RUNTIME_DIR="$ROOT/runtime-go"

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
)

pass=0; fail=0
declare -a failures=()

run_example() {
    local name="$1" kind="$2" port="${3:-}" path="${4:-/}"
    local dir="$ROOT/examples/$name"
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
            local out
            out=$( (cd "$dir" && timeout 10 "$bin") 2>&1 ) || {
                failures+=("$name: cli non-zero exit"); fail=$((fail+1)); return; }
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

#!/usr/bin/env bash
#
# scripts/console-controls-e2e.sh — every control of the Sky Console, operated
# in Chromium AND WebKit, with its effect asserted (scripts/console-controls-e2e.mjs).
#
# Field report behind it (v0.27.7): "many of the toggles/ranges aren't really
# working". They were not: the range chips filtered the newest 200 log lines in
# the browser (so 24h, 7d and All showed the same rows) and compared a UTC
# threshold with times stamped in the server's zone; the range did nothing on
# Errors and Analytics; the searches and the session pivot only saw those 200
# rows; a log row's trace badge opened a Traces tab without the trace; a
# console link opened on the defaults; "Sign out" showed where there was no
# sign-in; the hub's level toggles ignored any selection of two or three
# levels. scripts/console-live-e2e.sh proves the console shows LIVE data; this
# gate proves each control does what it says.
#
# Scenarios (each in both browsers):
#   embedded  the console every app mounts, ENV=production, token auth,
#             strict CSP, app and browser in America/New_York
#   dev       the same app with no ENV: open console, no "Sign out"
#   hub       `sky console-serve --auth off` with two services
#
# Proven to FAIL on v0.27.7 (CONSOLE_CONTROLS_E2E_SKY=<a v0.27.7 sky>) and PASS
# on the fixed tree.
#
# Prereqs (all fail loudly): a fresh sky-out/sky, go, node + playwright
# (Chromium and WebKit), sqlite3, openssl.
#
# Env:
#   CONSOLE_CONTROLS_E2E_PORT     first of 10 ports used (default 9660)
#   CONSOLE_CONTROLS_E2E_SKY      measure another compiler (e.g. a released
#                                 one, to prove the gate fails there)
#   CONSOLE_CONTROLS_E2E_ONLY     scenarios to run, comma-separated
#                                 (embedded,dev,hub)
#   CONSOLE_CONTROLS_E2E_BROWSERS browsers to run, comma-separated
#                                 (chromium,webkit)
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
source "$ROOT/scripts/lib/with-timeout.sh"
source "$ROOT/scripts/lib/require-tool.sh"

if [ -n "${CONSOLE_CONTROLS_E2E_SKY:-}" ]; then
  SKY="$CONSOLE_CONTROLS_E2E_SKY"
  echo "console-controls-e2e: MEASURING $SKY ($("$SKY" --version 2>/dev/null || echo '?')), NOT this tree's compiler" >&2
else
  SKY="$ROOT/sky-out/sky"
  if [ ! -x "$SKY" ]; then
    echo "console-controls-e2e: $SKY not found — run ./scripts/build.sh first." >&2
    exit 1
  fi
  source "$ROOT/scripts/lib/fresh-compiler.sh"
  require_fresh_compiler "$SKY" "$ROOT"
fi
require_tool node "install Node.js (and 'npm ci' for playwright)"
require_tool go "install Go (https://go.dev/dl/)"
require_tool sqlite3 "install the sqlite3 command-line client"
require_tool openssl "install OpenSSL (the embedded scenario serves the console over a local TLS front)"

source "$ROOT/scripts/lib/gate-build-cache.sh"
_gc_compiler_hash "$SKY" >/dev/null
TMP="$(gate_e2e_dir "$ROOT" console-controls-e2e)"
BASE_PORT="${CONSOLE_CONTROLS_E2E_PORT:-9660}"

SCENARIOS="embedded dev hub"
BROWSERS="chromium webkit"
pick() { # pick <known> <requested, comma-separated> <what>
  local known="$1" req="$2" what="$3" out="" s
  [ -z "$req" ] && { echo "$known"; return 0; }
  for s in ${req//,/ }; do
    case " $known " in
      *" $s "*) out="$out $s" ;;
      *)
        echo "console-controls-e2e: unknown $what '$s' (known: $known)" >&2
        return 1
        ;;
    esac
  done
  echo "$out"
}
SCENARIOS="$(pick "$SCENARIOS" "${CONSOLE_CONTROLS_E2E_ONLY:-}" scenario)"
BROWSERS="$(pick "$BROWSERS" "${CONSOLE_CONTROLS_E2E_BROWSERS:-}" browser)"

APP_BIN=""
case " $SCENARIOS " in
  *" embedded "* | *" dev "*)
    src="$ROOT/rust/crates/sky/tests/fixtures/console-controls"
    rm -rf "$TMP/console-controls"
    mkdir -p "$TMP/console-controls"
    cp -Rf "$src/." "$TMP/console-controls/"
    rm -rf "$TMP/console-controls/.skyapp" "$TMP/console-controls/sky-out" "$TMP/console-controls/.skycache"
    echo "==> building rust/crates/sky/tests/fixtures/console-controls (--target web)"
    with_timeout 1500 bash "$ROOT/scripts/lib/gate-build-cache.sh" build "$SKY" "$TMP/console-controls" \
      --clean --artefact .skyapp/web -- build --target web src/Main.sky
    APP_BIN="$TMP/console-controls/.skyapp/web/sky-out/app"
    [ -x "$APP_BIN" ] || { echo "console-controls-e2e: app not built at $APP_BIN" >&2; exit 1; }
    ;;
esac

rc=0
runs=0
port="$BASE_PORT"
for scenario in $SCENARIOS; do
  for browser in $BROWSERS; do
    runs=$((runs + 1))
    work="$TMP/run-$scenario-$browser"
    rm -rf "$work"
    mkdir -p "$work"
    echo "==> $scenario in $browser"
    args=(--scenario "$scenario" --browser "$browser" --port "$port" --work "$work")
    if [ "$scenario" = hub ]; then
      args+=(--sky "$SKY")
    else
      args+=(--app "$APP_BIN" --cwd "$(dirname "$APP_BIN")")
    fi
    # The hub builds its daemon on first use (one `go build`), so its run
    # gets more time.
    # A failing control waits out its 10 s check, so a red run is slow.
    budget=900
    [ "$scenario" = hub ] && budget=1200
    with_timeout "$budget" node "$ROOT/scripts/console-controls-e2e.mjs" "${args[@]}" || rc=1
    port=$((port + 3))
  done
done

if [ "$runs" -eq 0 ]; then
  echo "console-controls-e2e: FAIL — no scenario ran." >&2
  exit 1
fi
if [ "$rc" -ne 0 ]; then
  echo "console-controls-e2e: FAIL — a Sky Console control did not do what it says (see the FAIL lines above)." >&2
  exit 1
fi
echo "console-controls-e2e: PASS — every Sky Console control works: tabs, range chips, level toggles, searches, session and trace pivots, console links, auto-refresh, sign-in and sign-out (embedded, dev, hub; Chromium and WebKit)."
rm -rf "$TMP"

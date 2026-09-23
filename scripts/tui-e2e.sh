#!/usr/bin/env bash
#
# scripts/tui-e2e.sh — pty e2e regression for the terminal TEA loops
# (terminal:tui Element + String views, terminal:cli).
#
# Builds the four fixtures under rust/crates/sky/tests/fixtures/tui-e2e and:
#   * drives the three terminal:tui apps in a real pseudo-terminal through
#     scripts/tui-e2e-drive.py (pyte reads the screen back): queued keys act on
#     the current frame, a Ui.width input stays one row, split UTF-8 / escape /
#     bracketed-paste reads decode whole keys, a slow Sub.every fires under
#     fast ticks, Cmd.publish reaches Sub.subscribeTopic, Alt+key reaches
#     onKey, Ctrl-C quits with onKey set, withDurable restores the model, an
#     App.tui String view draws at column 0 and quits on q, and the Std.Ui
#     controls work (focus by identity, textarea, form submit, slider,
#     onEnter, the App.withInput line prompt);
#   * runs the terminal:cli app with no input handler: it must exit 0 after
#     its slow Cmd.perform landed, with the published payload delivered and
#     the guarded Msg rejected.
#
# Proven to FAIL on a pre-fix compiler and PASS on the fixed one.
#
# Prereqs (all fail loudly): a fresh sky-out/sky, go, python3 (pyte is taken
# from the environment or installed into a throwaway venv).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SKY="$ROOT/sky-out/sky"
if [ ! -x "$SKY" ]; then
  echo "tui-e2e: $SKY not found — run ./scripts/build.sh first." >&2
  exit 1
fi
source "$ROOT/scripts/lib/fresh-compiler.sh"
require_fresh_compiler "$SKY" "$ROOT"
source "$ROOT/scripts/lib/require-tool.sh"
require_tool python3 "install Python 3"
require_tool go "install Go (https://go.dev/dl/)"
source "$ROOT/scripts/lib/with-timeout.sh"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

PY=python3
if ! python3 -c 'import pyte' >/dev/null 2>&1; then
  echo "==> installing pyte into a throwaway venv"
  python3 -m venv "$WORK/venv"
  "$WORK/venv/bin/pip" install --quiet pyte || {
    echo "tui-e2e: could not install pyte (pip install pyte)" >&2
    exit 1
  }
  PY="$WORK/venv/bin/python"
fi

for fx in app string forms cli; do
  mkdir -p "$WORK/$fx"
  cp -Rf "$ROOT/rust/crates/sky/tests/fixtures/tui-e2e/$fx/." "$WORK/$fx/"
  echo "==> building the tui-e2e $fx fixture"
  ( cd "$WORK/$fx" && with_timeout 600 "$SKY" build src/Main.sky )
done

bin_of() {
  local dir="$1" target="$2"
  local b="$dir/.skyapp/$target/sky-out/app"
  [ -x "$b" ] || b="$dir/sky-out/app"
  [ -x "$b" ] || { echo "tui-e2e: no binary built under $dir" >&2; exit 1; }
  echo "$b"
}
APP_BIN="$(bin_of "$WORK/app" terminal-tui)"
STR_BIN="$(bin_of "$WORK/string" terminal-tui)"
FORMS_BIN="$(bin_of "$WORK/forms" terminal-tui)"
CLI_BIN="$(bin_of "$WORK/cli" terminal-cli)"

echo "==> driving the terminal:tui fixtures in a pty"
with_timeout 300 "$PY" "$ROOT/scripts/tui-e2e-drive.py" "$WORK/app" "$APP_BIN" "$WORK/string" "$STR_BIN" \
  "$WORK/forms" "$FORMS_BIN"

echo "==> running the terminal:cli fixture without an input handler"
set +e
CLI_OUT="$(cd "$WORK/cli" && with_timeout 30 "$CLI_BIN" </dev/null 2>&1)"
CLI_RC=$?
set -e
fail=0
[ "$CLI_RC" -eq 0 ] || { echo "FAIL  terminal:cli without withInput exits 0 (got $CLI_RC)"; fail=1; }
case "$CLI_OUT" in *"loaded=yes heard=hello secret=hidden"*) echo "PASS  cli: perform landed + publish delivered + guard held" ;;
  *) echo "FAIL  cli: expected 'loaded=yes heard=hello secret=hidden'"; fail=1 ;; esac
case "$CLI_OUT" in *REVEALED*) echo "FAIL  cli: the guarded Msg reached update"; fail=1 ;; esac
if [ "$fail" -ne 0 ]; then
  echo "---- cli output ----"
  echo "$CLI_OUT"
  exit 1
fi

echo "tui-e2e: PASS — terminal loops honour keys, timers, pub/sub, guards, durability and exit rules."

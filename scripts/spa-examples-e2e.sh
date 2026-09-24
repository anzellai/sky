#!/usr/bin/env bash
#
# scripts/spa-examples-e2e.sh — browser e2e for the two Sky.Spa (web:app)
# auto-split examples whose persistence paths a UI sweep found broken:
#
#   * examples/63-app-chat — a second client (or a reload) loads the chat
#     history over the `Load` RPC. The history rows rendered with no author and
#     no text: the generated response record `LoadResp { messages : List
#     Message }` lowered its field to the same-tailed stdlib
#     `Std.Ai.Provider.Message`, and the constructor zeroed every field
#     (compiler, rust/crates/lower collect_types). Also: the dev Console badge
#     must not cover the Send button.
#   * examples/62-app-notes — "New note", type a title, Save must persist the
#     title into THAT note. `Create` left `selected = 0`, so Save updated no row
#     and the sidebar lagged one action behind (example logic).
#
# Builds each example from a scratch copy (never from the repo tree) and drives
# scripts/spa-examples-e2e-verify.mjs. Proven to FAIL on the pre-fix compiler
# (63: an empty history row) and on the pre-fix 62 source (the second note is
# never saved), and PASS on the fixed tree.
#
# Prereqs (all fail loudly): a fresh sky-out/sky, node + playwright.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SKY="$ROOT/sky-out/sky"
if [ ! -x "$SKY" ]; then
  echo "spa-examples-e2e: $SKY not found — run ./scripts/build.sh first." >&2
  exit 1
fi
source "$ROOT/scripts/lib/fresh-compiler.sh"
require_fresh_compiler "$SKY" "$ROOT"
source "$ROOT/scripts/lib/with-timeout.sh"
command -v node >/dev/null 2>&1 || { echo "spa-examples-e2e: 'node' is required." >&2; exit 1; }

TMP="$(mktemp -d)"
build_example() { # build_example <example-dir-name>
  local name="$1"
  mkdir -p "$TMP/$name"
  cp -Rf "$ROOT/examples/$name/." "$TMP/$name/"
  rm -rf "$TMP/$name/.skyapp" "$TMP/$name/sky-out" "$TMP/$name/.skycache"
  echo "==> building examples/$name (--target web:app)"
  ( cd "$TMP/$name" && with_timeout 1200 "$SKY" build --target web:app src/Main.sky )
  local app="$TMP/$name/.skyapp/web-app/.split/backend/sky-out/app"
  [ -x "$app" ] || { echo "spa-examples-e2e: backend not built at $app" >&2; exit 1; }
}

build_example 62-app-notes
build_example 63-app-chat

echo "==> driving examples/62-app-notes"
with_timeout 300 node "$ROOT/scripts/spa-examples-e2e-verify.mjs" notes \
  "$TMP/62-app-notes/.skyapp/web-app/.split/backend/sky-out/app" --port "${NOTES_PORT:-9341}"
echo "==> driving examples/63-app-chat"
with_timeout 300 node "$ROOT/scripts/spa-examples-e2e-verify.mjs" chat \
  "$TMP/63-app-chat/.skyapp/web-app/.split/backend/sky-out/app" --port "${CHAT_PORT:-9342}"

echo "spa-examples-e2e: PASS — notes persist each Save; chat history loads with author + text; the dev badge clears Send."
rm -rf "$TMP"

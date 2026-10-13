#!/usr/bin/env bash
#
# scripts/ai-device-e2e.sh — browser e2e for Std.Ai.Device (docs/ai/device.md).
#
# Builds the probe fixture rust/crates/sky/tests/fixtures/ai-device as a
# web:app with both browser engines (--ai-web-engine both) and drives it under
# SKY_CSP=strict (scripts/ai-device-verify.mjs):
#
#   chromium-cpu         the CPU engine in a Web Worker: downloaded from the app's
#                        own server, checked, answered on the device, and no call
#                        reached the server (the privacy gate); no violation of
#                        the strict policy
#   chromium-webgpu      the same on WebGPU (--webgpu: needs a real GPU adapter
#                        with shader-f16, so a local run, not a CI runner)
#   webkit-cpu           WebKit (no JSPI, so never the WebGPU engine) on the CPU
#   webkit-private       a WebKit context that keeps no storage: the server
#                        answers through the fallback call, nothing downloads
#   server-placement     SKY_AI_PROBE_PLACEMENT=server: the server answers
#   device-only-private  SKY_AI_PROBE_PLACEMENT=device with no storage: a typed
#                        StorageDenied, and no call to the server
#
# The model: by default the synthetic Qwen2 model and adapter
# (`go run ./cmd/sky-tiny-gguf`), set through the operator variables
# SKY_AI_PROBE_MODEL / SKY_AI_PROBE_DEVICE_ADAPTER, so a CI runner needs no
# 430 MB download; --dev-model uses the development model (Qwen2.5 0.5B).
#
# Prereqs (all fail loudly): a fresh sky-out/sky, go, node + playwright (`npm ci`
# in the repo root, then `npx playwright install chromium webkit`), the
# web engines (`sky ai provision --device web`) and the server's engine
# (`sky ai provision`).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SKY="$ROOT/sky-out/sky"
[ -x "$SKY" ] || { echo "ai-device-e2e: $SKY not found — run ./scripts/build.sh first." >&2; exit 1; }
source "$ROOT/scripts/lib/fresh-compiler.sh"
require_fresh_compiler "$SKY" "$ROOT"
source "$ROOT/scripts/lib/with-timeout.sh"
source "$ROOT/scripts/lib/require-tool.sh"
require_tool node "install Node.js 20+"
require_tool go "install Go 1.25+"

MODEL=tiny
SCENARIOS="chromium-cpu,webkit-cpu,webkit-private,server-placement,device-only-private"
while [ $# -gt 0 ]; do
  case "$1" in
    --dev-model) MODEL=dev; shift ;;
    --webgpu) SCENARIOS="$SCENARIOS,chromium-webgpu"; shift ;;
    --scenarios) SCENARIOS="$2"; shift 2 ;;
    *) echo "usage: ai-device-e2e.sh [--dev-model] [--webgpu] [--scenarios a,b]" >&2; exit 2 ;;
  esac
done

# The browsers the chosen scenarios launch: the webkit-* scenarios and
# device-only-private WebKit, the others Chromium. Checked before the build, so a worktree without
# `npm ci` fails naming the install, not with a Node stack trace afterwards.
BROWSERS=""
case ",$SCENARIOS," in *,webkit-*|*,device-only-private,*) BROWSERS="webkit" ;; esac
case ",$SCENARIOS," in *,chromium-*|*,server-placement,*) BROWSERS="chromium $BROWSERS" ;; esac
# shellcheck disable=SC2086
require_playwright "$ROOT" $BROWSERS

source "$ROOT/scripts/lib/gate-build-cache.sh"
_gc_compiler_hash "$SKY" >/dev/null
TMP="$(gate_e2e_dir "$ROOT" ai-device-e2e)"
rm -rf "$TMP"
mkdir -p "$TMP/app"
cp -Rf "$ROOT/rust/crates/sky/tests/fixtures/ai-device/." "$TMP/app/"

if [ "$MODEL" = tiny ]; then
  echo "==> the synthetic Qwen2 model and adapter"
  OUT="$(cd "$ROOT/runtime-go" && with_timeout 600 go run ./cmd/sky-tiny-gguf "$TMP/models")"
  M="$(printf '%s' "$OUT" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const j=JSON.parse(s);console.log("file:"+j.model+"#sha256="+j.modelSha256)})')"
  A="$(printf '%s' "$OUT" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const j=JSON.parse(s);console.log("file:"+j.adapter+"#sha256="+j.adapterSha256)})')"
  export SKY_AI_PROBE_MODEL="$M"
  export SKY_AI_PROBE_DEVICE_ADAPTER="$A"
fi

echo "==> building the probe (--target web:app --ai-web-engine both)"
(cd "$TMP/app" && with_timeout 1200 "$SKY" build --target web:app --ai-web-engine both src/Main.sky) >"$TMP/build.log" 2>&1 \
  || { cat "$TMP/build.log" >&2; echo "ai-device-e2e: the build failed" >&2; exit 1; }
BACKEND="$TMP/app/.skyapp/web-app/.split/backend/sky-out/app"
[ -x "$BACKEND" ] || { echo "ai-device-e2e: backend not built at $BACKEND" >&2; exit 1; }
ls "$TMP/app/.skyapp/web-app/.split/frontend/dist/" | grep -q '^sky-ai-cpu\.' \
  || { echo "ai-device-e2e: the browser engines were not staged; run \`sky ai provision --device web\`" >&2; exit 1; }

echo "==> driving the probe in the browsers (${SCENARIOS})"
with_timeout 3600 node "$ROOT/scripts/ai-device-verify.mjs" "$BACKEND" --port "${AI_DEVICE_E2E_PORT:-9391}" --scenarios "$SCENARIOS"

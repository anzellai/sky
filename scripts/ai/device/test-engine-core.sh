#!/usr/bin/env bash
#
# scripts/ai/device/test-engine-core.sh - build the engine core (sky_llama.cpp)
# for the build machine's CPU and run its contract test (test_sky_llama.cpp):
# the streamed answer is the one-call answer, piece by piece, in whole UTF-8.
#
#   test-engine-core.sh --llama-src <llama.cpp-b11200 dir> [--model <gguf>] [--work <dir>]
#
# The model defaults to the synthetic Qwen2 model (`go run ./cmd/sky-tiny-gguf`).
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../../.." && pwd)"
source "$ROOT/scripts/lib/with-timeout.sh"
source "$ROOT/scripts/lib/require-tool.sh"
require_tool cmake "install CMake 3.20+"
require_tool ninja "install Ninja"
LLAMA_SRC="" MODEL="" WORK=""
while [ $# -gt 0 ]; do
  case "$1" in
    --llama-src) LLAMA_SRC="$2"; shift 2 ;;
    --model) MODEL="$2"; shift 2 ;;
    --work) WORK="$2"; shift 2 ;;
    *) echo "usage: test-engine-core.sh --llama-src <dir> [--model <gguf>] [--work <dir>]" >&2; exit 2 ;;
  esac
done
[ -f "$LLAMA_SRC/include/llama.h" ] || { echo "test-engine-core: --llama-src must be the llama.cpp b11200 source" >&2; exit 2; }
WORK="${WORK:-$(mktemp -d "${TMPDIR:-/tmp}/sky-engine-core.XXXXXX")}"
mkdir -p "$WORK"
if [ -z "$MODEL" ]; then
  require_tool go "install Go 1.25+"
  MODEL="$(cd "$ROOT/runtime-go" && go run ./cmd/sky-tiny-gguf "$WORK/model" | sed -n 's/.*"model":"\([^"]*\)".*/\1/p')"
fi
with_timeout 1800 cmake -S "$HERE" -B "$WORK/build" -G Ninja -DCMAKE_BUILD_TYPE=Release \
  -DSKY_TARGET=host -DLLAMA_SRC="$LLAMA_SRC" -DGGML_METAL=OFF -DGGML_BLAS=OFF > "$WORK/configure.log" 2>&1 \
  || { tail -40 "$WORK/configure.log" >&2; exit 1; }
with_timeout 3600 cmake --build "$WORK/build" --target sky_llama_test -j 4 > "$WORK/build.log" 2>&1 \
  || { tail -40 "$WORK/build.log" >&2; exit 1; }
with_timeout 600 "$WORK/build/sky_llama_test" "$MODEL"

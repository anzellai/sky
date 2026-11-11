#!/usr/bin/env bash
# Generated typed Codec.auto codecs vs the reflective walker, native and Wasm
# (Node). Usage: run.sh <out-dir> [targets, default "native wasm"]. Needs a fresh sky-out/sky (scripts/build.sh),
# go and node. One process per mode, shape and size.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../../../.." && pwd)"
OUT="${1:?out dir}"
TARGETS="${2:-native wasm}"
source "$ROOT/scripts/lib/with-timeout.sh"
source "$ROOT/scripts/lib/fresh-compiler.sh"
require_fresh_compiler "$ROOT/sky-out/sky" "$ROOT"
mkdir -p "$OUT"
WORK="$(mktemp -d)"
trap '/bin/rm -rf "$WORK"' EXIT
cp -R "$HERE/app/." "$WORK/"
(cd "$WORK" && with_timeout 600 "$ROOT/sky-out/sky" build src/Main.sky >/dev/null)
cp "$HERE/bench_test.go" "$WORK/sky-out/"
(cd "$WORK/sky-out" && with_timeout 600 go test -c -o "$WORK/bench.native" . \
  && GOOS=js GOARCH=wasm with_timeout 600 go test -c -o "$WORK/bench.wasm" .)
EXEC_NODE="$(go env GOROOT)/lib/wasm/wasm_exec_node.js"
for target in $TARGETS; do
  for mode in generated reference; do
    for shape in list dict blob; do
      for mb in 1 3; do
        args=(-test.run '^$' -test.bench AutoCodec -test.benchtime 2s -test.count 3 -test.benchmem)
        if [ "$target" = native ]; then
          AUTO_CODEC_MODE=$mode AUTO_CODEC_SHAPE=$shape AUTO_CODEC_MB=$mb \
            with_timeout 900 "$WORK/bench.native" "${args[@]}" >"$OUT/$target-$mode-$shape-$mb.txt"
        else
          # Go's wasm_exec copies the environment into Wasm memory and has a
          # size limit, so Node gets a minimal one.
          with_timeout 1800 env -i PATH="$PATH" HOME="$HOME" \
            AUTO_CODEC_MODE=$mode AUTO_CODEC_SHAPE=$shape AUTO_CODEC_MB=$mb node "$EXEC_NODE" "$WORK/bench.wasm" "${args[@]}" >"$OUT/$target-$mode-$shape-$mb.txt"
        fi
      done
    done
  done
done

#!/usr/bin/env bash
# build.sh — forumbench app + skyliveload, cross-compiled for linux/amd64.
#
# The Sky compiler emits Go; we let it emit on the M1 and then rebuild the
# emitted package for linux/amd64. The emitted Go is architecture-independent
# (no cgo in this app), so the ONLY thing that differs from a native build on
# the box is the Go toolchain's target, which is the point.
set -euo pipefail
BASE=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/x86bench
WT=/Users/anzel/works/playground/sky-bench-x86
SKY="$WT/rust/target/release/sky"
APP="$BASE/forumbench"
PATCH=/Users/anzel/works/playground/sky/docs/perf/runs/forum-rebaseline-20260816/harness/forumbench-Main.sky.patch

source "$WT/scripts/lib/with-timeout.sh"

rm -rf "$APP"
cp -Rf "$WT/examples/19-skyforum" "$APP"
rm -rf "$APP/sky-out" "$APP/.skycache" "$APP/.skydeps"

# Apply the ONE-file view-size lever, identical to the M1 re-baseline, so the
# x86 numbers are comparable to the arm64 ones rather than merely adjacent.
( cd "$APP" && patch -p0 --batch --forward src/Main.sky < "$PATCH" >/dev/null ) || {
  # the patch's paths are absolute to another scratchpad; strip and retry
  ( cd "$APP" && patch -l -p1000 --batch --forward src/Main.sky < "$PATCH" )
}
echo "== patch applied =="
grep -c benchPosts "$APP/src/Main.sky"

cd "$APP"
with_timeout 900 "$SKY" build src/Main.sky
echo "== sky build ok =="
ls -la "$APP/sky-out/"

# Cross-compile the emitted Go for the targets.
GODIR="$APP/sky-out"
cd "$GODIR"
[ -f go.mod ] || { echo "no go.mod in $GODIR — layout changed" >&2; ls -la; exit 1; }
with_timeout 900 env GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$BASE/bin/forumbench-linux-amd64" .
echo "== app cross-compiled =="

# The generator, for the generator box.
cd "$WT/tools/skyliveload"
with_timeout 600 env GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$BASE/bin/skyliveload-linux-amd64" .
echo "== generator cross-compiled =="

file "$BASE/bin/forumbench-linux-amd64" "$BASE/bin/skyliveload-linux-amd64"
ls -la "$BASE/bin/"

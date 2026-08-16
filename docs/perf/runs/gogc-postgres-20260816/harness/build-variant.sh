#!/usr/bin/env bash
# build-variant.sh <sky-compiler> <name>
#
# Emits forumbench with the given Sky compiler and builds TWO binaries from the
# SAME emitted package: a plain one for throughput and an instrumented one
# (mutex/block profiles behind SKY_PROBE_ADDR) for lock attribution.
#
# The runtime is embedded into the compiler, so the ONLY way to change
# runtime-go/rt in a measured app is to rebuild the compiler and re-emit. That
# is why the control compiler was copied aside before any rt edit and verified
# by symbol grep rather than by timestamp.
set -euo pipefail
BASE=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/gogc
WT=/Users/anzel/works/playground/sky-perf-gogc
SKY="$1"; NAME="$2"
APP="$BASE/src-$NAME"
PATCH="$WT/docs/perf/runs/forum-rebaseline-20260816/harness/forumbench-Main.sky.patch"
PROBE="$WT/docs/perf/runs/gomaxprocs-scaling-20260816/harness/zz_gmpprobe.go"

source "$WT/scripts/lib/with-timeout.sh"
mkdir -p "$BASE/bin"

rm -rf "$APP"
cp -Rf "$WT/examples/19-skyforum" "$APP"
rm -rf "$APP/sky-out" "$APP/.skycache" "$APP/.skydeps"
( cd "$APP" && patch -p0 --batch --forward src/Main.sky < "$PATCH" >/dev/null ) || \
( cd "$APP" && patch -l -p1000 --batch --forward src/Main.sky < "$PATCH" )

cd "$APP"
with_timeout 900 "$SKY" build src/Main.sky >/dev/null
cd "$APP/sky-out"
with_timeout 900 env CGO_ENABLED=0 go build -o "$BASE/bin/forumbench-$NAME" .
cp -f "$PROBE" ./zz_gmpprobe.go
with_timeout 900 env CGO_ENABLED=0 go build -o "$BASE/bin/forumbench-$NAME-prof" .

echo "plain $NAME: $(shasum -a 256 "$BASE/bin/forumbench-$NAME" | cut -c1-16)"
echo "prof  $NAME: $(shasum -a 256 "$BASE/bin/forumbench-$NAME-prof" | cut -c1-16)"
# The claim under test is that the runtime CHANGED, so prove it in the artefact
# rather than trusting the build. An aliased cp and a stale binary have each
# produced a false verdict on this host today.
for s in memCacheAlreadyHolds goidShardedMap sessionLockerShards; do
  echo "  $s in $NAME: $(strings "$BASE/bin/forumbench-$NAME" | grep -c "$s")"
done

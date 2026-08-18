#!/usr/bin/env bash
# buildarm.sh — build one measurement arm: `sky build` with a named compiler,
# then drop zz_perfprobe.go into the generated sky-out/rt/ and `go build` a
# SEPARATE `app-probe` binary. The shipped `app` stays probe-free so the
# "profiler off" control remains meaningful.
#
# A plain `cp app app-probe` does NOT work and silently produces a binary with
# no pprof listener: the probe is a Go source file compiled into app-probe, not
# a runtime flag. That mistake costs one whole run whose allocs-*.pprof are
# simply absent, which is how it was found.
set -euo pipefail
S="${S:-/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/stage4}"
WT="${WT:-/Users/anzel/works/playground/sky-stage4}"
PROBE="${PROBE:-/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/forumperf/wt/docs/perf/runs/attribution-20260815/harness/zz_perfprobe.go.txt}"

SKY="${SKY:?set SKY=<path to sky binary>}"
ARM="${ARM:?set ARM=<arm dir name>}"

source "$WT/scripts/lib/with-timeout.sh"

rm -rf "$S/$ARM"
mkdir -p "$S/$ARM"
cp -Rf "$S/forumbench/." "$S/$ARM/"
rm -rf "$S/$ARM/sky-out" "$S/$ARM/.skycache" "$S/$ARM/.skydeps"

cd "$S/$ARM"
with_timeout 900 "$SKY" build src/Main.sky

cp -f "$PROBE" "$S/$ARM/sky-out/rt/zz_perfprobe.go"
cd "$S/$ARM/sky-out"
with_timeout 900 go build -o app-probe .

echo "ARM $ARM built"
echo "  sky      $SKY"
echo "  main.go  $(md5sum main.go | cut -d' ' -f1)"
echo "  app      $(md5sum app | cut -d' ' -f1)"
echo "  probe    $(md5sum app-probe | cut -d' ' -f1)"

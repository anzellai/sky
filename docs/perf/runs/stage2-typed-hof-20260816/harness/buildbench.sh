#!/usr/bin/env bash
# buildbench.sh — build forumbench with a NAMED compiler binary and produce the
# pprof-instrumented `app-probe` the measurement harness runs.
#
# Both arms of a Stage 2 A/B are the same app source built by two `sky`
# binaries. Nothing else may differ, so the app tree is wiped between arms:
# a stale `sky-out/main.go` would be re-linked into `app-probe` and the run
# would measure the other arm's codegen under this arm's label.
set -euo pipefail

BASE="${BASE:-/Users/anzel/works/playground/sky-stage2-perf}"
WT="${WT:-/Users/anzel/works/playground/sky-wt-stage2}"
SKY="${SKY:?set SKY to the compiler binary}"
APPDIR="${APPDIR:-$BASE/forumbench}"
PROBE="$WT/docs/perf/runs/attribution-20260815/harness/zz_perfprobe.go.txt"

source "$WT/scripts/lib/with-timeout.sh"

[ -x "$SKY" ] || { echo "no compiler at $SKY" >&2; exit 1; }
[ -f "$PROBE" ] || { echo "no perf probe at $PROBE" >&2; exit 1; }

cd "$APPDIR"
rm -rf sky-out .skycache .skydeps
with_timeout 900 "$SKY" build src/Main.sky
cp -f "$PROBE" sky-out/rt/zz_perfprobe.go
( cd sky-out && with_timeout 900 env CGO_ENABLED=0 go build -o app-probe . )

# The binary must carry THIS arm's codegen. `sky` version alone does not prove
# it: a wipe that silently failed would relink yesterday's main.go.
{
  echo "sky_binary   $SKY"
  echo "sky_version  $("$SKY" --version 2>&1 | head -1)"
  echo "main_go_sha  $(shasum -a 256 sky-out/main.go | cut -d' ' -f1)"
  echo "app_sha      $(shasum -a 256 sky-out/app-probe | cut -d' ' -f1)"
  echo "built_at     $(date -u +%Y-%m-%dT%H:%M:%SZ)"
} | tee sky-out/BUILDINFO.txt

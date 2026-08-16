#!/usr/bin/env bash
# build-app.sh — forumbench, native arm64, from THIS worktree.
#
# Byte-comparable in method to docs/perf/runs/gcp-x86-capacity-20260816's
# build.sh: examples/19-skyforum + the one-file `init`-only view-size lever,
# so the view is 94 sky-id elements and the numbers sit on the same corpus.
set -euo pipefail
BASE=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/gogc
WT=/Users/anzel/works/playground/sky-perf-gogc
SKY="$WT/rust/target/release/sky"
APP="$BASE/forumbench"
PATCH="$WT/docs/perf/runs/forum-rebaseline-20260816/harness/forumbench-Main.sky.patch"
OUTBIN="${1:-$BASE/bin/forumbench}"

source "$WT/scripts/lib/with-timeout.sh"
mkdir -p "$BASE/bin"

rm -rf "$APP"
cp -Rf "$WT/examples/19-skyforum" "$APP"
rm -rf "$APP/sky-out" "$APP/.skycache" "$APP/.skydeps"

( cd "$APP" && patch -p0 --batch --forward src/Main.sky < "$PATCH" >/dev/null ) || \
( cd "$APP" && patch -l -p1000 --batch --forward src/Main.sky < "$PATCH" )
echo "== patch applied, benchPosts occurrences: $(grep -c benchPosts "$APP/src/Main.sky") =="

cd "$APP"
with_timeout 900 "$SKY" build src/Main.sky
echo "== sky build ok =="

cd "$APP/sky-out"
[ -f go.mod ] || { echo "no go.mod in sky-out — layout changed" >&2; exit 1; }
with_timeout 900 env CGO_ENABLED=0 go build -o "$OUTBIN" .
echo "== app built: $OUTBIN =="
shasum -a 256 "$OUTBIN"

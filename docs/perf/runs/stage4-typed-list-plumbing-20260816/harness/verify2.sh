#!/usr/bin/env bash
# verify2.sh — the legs verify.sh could not run in a fresh worktree, plus the
# FULL golden corpus.
#
# Three things the first sweep got wrong, all environment rather than code:
#   * `example-sweep.sh` needs `sky-out/sky` (scripts/build.sh) and said so.
#   * `xtask build-run --golden` selects a SUBSET without `--all`: it reported
#     PASS having matched 8 goldens while 16 committed goldens "had no emitting
#     example this run". A third of the corpus, passing.
#   * the Playwright e2e needs node_modules, absent in a fresh worktree.
set -uo pipefail
WT="${WT:-/Users/anzel/works/playground/sky-stage4}"
LOGS="${LOGS:-/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/stage4/verify2}"
mkdir -p "$LOGS"
source "$WT/scripts/lib/with-timeout.sh"
SUMMARY="$LOGS/SUMMARY.txt"; : >| "$SUMMARY"
leg() {
  local name="$1" secs="$2"; shift 2
  local t0 t1 rc; t0=$(date +%s)
  ( cd "$WT" && with_timeout "$secs" "$@" ) >| "$LOGS/$name.log" 2>&1; rc=$?
  t1=$(date +%s)
  if [ "$rc" -eq 0 ]; then printf "PASS  %-26s %5ss\n" "$name" "$((t1-t0))" | tee -a "$SUMMARY"
  else printf "FAIL  %-26s %5ss  (exit %s)\n" "$name" "$((t1-t0))" "$rc" | tee -a "$SUMMARY"; fi
}
leg xtask-build-run-all 3600 cargo run --release -p xtask --manifest-path rust/Cargo.toml -- build-run --golden --all
leg example-sweep       3600 bash scripts/example-sweep.sh
leg skyforum-e2e        1200 node scripts/playwright-live-verify.mjs 19-skyforum
echo; echo "===== SUMMARY ====="; cat "$SUMMARY"

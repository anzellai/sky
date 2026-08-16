#!/usr/bin/env bash
# sweep-cpu.sh — the view-size matrix, one GOMAXPROCS setting per invocation.
#
# View sizes are set through FORUM_POSTS on ONE binary (see forumbench's
# `benchPostCount`), so every point in the fit is the same compiled code.
# FORUM_POSTS=5 is the stock `examples/19-skyforum` seed and reproduces its
# 94 sky-id elements / 135 tags exactly.
#
# Sizes are visited in INTERLEAVED repeat order (all sizes at rep 1, then all
# at rep 2, ...) rather than three-in-a-row per size. A machine that warms or
# throttles over a 15-minute sweep otherwise confounds the trend with the
# regression's x-axis, which is the single thing this sweep must not do.
set -euo pipefail
BASE=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/forumperf

G="${G:-1}"
SIZES="${SIZES:-5 23 60 100}"
REPS="${REPS:-3}"
N="${N:-50}"
DUR="${DUR:-45s}"
PROFILE="${PROFILE:-1}"
TAG="${TAG:-g$G}"

for rep in $(seq 1 "$REPS"); do
  for posts in $SIZES; do
    out="$BASE/runs/cpu-$TAG/p$posts-r$rep"
    if [ -f "$out/load.json" ] && [ ! -f "$out/REJECTED" ]; then
      echo "skip (done) $out"; continue
    fi
    rm -rf "$out"
    echo "=== $TAG posts=$posts rep=$rep ==="
    MODE=cpu N="$N" THINK=0 DUR="$DUR" RAMP=3s WARMUP=3s \
      GOMAXPROCS_SET="$G" PROFILE="$PROFILE" PROFSECS=25 PROFDELAY=12 \
      APP_ENV="FORUM_POSTS=$posts" LABEL="forum-p$posts-$TAG-r$rep" \
      OUT="$out" bash "$BASE/harness/forumrun.sh" || echo "RUN FAILED: $out"
    sleep 3
  done
done

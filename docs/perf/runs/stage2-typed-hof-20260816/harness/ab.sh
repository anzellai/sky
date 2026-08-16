#!/usr/bin/env bash
# ab.sh — the Stage 2 A/B, run ALTERNATING.
#
# Both arms are the same app source at the same commit, compiled by two `sky`
# binaries that differ by exactly one thing: the typed list-HOF dispatch in
# `Ctx::list_hof_typed`. Runs alternate before/after within each view size so
# thermal drift and background load land on both arms equally — the obvious
# "all the befores, then all the afters" ordering charges any drift entirely to
# the second arm.
set -euo pipefail
B="${BASE:-/Users/anzel/works/playground/sky-stage2-perf}"
OUTROOT="${OUTROOT:?set OUTROOT}"
SIZES="${SIZES:-5 60}"
REPS="${REPS:-3}"

for posts in $SIZES; do
  for rep in $(seq 1 "$REPS"); do
    for arm in before after; do
      out="$OUTROOT/p$posts-$arm-r$rep"
      [ -d "$out" ] && continue
      echo "=== p$posts $arm r$rep ==="
      APPDIR="$B/arm-$arm" MODE=cpu N=25 THINK=0 DUR=45s REP="$rep" BIN=app-probe \
        APP_ENV="FORUM_POSTS=$posts" LABEL="$arm-p$posts-r$rep" OUT="$out" \
        bash "$B/harness/forumrun.sh"
    done
  done
done

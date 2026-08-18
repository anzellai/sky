#!/usr/bin/env bash
# ab.sh — the Stage 4 A/B, run ALTERNATING and SEQUENTIALLY.
#
# Both arms are the same app source at the same commit, compiled by two `sky`
# binaries differing by exactly one thing: whether a `++` whose two operands
# carry the same proven Go slice type lowers to `rt.List_appendT[T]` or to
# `rt.AsListT[T](rt.Concat(any(a), any(b)))`.
#
# Runs alternate before/after within each view size so thermal drift and
# background load land on both arms equally — "all the befores, then all the
# afters" charges any drift entirely to the second arm. Nothing runs in
# parallel: this host hard-locked once already and a co-scheduled build would
# also contaminate the very throughput number being read.
set -euo pipefail
S="${S:-/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/stage4}"
OUTROOT="${OUTROOT:-$S/runs}"
SIZES="${SIZES:-5 60}"
REPS="${REPS:-3}"

for posts in $SIZES; do
  for rep in $(seq 1 "$REPS"); do
    for arm in before after; do
      out="$OUTROOT/p$posts-$arm-r$rep"
      [ -d "$out" ] && { echo "skip $out (exists)"; continue; }
      echo "=== p$posts $arm r$rep ==="
      APPDIR="$S/arm-$arm" MODE=cpu N=25 THINK=0 DUR=45s REP="$rep" BIN=app-probe \
        PORT=8541 PPROF_PORT=6587 \
        APP_ENV="$(printf 'FORUM_POSTS=%s\n' "$posts")" \
        LABEL="$arm-p$posts-r$rep" OUT="$out" \
        bash "$S/harness/forumrun.sh"
    done
  done
done
echo "AB COMPLETE"

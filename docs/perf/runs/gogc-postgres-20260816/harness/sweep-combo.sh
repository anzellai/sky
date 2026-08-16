#!/usr/bin/env bash
# sweep-combo.sh — the configuration actually being recommended: a MULTIPLIER
# for normal pacing plus a BOUND for the worst case. Neither arm above tests
# it: GOGC-only is unbounded, and GOGC=off+limit spends the whole budget even
# at low session counts.
set -u
BASE=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/gogc
for a in "500:400:750MiB" "100:400:750MiB" "500:200:750MiB"; do
  IFS=: read -r N G L <<< "$a"
  TAG="combo-n${N}-gogc${G}-${L}"
  echo "=== [$(date +%H:%M:%S)] $TAG ==="
  "$BASE/runone.sh" "$TAG" "$N" "$G" "$L" || echo "ARM FAILED: $TAG"
  sleep 6
done
echo "=== COMBO COMPLETE $(date +%H:%M:%S) ==="

#!/usr/bin/env bash
# sweep.sh — the GOGC x sessions grid on the PostgreSQL-backed store.
#
# Two blocks, block B the exact reverse of block A, so no cell sits at the same
# sequence position twice and the (gogc=100, n=100) control cell is measured at
# position 1 and position 24 — a drift check that costs no extra arms.
set -u
BASE=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/gogc
CELLS=()
for N in 100 300 500; do for G in 100 200 400 800; do CELLS+=("$N:$G"); done; done

run_block() {
  local blk="$1"; shift
  local -a order=("$@")
  for c in "${order[@]}"; do
    N="${c%%:*}"; G="${c##*:}"
    TAG="n${N}-gogc${G}-b${blk}"
    echo "=== [$(date +%H:%M:%S)] $TAG ==="
    "$BASE/runone.sh" "$TAG" "$N" "$G" - || echo "ARM FAILED: $TAG"
    sleep 6
  done
}

run_block 1 "${CELLS[@]}"
REV=(); for ((i=${#CELLS[@]}-1; i>=0; i--)); do REV+=("${CELLS[$i]}"); done
run_block 2 "${REV[@]}"
echo "=== SWEEP COMPLETE $(date +%H:%M:%S) ==="

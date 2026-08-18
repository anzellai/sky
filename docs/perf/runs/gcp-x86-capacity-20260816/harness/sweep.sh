#!/usr/bin/env bash
# sweep.sh — the capacity matrix for one target.
#
# usage: sweep.sh <small|medium>
#
# THREE BLOCKS, BACK TO BACK, NO IDLE GAPS. e2 instances are burstable and the
# effect is large enough to swamp a configuration difference: the counterbalance
# in ../gcp-embed-postgres-20260815 shows whichever arm runs first getting ~40/s
# and whichever runs third ~21/s, on identical configurations. So:
#
#   * the blocks run continuously, which SPENDS the credits rather than
#     waiting them out, and
#   * the config order alternates between blocks (mem-first, pg-first,
#     mem-first), so config is not confounded with position within a block.
#
# Block 1 is therefore the rested-instance number and blocks 2-3 are the
# sustained one. Reporting the first is what overstates an e2 instance.
set -u
BASE=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/x86bench
TGT="$1"
export HOLD=90 RAMP=25 WARM=10 THINK=0

for BLOCK in 1 2 3; do
  case "$BLOCK" in
    1|3) ORDER="mem pg" ;;
    2)   ORDER="pg mem" ;;
  esac
  for CFG in $ORDER; do
    for N in 100 300 500; do
      bash "$BASE/harness/runone.sh" "$TGT" "$CFG" "$N" 1 "$BLOCK" 5 \
        >> "$BASE/out/sweep-$TGT.log" 2>&1
    done
  done
  echo "$(date -u +%H:%M:%S) $TGT block $BLOCK done" >> "$BASE/out/progress.log"
done
echo "$(date -u +%H:%M:%S) $TGT SWEEP COMPLETE" >> "$BASE/out/progress.log"

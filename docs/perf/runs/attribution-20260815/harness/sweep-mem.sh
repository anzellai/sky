#!/usr/bin/env bash
set -euo pipefail
BASE=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/perfbench
for spec in "sky 100 1" "sky 100 2" "sky 100 3" "sky 25 1" "sky 50 1" "control 100 1"; do
  set -- $spec
  t=$1; n=$2; r=$3
  o="$BASE/runs/mem/$t-n$n-r$r"
  rm -rf "$o"
  echo "### $t n=$n rep=$r"
  TARGET="$t" N="$n" REP="$r" OUT="$o" bash "$BASE/memrun.sh" 2>&1 | sed 's/^/    /' || echo "    FAILED"
done

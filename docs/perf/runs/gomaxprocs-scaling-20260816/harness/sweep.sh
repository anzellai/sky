#!/usr/bin/env bash
# sweep.sh — the scaling sweep: 4 GOMAXPROCS levels x 3 blocks, ONE box.
#
# The arm order is permuted between blocks so that a level is never measured
# at the same position in the sequence twice. Position matters even on
# dedicated vCPUs: page cache, the Go heap's arena growth and the host's own
# thermal/neighbour state all drift over a sweep, and a fixed 1,2,4,8 order
# would charge every one of those drifts to GOMAXPROCS.
set -u
S=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/gmp
N="${N:-100}"
run() { bash "$S/harness/runone.sh" "$1" "$N" "$2" plain "${3:-}"; }
for a in 1 2 4 8; do run "$a" 1; done
for a in 8 4 2 1; do run "$a" 2; done
for a in 2 8 1 4; do run "$a" 3; done
echo "=== SWEEP DONE ==="

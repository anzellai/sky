#!/usr/bin/env bash
# diag.sh — the "if it does not scale, why" half.
#
#  1. GOGC sweep at the top level. If GC is the binder, relaxing the pacer
#     raises throughput; if it is not, the curve is flat in GOGC.
#  2. Session-count sensitivity at the top level and at 1 core. If the sweep
#     were serialising on one session's mutex, or simply not offering enough
#     concurrency to fill 8 Ps, throughput would move with session count.
#  3. gctrace at 1 and 8, for the GC CPU fraction the Go runtime reports itself.
set -u
S=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/gmp
R() { bash "$S/harness/runone.sh" "$@"; }

echo "########## GOGC sweep at GOMAXPROCS=8, n=100 ##########"
for g in 100 400 800; do GOGC_SET=$g R 8 100 "gogc$g" plain "-gogc$g"; done

echo "########## session-count sensitivity ##########"
for n in 25 400; do R 8 "$n" 9 plain "-sens"; done
R 1 400 9 plain "-sens"

echo "########## gctrace arms ##########"
GCTRACE=1 R 1 100 7 plain "-gct"
GCTRACE=1 R 8 100 7 plain "-gct"
echo "=== DIAG DONE ==="

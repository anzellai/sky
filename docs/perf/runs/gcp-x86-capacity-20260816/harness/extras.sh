#!/usr/bin/env bash
# extras.sh — the two questions the capacity matrix cannot answer on its own.
#
# A. IS THE WAL FSYNC THE BINDER?  The session store writes the whole model gob
#    synchronously on every interaction and commits, so on a durable cluster
#    that is one WAL fsync per interaction, unconditionally. The M1 corpus
#    cannot answer this: its bench cluster ran `fsync = off` and
#    `synchronous_commit = off` (harness/pg-up.sh), so the M1 postgres numbers
#    have no fsync in them at all. Here the embedded cluster's own config
#    generator deliberately leaves durability alone (pg_embed_conf.go:13 --
#    "Nothing here changes what a query MEANS -- not fsync, not
#    synchronous_commit, not wal_level"), so the `pg` arm carries a real fsync
#    and `pgnofsync` is the same cluster with it turned off at runtime. The
#    difference between them IS the fsync cost. Arms alternate.
#
# B. WHAT DOES A SUSTAINED e2-small ACTUALLY SERVE?  Eight consecutive runs at
#    the mandated concurrency with no idle gap, so burst credits drain during
#    the series. The trend across run index is the answer; the first run is
#    not.
set -u
BASE=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/x86bench
WHICH="${1:?A or B}"
export HOLD=90 RAMP=25 WARM=10 THINK=0

if [ "$WHICH" = "A" ]; then
  # e2-medium: its guest tick accounting is sound (measured 199-200 Hz against
  # the 200 two vCPUs owe), so a CPU-vs-IO attribution is readable there. On
  # e2-small the same counter ran at 86-143 Hz under load and cannot carry it.
  for R in 1 2 3; do
    case "$R" in
      1|3) ORDER="pg pgnofsync" ;;
      2)   ORDER="pgnofsync pg" ;;
    esac
    for CFG in $ORDER; do
      bash "$BASE/harness/runone.sh" medium "$CFG" 100 "$R" fs 5 \
        >> "$BASE/out/extras-A.log" 2>&1
    done
    echo "$(date -u +%H:%M:%S) fsync block $R done" >> "$BASE/out/progress.log"
  done
  echo "$(date -u +%H:%M:%S) EXTRAS-A COMPLETE" >> "$BASE/out/progress.log"
fi

if [ "$WHICH" = "B" ]; then
  for R in 1 2 3 4 5 6 7 8; do
    bash "$BASE/harness/runone.sh" small pg 300 "$R" soak 5 \
      >> "$BASE/out/extras-B.log" 2>&1
  done
  echo "$(date -u +%H:%M:%S) EXTRAS-B COMPLETE" >> "$BASE/out/progress.log"
fi

#!/usr/bin/env bash
# sweep-mem.sh — RSS UNDER SUSTAINED LOAD, on the postgres session store.
#
# WHY NOT REUSE memrun.sh's NUMBER
#
# The archived 336 kB/session is `(P1.heap_alloc - P0.heap_alloc) / N` with
# the sessions IDLE (`-think 1h`) after two forced `runtime.GC()` passes. That
# is a retention measurement and it is the right way to measure retention. It
# is the wrong input to a capacity table: the same runs show `heap_alloc`
# reaching 169-237 MB at 100 sessions under load against 39.9 MB idle, and RSS
# settling at 358-380 MB and never returning. An operator provisions for the
# loaded figure, so that is what this sweep records:
#
#   rss_kb_under_load_nogc      what the process is actually holding
#   rss_kb_under_load_after_gc  the same instant, one forced GC later
#   rss_kb_after_settle         20 s after the load stops, GC forced
#
# Two idle-evict arms. `SKY_LIVE_IDLE_EVICT` defaults to 5m, which cannot fire
# inside a 90 s window -- so the default arm establishes what the feature does
# NOT do at bench timescales, and a 15 s arm establishes what it does when it
# engages. Reporting only the first would credit an eviction that never ran.
set -euo pipefail
BASE=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/forumperf

DSN="${DSN:?set DSN to the postgres session-store URL}"
SESSIONS="${SESSIONS:-100 300 500}"
REPS="${REPS:-3}"
EVICT="${EVICT:-}"                 # empty = runtime default (5m)
TAG="${TAG:-pg}"
G="${G:-8}"

for rep in $(seq 1 "$REPS"); do
  for n in $SESSIONS; do
    out="$BASE/runs/mem-$TAG/n$n-r$rep"
    if [ -f "$out/snapshot.txt" ] && [ ! -f "$out/REJECTED" ]; then
      echo "skip (done) $out"; continue
    fi
    rm -rf "$out"
    echo "=== $TAG n=$n rep=$rep ==="
    appenv=$(printf 'FORUM_POSTS=5\nSKY_LIVE_STORE=postgres\nSKY_LIVE_STORE_PATH=%s' "$DSN")
    if [ -n "$EVICT" ]; then appenv="$appenv
SKY_LIVE_IDLE_EVICT=$EVICT"; fi
    MODE=mem N="$n" THINK=1s DUR=90s RAMP=20s WARMUP=5s \
      GOMAXPROCS_SET="$G" PROFILE=1 MEMRATE=0 \
      APP_ENV="$appenv" LABEL="mem-$TAG-n$n-r$rep" \
      OUT="$out" bash "$BASE/harness/forumrun.sh" || echo "RUN FAILED: $out"
    sleep 5
  done
done

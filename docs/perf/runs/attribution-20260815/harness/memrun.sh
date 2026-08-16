#!/usr/bin/env bash
# memrun.sh — separate genuine per-session RETENTION from GC headroom.
#
# The published 1,379-1,450 kB/session came from regressing RSS against
# session count while interactions were in flight. RSS is a high-water mark
# and Go returns memory to the OS lazily, so that figure cannot distinguish:
#   (a) retention   — Model, prevTree, buffers: a real per-session ceiling
#   (b) GC headroom — transient garbage from interactions, never returned
# and in that sweep sessions and interaction rate rose together, so the two
# are perfectly confounded.
#
# Three phases, in one process, so nothing is confounded across runs:
#
#   P0  baseline    0 sessions, after forced GC
#   P1  idle hold   N sessions established, SSE open, essentially no
#                   interactions (think = 1h), then forced GC.
#                   THE DECISIVE PHASE: what remains is retention.
#   P2  under load  same N sessions driven hard, sampled at peak, then load
#                   stopped, settled, forced GC and sampled again.
#                   P2peak - P1 is the transient term; P2after - P1 that
#                   does NOT return is a leak.
#
# Every phase records RSS *and* HeapInuse *and* HeapAlloc *and* StackInuse,
# because the RSS-vs-HeapInuse divergence IS the headroom term.
set -euo pipefail
BASE=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/perfbench
WT="$BASE/wt"
APPDIR="$WT/examples/26-ui-showcase"
GEN="$BASE/bin/skyliveload"

TARGET="${TARGET:-sky}"
N="${N:-100}"
REP="${REP:-1}"
GOMAXPROCS_SET="${GOMAXPROCS_SET:-8}"
MEMRATE="${MEMRATE:-16384}"
PORT="${PORT:-8551}"
PPROF_PORT="${PPROF_PORT:-6591}"
OUT="${OUT:?set OUT}"
IDLE_HOLD="${IDLE_HOLD:-240s}"   # generator duration for the idle phase
LOAD_HOLD="${LOAD_HOLD:-45s}"

mkdir -p "$OUT"
for p in "$PORT" "$PPROF_PORT"; do
  lsof -nP -iTCP:"$p" -sTCP:LISTEN >/dev/null 2>&1 && { echo "port $p busy" >&2; exit 69; }
done

{
  echo "timestamp      $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "commit         $(cd "$WT" && git rev-parse HEAD)"
  echo "host           $(hostname)"
  echo "os             $(uname -srm)"
  echo "cores          $(sysctl -n hw.ncpu)"
  echo "go             $(go version)"
  echo "load1          $(uptime | sed -E 's/.*load averages?: ([0-9.]+).*/\1/')"
  echo "target         $TARGET"
  echo "sessions       $N"
  echo "gomaxprocs     $GOMAXPROCS_SET"
  echo "memprofilerate $MEMRATE"
  echo "repeat         $REP"
} >| "$OUT/env.txt"

if [ "$TARGET" = "control" ]; then
  cd "$BASE/control"; CMD="$BASE/bin/control"
  ENVARGS=("PORT=$PORT" "GOMAXPROCS=$GOMAXPROCS_SET" "SKY_PERF_PPROF_ADDR=127.0.0.1:$PPROF_PORT" "SKY_PERF_MEMPROFILERATE=$MEMRATE")
else
  cd "$APPDIR"; CMD="./sky-out/app-probe"
  ENVARGS=("SKY_LIVE_PORT=$PORT" "GOMAXPROCS=$GOMAXPROCS_SET" "SKY_PERF_PPROF_ADDR=127.0.0.1:$PPROF_PORT" "SKY_PERF_MEMPROFILERATE=$MEMRATE")
fi

env "${ENVARGS[@]}" "$CMD" >| "$OUT/app.log" 2>&1 &
APP_PID=$!
GEN_PID=""
cleanup() {
  [ -n "$GEN_PID" ] && kill "$GEN_PID" 2>/dev/null || true
  kill "$APP_PID" 2>/dev/null || true
  wait "$APP_PID" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

for _ in $(seq 1 80); do curl -sf "http://127.0.0.1:$PORT/" -o /dev/null 2>/dev/null && break; sleep 0.25; done
curl -sf "http://127.0.0.1:$PORT/" -o /dev/null || { echo "app never came up" >&2; exit 70; }
OWNER=$(lsof -nP -iTCP:"$PORT" -sTCP:LISTEN -t 2>/dev/null | head -1)
[ "$OWNER" = "$APP_PID" ] || { echo "port owned by $OWNER not $APP_PID" >&2; exit 72; }

rss_kb() { ps -o rss= -p "$APP_PID" | tr -d ' '; }

# snapshot <phase>: force GC twice (one pass can leave finalisable objects),
# then record RSS + the full MemStats + a heap profile + goroutines.
snapshot() {
  local phase="$1"
  curl -sf "http://127.0.0.1:$PPROF_PORT/perf/memstats" -o /dev/null
  curl -sf "http://127.0.0.1:$PPROF_PORT/perf/memstats" -o "$OUT/memstats-$phase.json"
  echo "rss_kb $(rss_kb)" >| "$OUT/rss-$phase.txt"
  curl -sf "http://127.0.0.1:$PPROF_PORT/debug/pprof/heap?gc=1" -o "$OUT/heap-$phase.pprof"
  curl -sf "http://127.0.0.1:$PPROF_PORT/debug/pprof/goroutine?debug=1" -o "$OUT/goroutine-$phase.txt"
  echo "  [$phase] rss=$(rss_kb) kB $(awk -F'[,:]' '{for(i=1;i<=NF;i++){if($i ~ /"heap_inuse"/) printf "heapInuse=%.1fMB ", $(i+1)/1048576; if($i ~ /"stack_inuse"/) printf "stackInuse=%.1fMB ", $(i+1)/1048576; if($i ~ /"num_goroutine"/) printf "goroutines=%s", $(i+1)}}' "$OUT/memstats-$phase.json")"
}

echo "== P0 baseline (0 sessions)"
sleep 3
snapshot p0

echo "== P1 idle hold: $N sessions, think=1h (established, then quiescent)"
"$GEN" -url "http://127.0.0.1:$PORT" -sessions "$N" -think 1h -think-jitter 0 \
    -duration "$IDLE_HOLD" -ramp 10s -warmup 1s -max-error-rate 1.0 \
    -json "$OUT/load-idle.json" -label "memidle-n$N" >| "$OUT/load-idle.txt" 2>&1 &
GEN_PID=$!
sleep 45   # ramp 10s + establish + settle; interactions ~0 at think=1h
ESTAB=$(lsof -nP -iTCP:"$PORT" -sTCP:ESTABLISHED 2>/dev/null | wc -l | tr -d ' ')
echo "established_conns_incl_header $ESTAB" >| "$OUT/estab.txt"
snapshot p1

echo "== P2 under load: same process, driving interactions"
kill "$GEN_PID" 2>/dev/null || true; wait "$GEN_PID" 2>/dev/null || true
"$GEN" -url "http://127.0.0.1:$PORT" -sessions "$N" -think 0 \
    -duration "$LOAD_HOLD" -ramp 5s -warmup 2s -max-error-rate 1.0 \
    -json "$OUT/load-drive.json" -label "memload-n$N" >| "$OUT/load-drive.txt" 2>&1 &
GEN_PID=$!
sleep 35
echo "rss_kb_peak_noGC $(rss_kb)" >| "$OUT/rss-p2peak-nogc.txt"
curl -sf "http://127.0.0.1:$PPROF_PORT/perf/memstats?gc=0" -o "$OUT/memstats-p2peak-nogc.json"
snapshot p2peak
wait "$GEN_PID" 2>/dev/null || true
GEN_PID=""

echo "== P2after: load stopped, settled, forced GC"
sleep 20
snapshot p2after

echo "OK $OUT"

#!/usr/bin/env bash
# perfrun.sh — attribute Sky.Live's per-interaction CPU and per-session memory.
#
# Two modes:
#   cpu   start app, drive load, take a CPU pprof over an exact steady-state
#         window (NOT the whole process lifetime), plus an independent
#         ps(1) CPU-time delta as a profiler-free cross-check.
#   mem   start app, hold N sessions open with live SSE, then snapshot
#         heap pprof + full runtime.MemStats + RSS + goroutine profile while
#         they are still resident.
#
# Every run writes env.txt with host/commit/load so no number can travel
# without its conditions.
set -euo pipefail

WT=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/perfbench/wt
BASE=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/perfbench
APPDIR="${APPDIR:-$WT/examples/26-ui-showcase}"
GEN="$BASE/bin/skyliveload"

MODE="${MODE:?set MODE=cpu|mem}"
N="${N:-25}"
THINK="${THINK:-1s}"
DUR="${DUR:-60s}"
RAMP="${RAMP:-5s}"
WARMUP="${WARMUP:-5s}"
REP="${REP:-1}"
GOMAXPROCS_SET="${GOMAXPROCS_SET:-1}"
PROFILE="${PROFILE:-1}"          # 0 = unprofiled control run
PROFSECS="${PROFSECS:-30}"
PROFDELAY="${PROFDELAY:-15}"     # start CPU profile this many seconds in
MEMRATE="${MEMRATE:-0}"          # 0 = Go default 512KiB
BIN="${BIN:-app-probe}"
PORT="${PORT:-8531}"
PPROF_PORT="${PPROF_PORT:-6577}"
OUT="${OUT:?set OUT}"

mkdir -p "$OUT"

# ---- conditions, written before anything runs -----------------------------
{
  echo "timestamp       $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "commit          $(cd "$WT" && git rev-parse HEAD)"
  echo "branch          $(cd "$WT" && git rev-parse --abbrev-ref HEAD)"
  echo "host            $(hostname)"
  echo "os              $(uname -srm)"
  echo "cpu             $(sysctl -n machdep.cpu.brand_string 2>/dev/null || echo unknown)"
  echo "cores           $(sysctl -n hw.ncpu)"
  echo "mem_bytes       $(sysctl -n hw.memsize)"
  echo "go              $(go version)"
  echo "load1_at_start  $(uptime | sed -E 's/.*load averages?: ([0-9.]+).*/\1/')"
  echo "mode            $MODE"
  echo "binary          $BIN"
  echo "sessions        $N"
  echo "think           $THINK"
  echo "duration        $DUR"
  echo "gomaxprocs      $GOMAXPROCS_SET"
  echo "cpu_profiling   $PROFILE"
  echo "memprofilerate  $MEMRATE"
  echo "repeat          $REP"
} >| "$OUT/env.txt"

# ---- refuse to run against someone else's process -------------------------
# A stale app on $PORT answers curl, so the readiness probe passes while THIS
# run's app has already died on bind. Every number then describes the wrong
# process. This cost one pilot run; it is now impossible.
for p in "$PORT" "$PPROF_PORT"; do
  if lsof -nP -iTCP:"$p" -sTCP:LISTEN >/dev/null 2>&1; then
    echo "port $p is already in use — refusing to run (would measure the wrong process)" >&2
    exit 69
  fi
done

# ---- start the target -----------------------------------------------------
# TARGET=sky     the Sky.Live app under test
# TARGET=control the minimal Go SSE server (perfbench/control) — the floor
TARGET="${TARGET:-sky}"
if [ "$TARGET" = "control" ]; then
  cd "$BASE/control"
  CMD="$BASE/bin/control"
  ENVARGS=("PORT=$PORT" "GOMAXPROCS=$GOMAXPROCS_SET")
else
  cd "$APPDIR"
  CMD="./sky-out/$BIN"
  ENVARGS=("SKY_LIVE_PORT=$PORT" "GOMAXPROCS=$GOMAXPROCS_SET")
fi
if [ "$PROFILE" = "1" ]; then
  ENVARGS+=("SKY_PERF_PPROF_ADDR=127.0.0.1:$PPROF_PORT")
  [ "$MEMRATE" != "0" ] && ENVARGS+=("SKY_PERF_MEMPROFILERATE=$MEMRATE")
fi
echo "target          $TARGET" >> "$OUT/env.txt"

env "${ENVARGS[@]}" "$CMD" >| "$OUT/app.log" 2>&1 &
APP_PID=$!
cleanup() {
  kill "$APP_PID" 2>/dev/null || true
  wait "$APP_PID" 2>/dev/null || true
}
trap cleanup EXIT INT TERM
echo "$APP_PID" >| "$OUT/app.pid"

for _ in $(seq 1 80); do
  curl -sf "http://127.0.0.1:$PORT/" -o /dev/null 2>/dev/null && break
  sleep 0.25
done
curl -sf "http://127.0.0.1:$PORT/" -o /dev/null || { echo "app never came up" >&2; exit 70; }
# And prove the thing answering is OUR pid, not a survivor.
kill -0 "$APP_PID" 2>/dev/null || { echo "app pid $APP_PID is gone but the port answers" >&2; exit 71; }
OWNER=$(lsof -nP -iTCP:"$PORT" -sTCP:LISTEN -t 2>/dev/null | head -1)
[ "$OWNER" = "$APP_PID" ] || { echo "port $PORT owned by pid $OWNER, not our $APP_PID" >&2; exit 72; }

rss_kb() { ps -o rss= -p "$APP_PID" | tr -d ' '; }
# ps TIME carries centiseconds, so deltas are fractional -> awk, not $(( )).
fsub() { awk -v a="$1" -v b="$2" 'BEGIN{printf "%.2f", a-b}'; }
cpu_secs() {
  # ps TIME as [dd-]hh:mm:ss -> seconds
  ps -o time= -p "$APP_PID" | tr -d ' ' | awk -F: '
    NF==3 {print $1*3600 + $2*60 + $3}
    NF==2 {print $1*60 + $2}'
}

sleep 2
echo "idle_rss_kb $(rss_kb)" >| "$OUT/idle.txt"
echo "idle_cpu_s  $(cpu_secs)" >> "$OUT/idle.txt"
[ "$PROFILE" = "1" ] && curl -sf "http://127.0.0.1:$PPROF_PORT/perf/memstats" >| "$OUT/memstats-idle.json" || true

# ==========================================================================
if [ "$MODE" = "cpu" ]; then
  CPU0="$(cpu_secs)"; T0=$(date +%s)
  "$GEN" -url "http://127.0.0.1:$PORT" -sessions "$N" -think "$THINK" \
      -duration "$DUR" -ramp "$RAMP" -warmup "$WARMUP" \
      -json "$OUT/load.json" -label "cpu-n$N-rep$REP" >| "$OUT/load.txt" 2>&1 &
  GEN_PID=$!

  if [ "$PROFILE" = "1" ]; then
    sleep "$PROFDELAY"
    echo "prof_window_start_rss_kb $(rss_kb)" >| "$OUT/profwindow.txt"
    PCPU0="$(cpu_secs)"; PT0=$(date +%s)
    curl -sf "http://127.0.0.1:$PPROF_PORT/debug/pprof/profile?seconds=$PROFSECS" \
        -o "$OUT/cpu.pprof" || echo "CPU PROFILE FETCH FAILED" >&2
    PCPU1="$(cpu_secs)"; PT1=$(date +%s)
    {
      echo "prof_seconds_requested $PROFSECS"
      echo "prof_wall_s            $((PT1-PT0))"
      echo "prof_cpu_delta_s       $(fsub "$PCPU1" "$PCPU0")"
      echo "prof_window_end_rss_kb $(rss_kb)"
    } >> "$OUT/profwindow.txt"
  fi

  wait "$GEN_PID" || true
  CPU1="$(cpu_secs)"; T1=$(date +%s)
  {
    echo "run_wall_s        $((T1-T0))"
    echo "app_cpu_delta_s   $(fsub "$CPU1" "$CPU0")"
    echo "loaded_rss_kb     $(rss_kb)"
  } >| "$OUT/cpu-accounting.txt"
  [ "$PROFILE" = "1" ] && curl -sf "http://127.0.0.1:$PPROF_PORT/perf/memstats" >| "$OUT/memstats-loaded.json" || true

# ==========================================================================
elif [ "$MODE" = "mem" ]; then
  # Hold N sessions with SSE open for the whole window, snapshot mid-flight.
  "$GEN" -url "http://127.0.0.1:$PORT" -sessions "$N" -think "$THINK" \
      -duration "$DUR" -ramp "$RAMP" -warmup "$WARMUP" \
      -json "$OUT/load.json" -label "mem-n$N-rep$REP" >| "$OUT/load.txt" 2>&1 &
  GEN_PID=$!

  # wait past ramp so every session is established, then settle
  sleep "$(( ${RAMP%s} + 20 ))"

  ESTAB=$(lsof -nP -iTCP:"$PORT" -sTCP:ESTABLISHED 2>/dev/null | wc -l | tr -d " ")
  {
    echo "established_conns_incl_header $ESTAB"
    echo "rss_kb $(rss_kb)"
  } >| "$OUT/snapshot.txt"

  curl -sf "http://127.0.0.1:$PPROF_PORT/perf/memstats" >| "$OUT/memstats.json"
  curl -sf "http://127.0.0.1:$PPROF_PORT/debug/pprof/heap?gc=1" -o "$OUT/heap.pprof"
  curl -sf "http://127.0.0.1:$PPROF_PORT/debug/pprof/goroutine?debug=1" -o "$OUT/goroutine.txt"
  # RSS again straight after the forced GC of the heap fetch
  echo "rss_kb_after_gc $(rss_kb)" >> "$OUT/snapshot.txt"

  wait "$GEN_PID" || true
else
  echo "unknown MODE=$MODE" >&2; exit 64
fi

echo "load1_at_end $(uptime | sed -E 's/.*load averages?: ([0-9.]+).*/\1/')" >> "$OUT/env.txt"
echo "OK $OUT"

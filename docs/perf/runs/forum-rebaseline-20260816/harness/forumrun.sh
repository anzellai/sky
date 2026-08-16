#!/usr/bin/env bash
# forumrun.sh — the attribution-20260815 perfrun.sh, re-pointed at a real app,
# with the defect that let three invalid runs into the corpus closed.
#
# WHAT CHANGED vs harness/perfrun.sh
#
#  1. THE GENERATOR'S EXIT STATUS IS CHECKED. perfrun.sh:150 reads
#     `wait "$GEN_PID" || true`. skyliveload exits 2 on an invalid run and
#     had already flagged the three forum runs `"valid": false`; `|| true`
#     discarded that, the sweep wrote load.json anyway, and the numbers were
#     quoted as "cost tracks view size". Here a non-zero generator status
#     stamps REJECTED in the output directory and returns non-zero, so an
#     invalid run cannot be mistaken for data by whoever reads the tree next.
#  2. Patch production is a PRECONDITION, not a post-hoc note: a -self-check
#     runs against the started app before the measurement window opens, and
#     it now requires a patch on all four of its interactions.
#  3. The handler is chosen (-hid-context) and the session state it needs is
#     scripted (-setup), because on skyforum the default "first .click" is
#     the site title, whose Msg is a no-op on the page it renders on.
#  4. APP_ENV lets one binary serve every view size (FORUM_POSTS), so the
#     fixed-term regression varies N with the code held identical.
#
# Everything else — the port-collision guard, the pid-ownership assertion,
# env.txt written before anything runs, the ps(1) CPU-time delta as a
# profiler-free cross-check — is perfrun.sh's and is deliberately unchanged.
set -euo pipefail

BASE=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/forumperf
WT="$BASE/wt"
APPDIR="${APPDIR:-$BASE/forumbench}"
GEN="$BASE/bin/skyliveload"
SETUP="${SETUP-$BASE/harness/forum-setup.json}"
HID_SUFFIX="${HID_SUFFIX:-.click}"
HID_CONTEXT="${HID_CONTEXT->▲<}"

source "$WT/scripts/lib/with-timeout.sh"

MODE="${MODE:?set MODE=cpu|mem}"
N="${N:-25}"
THINK="${THINK:-0}"
DUR="${DUR:-20s}"
RAMP="${RAMP:-3s}"
WARMUP="${WARMUP:-3s}"
REP="${REP:-1}"
GOMAXPROCS_SET="${GOMAXPROCS_SET:-1}"
PROFILE="${PROFILE:-1}"
PROFSECS="${PROFSECS:-25}"
PROFDELAY="${PROFDELAY:-12}"
MEMRATE="${MEMRATE:-0}"
BIN="${BIN:-app-probe}"
PORT="${PORT:-8531}"
PPROF_PORT="${PPROF_PORT:-6577}"
APP_ENV="${APP_ENV:-}"          # e.g. "FORUM_POSTS=60"
LABEL="${LABEL:-$MODE-n$N-rep$REP}"
OUT="${OUT:?set OUT}"

mkdir -p "$OUT"

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
  echo "appdir          $APPDIR"
  echo "binary          $BIN"
  echo "app_env         $APP_ENV"
  echo "sessions        $N"
  echo "think           $THINK"
  echo "duration        $DUR"
  echo "gomaxprocs      $GOMAXPROCS_SET"
  echo "cpu_profiling   $PROFILE"
  echo "memprofilerate  $MEMRATE"
  echo "repeat          $REP"
  echo "setup           $SETUP"
  echo "hid_suffix      $HID_SUFFIX"
  echo "hid_context     $HID_CONTEXT"
} >| "$OUT/env.txt"

reject() {
  echo "REJECTED: $*" >| "$OUT/REJECTED"
  echo "REJECTED ($OUT): $*" >&2
  exit 65
}

for p in "$PORT" "$PPROF_PORT"; do
  if lsof -nP -iTCP:"$p" -sTCP:LISTEN >/dev/null 2>&1; then
    echo "port $p is already in use — refusing to run (would measure the wrong process)" >&2
    exit 69
  fi
done

cd "$APPDIR"
CMD="./sky-out/$BIN"
ENVARGS=("SKY_LIVE_PORT=$PORT" "GOMAXPROCS=$GOMAXPROCS_SET")
[ -n "$APP_ENV" ] && ENVARGS+=($APP_ENV)
if [ "$PROFILE" = "1" ]; then
  ENVARGS+=("SKY_PERF_PPROF_ADDR=127.0.0.1:$PPROF_PORT")
  [ "$MEMRATE" != "0" ] && ENVARGS+=("SKY_PERF_MEMPROFILERATE=$MEMRATE")
fi

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
kill -0 "$APP_PID" 2>/dev/null || { echo "app pid $APP_PID is gone but the port answers" >&2; exit 71; }
OWNER=$(lsof -nP -iTCP:"$PORT" -sTCP:LISTEN -t 2>/dev/null | head -1)
[ "$OWNER" = "$APP_PID" ] || { echo "port $PORT owned by pid $OWNER, not our $APP_PID" >&2; exit 72; }

# The view size this binary is actually serving, counted from the HTML it
# actually served, at the moment it served it. The regression's x-axis is
# never taken from an expectation.
curl -s "http://127.0.0.1:$PORT/" >| "$OUT/page.html"
{
  echo "sky_id_elements $(grep -o 'sky-id="' "$OUT/page.html" | wc -l | tr -d ' ')"
  echo "open_tags       $(grep -o '<[a-z]' "$OUT/page.html" | wc -l | tr -d ' ')"
  echo "page_bytes      $(wc -c < "$OUT/page.html" | tr -d ' ')"
} >| "$OUT/viewsize.txt"

# PRECONDITION, before any measurement: this app + this handler + this setup
# produces a patch on EVERY press. A run that discovers otherwise afterwards
# has already spent the window.
if ! with_timeout 120 "$GEN" -url "http://127.0.0.1:$PORT" -self-check \
      -setup "$SETUP" -hid-suffix "$HID_SUFFIX" -hid-context "$HID_CONTEXT" \
      >| "$OUT/selfcheck.txt" 2>&1; then
  cat "$OUT/selfcheck.txt" >&2
  reject "self-check failed: the handler does not patch on every press"
fi

rss_kb() { ps -o rss= -p "$APP_PID" | tr -d ' '; }
fsub() { awk -v a="$1" -v b="$2" 'BEGIN{printf "%.2f", a-b}'; }
cpu_secs() {
  ps -o time= -p "$APP_PID" | tr -d ' ' | awk -F: '
    NF==3 {print $1*3600 + $2*60 + $3}
    NF==2 {print $1*60 + $2}'
}

sleep 2
echo "idle_rss_kb $(rss_kb)" >| "$OUT/idle.txt"
echo "idle_cpu_s  $(cpu_secs)" >> "$OUT/idle.txt"
[ "$PROFILE" = "1" ] && curl -sf "http://127.0.0.1:$PPROF_PORT/perf/memstats" >| "$OUT/memstats-idle.json" || true

GENARGS=(-url "http://127.0.0.1:$PORT" -sessions "$N" -think "$THINK"
         -duration "$DUR" -ramp "$RAMP" -warmup "$WARMUP"
         -setup "$SETUP" -hid-suffix "$HID_SUFFIX" -hid-context "$HID_CONTEXT"
         -json "$OUT/load.json" -label "$LABEL")

# ==========================================================================
if [ "$MODE" = "cpu" ]; then
  CPU0="$(cpu_secs)"; T0=$(date +%s)
  with_timeout 900 "$GEN" "${GENARGS[@]}" >| "$OUT/load.txt" 2>&1 &
  GEN_PID=$!

  if [ "$PROFILE" = "1" ]; then
    sleep "$PROFDELAY"
    echo "prof_window_start_rss_kb $(rss_kb)" >| "$OUT/profwindow.txt"
    # Bracket the CPU window with two CUMULATIVE alloc profiles. pprof's
    # `-base` over the pair attributes objects and bytes to call sites for
    # exactly the window the CPU profile covers, which is what makes the
    # time share and the allocation share of a given call site comparable.
    curl -sf "http://127.0.0.1:$PPROF_PORT/debug/pprof/allocs?gc=0" -o "$OUT/allocs-pre.pprof" || true
    PCPU0="$(cpu_secs)"; PT0=$(date +%s)
    curl -sf "http://127.0.0.1:$PPROF_PORT/debug/pprof/profile?seconds=$PROFSECS" \
        -o "$OUT/cpu.pprof" || echo "CPU PROFILE FETCH FAILED" >&2
    PCPU1="$(cpu_secs)"; PT1=$(date +%s)
    curl -sf "http://127.0.0.1:$PPROF_PORT/debug/pprof/allocs?gc=0" -o "$OUT/allocs-post.pprof" || true
    {
      echo "prof_seconds_requested $PROFSECS"
      echo "prof_wall_s            $((PT1-PT0))"
      echo "prof_cpu_delta_s       $(fsub "$PCPU1" "$PCPU0")"
      echo "prof_window_end_rss_kb $(rss_kb)"
    } >> "$OUT/profwindow.txt"
  fi

  GEN_RC=0; wait "$GEN_PID" || GEN_RC=$?
  CPU1="$(cpu_secs)"; T1=$(date +%s)
  {
    echo "run_wall_s        $((T1-T0))"
    echo "app_cpu_delta_s   $(fsub "$CPU1" "$CPU0")"
    echo "loaded_rss_kb     $(rss_kb)"
    echo "generator_rc      $GEN_RC"
  } >| "$OUT/cpu-accounting.txt"
  [ "$PROFILE" = "1" ] && curl -sf "http://127.0.0.1:$PPROF_PORT/perf/memstats" >| "$OUT/memstats-loaded.json" || true
  if [ "$GEN_RC" -ne 0 ]; then reject "generator exited $GEN_RC (see load.txt / load.json invalid_reason)"; fi

# ==========================================================================
elif [ "$MODE" = "mem" ]; then
  with_timeout 900 "$GEN" "${GENARGS[@]}" >| "$OUT/load.txt" 2>&1 &
  GEN_PID=$!

  sleep "$(( ${RAMP%s} + 20 ))"

  ESTAB=$(lsof -nP -iTCP:"$PORT" -sTCP:ESTABLISHED 2>/dev/null | wc -l | tr -d " ")
  {
    echo "established_conns_incl_header $ESTAB"
    echo "rss_kb $(rss_kb)"
  } >| "$OUT/snapshot.txt"

  # UNDER LOAD, before any forced GC: this is the number a capacity table
  # needs. The archived memory runs sampled a quiesced process after two
  # forced GC passes, which answers a different question (retention) and
  # understates the footprint the operator has to provision for.
  curl -sf "http://127.0.0.1:$PPROF_PORT/perf/memstats?gc=0" >| "$OUT/memstats-load-nogc.json"
  echo "rss_kb_under_load_nogc $(rss_kb)" >> "$OUT/snapshot.txt"
  curl -sf "http://127.0.0.1:$PPROF_PORT/perf/memstats" >| "$OUT/memstats-load-gc.json"
  echo "rss_kb_under_load_after_gc $(rss_kb)" >> "$OUT/snapshot.txt"
  curl -sf "http://127.0.0.1:$PPROF_PORT/debug/pprof/goroutine?debug=1" -o "$OUT/goroutine.txt"

  GEN_RC=0; wait "$GEN_PID" || GEN_RC=$?
  echo "generator_rc $GEN_RC" >> "$OUT/snapshot.txt"

  # And after the load stops: what does NOT come back is the retention.
  sleep 20
  curl -sf "http://127.0.0.1:$PPROF_PORT/perf/memstats" >| "$OUT/memstats-after.json"
  echo "rss_kb_after_settle $(rss_kb)" >> "$OUT/snapshot.txt"
  if [ "$GEN_RC" -ne 0 ]; then reject "generator exited $GEN_RC (see load.txt / load.json invalid_reason)"; fi
else
  echo "unknown MODE=$MODE" >&2; exit 64
fi

echo "load1_at_end $(uptime | sed -E 's/.*load averages?: ([0-9.]+).*/\1/')" >> "$OUT/env.txt"
echo "OK $OUT"

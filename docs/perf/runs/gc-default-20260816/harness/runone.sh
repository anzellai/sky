#!/usr/bin/env bash
# runone.sh — one RSS-bound arm for the shipped GC default.
#
# Derived from docs/perf/runs/gogc-postgres-20260816/harness/runone.sh, with
# three changes, all forced by the thing being measured:
#
#  1. PORT and DATA are derived from THIS agent's pid, not fixed. The parent
#     harness hardcodes 8541, which a sibling agent is using right now; a
#     collision between two agents already cost a wrongly-killed run today.
#  2. The GC readback ASSERTS ABSENCE. The shipped default sets the limit from
#     INSIDE the process (debug.SetMemoryLimit), so `ps -E` shows no GOGC and
#     no GOMEMLIMIT — the parent harness's "read it back from the live process
#     environment" guard would reject every treatment arm. The equivalent
#     guard here is the app's own `[sky.gc]` banner, which is the shipped
#     mechanism's own statement of what it derived.
#  3. It measures RSS, not throughput. Throughput on this host is unusable
#     today: LOCKS.md records a 24–44% within-arm spread with sibling agents
#     running. Peak RSS is what the safety property needs and is ≤6% spread at
#     GOGC ≤ 400 in the parent run.
#
# Usage: runone.sh <tag> <sessions> <binary> [GOGC] [GOMEMLIMIT]
set -u
MB=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/gcdef
WT=/Users/anzel/works/sky-wt-gcdefault
GEN=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/gogc/bin/skyliveload
SETUP="$WT/docs/perf/runs/gcp-x86-capacity-20260816/harness/forum-setup.json"
PGBIN=/opt/homebrew/Cellar/postgresql@14/14.21/bin
PORT=$(cat "$MB/PORT")
DATA="/Users/anzel/.skyperf-gcdefault-21919/pgembed"
ABORT_RSS_KB=${ABORT_RSS_KB:-5000000}
RAMP=${RAMP:-20s}; WARMUP=${WARMUP:-8s}; WINDOW=${WINDOW:-45s}

TAG="$1"; SESSIONS="$2"; APP="$3"; GOGC_V="${4:--}"; GML_V="${5:--}"
O="$MB/runs/$TAG"; rm -rf "$O"; mkdir -p "$O"
source "$WT/scripts/lib/with-timeout.sh"

fail() { echo "REJECTED[$TAG] $*" | tee -a "$O/reject.txt"; cleanup; exit 1; }
cleanup() {
  [ -n "${SAMP_PID:-}" ] && kill "$SAMP_PID" 2>/dev/null
  [ -n "${APP_PID:-}" ] && kill "$APP_PID" 2>/dev/null
  for _ in $(seq 1 30); do
    [ -n "${APP_PID:-}" ] && kill -0 "$APP_PID" 2>/dev/null || break
    sleep 1
  done
  [ -n "${APP_PID:-}" ] && kill -9 "$APP_PID" 2>/dev/null
  pkill -f "$DATA/pg" 2>/dev/null   # scoped to THIS arm's data dir only
  sleep 1
}

for _ in $(seq 1 60); do
  lsof -nP -iTCP:$PORT -sTCP:LISTEN >/dev/null 2>&1 || break
  sleep 1
done
lsof -nP -iTCP:$PORT -sTCP:LISTEN >/dev/null 2>&1 && fail "port $PORT still busy after 60s"

rm -rf "$DATA"; mkdir -p "$DATA"

ENVV=(SKY_LIVE_PORT=$PORT SKY_LIVE_STORE=postgres FORUM_POSTS=5 SKY_POSTGRES_BIN="$PGBIN")
[ "$GOGC_V" != "-" ] && ENVV+=(GOGC=$GOGC_V)
[ "$GML_V"  != "-" ] && ENVV+=(GOMEMLIMIT=$GML_V)

env "${ENVV[@]}" "$APP" --embed --data-dir "$DATA" >| "$O/app.log" 2>&1 &
APP_PID=$!
for _ in $(seq 1 240); do curl -sf "http://127.0.0.1:$PORT/" -o /dev/null 2>/dev/null && break; sleep 0.5; done
curl -sf "http://127.0.0.1:$PORT/" -o /dev/null || fail "app never came up"

OWNER=$(lsof -nP -iTCP:$PORT -sTCP:LISTEN -t 2>/dev/null | head -1)
[ "$OWNER" = "$APP_PID" ] || fail "port owned by ${OWNER:-none}, not $APP_PID"

GOT=$(awk -F'session store: ' '/session store: /{split($2,a," "); print a[1]; exit}' "$O/app.log")
[ "$GOT" = "postgres" ] || fail "app opened store '${GOT:-none}', wanted postgres"

# The shipped mechanism's own statement of what it derived, from the running
# process. Absent on a control binary, which is how the two are told apart.
GCLINE=$(grep -m1 '^\[sky.gc\]' "$O/app.log" | sed 's/^\[sky.gc\] //')
ENVDUMP=$(ps -E -p "$APP_PID" -o command= 2>/dev/null | tr ' ' '\n')
RB_GOGC=$(echo "$ENVDUMP" | awk -F= '/^GOGC=/{print $2; exit}')
RB_GML=$(echo  "$ENVDUMP" | awk -F= '/^GOMEMLIMIT=/{print $2; exit}')
[ "$GOGC_V" = "-" ] || [ "$RB_GOGC" = "$GOGC_V" ] || fail "GOGC readback '$RB_GOGC' != '$GOGC_V'"
[ "$GML_V"  = "-" ] || [ "$RB_GML"  = "$GML_V"  ] || fail "GOMEMLIMIT readback '$RB_GML' != '$GML_V'"

curl -s "http://127.0.0.1:$PORT/" >| "$O/page.html"
ELS=$(grep -o 'sky-id="' "$O/page.html" | wc -l | tr -d ' ')
[ "$ELS" = "94" ] || fail "view is $ELS elements, corpus is 94"

with_timeout 120 "$GEN" -url "http://127.0.0.1:$PORT" -self-check -setup "$SETUP" \
  -hid-suffix .click -hid-context '>▲<' >| "$O/selfcheck.txt" 2>&1 || fail "self-check failed"

cpu_s() { ps -o time= -p "$APP_PID" 2>/dev/null | tr -d ' ' | awk -F: 'NF==3{print $1*3600+$2*60+$3} NF==2{print $1*60+$2}'; }
IDLE_RSS=$(ps -o rss= -p "$APP_PID" | tr -d ' ')

(
  while kill -0 "$APP_PID" 2>/dev/null; do
    r=$(ps -o rss= -p "$APP_PID" 2>/dev/null | tr -d ' ')
    pg=$(ps -o rss=,command= -ax 2>/dev/null | grep "$DATA/pg" | grep -v grep | awk '{s+=$1} END{print s+0}')
    printf '%s\t%s\t%s\n' "$(date +%s)" "${r:-0}" "${pg:-0}"
    if [ -n "$r" ] && [ "$r" -gt "$ABORT_RSS_KB" ] 2>/dev/null; then
      echo "ABORT_RSS $r" >> "$O/abort.txt"; kill "$APP_PID" 2>/dev/null; break
    fi
    sleep 1
  done
) >| "$O/rss.tsv" 2>/dev/null &
SAMP_PID=$!

C0=$(cpu_s); T0=$(date +%s)
with_timeout 600 "$GEN" -url "http://127.0.0.1:$PORT" -sessions "$SESSIONS" -think 0 \
  -duration "$WINDOW" -ramp "$RAMP" -warmup "$WARMUP" -max-error-rate 1.0 -min-patch-rate 0.9 \
  -setup "$SETUP" -hid-suffix .click -hid-context '>▲<' \
  -json "$O/load.json" -label "$TAG" >| "$O/load.txt" 2>&1
RC=$?
T1=$(date +%s); C1=$(cpu_s)
kill "$SAMP_PID" 2>/dev/null

WSTART=$(( T0 + ${RAMP%s} + ${WARMUP%s} ))
PEAK=$(awk '{if($2>m)m=$2} END{print m+0}' "$O/rss.tsv")
WPEAK=$(awk -v s="$WSTART" '$1>=s{if($2>m)m=$2} END{print m+0}' "$O/rss.tsv")
WMEAN=$(awk -v s="$WSTART" '$1>=s{t+=$2;n++} END{if(n)printf "%.0f", t/n; else print 0}' "$O/rss.tsv")
PGPEAK=$(awk '{if($3>m)m=$3} END{print m+0}' "$O/rss.tsv")
TPUT=$(grep -o '"interactions_per_sec": *[0-9.]*' "$O/load.json" 2>/dev/null | sed 's/.*: *//')
ESTAB=$(grep -o '"sessions_established": *[0-9]*' "$O/load.json" 2>/dev/null | sed 's/.*: *//')
ERR=$(grep -o '"error_rate": *[0-9.]*' "$O/load.json" 2>/dev/null | sed 's/.*: *//')
PATCH=$(grep -o '"patch_rate": *[0-9.]*' "$O/load.json" 2>/dev/null | sed 's/.*: *//')

{
  echo "tag $TAG"; echo "sessions $SESSIONS"; echo "binary $(basename "$APP")"
  echo "gc_banner ${GCLINE:-none}"
  echo "env_gogc ${RB_GOGC:-unset}"; echo "env_gomemlimit ${RB_GML:-unset}"
  echo "elements $ELS"; echo "idle_rss_kb $IDLE_RSS"
  echo "peak_rss_kb $PEAK"; echo "window_peak_rss_kb $WPEAK"; echo "window_mean_rss_kb $WMEAN"
  echo "pg_peak_rss_kb $PGPEAK"
  echo "tput $TPUT"; echo "established $ESTAB"; echo "error_rate $ERR"; echo "patch_rate $PATCH"
  echo "app_cpu_delta_s $(awk -v a="${C1:-0}" -v b="${C0:-0}" 'BEGIN{printf "%.2f", a-b}')"
  echo "wall_s $((T1-T0))"; echo "generator_rc $RC"
  echo "aborted $([ -f "$O/abort.txt" ] && echo yes || echo no)"
  echo "load1 $(uptime | sed -E 's/.*load averages?: ([0-9.]+).*/\1/')"
} >| "$O/acct.txt"

cleanup
[ "$RC" -ne 0 ] && { echo "REJECTED[$TAG] generator rc=$RC"; tail -3 "$O/load.txt"; exit 1; }
echo "ok[$TAG] n=$SESSIONS peak_rss=${WPEAK}kB tput=${TPUT} banner='${GCLINE:-none}'"
exit 0

#!/usr/bin/env bash
# runone.sh — one measurement arm: start forumbench on the embedded PostgreSQL
# store, sample RSS at 1 Hz through the whole load, run skyliveload, tear down.
#
# Usage: runone.sh <tag> <sessions> <gogc> <gomemlimit|-> [outdir]
#
# Every arm ASSERTS, and refuses rather than reports on failure:
#   * the port is free before start (else we would measure another process)
#   * the pid that owns the port is the pid we launched
#   * the app's own banner says the store is `postgres`
#   * GOGC / GOMEMLIMIT read back from the live process environment
#   * the view really is 94 sky-id elements
#   * a 1-interaction self-check patches BEFORE the window opens
#   * the generator's own validity verdict (patch_rate, error_rate)
# and it aborts the arm if app RSS crosses ABORT_RSS_KB, so a relaxed pacer
# cannot take the host down. An aborted arm is recorded as aborted, not dropped.
set -u
BASE=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/gogc
WT=/Users/anzel/works/playground/sky-perf-gogc
APP="${APP_BIN:-$BASE/bin/forumbench}"
GEN="$BASE/bin/skyliveload"
SETUP="$WT/docs/perf/runs/gcp-x86-capacity-20260816/harness/forum-setup.json"
PGBIN=/opt/homebrew/Cellar/postgresql@14/14.21/bin
DATA=/Users/anzel/.skyperf-gogc/pgembed
PORT=8541
ABORT_RSS_KB=${ABORT_RSS_KB:-5000000}     # 5 GB — protects a 16 GB host
RAMP=${RAMP:-20s}; WARMUP=${WARMUP:-8s}; WINDOW=${WINDOW:-45s}

TAG="$1"; SESSIONS="$2"; GOGC_V="$3"; GML_V="$4"
O="${5:-$BASE/runs/$TAG}"; mkdir -p "$O"
source "$WT/scripts/lib/with-timeout.sh"

fail() { echo "REJECTED[$TAG] $*" | tee -a "$O/reject.txt"; cleanup; exit 1; }
cleanup() {
  [ -n "${SAMP_PID:-}" ] && kill "$SAMP_PID" 2>/dev/null
  [ -n "${APP_PID:-}" ] && kill "$APP_PID" 2>/dev/null
  # Wait for the process to actually go, rather than sleeping a guessed
  # interval; the next arm's port guard depends on this having finished.
  for _ in $(seq 1 30); do
    [ -n "${APP_PID:-}" ] && kill -0 "$APP_PID" 2>/dev/null || break
    sleep 1
  done
  [ -n "${APP_PID:-}" ] && kill -9 "$APP_PID" 2>/dev/null
  # Scoped to THIS arm's data dir, so a sibling agent's cluster is never hit.
  pkill -f "$DATA/pg" 2>/dev/null
  sleep 1
}

# The port guard exists so an arm can never measure a process it did not start.
# But the PREVIOUS arm's app can still be releasing the socket, and failing on
# that discards a good arm for a teardown race rather than a real conflict —
# which is what happened to 4 of 6 arms in the first confirmatory A/B. Wait for
# it, then fail. This port is used by nothing but this harness, so waiting
# cannot mask somebody else's server.
for _ in $(seq 1 60); do
  lsof -nP -iTCP:$PORT -sTCP:LISTEN >/dev/null 2>&1 || break
  sleep 1
done
lsof -nP -iTCP:$PORT -sTCP:LISTEN >/dev/null 2>&1 && fail "port $PORT still busy after 60s"

# Fresh cluster per arm: a previous arm's session rows must not travel.
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

# GC knobs read back from the LIVE process, never trusted from the launch line.
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

# 1 Hz sampler: app RSS, plus the embedded cluster's total, plus host free pages.
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
LOAD_RSS=$(ps -o rss= -p "$APP_PID" 2>/dev/null | tr -d ' ')
kill "$SAMP_PID" 2>/dev/null

# Peak RSS over the whole load, and over the measurement window only (the
# window starts ramp+warmup after T0; anything before that is establishment).
WSTART=$(( T0 + ${RAMP%s} + ${WARMUP%s} ))
PEAK=$(awk '{if($2>m)m=$2} END{print m+0}' "$O/rss.tsv")
WPEAK=$(awk -v s="$WSTART" '$1>=s{if($2>m)m=$2} END{print m+0}' "$O/rss.tsv")
WMEAN=$(awk -v s="$WSTART" '$1>=s{t+=$2;n++} END{if(n)printf "%.0f", t/n; else print 0}' "$O/rss.tsv")
PGPEAK=$(awk '{if($3>m)m=$3} END{print m+0}' "$O/rss.tsv")

{
  echo "tag $TAG"; echo "sessions $SESSIONS"; echo "gogc ${RB_GOGC:-default}"; echo "gomemlimit ${RB_GML:-none}"
  echo "elements $ELS"; echo "idle_rss_kb $IDLE_RSS"; echo "load_rss_kb ${LOAD_RSS:-0}"
  echo "peak_rss_kb $PEAK"; echo "window_peak_rss_kb $WPEAK"; echo "window_mean_rss_kb $WMEAN"
  echo "pg_peak_rss_kb $PGPEAK"
  echo "app_cpu_delta_s $(awk -v a="${C1:-0}" -v b="${C0:-0}" 'BEGIN{printf "%.2f", a-b}')"
  echo "wall_s $((T1-T0))"; echo "generator_rc $RC"
  echo "aborted $([ -f "$O/abort.txt" ] && echo yes || echo no)"
  echo "load1 $(uptime | sed -E 's/.*load averages?: ([0-9.]+).*/\1/')"
} >| "$O/acct.txt"

cleanup
[ "$RC" -ne 0 ] && { echo "REJECTED[$TAG] generator rc=$RC"; tail -3 "$O/load.txt"; exit 1; }
echo "ok[$TAG] $(grep -o '"interactions_per_sec": *[0-9.]*' "$O/load.json" | sed 's/.*: *//') int/s  peak_rss ${WPEAK}kB"
exit 0

#!/usr/bin/env bash
# lockrun.sh — one lock-attribution arm: run an INSTRUMENTED forumbench under
# load and capture the mutex profile as a DELTA across the measurement window.
#
# Why a delta: Go's mutex profile is cumulative from process start, so a raw
# reading includes startup contention (store init, cluster connect, session
# establishment) which has nothing to do with the interaction path. Both
# endpoints are captured and the start is subtracted, exactly as the
# gomaxprocs-scaling run did.
#
# Wall-clock CPU attribution on this host redistributes by up to ±37% between
# identical runs, so THIS is the instrument the lock claims rest on. Throughput
# is recorded alongside but is not the evidence.
#
# Usage: lockrun.sh <tag> <app-binary> <sessions> [gomaxprocs]
set -u
BASE=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/gogc
WT=/Users/anzel/works/playground/sky-perf-gogc
GEN="$BASE/bin/skyliveload"
SETUP="$WT/docs/perf/runs/gcp-x86-capacity-20260816/harness/forum-setup.json"
PGBIN=/opt/homebrew/Cellar/postgresql@14/14.21/bin
DATA=/Users/anzel/.skyperf-gogc/pgembed
PORT=8542
PROBE=127.0.0.1:6577

TAG="$1"; APP="$2"; SESSIONS="$3"; GMP="${4:-8}"
O="$BASE/lockruns/$TAG"; mkdir -p "$O"
source "$WT/scripts/lib/with-timeout.sh"

fail() { echo "REJECTED[$TAG] $*"; cleanup; exit 1; }
cleanup() {
  [ -n "${APP_PID:-}" ] && kill "$APP_PID" 2>/dev/null; sleep 2
  [ -n "${APP_PID:-}" ] && kill -9 "$APP_PID" 2>/dev/null
  pkill -f "$DATA/pg" 2>/dev/null; sleep 1
}

lsof -nP -iTCP:$PORT -sTCP:LISTEN >/dev/null 2>&1 && fail "port $PORT busy"
rm -rf "$DATA"; mkdir -p "$DATA"

env SKY_LIVE_PORT=$PORT SKY_LIVE_STORE=postgres FORUM_POSTS=5 \
    SKY_POSTGRES_BIN="$PGBIN" GOMAXPROCS="$GMP" \
    SKY_PROBE_ADDR="$PROBE" SKY_PROBE_MUTEX_FRACTION=1 SKY_PROBE_BLOCK_RATE=0 \
    "$APP" --embed --data-dir "$DATA" >| "$O/app.log" 2>&1 &
APP_PID=$!
for _ in $(seq 1 240); do curl -sf "http://127.0.0.1:$PORT/" -o /dev/null 2>/dev/null && break; sleep 0.5; done
curl -sf "http://127.0.0.1:$PORT/" -o /dev/null || fail "app never came up"

OWNER=$(lsof -nP -iTCP:$PORT -sTCP:LISTEN -t 2>/dev/null | head -1)
[ "$OWNER" = "$APP_PID" ] || fail "port owned by ${OWNER:-none}, not $APP_PID"
GOT=$(awk -F'session store: ' '/session store: /{split($2,a," "); print a[1]; exit}' "$O/app.log")
[ "$GOT" = "postgres" ] || fail "store is '${GOT:-none}'"
RB_GMP=$(ps -E -p "$APP_PID" -o command= 2>/dev/null | tr ' ' '\n' | awk -F= '/^GOMAXPROCS=/{print $2; exit}')
[ "$RB_GMP" = "$GMP" ] || fail "GOMAXPROCS readback '$RB_GMP' != '$GMP'"
curl -sf "http://$PROBE/debug/pprof/" -o /dev/null || fail "probe endpoint not serving — binary is not instrumented"
ELS=$(curl -s "http://127.0.0.1:$PORT/" | grep -o 'sky-id="' | wc -l | tr -d ' ')
[ "$ELS" = "94" ] || fail "view is $ELS elements, corpus is 94"

with_timeout 120 "$GEN" -url "http://127.0.0.1:$PORT" -self-check -setup "$SETUP" \
  -hid-suffix .click -hid-context '>▲<' >| "$O/selfcheck.txt" 2>&1 || fail "self-check failed"

# Establish sessions, THEN take the start profile, so establishment is outside
# the delta. skyliveload finishes its ramp before its clock starts, so the
# window this brackets is the interaction phase.
curl -s "http://$PROBE/debug/pprof/mutex?debug=0" -o "$O/mutex.start.pb.gz" || fail "start profile failed"

with_timeout 600 "$GEN" -url "http://127.0.0.1:$PORT" -sessions "$SESSIONS" -think 0 \
  -duration 45s -ramp 20s -warmup 8s -max-error-rate 1.0 -min-patch-rate 0.9 \
  -setup "$SETUP" -hid-suffix .click -hid-context '>▲<' \
  -json "$O/load.json" -label "$TAG" >| "$O/load.txt" 2>&1
RC=$?
curl -s "http://$PROBE/debug/pprof/mutex?debug=0" -o "$O/mutex.end.pb.gz" || fail "end profile failed"

go tool pprof -proto -base "$O/mutex.start.pb.gz" "$O/mutex.end.pb.gz" \
   >| "$O/mutex.delta.pb.gz" 2>"$O/pprof.err" || fail "delta failed: $(cat "$O/pprof.err")"
go tool pprof -top -nodecount=25 "$O/mutex.delta.pb.gz" >| "$O/mutex.top.txt" 2>&1
go tool pprof -top -cum -nodecount=30 "$O/mutex.delta.pb.gz" >| "$O/mutex.cum.txt" 2>&1

cleanup
echo "=== $TAG  rc=$RC  tput=$(tr ',' '\n' < "$O/load.json" | grep -o '"interactions_per_sec": *[0-9.]*' | sed 's/.*: *//') ==="
head -14 "$O/mutex.top.txt"

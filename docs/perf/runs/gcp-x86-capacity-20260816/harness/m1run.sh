#!/usr/bin/env bash
# m1run.sh — the M1 side of the x86-vs-M1 per-core factor.
#
# Reproduces docs/perf/runs/stage2-typed-hof-20260816's conditions exactly:
# forumbench at FORUM_POSTS=5, memory store, GOMAXPROCS=1, generator on the
# same host over loopback, 25 sessions, closed loop, 45 s window, 3 s ramp,
# 3 s warmup, 3 repeats. The x86 arm on skyperf-core runs the identical
# configuration, so the ratio is a machine ratio and not a workload ratio.
set -u
BASE=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/x86bench
WT=/Users/anzel/works/playground/sky-bench-x86
APP="$BASE/forumbench/sky-out/app"
GEN="$BASE/bin/skyliveload"
SETUP=/Users/anzel/works/playground/sky/docs/perf/runs/forum-rebaseline-20260816/harness/forum-setup.json
OUT="$BASE/m1"; mkdir -p "$OUT"
PORT=8541
source "$WT/scripts/lib/with-timeout.sh"

if lsof -nP -iTCP:$PORT -sTCP:LISTEN >/dev/null 2>&1; then
  echo "port $PORT busy — refusing (would measure the wrong process)"; exit 69
fi

for REP in 1 2 3; do
  O="$OUT/r$REP"; mkdir -p "$O"
  SKY_LIVE_PORT=$PORT SKY_LIVE_STORE=memory FORUM_POSTS=5 GOMAXPROCS=1 \
    "$APP" >| "$O/app.log" 2>&1 &
  APP_PID=$!
  for _ in $(seq 1 80); do curl -sf "http://127.0.0.1:$PORT/" -o /dev/null 2>/dev/null && break; sleep 0.25; done
  curl -sf "http://127.0.0.1:$PORT/" -o /dev/null || { echo "rep$REP app never came up"; kill $APP_PID; continue; }

  # the pid that owns the port IS the process we measure
  OWNER=$(lsof -nP -iTCP:$PORT -sTCP:LISTEN -t 2>/dev/null | head -1)
  [ "$OWNER" = "$APP_PID" ] || { echo "rep$REP port owned by $OWNER not $APP_PID"; kill $APP_PID; continue; }

  # store ASSERTED from the app's own banner, never assumed
  GOT=$(awk -F'session store: ' '/session store: /{split($2,a," "); print a[1]; exit}' "$O/app.log")
  [ "$GOT" = "memory" ] || { echo "rep$REP REJECTED: opened store '$GOT'"; kill $APP_PID; continue; }

  curl -s "http://127.0.0.1:$PORT/" >| "$O/page.html"
  ELS=$(grep -o 'sky-id="' "$O/page.html" | wc -l | tr -d ' ')

  # PRECONDITION: this handler patches on every press
  if ! with_timeout 120 "$GEN" -url "http://127.0.0.1:$PORT" -self-check \
        -setup "$SETUP" -hid-suffix .click -hid-context '>▲<' >| "$O/selfcheck.txt" 2>&1; then
    echo "rep$REP REJECTED: self-check failed"; cat "$O/selfcheck.txt" | tail -5; kill $APP_PID; continue
  fi

  cpu_s() { ps -o time= -p "$APP_PID" | tr -d ' ' | awk -F: 'NF==3{print $1*3600+$2*60+$3} NF==2{print $1*60+$2}'; }
  echo "idle_rss_kb $(ps -o rss= -p $APP_PID | tr -d ' ')" >| "$O/acct.txt"
  C0=$(cpu_s)
  with_timeout 300 "$GEN" -url "http://127.0.0.1:$PORT" -sessions 25 -think 0 \
      -duration 45s -ramp 3s -warmup 3s -max-error-rate 1.0 -min-patch-rate 0.9 \
      -setup "$SETUP" -hid-suffix .click -hid-context '>▲<' \
      -json "$O/load.json" -label "m1-r$REP" >| "$O/load.txt" 2>&1
  RC=$?
  C1=$(cpu_s)
  {
    echo "load_rss_kb $(ps -o rss= -p $APP_PID | tr -d ' ')"
    echo "app_cpu_delta_s $(awk -v a="$C1" -v b="$C0" 'BEGIN{printf "%.2f", a-b}')"
    echo "elements $ELS"
    echo "generator_rc $RC"
    echo "load1 $(uptime | sed -E 's/.*load averages?: ([0-9.]+).*/\1/')"
  } >> "$O/acct.txt"
  kill $APP_PID 2>/dev/null; wait $APP_PID 2>/dev/null
  [ "$RC" -ne 0 ] && echo "rep$REP REJECTED: generator rc=$RC" && continue
  echo "rep$REP ok"
  sleep 5
done

printf 'rep\telements\ttput\tp50\tp95\tp99\terr\tpatch_rate\tvalid\tints\tcpu_s\tidle_rss\tload_rss\tgen_cpu\n' >| "$OUT/m1.tsv"
for REP in 1 2 3; do
  O="$OUT/r$REP"; J="$O/load.json"; [ -f "$J" ] || continue
  jg() { grep -o "\"$1\": *[0-9.eE+-]*" "$J" | head -1 | sed 's/.*: *//'; }
  ag() { awk -v k="$1" '$1==k{print $2}' "$O/acct.txt"; }
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$REP" "$(ag elements)" \
    "$(jg interactions_per_sec)" "$(jg p50_ms)" "$(jg p95_ms)" "$(jg p99_ms)" "$(jg error_rate)" \
    "$(jg patch_rate)" "$(grep -o '"valid": *[a-z]*' "$J"|head -1|sed 's/.*: *//')" \
    "$(jg interactions_counted)" "$(ag app_cpu_delta_s)" "$(ag idle_rss_kb)" "$(ag load_rss_kb)" \
    "$(jg generator_cpu_percent_of_machine)" >> "$OUT/m1.tsv"
done
column -t -s$'\t' "$OUT/m1.tsv"

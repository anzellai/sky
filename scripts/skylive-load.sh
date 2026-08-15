#!/usr/bin/env bash
# Sky.Live load harness: one command, parameterised, re-runnable.
#
#   scripts/skylive-load.sh --app examples/26-ui-showcase
#   scripts/skylive-load.sh --app examples/52-blog-analytics --label analytics=on
#   CONCURRENCY="100 500 1000" DURATION=60s scripts/skylive-load.sh
#
# For each concurrency level it:
#   1. drives the app with skyliveload (the real Sky.Live protocol:
#      session cookie, CSRF, held SSE stream, POST /_sky/event),
#   2. observes the same app with ONE real Chromium, measuring what a
#      user actually perceives (click -> DOM mutation),
#   3. samples the server's RSS and its PostgreSQL backend count.
#
# The browser is deliberately not part of the load -- see
# scripts/skylive-observer.mjs for why.
#
# Every run records its conditions (host, commit, container flags) so a
# number cannot be quoted without them, and refuses to report when the
# load generator failed to actually load. See the validity gates in
# tools/skyliveload/main.go.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

APP="${APP:-examples/26-ui-showcase}"
LABEL="${LABEL:-}"
CONCURRENCY="${CONCURRENCY:-1 100 500 1000}"
DURATION="${DURATION:-30s}"
THINK="${THINK:-1s}"
RAMP="${RAMP:-5s}"
PORT="${PORT:-8477}"
OBSERVER="${OBSERVER:-1}"
OBS_SAMPLES="${OBS_SAMPLES:-30}"
REPEATS="${REPEATS:-3}"
PGURL="${PGURL:-}"

while [ $# -gt 0 ]; do
  case "$1" in
    --app) APP="$2"; shift 2 ;;
    --label) LABEL="$2"; shift 2 ;;
    --concurrency) CONCURRENCY="$2"; shift 2 ;;
    --duration) DURATION="$2"; shift 2 ;;
    --think) THINK="$2"; shift 2 ;;
    --port) PORT="$2"; shift 2 ;;
    --repeats) REPEATS="$2"; shift 2 ;;
    --no-observer) OBSERVER=0; shift ;;
    *) echo "unknown flag: $1" >&2; exit 64 ;;
  esac
done

STAMP="$(date +%Y%m%d-%H%M%S)"
OUTDIR="${OUTDIR:-$ROOT/docs/perf/runs/load-$STAMP}"
mkdir -p "$OUTDIR"

# ---------------------------------------------------------------------
# Conditions, written first
# ---------------------------------------------------------------------
{
  echo "timestamp        $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "commit           $(git rev-parse HEAD 2>/dev/null || echo unknown)"
  echo "branch           $(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown)"
  echo "host             $(hostname)"
  echo "os               $(uname -srm)"
  echo "cpu              $(sysctl -n machdep.cpu.brand_string 2>/dev/null || echo unknown)"
  echo "cores            $(sysctl -n hw.ncpu 2>/dev/null || nproc)"
  echo "mem_bytes        $(sysctl -n hw.memsize 2>/dev/null || echo unknown)"
  echo "load1_at_start   $(uptime | sed -E 's/.*load averages?: ([0-9.]+).*/\1/')"
  echo "app              $APP"
  echo "label            ${LABEL:-none}"
  echo "concurrency      $CONCURRENCY"
  echo "duration         $DURATION"
  echo "think            $THINK"
  echo "repeats          $REPEATS"
  echo "container_flags  ${CONTAINER_FLAGS:-none (bare host)}"
} | tee "$OUTDIR/env.txt"
echo

# ---------------------------------------------------------------------
# Build the generator + the app under test
# ---------------------------------------------------------------------
echo "==> building load generator"
(cd tools/skyliveload && go build -o "$OUTDIR/skyliveload" .)

SKY_BIN="${SKY_BIN:-$ROOT/rust/target/release/sky}"
if [ ! -x "$SKY_BIN" ]; then
  echo "no sky binary at $SKY_BIN; set SKY_BIN=/path/to/sky" >&2
  exit 66
fi

if [ ! -x "$ROOT/$APP/sky-out/app" ]; then
  echo "==> building $APP"
  (cd "$ROOT/$APP" && "$SKY_BIN" build src/Main.sky)
fi

# ---------------------------------------------------------------------
# Start the app. Trap guarantees it dies even on failure -- an orphaned
# Sky.Live server holds a port and its memory for the whole session.
# ---------------------------------------------------------------------
APP_LOG="$OUTDIR/app.log"
( cd "$ROOT/$APP" && SKY_LIVE_PORT="$PORT" ./sky-out/app >| "$APP_LOG" 2>&1 ) &
APP_PID=$!
cleanup() {
  [ -n "${APP_PID:-}" ] && kill "$APP_PID" 2>/dev/null || true
  wait "$APP_PID" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

echo "==> waiting for the app on :$PORT"
for _ in $(seq 1 60); do
  if curl -sf "http://127.0.0.1:$PORT/" -o /dev/null 2>/dev/null; then break; fi
  sleep 0.5
done
if ! curl -sf "http://127.0.0.1:$PORT/" -o /dev/null 2>/dev/null; then
  echo "app never became reachable on :$PORT; log follows" >&2
  tail -20 "$APP_LOG" >&2
  exit 69
fi

# The real server PID: the app may re-exec (e.g. the embedded-postgres
# supervisor), so resolve by port rather than trusting $APP_PID for RSS.
server_pid() {
  lsof -nP -iTCP:"$PORT" -sTCP:LISTEN -t 2>/dev/null | head -1
}
rss_kb() {
  local p; p="$(server_pid)"
  [ -n "$p" ] && ps -o rss= -p "$p" 2>/dev/null | tr -d ' ' || echo 0
}
pg_backends() {
  [ -z "$PGURL" ] && { echo "n/a"; return; }
  psql "$PGURL" -tAc \
    "select count(*) from pg_stat_activity where state is not null" 2>/dev/null || echo "n/a"
}

# ---------------------------------------------------------------------
# Prove the client speaks the protocol BEFORE trusting any number.
# ---------------------------------------------------------------------
echo
echo "==> self-check: is the generator actually driving the app?"
if ! "$OUTDIR/skyliveload" -url "http://127.0.0.1:$PORT" -self-check | tee "$OUTDIR/self-check.txt"; then
  echo "SELF-CHECK FAILED -- refusing to run a load test whose client does not" >&2
  echo "speak the protocol. Any throughput it reported would be fiction." >&2
  exit 70
fi

# ---------------------------------------------------------------------
# Sweep
# ---------------------------------------------------------------------
SUMMARY="$OUTDIR/summary.tsv"
printf "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n" \
  sessions rep throughput p50_ms p95_ms p99_ms err_rate rss_mb pg_backends valid > "$SUMMARY"

OBS_SUMMARY="$OUTDIR/observer.tsv"
printf "%s\t%s\t%s\t%s\t%s\n" synthetic_sessions browser_p50_ms browser_p95_ms browser_p99_ms samples > "$OBS_SUMMARY"

for n in $CONCURRENCY; do
  for rep in $(seq 1 "$REPEATS"); do
    echo
    echo "==> $n sessions, repetition $rep/$REPEATS"
    json="$OUTDIR/load-n${n}-r${rep}.json"

    "$OUTDIR/skyliveload" \
      -url "http://127.0.0.1:$PORT" \
      -sessions "$n" -duration "$DURATION" -think "$THINK" -ramp "$RAMP" \
      -label "${LABEL}" -json "$json" || true

    rss="$(rss_kb)"; pgb="$(pg_backends)"
    if [ -f "$json" ]; then
      awk -v n="$n" -v rep="$rep" -v rss="$rss" -v pgb="$pgb" '
        /"interactions_per_sec"/ { gsub(/[",]/,""); tp=$2 }
        /"p50_ms"/  { gsub(/[",]/,""); p50=$2 }
        /"p95_ms"/  { gsub(/[",]/,""); p95=$2 }
        /"p99_ms"/  { gsub(/[",]/,""); p99=$2 }
        /"error_rate"/ { gsub(/[",]/,""); er=$2 }
        /"valid"/   { gsub(/[",]/,""); v=$2 }
        END { printf "%s\t%s\t%.1f\t%.2f\t%.2f\t%.2f\t%.4f\t%.1f\t%s\t%s\n",
                n, rep, tp, p50, p95, p99, er, rss/1024, pgb, v }
      ' "$json" >> "$SUMMARY"
    fi
  done

  # One browser observes at this concurrency level, while the load is
  # NOT running -- a steady-state reading would need the load held, so
  # we re-raise it in the background for the observation window.
  if [ "$OBSERVER" = "1" ] && command -v node >/dev/null 2>&1; then
    echo "==> observing with a real browser under $n synthetic sessions"
    "$OUTDIR/skyliveload" -url "http://127.0.0.1:$PORT" -sessions "$n" \
      -duration "$DURATION" -think "$THINK" -ramp 2s \
      -json "$OUTDIR/load-during-observe-n${n}.json" >/dev/null 2>&1 &
    LOADPID=$!
    sleep 4   # let the synthetic load reach steady state
    node scripts/skylive-observer.mjs \
      --url "http://127.0.0.1:$PORT" --samples "$OBS_SAMPLES" --think 200 \
      --label "under-${n}-sessions" \
      --json "$OUTDIR/observer-n${n}.json" >/dev/null 2>&1 || true
    wait "$LOADPID" 2>/dev/null || true

    if [ -f "$OUTDIR/observer-n${n}.json" ]; then
      awk -v n="$n" '
        /"p50_ms"/ { gsub(/[",]/,""); p50=$2 }
        /"p95_ms"/ { gsub(/[",]/,""); p95=$2 }
        /"p99_ms"/ { gsub(/[",]/,""); p99=$2 }
        /"samples_observed"/ { gsub(/[",]/,""); s=$2 }
        END { printf "%s\t%.2f\t%.2f\t%.2f\t%s\n", n, p50, p95, p99, s }
      ' "$OUTDIR/observer-n${n}.json" >> "$OBS_SUMMARY"
    fi
  fi
done

echo
echo "==> load sweep (3 repetitions per level; compare them, do not average blindly)"
column -t -s "$(printf '\t')" "$SUMMARY" 2>/dev/null || cat "$SUMMARY"
if [ "$OBSERVER" = "1" ]; then
  echo
  echo "==> what a real browser perceived at each synthetic concurrency"
  column -t -s "$(printf '\t')" "$OBS_SUMMARY" 2>/dev/null || cat "$OBS_SUMMARY"
fi

echo "load1_at_end     $(uptime | sed -E 's/.*load averages?: ([0-9.]+).*/\1/')" >> "$OUTDIR/env.txt"
echo
echo "==> results in $OUTDIR"

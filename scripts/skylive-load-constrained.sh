#!/usr/bin/env bash
# Run the Sky.Live load harness against an app confined to a small
# machine, to bracket what a constrained VM can serve.
#
#   scripts/skylive-load-constrained.sh --app examples/26-ui-showcase
#
# TOPOLOGY: the APP runs in the container; the LOAD GENERATOR runs on the
# host. Constraining the server is the point; putting the generator
# inside the same quota would have it competing with the thing it is
# measuring, and the run would report the contention rather than the
# server's capacity.
#
# ============================ READ THIS ============================
# WHAT THESE NUMBERS ARE NOT
#
# 1. NOT a GCP e2-small. Apple's `container` runs an ARM64 Linux VM on
#    Apple silicon. e2-small is a shared-core x86 instance. Different
#    ISA, different memory subsystem, different hypervisor. These runs
#    are good for RELATIVE comparisons -- before/after a change, where
#    the throughput knee sits, memory per session -- and must never be
#    published as "you will serve N users on an e2-small". Only a run on
#    real GCP settles that.
#
# 2. NOT GCP's burstable credit model. An e2-small has a baseline CPU
#    entitlement and accrues credits it can spend to burst above it;
#    sustained load drains the credits and drops it to the floor. A
#    fixed vCPU allocation has no such dynamics. The 1-CPU and 2-CPU
#    runs bracket a throttled floor and a burst ceiling; real behaviour
#    moves between them over time in a way this cannot reproduce.
#
# 3. NOT the fractional quotas originally asked for. Apple's `container`
#    v1.0.0 accepts only INTEGER --cpus: it allocates whole vCPUs to a
#    VM rather than applying a CFS quota the way Docker's --cpus 0.5
#    does. `--cpus 0.5` and `--cpus 0.25` are rejected outright. The
#    e2-small baseline (0.5) and e2-micro baseline (0.25) therefore
#    CANNOT be reproduced with this tool, and the 1-CPU run is an
#    OPTIMISTIC stand-in for the e2-small baseline -- it is twice the
#    baseline entitlement. Reproducing the real floor needs Docker
#    (--cpus 0.5), a Linux host with cgroup v2 cpu.max, or a real VM.
#
# 4. Network adds a floor. Traffic crosses the VM's virtual NIC, which
#    adds latency a same-host run does not have. Compare constrained
#    runs with each other, not against the bare-host numbers.
# ===================================================================

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# Printed after every sweep, so the caveats travel with the numbers even
# when someone pastes only the tail of the output. Held in a heredoc
# rather than parsed back out of this file: an earlier version used
# `sed -n '/READ THIS/,/^# ===/p' "$0"`, whose own pattern line contained
# the opening marker, so the range re-opened on itself and dumped the
# rest of the script.
print_caveats() {
  cat <<'CAVEATS'
================= WHAT THESE NUMBERS ARE NOT =================
1. NOT a GCP e2-small. Apple's `container` runs an ARM64 Linux VM on
   Apple silicon; e2-small is a shared-core x86 instance. Good for
   RELATIVE comparisons -- before/after, where the knee sits, memory
   per session. Never publish as "you will serve N users on e2-small";
   only a run on real GCP settles that.

2. NOT GCP's burstable credit model. An e2-small has a baseline CPU
   entitlement and accrues credits to burst above it; sustained load
   drains them back to the floor. A fixed vCPU allocation has no such
   dynamics. These runs bracket a floor and a ceiling; real behaviour
   moves between them over time.

3. NOT the fractional quotas asked for. container v1.0.0 accepts only
   INTEGER --cpus -- whole vCPUs to a VM, not a CFS quota like Docker's
   --cpus 0.5, which it rejects outright. The e2-small baseline (0.5)
   and e2-micro baseline (0.25) cannot be reproduced with this tool.
   The 1-CPU run is an OPTIMISTIC stand-in at twice the e2-small
   baseline entitlement. The real floor needs Docker, cgroup v2
   cpu.max, or a real VM.

4. The VM's virtual NIC adds a latency floor. Compare constrained runs
   with each other, not against bare-host runs.
==============================================================
CAVEATS
}

APP="${APP:-examples/26-ui-showcase}"
IMAGE="${IMAGE:-alpine:3.19}"
HOST_PORT="${HOST_PORT:-8500}"
SESSIONS="${SESSIONS:-100 500}"
DURATION="${DURATION:-30s}"
THINK="${THINK:-1s}"
REPEATS="${REPEATS:-3}"
NAME="${NAME:-skybench}"

# Each entry is "<cpus>x<memory>". Integers only -- see note 3 above.
PROFILES="${PROFILES:-1x2g 2x2g 1x1g}"

while [ $# -gt 0 ]; do
  case "$1" in
    --app) APP="$2"; shift 2 ;;
    --profiles) PROFILES="$2"; shift 2 ;;
    --sessions) SESSIONS="$2"; shift 2 ;;
    --duration) DURATION="$2"; shift 2 ;;
    --repeats) REPEATS="$2"; shift 2 ;;
    *) echo "unknown flag: $1" >&2; exit 64 ;;
  esac
done

STAMP="$(date +%Y%m%d-%H%M%S)"
OUTDIR="${OUTDIR:-$ROOT/docs/perf/runs/constrained-$STAMP}"
mkdir -p "$OUTDIR"
STAGE="$OUTDIR/stage"
mkdir -p "$STAGE"

# ---------------------------------------------------------------------
# Build: a static linux/arm64 binary the container can run without libc
# ---------------------------------------------------------------------
SKY_BIN="${SKY_BIN:-$ROOT/rust/target/release/sky}"
if [ ! -d "$ROOT/$APP/sky-out" ]; then
  echo "==> building $APP with sky"
  (cd "$ROOT/$APP" && "$SKY_BIN" build src/Main.sky)
fi
echo "==> cross-compiling $APP for linux/arm64"
(cd "$ROOT/$APP/sky-out" && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o "$STAGE/app" .)

echo "==> building the load generator for the host"
(cd tools/skyliveload && go build -o "$OUTDIR/skyliveload" .)

{
  echo "timestamp        $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "commit           $(git rev-parse HEAD 2>/dev/null || echo unknown)"
  echo "host             $(hostname)"
  echo "host_os          $(uname -srm)"
  echo "host_cpu         $(sysctl -n machdep.cpu.brand_string 2>/dev/null || echo unknown)"
  echo "host_cores       $(sysctl -n hw.ncpu 2>/dev/null || nproc)"
  echo "container_cli    $(container --version 2>&1 | head -1)"
  echo "image            $IMAGE"
  echo "guest_arch       linux/arm64 (ARM VM on Apple silicon -- NOT x86 e2-small)"
  echo "app              $APP"
  echo "profiles         $PROFILES"
  echo "sessions         $SESSIONS"
  echo "duration         $DURATION"
  echo "repeats          $REPEATS"
  echo "caveat_cpu_model fixed vCPU allocation, NOT GCP burstable credits"
  echo "caveat_fractional  --cpus 0.5/0.25 unsupported by container v1.0.0 (integer only)"
} | tee "$OUTDIR/env.txt"

SUMMARY="$OUTDIR/summary.tsv"
printf "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n" \
  profile cpus mem sessions rep throughput p50_ms p95_ms valid > "$SUMMARY"

cleanup() { container stop "$NAME" >/dev/null 2>&1 || true; }
trap cleanup EXIT INT TERM

for profile in $PROFILES; do
  cpus="${profile%x*}"
  mem="${profile#*x}"
  echo
  echo "=============================================================="
  echo "==> profile: --cpus $cpus --memory $mem"
  echo "=============================================================="

  container stop "$NAME" >/dev/null 2>&1 || true
  sleep 1
  container run --rm -d --name "$NAME" \
    --cpus "$cpus" --memory "$mem" \
    -v "$STAGE:/bench" -p "${HOST_PORT}:8000" \
    -e SKY_LIVE_PORT=8000 \
    "$IMAGE" /bench/app >/dev/null

  echo "==> waiting for the app"
  ok=0
  for _ in $(seq 1 60); do
    if curl -sf -m 3 "http://127.0.0.1:$HOST_PORT/" -o /dev/null 2>/dev/null; then ok=1; break; fi
    sleep 1
  done
  if [ "$ok" != 1 ]; then
    echo "app never became reachable under $profile; logs:" >&2
    container logs "$NAME" 2>&1 | tail -20 >&2
    continue
  fi

  # Prove the client is really driving this container before measuring.
  if ! "$OUTDIR/skyliveload" -url "http://127.0.0.1:$HOST_PORT" -self-check \
        > "$OUTDIR/self-check-$profile.txt" 2>&1; then
    echo "SELF-CHECK FAILED under $profile -- skipping, numbers would be fiction" >&2
    cat "$OUTDIR/self-check-$profile.txt" >&2
    continue
  fi

  for n in $SESSIONS; do
    for rep in $(seq 1 "$REPEATS"); do
      echo "--> $n sessions, rep $rep/$REPEATS"
      json="$OUTDIR/load-$profile-n$n-r$rep.json"
      CONTAINER_FLAGS="--cpus $cpus --memory $mem" \
      "$OUTDIR/skyliveload" -url "http://127.0.0.1:$HOST_PORT" \
        -sessions "$n" -duration "$DURATION" -think "$THINK" -ramp 5s \
        -label "container:--cpus $cpus --memory $mem" -json "$json" || true

      [ -f "$json" ] && awk -v p="$profile" -v c="$cpus" -v m="$mem" -v n="$n" -v rep="$rep" '
        /"interactions_per_sec"/ { gsub(/[",]/,""); tp=$2 }
        /"p50_ms"/ { gsub(/[",]/,""); p50=$2 }
        /"p95_ms"/ { gsub(/[",]/,""); p95=$2 }
        /"valid"/  { gsub(/[",]/,""); v=$2 }
        END { printf "%s\t%s\t%s\t%s\t%s\t%.1f\t%.2f\t%.2f\t%s\n", p,c,m,n,rep,tp,p50,p95,v }
      ' "$json" >> "$SUMMARY"
    done
  done

  # Peak RSS as seen from inside the guest -- the host cannot see the
  # process's memory through the VM boundary.
  container exec "$NAME" sh -c \
    "grep VmRSS /proc/\$(pidof app)/status 2>/dev/null || true" \
    >> "$OUTDIR/rss-$profile.txt" 2>/dev/null || true

  container stop "$NAME" >/dev/null 2>&1 || true
done

echo
echo "==> constrained sweep"
column -t -s "$(printf '\t')" "$SUMMARY" 2>/dev/null || cat "$SUMMARY"
echo
print_caveats
echo "==> results in $OUTDIR"

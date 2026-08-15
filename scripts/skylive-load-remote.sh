#!/usr/bin/env bash
# Load a REMOTE Sky.Live app, and sample its RSS while doing it.
#
#   # preflight only -- the default, sends no load:
#   scripts/skylive-load-remote.sh --url http://34.x.x.x:8000
#
#   # the real thing, against a throwaway:
#   scripts/skylive-load-remote.sh --url http://34.x.x.x:8000 \
#       --project settleby --instance sky-lang-bench --load
#
# WHY THIS PAIRS LOAD WITH OBSERVATION
# ------------------------------------
# The number this whole exercise exists to validate is "~1.1 MB of server
# RSS per live session", and it cannot be had from either half alone:
#
#   * The app exposes NO session count and NO memory metric. Neither
#     `sky_live_sessions_active` nor any go_memstats_* is ever recorded --
#     the first is declared in the Prometheus help table and never written,
#     the second does not exist. So RSS has to come from /proc over SSH.
#   * A passive observer of a real site supplies RSS but no session
#     variance, because a small site simply has no concurrent sessions.
#
# Load from here plus /proc sampling on the box supplies both halves at
# once: a KNOWN session count on the x-axis, measured RSS on the y-axis.
# That is the only configuration that settles the figure on x86.
#
# SAFETY
# ------
# The default is preflight: identify the target, send nothing. Load needs
# --load. Production hosts are refused inside the binary itself
# (tools/skyliveload/guard.go), not just here, because a script guard is
# bypassed the moment someone runs the binary by hand. See guard_test.go.
#
# THE TARGET SHOULD BE A THROWAWAY
# --------------------------------
# Stand one up with the same tooling that deploys the real site:
#
#   cd /path/to/sky-lang.org
#   deploy/deploy.sh --project <id> --instance sky-lang-bench \
#       --zone us-central1-a --account deployer@settleby.iam.gserviceaccount.com
#
# and delete it when finished. This script deliberately does NOT create
# the instance: provisioning cloud resources costs money and is the
# operator's decision, not the harness's.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

URL="${URL:-}"
PROJECT="${SKYLANG_GCP_PROJECT:-}"
ACCOUNT="${SKYLANG_GCP_ACCOUNT:-}"
INSTANCE="${INSTANCE:-}"
ZONE="${ZONE:-us-central1-a}"
SERVICE="${SERVICE:-sky-lang-org}"
CONCURRENCY="${CONCURRENCY:-1 50 100 250 500}"
DURATION="${DURATION:-30s}"
THINK="${THINK:-1s}"
RAMP="${RAMP:-5s}"
REPEATS="${REPEATS:-3}"
LOAD=0
ASSUME_YES=0
OUTDIR="${OUTDIR:-}"

usage() { sed -n '2,45p' "$0" | sed 's/^# \{0,1\}//'; exit "${1:-0}"; }

while [ $# -gt 0 ]; do
    case "$1" in
        --url)         URL="$2";         shift 2 ;;
        --project)     PROJECT="$2";     shift 2 ;;
        --account)     ACCOUNT="$2";     shift 2 ;;
        --instance)    INSTANCE="$2";    shift 2 ;;
        --zone)        ZONE="$2";        shift 2 ;;
        --service)     SERVICE="$2";     shift 2 ;;
        --concurrency) CONCURRENCY="$2"; shift 2 ;;
        --duration)    DURATION="$2";    shift 2 ;;
        --think)       THINK="$2";       shift 2 ;;
        --repeats)     REPEATS="$2";     shift 2 ;;
        --load)        LOAD=1;           shift ;;
        --assume-yes)  ASSUME_YES=1;     shift ;;
        --out)         OUTDIR="$2";      shift 2 ;;
        -h|--help)     usage 0 ;;
        *) echo "unknown flag: $1" >&2; usage 64 ;;
    esac
done

[ -n "$URL" ] || { echo "ERROR: --url is required" >&2; usage 64; }

HOST="$(printf '%s' "$URL" | sed -E 's#^[a-z]+://##; s#[:/].*$##')"

STAMP="$(date +%Y%m%d-%H%M%S)"
[ -n "$OUTDIR" ] || OUTDIR="$ROOT/docs/perf/runs/remote-load-$STAMP"
mkdir -p "$OUTDIR"

# ---------------------------------------------------------------------
# Preflight: identify the target without loading it. /_sky/buildinfo and
# /_sky/healthz are unauthenticated by design and cost one request each.
# ---------------------------------------------------------------------
echo "==> preflight against $URL"
BUILDINFO="$(curl -s --max-time 10 "$URL/_sky/buildinfo" 2>/dev/null || true)"
HEALTH="$(curl -s --max-time 10 "$URL/_sky/healthz" 2>/dev/null || true)"
echo "    buildinfo  ${BUILDINFO:-<unreachable>}"
echo "    healthz    ${HEALTH:-<unreachable>}"
if [ -z "$BUILDINFO" ]; then
    echo "ERROR: $URL/_sky/buildinfo did not answer. Wrong URL, firewall, or app down." >&2
    exit 1
fi

{
    echo "timestamp        $(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "mode             $([ "$LOAD" = 1 ] && echo 'REMOTE LOAD' || echo 'preflight only (no load sent)')"
    echo "commit           $(git rev-parse HEAD 2>/dev/null || echo unknown)"
    echo "branch           $(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown)"
    echo "target_url       $URL"
    echo "target_host      $HOST"
    echo "target_buildinfo $BUILDINFO"
    echo "gcp_project      ${PROJECT:-<none>}"
    echo "gcp_instance     ${INSTANCE:-<none>}"
    echo "gcp_zone         $ZONE"
    echo "concurrency      $CONCURRENCY"
    echo "duration         $DURATION"
    echo "think            $THINK"
    echo "repeats          $REPEATS"
    echo "generator_host   $(hostname)"
    echo "generator_os     $(uname -srm)"
    echo "generator_cores  $(sysctl -n hw.ncpu 2>/dev/null || nproc)"
} | tee "$OUTDIR/env.txt"
echo

if [ "$LOAD" != 1 ]; then
    cat <<EOF
==> PREFLIGHT ONLY. No load was sent.

    The target answered and is identified above. To actually load it, add
    --load. Before you do, confirm the target is a throwaway: this sweep
    reaches ${CONCURRENCY##* } concurrent sessions, and the local runs put a
    1-CPU target at 4.2s p50 latency by 500 sessions.

    Re-run with:
      $0 --url $URL --load \\
          ${PROJECT:+--project $PROJECT }${INSTANCE:+--instance $INSTANCE }--zone $ZONE
EOF
    exit 0
fi

# ---------------------------------------------------------------------
# Load path
# ---------------------------------------------------------------------
BIN="$OUTDIR/skyliveload"

# Run the target guards' own tests before building the thing that can send
# traffic. tools/skyliveload is a standalone Go module and is NOT part of
# the cargo workspace, so nothing else in this repo compiles or exercises
# it on a normal change -- a broken guard would otherwise be discovered by
# a production outage rather than by a test. It costs about a second.
echo "==> verifying the target guards"
if ! ( cd tools/skyliveload && go test ./... ); then
    echo "ERROR: the target guards failed their own tests. Refusing to load" >&2
    echo "       anything until tools/skyliveload/guard_test.go is green." >&2
    exit 1
fi

echo "==> building generator"
( cd tools/skyliveload && go build -o "$BIN" . )

GENFLAGS=( -remote-load )
[ "$ASSUME_YES" = 1 ] && GENFLAGS+=( -assume-yes )

# RSS sampling on the target, concurrent with the load. Without this the
# run measures throughput only, and the memory question -- the one the
# sizing table actually got wrong -- goes unanswered again.
OBS_PID=""
if [ -n "$PROJECT" ] && [ -n "$INSTANCE" ]; then
    NLEVELS="$(printf '%s\n' $CONCURRENCY | wc -l | tr -d ' ')"
    # Cover the whole sweep: every level, every repeat, plus ramp and slack.
    OBS_SECS=$(( NLEVELS * REPEATS * ( ${DURATION%s} + 20 ) + 60 ))
    echo "==> starting RSS observer on $INSTANCE for ${OBS_SECS}s"
    INTERVAL=5 DURATION="$OBS_SECS" OUTDIR="$OUTDIR/observer" \
        "$ROOT/scripts/skylive-observe-remote.sh" \
            --project "$PROJECT" --instance "$INSTANCE" --zone "$ZONE" \
            --service "$SERVICE" ${ACCOUNT:+--account "$ACCOUNT"} \
            > "$OUTDIR/observer.log" 2>&1 &
    OBS_PID=$!
    sleep 10   # let it establish and record a pre-load baseline
else
    echo "==> NOTE: --project/--instance not given, so RSS will NOT be sampled."
    echo "    Throughput will be measured; per-session memory will not be."
fi

printf 'level\trepeat\tthroughput\tp50_ms\tp95_ms\tp99_ms\terr_rate\tvalid\n' > "$OUTDIR/summary.tsv"

for N in $CONCURRENCY; do
    for R in $(seq 1 "$REPEATS"); do
        echo "==> n=$N repeat=$R"
        J="$OUTDIR/load-n${N}-r${R}.json"
        set +e
        "$BIN" "${GENFLAGS[@]}" \
            -url "$URL" -sessions "$N" -duration "$DURATION" \
            -think "$THINK" -ramp "$RAMP" -json "$J" \
            -label "remote n=$N r=$R" 2>&1 | tee "$OUTDIR/load-n${N}-r${R}.log"
        RC=${PIPESTATUS[0]}
        set -e
        if [ "$RC" = 3 ]; then
            echo "ERROR: the generator refused this target. Nothing was sent." >&2
            [ -n "$OBS_PID" ] && kill "$OBS_PID" 2>/dev/null || true
            exit 3
        fi
        if [ -f "$J" ]; then
            # Field names must track Result's json tags in
            # tools/skyliveload/main.go -- they are snake_case there.
            awk -v n="$N" -v r="$R" '
                /"interactions_per_sec"/ { gsub(/[",]/,""); tp=$2 }
                /"p50_ms"/               { gsub(/[",]/,""); p50=$2 }
                /"p95_ms"/               { gsub(/[",]/,""); p95=$2 }
                /"p99_ms"/               { gsub(/[",]/,""); p99=$2 }
                /"error_rate"/           { gsub(/[",]/,""); er=$2 }
                /"valid"/                { gsub(/[",]/,""); v=$2 }
                END { printf "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", n, r, tp, p50, p95, p99, er, v }
            ' "$J" >> "$OUTDIR/summary.tsv"
        fi
        # Let sessions drain so the next level starts from a known floor --
        # otherwise the RSS attributed to level N+1 includes level N's
        # not-yet-reaped memory-store sessions.
        sleep 15
    done
done

if [ -n "$OBS_PID" ]; then
    echo "==> waiting for the RSS observer to finish its window"
    wait "$OBS_PID" 2>/dev/null || true
    echo
    [ -f "$OUTDIR/observer/summary.txt" ] && command cat "$OUTDIR/observer/summary.txt"
fi

echo
echo "==> summary"
command cat "$OUTDIR/summary.tsv"
echo
echo "==> wrote $OUTDIR"
echo
cat <<'EOF'
To get per-session memory out of this run, join summary.tsv against
observer/derived.tsv on the timestamp: each load level holds a KNOWN
session count, so RSS during that level, minus the idle baseline the
observer recorded before the sweep started, divided by the level, is the
per-session cost on this hardware. Quote it only with the view size --
the local 1.1 MB/session figure was measured holding a 384-node view,
and a lighter view will be cheaper.
EOF

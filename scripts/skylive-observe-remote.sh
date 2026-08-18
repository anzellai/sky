#!/usr/bin/env bash
# Passive observation of a REMOTE Sky.Live app. Read-only, by construction.
#
#   scripts/skylive-observe-remote.sh --project settleby --instance sky-lang-org
#   INTERVAL=15 DURATION=1800 scripts/skylive-observe-remote.sh --project settleby
#
# WHY THIS EXISTS
# ---------------
# Every constrained number in docs/perf/skylive-interaction-cost.md was taken
# on an ARM64 Linux VM on Apple silicon, under an integer CPU allocation.
# Apple's `container` rejects fractional `--cpus`, so the e2-micro (0.25) and
# e2-small (0.5) baselines could not be reproduced at all. Those figures are
# explicitly NOT publishable as GCP numbers. This script reads a real x86
# GCP instance instead.
#
# It samples, over time, and writes one TSV row per tick:
#
#   * RSS of the app process        -- from /proc/<pid>/status, over SSH
#   * concurrent connections        -- app upstream (:8000) and public (:443)
#   * CPU of the process and host   -- from /proc/<pid>/stat and /proc/stat
#   * cumulative request + Msg counts from /_sky/metrics
#
# WHAT IT DOES NOT DO
# -------------------
# It never sends a request to the app from here, never establishes a session,
# and never writes anything on the target. The only traffic it causes is the
# SSH transport itself and one localhost metrics scrape per tick, which the
# Ops Agent is already doing every 30s anyway. Applying load is a different
# script (scripts/skylive-load-remote.sh) with its own guards.
#
# THE MEASUREMENT THIS IS FOR, AND ITS DIVISOR PROBLEM
# ----------------------------------------------------
# The headline it is meant to test is "~1.1 MB of server RSS per live
# session", measured locally on ARM. Testing that needs RSS (easy) divided by
# a live session count (hard):
#
#   * `sky_live_sessions_active` is declared in the Prometheus help table
#     (runtime-go/rt/telemetry/prometheus.go) but NOTHING EVER RECORDS IT.
#     There is no Count()/Len() on the SessionStore interface, and
#     memoryStore.sessions is an unexported map with no size accessor.
#   * No Go memory metric is exposed either -- no runtime.ReadMemStats, no
#     go_memstats_*, no pprof mount.
#
# So the session count has to be inferred from held SSE connections, and that
# inference is a LOWER BOUND, not an equality: with store="memory" a session
# outlives its SSE stream until the TTL sweep reaps it. Read `sse_est` as
# "sessions with a tab open right now", never as "sessions resident in RSS".
# A per-session figure derived from it is therefore an UPPER bound on cost.
# The summary refuses to print one at all unless the window saw real variance.
#
# An idle instance reporting low RSS is not evidence against 1.1 MB/session.
# That is why every row carries its traffic level and the summary states
# plainly whether the window contained enough activity to mean anything.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# `with_timeout <secs> <cmd...>` — the one time bound. See the header of
# scripts/lib/with-timeout.sh for what a bare `timeout` did when it went missing.
source "$ROOT/scripts/lib/with-timeout.sh"

PROJECT="${SKYLANG_GCP_PROJECT:-}"
ACCOUNT="${SKYLANG_GCP_ACCOUNT:-}"
INSTANCE="${INSTANCE:-sky-lang-org}"
ZONE="${ZONE:-us-central1-a}"
SERVICE="${SERVICE:-sky-lang-org}"
APP_PORT="${APP_PORT:-8000}"
INTERVAL="${INTERVAL:-15}"
DURATION="${DURATION:-900}"
OUTDIR="${OUTDIR:-}"

usage() {
    sed -n '2,40p' "$0" | sed 's/^# \{0,1\}//'
    exit "${1:-0}"
}

while [ $# -gt 0 ]; do
    case "$1" in
        --project)  PROJECT="$2";  shift 2 ;;
        --account)  ACCOUNT="$2";  shift 2 ;;
        --instance) INSTANCE="$2"; shift 2 ;;
        --zone)     ZONE="$2";     shift 2 ;;
        --service)  SERVICE="$2";  shift 2 ;;
        --port)     APP_PORT="$2"; shift 2 ;;
        --interval) INTERVAL="$2"; shift 2 ;;
        --duration) DURATION="$2"; shift 2 ;;
        --out)      OUTDIR="$2";   shift 2 ;;
        -h|--help)  usage 0 ;;
        *) echo "unknown flag: $1" >&2; usage 64 ;;
    esac
done

# The project is a required PARAMETER. Guessing it risks pointing a
# production-touching command at the wrong estate, and gcloud's active
# project on this workstation is routinely something unrelated.
if [ -z "$PROJECT" ]; then
    echo "ERROR: --project is required (or set SKYLANG_GCP_PROJECT)." >&2
    echo "       gcloud's active project is NOT used as a fallback on purpose." >&2
    exit 64
fi

GC=( --project "$PROJECT" --zone "$ZONE" )
[ -n "$ACCOUNT" ] && GC+=( --account "$ACCOUNT" )

STAMP="$(date +%Y%m%d-%H%M%S)"
[ -n "$OUTDIR" ] || OUTDIR="$ROOT/docs/perf/runs/observe-$STAMP"
mkdir -p "$OUTDIR"

TICKS=$(( DURATION / INTERVAL ))
[ "$TICKS" -ge 2 ] || { echo "ERROR: need DURATION >= 2*INTERVAL" >&2; exit 64; }

echo "==> passive observation (READ-ONLY) of $INSTANCE"
echo "    project   $PROJECT"
echo "    zone      $ZONE"
echo "    service   $SERVICE"
echo "    sampling  ${TICKS} ticks x ${INTERVAL}s = ${DURATION}s"
echo "    out       $OUTDIR"
echo

# ---------------------------------------------------------------------
# Conditions, written before any sample -- same rule as the local runs:
# a number may not travel without them.
# ---------------------------------------------------------------------
{
    echo "timestamp        $(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "mode             passive-remote (read-only)"
    echo "commit           $(git -C "$ROOT" rev-parse HEAD 2>/dev/null || echo unknown)"
    echo "branch           $(git -C "$ROOT" rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown)"
    echo "gcp_project      $PROJECT"
    echo "gcp_instance     $INSTANCE"
    echo "gcp_zone         $ZONE"
    echo "service          $SERVICE"
    echo "app_port         $APP_PORT"
    echo "interval_s       $INTERVAL"
    echo "duration_s       $DURATION"
    echo "observer_host    $(hostname)"
} | tee "$OUTDIR/env.txt"
echo

# ---------------------------------------------------------------------
# The remote sampler. Runs entirely on the target in ONE ssh session --
# re-dialling per tick would cost more than the thing being measured.
#
# The admin token is read on the box, used on the box, and never printed;
# it must not reach this workstation, this repo, or any artefact.
# ---------------------------------------------------------------------
# NOT `REMOTE_SCRIPT="$(cat <<REMOTE_EOF …)"`. bash 3.2 — the only bash stock
# macOS ships, and what `#!/usr/bin/env bash` resolves to the moment the nix
# shell supplying bash 5 is not on PATH — cannot parse a heredoc inside a
# command substitution inside double quotes when the heredoc body contains `"`,
# and this one is full of them. It failed the WHOLE FILE at parse time:
# `line 336: unexpected EOF while looking for matching '`, pointing 180 lines
# away from the cause. The quotes bought nothing — the right-hand side of an
# assignment is not word-split or globbed — so they are gone.
REMOTE_SCRIPT=$(cat <<REMOTE_EOF
set -u
SERVICE='$SERVICE'
PORT='$APP_PORT'
TICKS=$TICKS
INTERVAL=$INTERVAL

PID=\$(systemctl show "\$SERVICE" -p MainPID --value 2>/dev/null || echo 0)
if [ -z "\$PID" ] || [ "\$PID" = "0" ]; then
    echo "FATAL: \$SERVICE has no MainPID (not running?)" >&2
    exit 1
fi

TOKEN=\$(sudo grep -E '^SKY_ADMIN_TOKEN=' /opt/\$SERVICE/.env 2>/dev/null | head -1 | cut -d= -f2-)
[ -n "\$TOKEN" ] || TOKEN=\$(sudo cat /etc/google-cloud-ops-agent/sky-metrics-token 2>/dev/null || true)

CLK=\$(getconf CLK_TCK)
NCPU=\$(nproc)

echo "#meta pid=\$PID ncpu=\$NCPU clk_tck=\$CLK kernel=\$(uname -srm) memtotal_kb=\$(awk '/MemTotal/{print \$2}' /proc/meminfo)"
echo "#meta boot_epoch=\$(awk '/btime/{print \$2}' /proc/stat) now_epoch=\$(date +%s)"
if [ -n "\$TOKEN" ]; then echo "#meta metrics_auth=ok"; else echo "#meta metrics_auth=MISSING"; fi

# Column contract. Every row carries its own traffic level so that no RSS
# figure can be quoted without the activity it was taken under.
echo -e "ts\trss_kb\tvmsize_kb\tthreads\tconn_app\tconn_pub\tproc_jiffies\tcpu_total\tcpu_idle\tload1\treq_total\tmsg_total\tmem_avail_kb"

i=0
while [ \$i -lt \$TICKS ]; do
    TS=\$(date +%s)

    if [ ! -r /proc/\$PID/status ]; then
        echo "#warn pid \$PID vanished at \$TS -- service restarted?" >&2
        NEWPID=\$(systemctl show "\$SERVICE" -p MainPID --value 2>/dev/null || echo 0)
        if [ -n "\$NEWPID" ] && [ "\$NEWPID" != "0" ]; then
            echo "#meta pid_changed from=\$PID to=\$NEWPID at=\$TS"
            PID=\$NEWPID
        fi
    fi

    RSS=\$(awk '/^VmRSS:/{print \$2}' /proc/\$PID/status 2>/dev/null || echo -1)
    VSZ=\$(awk '/^VmSize:/{print \$2}' /proc/\$PID/status 2>/dev/null || echo -1)
    THR=\$(awk '/^Threads:/{print \$2}' /proc/\$PID/status 2>/dev/null || echo -1)

    # Upstream connections to the app. With Sky.Live each session holds one
    # persistent SSE stream, so this tracks "tabs open right now" -- see the
    # divisor caveat in the header.
    CONN_APP=\$(ss -tn state established "( sport = :\$PORT )" 2>/dev/null | tail -n +2 | wc -l | tr -d ' ')
    # Public side: Caddy's TLS connections. A separate "is anyone there" signal.
    CONN_PUB=\$(ss -tn state established "( sport = :443 )" 2>/dev/null | tail -n +2 | wc -l | tr -d ' ')

    PJ=\$(awk '{print \$14 + \$15}' /proc/\$PID/stat 2>/dev/null || echo -1)
    CPU_TOTAL=\$(awk '/^cpu /{t=0; for(k=2;k<=NF;k++) t+=\$k; print t}' /proc/stat)
    CPU_IDLE=\$(awk '/^cpu /{print \$5 + \$6}' /proc/stat)
    LOAD1=\$(awk '{print \$1}' /proc/loadavg)
    MEMAV=\$(awk '/MemAvailable/{print \$2}' /proc/meminfo)

    REQ=-1; MSG=-1
    if [ -n "\$TOKEN" ]; then
        M=\$(curl -s --max-time 5 -H "Authorization: Bearer \$TOKEN" "http://localhost:\$PORT/_sky/metrics" 2>/dev/null || true)
        if [ -n "\$M" ]; then
            REQ=\$(printf '%s' "\$M" | awk '/^sky_live_requests_total/{s+=\$2} END{printf "%d", s+0}')
            MSG=\$(printf '%s' "\$M" | awk '/^sky_live_msg_total/{s+=\$2} END{printf "%d", s+0}')
        fi
        unset M
    fi

    echo -e "\$TS\t\$RSS\t\$VSZ\t\$THR\t\$CONN_APP\t\$CONN_PUB\t\$PJ\t\$CPU_TOTAL\t\$CPU_IDLE\t\$LOAD1\t\$REQ\t\$MSG\t\$MEMAV"

    i=\$((i + 1))
    # The guard must not become the loop's -- and so the script's -- exit
    # status on the final tick, or every clean run reports a false failure.
    if [ \$i -lt \$TICKS ]; then sleep \$INTERVAL; fi
done
exit 0
REMOTE_EOF
)

RAW="$OUTDIR/samples.tsv"
# ssh can die mid-window (preemption, network). Keep whatever we collected
# rather than losing the whole run to `set -e`.
set +e
printf '%s\n' "$REMOTE_SCRIPT" \
    | with_timeout $(( DURATION + 180 )) gcloud compute ssh "$INSTANCE" "${GC[@]}" --command 'bash -s' \
    > "$RAW" 2> "$OUTDIR/ssh.err"
SSH_RC=$?
set -e

if [ ! -s "$RAW" ]; then
    echo "ERROR: no samples collected. ssh stderr:" >&2
    tail -20 "$OUTDIR/ssh.err" >&2
    exit 1
fi
[ "$SSH_RC" -ne 0 ] && echo "WARNING: ssh exited $SSH_RC -- partial window, see ssh.err" >&2

command grep '^#' "$RAW" > "$OUTDIR/meta.txt" 2>/dev/null || true
command grep -v '^#' "$RAW" > "$OUTDIR/samples.clean.tsv"

echo "==> samples: $(( $(wc -l < "$OUTDIR/samples.clean.tsv") - 1 ))"
command cat "$OUTDIR/meta.txt"
echo

# ---------------------------------------------------------------------
# Analysis. Deltas between consecutive ticks; then a verdict on whether
# the window is worth anything at all.
# ---------------------------------------------------------------------
awk -F'\t' -v OFS='\t' -v ncpu="$(command grep -o 'ncpu=[0-9]*' "$OUTDIR/meta.txt" | head -1 | cut -d= -f2)" '
NR==1 { print "ts","dt_s","rss_kb","rss_mb","conn_app","conn_pub","proc_cpu_pct","host_cpu_pct","load1","req_per_s","msg_per_s","mem_avail_mb"; next }
{
    ts=$1; rss=$2; conn=$5; connp=$6; pj=$7; ct=$8; ci=$9; l1=$10; req=$11; msg=$12; mav=$13
    if (prev_ts != "") {
        dt = ts - prev_ts
        if (dt <= 0) dt = 1
        # Process CPU as a percentage of ONE core (so >100% means multi-core).
        pcpu = (pj - prev_pj) * 100.0 / (dt * 100.0)
        dtot = ct - prev_ct; didle = ci - prev_ci
        hcpu = (dtot > 0) ? (dtot - didle) * 100.0 / dtot : 0
        rps = (req >= 0 && prev_req >= 0) ? (req - prev_req) / dt : -1
        mps = (msg >= 0 && prev_msg >= 0) ? (msg - prev_msg) / dt : -1
        printf "%s\t%d\t%d\t%.1f\t%d\t%d\t%.1f\t%.1f\t%s\t%.3f\t%.3f\t%.1f\n", \
               ts, dt, rss, rss/1024.0, conn, connp, pcpu, hcpu, l1, rps, mps, mav/1024.0
    }
    prev_ts=ts; prev_pj=pj; prev_ct=ct; prev_ci=ci; prev_req=req; prev_msg=msg
}
' "$OUTDIR/samples.clean.tsv" > "$OUTDIR/derived.tsv"

command cat "$OUTDIR/derived.tsv" | head -40
echo

# ---------------------------------------------------------------------
# The verdict. This is the part that stops an idle window being mistaken
# for a measurement.
# ---------------------------------------------------------------------
awk -F'\t' '
NR==1 { next }
{
    n++
    rss=$3; conn=$5; connp=$6; pcpu=$7; rps=$10; mps=$11
    if (n==1 || rss<rmin) rmin=rss
    if (n==1 || rss>rmax) rmax=rss
    rsum+=rss
    if (n==1 || conn>cmax) cmax=conn
    if (n==1 || conn<cmin) cmin=conn
    if (connp>pmax) pmax=connp
    if (pcpu>cpumax) cpumax=pcpu
    cpusum+=pcpu
    if (rps>0) { reqsum+=rps; reqn++ }
    if (rps>rpsmax) rpsmax=rps
    if (mps>0) msgsum+=mps
    # Accumulate a least-squares fit of RSS against concurrent connections.
    sx+=conn; sy+=rss; sxx+=conn*conn; sxy+=conn*rss
}
END {
    if (n==0) { print "NO SAMPLES"; exit 1 }
    printf "SAMPLES              %d\n", n
    printf "RSS min/mean/max     %.1f / %.1f / %.1f MB\n", rmin/1024, rsum/n/1024, rmax/1024
    printf "RSS range            %.1f MB\n", (rmax-rmin)/1024
    printf "conn_app min/max     %d / %d\n", cmin, cmax
    printf "conn_pub max         %d\n", pmax
    printf "proc CPU mean/max    %.1f%% / %.1f%% of one core\n", cpusum/n, cpumax
    printf "req/s mean/max       %.3f / %.3f\n", (reqn? reqsum/reqn : 0), rpsmax
    printf "msg/s mean           %.3f\n", msgsum/n
    print  ""
    print  "--- VERDICT ---------------------------------------------------"
    span = cmax - cmin
    if (span >= 5 && n >= 3) {
        den = n*sxx - sx*sx
        if (den != 0) {
            slope = (n*sxy - sx*sy) / den
            icpt  = (sy - slope*sx) / n
            printf "Session-count variance in window: %d connections.\n", span
            printf "Least-squares RSS vs conn_app: %.0f KB per connection,\n", slope
            printf "  intercept (base RSS at zero sessions) %.1f MB.\n", icpt/1024
            print  "TREAT AS AN UPPER BOUND: conn_app counts tabs open now, while"
            print  "  a memory-store session stays resident until the TTL sweep."
        }
    } else {
        printf "INSUFFICIENT ACTIVITY. Concurrent-connection span over the whole\n"
        printf "  window was %d (min %d, max %d).\n", span, cmin, cmax
        print  "A per-session memory figure CANNOT be derived from this window."
        print  "  The base RSS below is still valid; the per-session slope is not"
        print  "  measurable without sessions to vary. This is a statement about"
        print  "  the traffic, NOT evidence against the ~1.1 MB/session figure."
        printf "\nUSABLE RESULT: base RSS on this target = %.1f MB\n", rsum/n/1024
        print  "  (idle, x86_64 Linux, real GCP hardware)"
    }
    print  "---------------------------------------------------------------"
}
' "$OUTDIR/derived.tsv" | tee "$OUTDIR/summary.txt"

echo
echo "==> wrote $OUTDIR/{env.txt,meta.txt,samples.clean.tsv,derived.tsv,summary.txt}"

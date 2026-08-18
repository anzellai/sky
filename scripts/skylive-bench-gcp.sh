#!/usr/bin/env bash
# Throwaway GCP instances for Sky.Live benchmarking: create, list, destroy.
#
#   scripts/skylive-bench-gcp.sh up     --project <id> --name sky-bench-micro --machine-type e2-micro
#   scripts/skylive-bench-gcp.sh ip     --project <id> --name sky-bench-micro
#   scripts/skylive-bench-gcp.sh down   --project <id>            # ALL sky-bench-*
#   scripts/skylive-bench-gcp.sh verify --project <id>
#
# THE FAILURE THAT MATTERS
# ------------------------
# An orphaned instance bills forever, and the process that created it is
# exactly the process that cannot be trusted to clean it up -- an agent
# dies, a session ends, a command hangs. Teardown therefore does not
# depend on this script surviving. Three independent layers:
#
#   1. A HARD TTL SET AT CREATION. Every instance is created with
#      --max-run-duration and --instance-termination-action=DELETE, so
#      GCE deletes it even if nothing else here ever runs again. This is
#      the only layer that survives the death of everything else.
#   2. EXPLICIT TEARDOWN (`down`), run unconditionally including on the
#      failure path.
#   3. VERIFICATION (`verify`), which lists what actually survives and
#      exits non-zero if anything matching the prefix is still there.
#
# Boot disks are created with auto-delete, so layers 1 and 2 both take
# the disk with them; `verify` checks for orphaned disks anyway, because
# a disk left behind bills just as quietly as an instance.
#
# THE NAME PREFIX IS A SAFETY DEVICE, NOT A CONVENTION
# ----------------------------------------------------
# Every instance is named `sky-bench-*`, and this script REFUSES to
# create or delete anything that is not. Production instances reachable
# by the same credentials include `sky-lang-org` (the live site),
# `darraghstudio-vm`, `ringfence-cloud-1`, `settleby-caddy`,
# `sky-pro-user-*` and `skydeploy-cp-dev`. None of them can be named by
# this script even deliberately: the prefix check runs before every
# mutating call, so a typo or a wrong shell variable cannot reach them.

set -euo pipefail

BENCH_PREFIX="sky-bench-"
PROJECT="${SKYLANG_GCP_PROJECT:-}"
ACCOUNT="${SKYLANG_GCP_ACCOUNT:-}"
ZONE="${ZONE:-us-central1-a}"
NAME=""
MACHINE_TYPE="e2-micro"
# Generous but finite. Long enough for a full sweep, short enough that a
# forgotten instance costs pennies rather than a month of billing.
TTL="${TTL:-3h}"
DISK_SIZE="${DISK_SIZE:-20GB}"
OPS_AGENT=0

CMD="${1:-}"; shift || true

while [ $# -gt 0 ]; do
    case "$1" in
        --project)      PROJECT="$2";      shift 2 ;;
        --account)      ACCOUNT="$2";      shift 2 ;;
        --zone)         ZONE="$2";         shift 2 ;;
        --name)         NAME="$2";         shift 2 ;;
        --machine-type) MACHINE_TYPE="$2"; shift 2 ;;
        --ttl)          TTL="$2";          shift 2 ;;
        --ops-agent)    OPS_AGENT=1;       shift ;;
        *) echo "unknown flag: $1" >&2; exit 64 ;;
    esac
done

[ -n "$PROJECT" ] || { echo "ERROR: --project is required (never inferred from gcloud's active config)" >&2; exit 64; }

GC=( --project "$PROJECT" )
[ -n "$ACCOUNT" ] && GC+=( --account "$ACCOUNT" )

# The prefix check. Called before every create and every delete.
assert_bench_name() {
    local n="$1"
    case "$n" in
        "$BENCH_PREFIX"*) ;;
        *)
            printf 'REFUSING to act on %q -- name does not start with %q.\n' "$n" "$BENCH_PREFIX" >&2
            echo "  This script may only create or delete throwaway bench instances." >&2
            echo "  Production instances are deliberately unreachable from here." >&2
            exit 3 ;;
    esac
    # Reject anything that could confuse a shell or the API.
    if ! printf '%s' "$n" | command grep -qE '^[a-z][-a-z0-9]{0,61}[a-z0-9]$'; then
        printf 'REFUSING: %q is not a valid GCE instance name\n' "$n" >&2
        exit 3
    fi
}

case "$CMD" in

up)
    [ -n "$NAME" ] || { echo "ERROR: --name is required" >&2; exit 64; }
    assert_bench_name "$NAME"

    echo "==> creating $NAME ($MACHINE_TYPE, $ZONE, project $PROJECT)"
    echo "    TTL $TTL, termination action DELETE"

    # A startup script is NOT used to install anything heavy: the point
    # of the bench box is to be a known-minimal baseline. The Ops Agent
    # is opt-in via --ops-agent precisely because it costs ~87 MB on a
    # 970 MB instance and its presence or absence must be a stated
    # condition of every memory figure, never an accident.
    STARTUP='#!/bin/bash
set -e
mkdir -p /opt/skybench
echo ready > /opt/skybench/.provisioned
'
    if [ "$OPS_AGENT" = 1 ]; then
        STARTUP="$STARTUP"'
curl -sSO https://dl.google.com/cloudagents/add-google-cloud-ops-agent-repo.sh
bash add-google-cloud-ops-agent-repo.sh --also-install
echo ops-agent >> /opt/skybench/.provisioned
'
    fi

    gcloud compute instances create "$NAME" "${GC[@]}" \
        --zone "$ZONE" \
        --machine-type "$MACHINE_TYPE" \
        --image-family debian-12 --image-project debian-cloud \
        --boot-disk-size "$DISK_SIZE" \
        --boot-disk-type pd-balanced \
        --boot-disk-auto-delete \
        --network default \
        --max-run-duration "$TTL" \
        --instance-termination-action DELETE \
        --labels "purpose=skylive-bench,harness=perf,ops-agent=$OPS_AGENT" \
        --metadata startup-script="$STARTUP" \
        --quiet

    echo "==> waiting for sshd"
    for i in $(seq 1 40); do
        if gcloud compute ssh "$NAME" "${GC[@]}" --zone "$ZONE" \
                --command 'test -f /opt/skybench/.provisioned' >/dev/null 2>&1; then
            echo "    up after ${i} attempts"
            break
        fi
        sleep 10
    done

    gcloud compute instances describe "$NAME" "${GC[@]}" --zone "$ZONE" \
        --format='table(name,status,machineType.basename(),
                        networkInterfaces[0].networkIP,
                        scheduling.maxRunDuration.seconds,
                        scheduling.instanceTerminationAction)'
    ;;

ip)
    [ -n "$NAME" ] || { echo "ERROR: --name is required" >&2; exit 64; }
    gcloud compute instances describe "$NAME" "${GC[@]}" --zone "$ZONE" \
        --format='value(networkInterfaces[0].networkIP)'
    ;;

down)
    # With no --name, every sky-bench-* instance in the zone goes. The
    # prefix filter is applied by the API, and then asserted again
    # per-name before the delete call.
    if [ -n "$NAME" ]; then
        assert_bench_name "$NAME"
        TARGETS="$NAME"
    else
        TARGETS="$(gcloud compute instances list "${GC[@]}" \
            --filter="name~^${BENCH_PREFIX} AND zone:($ZONE)" \
            --format='value(name)' || true)"
    fi

    if [ -z "$TARGETS" ]; then
        echo "==> nothing to delete (no ${BENCH_PREFIX}* in $ZONE)"
    else
        for t in $TARGETS; do
            assert_bench_name "$t"
            echo "==> deleting $t"
            gcloud compute instances delete "$t" "${GC[@]}" --zone "$ZONE" --quiet || \
                echo "WARNING: delete of $t failed -- the TTL will still reap it" >&2
        done
    fi

    # Disks are auto-delete, but a failed instance-delete can strand one.
    ORPHANS="$(gcloud compute disks list "${GC[@]}" \
        --filter="name~^${BENCH_PREFIX} AND zone:($ZONE) AND -users:*" \
        --format='value(name)' 2>/dev/null || true)"
    for d in $ORPHANS; do
        assert_bench_name "$d"
        echo "==> deleting orphaned disk $d"
        gcloud compute disks delete "$d" "${GC[@]}" --zone "$ZONE" --quiet || true
    done
    ;;

verify)
    echo "==> instances matching ${BENCH_PREFIX}* in $PROJECT (all zones)"
    LEFT="$(gcloud compute instances list "${GC[@]}" \
        --filter="name~^${BENCH_PREFIX}" \
        --format='table(name,zone.basename(),machineType.basename(),status)' || true)"
    if [ -z "$LEFT" ]; then echo "    (none)"; else echo "$LEFT"; fi

    echo "==> disks matching ${BENCH_PREFIX}* in $PROJECT (all zones)"
    DLEFT="$(gcloud compute disks list "${GC[@]}" \
        --filter="name~^${BENCH_PREFIX}" \
        --format='table(name,zone.basename(),sizeGb,users.basename())' || true)"
    if [ -z "$DLEFT" ]; then echo "    (none)"; else echo "$DLEFT"; fi

    N="$(gcloud compute instances list "${GC[@]}" --filter="name~^${BENCH_PREFIX}" --format='value(name)' | wc -l | tr -d ' ')"
    D="$(gcloud compute disks list "${GC[@]}" --filter="name~^${BENCH_PREFIX}" --format='value(name)' | wc -l | tr -d ' ')"
    if [ "$N" = "0" ] && [ "$D" = "0" ]; then
        echo
        echo "VERIFIED CLEAN: no ${BENCH_PREFIX}* instances and no ${BENCH_PREFIX}* disks remain."
    else
        echo
        echo "NOT CLEAN: $N instance(s), $D disk(s) still present." >&2
        exit 1
    fi
    ;;

*)
    sed -n '2,40p' "$0" | sed 's/^# \{0,1\}//'
    exit 64 ;;
esac

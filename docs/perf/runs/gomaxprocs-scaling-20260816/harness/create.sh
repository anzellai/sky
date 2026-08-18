#!/usr/bin/env bash
# create.sh — the two throwaway boxes for the GOMAXPROCS scaling sweep.
#
# --max-run-duration + --instance-termination-action=DELETE is NON-NEGOTIABLE
# and is set AT CREATION, not added afterwards: an unattended expiry is the
# only guarantee that survives the driver being killed mid-sweep.
#
# e2-standard-8 is chosen over any shared-core type deliberately. Its 8 vCPUs
# are DEDICATED, so there are no burst credits to exhaust and no
# scheduling-credit confound between the GOMAXPROCS arms — which is the whole
# point of doing this on one box.
set -u
# The repo this file is committed in, not the worktree it was measured from: an
# archived harness has to read as wired wherever it is checked out, and this
# line named a sibling worktree that exists on exactly one machine. Gated by
# xtask's `every_lib_source_line_names_a_file_that_exists`.
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../../.." && pwd)"
source "$REPO_ROOT/scripts/lib/with-timeout.sh"
GP=(--project settleby --zone us-central1-a)

create_one() {
  with_timeout 500 gcloud compute instances create "$1" "${GP[@]}" \
    --machine-type=e2-standard-8 \
    --image-family=debian-12 --image-project=debian-cloud \
    --boot-disk-size=20GB --boot-disk-type=pd-balanced \
    --max-run-duration=4h --instance-termination-action=DELETE \
    --no-restart-on-failure \
    --format="value(name,status)" 2>&1 | tail -3
}
create_one skygmp-app & create_one skygmp-gen & wait
echo "=== CREATE DONE ==="
gcloud compute instances list "${GP[@]::2}" --filter="name~skygmp" \
  --format="table(name,machineType.basename(),status,scheduling.maxRunDuration.maxRunDuration,scheduling.instanceTerminationAction,networkInterfaces[0].networkIP)"

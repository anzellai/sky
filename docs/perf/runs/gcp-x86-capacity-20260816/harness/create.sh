#!/usr/bin/env bash
set -u
# The repo this file is committed in, not the worktree it was measured from: an
# archived harness has to read as wired wherever it is checked out, and this
# line named a sibling worktree that exists on exactly one machine. Gated by
# xtask's `every_lib_source_line_names_a_file_that_exists`.
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../../.." && pwd)"
source "$REPO_ROOT/scripts/lib/with-timeout.sh"
create_one() {
  local n="$1" m="$2"
  with_timeout 400 gcloud compute instances create "$n" \
    --project settleby --zone us-central1-a \
    --machine-type="$m" \
    --image-family=debian-12 --image-project=debian-cloud \
    --boot-disk-size=20GB --boot-disk-type=pd-balanced \
    --max-run-duration=5h --instance-termination-action=DELETE \
    --no-restart-on-failure \
    --format="value(name,status)" 2>&1 | tail -3
}
create_one skyperf-gen    e2-standard-4 &
create_one skyperf-small  e2-small &
create_one skyperf-medium e2-medium &
wait
echo "=== CREATE DONE ==="
gcloud compute instances list --project settleby --filter="name~skyperf" \
  --format="table(name,machineType.basename(),status,scheduling.maxRunDuration,scheduling.instanceTerminationAction,networkInterfaces[0].accessConfigs[0].natIP)"

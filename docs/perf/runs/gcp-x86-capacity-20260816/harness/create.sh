#!/usr/bin/env bash
set -u
source /Users/anzel/works/playground/sky-bench-x86/scripts/lib/with-timeout.sh
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

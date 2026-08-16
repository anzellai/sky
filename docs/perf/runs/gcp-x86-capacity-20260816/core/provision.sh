#!/usr/bin/env bash
# provision.sh — push the two prebuilt linux/amd64 binaries + the scripts to
# skyperf-core ONLY. Never touches skyperf-small / -medium / -gen: a capacity
# sweep is live on those.
set -u
BASE=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/x86bench
CORE="$BASE/core"
source /Users/anzel/works/playground/sky-bench-x86/scripts/lib/with-timeout.sh
GP=(--project settleby --zone us-central1-a)

gzip -kf "$BASE/bin/forumbench-linux-amd64"
gzip -kf "$BASE/bin/skyliveload-linux-amd64"

with_timeout 900 gcloud compute scp \
  "$BASE/bin/forumbench-linux-amd64.gz" \
  "$BASE/bin/skyliveload-linux-amd64.gz" \
  /Users/anzel/works/playground/sky/docs/perf/runs/forum-rebaseline-20260816/harness/forum-setup.json \
  "$CORE/remote_app.sh" "$CORE/remote_run.sh" \
  skyperf-core:/tmp/ "${GP[@]}" || exit 1

with_timeout 600 gcloud compute ssh skyperf-core "${GP[@]}" --command '
  set -e
  sudo mkdir -p /opt/skybench /opt/gen
  gunzip -f /tmp/forumbench-linux-amd64.gz
  gunzip -f /tmp/skyliveload-linux-amd64.gz
  sudo install -m 0755 /tmp/forumbench-linux-amd64 /opt/skybench/app
  sudo install -m 0755 /tmp/skyliveload-linux-amd64 /opt/gen/skyliveload
  sudo install -m 0644 /tmp/forum-setup.json /opt/gen/forum-setup.json
  chmod +x /tmp/remote_app.sh /tmp/remote_run.sh
  mkdir -p /tmp/out
  sha256sum /opt/skybench/app /opt/gen/skyliveload
  nproc; grep -m1 "model name" /proc/cpuinfo; getconf CLK_TCK
  echo CORE_READY' 2>&1 | tail -12

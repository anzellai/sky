#!/usr/bin/env bash
set -u
BASE=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/x86bench
source /Users/anzel/works/playground/sky-bench-x86/scripts/lib/with-timeout.sh
GP="--project settleby --zone us-central1-a"

cat > "$BASE/ips.txt" <<'EOF'
skyperf-gen 10.128.0.6
skyperf-small 10.128.0.7
skyperf-medium 10.128.0.8
EOF

gz() { gzip -kf "$1"; }
gz "$BASE/bin/forumbench-linux-amd64"
gz "$BASE/bin/skyliveload-linux-amd64"

push_target() {
  local n="$1"
  with_timeout 900 gcloud compute scp "$BASE/bin/forumbench-linux-amd64.gz" \
      "$BASE/harness/remote_app.sh" "$BASE/harness/remote_sampler.sh" \
      "$n":/tmp/ --project settleby --zone us-central1-a >/dev/null || return 1
  with_timeout 300 gcloud compute ssh "$n" --project settleby --zone us-central1-a --command \
    'set -e
     gunzip -f /tmp/forumbench-linux-amd64.gz
     sudo install -o skybench -g skybench -m 0755 /tmp/forumbench-linux-amd64 /opt/skybench/app
     chmod +x /tmp/remote_app.sh /tmp/remote_sampler.sh
     /opt/skybench/app --version 2>/dev/null | head -1 || true
     ls -la /opt/skybench/app
     echo TARGET_READY' 2>&1 | tail -4
}

push_gen() {
  with_timeout 900 gcloud compute scp "$BASE/bin/skyliveload-linux-amd64.gz" \
      /Users/anzel/works/playground/sky/docs/perf/runs/forum-rebaseline-20260816/harness/forum-setup.json \
      skyperf-gen:/tmp/ --project settleby --zone us-central1-a >/dev/null || return 1
  with_timeout 300 gcloud compute ssh skyperf-gen --project settleby --zone us-central1-a --command \
    'set -e
     sudo mkdir -p /opt/gen
     gunzip -f /tmp/skyliveload-linux-amd64.gz
     sudo install -m 0755 /tmp/skyliveload-linux-amd64 /opt/gen/skyliveload
     sudo install -m 0644 /tmp/forum-setup.json /opt/gen/forum-setup.json
     ls -la /opt/gen/
     echo GEN_READY' 2>&1 | tail -5
}

push_target skyperf-small &
P1=$!
push_target skyperf-medium &
P2=$!
push_gen &
P3=$!
wait $P1; echo "small rc=$?"
wait $P2; echo "medium rc=$?"
wait $P3; echo "gen rc=$?"
echo "=== DEPLOY DONE ==="

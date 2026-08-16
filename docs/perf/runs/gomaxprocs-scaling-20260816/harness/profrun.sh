#!/usr/bin/env bash
# profrun.sh — one load run against the INSTRUMENTED binary, with mutex, block
# and CPU profiles taken across the measurement window.
#
# The instrumented binary is run at the SAME GOMAXPROCS as a plain run so the
# instrument's own cost is measured (throughput here vs the plain arm) rather
# than assumed away.
#
# usage: profrun.sh <gomaxprocs> <sessions> <tag-suffix>
set -u
S=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/gmp
# The repo this file is committed in, not the worktree it was measured from: an
# archived harness has to read as wired wherever it is checked out, and this
# line named a sibling worktree that exists on exactly one machine. Gated by
# xtask's `every_lib_source_line_names_a_file_that_exists`.
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../../.." && pwd)"
source "$REPO_ROOT/scripts/lib/with-timeout.sh"
GP=(--project settleby --zone us-central1-a)
APPIP=10.128.0.11
GMP="$1"; N="$2"; SUF="$3"
DUR=60; RAMP=15; WARM=8
TAG="prof-g${GMP}-n${N}${SUF}"
OUT="$S/out/$TAG"; mkdir -p "$OUT"

echo "=== $TAG ==="
IDLE=$(with_timeout 300 gcloud compute ssh skygmp-app "${GP[@]}" \
  --command "FORUM_POSTS=5 MUTEX_FRACTION=1 BLOCK_RATE=100000 bash /tmp/remote_app.sh $GMP prof" 2>/dev/null | grep -E '^(IDLE|FAIL)')
echo "  $IDLE"
case "$IDLE" in IDLE*) ;; *) echo "REJECTED"; exit 65;; esac

with_timeout 90 gcloud compute ssh skygmp-app "${GP[@]}" \
  --command "setsid bash /tmp/remote_sampler.sh 150 /tmp/s.tsv app-prof </dev/null >/dev/null 2>&1 & \
             setsid bash /tmp/remote_prof.sh 30 30 $TAG </dev/null >/tmp/prof.log 2>&1 & echo started" >/dev/null 2>&1
sleep 2
with_timeout 900 gcloud compute ssh skygmp-gen "${GP[@]}" \
  --command "bash /tmp/remote_gen.sh $APPIP $TAG $N $DUR $RAMP $WARM" >| "$OUT/run.txt" 2>&1
sed -n '/^{/,$p' "$OUT/run.txt" >| "$OUT/load.json"
grep -o '"interactions_per_sec": *[0-9.]*' "$OUT/load.json" | head -1

sleep 8
with_timeout 300 gcloud compute ssh skygmp-app "${GP[@]}" --command "cd /tmp/prof && tar czf /tmp/$TAG.tgz $TAG && ls -la /tmp/$TAG.tgz" 2>&1 | tail -2
with_timeout 300 gcloud compute scp "skygmp-app:/tmp/$TAG.tgz" "$OUT/" "${GP[@]}" >/dev/null 2>&1
with_timeout 200 gcloud compute scp skygmp-app:/tmp/s.tsv "$OUT/sample.tsv" "${GP[@]}" >/dev/null 2>&1
with_timeout 200 gcloud compute scp skygmp-app:/tmp/app.log "$OUT/app.log" "${GP[@]}" >/dev/null 2>&1
( cd "$OUT" && tar xzf "$TAG.tgz" && mv -f "$TAG"/* . && rmdir "$TAG" ) 2>/dev/null
ls -la "$OUT" | head -20

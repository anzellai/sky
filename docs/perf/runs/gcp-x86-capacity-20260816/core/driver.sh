#!/usr/bin/env bash
# driver.sh — 3 repeats of the one configuration, app restarted between each.
# usage: driver.sh <first_rep> <last_rep>
set -u
BASE=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/x86bench
CORE="$BASE/core"
source /Users/anzel/works/playground/sky-bench-x86/scripts/lib/with-timeout.sh
GP=(--project settleby --zone us-central1-a)
FIRST="${1:-1}"; LAST="${2:-3}"

for r in $(seq "$FIRST" "$LAST"); do
  TAG="core-mem-p5-n25-r$r"
  OUT="$CORE/$TAG"
  mkdir -p "$OUT"
  echo "=== $TAG ==="

  IDLE=$(with_timeout 400 gcloud compute ssh skyperf-core "${GP[@]}" \
          --command "bash /tmp/remote_app.sh 5" 2>/dev/null | grep -E '^(IDLE|FAIL)')
  printf '%s\n' "$IDLE" >| "$OUT/idle.txt"
  case "$IDLE" in
    IDLE*) echo "  $IDLE" ;;
    *) echo "  REJECTED — app setup: ${IDLE:-no output}"; printf 'app setup failed: %s\n' "$IDLE" >| "$OUT/REJECTED"; continue ;;
  esac

  RC=0
  with_timeout 700 gcloud compute ssh skyperf-core "${GP[@]}" \
     --command "bash /tmp/remote_run.sh $TAG" >| "$OUT/run.txt" 2>&1 || RC=$?
  if grep -q '^REJECT' "$OUT/run.txt"; then
    echo "  REJECTED — self-check"; cp -f "$OUT/run.txt" "$OUT/REJECTED"; continue
  fi
  if [ "$RC" -ne 0 ]; then
    echo "  ssh/driver rc=$RC"; tail -5 "$OUT/run.txt"
  fi

  with_timeout 300 gcloud compute scp \
     "skyperf-core:/tmp/out/$TAG/load.json" \
     "skyperf-core:/tmp/out/$TAG/sample.tsv" \
     "skyperf-core:/tmp/out/$TAG/selfcheck.txt" \
     "skyperf-core:/tmp/out/$TAG/gen.txt" \
     "$OUT/" "${GP[@]}" >/dev/null 2>&1
  with_timeout 300 gcloud compute scp "skyperf-core:/tmp/app.log" "$OUT/app.log" "${GP[@]}" >/dev/null 2>&1

  grep -oE '"(interactions_per_sec|p50_ms|p95_ms|p99_ms|error_rate|patch_rate|valid|interactions_counted|generator_cpu_percent_of_machine|generator_possibly_saturated|patches_naming_absent_ids)": *[^,}]*' \
      "$OUT/load.json" 2>/dev/null | tr '\n' ' '
  echo
done

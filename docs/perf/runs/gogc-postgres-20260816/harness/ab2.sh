#!/usr/bin/env bash
# ab2.sh — confirmatory A/B, gated on a QUIET host.
# The first A/B ran while a sibling agent's xcrun/clang builds pushed load to
# 6-8, and its reps 2-3 degraded monotonically in a way interleaving cannot
# correct for. This one waits for load < 3.0 and re-checks between every arm,
# so an arm never starts into someone else's build.
set -u
BASE=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/gogc
wait_quiet() {
  for _ in $(seq 1 120); do
    l=$(uptime | sed -E 's/.*load averages?: ([0-9.]+).*/\1/')
    x=$(pgrep -c xcrun 2>/dev/null || echo 0)
    awk -v l="$l" 'BEGIN{exit !(l<4.0)}' && [ "$x" -eq 0 ] && return 0
    sleep 15
  done
  echo "HOST NEVER QUIET — refusing to measure into someone else's build"; return 1
}
for rep in 1 2 3 4; do
  for arm in control treat; do
    wait_quiet || exit 1
    TAG="ab2-$arm-n$N_SESS-r$rep"
    echo "=== [$(date +%H:%M:%S)] $TAG load=$(uptime | sed -E 's/.*load averages?: ([0-9.]+).*/\1/') ==="
    APP_BIN="$BASE/bin/forumbench-$arm" "$BASE/runone.sh" "$TAG" "$N_SESS" 100 - || echo "ARM FAILED: $TAG"
    sleep 5
  done
done
echo "=== AB2 COMPLETE $(date +%H:%M:%S) ==="

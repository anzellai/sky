#!/usr/bin/env bash
# sweep-gml.sh — the GOMEMLIMIT arm.
#
# The question GOGC cannot answer: GOGC is a MULTIPLIER on live heap, so the
# memory it permits scales with the session count and the operator cannot bound
# it. GOMEMLIMIT is a soft limit in bytes — the collector runs as rarely as that
# bound allows and no rarer — which is the shape a fixed-size instance actually
# wants.
#
# Two shapes are measured, because they are NOT the same policy:
#   * GOGC=off + GOMEMLIMIT=X — the limit is the ONLY trigger. Maximum
#     throughput for the budget, but a live-heap spike has no multiplier
#     backstop, so the collector thrashes rather than exceeding X.
#   * GOGC=100 + GOMEMLIMIT=X — the multiplier paces normal operation and the
#     limit is a ceiling. This is the configuration the Go runtime docs
#     recommend for a container with a known memory allowance.
#
# Budgets are chosen from real instance sizes rather than round numbers:
# an e2-small is 1.93 GiB and an e2-medium 3.83 GiB, and the app is not alone on
# the box — PostgreSQL and the OS want their share.
set -u
BASE=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/gogc

# tag-suffix : sessions : gogc : gomemlimit
ARMS=(
  "n500:500:off:750MiB"     # e2-small budget: 1.93 GiB - OS - postgres
  "n500:500:100:750MiB"
  "n500:500:off:1500MiB"    # e2-medium budget
  "n500:500:100:1500MiB"
  "n100:100:off:750MiB"
  "n100:100:100:750MiB"
)

for a in "${ARMS[@]}"; do
  IFS=: read -r sfx N G L <<< "$a"
  TAG="gml-${sfx}-gogc${G}-${L}"
  echo "=== [$(date +%H:%M:%S)] $TAG ==="
  "$BASE/runone.sh" "$TAG" "$N" "$G" "$L" || echo "ARM FAILED: $TAG"
  sleep 6
done
echo "=== GML SWEEP COMPLETE $(date +%H:%M:%S) ==="

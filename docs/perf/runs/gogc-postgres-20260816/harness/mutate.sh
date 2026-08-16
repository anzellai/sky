#!/usr/bin/env bash
# mutate.sh — prove each gate can fail.
#
# The discipline the mandate demands, and the reason for it: an aliased `cp`, a
# perl substitution that matched nothing, and a stale log served by `noclobber`
# have each produced a false verdict on this host today. So every step here is
# CONFIRMED BY GREP against the file on disk, in both directions:
#
#   1. assert the pristine text is present   (else the substitution is a no-op)
#   2. apply the mutation
#   3. assert the mutated text is present AND the pristine text is gone
#   4. run the gate; a PASS here is a FAILED PROOF
#   5. revert from git and assert the pristine text is back
#
# A gate that stays green under its own falsifying mutation is not a gate.
set -u
RT=/Users/anzel/works/playground/sky-perf-gogc/runtime-go/rt
cd /Users/anzel/works/playground/sky-perf-gogc/runtime-go || exit 1
source ../scripts/lib/with-timeout.sh
PASS=0; FAIL=0

# mutate <file> <pristine-regex> <mutated-regex> <perl-expr> <test-regex> <pkg>
mutate() {
  local f="$1" pre="$2" post="$3" expr="$4" tests="$5" pkg="$6"
  echo "─── mutation: $f  [$tests]"

  grep -qE "$pre" "$f" || { echo "  PROOF INVALID: pristine text absent before mutation"; FAIL=$((FAIL+1)); return; }
  perl -0pi -e "$expr" "$f"
  if ! grep -qE "$post" "$f"; then
    echo "  PROOF INVALID: mutation NOT present after perl — substitution matched nothing"
    git checkout -- "$f"; FAIL=$((FAIL+1)); return
  fi
  if grep -qE "$pre" "$f"; then
    echo "  PROOF INVALID: pristine text still present — perl edited a different site"
    git checkout -- "$f"; FAIL=$((FAIL+1)); return
  fi
  echo "  mutation confirmed present on disk"

  if with_timeout 600 env CGO_ENABLED=1 go test -count=1 -run "$tests" "$pkg" >/tmp/mut.out 2>&1; then
    echo "  ✗ GATE DID NOT GO RED — it does not test what it claims"
    grep -E "^(ok|FAIL|---)" /tmp/mut.out | head -3
    FAIL=$((FAIL+1))
  else
    echo "  ✓ gate went RED under its falsifying mutation"
    grep -E "^\s+--- FAIL|^--- FAIL" /tmp/mut.out | head -3
    PASS=$((PASS+1))
  fi

  git checkout -- "$f"
  grep -qE "$pre" "$f" || { echo "  WARNING: revert did not restore pristine text"; FAIL=$((FAIL+1)); }
}

mutate "$RT/live.go" \
  'sessionLockerShards = 64' 'sessionLockerShards = 1' \
  's/sessionLockerShards = 64/sessionLockerShards = 1/' \
  'TestSessionLocker_ConcurrentDistinctSidsDoNotShareAShard' ./rt/

mutate "$RT/goid_shard_map.go" \
  'goidShards = 64' 'goidShards = 1' \
  's/goidShards = 64/goidShards = 1/' \
  'TestGoidShardedMap_ConcurrentGoroutinesDoNotShareAShard' ./rt/

mutate "$RT/live_store.go" \
  'entry == sess && !sess\.evicted\.Load\(\)' 'entry == sess$' \
  's/entry == sess && !sess\.evicted\.Load\(\)/entry == sess/' \
  'TestMemCacheAlreadyHolds_Truth|TestSetFastPath_NeverResurrectsEvictedCorpse' ./rt/

mutate "$RT/telemetry/otel.go" \
  'c != nil && c\.provider == tp' 'c != nil \{' \
  's/if c := tracerCache\.Load\(\); c != nil && c\.provider == tp \{/if c := tracerCache.Load(); c != nil {/' \
  'TestTracerCache_SwapIsPickedUp' ./rt/telemetry/

echo "════ mutation proofs: $PASS proven red, $FAIL invalid/unproven ════"
[ "$FAIL" -eq 0 ]

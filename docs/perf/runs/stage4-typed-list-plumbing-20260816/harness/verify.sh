#!/usr/bin/env bash
# verify.sh — the Stage 4 verification sweep, SEQUENTIAL and time-bounded.
#
# Every leg is bounded through scripts/lib/with-timeout.sh, never a bare
# `timeout` (absent on stock macOS; `rust/crates/xtask/tests/
# scripts_bound_time_portably.rs` fails the build on one). Nothing is piped
# through `tail`, which would take the pipe's exit status and report a hung or
# missing command as success.
#
# Legs run one at a time. Each writes its own log and its own PASS/FAIL line to
# the summary; the script does NOT stop at the first red, because a partial
# sweep whose remaining legs were never run is indistinguishable from a sweep
# that passed them.
set -uo pipefail
WT="${WT:-/Users/anzel/works/playground/sky-stage4}"
LOGS="${LOGS:-/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/stage4/verify}"
mkdir -p "$LOGS"
source "$WT/scripts/lib/with-timeout.sh"

SUMMARY="$LOGS/SUMMARY.txt"
: >| "$SUMMARY"

leg() {
  local name="$1" secs="$2"; shift 2
  echo "=== $name ===" >&2
  local t0 t1 rc
  t0=$(date +%s)
  ( cd "$WT" && with_timeout "$secs" "$@" ) >| "$LOGS/$name.log" 2>&1
  rc=$?
  t1=$(date +%s)
  if [ "$rc" -eq 0 ]; then
    printf "PASS  %-26s %5ss\n" "$name" "$((t1-t0))" | tee -a "$SUMMARY"
  else
    printf "FAIL  %-26s %5ss  (exit %s, see %s.log)\n" "$name" "$((t1-t0))" "$rc" "$name" | tee -a "$SUMMARY"
  fi
}

leg cargo-test-workspace 3600 cargo test --workspace --manifest-path rust/Cargo.toml
leg go-race-rt           2400 env CGO_ENABLED=1 go test -C runtime-go -race -timeout 2000s -count=1 ./rt/...
leg xtask-roundtrip       900 cargo run --release -p xtask --manifest-path rust/Cargo.toml -- roundtrip
leg xtask-infer           900 cargo run --release -p xtask --manifest-path rust/Cargo.toml -- infer
leg xtask-resolve         900 cargo run --release -p xtask --manifest-path rust/Cargo.toml -- resolve
leg xtask-repro          1200 cargo run --release -p xtask --manifest-path rust/Cargo.toml -- repro
leg xtask-coerce-floor   1800 cargo run --release -p xtask --manifest-path rust/Cargo.toml -- coerce-floor
leg xtask-build-run      3600 cargo run --release -p xtask --manifest-path rust/Cargo.toml -- build-run --golden
leg example-sweep        3600 bash scripts/example-sweep.sh
leg doc-examples         1800 bash scripts/doc-examples.sh
leg skyforum-e2e         1200 node scripts/playwright-live-verify.mjs 19-skyforum

echo; echo "===== SUMMARY ====="; cat "$SUMMARY"

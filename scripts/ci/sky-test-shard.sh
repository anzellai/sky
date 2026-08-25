#!/usr/bin/env bash
# Emit the `cargo test -p sky` TARGET flags for shard I of N.
#
# `test-sky` (`cargo test -p sky`) is the slowest setup-DEPENDENT job and the
# thing that now bounds the T1 tier. Its cost is the integration test binaries
# under `rust/crates/sky/tests/` — several spin up a real PostgreSQL — so it is
# sharded across sibling jobs the same way `build-corpus` shards the example
# corpus. This script is the partition: given `I N`, it prints the target flags
# for shard I, interleaved by SORTED binary name so the alphabetically-clustered
# `db_*_flow` tests spread evenly across shards. Shard 0 also carries the crate's
# `--lib` unit tests. Every binary lands in exactly one shard and the union is the
# whole set — proved by `rust/crates/xtask/tests/sky_test_shards_are_total.rs`, so
# a newly-added `tests/*.rs` is auto-assigned and can never be silently dropped.
#
# Usage:  cargo test -p sky --locked $(scripts/ci/sky-test-shard.sh 0 2)
#
# Bash 3.2 clean (no mapfile / associative arrays) — stock macOS runs the meta
# test that parses it, and `gates_measure_a_fresh_compiler` fails the build on a
# script bash 3.2 cannot parse.
set -euo pipefail

I="${1:?usage: sky-test-shard.sh <shard-index 0-based> <shard-count>}"
N="${2:?usage: sky-test-shard.sh <shard-index 0-based> <shard-count>}"

case "$I$N" in
  *[!0-9]*) echo "sky-test-shard: I and N must be integers (got $I/$N)" >&2; exit 2 ;;
esac
if [ "$N" -eq 0 ] || [ "$I" -ge "$N" ]; then
  echo "sky-test-shard: need 0 <= I < N (got $I/$N)" >&2
  exit 2
fi

root="$(cd "$(dirname "$0")/../.." && pwd)"

out=""
# The lib unit tests run once, in shard 0.
[ "$I" -eq 0 ] && out="--lib"

idx=0
# Process substitution keeps the counter in THIS shell (a plain pipe would run
# the loop in a subshell and lose it). Sorted for a deterministic partition.
while IFS= read -r name; do
  [ -n "$name" ] || continue
  if [ "$(( idx % N ))" -eq "$I" ]; then
    out="$out --test $name"
  fi
  idx=$(( idx + 1 ))
done < <(ls "$root"/rust/crates/sky/tests/*.rs 2>/dev/null | while IFS= read -r p; do basename "$p" .rs; done | sort)

# Trim the leading space; print with no trailing newline so `$(...)` word-splits
# cleanly into cargo args.
printf '%s' "${out# }"

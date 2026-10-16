#!/usr/bin/env bash
#
# Run one shard of a `go test` over packages, split by TEST, not by package.
#
# WHY THIS EXISTS
# ---------------
# `go test -race ./rt/...` over the Go runtime is one long run: 655-668 s in
# rust-ci `integration-postgres` and release `gate-race` (runs 38892647386,
# 38892664475), almost all of it in the one package `sky-app/rt`. A package
# split cannot shorten that, so this splits the TESTS: it lists every top-level
# Test/Example/Fuzz function of the packages with the same flags the run uses
# (`go test -list`, which compiles the same test binaries the run then reuses),
# weighs each (package, name) pair by its measured duration
# (go-test-durations.tsv) and deals them longest first to the least-loaded
# shard. Every shard derives its share from the same full list and the same
# weights, so the shards are disjoint and their union is the unsharded run, by
# construction. `--partition` exposes that rule on a list read from stdin, and
# rust/crates/xtask/tests/go_test_shards_are_disjoint_and_total.rs checks it.
#
# A shard runs each of its packages with `-run '^(A|B|...)$'`, so a parent test
# still runs every subtest, and TestMain still runs once per package. A shard
# that selects nothing FAILS: an empty shard means the list or the partition
# is wrong, and an empty run would report `ok`.
#
# USAGE
#   go-test-shard.sh K/N <package-pattern>... -- <go test flags>...
#   go-test-shard.sh --partition K/N   < list     (pure: "pkg<TAB>name" lines)
#
# The flags after `--` are passed to both the listing and the run, so the list
# is built under the same build tags and -race setting as the tests.
set -euo pipefail

usage() {
  echo "usage: $0 K/N <package-pattern>... -- <go test flags>..." >&2
  echo "       $0 --partition K/N < list" >&2
  exit 2
}

# parse_shard K/N -> sets SHARD_K SHARD_N, validated.
parse_shard() {
  case "$1" in
    */*) SHARD_K="${1%/*}"; SHARD_N="${1#*/}" ;;
    *) usage ;;
  esac
  case "$SHARD_K$SHARD_N" in *[!0-9]*|'') usage ;; esac
  if [ "$SHARD_N" -lt 1 ] || [ "$SHARD_K" -lt 1 ] || [ "$SHARD_K" -gt "$SHARD_N" ]; then
    echo "go-test-shard: shard $1 is out of range (want 1 <= K <= N)" >&2
    exit 2
  fi
}

sha256_16() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum; else shasum -a 256; fi | cut -c1-16
}

# The measured duration of each test, "pkg<TAB>name<TAB>seconds"
# (scripts/ci/go-test-durations.tsv). Only the BALANCE uses it: an unlisted or
# renamed test gets DEFAULT_WEIGHT and is still assigned, so a stale file can
# make the shards uneven but never drops or repeats a test.
DURATIONS="${GO_TEST_DURATIONS:-$(cd "$(dirname "$0")" && pwd)/go-test-durations.tsv}"
DEFAULT_WEIGHT=0.1

# partition: stdin "pkg<TAB>name" lines -> this shard's lines, sorted.
#
# Longest first: every test is weighed (its measured seconds, else
# DEFAULT_WEIGHT), the list is ordered by weight descending then by name, and
# each test goes to the shard with the least weight so far (the lowest index on
# a tie). Every shard computes the same assignment from the same list, so the
# shards are disjoint and their union is the list. Round-robin by name, the
# first rule here, split the race run 120 s / 479 s (rust-ci run 38905775007).
partition() {
  local weights=/dev/null
  [ -f "$DURATIONS" ] && weights="$DURATIONS"
  LC_ALL=C sort -u | awk -F '\t' -v OFS='\t' -v dflt="$DEFAULT_WEIGHT" '
    FILENAME == ARGV[1] { if (NF >= 3) w[$1 "\t" $2] = $3 + 0; next }
    NF == 2 && $1 != "" && $2 != "" {
      key = $1 "\t" $2
      printf "%.6f\t%s\t%s\n", ((key in w) ? w[key] : dflt), $1, $2
    }' "$weights" - \
    | LC_ALL=C sort -t "$(printf '\t')" -k1,1gr -k2,2 -k3,3 \
    | awk -F '\t' -v OFS='\t' -v k="$SHARD_K" -v n="$SHARD_N" '
      BEGIN { for (s = 1; s <= n; s++) load[s] = 0 }
      {
        best = 1
        for (s = 2; s <= n; s++) if (load[s] < load[best]) best = s
        load[best] += $1
        if (best == k) print $2, $3
      }' \
    | LC_ALL=C sort
}

if [ "${1:-}" = "--partition" ]; then
  [ "$#" -eq 2 ] || usage
  parse_shard "$2"
  partition
  exit 0
fi

[ "$#" -ge 3 ] || usage
parse_shard "$1"
shift
pkgs=()
while [ "$#" -gt 0 ] && [ "$1" != "--" ]; do
  pkgs+=("$1")
  shift
done
[ "${1:-}" = "--" ] || usage
shift
flags=("$@")
[ "${#pkgs[@]}" -gt 0 ] || usage

# The full list. `go test -list` prints a package's names, then its `ok` line;
# a package with no tests prints `?` or an `ok` with no names before it.
listing="$(go test ${flags[@]+"${flags[@]}"} -list '.*' "${pkgs[@]}")"
full="$(printf '%s\n' "$listing" | awk '
  /^(Test|Example|Fuzz)[A-Za-z0-9_]*$/ { names[++c] = $0; next }
  /^ok[ \t]/ { for (i = 1; i <= c; i++) printf "%s\t%s\n", $2, names[i]; c = 0; next }
')"
total="$(printf '%s\n' "$full" | grep -c . || true)"
mine="$(printf '%s\n' "$full" | partition)"
count="$(printf '%s\n' "$mine" | grep -c . || true)"
echo "go-test-shard: shard $SHARD_K/$SHARD_N runs $count of $total tests" \
  "(list sha256 $(printf '%s\n' "$full" | LC_ALL=C sort -u | sha256_16))"
if [ "$total" -eq 0 ] || [ "$count" -eq 0 ]; then
  echo "go-test-shard: FAIL — shard $SHARD_K/$SHARD_N selected no tests from ${pkgs[*]}; the list or the partition is wrong" >&2
  exit 1
fi

status=0
timings="$(mktemp "${TMPDIR:-/tmp}/go-test-shard.XXXXXX")"
trap 'rm -f "$timings" "$timings.log"' EXIT
for pkg in $(printf '%s\n' "$mine" | cut -f1 | LC_ALL=C sort -u); do
  regex="^($(printf '%s\n' "$mine" | awk -F '\t' -v p="$pkg" '$1 == p { print $2 }' | paste -sd '|' -))\$"
  n="$(printf '%s\n' "$mine" | awk -F '\t' -v p="$pkg" '$1 == p' | grep -c .)"
  echo "go-test-shard: $pkg ($n tests)"
  # -v prints each top-level test's own duration ("--- PASS: TestX (1.23s)"
  # at column 0), which the summary below turns into go-test-durations.tsv
  # lines. The test output itself passes through unchanged.
  rc=0
  # pipefail (set above) makes the pipeline's status go test's own.
  go test ${flags[@]+"${flags[@]}"} -v -run "$regex" "$pkg" 2>&1 | tee "$timings.log" || rc=$?
  awk -v p="$pkg" 'match($0, /^--- (PASS|FAIL): [A-Za-z0-9_]+ \([0-9.]+s\)$/) {
        printf "%s\t%s\t%s\n", p, $3, substr($4, 2, length($4) - 3)
      }' "$timings.log" >> "$timings"
  [ "$rc" = 0 ] || status="$rc"
done
echo "go-test-shard: the 25 slowest tests of this shard (go-test-durations.tsv lines):"
LC_ALL=C sort -t "$(printf '\t')" -k3,3gr "$timings" | head -25 | sed 's/^/  /'
exit "$status"

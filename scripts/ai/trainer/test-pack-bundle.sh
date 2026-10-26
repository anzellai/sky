#!/usr/bin/env bash
#
# scripts/ai/trainer/test-pack-bundle.sh - pack-bundle.sh gives the same bytes
# for the same bundle content, whatever the tree's mtimes, modes, owner,
# directory order or place on disk; and different bytes for different
# content or a different epoch.
#
#   test-pack-bundle.sh [--work <dir>]
#
# It builds a small fake bundle (no trainer, no compiler), packs it, then
# copies it elsewhere, perturbs everything that must not matter, and packs it
# again.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
WORK=""
while [ $# -gt 0 ]; do
  case "$1" in
    --work) WORK="$2"; shift 2 ;;
    *) echo "usage: test-pack-bundle.sh [--work <dir>]" >&2; exit 2 ;;
  esac
done
WORK="${WORK:-$(mktemp -d "${TMPDIR:-/tmp}/test-pack-bundle.XXXXXX")}"
rm -rf "$WORK" && mkdir -p "$WORK"
FAILS=0
fail() { echo "FAIL: $*" >&2; FAILS=$((FAILS + 1)); }
pass() { echo "ok    $*"; }
digest() { shasum -a 256 "$1" | cut -c1-64; }

a="$WORK/a/sky-trainer-0.0.0-test"
mkdir -p "$a/lib" "$a/licenses"
printf '#!/bin/sh\necho trainer\n' > "$a/sky-trainer"
chmod 0755 "$a/sky-trainer"
head -c 4096 /dev/zero | tr '\0' 'x' > "$a/lib/libfake.so"
chmod 0755 "$a/lib/libfake.so"
echo "MIT" > "$a/licenses/LICENSE-fake"
echo '{"spdxVersion":"SPDX-2.3"}' > "$a/sbom.spdx.json"
E=1700000000

"$HERE/pack-bundle.sh" "$a" "$WORK/one.tar.gz" "$E" > /dev/null

# The same content, elsewhere, created in another order, with other mtimes
# and modes.
b="$WORK/b/deeper/sky-trainer-0.0.0-test"
mkdir -p "$b/licenses" "$b/lib"
cp "$a/sbom.spdx.json" "$b/"
cp "$a/licenses/LICENSE-fake" "$b/licenses/"
cp "$a/lib/libfake.so" "$b/lib/"
cp "$a/sky-trainer" "$b/"
chmod 0700 "$b/sky-trainer" "$b/lib/libfake.so"
chmod 0600 "$b/sbom.spdx.json"
chmod 0775 "$b/lib"
touch -t 202001020304 "$b/lib/libfake.so" "$b/licenses"
sleep 1
touch "$b/sky-trainer"
"$HERE/pack-bundle.sh" "$b" "$WORK/two.tar.gz" "$E" > /dev/null
if cmp -s "$WORK/one.tar.gz" "$WORK/two.tar.gz"; then
  pass "the same content packs to the same bytes ($(digest "$WORK/one.tar.gz"))"
else
  fail "the same content packed to $(digest "$WORK/one.tar.gz") and $(digest "$WORK/two.tar.gz")"
fi

# SOURCE_DATE_EPOCH is the default epoch.
SOURCE_DATE_EPOCH="$E" "$HERE/pack-bundle.sh" "$b" "$WORK/three.tar.gz" > /dev/null
cmp -s "$WORK/one.tar.gz" "$WORK/three.tar.gz" && pass "SOURCE_DATE_EPOCH is the epoch" ||
  fail "SOURCE_DATE_EPOCH did not give the same bytes"

# What does matter still changes the bytes.
"$HERE/pack-bundle.sh" "$b" "$WORK/later.tar.gz" "$((E + 1))" > /dev/null
cmp -s "$WORK/one.tar.gz" "$WORK/later.tar.gz" && fail "another epoch gave the same bytes" || pass "another epoch changes the bytes"
echo "Apache-2.0" > "$b/licenses/LICENSE-fake"
"$HERE/pack-bundle.sh" "$b" "$WORK/edited.tar.gz" "$E" > /dev/null
cmp -s "$WORK/one.tar.gz" "$WORK/edited.tar.gz" && fail "other content gave the same bytes" || pass "other content changes the bytes"

# The entries are in name order, the top directory first.
names="$(tar -tzf "$WORK/one.tar.gz")"
if [ "$names" = "$(echo "$names" | LC_ALL=C sort)" ]; then pass "the entries are in name order"; else fail "the entries are not sorted: $names"; fi

if [ "$FAILS" -gt 0 ]; then
  echo "test-pack-bundle: $FAILS FAILED (work dir $WORK)" >&2
  exit 1
fi
echo "test-pack-bundle: all passed"

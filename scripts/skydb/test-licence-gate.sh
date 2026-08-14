#!/usr/bin/env bash
#
# Falsification suite for the PostgreSQL bundle licence gate.
#
# A gate that has never been observed failing is not evidence. This repo has
# repeatedly shipped gates that looked correct and passed against the very
# defect they existed to catch, so the requirement here is not that a failing
# path is *written* — it is that a failing path is EXECUTED and OBSERVED.
#
# Each case below constructs a bundle that SHOULD be rejected and asserts that
# scan-bundle-licences.sh rejects it, for the stated reason. The clean case is
# equally load-bearing in the other direction: a gate that rejects everything
# is as useless as one that accepts everything, and only running both proves
# the gate discriminates.
#
# Fixtures are real ELF/Mach-O objects produced by the host compiler, so the
# scanner reads genuine load commands through genuine otool/objdump. The only
# thing synthesised is the CONTENT of the depended-on libraries, which the
# scanner never inspects — it classifies by recorded dependency name, and that
# is exactly the mechanism under test. Where a real GNU readline is installed
# on the host, an additional case links against it for full fidelity.
#
# Usage:
#   test-licence-gate.sh [--bundle DIR]
#
# With --bundle, two extra cases run against a REAL built bundle: unmodified
# (must pass) and with a GPL-linked extension planted in lib/ (must fail).
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCANNER="${SCRIPT_DIR}/scan-bundle-licences.sh"

REAL_BUNDLE=""
while [ $# -gt 0 ]; do
  case "$1" in
    --bundle) REAL_BUNDLE="$2"; shift 2 ;;
    -h|--help) sed -n '2,28p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

case "$(uname -s)" in
  Darwin) HOST_OS=darwin; DL=dylib ;;
  Linux)  HOST_OS=linux;  DL=so ;;
  *) echo "unsupported platform" >&2; exit 2 ;;
esac

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
CC="${CC:-cc}"

PASS=0; FAIL=0
ok()   { printf '  \033[1;32mok\033[0m   %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[1;31mFAIL\033[0m %s\n' "$*"; FAIL=$((FAIL+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

# ─────────────────────────────────────────────────────────────────────
# Fixture construction
# ─────────────────────────────────────────────────────────────────────

# A shared library with a chosen SONAME/install_name and no real content. The
# gate classifies dependencies by recorded name; content is irrelevant to it.
make_stub_lib() {
  local out="$1" soname="$2" src="${WORK}/stub.c"
  echo 'int sky_stub_symbol(void) { return 0; }' >| "$src"
  if [ "$HOST_OS" = darwin ]; then
    $CC -dynamiclib -o "$out" "$src" -install_name "@rpath/${soname}" 2>/dev/null
  else
    $CC -shared -fPIC -o "$out" "$src" -Wl,-soname,"${soname}" 2>/dev/null
  fi
}

# An object linking zero or more libraries by path.
make_object() {
  local out="$1" kind="$2"; shift 2
  local src="${WORK}/obj.c"
  if [ "$kind" = exe ]; then
    echo 'int main(void) { return 0; }' >| "$src"
    $CC -o "$out" "$src" "$@" 2>/dev/null
  else
    echo 'int sky_mod_symbol(void) { return 0; }' >| "$src"
    if [ "$HOST_OS" = darwin ]; then
      $CC -dynamiclib -o "$out" "$src" "$@" 2>/dev/null
    else
      $CC -shared -fPIC -o "$out" "$src" "$@" 2>/dev/null
    fi
  fi
}

# A minimal but structurally faithful bundle: the manifest the scanner reads,
# the five shipped binaries, and a couple of real contrib module names so the
# committed module allowlist applies exactly as it does to a real bundle.
make_base_bundle() {
  local b="$1"
  mkdir -p "$b/bin" "$b/lib" "$b/share/extension"
  cat >| "$b/BUNDLE.json" <<'JSON'
{
  "postgres_version": "18.6",
  "platform": "fixture",
  "psql_excluded": "GPL-3.0 readline linkage"
}
JSON
  local x
  for x in postgres initdb pg_ctl pg_dump pg_restore; do make_object "$b/bin/$x" exe; done
  for x in pg_trgm pgcrypto vector pg_partman_bgw; do make_object "$b/lib/$x.$DL" lib; done
  make_object "$b/lib/libpq.5.$DL" lib
}

# Run the gate; capture output and exit status without tripping `set -e`.
run_gate() {
  local bundle="$1" out="$2" rc=0
  "$SCANNER" "$bundle" >| "$out" 2>&1 || rc=$?
  echo "$rc"
}

# Assert the gate REJECTED, and that its output names the expected reason.
expect_reject() {
  local label="$1" bundle="$2" needle="$3" out="${WORK}/out.$$"
  local rc; rc="$(run_gate "$bundle" "$out")"
  if [ "$rc" -ne 1 ]; then
    bad "${label}: expected exit 1 (REJECTED), got ${rc}"
    sed 's/^/       | /' "$out" | head -20
    return
  fi
  if ! command grep -q "GATE FAIL" "$out"; then
    bad "${label}: exit 1 but no GATE FAIL verdict in output"; return
  fi
  if ! command grep -qi "$needle" "$out"; then
    bad "${label}: rejected, but output never mentions '${needle}' — rejected for the wrong reason"
    sed 's/^/       | /' "$out" | head -20
    return
  fi
  ok "${label} — rejected, citing '${needle}'"
  LAST_OUT="$out"
}

expect_accept() {
  local label="$1" bundle="$2" out="${WORK}/out.$$"
  local rc; rc="$(run_gate "$bundle" "$out")"
  if [ "$rc" -ne 0 ]; then
    bad "${label}: expected exit 0 (clean), got ${rc}"
    sed 's/^/       | /' "$out" | head -30
    return
  fi
  command grep -q "GATE PASS" "$out" || { bad "${label}: exit 0 but no GATE PASS verdict"; return; }
  ok "${label} — accepted"
  LAST_OUT="$out"
}

# ─────────────────────────────────────────────────────────────────────
hdr "Gate discrimination — synthesised fixtures (${HOST_OS})"

# C1. A clean bundle MUST pass. Without this, a gate that rejects unconditionally
#     would score full marks on every rejection case below and still be worthless.
C1="${WORK}/c1"; make_base_bundle "$C1"
expect_accept "C1 clean bundle" "$C1"

# C2. GPL dependency on the main server binary — the obvious case.
C2="${WORK}/c2"; make_base_bundle "$C2"
make_stub_lib "${WORK}/libreadline.8.${DL}" "libreadline.8.${DL}"
make_object "$C2/bin/postgres" exe "${WORK}/libreadline.8.${DL}"
expect_reject "C2 GPL dep on bin/postgres" "$C2" "readline"

# C3. THE LOAD-BEARING CASE.
#     bin/postgres is clean; the GPL linkage is on an extension module only.
#     An extension is dlopen()ed at runtime and is never linked into the server
#     executable, so a gate that inspects only bin/postgres sees nothing wrong
#     here and passes a bundle carrying a GPL extension in lib/ — while
#     appearing to check exactly that. If this case ever goes green, the gate
#     has silently regressed to inspecting the main executable alone.
C3="${WORK}/c3"; make_base_bundle "$C3"
make_object "$C3/lib/pg_trgm.$DL" lib "${WORK}/libreadline.8.${DL}"
expect_reject "C3 GPL dep on an EXTENSION only, server binary clean" "$C3" "pg_trgm"

# C4. Fail closed. An unclassified dependency is rejected, not waved through:
#     the dangerous dependency is by definition the one nobody anticipated.
C4="${WORK}/c4"; make_base_bundle "$C4"
make_stub_lib "${WORK}/libmysteryware.1.${DL}" "libmysteryware.1.${DL}"
make_object "$C4/lib/pgcrypto.$DL" lib "${WORK}/libmysteryware.1.${DL}"
expect_reject "C4 unclassified dependency (fail closed)" "$C4" "libmysteryware"

# C5. A shipped module nobody reviewed. PostGIS is GPL-2.0 and is excluded on
#     licence grounds by docs/skydb/embedded-postgres.md; planting it in lib/
#     must be caught as a shipped object, independent of what it links.
C5="${WORK}/c5"; make_base_bundle "$C5"
make_object "$C5/lib/postgis-3.$DL" lib
expect_reject "C5 unreviewed GPL extension planted in lib/" "$C5" "postgis"

# C6. A vendored copy of readline sitting in lib/ — caught as a shipped object
#     even if nothing in the bundle links it.
C6="${WORK}/c6"; make_base_bundle "$C6"
make_stub_lib "$C6/lib/libreadline.8.${DL}" "libreadline.8.${DL}"
expect_reject "C6 vendored libreadline in lib/, unlinked" "$C6" "readline"

# C7. Full fidelity: link against the host's REAL GNU readline where one is
#     installed, so at least one rejection is driven by a genuine GPL library
#     rather than a stub carrying its name.
REAL_RL=""
for cand in /opt/homebrew/opt/readline/lib/libreadline.dylib \
            /usr/local/opt/readline/lib/libreadline.dylib \
            /usr/lib/x86_64-linux-gnu/libreadline.so \
            /usr/lib/aarch64-linux-gnu/libreadline.so \
            /usr/lib64/libreadline.so; do
  [ -e "$cand" ] && { REAL_RL="$cand"; break; }
done
if [ -n "$REAL_RL" ]; then
  C7="${WORK}/c7"; make_base_bundle "$C7"
  if make_object "$C7/lib/vector.$DL" lib "$REAL_RL"; then
    expect_reject "C7 extension linked against the REAL GNU readline (${REAL_RL})" "$C7" "readline"
  else
    printf '  \033[1;33mskip\033[0m C7 — could not link against %s\n' "$REAL_RL"
  fi
else
  printf '  \033[1;33mskip\033[0m C7 — no GNU readline installed on this host\n'
fi

# ─────────────────────────────────────────────────────────────────────
if [ -n "$REAL_BUNDLE" ]; then
  hdr "Gate discrimination — REAL built bundle"
  [ -d "$REAL_BUNDLE" ] || { echo "no such bundle: $REAL_BUNDLE" >&2; exit 2; }

  # C8. The real bundle, untouched, must be clean.
  expect_accept "C8 real bundle, unmodified" "$REAL_BUNDLE"

  # C9. The same real bundle with one GPL-linked extension planted in lib/.
  #     This is the deliberately-dirty bundle: identical to the shipping
  #     artifact in every other respect, so the ONLY thing distinguishing a
  #     green run from a red one is the planted object.
  C9="${WORK}/c9"
  command cp -Rf "$REAL_BUNDLE" "$C9"
  chmod -R u+w "$C9"
  make_object "$C9/lib/pg_trgm.$DL" lib "${WORK}/libreadline.8.${DL}"
  expect_reject "C9 real bundle + GPL-linked extension planted in lib/" "$C9" "readline"
fi

# ─────────────────────────────────────────────────────────────────────
hdr "Result"
printf '  %d passed, %d failed\n\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
echo "The licence gate discriminates: it accepts a clean bundle and rejects"
echo "each constructed violation, including a GPL dependency reachable only"
echo "through an extension module."

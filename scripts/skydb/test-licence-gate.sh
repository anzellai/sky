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

# Assert the gate REJECTED, that its output names the expected reason, and that
# it rejected for EXACTLY the expected cause or causes.
#
#   expect_reject <label> <bundle> <needle> "<CAUSE> [<CAUSE> …]"
#
# The cause set is not decoration. The scanner counts three independent
# rejection causes — COPYLEFT, UNKNOWN, UNVENDORED — and before this argument
# existed the suite asserted only "exit 1 and the output mentions readline".
# Every fixture's planted library tripped COPYLEFT or UNKNOWN as well as
# UNVENDORED, so UNVENDORED was never the SOLE cause of any rejection: the
# entire arm could be deleted (`if false && …`) and the suite still reported
# 7 passed, 0 failed. A gate arm that no fixture isolates is not tested, it is
# merely present.
#
# So the named causes must each be non-zero AND the unnamed ones must each be
# ZERO. That direction matters as much: it is what makes a fixture pin down
# which arm did the rejecting rather than merely that something did.
expect_reject() {
  local label="$1" bundle="$2" needle="$3" causes="$4" out="${WORK}/out.$$"
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

  local c u v cause
  c="$(sed -n 's/^ *copyleft violations *: *//p' "$out")"
  u="$(sed -n 's/^ *unclassified *: *//p' "$out")"
  v="$(sed -n 's/^ *unvendored deps *: *//p' "$out")"
  if [ -z "$c" ] || [ -z "$u" ] || [ -z "$v" ]; then
    bad "${label}: could not read the violation counters from the verdict — the \
report format changed and every cause assertion below is now blind"
    sed 's/^/       | /' "$out" | head -20
    return
  fi

  local want_c=0 want_u=0 want_v=0
  for cause in $causes; do
    case "$cause" in
      COPYLEFT)   want_c=1 ;;
      UNKNOWN)    want_u=1 ;;
      UNVENDORED) want_v=1 ;;
      *) bad "${label}: '${cause}' is not a rejection cause — typo in the fixture"; return ;;
    esac
  done
  [ "$want_c$want_u$want_v" != "000" ] || { bad "${label}: no cause named"; return; }

  # Written as `if` blocks, not as `[ … ] && why=…` one-liners: under `set -e` a
  # standalone `&&` list whose LAST command fails aborts the script, so the
  # terse form would have made a satisfied assertion look like a crash.
  local why=""
  if [ "$want_c" -eq 1 ] && [ "$c" -eq 0 ]; then why="${why} expected COPYLEFT but the count is 0;"; fi
  if [ "$want_c" -eq 0 ] && [ "$c" -ne 0 ]; then why="${why} unexpected COPYLEFT (${c});"; fi
  if [ "$want_u" -eq 1 ] && [ "$u" -eq 0 ]; then why="${why} expected UNKNOWN but the count is 0;"; fi
  if [ "$want_u" -eq 0 ] && [ "$u" -ne 0 ]; then why="${why} unexpected UNKNOWN (${u});"; fi
  if [ "$want_v" -eq 1 ] && [ "$v" -eq 0 ]; then why="${why} expected UNVENDORED but the count is 0;"; fi
  if [ "$want_v" -eq 0 ] && [ "$v" -ne 0 ]; then why="${why} unexpected UNVENDORED (${v});"; fi
  if [ -n "$why" ]; then
    bad "${label}: rejected, but not for the stated cause(s) [${causes}] —${why} \
(counted copyleft=${c} unclassified=${u} unvendored=${v})"
    sed 's/^/       | /' "$out" | head -20
    return
  fi

  ok "${label} — rejected as ${causes}, citing '${needle}'"
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
#     Two causes, not one: the library is GPL (COPYLEFT) and it lives outside
#     the bundle (UNVENDORED). Naming both is the point — a fixture that trips
#     two arms cannot be evidence for either one alone.
C2="${WORK}/c2"; make_base_bundle "$C2"
make_stub_lib "${WORK}/libreadline.8.${DL}" "libreadline.8.${DL}"
make_object "$C2/bin/postgres" exe "${WORK}/libreadline.8.${DL}"
expect_reject "C2 GPL dep on bin/postgres" "$C2" "readline" "COPYLEFT UNVENDORED"

# C3. THE LOAD-BEARING CASE.
#     bin/postgres is clean; the GPL linkage is on an extension module only.
#     An extension is dlopen()ed at runtime and is never linked into the server
#     executable, so a gate that inspects only bin/postgres sees nothing wrong
#     here and passes a bundle carrying a GPL extension in lib/ — while
#     appearing to check exactly that. If this case ever goes green, the gate
#     has silently regressed to inspecting the main executable alone.
C3="${WORK}/c3"; make_base_bundle "$C3"
make_object "$C3/lib/pg_trgm.$DL" lib "${WORK}/libreadline.8.${DL}"
expect_reject "C3 GPL dep on an EXTENSION only, server binary clean" "$C3" "pg_trgm" "COPYLEFT UNVENDORED"

# C4. Fail closed. An unclassified dependency is rejected, not waved through:
#     the dangerous dependency is by definition the one nobody anticipated.
C4="${WORK}/c4"; make_base_bundle "$C4"
make_stub_lib "${WORK}/libmysteryware.1.${DL}" "libmysteryware.1.${DL}"
make_object "$C4/lib/pgcrypto.$DL" lib "${WORK}/libmysteryware.1.${DL}"
expect_reject "C4 unclassified dependency (fail closed)" "$C4" "libmysteryware" "UNKNOWN UNVENDORED"

# C5. A shipped module nobody reviewed. PostGIS is GPL-2.0 and is excluded on
#     licence grounds by docs/skydb/embedded-postgres.md; planting it in lib/
#     must be caught as a shipped object, independent of what it links.
C5="${WORK}/c5"; make_base_bundle "$C5"
make_object "$C5/lib/postgis-3.$DL" lib
expect_reject "C5 unreviewed GPL extension planted in lib/" "$C5" "postgis" "COPYLEFT"

# C6. A vendored copy of readline sitting in lib/ — caught as a shipped object
#     even if nothing in the bundle links it.
C6="${WORK}/c6"; make_base_bundle "$C6"
make_stub_lib "$C6/lib/libreadline.8.${DL}" "libreadline.8.${DL}"
expect_reject "C6 vendored libreadline in lib/, unlinked" "$C6" "readline" "COPYLEFT"

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
    expect_reject "C7 extension linked against the REAL GNU readline (${REAL_RL})" "$C7" "readline" "COPYLEFT UNVENDORED"
  else
    printf '  \033[1;33mskip\033[0m C7 — could not link against %s\n' "$REAL_RL"
  fi
else
  printf '  \033[1;33mskip\033[0m C7 — no GNU readline installed on this host\n'
fi

# C10. THE UNVENDORED ARM, ISOLATED — the one cause no other fixture proves.
#      Every case above plants something that is ALSO copyleft or ALSO
#      unclassified, so each of them would still be rejected with the
#      unvendored arm deleted outright; the suite scored 7/7 with `if false &&`
#      in front of it. Here bin/postgres links a PERMISSIVE library (zstd, in
#      the licence table, no copyleft anywhere in sight) that simply is not in
#      the bundle. Nothing but the unvendored arm can reject this.
#
#      It is also the realistic case, not a contrived one: a bundle that links
#      the BUILD MACHINE's OpenSSL, ICU or zstd is what happens when the
#      rpath/@loader_path relocation in build-postgres-bundle.sh silently stops
#      covering a library. It runs on the machine that built it and fails on
#      every other machine — or worse, resolves against a different version.
C10="${WORK}/c10"; make_base_bundle "$C10"
make_stub_lib "${WORK}/libzstd.1.${DL}" "libzstd.1.${DL}"
make_object "$C10/bin/postgres" exe "${WORK}/libzstd.1.${DL}"
expect_reject "C10 permissive dependency living OUTSIDE the bundle" "$C10" "libzstd" "UNVENDORED"

# C11. A SYMLINK IS PART OF WHAT WE SHIP — the C6 fixture, one `ln -s` apart.
#      `find -type f` does not match a symbolic link, so lib/ was enumerated
#      with every link in it skipped. The same GNU readline that made C6 read
#      `GATE FAIL — copyleft violations: 1` made this bundle read `GATE PASS —
#      no GPL, LGPL or AGPL component is shipped or linked`. Bundles carry
#      these links by construction: build-postgres-bundle.sh copies the staged
#      tree with `cp -Rf`, which preserves PostgreSQL's soname chains.
C11="${WORK}/c11"; make_base_bundle "$C11"
ln -s "${WORK}/libreadline.8.${DL}" "$C11/lib/libreadline.8.${DL}"
expect_reject "C11 libreadline in lib/ as a SYMLINK, unlinked" "$C11" "readline" "COPYLEFT UNVENDORED"

# C12. THE LAUNDERING HALF of the same defect. `resolve_dep` tested `[ -f
#      "$BUNDLE/lib/$base" ]`, which FOLLOWS a link — so a dependency on the
#      build machine's readline, reached through a link in lib/, resolved to
#      `bundle:lib/libreadline.8.dylib` and reported `unvendored deps: 0`. The
#      bundle claimed to vendor a file it did not contain.
C12="${WORK}/c12"; make_base_bundle "$C12"
make_object "$C12/bin/postgres" exe "${WORK}/libreadline.8.${DL}"
ln -s "${WORK}/libreadline.8.${DL}" "$C12/lib/libreadline.8.${DL}"
expect_reject "C12 symlink launders an out-of-bundle dependency" "$C12" "escapes:" "COPYLEFT UNVENDORED"
if [ -n "${LAST_OUT:-}" ] && command grep -q "bundle:lib/libreadline" "$LAST_OUT"; then
  bad "C12: the dependency is still reported as bundle:lib/libreadline — a link out of the bundle is being counted as vendored"
fi

# C13. THE OTHER DIRECTION, and the reason C11/C12 cannot simply reject every
#      link: a real bundle's lib/ is mostly soname chains pointing WITHIN
#      itself. `libpq.5.dylib -> libpq.5.18.dylib` is how shared libraries are
#      shipped, and a gate that failed on it would be turned off within the day.
#      A link whose chain stays inside the bundle is a name, not a violation.
C13="${WORK}/c13"; make_base_bundle "$C13"
make_object "$C13/lib/libpq.5.18.${DL}" lib
ln -sf "libpq.5.18.${DL}" "$C13/lib/libpq.5.${DL}"
ln -sf "libpq.5.${DL}" "$C13/lib/libpq.${DL}"
expect_accept "C13 in-bundle soname chain (relative, two hops)" "$C13"

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
  expect_reject "C9 real bundle + GPL-linked extension planted in lib/" "$C9" "readline" "COPYLEFT UNVENDORED"
fi

# ─────────────────────────────────────────────────────────────────────
hdr "Result"
printf '  %d passed, %d failed\n\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
echo "The licence gate discriminates: it accepts a clean bundle and rejects"
echo "each constructed violation, including a GPL dependency reachable only"
echo "through an extension module."

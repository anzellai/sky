#!/usr/bin/env bash
#
# SBOM generator + licence gate for a PostgreSQL bundle.
#
# See docs/skydb/embedded-postgres.md — "The gate". Two properties of this
# scanner are load-bearing, and both are easy to write something that *looks*
# like and is not:
#
#   1. IT RUNS AGAINST THE BUILT ARTIFACTS, NOT THE CONFIGURE LINE.
#      A configure flag records an intention. `--without-readline` in a
#      workflow file is a statement about what we asked for; only the binary
#      records what happened. A gate that greps the workflow for
#      `--without-readline` verifies nothing at all about what shipped, while
#      looking like it verifies exactly that.
#
#   2. IT WALKS EVERY SHARED OBJECT IN THE BUNDLE, NOT JUST `postgres`.
#      An extension is a .so that the server dlopen()s at runtime. It is never
#      linked into the server executable, so it never appears in the server
#      executable's dependency list. A gate that inspected only bin/postgres
#      would pass a bundle carrying a GPL extension in lib/, while appearing to
#      check precisely that. Every ELF/Mach-O object under the bundle is
#      enumerated and its dependency list read individually.
#
# Usage:
#   scan-bundle-licences.sh <bundle-dir> [--sbom-out FILE] [--bless-modules]
#
# Exit status:
#   0  clean       — no copyleft, every component accounted for
#   1  REJECTED    — copyleft component, or a component this scanner cannot
#                    account for (see "fail closed" below)
#   2  usage/internal error
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MODULE_ALLOWLIST="${SCRIPT_DIR}/postgres-modules.txt"

BUNDLE=""
SBOM_OUT=""
BLESS=0
while [ $# -gt 0 ]; do
  case "$1" in
    --sbom-out)      SBOM_OUT="$2"; shift 2 ;;
    --bless-modules) BLESS=1; shift ;;
    -h|--help)       sed -n '2,32p' "$0"; exit 0 ;;
    -*)              echo "unknown option: $1" >&2; exit 2 ;;
    *)               BUNDLE="$1"; shift ;;
  esac
done
[ -n "$BUNDLE" ] || { echo "usage: $0 <bundle-dir> [--sbom-out FILE]" >&2; exit 2; }
[ -d "$BUNDLE" ] || { echo "no such bundle directory: $BUNDLE" >&2; exit 2; }
BUNDLE="$(cd "$BUNDLE" && pwd)"

case "$(uname -s)" in
  Darwin) HOST_OS=darwin ;;
  Linux)  HOST_OS=linux ;;
  *) echo "licence scanning is only implemented for Linux and macOS" >&2; exit 2 ;;
esac

# ─────────────────────────────────────────────────────────────────────
# THE LICENCE TABLE
#
# Keyed on the NORMALISED library name (version suffixes stripped), because
# that is the only stable identity a dependency record carries. Classes:
#
#   PERMISSIVE  redistributable inside an Apache-2.0 product, no obligation
#               beyond attribution.
#   PLATFORM    provided by the host operating system. NOT redistributed by
#               us; the bundle references it, the bundle does not contain it.
#               This distinction is load-bearing on Linux, where glibc is
#               LGPL-2.1: were "LGPL anywhere" the rule, every Linux bundle
#               would be rejected on libc.so.6 — a component we do not ship
#               and could not avoid. Membership here is by explicit NAME, never
#               by path: libreadline lives in /usr/lib on Debian, so treating
#               "resolved from a system path" as a platform signal would grant
#               the single library this gate exists to catch a free pass.
#   EXCEPTION   nominally copyleft, carrying an explicit linking exception that
#               permits use in a non-copyleft program. libgcc/libstdc++ are
#               GPL-3.0 WITH GCC-exception; the exception is what makes every
#               non-GPL C program on Linux lawful. Recorded, not rejected — and
#               matched exactly, never by substring, since a substring match for
#               "GPL" cannot tell GPL-3.0 from GPL-3.0-with-GCC-exception or
#               from LGPL-2.1.
#   COPYLEFT    GPL / LGPL / AGPL without a usable exception. REJECTED.
#
# Anything absent from this table is UNKNOWN and is REJECTED (fail closed). A
# new dependency must be classified by a human before it can ship. Defaulting
# unknown to "probably fine" is how a gate passes the defect it exists to
# catch: the dangerous dependency is by definition the one nobody anticipated.
# ─────────────────────────────────────────────────────────────────────
classify() {
  local name="$1" path="${2:-}"
  case "$name" in
    # ── The PostgreSQL distribution itself ────────────────────────────
    postgres|initdb|pg_ctl|pg_dump|pg_restore|libpq|libpgcommon|libpgport)
      echo "PostgreSQL|PERMISSIVE|PostgreSQL ${PG_VER:-} core" ;;

    # ── Platform C runtime / OS-provided ──────────────────────────────
    libc|libm|libdl|libpthread|librt|libutil|libresolv|libnsl|libanl)
      echo "LGPL-2.1-or-later|PLATFORM|host C library; referenced, not redistributed" ;;
    ld-linux*|ld64|libSystem|libSystem.B)
      echo "Platform|PLATFORM|dynamic loader / host libSystem" ;;
    CoreFoundation|Security|SystemConfiguration|CoreServices|Foundation|IOKit|CoreGraphics|Kerberos|GSS|libobjc|libc++|libc++abi)
      echo "Apple-Platform|PLATFORM|macOS system framework, OS-provided" ;;
    # libiconv is the one genuinely path-sensitive entry in this table.
    #
    # Apple's /usr/lib/libiconv.2.dylib is GNU-derived and LGPL — it is not a
    # different, permissive project, and it would be wrong to record it as one.
    # It is nonetheless acceptable for the same reason glibc is: it is part of
    # the operating system, provided by macOS, and NOT redistributed by us. The
    # bundle references it; the bundle does not contain it.
    #
    # A libiconv resolved from anywhere else — Homebrew, /usr/local, or
    # vendored into lib/ — IS redistributed by us and is rejected. Same soname,
    # entirely different obligation, decided by where it comes from.
    libiconv|libcharset)
      case "${HOST_OS}:${path}" in
        darwin:/usr/lib/*|darwin:/System/Library/*)
          echo "LGPL-2.1|PLATFORM|macOS system libiconv; OS-provided, referenced not redistributed" ;;
        *)
          echo "LGPL-2.1|COPYLEFT|GNU libiconv, redistributed" ;;
      esac ;;

    # ── Copyleft with a linking exception ─────────────────────────────
    libgcc_s|libstdc++)
      echo "GPL-3.0-with-GCC-exception|EXCEPTION|GCC runtime library exception permits non-GPL linking" ;;

    # ── Permissive, deliberately linked (see the configure line) ──────
    libssl|libcrypto)
      echo "Apache-2.0|PERMISSIVE|OpenSSL 3.x" ;;
    libz)
      echo "Zlib|PERMISSIVE|zlib" ;;
    liblz4)
      echo "BSD-2-Clause|PERMISSIVE|LZ4" ;;
    libzstd)
      echo "BSD-3-Clause|PERMISSIVE|Zstandard (dual BSD-3/GPL-2; taken under BSD-3)" ;;
    libicuuc|libicui18n|libicudata|libicuio|libicutu)
      echo "Unicode-3.0|PERMISSIVE|ICU" ;;
    libxml2)
      echo "MIT|PERMISSIVE|libxml2" ;;
    liblzma)
      echo "0BSD|PERMISSIVE|xz-utils liblzma (public-domain terms)" ;;

    # ── Copyleft. These are what the gate exists to reject. ───────────
    # readline is the reason psql is not in the shipped set at all; see
    # docs/skydb/embedded-postgres.md and the exclusion comment in
    # build-postgres-bundle.sh.
    libreadline|libhistory)
      echo "GPL-3.0-or-later|COPYLEFT|GNU readline" ;;
    libsystemd|libudev)
      echo "LGPL-2.1-or-later|COPYLEFT|systemd" ;;
    libgmp)         echo "LGPL-3.0-or-later|COPYLEFT|GNU MP" ;;
    libgnutls)      echo "LGPL-2.1-or-later|COPYLEFT|GnuTLS" ;;
    libgcrypt)      echo "LGPL-2.1-or-later|COPYLEFT|libgcrypt" ;;
    libidn2)        echo "LGPL-3.0-or-later|COPYLEFT|libidn2" ;;
    libunistring)   echo "LGPL-3.0-or-later|COPYLEFT|libunistring" ;;
    libcrypt)       echo "LGPL-2.1-or-later|COPYLEFT|libxcrypt" ;;
    libperl)        echo "GPL-1.0-or-later|COPYLEFT|Perl (excluded via --without-perl)" ;;
    libtinfo|libncurses|libncursesw)
      # ncurses itself is X11-licensed, but nothing in the shipped set has any
      # business linking it: it arrives as a readline dependency, which means
      # readline got in. Rejected as the tripwire it is.
      echo "X11-distribute-modifications-variant|COPYLEFT|ncurses — only reachable via readline; its presence means readline linked" ;;
    postgis*|libpostgis*)
      echo "GPL-2.0-or-later|COPYLEFT|PostGIS — excluded on licence grounds" ;;
    timescaledb*) echo "TSL|COPYLEFT|TimescaleDB Timescale License — source-available, not permissive" ;;
    citus*)       echo "AGPL-3.0-or-later|COPYLEFT|Citus — network copyleft" ;;

    *) echo "UNKNOWN|UNKNOWN|not classified in the licence table" ;;
  esac
}

# Strip version decoration so `libssl.so.3`, `libicuuc.78.dylib` and
# `libreadline.8.3.dylib` all reduce to a stable identity.
normalise() {
  printf '%s' "${1##*/}" | sed -E 's/\.(so|dylib)(\.[0-9]+)*$//; s/(\.[0-9]+)+$//'
}

is_object() {
  case "$HOST_OS" in
    darwin) file -b "$1" 2>/dev/null | command grep -q 'Mach-O' ;;
    linux)  [ "$(head -c 4 "$1" 2>/dev/null | od -An -c | tr -d ' \n')" = '177ELF' ] ;;
  esac
}

# Direct dependency records of one object, one per line.
deps_of() {
  local obj="$1"
  if [ "$HOST_OS" = darwin ]; then
    # otool -L line 1 is the object itself; the rest are LC_LOAD_DYLIB entries.
    otool -L "$obj" 2>/dev/null | tail -n +2 | awk '{print $1}'
  else
    # objdump -p reads DT_NEEDED without executing the object. `ldd` would
    # resolve transitively but runs the loader against untrusted input; the
    # closure is recovered anyway because every vendored library in the bundle
    # is itself walked.
    objdump -p "$obj" 2>/dev/null | awk '/NEEDED/ {print $2}'
  fi
}

# Where a dependency record actually resolves. Used only for reporting and for
# the libiconv path split — never to decide PLATFORM membership.
resolve_dep() {
  local dep="$1" base; base="${dep##*/}"
  if [ -f "${BUNDLE}/lib/${base}" ]; then echo "bundle:lib/${base}"; return; fi
  # macOS 11+ ships its system libraries inside the dyld shared cache, so
  # /usr/lib/libiconv.2.dylib is a perfectly valid, loadable dependency that
  # does NOT exist as a file on disk. Testing -f and calling it "absent" makes
  # every macOS platform library look like a missing dependency.
  if [ "$HOST_OS" = darwin ]; then
    case "$dep" in /usr/lib/*|/System/Library/*) echo "platform:${dep}"; return ;; esac
  fi
  case "$dep" in
    @rpath/*|@loader_path/*|@executable_path/*) echo "unresolved:${dep}"; return ;;
  esac
  [ -f "$dep" ] && { echo "host:${dep}"; return; }
  echo "host-absent:${dep}"
}

PG_VER="$(sed -n 's/.*"postgres_version": *"\([^"]*\)".*/\1/p' "${BUNDLE}/BUNDLE.json" 2>/dev/null || true)"
PLATFORM="$(sed -n 's/.*"platform": *"\([^"]*\)".*/\1/p' "${BUNDLE}/BUNDLE.json" 2>/dev/null || echo unknown)"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
: >| "$TMP/objects"; : >| "$TMP/deps"; : >| "$TMP/shipped"

# ── Enumerate every object in the bundle ─────────────────────────────
while IFS= read -r f; do
  is_object "$f" || continue
  printf '%s\n' "${f#"$BUNDLE"/}" >> "$TMP/objects"
done < <(find "$BUNDLE" -type f | sort)

OBJ_COUNT="$(wc -l < "$TMP/objects" | tr -d ' ')"
[ "$OBJ_COUNT" -gt 0 ] || { echo "no ELF/Mach-O objects found under ${BUNDLE} — refusing to report a verdict" >&2; exit 2; }

# ── Shipped-object identity ──────────────────────────────────────────
# Every object we redistribute must be one we decided to redistribute. The
# allowlist is the built module set of the pinned PostgreSQL + pgvector +
# pg_partman; an object not on it is an extension nobody reviewed.
if [ "$BLESS" -eq 1 ]; then
  # --bless-modules is the only path that can widen what this gate accepts, so
  # it refuses to launder a violation: an object whose NAME already classifies
  # as copyleft (postgis, citus, timescaledb, a vendored libreadline) is never
  # written to the allowlist. Blessing records "we reviewed this module and it
  # is part of the pinned PostgreSQL/pgvector/pg_partman build" — it is not a
  # mechanism for silencing a red gate.
  : >| "$TMP/bless"
  while IFS= read -r rel; do
    bn="$(normalise "$rel")"
    IFS='|' read -r blic bcls _ <<< "$(classify "$bn" "${BUNDLE}/${rel}")"
    if [ "$bcls" = COPYLEFT ]; then
      echo "refusing to bless ${rel}: classifies as ${blic} (COPYLEFT)" >&2
      exit 1
    fi
    # Already classified by name — no allowlist entry needed.
    [ "$bcls" = UNKNOWN ] || continue
    printf '%s\n' "$bn" >> "$TMP/bless"
  done < "$TMP/objects"
  {
    echo "# Modules shipped in a Sky PostgreSQL bundle. Generated by"
    echo "#   scripts/skydb/scan-bundle-licences.sh <bundle> --bless-modules"
    echo "#"
    echo "# Every entry is PostgreSQL-licensed: core, contrib, pgvector, pg_partman."
    echo "# An object NOT listed here fails the licence gate. That is the point —"
    echo "# adding an extension to the shipped set becomes a reviewable diff here"
    echo "# rather than something that rides along unnoticed in a build script."
    sort -u "$TMP/bless"
  } >| "$MODULE_ALLOWLIST"
  echo "blessed $(command grep -cv '^#' "$MODULE_ALLOWLIST") module names into ${MODULE_ALLOWLIST}"
fi

declare -a ALLOWED_MODULES=()
if [ -f "$MODULE_ALLOWLIST" ]; then
  while IFS= read -r line; do
    case "$line" in ''|\#*) continue ;; esac
    ALLOWED_MODULES+=("$line")
  done < "$MODULE_ALLOWLIST"
fi
in_allowlist() {
  local n="$1" e
  for e in "${ALLOWED_MODULES[@]:-}"; do [ "$e" = "$n" ] && return 0; done
  return 1
}

# ── Walk objects, classify shipped identity + every dependency ───────
COPYLEFT=0; UNKNOWN=0; UNVENDORED=0
: >| "$TMP/violations"

while IFS= read -r rel; do
  obj="${BUNDLE}/${rel}"
  n="$(normalise "$rel")"

  # (a) the shipped object itself
  IFS='|' read -r lic cls note <<< "$(classify "$n" "$obj")"
  if [ "$cls" = UNKNOWN ]; then
    if in_allowlist "$n"; then
      lic="PostgreSQL"; cls="PERMISSIVE"; note="reviewed module of the pinned PostgreSQL/pgvector/pg_partman build"
    fi
  fi
  printf '%s\t%s\t%s\t%s\n' "$rel" "$lic" "$cls" "$note" >> "$TMP/shipped"
  case "$cls" in
    COPYLEFT) COPYLEFT=$((COPYLEFT+1))
      printf 'COPYLEFT  shipped object %s — %s (%s)\n' "$rel" "$lic" "$note" >> "$TMP/violations" ;;
    UNKNOWN)  UNKNOWN=$((UNKNOWN+1))
      printf 'UNKNOWN   shipped object %s — unclassified; add it to the licence table or to %s\n' \
        "$rel" "${MODULE_ALLOWLIST##*/}" >> "$TMP/violations" ;;
  esac

  # (b) each dependency it declares
  while IFS= read -r dep; do
    [ -n "$dep" ] || continue
    # A self-reference (Mach-O LC_ID_DYLIB echoed by otool, or an object naming
    # itself) is not a dependency.
    dn="$(normalise "$dep")"
    [ "$dn" = "$n" ] && continue
    res="$(resolve_dep "$dep")"
    # Classify against the RECORDED dependency path, not the resolution string.
    # The one path-sensitive rule in the table (libiconv) keys on /usr/lib,
    # which a "host-absent:/usr/lib/..." prefix would never match.
    IFS='|' read -r dlic dcls dnote <<< "$(classify "$dn" "$dep")"
    if [ "$dcls" = UNKNOWN ] && in_allowlist "$dn"; then
      dlic="PostgreSQL"; dcls="PERMISSIVE"; dnote="reviewed module of the pinned build"
    fi
    printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$dn" "$dep" "$res" "$dlic" "$dcls" "$dnote" >> "$TMP/deps"
    case "$dcls" in
      COPYLEFT) COPYLEFT=$((COPYLEFT+1))
        printf 'COPYLEFT  %s links %s — %s (%s) [%s]\n' "$rel" "$dep" "$dlic" "$dnote" "$res" >> "$TMP/violations" ;;
      UNKNOWN)  UNKNOWN=$((UNKNOWN+1))
        printf 'UNKNOWN   %s links %s — unclassified dependency; classify it in the licence table [%s]\n' \
          "$rel" "$dep" "$res" >> "$TMP/violations" ;;
    esac
    # A dependency that is neither inside the bundle nor a platform library
    # means the bundle is not self-contained: it will resolve against whatever
    # the host happens to have, which is a licence surface we cannot describe.
    if [ "$dcls" != PLATFORM ] && [ "$dcls" != EXCEPTION ]; then
      case "$res" in
        bundle:*) ;;
        *) UNVENDORED=$((UNVENDORED+1))
           printf 'UNVENDORED %s links %s which is not inside the bundle [%s]\n' "$rel" "$dep" "$res" >> "$TMP/violations" ;;
      esac
    fi
  done < <(deps_of "$obj")
done < "$TMP/objects"

# ── SBOM ─────────────────────────────────────────────────────────────
STATUS=PASS
if [ "$COPYLEFT" -gt 0 ] || [ "$UNKNOWN" -gt 0 ] || [ "$UNVENDORED" -gt 0 ]; then
  STATUS=REJECTED
fi

emit_sbom() {
  local first p l c nt nm raw res
  printf '{\n'
  printf '  "bundle": "%s",\n' "$(basename "$BUNDLE")"
  printf '  "platform": "%s",\n' "$PLATFORM"
  printf '  "postgres_version": "%s",\n' "${PG_VER:-unknown}"
  printf '  "generated_at": "%s",\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  printf '  "generator": "scripts/skydb/scan-bundle-licences.sh",\n'
  printf '  "objects_scanned": %s,\n' "$OBJ_COUNT"
  printf '  "shipped": [\n'
  first=1
  while IFS=$'\t' read -r p l c nt; do
    [ "$first" -eq 1 ] || printf ',\n'
    first=0
    printf '    {"path": "%s", "licence": "%s", "class": "%s", "note": "%s"}' "$p" "$l" "$c" "$nt"
  done < "$TMP/shipped"
  printf '\n  ],\n'
  printf '  "dependencies": [\n'
  first=1
  while IFS=$'\t' read -r nm raw res l c nt; do
    [ "$first" -eq 1 ] || printf ',\n'
    first=0
    printf '    {"name": "%s", "record": "%s", "resolved": "%s", "licence": "%s", "class": "%s", "note": "%s"}' \
      "$nm" "$raw" "$res" "$l" "$c" "$nt"
  done < <(sort -u "$TMP/deps")
  printf '\n  ],\n'
  printf '  "verdict": {"status": "%s", "copyleft": %s, "unknown": %s, "unvendored": %s}\n' \
    "$STATUS" "$COPYLEFT" "$UNKNOWN" "$UNVENDORED"
  printf '}\n'
}

if [ -n "$SBOM_OUT" ]; then
  emit_sbom >| "$SBOM_OUT"
fi

# ── Verdict ──────────────────────────────────────────────────────────
echo
echo "licence gate — $(basename "$BUNDLE") (${PLATFORM})"
echo "  objects scanned      : ${OBJ_COUNT}"
echo "  distinct dependencies: $(sort -u "$TMP/deps" | wc -l | tr -d ' ')"
echo

if [ "$STATUS" = PASS ]; then
  sort -u -t$'\t' -k1,1 "$TMP/deps" | awk -F'\t' '{printf "  %-16s %-32s %s\n", $1, $4, $5}' | sort -u
  echo
  echo "GATE PASS — no GPL, LGPL or AGPL component is shipped or linked."
  exit 0
fi

echo "GATE FAIL — this bundle must not be distributed."
echo
sort -u "$TMP/violations" | sed 's/^/  /'
echo
echo "  copyleft violations : ${COPYLEFT}"
echo "  unclassified        : ${UNKNOWN}"
echo "  unvendored deps     : ${UNVENDORED}"
echo
echo "A copyleft component inside an Apache-2.0 distribution is a licence"
echo "violation, not a build warning. Fix the configure line or the shipped"
echo "set — do not add the component to the licence table to silence this."
exit 1

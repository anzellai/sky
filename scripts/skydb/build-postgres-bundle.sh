#!/usr/bin/env bash
#
# Build a redistributable PostgreSQL bundle from source.
#
# See docs/skydb/embedded-postgres.md — "Licensing and distribution". Sky does
# not redistribute a third party's prebuilt PostgreSQL. Inheriting someone
# else's configure line means inheriting their linked dependencies, and those
# are exactly what the licence gate exists to control. So we compile it here,
# from a pinned tarball with a pinned checksum, with a configure line that is
# reviewable in this file.
#
# The output is a self-contained, relocatable tree:
#
#   <bundle>/bin/    postgres initdb pg_ctl pg_dump pg_dumpall pg_restore   (NO psql)
#   <bundle>/lib/    libpq + every extension module + vendored non-system deps
#   <bundle>/share/  timezone db, extension control/SQL files, base catalogs
#   <bundle>/BUNDLE.json
#
# PostgreSQL relocates itself at runtime (src/port/path.c computes share/ and
# lib/ relative to the running executable), so the tree may be extracted
# anywhere provided bin/, lib/ and share/ keep their relative positions.
#
# Usage:
#   scripts/skydb/build-postgres-bundle.sh [--out DIR] [--jobs N] [--keep-src]
#
# Environment overrides (all pinned by default — see the PINS block):
#   PG_VERSION PGVECTOR_VERSION PG_PARTMAN_VERSION SKYDB_BUILD_DIR
#
set -euo pipefail

# ─────────────────────────────────────────────────────────────────────
# PINS
#
# PostgreSQL 18.6 — 18 is the current stable major (the newest major with
# production status), and .6 is its newest patch release. PostgreSQL's own
# guidance is to always run the latest minor of your major, because minors are
# security and data-corruption fixes only and are never behaviour breaks. The
# pin is exact, not a range: a bundle whose PostgreSQL version drifts between
# builds is not reproducible, and this artifact is what user data lives in.
#
# Bumping the major is a deliberate act — the on-disk data directory format is
# major-version-specific, so a major bump is a pg_upgrade migration for every
# existing --embed deployment, not a dependency refresh.
# ─────────────────────────────────────────────────────────────────────
PG_VERSION="${PG_VERSION:-18.6}"
PG_SHA256="${PG_SHA256:-555610c24d53e4316da5b7d3fc25c279d96856d5e0e23ee308c328c5fa881d9f}"

# pgvector 0.8.6 and pg_partman 5.5.0 — both PostgreSQL Licence, both small.
# Rationale for including exactly these two, and for excluding PostGIS /
# TimescaleDB / Citus on licence grounds, is settled in
# docs/skydb/embedded-postgres.md — "Extensions".
PGVECTOR_VERSION="${PGVECTOR_VERSION:-0.8.6}"
PG_PARTMAN_VERSION="${PG_PARTMAN_VERSION:-5.5.0}"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

# Build outputs NEVER land in the repo. Default to a scratch dir outside it.
BUILD_DIR="${SKYDB_BUILD_DIR:-${TMPDIR:-/tmp}/sky-postgres-build}"
OUT_DIR=""
JOBS=""
KEEP_SRC=0

while [ $# -gt 0 ]; do
  case "$1" in
    --out)      OUT_DIR="$2"; shift 2 ;;
    --jobs)     JOBS="$2"; shift 2 ;;
    --keep-src) KEEP_SRC=1; shift ;;
    -h|--help)  sed -n '2,30p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

# ─────────────────────────────────────────────────────────────────────
# Platform identity. This string names the release artifact and is what
# `sky db provision --embed` (Phase 3) will resolve against.
# ─────────────────────────────────────────────────────────────────────
case "$(uname -s)" in
  Linux)  OS=linux ;;
  Darwin) OS=darwin ;;
  *)
    # Windows needs an MSVC toolchain and PostgreSQL's separate meson/MSVC
    # build path. It is OUT OF SCOPE for this phase — stated rather than
    # silently omitted. See the workflow's matrix comment.
    echo "unsupported platform: $(uname -s) (Windows/MSVC is out of scope)" >&2
    exit 2 ;;
esac
case "$(uname -m)" in
  x86_64|amd64)  ARCH=amd64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 2 ;;
esac
PLATFORM="${OS}-${ARCH}"
BUNDLE_NAME="postgres-${PG_VERSION}-${PLATFORM}"

: "${OUT_DIR:=${BUILD_DIR}/out}"
mkdir -p "$BUILD_DIR" "$OUT_DIR"
BUNDLE="${OUT_DIR}/${BUNDLE_NAME}"

if [ -z "$JOBS" ]; then
  JOBS="$( (command -v nproc >/dev/null && nproc) || sysctl -n hw.ncpu || echo 4 )"
fi

SRC_DIR="${BUILD_DIR}/src"
STAGE="${BUILD_DIR}/stage"      # `make install` prefix; superset of the bundle
PREFIX="/opt/sky/postgres"      # baked-in prefix; overridden by relocation

log() { printf '\033[1;34m[bundle]\033[0m %s\n' "$*"; }
die() { printf '\033[1;31m[bundle] FATAL:\033[0m %s\n' "$*" >&2; exit 1; }

# ─────────────────────────────────────────────────────────────────────
# Disk guard. A PostgreSQL build tree plus staging is ~2.5 GB.
# ─────────────────────────────────────────────────────────────────────
free_mb="$(df -Pm "$BUILD_DIR" | awk 'NR==2 {print $4}')"
[ "${free_mb:-0}" -ge 6000 ] || die "need >=6000 MB free at ${BUILD_DIR}, have ${free_mb} MB"

# ─────────────────────────────────────────────────────────────────────
# Fetch + verify. An unverified tarball would make every downstream licence
# claim unfalsifiable, so a checksum mismatch is fatal, never a warning.
# ─────────────────────────────────────────────────────────────────────
TARBALL="${BUILD_DIR}/postgresql-${PG_VERSION}.tar.bz2"
if [ ! -f "$TARBALL" ]; then
  log "fetching PostgreSQL ${PG_VERSION}"
  curl -fsSL --retry 3 --retry-delay 5 \
    "https://ftp.postgresql.org/pub/source/v${PG_VERSION}/postgresql-${PG_VERSION}.tar.bz2" \
    -o "${TARBALL}.part"
  mv -f "${TARBALL}.part" "$TARBALL"
fi

log "verifying checksum"
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$TARBALL" | awk '{print $1}')"
else
  actual="$(shasum -a 256 "$TARBALL" | awk '{print $1}')"
fi
[ "$actual" = "$PG_SHA256" ] \
  || die "checksum mismatch for postgresql-${PG_VERSION}.tar.bz2
  expected ${PG_SHA256}
  actual   ${actual}"

rm -rf "$SRC_DIR" "$STAGE"
mkdir -p "$SRC_DIR" "$STAGE"
log "extracting"
tar -xjf "$TARBALL" -C "$SRC_DIR" --strip-components=1

# ─────────────────────────────────────────────────────────────────────
# Dependency discovery. On macOS the permissive deps come from Homebrew and
# are NOT on the default search path; on Linux they come from the distro's
# -dev packages and are.
# ─────────────────────────────────────────────────────────────────────
CPPFLAGS_EXTRA=""
LDFLAGS_EXTRA=""
PKG_CONFIG_PATH_EXTRA=""

if [ "$OS" = darwin ]; then
  brew_prefix="$(brew --prefix 2>/dev/null || echo /opt/homebrew)"
  # icu4c is versioned+keg-only in Homebrew; resolve whichever is installed
  # rather than hardcoding a version that will rot.
  icu_prefix="$(brew --prefix icu4c 2>/dev/null || true)"
  if [ -z "$icu_prefix" ]; then
    icu_prefix="$(printf '%s\n' "${brew_prefix}"/opt/icu4c* | sort -V | tail -1)"
  fi
  brew_deps=("$(brew --prefix openssl@3)" "$icu_prefix")
  for d in lz4 zstd zlib libxml2; do
    p="$(brew --prefix "$d" 2>/dev/null || true)"
    [ -n "$p" ] && [ -d "$p" ] && brew_deps+=("$p")
  done
  for p in "${brew_deps[@]}"; do
    [ -d "$p/lib/pkgconfig" ] && PKG_CONFIG_PATH_EXTRA="${p}/lib/pkgconfig:${PKG_CONFIG_PATH_EXTRA}"
    [ -d "$p/include" ] && CPPFLAGS_EXTRA="${CPPFLAGS_EXTRA} -I${p}/include"
    [ -d "$p/lib" ]     && LDFLAGS_EXTRA="${LDFLAGS_EXTRA} -L${p}/lib"
  done
  # Prefer Homebrew's pkgconf. A `pkg-config` earlier on PATH may be a wrapper
  # that pins its own search path and ignores PKG_CONFIG_PATH entirely (Nix
  # ships exactly such a wrapper), which makes every keg-only dependency —
  # icu4c, openssl@3, libxml2 — invisible to configure for no obvious reason.
  if [ -x "${brew_prefix}/bin/pkg-config" ]; then
    PKG_CONFIG="${brew_prefix}/bin/pkg-config"; export PKG_CONFIG
  fi
  # Homebrew's bison; macOS ships Bison 2.3, which is exactly PostgreSQL's
  # documented floor and has bitten builds before. Prefer a modern one.
  if [ -x "$(brew --prefix bison 2>/dev/null)/bin/bison" ]; then
    PATH="$(brew --prefix bison)/bin:$PATH"; export PATH
  fi
fi
export PKG_CONFIG_PATH="${PKG_CONFIG_PATH_EXTRA}${PKG_CONFIG_PATH:-}"

# ─────────────────────────────────────────────────────────────────────
# THE CONFIGURE LINE
#
# Every exclusion below is a licence decision or a dependency-surface
# decision, not a build convenience. Read docs/skydb/embedded-postgres.md —
# "Licensing and distribution" before changing any of them.
#
#   --without-readline   GNU readline is GPL-3.0. This is the single sharpest
#                        licence edge in a stock PostgreSQL build.
#   --without-systemd    libsystemd is LGPL-2.1.
#   --disable-nls        NLS links GNU libintl/gettext (LGPL-2.1). PostgreSQL
#                        defaults NLS off, but it is asserted here rather than
#                        left to the default, because a default is not a
#                        guarantee and this one carries an LGPL dependency.
#   --without-perl
#   --without-python     PL/Perl, PL/Python and PL/Tcl. Excluded for
#   --without-tcl        dependency surface: each drags an entire language
#                        runtime into a bundle that exists to be small.
#   --with-ssl=openssl   OpenSSL 3.x ONLY (Apache-2.0). Pre-3.0 OpenSSL is
#                        under the OpenSSL/SSLeay dual licence, which has a
#                        known advertising-clause incompatibility with GPL and
#                        is simply messier to reason about. Enforced below.
#   --without-gssapi     MIT Kerberos is permissive, but it is a large surface
#   --without-ldap       we do not need: --embed talks over a unix socket to a
#   --without-pam        cluster it started itself. Fewer links, smaller gate.
#   --without-bonjour
#   --without-selinux
#   --disable-rpath      We install our OWN rpath ($ORIGIN / @loader_path) at
#                        bundle-assembly time. Letting configure bake absolute
#                        build-host library paths into a redistributable
#                        artifact is exactly the bug that makes bundles work on
#                        the build machine and nowhere else.
#
# Deliberately NOT passed: --with-uuid. PostgreSQL has had a built-in
# gen_random_uuid() since 13, so contrib/uuid-ossp buys nothing and would add
# a dependency.
#
# LINKED, permissive, allowed by the doc: zlib (Zlib), lz4 (BSD-2-Clause),
# zstd (BSD-3-Clause/GPL-2.0 dual — we take BSD-3-Clause), ICU (Unicode-3.0),
# libxml2 (MIT).
# ─────────────────────────────────────────────────────────────────────
CONFIGURE_ARGS=(
  "--prefix=${PREFIX}"
  --without-readline
  --without-systemd
  --disable-nls
  --without-perl
  --without-python
  --without-tcl
  --with-ssl=openssl
  --with-zlib
  --with-lz4
  --with-zstd
  --with-icu
  --with-libxml
  --without-gssapi
  --without-ldap
  --without-pam
  --without-bonjour
  --without-selinux
  --disable-rpath
)

log "configuring PostgreSQL ${PG_VERSION} for ${PLATFORM}"
(
  cd "$SRC_DIR"
  # CPPFLAGS/LDFLAGS are SET, not appended to. Inheriting the caller's flags
  # makes the bundle a function of whatever happened to be exported in the
  # invoking shell — a developer machine with LLVM and OpenJDK on CPPFLAGS
  # produces a different link than CI does, from identical source. The whole
  # point of building from source with a pinned configure line is that the
  # licence surface is determined here and nowhere else.
  CPPFLAGS="${CPPFLAGS_EXTRA}" \
  LDFLAGS="${LDFLAGS_EXTRA}" \
  ./configure "${CONFIGURE_ARGS[@]}"
)

# OpenSSL 3.x only — assert on what configure actually found, not on what we
# asked for. The doc says "OpenSSL is 3.x only"; a build host with OpenSSL 1.1
# development headers would otherwise satisfy --with-ssl=openssl silently.
ssl_ver="$(command grep -oE 'OpenSSL [0-9]+\.[0-9]+' "${SRC_DIR}/config.log" | head -1 || true)"
if command -v openssl >/dev/null 2>&1; then
  log "openssl on PATH: $(openssl version)"
fi
log "configure recorded: ${ssl_ver:-<not recorded>}"

# `world-bin` is "everything except the documentation", which is exactly the
# shipped set plus contrib. Building contrib separately would be redundant.
log "building (make -j${JOBS}) — this takes 10-20 minutes"
make -C "$SRC_DIR" -j"$JOBS" world-bin

log "installing to staging prefix"
make -C "$SRC_DIR" DESTDIR="$STAGE" install-world-bin >/dev/null

STAGED="${STAGE}${PREFIX}"
PG_CONFIG="${STAGED}/bin/pg_config"
[ -x "$PG_CONFIG" ] || die "pg_config missing from staging install"

# ─────────────────────────────────────────────────────────────────────
# Third-party extensions. Both build against the staged tree via PG_CONFIG and
# install into it, so they land in the same lib/ and share/ the bundle copies.
# ─────────────────────────────────────────────────────────────────────
# NOTE: no DESTDIR here, deliberately.
#
# PostgreSQL's pg_config is RELOCATABLE — it reports paths relative to its own
# location, so the staged pg_config already answers `--pkglibdir` with
# "<stage>/opt/sky/postgres/lib". Adding DESTDIR=<stage> on top prepends the
# stage a second time and PGXS installs into "<stage><stage>/opt/sky/...".
#
# That failure is SILENT: every make exits 0, the bundle assembles, and the
# script reports a plausible size — with pgvector and pg_partman simply absent.
# It is why the completeness assertion below exists rather than trusting exit
# codes.
build_ext() {
  local name="$1" url="$2" dir="${BUILD_DIR}/ext-$1"
  log "building ${name}"
  rm -rf "$dir"; mkdir -p "$dir"
  curl -fsSL --retry 3 "$url" | tar -xz -C "$dir" --strip-components=1
  make -C "$dir" -j"$JOBS" PG_CONFIG="$PG_CONFIG" >/dev/null
  make -C "$dir" PG_CONFIG="$PG_CONFIG" install >/dev/null
}

build_ext pgvector \
  "https://github.com/pgvector/pgvector/archive/refs/tags/v${PGVECTOR_VERSION}.tar.gz"
build_ext pg_partman \
  "https://github.com/pgpartman/pg_partman/archive/refs/tags/v${PG_PARTMAN_VERSION}.tar.gz"

# ─────────────────────────────────────────────────────────────────────
# Assemble the bundle.
# ─────────────────────────────────────────────────────────────────────
log "assembling bundle"
rm -rf "$BUNDLE"
mkdir -p "$BUNDLE/bin" "$BUNDLE/lib" "$BUNDLE/share"

# The shipped executable set. `--embed` needs a server it can initialise,
# start, stop, and back up. That is all five of these and nothing else.
#
#   ┌──────────────────────────────────────────────────────────────────┐
#   │ psql IS DELIBERATELY ABSENT. DO NOT ADD IT.                      │
#   │                                                                  │
#   │ A stock psql links GNU readline (GPL-3.0), which would put a     │
#   │ GPL-linked binary inside an Apache-2.0 distribution. `--embed`   │
#   │ never needs an interactive client: the app speaks the wire       │
#   │ protocol, not the REPL. This is a settled decision recorded in   │
#   │ docs/skydb/embedded-postgres.md — "Licensing and distribution",  │
#   │ not an oversight to be helpfully corrected later.                │
#   │                                                                  │
#   │ The licence gate would catch a readline-linked psql on Linux,    │
#   │ but do not rely on that: this build passes --without-readline,   │
#   │ so a psql added here would link cleanly and ship a binary we     │
#   │ never decided to ship.                                           │
#   └──────────────────────────────────────────────────────────────────┘
# `pg_dumpall` is here because roles are CLUSTER-wide while `pg_dump` is
# per-database. On a shared cluster (database-per-app, role-per-app) a pg_dump
# of one database restores into a cluster that has none of its roles, and fails
# on the first `OWNER TO` — an un-restorable backup, which is the worst kind,
# because it looks like a backup right up until you need it. `pg_dumpall
# --roles-only` is what makes the dump restorable.
#
# It links libpq, not readline, so it costs nothing on licence grounds — and
# the gate walks it either way rather than taking that on trust.
#
# `psql` is NOT here and must not be added: it links GNU readline (GPL-3.0),
# and the licence gate does NOT backstop that decision — under
# `--without-readline` psql links cleanly and would pass. This list is the
# enforcement. See docs/skydb/embedded-postgres.md.
SHIPPED_BINARIES=(postgres initdb pg_ctl pg_dump pg_dumpall pg_restore)
for b in "${SHIPPED_BINARIES[@]}"; do
  [ -x "${STAGED}/bin/${b}" ] || die "expected binary missing from install: ${b}"
  command cp -f "${STAGED}/bin/${b}" "${BUNDLE}/bin/${b}"
done

# Everything under lib/: libpq, and every extension module (.so/.dylib). These
# are dlopen()ed by the server at runtime and are never linked into `postgres`
# — which is precisely why the licence gate must walk them individually.
command cp -Rf "${STAGED}/lib/." "${BUNDLE}/lib/"
find "${BUNDLE}/lib" -name '*.a' -delete          # static archives are not shipped
rm -rf "${BUNDLE}/lib/pkgconfig"

# share/: timezone database, extension .control + .sql files, base catalogs,
# postgres.bki. initdb cannot run without these.
command cp -Rf "${STAGED}/share/." "${BUNDLE}/share/"
rm -rf "${BUNDLE}/share/doc" "${BUNDLE}/share/man"

# ─────────────────────────────────────────────────────────────────────
# Vendor + relocate non-system dependencies.
#
# Without this the bundle links absolute build-host paths (Homebrew's
# /opt/homebrew/opt/... , a runner's /usr/lib/x86_64-linux-gnu/...) and works
# only on the machine that built it. Vendoring also puts every non-platform
# dependency INSIDE the bundle, where the licence gate can see it — a
# dependency resolved from the host at runtime is one the SBOM cannot honestly
# describe.
# ─────────────────────────────────────────────────────────────────────
is_system_lib() {
  local base; base="$(basename "$1")"
  if [ "$OS" = darwin ]; then
    # macOS system libs live in the dyld shared cache (/usr/lib, /System/Library)
    # — OS-provided, and there is no on-disk file to copy out. Homebrew deps
    # under /opt are real files and get vendored. Matches the licence gate's
    # darwin PLATFORM classification (incl. its /usr/lib libz + libiconv rules).
    case "$1" in
      /usr/lib/*|/System/Library/*) return 0 ;;
    esac
    return 1
  fi
  # Linux: /usr/lib and /lib hold BOTH the base toolchain (glibc, the dynamic
  # loader, libgcc_s/libstdc++) AND optional libraries (openssl, icu, xml2,
  # zstd, lz4, zlib) that scan-bundle-licences.sh classifies PERMISSIVE and
  # REQUIRES vendored. The old broad `/usr/lib/*|/lib/*` rule treated those as
  # system and skipped them, so the bundle shipped non-self-contained and the
  # SBOM gate rejected it. Classify by basename against the gate's PLATFORM set
  # (glibc family + loader + GCC runtime); vendor everything else.
  case "$base" in
    libc.so*|libm.so*|libdl.so*|libpthread.so*|librt.so*|libutil.so*|libresolv.so*|libnsl.so*|libanl.so*) return 0 ;;
    ld-linux*.so*|ld.so*|ld64.so*) return 0 ;;
    libgcc_s.so*|libstdc++.so*) return 0 ;;
    *) return 1 ;;
  esac
}

if [ "$OS" = darwin ]; then
  # Mach-O: copy each non-system dependency into lib/, rewrite the referring
  # load command to @rpath, and give every object an rpath that resolves
  # inside the bundle. Iterated to a fixpoint because vendored dylibs have
  # dependencies of their own.
  # Directories a vendored library originally came from. A Homebrew dylib
  # records its own siblings as @loader_path/<name> (ICU does exactly this for
  # libicudata), so those records carry no directory to copy FROM — they must
  # be resolved against the origin of the library that names them. Missing this
  # produces a bundle that assembles cleanly and dies at exec time with
  # "Library not loaded: @loader_path/libicudata.78.dylib".
  VENDOR_SEARCH_DIRS=()
  for p in "${brew_deps[@]:-}"; do
    [ -n "$p" ] && [ -d "$p/lib" ] && VENDOR_SEARCH_DIRS+=("$(cd "$p/lib" && pwd -P)")
  done
  remember_dir() {
    local d="$1" e
    for e in "${VENDOR_SEARCH_DIRS[@]:-}"; do [ "$e" = "$d" ] && return 0; done
    VENDOR_SEARCH_DIRS+=("$d")
  }
  find_in_search_dirs() {
    local base="$1" d
    for d in "${VENDOR_SEARCH_DIRS[@]:-}"; do
      [ -n "$d" ] && [ -f "${d}/${base}" ] && { printf '%s' "${d}/${base}"; return 0; }
    done
    return 1
  }

  vendor_pass() {
    local changed=0 obj dep base src
    while IFS= read -r obj; do
      file "$obj" | command grep -q 'Mach-O' || continue
      while IFS= read -r dep; do
        [ -n "$dep" ] || continue
        base="$(basename "$dep")"
        case "$dep" in
          @*) # Already relative. Only actionable if we do not have it yet.
              [ -f "${BUNDLE}/lib/${base}" ] && continue
              src="$(find_in_search_dirs "$base")" || continue ;;
          *)  is_system_lib "$dep" && continue
              src="$dep" ;;
        esac
        if [ ! -f "${BUNDLE}/lib/${base}" ]; then
          [ -f "$src" ] || continue
          command cp -f "$src" "${BUNDLE}/lib/${base}"
          chmod u+w "${BUNDLE}/lib/${base}"
          install_name_tool -id "@rpath/${base}" "${BUNDLE}/lib/${base}" 2>/dev/null || true
          remember_dir "$(cd "$(dirname "$src")" && pwd -P)"
          changed=1
        fi
        install_name_tool -change "$dep" "@rpath/${base}" "$obj" 2>/dev/null || true
      done < <(otool -L "$obj" | tail -n +2 | awk '{print $1}')
    done < <(find "$BUNDLE" -type f \( -perm -u+x -o -name '*.dylib' -o -name '*.so' \) | sort -u)
    return $changed
  }
  for _ in 1 2 3 4 5; do vendor_pass || true; done

  while IFS= read -r obj; do
    file "$obj" | command grep -q 'Mach-O' || continue
    case "$obj" in
      "${BUNDLE}/bin/"*) install_name_tool -add_rpath "@loader_path/../lib" "$obj" 2>/dev/null || true ;;
      *)                 install_name_tool -add_rpath "@loader_path"        "$obj" 2>/dev/null || true ;;
    esac
  done < <(find "$BUNDLE" -type f \( -perm -u+x -o -name '*.dylib' -o -name '*.so' \) | sort -u)

  # Ad-hoc re-sign: install_name_tool invalidates the signature Apple's
  # linker attaches on arm64, and an invalid signature is a hard load failure
  # (SIGKILL, not a warning) on Apple silicon.
  if command -v codesign >/dev/null 2>&1; then
    while IFS= read -r obj; do
      file "$obj" | command grep -q 'Mach-O' || continue
      codesign --force --sign - --timestamp=none "$obj" >/dev/null 2>&1 || true
    done < <(find "$BUNDLE" -type f \( -perm -u+x -o -name '*.dylib' -o -name '*.so' \) | sort -u)
  fi
else
  # ELF: same idea, via DT_NEEDED + patchelf.
  command -v patchelf >/dev/null 2>&1 || die "patchelf is required to relocate ELF bundles"
  for _ in 1 2 3 4 5; do
    while IFS= read -r obj; do
      head -c 4 "$obj" | command grep -q ELF || continue
      while IFS= read -r dep; do
        [ -n "$dep" ] || continue
        resolved="$(ldd "$obj" 2>/dev/null | awk -v d="$dep" '$1==d {print $3}' | head -1)"
        [ -n "$resolved" ] && [ -f "$resolved" ] || continue
        is_system_lib "$resolved" && continue
        [ -f "${BUNDLE}/lib/${dep}" ] && continue
        command cp -f "$resolved" "${BUNDLE}/lib/${dep}"
        chmod u+w "${BUNDLE}/lib/${dep}"
      done < <(objdump -p "$obj" 2>/dev/null | awk '/NEEDED/ {print $2}')
    done < <(find "$BUNDLE/bin" "$BUNDLE/lib" -type f | sort -u)
  done
  while IFS= read -r obj; do
    head -c 4 "$obj" | command grep -q ELF || continue
    case "$obj" in
      "${BUNDLE}/bin/"*) patchelf --set-rpath '$ORIGIN/../lib' "$obj" 2>/dev/null || true ;;
      *)                 patchelf --set-rpath '$ORIGIN'        "$obj" 2>/dev/null || true ;;
    esac
  done < <(find "$BUNDLE/bin" "$BUNDLE/lib" -type f | sort -u)
fi

# ─────────────────────────────────────────────────────────────────────
# COMPLETENESS + LIVENESS ASSERTIONS
#
# Exit codes are not evidence. The DESTDIR double-prefix bug documented at
# build_ext() produced a green build, a plausible bundle and a plausible size,
# with both third-party extensions missing — because `make install` succeeded
# at installing them somewhere nobody looked. Likewise a mis-relocated bundle
# links and assembles fine and only fails when something first tries to exec
# it. So assert the artifact has what we said it has, and that it runs.
#
# DLSUFFIX differs by platform (.dylib on macOS, .so on Linux), so probe both.
# ─────────────────────────────────────────────────────────────────────
have_module() {
  [ -f "${BUNDLE}/lib/$1.so" ] || [ -f "${BUNDLE}/lib/$1.dylib" ]
}

log "asserting bundle completeness"
missing=()
# Third-party extensions the doc promises: pgvector and pg_partman.
for m in vector pg_partman_bgw; do have_module "$m" || missing+=("lib/${m}"); done
# A representative slice of contrib. If these are present the contrib install
# landed; if the whole install silently went elsewhere, they will not be.
for m in pg_trgm pgcrypto hstore citext btree_gin btree_gist pg_stat_statements postgres_fdw; do
  have_module "$m" || missing+=("lib/${m}")
done
# Extension SQL/control files are separate from the module and installed by a
# separate rule — a bundle with vector.so and no vector.control cannot
# CREATE EXTENSION.
for c in vector.control pg_partman.control pg_trgm.control pgcrypto.control; do
  [ -f "${BUNDLE}/share/extension/${c}" ] || missing+=("share/extension/${c}")
done
# initdb cannot run without these.
[ -f "${BUNDLE}/share/postgres.bki" ] || missing+=("share/postgres.bki")
[ -d "${BUNDLE}/share/timezone" ]     || missing+=("share/timezone")
if [ "${#missing[@]}" -gt 0 ]; then
  die "bundle is incomplete — missing:$(printf '\n    %s' "${missing[@]}")"
fi

log "smoke-testing relocated binaries"
for b in "${SHIPPED_BINARIES[@]}"; do
  "${BUNDLE}/bin/${b}" --version >/dev/null 2>&1 \
    || die "${b} does not run from the bundle — relocation is broken (try: ${BUNDLE}/bin/${b} --version)"
done

# ─────────────────────────────────────────────────────────────────────
# Bundle manifest. Records what was pinned and how it was configured, so an
# artifact downloaded a year from now can be audited without this repo.
# ─────────────────────────────────────────────────────────────────────
{
  printf '{\n'
  printf '  "postgres_version": "%s",\n' "$PG_VERSION"
  printf '  "postgres_sha256": "%s",\n'  "$PG_SHA256"
  printf '  "pgvector_version": "%s",\n' "$PGVECTOR_VERSION"
  printf '  "pg_partman_version": "%s",\n' "$PG_PARTMAN_VERSION"
  printf '  "platform": "%s",\n' "$PLATFORM"
  printf '  "built_at": "%s",\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  printf '  "psql_excluded": "GPL-3.0 readline linkage; see docs/skydb/embedded-postgres.md",\n'
  printf '  "configure_args": ['
  sep=''
  for a in "${CONFIGURE_ARGS[@]}"; do printf '%s"%s"' "$sep" "$a"; sep=', '; done
  printf ']\n}\n'
} >| "${BUNDLE}/BUNDLE.json"

if [ "$KEEP_SRC" -eq 0 ]; then
  log "cleaning build tree"
  rm -rf "$SRC_DIR" "$STAGE" "${BUILD_DIR}"/ext-*
fi

log "bundle ready: ${BUNDLE}"
du -sh "$BUNDLE" | awk '{print "[bundle] size: " $1}'
echo "$BUNDLE"
